package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
)

func TestStatusGitPrintsBranchForPath(t *testing.T) {
	t.Parallel()

	cmd := testStatusCommand(t.TempDir())
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "rev-parse", "--is-inside-work-tree"}) {
			return []byte("true\n"), nil
		}
		if name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "symbolic-ref", "--quiet", "--short", "HEAD"}) {
			return []byte("main\n"), nil
		}
		return nil, os.ErrNotExist
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"git", "/repo"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), " #[bold,fg=colour231,bg=colour30] main #[default]"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestStatusGitUsesCurrentPanePathInsideTmux(t *testing.T) {
	t.Parallel()

	cmd := testStatusCommand(t.TempDir())
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/tmux"
		}
		return ""
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "#{pane_current_path}"}):
			return []byte("/repo\n"), nil
		case name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "rev-parse", "--is-inside-work-tree"}):
			return []byte("true\n"), nil
		case name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "symbolic-ref", "--quiet", "--short", "HEAD"}):
			return nil, errors.New("detached")
		case name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "rev-parse", "--short", "HEAD"}):
			return []byte("abc1234\n"), nil
		default:
			return nil, os.ErrNotExist
		}
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"git"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), " #[bold,fg=colour231,bg=colour30] abc1234 #[default]"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestStatusGitPrintsConfiguredSymbolDecorator(t *testing.T) {
	t.Parallel()

	cmd := testStatusCommand(t.TempDir())
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/tmux"
		}
		return ""
	}
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "#{pane_current_path}"}):
			return []byte("/repo\n"), nil
		case name == "tmux" && reflect.DeepEqual(args, []string{"show-options", "-gqv", statusbarDecorationTmuxOption}):
			return []byte("symbol\n"), nil
		case name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "rev-parse", "--is-inside-work-tree"}):
			return []byte("true\n"), nil
		case name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "symbolic-ref", "--quiet", "--short", "HEAD"}):
			return []byte("main\n"), nil
		case name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "config", "--get", "remote.origin.url"}):
			return []byte("git@gitlab.com:org/repo.git\n"), nil
		default:
			return nil, os.ErrNotExist
		}
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"git"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := " #[bold,fg=colour231,bg=colour30] #[fg=colour215] #[fg=colour231]main #[default]"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestStatusGitPrintsConfiguredEmojiDecoratorFromConfig(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	paths, err := config.Homes{HomeDir: home}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveStatusbarDecorationFile(paths.StatusbarDecorationFile(), config.StatusbarDecorationEmoji); err != nil {
		t.Fatal(err)
	}
	cmd := testStatusCommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "rev-parse", "--is-inside-work-tree"}) {
			return []byte("true\n"), nil
		}
		if name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "symbolic-ref", "--quiet", "--short", "HEAD"}) {
			return []byte("main\n"), nil
		}
		if name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "config", "--get", "remote.origin.url"}) {
			return []byte("git@github.com:org/repo.git\n"), nil
		}
		return nil, os.ErrNotExist
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"git", "/repo"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := " #[bold,fg=colour231,bg=colour30] #[fg=colour153]🐱 #[fg=colour231]main #[default]"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestStatusGitPrintsConfiguredEmojiDecoratorForGitLabRemote(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	paths, err := config.Homes{HomeDir: home}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveStatusbarDecorationFile(paths.StatusbarDecorationFile(), config.StatusbarDecorationEmoji); err != nil {
		t.Fatal(err)
	}
	cmd := testStatusCommand(home)
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "rev-parse", "--is-inside-work-tree"}):
			return []byte("true\n"), nil
		case name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "symbolic-ref", "--quiet", "--short", "HEAD"}):
			return []byte("main\n"), nil
		case name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "config", "--get", "remote.origin.url"}):
			return []byte("https://gitlab.com/org/repo.git\n"), nil
		default:
			return nil, os.ErrNotExist
		}
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"git", "/repo"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := " #[bold,fg=colour231,bg=colour30] #[fg=colour215]🦊 #[fg=colour231]main #[default]"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestStatusGitPrintsStateIndicators(t *testing.T) {
	t.Parallel()

	cmd := testStatusCommand(t.TempDir())
	cmd.readCommand = func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "rev-parse", "--is-inside-work-tree"}):
			return []byte("true\n"), nil
		case name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "symbolic-ref", "--quiet", "--short", "HEAD"}):
			return []byte("main\n"), nil
		case name == "git" && reflect.DeepEqual(args, []string{"-C", "/repo", "status", "--porcelain=v1", "--branch"}):
			return []byte("## main...origin/main [ahead 2, behind 1]\nM  staged.go\n M dirty.go\nA  added.go\n?? new.go\n"), nil
		default:
			return nil, os.ErrNotExist
		}
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"git", "/repo"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := " #[bold,fg=colour231,bg=colour30] main #[nobold,fg=colour222]*#[bold,fg=colour231] #[nobold,fg=colour151]+2#[bold,fg=colour231] #[nobold,fg=colour153]↑2#[bold,fg=colour231] #[nobold,fg=colour181]↓1#[bold,fg=colour231] #[default]"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestStatusGitCommandTimeoutStopsHangingRunner(t *testing.T) {
	t.Parallel()

	cmd := testStatusCommand(t.TempDir())
	cmd.commandLimit = 20 * time.Millisecond
	active := 0
	cmd.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name != "git" || !reflect.DeepEqual(args, []string{"-C", "/slow", "rev-parse", "--is-inside-work-tree"}) {
			return nil, os.ErrNotExist
		}
		active++
		defer func() { active-- }()
		<-ctx.Done()
		return nil, ctx.Err()
	}

	start := time.Now()
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"git", "/slow"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Run() elapsed = %v, want bounded execution", elapsed)
	}
	if active != 0 {
		t.Fatalf("active hanging runners = %d, want 0 after timeout", active)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want silent timeout", got)
	}
}

