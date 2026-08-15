package metadata

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
)

// snapshotFor renders the session snapshot a project's offline topology would
// produce, then stamps resource metadata onto it.
func snapshotFor(t *testing.T, reg *coremetadata.Registry, projectUID, session string) sessionstate.Snapshot {
	t.Helper()
	project, ok := reg.Project(projectUID)
	if !ok {
		t.Fatalf("project %q not found", projectUID)
	}
	snap := sessionstate.Snapshot{
		Version:    sessionstate.Version,
		Session:    session,
		Source:     sessionstate.SourceFresh,
		DefaultCWD: project.Spec.Root,
		SavedAt:    fixedNow,
	}
	for wi, window := range reg.WindowsOf(projectUID) {
		snapWindow := sessionstate.Window{Index: wi, Name: window.Metadata.Name}
		for pi, pane := range reg.PanesOf(window.Metadata.UID) {
			snapWindow.Panes = append(snapWindow.Panes, sessionstate.Pane{
				Index:  pi,
				Label:  pane.Metadata.Name,
				CWD:    pane.Spec.CWD,
				Recipe: sessionstate.ShellRecipe(),
			})
		}
		snap.Windows = append(snap.Windows, snapWindow)
	}
	if err := coremetadata.AttachSnapshotMetadata(reg, projectUID, &snap); err != nil {
		t.Fatalf("attach snapshot metadata: %v", err)
	}
	return snap
}

func TestOfflineSnapshotRoundTripsThroughBothStoresAndKeepsTheSameLogicalResources(t *testing.T) {
	t.Parallel()

	roots := map[string]bool{"/src/projmux": true}
	m := testMutator(roots)
	registryStore := testStore(t)
	snapshotStore := sessionstate.NewStore(t.TempDir())

	var projectUID string
	registry, err := registryStore.Update(func(reg *coremetadata.Registry) error {
		result, err := m.RegisterProject(reg, coremetadata.RegisterProjectOptions{
			Root:         "/src/projmux",
			DefaultShell: "/bin/zsh",
			Topology: []coremetadata.BootstrapWindow{
				{Command: "nvim"},
				{Name: "server", Panes: []coremetadata.BootstrapPane{{Command: "npm run dev"}, {Command: "htop"}}},
			},
			Labels:      map[string]string{"tier": "primary"},
			SessionName: "projmux",
			OperationID: "op-e2e",
		})
		if err != nil {
			return err
		}
		projectUID = result.Project.Metadata.UID
		return nil
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	snap := snapshotFor(t, &registry, projectUID, "projmux")
	if err := snapshotStore.Save(snap); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}

	// The snapshot file carries the resource metadata blocks verbatim.
	raw, err := os.ReadFile(filepath.Join(snapshotStore.Dir, "projmux.json"))
	if err != nil {
		t.Fatalf("read snapshot file: %v", err)
	}
	for _, want := range []string{`"uid": "project-01"`, `"uid": "window-01"`, `"uid": "pane-01"`, `"owner_uid": "project-01"`, `"tier": "primary"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("snapshot file is missing %s:\n%s", want, raw)
		}
	}

	// A fresh process reloads both stores and reconciles the restored
	// snapshot back onto the very same resources.
	reloadedRegistry, err := NewStore(registryStore.Path()).Load()
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	restored, err := sessionstate.NewStore(snapshotStore.Dir).Load("projmux")
	if err != nil {
		t.Fatalf("reload snapshot: %v", err)
	}

	reconciled := coremetadata.ReconcileSnapshot(&reloadedRegistry, restored)
	if reconciled.ProjectUID != projectUID || reconciled.ProjectMatch != coremetadata.MatchUID {
		t.Fatalf("project reconciliation = %+v", reconciled)
	}
	if len(reconciled.Windows) != 2 || len(reconciled.Panes) != 3 {
		t.Fatalf("bindings = %d windows / %d panes, want 2/3", len(reconciled.Windows), len(reconciled.Panes))
	}
	for _, binding := range append(append([]coremetadata.SnapshotBinding{}, reconciled.Windows...), reconciled.Panes...) {
		if binding.Match != coremetadata.MatchUID {
			t.Fatalf("%s %d/%d matched by %q, want uid", binding.Kind, binding.WindowIndex, binding.PaneIndex, binding.Match)
		}
	}

	// primaryPaneRef still resolves to a live registry pane after the whole
	// offline round trip.
	for _, window := range reloadedRegistry.WindowsOf(projectUID) {
		pane, ok := reloadedRegistry.Pane(window.Spec.PrimaryPaneRef)
		if !ok {
			t.Fatalf("window %q primaryPaneRef %q does not resolve", window.Metadata.Name, window.Spec.PrimaryPaneRef)
		}
		if pane.Metadata.OwnerUID() != window.Metadata.UID {
			t.Fatalf("window %q primaryPaneRef is owned by %q", window.Metadata.Name, pane.Metadata.OwnerUID())
		}
	}
}

func TestASnapshotSavedWithoutResourceMetadataStaysByteCompatible(t *testing.T) {
	t.Parallel()

	store := sessionstate.NewStore(t.TempDir())
	snap := sessionstate.Snapshot{
		Version:    sessionstate.Version,
		Session:    "legacy",
		Source:     sessionstate.SourceAutosave,
		DefaultCWD: "/src/projmux",
		SavedAt:    fixedNow,
		Windows: []sessionstate.Window{{
			Index: 0,
			Name:  "zsh",
			Panes: []sessionstate.Pane{{Index: 0, CWD: "/src/projmux", Recipe: sessionstate.ShellRecipe()}},
		}},
	}
	if err := store.Save(snap); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(store.Dir, "legacy.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), `"metadata"`) {
		t.Fatalf("a snapshot without resource metadata must not gain a metadata key:\n%s", raw)
	}
	if _, err := store.Load("legacy"); err != nil {
		t.Fatalf("load: %v", err)
	}
}
