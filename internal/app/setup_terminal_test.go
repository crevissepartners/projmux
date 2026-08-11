package app

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppSetupTerminalAndLegacyInitPreviewParity(t *testing.T) {
	t.Parallel()

	config := filepath.Join(t.TempDir(), "ghostty.conf")
	original := "# user config\n"
	if err := os.WriteFile(config, []byte(original), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	initCmd := newInitCommand()
	app := &App{initCmd: initCmd, setup: newSetupCommand(initCmd)}
	canonicalArgs := []string{"setup", "terminal", "ghostty", "--config", config}
	legacyArgs := []string{"init", "ghostty", "--dry-run", "--config", config}

	var canonicalOut, canonicalErr bytes.Buffer
	if err := app.Run(canonicalArgs, &canonicalOut, &canonicalErr); err != nil {
		t.Fatalf("canonical Run() error = %v (stderr=%s)", err, canonicalErr.String())
	}
	var legacyOut, legacyErr bytes.Buffer
	if err := app.Run(legacyArgs, &legacyOut, &legacyErr); err != nil {
		t.Fatalf("legacy Run() error = %v (stderr=%s)", err, legacyErr.String())
	}

	wantWarning := "warning: projmux init is deprecated; use: projmux setup terminal ghostty --config " + config + "\n"
	if got := legacyErr.String(); got != wantWarning {
		t.Fatalf("legacy warning = %q, want %q", got, wantWarning)
	}
	if canonicalErr.Len() != 0 {
		t.Fatalf("canonical stderr = %q, want empty", canonicalErr.String())
	}
	canonicalPlan := strings.Replace(canonicalOut.String(), "projmux setup terminal ghostty (preview)", "<entrypoint>", 1)
	legacyPlan := strings.Replace(legacyOut.String(), "projmux init ghostty (dry-run)", "<entrypoint>", 1)
	if canonicalPlan != legacyPlan {
		t.Fatalf("plan mismatch\ncanonical:\n%s\nlegacy:\n%s", canonicalOut.String(), legacyOut.String())
	}
	got, err := os.ReadFile(config)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != original {
		t.Fatalf("preview mutated config:\n%s", got)
	}
}

func TestAppSetupTerminalAndLegacyInitErrorParity(t *testing.T) {
	t.Parallel()

	initCmd := newInitCommand()
	app := &App{initCmd: initCmd, setup: newSetupCommand(initCmd)}
	canonicalErr := app.Run([]string{"setup", "terminal", "unsupported"}, &bytes.Buffer{}, &bytes.Buffer{})
	var legacyStderr bytes.Buffer
	legacyErr := app.Run([]string{"init", "unsupported"}, &bytes.Buffer{}, &legacyStderr)
	if canonicalErr == nil || legacyErr == nil {
		t.Fatalf("errors = (%v, %v), want both non-nil", canonicalErr, legacyErr)
	}
	canonicalDetail := strings.TrimPrefix(canonicalErr.Error(), "projmux setup terminal: ")
	legacyDetail := strings.TrimPrefix(legacyErr.Error(), "projmux init: ")
	if canonicalDetail != legacyDetail {
		t.Fatalf("error detail mismatch: canonical=%q legacy=%q", canonicalDetail, legacyDetail)
	}
	if got, want := legacyStderr.String(), "warning: projmux init is deprecated; use: projmux setup terminal unsupported\n"; got != want {
		t.Fatalf("legacy warning = %q, want %q", got, want)
	}
}

func TestSetupTerminalHelpOmitsDryRunAndLegacyAcceptsIt(t *testing.T) {
	t.Parallel()

	cmd := newInitCommand()
	app := &App{initCmd: cmd, setup: newSetupCommand(cmd)}
	var canonicalHelp bytes.Buffer
	err := app.Run([]string{"setup", "terminal", "--help"}, &bytes.Buffer{}, &canonicalHelp)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("App.Run(setup terminal --help) error = %v, want flag.ErrHelp", err)
	}
	if strings.Contains(canonicalHelp.String(), "dry-run") {
		t.Fatalf("canonical help exposes --dry-run:\n%s", canonicalHelp.String())
	}
	if !strings.Contains(canonicalHelp.String(), "Usage of projmux setup terminal:") {
		t.Fatalf("canonical help has wrong usage:\n%s", canonicalHelp.String())
	}

	config := filepath.Join(t.TempDir(), "ghostty.conf")
	if err := os.WriteFile(config, []byte("# user\n"), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := cmd.Run([]string{"ghostty", "--dry-run", "--config", config}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("legacy --dry-run error = %v", err)
	}
	canonicalErr := cmd.RunCanonical([]string{"ghostty", "--dry-run", "--config", config}, &bytes.Buffer{}, &bytes.Buffer{})
	if canonicalErr == nil || !strings.Contains(canonicalErr.Error(), "flag provided but not defined") {
		t.Fatalf("canonical --dry-run error = %v, want unknown flag", canonicalErr)
	}
}

func TestLegacyInitParseFailurePrintsOneWarningLine(t *testing.T) {
	t.Parallel()

	initCmd := newInitCommand()
	app := &App{initCmd: initCmd, setup: newSetupCommand(initCmd)}
	var stderr bytes.Buffer
	err := app.Run([]string{"init", "ghostty", "--unknown"}, &bytes.Buffer{}, &stderr)
	if err == nil {
		t.Fatal("legacy parse failure error = nil")
	}
	warning := "warning: projmux init is deprecated; use: projmux setup terminal ghostty --unknown"
	lines := strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n")
	if len(lines) == 0 || lines[0] != warning {
		t.Fatalf("legacy parse stderr first line = %q, want %q\nfull stderr:\n%s", lines[0], warning, stderr.String())
	}
	if got := strings.Count(stderr.String(), "warning: projmux init is deprecated;"); got != 1 {
		t.Fatalf("legacy warning count = %d, want 1\nfull stderr:\n%s", got, stderr.String())
	}
}

func TestSetupTerminalAndLegacyArgumentOrderingParity(t *testing.T) {
	t.Parallel()

	config := filepath.Join(t.TempDir(), "ghostty.conf")
	if err := os.WriteFile(config, []byte("# user\n"), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	cmd := newInitCommand()

	canonicalOutputs := make([]string, 0, 2)
	for _, args := range [][]string{
		{"ghostty", "--config", config},
		{"--config", config, "ghostty"},
		{"--config=" + config, "ghostty"},
	} {
		var stdout bytes.Buffer
		if err := cmd.RunCanonical(args, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("canonical args %v error = %v", args, err)
		}
		canonicalOutputs = append(canonicalOutputs, stdout.String())
	}
	if canonicalOutputs[0] != canonicalOutputs[1] {
		t.Fatalf("canonical terminal ordering changed plan\nfirst:\n%s\nsecond:\n%s", canonicalOutputs[0], canonicalOutputs[1])
	}
	if canonicalOutputs[0] != canonicalOutputs[2] {
		t.Fatalf("canonical --config=value ordering changed plan\nfirst:\n%s\nthird:\n%s", canonicalOutputs[0], canonicalOutputs[2])
	}

	legacyOutputs := make([]string, 0, 2)
	for _, args := range [][]string{
		{"ghostty", "--config", config},
		{"--config", config, "ghostty"},
		{"--config=" + config, "ghostty"},
	} {
		var stdout bytes.Buffer
		if err := cmd.Run(args, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("legacy args %v error = %v", args, err)
		}
		legacyOutputs = append(legacyOutputs, stdout.String())
	}
	if legacyOutputs[0] != legacyOutputs[1] {
		t.Fatalf("legacy terminal ordering changed plan\nfirst:\n%s\nsecond:\n%s", legacyOutputs[0], legacyOutputs[1])
	}
	if legacyOutputs[0] != legacyOutputs[2] {
		t.Fatalf("legacy --config=value ordering changed plan\nfirst:\n%s\nthird:\n%s", legacyOutputs[0], legacyOutputs[2])
	}

	app := &App{initCmd: cmd, setup: newSetupCommand(cmd)}
	var legacyStderr bytes.Buffer
	if err := app.Run([]string{"init", "--config", config, "ghostty", "--dry-run"}, &bytes.Buffer{}, &legacyStderr); err != nil {
		t.Fatalf("legacy config-first dispatch error = %v", err)
	}
	wantWarning := "warning: projmux init is deprecated; use: projmux setup terminal --config " + config + " ghostty\n"
	if got := legacyStderr.String(); got != wantWarning {
		t.Fatalf("legacy config-first warning = %q, want %q", got, wantWarning)
	}
}

func TestLegacyInitReplacementQuotesAndDropsDryRun(t *testing.T) {
	t.Parallel()

	got := legacyInitReplacement([]string{"ghostty", "--dry-run", "--config", "/tmp/config with space", "--allow-symlink"})
	want := "projmux setup terminal ghostty --config '/tmp/config with space' --allow-symlink"
	if got != want {
		t.Fatalf("legacyInitReplacement() = %q, want %q", got, want)
	}
}

func TestTopLevelHelpShowsCanonicalSetupAndDeprecatedAlias(t *testing.T) {
	t.Parallel()

	var usage bytes.Buffer
	printUsage(&usage)
	out := usage.String()
	for _, want := range []string{
		"setup     Probe terminal keys or remediate them with setup terminal",
		"init      Deprecated alias for setup terminal (compatibility period)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("top-level help missing %q:\n%s", want, out)
		}
	}
}
