package object

import (
	"bytes"
	"errors"
	"io"
	"os"

	"acs/internal/blob"
)

type ReadSeekCloser interface {
	io.ReadSeeker
	io.Closer
}

// reader presents an object's inline data or blob parts as one seekable stream.
// Part files are opened lazily, so a reader holds at most one descriptor.
type reader struct {
	blobs *blob.Store
	o     *Object
	inl   *bytes.Reader
	pos   int64
	// open part
	idx  int
	f    *os.File
	fOff int64 // offset of part idx within the object
}

func newReader(blobs *blob.Store, o *Object) *reader {
	r := &reader{blobs: blobs, o: o, idx: -1}
	if len(o.Parts) == 0 {
		r.inl = bytes.NewReader(o.Inline)
	}
	return r
}

func (r *reader) Read(p []byte) (int, error) {
	if r.inl != nil {
		return r.inl.Read(p)
	}
	if r.pos >= r.o.Size {
		return 0, io.EOF
	}
	if err := r.ensurePart(); err != nil {
		return 0, err
	}
	part := r.o.Parts[r.idx]
	remaining := part.Size - (r.pos - r.fOff)
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := r.f.ReadAt(p, r.pos-r.fOff)
	r.pos += int64(n)
	if errors.Is(err, io.EOF) && int64(n) == remaining {
		err = nil
	}
	if err == nil && n == 0 {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}

// ensurePart opens the part containing r.pos.
func (r *reader) ensurePart() error {
	if r.f != nil && r.pos >= r.fOff && r.pos < r.fOff+r.o.Parts[r.idx].Size {
		return nil
	}
	if r.f != nil {
		r.f.Close()
		r.f = nil
	}
	var off int64
	for i, p := range r.o.Parts {
		if r.pos < off+p.Size {
			f, err := r.blobs.Open(p.Blob)
			if err != nil {
				return err
			}
			r.f, r.idx, r.fOff = f, i, off
			return nil
		}
		off += p.Size
	}
	return io.EOF
}

func (r *reader) Seek(offset int64, whence int) (int64, error) {
	if r.inl != nil {
		return r.inl.Seek(offset, whence)
	}
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.pos + offset
	case io.SeekEnd:
		abs = r.o.Size + offset
	default:
		return 0, errors.New("invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("negative position")
	}
	r.pos = abs
	return abs, nil
}

func (r *reader) Close() error {
	if r.f != nil {
		err := r.f.Close()
		r.f = nil
		return err
	}
	return nil
}
