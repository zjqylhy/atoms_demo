package store

import (
	"mockatoms/internal/core"
	"path/filepath"
	"testing"
)

func TestPresetCopiesAreAtomicIsolatedAndIdempotent(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "samples.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, a, _ := s.NewGuest()
	_, b, _ := s.NewGuest()
	p, err := s.CopyPreset(a.TokenHash, "board", "copy-request-1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CopyPreset(a.TokenHash, "board", "copy-request-1")
	if err != nil || again.ID != p.ID {
		t.Fatal(again, err)
	}
	if _, err = s.CopyPreset(a.TokenHash, "reading", "copy-request-1"); err == nil {
		t.Fatal("reused request")
	}
	q, err := s.CopyPreset(b.TokenHash, "board", "copy-request-2")
	if err != nil || q.ID == p.ID {
		t.Fatal(q, err)
	}
	grant, err := s.PreviewAccess(a.TokenHash, p.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveAppState(a.TokenHash, p.ID, SaveAppRequest{Grant: grant.Grant, Schema: 1, Expected: 0, RequestID: "change-record", Value: []byte(`{"items":[]}`)}); err != nil {
		t.Fatal(err)
	}
	other, err := s.PreviewAccess(b.TokenHash, q.ID, "")
	if err != nil || other.State.Revision != 0 || string(other.State.Value) == `{"items":[]}` {
		t.Fatal(other, err)
	}
	if core.PreviewHTML(p.Reports, p.Files) == "" || p.Source.Kind != "preset" {
		t.Fatal("missing packaged preview")
	}
}
