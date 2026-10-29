// Package remote connects ACS to an external S3-compatible provider (AWS,
// ArvanCloud, ParsPack, MinIO, ...). A Backend works inside one existing
// bucket under an optional prefix, because many providers only grant access
// to a pre-created bucket. It serves two roles:
//
//   - Store: a blob.Store for store-mode buckets, keeping opaque blobs under
//     "<prefix>.acs/blobs/".
//   - View: an object.Remote for mirror buckets, mapping ACS keys 1:1 to
//     "<prefix><bucket prefix><key>".
package remote

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/tags"

	"acs/internal/blob"
	"acs/internal/object"
)

// internalDir holds ACS's own data inside the backend prefix; mirror views
// hide it from listings.
const internalDir = ".acs/"

// partSize bounds memory per streaming upload and allows blobs up to ~160 GB.
const partSize = 16 << 20

type Config struct {
	// Endpoint is the provider URL, e.g. https://s3.ir-thr-at1.arvanstorage.ir.
	Endpoint string
	Region   string
	Bucket   string
	// Prefix scopes everything ACS writes inside Bucket ("" = bucket root).
	Prefix    string
	AccessKey string
	SecretKey string
	// PathStyle forces path-style requests (needed by most non-AWS providers).
	PathStyle bool
	// InsecureTLS skips certificate verification (self-signed test setups).
	InsecureTLS bool
	// TempDir spools part uploads of unknown length.
	TempDir string
}

type Backend struct {
	core   *minio.Core
	bucket string
	prefix string
	tmp    string
	// id distinguishes providers for server-side copies.
	id string
}

// New validates cfg and builds a client. It does not contact the provider.
func New(cfg Config) (*Backend, error) {
	u, err := url.Parse(strings.TrimSpace(cfg.Endpoint))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("endpoint must be an http(s) URL, e.g. https://s3.example.com")
	}
	if cfg.Bucket == "" {
		return nil, errors.New("bucket is required")
	}
	lookup := minio.BucketLookupAuto
	if cfg.PathStyle {
		lookup = minio.BucketLookupPath
	}
	transport, err := minio.DefaultTransport(u.Scheme == "https")
	if err != nil {
		return nil, err
	}
	if cfg.InsecureTLS {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	region := cfg.Region
	if region == "" {
		// Many providers ignore the region; a fixed one avoids an extra
		// GetBucketLocation round trip that some of them reject.
		region = "us-east-1"
	}
	core, err := minio.NewCore(u.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:       u.Scheme == "https",
		Region:       region,
		BucketLookup: lookup,
		Transport:    transport,
	})
	if err != nil {
		return nil, err
	}
	prefix := strings.TrimLeft(cfg.Prefix, "/")
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	tmp := cfg.TempDir
	if tmp == "" {
		tmp = os.TempDir()
	}
	return &Backend{core: core, bucket: cfg.Bucket, prefix: prefix, tmp: tmp, id: u.Host + "/" + cfg.Bucket}, nil
}

// Check verifies credentials and permissions by writing, reading and deleting
// a small probe object (HeadBucket is often denied on shared providers).
func (b *Backend) Check(ctx context.Context) error {
	key := b.prefix + internalDir + "health-" + blob.NewID()
	body := []byte("acs health check")
	if _, err := b.core.Client.PutObject(ctx, b.bucket, key, bytes.NewReader(body), int64(len(body)),
		minio.PutObjectOptions{ContentType: "text/plain"}); err != nil {
		return describe(err, "write")
	}
	obj, err := b.core.Client.GetObject(ctx, b.bucket, key, minio.GetObjectOptions{})
	if err == nil {
		_, err = io.ReadAll(obj)
		obj.Close()
	}
	if err != nil {
		b.core.Client.RemoveObject(ctx, b.bucket, key, minio.RemoveObjectOptions{})
		return describe(err, "read")
	}
	if err := b.core.Client.RemoveObject(ctx, b.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return describe(err, "delete")
	}
	return nil
}

// describe turns a provider error into a readable message for the panel.
func describe(err error, op string) error {
	resp := minio.ToErrorResponse(err)
	if resp.Code != "" {
		return fmt.Errorf("%s failed: %s (%s)", op, resp.Message, resp.Code)
	}
	return fmt.Errorf("%s failed: %v", op, err)
}

