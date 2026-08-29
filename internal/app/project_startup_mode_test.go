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
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// startupModeFreshStarter answers the one registration question the picker-off
// startup decision asks, and records which Project lifecycle the mode that was
// finally chosen entered.
//
// The mode itself is never observable at the switch boundary -- it is a local
// value -- so the lifecycle log is what proves which mode was acted on:
// `continue` reaches ContinueProject through prepareProjectContinue, `fresh`
// reaches PruneProjectFreshStart through startProjectFresh.
type startupModeFreshStarter struct {
	registered    bool
	registerErr   error
	registerCalls int
	lifecycle     []string
}

func (s *startupModeFreshStarter) ProjectRegistered(string) (bool, error) {
	s.registerCalls++
	if s.registerErr != nil {
		return false, s.registerErr
	}
	return s.registered, nil
}

func (s *startupModeFreshStarter) PlanProjectFreshStart(string) (projectFreshStartPlan, error) {
	return projectFreshStartPlan{}, nil
}

func (s *startupModeFreshStarter) PruneProjectFreshStart(context.Context, string, projectFreshStartPlan) (projectFreshStartCommit, error) {
	s.lifecycle = append(s.lifecycle, projectStartupKindNew)
	return projectFreshStartCommit{}, nil
}

func (s *startupModeFreshStarter) ContinueProject(_ context.Context, root, sessionName string) (openedProjectBootstrap, error) {
	s.lifecycle = append(s.lifecycle, projectStartupKindTopology)
	return openedProjectBootstrap{project: coremetadata.Project{
		Metadata: coremetadata.ObjectMeta{UID: "proj-existing", Name: sessionName},
		Spec:     coremetadata.ProjectSpec{Root: root},
	}}, nil
}

type startupModePickerState string

const (
	startupModePickerNoFile startupModePickerState = "no-file"
	startupModePickerOn     startupModePickerState = "on"
	startupModePickerOff    startupModePickerState = "off"
)

// startupModeConfigHome builds an isolated config home carrying the requested
// `sidebar-startup-picker` state, so no-file, saved on, and saved off remain
// distinct inputs instead of treating the old missing-file fallback as off.
func startupModeConfigHome(t *testing.T, pickerState startupModePickerState) (string, func(string) string) {
	t.Helper()
	home := t.TempDir()
	configHome := filepath.Join(home, "config")
	stateHome := filepath.Join(home, "state")
	paths, err := config.Homes{HomeDir: home, ConfigHome: configHome, StateHome: stateHome}.Paths()
	if err != nil {
		t.Fatalf("resolve fixture paths: %v", err)
	}
	if pickerState != startupModePickerNoFile {
		if err := os.MkdirAll(paths.ConfigDir, 0o755); err != nil {
			t.Fatalf("create fixture config dir: %v", err)
		}
		if err := os.WriteFile(paths.SidebarStartupPickerFile(), []byte(string(pickerState)+"\n"), 0o644); err != nil {
			t.Fatalf("write fixture startup picker toggle: %v", err)
		}
	}
	return home, func(name string) string {
		switch name {
		case "XDG_CONFIG_HOME":
			return configHome
		case "XDG_STATE_HOME":
			return stateHome
		default:
			return ""
		}
	}
}

// startupModeFixture wires one closed-Project open with every seam the mode
// decision touches: the picker toggle, the registration reader, and the two
// lifecycles a chosen mode can enter.
func startupModeFixture(t *testing.T, pickerState startupModePickerState, registered bool, steps []pickerStep) (*switchCommand, *startupModeFreshStarter, *capturingSwitchSessionExecutor, string) {
	t.Helper()
	home, lookupEnv := startupModeConfigHome(t, pickerState)
	target := t.TempDir()
	starter := &startupModeFreshStarter{registered: registered}
	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	runner, native := scriptedPicker(t, steps)
	cmd := &switchCommand{
		sessions:          executor,
		identity:          stubSwitchIdentityResolver{name: "workspace"},
		homeDir:           func() (string, error) { return home, nil },
		lookupEnv:         lookupEnv,
		runner:            runner,
		nativePicker:      native,
		executable:        func() (string, error) { return "/tmp/projmux", nil },
		projectTopology:   &fakeProjectTopologyMaterializer{materialized: true},
		projectRegistrar:  &fakeProjectRegistrar{uid: "proj-new", name: "workspace", reused: registered},
		projectFreshStart: starter,
		startupNotices:    &recordingProjectStartupReporter{},
	}
	wireFakeProjectSessionPlan(cmd)
	return cmd, starter, executor, target
}

