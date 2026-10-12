package runtime

import (
	"context"
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/services/mutations"

	"path/filepath"
	"slices"
	"strconv"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// deletedPaneMirrorPrefix is a transport tombstone used only while a
// self-target delete is being handed to tmux. A queued kill can fail after the
// Registry result is durable; keeping that live pane out of orphan import is
// safer than silently minting a new identity for a resource the operator just
// deleted.
type TmuxPaneDeleteRuntime struct {
	Runner                TmuxCommandRunner
	Target                TmuxTransport
	ExpectedSocketPath    string
	ExpectedLogicalSocket string
	RouteAuthority        *RuntimeMutationRouteAuthority
	Getenv                func(string) string
	RouteAnchor           string
	ProcessAlive          func(int) bool
	CurrentInvocationPane func(context.Context) (string, string, error)
}

func (r *TmuxPaneDeleteRuntime) UseExactTarget(target TmuxTransport) {
	if r == nil {
		return
	}
	BindExactDeleteTarget(target, &r.Target, &r.ExpectedSocketPath, &r.ExpectedLogicalSocket, &r.RouteAuthority)
}

func (r *TmuxPaneDeleteRuntime) UseRouteAnchor(paneID string) {
	r.RouteAnchor = ExactTmuxHandle(strings.TrimSpace(paneID), "%")
}

type LivePaneDeleteRow struct {
	SessionID   string
	SessionName string
	WindowID    string
	PaneID      string
	ProjectUID  string
	WindowUID   string
	PaneUID     string
}

func (r *TmuxPaneDeleteRuntime) Routed() ExplicitTmuxRunner {
	if r.ExpectedSocketPath != "" {
		return ExplicitTmuxRunner{Runner: r.Runner, Target: TmuxTransport{Kind: TmuxSocketPath, Value: r.ExpectedSocketPath, Source: TmuxSocketPathSource}}
	}
	return ExplicitTmuxRunner{Runner: r.Runner, Target: r.Target}
}

func (r *TmuxPaneDeleteRuntime) MutationAction(kind RuntimeMutationVerb, target PaneLiveDeleteTarget, guard, _ string, args ...string) PlannedRuntimeMutation {
	logicalRoute := r.Target.Flag() + "=" + r.Target.Value
	action := NewRuntimeMutation(1, kind, RuntimeMutationTarget{
		Socket: logicalRoute, PhysicalSocket: PrintableRuntimeMutationSocket(r.ExpectedSocketPath),
		RouteAuthority: r.RouteAuthority.Printable(),
		Kind:           "pane",
		ID:             target.PaneID,
		UID:            target.PaneUID,
		Parent:         target.SessionID + "/" + target.WindowID + "/" + target.RootUID,
	})
	BindRuntimeMutationGuard(&action, guard)
	action.Operands = slices.Clone(args)
	return action
}

func (r *TmuxPaneDeleteRuntime) GuardPrintedMutationRoute(ctx context.Context, action PlannedRuntimeMutation) error {
	return GuardPrintedRuntimeMutationRoute(ctx, r.Runner, RuntimeMutationRoute{
		Target: r.Target, ExpectedSocketPath: r.ExpectedSocketPath,
		SocketName: r.ExpectedLogicalSocket, Authority: r.RouteAuthority,
	}, action)
}

func (r *TmuxPaneDeleteRuntime) MutationSteps(
	targets []PaneLiveDeleteTarget,
	verb RuntimeMutationVerb,
	guardMirror func(PaneLiveDeleteTarget) string,
	effectMirror func(PaneLiveDeleteTarget) string,
	args func(PaneLiveDeleteTarget) []string,
	undo func(context.Context, PaneLiveDeleteTarget) error,
	applied *int,
) []RuntimeMutationStep {
	steps := make([]RuntimeMutationStep, 0, len(targets))
	for i, target := range targets {
		attempted := false
		guardUID := guardMirror(target)
		effectUID := effectMirror(target)
		guardDetail := "socket=" + r.Target.Flag() + "=" + r.Target.Value +
			";session=" + target.SessionID + "/" + target.SessionName +
			";window=" + target.WindowID + "/" + target.WindowUID +
			";pane=" + target.PaneID + "/" + guardUID +
			";root=" + string(target.RootKind) + "/" + target.RootUID
		action := r.MutationAction(verb, target, guardDetail, "", args(target)...)
		if verb == MutationQueuePaneKill {
			action.Target.UID = effectUID
			action.Queue = &RuntimeMutationQueuedKill{PhysicalSocket: r.ExpectedSocketPath, LogicalSocket: r.ExpectedLogicalSocket,
				RouteAuthority: action.Target.RouteAuthority,
				ExpectedUID:    effectUID, SessionID: target.SessionID, WindowID: target.WindowID}
			action.Queue.Marker = RuntimeMutationQueueMarker(action)
			BindRuntimeMutationGuard(&action, guardDetail)
		}
		action.Order = i + 1
		steps = append(steps, RuntimeMutationStep{
			Action: action,
			TargetRouteGuard: func(ctx context.Context) error {
				return r.GuardPrintedMutationRoute(ctx, action)
			},
			Reobserve: func(ctx context.Context) (bool, error) {
				if verb == MutationQueuePaneKill {
					return ObserveQueuedRuntimeMutationEffect(ctx,
						func(ctx context.Context) (bool, error) {
							return r.ObserveMutationEffect(ctx, target, effectUID, true, attempted)
						},
						func(ctx context.Context) (bool, error) {
							return ObserveRuntimeMutationQueueMarker(ctx, r.Runner, action)
						})
				}
				return r.ObserveMutationEffect(ctx, target, effectUID, verb == MutationKillPane, attempted)
			},
			Guard: func(ctx context.Context) error {
				if filepath.Clean(action.Target.PhysicalSocket) != filepath.Clean(r.ExpectedSocketPath) {
					return errors.New("delete pane: printable physical socket disagrees with bound execution route")
				}
				err := r.RevalidateMutationTarget(ctx, target, guardUID)
				if verb != MutationKillPane {
					return err
				}
				// Only kill-pane's goal is the exact Pane's absence; a
				// concurrent removal after Reobserve is converged, not drift.
				return RuntimeMutationAbsenceConverged(ctx, err, func(ctx context.Context) (bool, error) {
					return r.ObserveMutationEffect(ctx, target, effectUID, true, attempted)
				})
			},
			Apply: func(ctx context.Context) error {
				if _, err := RunRuntimeMutationCommand(ctx, r.Routed(), action); err != nil {
					return err
				}
				attempted = true
				if applied != nil {
					*applied++
				}
				return nil
			},
		})
		if verb == MutationQueuePaneKill {
			steps[len(steps)-1].Undo = func(ctx context.Context) error {
				return ClearRuntimeMutationQueueMarker(ctx, r.Runner, action)
			}
		}
		if undo != nil {
			steps[len(steps)-1].Undo = func(ctx context.Context) error { return undo(ctx, target) }
		}
	}
	return steps
}

func (r *TmuxPaneDeleteRuntime) ObserveMutationEffect(ctx context.Context, target PaneLiveDeleteTarget, wantMirror string, absent, allowNoServerAbsent bool) (bool, error) {
	if err := r.GuardSocketIdentity(ctx); err != nil {
		if absent && allowNoServerAbsent && inttmux.IsNoServerFailure(err) {
			return true, nil
		}
		return false, err
	}
	out, err := r.Routed().Run(ctx, "tmux", "list-panes", "-a", "-F", TmuxRowFormat(
		"#{session_id}", "#{window_id}", "#{pane_id}", "#{"+tmuxopts.PaneUID+"}"))
	if err != nil {
		return false, err
	}
	for _, row := range SplitTmuxRows(string(out), 4) {
		if row[2] != target.PaneID {
			continue
		}
		if absent {
			return false, nil
		}
		return row[0] == target.SessionID && row[1] == target.WindowID && row[3] == wantMirror, nil
	}
	return absent, nil
}

func (r *TmuxPaneDeleteRuntime) RevalidateMutationTarget(ctx context.Context, target PaneLiveDeleteTarget, wantMirror string) error {
	if err := r.GuardSocketIdentity(ctx); err != nil {
		return err
	}
	columns := []string{
		"#{session_id}", "#{session_name}", "#{window_id}", "#{pane_id}",
		"#{" + tmuxopts.ProjectUIDSession + "}", "#{" + tmuxopts.WindowUID + "}", "#{" + tmuxopts.PaneUID + "}",
	}
	if target.RequireDead {
		columns = []string{
			"#{session_id}", "#{session_name}", "#{window_id}", "#{pane_id}",
			"#{" + tmuxopts.ProjectUIDSession + "}", "#{" + tmuxopts.SessionRole + "}",
			"#{" + tmuxopts.WindowUID + "}", "#{" + tmuxopts.PaneUID + "}", "#{pane_dead}",
			"#{" + tmuxopts.PaneOwnerKind + "}", "#{" + tmuxopts.PaneOwnerUID + "}",
			"#{" + tmuxopts.AgentUIDPane + "}", "#{" + tmuxopts.PaneRole + "}",
		}
		if target.SupervisorPID > 0 {
			columns = append(columns, "#{pane_pid}")
		}
	}
	format := TmuxRowFormat(columns...)
	out, err := r.Routed().Run(ctx, "tmux", "display-message", "-p", "-t", target.PaneID, "-F", format)
	if err != nil {
		return fmt.Errorf("revalidate exact live Pane %s on %s %s: %w", target.PaneID, r.Target.Flag(), r.Target.Value, err)
	}
	rows := SplitTmuxRows(strings.ReplaceAll(string(out), TmuxRowSepFormat, TmuxRowSep), len(columns))
	if len(rows) != 1 {
		return fmt.Errorf("exact live Pane %s returned malformed containment evidence", target.PaneID)
	}
	row := rows[0]
	windowUIDIndex, paneUIDIndex := 5, 6
	if target.RequireDead {
		windowUIDIndex, paneUIDIndex = 6, 7
	}
	if row[0] != target.SessionID || row[1] != target.SessionName || row[2] != target.WindowID ||
		row[3] != target.PaneID || row[windowUIDIndex] != target.WindowUID || row[paneUIDIndex] != wantMirror {
		return fmt.Errorf("exact live Pane %s drifted: got session=%s/%q window=%s/%q pane=%s uid=%q, want session=%s/%q window=%s/%q pane=%s uid=%q",
			target.PaneID, row[0], row[1], row[2], row[windowUIDIndex], row[3], row[paneUIDIndex],
			target.SessionID, target.SessionName, target.WindowID, target.WindowUID, target.PaneID, wantMirror)
	}
	if target.RequireDead && row[8] != "1" {
		return fmt.Errorf("exact lifecycle Pane %s is no longer retained dead", target.PaneID)
	}
	if target.RequireDead && (row[9] != string(target.OwnerKind) || row[10] != target.OwnerUID ||
		row[11] != target.AgentUID || row[12] != string(target.PaneRole)) {
		return fmt.Errorf("exact lifecycle Pane %s stable owner mirror drifted", target.PaneID)
	}
	if target.RequireDead && target.SupervisorPID > 0 {
		pid, parseErr := strconv.Atoi(strings.TrimSpace(row[13]))
		if parseErr != nil || pid != target.SupervisorPID {
			return fmt.Errorf("exact lifecycle Pane %s original supervisor PID drifted", target.PaneID)
		}
		alive := r.ProcessAlive
		if alive == nil || alive(pid) {
			return fmt.Errorf("exact lifecycle Pane %s original supervisor PID %d is active or unobservable", target.PaneID, pid)
		}
	}
	if target.RequireDead {
		switch target.RootKind {
		case coremetadata.KindProject:
			if row[4] != target.RootUID || strings.TrimSpace(row[5]) != "" {
				return fmt.Errorf("exact live Pane %s Project root identity drifted", target.PaneID)
			}
		case coremetadata.KindControlSession:
			if strings.TrimSpace(row[4]) != "" || row[5] != resourcegraph.ControlSessionRole {
				return fmt.Errorf("exact live Pane %s ControlSession root identity drifted", target.PaneID)
			}
		}
	}
	root := DeleteRootOwner{Kind: target.RootKind, UID: target.RootUID, Session: target.SessionName}
	if err := root.ValidateLiveSession("delete pane", "Pane", target.PaneID, target.PaneUID, row[4], row[1]); err != nil {
		return err
	}
	return nil
}

func (r *TmuxPaneDeleteRuntime) Inventory(ctx context.Context) ([]LivePaneDeleteRow, error) {
	if r == nil || r.Runner == nil {
		return nil, errors.New("delete pane: tmux runtime is not configured")
	}
	if r.Target.Flag() == "" || r.Target.Value == "" {
		return nil, errors.New("delete pane: no exact tmux target is bound")
	}
	if err := r.ObserveSocketIdentity(ctx); err != nil {
		return nil, err
	}
	format := TmuxRowFormat(
		"#{session_id}", "#{session_name}", "#{window_id}", "#{pane_id}",
		"#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.WindowUID+"}", "#{"+tmuxopts.PaneUID+"}",
	)
	out, err := r.Routed().Run(ctx, "tmux", "list-panes", "-a", "-F", format)
	if err != nil {
		return nil, fmt.Errorf("delete pane: inventory exact tmux socket: %w", err)
	}
	out = []byte(strings.ReplaceAll(string(out), TmuxRowSepFormat, TmuxRowSep))
	var rows []LivePaneDeleteRow
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, TmuxRowSep)
		if len(fields) != 7 {
			return nil, fmt.Errorf("delete pane: malformed exact tmux inventory row %q", line)
		}
		row := LivePaneDeleteRow{
			SessionID: strings.TrimSpace(fields[0]), SessionName: strings.TrimSpace(fields[1]),
			WindowID: strings.TrimSpace(fields[2]), PaneID: strings.TrimSpace(fields[3]),
			ProjectUID: strings.TrimSpace(fields[4]), WindowUID: strings.TrimSpace(fields[5]),
			PaneUID: strings.TrimSpace(fields[6]),
		}
		if ExactTmuxHandle(row.SessionID, "$") == "" || ExactTmuxHandle(row.WindowID, "@") == "" || ExactTmuxHandle(row.PaneID, "%") == "" {
			return nil, fmt.Errorf("delete pane: malformed exact tmux handles session=%q window=%q pane=%q",
				row.SessionID, row.WindowID, row.PaneID)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (r *TmuxPaneDeleteRuntime) ExactSocketPath(ctx context.Context) (string, error) {
	if err := r.ObserveSocketIdentity(ctx); err != nil {
		if inttmux.IsNoServerFailure(err) {
			return "", fmt.Errorf("delete pane: exact tmux socket is unavailable (no-server); absence is not Registry deletion authority and nothing was changed: %w", err)
		}
		return "", fmt.Errorf("delete pane: exact tmux socket observation failed: %w", err)
	}
	if strings.TrimSpace(r.ExpectedSocketPath) == "" {
		return "", errors.New("delete pane: exact tmux socket identity is empty; absence is not Registry deletion authority and nothing was changed")
	}
	return r.ExpectedSocketPath, nil
}

// observeSocketIdentity binds every read in this invocation to one immutable
// physical socket without claiming mutation authority. Registry-only cleanup
// of an exact durable Offline/Failed/MissingRuntime target is intentionally
// available on a standalone tmux server, but it still requires a positive
// inventory from this same physical socket and a second byte-identical
// preflight under lock.
func (r *TmuxPaneDeleteRuntime) ObserveSocketIdentity(ctx context.Context) error {
	out, err := r.Routed().Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		// Preserve the typed no-server carrier for the expected-absence effect
		// observer. The command boundary flattens the eventual diagnostic; this
		// internal guard must not erase evidence that the last Pane ended the
		// server before post-effect reobservation.
		return fmt.Errorf("delete pane: reobserve exact socket identity: %w", err)
	}
	observed := strings.TrimSpace(string(out))
	if observed == "" {
		return errors.New("delete pane: exact socket identity is empty")
	}
	if r.Target.Flag() == "-S" && observed != r.Target.Value {
		return fmt.Errorf("delete pane: exact socket drifted: observed %q, planned %q", observed, r.Target.Value)
	}
	if r.ExpectedSocketPath == "" {
		r.ExpectedSocketPath = observed
	}
	if observed != r.ExpectedSocketPath {
		return fmt.Errorf("delete pane: exact socket drifted: observed %q, planned %q", observed, r.ExpectedSocketPath)
	}
	return nil
}

