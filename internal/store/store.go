package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"mockatoms/internal/core"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Store struct {
	db *sql.DB
	mu sync.Mutex
}

func Open(filename string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return nil, err
	}
	db, e := sql.Open("sqlite", filename)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "PRAGMA busy_timeout=5000", "PRAGMA foreign_keys=ON", `CREATE TABLE IF NOT EXISTS model_window(id TEXT PRIMARY KEY, stamp_ms INTEGER NOT NULL, tokens INTEGER NOT NULL)`, `CREATE TABLE IF NOT EXISTS call_quota(day TEXT NOT NULL, owner TEXT NOT NULL, calls INTEGER NOT NULL, PRIMARY KEY(day,owner))`, `CREATE TABLE IF NOT EXISTS workspaces (id TEXT PRIMARY KEY, token_hash TEXT UNIQUE NOT NULL, created TEXT NOT NULL)`, `CREATE TABLE IF NOT EXISTS projects (id TEXT PRIMARY KEY, owner TEXT NOT NULL, title TEXT NOT NULL, updated TEXT NOT NULL, state BLOB NOT NULL)`, `CREATE INDEX IF NOT EXISTS projects_owner ON projects(owner)`, `CREATE TABLE IF NOT EXISTS blobs (owner TEXT NOT NULL, hash TEXT NOT NULL, content BLOB NOT NULL, PRIMARY KEY(owner,hash))`} {
		if _, e = db.Exec(q); e != nil {
			db.Close()
			return nil, e
		}
	}
	s := &Store{db: db}
	if e = s.migrate(); e != nil {
		db.Close()
		return nil, e
	}
	return s, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Workspace(token string, create bool) (string, error) {
	h := core.Hash([]byte(token))
	var id string
	e := s.db.QueryRow("SELECT id FROM workspaces WHERE token_hash=?", h).Scan(&id)
	if errors.Is(e, sql.ErrNoRows) && create {
		id = core.ID()
		_, e = s.db.Exec("INSERT INTO workspaces VALUES(?,?,?)", id, h, core.Now())
	}
	return id, e
}
func (s *Store) Create(owner, title string) (*core.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.db.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var count int
	if e = tx.QueryRow(`SELECT COUNT(*) FROM projects WHERE owner=? OR owner IN (SELECT workspace_id FROM workspace_accounts WHERE user_id=(SELECT user_id FROM workspace_accounts WHERE workspace_id=?))`, owner, owner).Scan(&count); e != nil {
		return nil, e
	}
	if count >= 30 {
		return nil, errors.New("当前账号或游客工作区最多 30 个项目")
	}
	p := core.NewProject(owner, title)
	b, e := json.Marshal(p)
	if e != nil {
		return nil, e
	}
	if _, e = tx.Exec("INSERT INTO projects VALUES(?,?,?,?,?)", p.ID, p.Owner, p.Title, p.Updated, b); e != nil {
		return nil, e
	}
	return p, tx.Commit()
}
func decode(b []byte) (*core.Project, error) {
	var p core.Project
	e := json.Unmarshal(b, &p)
	return &p, e
}
func (s *Store) Get(owner, id string) (*core.Project, error) {
	var b []byte
	e := s.db.QueryRow("SELECT state FROM projects WHERE id=? AND owner=?", id, owner).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, core.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	return decode(b)
}
func (s *Store) List(owner string) ([]map[string]any, error) {
	return s.listProjects(`SELECT id,title,updated FROM projects WHERE owner=? OR owner IN (SELECT workspace_id FROM workspace_accounts WHERE user_id=(SELECT user_id FROM workspace_accounts WHERE workspace_id=?)) ORDER BY updated DESC`, owner, owner)
}

func (s *Store) SharedProjects(sessionHash string) ([]map[string]any, error) {
	if _, err := s.AccountSession(sessionHash); err != nil {
		return nil, err
	}
	return s.listProjects(`SELECT id,title,updated FROM projects ORDER BY updated DESC,id`)
}

func (s *Store) listProjects(query string, args ...any) ([]map[string]any, error) {
	rows, e := s.db.Query(query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, title, updated string
		if e = rows.Scan(&id, &title, &updated); e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"id": id, "title": title, "updated": updated})
	}
	return out, rows.Err()
}

