package web

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// Request bodies. None of them carries a resource ref: every uid a mutation
// acts on comes from the path.

// CreateWindowRequest is the body of POST /projects/{project}/windows.
type CreateWindowRequest struct {
	Name string `json:"name,omitempty"`
	// Agent, when set, also starts that provider in the new Window.
	Agent *NewAgent `json:"agent,omitempty"`
	// Focus moves the operator's attached client to the new Window.
	Focus   bool `json:"focus,omitempty"`
	Confirm bool `json:"confirm"`
}

// NewAgent is the agent half of a create-window request.
type NewAgent struct {
	Provider string `json:"provider"`
	Payload  string `json:"payload,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
}

// CreateAgentRequest is the body of POST /projects/{p}/windows/{w}/agents.
type CreateAgentRequest struct {
	Provider string `json:"provider"`
	// AnchorPane is the Pane in this Window the new one is split from.
	AnchorPane string `json:"anchorPane,omitempty"`
	// CwdFrom is "pane" (the anchor's directory) or "project".
	CwdFrom string `json:"cwdFrom,omitempty"`
	Payload string `json:"payload,omitempty"`
	// Model and Effort choose what a new Claude Agent runs with; create
	// refuses them for another provider.
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Confirm bool   `json:"confirm"`
}

// CreatePaneRequest is the body of POST /projects/{p}/windows/{w}/panes: a
// plain shell split beside AnchorPane.
type CreatePaneRequest struct {
	AnchorPane string `json:"anchorPane"`
	CwdFrom    string `json:"cwdFrom,omitempty"`
	Confirm    bool   `json:"confirm"`
}

// SettingRequest is the body of PATCH /web/settings: one setting and its new
// value.
type SettingRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// MessageRequest is the body of POST /agents/{agent}/messages.
type MessageRequest struct {
	// Source is the Agent the message is anchored on. A browser has no Pane,
	// so the caller has to name one.
	Source     string `json:"source"`
	Body       string `json:"body"`
	MessageRef string `json:"messageRef,omitempty"`
	ReplyTo    string `json:"replyTo,omitempty"`
	TTL        string `json:"ttl,omitempty"`
}

type nameRequest struct {
	Name string `json:"name"`
}

type textRequest struct {
	Text string `json:"text"`
}

type confirmRequest struct {
	Confirm bool `json:"confirm"`
}

const maxBody = 64 << 10

// decodeBody reads exactly one JSON object. Unknown fields are refused, so a
// body cannot smuggle a ref the route does not read. An empty body decodes as
// the zero value.
func decodeBody(w http.ResponseWriter, r *http.Request, into any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return InvalidRequest("request body: " + err.Error())
	}
	if dec.More() {
		return InvalidRequest("request body holds more than one JSON value")
	}
	return nil
}

func confirmRequired(what string) *Error {
	return NewError(http.StatusBadRequest, CodeConfirmRequired, what+" needs \"confirm\": true")
}

func decodeName(w http.ResponseWriter, r *http.Request) (string, error) {
	var req nameRequest
	if err := decodeBody(w, r, &req); err != nil {
		return "", err
	}
	if strings.TrimSpace(req.Name) == "" {
		return "", InvalidRequest("name is empty")
	}
	return strings.TrimSpace(req.Name), nil
}

func decodeText(w http.ResponseWriter, r *http.Request) (string, error) {
	var req textRequest
	if err := decodeBody(w, r, &req); err != nil {
		return "", err
	}
	if strings.TrimSpace(req.Text) == "" {
		return "", InvalidRequest("text is empty")
	}
	return req.Text, nil
}
