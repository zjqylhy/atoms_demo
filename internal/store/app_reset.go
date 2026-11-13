package store

import (
	"database/sql"
	"errors"
	"mockatoms/internal/core"
	"time"
)

type ResetAppRequest struct {
	Version   string `json:"version"`
	Expected  int64  `json:"expected_revision"`
	RequestID string `json:"request_id"`
}

// Reset is an explicit owner action, separate from the generated application's
// channel. A format change never silently replaces existing business records.
func (s *Store) ResetAppState(sessionHash, id string, in ResetAppRequest) (int64, error) {
	if len(in.RequestID) < 8 || len(in.RequestID) > 100 {
		return 0, errors.New("缺少有效请求标识")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	p, err := sharedProject(tx, sessionHash, id)
	if err != nil {
		return 0, err
	}
	if p.Head == "" || p.Head != in.Version {
		return 0, ErrGrant
	}
	files, _, err := p.PreviewFiles(in.Version)
	if err != nil {
		return 0, err
	}
	c, err := contractAt(tx, p, files)
	if err != nil {
		return 0, err
	}
	if c == nil {
		return 0, ErrSchema
	}
	hash := core.Hash([]byte("reset:" + core.JSON(in)))
	var receipt string
	var revision int64
	err = tx.QueryRow(`SELECT request_hash,revision FROM app_receipts WHERE project_id=? AND request_id=?`, id, in.RequestID).Scan(&receipt, &revision)
	if err == nil {
		if receipt != hash {
			return 0, errors.New("请求标识不能重复用于其他内容")
		}
		return revision, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	state, err := readAppState(tx, id)
	if err != nil {
		return 0, err
	}
	if state.Revision != in.Expected {
		return 0, ErrConflict
	}
	if state.Schema == c.Schema {
		return 0, errors.New("格式已兼容，无需重置")
	}
	if err = checkStateQuota(tx, p.Owner, id, len(c.Initial)); err != nil {
		return 0, err
	}
	if err = backupState(tx, id, state); err != nil {
		return 0, err
	}
	revision = state.Revision + 1
	if _, err = tx.Exec(`UPDATE app_states SET schema_version=?,revision=?,value=?,updated=? WHERE project_id=?`, c.Schema, revision, []byte(c.Initial), core.Now(), id); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`INSERT INTO app_receipts VALUES(?,?,?,?,?)`, id, in.RequestID, hash, revision, time.Now().Unix()); err != nil {
		return 0, err
	}
	return revision, tx.Commit()
}
