package app

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func processCreatorFixture(t *testing.T) (*createCommand, coremetadata.Registry, coremetadata.ProcessIdentity) {
	t.Helper()
	fx := newCreatorFixture(t)
	reg := fx.store.registry.Clone()
	pane, _ := reg.Pane(fx.creatorPane)
	agent, _ := reg.Agent(fx.creatorAgent)
	child := coremetadata.ProcessIdentity{PID: 7001, OwnerUID: uint32(os.Getuid()), Start: "kernel-birth"}
	binding := coremetadata.ProcessBinding{HostInstanceID: "host", ProjectUID: "prj-alpha", WindowUID: "win-alpha-main", AgentUID: agent.Metadata.UID, PaneUID: pane.Metadata.UID, Generation: "gen-process", OperationID: "op-process"}
	pane.Spec.Runtime.Kind = coremetadata.RuntimeProcess
	pane.Status.Activation = coremetadata.PaneActivation{Kind: coremetadata.RuntimeProcess, AgentUID: binding.AgentUID, Generation: binding.Generation, OperationID: binding.OperationID, Process: &coremetadata.ProcessActivation{Binding: binding, Child: child, HostProcess: coremetadata.ProcessIdentity{PID: 4242, OwnerUID: child.OwnerUID, Start: "host-birth"}}}
	pane.Status.ProcessSession = &coremetadata.ProcessSessionRecord{Provider: agent.Spec.Provider, Binding: binding}
	agent.Status.Phase = coremetadata.PhaseRunning
	fx.command.lookupEnv = func(string) string { return "" }
	fx.command.processCreatorAncestors = func() ([]coremetadata.ProcessIdentity, error) {
		return []coremetadata.ProcessIdentity{{PID: 90001, OwnerUID: child.OwnerUID, Start: "caller"}, child}, nil
	}
	return fx.command, reg, child
}

func TestProcessCreatorEvidenceAndPrecedence(t *testing.T) {
	c, reg, _ := processCreatorFixture(t)
	pane := reg.Panes[len(reg.Panes)-1]
	owner := pane.Metadata.OwnerUID()
	record, err := c.decideCreator(context.Background(), canonicalCreateAgent, &reg, "")
	if err != nil || !maps.Equal(record.annotations(), coremetadata.ProcessCreatorAnnotations(owner, pane.Metadata.UID)) {
		t.Fatalf("record %+v: %v", record, err)
	}
	explicit, err := c.decideCreator(context.Background(), canonicalCreateAgent, &reg, owner)
	if err != nil || explicit.declined != "" || explicit.basis != coremetadata.CreatorBasisProcessChain {
		t.Fatalf("matching declaration %+v %v", explicit, err)
	}
	reg.Agents = append(reg.Agents, coremetadata.Agent{Metadata: coremetadata.ObjectMeta{UID: "agent-other"}})
	different, err := c.decideCreator(context.Background(), canonicalCreateAgent, &reg, "agent-other")
	if err != nil || different.declined != creatorDeclinedProcessChain {
		t.Fatalf("different declaration %+v %v", different, err)
	}
	var stderr bytes.Buffer
	different.report(&stderr)
	if stderr.String() != "creator declaration not recorded: process-chain-disagrees (--creator uid:agent-other)\n" {
		t.Fatal(stderr.String())
	}
	if _, err := c.decideCreator(context.Background(), canonicalCreateAgent, &reg, "missing"); err == nil {
		t.Fatal("missing declaration accepted")
	}
	if err := c.recordOperatorCreator("ui"); err != nil {
		t.Fatal(err)
	}
	c.processCreatorAncestors = func() ([]coremetadata.ProcessIdentity, error) {
		t.Fatal("operator observed processes")
		return nil, nil
	}
	operator, err := c.decideCreator(context.Background(), canonicalCreateAgent, &reg, "")
	if err != nil || operator.basis != coremetadata.CreatorBasisOperator {
		t.Fatalf("operator %+v %v", operator, err)
	}
	fx := newCreatorFixture(t)
	fx.command.processCreatorAncestors = c.processCreatorAncestors
	paneRecord, err := fx.command.decideCreator(context.Background(), canonicalCreateAgent, &fx.store.registry, "")
	if err != nil || paneRecord.basis != coremetadata.CreatorBasisPaneChain {
		t.Fatalf("pane %+v %v", paneRecord, err)
	}
}

