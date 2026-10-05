package api

import (
	"fmt"
	"net/http"

	"acs/internal/auth"
	"acs/internal/object"
)

type bucketJSON struct {
	*object.Bucket
	Stats object.Stats `json:"stats"`
}

func (s *Server) bucketWithStats(r *http.Request, b *object.Bucket) (bucketJSON, error) {
	st, err := s.objects.BucketStats(r.Context(), b.Name)
	return bucketJSON{Bucket: b, Stats: st}, err
}

func (s *Server) handleListBuckets(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	buckets, err := s.objects.ListBuckets(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := []bucketJSON{}
	for _, b := range buckets {
		if !p.Can(auth.ActRead, b.Name) {
			continue
		}
		bj, err := s.bucketWithStats(r, b)
		if err != nil {
			writeError(w, r, err)
			return
		}
		out = append(out, bj)
	}
	writeJSON(w, http.StatusOK, out)
}

type createBucketRequest struct {
	Name       string `json:"name"`
	Versioning bool   `json:"versioning"`
}

func (s *Server) handleCreateBucket(w http.ResponseWriter, r *http.Request) {
	var req createBucketRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !can(w, r, auth.ActManageBuckets, req.Name) {
		return
	}
	b, err := s.objects.CreateBucket(r.Context(), req.Name)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if req.Versioning {
		if b, err = s.objects.UpdateBucket(r.Context(), b.Name, func(b *object.Bucket) error {
			b.Versioning = object.VersioningEnabled
			return nil
		}); err != nil {
			writeError(w, r, err)
			return
		}
	}
	s.audit.Record(r.Context(), principal(r).Name(), "bucket.create", b.Name, clientIP(r), nil)
	bj, _ := s.bucketWithStats(r, b)
	writeJSON(w, http.StatusCreated, bj)
}

func (s *Server) handleGetBucket(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("bucket")
	if !can(w, r, auth.ActRead, name) {
		return
	}
	b, err := s.objects.GetBucket(r.Context(), name)
	if err != nil {
		writeError(w, r, err)
		return
	}
	bj, err := s.bucketWithStats(r, b)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, bj)
}

// updateBucketRequest fields are applied when present.
type updateBucketRequest struct {
	Versioning   *string                 `json:"versioning"`
	Public       *bool                   `json:"public"`
	QuotaBytes   *int64                  `json:"quotaBytes"`
	QuotaObjects *int64                  `json:"quotaObjects"`
	Lifecycle    *[]object.LifecycleRule `json:"lifecycle"`
	CORS         *[]object.CORSRule      `json:"cors"`
}

func (s *Server) handleUpdateBucket(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("bucket")
	if !can(w, r, auth.ActManageBuckets, name) {
		return
	}
	var req updateBucketRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	b, err := s.objects.UpdateBucket(r.Context(), name, func(b *object.Bucket) error {
		if req.Versioning != nil {
			b.Versioning = *req.Versioning
		}
		if req.Public != nil {
			b.Public = *req.Public
		}
		if req.QuotaBytes != nil {
			b.QuotaBytes = max(*req.QuotaBytes, 0)
		}
		if req.QuotaObjects != nil {
			b.QuotaObjects = max(*req.QuotaObjects, 0)
		}
		if req.Lifecycle != nil {
			for i, rule := range *req.Lifecycle {
				if rule.ID == "" {
					(*req.Lifecycle)[i].ID = fmt.Sprintf("rule-%d", i+1)
				}
				if rule.ExpirationDays < 0 || rule.NoncurrentDays < 0 || rule.AbortMultipartDays < 0 {
					return object.ErrInvalidArgument
				}
			}
			b.Lifecycle = *req.Lifecycle
		}
		if req.CORS != nil {
			b.CORS = *req.CORS
		}
		return nil
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), principal(r).Name(), "bucket.update", name, clientIP(r), nil)
	bj, _ := s.bucketWithStats(r, b)
	writeJSON(w, http.StatusOK, bj)
}

func (s *Server) handleDeleteBucket(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("bucket")
	if !can(w, r, auth.ActManageBuckets, name) {
		return
	}
	force := r.URL.Query().Get("force") == "true"
	if err := s.objects.DeleteBucket(r.Context(), name, force); err != nil {
		writeError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), principal(r).Name(), "bucket.delete", name, clientIP(r), map[string]any{"force": force})
	w.WriteHeader(http.StatusNoContent)
}
