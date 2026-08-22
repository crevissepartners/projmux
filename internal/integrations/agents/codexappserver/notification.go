package codexappserver

import "encoding/json"

// Notification is one server-initiated app-server message. Method is the
// bounded protocol discriminator. Params must be decoded by the capability
// consumer and must never be copied to diagnostics or durable state verbatim.
type Notification struct {
	Method    string
	Params    json.RawMessage
	RequestID string
}
