package replica

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"acs/internal/object"
)

const (
	formatVersion = 1
	// maxBuffered bounds memory for records not yet copied everywhere; a
	// target that falls further behind gets a fresh snapshot instead.
	maxBuffered = 200_000
	// segmentsPerSnapshot triggers compaction of a target's journal.
	segmentsPerSnapshot = 300
	catalogInterval     = 10 * time.Second
	keepCatalogs        = 2
)

// Manifest describes the installation that owns a backend.
type Manifest struct {
	Format       int       `json:"format"`
	Installation string    `json:"installation"`
	SiteName     string    `json:"siteName"`
	Backend      string    `json:"backend"`
	BackendName  string    `json:"backendName"`
	Type         string    `json:"type"`
	LastSeq      uint64    `json:"lastSeq"`
	UpdatedAt    time.Time `json:"updatedAt"`
	// Buckets whose data lives on this backend.
	Buckets    []string `json:"buckets"`
	HasKeyring bool     `json:"hasKeyring"`
}

// TargetStatus reports how current a backend's metadata copy is.
type TargetStatus struct {
	SyncedAt time.Time `json:"syncedAt,omitempty"`
	LastSeq  uint64    `json:"lastSeq"`
	Error    string    `json:"error,omitempty"`
	// Foreign is set when the backend belongs to another installation;
	// nothing is written to it until it is imported.
	Foreign     string `json:"foreign,omitempty"`
	ForeignSite string `json:"foreignSite,omitempty"`
}

type target struct {
	id, name, kind string
	area           Area
	interval       time.Duration

	cursor    uint64
	lastFlush time.Time
	needSnap  bool
	segments  int
	lastSnap  time.Time
	catalog   string // hash of the last catalog written
	keyring   string // hash of the last keyring written
	manifest  time.Time
	status    TargetStatus
}

// Options configure a Replicator.
type Options struct {
	Objects      *object.Service
	DB           *sql.DB
	DataDir      string
	Installation string
	SiteName     func() string
	Keyring      func() []byte
	// OnError is told about write failures and recoveries (err == nil).
	OnError func(backendID, name string, err error)
}

type Replicator struct {
	o Options

	mu      sync.Mutex
	buf     []object.Record
	targets map[string]*target

	catalogMu      sync.Mutex
	catalogChanges int64
	catalogBytes   []byte
	catalogHash    string
	catalogAt      time.Time

	kick chan struct{}
}

func New(o Options) *Replicator {
	return &Replicator{o: o, targets: map[string]*target{}, catalogChanges: -1, kick: make(chan struct{}, 1)}
}

// Add receives a committed batch (object.Service journal hook).
func (r *Replicator) Add(rec object.Record) {
	r.mu.Lock()
	r.buf = append(r.buf, rec)
	if len(r.buf) > maxBuffered {
		// Drop the oldest half; targets that still needed it get a snapshot.
		cut := r.buf[len(r.buf)/2].Seq
		sort.Slice(r.buf, func(i, j int) bool { return r.buf[i].Seq < r.buf[j].Seq })
		keep := r.buf[:0]
		for _, x := range r.buf {
			if x.Seq >= cut {
				keep = append(keep, x)
			}
		}
		r.buf = keep
		for _, t := range r.targets {
			if t.cursor < cut {
				t.needSnap = true
			}
		}
	}
	r.mu.Unlock()
}

// SetTarget registers or replaces a backend. interval is how often its journal
// is written (about 1 s for drives, 5 s for S3 providers).
func (r *Replicator) SetTarget(ctx context.Context, id, name, kind string, area Area, interval time.Duration) {
	t := &target{id: id, name: name, kind: kind, area: area, interval: interval, needSnap: true}
	// The built-in disk (id "") lives in our own data directory, so it is never foreign.
	if m, err := ReadManifest(ctx, area); err == nil && id != "" && m.Installation != "" && m.Installation != r.o.Installation {
		t.status.Foreign, t.status.ForeignSite = m.Installation, m.SiteName
	} else if err == nil && r.cleanResume(m) {
		// Clean shutdown and nothing written since: continue the journal.
		t.cursor, t.needSnap, t.lastSnap = m.LastSeq, false, time.Now()
		t.status.LastSeq, t.status.SyncedAt = m.LastSeq, m.UpdatedAt
	}
	r.mu.Lock()
	if old := r.targets[id]; old != nil && old.area == area {
		r.mu.Unlock()
		return
	}
	r.targets[id] = t
	r.mu.Unlock()
	r.Kick()
}

