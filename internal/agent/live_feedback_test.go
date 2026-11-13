package agent

import (
	"context"
	"encoding/json"
	"mockatoms/internal/config"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"mockatoms/internal/store"
	"mockatoms/internal/validation"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Explicit opt-in: two real correction requests, charged to the original quota
// ledger. Returned calls are inspected only; no project or tool is executed.
func TestLiveValidationFeedback(t *testing.T) {
	database, owner, directory := os.Getenv("ATOMS_FEEDBACK_QUOTA_DB"), os.Getenv("ATOMS_FEEDBACK_OWNER"), os.Getenv("ATOMS_FEEDBACK_OUTPUT")
	if database == "" && owner == "" && directory == "" {
		t.Skip("requires explicit live feedback acceptance configuration")
	}
	if database == "" || owner == "" || directory == "" {
		t.Fatal("all feedback acceptance settings are required")
	}
	cfg := config.Load()
	if err := cfg.ModelError(); err != nil || cfg.ToolMode != "json" {
		t.Fatal("requires the application's configured JSON model", err)
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal("acceptance output must be a new directory", err)
	}
	quota, err := store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer quota.Close()
	cfg.MaxOutputTokens = min(cfg.MaxOutputTokens, 2048)
	client := model.New(cfg)
	for _, scenario := range []struct{ name, role, tool, task, invalid string }{
		{"envelope", "Mike", "assign_step", "将只读核对 evidence.txt 的工作委派给 Alex。子目标为核对已保存证据，不要改写文件。本轮只修正当前调用，不生成后续动作。", `{"content":"delegate","calls":[{"name":"assign_step","role":"Alex","goal":"核对 evidence.txt 的已保存证据，不改写文件"}]}`},
		{"parameters", "Alex", "run_project", "当前意图：运行 Python 脚本 scripts/check.py，标准输出应为 done；回收新二进制文件 result.bin（old_hash 为空字符串）。只修正此调用，不添加其他动作。", `{"kind":"python","entry":123,"outputs":[{"path":"result.bin","format":"bytes"}]}`},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			tools := definitions(scenario.role)
			var selected model.Tool
			for _, tool := range tools {
				if tool.Name == scenario.tool {
					selected = tool
				}
			}
			var failure error
			if scenario.name == "envelope" {
				// Generate feedback through the real protocol adapter, using a fixed
				// invalid reply locally before making the single upstream request.
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { writeDialogueReply(w, scenario.invalid) }))
				mock := cfg
				mock.BaseURL, mock.APIKey = server.URL, "fixture-key"
				_, _, failure = model.New(mock).Generate(context.Background(), []core.ModelMessage{{Role: "system", Content: "验收夹具"}}, tools)
				server.Close()
			} else {
				value, err := validation.Decode(scenario.invalid, "$.arguments")
				if err != nil {
					t.Fatal(err)
				}
				failure = validation.Validate(value, selected.Parameters, "$.arguments")
			}
			if failure == nil {
				t.Fatal("invalid fixture unexpectedly accepted")
			}
			feedback := (&Engine{Config: cfg}).failureFeedback(failure)
			messages := []core.ModelMessage{
				{Role: "system", Content: "这是调用纠错验收。根据任务依据和校验反馈生成一个合法调用，保持原目标；不要声称工具已经执行。"},
				{Role: "user", Content: scenario.task + "\n已保存材料：evidence.txt，版本 H1，正文 VERIFIED_SOURCE。"},
				{Role: "user", Content: formatFeedbackPrefix + core.JSON(feedback)},
			}
			prepared, err := client.PrepareRequest(messages, tools, len(messages))
			if err != nil {
				t.Fatal(err)
			}
			if err := client.ValidateRequest(prepared); err != nil {
				t.Fatal(err)
			}
			wait, err := quota.ReserveModelWindow(prepared.ReservationTokens(), cfg.ProviderTPM)
			if err != nil || wait > 0 {
				t.Fatal("minute quota unavailable; no upstream request sent", wait, err)
			}
			if err := quota.ReserveCall(owner, cfg.DailyCalls, cfg.WorkspaceDailyCalls); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
			defer cancel()
			message, usage, failure := client.GeneratePrepared(ctx, prepared, tools)
			if failure == nil {
				if len(message.ToolCalls) != 1 || message.ToolCalls[0].Function.Name != scenario.tool {
					failure = &validation.Error{Issues: []validation.Issue{{Path: "$.calls", Code: "intent", Actual: "目标调用不一致", Expected: scenario.tool, Hint: "保留原目标"}}}
				} else {
					var args any
					if failure = json.Unmarshal([]byte(message.ToolCalls[0].Function.Arguments), &args); failure == nil {
						failure = validation.Validate(args, selected.Parameters, "$.arguments")
					}
				}
			}
			receipt := map[string]any{"scenario": scenario.name, "model": cfg.Ref(), "requests": 1, "request_bytes": prepared.ByteSize(), "usage": usage, "messages": messages, "reply": message, "passed": failure == nil, "executed": false}
			if failure != nil {
				receipt["error"] = cfg.Redact(failure.Error())
			}
			if err := os.WriteFile(filepath.Join(directory, scenario.name+".json"), []byte(core.JSON(receipt)), 0600); err != nil {
				t.Fatal(err)
			}
			t.Logf("requests=1 known_usage=%t tokens=%d passed=%t executed=false", usage.Known, usage.Total, failure == nil)
			if failure != nil {
				t.Error(cfg.Redact(failure.Error()))
			}
		})
	}
}
