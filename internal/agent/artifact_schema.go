package agent

func objectProp(fields map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": fields, "required": required, "additionalProperties": false}
}
func objectList(fields map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "array", "items": objectProp(fields, required...)}
}
func assertionsProp() map[string]any {
	return objectList(map[string]any{
		"path":        prop("string", "核对的文件相对路径"),
		"kind":        map[string]any{"type": "string", "enum": []string{"nonempty", "text_contains", "json_equals", "archive_count", "archive_text_contains", "archive_xml_contains"}, "description": "archive_count 必填 member/count；归档内容检查必填 member/expected；archive_xml_contains 只匹配元素文字，XML 属性用 archive_text_contains"},
		"requirement": prop("string", "对应计划验收条件的原文"),
		"expected":    prop("string", "期望内容；JSON 比较填写完整 JSON 文本"),
		"member":      prop("string", "归档成员名称或 glob 模式；XML 检查提取实际文本后匹配"),
		"count":       prop("integer", "archive_count 期望成员数量"),
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
