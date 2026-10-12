package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

func GuardResolvedRuntimeMutationRoute(ctx context.Context, runner TmuxCommandRunner, route RuntimeMutationRoute) error {
	return GuardResolvedRuntimeMutationRouteWithMarkerPolicy(ctx, runner, route, false, nil)
}

// guardResolvedRuntimeMutationRouteWithIdentity is the same guard, allowed to
// reuse an identity this transaction already proved against tmux. A nil cache,
// a first proof, or any differing key component reads tmux exactly as before.
func GuardResolvedRuntimeMutationRouteWithIdentity(
	ctx context.Context,
	runner TmuxCommandRunner,
	route RuntimeMutationRoute,
	identity *RuntimeRouteIdentityCache,
) error {
	return GuardResolvedRuntimeMutationRouteWithMarkerPolicy(ctx, runner, route, false, identity)
}

// guardResolvedRuntimeMutationRouteBeforeMarkerWrite preserves the physical
// socket, server-generation, and app-ownership authority of the normal route
// guard while allowing the logical marker to be absent for the one action that
// creates it. An already-present foreign marker is still drift. Callers must
// use guardResolvedRuntimeMutationRoute after the write to prove the effect.
func GuardResolvedRuntimeMutationRouteBeforeMarkerWrite(ctx context.Context, runner TmuxCommandRunner, route RuntimeMutationRoute, identity *RuntimeRouteIdentityCache) error {
	return GuardResolvedRuntimeMutationRouteWithMarkerPolicy(ctx, runner, route, true, identity)
}

// guardResolvedRuntimeMutationRouteWithMarkerPolicy proves the resolved route
// against the live server. identity may reuse the result of an identical proof
// already made in this transaction; the marker-write phase exception is never
// cacheable, because the logical marker it tolerates is ambiguous evidence.
func GuardResolvedRuntimeMutationRouteWithMarkerPolicy(
	ctx context.Context,
	runner TmuxCommandRunner,
	route RuntimeMutationRoute,
	allowMissingLogicalMarker bool,
	identity *RuntimeRouteIdentityCache,
) error {
	return GuardResolvedRuntimeMutationRouteObserved(ctx, runner, route, allowMissingLogicalMarker, identity, nil)
}

// guardResolvedRuntimeMutationRouteObserved is the resolved-route guard for a
// caller that has just read this proof's whole read set (#{socket_path}, the
// server generation, and the markers its class demands) through the exact
// physical socket this route names, in the same tmux invocation. The proof then
// takes those values instead of reading them a second time; a nil observation
// reads them itself, exactly as the plain guard does.
func GuardResolvedRuntimeMutationRouteObserved(
	ctx context.Context,
	runner TmuxCommandRunner,
	route RuntimeMutationRoute,
	allowMissingLogicalMarker bool,
	identity *RuntimeRouteIdentityCache,
	observed *ResolvedRouteObservation,
) error {
	if runner == nil || route.Target.Flag() == "" || route.Target.Value == "" {
		return errors.New("runtime mutation route is not exact")
	}
	key, cacheable := RuntimeRouteIdentityKeyForRoute(RouteIdentityScopeResolvedRoute, route)
	cacheable = cacheable && !allowMissingLogicalMarker
	if cacheable && identity.Reuse(key) {
		return nil
	}
	if err := ProveResolvedRuntimeMutationRouteObserved(ctx, runner, route, allowMissingLogicalMarker, observed); err != nil {
		identity.Invalidate("resolved-route-probe-error")
		return err
	}
	if cacheable {
		identity.Record(key)
	}
	return nil
}

// resolvedRouteObservation is one resolved-route proof's read set. Only the
// fields readResolvedRouteObservation reads for the route's authority are
// filled; the others stay empty and are never compared.
type ResolvedRouteObservation struct {
	Socket  string
	Pid     string
	App     string
	Logical string
}

// readResolvedRouteObservation reads one resolved-route proof's read set in
// one tmux invocation through routed: #{socket_path}, then the server
// generation when a generation was captured, then both ownership markers
// unless the authority class is unknown (that proof is refused right after the
// generation check, as it always was). The error is the sequence's; each
// caller reports it with the text of its own socket-path read, the sequence's
// first read.
func ReadResolvedRouteObservation(ctx context.Context, routed TmuxCommandRunner, authority *RuntimeMutationRouteAuthority) (ResolvedRouteObservation, error) {
	var observed ResolvedRouteObservation
	reads := []RouteRead{RouteReadSocketPath}
	fields := []*string{&observed.Socket}
	if authority != nil {
		reads = append(reads, RouteReadServerPID)
		fields = append(fields, &observed.Pid)
	}
	switch {
	case authority == nil, authority.Class == RuntimeMutationRouteApp,
		authority.Class == RuntimeMutationRouteStandalone, authority.Class == RuntimeMutationRouteStandaloneExplicit:
		reads = append(reads, RouteReadAppMarker, RouteReadSocketNameMarker)
		fields = append(fields, &observed.App, &observed.Logical)
	}
	sections, err := ReadTmuxSequence(ctx, routed, reads...)
	if err != nil {
		return ResolvedRouteObservation{}, err
	}
	for i, field := range fields {
		*field = sections[i]
	}
	return observed, nil
}

