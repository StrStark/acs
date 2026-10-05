package api

import (
	"net/http"
	"time"

	"acs/internal/auth"
)

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	users, err := s.auth.ListUsers(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	var req createUserRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	u, err := s.auth.CreateUser(r.Context(), req.Username, req.Password, req.Role)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), principal(r).Name(), "user.create", u.Username, clientIP(r), map[string]any{"role": u.Role})
	writeJSON(w, http.StatusCreated, u)
}

type updateUserRequest struct {
	Role     string `json:"role"`
	Password string `json:"password"`
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req updateUserRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	u, err := s.auth.UpdateUser(r.Context(), id, req.Role, req.Password)
	if err != nil {
		writeError(w, r, err)
		return
	}
	detail := map[string]any{}
	if req.Role != "" {
		detail["role"] = req.Role
	}
	if req.Password != "" {
		detail["passwordReset"] = true
	}
	s.audit.Record(r.Context(), principal(r).Name(), "user.update", u.Username, clientIP(r), detail)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	p := principal(r)
	if id == p.User.ID {
		writeProblem(w, http.StatusConflict, "you cannot delete your own account")
		return
	}
	u, err := s.auth.GetUser(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.auth.DeleteUser(r.Context(), id); err != nil {
		writeError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), p.Name(), "user.delete", u.Username, clientIP(r), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	userID := p.User.ID
	if r.URL.Query().Get("all") == "true" {
		if !can(w, r, auth.ActAdmin, "") {
			return
		}
		userID = 0
	}
	keys, err := s.auth.ListAccessKeys(r.Context(), userID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

type createKeyRequest struct {
	Name       string     `json:"name"`
	Permission string     `json:"permission"`
	Buckets    []string   `json:"buckets"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	// UserID creates the key for another user (admin only).
	UserID int64 `json:"userId"`
}

type createKeyResponse struct {
	*auth.AccessKey
	Secret string `json:"secret"`
}

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	var req createKeyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	userID := p.User.ID
	if req.UserID != 0 && req.UserID != p.User.ID {
		if !can(w, r, auth.ActAdmin, "") {
			return
		}
		userID = req.UserID
	}
	// A key cannot be broader than the key used to create it.
	if p.Key != nil {
		rank := map[string]int{auth.KeyRead: 0, auth.KeyReadWrite: 1, "": 1, auth.KeyFull: 2}
		if rank[req.Permission] > rank[p.Key.Permission] {
			writeProblem(w, http.StatusForbidden, "cannot create a key with more permission than your own")
			return
		}
		if !p.AllBuckets() {
			for _, b := range req.Buckets {
				if !p.Key.AllowsBucket(b) {
					writeProblem(w, http.StatusForbidden, "cannot grant access to bucket "+b)
					return
				}
			}
			if len(req.Buckets) == 0 {
				req.Buckets = p.Key.Buckets
			}
		}
	}
	k, secret, err := s.auth.CreateAccessKey(r.Context(), userID, auth.CreateKeyInput{
		Name: req.Name, Permission: req.Permission, Buckets: req.Buckets, ExpiresAt: req.ExpiresAt,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), p.Name(), "key.create", k.ID, clientIP(r),
		map[string]any{"name": k.Name, "permission": k.Permission, "buckets": k.Buckets, "owner": k.Username})
	writeJSON(w, http.StatusCreated, createKeyResponse{AccessKey: k, Secret: secret})
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	k, err := s.auth.GetAccessKey(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	if k.UserID != p.User.ID && !can(w, r, auth.ActAdmin, "") {
		return
	}
	if err := s.auth.DeleteAccessKey(r.Context(), k.ID); err != nil {
		writeError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), p.Name(), "key.delete", k.ID, clientIP(r), map[string]any{"name": k.Name})
	w.WriteHeader(http.StatusNoContent)
}
