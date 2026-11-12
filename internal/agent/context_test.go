package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mockatoms/internal/config"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func contextFixture() (*core.Project, *core.Task, *core.Frame) {
	p := core.NewProject("alice", "context test")
	frame := &core.Frame{ID: "frame", Role: "Alex", Goal: "完成网页", Messages: []core.ModelMessage{{Role: "user", Content: "保留角色原始任务"}}}
	task := &core.Task{ID: "task", Goal: "用户原始目标", Mode: "plan", Status: "failed", Base: p.Files,
		Root: frame.ID, Frames: map[string]*core.Frame{frame.ID: frame}, Steps: map[string]core.Step{},
		Plan:   &core.Plan{Revision: "v1", Approved: true, Acceptance: []string{"APPROVED_ACCEPTANCE"}},
		RunIDs: []string{"previous"}}
	p.CurrentTask, p.Tasks[task.ID] = task.ID, task
	p.Runs["previous"] = &core.Run{ID: "previous", TaskID: task.ID, Started: "2026-11-12T01:00:00Z", Status: "failed"}
	p.Messages = []core.Message{
		{Role: "user", Kind: "answer", Text: "UNRELATED_ANSWER", At: "2026-11-12T00:10:00Z"},
		{Role: "user", Kind: "answer", Text: "KEEP_CONFIRMED_ANSWER", At: "2026-11-12T01:01:10Z"},
		{Role: "user", Kind: "plan_feedback", Text: "KEEP_LATEST_CORRECTION", At: "2026-11-12T01:02:10Z"},
	}
	p.Events = []core.Event{{Kind: "question", Text: "KEEP_QUESTION_WITH_ANSWER", At: "2026-11-12T01:01:00Z"}}
	p.Reports = []core.Report{{ID: "CURRENT_REPORT", Kind: "static", Status: "failed", Digest: p.Files.Digest()},
		{ID: "STALE_REPORT", Kind: "static", Status: "passed", Digest: "another-version"}}
	return p, task, frame
}

func contextStep(task *core.Task, frame *core.Frame, n int, name, result string) {
	callID := fmt.Sprintf("action-%02d", n)
	recordID := frame.ID + "/" + callID
	frame.Messages = append(frame.Messages, core.ModelMessage{Role: "assistant", Content: "说明 " + callID,
		ToolCalls: []core.Call{{ID: callID, Type: "function", Function: core.Function{Name: name, Arguments: core.JSON(map[string]string{"path": callID + ".js", "content": "ARGUMENT_" + callID})}}}},
		core.ModelMessage{Role: "tool", ToolCallID: callID, Content: result})
	task.Steps[recordID] = core.Step{ID: recordID, Tool: name, FrameID: frame.ID, Role: frame.Role, Result: result}
	task.Checkpoints = append(task.Checkpoints, core.Checkpoint{ID: callID, StepID: recordID})
}

func measuredFrame(t *testing.T, client *model.Client, p *core.Project, task *core.Task, f *core.Frame, tools []model.Tool) int {
	t.Helper()
	messages := append([]core.ModelMessage{{Role: "system", Content: instructions(f.Role, p, task)}}, f.Messages...)
	r, err := client.PrepareRequest(messages, tools, len(messages))
	if err != nil {
		t.Fatal(err)
	}
	return r.ByteSize()
}

