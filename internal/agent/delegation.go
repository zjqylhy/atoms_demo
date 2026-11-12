package agent

import (
	"context"
	"errors"
	"fmt"
	"mockatoms/internal/core"
	"sort"
	"time"
)

var errDelegateBudget = errors.New("委派预算达到边界")

func positive(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}
func (e *Engine) parentCalls() int { return positive(e.Config.ParentReserveCalls, 3) }
func (e *Engine) parentSeconds() int {
	return positive(e.Config.ParentReserveSeconds, max(1, min(30, e.Config.RunSeconds/4)))
}
func (e *Engine) runDeadline(p *core.Project, s scope) time.Time {
	start, _ := time.Parse(time.RFC3339Nano, p.Runs[s.Run].Started)
	return start.Add(time.Duration(e.Config.RunSeconds) * time.Second)
}

func (e *Engine) newDelegation(p *core.Project, s scope, a map[string]any) (*core.Delegation, error) {
	remaining := e.Config.MaxCalls - p.Runs[s.Run].Calls - e.parentCalls()
	parentDeadline := e.runDeadline(p, s).Add(-time.Duration(e.parentSeconds()) * time.Second)
	if remaining < 1 || time.Until(parentDeadline) <= 0 {
		return nil, errors.New("剩余预算不足以分派并保留主 Agent 收尾预算")
	}
	deadline := time.Now().Add(time.Duration(positive(e.Config.DelegateSeconds, 180)) * time.Second)
	if parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	constraints := []string{p.Tasks[s.Task].Goal}
	for _, r := range taskRequirements(p, p.Tasks[s.Task]) {
		constraints = append(constraints, r.Text)
	}
	acceptance := stringsArg(a, "acceptance")
	if len(acceptance) == 0 && p.Tasks[s.Task].Plan != nil {
		acceptance = p.Tasks[s.Task].Plan.Acceptance
	}
	if len(acceptance) == 0 {
		acceptance = []string{str(a, "goal")}
	}
	inputs := core.Clone(p.Files)
	// A stable task-owned subgoal prevents renaming or re-delegation from clearing history.
	return &core.Delegation{Subgoal: p.Tasks[s.Task].ID, GoalVersion: s.InstructionVersion, Inputs: inputs,
		Constraints: constraints, Acceptance: acceptance, Approach: str(a, "approach"), RunID: s.Run, Started: core.Now(),
		Deadline: deadline.UTC().Format(time.RFC3339Nano), CallLimit: min(remaining, positive(e.Config.DelegateCalls, 8))}, nil
}

func (e *Engine) delegationBoundary(p *core.Project, s scope, pending bool) string {
	t := p.Tasks[s.Task]
	f := t.Frames[s.Frame]
	if s.Frame == t.Root || f.Delegation == nil {
		return ""
	}
	d := f.Delegation
	if d.RunID != s.Run {
		return "上次委派运行已结束，先交回主 Agent 核对进度"
	}
	deadline, _ := time.Parse(time.RFC3339Nano, d.Deadline)
	if !deadline.IsZero() && !time.Now().Before(deadline) {
		return "委派时间预算已用完"
	}
	if e.Config.MaxTools > 0 && p.Runs[s.Run].Tools >= e.Config.MaxTools-2 {
		return "保留主 Agent 的工具收尾预算"
	}
	if !pending && (d.Calls >= d.CallLimit || p.Runs[s.Run].Calls >= e.Config.MaxCalls-e.parentCalls()) {
		return "委派调用预算已用完"
	}
	return ""
}

func (e *Engine) finishDelegation(s scope, status, reason, summary string) (string, error) {
	var outcome core.DelegationResult
	_, err := e.Store.Mutate(s.Owner, s.Project, nil, func(p *core.Project) error {
		if err := p.Fence(s.Run, s.Epoch); err != nil {
			return err
		}
		t := p.Tasks[s.Task]
		f := t.Frames[s.Frame]
		if f.Outcome != nil {
			outcome = *f.Outcome
			return nil
		}
		pending, _ := pendingCalls(f.Messages)
		closeUnstarted(p, s, pending, reason)
		var evidence []string
		for key, step := range t.Steps {
			if step.FrameID == f.ID {
				evidence = append(evidence, key)
			}
		}
		sort.Strings(evidence)
		outcome = core.DelegationResult{Status: status, Reason: reason, Summary: summary, Evidence: evidence, Artifacts: core.Clone(p.Files)}
		if status != "completed" {
			outcome.Todo = []string{f.Goal, "核对实际证据，说明下一段改变的条件和完成标准"}
		}
		f.Complete = true
		f.Outcome = &outcome
		f.Result = core.JSON(outcome)
		t.LastHandoff = &outcome
		p.Event("handoff", f.Role, reason, outcome)
		return nil
	})
	return core.JSON(outcome), err
}

