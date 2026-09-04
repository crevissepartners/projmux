package metadata

import (
	"errors"
	"reflect"
	"testing"
)

// orphanPaneFixture registers one Project and returns the registry together
// with the uid of its bootstrap Window, which is the Window an orphan pane is
// minted into.
func orphanPaneFixture(t *testing.T) (Mutator, *Registry, string) {
	t.Helper()

	root := "/tmp/alpha"
	mutator := testMutator(dirSet{root: true})
	registry := NewRegistry()
	result, err := registerFixture(mutator, &registry, root)
	if err != nil {
		t.Fatalf("register project fixture: %v", err)
	}
	windows := registry.WindowsOf(result.Project.Metadata.UID)
	if len(windows) != 1 {
		t.Fatalf("fixture Project owns %d Windows, want 1", len(windows))
	}
	return mutator, &registry, windows[0].Metadata.UID
}

// TestImportOrphanPaneNamesUseExactUIDAndNeverRuntimeContext is the naming
// contract of the phase: `pane_current_command` is what the operator happens to
// be running right now, so it cannot participate in a durable address.
func TestImportOrphanPaneNamesUseExactUIDAndNeverRuntimeContext(t *testing.T) {
	t.Parallel()

	mutator, registry, windowUID := orphanPaneFixture(t)

	first, err := mutator.ImportOrphanPane(registry, windowUID, LegacyPane{
		Label:   "lead-roadmap",
		Command: "claude",
		Title:   "claude - working",
	}, "op-1")
	if err != nil {
		t.Fatalf("import orphan pane: %v", err)
	}
	if first.Metadata.Name != first.Metadata.UID {
		t.Fatalf("minted Pane name = %q, want exact uid %q", first.Metadata.Name, first.Metadata.UID)
	}

	// A second orphan receives another full uid, never a numeric name suffix.
	second, err := mutator.ImportOrphanPane(registry, windowUID, LegacyPane{Command: "zsh"}, "op-1")
	if err != nil {
		t.Fatalf("import second orphan pane: %v", err)
	}
	if second.Metadata.Name != second.Metadata.UID {
		t.Fatalf("second minted Pane name = %q, want exact uid %q", second.Metadata.Name, second.Metadata.UID)
	}
	if second.Metadata.UID == first.Metadata.UID {
		t.Fatalf("two orphan panes were minted onto one uid %q", first.Metadata.UID)
	}
}

// TestImportOrphanPaneMintsAShellPaneAndNeverAnAgent pins the fixed decision
// that an AI pane is registered as a plain shell Pane.
//
// Deciding a Window owes an Agent resource because the pane options say
// `claude` is a content heuristic choosing registry topology, which is the
// inference direction this whole track refuses. Agent phase is an adjacent
// track's concern.
func TestImportOrphanPaneMintsAShellPaneAndNeverAnAgent(t *testing.T) {
	t.Parallel()

	mutator, registry, windowUID := orphanPaneFixture(t)

	pane, err := mutator.ImportOrphanPane(registry, windowUID, LegacyPane{
		Provider: "claude",
		Topic:    "roadmap",
		Command:  "claude",
	}, "op-1")
	if err != nil {
		t.Fatalf("import orphan pane: %v", err)
	}
	if pane.Spec.Role != PaneRoleShell {
		t.Fatalf("minted Pane role = %q, want %q", pane.Spec.Role, PaneRoleShell)
	}
	if owner := pane.Metadata.OwnerUID(); owner != windowUID {
		t.Fatalf("minted Pane owner = %q, want the paired Window %q", owner, windowUID)
	}
	if pane.Metadata.OwnerRef.Kind != KindWindow {
		t.Fatalf("minted Pane owner kind = %q, want %q", pane.Metadata.OwnerRef.Kind, KindWindow)
	}
	if len(registry.Agents) != 0 {
		t.Fatalf("an AI-looking pane minted %d Agents, want 0", len(registry.Agents))
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("registry does not validate after minting an orphan Pane: %v", err)
	}
}

// TestImportOrphanPaneCWDFallsBackToTheOwningProjectRoot keeps the minted Pane
// answerable to `get pane -o cwd` even when the observation carried no
// `pane_current_path`, using the same fallback AddPane already uses.
func TestImportOrphanPaneCWDFallsBackToTheOwningProjectRoot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		observed string
		want     string
	}{
		{name: "observed cwd is kept", observed: "/tmp/alpha/sub", want: "/tmp/alpha/sub"},
		{name: "blank cwd falls back to the project root", observed: "", want: "/tmp/alpha"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mutator, registry, windowUID := orphanPaneFixture(t)
			pane, err := mutator.ImportOrphanPane(registry, windowUID, LegacyPane{CWD: tt.observed}, "op-1")
			if err != nil {
				t.Fatalf("import orphan pane: %v", err)
			}
			if pane.Spec.CWD != tt.want {
				t.Fatalf("minted Pane cwd = %q, want %q", pane.Spec.CWD, tt.want)
			}
		})
	}
}

// TestImportOrphanPaneRefusesAnUnknownWindowWithZeroWrites keeps a caller that
// lost its Window between the match and the mint from mutating anything. The
// binding-repair path treats the error as "skip this pane", so a partial write
// here would be a silent orphan of a different shape.
func TestImportOrphanPaneRefusesAnUnknownWindowWithZeroWrites(t *testing.T) {
	t.Parallel()

	mutator, registry, _ := orphanPaneFixture(t)
	before := registry.Clone()

	if _, err := mutator.ImportOrphanPane(registry, "win-does-not-exist", LegacyPane{}, "op-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	if !reflect.DeepEqual(*registry, before) {
		t.Fatalf("a refused mint still wrote to the registry")
	}
}
