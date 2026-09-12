package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// FailureDiagnostic is a content-free projection, never an error message. Method
// and cause are closed tokens of at most 32 bytes; RPCCode is an optional signed
// integer (at most 20 decimal bytes). Its entire JSON representation is <= 160
// bytes. Missing codes are null, including transport and local protocol errors.
const MaxFailureDiagnosticBytes = 160

type FailureDiagnostic struct {
	Method  string `json:"method"`
	RPCCode *int   `json:"rpc_code"`
	Cause   string `json:"cause"`
}

func safeDiagnosticMethod(method string) string {
	switch method {
	case methodInitialize, methodInitialized, methodModelList, methodReviewStart,
		methodThreadList, methodThreadLoadedList, methodThreadRead, methodThreadStart,
		methodThreadResume, methodTurnStart, methodTurnSteer, methodTurnInterrupt,
		methodRemoteControlStatusRead:
		return method
	default:
		return "unknown"
	}
}

func (d FailureDiagnostic) safe() FailureDiagnostic {
	d.Method = safeDiagnosticMethod(d.Method)
	switch d.Cause {
	case "unsupported", "catalog-rejected", "thread-not-durable", "thread-absent",
		"rpc-refused", "protocol-error", "payload-too-large", "timeout", "cancelled",
		"disconnected", "endpoint-changed":
	default:
		d.Cause = "unknown"
	}
	return d
}

// MarshalJSON also validates values received across the broker IPC boundary.
func (d FailureDiagnostic) MarshalJSON() ([]byte, error) {
	type plain FailureDiagnostic
	return json.Marshal(plain(d.safe()))
}

func (d FailureDiagnostic) String() string {
	raw, _ := d.MarshalJSON()
	return string(raw)
}

type requestFailure struct {
	method string
	err    error
}

func (e *requestFailure) Error() string {
	return "codex app-server request failed: " + Diagnostic(e).String()
}
func (e *requestFailure) Unwrap() error { return e.err }

func withRequestFailure(method string, err error) error {
	if err == nil {
		return nil
	}
	return &requestFailure{method: safeDiagnosticMethod(method), err: err}
}

// Diagnostic retains the innermost request method and original response code
// through ordinary %w wrappers. No arbitrary Error() string is ever consulted.
func Diagnostic(err error) FailureDiagnostic {
	d := FailureDiagnostic{Method: "unknown", Cause: "unknown"}
	var request *requestFailure
	if errors.As(err, &request) {
		d.Method = request.method
	}
	var remote *diagnosticFailure
	if errors.As(err, &remote) {
		d = remote.diagnostic.safe()
	}
	var response *responseError
	if errors.As(err, &response) {
		code := response.code
		d.RPCCode = &code
	}
	switch {
	case errors.Is(err, ErrStateDbOnlyRejected):
		d.Cause = "catalog-rejected"
	case remote != nil: // keep the safely projected cause from the producing process
	case errors.Is(err, ErrThreadNotDurable):
		d.Cause = "thread-not-durable"
	case errors.Is(err, ErrThreadAbsent):
		d.Cause = "thread-absent"
	case errors.Is(err, ErrUnsupported):
		d.Cause = "unsupported"
	case response != nil:
		d.Cause = "rpc-refused"
	case errors.Is(err, ErrPayloadTooLarge):
		d.Cause = "payload-too-large"
	case errors.Is(err, ErrEndpointChanged):
		d.Cause = "endpoint-changed"
	case errors.Is(err, context.DeadlineExceeded):
		d.Cause = "timeout"
	case errors.Is(err, context.Canceled):
		d.Cause = "cancelled"
	case errors.Is(err, ErrProtocol):
		d.Cause = "protocol-error"
	case errors.Is(err, ErrDisconnected):
		d.Cause = "disconnected"
	}
	return d.safe()
}

type diagnosticFailure struct {
	diagnostic FailureDiagnostic
	err        error
}

func (e *diagnosticFailure) Error() string {
	return fmt.Sprintf("codex app-server failure: %s", e.diagnostic)
}
func (e *diagnosticFailure) Unwrap() error { return e.err }

// WithDiagnostic restores only the bounded projection across a process or
// classification boundary. Existing errors.Is categories stay authoritative.
func WithDiagnostic(err error, diagnostic FailureDiagnostic) error {
	if err == nil {
		return nil
	}
	return &diagnosticFailure{err: err, diagnostic: diagnostic.safe()}
}
