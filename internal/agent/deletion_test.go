package agent

import (
	"context"
	"errors"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"mockatoms/internal/store"
	"testing"
	"time"
)

func engineAccount(t *testing.T, e *Engine) store.Session {
	t.Helper()
	_, guest, err := e.Store.NewGuest()
	if err != nil {
		t.Fatal(err)
	}
	token, err := e.Store.Register(guest, "deleter", "test-only-hash")
	if err != nil {
		t.Fatal(err)
	}
	account, err := e.Store.Session(token, false)
	if err != nil {
		t.Fatal(err)
	}
	return account
}
func TestDeleteWaitsForStoppedRunCleanup(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	e, p := testEngine(t, fakeModel(func(ctx context.Context, _ []core.ModelMessage, _ []model.Tool) (core.ModelMessage, core.Usage, error) {
		close(entered)
		<-release
		return core.ModelMessage{}, core.Usage{}, ctx.Err()
	}))
	account := engineAccount(t, e)
	if _, err := e.Start(core.RunActor{Workspace: account.Workspace}, p.Owner, p.ID, "test", "build", "start", "submit", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("model not started")
	}
	if err := e.DeleteProject(account.TokenHash, p.ID); !errors.Is(err, store.ErrProjectRunning) {
		t.Fatal(err)
	}
	if _, err := e.Stop(p.Owner, p.ID, false); err != nil {
		t.Fatal(err)
	}
	saved, _ := e.Store.Get(p.Owner, p.ID)
	if saved.ActiveRun != "" {
		t.Fatal("stop not persisted")
	}
	if err := e.DeleteProject(account.TokenHash, p.ID); !errors.Is(err, store.ErrProjectRunning) {
		t.Fatal("deleted during cleanup", err)
	}
	close(release)
	waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
	if err := e.DeleteProject(account.TokenHash, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.Mutate(p.Owner, p.ID, nil, func(*core.Project) error { return nil }); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("late mutation resurrected project", err)
	}
}

func TestStartAndDeleteShareAdmissionLock(t *testing.T) {
	e, _ := testEngine(t, fakeModel(func(ctx context.Context, _ []core.ModelMessage, _ []model.Tool) (core.ModelMessage, core.Usage, error) {
		<-ctx.Done()
		return core.ModelMessage{}, core.Usage{}, ctx.Err()
	}))
	account := engineAccount(t, e)
	for i := 0; i < 12; i++ {
		p, _ := e.Store.Create("alice", "race")
		gate := make(chan struct{})
		started, deleted := make(chan error, 1), make(chan error, 1)
		go func() {
			<-gate
			_, err := e.Start(core.RunActor{Workspace: account.Workspace}, p.Owner, p.ID, "test", "build", "race", "submit", "")
			started <- err
		}()
		go func() { <-gate; deleted <- e.DeleteProject(account.TokenHash, p.ID) }()
		close(gate)
		startErr, deleteErr := <-started, <-deleted
		if deleteErr == nil {
			if !errors.Is(startErr, core.ErrNotFound) {
				t.Fatal("both start and delete admitted", startErr)
			}
		} else {
			if startErr != nil || !errors.Is(deleteErr, store.ErrProjectRunning) {
				t.Fatal(startErr, deleteErr)
			}
			e.Stop(p.Owner, p.ID, false)
			waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
			if err := e.DeleteProject(account.TokenHash, p.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
}
