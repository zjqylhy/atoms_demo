package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"mockatoms/internal/core"
	"time"
)

var ErrConflict = errors.New("应用数据已被其他页面修改，请重新加载后再保存")
var ErrGrant = errors.New("预览授权已失效，请刷新预览")
var ErrSchema = errors.New("代码与现有数据格式不兼容，请切回兼容版本或确认备份后重置")

type PreviewAccess struct {
	Mode     string            `json:"mode"`
	Grant    string            `json:"grant,omitempty"`
	Contract *core.AppContract `json:"contract,omitempty"`
	State    *core.AppState    `json:"state,omitempty"`
	Version  string            `json:"version"`
}

// Recheck the account inside transactions so a revoked session cannot keep
// using a previously issued preview grant. Every signed-in account shares projects.
func sharedProject(tx *sql.Tx, hash, id string) (*core.Project, error) {
	_, err := readAccount(tx, hash)
	if err != nil {
		return nil, err
	}
	var data []byte
	err = tx.QueryRow(`SELECT state FROM projects WHERE id=?`, id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return decode(data)
}

func contractAt(tx *sql.Tx, p *core.Project, files core.Manifest) (*core.AppContract, error) {
	file, ok := files[core.AppContractPath]
	if !ok {
		return nil, nil
	}
	var data []byte
	if err := tx.QueryRow(`SELECT content FROM blobs WHERE owner=? AND hash=?`, p.Owner, file.Hash).Scan(&data); err != nil {
		return nil, err
	}
	c, err := core.ParseAppContract(data)
	return &c, err
}

func readAppState(tx *sql.Tx, id string) (core.AppState, error) {
	var state core.AppState
	err := tx.QueryRow(`SELECT schema_version,revision,value,updated FROM app_states WHERE project_id=?`, id).Scan(&state.Schema, &state.Revision, &state.Value, &state.Updated)
	return state, err
}

func (s *Store) PreviewAccess(sessionHash, id, version string) (PreviewAccess, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return PreviewAccess{}, err
	}
	defer tx.Rollback()
	p, err := sharedProject(tx, sessionHash, id)
	if err != nil {
		return PreviewAccess{}, err
	}
	files, reports, err := p.PreviewFiles(version)
	if err != nil {
		return PreviewAccess{}, err
	}
	if core.PreviewHTML(reports, files) == "" {
		return PreviewAccess{}, errors.New("当前版本尚未生成可运行页面")
	}
	c, err := contractAt(tx, p, files)
	if err != nil {
		return PreviewAccess{}, err
	}
	out := PreviewAccess{Mode: "legacy", Contract: c, Version: version}
	if c == nil {
		return out, nil
	}
	out.Mode = "trial"
	for _, v := range p.Versions {
		if v.ID == p.Head && v.Manifest.Digest() == files.Digest() && (version == "" || version == p.Head) {
			out.Mode = "formal"
			out.Version = v.ID
			break
		}
	}
	if out.Mode != "formal" {
		return out, nil
	}
	state, err := readAppState(tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		state = core.AppState{Schema: c.Schema, Revision: 0, Value: c.Initial, Updated: core.Now()}
		if err = checkStateQuota(tx, p.Owner, id, len(state.Value)); err != nil {
			return out, err
		}
		_, err = tx.Exec(`INSERT INTO app_states VALUES(?,?,?,?,?)`, id, state.Schema, state.Revision, []byte(state.Value), state.Updated)
	}
	if err != nil {
		return out, err
	}
	out.State = &state
	if state.Schema != c.Schema {
		out.Mode = "incompatible"
		return out, tx.Commit()
	}
	token := core.ID() + core.ID()
	now := time.Now().Unix()
	_, err = tx.Exec(`INSERT INTO preview_grants VALUES(?,?,?,?,?,?)`, core.Hash([]byte(token)), sessionHash, id, out.Version, c.Schema, now+900)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(`DELETE FROM preview_grants WHERE expires_at<?`, now); err != nil {
		return out, err
	}
	out.Grant = token
	return out, tx.Commit()
}

func validateGrant(tx *sql.Tx, sessionHash, id, grant string) (*core.Project, int, error) {
	p, err := sharedProject(tx, sessionHash, id)
	if err != nil {
		return nil, 0, err
	}
	var version, session string
	var schema int
	var expires int64
	err = tx.QueryRow(`SELECT session_hash,version_id,schema_version,expires_at FROM preview_grants WHERE token_hash=? AND project_id=?`, core.Hash([]byte(grant)), id).Scan(&session, &version, &schema, &expires)
	if err != nil || session != sessionHash || expires <= time.Now().Unix() || version != p.Head {
		return nil, 0, ErrGrant
	}
	return p, schema, nil
}

