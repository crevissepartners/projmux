// Package sessionstate stores versioned tmux session snapshots.
//
// Phase 1 writes snapshots with a same-directory temp file followed by atomic
// rename. It does not use a cross-process file lock yet.
//
// The pure snapshot data model (types, validation, normalization) lives in
// internal/core/sessionstate; this package re-exports it so existing
// importers keep a single import path while owning the file persistence and
// tmux replay adapters.
package sessionstate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	coresessionstate "github.com/crevissepartners/projmux/internal/core/sessionstate"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

const (
	// Version is the current on-disk session snapshot schema version.
	Version = coresessionstate.Version

	sessionDirName = "sessions"
	fileMode       = localstate.PrivateFileMode
)

var (
	ErrInvalidSessionName = coresessionstate.ErrInvalidSessionName
	ErrNotFound           = errors.New("session snapshot not found")
	ErrMissingVersion     = coresessionstate.ErrMissingVersion
	ErrUnsupportedVersion = coresessionstate.ErrUnsupportedVersion
	ErrInvalidSnapshot    = coresessionstate.ErrInvalidSnapshot
	ErrMalformedJSON      = errors.New("malformed session snapshot JSON")
)

// Snapshot, Window, Pane, and Recipe alias the core snapshot data model so
// values flow between the adapter and core rules without conversion.
type (
	Snapshot = coresessionstate.Snapshot
	Window   = coresessionstate.Window
	Pane     = coresessionstate.Pane
	Recipe   = coresessionstate.Recipe
)

const (
	RecipeKindShell   = coresessionstate.RecipeKindShell
	RecipeKindAgent   = coresessionstate.RecipeKindAgent
	RecipeKindStartup = coresessionstate.RecipeKindStartup

	SourceAutosave = coresessionstate.SourceAutosave
	SourceFresh    = coresessionstate.SourceFresh
)

// Re-exported constructors and label helpers from the core snapshot model.
var (
	LayoutSource                  = coresessionstate.LayoutSource
	SourceLabel                   = coresessionstate.SourceLabel
	ShellRecipe                   = coresessionstate.ShellRecipe
	AgentRecipe                   = coresessionstate.AgentRecipe
	AgentRecipeWithResumeMetadata = coresessionstate.AgentRecipeWithResumeMetadata
	StartupRecipe                 = coresessionstate.StartupRecipe
)

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
	if err := coresessionstate.ValidateSessionName(session); err != nil {
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

	localstate.RepairPrivateFile(path)
	return s.loadPath(session, path)
}

// LoadReadOnly reads and validates a snapshot without repairing permissions.
// Strict diagnostic/report consumers use this method so inspecting an
// existing source cannot mutate product state.
func (s Store) LoadReadOnly(session string) (Snapshot, error) {
	path, err := s.Path(session)
	if err != nil {
		return Snapshot{}, err
	}
	return s.loadPath(session, path)
}

func (s Store) loadPath(session, path string) (Snapshot, error) {
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
	return snap.Normalize(), nil
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

	snap = snap.Normalize()
	path, err := s.Path(snap.Session)
	if err != nil {
		return err
	}
	if err := localstate.EnsurePrivateDir(s.Dir); err != nil {
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
	localstate.RepairPrivateFile(path)
	return nil
}
