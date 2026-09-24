package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	localstate "github.com/crevissepartners/projmux/internal/state"
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
// a human ran the deletion: a UI route never judges one, and a command whose
// process does not descend from the Agent's Pane (a Codex app-server command,
// for one) is recorded with the skip token that stopped the judgment.

const (
	deletionRecordsFile         = "deletion-records.jsonl"
	deletionRecordSchemaVersion = 1
)

// deletionVia is the closed vocabulary of routes that reach an explicit
// deletion.
type deletionVia string

const (
	deletionViaCLI   deletionVia = "cli"
	deletionViaUI    deletionVia = "ui"
	deletionViaPrune deletionVia = "prune"
)

// The closed operation vocabulary. `delete-project` is the deprecated alias of
// `unregister-project`; the record keeps the spelling the operator used.
const (
	deletionOperationDeleteWindow      = "delete-window"
	deletionOperationDeletePane        = "delete-pane"
	deletionOperationDeleteAgent       = "delete-agent"
	deletionOperationUnregisterProject = "unregister-project"
	deletionOperationDeleteProject     = "delete-project"
	deletionOperationPruneAgent        = "prune-agent"
	deletionOperationPruneProject      = "prune-project"
)

// deletionAffectedDeleted is the only affected action today.
const deletionAffectedDeleted = "deleted"

// Reason tokens, printed as `deletion not recorded: <token>`. They are a
// closed, stable vocabulary.
const (
	deletionSkipStateDirUnavailable  = "state-dir-unavailable"
	deletionSkipAppendFailed         = "append-failed"
	deletionSkipOperationIDMissing   = "operation-id-unavailable"
	deletionNotRecordedDiagnosticFmt = "deletion not recorded: %s\n"
)

// DeletionRecord is one line of deletion-records.jsonl.
type DeletionRecord struct {
	SchemaVersion int                `json:"schemaVersion"`
	At            time.Time          `json:"at"`
	Operation     string             `json:"operation"`
	OperationID   string             `json:"operationID"`
	Via           deletionVia        `json:"via"`
	Actor         DeletionActor      `json:"actor"`
	Targets       []DeletionTarget   `json:"targets"`
	Affected      []DeletionAffected `json:"affected"`
}

// DeletionActor is the pane-chain judgment of who ran the deletion. Every key
// is always present. Basis is `pane-chain` when the judgment succeeded, the
// creation skip token that stopped it otherwise, and empty when there was no
// ambient Pane to judge or the route never judges (UI).
type DeletionActor struct {
	AgentUID string `json:"agentUID"`
	PaneUID  string `json:"paneUID"`
	Basis    string `json:"basis"`
}

// DeletionTarget is one resource the operator named.
type DeletionTarget struct {
	Kind string `json:"kind"`
	UID  string `json:"uid"`
	Name string `json:"name"`
}

// DeletionAffected is one resource the committed cascade removed.
type DeletionAffected struct {
	Kind   string `json:"kind"`
	UID    string `json:"uid"`
	Name   string `json:"name"`
	Action string `json:"action"`
}

