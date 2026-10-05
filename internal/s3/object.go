package s3

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"acs/internal/auth"
	"acs/internal/object"
)

// storedHeaders are standard headers persisted with an object and replayed on GET.
var storedHeaders = []string{"Cache-Control", "Content-Disposition", "Content-Encoding", "Content-Language", "Expires"}

// putOptions collects object metadata from request headers.
func putOptions(r *http.Request) (object.PutOptions, *Error) {
	opts := object.PutOptions{ContentType: r.Header.Get("Content-Type")}
	for name, vals := range r.Header {
		if strings.HasPrefix(name, "X-Amz-Meta-") && len(vals) > 0 {
			if opts.UserMeta == nil {
				opts.UserMeta = map[string]string{}
			}
			opts.UserMeta[strings.ToLower(strings.TrimPrefix(name, "X-Amz-Meta-"))] = vals[0]
		}
	}
	for _, h := range storedHeaders {
		v := r.Header.Get(h)
		if h == "Content-Encoding" {
			// "aws-chunked" describes the transfer, not the stored object.
			var kept []string
			for _, enc := range strings.Split(v, ",") {
				if enc = strings.TrimSpace(enc); enc != "" && enc != "aws-chunked" {
					kept = append(kept, enc)
				}
			}
			v = strings.Join(kept, ",")
		}
		if v != "" {
			if opts.Headers == nil {
				opts.Headers = map[string]string{}
			}
			opts.Headers[h] = v
		}
	}
	if t := r.Header.Get("X-Amz-Tagging"); t != "" {
		tags, err := parseTagQuery(t)
		if err != nil {
			return opts, err
		}
		opts.Tags = tags
	}
	if v := r.Header.Get("Content-MD5"); v != "" {
		sum, err := base64.StdEncoding.DecodeString(v)
		if err != nil || len(sum) != 16 {
			return opts, newErr(http.StatusBadRequest, "InvalidDigest", "The Content-MD5 you specified was invalid.")
		}
		opts.ContentMD5 = sum
	}
	return opts, nil
}

func parseTagQuery(s string) (map[string]string, *Error) {
	vals, err := url.ParseQuery(s)
	if err != nil {
		return nil, ErrInvalidTag
	}
	tags := map[string]string{}
	for k, v := range vals {
		if len(v) > 0 {
			tags[k] = v[0]
		}
	}
	if e := validateTags(tags); e != nil {
		return nil, e
	}
	return tags, nil
}

func validateTags(tags map[string]string) *Error {
	if len(tags) > 10 {
		return newErr(http.StatusBadRequest, "BadRequest", "Object tags cannot be greater than 10")
	}
	for k, v := range tags {
		if k == "" || len(k) > 128 || len(v) > 256 {
			return ErrInvalidTag
		}
	}
	return nil
}

func (s *Server) versioned(q *request, bucket string) bool {
	b, err := s.objects.GetBucket(q.r.Context(), bucket)
	return err == nil && b.Versioning != object.VersioningOff
}

func etagMatch(header, etag string) bool {
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || strings.Trim(strings.TrimPrefix(part, "W/"), `"`) == etag {
			return true
		}
	}
	return false
}

// checkConditions evaluates If-* headers (RFC 7232 order).
// It returns 0 to proceed, 304 or 412.
func checkConditions(h http.Header, etag string, mod time.Time, prefix string) int {
	mod = mod.Truncate(time.Second)
	if v := h.Get(prefix + "If-Match"); v != "" {
		if !etagMatch(v, etag) {
			return http.StatusPreconditionFailed
		}
	} else if v := h.Get(prefix + "If-Unmodified-Since"); v != "" {
		if t, err := http.ParseTime(v); err == nil && mod.After(t) {
			return http.StatusPreconditionFailed
		}
	}
	if v := h.Get(prefix + "If-None-Match"); v != "" {
		if etagMatch(v, etag) {
			return http.StatusNotModified
		}
	} else if v := h.Get(prefix + "If-Modified-Since"); v != "" {
		if t, err := http.ParseTime(v); err == nil && !mod.After(t) {
			return http.StatusNotModified
		}
	}
	return 0
}

