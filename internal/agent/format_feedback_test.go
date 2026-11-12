package agent

import (
	"io"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMalformedEnvelopeRetryKeepsEvidenceAndExecutesOnlyCorrection(t *testing.T) {
	e, p := testEngine(t, nil)
	body := "REQUIRED_EVIDENCE_" + strings.Repeat("input", 1000)
	hash := core.Hash([]byte(body))
	_, err := e.Store.Mutate("alice", p.ID, map[string][]byte{hash: []byte(body)}, func(p *core.Project) error {
		p.Files["evidence.txt"] = core.File{Hash: hash, Size: len(body)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	bad := `{"content":"delegate","calls":[{"name":"assign_step","role":"Alex","goal":"inspect saved evidence"}]}`
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire, _ := io.ReadAll(r.Body)
		var reply string
		switch requests.Add(1) {
		case 1:
			reply = `{"calls":[{"name":"read_file","arguments":{"path":"evidence.txt"}}]}`
		case 2:
			reply = bad
		case 3:
			for _, want := range []string{body, hash, "$.calls[0].arguments.role", "允许字段：name, arguments"} {
				if !strings.Contains(string(wire), want) {
					t.Errorf("actual retry request lost evidence or repair feedback: %s", excerpt(want, 60))
				}
			}
			reply = `{"calls":[{"name":"assign_step","arguments":{"role":"Alex","goal":"inspect saved evidence"}}]}`
		case 4:
			reply = `{"calls":[{"name":"return_step","arguments":{"status":"completed","reason":"read-only inspection complete","summary":"input retained","todo":[]}}]}`
		case 5:
			reply = `{"calls":[{"name":"finish_task","arguments":{"summary":"inspection complete","answer_only":true}}]}`
		default:
			t.Error("unexpected retry")
			reply = `{"content":"unexpected","calls":[]}`
		}
		writeDialogueReply(w, reply)
	}))
	defer server.Close()
	e.Config.BaseURL, e.Config.APIKey, e.Config.Model = server.URL, "test-key", "test"
	e.Config.ToolMode, e.Config.Protocol = "json", "chat_completions"
	e.Model = model.New(e.Config)
	started, err := e.Start("alice", p.ID, "inspect retained evidence", "build", "start", "submit", "")
	if err != nil {
		t.Fatal(err)
	}
	p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
	run, task := p.Runs[started.ActiveRun], p.Tasks[p.CurrentTask]
	if run.Status != "completed" || run.Calls != 5 || run.FormatRepairs != 1 || run.Tools != 4 || len(task.Frames) != 2 {
		t.Fatal("invalid envelope executed or repair failed", core.JSON(run))
	}
	found := false
	for _, message := range task.Frames[task.Root].Messages {
		found = found || (message.InvalidAction && message.Content == bad)
	}
	if !found || p.Files["evidence.txt"].Hash != hash {
		t.Fatal("raw rejection record or saved input lost")
	}
}
