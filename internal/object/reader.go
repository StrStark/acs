package object

import (
	"bytes"
	"context"
	"errors"
	"io"
)

type ReadSeekCloser interface {
	io.ReadSeeker
	io.Closer
}

// segment is a contiguous slice of an object's data that can be streamed
// from any offset (a blob in some store, or a mirrored remote object).
type segment struct {
	size int64
	open func(offset int64) (io.ReadCloser, error)
}

// reader presents an object's segments as one seekable stream. Streams are
// opened lazily and reopened after a seek, so Seek itself does no I/O (which
// http.ServeContent relies on to learn the size).
type reader struct {
	inl  *bytes.Reader
	segs []segment
	size int64
	pos  int64

	cur    io.ReadCloser
	curPos int64 // object offset the open stream will read next
}

func (r *reader) Read(p []byte) (int, error) {
	if r.inl != nil {
		return r.inl.Read(p)
	}
	if r.pos >= r.size {
		return 0, io.EOF
	}
	if r.cur == nil || r.curPos != r.pos {
		if err := r.openAt(r.pos); err != nil {
			return 0, err
		}
	}
	n, err := r.cur.Read(p)
	r.pos += int64(n)
	r.curPos = r.pos
	if errors.Is(err, io.EOF) {
		// End of this segment; the next Read opens the following one.
		r.cur.Close()
		r.cur = nil
		if r.pos < r.size {
			err = nil
			if n == 0 {
				return r.Read(p)
			}
		}
	}
	return n, err
}

func (r *reader) openAt(pos int64) error {
	if r.cur != nil {
		r.cur.Close()
		r.cur = nil
	}
	var off int64
	for _, sg := range r.segs {
		if pos < off+sg.size {
			rc, err := sg.open(pos - off)
			if err != nil {
				return err
			}
			r.cur, r.curPos = rc, pos
			return nil
		}
		off += sg.size
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
		abs = r.size + offset
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
	if r.cur != nil {
		err := r.cur.Close()
		r.cur = nil
		return err
	}
	return nil
}

// Open returns a reader over the object's data.
func (s *Service) Open(o *Object) ReadSeekCloser {
	ctx := context.Background()
	if o.remote != nil {
		rem, key := o.remote, o.Key
		return &reader{size: o.Size, segs: []segment{{size: o.Size, open: func(off int64) (io.ReadCloser, error) {
			return rem.Get(ctx, key, off)
		}}}}
	}
	if len(o.Parts) == 0 {
		return &reader{inl: bytes.NewReader(o.Inline)}
	}
	r := &reader{size: o.Size}
	for _, p := range o.Parts {
		p := p
		r.segs = append(r.segs, segment{size: p.Size, open: func(off int64) (io.ReadCloser, error) {
			st, err := s.store(p.Store)
			if err != nil {
				return nil, err
			}
			return st.Open(ctx, p.Blob, off)
		}})
	}
	return r
}
