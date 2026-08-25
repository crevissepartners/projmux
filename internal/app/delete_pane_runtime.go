package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// deletedPaneMirrorPrefix is a transport tombstone used only while a
// self-target delete is being handed to tmux. A queued kill can fail after the
// Registry result is durable; keeping that live pane out of orphan import is
// safer than silently minting a new identity for a resource the operator just
// deleted.
const deletedPaneMirrorPrefix = coremetadata.DeletedPaneMirrorPrefix

type paneDeleteRuntime interface {
	// useExactTarget pins every read and write of this seam to one resolved
	// server. It is called before the first inventory of an invocation.
	useExactTarget(explicitTmuxTarget)
	preflight(context.Context, coremetadata.Registry, deletePlan) (paneLiveDeletePlan, error)
	prepareReplacements(context.Context, []paneReplacementShell) (paneReplacementReceipt, error)
	rollbackReplacements(context.Context, paneReplacementReceipt) error
	kill(context.Context, paneLiveDeleteTarget) error
	tombstoneSelfKill(context.Context, []paneLiveDeleteTarget) error
	restoreSelfKill(context.Context, []paneLiveDeleteTarget) error
	queueSelfKill(context.Context, []paneLiveDeleteTarget) error
}

// paneReplacementShell binds one desired replacement row to the exact live
// Pane that still anchors its Window immediately before deletion.
type paneReplacementShell struct {
	Pane   coremetadata.Pane
	Anchor paneLiveDeleteTarget
}

// paneReplacementReceipt contains only runtime objects this delete created.
// Rollback is therefore exact and never targets a pre-existing sibling.
type paneReplacementReceipt struct {
	created []runtimeObject
}

type paneLiveDeleteTarget struct {
	ResourceUID string
	PaneUID     string
	PaneID      string
	WindowUID   string
	WindowID    string
	SessionID   string
	SessionName string
	RootKind    coremetadata.Kind
	RootUID     string
	EndsWindow  bool
	EndsSession bool
	Self        bool
}

type paneLiveDeletePlan struct {
	Targets      []paneLiveDeleteTarget
	RegistryOnly []paneRegistryOnlyDeleteTarget
	SocketPath   string
	// Authority is the exact Registry owner/generation/lifecycle signature
	// observed together with SocketPath. Locked revalidation must reproduce it
	// before either a live kill or a Registry-only commit is allowed.
	Authority string
}

type paneRegistryOnlyDeleteTarget struct {
	ResourceUID string
	Kind        string
	Evidence    string
	WindowUID   string
	RootKind    coremetadata.Kind
	RootUID     string
}

func (p paneLiveDeletePlan) signature() string {
	var b strings.Builder
	fmt.Fprintf(&b, "socket=%q;authority=%q;", p.SocketPath, p.Authority)
	for _, target := range p.Targets {
		fmt.Fprintf(&b, "%s,%s,%s,%s,%s,%s,%s,%s,%s,%t,%t,%t;", target.ResourceUID,
			target.PaneUID, target.PaneID, target.WindowUID, target.WindowID,
			target.SessionID, target.SessionName, target.RootKind, target.RootUID,
			target.EndsWindow, target.EndsSession, target.Self)
	}
	for _, target := range p.RegistryOnly {
		fmt.Fprintf(&b, "registry-only,%s,%s,%s,%s,%s,%s;", target.ResourceUID,
			target.Kind, target.Evidence, target.WindowUID, target.RootKind, target.RootUID)
	}
	return b.String()
}

func (p paneLiveDeletePlan) endsWindows() int {
	// Canonical Pane/Agent delete always preserves its owning Window. The
	// EndsWindow bit means a replacement must be created before the kill, not
	// that the final plan ends the Window.
	return 0
}

func (p paneLiveDeletePlan) endsSessions() int {
	return 0
}

func (p paneLiveDeletePlan) hasSelfTarget() bool {
	for _, target := range p.Targets {
		if target.Self {
			return true
		}
	}
	return false
}

type tmuxPaneDeleteRuntime struct {
	runner                tmuxCommandRunner
	target                explicitTmuxTarget
	expectedSocketPath    string
	expectedLogicalSocket string
	routeAuthority        *runtimeMutationRouteAuthority
	getenv                func(string) string
	routeAnchor           string
}

// newTmuxPaneDeleteRuntime builds the live half with no server bound yet.
//
// There is deliberately no default target. The route resolves the exact server
// from the invocation's own flags or inherited $TMUX and installs it with
// useExactTarget; a runtime that was never given one refuses rather than
// reaching for the app socket.
func newTmuxPaneDeleteRuntime() *tmuxPaneDeleteRuntime {
	return &tmuxPaneDeleteRuntime{runner: inttmux.ExecRunner{}, getenv: os.Getenv}
}

func (r *tmuxPaneDeleteRuntime) useExactTarget(target explicitTmuxTarget) {
	if r == nil {
		return
	}
	r.target = target
	r.expectedSocketPath = ""
	r.expectedLogicalSocket = ""
	r.routeAuthority = nil
}

func (r *tmuxPaneDeleteRuntime) useRouteAnchor(paneID string) {
	r.routeAnchor = exactTmuxHandle(strings.TrimSpace(paneID), "%")
}

type livePaneDeleteRow struct {
	sessionID   string
	sessionName string
	windowID    string
	paneID      string
	projectUID  string
	windowUID   string
	paneUID     string
}

