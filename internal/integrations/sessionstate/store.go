// Package sessionstate stores versioned tmux session snapshots.
//
// Phase 1 writes snapshots with a same-directory temp file followed by atomic
// rename. It does not use a cross-process file lock yet.
package sessionstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// Version is the current on-disk session snapshot schema version.
	Version = 1

	sessionDirName = "sessions"
	fileMode       = 0o644
)

var (
	ErrInvalidSessionName = errors.New("invalid session name")
	ErrNotFound           = errors.New("session snapshot not found")
	ErrMissingVersion     = errors.New("session snapshot version is missing")
	ErrUnsupportedVersion = errors.New("unsupported session snapshot version")
	ErrInvalidSnapshot    = errors.New("invalid session snapshot")
	ErrMalformedJSON      = errors.New("malformed session snapshot JSON")
)

// Snapshot is the versioned on-disk record for one tmux session. Phase 1 only
// records enough metadata to read and write the snapshot; replay is handled by
// a later phase.
type Snapshot struct {
	Version    int       `json:"version"`
	Session    string    `json:"session"`
	Source     string    `json:"source,omitempty"`
	DefaultCWD string    `json:"default_cwd,omitempty"`
	SavedAt    time.Time `json:"saved_at"`
	Windows    []Window  `json:"windows"`
}

// Window describes one tmux window in session order.
type Window struct {
	Index           int    `json:"index"`
	Name            string `json:"name"`
	Layout          string `json:"layout,omitempty"`
	ActivePaneIndex int    `json:"active_pane_index"`
	Panes           []Pane `json:"panes"`
}

// Pane describes one tmux pane and the recipe metadata needed by future replay.
type Pane struct {
	Index  int    `json:"index"`
	Title  string `json:"title,omitempty"`
	CWD    string `json:"cwd"`
	Recipe Recipe `json:"recipe"`
}

// Recipe records how a pane should be classified for future restore.
type Recipe struct {
	Kind            string `json:"kind"`
	Agent           string `json:"agent,omitempty"`
	ResumeID        string `json:"resume_id,omitempty"`
	ResumeSource    string `json:"resume_source,omitempty"`
	ResumeUpdatedAt string `json:"resume_updated_at,omitempty"`
	Topic           string `json:"topic,omitempty"`
	Command         string `json:"command,omitempty"`
}

const (
	RecipeKindShell   = "shell"
	RecipeKindAgent   = "agent"
	RecipeKindStartup = "startup"

	SourceAutosave = "autosave"
	SourceFresh    = "fresh"
)

// LayoutSource returns the display/source marker for a project layout preset.
func LayoutSource(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return "layout(" + name + ")"
}

// SourceLabel returns the user-facing source label for a snapshot. Missing
// source means an older autosave snapshot.
func (s Snapshot) SourceLabel() string {
	return SourceLabel(s.Source)
}

// SourceLabel normalizes empty source markers to the legacy autosave source.
func SourceLabel(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return SourceAutosave
	}
	return source
}

// ShellRecipe returns the plain-shell recipe form.
func ShellRecipe() Recipe {
	return Recipe{Kind: RecipeKindShell}
}

// AgentRecipe returns the agent recipe form with resume metadata.
func AgentRecipe(agent, resumeID, topic string) Recipe {
	return Recipe{
		Kind:     RecipeKindAgent,
		Agent:    agent,
		ResumeID: resumeID,
		Topic:    topic,
	}
}

// AgentRecipeWithResumeMetadata returns the agent recipe form with resume
// provenance metadata used by read-only health surfaces.
func AgentRecipeWithResumeMetadata(agent, resumeID, topic, source, updatedAt string) Recipe {
	return Recipe{
		Kind:            RecipeKindAgent,
		Agent:           agent,
		ResumeID:        strings.TrimSpace(resumeID),
		ResumeSource:    strings.TrimSpace(source),
		ResumeUpdatedAt: strings.TrimSpace(updatedAt),
		Topic:           topic,
	}
}

