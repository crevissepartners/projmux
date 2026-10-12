package app

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestAgentTurnDeliverDispatchReceiptsAndLegacyObserver(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses []agentControlResponse
		ops       []string
		mode      string
	}{
		{"start", []agentControlResponse{{OK: true, ThreadID: "thread-1", TurnID: "new"}}, []string{agentControlOpDeliver}, "start"},
		{"steer", []agentControlResponse{{OK: true, ThreadID: "thread-1", TurnID: "current", Acceptance: agentControlAcceptanceProvider, Delivery: agentControlDeliveryUnconfirmed}}, []string{agentControlOpDeliver}, "steer"},
		{"legacy start", []agentControlResponse{{Code: "invalid-operation"}, {OK: true, ThreadID: "thread-1", TurnID: "new"}}, []string{agentControlOpDeliver, agentControlOpStart}, "start"},
		{"legacy steer", []agentControlResponse{{Code: "invalid-operation"}, {Code: "turn-in-progress"}, {OK: true, ThreadID: "thread-1", TurnID: "current", Acceptance: agentControlAcceptanceProvider, Delivery: agentControlDeliveryUnconfirmed}}, []string{agentControlOpDeliver, agentControlOpStart, agentControlOpSteer}, "steer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, store, _ := exactControlCLICommand(t)
			var ops []string
			text := " --dry-run=false\n  exact text  "
			c.controlCall = func(_ context.Context, _ string, _ coremetadata.CodexEndpointRef, identity codexLifecycleIdentity, r agentControlRequest) (agentControlResponse, error) {
				if identity != phase6CLIIdentity() || r.Text != text {
					t.Fatalf("request: %+v identity=%+v", r, identity)
				}
				ops = append(ops, r.Operation)
				if len(ops) > len(tc.responses) {
					t.Fatal("extra control write")
				}
				return tc.responses[len(ops)-1], nil
			}
			out, _, err := runRoute(t, c, "turn", "deliver", "uid:agt-alpha-codex", "--", text)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ops, tc.ops) || !strings.Contains(out, "mode="+tc.mode) {
				t.Fatalf("ops=%v output=%q", ops, out)
			}
			if tc.mode == "steer" && !strings.Contains(out, "acceptance=provider delivery=unconfirmed") {
				t.Fatalf("receipt=%q", out)
			}
			a, _ := store.registry.Agent("agt-alpha-codex")
			if tc.mode == "start" && !a.Status.SessionRef.Codex.HasStartedTurn {
				t.Fatalf("first turn evidence: %+v", a.Status.SessionRef.Codex)
			}
		})
	}
}

func TestAgentTurnDeliverFailureNeverReplays(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses []agentControlResponse
		transport bool
		calls     int
	}{
		{"lost transport", nil, true, 1},
		{"busy", []agentControlResponse{{Code: "turn-in-progress"}}, false, 1},
		{"stale", []agentControlResponse{{Code: "stale-binding"}}, false, 1},
		{"uncertain", []agentControlResponse{{Code: "delivery-unconfirmed"}}, false, 1},
		{"malformed steer", []agentControlResponse{{OK: true, Acceptance: agentControlAcceptanceProvider}}, false, 1},
		{"wrong start thread", []agentControlResponse{{OK: true, ThreadID: "other", TurnID: "new"}}, false, 1},
		{"legacy lost start", []agentControlResponse{{Code: "invalid-operation"}}, true, 2},
		{"legacy refused start", []agentControlResponse{{Code: "invalid-operation"}, {Code: "stale-binding"}}, false, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, store, _ := exactControlCLICommand(t)
			before := store.snapshot()
			calls := 0
			c.controlCall = func(context.Context, string, coremetadata.CodexEndpointRef, codexLifecycleIdentity, agentControlRequest) (agentControlResponse, error) {
				calls++
				if calls > tc.calls {
					t.Fatal("extra write")
				}
				if tc.transport && calls == tc.calls {
					return agentControlResponse{}, errors.New("receipt lost")
				}
				return tc.responses[calls-1], nil
			}
			out, _, err := runRoute(t, c, "turn", "deliver", "uid:agt-alpha-codex", "--", "input")
			if err == nil || out != "" || calls != tc.calls || store.snapshot() != before {
				t.Fatalf("out=%q err=%v calls=%d registry changed=%t", out, err, calls, store.snapshot() != before)
			}
		})
	}
}

func TestAgentTurnDeliverRefusesProviderAndEmptyInputBeforeControl(t *testing.T) {
	for _, args := range [][]string{
		{"turn", "deliver", "uid:agt-alpha-claude", "--", "text"},
		{"turn", "deliver", "uid:agt-alpha-codex", "--", " \n\t"},
		{"turn", "deliver", "uid:agt-alpha-codex", "text"},
		{"turn", "deliver", "uid:agt-alpha-codex", "--", "first", "second"},
	} {
		c, store, _ := exactControlCLICommand(t)
		before := store.snapshot()
		c.controlCall = func(context.Context, string, coremetadata.CodexEndpointRef, codexLifecycleIdentity, agentControlRequest) (agentControlResponse, error) {
			t.Fatal("refusal reached control")
			return agentControlResponse{}, nil
		}
		out, _, err := runRoute(t, c, args...)
		if err == nil || out != "" || before != store.snapshot() {
			t.Fatalf("args=%q out=%q err=%v", args, out, err)
		}
	}
}