// guardSocketIdentity upgrades the immutable observation route to live tmux
// mutation authority. Every kill, tombstone, restore, and deferred queue step
// calls this immediately before its write; a standalone/foreign server can
// therefore authorize only the zero-tmux-write Registry cleanup above.
func (r *TmuxPaneDeleteRuntime) GuardSocketIdentity(ctx context.Context) error {
	if err := r.ObserveSocketIdentity(ctx); err != nil {
		return err
	}
	if r.RouteAuthority == nil {
		route, err := ResolveExistingRuntimeMutationRouteWithAnchor(ctx, r.Runner, r.Target, r.Getenv, r.RouteAnchor)
		if err != nil {
			return fmt.Errorf("delete pane: %w", err)
		}
		if route.ExpectedSocketPath != r.ExpectedSocketPath || route.Authority == nil {
			return errors.New("delete pane: invocation server-generation authority drifted")
		}
		r.Target = route.Target
		r.RouteAuthority = route.Authority
		r.ExpectedLogicalSocket = route.SocketName
		if route.Authority.Class == RuntimeMutationRouteStandalone {
			r.ExpectedLogicalSocket = ""
		}
	}
	if r.RouteAuthority != nil {
		return GuardResolvedRuntimeMutationRoute(ctx, r.Runner, RuntimeMutationRoute{
			Target: r.Target, ExpectedSocketPath: r.ExpectedSocketPath,
			SocketName: r.ExpectedLogicalSocket, Authority: r.RouteAuthority,
		})
	}
	if err := GuardRuntimeMutationServerOwnership(ctx, r.Routed(), r.Target); err != nil {
		return fmt.Errorf("delete pane: %w", err)
	}
	logical, err := r.Routed().Run(ctx, "tmux", "show-options", "-gqv", RuntimeMutationSocketNameOption)
	if err != nil || strings.TrimSpace(string(logical)) == "" {
		return errors.New("delete pane: exact app logical socket marker is unavailable")
	}
	if r.ExpectedLogicalSocket == "" {
		r.ExpectedLogicalSocket = strings.TrimSpace(string(logical))
	}
	if strings.TrimSpace(string(logical)) != r.ExpectedLogicalSocket {
		return errors.New("delete pane: app logical socket marker drifted")
	}
	return nil
}

