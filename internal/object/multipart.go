package object

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

const (
	MinPartSize = 5 << 20
	MaxPartNum  = 10000
)

func (s *Service) CreateUpload(ctx context.Context, bucket, key string, opts PutOptions) (*Upload, error) {
	if !ValidKey(key) {
		return nil, ErrInvalidKey
	}
	if _, err := s.GetBucket(ctx, bucket); err != nil {
		return nil, err
	}
	u := &Upload{
		Bucket: bucket, Key: key, ID: randomID(), Initiated: time.Now().UTC(),
		ContentType: opts.ContentType, UserMeta: opts.UserMeta, Headers: opts.Headers, Tags: opts.Tags,
	}
	batch := s.db.NewBatch()
	setJSON(batch, uploadKey(bucket, key, u.ID), u)
	if err := s.commit(batch); err != nil {
		return nil, err
	}
	return u, nil
}

func (s *Service) GetUpload(ctx context.Context, bucket, key, uploadID string) (*Upload, error) {
	if _, err := s.GetBucket(ctx, bucket); err != nil {
		return nil, err
	}
	var u Upload
	ok, err := s.getJSON(uploadKey(bucket, key, uploadID), &u)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNoSuchUpload
	}
	u.Bucket, u.Key = bucket, key
	return &u, nil
}

// UploadPart stores one part. Re-uploading a part number replaces it.
func (s *Service) UploadPart(ctx context.Context, bucket, key, uploadID string, num int, r io.Reader, size int64, contentMD5 []byte) (*Part, error) {
	if num < 1 || num > MaxPartNum {
		return nil, fmt.Errorf("%w: part number must be between 1 and %d", ErrInvalidArgument, MaxPartNum)
	}
	if _, err := s.GetUpload(ctx, bucket, key, uploadID); err != nil {
		return nil, err
	}
	id, n, sum, err := s.blobs.CopyFrom(r)
	if err != nil {
		return nil, err
	}
	drop := func(err error) (*Part, error) {
		s.deleteBlobsNow([]PartRef{{Blob: id}})
		return nil, err
	}
	if size >= 0 && n != size {
		return drop(ErrIncompleteBody)
	}
	if contentMD5 != nil && !bytes.Equal(contentMD5, sum) {
		return drop(ErrBadDigest)
	}
	p := &Part{Number: num, Size: n, ETag: hex.EncodeToString(sum), Blob: id, ModTime: time.Now().UTC()}

	unlock := s.lockKey(uploadID, fmt.Sprint(num))
	defer unlock()
	// The upload may have been completed or aborted while we were writing.
	if _, err := s.GetUpload(ctx, bucket, key, uploadID); err != nil {
		return drop(err)
	}
	batch := s.db.NewBatch()
	var old Part
	if ok, err := s.getJSON(partKey(uploadID, num), &old); err != nil {
		batch.Close()
		return drop(err)
	} else if ok {
		s.enqueueGC(batch, &Object{Parts: []PartRef{{Blob: old.Blob, Size: old.Size}}})
	}
	setJSON(batch, partKey(uploadID, num), p)
	if err := s.commit(batch); err != nil {
		return drop(err)
	}
	return p, nil
}

func (s *Service) parts(uploadID string) ([]*Part, error) {
	var out []*Part
	p := partPrefix(uploadID)
	err := s.scan(p, prefixEnd(p), func(_, val []byte) (bool, error) {
		var part Part
		if err := jsonUnmarshal(val, &part); err != nil {
			return false, err
		}
		out = append(out, &part)
		return true, nil
	})
	return out, err
}

// ListParts returns parts with number greater than marker.
func (s *Service) ListParts(ctx context.Context, bucket, key, uploadID string, marker, max int) (parts []*Part, truncated bool, err error) {
	if _, err := s.GetUpload(ctx, bucket, key, uploadID); err != nil {
		return nil, false, err
	}
	all, err := s.parts(uploadID)
	if err != nil {
		return nil, false, err
	}
	max = clampMax(max)
	for _, p := range all {
		if p.Number <= marker {
			continue
		}
		if len(parts) == max {
			return parts, true, nil
		}
		parts = append(parts, p)
	}
	return parts, false, nil
}

type CompletePart struct {
	Number int
	ETag   string
}

