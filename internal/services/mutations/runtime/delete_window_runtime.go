package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type WindowLiveDeleteTarget struct {
	UID         string
	WindowID    string
	SessionID   string
	SessionName string
	RootKind    coremetadata.Kind
	RootUID     string
	EndsSession bool
	Self        bool
}

type WindowLiveDeletePlan struct {
	Targets []WindowLiveDeleteTarget
}

func (p WindowLiveDeletePlan) Signature() string {
	var b strings.Builder
	for _, target := range p.Targets {
		fmt.Fprintf(&b, "%s,%s,%s,%s,%s,%s,%t,%t;", target.UID, target.WindowID,
			target.SessionID, target.SessionName, target.RootKind, target.RootUID, target.EndsSession, target.Self)
	}
	return b.String()
}

func (p WindowLiveDeletePlan) EndsSessions() int {
	total := 0
	for _, target := range p.Targets {
		if target.EndsSession {
			total++
		}
	}
	return total
}

func (p WindowLiveDeletePlan) HasSelfTarget() bool {
	return slices.ContainsFunc(p.Targets, func(target WindowLiveDeleteTarget) bool { return target.Self })
}

type TmuxWindowDeleteRuntime struct {
	Runner                TmuxCommandRunner
	Target                TmuxTransport
	ExpectedSocketPath    string
	ExpectedLogicalSocket string
	RouteAuthority        *RuntimeMutationRouteAuthority
	Getenv                func(string) string
}

func (r *TmuxWindowDeleteRuntime) UseExactTarget(target TmuxTransport) {
	if r == nil {
		return
	}
	BindExactDeleteTarget(target, &r.Target, &r.ExpectedSocketPath, &r.ExpectedLogicalSocket, &r.RouteAuthority)
}

type LiveWindowRow struct {
	SessionID   string
	SessionName string
	WindowID    string
	ProjectUID  string
	WindowUID   string
}

func (r *TmuxWindowDeleteRuntime) Routed() ExplicitTmuxRunner {
	if r.ExpectedSocketPath != "" {
		return ExplicitTmuxRunner{Runner: r.Runner, Target: TmuxTransport{Kind: TmuxSocketPath, Value: r.ExpectedSocketPath, Source: TmuxSocketPathSource}}
	}
	return ExplicitTmuxRunner{Runner: r.Runner, Target: r.Target}
}

func (r *TmuxWindowDeleteRuntime) Inventory(ctx context.Context) ([]LiveWindowRow, bool, error) {
	if r == nil || r.Runner == nil {
		return nil, false, errors.New("delete window: tmux runtime is not configured")
	}
	if r.Target.Flag() == "" || r.Target.Value == "" {
		return nil, false, errors.New("delete window: no exact tmux target is bound")
	}
	if err := r.ObserveSocketIdentity(ctx, true); err != nil {
		return nil, false, err
	}
	format := TmuxRowFormat(
		"#{session_id}",
		"#{session_name}",
		"#{window_id}",
		"#{"+tmuxopts.ProjectUIDSession+"}",
		"#{"+tmuxopts.WindowUID+"}",
	)
	out, err := r.Routed().Run(ctx, "tmux", "list-windows", "-a", "-F", format)
	if err != nil {
		// An absent named server is the strongest possible observation that this
		// selected socket projects zero Windows. Accept only the typed subprocess
		// classification: permissions, missing executables, arbitrary runner
		// failures, and plain errors containing similar words cannot authorize a
		// destructive Registry-only path.
		if inttmux.IsNoServerFailure(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("delete window: inventory exact tmux socket: %w", err)
	}
	out = []byte(strings.ReplaceAll(string(out), TmuxRowSepFormat, TmuxRowSep))
	var rows []LiveWindowRow
	for line := range strings.SplitSeq(string(out), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, TmuxRowSep)
		if len(fields) != 5 {
			return nil, false, fmt.Errorf("delete window: malformed exact tmux inventory row %q", line)
		}
		row := LiveWindowRow{
			SessionID: strings.TrimSpace(fields[0]), SessionName: strings.TrimSpace(fields[1]),
			WindowID: strings.TrimSpace(fields[2]), ProjectUID: strings.TrimSpace(fields[3]),
			WindowUID: strings.TrimSpace(fields[4]),
		}
		if ExactTmuxHandle(row.SessionID, "$") == "" || ExactTmuxHandle(row.WindowID, "@") == "" {
			return nil, false, fmt.Errorf("delete window: malformed exact tmux handles session=%q window=%q",
				row.SessionID, row.WindowID)
		}
		rows = append(rows, row)
	}
	return rows, false, nil
}

