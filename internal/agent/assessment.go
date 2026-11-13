package agent

import (
	"encoding/json"
	"fmt"
	"mockatoms/internal/core"
	"path"
	"slices"
	"sort"
	"strings"
)

// AcceptanceAssessment is a pure diagnosis used by delivery, context and retry
// admission. It never executes a check or accepts a model's success statement.
type AcceptanceAssessment struct {
	Code     string                `json:"code"`
	Ready    bool                  `json:"ready"`
	Issues   []AcceptanceIssue     `json:"issues"`
	Evidence []AcceptanceReference `json:"evidence"`
	Reports  []acceptanceReport    `json:"reports"`
}

type acceptanceReport struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
}

type AcceptanceReference struct {
	Kind        string   `json:"kind"`
	Paths       []string `json:"paths"`
	Requirement string   `json:"requirement,omitempty"`
	ReportID    string   `json:"report_id"`
}

type AcceptanceIssue struct {
	Code         string   `json:"code"`
	Kind         string   `json:"kind"`
	Paths        []string `json:"paths,omitempty"`
	Requirement  string   `json:"requirement,omitempty"`
	Reason       string   `json:"reason"`
	Observed     string   `json:"observed"`
	Next         string   `json:"next"`
	ReportID     string   `json:"report_id,omitempty"`
	ChangedPaths []string `json:"changed_paths,omitempty"`
	Basis        string   `json:"retry_basis"`
}

func (a *AcceptanceAssessment) Error() string {
	var lines []string
	for _, issue := range a.Issues {
		target := strings.Join(issue.Paths, ", ")
		if issue.Requirement != "" {
			target += "（条件：" + issue.Requirement + "）"
		}
		lines = append(lines, fmt.Sprintf("%s [%s] %s：%s；%s", issue.Kind, issue.Code, target, issue.Observed, issue.Next))
	}
	return "产物验收未完成：\n" + strings.Join(lines, "\n")
}

type evidenceCandidate struct {
	unit     core.CheckEvidence
	report   string
	reason   string
	failures string
}

func failedCheckDetails(r core.Report, u core.CheckEvidence) string {
	var relevant, all []string
	for _, check := range r.Checks {
		if check.Status != "failed" {
			continue
		}
		detail := check.Name + ": " + excerpt(check.Detail, 300)
		all = append(all, detail)
		if u.Kind == "criterion" && normalizeRequirement(check.Requirement) == u.Requirement {
			relevant = append(relevant, detail)
		}
		if u.Kind == "static" && (slices.Contains(u.Paths, check.Name) || overlaps(u.Paths, check.Paths)) {
			relevant = append(relevant, detail)
		}
	}
	if len(relevant) == 0 {
		relevant = all
	}
	if len(relevant) > 3 {
		relevant = relevant[:3]
	}
	return strings.Join(relevant, "；")
}

func overlaps(a, b []string) bool {
	for _, name := range a {
		if slices.Contains(b, name) {
			return true
		}
	}
	return false
}

func changedDependencyPaths(u core.CheckEvidence, files core.Manifest) []string {
	var changed []string
	for name, old := range u.Dependencies {
		current, exists := files[name]
		if old != current || (old.Hash == "" && exists) {
			changed = append(changed, name)
		}
	}
	var names []string
	if u.Namespace != nil {
		names = u.Namespace
	} else if u.Precision == "snapshot" {
		names = u.Dependencies.Paths()
	}
	if names != nil {
		for _, name := range names {
			if _, ok := files[name]; !ok {
				changed = append(changed, name)
			}
		}
		for name := range files {
			if !slices.Contains(names, name) {
				changed = append(changed, name)
			}
		}
	}
	return sortedUnique(changed)
}

func legacyInvalidReason(p *core.Project, t *core.Task, r core.Report) string {
	if r.Evidence == nil {
		if r.Digest == p.Files.Digest() {
			return ""
		}
		return "dependencies_changed"
	}
	if reason := evidenceBoundary(t, r.Evidence); reason != "" {
		return reason
	}
	revision := ""
	if t.Plan != nil {
		revision = t.Plan.Revision
	}
	if r.Evidence.PlanRevision != revision {
		return "legacy_plan_changed"
	}
	if !legacyReportCurrent(p, t, r) {
		return "dependencies_changed"
	}
	return ""
}

