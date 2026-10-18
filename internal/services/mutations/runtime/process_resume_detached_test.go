package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestDetachedResumeCleanupRecoversOnlyExactOwner(t *testing.T) {
	for _, kind := range []string{"matching-retirement", "different-owner", "missing-retirement", "different-pane", "different-Wait"} {
		t.Run(kind, func(t *testing.T) {
			reg, _, pane := stopFixture()
			activation := *pane.Status.Activation.Process
			// A mismatched kernel start proves this historical test identity is absent.
			owner := m.ProcessIdentity{PID: os.Getpid(), OwnerUID: uint32(os.Getuid()), Start: "retired-test-owner"}
			activation.HostProcess = owner
			previous := activation.Binding
			previous.Generation, previous.OperationID, previous.HostInstanceID = "previous-generation", "previous-op", "previous-host"
			request := ProcessDetachedResumeRequest{Agent: previous.AgentUID, Previous: previous}
			writeStopWait(&reg)
			path := filepath.Join(t.TempDir(), "metadata", "registry.json")
			if kind != "missing-retirement" {
				if err := WriteProcessRetirement(path, activation, reg.Panes[0].Status.LastTermination); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "different-owner":
				owner.Start = "another-retired-owner"
			case "different-pane":
				request.Previous.PaneUID = "another-pane"
			case "different-Wait":
				wait := reg.Panes[0].Status.LastTermination.Clone()
				wait.ObservedAt = wait.ObservedAt.Add(time.Second)
				reg.Panes[0].Status.LastTermination = wait
				reg.Agents[0].Status.LastTermination = wait.Clone()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
			defer cancel()
			binding, err := awaitDetachedProcessResumeCleanup(ctx, func() (m.Registry, error) { return reg.Clone(), nil }, path, request, owner, processhost.Binding{})
			if kind == "matching-retirement" {
				if err != nil || binding != processResumeHostBinding(activation.Binding) {
					t.Fatalf("exact retirement not recovered: %+v %v", binding, err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "process-detached-cleanup-unconfirmed") || binding != (processhost.Binding{}) {
				t.Fatalf("unproved generation accepted: %+v %v", binding, err)
			}
		})
	}
}

func TestPreparedResumeCleanupUnreservedLostSource(t *testing.T) {
	for _, mode := range []string{"prepared-refusal", "ordinary", "same-owner", "other-binding"} {
		t.Run(mode, func(t *testing.T) {
			reg, agent, pane := stopFixture()
			owner := m.ProcessIdentity{PID: os.Getpid(), OwnerUID: uint32(os.Getuid()), Start: "absent-owned-preparation-helper"}
			request := ProcessDetachedResumeRequest{Agent: agent.Metadata.UID, Previous: pane.Status.ProcessSession.Binding, Preparation: &ProcessResumePreparation{Binding: pane.Status.ProcessSession.Binding}}
			switch mode {
			case "ordinary":
				request.Preparation = nil
			case "same-owner":
				reg.Panes[0].Status.Activation.Process.HostProcess = owner
			case "other-binding":
				request.Previous.Generation = "other-generation"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			_, err := awaitDetachedProcessResumeCleanup(ctx, func() (m.Registry, error) { return reg.Clone(), nil }, filepath.Join(t.TempDir(), "registry.json"), request, owner, processhost.Binding{})
			if mode == "prepared-refusal" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("cleanup weakened ordinary/owner/binding proof", mode, err)
			}
		})
	}
}
