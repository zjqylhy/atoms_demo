package httpapi

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mockatoms/internal/agent"
	"mockatoms/internal/appruntime"
	"mockatoms/internal/config"
	"mockatoms/internal/core"
	"mockatoms/internal/store"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ctxKey string
type window struct {
	At    time.Time
	Count int
}
type Server struct {
	Store  *store.Store
	Engine *agent.Engine
	Config config.Config
	mu     sync.Mutex
	rates  map[string]window
}

func New(s *store.Store, e *agent.Engine, c config.Config) http.Handler {
	app := &Server{Store: s, Engine: e, Config: c, rates: map[string]window{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { send(w, 200, map[string]any{"ok": true}) })
	mux.HandleFunc("GET /api/bootstrap", app.bootstrap)
	mux.HandleFunc("GET /api/catalog", app.catalog)
	mux.HandleFunc("POST /api/catalog/{template}/copy", app.copyPreset)
	mux.HandleFunc("GET /api/shares/{token}", app.publicShare)
	mux.HandleFunc("GET /api/shares/{token}/{view}", app.publicShare)
	mux.HandleFunc("POST /api/shares/{token}/copy", app.copyShare)
	mux.HandleFunc("GET /api/auth/me", app.bootstrap)
	mux.HandleFunc("POST /api/auth/register", app.authenticate)
	mux.HandleFunc("POST /api/auth/login", app.authenticate)
	mux.HandleFunc("POST /api/auth/logout", app.logout)
	mux.HandleFunc("GET /api/projects", app.projects)
	mux.HandleFunc("POST /api/projects", app.projects)
	mux.HandleFunc("GET /api/projects/{id}", app.project)
	mux.HandleFunc("PATCH /api/projects/{id}", app.project)
	mux.HandleFunc("POST /api/projects/{id}/actions", app.action)
	mux.HandleFunc("POST /api/projects/{id}/inputs", app.input)
	mux.HandleFunc("GET /api/projects/{id}/files", app.file)
	mux.HandleFunc("GET /api/projects/{id}/export", app.export)
	mux.HandleFunc("GET /api/projects/{id}/preview", app.preview)
	mux.HandleFunc("POST /api/projects/{id}/preview-access", app.previewAccess)
	mux.HandleFunc("GET /api/projects/{id}/app-state", app.appState)
	mux.HandleFunc("PUT /api/projects/{id}/app-state", app.appState)
	mux.HandleFunc("GET /api/projects/{id}/app-backups", app.appBackups)
	mux.HandleFunc("GET /api/projects/{id}/shares", app.shares)
	mux.HandleFunc("POST /api/projects/{id}/shares", app.shares)
	mux.HandleFunc("DELETE /api/projects/{id}/shares/{share}", app.shares)
	mux.HandleFunc("GET /api/projects/{id}/events", app.events)
	mux.HandleFunc("/", app.static)
	return app.sessionMiddleware(mux)
}
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (s *Server) fail(w http.ResponseWriter, status int, err error) {
	send(w, status, map[string]any{"error": s.Config.Redact(err.Error())})
}
func owner(r *http.Request) string { v, _ := r.Context().Value(ctxKey("owner")).(string); return v }
func csrf(token string) string     { return core.Hash([]byte("atoms-csrf:" + token)) }
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("请求内容格式无效")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("请求只能包含一个 JSON 对象")
	}
	return nil
}
func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	ready := s.Config.ModelError() == nil
	var user any
	session := sessionOf(r)
	notice := "游客数据保存在服务端；注册账号后可跨设备恢复，清除游客 Cookie 后无法自动找回。"
	if session.UserID != "" {
		user = map[string]string{"id": session.UserID, "username": session.Username}
		notice = "项目与已保存应用数据归属当前账号，可在其他设备登录恢复。"
	}
	send(w, 200, map[string]any{"csrf": r.Context().Value(ctxKey("csrf")), "user": user, "model_ready": ready, "runner_configured": s.Config.RunnerURL != "" && s.Config.RunnerToken != "", "limits": map[string]any{"max_calls": s.Config.MaxCalls, "max_tools": s.Config.MaxTools, "run_seconds": s.Config.RunSeconds}, "notice": notice})
}
func (s *Server) projects(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.List(owner(r))
	if err != nil {
		s.fail(w, 500, errors.New("无法读取项目"))
		return
	}
	if r.Method == "GET" {
		send(w, 200, list)
		return
	}
	if len(list) >= 30 {
		s.fail(w, 409, errors.New("当前工作区最多 30 个项目"))
		return
	}
	var body struct {
		Title string `json:"title"`
	}
	if err = decode(r, &body); err != nil {
		s.fail(w, 400, err)
		return
	}
	if body.Title == "" {
		body.Title = "未命名项目"
	}
	if len(body.Title) > 160 {
		s.fail(w, 400, errors.New("项目名称过长"))
		return
	}
	p, err := s.Store.Create(owner(r), body.Title)
	if err != nil {
		s.fail(w, 500, errors.New("创建项目失败"))
		return
	}
	send(w, 201, p.Public())
}
func (s *Server) project(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.Get(owner(r), r.PathValue("id"))
	if err != nil {
		s.fail(w, 404, core.ErrNotFound)
		return
	}
	if r.Method == "PATCH" {
		var body struct {
			Title string `json:"title"`
		}
		if err = decode(r, &body); err != nil || strings.TrimSpace(body.Title) == "" || len(body.Title) > 160 {
			s.fail(w, 400, errors.New("项目名称无效"))
			return
		}
		p, err = s.Store.Mutate(owner(r), p.ID, nil, func(p *core.Project) error { p.Title = body.Title; return nil })
		if err != nil {
			s.fail(w, 500, errors.New("更新项目失败"))
			return
		}
	}
	send(w, 200, p.Public())
}
func (s *Server) action(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action     string `json:"action"`
		Text       string `json:"text"`
		Mode       string `json:"mode"`
		RequestID  string `json:"request_id"`
		Revision   string `json:"revision"`
		QuestionID string `json:"question_id"`
		Version    string `json:"version"`
		RunID      string `json:"run_id"`
	}
	if err := decode(r, &body); err != nil {
		s.fail(w, 400, err)
		return
	}
	id := r.PathValue("id")
	var p *core.Project
	var err error
	switch body.Action {
	case "steer":
		p, err = s.Engine.Steer(owner(r), id, body.RunID, body.RequestID, body.Text)
	case "stop", "end":
		p, err = s.Engine.Stop(owner(r), id, body.Action == "end")
	case "restore":
		p, err = s.Store.Mutate(owner(r), id, nil, func(p *core.Project) error {
			if p.ActiveRun != "" {
				return errors.New("运行中不能恢复历史版本")
			}
			if body.RequestID == "" {
				return errors.New("缺少请求标识")
			}
			if _, exists := p.Requests[body.RequestID]; exists {
				return nil
			}
			for _, v := range p.Versions {
				if v.ID == body.Version {
					p.Files = core.Clone(v.Manifest)
					p.Head = v.ID
					p.CurrentTask = ""
					p.Epoch++
					p.Requests[body.RequestID] = "restore:" + v.ID
					p.Event("restore", "system", fmt.Sprintf("已恢复 v%d，其他历史版本保留", v.Number), nil)
					return nil
				}
			}
			return errors.New("版本不存在")
		})
	default:
		decisionID := body.Revision
		if body.Action == "answer" {
			decisionID = body.QuestionID
		}
		p, err = s.Engine.Start(owner(r), id, body.Text, body.Mode, body.RequestID, body.Action, decisionID)
	}
	if err != nil {
		status := 409
		if errors.Is(err, core.ErrNotFound) {
			status = 404
		}
		s.fail(w, status, err)
		return
	}
	send(w, 200, p.Public())
}
func (s *Server) input(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}
	if err := decode(r, &body); err != nil {
		s.fail(w, 400, err)
		return
	}
	name := "inputs/" + body.Name
	if _, err := core.FilePath(name); err != nil || path.Base(body.Name) != body.Name || len(body.Content) == 0 || len(body.Content) > core.MaxFileBytes {
		s.fail(w, 400, errors.New("输入文件路径或大小无效"))
		return
	}
	if path.Ext(name) != ".csv" && path.Ext(name) != ".json" {
		s.fail(w, 400, errors.New("仅支持粘贴 CSV/JSON"))
		return
	}
	if err := agent.ValidateTable(body.Content); err != nil {
		s.fail(w, 400, err)
		return
	}
	hash := core.Hash([]byte(body.Content))
	p, err := s.Store.Mutate(owner(r), r.PathValue("id"), map[string][]byte{hash: []byte(body.Content)}, func(p *core.Project) error {
		if p.ActiveRun != "" || p.CurrentTask != "" && p.Tasks[p.CurrentTask].Status != "completed" {
			return errors.New("请在开始任务前添加输入，或结束当前任务后添加")
		}
		if _, exists := p.Files[name]; exists {
			return errors.New("输入原件不可覆盖，请使用新名称")
		}
		p.Files[name] = core.File{Hash: hash, Size: len(body.Content), Kind: "data", ReadOnly: true}
		p.Event("input", "user", "已保存输入原件："+name, nil)
		return nil
	})
	if err != nil {
		s.fail(w, 409, err)
		return
	}
	send(w, 200, p.Public())
}
func selected(p *core.Project, id string) (core.Manifest, error) {
	if id == "" || id == "draft" {
		return p.Files, nil
	}
	for _, v := range p.Versions {
		if v.ID == id {
			return v.Manifest, nil
		}
	}
	return nil, errors.New("版本不存在")
}
func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.Get(owner(r), r.PathValue("id"))
	if err != nil {
		s.fail(w, 404, core.ErrNotFound)
		return
	}
	manifest, err := selected(p, r.URL.Query().Get("version"))
	if err != nil {
		s.fail(w, 404, err)
		return
	}
	name := r.URL.Query().Get("path")
	file, ok := manifest[name]
	if !ok {
		s.fail(w, 404, errors.New("文件不存在"))
		return
	}
	b, err := s.Store.Blob(owner(r), file.Hash)
	if err != nil {
		s.fail(w, 500, errors.New("文件读取失败"))
		return
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(name)}))
		w.Write(b)
		return
	}
	if file.Binary {
		send(w, 200, map[string]any{"path": name, "file": file, "content": "二进制产物，请下载查看。"})
		return
	}
	send(w, 200, map[string]any{"path": name, "file": file, "content": string(b)})
}
func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.Get(owner(r), r.PathValue("id"))
	if err != nil {
		s.fail(w, 404, core.ErrNotFound)
		return
	}
	manifest, err := selected(p, r.URL.Query().Get("version"))
	if err != nil {
		s.fail(w, 404, err)
		return
	}
	files := map[string][]byte{}
	for name, file := range manifest {
		data, err := s.Store.Blob(owner(r), file.Hash)
		if err != nil {
			s.fail(w, 500, errors.New("无法读取导出文件"))
			return
		}
		files[name] = data
	}
	var contract *core.AppContract
	if data, ok := files[core.AppContractPath]; ok {
		parsed, err := core.ParseAppContract(data)
		if err != nil {
			s.fail(w, 400, err)
			return
		}
		contract = &parsed
	}
	markup := core.PreviewHTML(p.Reports, manifest)
	if r.URL.Query().Get("format") == "html" {
		if markup == "" {
			s.fail(w, 409, errors.New("此版本没有已构建网页，请先运行检查"))
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="atoms-app.html"`)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = io.WriteString(w, appruntime.Offline(markup, contract))
		return
	}
	if markup != "" {
		files["atoms-standalone.html"] = []byte(appruntime.Offline(markup, contract))
		files["ATOMS-EXPORT.txt"] = []byte("源码及构建网页导出。atoms-standalone.html 可直接打开，无需安装依赖。默认仅带应用种子数据，不包含服务端业务记录。离线修改不会同步服务端，请用页面的数据 JSON 导出/导入功能备份。")
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": "atoms-project.zip"}))
	w.Header().Set("Cache-Control", "no-store")
	z := zip.NewWriter(w)
	for name, content := range files {
		entry, e := z.Create(name)
		if e != nil {
			break
		}
		_, _ = entry.Write(content)
	}
	_ = z.Close()
}
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.Get(owner(r), r.PathValue("id"))
	if err != nil {
		s.fail(w, 404, core.ErrNotFound)
		return
	}
	manifest, err := selected(p, r.URL.Query().Get("version"))
	if err != nil {
		s.fail(w, 404, err)
		return
	}
	markup := core.PreviewHTML(p.Reports, manifest)
	if markup == "" {
		s.fail(w, 404, errors.New("尚未生成可预览构建，请查看文件或继续执行"))
		return
	}
	if _, ok := manifest[core.AppContractPath]; ok {
		markup = appruntime.Inject(markup)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:; connect-src 'none'; form-action 'none'; base-uri 'none'; frame-ancestors 'self'; sandbox allow-scripts allow-forms")
	_, _ = io.WriteString(w, markup)
}
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.Store.Get(owner(r), id); err != nil {
		s.fail(w, 404, core.ErrNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.fail(w, 500, errors.New("不支持事件流"))
		return
	}
	last, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	if last == 0 {
		last, _ = strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	ticker := time.NewTicker(700 * time.Millisecond)
	defer ticker.Stop()
	heartbeat := 0
	var updated string
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			if !s.Store.SessionValid(sessionOf(r).TokenHash) {
				return
			}
			p, err := s.Store.Get(owner(r), id)
			if err != nil {
				return
			}
			previous := last
			for _, event := range p.PublicEventsAfter(last) {
				if event.Seq > last {
					fmt.Fprintf(w, "id: %d\ndata: %s\n\n", event.Seq, core.JSON(event))
					last = event.Seq
				}
			}
			// The worker can clear active_run after its last recorded tool event.
			if last == previous && p.Updated != updated {
				fmt.Fprint(w, "event: snapshot\ndata: {}\n\n")
			}
			updated = p.Updated
			heartbeat++
			if heartbeat%20 == 0 {
				fmt.Fprint(w, ": keepalive\n\n")
			}
			flusher.Flush()
		}
	}
}
func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.NotFound(w, r)
		return
	}
	clean := path.Clean("/" + r.URL.Path)
	file := filepath.Join(s.Config.WebDir, filepath.FromSlash(strings.TrimPrefix(clean, "/")))
	info, err := os.Stat(file)
	if err != nil || info.IsDir() {
		file = filepath.Join(s.Config.WebDir, "index.html")
	}
	if _, err = os.Stat(file); err != nil {
		s.fail(w, 503, errors.New("前端尚未构建，请先执行 web 的构建命令"))
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; frame-src 'self'")
	http.ServeFile(w, r, file)
}
func ValidPublicURL(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https")
}
