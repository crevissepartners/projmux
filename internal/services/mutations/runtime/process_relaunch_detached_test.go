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

// A Prepare request succeeds only through a complete reply that agrees with the
// request on DryRun and carries no claim; an execution also needs the proof.
func TestCheckRelaunchPrepareReplyClosesOtherSuccesses(t *testing.T) {
	proof := &ProcessResumePreparation{AgentUID: "agent-a"}
	for name, tc := range map[string]struct {
		requested, requestDryRun bool
		phase                    string
		replyDryRun, claim       bool
		proof                    *ProcessResumePreparation
		want                     error
	}{
		"not requested, any reply":      {false, false, "", false, true, nil, nil},
		"complete execution with proof": {true, false, "complete", false, false, proof, nil},
		"complete dry run, no proof":    {true, true, "complete", true, false, nil, nil},
		"complete execution, no proof":  {true, false, "complete", false, false, nil, processhost.ErrStale},
		"execution answered as dry run": {true, false, "complete", true, false, nil, processhost.ErrStale},
		"dry run answered as execution": {true, true, "complete", false, false, proof, processhost.ErrStale},
		"complete with proof and claim": {true, false, "complete", false, true, proof, processhost.ErrStale},
		"complete dry run with a claim": {true, true, "complete", true, true, nil, processhost.ErrStale},
		"waiting claim reply":           {true, false, "", false, true, proof, processhost.ErrStale},
		"live owner starting reply":     {true, false, "starting", false, false, proof, processhost.ErrStale},
		"planned reply":                 {true, true, "planned", true, false, nil, processhost.ErrStale},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkRelaunchPrepareReply(tc.requested, tc.requestDryRun, tc.phase, tc.replyDryRun, tc.claim, tc.proof)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("want %v got %v", tc.want, err)
			}
		})
	}
}

// On a valid ownerless Registry the exact proof passes, and each guard still
// refuses its own broken input (an empty Registry would refuse them all alike).
func TestCheckRelaunchPreparationOnValidRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "registry.json")
	ownerless := func() (m.Registry, ProcessResumePreparation) {
		reg, _, pane := stopFixture()
		writeStopWait(&reg)
		return reg, ProcessResumePreparation{AgentUID: "agent", PaneUID: "pane", Conversation: "conversation", LaunchDigest: "digest", Binding: pane.Status.ProcessSession.Binding}
	}
	for name, tc := range map[string]struct {
		mutate func(*m.Registry, *ProcessResumePreparation)
		want   error
	}{
		"exact proof":            {func(*m.Registry, *ProcessResumePreparation) {}, nil},
		"agent still running":    {func(r *m.Registry, _ *ProcessResumePreparation) { r.Agents[0].Status.Phase = m.PhaseRunning }, processhost.ErrStale},
		"pane still activated":   {func(r *m.Registry, _ *ProcessResumePreparation) { _, _, p := stopFixture(); r.Panes[0].Status.Activation = p.Status.Activation }, processhost.ErrStale},
		"binding differs":        {func(_ *m.Registry, p *ProcessResumePreparation) { p.Binding.Generation += "x" }, processhost.ErrStale},
		"pane differs":           {func(_ *m.Registry, p *ProcessResumePreparation) { p.PaneUID = "other" }, processhost.ErrStale},
		"agent points elsewhere": {func(r *m.Registry, _ *ProcessResumePreparation) { r.Agents[0].Status.PaneRef = "other" }, processhost.ErrStale},
	} {
		t.Run(name, func(t *testing.T) {
			reg, proof := ownerless()
			tc.mutate(&reg, &proof)
			err := checkRelaunchPreparation(reg, path, true, &proof, "agent")
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("want %v got %v", tc.want, err)
			}
		})
	}
}