func TestContextCompactionKeepsRecentActionsAndConfirmedRequirements(t *testing.T) {
	for _, limit := range []int{48 * 1024, 64 * 1024} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			p, task, f := contextFixture()
			client := model.New(config.Config{Protocol: "chat_completions", ToolMode: "json", Model: "test", APIKey: "test-key", MaxRequestBytes: limit})
			tools := definitions(f.Role)
			contextStep(task, f, 1, "search_files", `{"ok":true,"result":"OMIT_OLD_SEARCH:"}`)
			contextStep(task, f, 2, "write_file", `{"ok":true,"result":{"saved":true}}`)
			for i := 3; i <= 6; i++ {
				contextStep(task, f, i, "write_file", fmt.Sprintf(`{"ok":true,"result":{"content":"BODY_%d_%s"}}`, i, strings.Repeat("a", 2100)))
			}
			f.Messages = append(f.Messages, core.ModelMessage{Role: "assistant", InvalidAction: true, Content: "DO_NOT_RESEND_INVALID"},
				core.ModelMessage{Role: "user", Content: formatFeedbackPrefix + "KEEP_CURRENT_FORMAT_ERROR"})
			base := measuredFrame(t, client, p, task, f, tools)
			cutoff := percentOf(limit, 75)
			padded := func(n int) {
				f.Messages[2].Content = fmt.Sprintf(`{"ok":true,"result":"OMIT_OLD_SEARCH:%s"}`, strings.Repeat("x", n))
				step := task.Steps[f.ID+"/action-01"]
				step.Result = f.Messages[2].Content
				task.Steps[step.ID] = step
			}
			padded(cutoff - base - 1)
			below, err := prepareFrameContext(client, p, task, f, tools)
			if err != nil || below.replacement != nil || below.request.ByteSize() != cutoff-1 {
				t.Fatalf("below trigger: size=%d, compressed=%t, err=%v", below.request.ByteSize(), below.replacement != nil, err)
			}
			padded(cutoff - base)
			original := core.JSON(p)
			at, err := prepareFrameContext(client, p, task, f, tools)
			if err != nil || at.beforeBytes != cutoff || at.replacement == nil || at.replacement.Through != 5 || at.request.ByteSize() > limit*60/100 {
				t.Fatalf("at trigger: before=%d, after=%d, summary=%+v, err=%v", at.beforeBytes, at.request.ByteSize(), at.replacement, err)
			}
			if core.JSON(p) != original {
				t.Fatal("compression mutated stored history")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil || len(body) != at.request.ByteSize() {
					t.Errorf("actual bytes = %d, want %d, error = %v", len(body), at.request.ByteSize(), err)
				}
				wire := string(body)
				for _, want := range []string{"APPROVED_ACCEPTANCE", "KEEP_CONFIRMED_ANSWER", "KEEP_QUESTION_WITH_ANSWER",
					"KEEP_LATEST_CORRECTION", "CURRENT_REPORT", "KEEP_CURRENT_FORMAT_ERROR", "action-02.js",
					"ARGUMENT_action-03", "ARGUMENT_action-06", "BODY_3_" + strings.Repeat("a", 2100), "BODY_6_" + strings.Repeat("a", 2100)} {
					if !strings.Contains(wire, want) {
						t.Errorf("missing %q", excerpt(want, 80))
					}
				}
				for _, absent := range []string{"OMIT_OLD_SEARCH", "STALE_REPORT", "UNRELATED_ANSWER", "DO_NOT_RESEND_INVALID", "ARGUMENT_action-02"} {
					if strings.Contains(wire, absent) {
						t.Errorf("retained %q", absent)
					}
				}
				_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"{\"content\":\"done\",\"calls\":[]}"},"finish_reason":"stop"}]}`)
			}))
			defer server.Close()
			client.Config.BaseURL = server.URL
			if _, _, err := client.GeneratePrepared(context.Background(), at.request, tools); err != nil {
				t.Fatal(err)
			}
			f.Context = at.replacement
			reused, err := prepareFrameContext(client, p, task, f, tools)
			if err != nil || reused.replacement != nil || reused.request.ByteSize() != at.request.ByteSize() {
				t.Fatalf("cached summary not reused: %+v %v", reused, err)
			}
			for i := 7; i <= 13; i++ {
				contextStep(task, f, i, "search_files", core.JSON(map[string]any{"ok": true, "result": strings.Repeat("b", 3200)}))
			}
			contextStep(task, f, 14, "search_files", core.JSON(map[string]any{"ok": true, "result": strings.Repeat("c", limit*60/100)}))
			// A large recent action may need to be kept in full even when it exceeds the soft target.
			again, err := prepareFrameContext(client, p, task, f, tools)
			if err != nil || again.replacement == nil || again.replacement.Through <= f.Context.Through || again.request.ByteSize() > limit {
				t.Fatalf("second compression: size=%d, summary=%+v, err=%v", again.request.ByteSize(), again.replacement, err)
			}
		})
	}
}

func TestContinuePersistsCompactionWithoutReplayingTools(t *testing.T) {
	for _, oversize := range []bool{false, true} {
		t.Run(fmt.Sprint("required input oversized=", oversize), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				body, _ := io.ReadAll(r.Body)
				if len(body) >= 48*1024 || !strings.Contains(string(body), "旧历史进展摘要") {
					t.Errorf("uncompacted request: %d bytes", len(body))
				}
				answer := core.JSON(map[string]any{"content": "", "calls": []any{map[string]any{
					"name": "finish_task", "arguments": map[string]any{"summary": "已续跑", "answer_only": true},
				}}})
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
					"message": map[string]any{"role": "assistant", "content": answer}, "finish_reason": "stop",
				}}})
			}))
			defer server.Close()
			client := model.New(config.Config{Protocol: "chat_completions", ToolMode: "json", Model: "test", APIKey: "test-key",
				BaseURL: server.URL, MaxRequestBytes: 48 * 1024})
			e, created := testEngine(t, client)
			fixture, task, f := contextFixture()
			f.Role, task.Mode = "Mike", "build"
			fixture.Runs["previous"].Model = e.Config.Ref()
			if oversize {
				task.Goal = strings.Repeat("必要输入", 14*1024)
			}
			contextStep(task, f, 1, "search_files", core.JSON(map[string]any{"ok": true, "result": strings.Repeat("x", 55*1024)}))
			for i := 2; i <= 6; i++ {
				contextStep(task, f, i, "list_files", `{"ok":true,"result":{}}`)
			}
			_, err := e.Store.Mutate("alice", created.ID, nil, func(p *core.Project) error {
				p.Tasks, p.Runs, p.CurrentTask, p.Messages, p.Reports = fixture.Tasks, fixture.Runs, fixture.CurrentTask, fixture.Messages, fixture.Reports
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			started, err := e.Start("alice", created.ID, "", "build", "continue-test", "continue", "")
			if err != nil {
				t.Fatal(err)
			}
			finished := waitProject(t, e, created.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
			run := finished.Runs[started.ActiveRun]
			saved := finished.Tasks[task.ID]
			frame := saved.Frames[f.ID]
			if len(frame.Messages) < len(f.Messages) || core.JSON(frame.Messages[:len(f.Messages)]) != core.JSON(f.Messages) ||
				core.JSON(saved.Checkpoints[:len(task.Checkpoints)]) != core.JSON(task.Checkpoints) {
				t.Fatal("existing messages or checkpoints changed")
			}
			if oversize {
				if calls.Load() != 0 || run.Calls != 0 || run.Status != "failed" || !strings.Contains(run.Error, "必须保留的任务要求") {
					t.Fatalf("oversize request was not rejected locally: %+v, HTTP calls = %d", run, calls.Load())
				}
				return
			}
			if calls.Load() != 1 || run.Status != "completed" || run.Calls != 1 || run.Tools != 1 || frame.Context == nil ||
				len(saved.Checkpoints) != len(task.Checkpoints)+1 {
				t.Fatalf("unexpected continuation: %+v, HTTP calls = %d, context = %+v", run, calls.Load(), frame.Context)
			}
			for _, event := range finished.Events {
				if event.Kind == "context_compacted" {
					return
				}
			}
			t.Fatal("compaction event missing")
		})
	}
}
