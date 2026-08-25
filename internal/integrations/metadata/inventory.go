package metadata

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// The runtime observation half of the resolved resource graph.
//
// It is deliberately a fixed set of list queries rather than a per-resource
// probe. A registry with two hundred Panes must cost the same as one with two,
// or every read verb on a busy machine pays a tmux round trip per row -- and
// the per-row shape is also less correct, because rows observed at different
// moments cannot be judged against one snapshot.
//
// Three properties are contractual:
//
//   - Every call is routed through exactly one server. There is no unprefixed
//     tmux invocation anywhere in this file, so a graph asked about one socket
//     can never answer with the default server's objects or read a sibling
//     socket.
//   - Nothing here writes. No set-option, no adopt, no re-mirror. A read that
//     repaired a binding would turn every status query into a mutation.
//   - A failed query degrades exactly one scope. The Registry rows judged
//     against that scope become unknown with a stated reason; the other scopes
//     keep their observation, because a Pane whose tmux pane is provably gone is
//     still offline when the window query failed.

// transportRunner pins every tmux call to one exact server.
type transportRunner struct {
	runner    Runner
	transport resourcegraph.Transport
}

func (t transportRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if t.runner == nil {
		return nil, errors.New("metadata: inventory requires a tmux runner")
	}
	if name != "tmux" {
		return nil, fmt.Errorf("metadata: inventory cannot route executable %q", name)
	}
	prefix := t.transport.Args()
	if len(prefix) == 0 {
		return nil, errors.New("metadata: inventory requires an exact tmux transport")
	}
	routed := make([]string, 0, len(args)+len(prefix))
	routed = append(routed, prefix...)
	routed = append(routed, args...)
	return t.runner.Run(ctx, name, routed...)
}

// InventoryObserver takes at most one bounded observation of one tmux server per
// instance.
//
// The lifetime is the invocation, not a TTL. A cache with an expiry would defeat
// the property that makes the observation worth taking: closing a pane must make
// the next command report it offline, not the next command after the window
// expires. Memoizing within one instance is the shortest lifetime that still
// answers a whole invocation from a single consistent snapshot -- a route that
// resolves several times observes the machine once and judges every row against
// the same bytes.
type InventoryObserver struct {
	runner    Runner
	transport resourcegraph.Transport
	once      sync.Once
	result    resourcegraph.Inventory
}

// NewInventoryObserver builds an observer for exactly transport.
//
// An absent transport is legal and costs zero tmux calls: the observation is a
// Registry-only snapshot whose every runtime scope is unavailable. That is the
// case that makes an implicit default-server probe impossible to reach by
// accident.
func NewInventoryObserver(runner Runner, transport resourcegraph.Transport) *InventoryObserver {
	return &InventoryObserver{runner: runner, transport: transport}
}

// Observe returns the memoized inventory. The first caller's context is the one
// the queries run under; later callers get the same bytes without re-querying.
func (o *InventoryObserver) Observe(ctx context.Context) resourcegraph.Inventory {
	o.once.Do(func() { o.result = o.observe(ctx) })
	return o.result.Clone()
}

