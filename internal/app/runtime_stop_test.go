package app

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type exactManagedStopRunner struct {
	physical string
	logical  string
	rootUID  string
	killed   bool
	calls    []recordedTmuxCall
}

func (r *exactManagedStopRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recordedTmuxCall{name: name, args: slices.Clone(args)})
	if name != "tmux" || len(args) < 3 || args[0] != "-S" || args[1] != r.physical {
		return nil, fmt.Errorf("managed stop escaped printed physical route: %s %v", name, args)
	}
	if r.killed {
		return nil, appTypedCommandFailure{inttmux.CommandFailure{
			Kind: inttmux.CommandFailureExit, Stderr: "no server running on " + r.physical,
		}}
	}
	switch args[2] {
	case "display-message":
		if args[len(args)-1] == "#{pid}" {
			return []byte("4242\n"), nil
		}
		return []byte(r.physical + "\n"), nil
	case "show-options":
		switch args[len(args)-1] {
		case tmuxopts.AppGlobal:
			return []byte("1\n"), nil
		case runtimeMutationSocketNameOption:
			return []byte(r.logical + "\n"), nil
		}
	case "list-sessions":
		rootUID := r.rootUID
		if rootUID == "" {
			rootUID = "uid:project"
		}
		return []byte(tmuxRowFormat("$1", "alpha", rootUID, "") + "\n"), nil
	case "kill-session":
		if flagValue(args[3:], "-t") != "$1" {
			return nil, fmt.Errorf("managed stop targeted %v", args)
		}
		r.killed = true
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected managed stop command: %v", args)
}

func TestManagedRuntimeStopUsesOnePrintedPhysicalObservationAndRegistryAuthority(t *testing.T) {
	store := freshStartFixtureStore(t)
	before := store.snapshot()
	runner := &exactManagedStopRunner{physical: "/tmp/projmux-stop", logical: defaultAppSocket, rootUID: "prj-alpha"}
	target := managedRuntimeStopTarget{
		SessionID: "$1", SessionName: "alpha", RootKind: coremetadata.KindProject, RootUID: "prj-alpha",
		Route: runtimeMutationRoute{
			target: tmuxTransport{Kind: tmuxSocketName, Value: defaultAppSocket, Source: tmuxSocketNameSource}, socketName: defaultAppSocket,
			expectedSocketPath: runner.physical,
			authority:          &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"},
		},
	}
	authorityReads := 0
	registryAuthority := managedRuntimeStopRegistryAuthority(store.store().snapshot)
	authoritative := func(ctx context.Context, kind coremetadata.Kind, uid, session string) (bool, error) {
		authorityReads++
		return registryAuthority(ctx, kind, uid, session)
	}
	if err := executeManagedRuntimeStop(context.Background(), runner, target, authoritative); err != nil {
		t.Fatalf("executeManagedRuntimeStop() error = %v", err)
	}
	if !runner.killed || authorityReads != 2 {
		t.Fatalf("managed stop = killed %t, Registry reads %d; want one exact kill after two authority reads", runner.killed, authorityReads)
	}
	if store.writes != 0 || store.snapshot() != before {
		t.Fatalf("managed stop changed desired Project graph: writes=%d", store.writes)
	}
	for _, call := range runner.calls {
		if len(call.args) < 2 || call.args[0] != "-S" || call.args[1] != runner.physical {
			t.Fatalf("managed stop mixed logical/ambient route: %#v", runner.calls)
		}
	}
}

func TestManagedRuntimeStopRegistryAuthorityDriftRefusesBeforeWrite(t *testing.T) {
	runner := &exactManagedStopRunner{physical: "/tmp/projmux-stop", logical: defaultAppSocket}
	target := managedRuntimeStopTarget{
		SessionID: "$1", SessionName: "alpha", RootKind: coremetadata.KindProject, RootUID: "uid:project",
		Route: runtimeMutationRoute{
			target: tmuxTransport{Kind: tmuxSocketName, Value: defaultAppSocket, Source: tmuxSocketNameSource}, socketName: defaultAppSocket,
			expectedSocketPath: runner.physical,
			authority:          &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"},
		},
	}
	err := executeManagedRuntimeStop(context.Background(), runner, target,
		func(context.Context, coremetadata.Kind, string, string) (bool, error) { return false, nil })
	if err == nil || !strings.Contains(err.Error(), "Registry authority disappeared") || runner.killed {
		t.Fatalf("Registry authority drift = killed %t, err %v; want residual refusal and zero kill", runner.killed, err)
	}
}

func TestManagedRuntimeStopAuthorityRejectsZeroWindowNoWriteCell(t *testing.T) {
	t.Parallel()
	store := freshStartFixtureStore(t)
	mutator := store.mutator()
	for _, window := range store.registry.WindowsOf("prj-alpha") {
		if err := mutator.DeleteWindow(&store.registry, window.Metadata.UID); err != nil {
			t.Fatal(err)
		}
	}
	authoritative := managedRuntimeStopRegistryAuthority(store.store().snapshot)
	ok, err := authoritative(context.Background(), coremetadata.KindProject, "prj-alpha", "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("zero-Window Project authorized a runtime stop despite the table's no-write cell")
	}
}

func TestProjectStopSurfaceExecutorMatchesLifecycleTableAndGenericCopyStaysGeneric(t *testing.T) {
	t.Parallel()
	decision := coremetadata.DecideProjectLifecycle(coremetadata.ProjectLifecycleRetainedWindows,
		coremetadata.ProjectLifecycleStop, coremetadata.ProjectLifecyclePreconditions{})
	if err := requireProjectLifecyclePlan(decision, coremetadata.ProjectLifecycleOperationStop,
		coremetadata.ProjectUIDPreserved, coremetadata.ProjectDescendantUIDsPreserved,
		coremetadata.ProjectStartupWriteStopRuntime); err != nil {
		t.Fatal(err)
	}
	var projectSurface runtimeMutationSurface
	for _, surface := range runtimeMutationSurfaces {
		if surface.ID == "catalog.project-sidebar.runtime.stop" {
			projectSurface = surface
			break
		}
	}
	if projectSurface.Handler != "executeManagedRuntimeStop" || projectSurface.PlanVerb != string(mutationStopManagedSession) ||
		!strings.Contains(projectSurface.Guard, "Stop table cell") || !strings.Contains(projectSurface.Effect, "Project UID") ||
		!strings.Contains(projectSurface.Effect, "desired Window/Pane topology unchanged") {
		t.Fatalf("Project Stop production surface diverged from lifecycle table: %+v", projectSurface)
	}
	generic, ok := keyBindingActionByID(defaultKeyBindingCatalog(), "SessionPopup:KillSession")
	if !ok {
		t.Fatal("generic Session stop action missing")
	}
	if strings.Contains(generic.Description, "Project UID") || strings.Contains(keyBindingDisplayName(generic), "UID/topology") {
		t.Fatalf("generic Session stop copy incorrectly claims Project identity semantics: label=%q description=%q",
			keyBindingDisplayName(generic), generic.Description)
	}
	if !strings.Contains(generic.Description, "Stop only") || !strings.Contains(generic.Description, "managed Registry identity") ||
		!strings.Contains(generic.Description, "desired topology") {
		t.Fatalf("generic Session stop copy does not state runtime-only managed-identity preservation: label=%q description=%q",
			keyBindingDisplayName(generic), generic.Description)
	}
}
