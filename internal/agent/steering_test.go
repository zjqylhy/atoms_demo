package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mockatoms/internal/config"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func waitForSignal(t *testing.T, ready <-chan struct{}) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the in-flight action")
	}
}

func TestSteeringReturnsEveryDelegateToTheRoot(t *testing.T) {
	for _, role := range []string{"Emma", "Bob", "Alex", "David"} {
		t.Run(role, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			childCalled := false
			old := call("list_files", map[string]any{})
			assign := call("assign_step", map[string]any{"role": role, "goal": "沿用初版"})
			g := fakeModel(func(ctx context.Context, messages []core.ModelMessage, _ []model.Tool) (core.ModelMessage, core.Usage, error) {
				usage := core.Usage{Known: true, Total: 3}
				last := messages[len(messages)-1].Content
				if strings.Contains(messages[0].Content, "的 "+role+"。") {
					if childCalled {
						return core.ModelMessage{}, usage, errors.New("子 Agent 在交接后继续生成")
					}
					childCalled = true
					close(entered)
					select {
					case <-release:
						return old, usage, nil
					case <-ctx.Done():
						return core.ModelMessage{}, usage, ctx.Err()
					}
				}
				if !strings.Contains(last, "更正") {
					return assign, usage, nil
				}
				if strings.Index(last, "更正为蓝色") >= strings.Index(last, "更正为绿色") ||
					!strings.Contains(last, "更正为蓝色") ||
					!strings.Contains(core.JSON(messages), "redirected") {
					return core.ModelMessage{}, usage, fmt.Errorf("root lost the ordered instructions or the handoff: %s", core.JSON(messages))
				}
				return call("finish_task", map[string]any{"summary": "按最新指令改为绿色", "answer_only": true}), usage, nil
			})
			e, p := testEngine(t, g)
			started, err := e.Start("alice", p.ID, "初版", "build", "start", "submit", "")
			if err != nil {
				t.Fatal(err)
			}
			waitForSignal(t, entered)
			for i, text := range []string{"更正为蓝色", "更正为绿色", "更正为绿色"} {
				requestID := fmt.Sprint(min(i, 1))
				if _, err := e.Steer("alice", p.ID, started.ActiveRun, requestID, text); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			finished := waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
			run, task := finished.Runs[started.ActiveRun], finished.Tasks[started.CurrentTask]
			if run.Status != "completed" || run.Calls != 3 || run.Usage.Total != 9 || len(task.InstructionMessages) != 2 || run.Tools != 2 {
				t.Fatalf("unexpected completion: %+v, instructions=%v", run, task.InstructionMessages)
			}
			frame := task.Frames[task.Root+"/"+assign.ToolCalls[0].ID]
			if len(frame.Messages) != 1 || frame.Complete {
				t.Fatalf("stale child reply was used: %+v", frame)
			}
			if _, err := e.Steer("alice", p.ID, started.ActiveRun, "late", "太晚"); err == nil {
				t.Fatal("accepted a new instruction after delivery")
			}
			if _, err := e.Steer("alice", p.ID, started.ActiveRun, "1", "更正为绿色"); err != nil {
				t.Fatalf("an accepted request cannot be retried after delivery: %v", err)
			}
		})
	}
}

