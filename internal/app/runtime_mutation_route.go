package app

import mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

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

var bindRuntimeMutationRouteTarget = mutationruntime.BindRuntimeMutationRouteTarget

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
	sessionID, windowID, err := observeRuntimeMutationAnchorRow(
		ctx, routed, explicitRuntimeMutationAnchorScope, expectedSocketPath, serverPID, paneID)
	if err != nil {
		return nil, err
	}
	return &runtimeMutationRouteAuthority{
		Class: runtimeMutationRouteApp, ServerPID: serverPID,
		SessionID: sessionID, WindowID: windowID, PaneID: paneID,
	}, nil
}

func defaultRuntimeMutationRoute() runtimeMutationRoute {
	return runtimeMutationRoute{
		Target:     tmuxTransport{Kind: tmuxSocketName, Value: defaultAppSocket, Source: tmuxSocketNameSource},
		SocketName: defaultAppSocket,
	}
}

// guardRuntimeMutationServerOwnership proves that an already-running server is
// the app-owned server declared by the logical route. A matching socket path is
// not ownership evidence: a foreign server can be cloned onto the same -L name.
var guardRuntimeMutationServerOwnership = mutationruntime.GuardRuntimeMutationServerOwnership

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
		routed := explicitTmuxRunner{Runner: runner, Target: route.Target}
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
		route.ExpectedSocketPath = filepath.Clean(strings.TrimSpace(string(observed)))
		if route.ExpectedSocketPath == "." {
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
		nameRunner := explicitTmuxRunner{Runner: runner, Target: nameTarget}
		byName, err := nameRunner.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
		if err != nil {
			return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: reobserve default server canonical -L %s: %w", nameTarget.Value, err)
		}
		byNamePath := filepath.Clean(strings.TrimSpace(string(byName)))
		if byNamePath != route.ExpectedSocketPath {
			return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: canonical -L %s resolves to %q, default invocation is %q", nameTarget.Value, byNamePath, route.ExpectedSocketPath)
		}
		physical := explicitTmuxRunner{Runner: runner, Target: tmuxTransport{Kind: tmuxSocketPath, Value: route.ExpectedSocketPath, Source: tmuxSocketPathSource}}
		pidOut, err := physical.Run(ctx, "tmux", "display-message", "-p", "-F", "#{pid}")
		pid := strings.TrimSpace(string(pidOut))
		parsedPID, pidErr := strconv.Atoi(pid)
		if err != nil || pidErr != nil || parsedPID <= 0 {
			return runtimeMutationRoute{}, errors.New("runtime mutation route: default app server generation is unreadable")
		}
		authority := &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: pid}
		if paneID != "" {
			authority, err = observeExplicitAppAnchorAuthority(ctx, physical, route.ExpectedSocketPath, pid, paneID)
			if err != nil {
				return runtimeMutationRoute{}, err
			}
		}
		return runtimeMutationRoute{
			Target: nameTarget, ExpectedSocketPath: route.ExpectedSocketPath, SocketName: nameTarget.Value,
			Authority: authority,
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
	pathRunner := explicitTmuxRunner{Runner: runner, Target: pathTarget}
	observed, err := pathRunner.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: reobserve inherited socket: %w", err)
	}
	observedPath := filepath.Clean(strings.TrimSpace(string(observed)))
	if observedPath != pathTarget.Value {
		return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: inherited socket drifted: observed %q, expected %q", observedPath, pathTarget.Value)
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
			Target: pathTarget, ExpectedSocketPath: observedPath, SocketName: defaultAppSocket, Authority: authority,
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
	nameRunner := explicitTmuxRunner{Runner: runner, Target: nameTarget}
	byName, err := nameRunner.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: reobserve logical socket -L %s: %w", nameTarget.Value, err)
	}
	byNamePath := filepath.Clean(strings.TrimSpace(string(byName)))
	if byNamePath != observedPath {
		return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: logical socket -L %s resolves to %q, invocation is %q", nameTarget.Value, byNamePath, observedPath)
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
		Target: nameTarget, ExpectedSocketPath: observedPath, SocketName: nameTarget.Value, Authority: authority,
	}, nil
}
