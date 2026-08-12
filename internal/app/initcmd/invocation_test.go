package initcmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalRealAdapterApplyAndBackup(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		terminal string
		original string
		newCmd   func() *Command
	}{
		{
			name:     "ghostty",
			terminal: "ghostty",
			original: "# user config\nkeybind = ctrl+t=new_tab\n",
			newCmd: func() *Command {
				return New(NewGhosttyAdapter(testGhosttyBindings))
			},
		},
		{
			name:     "windows terminal",
			terminal: "windows-terminal",
			original: presetSidebarOnly(),
			newCmd: func() *Command {
				return New(newWTTestAdapter(t))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config")
			if err := os.WriteFile(configPath, []byte(tc.original), 0o644); err != nil {
				t.Fatalf("seed config: %v", err)
			}

			var stdout bytes.Buffer
			if err := tc.newCmd().Run([]string{tc.terminal, "--apply", "--config", configPath}, &stdout, &bytes.Buffer{}); err != nil {
				t.Fatalf("canonical apply error = %v", err)
			}
			if !strings.Contains(stdout.String(), "projmux setup terminal") {
				t.Fatalf("apply output = %q, want canonical entrypoint", stdout.String())
			}
			assertSingleBackupWithContent(t, configPath, tc.original)
		})
	}
}

func TestCanonicalGhosttyCandidateConflict(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "ghostty")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"config", "config.ghostty"} {
		if err := os.WriteFile(filepath.Join(cfgDir, name), []byte("# "+name+"\n"), 0o644); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}

	err := newGhosttyTestInitCommand(t, tmp).Run([]string{"ghostty", "--apply"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "multiple ghostty config files found") || !strings.Contains(err.Error(), "--config <path>") {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestCanonicalTerminalDetectionPreview(t *testing.T) {
	t.Parallel()

	config := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(config, []byte("# user\n"), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	adapter := &fakeAdapter{name: "alpha", detect: true, configPath: config}
	cmd := New(adapter)
	cmd.getenv = func(string) string { return "detected" }

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--config", config}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("canonical auto-detect error = %v", err)
	}
	if adapter.detectCalls != 1 {
		t.Fatalf("Detect calls = %d, want one", adapter.detectCalls)
	}
	if adapter.applied != nil {
		t.Fatalf("preview unexpectedly applied: %+v", adapter.applied)
	}
	if !strings.Contains(stdout.String(), "projmux setup terminal alpha (preview)") {
		t.Fatalf("preview output = %q", stdout.String())
	}
}

func TestCanonicalSymlinkRefusalAndAllow(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	source := filepath.Join(tmp, "tracked-ghostty.conf")
	if err := os.WriteFile(source, []byte("# tracked\n"), 0o644); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	link := filepath.Join(tmp, "config")
	if err := os.Symlink(source, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	cmd := New(NewGhosttyAdapter(testGhosttyBindings))

	err := cmd.Run([]string{"ghostty", "--apply", "--config", link}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "is a symlink") || !strings.Contains(err.Error(), "--allow-symlink") {
		t.Fatalf("symlink refusal error = %v", err)
	}
	if err := cmd.Run([]string{"ghostty", "--apply", "--config", link, "--allow-symlink"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("allow-symlink error = %v", err)
	}
	updated, err := os.ReadFile(source)
	if err != nil {
		t.Fatalf("read source: %v", err)
	}
	if !strings.Contains(string(updated), "keybind") {
		t.Fatalf("allowed symlink target was not updated:\n%s", updated)
	}
}

func assertSingleBackupWithContent(t *testing.T, configPath, want string) {
	t.Helper()
	backups, err := filepath.Glob(configPath + ".bak.*")
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("backups for %s = %v, want one", configPath, backups)
	}
	got, err := os.ReadFile(backups[0])
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(got) != want {
		t.Fatalf("backup = %q, want %q", got, want)
	}
}
