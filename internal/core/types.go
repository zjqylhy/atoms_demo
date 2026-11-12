package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
)

var ErrStale = errors.New("运行已失效，拒绝写入")
var ErrNotFound = errors.New("项目不存在或无权访问")
var ErrWaiting = errors.New("等待用户输入")

func ID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func Hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func Now() string          { return time.Now().UTC().Format(time.RFC3339Nano) }
func JSON(v any) string    { b, _ := json.Marshal(v); return string(b) }
func Clone[T any](v T) T   { var c T; _ = json.Unmarshal([]byte(JSON(v)), &c); return c }

const MaxFileBytes = 256 * 1024
const MaxProjectBytes = 2 * 1024 * 1024

func FilePath(p string) (string, error) {
	if p == "" || len(p) > 180 || strings.ContainsAny(p, "\\:\x00\r\n") || strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return "", errors.New("文件路径必须为规范的项目相对路径")
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." || strings.HasPrefix(part, ".") || part == "node_modules" {
			return "", errors.New("不允许隐藏目录、上级路径或依赖目录")
		}
	}
	return p, nil
}

type File struct {
	Hash     string `json:"hash"`
	Size     int    `json:"size"`
	Kind     string `json:"kind"`
	ReadOnly bool   `json:"read_only,omitempty"`
}
type Manifest map[string]File

func (m Manifest) Digest() string { return Hash([]byte(JSON(m))) }
func (m Manifest) Validate() error {
	if len(m) > 100 {
		return errors.New("最多 100 个项目文件")
	}
	total := 0
	for p, f := range m {
		if _, err := FilePath(p); err != nil {
			return err
		}
		if f.Size > MaxFileBytes {
			return fmt.Errorf("文件 %s 超过 256 KiB", p)
		}
		total += f.Size
	}
	if total > MaxProjectBytes {
		return errors.New("当前项目快照超过 2 MiB")
	}
	return nil
}

type Call struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function Function `json:"function"`
}
type Function struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
type ModelMessage struct {
	Role          string            `json:"role"`
	Content       string            `json:"content"`
	ToolCalls     []Call            `json:"tool_calls,omitempty"`
	ToolCallID    string            `json:"tool_call_id,omitempty"`
	ProtocolItems []json.RawMessage `json:"protocol_items,omitempty"`
	InvalidAction bool              `json:"invalid_action,omitempty"`
}
type Message struct {
	ID   string `json:"id"`
	Role string `json:"role"`
	Text string `json:"text"`
	At   string `json:"at"`
	Kind string `json:"kind,omitempty"`
}
type Plan struct {
	Revision   string   `json:"revision"`
	Text       string   `json:"text"`
	Acceptance []string `json:"acceptance"`
	Approved   bool     `json:"approved"`
}
type Question struct {
	ID      string   `json:"id"`
	FrameID string   `json:"frame_id"`
	Text    string   `json:"text"`
	Options []string `json:"options,omitempty"`
}
type ModelConfigRef struct {
	ToolMode string `json:"tool_mode"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url"`
	Model    string `json:"model"`
	Revision string `json:"revision"`
}
type Usage struct {
	Input  int  `json:"input"`
	Output int  `json:"output"`
	Total  int  `json:"total"`
	Known  bool `json:"known"`
}
type Frame struct {
	ID       string          `json:"id"`
	Role     string          `json:"role"`
	Goal     string          `json:"goal"`
	Messages []ModelMessage  `json:"messages"`
	Complete bool            `json:"complete"`
	Result   string          `json:"result,omitempty"`
	Context  *ContextSummary `json:"context_summary,omitempty"`
}

// ContextSummary projects a closed prefix of the frame's immutable model history.
type ContextSummary struct {
	Through int    `json:"through"`
	Text    string `json:"text"`
}
type Step struct {
	ID       string `json:"id"`
	FrameID  string `json:"frame_id"`
	Role     string `json:"role"`
	Tool     string `json:"tool"`
	ArgsHash string `json:"args_hash"`
	Result   string `json:"result"`
	At       string `json:"at"`
}
type Checkpoint struct {
	ID       string   `json:"id"`
	At       string   `json:"at"`
	Manifest Manifest `json:"manifest"`
	StepID   string   `json:"step_id"`
}
type Run struct {
	ID            string         `json:"id"`
	TaskID        string         `json:"task_id"`
	Epoch         int64          `json:"epoch"`
	Status        string         `json:"status"`
	Started       string         `json:"started"`
	Ended         string         `json:"ended,omitempty"`
	Model         ModelConfigRef `json:"model"`
	Calls         int            `json:"calls"`
	UsageReports  int            `json:"usage_reports"`
	FormatRepairs int            `json:"format_repairs"`
	Tools         int            `json:"tools"`
	Usage         Usage          `json:"usage"`
	Error         string         `json:"error,omitempty"`
}
type Task struct {
	ID          string            `json:"id"`
	Goal        string            `json:"goal"`
	Mode        string            `json:"mode"`
	Status      string            `json:"status"`
	Base        Manifest          `json:"base"`
	Frames      map[string]*Frame `json:"frames"`
	Root        string            `json:"root"`
	Steps       map[string]Step   `json:"steps"`
	Checkpoints []Checkpoint      `json:"checkpoints"`
	Plan        *Plan             `json:"plan,omitempty"`
	Question    *Question         `json:"question,omitempty"`
	RunIDs      []string          `json:"run_ids"`
}
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}
type Report struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Digest  string          `json:"digest"`
	At      string          `json:"at"`
	Status  string          `json:"status"`
	Checks  []Check         `json:"checks"`
	Details json.RawMessage `json:"details,omitempty"`
}
type Version struct {
	PreviewReady bool     `json:"preview_ready,omitempty"`
	ID           string   `json:"id"`
	Number       int      `json:"number"`
	TaskID       string   `json:"task_id"`
	At           string   `json:"at"`
	Summary      string   `json:"summary"`
	Manifest     Manifest `json:"manifest"`
	Reports      []Report `json:"reports"`
}
type Event struct {
	Seq  int64  `json:"seq"`
	At   string `json:"at"`
	Kind string `json:"kind"`
	Role string `json:"role,omitempty"`
	Text string `json:"text"`
	Data any    `json:"data,omitempty"`
}
type Project struct {
	ID          string            `json:"id"`
	Owner       string            `json:"owner"`
	Title       string            `json:"title"`
	Created     string            `json:"created"`
	Updated     string            `json:"updated"`
	Epoch       int64             `json:"epoch"`
	ActiveRun   string            `json:"active_run,omitempty"`
	CurrentTask string            `json:"current_task,omitempty"`
	Head        string            `json:"head,omitempty"`
	Files       Manifest          `json:"files"`
	Messages    []Message         `json:"messages"`
	Tasks       map[string]*Task  `json:"tasks"`
	Runs        map[string]*Run   `json:"runs"`
	Versions    []Version         `json:"versions"`
	Reports     []Report          `json:"reports"`
	Events      []Event           `json:"events"`
	Requests    map[string]string `json:"requests"`
}

