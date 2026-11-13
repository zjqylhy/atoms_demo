package validation

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// The standard decoder owns syntax acceptance. This stack only locates its
// failure in already accepted tokens, without guessing keys from broken text.
type jsonContainer struct {
	kind  json.Delim
	path  string
	key   string
	value bool
	count int
}

type jsonPrefix struct {
	stack      []jsonContainer
	end        int64
	lastPath   string
	lastString bool
}

func (p jsonPrefix) valuePath(root string) string {
	if len(p.stack) == 0 {
		return root
	}
	top := p.stack[len(p.stack)-1]
	if top.kind == '[' {
		return fmt.Sprintf("%s[%d]", top.path, top.count)
	}
	if top.value {
		return Child(top.path, top.key)
	}
	return top.path
}

func inspectPrefix(text, root string) jsonPrefix {
	d := json.NewDecoder(strings.NewReader(text))
	d.UseNumber()
	p := jsonPrefix{}
	for {
		token, err := d.Token()
		if err != nil {
			return p
		}
		path := p.valuePath(root)
		if delim, ok := token.(json.Delim); ok {
			if delim == '{' || delim == '[' {
				// Bound diagnostic work on pathological nesting and long keys.
				if len(p.stack) >= 128 || utf8.RuneCountInString(path) > 256 {
					return jsonPrefix{}
				}
				p.stack = append(p.stack, jsonContainer{kind: delim, path: path})
				p.end, p.lastString = d.InputOffset(), false
				continue
			}
			path = p.stack[len(p.stack)-1].path
			p.stack = p.stack[:len(p.stack)-1]
		} else if len(p.stack) > 0 {
			top := &p.stack[len(p.stack)-1]
			if top.kind == '{' && !top.value {
				top.key, _ = token.(string)
				top.value = true
				p.end, p.lastString = d.InputOffset(), false
				continue
			}
		}
		p.lastPath = path
		_, p.lastString = token.(string)
		p.end = d.InputOffset()
		if len(p.stack) == 0 {
			return p
		}
		top := &p.stack[len(p.stack)-1]
		top.value = false
		top.count++
	}
}

func syntaxIssue(text, root string, failure error) Issue {
	prefix := inspectPrefix(text, root)
	issue := Issue{Path: shorten(prefix.valuePath(root), 256), Code: "syntax", Actual: "无效 JSON", Expected: "一个完整 JSON 值", Hint: "检查引号、逗号和括号，重新生成完整 JSON"}
	offset := int64(len(text)) + 1
	var syntax *json.SyntaxError
	if errors.As(failure, &syntax) {
		offset = syntax.Offset
		issue.Actual = fmt.Sprintf("JSON 语法错误（第 %d 字节）", offset)
		describeSyntax(&issue, prefix, syntax.Error(), text, offset)
	} else if errors.Is(failure, io.EOF) {
		issue.Code, issue.Actual = "empty", "空白回复"
		issue.Reason = "回复中没有 JSON 值"
	} else if errors.Is(failure, io.ErrUnexpectedEOF) {
		issue.Actual = "JSON 尚未闭合"
		describeIncomplete(&issue, prefix, text)
	} else {
		issue.Reason = "解码器拒绝该值：" + shorten(failure.Error(), 160)
		var valueError *json.UnmarshalTypeError
		if errors.As(failure, &valueError) {
			offset = valueError.Offset
		}
	}
	locateIssue(&issue, text, offset)
	return issue
}