// sidebarEmittedStartupMode drives the sidebar emit point -- the half that runs
// before the re-exec -- and returns the `--mode` token the continuation command
// actually carries. The emitted token is the sidebar's own share of the startup
// decision, so it is asserted separately from the mode the re-exec acts on.
func sidebarEmittedStartupMode(t *testing.T, cmd *switchCommand, target string) string {
	t.Helper()
	runner := &recordingTmuxRunner{}
	cmd.tmuxRunner = runner
	if err := cmd.openProjectTargetPathFromSidebar(context.Background(), switchPlan{
		UI: switchUISidebar, Selection: target, SessionName: "workspace", Anchor: "%12",
	}); err != nil {
		t.Fatalf("openProjectTargetPathFromSidebar() error = %v", err)
	}
	for _, call := range runner.calls {
		if len(call.args) == 3 && call.args[0] == "run-shell" && call.args[1] == "-b" {
			return sidebarContinuationModeToken(t, call.args[2])
		}
	}
	t.Fatalf("the sidebar emitted no continuation: %#v", runner.calls)
	return ""
}

func sidebarContinuationModeToken(t *testing.T, command string) string {
	t.Helper()
	const marker = "'--mode' '"
	_, after, ok := strings.Cut(command, marker)
	if !ok {
		t.Fatalf("continuation command carries no --mode: %q", command)
	}
	rest := after
	end := strings.Index(rest, "'")
	if end < 0 {
		t.Fatalf("continuation command --mode token is unterminated: %q", command)
	}
	return rest[:end]
}