func NewProject(owner, title string) *Project {
	return &Project{ID: ID(), Owner: owner, Title: title, Created: Now(), Updated: Now(), Files: Manifest{}, Tasks: map[string]*Task{}, Runs: map[string]*Run{}, Requests: map[string]string{}}
}
func (p *Project) Event(kind, role, text string, data any) {
	p.Events = append(p.Events, Event{Seq: int64(len(p.Events) + 1), At: Now(), Kind: kind, Role: role, Text: text, Data: data})
	p.Updated = Now()
}
func (p *Project) Message(role, text, kind string) {
	p.Messages = append(p.Messages, Message{ID: ID(), Role: role, Text: text, At: Now(), Kind: kind})
	p.Event("message", role, text, nil)
}
func (p *Project) Fence(run string, epoch int64) error {
	if p.ActiveRun != run || p.Epoch != epoch || p.Runs[run] == nil || p.Runs[run].Status != "running" {
		return ErrStale
	}
	return nil
}

// Public keeps provider protocol items and private execution context off the browser API.
func (p *Project) Public() any {
	tasks := []map[string]any{}
	for _, t := range p.Tasks {
		tasks = append(tasks, map[string]any{"id": t.ID, "goal": t.Goal, "mode": t.Mode, "status": t.Status, "plan": t.Plan, "question": t.Question, "checkpoints": len(t.Checkpoints), "run_ids": t.RunIDs})
	}
	runs := []map[string]any{}
	for _, r := range p.Runs {
		runs = append(runs, map[string]any{"id": r.ID, "task_id": r.TaskID, "status": r.Status, "started": r.Started, "ended": r.Ended, "calls": r.Calls, "tools": r.Tools, "usage": r.Usage, "error": r.Error})
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i]["started"].(string) < runs[j]["started"].(string) })
	versions := make([]Version, len(p.Versions))
	copy(versions, p.Versions)
	for i := range versions {
		versions[i].PreviewReady = hasPreview(versions[i].Reports, versions[i].Manifest.Digest())
		versions[i].Reports = publicReports(versions[i].Reports)
	}
	return map[string]any{"id": p.ID, "preview_ready": hasPreview(p.Reports, p.Files.Digest()), "title": p.Title, "created": p.Created, "updated": p.Updated, "active_run": p.ActiveRun, "current_task": p.CurrentTask, "head": p.Head, "files": p.Files, "messages": p.Messages, "tasks": tasks, "runs": runs, "versions": versions, "reports": publicReports(p.Reports), "events": publicEvents(p.Events)}
}

func hasPreview(reports []Report, digest string) bool {
	for _, r := range reports {
		if r.Digest == digest {
			var d map[string]any
			_ = json.Unmarshal(r.Details, &d)
			if html, ok := d["preview_html"].(string); ok && html != "" {
				return true
			}
		}
	}
	return false
}

func publicReports(reports []Report) []Report {
	out := make([]Report, len(reports))
	copy(out, reports)
	for i := range out {
		var d map[string]any
		if json.Unmarshal(out[i].Details, &d) == nil {
			delete(d, "preview_html")
			out[i].Details = json.RawMessage(JSON(d))
		}
	}
	return out
}
func publicEvents(events []Event) []Event {
	out := make([]Event, len(events))
	copy(out, events)
	// Full read results are already kept in private step records; the UI timeline
	// only needs the action and whether it succeeded, avoiding duplicate file bodies.
	for i := range out {
		if out[i].Kind == "tool" {
			if d, ok := out[i].Data.(map[string]any); ok {
				out[i].Data = map[string]any{"ok": d["ok"]}
			}
		}
	}
	return out
}

func (p *Project) PublicEventsAfter(seq int64) []Event {
	out := []Event{}
	for _, event := range publicEvents(p.Events) {
		if event.Seq > seq {
			out = append(out, event)
		}
	}
	return out
}
