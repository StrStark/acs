// Package webhook delivers server events to configured HTTP endpoints.
//
// Each delivery is a JSON POST signed with HMAC-SHA256 over
// "<timestamp>.<body>" using the webhook's secret:
//
//	X-ACS-Event:     object.created
//	X-ACS-Delivery:  <unique id>
//	X-ACS-Timestamp: <unix seconds>
//	X-ACS-Signature: sha256=<hex>
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"acs/internal/secret"
)

// Events that can be subscribed to.
var EventTypes = []string{
	"object.created", "object.deleted", "bucket.created", "bucket.deleted",
	"share.created", "share.downloaded", "share.uploaded",
}

var ErrNotFound = errors.New("webhook not found")

type ValidationError struct{ Field, Message string }

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

type Webhook struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Secret    string    `json:"secret"`
	Events    []string  `json:"events"`
	Bucket    string    `json:"bucket"`
	Prefix    string    `json:"prefix"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"createdAt"`
}

func (w *Webhook) matches(e Event) bool {
	if !w.Enabled {
		return false
	}
	if !slices.Contains(w.Events, "*") && !slices.Contains(w.Events, e.Type) {
		return false
	}
	if w.Bucket != "" && e.Bucket != w.Bucket {
		return false
	}
	return w.Prefix == "" || strings.HasPrefix(e.Key, w.Prefix)
}

// Event is published by other packages; Data becomes the payload's "data".
type Event struct {
	Type   string
	Bucket string
	Key    string
	Data   any
}

type Delivery struct {
	ID         int64     `json:"id"`
	WebhookID  int64     `json:"webhookId"`
	Event      string    `json:"event"`
	Payload    string    `json:"payload"`
	Status     int       `json:"status"`
	Error      string    `json:"error"`
	Attempt    int       `json:"attempt"`
	DurationMS int64     `json:"durationMs"`
	CreatedAt  time.Time `json:"createdAt"`
}

type Manager struct {
	db     *sql.DB
	box    *secret.Box
	client *http.Client

	mu    sync.RWMutex
	hooks []*Webhook

	queue chan job
}

type job struct {
	hook    *Webhook
	event   string
	body    []byte
	attempt int
}

const maxAttempts = 4

func New(ctx context.Context, db *sql.DB, box *secret.Box) (*Manager, error) {
	m := &Manager{
		db:     db,
		box:    box,
		client: &http.Client{Timeout: 10 * time.Second},
		queue:  make(chan job, 1000),
	}
	return m, m.reload(ctx)
}

// Run starts delivery workers until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) {
	for i := 0; i < 4; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-m.queue:
					m.deliver(ctx, j)
				}
			}
		}()
	}
}

func (m *Manager) reload(ctx context.Context) error {
	hooks, err := m.List(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.hooks = hooks
	m.mu.Unlock()
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Publish enqueues an event for all matching webhooks. It never blocks; if the
// queue is full the event is dropped and logged.
func (m *Manager) Publish(e Event) {
	m.mu.RLock()
	var targets []*Webhook
	for _, h := range m.hooks {
		if h.matches(e) {
			targets = append(targets, h)
		}
	}
	m.mu.RUnlock()
	if len(targets) == 0 {
		return
	}
	body, err := json.Marshal(map[string]any{
		"id":   randomHex(12),
		"type": e.Type,
		"time": time.Now().UTC(),
		"data": e.Data,
	})
	if err != nil {
		slog.Error("webhook payload", "err", err)
		return
	}
	for _, h := range targets {
		select {
		case m.queue <- job{hook: h, event: e.Type, body: body, attempt: 1}:
		default:
			slog.Warn("webhook queue full, dropping event", "webhook", h.ID, "event", e.Type)
		}
	}
}

// Sign computes the X-ACS-Signature value for a payload.
func Sign(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (m *Manager) send(ctx context.Context, h *Webhook, event string, body []byte) (int, time.Duration, error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return 0, 0, err
	}
	ts := time.Now().Unix()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ACS-Webhook/1")
	req.Header.Set("X-ACS-Event", event)
	req.Header.Set("X-ACS-Delivery", randomHex(12))
	req.Header.Set("X-ACS-Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("X-ACS-Signature", Sign(h.Secret, ts, body))
	resp, err := m.client.Do(req)
	if err != nil {
		return 0, time.Since(start), err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return resp.StatusCode, time.Since(start), fmt.Errorf("endpoint returned %s", resp.Status)
	}
	return resp.StatusCode, time.Since(start), nil
}

func (m *Manager) deliver(ctx context.Context, j job) {
	status, dur, err := m.send(ctx, j.hook, j.event, j.body)
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	m.recordDelivery(ctx, j.hook.ID, j.event, j.body, status, errMsg, j.attempt, dur)
	if err == nil || j.attempt >= maxAttempts || ctx.Err() != nil {
		return
	}
	// Exponential backoff: 5s, 25s, 125s.
	delay := 5 * time.Second
	for i := 1; i < j.attempt; i++ {
		delay *= 5
	}
	j.attempt++
	time.AfterFunc(delay, func() {
		select {
		case m.queue <- j:
		default:
		}
	})
}

func (m *Manager) recordDelivery(ctx context.Context, hookID int64, event string, body []byte, status int, errMsg string, attempt int, dur time.Duration) {
	ctx = context.WithoutCancel(ctx)
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO webhook_deliveries (webhook_id, event, payload, status, error, attempt, duration_ms, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		hookID, event, string(body), status, errMsg, attempt, dur.Milliseconds(), time.Now().Unix()); err != nil {
		slog.Warn("record webhook delivery", "err", err)
		return
	}
	// Keep the most recent 100 deliveries per webhook.
	m.db.ExecContext(ctx, `
		DELETE FROM webhook_deliveries WHERE webhook_id = ? AND id NOT IN (
			SELECT id FROM webhook_deliveries WHERE webhook_id = ? ORDER BY id DESC LIMIT 100)`, hookID, hookID)
}

// Test sends a synchronous ping and records it.
func (m *Manager) Test(ctx context.Context, id int64) (*Delivery, error) {
	h, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{
		"id": randomHex(12), "type": "ping", "time": time.Now().UTC(),
		"data": map[string]any{"webhookId": h.ID, "message": "Hello from ACS"},
	})
	status, dur, err := m.send(ctx, h, "ping", body)
	d := &Delivery{WebhookID: id, Event: "ping", Payload: string(body), Status: status, Attempt: 1,
		DurationMS: dur.Milliseconds(), CreatedAt: time.Now()}
	if err != nil {
		d.Error = err.Error()
	}
	m.recordDelivery(ctx, id, "ping", body, status, d.Error, 1, dur)
	return d, nil
}

// ---- CRUD ----

type Input struct {
	Name    string
	URL     string
	Events  []string
	Bucket  string
	Prefix  string
	Enabled bool
}

func validate(in *Input) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		return &ValidationError{"name", "is required (max 100 characters)"}
	}
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return &ValidationError{"url", "must be an http(s) URL"}
	}
	if len(in.Events) == 0 {
		return &ValidationError{"events", "select at least one event"}
	}
	for _, e := range in.Events {
		if e != "*" && !slices.Contains(EventTypes, e) {
			return &ValidationError{"events", "unknown event " + e}
		}
	}
	return nil
}

