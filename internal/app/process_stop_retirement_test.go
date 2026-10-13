package app

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	sdk "github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestProcessWaitWriterRetainsOwnerBarrierForConcurrentSDKStops(t *testing.T) {
	store, b := sessionBindingFixture(t, aiModeCodex)
	host := exec.Command("sleep", "5")
	if err := host.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = host.Process.Kill(); _ = host.Wait() }()
	child := exec.Command("sleep", "0.05")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill() }()
	ownerID, _, err := localipc.Process(host.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	childID, _, err := localipc.Process(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Update(func(reg *coremetadata.Registry) error {
		pane, _ := reg.Pane(b.Pane)
		pane.Status.Activation.Process.HostProcess = ownerID
		pane.Status.Activation.Process.Child = childID
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = child.Wait(); err != nil {
		t.Fatal(err)
	}
	writer := processAgentCreateResult{Binding: b, Provider: aiModeCodex, registryPath: store.Path()}
	if err = writer.persistProcessWait(processhost.Snapshot{Binding: b, Provider: aiModeCodex, State: "exited", Exit: &processhost.Exit{Code: 0}}); err != nil {
		t.Fatal(err)
	}
	reg, err := store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane(b.Pane)
	if !pane.Status.Activation.IsZero() {
		t.Fatal("actual Wait did not clear activation")
	}
	retained, err := mutationruntime.ReadProcessRetirement(store.Path(), pane.Status.ProcessSession.Binding, pane.Status.LastTermination)
	if err != nil || retained.HostProcess != ownerID || retained.Child != childID {
		t.Fatalf("actual writer lost owner identity: %+v %v", retained, err)
	}
	state := filepath.Dir(filepath.Dir(store.Path()))
	client, err := sdk.NewClient(sdk.Options{ConfigDir: filepath.Join(state, "config"), StateDir: state, HomeDir: state})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for range 2 {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 65*time.Millisecond)
			defer cancel()
			result, err := client.StopAgent(ctx, core.StopAgentRequest{Ref: "uid:" + b.Agent})
			if result.Stopped || !errors.Is(err, context.DeadlineExceeded) {
				results <- errors.New("concurrent Stop succeeded before owner retirement")
				return
			}
			results <- nil
		}()
	}
	for range 2 {
		if err = <-results; err != nil {
			t.Fatal(err)
		}
	}
	current, _, err := localipc.Process(ownerID.PID)
	if err != nil || current != ownerID {
		t.Fatal("retired wait stop signaled owner")
	}
	if err = host.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = host.Wait()
	for range 2 {
		result, err := client.StopAgent(context.Background(), core.StopAgentRequest{Ref: "uid:" + b.Agent})
		if err != nil || !result.Stopped || result.Termination == nil {
			t.Fatalf("owner retired idempotence %+v %v", result, err)
		}
	}
}
