package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"acs/internal/auth"
	"acs/internal/object"
	"acs/internal/settings"
	"acs/internal/share"
	"acs/internal/webhook"
)

// Problem is an RFC 9457 problem details body.
type Problem struct {
	Type   string            `json:"type"`
	Title  string            `json:"title"`
	Status int               `json:"status"`
	Detail string            `json:"detail,omitempty"`
	Errors map[string]string `json:"errors,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		json.NewEncoder(w).Encode(v)
	}
}

func writeProblem(w http.ResponseWriter, status int, detail string) {
	writeProblemFields(w, status, detail, nil)
}

func writeProblemFields(w http.ResponseWriter, status int, detail string, fields map[string]string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(Problem{
		Type:   "about:blank",
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
		Errors: fields,
	})
}

func writeInternalError(w http.ResponseWriter, r *http.Request, err error) {
	slog.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeProblem(w, http.StatusInternalServerError, "an internal error occurred")
}

// writeError maps domain errors to HTTP problems; unknown errors become 500s.
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var av *auth.ValidationError
	var sv *share.ValidationError
	var wv *webhook.ValidationError
	switch {
	case errors.As(err, &av):
		writeProblemFields(w, http.StatusUnprocessableEntity, av.Error(), map[string]string{av.Field: av.Message})
	case errors.As(err, &sv):
		writeProblemFields(w, http.StatusUnprocessableEntity, sv.Error(), map[string]string{sv.Field: sv.Message})
	case errors.As(err, &wv):
		writeProblemFields(w, http.StatusUnprocessableEntity, wv.Error(), map[string]string{wv.Field: wv.Message})
	case errors.Is(err, object.ErrInvalidBucketName):
		writeProblemFields(w, http.StatusUnprocessableEntity, err.Error(),
			map[string]string{"name": "use 3–63 lowercase letters, digits, dots or hyphens, starting and ending with a letter or digit"})
	case errors.Is(err, object.ErrInvalidKey), errors.Is(err, object.ErrInvalidArgument),
		errors.Is(err, object.ErrInvalidPart), errors.Is(err, object.ErrInvalidPartOrder),
		errors.Is(err, object.ErrEntityTooSmall), errors.Is(err, object.ErrBadDigest),
		errors.Is(err, object.ErrIncompleteBody), errors.Is(err, settings.ErrInvalid):
		writeProblem(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, object.ErrNoSuchBucket), errors.Is(err, object.ErrNoSuchKey),
		errors.Is(err, object.ErrNoSuchVersion), errors.Is(err, object.ErrNoSuchUpload),
		errors.Is(err, auth.ErrNoSuchKey), errors.Is(err, auth.ErrNoSuchUser),
		errors.Is(err, share.ErrNotFound), errors.Is(err, webhook.ErrNotFound):
		writeProblem(w, http.StatusNotFound, err.Error())
	case errors.Is(err, object.ErrDeleteMarker):
		writeProblem(w, http.StatusMethodNotAllowed, err.Error())
	case errors.Is(err, object.ErrBucketExists), errors.Is(err, object.ErrBucketNotEmpty),
		errors.Is(err, auth.ErrUserExists), errors.Is(err, auth.ErrLastAdmin):
		writeProblem(w, http.StatusConflict, err.Error())
	case errors.Is(err, object.ErrQuotaExceeded):
		writeProblem(w, http.StatusInsufficientStorage, err.Error())
	case errors.Is(err, auth.ErrWrongPasswd):
		writeProblemFields(w, http.StatusUnprocessableEntity, err.Error(), map[string]string{"currentPassword": "is incorrect"})
	default:
		writeInternalError(w, r, err)
	}
}

const maxJSONBody = 1 << 20

// decodeJSON reads a JSON request body into v, writing a problem response and
// returning false on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var maxErr *http.MaxBytesError
		switch {
		case errors.As(err, &maxErr):
			writeProblem(w, http.StatusRequestEntityTooLarge, "request body too large")
		case errors.Is(err, io.EOF):
			writeProblem(w, http.StatusBadRequest, "request body is empty")
		default:
			writeProblem(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		}
		return false
	}
	return true
}

// pathID parses a numeric path parameter, writing a 404 on failure.
func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		writeProblem(w, http.StatusNotFound, "not found")
		return 0, false
	}
	return id, true
}
