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
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// Optional regression input is a read-only export, never a production database.
func TestHistoricalProgressRepairReplay(t *testing.T) {
	filename := os.Getenv("ATOMS_REPAIR_REPLAY_SNAPSHOT")
	if filename == "" {
		t.Skip("no historical snapshot selected")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Project core.Project `json:"project"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	task := snapshot.Project.Tasks[snapshot.Project.CurrentTask]
	var actual core.ModelMessage
	var body, version string
	for _, frame := range task.Frames {
		for _, message := range frame.Messages {
			if message.Role != "tool" {
				continue
			}
			var result struct {
				OK     bool                                 `json:"ok"`
				Result struct{ Path, Hash, Content string } `json:"result"`
			}
			if json.Unmarshal([]byte(message.Content), &result) == nil && result.OK && result.Result.Path == "scripts/build_pptx.py" && utf8.RuneCountInString(result.Result.Content) == 3123 {
				actual = message
				body = result.Result.Content
				version = result.Result.Hash
			}
		}
	}
	if body == "" || version != core.Hash([]byte(body)) {
		t.Fatal("historical 3123-character evidence not found or hash differs")
	}
	p, testTask, frame := contextFixture()
	p.Files["scripts/build_pptx.py"] = core.File{Hash: version, Size: len(body)}
	frame.Messages = append(frame.Messages, core.ModelMessage{Role: "assistant", ToolCalls: []core.Call{{ID: actual.ToolCallID, Function: core.Function{Name: "read_file", Arguments: `{"path":"scripts/build_pptx.py"}`}}}}, actual)
	restoreFileMaterials(p, testTask)
	for i := 1; i <= 8; i++ {
		contextStep(testTask, frame, i, "list_files", `{"ok":true,"result":{}}`)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		wire, _ := io.ReadAll(r.Body)
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		json.Unmarshal(wire, &request)
		found := false
		for _, m := range request.Messages {
			var materials []core.WorkingMaterial
			if strings.HasPrefix(m.Content, "当前工作材料") {
				start := strings.Index(m.Content, "\n")
				if start >= 0 && json.Unmarshal([]byte(m.Content[start+1:]), &materials) == nil {
					for _, material := range materials {
						found = found || (material.Content == body && material.Version == version)
					}
				}
			}
		}
		if !found {
			t.Error("historical work material absent from actual request")
		}
		writeDialogueReply(w, `{"content":"完成受控回放","calls":[]}`)
	}))
	defer server.Close()
	client := model.New(config.Config{ToolMode: "json", Protocol: "chat_completions", Model: "test", APIKey: "test", BaseURL: server.URL})
	for level := 0; level < 3; level++ {
		if level > 0 {
			frame.Context = summarizeFrame(testTask, frame, 7)
		}
		if level > 1 {
			frame.Dialogue = &core.DialogueSummary{Covered: map[string]int{"user:1": 9999}, Items: []core.DialogueSummaryItem{{Kind: "conclusion", Text: "旧对话结论", Sources: []string{"frame:0"}}}}
		}
		for i := 0; i < 5; i++ {
			frame.Messages = append(frame.Messages, core.ModelMessage{Role: "assistant", Content: `{"calls":[`, InvalidAction: true}, core.ModelMessage{Role: "user", Content: formatFeedbackPrefix + "invalid json"})
			prepared, err := prepareFrameContext(client, p, testTask, frame, definitions(frame.Role))
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := client.GeneratePrepared(context.Background(), prepared.request, definitions(frame.Role)); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Logf("historical evidence hash=%s, captured requests=%d", version, requests)
}
