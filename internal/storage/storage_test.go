package storage_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"acs/internal/auth"
	"acs/internal/db"
	"acs/internal/metrics"
	"acs/internal/object"
	"acs/internal/s3"
	"acs/internal/secret"
	"acs/internal/settings"
	"acs/internal/storage"
)

// provider is a second ACS instance acting as the remote S3 provider.
type provider struct {
	objects        *object.Service
	endpoint       string
	accessKey, key string
}

func newProvider(t *testing.T) *provider {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	database, err := db.Open(ctx, filepath.Join(dir, "acs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	box, _ := secret.New(make([]byte, 32))
	authSvc, _ := auth.NewService(database, box, time.Hour, "")
	admin, err := authSvc.Setup(ctx, "admin", "provider password", "")
	if err != nil {
		t.Fatal(err)
	}
	k, sec, err := authSvc.CreateAccessKey(ctx, admin.ID, auth.CreateKeyInput{Name: "acs", Permission: auth.KeyFull})
	if err != nil {
		t.Fatal(err)
	}
	objects, err := object.Open(filepath.Join(dir, "meta"), filepath.Join(dir, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { objects.Close() })
	if _, err := objects.CreateBucket(ctx, "remote-bkt", object.BucketOptions{}); err != nil {
		t.Fatal(err)
	}
	st, _ := settings.Open(ctx, database)
	srv := httptest.NewServer(s3.New(objects, authSvc, st, metrics.New(), "").Handler())
	t.Cleanup(srv.Close)
	return &provider{objects: objects, endpoint: srv.URL, accessKey: k.ID, key: sec}
}

// local is the ACS instance under test.
type local struct {
	objects *object.Service
	mgr     *storage.Manager
}

func newLocal(t *testing.T) *local {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	database, err := db.Open(ctx, filepath.Join(dir, "acs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	box, _ := secret.New(make([]byte, 32))
	objects, err := object.Open(filepath.Join(dir, "meta"), filepath.Join(dir, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { objects.Close() })
	mgr, err := storage.Open(ctx, database, box, dir)
	if err != nil {
		t.Fatal(err)
	}
	objects.SetBackends(mgr)
	return &local{objects: objects, mgr: mgr}
}

func (l *local) addS3(t *testing.T, p *provider, prefix string) storage.Backend {
	t.Helper()
	b, err := l.mgr.Create(context.Background(), storage.Input{
		Name: "provider", Type: storage.TypeS3, Provider: "other", Endpoint: p.endpoint,
		Bucket: "remote-bkt", Prefix: prefix, AccessKey: p.accessKey, SecretKey: p.key, PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !b.Status.Online {
		t.Fatalf("backend offline: %s", b.Status.Error)
	}
	return b
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func readAll(t *testing.T, s *object.Service, bucket, key string) []byte {
	t.Helper()
	o, err := s.StatObject(context.Background(), bucket, key, "")
	if err != nil {
		t.Fatalf("stat %s/%s: %v", bucket, key, err)
	}
	r := s.Open(o)
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func put(t *testing.T, s *object.Service, bucket, key string, data []byte) *object.Object {
	t.Helper()
	o, err := s.PutObject(context.Background(), bucket, key, bytes.NewReader(data), object.PutOptions{Size: int64(len(data)), ContentType: "application/x-test"})
	if err != nil {
		t.Fatalf("put %s/%s: %v", bucket, key, err)
	}
	return o
}

func TestBadCredentialsRejected(t *testing.T) {
	p := newProvider(t)
	l := newLocal(t)
	_, err := l.mgr.Create(context.Background(), storage.Input{
		Name: "bad", Type: storage.TypeS3, Endpoint: p.endpoint, Bucket: "remote-bkt",
		AccessKey: p.accessKey, SecretKey: "wrong", PathStyle: true,
	})
	if err == nil || !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Fatalf("expected signature error, got %v", err)
	}
}

func TestS3StoreMode(t *testing.T) {
	ctx := context.Background()
	p := newProvider(t)
	l := newLocal(t)
	be := l.addS3(t, p, "acs")
	if _, err := l.objects.CreateBucket(ctx, "cloud", object.BucketOptions{Backend: be.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.objects.UpdateBucket(ctx, "cloud", func(b *object.Bucket) error { b.Versioning = object.VersioningEnabled; return nil }); err != nil {
		t.Fatal(err)
	}

	small := []byte("small object stays inline")
	big := randBytes(300 << 10)
	put(t, l.objects, "cloud", "small.txt", small)
	put(t, l.objects, "cloud", "dir/big.bin", big)
	if !bytes.Equal(readAll(t, l.objects, "cloud", "small.txt"), small) || !bytes.Equal(readAll(t, l.objects, "cloud", "dir/big.bin"), big) {
		t.Fatal("content mismatch")
	}

	// Range read through the remote store.
	o, _ := l.objects.StatObject(ctx, "cloud", "dir/big.bin", "")
	r := l.objects.Open(o)
	r.Seek(1000, io.SeekStart)
	chunk := make([]byte, 64)
	io.ReadFull(r, chunk)
	r.Close()
	if !bytes.Equal(chunk, big[1000:1064]) {
		t.Fatal("range mismatch")
	}

	// Multipart: parts are blobs in the backend.
	u, err := l.objects.CreateUpload(ctx, "cloud", "video.bin", object.PutOptions{})
	if err != nil {
		t.Fatal(err)
	}
	p1, p2 := randBytes(object.MinPartSize), randBytes(4096)
	a, err := l.objects.UploadPart(ctx, "cloud", "video.bin", u.ID, 1, bytes.NewReader(p1), int64(len(p1)), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := l.objects.UploadPart(ctx, "cloud", "video.bin", u.ID, 2, bytes.NewReader(p2), -1, nil)
	if _, err := l.objects.CompleteUpload(ctx, "cloud", "video.bin", u.ID, []object.CompletePart{{Number: 1, ETag: a.ETag}, {Number: 2, ETag: b.ETag}}); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, l.objects, "cloud", "video.bin"); !bytes.Equal(got, append(append([]byte(nil), p1...), p2...)) {
		t.Fatal("multipart content mismatch")
	}

	// Blobs are opaque objects under <prefix>.acs/blobs/ at the provider.
	res, err := p.objects.List(ctx, "remote-bkt", object.ListOptions{Prefix: "acs/.acs/blobs/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Objects) != 3 { // big.bin + 2 parts; small.txt is inline
		t.Fatalf("provider holds %d blobs, want 3", len(res.Objects))
	}

	// Copy and move between the built-in disk and the S3 backend.
	if _, err := l.objects.CreateBucket(ctx, "disk", object.BucketOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.objects.CopyObject(ctx, "cloud", "dir/big.bin", "disk", "copy.bin", object.CopyOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.objects.MoveObject(ctx, "disk", "copy.bin", "cloud", "moved.bin"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readAll(t, l.objects, "cloud", "moved.bin"), big) {
		t.Fatal("moved content mismatch")
	}

	// A blob written without metadata (crash) is removed by the scrubber.
	st, _ := l.mgr.Store(be.ID)
	if _, _, _, err := st.Put(ctx, bytes.NewReader(randBytes(10))); err != nil {
		t.Fatal(err)
	}
	n, err := l.objects.RemoveOrphans(time.Now().Add(time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("orphans removed %d, %v", n, err)
	}
	if !bytes.Equal(readAll(t, l.objects, "cloud", "dir/big.bin"), big) {
		t.Fatal("scrubber removed a live blob")
	}
}

func TestMirrorMode(t *testing.T) {
	ctx := context.Background()
	p := newProvider(t)
	l := newLocal(t)
	be := l.addS3(t, p, "acs")
	if _, err := l.objects.CreateBucket(ctx, "gallery", object.BucketOptions{Backend: be.ID, Mode: object.ModeMirror, RemotePrefix: "photos"}); err != nil {
		t.Fatal(err)
	}

	// Writes through ACS land under their real key at the provider.
	data := randBytes(200 << 10)
	put(t, l.objects, "gallery", "2026/cat.bin", data)
	ro, err := p.objects.StatObject(ctx, "remote-bkt", "acs/photos/2026/cat.bin", "")
	if err != nil || ro.Size != int64(len(data)) {
		t.Fatalf("object not at provider: %v", err)
	}

	// Objects written directly at the provider appear in ACS.
	if _, err := p.objects.PutObject(ctx, "remote-bkt", "acs/photos/readme.txt", strings.NewReader("hello"),
		object.PutOptions{Size: 5, ContentType: "text/plain", UserMeta: map[string]string{"origin": "provider"}}); err != nil {
		t.Fatal(err)
	}
	res, err := l.objects.List(ctx, "gallery", object.ListOptions{Delimiter: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Objects) != 1 || res.Objects[0].Key != "readme.txt" || len(res.CommonPrefixes) != 1 || res.CommonPrefixes[0] != "2026/" {
		t.Fatalf("mirror listing: %+v %v", res.Objects, res.CommonPrefixes)
	}
	o, err := l.objects.StatObject(ctx, "gallery", "readme.txt", "")
	if err != nil || o.ContentType != "text/plain" || o.UserMeta["origin"] != "provider" {
		t.Fatalf("stat: %+v %v", o, err)
	}
	if string(readAll(t, l.objects, "gallery", "readme.txt")) != "hello" {
		t.Fatal("read through mirror failed")
	}

	// Ranged read.
	o, _ = l.objects.StatObject(ctx, "gallery", "2026/cat.bin", "")
	r := l.objects.Open(o)
	r.Seek(5000, io.SeekStart)
	chunk := make([]byte, 100)
	io.ReadFull(r, chunk)
	r.Close()
	if !bytes.Equal(chunk, data[5000:5100]) {
		t.Fatal("mirror range mismatch")
	}

	// Tags and metadata updates pass through.
	if _, err := l.objects.SetObjectTags(ctx, "gallery", "readme.txt", "", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	tags, err := l.objects.ObjectTags(ctx, "gallery", "readme.txt", "")
	if err != nil || tags["k"] != "v" {
		t.Fatalf("tags %v %v", tags, err)
	}
	if _, err := l.objects.UpdateObjectMetadata(ctx, "gallery", "readme.txt", "text/markdown", map[string]string{"a": "b"}, nil); err != nil {
		t.Fatal(err)
	}
	if o, _ := l.objects.StatObject(ctx, "gallery", "readme.txt", ""); o.ContentType != "text/markdown" || o.UserMeta["a"] != "b" {
		t.Fatalf("metadata update not applied: %+v", o)
	}

	// Copy from a store-mode bucket into the mirror, and server-side within it.
	if _, err := l.objects.CreateBucket(ctx, "disk", object.BucketOptions{}); err != nil {
		t.Fatal(err)
	}
	put(t, l.objects, "disk", "doc.bin", data)
	if _, err := l.objects.CopyObject(ctx, "disk", "doc.bin", "gallery", "docs/doc.bin", object.CopyOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.objects.MoveObject(ctx, "gallery", "docs/doc.bin", "gallery", "docs/renamed.bin"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readAll(t, l.objects, "gallery", "docs/renamed.bin"), data) {
		t.Fatal("copied content mismatch")
	}
	if _, err := l.objects.StatObject(ctx, "gallery", "docs/doc.bin", ""); !errors.Is(err, object.ErrNoSuchKey) {
		t.Fatalf("move source still present: %v", err)
	}

	// Multipart through the mirror.
	u, err := l.objects.CreateUpload(ctx, "gallery", "big.bin", object.PutOptions{ContentType: "video/mp4"})
	if err != nil {
		t.Fatal(err)
	}
	p1, p2 := randBytes(object.MinPartSize), randBytes(1000)
	a, err := l.objects.UploadPart(ctx, "gallery", "big.bin", u.ID, 1, bytes.NewReader(p1), int64(len(p1)), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.objects.UploadPart(ctx, "gallery", "big.bin", u.ID, 2, bytes.NewReader(p2), -1, nil)
	if err != nil {
		t.Fatal(err)
	}
	parts, _, err := l.objects.ListParts(ctx, "gallery", "big.bin", u.ID, 0, 0)
	if err != nil || len(parts) != 2 {
		t.Fatalf("list parts: %d %v", len(parts), err)
	}
	if _, err := l.objects.CompleteUpload(ctx, "gallery", "big.bin", u.ID, []object.CompletePart{{Number: 1, ETag: a.ETag}, {Number: 2, ETag: b.ETag}}); err != nil {
		t.Fatal(err)
	}
	if got := readAll(t, l.objects, "gallery", "big.bin"); len(got) != len(p1)+len(p2) {
		t.Fatalf("multipart size %d", len(got))
	}

	// Mirror buckets reject features owned by the provider.
	if _, err := l.objects.UpdateBucket(ctx, "gallery", func(b *object.Bucket) error { b.Versioning = object.VersioningEnabled; return nil }); err == nil {
		t.Fatal("versioning must be rejected on a mirror bucket")
	}

	// Delete passes through; deleting the bucket leaves provider data alone.
	if _, err := l.objects.DeleteObject(ctx, "gallery", "readme.txt", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := p.objects.StatObject(ctx, "remote-bkt", "acs/photos/readme.txt", ""); !errors.Is(err, object.ErrNoSuchKey) {
		t.Fatalf("delete did not reach provider: %v", err)
	}
	if err := l.objects.DeleteBucket(ctx, "gallery", true); err != nil {
		t.Fatal(err)
	}
	if _, err := p.objects.StatObject(ctx, "remote-bkt", "acs/photos/2026/cat.bin", ""); err != nil {
		t.Fatalf("deleting the mirror bucket removed provider data: %v", err)
	}
}

func TestMirrorHidesInternalData(t *testing.T) {
	ctx := context.Background()
	p := newProvider(t)
	l := newLocal(t)
	be := l.addS3(t, p, "")
	// A store-mode bucket writes blobs under .acs/ at the provider root...
	l.objects.CreateBucket(ctx, "store", object.BucketOptions{Backend: be.ID})
	put(t, l.objects, "store", "x.bin", randBytes(100<<10))
	// ...which a root mirror of the same provider must not expose.
	if _, err := l.objects.CreateBucket(ctx, "root", object.BucketOptions{Backend: be.ID, Mode: object.ModeMirror}); err != nil {
		t.Fatal(err)
	}
	put(t, l.objects, "root", "visible.txt", []byte("hi"))
	res, err := l.objects.List(ctx, "root", object.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Objects) != 1 || res.Objects[0].Key != "visible.txt" || len(res.CommonPrefixes) != 0 {
		t.Fatalf("root mirror listing leaked internal data: %+v %v", res.Objects, res.CommonPrefixes)
	}
}

func TestDriveBackend(t *testing.T) {
	ctx := context.Background()
	l := newLocal(t)
	drive := filepath.Join(t.TempDir(), "disk2")
	os.MkdirAll(drive, 0o750)

	if _, err := l.mgr.Create(ctx, storage.Input{Name: "missing", Type: storage.TypeLocal, Path: drive + "-nope"}); err == nil {
		t.Fatal("a missing directory must be rejected")
	}
	be, err := l.mgr.Create(ctx, storage.Input{Name: "Disk 2", Type: storage.TypeLocal, Path: drive})
	if err != nil {
		t.Fatal(err)
	}
	if !be.Status.Online || be.Status.TotalBytes == 0 {
		t.Fatalf("drive status %+v", be.Status)
	}
	if _, err := l.mgr.Create(ctx, storage.Input{Name: "again", Type: storage.TypeLocal, Path: drive}); err == nil {
		t.Fatal("attaching the same drive twice must fail")
	}

	l.objects.CreateBucket(ctx, "archive", object.BucketOptions{Backend: be.ID})
	data := randBytes(500 << 10)
	o := put(t, l.objects, "archive", "a.bin", data)
	if _, err := os.Stat(filepath.Join(drive, "blobs", o.Parts[0].Blob[0:2], o.Parts[0].Blob[2:4], o.Parts[0].Blob)); err != nil {
		t.Fatalf("blob not on the drive: %v", err)
	}
	if !bytes.Equal(readAll(t, l.objects, "archive", "a.bin"), data) {
		t.Fatal("drive content mismatch")
	}
	if _, err := l.objects.CreateBucket(ctx, "bad", object.BucketOptions{Backend: be.ID, Mode: object.ModeMirror}); err == nil {
		t.Fatal("mirror mode needs an S3 backend")
	}
	if err := l.mgr.Delete(ctx, be.ID, true); !errors.Is(err, storage.ErrInUse) {
		t.Fatalf("delete in-use backend: %v", err)
	}
}
