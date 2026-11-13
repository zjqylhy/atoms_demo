package agent

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mockatoms/internal/core"
	"mockatoms/internal/validation"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

func decodeArgument[T any](a map[string]any, key string) (T, error) {
	var value T
	if a[key] == nil {
		return value, nil
	}
	err := json.Unmarshal([]byte(core.JSON(a[key])), &value)
	return value, err
}

func validateDelivery(d *core.DeliveryScope, acceptance []string) error {
	if d == nil {
		return nil
	}
	if len(d.Targets) == 0 || len(d.Criteria) == 0 {
		return errors.New("交付范围须有目标文件和验收对应关系")
	}
	for _, name := range append(append(slices.Clone(d.Targets), d.Inputs...), d.Preserve...) {
		if _, err := core.FilePath(name); err != nil {
			return err
		}
	}
	covered := map[string]bool{}
	pathsCovered := map[string]bool{}
	for _, c := range d.Criteria {
		if !slices.Contains(acceptance, c.Requirement) || len(c.Paths) == 0 {
			return errors.New("交付验收项必须对应计划的原文条件和目标文件")
		}
		for _, name := range c.Paths {
			pathsCovered[name] = true
			if !slices.Contains(d.Targets, name) && !slices.Contains(d.Preserve, name) {
				return errors.New("验收项路径不在目标或保留能力范围")
			}
		}
		covered[c.Requirement] = true
	}
	for _, name := range append(slices.Clone(d.Targets), d.Preserve...) {
		if !pathsCovered[name] {
			return fmt.Errorf("交付范围缺少目标的验收：%s", name)
		}
	}
	for _, item := range acceptance {
		if !covered[item] {
			return fmt.Errorf("交付范围漏掉计划验收项：%s", item)
		}
	}
	return nil
}

func evidenceFor(t *core.Task, s scope, inputs, outputs core.Manifest) *core.ReportEvidence {
	ref := &core.ReportEvidence{TaskID: t.ID, InstructionVersion: s.InstructionVersion, Inputs: core.Clone(inputs), Outputs: core.Clone(outputs), RunID: s.Run, Epoch: s.Epoch}
	if t.Plan != nil {
		ref.PlanRevision = t.Plan.Revision
	}
	return ref
}

func reportCurrent(p *core.Project, t *core.Task, r core.Report) bool {
	if r.Evidence == nil {
		return r.Digest == p.Files.Digest()
	}
	e := r.Evidence
	if t == nil || e.TaskID != t.ID || e.InstructionVersion != len(t.InstructionMessages) {
		return false
	}
	revision := ""
	if t.Plan != nil {
		revision = t.Plan.Revision
	}
	if e.PlanRevision != revision {
		return false
	}
	for name, file := range e.Inputs {
		if _, replaced := e.Outputs[name]; !replaced && p.Files[name] != file {
			return false
		}
	}
	for name, file := range e.Outputs {
		if p.Files[name] != file {
			return false
		}
	}
	return len(e.Inputs)+len(e.Outputs) > 0
}

func deliveryManifest(p *core.Project, t *core.Task) (core.Manifest, error) {
	if t == nil || t.Plan == nil || t.Plan.Delivery == nil {
		return p.Files, nil
	}
	d := t.Plan.Delivery
	m := core.Manifest{}
	for _, name := range append(append(slices.Clone(d.Targets), d.Inputs...), d.Preserve...) {
		file, ok := p.Files[name]
		if !ok {
			return nil, fmt.Errorf("交付范围缺少文件：%s", name)
		}
		m[name] = file
	}
	return m, nil
}

