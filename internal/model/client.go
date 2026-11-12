package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mockatoms/internal/config"
	"mockatoms/internal/core"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}
type Generator interface {
	Generate(context.Context, []core.ModelMessage, []Tool) (core.ModelMessage, core.Usage, error)
}
type Client struct {
	Config config.Config
	HTTP   *http.Client
}

// TPMThrottleError identifies a gateway response for the model's minute token quota.
type TPMThrottleError struct {
	RetryAfter time.Duration
}

func (e *TPMThrottleError) Error() string {
	return "模型网关每分钟 Token 额度不足（HTTP 429，model_tpm）"
}

func New(c config.Config) *Client {
	return &Client{Config: c, HTTP: &http.Client{Timeout: 120 * time.Second, Transport: &http.Transport{Proxy: nil, MaxIdleConns: 8, IdleConnTimeout: 60 * time.Second}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("模型服务重定向被拒绝") }}}
}

// PreparedRequest keeps admission and transport on the same serialized payload.
type PreparedRequest struct {
	route     string
	body      []byte
	maxOutput int
}

func (r PreparedRequest) ByteSize() int          { return len(r.body) }
func (r PreparedRequest) ReservationTokens() int { return r.ByteSize() + r.maxOutput }

// PrepareRequest measures a candidate before the engine decides whether to compact it.
func (c *Client) PrepareRequest(messages []core.ModelMessage, tools []Tool, completeFrom int) (PreparedRequest, error) {
	var payload any
	route := "/chat/completions"
	if c.Config.ToolMode == "json" {
		payload = c.jsonPayload(messages, tools, completeFrom)
	} else if c.Config.Protocol == "responses" {
		route = "/responses"
		input := []any{}
		defs := []any{}
		for _, m := range messages {
			if m.Role == "tool" {
				input = append(input, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.Content})
				continue
			}
			if len(m.ProtocolItems) > 0 {
				for _, raw := range m.ProtocolItems {
					input = append(input, raw)
				}
				continue
			}
			if m.Content != "" {
				input = append(input, map[string]any{"role": m.Role, "content": m.Content})
			}
			for _, call := range m.ToolCalls {
				input = append(input, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
			}
		}
		for _, t := range tools {
			defs = append(defs, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.Parameters, "strict": false})
		}
		payload = map[string]any{"model": c.Config.Model, "input": input, "tools": defs, "store": false, "parallel_tool_calls": false, "max_output_tokens": c.outputLimit()}
	} else {
		input := []any{}
		defs := []any{}
		for _, m := range messages {
			v := map[string]any{"role": m.Role, "content": m.Content}
			if len(m.ToolCalls) > 0 {
				v["tool_calls"] = m.ToolCalls
			}
			if m.ToolCallID != "" {
				v["tool_call_id"] = m.ToolCallID
			}
			input = append(input, v)
		}
		for _, t := range tools {
			defs = append(defs, map[string]any{"type": "function", "function": t})
		}
		payload = map[string]any{"model": c.Config.Model, "messages": input, "tools": defs, "stream": false, "max_tokens": c.outputLimit()}
	}
	b, e := json.Marshal(payload)
	if e != nil {
		return PreparedRequest{}, e
	}
	return PreparedRequest{route: route, body: b, maxOutput: c.outputLimit()}, nil
}

func (c *Client) ValidateRequest(request PreparedRequest) error {
	if limit := c.Config.RequestByteLimit(); request.ByteSize() > limit {
		return fmt.Errorf("当前模型上下文过大：请求 %d 字节，超过 %d 字节上限，请缩小任务范围", request.ByteSize(), limit)
	}
	return nil
}

func (c *Client) Generate(ctx context.Context, messages []core.ModelMessage, tools []Tool) (core.ModelMessage, core.Usage, error) {
	request, err := c.PrepareRequest(messages, tools, len(messages))
	if err != nil {
		return core.ModelMessage{}, core.Usage{}, err
	}
	return c.GeneratePrepared(ctx, request, tools)
}

