package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/candidates"
	corepreview "github.com/crevissepartners/projmux/internal/core/preview"
	intfzf "github.com/crevissepartners/projmux/internal/ui/fzf"
)

func TestAppRunSwitchDefaultsToPopupAndOpensSelectedSession(t *testing.T) {
	t.Parallel()

	var gotInputs candidates.Inputs
	var gotRunnerOptions intfzf.Options
	executor := &capturingSwitchSessionExecutor{
		exists: map[string]bool{"workspace": true},
	}

	app := &App{
		switcher: &switchCommand{
			discover: func(inputs candidates.Inputs) ([]string, error) {
				gotInputs = inputs
				return []string{"/home/tester", "/home/tester/workspace"}, nil
			},
			pinStore: func() (switchPinStore, error) {
				return &stubSwitchPinStore{list: []string{"/pins/app"}}, nil
			},
			runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
				gotRunnerOptions = options
				return intfzf.Result{Value: "/home/tester/workspace"}, nil
			}),
			sessions:   executor,
			executable: func() (string, error) { return "/tmp/projmux", nil },
			identity: switchIdentityResolverFunc(func(path string) (string, error) {
				switch path {
				case "/home/tester/workspace":
					return "workspace", nil
				case "/home/tester":
					return "tester", nil
				default:
					return "", errors.New("unexpected path")
				}
			}),
			validate:   func(string) error { return nil },
			homeDir:    func() (string, error) { return "/home/tester", nil },
			workingDir: func() (string, error) { return "/rp/repo-a/nested", nil },
			lookupEnv: func(name string) string {
				switch name {
				case projdirEnvVar:
					return "/rp"
				case managedRootsEnvVar:
					return "/managed/a:/managed/b"
				default:
					return ""
				}
			},
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if err := app.Run([]string{"switch"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := stdout.String(), ""; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	if got, want := gotInputs.HomeDir, "/home/tester"; got != want {
		t.Fatalf("inputs.HomeDir = %q, want %q", got, want)
	}
	if got, want := gotInputs.RepoRoot, "/rp"; got != want {
		t.Fatalf("inputs.RepoRoot = %q, want %q", got, want)
	}
	if got, want := gotInputs.ManagedRoots, []string{"/managed/a", "/managed/b"}; !equalStrings(got, want) {
		t.Fatalf("inputs.ManagedRoots = %q, want %q", got, want)
	}
	if got, want := gotInputs.Pins, []string{"/pins/app"}; !equalStrings(got, want) {
		t.Fatalf("inputs.Pins = %q, want %q", got, want)
	}
	if got, want := gotInputs.CurrentPath, "/rp/repo-a/nested"; got != want {
		t.Fatalf("inputs.CurrentPath = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.UI, switchUIPopup; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.ExpectKeys, []string{switchKillExpectKey, switchPinExpectKey}; !equalStrings(got, want) {
		t.Fatalf("runner expect keys = %q, want %q", got, want)
	}
	if !gotRunnerOptions.Read0 {
		t.Fatal("runner Read0 = false, want true")
	}
	if got, want := gotRunnerOptions.Prompt, "› "; got != want {
		t.Fatalf("runner prompt = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Footer, "Enter: switch to previewed target\nCtrl-X: kill focused session\nAlt-P: pin/unpin focused directory\nLeft/Right: preview window\nAlt-Up/Alt-Down: preview pane"; got != want {
		t.Fatalf("runner footer = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.PreviewCommand, "exec '/tmp/projmux' 'switch' 'preview' '--ui=popup' {2}"; got != want {
		t.Fatalf("runner preview command = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.PreviewWindow, "right,60%,border-left"; got != want {
		t.Fatalf("runner preview window = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Bindings, []string{
		"esc:abort",
		"ctrl-n:abort",
		"alt-1:abort",
		"alt-2:abort",
		"alt-3:abort",
		"left:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-window' {2} 'prev')+refresh-preview",
		"right:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-window' {2} 'next')+refresh-preview",
		"alt-up:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-pane' {2} 'prev')+refresh-preview",
		"alt-down:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-pane' {2} 'next')+refresh-preview",
	}; !equalStrings(got, want) {
		t.Fatalf("runner bindings = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Candidates, []string{"/home/tester", "/home/tester/workspace"}; !equalStrings(got, want) {
		t.Fatalf("runner candidates = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Entries, []intfzf.Entry{
		{Label: "\x1b[1m\x1b[32mworkspace\x1b[0m\n  \x1b[38;5;242m~/workspace\x1b[0m", Value: "/home/tester/workspace"},
	}; !equalEntries(got, want) {
		t.Fatalf("runner entries = %#v, want %#v", got, want)
	}
	if got, want := gotRunnerOptions.Entries[0].SearchKey, "workspace"; got != want {
		t.Fatalf("runner entry search key = %q, want %q", got, want)
	}
	if got, want := executor.ensureSessionName, "workspace"; got != want {
		t.Fatalf("ensure session = %q, want %q", got, want)
	}
	if got, want := executor.ensureCWD, "/home/tester/workspace"; got != want {
		t.Fatalf("ensure cwd = %q, want %q", got, want)
	}
	if got, want := executor.openSessionName, "workspace"; got != want {
		t.Fatalf("open session = %q, want %q", got, want)
	}
}

func TestSwitchCommandSupportsSidebarUI(t *testing.T) {
	t.Parallel()

	var gotRunnerOptions intfzf.Options
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app"}, nil
		},
		pinStore: func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			gotRunnerOptions = options
			return intfzf.Result{Value: "/tmp/app"}, nil
		}),
		sessions:   &capturingSwitchSessionExecutor{},
		executable: func() (string, error) { return "/tmp/projmux", nil },
		identity:   stubSwitchIdentityResolver{name: "tmp-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--ui=sidebar"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), ""; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.UI, switchUISidebar; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Prompt, "› "; got != want {
		t.Fatalf("runner prompt = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Footer, "C-x: kill | M-p: pin"; got != want {
		t.Fatalf("runner footer = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.PreviewCommand, "exec '/tmp/projmux' 'switch' 'preview' '--ui=sidebar' {2}"; got != want {
		t.Fatalf("runner preview command = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.PreviewWindow, "down,25%,border-top"; got != want {
		t.Fatalf("runner preview window = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Bindings, []string{
		"esc:abort",
		"ctrl-n:abort",
		"alt-1:abort",
		"alt-2:abort",
		"alt-3:abort",
	}; !equalStrings(got, want) {
		t.Fatalf("runner bindings = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Entries, []intfzf.Entry{
		{Label: "\x1b[1mapp\x1b[0m\n  \x1b[38;5;242m/tmp/app\x1b[0m", Value: "/tmp/app"},
	}; !equalEntries(got, want) {
		t.Fatalf("runner entries = %#v, want %#v", got, want)
	}
}

func TestSwitchCommandSidebarRowsIncludeAttentionBadge(t *testing.T) {
	t.Parallel()

	var gotRunnerOptions intfzf.Options
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app"}, nil
		},
		pinStore: func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			gotRunnerOptions = options
			return intfzf.Result{}, nil
		}),
		sessions: &capturingSwitchSessionExecutor{exists: map[string]bool{"tmp-app": true}},
		inventory: &stubPreviewInventory{panes: []corepreview.Pane{{
			SessionName:    "tmp-app",
			Title:          "server",
			AttentionState: attentionStateBusy,
		}}},
		executable: func() (string, error) { return "/tmp/projmux", nil },
		identity:   stubSwitchIdentityResolver{name: "tmp-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	if err := cmd.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := gotRunnerOptions.Entries[0].Label, "\x1b[1m\x1b[32mapp\x1b[0m \x1b[33m●\x1b[0m\n  \x1b[38;5;242m/tmp/app\x1b[0m"; got != want {
		t.Fatalf("runner entry = %q, want %q", got, want)
	}
}

func TestSwitchCommandSidebarUsesContextSessionForInitialPosition(t *testing.T) {
	t.Parallel()

	var gotRunnerOptions intfzf.Options
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/a", "/tmp/b"}, nil
		},
		pinStore: func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			gotRunnerOptions = options
			return intfzf.Result{}, nil
		}),
		sessions: &capturingSwitchSessionExecutor{
			exists: map[string]bool{"session-b": true},
		},
		executable: func() (string, error) { return "/tmp/projmux", nil },
		identity: switchIdentityResolverFunc(func(path string) (string, error) {
			switch path {
			case "/tmp/a":
				return "session-a", nil
			case "/tmp/b":
				return "session-b", nil
			default:
				return "", errors.New("unexpected path")
			}
		}),
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp/a/deeper", nil },
		lookupEnv: func(name string) string {
			if name == switchContextSessionEnv {
				return "session-b"
			}
			return ""
		},
	}

	if err := cmd.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := gotRunnerOptions.Bindings, []string{
		"esc:abort",
		"ctrl-n:abort",
		"alt-1:abort",
		"alt-2:abort",
		"alt-3:abort",
		"start:pos(1)",
	}; !equalStrings(got, want) {
		t.Fatalf("runner bindings = %q, want %q", got, want)
	}
}

func TestSwitchCommandSidebarFocusOpensExistingSession(t *testing.T) {
	t.Parallel()

	executor := &capturingSwitchSessionExecutor{
		exists: map[string]bool{"tmp-app": true},
	}
	cmd := &switchCommand{
		sessions: executor,
		identity: stubSwitchIdentityResolver{name: "tmp-app"},
	}

	if err := cmd.Run([]string{"sidebar-focus", "/tmp/app"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := executor.openSessionName, "tmp-app"; got != want {
		t.Fatalf("open session = %q, want %q", got, want)
	}
	if got := executor.ensureSessionName; got != "" {
		t.Fatalf("ensure session called unexpectedly: %q", got)
	}
}

func TestSwitchCommandMarksExistingSessionsInRows(t *testing.T) {
	t.Parallel()

	var gotRunnerOptions intfzf.Options
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/new-app", "/tmp/live-app"}, nil
		},
		pinStore: func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			gotRunnerOptions = options
			return intfzf.Result{}, nil
		}),
		sessions: &capturingSwitchSessionExecutor{
			exists: map[string]bool{"tmp-live-app": true},
		},
		identity: switchIdentityResolverFunc(func(path string) (string, error) {
			switch path {
			case "/tmp/live-app":
				return "tmp-live-app", nil
			case "/tmp/new-app":
				return "tmp-new-app", nil
			default:
				return "", errors.New("unexpected path")
			}
		}),
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := gotRunnerOptions.Entries, []intfzf.Entry{
		{Label: "\x1b[1m\x1b[32mlive-app\x1b[0m\n  \x1b[38;5;242m/tmp/live-app\x1b[0m", Value: "/tmp/live-app"},
	}; !equalEntries(got, want) {
		t.Fatalf("runner entries = %#v, want %#v", got, want)
	}
}

func TestNewSwitchCommandUsesEnvAndDefaultPinStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fixture := newSwitchFixture(t)
	fixture.mkdir("home/workspace")
	fixture.mkdir("pins/app")
	fixture.mkdir("rp/repo-a")
	fixture.mkdir("managed/work-a/nested")
	fixture.mkdir("managed/work-b")

	configHome := fixture.path("xdg-config")
	stateHome := fixture.path("xdg-state")
	t.Setenv("HOME", fixture.path("home"))
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, fixture.path("rp"))
	t.Setenv(managedRootsEnvVar, fixture.path("managed"))

	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatalf("DefaultPathsFromEnv() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.PinFile()), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(paths.PinFile(), []byte(fixture.path("pins/app")+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	t.Chdir(fixture.path("managed/work-a/nested"))

	cmd := newSwitchCommand()
	fakeRunner := &capturingSwitchRunner{result: intfzf.Result{Value: fixture.path("managed/work-a")}}
	fakeExecutor := &capturingSwitchSessionExecutor{}
	cmd.runner = fakeRunner
	cmd.sessions = fakeExecutor
	cmd.executable = func() (string, error) { return "/tmp/projmux", nil }

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := cmd.Run([]string{"--ui=sidebar"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	if got, want := stdout.String(), ""; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	wantCandidates := []string{
		fixture.path("home"),
		fixture.path("pins/app"),
		fixture.path("managed/work-a"),
		fixture.path("rp/repo-a"),
		fixture.path("managed/work-b"),
	}
	if got := fakeRunner.last.Candidates; !equalStrings(got, wantCandidates) {
		t.Fatalf("runner candidates = %q, want %q", got, wantCandidates)
	}
	wantEntries := []intfzf.Entry{
		{Label: "\x1b[1mhome\x1b[0m\n  \x1b[38;5;242m~\x1b[0m", Value: fixture.path("home")},
		{Label: "\x1b[1mapp\x1b[0m \x1b[33m*\x1b[0m\n  \x1b[38;5;242m" + fixture.path("pins/app") + "\x1b[0m", Value: fixture.path("pins/app")},
		{Label: "\x1b[1mrepo-a\x1b[0m\n  \x1b[38;5;242m~rp/repo-a\x1b[0m", Value: fixture.path("rp/repo-a")},
		{Label: "\x1b[1mwork-a\x1b[0m\n  \x1b[38;5;242m" + fixture.path("managed/work-a") + "\x1b[0m", Value: fixture.path("managed/work-a")},
		{Label: "\x1b[1mwork-b\x1b[0m\n  \x1b[38;5;242m" + fixture.path("managed/work-b") + "\x1b[0m", Value: fixture.path("managed/work-b")},
	}
	if got := fakeRunner.last.Entries; !equalEntries(got, wantEntries) {
		t.Fatalf("runner entries = %#v, want %#v", got, wantEntries)
	}
	if got, want := fakeRunner.last.PreviewCommand, "exec '/tmp/projmux' 'switch' 'preview' '--ui=sidebar' {2}"; got != want {
		t.Fatalf("runner preview command = %q, want %q", got, want)
	}
	if got, want := fakeRunner.last.PreviewWindow, "down,25%,border-top"; got != want {
		t.Fatalf("runner preview window = %q, want %q", got, want)
	}
	if got, want := fakeRunner.last.Bindings, []string{
		"esc:abort",
		"ctrl-n:abort",
		"alt-1:abort",
		"alt-2:abort",
		"alt-3:abort",
		"start:pos(4)",
	}; !equalStrings(got, want) {
		t.Fatalf("runner bindings = %q, want %q", got, want)
	}
	if got, want := fakeRunner.last.UI, switchUISidebar; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
	}
	if got, want := fakeRunner.last.Footer, "C-x: kill | M-p: pin"; got != want {
		t.Fatalf("runner footer = %q, want %q", got, want)
	}
	if got, want := fakeExecutor.ensureSessionName, "managed-work-a"; got != want {
		t.Fatalf("ensure session = %q, want %q", got, want)
	}
	if got, want := fakeExecutor.ensureCWD, fixture.path("managed/work-a"); got != want {
		t.Fatalf("ensure cwd = %q, want %q", got, want)
	}
	if got, want := fakeExecutor.openSessionName, "managed-work-a"; got != want {
		t.Fatalf("open session = %q, want %q", got, want)
	}
}

func TestNewSwitchCommandInfersRepoRootFromHomeSourceRepos(t *testing.T) {
	fixture := newSwitchFixture(t)
	fixture.mkdir("home")
	fixture.mkdir("home/source/repos/app/nested")
	fixture.mkdir("home/source/repos/lib")

	t.Setenv("HOME", fixture.path("home"))
	t.Setenv("XDG_CONFIG_HOME", fixture.path("xdg-config"))
	t.Setenv("XDG_STATE_HOME", fixture.path("xdg-state"))
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, "")
	t.Chdir(fixture.path("home/source/repos/app/nested"))

	cmd := newSwitchCommand()
	fakeRunner := &capturingSwitchRunner{result: intfzf.Result{}}
	cmd.runner = fakeRunner
	cmd.sessions = &capturingSwitchSessionExecutor{}
	cmd.executable = func() (string, error) { return "/tmp/projmux", nil }

	if err := cmd.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	wantCandidates := []string{
		fixture.path("home"),
		fixture.path("home/source/repos/app"),
		fixture.path("home/source/repos/lib"),
		fixture.path("home/source/repos"),
	}
	if got := fakeRunner.last.Candidates; !equalStrings(got, wantCandidates) {
		t.Fatalf("runner candidates = %q, want %q", got, wantCandidates)
	}

	wantEntries := []intfzf.Entry{
		{Label: "\x1b[1mhome\x1b[0m\n  \x1b[38;5;242m~\x1b[0m", Value: fixture.path("home")},
		{Label: "\x1b[1mapp\x1b[0m\n  \x1b[38;5;242m~rp/app\x1b[0m", Value: fixture.path("home/source/repos/app")},
		{Label: "\x1b[1mlib\x1b[0m\n  \x1b[38;5;242m~rp/lib\x1b[0m", Value: fixture.path("home/source/repos/lib")},
		{Label: "\x1b[1mrepos\x1b[0m\n  \x1b[38;5;242m~rp\x1b[0m", Value: fixture.path("home/source/repos")},
	}
	if got := fakeRunner.last.Entries; !equalEntries(got, wantEntries) {
		t.Fatalf("runner entries = %#v, want %#v", got, wantEntries)
	}
}

func TestSwitchCommandRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "invalid ui",
			args: []string{"--ui=dialog"},
			want: "invalid --ui value",
		},
		{
			name: "positional args",
			args: []string{"extra"},
			want: "switch does not accept positional arguments",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			err := (&switchCommand{
				discover:   func(candidates.Inputs) ([]string, error) { return nil, nil },
				pinStore:   func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
				runner:     switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) { return intfzf.Result{}, nil }),
				sessions:   &capturingSwitchSessionExecutor{},
				identity:   stubSwitchIdentityResolver{name: "tmp"},
				validate:   func(string) error { return nil },
				homeDir:    func() (string, error) { return "/home/tester", nil },
				workingDir: func() (string, error) { return "/tmp", nil },
			}).Run(tt.args, &bytes.Buffer{}, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage text", stderr.String())
			}
		})
	}
}

func TestSwitchCommandPropagatesSetupErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  *switchCommand
		want string
	}{
		{
			name: "home dir",
			cmd: &switchCommand{
				homeDir: func() (string, error) { return "", errors.New("no home") },
			},
			want: "resolve home directory",
		},
		{
			name: "pin store",
			cmd: &switchCommand{
				homeDir:  func() (string, error) { return "/home/tester", nil },
				pinStore: func() (switchPinStore, error) { return nil, errors.New("no config") },
			},
			want: "configure pin store",
		},
		{
			name: "working dir",
			cmd: &switchCommand{
				homeDir:  func() (string, error) { return "/home/tester", nil },
				pinStore: func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
				runner:   switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) { return intfzf.Result{}, nil }),
				workingDir: func() (string, error) {
					return "", errors.New("no cwd")
				},
			},
			want: "resolve current working directory",
		},
		{
			name: "runner",
			cmd: &switchCommand{
				discover:   func(candidates.Inputs) ([]string, error) { return []string{"/tmp/app"}, nil },
				homeDir:    func() (string, error) { return "/home/tester", nil },
				pinStore:   func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
				workingDir: func() (string, error) { return "/tmp", nil },
				identity:   stubSwitchIdentityResolver{name: "tmp-app"},
				runner: switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
					return intfzf.Result{}, errors.New("fzf exploded")
				}),
			},
			want: "run switch picker",
		},
		{
			name: "identity setup",
			cmd: &switchCommand{
				discover:    func(candidates.Inputs) ([]string, error) { return []string{"/tmp/app"}, nil },
				homeDir:     func() (string, error) { return "/home/tester", nil },
				pinStore:    func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
				workingDir:  func() (string, error) { return "/tmp", nil },
				runner:      switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) { return intfzf.Result{Value: "/tmp/app"}, nil }),
				validate:    func(string) error { return nil },
				identityErr: errors.New("missing home"),
			},
			want: "configure session identity resolver",
		},
		{
			name: "open session",
			cmd: &switchCommand{
				discover:   func(candidates.Inputs) ([]string, error) { return []string{"/tmp/app"}, nil },
				homeDir:    func() (string, error) { return "/home/tester", nil },
				pinStore:   func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
				workingDir: func() (string, error) { return "/tmp", nil },
				runner:     switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) { return intfzf.Result{Value: "/tmp/app"}, nil }),
				identity:   stubSwitchIdentityResolver{name: "tmp-app"},
				validate:   func(string) error { return nil },
				sessions: &capturingSwitchSessionExecutor{
					openErr: errors.New("attach exploded"),
				},
			},
			want: "open tmux session",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestSwitchCommandAllowsEmptySelection(t *testing.T) {
	t.Parallel()

	cmd := &switchCommand{
		discover:   func(candidates.Inputs) ([]string, error) { return []string{"/tmp/a"}, nil },
		pinStore:   func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		runner:     switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) { return intfzf.Result{}, nil }),
		sessions:   &capturingSwitchSessionExecutor{},
		identity:   stubSwitchIdentityResolver{name: "tmp-a"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want empty", got)
	}
}

