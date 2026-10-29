package core

import "time"

// MessageState is an observed coordination receipt state, not model completion.
type MessageState string

const (
	MessageAccepted  MessageState = "accepted"
	MessageHeld      MessageState = "held"
	MessageDelivered MessageState = "delivered"
	MessageRefused   MessageState = "refused"
	MessageExpired   MessageState = "expired"
	MessageStale     MessageState = "stale"
	MessageFailed    MessageState = "failed"
)

// MessageRoute exposes opaque routing fences without provider locators or credentials.
type MessageRoute struct {
	AgentUID             string `json:"agentUID"`
	PaneUID              string `json:"paneUID"`
	ActivationGeneration string `json:"activationGeneration"`
	Provider             string `json:"provider"`
	Incarnation          string `json:"incarnation"`
}
type MessageOrigin struct {
	Kind   string `json:"kind"`
	Client string `json:"client"`
}

// MessageDelivery preserves uncertainty separately from State. Never replay an
// uncertain send. Delivered acknowledges peer context acceptance only.
type MessageDelivery struct {
	MessageRef      string       `json:"messageRef"`
	ConversationRef string       `json:"conversationRef"`
	State           MessageState `json:"state"`
	Reason          string       `json:"reason,omitempty"`
	OutcomeUnknown  bool         `json:"outcomeUnknown,omitempty"`
	AcceptedAt      time.Time    `json:"acceptedAt"`
	TerminalAt      time.Time    `json:"terminalAt"`
}

// MessageReceipt is the redacted CLI receipt shape. Inspect it even on error.
type MessageReceipt struct {
	Version         int             `json:"version"`
	MessageRef      string          `json:"messageRef"`
	ConversationRef string          `json:"conversationRef"`
	ReplyTo         string          `json:"replyTo,omitempty"`
	Origin          MessageOrigin   `json:"origin,omitzero"`
	Source          MessageRoute    `json:"source,omitzero"`
	Target          MessageRoute    `json:"target"`
	Delivery        MessageDelivery `json:"delivery"`
	Deadline        time.Time       `json:"deadline"`
}

// MessageOperator names the in-process operator client an operator message is
// sent through. Its client name is unexported, so decoding request data (JSON,
// a form, a peer envelope or a Source string) never produces one; only
// in-process code that calls NewMessageOperator does. SendOperatorMessage
// checks the name against the operator client rule (1-32 bytes of lowercase
// ASCII letters, digits and '-', starting with a letter) before anything is
// read or written. The zero value names no operator. A MessageOperator is not
// authentication: the caller has already confirmed who the person is.
type MessageOperator struct{ client string }

// NewMessageOperator names client as the operator a message is sent through.
func NewMessageOperator(client string) MessageOperator { return MessageOperator{client: client} }

// Client is the operator client name, or empty for the zero value.
func (o MessageOperator) Client() string { return o.client }
