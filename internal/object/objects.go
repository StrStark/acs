package object

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cockroachdb/pebble/v2"
)

// Objects up to this size are stored inside the metadata record instead of a
// blob file, avoiding one file per tiny object.
const inlineMax = 64 << 10

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// ValidKey reports whether key is an acceptable object key.
func ValidKey(key string) bool {
	return key != "" && len(key) <= 1024 && utf8.ValidString(key) && !strings.ContainsRune(key, 0)
}

type PutOptions struct {
	ContentType string
	UserMeta    map[string]string
	Headers     map[string]string
	Tags        map[string]string
	// Size is the declared length, or -1 if unknown.
	Size int64
	// ContentMD5, if set, must match the MD5 of the received data.
	ContentMD5 []byte
}

type data struct {
	inline []byte
	parts  []PartRef
	size   int64
	md5    []byte
}

// writeData stores r either inline or in a blob.
func (s *Service) writeData(r io.Reader) (*data, error) {
	buf := make([]byte, inlineMax+1)
	n, err := io.ReadFull(r, buf)
	switch {
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		sum := md5.Sum(buf[:n])
		return &data{inline: buf[:n:n], size: int64(n), md5: sum[:]}, nil
	case err != nil:
		return nil, err
	}

	w, err := s.blobs.Create()
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(buf); err != nil {
		w.Abort()
		return nil, err
	}
	if _, err := io.Copy(w, r); err != nil {
		w.Abort()
		return nil, err
	}
	size := w.Size()
	id, sum, err := w.Commit()
	if err != nil {
		return nil, err
	}
	return &data{parts: []PartRef{{Blob: id, Size: size}}, size: size, md5: sum}, nil
}

func (s *Service) PutObject(ctx context.Context, bucket, key string, r io.Reader, opts PutOptions) (*Object, error) {
	if !ValidKey(key) {
		return nil, ErrInvalidKey
	}
	b, err := s.GetBucket(ctx, bucket)
	if err != nil {
		return nil, err
	}
	// Cheap early rejection; the exact check (which accounts for an
	// overwritten version being freed) runs at commit.
	if b.QuotaBytes > 0 && opts.Size > b.QuotaBytes {
		return nil, ErrQuotaExceeded
	}

	d, err := s.writeData(r)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Object, error) {
		s.deleteBlobsNow(d.parts)
		return nil, err
	}
	if opts.Size >= 0 && d.size != opts.Size {
		return fail(ErrIncompleteBody)
	}
	if opts.ContentMD5 != nil && !bytes.Equal(opts.ContentMD5, d.md5) {
		return fail(ErrBadDigest)
	}

	o := &Object{
		Size:        d.size,
		ETag:        hex.EncodeToString(d.md5),
		ContentType: opts.ContentType,
		UserMeta:    opts.UserMeta,
		Headers:     opts.Headers,
		Tags:        opts.Tags,
		Inline:      d.inline,
		Parts:       d.parts,
	}
	if err := s.commitVersion(ctx, bucket, key, o, nil); err != nil {
		return fail(err)
	}
	return o, nil
}

func (s *Service) checkQuota(ctx context.Context, b *Bucket, addBytes, addObjects int64) error {
	if b.QuotaBytes <= 0 && b.QuotaObjects <= 0 {
		return nil
	}
	st, err := s.BucketStats(ctx, b.Name)
	if err != nil {
		return err
	}
	if b.QuotaBytes > 0 && st.StoredBytes+addBytes > b.QuotaBytes {
		return ErrQuotaExceeded
	}
	if b.QuotaObjects > 0 && st.Objects+addObjects > b.QuotaObjects {
		return ErrQuotaExceeded
	}
	return nil
}

// currentDelta returns the stats change when the current version of a key
// goes from before to after (either may be nil).
func currentDelta(before, after *Object) Stats {
	var d Stats
	if before != nil {
		d.Objects--
		d.Bytes -= before.Size
	}
	if after != nil {
		d.Objects++
		d.Bytes += after.Size
	}
	return d
}

