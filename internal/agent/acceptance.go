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
	case "text_contains":
		if a.Expected == "" || !utf8.Valid(data) || !strings.Contains(string(data), a.Expected) {
			return errors.New("文本未包含期望内容")
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
	case "archive_count", "archive_text_contains", "archive_xml_contains":
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
		for _, file := range reader.File {
			if file.UncompressedSize64 > 16*1024*1024-total {
				return errors.New("归档解压后大小超限")
			}
			total += file.UncompressedSize64
			ok, err := path.Match(a.Member, file.Name)
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
			if a.Kind == "archive_xml_contains" {
				decoder := xml.NewDecoder(bytes.NewReader(content))
				var text strings.Builder
				for {
					token, err := decoder.Token()
					if err == io.EOF {
						break
					}
					if err != nil {
						return errors.New("归档 XML 无效")
					}
					if chars, ok := token.(xml.CharData); ok {
						text.Write(chars)
					}
				}
				content = []byte(text.String())
			}
			found = found || (a.Expected != "" && utf8.Valid(content) && strings.Contains(string(content), a.Expected))
		}
		if a.Kind == "archive_count" {
			if a.Count < 1 || matched != a.Count {
				return fmt.Errorf("成员数 %d，期望 %d", matched, a.Count)
			}
		} else if !found {
			return errors.New("归档成员没有期望内容")
		}
	default:
		return errors.New("不支持的内容断言")
	}
	return nil
}

func validateAssertions(assertions []core.ArtifactAssertion) error {
	if len(assertions) > 64 {
		return errors.New("最多 64 个内容断言")
	}
	for _, a := range assertions {
		if _, err := core.FilePath(a.Path); err != nil {
			return err
		}
		if strings.TrimSpace(a.Requirement) == "" {
			return errors.New("断言缺少 requirement（对应验收条件）")
		}
		switch a.Kind {
		case "nonempty":
		case "json_equals":
			if !json.Valid([]byte(a.Expected)) {
				return errors.New("json_equals 的 expected 必须是完整有效 JSON")
			}
		case "text_contains":
			if a.Expected == "" {
				return errors.New("text_contains 缺少 expected")
			}
		case "archive_count", "archive_text_contains", "archive_xml_contains":
			if a.Member == "" {
				return fmt.Errorf("%s 缺少 member（归档成员名称或 glob 模式）", a.Kind)
			}
			if _, err := path.Match(a.Member, ""); err != nil {
				return errors.New("member 匹配模式无效")
			}
			if a.Kind == "archive_count" && a.Count < 1 {
				return errors.New("archive_count 的 count 必须为正整数")
			}
			if a.Kind != "archive_count" && a.Expected == "" {
				return fmt.Errorf("%s 缺少 expected", a.Kind)
			}
		default:
			return errors.New("不支持的内容断言类型")
		}
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
