package model

import (
	"encoding/json"
	"fmt"
	"mockatoms/internal/core"
	"sort"
	"strings"
)

// The protocol owns envelope shape; argument validation remains with the tool.
// Report misplaced fields without repairing or executing an invalid intention.
func validateActionEnvelope(text string, tools []Tool) error {
	var root map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &root) != nil || root == nil {
		return nil // The strict parser reports malformed JSON and trailing data.
	}
	problems := unknownEnvelopeFields(root, []string{"content", "calls"}, "$")
	var calls []map[string]json.RawMessage
	if json.Unmarshal(root["calls"], &calls) != nil || len(calls) != 1 || calls[0] == nil {
		if len(problems) > 0 {
			return fmt.Errorf("模型动作 JSON 格式无效，未执行：%s", strings.Join(problems, "；"))
		}
		return nil
	}
	call := calls[0]
	var name string
	_ = json.Unmarshal(call["name"], &name)
	problems = append(problems, unknownEnvelopeFields(call, []string{"name", "arguments"}, "$.calls[0]")...)
	if _, exists := call["arguments"]; !exists {
		problems = append(problems, "$.calls[0].arguments 缺失：所有工具参数都必须放在此对象内")
	}
	if len(problems) == 0 {
		return nil
	}
	for _, tool := range tools {
		if tool.Name != name {
			continue
		}
		properties, _ := tool.Parameters["properties"].(map[string]any)
		var misplaced []string
		for key := range call {
			if _, defined := properties[key]; defined && key != "name" && key != "arguments" {
				misplaced = append(misplaced, "$.calls[0]."+key+" 应位于 $.calls[0].arguments."+key)
			}
		}
		sort.Strings(misplaced)
		problems = append(problems, misplaced...)
		example := map[string]any{"content": "", "calls": []any{map[string]any{"name": name, "arguments": map[string]any{}}}}
		problems = append(problems, "合法外层结构示例（arguments 须按工具 Schema 填写实际参数）："+core.JSON(example))
		break
	}
	return fmt.Errorf("模型动作 JSON 格式无效，未执行：%s", strings.Join(problems, "；"))
}

func unknownEnvelopeFields(fields map[string]json.RawMessage, allowed []string, location string) []string {
	var unknown []string
	for key := range fields {
		valid := false
		for _, name := range allowed {
			valid = valid || key == name
		}
		if !valid {
			unknown = append(unknown, location+"."+key+" 不允许出现在此层；允许字段："+strings.Join(allowed, ", "))
		}
	}
	sort.Strings(unknown)
	return unknown
}
