package store

import (
	"errors"
	"mockatoms/internal/core"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

func orderIDs(order ProjectOrder) []string {
	ids := make([]string, 0, len(order.Projects))
	for _, p := range order.Projects {
		ids = append(ids, p["id"].(string))
	}
	return ids
}

func TestProjectOrderSharedDurableAndContentIndependent(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "test.db")
	s, err := Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	_, a := testAccount(t, s)
	_, b := testAccount(t, s)
	p1, _ := s.Create(a.Workspace, "one")
	p2, _ := s.Create(b.Workspace, "two")
	p3, _ := s.Create(a.Workspace, "three")
	initial, err := s.ProjectOrder(a.TokenHash)
	if err != nil || !slices.Equal(orderIDs(initial), []string{p3.ID, p2.ID, p1.ID}) {
		t.Fatalf("legacy order %v %v", initial, err)
	}
	want := []string{p1.ID, p3.ID, p2.ID}
	saved, err := s.SaveProjectOrder(a.TokenHash, initial.Revision, want)
	if err != nil || saved.Revision != initial.Revision+1 {
		t.Fatal(saved, err)
	}
	if _, err = s.Mutate(b.Workspace, p2.ID, nil, func(p *core.Project) error { p.Title = "two edited"; return nil }); err != nil {
		t.Fatal(err)
	}
	shared, err := s.ProjectOrder(b.TokenHash)
	if err != nil || !slices.Equal(orderIDs(shared), want) {
		t.Fatalf("cross-account/content change %v %v", shared, err)
	}
	if shared.Projects[2]["title"] != "two edited" {
		t.Fatal("stale title")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Recover(); err != nil {
		t.Fatal(err)
	}
	shared, err = s.ProjectOrder(a.TokenHash)
	if err != nil || shared.Revision != saved.Revision || !slices.Equal(orderIDs(shared), want) {
		t.Fatalf("restart %v %v", shared, err)
	}
	p4, err := s.Create(b.Workspace, "four")
	if err != nil {
		t.Fatal(err)
	}
	want = append([]string{p4.ID}, want...)
	if _, err = s.Mutate(a.Workspace, p1.ID, nil, func(p *core.Project) error { p.Title = "one edited"; return nil }); err != nil {
		t.Fatal(err)
	}
	shared, err = s.ProjectOrder(b.TokenHash)
	if err != nil || !slices.Equal(orderIDs(shared), want) {
		t.Fatalf("new first %v %v", shared, err)
	}
	if _, err = s.SaveProjectOrder(a.TokenHash, saved.Revision, want[1:]); !errors.Is(err, ErrOrderConflict) {
		t.Fatal("missing new project", err)
	}
	if err = s.DeleteProject(b.TokenHash, p3.ID); err != nil {
		t.Fatal(err)
	}
	shared, err = s.ProjectOrder(a.TokenHash)
	if err != nil || !slices.Equal(orderIDs(shared), []string{p4.ID, p1.ID, p2.ID}) {
		t.Fatalf("delete preserves order %v %v", shared, err)
	}
	if _, err = s.SaveProjectOrder(a.TokenHash, saved.Revision, want); !errors.Is(err, ErrOrderConflict) {
		t.Fatal("deleted project", err)
	}
}

func TestRecoverPreservesIdleProjectsAndInitialOrder(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, account := testAccount(t, s)
	one, err := s.Create(account.Workspace, "one")
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.Create(account.Workspace, "two")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Mutate(account.Workspace, one.ID, nil, func(p *core.Project) error { p.Title = "recently edited"; return nil }); err != nil {
		t.Fatal(err)
	}
	before, err := s.ProjectOrder(account.TokenHash)
	if err != nil || !slices.Equal(orderIDs(before), []string{one.ID, two.ID}) {
		t.Fatal(before, err)
	}
	var beforeState []byte
	if err = s.db.QueryRow("SELECT state FROM projects WHERE id=?", one.ID).Scan(&beforeState); err != nil {
		t.Fatal(err)
	}
	if err = s.Recover(); err != nil {
		t.Fatal(err)
	}
	after, err := s.ProjectOrder(account.TokenHash)
	if err != nil || core.JSON(after) != core.JSON(before) {
		t.Fatalf("idle recovery changed order or timestamps: %v %v %v", before, after, err)
	}
	var afterState []byte
	if err = s.db.QueryRow("SELECT state FROM projects WHERE id=?", one.ID).Scan(&afterState); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(beforeState, afterState) {
		t.Fatal("idle recovery rewrote project state")
	}
}

func TestProjectOrderConcurrentWritesValidationAndRevocation(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, a := testAccount(t, s)
	_, b := testAccount(t, s)
	p1, _ := s.Create(a.Workspace, "one")
	p2, _ := s.Create(a.Workspace, "two")
	p3, _ := s.Create(a.Workspace, "three")
	orders := [][]string{{p1.ID, p2.ID, p3.ID}, {p2.ID, p3.ID, p1.ID}}
	errorsOut := make([]error, 2)
	var wg sync.WaitGroup
	for i, session := range []Session{a, b} {
		wg.Add(1)
		go func(i int, session Session) {
			defer wg.Done()
			_, errorsOut[i] = s.SaveProjectOrder(session.TokenHash, 0, orders[i])
		}(i, session)
	}
	wg.Wait()
	success, conflict := 0, 0
	for _, err := range errorsOut {
		if err == nil {
			success++
		} else if errors.Is(err, ErrOrderConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(errorsOut)
	}
	saved, err := s.ProjectOrder(a.TokenHash)
	if err != nil {
		t.Fatal(err)
	}
	// Retrying an acknowledged write after a lost response is harmless.
	retried, err := s.SaveProjectOrder(b.TokenHash, 0, orderIDs(saved))
	if err != nil || retried.Revision != saved.Revision {
		t.Fatal(retried, err)
	}
	for _, tc := range []struct {
		revision int64
		ids      []string
		want     error
	}{
		{saved.Revision, []string{p1.ID, p1.ID, p3.ID}, ErrInvalidOrder},
		{-1, orderIDs(saved), ErrInvalidOrder},
		{saved.Revision, []string{"bad", p2.ID, p3.ID}, ErrInvalidOrder},
		{saved.Revision, []string{core.ID(), p2.ID, p3.ID}, ErrOrderConflict},
	} {
		if _, err = s.SaveProjectOrder(a.TokenHash, tc.revision, tc.ids); !errors.Is(err, tc.want) {
			t.Fatal(tc, err)
		}
	}
	_, guest, err := s.NewGuest()
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeSession(a.TokenHash); err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{"", guest.TokenHash, a.TokenHash} {
		if _, err = s.ProjectOrder(hash); !errors.Is(err, ErrSession) {
			t.Fatal("unauthorized read", err)
		}
		if _, err = s.SaveProjectOrder(hash, saved.Revision, orders[0]); !errors.Is(err, ErrSession) {
			t.Fatal("unauthorized write", err)
		}
	}
	after, err := s.ProjectOrder(b.TokenHash)
	if err != nil || after.Revision != saved.Revision || !slices.Equal(orderIDs(after), orderIDs(saved)) {
		t.Fatal("rejected requests mutated order", after, err)
	}
}