// RemoveTarget stops replicating to a backend.
func (r *Replicator) RemoveTarget(id string) {
	r.mu.Lock()
	delete(r.targets, id)
	r.mu.Unlock()
}

// Adopt takes ownership of a foreign backend (after its buckets were imported).
func (r *Replicator) Adopt(id string) {
	r.mu.Lock()
	if t := r.targets[id]; t != nil {
		t.status.Foreign, t.status.ForeignSite = "", ""
		t.needSnap = true
	}
	r.mu.Unlock()
	r.Kick()
}

// Status returns the state of every target.
func (r *Replicator) Status() map[string]TargetStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]TargetStatus, len(r.targets))
	for id, t := range r.targets {
		out[id] = t.status
	}
	return out
}

// Kick requests an immediate replication round.
func (r *Replicator) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Run replicates until ctx ends, then performs a final flush.
func (r *Replicator) Run(ctx context.Context) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			r.Flush(context.Background(), true)
			return
		case <-t.C:
		case <-r.kick:
		}
		r.Flush(ctx, false)
	}
}

// Flush runs one replication round. With final, every target is written
// regardless of its interval and the clean-shutdown state is recorded.
func (r *Replicator) Flush(ctx context.Context, final bool) {
	safe := r.o.Objects.SafeSeq()
	r.syncCatalog(ctx, final)

	r.mu.Lock()
	targets := make([]*target, 0, len(r.targets))
	for _, t := range r.targets {
		if t.status.Foreign == "" {
			targets = append(targets, t)
		}
	}
	r.mu.Unlock()

	var snapFile string
	var snapFrom uint64
	defer func() {
		if snapFile != "" {
			os.Remove(snapFile)
		}
	}()
	for _, t := range targets {
		var err error
		if t.needSnap {
			if snapFile == "" {
				snapFile, snapFrom, err = r.exportSnapshot()
			}
			if err == nil {
				err = r.writeSnapshot(ctx, t, snapFile, snapFrom)
			}
		} else if final || time.Since(t.lastFlush) >= t.interval {
			err = r.writeJournal(ctx, t, safe)
		}
		if err == nil {
			err = r.writeCatalog(ctx, t)
		}
		if err == nil && (final || time.Since(t.manifest) > 10*time.Second) {
			err = r.writeManifest(ctx, t)
		}
		r.mu.Lock()
		prev := t.status.Error
		if err != nil {
			t.status.Error = err.Error()
		} else {
			t.status.Error = ""
		}
		r.mu.Unlock()
		if r.o.OnError != nil && (err != nil) != (prev != "") {
			r.o.OnError(t.id, t.name, err)
		}
		if err != nil {
			slog.Warn("metadata replication failed", "backend", t.name, "err", err)
		}
	}
	r.trim()
	if final {
		r.saveCleanState(safe)
	}
}

// trim drops buffered records every target already has.
func (r *Replicator) trim() {
	r.mu.Lock()
	defer r.mu.Unlock()
	min := ^uint64(0)
	for _, t := range r.targets {
		if t.status.Foreign == "" && t.cursor < min {
			min = t.cursor
		}
	}
	keep := r.buf[:0]
	for _, x := range r.buf {
		if x.Seq > min {
			keep = append(keep, x)
		}
	}
	r.buf = keep
}

