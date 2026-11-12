package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"mockatoms/internal/config"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"mockatoms/internal/store"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func dialogueFixture(older []core.ModelMessage) (*core.Project, *core.Task, *core.Frame) {
	p, task, frame := contextFixture()
	p.Messages, p.Events = nil, nil
	frame.Role, task.Mode = "Mike", "build"
	frame.Messages = append(frame.Messages, older...)
	frame.Messages = append(frame.Messages, core.ModelMessage{Role: "user", Content: "LATEST_USER: 改为 JSON 导出"})
	contextStep(task, frame, 1, "search_files", core.JSON(map[string]any{"ok": true, "result": "OLD_TOOL_BODY:" + strings.Repeat("x", 64*1024)}))
	for i := 2; i <= 6; i++ {
		contextStep(task, frame, i, "list_files", `{"ok":true,"result":{}}`)
	}
	return p, task, frame
}

func seedDialogueTask(t *testing.T, e *Engine, id string, fixture *core.Project, task *core.Task) {
	t.Helper()
	fixture.Runs["previous"].Model = e.Config.Ref()
	_, err := e.Store.Mutate("alice", id, nil, func(p *core.Project) error {
		p.Tasks, p.Runs, p.CurrentTask = fixture.Tasks, fixture.Runs, task.ID
		p.Messages, p.Events, p.Reports = fixture.Messages, fixture.Events, fixture.Reports
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func writeDialogueReply(w http.ResponseWriter, content string) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{
			"message":       map[string]string{"role": "assistant", "content": content},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{"prompt_tokens": 11, "completion_tokens": 3, "total_tokens": 14},
	})
}

const finishDialogueTask = `{"content":"","calls":[{"name":"finish_task","arguments":{"summary":"已按 JSON 续做","answer_only":true}}]}`

func TestDialogueSummaryHardLimitBoundary(t *testing.T) {
	for _, overflow := range []int{0, 1} {
		t.Run(fmt.Sprintf("一级后超出%d字节", overflow), func(t *testing.T) {
			fixture, task, frame := dialogueFixture([]core.ModelMessage{
				{Role: "user", Content: "OLD_USER: 原先考虑 CSV。" + strings.Repeat("u", 6000)},
				{Role: "assistant", Content: "OLD_ASSISTANT: 可以兼容 CSV。" + strings.Repeat("a", 6000)},
			})
			client := model.New(config.Config{Protocol: "chat_completions", ToolMode: "json", Model: "test", APIKey: "test-key"})
			tools := definitions(frame.Role)
			history := newContextHistory(core.ModelMessage{Role: "system", Content: instructions(frame.Role, fixture, task)}, frame.Messages, nil)
			starts := completeActionStarts(frame.Messages)
			smallest := measuredFrame(t, client, fixture, task, frame, tools)
			for _, cut := range starts[len(starts)-recentActionCount:] {
				messages, completeFrom := history.project(summarizeFrame(task, frame, cut), nil)
				request, err := client.PrepareRequest(messages, tools, completeFrom)
				if err != nil {
					t.Fatal(err)
				}
				smallest = min(smallest, request.ByteSize())
			}
			limit := smallest - overflow
			client.Config.MaxRequestBytes = limit
			var summaries, formal atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				wire := string(body)
				if len(body) > limit || strings.Contains(wire, "OLD_TOOL_BODY") {
					t.Errorf("untrimmed request: %d bytes, limit %d", len(body), limit)
				}
				if strings.Contains(wire, "你负责合并旧对话摘要") {
					summaries.Add(1)
					if strings.Contains(wire, "OLD_USER") || !strings.Contains(wire, "OLD_ASSISTANT") || strings.Contains(wire, "LATEST_USER") {
						t.Error("summary request should include only old dialogue")
					}
					writeDialogueReply(w, `{"items":[{"kind":"suggestion","text":"可兼容 CSV","sources":["frame:2"]}]}`)
					return
				}
				formal.Add(1)
				if !strings.Contains(wire, "LATEST_USER") {
					t.Error("formal request lost the latest correction")
				}
				if overflow == 0 {
					if len(body) != limit || !strings.Contains(wire, "OLD_USER") || !strings.Contains(wire, "OLD_ASSISTANT") {
						t.Errorf("boundary should send the original dialogue at exactly %d bytes, got %d", limit, len(body))
					}
				} else if !strings.Contains(wire, "可兼容 CSV") || strings.Contains(wire, "OLD_ASSISTANT") || !strings.Contains(wire, "OLD_USER") {
					t.Error("formal request did not substitute the saved summary")
				}
				writeDialogueReply(w, finishDialogueTask)
			}))
			defer server.Close()
			client.Config.BaseURL = server.URL
			e, p := testEngine(t, client)
			seedDialogueTask(t, e, p.ID, fixture, task)
			started, err := e.Start("alice", p.ID, "", "build", "boundary", "continue", "")
			if err != nil {
				t.Fatal(err)
			}
			finished := waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
			run := finished.Runs[started.ActiveRun]
			if run.Status != "completed" || summaries.Load() != int32(overflow) || formal.Load() != 1 || run.Calls != 1+overflow || run.UsageReports != run.Calls || run.Tools != 1 {
				t.Fatalf("unexpected run %+v, summary=%d, formal=%d", run, summaries.Load(), formal.Load())
			}
		})
	}
}

