package app

import (
	"context"
	"errors"
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

// aiPaneOptionTarget carries transport and its transient containment proof.
// Provider authority, semantic delivery, and durable failure vocabulary remain
// with their existing callers.
type aiPaneOptionTarget struct {
	transport     tmuxTransport
	provenPaneUID string
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
	target := aiPaneOptionTarget{transport: defaultRuntimeMutationRoute().Target}
	runner := explicitTmuxRunner{
		Runner: aiCommandMuxBackend{runCommand: c.runCommand, readCommand: c.readCommand},
		Target: target.transport,
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
	target.provenPaneUID = strings.TrimSpace(rows[0][2])
	return target, nil
}

// withAIHookPaneRoute binds only this attributed hook invocation. Shared
// notification/authority helpers keep their existing semantics, but all of
// their tmux reads and writes use the same transport as reflection writes.
// Non-hook commands retain their original seams. Already explicit commands
// keep their transport receipt after proving that it is this hook's route.
func (c *aiCommand) withAIHookPaneRoute(identity aiHookPaneIdentity) *aiCommand {
	paneID := identity.paneID
	clone := c.cloneSeams()
	// Attribution supplies the UID from its original Registry snapshot.
	// Never look it up again by %N: a replacement resource could own that
	// runtime handle by now. Containment is still re-proved for each call.
	inherited := c.hasInheritedAIHookTransport()
	expectedUID := identity.paneUID
	if inherited {
		expectedUID = ""
	}
	refusedIdentity := !inherited && expectedUID == ""
	checkProof := func(uid string) bool { return expectedUID == "" || uid == expectedUID }
	routeArgs := func(name string, args []string) ([]string, error) {
		if name != "tmux" {
			return args, nil
		}
		if refusedIdentity {
			return nil, errAIPaneWriteUnavailable
		}
		// Explicit operations already perform their own containment probe.
		// The reader below checks that probe's UID before a write can follow.
		if len(args) >= 2 && (args[0] == "-L" || args[0] == "-S") {
			return args, nil
		}
		route, refusal := c.aiPaneOptionRoute(paneID)
		if refusal != nil || (route.provenPaneUID != "" && !checkProof(route.provenPaneUID)) {
			return nil, errAIPaneWriteUnavailable
		}
		return route.args(args...), nil
	}
	clone.runCommand = func(ctx context.Context, name string, args ...string) error {
		routed, err := routeArgs(name, args)
		if err != nil {
			return err
		}
		if c.runCommand == nil {
			return errors.New("ai command runner is not configured")
		}
		return c.runCommand(ctx, name, routed...)
	}
	clone.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		routed, err := routeArgs(name, args)
		if err != nil {
			return nil, err
		}
		if c.readCommand == nil {
			return nil, errors.New("ai command reader is not configured")
		}
		out, err := c.readCommand(ctx, name, routed...)
		if err == nil && name == "tmux" && len(args) > 0 && args[len(args)-1] == aiPaneOptionRouteFormat {
			rows := splitTmuxRows(string(out), 3)
			if len(rows) != 1 || !checkProof(strings.TrimSpace(rows[0][2])) {
				return nil, errAIPaneWriteUnavailable
			}
		}
		return out, err
	}
	return clone
}
