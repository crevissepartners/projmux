package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// runtimeMutationSocketNameOption records the logical -L name that config
// apply used for one app-owned server. #{socket_path} alone is insufficient:
// an arbitrary -S path and an -L name may share a basename, while only the
// latter can preserve the name-only PROJMUX_SOCKET hook contract.
const runtimeMutationSocketNameOption = "@projmux_socket_name"

type runtimeMutationMarkerDiagnosis string

const runtimeMutationMarkerMissing runtimeMutationMarkerDiagnosis = "missing-logical-marker"

// runtimeMutationMarkerError is the typed, zero-write diagnosis returned when
// an app-owned server has only the pre-0.13 ownership marker. Ordinary
// mutation routes never repair this state. The only recovery is the explicit
// config-apply writer, and the command is included only after the logical -L
// name has been matched back to the observed physical socket.
type runtimeMutationMarkerError struct {
	Diagnosis     runtimeMutationMarkerDiagnosis
	LogicalSocket string
}

func (e *runtimeMutationMarkerError) Error() string {
	if e == nil {
		return "runtime mutation route: app-owned server has an invalid marker diagnosis"
	}
	if e.LogicalSocket == "" {
		return "runtime mutation route: app-owned server has no logical socket marker (partial marker state); run `projmux doctor --section runtime` to identify the exact recovery route"
	}
	return fmt.Sprintf("runtime mutation route: app-owned server has no logical socket marker (partial marker state); recovery: run `projmux config apply --socket %s`, then retry", e.LogicalSocket)
}

func missingRuntimeMutationMarker(socketName string) error {
	return &runtimeMutationMarkerError{Diagnosis: runtimeMutationMarkerMissing, LogicalSocket: socketName}
}

// runtimeMutationAnchorPaneEnv is private transport between a generated,
// authority-checked popup launcher and its child process. It is deliberately
// distinct from TMUX_PANE: display-popup does not promise that inherited value
// names the originating Pane, while the generated --anchor operand does.
const runtimeMutationAnchorPaneEnv = "__PROJMUX_RUNTIME_ANCHOR_PANE"

type runtimeMutationRoute struct {
	target             explicitTmuxTarget
	expectedSocketPath string
	socketName         string
	authority          *runtimeMutationRouteAuthority
}

const (
	runtimeMutationRouteApp                = "app"
	runtimeMutationRouteStandalone         = "standalone"
	runtimeMutationRouteStandaloneExplicit = "standalone-explicit"
)

type runtimeMutationRouteAuthority struct {
	Class     string
	ServerPID string
	SessionID string
	WindowID  string
	PaneID    string
}

func (a *runtimeMutationRouteAuthority) printable() string {
	if a == nil {
		return ""
	}
	return a.Class + ":pid=" + a.ServerPID + "/session=" + a.SessionID + "/window=" + a.WindowID + "/pane=" + a.PaneID
}

func bindRuntimeMutationRouteTarget(target *runtimeMutationTarget, route runtimeMutationRoute) {
	if target == nil {
		return
	}
	target.Socket = route.target.flag + "=" + route.target.value
	target.PhysicalSocket = printableRuntimeMutationSocket(route.expectedSocketPath)
	target.RouteAuthority = route.authority.printable()
}

