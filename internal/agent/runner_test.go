package agent

import (
	"encoding/json"
	"mockatoms/internal/core"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunnerAllowsFiveRepairsAfterInitialFailure(t *testing.T) {
	var jobs atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var job struct{ ID, Digest string }
		if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
			http.Error(w, "invalid job", http.StatusBadRequest)
			return
		}
		jobs.Add(1)
		_ = json.NewEncoder(w).Encode(core.Report{
			ID: job.ID, Digest: job.Digest, Status: "failed",
			Checks: []core.Check{{Name: "expected_stdout", Status: "failed"}},
		})
	}))
	defer server.Close()

	e, p := testEngine(t, nil)
	e.Config.RunnerURL, e.Config.RunnerToken = server.URL, "test"
	s := scope{Owner: "alice", Project: p.ID, Task: "task", Run: "run", Frame: "frame", Epoch: 1}
	p, err := e.Store.Mutate(s.Owner, p.ID, nil, func(p *core.Project) error {
		p.Epoch, p.ActiveRun = s.Epoch, s.Run
		p.Runs[s.Run] = &core.Run{ID: s.Run, Status: "running"}
		p.Tasks[s.Task] = &core.Task{
			Steps:  map[string]core.Step{},
			Frames: map[string]*core.Frame{s.Frame: {ID: s.Frame, Role: "Alex"}},
		}
		p.Reports = []core.Report{{Kind: "run", Status: "failed", Details: []byte(`{"run_id":"previous"}`)}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"kind": "script", "entry": "main.js", "expected_stdout": "ok"}
	for attempt := 1; attempt <= 7; attempt++ {
		c := call("run_project", args).ToolCalls[0]
		_, err := e.executeRunner(t.Context(), s, c, args, p)
		if attempt == 7 {
			if err == nil || !strings.Contains(err.Error(), "5 次上限") || jobs.Load() != 6 {
				t.Fatalf("seventh attempt must be blocked, jobs=%d, err=%v", jobs.Load(), err)
			}
			break
		}
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		p, err = e.Store.Get(s.Owner, s.Project)
		if err != nil {
			t.Fatal(err)
		}
	}
}
