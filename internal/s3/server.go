// Package s3 implements the S3-compatible API on top of the object service.
package s3

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"acs/internal/auth"
	"acs/internal/metrics"
	"acs/internal/object"
	"acs/internal/settings"
)

type Server struct {
	objects  *object.Service
	auth     *auth.Service
	settings *settings.Store
	metrics  *metrics.Registry
	// domain enables virtual-hosted-style addressing (bucket.domain).
	domain string
}

func New(objects *object.Service, a *auth.Service, st *settings.Store, m *metrics.Registry, domain string) *Server {
	return &Server{objects: objects, auth: a, settings: st, metrics: m, domain: strings.ToLower(domain)}
}

// request carries per-request state through the handlers.
type request struct {
	w      http.ResponseWriter
	r      *http.Request
	id     string
	bucket string
	key    string
	query  url.Values
	auth   *authResult
}

func (q *request) can(a auth.Action, bucket string) bool {
	if q.auth.anonymous {
		return false
	}
	return q.auth.principal.Can(a, bucket)
}

func requestID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b))
}

type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

// parseTarget extracts bucket and key from a path-style or virtual-hosted URL.
func (s *Server) parseTarget(r *http.Request) (bucket, key string) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if s.domain != "" {
		host := strings.ToLower(r.Host)
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if b, ok := strings.CutSuffix(host, "."+s.domain); ok && b != "" {
			return b, path
		}
	}
	bucket, key, _ = strings.Cut(path, "/")
	return bucket, key
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			s.metrics.ObserveHTTP("s3", r.Method, sw.status, sw.bytes, r.ContentLength)
		}()
		q := &request{w: sw, r: r, id: requestID(), query: r.URL.Query()}
		q.bucket, q.key = s.parseTarget(r)
		h := sw.Header()
		h.Set("x-amz-request-id", q.id)
		h.Set("x-amz-id-2", q.id)
		h.Set("Server", "ACS")

		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				slog.Error("panic in S3 handler", "panic", v, "path", r.URL.Path)
				s.writeError(q, newErr(http.StatusInternalServerError, "InternalError", "We encountered an internal error."))
			}
		}()

		if r.Method == http.MethodOptions {
			s.handlePreflight(q)
			return
		}
		s.applyCORS(q)

		a, err := s.authenticate(r)
		if err != nil {
			s.writeError(q, err)
			return
		}
		q.auth = a
		s.route(q)
	})
}