// parseRange parses a single "bytes=" range against size.
// ok=false means no (usable) Range header; err means unsatisfiable.
func parseRange(h string, size int64) (start, length int64, ok bool, err *Error) {
	spec, found := strings.CutPrefix(h, "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, 0, false, nil
	}
	a, b, _ := strings.Cut(strings.TrimSpace(spec), "-")
	switch {
	case a == "": // suffix range: last b bytes
		n, perr := strconv.ParseInt(b, 10, 64)
		if perr != nil {
			return 0, 0, false, nil
		}
		if n <= 0 || size == 0 {
			return 0, 0, false, ErrInvalidRange
		}
		n = min(n, size)
		return size - n, n, true, nil
	default:
		first, perr := strconv.ParseInt(a, 10, 64)
		if perr != nil || first < 0 {
			return 0, 0, false, nil
		}
		if first >= size {
			return 0, 0, false, ErrInvalidRange
		}
		last := size - 1
		if b != "" {
			l, perr := strconv.ParseInt(b, 10, 64)
			if perr != nil || l < first {
				return 0, 0, false, nil
			}
			last = min(l, size-1)
		}
		return first, last - first + 1, true, nil
	}
}

func (s *Server) setObjectHeaders(q *request, o *object.Object) {
	h := q.w.Header()
	h.Set("ETag", quoteETag(o.ETag))
	h.Set("Last-Modified", httpTime(o.ModTime))
	h.Set("Content-Type", o.ContentType)
	h.Set("Accept-Ranges", "bytes")
	// Set directly to keep the lower-case name S3 uses; SDKs preserve the
	// case of the suffix as the metadata key.
	for k, v := range o.UserMeta {
		h["x-amz-meta-"+strings.ToLower(k)] = []string{v}
	}
	for _, name := range storedHeaders {
		if v := o.Headers[name]; v != "" {
			h.Set(name, v)
		}
	}
	if len(o.Tags) > 0 {
		h.Set("x-amz-tagging-count", strconv.Itoa(len(o.Tags)))
	}
	if n := o.PartsCount(); n > 0 && q.query.Get("partNumber") != "" {
		h.Set("x-amz-mp-parts-count", strconv.Itoa(n))
	}
	if o.VersionID != object.NullVersion || s.versioned(q, q.bucket) {
		h.Set("x-amz-version-id", o.VersionID)
	}
	h.Set("x-amz-storage-class", "STANDARD")
	// Presigned URLs may override response headers.
	for param, header := range map[string]string{
		"response-content-type": "Content-Type", "response-content-language": "Content-Language",
		"response-expires": "Expires", "response-cache-control": "Cache-Control",
		"response-content-disposition": "Content-Disposition", "response-content-encoding": "Content-Encoding",
	} {
		if v := q.query.Get(param); v != "" {
			h.Set(header, v)
		}
	}
}

func (s *Server) getObject(q *request, head bool) {
	if !s.authorize(q, auth.ActRead, q.bucket) {
		return
	}
	ctx := q.r.Context()
	o, err := s.objects.StatObject(ctx, q.bucket, q.key, q.query.Get("versionId"))
	if err != nil {
		if errors.Is(err, object.ErrDeleteMarker) && o != nil {
			q.w.Header().Set("x-amz-delete-marker", "true")
			q.w.Header().Set("x-amz-version-id", o.VersionID)
			q.w.Header().Set("Last-Modified", httpTime(o.ModTime))
		} else if errors.Is(err, object.ErrNoSuchKey) {
			if vers, verr := s.objects.ObjectVersions(ctx, q.bucket, q.key); verr == nil && len(vers) > 0 && vers[0].DeleteMarker {
				q.w.Header().Set("x-amz-delete-marker", "true")
				q.w.Header().Set("x-amz-version-id", vers[0].VersionID)
			}
		}
		s.writeError(q, fromObjectErr(err))
		return
	}

	if st := checkConditions(q.r.Header, o.ETag, o.ModTime, ""); st != 0 {
		if st == http.StatusNotModified {
			q.w.Header().Set("ETag", quoteETag(o.ETag))
			q.w.Header().Set("Last-Modified", httpTime(o.ModTime))
			q.w.WriteHeader(http.StatusNotModified)
			return
		}
		s.writeError(q, ErrPreconditionFailed)
		return
	}

	start, length := int64(0), o.Size
	status := http.StatusOK
	if pn := q.query.Get("partNumber"); pn != "" {
		n, perr := strconv.Atoi(pn)
		if perr != nil || n < 1 || n > object.MaxPartNum {
			s.writeError(q, ErrInvalidPartNumber)
			return
		}
		if o.PartsCount() > 0 {
			if n > len(o.Parts) {
				s.writeError(q, ErrInvalidRange)
				return
			}
			for i := 0; i < n-1; i++ {
				start += o.Parts[i].Size
			}
			length = o.Parts[n-1].Size
			status = http.StatusPartialContent
		} else if n != 1 {
			s.writeError(q, ErrInvalidRange)
			return
		}
	} else if rh := q.r.Header.Get("Range"); rh != "" {
		st, ln, ok, rerr := parseRange(rh, o.Size)
		if rerr != nil {
			q.w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", o.Size))
			s.writeError(q, rerr)
			return
		}
		if ok {
			start, length, status = st, ln, http.StatusPartialContent
		}
	}

	s.setObjectHeaders(q, o)
	h := q.w.Header()
	h.Set("Content-Length", strconv.FormatInt(length, 10))
	if status == http.StatusPartialContent {
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+length-1, o.Size))
	}
	q.w.WriteHeader(status)
	if head || length == 0 {
		return
	}
	rd := s.objects.Open(o)
	defer rd.Close()
	if _, err := rd.Seek(start, io.SeekStart); err != nil {
		return
	}
	io.CopyN(q.w, rd, length)
}

