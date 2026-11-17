package core

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
)

const CheckEvidenceVersion = 1

func (m Manifest) Paths() []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// MatchesFiles checks only file facts. Task/instruction/criterion boundaries
// are additional acceptance gates; previewing bytes grants no delivery approval.
func (u CheckEvidence) MatchesFiles(files Manifest) bool {
	if len(u.Dependencies) == 0 || (u.Precision != "exact" && u.Precision != "snapshot") {
		return false
	}
	if u.Precision == "snapshot" && !slices.Equal(u.Dependencies.Paths(), files.Paths()) {
		return false
	}
	if u.Namespace != nil && !slices.Equal(u.Namespace, files.Paths()) {
		return false
	}
	for name, expected := range u.Dependencies {
		actual, exists := files[name]
		if expected.Hash == "" {
			if exists {
				return false
			}
			continue
		}
		if !exists || actual != expected {
			return false
		}
	}
	return true
}

// PreviewHTML uses the same dependency facts as acceptance. An unrelated output
// change must not hide a previously built page or a saved version's preview.
func PreviewHTML(reports []Report, files Manifest) string {
	return findWebPreview(reports, files).html
}

type webPreview struct {
	html, entry, reportID string
}

func findWebPreview(reports []Report, files Manifest) webPreview {
	if len(reports) == 0 {
		return webPreview{}
	}
	digest := files.Digest()
	for _, report := range slices.Backward(reports) {
		entry, matches := previewReportEntry(report, files, digest)
		if !matches {
			continue
		}
		var details struct {
			HTML string `json:"preview_html"`
		}
		if json.Unmarshal(report.Details, &details) != nil || details.HTML == "" {
			continue
		}
		if entry == "" {
			entry = legacyPreviewEntry(report.Kind, files)
		}
		return webPreview{html: details.HTML, entry: entry, reportID: report.ID}
	}
	return webPreview{}
}

func previewReportEntry(report Report, files Manifest, digest string) (string, bool) {
	evidence := report.Evidence
	if evidence == nil || evidence.Version == 0 {
		return "", report.Digest == digest
	}
	if evidence.Version != CheckEvidenceVersion {
		return "", false
	}
	entry, matches := "", false
	for _, unit := range evidence.Units {
		if (unit.Kind != "web" && unit.Kind != "browser") || !unit.MatchesFiles(files) {
			continue
		}
		matches = true
		if len(unit.Paths) != 1 || !isPreviewEntry(files, unit.Paths[0]) {
			continue
		}
		if entry != "" && entry != unit.Paths[0] {
			return "", true
		}
		entry = unit.Paths[0]
	}
	return entry, matches
}

func isPreviewEntry(files Manifest, name string) bool {
	file, exists := files[name]
	return exists && !file.Binary && strings.HasSuffix(name, ".html")
}

func legacyPreviewEntry(kind string, files Manifest) string {
	// Packaged templates and shared copies build a known root entry. For other
	// old reports, multiple HTML sources leave the saved page's name ambiguous.
	if (kind == "packaged_browser" || kind == "shared_preview") && isPreviewEntry(files, "index.html") {
		return "index.html"
	}
	entry := ""
	for name := range files {
		if !isPreviewEntry(files, name) {
			continue
		}
		if entry != "" {
			return ""
		}
		entry = name
	}
	return entry
}
