package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"acs/internal/auth"
	"acs/internal/object"
	"acs/internal/share"
	"acs/internal/webhook"
)

type shareJSON struct {
	*share.Share
	URL string `json:"url"`
}

// publicBase is the externally visible origin of the panel.
func (s *Server) publicBase(r *http.Request) string {
	if u := s.settings.Get().PublicURL; u != "" {
		return u
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) toShareJSON(r *http.Request, sh *share.Share) shareJSON {
	return shareJSON{Share: sh, URL: s.publicBase(r) + "/s/" + sh.Token}
}

func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	q := r.URL.Query()
	f := share.ListFilter{Bucket: q.Get("bucket"), Key: q.Get("key")}
	if !p.Can(auth.ActAdmin, "") {
		f.CreatedBy = p.User.ID
	}
	list, err := s.shares.List(r.Context(), f)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := []shareJSON{}
	for _, sh := range list {
		out = append(out, s.toShareJSON(r, sh))
	}
	writeJSON(w, http.StatusOK, out)
}

type createShareRequest struct {
	Type           string     `json:"type"`
	Bucket         string     `json:"bucket"`
	Key            string     `json:"key"`
	ExpiresAt      *time.Time `json:"expiresAt"`
	Password       string     `json:"password"`
	MaxDownloads   *int64     `json:"maxDownloads"`
	MaxUploadBytes *int64     `json:"maxUploadBytes"`
	Note           string     `json:"note"`
}

func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	var req createShareRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !can(w, r, auth.ActWrite, req.Bucket) {
		return
	}
	ctx := r.Context()
	if _, err := s.objects.GetBucket(ctx, req.Bucket); err != nil {
		writeError(w, r, err)
		return
	}
	if req.Type == share.TypeFile {
		if _, err := s.objects.StatObject(ctx, req.Bucket, req.Key, ""); err != nil {
			writeError(w, r, err)
			return
		}
	}
	p := principal(r)
	sh, err := s.shares.Create(ctx, share.CreateInput{
		Type: req.Type, Bucket: req.Bucket, Key: req.Key, ExpiresAt: req.ExpiresAt, Password: req.Password,
		MaxDownloads: req.MaxDownloads, MaxUploadBytes: req.MaxUploadBytes, Note: req.Note, CreatedBy: p.User.ID,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.audit.Record(ctx, p.Name(), "share.create", sh.Bucket+"/"+sh.Key, clientIP(r),
		map[string]any{"id": sh.ID, "type": sh.Type, "password": sh.HasPassword})
	s.webhooks.Publish(webhook.Event{Type: "share.created", Bucket: sh.Bucket, Key: sh.Key, Data: s.toShareJSON(r, sh)})
	writeJSON(w, http.StatusCreated, s.toShareJSON(r, sh))
}

// loadOwnedShare fetches a share the caller may manage (creator or admin).
func (s *Server) loadOwnedShare(w http.ResponseWriter, r *http.Request) (*share.Share, bool) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return nil, false
	}
	sh, err := s.shares.Get(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return nil, false
	}
	p := principal(r)
	if !p.Can(auth.ActAdmin, "") && (sh.CreatedBy == nil || *sh.CreatedBy != p.User.ID || !p.Can(auth.ActWrite, sh.Bucket)) {
		writeProblem(w, http.StatusNotFound, share.ErrNotFound.Error())
		return nil, false
	}
	return sh, true
}