func (r *TmuxWindowDeleteRuntime) Preflight(ctx context.Context, registry coremetadata.Registry, plan DeletePlan) (WindowLiveDeletePlan, error) {
	rows, serverAbsent, err := r.Inventory(ctx)
	if err != nil {
		return WindowLiveDeletePlan{}, err
	}
	targetUIDs := make(map[string]bool, len(plan.Targets))
	for _, target := range plan.Targets {
		targetUIDs[target.Match.UID] = true
	}
	windowCount := map[string]int{}
	removedCount := map[string]int{}
	byUID := map[string][]LiveWindowRow{}
	for _, row := range rows {
		windowCount[row.SessionID]++
		if targetUIDs[row.WindowUID] {
			removedCount[row.SessionID]++
			byUID[row.WindowUID] = append(byUID[row.WindowUID], row)
		}
	}

	var currentWindowID, currentSocket string
	if !serverAbsent {
		currentWindowID, currentSocket, err = r.CurrentInvocationWindow(ctx)
		if err != nil {
			return WindowLiveDeletePlan{}, err
		}
	}
	if plan.Implicit && currentSocket == "" {
		return WindowLiveDeletePlan{}, errors.New("delete window: implicit active target is not attached to the exact projmux tmux socket")
	}

	live := WindowLiveDeletePlan{}
	for _, target := range plan.Targets {
		window, ok := registry.Window(target.Match.UID)
		if !ok {
			return WindowLiveDeletePlan{}, fmt.Errorf("delete window: registry window uid %q disappeared during live preflight", target.Match.UID)
		}
		root, rootErr := DeleteRootForWindow(registry, *window)
		if rootErr != nil {
			return WindowLiveDeletePlan{}, fmt.Errorf("delete window: %w", rootErr)
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
				return WindowLiveDeletePlan{}, fmt.Errorf("delete window: registry window uid %q has no exact live tmux Window mirror on -L %s; nothing was changed",
					target.Match.UID, r.Target.Value)
			}
			continue
		}
		if len(matches) != 1 {
			return WindowLiveDeletePlan{}, fmt.Errorf("delete window: registry window uid %q has %d live tmux Window mirrors on -L %s; exact target is ambiguous and nothing was changed",
				target.Match.UID, len(matches), r.Target.Value)
		}
		row := matches[0]
		if err := root.ValidateLiveSession("delete window", "Window", row.WindowID, target.Match.UID, row.ProjectUID, row.SessionName); err != nil {
			return WindowLiveDeletePlan{}, err
		}
		if plan.Implicit && row.WindowID != currentWindowID {
			return WindowLiveDeletePlan{}, fmt.Errorf("delete window: implicit active registry uid %q mirrors live Window %s but the exact caller is in %s; nothing was changed",
				target.Match.UID, row.WindowID, currentWindowID)
		}
		live.Targets = append(live.Targets, WindowLiveDeleteTarget{
			UID: target.Match.UID, WindowID: row.WindowID, SessionID: row.SessionID,
			SessionName: row.SessionName, RootKind: root.Kind, RootUID: root.UID,
			Self: row.WindowID == currentWindowID && currentSocket != "",
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
		if err := r.GuardSocketIdentity(ctx, false); err != nil {
			return WindowLiveDeletePlan{}, err
		}
	}
	return live, nil
}

// currentInvocationWindow distinguishes a self target from an external target
// and proves that an implicit target came from the same exact socket. A leaked
// TMUX_PANE from another server is not enough.
func (r *TmuxWindowDeleteRuntime) CurrentInvocationWindow(ctx context.Context) (string, string, error) {
	if r.Getenv == nil || strings.TrimSpace(r.Getenv("TMUX")) == "" || strings.TrimSpace(r.Getenv("TMUX_PANE")) == "" {
		return "", "", nil
	}
	inheritedSocket, _, _ := strings.Cut(strings.TrimSpace(r.Getenv("TMUX")), ",")
	serverSocket, err := r.Routed().Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		return "", "", fmt.Errorf("delete window: inspect exact caller socket: %w", err)
	}
	if strings.TrimSpace(string(serverSocket)) != inheritedSocket {
		return "", "", nil
	}
	out, err := r.Routed().Run(ctx, "tmux", "display-message", "-p", "-t", strings.TrimSpace(r.Getenv("TMUX_PANE")),
		"-F", TmuxRowFormat("#{socket_path}", "#{window_id}"))
	if err != nil {
		return "", "", fmt.Errorf("delete window: inspect exact caller pane %s: %w", strings.TrimSpace(r.Getenv("TMUX_PANE")), err)
	}
	rows := SplitTmuxRows(string(out), 2)
	if len(rows) != 1 || strings.TrimSpace(rows[0][0]) != inheritedSocket {
		return "", "", nil
	}
	windowID := ExactTmuxHandle(strings.TrimSpace(rows[0][1]), "@")
	if windowID == "" {
		return "", "", errors.New("delete window: exact caller tmux Window handle is malformed")
	}
	return windowID, inheritedSocket, nil
}

