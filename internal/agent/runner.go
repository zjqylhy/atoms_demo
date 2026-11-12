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
	"strings"
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

func runnerFailureCode(status string, checks []core.Check, details map[string]any) string {
	if status != "failed" {
		return ""
	}
	if code, _ := details["failure_code"].(string); code == "container_failed" || code == "runner_interrupted" {
		return code
	}
	// Reports stored before failure codes used a single check for these cases.
	if len(checks) == 1 {
		switch checks[0].Name {
		case "container":
			return "container_failed"
		case "interrupted":
			return "runner_interrupted"
		}
	}
	return ""
}

type runnerClient struct {
	http    *http.Client
	baseURL string
	token   string
}

type runnerReply struct {
	httpStatus int
	ID         string         `json:"id"`
	Digest     string         `json:"digest"`
	Status     string         `json:"status"`
	Checks     []core.Check   `json:"checks"`
	Details    map[string]any `json:"details"`
	Code       string         `json:"code"`
	Error      string         `json:"error"`
}

func (r runnerReply) is(status int, code string) bool {
	return r.httpStatus == status && r.Code == code
}

func (c runnerClient) request(ctx context.Context, method, route string, body []byte) (runnerReply, error) {
	if method == http.MethodGet {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+route, bytes.NewReader(body))
	if err != nil {
		return runnerReply{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return runnerReply{}, err
	}
	defer resp.Body.Close()
	reply := runnerReply{httpStatus: resp.StatusCode}
	const maxReport = 2 * 1024 * 1024
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxReport+1))
	if err != nil {
		return reply, err
	}
	if len(b) > maxReport {
		return reply, errors.New("执行报告过大")
	}
	err = json.Unmarshal(b, &reply)
	return reply, err
}

func (c runnerClient) result(ctx context.Context, jobID, digest string, body []byte, onWait func(string) error) (runnerReply, error) {
	backoff := [...]time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}
	lookup, maySubmit := true, true
	waits := 0
	for {
		var reply runnerReply
		var err error
		if lookup {
			reply, err = c.request(ctx, http.MethodGet, "/jobs/"+jobID, nil)
		} else {
			reply, err = c.request(ctx, http.MethodPost, "/jobs", body)
		}
		if ctx.Err() != nil {
			return runnerReply{}, ctx.Err()
		}
		if err == nil && reply.httpStatus == http.StatusOK {
			if reply.ID == jobID && reply.Digest == digest && (reply.Status == "passed" || reply.Status == "failed") && len(reply.Checks) > 0 {
				return reply, nil
			}
			err = errors.New("执行报告与当前作业或产物不匹配")
		}
		reason := "执行结果尚未确认，正在核对原作业"
		exhausted := "执行结果尚未确认，进度已保留，继续时会核对原作业"
		switch {
		case err == nil && lookup && reply.is(http.StatusNotFound, "job_not_found") && maySubmit:
			lookup = false
			continue
		case err == nil && !lookup && reply.is(http.StatusTooManyRequests, "job_busy"):
			reason = "执行节点忙，等待后重试"
			exhausted = "执行节点仍忙，进度已保留，请稍后继续"
		case err != nil || reply.httpStatus >= 500 || reply.is(http.StatusConflict, "job_running") || (lookup && reply.is(http.StatusNotFound, "job_not_found")):
			if !lookup {
				// A submission may already have been accepted. Only query from now on.
				lookup, maySubmit = true, false
				continue
			}
			if reply.is(http.StatusConflict, "job_running") {
				maySubmit = false
			}
		default:
			return reply, nil
		}
		if waits == len(backoff) {
			return runnerReply{}, errors.New(exhausted)
		}
		if err := onWait(fmt.Sprintf("%s（%d/%d）", reason, waits+1, len(backoff))); err != nil {
			return runnerReply{}, err
		}
		if err := waitForRetry(ctx, backoff[waits]); err != nil {
			return runnerReply{}, err
		}
		waits++
	}
}

func (e *Engine) executeRunner(ctx context.Context, s scope, c core.Call, args map[string]any, p *core.Project) (string, error) {
	if e.Config.RunnerURL == "" || e.Config.RunnerToken == "" {
		return "", errors.New("执行服务尚未配置，请由部署方配置受限执行节点")
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
			if d["run_id"] == s.Run && runnerFailureCode(report.Status, report.Checks, d) == "" {
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
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := runnerClient{
		http: &http.Client{Timeout: 150 * time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("执行服务重定向被拒绝")
		}},
		baseURL: strings.TrimRight(e.Config.RunnerURL, "/"),
		token:   e.Config.RunnerToken,
	}
	role := p.Tasks[s.Task].Frames[s.Frame].Role
	result, err := client.result(ctx, jobID, digest, []byte(core.JSON(payload)), func(message string) error {
		return e.noteWait(s, "runner_wait", role, message)
	})
	if err != nil {
		return "", err
	}
	if result.httpStatus != http.StatusOK {
		rejection := fmt.Errorf("执行服务 HTTP %d：%s", result.httpStatus, e.Config.Redact(result.Error))
		if result.httpStatus == http.StatusBadRequest || result.httpStatus == http.StatusRequestEntityTooLarge {
			return e.record(s, c, nil, func(*core.Project) (any, error) { return nil, rejection })
		}
		return "", rejection
	}
	if result.Details == nil {
		result.Details = map[string]any{}
	}
	failureCode := runnerFailureCode(result.Status, result.Checks, result.Details)
	if failureCode != "" {
		result.Details["failure_code"] = failureCode
		for i := range result.Checks {
			result.Checks[i].Detail = e.Config.Redact(result.Checks[i].Detail)
		}
	}
	result.Details["run_id"] = s.Run
	kind := "run"
	if c.Function.Name == "test_preview" {
		kind = "browser"
	} else {
		result.Details["execution"] = executionType(args)
	}
	report := core.Report{ID: jobID, Kind: kind, Digest: digest, At: core.Now(), Status: result.Status, Checks: result.Checks, Details: json.RawMessage(core.JSON(result.Details))}
	saved, err := e.record(s, c, nil, func(p *core.Project) (any, error) {
		if p.Files.Digest() != digest {
			return nil, errors.New("执行期间文件变化，报告不能用于新版本")
		}
		p.Reports = append(p.Reports, report)
		result := map[string]any{"id": report.ID, "status": report.Status, "checks": report.Checks}
		if failureCode != "" {
			result["failure_code"] = failureCode
			result["next_action"] = "执行环境故障，请在环境恢复后重新检查当前产物"
		}
		return result, nil
	})
	if err == nil && failureCode != "" {
		return "", fmt.Errorf("执行环境故障，进度已保留：%s", result.Checks[0].Detail)
	}
	return saved, err
}