func (c *Client) GeneratePrepared(ctx context.Context, request PreparedRequest, tools []Tool) (core.ModelMessage, core.Usage, error) {
	if e := c.Config.ModelError(); e != nil {
		return core.ModelMessage{}, core.Usage{}, e
	}
	if e := c.ValidateRequest(request); e != nil {
		return core.ModelMessage{}, core.Usage{}, e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", c.Config.BaseURL+request.route, bytes.NewReader(request.body))
	if e != nil {
		return core.ModelMessage{}, core.Usage{}, e
	}
	req.Header.Set("Authorization", "Bearer "+c.Config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return core.ModelMessage{}, core.Usage{}, fmt.Errorf("模型请求失败：%s", c.Config.Redact(e.Error()))
	}
	defer resp.Body.Close()
	body, e := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if e != nil {
		return core.ModelMessage{}, core.Usage{}, e
	}
	if len(body) > 2*1024*1024 {
		return core.ModelMessage{}, core.Usage{}, errors.New("模型响应超过安全大小限制")
	}
	// Reject reflected credentials before any response is persisted or exposed.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && c.Config.APIKey != "" && bytes.Contains(body, []byte(c.Config.APIKey)) {
		return core.ModelMessage{}, core.Usage{}, errors.New("模型响应包含敏感配置，已拒绝保存")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var er struct {
			Error struct {
				Message string `json:"message"`
				Code    string `json:"code"`
				Reason  string `json:"reason"`
			} `json:"error"`
		}
		parseErr := json.Unmarshal(body, &er)
		if parseErr == nil && resp.StatusCode == http.StatusTooManyRequests &&
			er.Error.Code == "insufficient_quota" && er.Error.Reason == "model_tpm" {
			return core.ModelMessage{}, core.Usage{}, &TPMThrottleError{RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"))}
		}
		detail := er.Error.Message
		if len(detail) > 600 {
			detail = detail[:600]
		}
		if detail == "" {
			detail = "上游未返回可识别错误"
		}
		return core.ModelMessage{}, core.Usage{}, fmt.Errorf("模型服务 HTTP %d：%s", resp.StatusCode, c.Config.Redact(detail))
	}
	var wire struct {
		Choices []struct {
			Message core.ModelMessage `json:"message"`
			Finish  string            `json:"finish_reason"`
		} `json:"choices"`
		Output []json.RawMessage `json:"output"`
		Status string            `json:"status"`
		Usage  *struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
			Input      int `json:"input_tokens"`
			Output     int `json:"output_tokens"`
			Total      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if e = json.Unmarshal(body, &wire); e != nil {
		return core.ModelMessage{}, core.Usage{}, errors.New("模型返回不是有效 JSON")
	}
	usage := core.Usage{}
	if wire.Usage != nil {
		usage = core.Usage{Known: true, Input: wire.Usage.Prompt + wire.Usage.Input, Output: wire.Usage.Completion + wire.Usage.Output, Total: wire.Usage.Total}
	}
	m := core.ModelMessage{Role: "assistant"}
	if c.Config.Protocol == "responses" {
		if wire.Status != "completed" {
			return m, usage, fmt.Errorf("模型响应未完整结束：%s", wire.Status)
		}
		m.ProtocolItems = wire.Output
		for _, raw := range wire.Output {
			var item struct {
				Type      string `json:"type"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
				Content   []struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			if e = json.Unmarshal(raw, &item); e != nil {
				return m, usage, e
			}
			switch item.Type {
			case "function_call":
				m.ToolCalls = append(m.ToolCalls, core.Call{ID: item.CallID, Type: "function", Function: core.Function{Name: item.Name, Arguments: item.Arguments}})
			case "message":
				for _, part := range item.Content {
					m.Content += part.Text
				}
			}
		}
	} else {
		if len(wire.Choices) != 1 {
			return m, usage, errors.New("模型未返回唯一有效回复")
		}
		m = wire.Choices[0].Message
		if wire.Choices[0].Finish == "length" {
			return m, usage, errors.New("模型输出被长度限制截断，未执行不完整工具")
		}
		if c.Config.ToolMode == "json" {
			raw := m.Content
			m, e = parseJSONAction(raw, tools)
			if e != nil {
				return core.ModelMessage{Role: "assistant", Content: raw, InvalidAction: true}, usage, &ActionFormatError{Reason: e.Error()}
			}
		}
	}
	if m.Role != "assistant" || (strings.TrimSpace(m.Content) == "" && len(m.ToolCalls) == 0) {
		return m, usage, errors.New("模型返回为空或角色无效")
	}
	if len(m.ToolCalls) > 16 {
		return m, usage, errors.New("单次模型返回的工具请求过多")
	}
	seen := map[string]bool{}
	for _, call := range m.ToolCalls {
		if call.ID == "" || len(call.ID) > 200 || seen[call.ID] || call.Function.Name == "" || !json.Valid([]byte(call.Function.Arguments)) {
			return m, usage, errors.New("模型工具参数或调用标识无效")
		}
		seen[call.ID] = true
	}
	return m, usage, nil
}

func parseRetryAfter(value string) time.Duration {
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(0, time.Until(at))
	}
	return 0
}

func (c *Client) outputLimit() int {
	if c.Config.MaxOutputTokens > 0 {
		return c.Config.MaxOutputTokens
	}
	return 4096
}