func parseRuntimeMutationRouteAuthority(value string) (runtimeMutationRouteAuthority, error) {
	class, rest, ok := strings.Cut(strings.TrimSpace(value), ":pid=")
	if !ok || (class != runtimeMutationRouteApp && class != runtimeMutationRouteStandalone && class != runtimeMutationRouteStandaloneExplicit) {
		return runtimeMutationRouteAuthority{}, errors.New("runtime mutation route authority has an unknown class")
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 4 {
		return runtimeMutationRouteAuthority{}, errors.New("runtime mutation route authority has incomplete containment")
	}
	pid := parts[0]
	session, okSession := strings.CutPrefix(parts[1], "session=")
	window, okWindow := strings.CutPrefix(parts[2], "window=")
	pane, okPane := strings.CutPrefix(parts[3], "pane=")
	parsedPID, err := strconv.Atoi(pid)
	if err != nil || parsedPID <= 0 || !okSession || !okWindow || !okPane {
		return runtimeMutationRouteAuthority{}, errors.New("runtime mutation route authority has an invalid server generation")
	}
	hasReceipt := session != "" || window != "" || pane != ""
	if hasReceipt && (exactTmuxHandle(session, "$") == "" || exactTmuxHandle(window, "@") == "" || exactTmuxHandle(pane, "%") == "") {
		return runtimeMutationRouteAuthority{}, errors.New("runtime mutation route authority has a partial or invalid $/@/% receipt")
	}
	if class == runtimeMutationRouteStandalone && !hasReceipt {
		return runtimeMutationRouteAuthority{}, errors.New("runtime mutation standalone authority requires an exact $/@/% receipt")
	}
	if class == runtimeMutationRouteStandaloneExplicit && hasReceipt {
		return runtimeMutationRouteAuthority{}, errors.New("runtime mutation explicit standalone authority cannot claim an inferred $/@/% receipt")
	}
	return runtimeMutationRouteAuthority{Class: class, ServerPID: pid, SessionID: session, WindowID: window, PaneID: pane}, nil
}

type inheritedTmuxReceipt struct {
	SocketPath string
	ServerPID  string
	ClientID   string
}

func parseInheritedTmuxReceipt(value string) (inheritedTmuxReceipt, error) {
	parts := strings.Split(strings.TrimSpace(value), ",")
	if len(parts) != 3 {
		return inheritedTmuxReceipt{}, errors.New("runtime mutation route: inherited TMUX must contain exactly path,pid,index")
	}
	pathTarget, err := tmuxSocketPathTarget(parts[0])
	if err != nil || pathTarget.value != parts[0] {
		return inheritedTmuxReceipt{}, errors.New("runtime mutation route: inherited TMUX socket path is not absolute and clean")
	}
	pid, pidErr := strconv.Atoi(parts[1])
	index, indexErr := strconv.Atoi(parts[2])
	if pidErr != nil || pid <= 0 || indexErr != nil || index < 0 {
		return inheritedTmuxReceipt{}, errors.New("runtime mutation route: inherited TMUX pid or client index is invalid")
	}
	return inheritedTmuxReceipt{SocketPath: pathTarget.value, ServerPID: parts[1], ClientID: parts[2]}, nil
}

func observeInheritedRuntimeMutationAuthority(
	ctx context.Context,
	routed tmuxCommandRunner,
	receipt inheritedTmuxReceipt,
	paneID, class string,
) (*runtimeMutationRouteAuthority, error) {
	if exactTmuxHandle(paneID, "%") == "" {
		return nil, errors.New("runtime mutation route: inherited invocation requires exact TMUX_PANE authority")
	}
	out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-t", paneID, "-F", tmuxRowFormat(
		"#{socket_path}", "#{pid}", "#{session_id}", "#{window_id}", "#{pane_id}"))
	if err != nil {
		return nil, fmt.Errorf("runtime mutation route: reobserve inherited anchor: %w", err)
	}
	rows := splitTmuxRows(string(out), 5)
	if len(rows) != 1 || rows[0][0] != receipt.SocketPath || rows[0][1] != receipt.ServerPID ||
		exactTmuxHandle(rows[0][2], "$") == "" || exactTmuxHandle(rows[0][3], "@") == "" || rows[0][4] != paneID {
		return nil, errors.New("runtime mutation route: inherited invocation socket/pid/pane containment drifted")
	}
	return &runtimeMutationRouteAuthority{
		Class: class, ServerPID: receipt.ServerPID, SessionID: rows[0][2], WindowID: rows[0][3], PaneID: rows[0][4],
	}, nil
}

