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

type externalPendingError struct{ job string }

func (e *externalPendingError) Error() string {
	return "执行结果尚未确认；原作业 " + e.job + " 已保留，只能核对原作业，不能重新执行"
}

// Recovery is owned by the execution adapter. The loop sees only whether a
// recovery action was handled, while unresolved work is returned as evidence.
func (e *Engine) recoverExternal(ctx context.Context, s scope, p *core.Project) (bool, error) {
	t := p.Tasks[s.Task]
	action := t.RunnerAction
	if s.Frame != t.Root || action == nil || action.LastLookupRun == s.Run {
		return false, nil
	}
	needed := action.InstructionVersion != len(t.InstructionMessages) || (action.RunID != "" && action.RunID != s.Run) ||
		(action.FrameID != t.Root && !strings.HasPrefix(action.FrameID, t.Root+"/"))
	if !needed {
		return false, nil
	}
	err := e.recoverRunner(ctx, s, p, action)
	var pending *externalPendingError
	if !errors.As(err, &pending) {
		return true, err
	}
	_, saveErr := e.Store.Mutate(s.Owner, s.Project, nil, func(p *core.Project) error {
		if err := p.Fence(s.Run, s.Epoch); err != nil {
			return err
		}
		t := p.Tasks[s.Task]
		t.LastHandoff = &core.DelegationResult{Status: "blocked", Reason: pending.Error(), Evidence: []string{action.FrameID + "/" + action.CallID}, Artifacts: core.Clone(p.Files), Todo: []string{"保留原作业标识，稍后继续核对；可先诊断其他输入"}}
		t.Frames[t.Root].Messages = append(t.Frames[t.Root].Messages, core.ModelMessage{Role: "user", Content: "执行器恢复事实（非用户新指令）：" + core.JSON(t.LastHandoff)})
		p.Event("handoff", "system", pending.Error(), t.LastHandoff)
		return nil
	})
	return true, saveErr
}

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
	Artifacts  []runnerArtifact `json:"artifacts"`
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
	const maxReport = 4 * 1024 * 1024
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

func (c runnerClient) result(ctx context.Context, jobID, digest string, body []byte, lookupOnly bool, onWait func(string) error) (runnerReply, error) {
	backoff := [...]time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}
	lookup, maySubmit := true, !lookupOnly
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
		case err == nil && lookupOnly && waits == 0 && reply.is(http.StatusNotFound, "job_not_found"):
			return reply, nil
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
	action := core.RunnerAction{Inputs: core.Clone(p.Files), RunID: s.Run, Epoch: s.Epoch, Call: &c,
		EvidenceVersion: evidenceVersion, EvidencePlan: core.Clone(p.Tasks[s.Task].Plan),
		FrameID: s.Frame, CallID: c.ID, InstructionVersion: s.InstructionVersion,
		JobID: core.Hash([]byte(s.Task + "/" + s.Frame + "/" + c.ID)),
	}
	if plan := p.Tasks[s.Task].Plan; plan != nil {
		action.PlanRevision = plan.Revision
	}
	if pending := p.Tasks[s.Task].RunnerAction; pending != nil {
		if pending.FrameID == s.Frame && pending.CallID == c.ID {
			if pending.Unknown && pending.LastLookupRun == s.Run {
				return "", &externalPendingError{job: pending.JobID}
			}
			return e.runRunner(ctx, s, c, args, p, *pending, true)
		}
		return e.record(s, c, nil, func(*core.Project) (any, error) {
			return nil, errors.New("还有未确认的原作业，必须先核对原作业，不能启动新作业")
		})
	}
	return e.runRunner(ctx, s, c, args, p, action, false)
}

func (e *Engine) recoverRunner(ctx context.Context, s scope, p *core.Project, action *core.RunnerAction) error {
	s.Frame = action.FrameID
	s.toolStarted = true
	frame := p.Tasks[s.Task].Frames[s.Frame]
	if frame == nil {
		return errors.New("原执行器动作上下文缺失")
	}
	calls, _ := pendingCalls(frame.Messages)
	if action.Call != nil {
		calls = []core.Call{*action.Call}
	}
	s.InstructionVersion = action.InstructionVersion
	for _, call := range calls {
		if call.ID != action.CallID {
			continue
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return err
		}
		_, err := e.runRunner(ctx, s, call, args, p, *action, true)
		return err
	}
	return errors.New("原执行器动作上下文缺失")
}