func acceptanceCandidates(p *core.Project, t *core.Task) []evidenceCandidate {
	var candidates []evidenceCandidate
	for _, r := range p.Reports {
		if r.Evidence != nil && r.Evidence.Version != 0 {
			for _, u := range r.Evidence.Units {
				candidates = append(candidates, evidenceCandidate{u, r.ID, unitInvalidReason(p, t, r.Evidence, u), failedCheckDetails(r, u)})
			}
			continue
		}
		reason := legacyInvalidReason(p, t, r)
		deps := p.Files
		if r.Evidence != nil {
			deps = core.Clone(r.Evidence.Inputs)
			for n, f := range r.Evidence.Outputs {
				deps[n] = f
			}
		}
		kind := r.Kind
		if execution := reportExecution(r); execution != "" {
			kind = execution
		}
		var detail struct {
			Entry string `json:"entry"`
		}
		_ = json.Unmarshal(r.Details, &detail)
		paths := []string{}
		if kind == "static" {
			paths = fileNames(deps)
		} else if detail.Entry != "" {
			paths = []string{detail.Entry}
		}
		status := r.Status
		if kind == "go" && !goChecksPassed(r.Checks) {
			status = "failed"
		}
		u := core.CheckEvidence{Kind: kind, Paths: paths, Status: status, Dependencies: deps, Precision: "snapshot"}
		candidates = append(candidates, evidenceCandidate{u, r.ID, reason, failedCheckDetails(r, u)})
		// Legacy reports keep their whole-report and plan-revision boundaries.
		for _, u := range criterionUnits(t, deps, r.Checks, nil, r.Status != "passed") {
			u.Dependencies = deps
			u.Precision = "snapshot"
			candidates = append(candidates, evidenceCandidate{u, r.ID, reason, failedCheckDetails(r, u)})
		}
	}
	return candidates
}

func goChecksPassed(checks []core.Check) bool {
	build, test := false, false
	for _, c := range checks {
		build = build || c.Name == "go_build" && c.Status == "passed"
		test = test || c.Name == "go_test" && c.Status == "passed"
	}
	return build && test
}

func issueReason(reason string) string {
	switch reason {
	case "missing":
		return "从未记录对应检查"
	case "failed":
		return "对应版本的最新完整检查失败"
	case "dependencies_changed":
		return "检查依赖的文件内容或路径集合已变化"
	case "criterion_changed":
		return "验收条件、对应路径或必用输入已变化"
	case "task_changed":
		return "检查属于其他任务"
	case "instruction_changed":
		return "检查属于旧用户指令"
	case "legacy_plan_changed":
		return "旧报告绑定的计划已变化，缺少可复用的条件标识"
	case "evidence_incomplete", "unbound_target":
		return "旧报告或回执缺少可靠的目标与依赖信息"
	default:
		return "检查尚未确认通过"
	}
}

func issueBasis(p *core.Project, t *core.Task, issue AcceptanceIssue, deps core.Manifest, namespace []string) string {
	versions := dependencyFiles(p.Files, fileNames(deps))
	if len(versions) == 0 {
		versions = dependencyFiles(p.Files, issue.Paths)
	}
	addRequiredInputs(t, p.Files, versions)
	var names []string
	if namespace != nil {
		names = fileNames(p.Files)
	}
	taskID, instruction := "", 0
	if t != nil {
		taskID = t.ID
		instruction = len(t.InstructionMessages)
	}
	return core.Hash([]byte(core.JSON([]any{"acceptance-v1", taskID, instruction, issue.Kind, issue.Paths, normalizeRequirement(issue.Requirement), criterionKey(t, issue.Requirement, criterionPaths(t, issue.Requirement)), issue.Reason, versions, names})))
}