func TestSteeringLetsStartedRunnerFinishAndSkipsLaterTools(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(fmt.Sprintf("resume=%t", resume), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var posts atomic.Int32
			type job struct{ ID, Digest string }
			var submitted atomic.Pointer[job]
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				j := submitted.Load()
				if r.Method == http.MethodPost {
					var payload job
					_ = json.NewDecoder(r.Body).Decode(&payload)
					j = &payload
					submitted.Store(j)
					if posts.Add(1) == 1 {
						close(entered)
					}
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				} else if j == nil {
					writeRunnerTestError(w, http.StatusNotFound, "job_not_found")
					return
				} else if r.URL.Path != "/jobs/"+j.ID {
					t.Errorf("queried another job: %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(core.Report{
					ID: j.ID, Digest: j.Digest, Status: "passed",
					Checks: []core.Check{{Name: "test", Status: "passed"}},
				})
			}))
			defer server.Close()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			assign := call("assign_step", map[string]any{"role": "Alex", "goal": "验证现有产物"})
			write := call("write_file", map[string]any{"path": "saved.txt", "content": "已保存", "old_hash": ""})
			run := call("run_project", map[string]any{"kind": "go", "entry": "go.mod"})
			oldWrite := call("write_file", map[string]any{"path": "stale.txt", "content": "应跳过", "old_hash": ""})
			run.ToolCalls = append(run.ToolCalls, oldWrite.ToolCalls...)
			g := fakeModel(func(_ context.Context, messages []core.ModelMessage, _ []model.Tool) (core.ModelMessage, core.Usage, error) {
				last := messages[len(messages)-1]
				if strings.Contains(messages[0].Content, "的 Alex。") {
					if last.ToolCallID == write.ToolCalls[0].ID {
						return run, core.Usage{}, nil
					}
					if last.ToolCallID == "" && !strings.Contains(last.Content, "保留已有文件") {
						return write, core.Usage{}, nil
					}
					return core.ModelMessage{}, core.Usage{}, errors.New("旧子任务继续执行")
				}
				if strings.Contains(last.Content, "保留已有文件") {
					return call("ask_user", map[string]any{"question": "还需验证哪些内容？"}), core.Usage{}, nil
				}
				return assign, core.Usage{}, nil
			})
			e, p := testEngine(t, g)
			e.Config.RunnerURL, e.Config.RunnerToken = server.URL, "test"
			started, err := e.Start("alice", p.ID, "执行检查", "build", "start", "submit", "")
			if err != nil {
				t.Fatal(err)
			}
			waitForSignal(t, entered)
			stored, err := e.Steer("alice", p.ID, started.ActiveRun, "steer", "保留已有文件，先询问我")
			if err != nil || len(stored.Reports) != 0 || stored.Tasks[stored.CurrentTask].RunnerAction == nil {
				t.Fatalf("runner did not stay in flight: %v", err)
			}
			if resume {
				if _, err := e.Stop("alice", p.ID, false); err != nil {
					t.Fatal(err)
				}
				waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
				if err := e.Store.Recover(); err != nil {
					t.Fatal(err)
				}
				if _, err := e.Start("alice", p.ID, "", "build", "resume", "continue", ""); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			finished := waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
			task := finished.Tasks[finished.CurrentTask]
			if task.Status != "waiting_input" || task.RunnerAction != nil || posts.Load() != 1 ||
				len(finished.Reports) != 1 || finished.Reports[0].ID != submitted.Load().ID ||
				finished.Files["saved.txt"].Hash == "" || finished.Files["stale.txt"].Hash != "" {
				t.Fatalf("incorrect runner handoff: %s", core.JSON(finished.Public()))
			}
			for _, step := range task.Steps {
				if strings.HasSuffix(step.ID, "/"+oldWrite.ToolCalls[0].ID) {
					t.Fatal("a skipped tool was recorded as an executed step")
				}
			}
		})
	}
}

