package s3

import (
	"encoding/base64"
	"encoding/xml"
	"io"
	"net/http"
	"strconv"

	"acs/internal/auth"
	"acs/internal/object"
)

func (s *Server) listBuckets(q *request) {
	if q.auth.anonymous {
		s.writeError(q, ErrAccessDenied)
		return
	}
	buckets, err := s.objects.ListBuckets(q.r.Context())
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	res := listAllMyBucketsResult{Xmlns: xmlns, Owner: defaultOwner}
	res.Buckets.Bucket = []bucketEntry{}
	for _, b := range buckets {
		if q.auth.principal.Can(auth.ActRead, b.Name) {
			res.Buckets.Bucket = append(res.Buckets.Bucket, bucketEntry{Name: b.Name, CreationDate: s3Time(b.CreatedAt)})
		}
	}
	s.writeXML(q, http.StatusOK, res)
}

func (s *Server) createBucket(q *request) {
	if !s.authorize(q, auth.ActManageBuckets, q.bucket) {
		return
	}
	// The optional CreateBucketConfiguration body (location constraint) is ignored.
	if body, _, e := q.auth.bodyReader(q.r); e == nil {
		io.Copy(io.Discard, io.LimitReader(body, 1<<20))
	}
	if _, err := s.objects.CreateBucket(q.r.Context(), q.bucket); err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	q.w.Header().Set("Location", "/"+q.bucket)
	s.ok(q)
}

func (s *Server) headBucket(q *request) {
	s.bucketRead(q, func() {
		q.w.Header().Set("x-amz-bucket-region", s.settings.Get().Region)
		s.ok(q)
	})
}

func (s *Server) deleteBucket(q *request) {
	s.bucketManage(q, func() {
		if err := s.objects.DeleteBucket(q.r.Context(), q.bucket, false); err != nil {
			s.writeError(q, fromObjectErr(err))
			return
		}
		q.w.WriteHeader(http.StatusNoContent)
	})
}

func (s *Server) getBucketLocation(q *request) {
	s.bucketRead(q, func() {
		region := s.settings.Get().Region
		if region == "us-east-1" {
			region = ""
		}
		s.writeXML(q, http.StatusOK, locationConstraint{Xmlns: xmlns, Value: region})
	})
}

func (s *Server) getBucketVersioning(q *request) {
	if !s.authorize(q, auth.ActRead, q.bucket) {
		return
	}
	b, err := s.objects.GetBucket(q.r.Context(), q.bucket)
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	s.writeXML(q, http.StatusOK, versioningConfiguration{Xmlns: xmlns, Status: b.Versioning})
}

func (s *Server) putBucketVersioning(q *request) {
	if !s.authorize(q, auth.ActManageBuckets, q.bucket) {
		return
	}
	var cfg versioningConfiguration
	if e := s.decodeXML(q, &cfg); e != nil {
		s.writeError(q, e)
		return
	}
	if cfg.Status != object.VersioningEnabled && cfg.Status != object.VersioningSuspended {
		s.writeError(q, ErrMalformedXML)
		return
	}
	if _, err := s.objects.UpdateBucket(q.r.Context(), q.bucket, func(b *object.Bucket) error {
		if cfg.Status == object.VersioningSuspended && b.Versioning == object.VersioningOff {
			return nil // suspending an unversioned bucket is a no-op
		}
		b.Versioning = cfg.Status
		return nil
	}); err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	s.ok(q)
}

func (s *Server) getBucketCORS(q *request) {
	if !s.authorize(q, auth.ActRead, q.bucket) {
		return
	}
	b, err := s.objects.GetBucket(q.r.Context(), q.bucket)
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	if len(b.CORS) == 0 {
		s.writeError(q, ErrNoSuchCORS)
		return
	}
	cfg := corsConfiguration{Xmlns: xmlns}
	for _, r := range b.CORS {
		cfg.Rules = append(cfg.Rules, struct {
			AllowedOrigin []string `xml:"AllowedOrigin"`
			AllowedMethod []string `xml:"AllowedMethod"`
			AllowedHeader []string `xml:"AllowedHeader"`
			ExposeHeader  []string `xml:"ExposeHeader"`
			MaxAgeSeconds int      `xml:"MaxAgeSeconds,omitempty"`
		}{r.AllowedOrigins, r.AllowedMethods, r.AllowedHeaders, r.ExposeHeaders, r.MaxAgeSeconds})
	}
	s.writeXML(q, http.StatusOK, cfg)
}

