package object

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

var bucketNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// ValidBucketName applies S3's bucket naming rules.
func ValidBucketName(name string) bool {
	if !bucketNameRe.MatchString(name) {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, ".-") || strings.Contains(name, "-.") {
		return false
	}
	return net.ParseIP(name) == nil
}

func (s *Service) CreateBucket(ctx context.Context, name string) (*Bucket, error) {
	if !ValidBucketName(name) {
		return nil, ErrInvalidBucketName
	}
	s.bucketMu.Lock()
	defer s.bucketMu.Unlock()

	var existing Bucket
	if ok, err := s.getJSON(bucketKey(name), &existing); err != nil {
		return nil, err
	} else if ok {
		return nil, ErrBucketExists
	}
	b := &Bucket{Name: name, CreatedAt: time.Now().UTC()}
	batch := s.db.NewBatch()
	setJSON(batch, bucketKey(name), b)
	if err := s.commit(batch); err != nil {
		return nil, err
	}
	s.emit(Event{Type: EventBucketCreated, Bucket: name})
	return b, nil
}

func (s *Service) GetBucket(ctx context.Context, name string) (*Bucket, error) {
	var b Bucket
	ok, err := s.getJSON(bucketKey(name), &b)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNoSuchBucket
	}
	return &b, nil
}

func (s *Service) ListBuckets(ctx context.Context) ([]*Bucket, error) {
	var out []*Bucket
	p := bucketPrefix()
	err := s.scan(p, prefixEnd(p), func(_, val []byte) (bool, error) {
		var b Bucket
		if err := jsonUnmarshal(val, &b); err != nil {
			return false, err
		}
		out = append(out, &b)
		return true, nil
	})
	return out, err
}

// UpdateBucket applies fn to the bucket's settings and saves them.
func (s *Service) UpdateBucket(ctx context.Context, name string, fn func(*Bucket) error) (*Bucket, error) {
	s.bucketMu.Lock()
	defer s.bucketMu.Unlock()
	b, err := s.GetBucket(ctx, name)
	if err != nil {
		return nil, err
	}
	prevVersioning := b.Versioning
	if err := fn(b); err != nil {
		return nil, err
	}
	b.Name = name
	switch b.Versioning {
	case VersioningEnabled, VersioningSuspended:
	case VersioningOff:
		if prevVersioning != VersioningOff {
			return nil, errors.Join(ErrInvalidArgument, errors.New("versioning cannot be disabled once enabled; suspend it instead"))
		}
	default:
		return nil, errors.Join(ErrInvalidArgument, errors.New("unknown versioning state"))
	}
	batch := s.db.NewBatch()
	setJSON(batch, bucketKey(name), b)
	if err := s.commit(batch); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *Service) BucketStats(ctx context.Context, name string) (Stats, error) {
	val, closer, err := s.db.Get(statsKey(name))
	if errors.Is(err, pebble.ErrNotFound) {
		return Stats{}, nil
	}
	if err != nil {
		return Stats{}, err
	}
	defer closer.Close()
	return decodeStats(val), nil
}

// DeleteBucket removes an empty bucket. With force, all objects, versions and
// pending uploads are deleted first.
func (s *Service) DeleteBucket(ctx context.Context, name string, force bool) error {
	s.bucketMu.Lock()
	defer s.bucketMu.Unlock()
	if _, err := s.GetBucket(ctx, name); err != nil {
		return err
	}

	if !force {
		empty := true
		for _, p := range [][]byte{verPrefix(name), uploadPrefix(name)} {
			if err := s.scan(p, prefixEnd(p), func(_, _ []byte) (bool, error) {
				empty = false
				return false, nil
			}); err != nil {
				return err
			}
		}
		if !empty {
			return ErrBucketNotEmpty
		}
	} else if err := s.purgeBucket(name); err != nil {
		return err
	}

	batch := s.db.NewBatch()
	batch.Delete(bucketKey(name), nil)
	batch.Delete(statsKey(name), nil)
	if err := s.commit(batch); err != nil {
		return err
	}
	s.emit(Event{Type: EventBucketDeleted, Bucket: name})
	return nil
}

// purgeBucket deletes every version, current pointer and upload in chunks.
func (s *Service) purgeBucket(name string) error {
	const chunk = 1000
	for _, p := range [][]byte{verPrefix(name), objPrefix(name)} {
		for {
			batch := s.db.NewBatch()
			n := 0
			err := s.scan(p, prefixEnd(p), func(key, val []byte) (bool, error) {
				if key[0] == 'V' {
					var o Object
					if err := jsonUnmarshal(val, &o); err != nil {
						return false, err
					}
					s.enqueueGC(batch, &o)
				}
				batch.Delete(append([]byte(nil), key...), nil)
				n++
				return n < chunk, nil
			})
			if err == nil && n > 0 {
				err = s.commit(batch)
			} else {
				batch.Close()
			}
			if err != nil {
				return err
			}
			if n < chunk {
				break
			}
		}
	}

	var uploads []*Upload
	if err := s.scanUploads(name, "", "", "", func(u *Upload) bool {
		uploads = append(uploads, u)
		return true
	}); err != nil {
		return err
	}
	for _, u := range uploads {
		if err := s.abortUpload(u); err != nil {
			return err
		}
	}
	return nil
}
