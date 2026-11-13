package agent

import (
	"archive/zip"
	"bytes"
	"errors"
	"mockatoms/internal/core"
	"mockatoms/internal/validation"
	"os"
	"strings"
	"testing"
)

func acceptanceArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	for name, content := range files {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestArchiveSelectionDoesNotConfuseNamesAndPatterns(t *testing.T) {
	data := acceptanceArchive(t, map[string]string{"[a].xml": "literal", "a.xml": "pattern", "broken[.xml": "unclosed", "[Content_Types].xml": "types"})
	for _, a := range []core.ArtifactAssertion{
		{Kind: "archive_text_contains", Member: "[a].xml", MemberMatch: "exact", Expected: "literal"},
		{Kind: "archive_text_contains", Member: "[a].xml", MemberMatch: "glob", Expected: "pattern"},
		{Kind: "archive_text_contains", Member: "[a].xml", Expected: "pattern"},
		{Kind: "archive_text_contains", Member: "broken[.xml", MemberMatch: "exact", Expected: "unclosed"},
		{Kind: "archive_count", Member: "[Content_Types].xml", MemberMatch: "exact", Count: 1},
	} {
		a.Path, a.Requirement = "result.zip", "选择正确成员"
		if err := validateAssertions([]core.ArtifactAssertion{a}); err != nil {
			t.Fatal("valid selector rejected", a, err)
		}
		if err := assertArtifact(data, a); err != nil {
			t.Fatal("wrong member selection", a, err)
		}
	}
	err := assertArtifact(data, core.ArtifactAssertion{Kind: "archive_count", Member: "[Content_Types].xml", Count: 1})
	if err == nil || !strings.Contains(err.Error(), "member_match=\"exact\"") || !strings.Contains(err.Error(), "0 个成员") {
		t.Fatal("ambiguous member lacks actionable feedback", err)
	}
}

func TestNegativeAssertionsRequireValidSelectedContent(t *testing.T) {
	data := acceptanceArchive(t, map[string]string{
		"items/one.xml": "<root>clean</root>", "items/two.xml": "<root>&lt;div&gt;forbidden</root>",
		"invalid.xml": "<root>", "empty.xml": "", "many.xml": "<a/><b/>", "binary.txt": string([]byte{255, 254}),
	})
	for _, test := range []struct {
		name, kind, member, expected string
		passed                       bool
	}{
		{"absent in all", "archive_text_not_contains", "items/*.xml", "card-grid", true},
		{"present in one", "archive_text_not_contains", "items/*.xml", "forbidden", false},
		{"XML decoded text", "archive_xml_not_contains", "items/*.xml", "<div>", false},
		{"XML absent in all", "archive_xml_not_contains", "items/*.xml", "card-grid", true},
		{"zero selection", "archive_xml_not_contains", "missing/*.xml", "forbidden", false},
		{"invalid XML", "archive_xml_not_contains", "invalid.xml", "forbidden", false},
		{"empty XML", "archive_xml_not_contains", "empty.xml", "forbidden", false},
		{"multiple roots", "archive_xml_not_contains", "many.xml", "forbidden", false},
		{"invalid UTF8", "archive_text_not_contains", "binary.txt", "forbidden", false},
		{"empty expected", "archive_text_not_contains", "items/*.xml", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := assertArtifact(data, core.ArtifactAssertion{Kind: test.kind, Member: test.member, Expected: test.expected})
			if (err == nil) != test.passed {
				t.Fatal("incorrect negative assertion result", err)
			}
		})
	}
	for _, text := range []string{"forbidden", string([]byte{255})} {
		if assertArtifact([]byte(text), core.ArtifactAssertion{Kind: "text_not_contains", Expected: "forbidden"}) == nil {
			t.Fatal("negative text check accepted forbidden or unreadable data")
		}
	}
}

func TestAssertionConditionsReturnStructuredLocations(t *testing.T) {
	err := validateAssertions([]core.ArtifactAssertion{
		{Path: "output.zip", Kind: "archive_count", Requirement: "检查数量"},
		{Path: "output.zip", Kind: "archive_text_not_contains", Member: "x", MemberMatch: "guess", Requirement: "检查内容"},
	})
	var detail *validation.Error
	if !errors.As(err, &detail) || len(detail.Issues) != 4 {
		t.Fatal("lost assertion condition failures", err)
	}
	for _, field := range []string{"assertions[0].member", "assertions[0].count", "assertions[1].member_match", "assertions[1].expected"} {
		if !strings.Contains(err.Error(), field) {
			t.Fatal("missing actionable field path", field, err)
		}
	}
}

func TestHistoricalCandidateLiteralMemberAndNegativeChecks(t *testing.T) {
	name := os.Getenv("ATOMS_REPAIR_MATCHING_FIXTURE")
	if name == "" {
		t.Skip("requires the saved v5 runner candidate")
	}
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if core.Hash(data) != "b44d2fbc6e04b636ac86bec55a7eca75b5bb29da18123b449e1073634e038b8b" {
		t.Fatal("unexpected real candidate fixture")
	}
	assertions := []core.ArtifactAssertion{
		{Kind: "archive_count", Member: "[Content_Types].xml", MemberMatch: "exact", Count: 1},
		{Kind: "archive_count", Member: "ppt/slides/slide*.xml", MemberMatch: "glob", Count: 10},
		{Kind: "archive_xml_contains", Member: "ppt/slides/slide2.xml", MemberMatch: "exact", Expected: "鬃毛邮报是什么"},
		{Kind: "archive_xml_not_contains", Member: "ppt/slides/slide*.xml", MemberMatch: "glob", Expected: "<div"},
		{Kind: "archive_xml_not_contains", Member: "ppt/slides/slide*.xml", MemberMatch: "glob", Expected: "card-grid"},
		{Kind: "archive_xml_not_contains", Member: "ppt/slides/slide*.xml", MemberMatch: "glob", Expected: "翻页方式"},
	}
	for _, a := range assertions {
		if err := assertArtifact(data, a); err != nil {
			t.Fatal("actual candidate rejected", a, err)
		}
	}
	// The old ambiguous pattern still fails with guidance, never by silently
	// changing its meaning or discarding the failed assertion.
	assertions[0].MemberMatch = ""
	if err := assertArtifact(data, assertions[0]); err == nil || !strings.Contains(err.Error(), "exact") {
		t.Fatal("historical failure not reproduced", err)
	}
}
