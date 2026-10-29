// Package storage manages storage backends: attached drives (directories on
// any mounted disk) and remote S3-compatible providers. It implements
// object.Backends so buckets can keep their data on any of them.
package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"acs/internal/blob"
	"acs/internal/object"
	"acs/internal/remote"
	"acs/internal/secret"
	"acs/internal/sysinfo"
)

const (
	TypeLocal = "local"
	TypeS3    = "s3"
)

// markerFile identifies a directory as belonging to a backend, so one drive
// cannot be attached twice or swapped silently.
const markerFile = ".acs-backend"

var (
	ErrNotFound = errors.New("storage backend not found")
	ErrInUse    = errors.New("storage backend is used by one or more buckets")
)

type ValidationError struct{ Field, Message string }

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

// Backend is a configured storage backend. Secrets are never returned.
type Backend struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"createdAt"`

	// Drive settings.
	Path string `json:"path,omitempty"`

	// S3 settings.
	Provider    string `json:"provider,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	Region      string `json:"region,omitempty"`
	Bucket      string `json:"bucket,omitempty"`
	Prefix      string `json:"prefix,omitempty"`
	AccessKey   string `json:"accessKey,omitempty"`
	PathStyle   bool   `json:"pathStyle,omitempty"`
	InsecureTLS bool   `json:"insecureTls,omitempty"`

	Status Status `json:"status"`
}

type Status struct {
	Online    bool      `json:"online"`
	Error     string    `json:"error,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
	// Disk usage, for drives.
	TotalBytes uint64 `json:"totalBytes,omitempty"`
	FreeBytes  uint64 `json:"freeBytes,omitempty"`
}

// config is the JSON stored in the config column.
type config struct {
	Path        string `json:"path,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	Region      string `json:"region,omitempty"`
	Bucket      string `json:"bucket,omitempty"`
	Prefix      string `json:"prefix,omitempty"`
	AccessKey   string `json:"accessKey,omitempty"`
	PathStyle   bool   `json:"pathStyle,omitempty"`
	InsecureTLS bool   `json:"insecureTls,omitempty"`
}

// runtime is a live backend.
type runtime struct {
	meta   Backend
	store  blob.Store
	remote *remote.Backend // nil for drives
	status Status
}

type Manager struct {
	db      *sql.DB
	box     *secret.Box
	dataDir string

	mu       sync.RWMutex
	backends map[string]*runtime
}

// Open loads all backends. A backend that fails to start stays listed with
// an error status so it can be fixed from the panel.
func Open(ctx context.Context, db *sql.DB, box *secret.Box, dataDir string) (*Manager, error) {
	m := &Manager{db: db, box: box, dataDir: dataDir, backends: map[string]*runtime{}}
	rows, err := db.QueryContext(ctx, `SELECT id, name, type, config, secret_enc, created_at FROM storage_backends`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var b Backend
		var cfgJSON string
		var enc []byte
		var created int64
		if err := rows.Scan(&b.ID, &b.Name, &b.Type, &cfgJSON, &enc, &created); err != nil {
			return nil, err
		}
		b.CreatedAt = time.Unix(created, 0)
		var c config
		json.Unmarshal([]byte(cfgJSON), &c)
		applyConfig(&b, c)
		secretKey := ""
		if len(enc) > 0 {
			sk, err := box.Open(enc)
			if err != nil {
				return nil, fmt.Errorf("decrypt secret of backend %s: %w", b.ID, err)
			}
			secretKey = string(sk)
		}
		rt := &runtime{meta: b}
		if err := m.start(rt, secretKey, false); err != nil {
			rt.status = Status{Error: err.Error(), CheckedAt: time.Now()}
			slog.Warn("storage backend unavailable", "backend", b.Name, "err", err)
		}
		m.backends[b.ID] = rt
	}
	return m, rows.Err()
}

func applyConfig(b *Backend, c config) {
	b.Path, b.Provider, b.Endpoint, b.Region = c.Path, c.Provider, c.Endpoint, c.Region
	b.Bucket, b.Prefix, b.AccessKey, b.PathStyle, b.InsecureTLS = c.Bucket, c.Prefix, c.AccessKey, c.PathStyle, c.InsecureTLS
}

func toConfig(b Backend) config {
	return config{Path: b.Path, Provider: b.Provider, Endpoint: b.Endpoint, Region: b.Region, Bucket: b.Bucket,
		Prefix: b.Prefix, AccessKey: b.AccessKey, PathStyle: b.PathStyle, InsecureTLS: b.InsecureTLS}
}

