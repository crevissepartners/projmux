// Package aicapability defines the provider-neutral capability projection used
// by launch pickers and Agent actions. Provider protocol values are normalized
// before they reach this package.
package aicapability

import (
	"errors"
	"slices"
	"strings"
	"sync"
)

var (
	ErrUnavailable    = errors.New("AI provider capability unavailable")
	ErrStaleSelection = errors.New("AI provider capability selection is stale")
)

// Epoch binds one snapshot to both the app-server connection and the version
// negotiated on that connection. Either value changing invalidates selections
// rendered from the old snapshot.
type Epoch struct {
	Connection string
	Version    string
}

func (e Epoch) Valid() bool {
	return strings.TrimSpace(e.Connection) != "" && strings.TrimSpace(e.Version) != ""
}

type Model struct {
	ID                  string
	LaunchName          string
	DisplayName         string
	Description         string
	Default             bool
	DefaultEffort       string
	Efforts             []string
	InputModalities     []string
	SupportsPersonality bool
}

func (m Model) SupportsEffort(effort string) bool {
	return slices.Contains(m.Efforts, strings.TrimSpace(effort))
}

type ReviewCapability struct {
	Available bool
	Reason    string
}

type Snapshot struct {
	Epoch  Epoch
	Models []Model
	Review ReviewCapability
}

func (s Snapshot) Clone() Snapshot {
	out := s
	out.Models = make([]Model, len(s.Models))
	for i, model := range s.Models {
		out.Models[i] = model
		out.Models[i].Efforts = slices.Clone(model.Efforts)
		out.Models[i].InputModalities = slices.Clone(model.InputModalities)
	}
	return out
}

type Selection struct {
	Epoch      Epoch
	ModelID    string
	LaunchName string
	Effort     string
}

// Cache owns at most one connection/version snapshot. Replace deliberately
// drops the prior epoch; Validate never repairs a stale selection by matching
// model text in a newer snapshot.
type Cache struct {
	mu       sync.RWMutex
	snapshot Snapshot
	valid    bool
}

func (c *Cache) Replace(snapshot Snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot = snapshot.Clone()
	c.valid = snapshot.Epoch.Valid()
}

func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.snapshot = Snapshot{}
	c.valid = false
	c.mu.Unlock()
}

func (c *Cache) Snapshot() (Snapshot, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.valid {
		return Snapshot{}, false
	}
	return c.snapshot.Clone(), true
}

func (c *Cache) Validate(selection Selection) (Model, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.valid || selection.Epoch != c.snapshot.Epoch {
		return Model{}, ErrStaleSelection
	}
	for _, model := range c.snapshot.Models {
		if model.ID != selection.ModelID || model.LaunchName != selection.LaunchName {
			continue
		}
		if !model.SupportsEffort(selection.Effort) {
			return Model{}, ErrStaleSelection
		}
		return model, nil
	}
	return Model{}, ErrStaleSelection
}

type ReviewTargetKind string

const (
	ReviewUncommitted ReviewTargetKind = "uncommitted-changes"
	ReviewBaseBranch  ReviewTargetKind = "base-branch"
	ReviewCommit      ReviewTargetKind = "commit"
	ReviewCustom      ReviewTargetKind = "custom"
)

type ReviewTarget struct {
	Kind  ReviewTargetKind
	Value string
}

type ReviewStatus string

const (
	ReviewInProgress  ReviewStatus = "in-progress"
	ReviewCompleted   ReviewStatus = "completed"
	ReviewFailed      ReviewStatus = "failed"
	ReviewInterrupted ReviewStatus = "interrupted"
	ReviewUnknown     ReviewStatus = "unknown"
)

type ReviewResult struct {
	ThreadID string
	TurnID   string
	Status   ReviewStatus
}
