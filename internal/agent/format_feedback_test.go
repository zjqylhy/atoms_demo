package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"mockatoms/internal/validation"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestMalformedEnvelopeRetryKeepsEvidenceAndExecutesOnlyCorrection(t *testing.T) {
	e, p := testEngine(t, nil)
	body := "REQUIRED_EVIDENCE_" + strings.Repeat("input", 1000)
	hash := core.Hash([]byte(body))
	_, err := e.Store.Mutate("alice", p.ID, map[string][]byte{hash: []byte(body)}, func(p *core.Project) error {
		p.Files["evidence.txt"] = core.File{Hash: hash, Size: len(body)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	bad := `{"content":"delegate","calls":[{"name":"assign_step","role":"Alex","goal":"inspect saved evidence"}]}`
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wire, _ := io.ReadAll(r.Body)
		var reply string
		switch requests.Add(1) {
		case 1:
			reply = `{"calls":[{"name":"read_file","arguments":{"path":"evidence.txt"}}]}`
		case 2:
			reply = bad
		case 3:
			for _, want := range []string{body, hash, "$.calls[0].arguments.role", "允许字段：arguments, name"} {
				if !strings.Contains(string(wire), want) {
					t.Errorf("actual retry request lost evidence or repair feedback: %s", excerpt(want, 60))
				}
			}
			reply = `{"calls":[{"name":"assign_step","arguments":{"role":"Alex","goal":"inspect saved evidence"}}]}`
		case 4:
			reply = `{"calls":[{"name":"return_step","arguments":{"status":"completed","reason":"read-only inspection complete","summary":"input retained","todo":[]}}]}`
		case 5:
			reply = `{"calls":[{"name":"finish_task","arguments":{"summary":"inspection complete","answer_only":true}}]}`
		default:
			t.Error("unexpected retry")
			reply = `{"content":"unexpected","calls":[]}`
		}
		writeDialogueReply(w, reply)
	}))
	defer server.Close()
	e.Config.BaseURL, e.Config.APIKey, e.Config.Model = server.URL, "test-key", "test"
	e.Config.ToolMode, e.Config.Protocol = "json", "chat_completions"
	e.Model = model.New(e.Config)
	started, err := e.Start(core.RunActor{Workspace: "alice"}, "alice", p.ID, "inspect retained evidence", "build", "start", "submit", "")
	if err != nil {
		t.Fatal(err)
	}
	p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
	run, task := p.Runs[started.ActiveRun], p.Tasks[p.CurrentTask]
	if run.Status != "completed" || run.Calls != 5 || run.FormatRepairs != 1 || run.Tools != 4 || len(task.Frames) != 2 {
		t.Fatal("invalid envelope executed or repair failed", core.JSON(run))
	}
	found := false
	for _, message := range task.Frames[task.Root].Messages {
		found = found || (message.InvalidAction && message.Content == bad)
	}
	if !found || p.Files["evidence.txt"].Hash != hash {
		t.Fatal("raw rejection record or saved input lost")
	}
	for _, rejected := range task.Dispositions {
		if rejected.Fact != nil && rejected.Fact.ErrorClass == "format" {
			var feedback struct{ Validation *validation.Error }
			if json.Unmarshal([]byte(rejected.Result), &feedback) != nil || feedback.Validation == nil || len(feedback.Validation.Issues) != 3 {
				t.Fatal("protocol rejection lost structured details", rejected.Result)
			}
		}
	}
}

