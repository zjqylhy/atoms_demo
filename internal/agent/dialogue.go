package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"strings"
)

type contextHistory struct {
	system       core.ModelMessage
	messages     []core.ModelMessage
	requirements []contextRequirement
	duplicates   map[string]bool
	recent       int
	latestUser   int
}

func newContextHistory(system core.ModelMessage, messages []core.ModelMessage, requirements []contextRequirement) contextHistory {
	h := contextHistory{system: system, messages: messages, requirements: requirements, duplicates: map[string]bool{}, latestUser: -1}
	for _, r := range requirements {
		if r.Kind == "answer" {
			h.duplicates["用户补充："+r.Text] = true
		} else {
			h.duplicates["修改计划，仍须等待批准："+r.Text] = true
		}
	}
	recentCount := 0
	for i := len(messages) - 1; i >= 0; i-- {
		if !h.isDialogue(i) {
			continue
		}
		if recentCount < 4 {
			h.recent = i
			recentCount++
		}
		if h.latestUser == -1 && messages[i].Role == "user" {
			h.latestUser = i
		}
	}
	return h
}

func (h contextHistory) isDialogue(i int) bool {
	m := h.messages[i]
	if m.Role == "assistant" {
		return !m.InvalidAction && m.Content != ""
	}
	return m.Role == "user" && m.Content != "" && m.Content != nextActionPrompt &&
		!(i > 0 && h.messages[i-1].InvalidAction && strings.HasPrefix(m.Content, formatFeedbackPrefix))
}

func (h contextHistory) duplicate(i int) bool {
	m := h.messages[i]
	return i < h.recent && i != h.latestUser && m.Role == "user" && h.duplicates[m.Content]
}

func frameSource(i int) string { return fmt.Sprintf("frame:%d", i) }

func uncoveredText(s string, through int) string {
	if through <= 0 {
		return s
	}
	return s[min(through, len(s)):]
}

// project keeps tool-call boundaries independent of the range summarized as dialogue.
func (h contextHistory) project(tools *core.ContextSummary, dialogue *core.DialogueSummary) ([]core.ModelMessage, int) {
	messages := []core.ModelMessage{h.system}
	if tools == nil && dialogue == nil {
		messages = append(messages, h.messages...)
		return messages, len(messages)
	}
	var covered map[string]int
	if dialogue != nil {
		covered = dialogue.Covered
	}
	var requirements []contextRequirement
	for _, r := range h.requirements {
		r.Text = uncoveredText(r.Text, covered[r.Source])
		if r.Text != "" {
			requirements = append(requirements, r)
		}
	}
	if len(requirements) != 0 {
		messages = append(messages, core.ModelMessage{Role: "user", Content: "本任务已记录的用户回答和计划更正（按时间排序）：\n" + core.JSON(requirements)})
	}
	cut := 0
	if tools != nil {
		cut = tools.Through
		messages = append(messages, core.ModelMessage{Role: "user", Content: tools.Text})
	}
	if dialogue != nil && len(dialogue.Items) != 0 {
		messages = append(messages, core.ModelMessage{Role: "user", Content: "旧对话摘要（只读历史。用户最新要求、当前计划和实际检查记录优先）：\n" + core.JSON(dialogue.Items)})
	}
	completeFrom := 0
	for i, m := range h.messages {
		if i == cut && tools != nil {
			completeFrom = len(messages)
		}
		if h.duplicate(i) || (i < cut && !h.isDialogue(i)) {
			continue
		}
		if i < cut {
			m = core.ModelMessage{Role: m.Role, Content: m.Content}
		}
		if through := covered[frameSource(i)]; through > 0 {
			m.Content = uncoveredText(m.Content, through)
			if m.Content == "" && len(m.ToolCalls) == 0 {
				continue
			}
		}
		messages = append(messages, m)
	}
	if completeFrom == 0 {
		completeFrom = len(messages)
	}
	return messages, completeFrom
}

type dialogueLine struct {
	ID       string `json:"id"`
	Role     string `json:"role"`
	Text     string `json:"text"`
	Question string `json:"question,omitempty"`
}

func (h contextHistory) oldDialogue(tools *core.ContextSummary) []dialogueLine {
	var lines []dialogueLine
	latestCorrection := -1
	for i, r := range h.requirements {
		if r.Kind == "plan_feedback" {
			latestCorrection = i
		}
	}
	for i, r := range h.requirements {
		if i != latestCorrection && i < len(h.requirements)-1 && r.Text != "" {
			lines = append(lines, dialogueLine{ID: r.Source, Role: "user", Text: r.Text, Question: r.Question})
		}
	}
	for i := 1; i < h.recent; i++ {
		m := h.messages[i]
		if i == h.latestUser || !h.isDialogue(i) || h.duplicate(i) {
			continue
		}
		if len(m.ToolCalls) != 0 && (tools == nil || i >= tools.Through) {
			continue
		}
		lines = append(lines, dialogueLine{ID: frameSource(i), Role: m.Role, Text: m.Content})
	}
	return lines
}

type dialogueBatch struct {
	covered  map[string]int
	roles    map[string]string
	rebuild  func(*core.DialogueSummary) (model.PreparedRequest, error)
	previous int
}

