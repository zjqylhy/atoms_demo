package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
)

func repairFixture() (*core.Project, *core.Task, map[string]string) {
	p, t, _ := contextFixture()
	files := map[string]string{"index.html": `<link rel="stylesheet" href="style.css"><button id="next">下一页</button><script src="app.js"></script>`, "app.js": "document.querySelector('#next').onclick=()=>{}", "style.css": "body{color:black}", "slides.pptx": "original slide", "prd.md": "requirements"}
	p.Files = core.Manifest{}
	for name, body := range files {
		p.Files[name] = core.File{Hash: core.Hash([]byte(body)), Size: len(body)}
	}
	p.Reports = nil
	t.Plan = &core.Plan{Revision: "p1", Approved: true, Acceptance: []string{"网页可翻页", "幻灯片保留正文"}, Delivery: &core.DeliveryScope{Targets: []string{"index.html", "slides.pptx", "prd.md"}, Criteria: []core.DeliveryCriterion{{Requirement: "网页可翻页", Paths: []string{"index.html"}}, {Requirement: "幻灯片保留正文", Paths: []string{"slides.pptx"}}, {Requirement: "需求", Paths: []string{"prd.md"}}}}}
	return p, t, files
}

func repairStatic(p *core.Project, t *core.Task, files map[string]string) core.Report {
	r := checkArtifacts(files, p.Files)
	r.Checks = append(r.Checks, artifactChecks(map[string][]byte{"slides.pptx": []byte(files["slides.pptx"]), "prd.md": []byte(files["prd.md"])}, []core.ArtifactAssertion{{Path: "slides.pptx", Kind: "text_contains", Expected: "original", Requirement: "幻灯片保留正文"}, {Path: "prd.md", Kind: "nonempty", Requirement: "需求"}})...)
	r.Evidence = staticEvidence(t, scope{}, files, p.Files, r)
	return r
}

func repairRuntime(p *core.Project, t *core.Task, kind, entry string, checks []core.Check) core.Report {
	args := map[string]any{"entry": entry, "kind": "web"}
	detail := map[string]any{"entry": entry, "execution": kind, "dependencies": runnerDependencies{Version: 1, Precision: "exact", Paths: []string{entry, "app.js", "style.css"}, Namespace: fileNames(p.Files)}}
	r := core.Report{ID: core.ID(), Kind: "run", Status: "passed", Checks: checks, Details: []byte(core.JSON(detail)), Evidence: evidenceFor(t, scope{}, p.Files, nil)}
	if kind == "browser" {
		r.Kind = "browser"
	}
	runnerEvidence(t, r.Evidence, r, args)
	return r
}

func issueKinds(a *AcceptanceAssessment) []string {
	var kinds []string
	for _, i := range a.Issues {
		kinds = append(kinds, i.Kind)
	}
	return sortedUnique(kinds)
}

func TestAcceptanceReportsAllMissingAndDistinguishesWebFromBrowser(t *testing.T) {
	p, task, files := repairFixture()
	a := assessAcceptance(p)
	for _, kind := range []string{"static", "criterion", "web", "browser"} {
		if !slices.Contains(issueKinds(a), kind) {
			t.Fatal("missing independent blocker", kind, a)
		}
	}
	p.Reports = append(p.Reports, repairStatic(p, task, files), repairRuntime(p, task, "browser", "index.html", []core.Check{{Status: "passed", Requirement: "网页可翻页", Paths: []string{"index.html"}}}))
	a = assessAcceptance(p)
	if len(a.Issues) != 1 || a.Issues[0].Kind != "web" || !strings.Contains(a.Issues[0].Next, "kind=web") {
		t.Fatal("browser success hid missing web execution", a)
	}
	e := &Engine{}
	feedback := e.failureFeedback(a)
	if feedback["acceptance"] == nil || feedback["ok"] != false {
		t.Fatal("missing structured feedback", feedback)
	}
	p.Reports = append(p.Reports, repairRuntime(p, task, "web", "index.html", nil))
	if err := completionCheck(p); err != nil {
		t.Fatal(err)
	}
}

