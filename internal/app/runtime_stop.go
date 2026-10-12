package app

import (
	"context"
	"errors"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

type managedRuntimeStopTarget = mutationruntime.ManagedRuntimeStopTarget
type managedRuntimeStopAuthority = mutationruntime.ManagedRuntimeStopAuthority

func stopServiceStore(s *resourceStore) *mutationruntime.StopStore {
	if s == nil {
		return nil
	}
	return &mutationruntime.StopStore{Load: s.load, Update: s.update, Mutator: s.mutator, MapError: MapMetadataError, WrapError: wrapProjectLifecycleError}
}
func executeManagedRuntimeStop(ctx context.Context, runner tmuxCommandRunner, target managedRuntimeStopTarget, authority managedRuntimeStopAuthority, s *resourceStore) error {
	return mutationruntime.ExecuteManagedRuntimeStop(ctx, runner, target, authority, stopServiceStore(s))
}

var managedRuntimeStopRegistryAuthority = mutationruntime.ManagedRuntimeStopRegistryAuthority

// guardPrintedRuntimeMutationRouteBeforeMarkerWrite is deliberately limited to
// the route-marker verb. It keeps the printable/execution tuple checks exact,
// then applies the only valid phase exception: the desired logical marker may
// not exist until this action executes.
func guardPrintedRuntimeMutationRouteBeforeMarkerWrite(ctx context.Context, runner tmuxCommandRunner, route runtimeMutationRoute, action plannedRuntimeMutation) error {
	if action.Verb != mutationWriteRouteMarker {
		return errors.New("pre-marker route guard requires a write-route-marker action")
	}
	return guardPrintedRuntimeMutationRouteWithMarkerPolicy(ctx, runner, route, action, true, nil)
}