// observeExplicitAppAnchorAuthority binds a detached invocation's required
// `%N` operand to the already-proven physical app socket and server generation.
// Unlike the generic inherited path, it uses no TMUX_PANE or active-pane
// inference: the typed operand must itself resolve to one exact $/@/% receipt.
func observeExplicitAppAnchorAuthority(
	ctx context.Context,
	routed tmuxCommandRunner,
	expectedSocketPath, serverPID, paneID string,
) (*runtimeMutationRouteAuthority, error) {
	if exactTmuxHandle(paneID, "%") == "" {
		return nil, errors.New("runtime mutation route: detached invocation requires an exact --anchor %N")
	}
	out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-t", paneID, "-F", tmuxRowFormat(
		"#{socket_path}", "#{pid}", "#{session_id}", "#{window_id}", "#{pane_id}"))
	if err != nil {
		return nil, fmt.Errorf("runtime mutation route: reobserve explicit anchor: %w", err)
	}
	rows := splitTmuxRows(string(out), 5)
	if len(rows) != 1 || rows[0][0] != expectedSocketPath || rows[0][1] != serverPID ||
		exactTmuxHandle(rows[0][2], "$") == "" || exactTmuxHandle(rows[0][3], "@") == "" || rows[0][4] != paneID {
		return nil, errors.New("runtime mutation route: explicit anchor socket/pid/pane containment drifted")
	}
	return &runtimeMutationRouteAuthority{
		Class: runtimeMutationRouteApp, ServerPID: serverPID,
		SessionID: rows[0][2], WindowID: rows[0][3], PaneID: rows[0][4],
	}, nil
}

func defaultRuntimeMutationRoute() runtimeMutationRoute {
	return runtimeMutationRoute{
		target:     explicitTmuxTarget{flag: "-L", value: defaultAppSocket},
		socketName: defaultAppSocket,
	}
}

// resolveExistingRuntimeMutationRoute binds an already-running explicit route
// to one physical socket and server generation. Blank ownership markers are
// accepted only when exact inherited TMUX+TMUX_PANE containment proves an
// operator-owned standalone invocation; explicit foreign routes never acquire
// mutation authority by absence alone.
func resolveExistingRuntimeMutationRoute(
	ctx context.Context,
	runner tmuxCommandRunner,
	target explicitTmuxTarget,
	lookupEnv func(string) string,
) (runtimeMutationRoute, error) {
	return resolveExistingRuntimeMutationRouteWithAnchor(ctx, runner, target, lookupEnv, "")
}

