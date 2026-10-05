package object

import "errors"

var (
	ErrInvalidBucketName = errors.New("invalid bucket name")
	ErrInvalidKey        = errors.New("invalid object key")
	ErrNoSuchBucket      = errors.New("bucket does not exist")
	ErrBucketExists      = errors.New("bucket already exists")
	ErrBucketNotEmpty    = errors.New("bucket is not empty")
	ErrNoSuchKey         = errors.New("object does not exist")
	ErrNoSuchVersion     = errors.New("version does not exist")
	// ErrDeleteMarker is returned when a specific version requested is a delete marker.
	ErrDeleteMarker     = errors.New("version is a delete marker")
	ErrNoSuchUpload     = errors.New("multipart upload does not exist")
	ErrInvalidPart      = errors.New("one or more parts could not be found or do not match")
	ErrInvalidPartOrder = errors.New("parts must be listed in ascending order")
	ErrEntityTooSmall   = errors.New("part is smaller than the minimum allowed size")
	ErrQuotaExceeded    = errors.New("bucket quota exceeded")
	ErrBadDigest        = errors.New("content MD5 does not match")
	ErrIncompleteBody   = errors.New("request body is shorter than the declared length")
	ErrInvalidArgument  = errors.New("invalid argument")
)
