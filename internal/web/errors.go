package web

import (
	"encoding/json"
	"errors"
	"net/http"
)

// Error codes the server itself assigns. Codes that projmux already carries,
// such as a Codex control refusal, are passed through verbatim instead.
const (
	CodeInvalidRequest  = "invalid-request"
	CodeConfirmRequired = "confirm-required"
	CodeForbiddenOrigin = "forbidden-origin"
	// CodeUnauthorized refuses a TCP request without the start token.
	CodeUnauthorized = "unauthorized"
	CodeNotFound     = "not-found"
	CodeNameConflict = "name-conflict"
	CodeInvalidName  = "invalid-name"
	CodeNotLive      = "not-live"
	CodeUnsupported  = "unsupported"
	CodeRefused      = "refused"
	CodeInternal     = "internal"
	// CodeInProgress refuses a create that repeats one still running.
	CodeInProgress = "create-in-progress"
)

// Error is the one shape every non-2xx response takes. Code is the stable
// token a client branches on; Message is for people and may change.
type Error struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Status  int            `json:"status"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// NewError builds an Error with the given status.
func NewError(status int, code, message string) *Error {
	return &Error{Code: code, Message: message, Status: status}
}

// NotFound is the error for a uid that does not exist, or that exists under
// another parent than the path names.
func NotFound(message string) *Error {
	return NewError(http.StatusNotFound, CodeNotFound, message)
}

// InvalidRequest is the error for a request the server will not interpret.
func InvalidRequest(message string) *Error {
	return NewError(http.StatusBadRequest, CodeInvalidRequest, message)
}

// AsError is asError for backends that embed a refusal in a result.
func AsError(err error) *Error { return asError(err) }

// asError turns anything a backend returned into the envelope. A backend that
// knows the refusal returns *Error; anything else is internal, because an
// unclassified failure must not look like a deliberate refusal.
func asError(err error) *Error {
	var typed *Error
	if errors.As(err, &typed) {
		if typed.Status == 0 {
			typed.Status = http.StatusInternalServerError
		}
		return typed
	}
	return NewError(http.StatusInternalServerError, CodeInternal, err.Error())
}

func writeError(w http.ResponseWriter, err error) {
	e := asError(err)
	writeJSON(w, e.Status, map[string]*Error{"error": e})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