func resolveExistingRuntimeMutationRouteWithAnchor(
	ctx context.Context,
	runner tmuxCommandRunner,
	target explicitTmuxTarget,
	lookupEnv func(string) string,
	anchorPaneID string,
) (runtimeMutationRoute, error) {
	paneID, err := resolveRuntimeMutationAnchorPane(lookupEnv, anchorPaneID)
	if err != nil {
		return runtimeMutationRoute{}, err
	}
	routed := explicitTmuxRunner{runner: runner, target: target}
	pathOut, err := routed.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		return runtimeMutationRoute{}, err
	}
	path := strings.TrimSpace(string(pathOut))
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return runtimeMutationRoute{}, errors.New("runtime mutation route: observed socket is not absolute and clean")
	}
	physical := explicitTmuxRunner{runner: runner, target: explicitTmuxTarget{flag: "-S", value: path}}
	appOut, appErr := physical.Run(ctx, "tmux", "show-options", "-gqv", tmuxopts.AppGlobal)
	logicalOut, logicalErr := physical.Run(ctx, "tmux", "show-options", "-gqv", runtimeMutationSocketNameOption)
	if appErr != nil || logicalErr != nil {
		return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: ownership markers are unreadable (app=%v logical=%v)", appErr, logicalErr)
	}
	appMarker, logicalMarker := strings.TrimSpace(string(appOut)), strings.TrimSpace(string(logicalOut))
	var inherited *inheritedTmuxReceipt
	if lookupEnv != nil && strings.TrimSpace(lookupEnv("TMUX")) != "" {
		receipt, parseErr := parseInheritedTmuxReceipt(lookupEnv("TMUX"))
		if parseErr != nil {
			return runtimeMutationRoute{}, parseErr
		}
		if receipt.SocketPath == path {
			inherited = &receipt
		}
	}
	if appMarker == "" && logicalMarker == "" {
		if inherited == nil {
			return runtimeMutationRoute{}, errors.New("runtime mutation route: standalone authority requires exact inherited TMUX receipt")
		}
		authority, err := observeInheritedRuntimeMutationAuthority(ctx, physical, *inherited, paneID, runtimeMutationRouteStandalone)
		if err != nil {
			return runtimeMutationRoute{}, err
		}
		return runtimeMutationRoute{
			target: explicitTmuxTarget{flag: "-S", value: path}, expectedSocketPath: path,
			socketName: defaultAppSocket, authority: authority,
		}, nil
	}
	if appMarker == "1" && logicalMarker == "" {
		return runtimeMutationRoute{}, missingRuntimeMutationMarker(observeMissingMarkerLogicalSocket(ctx, runner, target, path, lookupEnv))
	}
	if appMarker != "1" || logicalMarker == "" {
		return runtimeMutationRoute{}, errors.New("runtime mutation route: exact server is not app-owned; partial or foreign markers refuse mutation authority")
	}
	logicalTarget, err := tmuxSocketNameTarget(logicalMarker)
	if err != nil {
		return runtimeMutationRoute{}, errors.New("runtime mutation route: app server logical marker is invalid")
	}
	routeTarget := target
	if target.flag == "-L" {
		byName, err := (explicitTmuxRunner{runner: runner, target: logicalTarget}).Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
		if err != nil || strings.TrimSpace(string(byName)) != path {
			return runtimeMutationRoute{}, errors.New("runtime mutation route: app logical alias no longer names the exact server")
		}
		routeTarget = logicalTarget
	}
	var authority *runtimeMutationRouteAuthority
	if inherited != nil {
		authority, err = observeInheritedRuntimeMutationAuthority(ctx, physical, *inherited, paneID, runtimeMutationRouteApp)
	} else {
		pidOut, pidErr := physical.Run(ctx, "tmux", "display-message", "-p", "-F", "#{pid}")
		pid := strings.TrimSpace(string(pidOut))
		parsedPID, parseErr := strconv.Atoi(pid)
		if pidErr != nil || parseErr != nil || parsedPID <= 0 {
			return runtimeMutationRoute{}, errors.New("runtime mutation route: app server generation is unreadable")
		}
		authority = &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: pid}
	}
	if err != nil {
		return runtimeMutationRoute{}, err
	}
	return runtimeMutationRoute{
		target: routeTarget, expectedSocketPath: path, socketName: logicalMarker, authority: authority,
	}, nil
}

func resolveRuntimeMutationAnchorPane(lookupEnv func(string) string, explicit string) (string, error) {
	if raw := strings.TrimSpace(explicit); raw != "" {
		if pane := exactTmuxHandle(raw, "%"); pane != "" {
			return pane, nil
		}
		return "", errors.New("runtime mutation route: explicit anchor is not an exact TMUX Pane %N")
	}
	if lookupEnv == nil {
		return "", nil
	}
	if raw := strings.TrimSpace(lookupEnv(runtimeMutationAnchorPaneEnv)); raw != "" {
		if pane := exactTmuxHandle(raw, "%"); pane != "" {
			return pane, nil
		}
		return "", errors.New("runtime mutation route: private producer anchor is not an exact TMUX Pane %N")
	}
	if raw := strings.TrimSpace(lookupEnv("TMUX_PANE")); raw != "" {
		if pane := exactTmuxHandle(raw, "%"); pane != "" {
			return pane, nil
		}
		return "", errors.New("runtime mutation route: inherited TMUX_PANE is not an exact Pane %N")
	}
	return "", nil
}

