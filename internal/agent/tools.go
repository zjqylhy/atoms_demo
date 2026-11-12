package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"mockatoms/internal/core"
	"mockatoms/internal/model"
	"path"
	"sort"
	"strings"
)

func jsonValue(s string) any { var v any; _ = json.Unmarshal([]byte(s), &v); return v }
func prop(kind, description string) map[string]any {
	return map[string]any{"type": kind, "description": description}
}
func listProp(kind string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": kind}}
}
func def(name, desc string, fields map[string]any, required ...string) model.Tool {
	if required == nil {
		required = []string{}
	}
	return model.Tool{Name: name, Description: desc, Parameters: map[string]any{"type": "object", "properties": fields, "required": required, "additionalProperties": false}}
}
func definitions(role string) []model.Tool {
	all := []model.Tool{
		def("list_files", "查看当前项目文件和内容摘要", map[string]any{}),
		def("read_file", "分行读取项目文件，修改时使用返回的 hash；截取时按 next_line 继续读取", map[string]any{"path": prop("string", "相对路径"), "start_line": prop("integer", "起始行，默认 1"), "end_line": prop("integer", "结束行，默认到末尾；每次最多 16000 字节")}, "path"),
		def("search_files", "在项目文本中搜索字面文本，最多返回 50 处", map[string]any{"query": prop("string", "搜索文本")}, "query"),
		def("check_artifacts", "执行真实结构、语法及格式检查，结果不代替运行测试", map[string]any{}),
	}
	writes := map[string]any{"path": prop("string", "相对路径"), "content": prop("string", "全部 UTF-8 内容，保存会完整替换文件；局部片段请使用局部编辑工具"), "old_hash": prop("string", "已有文件的 hash；创建新文件填空字符串")}
	switch role {
	case "Mike":
		all = append(all,
			def("ask_user", "提出必要问题并等待用户回答", map[string]any{"question": prop("string", "问题"), "options": listProp("string")}, "question"),
			def("propose_plan", "保存计划及验收条件，Plan 模式会等待用户批准", map[string]any{"plan": prop("string", "Markdown 计划"), "acceptance": listProp("string")}, "plan", "acceptance"),
			def("assign_step", "按需分派一个专家并回收工作结果；不能多层委派", map[string]any{"role": map[string]any{"type": "string", "enum": []string{"Emma", "Bob", "Alex", "David"}}, "goal": prop("string", "子目标、已确认限制、输入文件引用及验收要求")}, "role", "goal"),
			def("finish_task", "申请完成：后端核验产物与真实报告，通过后才形成版本", map[string]any{"summary": prop("string", "交付内容、已验证项、未覆盖项"), "answer_only": prop("boolean", "仅普通解释问答且未修改产物时为 true")}, "summary", "answer_only"))
	case "Emma":
		all = append(all, def("ask_user", "询问缺少的关键信息", map[string]any{"question": prop("string", "问题"), "options": listProp("string")}, "question"), def("write_document", "保存 Markdown 或文本说明", writes, "path", "content", "old_hash"))
	case "Bob":
		all = append(all, def("write_document", "保存技术设计 Markdown 或文本", writes, "path", "content", "old_hash"))
	case "Alex":
		all = append(all,
			def("write_file", "完整替换文件，必须提供全部内容；不得把片段当完整文件。局部修改或大文件分次修改用 replace_text，拒绝覆盖意外变化", writes, "path", "content", "old_hash"),
			def("replace_text", "按旧摘要精确替换，匹配次数必须等于 expected_count", map[string]any{"path": prop("string", "文件路径"), "old_hash": prop("string", "文件当前摘要"), "old_text": prop("string", "待替换文字"), "new_text": prop("string", "新文字"), "expected_count": prop("integer", "预期匹配次数")}, "path", "old_hash", "old_text", "new_text", "expected_count"),
			def("delete_file", "只从当前草稿中移除文件，保留历史", map[string]any{"path": prop("string", "路径"), "old_hash": prop("string", "文件当前摘要")}, "path", "old_hash"),
			def("run_project", "在隔离环境真实构建网页、运行 JavaScript/TypeScript/Python 脚本或编译并测试 Go 模块。kind=go 时使用 go.mod；kind=script 必填 expected_stdout，工作目录为临时目录，项目文件按脚本位置解析", map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"web", "script", "go"}}, "entry": prop("string", "网页 index.html、脚本相对路径或根目录 go.mod"), "args": listProp("string"), "expected_stdout": prop("string", "kind=script 必填，去除首尾空白后精确比较；其他类型省略")}, "kind", "entry"),
			def("test_preview", "隔离浏览器执行需求对应的交互断言；至少一个断言，禁止自定义 JS", map[string]any{"steps": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"action": map[string]any{"type": "string", "enum": []string{"click", "fill", "select", "check", "expect_text", "expect_count", "expect_visible"}}, "selector": prop("string", "CSS 选择器"), "value": prop("string", "输入或期望文本"), "count": prop("integer", "期望数量"), "requirement": prop("string", "对应用户需求或计划验收项")}, "required": []string{"action", "selector", "requirement"}, "additionalProperties": false}}}, "steps"))
	case "David":
		all = append(all, def("write_document", "保存分析报告 Markdown", writes, "path", "content", "old_hash"),
			def("analyze_table", "对用户原始 CSV/JSON 执行真实统计并保存结果 CSV；聚合结果固定列名 group,value，分别为分组和数值，可直接交付并生成图表，不要手工改写", map[string]any{"source": prop("string", "只读输入文件路径"), "group_by": prop("string", "分组字段，可为空"), "value": prop("string", "运算字段，count 可为空"), "operation": map[string]any{"type": "string", "enum": []string{"select", "count", "sum", "mean", "min", "max"}}, "output": prop("string", "结果 CSV 路径"), "filter_column": prop("string", "可选筛选字段"), "filter_value": prop("string", "等值筛选值"), "select_columns": listProp("string"), "sort_by": prop("string", "输出排序字段，聚合结果为 group 或 value"), "sort_order": map[string]any{"type": "string", "enum": []string{"asc", "desc"}}, "sort_numeric": prop("boolean", "select 模式是否按数值排序")}, "source", "group_by", "value", "operation", "output"),
			def("create_chart", "引用真实分析结果生成柱状/折线图配置", map[string]any{"source": prop("string", "分析结果 CSV"), "output": prop("string", "输出 .chart.json 路径"), "kind": map[string]any{"type": "string", "enum": []string{"bar", "line"}}, "title": prop("string", "图表标题")}, "source", "output", "kind", "title"))
	}
	return all
}

