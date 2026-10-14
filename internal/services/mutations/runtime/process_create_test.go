package runtime

import (
	"errors"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestDetachedCreateVerifierRejectsStaleAndAmbiguous(t *testing.T) {
	b := processhost.Binding{Host: "host", Project: "project", Window: "window", Agent: "agent", Pane: "pane", Generation: "generation", Operation: "operation"}
	schema := processCreateSchemaBinding(b)
	owner := metadata.ProcessIdentity{PID: 1, OwnerUID: 1000, Start: "birth"}
	pane := metadata.Pane{Metadata: metadata.ObjectMeta{UID: "pane", OwnerRef: &metadata.OwnerRef{Kind: metadata.KindAgent, UID: "agent"}}, Spec: metadata.PaneSpec{Role: metadata.PaneRoleAgent, Runtime: metadata.PaneRuntimeSpec{Kind: metadata.RuntimeProcess}}, Status: metadata.PaneStatus{ProcessSession: &metadata.ProcessSessionRecord{Provider: "claude", Binding: schema}, Activation: metadata.PaneActivation{Kind: metadata.RuntimeProcess, Generation: "generation", AgentUID: "agent", OperationID: "operation", Process: &metadata.ProcessActivation{Binding: schema, HostProcess: owner, Child: metadata.ProcessIdentity{PID: 2, OwnerUID: 1000, Start: "child-birth"}}}}}
	agent := metadata.Agent{Metadata: metadata.ObjectMeta{UID: "agent", OwnerRef: &metadata.OwnerRef{Kind: metadata.KindWindow, UID: "window"}}, Spec: metadata.AgentSpec{Provider: "claude"}, Status: metadata.AgentStatus{PaneRef: "pane", Phase: metadata.PhaseRunning}}
	reg := metadata.Registry{Projects: []metadata.Project{{Metadata: metadata.ObjectMeta{UID: "project"}}}, Windows: []metadata.Window{{Metadata: metadata.ObjectMeta{UID: "window", OwnerRef: &metadata.OwnerRef{Kind: metadata.KindProject, UID: "project"}}}}, Agents: []metadata.Agent{agent}, Panes: []metadata.Pane{pane}}
	reply := ProcessDetachedReply{Owner: owner, Binding: b, Result: &ProcessCreateCommitted{Agent: agent, Pane: pane}}
	load := func() (metadata.Registry, error) { return reg, nil }
	if err := VerifyDetachedProcessCreate(load, reply, owner); err != nil {
		t.Fatalf("positive control %v", err)
	}
	changed := reg.Clone()
	changed.Panes[0].Status.ProcessSession.Binding.Generation = "replacement"
	if err := VerifyDetachedProcessCreate(func() (metadata.Registry, error) { return changed, nil }, reply, owner); !errors.Is(err, processhost.ErrStale) {
		t.Fatal("stale accepted", err)
	}
	duplicate := pane
	duplicate.Metadata.UID = "another"
	reg.Panes = append(reg.Panes, duplicate)
	if err := VerifyDetachedProcessCreate(load, reply, owner); !errors.Is(err, processhost.ErrStale) {
		t.Fatal("ambiguous accepted", err)
	}
	reg.Panes = reg.Panes[:1]
	wrong := owner
	wrong.Start = "replacement"
	if err := VerifyDetachedProcessCreate(load, reply, wrong); !errors.Is(err, processhost.ErrStale) {
		t.Fatal("wrong owner accepted", err)
	}
	reply.Result.Pane.Status.ProcessSession.Binding.Generation = "replacement"
	if err := VerifyDetachedProcessCreate(load, reply, owner); !errors.Is(err, processhost.ErrStale) {
		t.Fatal("wrong reply accepted", err)
	}
}
