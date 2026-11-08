package projmux

import (
	"errors"
	"os"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// The fixture Agent is agent-01 on pane-02 with child pid 12; the caller uid is
// the test process's, because the SDK reads it itself.
func processChainFixture(t *testing.T, change func(*metadata.Registry)) (*PreparedRegistrySnapshot, metadata.Registry, core.ProcessIdentity) {
	t.Helper()
	uid := uint32(os.Getuid())
	prepared, loaded := lookupFixture(t, func(r *metadata.Registry) {
		r.Panes[1].Status.Activation.Process.Child.OwnerUID = uid
		if change != nil {
			change(r)
		}
	})
	return prepared, loaded, core.ProcessIdentity{PID: 12, OwnerUID: uid, Start: "child-start"}
}

func metadataChain(chain []core.ProcessIdentity) []metadata.ProcessIdentity {
	out := make([]metadata.ProcessIdentity, len(chain))
	for i := range chain {
		out[i] = metadata.ProcessIdentity(chain[i])
	}
	return out
}

// The public answer equals the Registry's own judgment, which the CLI creator
// provenance shares, for found, every skip token and nothing to observe.
func TestProcessChainAgentEqualsRegistryJudgment(t *testing.T) {
	caller := core.ProcessIdentity{PID: 90001, OwnerUID: uint32(os.Getuid()), Start: "caller"}
	cases := []struct {
		name   string
		change func(*metadata.Registry)
		chain  func(child core.ProcessIdentity) ([]core.ProcessIdentity, error)
		agent  ProcessChainAgent
		skip   ProcessChainSkip
	}{
		{"found", nil, func(c core.ProcessIdentity) ([]core.ProcessIdentity, error) {
			return []core.ProcessIdentity{caller, c}, nil
		},
			ProcessChainAgent{AgentUID: "agent-01", PaneUID: "pane-02"}, ""},
		{"unrelated shell", nil, func(core.ProcessIdentity) ([]core.ProcessIdentity, error) { return []core.ProcessIdentity{caller}, nil }, ProcessChainAgent{}, ""},
		{"empty chain", nil, func(core.ProcessIdentity) ([]core.ProcessIdentity, error) { return nil, nil }, ProcessChainAgent{}, ""},
		{"partial chain with the child", nil, func(c core.ProcessIdentity) ([]core.ProcessIdentity, error) {
			return []core.ProcessIdentity{c}, errors.New("unobservable")
		}, ProcessChainAgent{}, ProcessChainSkipUnobservable},
		{"partial chain without a child", nil, func(core.ProcessIdentity) ([]core.ProcessIdentity, error) {
			return []core.ProcessIdentity{caller}, errors.New("unobservable")
		}, ProcessChainAgent{}, ""},
		{"over the bound", nil, func(c core.ProcessIdentity) ([]core.ProcessIdentity, error) {
			chain := make([]core.ProcessIdentity, ProcessAncestryMaxSteps+1)
			chain[0] = c
			return chain, nil
		}, ProcessChainAgent{}, ProcessChainSkipUnobservable},
		{"pid reused", nil, func(c core.ProcessIdentity) ([]core.ProcessIdentity, error) {
			c.Start = "other-birth"
			return []core.ProcessIdentity{c}, nil
		}, ProcessChainAgent{}, ProcessChainSkipIdentityMismatch},
		{"other uid", func(r *metadata.Registry) { r.Panes[1].Status.Activation.Process.Child.OwnerUID++ },
			func(c core.ProcessIdentity) ([]core.ProcessIdentity, error) {
				c.OwnerUID++
				return []core.ProcessIdentity{c}, nil
			},
			ProcessChainAgent{}, ProcessChainSkipIdentityMismatch},
		{"offline", func(r *metadata.Registry) { r.Agents[0].Status.Phase = metadata.PhaseOffline },
			func(c core.ProcessIdentity) ([]core.ProcessIdentity, error) { return []core.ProcessIdentity{c}, nil },
			ProcessChainAgent{}, ProcessChainSkipBindingMismatch},
		{"ambiguous", func(r *metadata.Registry) {
			p := r.Panes[1].Clone()
			owner, _ := r.Agent(p.Metadata.OwnerUID())
			a := owner.Clone()
			p.Metadata.UID, a.Metadata.UID = "pane-second", "agent-second"
			a.Status.PaneRef = p.Metadata.UID
			p.Metadata.OwnerRef.UID = a.Metadata.UID
			b := &p.Status.Activation.Process.Binding
			b.AgentUID, b.PaneUID = a.Metadata.UID, p.Metadata.UID
			p.Status.Activation.AgentUID = a.Metadata.UID
			p.Status.ProcessSession.Binding = *b
			r.Panes = append(r.Panes, p)
			r.Agents = append(r.Agents, a)
		}, func(c core.ProcessIdentity) ([]core.ProcessIdentity, error) { return []core.ProcessIdentity{c}, nil },
			ProcessChainAgent{}, ProcessChainSkipAmbiguous},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prepared, loaded, child := processChainFixture(t, tc.change)
			chain, chainErr := tc.chain(child)
			agent, skip := prepared.ResolveProcessChainAgent(chain, chainErr)
			if agent != tc.agent || skip != tc.skip {
				t.Fatalf("got %+v %q, want %+v %q", agent, skip, tc.agent, tc.skip)
			}
			wantAgent, wantPane, wantSkip := loaded.ResolveProcessChainActor(metadataChain(chain), chainErr, uint32(os.Getuid()))
			if agent.AgentUID != wantAgent || agent.PaneUID != wantPane || string(skip) != string(wantSkip) {
				t.Fatalf("public %+v %q differs from the Registry judgment %q %q %q", agent, skip, wantAgent, wantPane, wantSkip)
			}
			if (agent.AgentUID != "") && skip != "" {
				t.Fatalf("found and skipped at once: %+v %q", agent, skip)
			}
		})
	}
}

// A handle that was not prepared, and a nil one, observe nothing.
func TestProcessChainAgentUnpreparedHandleObservesNothing(t *testing.T) {
	chain := []core.ProcessIdentity{{PID: 12, OwnerUID: uint32(os.Getuid()), Start: "child-start"}}
	for name, handle := range map[string]*PreparedRegistrySnapshot{"zero": {}, "nil": nil} {
		agent, skip := handle.ResolveProcessChainAgent(chain, nil)
		if agent != (ProcessChainAgent{}) || skip != "" {
			t.Fatalf("%s handle = %+v %q, want nothing", name, agent, skip)
		}
	}
}

// The four tokens are public spelling; the core sentence is the web's, not the SDK's.
func TestProcessChainSkipSpellingIsPinned(t *testing.T) {
	for token, want := range map[ProcessChainSkip]string{
		ProcessChainSkipUnobservable:     "process-chain-unobservable",
		ProcessChainSkipIdentityMismatch: "process-child-identity-mismatch",
		ProcessChainSkipBindingMismatch:  "process-binding-mismatch",
		ProcessChainSkipAmbiguous:        "process-child-ambiguous",
	} {
		if string(token) != want || string(token) != string(metadata.ProcessChainSkip(want)) || strings.ContainsAny(want, " :") {
			t.Fatalf("token %q, want %q", token, want)
		}
	}
	if ProcessAncestryMaxSteps != metadata.ProcessChainMaxSteps {
		t.Fatalf("ancestry bound %d differs from the chain bound %d", ProcessAncestryMaxSteps, metadata.ProcessChainMaxSteps)
	}
}
