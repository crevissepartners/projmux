package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/receipt"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// ResolveProjectStopRoute never accepts inherited invocation evidence. The
// default route proves app ownership; an explicit route captures the server
// generation and relies on the managed stop's exact Project/session proof.
func ResolveProjectStopRoute(ctx context.Context, runner TmuxCommandRunner, socket string) (RuntimeMutationRoute, error) {
	if socket == "" {
		return ResolveExistingRuntimeMutationRoute(ctx, runner, TmuxTransport{Kind: TmuxSocketName, Value: DefaultAppSocket, Source: TmuxSocketNameSource}, nil)
	}
	if !filepath.IsAbs(socket) {
		return RuntimeMutationRoute{}, errors.New("project stop: SocketPath must be absolute")
	}
	target := TmuxTransport{Kind: TmuxSocketPath, Value: filepath.Clean(socket), Source: TmuxSocketPathSource}
	routed := ExplicitTmuxRunner{Runner: runner, Target: target}
	out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-F", TmuxRowFormat("#{socket_path}", "#{pid}"))
	if err != nil {
		return RuntimeMutationRoute{}, err
	}
	rows := SplitTmuxRows(string(out), 2)
	if len(rows) != 1 || filepath.Clean(rows[0][0]) != target.Value {
		return RuntimeMutationRoute{}, errors.New("project stop socket identity drifted")
	}
	pid, err := strconv.Atoi(rows[0][1])
	if err != nil || pid <= 0 {
		return RuntimeMutationRoute{}, errors.New("project stop server PID is unreadable")
	}
	app, err := routed.Run(ctx, "tmux", "show-options", "-gqv", tmuxopts.AppGlobal)
	if err != nil {
		return RuntimeMutationRoute{}, err
	}
	logical, err := routed.Run(ctx, "tmux", "show-options", "-gqv", RuntimeMutationSocketNameOption)
	if err != nil {
		return RuntimeMutationRoute{}, err
	}
	route := RuntimeMutationRoute{Target: target, ExpectedSocketPath: target.Value, SocketName: DefaultAppSocket, Authority: &RuntimeMutationRouteAuthority{Class: RuntimeMutationRouteStandaloneExplicit, ServerPID: rows[0][1]}}
	if strings.TrimSpace(string(app)) == "1" && strings.TrimSpace(string(logical)) != "" {
		route.SocketName = strings.TrimSpace(string(logical))
		route.Authority.Class = RuntimeMutationRouteApp
	} else if strings.TrimSpace(string(app)) != "" || strings.TrimSpace(string(logical)) != "" {
		return RuntimeMutationRoute{}, errors.New("project stop socket has partial or foreign ownership markers")
	}
	if err := GuardResolvedRuntimeMutationRoute(ctx, runner, route); err != nil {
		return RuntimeMutationRoute{}, err
	}
	return route, nil
}

// FindProjectStopTarget observes through the one proved physical socket. A
// same-name session is not authority: its exact handle and Project UID must be
// proved again by ExecuteManagedRuntimeStop before any evidence or kill.
func FindProjectStopTarget(ctx context.Context, runner TmuxCommandRunner, route RuntimeMutationRoute, project metadata.Project, sessionName string) (ManagedRuntimeStopTarget, bool, error) {
	if err := GuardResolvedRuntimeMutationRoute(ctx, runner, route); err != nil {
		return ManagedRuntimeStopTarget{}, false, err
	}
	routed := ExplicitTmuxRunner{Runner: runner, Target: TmuxTransport{Kind: TmuxSocketPath, Value: route.ExpectedSocketPath, Source: TmuxSocketPathSource}}
	out, err := routed.Run(ctx, "tmux", "list-sessions", "-F", TmuxRowFormat("#{session_id}", "#{session_name}"))
	if err != nil {
		if tmux.IsNoServerFailure(err) {
			return ManagedRuntimeStopTarget{}, false, nil
		}
		return ManagedRuntimeStopTarget{}, false, err
	}
	for _, row := range SplitTmuxRows(string(out), 2) {
		if row[1] == sessionName {
			if ExactTmuxHandle(row[0], "$") == "" {
				return ManagedRuntimeStopTarget{}, false, errors.New("project stop session handle is invalid")
			}
			// Reuse the CLI/sidebar containment selector, including a stored
			// session projection that disagrees with the observed session name.
			projectedName := ""
			if project.Status.Session != nil {
				projectedName = project.Status.Session.Name
			}
			view := registryview.View{Rows: []registryview.Row{{Section: registryview.SectionProjects, Kind: registryview.RowKindProject, UID: project.Metadata.UID, Root: project.Spec.Root, SessionName: projectedName, Runtime: &resourcegraph.RuntimeRef{ID: row[0], Name: row[1]}}}}
			target, ok := ResolveManagedProjectStopTarget(view, "uid:"+project.Metadata.UID, sessionName)
			if !ok {
				return ManagedRuntimeStopTarget{}, false, errors.New("switch managed runtime stop: exact Project UID/session containment is unknown; nothing was changed")
			}
			target.Route = route
			return target, true, nil
		}
	}
	return ManagedRuntimeStopTarget{}, false, nil
}

type StopProjectResult struct {
	Receipt       receipt.OperationReceipt
	Interruptions []ProjectStopInterruption
	OperationID   string
	Stopped       bool
}

// StopProject admits cancellation before the evidence transaction, then
// completes execution and compensation independently of the caller's context.
func StopProject(ctx context.Context, runner TmuxCommandRunner, target ManagedRuntimeStopTarget, store *StopStore, project metadata.Project) (StopProjectResult, error) {
	result := StopProjectResult{}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if store == nil {
		return result, errors.New("project stop Registry store is not configured")
	}
	execution := *store
	execution.Interruptions = func(op string, items []ProjectStopInterruption) {
		result.OperationID = op
		result.Interruptions = append([]ProjectStopInterruption{}, items...)
	}
	err := ExecuteManagedRuntimeStop(context.WithoutCancel(ctx), runner, target, ManagedRuntimeStopRegistryAuthority(store.Load), &execution)
	if err != nil {
		return result, err
	}
	result.Stopped = true
	result.Receipt = receipt.NewReceipt(receipt.OperationStopProject, receipt.ReceiptTarget{Kind: "Project", UID: project.Metadata.UID, Name: project.Metadata.Name}, receipt.ReceiptEffects{Identity: receipt.IdentityUnchanged, Address: receipt.AddressUnchanged, Topology: receipt.TopologyUnchanged, DesiredState: receipt.DesiredStateUnchanged, Runtime: receipt.RuntimeStopped, Focus: receipt.FocusUnchanged})
	result.Receipt.Add("Project", project.Metadata.UID, project.Metadata.Name, receipt.ActionStopped)
	return result, nil
}

func ProjectNotLive(project metadata.Project) error {
	return fmt.Errorf("stop project: project/%s has no live persistent session; nothing was changed", project.Metadata.Name)
}
