package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"acs/internal/audit"
	"acs/internal/auth"
	"acs/internal/config"
	"acs/internal/db"
	"acs/internal/metrics"
	"acs/internal/object"
	"acs/internal/secret"
	"acs/internal/settings"
	"acs/internal/share"
	"acs/internal/webhook"
)

type harness struct {
	t   *testing.T
	srv *httptest.Server
}

func newHarness(t *testing.T) *harness {
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
	objects, err := object.Open(filepath.Join(dir, "meta"), filepath.Join(dir, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { objects.Close() })
	st, _ := settings.Open(ctx, database)
	hooks, _ := webhook.New(ctx, database, box)
	s := NewServer(Deps{
		Config: config.Config{DataDir: dir, SessionTTL: time.Hour, S3Listen: ":9000"},
		DB:     database, Auth: authSvc, Objects: objects, Shares: share.New(database, box.DeriveKey("x")),
		Webhooks: hooks, Audit: audit.New(database), Settings: st, Metrics: metrics.New(),
	})
	srv := httptest.NewServer(s.Handler(http.NotFoundHandler()))
	t.Cleanup(srv.Close)
	return &harness{t: t, srv: srv}
}

// client returns an HTTP client with its own cookie jar.
func (h *harness) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

type resp struct {
	status int
	body   []byte
	header http.Header
}

func (r resp) json(v any) { json.Unmarshal(r.body, v) }

func (h *harness) do(c *http.Client, method, path string, body any, hdr ...string) resp {
	h.t.Helper()
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
		ct = "application/json"
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, rd)
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := c.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, data, res.Header}
}

func (h *harness) expect(r resp, status int) resp {
	h.t.Helper()
	if r.status != status {
		h.t.Fatalf("expected %d, got %d: %s", status, r.status, r.body)
	}
	return r
}

func setupAdmin(h *harness) *http.Client {
	c := h.client()
	h.expect(h.do(c, "POST", "/api/v1/setup", map[string]string{"username": "admin", "password": "correct horse battery"}), 201)
	return c
}

