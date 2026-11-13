package httpapi

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"mockatoms/internal/agent"
	"mockatoms/internal/config"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"mockatoms/internal/store"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBinaryDownloadAndZIPPreserveSavedBytes(t *testing.T) {
	testBinaryDownload(t, []byte{0, 255, 128, 0, 13, 10, 254, 7}, "output.bin")
}

func TestCollectedArtifactDownloadAfterContinue(t *testing.T) {
	fixture := os.Getenv("ATOMS_REPAIR_BINARY_FIXTURE")
	if fixture == "" {
		t.Skip("set ATOMS_REPAIR_BINARY_FIXTURE to an actual collected output")
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	testBinaryDownload(t, data, filepath.Base(fixture))
}

type pauseBinaryModel struct{}

func (pauseBinaryModel) Generate(context.Context, []core.ModelMessage, []model.Tool) (core.ModelMessage, core.Usage, error) {
	return core.ModelMessage{Role: "assistant", ToolCalls: []core.Call{{ID: "pause", Type: "function", Function: core.Function{Name: "ask_user", Arguments: `{"question":"保留当前产物，等待后续指令"}`}}}}, core.Usage{}, nil
}

func testBinaryDownload(t *testing.T, data []byte, name string) {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "binary.db")
	db, err := store.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	p, _ := db.Create("owner", "binary")
	hash := core.Hash(data)
	_, err = db.Mutate("owner", p.ID, map[string][]byte{hash: data}, func(p *core.Project) error {
		p.Files[name] = core.File{Hash: hash, Size: len(data), Binary: true, Kind: "artifact"}
		p.CurrentTask = "task"
		p.Tasks["task"] = &core.Task{ID: "task", Root: "root", Status: "stopped", Mode: "build", Goal: "保留已收集的产物", Base: core.Clone(p.Files), Steps: map[string]core.Step{}, Frames: map[string]*core.Frame{"root": {ID: "root", Role: "Mike"}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	engine := agent.New(db, config.Config{MaxCalls: 2, MaxTools: 5, MaxConcurrent: 1, RunSeconds: 5}, pauseBinaryModel{})
	defer engine.Shutdown()
	if _, err := engine.Start("owner", p.ID, "", "build", "continue", "continue", ""); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Second); ; {
		state, err := db.Get("owner", p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state.ActiveRun == "" {
			if state.Tasks["task"].Status != "waiting_input" || state.Files[name].Hash != hash {
				t.Fatal("continue lost persisted artifact", state.Tasks["task"].Status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("continue did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	server := &Server{Store: db}
	request := func(route string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", route, nil)
		r.SetPathValue("id", p.ID)
		r = r.WithContext(context.WithValue(r.Context(), ctxKey("owner"), "owner"))
		w := httptest.NewRecorder()
		if route == "/export" {
			server.export(w, r)
		} else {
			server.file(w, r)
		}
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		return w
	}
	if got := request("/file?path=" + url.QueryEscape(name) + "&download=1").Body.Bytes(); !bytes.Equal(got, data) {
		t.Fatal("download corrupted bytes")
	}
	body := request("/export").Body.Bytes()
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 1 {
		t.Fatal("wrong ZIP files")
	}
	f, _ := archive.File[0].Open()
	got, _ := io.ReadAll(f)
	f.Close()
	if !bytes.Equal(got, data) {
		t.Fatal("ZIP corrupted bytes")
	}
	if bytes.Contains(request("/file?path="+url.QueryEscape(name)).Body.Bytes(), data) {
		t.Fatal("binary sent through JSON text viewer")
	}
}