type PlannedPaneDelete struct {
	ResourceUID string
	PaneUID     string
}

func PlannedPaneDeletes(plan DeletePlan) []PlannedPaneDelete {
	var out []PlannedPaneDelete
	for _, target := range plan.Targets {
		switch plan.Kind {
		case coremetadata.KindPane:
			out = append(out, PlannedPaneDelete{ResourceUID: target.Match.UID, PaneUID: target.Match.UID})
		case coremetadata.KindAgent:
			for _, descendant := range target.Descendants {
				if descendant.Kind == coremetadata.KindPane {
					out = append(out, PlannedPaneDelete{ResourceUID: target.Match.UID, PaneUID: descendant.UID})
				}
			}
		}
	}
	return out
}

func PaneRegistryAncestry(registry coremetadata.Registry, paneUID string) (coremetadata.Pane, coremetadata.Window, DeleteRootOwner, error) {
	pane, ok := registry.Pane(paneUID)
	if !ok {
		return coremetadata.Pane{}, coremetadata.Window{}, DeleteRootOwner{},
			fmt.Errorf("registry Pane uid %q disappeared during live preflight", paneUID)
	}
	windowUID := pane.Metadata.OwnerUID()
	if pane.Metadata.OwnerRef != nil && pane.Metadata.OwnerRef.Kind == coremetadata.KindAgent {
		agent, ok := registry.Agent(windowUID)
		if !ok {
			return coremetadata.Pane{}, coremetadata.Window{}, DeleteRootOwner{},
				fmt.Errorf("registry Pane uid %q has no owning Agent %q", paneUID, windowUID)
		}
		windowUID = agent.Metadata.OwnerUID()
	}
	window, ok := registry.Window(windowUID)
	if !ok {
		return coremetadata.Pane{}, coremetadata.Window{}, DeleteRootOwner{},
			fmt.Errorf("registry Pane uid %q has no owning Window %q", paneUID, windowUID)
	}
	root, err := DeleteRootForWindow(registry, *window)
	if err != nil {
		return coremetadata.Pane{}, coremetadata.Window{}, DeleteRootOwner{}, err
	}
	return *pane, *window, root, nil
}

