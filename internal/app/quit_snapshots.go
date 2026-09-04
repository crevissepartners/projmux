package app

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// quitSnapshotDependencies are the read/capture seams of the interactive safe
// quit action. The unsaved action and noninteractive flags never touch this
// value, which keeps their pre-feature call and exit semantics unchanged.
type quitSnapshotDependencies struct {
	loadRegistry func() (coremetadata.Registry, error)
	observe      func(context.Context, resourcegraph.Transport) resourcegraph.Inventory
	store        func() (sessionstate.Store, error)
	now          func() time.Time
	capture      func(context.Context, tmuxRunner, sessionstate.Store, coremetadata.Registry, quitSnapshotTarget, time.Time) (sessionstate.Snapshot, error)
}

func newQuitSnapshotDependencies() *quitSnapshotDependencies {
	return &quitSnapshotDependencies{
		loadRegistry: snapshotResourceRegistry,
		store:        sessionstate.NewDefaultStoreFromEnv,
		now:          time.Now,
		capture: func(ctx context.Context, runner tmuxRunner, store sessionstate.Store, registry coremetadata.Registry, target quitSnapshotTarget, at time.Time) (sessionstate.Snapshot, error) {
			return inttmux.NewClient(runner).SaveExplicitSessionSnapshotWithTransform(ctx, store, target.RuntimeID, target.Session, at,
				snapshotMetadataTransform(registry, target.ProjectUID))
		},
	}
}

type quitSnapshotTarget struct {
	ProjectUID string
	Session    string
	RuntimeID  string
}

type quitSnapshotSkipped struct {
	Offline      int
	Control      int
	Ephemeral    int
	Unattributed int
	Recoverable  int
	Foreign      int
	Conflict     int
}

type quitSnapshotPlan struct {
	Targets []quitSnapshotTarget
	Skipped quitSnapshotSkipped
}

type quitSnapshotLedgerEntry struct {
	Target quitSnapshotTarget
	Saved  bool
	Reason string
}

type quitSnapshotLedger struct {
	Entries []quitSnapshotLedgerEntry
	Skipped quitSnapshotSkipped
	Saved   int
	Failed  int
}

func (l quitSnapshotLedger) summary() string {
	return fmt.Sprintf("Project snapshot batch: targets=%d saved=%d failed=%d skipped(offline=%d control=%d ephemeral=%d unattributed=%d recoverable=%d foreign=%d conflict=%d)",
		len(l.Entries), l.Saved, l.Failed, l.Skipped.Offline, l.Skipped.Control,
		l.Skipped.Ephemeral, l.Skipped.Unattributed, l.Skipped.Recoverable,
		l.Skipped.Foreign, l.Skipped.Conflict)
}

func (l quitSnapshotLedger) failure() error {
	if l.Failed == 0 {
		return nil
	}
	parts := make([]string, 0, l.Failed)
	for _, entry := range l.Entries {
		if entry.Saved {
			continue
		}
		parts = append(parts, fmt.Sprintf("Project %s session %q: %s", entry.Target.ProjectUID, entry.Target.Session, entry.Reason))
	}
	return fmt.Errorf("save Project snapshots before quit failed (saved=%d failed=%d): %s", l.Saved, l.Failed, strings.Join(parts, "; "))
}

