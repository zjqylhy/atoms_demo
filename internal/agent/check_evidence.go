package agent

import (
	"encoding/json"
	"mockatoms/internal/core"
	"path"
	"slices"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

const evidenceVersion = core.CheckEvidenceVersion

func sortedUnique(values []string) []string {
	values = slices.Clone(values)
	sort.Strings(values)
	return slices.Compact(values)
}

func fileNames(files core.Manifest) []string { return files.Paths() }

func normalizeRequirement(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
}

func criterionKey(t *core.Task, requirement string, paths []string) string {
	var inputs []string
	if t != nil && t.Plan != nil && t.Plan.Delivery != nil {
		inputs = t.Plan.Delivery.Inputs
	}
	return core.Hash([]byte(core.JSON([]any{normalizeRequirement(requirement), sortedUnique(paths), sortedUnique(inputs)})))
}

func criterionPaths(t *core.Task, requirement string) []string {
	var paths []string
	if t != nil && t.Plan != nil && t.Plan.Delivery != nil {
		for _, c := range t.Plan.Delivery.Criteria {
			if normalizeRequirement(c.Requirement) == normalizeRequirement(requirement) {
				paths = append(paths, c.Paths...)
			}
		}
	}
	return sortedUnique(paths)
}

func dependencyFiles(files core.Manifest, names []string) core.Manifest {
	deps := core.Manifest{}
	for _, name := range names {
		// An absent path is evidence too; a subsequently added file invalidates it.
		deps[name] = files[name]
	}
	return deps
}

func addRequiredInputs(t *core.Task, files core.Manifest, deps core.Manifest) {
	if t != nil && t.Plan != nil && t.Plan.Delivery != nil {
		for _, name := range t.Plan.Delivery.Inputs {
			deps[name] = files[name]
		}
	}
}

func evidenceBoundary(t *core.Task, e *core.ReportEvidence) string {
	if t == nil || e.TaskID != t.ID {
		return "task_changed"
	}
	if e.InstructionVersion != len(t.InstructionMessages) {
		return "instruction_changed"
	}
	return ""
}

func unitInvalidReason(p *core.Project, t *core.Task, e *core.ReportEvidence, u core.CheckEvidence) string {
	if reason := evidenceBoundary(t, e); reason != "" {
		return reason
	}
	if e.Version != evidenceVersion || len(u.Dependencies) == 0 || (u.Precision != "exact" && u.Precision != "snapshot") {
		return "evidence_incomplete"
	}
	if !u.MatchesFiles(p.Files) {
		return "dependencies_changed"
	}
	if u.Requirement != "" {
		paths := criterionPaths(t, u.Requirement)
		if len(paths) == 0 || u.CriterionKey != criterionKey(t, u.Requirement, paths) {
			return "criterion_changed"
		}
	}
	return ""
}

// Static checks are independent by file. All assertions for one criterion remain
// a single group, so a passing assertion cannot mask a failed sibling.
func staticEvidence(t *core.Task, s scope, files map[string]string, manifest core.Manifest, r core.Report) *core.ReportEvidence {
	e := evidenceFor(t, s, manifest, nil)
	e.Version = evidenceVersion
	for _, name := range fileNames(manifest) {
		deps := dependencyFiles(manifest, []string{name})
		if path.Ext(name) == ".html" {
			tokens := html.NewTokenizer(strings.NewReader(files[name]))
			for {
				typ := tokens.Next()
				if typ == html.ErrorToken {
					break
				}
				if typ != html.StartTagToken && typ != html.SelfClosingTagToken {
					continue
				}
				for _, a := range tokens.Token().Attr {
					if a.Key != "src" && a.Key != "href" {
						continue
					}
					ref := strings.Split(strings.Split(a.Val, "?")[0], "#")[0]
					if ref == "" || strings.Contains(ref, ":") || strings.HasPrefix(ref, "//") {
						continue
					}
					target := path.Join(path.Dir(name), ref)
					deps[target] = manifest[target]
				}
			}
		}
		status := "passed"
		for _, c := range r.Checks {
			if (c.Name == name || slices.Contains(c.Paths, name)) && c.Status == "failed" {
				status = "failed"
			}
		}
		e.Units = append(e.Units, core.CheckEvidence{Kind: "static", Paths: []string{name}, Status: status, Dependencies: deps, Precision: "exact"})
	}
	e.Units = append(e.Units, criterionUnits(t, manifest, r.Checks, nil, false)...)
	return e
}

func criterionUnits(t *core.Task, files core.Manifest, checks []core.Check, base *core.CheckEvidence, blocked bool) []core.CheckEvidence {
	groups := map[string][]core.Check{}
	for _, c := range checks {
		if c.Requirement != "" {
			key := normalizeRequirement(c.Requirement)
			groups[key] = append(groups[key], c)
		}
	}
	var units []core.CheckEvidence
	for requirement, group := range groups {
		var checked []string
		status := "passed"
		for _, c := range group {
			checked = append(checked, c.Paths...)
			if c.Status != "passed" {
				status = "failed"
			}
		}
		if blocked {
			status = "failed"
		}
		paths := criterionPaths(t, requirement)
		deps := dependencyFiles(files, checked)
		u := core.CheckEvidence{Kind: "criterion", Paths: sortedUnique(checked), Requirement: requirement, CriterionKey: criterionKey(t, requirement, paths), Status: status, Dependencies: deps, Precision: "exact"}
		if base != nil {
			u.Dependencies = core.Clone(base.Dependencies)
			u.Precision = base.Precision
			u.Namespace = slices.Clone(base.Namespace)
			u.Reason = base.Reason
			for name, file := range deps {
				u.Dependencies[name] = file
			}
		}
		addRequiredInputs(t, files, u.Dependencies)
		units = append(units, u)
	}
	sort.Slice(units, func(i, j int) bool { return units[i].Requirement < units[j].Requirement })
	return units
}

// The runner only supplies paths; hashes always come from the registered input
// snapshot. Missing, malformed or unsupported receipts fall back conservatively.
type runnerDependencies struct {
	Version   int      `json:"version"`
	Precision string   `json:"precision"`
	Paths     []string `json:"paths"`
	Namespace []string `json:"namespace"`
}

func runnerEvidence(t *core.Task, e *core.ReportEvidence, r core.Report, args map[string]any) {
	e.Version = evidenceVersion
	files := core.Clone(e.Inputs)
	for name, f := range e.Outputs {
		files[name] = f
	}
	kind := reportExecution(r)
	if r.Kind == "browser" {
		kind = "browser"
	}
	entry := str(args, "entry")
	if entry == "" && (kind == "web" || kind == "browser") {
		entry = "index.html"
	}
	u := core.CheckEvidence{Kind: kind, Paths: []string{entry}, Status: r.Status, Dependencies: files, Precision: "snapshot", Reason: "运行时可读取全部输入，或缺少可信完整依赖清单"}
	if kind == "go" && !goChecksPassed(r.Checks) {
		u.Status = "failed"
	}
	var details struct {
		Dependencies runnerDependencies `json:"dependencies"`
	}
	_ = json.Unmarshal(r.Details, &details)
	d := details.Dependencies
	valid := (kind == "web" || kind == "browser") && d.Version == 1 && d.Precision == "exact" && slices.Contains(d.Paths, entry) && slices.Equal(sortedUnique(d.Namespace), fileNames(e.Inputs))
	for _, name := range d.Paths {
		if _, err := core.FilePath(name); err != nil || e.Inputs[name].Hash == "" {
			valid = false
		}
	}
	if valid {
		u.Precision = "exact"
		u.Reason = "构建依赖及模块解析文件集合"
		u.Dependencies = dependencyFiles(files, d.Paths)
		u.Namespace = sortedUnique(d.Namespace)
	}
	e.Units = append(e.Units, u)
	e.Units = append(e.Units, criterionUnits(t, files, r.Checks, &u, r.Status != "passed")...)
}
