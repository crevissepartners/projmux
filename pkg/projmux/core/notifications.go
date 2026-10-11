package core

import "time"

type Notification struct {
	ID        string            `json:"id"`
	Text      string            `json:"text"`
	Severity  string            `json:"severity"`
	Socket    string            `json:"socket,omitempty"`
	Session   string            `json:"session"`
	Window    string            `json:"window,omitempty"`
	Pane      string            `json:"pane,omitempty"`
	Source    string            `json:"source"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	ExpiresAt time.Time         `json:"expires_at"`
}

// NotificationFilter matches get notifications' repeatable filters and limit.
// Empty filters select all entries; zero Limit means no limit.
type NotificationFilter struct {
	Severities, Sources []string
	Limit               int
}

// NotificationAck requests one id or All, never both. A missing id is refused.
type NotificationAck struct {
	ID  string
	All bool
}
