package consumer_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestExternalRegistrySnapshot(t *testing.T) {
	root := t.TempDir()
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")})
	if err != nil {
		t.Fatal(err)
	}
	var result core.RegistrySnapshot
	result, err = c.ReadRegistrySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"apiVersion":"projmux.io/v1alpha1","schemaVersion":5,"updatedAt":"0001-01-01T00:00:00Z","projects":[],"controlSessions":[],"windows":[],"panes":[],"agents":[],"nameReservations":[]}` {
		t.Fatal(string(b))
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("first-use effects: %v %v", entries, err)
	}
}