func (s *Server) putObject(q *request) {
	if !s.authorize(q, auth.ActWrite, q.bucket) {
		return
	}
	ctx := q.r.Context()
	opts, e := putOptions(q.r)
	if e != nil {
		s.writeError(q, e)
		return
	}
	// Conditional writes: If-None-Match: * (create only) and If-Match (compare-and-swap).
	if v := q.r.Header.Get("If-None-Match"); v != "" || q.r.Header.Get("If-Match") != "" {
		cur, err := s.objects.StatObject(ctx, q.bucket, q.key, "")
		if err != nil && !errors.Is(err, object.ErrNoSuchKey) {
			s.writeError(q, fromObjectErr(err))
			return
		}
		if v == "*" && cur != nil {
			s.writeError(q, ErrPreconditionFailed)
			return
		}
		if m := q.r.Header.Get("If-Match"); m != "" && (cur == nil || !etagMatch(m, cur.ETag)) {
			if cur == nil {
				s.writeError(q, fromObjectErr(object.ErrNoSuchKey))
				return
			}
			s.writeError(q, ErrPreconditionFailed)
			return
		}
	}
	body, size, e := q.auth.bodyReader(q.r)
	if e != nil {
		s.writeError(q, e)
		return
	}
	opts.Size = size
	o, err := s.objects.PutObject(ctx, q.bucket, q.key, body, opts)
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	q.w.Header().Set("ETag", quoteETag(o.ETag))
	if o.VersionID != object.NullVersion {
		q.w.Header().Set("x-amz-version-id", o.VersionID)
	}
	s.ok(q)
}

// parseCopySource parses "x-amz-copy-source: /bucket/key?versionId=..."
func parseCopySource(v string) (bucket, key, version string, e *Error) {
	src, query, _ := strings.Cut(v, "?")
	src, err := url.PathUnescape(src)
	if err != nil {
		return "", "", "", ErrInvalidCopySource
	}
	src = strings.TrimPrefix(src, "/")
	bucket, key, ok := strings.Cut(src, "/")
	if !ok || bucket == "" || key == "" {
		return "", "", "", ErrInvalidCopySource
	}
	if query != "" {
		qv, _ := url.ParseQuery(query)
		version = qv.Get("versionId")
	}
	return bucket, key, version, nil
}

func (s *Server) copyObject(q *request) {
	srcBucket, srcKey, srcVersion, e := parseCopySource(q.r.Header.Get("X-Amz-Copy-Source"))
	if e != nil {
		s.writeError(q, e)
		return
	}
	if !s.authorize(q, auth.ActRead, srcBucket) || !s.authorize(q, auth.ActWrite, q.bucket) {
		return
	}
	ctx := q.r.Context()
	src, err := s.objects.StatObject(ctx, srcBucket, srcKey, srcVersion)
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	if st := checkConditions(q.r.Header, src.ETag, src.ModTime, "X-Amz-Copy-Source-"); st != 0 {
		s.writeError(q, ErrPreconditionFailed)
		return
	}
	replaceMeta := strings.EqualFold(q.r.Header.Get("X-Amz-Metadata-Directive"), "REPLACE")
	replaceTags := strings.EqualFold(q.r.Header.Get("X-Amz-Tagging-Directive"), "REPLACE")
	if srcBucket == q.bucket && srcKey == q.key && srcVersion == "" && !replaceMeta && !replaceTags {
		s.writeError(q, ErrCopyToSelf)
		return
	}
	opts := object.CopyOptions{SrcVersionID: srcVersion, ReplaceMetadata: replaceMeta, ReplaceTags: replaceTags}
	if replaceMeta || replaceTags {
		po, e := putOptions(q.r)
		if e != nil {
			s.writeError(q, e)
			return
		}
		opts.ContentType, opts.UserMeta, opts.Headers, opts.Tags = po.ContentType, po.UserMeta, po.Headers, po.Tags
	}
	o, err := s.objects.CopyObject(ctx, srcBucket, srcKey, q.bucket, q.key, opts)
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	if src.VersionID != object.NullVersion {
		q.w.Header().Set("x-amz-copy-source-version-id", src.VersionID)
	}
	if o.VersionID != object.NullVersion {
		q.w.Header().Set("x-amz-version-id", o.VersionID)
	}
	s.writeXML(q, http.StatusOK, copyObjectResult{Xmlns: xmlns, LastModified: s3Time(o.ModTime), ETag: quoteETag(o.ETag)})
}

