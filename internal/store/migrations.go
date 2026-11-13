package store

// Additive schema changes preserve legacy project JSON, blobs and quota ledgers.
func (s *Store) migrate() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS users (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, workspace_id TEXT NOT NULL, created TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS sessions (token_hash TEXT PRIMARY KEY, user_id TEXT NOT NULL DEFAULT '', workspace_id TEXT NOT NULL, expires_at INTEGER NOT NULL, seen_at INTEGER NOT NULL, revoked_at INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS workspace_accounts (workspace_id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id), created TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS workspace_account_user ON workspace_accounts(user_id)`,
		`CREATE TABLE IF NOT EXISTS auth_attempts (bucket TEXT PRIMARY KEY, window_at INTEGER NOT NULL, attempts INTEGER NOT NULL)`,
	} {
		if _, err = tx.Exec(statement); err != nil {
			return err
		}
	}
	return tx.Commit()
}
