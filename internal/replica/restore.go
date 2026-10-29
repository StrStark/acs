package replica

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"acs/internal/object"
	"acs/internal/secret"
)

// Source is the metadata copy found on one backend.
type Source struct {
	Manifest *Manifest
	Keyring  *secret.Keyring
	// Catalog is the newest SQLite catalog (uncompressed), if any.
	Catalog []byte
	// Snapshot is the newest object-index snapshot (gzip), if any.
	Snapshot []byte
	Records  []object.Record
}

// ReadSource loads everything needed to restore from a backend's area.
func ReadSource(ctx context.Context, a Area) (*Source, error) {
	m, err := ReadManifest(ctx, a)
	if err != nil {
		if errors.Is(err, ErrNotExist) {
			return nil, errors.New("no ACS metadata found on this storage")
		}
		return nil, err
	}
	src := &Source{Manifest: m}
	if data, err := readAll(ctx, a, "keyring.json"); err == nil {
		if src.Keyring, err = secret.ParseKeyring(data); err != nil {
			return nil, fmt.Errorf("keyring: %w", err)
		}
	}

	if names, err := a.List(ctx, "catalog/"); err != nil {
		return nil, err
	} else if len(names) > 0 {
		sort.Strings(names)
		gz, err := readAll(ctx, a, names[len(names)-1])
		if err != nil {
			return nil, err
		}
		zr, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			return nil, fmt.Errorf("catalog: %w", err)
		}
		if src.Catalog, err = io.ReadAll(zr); err != nil {
			return nil, fmt.Errorf("catalog: %w", err)
		}
	}

	names, err := a.List(ctx, "meta/")
	if err != nil {
		return nil, err
	}
	var snapName string
	var from uint64
	for _, n := range names {
		base := strings.TrimPrefix(n, "meta/")
		if strings.HasPrefix(base, "snap-") && n > snapName {
			snapName = n
			fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(base, "snap-"), ".gz"), "%d", &from)
		}
	}
	if snapName != "" {
		if src.Snapshot, err = readAll(ctx, a, snapName); err != nil {
			return nil, err
		}
	}
	for _, n := range names {
		base := strings.TrimPrefix(n, "meta/")
		if !strings.HasPrefix(base, "j-") {
			continue
		}
		if _, last, ok := segmentRange(base); !ok || last <= from {
			continue
		}
		data, err := readAll(ctx, a, n)
		if err != nil {
			return nil, err
		}
		recs, err := object.DecodeSegment(data)
		if err != nil {
			continue // skip a damaged segment; later ones still apply
		}
		src.Records = append(src.Records, recs...)
	}
	return src, nil
}

// ---- staged restore ----
//
// Restoring replaces the catalog database and the object index, which are
// open while the server runs. The panel therefore stages a restore in
// <data>/restore/ and restarts the process; ApplyStaged installs it before
// anything is opened.

const stageDir = "restore"

type stageInfo struct {
	Installation string    `json:"installation"`
	SiteName     string    `json:"siteName"`
	FromBackend  string    `json:"fromBackend"`
	StagedAt     time.Time `json:"stagedAt"`
	HasSecrets   bool      `json:"hasSecrets"`
}

// Stage prepares a restore from src. With a keyring, passphrase must unlock
// it; without one, secrets (access keys, provider credentials) are lost.
func Stage(dataDir string, src *Source, passphrase string) error {
	if src.Catalog == nil {
		return errors.New("this storage has no catalog copy yet; it cannot be restored from")
	}
	var key []byte
	if src.Keyring != nil {
		var err error
		if key, err = src.Keyring.Unlock(passphrase); err != nil {
			return err
		}
	}
	dir := filepath.Join(dataDir, stageDir)
	os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	write := func(name string, data []byte) error {
		return os.WriteFile(filepath.Join(dir, name), data, 0o640)
	}
	if key != nil {
		if err := secret.WriteKeyFile(filepath.Join(dir, "master.key"), key); err != nil {
			return err
		}
	}
	if err := write("catalog.db", src.Catalog); err != nil {
		return err
	}
	if src.Snapshot != nil {
		if err := write("snapshot.gz", src.Snapshot); err != nil {
			return err
		}
	}
	if err := write("journal.log", object.EncodeSegment(src.Records)); err != nil {
		return err
	}
	info, _ := json.Marshal(stageInfo{
		Installation: src.Manifest.Installation, SiteName: src.Manifest.SiteName,
		FromBackend: src.Manifest.BackendName, StagedAt: time.Now().UTC(), HasSecrets: key != nil,
	})
	return write("ready.json", info) // written last: marks the stage complete
}

