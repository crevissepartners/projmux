package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/es5h/projmux/internal/config"
	"github.com/es5h/projmux/internal/core/candidates"
	corepreview "github.com/es5h/projmux/internal/core/preview"
	intfzf "github.com/es5h/projmux/internal/ui/fzf"
)

func TestAppRunSwitchDefaultsToPopupAndOpensSelectedSession(t *testing.T) {
	t.Parallel()

	var gotInputs candidates.Inputs
	var gotRunnerOptions intfzf.Options
	executor := &capturingSwitchSessionExecutor{}

	app := &App{
		switcher: &switchCommand{
			discover: func(inputs candidates.Inputs) ([]string, error) {
				gotInputs = inputs
				return []string{"/home/tester", "/home/tester/dotfiles"}, nil
			},
			pinStore: func() (switchPinStore, error) {
				return &stubSwitchPinStore{list: []string{"/pins/app"}}, nil
			},
			runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
				gotRunnerOptions = options
				return intfzf.Result{Value: "/home/tester/dotfiles"}, nil
			}),
			sessions:   executor,
			executable: func() (string, error) { return "/tmp/projmux", nil },
			identity:   stubSwitchIdentityResolver{name: "dotfiles"},
			validate:   func(string) error { return nil },
			homeDir:    func() (string, error) { return "/home/tester", nil },
			workingDir: func() (string, error) { return "/rp/repo-a/nested", nil },
			lookupEnv: func(name string) string {
				switch name {
				case repoRootEnvVar:
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
	if got, want := gotRunnerOptions.ExpectKeys, []string{switchTagExpectKey, switchPinExpectKey}; !equalStrings(got, want) {
		t.Fatalf("runner expect keys = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.PreviewCommand, "exec '/tmp/projmux' 'switch' 'preview' {2}"; got != want {
		t.Fatalf("runner preview command = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.PreviewWindow, "down,35%,border-top"; got != want {
		t.Fatalf("runner preview window = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Bindings, []string{
		"left:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-window' {2} 'prev')+refresh-preview",
		"right:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-window' {2} 'next')+refresh-preview",
		"alt-up:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-pane' {2} 'prev')+refresh-preview",
		"alt-down:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-pane' {2} 'next')+refresh-preview",
	}; !equalStrings(got, want) {
		t.Fatalf("runner bindings = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Candidates, []string{"/home/tester", "/home/tester/dotfiles", switchSettingsSentinel}; !equalStrings(got, want) {
		t.Fatalf("runner candidates = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Entries, []intfzf.Entry{
		{Label: "dotfiles  [new]  ~", Value: "/home/tester"},
		{Label: "dotfiles  [new]  ~/dotfiles", Value: "/home/tester/dotfiles"},
		{Label: "Settings", Value: switchSettingsSentinel},
	}; !equalEntries(got, want) {
		t.Fatalf("runner entries = %#v, want %#v", got, want)
	}
	if got, want := executor.ensureSessionName, "dotfiles"; got != want {
		t.Fatalf("ensure session = %q, want %q", got, want)
	}
	if got, want := executor.ensureCWD, "/home/tester/dotfiles"; got != want {
		t.Fatalf("ensure cwd = %q, want %q", got, want)
	}
	if got, want := executor.openSessionName, "dotfiles"; got != want {
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
	if got, want := gotRunnerOptions.PreviewCommand, "exec '/tmp/projmux' 'switch' 'preview' {2}"; got != want {
		t.Fatalf("runner preview command = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.PreviewWindow, "right,60%,border-left"; got != want {
		t.Fatalf("runner preview window = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Bindings, []string{
		"left:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-window' {2} 'prev')+refresh-preview",
		"right:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-window' {2} 'next')+refresh-preview",
		"alt-up:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-pane' {2} 'prev')+refresh-preview",
		"alt-down:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-pane' {2} 'next')+refresh-preview",
	}; !equalStrings(got, want) {
		t.Fatalf("runner bindings = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.Entries, []intfzf.Entry{
		{Label: "tmp-app  [new]  /tmp/app", Value: "/tmp/app"},
		{Label: "Settings", Value: switchSettingsSentinel},
	}; !equalEntries(got, want) {
		t.Fatalf("runner entries = %#v, want %#v", got, want)
	}
}

func TestSwitchCommandMarksExistingSessionsInRows(t *testing.T) {
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
		sessions: &capturingSwitchSessionExecutor{
			exists: map[string]bool{"tmp-app": true},
		},
		identity:   stubSwitchIdentityResolver{name: "tmp-app"},
		validate:   func(string) error { return nil },
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := gotRunnerOptions.Entries, []intfzf.Entry{
		{Label: "tmp-app  [existing]  /tmp/app", Value: "/tmp/app"},
		{Label: "Settings", Value: switchSettingsSentinel},
	}; !equalEntries(got, want) {
		t.Fatalf("runner entries = %#v, want %#v", got, want)
	}
}

func TestNewSwitchCommandUsesEnvAndDefaultPinStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	fixture := newSwitchFixture(t)
	fixture.mkdir("home/dotfiles")
	fixture.mkdir("pins/app")
	fixture.mkdir("rp/repo-a")
	fixture.mkdir("managed/work-a/nested")
	fixture.mkdir("managed/work-b")

	configHome := fixture.path("xdg-config")
	stateHome := fixture.path("xdg-state")
	t.Setenv("HOME", fixture.path("home"))
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv(repoRootEnvVar, fixture.path("rp"))
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
		fixture.path("home/dotfiles"),
		fixture.path("pins/app"),
		fixture.path("managed/work-a"),
		fixture.path("rp/repo-a"),
		fixture.path("managed/work-b"),
		switchSettingsSentinel,
	}
	if got := fakeRunner.last.Candidates; !equalStrings(got, wantCandidates) {
		t.Fatalf("runner candidates = %q, want %q", got, wantCandidates)
	}
	wantEntries := []intfzf.Entry{
		{Label: "home  [new]  ~", Value: fixture.path("home")},
		{Label: "dotfiles  [new]  ~/dotfiles", Value: fixture.path("home/dotfiles")},
		{Label: "pins-app  [new]  " + fixture.path("pins/app"), Value: fixture.path("pins/app")},
		{Label: "managed-work-a  [new]  " + fixture.path("managed/work-a"), Value: fixture.path("managed/work-a")},
		{Label: "rp-repo-a  [new]  ~rp/repo-a", Value: fixture.path("rp/repo-a")},
		{Label: "managed-work-b  [new]  " + fixture.path("managed/work-b"), Value: fixture.path("managed/work-b")},
		{Label: "Settings", Value: switchSettingsSentinel},
	}
	if got := fakeRunner.last.Entries; !equalEntries(got, wantEntries) {
		t.Fatalf("runner entries = %#v, want %#v", got, wantEntries)
	}
	if got, want := fakeRunner.last.PreviewCommand, "exec '/tmp/projmux' 'switch' 'preview' {2}"; got != want {
		t.Fatalf("runner preview command = %q, want %q", got, want)
	}
	if got, want := fakeRunner.last.PreviewWindow, "right,60%,border-left"; got != want {
		t.Fatalf("runner preview window = %q, want %q", got, want)
	}
	if got, want := fakeRunner.last.Bindings, []string{
		"left:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-window' {2} 'prev')+refresh-preview",
		"right:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-window' {2} 'next')+refresh-preview",
		"alt-up:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-pane' {2} 'prev')+refresh-preview",
		"alt-down:execute-silent(exec '/tmp/projmux' 'switch' 'cycle-pane' {2} 'next')+refresh-preview",
	}; !equalStrings(got, want) {
		t.Fatalf("runner bindings = %q, want %q", got, want)
	}
	if got, want := fakeRunner.last.UI, switchUISidebar; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
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
	fixture.mkdir("home/dotfiles")
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
	}; !equalStrings(got, want) {
		t.Fatalf("inputs.ManagedRoots = %q, want %q", got, want)
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
			{WindowIndex: "2", Index: "1", Active: true},
		},
	}
	cmd := &switchCommand{
		discover:     candidates.Discover,
		pinStore:     func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		sessions:     &capturingSwitchSessionExecutor{exists: map[string]bool{"repo-a": true}},
		previewStore: store,
		inventory:    inventory,
		identity:     stubSwitchIdentityResolver{name: "repo-a"},
		validate:     validateDirectory,
		homeDir:      func() (string, error) { return fixture.path("home"), nil },
		workingDir:   func() (string, error) { return fixture.path("home/source/repos/repo-a/subdir"), nil },
		lookupEnv: func(name string) string {
			if name == repoRootEnvVar {
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
		"dir: ~rp/repo-a\n" +
		"session: repo-a\n" +
		"state: existing\n" +
		"selected: window=2 pane=1\n" +
		"windows:\n" +
		"    1\n" +
		"  * 2\n" +
		"panes:\n" +
		"    0\n" +
		"  * 1\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got, want := inventory.sessionWindowsSession, "repo-a"; got != want {
		t.Fatalf("SessionWindows session = %q, want %q", got, want)
	}
	if got, want := inventory.sessionPanesSession, "repo-a"; got != want {
		t.Fatalf("SessionPanes session = %q, want %q", got, want)
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
			if name == repoRootEnvVar {
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
		"dir: ~rp/repo-a\n" +
		"session: repo-a\n" +
		"state: new\n" +
		"selected: none\n" +
		"windows:\n" +
		"  (none)\n" +
		"panes:\n" +
		"  (none)\n"
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
			if name == repoRootEnvVar {
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
		"  alt-p  pin/unpin focused directory\n"
	if got := stdout.String(); got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
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
			if name == repoRootEnvVar {
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
			if name == repoRootEnvVar {
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

func TestSwitchCommandPickerAltTLoopsUntilSelection(t *testing.T) {
	t.Parallel()

	var gotRunnerOptions []intfzf.Options
	store := &capturingSwitchTagStore{tagged: true}
	executor := &capturingSwitchSessionExecutor{}
	call := 0

	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app"}, nil
		},
		pinStore: func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		tagStore: func() (switchTagStore, error) { return store, nil },
		runner: switchRunnerFunc(func(options intfzf.Options) (intfzf.Result, error) {
			gotRunnerOptions = append(gotRunnerOptions, options)
			call++
			if call == 1 {
				return intfzf.Result{Key: switchTagExpectKey, Value: "/tmp/app"}, nil
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
		if got, want := options.ExpectKeys, []string{switchTagExpectKey, switchPinExpectKey}; !equalStrings(got, want) {
			t.Fatalf("runner expect keys call %d = %q, want %q", i, got, want)
		}
		if got, want := options.UI, switchUIPopup; got != want {
			t.Fatalf("runner UI call %d = %q, want %q", i, got, want)
		}
	}
	if got, want := store.calls, []string{"/tmp/app"}; !equalStrings(got, want) {
		t.Fatalf("Toggle() calls = %q, want %q", got, want)
	}
	if got, want := executor.ensureSessionName, "tmp-app"; got != want {
		t.Fatalf("ensure session = %q, want %q", got, want)
	}
	if got, want := executor.openSessionName, "tmp-app"; got != want {
		t.Fatalf("open session = %q, want %q", got, want)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("stdout = %q, want empty for alt-t loop", got)
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
		if got, want := options.ExpectKeys, []string{switchTagExpectKey, switchPinExpectKey}; !equalStrings(got, want) {
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

func TestSwitchCommandSelectingSettingsRunsSettingsMenu(t *testing.T) {
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
				return intfzf.Result{Value: switchSettingsSentinel}, nil
			}
			if runnerCalls == 2 {
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
	if err := cmd.Run(nil, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := runnerCalls, 3; got != want {
		t.Fatalf("runner calls = %d, want %d", got, want)
	}
	if got, want := store.clearCalls, 1; got != want {
		t.Fatalf("clear calls = %d, want %d", got, want)
	}
}

func TestSwitchCommandToggleTagSnapsExplicitPathToCandidate(t *testing.T) {
	t.Parallel()

	fixture := newSwitchFixture(t)
	fixture.mkdir("home/dotfiles")
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
	fixture.mkdir("home/dotfiles")
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

type capturingSwitchSessionExecutor struct {
	ensureSessionName string
	ensureCWD         string
	openSessionName   string
	exists            map[string]bool
	ensureErr         error
	openErr           error
	existsErr         error
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

func (e *capturingSwitchSessionExecutor) SessionExists(_ context.Context, sessionName string) (bool, error) {
	if e.existsErr != nil {
		return false, e.existsErr
	}
	if e.exists == nil {
		return false, nil
	}
	return e.exists[sessionName], nil
}

type stubSwitchPinStore struct {
	list        []string
	err         error
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
	err    error
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
		if got[i] != want[i] {
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