func ProveResolvedRuntimeMutationRouteObserved(ctx context.Context, runner TmuxCommandRunner, route RuntimeMutationRoute, allowMissingLogicalMarker bool, pre *ResolvedRouteObservation) error {
	// Once a physical socket has been observed, it is the execution authority.
	// Re-resolving the logical alias here would let an alias replacement make a
	// pre-observation report the effect from the wrong server.
	probeTarget := route.Target
	if route.ExpectedSocketPath != "" {
		probeTarget = TmuxTransport{Kind: TmuxSocketPath, Value: filepath.Clean(route.ExpectedSocketPath), Source: TmuxSocketPathSource}
	}
	routed := ExplicitTmuxRunner{Runner: runner, Target: probeTarget}
	if pre == nil || route.ExpectedSocketPath == "" {
		// The whole read set is one tmux invocation through routed. A failed
		// sequence reports the text of its first read, the socket path.
		read, err := ReadResolvedRouteObservation(ctx, routed, route.Authority)
		if err != nil {
			return fmt.Errorf("reobserve planned runtime socket: %w", err)
		}
		pre = &read
	}
	observed := filepath.Clean(strings.TrimSpace(pre.Socket))
	if route.ExpectedSocketPath != "" && observed != filepath.Clean(route.ExpectedSocketPath) {
		return fmt.Errorf("runtime socket drifted: observed %q, planned %q", observed, filepath.Clean(route.ExpectedSocketPath))
	}
	if route.Authority != nil {
		if (route.Authority.Class == RuntimeMutationRouteStandalone || route.Authority.Class == RuntimeMutationRouteStandaloneExplicit) &&
			(route.Target.Flag() != "-S" || route.Target.Value != route.ExpectedSocketPath) {
			return errors.New("planned standalone runtime route is not exact physical authority")
		}
		if strings.TrimSpace(pre.Pid) != route.Authority.ServerPID {
			return errors.New("planned runtime server generation drifted")
		}
		if route.Authority.Class == RuntimeMutationRouteStandalone || route.Authority.Class == RuntimeMutationRouteStandaloneExplicit {
			if strings.TrimSpace(pre.App) != "" || strings.TrimSpace(pre.Logical) != "" {
				return errors.New("planned standalone runtime route class drifted")
			}
			return nil
		}
		if route.Authority.Class != RuntimeMutationRouteApp {
			return errors.New("planned runtime route has an unknown authority class")
		}
	}
	if strings.TrimSpace(pre.App) != "1" {
		return errors.New("planned runtime socket is not app-owned")
	}
	logicalName := strings.TrimSpace(pre.Logical)
	if logicalName != route.SocketName && !(allowMissingLogicalMarker && logicalName == "") {
		return errors.New("planned runtime socket logical route marker drifted")
	}
	return nil
}

func GuardPrintedRuntimeMutationRoute(ctx context.Context, runner TmuxCommandRunner, route RuntimeMutationRoute, action PlannedRuntimeMutation) error {
	return GuardPrintedRuntimeMutationRouteWithMarkerPolicy(ctx, runner, route, action, false, nil)
}

func GuardPrintedRuntimeMutationRouteWithMarkerPolicy(
	ctx context.Context,
	runner TmuxCommandRunner,
	route RuntimeMutationRoute,
	action PlannedRuntimeMutation,
	allowMissingLogicalMarker bool,
	identity *RuntimeRouteIdentityCache,
) error {
	printed := strings.TrimSpace(action.Target.PhysicalSocket)
	if printed == RuntimeMutationSocketAbsentBeforeCreate {
		if route.ExpectedSocketPath != "" || (action.Verb != MutationCreateSession && action.Verb != MutationBootstrapControlSession) {
			return errors.New("printed absent-before-create socket disagrees with execution route")
		}
		if action.Target.RouteAuthority != "" || route.Authority != nil {
			return errors.New("printed absent-before-create action unexpectedly claims a server generation")
		}
	} else if filepath.Clean(printed) != filepath.Clean(route.ExpectedSocketPath) {
		return fmt.Errorf("printed physical socket %q disagrees with execution route %q", printed, route.ExpectedSocketPath)
	} else {
		if route.Authority == nil || action.Target.RouteAuthority == "" || action.Target.RouteAuthority != route.Authority.Printable() {
			return errors.New("printed runtime route authority disagrees with captured server generation")
		}
		printedRoute := route.Target.Flag() + "=" + route.Target.Value
		if action.Verb == MutationWriteRouteMarker {
			printedRoute = "-S=" + route.ExpectedSocketPath
		}
		if action.Target.Socket != printedRoute {
			return errors.New("printed runtime execution route disagrees with captured route")
		}
	}
	if allowMissingLogicalMarker {
		return GuardResolvedRuntimeMutationRouteBeforeMarkerWrite(ctx, runner, route, identity)
	}
	return GuardResolvedRuntimeMutationRouteWithIdentity(ctx, runner, route, identity)
}
