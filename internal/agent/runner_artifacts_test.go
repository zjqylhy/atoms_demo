package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"math"
	"mockatoms/internal/core"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunnerArtifactCommitIsAtomicAndVersionBound(t *testing.T) {
	for _, scenario := range []string{"valid", "bad_hash", "missing", "extra", "bad_assertion", "bad_assertion_schema", "instruction", "stopped"} {
		t.Run(scenario, func(t *testing.T) {
			e, p := testEngine(t, nil)
			s := scope{Owner: "alice", Project: p.ID, Task: "task", Run: "run", Frame: "frame", Epoch: 1, toolStarted: true}
			input := []byte("print('ok')")
			old := []byte{0, 255, 3}
			data := []byte{0, 255, 128, 12, 9}
			p, err := e.Store.Mutate(s.Owner, p.ID, map[string][]byte{core.Hash(input): input, core.Hash(old): old}, func(p *core.Project) error {
				p.Epoch = 1
				p.ActiveRun = s.Run
				p.CurrentTask = s.Task
				p.Runs[s.Run] = &core.Run{ID: s.Run, Status: "running"}
				p.Files = core.Manifest{"build.py": {Hash: core.Hash(input), Size: len(input), Kind: "code"}, "output.bin": {Hash: core.Hash(old), Size: len(old), Kind: "artifact", Binary: true}}
				p.Tasks[s.Task] = &core.Task{ID: s.Task, Root: s.Frame, Mode: "build", Frames: map[string]*core.Frame{s.Frame: {ID: s.Frame, Role: "Alex"}}, Steps: map[string]core.Step{}}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			before := core.Clone(p.Files)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method == "GET" {
					writeRunnerTestError(w, 404, "job_not_found")
					return
				}
				var job struct {
					ID, Digest  string
					BinaryFiles map[string]string `json:"binary_files"`
				}
				json.NewDecoder(r.Body).Decode(&job)
				if job.BinaryFiles["output.bin"] != base64.StdEncoding.EncodeToString(old) {
					t.Error("binary input changed")
				}
				artifacts := []runnerArtifact{{Path: "output.bin", Format: "binary", Data: base64.StdEncoding.EncodeToString(data), Hash: core.Hash(data), Size: len(data)}}
				switch scenario {
				case "bad_hash":
					artifacts[0].Hash = "bad"
				case "missing":
					artifacts = nil
				case "extra":
					artifacts = append(artifacts, artifacts[0])
				case "instruction", "stopped":
					e.Store.Mutate(s.Owner, p.ID, nil, func(p *core.Project) error {
						if scenario == "instruction" {
							p.Tasks[s.Task].InstructionMessages = append(p.Tasks[s.Task].InstructionMessages, "new")
						} else {
							p.ActiveRun = ""
							p.Epoch++
						}
						return nil
					})
				}
				json.NewEncoder(w).Encode(runnerReply{ID: job.ID, Digest: job.Digest, Status: "passed", Checks: []core.Check{{Name: "script_exit", Status: "passed"}}, Artifacts: artifacts})
			}))
			defer server.Close()
			e.Config.RunnerURL = server.URL
			e.Config.RunnerToken = "test"
			args := map[string]any{"kind": "script", "entry": "build.py", "expected_stdout": "ok", "outputs": []core.OutputDeclaration{{Path: "output.bin", Format: "binary", OldHash: core.Hash(old)}}}
			if scenario == "bad_assertion" {
				args["assertions"] = []core.ArtifactAssertion{{Path: "output.bin", Kind: "text_contains", Expected: "does not exist", Requirement: "content"}}
			}
			if scenario == "bad_assertion_schema" {
				args["assertions"] = []core.ArtifactAssertion{{Path: "output.bin", Kind: "archive_count", Count: 10, Requirement: "count"}}
			}
			result, runErr := e.executeRunner(context.Background(), s, call("run_project", args).ToolCalls[0], args, p)
			if scenario == "bad_assertion_schema" && (requests.Load() != 0 || !strings.Contains(result, "缺少 member")) {
				t.Fatalf("invalid assertion launched an external action or lost feedback: %d %s", requests.Load(), result)
			}
			p, err = e.Store.Get(s.Owner, s.Project)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "valid" {
				if runErr != nil || len(p.Reports) != 1 || p.Files["output.bin"].Hash != core.Hash(data) || len(p.Tasks[s.Task].Checkpoints) != 1 {
					t.Fatal("valid collection failed", runErr, p.Reports)
				}
				report := p.Reports[0]
				if report.Digest != before.Digest() || report.Evidence.Inputs.Digest() != before.Digest() || !reportCurrent(p, p.Tasks[s.Task], report) {
					t.Fatal("input evidence rewritten", report)
				}
				stored, _ := e.Store.Blob(s.Owner, p.Files["output.bin"].Hash)
				if !bytes.Equal(stored, data) {
					t.Fatal("bytes changed")
				}
				p.Files["build.py"] = core.File{Hash: "modified"}
				if reportCurrent(p, p.Tasks[s.Task], report) {
					t.Fatal("changed dependency still accepted")
				}
			} else {
				if p.Files.Digest() != before.Digest() {
					t.Fatal("failed collection changed old files")
				}
				for _, r := range p.Reports {
					if r.Status == "passed" {
						t.Fatal("failed collection recorded as passed")
					}
				}
				if _, err := e.Store.Blob(s.Owner, core.Hash(data)); err == nil && (scenario == "bad_hash" || scenario == "missing" || scenario == "extra" || scenario == "bad_assertion" || scenario == "stopped") {
					t.Fatal("failed collection committed output blob")
				}
			}
		})
	}
}