func (r *TmuxWindowDeleteRuntime) KillAll(ctx context.Context, targets []WindowLiveDeleteTarget) (int, error) {
	applied := 0
	steps := make([]RuntimeMutationStep, 0, len(targets))
	for i, target := range targets {
		attempted := false
		action := r.MutationAction(MutationKillWindow, target, target.UID, "-t", target.WindowID)
		action.Order = i + 1
		steps = append(steps, RuntimeMutationStep{
			Action: action,
			TargetRouteGuard: func(ctx context.Context) error {
				return r.GuardPrintedMutationRoute(ctx, action)
			},
			Reobserve: func(ctx context.Context) (bool, error) {
				return r.ObserveWindowMutationEffect(ctx, target, target.UID, true, attempted)
			},
			Guard: func(ctx context.Context) error {
				if filepath.Clean(action.Target.PhysicalSocket) != filepath.Clean(r.ExpectedSocketPath) {
					return errors.New("delete window: printable physical socket disagrees with bound execution route")
				}
				// kill-window's goal is the exact Window's absence. A Guard
				// refusal is converged only when one re-read of the Reobserve
				// predicate proves the exact Window gone; every other drift
				// stays refused.
				err := r.RevalidateMutationTarget(ctx, target, target.UID)
				return RuntimeMutationAbsenceConverged(ctx, err, func(ctx context.Context) (bool, error) {
					return r.ObserveWindowMutationEffect(ctx, target, target.UID, true, attempted)
				})
			},
			Apply: func(ctx context.Context) error {
				_, err := RunRuntimeMutationCommand(ctx, r.Routed(), action)
				if err == nil {
					attempted = true
					applied++
				}
				return err
			},
		})
	}
	err := ExecuteRuntimeMutationPlan(ctx, steps)
	if err != nil {
		return applied, fmt.Errorf("delete window: kill exact live Window batch: %w", err)
	}
	return applied, nil
}

