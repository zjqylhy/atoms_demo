package store

import (
	"errors"
	"mockatoms/internal/core"
	"time"
)

// ReserveModelWindow uses a conservative input estimate plus output ceiling.
// Reservations survive process restarts and are not refunded for unknown calls.
func (s *Store) ReserveModelWindow(tokens, limit int) (time.Duration, error) {
	if limit <= 0 {
		return 0, nil
	}
	if tokens > limit {
		return 0, errors.New("单次模型输入与输出预留超过每分钟额度，请缩小任务范围")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	if _, err = tx.Exec("DELETE FROM model_window WHERE stamp_ms<=?", now-60000); err != nil {
		return 0, err
	}
	var used int
	var earliest int64
	if err = tx.QueryRow("SELECT COALESCE(SUM(tokens),0),COALESCE(MIN(stamp_ms),0) FROM model_window").Scan(&used, &earliest); err != nil {
		return 0, err
	}
	if used+tokens > limit {
		return time.Duration(earliest+60200-now) * time.Millisecond, nil
	}
	if _, err = tx.Exec("INSERT INTO model_window VALUES(?,?,?)", core.ID(), now, tokens); err != nil {
		return 0, err
	}
	return 0, tx.Commit()
}
