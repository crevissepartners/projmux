package app

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/app/keybinding"
	"github.com/crevissepartners/projmux/internal/app/updatecmd"
)

// keymapBackupFiles lists the pre-v1 backups sitting next to a keymap.
func keymapBackupFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read keymap directory: %v", err)
	}
	var out []string
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".pre-v1-") && strings.HasSuffix(entry.Name(), ".bak") {
			out = append(out, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(out)
	return out
}

// testKeymapCanonicalIDs lists every canonical id, sorted.
func testKeymapCanonicalIDs() []string {
	seen := map[string]bool{}
	var ids []string
	for _, action := range keybinding.DefaultKeyBindingCatalog() {
		if action.CanonicalID == "" || seen[action.CanonicalID] {
			continue
		}
		seen[action.CanonicalID] = true
		ids = append(ids, action.CanonicalID)
	}
	sort.Strings(ids)
	return ids
}

// newKeymapFixture writes a keymap under an isolated HOME and returns the store
// plus the keymap path.
func newKeymapFixture(t *testing.T, body string) (keybinding.KeymapStore, string) {
	t.Helper()
	home := t.TempDir()
	path := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return keybinding.KeymapStore{
		HomeDir:   func() (string, error) { return home, nil },
		LookupEnv: func(string) string { return "" },
	}, path
}

func TestConfigRenderReportsMigrationOnStderrAndWritesNoKeymap(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymap := filepath.Join(home, ".config", "projmux", "keymap.toml")
	original := "[bindings.new-window]\nkeys = [\"C-t\"]\n"
	writeFile(t, keymap, original)

	for _, artifact := range []string{"print-config", "print-app-config"} {
		cmd := &tmuxCommand{
			executable: func() (string, error) { return "/tmp/projmux", nil },
			homeDir:    func() (string, error) { return home, nil },
			lookupEnv:  func(string) string { return "" },
			readFile:   os.ReadFile,
			writeFile:  os.WriteFile,
		}
		var stdout, stderr bytes.Buffer
		if err := cmd.Run([]string{artifact}, &stdout, &stderr); err != nil {
			t.Fatalf("%s error = %v", artifact, err)
		}
		if !strings.Contains(stderr.String(), "keymap migration pending") {
			t.Fatalf("%s stderr = %q, want the preflight report", artifact, stderr.String())
		}
		if strings.Contains(stdout.String(), "keymap migration") {
			t.Fatalf("%s leaked the preflight onto the generated artifact:\n%s", artifact, stdout.String())
		}
		if got := readFile(t, keymap); got != original {
			t.Fatalf("%s rewrote the keymap", artifact)
		}
		if backups := keymapBackupFiles(t, filepath.Dir(keymap)); len(backups) != 0 {
			t.Fatalf("%s created backups %v", artifact, backups)
		}
	}
}

func TestSettingsKeySaveConvergesAV0KeymapToV2(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymap := filepath.Join(home, ".config", "projmux", "keymap.toml")
	writeFile(t, keymap, "[bindings.new-window]\nkeys = [\"C-t\"]\n")
	cmd := &settingsCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
	}

	var stdout bytes.Buffer
	if err := cmd.saveKeymapKeysAndApply("ProjectSidebarToggle", []string{"M-a"}, &stdout); err != nil {
		t.Fatalf("saveKeymapKeysAndApply() error = %v", err)
	}

	got := readFile(t, keymap)
	for _, want := range []string{
		"schema_version = 2\n",
		`[bindings."window.create"]`,
		`[bindings."project-sidebar.toggle"]`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("keymap = %q, want %q", got, want)
		}
	}
	// The save must not have grown a second table for the action it wrote.
	if strings.Contains(got, "[bindings.ProjectSidebarToggle]") {
		t.Fatalf("save wrote a v0 table into a migrated file:\n%s", got)
	}
	if !strings.Contains(stdout.String(), "Schema: ok") {
		t.Fatalf("stdout = %q, want a Schema stage", stdout.String())
	}
	if backups := keymapBackupFiles(t, filepath.Dir(keymap)); len(backups) != 1 {
		t.Fatalf("backups = %v, want exactly one", backups)
	}
}

