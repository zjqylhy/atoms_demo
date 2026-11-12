package agent

import (
	"errors"
	"mockatoms/internal/core"
	"strings"
)

const instructionKind = "steering"

var errNewInstruction = errors.New("收到新的用户指令")

func (e *Engine) Steer(owner, projectID, runID, requestID, text string) (*core.Project, error) {
	text = strings.TrimSpace(text)
	if requestID == "" || len(requestID) > 100 || runID == "" {
		return nil, errors.New("缺少有效的请求或运行标识")
	}
	if text == "" || len(text) > 32000 {
		return nil, errors.New("请输入 32000 字节以内的指令")
	}
	receipt := "steer:" + core.Hash([]byte(runID+"\n"+text))
	// Serialize instruction acceptance with model admission and Stop.
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.Store.Mutate(owner, projectID, nil, func(p *core.Project) error {
		if saved, exists := p.Requests[requestID]; exists {
			if saved == receipt {
				return nil
			}
			return errors.New("请求标识已用于其他操作")
		}
		task := p.Tasks[p.CurrentTask]
		run := p.Runs[runID]
		if task == nil || run == nil || run.TaskID != task.ID || task.Status != "running" || p.Fence(runID, run.Epoch) != nil {
			return errors.New("原运行已结束或正在等待确认，指令未发送")
		}
		messageID := p.Message("user", text, instructionKind)
		task.InstructionMessages = append(task.InstructionMessages, messageID)
		p.Requests[requestID] = receipt
		return nil
	})
}

func (s scope) checkInstruction(p *core.Project) error {
	if err := p.Fence(s.Run, s.Epoch); err != nil {
		return err
	}
	if s.InstructionVersion != len(p.Tasks[s.Task].InstructionMessages) {
		return errNewInstruction
	}
	return nil
}

// Close the provider's tool-call sequence without recording skipped calls as executed steps.
func (e *Engine) skipPendingTools(s scope, calls []core.Call) error {
	_, err := e.Store.Mutate(s.Owner, s.Project, nil, func(p *core.Project) error {
		if err := p.Fence(s.Run, s.Epoch); err != nil {
			return err
		}
		task := p.Tasks[s.Task]
		frame := task.Frames[s.Frame]
		skipped := false
		for _, call := range calls {
			content := `{"ok":false,"skipped":true,"error":"收到新指令，此工具未启动"}`
			if child := task.Frames[s.Frame+"/"+call.ID]; call.Function.Name == "assign_step" && child != nil {
				content = core.JSON(map[string]any{"ok": true, "result": redirectedDelegation(p, child.Role)})
			} else {
				skipped = true
			}
			frame.Messages = append(frame.Messages, core.ModelMessage{
				Role: "tool", ToolCallID: call.ID, Content: content,
			})
			if action := task.RunnerAction; action != nil && action.FrameID == s.Frame && action.CallID == call.ID {
				task.RunnerAction = nil
			}
		}
		if skipped {
			p.Event("action_skipped", frame.Role, "已跳过尚未启动的旧动作", nil)
		}
		return nil
	})
	return err
}

func redirectedDelegation(p *core.Project, role string) map[string]any {
	p.Event("handoff", role, "已交回主 Agent 处理新指令", nil)
	return map[string]any{
		"role": role, "status": "redirected", "files": p.Files,
		"summary": "收到用户新指令，已结束本次分派，已保存的工具结果和文件可继续使用。",
	}
}
