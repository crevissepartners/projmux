// Package focus parses click-to-focus targets and resolves them against tmux.
//
// Targets describe a coordinate inside a tmux server in the form
//
//	SESSION[:WINDOW[.PANE]]
//
// where WINDOW is either a numeric index or a tmux window id (`@N`) and PANE
// is either a numeric index or a tmux pane id (`%N`). When both an index and
// an id are usable for the same coordinate the id wins; the index is kept
// only as a fallback for tmux releases that drop ids on layout changes.
package focus

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrEmptyTarget is returned when the input target string is blank.
var ErrEmptyTarget = errors.New("focus target is required")

// Target is a parsed focus coordinate.
type Target struct {
	// Raw preserves the original spec for logging.
	Raw string

	// Session is the tmux session name; always populated.
	Session string

	// WindowIndex is the numeric window index (zero if unset).
	WindowIndex int
	// HasWindowIndex reports whether WindowIndex was supplied.
	HasWindowIndex bool
	// WindowID is the tmux window id (`@N`) when supplied.
	WindowID string

	// PaneIndex is the numeric pane index (zero if unset).
	PaneIndex int
	// HasPaneIndex reports whether PaneIndex was supplied.
	HasPaneIndex bool
	// PaneID is the tmux pane id (`%N`) when supplied.
	PaneID string
}

// HasWindow reports whether a window-level coordinate was supplied.
func (t Target) HasWindow() bool {
	return t.HasWindowIndex || t.WindowID != ""
}

// HasPane reports whether a pane-level coordinate was supplied.
func (t Target) HasPane() bool {
	return t.HasPaneIndex || t.PaneID != ""
}

// WindowSelector returns the tmux selector portion for the window, preferring
// the id form when present.
func (t Target) WindowSelector() string {
	if t.WindowID != "" {
		return t.WindowID
	}
	if t.HasWindowIndex {
		return strconv.Itoa(t.WindowIndex)
	}
	return ""
}

// PaneSelector returns the tmux selector portion for the pane, preferring the
// id form when present.
func (t Target) PaneSelector() string {
	if t.PaneID != "" {
		return t.PaneID
	}
	if t.HasPaneIndex {
		return strconv.Itoa(t.PaneIndex)
	}
	return ""
}

// Parse interprets a target spec of the form SESSION[:WINDOW[.PANE]].
//
// Window may be a positive integer index or a tmux window id like @7.
// Pane may be a non-negative integer index or a tmux pane id like %12.
// Pane id (%N) takes precedence over pane index when both forms are usable
// for the same coordinate; the same precedence applies at the window level.
func Parse(spec string) (Target, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return Target{}, ErrEmptyTarget
	}

	target := Target{Raw: trimmed}

	sessionPart, rest, hasRest := strings.Cut(trimmed, ":")
	sessionPart = strings.TrimSpace(sessionPart)
	if sessionPart == "" {
		return Target{}, fmt.Errorf("focus target %q: session name is required", trimmed)
	}
	target.Session = sessionPart
	if !hasRest {
		return target, nil
	}

	windowPart, panePart, hasPane := strings.Cut(rest, ".")
	windowPart = strings.TrimSpace(windowPart)
	if windowPart == "" {
		return Target{}, fmt.Errorf("focus target %q: window selector is required after ':'", trimmed)
	}
	if err := assignWindow(&target, windowPart); err != nil {
		return Target{}, fmt.Errorf("focus target %q: %w", trimmed, err)
	}
	if !hasPane {
		return target, nil
	}

	panePart = strings.TrimSpace(panePart)
	if panePart == "" {
		return Target{}, fmt.Errorf("focus target %q: pane selector is required after '.'", trimmed)
	}
	if err := assignPane(&target, panePart); err != nil {
		return Target{}, fmt.Errorf("focus target %q: %w", trimmed, err)
	}

	return target, nil
}

func assignWindow(target *Target, raw string) error {
	if after, ok := strings.CutPrefix(raw, "@"); ok {
		body := after
		if body == "" {
			return errors.New("window id '@' must be followed by digits")
		}
		if _, err := strconv.Atoi(body); err != nil {
			return fmt.Errorf("window id %q is not numeric", raw)
		}
		target.WindowID = raw
		return nil
	}

	idx, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("window selector %q must be an index or @id", raw)
	}
	if idx < 0 {
		return fmt.Errorf("window index %d must be non-negative", idx)
	}
	target.WindowIndex = idx
	target.HasWindowIndex = true
	return nil
}

func assignPane(target *Target, raw string) error {
	if after, ok := strings.CutPrefix(raw, "%"); ok {
		body := after
		if body == "" {
			return errors.New("pane id '%' must be followed by digits")
		}
		if _, err := strconv.Atoi(body); err != nil {
			return fmt.Errorf("pane id %q is not numeric", raw)
		}
		target.PaneID = raw
		return nil
	}

	idx, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("pane selector %q must be an index or %%id", raw)
	}
	if idx < 0 {
		return fmt.Errorf("pane index %d must be non-negative", idx)
	}
	target.PaneIndex = idx
	target.HasPaneIndex = true
	return nil
}