func (s *Server) handleUpdateShare(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.loadOwnedShare(w, r)
	if !ok {
		return
	}
	// Decode to raw fields so that an explicit null ("remove the limit") can be
	// told apart from an omitted field ("leave unchanged").
	var raw map[string]json.RawMessage
	if !decodeJSON(w, r, &raw) {
		return
	}
	var in share.UpdateInput
	bad := func(field string) {
		writeProblemFields(w, http.StatusUnprocessableEntity, "invalid "+field, map[string]string{field: "is invalid"})
	}
	for k, v := range raw {
		isNull := string(v) == "null"
		switch k {
		case "expiresAt":
			var t *time.Time
			if !isNull {
				var tv time.Time
				if json.Unmarshal(v, &tv) != nil {
					bad(k)
					return
				}
				t = &tv
			}
			in.ExpiresAt = &t
		case "password":
			var pw string
			if !isNull && json.Unmarshal(v, &pw) != nil {
				bad(k)
				return
			}
			in.Password = &pw
		case "maxDownloads", "maxUploadBytes":
			var n *int64
			if !isNull {
				var nv int64
				if json.Unmarshal(v, &nv) != nil {
					bad(k)
					return
				}
				n = &nv
			}
			if k == "maxDownloads" {
				in.MaxDownloads = &n
			} else {
				in.MaxUploadBytes = &n
			}
		case "note":
			var note string
			if json.Unmarshal(v, &note) != nil {
				bad(k)
				return
			}
			in.Note = &note
		case "disabled":
			var d bool
			if json.Unmarshal(v, &d) != nil {
				bad(k)
				return
			}
			in.Disabled = &d
		default:
			writeProblem(w, http.StatusBadRequest, "unknown field "+k)
			return
		}
	}
	updated, err := s.shares.Update(r.Context(), sh.ID, in)
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), principal(r).Name(), "share.update", sh.Bucket+"/"+sh.Key, clientIP(r), map[string]any{"id": sh.ID})
	writeJSON(w, http.StatusOK, s.toShareJSON(r, updated))
}

func (s *Server) handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.loadOwnedShare(w, r)
	if !ok {
		return
	}
	if err := s.shares.Delete(r.Context(), sh.ID); err != nil {
		writeError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), principal(r).Name(), "share.delete", sh.Bucket+"/"+sh.Key, clientIP(r), map[string]any{"id": sh.ID})
	w.WriteHeader(http.StatusNoContent)
}

// ---- public endpoints ----

func shareCookie(sh *share.Share) string { return "acs_share_" + strconv.FormatInt(sh.ID, 10) }

func (s *Server) unlocked(r *http.Request, sh *share.Share) bool {
	if !sh.HasPassword {
		return true
	}
	c, err := r.Cookie(shareCookie(sh))
	return err == nil && s.shares.VerifyUnlock(sh, c.Value)
}

// publicShare loads a usable share; with needUnlock it also requires the
// password to have been supplied.
func (s *Server) publicShare(w http.ResponseWriter, r *http.Request, needUnlock bool) (*share.Share, bool) {
	sh, err := s.shares.GetByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		if errors.Is(err, share.ErrNotFound) {
			writeProblem(w, http.StatusNotFound, "this link does not exist")
		} else {
			writeInternalError(w, r, err)
		}
		return nil, false
	}
	if err := sh.Status(time.Now()); err != nil {
		writeProblem(w, http.StatusGone, err.Error())
		return nil, false
	}
	if needUnlock && !s.unlocked(r, sh) {
		writeProblem(w, http.StatusUnauthorized, "password required")
		return nil, false
	}
	return sh, true
}

type publicFile struct {
	Name         string    `json:"name"`
	Size         int64     `json:"size"`
	ContentType  string    `json:"contentType"`
	LastModified time.Time `json:"lastModified"`
}

type publicShareResponse struct {
	Type             string      `json:"type"`
	Name             string      `json:"name"`
	SiteName         string      `json:"siteName"`
	Note             string      `json:"note"`
	ExpiresAt        *time.Time  `json:"expiresAt,omitempty"`
	RequiresPassword bool        `json:"requiresPassword"`
	Unlocked         bool        `json:"unlocked"`
	DownloadsLeft    *int64      `json:"downloadsLeft,omitempty"`
	MaxUploadBytes   *int64      `json:"maxUploadBytes,omitempty"`
	File             *publicFile `json:"file,omitempty"`
}

func shareName(sh *share.Share) string {
	if sh.Type == share.TypeFile {
		return path.Base(sh.Key)
	}
	if p := strings.TrimSuffix(sh.Key, "/"); p != "" {
		return path.Base(p)
	}
	return sh.Bucket
}

