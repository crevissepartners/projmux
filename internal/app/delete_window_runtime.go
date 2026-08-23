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
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// windowDeleteRuntime is the live half of delete window. The route owns the
// resource transaction; this seam owns only exact-server inventory and exact
// tmux Window mutation.
type windowDeleteRuntime interface {
	// useExactTarget pins every read and write of this seam to one resolved
	// server. It is called before the first inventory of an invocation.
	useExactTarget(explicitTmuxTarget)
	preflight(context.Context, coremetadata.Registry, deletePlan) (windowLiveDeletePlan, error)
	killAll(context.Context, []windowLiveDeleteTarget) (int, error)
	queueSelfKill(context.Context, []windowLiveDeleteTarget) error
}

type windowLiveDeleteTarget struct {
	UID         string
	WindowID    string
	SessionID   string
	SessionName string
	RootKind    coremetadata.Kind
	RootUID     string
	EndsSession bool
	Self        bool
}

type windowLiveDeletePlan struct {
	Targets []windowLiveDeleteTarget
}

func (p windowLiveDeletePlan) signature() string {
	var b strings.Builder
	for _, target := range p.Targets {
		fmt.Fprintf(&b, "%s,%s,%s,%s,%s,%s,%t,%t;", target.UID, target.WindowID,
			target.SessionID, target.SessionName, target.RootKind, target.RootUID, target.EndsSession, target.Self)
	}
	return b.String()
}

func (p windowLiveDeletePlan) endsSessions() int {
	total := 0
	for _, target := range p.Targets {
		if target.EndsSession {
			total++
		}
	}
	return total
}

func (p windowLiveDeletePlan) hasSelfTarget() bool {
	for _, target := range p.Targets {
		if target.Self {
			return true
		}
	}
	return false
}

type tmuxWindowDeleteRuntime struct {
	runner                tmuxCommandRunner
	target                explicitTmuxTarget
	expectedSocketPath    string
	expectedLogicalSocket string
	routeAuthority        *runtimeMutationRouteAuthority
	getenv                func(string) string
}

// newTmuxWindowDeleteRuntime builds the live half with no server bound yet.
// See newTmuxPaneDeleteRuntime for why there is no default target.
func newTmuxWindowDeleteRuntime() *tmuxWindowDeleteRuntime {
	return &tmuxWindowDeleteRuntime{
		runner: inttmux.ExecRunner{},
		getenv: os.Getenv,
	}
}

func (r *tmuxWindowDeleteRuntime) useExactTarget(target explicitTmuxTarget) {
	if r == nil {
		return
	}
	r.target = target
	r.expectedSocketPath = ""
	r.expectedLogicalSocket = ""
	r.routeAuthority = nil
}

type liveWindowRow struct {
	sessionID   string
	sessionName string
	windowID    string
	projectUID  string
	windowUID   string
}

func (r *tmuxWindowDeleteRuntime) routed() explicitTmuxRunner {
	if r.expectedSocketPath != "" {
		return explicitTmuxRunner{runner: r.runner, target: explicitTmuxTarget{flag: "-S", value: r.expectedSocketPath}}
	}
	return explicitTmuxRunner{runner: r.runner, target: r.target}
}

func (r *tmuxWindowDeleteRuntime) inventory(ctx context.Context) ([]liveWindowRow, bool, error) {
	if r == nil || r.runner == nil {
		return nil, false, errors.New("delete window: tmux runtime is not configured")
	}
	if r.target.flag == "" || r.target.value == "" {
		return nil, false, errors.New("delete window: no exact tmux target is bound")
	}
	if err := r.observeSocketIdentity(ctx, true); err != nil {
		return nil, false, err
	}
	format := tmuxRowFormat(
		"#{session_id}",
		"#{session_name}",
		"#{window_id}",
		"#{"+tmuxopts.ProjectUIDSession+"}",
		"#{"+tmuxopts.WindowUID+"}",
	)
	out, err := r.routed().Run(ctx, "tmux", "list-windows", "-a", "-F", format)
	if err != nil {
		// An absent named server is the strongest possible observation that this
		// selected socket projects zero Windows. Accept only the typed subprocess
		// classification: permissions, missing executables, arbitrary runner
		// failures, and plain errors containing similar words cannot authorize a
		// destructive Registry-only path.
		if inttmux.IsNoServerFailure(err) {
			return nil, true, nil
		}
		return nil, false, tmuxError("delete window: inventory exact tmux socket: %v", err)
	}
	out = []byte(strings.ReplaceAll(string(out), tmuxRowSepFormat, tmuxRowSep))
	var rows []liveWindowRow
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, tmuxRowSep)
		if len(fields) != 5 {
			return nil, false, fmt.Errorf("delete window: malformed exact tmux inventory row %q", line)
		}
		row := liveWindowRow{
			sessionID: strings.TrimSpace(fields[0]), sessionName: strings.TrimSpace(fields[1]),
			windowID: strings.TrimSpace(fields[2]), projectUID: strings.TrimSpace(fields[3]),
			windowUID: strings.TrimSpace(fields[4]),
		}
		if exactTmuxHandle(row.sessionID, "$") == "" || exactTmuxHandle(row.windowID, "@") == "" {
			return nil, false, fmt.Errorf("delete window: malformed exact tmux handles session=%q window=%q",
				row.sessionID, row.windowID)
		}
		rows = append(rows, row)
	}
	return rows, false, nil
}

