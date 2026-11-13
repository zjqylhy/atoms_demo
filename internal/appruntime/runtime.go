package appruntime

import (
	_ "embed"
	"strings"
)

//go:embed runtime.js
var SDK string

func Inject(html string) string {
	script := "<script>" + SDK + "</script>"
	if pos := strings.Index(strings.ToLower(html), "<head>"); pos >= 0 {
		return html[:pos+6] + script + html[pos+6:]
	}
	return script + html
}
