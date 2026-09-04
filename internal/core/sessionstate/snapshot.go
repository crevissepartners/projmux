// Package sessionstate holds the pure session snapshot data model shared by
// the persistence/replay adapter (internal/integrations/sessionstate) and
// core rules such as project layout presets. It has no I/O: validation and
// normalization only.
package sessionstate

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Version is the current on-disk session snapshot schema version.
// Legacy: retained for validating and reading persisted session-state
// snapshots; sunset when a schema version bump plus migration window makes
// v1 snapshot compatibility intentionally droppable.
const Version = 1

var (
	ErrInvalidSessionName = errors.New("invalid session name")
	ErrMissingVersion     = errors.New("session snapshot version is missing")
	ErrUnsupportedVersion = errors.New("unsupported session snapshot version")
	ErrInvalidSnapshot    = errors.New("invalid session snapshot")
)

// ResourceMetadata is the additive Projmux resource-metadata block carried by
// a snapshot so restored resources keep their opaque identity.
//
// It is an omitempty addition at snapshot Version 1 rather than a schema bump:
// snapshots written before it decode with a nil block, and snapshots written
// without resource metadata still serialize byte-identically to the older
// form. Field spelling stays snake_case to match the rest of this file; the
// resource-model camelCase spelling is used only by the resource registry.
type ResourceMetadata struct {
	UID                   string            `json:"uid,omitempty"`
	Name                  string            `json:"name,omitempty"`
	Labels                map[string]string `json:"labels,omitempty"`
	OwnerKind             string            `json:"owner_kind,omitempty"`
	OwnerUID              string            `json:"owner_uid,omitempty"`
	RegistrySchemaVersion int               `json:"registry_schema_version,omitempty"`
}

// Snapshot is the versioned record for one tmux session. Phase 1 only
// records enough metadata to read and write the snapshot; replay is handled
// by the integrations adapter.
//
// Metadata carries the owning Project resource identity. The tmux session
// itself owns no uid or name: it is a 1:1 runtime projection of the Project.
type Snapshot struct {
	Version    int               `json:"version"`
	Session    string            `json:"session"`
	Source     string            `json:"source,omitempty"`
	DefaultCWD string            `json:"default_cwd,omitempty"`
	SavedAt    time.Time         `json:"saved_at"`
	Metadata   *ResourceMetadata `json:"metadata,omitempty"`
	Windows    []Window          `json:"windows"`
}

// Window describes one tmux window in session order.
type Window struct {
	Index           int               `json:"index"`
	RuntimeID       string            `json:"-"`
	RegistryUID     string            `json:"-"`
	Name            string            `json:"name"`
	Layout          string            `json:"layout,omitempty"`
	ActivePaneIndex int               `json:"active_pane_index"`
	Metadata        *ResourceMetadata `json:"metadata,omitempty"`
	Panes           []Pane            `json:"panes"`
}

// Pane describes one tmux pane and the recipe metadata needed by future replay.
type Pane struct {
	Index         int               `json:"index"`
	RuntimeID     string            `json:"-"`
	RegistryUID   string            `json:"-"`
	Label         string            `json:"label,omitempty"`
	Title         string            `json:"title,omitempty"`
	CWD           string            `json:"cwd"`
	Metadata      *ResourceMetadata `json:"metadata,omitempty"`
	AgentMetadata *ResourceMetadata `json:"agent_metadata,omitempty"`
	Recipe        Recipe            `json:"recipe"`
}

