package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type SessionID string

type Usage struct{ Input, Output, CacheRead, CacheWrite int64 }

func (u Usage) Add(v Usage) Usage {
	return Usage{u.Input + v.Input, u.Output + v.Output, u.CacheRead + v.CacheRead, u.CacheWrite + v.CacheWrite}
}

func (u Usage) Total() int64 { return u.Input + u.Output + u.CacheRead + u.CacheWrite }

func (u Usage) IsZero() bool { return u == Usage{} }

func (u Usage) valid() bool {
	return u.Input >= 0 && u.Output >= 0 && u.CacheRead >= 0 && u.CacheWrite >= 0
}

type Session struct {
	ID      SessionID
	Project string
	Agent   string
	Task    string
	Start   time.Time
	End     time.Time // zero => open
	Usage   map[string]Usage
}

func (s Session) Open() bool { return s.End.IsZero() }

func (s Session) Duration(now time.Time) time.Duration {
	if s.Open() {
		return now.Sub(s.Start)
	}
	return s.End.Sub(s.Start)
}

func (s Session) TotalUsage() Usage {
	var t Usage
	for _, u := range s.Usage {
		t = t.Add(u)
	}
	return t
}

type NewSession struct {
	ID                   SessionID
	Project, Agent, Task string
	Start                time.Time
}

type UsageRecord struct {
	Model string
	Usage Usage
	At    time.Time
}

// Amendment fields left nil are unchanged.
type Amendment struct {
	Project, Agent, Task *string
	Start, End           *time.Time
}

// Filter zero values mean no constraint. Limit keeps the newest N sessions.
type Filter struct {
	Project  string
	OpenOnly bool
	Since    time.Time
	Limit    int
}

// Fixed-width so that string comparison in the CHECK constraint and in ORDER BY matches time order.
const timeLayout = "2006-01-02T15:04:05Z"

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	return t.UTC(), err
}

func (s *Store) Start(n NewSession) error {
	_, err := s.db.Exec(`INSERT INTO sessions (id, project, agent, task, started_at) VALUES (?, ?, ?, ?, ?)`,
		string(n.ID), n.Project, n.Agent, n.Task, formatTime(n.Start))
	return err
}

func (s *Store) Stop(id SessionID, at time.Time, usage *UsageRecord) error {
	if usage != nil && !usage.Usage.valid() {
		return fmt.Errorf("%w: negative token count", ErrInvalid)
	}
	return s.tx(func(tx *sql.Tx) error {
		start, end, err := sessionBounds(tx, id)
		if err != nil {
			return err
		}
		if !end.IsZero() {
			return ErrAlreadyStopped
		}
		if at.Before(start) {
			return fmt.Errorf("%w: end %s is before start %s", ErrInvalid, formatTime(at), formatTime(start))
		}
		if usage != nil {
			if err := insertUsage(tx, id, UsageRecord{Model: usage.Model, Usage: usage.Usage, At: at}); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`UPDATE sessions SET ended_at = ? WHERE id = ?`, formatTime(at), string(id))
		return err
	})
}

func (s *Store) RecordUsage(id SessionID, u UsageRecord) error {
	if !u.Usage.valid() {
		return fmt.Errorf("%w: negative token count", ErrInvalid)
	}
	return s.tx(func(tx *sql.Tx) error {
		_, end, err := sessionBounds(tx, id)
		if err != nil {
			return err
		}
		if !end.IsZero() {
			return ErrClosed
		}
		return insertUsage(tx, id, u)
	})
}

func (s *Store) Amend(id SessionID, a Amendment) error {
	return s.tx(func(tx *sql.Tx) error {
		var cur Session
		var start string
		var end sql.NullString
		err := tx.QueryRow(`SELECT project, agent, task, started_at, ended_at FROM sessions WHERE id = ?`, string(id)).
			Scan(&cur.Project, &cur.Agent, &cur.Task, &start, &end)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if cur.Start, err = parseTime(start); err != nil {
			return err
		}
		if end.Valid {
			if cur.End, err = parseTime(end.String); err != nil {
				return err
			}
		}
		if a.Project != nil {
			cur.Project = *a.Project
		}
		if a.Agent != nil {
			cur.Agent = *a.Agent
		}
		if a.Task != nil {
			cur.Task = *a.Task
		}
		if a.Start != nil {
			cur.Start = a.Start.UTC().Truncate(time.Second)
		}
		if a.End != nil {
			cur.End = a.End.UTC().Truncate(time.Second)
		}
		if !cur.End.IsZero() && cur.End.Before(cur.Start) {
			return fmt.Errorf("%w: end %s is before start %s", ErrInvalid, formatTime(cur.End), formatTime(cur.Start))
		}
		var endVal any
		if !cur.End.IsZero() {
			endVal = formatTime(cur.End)
		}
		_, err = tx.Exec(`UPDATE sessions SET project = ?, agent = ?, task = ?, started_at = ?, ended_at = ? WHERE id = ?`,
			cur.Project, cur.Agent, cur.Task, formatTime(cur.Start), endVal, string(id))
		return err
	})
}