type commitOptions struct {
	// extra adds operations to the same atomic batch (multipart completion, moves).
	extra     func(*pebble.Batch)
	skipQuota bool
}

// commitVersion makes o the newest version of bucket/key, honouring the
// bucket's versioning state.
func (s *Service) commitVersion(ctx context.Context, bucket, key string, o *Object, opts *commitOptions) error {
	unlock := s.lockKey(bucket, key)
	defer unlock()
	return s.commitVersionLocked(ctx, bucket, key, o, opts)
}

// commitVersionLocked is commitVersion for callers already holding the key lock.
func (s *Service) commitVersionLocked(ctx context.Context, bucket, key string, o *Object, opts *commitOptions) error {
	if opts == nil {
		opts = &commitOptions{}
	}
	s.bucketMu.RLock()
	defer s.bucketMu.RUnlock()

	b, err := s.GetBucket(ctx, bucket)
	if err != nil {
		return err
	}
	cur, err := s.current(bucket, key)
	if err != nil {
		return err
	}

	batch := s.db.NewBatch()
	var delta Stats
	o.Seq = s.newSeq()
	o.ModTime = time.Now().UTC()
	o.Bucket, o.Key = bucket, key
	if o.ContentType == "" && !o.DeleteMarker {
		o.ContentType = "application/octet-stream"
	}

	var replaced *Object
	if b.Versioning == VersioningEnabled {
		o.VersionID = o.Seq
	} else {
		o.VersionID = NullVersion
		old, err := s.findVersion(bucket, key, NullVersion)
		if err != nil {
			batch.Close()
			return err
		}
		if old != nil {
			replaced = old
			batch.Delete(verKey(bucket, key, old.Seq), nil)
			s.enqueueGC(batch, old)
			delta.StoredBytes -= old.Size
		}
	}

	if !o.DeleteMarker && !opts.skipQuota {
		var addObjects int64 = 1
		if cur != nil {
			addObjects = 0
		}
		freed := int64(0)
		if replaced != nil {
			freed = replaced.Size
		}
		if err := s.checkQuota(ctx, b, o.Size-freed, addObjects); err != nil {
			batch.Close()
			return err
		}
	}

	setJSON(batch, verKey(bucket, key, o.Seq), o)
	delta.StoredBytes += o.Size
	if o.DeleteMarker {
		batch.Delete(objKey(bucket, key), nil)
		delta.add(currentDelta(cur, nil))
	} else {
		setJSON(batch, objKey(bucket, key), o)
		delta.add(currentDelta(cur, o))
	}
	batch.Merge(statsKey(bucket), delta.encode(), nil)
	if opts.extra != nil {
		opts.extra(batch)
	}
	if err := s.commit(batch); err != nil {
		return err
	}
	o.IsLatest = true
	if replaced != nil {
	}

	if o.DeleteMarker {
		s.emit(Event{Type: EventObjectDeleted, Bucket: bucket, Key: key, VersionID: o.VersionID, DeleteMarker: true})
	} else {
		s.emit(Event{Type: EventObjectCreated, Bucket: bucket, Key: key, VersionID: o.VersionID, Size: o.Size, ETag: o.ETag})
	}
	return nil
}

// current returns the current version of a key, or nil.
func (s *Service) current(bucket, key string) (*Object, error) {
	var o Object
	ok, err := s.getJSON(objKey(bucket, key), &o)
	if err != nil || !ok {
		return nil, err
	}
	o.Bucket, o.Key, o.IsLatest = bucket, key, true
	return &o, nil
}

