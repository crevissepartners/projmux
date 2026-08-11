// Package paneidentity resolves the user-visible identity of a pane without
// merging the independently owned label, AI topic, and raw title fields.
package paneidentity

import "strings"

type Source string

const (
	SourceNone  Source = ""
	SourceLabel Source = "label"
	SourceTopic Source = "topic"
	SourceShell Source = "shell"
	SourceTitle Source = "title"
)

type Inputs struct {
	Label   string
	AIAgent string
	AITopic string
	Command string
	Title   string
}

type Identity struct {
	Value  string
	Source Source
}

// Resolve applies the canonical visible pane identity order. It never derives
// one identity field from another: absent labels stay absent, and topics are
// considered only for agent panes.
func Resolve(in Inputs) Identity {
	if value := strings.TrimSpace(in.Label); value != "" {
		return Identity{Value: value, Source: SourceLabel}
	}
	if strings.TrimSpace(in.AIAgent) != "" {
		if value := strings.TrimSpace(in.AITopic); value != "" {
			return Identity{Value: value, Source: SourceTopic}
		}
	}
	if value := strings.TrimSpace(in.Command); KnownInteractiveShell(value) {
		return Identity{Value: value, Source: SourceShell}
	}
	if value := strings.TrimSpace(in.Title); value != "" {
		return Identity{Value: value, Source: SourceTitle}
	}
	return Identity{}
}

func KnownInteractiveShell(command string) bool {
	switch strings.TrimSpace(command) {
	case "zsh", "bash", "fish", "sh", "nu", "xonsh":
		return true
	default:
		return false
	}
}
