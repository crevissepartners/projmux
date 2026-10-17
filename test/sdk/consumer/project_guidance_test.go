package consumer_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestExternalProjectGuidanceConsumer(t *testing.T) {
	root := t.TempDir()
	configDir, stateDir := filepath.Join(root, "config"), filepath.Join(root, "state")
	if err := os.MkdirAll(filepath.Join(stateDir, "metadata"), 0700); err != nil {
		t.Fatal(err)
	}
	uid := "proj-aaaaaaaaaaaaaaaaaaaaaaaaaa"
	// An inactive registered Project; the public DTO also pins the consumer type.
	project := core.Project{APIVersion: "projmux.io/v1alpha1", Kind: core.KindProject, Metadata: core.ObjectMeta{UID: uid, Name: "sample"}, Spec: core.ProjectSpec{Root: root}}
	reg := struct {
		APIVersion    string         `json:"apiVersion"`
		SchemaVersion int            `json:"schemaVersion"`
		Projects      []core.Project `json:"projects"`
	}{"projmux.io/v1alpha1", 5, []core.Project{project}}
	b, err := json.Marshal(reg)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(stateDir, "metadata", "registry.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	client, err := projmux.NewClient(projmux.Options{ConfigDir: configDir, StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var file core.ProjectGuidanceFile
	file, err = client.ReadProjectGuidance(ctx, "sample")
	if err != nil || file.Present || file.ProjectUID != uid || file.Source != "off" || file.MaxBytes != 65536 {
		t.Fatalf("read %+v %v", file, err)
	}
	var validation core.ProjectGuidanceValidation
	validation, err = client.ValidateProjectGuidance(ctx, "uid:"+uid, []byte(" \t\n"))
	if err != nil || validation.Revision != file.Revision {
		t.Fatalf("validate %+v %v", validation, err)
	}
	if _, err = os.Stat(configDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read/validate created config", err)
	}
	file, err = client.WriteProjectGuidance(ctx, "sample", []byte(validation.Text), validation.Revision)
	if err != nil || !file.Present || file.Text != validation.Text || file.Source != "off" || file.EffectiveText != "" {
		t.Fatalf("raw/effective %+v %v", file, err)
	}
	_, err = client.WriteProjectGuidance(ctx, "sample", []byte("stale"), validation.Revision)
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeAgentGuidanceStale {
		t.Fatal("typed stale", err)
	}
	file, err = client.DeleteProjectGuidance(ctx, "sample", file.Revision)
	if err != nil || file.Present {
		t.Fatalf("delete %+v %v", file, err)
	}
	for _, ref := range []string{"", "uid:", "missing"} {
		_, err = client.ReadProjectGuidance(ctx, ref)
		if !errors.As(err, &refusal) {
			t.Fatalf("typed selector refusal %q %v", ref, err)
		}
	}
}
