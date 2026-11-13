package agent

import (
	"context"
	"errors"
	"mockatoms/internal/config"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"mockatoms/internal/store"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeModel func(context.Context, []core.ModelMessage, []model.Tool) (core.ModelMessage, core.Usage, error)

func (f fakeModel) Generate(c context.Context, m []core.ModelMessage, t []model.Tool) (core.ModelMessage, core.Usage, error) {
	return f(c, m, t)
}
func call(name string, args any) core.ModelMessage {
	return core.ModelMessage{Role: "assistant", ToolCalls: []core.Call{{ID: core.ID(), Type: "function", Function: core.Function{Name: name, Arguments: core.JSON(args)}}}}
}
func testEngine(t *testing.T, g model.Generator) (*Engine, *core.Project) {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	p, _ := s.Create("alice", "test")
	e := New(s, config.Config{MaxCalls: 24, MaxTools: 64, MaxConcurrent: 2, RunSeconds: 20, Revision: "test"}, g)
	t.Cleanup(e.Shutdown)
	return e, p
}
func waitProject(t *testing.T, e *Engine, id string, predicate func(*core.Project) bool) *core.Project {
	t.Helper()
	end := time.Now().Add(5 * time.Second)
	for time.Now().Before(end) {
		p, err := e.Store.Get("alice", id)
		if err != nil {
			t.Fatal(err)
		}
		e.mu.Lock()
		_, running := e.active[id]
		e.mu.Unlock()
		if predicate(p) && !running {
			return p
		}
		time.Sleep(10 * time.Millisecond)
	}
	p, _ := e.Store.Get("alice", id)
	t.Fatalf("timed out: %s", core.JSON(p.Public()))
	return nil
}

func TestActionFormatRetries(t *testing.T) {
	const trailingError = "模型动作 JSON 包含多余内容，未执行：顶层对象闭合后只允许空白"
	for _, tt := range []struct {
		name                  string
		validAfter            int
		finishOn              int
		status, runError      string
		calls, repairs, tools int
	}{
		{name: "reset after valid action", validAfter: 6, finishOn: 12, status: "completed", calls: 12, repairs: 10, tools: 2},
		{name: "stop after five retries", validAfter: 7, status: "failed", runError: trailingError, calls: 6, repairs: 5},
		{name: "retries count toward run limit", validAfter: 6, status: "failed", runError: "达到本次模型调用上限", calls: 48, repairs: 40, tools: 8},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			g := fakeModel(func(_ context.Context, messages []core.ModelMessage, _ []model.Tool) (core.ModelMessage, core.Usage, error) {
				calls++
				usage := core.Usage{Known: true, Input: 1, Output: 1, Total: 2}
				if (calls-1)%tt.validAfter != 0 {
					last := messages[len(messages)-1].Content
					if !strings.Contains(last, trailingError) || !strings.Contains(last, "只输出一个完整 JSON 对象") {
						return core.ModelMessage{}, usage, errors.New("retry prompt omitted error or output instructions")
					}
				}
				switch {
				case calls == tt.finishOn:
					return call("finish_task", map[string]any{"summary": "已检查", "answer_only": true}), usage, nil
				case calls%tt.validAfter == 0:
					return call("list_files", map[string]any{}), usage, nil
				default:
					return core.ModelMessage{Role: "assistant", Content: `{"content":"","calls":[]} extra`, InvalidAction: true},
						usage, &model.ActionFormatError{Reason: trailingError}
				}
			})
			e, p := testEngine(t, g)
			e.Config.MaxCalls = 48
			started, err := e.Start(core.RunActor{Workspace: "alice"}, "alice", p.ID, "测试格式重试", "build", "start", "submit", "")
			if err != nil {
				t.Fatal(err)
			}
			runID := started.ActiveRun
			p = waitProject(t, e, p.ID, func(p *core.Project) bool {
				return p.Runs[runID].Status != "running"
			})
			r := p.Runs[runID]
			if r.Status != tt.status || (tt.runError != "" && !strings.Contains(r.Error, tt.runError)) ||
				r.Calls != tt.calls || r.FormatRepairs != tt.repairs || r.Tools != tt.tools || r.UsageReports != tt.calls {
				t.Fatalf("unexpected run: %+v", r)
			}
		})
	}
}