// Mutate serializes state changes and writes file content and its checkpoint in one transaction.
// The callback must not perform network calls, code execution, or query Store again.
func (s *Store) Mutate(owner, id string, blobs map[string][]byte, f func(*core.Project) error) (*core.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, e := s.db.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var b []byte
	e = tx.QueryRow("SELECT state FROM projects WHERE id=? AND owner=?", id, owner).Scan(&b)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, core.ErrNotFound
	}
	if e != nil {
		return nil, e
	}
	p, e := decode(b)
	if e != nil {
		return nil, e
	}
	previousHead := p.Head
	if e = f(p); e != nil {
		return nil, e
	}
	if p.Head != previousHead {
		// Reverting to an old version must not resurrect its previous ports/grants.
		if _, e = tx.Exec(`DELETE FROM preview_grants WHERE project_id=?`, id); e != nil {
			return nil, e
		}
	}
	if e = p.Files.Validate(); e != nil {
		return nil, e
	}
	referenced := map[string]bool{}
	reference := func(manifest core.Manifest) {
		for _, file := range manifest {
			referenced[file.Hash] = true
		}
	}
	reference(p.Files)
	// Snapshot imports must retain the bytes behind saved history as well as
	// the current draft. Unreferenced blobs are still excluded.
	for _, task := range p.Tasks {
		reference(task.Base)
		for _, checkpoint := range task.Checkpoints {
			reference(checkpoint.Manifest)
		}
	}
	for _, version := range p.Versions {
		reference(version.Manifest)
	}
	for hash, content := range blobs {
		if !referenced[hash] {
			continue
		}
		if core.Hash(content) != hash || len(content) > core.MaxFileBytes {
			return nil, errors.New("文件摘要或大小不合法")
		}
		if _, e = tx.Exec("INSERT OR IGNORE INTO blobs VALUES(?,?,?)", owner, hash, content); e != nil {
			return nil, e
		}
	}
	// A manifest may reference old blobs or blobs inserted by this same transaction, never absent content.
	for _, file := range p.Files {
		var n int
		if e = tx.QueryRow("SELECT length(content) FROM blobs WHERE owner=? AND hash=?", owner, file.Hash).Scan(&n); e != nil {
			return nil, e
		}
		if n != file.Size {
			return nil, errors.New("文件清单与内容不一致")
		}
	}
	p.Updated = core.Now()
	b, e = json.Marshal(p)
	if e != nil {
		return nil, e
	}
	if _, e = tx.Exec("UPDATE projects SET title=?,updated=?,state=? WHERE id=? AND owner=?", p.Title, p.Updated, b, id, owner); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return p, nil
}
func (s *Store) Blob(owner, hash string) ([]byte, error) {
	var b []byte
	e := s.db.QueryRow("SELECT content FROM blobs WHERE owner=? AND hash=?", owner, hash).Scan(&b)
	return b, e
}
func (s *Store) Files(owner string, m core.Manifest) (map[string]string, error) {
	out := map[string]string{}
	for name, f := range m {
		b, e := s.Blob(owner, f.Hash)
		if e != nil {
			return nil, e
		}
		out[name] = string(b)
	}
	return out, nil
}
func (s *Store) Recover() error {
	rows, e := s.db.Query("SELECT owner,id FROM projects")
	if e != nil {
		return e
	}
	var ids [][2]string
	for rows.Next() {
		var pair [2]string
		if e = rows.Scan(&pair[0], &pair[1]); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, pair)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, pair := range ids {
		_, e = s.Mutate(pair[0], pair[1], nil, func(p *core.Project) error {
			if p.ActiveRun != "" {
				r := p.Runs[p.ActiveRun]
				if r != nil && r.Status == "running" {
					r.Status = "interrupted"
					r.Ended = core.Now()
					r.Error = "服务中断，已保存工作保留，可继续任务"
					if t := p.Tasks[r.TaskID]; t != nil {
						t.Status = "interrupted"
					}
					p.Event("interrupted", "system", r.Error, nil)
				}
				p.ActiveRun = ""
				p.Epoch++
			}
			return nil
		})
		if e != nil {
			return e
		}
	}
	return nil
}

// ReserveCall durably reserves before sending. Unknown outcomes retain their slot;
// closing a browser or restarting a process cannot reset daily limits.
func (s *Store) ReserveCall(owner string, globalLimit, workspaceLimit int) error {
	if globalLimit <= 0 && workspaceLimit <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	day := time.Now().UTC().Format("2006-01-02")
	var total, used int
	if err = tx.QueryRow(`SELECT COALESCE(SUM(calls),0),COALESCE(SUM(CASE WHEN owner=? OR owner IN (SELECT workspace_id FROM workspace_accounts WHERE user_id=(SELECT user_id FROM workspace_accounts WHERE workspace_id=?)) THEN calls ELSE 0 END),0) FROM call_quota WHERE day=?`, owner, owner, day).Scan(&total, &used); err != nil {
		return err
	}
	if globalLimit > 0 && total >= globalLimit {
		return errors.New("服务当日模型调用额度已用完，任务与产物保留")
	}
	if workspaceLimit > 0 && used >= workspaceLimit {
		return errors.New("工作区当日模型调用额度已用完，任务与产物保留")
	}
	if _, err = tx.Exec("INSERT INTO call_quota VALUES(?,?,1) ON CONFLICT(day,owner) DO UPDATE SET calls=calls+1", day, owner); err != nil {
		return err
	}
	return tx.Commit()
}
