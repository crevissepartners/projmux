package codexappserver

import "encoding/json"

const (
	methodInitialize   = "initialize"
	methodInitialized  = "initialized"
	methodModelList    = "model/list"
	methodReviewStart  = "review/start"
	methodThreadStart  = "thread/start"
	methodThreadResume = "thread/resume"
	methodTurnStart    = "turn/start"
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

type threadStartParams struct {
	CWD                   string   `json:"cwd,omitempty"`
	RuntimeWorkspaceRoots []string `json:"runtimeWorkspaceRoots,omitempty"`
}

type threadResumeParams struct {
	ThreadID              string   `json:"threadId"`
	CWD                   string   `json:"cwd,omitempty"`
	RuntimeWorkspaceRoots []string `json:"runtimeWorkspaceRoots,omitempty"`
	ExcludeTurns          bool     `json:"excludeTurns"`
}

type threadResult struct {
	Thread wireThread `json:"thread"`
}

type wireThread struct {
	ID string `json:"id"`
}

type turnStartParams struct {
	ThreadID            string          `json:"threadId"`
	Input               []wireUserInput `json:"input"`
	ClientUserMessageID string          `json:"clientUserMessageId,omitempty"`
}

type wireUserInput struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type turnStartResult struct {
	Turn wireTurn `json:"turn"`
}

type response struct {
	result json.RawMessage
	err    error
}
