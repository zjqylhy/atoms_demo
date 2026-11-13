package store

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestAccountClaimPreservesProjectsQuotaAndRevokesLegacyCookie(t *testing.T) {
	file := filepath.Join(t.TempDir(), "account.db")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	old := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	workspace, err := s.Workspace(old, true)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Create(workspace, "old project")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReserveCall(workspace, 20, 2); err != nil {
		t.Fatal(err)
	}
	guest, err := s.Session(old, true)
	if err != nil {
		t.Fatal(err)
	}
	accountToken, err := s.Register(guest, "alice", "encoded-password")
	if err != nil {
		t.Fatal(err)
	}
	account, err := s.Session(accountToken, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Session(old, true); !errors.Is(err, ErrSession) {
		t.Fatalf("old cookie accepted: %v", err)
	}
	if _, err = s.Workspace(old, false); err == nil {
		t.Fatal("legacy server can still resolve claimed cookie")
	}
	_, other, err := s.NewGuest()
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.Create(other.Workspace, "imported project")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReserveCall(other.Workspace, 20, 2); err != nil {
		t.Fatal(err)
	}
	secondToken, err := s.Login(account.UserID, &other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Login(account.UserID, &other); err == nil {
		t.Fatal("guest claimed twice")
	}
	if err = s.ReserveCall(workspace, 20, 2); err == nil {
		t.Fatal("claim reset quota")
	}
	s.Close()
	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	account, err = s.Session(secondToken, false)
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.List(account.Workspace)
	if err != nil || len(list) != 2 {
		t.Fatalf("projects lost: %v %v", list, err)
	}
	for _, id := range []string{p.ID, q.ID} {
		if _, err = s.ProjectOwner(account, id); err != nil {
			t.Fatal(err)
		}
	}
	_, stranger, _ := s.NewGuest()
	if _, err = s.ProjectOwner(stranger, p.ID); err == nil {
		t.Fatal("cross-account access")
	}
	if err = s.RevokeSession(account.TokenHash); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Session(secondToken, false); !errors.Is(err, ErrSession) {
		t.Fatal("revoked session accepted", err)
	}
}

func TestAuthThrottleSurvivesRestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "throttle.db")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err = s.AuthAttempt("name", "127.0.0.1"); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.AuthAttempt("name", "127.0.0.1"); !errors.Is(err, ErrAuthLimit) {
		t.Fatal("throttle reset", err)
	}
}
