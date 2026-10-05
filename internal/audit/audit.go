// Package audit records security-relevant and administrative actions.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"strings"
	"time"
)

type Entry struct {
	ID     int64          `json:"id"`
	Time   time.Time      `json:"time"`
	Actor  string         `json:"actor"`
	Action string         `json:"action"`
	Target string         `json:"target"`
	IP     string         `json:"ip"`
	Detail map[string]any `json:"detail,omitempty"`
}

type Log struct {
	db *sql.DB
}

func New(db *sql.DB) *Log { return &Log{db: db} }

// Record stores an entry. Failures are logged, never returned: auditing must
// not break the action being audited.
func (l *Log) Record(ctx context.Context, actor, action, target, ip string, detail map[string]any) {
	var d string
	if len(detail) > 0 {
		b, _ := json.Marshal(detail)
		d = string(b)
	}
	if _, err := l.db.ExecContext(context.WithoutCancel(ctx),
		`INSERT INTO audit_log (ts, actor, action, target, ip, detail) VALUES (?, ?, ?, ?, ?, ?)`,
		time.Now().Unix(), actor, action, target, ip, d); err != nil {
		slog.Error("audit log write failed", "action", action, "err", err)
	}
}

type Query struct {
	// Search matches actor, action or target (substring).
	Search string
	// Before returns entries with ID lower than this (cursor), 0 for newest.
	Before int64
	Limit  int
}

func (l *Log) List(ctx context.Context, q Query) ([]Entry, error) {
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}
	where := []string{"1=1"}
	var args []any
	if q.Before > 0 {
		where = append(where, "id < ?")
		args = append(args, q.Before)
	}
	if s := strings.TrimSpace(q.Search); s != "" {
		where = append(where, "(actor LIKE ? OR action LIKE ? OR target LIKE ?)")
		like := "%" + s + "%"
		args = append(args, like, like, like)
	}
	args = append(args, q.Limit)
	rows, err := l.db.QueryContext(ctx,
		`SELECT id, ts, actor, action, target, ip, detail FROM audit_log WHERE `+strings.Join(where, " AND ")+
			` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var e Entry
		var ts int64
		var detail string
		if err := rows.Scan(&e.ID, &ts, &e.Actor, &e.Action, &e.Target, &e.IP, &detail); err != nil {
			return nil, err
		}
		e.Time = time.Unix(ts, 0)
		if detail != "" {
			json.Unmarshal([]byte(detail), &e.Detail)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Prune deletes entries older than retention.
func (l *Log) Prune(ctx context.Context, retention time.Duration) error {
	_, err := l.db.ExecContext(ctx, `DELETE FROM audit_log WHERE ts < ?`, time.Now().Add(-retention).Unix())
	return err
}