func (r *TmuxWindowDeleteRuntime) QueueSelfKill(ctx context.Context, targets []WindowLiveDeleteTarget) error {
	steps := make([]RuntimeMutationStep, 0, len(targets))
	for i, target := range targets {
		attempted := false
		action := r.MutationAction(MutationQueueWindowKill, target, target.UID)
		action.Queue = &RuntimeMutationQueuedKill{PhysicalSocket: r.ExpectedSocketPath, LogicalSocket: r.ExpectedLogicalSocket,
			RouteAuthority: action.Target.RouteAuthority,
			ExpectedUID:    target.UID, SessionID: target.SessionID, WindowID: target.WindowID}
		action.Queue.Marker = RuntimeMutationQueueMarker(action)
		action.Order = i + 1
		steps = append(steps, RuntimeMutationStep{
			Action: action,
			TargetRouteGuard: func(ctx context.Context) error {
				return r.GuardPrintedMutationRoute(ctx, action)
			},
			Reobserve: func(ctx context.Context) (bool, error) {
				return ObserveQueuedRuntimeMutationEffect(ctx,
					func(ctx context.Context) (bool, error) {
						return r.ObserveWindowMutationEffect(ctx, target, target.UID, true, attempted)
					},
					func(ctx context.Context) (bool, error) {
						return ObserveRuntimeMutationQueueMarker(ctx, r.Runner, action)
					})
			},
			Guard: func(ctx context.Context) error {
				if filepath.Clean(action.Target.PhysicalSocket) != filepath.Clean(r.ExpectedSocketPath) {
					return errors.New("delete window: printable physical socket disagrees with bound execution route")
				}
				return r.RevalidateQueuedWindow(ctx, target)
			},
			Apply: func(ctx context.Context) error {
				_, err := RunRuntimeMutationCommand(ctx, r.Routed(), action)
				if err != nil {
					return fmt.Errorf("queue exact live Window %s in session %s (%s) for self-target deletion: %w",
						target.WindowID, target.SessionName, target.SessionID, err)
				}
				attempted = true
				return nil
			},
		})
		steps[len(steps)-1].Undo = func(ctx context.Context) error {
			return ClearRuntimeMutationQueueMarker(ctx, r.Runner, action)
		}
	}
	if err := ExecuteRuntimeMutationPlan(ctx, steps); err != nil {
		return fmt.Errorf("queue exact self-target Window deletion: %w", err)
	}
	return nil
}

