package store

import (
	"database/sql"
	"errors"
	"mockatoms/internal/core"
)

func (s *Store) CreateShare(sessionHash, id, version string) (string, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	p, err := authProject(tx, sessionHash, id)
	if err != nil {
		return "", "", err
	}
	if version == "" {
		version = p.Head
	}
	if version == "" {
		return "", "", errors.New("只能分享已交付版本")
	}
	files, reports, err := p.PreviewFiles(version)
	if err != nil {
		return "", "", err
	}
	if core.PreviewHTML(reports, files) == "" {
		return "", "", errors.New("此版本没有可运行页面")
	}
	var count int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM share_links WHERE project_id=? AND revoked_at=''`, id).Scan(&count); err != nil {
		return "", "", err
	}
	if count >= 10 {
		return "", "", errors.New("最多保留 10 个分享链接，请先撤销旧链接")
	}
	token, sid := core.ID()+core.ID(), core.ID()
	if _, err = tx.Exec(`INSERT INTO share_links VALUES(?,?,?,?,?,?,'')`, sid, core.Hash([]byte(token)), id, version, p.Title, core.Now()); err != nil {
		return "", "", err
	}
	return sid, token, tx.Commit()
}

type SharedPreview struct {
	Title    string            `json:"title"`
	Version  string            `json:"version"`
	Mode     string            `json:"mode"`
	Contract *core.AppContract `json:"contract,omitempty"`
	HTML     string            `json:"-"`
}

func readShare(tx *sql.Tx, token string) (*core.Project, string, string, error) {
	if len(token) != 64 {
		return nil, "", "", core.ErrNotFound
	}
	var data []byte
	var version, title string
	err := tx.QueryRow(`SELECT p.state,s.version_id,s.title FROM share_links s JOIN projects p ON p.id=s.project_id WHERE s.token_hash=? AND s.revoked_at=''`, core.Hash([]byte(token))).Scan(&data, &version, &title)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", "", core.ErrNotFound
	}
	if err != nil {
		return nil, "", "", err
	}
	p, err := decode(data)
	return p, version, title, err
}

func (s *Store) Shared(token string) (SharedPreview, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return SharedPreview{}, err
	}
	defer tx.Rollback()
	p, version, title, err := readShare(tx, token)
	if err != nil {
		return SharedPreview{}, err
	}
	files, reports, err := p.PreviewFiles(version)
	if err != nil {
		return SharedPreview{}, err
	}
	c, err := contractAt(tx, p, files)
	if err != nil {
		return SharedPreview{}, err
	}
	mode := "legacy"
	if c != nil {
		mode = "trial"
	}
	return SharedPreview{Title: title, Version: version, Mode: mode, Contract: c, HTML: core.PreviewHTML(reports, files)}, nil
}

func (s *Store) Shares(sessionHash, id string) ([]map[string]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = authProject(tx, sessionHash, id); err != nil {
		return nil, err
	}
	rows, err := tx.Query(`SELECT id,version_id,created FROM share_links WHERE project_id=? AND revoked_at='' ORDER BY created DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var sid, version, created string
		if err = rows.Scan(&sid, &version, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{"id": sid, "version": version, "created": created})
	}
	return out, rows.Err()
}

func (s *Store) RevokeShare(sessionHash, id, shareID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = authProject(tx, sessionHash, id); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE share_links SET revoked_at=? WHERE id=? AND project_id=?`, core.Now(), shareID, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CopyShare(sessionHash, token, requestID string) (*core.Project, error) {
	if len(requestID) < 8 || len(requestID) > 100 {
		return nil, errors.New("缺少有效请求标识")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	session, err := readSession(tx, sessionHash)
	if err != nil {
		return nil, ErrSession
	}
	original, version, title, err := readShare(tx, token)
	if err != nil {
		return nil, err
	}
	var data []byte
	var source string
	err = tx.QueryRow(`SELECT p.state,c.source FROM project_copies c JOIN projects p ON p.id=c.project_id WHERE c.session_hash=? AND c.request_id=?`, sessionHash, requestID).Scan(&data, &source)
	key := "share:" + core.Hash([]byte(token))
	if err == nil {
		if source != key {
			return nil, errors.New("请求标识已用于其他副本")
		}
		return decode(data)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	files, reports, err := original.PreviewFiles(version)
	if err != nil {
		return nil, err
	}
	c, err := contractAt(tx, original, files)
	if err != nil {
		return nil, err
	}
	contract := core.AppContract{}
	if c != nil {
		contract = *c
	}
	p := core.NewProject(session.Workspace, "副本 · "+title)
	p.Source = &core.ProjectSource{Kind: "share"}
	// Copy only the public page and its declared seed. Other source files may
	// contain private inputs and are outside the share's explicit boundary.
	blobs := map[string][]byte{}
	add := func(name string, data []byte, readOnly bool) {
		hash := core.Hash(data)
		p.Files[name] = core.File{Hash: hash, Size: len(data), Kind: "web", ReadOnly: readOnly}
		blobs[hash] = data
	}
	add("index.html", []byte(core.PreviewHTML(reports, files)), false)
	for _, name := range []string{core.AppContractPath, core.AppRuntimePath} {
		if file, ok := files[name]; ok {
			var content []byte
			if err = tx.QueryRow(`SELECT content FROM blobs WHERE owner=? AND hash=?`, original.Owner, file.Hash).Scan(&content); err != nil {
				return nil, err
			}
			add(name, content, name == core.AppRuntimePath)
		}
	}
	p.Reports = []core.Report{{ID: core.ID(), Kind: "shared_preview", At: core.Now(), Digest: p.Files.Digest(), Status: "available", Details: []byte(core.JSON(map[string]any{"preview_html": core.PreviewHTML(reports, files), "notice": "分享页面副本；未重新运行模型或浏览器检查"}))}}
	p.Head = core.ID()
	p.Versions = []core.Version{{ID: p.Head, Number: 1, At: core.Now(), Summary: "分享副本", Manifest: core.Clone(p.Files), Reports: p.Reports}}
	if err = insertPreparedProject(tx, p, blobs, contract); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`INSERT INTO project_copies VALUES(?,?,?,?)`, sessionHash, requestID, key, p.ID); err != nil {
		return nil, err
	}
	return p, tx.Commit()
}