// Resolve matches by prefix with substr rather than LIKE because "_" in "s_" is a LIKE wildcard.
func (s *Store) Resolve(prefix string) (SessionID, error) {
	if prefix == "" {
		return "", ErrNotFound
	}
	rows, err := s.db.Query(`SELECT id FROM sessions WHERE substr(id, 1, ?) = ? LIMIT 2`, len([]rune(prefix)), prefix)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var ids []SessionID
	for rows.Next() {
		var id SessionID
		if err := rows.Scan(&id); err != nil {
			return "", err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	switch len(ids) {
	case 0:
		return "", ErrNotFound
	case 1:
		return ids[0], nil
	}
	return "", ErrAmbiguous
}

func (s *Store) Get(id SessionID) (Session, error) {
	sessions, err := s.query(`WHERE id = ?`, []any{string(id)}, 0)
	if err != nil {
		return Session{}, err
	}
	if len(sessions) == 0 {
		return Session{}, ErrNotFound
	}
	return sessions[0], nil
}

func (s *Store) List(f Filter) ([]Session, error) {
	var conds []string
	var args []any
	if f.Project != "" {
		conds = append(conds, "project = ?")
		args = append(args, f.Project)
	}
	if f.OpenOnly {
		conds = append(conds, "ended_at IS NULL")
	}
	if !f.Since.IsZero() {
		conds = append(conds, "started_at >= ?")
		args = append(args, formatTime(f.Since))
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	return s.query(where, args, f.Limit)
}

func (s *Store) query(where string, args []any, limit int) ([]Session, error) {
	q := `SELECT id, project, agent, task, started_at, ended_at FROM sessions ` + where
	if limit > 0 {
		q = `SELECT * FROM (` + q + ` ORDER BY started_at DESC, id DESC LIMIT ` + fmt.Sprint(limit) + `)`
	}
	rows, err := s.db.Query(q+` ORDER BY started_at, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	byID := map[SessionID]int{}
	for rows.Next() {
		var sess Session
		var start string
		var end sql.NullString
		if err := rows.Scan(&sess.ID, &sess.Project, &sess.Agent, &sess.Task, &start, &end); err != nil {
			return nil, err
		}
		if sess.Start, err = parseTime(start); err != nil {
			return nil, err
		}
		if end.Valid {
			if sess.End, err = parseTime(end.String); err != nil {
				return nil, err
			}
		}
		sess.Usage = map[string]Usage{}
		byID[sess.ID] = len(out)
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}

	urows, err := s.db.Query(`SELECT session_id, model, SUM(input), SUM(output), SUM(cache_read), SUM(cache_write)
		FROM token_usage WHERE session_id IN (SELECT id FROM sessions `+where+`) GROUP BY session_id, model`, args...)
	if err != nil {
		return nil, err
	}
	defer urows.Close()
	for urows.Next() {
		var id SessionID
		var model string
		var u Usage
		if err := urows.Scan(&id, &model, &u.Input, &u.Output, &u.CacheRead, &u.CacheWrite); err != nil {
			return nil, err
		}
		if i, ok := byID[id]; ok {
			out[i].Usage[model] = u
		}
	}
	return out, urows.Err()
}

func sessionBounds(tx *sql.Tx, id SessionID) (start, end time.Time, err error) {
	var s string
	var e sql.NullString
	err = tx.QueryRow(`SELECT started_at, ended_at FROM sessions WHERE id = ?`, string(id)).Scan(&s, &e)
	if errors.Is(err, sql.ErrNoRows) {
		return start, end, ErrNotFound
	}
	if err != nil {
		return start, end, err
	}
	if start, err = parseTime(s); err != nil {
		return
	}
	if e.Valid {
		end, err = parseTime(e.String)
	}
	return
}

func insertUsage(tx *sql.Tx, id SessionID, u UsageRecord) error {
	_, err := tx.Exec(`INSERT INTO token_usage (session_id, model, input, output, cache_read, cache_write, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		string(id), u.Model, u.Usage.Input, u.Usage.Output, u.Usage.CacheRead, u.Usage.CacheWrite, formatTime(u.At))
	return err
}