func (m *Manager) Create(ctx context.Context, in Input) (*Webhook, error) {
	if err := validate(&in); err != nil {
		return nil, err
	}
	secret := "whsec_" + randomHex(24)
	events, _ := json.Marshal(in.Events)
	res, err := m.db.ExecContext(ctx, `
		INSERT INTO webhooks (name, url, secret_enc, events, bucket, prefix, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		in.Name, in.URL, m.box.Seal([]byte(secret)), string(events), in.Bucket, in.Prefix, in.Enabled, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if err := m.reload(ctx); err != nil {
		return nil, err
	}
	return m.Get(ctx, id)
}

func (m *Manager) Update(ctx context.Context, id int64, in Input) (*Webhook, error) {
	if err := validate(&in); err != nil {
		return nil, err
	}
	events, _ := json.Marshal(in.Events)
	res, err := m.db.ExecContext(ctx, `
		UPDATE webhooks SET name = ?, url = ?, events = ?, bucket = ?, prefix = ?, enabled = ? WHERE id = ?`,
		in.Name, in.URL, string(events), in.Bucket, in.Prefix, in.Enabled, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	if err := m.reload(ctx); err != nil {
		return nil, err
	}
	return m.Get(ctx, id)
}

func (m *Manager) Delete(ctx context.Context, id int64) error {
	res, err := m.db.ExecContext(ctx, `DELETE FROM webhooks WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return m.reload(ctx)
}

func (m *Manager) scanHook(row interface{ Scan(...any) error }) (*Webhook, error) {
	var h Webhook
	var enc []byte
	var events string
	var created int64
	if err := row.Scan(&h.ID, &h.Name, &h.URL, &enc, &events, &h.Bucket, &h.Prefix, &h.Enabled, &created); err != nil {
		return nil, err
	}
	sec, err := m.box.Open(enc)
	if err != nil {
		return nil, err
	}
	h.Secret = string(sec)
	json.Unmarshal([]byte(events), &h.Events)
	h.CreatedAt = time.Unix(created, 0)
	return &h, nil
}

const hookColumns = `id, name, url, secret_enc, events, bucket, prefix, enabled, created_at`

func (m *Manager) Get(ctx context.Context, id int64) (*Webhook, error) {
	h, err := m.scanHook(m.db.QueryRowContext(ctx, `SELECT `+hookColumns+` FROM webhooks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return h, err
}

func (m *Manager) List(ctx context.Context) ([]*Webhook, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT `+hookColumns+` FROM webhooks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Webhook{}
	for rows.Next() {
		h, err := m.scanHook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (m *Manager) Deliveries(ctx context.Context, id int64, limit int) ([]Delivery, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, webhook_id, event, payload, status, error, attempt, duration_ms, created_at
		FROM webhook_deliveries WHERE webhook_id = ? ORDER BY id DESC LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		var created int64
		if err := rows.Scan(&d.ID, &d.WebhookID, &d.Event, &d.Payload, &d.Status, &d.Error, &d.Attempt, &d.DurationMS, &created); err != nil {
			return nil, err
		}
		d.CreatedAt = time.Unix(created, 0)
		out = append(out, d)
	}
	return out, rows.Err()
}
