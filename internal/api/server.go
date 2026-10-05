// Package api implements the HTTP management API (/api/v1), public share
// endpoints, and serves the embedded web panel.
package api

import (
	"database/sql"
	"net/http"
	"strings"
	"time"

	"acs/internal/audit"
	"acs/internal/auth"
	"acs/internal/config"
	"acs/internal/metrics"
	"acs/internal/object"
	"acs/internal/settings"
	"acs/internal/share"
	"acs/internal/webhook"
)

type Deps struct {
	Config   config.Config
	DB       *sql.DB
	Auth     *auth.Service
	Objects  *object.Service
	Shares   *share.Service
	Webhooks *webhook.Manager
	Audit    *audit.Log
	Settings *settings.Store
	Metrics  *metrics.Registry
}

type Server struct {
	cfg      config.Config
	db       *sql.DB
	auth     *auth.Service
	objects  *object.Service
	shares   *share.Service
	webhooks *webhook.Manager
	audit    *audit.Log
	settings *settings.Store
	metrics  *metrics.Registry
	limiter  *auth.AttemptLimiter
	// shareLimiter throttles share password attempts per client.
	shareLimiter *auth.AttemptLimiter
	startedAt    time.Time
}

func NewServer(d Deps) *Server {
	return &Server{
		cfg:          d.Config,
		db:           d.DB,
		auth:         d.Auth,
		objects:      d.Objects,
		shares:       d.Shares,
		webhooks:     d.Webhooks,
		audit:        d.Audit,
		settings:     d.Settings,
		metrics:      d.Metrics,
		limiter:      auth.NewAttemptLimiter(10, time.Minute),
		shareLimiter: auth.NewAttemptLimiter(20, time.Minute),
		startedAt:    time.Now(),
	}
}