func (s *Server) route(q *request) {
	m := q.r.Method
	has := func(k string) bool { _, ok := q.query[k]; return ok }

	if q.bucket == "" {
		if m == http.MethodGet {
			s.listBuckets(q)
			return
		}
		s.writeError(q, ErrMethodNotAllowed)
		return
	}

	if q.key == "" {
		switch m {
		case http.MethodGet:
			switch {
			case has("location"):
				s.getBucketLocation(q)
			case has("versioning"):
				s.getBucketVersioning(q)
			case has("versions"):
				s.listObjectVersions(q)
			case has("uploads"):
				s.listMultipartUploads(q)
			case has("cors"):
				s.getBucketCORS(q)
			case has("lifecycle"):
				s.getBucketLifecycle(q)
			case has("policy"):
				s.bucketRead(q, func() { s.writeError(q, ErrNoSuchBucketPolicy) })
			case has("acl"):
				s.bucketRead(q, func() { s.writeXML(q, http.StatusOK, fullControlACL()) })
			case has("tagging"):
				s.bucketRead(q, func() { s.writeError(q, ErrNoSuchTagSet) })
			case has("object-lock"):
				s.bucketRead(q, func() {
					s.writeError(q, newErr(http.StatusNotFound, "ObjectLockConfigurationNotFoundError", "Object Lock configuration does not exist for this bucket"))
				})
			case has("encryption"):
				s.bucketRead(q, func() {
					s.writeError(q, newErr(http.StatusNotFound, "ServerSideEncryptionConfigurationNotFoundError", "The server side encryption configuration was not found"))
				})
			case has("policyStatus"):
				s.bucketRead(q, func() {
					type policyStatus struct {
						XMLName  xml.Name `xml:"PolicyStatus"`
						IsPublic bool     `xml:"IsPublic"`
					}
					b, _ := s.objects.GetBucket(q.r.Context(), q.bucket)
					s.writeXML(q, http.StatusOK, policyStatus{IsPublic: b != nil && b.Public})
				})
			case q.query.Get("list-type") == "2":
				s.listObjectsV2(q)
			case has("website"), has("logging"), has("notification"), has("replication"),
				has("accelerate"), has("requestPayment"), has("ownershipControls"), has("intelligent-tiering"),
				has("analytics"), has("metrics"), has("inventory"):
				s.writeError(q, ErrNotImplemented)
			default:
				s.listObjectsV1(q)
			}
		case http.MethodHead:
			s.headBucket(q)
		case http.MethodPut:
			switch {
			case has("versioning"):
				s.putBucketVersioning(q)
			case has("cors"):
				s.putBucketCORS(q)
			case has("lifecycle"):
				s.putBucketLifecycle(q)
			case has("acl"):
				s.bucketManage(q, func() { s.ok(q) })
			case has("policy"), has("tagging"), has("encryption"), has("website"), has("logging"),
				has("notification"), has("replication"), has("object-lock"), has("ownershipControls"):
				s.writeError(q, ErrNotImplemented)
			default:
				s.createBucket(q)
			}
		case http.MethodDelete:
			switch {
			case has("cors"):
				s.deleteBucketCORS(q)
			case has("lifecycle"):
				s.deleteBucketLifecycle(q)
			case has("policy"), has("tagging"), has("encryption"), has("website"):
				s.bucketManage(q, func() { q.w.WriteHeader(http.StatusNoContent) })
			default:
				s.deleteBucket(q)
			}
		case http.MethodPost:
			if has("delete") {
				s.deleteObjects(q)
				return
			}
			s.writeError(q, ErrNotImplemented)
		default:
			s.writeError(q, ErrMethodNotAllowed)
		}
		return
	}

	switch m {
	case http.MethodGet:
		switch {
		case has("uploadId"):
			s.listParts(q)
		case has("tagging"):
			s.getObjectTagging(q)
		case has("acl"):
			s.objectRead(q, func() { s.writeXML(q, http.StatusOK, fullControlACL()) })
		case has("attributes"), has("torrent"), has("legal-hold"), has("retention"):
			s.writeError(q, ErrNotImplemented)
		default:
			s.getObject(q, false)
		}
	case http.MethodHead:
		s.getObject(q, true)
	case http.MethodPut:
		switch {
		case has("uploadId") && has("partNumber"):
			if q.r.Header.Get("X-Amz-Copy-Source") != "" {
				s.uploadPartCopy(q)
			} else {
				s.uploadPart(q)
			}
		case has("tagging"):
			s.putObjectTagging(q)
		case has("acl"):
			s.objectWrite(q, func() { s.ok(q) })
		case has("retention"), has("legal-hold"):
			s.writeError(q, ErrNotImplemented)
		case q.r.Header.Get("X-Amz-Copy-Source") != "":
			s.copyObject(q)
		default:
			s.putObject(q)
		}
	case http.MethodDelete:
		switch {
		case has("uploadId"):
			s.abortMultipartUpload(q)
		case has("tagging"):
			s.deleteObjectTagging(q)
		default:
			s.deleteObject(q)
		}
	case http.MethodPost:
		switch {
		case has("uploads"):
			s.createMultipartUpload(q)
		case has("uploadId"):
			s.completeMultipartUpload(q)
		default:
			s.writeError(q, ErrNotImplemented)
		}
	default:
		s.writeError(q, ErrMethodNotAllowed)
	}
}

// ---- authorization helpers ----

// authorize checks the action and writes AccessDenied if not allowed.
// Anonymous reads are allowed on public buckets.
func (s *Server) authorize(q *request, a auth.Action, bucket string) bool {
	if q.can(a, bucket) {
		return true
	}
	if q.auth.anonymous && a == auth.ActRead {
		if b, err := s.objects.GetBucket(q.r.Context(), bucket); err == nil && b.Public {
			return true
		}
	}
	s.writeError(q, ErrAccessDenied)
	return false
}

func (s *Server) bucketRead(q *request, fn func()) {
	if !s.authorize(q, auth.ActRead, q.bucket) {
		return
	}
	if _, err := s.objects.GetBucket(q.r.Context(), q.bucket); err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	fn()
}

func (s *Server) bucketManage(q *request, fn func()) {
	if !s.authorize(q, auth.ActManageBuckets, q.bucket) {
		return
	}
	if _, err := s.objects.GetBucket(q.r.Context(), q.bucket); err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	fn()
}

func (s *Server) objectRead(q *request, fn func()) {
	if !s.authorize(q, auth.ActRead, q.bucket) {
		return
	}
	if _, err := s.objects.StatObject(q.r.Context(), q.bucket, q.key, q.query.Get("versionId")); err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	fn()
}

func (s *Server) objectWrite(q *request, fn func()) {
	if !s.authorize(q, auth.ActWrite, q.bucket) {
		return
	}
	if _, err := s.objects.StatObject(q.r.Context(), q.bucket, q.key, q.query.Get("versionId")); err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	fn()
}

// ---- response helpers ----

func (s *Server) writeXML(q *request, status int, v any) {
	body, err := xml.Marshal(v)
	if err != nil {
		s.writeError(q, newErr(http.StatusInternalServerError, "InternalError", "Failed to encode response."))
		return
	}
	q.w.Header().Set("Content-Type", "application/xml")
	q.w.Header().Set("Content-Length", strconv.Itoa(len(xml.Header)+len(body)))
	q.w.WriteHeader(status)
	io.WriteString(q.w, xml.Header)
	q.w.Write(body)
}