// guardRuntimeMutationServerOwnership proves that an already-running server is
// the app-owned server declared by the logical route. A matching socket path is
// not ownership evidence: a foreign server can be cloned onto the same -L name.
func guardRuntimeMutationServerOwnership(ctx context.Context, routed tmuxCommandRunner, target explicitTmuxTarget) error {
	owned, err := routed.Run(ctx, "tmux", "show-options", "-gqv", tmuxopts.AppGlobal)
	if err != nil {
		return fmt.Errorf("runtime mutation route: read app ownership marker: %w", err)
	}
	if strings.TrimSpace(string(owned)) != "1" {
		return errors.New("runtime mutation route: exact server is not app-owned")
	}
	logical, err := routed.Run(ctx, "tmux", "show-options", "-gqv", runtimeMutationSocketNameOption)
	if err != nil {
		return fmt.Errorf("runtime mutation route: read app logical socket marker: %w", err)
	}
	logicalName := strings.TrimSpace(string(logical))
	if logicalName == "" {
		name := ""
		if target.flag == "-L" {
			name = target.value
		}
		return missingRuntimeMutationMarker(name)
	}
	if _, err := tmuxSocketNameTarget(logicalName); err != nil {
		return errors.New("runtime mutation route: app server logical marker is invalid")
	}
	if target.flag == "-L" && logicalName != target.value {
		return fmt.Errorf("runtime mutation route: logical socket marker is %q, planned %q", logicalName, target.value)
	}
	return nil
}

// resolveInvocationRuntimeMutationRoute converts inherited invocation evidence
// into an explicit logical route before any mutation. The inherited value is
// never used to execute a write: it is queried through -S, matched to the
// app-owned logical-name marker, and then independently reobserved through -L.
// No filesystem basename inference participates.
func resolveInvocationRuntimeMutationRoute(
	ctx context.Context,
	runner tmuxCommandRunner,
	lookupEnv func(string) string,
) (runtimeMutationRoute, error) {
	return resolveInvocationRuntimeMutationRouteWithPolicy(ctx, runner, lookupEnv, "", false)
}

func resolveInvocationRuntimeMutationRouteWithAnchor(
	ctx context.Context,
	runner tmuxCommandRunner,
	lookupEnv func(string) string,
	paneID string,
) (runtimeMutationRoute, error) {
	return resolveInvocationRuntimeMutationRouteWithPolicy(ctx, runner, lookupEnv, paneID, false)
}

// resolveExactObjectRuntimeMutationRoute permits an app-owned invocation to
// carry server-generation authority without a Pane receipt. The caller must
// still prove its exact object UID and containment in its typed action guard.
// Standalone servers never receive this authority: they continue to require
// the inherited $/@/% receipt.
func resolveExactObjectRuntimeMutationRoute(
	ctx context.Context,
	runner tmuxCommandRunner,
	lookupEnv func(string) string,
) (runtimeMutationRoute, error) {
	return resolveInvocationRuntimeMutationRouteWithPolicy(ctx, runner, lookupEnv, "", true)
}

