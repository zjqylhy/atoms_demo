package agent

import (
	"context"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"strings"
	"testing"
	"time"
)

func TestDelegationReturnsBeforeParentBudgetExhaustion(t *testing.T) {
	for _, role := range []string{"Emma", "Bob", "Alex", "David"} {
		for _, mode := range []string{"calls", "format", "time", "explicit"} {
			t.Run(role+"/"+mode, func(t *testing.T) {
				childCalls := 0
				g := fakeModel(func(ctx context.Context, m []core.ModelMessage, _ []model.Tool) (core.ModelMessage, core.Usage, error) {
					if strings.Contains(m[0].Content, "的 Mike。") {
						if strings.Contains(core.JSON(m), `\"outcome\"`) {
							return call("ask_user", map[string]any{"question": "交接后由主 Agent 核对下一步"}), core.Usage{}, nil
						}
						return call("assign_step", map[string]any{"role": role, "goal": "核对现有资料", "acceptance": []string{"引用实际证据"}}), core.Usage{}, nil
					}
					childCalls++
					if mode == "format" {
						return core.ModelMessage{Role: "assistant", Content: "bad", InvalidAction: true}, core.Usage{}, &model.ActionFormatError{Reason: "missing calls"}
					}
					if mode == "time" {
						<-ctx.Done()
						return core.ModelMessage{}, core.Usage{}, ctx.Err()
					}
					if mode == "explicit" {
						return call("return_step", map[string]any{"status": "blocked", "reason": "缺少业务数据", "summary": "已保留已有材料", "todo": []string{"补充输入"}}), core.Usage{}, nil
					}
					return call("list_files", map[string]any{}), core.Usage{}, nil
				})
				e, p := testEngine(t, g)
				e.Config.DelegateCalls = 3
				e.Config.DelegateSeconds = 1
				e.Config.RunSeconds = 8
				e.Config.ParentReserveSeconds = 2
				started, err := e.Start(core.RunActor{Workspace: "alice"}, "alice", p.ID, "核对资料", "build", "start", "submit", "")
				if err != nil {
					t.Fatal(err)
				}
				finished := waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
				run := finished.Runs[started.ActiveRun]
				task := finished.Tasks[finished.CurrentTask]
				if run.Status != "waiting_input" || e.Config.MaxCalls-run.Calls < e.parentCalls()-1 || task.LastHandoff == nil {
					t.Fatalf("parent could not consume handoff: %+v", run)
				}
				if (mode == "calls" || mode == "format") && childCalls != 3 {
					t.Fatal("wrong delegate calls", childCalls)
				}
				for _, frame := range task.Frames {
					if frame.ID != task.Root {
						if frame.Outcome == nil || frame.Outcome.Status == "completed" || frame.Delegation.Calls != childCalls {
							t.Fatal("missing structured budget result", frame)
						}
						if calls, _ := pendingCalls(frame.Messages); len(calls) != 0 {
							t.Fatal("unclosed child calls")
						}
					}
				}
				deadline, _ := time.Parse(time.RFC3339Nano, run.Started)
				deadline = deadline.Add(8 * time.Second)
				if time.Until(deadline) < time.Second {
					t.Fatal("parent time reserve unavailable")
				}
			})
		}
	}
}

func TestDispositionClosesCallsWithoutExecuting(t *testing.T) {
	p, task, f := contextFixture()
	s := scope{Task: task.ID, Frame: f.ID}
	c := call("renamed_nonfile_tool", map[string]any{"argument": "value"})
	f.Messages = append(f.Messages, c)
	closeUnstarted(p, s, c.ToolCalls, "delegate budget")
	if pending, _ := pendingCalls(f.Messages); len(pending) != 0 {
		t.Fatal("pending calls survived")
	}
	if len(task.Steps) != 0 || len(task.Dispositions) != 1 {
		t.Fatal("unstarted call counted as execution")
	}
	for _, d := range task.Dispositions {
		if d.Fact.Disposition != "not_started" {
			t.Fatal(d)
		}
	}
}

