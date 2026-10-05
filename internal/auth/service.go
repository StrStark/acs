// Package auth manages panel users, first-run setup, and login sessions.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"regexp"
	"time"

	"acs/internal/secret"
)

var (
	ErrSetupComplete      = errors.New("setup has already been completed")
	ErrBadSetupToken      = errors.New("invalid setup token")
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrNoSession          = errors.New("no valid session")
)

// ValidationError describes a rejected input field.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

type User struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
}

type Service struct {
	db         *sql.DB
	box        *secret.Box
	keys       keyCache
	sessionTTL time.Duration
	setupToken string
	// dummyHash is verified against when a username does not exist, so
	// login timing does not reveal which usernames are valid.
	dummyHash string
}

func NewService(db *sql.DB, box *secret.Box, sessionTTL time.Duration, setupToken string) (*Service, error) {
	dummy, err := HashPassword("not-a-real-password")
	if err != nil {
		return nil, err
	}
	return &Service{
		db: db, box: box, keys: keyCache{m: make(map[string]cachedKey)},
		sessionTTL: sessionTTL, setupToken: setupToken, dummyHash: dummy,
	}, nil
}

// SetupTokenRequired reports whether Setup needs a token (ACS_SETUP_TOKEN is set).
func (s *Service) SetupTokenRequired() bool { return s.setupToken != "" }

// NeedsSetup reports whether no admin account exists yet.
func (s *Service) NeedsSetup(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n == 0, err
}

var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9._-]{3,32}$`)

const (
	minPasswordLen = 10
	maxPasswordLen = 1024
)

func validateCredentials(username, password string) error {
	if !usernameRe.MatchString(username) {
		return &ValidationError{"username", "must be 3–32 characters: letters, digits, dot, dash or underscore"}
	}
	if len(password) < minPasswordLen {
		return &ValidationError{"password", "must be at least 10 characters"}
	}
	if len(password) > maxPasswordLen {
		return &ValidationError{"password", "is too long"}
	}
	return nil
}

// Setup creates the first admin account. It fails with ErrSetupComplete if any
// user already exists; the check and insert are a single statement, so two
// concurrent setup requests cannot both succeed.
func (s *Service) Setup(ctx context.Context, username, password, token string) (User, error) {
	if s.setupToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.setupToken)) != 1 {
		return User{}, ErrBadSetupToken
	}
	// Fast path for a clear error; the INSERT below is the authoritative check.
	if needs, err := s.NeedsSetup(ctx); err != nil {
		return User{}, err
	} else if !needs {
		return User{}, ErrSetupComplete
	}
	if err := validateCredentials(username, password); err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}

	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO users (username, password_hash, role, created_at, updated_at)
		SELECT ?, ?, 'admin', ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM users)`,
		username, hash, now, now)
	if err != nil {
		return User{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return User{}, ErrSetupComplete
	}
	id, err := res.LastInsertId()
	if err != nil {
		return User{}, err
	}
	return User{ID: id, Username: username, Role: "admin", CreatedAt: time.Unix(now, 0)}, nil
}

// Login verifies credentials and creates a session, returning its token.
func (s *Service) Login(ctx context.Context, username, password, ip, userAgent string) (string, User, error) {
	var u User
	var hash string
	var created int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id, username, role, created_at, password_hash FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.Role, &created, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		VerifyPassword(password, s.dummyHash)
		return "", User{}, ErrInvalidCredentials
	}
	if err != nil {
		return "", User{}, err
	}
	ok, err := VerifyPassword(password, hash)
	if err != nil {
		return "", User{}, err
	}
	if !ok {
		return "", User{}, ErrInvalidCredentials
	}
	u.CreatedAt = time.Unix(created, 0)

	token, err := s.StartSession(ctx, u.ID, ip, userAgent)
	if err != nil {
		return "", User{}, err
	}
	return token, u, nil
}

// StartSession creates a login session for userID and returns its token.
func (s *Service) StartSession(ctx context.Context, userID int64, ip, userAgent string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	now := time.Now()
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions (token_hash, user_id, created_at, expires_at, last_seen_at, ip, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		hashToken(token), userID, now.Unix(), now.Add(s.sessionTTL).Unix(), now.Unix(), ip, userAgent)
	return token, err
}

// Authenticate resolves a session token to its user and slides the expiry forward.
func (s *Service) Authenticate(ctx context.Context, token string) (User, error) {
	if token == "" {
		return User{}, ErrNoSession
	}
	h := hashToken(token)
	now := time.Now()

	var u User
	var created, lastSeen int64
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.role, u.created_at, s.last_seen_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, h, now.Unix()).
		Scan(&u.ID, &u.Username, &u.Role, &created, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNoSession
	}
	if err != nil {
		return User{}, err
	}
	u.CreatedAt = time.Unix(created, 0)

	// Refresh at most once a minute to avoid a write on every request.
	if now.Unix()-lastSeen > 60 {
		if _, err := s.db.ExecContext(ctx,
			`UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE token_hash = ?`,
			now.Unix(), now.Add(s.sessionTTL).Unix(), h); err != nil {
			return User{}, err
		}
	}
	return u, nil
}

// Logout deletes the session for token.
func (s *Service) Logout(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

// DeleteExpiredSessions removes sessions past their expiry.
func (s *Service) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