func TestSteeringRejectsOldControlActionsAtCommit(t *testing.T) {
	e, p := testEngine(t, nil)
	s := scope{Owner: "alice", Project: p.ID, Task: "task", Run: "run", Frame: "root", Epoch: 1, toolStarted: true}
	_, err := e.Store.Mutate(s.Owner, s.Project, nil, func(p *core.Project) error {
		p.ActiveRun, p.CurrentTask, p.Epoch = s.Run, s.Task, s.Epoch
		p.Runs[s.Run] = &core.Run{ID: s.Run, TaskID: s.Task, Epoch: s.Epoch, Status: "running"}
		p.Tasks[s.Task] = &core.Task{ID: s.Task, Root: s.Frame, Status: "running", Steps: map[string]core.Step{},
			Frames: map[string]*core.Frame{s.Frame: {ID: s.Frame, Role: "Mike"}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Steer(s.Owner, s.Project, s.Run, "steer", "更新验收条件"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"finish_task", "ask_user", "propose_plan"} {
		_, err := e.record(s, call(name, map[string]any{}).ToolCalls[0], nil, func(*core.Project) (any, error) {
			t.Errorf("stale %s committed", name)
			return nil, nil
		})
		if !errors.Is(err, errNewInstruction) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestSteeringCanReviseAnApprovedPlan(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	initial := call("propose_plan", map[string]any{"plan": "旧方案", "acceptance": []string{"输出 CSV"}})
	revised := call("propose_plan", map[string]any{"plan": "修订方案", "acceptance": []string{"输出 JSON"}})
	calls := 0
	g := fakeModel(func(ctx context.Context, messages []core.ModelMessage, _ []model.Tool) (core.ModelMessage, core.Usage, error) {
		calls++
		switch calls {
		case 1:
			return initial, core.Usage{}, nil
		case 2:
			close(entered)
			select {
			case <-release:
				return call("finish_task", map[string]any{"summary": "旧交付", "answer_only": true}), core.Usage{}, nil
			case <-ctx.Done():
				return core.ModelMessage{}, core.Usage{}, ctx.Err()
			}
		default:
			if !strings.Contains(messages[len(messages)-1].Content, "输出 JSON") {
				return core.ModelMessage{}, core.Usage{}, errors.New("修订条件未传给主 Agent")
			}
			return revised, core.Usage{}, nil
		}
	})
	e, p := testEngine(t, g)
	if _, err := e.Start("alice", p.ID, "输出数据", "plan", "start", "submit", ""); err != nil {
		t.Fatal(err)
	}
	p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
	firstPlan := p.Tasks[p.CurrentTask].Plan.Revision
	approved, err := e.Start("alice", p.ID, "", "plan", "approve", "approve", firstPlan)
	if err != nil {
		t.Fatal(err)
	}
	waitForSignal(t, entered)
	if _, err := e.Steer("alice", p.ID, approved.ActiveRun, "steer", "改为输出 JSON"); err != nil {
		t.Fatal(err)
	}
	close(release)
	p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
	task := p.Tasks[p.CurrentTask]
	if task.Status != "waiting_plan" || task.Plan.Approved || task.Plan.Revision == firstPlan ||
		len(task.Plan.Acceptance) != 1 || task.Plan.Acceptance[0] != "输出 JSON" || len(p.Versions) != 0 {
		t.Fatalf("revision did not return to approval: %s", core.JSON(p.Public()))
	}
	if _, err := e.Start("alice", p.ID, "", "plan", "old-approval", "approve", firstPlan); err == nil {
		t.Fatal("accepted approval of the old plan")
	}
}

func TestSteeringRemainsInCompactedModelContext(t *testing.T) {
	p, task, frame := dialogueFixture(nil)
	task.InstructionMessages = append(task.InstructionMessages, p.Message("user", "OLD_STEER 输出 CSV", instructionKind))
	task.InstructionMessages = append(task.InstructionMessages, p.Message("user", "LATEST_STEER 改为 JSON", instructionKind))
	p.Message("user", "新问题的回答", "answer")
	client := model.New(config.Config{Protocol: "chat_completions", ToolMode: "json", MaxRequestBytes: 32 * 1024})
	prepared, err := prepareFrameContext(client, p, task, frame, definitions(frame.Role))
	if err != nil || prepared.replacement == nil || prepared.dialogue != nil {
		t.Fatalf("expected a compacted request: %+v %v", prepared, err)
	}
	requirements := taskRequirements(p, task)
	history := newContextHistory(core.ModelMessage{Role: "system"}, frame.Messages, requirements)
	projected, _ := history.project(prepared.replacement, nil)
	wire := core.JSON(projected)
	if !strings.Contains(wire, "LATEST_STEER") || !strings.Contains(wire, "OLD_STEER") || strings.Contains(wire, "OLD_TOOL_BODY") {
		t.Fatal("tool compaction did not preserve the steering messages")
	}
	for _, line := range history.oldDialogue(prepared.replacement) {
		if strings.Contains(line.Text, "LATEST_STEER") {
			t.Fatal("the latest steering instruction was eligible for summarization")
		}
	}
}