func TestSwitchCommandToggleTagUsesCurrentSnappedCandidate(t *testing.T) {
	t.Parallel()

	fixture := newSwitchFixture(t)
	fixture.mkdir("home/workspace")
	fixture.mkdir("managed/work-a/nested")

	store := &capturingSwitchTagStore{tagged: true}
	cmd := &switchCommand{
		discover: candidates.Discover,
		pinStore: func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		tagStore: func() (switchTagStore, error) { return store, nil },
		validate: validateDirectory,
		homeDir:  func() (string, error) { return fixture.path("home"), nil },
		workingDir: func() (string, error) {
			return fixture.path("managed/work-a/nested"), nil
		},
		lookupEnv: func(name string) string {
			if name == managedRootsEnvVar {
				return fixture.path("managed")
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := cmd.Run([]string{"toggle-tag"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := store.calls, []string{fixture.path("managed/work-a")}; !equalStrings(got, want) {
		t.Fatalf("Toggle() calls = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "tagged: "+fixture.path("managed/work-a")+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestSwitchCommandUsesDefaultManagedRootsWhenEnvUnset(t *testing.T) {
	t.Parallel()

	var gotInputs candidates.Inputs
	cmd := &switchCommand{
		discover: func(inputs candidates.Inputs) ([]string, error) {
			gotInputs = inputs
			return []string{"/tmp/app"}, nil
		},
		pinStore:   func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		runner:     switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) { return intfzf.Result{}, nil }),
		sessions:   &capturingSwitchSessionExecutor{},
		identity:   stubSwitchIdentityResolver{name: "tmp-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
		lookupEnv:  func(string) string { return "" },
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := gotInputs.ManagedRoots, []string{
		"/home/tester/source",
		"/home/tester/work",
		"/home/tester/projects",
		"/home/tester/src",
		"/home/tester/code",
		"/home/tester/source/repos",
	}; !equalStrings(got, want) {
		t.Fatalf("inputs.ManagedRoots = %q, want %q", got, want)
	}
}

func TestSwitchCommandUsesSavedWorkdirsWhenEnvUnset(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, "")

	var gotInputs candidates.Inputs
	cmd := &switchCommand{
		discover: func(inputs candidates.Inputs) ([]string, error) {
			gotInputs = inputs
			return []string{"/tmp/app"}, nil
		},
		pinStore:   func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		runner:     switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) { return intfzf.Result{}, nil }),
		sessions:   &capturingSwitchSessionExecutor{},
		identity:   stubSwitchIdentityResolver{name: "tmp-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
		lookupEnv:  func(string) string { return "" },
		loadWorkdirs: func(homeDir string) ([]string, error) {
			if homeDir != "/home/tester" {
				t.Fatalf("loadWorkdirs homeDir = %q, want /home/tester", homeDir)
			}
			return []string{"/srv/projects", "/srv/lib"}, nil
		},
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := gotInputs.ManagedRoots, []string{
		"/srv/projects",
		"/srv/lib",
	}; !equalStrings(got, want) {
		t.Fatalf("inputs.ManagedRoots = %q, want %q", got, want)
	}
}

func TestSwitchCommandManagedRootsEnvBeatsSavedWorkdirs(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, "")

	var gotInputs candidates.Inputs
	cmd := &switchCommand{
		discover: func(inputs candidates.Inputs) ([]string, error) {
			gotInputs = inputs
			return []string{"/tmp/app"}, nil
		},
		pinStore:   func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		runner:     switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) { return intfzf.Result{}, nil }),
		sessions:   &capturingSwitchSessionExecutor{},
		identity:   stubSwitchIdentityResolver{name: "tmp-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
		lookupEnv: func(name string) string {
			if name == managedRootsEnvVar {
				return "/env/one:/env/two"
			}
			return ""
		},
		loadWorkdirs: func(string) ([]string, error) {
			t.Fatalf("loadWorkdirs should not be consulted when env is set")
			return nil, nil
		},
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := gotInputs.ManagedRoots, []string{
		"/env/one",
		"/env/two",
	}; !equalStrings(got, want) {
		t.Fatalf("inputs.ManagedRoots = %q, want %q", got, want)
	}
}

func TestSwitchCommandMultiPathProjdirSplitsPrimaryAndExtras(t *testing.T) {
	t.Setenv("TMUX", "")

	multi := strings.Join([]string{"/main/repo", "/extra/one", "/extra/two"}, string(os.PathListSeparator))
	t.Setenv(projdirEnvVar, multi)

	var gotInputs candidates.Inputs
	cmd := &switchCommand{
		discover: func(inputs candidates.Inputs) ([]string, error) {
			gotInputs = inputs
			return []string{"/tmp/app"}, nil
		},
		pinStore:   func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		runner:     switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) { return intfzf.Result{}, nil }),
		sessions:   &capturingSwitchSessionExecutor{},
		identity:   stubSwitchIdentityResolver{name: "tmp-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
		lookupEnv:  os.Getenv,
		loadWorkdirs: func(string) ([]string, error) {
			t.Fatalf("loadWorkdirs should not be consulted when PROJMUX_PROJDIR provides extras")
			return nil, nil
		},
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := gotInputs.RepoRoot, "/main/repo"; got != want {
		t.Fatalf("inputs.RepoRoot = %q, want %q", got, want)
	}
	if got, want := gotInputs.ManagedRoots, []string{
		"/extra/one",
		"/extra/two",
	}; !equalStrings(got, want) {
		t.Fatalf("inputs.ManagedRoots = %q, want %q", got, want)
	}
}

func TestSwitchCommandMultiPathProjdirCombinesWithManagedRootsEnv(t *testing.T) {
	t.Setenv("TMUX", "")

	multi := strings.Join([]string{"/main/repo", "/extra/one"}, string(os.PathListSeparator))
	t.Setenv(projdirEnvVar, multi)
	t.Setenv(managedRootsEnvVar, strings.Join([]string{"/extra/one", "/managed/two"}, string(os.PathListSeparator)))

	var gotInputs candidates.Inputs
	cmd := &switchCommand{
		discover: func(inputs candidates.Inputs) ([]string, error) {
			gotInputs = inputs
			return []string{"/tmp/app"}, nil
		},
		pinStore:   func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		runner:     switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) { return intfzf.Result{}, nil }),
		sessions:   &capturingSwitchSessionExecutor{},
		identity:   stubSwitchIdentityResolver{name: "tmp-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
		lookupEnv:  os.Getenv,
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// extras prepend; PROJMUX_MANAGED_ROOTS follows, dedupe drops the
	// repeated /extra/one.
	if got, want := gotInputs.ManagedRoots, []string{
		"/extra/one",
		"/managed/two",
	}; !equalStrings(got, want) {
		t.Fatalf("inputs.ManagedRoots = %q, want %q", got, want)
	}
}

func TestSwitchCommandSinglePathProjdirRetainsSavedWorkdirs(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv(projdirEnvVar, "/main/repo")

	var gotInputs candidates.Inputs
	cmd := &switchCommand{
		discover: func(inputs candidates.Inputs) ([]string, error) {
			gotInputs = inputs
			return []string{"/tmp/app"}, nil
		},
		pinStore:   func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		runner:     switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) { return intfzf.Result{}, nil }),
		sessions:   &capturingSwitchSessionExecutor{},
		identity:   stubSwitchIdentityResolver{name: "tmp-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
		lookupEnv:  os.Getenv,
		loadWorkdirs: func(string) ([]string, error) {
			return []string{"/srv/projects"}, nil
		},
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := gotInputs.RepoRoot, "/main/repo"; got != want {
		t.Fatalf("inputs.RepoRoot = %q, want %q", got, want)
	}
	if got, want := gotInputs.ManagedRoots, []string{"/srv/projects"}; !equalStrings(got, want) {
		t.Fatalf("inputs.ManagedRoots = %q, want %q (single-path PROJMUX_PROJDIR must not suppress saved workdirs)", got, want)
	}
}

func TestSwitchCommandPreviewRendersExistingSessionContext(t *testing.T) {
	t.Parallel()

	fixture := newSwitchFixture(t)
	fixture.mkdir("home/source/repos/repo-a/subdir")

	store := &stubPreviewStore{
		readSelection: corepreview.Selection{
			SessionName: "repo-a",
			WindowIndex: "2",
			PaneIndex:   "1",
		},
		readFound: true,
	}
	inventory := &stubPreviewInventory{
		windows: []corepreview.Window{
			{Index: "1"},
			{Index: "2", Active: true},
		},
		panes: []corepreview.Pane{
			{WindowIndex: "2", Index: "0"},
			{ID: "%9", WindowIndex: "2", Index: "1", Active: true},
		},
		snapshot: "npm test\nok",
	}
	cmd := &switchCommand{
		discover:     candidates.Discover,
		pinStore:     func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		sessions:     &capturingSwitchSessionExecutor{exists: map[string]bool{"repo-a": true}},
		previewStore: store,
		inventory:    inventory,
		gitBranch:    func(string) string { return "main" },
		identity:     stubSwitchIdentityResolver{name: "repo-a"},
		validate:     validateDirectory,
		homeDir:      func() (string, error) { return fixture.path("home"), nil },
		workingDir:   func() (string, error) { return fixture.path("home/source/repos/repo-a/subdir"), nil },
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return fixture.path("home/source/repos")
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"preview"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := "" +
		"\x1b[1m\x1b[36mTarget\x1b[0m\n" +
		"  \x1b[2msession\x1b[0m  repo-a\n" +
		"  \x1b[2mmode\x1b[0m  \x1b[32mexisting\x1b[0m\n" +
		"\n" +
		"\x1b[1m\x1b[36mWindows\x1b[0m\n" +
		"[1] -                   0p\n" +
		"\x1b[1m\x1b[32m[2] -                   0p\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mPanes\x1b[0m\n" +
		"[2.0] -                  -\n" +
		"\x1b[1m\x1b[32m[2.1] -                  -\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mPane Snapshot\x1b[0m\n" +
		"\x1b[2m────────────────────────────────────────────────────────────────\x1b[0m\n" +
		"npm test\nok\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got, want := inventory.sessionWindowsSession, "repo-a"; got != want {
		t.Fatalf("SessionWindows session = %q, want %q", got, want)
	}
	if got, want := inventory.sessionPanesSession, "repo-a"; got != want {
		t.Fatalf("SessionPanes session = %q, want %q", got, want)
	}
	if got, want := inventory.snapshotTarget, "%9"; got != want {
		t.Fatalf("CapturePane target = %q, want %q", got, want)
	}
	if got, want := inventory.snapshotStartLine, -60; got != want {
		t.Fatalf("CapturePane start line = %d, want %d", got, want)
	}
}

func TestSwitchCommandPreviewRendersNewSessionContextWithoutInventory(t *testing.T) {
	t.Parallel()

	fixture := newSwitchFixture(t)
	fixture.mkdir("home/source/repos/repo-a/subdir")

	inventory := &stubPreviewInventory{}
	cmd := &switchCommand{
		discover:     candidates.Discover,
		pinStore:     func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		sessions:     &capturingSwitchSessionExecutor{},
		previewStore: &stubPreviewStore{},
		inventory:    inventory,
		identity:     stubSwitchIdentityResolver{name: "repo-a"},
		validate:     validateDirectory,
		homeDir:      func() (string, error) { return fixture.path("home"), nil },
		workingDir:   func() (string, error) { return fixture.path("home/source/repos/repo-a/subdir"), nil },
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return fixture.path("home/source/repos")
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"preview"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := "" +
		"\x1b[1m\x1b[36mTarget\x1b[0m\n" +
		"  \x1b[2msession\x1b[0m  repo-a\n" +
		"  \x1b[2mmode\x1b[0m  \x1b[33mnew session\x1b[0m\n\n" +
		"\x1b[1m\x1b[36mAction\x1b[0m\n" +
		"  \x1b[2menter\x1b[0m  switch/create this session\n" +
		"  \x1b[2mresult\x1b[0m  tmux new-session -d -s <name> -c <dir>\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := inventory.sessionWindowsSession; got != "" {
		t.Fatalf("SessionWindows session = %q, want empty", got)
	}
	if got := inventory.sessionPanesSession; got != "" {
		t.Fatalf("SessionPanes session = %q, want empty", got)
	}
}

func TestSwitchCommandPreviewRendersSettingsSentinel(t *testing.T) {
	t.Parallel()

	cmd := &switchCommand{
		pinStore: func() (switchPinStore, error) {
			return &stubSwitchPinStore{list: []string{"/home/tester/source/repos/app"}}, nil
		},
		homeDir: func() (string, error) { return "/home/tester", nil },
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return "/home/tester/source/repos"
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"preview", switchSettingsSentinel}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := "" +
		"settings\n" +
		"pins:\n" +
		"  * ~rp/app\n" +
		"keys:\n" +
		"  enter  open settings menu\n" +
		"  alt-p  pin/unpin focused directory\n" +
		"menu:\n" +
		"  + add pin...\n" +
		"  + add current pin\n" +
		"  x remove pin\n" +
		"  x clear all pins\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSwitchCommandSettingsMenuOffersAddCurrentPin(t *testing.T) {
	t.Parallel()

	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/home/tester/source/repos/app", "/home/tester/source/repos/new-app"}, nil
		},
		pinStore: func() (switchPinStore, error) {
			return &stubSwitchPinStore{list: []string{"/home/tester/source/repos/app"}}, nil
		},
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/home/tester/source/repos/new-app/subdir", nil },
		validate:   func(string) error { return nil },
		identity:   stubSwitchIdentityResolver{name: "new-app"},
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return "/home/tester/source/repos"
			}
			return ""
		},
	}

	entries, err := cmd.settingsEntries()
	if err != nil {
		t.Fatalf("settingsEntries() error = %v", err)
	}

	want := []intfzf.Entry{
		{Label: "+ Add pin...", Value: "add-interactive"},
		{Label: "+ Add current pin  ~rp/new-app", Value: "add:/home/tester/source/repos/new-app"},
		{Label: "x Clear all pins", Value: "clear"},
		{Label: "x Remove  ~rp/app", Value: "pin:/home/tester/source/repos/app"},
	}
	if !equalEntries(entries, want) {
		t.Fatalf("settings entries = %#v, want %#v", entries, want)
	}
}

func TestSwitchCommandSettingsMenuSkipsAddWhenCurrentTargetAlreadyPinned(t *testing.T) {
	t.Parallel()

	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/home/tester/source/repos/app"}, nil
		},
		pinStore: func() (switchPinStore, error) {
			return &stubSwitchPinStore{list: []string{"/home/tester/source/repos/app"}}, nil
		},
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/home/tester/source/repos/app/subdir", nil },
		validate:   func(string) error { return nil },
		identity:   stubSwitchIdentityResolver{name: "app"},
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return "/home/tester/source/repos"
			}
			return ""
		},
	}

	entries, err := cmd.settingsEntries()
	if err != nil {
		t.Fatalf("settingsEntries() error = %v", err)
	}

	want := []intfzf.Entry{
		{Label: "+ Add pin...", Value: "add-interactive"},
		{Label: "x Clear all pins", Value: "clear"},
		{Label: "x Remove  ~rp/app", Value: "pin:/home/tester/source/repos/app"},
	}
	if !equalEntries(entries, want) {
		t.Fatalf("settings entries = %#v, want %#v", entries, want)
	}
}

