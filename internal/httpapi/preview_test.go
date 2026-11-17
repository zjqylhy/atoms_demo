package httpapi

import (
	"context"
	"mockatoms/internal/core"
	"mockatoms/internal/store"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewRouteUsesFileDependenciesAndPreservesSavedVersion(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "preview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, err := db.Create("owner", "preview")
	if err != nil {
		t.Fatal(err)
	}
	blobs := map[string][]byte{}
	file := func(body string) core.File {
		data := []byte(body)
		hash := core.Hash(data)
		blobs[hash] = data
		return core.File{Hash: hash, Size: len(data)}
	}
	page, oldOutput, newOutput, newPage := file("page"), file("old"), file("new"), file("new page")
	_, err = db.Mutate("owner", p.ID, blobs, func(p *core.Project) error {
		p.Files = core.Manifest{"index.html": page, "output.bin": oldOutput}
		u := core.CheckEvidence{Kind: "web", Precision: "exact", Dependencies: core.Manifest{"index.html": p.Files["index.html"]}, Namespace: p.Files.Paths()}
		r := core.Report{Digest: p.Files.Digest(), Details: []byte(`{"preview_html":"<p>saved page</p>"}`), Evidence: &core.ReportEvidence{Version: core.CheckEvidenceVersion, Units: []core.CheckEvidence{u}}}
		p.Reports = []core.Report{r}
		p.Files["output.bin"] = newOutput
		p.Versions = []core.Version{{ID: "saved", Manifest: core.Clone(p.Files), Reports: p.Reports}}
		draft := r
		draft.Details = []byte(`{"preview_html":"<p>draft page</p>"}`)
		p.Reports = append(p.Reports, draft)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: db}
	request := func(query string, want int, html string) {
		t.Helper()
		r := httptest.NewRequest("GET", "/preview"+query, nil)
		r.SetPathValue("id", p.ID)
		r = r.WithContext(context.WithValue(r.Context(), ctxKey("owner"), "owner"))
		w := httptest.NewRecorder()
		s.preview(w, r)
		if w.Code != want {
			t.Fatal("wrong preview status", w.Code, w.Body.String())
		}
		if want == 200 && (w.Body.String() != html || w.Header().Get("Content-Security-Policy") == "") {
			t.Fatal("preview content or isolation changed")
		}
	}
	request("", 200, "<p>draft page</p>")
	request("?version=saved", 200, "<p>saved page</p>")
	export := httptest.NewRequest("GET", "/export?format=html&version=saved", nil)
	export.SetPathValue("id", p.ID)
	export = export.WithContext(context.WithValue(export.Context(), ctxKey("owner"), "owner"))
	w := httptest.NewRecorder()
	s.export(w, export)
	if w.Code != 200 || !strings.HasSuffix(w.Body.String(), "<p>saved page</p>") {
		t.Fatal("HTML download did not use the same saved build as the preview", w.Code, w.Body.String())
	}
	_, err = db.Mutate("owner", p.ID, blobs, func(p *core.Project) error { p.Files["index.html"] = newPage; return nil })
	if err != nil {
		t.Fatal(err)
	}
	request("", 404, "")
	request("?version=saved", 200, "<p>saved page</p>")
}
