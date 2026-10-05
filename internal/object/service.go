// Package object implements buckets, objects, versioning and multipart uploads
// on top of a Pebble metadata store and a local blob store.
//
// Its exported Service API is the boundary that the HTTP layers (S3, REST,
// share links) use; a future clustered implementation replaces what is behind it.
package object

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"acs/internal/blob"
)

// Event types emitted by the service.
const (
	EventObjectCreated = "object.created"
	EventObjectDeleted = "object.deleted"
	EventBucketCreated = "bucket.created"
	EventBucketDeleted = "bucket.deleted"
)

type Event struct {
	Type         string    `json:"type"`
	Time         time.Time `json:"time"`
	Bucket       string    `json:"bucket"`
	Key          string    `json:"key,omitempty"`
	VersionID    string    `json:"versionId,omitempty"`
	Size         int64     `json:"size,omitempty"`
	ETag         string    `json:"etag,omitempty"`
	DeleteMarker bool      `json:"deleteMarker,omitempty"`
}

type Service struct {
	db    *pebble.DB
	blobs *blob.Store

	// keyLocks serialise read-modify-write of a single object's metadata.
	keyLocks [512]sync.Mutex
	// bucketMu is held shared by object writes and exclusively while a bucket
	// is created or deleted, so objects cannot land in a vanishing bucket.
	bucketMu sync.RWMutex

	lastSeq atomic.Int64

	evMu     sync.RWMutex
	handlers []func(Event)
}

// Open opens the metadata store in metaDir and the blob store in dataDir.
func Open(metaDir, dataDir string) (*Service, error) {
	db, err := pebble.Open(metaDir, &pebble.Options{Merger: statsMerger, Logger: pebbleLogger{}})
	if err != nil {
		return nil, err
	}
	blobs, err := blob.Open(dataDir)
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Service{db: db, blobs: blobs}, nil
}

func (s *Service) Close() error { return s.db.Close() }

// pebbleLogger routes Pebble's chatter to slog at debug level.
type pebbleLogger struct{}

func (pebbleLogger) Infof(format string, args ...any) {
	slog.Debug("pebble: " + fmt.Sprintf(format, args...))
}
func (pebbleLogger) Errorf(format string, args ...any) {
	slog.Error("pebble: " + fmt.Sprintf(format, args...))
}
func (pebbleLogger) Fatalf(format string, args ...any) {
	slog.Error("pebble: " + fmt.Sprintf(format, args...))
	os.Exit(1)
}

// Subscribe registers fn to receive every event. fn must not block.
func (s *Service) Subscribe(fn func(Event)) {
	s.evMu.Lock()
	s.handlers = append(s.handlers, fn)
	s.evMu.Unlock()
}

func (s *Service) emit(e Event) {
	e.Time = time.Now().UTC()
	s.evMu.RLock()
	defer s.evMu.RUnlock()
	for _, h := range s.handlers {
		h(e)
	}
}

// Run starts background maintenance (blob GC and lifecycle rules) until ctx ends.
func (s *Service) Run(ctx context.Context) {
	go s.runGC(ctx)
	go s.runScrubber(ctx)
	go s.runLifecycle(ctx)
}

func (s *Service) stripe(bucket, key string) int {
	h := fnv.New32a()
	h.Write([]byte(bucket))
	h.Write([]byte{0})
	h.Write([]byte(key))
	return int(h.Sum32() % uint32(len(s.keyLocks)))
}

func (s *Service) lockKey(bucket, key string) func() {
	m := &s.keyLocks[s.stripe(bucket, key)]
	m.Lock()
	return m.Unlock
}

// lockKeys locks two keys in a fixed order so concurrent callers cannot deadlock.
func (s *Service) lockKeys(b1, k1, b2, k2 string) func() {
	i, j := s.stripe(b1, k1), s.stripe(b2, k2)
	if i == j {
		s.keyLocks[i].Lock()
		return s.keyLocks[i].Unlock
	}
	if i > j {
		i, j = j, i
	}
	s.keyLocks[i].Lock()
	s.keyLocks[j].Lock()
	return func() { s.keyLocks[j].Unlock(); s.keyLocks[i].Unlock() }
}

// newSeq returns a version sequence that sorts newest-first and is unique
// within this process: inverted nanosecond time plus random suffix.
func (s *Service) newSeq() string {
	var now int64
	for {
		last := s.lastSeq.Load()
		now = time.Now().UnixNano()
		if now <= last {
			now = last + 1
		}
		if s.lastSeq.CompareAndSwap(last, now) {
			break
		}
	}
	var b [12]byte
	inv := uint64(math.MaxUint64) - uint64(now)
	for i := 0; i < 8; i++ {
		b[i] = byte(inv >> (56 - 8*i))
	}
	rand.Read(b[8:])
	return hex.EncodeToString(b[:])
}

func randomID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- low-level store helpers ----

func (s *Service) getJSON(key []byte, v any) (bool, error) {
	val, closer, err := s.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer closer.Close()
	return true, json.Unmarshal(val, v)
}

func setJSON(b *pebble.Batch, key []byte, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		// Records are plain structs; marshalling cannot fail.
		panic(err)
	}
	b.Set(key, data, nil)
}

func (s *Service) commit(b *pebble.Batch) error {
	defer b.Close()
	return b.Commit(pebble.Sync)
}

// scan iterates keys in [start, end) calling fn until it returns false.
func (s *Service) scan(start, end []byte, fn func(key, val []byte) (bool, error)) error {
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: start, UpperBound: end})
	if err != nil {
		return err
	}
	defer it.Close()
	for it.First(); it.Valid(); it.Next() {
		val, err := it.ValueAndErr()
		if err != nil {
			return err
		}
		cont, err := fn(it.Key(), val)
		if err != nil || !cont {
			return err
		}
	}
	return it.Error()
}

// enqueueGC schedules the blobs of o for deletion once b commits.
func (s *Service) enqueueGC(b *pebble.Batch, o *Object) {
	if len(o.Parts) == 0 {
		return
	}
	ts := []byte(time.Now().UTC().Format(time.RFC3339))
	for _, p := range o.Parts {
		b.Set(gcKey(p.Blob), ts, nil)
	}
}

// deleteBlobsNow removes blobs that were never referenced by committed metadata.
func (s *Service) deleteBlobsNow(parts []PartRef) {
	for _, p := range parts {
		if err := s.blobs.Delete(p.Blob); err != nil {
			slog.Warn("delete blob", "blob", p.Blob, "err", err)
		}
	}
}
