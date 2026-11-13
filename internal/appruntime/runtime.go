package appruntime

import (
	_ "embed"
	"mockatoms/internal/core"
	"strings"
)

//go:embed runtime.js
var SDK string

//go:embed offline.js
var offline string

func Offline(html string, c *core.AppContract) string {
	if c == nil {
		return "<!-- 离线副本：此旧版应用没有持久化契约，页面修改不保证保留。 -->" + html
	}
	config := core.JSON(map[string]any{"key": core.Hash([]byte(html)), "schema": c.Schema, "initial": c.Initial})
	script := "<script>" + strings.Replace(offline, "OFFLINE_CONFIG", config, 1) + "</script>"
	if pos := strings.Index(strings.ToLower(html), "<head>"); pos >= 0 {
		return html[:pos+6] + script + html[pos+6:]
	}
	return script + html
}

func Inject(html string) string {
	script := "<script>" + SDK + "</script>"
	if pos := strings.Index(strings.ToLower(html), "<head>"); pos >= 0 {
		return html[:pos+6] + script + html[pos+6:]
	}
	return script + html
}