// Recipe records how a pane should be classified for future restore.
type Recipe struct {
	Kind            string `json:"kind"`
	Agent           string `json:"agent,omitempty"`
	ResumeID        string `json:"resume_id,omitempty"`
	ResumeSource    string `json:"resume_source,omitempty"`
	ResumeUpdatedAt string `json:"resume_updated_at,omitempty"`
	Topic           string `json:"topic,omitempty"`
	TopicManual     bool   `json:"topic_manual,omitempty"`
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

// Validate checks the schema version and required restore metadata.
func (s Snapshot) Validate() error {
	if s.Version == 0 {
		return ErrMissingVersion
	}
	if s.Version != Version {
		return fmt.Errorf("%w: %d", ErrUnsupportedVersion, s.Version)
	}
	if err := ValidateSessionName(s.Session); err != nil {
		return err
	}
	if s.SavedAt.IsZero() {
		return fmt.Errorf("%w: saved_at is required", ErrInvalidSnapshot)
	}
	if s.DefaultCWD != "" && !filepath.IsAbs(s.DefaultCWD) {
		return fmt.Errorf("%w: default_cwd must be absolute", ErrInvalidSnapshot)
	}
	if err := s.Metadata.validate("snapshot"); err != nil {
		return err
	}
	for wi, window := range s.Windows {
		if window.Index < 0 {
			return fmt.Errorf("%w: window %d index must be non-negative", ErrInvalidSnapshot, wi)
		}
		if err := window.Metadata.validate(fmt.Sprintf("window %d", wi)); err != nil {
			return err
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
			if err := pane.Metadata.validate(fmt.Sprintf("window %d pane %d", wi, pi)); err != nil {
				return err
			}
			if err := pane.AgentMetadata.validate(fmt.Sprintf("window %d pane %d agent", wi, pi)); err != nil {
				return err
			}
			if pane.AgentMetadata != nil {
				if pane.Recipe.Kind != RecipeKindAgent || pane.Metadata == nil || window.Metadata == nil ||
					pane.Metadata.OwnerKind != "Agent" || pane.Metadata.OwnerUID != pane.AgentMetadata.UID ||
					pane.AgentMetadata.OwnerKind != "Window" || pane.AgentMetadata.OwnerUID != window.Metadata.UID {
					return fmt.Errorf("%w: window %d pane %d Agent metadata does not match the exact Agent/Pane/Window owner chain", ErrInvalidSnapshot, wi, pi)
				}
			}
			if err := pane.Recipe.Validate(); err != nil {
				return fmt.Errorf("%w: window %d pane %d recipe: %w", ErrInvalidSnapshot, wi, pi, err)
			}
		}
		if !activePaneFound {
			return fmt.Errorf("%w: window %d active_pane_index %d does not match a pane index", ErrInvalidSnapshot, wi, window.ActivePaneIndex)
		}
	}
	if _, _, err := s.RegistrySchemaProvenance(); err != nil {
		return err
	}
	return nil
}

// RegistrySchemaProvenance returns the one Registry schema version carried by
// every metadata-bearing resource in the snapshot. The boolean is false only
// when the snapshot carries no resource metadata at all. A zero version is the
// legacy pre-provenance form; it is still a real, consistently legacy marker
// when metadata blocks are present.
//
// Missing ancestors do not hide a child marker: a Pane stamped by a current
// producer makes the snapshot current even when the top-level metadata block
// is absent. Conversely, mixing zero/v3/v4 markers is refused instead of
// guessing which resource supplied the authoritative version.
func (s Snapshot) RegistrySchemaProvenance() (version int, present bool, err error) {
	visit := func(where string, metadata *ResourceMetadata) error {
		if metadata == nil {
			return nil
		}
		if !present {
			version, present = metadata.RegistrySchemaVersion, true
			return nil
		}
		if metadata.RegistrySchemaVersion != version {
			return fmt.Errorf("%w: mixed registry schema provenance: %s carries v%d, want v%d",
				ErrInvalidSnapshot, where, metadata.RegistrySchemaVersion, version)
		}
		return nil
	}
	if err := visit("snapshot", s.Metadata); err != nil {
		return 0, false, err
	}
	for wi, window := range s.Windows {
		if err := visit(fmt.Sprintf("window %d", wi), window.Metadata); err != nil {
			return 0, false, err
		}
		for pi, pane := range window.Panes {
			if err := visit(fmt.Sprintf("window %d pane %d", wi, pi), pane.Metadata); err != nil {
				return 0, false, err
			}
			if err := visit(fmt.Sprintf("window %d pane %d agent", wi, pi), pane.AgentMetadata); err != nil {
				return 0, false, err
			}
		}
	}
	return version, present, nil
}

// validate rejects a present-but-identity-free resource metadata block. A nil
// block is always valid: snapshots written before resource metadata existed
// must keep loading unchanged.
func (m *ResourceMetadata) validate(where string) error {
	if m == nil {
		return nil
	}
	if strings.TrimSpace(m.UID) == "" {
		return fmt.Errorf("%w: %s metadata requires uid", ErrInvalidSnapshot, where)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("%w: %s metadata requires name", ErrInvalidSnapshot, where)
	}
	if (m.OwnerKind == "") != (m.OwnerUID == "") {
		return fmt.Errorf("%w: %s metadata owner_kind and owner_uid must be set together", ErrInvalidSnapshot, where)
	}
	if m.RegistrySchemaVersion < 0 {
		return fmt.Errorf("%w: %s metadata registry_schema_version must not be negative", ErrInvalidSnapshot, where)
	}
	return nil
}

// Validate checks that the recipe is one of the Phase 1 forms.
func (r Recipe) Validate() error {
	switch r.Kind {
	case RecipeKindShell:
		if r.Agent != "" || r.ResumeID != "" || r.ResumeSource != "" || r.ResumeUpdatedAt != "" || r.Topic != "" || r.TopicManual || r.Command != "" {
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
		if r.Agent != "" || r.ResumeID != "" || r.ResumeSource != "" || r.ResumeUpdatedAt != "" || r.Topic != "" || r.TopicManual {
			return fmt.Errorf("%w: startup recipe cannot include agent metadata", ErrInvalidSnapshot)
		}
	case "":
		return fmt.Errorf("%w: recipe kind is required", ErrInvalidSnapshot)
	default:
		return fmt.Errorf("%w: unsupported recipe kind %q", ErrInvalidSnapshot, r.Kind)
	}
	return nil
}

// Normalize returns the snapshot with canonical field forms (UTC timestamps,
// trimmed source marker).
func (s Snapshot) Normalize() Snapshot {
	s.SavedAt = s.SavedAt.UTC()
	s.Source = strings.TrimSpace(s.Source)
	return s
}

// ValidateSessionName rejects empty, relative-traversal, absolute, and
// separator-bearing session names.
func ValidateSessionName(session string) error {
	if strings.TrimSpace(session) == "" {
		return ErrInvalidSessionName
	}
	if session == "." || session == ".." || filepath.IsAbs(session) || strings.ContainsAny(session, `/\`) {
		return fmt.Errorf("%w: %q", ErrInvalidSessionName, session)
	}
	return nil
}