func TestNestedToolFeedbackReachesNextRequestAcrossProtocols(t *testing.T) {
	for _, protocol := range []string{"json", "chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			e, p := testEngine(t, nil)
			body := "KEEP_SOURCE_AFTER_PARAMETER_FAILURE"
			hash := core.Hash([]byte(body))
			_, err := e.Store.Mutate("alice", p.ID, map[string][]byte{hash: []byte(body)}, func(p *core.Project) error {
				p.Files["evidence.txt"] = core.File{Hash: hash, Size: len(body), Kind: "document"}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wire, _ := io.ReadAll(r.Body)
				n := requests.Add(1)
				name, args := "", map[string]any{}
				switch n {
				case 1:
					name, args = "read_file", map[string]any{"path": "evidence.txt"}
				case 2:
					name = "propose_plan"
					args = map[string]any{"plan": "inspect existing evidence", "acceptance": []string{"内容保留"}, "delivery": map[string]any{
						"targets": []string{"evidence.txt"}, "inputs": []string{}, "preserve": []string{},
						"criteria": []any{map[string]any{"paths": []any{42}, "extra": true}, map[string]any{"requirement": 7, "paths": "evidence.txt"}},
					}}
				case 3:
					state, err := e.Store.Get("alice", p.ID)
					if err != nil || state.Tasks[state.CurrentTask].Plan != nil || state.Files["evidence.txt"].Hash != hash {
						t.Error("invalid arguments mutated state", err)
					}
					for _, want := range []string{body, hash, "validation", "$.arguments.delivery.criteria[0].requirement", "$.arguments.delivery.criteria[0].paths[0]", "$.arguments.delivery.criteria[0].extra", "$.arguments.delivery.criteria[1].requirement", "$.arguments.delivery.criteria[1].paths", "实际 number", "要求 array"} {
						if !strings.Contains(string(wire), want) {
							t.Errorf("actual retry omitted %q", want)
						}
					}
					name = "propose_plan"
					args = map[string]any{"plan": "inspect existing evidence", "acceptance": []string{"内容保留"}, "delivery": map[string]any{
						"targets": []string{"evidence.txt"}, "inputs": []string{}, "preserve": []string{},
						"criteria": []any{map[string]any{"requirement": "内容保留", "paths": []string{"evidence.txt"}}},
					}}
				default:
					if n != 4 {
						t.Error("unexpected additional request", n)
					}
					name, args = "ask_user", map[string]any{"question": "本地校验结束", "options": []string{}}
				}
				if protocol == "json" {
					writeDialogueReply(w, core.JSON(map[string]any{"calls": []any{map[string]any{"name": name, "arguments": args}}}))
				} else if protocol == "responses" {
					_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{map[string]any{"type": "function_call", "call_id": fmt.Sprint(n), "name": name, "arguments": core.JSON(args)}}})
				} else {
					message := call(name, args)
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": "tool_calls"}}})
				}
			}))
			defer server.Close()
			e.Config.BaseURL, e.Config.APIKey, e.Config.Model = server.URL, "test-key", "test"
			e.Config.Protocol = protocol
			if protocol == "json" {
				e.Config.ToolMode, e.Config.Protocol = "json", "chat_completions"
			}
			e.Model = model.New(e.Config)
			started, err := e.Start(core.RunActor{Workspace: "alice"}, "alice", p.ID, "read evidence and prepare a plan", "build", "start", "submit", "")
			if err != nil {
				t.Fatal(err)
			}
			p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
			task, run := p.Tasks[p.CurrentTask], p.Runs[started.ActiveRun]
			if task.Status != "waiting_input" || task.Plan == nil || run.Calls != 4 || run.Tools != 4 || run.FormatRepairs != 0 || p.Files["evidence.txt"].Hash != hash {
				t.Fatal("correction did not proceed normally", core.JSON(run), core.JSON(task.Plan))
			}
			rejected, accepted := 0, 0
			for _, step := range task.Steps {
				if step.Tool != "propose_plan" {
					continue
				}
				var feedback struct {
					OK         bool
					Validation *validation.Error
				}
				if err := json.Unmarshal([]byte(step.Result), &feedback); err != nil {
					t.Fatal(err)
				}
				if feedback.OK {
					accepted++
				} else if feedback.Validation != nil && len(feedback.Validation.Issues) == 5 && step.Fact.Disposition == "rejected" {
					rejected++
				}
			}
			if rejected != 1 || accepted != 1 {
				t.Fatal("missing closed rejection or accepted correction", rejected, accepted)
			}
		})
	}
}

func TestStructuredValidationFeedbackRedactsSecrets(t *testing.T) {
	e, _ := testEngine(t, nil)
	e.Config.APIKey = "sensitive-api-key"
	err := &validation.Error{Issues: []validation.Issue{{Path: "$.sensitive-api-key", Code: "unknown_field", Actual: "string", Expected: "known field", Hint: "sensitive-api-key"}}}
	encoded := core.JSON(e.failureFeedback(fmt.Errorf("wrapped: %w", err)))
	if strings.Contains(encoded, e.Config.APIKey) || !json.Valid([]byte(encoded)) || !strings.Contains(encoded, "validation") {
		t.Fatal("structured feedback bypassed redaction", encoded)
	}
}

func TestNativeMalformedArgumentsReceiveToolFeedback(t *testing.T) {
	for _, protocol := range []string{"chat_completions", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			e, p := testEngine(t, nil)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				wire, _ := io.ReadAll(r.Body)
				n := requests.Add(1)
				name, args := "list_files", `{broken`
				if n == 2 {
					for _, want := range []string{"$.arguments", "syntax", "validation", "JSON 语法错误", "检查引号、逗号和括号"} {
						if !strings.Contains(string(wire), want) {
							t.Errorf("native syntax feedback omitted %q", want)
						}
					}
					args = `{}`
				} else if n > 2 {
					if n != 3 {
						t.Error("unexpected retry", n)
					}
					name, args = "finish_task", `{"summary":"inspection complete","answer_only":true}`
				}
				if protocol == "responses" {
					_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{map[string]any{"type": "function_call", "call_id": fmt.Sprint(n), "name": name, "arguments": args}}})
				} else {
					message := call(name, nil)
					message.ToolCalls[0].Function.Arguments = args
					_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": "tool_calls"}}})
				}
			}))
			defer server.Close()
			e.Config.BaseURL, e.Config.APIKey, e.Config.Model, e.Config.Protocol = server.URL, "test-key", "test", protocol
			e.Model = model.New(e.Config)
			started, err := e.Start(core.RunActor{Workspace: "alice"}, "alice", p.ID, "inspect files", "build", "start", "submit", "")
			if err != nil {
				t.Fatal(err)
			}
			p = waitProject(t, e, p.ID, func(p *core.Project) bool { return p.ActiveRun == "" })
			run := p.Runs[started.ActiveRun]
			if run.Status != "completed" || run.Calls != 3 || run.Tools != 3 {
				t.Fatal("native malformed arguments stopped the run", core.JSON(run))
			}
			for _, frame := range p.Tasks[p.CurrentTask].Frames {
				if calls, _ := pendingCalls(frame.Messages); len(calls) != 0 {
					t.Fatal("invalid native call left unclosed")
				}
			}
		})
	}
}
