package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mockatoms/internal/core"
	"net/http"
	"net/url"
	"path"
	"time"
)

const maxRunRepairs = 5

func executionType(args map[string]any) string {
	switch str(args, "kind") {
	case "go":
		return "go"
	case "web":
		return "web"
	}
	switch path.Ext(str(args, "entry")) {
	case ".py":
		return "python"
	case ".ts", ".mts", ".cts", ".tsx":
		return "typescript"
	default:
		return "javascript"
	}
}

func reportExecution(r core.Report) string {
	if r.Kind != "run" {
		return ""
	}
	var details struct {
		Execution string `json:"execution"`
	}
	_ = json.Unmarshal(r.Details, &details)
	return details.Execution
}

func (e *Engine) executeRunner(ctx context.Context, s scope, c core.Call, args map[string]any, p *core.Project) (string, error) {
	if e.Config.RunnerURL == "" || e.Config.RunnerToken == "" {
		return e.record(s, c, nil, func(*core.Project) (any, error) {
			return nil, errors.New("执行服务尚未配置，未运行；请由部署方配置受限执行节点")
		})
	}
	endpoint, err := url.Parse(e.Config.RunnerURL)
	if err != nil || endpoint.User != nil || endpoint.Host == "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return "", errors.New("执行服务地址无效")
	}
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && (endpoint.Hostname() == "127.0.0.1" || endpoint.Hostname() == "localhost")) {
		return "", errors.New("远程执行服务必须使用 HTTPS")
	}
	failures := 0
	for _, report := range p.Reports {
		if report.Status == "failed" && (report.Kind == "run" || report.Kind == "browser") {
			var d map[string]any
			_ = json.Unmarshal(report.Details, &d)
			if d["run_id"] == s.Run {
				failures++
			}
		}
	}
	// The initial failed execution does not use one of the repair attempts.
	if failures > maxRunRepairs {
		return "", fmt.Errorf("自动运行修复已达到 %d 次上限，请查看失败记录后调整任务", maxRunRepairs)
	}
	files, err := e.Store.Files(s.Owner, p.Files)
	if err != nil {
		return "", err
	}
	jobID := core.Hash([]byte(s.Task + "/" + s.Frame + "/" + c.ID))
	digest := p.Files.Digest()
	payload := map[string]any{"id": jobID, "digest": digest, "files": files, "action": c.Function.Name, "parameters": args}
	body := []byte(core.JSON(payload))
	req, err := http.NewRequestWithContext(ctx, "POST", e.Config.RunnerURL+"/jobs", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+e.Config.RunnerToken)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 150 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("执行服务重定向被拒绝") }}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("执行结果未知，保留工作；继续时将按相同任务标识核对：%s", e.Config.Redact(err.Error()))
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024+1))
	if err != nil {
		return "", err
	}
	if len(b) > 2*1024*1024 {
		return "", errors.New("执行报告过大")
	}
	if resp.StatusCode != 200 {
		return e.record(s, c, nil, func(*core.Project) (any, error) {
			return nil, fmt.Errorf("执行服务 HTTP %d：%s", resp.StatusCode, e.Config.Redact(string(b)))
		})
	}
	var result struct {
		ID      string         `json:"id"`
		Digest  string         `json:"digest"`
		Status  string         `json:"status"`
		Checks  []core.Check   `json:"checks"`
		Details map[string]any `json:"details"`
	}
	if err = json.Unmarshal(b, &result); err != nil {
		return "", errors.New("执行服务报告格式无效")
	}
	if result.ID != jobID || result.Digest != digest || (result.Status != "passed" && result.Status != "failed") || len(result.Checks) == 0 {
		return "", errors.New("执行报告与当前产物不匹配")
	}
	if result.Details == nil {
		result.Details = map[string]any{}
	}
	result.Details["run_id"] = s.Run
	kind := "run"
	if c.Function.Name == "test_preview" {
		kind = "browser"
	} else {
		result.Details["execution"] = executionType(args)
	}
	report := core.Report{ID: jobID, Kind: kind, Digest: digest, At: core.Now(), Status: result.Status, Checks: result.Checks, Details: json.RawMessage(core.JSON(result.Details))}
	return e.record(s, c, nil, func(p *core.Project) (any, error) {
		if p.Files.Digest() != digest {
			return nil, errors.New("执行期间文件变化，报告不能用于新版本")
		}
		p.Reports = append(p.Reports, report)
		return map[string]any{"id": report.ID, "status": report.Status, "checks": report.Checks}, nil
	})
}
