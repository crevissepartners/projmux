package resources

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
)

// This raw envelope deliberately bypasses the validating writer. Degraded
// reads must retain duplicate rows instead of normalizing their identity.
func TestLoadRegistrySnapshotRawDuplicates(t *testing.T) {
	raw, err := os.ReadFile("testdata/registry_snapshot.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "metadata"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata", "registry.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(config.Paths{StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Projects) != 3 || len(r.Windows) != 2 || len(r.Agents) != 4 {
		t.Fatalf("rows lost: %d/%d/%d", len(r.Projects), len(r.Windows), len(r.Agents))
	}
	for i, name := range []string{"direct-b", "window-a", "direct-a", "orphan"} {
		if r.Agents[i].Metadata.Name != name || r.Agents[i].Metadata.UID != "agent-duplicate" || r.Agents[i].Status.Phase != "Offline" {
			t.Fatalf("row %d changed: %+v", i, r.Agents[i])
		}
	}
	p, ok := r.Project("proj-a")
	if !ok || p.Metadata.Name != "first-a" {
		t.Fatalf("project first match: %+v", p)
	}
	w, ok := r.Window("win-duplicate")
	if !ok || w.Metadata.Name != "first-window" || w.Metadata.OwnerUID() != "proj-a" {
		t.Fatalf("window first match: %+v", w)
	}
	if r.Agents[0].Status.SessionRef.Codex.Endpoint.StateDomainID != "domain" || r.Projects[0].Metadata.Labels["nested"] != "original" || r.Agents[0].Spec.Workspace.AdditionalWritableRoots[0] != "/extra/direct-b" {
		t.Fatal("nested values lost")
	}
}