func (s *Service) CompleteUpload(ctx context.Context, bucket, key, uploadID string, want []CompletePart) (*Object, error) {
	u, err := s.GetUpload(ctx, bucket, key, uploadID)
	if err != nil {
		return nil, err
	}
	if len(want) == 0 {
		return nil, ErrInvalidPart
	}
	have, err := s.parts(uploadID)
	if err != nil {
		return nil, err
	}
	byNum := make(map[int]*Part, len(have))
	for _, p := range have {
		byNum[p.Number] = p
	}

	o := &Object{ContentType: u.ContentType, UserMeta: u.UserMeta, Headers: u.Headers, Tags: u.Tags}
	digests := md5.New()
	used := make(map[int]bool, len(want))
	for i := 1; i < len(want); i++ {
		if want[i].Number <= want[i-1].Number {
			return nil, ErrInvalidPartOrder
		}
	}
	for i, w := range want {
		p := byNum[w.Number]
		if p == nil || strings.Trim(w.ETag, `"`) != p.ETag {
			return nil, ErrInvalidPart
		}
		if i < len(want)-1 && p.Size < MinPartSize {
			return nil, ErrEntityTooSmall
		}
		raw, _ := hex.DecodeString(p.ETag)
		digests.Write(raw)
		o.Parts = append(o.Parts, PartRef{Blob: p.Blob, Size: p.Size})
		o.Size += p.Size
		used[p.Number] = true
	}
	o.ETag = fmt.Sprintf("%s-%d", hex.EncodeToString(digests.Sum(nil)), len(want))

	err = s.commitVersion(ctx, bucket, key, o, &commitOptions{extra: func(b *pebble.Batch) {
		b.Delete(uploadKey(bucket, key, uploadID), nil)
		for _, p := range have {
			b.Delete(partKey(uploadID, p.Number), nil)
			if !used[p.Number] {
				s.enqueueGC(b, &Object{Parts: []PartRef{{Blob: p.Blob, Size: p.Size}}})
			}
		}
	}})
	if err != nil {
		return nil, err
	}
	return o, nil
}

func (s *Service) AbortUpload(ctx context.Context, bucket, key, uploadID string) error {
	u, err := s.GetUpload(ctx, bucket, key, uploadID)
	if err != nil {
		return err
	}
	return s.abortUpload(u)
}

func (s *Service) abortUpload(u *Upload) error {
	parts, err := s.parts(u.ID)
	if err != nil {
		return err
	}
	batch := s.db.NewBatch()
	batch.Delete(uploadKey(u.Bucket, u.Key, u.ID), nil)
	for _, p := range parts {
		batch.Delete(partKey(u.ID, p.Number), nil)
		s.enqueueGC(batch, &Object{Parts: []PartRef{{Blob: p.Blob, Size: p.Size}}})
	}
	if err := s.commit(batch); err != nil {
		return err
	}
	return nil
}

// scanUploads iterates uploads in key, then upload-ID order, after the markers.
func (s *Service) scanUploads(bucket, prefix, keyMarker, uploadIDMarker string, fn func(*Upload) bool) error {
	base := uploadPrefix(bucket)
	lower := append(append([]byte(nil), base...), prefix...)
	return s.scan(lower, prefixEnd(lower), func(key, val []byte) (bool, error) {
		rest := key[len(base):]
		sep := bytes.LastIndexByte(rest, 0)
		objKey, id := string(rest[:sep]), string(rest[sep+1:])
		if keyMarker != "" && (objKey < keyMarker || (objKey == keyMarker && (uploadIDMarker == "" || id <= uploadIDMarker))) {
			return true, nil
		}
		var u Upload
		if err := jsonUnmarshal(val, &u); err != nil {
			return false, err
		}
		u.Bucket, u.Key = bucket, objKey
		return fn(&u), nil
	})
}

// ListUploads lists in-progress multipart uploads.
func (s *Service) ListUploads(ctx context.Context, bucket, prefix, keyMarker, uploadIDMarker string, max int) (uploads []*Upload, truncated bool, err error) {
	if _, err := s.GetBucket(ctx, bucket); err != nil {
		return nil, false, err
	}
	max = clampMax(max)
	err = s.scanUploads(bucket, prefix, keyMarker, uploadIDMarker, func(u *Upload) bool {
		if len(uploads) == max {
			truncated = true
			return false
		}
		uploads = append(uploads, u)
		return true
	})
	return uploads, truncated, err
}