func resolveInvocationRuntimeMutationRouteWithPolicy(
	ctx context.Context,
	runner tmuxCommandRunner,
	lookupEnv func(string) string,
	paneID string,
	allowAppPIDOnly bool,
) (runtimeMutationRoute, error) {
	if runner == nil {
		return runtimeMutationRoute{}, errors.New("runtime mutation route requires a tmux runner")
	}
	anchorLookup := lookupEnv
	inheritedPaneID := ""
	if allowAppPIDOnly && lookupEnv != nil {
		// Exact resource selectors own the object target, so an unrelated
		// inherited TMUX_PANE or private child-process anchor cannot choose or
		// veto it. Preserve TMUX itself as physical socket/server-generation
		// evidence. The explicit paneID argument remains authoritative for a
		// generated intent that deliberately supplied command.routeAnchor.
		anchorLookup = func(key string) string {
			if key == "TMUX_PANE" || key == runtimeMutationAnchorPaneEnv {
				return ""
			}
			return lookupEnv(key)
		}
		inheritedPaneID = exactTmuxHandle(strings.TrimSpace(lookupEnv("TMUX_PANE")), "%")
	}
	paneID, err := resolveRuntimeMutationAnchorPane(anchorLookup, paneID)
	if err != nil {
		return runtimeMutationRoute{}, err
	}
	tmuxEnv := ""
	if lookupEnv != nil {
		tmuxEnv = strings.TrimSpace(lookupEnv("TMUX"))
	}
	if tmuxEnv == "" {
		route := defaultRuntimeMutationRoute()
		routed := explicitTmuxRunner{runner: runner, target: route.target}
		observed, err := routed.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
		if err != nil {
			if inttmux.IsNoServerFailure(err) {
				if paneID != "" {
					return runtimeMutationRoute{}, errors.New("runtime mutation route: explicit anchor server is unavailable")
				}
				return route, nil
			}
			return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: probe default logical socket: %w", err)
		}
		route.expectedSocketPath = filepath.Clean(strings.TrimSpace(string(observed)))
		if route.expectedSocketPath == "." {
			return runtimeMutationRoute{}, errors.New("runtime mutation route: default logical socket identity is empty")
		}
		appOwned, err := routed.Run(ctx, "tmux", "show-options", "-gqv", tmuxopts.AppGlobal)
		if err != nil || strings.TrimSpace(string(appOwned)) != "1" {
			return runtimeMutationRoute{}, errors.New("runtime mutation route: default logical socket is not app-owned")
		}
		logical, err := routed.Run(ctx, "tmux", "show-options", "-gqv", runtimeMutationSocketNameOption)
		if err != nil {
			return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: read default server logical socket marker: %w", err)
		}
		logicalName := strings.TrimSpace(string(logical))
		if logicalName == "" {
			return runtimeMutationRoute{}, missingRuntimeMutationMarker(defaultAppSocket)
		}
		nameTarget, err := tmuxSocketNameTarget(logicalName)
		if err != nil {
			return runtimeMutationRoute{}, errors.New("runtime mutation route: default server logical marker is invalid")
		}
		nameRunner := explicitTmuxRunner{runner: runner, target: nameTarget}
		byName, err := nameRunner.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
		if err != nil {
			return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: reobserve default server canonical -L %s: %w", nameTarget.value, err)
		}
		byNamePath := filepath.Clean(strings.TrimSpace(string(byName)))
		if byNamePath != route.expectedSocketPath {
			return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: canonical -L %s resolves to %q, default invocation is %q", nameTarget.value, byNamePath, route.expectedSocketPath)
		}
		physical := explicitTmuxRunner{runner: runner, target: explicitTmuxTarget{flag: "-S", value: route.expectedSocketPath}}
		pidOut, err := physical.Run(ctx, "tmux", "display-message", "-p", "-F", "#{pid}")
		pid := strings.TrimSpace(string(pidOut))
		parsedPID, pidErr := strconv.Atoi(pid)
		if err != nil || pidErr != nil || parsedPID <= 0 {
			return runtimeMutationRoute{}, errors.New("runtime mutation route: default app server generation is unreadable")
		}
		authority := &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: pid}
		if paneID != "" {
			authority, err = observeExplicitAppAnchorAuthority(ctx, physical, route.expectedSocketPath, pid, paneID)
			if err != nil {
				return runtimeMutationRoute{}, err
			}
		}
		return runtimeMutationRoute{
			target: nameTarget, expectedSocketPath: route.expectedSocketPath, socketName: nameTarget.value,
			authority: authority,
		}, nil
	}
	receipt, err := parseInheritedTmuxReceipt(tmuxEnv)
	if err != nil {
		return runtimeMutationRoute{}, err
	}
	pathTarget, err := tmuxSocketPathTarget(receipt.SocketPath)
	if err != nil {
		return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: inherited socket is not exact: %w", err)
	}
	pathRunner := explicitTmuxRunner{runner: runner, target: pathTarget}
	observed, err := pathRunner.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: reobserve inherited socket: %w", err)
	}
	observedPath := filepath.Clean(strings.TrimSpace(string(observed)))
	if observedPath != pathTarget.value {
		return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: inherited socket drifted: observed %q, expected %q", observedPath, pathTarget.value)
	}
	appOwnedOut, err := pathRunner.Run(ctx, "tmux", "show-options", "-gqv", tmuxopts.AppGlobal)
	if err != nil {
		return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: read app ownership marker: %w", err)
	}
	nameOut, err := pathRunner.Run(ctx, "tmux", "show-options", "-gqv", runtimeMutationSocketNameOption)
	if err != nil {
		return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: read app logical socket marker: %w", err)
	}
	appMarker := strings.TrimSpace(string(appOwnedOut))
	logicalMarker := strings.TrimSpace(string(nameOut))
	if appMarker == "" && logicalMarker == "" {
		standalonePaneID := paneID
		if standalonePaneID == "" && allowAppPIDOnly {
			// PID-only authority is app-owned only. A standalone invocation must
			// still prove its exact inherited Pane containment.
			standalonePaneID = inheritedPaneID
		}
		authority, err := observeInheritedRuntimeMutationAuthority(ctx, pathRunner, receipt, standalonePaneID, runtimeMutationRouteStandalone)
		if err != nil {
			return runtimeMutationRoute{}, err
		}
		return runtimeMutationRoute{
			target: pathTarget, expectedSocketPath: observedPath, socketName: defaultAppSocket, authority: authority,
		}, nil
	}
	if appMarker == "1" && logicalMarker == "" {
		return runtimeMutationRoute{}, missingRuntimeMutationMarker(observeMissingMarkerLogicalSocket(
			ctx, runner, pathTarget, observedPath, lookupEnv,
		))
	}
	if appMarker != "1" || logicalMarker == "" {
		return runtimeMutationRoute{}, errors.New("runtime mutation route: exact invocation server is not app-owned; partial or foreign ownership markers refuse standalone classification")
	}
	nameTarget, err := tmuxSocketNameTarget(logicalMarker)
	if err != nil {
		return runtimeMutationRoute{}, errors.New("runtime mutation route: exact invocation server has no app logical socket marker")
	}
	nameRunner := explicitTmuxRunner{runner: runner, target: nameTarget}
	byName, err := nameRunner.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: reobserve logical socket -L %s: %w", nameTarget.value, err)
	}
	byNamePath := filepath.Clean(strings.TrimSpace(string(byName)))
	if byNamePath != observedPath {
		return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: logical socket -L %s resolves to %q, invocation is %q", nameTarget.value, byNamePath, observedPath)
	}
	var authority *runtimeMutationRouteAuthority
	if paneID != "" || !allowAppPIDOnly {
		authority, err = observeInheritedRuntimeMutationAuthority(ctx, pathRunner, receipt, paneID, runtimeMutationRouteApp)
		if err != nil {
			return runtimeMutationRoute{}, err
		}
	} else {
		pidOut, pidErr := pathRunner.Run(ctx, "tmux", "display-message", "-p", "-F", "#{pid}")
		pid := strings.TrimSpace(string(pidOut))
		if pidErr != nil || pid != receipt.ServerPID {
			return runtimeMutationRoute{}, errors.New("runtime mutation route: inherited app server PID drifted")
		}
		authority = &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: pid}
	}
	return runtimeMutationRoute{
		target: nameTarget, expectedSocketPath: observedPath, socketName: nameTarget.value, authority: authority,
	}, nil
}

// observeMissingMarkerLogicalSocket finds a recovery-only logical name. It
// never grants mutation authority: each candidate must independently resolve
// through -L to the already observed absolute socket path, and the caller
// still returns the typed refusal unconditionally.
func observeMissingMarkerLogicalSocket(
	ctx context.Context,
	runner tmuxCommandRunner,
	target explicitTmuxTarget,
	expectedPath string,
	lookupEnv func(string) string,
) string {
	candidates := make([]string, 0, 3)
	if target.flag == "-L" {
		candidates = append(candidates, target.value)
	}
	if lookupEnv != nil {
		candidates = append(candidates, strings.TrimSpace(lookupEnv("PROJMUX_SOCKET")))
	}
	candidates = append(candidates, defaultAppSocket)
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		logical, err := tmuxSocketNameTarget(candidate)
		if err != nil {
			continue
		}
		out, err := (explicitTmuxRunner{runner: runner, target: logical}).Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
		if err == nil && filepath.Clean(strings.TrimSpace(string(out))) == expectedPath {
			return candidate
		}
	}
	return ""
}
