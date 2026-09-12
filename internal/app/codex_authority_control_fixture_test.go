package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// This runner supplies only read-only tmux facts, preserving the production
// alias -> canonical logical name -> exact physical socket/PID proof.
type installedControlRouteFixture struct {
	calls           [][]string
	epoch           string
	canonicalSocket string
}

func (fixture *installedControlRouteFixture) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	fixture.calls = append(fixture.calls, append([]string(nil), args...))
	if name != "tmux" || len(args) < 3 {
		return nil, errors.New("unexpected fixture command")
	}
	key := strings.Join(args, " ")
	switch key {
	case "-L projmux display-message -p -F #{socket_path}":
		return []byte("/private/tmux/cp1-control\n"), nil
	case "-L projmux show-options -gqv " + tmuxopts.AppGlobal:
		return []byte("1\n"), nil
	case "-L projmux show-options -gqv " + runtimeMutationSocketNameOption:
		return []byte("cp1-control\n"), nil
	case "-L cp1-control display-message -p -F #{socket_path}":
		return []byte(fixture.canonicalSocket + "\n"), nil
	case "-S /private/tmux/cp1-control display-message -p -F #{pid}":
		return []byte("4242\n"), nil
	}
	if args[0] != "-L" || args[1] != "cp1-control" {
		return nil, errors.New("live binding escaped canonical fixture route")
	}
	switch args[2] {
	case "list-panes":
		return []byte("pan-alpha-codex" + tmuxRowSepFormat + "%7\n"), nil
	case "display-message":
		return []byte(strings.Join([]string{"%7", "pan-alpha-codex", "thread-1", codexAuthorityControlPlane, fixture.epoch, "ready"}, tmuxRowSepFormat) + "\n"), nil
	}
	return nil, errors.New("unexpected fixture mutation")
}

func installedRecoveryControlFixture(t *testing.T) (*agentCommand, *fakeResourceStore, *installedControlRouteFixture, *[]agentControlRequest) {
	t.Helper()
	fixtureCommand, store, _ := exactControlCLICommand(t)
	agent, _ := store.registry.Agent(phase6CLIIdentity().AgentUID)
	agent.Status.SessionRef.Codex.SessionID = ""
	command := newAgentCommand()
	command.loadRegistry, command.store = fixtureCommand.loadRegistry, fixtureCommand.store
	runner := &installedControlRouteFixture{epoch: "9589-1", canonicalSocket: "/private/tmux/cp1-control"}
	command.controlRunner = runner
	command.controlRoute = func(ctx context.Context) (runtimeMutationRoute, error) {
		route, err := resolveExactObjectRuntimeMutationRoute(ctx, runner, func(string) string { return "" })
		if err == nil && (route.target.Flag() != "-L" || route.target.Value != "cp1-control" || route.expectedSocketPath != "/private/tmux/cp1-control" || route.authority == nil || route.authority.ServerPID != "4242") {
			t.Fatal("default alias did not prove the exact canonical route")
		}
		return route, err
	}
	state := t.TempDir()
	command.controlPaths = func() (config.Paths, error) { return config.Paths{StateDir: state}, nil }
	var requests []agentControlRequest
	command.controlCall = func(_ context.Context, _ string, endpoint coremetadata.CodexEndpointRef, identity codexLifecycleIdentity, request agentControlRequest) (agentControlResponse, error) {
		current, _ := store.registry.Pane(phase6CLIIdentity().PaneUID)
		if !endpoint.Same(current.Status.Activation.Codex.Authority.Endpoint()) || identity != phase6CLIIdentity() {
			t.Fatal("control transport lost exact current endpoint/identity")
		}
		requests = append(requests, request)
		return agentControlResponse{OK: true, Availability: agentControlAvailability{Start: true}, ThreadID: "thread-1", TurnID: "fixture-turn"}, nil
	}
	return command, store, runner, &requests
}

