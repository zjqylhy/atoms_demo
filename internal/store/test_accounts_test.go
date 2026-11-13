package store

import (
	"mockatoms/internal/core"
	"testing"
)

func testAccount(t *testing.T, s *Store) (string, Session) {
	t.Helper()
	_, guest, err := s.NewGuest()
	if err != nil {
		t.Fatal(err)
	}
	token, err := s.Register(guest, "test_"+core.ID(), "test-only-password-hash")
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.Session(token, false)
	if err != nil {
		t.Fatal(err)
	}
	return token, session
}
