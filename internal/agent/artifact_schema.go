package agent

func objectProp(fields map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": fields, "required": required, "additionalProperties": false}
}
func objectList(fields map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "array", "items": objectProp(fields, required...)}
}
func assertionsProp() map[string]any {
	return objectList(map[string]any{
		"path":         prop("string", "核对的文件相对路径"),
		"kind":         map[string]any{"type": "string", "enum": []string{"nonempty", "text_contains", "text_not_contains", "json_equals", "archive_count", "archive_text_contains", "archive_text_not_contains", "archive_xml_contains", "archive_xml_not_contains"}, "description": "archive_count 必填 member/count；归档内容检查必填 member/expected；archive_xml_* 只检查元素文字，XML 属性用 archive_text_*。*_not_contains 要求所有选中内容都不包含 expected，不能用正向断言失败代替；归档零匹配仍失败"},
		"requirement":  prop("string", "对应计划验收条件的原文，逐字一致；同一条件可对应多条断言，不要改写条件措辞"),
		"expected":     prop("string", "包含或不得包含的内容，不得为空；JSON 比较填写完整 JSON 文本"),
		"member":       prop("string", "归档成员选择器；由 member_match 指定名称或通配模式"),
		"member_match": map[string]any{"type": "string", "enum": []string{"exact", "glob"}, "description": "归档成员匹配方式：exact 按完整名称匹配，名称含 [ ] * ? 时也按字面处理；glob 为通配模式，默认 glob。具体文件建议明确指定 exact"},
		"count":        prop("integer", "archive_count 期望成员数量"),
	}, "path", "kind", "requirement")
}
func outputsProp() map[string]any {
	return objectList(map[string]any{"path": prop("string", "脚本工作目录下生成的相对路径，回收入项目同一路径"),
		"format":   map[string]any{"type": "string", "enum": []string{"text", "binary"}},
		"old_hash": prop("string", "替换已有文件的 hash，新文件填空字符串")}, "path", "format", "old_hash")
}
func deliveryProp() map[string]any {
	return objectProp(map[string]any{"targets": listProp("string"), "inputs": listProp("string"), "preserve": listProp("string"),
		"criteria": objectList(map[string]any{"requirement": prop("string", "必须对应 acceptance 的原文，不得漏掉用户要求"), "paths": listProp("string")}, "requirement", "paths")}, "targets", "inputs", "preserve", "criteria")
}