func (r *tmuxWindowDeleteRuntime) preflight(ctx context.Context, registry coremetadata.Registry, plan deletePlan) (windowLiveDeletePlan, error) {
	rows, serverAbsent, err := r.inventory(ctx)
	if err != nil {
		return windowLiveDeletePlan{}, err
	}
	targetUIDs := make(map[string]bool, len(plan.Targets))
	for _, target := range plan.Targets {
		targetUIDs[target.Match.UID] = true
	}
	windowCount := map[string]int{}
	removedCount := map[string]int{}
	byUID := map[string][]liveWindowRow{}
	for _, row := range rows {
		windowCount[row.sessionID]++
		if targetUIDs[row.windowUID] {
			removedCount[row.sessionID]++
			byUID[row.windowUID] = append(byUID[row.windowUID], row)
		}
	}

	var currentWindowID, currentSocket string
	if !serverAbsent {
		currentWindowID, currentSocket, err = r.currentInvocationWindow(ctx)
		if err != nil {
			return windowLiveDeletePlan{}, err
		}
	}
	if plan.Implicit && currentSocket == "" {
		return windowLiveDeletePlan{}, errors.New("delete window: implicit active target is not attached to the exact projmux tmux socket")
	}

	live := windowLiveDeletePlan{}
	for _, target := range plan.Targets {
		window, ok := registry.Window(target.Match.UID)
		if !ok {
			return windowLiveDeletePlan{}, fmt.Errorf("delete window: registry window uid %q disappeared during live preflight", target.Match.UID)
		}
		root, rootErr := deleteRootForWindow(registry, *window)
		if rootErr != nil {
			return windowLiveDeletePlan{}, fmt.Errorf("delete window: %w", rootErr)
		}
		matches := byUID[target.Match.UID]
		if len(matches) == 0 {
			// A non-implicit Window target (an explicit selector or --all) is desired
			// Registry topology even when the selected tmux socket currently projects
			// none of it. Canonical deletion must be able to retire that offline
			// topology without a raw Registry edit. The empty live target is part of
			// the signed preflight:
			// if a matching Window appears before the locked execution pass, the
			// live signature changes and the route fails closed instead of skipping
			// an unapproved kill.
			//
			// An implicit active target cannot legitimately be offline. Keeping its
			// zero-match case closed also protects against a caller/window race.
			if plan.Implicit {
				return windowLiveDeletePlan{}, fmt.Errorf("delete window: registry window uid %q has no exact live tmux Window mirror on -L %s; nothing was changed",
					target.Match.UID, r.target.value)
			}
			continue
		}
		if len(matches) != 1 {
			return windowLiveDeletePlan{}, fmt.Errorf("delete window: registry window uid %q has %d live tmux Window mirrors on -L %s; exact target is ambiguous and nothing was changed",
				target.Match.UID, len(matches), r.target.value)
		}
		row := matches[0]
		if err := root.validateLiveSession("delete window", "Window", row.windowID, target.Match.UID, row.projectUID, row.sessionName); err != nil {
			return windowLiveDeletePlan{}, err
		}
		if plan.Implicit && row.windowID != currentWindowID {
			return windowLiveDeletePlan{}, fmt.Errorf("delete window: implicit active registry uid %q mirrors live Window %s but the exact caller is in %s; nothing was changed",
				target.Match.UID, row.windowID, currentWindowID)
		}
		live.Targets = append(live.Targets, windowLiveDeleteTarget{
			UID: target.Match.UID, WindowID: row.windowID, SessionID: row.sessionID,
			SessionName: row.sessionName, RootKind: root.Kind, RootUID: root.UID,
			Self: row.windowID == currentWindowID && currentSocket != "",
		})
	}
	// Only the final planned kill in a session carries the implicit session
	// cascade. A multi-Window --all must report one ended session, not one per
	// selected sibling.
	markedSession := map[string]bool{}
	for i := len(live.Targets) - 1; i >= 0; i-- {
		target := &live.Targets[i]
		if !markedSession[target.SessionID] && removedCount[target.SessionID] == windowCount[target.SessionID] {
			target.EndsSession = true
			markedSession[target.SessionID] = true
		}
	}
	if len(live.Targets) > 0 {
		if err := r.guardSocketIdentity(ctx, false); err != nil {
			return windowLiveDeletePlan{}, err
		}
	}
	return live, nil
}

