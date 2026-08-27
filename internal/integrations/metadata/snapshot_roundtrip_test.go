package metadata

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
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
		Metadata: &sessionstate.ResourceMetadata{
			UID: project.Metadata.UID, Name: project.Metadata.Name, Labels: copySnapshotLabels(project.Metadata.Labels),
		},
	}
	for wi, window := range reg.WindowsOf(projectUID) {
		snapWindow := sessionstate.Window{Index: wi, Name: window.Metadata.Name, Metadata: &sessionstate.ResourceMetadata{
			UID: window.Metadata.UID, Name: window.Metadata.Name, Labels: copySnapshotLabels(window.Metadata.Labels),
			OwnerKind: string(coremetadata.KindProject), OwnerUID: projectUID,
		}}
		for pi, pane := range reg.PanesOf(window.Metadata.UID) {
			recipe := sessionstate.ShellRecipe()
			if pane.Spec.Command != "" {
				recipe = sessionstate.StartupRecipe(pane.Spec.Command)
			}
			snapWindow.Panes = append(snapWindow.Panes, sessionstate.Pane{
				Index:  pi,
				Label:  pane.Metadata.Name,
				CWD:    pane.Spec.CWD,
				Recipe: recipe,
				Metadata: &sessionstate.ResourceMetadata{
					UID: pane.Metadata.UID, Name: pane.Metadata.Name, Labels: copySnapshotLabels(pane.Metadata.Labels),
					OwnerKind: string(coremetadata.KindWindow), OwnerUID: window.Metadata.UID,
				},
			})
		}
		snap.Windows = append(snap.Windows, snapWindow)
	}
	return snap
}

func copySnapshotLabels(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
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

	// A fresh process reloads both stores and projects the restored snapshot
	// back onto the very same desired Registry resources.
	reloadedRegistry, err := NewStore(registryStore.Path()).Load()
	if err != nil {
		t.Fatalf("reload registry: %v", err)
	}
	restored, err := sessionstate.NewStore(snapshotStore.Dir).Load("projmux")
	if err != nil {
		t.Fatalf("reload snapshot: %v", err)
	}

	registryBytes, err := json.Marshal(reloadedRegistry)
	if err != nil {
		t.Fatalf("marshal registry baseline: %v", err)
	}
	uidSourceCalled := false
	projection, err := coremetadata.PlanSnapshotProjection(reloadedRegistry, projectUID, restored, fixedNow, func(coremetadata.Kind) (string, error) {
		uidSourceCalled = true
		return "", errors.New("current snapshot unexpectedly requested a uid")
	})
	if err != nil {
		t.Fatalf("plan snapshot projection: %v", err)
	}
	if uidSourceCalled {
		t.Fatal("current snapshot projection allocated a replacement uid")
	}
	projectedBytes, err := json.Marshal(projection.Desired)
	if err != nil {
		t.Fatalf("marshal projected Registry: %v", err)
	}
	if projection.Changed || !bytes.Equal(projectedBytes, registryBytes) {
		t.Fatalf("projected Registry bytes drifted:\n%s", projectedBytes)
	}

	// anchorPaneRef still resolves to a live registry pane after the whole
	// offline round trip.
	for _, window := range reloadedRegistry.WindowsOf(projectUID) {
		pane, ok := reloadedRegistry.Pane(window.Spec.AnchorPaneRef)
		if !ok {
			t.Fatalf("window %q anchorPaneRef %q does not resolve", window.Metadata.Name, window.Spec.AnchorPaneRef)
		}
		if pane.Metadata.OwnerUID() != window.Metadata.UID {
			t.Fatalf("window %q anchorPaneRef is owned by %q", window.Metadata.Name, pane.Metadata.OwnerUID())
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