func TestEvidenceReusesUnchangedFilesAndConditions(t *testing.T) {
	p, task, files := repairFixture()
	p.Reports = []core.Report{repairStatic(p, task, files), repairRuntime(p, task, "web", "index.html", nil), repairRuntime(p, task, "browser", "index.html", []core.Check{{Status: "passed", Requirement: "网页可翻页", Paths: []string{"index.html"}}})}
	task.Plan.Revision = "p2"
	task.Plan.Text = "new implementation explanation"
	if err := completionCheck(p); err != nil {
		t.Fatal("explanation invalidated evidence", err)
	}
	p.Files["slides.pptx"] = core.File{Hash: "new slide"}
	a := assessAcceptance(p)
	for _, issue := range a.Issues {
		if !slices.Contains(issue.Paths, "slides.pptx") {
			t.Fatal("unrelated file invalidated web or PRD", issue)
		}
	}
	if len(a.Issues) != 2 {
		t.Fatal("changed output not invalidated", a)
	}
	p.Files["app.js"] = core.File{Hash: "broken script"}
	a = assessAcceptance(p)
	if !slices.Contains(issueKinds(a), "web") || !slices.Contains(issueKinds(a), "browser") {
		t.Fatal("actual dependencies not invalidated", a)
	}
	for _, issue := range a.Issues {
		if issue.Kind == "web" && !slices.Contains(issue.ChangedPaths, "app.js") {
			t.Fatal("feedback omitted changed dependency", issue)
		}
	}
	// Restore files, then change only the formal contract.
	for name, body := range files {
		p.Files[name] = core.File{Hash: core.Hash([]byte(body)), Size: len(body)}
	}
	task.Plan.Delivery.Criteria[0].Requirement = "网页支持键盘翻页"
	a = assessAcceptance(p)
	if len(a.Issues) != 1 || a.Issues[0].Kind != "criterion" {
		t.Fatal("generic facts should survive new requirements", a)
	}
	task.Plan.Delivery.Criteria[0].Requirement = "网页可翻页"
	task.Plan.Delivery.Inputs = []string{"prd.md"}
	a = assessAcceptance(p)
	if !slices.Equal(issueKinds(a), []string{"criterion"}) {
		t.Fatal("required input changes must invalidate criterion proof only", a)
	}
}

func TestIndependentStaticGroupsCannotCherryPickAssertions(t *testing.T) {
	p, task, files := repairFixture()
	r := repairStatic(p, task, files)
	r.Status = "failed"
	r.Checks = append(r.Checks, core.Check{Name: "conflicting", Status: "failed", Requirement: "幻灯片保留正文", Paths: []string{"slides.pptx"}})
	r.Evidence = staticEvidence(task, scope{}, files, p.Files, r)
	p.Reports = []core.Report{r}
	a := assessAcceptance(p)
	for _, i := range a.Issues {
		if i.Kind == "static" && slices.Contains(i.Paths, "prd.md") {
			t.Fatal("independent PRD check lost", i)
		}
	}
	for _, e := range a.Evidence {
		if e.Requirement == "幻灯片保留正文" {
			t.Fatal("failed sibling assertion ignored", e)
		}
	}
	// A newer failed group must supersede its older successful version.
	p.Reports = []core.Report{repairStatic(p, task, files), r}
	for _, e := range assessAcceptance(p).Evidence {
		if e.Requirement == "幻灯片保留正文" {
			t.Fatal("old success masked new failure")
		}
	}
}

func TestRuntimeEvidenceBindsEntriesAndConservativelyTracksSnapshots(t *testing.T) {
	p, task, _ := repairFixture()
	p.Files["second.html"] = core.File{Hash: "second"}
	task.Plan.Delivery.Targets = append(task.Plan.Delivery.Targets, "second.html")
	p.Reports = []core.Report{repairRuntime(p, task, "web", "index.html", nil)}
	missingSecond := false
	for _, i := range assessAcceptance(p).Issues {
		if i.Kind == "web" && slices.Contains(i.Paths, "second.html") {
			missingSecond = true
		}
	}
	if !missingSecond {
		t.Fatal("one entry covered another")
	}
	r := p.Reports[0]
	p.Files["new.ts"] = core.File{Hash: "new resolution candidate"}
	if reportCurrent(p, task, r) {
		t.Fatal("new file can change module resolution")
	}
	delete(p.Files, "new.ts")
	for _, details := range []map[string]any{{"execution": "python"}, {"execution": "web", "dependencies": runnerDependencies{Version: 1, Precision: "exact", Paths: []string{"../escape"}, Namespace: fileNames(p.Files)}}} {
		r = core.Report{Kind: "run", Status: "passed", Details: []byte(core.JSON(details)), Evidence: evidenceFor(task, scope{}, p.Files, core.Manifest{"result.bin": {Hash: "new output"}})}
		runnerEvidence(task, r.Evidence, r, map[string]any{"entry": "index.html"})
		if r.Evidence.Units[0].Precision != "snapshot" {
			t.Fatal("unproven dependency list trusted")
		}
		p.Files["result.bin"] = core.File{Hash: "new output"}
		if !reportCurrent(p, task, r) {
			t.Fatal("collected output immediately invalidated snapshot")
		}
		p.Files["extra.txt"] = core.File{Hash: "addition"}
		if reportCurrent(p, task, r) {
			t.Fatal("snapshot ignored path addition")
		}
		delete(p.Files, "extra.txt")
		delete(p.Files, "result.bin")
	}
}