// mapErr translates provider errors to object package errors.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	resp := minio.ToErrorResponse(err)
	switch resp.Code {
	case "NoSuchKey", "NotFound":
		return object.ErrNoSuchKey
	case "NoSuchUpload":
		return object.ErrNoSuchUpload
	case "InvalidPart":
		return object.ErrInvalidPart
	case "InvalidPartOrder":
		return object.ErrInvalidPartOrder
	case "EntityTooSmall":
		return object.ErrEntityTooSmall
	case "BadDigest":
		return object.ErrBadDigest
	}
	if resp.StatusCode == http.StatusNotFound && resp.Code == "" {
		return object.ErrNoSuchKey
	}
	if resp.Code != "" {
		return fmt.Errorf("%w: %s: %s", object.ErrBackend, resp.Code, resp.Message)
	}
	return fmt.Errorf("%w: %v", object.ErrBackend, err)
}

// ---- blob store ----

// Store returns the backend's blob store.
func (b *Backend) Store() blob.Store { return blobStore{b} }

type blobStore struct{ b *Backend }

func (s blobStore) key(id string) string {
	return s.b.prefix + internalDir + "blobs/" + id[0:2] + "/" + id[2:4] + "/" + id
}

func (s blobStore) Put(ctx context.Context, r io.Reader) (string, int64, []byte, error) {
	id := blob.NewID()
	h := md5.New()
	info, err := s.b.core.Client.PutObject(ctx, s.b.bucket, s.key(id), io.TeeReader(r, h), -1,
		minio.PutObjectOptions{ContentType: "application/octet-stream", PartSize: partSize})
	if err != nil {
		return "", 0, nil, mapErr(err)
	}
	return id, info.Size, h.Sum(nil), nil
}

func (s blobStore) Open(ctx context.Context, id string, offset int64) (io.ReadCloser, error) {
	if !blob.ValidID(id) {
		return nil, blob.ErrNotFound
	}
	opts := minio.GetObjectOptions{}
	if offset > 0 {
		if err := opts.SetRange(offset, 0); err != nil {
			return nil, err
		}
	}
	rc, _, _, err := s.b.core.GetObject(ctx, s.b.bucket, s.key(id), opts)
	if err != nil {
		if errors.Is(mapErr(err), object.ErrNoSuchKey) {
			return nil, blob.ErrNotFound
		}
		return nil, mapErr(err)
	}
	return rc, nil
}

func (s blobStore) Delete(ctx context.Context, id string) error {
	if !blob.ValidID(id) {
		return fmt.Errorf("invalid blob id %q", id)
	}
	return mapErr(s.b.core.Client.RemoveObject(ctx, s.b.bucket, s.key(id), minio.RemoveObjectOptions{}))
}

func (s blobStore) Walk(ctx context.Context, fn func(id string, modTime time.Time, size int64) error) error {
	for o := range s.b.core.Client.ListObjects(ctx, s.b.bucket, minio.ListObjectsOptions{
		Prefix: s.b.prefix + internalDir + "blobs/", Recursive: true,
	}) {
		if o.Err != nil {
			return mapErr(o.Err)
		}
		id := path.Base(o.Key)
		if !blob.ValidID(id) {
			continue
		}
		if err := fn(id, o.LastModified, o.Size); err != nil {
			return err
		}
	}
	return nil
}

// ---- mirror view ----

// View returns a mirror view rooted at sub (relative to the backend prefix).
func (b *Backend) View(sub string) object.Remote {
	return &View{b: b, prefix: b.prefix + sub}
}

type View struct {
	b      *Backend
	prefix string
}

func (v *View) full(key string) string { return v.prefix + key }

// hidden reports whether a full key belongs to ACS's internal area.
func (v *View) hidden(full string) bool { return strings.HasPrefix(full, v.b.prefix+internalDir) }

var storedHeaders = []string{"Cache-Control", "Content-Disposition", "Content-Encoding", "Content-Language", "Expires"}