func TestSettingsKeySaveAbortsWhenMigrationConflicts(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymap := filepath.Join(home, ".config", "projmux", "keymap.toml")
	original := `[bindings.new-window]
keys = ["C-t"]

[bindings."window.create"]
keys = ["C-n"]
`
	writeFile(t, keymap, original)
	cmd := &settingsCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
	}

	var stdout bytes.Buffer
	err := cmd.saveKeymapKeysAndApply("ProjectSidebarToggle", []string{"M-a"}, &stdout)
	if err == nil || !strings.Contains(err.Error(), "migrate keymap schema") {
		t.Fatalf("error = %v, want a schema-stage abort", err)
	}
	if got := readFile(t, keymap); got != original {
		t.Fatalf("a blocked save changed the keymap:\n%s", got)
	}
	for _, want := range []string{
		"  Schema: failed (keymap schema:",
		"  Saved: skipped (keymap schema was not migrated)",
		"  Prepared: skipped (keymap schema was not migrated)",
		"  Running session: skipped (keymap schema was not migrated)",
		"Recovery: resolve the keymap schema problem",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func TestTmuxApplyRefusesToApplyWhenTheKeymapMigrationFails(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymap := filepath.Join(home, ".config", "projmux", "keymap.toml")
	original := `[bindings.new-window]
keys = ["C-t"]

[bindings."window.create"]
keys = ["C-n"]
`
	writeFile(t, keymap, original)
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	runner := &recordingTmuxRunner{outputs: map[string]string{}}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
		writeFile:  os.WriteFile,
		runner:     runner,
	}

	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"apply", "--config", configPath, "--socket", "projmux-test"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "migrate keymap before apply") {
		t.Fatalf("error = %v, want an apply refusal", err)
	}
	if got := readFile(t, keymap); got != original {
		t.Fatal("a refused apply changed the keymap")
	}
	if _, statErr := os.Stat(configPath); !os.IsNotExist(statErr) {
		t.Fatal("a refused apply wrote the generated tmux config")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("a refused apply touched tmux: %v", runner.calls)
	}
	for _, want := range []string{"keymap unchanged", "generated tmux config unchanged", "skipped reload"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func TestTmuxApplyNoReloadMigratesWithoutTouchingTheServer(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymap := filepath.Join(home, ".config", "projmux", "keymap.toml")
	writeFile(t, keymap, "[bindings.new-window]\nkeys = [\"C-t\"]\n")
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	runner := &recordingTmuxRunner{outputs: map[string]string{}}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
		writeFile:  os.WriteFile,
		runner:     runner,
	}

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"apply", "--config", configPath, "--no-reload"}, &stdout, &stderr); err != nil {
		t.Fatalf("apply --no-reload error = %v; stderr = %q", err, stderr.String())
	}
	if !strings.Contains(readFile(t, keymap), "schema_version = 2") {
		t.Fatal("--no-reload skipped the keymap migration")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("--no-reload contacted the tmux server: %v", runner.calls)
	}
	if !strings.Contains(stdout.String(), "skipped reload: --no-reload") {
		t.Fatalf("stdout = %q, want the suppressed-reload line", stdout.String())
	}
}

// TestReadOnlyRoutesNeverRewriteTheKeymap is the negative half of the write
// boundary.
//
// Migration is supposed to happen at exactly two kinds of moment: an explicit
// apply, and a Settings save. Everything else — printing help, reporting a
// version, reading a resource, rendering a config, previewing an update — is a
// read, and a read that silently rewrote the user's keymap would make the
// schema's careful backup-then-replace ordering pointless, because it would run
// at times the user never asked for it.
//
// It drives the real top-level dispatcher against an isolated HOME/XDG rather
// than a hand-built command struct, because the property under test is about
// routes rather than about any one handler.
func TestReadOnlyRoutesNeverRewriteTheKeymap(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, ".config")
	keymap := filepath.Join(configHome, "projmux", "keymap.toml")
	original := "[bindings.new-window]\nkeys = [\"C-t\"]\n"
	writeFile(t, keymap, original)

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	// Pin the installer so the update preview is deterministic and never probes
	// the real install layout.
	t.Setenv("PROJMUX_INSTALLER", "npm")
	// `agent usage` is the one route here that execs a provider: the Codex
	// usage adapter probes `codex app-server proxy` and `daemon version`, then
	// runs `codex app-server daemon start`. With the real codex on PATH that
	// installed a release under this HOME and left its managed daemon running
	// after the test, so the route gets recording stand-ins instead.
	record := fakeProviderBinaries(t)

	for _, args := range [][]string{
		{"help"},
		{"version"},
		{"get"},
		{"describe"},
		{"agent", "usage"},
		{"doctor", "--help"},
		{"config", "render", "standalone"},
		{"config", "render", "app"},
		{"update", "apply", "--dry-run"},
	} {
		var stdout, stderr bytes.Buffer
		before := len(providerInvocations(t, record))
		// The exit status is not the assertion. A usage error is a perfectly
		// good read; what matters is that nothing on disk moved.
		_ = Run(args, &stdout, &stderr)
		if calls := providerInvocations(t, record); len(calls) > before {
			t.Logf("%v invoked a provider: %q", args, calls[before:])
		}

		if got := readFile(t, keymap); got != original {
			t.Fatalf("%v rewrote the keymap:\ngot:  %q\nwant: %q", args, got, original)
		}
		if backups := keymapBackupFiles(t, filepath.Dir(keymap)); len(backups) != 0 {
			t.Fatalf("%v created keymap backups %v", args, backups)
		}
	}
	assertNoProcessRunsFrom(t, home)
}

// TestUpdateDryRunPreviewsTheMigrationStageWithoutPromisingADiff pins the
// updater preview contract.
func TestUpdateDryRunPreviewsTheMigrationStageWithoutPromisingADiff(t *testing.T) {
	t.Parallel()

	line := updatecmd.KeymapMigrationStagePreviewLine("/usr/local/bin/projmux")
	if !strings.Contains(line, "would migrate: keymap schema via /usr/local/bin/projmux") {
		t.Fatalf("preview = %q, want the migration stage named", line)
	}
	if !strings.Contains(line, "the installed binary computes the exact action-id table") {
		t.Fatalf("preview = %q, want an explicit disclaimer about the rename table", line)
	}
	for _, canonical := range testKeymapCanonicalIDs() {
		if strings.Contains(line, canonical) {
			t.Fatalf("preview leaks a canonical id %q; a dry run cannot know the candidate binary's table", canonical)
		}
	}
}
