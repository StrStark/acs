package s3

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"hash"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"acs/internal/auth"
)

const (
	algorithm        = "AWS4-HMAC-SHA256"
	unsignedPayload  = "UNSIGNED-PAYLOAD"
	streamingSigned  = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	streamingTrailer = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"
	streamingUnsignT = "STREAMING-UNSIGNED-PAYLOAD-TRAILER"
	emptySHA256      = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	amzDateFormat    = "20060102T150405Z"
	maxSkew          = 15 * time.Minute
)

// authResult describes how a request was authenticated.
type authResult struct {
	principal auth.Principal
	anonymous bool
	// Fields needed to verify streaming chunk signatures.
	payload    string
	signingKey []byte
	scope      string
	amzDate    string
	seedSig    string
}

// awsEncode percent-encodes s per SigV4 rules (RFC 3986 unreserved kept).
func awsEncode(s string, encodeSlash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' || (c == '/' && !encodeSlash) {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&15])
	}
	return b.String()
}

func canonicalQuery(r *http.Request) string {
	q, _ := url.ParseQuery(r.URL.RawQuery)
	keys := make([]string, 0, len(q))
	for k := range q {
		if k != "X-Amz-Signature" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := append([]string(nil), q[k]...)
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, awsEncode(k, true)+"="+awsEncode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

func headerValue(r *http.Request, name string) string {
	switch name {
	case "host":
		return r.Host
	case "transfer-encoding":
		return strings.Join(r.TransferEncoding, ",")
	case "content-length":
		if v := r.Header.Get("Content-Length"); v != "" {
			return v
		}
		if r.ContentLength >= 0 {
			return strconv.FormatInt(r.ContentLength, 10)
		}
	}
	// Copy: Values returns the header's backing slice.
	vals := append([]string(nil), r.Header.Values(name)...)
	for i, v := range vals {
		vals[i] = strings.Join(strings.Fields(v), " ")
	}
	return strings.Join(vals, ",")
}

func canonicalRequest(r *http.Request, signedHeaders []string, payloadHash string) string {
	var hdrs strings.Builder
	for _, h := range signedHeaders {
		hdrs.WriteString(h)
		hdrs.WriteByte(':')
		hdrs.WriteString(headerValue(r, h))
		hdrs.WriteByte('\n')
	}
	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	return strings.Join([]string{
		r.Method,
		awsEncode(path, false),
		canonicalQuery(r),
		hdrs.String(),
		strings.Join(signedHeaders, ";"),
		payloadHash,
	}, "\n")
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func sha256Hex(data string) string {
	h := sha256.Sum256([]byte(data))
	return hex.EncodeToString(h[:])
}

func signingKey(secret, date, region, service string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	return hmacSHA256(k, "aws4_request")
}

type credential struct {
	accessKey, date, region, service string
}

func parseCredential(s string) (credential, bool) {
	parts := strings.Split(s, "/")
	if len(parts) != 5 || parts[4] != "aws4_request" || parts[3] != "s3" {
		return credential{}, false
	}
	return credential{parts[0], parts[1], parts[2], parts[3]}, true
}

func (c credential) scope() string {
	return c.date + "/" + c.region + "/" + c.service + "/aws4_request"
}

// authenticate verifies SigV4 (header or presigned) or returns an anonymous result.
func (s *Server) authenticate(r *http.Request) (*authResult, *Error) {
	authz := r.Header.Get("Authorization")
	switch {
	case strings.HasPrefix(authz, algorithm):
		return s.authHeader(r, authz)
	case r.URL.Query().Get("X-Amz-Algorithm") != "":
		return s.authPresigned(r)
	case strings.HasPrefix(authz, "AWS "):
		return nil, newErr(http.StatusBadRequest, "InvalidRequest", "Signature Version 2 is not supported; use Signature Version 4.")
	case authz != "":
		return nil, ErrAuthMalformed
	}
	return &authResult{anonymous: true}, nil
}

func (s *Server) lookup(r *http.Request, accessKey string) (auth.Principal, string, *Error) {
	p, secret, err := s.auth.LookupKey(r.Context(), accessKey)
	switch {
	case errors.Is(err, auth.ErrNoSuchKey):
		return auth.Principal{}, "", ErrInvalidAccessKeyID
	case errors.Is(err, auth.ErrKeyExpired):
		return auth.Principal{}, "", ErrExpiredToken
	case err != nil:
		return auth.Principal{}, "", newErr(http.StatusInternalServerError, "InternalError", "Authentication failed.")
	}
	return p, secret, nil
}

func (s *Server) authHeader(r *http.Request, authz string) (*authResult, *Error) {
	fields := map[string]string{}
	for _, part := range strings.Split(strings.TrimSpace(strings.TrimPrefix(authz, algorithm)), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			fields[k] = v
		}
	}
	cred, ok := parseCredential(fields["Credential"])
	if !ok || fields["SignedHeaders"] == "" || fields["Signature"] == "" {
		return nil, ErrAuthMalformed
	}

	amzDate := r.Header.Get("X-Amz-Date")
	var t time.Time
	var err error
	if amzDate != "" {
		t, err = time.Parse(amzDateFormat, amzDate)
	} else if d := r.Header.Get("Date"); d != "" {
		t, err = http.ParseTime(d)
		amzDate = t.UTC().Format(amzDateFormat)
	} else {
		return nil, newErr(http.StatusForbidden, "AccessDenied", "AWS authentication requires a valid Date or x-amz-date header")
	}
	if err != nil {
		return nil, newErr(http.StatusForbidden, "AccessDenied", "AWS authentication requires a valid Date or x-amz-date header")
	}
	if d := time.Since(t); d > maxSkew || d < -maxSkew {
		return nil, ErrRequestTimeTooSkewed
	}
	if amzDate[:8] != cred.date {
		return nil, ErrAuthMalformed
	}

	payload := r.Header.Get("X-Amz-Content-Sha256")
	if payload == "" {
		if r.ContentLength > 0 || len(r.TransferEncoding) > 0 {
			return nil, ErrMissingSecurityHeader
		}
		payload = emptySHA256
	}

	p, secret, e := s.lookup(r, cred.accessKey)
	if e != nil {
		return nil, e
	}
	signed := strings.Split(fields["SignedHeaders"], ";")
	key := signingKey(secret, cred.date, cred.region, cred.service)
	sts := strings.Join([]string{algorithm, amzDate, cred.scope(), sha256Hex(canonicalRequest(r, signed, payload))}, "\n")
	want := hex.EncodeToString(hmacSHA256(key, sts))
	if !hmac.Equal([]byte(want), []byte(fields["Signature"])) {
		return nil, ErrSignatureDoesNotMatch
	}
	return &authResult{principal: p, payload: payload, signingKey: key, scope: cred.scope(), amzDate: amzDate, seedSig: want}, nil
}

func (s *Server) authPresigned(r *http.Request) (*authResult, *Error) {
	q := r.URL.Query()
	if q.Get("X-Amz-Algorithm") != algorithm {
		return nil, ErrAuthQueryMalformed
	}
	cred, ok := parseCredential(q.Get("X-Amz-Credential"))
	if !ok {
		return nil, ErrAuthQueryMalformed
	}
	t, err := time.Parse(amzDateFormat, q.Get("X-Amz-Date"))
	if err != nil {
		return nil, ErrAuthQueryMalformed
	}
	expires, err := strconv.Atoi(q.Get("X-Amz-Expires"))
	if err != nil || expires < 0 || expires > 7*24*3600 {
		return nil, newErr(http.StatusBadRequest, "AuthorizationQueryParametersError", "X-Amz-Expires must be between 0 and 604800 seconds.")
	}
	if time.Now().After(t.Add(time.Duration(expires) * time.Second)) {
		return nil, newErr(http.StatusForbidden, "AccessDenied", "Request has expired")
	}
	if t.After(time.Now().Add(maxSkew)) {
		return nil, ErrRequestTimeTooSkewed
	}
	payload := q.Get("X-Amz-Content-Sha256")
	if payload == "" {
		payload = unsignedPayload
	}

	p, secret, e := s.lookup(r, cred.accessKey)
	if e != nil {
		return nil, e
	}
	signed := strings.Split(q.Get("X-Amz-SignedHeaders"), ";")
	key := signingKey(secret, cred.date, cred.region, cred.service)
	amzDate := q.Get("X-Amz-Date")
	sts := strings.Join([]string{algorithm, amzDate, cred.scope(), sha256Hex(canonicalRequest(r, signed, payload))}, "\n")
	want := hex.EncodeToString(hmacSHA256(key, sts))
	if !hmac.Equal([]byte(want), []byte(q.Get("X-Amz-Signature"))) {
		return nil, ErrSignatureDoesNotMatch
	}
	return &authResult{principal: p, payload: payload}, nil
}

// ---- payload verification ----

// bodyReader wraps the request body according to the payload mode, verifying
// hashes, chunk signatures and checksums. It returns the decoded size (-1 if
// unknown).
func (a *authResult) bodyReader(r *http.Request) (io.Reader, int64, *Error) {
	size := r.ContentLength
	var body io.Reader = r.Body
	switch a.payload {
	case streamingSigned, streamingTrailer, streamingUnsignT:
		dec := r.Header.Get("X-Amz-Decoded-Content-Length")
		n, err := strconv.ParseInt(dec, 10, 64)
		if err != nil || n < 0 {
			return nil, 0, ErrMissingContentLength
		}
		size = n
		cr := &chunkedReader{
			br:      bufio.NewReader(r.Body),
			signed:  a.payload != streamingUnsignT,
			trailer: a.payload != streamingSigned,
			key:     a.signingKey,
			scope:   a.scope,
			amzDate: a.amzDate,
			prevSig: a.seedSig,
		}
		if cr.signed {
			cr.chunkHash = sha256.New()
		}
		if name := strings.ToLower(r.Header.Get("X-Amz-Trailer")); name != "" {
			cr.checksumName = name
			cr.checksum = checksumHash(strings.TrimPrefix(name, "x-amz-checksum-"))
		}
		body = cr
	case unsignedPayload, "":
	default:
		if len(a.payload) != 64 {
			return nil, 0, ErrContentSHA256Mismatch
		}
		body = &sha256Verifier{r: body, want: a.payload, h: sha256.New()}
	}
	// A checksum sent as a plain header (not a trailer) covers the whole body.
	for _, alg := range []string{"crc32", "crc32c", "sha1", "sha256"} {
		if v := r.Header.Get("X-Amz-Checksum-" + alg); v != "" {
			body = &checksumVerifier{r: body, h: checksumHash(alg), want: v}
			break
		}
	}
	return body, size, nil
}

func checksumHash(alg string) hash.Hash {
	switch alg {
	case "crc32":
		return crc32.NewIEEE()
	case "crc32c":
		return crc32.New(crc32.MakeTable(crc32.Castagnoli))
	case "sha1":
		return sha1.New()
	case "sha256":
		return sha256.New()
	}
	// Unknown algorithms (e.g. crc64nvme) are accepted without verification.
	return nil
}

type sha256Verifier struct {
	r    io.Reader
	h    hash.Hash
	want string
}

func (v *sha256Verifier) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	v.h.Write(p[:n])
	if err == io.EOF && hex.EncodeToString(v.h.Sum(nil)) != v.want {
		return n, ErrContentSHA256Mismatch
	}
	return n, err
}

type checksumVerifier struct {
	r    io.Reader
	h    hash.Hash
	want string
}

func (v *checksumVerifier) Read(p []byte) (int, error) {
	n, err := v.r.Read(p)
	if v.h != nil {
		v.h.Write(p[:n])
		if err == io.EOF && base64.StdEncoding.EncodeToString(v.h.Sum(nil)) != v.want {
			return n, ErrBadChecksum
		}
	}
	return n, err
}

// chunkedReader decodes aws-chunked bodies, verifying per-chunk signatures
// (signed variants) and the trailing checksum (trailer variants).
type chunkedReader struct {
	br      *bufio.Reader
	signed  bool
	trailer bool
	key     []byte
	scope   string
	amzDate string
	prevSig string

	remaining int64
	inChunk   bool // a data chunk was read and its CRLF/signature is pending
	chunkSig  string
	chunkHash hash.Hash

	checksumName string
	checksum     hash.Hash

	err error
}

var errChunkFormat = newErr(http.StatusBadRequest, "IncompleteBody", "The request body is not valid aws-chunked encoding.")

func (c *chunkedReader) readLine() (string, error) {
	line, err := c.br.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			return "", errChunkFormat
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (c *chunkedReader) verifyChunk(dataHash string) error {
	if !c.signed {
		return nil
	}
	sts := strings.Join([]string{algorithm + "-PAYLOAD", c.amzDate, c.scope, c.prevSig, emptySHA256, dataHash}, "\n")
	want := hex.EncodeToString(hmacSHA256(c.key, sts))
	if !hmac.Equal([]byte(want), []byte(c.chunkSig)) {
		return ErrSignatureDoesNotMatch
	}
	c.prevSig = want
	return nil
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	for c.remaining == 0 {
		if err := c.nextChunk(); err != nil {
			c.err = err
			return 0, err
		}
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.br.Read(p)
	c.remaining -= int64(n)
	if c.chunkHash != nil {
		c.chunkHash.Write(p[:n])
	}
	if c.checksum != nil {
		c.checksum.Write(p[:n])
	}
	if err == io.EOF {
		err = errChunkFormat
	}
	if err != nil {
		c.err = err
	}
	return n, err
}

// nextChunk finishes the previous chunk and reads the next chunk header. At
// the final chunk it processes trailers and returns io.EOF.
func (c *chunkedReader) nextChunk() error {
	if c.inChunk {
		if line, err := c.readLine(); err != nil || line != "" {
			return errChunkFormat
		}
		var sum string
		if c.chunkHash != nil {
			sum = hex.EncodeToString(c.chunkHash.Sum(nil))
			c.chunkHash.Reset()
		}
		if err := c.verifyChunk(sum); err != nil {
			return err
		}
		c.inChunk = false
	}
	line, err := c.readLine()
	if err != nil {
		return err
	}
	sizeHex, ext, _ := strings.Cut(line, ";")
	size, err := strconv.ParseInt(strings.TrimSpace(sizeHex), 16, 64)
	if err != nil || size < 0 {
		return errChunkFormat
	}
	c.chunkSig = strings.TrimPrefix(ext, "chunk-signature=")
	if c.signed && c.chunkSig == "" {
		return ErrIncompleteSignature
	}
	if size > 0 {
		c.remaining, c.inChunk = size, true
		return nil
	}

	// Final zero-length chunk.
	if err := c.verifyChunk(emptySHA256); err != nil {
		return err
	}
	if !c.trailer {
		c.readLine() // optional terminating CRLF
		return io.EOF
	}
	var trailerBuf strings.Builder
	var trailerSig, gotChecksum string
	for {
		line, err := c.readLine()
		if err != nil {
			return err
		}
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return errChunkFormat
		}
		name = strings.ToLower(strings.TrimSpace(name))
		value = strings.TrimSpace(value)
		if name == "x-amz-trailer-signature" {
			trailerSig = value
			continue
		}
		trailerBuf.WriteString(name + ":" + value + "\n")
		if name == c.checksumName {
			gotChecksum = value
		}
	}
	if c.signed {
		sts := strings.Join([]string{algorithm + "-TRAILER", c.amzDate, c.scope, c.prevSig, sha256Hex(trailerBuf.String())}, "\n")
		want := hex.EncodeToString(hmacSHA256(c.key, sts))
		if !hmac.Equal([]byte(want), []byte(trailerSig)) {
			return ErrSignatureDoesNotMatch
		}
	}
	if c.checksum != nil && base64.StdEncoding.EncodeToString(c.checksum.Sum(nil)) != gotChecksum {
		return ErrBadChecksum
	}
	return io.EOF
}