func TestGatewayTPMRetries(t *testing.T) {
	for _, tt := range []struct {
		name                    string
		finishOn, runCap, daily int
		calls, waits            int
		errorText               string
	}{
		{name: "retry and complete", finishOn: 3, runCap: 96, calls: 3, waits: 2},
		{name: "stop after two retries", runCap: 96, calls: 3, waits: 2, errorText: "model_tpm"},
		{name: "respect run quota", runCap: 1, calls: 1, errorText: "model_tpm"},
		{name: "respect daily quota", runCap: 96, daily: 2, calls: 2, waits: 2, errorText: "服务当日模型调用额度已用完"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			g := fakeModel(func(_ context.Context, _ []core.ModelMessage, _ []model.Tool) (core.ModelMessage, core.Usage, error) {
				calls++
				if calls == tt.finishOn {
					return call("finish_task", map[string]any{"summary": "已恢复", "answer_only": true}), core.Usage{Known: true}, nil
				}
				return core.ModelMessage{}, core.Usage{}, &model.TPMThrottleError{RetryAfter: time.Millisecond}
			})
			e, p := testEngine(t, g)
			e.Config.MaxCalls = tt.runCap
			e.Config.DailyCalls = tt.daily
			started, err := e.Start(core.RunActor{Workspace: "alice"}, "alice", p.ID, "测试网关限流恢复", "build", "start", "submit", "")
			if err != nil {
				t.Fatal(err)
			}
			runID := started.ActiveRun
			p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.Runs[runID].Status != "running" })
			r := p.Runs[runID]
			waits := 0
			for _, event := range p.Events {
				if event.Kind == "model_wait" {
					waits++
				}
			}
			if r.Calls != tt.calls || waits != tt.waits || r.FormatRepairs != 0 {
				t.Fatalf("unexpected run: %+v, waits=%d", r, waits)
			}
			if tt.errorText == "" {
				if r.Status != "completed" || r.Tools != 1 || r.Usage.Known {
					t.Fatalf("unexpected completion: %+v", r)
				}
			} else if r.Status != "failed" || r.Tools != 0 || !strings.Contains(r.Error, tt.errorText) {
				t.Fatalf("unexpected failure: %+v", r)
			}
		})
	}
}

