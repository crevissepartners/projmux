package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// The public process-chain judgment equals observeProcessChainActor on the same
// Registry (saved to a file and opened through PrepareRegistrySnapshot) and the
// same ancestry: found, each skip token, nothing to observe, a partial chain, an
// empty chain and a chain over the bound.
func TestProcessChainAgentEqualsObserveProcessChainActor(t *testing.T) {
	caller := coremetadata.ProcessIdentity{PID: 90001, OwnerUID: uint32(os.Getuid()), Start: "caller"}
	last := func(r *coremetadata.Registry) *coremetadata.Pane { return &r.Panes[len(r.Panes)-1] }
	chainOf := func(ids ...coremetadata.ProcessIdentity) func(coremetadata.ProcessIdentity) ([]coremetadata.ProcessIdentity, error) {
		return func(child coremetadata.ProcessIdentity) ([]coremetadata.ProcessIdentity, error) {
			out := []coremetadata.ProcessIdentity{}
			for _, id := range ids {
				if id.PID == 0 {
					id = child
				}
				out = append(out, id)
			}
			return out, nil
		}
	}
	cases := []struct {
		name   string
		mutate func(*coremetadata.Registry)
		chain  func(coremetadata.ProcessIdentity) ([]coremetadata.ProcessIdentity, error)
	}{
		{"found", nil, chainOf(caller, coremetadata.ProcessIdentity{})},
		{"nothing to observe", nil, chainOf(caller)},
		{"empty chain", nil, func(coremetadata.ProcessIdentity) ([]coremetadata.ProcessIdentity, error) { return nil, nil }},
		{"partial chain", nil, func(c coremetadata.ProcessIdentity) ([]coremetadata.ProcessIdentity, error) {
			return []coremetadata.ProcessIdentity{c}, errors.New("unobservable")
		}},
		{"partial chain without a child", nil, func(coremetadata.ProcessIdentity) ([]coremetadata.ProcessIdentity, error) {
			return []coremetadata.ProcessIdentity{caller}, errors.New("unobservable")
		}},
		{"over the bound", nil, func(c coremetadata.ProcessIdentity) ([]coremetadata.ProcessIdentity, error) {
			chain := make([]coremetadata.ProcessIdentity, coremetadata.ProcessChainMaxSteps+1)
			chain[0] = c
			return chain, nil
		}},
		{"pid reused", nil, func(c coremetadata.ProcessIdentity) ([]coremetadata.ProcessIdentity, error) {
			c.Start = "old-birth"
			return []coremetadata.ProcessIdentity{c}, nil
		}},
		{"other uid", func(r *coremetadata.Registry) { last(r).Status.Activation.Process.Child.OwnerUID++ }, chainOf(coremetadata.ProcessIdentity{})},
		{"offline", func(r *coremetadata.Registry) {
			a, _ := r.Agent(last(r).Metadata.OwnerUID())
			a.Status.Phase = coremetadata.PhaseOffline
		}, chainOf(coremetadata.ProcessIdentity{})},
		{"ambiguous", func(r *coremetadata.Registry) {
			p := last(r).Clone()
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
		}, chainOf(coremetadata.ProcessIdentity{})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, reg, child := processCreatorFixture(t)
			if tc.mutate != nil {
				tc.mutate(&reg)
			}
			chain, chainErr := tc.chain(child)
			want := observeProcessChainActor(&reg, func() ([]coremetadata.ProcessIdentity, error) { return chain, chainErr })

			state := filepath.Join(t.TempDir(), "state")
			raw, err := json.Marshal(reg)
			if err != nil {
				t.Fatal(err)
			}
			path := store.PathFor(state)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o644); err != nil {
				t.Fatal(err)
			}
			client, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(filepath.Dir(state), "config"), StateDir: state})
			if err != nil {
				t.Fatal(err)
			}
			prepared, _, err := client.PrepareRegistrySnapshot(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			publicChain := make([]core.ProcessIdentity, len(chain))
			for i, id := range chain {
				publicChain[i] = core.ProcessIdentity(id)
			}
			agent, skip := prepared.ResolveProcessChainAgent(publicChain, chainErr)
			if agent.AgentUID != want.agentUID || agent.PaneUID != want.paneUID || string(skip) != want.skip {
				t.Fatalf("public %+v %q, app %+v", agent, skip, want)
			}
		})
	}
}