// TestProjectStartupModeSelectionIsOneDecisionAcrossEntryPoints is the mode
// selection table and the parity contract in one.
//
// The no-file/on/off x registered/unregistered x explicit-choice matrix is
// driven through the in-process open, the sidebar emitter, and the sidebar
// continuation after re-exec. Every acted-on entry point must land on the same
// startup mode exactly once. The saved-off, unregistered row preserves the
// earlier repair: the sidebar must not send its old fixed `continue` token
// straight into ContinueProject.
func TestProjectStartupModeSelectionIsOneDecisionAcrossEntryPoints(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		pickerState startupModePickerState
		// registered reports the opened root as an existing Registry Project.
		registered bool
		// choice is the row the operator picks when the picker is on. It is also
		// exactly what the sidebar forwards as `--mode`; with the picker off the
		// sidebar has no choice to carry and always forwards `continue`.
		choice string
		// want is the lifecycle the chosen mode must enter.
		want string
	}{
		{
			name:        "saved off promotes an unregistered root to fresh",
			pickerState: startupModePickerOff,
			want:        projectStartupKindNew,
			choice:      projectStartupKindTopology,
		},
		{
			name:        "saved off keeps a registered root on continue",
			pickerState: startupModePickerOff,
			registered:  true,
			choice:      projectStartupKindTopology,
			want:        projectStartupKindTopology,
		},
		{
			name:        "no file honors an explicit continue on an unregistered root",
			pickerState: startupModePickerNoFile,
			choice:      projectStartupKindTopology,
			want:        projectStartupKindTopology,
		},
		{
			name:        "no file honors an explicit fresh on an unregistered root",
			pickerState: startupModePickerNoFile,
			choice:      projectStartupKindNew,
			want:        projectStartupKindNew,
		},
		{
			name:        "no file honors an explicit continue on a registered root",
			pickerState: startupModePickerNoFile,
			registered:  true,
			choice:      projectStartupKindTopology,
			want:        projectStartupKindTopology,
		},
		{
			name:        "no file honors an explicit fresh on a registered root",
			pickerState: startupModePickerNoFile,
			registered:  true,
			choice:      projectStartupKindNew,
			want:        projectStartupKindNew,
		},
		{
			name:        "saved on honors an explicit continue on an unregistered root",
			pickerState: startupModePickerOn,
			choice:      projectStartupKindTopology,
			want:        projectStartupKindTopology,
		},
		{
			name:        "saved on honors an explicit fresh on an unregistered root",
			pickerState: startupModePickerOn,
			choice:      projectStartupKindNew,
			want:        projectStartupKindNew,
		},
		{
			name:        "saved on honors an explicit continue on a registered root",
			pickerState: startupModePickerOn,
			registered:  true,
			choice:      projectStartupKindTopology,
			want:        projectStartupKindTopology,
		},
		{
			name:        "saved on honors an explicit fresh on a registered root",
			pickerState: startupModePickerOn,
			registered:  true,
			choice:      projectStartupKindNew,
			want:        projectStartupKindNew,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			steps := []pickerStep{{reply: intpickercompat.Result{Key: "enter", Value: test.choice}}}

			inProcess, inProcessStarter, _, inProcessTarget := startupModeFixture(t, test.pickerState, test.registered, steps)
			if err := inProcess.openProjectTarget(context.Background(), inProcessTarget, "workspace"); err != nil {
				t.Fatalf("openProjectTarget() error = %v", err)
			}

			// The sidebar's own share of the decision: which `--mode` does the
			// emitted continuation command carry?
			emit, _, _, emitTarget := startupModeFixture(t, test.pickerState, test.registered, steps)
			if got := sidebarEmittedStartupMode(t, emit, emitTarget); got != test.want {
				t.Fatalf("emitted --mode = %q, want %q", got, test.want)
			}

			sidebar, sidebarStarter, _, sidebarTarget := startupModeFixture(t, test.pickerState, test.registered, steps)
			// What the re-exec acts on. The picker-off arrival is deliberately
			// driven with the neutral `continue` token an older client would still
			// emit, so the second layer is exercised on its own.
			forwarded := projectStartupKindTopology
			if test.pickerState != startupModePickerOff {
				forwarded = test.choice
			}
			if err := sidebar.runSidebarOpen([]string{
				"--path", sidebarTarget, "--session", "workspace", "--mode", forwarded, "--anchor", "%12",
			}, &bytes.Buffer{}); err != nil {
				t.Fatalf("runSidebarOpen() error = %v", err)
			}

			if got, want := inProcessStarter.lifecycle, []string{test.want}; !equalStrings(got, want) {
				t.Fatalf("in-process lifecycle = %q, want %q", got, want)
			}
			if got, want := sidebarStarter.lifecycle, []string{test.want}; !equalStrings(got, want) {
				t.Fatalf("sidebar lifecycle = %q, want %q", got, want)
			}
			if !equalStrings(inProcessStarter.lifecycle, sidebarStarter.lifecycle) {
				t.Fatalf("entry points disagreed: in-process=%q sidebar=%q",
					inProcessStarter.lifecycle, sidebarStarter.lifecycle)
			}
		})
	}
}