func (s *Server) putBucketCORS(q *request) {
	if !s.authorize(q, auth.ActManageBuckets, q.bucket) {
		return
	}
	var cfg corsConfiguration
	if e := s.decodeXML(q, &cfg); e != nil {
		s.writeError(q, e)
		return
	}
	var rules []object.CORSRule
	for _, r := range cfg.Rules {
		if len(r.AllowedOrigin) == 0 || len(r.AllowedMethod) == 0 {
			s.writeError(q, ErrMalformedXML)
			return
		}
		rules = append(rules, object.CORSRule{
			AllowedOrigins: r.AllowedOrigin, AllowedMethods: r.AllowedMethod, AllowedHeaders: r.AllowedHeader,
			ExposeHeaders: r.ExposeHeader, MaxAgeSeconds: r.MaxAgeSeconds,
		})
	}
	if _, err := s.objects.UpdateBucket(q.r.Context(), q.bucket, func(b *object.Bucket) error {
		b.CORS = rules
		return nil
	}); err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	s.ok(q)
}

func (s *Server) deleteBucketCORS(q *request) {
	if !s.authorize(q, auth.ActManageBuckets, q.bucket) {
		return
	}
	if _, err := s.objects.UpdateBucket(q.r.Context(), q.bucket, func(b *object.Bucket) error {
		b.CORS = nil
		return nil
	}); err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	q.w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getBucketLifecycle(q *request) {
	if !s.authorize(q, auth.ActRead, q.bucket) {
		return
	}
	b, err := s.objects.GetBucket(q.r.Context(), q.bucket)
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	if len(b.Lifecycle) == 0 {
		s.writeError(q, ErrNoSuchLifecycle)
		return
	}
	cfg := lifecycleConfiguration{Xmlns: xmlns}
	for _, r := range b.Lifecycle {
		lr := lifecycleRule{ID: r.ID, Status: "Disabled"}
		if r.Enabled {
			lr.Status = "Enabled"
		}
		lr.Filter = &struct {
			Prefix string `xml:"Prefix"`
		}{r.Prefix}
		if r.ExpirationDays > 0 {
			lr.Expiration = &struct {
				Days int `xml:"Days,omitempty"`
			}{r.ExpirationDays}
		}
		if r.NoncurrentDays > 0 {
			lr.NoncurrentVersionExpiration = &struct {
				NoncurrentDays int `xml:"NoncurrentDays,omitempty"`
			}{r.NoncurrentDays}
		}
		if r.AbortMultipartDays > 0 {
			lr.AbortIncompleteMultipartUpload = &struct {
				DaysAfterInitiation int `xml:"DaysAfterInitiation,omitempty"`
			}{r.AbortMultipartDays}
		}
		cfg.Rules = append(cfg.Rules, lr)
	}
	s.writeXML(q, http.StatusOK, cfg)
}

func (s *Server) putBucketLifecycle(q *request) {
	if !s.authorize(q, auth.ActManageBuckets, q.bucket) {
		return
	}
	var cfg lifecycleConfiguration
	if e := s.decodeXML(q, &cfg); e != nil {
		s.writeError(q, e)
		return
	}
	var rules []object.LifecycleRule
	for i, r := range cfg.Rules {
		lr := object.LifecycleRule{ID: r.ID, Enabled: r.Status == "Enabled", Prefix: r.Prefix}
		if lr.ID == "" {
			lr.ID = "rule-" + strconv.Itoa(i+1)
		}
		if r.Filter != nil {
			lr.Prefix = r.Filter.Prefix
		}
		if r.Expiration != nil {
			lr.ExpirationDays = r.Expiration.Days
		}
		if r.NoncurrentVersionExpiration != nil {
			lr.NoncurrentDays = r.NoncurrentVersionExpiration.NoncurrentDays
		}
		if r.AbortIncompleteMultipartUpload != nil {
			lr.AbortMultipartDays = r.AbortIncompleteMultipartUpload.DaysAfterInitiation
		}
		rules = append(rules, lr)
	}
	if _, err := s.objects.UpdateBucket(q.r.Context(), q.bucket, func(b *object.Bucket) error {
		b.Lifecycle = rules
		return nil
	}); err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	s.ok(q)
}

func (s *Server) deleteBucketLifecycle(q *request) {
	if !s.authorize(q, auth.ActManageBuckets, q.bucket) {
		return
	}
	if _, err := s.objects.UpdateBucket(q.r.Context(), q.bucket, func(b *object.Bucket) error {
		b.Lifecycle = nil
		return nil
	}); err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	q.w.WriteHeader(http.StatusNoContent)
}

func toContent(o *object.Object, enc string, withOwner bool) contentEntry {
	c := contentEntry{
		Key: encodeKey(o.Key, enc), LastModified: s3Time(o.ModTime), ETag: quoteETag(o.ETag),
		Size: o.Size, StorageClass: "STANDARD",
	}
	if withOwner {
		own := defaultOwner
		c.Owner = &own
	}
	return c
}

func prefixes(list []string, enc string) []commonPrefix {
	out := make([]commonPrefix, len(list))
	for i, p := range list {
		out[i] = commonPrefix{Prefix: encodeKey(p, enc)}
	}
	return out
}

func (s *Server) listObjectsV1(q *request) {
	if !s.authorize(q, auth.ActRead, q.bucket) {
		return
	}
	enc := q.query.Get("encoding-type")
	max := parseMaxKeys(q.query.Get("max-keys"))
	res := listBucketResult{
		Xmlns: xmlns, Name: q.bucket, Prefix: encodeKey(q.query.Get("prefix"), enc),
		Marker: encodeKey(q.query.Get("marker"), enc), MaxKeys: max,
		Delimiter: encodeKey(q.query.Get("delimiter"), enc), EncodingType: enc,
	}
	if max == 0 {
		if _, err := s.objects.GetBucket(q.r.Context(), q.bucket); err != nil {
			s.writeError(q, fromObjectErr(err))
			return
		}
		s.writeXML(q, http.StatusOK, res)
		return
	}
	lr, err := s.objects.List(q.r.Context(), q.bucket, object.ListOptions{
		Prefix: q.query.Get("prefix"), Delimiter: q.query.Get("delimiter"), Marker: q.query.Get("marker"), MaxKeys: max,
	})
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	res.IsTruncated = lr.IsTruncated
	if lr.IsTruncated {
		res.NextMarker = encodeKey(lr.NextMarker, enc)
	}
	for _, o := range lr.Objects {
		res.Contents = append(res.Contents, toContent(o, enc, true))
	}
	res.CommonPrefixes = prefixes(lr.CommonPrefixes, enc)
	s.writeXML(q, http.StatusOK, res)
}

func (s *Server) listObjectsV2(q *request) {
	if !s.authorize(q, auth.ActRead, q.bucket) {
		return
	}
	enc := q.query.Get("encoding-type")
	max := parseMaxKeys(q.query.Get("max-keys"))
	token := q.query.Get("continuation-token")
	startAfter := q.query.Get("start-after")
	marker := startAfter
	if token != "" {
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			s.writeError(q, newErr(http.StatusBadRequest, "InvalidArgument", "The continuation token provided is incorrect"))
			return
		}
		marker = string(raw)
	}
	res := listBucketResultV2{
		Xmlns: xmlns, Name: q.bucket, Prefix: encodeKey(q.query.Get("prefix"), enc),
		StartAfter: encodeKey(startAfter, enc), ContinuationToken: token, MaxKeys: max,
		Delimiter: encodeKey(q.query.Get("delimiter"), enc), EncodingType: enc,
	}
	if max == 0 {
		if _, err := s.objects.GetBucket(q.r.Context(), q.bucket); err != nil {
			s.writeError(q, fromObjectErr(err))
			return
		}
		s.writeXML(q, http.StatusOK, res)
		return
	}
	lr, err := s.objects.List(q.r.Context(), q.bucket, object.ListOptions{
		Prefix: q.query.Get("prefix"), Delimiter: q.query.Get("delimiter"), Marker: marker, MaxKeys: max,
	})
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	fetchOwner := q.query.Get("fetch-owner") == "true"
	for _, o := range lr.Objects {
		res.Contents = append(res.Contents, toContent(o, enc, fetchOwner))
	}
	res.CommonPrefixes = prefixes(lr.CommonPrefixes, enc)
	res.KeyCount = len(res.Contents) + len(res.CommonPrefixes)
	res.IsTruncated = lr.IsTruncated
	if lr.IsTruncated {
		res.NextContinuationToken = base64.RawURLEncoding.EncodeToString([]byte(lr.NextMarker))
	}
	s.writeXML(q, http.StatusOK, res)
}

