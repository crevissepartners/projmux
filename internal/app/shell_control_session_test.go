package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// The `projmux shell` control-session lifecycle.
//
// Every case here measures the whole entry, not the converger: the preflight
// argv, whether the pass ran at all, and the foreground attach that follows it.
// The attach argv is asserted in each case because keeping it byte-identical to
// the pre-marker one is what makes the preflight a safe addition -- it is the
// same command, against the same server, started with the same config.

// recordedControlPass is a fake control-session pass that records what `shell`
// handed it.
type recordedControlPass struct {
	calls  [][2]string
	result controlSessionConvergence
	err    error
}

func (p *recordedControlPass) converge(_ context.Context, socketName, sessionName string) (controlSessionConvergence, error) {
	p.calls = append(p.calls, [2]string{socketName, sessionName})
	return p.result, p.err
}

// shellControlFixture builds a `projmux shell` command whose control pass is
// observable and whose tmux calls are recorded.
func shellControlFixture(t *testing.T, home string, pass *recordedControlPass) (*shellCommand, *recordingShellRunner, *scriptedShellTmuxRunner) {
	t.Helper()
	foreground := &recordingShellRunner{}
	tmux := &scriptedShellTmuxRunner{}
	cmd := &shellCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		lookupEnv: func(name string) string {
			if name == "SHELL" {
				return "/bin/zsh"
			}
			return ""
		},
		homeDir:    func() (string, error) { return home, nil },
		writeFile:  os.WriteFile,
		runCommand: foreground.run,
		tmuxRunner: tmux,
		goos:       func() string { return "linux" },
		controlSession: func(runner tmuxRunner, shell string) controlSessionPass {
			if runner == nil {
				t.Fatal("shell built the control pass over a nil tmux runner")
			}
			if shell != "/bin/zsh" {
				t.Fatalf("shell built the control pass with shell %q, want %q", shell, "/bin/zsh")
			}
			return pass
		},
	}
	return cmd, foreground, tmux
}