func PaneHasMissingRuntime(pane coremetadata.Pane) bool {
	condition, ok := pane.HasCondition(coremetadata.ConditionMissingRuntime)
	return ok && condition.Status == coremetadata.ConditionTrue && condition.Reason == coremetadata.ReasonRuntimeUnbound
}

// paneDeleteAuthoritySignature signs the exact Registry facts that make a Pane
// or Agent delete safe. buildDeletePlan already signs the uid cascade; this
// signs the parts that can change without changing that set: owner chain,
// Offline/Failed/MissingRuntime evidence, current binding, and activation
// generation.
func PaneDeleteAuthoritySignature(registry coremetadata.Registry, plan DeletePlan) (string, error) {
	var b strings.Builder
	writeRoot := func(window coremetadata.Window) error {
		root, err := DeleteRootForWindow(registry, window)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "window=%q,window-owner=%q,root-kind=%q,root=%q,session=%q;",
			window.Metadata.UID, window.Metadata.OwnerUID(), root.Kind, root.UID, root.Session)
		return nil
	}
	writePane := func(paneUID string) error {
		pane, window, _, err := PaneRegistryAncestry(registry, paneUID)
		if err != nil {
			return err
		}
		ownerKind := coremetadata.Kind("")
		ownerUID := ""
		if pane.Metadata.OwnerRef != nil {
			ownerKind = pane.Metadata.OwnerRef.Kind
			ownerUID = pane.Metadata.OwnerRef.UID
		}
		fmt.Fprintf(&b, "pane=%q,pane-owner-kind=%q,pane-owner=%q,generation=%q,runtime=%q,missing=%t;",
			pane.Metadata.UID, ownerKind, ownerUID, pane.Status.Activation.Generation,
			pane.Status.Activation.RuntimeID, PaneHasMissingRuntime(pane))
		return writeRoot(window)
	}
	for _, target := range plan.Targets {
		fmt.Fprintf(&b, "target-kind=%q,target=%q;", plan.Kind, target.Match.UID)
		switch plan.Kind {
		case coremetadata.KindPane:
			if err := writePane(target.Match.UID); err != nil {
				return "", err
			}
		case coremetadata.KindAgent:
			agent, ok := registry.Agent(target.Match.UID)
			if !ok {
				return "", fmt.Errorf("registry Agent uid %q disappeared during live preflight", target.Match.UID)
			}
			ownerKind := coremetadata.Kind("")
			ownerUID := ""
			if agent.Metadata.OwnerRef != nil {
				ownerKind = agent.Metadata.OwnerRef.Kind
				ownerUID = agent.Metadata.OwnerRef.UID
			}
			fmt.Fprintf(&b, "agent=%q,agent-owner-kind=%q,agent-owner=%q,phase=%q,pane-ref=%q;",
				agent.Metadata.UID, ownerKind, ownerUID, agent.Status.Phase,
				agent.Status.PaneRef)
			window, ok := registry.Window(ownerUID)
			if !ok || ownerKind != coremetadata.KindWindow {
				return "", fmt.Errorf("registry Agent uid %q has no exact owning Window %q", target.Match.UID, ownerUID)
			}
			if err := writeRoot(*window); err != nil {
				return "", err
			}
			for _, descendant := range target.Descendants {
				if descendant.Kind == coremetadata.KindPane {
					if err := writePane(descendant.UID); err != nil {
						return "", err
					}
				}
			}
		}
	}
	return b.String(), nil
}

