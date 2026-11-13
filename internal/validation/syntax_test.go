package validation

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSyntaxDiagnosisLocatesParserState(t *testing.T) {
	for _, test := range []struct{ name, input, path, reason, expected string }{
		{"object_close", `{"calls":[{"name":"act","arguments":{}]}`, "$.calls[0]", "对象尚未关闭", "}"},
		{"array_close", `{"items":[1}`, "$.items", "数组元素", "]"},
		{"missing_outer_closers", `{"calls":[{"name":"act","arguments":{}}`, "$.calls", "尚未关闭", "] }"},
		{"quoted_key", `{"a.b":[{"x":true]}`, `$["a.b"][0]`, "对象尚未关闭", "}"},
		{"missing_colon", `{"x" 1}`, "$.x", "不是冒号", ":"},
		{"missing_separator", `{"items":[1 2]}`, "$.items", "数组元素", ","},
		{"object_trailing_comma", `{"x":1,}`, "$", "字段名", "双引号"},
		{"array_trailing_comma", `[1,]`, "$[1]", "需要 JSON 值", "数字"},
		{"unescaped_quote", `{"content":"<meta name=\"viewport" content=\"width\">"}`, "$.content", "对象字段值", "}"},
		{"raw_newline", "{\"x\":\"first\nsecond\"}", "$.x", "控制字符", "转义"},
		{"invalid_escape", `{"x":"\q"}`, "$.x", "反斜杠", `\\`},
		{"invalid_unicode_escape", `{"x":"\u12Q4"}`, "$.x", "Unicode", "十六进制"},
		{"bad_number", `{"x":1.}`, "$.x", "数字", "数字"},
		{"bad_literal", `{"x":truf}`, "$.x", "拼写", "true"},
		{"unfinished_string", `{"x":"abc`, "$.x", "字符串", "结束双引号"},
		{"unfinished_escape", `{"x":"abc\`, "$.x", "字符串", "结束双引号"},
		{"unfinished_literal", `{"x":tru`, "$.x", "尚未写完", "JSON 值"},
		{"unfinished_number", `{"x":1e`, "$.x", "尚未写完", "JSON 值"},
		{"missing_value", `{"x":`, "$.x", "缺少完整的值", "字段值"},
		{"missing_after_comma", `[1,`, "$[1]", "分隔逗号", "下一"},
		{"trailing_text", "{\"中文\":1}\nextra", "$", "额外内容", "JSON 值"},
	} {
		t.Run(test.name, func(t *testing.T) {
			value, err := Decode(test.input, "$")
			var detail *Error
			if value != nil || !errors.As(err, &detail) || len(detail.Issues) != 1 {
				t.Fatalf("malformed input was accepted or lost diagnostics: %v", err)
			}
			issue := detail.Issues[0]
			if issue.Path != test.path || !strings.Contains(issue.Reason, test.reason) || !strings.Contains(issue.Expected, test.expected) {
				t.Fatalf("wrong parser diagnosis: %+v", issue)
			}
			if issue.Offset < 1 || issue.Offset > int64(len(test.input)+1) || issue.Line < 1 || issue.Column < 1 || !strings.Contains(issue.Excerpt, "⟦") {
				t.Fatalf("missing source location: %+v", issue)
			}
			if test.name == "unescaped_quote" && !strings.Contains(issue.Hint, "可能") {
				t.Fatal("ambiguous quote repair was presented as certain", issue.Hint)
			}
		})
	}
}

func TestSyntaxPositionUsesUTF8BytesAndCharacterColumns(t *testing.T) {
	input := "{\n  \"中文\": 1]\n}"
	_, err := Decode(input, "$")
	var detail *Error
	if !errors.As(err, &detail) {
		t.Fatal(err)
	}
	i := detail.Issues[0]
	if i.Offset != int64(strings.Index(input, "]")+1) || i.Line != 2 || i.Column != 10 || !strings.Contains(i.Excerpt, "⟦]⟧") {
		t.Fatalf("byte and character locations were confused: %+v", i)
	}
	_, err = Decode("{\r\n  \"😀\": true", "$")
	if !errors.As(err, &detail) || detail.Issues[0].Line != 2 || detail.Issues[0].Column != 12 || !strings.HasSuffix(detail.Issues[0].Excerpt, "⟦EOF⟧") {
		t.Fatal("EOF or supplementary character position is wrong", err)
	}
}

func TestSyntaxDiagnosisIsBoundedWithoutChangingAcceptance(t *testing.T) {
	for _, input := range []string{
		`{"content":"` + strings.Repeat("中文😀", 10000) + `"x}`,
		strings.Repeat("[", 10001),
		`{"` + strings.Repeat("long-key", 1000) + `":]}`,
	} {
		_, err := Decode(input, "$")
		var detail *Error
		if !errors.As(err, &detail) {
			t.Fatal("invalid input accepted", err)
		}
		issue := detail.Issues[0]
		if utf8.RuneCountInString(issue.Excerpt) > 168 || utf8.RuneCountInString(issue.Path) > 257 || !utf8.ValidString(issue.Excerpt) {
			t.Fatalf("unbounded or corrupt context: path=%d excerpt=%d", len(issue.Path), len(issue.Excerpt))
		}
	}
	valid := `{"content":"引号 \"、反斜杠 \\、括号 ] } 和 😀","items":[null,true,-1.2e3]}`
	if _, err := Decode(valid, "$"); err != nil {
		t.Fatal("diagnostics changed valid input acceptance", err)
	}
}

func TestCalculatorOriginalFailureDiagnostics(t *testing.T) {
	raw, err := os.ReadFile("testdata/calculator_format_errors.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name      string `json:"name"`
		Input     string `json:"input"`
		Path      string `json:"path"`
		Offset    int64  `json:"offset"`
		Reason    string `json:"reason"`
		Corrected string `json:"corrected"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil || len(fixtures) != 6 {
		t.Fatal("missing original failures", err)
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			value, err := Decode(f.Input, "$")
			var detail *Error
			if value != nil || !errors.As(err, &detail) {
				t.Fatal("original failure was accepted", err)
			}
			i := detail.Issues[0]
			if i.Offset != f.Offset || i.Path != f.Path || !strings.Contains(i.Reason, f.Reason) || i.Excerpt == "" {
				t.Fatalf("wrong original failure diagnosis: %+v", i)
			}
			if _, err := Decode(f.Corrected, "$"); err != nil {
				t.Fatal("syntactically corrected fixture rejected", err)
			}
		})
	}
}

func FuzzDecodeDiagnostics(f *testing.F) {
	for _, seed := range []string{`{"x":]}`, `{"x":"\q"}`, `{"中":[true,`, `{"a.b":[{"x":1]}`, "{\"x\":\"raw\nline\"}", strings.Repeat("[", 130), `{} extra`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		if len(input) > 65536 {
			t.Skip()
		}
		value, err := Decode(input, "$")
		if err == nil {
			if !json.Valid([]byte(input)) {
				t.Fatal("diagnostics accepted invalid JSON")
			}
			return
		}
		var detail *Error
		if value != nil || !errors.As(err, &detail) || len(detail.Issues) != 1 {
			t.Fatal("rejection lost its diagnostics", err)
		}
		issue := detail.Issues[0]
		if issue.Offset < 1 || issue.Offset > int64(len(input)+1) || issue.Line < 1 || issue.Column < 1 || issue.Reason == "" || !utf8.ValidString(issue.Excerpt) || utf8.RuneCountInString(issue.Excerpt) > 168 {
			t.Fatal("invalid or unbounded source diagnostic", err)
		}
	})
}