func TestClosedDispositionCannotReplayEvenWithProviderIDReuse(t *testing.T) {
	e, p := testEngine(t, nil)
	s := scope{Owner: "alice", Project: p.ID, Task: "task", Run: "run", Frame: "root", Epoch: 1}
	message := call("write_file", map[string]any{"path": "never.txt", "content": "not started", "old_hash": ""})
	c := message.ToolCalls[0]
	_, err := e.Store.Mutate(s.Owner, p.ID, nil, func(p *core.Project) error {
		p.CurrentTask, p.ActiveRun, p.Epoch = s.Task, s.Run, s.Epoch
		p.Runs[s.Run] = &core.Run{ID: s.Run, Status: "running"}
		p.Tasks[s.Task] = &core.Task{ID: s.Task, Root: s.Frame, Mode: "build", Steps: map[string]core.Step{}, Frames: map[string]*core.Frame{s.Frame: {ID: s.Frame, Role: "Alex", Messages: []core.ModelMessage{message}}}}
		closeUnstarted(p, s, message.ToolCalls, "budget")
		p.Tasks[s.Task].Frames[s.Frame].Messages = append(p.Tasks[s.Task].Frames[s.Frame].Messages, message)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := e.record(s, c, nil, func(*core.Project) (any, error) {
		t.Fatal("closed call executed again")
		return nil, nil
	})
	if err != nil || !strings.Contains(result, `"not_started"`) {
		t.Fatal("closed disposition not returned", result, err)
	}
	p, _ = e.Store.Get(s.Owner, p.ID)
	task := p.Tasks[s.Task]
	pending, _ := pendingCalls(task.Frames[s.Frame].Messages)
	if len(pending) != 0 || len(task.Steps) != 0 || p.Runs[s.Run].Tools != 0 || len(task.Checkpoints) != 0 {
		t.Fatal("repeated closed call changed execution accounting")
	}
	c.Function.Arguments = `{"path":"different.txt"}`
	if _, err := e.record(s, c, nil, func(*core.Project) (any, error) { return nil, nil }); err == nil {
		t.Fatal("provider ID reused for different arguments")
	}
}

func TestDelegationPassesReadEvidenceInBothDirections(t *testing.T) {
	rootCalls, alexCalls := 0, 0
	g := fakeModel(func(_ context.Context, messages []core.ModelMessage, _ []model.Tool) (core.ModelMessage, core.Usage, error) {
		wire := core.JSON(messages)
		if strings.Contains(messages[0].Content, "的 Alex。") {
			alexCalls++
			if !strings.Contains(wire, "PARENT_READ_EVIDENCE") {
				t.Error("child lost the parent's actual read")
			}
			if alexCalls == 1 {
				return call("read_file", map[string]any{"path": "second.txt"}), core.Usage{}, nil
			}
			return call("return_step", map[string]any{"status": "needs_replan", "reason": "read complete; continue in another subgoal", "summary": "材料已读取", "todo": []string{"review evidence"}}), core.Usage{}, nil
		}
		if strings.Contains(messages[0].Content, "的 Bob。") {
			for _, expected := range []string{"PARENT_READ_EVIDENCE", "CHILD_READ_EVIDENCE"} {
				if !strings.Contains(wire, expected) {
					t.Errorf("new delegation rerequired saved evidence %s", expected)
				}
			}
			return call("return_step", map[string]any{"status": "completed", "reason": "evidence reviewed", "summary": "review complete", "todo": []string{}}), core.Usage{}, nil
		}
		rootCalls++
		switch rootCalls {
		case 1:
			return call("read_file", map[string]any{"path": "first.txt"}), core.Usage{}, nil
		case 2:
			return call("assign_step", map[string]any{"role": "Alex", "goal": "inspect second input"}), core.Usage{}, nil
		case 3:
			if !strings.Contains(wire, "CHILD_READ_EVIDENCE") {
				t.Error("parent received a reference without the child's active material")
			}
			return call("assign_step", map[string]any{"role": "Bob", "goal": "review existing evidence"}), core.Usage{}, nil
		default:
			return call("finish_task", map[string]any{"summary": "review complete", "answer_only": true}), core.Usage{}, nil
		}
	})
	e, p := testEngine(t, g)
	blobs := map[string][]byte{}
	manifest := core.Manifest{}
	for name, content := range map[string]string{"first.txt": "PARENT_READ_EVIDENCE", "second.txt": "CHILD_READ_EVIDENCE"} {
		hash := core.Hash([]byte(content))
		blobs[hash] = []byte(content)
		manifest[name] = core.File{Hash: hash, Size: len(content)}
	}
	if _, err := e.Store.Mutate("alice", p.ID, blobs, func(p *core.Project) error { p.Files = manifest; return nil }); err != nil {
		t.Fatal(err)
	}
	started, err := e.Start(core.RunActor{Workspace: "alice"}, "alice", p.ID, "review inputs", "build", "start", "submit", "")
	if err != nil {
		t.Fatal(err)
	}
	p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
	if p.Runs[started.ActiveRun].Status != "completed" || p.Runs[started.ActiveRun].Calls != 7 {
		t.Fatal("delegation evidence flow failed", core.JSON(p.Runs[started.ActiveRun]))
	}
}
