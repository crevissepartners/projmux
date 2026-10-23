package app

import (
	"errors"
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
)

type managedRecordFixture struct {
	reg      *coremetadata.Registry
	owner    managedCodexBaseOwner
	endpoint *codexProcessEndpoint
	base     *mutationruntime.ManagedCodexBase
}

// newManagedRecordFixture is the Registry right after one operation's provider
// accepted recipe B on generation gen-1, before its confirmation is recorded.
func newManagedRecordFixture(t *testing.T) managedRecordFixture {
	t.Helper()
	binding := processhost.Binding{Host: "op-1", Project: "proj-1", Window: "win-1", Agent: "agent-1", Pane: "pane-1", Generation: "gen-1", Operation: "op-1"}
	recorded := map[string]string{coremetadata.AnnotationAgentPersona: "role-A", coremetadata.AnnotationAgentPersonaDigest: "digest-A", "unrelated": "kept"}
	reg := &coremetadata.Registry{
		Agents: []coremetadata.Agent{{Metadata: coremetadata.ObjectMeta{UID: binding.Agent, Annotations: maps.Clone(recorded)}, Status: coremetadata.AgentStatus{Phase: coremetadata.PhaseRunning, PaneRef: binding.Pane}}},
		Panes: []coremetadata.Pane{{Metadata: coremetadata.ObjectMeta{UID: binding.Pane}, Status: coremetadata.PaneStatus{
			ProcessSession: &coremetadata.ProcessSessionRecord{Provider: aiModeCodex, Binding: metadataProcessBinding(binding), ThreadID: "thread-1"},
			Activation:     coremetadata.PaneActivation{Process: &coremetadata.ProcessActivation{Binding: metadataProcessBinding(binding)}},
		}}},
	}
	endpoint := &codexProcessEndpoint{binding: binding}
	endpoint.evidence.ThreadID = "thread-1"
	paths := config.Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir()}
	base, err := mutationruntime.PrepareManagedCodexBase(paths, binding.Agent, "recipe B", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	base.Instructions.Accepted = true
	base.Annotations = map[string]string{coremetadata.AnnotationAgentPersona: "role-B", coremetadata.AnnotationAgentPersonaDigest: "digest-B", "unrelated": "kept"}
	return managedRecordFixture{reg: reg, owner: managedCodexBaseOwner{binding: binding, recipe: managedCodexRecipe(recorded)}, endpoint: endpoint, base: base}
}

func TestManagedCodexBaseConfirmationRecordsSameGeneration(t *testing.T) {
	f := newManagedRecordFixture(t)
	if err := confirmManagedCodexBase(f.reg, f.base, f.owner, f.endpoint, false); err != nil {
		t.Fatal(err)
	}
	agent, _ := f.reg.Agent(f.owner.binding.Agent)
	got := agent.Metadata.Annotations
	if got[coremetadata.AnnotationAgentPersona] != "role-B" || got[coremetadata.AnnotationAgentPersonaDigest] != "digest-B" || got[mutationruntime.CodexManagedBaseDigest] != f.base.Digest || got[mutationruntime.CodexManagedBaseStatus] != "base-delivered" || got["unrelated"] != "kept" {
		t.Fatalf("same-generation confirmation not recorded: %v", got)
	}
}

// A late confirmation from a replaced generation or recipe must not overwrite
// what the Registry now records for the current one.
func TestManagedCodexBaseLateConfirmationIsStale(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*managedRecordFixture)
	}{
		{"next generation", func(f *managedRecordFixture) {
			next := f.owner.binding
			next.Generation, next.Operation, next.Host = "gen-2", "op-2", "op-2"
			pane, _ := f.reg.Pane(next.Pane)
			pane.Status.ProcessSession.Binding = metadataProcessBinding(next)
			pane.Status.Activation.Process.Binding = metadataProcessBinding(next)
		}},
		{"stopped after Wait", func(f *managedRecordFixture) {
			agent, _ := f.reg.Agent(f.owner.binding.Agent)
			agent.Status.Phase, agent.Status.PaneRef = coremetadata.PhaseOffline, ""
			pane, _ := f.reg.Pane(f.owner.binding.Pane)
			pane.Status.Activation.Process = nil
		}},
		{"thread changed", func(f *managedRecordFixture) {
			pane, _ := f.reg.Pane(f.owner.binding.Pane)
			pane.Status.ProcessSession.ThreadID = "thread-2"
		}},
		{"recipe changed", func(f *managedRecordFixture) {
			agent, _ := f.reg.Agent(f.owner.binding.Agent)
			agent.Metadata.Annotations[coremetadata.AnnotationAgentPersonaDigest] = "digest-C"
			agent.Metadata.Annotations[mutationruntime.CodexManagedBaseDigest] = "managed-C"
			agent.Metadata.Annotations[mutationruntime.CodexManagedBaseStatus] = "base-delivered"
		}},
		{"foreign endpoint", func(f *managedRecordFixture) {
			f.endpoint.binding.Generation = "gen-2"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newManagedRecordFixture(t)
			tc.change(&f)
			agent, _ := f.reg.Agent(f.owner.binding.Agent)
			before := maps.Clone(agent.Metadata.Annotations)
			err := confirmManagedCodexBase(f.reg, f.base, f.owner, f.endpoint, false)
			if !errors.Is(err, processhost.ErrStale) {
				t.Fatalf("late confirmation was not refused as stale: %v", err)
			}
			if !maps.Equal(agent.Metadata.Annotations, before) {
				t.Fatalf("stale confirmation changed annotations: before=%v after=%v", before, agent.Metadata.Annotations)
			}
		})
	}
}

// The three ordinary process lifecycles must confirm through the same
// ownership and recipe check rather than writing the managed recipe directly.
func TestManagedCodexBaseCallersUseOwnershipCheck(t *testing.T) {
	for _, file := range []string{"create_agent_process.go", "process_agent_resume.go", "agent_relaunch_process.go"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		if strings.Count(source, "captureManagedCodexBaseOwner(") != 1 || strings.Count(source, "recordManagedCodexBase(") != 1 {
			t.Fatalf("%s does not capture and confirm the managed base exactly once", file)
		}
		for _, direct := range []string{"managed.Record(", "managedBase.Record("} {
			if strings.Contains(source, direct) {
				t.Fatalf("%s writes the managed recipe without the ownership check: %s", file, direct)
			}
		}
	}
}
