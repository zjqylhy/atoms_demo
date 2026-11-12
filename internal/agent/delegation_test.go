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
				started, err := e.Start("alice", p.ID, "核对资料", "build", "start", "submit", "")
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
