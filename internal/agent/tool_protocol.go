package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"mockatoms/internal/core"
	"strings"
)

// Control semantics and execution conditions belong to tool registration.
// The loop consumes these properties without decoding a tool's parameters.
type toolProtocol struct {
	control         string
	label           string
	currentAtCommit bool
	local           bool
	inputs          func(*core.Project, map[string]any) core.Manifest
	retry           func(*core.Project, map[string]any, *core.ActionFact)
	retryArguments  func(map[string]any) map[string]any
}

func fileInputs(fields ...string) func(*core.Project, map[string]any) core.Manifest {
	return func(p *core.Project, a map[string]any) core.Manifest {
		m := core.Manifest{}
		for _, field := range fields {
			name := str(a, field)
			if name != "" {
				file, ok := p.Files[name]
				if !ok {
					file.Hash = "absent"
				}
				m[name] = file
			}
		}
		return m
	}
}
func allInputs(p *core.Project, _ map[string]any) core.Manifest { return core.Clone(p.Files) }

var toolProtocols = map[string]toolProtocol{
	"list_files":      {label: "查看文件清单", local: true, inputs: allInputs},
	"read_file":       {label: "读取文件", local: true, inputs: fileInputs("path")},
	"search_files":    {label: "搜索文件", local: true, inputs: allInputs},
	"write_file":      {label: "保存文件", local: true, inputs: fileInputs("path")},
	"write_document":  {label: "保存文档", local: true, inputs: fileInputs("path")},
	"replace_text":    {label: "修改文件", local: true, inputs: fileInputs("path")},
	"delete_file":     {label: "删除文件", local: true, inputs: fileInputs("path")},
	"analyze_table":   {label: "计算表格数据", local: true, inputs: fileInputs("source", "output")},
	"create_chart":    {label: "生成图表", local: true, inputs: fileInputs("source", "output")},
	"check_artifacts": {label: "检查文件", local: true, inputs: allInputs},
	"run_project":     {label: "实际运行产物", inputs: allInputs},
	"test_preview":    {label: "执行浏览器验收", inputs: allInputs},
	"assign_step":     {control: "delegate", label: "接收角色交付"},
	"ask_user":        {control: "wait", label: "等待用户补充", currentAtCommit: true},
	"propose_plan":    {control: "plan", label: "提交计划", currentAtCommit: true},
	"finish_task":     {control: "finish", label: "完成任务", currentAtCommit: true, local: true, inputs: allInputs, retry: completionRetry, retryArguments: completionArguments},
	"return_step":     {control: "return", label: "交回主 Agent", currentAtCommit: true},
}

func controlTool(name string) bool { return toolProtocols[name].control != "" }

func actionFact(p *core.Project, s scope, c core.Call) core.ActionFact {
	profile := toolProtocols[c.Function.Name]
	args := map[string]any{}
	_ = json.Unmarshal([]byte(c.Function.Arguments), &args)
	var inputs core.Manifest
	if profile.inputs != nil {
		inputs = profile.inputs(p, args)
	}
	conditions := fmt.Sprintf("instruction:%d;approved:%t", s.InstructionVersion, permitted(p.Tasks[s.Task]))
	fact := core.ActionFact{Disposition: "executed", Inputs: inputs, Deterministic: profile.local, Conditions: conditions}
	if profile.retry != nil {
		profile.retry(p, args, &fact)
	}
	return fact
}

func completionArguments(args map[string]any) map[string]any {
	delete(args, "summary")
	// Missing and explicit false use the same delivery mode.
	if args["answer_only"] == nil {
		args["answer_only"] = false
	}
	return args
}

func completionRetry(p *core.Project, args map[string]any, fact *core.ActionFact) {
	if answerOnly, _ := args["answer_only"].(bool); answerOnly {
		return
	}
	fact.RetryVersion = 1
	fact.Inputs = nil
	for _, issue := range assessAcceptance(p).Issues {
		fact.RetryKeys = append(fact.RetryKeys, issue.Basis)
	}
	fact.RetryKeys = sortedUnique(fact.RetryKeys)
}

func recordFailure(fact *core.ActionFact, failure error) {
	fact.ErrorClass = "validation"
	fact.ErrorCode = core.Hash([]byte(failure.Error()))
	var assessment *AcceptanceAssessment
	if errors.As(failure, &assessment) {
		fact.ErrorCode = assessment.Code
	} else {
		// Parameter/permission errors must not increment unrelated acceptance blockers.
		fact.RetryVersion = 0
		fact.RetryKeys = nil
	}
}

func toolEvent(c core.Call) string {
	label := toolProtocols[c.Function.Name].label
	if label == "" {
		label = c.Function.Name
	}
	var args map[string]any
	if json.Unmarshal([]byte(c.Function.Arguments), &args) == nil && str(args, "path") != "" {
		label += "：" + str(args, "path")
	}
	return label
}

// Normalization sorts object keys while preserving numeric precision, arrays,
// strings and all meaningful parameter changes.
func normalizedArguments(raw string) string {
	d := json.NewDecoder(strings.NewReader(raw))
	d.UseNumber()
	var value any
	if d.Decode(&value) != nil {
		return raw
	}
	return core.JSON(value)
}

func failureFingerprint(tool, args string, fact core.ActionFact) string {
	if project := toolProtocols[tool].retryArguments; project != nil {
		decoder := json.NewDecoder(strings.NewReader(args))
		decoder.UseNumber()
		var values map[string]any
		if decoder.Decode(&values) == nil && values != nil {
			args = core.JSON(project(values))
		}
	}
	basis := tool + "\n" + normalizedArguments(args) + "\n" + core.JSON(fact.Inputs) + "\n" + fact.Conditions + "\n" + fact.ErrorCode
	if fact.RetryVersion == 1 {
		basis += "\nacceptance-v1\n" + core.JSON(fact.RetryKeys)
	}
	return core.Hash([]byte(basis))
}
