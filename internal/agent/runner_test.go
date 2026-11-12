package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestRunnerAllowsFiveRepairsAfterInitialFailure(t *testing.T) {
	var jobs atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeRunnerTestError(w, http.StatusNotFound, "job_not_found")
			return
		}
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
		for i := 0; i < 6; i++ {
			p.Reports = append(p.Reports, core.Report{Kind: "run", Status: "failed", Details: []byte(`{"run_id":"run","failure_code":"container_failed"}`)})
		}
		p.Reports = append(p.Reports, core.Report{Kind: "run", Status: "failed", Details: []byte(`{"run_id":"run"}`), Checks: []core.Check{{Name: "interrupted", Status: "failed"}}})
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

func writeRunnerTestError(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code})
}

func TestRunnerRecoversLostResponseAfterBusyRetry(t *testing.T) {
	t.Parallel()
	var posts, lookups, executed atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Error("request did not use the runner credential")
		}
		if r.Method == http.MethodGet {
			lookups.Add(1)
			if r.URL.Path != "/jobs/job-id" {
				t.Errorf("unexpected lookup: %s", r.URL.Path)
			}
			if executed.Load() == 0 {
				writeRunnerTestError(w, http.StatusNotFound, "job_not_found")
				return
			}
			_ = json.NewEncoder(w).Encode(core.Report{ID: "job-id", Digest: "digest", Status: "passed", Checks: []core.Check{{Name: "go_build", Status: "passed"}}})
			return
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"id":"job-id"}` || r.URL.Path != "/jobs" {
			t.Errorf("submission changed: %s %s", r.URL.Path, body)
		}
		if posts.Add(1) == 1 {
			writeRunnerTestError(w, http.StatusTooManyRequests, "job_busy")
			return
		}
		executed.Add(1)
		// The runner saved the report but the POST response did not reach the app.
		w.Header().Set("Content-Length", "1000")
		_, _ = fmt.Fprint(w, `{"id":`)
	}))
	defer server.Close()
	client := runnerClient{http: server.Client(), baseURL: server.URL, token: "test"}
	var waits []string
	result, err := client.result(t.Context(), "job-id", "digest", []byte(`{"id":"job-id"}`), false, func(message string) error {
		waits = append(waits, message)
		return nil
	})
	if err != nil || result.Status != "passed" || posts.Load() != 2 || lookups.Load() != 2 || executed.Load() != 1 {
		t.Fatalf("result=%+v err=%v posts=%d lookups=%d executions=%d", result, err, posts.Load(), lookups.Load(), executed.Load())
	}
	if len(waits) != 1 || !strings.Contains(waits[0], "执行节点忙") {
		t.Fatalf("unexpected waits: %v", waits)
	}
}

type runnerTestTransport func(*http.Request) (*http.Response, error)

func (f runnerTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUnknownRunnerResultOnlyQueriesUntilLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var posts int
		var lookups []time.Duration
		start := time.Now()
		client := runnerClient{
			baseURL: "http://127.0.0.1",
			http: &http.Client{Transport: runnerTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodPost {
					posts++
					return nil, io.ErrUnexpectedEOF
				}
				lookups = append(lookups, time.Since(start))
				return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{"code":"job_not_found"}`))}, nil
			})},
		}
		_, err := client.result(t.Context(), "job-id", "digest", nil, false, func(string) error { return nil })
		want := []time.Duration{0, 0, 2 * time.Second, 7 * time.Second, 17 * time.Second}
		if err == nil || !strings.Contains(err.Error(), "执行结果尚未确认") || posts != 1 || !slices.Equal(lookups, want) {
			t.Fatalf("err=%v submissions=%d lookup times=%v", err, posts, lookups)
		}
	})
}

func TestRunnerLookupOnlyDoesNotSubmitAnAbsentJob(t *testing.T) {
	client := runnerClient{
		baseURL: "http://127.0.0.1",
		http: &http.Client{Transport: runnerTestTransport(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodGet || r.URL.Path != "/jobs/original" {
				t.Fatalf("unexpected runner request: %s %s", r.Method, r.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(strings.NewReader(`{"code":"job_not_found"}`)),
			}, nil
		})},
	}
	result, err := client.result(t.Context(), "original", "digest", nil, true, func(string) error {
		t.Fatal("unexpected wait for an absent job")
		return nil
	})
	if err != nil || !result.is(http.StatusNotFound, "job_not_found") {
		t.Fatalf("unexpected result %+v: %v", result, err)
	}
}

