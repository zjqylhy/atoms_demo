package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
)

var ErrOrderConflict = errors.New("项目列表或共享顺序已变化，请使用最新顺序重试")
var ErrInvalidOrder = errors.New("项目顺序必须包含全部项目，且不能包含重复或无效 ID")

type ProjectOrder struct {
	Revision int64            `json:"revision"`
	Projects []map[string]any `json:"projects"`
}

func readProjectOrder(tx *sql.Tx) (ProjectOrder, error) {
	var out ProjectOrder
	var raw []byte
	if err := tx.QueryRow(`SELECT revision,ids FROM project_order WHERE id=1`).Scan(&out.Revision, &raw); err != nil {
		return out, err
	}
	var saved []string
	if err := json.Unmarshal(raw, &saved); err != nil {
		return out, err
	}
	rows, err := tx.Query(`SELECT id,title,updated,COALESCE(json_extract(state,'$.created'),updated) FROM projects ORDER BY updated DESC,id`)
	if err != nil {
		return out, err
	}
	out.Projects = []map[string]any{}
	created := map[string]string{}
	for rows.Next() {
		var id, title, updated, at string
		if err = rows.Scan(&id, &title, &updated, &at); err != nil {
			rows.Close()
			return out, err
		}
		out.Projects = append(out.Projects, map[string]any{"id": id, "title": title, "updated": updated})
		created[id] = strings.TrimSuffix(at, "Z")
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	if len(saved) > 0 {
		ranks := map[string]int{}
		for index, id := range saved {
			ranks[id] = index
		}
		// Newly created projects precede the saved order. Content updates never
		// move a ranked project, and deleted IDs cannot reappear in this view.
		sort.SliceStable(out.Projects, func(i, j int) bool {
			a, b := out.Projects[i]["id"].(string), out.Projects[j]["id"].(string)
			ra, ka := ranks[a]
			rb, kb := ranks[b]
			if ka != kb {
				return !ka
			}
			if ka {
				return ra < rb
			}
			if created[a] == created[b] {
				return a < b
			}
			return created[a] > created[b]
		})
	}
	return out, nil
}

func (s *Store) ProjectOrder(sessionHash string) (ProjectOrder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return ProjectOrder{}, err
	}
	defer tx.Rollback()
	if _, err = readAccount(tx, sessionHash); err != nil {
		return ProjectOrder{}, err
	}
	return readProjectOrder(tx)
}

func (s *Store) SaveProjectOrder(sessionHash string, revision int64, ids []string) (ProjectOrder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return ProjectOrder{}, err
	}
	defer tx.Rollback()
	if _, err = readAccount(tx, sessionHash); err != nil {
		return ProjectOrder{}, err
	}
	current, err := readProjectOrder(tx)
	if err != nil {
		return current, err
	}
	seen := map[string]bool{}
	if revision < 0 {
		return current, ErrInvalidOrder
	}
	for _, id := range ids {
		if len(id) != 32 || seen[id] {
			return current, ErrInvalidOrder
		}
		seen[id] = true
	}
	if len(ids) != len(current.Projects) {
		return current, ErrOrderConflict
	}
	before := make([]string, 0, len(current.Projects))
	for _, p := range current.Projects {
		id := p["id"].(string)
		if !seen[id] {
			return current, ErrOrderConflict
		}
		before = append(before, id)
	}
	// A lost successful response can safely be retried without another revision.
	if revision != current.Revision {
		if slices.Equal(before, ids) {
			return current, nil
		}
		return current, ErrOrderConflict
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return current, err
	}
	if _, err = tx.Exec(`UPDATE project_order SET ids=?,revision=revision+1 WHERE id=1`, raw); err != nil {
		return current, err
	}
	result, err := readProjectOrder(tx)
	if err != nil {
		return current, err
	}
	return result, tx.Commit()
}
