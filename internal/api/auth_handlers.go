package api

import (
	"errors"
	"net/http"
	"time"

	"acs/internal/auth"
)

const sessionCookie = "acs_session"

type setupStatus struct {
	NeedsSetup    bool `json:"needsSetup"`
	TokenRequired bool `json:"tokenRequired"`
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	needs, err := s.auth.NeedsSetup(r.Context())
	if err != nil {
		writeInternalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, setupStatus{NeedsSetup: needs, TokenRequired: needs && s.auth.SetupTokenRequired()})
}

type setupRequest struct {
	Username   string `json:"username"`
	Password   string `json:"password"`
	SetupToken string `json:"setupToken"`
}

// handleSetup creates the first admin account and signs it in.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.Allow(clientIP(r)) {
		writeProblem(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	var req setupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	u, err := s.auth.Setup(r.Context(), req.Username, req.Password, req.SetupToken)
	var verr *auth.ValidationError
	switch {
	case errors.As(err, &verr):
		writeProblemFields(w, http.StatusUnprocessableEntity, verr.Error(), map[string]string{verr.Field: verr.Message})
		return
	case errors.Is(err, auth.ErrSetupComplete):
		writeProblem(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, auth.ErrBadSetupToken):
		writeProblemFields(w, http.StatusForbidden, err.Error(), map[string]string{"setupToken": "is invalid"})
		return
	case err != nil:
		writeInternalError(w, r, err)
		return
	}

	token, err := s.auth.StartSession(r.Context(), u.ID, clientIP(r), r.UserAgent())
	if err != nil {
		writeInternalError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), u.Username, "auth.setup", u.Username, clientIP(r), nil)
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusCreated, u)
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.Allow(clientIP(r)) {
		writeProblem(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	token, u, err := s.auth.Login(r.Context(), req.Username, req.Password, clientIP(r), r.UserAgent())
	if errors.Is(err, auth.ErrInvalidCredentials) {
		s.audit.Record(r.Context(), req.Username, "auth.login_failed", req.Username, clientIP(r), nil)
		writeProblem(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err != nil {
		writeInternalError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), u.Username, "auth.login", u.Username, clientIP(r), nil)
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		if err := s.auth.Logout(r.Context(), c.Value); err != nil {
			writeInternalError(w, r, err)
			return
		}
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

type meResponse struct {
	auth.User
	// Key is set when authenticated with an access key.
	Key *auth.AccessKey `json:"key,omitempty"`
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	writeJSON(w, http.StatusOK, meResponse{User: p.User, Key: p.Key})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.Key != nil {
		writeProblem(w, http.StatusForbidden, "passwords cannot be changed with an access key")
		return
	}
	if !s.limiter.Allow(clientIP(r)) {
		writeProblem(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	var req changePasswordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	token := ""
	if c, err := r.Cookie(sessionCookie); err == nil {
		token = c.Value
	}
	if err := s.auth.ChangePassword(r.Context(), p.User.ID, req.CurrentPassword, req.NewPassword, token); err != nil {
		writeError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), p.Name(), "auth.password_change", p.User.Username, clientIP(r), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		MaxAge:   int(s.cfg.SessionTTL / time.Second),
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}
