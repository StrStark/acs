package api

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"acs/internal/auth"
	"acs/internal/replica"
	"acs/internal/storage"
	"acs/internal/sysinfo"
)

// builtinBackend describes the default store in the data directory.
type builtinBackend struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Path    string         `json:"path,omitempty"`
	Builtin bool           `json:"builtin"`
	Status  storage.Status `json:"status"`
}

type backendJSON struct {
	storage.Backend
	Buckets     int   `json:"buckets"`
	StoredBytes int64 `json:"storedBytes"`
	// Replica is the state of this backend's metadata copy.
	Replica *replica.TargetStatus `json:"replica,omitempty"`
}

type storageList struct {
	Builtin  builtinBackend `json:"builtin"`
	Backends []backendJSON  `json:"backends"`
	// Usage of the built-in store.
	BuiltinBuckets     int                   `json:"builtinBuckets"`
	BuiltinStoredBytes int64                 `json:"builtinStoredBytes"`
	BuiltinReplica     *replica.TargetStatus `json:"builtinReplica,omitempty"`
	RecoveryConfigured bool                  `json:"recoveryConfigured"`
}

func (s *Server) handleListStorage(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActManageBuckets, "") {
		return
	}
	buckets, err := s.objects.ListBuckets(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	counts := map[string]int{}
	stored := map[string]int64{}
	for _, b := range buckets {
		counts[b.Backend]++
		if st, err := s.objects.BucketStats(r.Context(), b.Name); err == nil {
			stored[b.Backend] += st.StoredBytes
		}
	}

	out := storageList{Backends: []backendJSON{}}
	out.Builtin = builtinBackend{ID: "", Name: "Built-in disk", Type: storage.TypeLocal, Builtin: true}
	out.Builtin.Status.Online = true
	if principal(r).Can(auth.ActAdmin, "") {
		out.Builtin.Path = filepath.Join(s.cfg.DataDir, "objects")
	}
	if du, err := sysinfo.Disk(s.cfg.DataDir); err == nil {
		out.Builtin.Status.TotalBytes, out.Builtin.Status.FreeBytes = du.Total, du.Free
	}
	out.BuiltinBuckets, out.BuiltinStoredBytes = counts[""], stored[""]
	reps := s.recovery.Replicator.Status()
	if st, ok := reps[""]; ok {
		out.BuiltinReplica = &st
	}
	out.RecoveryConfigured = replica.LoadKeyring(r.Context(), s.db) != nil
	for _, b := range s.storage.List() {
		bj := backendJSON{Backend: b, Buckets: counts[b.ID], StoredBytes: stored[b.ID]}
		if st, ok := reps[b.ID]; ok {
			bj.Replica = &st
		}
		out.Backends = append(out.Backends, bj)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleTestStorage(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	var in storage.Input
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.storage.Test(r.Context(), in); err != nil {
		var ve *storage.ValidationError
		if errors.As(err, &ve) {
			writeStorageError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	resp := map[string]any{"ok": true}
	if m := s.existingACS(r.Context(), in); m != nil {
		resp["existing"] = map[string]any{
			"siteName": m.SiteName, "buckets": m.Buckets, "updatedAt": m.UpdatedAt,
			"foreign": m.Installation != s.recovery.Installation,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

type createStorageRequest struct {
	storage.Input
	// Import takes over a provider that another installation wrote to.
	Import bool `json:"import"`
}

// existingACS reports ACS metadata already present on an S3 provider.
func (s *Server) existingACS(ctx context.Context, in storage.Input) *replica.Manifest {
	if in.Type != storage.TypeS3 {
		return nil
	}
	area, err := storage.AreaFor(in)
	if err != nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	m, err := replica.ReadManifest(cctx, area)
	if err != nil {
		return nil
	}
	return m
}

func (s *Server) handleCreateStorage(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	var req createStorageRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	in := req.Input
	ctx := r.Context()
	var b storage.Backend
	var err error
	m := s.existingACS(ctx, in)
	switch {
	case m == nil:
		b, err = s.storage.Create(ctx, in)
	case m.Installation == s.recovery.Installation:
		// Re-attaching our own provider: keep its ID so buckets find it.
		b, err = s.storage.CreateWithID(ctx, m.Backend, in)
	case !req.Import:
		writeProblem(w, http.StatusConflict, "this storage already holds data from the ACS installation \""+m.SiteName+"\"; import it to take over its buckets")
		return
	default:
		if b, err = s.storage.CreateWithID(ctx, m.Backend, in); err == nil {
			area, _ := storage.AreaFor(in)
			var res *importResult
			if res, err = s.importFrom(ctx, area, b.ID, m); err == nil {
				s.audit.Record(ctx, principal(r).Name(), "storage.import", b.Name, clientIP(r),
					map[string]any{"imported": res.Imported, "skipped": res.Skipped})
				writeJSON(w, http.StatusCreated, map[string]any{"backend": backendJSON{Backend: b}, "imported": res.Imported, "skipped": res.Skipped})
				return
			}
		}
	}
	if err != nil {
		writeStorageError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), principal(r).Name(), "storage.create", b.Name, clientIP(r),
		map[string]any{"id": b.ID, "type": b.Type, "endpoint": b.Endpoint, "path": b.Path})
	writeJSON(w, http.StatusCreated, backendJSON{Backend: b})
}

func (s *Server) handleUpdateStorage(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	var in storage.Input
	if !decodeJSON(w, r, &in) {
		return
	}
	b, err := s.storage.Update(r.Context(), r.PathValue("id"), in)
	if err != nil {
		writeStorageError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), principal(r).Name(), "storage.update", b.Name, clientIP(r), map[string]any{"id": b.ID})
	writeJSON(w, http.StatusOK, backendJSON{Backend: b})
}

func (s *Server) handleDeleteStorage(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	id := r.PathValue("id")
	buckets, err := s.objects.ListBuckets(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	inUse := false
	for _, b := range buckets {
		if b.Backend == id {
			inUse = true
			break
		}
	}
	b, _ := s.storage.Get(id)
	if err := s.storage.Delete(r.Context(), id, inUse); err != nil {
		writeStorageError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), principal(r).Name(), "storage.delete", b.Name, clientIP(r), map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCheckStorage(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActManageBuckets, "") {
		return
	}
	b, err := s.storage.Check(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStorageError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, backendJSON{Backend: b})
}

func writeStorageError(w http.ResponseWriter, r *http.Request, err error) {
	var ve *storage.ValidationError
	switch {
	case errors.As(err, &ve):
		writeProblemFields(w, http.StatusUnprocessableEntity, ve.Error(), map[string]string{ve.Field: ve.Message})
	case errors.Is(err, storage.ErrNotFound):
		writeProblem(w, http.StatusNotFound, err.Error())
	case errors.Is(err, storage.ErrInUse):
		writeProblem(w, http.StatusConflict, err.Error()+"; move or delete those buckets first")
	default:
		// Connection failures from the provider are the user's to fix.
		writeProblem(w, http.StatusUnprocessableEntity, err.Error())
	}
}