// currentInvocationWindow distinguishes a self target from an external target
// and proves that an implicit target came from the same exact socket. A leaked
// TMUX_PANE from another server is not enough.
func (r *tmuxWindowDeleteRuntime) currentInvocationWindow(ctx context.Context) (string, string, error) {
	if r.getenv == nil || strings.TrimSpace(r.getenv("TMUX")) == "" || strings.TrimSpace(r.getenv("TMUX_PANE")) == "" {
		return "", "", nil
	}
	inheritedSocket, _, _ := strings.Cut(strings.TrimSpace(r.getenv("TMUX")), ",")
	serverSocket, err := r.routed().Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		return "", "", tmuxError("delete window: inspect exact caller socket: %v", err)
	}
	if strings.TrimSpace(string(serverSocket)) != inheritedSocket {
		return "", "", nil
	}
	out, err := r.routed().Run(ctx, "tmux", "display-message", "-p", "-t", strings.TrimSpace(r.getenv("TMUX_PANE")),
		"-F", tmuxRowFormat("#{socket_path}", "#{window_id}"))
	if err != nil {
		return "", "", tmuxError("delete window: inspect exact caller pane %s: %v", strings.TrimSpace(r.getenv("TMUX_PANE")), err)
	}
	rows := splitTmuxRows(string(out), 2)
	if len(rows) != 1 || strings.TrimSpace(rows[0][0]) != inheritedSocket {
		return "", "", nil
	}
	windowID := exactTmuxHandle(strings.TrimSpace(rows[0][1]), "@")
	if windowID == "" {
		return "", "", errors.New("delete window: exact caller tmux Window handle is malformed")
	}
	return windowID, inheritedSocket, nil
}

func (r *tmuxWindowDeleteRuntime) killAll(ctx context.Context, targets []windowLiveDeleteTarget) (int, error) {
	applied := 0
	steps := make([]runtimeMutationStep, 0, len(targets))
	for i, target := range targets {
		attempted := false
		action := r.mutationAction(mutationKillWindow, target, target.UID, "-t", target.WindowID)
		action.Order = i + 1
		steps = append(steps, runtimeMutationStep{
			Action: action,
			TargetRouteGuard: func(ctx context.Context) error {
				return r.guardPrintedMutationRoute(ctx, action)
			},
			Reobserve: func(ctx context.Context) (bool, error) {
				return r.observeWindowMutationEffect(ctx, target, target.UID, true, attempted)
			},
			Guard: func(ctx context.Context) error {
				if filepath.Clean(action.Target.PhysicalSocket) != filepath.Clean(r.expectedSocketPath) {
					return errors.New("delete window: printable physical socket disagrees with bound execution route")
				}
				return r.revalidateMutationTarget(ctx, target, target.UID)
			},
			Apply: func(ctx context.Context) error {
				_, err := runRuntimeMutationCommand(ctx, r.routed(), action)
				if err == nil {
					attempted = true
					applied++
				}
				return err
			},
		})
	}
	err := executeRuntimeMutationPlan(ctx, steps)
	if err != nil {
		return applied, tmuxError("delete window: kill exact live Window batch: %v", err)
	}
	return applied, nil
}