func (e *Engine) runRunner(ctx context.Context, s scope, c core.Call, args map[string]any, p *core.Project, action core.RunnerAction, lookupOnly bool) (string, error) {
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
	inputs := action.Inputs
	if inputs == nil {
		inputs = core.Clone(p.Files)
	}
	jobID, digest := action.JobID, inputs.Digest()
	declared, decodeErr := decodeArgument[[]core.OutputDeclaration](args, "outputs")
	if decodeErr != nil {
		return "", decodeErr
	}
	assertions, decodeErr := decodeArgument[[]core.ArtifactAssertion](args, "assertions")
	if decodeErr != nil {
		return "", decodeErr
	}
	if err := validateAssertions(assertions); err != nil {
		fact := actionFact(p, s, c)
		fact.Deterministic = true
		fact.Conditions = "artifact_assertion_schema"
		s.fact = &fact
		if err := e.admitTool(s, c, fact); err != nil {
			return "", err
		}
		return e.record(s, c, nil, func(*core.Project) (any, error) { return nil, err })
	}
	if err := validateOutputDeclarations(declared, inputs); err != nil {
		return e.record(s, c, nil, func(*core.Project) (any, error) { return nil, err })
	}
	if len(declared) > 0 && str(args, "kind") != "script" {
		return e.record(s, c, nil, func(*core.Project) (any, error) { return nil, errors.New("只有脚本模式可以声明生成输出") })
	}
	var body []byte
	if !lookupOnly {
		files, binaryFiles, err := e.runnerFiles(s.Owner, inputs)
		if err != nil {
			return "", err
		}
		body = []byte(core.JSON(map[string]any{"id": jobID, "digest": digest, "files": files, "binary_files": binaryFiles, "action": c.Function.Name, "parameters": args}))
		// Register the tool before external I/O. A later instruction can let it finish
		// in this run, or let a resumed run look up the same job without submitting it.
		_, err = e.Store.Mutate(s.Owner, s.Project, nil, func(p *core.Project) error {
			if err := s.checkInstruction(p); err != nil {
				return err
			}
			p.Tasks[s.Task].RunnerAction = &action
			return nil
		})
		if err != nil {
			return "", err
		}
	}
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
	result, err := client.result(ctx, jobID, digest, body, lookupOnly, func(message string) error {
		return e.noteWait(s, "runner_wait", role, message)
	})
	if lookupOnly && result.is(http.StatusNotFound, "job_not_found") {
		// A missing receipt cannot prove that a previously dispatched action did
		// not execute. Keep its identity instead of reopening a submission path.
		err = &externalPendingError{job: jobID}
	}
	if err != nil {
		_, _ = e.Store.Mutate(s.Owner, s.Project, nil, func(p *core.Project) error {
			if err := p.Fence(s.Run, s.Epoch); err != nil {
				return err
			}
			task := p.Tasks[s.Task]
			if task.RunnerAction != nil {
				task.RunnerAction.Unknown = true
				task.RunnerAction.LastLookupRun = s.Run
			}
			if task.Dispositions == nil {
				task.Dispositions = map[string]core.Step{}
			}
			key := s.Frame + "/" + c.ID
			task.Dispositions[key] = core.Step{ID: key, FrameID: s.Frame, Tool: c.Function.Name, At: core.Now(), Fact: &core.ActionFact{Disposition: "unknown", ErrorClass: "unknown", Inputs: inputs, Recovery: jobID}}
			return nil
		})
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", &externalPendingError{job: jobID}
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
	result.Details["entry"] = str(args, "entry")
	kind := "run"
	if c.Function.Name == "test_preview" {
		kind = "browser"
	} else {
		result.Details["execution"] = executionType(args)
	}
	blobs, outputs, collectionErr := collectRunnerArtifacts(declared, result.Artifacts, inputs)
	if result.Status == "passed" && collectionErr != nil {
		result.Status = "failed"
		result.Checks = append(result.Checks, core.Check{Name: "artifact_collection", Status: "failed", Detail: collectionErr.Error()})
	}
	if result.Status == "passed" && len(assertions) > 0 {
		bytesByPath := map[string][]byte{}
		for name, file := range inputs {
			data, err := e.Store.Blob(s.Owner, file.Hash)
			if err != nil {
				return "", err
			}
			bytesByPath[name] = data
		}
		for name, file := range outputs {
			bytesByPath[name] = blobs[file.Hash]
		}
		for _, check := range artifactChecks(bytesByPath, assertions) {
			result.Checks = append(result.Checks, check)
			if check.Status != "passed" {
				result.Status = "failed"
			}
		}
	}
	if result.Status != "passed" {
		blobs = nil
		outputs = nil
	}
	report := core.Report{ID: jobID, Kind: kind, Digest: digest, At: core.Now(), Status: result.Status, Checks: result.Checks, Details: json.RawMessage(core.JSON(result.Details))}
	if action.Inputs != nil {
		report.Evidence = &core.ReportEvidence{TaskID: s.Task, InstructionVersion: action.InstructionVersion, PlanRevision: action.PlanRevision, Inputs: inputs, Outputs: outputs, RunID: action.RunID, Epoch: action.Epoch}
		if action.EvidenceVersion == evidenceVersion {
			// Recover against the contract registered with the original job, never
			// stamp a newly edited plan onto an old execution receipt.
			task := core.Clone(*p.Tasks[s.Task])
			task.Plan = action.EvidencePlan
			runnerEvidence(&task, report.Evidence, report, args)
		}
	}
	fact := actionFact(p, s, c)
	if failureCode != "" {
		fact.ErrorClass = "transient"
		fact.ErrorCode = failureCode
		fact.Recovery = jobID
	}
	s.fact = &fact
	saved, err := e.record(s, c, blobs, func(p *core.Project) (any, error) {
		if p.Files.Digest() != digest {
			return nil, errors.New("执行输入版本已变化，拒绝回收与旧报告证明新版本")
		}
		if len(outputs) > 0 && len(p.Tasks[s.Task].InstructionMessages) != action.InstructionVersion {
			return nil, errors.New("输出所属指令版本已失效，拒绝回收")
		}
		next := core.Clone(p.Files)
		for name, file := range outputs {
			next[name] = file
		}
		if err := next.Validate(); err != nil {
			return nil, err
		}
		p.Files = next
		for name, file := range outputs {
			if file.Binary {
				replaceResourceMaterial(p.Tasks[s.Task], s.Frame, core.WorkingMaterial{Resource: "file:" + name, Version: file.Hash, Source: s.Frame + "/" + c.ID, Reason: "已回收的二进制产物，内容验收以报告为准", Recovery: "下载文件或使用内容断言检查"})
			} else {
				rememberFile(p, s, c, name, string(blobs[file.Hash]))
			}
		}
		p.Reports = append(p.Reports, report)
		return map[string]any{"id": report.ID, "status": report.Status, "checks": report.Checks, "outputs": outputs, "failure_code": failureCode}, nil
	})
	return saved, err
}
