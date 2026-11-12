package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestModelReservationPersistsAndExpires(t *testing.T) {
	file := filepath.Join(t.TempDir(), "limits.db")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := s.ReserveModelWindow(70, 100); err != nil || wait != 0 {
		t.Fatal(wait, err)
	}
	s.Close()
	s, err = Open(file)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if wait, err := s.ReserveModelWindow(40, 100); err != nil || wait <= 0 {
		t.Fatal("lost reservation", wait, err)
	}
	if _, err := s.ReserveModelWindow(101, 100); err == nil {
		t.Fatal("oversized call admitted")
	}
	if _, err = s.db.Exec("UPDATE model_window SET stamp_ms=?", time.Now().Add(-time.Minute-time.Second).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if wait, err := s.ReserveModelWindow(40, 100); err != nil || wait != 0 {
		t.Fatal("expired reservation retained", wait, err)
	}
}
