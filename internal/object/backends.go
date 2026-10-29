package object

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"acs/internal/blob"
)

// Bucket modes.
const (
	// ModeStore keeps metadata in ACS and object data in the bucket's backend.
	ModeStore = ""
	// ModeMirror makes the bucket a live view of a remote S3 bucket/prefix:
	// objects keep their real keys and ACS stores no metadata for them.
	ModeMirror = "mirror"
)

// ErrBackend wraps failures reported by a storage backend.
var ErrBackend = errors.New("storage backend error")

// ErrUnsupported is returned for features a mirror bucket cannot offer.
var ErrUnsupported = errors.New("not supported for mirror buckets")

// Backends resolves configured storage backends. The built-in local store
// (ID "") is owned by the Service and never asked for.
type Backends interface {
	// Store returns the blob store of a backend (store-mode buckets).
	Store(id string) (blob.Store, error)
	// Mirror returns a view of a remote backend rooted at prefix (mirror buckets).
	Mirror(id, prefix string) (Remote, error)
	// StoreIDs lists configured backends, for the orphan scrubber.
	StoreIDs() []string
}

// RemoteObject describes an object in a mirrored remote bucket.
type RemoteObject struct {
	Key         string
	Size        int64
	ETag        string
	ContentType string
	ModTime     time.Time
	UserMeta    map[string]string
	Headers     map[string]string
}

// RemoteMeta is metadata written with a remote object.
type RemoteMeta struct {
	ContentType string
	UserMeta    map[string]string
	Headers     map[string]string
	Tags        map[string]string
}

// Remote is a mirrored S3 bucket/prefix. Keys are relative to the view's
// prefix. Implementations translate provider errors to this package's errors.
type Remote interface {
	Stat(ctx context.Context, key string) (*RemoteObject, error)
	Get(ctx context.Context, key string, offset int64) (io.ReadCloser, error)
	Put(ctx context.Context, key string, r io.Reader, size int64, md5 []byte, meta RemoteMeta) (*RemoteObject, error)
	Delete(ctx context.Context, key string) error
	// Copy copies srcKey of src (possibly another view) to dstKey of this view.
	// A nil meta keeps the source metadata.
	Copy(ctx context.Context, src Remote, srcKey, dstKey string, meta *RemoteMeta) (*RemoteObject, error)
	List(ctx context.Context, prefix, delimiter, startAfter string, max int) (objs []RemoteObject, prefixes []string, truncated bool, err error)
	GetTags(ctx context.Context, key string) (map[string]string, error)
	PutTags(ctx context.Context, key string, tags map[string]string) error

	CreateUpload(ctx context.Context, key string, meta RemoteMeta) (string, error)
	UploadPart(ctx context.Context, key, uploadID string, num int, r io.Reader, size int64) (*Part, error)
	CompleteUpload(ctx context.Context, key, uploadID string, parts []CompletePart) (*RemoteObject, error)
	AbortUpload(ctx context.Context, key, uploadID string) error
	ListParts(ctx context.Context, key, uploadID string, marker, max int) ([]*Part, bool, error)
	ListUploads(ctx context.Context, prefix, keyMarker, uploadIDMarker string, max int) ([]*Upload, bool, error)
}

// SetBackends installs the backend registry. Without it only the built-in
// store is available.
func (s *Service) SetBackends(b Backends) { s.backends = b }

// store returns the blob store for a backend ID ("" = built-in).
func (s *Service) store(id string) (blob.Store, error) {
	if id == "" {
		return s.local, nil
	}
	if s.backends == nil {
		return nil, fmt.Errorf("%w: unknown storage backend %q", ErrBackend, id)
	}
	return s.backends.Store(id)
}

func (s *Service) mirror(b *Bucket) (Remote, error) {
	if s.backends == nil {
		return nil, fmt.Errorf("%w: unknown storage backend %q", ErrBackend, b.Backend)
	}
	return s.backends.Mirror(b.Backend, b.RemotePrefix)
}

// BucketOptions choose where a new bucket keeps its data.
type BucketOptions struct {
	// Backend is a storage backend ID; "" is the built-in disk.
	Backend string
	Mode    string
	// RemotePrefix is the key prefix inside the remote bucket (mirror mode).
	RemotePrefix string
}

func (o *BucketOptions) normalize() error {
	switch o.Mode {
	case ModeStore:
		o.RemotePrefix = ""
	case ModeMirror:
		if o.Backend == "" {
			return fmt.Errorf("%w: mirror buckets need an S3 storage backend", ErrInvalidArgument)
		}
		p := strings.TrimLeft(o.RemotePrefix, "/")
		if p != "" && !strings.HasSuffix(p, "/") {
			p += "/"
		}
		if strings.Contains(p, "//") || strings.HasPrefix(p, ".acs/") {
			return fmt.Errorf("%w: invalid remote prefix", ErrInvalidArgument)
		}
		o.RemotePrefix = p
	default:
		return fmt.Errorf("%w: unknown bucket mode %q", ErrInvalidArgument, o.Mode)
	}
	return nil
}
