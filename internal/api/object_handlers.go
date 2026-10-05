package api

import (
	"archive/zip"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"acs/internal/auth"
	"acs/internal/object"
)

type objectJSON struct {
	Key          string            `json:"key"`
	Size         int64             `json:"size"`
	ETag         string            `json:"etag,omitempty"`
	ContentType  string            `json:"contentType,omitempty"`
	LastModified time.Time         `json:"lastModified"`
	VersionID    string            `json:"versionId,omitempty"`
	IsLatest     bool              `json:"isLatest"`
	DeleteMarker bool              `json:"deleteMarker,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
}

func toObjectJSON(o *object.Object, full bool) objectJSON {
	j := objectJSON{
		Key: o.Key, Size: o.Size, ETag: o.ETag, ContentType: o.ContentType, LastModified: o.ModTime,
		VersionID: o.VersionID, IsLatest: o.IsLatest, DeleteMarker: o.DeleteMarker,
	}
	if full {
		j.Metadata, j.Headers, j.Tags = o.UserMeta, o.Headers, o.Tags
	}
	return j
}

func encodeCursor(marker string) string {
	if marker == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(marker))
}

func decodeCursor(c string) string {
	b, _ := base64.RawURLEncoding.DecodeString(c)
	return string(b)
}

type listResponse struct {
	Objects    []objectJSON `json:"objects"`
	Prefixes   []string     `json:"prefixes"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

func (s *Server) handleListObjects(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	if !can(w, r, auth.ActRead, bucket) {
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	res, err := s.objects.List(r.Context(), bucket, object.ListOptions{
		Prefix:    q.Get("prefix"),
		Delimiter: q.Get("delimiter"),
		Marker:    decodeCursor(q.Get("cursor")),
		MaxKeys:   limit,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := listResponse{Objects: []objectJSON{}, Prefixes: res.CommonPrefixes, NextCursor: encodeCursor(res.NextMarker)}
	if out.Prefixes == nil {
		out.Prefixes = []string{}
	}
	for _, o := range res.Objects {
		out.Objects = append(out.Objects, toObjectJSON(o, false))
	}
	writeJSON(w, http.StatusOK, out)
}

// storedHeaders are object headers replayed on download.
var storedHeaders = []string{"Cache-Control", "Content-Encoding", "Content-Language", "Expires"}

// serveObject streams an object with Range and conditional request support.
func (s *Server) serveObject(w http.ResponseWriter, r *http.Request, o *object.Object, attachment bool) {
	h := w.Header()
	h.Set("Content-Security-Policy", contentCSP)
	h.Set("ETag", `"`+o.ETag+`"`)
	h.Set("Content-Type", o.ContentType)
	h.Set("Cache-Control", "private, no-cache")
	for _, name := range storedHeaders {
		if v := o.Headers[name]; v != "" {
			h.Set(name, v)
		}
	}
	disp := "inline"
	if attachment {
		disp = "attachment"
	}
	h.Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": path.Base(o.Key)}))
	if o.VersionID != "" {
		h.Set("X-ACS-Version-Id", o.VersionID)
	}
	rd := s.objects.Open(o)
	defer rd.Close()
	http.ServeContent(w, r, "", o.ModTime, rd)
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	bucket, key := r.PathValue("bucket"), r.PathValue("key")
	if !can(w, r, auth.ActRead, bucket) {
		return
	}
	o, err := s.objects.StatObject(r.Context(), bucket, key, r.URL.Query().Get("versionId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.serveObject(w, r, o, r.URL.Query().Get("download") == "1")
}

const metaHeaderPrefix = "X-Acs-Meta-"

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	bucket, key := r.PathValue("bucket"), r.PathValue("key")
	if !can(w, r, auth.ActWrite, bucket) {
		return
	}
	opts := object.PutOptions{Size: r.ContentLength, ContentType: r.Header.Get("Content-Type")}
	for name, vals := range r.Header {
		if strings.HasPrefix(name, metaHeaderPrefix) && len(vals) > 0 {
			if opts.UserMeta == nil {
				opts.UserMeta = map[string]string{}
			}
			opts.UserMeta[strings.ToLower(strings.TrimPrefix(name, metaHeaderPrefix))] = vals[0]
		}
	}
	if v := r.Header.Get("Content-MD5"); v != "" {
		sum, err := base64.StdEncoding.DecodeString(v)
		if err != nil || len(sum) != 16 {
			writeProblem(w, http.StatusBadRequest, "invalid Content-MD5")
			return
		}
		opts.ContentMD5 = sum
	}
	o, err := s.objects.PutObject(r.Context(), bucket, key, r.Body, opts)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toObjectJSON(o, true))
}

func (s *Server) handleDeleteObject(w http.ResponseWriter, r *http.Request) {
	bucket, key := r.PathValue("bucket"), r.PathValue("key")
	if !can(w, r, auth.ActWrite, bucket) {
		return
	}
	versionID := r.URL.Query().Get("versionId")
	res, err := s.objects.DeleteObject(r.Context(), bucket, key, versionID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	detail := map[string]any{}
	if versionID != "" {
		detail["versionId"] = versionID
	}
	s.audit.Record(r.Context(), principal(r).Name(), "object.delete", bucket+"/"+key, clientIP(r), detail)
	writeJSON(w, http.StatusOK, map[string]any{"versionId": res.VersionID, "deleteMarker": res.DeleteMarker})
}

func (s *Server) handleObjectInfo(w http.ResponseWriter, r *http.Request) {
	bucket, key := r.PathValue("bucket"), r.PathValue("key")
	if !can(w, r, auth.ActRead, bucket) {
		return
	}
	o, err := s.objects.StatObject(r.Context(), bucket, key, r.URL.Query().Get("versionId"))
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toObjectJSON(o, true))
}

type updateObjectRequest struct {
	ContentType *string            `json:"contentType"`
	Metadata    *map[string]string `json:"metadata"`
	Headers     *map[string]string `json:"headers"`
	Tags        *map[string]string `json:"tags"`
}

func (s *Server) handleUpdateObject(w http.ResponseWriter, r *http.Request) {
	bucket, key := r.PathValue("bucket"), r.PathValue("key")
	if !can(w, r, auth.ActWrite, bucket) {
		return
	}
	var req updateObjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	o, err := s.objects.StatObject(ctx, bucket, key, "")
	if err != nil {
		writeError(w, r, err)
		return
	}
	if req.ContentType != nil || req.Metadata != nil || req.Headers != nil {
		ct, meta, hdrs := o.ContentType, o.UserMeta, o.Headers
		if req.ContentType != nil {
			ct = *req.ContentType
		}
		if req.Metadata != nil {
			meta = *req.Metadata
		}
		if req.Headers != nil {
			hdrs = map[string]string{}
			for k, v := range *req.Headers {
				k = http.CanonicalHeaderKey(k)
				if k == "Content-Disposition" || slices.Contains(storedHeaders, k) {
					hdrs[k] = v
				}
			}
		}
		if o, err = s.objects.UpdateObjectMetadata(ctx, bucket, key, ct, meta, hdrs); err != nil {
			writeError(w, r, err)
			return
		}
	}
	if req.Tags != nil {
		if len(*req.Tags) > 10 {
			writeProblem(w, http.StatusBadRequest, "an object can have at most 10 tags")
			return
		}
		if o, err = s.objects.SetObjectTags(ctx, bucket, key, "", *req.Tags); err != nil {
			writeError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, toObjectJSON(o, true))
}

func (s *Server) handleObjectVersions(w http.ResponseWriter, r *http.Request) {
	bucket, key := r.PathValue("bucket"), r.PathValue("key")
	if !can(w, r, auth.ActRead, bucket) {
		return
	}
	vers, err := s.objects.ObjectVersions(r.Context(), bucket, key)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := []objectJSON{}
	for _, v := range vers {
		out = append(out, toObjectJSON(v, false))
	}
	writeJSON(w, http.StatusOK, out)
}

type versionRef struct {
	Key       string `json:"key"`
	VersionID string `json:"versionId"`
}

type bulkDeleteRequest struct {
	Keys     []string     `json:"keys"`
	Prefixes []string     `json:"prefixes"`
	Versions []versionRef `json:"versions"`
}

type bulkError struct {
	Key   string `json:"key"`
	Error string `json:"error"`
}

// eachUnder calls fn for every current object under prefix.
func (s *Server) eachUnder(r *http.Request, bucket, prefix string, fn func(*object.Object) error) error {
	marker := ""
	for {
		res, err := s.objects.List(r.Context(), bucket, object.ListOptions{Prefix: prefix, Marker: marker})
		if err != nil {
			return err
		}
		for _, o := range res.Objects {
			if err := fn(o); err != nil {
				return err
			}
		}
		if !res.IsTruncated {
			return nil
		}
		marker = res.NextMarker
	}
}

func (s *Server) handleBulkDelete(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	if !can(w, r, auth.ActWrite, bucket) {
		return
	}
	var req bulkDeleteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	deleted := 0
	errs := []bulkError{}
	del := func(key, version string) {
		if _, err := s.objects.DeleteObject(ctx, bucket, key, version); err != nil {
			errs = append(errs, bulkError{Key: key, Error: err.Error()})
			return
		}
		deleted++
	}
	for _, k := range req.Keys {
		del(k, "")
	}
	for _, v := range req.Versions {
		del(v.Key, v.VersionID)
	}
	for _, p := range req.Prefixes {
		if p == "" {
			writeProblem(w, http.StatusBadRequest, "refusing to delete the whole bucket via an empty prefix")
			return
		}
		// Collect first: deleting while paginating would shift the listing.
		var keys []string
		if err := s.eachUnder(r, bucket, p, func(o *object.Object) error {
			keys = append(keys, o.Key)
			return nil
		}); err != nil {
			writeError(w, r, err)
			return
		}
		for _, k := range keys {
			del(k, "")
		}
	}
	s.audit.Record(ctx, principal(r).Name(), "object.bulk_delete", bucket, clientIP(r), map[string]any{
		"keys": len(req.Keys), "prefixes": req.Prefixes, "versions": len(req.Versions), "deleted": deleted,
	})
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "errors": errs})
}

type copyRequest struct {
	SourceBucket    string `json:"sourceBucket"`
	SourceKey       string `json:"sourceKey"`
	SourceVersionID string `json:"sourceVersionId"`
	// Key is the destination key in the path bucket. When SourceKey ends with
	// "/", the whole folder is copied and Key must end with "/" too.
	Key  string `json:"key"`
	Move bool   `json:"move"`
}

func (s *Server) handleCopy(w http.ResponseWriter, r *http.Request) {
	dstBucket := r.PathValue("bucket")
	var req copyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.SourceBucket == "" {
		req.SourceBucket = dstBucket
	}
	srcAction := auth.ActRead
	if req.Move {
		srcAction = auth.ActWrite
	}
	if !can(w, r, srcAction, req.SourceBucket) || !can(w, r, auth.ActWrite, dstBucket) {
		return
	}
	ctx := r.Context()
	one := func(srcKey, dstKey string) (*object.Object, error) {
		if req.Move {
			return s.objects.MoveObject(ctx, req.SourceBucket, srcKey, dstBucket, dstKey)
		}
		return s.objects.CopyObject(ctx, req.SourceBucket, srcKey, dstBucket, dstKey,
			object.CopyOptions{SrcVersionID: req.SourceVersionID})
	}

	if !strings.HasSuffix(req.SourceKey, "/") {
		o, err := one(req.SourceKey, req.Key)
		if err != nil {
			writeError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"count": 1, "object": toObjectJSON(o, false)})
		return
	}

	if !strings.HasSuffix(req.Key, "/") {
		req.Key += "/"
	}
	if req.SourceBucket == dstBucket && strings.HasPrefix(req.Key, req.SourceKey) {
		writeProblem(w, http.StatusBadRequest, "cannot copy or move a folder into itself")
		return
	}
	var keys []string
	if err := s.eachUnder(r, req.SourceBucket, req.SourceKey, func(o *object.Object) error {
		keys = append(keys, o.Key)
		return nil
	}); err != nil {
		writeError(w, r, err)
		return
	}
	for _, k := range keys {
		if _, err := one(k, req.Key+strings.TrimPrefix(k, req.SourceKey)); err != nil {
			writeError(w, r, err)
			return
		}
	}
	if req.Move {
		s.audit.Record(ctx, principal(r).Name(), "object.move", req.SourceBucket+"/"+req.SourceKey, clientIP(r),
			map[string]any{"to": dstBucket + "/" + req.Key, "count": len(keys)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(keys)})
}

type folderRequest struct {
	Prefix string `json:"prefix"`
}

func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	if !can(w, r, auth.ActWrite, bucket) {
		return
	}
	var req folderRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p := strings.TrimLeft(req.Prefix, "/")
	if p == "" || strings.Contains(p, "//") {
		writeProblemFields(w, http.StatusUnprocessableEntity, "invalid folder name", map[string]string{"prefix": "is invalid"})
		return
	}
	if !strings.HasSuffix(p, "/") {
		p += "/"
	}
	o, err := s.objects.PutObject(r.Context(), bucket, p, strings.NewReader(""), object.PutOptions{Size: 0, ContentType: "application/x-directory"})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toObjectJSON(o, false))
}

func (s *Server) handleRestoreVersion(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	if !can(w, r, auth.ActWrite, bucket) {
		return
	}
	var req versionRef
	if !decodeJSON(w, r, &req) {
		return
	}
	o, err := s.objects.CopyObject(r.Context(), bucket, req.Key, bucket, req.Key, object.CopyOptions{SrcVersionID: req.VersionID})
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), principal(r).Name(), "object.restore", bucket+"/"+req.Key, clientIP(r),
		map[string]any{"versionId": req.VersionID})
	writeJSON(w, http.StatusOK, toObjectJSON(o, false))
}

