// Package share manages public share links: single files, folders, and
// upload-only "file request" links.
package share

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math/big"
	"strings"
	"time"

	"acs/internal/auth"
)

const (
	TypeFile   = "file"
	TypeFolder = "folder"
	TypeUpload = "upload"
)

var (
	ErrNotFound      = errors.New("share link not found")
	ErrExpired       = errors.New("share link has expired")
	ErrDisabled      = errors.New("share link is disabled")
	ErrLimitReached  = errors.New("download limit reached")
	ErrWrongPassword = errors.New("incorrect password")
)

type ValidationError struct{ Field, Message string }

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

type Share struct {
	ID             int64      `json:"id"`
	Token          string     `json:"token"`
	Type           string     `json:"type"`
	Bucket         string     `json:"bucket"`
	Key            string     `json:"key"`
	CreatedBy      *int64     `json:"createdBy,omitempty"`
	CreatedByName  string     `json:"createdByName,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	ExpiresAt      *time.Time `json:"expiresAt,omitempty"`
	HasPassword    bool       `json:"hasPassword"`
	MaxDownloads   *int64     `json:"maxDownloads,omitempty"`
	Downloads      int64      `json:"downloads"`
	Views          int64      `json:"views"`
	MaxUploadBytes *int64     `json:"maxUploadBytes,omitempty"`
	Note           string     `json:"note"`
	Disabled       bool       `json:"disabled"`
	LastAccessedAt *time.Time `json:"lastAccessedAt,omitempty"`

	passwordHash string
}

// Status explains why a share cannot be used, or returns nil if it can.
func (s *Share) Status(now time.Time) error {
	switch {
	case s.Disabled:
		return ErrDisabled
	case s.ExpiresAt != nil && now.After(*s.ExpiresAt):
		return ErrExpired
	case s.MaxDownloads != nil && s.Downloads >= *s.MaxDownloads:
		return ErrLimitReached
	}
	return nil
}

type Service struct {
	db      *sql.DB
	signKey []byte
}

// New returns a share service; signKey signs password-unlock tokens.
func New(db *sql.DB, signKey []byte) *Service {
	return &Service{db: db, signKey: signKey}
}

const tokenChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func newToken() string {
	b := make([]byte, 22)
	max := big.NewInt(int64(len(tokenChars)))
	for i := range b {
		v, _ := rand.Int(rand.Reader, max)
		b[i] = tokenChars[v.Int64()]
	}
	return string(b)
}

type CreateInput struct {
	Type           string
	Bucket         string
	Key            string
	ExpiresAt      *time.Time
	Password       string
	MaxDownloads   *int64
	MaxUploadBytes *int64
	Note           string
	CreatedBy      int64
}

func (s *Service) Create(ctx context.Context, in CreateInput) (*Share, error) {
	switch in.Type {
	case TypeFile:
		if in.Key == "" || strings.HasSuffix(in.Key, "/") {
			return nil, &ValidationError{"key", "a file share needs an object key"}
		}
	case TypeFolder, TypeUpload:
		if in.Key != "" && !strings.HasSuffix(in.Key, "/") {
			in.Key += "/"
		}
	default:
		return nil, &ValidationError{"type", "must be file, folder or upload"}
	}
	if in.ExpiresAt != nil && in.ExpiresAt.Before(time.Now()) {
		return nil, &ValidationError{"expiresAt", "must be in the future"}
	}
	if in.MaxDownloads != nil && *in.MaxDownloads < 1 {
		return nil, &ValidationError{"maxDownloads", "must be at least 1"}
	}
	if in.MaxUploadBytes != nil && *in.MaxUploadBytes < 1 {
		return nil, &ValidationError{"maxUploadBytes", "must be positive"}
	}
	if len(in.Note) > 500 {
		return nil, &ValidationError{"note", "is too long"}
	}
	var pwHash any
	if in.Password != "" {
		h, err := auth.HashPassword(in.Password)
		if err != nil {
			return nil, err
		}
		pwHash = h
	}
	now := time.Now()
	token := newToken()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO shares (token, type, bucket, key, created_by, created_at, expires_at, password_hash,
		                    max_downloads, max_upload_bytes, note)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		token, in.Type, in.Bucket, in.Key, in.CreatedBy, now.Unix(), unixPtr(in.ExpiresAt), pwHash,
		in.MaxDownloads, in.MaxUploadBytes, in.Note)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Get(ctx, id)
}

func unixPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.Unix()
}

const columns = `s.id, s.token, s.type, s.bucket, s.key, s.created_by, COALESCE(u.username, ''), s.created_at,
	s.expires_at, COALESCE(s.password_hash, ''), s.max_downloads, s.downloads, s.views, s.max_upload_bytes,
	s.note, s.disabled, s.last_accessed_at`

const from = ` FROM shares s LEFT JOIN users u ON u.id = s.created_by`

func scan(row interface{ Scan(...any) error }) (*Share, error) {
	var sh Share
	var createdBy, expires, maxDl, maxUp, lastAcc sql.NullInt64
	var created int64
	if err := row.Scan(&sh.ID, &sh.Token, &sh.Type, &sh.Bucket, &sh.Key, &createdBy, &sh.CreatedByName, &created,
		&expires, &sh.passwordHash, &maxDl, &sh.Downloads, &sh.Views, &maxUp, &sh.Note, &sh.Disabled, &lastAcc); err != nil {
		return nil, err
	}
	sh.CreatedAt = time.Unix(created, 0)
	sh.HasPassword = sh.passwordHash != ""
	if createdBy.Valid {
		sh.CreatedBy = &createdBy.Int64
	}
	if expires.Valid {
		t := time.Unix(expires.Int64, 0)
		sh.ExpiresAt = &t
	}
	if maxDl.Valid {
		sh.MaxDownloads = &maxDl.Int64
	}
	if maxUp.Valid {
		sh.MaxUploadBytes = &maxUp.Int64
	}
	if lastAcc.Valid {
		t := time.Unix(lastAcc.Int64, 0)
		sh.LastAccessedAt = &t
	}
	return &sh, nil
}

func (s *Service) Get(ctx context.Context, id int64) (*Share, error) {
	sh, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+from+` WHERE s.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sh, err
}

