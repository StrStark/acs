package object

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "meta"), filepath.Join(dir, "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustBucket(t *testing.T, s *Service, name string) {
	t.Helper()
	if _, err := s.CreateBucket(context.Background(), name); err != nil {
		t.Fatal(err)
	}
}

func put(t *testing.T, s *Service, bucket, key string, data []byte) *Object {
	t.Helper()
	o, err := s.PutObject(context.Background(), bucket, key, bytes.NewReader(data), PutOptions{Size: int64(len(data))})
	if err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
	return o
}

func read(t *testing.T, s *Service, bucket, key, version string) []byte {
	t.Helper()
	o, err := s.StatObject(context.Background(), bucket, key, version)
	if err != nil {
		t.Fatalf("stat %s: %v", key, err)
	}
	r := s.Open(o)
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func TestBucketLifecycle(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	if _, err := s.CreateBucket(ctx, "Bad_Name"); !errors.Is(err, ErrInvalidBucketName) {
		t.Fatalf("got %v", err)
	}
	mustBucket(t, s, "photos")
	if _, err := s.CreateBucket(ctx, "photos"); !errors.Is(err, ErrBucketExists) {
		t.Fatalf("got %v", err)
	}
	put(t, s, "photos", "a.jpg", []byte("x"))
	if err := s.DeleteBucket(ctx, "photos", false); !errors.Is(err, ErrBucketNotEmpty) {
		t.Fatalf("got %v", err)
	}
	if err := s.DeleteBucket(ctx, "photos", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetBucket(ctx, "photos"); !errors.Is(err, ErrNoSuchBucket) {
		t.Fatalf("got %v", err)
	}
}

func TestPutGetInlineAndBlob(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	mustBucket(t, s, "bk-b1")

	small := []byte("hello world")
	o := put(t, s, "bk-b1", "small.txt", small)
	if len(o.Parts) != 0 || o.ETag != "5eb63bbbe01eeed093cb22bb8f5acdc3" {
		t.Fatalf("small object: parts=%d etag=%s", len(o.Parts), o.ETag)
	}
	big := randBytes(inlineMax + 12345)
	o = put(t, s, "bk-b1", "dir/big.bin", big)
	if len(o.Parts) != 1 {
		t.Fatalf("big object should be a blob")
	}
	if !bytes.Equal(read(t, s, "bk-b1", "small.txt", ""), small) || !bytes.Equal(read(t, s, "bk-b1", "dir/big.bin", ""), big) {
		t.Fatal("content mismatch")
	}

	// Range read via Seek.
	st, _ := s.StatObject(ctx, "bk-b1", "dir/big.bin", "")
	r := s.Open(st)
	r.Seek(1000, io.SeekStart)
	chunk := make([]byte, 100)
	io.ReadFull(r, chunk)
	r.Close()
	if !bytes.Equal(chunk, big[1000:1100]) {
		t.Fatal("range mismatch")
	}

	stats, _ := s.BucketStats(ctx, "bk-b1")
	if stats.Objects != 2 || stats.Bytes != int64(len(small)+len(big)) {
		t.Fatalf("stats %+v", stats)
	}

	// Overwrite replaces and updates stats.
	put(t, s, "bk-b1", "small.txt", []byte("hi"))
	stats, _ = s.BucketStats(ctx, "bk-b1")
	if stats.Objects != 2 || stats.Bytes != int64(2+len(big)) || stats.StoredBytes != stats.Bytes {
		t.Fatalf("stats after overwrite %+v", stats)
	}

	if _, err := s.DeleteObject(ctx, "bk-b1", "small.txt", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StatObject(ctx, "bk-b1", "small.txt", ""); !errors.Is(err, ErrNoSuchKey) {
		t.Fatalf("got %v", err)
	}
	stats, _ = s.BucketStats(ctx, "bk-b1")
	if stats.Objects != 1 {
		t.Fatalf("stats after delete %+v", stats)
	}
}

func TestBadDigestAndSize(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	mustBucket(t, s, "bk-b1")
	_, err := s.PutObject(ctx, "bk-b1", "k", strings.NewReader("abc"), PutOptions{Size: 3, ContentMD5: make([]byte, 16)})
	if !errors.Is(err, ErrBadDigest) {
		t.Fatalf("got %v", err)
	}
	_, err = s.PutObject(ctx, "bk-b1", "k", strings.NewReader("abc"), PutOptions{Size: 5})
	if !errors.Is(err, ErrIncompleteBody) {
		t.Fatalf("got %v", err)
	}
}

func TestListDelimiterAndPagination(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	mustBucket(t, s, "bk-b1")
	for _, k := range []string{"a.txt", "docs/1.txt", "docs/2.txt", "docs/sub/3.txt", "img/x.png", "z.txt"} {
		put(t, s, "bk-b1", k, []byte(k))
	}

	res, err := s.List(ctx, "bk-b1", ListOptions{Delimiter: "/"})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(res); got != "a.txt,z.txt|docs/,img/" {
		t.Fatalf("root listing: %s", got)
	}

	res, _ = s.List(ctx, "bk-b1", ListOptions{Prefix: "docs/", Delimiter: "/"})
	if got := names(res); got != "docs/1.txt,docs/2.txt|docs/sub/" {
		t.Fatalf("docs listing: %s", got)
	}

	// Paginate the root listing one entry at a time.
	var seen []string
	marker := ""
	for {
		res, err := s.List(ctx, "bk-b1", ListOptions{Delimiter: "/", Marker: marker, MaxKeys: 1})
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range res.Objects {
			seen = append(seen, o.Key)
		}
		seen = append(seen, res.CommonPrefixes...)
		if !res.IsTruncated {
			break
		}
		marker = res.NextMarker
	}
	if strings.Join(seen, ",") != "a.txt,docs/,img/,z.txt" {
		t.Fatalf("paginated: %v", seen)
	}
}

func names(r *ListResult) string {
	var o []string
	for _, x := range r.Objects {
		o = append(o, x.Key)
	}
	return strings.Join(o, ",") + "|" + strings.Join(r.CommonPrefixes, ",")
}

func TestVersioning(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	mustBucket(t, s, "bk-v")
	put(t, s, "bk-v", "doc", []byte("unversioned"))
	if _, err := s.UpdateBucket(ctx, "bk-v", func(b *Bucket) error { b.Versioning = VersioningEnabled; return nil }); err != nil {
		t.Fatal(err)
	}
	v1 := put(t, s, "bk-v", "doc", []byte("one"))
	v2 := put(t, s, "bk-v", "doc", []byte("two"))
	if v1.VersionID == v2.VersionID || v1.VersionID == NullVersion {
		t.Fatal("expected distinct version IDs")
	}
	if string(read(t, s, "bk-v", "doc", "")) != "two" || string(read(t, s, "bk-v", "doc", v1.VersionID)) != "one" ||
		string(read(t, s, "bk-v", "doc", NullVersion)) != "unversioned" {
		t.Fatal("version contents wrong")
	}

	del, err := s.DeleteObject(ctx, "bk-v", "doc", "")
	if err != nil || !del.DeleteMarker {
		t.Fatalf("delete: %+v %v", del, err)
	}
	if _, err := s.StatObject(ctx, "bk-v", "doc", ""); !errors.Is(err, ErrNoSuchKey) {
		t.Fatalf("after delete marker: %v", err)
	}
	vers, _ := s.ObjectVersions(ctx, "bk-v", "doc")
	if len(vers) != 4 || !vers[0].DeleteMarker || !vers[0].IsLatest {
		t.Fatalf("versions: %d", len(vers))
	}

	// Removing the delete marker restores the previous version.
	if _, err := s.DeleteObject(ctx, "bk-v", "doc", del.VersionID); err != nil {
		t.Fatal(err)
	}
	if string(read(t, s, "bk-v", "doc", "")) != "two" {
		t.Fatal("restore failed")
	}
	// Removing the current version promotes the previous one.
	if _, err := s.DeleteObject(ctx, "bk-v", "doc", v2.VersionID); err != nil {
		t.Fatal(err)
	}
	if string(read(t, s, "bk-v", "doc", "")) != "one" {
		t.Fatal("promotion failed")
	}
	stats, _ := s.BucketStats(ctx, "bk-v")
	if stats.Objects != 1 || stats.Bytes != 3 || stats.StoredBytes != int64(len("unversioned")+3) {
		t.Fatalf("stats %+v", stats)
	}

	lv, err := s.ListVersions(ctx, "bk-v", ListVersionsOptions{})
	if err != nil || len(lv.Versions) != 2 || !lv.Versions[0].IsLatest || lv.Versions[1].IsLatest {
		t.Fatalf("ListVersions: %+v %v", lv, err)
	}

	// Suspended: writes replace the null version.
	s.UpdateBucket(ctx, "bk-v", func(b *Bucket) error { b.Versioning = VersioningSuspended; return nil })
	put(t, s, "bk-v", "doc", []byte("s1"))
	put(t, s, "bk-v", "doc", []byte("s2"))
	vers, _ = s.ObjectVersions(ctx, "bk-v", "doc")
	nulls := 0
	for _, v := range vers {
		if v.VersionID == NullVersion {
			nulls++
		}
	}
	if nulls != 1 || len(vers) != 2 {
		t.Fatalf("suspended versions: total=%d nulls=%d", len(vers), nulls)
	}
	if _, err := s.UpdateBucket(ctx, "bk-v", func(b *Bucket) error { b.Versioning = VersioningOff; return nil }); err == nil {
		t.Fatal("disabling versioning should fail")
	}
}

func TestMultipart(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	mustBucket(t, s, "bk-mp")
	u, err := s.CreateUpload(ctx, "bk-mp", "video.mp4", PutOptions{ContentType: "video/mp4"})
	if err != nil {
		t.Fatal(err)
	}
	p1data := randBytes(MinPartSize)
	p2data := randBytes(1234)
	p1, err := s.UploadPart(ctx, "bk-mp", "video.mp4", u.ID, 1, bytes.NewReader(p1data), -1, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Re-upload part 1 then upload part 2.
	p1, _ = s.UploadPart(ctx, "bk-mp", "video.mp4", u.ID, 1, bytes.NewReader(p1data), int64(len(p1data)), nil)
	p2, _ := s.UploadPart(ctx, "bk-mp", "video.mp4", u.ID, 2, bytes.NewReader(p2data), -1, nil)
	parts, _, _ := s.ListParts(ctx, "bk-mp", "video.mp4", u.ID, 0, 0)
	if len(parts) != 2 {
		t.Fatalf("parts: %d", len(parts))
	}
	uploads, _, _ := s.ListUploads(ctx, "bk-mp", "", "", "", 0)
	if len(uploads) != 1 {
		t.Fatalf("uploads: %d", len(uploads))
	}

	if _, err := s.CompleteUpload(ctx, "bk-mp", "video.mp4", u.ID, []CompletePart{{2, p2.ETag}, {1, p1.ETag}}); !errors.Is(err, ErrInvalidPartOrder) {
		t.Fatalf("order: %v", err)
	}
	o, err := s.CompleteUpload(ctx, "bk-mp", "video.mp4", u.ID, []CompletePart{{1, `"` + p1.ETag + `"`}, {2, p2.ETag}})
	if err != nil {
		t.Fatal(err)
	}
	if o.Size != int64(len(p1data)+len(p2data)) || !strings.HasSuffix(o.ETag, "-2") || o.ContentType != "video/mp4" || o.PartsCount() != 2 {
		t.Fatalf("completed object %+v", o)
	}
	got := read(t, s, "bk-mp", "video.mp4", "")
	if !bytes.Equal(got, append(append([]byte(nil), p1data...), p2data...)) {
		t.Fatal("multipart content mismatch")
	}
	// Reading across the part boundary.
	r := s.Open(o)
	r.Seek(int64(len(p1data))-10, io.SeekStart)
	buf := make([]byte, 20)
	io.ReadFull(r, buf)
	r.Close()
	if !bytes.Equal(buf, got[len(p1data)-10:len(p1data)+10]) {
		t.Fatal("boundary read mismatch")
	}
	if _, err := s.GetUpload(ctx, "bk-mp", "video.mp4", u.ID); !errors.Is(err, ErrNoSuchUpload) {
		t.Fatal("upload should be gone")
	}

	// Too-small non-final parts are rejected.
	u2, _ := s.CreateUpload(ctx, "bk-mp", "x", PutOptions{})
	a, _ := s.UploadPart(ctx, "bk-mp", "x", u2.ID, 1, strings.NewReader("tiny"), -1, nil)
	b, _ := s.UploadPart(ctx, "bk-mp", "x", u2.ID, 2, strings.NewReader("tiny"), -1, nil)
	if _, err := s.CompleteUpload(ctx, "bk-mp", "x", u2.ID, []CompletePart{{1, a.ETag}, {2, b.ETag}}); !errors.Is(err, ErrEntityTooSmall) {
		t.Fatalf("got %v", err)
	}
	if err := s.AbortUpload(ctx, "bk-mp", "x", u2.ID); err != nil {
		t.Fatal(err)
	}
}

func TestCopyMoveAndGC(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	mustBucket(t, s, "bk-a")
	mustBucket(t, s, "bk-b")
	data := randBytes(inlineMax * 2)
	put(t, s, "bk-a", "src.bin", data)

	if _, err := s.CopyObject(ctx, "bk-a", "src.bin", "bk-b", "copy.bin", CopyOptions{ReplaceMetadata: true, ContentType: "x/y"}); err != nil {
		t.Fatal(err)
	}
	st, _ := s.StatObject(ctx, "bk-b", "copy.bin", "")
	if st.ContentType != "x/y" || !bytes.Equal(read(t, s, "bk-b", "copy.bin", ""), data) {
		t.Fatal("copy wrong")
	}

	if _, err := s.MoveObject(ctx, "bk-a", "src.bin", "bk-a", "moved/dst.bin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StatObject(ctx, "bk-a", "src.bin", ""); !errors.Is(err, ErrNoSuchKey) {
		t.Fatal("source should be gone")
	}
	if !bytes.Equal(read(t, s, "bk-a", "moved/dst.bin", ""), data) {
		t.Fatal("moved content wrong")
	}
	sa, _ := s.BucketStats(ctx, "bk-a")
	if sa.Objects != 1 || sa.StoredBytes != int64(len(data)) {
		t.Fatalf("stats after move %+v", sa)
	}

	// Metadata-only self copy keeps data.
	if _, err := s.CopyObject(ctx, "bk-a", "moved/dst.bin", "bk-a", "moved/dst.bin", CopyOptions{ReplaceMetadata: true, UserMeta: map[string]string{"k": "v"}}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(read(t, s, "bk-a", "moved/dst.bin", ""), data) {
		t.Fatal("self copy lost data")
	}

	// Deleting schedules blobs; GC removes them after the grace period.
	if _, err := s.DeleteObject(ctx, "bk-b", "copy.bin", ""); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.collectGarbage(time.Now().Add(-time.Hour)); n != 0 {
		t.Fatal("gc must respect grace period")
	}
	if n, _ := s.collectGarbage(time.Now().Add(time.Hour)); n != 1 {
		t.Fatalf("gc deleted %d", n)
	}
}

func TestQuota(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	mustBucket(t, s, "bk-q")
	s.UpdateBucket(ctx, "bk-q", func(b *Bucket) error { b.QuotaBytes = 10; b.QuotaObjects = 2; return nil })
	put(t, s, "bk-q", "bk-a", []byte("12345"))
	if _, err := s.PutObject(ctx, "bk-q", "bk-b", strings.NewReader("1234567"), PutOptions{Size: 7}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("bytes quota: %v", err)
	}
	// Overwrite that fits once the old version is freed.
	put(t, s, "bk-q", "bk-a", []byte("1234567890"))
	s.UpdateBucket(ctx, "bk-q", func(b *Bucket) error { b.QuotaBytes = 0; return nil })
	put(t, s, "bk-q", "bk-b", []byte("x"))
	if _, err := s.PutObject(ctx, "bk-q", "bk-c", strings.NewReader("x"), PutOptions{Size: -1}); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("object quota: %v", err)
	}
}

func TestLifecycleExpiration(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	mustBucket(t, s, "bk-lc")
	put(t, s, "bk-lc", "logs/old.log", []byte("x"))
	put(t, s, "bk-lc", "keep.txt", []byte("y"))
	s.UpdateBucket(ctx, "bk-lc", func(b *Bucket) error {
		b.Lifecycle = []LifecycleRule{{ID: "logs", Enabled: true, Prefix: "logs/", ExpirationDays: 1}}
		return nil
	})
	if err := s.ApplyLifecycle(ctx, time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StatObject(ctx, "bk-lc", "logs/old.log", ""); !errors.Is(err, ErrNoSuchKey) {
		t.Fatal("expired object should be deleted")
	}
	if _, err := s.StatObject(ctx, "bk-lc", "keep.txt", ""); err != nil {
		t.Fatal("object outside prefix should remain")
	}
}

func TestOrphanScrub(t *testing.T) {
	s := newTestService(t)
	mustBucket(t, s, "bk-o")
	put(t, s, "bk-o", "kept", randBytes(inlineMax+1))
	// Simulate a crash after writing a blob but before committing metadata.
	if _, _, _, err := s.blobs.CopyFrom(bytes.NewReader(randBytes(10))); err != nil {
		t.Fatal(err)
	}
	n, err := s.RemoveOrphans(time.Now().Add(time.Minute))
	if err != nil || n != 1 {
		t.Fatalf("removed %d, %v", n, err)
	}
	read(t, s, "bk-o", "kept", "")
}

func TestConcurrentPuts(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	mustBucket(t, s, "bk-c")
	errs := make(chan error, 50)
	for i := 0; i < 50; i++ {
		go func(i int) {
			_, err := s.PutObject(ctx, "bk-c", fmt.Sprintf("k%d", i%10), strings.NewReader("data"), PutOptions{Size: -1})
			errs <- err
		}(i)
	}
	for i := 0; i < 50; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	st, _ := s.BucketStats(ctx, "bk-c")
	if st.Objects != 10 || st.Bytes != 40 || st.StoredBytes != 40 {
		t.Fatalf("stats %+v", st)
	}
}