func (o *InventoryObserver) observe(ctx context.Context) resourcegraph.Inventory {
	inventory := resourcegraph.Inventory{
		Transport: o.transport,
		HostMode:  resourcegraph.HostModeUnknown,
	}
	if !o.transport.Present() {
		reason := "no exact tmux transport: pass a socket name or an absolute socket path to observe a server"
		for _, scope := range resourcegraph.Scopes() {
			inventory = inventory.MarkUnavailable(scope, reason)
		}
		return inventory
	}

	runner := transportRunner{runner: o.runner, transport: o.transport}
	host, err := runner.Run(ctx, "tmux", "show-options", "-gv", tmuxopts.AppGlobal)
	switch {
	case err != nil && optionUnset(err):
		// tmux answers a read of an unset user option with a non-zero
		// "invalid option", not with an empty value. That is an observation, not
		// a failed one: a server with no @projmux_app is a server projmux did
		// not start. Reporting it as unavailable instead would leave host mode
		// permanently unknown on every standalone server, which downgrades every
		// unmarked object there from unattributed to foreign for a reason that
		// has nothing to do with the object.
		inventory.HostMode = resourcegraph.HostModeFromAppMarker("")
	case err != nil && serverAbsent(err):
		// A server that is not running is knowledge, not a failure: nothing is
		// live on it. The three object scopes stay available and empty, so every
		// Registry row reports offline instead of unknown, and only host
		// ownership -- which genuinely cannot be observed -- is unavailable.
		return inventory.MarkUnavailable(resourcegraph.ScopeHostMode,
			"no tmux server on "+o.transport.String())
	case err != nil:
		inventory = inventory.MarkUnavailable(resourcegraph.ScopeHostMode,
			"host ownership could not be observed: "+err.Error())
	default:
		inventory.HostMode = resourcegraph.HostModeFromAppMarker(string(host))
	}

	sessions, err := runner.Run(ctx, "tmux", "list-sessions", "-F", tmuxFormat(
		"#{session_id}", "#{session_name}",
		"#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.ProjectNameSession+"}",
		"#{"+tmuxopts.ProjectPathSession+"}",
		"#{"+tmuxopts.SessionRole+"}", "#{"+tmuxopts.EphemeralSession+"}"))
	if err != nil {
		inventory = inventory.MarkUnavailable(resourcegraph.ScopeSessions,
			"tmux sessions could not be listed: "+err.Error())
	} else {
		for _, row := range parseRows(string(sessions), 7) {
			if row[0] == "" {
				continue
			}
			inventory.Sessions = append(inventory.Sessions, resourcegraph.Session{
				ID:          row[0],
				Name:        row[1],
				ProjectUID:  strings.TrimSpace(row[2]),
				ProjectName: row[3],
				Root:        row[4],
				Role:        strings.TrimSpace(row[5]),
				Ephemeral:   strings.TrimSpace(row[6]) == resourcegraph.EphemeralMarker,
			})
		}
	}

	windows, err := runner.Run(ctx, "tmux", "list-windows", "-a", "-F", tmuxFormat(
		"#{window_id}", "#{session_id}", "#{window_index}", "#{window_name}",
		"#{"+tmuxopts.WindowUID+"}", "#{"+tmuxopts.WindowName+"}"))
	if err != nil {
		inventory = inventory.MarkUnavailable(resourcegraph.ScopeWindows,
			"tmux windows could not be listed: "+err.Error())
	} else {
		for _, row := range parseRows(string(windows), 6) {
			if row[0] == "" {
				continue
			}
			inventory.Windows = append(inventory.Windows, resourcegraph.Window{
				ID:           row[0],
				SessionID:    row[1],
				Index:        row[2],
				DisplayName:  row[3],
				UID:          strings.TrimSpace(row[4]),
				MirroredName: row[5],
			})
		}
	}

	panes, err := runner.Run(ctx, "tmux", "list-panes", "-a", "-F", tmuxFormat(
		"#{pane_id}", "#{window_id}",
		"#{"+tmuxopts.PaneUID+"}", "#{"+tmuxopts.PaneName+"}",
		"#{"+tmuxopts.AgentProviderPane+"}", "#{"+tmuxopts.AgentLaunchAuthorshipPane+"}", "#{pane_title}",
		"#{"+tmuxopts.AgentSessionIDPane+"}", "#{"+tmuxopts.AgentThreadIDPane+"}"))
	if err != nil {
		inventory = inventory.MarkUnavailable(resourcegraph.ScopePanes,
			"tmux panes could not be listed: "+err.Error())
	} else {
		rows := parseRows(string(panes), 9)
		// Older test adapters and compatibility observers may still return the
		// pre-L8 six-field row. Treat the two routing indexes as absent without
		// weakening production: the production format above always requests all
		// nine in the same bounded list query.
		if len(rows) == 0 && strings.TrimSpace(string(panes)) != "" {
			for _, row := range parseRows(string(panes), 6) {
				rows = append(rows, []string{row[0], row[1], row[2], row[3], row[4], "", row[5], "", ""})
			}
		}
		for _, row := range rows {
			if row[0] == "" {
				continue
			}
			inventory.Panes = append(inventory.Panes, resourcegraph.Pane{
				ID:                    row[0],
				WindowID:              row[1],
				UID:                   strings.TrimSpace(row[2]),
				MirroredName:          row[3],
				AgentProvider:         strings.TrimSpace(row[4]),
				AgentLaunchAuthorship: strings.TrimSpace(row[5]),
				Title:                 row[6],
				AgentSessionID:        strings.TrimSpace(row[7]),
				AgentThreadID:         strings.TrimSpace(row[8]),
			})
		}
	}
	return inventory
}

// optionUnset recognizes the stderr signature tmux uses when a user option has
// never been set on the object being read.
//
// It is matched on text for the same reason serverAbsent is: the typed
// classifier lives behind the tmux client package, and importing it here would
// pull config, theme, and provider dependencies into the metadata adapter for
// one predicate.
func optionUnset(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "invalid option") || strings.Contains(message, "unknown option")
}

// serverAbsent recognizes the stderr signatures tmux uses when the socket has no
// server behind it.
//
// It matches on the composed error text rather than a typed failure because the
// typed classifier lives behind the tmux client package, and importing it here
// would pull config, theme, and provider dependencies into the metadata adapter
// for one predicate. The same text signatures are what the existing client-side
// compatibility classifier uses.
func serverAbsent(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no server running on") ||
		strings.Contains(message, "failed to connect to server") ||
		(strings.Contains(message, "error connecting to ") && strings.Contains(message, "(no such file or directory)"))
}