func (r *tmuxPaneDeleteRuntime) routed() explicitTmuxRunner {
	if r.expectedSocketPath != "" {
		return explicitTmuxRunner{runner: r.runner, target: explicitTmuxTarget{flag: "-S", value: r.expectedSocketPath}}
	}
	return explicitTmuxRunner{runner: r.runner, target: r.target}
}

func (r *tmuxPaneDeleteRuntime) mutationAction(kind runtimeMutationVerb, target paneLiveDeleteTarget, guard, _ string, args ...string) plannedRuntimeMutation {
	logicalRoute := r.target.flag + "=" + r.target.value
	action := newRuntimeMutation(1, kind, runtimeMutationTarget{
		Socket: logicalRoute, PhysicalSocket: printableRuntimeMutationSocket(r.expectedSocketPath),
		RouteAuthority: r.routeAuthority.printable(),
		Kind:           "pane",
		ID:             target.PaneID,
		UID:            target.PaneUID,
		Parent:         target.SessionID + "/" + target.WindowID + "/" + target.RootUID,
	})
	bindRuntimeMutationGuard(&action, guard)
	action.Operands = slices.Clone(args)
	return action
}

func (r *tmuxPaneDeleteRuntime) guardPrintedMutationRoute(ctx context.Context, action plannedRuntimeMutation) error {
	return guardPrintedRuntimeMutationRoute(ctx, r.runner, runtimeMutationRoute{
		target: r.target, expectedSocketPath: r.expectedSocketPath,
		socketName: r.expectedLogicalSocket, authority: r.routeAuthority,
	}, action)
}

func (r *tmuxPaneDeleteRuntime) mutationSteps(
	targets []paneLiveDeleteTarget,
	verb runtimeMutationVerb,
	guardMirror func(paneLiveDeleteTarget) string,
	effectMirror func(paneLiveDeleteTarget) string,
	args func(paneLiveDeleteTarget) []string,
	undo func(context.Context, paneLiveDeleteTarget) error,
	applied *int,
) []runtimeMutationStep {
	steps := make([]runtimeMutationStep, 0, len(targets))
	for i, target := range targets {
		attempted := false
		guardUID := guardMirror(target)
		effectUID := effectMirror(target)
		guardDetail := "socket=" + r.target.flag + "=" + r.target.value +
			";session=" + target.SessionID + "/" + target.SessionName +
			";window=" + target.WindowID + "/" + target.WindowUID +
			";pane=" + target.PaneID + "/" + guardUID +
			";root=" + string(target.RootKind) + "/" + target.RootUID
		action := r.mutationAction(verb, target, guardDetail, "", args(target)...)
		if verb == mutationQueuePaneKill {
			action.Target.UID = effectUID
			action.Queue = &runtimeMutationQueuedKill{PhysicalSocket: r.expectedSocketPath, LogicalSocket: r.expectedLogicalSocket,
				RouteAuthority: action.Target.RouteAuthority,
				ExpectedUID:    effectUID, SessionID: target.SessionID, WindowID: target.WindowID}
			action.Queue.Marker = runtimeMutationQueueMarker(action)
			bindRuntimeMutationGuard(&action, guardDetail)
		}
		action.Order = i + 1
		steps = append(steps, runtimeMutationStep{
			Action: action,
			TargetRouteGuard: func(ctx context.Context) error {
				return r.guardPrintedMutationRoute(ctx, action)
			},
			Reobserve: func(ctx context.Context) (bool, error) {
				if verb == mutationQueuePaneKill {
					return observeQueuedRuntimeMutationEffect(ctx,
						func(ctx context.Context) (bool, error) {
							return r.observeMutationEffect(ctx, target, effectUID, true, attempted)
						},
						func(ctx context.Context) (bool, error) {
							return observeRuntimeMutationQueueMarker(ctx, r.runner, action)
						})
				}
				return r.observeMutationEffect(ctx, target, effectUID, verb == mutationKillPane, attempted)
			},
			Guard: func(ctx context.Context) error {
				if filepath.Clean(action.Target.PhysicalSocket) != filepath.Clean(r.expectedSocketPath) {
					return errors.New("delete pane: printable physical socket disagrees with bound execution route")
				}
				return r.revalidateMutationTarget(ctx, target, guardUID)
			},
			Apply: func(ctx context.Context) error {
				if _, err := runRuntimeMutationCommand(ctx, r.routed(), action); err != nil {
					return err
				}
				attempted = true
				if applied != nil {
					*applied++
				}
				return nil
			},
		})
		if verb == mutationQueuePaneKill {
			steps[len(steps)-1].Undo = func(ctx context.Context) error {
				return clearRuntimeMutationQueueMarker(ctx, r.runner, action)
			}
		}
		if undo != nil {
			steps[len(steps)-1].Undo = func(ctx context.Context) error { return undo(ctx, target) }
		}
	}
	return steps
}

