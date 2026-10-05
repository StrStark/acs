package object

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// gcGrace delays blob deletion so readers that resolved an object just before
// it was deleted or overwritten can still open its parts.
const gcGrace = 15 * time.Minute

func (s *Service) runGC(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if n, err := s.collectGarbage(time.Now().Add(-gcGrace)); err != nil {
			slog.Warn("blob gc failed", "err", err)
		} else if n > 0 {
			slog.Debug("blob gc", "deleted", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// collectGarbage deletes blobs enqueued before cutoff and returns how many.
func (s *Service) collectGarbage(cutoff time.Time) (int, error) {
	type item struct {
		key  []byte
		blob string
	}
	var due []item
	p := gcPrefix()
	err := s.scan(p, prefixEnd(p), func(key, val []byte) (bool, error) {
		ts, err := time.Parse(time.RFC3339, string(val))
		if err == nil && ts.After(cutoff) {
			return true, nil
		}
		due = append(due, item{append([]byte(nil), key...), string(key[len(p):])})
		return len(due) < 10_000, nil
	})
	if err != nil {
		return 0, err
	}
	if len(due) == 0 {
		return 0, nil
	}
	batch := s.db.NewBatch()
	for _, it := range due {
		if err := s.blobs.Delete(it.blob); err != nil {
			slog.Warn("gc: delete blob", "blob", it.blob, "err", err)
			continue
		}
		batch.Delete(it.key, nil)
	}
	return len(due), s.commit(batch)
}

func (s *Service) runScrubber(ctx context.Context) {
	t := time.NewTimer(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if n, err := s.RemoveOrphans(time.Now().Add(-time.Hour)); err != nil {
			slog.Warn("orphan scrub failed", "err", err)
		} else if n > 0 {
			slog.Info("removed orphaned blobs", "count", n)
		}
		t.Reset(24 * time.Hour)
	}
}

// RemoveOrphans deletes blob files that no metadata references and that were
// written before cutoff (younger blobs may belong to in-flight uploads). Such
// orphans only appear after a crash between writing data and committing it.
func (s *Service) RemoveOrphans(cutoff time.Time) (int, error) {
	refs := make(map[string]struct{})
	addParts := func(parts []PartRef) {
		for _, p := range parts {
			refs[p.Blob] = struct{}{}
		}
	}
	for _, p := range [][]byte{k("V", ""), k("P", "")} {
		err := s.scan(p, prefixEnd(p), func(key, val []byte) (bool, error) {
			if key[0] == 'V' {
				var o Object
				if err := jsonUnmarshal(val, &o); err != nil {
					return false, err
				}
				addParts(o.Parts)
			} else {
				var part Part
				if err := jsonUnmarshal(val, &part); err != nil {
					return false, err
				}
				refs[part.Blob] = struct{}{}
			}
			return true, nil
		})
		if err != nil {
			return 0, err
		}
	}
	gp := gcPrefix()
	if err := s.scan(gp, prefixEnd(gp), func(key, _ []byte) (bool, error) {
		refs[string(key[len(gp):])] = struct{}{}
		return true, nil
	}); err != nil {
		return 0, err
	}

	n := 0
	err := s.blobs.Walk(func(id string, modTime time.Time) error {
		if _, ok := refs[id]; ok || modTime.After(cutoff) {
			return nil
		}
		if err := s.blobs.Delete(id); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}

func (s *Service) runLifecycle(ctx context.Context) {
	// First pass shortly after start, then hourly.
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := s.ApplyLifecycle(ctx, time.Now()); err != nil {
			slog.Warn("lifecycle run failed", "err", err)
		}
		t.Reset(time.Hour)
	}
}

// ApplyLifecycle evaluates every bucket's lifecycle rules as of now.
func (s *Service) ApplyLifecycle(ctx context.Context, now time.Time) error {
	buckets, err := s.ListBuckets(ctx)
	if err != nil {
		return err
	}
	for _, b := range buckets {
		for _, r := range b.Lifecycle {
			if !r.Enabled {
				continue
			}
			if err := s.applyRule(ctx, b.Name, r, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) applyRule(ctx context.Context, bucket string, r LifecycleRule, now time.Time) error {
	day := 24 * time.Hour
	if r.ExpirationDays > 0 {
		cutoff := now.Add(-time.Duration(r.ExpirationDays) * day)
		marker := ""
		for {
			res, err := s.List(ctx, bucket, ListOptions{Prefix: r.Prefix, Marker: marker})
			if err != nil {
				return err
			}
			for _, o := range res.Objects {
				if o.ModTime.Before(cutoff) {
					if _, err := s.DeleteObject(ctx, bucket, o.Key, ""); err != nil {
						return err
					}
				}
			}
			if !res.IsTruncated {
				break
			}
			marker = res.NextMarker
		}
	}

	if r.NoncurrentDays > 0 {
		cutoff := now.Add(-time.Duration(r.NoncurrentDays) * day)
		var keyMarker, vidMarker string
		// A version becomes noncurrent when the next newer version is written.
		var newerTime time.Time
		prevKey := ""
		type victim struct{ key, vid string }
		for {
			res, err := s.ListVersions(ctx, bucket, ListVersionsOptions{Prefix: r.Prefix, KeyMarker: keyMarker, VersionIDMarker: vidMarker})
			if err != nil {
				return err
			}
			var victims []victim
			for _, v := range res.Versions {
				if v.Key != prevKey {
					prevKey = v.Key
					newerTime = v.ModTime
					// An expired delete marker that is the only remaining version is cleaned up too.
					if v.IsLatest && v.DeleteMarker && v.ModTime.Before(cutoff) {
						if vs, err := s.versions(bucket, v.Key); err == nil && len(vs) == 1 {
							victims = append(victims, victim{v.Key, v.VersionID})
						}
					}
					continue
				}
				if newerTime.Before(cutoff) {
					victims = append(victims, victim{v.Key, v.VersionID})
				}
				newerTime = v.ModTime
			}
			for _, vc := range victims {
				if _, err := s.deleteVersion(ctx, bucket, vc.key, vc.vid); err != nil {
					return err
				}
			}
			if !res.IsTruncated {
				break
			}
			keyMarker, vidMarker = res.NextKeyMarker, res.NextVersionIDMarker
		}
	}

	if r.AbortMultipartDays > 0 {
		cutoff := now.Add(-time.Duration(r.AbortMultipartDays) * day)
		var stale []*Upload
		if err := s.scanUploads(bucket, r.Prefix, "", "", func(u *Upload) bool {
			if u.Initiated.Before(cutoff) && strings.HasPrefix(u.Key, r.Prefix) {
				stale = append(stale, u)
			}
			return true
		}); err != nil {
			return err
		}
		for _, u := range stale {
			if err := s.abortUpload(u); err != nil {
				return err
			}
		}
	}
	return nil
}
