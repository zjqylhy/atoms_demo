package agent

import (
	"context"
	"io"
	"mockatoms/internal/config"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkingMaterialsSurviveRetriesAndBothSummaries(t *testing.T) {
	for _, protocol := range []string{"json", "chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			p, task, f := contextFixture()
			body := "CURRENT_EVIDENCE_" + strings.Repeat("x", 3123)
			retainMaterial(f, core.WorkingMaterial{Resource: "query:schema", Version: "H1", Source: "actual-result", Content: body, Reason: "used by current subgoal"})
			retainMaterial(f, core.WorkingMaterial{Resource: "other:resource", Version: "H2", Source: "actual-result-2", Content: "SECOND_REQUIRED_INPUT"})
			for i := 1; i <= 8; i++ {
				contextStep(task, f, i, "renamed_tool", `{"ok":true,"result":"history"}`)
			}
			f.Messages = append(f.Messages, core.ModelMessage{Role: "assistant", Content: "BAD", InvalidAction: true}, core.ModelMessage{Role: "user", Content: formatFeedbackPrefix + "MISSING_FIELD"})
			cfg := config.Config{Model: "test", APIKey: "secret", Protocol: protocol, MaxRequestBytes: 100000}
			if protocol == "json" {
				cfg.Protocol, cfg.ToolMode = "chat_completions", "json"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wire, _ := io.ReadAll(r.Body)
				for _, want := range []string{body, "SECOND_REQUIRED_INPUT", "H1", "H2", "MISSING_FIELD", "KEEP_CONFIRMED_ANSWER", "KEEP_LATEST_CORRECTION"} {
					if !strings.Contains(string(wire), want) {
						t.Errorf("actual request omitted %s", excerpt(want, 60))
					}
				}
				if protocol == "responses" {
					io.WriteString(w, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"text":"done"}]}]}`)
				} else if protocol == "json" {
					writeDialogueReply(w, `{"content":"done","calls":[]}`)
				} else {
					io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
				}
			}))
			defer server.Close()
			cfg.BaseURL = server.URL
			client := model.New(cfg)
			for level := 0; level < 3; level++ {
				if level > 0 {
					f.Context = summarizeFrame(task, f, 7)
				}
				if level > 1 {
					f.Dialogue = &core.DialogueSummary{Covered: map[string]int{"user:1": 10000, "user:2": 10000}, Items: []core.DialogueSummaryItem{{Kind: "conclusion", Text: "summary omitted requirements", Sources: []string{"frame:1"}}}}
				}
				for retry := 0; retry < 3; retry++ {
					prepared, err := prepareFrameContext(client, p, task, f, definitions(f.Role))
					if err != nil {
						t.Fatal(err)
					}
					if _, _, err := client.GeneratePrepared(context.Background(), prepared.request, definitions(f.Role)); err != nil {
						t.Fatal(err)
					}
					f.Messages = append(f.Messages, core.ModelMessage{Role: "assistant", Content: "BAD", InvalidAction: true}, core.ModelMessage{Role: "user", Content: formatFeedbackPrefix + "MISSING_FIELD"})
				}
			}
			client.Config.MaxRequestBytes = 3000
			if _, err := prepareFrameContext(client, p, task, f, definitions(f.Role)); err == nil {
				t.Fatal("oversize required material was silently dropped")
			}
		})
	}
}

func TestFileMaterialReplacementAndLegacyRecovery(t *testing.T) {
	p, task, f := contextFixture()
	content := "ORIGINAL_BODY"
	file := core.File{Hash: core.Hash([]byte(content)), Size: len(content)}
	p.Files["input.txt"] = file
	f.Messages = append(f.Messages, core.ModelMessage{Role: "tool", ToolCallID: "read", Content: core.JSON(map[string]any{"ok": true, "result": map[string]string{"path": "input.txt", "hash": file.Hash, "content": content}})})
	restoreFileMaterials(p, task)
	if f.Materials["file:input.txt"].Content != content {
		t.Fatal("legacy material missing")
	}
	updated := "NEW_BODY"
	p.Files["input.txt"] = core.File{Hash: core.Hash([]byte(updated)), Size: len(updated)}
	rememberFile(p, scope{Task: task.ID, Frame: f.ID}, core.Call{ID: "write"}, "input.txt", updated)
	if material := f.Materials["file:input.txt"]; material.Content != updated || material.Version == file.Hash {
		t.Fatal("stale material after write", material)
	}
}

func TestSchemaFeedbackIncludesAllowedAndRequiredFields(t *testing.T) {
	for _, def := range definitions("Alex") {
		if def.Name == "replace_text" {
			err := validateArgs(map[string]any{"content": "partial", "expected_count": "wrong"}, def.Parameters)
			for _, want := range []string{"未知参数 content", "缺少参数 old_text", "类型无效", "允许字段", "new_text", "必填字段"} {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatal("missing repair feedback", err)
				}
			}
		}
	}
}