// findVersion locates a specific version, or returns nil.
func (s *Service) findVersion(bucket, key, versionID string) (*Object, error) {
	if versionID != NullVersion {
		if len(versionID) != 24 {
			return nil, nil
		}
		var o Object
		ok, err := s.getJSON(verKey(bucket, key, versionID), &o)
		if err != nil || !ok {
			return nil, err
		}
		o.Bucket, o.Key = bucket, key
		return &o, nil
	}
	var found *Object
	p := verKeyPrefix(bucket, key)
	err := s.scan(p, prefixEnd(p), func(_, val []byte) (bool, error) {
		var o Object
		if err := jsonUnmarshal(val, &o); err != nil {
			return false, err
		}
		if o.VersionID == NullVersion {
			o.Bucket, o.Key = bucket, key
			found = &o
			return false, nil
		}
		return true, nil
	})
	return found, err
}

// versions returns all versions of a key, newest first.
func (s *Service) versions(bucket, key string) ([]*Object, error) {
	var out []*Object
	p := verKeyPrefix(bucket, key)
	err := s.scan(p, prefixEnd(p), func(_, val []byte) (bool, error) {
		var o Object
		if err := jsonUnmarshal(val, &o); err != nil {
			return false, err
		}
		o.Bucket, o.Key = bucket, key
		out = append(out, &o)
		return true, nil
	})
	if len(out) > 0 {
		out[0].IsLatest = true
	}
	return out, err
}

// ObjectVersions returns every version of a key (newest first).
func (s *Service) ObjectVersions(ctx context.Context, bucket, key string) ([]*Object, error) {
	if _, err := s.GetBucket(ctx, bucket); err != nil {
		return nil, err
	}
	return s.versions(bucket, key)
}

// StatObject returns metadata for the current object, or a specific version.
func (s *Service) StatObject(ctx context.Context, bucket, key, versionID string) (*Object, error) {
	if _, err := s.GetBucket(ctx, bucket); err != nil {
		return nil, err
	}
	if versionID == "" {
		o, err := s.current(bucket, key)
		if err != nil {
			return nil, err
		}
		if o == nil {
			return nil, ErrNoSuchKey
		}
		return o, nil
	}
	o, err := s.findVersion(bucket, key, versionID)
	if err != nil {
		return nil, err
	}
	if o == nil {
		return nil, ErrNoSuchVersion
	}
	if o.DeleteMarker {
		return o, ErrDeleteMarker
	}
	if cur, _ := s.current(bucket, key); cur != nil && cur.Seq == o.Seq {
		o.IsLatest = true
	}
	return o, nil
}

// Open returns a reader over the object's data.
func (s *Service) Open(o *Object) ReadSeekCloser {
	return newReader(s.blobs, o)
}

type DeleteResult struct {
	VersionID    string
	DeleteMarker bool
}

// DeleteObject deletes the current object (creating a delete marker when
// versioning is enabled) or, with versionID, permanently removes that version.
func (s *Service) DeleteObject(ctx context.Context, bucket, key, versionID string) (DeleteResult, error) {
	b, err := s.GetBucket(ctx, bucket)
	if err != nil {
		return DeleteResult{}, err
	}
	if versionID != "" {
		return s.deleteVersion(ctx, bucket, key, versionID)
	}
	switch b.Versioning {
	case VersioningEnabled, VersioningSuspended:
		// commitVersion replaces any existing null version when suspended.
		marker := &Object{DeleteMarker: true}
		if err := s.commitVersion(ctx, bucket, key, marker, nil); err != nil {
			return DeleteResult{}, err
		}
		return DeleteResult{VersionID: marker.VersionID, DeleteMarker: true}, nil
	default:
		return s.deleteVersion(ctx, bucket, key, NullVersion)
	}
}