func RegistryOnlyPaneTarget(registry coremetadata.Registry, plan DeletePlan, target mutations.DeleteTarget) (PaneRegistryOnlyDeleteTarget, bool, error) {
	if !plan.ExactUID {
		return PaneRegistryOnlyDeleteTarget{}, false, nil
	}
	switch plan.Kind {
	case coremetadata.KindPane:
		pane, window, root, err := PaneRegistryAncestry(registry, target.Match.UID)
		if err != nil {
			return PaneRegistryOnlyDeleteTarget{}, false, err
		}
		if !PaneHasMissingRuntime(pane) {
			return PaneRegistryOnlyDeleteTarget{}, false, nil
		}
		return PaneRegistryOnlyDeleteTarget{ResourceUID: target.Match.UID, Kind: "Pane", Evidence: coremetadata.ConditionMissingRuntime,
			WindowUID: window.Metadata.UID, RootKind: root.Kind, RootUID: root.UID}, true, nil
	case coremetadata.KindAgent:
		agent, ok := registry.Agent(target.Match.UID)
		if !ok {
			return PaneRegistryOnlyDeleteTarget{}, false, fmt.Errorf("registry Agent uid %q disappeared during live preflight", target.Match.UID)
		}
		// Offline and Failed are equally absent: neither has a live managed Pane
		// to authorize a kill, so the same exact-uid evidence authorizes both.
		if (agent.Status.Phase != coremetadata.PhaseOffline && agent.Status.Phase != coremetadata.PhaseFailed) ||
			strings.TrimSpace(agent.Status.PaneRef) != "" {
			return PaneRegistryOnlyDeleteTarget{}, false, nil
		}
		window, ok := registry.Window(agent.Metadata.OwnerUID())
		if !ok || agent.Metadata.OwnerRef == nil || agent.Metadata.OwnerRef.Kind != coremetadata.KindWindow {
			return PaneRegistryOnlyDeleteTarget{}, false, fmt.Errorf("registry Agent uid %q has no exact owning Window %q", target.Match.UID, agent.Metadata.OwnerUID())
		}
		root, err := DeleteRootForWindow(registry, *window)
		if err != nil {
			return PaneRegistryOnlyDeleteTarget{}, false, err
		}
		for _, descendant := range target.Descendants {
			if descendant.Kind != coremetadata.KindPane {
				continue
			}
			pane, ok := registry.Pane(descendant.UID)
			if !ok || !PaneHasMissingRuntime(*pane) {
				return PaneRegistryOnlyDeleteTarget{}, false, nil
			}
		}
		evidence := string(agent.Status.Phase)
		if len(target.Descendants) > 0 {
			evidence += "+" + coremetadata.ConditionMissingRuntime
		}
		return PaneRegistryOnlyDeleteTarget{ResourceUID: target.Match.UID, Kind: "Agent", Evidence: evidence,
			WindowUID: window.Metadata.UID, RootKind: root.Kind, RootUID: root.UID}, true, nil
	default:
		return PaneRegistryOnlyDeleteTarget{}, false, fmt.Errorf("delete pane runtime: unsupported Registry-only kind %q", plan.Kind)
	}
}