func (s *Server) listObjectVersions(q *request) {
	if !s.authorize(q, auth.ActRead, q.bucket) {
		return
	}
	enc := q.query.Get("encoding-type")
	max := parseMaxKeys(q.query.Get("max-keys"))
	lr, err := s.objects.ListVersions(q.r.Context(), q.bucket, object.ListVersionsOptions{
		Prefix: q.query.Get("prefix"), Delimiter: q.query.Get("delimiter"),
		KeyMarker: q.query.Get("key-marker"), VersionIDMarker: q.query.Get("version-id-marker"), MaxKeys: max,
	})
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	res := listVersionsResult{
		Xmlns: xmlns, Name: q.bucket, Prefix: encodeKey(q.query.Get("prefix"), enc),
		KeyMarker: encodeKey(q.query.Get("key-marker"), enc), VersionIDMarker: q.query.Get("version-id-marker"),
		MaxKeys: max, Delimiter: encodeKey(q.query.Get("delimiter"), enc), EncodingType: enc,
		IsTruncated: lr.IsTruncated, NextKeyMarker: encodeKey(lr.NextKeyMarker, enc), NextVersionIDMarker: lr.NextVersionIDMarker,
	}
	for _, v := range lr.Versions {
		e := versionEntry{
			XMLName: xml.Name{Local: "Version"}, Key: encodeKey(v.Key, enc), VersionID: v.VersionID,
			IsLatest: v.IsLatest, LastModified: s3Time(v.ModTime), Owner: defaultOwner,
		}
		if v.DeleteMarker {
			e.XMLName.Local = "DeleteMarker"
		} else {
			size := v.Size
			e.ETag, e.Size, e.StorageClass = quoteETag(v.ETag), &size, "STANDARD"
		}
		res.Entries = append(res.Entries, e)
	}
	res.CommonPrefixes = prefixes(lr.CommonPrefixes, enc)
	s.writeXML(q, http.StatusOK, res)
}