// start builds the store for rt. With claim, a drive's marker file is created.
func (m *Manager) start(rt *runtime, secretKey string, claim bool) error {
	switch rt.meta.Type {
	case TypeLocal:
		if err := m.checkDrive(rt.meta.ID, rt.meta.Path, claim); err != nil {
			return err
		}
		st, err := blob.OpenLocal(rt.meta.Path)
		if err != nil {
			return err
		}
		rt.store = st
	case TypeS3:
		rb, err := remote.New(remote.Config{
			Endpoint: rt.meta.Endpoint, Region: rt.meta.Region, Bucket: rt.meta.Bucket, Prefix: rt.meta.Prefix,
			AccessKey: rt.meta.AccessKey, SecretKey: secretKey, PathStyle: rt.meta.PathStyle,
			InsecureTLS: rt.meta.InsecureTLS, TempDir: filepath.Join(m.dataDir, "objects", "tmp"),
		})
		if err != nil {
			return err
		}
		rt.remote, rt.store = rb, rb.Store()
	default:
		return fmt.Errorf("unknown backend type %q", rt.meta.Type)
	}
	return nil
}

// checkDrive validates a drive directory and its marker file.
func (m *Manager) checkDrive(id, dir string, claim bool) error {
	if !filepath.IsAbs(dir) {
		return &ValidationError{"path", "must be an absolute path"}
	}
	st, err := os.Stat(dir)
	if err != nil {
		return &ValidationError{"path", "does not exist (mount the drive into the container first)"}
	}
	if !st.IsDir() {
		return &ValidationError{"path", "is not a directory"}
	}
	if abs, _ := filepath.Abs(m.dataDir); filepath.Clean(dir) == filepath.Clean(abs) {
		return &ValidationError{"path", "cannot be the ACS data directory itself"}
	}
	marker := filepath.Join(dir, markerFile)
	owner, err := os.ReadFile(marker)
	switch {
	case err == nil && strings.TrimSpace(string(owner)) != id:
		return &ValidationError{"path", "this directory already belongs to another ACS storage backend"}
	case err == nil:
		return nil
	case !claim:
		return errors.New("drive marker is missing; is the right drive mounted at " + dir + "?")
	}
	if err := os.WriteFile(marker, []byte(id+"\n"), 0o640); err != nil {
		return &ValidationError{"path", "is not writable by ACS (" + err.Error() + ")"}
	}
	return nil
}

// ---- object.Backends ----

func (m *Manager) get(id string) (*runtime, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rt := m.backends[id]
	if rt == nil {
		return nil, fmt.Errorf("%w: unknown storage backend %q", object.ErrBackend, id)
	}
	if rt.store == nil {
		return nil, fmt.Errorf("%w: storage backend %q is unavailable: %s", object.ErrBackend, rt.meta.Name, rt.status.Error)
	}
	return rt, nil
}

func (m *Manager) Store(id string) (blob.Store, error) {
	rt, err := m.get(id)
	if err != nil {
		return nil, err
	}
	return rt.store, nil
}

func (m *Manager) Mirror(id, prefix string) (object.Remote, error) {
	rt, err := m.get(id)
	if err != nil {
		return nil, err
	}
	if rt.remote == nil {
		return nil, fmt.Errorf("%w: mirror buckets need an S3 backend, %q is a drive", object.ErrInvalidArgument, rt.meta.Name)
	}
	return rt.remote.View(prefix), nil
}

