// Package validation describes JSON failures without repairing the submitted
// value. Protocol and tool owners retain responsibility for their own schemas.
package validation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

const maxIssues = 16

type Issue struct {
	Path     string `json:"path"`
	Code     string `json:"code"`
	Actual   string `json:"actual"`
	Expected string `json:"expected"`
	Hint     string `json:"hint"`
}

type Error struct {
	Issues    []Issue `json:"issues"`
	Truncated bool    `json:"truncated,omitempty"`
}

func (e *Error) Error() string {
	parts := make([]string, 0, len(e.Issues)+1)
	for _, issue := range e.Issues {
		parts = append(parts, fmt.Sprintf("%s [%s]：实际 %s；要求 %s；%s", issue.Path, issue.Code, issue.Actual, issue.Expected, issue.Hint))
	}
	if e.Truncated {
		parts = append(parts, "问题较多，仅列出前 16 项；修正后重新校验")
	}
	return strings.Join(parts, "\n")
}

// Bound feedback independently of the original response, which remains saved.
func (e *Error) Add(issue Issue) {
	if len(e.Issues) == maxIssues {
		e.Truncated = true
		return
	}
	issue.Path = shorten(issue.Path, 256)
	e.Issues = append(e.Issues, issue)
}

// Decode rejects incomplete input and trailing content before shape validation.
// Syntax locations use byte offsets; field paths are available once JSON parses.
func Decode(text, location string) (any, error) {
	d := json.NewDecoder(strings.NewReader(text))
	var value any
	if err := d.Decode(&value); err != nil {
		issue := Issue{Path: location, Code: "syntax", Actual: "无效 JSON", Expected: "一个完整 JSON 值", Hint: "检查引号、逗号和括号，重新生成完整 JSON"}
		var syntax *json.SyntaxError
		if errors.As(err, &syntax) {
			issue.Actual = fmt.Sprintf("JSON 语法错误（第 %d 字节）", syntax.Offset)
		} else if errors.Is(err, io.EOF) {
			issue.Code, issue.Actual = "empty", "空白回复"
		} else if errors.Is(err, io.ErrUnexpectedEOF) {
			issue.Actual = "JSON 尚未闭合"
		}
		return nil, &Error{Issues: []Issue{issue}}
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, &Error{Issues: []Issue{{Path: location, Code: "trailing_content", Actual: "首个 JSON 后还有内容", Expected: "一个完整 JSON 值", Hint: "顶层对象闭合后只允许空白；删除追加的文字、围栏或第二个对象，不要模拟后续动作"}}}
	}
	return value, nil
}

// Validate implements the structural keywords used by the application's tool
// definitions. Runtime conditions and business rules stay in each tool adapter.
func Validate(value any, spec map[string]any, location string) error {
	problems := &Error{}
	problems.validate(value, spec, location)
	if len(problems.Issues) == 0 {
		return nil
	}
	return problems
}

func (e *Error) validate(value any, spec map[string]any, location string) {
	if e.Truncated {
		return
	}
	types := stringList(spec["type"])
	if len(types) > 0 {
		matches := false
		for _, kind := range types {
			matches = matches || isType(value, kind)
		}
		if !matches {
			e.Add(Issue{Path: location, Code: "type", Actual: typeName(value), Expected: expected(spec), Hint: "类型无效，按要求重新填写此字段" + description(spec)})
			return
		}
	}
	if choices, exists := spec["enum"]; exists {
		valid := false
		for _, item := range stringList(choices) {
			valid = valid || value == item
		}
		if !valid {
			e.Add(Issue{Path: location, Code: "enum", Actual: typeName(value) + "（不在允许值内）", Expected: expected(spec), Hint: "从允许值中选择，不要自行改写名称" + description(spec)})
		}
	}
	switch v := value.(type) {
	case map[string]any:
		properties, _ := spec["properties"].(map[string]any)
		allowed := sortedKeys(properties)
		required := stringList(spec["required"])
		for _, key := range required {
			if _, exists := v[key]; !exists {
				field, _ := properties[key].(map[string]any)
				e.Add(Issue{Path: Child(location, key), Code: "required", Actual: "缺失", Expected: expected(field), Hint: "缺少参数 " + key + "；请在此对象补齐；必填字段：" + strings.Join(required, ", ") + description(field)})
			}
		}
		for _, key := range sortedKeys(v) {
			field, exists := properties[key]
			if !exists {
				if extra, ok := spec["additionalProperties"].(bool); ok && !extra {
					e.Add(Issue{Path: Child(location, key), Code: "unknown_field", Actual: typeName(v[key]), Expected: "此层已定义的字段", Hint: "未知参数 " + shorten(key, 80) + "；删除此字段或按定义移到正确层级；允许字段：" + strings.Join(allowed, ", ") + "；必填字段：" + strings.Join(required, ", ")})
				}
				continue
			}
			child, _ := field.(map[string]any)
			e.validate(v[key], child, Child(location, key))
			if e.Truncated {
				return
			}
		}
	case []any:
		if limit, ok := spec["maxItems"].(int); ok && len(v) > limit {
			e.Add(Issue{Path: location, Code: "max_items", Actual: fmt.Sprintf("array（%d 项）", len(v)), Expected: fmt.Sprintf("最多 %d 项", limit), Hint: "按数量限制重新填写数组"})
		}
		items, _ := spec["items"].(map[string]any)
		if items == nil {
			return
		}
		for i, item := range v {
			e.validate(item, items, fmt.Sprintf("%s[%d]", location, i))
			if e.Truncated {
				return
			}
		}
	}
}

func isType(value any, kind string) bool {
	if kind == "integer" {
		n, ok := value.(float64)
		return ok && n >= math.MinInt64 && n < math.MaxInt64 && n == math.Trunc(n)
	}
	return typeName(value) == kind
}

func typeName(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return fmt.Sprintf("%T", value)
	}
}

func expected(spec map[string]any) string {
	text := strings.Join(stringList(spec["type"]), " 或 ")
	if text == "" {
		text = "符合字段定义的值"
	}
	if choices, exists := spec["enum"]; exists {
		text += "；允许值：" + strings.Join(stringList(choices), ", ")
	}
	return text
}

func description(spec map[string]any) string {
	if text, _ := spec["description"].(string); text != "" {
		return "；" + text
	}
	return ""
}

func stringList(value any) []string {
	switch v := value.(type) {
	case string:
		return []string{v}
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func sortedKeys(v map[string]any) []string {
	keys := make([]string, 0, len(v))
	for key := range v {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func Child(parent, key string) string {
	identifier := key != ""
	for i, char := range key {
		if !(char == '_' || char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || i > 0 && char >= '0' && char <= '9') {
			identifier = false
			break
		}
	}
	if identifier {
		return parent + "." + key
	}
	encoded, _ := json.Marshal(key)
	return parent + "[" + string(encoded) + "]"
}

func shorten(text string, limit int) string {
	for i := range text {
		if limit == 0 {
			return text[:i] + "…"
		}
		limit--
	}
	return text
}