func (v *View) toRemote(key string, info minio.ObjectInfo) *object.RemoteObject {
	ro := &object.RemoteObject{
		Key: key, Size: info.Size, ETag: strings.Trim(info.ETag, `"`),
		ContentType: info.ContentType, ModTime: info.LastModified.UTC(),
	}
	for k, val := range info.UserMetadata {
		k = strings.ToLower(strings.TrimPrefix(strings.ToLower(k), "x-amz-meta-"))
		if ro.UserMeta == nil {
			ro.UserMeta = map[string]string{}
		}
		ro.UserMeta[k] = val
	}
	for _, h := range storedHeaders {
		if val := info.Metadata.Get(h); val != "" {
			if ro.Headers == nil {
				ro.Headers = map[string]string{}
			}
			ro.Headers[h] = val
		}
	}
	return ro
}

func (v *View) Stat(ctx context.Context, key string) (*object.RemoteObject, error) {
	if v.hidden(v.full(key)) {
		return nil, object.ErrNoSuchKey
	}
	info, err := v.b.core.Client.StatObject(ctx, v.b.bucket, v.full(key), minio.StatObjectOptions{})
	if err != nil {
		return nil, mapErr(err)
	}
	return v.toRemote(key, info), nil
}

func (v *View) Get(ctx context.Context, key string, offset int64) (io.ReadCloser, error) {
	opts := minio.GetObjectOptions{}
	if offset > 0 {
		if err := opts.SetRange(offset, 0); err != nil {
			return nil, err
		}
	}
	rc, _, _, err := v.b.core.GetObject(ctx, v.b.bucket, v.full(key), opts)
	if err != nil {
		return nil, mapErr(err)
	}
	return rc, nil
}

func putOptions(meta object.RemoteMeta) minio.PutObjectOptions {
	o := minio.PutObjectOptions{
		ContentType:  meta.ContentType,
		UserMetadata: meta.UserMeta,
		UserTags:     meta.Tags,
		PartSize:     partSize,
	}
	if o.ContentType == "" {
		o.ContentType = "application/octet-stream"
	}
	h := meta.Headers
	o.CacheControl, o.ContentDisposition = h["Cache-Control"], h["Content-Disposition"]
	o.ContentEncoding, o.ContentLanguage = h["Content-Encoding"], h["Content-Language"]
	if e := h["Expires"]; e != "" {
		if t, err := http.ParseTime(e); err == nil {
			o.Expires = t
		}
	}
	return o
}

func (v *View) Put(ctx context.Context, key string, r io.Reader, size int64, sum []byte, meta object.RemoteMeta) (*object.RemoteObject, error) {
	if v.hidden(v.full(key)) {
		return nil, object.ErrInvalidKey
	}
	h := md5.New()
	info, err := v.b.core.Client.PutObject(ctx, v.b.bucket, v.full(key), io.TeeReader(r, h), size, putOptions(meta))
	if err != nil {
		return nil, mapErr(err)
	}
	got := h.Sum(nil)
	if sum != nil && !bytes.Equal(sum, got) {
		v.b.core.Client.RemoveObject(ctx, v.b.bucket, v.full(key), minio.RemoveObjectOptions{})
		return nil, object.ErrBadDigest
	}
	etag := strings.Trim(info.ETag, `"`)
	if etag == "" {
		etag = hex.EncodeToString(got)
	}
	mod := info.LastModified
	if mod.IsZero() {
		mod = time.Now()
	}
	return &object.RemoteObject{
		Key: key, Size: info.Size, ETag: etag, ContentType: putOptions(meta).ContentType,
		ModTime: mod.UTC(), UserMeta: meta.UserMeta, Headers: meta.Headers,
	}, nil
}

func (v *View) Delete(ctx context.Context, key string) error {
	if v.hidden(v.full(key)) {
		return nil
	}
	return mapErr(v.b.core.Client.RemoveObject(ctx, v.b.bucket, v.full(key), minio.RemoveObjectOptions{}))
}