func TestSwitchCommandCycleWindowUpdatesStoredPreviewSelection(t *testing.T) {
	t.Parallel()

	fixture := newSwitchFixture(t)
	fixture.mkdir("home/source/repos/repo-a/subdir")

	store := &stubPreviewStore{
		cycleWindowResult: corepreview.CycleResult{
			Cursor:   corepreview.Cursor{WindowIndex: "3", PaneIndex: "1"},
			Selected: true,
			Changed:  true,
		},
	}
	inventory := &stubPreviewInventory{
		windows: []corepreview.Window{
			{Index: "2"},
			{Index: "3", Active: true},
		},
		panes: []corepreview.Pane{
			{WindowIndex: "3", Index: "1", Active: true},
		},
	}
	cmd := &switchCommand{
		discover:     candidates.Discover,
		sessions:     &capturingSwitchSessionExecutor{exists: map[string]bool{"repo-a": true}},
		previewStore: store,
		inventory:    inventory,
		identity:     stubSwitchIdentityResolver{name: "repo-a"},
		validate:     validateDirectory,
		homeDir:      func() (string, error) { return fixture.path("home"), nil },
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return fixture.path("home/source/repos")
			}
			return ""
		},
	}

	if err := cmd.Run([]string{"cycle-window", fixture.path("home/source/repos/repo-a/subdir"), "next"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := store.cycleWindowSession, "repo-a"; got != want {
		t.Fatalf("cycle window session = %q, want %q", got, want)
	}
	if got, want := store.cycleWindowDirection, corepreview.DirectionNext; got != want {
		t.Fatalf("cycle window direction = %q, want %q", got, want)
	}
	if got, want := inventory.sessionWindowsSession, "repo-a"; got != want {
		t.Fatalf("SessionWindows session = %q, want %q", got, want)
	}
	if got, want := inventory.sessionPanesSession, "repo-a"; got != want {
		t.Fatalf("SessionPanes session = %q, want %q", got, want)
	}
}

func TestSwitchCommandCyclePaneNoOpsForNewSessionCandidates(t *testing.T) {
	t.Parallel()

	fixture := newSwitchFixture(t)
	fixture.mkdir("home/source/repos/repo-a/subdir")

	store := &stubPreviewStore{}
	inventory := &stubPreviewInventory{}
	cmd := &switchCommand{
		discover:     candidates.Discover,
		sessions:     &capturingSwitchSessionExecutor{},
		previewStore: store,
		inventory:    inventory,
		identity:     stubSwitchIdentityResolver{name: "repo-a"},
		validate:     validateDirectory,
		homeDir:      func() (string, error) { return fixture.path("home"), nil },
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return fixture.path("home/source/repos")
			}
			return ""
		},
	}

	if err := cmd.Run([]string{"cycle-pane", fixture.path("home/source/repos/repo-a/subdir"), "prev"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := store.cyclePaneSession; got != "" {
		t.Fatalf("cycle pane session = %q, want empty", got)
	}
	if got := inventory.sessionPanesSession; got != "" {
		t.Fatalf("SessionPanes session = %q, want empty", got)
	}
}

func TestSwitchCommandPickerCtrlXSwitchesToPreviousActiveSessionBeforeKill(t *testing.T) {
	t.Parallel()

	var gotRunnerOptions []intfzf.Options
	executor := &capturingSwitchSessionExecutor{
		exists: map[string]bool{
			"tmp-app":      true,
			"tmp-previous": true,
		},
		recentSessions: []string{"tmp-app", "tmp-previous"},
	}
	call := 0

	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app", "/tmp/previous"}, nil
		},
		pinStore: func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			gotRunnerOptions = append(gotRunnerOptions, options)
			call++
			if call == 1 {
				return intfzf.Result{Key: switchKillExpectKey, Value: "/tmp/app"}, nil
			}
			return intfzf.Result{}, nil
		}),
		sessions:   executor,
		executable: func() (string, error) { return "/tmp/projmux", nil },
		identity: switchIdentityResolverFunc(func(path string) (string, error) {
			switch path {
			case "/tmp/app":
				return "tmp-app", nil
			case "/tmp/previous":
				return "tmp-previous", nil
			default:
				return "", errors.New("unexpected path")
			}
		}),
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--ui=sidebar"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := len(gotRunnerOptions), 2; got != want {
		t.Fatalf("runner calls = %d, want %d", got, want)
	}
	for i, options := range gotRunnerOptions {
		if got, want := options.ExpectKeys, []string{switchKillExpectKey, switchPinExpectKey}; !equalStrings(got, want) {
			t.Fatalf("runner expect keys call %d = %q, want %q", i, got, want)
		}
		if got, want := options.UI, switchUISidebar; got != want {
			t.Fatalf("runner UI call %d = %q, want %q", i, got, want)
		}
	}
	if !containsString(gotRunnerOptions[1].Bindings, "start:pos(2)") {
		t.Fatalf("second runner bindings = %q, want fallback focus start:pos(2)", gotRunnerOptions[1].Bindings)
	}
	if got, want := executor.killSessionName, "tmp-app"; got != want {
		t.Fatalf("kill session = %q, want %q", got, want)
	}
	if got, want := executor.openSessionName, "tmp-previous"; got != want {
		t.Fatalf("fallback open session = %q, want %q", got, want)
	}
	if got := executor.ensureSessionName; got != "" {
		t.Fatalf("ensure session called unexpectedly: %q", got)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want empty for ctrl-x loop", got)
	}
}