func (r *tmuxPaneDeleteRuntime) observeMutationEffect(ctx context.Context, target paneLiveDeleteTarget, wantMirror string, absent, allowNoServerAbsent bool) (bool, error) {
	if err := r.guardSocketIdentity(ctx); err != nil {
		if absent && allowNoServerAbsent && inttmux.IsNoServerFailure(err) {
			return true, nil
		}
		return false, err
	}
	out, err := r.routed().Run(ctx, "tmux", "list-panes", "-a", "-F", tmuxRowFormat(
		"#{session_id}", "#{window_id}", "#{pane_id}", "#{"+tmuxopts.PaneUID+"}"))
	if err != nil {
		return false, err
	}
	for _, row := range splitTmuxRows(string(out), 4) {
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

func (r *tmuxPaneDeleteRuntime) revalidateMutationTarget(ctx context.Context, target paneLiveDeleteTarget, wantMirror string) error {
	if err := r.guardSocketIdentity(ctx); err != nil {
		return err
	}
	format := tmuxRowFormat(
		"#{session_id}", "#{session_name}", "#{window_id}", "#{pane_id}",
		"#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.WindowUID+"}", "#{"+tmuxopts.PaneUID+"}",
	)
	out, err := r.routed().Run(ctx, "tmux", "display-message", "-p", "-t", target.PaneID, "-F", format)
	if err != nil {
		return tmuxError("revalidate exact live Pane %s on %s %s: %v", target.PaneID, r.target.flag, r.target.value, err)
	}
	rows := splitTmuxRows(strings.ReplaceAll(string(out), tmuxRowSepFormat, tmuxRowSep), 7)
	if len(rows) != 1 {
		return fmt.Errorf("exact live Pane %s returned malformed containment evidence", target.PaneID)
	}
	row := rows[0]
	if row[0] != target.SessionID || row[1] != target.SessionName || row[2] != target.WindowID ||
		row[3] != target.PaneID || row[5] != target.WindowUID || row[6] != wantMirror {
		return fmt.Errorf("exact live Pane %s drifted: got session=%s/%q window=%s/%q pane=%s uid=%q, want session=%s/%q window=%s/%q pane=%s uid=%q",
			target.PaneID, row[0], row[1], row[2], row[5], row[3], row[6],
			target.SessionID, target.SessionName, target.WindowID, target.WindowUID, target.PaneID, wantMirror)
	}
	root := deleteRootOwner{Kind: target.RootKind, UID: target.RootUID, Session: target.SessionName}
	if err := root.validateLiveSession("delete pane", "Pane", target.PaneID, target.PaneUID, row[4], row[1]); err != nil {
		return err
	}
	return nil
}

func (r *tmuxPaneDeleteRuntime) inventory(ctx context.Context) ([]livePaneDeleteRow, error) {
	if r == nil || r.runner == nil {
		return nil, errors.New("delete pane: tmux runtime is not configured")
	}
	if r.target.flag == "" || r.target.value == "" {
		return nil, errors.New("delete pane: no exact tmux target is bound")
	}
	if err := r.observeSocketIdentity(ctx); err != nil {
		return nil, err
	}
	format := tmuxRowFormat(
		"#{session_id}", "#{session_name}", "#{window_id}", "#{pane_id}",
		"#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.WindowUID+"}", "#{"+tmuxopts.PaneUID+"}",
	)
	out, err := r.routed().Run(ctx, "tmux", "list-panes", "-a", "-F", format)
	if err != nil {
		return nil, tmuxError("delete pane: inventory exact tmux socket: %v", err)
	}
	out = []byte(strings.ReplaceAll(string(out), tmuxRowSepFormat, tmuxRowSep))
	var rows []livePaneDeleteRow
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, tmuxRowSep)
		if len(fields) != 7 {
			return nil, fmt.Errorf("delete pane: malformed exact tmux inventory row %q", line)
		}
		row := livePaneDeleteRow{
			sessionID: strings.TrimSpace(fields[0]), sessionName: strings.TrimSpace(fields[1]),
			windowID: strings.TrimSpace(fields[2]), paneID: strings.TrimSpace(fields[3]),
			projectUID: strings.TrimSpace(fields[4]), windowUID: strings.TrimSpace(fields[5]),
			paneUID: strings.TrimSpace(fields[6]),
		}
		if exactTmuxHandle(row.sessionID, "$") == "" || exactTmuxHandle(row.windowID, "@") == "" || exactTmuxHandle(row.paneID, "%") == "" {
			return nil, fmt.Errorf("delete pane: malformed exact tmux handles session=%q window=%q pane=%q",
				row.sessionID, row.windowID, row.paneID)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func (r *tmuxPaneDeleteRuntime) exactSocketPath(ctx context.Context) (string, error) {
	if err := r.observeSocketIdentity(ctx); err != nil {
		if inttmux.IsNoServerFailure(err) {
			return "", tmuxError("delete pane: exact tmux socket is unavailable (no-server); absence is not Registry deletion authority and nothing was changed: %v", err)
		}
		return "", tmuxError("delete pane: exact tmux socket observation failed: %v", err)
	}
	if strings.TrimSpace(r.expectedSocketPath) == "" {
		return "", errors.New("delete pane: exact tmux socket identity is empty; absence is not Registry deletion authority and nothing was changed")
	}
	return r.expectedSocketPath, nil
}

// observeSocketIdentity binds every read in this invocation to one immutable
// physical socket without claiming mutation authority. Registry-only cleanup
// of an exact durable Offline/MissingRuntime target is intentionally available
// on a standalone tmux server, but it still requires a positive inventory from
// this same physical socket and a second byte-identical preflight under lock.
func (r *tmuxPaneDeleteRuntime) observeSocketIdentity(ctx context.Context) error {
	out, err := r.routed().Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
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
	if r.target.flag == "-S" && observed != r.target.value {
		return fmt.Errorf("delete pane: exact socket drifted: observed %q, planned %q", observed, r.target.value)
	}
	if r.expectedSocketPath == "" {
		r.expectedSocketPath = observed
	}
	if observed != r.expectedSocketPath {
		return fmt.Errorf("delete pane: exact socket drifted: observed %q, planned %q", observed, r.expectedSocketPath)
	}
	return nil
}

// guardSocketIdentity upgrades the immutable observation route to live tmux
// mutation authority. Every kill, tombstone, restore, and deferred queue step
// calls this immediately before its write; a standalone/foreign server can
// therefore authorize only the zero-tmux-write Registry cleanup above.
func (r *tmuxPaneDeleteRuntime) guardSocketIdentity(ctx context.Context) error {
	if err := r.observeSocketIdentity(ctx); err != nil {
		return err
	}
	if r.routeAuthority == nil {
		route, err := resolveExistingRuntimeMutationRouteWithAnchor(ctx, r.runner, r.target, r.getenv, r.routeAnchor)
		if err != nil {
			return fmt.Errorf("delete pane: %w", err)
		}
		if route.expectedSocketPath != r.expectedSocketPath || route.authority == nil {
			return errors.New("delete pane: invocation server-generation authority drifted")
		}
		r.target = route.target
		r.routeAuthority = route.authority
		r.expectedLogicalSocket = route.socketName
		if route.authority.Class == runtimeMutationRouteStandalone {
			r.expectedLogicalSocket = ""
		}
	}
	if r.routeAuthority != nil {
		return guardResolvedRuntimeMutationRoute(ctx, r.runner, runtimeMutationRoute{
			target: r.target, expectedSocketPath: r.expectedSocketPath,
			socketName: r.expectedLogicalSocket, authority: r.routeAuthority,
		})
	}
	if err := guardRuntimeMutationServerOwnership(ctx, r.routed(), r.target); err != nil {
		return fmt.Errorf("delete pane: %w", err)
	}
	logical, err := r.routed().Run(ctx, "tmux", "show-options", "-gqv", runtimeMutationSocketNameOption)
	if err != nil || strings.TrimSpace(string(logical)) == "" {
		return errors.New("delete pane: exact app logical socket marker is unavailable")
	}
	if r.expectedLogicalSocket == "" {
		r.expectedLogicalSocket = strings.TrimSpace(string(logical))
	}
	if strings.TrimSpace(string(logical)) != r.expectedLogicalSocket {
		return errors.New("delete pane: app logical socket marker drifted")
	}
	return nil
}

type plannedPaneDelete struct {
	resourceUID string
	paneUID     string
}

func plannedPaneDeletes(plan deletePlan) []plannedPaneDelete {
	var out []plannedPaneDelete
	for _, target := range plan.Targets {
		switch plan.Kind {
		case coremetadata.KindPane:
			out = append(out, plannedPaneDelete{resourceUID: target.Match.UID, paneUID: target.Match.UID})
		case coremetadata.KindAgent:
			for _, descendant := range target.Descendants {
				if descendant.Kind == coremetadata.KindPane {
					out = append(out, plannedPaneDelete{resourceUID: target.Match.UID, paneUID: descendant.UID})
				}
			}
		}
	}
	return out
}

func paneRegistryAncestry(registry coremetadata.Registry, paneUID string) (coremetadata.Pane, coremetadata.Window, deleteRootOwner, error) {
	pane, ok := registry.Pane(paneUID)
	if !ok {
		return coremetadata.Pane{}, coremetadata.Window{}, deleteRootOwner{},
			fmt.Errorf("registry Pane uid %q disappeared during live preflight", paneUID)
	}
	windowUID := pane.Metadata.OwnerUID()
	if pane.Metadata.OwnerRef != nil && pane.Metadata.OwnerRef.Kind == coremetadata.KindAgent {
		agent, ok := registry.Agent(windowUID)
		if !ok {
			return coremetadata.Pane{}, coremetadata.Window{}, deleteRootOwner{},
				fmt.Errorf("registry Pane uid %q has no owning Agent %q", paneUID, windowUID)
		}
		windowUID = agent.Metadata.OwnerUID()
	}
	window, ok := registry.Window(windowUID)
	if !ok {
		return coremetadata.Pane{}, coremetadata.Window{}, deleteRootOwner{},
			fmt.Errorf("registry Pane uid %q has no owning Window %q", paneUID, windowUID)
	}
	root, err := deleteRootForWindow(registry, *window)
	if err != nil {
		return coremetadata.Pane{}, coremetadata.Window{}, deleteRootOwner{}, err
	}
	return *pane, *window, root, nil
}

func paneHasMissingRuntime(pane coremetadata.Pane) bool {
	condition, ok := pane.HasCondition(coremetadata.ConditionMissingRuntime)
	return ok && condition.Status == coremetadata.ConditionTrue && condition.Reason == coremetadata.ReasonRuntimeUnbound
}

// paneDeleteAuthoritySignature signs the exact Registry facts that make a Pane
// or Agent delete safe. buildDeletePlan already signs the uid cascade; this
// signs the parts that can change without changing that set: owner chain,
// Offline/MissingRuntime evidence, current binding, and activation generation.
func paneDeleteAuthoritySignature(registry coremetadata.Registry, plan deletePlan) (string, error) {
	var b strings.Builder
	writeRoot := func(window coremetadata.Window) error {
		root, err := deleteRootForWindow(registry, window)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "window=%q,window-owner=%q,root-kind=%q,root=%q,session=%q;",
			window.Metadata.UID, window.Metadata.OwnerUID(), root.Kind, root.UID, root.Session)
		return nil
	}
	writePane := func(paneUID string) error {
		pane, window, _, err := paneRegistryAncestry(registry, paneUID)
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
			pane.Status.Activation.RuntimeID, paneHasMissingRuntime(pane))
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

func registryOnlyPaneTarget(registry coremetadata.Registry, plan deletePlan, target deleteTarget) (paneRegistryOnlyDeleteTarget, bool, error) {
	if !plan.ExactUID {
		return paneRegistryOnlyDeleteTarget{}, false, nil
	}
	switch plan.Kind {
	case coremetadata.KindPane:
		pane, window, root, err := paneRegistryAncestry(registry, target.Match.UID)
		if err != nil {
			return paneRegistryOnlyDeleteTarget{}, false, err
		}
		if !paneHasMissingRuntime(pane) {
			return paneRegistryOnlyDeleteTarget{}, false, nil
		}
		return paneRegistryOnlyDeleteTarget{ResourceUID: target.Match.UID, Kind: "Pane", Evidence: coremetadata.ConditionMissingRuntime,
			WindowUID: window.Metadata.UID, RootKind: root.Kind, RootUID: root.UID}, true, nil
	case coremetadata.KindAgent:
		agent, ok := registry.Agent(target.Match.UID)
		if !ok {
			return paneRegistryOnlyDeleteTarget{}, false, fmt.Errorf("registry Agent uid %q disappeared during live preflight", target.Match.UID)
		}
		if agent.Status.Phase != coremetadata.PhaseOffline || strings.TrimSpace(agent.Status.PaneRef) != "" {
			return paneRegistryOnlyDeleteTarget{}, false, nil
		}
		window, ok := registry.Window(agent.Metadata.OwnerUID())
		if !ok || agent.Metadata.OwnerRef == nil || agent.Metadata.OwnerRef.Kind != coremetadata.KindWindow {
			return paneRegistryOnlyDeleteTarget{}, false, fmt.Errorf("registry Agent uid %q has no exact owning Window %q", target.Match.UID, agent.Metadata.OwnerUID())
		}
		root, err := deleteRootForWindow(registry, *window)
		if err != nil {
			return paneRegistryOnlyDeleteTarget{}, false, err
		}
		for _, descendant := range target.Descendants {
			if descendant.Kind != coremetadata.KindPane {
				continue
			}
			pane, ok := registry.Pane(descendant.UID)
			if !ok || !paneHasMissingRuntime(*pane) {
				return paneRegistryOnlyDeleteTarget{}, false, nil
			}
		}
		evidence := string(coremetadata.PhaseOffline)
		if len(target.Descendants) > 0 {
			evidence += "+" + coremetadata.ConditionMissingRuntime
		}
		return paneRegistryOnlyDeleteTarget{ResourceUID: target.Match.UID, Kind: "Agent", Evidence: evidence,
			WindowUID: window.Metadata.UID, RootKind: root.Kind, RootUID: root.UID}, true, nil
	default:
		return paneRegistryOnlyDeleteTarget{}, false, fmt.Errorf("delete pane runtime: unsupported Registry-only kind %q", plan.Kind)
	}
}

func (r *tmuxPaneDeleteRuntime) preflight(ctx context.Context, registry coremetadata.Registry, plan deletePlan) (paneLiveDeletePlan, error) {
	socketPath, err := r.exactSocketPath(ctx)
	if err != nil {
		return paneLiveDeletePlan{}, err
	}
	standaloneEligible := plan.ExactUID && len(plan.Targets) > 0
	for _, target := range plan.Targets {
		_, eligible, targetErr := registryOnlyPaneTarget(registry, plan, target)
		if targetErr != nil {
			return paneLiveDeletePlan{}, fmt.Errorf("delete %s: %w", strings.ToLower(string(plan.Kind)), targetErr)
		}
		standaloneEligible = standaloneEligible && eligible
	}
	if !standaloneEligible {
		if err := r.guardSocketIdentity(ctx); err != nil {
			return paneLiveDeletePlan{}, err
		}
	}
	rows, err := r.inventory(ctx)
	if err != nil {
		return paneLiveDeletePlan{}, err
	}
	if len(rows) == 0 {
		return paneLiveDeletePlan{}, fmt.Errorf("delete %s: exact tmux inventory on %s was empty; absence is not Registry deletion authority and nothing was changed",
			strings.ToLower(string(plan.Kind)), r.target.label())
	}
	authority, err := paneDeleteAuthoritySignature(registry, plan)
	if err != nil {
		return paneLiveDeletePlan{}, fmt.Errorf("delete %s: %w", strings.ToLower(string(plan.Kind)), err)
	}
	planned := plannedPaneDeletes(plan)
	targetUIDs := make(map[string]bool, len(planned))
	for _, target := range planned {
		targetUIDs[target.paneUID] = true
	}
	paneCount := map[string]int{}
	windowCount := map[string]int{}
	removedCount := map[string]int{}
	byUID := map[string][]livePaneDeleteRow{}
	seenWindow := map[string]bool{}
	windowIDsByUID := map[string]map[string]bool{}
	for _, row := range rows {
		paneCount[row.windowID]++
		if !seenWindow[row.windowID] {
			windowCount[row.sessionID]++
			seenWindow[row.windowID] = true
		}
		if row.windowUID != "" {
			if windowIDsByUID[row.windowUID] == nil {
				windowIDsByUID[row.windowUID] = map[string]bool{}
			}
			windowIDsByUID[row.windowUID][row.windowID] = true
		}
		if targetUIDs[row.paneUID] {
			removedCount[row.windowID]++
			byUID[row.paneUID] = append(byUID[row.paneUID], row)
		}
	}

	currentPaneID, currentSocket, err := r.currentInvocationPane(ctx)
	if err != nil {
		return paneLiveDeletePlan{}, err
	}
	if plan.Implicit && currentSocket == "" {
		return paneLiveDeletePlan{}, errors.New("delete pane: implicit active target is not attached to the exact projmux tmux socket")
	}

	live := paneLiveDeletePlan{SocketPath: socketPath, Authority: authority}
	registryOnlyByResource := map[string]bool{}
	for _, target := range plan.Targets {
		registryOnly, ok, targetErr := registryOnlyPaneTarget(registry, plan, target)
		if targetErr != nil {
			return paneLiveDeletePlan{}, fmt.Errorf("delete %s: %w", strings.ToLower(string(plan.Kind)), targetErr)
		}
		if ok {
			// Lifecycle evidence makes a zero-mirror target eligible; it does not
			// override a positive live mirror. A target already live takes the
			// normal exact-kill path. A zero-to-live change on the locked pass
			// changes the signed plan and is refused before mutation.
			hasLiveMirror := false
			for _, pane := range planned {
				if pane.resourceUID == target.Match.UID && len(byUID[pane.paneUID]) > 0 {
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
				return paneLiveDeletePlan{}, fmt.Errorf("delete agent: registry Agent uid %q is %s, not an exact Offline target; no live managed Pane can authorize deletion and nothing was changed",
					target.Match.UID, agent.Status.Phase)
			}
		}
	}
	for _, target := range planned {
		_, window, root, ancestryErr := paneRegistryAncestry(registry, target.paneUID)
		if ancestryErr != nil {
			return paneLiveDeletePlan{}, fmt.Errorf("delete %s: %w", strings.ToLower(string(plan.Kind)), ancestryErr)
		}
		matches := byUID[target.paneUID]
		if len(matches) == 0 {
			if registryOnlyByResource[target.resourceUID] {
				continue
			}
			return paneLiveDeletePlan{}, fmt.Errorf("delete %s: registry Pane uid %q has no exact live tmux Pane mirror on -L %s; nothing was changed",
				strings.ToLower(string(plan.Kind)), target.paneUID, r.target.value)
		}
		if len(matches) != 1 {
			return paneLiveDeletePlan{}, fmt.Errorf("delete %s: registry Pane uid %q has %d live tmux Pane mirrors on -L %s; exact target is ambiguous and nothing was changed",
				strings.ToLower(string(plan.Kind)), target.paneUID, len(matches), r.target.value)
		}
		row := matches[0]
		if row.windowUID != window.Metadata.UID {
			observed := row.windowUID
			if observed == "" {
				observed = "<missing>"
			}
			return paneLiveDeletePlan{}, fmt.Errorf("delete %s: live tmux Pane %s mirrors registry uid %q under foreign Window uid %q, want %q; nothing was changed",
				strings.ToLower(string(plan.Kind)), row.paneID, target.paneUID, observed, window.Metadata.UID)
		}
		if mirrors := len(windowIDsByUID[window.Metadata.UID]); mirrors != 1 {
			return paneLiveDeletePlan{}, fmt.Errorf("delete %s: registry Window uid %q has %d live tmux Window mirrors on -L %s while resolving Pane uid %q; exact owner is ambiguous and nothing was changed",
				strings.ToLower(string(plan.Kind)), window.Metadata.UID, mirrors, r.target.value, target.paneUID)
		}
		spelling := "delete " + strings.ToLower(string(plan.Kind))
		if err := root.validateLiveSession(spelling, "Pane", row.paneID, target.paneUID, row.projectUID, row.sessionName); err != nil {
			return paneLiveDeletePlan{}, err
		}
		if plan.Implicit && row.paneID != currentPaneID {
			return paneLiveDeletePlan{}, fmt.Errorf("delete %s: implicit active registry uid %q mirrors live Pane %s but the exact caller is in %s; nothing was changed",
				strings.ToLower(string(plan.Kind)), target.paneUID, row.paneID, currentPaneID)
		}
		live.Targets = append(live.Targets, paneLiveDeleteTarget{
			ResourceUID: target.resourceUID, PaneUID: target.paneUID, PaneID: row.paneID,
			WindowUID: window.Metadata.UID, WindowID: row.windowID,
			SessionID: row.sessionID, SessionName: row.sessionName, RootKind: root.Kind, RootUID: root.UID,
			Self: row.paneID == currentPaneID && currentSocket != "",
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
		if err := r.guardSocketIdentity(ctx); err != nil {
			return paneLiveDeletePlan{}, err
		}
	}
	return live, nil
}

func (r *tmuxPaneDeleteRuntime) currentInvocationPane(ctx context.Context) (string, string, error) {
	if r.getenv == nil || strings.TrimSpace(r.getenv("TMUX")) == "" || strings.TrimSpace(r.getenv("TMUX_PANE")) == "" {
		return "", "", nil
	}
	inheritedSocket, _, _ := strings.Cut(strings.TrimSpace(r.getenv("TMUX")), ",")
	serverSocket, err := r.routed().Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		return "", "", tmuxError("delete pane: inspect exact caller socket: %v", err)
	}
	if strings.TrimSpace(string(serverSocket)) != inheritedSocket {
		return "", "", nil
	}
	out, err := r.routed().Run(ctx, "tmux", "display-message", "-p", "-t", strings.TrimSpace(r.getenv("TMUX_PANE")),
		"-F", tmuxRowFormat("#{socket_path}", "#{pane_id}"))
	if err != nil {
		return "", "", tmuxError("delete pane: inspect exact caller pane %s: %v", strings.TrimSpace(r.getenv("TMUX_PANE")), err)
	}
	rows := splitTmuxRows(string(out), 2)
	if len(rows) != 1 || strings.TrimSpace(rows[0][0]) != inheritedSocket {
		return "", "", nil
	}
	paneID := exactTmuxHandle(strings.TrimSpace(rows[0][1]), "%")
	if paneID == "" {
		return "", "", errors.New("delete pane: exact caller tmux Pane handle is malformed")
	}
	return paneID, inheritedSocket, nil
}

func (r *tmuxPaneDeleteRuntime) replacementMaterializer() *materializer {
	routed := r.routed()
	return &materializer{
		runner: routed, mirror: intmetadata.NewMirror(routed), sessions: inttmux.NewClient(routed),
		target: r.target, expectedSocketPath: r.expectedSocketPath,
		socketName: r.expectedLogicalSocket, routeAuthority: r.routeAuthority,
	}
}

// prepareReplacements creates and fully mirrors every last-descendant shell
// before any target Pane is killed. A partial create is ownership-ledgered and
// rolled back here, so callers either receive a complete receipt or the exact
// live/Registry preimage remains.
func (r *tmuxPaneDeleteRuntime) prepareReplacements(ctx context.Context, replacements []paneReplacementShell) (paneReplacementReceipt, error) {
	var receipt paneReplacementReceipt
	if len(replacements) == 0 {
		return receipt, nil
	}
	if err := r.guardSocketIdentity(ctx); err != nil {
		return receipt, err
	}
	runtime := r.replacementMaterializer()
	ledger := newRuntimeLedger("delete-pane-replacement")
	fail := func(cause error) (paneReplacementReceipt, error) {
		runtime.rollback(ctx, ledger)
		runtime.clearCreateOperations(ctx, ledger)
		return paneReplacementReceipt{}, cause
	}
	markedSessions := map[string]bool{}
	for _, replacement := range replacements {
		if markedSessions[replacement.Anchor.SessionID] {
			continue
		}
		if err := runtime.markCreateOperation(ctx, replacement.Anchor.SessionID, ledger); err != nil {
			return fail(err)
		}
		markedSessions[replacement.Anchor.SessionID] = true
	}
	for _, replacement := range replacements {
		paneID, err := runtime.splitPane(ctx, replacement.Anchor.PaneID, defaultPlacement,
			replacement.Pane.Spec.CWD, nil)
		if paneID != "" {
			if claimErr := adoptCreatedPane(ctx, runtime, paneID, replacement.Anchor.SessionID,
				replacement.Anchor.WindowID, replacement.Pane, ledger); claimErr != nil {
				return fail(errors.Join(err, claimErr))
			}
			if mirrorErr := runtime.runIdentityWrites(ctx, "pane", paneID, replacement.Pane.Metadata.UID, []identityPlanWrite{
				{operands: []string{"-p", "-t", paneID, "-q", tmuxopts.PaneOwnerKind, string(coremetadata.KindWindow)}, effect: "replacement Pane owner kind equals Window"},
				{operands: []string{"-p", "-t", paneID, "-q", tmuxopts.PaneOwnerUID, replacement.Anchor.WindowUID}, effect: "replacement Pane owner uid equals Window"},
				{operands: []string{"-p", "-t", paneID, "-q", tmuxopts.PaneRole, string(coremetadata.PaneRoleShell)}, effect: "replacement Pane role equals shell"},
			}); mirrorErr != nil {
				return fail(errors.Join(err, mirrorErr))
			}
		}
		if err != nil {
			return fail(err)
		}
	}
	receipt.created = slices.Clone(ledger.created)
	runtime.clearCreateOperations(ctx, ledger)
	return receipt, nil
}

func (r *tmuxPaneDeleteRuntime) rollbackReplacements(ctx context.Context, receipt paneReplacementReceipt) error {
	if len(receipt.created) == 0 {
		return nil
	}
	runtime := r.replacementMaterializer()
	ledger := &runtimeLedger{created: slices.Clone(receipt.created)}
	runtime.rollback(ctx, ledger)
	for _, created := range receipt.created {
		if got := runtime.option(ctx, created.ID, "#{"+created.ownershipOption()+"}"); got == created.UID {
			return fmt.Errorf("delete pane: replacement rollback left owned %s %s uid=%s", created.Kind, created.ID, created.UID)
		}
	}
	return nil
}

func (r *tmuxPaneDeleteRuntime) kill(ctx context.Context, target paneLiveDeleteTarget) error {
	_, err := r.killAll(ctx, []paneLiveDeleteTarget{target})
	if err != nil {
		return tmuxError("delete pane: kill exact live Pane %s in Window %s session %s (%s): %v",
			target.PaneID, target.WindowID, target.SessionName, target.SessionID, err)
	}
	return nil
}

// killAll guards the complete exact target set before killing the first Pane.
// The returned count lets the Registry transaction preserve evidence for the
// exact prefix that was already removed if tmux fails part-way through.
func (r *tmuxPaneDeleteRuntime) killAll(ctx context.Context, targets []paneLiveDeleteTarget) (int, error) {
	applied := 0
	steps := r.mutationSteps(targets, mutationKillPane,
		func(target paneLiveDeleteTarget) string { return target.PaneUID },
		func(target paneLiveDeleteTarget) string { return target.PaneUID },
		func(target paneLiveDeleteTarget) []string { return []string{"-t", target.PaneID} },
		nil, &applied)
	if err := executeRuntimeMutationPlan(ctx, steps); err != nil {
		return applied, err
	}
	return applied, nil
}

func (r *tmuxPaneDeleteRuntime) tombstoneSelfKill(ctx context.Context, targets []paneLiveDeleteTarget) error {
	applied := 0
	undo := func(ctx context.Context, target paneLiveDeleteTarget) error {
		want := deletedPaneMirrorPrefix + target.PaneUID
		if err := r.revalidateMutationTarget(ctx, target, want); err != nil {
			// A tmux command may fail either before or after applying its effect.
			// If the original exact evidence still holds, there is no current-step
			// effect to undo. Any third state is real drift and must fail closed.
			if originalErr := r.revalidateMutationTarget(ctx, target, target.PaneUID); originalErr == nil {
				return nil
			} else {
				return errors.Join(err, originalErr)
			}
		}
		action := r.mutationAction(mutationRestorePane, target,
			"exact tombstoned Pane remains operation-owned", "Pane UID mirror restored",
			"-pq", "-t", target.PaneID, tmuxopts.PaneUID, target.PaneUID)
		_, err := runRuntimeMutationCommand(ctx, r.routed(), action)
		return err
	}
	steps := r.mutationSteps(targets, mutationTombstonePane,
		func(target paneLiveDeleteTarget) string { return target.PaneUID },
		func(target paneLiveDeleteTarget) string { return deletedPaneMirrorPrefix + target.PaneUID },
		func(target paneLiveDeleteTarget) []string {
			return []string{"-pq", "-t", target.PaneID, tmuxopts.PaneUID, deletedPaneMirrorPrefix + target.PaneUID}
		}, undo, &applied)
	if err := executeRuntimeMutationPlan(ctx, steps); err != nil {
		marked := targets[:applied]
		if strings.Contains(err.Error(), "owned reverse rollback incomplete") {
			return fmt.Errorf("%w; rollback of earlier exact Pane tombstone(s) %s was incomplete; Registry resources remain authoritative and the reported tombstone drift cannot be orphan-imported",
				tmuxError("tombstone exact live Pane before Registry commit: %v", err), paneDeleteIDs(marked))
		}
		return fmt.Errorf("%w; earlier exact Pane tombstone(s) %s were restored and Registry resources remain unchanged",
			tmuxError("tombstone exact live Pane before Registry commit: %v", err), paneDeleteIDs(marked))
	}
	return nil
}

func (r *tmuxPaneDeleteRuntime) restoreSelfKill(ctx context.Context, targets []paneLiveDeleteTarget) error {
	steps := r.mutationSteps(targets, mutationRestorePane,
		func(target paneLiveDeleteTarget) string { return deletedPaneMirrorPrefix + target.PaneUID },
		func(target paneLiveDeleteTarget) string { return target.PaneUID },
		func(target paneLiveDeleteTarget) []string {
			return []string{"-pq", "-t", target.PaneID, tmuxopts.PaneUID, target.PaneUID}
		}, nil, nil)
	if err := executeRuntimeMutationPlan(ctx, steps); err != nil {
		return fmt.Errorf("restore exact Pane tombstone(s): %w", err)
	}
	return nil
}

func paneDeleteIDs(targets []paneLiveDeleteTarget) string {
	if len(targets) == 0 {
		return "<none>"
	}
	ids := make([]string, 0, len(targets))
	for _, target := range targets {
		ids = append(ids, fmt.Sprintf("%s/pane-uid=%s", target.PaneID, target.PaneUID))
	}
	return strings.Join(ids, ",")
}

func (r *tmuxPaneDeleteRuntime) queueSelfKill(ctx context.Context, targets []paneLiveDeleteTarget) error {
	ordered := selfLastPaneDeleteTargets(targets)
	queuedCount := 0
	steps := r.mutationSteps(ordered, mutationQueuePaneKill,
		func(target paneLiveDeleteTarget) string { return deletedPaneMirrorPrefix + target.PaneUID },
		func(target paneLiveDeleteTarget) string { return deletedPaneMirrorPrefix + target.PaneUID },
		func(paneLiveDeleteTarget) []string { return nil }, nil, &queuedCount)
	if err := executeRuntimeMutationPlan(ctx, steps); err != nil {
		queued := ordered[:queuedCount]
		remaining := ordered[queuedCount:]
		return fmt.Errorf("%w; queued exact Pane(s) %s may complete, while tombstoned unqueued Pane(s) %s remain as retryable drift and cannot be orphan-imported",
			tmuxError("queue exact live Pane plan for self-target deletion: %v", err),
			paneDeleteIDs(queued), paneDeleteIDs(remaining))
	}
	return nil
}

func selfLastPaneDeleteTargets(targets []paneLiveDeleteTarget) []paneLiveDeleteTarget {
	ordered := make([]paneLiveDeleteTarget, 0, len(targets))
	for _, target := range targets {
		if !target.Self {
			ordered = append(ordered, target)
		}
	}
	for _, target := range targets {
		if target.Self {
			ordered = append(ordered, target)
		}
	}
	return ordered
}
