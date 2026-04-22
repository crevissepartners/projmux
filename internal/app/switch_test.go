package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/es5h/projmux/internal/config"
	"github.com/es5h/projmux/internal/core/candidates"
	intfzf "github.com/es5h/projmux/internal/ui/fzf"
)

func TestAppRunSwitchDefaultsToPopupAndRendersCandidates(t *testing.T) {
	t.Parallel()

	var gotInputs candidates.Inputs
	var gotRunnerOptions intfzf.Options

	app := &App{
		switcher: &switchCommand{
			discover: func(inputs candidates.Inputs) ([]string, error) {
				gotInputs = inputs
				return []string{"/home/tester", "/home/tester/dotfiles"}, nil
			},
			pinStore: func() (switchPinStore, error) {
				return stubSwitchPinStore{list: []string{"/pins/app"}}, nil
			},
			runner: switchRunnerFunc(func(options intfzf.Options) (string, error) {
				gotRunnerOptions = options
				return "/home/tester/dotfiles", nil
			}),
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

	if got, want := stdout.String(), "/home/tester/dotfiles\n"; got != want {
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
	if got, want := gotRunnerOptions.Candidates, []string{"/home/tester", "/home/tester/dotfiles"}; !equalStrings(got, want) {
		t.Fatalf("runner candidates = %q, want %q", got, want)
	}
}

func TestSwitchCommandSupportsSidebarUI(t *testing.T) {
	t.Parallel()

	var gotRunnerOptions intfzf.Options
	cmd := &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) {
			return []string{"/tmp/app"}, nil
		},
		pinStore: func() (switchPinStore, error) { return stubSwitchPinStore{}, nil },
		runner: switchRunnerFunc(func(options intfzf.Options) (string, error) {
			gotRunnerOptions = options
			return "/tmp/app", nil
		}),
		homeDir:    func() (string, error) { return "/home/tester", nil },
		workingDir: func() (string, error) { return "/tmp", nil },
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"--ui=sidebar"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := stdout.String(), "/tmp/app\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got, want := gotRunnerOptions.UI, switchUISidebar; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
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
	fakeRunner := &capturingSwitchRunner{selection: fixture.path("managed/work-a")}
	cmd.runner = fakeRunner

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := cmd.Run([]string{"--ui=sidebar"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	if got, want := stdout.String(), fixture.path("managed/work-a")+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	wantCandidates := []string{
		fixture.path("home"),
		fixture.path("home/dotfiles"),
		fixture.path("pins/app"),
		fixture.path("managed/work-a"),
		fixture.path("rp/repo-a"),
		fixture.path("managed/work-b"),
	}
	if got := fakeRunner.last.Candidates; !equalStrings(got, wantCandidates) {
		t.Fatalf("runner candidates = %q, want %q", got, wantCandidates)
	}
	if got, want := fakeRunner.last.UI, switchUISidebar; got != want {
		t.Fatalf("runner UI = %q, want %q", got, want)
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
				pinStore:   func() (switchPinStore, error) { return stubSwitchPinStore{}, nil },
				runner:     switchRunnerFunc(func(intfzf.Options) (string, error) { return "", nil }),
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
				pinStore: func() (switchPinStore, error) { return stubSwitchPinStore{}, nil },
				runner:   switchRunnerFunc(func(intfzf.Options) (string, error) { return "", nil }),
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
				pinStore:   func() (switchPinStore, error) { return stubSwitchPinStore{}, nil },
				workingDir: func() (string, error) { return "/tmp", nil },
				runner: switchRunnerFunc(func(intfzf.Options) (string, error) {
					return "", errors.New("fzf exploded")
				}),
			},
			want: "run switch picker",
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
		pinStore:   func() (switchPinStore, error) { return stubSwitchPinStore{}, nil },
		runner:     switchRunnerFunc(func(intfzf.Options) (string, error) { return "", nil }),
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

type switchRunnerFunc func(options intfzf.Options) (string, error)

func (f switchRunnerFunc) Run(options intfzf.Options) (string, error) {
	return f(options)
}

type capturingSwitchRunner struct {
	last      intfzf.Options
	selection string
	err       error
}

func (r *capturingSwitchRunner) Run(options intfzf.Options) (string, error) {
	r.last = options
	return r.selection, r.err
}

type stubSwitchPinStore struct {
	list []string
	err  error
}

func (s stubSwitchPinStore) List() ([]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]string(nil), s.list...), nil
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