// writeZip streams objects as an uncompressed ZIP (most stored data is already
// compressed, and storing keeps CPU use and latency low).
func (s *Server) writeZip(w http.ResponseWriter, r *http.Request, filename string, objs []*object.Object, nameOf func(*object.Object) string) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	w.Header().Set("Content-Security-Policy", contentCSP)
	zw := zip.NewWriter(w)
	for _, o := range objs {
		name := nameOf(o)
		if name == "" || strings.HasSuffix(o.Key, "/") {
			continue
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store, Modified: o.ModTime})
		if err != nil {
			return
		}
		rd := s.objects.Open(o)
		_, err = io.Copy(fw, rd)
		rd.Close()
		if err != nil {
			// Headers are already sent; the truncated archive signals the failure.
			return
		}
	}
	zw.Close()
}

func (s *Server) handleZip(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	if !can(w, r, auth.ActRead, bucket) {
		return
	}
	q := r.URL.Query()
	prefix := q.Get("prefix")
	var objs []*object.Object
	if keys := q["key"]; len(keys) > 0 {
		for _, k := range keys {
			if strings.HasSuffix(k, "/") {
				if err := s.eachUnder(r, bucket, k, func(o *object.Object) error {
					objs = append(objs, o)
					return nil
				}); err != nil {
					writeError(w, r, err)
					return
				}
				continue
			}
			o, err := s.objects.StatObject(r.Context(), bucket, k, "")
			if err != nil {
				writeError(w, r, err)
				return
			}
			objs = append(objs, o)
		}
	} else if err := s.eachUnder(r, bucket, prefix, func(o *object.Object) error {
		objs = append(objs, o)
		return nil
	}); err != nil {
		writeError(w, r, err)
		return
	}
	name := bucket
	if p := strings.TrimSuffix(prefix, "/"); p != "" {
		name = path.Base(p)
	}
	s.writeZip(w, r, name+".zip", objs, func(o *object.Object) string { return strings.TrimPrefix(o.Key, prefix) })
}

