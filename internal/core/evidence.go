package core

import (
	"encoding/json"
	"slices"
	"sort"
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

// Preview uses the same dependency facts as acceptance. An unrelated output
// change must not hide a previously built page or a saved version's preview.
func PreviewHTML(reports []Report, files Manifest) string {
	markup := ""
	digest := files.Digest()
	for _, r := range reports {
		current := r.Digest == digest
		if r.Evidence != nil && r.Evidence.Version != 0 {
			current = false
			if r.Evidence.Version == CheckEvidenceVersion {
				for _, u := range r.Evidence.Units {
					if (u.Kind == "web" || u.Kind == "browser") && u.MatchesFiles(files) {
						current = true
					}
				}
			}
		}
		if !current {
			continue
		}
		var details struct {
			HTML string `json:"preview_html"`
		}
		if json.Unmarshal(r.Details, &details) == nil && details.HTML != "" {
			markup = details.HTML
		}
	}
	return markup
}