func TestDialogueSummaryBatchesAndResume(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("中断=%t", interrupt), func(t *testing.T) {
			const limit = 16 * 1024
			fixture, task, frame := dialogueFixture([]core.ModelMessage{
				{Role: "assistant", Content: "FIRST_ONLY: " + strings.Repeat("甲", 4000)},
				{Role: "assistant", Content: "LARGE_REPLY: " + strings.Repeat("乙", 10000)},
				{Role: "assistant", Content: "THIRD_REPLY: " + strings.Repeat("丙", 4000)},
			})
			secondStarted := make(chan struct{})
			var summaries, formal atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				wire := string(body)
				if len(body) > limit || strings.Contains(wire, "OLD_TOOL_BODY") {
					t.Errorf("oversize or untrimmed request: %d bytes", len(body))
				}
				if strings.Contains(wire, "你负责合并旧对话摘要") {
					n := summaries.Add(1)
					if n == 1 && !strings.Contains(wire, "FIRST_ONLY") {
						t.Error("first batch did not start at the oldest dialogue")
					}
					if n > 1 && (strings.Contains(wire, "FIRST_ONLY") || !strings.Contains(wire, "保留旧用户约束")) {
						t.Error("later batch repeated old input or omitted the saved summary")
					}
					if interrupt && n == 2 {
						close(secondStarted)
						<-r.Context().Done()
						return
					}
					writeDialogueReply(w, `{"items":[{"kind":"suggestion","text":"保留旧用户约束","sources":["frame:1"]}]}`)
					return
				}
				formal.Add(1)
				if !strings.Contains(wire, "LATEST_USER") || !strings.Contains(wire, "保留旧用户约束") {
					t.Error("resumed request lost dialogue requirements")
				}
				writeDialogueReply(w, finishDialogueTask)
			}))
			defer server.Close()
			client := model.New(config.Config{Protocol: "chat_completions", ToolMode: "json", Model: "test", APIKey: "test-key", BaseURL: server.URL, MaxRequestBytes: limit})
			path := filepath.Join(t.TempDir(), "dialogue.db")
			db, err := store.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			p, err := db.Create("alice", "dialogue")
			if err != nil {
				t.Fatal(err)
			}
			cfg := config.Config{MaxCalls: 16, MaxTools: 16, MaxConcurrent: 1, RunSeconds: 20, DailyCalls: 16, WorkspaceDailyCalls: 16}
			e := New(db, cfg, client)
			t.Cleanup(e.Shutdown)
			seedDialogueTask(t, e, p.ID, fixture, task)
			started, err := e.Start("alice", p.ID, "", "build", "first", "continue", "")
			if err != nil {
				t.Fatal(err)
			}
			if interrupt {
				select {
				case <-secondStarted:
				case <-time.After(5 * time.Second):
					t.Fatal("second summary call did not start")
				}
				stopped, err := e.Stop("alice", p.ID, false)
				if err != nil {
					t.Fatal(err)
				}
				saved := stopped.Tasks[task.ID].Frames[frame.ID].Dialogue
				if saved == nil || saved.Covered["frame:1"] != len(frame.Messages[1].Content) || saved.Covered["frame:2"] != 0 {
					t.Fatalf("stop should retain only the first batch: %+v", saved)
				}
				waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				db, err = store.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				e = New(db, cfg, client)
				t.Cleanup(e.Shutdown)
				started, err = e.Start("alice", p.ID, "", "build", "resume", "continue", "")
				if err != nil {
					t.Fatal(err)
				}
			}
			finished := waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
			run := finished.Runs[started.ActiveRun]
			saved := finished.Tasks[task.ID]
			if run.Status != "completed" || formal.Load() != 1 || summaries.Load() < 3 || run.UsageReports != run.Calls || run.Tools != 1 {
				t.Fatalf("unexpected run %+v, summary=%d, formal=%d", run, summaries.Load(), formal.Load())
			}
			counted := 0
			for _, r := range finished.Runs {
				counted += r.Calls
			}
			if counted != int(summaries.Load()+formal.Load()) {
				t.Fatalf("recorded calls = %d, actual = %d", counted, summaries.Load()+formal.Load())
			}
			if err := db.ReserveCall("alice", 0, counted); err == nil {
				t.Fatal("workspace daily quota did not include all summary calls")
			}
			if core.JSON(saved.Frames[frame.ID].Messages[:len(frame.Messages)]) != core.JSON(frame.Messages) ||
				core.JSON(saved.Checkpoints[:len(task.Checkpoints)]) != core.JSON(task.Checkpoints) || len(saved.Checkpoints) != len(task.Checkpoints)+1 {
				t.Fatal("compression changed old history or replayed a tool")
			}
		})
	}
}
