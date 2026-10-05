// Package settings stores server settings editable from the panel.
package settings

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"strings"
	"sync"
)

// Settings is the full set of editable settings.
type Settings struct {
	// SiteName is shown in the panel and on public share pages.
	SiteName string `json:"siteName"`
	// PublicURL is the externally reachable base URL of the panel, used to
	// build share links. Empty means "derive from the request".
	PublicURL string `json:"publicUrl"`
	// S3PublicURL is the externally reachable S3 endpoint shown to users.
	S3PublicURL string `json:"s3PublicUrl"`
	// Region is reported to S3 clients and used in SigV4 scope validation.
	Region string `json:"region"`
}

var defaults = Settings{SiteName: "ACS Storage", Region: "us-east-1"}

type Store struct {
	db  *sql.DB
	mu  sync.RWMutex
	cur Settings
}

func Open(ctx context.Context, db *sql.DB) (*Store, error) {
	s := &Store{db: db}
	return s, s.load(ctx)
}

func (s *Store) load(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return err
	}
	defer rows.Close()
	cur := defaults
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return err
		}
		switch k {
		case "site_name":
			cur.SiteName = v
		case "public_url":
			cur.PublicURL = v
		case "s3_public_url":
			cur.S3PublicURL = v
		case "region":
			cur.Region = v
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.cur = cur
	s.mu.Unlock()
	return nil
}

func (s *Store) Get() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cur
}

var ErrInvalid = errors.New("invalid settings")

func normalizeURL(field, raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.Join(ErrInvalid, errors.New(field+" must be an http(s) URL"))
	}
	return raw, nil
}

func (s *Store) Update(ctx context.Context, in Settings) (Settings, error) {
	in.SiteName = strings.TrimSpace(in.SiteName)
	if in.SiteName == "" {
		in.SiteName = defaults.SiteName
	}
	if len(in.SiteName) > 80 {
		return Settings{}, errors.Join(ErrInvalid, errors.New("siteName is too long"))
	}
	var err error
	if in.PublicURL, err = normalizeURL("publicUrl", in.PublicURL); err != nil {
		return Settings{}, err
	}
	if in.S3PublicURL, err = normalizeURL("s3PublicUrl", in.S3PublicURL); err != nil {
		return Settings{}, err
	}
	in.Region = strings.TrimSpace(in.Region)
	if in.Region == "" {
		in.Region = defaults.Region
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Settings{}, err
	}
	defer tx.Rollback()
	for k, v := range map[string]string{
		"site_name": in.SiteName, "public_url": in.PublicURL, "s3_public_url": in.S3PublicURL, "region": in.Region,
	} {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return Settings{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Settings{}, err
	}
	s.mu.Lock()
	s.cur = in
	s.mu.Unlock()
	return in, nil
}