func TestSwitchCommandPickerCtrlXBlocksKillWithoutPreviousLiveSession(t *testing.T) {
	t.Parallel()

	executor := &capturingSwitchSessionExecutor{
		exists:         map[string]bool{"tmp-app": true},
		recentSessions: []string{"tmp-app"},
	}
	call := 0
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app"}, nil
		},
		pinStore: func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		runner: switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
			call++
			if call == 1 {
				return intfzf.Result{Key: switchKillExpectKey, Value: "/tmp/app"}, nil
			}
			return intfzf.Result{}, nil
		}),
		sessions:   executor,
		identity:   stubSwitchIdentityResolver{name: "tmp-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := executor.killSessionName; got != "" {
		t.Fatalf("kill session called unexpectedly: %q", got)
	}
	if got := executor.openSessionName; got != "" {
		t.Fatalf("open session called unexpectedly: %q", got)
	}
}

func TestSwitchCommandPickerCtrlXDoesNotKillHome(t *testing.T) {
	t.Parallel()

	executor := &capturingSwitchSessionExecutor{exists: map[string]bool{"home": true}}
	call := 0
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/home/tester"}, nil
		},
		pinStore: func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		runner: switchRunnerFunc(func(intfzf.Options) (intfzf.Result, error) {
			call++
			if call == 1 {
				return intfzf.Result{Key: switchKillExpectKey, Value: "/home/tester"}, nil
			}
			return intfzf.Result{}, nil
		}),
		sessions:   executor,
		identity:   stubSwitchIdentityResolver{name: "home"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/home/tester", nil },
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := executor.killSessionName; got != "" {
		t.Fatalf("kill session called unexpectedly: %q", got)
	}
}