func TestArtifactJSONAssertionPreservesIntegerPrecision(t *testing.T) {
	assertion := core.ArtifactAssertion{Kind: "json_equals", Expected: `{"id":9007199254740992}`}
	if err := assertArtifact([]byte(`{"id":9007199254740993}`), assertion); err == nil {
		t.Fatal("different large integers accepted as equal")
	}
	if err := assertArtifact([]byte(`{"id":9007199254740992}`), assertion); err != nil {
		t.Fatal(err)
	}
	if err := assertArtifact([]byte(`{"id":9007199254740992} true`), assertion); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}

func TestArchiveAssertionRejectsExpandedSizeOverflow(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	first, err := writer.Create("small.bin")
	if err != nil {
		t.Fatal(err)
	}
	first.Write([]byte{1})
	if _, err := writer.CreateRaw(&zip.FileHeader{Name: "large.bin", Method: zip.Store, UncompressedSize64: math.MaxUint64}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := assertArtifact(buffer.Bytes(), core.ArtifactAssertion{Kind: "archive_count", Member: "*.bin", Count: 2}); err == nil {
		t.Fatal("overflowed archive size bypassed limit")
	}
}

func TestCollectedHistoricalPPTXRequiresOriginalContent(t *testing.T) {
	fixture := os.Getenv("ATOMS_REPAIR_BINARY_FIXTURE")
	if fixture == "" {
		t.Skip("requires the collected historical PPTX regression fixture")
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	// This historical candidate has ten slides but changed the second title.
	// A successful script/ZIP check must not turn that into accepted delivery.
	if core.Hash(data) != "fff1a90b2aa80c056388800c692cedb119d1a79bbf6f51699ef856a88171bde0" {
		t.Fatal("unexpected historical fixture version")
	}
	checks := artifactChecks(map[string][]byte{"result.pptx": data}, []core.ArtifactAssertion{
		{Path: "result.pptx", Kind: "archive_count", Member: "ppt/slides/slide*.xml", Count: 10, Requirement: "10 pages"},
		{Path: "result.pptx", Kind: "archive_xml_contains", Member: "ppt/slides/slide2.xml", Expected: "鬃毛邮报是什么", Requirement: "reuse original title"},
	})
	if checks[0].Status != "passed" || checks[1].Status != "failed" {
		t.Fatal("structural success hid changed content", checks)
	}
	p, task, _ := contextFixture()
	p.Files = core.Manifest{"result.pptx": {Hash: core.Hash(data), Size: len(data), Binary: true}}
	task.Plan.Acceptance = []string{"reuse original title"}
	task.Plan.Delivery = &core.DeliveryScope{Targets: []string{"result.pptx"}, Criteria: []core.DeliveryCriterion{{Requirement: "reuse original title", Paths: []string{"result.pptx"}}}}
	p.Reports = []core.Report{{ID: "actual-output", Kind: "static", Status: "failed", Checks: checks, Evidence: evidenceFor(task, scope{}, p.Files, nil)}}
	if completionCheck(p) == nil {
		t.Fatal("content mismatch accepted as delivery")
	}
}

func TestArtifactAssertionsAndScopedDelivery(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, name := range []string{"items/item1.xml", "items/item2.xml"} {
		f, _ := writer.Create(name)
		f.Write([]byte(`<root><a>标题</a><b>要点</b></root>`))
	}
	writer.Close()
	for _, a := range []core.ArtifactAssertion{{Kind: "archive_count", Member: "items/*.xml", Count: 2}, {Kind: "archive_xml_contains", Member: "items/item1.xml", Expected: "标题要点"}} {
		if err := assertArtifact(buffer.Bytes(), a); err != nil {
			t.Fatal(err)
		}
	}
	if assertArtifact(buffer.Bytes(), core.ArtifactAssertion{Kind: "archive_count", Member: "items/*.xml", Count: 10}) == nil {
		t.Fatal("wrong count accepted")
	}
	p, task, _ := contextFixture()
	p.Files = core.Manifest{"historical.html": {Hash: "old"}, "result.bin": {Hash: "out", Binary: true}, "source.txt": {Hash: "source"}}
	task.Plan.Delivery = &core.DeliveryScope{Targets: []string{"result.bin"}, Inputs: []string{"source.txt"}, Criteria: []core.DeliveryCriterion{{Requirement: "APPROVED_ACCEPTANCE", Paths: []string{"result.bin"}}}}
	if err := validateDelivery(task.Plan.Delivery, task.Plan.Acceptance); err != nil {
		t.Fatal(err)
	}
	manifest, _ := deliveryManifest(p, task)
	r := core.Report{Kind: "static", Status: "passed", Evidence: evidenceFor(task, scope{}, manifest, nil), Checks: []core.Check{{Status: "passed", Requirement: "APPROVED_ACCEPTANCE", Paths: []string{"result.bin"}}}}
	p.Reports = []core.Report{r}
	if err := completionCheck(p); err != nil {
		t.Fatal("unrelated historical web file demanded runtime checks", err)
	}
	p.Files["source.txt"] = core.File{Hash: "changed"}
	if completionCheck(p) == nil {
		t.Fatal("changed input left verification current")
	}
	if validateDelivery(task.Plan.Delivery, []string{"APPROVED_ACCEPTANCE", "missing user condition"}) == nil {
		t.Fatal("scope dropped a required condition")
	}
}
