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
	payload := core.JSON(c.jsonPayload([]core.ModelMessage{{Role: "system", Content: "system"}, m, {Role: "tool", ToolCallID: m.ToolCalls[0].ID, Content: `{"ok":true}`}}, tools))
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

func TestJSONHistoryKeepsEvidenceWithoutRepeatingFileBodies(t *testing.T) {
	c := New(config.Config{Model: "chat", ToolMode: "json"})
	body := strings.Repeat("large-file-body", 1000)
	messages := []core.ModelMessage{
		{Role: "system", Content: "system"},
		{Role: "assistant", Content: body, InvalidAction: true},
		{Role: "assistant", ToolCalls: []core.Call{{ID: "saved", Type: "function", Function: core.Function{Name: "write_file", Arguments: core.JSON(map[string]any{"path": "app.js", "content": body})}}}},
		{Role: "tool", ToolCallID: "saved", Content: `{"ok":true}`},
	}
	wire := core.JSON(c.jsonPayload(messages, nil))
	if strings.Contains(wire, body) || !strings.Contains(wire, core.Hash([]byte(body))) || !strings.Contains(wire, "saved") {
		t.Fatal("history evidence/size regression")
	}
	projected := c.jsonPayload(messages, nil).(map[string]any)["messages"].([]map[string]string)
	if projected[2]["role"] != "user" || strings.Contains(projected[2]["content"], `"arguments"`) || strings.Contains(wire, "historical_content_note") {
		t.Fatal("compression metadata presented as executable tool arguments")
	}
	if messages[1].Content != body || !strings.Contains(messages[2].ToolCalls[0].Function.Arguments, body) {
		t.Fatal("stored history mutated")
	}
}

func TestOldReadBodyIsCompactedButLatestReadRemainsExact(t *testing.T) {
	c := New(config.Config{ToolMode: "json"})
	oldBody := strings.Repeat("old-content", 1000)
	latest := "最新文件正文，不能改动"
	messages := []core.ModelMessage{
		{Role: "system", Content: "system"},
		{Role: "tool", ToolCallID: "old", Content: core.JSON(map[string]any{"ok": true, "result": map[string]any{"path": "a.js", "content": oldBody}})},
		{Role: "assistant", Content: "读取当前内容"},
		{Role: "tool", ToolCallID: "new", Content: core.JSON(map[string]any{"ok": true, "result": map[string]any{"path": "a.js", "content": latest}})},
	}
	wire := core.JSON(c.jsonPayload(messages, nil))
	if strings.Contains(wire, oldBody) || !strings.Contains(wire, latest) || !strings.Contains(wire, core.Hash([]byte(oldBody))) {
		t.Fatal("historical reads grew or latest body lost")
	}
	if !strings.Contains(messages[1].Content, oldBody) {
		t.Fatal("persisted history changed")
	}
	shortBody := "已确认的简短需求，不应在格式修正时被省略"
	messages[1].Content = core.JSON(map[string]any{"result": map[string]any{"content": shortBody}})
	if wire := core.JSON(c.jsonPayload(messages, nil)); !strings.Contains(wire, shortBody) {
		t.Fatal("short requirements lost in history")
	}
}
