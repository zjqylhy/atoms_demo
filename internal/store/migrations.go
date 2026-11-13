package store

// Additive schema changes preserve legacy project JSON, blobs and quota ledgers.
func (s *Store) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS project_order (id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL, ids BLOB NOT NULL)`,
		`INSERT OR IGNORE INTO project_order VALUES(1,0,'[]')`,
		`CREATE TABLE IF NOT EXISTS users (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, workspace_id TEXT NOT NULL, created TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS sessions (token_hash TEXT PRIMARY KEY, user_id TEXT NOT NULL DEFAULT '', workspace_id TEXT NOT NULL, expires_at INTEGER NOT NULL, seen_at INTEGER NOT NULL, revoked_at INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS workspace_accounts (workspace_id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id), created TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS workspace_account_user ON workspace_accounts(user_id)`,
		`CREATE TABLE IF NOT EXISTS auth_attempts (bucket TEXT PRIMARY KEY, window_at INTEGER NOT NULL, attempts INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS app_states (project_id TEXT PRIMARY KEY REFERENCES projects(id), schema_version INTEGER NOT NULL, revision INTEGER NOT NULL, value BLOB NOT NULL, updated TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS preview_grants (token_hash TEXT PRIMARY KEY, session_hash TEXT NOT NULL, project_id TEXT NOT NULL, version_id TEXT NOT NULL, schema_version INTEGER NOT NULL, expires_at INTEGER NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS preview_grant_session ON preview_grants(session_hash)`,
		`CREATE TABLE IF NOT EXISTS app_receipts (project_id TEXT NOT NULL, request_id TEXT NOT NULL, request_hash TEXT NOT NULL, revision INTEGER NOT NULL, created INTEGER NOT NULL, PRIMARY KEY(project_id,request_id))`,
		`CREATE TABLE IF NOT EXISTS app_backups (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, schema_version INTEGER NOT NULL, revision INTEGER NOT NULL, value BLOB NOT NULL, created TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS project_copies (session_hash TEXT NOT NULL, request_id TEXT NOT NULL, source TEXT NOT NULL, project_id TEXT NOT NULL, PRIMARY KEY(session_hash,request_id))`,
		`CREATE TABLE IF NOT EXISTS share_links (id TEXT PRIMARY KEY, token_hash TEXT UNIQUE NOT NULL, project_id TEXT NOT NULL, version_id TEXT NOT NULL, title TEXT NOT NULL, created TEXT NOT NULL, revoked_at TEXT NOT NULL DEFAULT '')`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}
