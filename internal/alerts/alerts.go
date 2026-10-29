// Package alerts records problems that need an administrator's attention and
// announces new ones through webhooks.
package alerts

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"acs/internal/object"
	"acs/internal/webhook"
)

// Alert kinds (also the webhook event names).
const (
	KindObjectMissing  = "object.missing"
	KindObjectCorrupt  = "object.corrupt"
	KindBackendOffline = "backend.offline"
	KindReplicaFailed  = "replica.failed"
	KindForeignStorage = "storage.foreign"
)

var ErrNotFound = errors.New("alert not found")

type Alert struct {
	ID         int64      `json:"id"`
	Kind       string     `json:"kind"`
	Severity   string     `json:"severity"`
	Title      string     `json:"title"`
	Detail     string     `json:"detail,omitempty"`
	Bucket     string     `json:"bucket,omitempty"`
	Key        string     `json:"key,omitempty"`
	Backend    string     `json:"backend,omitempty"`
	Count      int        `json:"count"`
	FirstSeen  time.Time  `json:"firstSeen"`
	LastSeen   time.Time  `json:"lastSeen"`
	ResolvedAt *time.Time `json:"resolvedAt,omitempty"`
}

type Service struct {
	db    *sql.DB
	hooks *webhook.Manager
}

func New(db *sql.DB, hooks *webhook.Manager) *Service { return &Service{db: db, hooks: hooks} }

// Raise opens (or re-opens) the alert with this fingerprint. Webhooks fire
// only when an alert becomes open, not on every repeat.
func (s *Service) Raise(ctx context.Context, fingerprint string, a Alert) {
	now := time.Now().Unix()
	var id int64
	var resolved sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, resolved_at FROM alerts WHERE fingerprint = ?`, fingerprint).Scan(&id, &resolved)
	isNew := false
	switch {
	case errors.Is(err, sql.ErrNoRows):
		isNew = true
		_, err = s.db.ExecContext(ctx, `INSERT INTO alerts (fingerprint, kind, severity, title, detail, bucket, object_key, backend, first_seen, last_seen)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, fingerprint, a.Kind, a.Severity, a.Title, a.Detail, a.Bucket, a.Key, a.Backend, now, now)
	case err == nil:
		isNew = resolved.Valid
		_, err = s.db.ExecContext(ctx, `UPDATE alerts SET last_seen = ?, count = count + 1, resolved_at = NULL, title = ?, detail = ? WHERE id = ?`,
			now, a.Title, a.Detail, id)
	}
	if err != nil {
		slog.Error("record alert", "kind", a.Kind, "err", err)
		return
	}
	if isNew && s.hooks != nil {
		s.hooks.Publish(webhook.Event{Type: a.Kind, Bucket: a.Bucket, Key: a.Key, Data: map[string]any{
			"title": a.Title, "detail": a.Detail, "severity": a.Severity, "bucket": a.Bucket, "key": a.Key, "backend": a.Backend,
		}})
	}
}

// ResolveFingerprint closes an open alert (e.g. a backend came back).
func (s *Service) ResolveFingerprint(ctx context.Context, fingerprint string) {
	s.db.ExecContext(ctx, `UPDATE alerts SET resolved_at = ? WHERE fingerprint = ? AND resolved_at IS NULL`, time.Now().Unix(), fingerprint)
}

// Resolve closes an alert by ID (an administrator dismissed it).
func (s *Service) Resolve(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE alerts SET resolved_at = ? WHERE id = ? AND resolved_at IS NULL`, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ResolveAll closes every open alert.
func (s *Service) ResolveAll(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alerts SET resolved_at = ? WHERE resolved_at IS NULL`, time.Now().Unix())
	return err
}

