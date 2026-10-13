package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	claude "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
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
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
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

// The owned protocol endpoint checks the public SDK's first-input projection;
// the actual Claude Handle/hook boundary is exercised by the CLI fixture suite.
func TestSDKClaudeHookSessionFirstInputTransport(t *testing.T) {
	c, _, uid, puid := sdkStopFixture(t)
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	owner, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	process, _, err := localipc.Process(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	storage := store.NewStore(store.PathFor(c.paths.StateDir))
	var b metadata.ProcessBinding
	_, err = storage.Update(func(reg *metadata.Registry) error {
		a, _ := reg.Agent(uid)
		a.Spec.Provider = "claude"
		a.Status.Phase = metadata.PhaseRunning
		a.Status.LastTermination = nil
		p, _ := reg.Pane(puid)
		p.Status.LastTermination = nil
		p.Status.ProcessSession.Provider = "claude"
		p.Status.ProcessSession.ThreadID = ""
		p.Status.ProcessSession.SessionID = "hook-session"
		b = p.Status.ProcessSession.Binding
		p.Status.Activation = metadata.PaneActivation{Kind: metadata.RuntimeProcess, AgentUID: uid, Generation: b.Generation, OperationID: b.OperationID, Process: &metadata.ProcessActivation{Binding: b, HostProcess: owner, Child: process}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := claude.ActivationLeaseDir(storage.Path(), puid, b.Generation) + "/host.sock"
	if err = os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(socket))
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err = os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	refused, err := c.DeliverTurn(context.Background(), core.TurnRequest{Ref: "uid:" + uid, Text: "unsupported"})
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeTurnProviderUnsupported || refused.Accepted || refused.OutcomeUnknown {
		t.Fatalf("DeliverTurn %+v %v", refused, err)
	}
	requests := make(chan runtime.ProcessTurnResponse, 1)
	checked := make(chan error, 1)
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			checked <- e
			return
		}
		defer conn.Close()
		var request struct {
			Foreground struct {
				Authority                 processhost.Authority
				Action, Operation, Prompt string
			}
		}
		if e = json.NewDecoder(conn).Decode(&request); e != nil {
			checked <- e
			return
		}
		a := request.Foreground.Authority
		if a.Binding.Agent != uid || a.Binding.Pane != puid || a.Binding.Generation != b.Generation || a.Connection != b.OperationID || a.Session != "hook-session" || request.Foreground.Action != "turn" || request.Foreground.Prompt != "first" {
			checked <- fmt.Errorf("wrong first-input projection %+v", request)
			return
		}
		requests <- runtime.ProcessTurnResponse{Accepted: true}
		checked <- json.NewEncoder(conn).Encode(runtime.ProcessTurnResponse{Accepted: true})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	accepted, err := c.StartTurn(ctx, core.TurnRequest{Ref: "uid:" + uid, Text: "first"})
	if err != nil || !accepted.Accepted || accepted.OutcomeUnknown || accepted.TurnID == "" {
		t.Fatalf("StartTurn %+v %v", accepted, err)
	}
	if err = <-checked; err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatal("public first input was not sent once")
	}
}
