package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/lifecycle"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
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
	calls      [][2]string
	result     controlSessionConvergence
	err        error
	onConverge func()
	// root answers rootUID. The fixture defaults it to the result's UID once
	// the scripted tmux row is converged, so the Registry root and the tmux
	// mirrors appear together exactly as a real converge leaves them.
	root func() string
}

func (p *recordedControlPass) rootUID(context.Context, string) (string, error) {
	if p.root == nil {
		return "", nil
	}
	return p.root(), nil
}

func (p *recordedControlPass) converge(_ context.Context, socketName, sessionName string) (controlSessionConvergence, error) {
	p.calls = append(p.calls, [2]string{socketName, sessionName})
	if p.err == nil && p.onConverge != nil {
		p.onConverge()
	}
	return p.result, p.err
}

// shellControlFixture builds a `projmux shell` command whose control pass is
// observable and whose tmux calls are recorded.
func shellControlFixture(t *testing.T, home string, pass *recordedControlPass) (*shellCommand, *recordingShellRunner, *scriptedShellTmuxRunner) {
	t.Helper()
	if pass.err == nil && pass.result == (controlSessionConvergence{}) {
		pass.result = controlSessionConvergence{controlUID: "ctl-home", changed: true, writes: 3, windows: 1, panes: 1}
	}
	foreground := &recordingShellRunner{}
	tmux := &scriptedShellTmuxRunner{}
	if pass.onConverge == nil {
		pass.onConverge = func() { tmux.identityConverged = true }
	}
	if pass.root == nil {
		pass.root = func() string {
			if tmux.identityConverged {
				return pass.result.controlUID
			}
			return ""
		}
	}
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
	var createCalls []recordedTmuxCall
	for _, call := range tmux.calls {
		if slicesHas(call.args, "new-session") {
			createCalls = append(createCalls, call)
		}
	}
	probeIndex := slices.IndexFunc(tmux.calls, func(call recordedTmuxCall) bool {
		return call.name == "tmux" && reflect.DeepEqual(call.args, wantProbe)
	})
	if probeIndex < 0 {
		t.Fatalf("session probe %#v is absent from tmux calls %#v", wantProbe, tmux.calls)
	}
	if len(createCalls) != 1 || !slicesHas(createCalls[0].args, "-P") || !slicesHas(createCalls[0].args, "-F") || !slicesHas(createCalls[0].args, "-e") {
		t.Fatalf("provision calls = %#v, want one atomic typed create with handles and lease", createCalls)
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
	wantAttach := []string{"-L", "projmux", "-f", configPath, "attach-session", "-t", "=home", "-c", home}
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
	cmd, foreground, tmux := shellControlFixture(t, home, pass)
	tmux.logicalSocket = "pmx-dev"
	tmux.observedSocket = "/tmp/tmux-1000/pmx-dev"

	if err := cmd.Run([]string{"--session", "scratch", "--socket", "pmx-dev"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// An explicit `--session` is still an app-session target: resolveShellTarget
	// leaves ProjectDefault false, and the session's cwd is $HOME.
	if want := [][2]string{{"pmx-dev", "scratch"}}; !reflect.DeepEqual(pass.calls, want) {
		t.Fatalf("control pass calls = %#v, want %#v", pass.calls, want)
	}
	if !slicesHas(foreground.args, "=scratch") {
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
	cmd.projectSession = func(context.Context, string, shellTarget) error { return nil }

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
	wantAttach := []string{"-L", "projmux", "-f", configPath, "attach-session", "-t", "=repos-projmux", "-c", project}
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
	wantAttach := []string{"-L", "projmux", "-f", configPath, "attach-session", "-t", "=home", "-c", home}
	if foreground.name != "tmux" || !reflect.DeepEqual(foreground.args, wantAttach) {
		t.Fatalf("attach = %s %#v, want tmux %#v", foreground.name, foreground.args, wantAttach)
	}
}

func TestShellFailedProvisionRefusesFailOpenAttachCreation(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	pass := &recordedControlPass{}
	cmd, foreground, tmux := shellControlFixture(t, home, pass)
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	tmux.errors = map[string]error{
		shellTmuxCallKey("tmux", "-L", "projmux", "-f", configPath, "has-session", "-t", "home"):                                  errors.New("can't find session: home"),
		shellTmuxCallKey("tmux", "-S", "/tmp/tmux-1000/projmux", "-f", configPath, "new-session", "-d", "-s", "home", "-c", home): errors.New("no space left on device"),
	}

	var stderr bytes.Buffer
	if err := cmd.Run(nil, &bytes.Buffer{}, &stderr); err == nil || !strings.Contains(err.Error(), "no space left on device") {
		t.Fatalf("Run() error = %v, want fail-closed provisioning refusal; tmux calls=%#v", err, tmux.calls)
	}
	// A provisioning failure short-circuits the pass -- there is nothing to mark
	// -- but the attach still runs and reports tmux's own failure to the operator.
	if len(pass.calls) != 0 {
		t.Fatalf("the control pass ran after a failed provision: %#v", pass.calls)
	}
	if len(foreground.args) != 0 {
		t.Fatalf("failed provisioning reached fail-open foreground -A: %#v", foreground.args)
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
	for _, call := range tmux.calls {
		for _, arg := range call.args {
			if arg == "new-session" {
				t.Fatalf("the preflight created a session that already existed: %#v", call.args)
			}
		}
	}
	if want := [][2]string{{"projmux", "home"}}; !reflect.DeepEqual(pass.calls, want) {
		t.Fatalf("control pass calls = %#v, want %#v; an already-live Home must still backfill", pass.calls, want)
	}
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	wantAttach := []string{"-L", "projmux", "-f", configPath, "attach-session", "-t", "=home", "-c", home}
	if !reflect.DeepEqual(foreground.args, wantAttach) {
		t.Fatalf("attach = %#v, want tmux %#v", foreground.args, wantAttach)
	}
}

// isControlBootstrapWrite reports whether a recorded tmux call is a topology or
// option write rather than an observation.
func isControlBootstrapWrite(call recordedTmuxCall) bool {
	for _, arg := range call.args {
		switch arg {
		case "new-session", "set-option", "set-environment", "kill-session":
			return true
		}
	}
	return false
}

// countControlIdentityObservations installs an onRun hook that numbers every
// ControlSession identity-row observation, in order: the pre-plan baseline,
// Reobserve, Guard, then the post-plan check. Each runs before the scripted
// runner answers that call.
func countControlIdentityObservations(tmux *scriptedShellTmuxRunner, each func(n int, call recordedTmuxCall)) {
	n := 0
	tmux.onRun = func(call recordedTmuxCall) {
		if slicesHas(call.args, "display-message") && strings.Contains(strings.Join(call.args, " "), tmuxopts.SessionRole) {
			n++
			each(n, call)
		}
	}
}

// TestShellReentersAnAlreadyConvergedControlSession is the re-entry path: the
// Home is live and already carries the control role, Window/Pane mirrors and a
// Registry root before the preflight runs, so there is nothing left to
// converge. Re-entering must succeed without a warning, without a single tmux
// write, and without running the converger.
func TestShellReentersAnAlreadyConvergedControlSession(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")

	// Direct preflight: the scripted runner answers has-session as live and the
	// identity row as converged from the start.
	pass := &recordedControlPass{}
	cmd, _, tmux := shellControlFixture(t, home, pass)
	tmux.identityConverged = true
	if err := cmd.prepareControlSession(context.Background(), "projmux", configPath, shellTarget{SessionName: "home", CWD: home}); err != nil {
		t.Errorf("prepareControlSession() error = %v, want an already-converged Home to be a no-op success; pass calls=%#v", err, pass.calls)
	}
	if len(pass.calls) != 0 {
		t.Errorf("control pass calls = %#v, want none against an already-converged Home", pass.calls)
	}
	for _, call := range tmux.calls {
		if isControlBootstrapWrite(call) {
			t.Fatalf("re-entry issued a tmux write %#v against an already-converged Home", call.args)
		}
	}

	// The same state through the whole entry: no fail-open warning either.
	pass = &recordedControlPass{}
	cmd, foreground, tmux := shellControlFixture(t, home, pass)
	tmux.identityConverged = true
	var stderr bytes.Buffer
	if err := cmd.Run(nil, &bytes.Buffer{}, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want silence when re-entering an already-converged Home", stderr.String())
	}
	if len(pass.calls) != 0 {
		t.Errorf("control pass calls = %#v, want none against an already-converged Home", pass.calls)
	}
	for _, call := range tmux.calls {
		if isControlBootstrapWrite(call) {
			t.Fatalf("re-entry issued a tmux write %#v against an already-converged Home", call.args)
		}
	}
	wantAttach := []string{"-L", "projmux", "-f", configPath, "attach-session", "-t", "=home", "-c", home}
	if !reflect.DeepEqual(foreground.args, wantAttach) {
		t.Fatalf("attach = %#v, want tmux %#v", foreground.args, wantAttach)
	}
}

// TestShellRefusesAConvergedLookingHomeWhoseHandlesDrifted keeps containment
// drift fail-closed: after the baseline observation the Home answers with
// converged mirrors and a Registry root, but under a different Window handle.
// That is not the Home the plan was bound to, so it is neither skipped as
// converged nor dropped by the guard.
func TestShellRefusesAConvergedLookingHomeWhoseHandlesDrifted(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	pass := &recordedControlPass{}
	cmd, _, tmux := shellControlFixture(t, home, pass)
	countControlIdentityObservations(tmux, func(n int, call recordedTmuxCall) {
		if n == 2 {
			tmux.identityConverged = true
			if tmux.outputs == nil {
				tmux.outputs = map[string][]byte{}
			}
			tmux.outputs[shellTmuxCallKey(call.name, call.args...)] = []byte(strings.Join([]string{
				"$1", resourcegraph.ControlSessionRole, "@2", "win-home", "%1", "pan-home",
			}, tmuxRowSep) + "\n")
		}
	})

	err := cmd.prepareControlSession(context.Background(), "projmux", configPath, shellTarget{SessionName: "home", CWD: home})
	if err == nil || !strings.Contains(err.Error(), "containment drifted") {
		t.Fatalf("prepareControlSession() error = %v, want containment drift refusal", err)
	}
	if len(pass.calls) != 0 {
		t.Fatalf("control pass calls = %#v, want none after containment drift", pass.calls)
	}
	for _, call := range tmux.calls {
		if isControlBootstrapWrite(call) {
			t.Fatalf("containment drift reached a tmux write %#v", call.args)
		}
	}
}

// TestShellDropsIdentityConvergenceAConcurrentWriterCompletedBeforeTheGuard is
// the race between Reobserve and Guard: the Home is unconverged when the plan
// is built, and another writer converges the same exact Home before the guard
// reads it. The guard drops the step instead of refusing it as drift or
// converging it a second time, and the root UID comes from that observation.
func TestShellDropsIdentityConvergenceAConcurrentWriterCompletedBeforeTheGuard(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	pass := &recordedControlPass{}
	cmd, _, tmux := shellControlFixture(t, home, pass)
	countControlIdentityObservations(tmux, func(n int, _ recordedTmuxCall) {
		if n == 3 {
			tmux.identityConverged = true
		}
	})

	if err := cmd.prepareControlSession(context.Background(), "projmux", configPath, shellTarget{SessionName: "home", CWD: home}); err != nil {
		t.Fatalf("prepareControlSession() error = %v, want a concurrently converged Home to be dropped as converged", err)
	}
	if len(pass.calls) != 0 {
		t.Fatalf("control pass calls = %#v, want the concurrently converged step dropped without Apply", pass.calls)
	}
	for _, call := range tmux.calls {
		if isControlBootstrapWrite(call) {
			t.Fatalf("a dropped step issued a tmux write %#v", call.args)
		}
	}
}

// TestShellConvergesAHomeWhoseMirrorsLackARegistryRoot pins that tmux mirrors
// alone are not convergence: a Home that carries the role and Window/Pane
// mirrors but has no Registry ControlSession root still runs the step, and the
// converger repairs it.
func TestShellConvergesAHomeWhoseMirrorsLackARegistryRoot(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	pass := &recordedControlPass{}
	pass.root = func() string {
		if len(pass.calls) > 0 {
			return "ctl-home"
		}
		return ""
	}
	cmd, _, tmux := shellControlFixture(t, home, pass)
	tmux.identityConverged = true

	if err := cmd.prepareControlSession(context.Background(), "projmux", configPath, shellTarget{SessionName: "home", CWD: home}); err != nil {
		t.Fatalf("prepareControlSession() error = %v", err)
	}
	if want := [][2]string{{"projmux", "home"}}; !reflect.DeepEqual(pass.calls, want) {
		t.Fatalf("control pass calls = %#v, want %#v; mirrors without a Registry root must converge", pass.calls, want)
	}
}

// TestAttachAutoEntersAnAlreadyConvergedHome is the fail-closed caller of the
// same preflight: `attach auto` ensures Home through prepareControlSession and
// turns its error into a hard failure, so re-entering an already-converged
// Home must proceed to the open.
func TestAttachAutoEntersAnAlreadyConvergedHome(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	pass := &recordedControlPass{}
	shell, _, tmux := shellControlFixture(t, home, pass)
	tmux.identityConverged = true
	client := &recordingAttachClient{}
	cmd := &attachCommand{
		sessions: client,
		ensureHomeSession: func(ctx context.Context, sessionName, cwd string) error {
			return shell.prepareControlSession(ctx, "projmux", configPath, shellTarget{SessionName: sessionName, CWD: cwd})
		},
	}

	err := cmd.executeAutoAttachPlan(context.Background(), lifecycle.AutoAttachPlan{EnsureHomeSession: true, AttachTarget: "home"}, "home", home)
	if err != nil {
		t.Fatalf("executeAutoAttachPlan() error = %v, want an already-converged Home to be entered", err)
	}
	if got, want := client.opened, []string{"home"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OpenSession calls = %#v, want %#v", got, want)
	}
	if len(pass.calls) != 0 {
		t.Fatalf("control pass calls = %#v, want none against an already-converged Home", pass.calls)
	}
	for _, call := range tmux.calls {
		if isControlBootstrapWrite(call) {
			t.Fatalf("attach auto re-entry issued a tmux write %#v", call.args)
		}
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
			creates := 0
			for _, call := range tmux.calls {
				if slicesHas(call.args, "new-session") {
					creates++
				}
			}
			if creates != 1 {
				t.Fatalf("tmux calls = %#v, want one detached typed create", tmux.calls)
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
	if err := cmd.Run(nil, &bytes.Buffer{}, &stderr); err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("Run() error = %v, want unreadable observation refusal", err)
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

func TestControlBootstrapRefusesForgedExistingRouteBeforeCreate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name, option, value, want string
	}{
		{name: "app marker", option: tmuxopts.AppGlobal, value: "0\n", want: "not app-owned"},
		{name: "logical marker", option: runtimeMutationSocketNameOption, value: "foreign\n", want: "planned \"projmux\""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			cmd, _, tmux := shellControlFixture(t, home, &recordedControlPass{})
			tmux.outputs = map[string][]byte{
				shellTmuxCallKey("tmux", "-S", "/tmp/tmux-1000/projmux", "show-options", "-gqv", tc.option): []byte(tc.value),
			}
			_, err := cmd.provisionAppSession(context.Background(), "projmux", filepath.Join(home, "tmux.conf"), shellTarget{SessionName: "home", CWD: home})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("provisionAppSession() error = %v, want %q refusal", err, tc.want)
			}
			for _, call := range tmux.calls {
				if slicesHas(call.args, "new-session") || slicesHas(call.args, "kill-session") {
					t.Fatalf("forged route reached topology write: %#v", tmux.calls)
				}
			}
		})
	}
}

func TestControlBootstrapErrorAfterEffectRollsBackOnlyExactLease(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd, _, tmux := shellControlFixture(t, home, &recordedControlPass{})
	tmux.errors = map[string]error{
		shellTmuxCallKey("tmux", "-L", "projmux", "-f", filepath.Join(home, "tmux.conf"), "has-session", "-t", "home"): errors.New("can't find session: home"),
	}
	tmux.createEffectErr = errors.New("hook failed after create")
	tmux.leaseMatches = [][]string{{"$9", "@12", "%15", "1"}}

	_, err := cmd.provisionAppSession(context.Background(), "projmux", filepath.Join(home, "tmux.conf"), shellTarget{SessionName: "home", CWD: home})
	if err == nil || !strings.Contains(err.Error(), "hook failed after create") {
		t.Fatalf("provisionAppSession() error = %v, want original create failure", err)
	}
	var kills [][]string
	for _, call := range tmux.calls {
		if slicesHas(call.args, "kill-session") {
			kills = append(kills, call.args)
		}
	}
	if len(kills) != 1 || !slicesHas(kills[0], "$9") {
		t.Fatalf("owned rollback calls = %#v, want one exact kill-session -t $9", kills)
	}
}

func TestControlBootstrapErrorAfterEffectRefusesPhysicalRouteDrift(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd, _, tmux := shellControlFixture(t, home, &recordedControlPass{})
	tmux.errors = map[string]error{
		shellTmuxCallKey("tmux", "-L", "projmux", "-f", filepath.Join(home, "tmux.conf"), "has-session", "-t", "home"): errors.New("can't find session: home"),
	}
	tmux.socketReads = []scriptedSocketRead{
		{output: []byte("/tmp/tmux-1000/projmux-a\n")},
		{output: []byte("/tmp/tmux-1000/projmux-a\n")},
		{output: []byte("/tmp/tmux-1000/projmux-b\n")},
	}
	tmux.createEffectErr = errors.New("hook failed after create")
	tmux.leaseMatches = [][]string{{"$9", "@12", "%15", "1"}}

	_, err := cmd.provisionAppSession(context.Background(), "projmux", filepath.Join(home, "tmux.conf"), shellTarget{SessionName: "home", CWD: home})
	if err == nil || (!strings.Contains(err.Error(), "physical route is unknown") &&
		!strings.Contains(err.Error(), "logical alias no longer names the exact server")) {
		t.Fatalf("provisionAppSession() error = %v, want physical drift residual", err)
	}
	for _, call := range tmux.calls {
		if slicesHas(call.args, "kill-session") || slicesHas(call.args, "kill-server") {
			t.Fatalf("drifted route reached destructive rollback: %#v", tmux.calls)
		}
	}
}

func TestControlBootstrapNoServerBindsFirstCreatedPhysicalRoute(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd, _, tmux := shellControlFixture(t, home, &recordedControlPass{})
	// A new server has the app marker from its generated config, but its logical
	// route marker is absent until the planned write-route-marker action runs.
	tmux.routeMarkerAbsent = true
	// tmux 3.5a renders display-message's separator as its escaped spelling;
	// bootstrap must compare parsed identity fields, not raw transport bytes.
	tmux.escapedDisplayRow = true
	noServer := errors.New("no server running on /tmp/tmux-1000/projmux")
	tmux.socketReads = []scriptedSocketRead{
		{err: noServer},
		{err: noServer},
		{err: noServer},
		{output: []byte("/tmp/tmux-1000/projmux-created\n")},
		{output: []byte("/tmp/tmux-1000/projmux-created\n")},
		{output: []byte("/tmp/tmux-1000/projmux-created\n")},
	}
	tmux.errors = map[string]error{
		shellTmuxCallKey("tmux", "-L", "projmux", "-f", filepath.Join(home, "tmux.conf"), "has-session", "-t", "home"): errors.New("can't find session: home"),
	}

	receipt, err := cmd.provisionAppSession(context.Background(), "projmux", filepath.Join(home, "tmux.conf"), shellTarget{SessionName: "home", CWD: home})
	if err != nil {
		t.Fatalf("provisionAppSession() error = %v", err)
	}
	if got, want := receipt.route.expectedSocketPath, "/tmp/tmux-1000/projmux-created"; got != want {
		t.Fatalf("bound route = %q, want %q", got, want)
	}
	if tmux.routeMarkerAbsent || tmux.logicalSocket != "projmux" {
		t.Fatalf("logical marker after bootstrap = absent:%v value:%q, want projmux", tmux.routeMarkerAbsent, tmux.logicalSocket)
	}
}

func TestControlBootstrapAbsentDeclarationRefusesServerAppearingBeforeWrite(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd, _, tmux := shellControlFixture(t, home, &recordedControlPass{})
	noServer := errors.New("no server running on /tmp/tmux-1000/projmux")
	tmux.socketReads = []scriptedSocketRead{
		{err: noServer},
		{err: noServer},
		{output: []byte("/tmp/tmux-1000/projmux-appeared\n")},
	}
	tmux.errors = map[string]error{
		shellTmuxCallKey("tmux", "-L", "projmux", "-f", filepath.Join(home, "tmux.conf"), "has-session", "-t", "home"): errors.New("can't find session: home"),
	}

	_, err := cmd.provisionAppSession(context.Background(), "projmux", filepath.Join(home, "tmux.conf"), shellTarget{SessionName: "home", CWD: home})
	if err == nil || !strings.Contains(err.Error(), "absent server appeared after planning") {
		t.Fatalf("provisionAppSession() error = %v, want pre-write appeared-server refusal", err)
	}
	for _, call := range tmux.calls {
		if slicesHas(call.args, "new-session") || slicesHas(call.args, "set-option") || slicesHas(call.args, "set-environment") {
			t.Fatalf("appeared server reached a bootstrap write: %#v", tmux.calls)
		}
	}
}

func TestControlBootstrapNoEffectErrorDoesNotRollback(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd, _, tmux := shellControlFixture(t, home, &recordedControlPass{})
	tmux.errors = map[string]error{
		shellTmuxCallKey("tmux", "-L", "projmux", "-f", filepath.Join(home, "tmux.conf"), "has-session", "-t", "home"): errors.New("can't find session: home"),
	}
	tmux.createEffectErr = errors.New("create refused before effect")

	_, err := cmd.provisionAppSession(context.Background(), "projmux", filepath.Join(home, "tmux.conf"), shellTarget{SessionName: "home", CWD: home})
	if err == nil || !strings.Contains(err.Error(), "create refused before effect") {
		t.Fatalf("provisionAppSession() error = %v, want original no-effect failure", err)
	}
	for _, call := range tmux.calls {
		if slicesHas(call.args, "kill-session") || slicesHas(call.args, "kill-server") {
			t.Fatalf("zero lease matches reached destructive rollback: %#v", tmux.calls)
		}
	}
}

func TestControlBootstrapRollbackFailureReportsOwnedResidual(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd, _, tmux := shellControlFixture(t, home, &recordedControlPass{})
	tmux.errors = map[string]error{
		shellTmuxCallKey("tmux", "-L", "projmux", "-f", filepath.Join(home, "tmux.conf"), "has-session", "-t", "home"): errors.New("can't find session: home"),
		shellTmuxCallKey("tmux", "-S", "/tmp/tmux-1000/projmux", "kill-session", "-t", "$9"):                           errors.New("rollback transport failed"),
	}
	tmux.createEffectErr = errors.New("hook failed after create")
	tmux.leaseMatches = [][]string{{"$9", "@12", "%15", "1"}}

	_, err := cmd.provisionAppSession(context.Background(), "projmux", filepath.Join(home, "tmux.conf"), shellTarget{SessionName: "home", CWD: home})
	if err == nil || !strings.Contains(err.Error(), "owned rollback incomplete") || !strings.Contains(err.Error(), "rollback transport failed") {
		t.Fatalf("provisionAppSession() error = %v, want joined owned residual", err)
	}
	for _, call := range tmux.calls {
		if slicesHas(call.args, "kill-server") || (slicesHas(call.args, "kill-session") && !slicesHas(call.args, "$9")) {
			t.Fatalf("rollback escaped exact leased session: %#v", tmux.calls)
		}
	}
}

func TestControlBootstrapAmbiguousErrorAfterEffectPreservesResidual(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd, _, tmux := shellControlFixture(t, home, &recordedControlPass{})
	tmux.errors = map[string]error{
		shellTmuxCallKey("tmux", "-L", "projmux", "-f", filepath.Join(home, "tmux.conf"), "has-session", "-t", "home"): errors.New("can't find session: home"),
	}
	tmux.createEffectErr = errors.New("hook failed after create")
	tmux.leaseMatches = [][]string{{"$9", "@12", "%15", "1"}, {"$10", "@13", "%16", "1"}}

	_, err := cmd.provisionAppSession(context.Background(), "projmux", filepath.Join(home, "tmux.conf"), shellTarget{SessionName: "home", CWD: home})
	if err == nil || !strings.Contains(err.Error(), "no ambiguous rollback attempted") {
		t.Fatalf("provisionAppSession() error = %v, want explicit ambiguous residual", err)
	}
	for _, call := range tmux.calls {
		if slicesHas(call.args, "kill-session") || slicesHas(call.args, "kill-server") {
			t.Fatalf("ambiguous lease reached destructive rollback: %#v", tmux.calls)
		}
	}
}

func TestControlBootstrapMalformedReceiptUsesLeaseWithoutServerWideRollback(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd, _, tmux := shellControlFixture(t, home, &recordedControlPass{})
	tmux.errors = map[string]error{
		shellTmuxCallKey("tmux", "-L", "projmux", "-f", filepath.Join(home, "tmux.conf"), "has-session", "-t", "home"): errors.New("can't find session: home"),
	}
	tmux.createOutput = []byte("malformed\n")
	tmux.leaseMatches = [][]string{{"$9", "@12", "%15", "0"}}

	_, err := cmd.provisionAppSession(context.Background(), "projmux", filepath.Join(home, "tmux.conf"), shellTarget{SessionName: "home", CWD: home})
	if err == nil || !strings.Contains(err.Error(), "without app ownership") {
		t.Fatalf("provisionAppSession() error = %v, want foreign-marker refusal", err)
	}
	var killSession, killServer int
	for _, call := range tmux.calls {
		if slicesHas(call.args, "kill-session") && slicesHas(call.args, "$9") {
			killSession++
		}
		if slicesHas(call.args, "kill-server") {
			killServer++
		}
	}
	if killSession != 1 || killServer != 0 {
		t.Fatalf("rollback calls = %#v, want exact leased session only", tmux.calls)
	}
}

func TestControlBootstrapLeaseClearRefusesPhysicalRouteDriftBeforeWrite(t *testing.T) {
	t.Parallel()

	_, _, tmux := shellControlFixture(t, t.TempDir(), &recordedControlPass{})
	tmux.operationMarker = "projmux-create-op-test"
	tmux.socketReads = []scriptedSocketRead{{output: []byte("/tmp/tmux-1000/replacement\n")}}
	cmd := &shellCommand{tmuxRunner: tmux}
	receipt := controlBootstrapReceipt{
		created: true, sessionID: "$9", windowID: "@12", paneID: "%15", operationMarker: tmux.operationMarker,
		route: runtimeMutationRoute{
			target: tmuxTransport{Kind: tmuxSocketName, Value: "projmux", Source: tmuxSocketNameSource}, socketName: "projmux",
			expectedSocketPath: "/tmp/tmux-1000/original",
			authority:          &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242", SessionID: "$9", WindowID: "@12", PaneID: "%15"},
		},
	}

	err := cmd.clearControlBootstrapLease(context.Background(), "projmux", receipt)
	if err == nil || !strings.Contains(err.Error(), "socket drifted") {
		t.Fatalf("clearControlBootstrapLease() error = %v, want physical drift refusal", err)
	}
	for _, call := range tmux.calls {
		if slicesHas(call.args, "set-environment") {
			t.Fatalf("drifted route reached lease write: %#v", tmux.calls)
		}
	}
}

func slicesHas(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}

// bootstrapLeaseRaceRunner is one exact app server holding the bootstrap
// session $9 and its operation lease. After `change` fires, a concurrent
// writer has acted: the session is gone (sessionGone) or its lease variable
// now reads lease.
type bootstrapLeaseRaceRunner struct {
	physical    string
	lease       string
	sessionGone bool
	// fireOnList / fireOnEnv trigger change right after that many
	// list-sessions / show-environment reads were served.
	fireOnList, fireOnEnv int
	change                func(*bootstrapLeaseRaceRunner)
	listReads, envReads   int
	calls                 [][]string
}

func (r *bootstrapLeaseRaceRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if handled, out, err := answerTmuxReadSequence(ctx, name, args, r.Run); handled {
		return out, err
	}
	r.calls = append(r.calls, slices.Clone(args))
	if name != "tmux" || len(args) < 3 || !(args[0] == "-S" && args[1] == r.physical || args[0] == "-L" && args[1] == "projmux") {
		return nil, fmt.Errorf("bootstrap lease race runner requires the exact route: %s %v", name, args)
	}
	missing := appTypedCommandFailure{inttmux.CommandFailure{Kind: inttmux.CommandFailureExit, Stderr: "can't find session: $9"}}
	switch args[2] {
	case "display-message":
		if target := flagValue(args[2:], "-t"); target != "" {
			if target != "$9" || r.sessionGone {
				return nil, missing
			}
		}
		if args[len(args)-1] == "#{pid}" {
			return []byte("4242\n"), nil
		}
		return []byte(r.physical + "\n"), nil
	case "show-options":
		switch args[len(args)-1] {
		case tmuxopts.AppGlobal:
			return []byte("1\n"), nil
		case runtimeMutationSocketNameOption:
			return []byte("projmux\n"), nil
		}
	case "list-sessions":
		r.listReads++
		out := "$1\n"
		if !r.sessionGone {
			out += "$9\n"
		}
		if r.listReads == r.fireOnList {
			r.change(r)
		}
		return []byte(out), nil
	case "show-environment":
		if r.sessionGone {
			return nil, missing
		}
		r.envReads++
		out := ""
		if r.lease != "" {
			out = createOperationEnvironment + "=" + r.lease + "\n"
		}
		if r.envReads == r.fireOnEnv {
			r.change(r)
		}
		return []byte(out), nil
	case "kill-session", "set-environment":
		return nil, fmt.Errorf("bootstrap lease race runner refuses write: %v", args)
	}
	return nil, fmt.Errorf("unexpected bootstrap lease race command: %v", args)
}

func (r *bootstrapLeaseRaceRunner) writes() int {
	writes := 0
	for _, call := range r.calls {
		if slices.Contains(call, "kill-session") || slices.Contains(call, "set-environment") {
			writes++
		}
	}
	return writes
}

func bootstrapLeaseRaceReceipt(runner *bootstrapLeaseRaceRunner) controlBootstrapReceipt {
	return controlBootstrapReceipt{
		created: true, sessionID: "$9", windowID: "@12", paneID: "%15", operationMarker: "projmux-create-op-race",
		route: runtimeMutationRoute{
			target: tmuxTransport{Kind: tmuxSocketName, Value: "projmux", Source: tmuxSocketNameSource}, socketName: "projmux",
			expectedSocketPath: runner.physical,
			authority:          &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242", SessionID: "$9", WindowID: "@12", PaneID: "%15"},
		},
	}
}

func TestControlBootstrapRollbackTreatsASessionKilledBetweenReobserveAndGuardAsAbsent(t *testing.T) {
	t.Parallel()
	// List read 1 is the plan's Reobserve; the session dies right after it.
	runner := &bootstrapLeaseRaceRunner{physical: "/tmp/tmux-1000/projmux", lease: "projmux-create-op-race",
		fireOnList: 1, change: func(r *bootstrapLeaseRaceRunner) { r.sessionGone = true }}
	cmd := &shellCommand{tmuxRunner: runner}
	if err := cmd.rollbackControlBootstrap(context.Background(), "projmux", bootstrapLeaseRaceReceipt(runner)); err != nil {
		t.Fatalf("rollbackControlBootstrap() error = %v; want the concurrently removed session treated as absent", err)
	}
	if runner.listReads != 2 || runner.writes() != 0 {
		t.Fatalf("list reads = %d, writes = %d; want Reobserve plus the Guard's absence proof and zero writes: %#v", runner.listReads, runner.writes(), runner.calls)
	}
}

func TestControlBootstrapRollbackStillRefusesALeaseChangedBetweenReobserveAndGuard(t *testing.T) {
	t.Parallel()
	runner := &bootstrapLeaseRaceRunner{physical: "/tmp/tmux-1000/projmux", lease: "projmux-create-op-race",
		fireOnList: 1, change: func(r *bootstrapLeaseRaceRunner) { r.lease = "projmux-create-op-someone-else" }}
	cmd := &shellCommand{tmuxRunner: runner}
	err := cmd.rollbackControlBootstrap(context.Background(), "projmux", bootstrapLeaseRaceReceipt(runner))
	if err == nil || !strings.Contains(err.Error(), `guard refused action "kill-owned" before first write`) ||
		!strings.Contains(err.Error(), "ControlSession bootstrap ownership lease is absent or changed") {
		t.Fatalf("rollbackControlBootstrap() error = %v; want the changed lease refused at the guard", err)
	}
	if runner.writes() != 0 {
		t.Fatalf("changed lease reached a write: %#v", runner.calls)
	}
}

func TestControlBootstrapLeaseClearTreatsALeaseClearedBetweenReobserveAndGuardAsAbsent(t *testing.T) {
	t.Parallel()
	// Env read 1 is the plan's Reobserve (lease still ours); a concurrent
	// writer clears the variable right after it.
	runner := &bootstrapLeaseRaceRunner{physical: "/tmp/tmux-1000/projmux", lease: "projmux-create-op-race",
		fireOnEnv: 1, change: func(r *bootstrapLeaseRaceRunner) { r.lease = "" }}
	cmd := &shellCommand{tmuxRunner: runner}
	if err := cmd.clearControlBootstrapLease(context.Background(), "projmux", bootstrapLeaseRaceReceipt(runner)); err != nil {
		t.Fatalf("clearControlBootstrapLease() error = %v; want the concurrently cleared lease treated as absent", err)
	}
	if runner.writes() != 0 {
		t.Fatalf("already-cleared lease reached a write: %#v", runner.calls)
	}
}

func TestControlBootstrapLeaseClearStillRefusesALeaseReplacedBetweenReobserveAndGuard(t *testing.T) {
	t.Parallel()
	runner := &bootstrapLeaseRaceRunner{physical: "/tmp/tmux-1000/projmux", lease: "projmux-create-op-race",
		fireOnEnv: 1, change: func(r *bootstrapLeaseRaceRunner) { r.lease = "projmux-create-op-someone-else" }}
	cmd := &shellCommand{tmuxRunner: runner}
	err := cmd.clearControlBootstrapLease(context.Background(), "projmux", bootstrapLeaseRaceReceipt(runner))
	if err == nil || !strings.Contains(err.Error(), `guard refused action "clear-lease" before first write`) ||
		!strings.Contains(err.Error(), "ControlSession bootstrap ownership lease is absent or changed") {
		t.Fatalf("clearControlBootstrapLease() error = %v; want another operation's lease refused at the guard", err)
	}
	if runner.writes() != 0 {
		t.Fatalf("replaced lease reached a write: %#v", runner.calls)
	}
}
