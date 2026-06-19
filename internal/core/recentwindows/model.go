package recentwindows

import (
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/aibadge"
)

const (
	Version      = 1
	DefaultLimit = 20
)

var ErrInvalidSnapshot = errors.New("invalid recent window snapshot")

type WindowKey struct {
	Socket   string `json:"socket"`
	Session  string `json:"session"`
	WindowID string `json:"window_id"`
}

type Snapshot struct {
	Socket        string   `json:"socket"`
	Session       string   `json:"session"`
	WindowID      string   `json:"window_id"`
	WindowName    string   `json:"window_name,omitempty"`
	Project       string   `json:"project,omitempty"`
	LastPaneID    string   `json:"last_pane_id,omitempty"`
	LastPaneTitle string   `json:"last_pane_title,omitempty"`
	PaneTitles    []string `json:"pane_titles,omitempty"`
	// PaneBadgeKinds carries the per-pane AI badge kind parallel to PaneTitles
	// (same length/order when available). Additive and backward compatible: old
	// state files omit it, so the picker falls back to titles-only rendering.
	PaneBadgeKinds []string  `json:"pane_badge_kinds,omitempty"`
	LastPaneTopic  string    `json:"last_pane_topic,omitempty"`
	LastCommand    string    `json:"last_command,omitempty"`
	LastFocusedAt  time.Time `json:"last_focused_at"`
}

type State struct {
	Version int        `json:"version"`
	Entries []Snapshot `json:"entries"`
}

type LiveWindow struct {
	Socket   string
	Session  string
	WindowID string
}

type Label struct {
	Primary   string
	Secondary string
	Debug     string
}

type Candidate struct {
	Snapshot
	Label     Label
	IsCurrent bool
}

func NewState(entries []Snapshot) State {
	return State{Version: Version, Entries: normalizeEntries(entries, DefaultLimit)}
}

func (s State) Record(snapshot Snapshot, limit int) (State, error) {
	snapshot = normalizeSnapshot(snapshot)
	if err := snapshot.Valid(); err != nil {
		return State{}, err
	}
	entries := make([]Snapshot, 0, len(s.Entries)+1)
	entries = append(entries, snapshot)
	snapshotKey := snapshot.Key()
	for _, existing := range s.Entries {
		existing = normalizeSnapshot(existing)
		if existing.Key() == snapshotKey {
			continue
		}
		entries = append(entries, existing)
	}
	return State{Version: Version, Entries: limitEntries(entries, effectiveLimit(limit))}, nil
}

func (s State) Candidates(current WindowKey, live []LiveWindow, limit int) ([]Candidate, State) {
	current = normalizeKey(current)
	liveSet := liveWindowSet(live)
	kept := make([]Snapshot, 0, len(s.Entries))
	candidates := make([]Candidate, 0, len(s.Entries))
	for _, entry := range normalizeEntries(s.Entries, effectiveLimit(limit)) {
		key := entry.Key()
		if _, ok := liveSet[key]; !ok {
			continue
		}
		kept = append(kept, entry)
		candidates = append(candidates, Candidate{
			Snapshot:  entry,
			Label:     BuildLabel(entry),
			IsCurrent: key == current,
		})
	}
	return candidates, State{Version: Version, Entries: kept}
}

func (s Snapshot) Key() WindowKey {
	return normalizeKey(WindowKey{Socket: s.Socket, Session: s.Session, WindowID: s.WindowID})
}

func (s Snapshot) Valid() error {
	key := s.Key()
	if key.Session == "" || key.WindowID == "" {
		return ErrInvalidSnapshot
	}
	return nil
}

func BuildLabel(snapshot Snapshot) Label {
	snapshot = normalizeSnapshot(snapshot)
	primary := firstNonEmpty(snapshot.WindowName, snapshot.Project, snapshot.Session, snapshot.LastPaneTitle, snapshot.LastPaneTopic, snapshot.LastCommand)
	debug := debugTarget(snapshot)
	if primary == "" {
		primary = debug
	}

	secondaryParts := make([]string, 0, 3)
	for _, value := range []string{snapshot.LastPaneTitle, snapshot.LastPaneTopic, snapshot.LastCommand, snapshot.Project, snapshot.Session} {
		value = strings.TrimSpace(value)
		if value == "" || value == primary || containsString(secondaryParts, value) {
			continue
		}
		secondaryParts = append(secondaryParts, value)
		if len(secondaryParts) == 3 {
			break
		}
	}

	return Label{
		Primary:   primary,
		Secondary: strings.Join(secondaryParts, " · "),
		Debug:     debug,
	}
}

