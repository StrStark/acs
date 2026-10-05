package object

import (
	"bytes"
	"context"
	"strings"

	"github.com/cockroachdb/pebble/v2"
)

const maxListKeys = 1000

type ListOptions struct {
	Prefix    string
	Delimiter string
	// Marker: only keys (or common prefixes) strictly greater are returned.
	Marker  string
	MaxKeys int
}

type ListResult struct {
	Objects        []*Object
	CommonPrefixes []string
	IsTruncated    bool
	// NextMarker is the last key or common prefix returned when truncated.
	NextMarker string
}

func clampMax(n int) int {
	if n <= 0 || n > maxListKeys {
		return maxListKeys
	}
	return n
}

// lister walks keys under base+prefix, grouping by delimiter, in key order.
type lister struct {
	it        *pebble.Iterator
	base      []byte // e.g. O\0bucket\0
	prefix    string
	delimiter string
}

// entry returns the name at the iterator position: the object key or, with a
// delimiter, the common prefix that contains it.
func (l *lister) entry() (name string, isPrefix bool) {
	key := string(l.it.Key()[len(l.base):])
	if l.delimiter != "" {
		rest := key[len(l.prefix):]
		if i := strings.Index(rest, l.delimiter); i >= 0 {
			return l.prefix + rest[:i+len(l.delimiter)], true
		}
	}
	return key, false
}

// List returns objects in bucket like S3 ListObjects.
func (s *Service) List(ctx context.Context, bucket string, opts ListOptions) (*ListResult, error) {
	if _, err := s.GetBucket(ctx, bucket); err != nil {
		return nil, err
	}
	max := clampMax(opts.MaxKeys)
	base := objPrefix(bucket)
	lower := append(append([]byte(nil), base...), opts.Prefix...)
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: prefixEnd(lower)})
	if err != nil {
		return nil, err
	}
	defer it.Close()
	l := &lister{it: it, base: base, prefix: opts.Prefix, delimiter: opts.Delimiter}

	res := &ListResult{}
	valid := it.First()
	if opts.Marker != "" {
		start := append(append(append([]byte(nil), base...), opts.Marker...), 0)
		if bytes.Compare(start, lower) > 0 {
			valid = it.SeekGE(start)
		}
	}
	count := 0
	for valid {
		name, isPrefix := l.entry()
		if isPrefix && name <= opts.Marker {
			// The marker was this common prefix; skip everything under it.
			valid = it.SeekGE(prefixEnd(append(append([]byte(nil), base...), name...)))
			continue
		}
		if count == max {
			res.IsTruncated = true
			break
		}
		if isPrefix {
			res.CommonPrefixes = append(res.CommonPrefixes, name)
			res.NextMarker = name
			count++
			valid = it.SeekGE(prefixEnd(append(append([]byte(nil), base...), name...)))
			continue
		}
		val, err := it.ValueAndErr()
		if err != nil {
			return nil, err
		}
		var o Object
		if err := jsonUnmarshal(val, &o); err != nil {
			return nil, err
		}
		o.Bucket, o.Key, o.IsLatest = bucket, name, true
		o.Inline = nil
		res.Objects = append(res.Objects, &o)
		res.NextMarker = name
		count++
		valid = it.Next()
	}
	if err := it.Error(); err != nil {
		return nil, err
	}
	if !res.IsTruncated {
		res.NextMarker = ""
	}
	return res, nil
}

type ListVersionsOptions struct {
	Prefix          string
	Delimiter       string
	KeyMarker       string
	VersionIDMarker string
	MaxKeys         int
}

type ListVersionsResult struct {
	Versions            []*Object
	CommonPrefixes      []string
	IsTruncated         bool
	NextKeyMarker       string
	NextVersionIDMarker string
}

// ListVersions returns all object versions like S3 ListObjectVersions.
func (s *Service) ListVersions(ctx context.Context, bucket string, opts ListVersionsOptions) (*ListVersionsResult, error) {
	if _, err := s.GetBucket(ctx, bucket); err != nil {
		return nil, err
	}
	max := clampMax(opts.MaxKeys)
	base := verPrefix(bucket)
	lower := append(append([]byte(nil), base...), opts.Prefix...)
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: prefixEnd(lower)})
	if err != nil {
		return nil, err
	}
	defer it.Close()

	valid := it.First()
	lastKey := ""
	if opts.KeyMarker != "" {
		var start []byte
		if opts.VersionIDMarker == "" {
			start = prefixEnd(verKeyPrefix(bucket, opts.KeyMarker))
		} else {
			v, err := s.findVersion(bucket, opts.KeyMarker, opts.VersionIDMarker)
			if err != nil {
				return nil, err
			}
			if v == nil {
				start = prefixEnd(verKeyPrefix(bucket, opts.KeyMarker))
			} else {
				start = append(verKey(bucket, opts.KeyMarker, v.Seq), 0)
				// Versions after the marker within the same key are not latest.
				lastKey = opts.KeyMarker
			}
		}
		if bytes.Compare(start, lower) > 0 {
			valid = it.SeekGE(start)
		}
	}

	res := &ListVersionsResult{}
	count := 0
	for valid {
		rest := it.Key()[len(base):]
		sep := bytes.LastIndexByte(rest, 0)
		key := string(rest[:sep])

		if opts.Delimiter != "" {
			if i := strings.Index(key[len(opts.Prefix):], opts.Delimiter); i >= 0 {
				cp := key[:len(opts.Prefix)+i+len(opts.Delimiter)]
				next := prefixEnd(append(append([]byte(nil), base...), cp...))
				if cp <= opts.KeyMarker {
					valid = it.SeekGE(next)
					continue
				}
				if count == max {
					res.IsTruncated = true
					break
				}
				res.CommonPrefixes = append(res.CommonPrefixes, cp)
				res.NextKeyMarker, res.NextVersionIDMarker = cp, ""
				count++
				valid = it.SeekGE(next)
				continue
			}
		}
		if count == max {
			res.IsTruncated = true
			break
		}
		val, err := it.ValueAndErr()
		if err != nil {
			return nil, err
		}
		var o Object
		if err := jsonUnmarshal(val, &o); err != nil {
			return nil, err
		}
		o.Bucket, o.Key, o.Inline = bucket, key, nil
		o.IsLatest = key != lastKey
		lastKey = key
		res.Versions = append(res.Versions, &o)
		res.NextKeyMarker, res.NextVersionIDMarker = key, o.VersionID
		count++
		valid = it.Next()
	}
	if err := it.Error(); err != nil {
		return nil, err
	}
	if !res.IsTruncated {
		res.NextKeyMarker, res.NextVersionIDMarker = "", ""
	}
	return res, nil
}