func TestStopCancelsGatewayTPMWait(t *testing.T) {
	g := fakeModel(func(_ context.Context, _ []core.ModelMessage, _ []model.Tool) (core.ModelMessage, core.Usage, error) {
		return core.ModelMessage{}, core.Usage{}, &model.TPMThrottleError{RetryAfter: time.Minute}
	})
	e, p := testEngine(t, g)
	started, err := e.Start(core.RunActor{Workspace: "alice"}, "alice", p.ID, "测试取消等待", "build", "start", "submit", "")
	if err != nil {
		t.Fatal(err)
	}
	runID := started.ActiveRun
	deadline := time.Now().Add(3 * time.Second)
	for {
		p, err = e.Store.Get("alice", p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if p.Events[len(p.Events)-1].Kind == "model_wait" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("model wait did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := e.Stop("alice", p.ID, false); err != nil {
		t.Fatal(err)
	}
	p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
	if r := p.Runs[runID]; r.Status != "stopped" || r.Calls != 1 {
		t.Fatalf("unexpected stopped run: %+v", r)
	}
}

func TestAnswerIsBoundToTheDisplayedQuestion(t *testing.T) {
	g := fakeModel(func(_ context.Context, messages []core.ModelMessage, _ []model.Tool) (core.ModelMessage, core.Usage, error) {
		switch messages[len(messages)-1].Content {
		case "用户补充：网页":
			return call("ask_user", map[string]any{"question": "还有什么要求？"}), core.Usage{}, nil
		case "用户补充：键盘可操作":
			return call("finish_task", map[string]any{"summary": "已收到回答", "answer_only": true}), core.Usage{}, nil
		default:
			return call("ask_user", map[string]any{"question": "需要什么形式？", "options": []string{"网页", "文档"}}), core.Usage{}, nil
		}
	})
	e, p := testEngine(t, g)
	start := func(action, request, text, decisionID string) (*core.Project, error) {
		return e.Start(core.RunActor{Workspace: "alice"}, "alice", p.ID, text, "build", request, action, decisionID)
	}
	waiting := func(p *core.Project) bool {
		return p.Tasks[p.CurrentTask].Status == "waiting_input" && p.ActiveRun == ""
	}
	if _, err := start("submit", "start", "确认需求", ""); err != nil {
		t.Fatal(err)
	}
	first := waitProject(t, e, p.ID, waiting)
	questionID := first.Tasks[first.CurrentTask].Question.ID
	if _, err := start("answer", "option", "网页", questionID); err != nil {
		t.Fatal(err)
	}
	second := waitProject(t, e, p.ID, waiting)
	if _, err := start("answer", "option", "网页", questionID); err != nil {
		t.Fatalf("retry of an accepted answer: %v", err)
	}
	if _, err := start("answer", "stale", "文档", questionID); err == nil {
		t.Fatal("an answer for the first question was accepted by the second question")
	}
	if _, err := start("answer", "custom", "键盘可操作", second.Tasks[second.CurrentTask].Question.ID); err != nil {
		t.Fatal(err)
	}
	finished := waitProject(t, e, p.ID, func(p *core.Project) bool {
		return p.Tasks[p.CurrentTask].Status == "completed"
	})
	var answers []string
	for _, message := range finished.Messages {
		if message.Kind == "answer" {
			answers = append(answers, message.Text)
		}
	}
	if len(answers) != 2 || answers[0] != "网页" || answers[1] != "键盘可操作" || len(finished.Runs) != 3 {
		t.Fatalf("unexpected answers or runs: %v, %d runs", answers, len(finished.Runs))
	}
}

func TestPlanApprovalAndRealToolWrites(t *testing.T) {
	var mu sync.Mutex
	counts := map[string]int{}
	g := fakeModel(func(ctx context.Context, m []core.ModelMessage, tools []model.Tool) (core.ModelMessage, core.Usage, error) {
		mu.Lock()
		defer mu.Unlock()
		role := "Mike"
		if strings.Contains(m[0].Content, "的 Alex。") {
			role = "Alex"
		}
		n := counts[role]
		counts[role]++
		u := core.Usage{Known: true, Input: 1, Output: 1, Total: 2}
		if role == "Mike" {
			switch n {
			case 0:
				return call("propose_plan", map[string]any{"plan": "写一份说明文档", "acceptance": []string{"正文存在"}}), u, nil
			case 1:
				return call("assign_step", map[string]any{"role": "Alex", "goal": "写 notes.md 然后检查"}), u, nil
			default:
				return call("finish_task", map[string]any{"summary": "文档已保存并检查", "answer_only": false}), u, nil
			}
		}
		switch n {
		case 0:
			return call("write_file", map[string]any{"path": "notes.md", "content": "# 完成\n真实写入", "old_hash": ""}), u, nil
		case 1:
			return call("check_artifacts", map[string]any{}), u, nil
		default:
			return core.ModelMessage{Role: "assistant", Content: "notes.md 已保存，检查通过"}, u, nil
		}
	})
	e, p := testEngine(t, g)
	if _, err := e.Start(core.RunActor{Workspace: "alice"}, "alice", p.ID, "写说明", "plan", "start", "submit", ""); err != nil {
		t.Fatal(err)
	}
	p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.Tasks[p.CurrentTask].Status == "waiting_plan" })
	if len(p.Files) != 0 {
		t.Fatal("modified files before approval")
	}
	if _, err := e.Start(core.RunActor{Workspace: "alice"}, "alice", p.ID, "", "plan", "bad", "approve", "outdated"); err == nil {
		t.Fatal("old approval accepted")
	}
	revision := p.Tasks[p.CurrentTask].Plan.Revision
	if _, err := e.Start(core.RunActor{Workspace: "alice"}, "alice", p.ID, "", "plan", "approve", "approve", revision); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(core.RunActor{Workspace: "alice"}, "alice", p.ID, "", "plan", "approve", "approve", revision); err != nil {
		t.Fatal("duplicate request should return existing state", err)
	}
	p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.Tasks[p.CurrentTask].Status == "completed" })
	if len(p.Versions) != 1 || len(p.Files) != 1 || len(p.Runs) != 2 {
		t.Fatalf("invalid result: %s", core.JSON(p.Public()))
	}
	b, err := e.Store.Blob("alice", p.Files["notes.md"].Hash)
	if err != nil || !strings.Contains(string(b), "真实写入") {
		t.Fatal("missing actual file")
	}
}
func TestStopThenContinueKeepsCommittedFiles(t *testing.T) {
	paused := make(chan struct{})
	var mu sync.Mutex
	alexCalls, mikeCalls := 0, 0
	g := fakeModel(func(ctx context.Context, m []core.ModelMessage, tools []model.Tool) (core.ModelMessage, core.Usage, error) {
		mu.Lock()
		role := "Mike"
		if strings.Contains(m[0].Content, "的 Alex。") {
			role = "Alex"
		}
		if role == "Mike" {
			n := mikeCalls
			mikeCalls++
			mu.Unlock()
			if n == 0 {
				return call("assign_step", map[string]any{"role": "Alex", "goal": "写 a/b/c 三份文件并检查"}), core.Usage{}, nil
			}
			return call("finish_task", map[string]any{"summary": "三份文件完成", "answer_only": false}), core.Usage{}, nil
		}
		n := alexCalls
		alexCalls++
		mu.Unlock()
		switch n {
		case 0, 1:
			name := []string{"a.txt", "b.txt"}[n]
			return call("write_file", map[string]any{"path": name, "content": "saved-" + name, "old_hash": ""}), core.Usage{}, nil
		case 2:
			close(paused)
			<-ctx.Done()
			return core.ModelMessage{}, core.Usage{}, ctx.Err()
		case 3:
			return call("write_file", map[string]any{"path": "c.txt", "content": "remaining", "old_hash": ""}), core.Usage{}, nil
		case 4:
			return call("check_artifacts", map[string]any{}), core.Usage{}, nil
		default:
			return core.ModelMessage{Role: "assistant", Content: "三份文件完成"}, core.Usage{}, nil
		}
	})
	e, p := testEngine(t, g)
	if _, err := e.Start(core.RunActor{Workspace: "alice"}, "alice", p.ID, "三份文件", "build", "start", "submit", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-paused:
	case <-time.After(5 * time.Second):
		t.Fatal("did not reach third file")
	}
	before, _ := e.Store.Get("alice", p.ID)
	if len(before.Files) != 2 {
		t.Fatal("first two files not saved")
	}
	if _, err := e.Stop("alice", p.ID, false); err != nil {
		t.Fatal(err)
	}
	p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.Tasks[p.CurrentTask].Status == "stopped" })
	if _, err := e.Start(core.RunActor{Workspace: "alice"}, "alice", p.ID, "", "build", "resume", "continue", ""); err != nil {
		t.Fatal(err)
	}
	p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.Tasks[p.CurrentTask].Status == "completed" })
	for name, f := range before.Files {
		if p.Files[name] != f {
			t.Fatal("committed file was regenerated")
		}
	}
	if len(p.Files) != 3 || len(p.Runs) != 2 {
		t.Fatal("invalid continued task")
	}
	writes := 0
	for _, step := range p.Tasks[p.CurrentTask].Steps {
		if step.Tool == "write_file" {
			writes++
		}
	}
	if writes != 3 {
		t.Fatalf("duplicate writes: %d", writes)
	}
}
func TestPlanCannotBypassImplementationGate(t *testing.T) {
	e, p := testEngine(t, fakeModel(func(context.Context, []core.ModelMessage, []model.Tool) (core.ModelMessage, core.Usage, error) {
		return core.ModelMessage{}, core.Usage{}, errors.New("not used")
	}))
	_, err := e.Store.Mutate("alice", p.ID, nil, func(p *core.Project) error {
		p.ActiveRun = "r"
		p.Epoch = 1
		p.Runs["r"] = &core.Run{ID: "r", Status: "running"}
		p.Tasks["t"] = &core.Task{ID: "t", Mode: "plan", Frames: map[string]*core.Frame{"f": {ID: "f", Role: "Alex"}}, Steps: map[string]core.Step{}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	c := call("write_file", map[string]any{"path": "app.js", "content": "alert(1)", "old_hash": ""}).ToolCalls[0]
	args := jsonValue(c.Function.Arguments).(map[string]any)
	result, err := e.execute(context.Background(), scope{Owner: "alice", Project: p.ID, Run: "r", Task: "t", Frame: "f", Epoch: 1}, c, args)
	if err != nil || !strings.Contains(result, "尚未批准") {
		t.Fatal("gate bypass", result, err)
	}
	p, _ = e.Store.Get("alice", p.ID)
	if len(p.Files) != 0 {
		t.Fatal("unauthorized file write")
	}
}
