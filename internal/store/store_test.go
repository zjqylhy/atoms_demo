package store

import (
	"bytes"
	"errors"
	"mockatoms/internal/core"
	"path/filepath"
	"testing"
)

func TestSnapshotImportKeepsAllReferencedHistory(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "snapshot.db")
	s, err := Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Create("alice", "copy")
	if err != nil {
		t.Fatal(err)
	}
	blobs := map[string][]byte{}
	manifest := func(text string) core.Manifest {
		data := []byte(text)
		blobs[core.Hash(data)] = data
		return core.Manifest{"document.txt": {Hash: core.Hash(data), Size: len(data)}}
	}
	current, base, checkpoint, version := manifest("draft"), manifest("base"), manifest("checkpoint"), manifest("version")
	orphan := []byte("unreferenced")
	blobs[core.Hash(orphan)] = orphan
	_, err = s.Mutate("alice", p.ID, blobs, func(p *core.Project) error {
		p.Files = current
		p.Tasks["task"] = &core.Task{Base: base, Checkpoints: []core.Checkpoint{{Manifest: checkpoint}}}
		p.Versions = []core.Version{{Manifest: version}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, text := range []string{"draft", "base", "checkpoint", "version"} {
		data := []byte(text)
		stored, err := s.Blob("alice", core.Hash(data))
		if err != nil || !bytes.Equal(stored, data) {
			t.Fatalf("lost referenced history %s: %v", text, err)
		}
	}
	if _, err := s.Blob("alice", core.Hash(orphan)); err == nil {
		t.Fatal("imported unreferenced blob")
	}
}

func TestAtomicFileCheckpointFenceAndRestart(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "atoms.db")
	s, e := Open(filename)
	if e != nil {
		t.Fatal(e)
	}
	p, _ := s.Create("alice", "demo")
	content := []byte("hello")
	hash := core.Hash(content)
	_, e = s.Mutate("alice", p.ID, map[string][]byte{hash: content}, func(p *core.Project) error {
		p.Files["a.txt"] = core.File{Hash: hash, Size: 5}
		return errors.New("crash before commit")
	})
	if e == nil {
		t.Fatal("expected rollback")
	}
	p, _ = s.Get("alice", p.ID)
	if len(p.Files) != 0 {
		t.Fatal("partial file visible")
	}
	if _, e = s.Blob("alice", hash); e == nil {
		t.Fatal("rolled-back blob visible")
	}
	_, e = s.Mutate("alice", p.ID, map[string][]byte{hash: content}, func(p *core.Project) error {
		p.Files["a.txt"] = core.File{Hash: hash, Size: 5}
		p.Epoch = 1
		p.ActiveRun = "r1"
		p.Runs["r1"] = &core.Run{ID: "r1", TaskID: "t1", Status: "running"}
		p.Tasks["t1"] = &core.Task{Status: "running"}
		p.Event("saved", "Alex", "a.txt", nil)
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	s.Close()
	s, e = Open(filename)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Recover(); e != nil {
		t.Fatal(e)
	}
	p, _ = s.Get("alice", p.ID)
	if p.Runs["r1"].Status != "interrupted" || p.Files["a.txt"].Hash != hash || p.ActiveRun != "" {
		t.Fatalf("invalid recovery: %+v", p)
	}
	_, e = s.Mutate("alice", p.ID, nil, func(p *core.Project) error { return p.Fence("r1", 1) })
	if !errors.Is(e, core.ErrStale) {
		t.Fatal("late write accepted")
	}
	if _, e = s.Get("bob", p.ID); !errors.Is(e, core.ErrNotFound) {
		t.Fatal("cross-workspace read")
	}
	if _, e = s.Blob("bob", hash); e == nil {
		t.Fatal("cross-workspace blob read")
	}
}
func TestRejectMissingBlobAndPath(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "a.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	p, _ := s.Create("a", "p")
	_, e = s.Mutate("a", p.ID, nil, func(p *core.Project) error { p.Files["a.txt"] = core.File{Hash: "missing", Size: 2}; return nil })
	if e == nil {
		t.Fatal("dangling file accepted")
	}
	for _, path := range []string{"../secret", "/secret", "x\\y", "a/../b", ".env", "node_modules/a"} {
		if _, e := core.FilePath(path); e == nil {
			t.Fatalf("unsafe path: %s", path)
		}
	}
}