// registryOnlyUIDSelectorHint names the runnable exact-uid form for a target the
// Registry-only path refused only because of how it was addressed.
//
// A name, scope, label, or active-target plan never takes that path, whatever
// evidence the target carries, so the live-mirror refusal it reaches is correct
// and, on its own, a dead end: nothing in it says the `uid:` spelling of the
// same target would succeed. The answer is the same eligibility check the
// preflight runs, asked again as if every target had been an exact uid. A plan
// that already was exact, or a target that check would still refuse, gets "":
// pointing an operator at a spelling that fails the same way is worse than no
// pointer.
func (r *TmuxPaneDeleteRuntime) RegistryOnlyUIDSelectorHint(registry coremetadata.Registry, plan DeletePlan, resourceUID string) string {
	if plan.ExactUID {
		return ""
	}
	exact := plan
	exact.ExactUID = true
	for _, target := range plan.Targets {
		if target.Match.UID != resourceUID {
			continue
		}
		eligible, ok, err := RegistryOnlyPaneTarget(registry, exact, target)
		if err != nil || !ok {
			return ""
		}
		command := "projmux delete " + strings.ToLower(string(plan.Kind)) + " uid:" + resourceUID + DeleteSocketFlagSpelling(r.Target)
		return fmt.Sprintf("; %s uid %q carries %s evidence, which authorizes Registry-only deletion only under the exact uid: selector: run `%s --dry-run`, then `%s --yes`",
			eligible.Kind, resourceUID, eligible.Evidence, command, command)
	}
	return ""
}

// deleteSocketFlagSpelling renders the delete route's own flag for the server
// this invocation addressed, so a named retry reaches the same server from
// inside or outside tmux.
func DeleteSocketFlagSpelling(target TmuxTransport) string {
	switch {
	case !target.Present():
		return ""
	case target.Kind == TmuxSocketName:
		return " --socket " + target.Value
	case target.Kind == TmuxSocketPath:
		return " --socket-path " + target.Value
	default:
		return ""
	}
}