func (s *Server) deleteObject(q *request) {
	if !s.authorize(q, auth.ActWrite, q.bucket) {
		return
	}
	res, err := s.objects.DeleteObject(q.r.Context(), q.bucket, q.key, q.query.Get("versionId"))
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	if res.DeleteMarker {
		q.w.Header().Set("x-amz-delete-marker", "true")
	}
	if res.VersionID != "" && (res.VersionID != object.NullVersion || q.query.Get("versionId") != "") {
		q.w.Header().Set("x-amz-version-id", res.VersionID)
	}
	q.w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteObjects(q *request) {
	if !s.authorize(q, auth.ActWrite, q.bucket) {
		return
	}
	var req deleteRequest
	if e := s.decodeXML(q, &req); e != nil {
		s.writeError(q, e)
		return
	}
	if len(req.Objects) > 1000 {
		s.writeError(q, ErrTooManyKeys)
		return
	}
	if _, err := s.objects.GetBucket(q.r.Context(), q.bucket); err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	res := deleteResult{Xmlns: xmlns}
	for _, obj := range req.Objects {
		r, err := s.objects.DeleteObject(q.r.Context(), q.bucket, obj.Key, obj.VersionID)
		if err != nil {
			e := fromObjectErr(err)
			res.Errors = append(res.Errors, deleteErrorEntry{Key: obj.Key, VersionID: obj.VersionID, Code: e.Code, Message: e.Message})
			continue
		}
		if req.Quiet {
			continue
		}
		d := deletedEntry{Key: obj.Key}
		if obj.VersionID != "" {
			d.VersionID = obj.VersionID
			if r.DeleteMarker {
				d.DeleteMarker, d.DeleteMarkerVersionID = true, obj.VersionID
			}
		} else if r.DeleteMarker {
			d.DeleteMarker, d.DeleteMarkerVersionID = true, r.VersionID
		}
		res.Deleted = append(res.Deleted, d)
	}
	s.writeXML(q, http.StatusOK, res)
}

func (s *Server) getObjectTagging(q *request) {
	if !s.authorize(q, auth.ActRead, q.bucket) {
		return
	}
	o, err := s.objects.StatObject(q.r.Context(), q.bucket, q.key, q.query.Get("versionId"))
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	t := tagging{Xmlns: xmlns}
	t.TagSet.Tags = []tag{}
	for k, v := range o.Tags {
		t.TagSet.Tags = append(t.TagSet.Tags, tag{Key: k, Value: v})
	}
	if o.VersionID != object.NullVersion {
		q.w.Header().Set("x-amz-version-id", o.VersionID)
	}
	s.writeXML(q, http.StatusOK, t)
}

func (s *Server) putObjectTagging(q *request) {
	if !s.authorize(q, auth.ActWrite, q.bucket) {
		return
	}
	var t tagging
	if e := s.decodeXML(q, &t); e != nil {
		s.writeError(q, e)
		return
	}
	tags := map[string]string{}
	for _, tg := range t.TagSet.Tags {
		tags[tg.Key] = tg.Value
	}
	if e := validateTags(tags); e != nil {
		s.writeError(q, e)
		return
	}
	o, err := s.objects.SetObjectTags(q.r.Context(), q.bucket, q.key, q.query.Get("versionId"), tags)
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	if o.VersionID != object.NullVersion {
		q.w.Header().Set("x-amz-version-id", o.VersionID)
	}
	s.ok(q)
}

func (s *Server) deleteObjectTagging(q *request) {
	if !s.authorize(q, auth.ActWrite, q.bucket) {
		return
	}
	o, err := s.objects.SetObjectTags(q.r.Context(), q.bucket, q.key, q.query.Get("versionId"), nil)
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	if o.VersionID != object.NullVersion {
		q.w.Header().Set("x-amz-version-id", o.VersionID)
	}
	q.w.WriteHeader(http.StatusNoContent)
}
