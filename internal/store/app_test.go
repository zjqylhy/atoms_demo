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
