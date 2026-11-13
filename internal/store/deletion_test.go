package store

import (
	"encoding/json"
	"errors"
	"mockatoms/internal/core"
	"path/filepath"
	"strings"
	"testing"
)

func deletionStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "delete.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func TestDeleteSharedProjectTransactionAndReceipts(t *testing.T) {
	s := deletionStore(t)
	_, a := testAccount(t, s)
	_, b := testAccount(t, s)
	p, err := s.CopyPreset(a.TokenHash, "board", "preset-to-delete")
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.CopyPreset(a.TokenHash, "board", "preset-to-keep")
	if err != nil {
		t.Fatal(err)
	}
	access, err := s.PreviewAccess(a.TokenHash, p.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveAppState(a.TokenHash, p.ID, SaveAppRequest{Grant: access.Grant, Schema: 1, Expected: 0, RequestID: "import-before-delete", Import: true, Value: json.RawMessage(`{"items":[]}`)}); err != nil {
		t.Fatal(err)
	}
	_, token, err := s.CreateShare(a.TokenHash, p.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	copy, err := s.CopyShare(b.TokenHash, token, "share-to-delete")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ReserveCall(a.Workspace, 0, 1); err != nil {
		t.Fatal(err)
	}
	// Fail late in cleanup and assert that earlier deletes roll back too.
	if _, err = s.db.Exec(`CREATE TRIGGER reject_cleanup BEFORE DELETE ON app_states BEGIN SELECT RAISE(ABORT,'test failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteProject(b.TokenHash, p.ID); err == nil {
		t.Fatal("expected injected failure")
	}
	for _, table := range []string{"app_receipts", "app_backups", "preview_grants", "share_links", "app_states"} {
		var count int
		if err = s.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE project_id=?`, p.ID).Scan(&count); err != nil || count == 0 {
			t.Fatal("partial delete", table, count, err)
		}
	}
	s.db.Exec(`DROP TRIGGER reject_cleanup`)
	if err = s.DeleteProject(b.TokenHash, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(p.Owner, p.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal(err)
	}
	for _, table := range []string{"app_receipts", "app_backups", "preview_grants", "share_links", "app_states"} {
		var count int
		s.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE project_id=?`, p.ID).Scan(&count)
		if count != 0 {
			t.Fatal(table, count)
		}
	}
	if _, err = s.Shared(token); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("share still works", err)
	}
	if _, err = s.CopyPreset(a.TokenHash, "board", "preset-to-delete"); !errors.Is(err, ErrCopyDeleted) {
		t.Fatal("copy resurrected", err)
	}
	if _, err = s.LoadAppState(a.TokenHash, p.ID, access.Grant); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("grant survived", err)
	}
	if err = s.DeleteProject(a.TokenHash, copy.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CopyShare(b.TokenHash, token, "share-to-delete"); !errors.Is(err, ErrCopyDeleted) {
		t.Fatal("share receipt lost", err)
	}
	for _, file := range q.Files {
		if _, err = s.Blob(q.Owner, file.Hash); err != nil {
			t.Fatal("shared bytes deleted", err)
		}
	}
	if err = s.ReserveCall(a.Workspace, 0, 1); err == nil {
		t.Fatal("quota reset by deletion")
	}
	if _, err = s.AccountSession(a.TokenHash); err != nil {
		t.Fatal("account lost", err)
	}
	if err = s.DeleteProject(b.TokenHash, p.ID); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("repeat delete", err)
	}
}

func TestDeleteRetainsHistoryAndUnknownReferences(t *testing.T) {
	s := deletionStore(t)
	_, a := testAccount(t, s)
	p, _ := s.Create(a.Workspace, "delete")
	q, _ := s.Create(a.Workspace, "history")
	blobs := map[string][]byte{}
	manifest := core.Manifest{}
	for _, name := range []string{"unreferenced", "checkpoint", "future"} {
		data := []byte(name)
		hash := core.Hash(data)
		blobs[hash] = data
		manifest[name] = core.File{Hash: hash, Size: len(data)}
	}
	if _, err := s.Mutate(p.Owner, p.ID, blobs, func(p *core.Project) error { p.Files = manifest; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Mutate(q.Owner, q.ID, nil, func(p *core.Project) error {
		p.Tasks["old"] = &core.Task{Base: core.Manifest{}, Checkpoints: []core.Checkpoint{{Manifest: core.Manifest{"old": manifest["checkpoint"]}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	s.db.QueryRow(`SELECT state FROM projects WHERE id=?`, q.ID).Scan(&raw)
	var obj map[string]json.RawMessage
	json.Unmarshal(raw, &obj)
	hash := manifest["future"].Hash
	// Escaped hex characters are a valid legacy string representation.
	obj["future_metadata"] = json.RawMessage(`{"proof":"\u00` + strings.ToUpper(coreHashPrefix(hash[0])) + hash[1:] + `"}`)
	raw, _ = json.Marshal(obj)
	if _, err := s.db.Exec(`UPDATE projects SET state=? WHERE id=?`, raw, q.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(a.TokenHash, p.ID); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"checkpoint", "future"} {
		if _, err := s.Blob(a.Workspace, manifest[name].Hash); err != nil {
			t.Fatal(name, err)
		}
	}
	if _, err := s.Blob(a.Workspace, manifest["unreferenced"].Hash); err == nil {
		t.Fatal("unreferenced bytes retained")
	}
}
func coreHashPrefix(b byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[b>>4], digits[b&15]})
}

func TestDeleteRejectsGuestActiveAndCorruptReferences(t *testing.T) {
	s := deletionStore(t)
	_, a := testAccount(t, s)
	_, guest, _ := s.NewGuest()
	p, _ := s.Create(a.Workspace, "target")
	if err := s.DeleteProject(guest.TokenHash, p.ID); !errors.Is(err, ErrSession) {
		t.Fatal(err)
	}
	s.Mutate(p.Owner, p.ID, nil, func(p *core.Project) error { p.ActiveRun = "active"; return nil })
	if err := s.DeleteProject(a.TokenHash, p.ID); !errors.Is(err, ErrProjectRunning) {
		t.Fatal(err)
	}
	s.Mutate(p.Owner, p.ID, nil, func(p *core.Project) error { p.ActiveRun = ""; return nil })
	q, _ := s.Create("other", "bad state")
	s.db.Exec(`UPDATE projects SET state='null' WHERE id=?`, q.ID)
	if err := s.DeleteProject(a.TokenHash, p.ID); err == nil {
		t.Fatal("unsafe garbage collection")
	}
	if _, err := s.Get(p.Owner, p.ID); err != nil {
		t.Fatal("delete not atomic", err)
	}
}

func TestDeletingFallbackDoesNotRecreateIt(t *testing.T) {
	s := deletionStore(t)
	_, a := testAccount(t, s)
	p, _ := s.Create(a.Workspace, "original")
	prepare := func(run string) {
		_, err := s.Mutate(p.Owner, p.ID, nil, func(p *core.Project) error {
			p.CurrentTask = "task"
			p.Tasks["task"] = &core.Task{ID: "task", Status: "failed", Mode: "build"}
			p.Runs[run] = &core.Run{ID: run, TaskID: "task", Status: "failed"}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	prepare("run1")
	p, err := s.InstallFallback(p.Owner, p.ID, "run1", "board", "no model")
	if err != nil {
		t.Fatal(err)
	}
	demo := p.Recovery.ProjectID
	if err = s.DeleteProject(a.TokenHash, demo); err != nil {
		t.Fatal(err)
	}
	prepare("run2")
	p, err = s.InstallFallback(p.Owner, p.ID, "run2", "board", "still no model")
	if err != nil || !p.Recovery.Unavailable || p.Recovery.ProjectID != demo {
		t.Fatal(p, err)
	}
	list, _ := s.SharedProjects(a.TokenHash)
	if len(list) != 1 {
		t.Fatal("demo recreated", list)
	}
}
