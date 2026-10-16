package runtime

import (
	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDetachedRelaunchRecoveryRequiresExactResumeWitness(t *testing.T) {
	for _, kind := range []string{"matching-retirement", "different-owner", "missing-retirement", "different-pane", "different-Wait"} {
		t.Run(kind, func(t *testing.T) {
			reg, agent, pane := stopFixture()
			activation := *pane.Status.Activation.Process
			owner := m.ProcessIdentity{PID: os.Getpid(), OwnerUID: uint32(os.Getuid()), Start: "retired-relaunch-owner"}
			activation.HostProcess = owner
			previous := pane.Clone()
			previous.Status.ProcessSession.Binding.Generation = "old-gen"
			previous.Status.ProcessSession.Binding.OperationID = "old-op"
			previous.Status.ProcessSession.Binding.HostInstanceID = "old-host"
			wire := ProcessDetachedRelaunchRequest{Source: agent.Clone(), Pane: &previous}
			writeStopWait(&reg)
			path := filepath.Join(t.TempDir(), "metadata", "registry.json")
			if kind != "missing-retirement" {
				if err := WriteProcessRetirement(path, activation, reg.Panes[0].Status.LastTermination); err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "different-owner":
				owner.Start = "unrelated-owner"
			case "different-pane":
				wire.Pane.Status.ProcessSession.Binding.PaneUID = "another-pane"
			case "different-Wait":
				wait := reg.Panes[0].Status.LastTermination.Clone()
				wait.ObservedAt = wait.ObservedAt.Add(time.Second)
				reg.Panes[0].Status.LastTermination = wait
				reg.Agents[0].Status.LastTermination = wait.Clone()
			}
			got := RecoverDetachedProcessRelaunchBinding(func() (m.Registry, error) { return reg.Clone(), nil }, path, wire, owner)
			if kind == "matching-retirement" {
				if got != processResumeHostBinding(activation.Binding) {
					t.Fatalf("exact retirement lost: %+v", got)
				}
			} else if got != (processhost.Binding{}) {
				t.Fatalf("unproven binding returned: %+v", got)
			}
		})
	}
}
