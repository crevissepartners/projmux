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
		return []byte(tmuxRowFormat("$1", "alpha", "uid:project", "") + "\n"), nil
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
	runner := &exactManagedStopRunner{physical: "/tmp/projmux-stop", logical: defaultAppSocket}
	target := managedRuntimeStopTarget{
		SessionID: "$1", SessionName: "alpha", RootKind: coremetadata.KindProject, RootUID: "uid:project",
		Route: runtimeMutationRoute{
			target: explicitTmuxTarget{flag: "-L", value: defaultAppSocket}, socketName: defaultAppSocket,
			expectedSocketPath: runner.physical,
			authority:          &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"},
		},
	}
	authorityReads := 0
	authoritative := func(_ context.Context, kind coremetadata.Kind, uid, session string) (bool, error) {
		authorityReads++
		return kind == coremetadata.KindProject && uid == "uid:project" && session == "alpha", nil
	}
	if err := executeManagedRuntimeStop(context.Background(), runner, target, authoritative); err != nil {
		t.Fatalf("executeManagedRuntimeStop() error = %v", err)
	}
	if !runner.killed || authorityReads != 2 {
		t.Fatalf("managed stop = killed %t, Registry reads %d; want one exact kill after two authority reads", runner.killed, authorityReads)
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
			target: explicitTmuxTarget{flag: "-L", value: defaultAppSocket}, socketName: defaultAppSocket,
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
