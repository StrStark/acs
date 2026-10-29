package replica_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"acs/internal/auth"
	"acs/internal/db"
	"acs/internal/object"
	"acs/internal/replica"
	"acs/internal/secret"
	"acs/internal/storage"
)

// machine is one ACS installation's data directory, opened in-process.
type machine struct {
	dir     string
	db      *sql.DB
	box     *secret.Box
	auth    *auth.Service
	objects *object.Service
	mgr     *storage.Manager
	rep     *replica.Replicator
}

func open(t *testing.T, dir string) *machine {
	t.Helper()
	ctx := context.Background()
	m := &machine{dir: dir}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	var err error
	if m.db, err = db.Open(ctx, filepath.Join(dir, "acs.db")); err != nil {
		t.Fatal(err)
	}
	if m.box, err = secret.LoadOrCreate(filepath.Join(dir, "master.key"), ""); err != nil {
		t.Fatal(err)
	}
	m.auth, _ = auth.NewService(m.db, m.box, time.Hour, "")
	if m.objects, err = object.Open(filepath.Join(dir, "meta"), filepath.Join(dir, "objects")); err != nil {
		t.Fatal(err)
	}
	if m.mgr, err = storage.Open(ctx, m.db, m.box, dir); err != nil {
		t.Fatal(err)
	}
	m.objects.SetBackends(m.mgr)
	inst, err := replica.EnsureInstallation(ctx, m.db)
	if err != nil {
		t.Fatal(err)
	}
	m.rep = replica.New(replica.Options{
		Objects: m.objects, DB: m.db, DataDir: dir, Installation: inst,
		SiteName: func() string { return "machine " + filepath.Base(dir) },
		Keyring:  func() []byte { return replica.LoadKeyring(ctx, m.db) },
	})
	m.objects.SetJournal(m.rep.Add)
	m.sync()
	return m
}

// sync registers every backend as a replication target (flushing at once).
func (m *machine) sync() {
	specs := []replica.TargetSpec{{ID: "", Name: "Built-in disk", Kind: "local",
		Area: replica.NewLocalArea(filepath.Join(m.dir, "objects", ".acs"))}}
	for _, tg := range m.mgr.Targets() {
		specs = append(specs, replica.TargetSpec{ID: tg.ID, Name: tg.Name, Kind: tg.Type, Area: tg.Area})
	}
	m.rep.Sync(context.Background(), specs)
}

func (m *machine) close() {
	m.objects.Close()
	m.db.Close()
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func put(t *testing.T, m *machine, bucket, key string, data []byte) {
	t.Helper()
	if _, err := m.objects.PutObject(context.Background(), bucket, key, bytes.NewReader(data), object.PutOptions{Size: int64(len(data))}); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, m *machine, bucket, key string) []byte {
	t.Helper()
	o, err := m.objects.StatObject(context.Background(), bucket, key, "")
	if err != nil {
		t.Fatalf("stat %s/%s: %v", bucket, key, err)
	}
	r := m.objects.Open(o)
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read %s/%s: %v", bucket, key, err)
	}
	return data
}

