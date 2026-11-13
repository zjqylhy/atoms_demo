package store

import (
	"database/sql"
	"errors"
	"mockatoms/internal/core"
	"time"
)

var ErrSession = errors.New("登录或游客会话已失效，请重新登录或刷新页面")
var ErrAuthLimit = errors.New("尝试过于频繁，请 15 分钟后重试")

type Session struct {
	TokenHash string `json:"-"`
	UserID    string `json:"id,omitempty"`
	Username  string `json:"username,omitempty"`
	Workspace string `json:"-"`
	Expires   int64  `json:"-"`
}

type rowReader interface{ QueryRow(string, ...any) *sql.Row }

func readSession(q rowReader, hash string) (Session, error) {
	var out Session
	var seen, revoked int64
	err := q.QueryRow(`SELECT s.token_hash,s.user_id,s.workspace_id,COALESCE(u.username,''),s.expires_at,s.seen_at,s.revoked_at FROM sessions s LEFT JOIN users u ON u.id=s.user_id WHERE s.token_hash=?`, hash).Scan(&out.TokenHash, &out.UserID, &out.Workspace, &out.Username, &out.Expires, &seen, &revoked)
	if err != nil {
		return out, err
	}
	now := time.Now().Unix()
	if revoked != 0 || out.Expires <= now || out.UserID != "" && (seen < now-7*86400 || out.Username == "") {
		return Session{}, ErrSession
	}
	if out.UserID == "" {
		var claimed int
		if err = q.QueryRow(`SELECT COUNT(*) FROM workspace_accounts WHERE workspace_id=?`, out.Workspace).Scan(&claimed); err != nil {
			return Session{}, err
		}
		if claimed != 0 {
			return Session{}, ErrSession
		}
	}
	return out, nil
}

func (s *Store) Session(token string, legacy bool) (Session, error) {
	if len(token) != 64 {
		return Session{}, ErrSession
	}
	hash := core.Hash([]byte(token))
	out, err := readSession(s.db, hash)
	if errors.Is(err, sql.ErrNoRows) && legacy {
		// A revoked/expired session never takes this branch. Claimed workspaces
		// cannot be reopened by replaying their pre-account cookie.
		now := time.Now().Unix()
		_, err = s.db.Exec(`INSERT OR IGNORE INTO sessions(token_hash,workspace_id,expires_at,seen_at) SELECT token_hash,id,?,? FROM workspaces WHERE token_hash=? AND NOT EXISTS(SELECT 1 FROM workspace_accounts WHERE workspace_id=workspaces.id)`, now+365*86400, now, hash)
		if err == nil {
			out, err = readSession(s.db, hash)
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrSession
	}
	if err == nil {
		_, err = s.db.Exec(`UPDATE sessions SET seen_at=? WHERE token_hash=? AND revoked_at=0 AND seen_at<?`, time.Now().Unix(), hash, time.Now().Unix()-60)
	}
	return out, err
}

func (s *Store) NewGuest() (string, Session, error) {
	token := core.ID() + core.ID()
	_, err := s.Workspace(token, true)
	if err != nil {
		return "", Session{}, err
	}
	session, err := s.Session(token, true)
	return token, session, err
}

func (s *Store) Credential(username string) (string, string, error) {
	var id, hash string
	err := s.db.QueryRow(`SELECT id,password_hash FROM users WHERE username=?`, username).Scan(&id, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return id, hash, err
}

func claimWorkspace(tx *sql.Tx, guest Session, userID string) error {
	current, err := readSession(tx, guest.TokenHash)
	if err != nil || current.UserID != "" || current.Workspace != guest.Workspace {
		return ErrSession
	}
	if _, err = tx.Exec(`INSERT INTO workspace_accounts(workspace_id,user_id,created) VALUES(?,?,?)`, guest.Workspace, userID, core.Now()); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE sessions SET revoked_at=? WHERE workspace_id=? AND user_id=''`, time.Now().Unix(), guest.Workspace); err != nil {
		return err
	}
	// Also invalidate the legacy lookup, including when an old server is started.
	_, err = tx.Exec(`UPDATE workspaces SET token_hash=? WHERE id=?`, "claimed:"+core.ID(), guest.Workspace)
	return err
}

func issueAccount(tx *sql.Tx, userID, workspace string) (string, error) {
	token := core.ID() + core.ID()
	now := time.Now().Unix()
	_, err := tx.Exec(`INSERT INTO sessions(token_hash,user_id,workspace_id,expires_at,seen_at) VALUES(?,?,?,?,?)`, core.Hash([]byte(token)), userID, workspace, now+30*86400, now)
	return token, err
}

func (s *Store) Register(guest Session, username, passwordHash string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var exists int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM users WHERE username=?`, username).Scan(&exists); err != nil {
		return "", err
	}
	if exists != 0 {
		return "", errors.New("用户名已被使用")
	}
	id := core.ID()
	if _, err = tx.Exec(`INSERT INTO users(id,username,password_hash,workspace_id,created) VALUES(?,?,?,?,?)`, id, username, passwordHash, guest.Workspace, core.Now()); err != nil {
		return "", err
	}
	if err = claimWorkspace(tx, guest, id); err != nil {
		return "", err
	}
	token, err := issueAccount(tx, id, guest.Workspace)
	if err != nil {
		return "", err
	}
	return token, tx.Commit()
}

func (s *Store) Login(userID string, guest *Session) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var workspace string
	if err = tx.QueryRow(`SELECT workspace_id FROM users WHERE id=?`, userID).Scan(&workspace); err != nil {
		return "", ErrSession
	}
	if guest != nil {
		if err = claimWorkspace(tx, *guest, userID); err != nil {
			return "", err
		}
	}
	token, err := issueAccount(tx, userID, workspace)
	if err != nil {
		return "", err
	}
	return token, tx.Commit()
}

