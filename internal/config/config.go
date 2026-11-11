package config

import (
	"errors"
	"mockatoms/internal/core"
	"net/url"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	ProviderTPM                                      int
	ToolMode                                         string
	Addr, DataDir, WebDir, PublicURL                 string
	Protocol, BaseURL, Model, APIKey, Revision       string
	RunnerURL, RunnerToken                           string
	MaxCalls, MaxTools, MaxConcurrent, RunSeconds    int
	DailyCalls, WorkspaceDailyCalls, MaxOutputTokens int
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func number(k string, d int) int {
	v, e := strconv.Atoi(os.Getenv(k))
	if e != nil || v <= 0 {
		return d
	}
	return v
}
func Load() Config {
	return Config{ProviderTPM: number("ATOMS_LLM_TPM", 40000), ToolMode: env("ATOMS_LLM_TOOL_MODE", "native"), Addr: env("ATOMS_ADDR", "127.0.0.1:8090"), DataDir: env("ATOMS_DATA_DIR", "data"), WebDir: env("ATOMS_WEB_DIR", "web/dist"), PublicURL: os.Getenv("ATOMS_PUBLIC_URL"), Protocol: env("ATOMS_LLM_PROTOCOL", "chat_completions"), BaseURL: strings.TrimRight(os.Getenv("ATOMS_LLM_BASE_URL"), "/"), Model: os.Getenv("ATOMS_LLM_MODEL"), APIKey: os.Getenv("ATOMS_LLM_API_KEY"), Revision: env("ATOMS_LLM_CONFIG_REVISION", core.ID()), RunnerURL: strings.TrimRight(os.Getenv("ATOMS_RUNNER_URL"), "/"), RunnerToken: os.Getenv("ATOMS_RUNNER_TOKEN"), MaxCalls: number("ATOMS_MAX_CALLS", 24), MaxTools: number("ATOMS_MAX_TOOLS", 64), MaxConcurrent: number("ATOMS_MAX_CONCURRENT", 2), RunSeconds: number("ATOMS_RUN_SECONDS", 600), DailyCalls: number("ATOMS_DAILY_CALLS", 200), WorkspaceDailyCalls: number("ATOMS_WORKSPACE_DAILY_CALLS", 80), MaxOutputTokens: number("ATOMS_MAX_OUTPUT_TOKENS", 4096)}
}
func (c Config) ModelError() error {
	if c.APIKey == "" || c.Model == "" || c.BaseURL == "" {
		return errors.New("模型服务尚未配置，请由部署方设置环境变量")
	}
	if c.Protocol != "chat_completions" && c.Protocol != "responses" {
		return errors.New("模型协议不支持")
	}
	if c.ToolMode != "" && c.ToolMode != "native" && c.ToolMode != "json" {
		return errors.New("工具调用协议不支持")
	}
	if c.ToolMode == "json" && c.Protocol != "chat_completions" {
		return errors.New("JSON 工具适配只支持 Chat Completions")
	}
	u, e := url.Parse(c.BaseURL)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("模型地址格式无效")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost")) {
		return errors.New("模型服务要求 HTTPS；本地测试可使用回环 HTTP")
	}
	return nil
}
func (c Config) Ref() core.ModelConfigRef {
	return core.ModelConfigRef{Protocol: c.Protocol, BaseURL: c.BaseURL, Model: c.Model, Revision: c.Revision, ToolMode: c.ToolMode}
}
func (c Config) Redact(s string) string {
	for _, secret := range []string{c.APIKey, c.RunnerToken} {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "[REDACTED]")
		}
	}
	return s
}
