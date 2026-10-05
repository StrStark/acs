package s3

import (
	"errors"
	"net/http"

	"acs/internal/object"
)

// Error is an S3 error response.
type Error struct {
	Code    string
	Message string
	Status  int
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func newErr(status int, code, msg string) *Error {
	return &Error{Code: code, Message: msg, Status: status}
}

var (
	ErrAccessDenied          = newErr(http.StatusForbidden, "AccessDenied", "Access Denied.")
	ErrSignatureDoesNotMatch = newErr(http.StatusForbidden, "SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided. Check your key and signing method.")
	ErrInvalidAccessKeyID    = newErr(http.StatusForbidden, "InvalidAccessKeyId", "The AWS Access Key Id you provided does not exist in our records.")
	ErrExpiredToken          = newErr(http.StatusForbidden, "ExpiredToken", "The provided token has expired.")
	ErrRequestTimeTooSkewed  = newErr(http.StatusForbidden, "RequestTimeTooSkewed", "The difference between the request time and the server's time is too large.")
	ErrAuthMalformed         = newErr(http.StatusBadRequest, "AuthorizationHeaderMalformed", "The authorization header is malformed.")
	ErrAuthQueryMalformed    = newErr(http.StatusBadRequest, "AuthorizationQueryParametersError", "Error parsing the X-Amz-Credential parameter.")
	ErrMissingSecurityHeader = newErr(http.StatusBadRequest, "MissingSecurityHeader", "Your request was missing a required header.")
	ErrContentSHA256Mismatch = newErr(http.StatusBadRequest, "XAmzContentSHA256Mismatch", "The provided 'x-amz-content-sha256' header does not match what was computed.")
	ErrBadChecksum           = newErr(http.StatusBadRequest, "BadDigest", "The checksum you specified did not match the calculated checksum.")
	ErrIncompleteSignature   = newErr(http.StatusBadRequest, "IncompleteSignature", "The request signature does not conform to AWS standards.")
	ErrMalformedXML          = newErr(http.StatusBadRequest, "MalformedXML", "The XML you provided was not well-formed or did not validate against our published schema.")
	ErrInvalidRange          = newErr(http.StatusRequestedRangeNotSatisfiable, "InvalidRange", "The requested range is not satisfiable.")
	ErrPreconditionFailed    = newErr(http.StatusPreconditionFailed, "PreconditionFailed", "At least one of the pre-conditions you specified did not hold.")
	ErrNotImplemented        = newErr(http.StatusNotImplemented, "NotImplemented", "A header or query you provided implies functionality that is not implemented.")
	ErrMissingContentLength  = newErr(http.StatusLengthRequired, "MissingContentLength", "You must provide the Content-Length HTTP header.")
	ErrEntityTooLarge        = newErr(http.StatusBadRequest, "EntityTooLarge", "Your proposed upload exceeds the maximum allowed object size.")
	ErrInvalidCopySource     = newErr(http.StatusBadRequest, "InvalidArgument", "Copy Source must mention the source bucket and key: sourcebucket/sourcekey.")
	ErrCopyToSelf            = newErr(http.StatusBadRequest, "InvalidRequest", "This copy request is illegal because it is trying to copy an object to itself without changing the object's metadata, storage class, website redirect location or encryption attributes.")
	ErrInvalidPartNumber     = newErr(http.StatusBadRequest, "InvalidArgument", "Part number must be an integer between 1 and 10000, inclusive.")
	ErrTooManyKeys           = newErr(http.StatusBadRequest, "MalformedXML", "The request contains more than 1000 keys.")
	ErrInvalidTag            = newErr(http.StatusBadRequest, "InvalidTag", "The tag provided was not a valid tag.")
	ErrMethodNotAllowed      = newErr(http.StatusMethodNotAllowed, "MethodNotAllowed", "The specified method is not allowed against this resource.")
	ErrNoSuchLifecycle       = newErr(http.StatusNotFound, "NoSuchLifecycleConfiguration", "The lifecycle configuration does not exist.")
	ErrNoSuchCORS            = newErr(http.StatusNotFound, "NoSuchCORSConfiguration", "The CORS configuration does not exist.")
	ErrNoSuchBucketPolicy    = newErr(http.StatusNotFound, "NoSuchBucketPolicy", "The bucket policy does not exist.")
	ErrNoSuchTagSet          = newErr(http.StatusNotFound, "NoSuchTagSet", "The TagSet does not exist.")
	ErrCORSForbidden         = newErr(http.StatusForbidden, "AccessForbidden", "CORSResponse: This CORS request is not allowed.")
)

// fromObjectErr maps storage engine errors to S3 errors.
func fromObjectErr(err error) *Error {
	var s3err *Error
	switch {
	case errors.As(err, &s3err):
		return s3err
	case errors.Is(err, object.ErrNoSuchBucket):
		return newErr(http.StatusNotFound, "NoSuchBucket", "The specified bucket does not exist.")
	case errors.Is(err, object.ErrNoSuchKey):
		return newErr(http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
	case errors.Is(err, object.ErrNoSuchVersion):
		return newErr(http.StatusNotFound, "NoSuchVersion", "The specified version does not exist.")
	case errors.Is(err, object.ErrDeleteMarker):
		return ErrMethodNotAllowed
	case errors.Is(err, object.ErrNoSuchUpload):
		return newErr(http.StatusNotFound, "NoSuchUpload", "The specified multipart upload does not exist.")
	case errors.Is(err, object.ErrBucketExists):
		return newErr(http.StatusConflict, "BucketAlreadyOwnedByYou", "Your previous request to create the named bucket succeeded and you already own it.")
	case errors.Is(err, object.ErrBucketNotEmpty):
		return newErr(http.StatusConflict, "BucketNotEmpty", "The bucket you tried to delete is not empty.")
	case errors.Is(err, object.ErrInvalidBucketName):
		return newErr(http.StatusBadRequest, "InvalidBucketName", "The specified bucket is not valid.")
	case errors.Is(err, object.ErrInvalidKey):
		return newErr(http.StatusBadRequest, "KeyTooLongError", "Your key is too long or invalid.")
	case errors.Is(err, object.ErrInvalidPart):
		return newErr(http.StatusBadRequest, "InvalidPart", "One or more of the specified parts could not be found.")
	case errors.Is(err, object.ErrInvalidPartOrder):
		return newErr(http.StatusBadRequest, "InvalidPartOrder", "The list of parts was not in ascending order.")
	case errors.Is(err, object.ErrEntityTooSmall):
		return newErr(http.StatusBadRequest, "EntityTooSmall", "Your proposed upload is smaller than the minimum allowed object size.")
	case errors.Is(err, object.ErrBadDigest):
		return newErr(http.StatusBadRequest, "BadDigest", "The Content-MD5 you specified did not match what we received.")
	case errors.Is(err, object.ErrIncompleteBody):
		return newErr(http.StatusBadRequest, "IncompleteBody", "You did not provide the number of bytes specified by the Content-Length HTTP header.")
	case errors.Is(err, object.ErrQuotaExceeded):
		return newErr(http.StatusForbidden, "QuotaExceeded", "The bucket quota has been exceeded.")
	case errors.Is(err, object.ErrInvalidArgument):
		return newErr(http.StatusBadRequest, "InvalidArgument", err.Error())
	}
	return newErr(http.StatusInternalServerError, "InternalError", "We encountered an internal error. Please try again.")
}
