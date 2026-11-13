package store

import (
	"encoding/json"
	"errors"
	"mockatoms/internal/core"
	"path/filepath"
	"sync"
	"testing"
)

func appFixture(t *testing.T, s *Store, session Session) *core.Project {
	t.Helper()
	p, err := s.Create(session.Workspace, "待办")
	if err != nil {
		t.Fatal(err)
	}
	contract := []byte(`{"api_version":1,"schema_version":1,"initial_state":{"items":[]}}`)
	manifest := core.Manifest{core.AppContractPath: {Hash: core.Hash(contract), Size: len(contract)}}
	p, err = s.Mutate(p.Owner, p.ID, map[string][]byte{core.Hash(contract): contract}, func(p *core.Project) error {
		p.Files = manifest
		p.Reports = []core.Report{{Digest: manifest.Digest(), Details: json.RawMessage(`{"preview_html":"<p>app</p>"}`)}}
		p.Head = "v1"
		p.Versions = []core.Version{{ID: "v1", Manifest: core.Clone(manifest), Reports: p.Reports}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAppDataCommitRestartConflictReceiptAndGrantIsolation(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "app.db")
	s, err := Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	token, session, _ := s.NewGuest()
	p := appFixture(t, s, session)
	access, err := s.PreviewAccess(session.TokenHash, p.ID, "")
	if err != nil || access.Mode != "formal" {
		t.Fatal(access, err)
	}
	in := SaveAppRequest{Grant: access.Grant, Expected: 0, Schema: 1, RequestID: "save-request-1", Value: json.RawMessage(`{"items":["已保存"]}`)}
	revision, err := s.SaveAppState(session.TokenHash, p.ID, in)
	if err != nil || revision != 1 {
		t.Fatal(revision, err)
	}
	s.Close()
	s, err = Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	session, err = s.Session(token, true)
	if err != nil {
		t.Fatal(err)
	}
	state, err := s.LoadAppState(session.TokenHash, p.ID, access.Grant)
	if err != nil || state.Revision != 1 || string(state.Value) != string(in.Value) {
		t.Fatal(state, err)
	}
	if revision, err = s.SaveAppState(session.TokenHash, p.ID, in); err != nil || revision != 1 {
		t.Fatal("lost receipt", revision, err)
	}
	changed := in
	changed.Value = json.RawMessage(`{"items":["different"]}`)
	if _, err = s.SaveAppState(session.TokenHash, p.ID, changed); err == nil {
		t.Fatal("reused request accepted")
	}
	changed.RequestID = "save-request-2"
	if _, err = s.SaveAppState(session.TokenHash, p.ID, changed); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	// Two real concurrent writers: exactly one is allowed to replace revision 1.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"concurrent-1", "concurrent-2"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			req := in
			req.Expected = 1
			req.RequestID = id
			_, e := s.SaveAppState(session.TokenHash, p.ID, req)
			results <- e
		}(id)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	_, stranger, _ := s.NewGuest()
	if _, err = s.LoadAppState(stranger.TokenHash, p.ID, access.Grant); !errors.Is(err, core.ErrNotFound) {
		t.Fatal("cross session", err)
	}
	in.Expected = 2
	in.RequestID = "import-request"
	in.Import = true
	if _, err = s.SaveAppState(session.TokenHash, p.ID, in); err != nil {
		t.Fatal(err)
	}
	backups, err := s.AppBackups(session.TokenHash, p.ID)
	if err != nil || len(backups) != 1 {
		t.Fatal(backups, err)
	}
	_, err = s.Mutate(p.Owner, p.ID, nil, func(p *core.Project) error {
		p.Head = "v2"
		p.Versions = append(p.Versions, core.Version{ID: "v2", Manifest: p.Files, Reports: p.Reports})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SaveAppState(session.TokenHash, p.ID, in); !errors.Is(err, ErrGrant) {
		t.Fatal("old version grant", err)
	}
	old, err := s.PreviewAccess(session.TokenHash, p.ID, "v1")
	if err != nil || old.Mode != "trial" || old.Grant != "" {
		t.Fatal(old, err)
	}
	_, err = s.Mutate(p.Owner, p.ID, nil, func(p *core.Project) error { p.Head = "v1"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.LoadAppState(session.TokenHash, p.ID, access.Grant); !errors.Is(err, ErrGrant) {
		t.Fatal("rollback revived old grant", err)
	}
	fresh, err := s.PreviewAccess(session.TokenHash, p.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeSession(session.TokenHash); err != nil {
		t.Fatal(err)
	}
	if _, err = s.LoadAppState(session.TokenHash, p.ID, fresh.Grant); !errors.Is(err, ErrSession) {
		t.Fatal("logged out grant", err)
	}
}

func TestIncompatibleCodePreservesDataAndResetRequiresBackup(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "schema.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, session, _ := s.NewGuest()
	p := appFixture(t, s, session)
	a, err := s.PreviewAccess(session.TokenHash, p.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	in := SaveAppRequest{Grant: a.Grant, Schema: 1, Expected: 0, RequestID: "schema-save-1", Value: []byte(`{"items":["retain me"]}`)}
	if _, err = s.SaveAppState(session.TokenHash, p.ID, in); err != nil {
		t.Fatal(err)
	}
	// Simulate a real database write failure; no receipt or revision is committed.
	if _, err = s.db.Exec(`CREATE TRIGGER reject_app_save BEFORE UPDATE ON app_states BEGIN SELECT RAISE(ABORT,'test write failure'); END`); err != nil {
		t.Fatal(err)
	}
	in.Expected = 1
	in.RequestID = "failed-database-save"
	if _, err = s.SaveAppState(session.TokenHash, p.ID, in); err == nil {
		t.Fatal("failed DB write acknowledged")
	}
	state, err := s.LoadAppState(session.TokenHash, p.ID, a.Grant)
	if err != nil || state.Revision != 1 {
		t.Fatal(state, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER reject_app_save`); err != nil {
		t.Fatal(err)
	}
	contract := []byte(`{"api_version":1,"schema_version":2,"initial_state":{"records":[]}}`)
	p, err = s.Mutate(p.Owner, p.ID, map[string][]byte{core.Hash(contract): contract}, func(p *core.Project) error {
		p.Files[core.AppContractPath] = core.File{Hash: core.Hash(contract), Size: len(contract)}
		p.Head = "v2"
		report := core.Report{Digest: p.Files.Digest(), Details: []byte(`{"preview_html":"<p>new</p>"}`)}
		p.Reports = append(p.Reports, report)
		p.Versions = append(p.Versions, core.Version{ID: "v2", Manifest: core.Clone(p.Files), Reports: []core.Report{report}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := s.PreviewAccess(session.TokenHash, p.ID, "")
	if err != nil || blocked.Mode != "incompatible" || blocked.Grant != "" || string(blocked.State.Value) != string(in.Value) {
		t.Fatal(blocked, err)
	}
	reset := ResetAppRequest{Version: "v2", Expected: 1, RequestID: "reset-schema-123"}
	if revision, err := s.ResetAppState(session.TokenHash, p.ID, reset); err != nil || revision != 2 {
		t.Fatal(revision, err)
	}
	if revision, err := s.ResetAppState(session.TokenHash, p.ID, reset); err != nil || revision != 2 {
		t.Fatal("reset retry", revision, err)
	}
	backups, err := s.AppBackups(session.TokenHash, p.ID)
	if err != nil || len(backups) != 1 {
		t.Fatal(backups, err)
	}
	current, err := s.PreviewAccess(session.TokenHash, p.ID, "")
	if err != nil || current.Mode != "formal" || current.State.Schema != 2 {
		t.Fatal(current, err)
	}
}