func (v *View) Copy(ctx context.Context, src object.Remote, srcKey, dstKey string, meta *object.RemoteMeta) (*object.RemoteObject, error) {
	sv, same := src.(*View)
	if !same || sv.b.id != v.b.id {
		// Different providers: stream through ACS.
		ro, err := src.Stat(ctx, srcKey)
		if err != nil {
			return nil, err
		}
		m := object.RemoteMeta{ContentType: ro.ContentType, UserMeta: ro.UserMeta, Headers: ro.Headers}
		if meta != nil {
			m = *meta
		}
		rc, err := src.Get(ctx, srcKey, 0)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return v.Put(ctx, dstKey, rc, ro.Size, nil, m)
	}

	dst := minio.CopyDestOptions{Bucket: v.b.bucket, Object: v.full(dstKey)}
	if meta != nil {
		um := map[string]string{}
		for k, val := range meta.UserMeta {
			um[k] = val
		}
		// Standard headers in UserMetadata are sent as real headers.
		if meta.ContentType != "" {
			um["Content-Type"] = meta.ContentType
		}
		for k, val := range meta.Headers {
			um[k] = val
		}
		dst.UserMetadata, dst.ReplaceMetadata = um, true
		if meta.Tags != nil {
			dst.UserTags, dst.ReplaceTags = meta.Tags, true
		}
	}
	if _, err := v.b.core.Client.CopyObject(ctx, dst, minio.CopySrcOptions{Bucket: sv.b.bucket, Object: sv.full(srcKey)}); err != nil {
		return nil, mapErr(err)
	}
	return v.Stat(ctx, dstKey)
}

func (v *View) List(ctx context.Context, prefix, delimiter, startAfter string, max int) ([]object.RemoteObject, []string, bool, error) {
	after := ""
	if startAfter != "" {
		after = v.full(startAfter)
	}
	res, err := v.b.core.ListObjectsV2(v.b.bucket, v.full(prefix), after, "", delimiter, max)
	if err != nil {
		return nil, nil, false, mapErr(err)
	}
	var objs []object.RemoteObject
	for _, o := range res.Contents {
		if v.hidden(o.Key) || !strings.HasPrefix(o.Key, v.prefix) {
			continue
		}
		key := strings.TrimPrefix(o.Key, v.prefix)
		objs = append(objs, object.RemoteObject{
			Key: key, Size: o.Size, ETag: strings.Trim(o.ETag, `"`), ModTime: o.LastModified.UTC(),
		})
	}
	var prefixes []string
	for _, p := range res.CommonPrefixes {
		if v.hidden(p.Prefix) || !strings.HasPrefix(p.Prefix, v.prefix) {
			continue
		}
		prefixes = append(prefixes, strings.TrimPrefix(p.Prefix, v.prefix))
	}
	return objs, prefixes, res.IsTruncated, nil
}

func (v *View) GetTags(ctx context.Context, key string) (map[string]string, error) {
	t, err := v.b.core.Client.GetObjectTagging(ctx, v.b.bucket, v.full(key), minio.GetObjectTaggingOptions{})
	if err != nil {
		return nil, mapErr(err)
	}
	return t.ToMap(), nil
}

func (v *View) PutTags(ctx context.Context, key string, m map[string]string) error {
	if len(m) == 0 {
		return mapErr(v.b.core.Client.RemoveObjectTagging(ctx, v.b.bucket, v.full(key), minio.RemoveObjectTaggingOptions{}))
	}
	t, err := tags.NewTags(m, true)
	if err != nil {
		return fmt.Errorf("%w: %v", object.ErrInvalidArgument, err)
	}
	return mapErr(v.b.core.Client.PutObjectTagging(ctx, v.b.bucket, v.full(key), t, minio.PutObjectTaggingOptions{}))
}

func (v *View) CreateUpload(ctx context.Context, key string, meta object.RemoteMeta) (string, error) {
	if v.hidden(v.full(key)) {
		return "", object.ErrInvalidKey
	}
	id, err := v.b.core.NewMultipartUpload(ctx, v.b.bucket, v.full(key), putOptions(meta))
	return id, mapErr(err)
}