func TestInstalledRecoveryReadinessReachesProductionControlThroughCanonicalAlias(t *testing.T) {
	command, store, runner, requests := installedRecoveryControlFixture(t)
	var retired *coremetadata.CodexAuthorityRef
	for row, generation := range []string{"codex-0.151.0", "codex-0.154.0"} {
		// Supplied deterministic Registry/transport facts represent current authority;
		// this fixture does not run a manager, retag a real client, or create a thread.
		agent, _ := store.registry.Agent(phase6CLIIdentity().AgentUID)
		pane, _ := store.registry.Pane(phase6CLIIdentity().PaneUID)
		endpoint := phase6CLIEndpoint()
		endpoint.EndpointGenerationID = generation
		agent.Status.SessionRef.Codex.Endpoint = &endpoint
		pane.Status.Activation.Codex.Authority.EndpointGenerationID = generation
		pane.Status.Activation.Codex.Authority.ConnectionEpoch = uint64(row + 1)
		runner.epoch = fmt.Sprintf("9589-%d", row+1)
		out := recoveryRegistryObservation(store.registry, agent.Metadata.UID, generation, retired)
		before := len(*requests)
		out = readInstalledRecoveryControl(command, out, row > 0)
		if out.Stage != "ready" || len(runner.calls) == 0 || len(*requests) != before+1 || (*requests)[before].Operation != agentControlOpStatus {
			t.Fatalf("readiness refused before intended control consumer: stage=%s code=%s tmuxReads=%d controlCalls=%d", out.Stage, out.Error, len(runner.calls), len(*requests))
		}
		if out.Identity.Session != "" || out.Identity.Thread != "thread-1" || out.Identity.ControlEpoch != runner.epoch {
			t.Fatal("optional session or current control identity was synthesized/lost")
		}
		// Exercise the public CLI handler used by the later installed matrix input.
		// Only this fake control transport receives the synthetic unit-test text.
		if _, _, err := runRoute(t, command, "turn", "start", "uid:"+agent.Metadata.UID, "--", "synthetic fixture input"); err != nil {
			t.Fatal(err)
		}
		if len(*requests) != before+2 || (*requests)[before+1].Operation != agentControlOpStart || (*requests)[before+1].Epoch != runner.epoch || (*requests)[before+1].Identity != phase6CLIIdentity() {
			t.Fatal("installed turn spelling did not consume the exact current route/epoch")
		}
		updated, _ := store.registry.Agent(agent.Metadata.UID)
		if updated.Status.SessionRef.Codex.SessionID != "" || updated.Status.SessionRef.Codex.ThreadID != "thread-1" {
			t.Fatal("turn command changed the optional session identity")
		}
		previous := *pane.Status.Activation.Codex.Authority
		retired = &previous
	}
	for _, call := range runner.calls {
		for _, mutation := range []string{"new-session", "split-window", "set-option", "kill-server", "send-keys"} {
			if slices.Contains(call, mutation) {
				t.Fatal("readiness routing mutated tmux")
			}
		}
	}
}

func TestInstalledRecoveryReadinessAndTurnRefuseCanonicalAliasDriftBeforeControl(t *testing.T) {
	command, store, runner, requests := installedRecoveryControlFixture(t)
	runner.canonicalSocket = "/private/tmux/foreign"
	out := recoveryRegistryObservation(store.registry, phase6CLIIdentity().AgentUID, phase6CLIEndpoint().EndpointGenerationID, nil)
	out = readInstalledRecoveryControl(command, out, false)
	if out.Stage != "control-binding" || out.Error != "unclassified" || len(runner.calls) == 0 || len(*requests) != 0 {
		t.Fatal("readiness bypassed canonical route refusal")
	}
	if _, _, err := runRoute(t, command, "turn", "start", "uid:"+phase6CLIIdentity().AgentUID, "--", "synthetic refused input"); err == nil || len(*requests) != 0 {
		t.Fatal("turn crossed unproved route")
	}
}

func TestRecoveryErrorDistinguishesNoBrokerRefusalAndRedactsUnknownContent(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"generic", errors.New("PRIVATE-PROVIDER-PAYLOAD"), "unclassified"},
		{"wrapped-generic", fmt.Errorf("PRIVATE-WRAPPER: %w", errors.New("PRIVATE-PAYLOAD")), "unclassified"},
		{"none", &codexbroker.BrokerError{Refusal: codexbroker.RefusalNone}, "unclassified"},
		{"known", &codexbroker.BrokerError{Refusal: codexbroker.RefusalHostUnavailable}, "broker:host-unavailable"},
		{"unknown", &codexbroker.BrokerError{Refusal: "PRIVATE-CODE"}, "broker:unclassified"},
		{"cancelled", fmt.Errorf("PRIVATE-WRAPPER: %w", context.Canceled), "cancelled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := recoveryError(test.err); got != test.want || strings.Contains(got, "PRIVATE") {
				t.Fatalf("classification=%q want=%q", got, test.want)
			}
		})
	}
}