func (s *Service) deleteVersion(ctx context.Context, bucket, key, versionID string) (DeleteResult, error) {
	unlock := s.lockKey(bucket, key)
	defer unlock()
	s.bucketMu.RLock()
	defer s.bucketMu.RUnlock()

	vers, err := s.versions(bucket, key)
	if err != nil {
		return DeleteResult{}, err
	}
	idx := -1
	for i, v := range vers {
		if v.VersionID == versionID {
			idx = i
			break
		}
	}
	if idx < 0 {
		// Deleting something that does not exist succeeds, as in S3.
		return DeleteResult{VersionID: versionID}, nil
	}
	victim := vers[idx]
	cur, err := s.current(bucket, key)
	if err != nil {
		return DeleteResult{}, err
	}

	batch := s.db.NewBatch()
	batch.Delete(verKey(bucket, key, victim.Seq), nil)
	s.enqueueGC(batch, victim)
	delta := Stats{StoredBytes: -victim.Size}

	if idx == 0 {
		// The latest version is going away; the next one (if any) becomes current.
		var next *Object
		if len(vers) > 1 && !vers[1].DeleteMarker {
			next = vers[1]
		}
		if next != nil {
			setJSON(batch, objKey(bucket, key), next)
		} else {
			batch.Delete(objKey(bucket, key), nil)
		}
		delta.add(currentDelta(cur, next))
	}
	batch.Merge(statsKey(bucket), delta.encode(), nil)
	if err := s.commit(batch); err != nil {
		return DeleteResult{}, err
	}
	s.emit(Event{Type: EventObjectDeleted, Bucket: bucket, Key: key, VersionID: victim.VersionID, Size: victim.Size})
	return DeleteResult{VersionID: victim.VersionID, DeleteMarker: victim.DeleteMarker}, nil
}

type CopyOptions struct {
	SrcVersionID string
	// ReplaceMetadata uses the fields below instead of the source's metadata.
	ReplaceMetadata bool
	ContentType     string
	UserMeta        map[string]string
	Headers         map[string]string
	ReplaceTags     bool
	Tags            map[string]string
}

// CopyObject copies an object (or version) to a new key, possibly in another bucket.
func (s *Service) CopyObject(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string, opts CopyOptions) (*Object, error) {
	if !ValidKey(dstKey) {
		return nil, ErrInvalidKey
	}
	src, err := s.StatObject(ctx, srcBucket, srcKey, opts.SrcVersionID)
	if err != nil {
		return nil, err
	}
	dstB, err := s.GetBucket(ctx, dstBucket)
	if err != nil {
		return nil, err
	}

	meta := func(o *Object) {
		o.ContentType, o.UserMeta, o.Headers = src.ContentType, src.UserMeta, src.Headers
		if opts.ReplaceMetadata {
			o.ContentType, o.UserMeta, o.Headers = opts.ContentType, opts.UserMeta, opts.Headers
		}
		o.Tags = src.Tags
		if opts.ReplaceTags {
			o.Tags = opts.Tags
		}
	}

	// Copying an unversioned object onto itself only rewrites metadata.
	if srcBucket == dstBucket && srcKey == dstKey && dstB.Versioning == VersioningOff && src.IsLatest {
		return s.updateVersion(ctx, srcBucket, srcKey, src.VersionID, func(o *Object) { meta(o); o.ModTime = time.Now().UTC() })
	}

	if err := s.checkQuota(ctx, dstB, src.Size, 1); err != nil {
		return nil, err
	}
	r := s.Open(src)
	d, err := s.writeData(r)
	r.Close()
	if err != nil {
		return nil, err
	}
	o := &Object{Size: d.size, ETag: src.ETag, Inline: d.inline, Parts: d.parts}
	meta(o)
	if err := s.commitVersion(ctx, dstBucket, dstKey, o, nil); err != nil {
		s.deleteBlobsNow(d.parts)
		return nil, err
	}
	return o, nil
}

