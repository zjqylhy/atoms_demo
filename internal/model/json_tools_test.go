package model

import (
	"errors"
	"mockatoms/internal/config"
	"mockatoms/internal/core"
	"mockatoms/internal/validation"
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

func TestJSONEnvelopeReportsAllShapeFailures(t *testing.T) {
	tools := []Tool{{Name: "inspect", Parameters: map[string]any{"type": "object"}}}
	for _, test := range []struct{ input, path, code string }{
		{`null`, "$", "type"},
		{`{"content":42,"calls":[]}`, "$.content", "type"},
		{`{"calls":{}}`, "$.calls", "type"},
		{`{"calls":[{}]}`, "$.calls[0].name", "required"},
		{`{"calls":[{"name":"inspect"}]}`, "$.calls[0].arguments", "required"},
		{`{"calls":[{"name":3,"arguments":{}}]}`, "$.calls[0].name", "type"},
		{`{"calls":[{"name":"unavailable","arguments":{}}]}`, "$.calls[0].name", "enum"},
		{`{"calls":[{"name":"inspect","arguments":null}]}`, "$.calls[0].arguments", "type"},
		{`{"calls":[{"name":"inspect","arguments":[]}]}`, "$.calls[0].arguments", "type"},
		{`{"calls":[{},{}]}`, "$.calls", "max_items"},
	} {
		t.Run(test.input, func(t *testing.T) {
			message, err := parseJSONAction(test.input, tools)
			var detail *validation.Error
			if len(message.ToolCalls) != 0 || !errors.As(err, &detail) {
				t.Fatal("failure lost structured feedback or returned an action", message, err)
			}
			found := false
			for _, issue := range detail.Issues {
				found = found || issue.Path == test.path && issue.Code == test.code && issue.Actual != "" && issue.Expected != "" && issue.Hint != ""
			}
			if !found {
				t.Fatal("missing precise repair issue", err)
			}
		})
	}
	_, err := parseJSONAction(`{"calls":[{}]}`, tools)
	if !strings.Contains(err.Error(), "$.calls[0].name") || !strings.Contains(err.Error(), "$.calls[0].arguments") || !strings.Contains(err.Error(), "允许值：inspect") {
		t.Fatal("missing name and arguments must be reported together", err)
	}
	// Arguments remain raw and are not business-validated by the protocol.
	text := `{"calls":[{"name":"inspect","arguments":{"large":9007199254740993}}]}`
	message, err := parseJSONAction(text, tools)
	if err != nil || message.ToolCalls[0].Function.Arguments != `{"large":9007199254740993}` {
		t.Fatal("protocol changed or interpreted business arguments", message, err)
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

func TestJSONEnvelopeFeedbackLocatesMisplacedToolArguments(t *testing.T) {
	for _, name := range []string{"assign_step", "renamed_action"} {
		tools := []Tool{{Name: name, Parameters: map[string]any{"type": "object", "properties": map[string]any{"role": map[string]any{"type": "string"}, "goal": map[string]any{"type": "string"}}}}}
		bad := `{"content":"delegate","calls":[{"name":"` + name + `","role":"Alex","goal":"check the saved input"}]}`
		message, err := parseJSONAction(bad, tools)
		if err == nil || len(message.ToolCalls) != 0 {
			t.Fatal("invalid argument nesting was executed", message, err)
		}
		for _, want := range []string{"$.calls[0].role", "$.calls[0].arguments.role", "$.calls[0].arguments [required]", "允许字段：arguments, name", `"name":"` + name + `"`, `"arguments":{}`} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("repair feedback omitted %q: %v", want, err)
			}
		}
		correct := `{"content":"delegate","calls":[{"name":"` + name + `","arguments":{"role":"Alex","goal":"check the saved input"}}]}`
		if fixed, err := parseJSONAction(correct, tools); err != nil || len(fixed.ToolCalls) != 1 {
			t.Fatal("corrected nesting rejected", fixed, err)
		}
	}
}
