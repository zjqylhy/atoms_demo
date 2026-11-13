package agent

import (
	"context"
	"fmt"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"strings"
	"testing"
)

func TestPolicyUsesFactsNotToolNamesOrWrites(t *testing.T) {
	for _, name := range []string{"database_query", "renamed_lookup", "write_file"} {
		task := &core.Task{ID: "stable"}
		fact := core.ActionFact{Disposition: "executed", Deterministic: true, ErrorClass: "execution", ErrorCode: "schema_missing", Inputs: core.Manifest{"query": {Hash: "H1"}}, Conditions: "environment-v1"}
		fact.ActionKey = actionSignature(name, `{"b":2,"a":1}`, fact)
		fact.Fingerprint = failureFingerprint(name, `{"a":1,"b":2}`, fact)
		for i := 1; i <= 3; i++ {
			if observeFact(task, core.Step{ID: fmt.Sprint(i), Tool: name, Fact: &fact}, 3) != (i == 3) {
				t.Fatal("wrong threshold", name, i)
			}
		}
		if obs := observation(task); obs.Actions[fact.ActionKey] != fact.Fingerprint {
			t.Fatal("lost observation")
		}
		for _, class := range []string{"transient", "unknown", ""} {
			other := fact
			other.ErrorClass = class
			other.Deterministic = false
			if observeFact(task, core.Step{ID: "external", Fact: &other}, 3) {
				t.Fatal("external uncertainty treated as deterministic")
			}
		}
		write := core.ActionFact{Disposition: "executed", Outputs: core.Manifest{"different": {Hash: "changed"}}}
		for i := 0; i < 20; i++ {
			if observeFact(task, core.Step{ID: "write", Tool: name, Fact: &write}, 3) {
				t.Fatal("normal writes treated as stalled")
			}
		}
		if len(observation(task).Verified) != 0 {
			t.Fatal("writes counted as verified progress")
		}
		for i := 0; i < 10; i++ {
			observeVerification(task, core.Report{ID: fmt.Sprint(i), Kind: "test", Digest: "H1", Status: "passed", Checks: []core.Check{{Name: "same_assertion", Status: "passed"}}})
		}
		if len(observation(task).Verified) != 1 {
			t.Fatal("same evidence counted repeatedly")
		}
	}
}

func TestReassignRenameAndContinueDoNotReopenBlindRetry(t *testing.T) {
	rootCalls := 0
	g := fakeModel(func(_ context.Context, m []core.ModelMessage, _ []model.Tool) (core.ModelMessage, core.Usage, error) {
		if strings.Contains(m[0].Content, "的 Mike。") {
			rootCalls++
			return call("assign_step", map[string]any{"role": []string{"Alex", "Bob", "David", "Emma"}[rootCalls%4], "goal": fmt.Sprintf("不同描述-%d", rootCalls)}), core.Usage{}, nil
		}
		return call("read_file", map[string]any{"path": "missing.txt"}), core.Usage{}, nil
	})
	e, p := testEngine(t, g)
	e.Config.RepeatFailures = 2
	e.Config.ReplanBudget = 2
	for _, action := range []string{"submit", "continue"} {
		_, err := e.Start(core.RunActor{Workspace: "alice"}, "alice", p.ID, "核对缺少输入", "build", action, action, "")
		if err != nil {
			t.Fatal(err)
		}
		p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
		task := p.Tasks[p.CurrentTask]
		obs := observation(task)
		if obs.Replans < 2 || task.LastHandoff == nil {
			t.Fatal("observation lost", obs)
		}
		count := 0
		for _, step := range task.Steps {
			if step.Tool == "read_file" {
				count++
			}
		}
		if count != 2 {
			t.Fatal("blind retry reopened", action, count)
		}
		for _, frame := range task.Frames {
			if calls, _ := pendingCalls(frame.Messages); len(calls) > 0 {
				t.Fatal("unclosed calls", calls)
			}
		}
	}
	// Changed arguments and actual conditions remain admissible for diagnosis.
	task := p.Tasks[p.CurrentTask]
	fact := core.ActionFact{Inputs: core.Manifest{"missing.txt": {Hash: "now_exists"}}, Conditions: "instruction:0;approved:true"}
	if observation(task).Actions[actionSignature("read_file", `{"path":"missing.txt"}`, fact)] != "" {
		t.Fatal("changed input blocked")
	}
}

func TestNormalizedArgumentsPreserveMeaning(t *testing.T) {
	if normalizedArguments(`{"b":2,"a":9007199254740993}`) != normalizedArguments(`{ "a":9007199254740993, "b":2 }`) {
		t.Fatal("object formatting not normalized")
	}
	if normalizedArguments(`{"n":9007199254740993}`) == normalizedArguments(`{"n":9007199254740992}`) {
		t.Fatal("integer precision lost")
	}
}
