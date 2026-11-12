package agent

import (
	"errors"
	"fmt"
	"mockatoms/internal/core"
)

type replanRequired struct{ reason string }

func (e *replanRequired) Error() string { return e.reason }

func observation(t *core.Task) *core.ExecutionObservation {
	key := fmt.Sprintf("%s:%d", t.ID, len(t.InstructionMessages))
	if t.Observations == nil {
		t.Observations = map[string]*core.ExecutionObservation{}
	}
	if t.Observations[key] == nil {
		t.Observations[key] = &core.ExecutionObservation{Subgoal: t.ID, GoalVersion: len(t.InstructionMessages), Failures: map[string]int{}, Actions: map[string]string{}, Verified: map[string]bool{}}
	}
	obs := t.Observations[key]
	if obs.Failures == nil {
		obs.Failures = map[string]int{}
	}
	if obs.Actions == nil {
		obs.Actions = map[string]string{}
	}
	if obs.Verified == nil {
		obs.Verified = map[string]bool{}
	}
	return obs
}

func actionSignature(tool, args string, fact core.ActionFact) string {
	fact.ErrorCode = ""
	return failureFingerprint(tool, args, fact)
}

// Only explicit deterministic facts can exhaust retries. Writes and repeated
// report IDs never count as new acceptance evidence, or by themselves as failure.
func observeFact(t *core.Task, step core.Step, threshold int) bool {
	if step.Fact == nil {
		return false
	}
	fact := step.Fact
	obs := observation(t)
	obs.Evidence = append(obs.Evidence, step.ID)
	if len(obs.Evidence) > 24 {
		obs.Evidence = obs.Evidence[len(obs.Evidence)-24:]
	}
	if !fact.Deterministic || fact.ErrorCode == "" || fact.Fingerprint == "" || fact.ErrorClass == "transient" || fact.ErrorClass == "unknown" {
		return false
	}
	obs.Failures[fact.Fingerprint]++
	// Store the exact attempted conditions separately from the returned error.
	obs.Actions[fact.ActionKey] = fact.Fingerprint
	if obs.Failures[fact.Fingerprint] >= threshold {
		obs.Reason = "相同参数、输入版本和执行条件重复产生同一确定性失败；请检查证据并改变策略，原样重试已关闭"
		return true
	}
	return false
}

func (e *Engine) admitTool(s scope, c core.Call, fact core.ActionFact) error {
	p, t, _, err := e.load(s)
	if err != nil {
		return err
	}
	if err := s.checkInstruction(p); err != nil {
		return err
	}
	obs := observation(t)
	signature := actionSignature(c.Function.Name, c.Function.Arguments, fact)
	fingerprint := obs.Actions[signature]
	if fingerprint == "" || obs.Failures[fingerprint] < positive(e.Config.RepeatFailures, 3) {
		return nil
	}
	reason := obs.Reason
	if obs.Replans >= positive(e.Config.ReplanBudget, 2) {
		reason += "；此问题的恢复预算已用完，已保存成果，仍可诊断或执行条件已改变的动作"
	}
	_, err = e.Store.Mutate(s.Owner, s.Project, nil, func(p *core.Project) error {
		if err := s.checkInstruction(p); err != nil {
			return err
		}
		t := p.Tasks[s.Task]
		obs := observation(t)
		obs.Replans++
		if obs.Replans >= positive(e.Config.ReplanBudget, 2) {
			obs.StopRun = s.Run
		}
		pending, _ := pendingCalls(t.Frames[s.Frame].Messages)
		closeUnstarted(p, s, pending, reason)
		t.LastHandoff = &core.DelegationResult{Status: "blocked", Reason: reason, Evidence: obs.Evidence, Artifacts: core.Clone(p.Files), Todo: []string{"核对失败条件；保留原记录，有限诊断或调整策略"}}
		p.Event("execution_blocked", t.Frames[s.Frame].Role, reason, t.LastHandoff)
		return nil
	})
	if err != nil {
		return err
	}
	return &replanRequired{reason: reason}
}

func (e *Engine) policyError(s scope, err error) (string, error) {
	var replan *replanRequired
	if !errors.As(err, &replan) {
		return "", err
	}
	_, t, _, loadErr := e.load(s)
	if loadErr != nil {
		return "", loadErr
	}
	if s.Frame == t.Root {
		return "", replan
	}
	return e.finishDelegation(s, "needs_replan", replan.reason, "")
}

func observeVerification(t *core.Task, r core.Report) {
	if r.Status != "passed" {
		return
	}
	items := []string{}
	for _, c := range r.Checks {
		if c.Status == "passed" {
			items = append(items, c.Name)
		}
	}
	if len(items) == 0 {
		return
	}
	key := core.Hash([]byte(r.Kind + "\n" + r.Digest + "\n" + core.JSON(items)))
	observation(t).Verified[key] = true
}
