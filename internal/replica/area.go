// Package replica keeps a complete, self-describing copy of ACS's metadata on
// every storage backend, so any one drive or provider is enough to rebuild an
// installation on a new machine.
//
// Layout of the ".acs" area on each backend:
//
//	manifest.json                 who owns this backend, last sequence, buckets
//	keyring.json                  master key locked with the recovery passphrase
//	catalog/catalog-<ts>.db.gz    SQLite catalog: users, keys, shares, settings, backends
//	meta/snap-<seq>.gz            full object index at <seq>
//	meta/j-<first>-<last>.log     journal records committed after the snapshot
package replica

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ErrNotExist is returned by Area.Get for missing files.
var ErrNotExist = errors.New("file does not exist")

// Area is a backend's ".acs" metadata area. Names use forward slashes.
type Area interface {
	Put(ctx context.Context, name string, r io.Reader, size int64) error
	Get(ctx context.Context, name string) (io.ReadCloser, error)
	List(ctx context.Context, prefix string) ([]string, error)
	Delete(ctx context.Context, name string) error
}

// LocalArea is a metadata area in a directory (the ".acs" folder of a drive).
type LocalArea struct{ root string }

func NewLocalArea(root string) *LocalArea { return &LocalArea{root: root} }

func (a *LocalArea) path(name string) string { return filepath.Join(a.root, filepath.FromSlash(name)) }

func (a *LocalArea) Put(ctx context.Context, name string, r io.Reader, size int64) error {
	dst := a.path(name)
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(f.Name())
		return err
	}
	f.Close()
	if err := os.Rename(f.Name(), dst); err != nil {
		os.Remove(f.Name())
		return err
	}
	// Make the rename durable.
	if d, err := os.Open(filepath.Dir(dst)); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

func (a *LocalArea) Get(ctx context.Context, name string) (io.ReadCloser, error) {
	f, err := os.Open(a.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotExist
	}
	return f, err
}

func (a *LocalArea) List(ctx context.Context, prefix string) ([]string, error) {
	dir := a.path(prefix)
	if !strings.HasSuffix(prefix, "/") && prefix != "" {
		dir = filepath.Dir(dir)
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".tmp-") {
			continue
		}
		rel, _ := filepath.Rel(a.root, filepath.Join(dir, e.Name()))
		name := filepath.ToSlash(rel)
		if strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (a *LocalArea) Delete(ctx context.Context, name string) error {
	err := os.Remove(a.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func readAll(ctx context.Context, a Area, name string) ([]byte, error) {
	rc, err := a.Get(ctx, name)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func putBytes(ctx context.Context, a Area, name string, data []byte) error {
	return a.Put(ctx, name, strings.NewReader(string(data)), int64(len(data)))
}