func (r *tmuxWindowDeleteRuntime) queueSelfKill(ctx context.Context, targets []windowLiveDeleteTarget) error {
	steps := make([]runtimeMutationStep, 0, len(targets))
	for i, target := range targets {
		attempted := false
		action := r.mutationAction(mutationQueueWindowKill, target, target.UID)
		action.Queue = &runtimeMutationQueuedKill{PhysicalSocket: r.expectedSocketPath, LogicalSocket: r.expectedLogicalSocket,
			RouteAuthority: action.Target.RouteAuthority,
			ExpectedUID:    target.UID, SessionID: target.SessionID, WindowID: target.WindowID}
		action.Queue.Marker = runtimeMutationQueueMarker(action)
		action.Order = i + 1
		steps = append(steps, runtimeMutationStep{
			Action: action,
			TargetRouteGuard: func(ctx context.Context) error {
				return r.guardPrintedMutationRoute(ctx, action)
			},
			Reobserve: func(ctx context.Context) (bool, error) {
				return observeQueuedRuntimeMutationEffect(ctx,
					func(ctx context.Context) (bool, error) {
						return r.observeWindowMutationEffect(ctx, target, target.UID, true, attempted)
					},
					func(ctx context.Context) (bool, error) {
						return observeRuntimeMutationQueueMarker(ctx, r.runner, action)
					})
			},
			Guard: func(ctx context.Context) error {
				if filepath.Clean(action.Target.PhysicalSocket) != filepath.Clean(r.expectedSocketPath) {
					return errors.New("delete window: printable physical socket disagrees with bound execution route")
				}
				return r.revalidateQueuedWindow(ctx, target)
			},
			Apply: func(ctx context.Context) error {
				_, err := runRuntimeMutationCommand(ctx, r.routed(), action)
				if err != nil {
					return tmuxError("queue exact live Window %s in session %s (%s) for self-target deletion: %v",
						target.WindowID, target.SessionName, target.SessionID, err)
				}
				attempted = true
				return nil
			},
		})
		steps[len(steps)-1].Undo = func(ctx context.Context) error {
			return clearRuntimeMutationQueueMarker(ctx, r.runner, action)
		}
	}
	if err := executeRuntimeMutationPlan(ctx, steps); err != nil {
		return tmuxError("queue exact self-target Window deletion: %v", err)
	}
	return nil
}

func (r *tmuxWindowDeleteRuntime) observeWindowMutationEffect(ctx context.Context, target windowLiveDeleteTarget, wantUID string, absent, allowNoServerAbsent bool) (bool, error) {
	if err := r.guardSocketIdentity(ctx, false); err != nil {
		if absent && allowNoServerAbsent && inttmux.IsNoServerFailure(err) {
			return true, nil
		}
		return false, err
	}
	out, err := r.routed().Run(ctx, "tmux", "list-windows", "-a", "-F", tmuxRowFormat(
		"#{session_id}", "#{window_id}", "#{"+tmuxopts.WindowUID+"}"))
	if err != nil {
		return false, err
	}
	for _, row := range splitTmuxRows(string(out), 3) {
		if row[1] != target.WindowID {
			continue
		}
		if absent {
			return false, nil
		}
		return row[0] == target.SessionID && row[2] == wantUID, nil
	}
	return absent, nil
}

func (r *tmuxWindowDeleteRuntime) revalidateQueuedWindow(ctx context.Context, target windowLiveDeleteTarget) error {
	if err := r.guardSocketIdentity(ctx, false); err != nil {
		return err
	}
	out, err := r.routed().Run(ctx, "tmux", "show-options", "-wqv", "-t", target.WindowID, tmuxopts.WindowUID)
	if err != nil {
		return tmuxError("revalidate exact live Window %s before self-target queue: %v", target.WindowID, err)
	}
	observed := strings.TrimSpace(string(out))
	if observed != target.UID {
		if observed == "" {
			observed = "<missing>"
		}
		return fmt.Errorf("delete window: delayed live Window %s mirrors uid %q, want registry uid %q; no self-target kill was queued",
			target.WindowID, observed, target.UID)
	}
	return nil
}

