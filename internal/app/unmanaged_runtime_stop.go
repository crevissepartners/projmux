package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type unmanagedRuntimeStopObservation struct {
	sessionID   string
	sessionName string
	projectUID  string
	role        string
	ephemeral   string
}

// executeUnmanagedRuntimeStop is the exact pre-write authority boundary for
// human maintenance. A name selected while it was unowned is not authority:
// the exact app route and `$N` tuple are rebound immediately before kill, and
// any Project or ControlSession attribution refuses with zero writes.
func executeUnmanagedRuntimeStop(ctx context.Context, runner tmuxCommandRunner, lookupEnv func(string) string, sessionName string) (bool, error) {
	sessionName = strings.TrimSpace(sessionName)
	if runner == nil || sessionName == "" {
		return false, errors.New("unmanaged runtime stop requires a runner and session name")
	}
	route, err := resolveInvocationRuntimeMutationRoute(ctx, runner, lookupEnv)
	if err != nil {
		return false, err
	}
	observeTarget := route.target
	if route.expectedSocketPath != "" {
		observeTarget = tmuxTransport{Kind: tmuxSocketPath, Value: route.expectedSocketPath, Source: tmuxSocketPathSource}
	}
	routed := explicitTmuxRunner{runner: runner, target: observeTarget}
	observe := func(ctx context.Context) (unmanagedRuntimeStopObservation, bool, error) {
		out, err := routed.Run(ctx, "tmux", "list-sessions", "-F", tmuxRowFormat(
			"#{session_id}", "#{session_name}", "#{"+tmuxopts.ProjectUIDSession+"}",
			"#{"+tmuxopts.SessionRole+"}", "#{"+tmuxopts.EphemeralSession+"}"))
		if err != nil {
			if inttmux.IsNoServerFailure(err) {
				return unmanagedRuntimeStopObservation{}, false, nil
			}
			return unmanagedRuntimeStopObservation{}, false, err
		}
		var matches []unmanagedRuntimeStopObservation
		for _, row := range splitTmuxRows(string(out), 5) {
			if row[1] == sessionName {
				matches = append(matches, unmanagedRuntimeStopObservation{
					sessionID: row[0], sessionName: row[1], projectUID: row[2], role: row[3], ephemeral: row[4],
				})
			}
		}
		if len(matches) == 0 {
			return unmanagedRuntimeStopObservation{}, false, nil
		}
		if len(matches) != 1 || exactTmuxHandle(matches[0].sessionID, "$") == "" {
			return unmanagedRuntimeStopObservation{}, false, errors.New("unmanaged runtime stop observation is ambiguous")
		}
		observed := matches[0]
		if observed.projectUID != "" || observed.role == resourcegraph.ControlSessionRole {
			return unmanagedRuntimeStopObservation{}, false, errors.New("runtime session became Registry-managed; unmanaged kill refused")
		}
		if observed.role != "" {
			return unmanagedRuntimeStopObservation{}, false, fmt.Errorf("runtime session has unknown role %q; unmanaged kill refused", observed.role)
		}
		if observed.ephemeral != "" && observed.ephemeral != resourcegraph.EphemeralMarker {
			return unmanagedRuntimeStopObservation{}, false, errors.New("runtime session has malformed ephemeral attribution; unmanaged kill refused")
		}
		return observed, true, nil
	}
	observed, found, err := observe(ctx)
	if err != nil || !found {
		return false, err
	}
	mutationTarget := runtimeMutationTarget{
		Kind: "unmanaged-session", ID: observed.sessionID,
		UID: "unowned", Parent: observed.sessionName,
	}
	bindRuntimeMutationRouteTarget(&mutationTarget, route)
	action := newRuntimeMutation(1, mutationStopUnmanagedSession, mutationTarget)
	bindRuntimeMutationGuard(&action, "exact unowned/ephemeral session="+observed.sessionID+"/"+observed.sessionName)
	action.Operands = []string{"-t", observed.sessionID}
	if err := executeRuntimeMutationPlan(ctx, []runtimeMutationStep{{
		Action: action,
		TargetRouteGuard: func(ctx context.Context) error {
			return guardPrintedRuntimeMutationRoute(ctx, runner, route, action)
		},
		Reobserve: func(ctx context.Context) (bool, error) {
			if err := guardPrintedRuntimeMutationRoute(ctx, runner, route, action); err != nil {
				if inttmux.IsNoServerFailure(err) {
					return true, nil
				}
				return false, err
			}
			current, found, err := observe(ctx)
			if err != nil {
				return false, err
			}
			return !found || current.sessionID != observed.sessionID, nil
		},
		Guard: func(ctx context.Context) error {
			if err := guardPrintedRuntimeMutationRoute(ctx, runner, route, action); err != nil {
				return err
			}
			current, found, err := observe(ctx)
			if err != nil {
				return err
			}
			if !found || current != observed {
				return errors.New("unmanaged runtime identity drifted before stop")
			}
			return nil
		},
		Apply: func(ctx context.Context) error {
			_, err := runRuntimeMutationCommand(ctx, routed, action)
			return err
		},
	}}); err != nil {
		return false, err
	}
	return true, nil
}
