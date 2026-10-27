package projmux_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestSettingsDirectoriesInputs(t *testing.T) {
	for _, tt := range []struct {
		name, input, profiles, personas string
	}{
		{"absolute", "/config/projmux", "/config/projmux/profiles", "/config/projmux/personas"},
		{"empty", "", "profiles", "personas"},
		{"relative", "config/projmux", "config/projmux/profiles", "config/projmux/personas"},
		{"trailing separator", "/config/projmux/", "/config/projmux/profiles", "/config/projmux/personas"},
		{"lexical cleaning", "config/old/../projmux//", "config/projmux/profiles", "config/projmux/personas"},
		{"literal environment", "$HOME/~/projmux", "$HOME/~/projmux/profiles", "$HOME/~/projmux/personas"},
		{"spaces preserved", " config ", " config /profiles", " config /personas"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			profiles, personas := projmux.SettingsDirectories(tt.input)
			if profiles != tt.profiles || personas != tt.personas {
				t.Fatalf("SettingsDirectories(%q) = (%q, %q), want (%q, %q)", tt.input, profiles, personas, tt.profiles, tt.personas)
			}
		})
	}
}

func TestSettingsDirectoriesNoSideEffects(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "missing", "projmux")
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "ignored"))
	before := centralTree(t, root)
	environment := os.Environ()
	var reads int
	restore := config.ObserveFrontReads(func(config.FrontRead) { reads++ })
	defer restore()
	config.NoteFrontRead(config.SettingConfigTheme, filepath.Join(root, "theme"))
	if reads != 1 {
		t.Fatal("front read observer positive control failed")
	}
	reads = 0
	for range 2 {
		profiles, personas := projmux.SettingsDirectories(configDir)
		if profiles != filepath.Join(configDir, "profiles") || personas != filepath.Join(configDir, "personas") {
			t.Fatalf("unexpected paths: %q, %q", profiles, personas)
		}
	}
	if reads != 0 || !reflect.DeepEqual(environment, os.Environ()) || !reflect.DeepEqual(before, centralTree(t, root)) {
		t.Fatal("directory calculation changed environment/filesystem or observed front reads")
	}
	// A file in place of a directory must not affect this lexical operation.
	if err := os.WriteFile(filepath.Dir(configDir), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, personas := projmux.SettingsDirectories(configDir)
	if profiles != filepath.Join(configDir, "profiles") || personas != filepath.Join(configDir, "personas") {
		t.Fatalf("filesystem contents affected paths: %q, %q", profiles, personas)
	}
}

const inputPathsProject = "proj-dddddddddddddddddddddddddq"

func TestSettingsInputPathsInputs(t *testing.T) {
	global := func(dir string) core.SettingsInputPaths {
		return core.SettingsInputPaths{
			GuidanceFile:                 filepath.Join(dir, "agent-guidance.md"),
			GuidancePolicyFile:           filepath.Join(dir, "agent-guidance.json"),
			GuidanceFragmentsDir:         filepath.Join(dir, "agent-guidance.d"),
			GuidanceMigrationReceiptFile: filepath.Join(dir, "agent-guidance-migration.json"),
			GuidanceMigrationBackupFile:  filepath.Join(dir, "agent-guidance.md.bak"),
		}
	}
	for _, input := range []string{"/config/projmux", "", "config/projmux", "/config/projmux/", "config/old/../projmux//", "$HOME/~/projmux", " config "} {
		got, err := projmux.SettingsInputPaths(input, "")
		if want := global(input); err != nil || got != want {
			t.Fatalf("SettingsInputPaths(%q, \"\") = %+v %v, want %+v", input, got, err, want)
		}
		got, err = projmux.SettingsInputPaths(input, inputPathsProject)
		want := global(input)
		want.ProjectGuidanceFile = filepath.Join(input, "project-guidance", inputPathsProject+".md")
		want.ProjectLinkRulesFile = filepath.Join(input, "project-links", inputPathsProject+".json")
		if err != nil || got != want {
			t.Fatalf("SettingsInputPaths(%q, Project) = %+v %v, want %+v", input, got, err, want)
		}
	}
	// The paths are the ones the guidance and link rule stores read.
	got, _ := projmux.SettingsInputPaths("/config/projmux", inputPathsProject)
	guidance := agentguidance.NewStore("/config/projmux", "/state")
	projectGuidance, _ := agentguidance.NewProjectStore("/config/projmux", "/state").Path(inputPathsProject)
	linkRules, _ := projectlinks.NewStore("/config/projmux").Path(inputPathsProject)
	if got.GuidanceFile != guidance.Path() || got.GuidancePolicyFile != guidance.SettingsPath() || got.GuidanceMigrationBackupFile != guidance.Path()+".bak" ||
		got.ProjectGuidanceFile != projectGuidance || got.ProjectLinkRulesFile != linkRules {
		t.Fatalf("paths differ from the stores: %+v", got)
	}
	for _, uid := range []string{"uid:" + inputPathsProject, "project-01", "../" + inputPathsProject, inputPathsProject + "/x", " "} {
		got, err := projmux.SettingsInputPaths("/config/projmux", uid)
		var refusal *projmux.Refusal
		if !errors.As(err, &refusal) || refusal.Code != projmux.CodeSelectorInvalid || got != (core.SettingsInputPaths{}) {
			t.Fatalf("SettingsInputPaths(%q) = %+v %v", uid, got, err)
		}
	}
}

func TestSettingsInputPathsNoSideEffects(t *testing.T) {
	root := t.TempDir()
	configDir := filepath.Join(root, "missing", "projmux")
	t.Setenv("HOME", root)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "ignored"))
	before := centralTree(t, root)
	environment := os.Environ()
	var reads int
	restore := config.ObserveFrontReads(func(config.FrontRead) { reads++ })
	defer restore()
	want, err := projmux.SettingsInputPaths(configDir, inputPathsProject)
	if err != nil {
		t.Fatal(err)
	}
	if reads != 0 || !reflect.DeepEqual(environment, os.Environ()) || !reflect.DeepEqual(before, centralTree(t, root)) {
		t.Fatal("path calculation changed environment/filesystem or observed front reads")
	}
	// A file in place of a directory must not affect this lexical operation.
	if err := os.WriteFile(filepath.Dir(configDir), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := projmux.SettingsInputPaths(configDir, inputPathsProject)
	if err != nil || got != want {
		t.Fatalf("filesystem contents affected paths: %+v %v", got, err)
	}
}