func assessAcceptance(p *core.Project) *AcceptanceAssessment {
	t := p.Tasks[p.CurrentTask]
	a := &AcceptanceAssessment{Code: "acceptance_incomplete", Issues: []AcceptanceIssue{}, Evidence: []AcceptanceReference{}}
	for _, r := range p.Reports {
		if reportCurrent(p, t, r) {
			a.Reports = append(a.Reports, acceptanceReport{r.ID, r.Kind, r.Status})
		}
	}
	paths := fileNames(p.Files)
	var delivery *core.DeliveryScope
	if t != nil && t.Plan != nil {
		delivery = t.Plan.Delivery
	}
	if delivery != nil {
		paths = sortedUnique(append(append(slices.Clone(delivery.Targets), delivery.Inputs...), delivery.Preserve...))
	}
	add := func(issue AcceptanceIssue, deps core.Manifest, namespace []string) {
		issue.Basis = issueBasis(p, t, issue, deps, namespace)
		a.Issues = append(a.Issues, issue)
	}
	if len(paths) == 0 {
		add(AcceptanceIssue{Code: "files_missing", Kind: "files", Reason: "missing", Observed: "没有可交付文件", Next: "先保存约定的交付文件"}, nil, nil)
	}
	for _, name := range paths {
		if _, ok := p.Files[name]; !ok {
			add(AcceptanceIssue{Code: "file_missing", Kind: "file", Paths: []string{name}, Reason: "missing", Observed: "交付范围缺少文件", Next: "保存该文件或核对已批准的交付范围"}, nil, nil)
		}
	}
	candidates := acceptanceCandidates(p, t)
	type need struct{ kind, name, requirement, next string }
	needs := []need{}
	for _, name := range paths {
		if _, ok := p.Files[name]; ok {
			needs = append(needs, need{"static", name, "", "补 check_artifacts(paths=[" + name + "])，内容断言应对应计划验收条件"})
		}
	}
	if delivery != nil {
		for _, criterion := range delivery.Criteria {
			for _, name := range sortedUnique(criterion.Paths) {
				if _, ok := p.Files[name]; ok {
					needs = append(needs, need{"criterion", name, normalizeRequirement(criterion.Requirement), "补与该条件及路径对应的内容断言或浏览器交互检查"})
				}
			}
		}
	}
	targets := paths
	if delivery != nil {
		targets = sortedUnique(append(slices.Clone(delivery.Targets), delivery.Preserve...))
	}
	web, py, ts, goProject := false, false, false, false
	for _, name := range targets {
		file, exists := p.Files[name]
		if !exists || file.ReadOnly {
			continue
		}
		switch path.Ext(name) {
		case ".html":
			web = true
		case ".py":
			py = true
		case ".ts", ".mts", ".cts", ".tsx":
			ts = true
		case ".go":
			goProject = true
		}
		if file.Binary && delivery == nil {
			add(AcceptanceIssue{Code: "delivery_scope_missing", Kind: "scope", Paths: []string{name}, Reason: "missing", Observed: "二进制交付缺少目标及内容验收条件", Next: "通过 propose_plan.delivery 声明目标和条件对应关系"}, nil, nil)
		}
	}
	for _, name := range targets {
		file, exists := p.Files[name]
		if !exists || file.ReadOnly {
			continue
		}
		kind := ""
		switch path.Ext(name) {
		case ".html":
			kind = "web"
			needs = append(needs, need{"browser", name, "", "分派具备权限的角色补 test_preview(entry=" + name + ") 的实际交互断言"})
		case ".py":
			kind = "python"
		case ".ts", ".mts", ".cts", ".tsx":
			if !web {
				kind = "typescript"
			}
		case ".js", ".mjs", ".cjs", ".jsx":
			if !web && !py && !ts && !goProject {
				kind = "javascript"
			}
		}
		if kind != "" {
			execution := "script"
			if kind == "web" {
				execution = "web"
			}
			needs = append(needs, need{kind, name, "", "分派具备权限的角色补 run_project(kind=" + execution + ", entry=" + name + ") 的实际运行"})
		}
	}
	if goProject {
		needs = append(needs, need{"go", "go.mod", "", "补 run_project(kind=go, entry=go.mod)，必须同时通过 go build 与 go test"})
	}
	counts := map[string]int{}
	for _, n := range needs {
		counts[n.kind]++
	}
	for _, n := range needs {
		var current, stale *evidenceCandidate
		for i := range candidates {
			c := candidates[i]
			if c.unit.Kind != n.kind && !(c.unit.Kind == "run" && (n.kind == "web" || n.kind == "javascript")) {
				continue
			}
			if n.kind == "criterion" && c.unit.Requirement != n.requirement {
				continue
			}
			if len(c.unit.Paths) > 0 && !slices.Contains(c.unit.Paths, n.name) {
				continue
			}
			if len(c.unit.Paths) == 0 && counts[n.kind] > 1 {
				c.reason = "unbound_target"
			}
			if c.reason == "" {
				current = &c
			} else {
				stale = &c
			}
		}
		if current != nil && current.unit.Status == "passed" {
			a.Evidence = append(a.Evidence, AcceptanceReference{n.kind, []string{n.name}, n.requirement, current.report})
			continue
		}
		selected, reason := current, "missing"
		if selected != nil {
			reason = "failed"
			if selected.unit.Status != "failed" {
				reason = "unknown"
			}
		} else if stale != nil {
			selected = stale
			reason = stale.reason
		}
		code := n.kind + "_missing"
		if reason != "missing" {
			code = n.kind + "_" + reason
		}
		issue := AcceptanceIssue{Code: code, Kind: n.kind, Paths: []string{n.name}, Requirement: n.requirement, Reason: reason, Observed: issueReason(reason), Next: n.next}
		var deps core.Manifest
		var namespace []string
		if selected != nil {
			issue.ReportID = selected.report
			if reason == "failed" && selected.failures != "" {
				issue.Observed += "：" + selected.failures
			}
			if reason == "dependencies_changed" {
				issue.ChangedPaths = changedDependencyPaths(selected.unit, p.Files)
				if len(issue.ChangedPaths) > 0 {
					issue.Observed += "：" + strings.Join(issue.ChangedPaths, ", ")
				}
			}
			deps = selected.unit.Dependencies
			namespace = selected.unit.Namespace
			if selected.unit.Precision == "snapshot" {
				namespace = fileNames(p.Files)
				deps = p.Files
			}
		}
		add(issue, deps, namespace)
	}
	sort.Slice(a.Issues, func(i, j int) bool { return a.Issues[i].Basis < a.Issues[j].Basis })
	a.Ready = len(a.Issues) == 0
	if a.Ready {
		a.Code = "acceptance_ready"
	}
	return a
}