func TestDisasterRecoveryFromOneDrive(t *testing.T) {
	ctx := context.Background()
	drives := t.TempDir()
	drive := filepath.Join(drives, "disk2")
	os.MkdirAll(drive, 0o750)

	// ---- machine A: normal operation ----
	a := open(t, filepath.Join(t.TempDir(), "a"))
	admin, err := a.auth.Setup(ctx, "admin", "correct horse battery", "")
	if err != nil {
		t.Fatal(err)
	}
	key, secretKey, _ := a.auth.CreateAccessKey(ctx, admin.ID, auth.CreateKeyInput{Name: "ci", Permission: auth.KeyFull})
	kr, _ := a.box.Lock("my recovery passphrase")
	replica.SaveKeyring(ctx, a.db, kr.Marshal())

	be, err := a.mgr.Create(ctx, storage.Input{Name: "Disk 2", Type: storage.TypeLocal, Path: drive})
	if err != nil {
		t.Fatal(err)
	}
	a.sync()
	a.objects.CreateBucket(ctx, "photos", object.BucketOptions{Backend: be.ID})
	a.objects.UpdateBucket(ctx, "photos", func(b *object.Bucket) error { b.Versioning = object.VersioningEnabled; return nil })
	a.objects.CreateBucket(ctx, "docs", object.BucketOptions{})

	big := randBytes(200 << 10)
	put(t, a, "photos", "cat.jpg", big)
	put(t, a, "photos", "note.txt", []byte("v1"))
	put(t, a, "photos", "note.txt", []byte("v2")) // a second version
	put(t, a, "docs", "inline.txt", []byte("small docs are stored inline"))
	put(t, a, "docs", "blob.bin", randBytes(100<<10))
	a.rep.Flush(ctx, false) // snapshot + catalog + manifest

	// Writes after the snapshot travel in journal segments.
	late := randBytes(150 << 10)
	put(t, a, "photos", "late.bin", late)
	a.objects.DeleteObject(ctx, "photos", "cat.jpg", "") // delete marker
	a.rep.Flush(ctx, false)
	a.rep.Flush(ctx, false)

	m, err := replica.ReadManifest(ctx, replica.NewLocalArea(filepath.Join(drive, ".acs")))
	if err != nil || len(m.Buckets) != 1 || m.Buckets[0] != "photos" || !m.HasKeyring {
		t.Fatalf("drive manifest: %+v %v", m, err)
	}
	a.close()

	// ---- the drive moves to another machine (and another mount path) ----
	moved := filepath.Join(drives, "usb-disk")
	if err := os.Rename(drive, moved); err != nil {
		t.Fatal(err)
	}
	found := replica.Scan(drives)
	if len(found) != 1 || found[0].Manifest == nil || found[0].Path != moved {
		t.Fatalf("scan: %+v", found)
	}
	src, err := replica.ReadSource(ctx, replica.NewLocalArea(filepath.Join(moved, ".acs")))
	if err != nil {
		t.Fatal(err)
	}
	bdir := filepath.Join(t.TempDir(), "b")
	open(t, bdir).close() // B first boots as a fresh installation, writing its own built-in copy
	if err := replica.Stage(bdir, src, "wrong passphrase"); !errors.Is(err, secret.ErrWrongPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	if err := replica.Stage(bdir, src, "my recovery passphrase"); err != nil {
		t.Fatal(err)
	}
	if applied, err := replica.ApplyStaged(bdir); err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}

	// ---- machine B: everything is back ----
	b := open(t, bdir)
	defer b.close()
	if moved := b.mgr.Relocate(ctx, drives); len(moved) != 1 {
		t.Fatalf("drive not relocated: %v", moved)
	}
	b.sync()
	for id, st := range b.rep.Status() {
		if st.Foreign != "" {
			t.Fatalf("target %q reported foreign after restore", id)
		}
	}
	if _, _, err := b.auth.Login(ctx, "admin", "correct horse battery", "", ""); err != nil {
		t.Fatalf("users not restored: %v", err)
	}
	if _, err := b.auth.AuthenticateKey(ctx, key.ID, secretKey); err != nil {
		t.Fatalf("access key secret not restored: %v", err)
	}
	if !bytes.Equal(read(t, b, "photos", "late.bin"), late) {
		t.Fatal("late object content mismatch")
	}
	if string(read(t, b, "photos", "note.txt")) != "v2" {
		t.Fatal("current version wrong")
	}
	if vers, _ := b.objects.ObjectVersions(ctx, "photos", "note.txt"); len(vers) != 2 {
		t.Fatalf("versions not restored: %d", len(vers))
	}
	if _, err := b.objects.StatObject(ctx, "photos", "cat.jpg", ""); !errors.Is(err, object.ErrNoSuchKey) {
		t.Fatal("delete marker not restored")
	}
	if st, _ := b.objects.BucketStats(ctx, "photos"); st.Objects != 2 {
		t.Fatalf("stats not recomputed: %+v", st)
	}
	// Inline data lived in the metadata, so it survives losing the built-in disk.
	if string(read(t, b, "docs", "inline.txt")) != "small docs are stored inline" {
		t.Fatal("inline object lost")
	}

	// ---- integrity: the built-in disk's blob is gone, and we lose one on the drive ----
	res, err := b.objects.Inventory(ctx, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if missing := count(res.Issues, object.IssueMissing); missing != 1 {
		t.Fatalf("expected the docs blob to be reported missing, got %+v", res.Issues)
	}
	o, _ := b.objects.StatObject(ctx, "photos", "late.bin", "")
	blobPath := filepath.Join(moved, "blobs", o.Parts[0].Blob[0:2], o.Parts[0].Blob[2:4], o.Parts[0].Blob)
	os.WriteFile(blobPath, randBytes(int(o.Size)), 0o640) // silent corruption, same size
	res, _ = b.objects.Inventory(ctx, true, 0)
	if count(res.Issues, object.IssueCorrupt) != 1 {
		t.Fatalf("deep check missed corruption: %+v", res.Issues)
	}
	os.Remove(blobPath)
	res, _ = b.objects.Inventory(ctx, false, 0)
	if count(res.Issues, object.IssueMissing) != 2 {
		t.Fatalf("expected 2 missing, got %+v", res.Issues)
	}
}

func count(issues []object.Issue, kind string) int {
	n := 0
	for _, is := range issues {
		if is.Kind == kind {
			n++
		}
	}
	return n
}

func TestImportDriveFromAnotherInstallation(t *testing.T) {
	ctx := context.Background()
	drives := t.TempDir()
	drive := filepath.Join(drives, "disk")
	os.MkdirAll(drive, 0o750)

	a := open(t, filepath.Join(t.TempDir(), "a"))
	be, _ := a.mgr.Create(ctx, storage.Input{Name: "Travel disk", Type: storage.TypeLocal, Path: drive})
	a.sync()
	a.objects.CreateBucket(ctx, "trip", object.BucketOptions{Backend: be.ID})
	data := randBytes(120 << 10)
	put(t, a, "trip", "beach.jpg", data)
	a.rep.Flush(ctx, false)
	a.close()

	// Another installation that already has a bucket called "trip"... no: a different name.
	c := open(t, filepath.Join(t.TempDir(), "c"))
	defer c.close()
	c.objects.CreateBucket(ctx, "home", object.BucketOptions{})
	c.rep.Flush(ctx, false)

	area := replica.NewLocalArea(filepath.Join(drive, ".acs"))
	// Until imported, the drive is foreign and nothing is written to it.
	adopted, err := c.mgr.AdoptDrive(ctx, be.ID, "Travel disk", drive)
	if err != nil {
		t.Fatal(err)
	}
	c.sync()
	if st := c.rep.Status()[adopted.ID]; st.Foreign == "" {
		t.Fatalf("drive should be reported as foreign: %+v", st)
	}
	src, err := replica.ReadSource(ctx, area)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.objects.ImportBuckets(ctx, bytes.NewReader(src.Snapshot), src.Records, adopted.ID)
	if err != nil || len(res.Imported) != 1 || res.Imported[0] != "trip" {
		t.Fatalf("import: %+v %v", res, err)
	}
	if !bytes.Equal(read(t, c, "trip", "beach.jpg"), data) {
		t.Fatal("imported object content mismatch")
	}
	c.rep.Adopt(adopted.ID)
	c.rep.Flush(ctx, false)
	m, _ := replica.ReadManifest(ctx, area)
	if m.SiteName != "machine c" {
		t.Fatalf("drive not taken over: %+v", m)
	}
	// Importing again skips the existing bucket.
	res, _ = c.objects.ImportBuckets(ctx, bytes.NewReader(src.Snapshot), src.Records, adopted.ID)
	if len(res.Skipped) != 1 {
		t.Fatalf("re-import should skip: %+v", res)
	}
}

func TestCleanRestartContinuesJournal(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "a")
	a := open(t, dir)
	a.objects.CreateBucket(ctx, "bucket1", object.BucketOptions{})
	put(t, a, "bucket1", "x", []byte("1"))
	a.rep.Flush(ctx, true) // clean shutdown
	area := replica.NewLocalArea(filepath.Join(dir, "objects", ".acs"))
	before, _ := area.List(ctx, "meta/")
	a.close()

	a = open(t, dir)
	defer a.close()
	put(t, a, "bucket1", "y", []byte("2"))
	a.rep.Flush(ctx, false)
	after, _ := area.List(ctx, "meta/")
	snaps := 0
	for _, n := range after {
		if filepath.Base(n)[:5] == "snap-" {
			snaps++
		}
	}
	if snaps != 1 || len(after) <= len(before) {
		t.Fatalf("expected a journal segment on top of the existing snapshot; before=%v after=%v", before, after)
	}
}
