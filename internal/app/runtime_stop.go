package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type managedRuntimeStopTarget struct {
	SessionID   string
	SessionName string
	RootKind    coremetadata.Kind
	RootUID     string
	Route       runtimeMutationRoute
}

// managedRuntimeStopAuthority re-reads only Registry authority. Runtime
// identity is observed separately through the exact printed physical socket;
// accepting an ambient runtime view here would split one guard across two
// servers if a logical alias were retargeted between reads.
type managedRuntimeStopAuthority func(context.Context, coremetadata.Kind, string, string) (bool, error)

func executeManagedRuntimeStop(ctx context.Context, runner tmuxCommandRunner, target managedRuntimeStopTarget, authoritative managedRuntimeStopAuthority) error {
	if runner == nil || authoritative == nil || exactTmuxHandle(target.SessionID, "$") == "" || strings.TrimSpace(target.RootUID) == "" {
		return errors.New("managed runtime stop requires exact session, root UID, route, and Registry authority")
	}
	routed := explicitTmuxRunner{runner: runner, target: target.Route.target}
	mutationTarget := runtimeMutationTarget{
		Kind: string(target.RootKind), ID: target.SessionID, UID: target.RootUID,
		Parent: target.SessionName,
	}
	bindRuntimeMutationRouteTarget(&mutationTarget, target.Route)
	action := newRuntimeMutation(1, mutationStopManagedSession, mutationTarget)
	bindRuntimeMutationGuard(&action, "exact managed session="+target.SessionID+"/"+target.SessionName+";root="+string(target.RootKind)+"/"+target.RootUID)
	action.Operands = []string{"-t", target.SessionID}
	return executeRuntimeMutationPlan(ctx, []runtimeMutationStep{{
		Action: action,
		TargetRouteGuard: func(ctx context.Context) error {
			return guardPrintedRuntimeMutationRoute(ctx, runner, target.Route, action)
		},
		Reobserve: func(ctx context.Context) (bool, error) {
			if err := guardPrintedRuntimeMutationRoute(ctx, runner, target.Route, action); err != nil {
				if inttmux.IsNoServerFailure(err) {
					return true, nil
				}
				return false, err
			}
			live, err := observeExactManagedRuntimeStopTarget(ctx, runner, target)
			if err != nil {
				if inttmux.IsNoServerFailure(err) {
					return true, nil
				}
				return false, err
			}
			if !live {
				return true, nil
			}
			managed, err := authoritative(ctx, target.RootKind, target.RootUID, target.SessionName)
			if err != nil {
				return false, err
			}
			if !managed {
				return false, errors.New("managed runtime Registry authority disappeared before stop")
			}
			return false, nil
		},
		Guard: func(ctx context.Context) error {
			if err := guardPrintedRuntimeMutationRoute(ctx, runner, target.Route, action); err != nil {
				return err
			}
			live, err := observeExactManagedRuntimeStopTarget(ctx, runner, target)
			if err != nil {
				return err
			}
			if !live {
				return errors.New("managed session disappeared before stop")
			}
			managed, err := authoritative(ctx, target.RootKind, target.RootUID, target.SessionName)
			if err != nil {
				return err
			}
			if !managed {
				return errors.New("managed session Registry authority drifted before stop")
			}
			return nil
		},
		Apply: func(ctx context.Context) error {
			_, err := runRuntimeMutationCommand(ctx, routed, action)
			return err
		},
	}})
}

func managedRuntimeStopRegistryAuthority(load func() (coremetadata.Registry, error)) managedRuntimeStopAuthority {
	return func(_ context.Context, kind coremetadata.Kind, uid, sessionName string) (bool, error) {
		if load == nil {
			return false, errors.New("managed runtime Registry reader is not configured")
		}
		registry, err := load()
		if err != nil {
			return false, fmt.Errorf("re-read managed runtime Registry authority: %w", err)
		}
		switch kind {
		case coremetadata.KindProject:
			_, ok := registry.Project(uid)
			return ok, nil
		case coremetadata.KindControlSession:
			control, ok := registry.ControlSession(uid)
			return ok && control.Spec.Session == sessionName, nil
		default:
			return false, fmt.Errorf("managed runtime stop has unknown Registry root kind %q", kind)
		}
	}
}

