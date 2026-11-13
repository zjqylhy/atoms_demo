package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"mockatoms/internal/catalog"
	"mockatoms/internal/core"
)

func insertPreparedProject(tx *sql.Tx, p *core.Project, blobs map[string][]byte, contract core.AppContract) error {
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM projects WHERE owner=? OR owner IN(SELECT workspace_id FROM workspace_accounts WHERE user_id=(SELECT user_id FROM workspace_accounts WHERE workspace_id=?))`, p.Owner, p.Owner).Scan(&count); err != nil {
		return err
	}
	if count >= 30 {
		return errors.New("当前账号或游客工作区最多 30 个项目，请使用已有示例")
	}
	if err := p.Files.Validate(); err != nil {
		return err
	}
	if err := checkStateQuota(tx, p.Owner, p.ID, len(contract.Initial)); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO projects VALUES(?,?,?,?,?)`, p.ID, p.Owner, p.Title, p.Updated, []byte(core.JSON(p))); err != nil {
		return err
	}
	for _, file := range p.Files {
		data, ok := blobs[file.Hash]
		if !ok || len(data) != file.Size || core.Hash(data) != file.Hash {
			return errors.New("副本文件校验失败")
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO blobs VALUES(?,?,?)`, p.Owner, file.Hash, data); err != nil {
			return err
		}
	}
	if contract.API == 1 {
		if _, err := tx.Exec(`INSERT INTO app_states VALUES(?,?,?,?,?)`, p.ID, contract.Schema, 0, []byte(contract.Initial), core.Now()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CopyPreset(sessionHash, template, requestID string) (*core.Project, error) {
	if len(requestID) < 8 || len(requestID) > 100 {
		return nil, errors.New("缺少有效请求标识")
	}
	b, err := catalog.Get(template)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	session, err := readAccount(tx, sessionHash)
	if err != nil {
		return nil, err
	}
	if p, found, err := priorCopy(tx, sessionHash, requestID, template); err != nil || found {
		return p, err
	}
	p, blobs, contract, err := b.Project(session.Workspace, "preset")
	if err != nil {
		return nil, err
	}
	if err = insertPreparedProject(tx, p, blobs, contract); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`INSERT INTO project_copies VALUES(?,?,?,?)`, sessionHash, requestID, template, p.ID); err != nil {
		return nil, err
	}
	return p, tx.Commit()
}

func (s *Store) InstallFallback(owner, id, run, template, reason string) (*core.Project, error) {
	b, err := catalog.Get(template)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var data []byte
	if err = tx.QueryRow(`SELECT state FROM projects WHERE owner=? AND id=?`, owner, id).Scan(&data); err != nil {
		return nil, err
	}
	p, err := decode(data)
	if err != nil {
		return nil, err
	}
	if p.Recovery != nil && p.Recovery.RunID == run {
		return p, nil
	}
	r := p.Runs[run]
	if r == nil || r.Status != "failed" || p.ActiveRun != "" || p.CurrentTask != r.TaskID || p.Tasks[r.TaskID].Status != "failed" {
		return nil, core.ErrStale
	}
	task := p.Tasks[r.TaskID]
	if task.Mode == "plan" && (task.Plan == nil || !task.Plan.Approved) {
		return nil, core.ErrStale
	}
	if p.Recovery != nil {
		// Retrying the original task reuses its existing demo without overwriting
		// the visitor's edits or consuming another project slot.
		p.Recovery.RunID = run
		p.Recovery.Reason = reason
		var count int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM projects WHERE id=?`, p.Recovery.ProjectID).Scan(&count); err != nil {
			return nil, err
		}
		p.Recovery.Unavailable = p.Recovery.Unavailable || count == 0
		if p.Recovery.Unavailable {
			p.Message("assistant", "原需求仍未完成；之前的演示副本已删除，可从首页重新选择示例。", "fallback")
		} else {
			p.Message("assistant", "原需求仍未完成，之前的演示副本及其中数据已保留，可继续使用。", "fallback")
		}
		if _, err = tx.Exec(`UPDATE projects SET state=?,updated=? WHERE id=?`, []byte(core.JSON(p)), p.Updated, id); err != nil {
			return nil, err
		}
		return p, tx.Commit()
	}
	demo, blobs, contract, err := b.Project(owner, "fallback")
	if err != nil {
		return nil, err
	}
	demo.Title = "演示 · " + demo.Title
	demo.Message("assistant", "演示应用已就绪，原需求尚未完成。此副本只提供预置功能；原项目的文件、运行记录及数据已保留。", "fallback")
	if err = insertPreparedProject(tx, demo, blobs, contract); err != nil {
		return nil, err
	}
	p.Recovery = &core.DemoRecovery{RunID: run, ProjectID: demo.ID, Reason: reason}
	p.Message("assistant", "演示应用已就绪，原需求尚未完成。可打开演示副本，或在模型恢复后继续原任务。", "fallback")
	data, err = json.Marshal(p)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`UPDATE projects SET state=?,updated=? WHERE id=?`, data, p.Updated, id); err != nil {
		return nil, err
	}
	return p, tx.Commit()
}