func TestStatusGitStatePaletteIsCompactAndNonDominant(t *testing.T) {
	t.Parallel()

	got := parseGitPorcelainStatus("## main...origin/main [ahead 12, behind 3]\n M dirty.go\nA  staged.go\n?? new.go\n")
	want := "#[nobold,fg=colour222]*#[bold,fg=colour231] #[nobold,fg=colour151]+1#[bold,fg=colour231] #[nobold,fg=colour153]↑12#[bold,fg=colour231] #[nobold,fg=colour181]↓3#[bold,fg=colour231]"
	if got != want {
		t.Fatalf("parseGitPorcelainStatus() = %q, want %q", got, want)
	}
	for _, disallowed := range []string{"fg=colour88]", "fg=colour22]", "fg=colour17]", "fg=colour94]", "bg=colour45"} {
		if strings.Contains(got, disallowed) {
			t.Fatalf("git state = %q, still contains old dominant color %q", got, disallowed)
		}
	}
}

func TestStatusRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing", args: nil, want: "status requires a subcommand"},
		{name: "unknown", args: []string{"bad"}, want: "unknown status subcommand"},
		{name: "git args", args: []string{"git", "a", "b"}, want: "status git accepts at most 1"},
		{name: "resources args", args: []string{"resources", "extra"}, want: "status resources does not accept positional arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stderr bytes.Buffer
			err := testStatusCommand(t.TempDir()).Run(tt.args, &bytes.Buffer{}, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage", stderr.String())
			}
		})
	}
}

func testStatusCommand(home string) *statusCommand {
	return &statusCommand{
		lookupEnv: func(string) string { return "" },
		homeDir:   func() (string, error) { return home, nil },
		readCommand: func(context.Context, string, ...string) ([]byte, error) {
			return nil, os.ErrNotExist
		},
		now: func() time.Time { return time.Now() },
	}
}

// statusProjectTmuxFields stubs the three per-signal display-message reads that
// runProject issues (tmux escapes a packed field separator, so each signal is
// read on its own).
func statusProjectTmuxFields(session, anchor, cwd string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "#{session_name}"}) {
			return []byte(session + "\n"), nil
		}
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "#{@projmux_project_path}"}) {
			return []byte(anchor + "\n"), nil
		}
		if name == "tmux" && reflect.DeepEqual(args, []string{"display-message", "-p", "#{pane_current_path}"}) {
			return []byte(cwd + "\n"), nil
		}
		return nil, os.ErrNotExist
	}
}

func TestStatusProjectResolvesAnchorBasename(t *testing.T) {
	t.Parallel()

	cmd := testStatusCommand(t.TempDir())
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/tmux"
		}
		return ""
	}
	// anchor present -> Anchor source wins, no de-slug applied, drifted cwd ignored.
	cmd.readCommand = statusProjectTmuxFields("repos-app", "/home/tester/source/repos/app", "/tmp/drifted")

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"project"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stdout.String(); got != "app" {
		t.Fatalf("stdout = %q, want %q", got, "app")
	}
}

func TestStatusProjectDeSlugsSessionNameFallback(t *testing.T) {
	t.Parallel()

	cmd := testStatusCommand(t.TempDir())
	cmd.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return "/tmp/tmux"
		}
		return ""
	}
	// no anchor, no worktree; a non-existent pane cwd must not be trusted over
	// the session name, which is de-slugged for display.
	cmd.readCommand = statusProjectTmuxFields("repos-app", "", "/tmp/does-not-exist-projmux")

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"project"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stdout.String(); got != "app" {
		t.Fatalf("stdout = %q, want %q", got, "app")
	}
}

func TestStatusProjectSilentOutsideTmux(t *testing.T) {
	t.Parallel()

	cmd := testStatusCommand(t.TempDir())
	cmd.lookupEnv = func(string) string { return "" }
	called := false
	cmd.readCommand = func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		called = true
		return nil, os.ErrNotExist
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"project"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty outside tmux", stdout.String())
	}
	if called {
		t.Fatalf("must not query tmux when TMUX is unset")
	}
}