func (v *View) UploadPart(ctx context.Context, key, uploadID string, num int, r io.Reader, size int64) (*object.Part, error) {
	if size < 0 {
		// S3 needs a length for each part; spool to disk to learn it.
		f, err := os.CreateTemp(v.b.tmp, "part-*")
		if err != nil {
			return nil, err
		}
		defer os.Remove(f.Name())
		defer f.Close()
		if size, err = io.Copy(f, r); err != nil {
			return nil, err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		r = f
	}
	p, err := v.b.core.PutObjectPart(ctx, v.b.bucket, v.full(key), uploadID, num, r, size, minio.PutObjectPartOptions{})
	if err != nil {
		return nil, mapErr(err)
	}
	return &object.Part{Number: num, Size: size, ETag: strings.Trim(p.ETag, `"`), ModTime: time.Now().UTC()}, nil
}

func (v *View) CompleteUpload(ctx context.Context, key, uploadID string, parts []object.CompletePart) (*object.RemoteObject, error) {
	cp := make([]minio.CompletePart, len(parts))
	for i, p := range parts {
		cp[i] = minio.CompletePart{PartNumber: p.Number, ETag: strings.Trim(p.ETag, `"`)}
	}
	if _, err := v.b.core.CompleteMultipartUpload(ctx, v.b.bucket, v.full(key), uploadID, cp, minio.PutObjectOptions{}); err != nil {
		return nil, mapErr(err)
	}
	return v.Stat(ctx, key)
}

func (v *View) AbortUpload(ctx context.Context, key, uploadID string) error {
	return mapErr(v.b.core.AbortMultipartUpload(ctx, v.b.bucket, v.full(key), uploadID))
}

func (v *View) ListParts(ctx context.Context, key, uploadID string, marker, max int) ([]*object.Part, bool, error) {
	res, err := v.b.core.ListObjectParts(ctx, v.b.bucket, v.full(key), uploadID, marker, max)
	if err != nil {
		return nil, false, mapErr(err)
	}
	var out []*object.Part
	for _, p := range res.ObjectParts {
		out = append(out, &object.Part{Number: p.PartNumber, Size: p.Size, ETag: strings.Trim(p.ETag, `"`), ModTime: p.LastModified})
	}
	return out, res.IsTruncated, nil
}

func (v *View) ListUploads(ctx context.Context, prefix, keyMarker, uploadIDMarker string, max int) ([]*object.Upload, bool, error) {
	km := ""
	if keyMarker != "" {
		km = v.full(keyMarker)
	}
	res, err := v.b.core.ListMultipartUploads(ctx, v.b.bucket, v.full(prefix), km, uploadIDMarker, "", max)
	if err != nil {
		return nil, false, mapErr(err)
	}
	var out []*object.Upload
	for _, u := range res.Uploads {
		if v.hidden(u.Key) || !strings.HasPrefix(u.Key, v.prefix) {
			continue
		}
		out = append(out, &object.Upload{Key: strings.TrimPrefix(u.Key, v.prefix), ID: u.UploadID, Initiated: u.Initiated})
	}
	return out, res.IsTruncated, nil
}

// ---- metadata area ----

// MetaArea returns the backend's ".acs" metadata area (for replication).
func (b *Backend) MetaArea() *MetaArea { return &MetaArea{b: b, root: b.prefix + internalDir} }

// MetaArea stores ACS metadata files under "<prefix>.acs/".
type MetaArea struct {
	b    *Backend
	root string
}

// ErrMetaNotExist is returned by MetaArea.Get for missing files.
var ErrMetaNotExist = errors.New("file does not exist")

func (a *MetaArea) Put(ctx context.Context, name string, r io.Reader, size int64) error {
	_, err := a.b.core.Client.PutObject(ctx, a.b.bucket, a.root+name, r, size,
		minio.PutObjectOptions{ContentType: "application/octet-stream", PartSize: partSize})
	return mapErr(err)
}

func (a *MetaArea) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	rc, _, _, err := a.b.core.GetObject(ctx, a.b.bucket, a.root+name, minio.GetObjectOptions{})
	if err != nil {
		if errors.Is(mapErr(err), object.ErrNoSuchKey) {
			return nil, ErrMetaNotExist
		}
		return nil, mapErr(err)
	}
	return rc, nil
}

func (a *MetaArea) List(ctx context.Context, prefix string) ([]string, error) {
	var out []string
	for o := range a.b.core.Client.ListObjects(ctx, a.b.bucket, minio.ListObjectsOptions{Prefix: a.root + prefix}) {
		if o.Err != nil {
			return nil, mapErr(o.Err)
		}
		if strings.HasSuffix(o.Key, "/") {
			continue
		}
		out = append(out, strings.TrimPrefix(o.Key, a.root))
	}
	return out, nil
}

func (a *MetaArea) Delete(ctx context.Context, name string) error {
	return mapErr(a.b.core.Client.RemoveObject(ctx, a.b.bucket, a.root+name, minio.RemoveObjectOptions{}))
}
