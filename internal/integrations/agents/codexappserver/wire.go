package codexappserver

import "encoding/json"

const (
	methodInitialize  = "initialize"
	methodInitialized = "initialized"
	methodModelList   = "model/list"
	methodReviewStart = "review/start"
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

type modelListParams struct {
	Cursor        *string `json:"cursor,omitempty"`
	IncludeHidden bool    `json:"includeHidden"`
}

type modelListResult struct {
	Data       []wireModel `json:"data"`
	NextCursor *string     `json:"nextCursor"`
}

type wireModel struct {
	ID                        string                      `json:"id"`
	Model                     string                      `json:"model"`
	DisplayName               string                      `json:"displayName"`
	Description               string                      `json:"description"`
	Hidden                    bool                        `json:"hidden"`
	Default                   bool                        `json:"isDefault"`
	DefaultReasoningEffort    string                      `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []wireReasoningEffortOption `json:"supportedReasoningEfforts"`
	InputModalities           []string                    `json:"inputModalities"`
	SupportsPersonality       bool                        `json:"supportsPersonality"`
}

type wireReasoningEffortOption struct {
	Effort string `json:"reasoningEffort"`
}

type reviewStartParams struct {
	ThreadID string `json:"threadId"`
	Target   any    `json:"target"`
}

type reviewStartResult struct {
	ReviewThreadID string   `json:"reviewThreadId"`
	Turn           wireTurn `json:"turn"`
}

type wireTurn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
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
