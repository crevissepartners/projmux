package layout

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/sessionstate"
)

func TestStoreListDiscoversValidPresetsAndWarnsMalformed(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	store := NewStore(project)
	mustWrite(t, filepath.Join(store.Dir(), "dev.toml"), `
schema_version = 1
description = "Daily dev"
mode = "fresh-each-time"
unknown = "ignored"

[[windows]]
index = 0
name = "main"
layout = "layout"
active_pane_index = 0
extra = "ignored"

[[windows.panes]]
index = 0
cwd = "${PROJMUX_CWD}"
command = "make watch"
`)
	mustWrite(t, filepath.Join(store.Dir(), "broken.toml"), `
schema_version = 1
[[windows.panes]]
index = 0
cwd = "${PROJMUX_CWD}"
`)

	entries, warnings, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "dev" {
		t.Fatalf("entries = %#v, want dev only", entries)
	}
	if entries[0].Mode != ModeFreshEachTime || entries[0].Windows != 1 || entries[0].Panes != 1 {
		t.Fatalf("entry = %#v, want summarized preset", entries[0])
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0].Path, "broken.toml") {
		t.Fatalf("warnings = %#v, want malformed warning", warnings)
	}
}

func TestLoadDefaultsModeAndSupportsStartupCommand(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	store := NewStore(project)
	mustWrite(t, filepath.Join(store.Dir(), "review.toml"), `
schema_version = 1

[[windows]]
index = 0
active_pane_index = 0

[[windows.panes]]
index = 0
cwd = "${PROJMUX_CWD}/service"
command = "nvim ."
`)

	preset, err := store.Load("review")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if preset.Mode != ModeInheritAutosave {
		t.Fatalf("Mode = %q, want default inherit", preset.Mode)
	}
	if got := preset.Windows[0].Panes[0].Recipe; got.Kind != sessionstate.RecipeKindStartup || got.Command != "nvim ." {
		t.Fatalf("Recipe = %#v, want startup command", got)
	}
}

func TestLoadArtifactUsesSameExactBytesForParseAndAuthorization(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	store := NewStore(project)
	body := []byte("schema_version = 1\n\n[[windows]]\nindex = 0\nactive_pane_index = 0\n\n[[windows.panes]]\nindex = 0\ncwd = \"${PROJMUX_CWD}\"\ncommand = \"printf exact-bytes\"\n")
	mustWrite(t, filepath.Join(store.Dir(), "exact.toml"), string(body))

	artifact, err := store.LoadArtifact("exact")
	if err != nil {
		t.Fatalf("LoadArtifact() error = %v", err)
	}
	if string(artifact.Contents) != string(body) {
		t.Fatalf("artifact contents changed:\ngot  %q\nwant %q", artifact.Contents, body)
	}
	commands := artifact.ExecutableCommands()
	if len(commands) != 1 || !strings.Contains(commands[0], "printf exact-bytes") {
		t.Fatalf("ExecutableCommands() = %#v", commands)
	}
}

func TestLoadArtifactRejectsFileAndDirectorySymlinks(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		linkComponent string
	}{
		{name: "file"},
		{name: "layouts-directory", linkComponent: "layouts"},
		{name: "projmux-directory", linkComponent: ".projmux"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			project := t.TempDir()
			store := NewStore(project)
			outside := t.TempDir()
			body := "schema_version = 1\n"
			if err := os.WriteFile(filepath.Join(outside, "unsafe.toml"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			switch tc.linkComponent {
			case ".projmux":
				outsideLayouts := filepath.Join(outside, "layouts")
				if err := os.MkdirAll(outsideLayouts, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(filepath.Join(outside, "unsafe.toml"), filepath.Join(outsideLayouts, "unsafe.toml")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(project, ".projmux")); err != nil {
					t.Fatal(err)
				}
			case "layouts":
				if err := os.MkdirAll(filepath.Dir(store.Dir()), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, store.Dir()); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.MkdirAll(store.Dir(), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "unsafe.toml"), filepath.Join(store.Dir(), "unsafe.toml")); err != nil {
					t.Fatal(err)
				}
			}

			_, err := store.LoadArtifact("unsafe")
			if !errors.Is(err, ErrUnsafeArtifact) {
				t.Fatalf("LoadArtifact() error = %v, want ErrUnsafeArtifact", err)
			}
		})
	}
}

func TestToSnapshotEscapesProjectOwnedDisplayControls(t *testing.T) {
	t.Parallel()

	preset := Preset{
		SchemaVersion: SchemaVersion,
		Windows: []Window{{
			Index:           0,
			Name:            "dev\x1b]0;owned\a",
			ActivePaneIndex: 0,
			Panes: []Pane{{
				Index:    0,
				CWD:      "${PROJMUX_CWD}",
				Agent:    "codex",
				ResumeID: "resume",
				Topic:    "topic\x1b[31m",
				Recipe: sessionstate.AgentRecipe(
					"codex",
					"resume",
					"topic\x1b[31m",
				),
			}},
		}},
	}
	snap, err := ToSnapshot(preset, "workspace", t.TempDir(), time.Now())
	if err != nil {
		t.Fatalf("ToSnapshot() error = %v", err)
	}
	if got := snap.Windows[0].Name; strings.Contains(got, "\x1b") || !strings.Contains(got, `\x1b`) {
		t.Fatalf("window name = %q, want visible escape", got)
	}
	if got := snap.Windows[0].Panes[0].Recipe.Topic; strings.Contains(got, "\x1b") || !strings.Contains(got, `\x1b`) {
		t.Fatalf("agent topic = %q, want visible escape", got)
	}
}

func TestLoadRejectsUnsupportedPlaceholder(t *testing.T) {
	t.Parallel()

	project := t.TempDir()
	store := NewStore(project)
	mustWrite(t, filepath.Join(store.Dir(), "bad.toml"), `
schema_version = 1

[[windows]]
index = 0
active_pane_index = 0

[[windows.panes]]
index = 0
cwd = "${HOME}"
recipe = "shell"
`)

	_, err := store.Load("bad")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrInvalidPreset) || !strings.Contains(err.Error(), "${HOME}") {
		t.Fatalf("error = %v, want unsupported placeholder validation", err)
	}
}

func TestToSnapshotInterpolatesAllowedPlaceholders(t *testing.T) {
	t.Parallel()

	preset, err := Parse(`
schema_version = 1

[[windows]]
index = 0
active_pane_index = 0

[[windows.panes]]
index = 0
cwd = "${PROJMUX_CWD}/pkg"
recipe = "shell"
`)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	snap, err := ToSnapshot(preset, "workspace", "/repo", time.Time{})
	if err != nil {
		t.Fatalf("ToSnapshot() error = %v", err)
	}
	if got := snap.Windows[0].Panes[0].CWD; got != "/repo/pkg" {
		t.Fatalf("CWD = %q, want interpolated project path", got)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimLeft(body, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
}
