package app

import (
	"bytes"
	"context"
	"errors"
	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSDKProcessTurnDefaultTransportCLIParityTwoRoots(t *testing.T) {
	for lane := range 2 {
		f := newProcessCodexFixture(t, func(root, binary string, env []string) processhost.Command {
			fixture := strings.Replace(processCodexProviderFixture, " elif method=='turn/interrupt':", " elif method=='turn/steer':reply({'turnId':current})\n elif method=='turn/interrupt':", 1)
			return processhost.Command{Path: "python3", Args: []string{"-u", "-c", fixture}, Dir: root, Env: env}
		})
		paths := config.Paths{ConfigDir: filepath.Join(f.root, "config", "projmux"), StateDir: filepath.Join(f.root, "state", "projmux")}
		sdk, err := projmux.NewClient(projmux.Options{ConfigDir: paths.ConfigDir, StateDir: paths.StateDir, HomeDir: f.root, ProjmuxExecutable: f.binary, SocketPath: filepath.Join(f.root, "unused.sock")})
		if err != nil {
			t.Fatal(err)
		}
		cmd := &agentCommand{loadRegistry: f.store.LoadReadOnly, controlPaths: func() (config.Paths, error) { return paths, nil }}
		ref := "uid:" + f.endpoint.binding.Agent
		// Detached owners retain their claim until actual Wait even after child
		// birth. An existing exact child remains controllable through the SDK.
		reg, err := f.store.LoadReadOnly()
		if err != nil {
			t.Fatal(err)
		}
		pane, _ := reg.Pane(f.endpoint.binding.Pane)
		claimPath := mutationruntime.DeferredClaimPath(intmetadata.PathFor(paths.StateDir), f.endpoint.binding.Agent)
		if err = os.MkdirAll(filepath.Dir(claimPath), 0700); err != nil {
			t.Fatal(err)
		}
		claim := mutationruntime.DeferredClaimRecord{Version: 1, Agent: f.endpoint.binding.Agent, Binding: pane.Status.ProcessSession.Binding, Provider: "codex", Thread: "process-thread", Process: f.endpoint.evidence.HostProcess, Nonce: "born-claim-fixture", Detached: true}
		if err = mutationruntime.WriteDeferredClaim(claimPath, claim); err != nil {
			t.Fatal(err)
		}
		claimBefore, err := os.ReadFile(claimPath)
		if err != nil {
			t.Fatal(err)
		}

		var first core.TurnResult
		if lane == 0 {
			first, err = sdk.DeliverTurn(context.Background(), core.TurnRequest{Ref: ref, Text: "hold"})
		} else {
			first, err = sdk.StartTurn(context.Background(), core.TurnRequest{Ref: ref, Text: "hold"})
		}
		if err != nil || !first.Accepted || first.Mode != "start" || first.ThreadID != "process-thread" || first.TurnID == "" || first.Generation != f.endpoint.binding.Generation {
			t.Fatalf("start %+v %v", first, err)
		}
		observed := f.wait(t, func(s processhost.Snapshot) bool { return s.Turn != "" })
		providerTurn := observed.Turn
		if lane == 0 && first.TurnID != providerTurn {
			t.Fatal("typed delivery lost provider turn")
		}
		if lane == 1 && first.TurnID != first.Operation {
			t.Fatal("explicit start changed legacy host operation receipt")
		}

		// With this fixture's absent semantic sink, both callers reject the empty
		// Registry turn before an interrupt reaches the provider.
		wireBefore := len(f.pollWire(t))
		var premature bytes.Buffer
		if err := cmd.runTurnInterrupt([]string{"interrupt", ref}, &premature); !errors.Is(err, processhost.ErrStale) {
			t.Fatalf("CLI fixture stale %v", err)
		}
		if _, err := sdk.InterruptTurn(context.Background(), core.TurnRequest{Ref: ref}); err == nil {
			t.Fatal("SDK accepted missing Registry turn")
		}
		if len(f.pollWire(t)) != wireBefore {
			t.Fatal("missing Registry turn wrote interrupt")
		}
		// This transport fixture has no semantic sink. Publish the observed exact
		// turn as the production sink does before testing Registry-pinned interrupt.
		if _, err = f.store.Update(func(reg *coremetadata.Registry) error {
			p, _ := reg.Pane(f.endpoint.binding.Pane)
			p.Status.ProcessSession.TurnID = providerTurn
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		var out bytes.Buffer
		err = cmd.runTurnInput([]string{"start", ref, "--", "busy"}, &out)
		if !errors.Is(err, processhost.ErrBusy) {
			t.Fatalf("CLI busy %v", err)
		}
		second, err := sdk.SteerTurn(context.Background(), core.TurnRequest{Ref: ref, Text: "-- exact bytes"})
		if err != nil || second.Mode != "steer" || second.TurnID != providerTurn || second.Acceptance != "provider" || second.Delivery != "unconfirmed" {
			t.Fatalf("steer %+v %v", second, err)
		}
		cli, steered, err := cmd.deliverTurn(ref, "CLI input")
		if err != nil || !steered || cli.TurnID != second.TurnID || cli.Acceptance != second.Acceptance || cli.Delivery != second.Delivery {
			t.Fatalf("CLI %+v %v", cli, err)
		}
		before := len(f.pollWire(t))
		_, err = sdk.DeliverTurn(context.Background(), core.TurnRequest{Ref: ref, Text: "stale", Generation: "replaced"})
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodeTurnStale {
			t.Fatalf("generation %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err = sdk.StartTurn(ctx, core.TurnRequest{Ref: ref, Text: "cancelled"}); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		if len(f.pollWire(t)) != before {
			t.Fatal("pre-effect refusal wrote bytes")
		}

		// A mismatched kernel birth is a pre-write stale refusal, as on the CLI.
		var birth string
		if _, err = f.store.Update(func(reg *coremetadata.Registry) error {
			p, _ := reg.Pane(f.endpoint.binding.Pane)
			birth = p.Status.Activation.Process.HostProcess.Start
			p.Status.Activation.Process.HostProcess.Start += "-wrong"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		stale, err := sdk.SteerTurn(context.Background(), core.TurnRequest{Ref: ref, Text: "kernel stale"})
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodeTurnStale || stale.OutcomeUnknown || len(f.pollWire(t)) != before {
			t.Fatalf("kernel refusal %+v %v", stale, err)
		}
		if _, err = f.store.Update(func(reg *coremetadata.Registry) error {
			p, _ := reg.Pane(f.endpoint.binding.Pane)
			p.Status.Activation.Process.HostProcess.Start = birth
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		interrupted, err := sdk.InterruptTurn(context.Background(), core.TurnRequest{Ref: ref})
		if err != nil || !interrupted.Accepted || interrupted.Operation == "" {
			t.Fatalf("interrupt %+v %v", interrupted, err)
		}
		f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == "" })
		starts, steers, interrupts := 0, 0, 0
		for _, n := range f.pollWire(t) {
			switch string(n["method"]) {
			case `"turn/start"`:
				starts++
			case `"turn/steer"`:
				steers++
			case `"turn/interrupt"`:
				interrupts++
			}
		}

		claimAfter, err := os.ReadFile(claimPath)
		if err != nil || !bytes.Equal(claimBefore, claimAfter) {
			t.Fatal("SDK changed born claim", err)
		}
		if _, err = os.Stat(claimPath + ".lock"); !os.IsNotExist(err) {
			t.Fatal("SDK took claim lifetime lock", err)
		}
		if starts != 1 || steers != 2 || interrupts != 1 {
			t.Fatalf("wire %d %d %d", starts, steers, interrupts)
		}
	}
}
func TestSDKProcessTurnLostReceiptNoReplay(t *testing.T) {
	f := newProcessCodexFixture(t, func(root, binary string, env []string) processhost.Command {
		fixture := strings.Replace(processCodexProviderFixture, " elif method=='turn/interrupt':", " elif method=='turn/steer':sys.exit(0)\n elif method=='turn/interrupt':", 1)
		return processhost.Command{Path: "python3", Args: []string{"-u", "-c", fixture}, Dir: root, Env: env}
	})
	sdk, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(f.root, "config", "projmux"), StateDir: filepath.Join(f.root, "state", "projmux"), HomeDir: f.root})
	if err != nil {
		t.Fatal(err)
	}
	ref := "uid:" + f.endpoint.binding.Agent
	first, err := sdk.DeliverTurn(context.Background(), core.TurnRequest{Ref: ref, Text: "hold"})
	if err != nil {
		t.Fatal(err)
	}
	f.wait(t, func(s processhost.Snapshot) bool { return s.Turn == first.TurnID })
	lost, err := sdk.DeliverTurn(context.Background(), core.TurnRequest{Ref: ref, Text: "lost"})
	if err == nil || lost.Accepted || !lost.OutcomeUnknown {
		t.Fatalf("lost %+v %v", lost, err)
	}
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeTurnStale || !errors.Is(err, processhost.ErrStale) || lost.ThreadID != first.ThreadID || lost.Generation != first.Generation || lost.Operation == "" {
		t.Fatalf("lost receipt/cause changed %+v %v", lost, err)
	}
	starts, steers := 0, 0
	for _, n := range f.pollWire(t) {
		switch string(n["method"]) {
		case `"turn/start"`:
			starts++
		case `"turn/steer"`:
			steers++
		}
	}
	if starts != 1 || steers != 1 {
		t.Fatalf("replay %d %d", starts, steers)
	}
}