func (r *TmuxWindowDeleteRuntime) ObserveWindowMutationEffect(ctx context.Context, target WindowLiveDeleteTarget, wantUID string, absent, allowNoServerAbsent bool) (bool, error) {
	if err := r.GuardSocketIdentity(ctx, false); err != nil {
		if absent && allowNoServerAbsent && inttmux.IsNoServerFailure(err) {
			return true, nil
		}
		return false, err
	}
	out, err := r.Routed().Run(ctx, "tmux", "list-windows", "-a", "-F", TmuxRowFormat(
		"#{session_id}", "#{window_id}", "#{"+tmuxopts.WindowUID+"}"))
	if err != nil {
		return false, err
	}
	for _, row := range SplitTmuxRows(string(out), 3) {
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

func (r *TmuxWindowDeleteRuntime) RevalidateQueuedWindow(ctx context.Context, target WindowLiveDeleteTarget) error {
	if err := r.GuardSocketIdentity(ctx, false); err != nil {
		return err
	}
	out, err := r.Routed().Run(ctx, "tmux", "show-options", "-wqv", "-t", target.WindowID, tmuxopts.WindowUID)
	if err != nil {
		return fmt.Errorf("revalidate exact live Window %s before self-target queue: %w", target.WindowID, err)
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

func (r *TmuxWindowDeleteRuntime) MutationAction(verb RuntimeMutationVerb, target WindowLiveDeleteTarget, mirror string, args ...string) PlannedRuntimeMutation {
	logicalRoute := r.Target.Flag() + "=" + r.Target.Value
	action := NewRuntimeMutation(1, verb, RuntimeMutationTarget{
		Socket: logicalRoute, PhysicalSocket: PrintableRuntimeMutationSocket(r.ExpectedSocketPath),
		RouteAuthority: r.RouteAuthority.Printable(),
		Kind:           "window", ID: target.WindowID, UID: target.UID,
		Parent: target.SessionID + "/" + target.RootUID,
	})
	BindRuntimeMutationGuard(&action, "socket="+r.Target.Flag()+"="+r.Target.Value+
		";session="+target.SessionID+"/"+target.SessionName+
		";window="+target.WindowID+"/"+mirror+
		";root="+string(target.RootKind)+"/"+target.RootUID)
	action.Operands = slices.Clone(args)
	return action
}

func (r *TmuxWindowDeleteRuntime) GuardPrintedMutationRoute(ctx context.Context, action PlannedRuntimeMutation) error {
	return GuardPrintedRuntimeMutationRoute(ctx, r.Runner, RuntimeMutationRoute{
		Target: r.Target, ExpectedSocketPath: r.ExpectedSocketPath,
		SocketName: r.ExpectedLogicalSocket, Authority: r.RouteAuthority,
	}, action)
}

func (r *TmuxWindowDeleteRuntime) RevalidateMutationTarget(ctx context.Context, target WindowLiveDeleteTarget, wantUID string) error {
	if err := r.GuardSocketIdentity(ctx, false); err != nil {
		return err
	}
	format := TmuxRowFormat("#{session_id}", "#{session_name}", "#{window_id}", "#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.WindowUID+"}")
	out, err := r.Routed().Run(ctx, "tmux", "display-message", "-p", "-t", target.WindowID, "-F", format)
	if err != nil {
		return fmt.Errorf("revalidate exact live Window %s: %w", target.WindowID, err)
	}
	rows := SplitTmuxRows(strings.ReplaceAll(string(out), TmuxRowSepFormat, TmuxRowSep), 5)
	if len(rows) != 1 {
		return fmt.Errorf("exact live Window %s returned malformed containment evidence", target.WindowID)
	}
	row := rows[0]
	if row[0] != target.SessionID || row[1] != target.SessionName || row[2] != target.WindowID || row[4] != wantUID {
		return fmt.Errorf("exact live Window %s drifted before mutation", target.WindowID)
	}
	root := DeleteRootOwner{Kind: target.RootKind, UID: target.RootUID, Session: target.SessionName}
	return root.ValidateLiveSession("delete window", "Window", target.WindowID, target.UID, row[3], row[1])
}

// guardSocketIdentity binds the first successful observation to one immutable
// physical socket and rechecks it before every write. A logical -L name alone
// is not a stable target: tmux may later resolve the same name to a different
// server after restart or wrapper routing changes.
func (r *TmuxWindowDeleteRuntime) GuardSocketIdentity(ctx context.Context, allowAbsent bool) error {
	if err := r.ObserveSocketIdentity(ctx, allowAbsent); err != nil {
		return err
	}
	if allowAbsent && r.ExpectedSocketPath == "" {
		return nil
	}
	if r.RouteAuthority == nil {
		route, err := ResolveExistingRuntimeMutationRoute(ctx, r.Runner, r.Target, r.Getenv)
		if err != nil {
			return fmt.Errorf("delete window: %w", err)
		}
		if route.ExpectedSocketPath != r.ExpectedSocketPath || route.Authority == nil {
			return errors.New("delete window: invocation server-generation authority drifted")
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
	return errors.New("delete window: exact route has no mutation authority")
}

func (r *TmuxWindowDeleteRuntime) ObserveSocketIdentity(ctx context.Context, allowAbsent bool) error {
	out, err := r.Routed().Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
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
	if r.Target.Flag() == "-S" && observed != r.Target.Value {
		return fmt.Errorf("delete window: exact socket drifted: observed %q, planned %q", observed, r.Target.Value)
	}
	if r.ExpectedSocketPath == "" {
		r.ExpectedSocketPath = observed
	}
	if observed != r.ExpectedSocketPath {
		return fmt.Errorf("delete window: exact socket drifted: observed %q, planned %q", observed, r.ExpectedSocketPath)
	}
	return nil
}