// planQuitSnapshotBatch freezes the invocation-start target set from exactly
// one Registry value and one complete resource-graph inventory. It is pure:
// every unavailable or conflicted preflight returns before a snapshot store is
// opened or a capture is attempted.
func planQuitSnapshotBatch(registry coremetadata.Registry, inventory resourcegraph.Inventory) (quitSnapshotPlan, error) {
	if err := registry.Validate(); err != nil {
		return quitSnapshotPlan{}, fmt.Errorf("save Project snapshots before quit: Registry graph is invalid: %w", err)
	}
	for _, scope := range resourcegraph.Scopes() {
		if unavailable, found := inventory.Unavailability(scope); found {
			return quitSnapshotPlan{}, fmt.Errorf("save Project snapshots before quit: inventory scope %s unavailable: %s", scope, boundedQuitSnapshotReason(unavailable.Reason))
		}
	}
	if inventory.HostMode != resourcegraph.HostModeAppOwned {
		return quitSnapshotPlan{}, errors.New("save Project snapshots before quit: exact runtime is not app-owned")
	}

	graph := resourcegraph.Resolve(registry, inventory)
	projectUIDs := make(map[string]bool, len(graph.Projects))
	for _, node := range graph.Projects {
		projectUIDs[node.Project.Metadata.UID] = true
		if node.Class == resourcegraph.ClassConflict {
			return quitSnapshotPlan{Skipped: quitSnapshotSkipped{Conflict: 1}}, fmt.Errorf("save Project snapshots before quit: managed Project identity conflict for %s", node.Project.Metadata.UID)
		}
	}
	for _, conflict := range graph.Conflicts {
		if conflict.Kind == resourcegraph.ObjectSession && projectUIDs[conflict.UID] {
			return quitSnapshotPlan{Skipped: quitSnapshotSkipped{Conflict: 1}}, fmt.Errorf("save Project snapshots before quit: managed Project identity conflict for %s: %s", conflict.UID, boundedQuitSnapshotReason(conflict.Detail))
		}
	}

	plan := quitSnapshotPlan{}
	for _, node := range graph.Projects {
		switch node.Status {
		case resourcegraph.StatusLive:
			if node.Class != resourcegraph.ClassManaged || node.Runtime == nil || node.Runtime.Kind != resourcegraph.ObjectSession {
				return quitSnapshotPlan{}, fmt.Errorf("save Project snapshots before quit: live Project %s has no exact managed session binding", node.Project.Metadata.UID)
			}
			plan.Targets = append(plan.Targets, quitSnapshotTarget{
				ProjectUID: node.Project.Metadata.UID,
				Session:    node.Runtime.Name,
				RuntimeID:  node.Runtime.ID,
			})
		case resourcegraph.StatusOffline, resourcegraph.StatusMissingRoot:
			plan.Skipped.Offline++
		case resourcegraph.StatusUnknown:
			return quitSnapshotPlan{}, fmt.Errorf("save Project snapshots before quit: Project %s runtime status is unknown", node.Project.Metadata.UID)
		default:
			return quitSnapshotPlan{}, fmt.Errorf("save Project snapshots before quit: Project %s has unsupported runtime status %q", node.Project.Metadata.UID, node.Status)
		}
	}

	controlUIDs := make(map[string]bool, len(graph.ControlSessions))
	for _, node := range graph.ControlSessions {
		controlUIDs[node.ControlSession.Metadata.UID] = true
	}
	for _, node := range graph.Runtime {
		if node.Ref.Kind != resourcegraph.ObjectSession {
			continue
		}
		if node.Class == resourcegraph.ClassManaged {
			if controlUIDs[node.ResourceUID] {
				plan.Skipped.Control++
			}
			continue
		}
		switch node.Class {
		case resourcegraph.ClassControl:
			plan.Skipped.Control++
		case resourcegraph.ClassEphemeral:
			plan.Skipped.Ephemeral++
		case resourcegraph.ClassUnattributed:
			plan.Skipped.Unattributed++
		case resourcegraph.ClassRecoverable:
			plan.Skipped.Recoverable++
		case resourcegraph.ClassForeign:
			plan.Skipped.Foreign++
		case resourcegraph.ClassConflict:
			plan.Skipped.Conflict++
		}
	}

	for _, target := range plan.Targets {
		if strings.TrimSpace(target.ProjectUID) == "" || strings.TrimSpace(target.Session) == "" || strings.TrimSpace(target.RuntimeID) == "" {
			return quitSnapshotPlan{}, errors.New("save Project snapshots before quit: managed Project target has incomplete identity")
		}
	}
	slices.SortStableFunc(plan.Targets, func(a, b quitSnapshotTarget) int {
		if n := cmp.Compare(a.ProjectUID, b.ProjectUID); n != 0 {
			return n
		}
		if n := cmp.Compare(a.Session, b.Session); n != 0 {
			return n
		}
		return cmp.Compare(a.RuntimeID, b.RuntimeID)
	})
	return plan, nil
}

