package api

import (
	"errors"
	"net/http"
	"path/filepath"

	"acs/internal/auth"
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
}

type storageList struct {
	Builtin  builtinBackend `json:"builtin"`
	Backends []backendJSON  `json:"backends"`
	// Usage of the built-in store.
	BuiltinBuckets     int   `json:"builtinBuckets"`
	BuiltinStoredBytes int64 `json:"builtinStoredBytes"`
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
	for _, b := range s.storage.List() {
		out.Backends = append(out.Backends, backendJSON{Backend: b, Buckets: counts[b.ID], StoredBytes: stored[b.ID]})
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
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleCreateStorage(w http.ResponseWriter, r *http.Request) {
	if !can(w, r, auth.ActAdmin, "") {
		return
	}
	var in storage.Input
	if !decodeJSON(w, r, &in) {
		return
	}
	b, err := s.storage.Create(r.Context(), in)
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
