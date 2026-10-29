// Package blob stores immutable object data. Store is implemented by Local
// (files on an attached drive) and by remote S3 backends.
//
// Local writes a blob to tmp/, fsyncs it, then atomically renames it into
// blobs/<aa>/<bb>/<id>. Callers commit metadata only after Put returns, so a
// crash can leave an orphaned blob but never metadata pointing at missing data.
package blob

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

var ErrNotFound = errors.New("blob not found")

// Store holds immutable blobs addressed by random IDs.
type Store interface {
	// Put stores all of r as a new blob and returns its ID, size and MD5.
	Put(ctx context.Context, r io.Reader) (id string, size int64, sum []byte, err error)
	// Open streams a blob starting at offset.
	Open(ctx context.Context, id string, offset int64) (io.ReadCloser, error)
	// Delete removes a blob; deleting a missing blob is not an error.
	Delete(ctx context.Context, id string) error
	// Walk calls fn for every stored blob (used by the orphan scrubber).
	Walk(ctx context.Context, fn func(id string, modTime time.Time) error) error
}

// NewID returns a random blob ID.
func NewID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ValidID reports whether id looks like a blob ID.
func ValidID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// Local stores blobs as files under a root directory.
type Local struct {
	root string
}

// OpenLocal prepares the blob directories under root and clears leftover temp files.
func OpenLocal(root string) (*Local, error) {
	for _, d := range []string{"blobs", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			return nil, err
		}
	}
	// Anything in tmp/ belongs to an upload interrupted by a crash or restart.
	entries, err := os.ReadDir(filepath.Join(root, "tmp"))
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		os.Remove(filepath.Join(root, "tmp", e.Name()))
	}
	return &Local{root: root}, nil
}

func (s *Local) path(id string) string {
	return filepath.Join(s.root, "blobs", id[0:2], id[2:4], id)
}

func (s *Local) Put(ctx context.Context, r io.Reader) (id string, size int64, sum []byte, err error) {
	f, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "up-*")
	if err != nil {
		return "", 0, nil, err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
		}
	}()
	h := md5.New()
	if size, err = io.Copy(io.MultiWriter(f, h), r); err != nil {
		return "", 0, nil, err
	}
	if err = f.Sync(); err != nil {
		return "", 0, nil, err
	}
	if err = f.Close(); err != nil {
		return "", 0, nil, err
	}
	id = NewID()
	dst := s.path(id)
	if err = os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return "", 0, nil, err
	}
	if err = os.Rename(tmp, dst); err != nil {
		return "", 0, nil, err
	}
	return id, size, h.Sum(nil), nil
}

func (s *Local) Open(ctx context.Context, id string, offset int64) (io.ReadCloser, error) {
	if !ValidID(id) {
		return nil, ErrNotFound
	}
	f, err := os.Open(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

func (s *Local) Delete(ctx context.Context, id string) error {
	if !ValidID(id) {
		return fmt.Errorf("invalid blob id %q", id)
	}
	err := os.Remove(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Local) Walk(ctx context.Context, fn func(id string, modTime time.Time) error) error {
	return filepath.WalkDir(filepath.Join(s.root, "blobs"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() || !ValidID(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // removed concurrently
		}
		return fn(d.Name(), info.ModTime())
	})
}