func (r *tmuxWindowDeleteRuntime) mutationAction(verb runtimeMutationVerb, target windowLiveDeleteTarget, mirror string, args ...string) plannedRuntimeMutation {
	logicalRoute := r.target.flag + "=" + r.target.value
	action := newRuntimeMutation(1, verb, runtimeMutationTarget{
		Socket: logicalRoute, PhysicalSocket: printableRuntimeMutationSocket(r.expectedSocketPath),
		RouteAuthority: r.routeAuthority.printable(),
		Kind:           "window", ID: target.WindowID, UID: target.UID,
		Parent: target.SessionID + "/" + target.RootUID,
	})
	bindRuntimeMutationGuard(&action, "socket="+r.target.flag+"="+r.target.value+
		";session="+target.SessionID+"/"+target.SessionName+
		";window="+target.WindowID+"/"+mirror+
		";root="+string(target.RootKind)+"/"+target.RootUID)
	action.Operands = slices.Clone(args)
	return action
}

func (r *tmuxWindowDeleteRuntime) guardPrintedMutationRoute(ctx context.Context, action plannedRuntimeMutation) error {
	return guardPrintedRuntimeMutationRoute(ctx, r.runner, runtimeMutationRoute{
		target: r.target, expectedSocketPath: r.expectedSocketPath,
		socketName: r.expectedLogicalSocket, authority: r.routeAuthority,
	}, action)
}

func (r *tmuxWindowDeleteRuntime) revalidateMutationTarget(ctx context.Context, target windowLiveDeleteTarget, wantUID string) error {
	if err := r.guardSocketIdentity(ctx, false); err != nil {
		return err
	}
	format := tmuxRowFormat("#{session_id}", "#{session_name}", "#{window_id}", "#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.WindowUID+"}")
	out, err := r.routed().Run(ctx, "tmux", "display-message", "-p", "-t", target.WindowID, "-F", format)
	if err != nil {
		return tmuxError("revalidate exact live Window %s: %v", target.WindowID, err)
	}
	rows := splitTmuxRows(strings.ReplaceAll(string(out), tmuxRowSepFormat, tmuxRowSep), 5)
	if len(rows) != 1 {
		return fmt.Errorf("exact live Window %s returned malformed containment evidence", target.WindowID)
	}
	row := rows[0]
	if row[0] != target.SessionID || row[1] != target.SessionName || row[2] != target.WindowID || row[4] != wantUID {
		return fmt.Errorf("exact live Window %s drifted before mutation", target.WindowID)
	}
	root := deleteRootOwner{Kind: target.RootKind, UID: target.RootUID, Session: target.SessionName}
	return root.validateLiveSession("delete window", "Window", target.WindowID, target.UID, row[3], row[1])
}

// guardSocketIdentity binds the first successful observation to one immutable
// physical socket and rechecks it before every write. A logical -L name alone
// is not a stable target: tmux may later resolve the same name to a different
// server after restart or wrapper routing changes.
func (r *tmuxWindowDeleteRuntime) guardSocketIdentity(ctx context.Context, allowAbsent bool) error {
	if err := r.observeSocketIdentity(ctx, allowAbsent); err != nil {
		return err
	}
	if allowAbsent && r.expectedSocketPath == "" {
		return nil
	}
	if r.routeAuthority == nil {
		route, err := resolveExistingRuntimeMutationRoute(ctx, r.runner, r.target, r.getenv)
		if err != nil {
			return fmt.Errorf("delete window: %w", err)
		}
		if route.expectedSocketPath != r.expectedSocketPath || route.authority == nil {
			return errors.New("delete window: invocation server-generation authority drifted")
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
	return errors.New("delete window: exact route has no mutation authority")
}

func (r *tmuxWindowDeleteRuntime) observeSocketIdentity(ctx context.Context, allowAbsent bool) error {
	out, err := r.routed().Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		if allowAbsent && inttmux.IsNoServerFailure(err) {
			return nil
		}
		// Preserve typed no-server evidence until the expected-absence observer
		// has classified it. User-facing command boundaries still flatten the
		// final error so subprocess exit-code behavior is unchanged.
		return fmt.Errorf("delete window: reobserve exact socket identity: %w", err)
	}
	observed := strings.TrimSpace(string(out))
	if observed == "" {
		return errors.New("delete window: exact socket identity is empty")
	}
	if r.target.flag == "-S" && observed != r.target.value {
		return fmt.Errorf("delete window: exact socket drifted: observed %q, planned %q", observed, r.target.value)
	}
	if r.expectedSocketPath == "" {
		r.expectedSocketPath = observed
	}
	if observed != r.expectedSocketPath {
		return fmt.Errorf("delete window: exact socket drifted: observed %q, planned %q", observed, r.expectedSocketPath)
	}
	return nil
}
