package metadata

import (
	"errors"
	"reflect"
	"testing"
)

func TestRetainedWindowLifecycleDeterministicallyReanchorsAndClearsDefault(t *testing.T) {
	t.Parallel()

	mutator := testMutator(dirSet{"/srv/alpha": true})
	registry := NewRegistry()
	registered, err := registerFixture(mutator, &registry, "/srv/alpha")
	if err != nil {
		t.Fatal(err)
	}
	window := registered.Windows[0]
	shell := registered.Panes[0]
	agent, err := mutator.CreateAgent(&registry, window.Metadata.UID, CreateAgentOptions{Provider: "codex", OperationID: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	managed, err := mutator.AttachAgentPane(&registry, agent.Metadata.UID, BootstrapPane{}, "attach")
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := registry.Window(window.Metadata.UID)
	stored.Spec.AnchorPaneRef = managed.Metadata.UID

	if err := mutator.DeletePane(&registry, shell.Metadata.UID); err != nil {
		t.Fatal(err)
	}
	stored, _ = registry.Window(window.Metadata.UID)
	if stored.Metadata.Name != window.Metadata.Name || stored.Spec.AnchorPaneRef != managed.Metadata.UID || stored.Spec.DefaultShellPaneRef != "" {
		t.Fatalf("retained Window refs = %+v", stored)
	}
	if err := registry.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRetainedWindowLifecycleReplacementFailureRestoresExactPreimage(t *testing.T) {
	t.Parallel()

	mutator := testMutator(dirSet{"/srv/alpha": true})
	registry := NewRegistry()
	registered, err := registerFixture(mutator, &registry, "/srv/alpha")
	if err != nil {
		t.Fatal(err)
	}
	before := registry.Clone()
	mutator.NewUID = func(Kind) (string, error) { return "", errors.New("injected replacement uid failure") }
	if err := mutator.DeletePane(&registry, registered.Panes[0].Metadata.UID); err == nil {
		t.Fatal("replacement failure was accepted")
	}
	if !reflect.DeepEqual(registry, before) {
		t.Fatal("replacement failure changed Registry preimage")
	}
}

func TestReleaseAnchorAgentRetainsAgentAndWindowWithReplacementShell(t *testing.T) {
	t.Parallel()

	mutator := testMutator(dirSet{"/srv/alpha": true})
	registry := NewRegistry()
	registered, err := registerFixture(mutator, &registry, "/srv/alpha")
	if err != nil {
		t.Fatal(err)
	}
	window := registered.Windows[0]
	if err := mutator.DeletePane(&registry, registered.Panes[0].Metadata.UID); err != nil {
		t.Fatal(err)
	}
	replacement, _ := registry.WindowDefaultShell(window.Metadata.UID)
	agent, err := mutator.CreateAgent(&registry, window.Metadata.UID, CreateAgentOptions{Provider: "codex", OperationID: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	managed, err := mutator.AttachAgentPane(&registry, agent.Metadata.UID, BootstrapPane{}, "attach")
	if err != nil {
		t.Fatal(err)
	}
	if err := mutator.DeletePane(&registry, replacement.Metadata.UID); err != nil {
		t.Fatal(err)
	}
	stored, _ := registry.Window(window.Metadata.UID)
	stored.Spec.AnchorPaneRef = managed.Metadata.UID

	retained, err := mutator.ReleaseAgentPane(&registry, agent.Metadata.UID, AgentExitNormal, "clean exit")
	if err != nil {
		t.Fatal(err)
	}
	stored, _ = registry.Window(window.Metadata.UID)
	anchor, ok := registry.Pane(stored.Spec.AnchorPaneRef)
	if retained.Status.Phase != PhaseOffline || retained.Status.PaneRef != "" || !ok ||
		anchor.Spec.Role != PaneRoleShell || stored.Metadata.UID != window.Metadata.UID || stored.Metadata.Name != window.Metadata.Name {
		t.Fatalf("retained lifecycle = agent:%+v window:%+v anchor:%+v", retained, stored, anchor)
	}
	if err := registry.Validate(); err != nil {
		t.Fatal(err)
	}
}