// ---- multipart ----

type createUploadRequest struct {
	Key         string `json:"key"`
	ContentType string `json:"contentType"`
}

func (s *Server) handleCreateUpload(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	if !can(w, r, auth.ActWrite, bucket) {
		return
	}
	var req createUploadRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	u, err := s.objects.CreateUpload(r.Context(), bucket, req.Key, object.PutOptions{ContentType: req.ContentType})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"uploadId": u.ID, "key": u.Key, "minPartSize": object.MinPartSize})
}

func (s *Server) handleUploadPart(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	if !can(w, r, auth.ActWrite, bucket) {
		return
	}
	n, err := strconv.Atoi(r.PathValue("part"))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid part number")
		return
	}
	p, err := s.objects.UploadPart(r.Context(), bucket, r.URL.Query().Get("key"), r.PathValue("id"), n, r.Body, r.ContentLength, nil)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"partNumber": p.Number, "etag": p.ETag, "size": p.Size})
}

type completeUploadRequest struct {
	Key   string `json:"key"`
	Parts []struct {
		PartNumber int    `json:"partNumber"`
		ETag       string `json:"etag"`
	} `json:"parts"`
}

func (s *Server) handleCompleteUpload(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	if !can(w, r, auth.ActWrite, bucket) {
		return
	}
	var req completeUploadRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	parts := make([]object.CompletePart, len(req.Parts))
	for i, p := range req.Parts {
		parts[i] = object.CompletePart{Number: p.PartNumber, ETag: p.ETag}
	}
	o, err := s.objects.CompleteUpload(r.Context(), bucket, req.Key, r.PathValue("id"), parts)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toObjectJSON(o, true))
}

