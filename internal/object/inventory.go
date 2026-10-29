package object

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"time"

	"golang.org/x/time/rate"
)

// Issue kinds reported by integrity checks.
const (
	IssueMissing     = "missing"     // a blob referenced by metadata does not exist
	IssueCorrupt     = "corrupt"     // a blob has the wrong size or checksum
	IssueUnavailable = "unavailable" // the backend holding data cannot be reached
)

type Issue struct {
	Kind      string `json:"kind"`
	Bucket    string `json:"bucket,omitempty"`
	Key       string `json:"key,omitempty"`
	VersionID string `json:"versionId,omitempty"`
	Store     string `json:"store"`
	Blob      string `json:"blob,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type InventoryResult struct {
	Objects int           `json:"objects"`
	Blobs   int           `json:"blobs"`
	Issues  []Issue       `json:"issues"`
	Took    time.Duration `json:"took"`
}

type blobInfo struct{ size int64 }

// Inventory compares every blob referenced by object metadata with what the
// stores actually hold. With deep, blob contents are re-read (at most
// bytesPerSec) and checked against their stored MD5.
func (s *Service) Inventory(ctx context.Context, deep bool, bytesPerSec int) (*InventoryResult, error) {
	start := time.Now()
	res := &InventoryResult{Issues: []Issue{}}
	stores := []string{""}
	if s.backends != nil {
		stores = append(stores, s.backends.StoreIDs()...)
	}
	present := map[string]map[string]blobInfo{}
	unavailable := map[string]string{}
	for _, id := range stores {
		st, err := s.store(id)
		if err == nil {
			m := map[string]blobInfo{}
			err = st.Walk(ctx, func(blobID string, _ time.Time, size int64) error {
				m[blobID] = blobInfo{size: size}
				return nil
			})
			if err == nil {
				present[id] = m
				continue
			}
		}
		unavailable[id] = err.Error()
	}

	// Backends referenced by buckets but not configured at all.
	buckets, err := s.ListBuckets(ctx)
	if err != nil {
		return nil, err
	}
	for _, b := range buckets {
		if b.Mirror() {
			continue
		}
		if _, ok := present[b.Backend]; !ok {
			if _, known := unavailable[b.Backend]; !known {
				unavailable[b.Backend] = "storage backend is not attached"
			}
		}
	}
	for id, msg := range unavailable {
		res.Issues = append(res.Issues, Issue{Kind: IssueUnavailable, Store: id, Detail: msg})
	}

	var limiter *rate.Limiter
	if deep && bytesPerSec > 0 {
		limiter = rate.NewLimiter(rate.Limit(bytesPerSec), 1<<20)
	}
	vp := verPrefix("")[:2] // "V\0": every version of every bucket
	err = s.scan(vp, prefixEnd(vp), func(key, val []byte) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		var o Object
		if err := jsonUnmarshal(val, &o); err != nil {
			return false, err
		}
		if o.DeleteMarker {
			return true, nil
		}
		res.Objects++
		bucket, objKey := splitVersionKey(key)
		for _, p := range o.Parts {
			res.Blobs++
			blobs, ok := present[p.Store]
			if !ok {
				continue // reported once as unavailable
			}
			issue := Issue{Bucket: bucket, Key: objKey, VersionID: o.VersionID, Store: p.Store, Blob: p.Blob}
			info, found := blobs[p.Blob]
			switch {
			case !found:
				issue.Kind = IssueMissing
				res.Issues = append(res.Issues, issue)
			case info.size != p.Size:
				issue.Kind, issue.Detail = IssueCorrupt, "size mismatch"
				res.Issues = append(res.Issues, issue)
			case deep && p.Sum != "":
				if bad, detail := s.verifyBlob(ctx, p, limiter); bad {
					issue.Kind, issue.Detail = IssueCorrupt, detail
					res.Issues = append(res.Issues, issue)
				}
			}
		}
		return true, nil
	})
	res.Took = time.Since(start).Round(time.Millisecond)
	return res, err
}

func (s *Service) verifyBlob(ctx context.Context, p PartRef, limiter *rate.Limiter) (bool, string) {
	st, err := s.store(p.Store)
	if err != nil {
		return false, ""
	}
	rc, err := st.Open(ctx, p.Blob, 0)
	if err != nil {
		return true, "unreadable: " + err.Error()
	}
	defer rc.Close()
	h := md5.New()
	buf := make([]byte, 1<<20)
	for {
		n, err := rc.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			if limiter != nil {
				limiter.WaitN(ctx, n)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return true, "read failed: " + err.Error()
		}
	}
	if hex.EncodeToString(h.Sum(nil)) != p.Sum {
		return true, "checksum mismatch"
	}
	return false, ""
}

// splitVersionKey extracts bucket and object key from a V record key.
func splitVersionKey(key []byte) (bucket, objKey string) {
	rest := key[2:]
	i := bytes.IndexByte(rest, 0)
	bucket, rest = string(rest[:i]), rest[i+1:]
	return bucket, string(rest[:bytes.LastIndexByte(rest, 0)])
}