func (r *Replicator) pending(after, upTo uint64) []object.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []object.Record
	for _, x := range r.buf {
		if x.Seq > after && x.Seq <= upTo {
			out = append(out, x)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out
}

func seqName(n uint64) string { return fmt.Sprintf("%020d", n) }

func (r *Replicator) writeJournal(ctx context.Context, t *target, safe uint64) error {
	recs := r.pending(t.cursor, safe)
	t.lastFlush = time.Now()
	if len(recs) > 0 {
		data := object.EncodeSegment(recs)
		first, last := recs[0].Seq, recs[len(recs)-1].Seq
		if err := putBytes(ctx, t.area, "meta/j-"+seqName(first)+"-"+seqName(last)+".log", data); err != nil {
			return err
		}
	}
	// Everything up to safe is now on this target.
	r.mu.Lock()
	if safe > t.cursor {
		t.cursor = safe
	}
	if len(recs) > 0 {
		t.segments++
	}
	t.status.LastSeq, t.status.SyncedAt = t.cursor, time.Now()
	if t.segments >= segmentsPerSnapshot || (time.Since(t.lastSnap) > time.Hour && t.segments > 20) {
		t.needSnap = true
	}
	r.mu.Unlock()
	return nil
}

func (r *Replicator) exportSnapshot() (string, uint64, error) {
	f, err := os.CreateTemp(r.o.DataDir, ".snapshot-*")
	if err != nil {
		return "", 0, err
	}
	from, err := r.o.Objects.ExportSnapshot(f)
	f.Close()
	if err != nil {
		os.Remove(f.Name())
		return "", 0, err
	}
	return f.Name(), from, nil
}

func (r *Replicator) writeSnapshot(ctx context.Context, t *target, file string, from uint64) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	st, _ := f.Stat()
	err = t.area.Put(ctx, "meta/snap-"+seqName(from)+".gz", f, st.Size())
	f.Close()
	if err != nil {
		return err
	}
	// Drop older snapshots and journal segments the new snapshot covers.
	names, err := t.area.List(ctx, "meta/")
	if err != nil {
		return err
	}
	for _, n := range names {
		base := strings.TrimPrefix(n, "meta/")
		switch {
		case strings.HasPrefix(base, "snap-") && base != "snap-"+seqName(from)+".gz":
			t.area.Delete(ctx, n)
		case strings.HasPrefix(base, "j-"):
			if _, last, ok := segmentRange(base); ok && last <= from {
				t.area.Delete(ctx, n)
			}
		}
	}
	r.mu.Lock()
	t.cursor, t.needSnap, t.segments, t.lastSnap = from, false, 0, time.Now()
	t.status.LastSeq, t.status.SyncedAt = from, time.Now()
	r.mu.Unlock()
	// Records after the snapshot point are written right away.
	return r.writeJournal(ctx, t, r.o.Objects.SafeSeq())
}

// segmentRange parses "j-<first>-<last>.log".
func segmentRange(base string) (uint64, uint64, bool) {
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(base, "j-"), ".log"), "-")
	if len(parts) != 2 {
		return 0, 0, false
	}
	a, err1 := strconv.ParseUint(parts[0], 10, 64)
	b, err2 := strconv.ParseUint(parts[1], 10, 64)
	return a, b, err1 == nil && err2 == nil
}

// syncCatalog refreshes the in-memory catalog copy when SQLite changed.
func (r *Replicator) syncCatalog(ctx context.Context, force bool) {
	r.catalogMu.Lock()
	defer r.catalogMu.Unlock()
	if !force && time.Since(r.catalogAt) < catalogInterval {
		return
	}
	var changes int64
	if err := r.o.DB.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&changes); err != nil {
		return
	}
	if changes == r.catalogChanges && r.catalogBytes != nil {
		r.catalogAt = time.Now()
		return
	}
	tmp := filepath.Join(r.o.DataDir, ".catalog-"+randHex(6)+".db")
	defer os.Remove(tmp)
	if _, err := r.o.DB.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		slog.Warn("catalog copy failed", "err", err)
		return
	}
	raw, err := os.ReadFile(tmp)
	if err != nil {
		return
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(raw)
	zw.Close()
	sum := sha256.Sum256(raw)
	r.catalogBytes, r.catalogHash = buf.Bytes(), hex.EncodeToString(sum[:])
	r.catalogChanges, r.catalogAt = changes, time.Now()
}

func (r *Replicator) writeCatalog(ctx context.Context, t *target) error {
	r.catalogMu.Lock()
	data, hash := r.catalogBytes, r.catalogHash
	r.catalogMu.Unlock()
	if data != nil && hash != t.catalog {
		name := fmt.Sprintf("catalog/catalog-%020d.db.gz", time.Now().UnixNano())
		if err := putBytes(ctx, t.area, name, data); err != nil {
			return err
		}
		names, err := t.area.List(ctx, "catalog/")
		if err == nil && len(names) > keepCatalogs {
			sort.Strings(names)
			for _, n := range names[:len(names)-keepCatalogs] {
				t.area.Delete(ctx, n)
			}
		}
		t.catalog = hash
	}
	if kr := r.o.Keyring(); kr != nil {
		sum := sha256.Sum256(kr)
		if h := hex.EncodeToString(sum[:]); h != t.keyring {
			if err := putBytes(ctx, t.area, "keyring.json", kr); err != nil {
				return err
			}
			t.keyring = h
		}
	}
	return nil
}

