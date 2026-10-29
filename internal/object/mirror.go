package object

import (
	"context"
	"errors"
	"io"
	"time"
)

// Mirror buckets pass every object operation through to a remote S3 bucket
// (under the bucket's RemotePrefix). ACS keeps no object metadata for them,
// so objects are always "null" versions and stats/quotas do not apply.

func (s *Service) fromRemote(b *Bucket, rem Remote, ro *RemoteObject) *Object {
	return &Object{
		Bucket: b.Name, Key: ro.Key, IsLatest: true, VersionID: NullVersion,
		Size: ro.Size, ETag: ro.ETag, ContentType: ro.ContentType, ModTime: ro.ModTime,
		UserMeta: ro.UserMeta, Headers: ro.Headers, remote: rem,
	}
}

func (s *Service) mirrorStat(ctx context.Context, b *Bucket, key string) (*Object, error) {
	rem, err := s.mirror(b)
	if err != nil {
		return nil, err
	}
	ro, err := rem.Stat(ctx, key)
	if err != nil {
		return nil, err
	}
	return s.fromRemote(b, rem, ro), nil
}

func (s *Service) mirrorPut(ctx context.Context, b *Bucket, key string, r io.Reader, opts PutOptions) (*Object, error) {
	rem, err := s.mirror(b)
	if err != nil {
		return nil, err
	}
	src := &errCapture{r: r}
	ro, err := rem.Put(ctx, key, src, opts.Size, opts.ContentMD5, RemoteMeta{
		ContentType: opts.ContentType, UserMeta: opts.UserMeta, Headers: opts.Headers, Tags: opts.Tags,
	})
	if err != nil {
		if src.err != nil {
			return nil, src.err
		}
		return nil, err
	}
	o := s.fromRemote(b, rem, ro)
	o.Tags = opts.Tags
	s.emit(Event{Type: EventObjectCreated, Bucket: b.Name, Key: key, VersionID: NullVersion, Size: o.Size, ETag: o.ETag})
	return o, nil
}

func (s *Service) mirrorDelete(ctx context.Context, b *Bucket, key, versionID string) (DeleteResult, error) {
	if versionID != "" && versionID != NullVersion {
		return DeleteResult{VersionID: versionID}, nil
	}
	rem, err := s.mirror(b)
	if err != nil {
		return DeleteResult{}, err
	}
	if err := rem.Delete(ctx, key); err != nil && !errors.Is(err, ErrNoSuchKey) {
		return DeleteResult{}, err
	}
	s.emit(Event{Type: EventObjectDeleted, Bucket: b.Name, Key: key, VersionID: NullVersion})
	return DeleteResult{VersionID: NullVersion}, nil
}

// mirrorCopy copies src (from any bucket) into a mirror bucket. Within one
// remote provider this is a server-side copy; otherwise data is streamed.
func (s *Service) mirrorCopy(ctx context.Context, src *Object, dst *Bucket, dstKey string, opts CopyOptions) (*Object, error) {
	rem, err := s.mirror(dst)
	if err != nil {
		return nil, err
	}
	var meta *RemoteMeta
	if opts.ReplaceMetadata || opts.ReplaceTags {
		m := RemoteMeta{ContentType: src.ContentType, UserMeta: src.UserMeta, Headers: src.Headers, Tags: src.Tags}
		if opts.ReplaceMetadata {
			m.ContentType, m.UserMeta, m.Headers = opts.ContentType, opts.UserMeta, opts.Headers
		}
		if opts.ReplaceTags {
			m.Tags = opts.Tags
		}
		meta = &m
	}

	var ro *RemoteObject
	if src.remote != nil {
		ro, err = rem.Copy(ctx, src.remote, src.Key, dstKey, meta)
	} else {
		m := RemoteMeta{ContentType: src.ContentType, UserMeta: src.UserMeta, Headers: src.Headers, Tags: src.Tags}
		if meta != nil {
			m = *meta
		}
		r := s.Open(src)
		ro, err = rem.Put(ctx, dstKey, r, src.Size, nil, m)
		r.Close()
	}
	if err != nil {
		return nil, err
	}
	o := s.fromRemote(dst, rem, ro)
	s.emit(Event{Type: EventObjectCreated, Bucket: dst.Name, Key: dstKey, VersionID: NullVersion, Size: o.Size, ETag: o.ETag})
	return o, nil
}