func (s *Server) handlePublicShare(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.publicShare(w, r, false)
	if !ok {
		return
	}
	s.shares.RecordView(r.Context(), sh.ID)
	resp := publicShareResponse{
		Type: sh.Type, Name: shareName(sh), SiteName: s.settings.Get().SiteName, Note: sh.Note,
		ExpiresAt: sh.ExpiresAt, RequiresPassword: sh.HasPassword, Unlocked: s.unlocked(r, sh),
		MaxUploadBytes: sh.MaxUploadBytes,
	}
	if sh.MaxDownloads != nil {
		left := *sh.MaxDownloads - sh.Downloads
		resp.DownloadsLeft = &left
	}
	if sh.Type == share.TypeFile && resp.Unlocked {
		o, err := s.objects.StatObject(r.Context(), sh.Bucket, sh.Key, "")
		if err != nil {
			writeProblem(w, http.StatusGone, "the shared file no longer exists")
			return
		}
		resp.File = &publicFile{Name: path.Base(o.Key), Size: o.Size, ContentType: o.ContentType, LastModified: o.ModTime}
	}
	writeJSON(w, http.StatusOK, resp)
}

type unlockRequest struct {
	Password string `json:"password"`
}

func (s *Server) handlePublicUnlock(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.publicShare(w, r, false)
	if !ok {
		return
	}
	if !s.shareLimiter.Allow(clientIP(r)) {
		writeProblem(w, http.StatusTooManyRequests, "too many attempts, try again later")
		return
	}
	var req unlockRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.shares.CheckPassword(sh, req.Password); err != nil {
		if errors.Is(err, share.ErrWrongPassword) {
			writeProblemFields(w, http.StatusUnauthorized, err.Error(), map[string]string{"password": "is incorrect"})
			return
		}
		writeInternalError(w, r, err)
		return
	}
	const ttl = 12 * time.Hour
	http.SetCookie(w, &http.Cookie{
		Name:     shareCookie(sh),
		Value:    s.shares.UnlockToken(sh, ttl),
		Path:     "/api/v1/public/shares/" + sh.Token,
		MaxAge:   int(ttl / time.Second),
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePublicList(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.publicShare(w, r, true)
	if !ok {
		return
	}
	if sh.Type != share.TypeFolder {
		writeProblem(w, http.StatusBadRequest, errShareType.Error())
		return
	}
	sub := r.URL.Query().Get("path")
	if sub != "" {
		var err error
		if sub, err = safeRelPath(sub); err != nil {
			writeProblem(w, http.StatusBadRequest, err.Error())
			return
		}
		if !strings.HasSuffix(sub, "/") {
			sub += "/"
		}
	}
	res, err := s.objects.List(r.Context(), sh.Bucket, object.ListOptions{
		Prefix: sh.Key + sub, Delimiter: "/", Marker: decodeCursor(r.URL.Query().Get("cursor")),
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	type entry struct {
		Name         string    `json:"name"`
		Path         string    `json:"path"`
		Size         int64     `json:"size"`
		ContentType  string    `json:"contentType"`
		LastModified time.Time `json:"lastModified"`
	}
	files := []entry{}
	for _, o := range res.Objects {
		rel := strings.TrimPrefix(o.Key, sh.Key)
		if strings.HasSuffix(rel, "/") {
			continue // folder marker
		}
		files = append(files, entry{Name: path.Base(rel), Path: rel, Size: o.Size, ContentType: o.ContentType, LastModified: o.ModTime})
	}
	folders := []string{}
	for _, p := range res.CommonPrefixes {
		folders = append(folders, strings.TrimPrefix(p, sh.Key))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path": sub, "files": files, "folders": folders, "nextCursor": encodeCursor(res.NextMarker),
	})
}

// countsAsDownload is true for explicit downloads that start at the beginning
// of the file, so resumed or chunked range requests are not counted twice.
func countsAsDownload(r *http.Request) bool {
	if r.URL.Query().Get("download") != "1" || r.Method != http.MethodGet {
		return false
	}
	rg := r.Header.Get("Range")
	return rg == "" || strings.HasPrefix(rg, "bytes=0-")
}

func (s *Server) handlePublicDownload(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.publicShare(w, r, true)
	if !ok {
		return
	}
	key := sh.Key
	switch sh.Type {
	case share.TypeFile:
	case share.TypeFolder:
		rel, err := safeRelPath(r.URL.Query().Get("path"))
		if err != nil {
			writeProblem(w, http.StatusBadRequest, err.Error())
			return
		}
		key = sh.Key + rel
	default:
		writeProblem(w, http.StatusBadRequest, errShareType.Error())
		return
	}
	o, err := s.objects.StatObject(r.Context(), sh.Bucket, key, "")
	if err != nil {
		writeProblem(w, http.StatusNotFound, "file not found")
		return
	}
	if countsAsDownload(r) {
		if err := s.shares.RecordDownload(r.Context(), sh.ID); err != nil {
			writeProblem(w, http.StatusGone, err.Error())
			return
		}
		s.webhooks.Publish(webhook.Event{Type: "share.downloaded", Bucket: sh.Bucket, Key: key, Data: map[string]any{
			"shareId": sh.ID, "bucket": sh.Bucket, "key": key, "ip": clientIP(r),
		}})
	}
	s.serveObject(w, r, o, r.URL.Query().Get("download") == "1")
}

func (s *Server) handlePublicZip(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.publicShare(w, r, true)
	if !ok {
		return
	}
	if sh.Type != share.TypeFolder {
		writeProblem(w, http.StatusBadRequest, errShareType.Error())
		return
	}
	var objs []*object.Object
	if err := s.eachUnder(r, sh.Bucket, sh.Key, func(o *object.Object) error {
		objs = append(objs, o)
		return nil
	}); err != nil {
		writeError(w, r, err)
		return
	}
	if err := s.shares.RecordDownload(r.Context(), sh.ID); err != nil {
		writeProblem(w, http.StatusGone, err.Error())
		return
	}
	s.writeZip(w, r, shareName(sh)+".zip", objs, func(o *object.Object) string { return strings.TrimPrefix(o.Key, sh.Key) })
}

// uniqueKey returns key, or "name (n).ext" if key already exists.
func (s *Server) uniqueKey(r *http.Request, bucket, key string) (string, error) {
	ext := path.Ext(key)
	base := strings.TrimSuffix(key, ext)
	candidate := key
	for i := 1; i < 1000; i++ {
		_, err := s.objects.StatObject(r.Context(), bucket, candidate, "")
		if errors.Is(err, object.ErrNoSuchKey) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
		candidate = fmt.Sprintf("%s (%d)%s", base, i, ext)
	}
	return "", errors.New("too many files with the same name")
}

// limitedReader fails once more than n bytes are read.
type limitedReader struct {
	r io.Reader
	n int64
}

var errTooLarge = errors.New("file exceeds the upload size limit")

func (l *limitedReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.n -= int64(n)
	if l.n < 0 {
		return n, errTooLarge
	}
	return n, err
}

func (s *Server) handlePublicUpload(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.publicShare(w, r, true)
	if !ok {
		return
	}
	if sh.Type != share.TypeUpload {
		writeProblem(w, http.StatusBadRequest, errShareType.Error())
		return
	}
	rel, err := safeRelPath(r.PathValue("name"))
	if err != nil || strings.HasSuffix(rel, "/") {
		writeProblem(w, http.StatusBadRequest, "invalid file name")
		return
	}
	var body io.Reader = r.Body
	if sh.MaxUploadBytes != nil {
		if r.ContentLength > *sh.MaxUploadBytes {
			writeProblem(w, http.StatusRequestEntityTooLarge, errTooLarge.Error())
			return
		}
		body = &limitedReader{r: r.Body, n: *sh.MaxUploadBytes}
	}
	key, err := s.uniqueKey(r, sh.Bucket, sh.Key+rel)
	if err != nil {
		writeError(w, r, err)
		return
	}
	o, err := s.objects.PutObject(r.Context(), sh.Bucket, key, body, object.PutOptions{
		Size: r.ContentLength, ContentType: r.Header.Get("Content-Type"),
	})
	if errors.Is(err, errTooLarge) {
		writeProblem(w, http.StatusRequestEntityTooLarge, err.Error())
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	s.audit.Record(r.Context(), "anonymous", "share.upload", sh.Bucket+"/"+key, clientIP(r),
		map[string]any{"shareId": sh.ID, "size": o.Size})
	s.webhooks.Publish(webhook.Event{Type: "share.uploaded", Bucket: sh.Bucket, Key: key, Data: map[string]any{
		"shareId": sh.ID, "bucket": sh.Bucket, "key": key, "size": o.Size, "ip": clientIP(r),
	}})
	writeJSON(w, http.StatusCreated, map[string]any{"name": path.Base(key), "size": o.Size})
}