// TestSidebarOpenPromotesUnregisteredRootToFreshWhenPickerIsOff is the
// regression guard for the shipped defect.
//
// With an explicit saved `off`, an empty Registry and no snapshot still use the
// automatic decision. Opening an unregistered directory must promote to Fresh
// instead of reaching ContinueProject and failing with "no usable snapshot".
func TestSidebarOpenPromotesUnregisteredRootToFreshWhenPickerIsOff(t *testing.T) {
	t.Parallel()

	emit, _, _, emitTarget := startupModeFixture(t, startupModePickerOff, false, nil)
	if got, want := sidebarEmittedStartupMode(t, emit, emitTarget), projectStartupKindNew; got != want {
		t.Fatalf("emitted --mode = %q, want %q: the sidebar must not launch continue for an unregistered root", got, want)
	}

	cmd, starter, executor, target := startupModeFixture(t, startupModePickerOff, false, nil)
	if err := cmd.runSidebarOpen([]string{
		"--path", target, "--session", "workspace", "--mode", projectStartupKindTopology, "--anchor", "%12",
	}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runSidebarOpen() error = %v", err)
	}
	if got, want := starter.lifecycle, []string{projectStartupKindNew}; !equalStrings(got, want) {
		t.Fatalf("sidebar lifecycle = %q, want %q: an unregistered root must not be sent into continue", got, want)
	}
	if starter.registerCalls != 1 {
		t.Fatalf("registration reads = %d, want exactly one adjudication", starter.registerCalls)
	}
	if got, want := executor.calls, []string{"authorize:" + target, "open:workspace"}; !equalStrings(got, want) {
		t.Fatalf("session calls = %q, want %q", got, want)
	}
}

// TestSidebarOpenKeepsRegisteredRootOnContinue is the other half of the guard:
// promotion is bounded by the registration read, so an existing Project keeps
// its identity and its retained topology.
func TestSidebarOpenKeepsRegisteredRootOnContinue(t *testing.T) {
	t.Parallel()

	emit, _, _, emitTarget := startupModeFixture(t, startupModePickerOff, true, nil)
	if got, want := sidebarEmittedStartupMode(t, emit, emitTarget), projectStartupKindTopology; got != want {
		t.Fatalf("emitted --mode = %q, want %q", got, want)
	}

	cmd, starter, executor, target := startupModeFixture(t, startupModePickerOff, true, nil)
	topology := cmd.projectTopology.(*fakeProjectTopologyMaterializer)
	if err := cmd.runSidebarOpen([]string{
		"--path", target, "--session", "workspace", "--mode", projectStartupKindTopology, "--anchor", "%12",
	}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runSidebarOpen() error = %v", err)
	}
	if got, want := starter.lifecycle, []string{projectStartupKindTopology}; !equalStrings(got, want) {
		t.Fatalf("sidebar lifecycle = %q, want %q", got, want)
	}
	if got, want := topology.calls, []string{"topology:" + target + ":workspace"}; !equalStrings(got, want) {
		t.Fatalf("topology calls = %q, want %q: the retained topology must still be materialized", got, want)
	}
	if got, want := executor.calls, []string{"authorize:" + target, "open:workspace"}; !equalStrings(got, want) {
		t.Fatalf("session calls = %q, want %q", got, want)
	}
}

// TestSidebarOpenHonorsExplicitPickerChoice proves the re-adjudication never
// second-guesses the operator. With the picker on, the arriving `--mode` is a
// choice that was already made on screen, so an unregistered root that the
// operator asked to continue stays on continue.
func TestSidebarOpenHonorsExplicitPickerChoice(t *testing.T) {
	t.Parallel()

	emit, _, _, emitTarget := startupModeFixture(t, startupModePickerOn, false, []pickerStep{
		{reply: intpickercompat.Result{Key: "enter", Value: projectStartupValueTopology}},
	})
	if got, want := sidebarEmittedStartupMode(t, emit, emitTarget), projectStartupKindTopology; got != want {
		t.Fatalf("emitted --mode = %q, want %q: the operator's row is what the sidebar forwards", got, want)
	}

	cmd, starter, _, target := startupModeFixture(t, startupModePickerOn, false, nil)
	if err := cmd.runSidebarOpen([]string{
		"--path", target, "--session", "workspace", "--mode", projectStartupKindTopology, "--anchor", "%12",
	}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runSidebarOpen() error = %v", err)
	}
	if got, want := starter.lifecycle, []string{projectStartupKindTopology}; !equalStrings(got, want) {
		t.Fatalf("sidebar lifecycle = %q, want %q: an explicit choice must not be promoted", got, want)
	}
	if starter.registerCalls != 0 {
		t.Fatalf("registration reads = %d, want none behind an explicit choice", starter.registerCalls)
	}
}

