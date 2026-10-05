package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

func (s *Service) GetUser(ctx context.Context, id int64) (User, error) {
	var u User
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT id, username, role, created_at FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &u.Role, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNoSuchUser
	}
	u.CreatedAt = time.Unix(created, 0)
	return u, err
}

func (s *Service) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, role, created_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		var created int64
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &created); err != nil {
			return nil, err
		}
		u.CreatedAt = time.Unix(created, 0)
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Service) CreateUser(ctx context.Context, username, password, role string) (User, error) {
	if !ValidRole(role) {
		return User{}, &ValidationError{"role", "must be viewer, editor or admin"}
	}
	if err := validateCredentials(username, password); err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	now := time.Now().Unix()
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		username, hash, role, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, ErrUserExists
		}
		return User{}, err
	}
	id, _ := res.LastInsertId()
	return User{ID: id, Username: username, Role: role, CreatedAt: time.Unix(now, 0)}, nil
}

// countOtherAdmins returns the number of admins other than userID.
func (s *Service) countOtherAdmins(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND id != ?`, userID).Scan(&n)
	return n, err
}

// UpdateUser changes a user's role and/or password (empty means unchanged).
// Setting a new password signs the user out everywhere.
func (s *Service) UpdateUser(ctx context.Context, id int64, role, password string) (User, error) {
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return User{}, err
	}
	if role != "" && role != u.Role {
		if !ValidRole(role) {
			return User{}, &ValidationError{"role", "must be viewer, editor or admin"}
		}
		if u.Role == RoleAdmin {
			if n, err := s.countOtherAdmins(ctx, id); err != nil {
				return User{}, err
			} else if n == 0 {
				return User{}, ErrLastAdmin
			}
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE users SET role = ?, updated_at = ? WHERE id = ?`, role, time.Now().Unix(), id); err != nil {
			return User{}, err
		}
		u.Role = role
		s.invalidateUserKeys(id)
	}
	if password != "" {
		if err := s.setPassword(ctx, id, password); err != nil {
			return User{}, err
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
			return User{}, err
		}
	}
	return u, nil
}

func (s *Service) setPassword(ctx context.Context, id int64, password string) error {
	if len(password) < minPasswordLen {
		return &ValidationError{"password", "must be at least 10 characters"}
	}
	if len(password) > maxPasswordLen {
		return &ValidationError{"password", "is too long"}
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, hash, time.Now().Unix(), id)
	return err
}

// ChangePassword lets a user change their own password. Other sessions are
// signed out; keepToken (the caller's session) stays valid.
func (s *Service) ChangePassword(ctx context.Context, id int64, current, next, keepToken string) error {
	var hash string
	if err := s.db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id = ?`, id).Scan(&hash); err != nil {
		return err
	}
	ok, err := VerifyPassword(current, hash)
	if err != nil {
		return err
	}
	if !ok {
		return ErrWrongPasswd
	}
	if err := s.setPassword(ctx, id, next); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash != ?`, id, hashToken(keepToken))
	return err
}

func (s *Service) DeleteUser(ctx context.Context, id int64) error {
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if u.Role == RoleAdmin {
		if n, err := s.countOtherAdmins(ctx, id); err != nil {
			return err
		} else if n == 0 {
			return ErrLastAdmin
		}
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id); err != nil {
		return err
	}
	s.invalidateUserKeys(id)
	return nil
}
