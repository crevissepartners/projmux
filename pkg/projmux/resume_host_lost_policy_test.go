package projmux

import (
	"context"
	"errors"
	"testing"

	m "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// A bad host-lost policy is refused as an invalid request before any effect.
// The Client has no executable, so a process request that got past the input
// check would instead fail with CodeExecutableInvalid.
func TestResumeAgentHostLostPolicyInputInvalid(t *testing.T) {
	f := agentProcessRegistryFixture(t, fixtureHostLost)
	var tmuxAgent string
	if _, err := store.NewStore(store.PathFor(f.state)).Update(func(reg *m.Registry) error {
		agent, _ := reg.Agent(f.agent)
		created, e := store.DefaultMutator().CreateAgent(reg, agent.Metadata.OwnerUID(), m.CreateAgentOptions{Name: "tmux-policy-agent", Provider: "codex", OperationID: "tmux-policy"})
		tmuxAgent = created.Metadata.UID
		return e
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		req  core.ResumeAgentRequest
	}{
		{"process/unknown value", core.ResumeAgentRequest{Ref: "uid:" + f.agent, Detach: true, HostLost: "allow"}},
		{"process/wrong case", core.ResumeAgentRequest{Ref: "uid:" + f.agent, Detach: true, HostLost: "Require"}},
		{"process/require with WaitForPeer", core.ResumeAgentRequest{Ref: "uid:" + f.agent, Detach: true, WaitForPeer: true, HostLost: core.ResumeHostLostRequire}},
		{"tmux/require", core.ResumeAgentRequest{Ref: "uid:" + tmuxAgent, HostLost: core.ResumeHostLostRequire}},
		{"tmux/refuse", core.ResumeAgentRequest{Ref: "uid:" + tmuxAgent, HostLost: core.ResumeHostLostRefuse}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.client.ResumeAgent(context.Background(), tc.req)
			var refusal *Refusal
			if !errors.As(err, &refusal) || refusal.Code != CodeResumeRequestInvalid {
				t.Fatalf("want %s, got %#v (%v)", CodeResumeRequestInvalid, refusal, err)
			}
		})
	}
}

func TestResumeHostLostPolicyWireValuesMatchPublicValues(t *testing.T) {
	if string(core.ResumeHostLostRequire) != mutationruntimeRequire || string(core.ResumeHostLostRefuse) != mutationruntimeRefuse {
		t.Fatalf("public %q/%q differ from wire %q/%q", core.ResumeHostLostRequire, core.ResumeHostLostRefuse, mutationruntimeRequire, mutationruntimeRefuse)
	}
}

const (
	mutationruntimeRequire = mutationruntime.ProcessResumeHostLostRequire
	mutationruntimeRefuse  = mutationruntime.ProcessResumeHostLostRefuse
)