func TestStaticMissingReferenceAndInstructionRemainFenced(t *testing.T) {
	p, task, _ := repairFixture()
	files := map[string]string{"index.html": `<img src="missing.svg">`}
	manifest := core.Manifest{"index.html": {Hash: core.Hash([]byte(files["index.html"]))}}
	p.Files = manifest
	r := checkArtifacts(files, manifest)
	r.Evidence = staticEvidence(task, scope{}, files, manifest, r)
	if !reportCurrent(p, task, r) {
		t.Fatal("fresh failed evidence should explain failure")
	}
	p.Files["missing.svg"] = core.File{Hash: "added"}
	if reportCurrent(p, task, r) {
		t.Fatal("added missing dependency left failure current")
	}
	delete(p.Files, "missing.svg")
	task.InstructionMessages = append(task.InstructionMessages, "new requirement")
	if reportCurrent(p, task, r) {
		t.Fatal("instruction boundary bypassed")
	}
}

func TestAcceptanceRetryCountsSurvivePartialProgressAndNewReportIDs(t *testing.T) {
	p, task, files := repairFixture()
	c := call("finish_task", map[string]any{"summary": "first"}).ToolCalls[0]
	s := scope{Task: task.ID}
	observe := func() core.ActionFact {
		f := actionFact(p, s, c)
		f.ErrorCode = "acceptance_incomplete"
		f.ErrorClass = "validation"
		f.Fingerprint = failureFingerprint(c.Function.Name, c.Function.Arguments, f)
		observeFact(task, core.Step{ID: core.ID(), Tool: "finish_task", Fact: &f}, 3)
		return f
	}
	first := observe()
	c.Function.Arguments = `{"summary":"different words","answer_only":false}`
	second := observe()
	if first.Fingerprint != second.Fingerprint {
		t.Fatal("summary reset retry")
	}
	p.Reports = append(p.Reports, repairStatic(p, task, files))
	last := observe()
	for _, key := range last.RetryKeys {
		if observation(task).Failures[key] != 3 {
			t.Fatal("partial repair reset outstanding issue", observation(task))
		}
	}
	copy := core.Clone(*task)
	if len(observation(&copy).Failures) != len(observation(task).Failures) {
		t.Fatal("persistence lost counts")
	}
	// Same failing result, with a new report ID, is still the same blocker.
	r := repairRuntime(p, task, "web", "index.html", nil)
	r.Status = "failed"
	r.Evidence.Units[0].Status = "failed"
	p.Reports = append(p.Reports, r)
	f1 := observe()
	r.ID = core.ID()
	p.Reports = append(p.Reports, r)
	f2 := observe()
	if !slices.Equal(f1.RetryKeys, f2.RetryKeys) {
		t.Fatal("report identity changed retry basis")
	}
}