func (s *Service) mirrorSetTags(ctx context.Context, b *Bucket, key string, tags map[string]string) (*Object, error) {
	rem, err := s.mirror(b)
	if err != nil {
		return nil, err
	}
	if err := rem.PutTags(ctx, key, tags); err != nil {
		return nil, err
	}
	o, err := s.mirrorStat(ctx, b, key)
	if err != nil {
		return nil, err
	}
	o.Tags = tags
	return o, nil
}

func (s *Service) mirrorUpdateMeta(ctx context.Context, b *Bucket, key, contentType string, userMeta, headers map[string]string) (*Object, error) {
	cur, err := s.mirrorStat(ctx, b, key)
	if err != nil {
		return nil, err
	}
	if contentType == "" {
		contentType = cur.ContentType
	}
	ro, err := cur.remote.Copy(ctx, cur.remote, key, key, &RemoteMeta{ContentType: contentType, UserMeta: userMeta, Headers: headers})
	if err != nil {
		return nil, err
	}
	return s.fromRemote(b, cur.remote, ro), nil
}

func (s *Service) mirrorList(ctx context.Context, b *Bucket, opts ListOptions) (*ListResult, error) {
	rem, err := s.mirror(b)
	if err != nil {
		return nil, err
	}
	max := clampMax(opts.MaxKeys)
	objs, prefixes, truncated, err := rem.List(ctx, opts.Prefix, opts.Delimiter, opts.Marker, max)
	if err != nil {
		return nil, err
	}
	res := &ListResult{CommonPrefixes: prefixes, IsTruncated: truncated}
	for i := range objs {
		res.Objects = append(res.Objects, s.fromRemote(b, rem, &objs[i]))
	}
	if truncated {
		// The next page starts after the greatest key or prefix returned.
		for _, o := range res.Objects {
			res.NextMarker = max2(res.NextMarker, o.Key)
		}
		for _, p := range prefixes {
			res.NextMarker = max2(res.NextMarker, p)
		}
	}
	return res, nil
}

func max2(a, b string) string {
	if b > a {
		return b
	}
	return a
}

func (s *Service) mirrorCreateUpload(ctx context.Context, b *Bucket, key string, opts PutOptions) (*Upload, error) {
	rem, err := s.mirror(b)
	if err != nil {
		return nil, err
	}
	id, err := rem.CreateUpload(ctx, key, RemoteMeta{ContentType: opts.ContentType, UserMeta: opts.UserMeta, Headers: opts.Headers, Tags: opts.Tags})
	if err != nil {
		return nil, err
	}
	return &Upload{Bucket: b.Name, Key: key, ID: id, Initiated: time.Now().UTC(), ContentType: opts.ContentType}, nil
}

func (s *Service) mirrorComplete(ctx context.Context, b *Bucket, key, uploadID string, parts []CompletePart) (*Object, error) {
	if len(parts) == 0 {
		return nil, ErrInvalidPart
	}
	for i := 1; i < len(parts); i++ {
		if parts[i].Number <= parts[i-1].Number {
			return nil, ErrInvalidPartOrder
		}
	}
	rem, err := s.mirror(b)
	if err != nil {
		return nil, err
	}
	ro, err := rem.CompleteUpload(ctx, key, uploadID, parts)
	if err != nil {
		return nil, err
	}
	o := s.fromRemote(b, rem, ro)
	s.emit(Event{Type: EventObjectCreated, Bucket: b.Name, Key: key, VersionID: NullVersion, Size: o.Size, ETag: o.ETag})
	return o, nil
}
