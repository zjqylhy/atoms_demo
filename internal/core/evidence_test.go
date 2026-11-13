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