func normalizeEntries(entries []Snapshot, limit int) []Snapshot {
	if len(entries) == 0 {
		return nil
	}
	out := make([]Snapshot, 0, len(entries))
	seen := make(map[WindowKey]struct{}, len(entries))
	for _, entry := range entries {
		entry = normalizeSnapshot(entry)
		if err := entry.Valid(); err != nil {
			continue
		}
		key := entry.Key()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, entry)
		if len(out) == effectiveLimit(limit) {
			break
		}
	}
	return out
}

func limitEntries(entries []Snapshot, limit int) []Snapshot {
	limit = effectiveLimit(limit)
	if len(entries) <= limit {
		return entries
	}
	return entries[:limit]
}

func effectiveLimit(limit int) int {
	if limit <= 0 {
		return DefaultLimit
	}
	return limit
}

func normalizeSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Socket = strings.TrimSpace(snapshot.Socket)
	snapshot.Session = strings.TrimSpace(snapshot.Session)
	snapshot.WindowID = strings.TrimSpace(snapshot.WindowID)
	snapshot.WindowName = strings.TrimSpace(snapshot.WindowName)
	snapshot.Project = strings.TrimSpace(snapshot.Project)
	snapshot.LastPaneID = strings.TrimSpace(snapshot.LastPaneID)
	snapshot.LastPaneTitle = strings.TrimSpace(snapshot.LastPaneTitle)
	snapshot.PaneTitles = normalizePaneTitles(snapshot.PaneTitles)
	snapshot.PaneBadgeKinds = normalizePaneBadgeKinds(snapshot.PaneBadgeKinds)
	snapshot.LastPaneTopic = strings.TrimSpace(snapshot.LastPaneTopic)
	snapshot.LastCommand = strings.TrimSpace(snapshot.LastCommand)
	if !snapshot.LastFocusedAt.IsZero() {
		snapshot.LastFocusedAt = snapshot.LastFocusedAt.UTC()
	}
	return snapshot
}

func normalizePaneTitles(titles []string) []string {
	if len(titles) == 0 {
		return nil
	}
	out := make([]string, 0, len(titles))
	for _, title := range titles {
		if title = strings.TrimSpace(title); title != "" {
			out = append(out, title)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizePaneBadgeKinds normalizes each per-pane AI badge kind while keeping
// positional alignment with PaneTitles: a pane with no/unknown badge keeps an
// empty slot rather than shifting later panes. Trailing empties are trimmed and
// an all-empty slice collapses to nil so backward-compatible state stays clean.
func normalizePaneBadgeKinds(kinds []string) []string {
	if len(kinds) == 0 {
		return nil
	}
	out := make([]string, len(kinds))
	last := -1
	for i, kind := range kinds {
		normalized := aibadge.Normalize(kind)
		out[i] = normalized
		if normalized != "" {
			last = i
		}
	}
	if last < 0 {
		return nil
	}
	return out[:last+1]
}

func normalizeKey(key WindowKey) WindowKey {
	return WindowKey{
		Socket:   strings.TrimSpace(key.Socket),
		Session:  strings.TrimSpace(key.Session),
		WindowID: strings.TrimSpace(key.WindowID),
	}
}

func liveWindowSet(live []LiveWindow) map[WindowKey]struct{} {
	out := make(map[WindowKey]struct{}, len(live))
	for _, window := range live {
		key := normalizeKey(WindowKey{
			Socket:   window.Socket,
			Session:  window.Session,
			WindowID: window.WindowID,
		})
		if key.Session == "" || key.WindowID == "" {
			continue
		}
		out[key] = struct{}{}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func debugTarget(snapshot Snapshot) string {
	key := snapshot.Key()
	parts := make([]string, 0, 3)
	if key.Session != "" {
		parts = append(parts, key.Session)
	}
	if key.WindowID != "" {
		parts = append(parts, "win "+key.WindowID)
	}
	if pane := strings.TrimSpace(snapshot.LastPaneID); pane != "" {
		parts = append(parts, "pane "+pane)
	}
	return strings.Join(parts, " ")
}

func containsString(values []string, target string) bool {
	return slices.Contains(values, target)
}
