package runtime

import (
	"errors"
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

// A proof is accepted only for a request that asked for one, and only for the
// exact ownerless recipe it names.
func TestCheckRelaunchPreparationRefusesUnrequestedAndMissingRecipe(t *testing.T) {
	proof := &ProcessResumePreparation{AgentUID: "agent-a", PaneUID: "pane-a", Conversation: "conversation", LaunchDigest: "digest"}
	path := filepath.Join(t.TempDir(), "registry.json")
	for name, tc := range map[string]struct {
		requested bool
		proof     *ProcessResumePreparation
		agent     string
		want      error
	}{
		"no proof, none requested":   {false, nil, "agent-a", nil},
		"no proof, one requested":    {true, nil, "agent-a", nil},
		"proof not requested":        {false, proof, "agent-a", processhost.ErrStale},
		"proof for another Agent":    {true, proof, "agent-b", processhost.ErrStale},
		"proof without a recipe":     {true, proof, "agent-a", processhost.ErrStale},
		"proof without Conversation": {true, &ProcessResumePreparation{AgentUID: "agent-a", PaneUID: "pane-a", LaunchDigest: "digest"}, "agent-a", processhost.ErrStale},
		"proof without LaunchDigest": {true, &ProcessResumePreparation{AgentUID: "agent-a", PaneUID: "pane-a", Conversation: "conversation"}, "agent-a", processhost.ErrStale},
		"proof without PaneUID":      {true, &ProcessResumePreparation{AgentUID: "agent-a", Conversation: "conversation", LaunchDigest: "digest"}, "agent-a", processhost.ErrStale},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkRelaunchPreparation(m.Registry{}, path, tc.requested, tc.proof, tc.agent)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("want %v got %v", tc.want, err)
			}
		})
	}
}

// A Prepare request succeeds only through a complete reply, with the proof for an
// execution; a waiting claim or live owner reply never does.
func TestCheckRelaunchPrepareReplyClosesOtherSuccesses(t *testing.T) {
	proof := &ProcessResumePreparation{AgentUID: "agent-a"}
	for name, tc := range map[string]struct {
		requested bool
		phase     string
		dryRun    bool
		proof     *ProcessResumePreparation
		want      error
	}{
		"not requested, any reply":      {false, "", false, nil, nil},
		"complete execution with proof": {true, "complete", false, proof, nil},
		"complete dry run, no proof":    {true, "complete", true, nil, nil},
		"complete execution, no proof":  {true, "complete", false, nil, processhost.ErrStale},
		"waiting claim reply":           {true, "", false, proof, processhost.ErrStale},
		"live owner starting reply":     {true, "starting", false, proof, processhost.ErrStale},
		"planned reply":                 {true, "planned", true, nil, processhost.ErrStale},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkRelaunchPrepareReply(tc.requested, tc.phase, tc.dryRun, tc.proof)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("want %v got %v", tc.want, err)
			}
		})
	}
}