func TestObjectsAndPermissions(t *testing.T) {
	h := newHarness(t)
	admin := setupAdmin(h)

	h.expect(h.do(admin, "POST", "/api/v1/buckets", map[string]any{"name": "docs"}), 201)
	h.expect(h.do(admin, "POST", "/api/v1/buckets", map[string]any{"name": "docs"}), 409)
	h.expect(h.do(admin, "POST", "/api/v1/buckets", map[string]any{"name": "Bad_Name"}), 422)

	// Raw PUT upload, metadata, ranged download.
	h.expect(h.do(admin, "PUT", "/api/v1/buckets/docs/objects/a/hello%20world.txt", []byte("hello world"),
		"Content-Type", "text/plain", "X-Acs-Meta-Owner", "me"), 201)
	r := h.expect(h.do(admin, "GET", "/api/v1/buckets/docs/objects/a/hello%20world.txt", nil, "Range", "bytes=6-"), 206)
	if string(r.body) != "world" || !strings.Contains(r.header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatalf("range body %q csp %q", r.body, r.header.Get("Content-Security-Policy"))
	}
	var info objectJSON
	h.expect(h.do(admin, "GET", "/api/v1/buckets/docs/meta/a/hello%20world.txt", nil), 200).json(&info)
	if info.Metadata["owner"] != "me" || info.ContentType != "text/plain" {
		t.Fatalf("info %+v", info)
	}

	// Listing with delimiter.
	var list listResponse
	h.expect(h.do(admin, "GET", "/api/v1/buckets/docs/objects?delimiter=/", nil), 200).json(&list)
	if len(list.Prefixes) != 1 || list.Prefixes[0] != "a/" {
		t.Fatalf("listing %+v", list)
	}

	// Folder move.
	h.expect(h.do(admin, "POST", "/api/v1/buckets/docs/copy", map[string]any{"sourceKey": "a/", "key": "b/", "move": true}), 200)
	h.expect(h.do(admin, "GET", "/api/v1/buckets/docs/objects/b/hello%20world.txt", nil), 200)
	h.expect(h.do(admin, "GET", "/api/v1/buckets/docs/objects/a/hello%20world.txt", nil), 404)

	// Viewer can read but not write.
	h.expect(h.do(admin, "POST", "/api/v1/users", map[string]string{"username": "vic", "password": "viewer password", "role": "viewer"}), 201)
	viewer := h.client()
	h.expect(h.do(viewer, "POST", "/api/v1/auth/login", map[string]string{"username": "vic", "password": "viewer password"}), 200)
	h.expect(h.do(viewer, "GET", "/api/v1/buckets/docs/objects/b/hello%20world.txt", nil), 200)
	h.expect(h.do(viewer, "PUT", "/api/v1/buckets/docs/objects/x.txt", []byte("x")), 403)
	h.expect(h.do(viewer, "GET", "/api/v1/users", nil), 403)

	// Access key scoped to another bucket cannot read docs; Bearer auth works.
	h.expect(h.do(admin, "POST", "/api/v1/buckets", map[string]any{"name": "other"}), 201)
	var key struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	h.expect(h.do(admin, "POST", "/api/v1/keys", map[string]any{"name": "k", "permission": "readwrite", "buckets": []string{"other"}}), 201).json(&key)
	anon := &http.Client{}
	bearer := "Bearer " + key.ID + ":" + key.Secret
	h.expect(h.do(anon, "PUT", "/api/v1/buckets/other/objects/f.txt", []byte("ok"), "Authorization", bearer), 201)
	h.expect(h.do(anon, "GET", "/api/v1/buckets/docs/objects?delimiter=/", nil, "Authorization", bearer), 403)
	var buckets []bucketJSON
	h.expect(h.do(anon, "GET", "/api/v1/buckets", nil, "Authorization", bearer), 200).json(&buckets)
	if len(buckets) != 1 || buckets[0].Name != "other" {
		t.Fatalf("scoped key sees %d buckets", len(buckets))
	}
	h.expect(h.do(anon, "GET", "/api/v1/buckets", nil, "Authorization", "Bearer "+key.ID+":wrong"), 401)

	// CSRF guard: form-encoded POST with a session cookie is rejected.
	h.expect(h.do(admin, "POST", "/api/v1/buckets", []byte("name=evil"), "Content-Type", "application/x-www-form-urlencoded"), 415)

	// Bulk delete a folder, then force-delete the bucket.
	var del struct{ Deleted int }
	h.expect(h.do(admin, "POST", "/api/v1/buckets/docs/delete", map[string]any{"prefixes": []string{"b/"}}), 200).json(&del)
	if del.Deleted != 1 {
		t.Fatalf("bulk deleted %d", del.Deleted)
	}
	h.expect(h.do(admin, "DELETE", "/api/v1/buckets/other", nil), 409)
	h.expect(h.do(admin, "DELETE", "/api/v1/buckets/other?force=true", nil), 204)

	var entries []audit.Entry
	h.expect(h.do(admin, "GET", "/api/v1/audit?search=bucket.delete", nil), 200).json(&entries)
	if len(entries) != 1 {
		t.Fatalf("audit entries %d", len(entries))
	}
}

