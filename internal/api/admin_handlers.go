package api

import (
	"net"
	"net/http"
	"strconv"
	"strings"

	"acs/internal/audit"
	"acs/internal/auth"
	"acs/internal/settings"
	"acs/internal/webhook"
)

type settingsResponse struct {
	settings.Settings
	// S3Endpoint is the effective endpoint shown to users.
	S3Endpoint string `json:"s3Endpoint"`
	S3Enabled  bool   `json:"s3Enabled"`
}

// s3Endpoint returns the configured public S3 URL or derives one from the
// request host and the S3 listen port.
func (s *Server) s3Endpoint(r *http.Request) string {
	if u := s.settings.Get().S3PublicURL; u != "" {
		return u
	}
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	port := s.cfg.S3Listen
	if i := strings.LastIndexByte(port, ':'); i >= 0 {
		port = port[i+1:]
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + host + ":" + port
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, settingsResponse{
		Settings: s.settings.Get(), S3Endpoint: s.s3Endpoint(r), S3Enabled: s.cfg.S3Listen != "",
	})
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	var req settings.Settings
	if !decodeJSON(w, r, &req) {
		return
	}
	updated, err := s.settings.Update(r.Context(), req)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), principal(r).Name(), "settings.update", "", clientIP(r), map[string]any{
		"siteName": updated.SiteName, "publicUrl": updated.PublicURL, "s3PublicUrl": updated.S3PublicURL, "region": updated.Region,
	})
	writeJSON(w, http.StatusOK, settingsResponse{Settings: updated, S3Endpoint: s.s3Endpoint(r), S3Enabled: s.cfg.S3Listen != ""})
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	entries, err := s.audit.List(r.Context(), audit.Query{Search: q.Get("search"), Before: before, Limit: limit})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func (s *Server) handleWebhookEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, webhook.EventTypes)
}

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	hooks, err := s.webhooks.List(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, hooks)
}

type webhookRequest struct {
	Name    string   `json:"name"`
	URL     string   `json:"url"`
	Events  []string `json:"events"`
	Bucket  string   `json:"bucket"`
	Prefix  string   `json:"prefix"`
	Enabled bool     `json:"enabled"`
}

func (req webhookRequest) input() webhook.Input {
	return webhook.Input{Name: req.Name, URL: req.URL, Events: req.Events, Bucket: req.Bucket, Prefix: req.Prefix, Enabled: req.Enabled}
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	var req webhookRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	h, err := s.webhooks.Create(r.Context(), req.input())
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), principal(r).Name(), "webhook.create", h.URL, clientIP(r), map[string]any{"id": h.ID})
	writeJSON(w, http.StatusCreated, h)
}

func (s *Server) handleUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var req webhookRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	h, err := s.webhooks.Update(r.Context(), id, req.input())
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), principal(r).Name(), "webhook.update", h.URL, clientIP(r), map[string]any{"id": h.ID})
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := s.webhooks.Delete(r.Context(), id); err != nil {
		writeError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), principal(r).Name(), "webhook.delete", strconv.FormatInt(id, 10), clientIP(r), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	d, err := s.webhooks.Test(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleWebhookDeliveries(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	ds, err := s.webhooks.Deliveries(r.Context(), id, 50)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ds)
}