func (s *Server) listMultipartUploads(q *request) {
	if !s.authorize(q, auth.ActRead, q.bucket) {
		return
	}
	max := parseMaxKeys(q.query.Get("max-uploads"))
	uploads, truncated, err := s.objects.ListUploads(q.r.Context(), q.bucket, q.query.Get("prefix"),
		q.query.Get("key-marker"), q.query.Get("upload-id-marker"), max)
	if err != nil {
		s.writeError(q, fromObjectErr(err))
		return
	}
	res := listMultipartUploadsResult{
		Xmlns: xmlns, Bucket: q.bucket, KeyMarker: q.query.Get("key-marker"), UploadIDMarker: q.query.Get("upload-id-marker"),
		Prefix: q.query.Get("prefix"), MaxUploads: max, IsTruncated: truncated,
	}
	for _, u := range uploads {
		res.Uploads = append(res.Uploads, uploadEntry{
			Key: u.Key, UploadID: u.ID, Initiator: defaultOwner, Owner: defaultOwner,
			StorageClass: "STANDARD", Initiated: s3Time(u.Initiated),
		})
	}
	if truncated && len(uploads) > 0 {
		last := uploads[len(uploads)-1]
		res.NextKeyMarker, res.NextUploadIDMarker = last.Key, last.ID
	}
	s.writeXML(q, http.StatusOK, res)
}