func TestStopRunnerWaitAndContinueOriginalJob(t *testing.T) {
	t.Parallel()
	var posts atomic.Int32
	var ready atomic.Bool
	args := map[string]any{"kind": "go", "entry": "go.mod"}
	assign := call("assign_step", map[string]any{"role": "Alex", "goal": "保存文件并执行检查"})
	write := call("write_file", map[string]any{"path": "notes.txt", "content": "saved", "old_hash": ""})
	job := call("run_project", args)
	g := fakeModel(func(_ context.Context, m []core.ModelMessage, _ []model.Tool) (core.ModelMessage, core.Usage, error) {
		if !strings.Contains(m[0].Content, "的 Alex。") {
			return assign, core.Usage{}, nil
		}
		last := m[len(m)-1]
		switch last.ToolCallID {
		case write.ToolCalls[0].ID:
			return job, core.Usage{}, nil
		case job.ToolCalls[0].ID:
			if !strings.Contains(last.Content, "runner_interrupted") {
				return core.ModelMessage{}, core.Usage{}, fmt.Errorf("wrong tool result: %s", last.Content)
			}
			return core.ModelMessage{}, core.Usage{}, fmt.Errorf("recovered tool result received")
		}
		return write, core.Usage{}, nil
	})
	e, p := testEngine(t, g)
	var jobID, digest string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var input struct{ ID, Digest string }
			_ = json.NewDecoder(r.Body).Decode(&input)
			jobID, digest = input.ID, input.Digest
			posts.Add(1)
			writeRunnerTestError(w, http.StatusConflict, "job_running")
			return
		}
		if posts.Load() == 0 {
			writeRunnerTestError(w, http.StatusNotFound, "job_not_found")
			return
		}
		if r.URL.Path != "/jobs/"+jobID {
			t.Errorf("continue did not query original job: %s", r.URL.Path)
		}
		if !ready.Load() {
			writeRunnerTestError(w, http.StatusConflict, "job_running")
			return
		}
		_ = json.NewEncoder(w).Encode(core.Report{ID: jobID, Digest: digest, Status: "failed", Checks: []core.Check{{Name: "interrupted", Status: "failed", Detail: "runner restarted"}}})
	}))
	defer server.Close()
	e.Config.RunnerURL, e.Config.RunnerToken = server.URL, "test"
	first, err := e.Start("alice", p.ID, "执行检查", "build", "start", "submit", "")
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		p, err = e.Store.Get("alice", p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if p.Events[len(p.Events)-1].Kind == "runner_wait" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("did not wait for the runner: %s", core.JSON(p.Public()))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = e.Stop("alice", p.ID, false); err != nil {
		t.Fatal(err)
	}
	p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.Runs[first.ActiveRun].Status == "stopped" })
	if len(p.Reports) != 0 || len(p.Tasks[p.CurrentTask].Checkpoints) != 1 {
		t.Fatal("stopped run changed the saved checkpoint or recorded an unconfirmed report")
	}
	ready.Store(true)
	second, err := e.Start("alice", p.ID, "", "build", "resume", "continue", "")
	if err != nil {
		t.Fatal(err)
	}
	p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.Runs[second.ActiveRun].Status == "failed" })
	if posts.Load() != 1 || len(p.Reports) != 1 || p.Files["notes.txt"].Hash == "" || !strings.Contains(p.Runs[second.ActiveRun].Error, "执行环境故障") {
		t.Fatalf("unexpected resumed run: posts=%d %s", posts.Load(), core.JSON(p.Public()))
	}
	third, err := e.Start("alice", p.ID, "", "build", "resume2", "continue", "")
	if err != nil {
		t.Fatal(err)
	}
	p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.Runs[third.ActiveRun].Status == "failed" })
	if posts.Load() != 1 || len(p.Reports) != 1 || !strings.Contains(p.Runs[third.ActiveRun].Error, "recovered tool result received") {
		t.Fatalf("the recorded environment failure was not returned on continue: %s", core.JSON(p.Public()))
	}
}
