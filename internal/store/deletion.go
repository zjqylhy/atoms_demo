package store

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"mockatoms/internal/core"
)

var ErrProjectRunning = errors.New("项目正在运行或收尾，请先停止任务并等待结束后再删除")
var ErrCopyDeleted = errors.New("原副本已删除，请重新点击创建副本")

// DeleteProject is called under the engine admission lock. The transaction also
// checks persisted activity, so a stopped run cannot race its final cleanup.
func (s *Store) DeleteProject(sessionHash, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	p, err := sharedProject(tx, sessionHash, id)
	if err != nil {
		return err
	}
	if p.ActiveRun != "" {
		return ErrProjectRunning
	}

	// Keep unknown/older JSON fields when marking demo references. Scan every
	// remaining state conservatively: history and evidence may reference bytes
	// that are no longer in the current file manifest.
	rows, err := tx.Query(`SELECT id,state FROM projects WHERE id<>?`, id)
	if err != nil {
		return err
	}
	type remaining struct {
		id         string
		state      []byte
		references []byte
		changed    bool
		updated    string
	}
	states := []remaining{}
	for rows.Next() {
		var item remaining
		if err = rows.Scan(&item.id, &item.state); err != nil {
			rows.Close()
			return err
		}
		var object map[string]json.RawMessage
		if err = json.Unmarshal(item.state, &object); err != nil || object == nil {
			rows.Close()
			return errors.New("其他项目记录无法校验，删除已取消")
		}
		// Normalize escaped string values for reference scanning only; never
		// rewrite the original project through a lossy generic JSON decoder.
		var value any
		if err = json.Unmarshal(item.state, &value); err != nil {
			rows.Close()
			return err
		}
		item.references, err = json.Marshal(value)
		if err != nil {
			rows.Close()
			return err
		}
		if raw := object["recovery"]; len(raw) > 0 && string(raw) != "null" {
			var recovery map[string]json.RawMessage
			if err = json.Unmarshal(raw, &recovery); err != nil {
				rows.Close()
				return err
			}
			var target string
			_ = json.Unmarshal(recovery["project_id"], &target)
			if target == id {
				recovery["unavailable"] = json.RawMessage(`true`)
				object["recovery"], _ = json.Marshal(recovery)
				// SSE observes Updated, including changes without a new chat event.
				item.updated = core.Now()
				object["updated"], _ = json.Marshal(item.updated)
				item.state, err = json.Marshal(object)
				if err != nil {
					rows.Close()
					return err
				}
				item.changed = true
			}
		}
		states = append(states, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range states {
		if item.changed {
			if _, err = tx.Exec(`UPDATE projects SET state=?,updated=? WHERE id=?`, item.state, item.updated, item.id); err != nil {
				return err
			}
		}
	}
	for _, table := range []string{"app_receipts", "app_backups", "preview_grants", "share_links", "app_states"} {
		if _, err = tx.Exec(`DELETE FROM `+table+` WHERE project_id=?`, id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`DELETE FROM projects WHERE id=?`, id); err != nil {
		return err
	}
	// Copy receipts deliberately survive: retries must not recreate a deleted
	// copy. Account sessions, workspace mappings and quota ledgers also survive.
	rows, err = tx.Query(`SELECT hash FROM blobs WHERE owner=?`, p.Owner)
	if err != nil {
		return err
	}
	garbage := []string{}
	for rows.Next() {
		var hash string
		if err = rows.Scan(&hash); err != nil {
			rows.Close()
			return err
		}
		referenced := false
		for _, item := range states {
			if bytes.Contains(item.references, []byte(hash)) {
				referenced = true
				break
			}
		}
		if !referenced {
			garbage = append(garbage, hash)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, hash := range garbage {
		if _, err = tx.Exec(`DELETE FROM blobs WHERE owner=? AND hash=?`, p.Owner, hash); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func priorCopy(tx *sql.Tx, sessionHash, requestID, source string) (*core.Project, bool, error) {
	var id, priorSource string
	err := tx.QueryRow(`SELECT project_id,source FROM project_copies WHERE session_hash=? AND request_id=?`, sessionHash, requestID).Scan(&id, &priorSource)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if priorSource != source {
		return nil, true, errors.New("请求标识已用于其他副本")
	}
	var data []byte
	err = tx.QueryRow(`SELECT state FROM projects WHERE id=?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, true, ErrCopyDeleted
	}
	if err != nil {
		return nil, true, err
	}
	p, err := decode(data)
	return p, true, err
}