func (r *TmuxPaneDeleteRuntime) Preflight(ctx context.Context, registry coremetadata.Registry, plan DeletePlan) (PaneLiveDeletePlan, error) {
	socketPath, err := r.ExactSocketPath(ctx)
	if err != nil {
		return PaneLiveDeletePlan{}, err
	}
	standaloneEligible := plan.ExactUID && len(plan.Targets) > 0
	for _, target := range plan.Targets {
		_, eligible, targetErr := RegistryOnlyPaneTarget(registry, plan, target)
		if targetErr != nil {
			return PaneLiveDeletePlan{}, fmt.Errorf("delete %s: %w", strings.ToLower(string(plan.Kind)), targetErr)
		}
		standaloneEligible = standaloneEligible && eligible
	}
	if !standaloneEligible {
		if err := r.GuardSocketIdentity(ctx); err != nil {
			return PaneLiveDeletePlan{}, err
		}
	}
	rows, err := r.Inventory(ctx)
	if err != nil {
		return PaneLiveDeletePlan{}, err
	}
	if len(rows) == 0 {
		return PaneLiveDeletePlan{}, fmt.Errorf("delete %s: exact tmux inventory on %s was empty; absence is not Registry deletion authority and nothing was changed",
			strings.ToLower(string(plan.Kind)), r.Target.Label())
	}
	authority, err := PaneDeleteAuthoritySignature(registry, plan)
	if err != nil {
		return PaneLiveDeletePlan{}, fmt.Errorf("delete %s: %w", strings.ToLower(string(plan.Kind)), err)
	}
	planned := PlannedPaneDeletes(plan)
	targetUIDs := make(map[string]bool, len(planned))
	for _, target := range planned {
		targetUIDs[target.PaneUID] = true
	}
	paneCount := map[string]int{}
	windowCount := map[string]int{}
	removedCount := map[string]int{}
	byUID := map[string][]LivePaneDeleteRow{}
	seenWindow := map[string]bool{}
	windowIDsByUID := map[string]map[string]bool{}
	for _, row := range rows {
		paneCount[row.WindowID]++
		if !seenWindow[row.WindowID] {
			windowCount[row.SessionID]++
			seenWindow[row.WindowID] = true
		}
		if row.WindowUID != "" {
			if windowIDsByUID[row.WindowUID] == nil {
				windowIDsByUID[row.WindowUID] = map[string]bool{}
			}
			windowIDsByUID[row.WindowUID][row.WindowID] = true
		}
		if targetUIDs[row.PaneUID] {
			removedCount[row.WindowID]++
			byUID[row.PaneUID] = append(byUID[row.PaneUID], row)
		}
	}

	currentPaneID, currentSocket := "", ""
	if r.CurrentInvocationPane != nil {
		currentPaneID, currentSocket, err = r.CurrentInvocationPane(ctx)
	}
	if err != nil {
		return PaneLiveDeletePlan{}, err
	}
	if plan.Implicit && currentSocket == "" {
		return PaneLiveDeletePlan{}, errors.New("delete pane: implicit active target is not attached to the exact projmux tmux socket")
	}

	live := PaneLiveDeletePlan{SocketPath: socketPath, Authority: authority}
	registryOnlyByResource := map[string]bool{}
	for _, target := range plan.Targets {
		registryOnly, ok, targetErr := RegistryOnlyPaneTarget(registry, plan, target)
		if targetErr != nil {
			return PaneLiveDeletePlan{}, fmt.Errorf("delete %s: %w", strings.ToLower(string(plan.Kind)), targetErr)
		}
		if ok {
			// Lifecycle evidence makes a zero-mirror target eligible; it does not
			// override a positive live mirror. A target already live takes the
			// normal exact-kill path. A zero-to-live change on the locked pass
			// changes the signed plan and is refused before mutation.
			hasLiveMirror := false
			for _, pane := range planned {
				if pane.ResourceUID == target.Match.UID && len(byUID[pane.PaneUID]) > 0 {
					hasLiveMirror = true
					break
				}
			}
			if hasLiveMirror {
				continue
			}
			registryOnlyByResource[target.Match.UID] = true
			live.RegistryOnly = append(live.RegistryOnly, registryOnly)
		}
	}
	if plan.Kind == coremetadata.KindAgent {
		for _, target := range plan.Targets {
			if len(target.Descendants) == 0 && !registryOnlyByResource[target.Match.UID] {
				agent, _ := registry.Agent(target.Match.UID)
				return PaneLiveDeletePlan{}, fmt.Errorf("delete agent: registry Agent uid %q is %s, not an exact Offline or Failed target; no live managed Pane can authorize deletion and nothing was changed%s",
					target.Match.UID, agent.Status.Phase, r.RegistryOnlyUIDSelectorHint(registry, plan, target.Match.UID))
			}
		}
	}
	for _, target := range planned {
		_, window, root, ancestryErr := PaneRegistryAncestry(registry, target.PaneUID)
		if ancestryErr != nil {
			return PaneLiveDeletePlan{}, fmt.Errorf("delete %s: %w", strings.ToLower(string(plan.Kind)), ancestryErr)
		}
		matches := byUID[target.PaneUID]
		if len(matches) == 0 {
			if registryOnlyByResource[target.ResourceUID] {
				continue
			}
			return PaneLiveDeletePlan{}, fmt.Errorf("delete %s: registry Pane uid %q has no exact live tmux Pane mirror on -L %s; nothing was changed%s",
				strings.ToLower(string(plan.Kind)), target.PaneUID, r.Target.Value,
				r.RegistryOnlyUIDSelectorHint(registry, plan, target.ResourceUID))
		}
		if len(matches) != 1 {
			return PaneLiveDeletePlan{}, fmt.Errorf("delete %s: registry Pane uid %q has %d live tmux Pane mirrors on -L %s; exact target is ambiguous and nothing was changed",
				strings.ToLower(string(plan.Kind)), target.PaneUID, len(matches), r.Target.Value)
		}
		row := matches[0]
		if row.WindowUID != window.Metadata.UID {
			observed := row.WindowUID
			if observed == "" {
				observed = "<missing>"
			}
			return PaneLiveDeletePlan{}, fmt.Errorf("delete %s: live tmux Pane %s mirrors registry uid %q under foreign Window uid %q, want %q; nothing was changed",
				strings.ToLower(string(plan.Kind)), row.PaneID, target.PaneUID, observed, window.Metadata.UID)
		}
		if mirrors := len(windowIDsByUID[window.Metadata.UID]); mirrors != 1 {
			return PaneLiveDeletePlan{}, fmt.Errorf("delete %s: registry Window uid %q has %d live tmux Window mirrors on -L %s while resolving Pane uid %q; exact owner is ambiguous and nothing was changed",
				strings.ToLower(string(plan.Kind)), window.Metadata.UID, mirrors, r.Target.Value, target.PaneUID)
		}
		spelling := "delete " + strings.ToLower(string(plan.Kind))
		if err := root.ValidateLiveSession(spelling, "Pane", row.PaneID, target.PaneUID, row.ProjectUID, row.SessionName); err != nil {
			return PaneLiveDeletePlan{}, err
		}
		if plan.Implicit && row.PaneID != currentPaneID {
			return PaneLiveDeletePlan{}, fmt.Errorf("delete %s: implicit active registry uid %q mirrors live Pane %s but the exact caller is in %s; nothing was changed",
				strings.ToLower(string(plan.Kind)), target.PaneUID, row.PaneID, currentPaneID)
		}
		live.Targets = append(live.Targets, PaneLiveDeleteTarget{
			ResourceUID: target.ResourceUID, PaneUID: target.PaneUID, PaneID: row.PaneID,
			WindowUID: window.Metadata.UID, WindowID: row.WindowID,
			SessionID: row.SessionID, SessionName: row.SessionName, RootKind: root.Kind, RootUID: root.UID,
			Self: row.PaneID == currentPaneID && currentSocket != "",
		})
	}

	endingWindows := map[string]bool{}
	markedWindow := map[string]bool{}
	for i := len(live.Targets) - 1; i >= 0; i-- {
		target := &live.Targets[i]
		if !markedWindow[target.WindowID] && removedCount[target.WindowID] == paneCount[target.WindowID] {
			target.EndsWindow = true
			endingWindows[target.WindowID] = true
			markedWindow[target.WindowID] = true
		}
	}
	endingWindowCount := map[string]int{}
	for _, target := range live.Targets {
		if target.EndsWindow {
			endingWindowCount[target.SessionID]++
		}
	}
	markedSession := map[string]bool{}
	for i := len(live.Targets) - 1; i >= 0; i-- {
		target := &live.Targets[i]
		if target.EndsWindow && !markedSession[target.SessionID] && endingWindowCount[target.SessionID] == windowCount[target.SessionID] {
			target.EndsSession = true
			markedSession[target.SessionID] = true
		}
	}
	if len(live.Targets) > 0 {
		if err := r.GuardSocketIdentity(ctx); err != nil {
			return PaneLiveDeletePlan{}, err
		}
	}
	return live, nil
}

