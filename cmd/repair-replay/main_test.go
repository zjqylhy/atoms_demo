package main

import (
	"mockatoms/internal/core"
	"testing"
)

func TestOrdinaryReplayContinuePreservesRootEvidenceAndBudgets(t *testing.T) {
	p := core.NewProject("owner", "source")
	p.CurrentTask = "task"
	p.Tasks["task"] = &core.Task{ID: "task", Root: "root", Status: "failed", Mode: "plan", InstructionMessages: []string{"user-correction"},
		Frames: map[string]*core.Frame{"root": {ID: "root", Role: "Mike", Materials: map[string]core.WorkingMaterial{"input": {Resource: "file:input", Version: "hash", Content: "required input"}}},
			"root/child": {ID: "root/child", Role: "Alex", Delegation: &core.Delegation{CallLimit: 8, Calls: 7}}},
		Observations: map[string]*core.ExecutionObservation{"task:1": {Subgoal: "task", GoalVersion: 1, Replans: 2}}}
	before := core.JSON(p)
	copy := replayProject(p, "isolated", "", true)
	if copy.ID != "isolated" || core.JSON(copy.Tasks["task"]) != core.JSON(p.Tasks["task"]) || len(copy.Messages) != len(p.Messages) {
		t.Fatal("ordinary continue reset context, approval state or observation history")
	}
	copy.Tasks["task"].Frames["root/child"].Delegation.Calls++
	if core.JSON(p) != before {
		t.Fatal("isolated replay changed source state")
	}
	started := replayProject(p, "fresh", "new regression input", false)
	if started.Tasks["task"].Root == "root" || len(started.Tasks["task"].InstructionMessages) != 2 || len(started.Tasks["task"].Frames) != 3 {
		t.Fatal("explicit fresh replay did not retain history and append its input")
	}
}