func (s *Service) GetByToken(ctx context.Context, token string) (*Share, error) {
	sh, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+from+` WHERE s.token = ?`, token))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sh, err
}

type ListFilter struct {
	Bucket    string
	Key       string
	CreatedBy int64
}

func (s *Service) List(ctx context.Context, f ListFilter) ([]*Share, error) {
	q := `SELECT ` + columns + from + ` WHERE 1=1`
	var args []any
	if f.Bucket != "" {
		q += ` AND s.bucket = ?`
		args = append(args, f.Bucket)
	}
	if f.Key != "" {
		q += ` AND s.key = ?`
		args = append(args, f.Key)
	}
	if f.CreatedBy != 0 {
		q += ` AND s.created_by = ?`
		args = append(args, f.CreatedBy)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY s.created_at DESC, s.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Share{}
	for rows.Next() {
		sh, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sh)
	}
	return out, rows.Err()
}

// UpdateInput fields are applied when non-nil. Password "" removes the password.
type UpdateInput struct {
	ExpiresAt      **time.Time
	Password       *string
	MaxDownloads   **int64
	MaxUploadBytes **int64
	Note           *string
	Disabled       *bool
}

func (s *Service) Update(ctx context.Context, id int64, in UpdateInput) (*Share, error) {
	if _, err := s.Get(ctx, id); err != nil {
		return nil, err
	}
	sets := []string{}
	var args []any
	if in.ExpiresAt != nil {
		sets = append(sets, "expires_at = ?")
		args = append(args, unixPtr(*in.ExpiresAt))
	}
	if in.Password != nil {
		if *in.Password == "" {
			sets = append(sets, "password_hash = NULL")
		} else {
			h, err := auth.HashPassword(*in.Password)
			if err != nil {
				return nil, err
			}
			sets = append(sets, "password_hash = ?")
			args = append(args, h)
		}
	}
	if in.MaxDownloads != nil {
		if *in.MaxDownloads != nil && **in.MaxDownloads < 1 {
			return nil, &ValidationError{"maxDownloads", "must be at least 1"}
		}
		sets = append(sets, "max_downloads = ?")
		args = append(args, *in.MaxDownloads)
	}
	if in.MaxUploadBytes != nil {
		sets = append(sets, "max_upload_bytes = ?")
		args = append(args, *in.MaxUploadBytes)
	}
	if in.Note != nil {
		sets = append(sets, "note = ?")
		args = append(args, *in.Note)
	}
	if in.Disabled != nil {
		sets = append(sets, "disabled = ?")
		args = append(args, *in.Disabled)
	}
	if len(sets) > 0 {
		args = append(args, id)
		if _, err := s.db.ExecContext(ctx, `UPDATE shares SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...); err != nil {
			return nil, err
		}
	}
	return s.Get(ctx, id)
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM shares WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteForBucket removes all shares of a deleted bucket.
func (s *Service) DeleteForBucket(ctx context.Context, bucket string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM shares WHERE bucket = ?`, bucket)
	return err
}

func (s *Service) RecordView(ctx context.Context, id int64) {
	s.db.ExecContext(ctx, `UPDATE shares SET views = views + 1, last_accessed_at = ? WHERE id = ?`, time.Now().Unix(), id)
}

// RecordDownload counts a download, failing with ErrLimitReached if the
// share's download limit is already used up.
func (s *Service) RecordDownload(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE shares SET downloads = downloads + 1, last_accessed_at = ?
		WHERE id = ? AND (max_downloads IS NULL OR downloads < max_downloads)`, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrLimitReached
	}
	return nil
}

// CheckPassword verifies a share's password.
func (s *Service) CheckPassword(sh *Share, password string) error {
	if !sh.HasPassword {
		return nil
	}
	ok, err := auth.VerifyPassword(password, sh.passwordHash)
	if err != nil {
		return err
	}
	if !ok {
		return ErrWrongPassword
	}
	return nil
}

// UnlockToken returns a signed token proving the password for sh was supplied.
// It is bound to the share's password hash, so changing the password revokes it.
func (s *Service) UnlockToken(sh *Share, ttl time.Duration) string {
	exp := make([]byte, 8)
	binary.BigEndian.PutUint64(exp, uint64(time.Now().Add(ttl).Unix()))
	return base64.RawURLEncoding.EncodeToString(append(exp, s.mac(sh, exp)...))
}

// VerifyUnlock checks a token from UnlockToken.
func (s *Service) VerifyUnlock(sh *Share, token string) bool {
	if !sh.HasPassword {
		return true
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 8+sha256.Size {
		return false
	}
	exp := raw[:8]
	if time.Now().Unix() > int64(binary.BigEndian.Uint64(exp)) {
		return false
	}
	return hmac.Equal(raw[8:], s.mac(sh, exp))
}

func (s *Service) mac(sh *Share, exp []byte) []byte {
	m := hmac.New(sha256.New, s.signKey)
	m.Write([]byte(sh.Token))
	m.Write([]byte{0})
	m.Write([]byte(sh.passwordHash))
	m.Write(exp)
	return m.Sum(nil)
}