func (r *Replicator) writeManifest(ctx context.Context, t *target) error {
	buckets := []string{}
	if all, err := r.o.Objects.ListBuckets(ctx); err == nil {
		for _, b := range all {
			if b.Backend == t.id && !b.Mirror() {
				buckets = append(buckets, b.Name)
			}
		}
	}
	m := Manifest{
		Format: formatVersion, Installation: r.o.Installation, SiteName: r.o.SiteName(),
		Backend: t.id, BackendName: t.name, Type: t.kind, LastSeq: t.cursor, UpdatedAt: time.Now().UTC(),
		Buckets: buckets, HasKeyring: r.o.Keyring() != nil,
	}
	data, _ := json.MarshalIndent(m, "", "  ")
	if err := putBytes(ctx, t.area, "manifest.json", data); err != nil {
		return err
	}
	t.manifest = time.Now()
	return nil
}

// ---- clean shutdown ----

type cleanState struct {
	LastSeq uint64 `json:"lastSeq"`
}

func (r *Replicator) statePath() string { return filepath.Join(r.o.DataDir, "replica-state.json") }

func (r *Replicator) saveCleanState(seq uint64) {
	data, _ := json.Marshal(cleanState{LastSeq: seq})
	os.WriteFile(r.statePath(), data, 0o640)
}

// cleanResume reports whether a target's journal can simply be continued:
// the last run shut down cleanly after writing everything up to m.LastSeq.
func (r *Replicator) cleanResume(m *Manifest) bool {
	data, err := os.ReadFile(r.statePath())
	if err != nil {
		return false
	}
	var st cleanState
	return json.Unmarshal(data, &st) == nil && st.LastSeq == m.LastSeq && m.LastSeq != 0
}

// ForgetCleanState must be called once at startup after targets are set, so a
// crash later in this run forces fresh snapshots on the next start.
func (r *Replicator) ForgetCleanState() { os.Remove(r.statePath()) }

// ---- helpers ----

// ReadManifest reads a backend's manifest.
func ReadManifest(ctx context.Context, a Area) (*Manifest, error) {
	data, err := readAll(ctx, a, "manifest.json")
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	return &m, nil
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// EnsureInstallation returns this installation's ID, creating it once. It is
// stored in the catalog, so a restored installation keeps its identity.
func EnsureInstallation(ctx context.Context, db *sql.DB) (string, error) {
	var id string
	err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'installation_id'`).Scan(&id)
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = "inst_" + randHex(8)
	_, err = db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('installation_id', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, id)
	return id, err
}

// TargetSpec describes a backend to replicate to.
type TargetSpec struct {
	ID, Name, Kind string
	Area           Area
	Interval       time.Duration
}

// Sync makes the target set equal to specs, keeping existing targets' state.
func (r *Replicator) Sync(ctx context.Context, specs []TargetSpec) {
	want := map[string]bool{}
	for _, sp := range specs {
		want[sp.ID] = true
		r.mu.Lock()
		old := r.targets[sp.ID]
		r.mu.Unlock()
		if old != nil && old.name == sp.Name {
			old.area = sp.Area
			continue
		}
		r.SetTarget(ctx, sp.ID, sp.Name, sp.Kind, sp.Area, sp.Interval)
	}
	r.mu.Lock()
	for id := range r.targets {
		if !want[id] {
			delete(r.targets, id)
		}
	}
	r.mu.Unlock()
}

// LoadKeyring returns the stored keyring (JSON), or nil if no recovery
// passphrase has been set.
func LoadKeyring(ctx context.Context, db *sql.DB) []byte {
	var v string
	if err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'keyring'`).Scan(&v); err != nil || v == "" {
		return nil
	}
	return []byte(v)
}

// SaveKeyring stores the keyring in the catalog.
func SaveKeyring(ctx context.Context, db *sql.DB, data []byte) error {
	_, err := db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES ('keyring', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(data))
	return err
}