// Handler returns the root handler: the API under /api/ and the panel elsewhere.
func (s *Server) Handler(panel http.Handler) http.Handler {
	m := http.NewServeMux()
	a := s.requireAuth

	m.HandleFunc("GET /api/v1/health", s.handleHealth)
	m.HandleFunc("GET /api/v1/openapi.yaml", handleOpenAPI)

	// Setup and session auth.
	m.HandleFunc("GET /api/v1/setup", s.handleSetupStatus)
	m.HandleFunc("POST /api/v1/setup", s.handleSetup)
	m.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	m.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	m.HandleFunc("GET /api/v1/auth/me", a(s.handleMe))
	m.HandleFunc("POST /api/v1/auth/password", a(s.handleChangePassword))

	// Users (admin).
	m.HandleFunc("GET /api/v1/users", a(s.handleListUsers))
	m.HandleFunc("POST /api/v1/users", a(s.handleCreateUser))
	m.HandleFunc("PATCH /api/v1/users/{id}", a(s.handleUpdateUser))
	m.HandleFunc("DELETE /api/v1/users/{id}", a(s.handleDeleteUser))

	// Access keys.
	m.HandleFunc("GET /api/v1/keys", a(s.handleListKeys))
	m.HandleFunc("POST /api/v1/keys", a(s.handleCreateKey))
	m.HandleFunc("DELETE /api/v1/keys/{id}", a(s.handleDeleteKey))

	// Buckets.
	m.HandleFunc("GET /api/v1/buckets", a(s.handleListBuckets))
	m.HandleFunc("POST /api/v1/buckets", a(s.handleCreateBucket))
	m.HandleFunc("GET /api/v1/buckets/{bucket}", a(s.handleGetBucket))
	m.HandleFunc("PATCH /api/v1/buckets/{bucket}", a(s.handleUpdateBucket))
	m.HandleFunc("DELETE /api/v1/buckets/{bucket}", a(s.handleDeleteBucket))

	// Objects.
	m.HandleFunc("GET /api/v1/buckets/{bucket}/objects", a(s.handleListObjects))
	m.HandleFunc("GET /api/v1/buckets/{bucket}/objects/{key...}", a(s.handleDownload))
	m.HandleFunc("HEAD /api/v1/buckets/{bucket}/objects/{key...}", a(s.handleDownload))
	m.HandleFunc("PUT /api/v1/buckets/{bucket}/objects/{key...}", a(s.handleUpload))
	m.HandleFunc("DELETE /api/v1/buckets/{bucket}/objects/{key...}", a(s.handleDeleteObject))
	m.HandleFunc("GET /api/v1/buckets/{bucket}/meta/{key...}", a(s.handleObjectInfo))
	m.HandleFunc("PATCH /api/v1/buckets/{bucket}/meta/{key...}", a(s.handleUpdateObject))
	m.HandleFunc("GET /api/v1/buckets/{bucket}/versions/{key...}", a(s.handleObjectVersions))
	m.HandleFunc("POST /api/v1/buckets/{bucket}/delete", a(s.handleBulkDelete))
	m.HandleFunc("POST /api/v1/buckets/{bucket}/copy", a(s.handleCopy))
	m.HandleFunc("POST /api/v1/buckets/{bucket}/folders", a(s.handleCreateFolder))
	m.HandleFunc("POST /api/v1/buckets/{bucket}/restore", a(s.handleRestoreVersion))
	m.HandleFunc("GET /api/v1/buckets/{bucket}/zip", a(s.handleZip))

	// Multipart uploads.
	m.HandleFunc("GET /api/v1/buckets/{bucket}/uploads", a(s.handleListUploads))
	m.HandleFunc("POST /api/v1/buckets/{bucket}/uploads", a(s.handleCreateUpload))
	m.HandleFunc("PUT /api/v1/buckets/{bucket}/uploads/{id}/parts/{part}", a(s.handleUploadPart))
	m.HandleFunc("POST /api/v1/buckets/{bucket}/uploads/{id}/complete", a(s.handleCompleteUpload))
	m.HandleFunc("DELETE /api/v1/buckets/{bucket}/uploads/{id}", a(s.handleAbortUpload))

	// Share links.
	m.HandleFunc("GET /api/v1/shares", a(s.handleListShares))
	m.HandleFunc("POST /api/v1/shares", a(s.handleCreateShare))
	m.HandleFunc("PATCH /api/v1/shares/{id}", a(s.handleUpdateShare))
	m.HandleFunc("DELETE /api/v1/shares/{id}", a(s.handleDeleteShare))

	// Public share access (no authentication).
	m.HandleFunc("GET /api/v1/public/shares/{token}", s.handlePublicShare)
	m.HandleFunc("POST /api/v1/public/shares/{token}/unlock", s.handlePublicUnlock)
	m.HandleFunc("GET /api/v1/public/shares/{token}/list", s.handlePublicList)
	m.HandleFunc("GET /api/v1/public/shares/{token}/download", s.handlePublicDownload)
	m.HandleFunc("GET /api/v1/public/shares/{token}/zip", s.handlePublicZip)
	m.HandleFunc("PUT /api/v1/public/shares/{token}/upload/{name...}", s.handlePublicUpload)

	// Administration.
	m.HandleFunc("GET /api/v1/settings", a(s.handleGetSettings))
	m.HandleFunc("PUT /api/v1/settings", a(s.handleUpdateSettings))
	m.HandleFunc("GET /api/v1/audit", a(s.handleAudit))
	m.HandleFunc("GET /api/v1/webhooks", a(s.handleListWebhooks))
	m.HandleFunc("GET /api/v1/webhooks/events", a(s.handleWebhookEvents))
	m.HandleFunc("POST /api/v1/webhooks", a(s.handleCreateWebhook))
	m.HandleFunc("PUT /api/v1/webhooks/{id}", a(s.handleUpdateWebhook))
	m.HandleFunc("DELETE /api/v1/webhooks/{id}", a(s.handleDeleteWebhook))
	m.HandleFunc("POST /api/v1/webhooks/{id}/test", a(s.handleTestWebhook))
	m.HandleFunc("GET /api/v1/webhooks/{id}/deliveries", a(s.handleWebhookDeliveries))
	m.HandleFunc("GET /api/v1/system", a(s.handleSystemInfo))

	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, http.StatusNotFound, "no such endpoint")
	})

	apiHandler := requireJSON(m)
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/metrics":
			s.handleMetrics(w, r)
		case r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/"):
			w.Header().Set("Cache-Control", "no-store")
			apiHandler.ServeHTTP(w, r)
		default:
			panel.ServeHTTP(w, r)
		}
	})
	return recoverPanics(s.observe(securityHeaders(root)))
}