func TestCompletionLoopStopsAcrossContinueWithChangingSummary(t *testing.T) {
	n := 0
	g := fakeModel(func(_ context.Context, _ []core.ModelMessage, _ []model.Tool) (core.ModelMessage, core.Usage, error) {
		n++
		return call("finish_task", map[string]any{"summary": fmt.Sprintf("new summary %d", n), "answer_only": false}), core.Usage{}, nil
	})
	e, p := testEngine(t, g)
	data := []byte("saved artifact")
	hash := core.Hash(data)
	_, err := e.Store.Mutate("alice", p.ID, map[string][]byte{hash: data}, func(p *core.Project) error {
		p.Files["result.txt"] = core.File{Hash: hash, Size: len(data)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"submit", "continue"} {
		if _, err := e.Start("alice", p.ID, "deliver artifact", "build", action, action, ""); err != nil {
			t.Fatal(err)
		}
		p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
		task := p.Tasks[p.CurrentTask]
		attempts := 0
		for _, step := range task.Steps {
			if step.Tool == "finish_task" {
				attempts++
			}
		}
		if attempts != 3 || observation(task).Replans < 1 || (action == "continue" && observation(task).Replans < 2) {
			t.Fatal("unbounded completion loop", attempts, observation(task))
		}
		if p.Files["result.txt"].Hash != hash {
			t.Fatal("saved output lost")
		}
	}
	// Real static validation supplies the missing fact; old failures no longer block.
	_, err = e.Store.Mutate("alice", p.ID, nil, func(p *core.Project) error {
		task := p.Tasks[p.CurrentTask]
		files := map[string]string{"result.txt": string(data)}
		r := checkArtifacts(files, p.Files)
		r.Evidence = staticEvidence(task, scope{}, files, p.Files, r)
		p.Reports = append(p.Reports, r)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Start("alice", p.ID, "", "build", "repaired", "continue", ""); err != nil {
		t.Fatal(err)
	}
	p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
	if p.Tasks[p.CurrentTask].Status != "completed" {
		t.Fatal("real repair did not reopen delivery")
	}
}

func TestAssessmentErrorSupportsStructuredExtraction(t *testing.T) {
	p, _, _ := repairFixture()
	var assessment *AcceptanceAssessment
	if !errors.As(completionCheck(p), &assessment) || assessment.Ready {
		t.Fatal("missing typed diagnosis")
	}
}

func TestLegacyRuntimeDoesNotIgnoreNewResolutionCandidates(t *testing.T) {
	p, task, _ := repairFixture()
	r := core.Report{Kind: "run", Status: "passed", Evidence: evidenceFor(task, scope{}, p.Files, nil)}
	if !reportCurrent(p, task, r) {
		t.Fatal("unchanged legacy snapshot rejected")
	}
	p.Files["new-module.ts"] = core.File{Hash: "new module"}
	if reportCurrent(p, task, r) {
		t.Fatal("legacy runtime accepted changed path namespace")
	}
	if r.Evidence.Version != 0 || len(r.Evidence.Units) != 0 {
		t.Fatal("legacy evidence was migrated")
	}
}

// Production snapshots are opt-in, read-only fixtures, never checked in or
// rewritten as successful evidence. Replay each rejection at its original state.
func TestHistoricalCompletionRejectionsHaveActionableDiagnosis(t *testing.T) {
	fixture := os.Getenv("ATOMS_ACCEPTANCE_SNAPSHOT")
	if fixture == "" {
		t.Skip("requires the read-only acceptance-loop snapshot")
	}
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Project core.Project `json:"project"`
	}
	if err = json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	original := snapshot.Project
	task := original.Tasks[original.CurrentTask]
	var steps []core.Step
	checkpoints := map[string]core.Manifest{}
	for _, step := range task.Steps {
		steps = append(steps, step)
	}
	for _, cp := range task.Checkpoints {
		checkpoints[cp.StepID] = cp.Manifest
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].At < steps[j].At })
	var plan *core.Plan
	attempts, webBlocked := 0, 0
	for _, step := range steps {
		if step.Tool == "propose_plan" {
			var result struct {
				OK     bool      `json:"ok"`
				Result core.Plan `json:"result"`
			}
			if err = json.Unmarshal([]byte(step.Result), &result); err != nil {
				t.Fatal(err)
			}
			if result.OK {
				plan = &result.Result
			}
		}
		if step.Tool != "finish_task" {
			continue
		}
		p := core.Clone(original)
		p.Files = core.Clone(checkpoints[step.ID])
		p.Tasks[p.CurrentTask].Plan = core.Clone(plan)
		p.Reports = nil
		for _, r := range original.Reports {
			if r.At <= step.At {
				p.Reports = append(p.Reports, r)
			}
		}
		a := assessAcceptance(&p)
		if a.Ready {
			t.Fatal("historical rejection incorrectly accepted", step.At, a)
		}
		if slices.Contains(issueKinds(a), "web") {
			webBlocked++
		}
		for _, issue := range a.Issues {
			if issue.Next == "" || issue.Reason == "" || issue.Basis == "" {
				t.Fatal("incomplete diagnosis", issue)
			}
		}
		attempts++
	}
	if attempts != 17 {
		t.Fatal("unexpected historical attempts", attempts)
	}
	if webBlocked == 0 {
		t.Fatal("web execution blocker not diagnosed")
	}
	t.Logf("replayed %d finish rejections; %d include missing/current-task web execution; all returned concrete blockers", attempts, webBlocked)
}