// MoveObject renames an object. Between unversioned buckets the data is
// re-linked without copying; otherwise it is copied and the source deleted.
func (s *Service) MoveObject(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string) (*Object, error) {
	if !ValidKey(dstKey) {
		return nil, ErrInvalidKey
	}
	if srcBucket == dstBucket && srcKey == dstKey {
		return s.StatObject(ctx, srcBucket, srcKey, "")
	}
	srcB, err := s.GetBucket(ctx, srcBucket)
	if err != nil {
		return nil, err
	}
	dstB, err := s.GetBucket(ctx, dstBucket)
	if err != nil {
		return nil, err
	}
	if srcB.Versioning != VersioningOff || dstB.Versioning != VersioningOff {
		o, err := s.CopyObject(ctx, srcBucket, srcKey, dstBucket, dstKey, CopyOptions{})
		if err != nil {
			return nil, err
		}
		if _, err := s.DeleteObject(ctx, srcBucket, srcKey, ""); err != nil {
			return nil, err
		}
		return o, nil
	}

	unlock := s.lockKeys(srcBucket, srcKey, dstBucket, dstKey)
	defer unlock()
	src, err := s.current(srcBucket, srcKey)
	if err != nil {
		return nil, err
	}
	if src == nil {
		return nil, ErrNoSuchKey
	}
	if srcBucket != dstBucket {
		if err := s.checkQuota(ctx, dstB, src.Size, 1); err != nil {
			return nil, err
		}
	}

	// The destination takes over the source's data; the source records are
	// removed in the same batch without scheduling their blobs for GC.
	moved := *src
	moved.IsLatest = false
	err = s.commitVersionLocked(ctx, dstBucket, dstKey, &moved, &commitOptions{
		skipQuota: true,
		extra: func(b *pebble.Batch) {
			b.Delete(verKey(srcBucket, srcKey, src.Seq), nil)
			b.Delete(objKey(srcBucket, srcKey), nil)
			d := currentDelta(src, nil)
			d.StoredBytes = -src.Size
			b.Merge(statsKey(srcBucket), d.encode(), nil)
		},
	})
	if err != nil {
		return nil, err
	}
	s.emit(Event{Type: EventObjectDeleted, Bucket: srcBucket, Key: srcKey, VersionID: src.VersionID, Size: src.Size})
	return &moved, nil
}

// updateVersion modifies a version's metadata in place (no new version).
func (s *Service) updateVersion(ctx context.Context, bucket, key, versionID string, fn func(*Object)) (*Object, error) {
	unlock := s.lockKey(bucket, key)
	defer unlock()
	var o *Object
	var err error
	if versionID == "" {
		o, err = s.current(bucket, key)
		if o == nil && err == nil {
			err = ErrNoSuchKey
		}
	} else {
		o, err = s.findVersion(bucket, key, versionID)
		if o == nil && err == nil {
			err = ErrNoSuchVersion
		}
	}
	if err != nil {
		return nil, err
	}
	if o.DeleteMarker {
		return nil, ErrDeleteMarker
	}
	fn(o)
	cur, err := s.current(bucket, key)
	if err != nil {
		return nil, err
	}
	batch := s.db.NewBatch()
	setJSON(batch, verKey(bucket, key, o.Seq), o)
	if cur != nil && cur.Seq == o.Seq {
		setJSON(batch, objKey(bucket, key), o)
		o.IsLatest = true
	}
	if err := s.commit(batch); err != nil {
		return nil, err
	}
	return o, nil
}

// SetObjectTags replaces the tags of the current object or a version.
func (s *Service) SetObjectTags(ctx context.Context, bucket, key, versionID string, tags map[string]string) (*Object, error) {
	if _, err := s.GetBucket(ctx, bucket); err != nil {
		return nil, err
	}
	return s.updateVersion(ctx, bucket, key, versionID, func(o *Object) { o.Tags = tags })
}

// UpdateObjectMetadata replaces content type, user metadata and headers in place.
func (s *Service) UpdateObjectMetadata(ctx context.Context, bucket, key string, contentType string, userMeta, headers map[string]string) (*Object, error) {
	if _, err := s.GetBucket(ctx, bucket); err != nil {
		return nil, err
	}
	return s.updateVersion(ctx, bucket, key, "", func(o *Object) {
		if contentType != "" {
			o.ContentType = contentType
		}
		o.UserMeta, o.Headers = userMeta, headers
	})
}