// OpenCount returns the number of open alerts.
func (s *Service) OpenCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM alerts WHERE resolved_at IS NULL`).Scan(&n)
	return n, err
}

// List returns alerts, newest first. state is "open", "resolved" or "all".
func (s *Service) List(ctx context.Context, state string, limit int) ([]Alert, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	where := "resolved_at IS NULL"
	switch state {
	case "resolved":
		where = "resolved_at IS NOT NULL"
	case "all":
		where = "1=1"
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, severity, title, detail, bucket, object_key, backend, count, first_seen, last_seen, resolved_at
		FROM alerts WHERE `+where+` ORDER BY last_seen DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		var a Alert
		var first, last int64
		var resolved sql.NullInt64
		if err := rows.Scan(&a.ID, &a.Kind, &a.Severity, &a.Title, &a.Detail, &a.Bucket, &a.Key, &a.Backend, &a.Count, &first, &last, &resolved); err != nil {
			return nil, err
		}
		a.FirstSeen, a.LastSeen = time.Unix(first, 0), time.Unix(last, 0)
		if resolved.Valid {
			t := time.Unix(resolved.Int64, 0)
			a.ResolvedAt = &t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---- integrity checker ----

// Checker runs object inventories and turns their findings into alerts.
type Checker struct {
	objects *object.Service
	alerts  *Service
	names   func(backendID string) string

	mu      sync.Mutex
	running bool
	last    *Report
}

// Report summarises the last integrity check.
type Report struct {
	StartedAt time.Time     `json:"startedAt"`
	Deep      bool          `json:"deep"`
	Objects   int           `json:"objects"`
	Blobs     int           `json:"blobs"`
	Missing   int           `json:"missing"`
	Corrupt   int           `json:"corrupt"`
	Offline   int           `json:"offline"`
	Took      time.Duration `json:"took"`
	Error     string        `json:"error,omitempty"`
}

func NewChecker(objects *object.Service, alerts *Service, names func(string) string) *Checker {
	return &Checker{objects: objects, alerts: alerts, names: names}
}

// Last returns the most recent report and whether a check is running.
func (c *Checker) Last() (*Report, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last, c.running
}

// Start runs a check in the background unless one is already running.
func (c *Checker) Start(deep bool) bool {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return false
	}
	c.running = true
	c.mu.Unlock()
	go func() {
		rep := c.run(context.Background(), deep)
		c.mu.Lock()
		c.running, c.last = false, rep
		c.mu.Unlock()
	}()
	return true
}

func (c *Checker) run(ctx context.Context, deep bool) *Report {
	rep := &Report{StartedAt: time.Now().UTC(), Deep: deep}
	res, err := c.objects.Inventory(ctx, deep, 50<<20)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	rep.Objects, rep.Blobs, rep.Took = res.Objects, res.Blobs, res.Took
	seen := map[string]bool{}
	for _, is := range res.Issues {
		backend := c.names(is.Store)
		switch is.Kind {
		case object.IssueMissing, object.IssueCorrupt:
			kind, title := KindObjectMissing, "Object data is missing"
			if is.Kind == object.IssueCorrupt {
				kind, title = KindObjectCorrupt, "Object data is damaged"
				rep.Corrupt++
			} else {
				rep.Missing++
			}
			fp := kind + ":" + is.Bucket + "/" + is.Key + "@" + is.VersionID
			seen[fp] = true
			detail := "Stored on " + backend
			if is.Detail != "" {
				detail += " (" + is.Detail + ")"
			}
			c.alerts.Raise(ctx, fp, Alert{Kind: kind, Severity: "critical", Title: title, Detail: detail,
				Bucket: is.Bucket, Key: is.Key, Backend: is.Store})
		case object.IssueUnavailable:
			rep.Offline++
		}
	}
	// Objects reported earlier that are fine now (restored, or deleted).
	open, _ := c.alerts.List(ctx, "open", 500)
	for _, a := range open {
		if a.Kind != KindObjectMissing && a.Kind != KindObjectCorrupt {
			continue
		}
		fpPrefix := a.Kind + ":" + a.Bucket + "/" + a.Key + "@"
		stillBad := false
		for fp := range seen {
			if strings.HasPrefix(fp, fpPrefix) {
				stillBad = true
				break
			}
		}
		if !stillBad && !deep && a.Kind == KindObjectCorrupt && strings.Contains(a.Detail, "checksum") {
			continue // only a deep check can clear checksum findings
		}
		if !stillBad {
			c.alerts.db.ExecContext(ctx, `UPDATE alerts SET resolved_at = ? WHERE id = ?`, time.Now().Unix(), a.ID)
		}
	}
	slog.Info("integrity check finished", "objects", rep.Objects, "missing", rep.Missing, "corrupt", rep.Corrupt, "deep", deep, "took", rep.Took)
	return rep
}

// Run checks periodically: a quick existence check every 6 hours and a deep
// checksum check weekly.
func (c *Checker) Run(ctx context.Context) {
	quick := time.NewTicker(6 * time.Hour)
	deep := time.NewTicker(7 * 24 * time.Hour)
	defer quick.Stop()
	defer deep.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-quick.C:
			c.Start(false)
		case <-deep.C:
			c.Start(true)
		}
	}
}