// StartupRecipe returns the declarative startup command recipe form.
func StartupRecipe(command string) Recipe {
	return Recipe{
		Kind:    RecipeKindStartup,
		Command: strings.TrimSpace(command),
	}
}

// Store persists session snapshots below Dir.
type Store struct {
	Dir string
}

// Summary is the compact status view for one saved snapshot.
type Summary struct {
	Session     string
	Source      string
	SavedAt     time.Time
	WindowCount int
	PaneCount   int
}

// NewStore builds a session snapshot store rooted at dir. Tests should pass a
// temp directory here to avoid writing to the real user state directory.
func NewStore(dir string) Store {
	return Store{Dir: dir}
}

// NewDefaultStoreFromEnv resolves the default
// ${XDG_STATE_HOME:-$HOME/.local/state}/projmux/sessions store.
func NewDefaultStoreFromEnv() (Store, error) {
	dir, err := DefaultDirFromEnv()
	if err != nil {
		return Store{}, err
	}
	return NewStore(dir), nil
}

// DefaultDirFromEnv resolves the default session snapshot directory.
func DefaultDirFromEnv() (string, error) {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("sessionstate: resolve user home: %w", err)
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateHome, "projmux", sessionDirName), nil
}

// Path returns the snapshot JSON path for session.
func (s Store) Path(session string) (string, error) {
	if err := validateSessionName(session); err != nil {
		return "", err
	}
	return filepath.Join(s.Dir, session+".json"), nil
}

// Load reads and validates a session snapshot.
func (s Store) Load(session string) (Snapshot, error) {
	path, err := s.Path(session)
	if err != nil {
		return Snapshot{}, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Snapshot{}, fmt.Errorf("%w: %s", ErrNotFound, path)
		}
		return Snapshot{}, fmt.Errorf("sessionstate: read snapshot %s: %w", path, err)
	}

	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("%w %s: %w", ErrMalformedJSON, path, err)
	}
	if err := snap.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("sessionstate: validate snapshot %s: %w", path, err)
	}
	if snap.Session != session {
		return Snapshot{}, fmt.Errorf("%w: path session %q does not match snapshot session %q", ErrInvalidSnapshot, session, snap.Session)
	}
	return snap.normalize(), nil
}

// Summary loads one snapshot and returns the fields needed by status surfaces.
func (s Store) Summary(session string) (Summary, error) {
	snap, err := s.Load(session)
	if err != nil {
		return Summary{}, err
	}
	panes := 0
	for _, window := range snap.Windows {
		panes += len(window.Panes)
	}
	return Summary{
		Session:     snap.Session,
		Source:      snap.SourceLabel(),
		SavedAt:     snap.SavedAt,
		WindowCount: len(snap.Windows),
		PaneCount:   panes,
	}, nil
}

// Delete removes the saved snapshot for session. Missing snapshots are treated
// as a successful no-op so Settings delete remains narrow and repeatable.
func (s Store) Delete(session string) error {
	path, err := s.Path(session)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("sessionstate: delete snapshot %s: %w", path, err)
	}
	return nil
}