type boundTool struct {
	def  model.Tool
	e    *Engine
	s    scope
	call core.Call
}

func (b *boundTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: b.def.Name, Desc: b.def.Description}, nil
}
func (b *boundTool) InvokableRun(ctx context.Context, raw string, _ ...tool.Option) (string, error) {
	if compose.GetToolCallID(ctx) != b.call.ID {
		return "", errors.New("工具调用关联错误")
	}
	var args map[string]any
	failure := json.Unmarshal([]byte(raw), &args)
	if failure == nil {
		failure = validateArgs(args, b.def.Parameters)
	}
	if failure != nil {
		return b.e.record(b.s, b.call, nil, func(*core.Project) (any, error) { return nil, failure })
	}
	return b.e.execute(ctx, b.s, b.call, args)
}

// Feedback is derived from the same schema exposed to the model.
func validateArgs(a map[string]any, schema map[string]any) error {
	if a == nil {
		return errors.New("参数必须是非 null 对象")
	}
	props := schema["properties"].(map[string]any)
	allowed := make([]string, 0, len(props))
	for key := range props {
		allowed = append(allowed, key)
	}
	sort.Strings(allowed)
	var problems []string
	for key, value := range a {
		spec, exists := props[key]
		if !exists {
			problems = append(problems, "未知参数 "+key)
			continue
		}
		if err := validateValue(value, spec.(map[string]any)); err != nil {
			problems = append(problems, key+"："+err.Error())
		}
	}
	for _, key := range schema["required"].([]string) {
		if _, exists := a[key]; !exists {
			problems = append(problems, "缺少参数 "+key)
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("%s；允许字段：%s；必填字段：%s", strings.Join(problems, "；"), strings.Join(allowed, ", "), strings.Join(schema["required"].([]string), ", "))
}
func validateValue(v any, spec map[string]any) error {
	valid := false
	switch spec["type"] {
	case "string":
		_, valid = v.(string)
	case "boolean":
		_, valid = v.(bool)
	case "integer":
		n, ok := v.(float64)
		valid = ok && n == float64(int64(n))
	case "array":
		items, ok := v.([]any)
		valid = ok
		if ok {
			for i, item := range items {
				if err := validateValue(item, spec["items"].(map[string]any)); err != nil {
					return fmt.Errorf("第 %d 项：%w", i+1, err)
				}
			}
		}
	case "object":
		object, ok := v.(map[string]any)
		valid = ok
		if ok {
			return validateArgs(object, spec)
		}
	}
	if !valid {
		return fmt.Errorf("类型无效，要求 %s", spec["type"])
	}
	if allowed, ok := spec["enum"].([]string); ok {
		for _, item := range allowed {
			if v == item {
				return nil
			}
		}
		return fmt.Errorf("允许值：%s", strings.Join(allowed, ", "))
	}
	return nil
}
func str(a map[string]any, k string) string { s, _ := a[k].(string); return s }
func stringsArg(a map[string]any, k string) []string {
	out := []string{}
	if values, ok := a[k].([]any); ok {
		for _, v := range values {
			s, ok := v.(string)
			if ok {
				out = append(out, s)
			}
		}
	}
	return out
}
func permitted(t *core.Task) bool { return t.Mode == "build" || (t.Plan != nil && t.Plan.Approved) }
func kind(name string) string {
	switch path.Ext(name) {
	case ".md", ".txt":
		return "document"
	case ".csv":
		return "data"
	case ".html":
		return "web"
	case ".js", ".mjs", ".cjs", ".ts", ".mts", ".cts", ".tsx", ".jsx", ".css", ".py", ".go":
		return "code"
	case ".json":
		if strings.HasSuffix(name, ".chart.json") {
			return "chart"
		}
		return "data"
	}
	return "text"
}
func write(p *core.Project, name, content, oldHash string, documentOnly bool) error {
	if _, err := core.FilePath(name); err != nil {
		return err
	}
	if len(content) > core.MaxFileBytes {
		return errors.New("文件超过大小限制")
	}
	old, exists := p.Files[name]
	if old.ReadOnly {
		return errors.New("原始输入文件不可修改")
	}
	if exists && old.Hash != oldHash {
		return errors.New("文件已变化，请重新读取后修改")
	}
	if !exists && oldHash != "" {
		return errors.New("文件不存在，不能使用旧摘要")
	}
	if documentOnly && path.Ext(name) != ".md" && path.Ext(name) != ".txt" {
		return errors.New("文档工具只接受 Markdown/文本")
	}
	p.Files[name] = core.File{Hash: core.Hash([]byte(content)), Size: len(content), Kind: kind(name)}
	return nil
}
func (e *Engine) execute(ctx context.Context, s scope, c core.Call, a map[string]any) (string, error) {
	p, t, f, err := e.load(s)
	if err != nil {
		return "", err
	}
	if err := s.checkInstruction(p); err != nil {
		return "", err
	}
	s.toolStarted = true
	// Mutating implementation tools remain blocked regardless of role, pathname or model text.
	needsApproval := map[string]bool{"write_file": true, "replace_text": true, "delete_file": true, "analyze_table": true, "create_chart": true, "run_project": true, "test_preview": true, "finish_task": true}
	answerOnly := c.Function.Name == "finish_task" && a["answer_only"] == true && p.Files.Digest() == t.Base.Digest()
	if needsApproval[c.Function.Name] && !permitted(t) && !answerOnly {
		return e.record(s, c, nil, func(*core.Project) (any, error) { return nil, errors.New("当前计划尚未批准") })
	}
	fail := func(err error) (string, error) {
		return e.record(s, c, nil, func(*core.Project) (any, error) { return nil, err })
	}
	switch c.Function.Name {
	case "assign_step":
		role := str(a, "role")
		if !permitted(t) && role != "Emma" && role != "Bob" {
			return fail(errors.New("批准前只能分派需求或设计规划"))
		}
		childID := s.Frame + "/" + c.ID
		_, err = e.Store.Mutate(s.Owner, s.Project, nil, func(p *core.Project) error {
			if err := s.checkInstruction(p); err != nil {
				return err
			}
			task := p.Tasks[s.Task]
			if task.Frames[childID] == nil {
				packet := map[string]any{"goal": str(a, "goal"), "task_goal": task.Goal, "plan": task.Plan, "files": p.Files, "acceptance": "按用户要求与已批准范围执行；检查引用必须是真实工具结果"}
				task.Frames[childID] = &core.Frame{ID: childID, Role: role, Goal: str(a, "goal"), Messages: []core.ModelMessage{{Role: "user", Content: core.JSON(packet)}}}
				p.Event("handoff", "Mike", "交给 "+role+"："+str(a, "goal"), nil)
			}
			return nil
		})
		if err != nil {
			return "", err
		}
		child := s
		child.Frame = childID
		result, err := e.runFrame(ctx, child)
		redirected := errors.Is(err, errNewInstruction)
		if err != nil && !redirected {
			return "", err
		}
		return e.record(s, c, nil, func(p *core.Project) (any, error) {
			if redirected {
				return redirectedDelegation(p, role), nil
			}
			return map[string]any{"role": role, "summary": result, "files": p.Files}, nil
		})
	case "list_files":
		return e.record(s, c, nil, func(p *core.Project) (any, error) { return p.Files, nil })
	case "read_file":
		file, ok := p.Files[str(a, "path")]
		if !ok {
			return fail(errors.New("文件不存在"))
		}
		content, err := e.Store.Blob(s.Owner, file.Hash)
		if err != nil {
			return "", err
		}
		start, end := 1, 0
		if n, ok := a["start_line"].(float64); ok {
			start = int(n)
		}
		if n, ok := a["end_line"].(float64); ok {
			end = int(n)
		}
		if start < 1 || end < 0 || (end > 0 && end < start) {
			return fail(errors.New("读取行范围无效"))
		}
		body, first, last, complete := fileSlice(string(content), start, end)
		if last < first {
			return fail(errors.New("范围为空或单行超过 16000 字节，请缩小资源或使用分段格式"))
		}
		return e.record(s, c, nil, func(p *core.Project) (any, error) {
			material := fileMaterial(str(a, "path"), file, body, s.Frame+"/"+c.ID)
			material.Reason = fmt.Sprintf("当前子目标依赖，第 %d–%d 行", first, last)
			material.Recovery = fmt.Sprintf("read_file(path=%q,start_line=%d)，hash=%s", str(a, "path"), last+1, file.Hash)
			retainMaterial(p.Tasks[s.Task].Frames[s.Frame], material)
			return map[string]any{"path": str(a, "path"), "hash": file.Hash, "content": body, "start_line": first, "end_line": last, "complete": complete, "next_line": last + 1}, nil
		})
	case "search_files":
		query := str(a, "query")
		if len(query) == 0 || len(query) > 200 {
			return fail(errors.New("搜索文本长度无效"))
		}
		files, err := e.Store.Files(s.Owner, p.Files)
		if err != nil {
			return "", err
		}
		hits := []any{}
		names := []string{}
		for n := range files {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			for i, line := range strings.Split(files[n], "\n") {
				if strings.Contains(line, query) && len(hits) < 50 {
					if len(line) > 500 {
						line = line[:500]
					}
					hits = append(hits, map[string]any{"path": n, "line": i + 1, "text": line})
				}
			}
		}
		return e.record(s, c, nil, func(*core.Project) (any, error) { return hits, nil })
	case "write_file", "write_document", "replace_text":
		name := str(a, "path")
		content := str(a, "content")
		if c.Function.Name == "replace_text" {
			file, ok := p.Files[name]
			if !ok {
				return fail(errors.New("文件不存在"))
			}
			b, err := e.Store.Blob(s.Owner, file.Hash)
			if err != nil {
				return "", err
			}
			old := str(a, "old_text")
			count := int(a["expected_count"].(float64))
			if old == "" || count < 1 || strings.Count(string(b), old) != count {
				return fail(errors.New("精确替换匹配次数不符"))
			}
			content = strings.ReplaceAll(string(b), old, str(a, "new_text"))
		}
		if !permitted(t) && !strings.HasPrefix(name, "planning/") {
			return fail(errors.New("批准前的规划文档必须保存在 planning/ 目录"))
		}
		hash := core.Hash([]byte(content))
		return e.record(s, c, map[string][]byte{hash: []byte(content)}, func(p *core.Project) (any, error) {
			if err := write(p, name, content, str(a, "old_hash"), c.Function.Name == "write_document"); err != nil {
				return nil, err
			}
			rememberFile(p, s, c, name, content)
			return map[string]any{"path": name, "hash": hash, "saved": true}, nil
		})
	case "delete_file":
		return e.record(s, c, nil, func(p *core.Project) (any, error) {
			old, ok := p.Files[str(a, "path")]
			if !ok || old.Hash != str(a, "old_hash") || old.ReadOnly {
				return nil, errors.New("文件不存在、只读或已变化")
			}
			delete(p.Files, str(a, "path"))
			for _, frame := range p.Tasks[s.Task].Frames {
				delete(frame.Materials, "file:"+str(a, "path"))
			}
			return "已从草稿移除，历史保留", nil
		})
	case "ask_user":
		return e.record(s, c, nil, func(p *core.Project) (any, error) {
			task := p.Tasks[s.Task]
			task.Question = &core.Question{ID: core.ID(), FrameID: s.Frame, Text: str(a, "question"), Options: stringsArg(a, "options")}
			task.Status = "waiting_input"
			p.Event("question", f.Role, task.Question.Text, task.Question)
			return task.Question, nil
		})
	case "propose_plan":
		return e.record(s, c, nil, func(p *core.Project) (any, error) {
			task := p.Tasks[s.Task]
			task.Plan = &core.Plan{Revision: core.ID(), Text: str(a, "plan"), Acceptance: stringsArg(a, "acceptance"), Approved: task.Mode == "build"}
			if len(task.Plan.Acceptance) == 0 {
				return nil, errors.New("计划必须列出验收条件")
			}
			if task.Mode == "plan" {
				task.Status = "waiting_plan"
			}
			p.Event("plan", "Mike", "计划已保存", task.Plan)
			return task.Plan, nil
		})
	case "check_artifacts":
		files, err := e.Store.Files(s.Owner, p.Files)
		if err != nil {
			return "", err
		}
		report := checkArtifacts(files, p.Files)
		return e.record(s, c, nil, func(p *core.Project) (any, error) {
			if p.Files.Digest() != report.Digest {
				return nil, errors.New("检查期间文件变化")
			}
			p.Reports = append(p.Reports, report)
			return report, nil
		})
	case "run_project", "test_preview":
		return e.executeRunner(ctx, s, c, a, p)
	case "analyze_table":
		return e.analyze(s, c, a, p)
	case "create_chart":
		return e.chart(s, c, a, p)
	case "finish_task":
		return e.record(s, c, nil, func(p *core.Project) (any, error) {
			task := p.Tasks[s.Task]
			answerOnly, _ := a["answer_only"].(bool)
			if answerOnly {
				if p.Files.Digest() != task.Base.Digest() {
					return nil, errors.New("已修改文件，不能按普通问答结束")
				}
				task.Status = "completed"
				p.Message("Mike", str(a, "summary"), "delivery")
				return "问答完成", nil
			}
			if len(p.Files) == 0 {
				return nil, errors.New("没有可交付文件")
			}
			if err := completionCheck(p); err != nil {
				return nil, err
			}
			version := core.Version{ID: core.ID(), Number: len(p.Versions) + 1, TaskID: task.ID, At: core.Now(), Summary: str(a, "summary"), Manifest: core.Clone(p.Files)}
			for _, report := range p.Reports {
				if report.Digest == p.Files.Digest() {
					version.Reports = append(version.Reports, report)
				}
			}
			p.Versions = append(p.Versions, version)
			p.Head = version.ID
			task.Status = "completed"
			p.Message("Mike", str(a, "summary"), "delivery")
			p.Event("version", "system", fmt.Sprintf("已保存版本 v%d", version.Number), map[string]any{"version": version.ID})
			return map[string]any{"version": version.ID, "number": version.Number}, nil
		})
	}
	return fail(errors.New("未实现工具"))
}