func newDeletionRecord(operation, operationID string, via deletionVia, actor DeletionActor,
	targets []DeletionTarget, affected []DeletionAffected,
) DeletionRecord {
	if targets == nil {
		targets = []DeletionTarget{}
	}
	if affected == nil {
		affected = []DeletionAffected{}
	}
	return DeletionRecord{
		SchemaVersion: deletionRecordSchemaVersion,
		At:            time.Now().UTC(),
		Operation:     operation,
		OperationID:   operationID,
		Via:           via,
		Actor:         actor,
		Targets:       targets,
		Affected:      affected,
	}
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
		out, err := (explicitTmuxRunner{runner: runner, target: route}).Run(ctx, "tmux", "display-message", "-p", "-t", paneID, "-F",
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
	targets := make([]DeletionTarget, 0, len(plan.Targets))
	for _, target := range plan.Targets {
		targets = append(targets, DeletionTarget{Kind: string(plan.Kind), UID: target.Match.UID, Name: target.Match.Name})
	}
	return targets
}

// deletionAffectedBetween is the actual cascade result: every resource of
// either root kind (Project, ControlSession) and every Window, Agent, and Pane
// present before the commit and absent after it, in kind order and then by
// uid.
func deletionAffectedBetween(before, after coremetadata.Registry) []DeletionAffected {
	var out []DeletionAffected
	diff := func(kind coremetadata.Kind, was, is []coremetadata.ObjectMeta) {
		present := make(map[string]bool, len(is))
		for _, meta := range is {
			present[meta.UID] = true
		}
		var removed []DeletionAffected
		for _, meta := range was {
			if !present[meta.UID] {
				removed = append(removed, DeletionAffected{Kind: string(kind), UID: meta.UID, Name: meta.Name, Action: deletionAffectedDeleted})
			}
		}
		slices.SortFunc(removed, func(a, b DeletionAffected) int { return strings.Compare(a.UID, b.UID) })
		out = append(out, removed...)
	}
	projects := func(registry coremetadata.Registry) []coremetadata.ObjectMeta {
		metas := make([]coremetadata.ObjectMeta, 0, len(registry.Projects))
		for _, project := range registry.Projects {
			metas = append(metas, project.Metadata)
		}
		return metas
	}
	controls := func(registry coremetadata.Registry) []coremetadata.ObjectMeta {
		metas := make([]coremetadata.ObjectMeta, 0, len(registry.ControlSessions))
		for _, control := range registry.ControlSessions {
			metas = append(metas, control.Metadata)
		}
		return metas
	}
	windows := func(registry coremetadata.Registry) []coremetadata.ObjectMeta {
		metas := make([]coremetadata.ObjectMeta, 0, len(registry.Windows))
		for _, window := range registry.Windows {
			metas = append(metas, window.Metadata)
		}
		return metas
	}
	agents := func(registry coremetadata.Registry) []coremetadata.ObjectMeta {
		metas := make([]coremetadata.ObjectMeta, 0, len(registry.Agents))
		for _, agent := range registry.Agents {
			metas = append(metas, agent.Metadata)
		}
		return metas
	}
	panes := func(registry coremetadata.Registry) []coremetadata.ObjectMeta {
		metas := make([]coremetadata.ObjectMeta, 0, len(registry.Panes))
		for _, pane := range registry.Panes {
			metas = append(metas, pane.Metadata)
		}
		return metas
	}
	diff(coremetadata.KindProject, projects(before), projects(after))
	diff(coremetadata.KindControlSession, controls(before), controls(after))
	diff(coremetadata.KindWindow, windows(before), windows(after))
	diff(coremetadata.KindAgent, agents(before), agents(after))
	diff(coremetadata.KindPane, panes(before), panes(after))
	return out
}

// recordDeletion appends one committed deletion's record next to the Registry
// store changed. It never fails the deletion: any failure is one stderr line.
func recordDeletion(store *resourceStore, record DeletionRecord, stderr io.Writer) {
	if store == nil || store.stateDir == nil {
		return
	}
	report := func(token string) {
		if stderr != nil {
			_, _ = fmt.Fprintf(stderr, deletionNotRecordedDiagnosticFmt, token)
		}
	}
	if strings.TrimSpace(record.OperationID) == "" {
		report(deletionSkipOperationIDMissing)
		return
	}
	stateDir, err := store.stateDir()
	if err != nil || strings.TrimSpace(stateDir) == "" {
		report(deletionSkipStateDirUnavailable)
		return
	}
	if token := (deletionRecordLog{path: filepath.Join(stateDir, deletionRecordsFile)}).append(record); token != "" {
		report(token)
	}
}

// deletionRecordLog appends deletion records exactly the way the termination
// journal appends its receipts: one framed O_APPEND write, then fsync. It is
// never truncated or rotated.
type deletionRecordLog struct {
	path string
}

// append returns the not-recorded token, or "" once the line is durable.
func (l deletionRecordLog) append(record DeletionRecord) string {
	if strings.TrimSpace(l.path) == "" {
		return deletionSkipStateDirUnavailable
	}
	body, err := json.Marshal(record)
	if err != nil {
		return deletionSkipAppendFailed
	}
	// The leading delimiter repairs a previous process's unterminated partial
	// tail before placing this complete row; both delimiters and the body are
	// one O_APPEND write.
	framed := make([]byte, 0, len(body)+2)
	framed = append(framed, '\n')
	framed = append(framed, body...)
	framed = append(framed, '\n')
	if err := localstate.EnsurePrivateDir(filepath.Dir(l.path)); err != nil {
		return deletionSkipStateDirUnavailable
	}
	// #nosec G304 -- the path is resolved from projmux's own state directory.
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, localstate.PrivateFileMode)
	if err != nil {
		return deletionSkipAppendFailed
	}
	defer file.Close()
	if _, err := file.Write(framed); err != nil {
		return deletionSkipAppendFailed
	}
	if err := file.Sync(); err != nil {
		return deletionSkipAppendFailed
	}
	return ""
}