func observeExactManagedRuntimeStopTarget(ctx context.Context, runner tmuxCommandRunner, target managedRuntimeStopTarget) (bool, error) {
	if target.Route.expectedSocketPath == "" {
		return false, errors.New("managed runtime stop has no physical socket authority")
	}
	exact := explicitTmuxRunner{runner: runner, target: explicitTmuxTarget{flag: "-S", value: filepath.Clean(target.Route.expectedSocketPath)}}
	out, err := exact.Run(ctx, "tmux", "list-sessions", "-F", tmuxRowFormat(
		"#{session_id}", "#{session_name}", "#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.SessionRole+"}"))
	if err != nil {
		return false, err
	}
	for _, row := range splitTmuxRows(string(out), 4) {
		if row[0] != target.SessionID {
			continue
		}
		if row[1] != target.SessionName {
			return false, errors.New("managed runtime session name drifted on exact physical route")
		}
		switch target.RootKind {
		case coremetadata.KindProject:
			if row[2] != target.RootUID || row[3] != "" {
				return false, errors.New("managed Project attribution drifted on exact physical route")
			}
		case coremetadata.KindControlSession:
			if row[2] != "" || row[3] != resourcegraph.ControlSessionRole {
				return false, errors.New("managed ControlSession attribution drifted on exact physical route")
			}
		default:
			return false, errors.New("managed runtime stop has unknown root kind")
		}
		return true, nil
	}
	return false, nil
}

func guardResolvedRuntimeMutationRoute(ctx context.Context, runner tmuxCommandRunner, route runtimeMutationRoute) error {
	if runner == nil || route.target.flag == "" || route.target.value == "" {
		return errors.New("runtime mutation route is not exact")
	}
	// Once a physical socket has been observed, it is the execution authority.
	// Re-resolving the logical alias here would let an alias replacement make a
	// pre-observation report the effect from the wrong server.
	probeTarget := route.target
	if route.expectedSocketPath != "" {
		probeTarget = explicitTmuxTarget{flag: "-S", value: filepath.Clean(route.expectedSocketPath)}
	}
	routed := explicitTmuxRunner{runner: runner, target: probeTarget}
	out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		return fmt.Errorf("reobserve planned runtime socket: %w", err)
	}
	observed := filepath.Clean(strings.TrimSpace(string(out)))
	if route.expectedSocketPath != "" && observed != filepath.Clean(route.expectedSocketPath) {
		return fmt.Errorf("runtime socket drifted: observed %q, planned %q", observed, filepath.Clean(route.expectedSocketPath))
	}
	if route.authority != nil {
		if (route.authority.Class == runtimeMutationRouteStandalone || route.authority.Class == runtimeMutationRouteStandaloneExplicit) &&
			(route.target.flag != "-S" || route.target.value != route.expectedSocketPath) {
			return errors.New("planned standalone runtime route is not exact physical authority")
		}
		pidOut, err := routed.Run(ctx, "tmux", "display-message", "-p", "-F", "#{pid}")
		if err != nil || strings.TrimSpace(string(pidOut)) != route.authority.ServerPID {
			return errors.New("planned runtime server generation drifted")
		}
		if route.authority.Class == runtimeMutationRouteStandalone || route.authority.Class == runtimeMutationRouteStandaloneExplicit {
			appOwned, appErr := routed.Run(ctx, "tmux", "show-options", "-gqv", tmuxopts.AppGlobal)
			logical, logicalErr := routed.Run(ctx, "tmux", "show-options", "-gqv", runtimeMutationSocketNameOption)
			if appErr != nil || logicalErr != nil || strings.TrimSpace(string(appOwned)) != "" || strings.TrimSpace(string(logical)) != "" {
				return errors.New("planned standalone runtime route class drifted")
			}
			return nil
		}
		if route.authority.Class != runtimeMutationRouteApp {
			return errors.New("planned runtime route has an unknown authority class")
		}
	}
	appOwned, err := routed.Run(ctx, "tmux", "show-options", "-gqv", tmuxopts.AppGlobal)
	if err != nil || strings.TrimSpace(string(appOwned)) != "1" {
		return errors.New("planned runtime socket is not app-owned")
	}
	logical, err := routed.Run(ctx, "tmux", "show-options", "-gqv", runtimeMutationSocketNameOption)
	if err != nil || strings.TrimSpace(string(logical)) != route.socketName {
		return errors.New("planned runtime socket logical route marker drifted")
	}
	return nil
}

func guardPrintedRuntimeMutationRoute(ctx context.Context, runner tmuxCommandRunner, route runtimeMutationRoute, action plannedRuntimeMutation) error {
	printed := strings.TrimSpace(action.Target.PhysicalSocket)
	if printed == runtimeMutationSocketAbsentBeforeCreate {
		if route.expectedSocketPath != "" || (action.Verb != mutationCreateSession && action.Verb != mutationBootstrapControlSession) {
			return errors.New("printed absent-before-create socket disagrees with execution route")
		}
		if action.Target.RouteAuthority != "" || route.authority != nil {
			return errors.New("printed absent-before-create action unexpectedly claims a server generation")
		}
	} else if filepath.Clean(printed) != filepath.Clean(route.expectedSocketPath) {
		return fmt.Errorf("printed physical socket %q disagrees with execution route %q", printed, route.expectedSocketPath)
	} else {
		if route.authority == nil || action.Target.RouteAuthority == "" || action.Target.RouteAuthority != route.authority.printable() {
			return errors.New("printed runtime route authority disagrees with captured server generation")
		}
		printedRoute := route.target.flag + "=" + route.target.value
		if action.Verb == mutationWriteRouteMarker {
			printedRoute = "-S=" + route.expectedSocketPath
		}
		if action.Target.Socket != printedRoute {
			return errors.New("printed runtime execution route disagrees with captured route")
		}
	}
	return guardResolvedRuntimeMutationRoute(ctx, runner, route)
}