// Assertions inspect bytes in the service, not a script's success prose.
func assertArtifact(data []byte, a core.ArtifactAssertion) error {
	switch a.Kind {
	case "nonempty":
		if len(data) == 0 {
			return errors.New("文件为空")
		}
	case "text_contains", "text_not_contains":
		if a.Expected == "" || !utf8.Valid(data) {
			return errors.New("expected 不能为空，待检查内容必须为有效 UTF-8 文本")
		}
		contains := strings.Contains(string(data), a.Expected)
		if a.Kind == "text_contains" && !contains {
			return errors.New("文本未包含期望内容")
		}
		if a.Kind == "text_not_contains" && contains {
			return errors.New("文本包含被禁止的内容")
		}
	case "json_equals":
		var actual, expected any
		decode := func(data []byte, target *any) error {
			d := json.NewDecoder(bytes.NewReader(data))
			d.UseNumber()
			if err := d.Decode(target); err != nil {
				return err
			}
			if d.Decode(new(any)) != io.EOF {
				return errors.New("多余 JSON 内容")
			}
			return nil
		}
		if decode(data, &actual) != nil || decode([]byte(a.Expected), &expected) != nil || core.JSON(actual) != core.JSON(expected) {
			return errors.New("JSON 内容与期望不一致")
		}
	case "archive_count", "archive_text_contains", "archive_text_not_contains", "archive_xml_contains", "archive_xml_not_contains":
		reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return errors.New("归档结构无效")
		}
		if len(reader.File) > 2048 {
			return errors.New("归档成员数量超限")
		}
		matched := 0
		total := uint64(0)
		found := false
		exactPresent := false
		negative := strings.HasSuffix(a.Kind, "_not_contains")
		for _, file := range reader.File {
			if file.UncompressedSize64 > 16*1024*1024-total {
				return errors.New("归档解压后大小超限")
			}
			total += file.UncompressedSize64
			exactPresent = exactPresent || file.Name == a.Member
			ok, err := matchArchiveMember(a, file.Name)
			if err != nil {
				return errors.New("归档成员匹配模式无效")
			}
			if !ok {
				continue
			}
			matched++
			if a.Kind == "archive_count" {
				continue
			}
			stream, err := file.Open()
			if err != nil {
				return err
			}
			content, err := io.ReadAll(io.LimitReader(stream, 2*1024*1024+1))
			stream.Close()
			if err != nil || len(content) > 2*1024*1024 {
				return errors.New("归档成员读取失败或超限")
			}
			if strings.HasPrefix(a.Kind, "archive_xml_") {
				content, err = archiveXMLText(content)
				if err != nil {
					return fmt.Errorf("成员 %q：%w", file.Name, err)
				}
			}
			if a.Expected == "" || !utf8.Valid(content) {
				return fmt.Errorf("成员 %q：expected 不能为空，内容必须为有效 UTF-8 文本", file.Name)
			}
			contains := strings.Contains(string(content), a.Expected)
			if negative && contains {
				return fmt.Errorf("成员 %q 包含被禁止的内容", file.Name)
			}
			found = found || contains
		}
		mode := a.MemberMatch
		if mode == "" {
			mode = "glob"
		}
		if matched == 0 {
			if mode == "glob" && exactPresent {
				return fmt.Errorf("member=%q 按 glob 匹配到 0 个成员；归档中存在同名字面成员，请设置 member_match=\"exact\"", excerpt(a.Member, 160))
			}
			return fmt.Errorf("member=%q 按 %s 匹配到 0 个成员；请核对 member 和 member_match，未匹配不代表内容检查通过", excerpt(a.Member, 160), mode)
		}
		if a.Kind == "archive_count" {
			if a.Count < 1 || matched != a.Count {
				return fmt.Errorf("member=%q 按 %s 匹配成员数 %d，期望 %d", excerpt(a.Member, 160), mode, matched, a.Count)
			}
		} else if !negative && !found {
			return fmt.Errorf("member=%q 按 %s 匹配到 %d 个成员，但没有期望内容", excerpt(a.Member, 160), mode, matched)
		}
	default:
		return errors.New("不支持的内容断言")
	}
	return nil
}

func matchArchiveMember(a core.ArtifactAssertion, name string) (bool, error) {
	switch a.MemberMatch {
	case "exact":
		return a.Member == name, nil
	case "", "glob":
		return path.Match(a.Member, name)
	default:
		return false, errors.New("member_match 必须为 exact 或 glob")
	}
}

// A negative XML check cannot pass just because an empty or malformed member
// yielded no text. Require one complete document before inspecting its text.
func archiveXMLText(content []byte) ([]byte, error) {
	decoder := xml.NewDecoder(bytes.NewReader(content))
	var text strings.Builder
	depth, roots := 0, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("归档 XML 无效")
		}
		switch value := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(value)) != "" {
				return nil, errors.New("归档 XML 根元素外存在正文")
			}
			text.Write(value)
		}
	}
	if roots != 1 || depth != 0 {
		return nil, errors.New("归档 XML 必须包含一个完整根元素")
	}
	return []byte(text.String()), nil
}