// Save validates and atomically writes snap to its session path using a
// same-directory temp file followed by rename.
func (s Store) Save(snap Snapshot) error {
	if err := snap.Validate(); err != nil {
		return err
	}

	snap = snap.normalize()
	path, err := s.Path(snap.Session)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("sessionstate: create snapshot dir %s: %w", s.Dir, err)
	}

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("sessionstate: encode snapshot: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(s.Dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("sessionstate: create temp snapshot: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(fileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sessionstate: chmod temp snapshot: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sessionstate: write temp snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("sessionstate: close temp snapshot: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("sessionstate: rename temp snapshot: %w", err)
	}
	cleanup = false
	return nil
}

// Validate checks the schema version and required restore metadata.
func (s Snapshot) Validate() error {
	if s.Version == 0 {
		return ErrMissingVersion
	}
	if s.Version != Version {
		return fmt.Errorf("%w: %d", ErrUnsupportedVersion, s.Version)
	}
	if err := validateSessionName(s.Session); err != nil {
		return err
	}
	if s.SavedAt.IsZero() {
		return fmt.Errorf("%w: saved_at is required", ErrInvalidSnapshot)
	}
	if s.DefaultCWD != "" && !filepath.IsAbs(s.DefaultCWD) {
		return fmt.Errorf("%w: default_cwd must be absolute", ErrInvalidSnapshot)
	}
	for wi, window := range s.Windows {
		if window.Index < 0 {
			return fmt.Errorf("%w: window %d index must be non-negative", ErrInvalidSnapshot, wi)
		}
		if window.ActivePaneIndex < 0 {
			return fmt.Errorf("%w: window %d active_pane_index must be non-negative", ErrInvalidSnapshot, wi)
		}
		activePaneFound := len(window.Panes) == 0
		for pi, pane := range window.Panes {
			if pane.Index < 0 {
				return fmt.Errorf("%w: window %d pane %d index must be non-negative", ErrInvalidSnapshot, wi, pi)
			}
			if pane.Index == window.ActivePaneIndex {
				activePaneFound = true
			}
			if pane.CWD == "" {
				return fmt.Errorf("%w: window %d pane %d cwd is required", ErrInvalidSnapshot, wi, pi)
			}
			if !filepath.IsAbs(pane.CWD) {
				return fmt.Errorf("%w: window %d pane %d cwd must be absolute", ErrInvalidSnapshot, wi, pi)
			}
			if err := pane.Recipe.Validate(); err != nil {
				return fmt.Errorf("%w: window %d pane %d recipe: %w", ErrInvalidSnapshot, wi, pi, err)
			}
		}
		if !activePaneFound {
			return fmt.Errorf("%w: window %d active_pane_index %d does not match a pane index", ErrInvalidSnapshot, wi, window.ActivePaneIndex)
		}
	}
	return nil
}

// Validate checks that the recipe is one of the Phase 1 forms.
func (r Recipe) Validate() error {
	switch r.Kind {
	case RecipeKindShell:
		if r.Agent != "" || r.ResumeID != "" || r.ResumeSource != "" || r.ResumeUpdatedAt != "" || r.Topic != "" || r.Command != "" {
			return fmt.Errorf("%w: shell recipe cannot include replay metadata", ErrInvalidSnapshot)
		}
	case RecipeKindAgent:
		if strings.TrimSpace(r.Agent) == "" {
			return fmt.Errorf("%w: agent recipe requires agent", ErrInvalidSnapshot)
		}
		if r.Command != "" {
			return fmt.Errorf("%w: agent recipe cannot include startup command", ErrInvalidSnapshot)
		}
	case RecipeKindStartup:
		if strings.TrimSpace(r.Command) == "" {
			return fmt.Errorf("%w: startup recipe requires command", ErrInvalidSnapshot)
		}
		if r.Agent != "" || r.ResumeID != "" || r.ResumeSource != "" || r.ResumeUpdatedAt != "" || r.Topic != "" {
			return fmt.Errorf("%w: startup recipe cannot include agent metadata", ErrInvalidSnapshot)
		}
	case "":
		return fmt.Errorf("%w: recipe kind is required", ErrInvalidSnapshot)
	default:
		return fmt.Errorf("%w: unsupported recipe kind %q", ErrInvalidSnapshot, r.Kind)
	}
	return nil
}

func (s Snapshot) normalize() Snapshot {
	s.SavedAt = s.SavedAt.UTC()
	s.Source = strings.TrimSpace(s.Source)
	return s
}

func validateSessionName(session string) error {
	if strings.TrimSpace(session) == "" {
		return ErrInvalidSessionName
	}
	if session == "." || session == ".." || filepath.IsAbs(session) || strings.ContainsAny(session, `/\`) {
		return fmt.Errorf("%w: %q", ErrInvalidSessionName, session)
	}
	return nil
}