func (s *Store) RevokeSession(hash string) error {
	_, err := s.db.Exec(`UPDATE sessions SET revoked_at=? WHERE token_hash=?`, time.Now().Unix(), hash)
	return err
}

func (s *Store) SessionValid(hash string) bool { _, err := readSession(s.db, hash); return err == nil }

func (s *Store) ProjectOwner(session Session, id string) (string, error) {
	var workspace string
	err := s.db.QueryRow(`SELECT owner FROM projects WHERE id=? AND (owner=? OR (?<>'' AND owner IN(SELECT workspace_id FROM workspace_accounts WHERE user_id=?)))`, id, session.Workspace, session.UserID, session.UserID).Scan(&workspace)
	if errors.Is(err, sql.ErrNoRows) {
		err = core.ErrNotFound
	}
	return workspace, err
}

// Principal is stable across all workspaces attached to one account.
func (s *Store) Principal(owner string) (string, error) {
	var user string
	err := s.db.QueryRow(`SELECT user_id FROM workspace_accounts WHERE workspace_id=?`, owner).Scan(&user)
	if errors.Is(err, sql.ErrNoRows) {
		return "guest:" + owner, nil
	}
	return "user:" + user, err
}

// Charge attempts before hashing; counters survive a restart and are bounded by expiry.
func (s *Store) AuthAttempt(username, ip string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	window := now / 900
	for _, limit := range []struct {
		key string
		max int
	}{{"ip:" + ip, 30}, {"pair:" + username + ":" + ip, 5}} {
		key := core.Hash([]byte(limit.key))
		var count int
		err = tx.QueryRow(`SELECT attempts FROM auth_attempts WHERE bucket=? AND window_at=?`, key, window).Scan(&count)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if count >= limit.max {
			return ErrAuthLimit
		}
		if _, err = tx.Exec(`INSERT INTO auth_attempts(bucket,window_at,attempts) VALUES(?,?,1) ON CONFLICT(bucket) DO UPDATE SET window_at=excluded.window_at,attempts=CASE WHEN auth_attempts.window_at=excluded.window_at THEN auth_attempts.attempts+1 ELSE 1 END`, key, window); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`DELETE FROM auth_attempts WHERE window_at<?`, window-96); err != nil {
		return err
	}
	return tx.Commit()
}
