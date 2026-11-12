package agent

import (
	"context"
	"encoding/json"
	"io"
	"mockatoms/internal/config"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestLongLineByteRangesAndDeletedMaterial(t *testing.T) {
	e, p := testEngine(t, nil)
	s := scope{Owner: "alice", Project: p.ID, Task: "task", Run: "run", Frame: "root", Epoch: 1}
	content := strings.Repeat("长行🙂", 2400)
	hash := core.Hash([]byte(content))
	_, err := e.Store.Mutate(s.Owner, p.ID, map[string][]byte{hash: []byte(content)}, func(p *core.Project) error {
		p.CurrentTask, p.ActiveRun, p.Epoch = s.Task, s.Run, s.Epoch
		p.Files["long.txt"] = core.File{Hash: hash, Size: len(content)}
		p.Runs[s.Run] = &core.Run{ID: s.Run, Status: "running"}
		p.Tasks[s.Task] = &core.Task{ID: s.Task, Root: s.Frame, Mode: "build", Steps: map[string]core.Step{}, Frames: map[string]*core.Frame{s.Frame: {ID: s.Frame, Role: "Alex"}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var recovered strings.Builder
	segments := 0
	for offset := 0; offset < len(content); segments++ {
		args := map[string]any{"path": "long.txt", "offset": float64(offset), "limit": float64(15999)}
		result, err := e.execute(t.Context(), s, call("read_file", args).ToolCalls[0], args)
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			OK     bool `json:"ok"`
			Result struct {
				Content string `json:"content"`
				Next    int    `json:"next_offset"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(result), &response) != nil || !response.OK || response.Result.Next <= offset || !utf8.ValidString(response.Result.Content) {
			t.Fatal("invalid byte read", result)
		}
		recovered.WriteString(response.Result.Content)
		offset = response.Result.Next
	}
	if recovered.String() != content {
		t.Fatal("byte ranges lost source content")
	}
	p, _ = e.Store.Get(s.Owner, s.Project)
	if len(p.Tasks[s.Task].Frames[s.Frame].Materials) != segments {
		t.Fatal("reading a later segment discarded an earlier dependency")
	}
	args := map[string]any{"path": "long.txt", "old_hash": hash}
	c := call("delete_file", args).ToolCalls[0]
	if _, err := e.execute(t.Context(), s, c, args); err != nil {
		t.Fatal(err)
	}
	p, _ = e.Store.Get(s.Owner, s.Project)
	if len(p.Tasks[s.Task].Frames[s.Frame].Materials) != 0 || core.JSON(p.Tasks[s.Task].Steps[s.Frame+"/"+c.ID].Fact.Deleted) != `["long.txt"]` {
		t.Fatal("deleted resource remained current or lacked a deletion fact")
	}
	if _, _, err := fileBytes(content, 1, 100); err == nil {
		t.Fatal("accepted a byte offset inside a character")
	}
	if material := fileMaterial("long.txt", core.File{Hash: hash}, content, "write"); material.Content == "" || !strings.Contains(material.Recovery, "offset=") {
		t.Fatal("long single-line write lost recoverable current material")
	}
}
