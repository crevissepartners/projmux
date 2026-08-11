package initcmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCanonicalAndLegacyRealAdapterApplyAndBackupParity(t *testing.T) {
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
			canonicalPath := filepath.Join(t.TempDir(), "config")
			legacyPath := filepath.Join(t.TempDir(), "config")
			for _, path := range []string{canonicalPath, legacyPath} {
				if err := os.WriteFile(path, []byte(tc.original), 0o644); err != nil {
					t.Fatalf("seed %s: %v", path, err)
				}
			}

			var canonicalOut, legacyOut bytes.Buffer
			if err := tc.newCmd().RunCanonical([]string{tc.terminal, "--apply", "--config", canonicalPath}, &canonicalOut, &bytes.Buffer{}); err != nil {
				t.Fatalf("canonical apply error = %v", err)
			}
			if err := tc.newCmd().Run([]string{tc.terminal, "--apply", "--config", legacyPath}, &legacyOut, &bytes.Buffer{}); err != nil {
				t.Fatalf("legacy apply error = %v", err)
			}

			canonicalConfig, err := os.ReadFile(canonicalPath)
			if err != nil {
				t.Fatalf("read canonical config: %v", err)
			}
			legacyConfig, err := os.ReadFile(legacyPath)
			if err != nil {
				t.Fatalf("read legacy config: %v", err)
			}
			if !bytes.Equal(canonicalConfig, legacyConfig) {
				t.Fatalf("file result mismatch\ncanonical:\n%s\nlegacy:\n%s", canonicalConfig, legacyConfig)
			}
			assertSingleBackupWithContent(t, canonicalPath, tc.original)
			assertSingleBackupWithContent(t, legacyPath, tc.original)

			canonicalSummary := normalizeApplyOutput(canonicalOut.String(), "projmux setup terminal", canonicalPath)
			legacySummary := normalizeApplyOutput(legacyOut.String(), "projmux init", legacyPath)
			if canonicalSummary != legacySummary {
				t.Fatalf("apply summary mismatch\ncanonical:\n%s\nlegacy:\n%s", canonicalOut.String(), legacyOut.String())
			}
		})
	}
}

func TestCanonicalAndLegacyGhosttyCandidateConflictParity(t *testing.T) {
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

	canonicalErr := newGhosttyTestInitCommand(t, tmp).RunCanonical([]string{"ghostty", "--apply"}, &bytes.Buffer{}, &bytes.Buffer{})
	legacyErr := newGhosttyTestInitCommand(t, tmp).Run([]string{"ghostty", "--apply"}, &bytes.Buffer{}, &bytes.Buffer{})
	if canonicalErr == nil || legacyErr == nil {
		t.Fatalf("errors = (%v, %v), want both conflict errors", canonicalErr, legacyErr)
	}
	canonicalDetail := strings.TrimPrefix(canonicalErr.Error(), "projmux setup terminal: ")
	legacyDetail := strings.TrimPrefix(legacyErr.Error(), "projmux init: ")
	if canonicalDetail != legacyDetail {
		t.Fatalf("conflict error mismatch: canonical=%q legacy=%q", canonicalDetail, legacyDetail)
	}
}

func TestCanonicalAndLegacyTerminalDetectionParity(t *testing.T) {
	t.Parallel()

	config := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(config, []byte("# user\n"), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	adapter := &fakeAdapter{name: "alpha", detect: true, configPath: config}
	cmd := New(adapter)
	cmd.getenv = func(string) string { return "detected" }

	var canonicalOut, legacyOut bytes.Buffer
	if err := cmd.RunCanonical([]string{"--config", config}, &canonicalOut, &bytes.Buffer{}); err != nil {
		t.Fatalf("canonical auto-detect error = %v", err)
	}
	if err := cmd.Run([]string{"--config", config}, &legacyOut, &bytes.Buffer{}); err != nil {
		t.Fatalf("legacy auto-detect error = %v", err)
	}
	canonicalPlan := strings.Replace(canonicalOut.String(), "projmux setup terminal alpha (preview)", "<entrypoint>", 1)
	legacyPlan := strings.Replace(legacyOut.String(), "projmux init alpha (dry-run)", "<entrypoint>", 1)
	if canonicalPlan != legacyPlan {
		t.Fatalf("auto-detected plan mismatch\ncanonical:\n%s\nlegacy:\n%s", canonicalOut.String(), legacyOut.String())
	}
	if adapter.detectCalls != 2 {
		t.Fatalf("Detect calls = %d, want one per entrypoint", adapter.detectCalls)
	}
}

func TestCanonicalAndLegacySymlinkRefusalAndAllowParity(t *testing.T) {
	t.Parallel()

	type fixture struct {
		command *Command
		source  string
		link    string
	}
	newFixture := func(t *testing.T) fixture {
		t.Helper()
		tmp := t.TempDir()
		source := filepath.Join(tmp, "tracked-ghostty.conf")
		if err := os.WriteFile(source, []byte("# tracked\n"), 0o644); err != nil {
			t.Fatalf("seed source: %v", err)
		}
		link := filepath.Join(tmp, "config")
		if err := os.Symlink(source, link); err != nil {
			t.Skipf("symlink unsupported: %v", err)
		}
		return fixture{command: New(NewGhosttyAdapter(testGhosttyBindings)), source: source, link: link}
	}

	canonical := newFixture(t)
	legacy := newFixture(t)
	canonicalErr := canonical.command.RunCanonical([]string{"ghostty", "--apply", "--config", canonical.link}, &bytes.Buffer{}, &bytes.Buffer{})
	legacyErr := legacy.command.Run([]string{"ghostty", "--apply", "--config", legacy.link}, &bytes.Buffer{}, &bytes.Buffer{})
	if canonicalErr == nil || legacyErr == nil {
		t.Fatalf("refusal errors = (%v, %v), want both non-nil", canonicalErr, legacyErr)
	}
	canonicalDetail := strings.TrimPrefix(canonicalErr.Error(), "projmux setup terminal: ")
	canonicalDetail = strings.ReplaceAll(canonicalDetail, canonical.link, "<config>")
	legacyDetail := strings.TrimPrefix(legacyErr.Error(), "projmux init: ")
	legacyDetail = strings.ReplaceAll(legacyDetail, legacy.link, "<config>")
	if canonicalDetail != legacyDetail {
		t.Fatalf("symlink refusal mismatch: canonical=%q legacy=%q", canonicalDetail, legacyDetail)
	}

	if err := canonical.command.RunCanonical([]string{"ghostty", "--apply", "--config", canonical.link, "--allow-symlink"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("canonical allow-symlink error = %v", err)
	}
	if err := legacy.command.Run([]string{"ghostty", "--apply", "--config", legacy.link, "--allow-symlink"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("legacy allow-symlink error = %v", err)
	}
	canonicalConfig, err := os.ReadFile(canonical.source)
	if err != nil {
		t.Fatalf("read canonical source: %v", err)
	}
	legacyConfig, err := os.ReadFile(legacy.source)
	if err != nil {
		t.Fatalf("read legacy source: %v", err)
	}
	if !bytes.Equal(canonicalConfig, legacyConfig) {
		t.Fatalf("allowed symlink file mismatch\ncanonical:\n%s\nlegacy:\n%s", canonicalConfig, legacyConfig)
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

func normalizeApplyOutput(output, command, configPath string) string {
	output = strings.Replace(output, command, "<entrypoint>", 1)
	return strings.ReplaceAll(output, configPath, "<config>")
}
