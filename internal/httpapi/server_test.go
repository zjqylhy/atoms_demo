package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mockatoms/internal/agent"
	"mockatoms/internal/config"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"mockatoms/internal/store"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type heldModel struct{ started, release chan struct{} }

func (m heldModel) Generate(ctx context.Context, messages []core.ModelMessage, _ []model.Tool) (core.ModelMessage, core.Usage, error) {
	select {
	case m.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return core.ModelMessage{}, core.Usage{}, ctx.Err()
	case <-m.release:
		summary := "说明完成"
		if strings.Contains(messages[len(messages)-1].Content, "用简短说明") {
			summary = "已改用简短说明"
		}
		args := core.JSON(map[string]any{"summary": summary, "answer_only": true})
		return core.ModelMessage{Role: "assistant", ToolCalls: []core.Call{{ID: "done", Type: "function", Function: core.Function{Name: "finish_task", Arguments: args}}}}, core.Usage{Known: true, Total: 3}, nil
	}
}
func TestHTTPWorkspaceCSRFBackgroundExecutionAndVersionIsolation(t *testing.T) {
	s, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	gate := heldModel{make(chan struct{}, 1), make(chan struct{})}
	c := config.Config{MaxCalls: 5, MaxTools: 10, MaxConcurrent: 2, RunSeconds: 30}
	e := agent.New(s, c, gate)
	defer e.Shutdown()
	ts := httptest.NewServer(New(s, e, c))
	defer ts.Close()
	newClient := func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{Jar: jar, Timeout: 3 * time.Second}
	}
	alice, bob := newClient(), newClient()
	req := func(client *http.Client, method, url, token, body string) (int, []byte) {
		t.Helper()
		r, _ := http.NewRequest(method, ts.URL+url, strings.NewReader(body))
		if token != "" {
			r.Header.Set("X-CSRF-Token", token)
		}
		res, er := client.Do(r)
		if er != nil {
			t.Fatal(er)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	bootstrap := func(client *http.Client) string {
		status, b := req(client, "GET", "/api/bootstrap", "", "")
		if status != 200 {
			t.Fatal(status, string(b))
		}
		var v map[string]any
		json.Unmarshal(b, &v)
		return v["csrf"].(string)
	}
	a, b := bootstrap(alice), bootstrap(bob)
	if status, _ := req(alice, "POST", "/api/projects", "", `{"title":"private"}`); status != 403 {
		t.Fatal("missing CSRF accepted")
	}
	status, raw := req(alice, "POST", "/api/projects", a, `{"title":"private"}`)
	if status != 201 {
		t.Fatal(string(raw))
	}
	var p struct {
		ID string `json:"id"`
	}
	json.Unmarshal(raw, &p)
	base := "/api/projects/" + p.ID
	for _, route := range []string{base, base + "/files?path=secret.md", base + "/export", base + "/preview", base + "/events"} {
		if status, _ := req(bob, "GET", route, "", ""); status != 404 {
			t.Fatalf("cross-owner read %s: %d", route, status)
		}
	}
	if status, _ := req(bob, "POST", base+"/actions", b, `{"action":"submit","text":"x","request_id":"foreign"}`); status != 404 {
		t.Fatal("cross-owner action accepted")
	}
	if status, _ := req(alice, "POST", base+"/inputs", a, `{"name":"../escape.csv","content":"x\n1"}`); status != 400 {
		t.Fatal("path escape accepted", status)
	}
	if status, raw = req(alice, "POST", base+"/inputs", a, `{"name":"sales.csv","content":"category,amount\ncoffee,20\ntea,10\n"}`); status != 200 {
		t.Fatal(status, string(raw))
	}
	if status, raw = req(alice, "POST", base+"/actions", a, `{"action":"submit","text":"解释需求，不修改文件","request_id":"once","mode":"build"}`); status != 200 {
		t.Fatal(string(raw))
	}
	<-gate.started
	var started struct {
		ActiveRun string `json:"active_run"`
	}
	if err := json.Unmarshal(raw, &started); err != nil || started.ActiveRun == "" {
		t.Fatal("missing active run", err)
	}
	steer := core.JSON(map[string]string{"action": "steer", "run_id": started.ActiveRun, "request_id": "steer-once", "text": "用简短说明"})
	if status, _ := req(bob, "POST", base+"/actions", b, steer); status != 404 {
		t.Fatal("cross-owner instruction accepted", status)
	}
	for range 2 {
		status, raw := req(alice, "POST", base+"/actions", a, steer)
		if status != 200 || bytes.Count(raw, []byte(`"kind":"steering"`)) != 1 {
			t.Fatal("instruction was not saved exactly once", status, string(raw))
		}
	}
	// No SSE connection is open. Completion must not depend on a browser request.
	close(gate.release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, raw = req(alice, "GET", base, "", "")
		if bytes.Contains(raw, []byte(`"status":"completed"`)) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !bytes.Contains(raw, []byte(`"status":"completed"`)) {
		t.Fatal("background run did not complete", string(raw))
	}
	if !bytes.Contains(raw, []byte("已改用简短说明")) {
		t.Fatal("delivery did not reflect the instruction", string(raw))
	}
	late := core.JSON(map[string]string{"action": "steer", "run_id": started.ActiveRun, "request_id": "late", "text": "太晚"})
	if status, _ := req(alice, "POST", base+"/actions", a, late); status != 409 {
		t.Fatal("ended run accepted an instruction", status)
	}
	if bytes.Contains(raw, []byte(`"frames"`)) || bytes.Contains(raw, []byte(`"protocol_items"`)) {
		t.Fatal("private model context exposed")
	}
	status, raw = req(alice, "GET", base+"/files?path=inputs/sales.csv", "", "")
	if status != 200 || !bytes.Contains(raw, []byte("coffee,20")) {
		t.Fatal(status, string(raw))
	}
	if status, _ := req(alice, "POST", base+"/actions", a, `{"action":"restore","version":"missing","request_id":"restore"}`); status != 409 {
		t.Fatal("nonexistent version accepted")
	}
}
