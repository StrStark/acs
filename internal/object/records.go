package object

import (
	"encoding/binary"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// Versioning states of a bucket. A bucket starts unversioned and, once
// enabled, can only move between Enabled and Suspended (as in S3).
const (
	VersioningOff       = ""
	VersioningEnabled   = "Enabled"
	VersioningSuspended = "Suspended"
)

// NullVersion is the version ID of objects written while versioning is off or suspended.
const NullVersion = "null"

type Bucket struct {
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"createdAt"`
	Versioning string    `json:"versioning,omitempty"`
	// Public allows anonymous read access (GET/HEAD/list) to objects.
	Public       bool            `json:"public,omitempty"`
	QuotaBytes   int64           `json:"quotaBytes,omitempty"`
	QuotaObjects int64           `json:"quotaObjects,omitempty"`
	Lifecycle    []LifecycleRule `json:"lifecycle,omitempty"`
	CORS         []CORSRule      `json:"cors,omitempty"`
}

type LifecycleRule struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Prefix  string `json:"prefix,omitempty"`
	// ExpirationDays deletes current objects this many days after creation.
	ExpirationDays int `json:"expirationDays,omitempty"`
	// NoncurrentDays permanently removes versions this many days after they
	// became noncurrent.
	NoncurrentDays int `json:"noncurrentDays,omitempty"`
	// AbortMultipartDays aborts incomplete multipart uploads after this many days.
	AbortMultipartDays int `json:"abortMultipartDays,omitempty"`
}

type CORSRule struct {
	AllowedOrigins []string `json:"allowedOrigins"`
	AllowedMethods []string `json:"allowedMethods"`
	AllowedHeaders []string `json:"allowedHeaders,omitempty"`
	ExposeHeaders  []string `json:"exposeHeaders,omitempty"`
	MaxAgeSeconds  int      `json:"maxAgeSeconds,omitempty"`
}

// Object is one version of an object (or a delete marker).
type Object struct {
	Bucket       string    `json:"-"`
	Key          string    `json:"-"`
	IsLatest     bool      `json:"-"`
	VersionID    string    `json:"vid"`
	Seq          string    `json:"seq"`
	DeleteMarker bool      `json:"dm,omitempty"`
	Size         int64     `json:"sz"`
	ETag         string    `json:"etag,omitempty"`
	ContentType  string    `json:"ct,omitempty"`
	ModTime      time.Time `json:"mt"`
	// UserMeta holds x-amz-meta-* values, keyed by lower-case name without prefix.
	UserMeta map[string]string `json:"um,omitempty"`
	// Headers holds standard stored headers (Cache-Control, Content-Disposition, ...).
	Headers map[string]string `json:"hd,omitempty"`
	Tags    map[string]string `json:"tg,omitempty"`
	Inline  []byte            `json:"in,omitempty"`
	Parts   []PartRef         `json:"pt,omitempty"`
}

// PartRef points at a blob holding a contiguous slice of the object's data.
type PartRef struct {
	Blob string `json:"b"`
	Size int64  `json:"s"`
}

// PartsCount returns the number of multipart parts, or 0 for a simple upload.
func (o *Object) PartsCount() int {
	if i := strings.LastIndexByte(o.ETag, '-'); i >= 0 {
		n, _ := strconv.Atoi(o.ETag[i+1:])
		return n
	}
	return 0
}

type Upload struct {
	Bucket      string            `json:"-"`
	Key         string            `json:"-"`
	ID          string            `json:"id"`
	Initiated   time.Time         `json:"initiated"`
	ContentType string            `json:"ct,omitempty"`
	UserMeta    map[string]string `json:"um,omitempty"`
	Headers     map[string]string `json:"hd,omitempty"`
	Tags        map[string]string `json:"tg,omitempty"`
}

type Part struct {
	Number  int       `json:"n"`
	Size    int64     `json:"sz"`
	ETag    string    `json:"etag"`
	Blob    string    `json:"b"`
	ModTime time.Time `json:"mt"`
}

// Stats are maintained incrementally per bucket.
type Stats struct {
	// Objects and Bytes count current (latest, non-deleted) objects.
	Objects int64 `json:"objects"`
	Bytes   int64 `json:"bytes"`
	// StoredBytes includes noncurrent versions; quotas apply to it.
	StoredBytes int64 `json:"storedBytes"`
}

func (s Stats) encode() []byte {
	b := make([]byte, 24)
	binary.LittleEndian.PutUint64(b[0:], uint64(s.Objects))
	binary.LittleEndian.PutUint64(b[8:], uint64(s.Bytes))
	binary.LittleEndian.PutUint64(b[16:], uint64(s.StoredBytes))
	return b
}

func decodeStats(b []byte) Stats {
	if len(b) < 24 {
		return Stats{}
	}
	return Stats{
		Objects:     int64(binary.LittleEndian.Uint64(b[0:])),
		Bytes:       int64(binary.LittleEndian.Uint64(b[8:])),
		StoredBytes: int64(binary.LittleEndian.Uint64(b[16:])),
	}
}

func (s *Stats) add(o Stats) {
	s.Objects += o.Objects
	s.Bytes += o.Bytes
	s.StoredBytes += o.StoredBytes
}

// statsMerger sums Stats deltas so concurrent writers never read-modify-write
// the counters.
var statsMerger = &pebble.Merger{
	Name: "acs.stats.v1",
	Merge: func(key, value []byte) (pebble.ValueMerger, error) {
		m := &statsValueMerger{}
		m.sum.add(decodeStats(value))
		return m, nil
	},
}

type statsValueMerger struct{ sum Stats }

func (m *statsValueMerger) MergeNewer(v []byte) error { m.sum.add(decodeStats(v)); return nil }
func (m *statsValueMerger) MergeOlder(v []byte) error { m.sum.add(decodeStats(v)); return nil }
func (m *statsValueMerger) Finish(bool) ([]byte, io.Closer, error) {
	return m.sum.encode(), nil, nil
}
