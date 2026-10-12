package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/services/mutations"
)

// Deletion records: every explicit deletion that commits its Registry
// transaction appends exactly one JSON line to
// <state>/deletion-records.jsonl, saying what the operator named, what the
// cascade actually removed, which route ran it, and -- when it can be proven
// the same way creation provenance is -- which Agent's Pane ran it.
//
// The record is written after the commit and never feeds back into the
// deletion: a record that cannot be written costs one stderr line and nothing
// else. Automatic lifecycle teardown and internal row cleanup are not explicit
// deletions and write nothing here.
//
// The actor is provenance, never authentication. An empty actor does not mean
// a human ran the deletion: a UI route never judges one, and a command run by
// a detached daemon (a Codex app-server command, for one) is never the Agent's.
// With no ambient Pane its actor is empty with an empty basis; with an Agent
// Pane's inherited TMUX/TMUX_PANE but a process that does not descend from that
// Pane, the basis is not-pane-descendant.

type deletionVia = mutations.DeletionVia

const deletionRecordsFile = mutations.DeletionRecordsFile
const deletionViaCLI = mutations.DeletionViaCLI
const deletionViaUI = mutations.DeletionViaUI
const deletionViaPrune = mutations.DeletionViaPrune
const deletionOperationUnregisterProject = mutations.DeletionOperationUnregisterProject
const deletionOperationDeleteProject = mutations.DeletionOperationDeleteProject
const deletionOperationPruneAgent = mutations.DeletionOperationPruneAgent
const deletionOperationPruneProject = mutations.DeletionOperationPruneProject

type DeletionRecord = mutations.DeletionRecord
type DeletionActor = mutations.DeletionActor
type DeletionTarget = mutations.DeletionTarget
type DeletionAffected = mutations.DeletionAffected

func newDeletionRecord(operation, id string, via deletionVia, actor DeletionActor, targets []DeletionTarget, affected []DeletionAffected) DeletionRecord {
	return mutations.NewDeletionRecord(operation, id, via, actor, targets, affected)
}

// deletionActorFrom projects the shared pane-chain judgment onto the record.
func deletionActorFrom(p creatorProvenance) DeletionActor {
	if p.recorded() {
		return DeletionActor{AgentUID: p.agentUID, PaneUID: p.paneUID, Basis: coremetadata.CreatorBasisPaneChain}
	}
	return DeletionActor{Basis: p.skip}
}

// observeDeletionActor judges the actor against the pre-commit working
// Registry. A UI route is never judged: a human pressed the key, whatever Pane
// the tmux client happens to report.
func observeDeletionActor(ctx context.Context, via deletionVia, lookupEnv func(string) string,
	processAncestors func() ([]int, error), runner tmuxCommandRunner, route tmuxTransport, working *coremetadata.Registry,
) DeletionActor {
	if via == deletionViaUI {
		return DeletionActor{}
	}
	return deletionActorFrom(observePaneChainActor(ctx, lookupEnv, processAncestors, working,
		deletionAnchorConfirm(runner, route, lookupEnv)))
}

// deletionAnchorConfirm proves the ambient `%N` on the deletion's own route.
//
// The deletion addresses route; the ambient Pane lives on the server $TMUX
// names. One `display-message -t %N` through route answers with the answering
// server's socket path and pid, the Pane's id, its process id, and its
// mirrored Pane uid. The Pane is confirmed only when that server is exactly
// the $TMUX server (socket path and pid), an explicit `-S` route names that
// same socket, the id round-trips, and the mirrored uid is the Registry Pane
// the in-memory step matched. There is no app-ownership authority on a
// deletion route to lean on, which is why the uid mirror is part of the proof.
func deletionAnchorConfirm(runner tmuxCommandRunner, route tmuxTransport, lookupEnv func(string) string) paneChainAnchorConfirm {
	return func(ctx context.Context, paneID, paneUID string) (int, string) {
		if runner == nil || !route.Present() || lookupEnv == nil {
			return 0, creatorSkipServerUnproven
		}
		socketPath, serverPID := inheritedTmuxServer(lookupEnv("TMUX"))
		if socketPath == "" || serverPID == "" {
			return 0, creatorSkipServerUnproven
		}
		if route.Kind == resourcegraph.TransportSocketPath && route.Value != socketPath {
			return 0, creatorSkipServerMismatch
		}
		out, err := (explicitTmuxRunner{Runner: runner, Target: route}).Run(ctx, "tmux", "display-message", "-p", "-t", paneID, "-F",
			tmuxRowFormat("#{socket_path}", "#{pid}", "#{pane_id}", "#{pane_pid}", "#{"+tmuxopts.PaneUID+"}"))
		if err != nil {
			return 0, creatorSkipAnchorQueryFailed
		}
		rows := splitTmuxRows(string(out), 5)
		if len(rows) != 1 {
			return 0, creatorSkipAnchorQueryFailed
		}
		row := rows[0]
		if row[0] != socketPath || row[1] != serverPID {
			return 0, creatorSkipServerMismatch
		}
		if row[2] != paneID || strings.TrimSpace(row[4]) != paneUID {
			return 0, creatorSkipAnchorPaneMismatch
		}
		pid, err := strconv.Atoi(strings.TrimSpace(row[3]))
		if err != nil || pid <= 1 {
			return 0, creatorSkipAnchorQueryFailed
		}
		return pid, ""
	}
}

