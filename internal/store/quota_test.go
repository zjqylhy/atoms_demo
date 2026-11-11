package store

import (
	"path/filepath"
	"testing"
)

func TestQuotaPersistsAcrossRestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "quota.db")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if s.ReserveCall("a", 3, 2) != nil || s.ReserveCall("a", 3, 2) != nil {
		t.Fatal("early quota failure")
	}
	if s.ReserveCall("a", 3, 2) == nil {
		t.Fatal("workspace quota ignored")
	}
	s.Close()
	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.ReserveCall("a", 3, 2) == nil {
		t.Fatal("restart reset quota")
	}
	if s.ReserveCall("b", 3, 2) != nil {
		t.Fatal("global quota exhausted early")
	}
	if s.ReserveCall("c", 3, 2) == nil {
		t.Fatal("global quota ignored")
	}
}
