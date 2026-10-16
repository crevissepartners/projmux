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