func (m *Manager) StoreIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.backends))
	for id, rt := range m.backends {
		if rt.store != nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// ---- management ----

func (m *Manager) snapshot(rt *runtime) Backend {
	b := rt.meta
	b.Status = rt.status
	return b
}

// List returns all backends sorted by name.
func (m *Manager) List() []Backend {
	m.mu.RLock()
	out := make([]Backend, 0, len(m.backends))
	for _, rt := range m.backends {
		out = append(out, m.snapshot(rt))
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

func (m *Manager) Get(id string) (Backend, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	rt := m.backends[id]
	if rt == nil {
		return Backend{}, ErrNotFound
	}
	return m.snapshot(rt), nil
}

// Input creates or updates a backend. SecretKey "" on update keeps the old one.
type Input struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Path        string `json:"path"`
	Provider    string `json:"provider"`
	Endpoint    string `json:"endpoint"`
	Region      string `json:"region"`
	Bucket      string `json:"bucket"`
	Prefix      string `json:"prefix"`
	AccessKey   string `json:"accessKey"`
	SecretKey   string `json:"secretKey"`
	PathStyle   bool   `json:"pathStyle"`
	InsecureTLS bool   `json:"insecureTls"`
}

var bucketNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.\-_]{1,254}$`)

func (in *Input) validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 80 {
		return &ValidationError{"name", "is required (max 80 characters)"}
	}
	switch in.Type {
	case TypeLocal:
		in.Path = filepath.Clean(strings.TrimSpace(in.Path))
	case TypeS3:
		in.Endpoint = strings.TrimRight(strings.TrimSpace(in.Endpoint), "/")
		if in.Endpoint == "" {
			return &ValidationError{"endpoint", "is required"}
		}
		if !strings.Contains(in.Endpoint, "://") {
			in.Endpoint = "https://" + in.Endpoint
		}
		in.Bucket = strings.TrimSpace(in.Bucket)
		if !bucketNameRe.MatchString(in.Bucket) {
			return &ValidationError{"bucket", "enter the name of an existing bucket at the provider"}
		}
		in.Prefix = strings.Trim(strings.TrimSpace(in.Prefix), "/")
		if in.Prefix != "" {
			in.Prefix += "/"
		}
		if in.AccessKey == "" {
			return &ValidationError{"accessKey", "is required"}
		}
	default:
		return &ValidationError{"type", "must be local or s3"}
	}
	return nil
}

func newID() string {
	b := make([]byte, 6)
	rand.Read(b)
	return "bk_" + hex.EncodeToString(b)
}

// Test validates input and checks connectivity without saving anything.
func (m *Manager) Test(ctx context.Context, in Input) error {
	if err := in.validate(); err != nil {
		return err
	}
	if in.Type == TypeLocal {
		if err := m.checkDrive("test", in.Path, false); err != nil {
			var ve *ValidationError
			if errors.As(err, &ve) {
				return err
			}
			// Missing marker is fine for a new drive; check writability instead.
			f, err := os.CreateTemp(in.Path, ".acs-probe-*")
			if err != nil {
				return &ValidationError{"path", "is not writable by ACS (" + err.Error() + ")"}
			}
			f.Close()
			os.Remove(f.Name())
		}
		return nil
	}
	if in.SecretKey == "" {
		return &ValidationError{"secretKey", "is required"}
	}
	rb, err := remote.New(remote.Config{Endpoint: in.Endpoint, Region: in.Region, Bucket: in.Bucket, Prefix: in.Prefix,
		AccessKey: in.AccessKey, SecretKey: in.SecretKey, PathStyle: in.PathStyle, InsecureTLS: in.InsecureTLS})
	if err != nil {
		return &ValidationError{"endpoint", err.Error()}
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return rb.Check(cctx)
}

// Create validates, tests and saves a new backend.
func (m *Manager) Create(ctx context.Context, in Input) (Backend, error) {
	if err := in.validate(); err != nil {
		return Backend{}, err
	}
	if in.Type == TypeS3 {
		if err := m.Test(ctx, in); err != nil {
			return Backend{}, err
		}
	}
	b := Backend{ID: newID(), Name: in.Name, Type: in.Type, CreatedAt: time.Now().Truncate(time.Second),
		Path: in.Path, Provider: in.Provider, Endpoint: in.Endpoint, Region: in.Region, Bucket: in.Bucket,
		Prefix: in.Prefix, AccessKey: in.AccessKey, PathStyle: in.PathStyle, InsecureTLS: in.InsecureTLS}
	rt := &runtime{meta: b}
	if err := m.start(rt, in.SecretKey, true); err != nil {
		return Backend{}, err
	}
	cfgJSON, _ := json.Marshal(toConfig(b))
	var enc []byte
	if in.SecretKey != "" {
		enc = m.box.Seal([]byte(in.SecretKey))
	}
	if _, err := m.db.ExecContext(ctx,
		`INSERT INTO storage_backends (id, name, type, config, secret_enc, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		b.ID, b.Name, b.Type, string(cfgJSON), enc, b.CreatedAt.Unix()); err != nil {
		if b.Type == TypeLocal {
			os.Remove(filepath.Join(b.Path, markerFile))
		}
		return Backend{}, err
	}
	m.mu.Lock()
	m.backends[b.ID] = rt
	m.mu.Unlock()
	m.check(ctx, rt)
	return m.Get(b.ID)
}