// TestSidebarOpenNeverDemotesAnArrivingFreshMode keeps the re-adjudication
// one-directional. Promotion exists to stop an impossible continue; a `fresh`
// that arrives is a decision that was already made, and re-deciding it would
// silently discard the operator's Project replacement.
func TestSidebarOpenNeverDemotesAnArrivingFreshMode(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		registered bool
	}{
		{name: "unregistered root"},
		{name: "registered root", registered: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			cmd, starter, _, target := startupModeFixture(t, startupModePickerOff, test.registered, nil)
			if err := cmd.runSidebarOpen([]string{
				"--path", target, "--session", "workspace", "--mode", projectStartupKindNew, "--anchor", "%12",
			}, &bytes.Buffer{}); err != nil {
				t.Fatalf("runSidebarOpen() error = %v", err)
			}
			if got, want := starter.lifecycle, []string{projectStartupKindNew}; !equalStrings(got, want) {
				t.Fatalf("sidebar lifecycle = %q, want %q: fresh is never demoted", got, want)
			}
			if starter.registerCalls != 0 {
				t.Fatalf("registration reads = %d, want none for an arriving fresh", starter.registerCalls)
			}
		})
	}
}

// TestSidebarOpenSurfacesRegistrationReadFailure keeps an unreadable Registry
// from being answered with a mode. Guessing `continue` here is exactly the
// failure the re-adjudication exists to remove.
func TestSidebarOpenSurfacesRegistrationReadFailure(t *testing.T) {
	t.Parallel()

	readErr := errors.New("injected registration read failure")
	cmd, starter, executor, target := startupModeFixture(t, startupModePickerOff, false, nil)
	starter.registerErr = readErr
	cmd.tmuxRunner = &recordingTmuxRunner{}

	err := cmd.runSidebarOpen([]string{
		"--path", target, "--session", "workspace", "--mode", projectStartupKindTopology, "--anchor", "%12",
	}, &bytes.Buffer{})
	if !errors.Is(err, readErr) {
		t.Fatalf("runSidebarOpen() error = %v, want the injected registration read failure", err)
	}
	if len(starter.lifecycle) != 0 {
		t.Fatalf("an unreadable Registry still opened a Project: %q", starter.lifecycle)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("an unreadable Registry reached the runtime: %q", executor.calls)
	}
}

// TestSidebarOpenContinueOnUnregisteredRootKeepsTheSnapshotRefusal pins the
// message the promotion exists to avoid. Reaching continue on an unregistered
// root is still possible -- an explicit picker choice does exactly that -- and
// when it happens the operator must still be told which session had no usable
// snapshot and what to choose instead.
func TestSidebarOpenContinueOnUnregisteredRootKeepsTheSnapshotRefusal(t *testing.T) {
	t.Parallel()

	for _, pickerState := range []startupModePickerState{startupModePickerNoFile, startupModePickerOn} {
		t.Run(string(pickerState), func(t *testing.T) {
			t.Parallel()

			home, lookupEnv := startupModeConfigHome(t, pickerState)
			target := t.TempDir()
			executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
			cmd := &switchCommand{
				sessions:   executor,
				identity:   stubSwitchIdentityResolver{name: "workspace"},
				homeDir:    func() (string, error) { return home, nil },
				lookupEnv:  lookupEnv,
				tmuxRunner: &recordingTmuxRunner{},
				executable: func() (string, error) { return "/tmp/projmux", nil },
				projectFreshStart: &registryProjectFreshStarter{
					resources: newFakeResourceStore(t).store(),
					loadSnapshot: func(string) (sessionstate.Snapshot, error) {
						return sessionstate.Snapshot{}, errors.New("no snapshot file")
					},
				},
			}
			wireFakeProjectSessionPlan(cmd)

			err := cmd.runSidebarOpen([]string{
				"--path", target, "--session", "workspace", "--mode", projectStartupKindTopology, "--anchor", "%12",
			}, &bytes.Buffer{})
			want := `continue project unavailable: no usable snapshot for "workspace"; choose Open fresh`
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("runSidebarOpen() error = %v, want a message containing %q", err, want)
			}
		})
	}
}
