package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// runtimeMutationSocketNameOption records the logical -L name that config
// apply used for one app-owned server. #{socket_path} alone is insufficient:
// an arbitrary -S path and an -L name may share a basename, while only the
// latter can preserve the name-only PROJMUX_SOCKET hook contract.
const runtimeMutationSocketNameOption = "@projmux_socket_name"

type runtimeMutationRoute struct {
	target             explicitTmuxTarget
	expectedSocketPath string
	socketName         string
}

func defaultRuntimeMutationRoute() runtimeMutationRoute {
	return runtimeMutationRoute{
		target:     explicitTmuxTarget{flag: "-L", value: defaultAppSocket},
		socketName: defaultAppSocket,
	}
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
	if _, err := tmuxSocketNameTarget(logicalName); err != nil {
		return errors.New("runtime mutation route: exact server has no app logical socket marker")
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
	if runner == nil {
		return runtimeMutationRoute{}, errors.New("runtime mutation route requires a tmux runner")
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
		nameTarget, err := tmuxSocketNameTarget(strings.TrimSpace(string(logical)))
		if err != nil {
			return runtimeMutationRoute{}, errors.New("runtime mutation route: default server has no app logical socket marker")
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
		return runtimeMutationRoute{target: nameTarget, expectedSocketPath: route.expectedSocketPath, socketName: nameTarget.value}, nil
	}
	inherited, _, _ := strings.Cut(tmuxEnv, ",")
	pathTarget, err := tmuxSocketPathTarget(inherited)
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
	if strings.TrimSpace(string(appOwnedOut)) != "1" {
		return runtimeMutationRoute{}, errors.New("runtime mutation route: exact invocation server is not app-owned")
	}
	nameOut, err := pathRunner.Run(ctx, "tmux", "show-options", "-gqv", runtimeMutationSocketNameOption)
	if err != nil {
		return runtimeMutationRoute{}, fmt.Errorf("runtime mutation route: read app logical socket marker: %w", err)
	}
	nameTarget, err := tmuxSocketNameTarget(strings.TrimSpace(string(nameOut)))
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
	return runtimeMutationRoute{target: nameTarget, expectedSocketPath: observedPath, socketName: nameTarget.value}, nil
}
