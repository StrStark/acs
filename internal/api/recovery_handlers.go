package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"acs/internal/alerts"
	"acs/internal/auth"
	"acs/internal/replica"
	"acs/internal/secret"
	"acs/internal/storage"
)

// Recovery wires the self-describing-storage features into the API.
type Recovery struct {
	Replicator   *replica.Replicator
	Alerts       *alerts.Service
	Checker      *alerts.Checker
	Box          *secret.Box
	Installation string
	// Restart re-executes the server after a staged restore.
	Restart func()
}

// ---- restore during first-run setup ----

type restoreCandidate struct {
	Path        string    `json:"path"`
	SiteName    string    `json:"siteName"`
	BackendName string    `json:"backendName"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Buckets     []string  `json:"buckets"`
	HasKeyring  bool      `json:"hasKeyring"`
}

func (s *Server) restoreCandidates() []restoreCandidate {
	out := []restoreCandidate{}
	for _, f := range replica.Scan(s.cfg.DrivesRoot) {
		if f.Manifest == nil {
			continue
		}
		m := f.Manifest
		out = append(out, restoreCandidate{Path: f.Path, SiteName: m.SiteName, BackendName: m.BackendName,
			UpdatedAt: m.UpdatedAt, Buckets: m.Buckets, HasKeyring: m.HasKeyring})
	}
	return out
}

type restoreRequest struct {
	// Path is a discovered drive; S3 is a provider to restore from instead.
	Path       string         `json:"path"`
	S3         *storage.Input `json:"s3"`
	Passphrase string         `json:"passphrase"`
	SetupToken string         `json:"setupToken"`
}

func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.Allow(clientIP(r)) {
		writeProblem(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	needs, err := s.auth.NeedsSetup(r.Context())
	if err != nil {
		writeInternalError(w, r, err)
		return
	}
	if !needs {
		writeProblem(w, http.StatusConflict, "this server is already set up; restore is only possible on a fresh installation")
		return
	}
	var req restoreRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.auth.SetupTokenRequired() && !s.auth.CheckSetupToken(req.SetupToken) {
		writeProblemFields(w, http.StatusForbidden, "invalid setup token", map[string]string{"setupToken": "is invalid"})
		return
	}

	var area replica.Area
	switch {
	case req.S3 != nil:
		req.S3.Type = storage.TypeS3
		if area, err = storage.AreaFor(*req.S3); err != nil {
			writeStorageError(w, r, err)
			return
		}
	case req.Path != "":
		known := false
		for _, c := range s.restoreCandidates() {
			if c.Path == req.Path {
				known = true
			}
		}
		if !known {
			writeProblemFields(w, http.StatusUnprocessableEntity, "no ACS storage found at this path", map[string]string{"path": "not found"})
			return
		}
		area = replica.NewLocalArea(filepath.Join(req.Path, ".acs"))
	default:
		writeProblem(w, http.StatusBadRequest, "choose a drive or an S3 provider to restore from")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	src, err := replica.ReadSource(ctx, area)
	if err != nil {
		writeProblem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if err := replica.Stage(s.cfg.DataDir, src, req.Passphrase); err != nil {
		if errors.Is(err, secret.ErrWrongPassphrase) {
			writeProblemFields(w, http.StatusUnprocessableEntity, err.Error(), map[string]string{"passphrase": "is incorrect"})
			return
		}
		writeProblem(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"restarting": true, "siteName": src.Manifest.SiteName, "secretsRestored": src.Keyring != nil,
	})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	go s.recovery.Restart()
}

// ---- recovery passphrase ----

func (s *Server) handleGetRecovery(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": replica.LoadKeyring(r.Context(), s.db) != nil})
}

type recoveryRequest struct {
	Passphrase      string `json:"passphrase"`
	CurrentPassword string `json:"currentPassword"`
}

func (s *Server) handleSetRecovery(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	if p.Key != nil {
		writeProblem(w, http.StatusForbidden, "the recovery passphrase can only be changed from the panel")
		return
	}
	if !s.limiter.Allow(clientIP(r)) {
		writeProblem(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	var req recoveryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ok, err := s.auth.CheckPassword(r.Context(), p.User.ID, req.CurrentPassword)
	if err != nil {
		writeInternalError(w, r, err)
		return
	}
	if !ok {
		writeProblemFields(w, http.StatusUnprocessableEntity, "current password is incorrect", map[string]string{"currentPassword": "is incorrect"})
		return
	}
	if len(req.Passphrase) < 12 {
		writeProblemFields(w, http.StatusUnprocessableEntity, "passphrase is too short", map[string]string{"passphrase": "must be at least 12 characters"})
		return
	}
	kr, err := s.recovery.Box.Lock(req.Passphrase)
	if err != nil {
		writeInternalError(w, r, err)
		return
	}
	if err := replica.SaveKeyring(r.Context(), s.db, kr.Marshal()); err != nil {
		writeInternalError(w, r, err)
		return
	}
	s.recovery.Replicator.Kick()
	s.audit.Record(r.Context(), p.Name(), "settings.recovery_passphrase", "", clientIP(r), nil)
	w.WriteHeader(http.StatusNoContent)
}

// ---- discovery and import ----

type discovered struct {
	Path string `json:"path"`
	// State: attached, detached (this installation, not attached), foreign
	// (another installation), or empty (a drive without ACS data).
	State       string     `json:"state"`
	BackendID   string     `json:"backendId,omitempty"`
	BackendName string     `json:"backendName,omitempty"`
	SiteName    string     `json:"siteName,omitempty"`
	UpdatedAt   *time.Time `json:"updatedAt,omitempty"`
	Buckets     []string   `json:"buckets,omitempty"`
}

func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	// Re-attach any known drive that moved before reporting.
	s.storage.Relocate(r.Context(), s.cfg.DrivesRoot)
	attached := map[string]string{}
	for _, b := range s.storage.List() {
		if b.Type == storage.TypeLocal {
			attached[b.ID] = b.Path
		}
	}
	status := s.recovery.Replicator.Status()
	builtin := filepath.Join(s.cfg.DataDir, "objects")
	out := []discovered{}
	for _, f := range replica.Scan(s.cfg.DrivesRoot) {
		if filepath.Clean(f.Path) == filepath.Clean(builtin) {
			continue
		}
		d := discovered{Path: f.Path, BackendID: f.Marker, State: "empty"}
		if m := f.Manifest; m != nil {
			t := m.UpdatedAt
			d.BackendName, d.SiteName, d.UpdatedAt, d.Buckets = m.BackendName, m.SiteName, &t, m.Buckets
			if d.BackendID == "" {
				d.BackendID = m.Backend
			}
			if m.Installation == s.recovery.Installation {
				d.State = "detached"
			} else {
				d.State = "foreign"
			}
		}
		// For attached drives the replicator knows best: one whose copy still
		// belongs to another installation (an import that did not finish) stays
		// "foreign" so it can be imported; an adopted one is ours even before
		// its manifest is rewritten.
		if path, ok := attached[d.BackendID]; ok && filepath.Clean(path) == filepath.Clean(f.Path) {
			if status[d.BackendID].Foreign == "" {
				d.State = "attached"
			} else {
				d.State = "foreign"
			}
		}
		out = append(out, d)
	}
	writeJSON(w, http.StatusOK, out)
}

type importRequest struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

// handleImportDrive attaches a discovered drive and imports its buckets.
func (s *Server) handleImportDrive(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	var req importRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	var found *replica.Found
	for _, f := range replica.Scan(s.cfg.DrivesRoot) {
		if f.Path == req.Path {
			found = &f
			break
		}
	}
	if found == nil || (found.Manifest == nil && found.Marker == "") {
		writeProblem(w, http.StatusUnprocessableEntity, "no ACS storage found at this path")
		return
	}
	id := found.Marker
	if id == "" {
		id = found.Manifest.Backend
	}
	name := req.Name
	if name == "" && found.Manifest != nil {
		name = found.Manifest.BackendName
	}
	// Already attached (for example after an interrupted import): import into it.
	b, err := s.storage.Get(id)
	if err != nil || b.Type != storage.TypeLocal || filepath.Clean(b.Path) != filepath.Clean(found.Path) {
		if b, err = s.storage.AdoptDrive(r.Context(), id, name, found.Path); err != nil {
			writeStorageError(w, r, err)
			return
		}
	}
	res, err := s.importFrom(r.Context(), replica.NewLocalArea(filepath.Join(found.Path, ".acs")), b.ID, found.Manifest)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), principal(r).Name(), "storage.import", b.Name, clientIP(r),
		map[string]any{"path": found.Path, "imported": res.Imported, "skipped": res.Skipped})
	writeJSON(w, http.StatusOK, map[string]any{"backend": backendJSON{Backend: b}, "imported": res.Imported, "skipped": res.Skipped})
}

type importResult struct {
	Imported []string
	Skipped  []string
}

// importFrom imports buckets of a foreign installation from a backend's
// metadata copy, then takes ownership of the backend and checks its data.
func (s *Server) importFrom(ctx context.Context, area replica.Area, backendID string, m *replica.Manifest) (*importResult, error) {
	res := &importResult{Imported: []string{}, Skipped: []string{}}
	if m != nil && m.Installation != s.recovery.Installation {
		src, err := replica.ReadSource(ctx, area)
		if err != nil {
			return nil, err
		}
		var snap io.Reader
		if src.Snapshot != nil {
			snap = bytes.NewReader(src.Snapshot)
		}
		r, err := s.objects.ImportBuckets(ctx, snap, src.Records, backendID)
		if err != nil {
			return nil, err
		}
		res.Imported, res.Skipped = r.Imported, r.Skipped
	}
	s.recovery.Replicator.Adopt(backendID)
	s.recovery.Alerts.ResolveFingerprint(ctx, alerts.KindForeignStorage+":"+backendID)
	s.recovery.Checker.Start(false)
	return res, nil
}

// ---- alerts ----

func (s *Server) handleListAlerts(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	list, err := s.recovery.Alerts.List(r.Context(), r.URL.Query().Get("state"), 0)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleAlertSummary(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	n, err := s.recovery.Alerts.OpenCount(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	last, running := s.recovery.Checker.Last()
	writeJSON(w, http.StatusOK, map[string]any{
		"open": n, "lastCheck": last, "checking": running,
		"recoveryConfigured": replica.LoadKeyring(r.Context(), s.db) != nil,
	})
}

func (s *Server) handleResolveAlert(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeProblem(w, http.StatusNotFound, "not found")
		return
	}
	if err := s.recovery.Alerts.Resolve(r.Context(), id); err != nil {
		if errors.Is(err, alerts.ErrNotFound) {
			writeProblem(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleResolveAllAlerts(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	if err := s.recovery.Alerts.ResolveAll(r.Context()); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRunCheck(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	var req struct {
		Deep bool `json:"deep"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	started := s.recovery.Checker.Start(req.Deep)
	writeJSON(w, http.StatusAccepted, map[string]any{"started": started})
}
