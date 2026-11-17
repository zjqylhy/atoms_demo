package core

import "testing"

func TestPreviewSurvivesIndependentOutputChangeButNotPageChange(t *testing.T) {
	p := NewProject("test", "preview")
	p.Files = Manifest{"index.html": {Hash: "html"}, "app.js": {Hash: "js"}, "slides.pptx": {Hash: "original"}}
	u := CheckEvidence{Kind: "web", Paths: []string{"index.html"}, Precision: "exact", Dependencies: Manifest{"index.html": p.Files["index.html"], "app.js": p.Files["app.js"]}, Namespace: p.Files.Paths()}
	r := Report{Kind: "run", Digest: p.Files.Digest(), Details: []byte(`{"preview_html":"<button>next</button>"}`), Evidence: &ReportEvidence{Version: CheckEvidenceVersion, Units: []CheckEvidence{u}}}
	p.Reports = []Report{r}
	p.Files["slides.pptx"] = File{Hash: "changed"}
	p.Versions = []Version{{ID: "saved", Manifest: Clone(p.Files), Reports: p.Reports}}
	if PreviewHTML(p.Reports, p.Files) == "" || p.Public().(map[string]any)["preview_ready"] != true {
		t.Fatal("unrelated output hid page")
	}
	if !p.Public().(map[string]any)["versions"].([]Version)[0].PreviewReady {
		t.Fatal("saved version lost preview")
	}
	p.Files["app.js"] = File{Hash: "changed script"}
	if PreviewHTML(p.Reports, p.Files) != "" || p.Public().(map[string]any)["preview_ready"] != false {
		t.Fatal("stale page remained visible")
	}
	if !p.Public().(map[string]any)["versions"].([]Version)[0].PreviewReady {
		t.Fatal("editing draft broke saved version")
	}
}

func TestLegacyPreviewStillRequiresFullDigest(t *testing.T) {
	files := Manifest{"index.html": {Hash: "html"}, "output.bin": {Hash: "old"}}
	r := Report{Digest: files.Digest(), Details: []byte(`{"preview_html":"legacy"}`)}
	if PreviewHTML([]Report{r}, files) != "legacy" {
		t.Fatal("legacy preview lost")
	}
	files["output.bin"] = File{Hash: "new"}
	if PreviewHTML([]Report{r}, files) != "" {
		t.Fatal("legacy dependencies guessed")
	}
}

func TestPublicPreviewDescribesTheBuildForEachVersion(t *testing.T) {
	p := NewProject("test", "preview")
	p.Files = Manifest{"index.html": {Hash: "home"}, "pages/dashboard.html": {Hash: "dashboard"}}
	build := func(id, entry, html string) Report {
		return Report{
			ID: id, Details: []byte(JSON(map[string]string{"preview_html": html})),
			Evidence: &ReportEvidence{Version: CheckEvidenceVersion, Units: []CheckEvidence{{
				Kind: "web", Paths: []string{entry}, Precision: "exact", Dependencies: Manifest{entry: p.Files[entry]},
			}}},
		}
	}
	saved := build("saved-build", "index.html", "<p>saved build</p>")
	draft := build("draft-build", "pages/dashboard.html", "<p>draft build</p>")
	unrelatedCheck := Report{Digest: p.Files.Digest(), Details: []byte(`{"check":"source only"}`)}
	p.Reports = []Report{saved, draft, unrelatedCheck}
	p.Versions = []Version{{ID: "saved", Manifest: Clone(p.Files), Reports: []Report{saved}}}

	public := p.Public().(map[string]any)
	if public["preview_ready"] != true || public["preview_entry"] != "pages/dashboard.html" || public["preview_report_id"] != "draft-build" {
		t.Fatal("draft preview metadata must describe the latest matching build")
	}
	version := public["versions"].([]Version)[0]
	if !version.PreviewReady || version.PreviewEntry != "index.html" || version.PreviewReportID != "saved-build" {
		t.Fatal("saved version inherited the draft preview")
	}
	if string(version.Reports[0].Details) != "{}" || p.Versions[0].PreviewReady {
		t.Fatal("public preview metadata leaked the page or altered the saved version")
	}
}

func TestLegacyPreviewKeepsSavedPageWhenEntryIsAmbiguous(t *testing.T) {
	p := NewProject("test", "legacy")
	p.Files = Manifest{"index.html": {Hash: "home"}, "pages/dashboard.html": {Hash: "dashboard"}}
	details := []byte(`{"preview_html":"<p>legacy page</p>"}`)
	for _, tc := range []struct {
		name, kind, entry string
	}{
		{name: "ambiguous generated page", kind: "run"},
		{name: "known packaged entry", kind: "packaged_browser", entry: "index.html"},
		{name: "known shared entry", kind: "shared_preview", entry: "index.html"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p.Reports = []Report{{ID: "legacy-build", Kind: tc.kind, Digest: p.Files.Digest(), Details: details}}
			public := p.Public().(map[string]any)
			if public["preview_ready"] != true || public["preview_entry"] != tc.entry || public["preview_report_id"] != "legacy-build" {
				t.Fatal("incorrect legacy preview metadata")
			}
			if PreviewHTML(p.Reports, p.Files) != "<p>legacy page</p>" {
				t.Fatal("the stored legacy page must remain usable")
			}
		})
	}
	// A source filename by itself cannot make a previously built page current.
	delete(p.Files, "index.html")
	if p.Public().(map[string]any)["preview_ready"] != false {
		t.Fatal("editing the manifest left a stale legacy preview active")
	}
	p.Reports[0].Digest = p.Files.Digest()
	if p.Public().(map[string]any)["preview_entry"] != "pages/dashboard.html" {
		t.Fatal("a unique HTML source should identify a matching legacy preview")
	}
}