func prepareDialogueBatch(client *model.Client, tools []model.Tool, history contextHistory, toolSummary *core.ContextSummary, prior *core.DialogueSummary, best preparedContext) (preparedContext, error) {
	if prior == nil {
		prior = &core.DialogueSummary{}
	}
	rebuild := func(summary *core.DialogueSummary) (model.PreparedRequest, error) {
		messages, completeFrom := history.project(toolSummary, summary)
		return client.PrepareRequest(messages, tools, completeFrom)
	}
	minimum := &core.DialogueSummary{Covered: make(map[string]int, len(prior.Covered))}
	maps.Copy(minimum.Covered, prior.Covered)
	var remaining []dialogueLine
	roles := map[string]string{}
	for _, line := range history.oldDialogue(toolSummary) {
		roles[line.ID] = line.Role
		minimum.Covered[line.ID] = len(line.Text)
		line.Text = uncoveredText(line.Text, prior.Covered[line.ID])
		if line.Text != "" {
			remaining = append(remaining, line)
		}
	}
	minimumRequest, err := rebuild(minimum)
	if err != nil {
		return preparedContext{}, err
	}
	limit := client.Config.RequestByteLimit()
	if minimumRequest.ByteSize() > limit {
		return preparedContext{}, fmt.Errorf("必须保留的任务要求、工具定义或近期完整动作占用过多：%d 字节超过 %d 字节上限，进度已保留", minimumRequest.ByteSize(), limit)
	}
	if len(remaining) == 0 {
		return preparedContext{}, fmt.Errorf("没有可继续压缩的旧对话：请求 %d 字节，超过 %d 字节上限，进度已保留", best.request.ByteSize(), limit)
	}
	request, lines, err := fitDialogueBatch(client, prior.Items, remaining)
	if err != nil {
		return preparedContext{}, err
	}
	covered := make(map[string]int, len(prior.Covered)+len(lines))
	maps.Copy(covered, prior.Covered)
	provided := make(map[string]string, len(lines))
	for _, item := range prior.Items {
		for _, source := range item.Sources {
			provided[source] = roles[source]
		}
	}
	for _, line := range lines {
		covered[line.ID] += len(line.Text)
		provided[line.ID] = line.Role
	}
	best.dialogue = &dialogueBatch{covered: covered, roles: provided, rebuild: rebuild, previous: best.request.ByteSize()}
	best.request = request
	return best, nil
}

func fitDialogueBatch(client *model.Client, previous []core.DialogueSummaryItem, lines []dialogueLine) (model.PreparedRequest, []dialogueLine, error) {
	prepare := func(lines []dialogueLine) (model.PreparedRequest, bool, error) {
		input := struct {
			Previous []core.DialogueSummaryItem `json:"previous,omitempty"`
			Messages []dialogueLine             `json:"messages"`
		}{previous, lines}
		request, err := client.PrepareDialogue(core.JSON(input))
		fits := request.ByteSize() <= client.Config.RequestByteLimit() &&
			(client.Config.ProviderTPM <= 0 || request.ReservationTokens() <= client.Config.ProviderTPM)
		return request, fits, err
	}
	var best model.PreparedRequest
	count := 0
	for low, high := 1, len(lines); low <= high; {
		mid := low + (high-low)/2
		request, fits, err := prepare(lines[:mid])
		if err != nil {
			return best, nil, err
		}
		if fits {
			best, count = request, mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if count > 0 {
		return best, lines[:count], nil
	}
	// A single long message can exceed the limit after JSON escaping.
	line := lines[0]
	runes := []rune(line.Text)
	for low, high := 1, len(runes); low <= high; {
		mid := low + (high-low)/2
		line.Text = string(runes[:mid])
		request, fits, err := prepare([]dialogueLine{line})
		if err != nil {
			return best, nil, err
		}
		if fits {
			best, count = request, mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if count == 0 {
		return best, nil, errors.New("摘要请求的必要内容超过请求体上限或每分钟额度，进度已保留")
	}
	line.Text = string(runes[:count])
	return best, []dialogueLine{line}, nil
}

func (b *dialogueBatch) accept(content string) (*core.DialogueSummary, int, error) {
	var response struct {
		Items []core.DialogueSummaryItem `json:"items"`
	}
	d := json.NewDecoder(strings.NewReader(content))
	d.DisallowUnknownFields()
	if err := d.Decode(&response); err != nil || response.Items == nil || d.Decode(new(any)) != io.EOF {
		return nil, 0, errors.New("模型返回的对话摘要格式无效")
	}
	for _, item := range response.Items {
		switch item.Kind {
		case "requirement", "suggestion", "conclusion", "pending":
		default:
			return nil, 0, errors.New("模型返回的对话摘要类别无效")
		}
		hasUser := false
		for _, source := range item.Sources {
			role, provided := b.roles[source]
			if !provided {
				return nil, 0, errors.New("模型返回的对话摘要引用了未提供的消息")
			}
			hasUser = hasUser || role == "user"
		}
		if strings.TrimSpace(item.Text) == "" || len(item.Sources) == 0 || (item.Kind == "requirement" && !hasUser) {
			return nil, 0, errors.New("模型返回的对话摘要缺少内容或用户来源")
		}
	}
	summary := &core.DialogueSummary{Covered: b.covered, Items: response.Items}
	request, err := b.rebuild(summary)
	return summary, request.ByteSize(), err
}