func (s *Store) LoadAppState(sessionHash, id, grant string) (core.AppState, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return core.AppState{}, err
	}
	defer tx.Rollback()
	_, schema, err := validateGrant(tx, sessionHash, id, grant)
	if err != nil {
		return core.AppState{}, err
	}
	state, err := readAppState(tx, id)
	if err == nil && state.Schema != schema {
		err = ErrSchema
	}
	return state, err
}

type SaveAppRequest struct {
	Grant     string          `json:"grant"`
	Expected  int64           `json:"expected_revision"`
	RequestID string          `json:"request_id"`
	Value     json.RawMessage `json:"value"`
	Import    bool            `json:"import,omitempty"`
	Schema    int             `json:"schema_version"`
}

func checkStateQuota(tx *sql.Tx, owner, id string, size int) error {
	var total int
	err := tx.QueryRow(`SELECT COALESCE(SUM(length(a.value)),0) FROM app_states a JOIN projects p ON p.id=a.project_id WHERE p.id<>? AND (p.owner=? OR p.owner IN(SELECT workspace_id FROM workspace_accounts WHERE user_id=(SELECT user_id FROM workspace_accounts WHERE workspace_id=?)))`, id, owner, owner).Scan(&total)
	if err != nil {
		return err
	}
	if total+size > 8*1024*1024 {
		return errors.New("账号应用数据超过 8 MiB，请导出备份后清理")
	}
	return nil
}

func backupState(tx *sql.Tx, id string, state core.AppState) error {
	_, err := tx.Exec(`INSERT INTO app_backups VALUES(?,?,?,?,?,?)`, core.ID(), id, state.Schema, state.Revision, []byte(state.Value), core.Now())
	if err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM app_backups WHERE project_id=? AND id NOT IN(SELECT id FROM app_backups WHERE project_id=? ORDER BY created DESC LIMIT 3)`, id, id)
	return err
}

func (s *Store) SaveAppState(sessionHash, id string, in SaveAppRequest) (int64, error) {
	if len(in.RequestID) < 8 || len(in.RequestID) > 100 || in.Expected < 0 {
		return 0, errors.New("保存请求标识或修订号无效")
	}
	if err := core.ValidateAppValue(in.Value); err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	p, schema, err := validateGrant(tx, sessionHash, id, in.Grant)
	if err != nil {
		return 0, err
	}
	if schema != in.Schema {
		return 0, ErrSchema
	}
	// Grant renewal must not alter the identity of a retry.
	identity := in
	identity.Grant = ""
	hash := core.Hash([]byte(core.JSON(identity)))
	var previousHash string
	var revision int64
	err = tx.QueryRow(`SELECT request_hash,revision FROM app_receipts WHERE project_id=? AND request_id=?`, id, in.RequestID).Scan(&previousHash, &revision)
	if err == nil {
		if previousHash != hash {
			return 0, errors.New("同一保存请求标识不能用于不同内容")
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
	if state.Schema != schema {
		return 0, ErrSchema
	}
	if state.Revision != in.Expected {
		return 0, ErrConflict
	}
	if err = checkStateQuota(tx, p.Owner, id, len(in.Value)); err != nil {
		return 0, err
	}
	if in.Import {
		if err = backupState(tx, id, state); err != nil {
			return 0, err
		}
	}
	revision = state.Revision + 1
	if _, err = tx.Exec(`UPDATE app_states SET value=?,revision=?,updated=? WHERE project_id=?`, []byte(in.Value), revision, core.Now(), id); err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	if _, err = tx.Exec(`INSERT INTO app_receipts VALUES(?,?,?,?,?)`, id, in.RequestID, hash, revision, now); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`DELETE FROM app_receipts WHERE project_id=? AND (created<? OR request_id NOT IN(SELECT request_id FROM app_receipts WHERE project_id=? ORDER BY revision DESC LIMIT 1000))`, id, now-7*86400, id); err != nil {
		return 0, err
	}
	return revision, tx.Commit()
}

func (s *Store) AppBackups(sessionHash, id string) ([]map[string]any, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = sharedProject(tx, sessionHash, id); err != nil {
		return nil, err
	}
	rows, err := tx.Query(`SELECT id,schema_version,revision,value,created FROM app_backups WHERE project_id=? ORDER BY created DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var bid, created string
		var state core.AppState
		if err = rows.Scan(&bid, &state.Schema, &state.Revision, &state.Value, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": bid, "created": created, "state": state})
	}
	return out, rows.Err()
}
