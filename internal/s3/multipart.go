package s3

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"acs/internal/auth"
	"acs/internal/object"
)

func (s *Server) createMultipartUpload(q *request) {
	if !s.authorize(q, auth.ActWrite, q.bucket) {
		return
	}
	opts, e := putOptions(q.r)
	if e != nil {
		s.writeError(q, e)
		return
	}
	u, err := s.objects.CreateUpload(q.r.Context(), q.bucket, q.key, opts)
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	if alg := q.r.Header.Get("X-Amz-Checksum-Algorithm"); alg != "" {
		q.w.Header().Set("x-amz-checksum-algorithm", alg)
	}
	s.writeXML(q, http.StatusOK, initiateMultipartUploadResult{Xmlns: xmlns, Bucket: q.bucket, Key: q.key, UploadID: u.ID})
}

func partNumber(q *request) (int, *Error) {
	n, err := strconv.Atoi(q.query.Get("partNumber"))
	if err != nil || n < 1 || n > object.MaxPartNum {
		return 0, ErrInvalidPartNumber
	}
	return n, nil
}

func (s *Server) uploadPart(q *request) {
	if !s.authorize(q, auth.ActWrite, q.bucket) {
		return
	}
	n, e := partNumber(q)
	if e != nil {
		s.writeError(q, e)
		return
	}
	opts, e := putOptions(q.r)
	if e != nil {
		s.writeError(q, e)
		return
	}
	body, size, e := q.auth.bodyReader(q.r)
	if e != nil {
		s.writeError(q, e)
		return
	}
	p, err := s.objects.UploadPart(q.r.Context(), q.bucket, q.key, q.query.Get("uploadId"), n, body, size, opts.ContentMD5)
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	q.w.Header().Set("ETag", quoteETag(p.ETag))
	s.ok(q)
}

func (s *Server) uploadPartCopy(q *request) {
	srcBucket, srcKey, srcVersion, e := parseCopySource(q.r.Header.Get("X-Amz-Copy-Source"))
	if e != nil {
		s.writeError(q, e)
		return
	}
	if !s.authorize(q, auth.ActRead, srcBucket) || !s.authorize(q, auth.ActWrite, q.bucket) {
		return
	}
	n, e := partNumber(q)
	if e != nil {
		s.writeError(q, e)
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
	start, length := int64(0), src.Size
	if rg := q.r.Header.Get("X-Amz-Copy-Source-Range"); rg != "" {
		st, ln, ok, rerr := parseRange(rg, src.Size)
		if rerr != nil || !ok || strings.HasPrefix(strings.TrimPrefix(rg, "bytes="), "-") {
			s.writeError(q, newErr(http.StatusBadRequest, "InvalidArgument", "The x-amz-copy-source-range value must be of the form bytes=first-last."))
			return
		}
		start, length = st, ln
	}
	rd := s.objects.Open(src)
	defer rd.Close()
	if _, err := rd.Seek(start, io.SeekStart); err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	p, err := s.objects.UploadPart(ctx, q.bucket, q.key, q.query.Get("uploadId"), n, io.LimitReader(rd, length), length, nil)
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	if src.VersionID != object.NullVersion {
		q.w.Header().Set("x-amz-copy-source-version-id", src.VersionID)
	}
	s.writeXML(q, http.StatusOK, copyPartResult{Xmlns: xmlns, LastModified: s3Time(p.ModTime), ETag: quoteETag(p.ETag)})
}

func (s *Server) completeMultipartUpload(q *request) {
	if !s.authorize(q, auth.ActWrite, q.bucket) {
		return
	}
	var req completeMultipartUpload
	if e := s.decodeXML(q, &req); e != nil {
		s.writeError(q, e)
		return
	}
	parts := make([]object.CompletePart, len(req.Parts))
	for i, p := range req.Parts {
		parts[i] = object.CompletePart{Number: p.PartNumber, ETag: p.ETag}
	}
	o, err := s.objects.CompleteUpload(q.r.Context(), q.bucket, q.key, q.query.Get("uploadId"), parts)
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	if o.VersionID != object.NullVersion {
		q.w.Header().Set("x-amz-version-id", o.VersionID)
	}
	scheme := "http"
	if q.r.TLS != nil {
		scheme = "https"
	}
	s.writeXML(q, http.StatusOK, completeMultipartUploadResult{
		Xmlns: xmlns, Location: fmt.Sprintf("%s://%s/%s/%s", scheme, q.r.Host, q.bucket, q.key),
		Bucket: q.bucket, Key: q.key, ETag: quoteETag(o.ETag),
	})
}

func (s *Server) abortMultipartUpload(q *request) {
	if !s.authorize(q, auth.ActWrite, q.bucket) {
		return
	}
	if err := s.objects.AbortUpload(q.r.Context(), q.bucket, q.key, q.query.Get("uploadId")); err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	q.w.WriteHeader(http.StatusNoContent)
}

func (s *Server) listParts(q *request) {
	if !s.authorize(q, auth.ActRead, q.bucket) {
		return
	}
	marker, _ := strconv.Atoi(q.query.Get("part-number-marker"))
	max := parseMaxKeys(q.query.Get("max-parts"))
	parts, truncated, err := s.objects.ListParts(q.r.Context(), q.bucket, q.key, q.query.Get("uploadId"), marker, max)
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	res := listPartsResult{
		Xmlns: xmlns, Bucket: q.bucket, Key: q.key, UploadID: q.query.Get("uploadId"),
		Initiator: defaultOwner, Owner: defaultOwner, StorageClass: "STANDARD",
		PartNumberMarker: marker, MaxParts: max, IsTruncated: truncated,
	}
	for _, p := range parts {
		res.Parts = append(res.Parts, partEntry{PartNumber: p.Number, LastModified: s3Time(p.ModTime), ETag: quoteETag(p.ETag), Size: p.Size})
	}
	if len(parts) > 0 {
		res.NextPartNumberMarker = parts[len(parts)-1].Number
	}
	s.writeXML(q, http.StatusOK, res)
}