func (s *Server) handleAbortUpload(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	if !can(w, r, auth.ActWrite, bucket) {
		return
	}
	if err := s.objects.AbortUpload(r.Context(), bucket, r.URL.Query().Get("key"), r.PathValue("id")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListUploads(w http.ResponseWriter, r *http.Request) {
	bucket := r.PathValue("bucket")
	if !can(w, r, auth.ActRead, bucket) {
		return
	}
	uploads, _, err := s.objects.ListUploads(r.Context(), bucket, r.URL.Query().Get("prefix"), "", "", 1000)
	if err != nil {
		writeError(w, r, err)
		return
	}
	type uploadJSON struct {
		UploadID  string    `json:"uploadId"`
		Key       string    `json:"key"`
		Initiated time.Time `json:"initiated"`
	}
	out := []uploadJSON{}
	for _, u := range uploads {
		out = append(out, uploadJSON{u.ID, u.Key, u.Initiated})
	}
	writeJSON(w, http.StatusOK, out)
}

var errShareType = errors.New("unsupported for this share type")

// safeRelPath validates a client-supplied relative path inside a share.
func safeRelPath(p string) (string, error) {
	p = strings.TrimLeft(strings.ReplaceAll(p, "\\", "/"), "/")
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." {
			return "", fmt.Errorf("invalid path")
		}
	}
	return p, nil
}