// Update changes a backend's name and, for S3, its connection settings.
// A drive's path and an S3 backend's bucket/prefix cannot change, because
// existing data is stored there.
func (m *Manager) Update(ctx context.Context, id string, in Input) (Backend, error) {
	m.mu.RLock()
	rt := m.backends[id]
	m.mu.RUnlock()
	if rt == nil {
		return Backend{}, ErrNotFound
	}
	old := rt.meta
	in.Type = old.Type
	if old.Type == TypeLocal {
		in.Path = old.Path
	} else {
		in.Bucket, in.Prefix = old.Bucket, strings.TrimSuffix(old.Prefix, "/")
	}
	if err := in.validate(); err != nil {
		return Backend{}, err
	}

	secretKey := in.SecretKey
	var enc []byte
	if err := m.db.QueryRowContext(ctx, `SELECT secret_enc FROM storage_backends WHERE id = ?`, id).Scan(&enc); err != nil {
		return Backend{}, err
	}
	if old.Type == TypeS3 {
		if secretKey == "" {
			sk, err := m.box.Open(enc)
			if err != nil {
				return Backend{}, err
			}
			secretKey = string(sk)
		} else {
			enc = m.box.Seal([]byte(secretKey))
		}
		in.SecretKey = secretKey
		if err := m.Test(ctx, in); err != nil {
			return Backend{}, err
		}
	}

	b := old
	b.Name, b.Provider, b.Endpoint, b.Region = in.Name, in.Provider, in.Endpoint, in.Region
	b.AccessKey, b.PathStyle, b.InsecureTLS = in.AccessKey, in.PathStyle, in.InsecureTLS
	next := &runtime{meta: b}
	if err := m.start(next, secretKey, false); err != nil {
		return Backend{}, err
	}
	cfgJSON, _ := json.Marshal(toConfig(b))
	if _, err := m.db.ExecContext(ctx, `UPDATE storage_backends SET name = ?, config = ?, secret_enc = ? WHERE id = ?`,
		b.Name, string(cfgJSON), enc, id); err != nil {
		return Backend{}, err
	}
	m.mu.Lock()
	m.backends[id] = next
	m.mu.Unlock()
	m.check(ctx, next)
	return m.Get(id)
}

// Delete removes a backend that no bucket uses. Data already written to it
// is left in place.
func (m *Manager) Delete(ctx context.Context, id string, inUse bool) error {
	m.mu.RLock()
	rt := m.backends[id]
	m.mu.RUnlock()
	if rt == nil {
		return ErrNotFound
	}
	if inUse {
		return ErrInUse
	}
	if _, err := m.db.ExecContext(ctx, `DELETE FROM storage_backends WHERE id = ?`, id); err != nil {
		return err
	}
	if rt.meta.Type == TypeLocal {
		os.Remove(filepath.Join(rt.meta.Path, markerFile))
	}
	m.mu.Lock()
	delete(m.backends, id)
	m.mu.Unlock()
	return nil
}

// Check refreshes the health status of one backend.
func (m *Manager) Check(ctx context.Context, id string) (Backend, error) {
	m.mu.RLock()
	rt := m.backends[id]
	m.mu.RUnlock()
	if rt == nil {
		return Backend{}, ErrNotFound
	}
	m.check(ctx, rt)
	return m.Get(id)
}

func (m *Manager) check(ctx context.Context, rt *runtime) {
	st := Status{CheckedAt: time.Now()}
	var err error
	switch {
	case rt.store == nil:
		err = errors.New(rt.status.Error)
		if rt.status.Error == "" {
			err = errors.New("backend failed to start")
		}
	case rt.meta.Type == TypeLocal:
		if err = m.checkDrive(rt.meta.ID, rt.meta.Path, false); err == nil {
			var du sysinfo.DiskUsage
			if du, err = sysinfo.Disk(rt.meta.Path); err == nil {
				st.TotalBytes, st.FreeBytes = du.Total, du.Free
			}
		}
	default:
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = rt.remote.Check(cctx)
		cancel()
	}
	st.Online = err == nil
	if err != nil {
		st.Error = err.Error()
	}
	m.mu.Lock()
	rt.status = st
	m.mu.Unlock()
}

// Run re-checks every backend periodically until ctx ends.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		m.mu.RLock()
		rts := make([]*runtime, 0, len(m.backends))
		for _, rt := range m.backends {
			rts = append(rts, rt)
		}
		m.mu.RUnlock()
		for _, rt := range rts {
			m.check(ctx, rt)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