// ApplyStaged installs a staged restore, moving the previous state aside to
// <data>/pre-restore-<time>/. It reports whether a restore was applied.
func ApplyStaged(dataDir string) (bool, error) {
	dir := filepath.Join(dataDir, stageDir)
	if _, err := os.Stat(filepath.Join(dir, "ready.json")); err != nil {
		return false, nil
	}
	backup := filepath.Join(dataDir, "pre-restore-"+time.Now().UTC().Format("20060102-150405"))
	if err := os.MkdirAll(backup, 0o750); err != nil {
		return false, err
	}
	for _, name := range []string{"acs.db", "acs.db-wal", "acs.db-shm", "meta", "replica-state.json"} {
		if _, err := os.Stat(filepath.Join(dataDir, name)); err == nil {
			if err := os.Rename(filepath.Join(dataDir, name), filepath.Join(backup, name)); err != nil {
				return false, err
			}
		}
	}
	// The built-in disk's metadata copy describes the replaced installation.
	if _, err := os.Stat(filepath.Join(dataDir, "objects", ".acs")); err == nil {
		if err := os.Rename(filepath.Join(dataDir, "objects", ".acs"), filepath.Join(backup, "objects.acs")); err != nil {
			return false, err
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "master.key")); err == nil {
		if _, err := os.Stat(filepath.Join(dataDir, "master.key")); err == nil {
			os.Rename(filepath.Join(dataDir, "master.key"), filepath.Join(backup, "master.key"))
		}
		if err := os.Rename(filepath.Join(dir, "master.key"), filepath.Join(dataDir, "master.key")); err != nil {
			return false, err
		}
	}
	if err := os.Rename(filepath.Join(dir, "catalog.db"), filepath.Join(dataDir, "acs.db")); err != nil {
		return false, err
	}

	var snap io.Reader
	if data, err := os.ReadFile(filepath.Join(dir, "snapshot.gz")); err == nil {
		snap = bytes.NewReader(data)
	}
	var recs []object.Record
	if data, err := os.ReadFile(filepath.Join(dir, "journal.log")); err == nil {
		recs, _ = object.DecodeSegment(data)
	}
	metaDir := filepath.Join(dataDir, "meta")
	if err := os.MkdirAll(metaDir, 0o750); err != nil {
		return false, err
	}
	if _, err := object.RestoreState(metaDir, snap, recs); err != nil {
		return false, err
	}
	// Mark the restore so the server runs an integrity check after starting.
	os.WriteFile(filepath.Join(dataDir, "restored.json"), mustRead(filepath.Join(dir, "ready.json")), 0o640)
	return true, os.RemoveAll(dir)
}

func mustRead(path string) []byte {
	data, _ := os.ReadFile(path)
	return data
}

// TakeRestoredMarker reports (once) whether this start follows a restore.
func TakeRestoredMarker(dataDir string) bool {
	p := filepath.Join(dataDir, "restored.json")
	if _, err := os.Stat(p); err != nil {
		return false
	}
	os.Remove(p)
	return true
}

// ---- discovery ----

// Found is ACS storage discovered in a directory.
type Found struct {
	Path string `json:"path"`
	// Manifest is nil for drives without a metadata copy.
	Manifest *Manifest `json:"manifest,omitempty"`
	// Marker is the backend ID from the drive's marker file, if any.
	Marker string `json:"marker,omitempty"`
}

// Scan looks for ACS drives in root and its immediate subdirectories.
func Scan(root string) []Found {
	var out []Found
	dirs := []string{root}
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				dirs = append(dirs, filepath.Join(root, e.Name()))
			}
		}
	}
	for _, dir := range dirs {
		f := Found{Path: dir}
		if m, err := ReadManifest(context.Background(), NewLocalArea(filepath.Join(dir, ".acs"))); err == nil {
			f.Manifest = m
		}
		if data, err := os.ReadFile(filepath.Join(dir, ".acs-backend")); err == nil {
			f.Marker = strings.TrimSpace(string(data))
		}
		if f.Manifest != nil || f.Marker != "" {
			out = append(out, f)
		}
	}
	return out
}
