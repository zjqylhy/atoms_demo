package store

import (
	"errors"
	"mockatoms/internal/core"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharePinsVersionAndDoesNotCopyPrivateInputs(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "share.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, owner, _ := s.NewGuest()
	_, guest, _ := s.NewGuest()
	p, err := s.CopyPreset(owner.TokenHash, "board", "share-original")
	if err != nil {
		t.Fatal(err)
	}
	sid, token, err := s.CreateShare(owner.TokenHash, p.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	private := []byte("PRIVATE_ATTACHMENT_NOT_SHARED")
	p, err = s.Mutate(p.Owner, p.ID, map[string][]byte{core.Hash(private): private}, func(p *core.Project) error {
		p.Files["secret.csv"] = core.File{Hash: core.Hash(private), Size: len(private)}
		p.Message("user", string(private), "request")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := s.Shared(token)
	if err != nil || shared.Version != p.Head || strings.Contains(shared.HTML, string(private)) {
		t.Fatal(shared, err)
	}
	copy, err := s.CopyShare(guest.TokenHash, token, "share-copy-123")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := copy.Files["secret.csv"]; ok {
		t.Fatal("private file copied")
	}
	if len(copy.Messages) > 0 || len(copy.Runs) > 0 {
		t.Fatal("private history copied")
	}
	if err = s.RevokeShare(guest.TokenHash, p.ID, sid); !errors.Is(err, core.ErrNotFound) {
		t.Fatal(err)
	}
	if err = s.RevokeShare(owner.TokenHash, p.ID, sid); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Shared(token); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("revoked share readable", err)
	}
}
