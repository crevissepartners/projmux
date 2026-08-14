package app

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

func TestAppInitIsUnknownCommandForEveryLegacyIntent(t *testing.T) {
	t.Parallel()

	initCmd := newInitCommand()
	app := &App{setup: newSetupCommand(initCmd)}
	for _, args := range [][]string{
		{"init"},
		{"init", "--help"},
		{"init", "ghostty", "--dry-run"},
		{"init", "ghostty", "--apply"},
	} {
		var stdout, stderr bytes.Buffer
		err := app.Run(args, &stdout, &stderr)
		if err == nil || err.Error() != "unknown command: init" {
			t.Errorf("Run(%q) error = %v, want unknown command: init", args, err)
		}
		if stdout.Len() != 0 {
			t.Errorf("Run(%q) stdout = %q, want empty", args, stdout.String())
		}
		if !strings.Contains(stderr.String(), "Commands:") || !strings.Contains(stderr.String(), "  setup     Probe terminal keys or remediate them with setup terminal") {
			t.Errorf("Run(%q) stderr does not contain top-level usage:\n%s", args, stderr.String())
		}
		if strings.Contains(stderr.String(), "Deprecated alias") || strings.Contains(stderr.String(), "  init ") {
			t.Errorf("Run(%q) stderr exposes removed init surface:\n%s", args, stderr.String())
		}
	}
}

func TestTopLevelHelpShowsCanonicalSetupAndOmitsInit(t *testing.T) {
	t.Parallel()

	var usage bytes.Buffer
	if err := cli.RenderRootHelp(&usage); err != nil {
		t.Fatalf("RenderRootHelp returned error: %v", err)
	}
	out := usage.String()
	if !strings.Contains(out, "setup     Probe terminal keys or remediate them with setup terminal") {
		t.Fatalf("top-level help missing canonical setup terminal guidance:\n%s", out)
	}
	if strings.Contains(out, "Deprecated alias") || strings.Contains(out, "  init ") {
		t.Fatalf("top-level help exposes removed init surface:\n%s", out)
	}
}

func TestSetupTerminalHelpAndFlagsOmitDryRun(t *testing.T) {
	t.Parallel()

	cmd := newInitCommand()
	app := &App{setup: newSetupCommand(cmd)}

	// `setup terminal --help` is answered by the shared manifest-driven help
	// boundary: exit 0, stdout only, and no handler or filesystem access.
	var routeHelp, routeErr bytes.Buffer
	if err := app.Run([]string{"setup", "terminal", "--help"}, &routeHelp, &routeErr); err != nil {
		t.Fatalf("App.Run(setup terminal --help) error = %v, want nil", err)
	}
	if !strings.HasPrefix(routeHelp.String(), "projmux setup terminal\n") || routeErr.Len() != 0 {
		t.Fatalf("route help = %q stderr = %q", routeHelp.String(), routeErr.String())
	}
	if strings.Contains(routeHelp.String(), "dry-run") {
		t.Fatalf("route help exposes removed --dry-run:\n%s", routeHelp.String())
	}

	// The leaf parser still owns its own flag documentation for direct
	// invocation, and it still omits the removed --dry-run flag.
	var help bytes.Buffer
	err := newSetupCommand(cmd).Run([]string{"terminal", "--help"}, &bytes.Buffer{}, &help)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("setupCommand.Run(terminal --help) error = %v, want flag.ErrHelp", err)
	}
	for _, want := range []string{"Usage of projmux setup terminal:", "-apply", "-config", "-allow-symlink"} {
		if !strings.Contains(help.String(), want) {
			t.Fatalf("canonical help missing %q:\n%s", want, help.String())
		}
	}
	if strings.Contains(help.String(), "dry-run") {
		t.Fatalf("canonical help exposes removed --dry-run:\n%s", help.String())
	}

	err = app.Run([]string{"setup", "terminal", "ghostty", "--dry-run"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("canonical --dry-run error = %v, want unknown flag", err)
	}
}

func TestAppSetupTerminalPreviewDoesNotWrite(t *testing.T) {
	t.Parallel()

	config := filepath.Join(t.TempDir(), "ghostty.conf")
	original := "# user config\n"
	if err := os.WriteFile(config, []byte(original), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	initCmd := newInitCommand()
	app := &App{setup: newSetupCommand(initCmd)}
	var stdout, stderr bytes.Buffer
	if err := app.Run([]string{"setup", "terminal", "ghostty", "--config", config}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v (stderr=%s)", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "projmux setup terminal ghostty (preview)") {
		t.Fatalf("preview output = %q, want canonical entrypoint", stdout.String())
	}
	got, err := os.ReadFile(config)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != original {
		t.Fatalf("preview mutated config:\n%s", got)
	}
}

func TestSetupTerminalArgumentOrdering(t *testing.T) {
	t.Parallel()

	config := filepath.Join(t.TempDir(), "ghostty.conf")
	if err := os.WriteFile(config, []byte("# user\n"), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	cmd := newInitCommand()

	var outputs []string
	for _, args := range [][]string{
		{"ghostty", "--config", config},
		{"--config", config, "ghostty"},
		{"--config=" + config, "ghostty"},
	} {
		var stdout bytes.Buffer
		if err := cmd.Run(args, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("args %v error = %v", args, err)
		}
		outputs = append(outputs, stdout.String())
	}
	if outputs[0] != outputs[1] || outputs[0] != outputs[2] {
		t.Fatalf("terminal argument ordering changed plan\nfirst:\n%s\nsecond:\n%s\nthird:\n%s", outputs[0], outputs[1], outputs[2])
	}
}