// Budget expiry closes provider calls without pretending they ran. An external
// job remains pending under its original identity and must be reconciled first.
func closeUnstarted(p *core.Project, s scope, calls []core.Call, reason string) {
	t := p.Tasks[s.Task]
	f := t.Frames[s.Frame]
	if t.Dispositions == nil {
		t.Dispositions = map[string]core.Step{}
	}
	for _, call := range calls {
		if job := t.RunnerAction; job != nil && job.FrameID == s.Frame && job.CallID == call.ID {
			continue
		}
		key := s.Frame + "/" + call.ID
		fact := &core.ActionFact{Disposition: "not_started", ErrorClass: "control", ErrorCode: reason}
		result := core.JSON(map[string]any{"ok": false, "skipped": true, "error": reason, "fact": fact})
		t.Dispositions[key] = core.Step{ID: key, FrameID: s.Frame, Tool: call.Function.Name, Role: f.Role, At: core.Now(), Result: result, Fact: fact, ArgsHash: core.Hash([]byte(call.Function.Name + "\n" + call.Function.Arguments))}
		f.Messages = append(f.Messages, core.ModelMessage{Role: "tool", ToolCallID: call.ID, Content: result})
	}
}

func (e *Engine) modelContext(ctx context.Context, f *core.Frame) (context.Context, context.CancelFunc) {
	if f.Delegation != nil {
		if deadline, err := time.Parse(time.RFC3339Nano, f.Delegation.Deadline); err == nil {
			return context.WithDeadline(ctx, deadline)
		}
	}
	return context.WithCancel(ctx)
}

func (e *Engine) delegate(ctx context.Context, s scope, c core.Call, a map[string]any, p *core.Project) (string, error) {
	role := str(a, "role")
	t := p.Tasks[s.Task]
	if !permitted(t) && role != "Emma" && role != "Bob" {
		return e.record(s, c, nil, func(*core.Project) (any, error) { return nil, errors.New("批准前只能分派需求或设计规划") })
	}
	childID := s.Frame + "/" + c.ID
	_, err := e.Store.Mutate(s.Owner, s.Project, nil, func(p *core.Project) error {
		if err := s.checkInstruction(p); err != nil {
			return err
		}
		t := p.Tasks[s.Task]
		if old := t.Frames[childID]; old != nil && !old.Complete {
			if old.Delegation == nil {
				d, err := e.newDelegation(p, s, a)
				if err != nil {
					return err
				}
				old.Delegation = d
			} else if d := old.Delegation; d.RunID != s.Run {
				// Resume the unused portion of the original contract, never reset it.
				deadline, _ := time.Parse(time.RFC3339Nano, d.Deadline)
				ended, _ := time.Parse(time.RFC3339Nano, p.Runs[d.RunID].Ended)
				remaining := max(time.Duration(0), deadline.Sub(ended))
				next := time.Now().Add(remaining)
				parent := e.runDeadline(p, s).Add(-time.Duration(e.parentSeconds()) * time.Second)
				if parent.Before(next) {
					next = parent
				}
				d.Deadline = next.UTC().Format(time.RFC3339Nano)
				d.RunID = s.Run
			}
		}
		if t.Frames[childID] == nil {
			d, err := e.newDelegation(p, s, a)
			if err != nil {
				return err
			}
			packet := map[string]any{"goal": str(a, "goal"), "task_goal": t.Goal, "plan": t.Plan, "contract": d,
				"handoff": "预算内未完成使用 return_step 返回 needs_replan 或 blocked；技术路线是模型建议，不是用户要求"}
			t.Frames[childID] = &core.Frame{ID: childID, Role: role, Goal: str(a, "goal"), Delegation: d, Messages: []core.ModelMessage{{Role: "user", Content: core.JSON(packet)}}}
			p.Event("handoff", "Mike", "交给 "+role+"："+str(a, "goal"), d)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, core.ErrStale) || errors.Is(err, errNewInstruction) {
			return "", err
		}
		return e.record(s, c, nil, func(*core.Project) (any, error) { return nil, err })
	}
	child := s
	child.Frame = childID
	childCtx, cancel := context.WithDeadline(ctx, e.runDeadline(p, s).Add(-time.Duration(e.parentSeconds())*time.Second))
	defer cancel()
	result, err := e.runFrame(childCtx, child)
	redirected := errors.Is(err, errNewInstruction)
	if err != nil && !redirected {
		if errors.Is(err, core.ErrStale) || errors.Is(err, core.ErrWaiting) || ctx.Err() != nil {
			return "", err
		}
		status := "blocked"
		if errors.Is(err, errDelegateBudget) || errors.Is(err, context.DeadlineExceeded) {
			status = "needs_replan"
		}
		result, err = e.finishDelegation(child, status, fmt.Sprintf("子任务交回：%s", e.Config.Redact(err.Error())), "")
		if err != nil {
			return "", err
		}
	}
	return e.record(s, c, nil, func(p *core.Project) (any, error) {
		if redirected {
			return redirectedDelegation(p, role), nil
		}
		return map[string]any{"role": role, "summary": result, "outcome": p.Tasks[s.Task].Frames[childID].Outcome, "files": p.Files}, nil
	})
}

func (e *Engine) waitForModel(ctx context.Context, f *core.Frame, wait time.Duration) error {
	bounded, cancel := e.modelContext(ctx, f)
	defer cancel()
	return waitForRetry(bounded, wait)
}
