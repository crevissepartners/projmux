package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
)

// The managed base and the persona digest it records must come from the same
// bytes of one named instructions file. A file replaced after settings
// resolution is refused before application, never mixed into the recipe.
func TestManagedCodexBaseRefusesInstructionsReplacedAfterResolution(t *testing.T) {
	for _, tc := range []struct {
		name      string
		confirmed bool
	}{{"first application", false}, {"confirmed previous recipe", true}} {
		t.Run(tc.name, func(t *testing.T) {
			paths := config.Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir()}
			dir := filepath.Join(paths.ConfigDir, persona.DirName)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "role"+persona.FileExt)
			if err := os.WriteFile(file, []byte("ROLE-A-CONTENT"), 0o600); err != nil {
				t.Fatal(err)
			}
			annotations := map[string]string{}
			if tc.confirmed {
				old, err := PrepareManagedCodexBase(paths, "agent-test", "WHOLE-OLD-RECIPE", nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				annotations = map[string]string{CodexManagedBaseDigest: old.Digest, CodexManagedBaseMode: "base"}
			}
			resolve := func() (config.Paths, error) { return paths, nil }
			name := "role"
			settings, err := ResolveAgentSettingsWithPaths(resolve, "codex", annotations, AgentSettingsRequest{ManagedCodexBase: true, Instructions: &name, Source: "resume"})
			if err != nil {
				t.Fatal(err)
			}
			if settings.Resolution.InstructionsDigest != persona.Digest([]byte("ROLE-A-CONTENT")) {
				t.Fatalf("resolution did not read the first content: %+v", settings.Resolution)
			}
			service := GuidanceService{Paths: resolve}
			if err = os.WriteFile(file, []byte("ROLE-B-CONTENT"), 0o600); err != nil {
				t.Fatal(err)
			}
			base, err := service.PlanManagedCodexBase(metadata.Project{}, "agent-test", annotations, settings, true)
			if !tc.confirmed {
				if err == nil || !strings.Contains(err.Error(), "codex-managed-base-instructions-changed") {
					t.Fatalf("replaced instructions were not refused before application: %+v %v", base, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !base.Fallback || base.Instructions.Base != "WHOLE-OLD-RECIPE" || strings.Contains(base.Instructions.Base, "ROLE-B-CONTENT") {
				t.Fatalf("replaced instructions were mixed into the recipe: %+v", base)
			}
		})
	}
}

// Unchanged instructions still reach the base with the digest the recipe
// records.
func TestManagedCodexBaseCarriesResolvedInstructionsBytes(t *testing.T) {
	paths := config.Paths{ConfigDir: t.TempDir(), StateDir: t.TempDir()}
	dir := filepath.Join(paths.ConfigDir, persona.DirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "role"+persona.FileExt), []byte("ROLE-A-CONTENT"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolve := func() (config.Paths, error) { return paths, nil }
	name := "role"
	settings, err := ResolveAgentSettingsWithPaths(resolve, "codex", nil, AgentSettingsRequest{ManagedCodexBase: true, Instructions: &name, Source: "resume"})
	if err != nil {
		t.Fatal(err)
	}
	service := GuidanceService{Paths: resolve}
	base, err := service.PlanManagedCodexBase(metadata.Project{}, "agent-test", nil, settings, true)
	if err != nil && strings.Contains(err.Error(), "codex-managed-base-instructions-changed") {
		t.Fatalf("unchanged instructions refused: %v", err)
	}
	if err == nil && !base.Fallback {
		if !strings.Contains(base.Instructions.Base, "ROLE-A-CONTENT") || base.Annotations[metadata.AnnotationAgentPersonaDigest] != persona.Digest([]byte("ROLE-A-CONTENT")) {
			t.Fatalf("base and recorded digest disagree: %+v", base)
		}
	}
}