func describeSyntax(issue *Issue, p jsonPrefix, message, text string, offset int64) {
	switch {
	case strings.HasSuffix(message, "after object key:value pair"):
		issue.Expected = "逗号 , 或对象结束符 }"
		issue.Reason = "对象字段值之后遇到非法字符"
		if len(p.stack) > 0 {
			issue.Path = p.stack[len(p.stack)-1].path
		}
		if offset > 0 && offset <= int64(len(text)) && text[offset-1] == ']' {
			issue.Reason = "当前对象尚未关闭，却遇到了数组结束符 ]"
			issue.Hint = "先关闭当前对象，再关闭外层数组；按嵌套顺序检查括号"
		} else {
			issue.Hint = "检查字段之间是否缺少逗号；若后续文字属于前面的字符串，可能有未转义的双引号"
			if p.lastString {
				issue.Path = shorten(p.lastPath, 256)
			}
		}
	case strings.HasSuffix(message, "after object key"):
		issue.Expected = "字段名后的冒号 :"
		issue.Reason = "已读到对象字段名，但接下来不是冒号"
		issue.Hint = "检查字段名与值之间的冒号；也可能是前面多余或未转义的双引号改变了字符串边界"
	case strings.HasSuffix(message, "after array element"):
		issue.Expected = "逗号 , 或数组结束符 ]"
		issue.Reason = "数组元素之后遇到非法字符"
		issue.Hint = "检查元素间的逗号和数组结束符；若后续文字属于字符串，检查内部双引号是否已转义"
		if len(p.stack) > 0 {
			issue.Path = p.stack[len(p.stack)-1].path
		}
	case strings.HasSuffix(message, "looking for beginning of object key string"):
		issue.Expected = "使用双引号包围的字段名"
		issue.Reason = "对象字段名必须以双引号开始，逗号后不能直接结束对象"
		issue.Hint = "检查引号、逗号和括号；字段名使用双引号，并删除末尾多余的逗号"
	case strings.HasSuffix(message, "looking for beginning of value"):
		issue.Expected = "字符串、数字、对象、数组、true、false 或 null"
		issue.Reason = "此处需要 JSON 值，实际字符不能开始一个值"
		issue.Hint = "补齐缺失的值；若在结束括号前出现逗号，检查是否多写了末尾逗号"
	case strings.Contains(message, "in \\u hexadecimal character escape") || strings.Contains(message, "invalid escape sequence `\\u"):
		issue.Expected = `\u 后连续四位十六进制数字`
		issue.Reason = "Unicode 转义包含非十六进制字符"
		issue.Hint = `检查 \uXXXX 中四位数字是否完整且仅包含 0-9、a-f、A-F`
	case strings.Contains(message, "in string escape code"), strings.Contains(message, "invalid escape sequence"):
		issue.Expected = `合法转义：\"、\\、\/、\b、\f、\n、\r、\t 或 \uXXXX`
		issue.Reason = "反斜杠后不是合法的 JSON 转义字符"
		issue.Hint = "字符串中的普通反斜杠需写成两个反斜杠；不要改变原始内容的含义"
	case strings.Contains(message, "in string literal"), strings.HasSuffix(message, "in string"):
		issue.Expected = "经过 JSON 转义的字符串内容"
		issue.Reason = "字符串中出现未转义的控制字符"
		issue.Hint = `换行和制表符分别写成 \n 和 \t；内部双引号写成 \"`
	case strings.Contains(message, "numeric literal"), strings.Contains(message, "exponent of numeric literal"):
		issue.Expected = "完整的 JSON 数字"
		issue.Reason = "数字的小数、符号或指数格式不完整"
		issue.Hint = "检查负号、小数点或指数符号之后是否缺少数字"
	case strings.Contains(message, "in literal"):
		issue.Expected = "完整的小写 true、false 或 null"
		issue.Reason = "JSON 固定值拼写错误"
		issue.Hint = "核对完整拼写，不使用 True、False、None 等其他语言的写法"
	default:
		issue.Reason = "解析器报告：" + shorten(message, 160)
	}
}

func describeIncomplete(issue *Issue, p jsonPrefix, text string) {
	tail := strings.TrimSpace(text[p.end:])
	value := strings.TrimSpace(strings.TrimLeft(tail, ":,"))
	switch {
	case strings.HasPrefix(value, `"`):
		issue.Expected = "完整字符串及结束双引号"
		issue.Reason = "输入在字符串或字符串转义尚未结束时结束"
		issue.Hint = "检查结束双引号、反斜杠和 Unicode 转义是否完整，再检查外层括号"
	case value != "":
		issue.Expected = "完整的 JSON 值"
		issue.Reason = "输入在字段名或值尚未写完时结束"
		issue.Hint = "先补齐当前字符串、数字或固定值，再按嵌套顺序检查外层括号"
	case len(p.stack) > 0:
		top := p.stack[len(p.stack)-1]
		if top.value {
			issue.Expected = "完整的字段值"
			issue.Reason = "字段名之后缺少完整的值"
			issue.Hint = "核对冒号并补齐字段值；不能只补结束括号"
		} else if strings.HasPrefix(tail, ",") {
			issue.Expected = "逗号后的下一个字段或数组元素"
			issue.Reason = "输入在分隔逗号之后结束"
			issue.Hint = "补齐下一项，或删除多余的末尾逗号，再检查括号"
		} else {
			issue.Path = top.path
			closers := make([]string, 0, len(p.stack))
			for i := len(p.stack) - 1; i >= 0; i-- {
				closer := "}"
				if p.stack[i].kind == '[' {
					closer = "]"
				}
				closers = append(closers, closer)
			}
			issue.Expected = "由内到外的结束符：" + strings.Join(closers, " ")
			issue.Reason = "输入已结束，仍有对象或数组尚未关闭"
			issue.Hint = "核对内容是否完整，再按上述嵌套顺序闭合；不要通过闭合括号掩盖缺失内容"
		}
	default:
		issue.Reason = "输入在一个 JSON 值完成之前结束"
	}
}

func locateIssue(issue *Issue, text string, offset int64) {
	index := min(max(0, int(offset)-1), len(text))
	issue.Offset = int64(index + 1)
	for index > 0 && index < len(text) && !utf8.RuneStart(text[index]) {
		index--
	}
	issue.Line = strings.Count(text[:index], "\n") + 1
	lineStart := strings.LastIndex(text[:index], "\n") + 1
	issue.Column = utf8.RuneCountInString(text[lineStart:index]) + 1
	before, after := []rune(text[:index]), []rune(text[index:])
	left, right, marker := "", "", "EOF"
	if len(before) > 80 {
		before, left = before[len(before)-80:], "…"
	}
	if len(after) > 0 {
		marker, after = string(after[0]), after[1:]
	}
	if len(after) > 80 {
		after, right = after[:80], "…"
	}
	issue.Excerpt = left + string(before) + "⟦" + marker + "⟧" + string(after) + right
	issue.source = text
}
