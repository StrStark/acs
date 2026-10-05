package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"
)

var (
	ErrNoSuchKey   = errors.New("access key not found")
	ErrKeyExpired  = errors.New("access key has expired")
	ErrInvalidKey  = errors.New("invalid access key or secret")
	ErrNoSuchUser  = errors.New("user not found")
	ErrLastAdmin   = errors.New("cannot remove the last administrator")
	ErrUserExists  = errors.New("username is already taken")
	ErrWrongPasswd = errors.New("current password is incorrect")
)

type AccessKey struct {
	ID         string     `json:"id"`
	UserID     int64      `json:"userId"`
	Username   string     `json:"username"`
	Name       string     `json:"name"`
	Permission string     `json:"permission"`
	Buckets    []string   `json:"buckets"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

func (k *AccessKey) AllowsBucket(bucket string) bool {
	if slices.Contains(k.Buckets, "*") {
		return true
	}
	return bucket != "" && slices.Contains(k.Buckets, bucket)
}

const alnum = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
const secretChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func randString(alphabet string, n int) string {
	b := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range b {
		v, _ := rand.Int(rand.Reader, max)
		b[i] = alphabet[v.Int64()]
	}
	return string(b)
}

type CreateKeyInput struct {
	Name       string
	Permission string
	Buckets    []string
	ExpiresAt  *time.Time
}

// keyCache holds decrypted secrets so SigV4 verification avoids a DB round trip.
type keyCache struct {
	mu sync.RWMutex
	m  map[string]cachedKey
}

type cachedKey struct {
	key      AccessKey
	user     User
	secret   string
	lastUsed time.Time
}

// CreateAccessKey creates a key for userID and returns it with its secret,
// which is shown to the user only once.
func (s *Service) CreateAccessKey(ctx context.Context, userID int64, in CreateKeyInput) (*AccessKey, string, error) {
	if in.Permission == "" {
		in.Permission = KeyReadWrite
	}
	if !ValidKeyPermission(in.Permission) {
		return nil, "", &ValidationError{"permission", "must be read, readwrite or full"}
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		return nil, "", &ValidationError{"name", "is required (max 100 characters)"}
	}
	if len(in.Buckets) == 0 {
		in.Buckets = []string{"*"}
	}
	if in.ExpiresAt != nil && in.ExpiresAt.Before(time.Now()) {
		return nil, "", &ValidationError{"expiresAt", "must be in the future"}
	}
	u, err := s.GetUser(ctx, userID)
	if err != nil {
		return nil, "", err
	}

	id := "ACS" + randString(alnum, 17)
	secret := randString(secretChars, 40)
	buckets, _ := json.Marshal(in.Buckets)
	now := time.Now()
	var expires any
	if in.ExpiresAt != nil {
		expires = in.ExpiresAt.Unix()
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO access_keys (id, user_id, name, secret_enc, permission, buckets, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, userID, in.Name, s.box.Seal([]byte(secret)), in.Permission, string(buckets), now.Unix(), expires)
	if err != nil {
		return nil, "", err
	}
	return &AccessKey{
		ID: id, UserID: userID, Username: u.Username, Name: in.Name, Permission: in.Permission,
		Buckets: in.Buckets, CreatedAt: now.Truncate(time.Second), ExpiresAt: in.ExpiresAt,
	}, secret, nil
}

const keyColumns = `k.id, k.user_id, u.username, k.name, k.permission, k.buckets, k.created_at, k.expires_at, k.last_used_at`

func scanKey(row interface{ Scan(...any) error }, extra ...any) (*AccessKey, error) {
	var k AccessKey
	var buckets string
	var created int64
	var expires, lastUsed sql.NullInt64
	dest := append([]any{&k.ID, &k.UserID, &k.Username, &k.Name, &k.Permission, &buckets, &created, &expires, &lastUsed}, extra...)
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(buckets), &k.Buckets)
	k.CreatedAt = time.Unix(created, 0)
	if expires.Valid {
		t := time.Unix(expires.Int64, 0)
		k.ExpiresAt = &t
	}
	if lastUsed.Valid {
		t := time.Unix(lastUsed.Int64, 0)
		k.LastUsedAt = &t
	}
	return &k, nil
}

// ListAccessKeys returns keys of userID, or of all users when userID is 0.
func (s *Service) ListAccessKeys(ctx context.Context, userID int64) ([]*AccessKey, error) {
	q := `SELECT ` + keyColumns + ` FROM access_keys k JOIN users u ON u.id = k.user_id`
	var args []any
	if userID != 0 {
		q += ` WHERE k.user_id = ?`
		args = append(args, userID)
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY k.created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*AccessKey{}
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Service) GetAccessKey(ctx context.Context, id string) (*AccessKey, error) {
	k, err := scanKey(s.db.QueryRowContext(ctx,
		`SELECT `+keyColumns+` FROM access_keys k JOIN users u ON u.id = k.user_id WHERE k.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSuchKey
	}
	return k, err
}

