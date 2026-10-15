package projmux

import (
	"context"
	"errors"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestRelaunchAgentAdmissionAndProcessRefusal(t *testing.T) {
	c, _, uid, _ := sdkStopFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.RelaunchAgent(ctx, core.RelaunchAgentRequest{Ref: "uid:" + uid}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		req  core.RelaunchAgentRequest
		code Code
	}{
		{core.RelaunchAgentRequest{}, CodeSelectorInvalid},
		{core.RelaunchAgentRequest{Ref: "uid:missing", Reset: []string{"invalid"}}, CodeRelaunchRequestInvalid},
		{core.RelaunchAgentRequest{Ref: "uid:" + uid, SocketPath: "relative"}, CodeRuntimeRequestInvalid},
		{core.RelaunchAgentRequest{Ref: "uid:" + uid, DryRun: true}, CodeRelaunchProcessUnsupported},
	} {
		_, err := c.RelaunchAgent(context.Background(), tc.req)
		var refused *Refusal
		if !errors.As(err, &refused) || refused.Code != tc.code {
			t.Fatalf("want %s got %v", tc.code, err)
		}
	}
}

func TestRelaunchLayerConflictAndExactRecovery(t *testing.T) {
	none := ""
	for _, q := range []core.RelaunchAgentRequest{
		{Model: "sonnet", Reset: []string{"model"}},
		{Instructions: &none, Reset: []string{"instructions"}},
		{Reset: []string{"invalid"}},
	} {
		err := validateRelaunchLayers(q)
		var refused *Refusal
		if !errors.As(err, &refused) || refused.Code != CodeRelaunchRequestInvalid {
			t.Fatalf("%+v: %v", q, err)
		}
	}
	if err := validateRelaunchLayers(core.RelaunchAgentRequest{Instructions: &none, Reset: []string{"model", "effort"}}); err != nil {
		t.Fatal(err)
	}
	// Multiple resets must form one CLI flag: a repeated --reset replaces the
	// earlier list. Recovery cannot silently lose a requested layer reset.
	got := relaunchRecovery(core.RelaunchAgentRequest{Reset: []string{"model", "effort"}}, "exact-agent", runtime.AgentResumePlan{ProjectUID: "exact-project", WindowUID: "exact-window"}, false)
	if strings.Count(got, "--reset") != 1 || !strings.Contains(got, "model,effort") || !strings.Contains(got, "agent relaunch uid:exact-agent") {
		t.Fatal(got)
	}
}

func TestRelaunchNativeProbeCancellationRetainsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := relaunchCodexProbe(ctx, nil, runtime.AgentRestartPlan{}, func(string, string) error { t.Fatal("canceled admission reached refusal callback"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
