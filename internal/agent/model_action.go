package agent

import (
	"errors"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
)

func (e *Engine) beginModelCall(s scope, role string, prepared preparedContext) error {
	// Keep a stale request from reserving a daily call between acceptance and admission.
	e.mu.Lock()
	defer e.mu.Unlock()
	p, err := e.Store.Get(s.Owner, s.Project)
	if err != nil {
		return err
	}
	if err := s.checkInstruction(p); err != nil {
		return err
	}
	if err := e.Store.ReserveCall(s.Owner, e.Config.DailyCalls, e.Config.WorkspaceDailyCalls); err != nil {
		return err
	}
	_, err = e.Store.Mutate(s.Owner, s.Project, nil, func(p *core.Project) error {
		if err := s.checkInstruction(p); err != nil {
			return err
		}
		if prepared.replacement != nil {
			p.Tasks[s.Task].Frames[s.Frame].Context = prepared.replacement
			afterBytes := prepared.request.ByteSize()
			if prepared.dialogue != nil {
				afterBytes = prepared.dialogue.previous
			}
			p.Event("context_compacted", role, "已压缩旧执行记录，继续处理", map[string]int{
				"before_bytes": prepared.beforeBytes, "after_bytes": afterBytes, "level": 1,
			})
		}
		p.Runs[s.Run].Calls++
		p.Runs[s.Run].Usage.Known = false
		if prepared.dialogue != nil {
			p.Event("model_call", role, "正在生成旧对话摘要", map[string]any{"purpose": "dialogue_summary", "request_bytes": prepared.request.ByteSize()})
		} else {
			p.Event("model_call", role, "正在处理", nil)
		}
		return nil
	})
	return err
}

type modelReply struct {
	discarded    bool
	formatRepair bool
	failure      error
}

func (e *Engine) saveModelReply(s scope, message core.ModelMessage, usage core.Usage, failure error, prepared preparedContext, allowRepair bool) (modelReply, error) {
	var dialogue *core.DialogueSummary
	var dialogueAfter int
	if failure == nil && prepared.dialogue != nil {
		dialogue, dialogueAfter, failure = prepared.dialogue.accept(message.Content)
	}
	reply := modelReply{failure: failure}
	_, err := e.Store.Mutate(s.Owner, s.Project, nil, func(p *core.Project) error {
		fresh := s.checkInstruction(p)
		if fresh != nil && !errors.Is(fresh, errNewInstruction) {
			return fresh
		}
		r := p.Runs[s.Run]
		addUsage(r, usage)
		task := p.Tasks[s.Task]
		frame := task.Frames[s.Frame]
		if fresh != nil {
			reply.discarded = true
			p.Event("action_skipped", frame.Role, "已跳过收到新指令前生成的回复", nil)
			return nil
		}
		if failure != nil {
			var formatError *model.ActionFormatError
			if allowRepair && errors.As(failure, &formatError) {
				reply.formatRepair = true
				r.FormatRepairs++
				frame.Messages = append(frame.Messages, message, core.ModelMessage{Role: "user", Content: formatFeedbackPrefix + formatError.Error() + "。请重新生成当前动作。只输出一个完整 JSON 对象，最后一个右花括号后立即结束，不追加说明、代码围栏或第二个对象。content 用字符串，无说明时填空字符串。calls 用数组，有动作时只放一个，无动作时填 []。"})
				p.Event("format_repair", frame.Role, "模型动作格式无效，未执行；正在修正格式", nil)
			}
			return nil
		}
		if dialogue != nil {
			frame.Dialogue = dialogue
			p.Event("context_compacted", frame.Role, "已保存旧对话摘要", map[string]int{
				"before_bytes": prepared.dialogue.previous, "after_bytes": dialogueAfter, "level": 2,
			})
			return nil
		}
		message.InstructionVersion = s.InstructionVersion
		frame.Messages = append(frame.Messages, message)
		if message.Content != "" {
			p.Message(frame.Role, message.Content, "agent")
		}
		if len(message.ToolCalls) == 0 {
			if s.Frame != task.Root {
				frame.Complete = true
				frame.Result = message.Content
				p.Event("handoff", frame.Role, "已返回工作结果", nil)
			} else {
				frame.Messages = append(frame.Messages, core.ModelMessage{Role: "user", Content: nextActionPrompt})
			}
		}
		return nil
	})
	return reply, err
}
