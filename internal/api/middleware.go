package api

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"acs/internal/auth"
)

type ctxKey int

const principalKey ctxKey = iota

func principal(r *http.Request) auth.Principal {
	p, _ := r.Context().Value(principalKey).(auth.Principal)
	return p
}

// keyCredentials extracts an access key from "Authorization: Bearer ID:SECRET"
// or HTTP Basic auth (user = key ID, password = secret).
func keyCredentials(r *http.Request) (id, secret string, ok bool) {
	h := r.Header.Get("Authorization")
	if rest, found := strings.CutPrefix(h, "Bearer "); found {
		id, secret, ok = strings.Cut(strings.TrimSpace(rest), ":")
		return id, secret, ok && id != "" && secret != ""
	}
	if rest, found := strings.CutPrefix(h, "Basic "); found {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rest))
		if err != nil {
			return "", "", false
		}
		id, secret, ok = strings.Cut(string(raw), ":")
		return id, secret, ok && id != "" && secret != ""
	}
	return "", "", false
}

// requireAuth accepts a session cookie or an access key and stores the
// resulting Principal in the request context.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if id, secret, ok := keyCredentials(r); ok {
			p, err := s.auth.AuthenticateKey(r.Context(), id, secret)
			if errors.Is(err, auth.ErrInvalidKey) || errors.Is(err, auth.ErrKeyExpired) {
				writeProblem(w, http.StatusUnauthorized, err.Error())
				return
			}
			if err != nil {
				writeInternalError(w, r, err)
				return
			}
			next(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
			return
		}

		c, err := r.Cookie(sessionCookie)
		if err != nil {
			writeProblem(w, http.StatusUnauthorized, "authentication required")
			return
		}
		u, err := s.auth.Authenticate(r.Context(), c.Value)
		if errors.Is(err, auth.ErrNoSession) {
			s.clearSessionCookie(w)
			writeProblem(w, http.StatusUnauthorized, "session expired")
			return
		}
		if err != nil {
			writeInternalError(w, r, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey, auth.Principal{User: u})))
	}
}

// can writes a 403 and returns false unless the caller may perform a on bucket.
func can(w http.ResponseWriter, r *http.Request, a auth.Action, bucket string) bool {
	if principal(r).Can(a, bucket) {
		return true
	}
	writeProblem(w, http.StatusForbidden, "you do not have permission to do this")
	return false
}

// requireJSON enforces a JSON content type on POST and PATCH API requests.
// Browsers cannot send application/json cross-origin without a CORS preflight
// (which this server never approves), so together with SameSite cookies this
// blocks CSRF. PUT and DELETE always require a preflight, so raw-body uploads
// via PUT stay safe.
func requireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost || r.Method == http.MethodPatch {
			mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if mt != "application/json" {
				writeProblem(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

const panelCSP = "default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; frame-src 'self' blob:; " +
	"style-src 'self' 'unsafe-inline'; frame-ancestors 'none'"

// contentCSP is applied to user-uploaded content served from the panel origin
// so an uploaded HTML or SVG file cannot run script with the panel's cookies.
const contentCSP = "sandbox; default-src 'none'; img-src 'self' data:; media-src 'self'; style-src 'unsafe-inline'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", panelCSP)
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	n, err := r.ResponseWriter.Write(p)
	r.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (s *Server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		s.metrics.ObserveHTTP("panel", r.Method, rec.status, rec.bytes, r.ContentLength)
		slog.Debug("http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration", time.Since(start).Round(time.Microsecond))
	})
}

func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.Error("panic in handler", "path", r.URL.Path, "panic", v, "stack", string(debug.Stack()))
				writeProblem(w, http.StatusInternalServerError, "an internal error occurred")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the remote peer address. Proxy headers are deliberately not
// trusted yet; add an explicit trusted-proxy setting before honouring them.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