func validateAssertions(assertions []core.ArtifactAssertion) error {
	if len(assertions) > 64 {
		return errors.New("最多 64 个内容断言")
	}
	problems := &validation.Error{}
	for i, a := range assertions {
		add := func(field, code, actual, expected, hint string) {
			problems.Add(validation.Issue{Path: fmt.Sprintf("$.arguments.assertions[%d].%s", i, field), Code: code, Actual: actual, Expected: expected, Hint: hint})
		}
		if _, err := core.FilePath(a.Path); err != nil {
			add("path", "path", "无效路径", "项目内相对文件路径", err.Error())
		}
		if strings.TrimSpace(a.Requirement) == "" {
			add("requirement", "required", "空字符串或缺失", "计划验收条件原文", "填写 requirement，不要改写原验收条件")
		}
		archive := strings.HasPrefix(a.Kind, "archive_")
		if a.MemberMatch != "" && !archive {
			add("member_match", "condition", "非归档断言声明了匹配方式", "仅用于 archive_* 断言", "移除此字段，或选择符合实际检查目标的归档断言")
		}
		switch a.Kind {
		case "nonempty":
		case "json_equals":
			if !json.Valid([]byte(a.Expected)) {
				add("expected", "json", "无效 JSON 文本", "完整有效 JSON 文本", "json_equals 比较实际 JSON 内容，修正 expected 中的 JSON")
			}
		case "text_contains", "text_not_contains":
			if a.Expected == "" {
				add("expected", "required", "空字符串或缺失", "非空字符串", a.Kind+" 缺少 expected；填写要包含或排除的具体内容")
			}
		case "archive_count", "archive_text_contains", "archive_text_not_contains", "archive_xml_contains", "archive_xml_not_contains":
			if a.Member == "" {
				add("member", "required", "空字符串或缺失", "归档成员名称或 glob 模式", a.Kind+" 缺少 member；具体名称使用 member_match=exact，通配模式使用 glob")
			}
			switch a.MemberMatch {
			case "exact":
			case "", "glob":
				if _, err := path.Match(a.Member, ""); err != nil {
					add("member", "pattern", "无效 glob 模式", "合法通配模式或 exact 字面名称", "名称含 [ ] * ? 时可使用 member_match=exact；glob 中特殊字符须转义")
				}
			default:
				add("member_match", "enum", "未知匹配方式", "exact 或 glob", "填写 exact 或 glob；省略时保留 glob 语义")
			}
			if a.Kind == "archive_count" && a.Count < 1 {
				add("count", "range", fmt.Sprint(a.Count), "正整数", "archive_count 的 count 必须为正整数")
			}
			if a.Kind != "archive_count" && a.Expected == "" {
				add("expected", "required", "空字符串或缺失", "非空字符串", a.Kind+" 缺少 expected；反向检查也必须声明具体禁止内容")
			}
		default:
			add("kind", "enum", "不支持的内容断言类型", "工具定义中列出的断言类型", "按包含、排除或数量条件选择对应 kind")
		}
	}
	if len(problems.Issues) > 0 {
		return problems
	}
	return nil
}

func artifactChecks(files map[string][]byte, assertions []core.ArtifactAssertion) []core.Check {
	var checks []core.Check
	for i, a := range assertions {
		c := core.Check{Name: fmt.Sprintf("artifact_%d:%s", i+1, a.Kind), Status: "passed", Detail: "实际字节符合声明断言", Requirement: a.Requirement, Paths: []string{a.Path}}
		data, exists := files[a.Path]
		var err error
		if !exists {
			err = errors.New("断言目标文件不存在")
		} else if strings.TrimSpace(a.Requirement) == "" {
			err = errors.New("内容断言必须说明对应验收条件")
		} else {
			err = assertArtifact(data, a)
		}
		if err != nil {
			c.Status = "failed"
			c.Detail = err.Error()
		}
		checks = append(checks, c)
	}
	return checks
}

func scopedAcceptance(p *core.Project, t *core.Task) error {
	if t == nil || t.Plan == nil || t.Plan.Delivery == nil {
		return nil
	}
	for _, criterion := range t.Plan.Delivery.Criteria {
		for _, name := range criterion.Paths {
			passed := false
			for _, r := range p.Reports {
				if !reportCurrent(p, t, r) || r.Status != "passed" {
					continue
				}
				for _, check := range r.Checks {
					if check.Status == "passed" && check.Requirement == criterion.Requirement && slices.Contains(check.Paths, name) {
						passed = true
					}
				}
			}
			if !passed {
				return fmt.Errorf("缺少当前交付证据：%s（%s）", criterion.Requirement, name)
			}
		}
	}
	return nil
}
