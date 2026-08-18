package metadata

import (
	"context"
	"fmt"
	"sort"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// Identity fragments are what a live tmux server can still testify to after the
// registry is gone, and nothing more.
//
// The mirror writes uids and names onto live sessions, windows, and panes so the
// resource routes can resolve a raw tmux target back to a resource. Read in the
// other direction, after a state loss, those options are evidence -- but only
// partial evidence, and the shape of what is missing is not obvious from a
// count. A machine showing twelve mirrored panes says nothing about the Windows
// that were offline, the Agents that owned half of those panes, or the names
// that were reserved but not in use.
//
// So this file deliberately produces observations, never a registry. It reports
// what identity is legible on the exact server it was pointed at, and the
// command layer pairs it with an explicit statement of what cannot be recovered
// from it. Turning fragments into a registry would need a user decision this
// package is in no position to make, and making it silently would replace a
// visible loss with an invisible one.

// IdentityFragmentKind is which resource kind a fragment carries identity for.
type IdentityFragmentKind = coremetadata.Kind

// IdentityFragment is one live tmux object that still mirrors Projmux identity.
type IdentityFragment struct {
	// Kind is the resource kind the mirrored uid belongs to.
	Kind IdentityFragmentKind `json:"kind"`
	// UID is the mirrored resource uid. A fragment is only produced when this
	// is non-empty: an object with no mirrored uid is unattributed runtime, not
	// a recoverable fragment.
	UID string `json:"uid"`
	// Name is the mirrored resource name, empty when the option is absent. It
	// is legible identity, but only for objects that are live right now.
	Name string `json:"name,omitempty"`
	// Target is the exact tmux handle the fragment was read from. It is
	// transport, never identity, and it is reported so an operator can go look.
	Target string `json:"target"`
	// ContainerUID is the mirrored uid of the containing object: the Project of
	// a Window's session, the Window of a Pane's window. It is containment, not
	// ownership -- an Agent-owned Pane is contained by a Window while the
	// registry records its owner as the Agent, and no tmux option carries that
	// Agent's uid.
	ContainerUID string `json:"containerUID,omitempty"`
	// Root is the cwd anchor of a Project session, empty for other kinds.
	Root string `json:"root,omitempty"`
	// AgentProvider is the provider option of a pane launched as an agent. Its
	// presence proves the Pane belonged to an Agent whose own uid is not
	// mirrored anywhere.
	AgentProvider string `json:"agentProvider,omitempty"`
}

// ObserveIdentityFragments reads every mirrored identity on the exact server
// behind the runner.
//
// It is three list queries and no writes. The budget is fixed rather than
// per-object so a large server costs the same three calls, matching the existing
// inventory readers. A failure of any query fails the whole observation: a
// partial fragment set would understate what is recoverable, and understating it
// could push an operator into accepting a loss they did not have to accept.
func (m Mirror) ObserveIdentityFragments(ctx context.Context) ([]IdentityFragment, error) {
	sessions, err := m.run(ctx, "list-sessions", "-F", tmuxFormat(
		"#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.ProjectNameSession+"}",
		"#{"+tmuxopts.ProjectPathSession+"}", "#{session_name}"))
	if err != nil {
		return nil, fmt.Errorf("metadata: list sessions for identity fragments: %w", err)
	}
	windows, err := m.run(ctx, "list-windows", "-a", "-F", tmuxFormat(
		"#{"+tmuxopts.WindowUID+"}", "#{"+tmuxopts.WindowName+"}",
		"#{window_id}", "#{session_name}", "#{window_index}"))
	if err != nil {
		return nil, fmt.Errorf("metadata: list windows for identity fragments: %w", err)
	}
	panes, err := m.run(ctx, "list-panes", "-a", "-F", tmuxFormat(
		"#{"+tmuxopts.PaneUID+"}", "#{"+tmuxopts.PaneName+"}",
		"#{pane_id}", "#{window_id}", "#{"+tmuxopts.AgentProviderPane+"}"))
	if err != nil {
		return nil, fmt.Errorf("metadata: list panes for identity fragments: %w", err)
	}

	var fragments []IdentityFragment
	// Containment is resolved in Go from stable ids rather than by asking tmux
	// to resolve an outer scope inside an inner format. A window option read
	// through a pane format would depend on tmux's option inheritance, and a
	// wrong inheritance answer here would attribute a Pane to a Window it never
	// belonged to.
	projectBySession := map[string]string{}
	for _, row := range parseRows(string(sessions), 4) {
		uid, name, root, session := row[0], row[1], row[2], row[3]
		if uid == "" {
			continue
		}
		projectBySession[session] = uid
		fragments = append(fragments, IdentityFragment{
			Kind: coremetadata.KindProject, UID: uid, Name: name, Target: session, Root: root,
		})
	}
	windowByID := map[string]string{}
	for _, row := range parseRows(string(windows), 5) {
		uid, name, windowID, session, index := row[0], row[1], row[2], row[3], row[4]
		if uid == "" {
			continue
		}
		windowByID[windowID] = uid
		fragments = append(fragments, IdentityFragment{
			Kind: coremetadata.KindWindow, UID: uid, Name: name,
			Target: session + ":" + index, ContainerUID: projectBySession[session],
		})
	}
	for _, row := range parseRows(string(panes), 5) {
		uid, name, paneID, windowID, provider := row[0], row[1], row[2], row[3], row[4]
		if uid == "" {
			continue
		}
		fragments = append(fragments, IdentityFragment{
			Kind: coremetadata.KindPane, UID: uid, Name: name, Target: paneID,
			ContainerUID: windowByID[windowID], AgentProvider: strings.TrimSpace(provider),
		})
	}

	// A stable total order over (kind, uid) keeps a JSON diagnostic byte-stable
	// across runs; tmux list order is not a contract.
	sort.SliceStable(fragments, func(a, b int) bool {
		if fragments[a].Kind != fragments[b].Kind {
			return identityFragmentKindRank(fragments[a].Kind) < identityFragmentKindRank(fragments[b].Kind)
		}
		return fragments[a].UID < fragments[b].UID
	})
	return fragments, nil
}

// identityFragmentKindRank orders kinds outermost first, which is the order an
// operator reads a topology in.
func identityFragmentKindRank(kind IdentityFragmentKind) int {
	switch kind {
	case coremetadata.KindProject:
		return 0
	case coremetadata.KindWindow:
		return 1
	case coremetadata.KindPane:
		return 2
	default:
		return 3
	}
}