func TestSwitchCommandPickerAltPLoopsUntilSelection(t *testing.T) {
	t.Parallel()

	var gotRunnerOptions []intfzf.Options
	store := &stubSwitchPinStore{toggled: true}
	executor := &capturingSwitchSessionExecutor{}
	call := 0

	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app"}, nil
		},
		pinStore: func() (switchPinStore, error) { return store, nil },
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			gotRunnerOptions = append(gotRunnerOptions, options)
			call++
			if call == 1 {
				return intfzf.Result{Key: switchPinExpectKey, Value: "/tmp/app"}, nil
			}
			return intfzf.Result{Value: "/tmp/app"}, nil
		}),
		sessions:   executor,
		identity:   stubSwitchIdentityResolver{name: "tmp-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	var stdout bytes.Buffer
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := len(gotRunnerOptions), 2; got != want {
		t.Fatalf("runner calls = %d, want %d", got, want)
	}
	for i, options := range gotRunnerOptions {
		if got, want := options.ExpectKeys, []string{switchKillExpectKey, switchPinExpectKey}; !equalStrings(got, want) {
			t.Fatalf("runner expect keys call %d = %q, want %q", i, got, want)
		}
	}
	if got, want := store.toggleCalls, []string{"/tmp/app"}; !equalStrings(got, want) {
		t.Fatalf("Toggle() calls = %q, want %q", got, want)
	}
	if got, want := executor.ensureSessionName, "tmp-app"; got != want {
		t.Fatalf("ensure session = %q, want %q", got, want)
	}
	if got, want := executor.openSessionName, "tmp-app"; got != want {
		t.Fatalf("open session = %q, want %q", got, want)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want empty for alt-p loop", got)
	}
}