func (s *Service) DeleteAccessKey(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM access_keys WHERE id = ?`, id)
	if err != nil {
		return err
	}
	s.keys.mu.Lock()
	delete(s.keys.m, id)
	s.keys.mu.Unlock()
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoSuchKey
	}
	return nil
}

// LookupKey returns the principal and secret for an access key ID. Used by
// SigV4 verification, which needs the secret to recompute the signature.
func (s *Service) LookupKey(ctx context.Context, id string) (Principal, string, error) {
	s.keys.mu.RLock()
	c, ok := s.keys.m[id]
	s.keys.mu.RUnlock()

	if !ok || time.Since(c.lastUsed) > time.Minute {
		var enc []byte
		var role string
		var userCreated int64
		k, err := scanKey(s.db.QueryRowContext(ctx,
			`SELECT `+keyColumns+`, k.secret_enc, u.role, u.created_at FROM access_keys k JOIN users u ON u.id = k.user_id WHERE k.id = ?`, id),
			&enc, &role, &userCreated)
		if errors.Is(err, sql.ErrNoRows) {
			s.keys.mu.Lock()
			delete(s.keys.m, id)
			s.keys.mu.Unlock()
			return Principal{}, "", ErrNoSuchKey
		}
		if err != nil {
			return Principal{}, "", err
		}
		secret, err := s.box.Open(enc)
		if err != nil {
			return Principal{}, "", err
		}
		now := time.Now()
		c = cachedKey{
			key:      *k,
			user:     User{ID: k.UserID, Username: k.Username, Role: role, CreatedAt: time.Unix(userCreated, 0)},
			secret:   string(secret),
			lastUsed: now,
		}
		// Record usage at most once a minute per key (this branch).
		s.db.ExecContext(ctx, `UPDATE access_keys SET last_used_at = ? WHERE id = ?`, now.Unix(), id)
		s.keys.mu.Lock()
		s.keys.m[id] = c
		s.keys.mu.Unlock()
	}
	if c.key.ExpiresAt != nil && time.Now().After(*c.key.ExpiresAt) {
		return Principal{}, "", ErrKeyExpired
	}
	key := c.key
	return Principal{User: c.user, Key: &key}, c.secret, nil
}

// AuthenticateKey verifies an access key ID and secret (REST Bearer/Basic auth).
func (s *Service) AuthenticateKey(ctx context.Context, id, secret string) (Principal, error) {
	p, want, err := s.LookupKey(ctx, id)
	if errors.Is(err, ErrNoSuchKey) {
		return Principal{}, ErrInvalidKey
	}
	if err != nil {
		return Principal{}, err
	}
	if subtle.ConstantTimeCompare([]byte(secret), []byte(want)) != 1 {
		return Principal{}, ErrInvalidKey
	}
	return p, nil
}

// invalidateUserKeys drops cached keys of a user whose role or existence changed.
func (s *Service) invalidateUserKeys(userID int64) {
	s.keys.mu.Lock()
	for id, c := range s.keys.m {
		if c.user.ID == userID {
			delete(s.keys.m, id)
		}
	}
	s.keys.mu.Unlock()
}