func (s *Server) ok(q *request) { q.w.WriteHeader(http.StatusOK) }

func (s *Server) writeError(q *request, e *Error) {
	if e.Status >= 500 {
		slog.Error("s3 error", "code", e.Code, "method", q.r.Method, "path", q.r.URL.Path, "msg", e.Message)
	}
	if q.r.Method == http.MethodHead {
		q.w.WriteHeader(e.Status)
		return
	}
	resource := q.r.URL.Path
	body, _ := xml.Marshal(errorResponse{
		Code: e.Code, Message: e.Message, BucketName: q.bucket, Key: q.key, Resource: resource, RequestID: q.id,
	})
	q.w.Header().Set("Content-Type", "application/xml")
	q.w.WriteHeader(e.Status)
	io.WriteString(q.w, xml.Header)
	q.w.Write(body)
}

// decodeXML reads a bounded XML request body.
func (s *Server) decodeXML(q *request, v any) *Error {
	body, _, aerr := q.auth.bodyReader(q.r)
	if aerr != nil {
		return aerr
	}
	data, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return fromObjectErr(err)
	}
	if err := xml.Unmarshal(data, v); err != nil {
		return ErrMalformedXML
	}
	return nil
}

// ---- CORS ----

func matchOrigin(patterns []string, origin string) bool {
	for _, p := range patterns {
		if p == "*" || p == origin {
			return true
		}
		if pre, suf, ok := strings.Cut(p, "*"); ok && strings.HasPrefix(origin, pre) && strings.HasSuffix(origin, suf) {
			return true
		}
	}
	return false
}

func matchHeaders(allowed []string, requested string) bool {
	for _, h := range strings.Split(requested, ",") {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		ok := false
		for _, a := range allowed {
			a = strings.ToLower(a)
			if a == "*" || a == h || (strings.HasSuffix(a, "*") && strings.HasPrefix(h, strings.TrimSuffix(a, "*"))) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func (s *Server) corsRule(q *request, method, reqHeaders string) *object.CORSRule {
	origin := q.r.Header.Get("Origin")
	if origin == "" || q.bucket == "" {
		return nil
	}
	b, err := s.objects.GetBucket(q.r.Context(), q.bucket)
	if err != nil {
		return nil
	}
	for i := range b.CORS {
		rule := &b.CORS[i]
		if matchOrigin(rule.AllowedOrigins, origin) && slices.Contains(rule.AllowedMethods, method) &&
			matchHeaders(rule.AllowedHeaders, reqHeaders) {
			return rule
		}
	}
	return nil
}

func setCORSHeaders(h http.Header, rule *object.CORSRule, origin string) {
	if slices.Contains(rule.AllowedOrigins, "*") {
		h.Set("Access-Control-Allow-Origin", "*")
	} else {
		h.Set("Access-Control-Allow-Origin", origin)
		h.Add("Vary", "Origin")
	}
	if len(rule.ExposeHeaders) > 0 {
		h.Set("Access-Control-Expose-Headers", strings.Join(rule.ExposeHeaders, ", "))
	}
}

func (s *Server) applyCORS(q *request) {
	if rule := s.corsRule(q, q.r.Method, ""); rule != nil {
		setCORSHeaders(q.w.Header(), rule, q.r.Header.Get("Origin"))
	}
}

func (s *Server) handlePreflight(q *request) {
	method := q.r.Header.Get("Access-Control-Request-Method")
	reqHeaders := q.r.Header.Get("Access-Control-Request-Headers")
	rule := s.corsRule(q, method, reqHeaders)
	if rule == nil {
		s.writeError(q, ErrCORSForbidden)
		return
	}
	h := q.w.Header()
	setCORSHeaders(h, rule, q.r.Header.Get("Origin"))
	h.Set("Access-Control-Allow-Methods", strings.Join(rule.AllowedMethods, ", "))
	if reqHeaders != "" {
		h.Set("Access-Control-Allow-Headers", reqHeaders)
	}
	if rule.MaxAgeSeconds > 0 {
		h.Set("Access-Control-Max-Age", strconv.Itoa(rule.MaxAgeSeconds))
	}
	q.w.WriteHeader(http.StatusOK)
}

// ---- small helpers ----

func quoteETag(etag string) string { return `"` + etag + `"` }

func httpTime(t time.Time) string { return t.UTC().Format(http.TimeFormat) }

func encodeKey(s, encoding string) string {
	if encoding == "url" {
		return url.QueryEscape(s)
	}
	return s
}

func parseMaxKeys(v string) int {
	if v == "" {
		return 1000
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 1000
	}
	return min(n, 1000)
}