func TestProcessCreatorRejectsStaleAndUnrelatedEvidence(t *testing.T) {
	cases := []struct {
		name, skip string
		mutate     func(*createCommand, *coremetadata.Registry, coremetadata.ProcessIdentity)
	}{
		{"unrelated shell", "", func(c *createCommand, r *coremetadata.Registry, id coremetadata.ProcessIdentity) {
			c.processCreatorAncestors = func() ([]coremetadata.ProcessIdentity, error) {
				return []coremetadata.ProcessIdentity{{PID: 999, OwnerUID: id.OwnerUID, Start: "other"}}, nil
			}
		}},
		{"shared host only", "", func(c *createCommand, r *coremetadata.Registry, id coremetadata.ProcessIdentity) {
			c.processCreatorAncestors = func() ([]coremetadata.ProcessIdentity, error) {
				return []coremetadata.ProcessIdentity{r.Panes[len(r.Panes)-1].Status.Activation.Process.HostProcess}, nil
			}
		}},
		{"read failure", "", func(c *createCommand, r *coremetadata.Registry, id coremetadata.ProcessIdentity) {
			c.processCreatorAncestors = func() ([]coremetadata.ProcessIdentity, error) {
				return []coremetadata.ProcessIdentity{id}, errors.New("unobservable")
			}
		}},
		{"PID reused", creatorSkipProcessIdentity, func(c *createCommand, r *coremetadata.Registry, id coremetadata.ProcessIdentity) {
			r.Panes[len(r.Panes)-1].Status.Activation.Process.Child.Start = "old-birth"
		}},
		{"other uid", creatorSkipProcessIdentity, func(c *createCommand, r *coremetadata.Registry, id coremetadata.ProcessIdentity) {
			r.Panes[len(r.Panes)-1].Status.Activation.Process.Child.OwnerUID++
		}},
		{"Offline", creatorSkipProcessBinding, func(c *createCommand, r *coremetadata.Registry, id coremetadata.ProcessIdentity) {
			a, _ := r.Agent(r.Panes[len(r.Panes)-1].Metadata.OwnerUID())
			a.Status.Phase = coremetadata.PhaseOffline
		}},
		{"generation", creatorSkipProcessBinding, func(c *createCommand, r *coremetadata.Registry, id coremetadata.ProcessIdentity) {
			r.Panes[len(r.Panes)-1].Status.Activation.Generation = "stale"
		}},
		{"PaneRef", creatorSkipProcessBinding, func(c *createCommand, r *coremetadata.Registry, id coremetadata.ProcessIdentity) {
			a, _ := r.Agent(r.Panes[len(r.Panes)-1].Metadata.OwnerUID())
			a.Status.PaneRef = "other"
		}},
		{"owner", creatorSkipProcessBinding, func(c *createCommand, r *coremetadata.Registry, id coremetadata.ProcessIdentity) {
			r.Panes[len(r.Panes)-1].Metadata.OwnerRef.UID = "other"
		}},
		{"project binding", creatorSkipProcessBinding, func(c *createCommand, r *coremetadata.Registry, id coremetadata.ProcessIdentity) {
			r.Panes[len(r.Panes)-1].Status.Activation.Process.Binding.ProjectUID = "other"
		}},
		{"session binding", creatorSkipProcessBinding, func(c *createCommand, r *coremetadata.Registry, id coremetadata.ProcessIdentity) {
			r.Panes[len(r.Panes)-1].Status.ProcessSession.Binding.Generation = "stale"
		}},
		{"same child multiple panes", creatorSkipProcessAmbiguous, func(c *createCommand, r *coremetadata.Registry, id coremetadata.ProcessIdentity) {
			p := r.Panes[len(r.Panes)-1].Clone()
			owner, _ := r.Agent(p.Metadata.OwnerUID())
			a := owner.Clone()
			p.Metadata.UID = "pane-second"
			a.Metadata.UID = "agent-second"
			a.Status.PaneRef = p.Metadata.UID
			p.Metadata.OwnerRef.UID = a.Metadata.UID
			b := &p.Status.Activation.Process.Binding
			b.AgentUID = a.Metadata.UID
			b.PaneUID = p.Metadata.UID
			p.Status.Activation.AgentUID = a.Metadata.UID
			p.Status.ProcessSession.Binding = *b
			r.Panes = append(r.Panes, p)
			r.Agents = append(r.Agents, a)
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			c, r, id := processCreatorFixture(t)
			test.mutate(c, &r, id)
			got := c.observeCreator(context.Background(), &r)
			if got.recorded() || got.skip != test.skip {
				t.Fatalf("got %+v want skip %q", got, test.skip)
			}
			var stderr bytes.Buffer
			got.reportSkip(&stderr)
			want := ""
			if test.skip != "" {
				want = "creator not recorded: " + test.skip + "\n"
			}
			if stderr.String() != want {
				t.Fatalf("stderr %q want %q", stderr.String(), want)
			}
		})
	}
}
