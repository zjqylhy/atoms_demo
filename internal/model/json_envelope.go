package model

import (
	"errors"
	"fmt"
	"mockatoms/internal/core"
	"mockatoms/internal/validation"
	"sort"
	"strings"
)

// Only the envelope belongs to the protocol. Argument properties are used for
// location hints here; their validation remains with the selected tool adapter.
func validateActionEnvelope(text string, tools []Tool) error {
	value, err := validation.Decode(text, "$")
	if err != nil {
		return fmt.Errorf("模型动作 JSON 格式无效，未执行：%w", err)
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	spec := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
		// Keep the established optional-prose behavior, including content:null.
		"content": map[string]any{"type": []string{"string", "null"}},
		"calls": map[string]any{"type": "array", "maxItems": 1, "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"name", "arguments"}, "properties": map[string]any{
				"name":      map[string]any{"type": "string", "enum": names, "description": "只使用当前角色可用的工具名称"},
				"arguments": map[string]any{"type": "object", "description": "所有工具参数都必须放在此对象内，按工具参数定义填写"},
			},
		}},
	}}
	problems := &validation.Error{}
	if err := validation.Validate(value, spec, "$"); err != nil {
		_ = errors.As(err, &problems)
	}
	root, _ := value.(map[string]any)
	if root != nil {
		if _, exists := root["calls"]; !exists {
			content, _ := root["content"].(string)
			if strings.TrimSpace(content) == "" {
				problems.Add(validation.Issue{Path: "$.calls", Code: "required", Actual: "缺失", Expected: "array", Hint: "有动作时填写一个调用；无动作时填写 [] 并提供非空 content 摘要"})
			}
		}
	}
	if len(problems.Issues) == 0 {
		return nil
	}
	addEnvelopeHints(problems, root, tools)
	return fmt.Errorf("模型动作 JSON 格式无效，未执行：%w", problems)
}

func addEnvelopeHints(problems *validation.Error, root map[string]any, tools []Tool) {
	calls, _ := root["calls"].([]any)
	for i := range problems.Issues {
		issue := &problems.Issues[i]
		for index, value := range calls {
			call, _ := value.(map[string]any)
			name, _ := call["name"].(string)
			location := fmt.Sprintf("$.calls[%d]", index)
			for _, tool := range tools {
				if name != tool.Name {
					continue
				}
				properties, _ := tool.Parameters["properties"].(map[string]any)
				for key := range properties {
					if issue.Code == "unknown_field" && issue.Path == validation.Child(location, key) {
						issue.Hint += "；" + issue.Path + " 应位于 " + validation.Child(location+".arguments", key)
					}
				}
				if issue.Path == location+".arguments" {
					example := map[string]any{"content": "", "calls": []any{map[string]any{"name": name, "arguments": map[string]any{}}}}
					issue.Hint += "；合法外层结构示例（arguments 须按工具 Schema 填写实际参数）：" + core.JSON(example)
				}
			}
		}
	}
}
