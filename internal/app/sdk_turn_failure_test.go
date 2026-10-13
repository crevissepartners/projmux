package app

import (
	"context"
	"errors"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"io"
	"testing"
	"time"
)

type sdkEpochReceiptControl struct {
	epoch    *codexControlEpoch
	identity codexLifecycleIdentity
	calls    int
}

func (c *sdkEpochReceiptControl) Bind(context.Context, metadata.Registry, metadata.Agent) (agentcontrol.Binding, error) {
	return serviceApprovalBinding(exactAgentControlBinding{Identity: c.identity, Epoch: "epoch-1"}), nil
}
func (c *sdkEpochReceiptControl) Call(ctx context.Context, _ agentcontrol.Binding, r agentcontrol.Request) (agentcontrol.Response, error) {
	c.calls++
	return agentcontrol.Response(c.epoch.Handle(ctx, agentControlRequest{Operation: r.Operation, Text: r.Text, Identity: c.identity, Epoch: "epoch-1"})), nil
}
func TestSDKNativeEpochLostReceiptIsUnknown(t *testing.T) {
	id := phase6Identity()
	active := activeWithThreadState(codexappserver.ThreadStateActive)
	wire := &fakeExactControlWire{snapshot: active, err: io.ErrUnexpectedEOF}
	control := &sdkEpochReceiptControl{identity: id, epoch: newCodexControlEpoch(wire, id, "epoch-1", active, func(codexLifecycleIdentity) bool { return true })}
	a := metadata.Agent{}
	a.Spec.Provider = "codex"
	a.Metadata.UID = id.AgentUID
	a.Status.PaneRef = id.PaneUID
	out, err := (runtime.TurnService{Control: control}).Execute(t.Context(), metadata.Registry{}, a, runtime.TurnRequest{Action: "deliver", Text: "receipt loss fixture"})
	t.Logf("native written=%d calls=%d code=%s unknown=%t accepted=%t err=%v", wire.writes(), control.calls, out.Response.Code, out.OutcomeUnknown, out.Accepted, err)
	if err == nil || wire.steer != 1 || control.calls != 1 {
		t.Fatalf("fixture failed to reach one steer: %+v %v", out, err)
	}
	var refusal *agentcontrol.Refusal
	if !errors.As(err, &refusal) || refusal.Code != "stale-turn" {
		t.Fatalf("legacy cause lost: %v", err)
	}
	if !out.OutcomeUnknown {
		t.Error("wire EOF after steer reported as known refusal")
	}

}
func TestClaudeTurnRouteKeepsConfiguredDeadline(t *testing.T) {
	cmd, _, _, _ := claudeTurnFixture(t)
	cmd.controlTimeout = 50 * time.Millisecond
	called := false
	cmd.controlRoute = func(ctx context.Context) (runtimeMutationRoute, error) {
		called = true
		deadline, bounded := ctx.Deadline()
		t.Logf("route called bounded=%t remaining=%s configured=%s", bounded, time.Until(deadline), cmd.controlTimeout)
		if !bounded || time.Until(deadline) <= 0 || time.Until(deadline) > cmd.controlTimeout {
			t.Error("CLI Claude exact route probe lost the existing control timeout")
		}
		return runtimeMutationRoute{}, errors.New("independent route probe sentinel")
	}
	err := cmd.runTurnInterrupt([]string{"interrupt", "uid:agt-alpha-codex", "--via", "review-fixture"}, io.Discard)
	if !called || err == nil {
		t.Fatalf("fixture did not reach route: called=%t err=%v", called, err)
	}
}

func TestSDKNativeEpochPrewriteBindingRefusalIsKnown(t *testing.T) {
	id := phase6Identity()
	active := activeWithThreadState(codexappserver.ThreadStateActive)
	wire := &fakeExactControlWire{snapshot: active, err: io.ErrUnexpectedEOF}
	control := &sdkEpochReceiptControl{identity: id, epoch: newCodexControlEpoch(wire, id, "epoch-1", active, func(codexLifecycleIdentity) bool { return false })}
	a := metadata.Agent{}
	a.Spec.Provider = "codex"
	a.Metadata.UID = id.AgentUID
	a.Status.PaneRef = id.PaneUID
	out, err := (runtime.TurnService{Control: control}).Execute(t.Context(), metadata.Registry{}, a, runtime.TurnRequest{Action: "deliver", Text: "prewrite fixture"})
	if err == nil || out.Accepted || out.OutcomeUnknown || wire.writes() != 0 || control.calls != 1 || out.Response.Code != "stale-binding" {
		t.Fatalf("known prewrite result=%+v err=%v wire=%d calls=%d", out, err, wire.writes(), control.calls)
	}
}

func TestSDKProcessSocketReplacementIsKnownBeforeWrite(t *testing.T) {
	f := newProcessCodexFixture(t, nil)
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Agent(f.endpoint.binding.Agent)
	before := len(f.pollWire(t))
	original := f.endpoint.socket
	var replacement *localipc.Listener
	defer func() {
		if replacement != nil {
			if err := replacement.Close(); err != nil {
				t.Error(err)
			}
		}
	}()
	out, err := runtime.CallProcessTurn(t.Context(), f.path, func() (metadata.Registry, error) {
		// This exact fixture owns the socket. Unlink its captured inode before
		// replacement; a rename/restore changes ctime and cannot restore identity.
		if err := localipc.RemoveOwnedSocket(original, f.endpoint.socketIdentity); err != nil {
			return metadata.Registry{}, err
		}
		var err error
		replacement, err = localipc.Listen(original)
		if err != nil {
			return metadata.Registry{}, err
		}
		return reg, nil
	}, reg, *a, runtime.TurnRequest{Action: "deliver", Text: "socket prewrite fixture"})
	if !errors.Is(err, processhost.ErrStale) || out.Accepted || out.OutcomeUnknown || len(f.pollWire(t)) != before {
		t.Fatalf("known socket replacement %+v %v", out, err)
	}
}
