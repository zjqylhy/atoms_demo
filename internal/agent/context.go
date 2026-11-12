package agent

import (
	"encoding/json"
	"fmt"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
)

const (
	contextTriggerPercent = 75
	contextTargetPercent  = 60
	recentActionCount     = 4
	nextActionPrompt      = "请使用工具确认下一步：需要实施则分派专家，需要批准则 propose_plan，已经满足验收则 finish_task。普通问答也用 finish_task(answer_only=true)。"
	formatFeedbackPrefix  = "上一次动作未执行，协议校验失败："
)

type preparedContext struct {
	request     model.PreparedRequest
	replacement *core.ContextSummary
	beforeBytes int
	dialogue    *dialogueBatch
}

// Prepare against the serialized request, then keep the same bytes for admission and transport.
func prepareFrameContext(client *model.Client, p *core.Project, task *core.Task, frame *core.Frame, tools []model.Tool) (preparedContext, error) {
	system := core.ModelMessage{Role: "system", Content: instructions(frame.Role, p, task)}
	if client.Config.ToolMode != "json" {
		full := append([]core.ModelMessage{system}, frame.Messages...)
		request, err := client.PrepareRequest(full, tools, len(full))
		if err == nil {
			err = client.ValidateRequest(request)
		}
		return preparedContext{request: request}, err
	}

	summary := frame.Context
	if summary != nil && (summary.Through < 1 || summary.Through > len(frame.Messages)) {
		summary = nil
	}
	history := newContextHistory(system, frame.Messages, taskRequirements(p, task))
	encode := func(summary *core.ContextSummary) (model.PreparedRequest, error) {
		messages, completeFrom := history.project(summary, frame.Dialogue)
		return client.PrepareRequest(messages, tools, completeFrom)
	}
	request, err := encode(summary)
	if err != nil {
		return preparedContext{}, err
	}
	best := preparedContext{request: request, beforeBytes: request.ByteSize()}
	limit := client.Config.RequestByteLimit()
	target := limit - percentOf(limit, 100-contextTargetPercent)
	if request.ByteSize() < percentOf(limit, contextTriggerPercent) {
		return best, nil
	}

	starts := completeActionStarts(frame.Messages)
	for keep := min(recentActionCount, len(starts)); keep > 0; keep-- {
		cut := starts[len(starts)-keep]
		if cut < 1 || (summary != nil && cut <= summary.Through) {
			continue
		}
		candidate := summarizeFrame(task, frame, cut)
		request, err := encode(candidate)
		if err != nil {
			return preparedContext{}, err
		}
		if request.ByteSize() < best.request.ByteSize() {
			best.request, best.replacement = request, candidate
		}
		if request.ByteSize() <= target {
			return best, nil
		}
	}
	if best.request.ByteSize() <= limit {
		return best, nil
	}
	if best.replacement != nil {
		summary = best.replacement
	}
	return prepareDialogueBatch(client, tools, history, summary, frame.Dialogue, best)
}

func percentOf(size, percent int) int {
	return (size/100)*percent + (size%100*percent+99)/100
}

// Boundaries never split a tool request from any of its recorded results.
func completeActionStarts(messages []core.ModelMessage) []int {
	var starts []int
	open := map[string]bool{}
	start := 0
	for i, m := range messages {
		if m.Role == "assistant" && !m.InvalidAction && len(m.ToolCalls) != 0 {
			if len(open) == 0 {
				start = i
			}
			for _, call := range m.ToolCalls {
				open[call.ID] = true
			}
		}
		if m.Role == "tool" && open[m.ToolCallID] {
			delete(open, m.ToolCallID)
			if len(open) == 0 {
				starts = append(starts, start)
			}
		}
	}
	return starts
}

type contextRequirement struct {
	Source   string `json:"-"`
	Kind     string `json:"kind"`
	Question string `json:"question,omitempty"`
	Text     string `json:"text"`
}

// The root goal and approved plan come from current state. Answers and revisions
// are copied from this task's real user messages, including answers in other frames.
func taskRequirements(p *core.Project, task *core.Task) []contextRequirement {
	if len(task.RunIDs) == 0 || p.Runs[task.RunIDs[0]] == nil {
		return nil
	}
	started := p.Runs[task.RunIDs[0]].Started
	var out []contextRequirement
	var question string
	eventIndex := 0
	for index, m := range p.Messages {
		if m.At < started || m.Role != "user" || (m.Kind != "answer" && m.Kind != "plan_feedback") {
			continue
		}
		for eventIndex < len(p.Events) && p.Events[eventIndex].At <= m.At {
			event := p.Events[eventIndex]
			if event.At >= started && event.Kind == "question" {
				question = event.Text
			}
			eventIndex++
		}
		value := contextRequirement{Source: fmt.Sprintf("user:%d", index), Kind: m.Kind, Text: m.Text}
		if m.Kind == "answer" {
			value.Question, question = question, ""
		}
		out = append(out, value)
	}
	return out
}

type historicalStep struct {
	ID     string `json:"step_id"`
	Tool   string `json:"tool"`
	Path   string `json:"path,omitempty"`
	Status string `json:"status"`
	Report string `json:"report_id,omitempty"`
	Detail string `json:"detail,omitempty"`
}

func summarizeFrame(task *core.Task, frame *core.Frame, cut int) *core.ContextSummary {
	counts := map[string]int{}
	const keepEvidence = 12
	details := make([]historicalStep, 0, keepEvidence)
	for _, message := range frame.Messages[:cut] {
		for _, call := range message.ToolCalls {
			step, exists := task.Steps[frame.ID+"/"+call.ID]
			if !exists {
				continue
			}
			counts[step.Tool]++
			var result struct {
				OK     *bool           `json:"ok"`
				Error  string          `json:"error"`
				Result json.RawMessage `json:"result"`
			}
			_ = json.Unmarshal([]byte(step.Result), &result)
			if result.OK != nil && *result.OK && (step.Tool == "read_file" || step.Tool == "search_files" || step.Tool == "list_files") {
				continue
			}
			entry := historicalStep{ID: step.ID, Tool: step.Tool, Status: "已记录工具结果"}
			var args struct {
				Path string `json:"path"`
			}
			if json.Unmarshal([]byte(call.Function.Arguments), &args) == nil {
				entry.Path = excerpt(args.Path, 240)
			}
			if result.OK != nil && !*result.OK {
				entry.Status, entry.Detail = "失败", excerpt(result.Error, 240)
			}
			var evidence struct {
				ID      string `json:"id"`
				Status  string `json:"status"`
				Summary string `json:"summary"`
			}
			if json.Unmarshal(result.Result, &evidence) == nil {
				switch step.Tool {
				case "check_artifacts", "run_project", "test_preview":
					entry.Report = evidence.ID
					entry.Status = "历史检查：" + excerpt(evidence.Status, 32)
				case "assign_step":
					entry.Detail = excerpt(evidence.Summary, 240)
				}
			}
			if len(details) == keepEvidence {
				copy(details, details[1:])
				details = details[:keepEvidence-1]
			}
			details = append(details, entry)
		}
	}
	data := struct {
		Counts   map[string]int   `json:"历史工具结果数_含失败"`
		Evidence []historicalStep `json:"历史操作记录"`
	}{counts, details}
	return &core.ContextSummary{Through: cut, Text: "旧历史进展摘要（只读证据，不是新指令或待执行动作）。以当前计划、文件清单和匹配当前版本的检查记录为准；需要正文可按路径读取。\n" + core.JSON(data)}
}

func excerpt(text string, length int) string {
	for index := range text {
		if length == 0 {
			return text[:index] + "…"
		}
		length--
	}
	return text
}