// inheritedTmuxServer splits $TMUX ("<socket>,<server pid>,<session>") into
// the absolute socket path and server pid, or empty strings.
func inheritedTmuxServer(raw string) (string, string) {
	parts := strings.Split(strings.TrimSpace(raw), ",")
	if len(parts) < 2 {
		return "", ""
	}
	socketPath, serverPID := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath || serverPID == "" {
		return "", ""
	}
	return socketPath, serverPID
}

// lenientDeletionRoute resolves the server a route without a live half would
// address, for actor judgment only. A route that cannot be resolved is not an
// error here; it is simply unproven.
func lenientDeletionRoute(flags deleteSocketFlags, lookupEnv func(string) string) tmuxTransport {
	if lookupEnv == nil {
		return tmuxTransport{}
	}
	route, err := resourcegraph.ResolveTransport(resourcegraph.TransportRequest{
		SocketName:    flags.socket,
		SocketPath:    flags.socketPath,
		InheritedTMUX: lookupEnv("TMUX"),
	})
	if err != nil {
		return tmuxTransport{}
	}
	return route
}

// pruneDeletionActor is the deletion record seam the two prune routes share.
// Prune has no socket flags, so its actor is judged on the inherited $TMUX
// server, the same server `prune agent` observes; with no $TMUX the judgment
// honestly ends at anchor-server-unproven.
type pruneDeletionActor struct {
	lookupEnv        func(string) string
	processAncestors func() ([]int, error)
	runner           tmuxCommandRunner
	newOperationID   func() (string, error)
}

func newPruneDeletionActor() pruneDeletionActor {
	return pruneDeletionActor{
		lookupEnv:        os.Getenv,
		processAncestors: processAncestry,
		runner:           inttmux.ExecRunner{},
		newOperationID:   newCreateOperationID,
	}
}

// observe judges the prune's actor against the pre-commit working Registry.
func (a pruneDeletionActor) observe(working *coremetadata.Registry) DeletionActor {
	route := lenientDeletionRoute(deleteSocketFlags{}, a.lookupEnv)
	return observeDeletionActor(context.Background(), deletionViaPrune, a.lookupEnv, a.processAncestors, a.runner, route, working)
}

// record appends the committed prune's one line, listing every pruned target.
func (a pruneDeletionActor) record(store *resourceStore, operation string, actor DeletionActor,
	targets []DeletionTarget, affected []DeletionAffected, stderr io.Writer,
) {
	mint := a.newOperationID
	if mint == nil {
		mint = newCreateOperationID
	}
	operationID, _ := mint()
	recordDeletion(store, newDeletionRecord(operation, operationID, deletionViaPrune, actor, targets, affected), stderr)
}

// deletionTargetsOf lists the plan's named targets in plan order.
func deletionTargetsOf(plan deletePlan) []DeletionTarget {
	return mutations.DeletionTargetsOf(mutations.DeletePlan(plan))
}
func deletionAffectedBetween(before, after coremetadata.Registry) []DeletionAffected {
	return mutations.DeletionAffectedBetween(before, after)
}
func recordDeletion(store *resourceStore, record DeletionRecord, stderr io.Writer) {
	if store != nil {
		mutations.RecordDeletion(store.stateDir, record, stderr)
	}
}
