// Package blob stores immutable object data as files on local disk.
//
// A blob is written to tmp/, fsynced, then atomically renamed into
// blobs/<aa>/<bb>/<id>. Callers commit metadata only after Commit returns, so
// a crash can leave an orphaned blob but never metadata pointing at missing data.
package blob

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

var ErrNotFound = errors.New("blob not found")

type Store struct {
	root string
}

// Open prepares the blob directories under root and clears leftover temp files.
func Open(root string) (*Store, error) {
	for _, d := range []string{"blobs", "tmp"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			return nil, err
		}
	}
	s := &Store{root: root}
	// Anything in tmp/ belongs to an upload interrupted by a crash or restart.
	entries, err := os.ReadDir(filepath.Join(root, "tmp"))
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		os.Remove(filepath.Join(root, "tmp", e.Name()))
	}
	return s, nil
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (s *Store) path(id string) string {
	return filepath.Join(s.root, "blobs", id[0:2], id[2:4], id)
}

// Writer streams a new blob to disk while computing its MD5.
type Writer struct {
	s    *Store
	f    *os.File
	md5  hash.Hash
	size int64
	done bool
}

// Create starts a new blob.
func (s *Store) Create() (*Writer, error) {
	f, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "up-*")
	if err != nil {
		return nil, err
	}
	return &Writer{s: s, f: f, md5: md5.New()}, nil
}

func (w *Writer) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	w.md5.Write(p[:n])
	w.size += int64(n)
	return n, err
}

// Size returns the number of bytes written so far.
func (w *Writer) Size() int64 { return w.size }

// Commit makes the blob durable and returns its ID and MD5 digest.
func (w *Writer) Commit() (id string, sum []byte, err error) {
	if w.done {
		return "", nil, errors.New("blob writer already finished")
	}
	w.done = true
	tmp := w.f.Name()
	defer func() {
		if err != nil {
			os.Remove(tmp)
		}
	}()
	if err = w.f.Sync(); err != nil {
		w.f.Close()
		return "", nil, err
	}
	if err = w.f.Close(); err != nil {
		return "", nil, err
	}
	id = newID()
	dst := w.s.path(id)
	if err = os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return "", nil, err
	}
	if err = os.Rename(tmp, dst); err != nil {
		return "", nil, err
	}
	return id, w.md5.Sum(nil), nil
}

// Abort discards the blob. It is safe to call after Commit (no-op).
func (w *Writer) Abort() {
	if w.done {
		return
	}
	w.done = true
	w.f.Close()
	os.Remove(w.f.Name())
}

// Open opens a blob for reading.
func (s *Store) Open(id string) (*os.File, error) {
	if !validID(id) {
		return nil, ErrNotFound
	}
	f, err := os.Open(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return f, err
}

// Delete removes a blob. Deleting a missing blob is not an error.
func (s *Store) Delete(id string) error {
	if !validID(id) {
		return fmt.Errorf("invalid blob id %q", id)
	}
	err := os.Remove(s.path(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// Walk calls fn for every committed blob. Used by the orphan scrubber.
func (s *Store) Walk(fn func(id string, modTime time.Time) error) error {
	return filepath.WalkDir(filepath.Join(s.root, "blobs"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !validID(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil // removed concurrently
		}
		return fn(d.Name(), info.ModTime())
	})
}

// CopyFrom is a convenience that writes all of r into a new blob.
func (s *Store) CopyFrom(r io.Reader) (id string, size int64, sum []byte, err error) {
	w, err := s.Create()
	if err != nil {
		return "", 0, nil, err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Abort()
		return "", 0, nil, err
	}
	id, sum, err = w.Commit()
	return id, w.size, sum, err
}