func TestShareLinks(t *testing.T) {
	h := newHarness(t)
	admin := setupAdmin(h)
	h.expect(h.do(admin, "POST", "/api/v1/buckets", map[string]any{"name": "pub"}), 201)
	h.expect(h.do(admin, "PUT", "/api/v1/buckets/pub/objects/report.pdf", []byte("%PDF-fake")), 201)
	h.expect(h.do(admin, "PUT", "/api/v1/buckets/pub/objects/album/1.jpg", []byte("img1")), 201)

	// File share with password and a download limit of 1.
	var sh shareJSON
	h.expect(h.do(admin, "POST", "/api/v1/shares", map[string]any{
		"type": "file", "bucket": "pub", "key": "report.pdf", "password": "s3cret", "maxDownloads": 1,
	}), 201).json(&sh)
	if !strings.HasSuffix(sh.URL, "/s/"+sh.Token) {
		t.Fatalf("share url %q", sh.URL)
	}
	visitor := h.client()
	base := "/api/v1/public/shares/" + sh.Token
	var pub publicShareResponse
	h.expect(h.do(visitor, "GET", base, nil), 200).json(&pub)
	if !pub.RequiresPassword || pub.Unlocked || pub.File != nil {
		t.Fatalf("locked share leaked info: %+v", pub)
	}
	h.expect(h.do(visitor, "GET", base+"/download?download=1", nil), 401)
	h.expect(h.do(visitor, "POST", base+"/unlock", map[string]string{"password": "nope"}), 401)
	h.expect(h.do(visitor, "POST", base+"/unlock", map[string]string{"password": "s3cret"}), 204)
	r := h.expect(h.do(visitor, "GET", base+"/download?download=1", nil), 200)
	if string(r.body) != "%PDF-fake" || !strings.HasPrefix(r.header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("download %q %q", r.body, r.header.Get("Content-Disposition"))
	}
	h.expect(h.do(visitor, "GET", base+"/download?download=1", nil), 410)

	// Folder share: listing, path traversal rejected, zip.
	h.expect(h.do(admin, "POST", "/api/v1/shares", map[string]any{"type": "folder", "bucket": "pub", "key": "album"}), 201).json(&sh)
	base = "/api/v1/public/shares/" + sh.Token
	var listing struct {
		Files []struct{ Path string } `json:"files"`
	}
	h.expect(h.do(visitor, "GET", base+"/list", nil), 200).json(&listing)
	if len(listing.Files) != 1 || listing.Files[0].Path != "1.jpg" {
		t.Fatalf("folder listing %+v", listing)
	}
	h.expect(h.do(visitor, "GET", base+"/download?path=../report.pdf", nil), 400)
	if r := h.expect(h.do(visitor, "GET", base+"/zip", nil), 200); !bytes.HasPrefix(r.body, []byte("PK")) {
		t.Fatal("zip download is not a zip")
	}

	// Upload link with a size limit; name collisions get a suffix.
	h.expect(h.do(admin, "POST", "/api/v1/shares", map[string]any{"type": "upload", "bucket": "pub", "key": "inbox/", "maxUploadBytes": 10}), 201).json(&sh)
	base = "/api/v1/public/shares/" + sh.Token
	h.expect(h.do(visitor, "PUT", base+"/upload/cv.txt", []byte("my cv")), 201)
	var up struct{ Name string }
	h.expect(h.do(visitor, "PUT", base+"/upload/cv.txt", []byte("my cv 2")), 201).json(&up)
	if up.Name != "cv (1).txt" {
		t.Fatalf("collision name %q", up.Name)
	}
	h.expect(h.do(visitor, "PUT", base+"/upload/big.bin", []byte("this is more than ten bytes")), 413)
	h.expect(h.do(visitor, "PUT", base+"/upload/..%2Fescape.txt", []byte("x")), 400)
	h.expect(h.do(visitor, "GET", base+"/list", nil), 400) // upload links cannot be browsed

	// Disabling a share makes it unavailable.
	h.expect(h.do(admin, "PATCH", "/api/v1/shares/"+strconv.FormatInt(sh.ID, 10), map[string]any{"disabled": true}), 200)
	h.expect(h.do(visitor, "GET", base, nil), 410)

	// Deleting the bucket removes its shares (via the event subscriber in main);
	// here we just check that shares list for the bucket works.
	var shares []shareJSON
	h.expect(h.do(admin, "GET", "/api/v1/shares?bucket=pub", nil), 200).json(&shares)
	if len(shares) != 3 {
		t.Fatalf("shares %d", len(shares))
	}
}
