package hooks

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMigrateProjectLegacyScriptsSingleLineConvertsAndBackups(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	scriptPath := filepath.Join(repo, ".projmux", "post-create")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho hello\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var logger bytes.Buffer
	result, err := MigrateProjectLegacyScripts(repo, "", &logger)
	if err != nil {
		t.Fatalf("MigrateProjectLegacyScripts() error = %v", err)
	}
	if len(result.Migrated) != 1 || result.Migrated[0].Event != EventPostCreate {
		t.Fatalf("Migrated = %#v, want post-create entry", result.Migrated)
	}
	if result.Migrated[0].Command != "echo hello" {
		t.Fatalf("Command = %q, want echo hello", result.Migrated[0].Command)
	}
	if len(result.Skipped) != 0 {
		t.Fatalf("Skipped = %#v, want none", result.Skipped)
	}

	configBody, err := os.ReadFile(filepath.Join(repo, ".projmux", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(configBody), "[hooks.post-create]") {
		t.Fatalf("config.toml missing migrated entry:\n%s", configBody)
	}
	if !strings.Contains(string(configBody), `run = "echo hello"`) {
		t.Fatalf("config.toml missing run line:\n%s", configBody)
	}

	if _, err := os.Stat(scriptPath); !os.IsNotExist(err) {
		t.Fatalf("original script still present, stat err = %v", err)
	}
	if _, err := os.Stat(scriptPath + ".bak"); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}
	if !strings.Contains(logger.String(), "migrated legacy project hook") {
		t.Fatalf("logger missing migration line:\n%s", logger.String())
	}
}

func TestMigrateProjectLegacyScriptsMultiLineSkippedAndLogged(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	scriptPath := filepath.Join(repo, ".projmux", "hooks", "post-create")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\necho one\necho two\n"
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	var logger bytes.Buffer
	result, err := MigrateProjectLegacyScripts(repo, "", &logger)
	if err != nil {
		t.Fatalf("MigrateProjectLegacyScripts() error = %v", err)
	}
	if len(result.Migrated) != 0 {
		t.Fatalf("Migrated = %#v, want none for multi-line", result.Migrated)
	}
	if len(result.Skipped) != 1 || result.Skipped[0].Event != EventPostCreate {
		t.Fatalf("Skipped = %#v, want post-create entry", result.Skipped)
	}
	if result.Skipped[0].Lines != 2 {
		t.Fatalf("Skipped[0].Lines = %d, want 2", result.Skipped[0].Lines)
	}
	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("multi-line script should remain in place: %v", err)
	}
	if _, err := os.Stat(scriptPath + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("multi-line script must not be backed up automatically: stat = %v", err)
	}
	if !strings.Contains(logger.String(), "declarative migration skipped") {
		t.Fatalf("logger missing skip warning:\n%s", logger.String())
	}
}

