// repair-replay runs an explicitly selected historical task in an isolated
// database. Only model quota reservations touch the configured quota database.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"mockatoms/internal/agent"
	"mockatoms/internal/config"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"mockatoms/internal/store"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

type recorder struct {
	base      http.RoundTripper
	directory string
	n         atomic.Int32
}

func (r *recorder) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.GetBody != nil {
		body, err := request.GetBody()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(body)
		body.Close()
		if err != nil {
			return nil, err
		}
		if err = os.WriteFile(filepath.Join(r.directory, fmt.Sprintf("request-%03d.json", r.n.Add(1))), data, 0600); err != nil {
			return nil, err
		}
	}
	return r.base.RoundTrip(request)
}
func main() {
	sourcePath := flag.String("source-db", "", "historical source database")
	quotaPath := flag.String("quota-db", "", "original quota ledger (defaults to source-db)")
	projectID := flag.String("project", "", "historical project ID")
	owner := flag.String("owner", "", "historical owner")
	directory := flag.String("output", "", "new isolated output directory")
	steeringPath := flag.String("steering", "", "explicit regression input file")
	keepRoot := flag.Bool("keep-root", false, "continue the existing coordinator and delegation history")
	maxCalls := flag.Int("max-calls", 24, "maximum actual model calls")
	flag.Parse()
	if *sourcePath == "" || *projectID == "" || *owner == "" || *directory == "" || (*steeringPath == "" && !*keepRoot) || *maxCalls < 1 || *maxCalls > 48 {
		log.Fatal("missing or invalid replay arguments")
	}
	if _, err := os.Stat(*directory); !os.IsNotExist(err) {
		log.Fatal("output directory must not already exist")
	}
	if err := os.MkdirAll(*directory, 0700); err != nil {
		log.Fatal(err)
	}
	source, err := store.Open(*sourcePath)
	if err != nil {
		log.Fatal(err)
	}
	defer source.Close()
	quota := source
	if *quotaPath != "" && *quotaPath != *sourcePath {
		quota, err = store.Open(*quotaPath)
		if err != nil {
			log.Fatal(err)
		}
		defer quota.Close()
	}
	p, err := source.Get(*owner, *projectID)
	if err != nil {
		log.Fatal(err)
	}
	original := core.JSON(p)
	if p.ActiveRun != "" || p.Tasks[p.CurrentTask] == nil || p.Tasks[p.CurrentTask].RunnerAction != nil {
		log.Fatal("source must be idle with no unconfirmed external job")
	}
	var input []byte
	if *steeringPath != "" {
		input, err = os.ReadFile(*steeringPath)
		if err != nil {
			log.Fatal(err)
		}
	}
	db, err := store.Open(filepath.Join(*directory, "atoms.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	created, err := db.Create(*owner, "历史任务验收副本")
	if err != nil {
		log.Fatal(err)
	}
	blobs := map[string][]byte{}
	collect := func(m core.Manifest) {
		for _, file := range m {
			if _, ok := blobs[file.Hash]; ok {
				continue
			}
			data, err := source.Blob(*owner, file.Hash)
			if err != nil {
				log.Fatalf("read historical blob %s: %v", file.Hash, err)
			}
			blobs[file.Hash] = data
		}
	}
	collect(p.Files)
	for _, task := range p.Tasks {
		collect(task.Base)
		for _, cp := range task.Checkpoints {
			collect(cp.Manifest)
		}
	}
	for _, v := range p.Versions {
		collect(v.Manifest)
	}
	_, err = db.Mutate(*owner, created.ID, blobs, func(copy *core.Project) error {
		*copy = replayProject(p, created.ID, string(input), *keepRoot)
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	c := config.Load()
	c.MaxCalls = *maxCalls
	c.MaxConcurrent = 1
	c.RunSeconds = min(c.RunSeconds, 900)
	client := model.New(c)
	client.HTTP.Transport = &recorder{base: client.HTTP.Transport, directory: *directory}
	engine := agent.New(db, c, client)
	engine.Quota = quota
	defer engine.Shutdown()
	started, err := engine.Start(*owner, created.ID, "", "build", core.ID(), "continue", "")
	if err != nil {
		log.Fatal(err)
	}
	lastCalls := -1
	for {
		state, err := db.Get(*owner, created.ID)
		if err != nil {
			log.Fatal(err)
		}
		r := state.Runs[started.ActiveRun]
		if r.Calls != lastCalls {
			fmt.Printf("calls=%d tools=%d status=%s\n", r.Calls, r.Tools, r.Status)
			lastCalls = r.Calls
		}
		if state.ActiveRun == "" {
			current, err := source.Get(*owner, *projectID)
			if err != nil {
				log.Fatal(err)
			}
			unchanged := core.JSON(current) == original
			result := map[string]any{"source_project": *projectID, "copy_project": created.ID, "run": r, "source_unchanged": unchanged, "files": state.Files, "reports": state.Reports, "handoff": state.Tasks[state.CurrentTask].LastHandoff, "requests": lastCalls, "quota": "original database ledger"}
			encoded, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				log.Fatal(err)
			}
			writeFile(filepath.Join(*directory, "result.json"), encoded)
			writeFile(filepath.Join(*directory, "project.json"), []byte(core.JSON(state)))
			for name, file := range state.Files {
				if !file.Binary {
					continue
				}
				data, err := db.Blob(*owner, file.Hash)
				if err != nil {
					log.Fatal(err)
				}
				dest := filepath.Join(*directory, "outputs", filepath.FromSlash(name))
				writeFile(dest, data)
			}
			fmt.Printf("finished status=%s calls=%d tools=%d source_unchanged=%t\n", r.Status, r.Calls, r.Tools, unchanged)
			if !unchanged {
				log.Fatal("source project unexpectedly changed")
			}
			return
		}
		time.Sleep(time.Second)
	}
}

func replayProject(source *core.Project, id, input string, keepRoot bool) core.Project {
	p := core.Clone(*source)
	p.ID, p.Title, p.ActiveRun = id, "历史任务验收副本", ""
	t := p.Tasks[p.CurrentTask]
	if input != "" {
		messageID := p.Message("user", input, "steering")
		t.InstructionMessages = append(t.InstructionMessages, messageID)
	}
	if !keepRoot {
		t.Status, t.Mode, t.Question = "failed", "build", nil
		t.Root = core.ID()
		t.Frames[t.Root] = &core.Frame{ID: t.Root, Role: "Mike", Goal: t.Goal, Messages: []core.ModelMessage{{Role: "user", Content: t.Goal}}}
	}
	return p
}

func writeFile(name string, data []byte) {
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(name, data, 0600); err != nil {
		log.Fatal(err)
	}
}