func (r *TmuxPaneDeleteRuntime) ReplacementMaterializer() *MaterializeRuntime {
	routed := r.Routed()
	return &MaterializeRuntime{Runner: routed, Sessions: inttmux.NewClient(routed), Target: r.Target, ExpectedSocketPath: r.ExpectedSocketPath, SocketName: r.ExpectedLogicalSocket, RouteAuthority: r.RouteAuthority}
}

// prepareReplacements creates and fully mirrors every last-descendant shell
// before any target Pane is killed. A partial create is ownership-ledgered and
// rolled back here, so callers either receive a complete receipt or the exact
// live/Registry preimage remains.
func (r *TmuxPaneDeleteRuntime) PrepareReplacements(ctx context.Context, replacements []PaneReplacementShell) (PaneReplacementReceipt, error) {
	var receipt PaneReplacementReceipt
	if len(replacements) == 0 {
		return receipt, nil
	}
	if err := r.GuardSocketIdentity(ctx); err != nil {
		return receipt, err
	}
	runtime := r.ReplacementMaterializer()
	ledger := NewRuntimeLedger("delete-pane-replacement")
	fail := func(cause error) (PaneReplacementReceipt, error) {
		runtime.Rollback(ctx, ledger)
		runtime.ClearCreateOperations(ctx, ledger)
		return PaneReplacementReceipt{}, cause
	}
	markedSessions := map[string]bool{}
	for _, replacement := range replacements {
		if markedSessions[replacement.Anchor.SessionID] {
			continue
		}
		if err := runtime.MarkCreateOperation(ctx, replacement.Anchor.SessionID, ledger); err != nil {
			return fail(err)
		}
		markedSessions[replacement.Anchor.SessionID] = true
	}
	for _, replacement := range replacements {
		paneID, err := runtime.SplitPane(ctx, replacement.Anchor.PaneID, "right",
			replacement.Pane.Spec.CWD, nil, func(string) error { return nil })
		if paneID != "" {
			if claimErr := AdoptCreatedPane(ctx, runtime, paneID, replacement.Anchor.SessionID,
				replacement.Anchor.WindowID, replacement.Pane, ledger); claimErr != nil {
				return fail(errors.Join(err, claimErr))
			}
			if mirrorErr := runtime.RunIdentityWrites(ctx, "pane", paneID, replacement.Pane.Metadata.UID, []IdentityPlanWrite{
				{Operands: []string{"-p", "-t", paneID, "-q", tmuxopts.PaneOwnerKind, string(coremetadata.KindWindow)}, Effect: "replacement Pane owner kind equals Window"},
				{Operands: []string{"-p", "-t", paneID, "-q", tmuxopts.PaneOwnerUID, replacement.Anchor.WindowUID}, Effect: "replacement Pane owner uid equals Window"},
				{Operands: []string{"-p", "-t", paneID, "-q", tmuxopts.PaneRole, string(coremetadata.PaneRoleShell)}, Effect: "replacement Pane role equals shell"},
			}); mirrorErr != nil {
				return fail(errors.Join(err, mirrorErr))
			}
		}
		if err != nil {
			return fail(err)
		}
	}
	receipt.Created = slices.Clone(ledger.Created)
	runtime.ClearCreateOperations(ctx, ledger)
	return receipt, nil
}

func (r *TmuxPaneDeleteRuntime) RollbackReplacements(ctx context.Context, receipt PaneReplacementReceipt) error {
	if len(receipt.Created) == 0 {
		return nil
	}
	runtime := r.ReplacementMaterializer()
	ledger := &RuntimeLedger{Created: slices.Clone(receipt.Created)}
	runtime.Rollback(ctx, ledger)
	for _, created := range receipt.Created {
		if got := runtime.Option(ctx, created.ID, "#{"+created.OwnershipOption()+"}"); got == created.UID {
			return fmt.Errorf("delete pane: replacement rollback left owned %s %s uid=%s", created.Kind, created.ID, created.UID)
		}
	}
	return nil
}

func (r *TmuxPaneDeleteRuntime) Kill(ctx context.Context, target PaneLiveDeleteTarget) error {
	_, err := r.KillAll(ctx, []PaneLiveDeleteTarget{target})
	if err != nil {
		return fmt.Errorf("delete pane: kill exact live Pane %s in Window %s session %s (%s): %w",
			target.PaneID, target.WindowID, target.SessionName, target.SessionID, err)
	}
	return nil
}

// killAll guards the complete exact target set before killing the first Pane.
// The returned count lets the Registry transaction preserve evidence for the
// exact prefix that was already removed if tmux fails part-way through.
func (r *TmuxPaneDeleteRuntime) KillAll(ctx context.Context, targets []PaneLiveDeleteTarget) (int, error) {
	applied := 0
	steps := r.MutationSteps(targets, MutationKillPane,
		func(target PaneLiveDeleteTarget) string { return target.PaneUID },
		func(target PaneLiveDeleteTarget) string { return target.PaneUID },
		func(target PaneLiveDeleteTarget) []string { return []string{"-t", target.PaneID} },
		nil, &applied)
	if err := ExecuteRuntimeMutationPlan(ctx, steps); err != nil {
		return applied, err
	}
	return applied, nil
}

func BindExactDeleteTarget(target TmuxTransport, bound *TmuxTransport, expectedSocketPath, expectedLogicalSocket *string, routeAuthority **RuntimeMutationRouteAuthority) {
	*bound = target
	*expectedSocketPath = ""
	*expectedLogicalSocket = ""
	*routeAuthority = nil
}