func TestMigrateProjectLegacyScriptsPreservesExistingDeclarativeEntry(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".projmux"), 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(repo, ".projmux", "config.toml")
	if err := os.WriteFile(configPath, []byte(`[hooks.post-create]
run = "echo declared-wins"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(repo, ".projmux", "post-create")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho legacy-loses\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := MigrateProjectLegacyScripts(repo, "", nil); err != nil {
		t.Fatalf("MigrateProjectLegacyScripts() error = %v", err)
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `run = "echo declared-wins"`) {
		t.Fatalf("declarative entry should win, config:\n%s", got)
	}
	if strings.Contains(string(got), "echo legacy-loses") {
		t.Fatalf("legacy script should not overwrite declarative entry, config:\n%s", got)
	}
	// Script still gets backed up so the duplicate row stops showing in the UI.
	if _, err := os.Stat(scriptPath + ".bak"); err != nil {
		t.Fatalf("script backup missing: %v", err)
	}
}

func TestMigrateGlobalLegacyScriptsConvertsSingleLine(t *testing.T) {
	t.Parallel()

	configHome := t.TempDir()
	scriptPath := filepath.Join(configHome, "projmux", "hooks", "post-create")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho global-hello\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	getenv := func(name string) string {
		if name == "XDG_CONFIG_HOME" {
			return configHome
		}
		return ""
	}

	result, err := MigrateGlobalLegacyScripts(getenv, nil, "", nil)
	if err != nil {
		t.Fatalf("MigrateGlobalLegacyScripts() error = %v", err)
	}
	if len(result.Migrated) != 1 {
		t.Fatalf("Migrated = %#v, want one entry", result.Migrated)
	}
	configPath := filepath.Join(configHome, "projmux", "config.toml")
	body, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read global config: %v", err)
	}
	if !strings.Contains(string(body), `run = "echo global-hello"`) {
		t.Fatalf("global config missing migrated run line:\n%s", body)
	}
	if _, err := os.Stat(scriptPath + ".bak"); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
}

func TestMigrateProjectLegacyScriptsSymlinkIsNeverMigrated(t *testing.T) {
	t.Parallel()

	// Cover both link targets that *would* qualify for migration (single
	// line) and ones that wouldn't (multi line) — neither should ever be
	// renamed or written to the declarative config, because dotfiles repos
	// commonly deploy these as symlinks and the migrator must not surprise
	// the source-of-truth on disk.
	cases := []struct {
		name string
		body string
	}{
		{name: "single-line target", body: "#!/bin/sh\necho hello\n"},
		{name: "multi-line target", body: "#!/bin/sh\necho one\necho two\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			repo := t.TempDir()
			external := t.TempDir()
			target := filepath.Join(external, "post-create-source.sh")
			if err := os.WriteFile(target, []byte(tc.body), 0o755); err != nil {
				t.Fatal(err)
			}
			scriptPath := filepath.Join(repo, ".projmux", "post-create")
			if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, scriptPath); err != nil {
				t.Fatalf("symlink: %v", err)
			}

			var logger bytes.Buffer
			result, err := MigrateProjectLegacyScripts(repo, "", &logger)
			if err != nil {
				t.Fatalf("MigrateProjectLegacyScripts() error = %v", err)
			}
			if len(result.Migrated) != 0 {
				t.Fatalf("Migrated = %#v, want none for symlink", result.Migrated)
			}
			if len(result.Skipped) != 1 {
				t.Fatalf("Skipped = %#v, want exactly one entry", result.Skipped)
			}
			skip := result.Skipped[0]
			if !skip.Symlink {
				t.Fatalf("Skipped[0].Symlink = false, want true (%#v)", skip)
			}
			if skip.Reason != SkipReasonSymlink {
				t.Fatalf("Skipped[0].Reason = %q, want %q", skip.Reason, SkipReasonSymlink)
			}
			if skip.Event != EventPostCreate {
				t.Fatalf("Skipped[0].Event = %q, want post-create", skip.Event)
			}

			// Link must still point at the original target, unrenamed.
			if got, err := os.Readlink(scriptPath); err != nil || got != target {
				t.Fatalf("readlink got=%q err=%v, want %q (link must not be renamed)", got, err, target)
			}
			if _, err := os.Lstat(scriptPath + ".bak"); !os.IsNotExist(err) {
				t.Fatalf("symlink must not be backed up: stat err = %v", err)
			}
			// Underlying target untouched.
			if body, err := os.ReadFile(target); err != nil || string(body) != tc.body {
				t.Fatalf("target file mutated: body=%q err=%v", body, err)
			}
			// Config must not have been created (no declarative entry).
			if _, err := os.Stat(filepath.Join(repo, ".projmux", "config.toml")); !os.IsNotExist(err) {
				t.Fatalf("config.toml unexpectedly created: stat err = %v", err)
			}
			if !strings.Contains(logger.String(), "symlink") {
				t.Fatalf("logger missing symlink notice:\n%s", logger.String())
			}
		})
	}
}

func TestMigrateGlobalLegacyScriptsSymlinkIsNeverMigrated(t *testing.T) {
	t.Parallel()

	configHome := t.TempDir()
	external := t.TempDir()
	target := filepath.Join(external, "post-create-source.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho dotfiles\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(configHome, "projmux", "hooks", "post-create")
	if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, scriptPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	getenv := func(name string) string {
		if name == "XDG_CONFIG_HOME" {
			return configHome
		}
		return ""
	}

	result, err := MigrateGlobalLegacyScripts(getenv, nil, "", nil)
	if err != nil {
		t.Fatalf("MigrateGlobalLegacyScripts() error = %v", err)
	}
	if len(result.Migrated) != 0 {
		t.Fatalf("Migrated = %#v, want none", result.Migrated)
	}
	if len(result.Skipped) != 1 || !result.Skipped[0].Symlink {
		t.Fatalf("Skipped = %#v, want one symlink entry", result.Skipped)
	}
	if _, err := os.Lstat(scriptPath); err != nil {
		t.Fatalf("symlink should remain in place: %v", err)
	}
	if _, err := os.Stat(filepath.Join(configHome, "projmux", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("global config.toml unexpectedly created: stat err = %v", err)
	}
}

func TestMigrateProjectLegacyScriptsEmptyDirIsNoOp(t *testing.T) {
	t.Parallel()

	repo := t.TempDir()
	result, err := MigrateProjectLegacyScripts(repo, "", nil)
	if err != nil {
		t.Fatalf("MigrateProjectLegacyScripts() error = %v", err)
	}
	if len(result.Migrated) != 0 || len(result.Skipped) != 0 {
		t.Fatalf("Result = %#v, want empty", result)
	}
}
