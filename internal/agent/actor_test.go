package agent

import (
	"context"
	"errors"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"testing"
	"time"
)

func TestSharedRunChargesItsInitiator(t *testing.T) {
	e, p := testEngine(t, fakeModel(func(context.Context, []core.ModelMessage, []model.Tool) (core.ModelMessage, core.Usage, error) {
		return core.ModelMessage{}, core.Usage{}, errors.New("test model failure")
	}))
	e.Config.WorkspaceDailyCalls = 1
	started, err := e.Start(core.RunActor{UserID: "bob-id", Workspace: "bob"}, p.Owner, p.ID, "test", "build", "actor-run", "submit", "")
	if err != nil {
		t.Fatal(err)
	}
	saved := waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
	run := saved.Runs[started.ActiveRun]
	if run.Actor == nil || run.Actor.Workspace != "bob" || run.Actor.UserID != "bob-id" || run.Calls != 1 {
		t.Fatalf("actor not persisted: %+v", run)
	}
	if err = e.Store.ReserveCall("bob", 0, 1); err == nil {
		t.Fatal("initiator was not charged")
	}
	if err = e.Store.ReserveCall("alice", 0, 1); err != nil {
		t.Fatal("storage owner was charged", err)
	}
}

func TestSharedRunConcurrencyFollowsInitiator(t *testing.T) {
	called := make(chan struct{}, 3)
	e, p := testEngine(t, fakeModel(func(ctx context.Context, _ []core.ModelMessage, _ []model.Tool) (core.ModelMessage, core.Usage, error) {
		called <- struct{}{}
		<-ctx.Done()
		return core.ModelMessage{}, core.Usage{}, ctx.Err()
	}))
	e.Config.MaxConcurrent = 3
	q, _ := e.Store.Create("alice", "another shared project")
	r, _ := e.Store.Create("carol", "third shared project")
	if _, err := e.Start(core.RunActor{Workspace: "bob"}, p.Owner, p.ID, "test", "build", "one", "submit", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("model did not start")
	}
	if _, err := e.Start(core.RunActor{Workspace: "bob"}, r.Owner, r.ID, "test", "build", "two", "submit", ""); err == nil {
		t.Fatal("same initiator ran twice")
	}
	if _, err := e.Start(core.RunActor{Workspace: "carol"}, q.Owner, q.ID, "test", "build", "three", "submit", ""); err != nil {
		t.Fatal("storage owner incorrectly limits another account", err)
	}
	select {
	case <-called:
	case <-time.After(3 * time.Second):
		t.Fatal("second initiator did not start")
	}
	e.Stop(p.Owner, p.ID, false)
	e.Stop(q.Owner, q.ID, false)
	waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
	waitProject(t, e, q.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
}