func TestSwitchCommandSettingsSubcommandRunsSettingsMenu(t *testing.T) {
	t.Parallel()

	var runnerCalls int
	store := &stubSwitchPinStore{list: []string{"/tmp/app"}, toggled: false}
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app"}, nil
		},
		pinStore: func() (switchPinStore, error) { return store, nil },
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			runnerCalls++
			if runnerCalls == 1 {
				return intfzf.Result{Value: "clear"}, nil
			}
			return intfzf.Result{}, nil
		}),
		sessions:   &capturingSwitchSessionExecutor{},
		identity:   stubSwitchIdentityResolver{name: "tmp-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"settings"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := runnerCalls, 2; got != want {
		t.Fatalf("runner calls = %d, want %d", got, want)
	}
	if got, want := store.clearCalls, 1; got != want {
		t.Fatalf("clear calls = %d, want %d", got, want)
	}
}

func TestSwitchCommandSettingsMenuAddCurrentPin(t *testing.T) {
	t.Parallel()

	var runnerCalls int
	store := &stubSwitchPinStore{}
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/home/tester/source/repos/new-app"}, nil
		},
		pinStore: func() (switchPinStore, error) { return store, nil },
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			runnerCalls++
			if runnerCalls == 1 {
				return intfzf.Result{Value: "add:/home/tester/source/repos/new-app"}, nil
			}
			return intfzf.Result{}, nil
		}),
		sessions:   &capturingSwitchSessionExecutor{},
		identity:   stubSwitchIdentityResolver{name: "new-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/home/tester/source/repos/new-app/subdir", nil },
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return "/home/tester/source/repos"
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"settings"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := runnerCalls, 2; got != want {
		t.Fatalf("runner calls = %d, want %d", got, want)
	}
	if got, want := store.addCalls, []string{"/home/tester/source/repos/new-app"}; !equalStrings(got, want) {
		t.Fatalf("add calls = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "pinned: /home/tester/source/repos/new-app\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSwitchCommandSettingsMenuInteractiveAddPin(t *testing.T) {
	t.Parallel()

	var runnerCalls int
	store := &stubSwitchPinStore{list: []string{"/home/tester/source/repos/app"}}
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{
				"/home/tester/source/repos/app",
				"/home/tester/source/repos/new-app",
				"/home/tester/source/repos/lib",
			}, nil
		},
		pinStore: func() (switchPinStore, error) { return store, nil },
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			runnerCalls++
			if runnerCalls == 1 {
				if got, want := options.UI, "settings"; got != want {
					t.Fatalf("settings picker UI = %q, want %q", got, want)
				}
				return intfzf.Result{Value: "add-interactive"}, nil
			}
			if runnerCalls == 2 {
				if got, want := options.UI, "pin"; got != want {
					t.Fatalf("add-pin picker UI = %q, want %q", got, want)
				}
				wantEntries := []intfzf.Entry{
					{Label: "~rp/new-app", Value: "/home/tester/source/repos/new-app"},
					{Label: "~rp/lib", Value: "/home/tester/source/repos/lib"},
				}
				if !equalEntries(options.Entries, wantEntries) {
					t.Fatalf("add-pin entries = %#v, want %#v", options.Entries, wantEntries)
				}
				return intfzf.Result{Value: "/home/tester/source/repos/lib"}, nil
			}
			return intfzf.Result{}, nil
		}),
		sessions:   &capturingSwitchSessionExecutor{},
		identity:   stubSwitchIdentityResolver{name: "new-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/home/tester/source/repos/new-app/subdir", nil },
		lookupEnv: func(name string) string {
			if name == projdirEnvVar {
				return "/home/tester/source/repos"
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"settings"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := runnerCalls, 3; got != want {
		t.Fatalf("runner calls = %d, want %d", got, want)
	}
	if got, want := store.addCalls, []string{"/home/tester/source/repos/lib"}; !equalStrings(got, want) {
		t.Fatalf("add calls = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "pinned: /home/tester/source/repos/lib\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSwitchCommandToggleTagSnapsExplicitPathToCandidate(t *testing.T) {
	t.Parallel()

	fixture := newSwitchFixture(t)
	fixture.mkdir("home/workspace")
	fixture.mkdir("managed/work-a/nested/deeper")

	store := &capturingSwitchTagStore{tagged: false}
	cmd := &switchCommand{
		discover: candidates.Discover,
		pinStore: func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		tagStore: func() (switchTagStore, error) { return store, nil },
		validate: validateDirectory,
		homeDir:  func() (string, error) { return fixture.path("home"), nil },
		workingDir: func() (string, error) {
			return fixture.path("home"), nil
		},
		lookupEnv: func(name string) string {
			if name == managedRootsEnvVar {
				return fixture.path("managed")
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"toggle-tag", fixture.path("managed/work-a/nested/deeper")}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := store.calls, []string{fixture.path("managed/work-a")}; !equalStrings(got, want) {
		t.Fatalf("Toggle() calls = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "untagged: "+fixture.path("managed/work-a")+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestSwitchCommandToggleTagRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	cmd := &switchCommand{
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "too many args", args: []string{"toggle-tag", "/tmp/a", "/tmp/b"}, want: "switch toggle-tag accepts at most 1 [path] argument"},
		{name: "blank arg", args: []string{"toggle-tag", "   "}, want: "switch toggle-tag requires a non-empty [path] argument"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			err := cmd.Run(tt.args, &bytes.Buffer{}, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage text", stderr.String())
			}
		})
	}
}

func TestSwitchCommandTogglePinSnapsExplicitPathToCandidate(t *testing.T) {
	t.Parallel()

	fixture := newSwitchFixture(t)
	fixture.mkdir("home/workspace")
	fixture.mkdir("managed/work-a/nested/deeper")

	store := &stubSwitchPinStore{toggled: false}
	cmd := &switchCommand{
		discover: candidates.Discover,
		pinStore: func() (switchPinStore, error) { return store, nil },
		validate: validateDirectory,
		homeDir:  func() (string, error) { return fixture.path("home"), nil },
		workingDir: func() (string, error) {
			return fixture.path("home"), nil
		},
		lookupEnv: func(name string) string {
			if name == managedRootsEnvVar {
				return fixture.path("managed")
			}
			return ""
		},
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"toggle-pin", fixture.path("managed/work-a/nested/deeper")}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := store.toggleCalls, []string{fixture.path("managed/work-a")}; !equalStrings(got, want) {
		t.Fatalf("Toggle() calls = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "unpinned: "+fixture.path("managed/work-a")+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

type switchRunnerFunc func(options intfzf.Options) (intfzf.Result, error)

func (f switchRunnerFunc) Run(options intfzf.Options) (intfzf.Result, error) {
	return f(options)
}

type capturingSwitchRunner struct {
	last   intfzf.Options
	result intfzf.Result
	err    error
}

func (r *capturingSwitchRunner) Run(options intfzf.Options) (intfzf.Result, error) {
	r.last = options
	return r.result, r.err
}

type stubSwitchIdentityResolver struct {
	name string
	err  error
}

func (r stubSwitchIdentityResolver) SessionIdentityForPath(string) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	return r.name, nil
}

type switchIdentityResolverFunc func(path string) (string, error)

func (f switchIdentityResolverFunc) SessionIdentityForPath(path string) (string, error) {
	return f(path)
}

type capturingSwitchSessionExecutor struct {
	ensureSessionName string
	ensureCWD         string
	openSessionName   string
	killSessionName   string
	exists            map[string]bool
	recentSessions    []string
	ensureErr         error
	openErr           error
	killErr           error
	existsErr         error
	recentErr         error
}

func (e *capturingSwitchSessionExecutor) EnsureSession(_ context.Context, sessionName, cwd string) error {
	e.ensureSessionName = sessionName
	e.ensureCWD = cwd
	return e.ensureErr
}

func (e *capturingSwitchSessionExecutor) OpenSession(_ context.Context, sessionName string) error {
	e.openSessionName = sessionName
	return e.openErr
}

func (e *capturingSwitchSessionExecutor) KillSession(_ context.Context, sessionName string) error {
	e.killSessionName = sessionName
	return e.killErr
}

func (e *capturingSwitchSessionExecutor) SessionExists(_ context.Context, sessionName string) (bool, error) {
	if e.existsErr != nil {
		return false, e.existsErr
	}
	if e.exists == nil {
		return false, nil
	}
	return e.exists[sessionName], nil
}

func (e *capturingSwitchSessionExecutor) RecentSessions(context.Context) ([]string, error) {
	if e.recentErr != nil {
		return nil, e.recentErr
	}
	return e.recentSessions, nil
}

type stubSwitchPinStore struct {
	list        []string
	err         error
	addCalls    []string
	toggleCalls []string
	clearCalls  int
	toggled     bool
}

func (s stubSwitchPinStore) List() ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]string(nil), s.list...), nil
}

func (s *stubSwitchPinStore) Add(path string) error {
	s.addCalls = append(s.addCalls, path)
	if s.err != nil {
		return s.err
	}
	if !containsString(s.list, path) {
		s.list = append(s.list, path)
	}
	return nil
}

func (s *stubSwitchPinStore) Toggle(path string) (bool, error) {
	s.toggleCalls = append(s.toggleCalls, path)
	if s.err != nil {
		return false, s.err
	}
	return s.toggled, nil
}

func (s *stubSwitchPinStore) Clear() error {
	s.clearCalls++
	if s.err != nil {
		return s.err
	}
	s.list = nil
	return nil
}

type capturingSwitchTagStore struct {
	calls  []string
	tagged bool
	list   []string
	err    error
}

func (s *capturingSwitchTagStore) List() ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]string(nil), s.list...), nil
}

func (s *capturingSwitchTagStore) Toggle(name string) (bool, error) {
	s.calls = append(s.calls, name)
	if s.err != nil {
		return false, s.err
	}
	return s.tagged, nil
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func equalEntries(got, want []intfzf.Entry) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].Label != want[i].Label || got[i].Value != want[i].Value {
			return false
		}
		if want[i].SearchKey != "" && got[i].SearchKey != want[i].SearchKey {
			return false
		}
	}
	return true
}

type switchFixtureFS struct {
	root string
	t    *testing.T
}

func newSwitchFixture(t *testing.T) switchFixtureFS {
	t.Helper()

	return switchFixtureFS{
		root: t.TempDir(),
		t:    t,
	}
}

func (f switchFixtureFS) mkdir(rel string) {
	f.t.Helper()

	if err := os.MkdirAll(f.path(rel), 0o755); err != nil {
		f.t.Fatalf("MkdirAll(%q): %v", rel, err)
	}
}

func (f switchFixtureFS) path(rel string) string {
	f.t.Helper()

	return filepath.Join(f.root, filepath.FromSlash(rel))
}
