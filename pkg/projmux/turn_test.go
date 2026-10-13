package projmux

import (
	"context"
	"errors"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
	"os"
	"testing"
)

func TestSDKTurnDeferredClaimRefusesBeforeEffects(t *testing.T) {
	c, reg, uid, puid := sdkStopFixture(t)
	identity, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := reg.Pane(puid)
	claim := runtime.DeferredClaimRecord{Version: 1, Agent: uid, Binding: p.Status.ProcessSession.Binding, Provider: "codex", Process: identity, Nonce: "isolated-fixture"}
	path := runtime.DeferredClaimPath(store.PathFor(c.paths.StateDir), uid)
	if err = runtime.WriteDeferredClaim(path, claim); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []func(context.Context, core.TurnRequest) (core.TurnResult, error){c.StartTurn, c.SteerTurn, c.DeliverTurn, c.InterruptTurn} {
		out, err := call(context.Background(), core.TurnRequest{Ref: "uid:" + uid, Text: "must not start claim"})
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeTurnDeferredUnsupported || out.Accepted || out.OutcomeUnknown {
			t.Fatalf("claim %+v %v", out, err)
		}
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("SDK changed claim")
	}
	if _, err = os.Stat(path + ".lock"); !os.IsNotExist(err) {
		t.Fatal("SDK acquired claim lifetime guard", err)
	}
}
func TestSDKTurnInputValidationAndExplicitRoot(t *testing.T) {
	c, _, uid, _ := sdkStopFixture(t)
	out, err := c.StartTurn(context.Background(), core.TurnRequest{Ref: "uid:" + uid})
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeTurnInvalidInput || out.Accepted {
		t.Fatalf("empty input %+v %v", out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = c.DeliverTurn(ctx, core.TurnRequest{Ref: "uid:" + uid, Text: "cancelled"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
