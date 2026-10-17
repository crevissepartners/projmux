package agentguidance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	guidance "github.com/crevissepartners/projmux/internal/core/agentguidance"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	storemetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/resources"
)

func TestProjectGuidanceDegradedRegistryReadOnly(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"inactive", "graph-invalid", "missing-project", "malformed", "unsupported", "lost"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			paths := config.Paths{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")}
			service := Service{Paths: func() (config.Paths, error) { return paths, nil }, RegistryPaths: func() (config.Paths, error) { return paths, nil }}
			reg := metadata.NewRegistry()
			uid := "proj-aaaaaaaaaaaaaaaaaaaaaaaaaa"
			reg.Projects = []metadata.Project{{APIVersion: metadata.APIVersion, Kind: metadata.KindProject, Metadata: metadata.ObjectMeta{UID: uid, Name: "sample"}, Spec: metadata.ProjectSpec{Root: root}}}
			// No session projection: this registered target is inactive.
			if kind == "graph-invalid" {
				reg.Projects[0].Spec.PrimaryWindowRef = "win-bbbbbbbbbbbbbbbbbbbbbbbbbb"
			}
			if kind == "unsupported" {
				reg.SchemaVersion = metadata.SchemaVersion + 1
			}
			raw, err := json.Marshal(reg)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "malformed" {
				raw = []byte("{bad")
			}
			registryPath := storemetadata.NewDefaultStore(paths).Path()
			if err := os.MkdirAll(filepath.Dir(registryPath), 0700); err != nil {
				t.Fatal(err)
			}
			if kind == "lost" {
				if err := os.WriteFile(filepath.Join(filepath.Dir(registryPath), "registry.initialized"), []byte("initialized"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "lost" {
				if err := os.WriteFile(registryPath, raw, 0600); err != nil {
					t.Fatal(err)
				}
				old := time.Unix(1700000000, 0)
				if err := os.Chtimes(registryPath, old, old); err != nil {
					t.Fatal(err)
				}
			}
			store := guidance.NewDefaultProjectStore(paths)
			if err := store.Save(uid, []byte("old")); err != nil {
				t.Fatal(err)
			}
			target := "sample"
			if kind == "missing-project" {
				target = "missing"
			}
			before := projectTree(t, root)
			read, readErr := service.ReadProject(ctx, target)
			validation, validateErr := service.ValidateProject(ctx, target, []byte("new"))
			if !reflect.DeepEqual(before, projectTree(t, root)) {
				t.Fatal("read/validate changed tree")
			}
			revision := validation.Revision
			if revision == "" {
				revision = "unused"
			}
			written, writeErr := service.WriteProject(ctx, target, []byte("new"), revision)
			revision = written.Revision
			if revision == "" {
				revision = "unused"
			}
			deleted, deleteErr := service.DeleteProject(ctx, target, revision)
			if kind == "inactive" || kind == "graph-invalid" {
				if readErr != nil || validateErr != nil || writeErr != nil || deleteErr != nil || read.Text != "old" || written.Text != "new" || deleted.Present {
					t.Fatalf("CRUD errors %v %v %v %v", readErr, validateErr, writeErr, deleteErr)
				}
			} else {
				for _, err := range []error{readErr, validateErr, writeErr, deleteErr} {
					var failure *resources.Failure
					if !errors.As(err, &failure) {
						t.Fatalf("unclassified target refusal %v", err)
					}
					want := "registry-unavailable"
					if kind == "missing-project" {
						want = "resource-not-found"
					}
					if failure.Code != want {
						t.Fatalf("target refusal %s want %s", failure.Code, want)
					}
				}
				if after := projectTree(t, root); !reflect.DeepEqual(before, after) {
					t.Fatal("refusal changed tree")
				}
			}
			afterRaw, err := os.ReadFile(registryPath)
			if kind == "lost" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatal("lost registry recreated")
				}
			} else {
				info, statErr := os.Stat(registryPath)
				if err != nil || statErr != nil || !bytes.Equal(raw, afterRaw) || !info.ModTime().Equal(time.Unix(1700000000, 0)) {
					t.Fatal("Registry bytes/mtime changed", err, statErr)
				}
			}
		})
	}
}
func projectTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			out[path] = "dir"
			return nil
		}
		b, err := os.ReadFile(path)
		out[path] = string(b)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestProjectGuidanceInvalidTargetsHaveNoEffects(t *testing.T) {
	for _, ref := range []string{"", " ", "uid:", "uid:../escape", "missing"} {
		t.Run(ref, func(t *testing.T) {
			root := t.TempDir()
			paths := config.Paths{ConfigDir: filepath.Join(root, "config"), StateDir: filepath.Join(root, "state")}
			s := Service{Paths: func() (config.Paths, error) { return paths, nil }, RegistryPaths: func() (config.Paths, error) { return paths, nil }}
			before := projectTree(t, root)
			ctx := context.Background()
			if _, err := s.ReadProject(ctx, ref); err == nil {
				t.Fatal("read accepted")
			}
			if _, err := s.ValidateProject(ctx, ref, []byte("text")); err == nil {
				t.Fatal("validate accepted")
			}
			if _, err := s.WriteProject(ctx, ref, []byte("text"), "revision"); err == nil {
				t.Fatal("write accepted")
			}
			if _, err := s.DeleteProject(ctx, ref, "revision"); err == nil {
				t.Fatal("delete accepted")
			}
			if !reflect.DeepEqual(before, projectTree(t, root)) {
				t.Fatal("target failure wrote files")
			}
		})
	}
}
