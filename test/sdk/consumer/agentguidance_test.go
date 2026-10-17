package consumer_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestExternalAgentGuidanceConsumer(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	c, err := projmux.NewClient(projmux.Options{ConfigDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var file core.AgentGuidanceFile
	file, err = c.ReadAgentGuidance(ctx)
	if err != nil || file.Present || file.Text != "" || file.Bytes != 0 || file.MaxBytes != 65536 || file.DefaultText == "" || file.DefaultDigest == "" {
		t.Fatalf("read %+v %v", file, err)
	}
	var validation core.AgentGuidanceValidation
	validation, err = c.ValidateAgentGuidance(ctx, []byte("consumer text"))
	if err != nil || validation.Revision != file.Revision || validation.ProcessAssembledBytes <= validation.AssembledBytes {
		t.Fatalf("validate %+v %v", validation, err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read/validate created config", err)
	}
	file, err = c.WriteAgentGuidance(ctx, []byte("consumer text"), validation.Revision)
	if err != nil || !file.Present || file.Text != "consumer text" {
		t.Fatalf("write %+v %v", file, err)
	}
	_, err = c.WriteAgentGuidance(ctx, []byte("stale"), validation.Revision)
	var refusal *projmux.Refusal
	if !errors.As(err, &refusal) || refusal.Code != projmux.CodeAgentGuidanceStale {
		t.Fatal("untyped stale", err)
	}
	file, err = c.ResetAgentGuidance(ctx, file.Revision)
	if err != nil || file.Present {
		t.Fatalf("reset %+v %v", file, err)
	}
	if err := c.SetAgentGuidanceEnabled(ctx, "", true); err != nil {
		t.Fatal(err)
	}
	var inspection core.AgentGuidanceInspection
	inspection, err = c.InspectAgentGuidance(ctx, true)
	if err != nil || inspection.Disabled || len(inspection.Parts) == 0 || inspection.Text == "" {
		t.Fatalf("inspect %+v %v", inspection, err)
	}
}

func TestExternalAgentGuidanceRefusalCause(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(path, "nested")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ReadAgentGuidance(context.Background())
	var refused *projmux.Refusal
	var pathError *os.PathError
	if !errors.As(err, &pathError) || !errors.As(err, &refused) || refused.Code != projmux.CodeAgentGuidanceUnreadable || !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("refusal lost filesystem cause: %v", err)
	}
}

func TestExternalAgentGuidanceInspectionSource(t *testing.T) {
	for _, process := range []bool{false, true} {
		c, err := projmux.NewClient(projmux.Options{ConfigDir: filepath.Join(t.TempDir(), "config")})
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		for _, source := range []string{"default", "file", "off"} {
			if source == "file" {
				candidate, err := c.ValidateAgentGuidance(ctx, []byte("custom"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := c.WriteAgentGuidance(ctx, []byte("custom"), candidate.Revision); err != nil {
					t.Fatal(err)
				}
			} else if source == "off" {
				if err := c.SetAgentGuidanceEnabled(ctx, "", false); err != nil {
					t.Fatal(err)
				}
			}
			inspection, err := c.InspectAgentGuidance(ctx, process)
			if err != nil || inspection.Source != source {
				t.Fatalf("process=%t want %s: %+v %v", process, source, inspection, err)
			}
			encoded, err := json.Marshal(inspection)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			if _, exists := fields["source"]; exists {
				t.Fatalf("Source changed show JSON: %s", encoded)
			}
		}
	}
}
