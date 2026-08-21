package codexappserver

import "encoding/json"

const (
	methodInitialize  = "initialize"
	methodInitialized = "initialized"
)

type wireRequest struct {
	Method string `json:"method"`
	ID     int64  `json:"id"`
	Params any    `json:"params,omitempty"`
}

type wireNotification struct {
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type wireEnvelope struct {
	Method string          `json:"method"`
	ID     json.RawMessage `json:"id"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *wireError      `json:"error"`
}

type wireError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type initializeParams struct {
	ClientInfo clientInfo `json:"clientInfo"`
}

type clientInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
}

type initializeResult struct {
	UserAgent      string `json:"userAgent"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
}

type response struct {
	result json.RawMessage
	err    error
}

// notification is intentionally package-private until a capability-specific
// consumer exists. That keeps Phase 0 from inventing an unused consumer port.
type notification struct {
	Method string
	Params json.RawMessage
}