func (c *quitCommand) saveProjectSnapshotsAndQuit(ctx context.Context, socketName string, stdout io.Writer) error {
	path, err := c.snapshotAppRuntimePath(ctx, socketName)
	if err != nil {
		return err
	}
	deps := c.snapshots
	if deps == nil {
		deps = newQuitSnapshotDependencies()
	}
	if deps.loadRegistry == nil {
		return errors.New("save Project snapshots before quit: Registry snapshot reader is not configured")
	}
	registry, err := deps.loadRegistry()
	if err != nil {
		return fmt.Errorf("save Project snapshots before quit: read Registry snapshot: %w", err)
	}
	transport := resourcegraph.Transport{Kind: resourcegraph.TransportSocketPath, Value: path, Source: resourcegraph.TransportSourceSocketPath}
	observe := deps.observe
	if observe == nil {
		observe = func(ctx context.Context, transport resourcegraph.Transport) resourcegraph.Inventory {
			return intmetadata.NewInventoryObserver(c.runner, transport).Observe(ctx)
		}
	}
	plan, err := planQuitSnapshotBatch(registry, observe(ctx, transport))
	if err != nil {
		return err
	}

	ledger := quitSnapshotLedger{Skipped: plan.Skipped, Entries: make([]quitSnapshotLedgerEntry, 0, len(plan.Targets))}
	if len(plan.Targets) > 0 {
		if deps.store == nil || deps.capture == nil {
			return errors.New("save Project snapshots before quit: snapshot capture is not configured")
		}
		store, err := deps.store()
		if err != nil {
			return fmt.Errorf("save Project snapshots before quit: open snapshot store: %w", err)
		}
		at := time.Now()
		if deps.now != nil {
			at = deps.now()
		}
		exactRunner := explicitTmuxRunner{runner: c.runner, target: tmuxTransport{Kind: tmuxSocketPath, Value: path, Source: tmuxSocketPathSource}}
		for _, target := range plan.Targets {
			entry := quitSnapshotLedgerEntry{Target: target}
			if _, captureErr := deps.capture(ctx, exactRunner, store, registry, target, at); captureErr != nil {
				entry.Reason = boundedQuitSnapshotReason(captureErr.Error())
				ledger.Failed++
			} else {
				entry.Saved = true
				ledger.Saved++
			}
			ledger.Entries = append(ledger.Entries, entry)
		}
	}
	if _, err := fmt.Fprintln(stdout, ledger.summary()); err != nil {
		return fmt.Errorf("report Project snapshot batch: %w", err)
	}
	if err := ledger.failure(); err != nil {
		return err
	}
	return c.shutdownAppRuntime(ctx, socketName, path)
}

// snapshotAppRuntimePath proves that the requested logical route currently
// names one exact app-owned physical server. shutdownAppRuntime repeats the
// same proof after every capture; this first proof prevents a foreign or drifted
// route from receiving any snapshot reads or writes.
func (c *quitCommand) snapshotAppRuntimePath(ctx context.Context, socketName string) (string, error) {
	if strings.TrimSpace(socketName) == "" {
		return "", errors.New("quit target socket is required")
	}
	if c.runner == nil {
		return "", errors.New("quit mux runner is not configured")
	}
	target, err := tmuxSocketNameTarget(socketName)
	if err != nil {
		return "", err
	}
	routed := explicitTmuxRunner{runner: c.runner, target: target}
	pathOut, err := routed.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		return "", fmt.Errorf("save Project snapshots before quit: resolve app tmux runtime: %w", err)
	}
	path := strings.TrimSpace(string(pathOut))
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("save Project snapshots before quit: requested socket has no exact absolute physical identity")
	}
	owned, err := routed.Run(ctx, "tmux", "show-options", "-gqv", "@projmux_app")
	if err != nil {
		return "", fmt.Errorf("save Project snapshots before quit: check app tmux runtime ownership: %w", err)
	}
	if strings.TrimSpace(string(owned)) != "1" {
		return "", errors.New("save Project snapshots before quit: requested runtime is not app-owned")
	}
	logical, err := routed.Run(ctx, "tmux", "show-options", "-gqv", runtimeMutationSocketNameOption)
	if err != nil || strings.TrimSpace(string(logical)) != socketName {
		return "", errors.New("save Project snapshots before quit: requested server has no matching logical route marker")
	}
	return path, nil
}

func boundedQuitSnapshotReason(reason string) string {
	reason = strings.Join(strings.Fields(reason), " ")
	const maxRunes = 180
	if utf8.RuneCountInString(reason) <= maxRunes {
		return reason
	}
	runes := []rune(reason)
	return string(runes[:maxRunes-1]) + "…"
}