func TestShellProvisionsAndConvergesTheControlSession(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	pass := &recordedControlPass{}
	cmd, foreground, tmux := shellControlFixture(t, home, pass)
	// A brand-new Home: nothing is running yet, so the probe reports absent.
	tmux.errors = map[string]error{
		shellTmuxCallKey("tmux", "-L", "projmux", "-f", filepath.Join(home, ".config", "projmux", "tmux.conf"), "has-session", "-t", "home"): errors.New("no server running on /tmp/tmux-1000/projmux"),
	}

	var stderr bytes.Buffer
	if err := cmd.Run(nil, &bytes.Buffer{}, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	// The preflight probes and then provisions the session detached, so a
	// brand-new Home exists before any option is written onto it. It is
	// deliberately not `new-session -A -d`: with `-A` and an existing session tmux
	// turns that into an attach that `-d` does not suppress, which would seize the
	// client here and never write the marker.
	wantProbe := []string{"-L", "projmux", "-f", configPath, "has-session", "-t", "home"}
	wantCreate := []string{"-L", "projmux", "-f", configPath, "new-session", "-d", "-s", "home", "-c", home}
	if len(tmux.calls) != 2 {
		t.Fatalf("tmux calls = %#v, want the probe and the provisioning call", tmux.calls)
	}
	if tmux.calls[0].name != "tmux" || !reflect.DeepEqual(tmux.calls[0].args, wantProbe) {
		t.Fatalf("probe = %s %#v, want tmux %#v", tmux.calls[0].name, tmux.calls[0].args, wantProbe)
	}
	if tmux.calls[1].name != "tmux" || !reflect.DeepEqual(tmux.calls[1].args, wantCreate) {
		t.Fatalf("provision = %s %#v, want tmux %#v", tmux.calls[1].name, tmux.calls[1].args, wantCreate)
	}
	for _, call := range tmux.calls {
		for _, arg := range call.args {
			if arg == "-A" {
				t.Fatalf("the preflight used -A: %#v; -A plus an existing session is an attach tmux -d cannot suppress", call.args)
			}
		}
	}
	if want := [][2]string{{"projmux", "home"}}; !reflect.DeepEqual(pass.calls, want) {
		t.Fatalf("control pass calls = %#v, want %#v", pass.calls, want)
	}
	// The foreground attach is byte-identical to the pre-marker contract.
	wantAttach := []string{"-L", "projmux", "-f", configPath, "new-session", "-A", "-s", "home", "-c", home}
	if foreground.name != "tmux" || !reflect.DeepEqual(foreground.args, wantAttach) {
		t.Fatalf("attach = %s %#v, want tmux %#v", foreground.name, foreground.args, wantAttach)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want silence on a successful pass", stderr.String())
	}
}

func TestShellConvergesAnExplicitAppSessionTarget(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	pass := &recordedControlPass{}
	cmd, foreground, _ := shellControlFixture(t, home, pass)

	if err := cmd.Run([]string{"--session", "scratch", "--socket", "pmx-dev"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// An explicit `--session` is still an app-session target: resolveShellTarget
	// leaves ProjectDefault false, and the session's cwd is $HOME.
	if want := [][2]string{{"pmx-dev", "scratch"}}; !reflect.DeepEqual(pass.calls, want) {
		t.Fatalf("control pass calls = %#v, want %#v", pass.calls, want)
	}
	if !slicesHas(foreground.args, "scratch") {
		t.Fatalf("attach = %#v, want the explicit session", foreground.args)
	}
}

func TestShellDoesNotMarkAProjectDefaultSession(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	project := filepath.Join(home, "source", "repos", "projmux")
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	pass := &recordedControlPass{}
	cmd, foreground, tmux := shellControlFixture(t, home, pass)
	cmd.getwd = func() (string, error) { return project, nil }

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// A session whose ownership goes to a Project must never carry the control
	// role: it is that Project's runtime projection.
	if len(pass.calls) != 0 {
		t.Fatalf("control pass ran for a Project-default target: %#v", pass.calls)
	}
	if len(tmux.calls) != 0 {
		t.Fatalf("a Project-default target issued preflight tmux calls: %#v", tmux.calls)
	}
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	wantAttach := []string{"-L", "projmux", "-f", configPath, "new-session", "-A", "-s", "repos-projmux", "-c", project}
	if foreground.name != "tmux" || !reflect.DeepEqual(foreground.args, wantAttach) {
		t.Fatalf("attach = %s %#v, want tmux %#v", foreground.name, foreground.args, wantAttach)
	}
}

func TestShellAttachesEvenWhenTheControlPassFails(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	pass := &recordedControlPass{err: errors.New("injected convergence failure")}
	cmd, foreground, _ := shellControlFixture(t, home, pass)

	var stderr bytes.Buffer
	if err := cmd.Run(nil, &bytes.Buffer{}, &stderr); err != nil {
		t.Fatalf("Run() error = %v, want the entry to survive a failed control pass", err)
	}
	if !strings.Contains(stderr.String(), "warning: converge control session \"home\": injected convergence failure") {
		t.Fatalf("stderr = %q, want the convergence warning", stderr.String())
	}
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	wantAttach := []string{"-L", "projmux", "-f", configPath, "new-session", "-A", "-s", "home", "-c", home}
	if foreground.name != "tmux" || !reflect.DeepEqual(foreground.args, wantAttach) {
		t.Fatalf("attach = %s %#v, want tmux %#v", foreground.name, foreground.args, wantAttach)
	}
}

func TestShellReportsAFailedProvisionWithoutBlockingTheAttach(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	pass := &recordedControlPass{}
	cmd, foreground, tmux := shellControlFixture(t, home, pass)
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	tmux.errors = map[string]error{
		shellTmuxCallKey("tmux", "-L", "projmux", "-f", configPath, "has-session", "-t", "home"):                   errors.New("can't find session: home"),
		shellTmuxCallKey("tmux", "-L", "projmux", "-f", configPath, "new-session", "-d", "-s", "home", "-c", home): errors.New("no space left on device"),
	}

	var stderr bytes.Buffer
	if err := cmd.Run(nil, &bytes.Buffer{}, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// A provisioning failure short-circuits the pass -- there is nothing to mark
	// -- but the attach still runs and reports tmux's own failure to the operator.
	if len(pass.calls) != 0 {
		t.Fatalf("the control pass ran after a failed provision: %#v", pass.calls)
	}
	if !strings.Contains(stderr.String(), "no space left on device") {
		t.Fatalf("stderr = %q, want the provisioning failure", stderr.String())
	}
	wantAttach := []string{"-L", "projmux", "-f", configPath, "new-session", "-A", "-s", "home", "-c", home}
	if !reflect.DeepEqual(foreground.args, wantAttach) {
		t.Fatalf("attach = %#v, want tmux %#v", foreground.args, wantAttach)
	}
}

// TestShellSkipsProvisioningAnAlreadyLiveAppSession is the already-live backfill
// path: the session is there, nothing is created, and the pass still runs.
func TestShellSkipsProvisioningAnAlreadyLiveAppSession(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	pass := &recordedControlPass{}
	cmd, foreground, tmux := shellControlFixture(t, home, pass)

	if err := cmd.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// The scripted runner answers has-session successfully by default, which is
	// the live-session case.
	if len(tmux.calls) != 1 {
		t.Fatalf("tmux calls = %#v, want only the probe for a live session", tmux.calls)
	}
	for _, arg := range tmux.calls[0].args {
		if arg == "new-session" {
			t.Fatalf("the preflight created a session that already existed: %#v", tmux.calls[0].args)
		}
	}
	if want := [][2]string{{"projmux", "home"}}; !reflect.DeepEqual(pass.calls, want) {
		t.Fatalf("control pass calls = %#v, want %#v; an already-live Home must still backfill", pass.calls, want)
	}
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	wantAttach := []string{"-L", "projmux", "-f", configPath, "new-session", "-A", "-s", "home", "-c", home}
	if !reflect.DeepEqual(foreground.args, wantAttach) {
		t.Fatalf("attach = %#v, want tmux %#v", foreground.args, wantAttach)
	}
}

// TestShellProvisionsThroughEveryAbsentServerSignature pins the socket-level
// answers as "absent".
//
// The last row is the one a real run measured: on a socket whose file does not
// exist yet, tmux answers `error connecting to <path> (No such file or
// directory)` rather than `no server running`, and reading that as a failure
// would refuse the very first terminal of a session.
func TestShellProvisionsThroughEveryAbsentServerSignature(t *testing.T) {
	t.Parallel()

	for _, message := range []string{
		"can't find session: home",
		"no server running on /tmp/tmux-1000/projmux",
		"can't find server",
		"failed to connect to server",
		"error connecting to /tmp/tt/tmux-1000/projmux (No such file or directory)",
	} {
		t.Run(message, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			pass := &recordedControlPass{}
			cmd, _, tmux := shellControlFixture(t, home, pass)
			configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
			tmux.errors = map[string]error{
				shellTmuxCallKey("tmux", "-L", "projmux", "-f", configPath, "has-session", "-t", "home"): errors.New(message),
			}

			var stderr bytes.Buffer
			if err := cmd.Run(nil, &bytes.Buffer{}, &stderr); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want the probe answer read as absent", stderr.String())
			}
			if len(tmux.calls) != 2 || !slicesHas(tmux.calls[1].args, "new-session") {
				t.Fatalf("tmux calls = %#v, want the probe followed by a detached create", tmux.calls)
			}
			if want := [][2]string{{"projmux", "home"}}; !reflect.DeepEqual(pass.calls, want) {
				t.Fatalf("control pass calls = %#v, want %#v", pass.calls, want)
			}
		})
	}
}

// TestShellReportsAnUnclassifiedProbeFailure keeps the preflight fail-closed on
// an answer it cannot read as absence.
func TestShellReportsAnUnclassifiedProbeFailure(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	pass := &recordedControlPass{}
	cmd, _, tmux := shellControlFixture(t, home, pass)
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	tmux.errors = map[string]error{
		shellTmuxCallKey("tmux", "-L", "projmux", "-f", configPath, "has-session", "-t", "home"): errors.New("permission denied"),
	}

	var stderr bytes.Buffer
	if err := cmd.Run(nil, &bytes.Buffer{}, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(stderr.String(), "permission denied") {
		t.Fatalf("stderr = %q, want the unclassified probe failure reported", stderr.String())
	}
	if len(pass.calls) != 0 {
		t.Fatalf("the control pass ran after an unreadable probe: %#v", pass.calls)
	}
	for _, call := range tmux.calls {
		if slicesHas(call.args, "new-session") {
			t.Fatalf("the preflight created a session after an unreadable probe: %#v", call.args)
		}
	}
}

// TestShellTreatsADuplicateSessionRaceAsProvisioned pins the one tmux answer that
// means the postcondition already holds.
func TestShellTreatsADuplicateSessionRaceAsProvisioned(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	pass := &recordedControlPass{}
	cmd, _, tmux := shellControlFixture(t, home, pass)
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	tmux.errors = map[string]error{
		shellTmuxCallKey("tmux", "-L", "projmux", "-f", configPath, "has-session", "-t", "home"):                   errors.New("can't find session: home"),
		shellTmuxCallKey("tmux", "-L", "projmux", "-f", configPath, "new-session", "-d", "-s", "home", "-c", home): errors.New("duplicate session: home"),
	}

	var stderr bytes.Buffer
	if err := cmd.Run(nil, &bytes.Buffer{}, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want silence: a duplicate session is the postcondition, not a failure", stderr.String())
	}
	if want := [][2]string{{"projmux", "home"}}; !reflect.DeepEqual(pass.calls, want) {
		t.Fatalf("control pass calls = %#v, want %#v", pass.calls, want)
	}
}

func slicesHas(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}
