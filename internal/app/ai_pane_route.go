package app

import (
	"context"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

var aiPaneOptionRouteFormat = tmuxRowFormat(
	intmux.TmuxFormat(tmuxopts.AppGlobal),
	"#{pane_id}",
	intmux.TmuxFormat(tmuxopts.PaneUID),
)

// aiPaneOptionTarget carries transport only. Provider authority, semantic
// delivery, and durable failure vocabulary remain with their existing callers.
type aiPaneOptionTarget struct {
	transport tmuxTransport
}

func (t aiPaneOptionTarget) args(args ...string) []string {
	return append(t.transport.Args(), args...)
}

type aiPaneRouteFailureKind uint8

const (
	aiPaneRouteProbeFailed aiPaneRouteFailureKind = iota
	aiPaneRouteNoRow
	aiPaneRouteNotOwned
	aiPaneRouteForeign
)

// Raw probe output stays transient; only the Codex adapter classifies it into
// its existing closed reason vocabulary. Shared writes expose one bounded token.
type aiPaneRouteFailure struct {
	kind   aiPaneRouteFailureKind
	output []byte
	err    error
}

// aiPaneOptionRoute preserves an inherited exact server receipt. Detached
// callers use the existing app runtime only after it proves both app ownership
// and containment of the already-attributed resource Pane. No server search is
// performed. Inherited launch configuration needs no prior Pane option: its
// receipt already binds materialization before the first marker is written.
func (c *aiCommand) aiPaneOptionRoute(paneID string) (aiPaneOptionTarget, *aiPaneRouteFailure) {
	inherited, err := resourcegraph.ResolveTransport(resourcegraph.TransportRequest{InheritedTMUX: c.env("TMUX")})
	if err == nil && inherited.Present() {
		return aiPaneOptionTarget{transport: inherited}, nil
	}
	target := aiPaneOptionTarget{transport: defaultRuntimeMutationRoute().target}
	runner := explicitTmuxRunner{
		runner: aiCommandMuxBackend{runCommand: c.runCommand, readCommand: c.readCommand},
		target: target.transport,
	}
	output, runErr := runner.Run(context.Background(), "tmux",
		"display-message", "-p", "-t", paneID, "-F", aiPaneOptionRouteFormat)
	if runErr != nil {
		return target, &aiPaneRouteFailure{kind: aiPaneRouteProbeFailed, output: output, err: runErr}
	}
	rows := splitTmuxRows(string(output), 3)
	if len(rows) != 1 {
		return target, &aiPaneRouteFailure{kind: aiPaneRouteNoRow}
	}
	if resourcegraph.HostModeFromAppMarker(rows[0][0]) != resourcegraph.HostModeAppOwned {
		return target, &aiPaneRouteFailure{kind: aiPaneRouteNotOwned}
	}
	if rows[0][1] != paneID || strings.TrimSpace(rows[0][2]) == "" {
		return target, &aiPaneRouteFailure{kind: aiPaneRouteForeign}
	}
	return target, nil
}
