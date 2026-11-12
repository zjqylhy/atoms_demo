package model

import (
	"mockatoms/internal/config"
	"mockatoms/internal/core"
	"strings"
	"testing"
)

func TestJSONToolAdaptation(t *testing.T) {
	tools := []Tool{{Name: "read_file", Parameters: map[string]any{"type": "object"}}}
	m, err := parseJSONAction(`{"content":"读取文件","calls":[{"name":"read_file","arguments":{"path":"notes.md"}}]}`, tools)
	if err != nil || len(m.ToolCalls) != 1 {
		t.Fatal(m, err)
	}
	for _, valid := range []string{`{"calls":[{"name":"read_file","arguments":{"path":"notes.md"}}]}`, `{"content":null,"calls":[{"name":"read_file","arguments":{"path":"notes.md"}}]}`} {
		if parsed, err := parseJSONAction(valid, tools); err != nil || len(parsed.ToolCalls) != 1 {
			t.Fatal("optional prose rejected", err)
		}
	}
	c := New(config.Config{Model: "chat", ToolMode: "json"})
	messages := []core.ModelMessage{{Role: "system", Content: "system"}, m, {Role: "tool", ToolCallID: m.ToolCalls[0].ID, Content: `{"ok":true}`}}
	payload := core.JSON(c.jsonPayload(messages, tools, len(messages)))
	if strings.Contains(payload, `"role":"tool"`) || strings.Contains(payload, `"tool_calls":`) || !strings.Contains(payload, `"type":"json_object"`) {
		t.Fatal(payload)
	}
	if prose, err := parseJSONAction(`{"content":"专家已完成"}`, tools); err != nil || prose.Content != "专家已完成" || len(prose.ToolCalls) != 0 {
		t.Fatal("prose-only handoff rejected", prose, err)
	}
	for _, bad := range []string{`{}`, `{"content":""}`, `{"content":"x","calls":null}`, `{"content":"x","name":"read_file","arguments":{}}`, `{"content":"x","calls":[{"name":"host_shell","arguments":{}}]}`, `{"content":"x","calls":[{"name":"read_file","arguments":"x"}]}`, `{"content":"x","calls":[],"extra":true}`} {
		if _, err := parseJSONAction(bad, tools); err == nil {
			t.Fatal("invalid action accepted", bad)
		}
	}
}

func TestJSONActionRejectsTrailingContent(t *testing.T) {
	tools := []Tool{{Name: "read_file"}}
	action := `{"content":"","calls":[{"name":"read_file","arguments":{"path":"notes.md"}}]}`
	if m, err := parseJSONAction(action+" \t\r\n", tools); err != nil || len(m.ToolCalls) != 1 {
		t.Fatalf("trailing whitespace should be allowed: %v", err)
	}
	for _, suffix := range []string{"\n解释", "\n" + action} {
		m, err := parseJSONAction(action+suffix, tools)
		if err == nil || !strings.Contains(err.Error(), "顶层对象闭合后只允许空白") || len(m.ToolCalls) != 0 {
			t.Fatalf("unexpected outcome for trailing content: %v, %d calls", err, len(m.ToolCalls))
		}
	}
}

func TestJSONEncoderPreservesSelectedEvidence(t *testing.T) {
	c := New(config.Config{ToolMode: "json"})
	body := strings.Repeat("old-content", 1000)
	messages := []core.ModelMessage{{Role: "system", Content: "system"},
		{Role: "tool", ToolCallID: "read", Content: core.JSON(map[string]any{"result": map[string]any{"content": body}})},
		{Role: "assistant", Content: "INVALID_BODY", InvalidAction: true},
		{Role: "user", Content: "format feedback"}}
	for _, boundary := range []int{0, len(messages)} {
		wire := core.JSON(c.jsonPayload(messages, nil, boundary))
		if !strings.Contains(wire, body) || strings.Contains(wire, "INVALID_BODY") {
			t.Fatal("encoder changed selected evidence")
		}
	}
}
