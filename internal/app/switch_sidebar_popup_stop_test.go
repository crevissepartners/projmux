package app

// The Alt-1 Project sidebar runs inside `tmux display-popup -E`. That child
// exports a valid inherited TMUX receipt but is not a Pane: it inherits no
// TMUX_PANE, and since the sidebar anchor transport became the `--anchor %N`
// operand it receives no private anchor env either
// (internal/app/tmux.go buildPopupToggleWithStyle). Every runtime mutation
// route the sidebar reaches must therefore read that operand.
//
// These cases pin the full popup shape -- valid TMUX, blank TMUX_PANE, blank
// __PROJMUX_RUNTIME_ANCHOR_PANE -- for both stop consumers the sidebar owns:
// the managed Project route in stopManagedProjectSession and the unmanaged
// sibling route in executeUnmanagedRuntimeStop. They are the enforcement of
// C-1 "Sidebar popup runtime mutation route authority".

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/candidates"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

// sidebarPopupStopRunner is exactManagedStopRunner widened to the routes a full
// inherited-TMUX resolution needs: the logical -L reobservation and the anchor
// containment probe. It still refuses every ambient or foreign route, and it
// answers the anchor probe only for the exact Pane the popup handed over.
type sidebarPopupStopRunner struct {
	physical    string
	logical     string
	pid         string
	sessionID   string
	sessionName string
	rootUID     string
	anchorPane  string
	// unmanaged makes list-sessions answer as an unowned sibling runtime, the
	// shape a discovered sidebar row stops through.
	unmanaged  bool
	killTarget string
	killed     bool
	calls      []recordedTmuxCall
}

func (r *sidebarPopupStopRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recordedTmuxCall{name: name, args: slices.Clone(args)})
	if name != "tmux" || len(args) < 3 {
		return nil, fmt.Errorf("unexpected stop command: %s %v", name, args)
	}
	switch {
	case args[0] == "-S" && args[1] == r.physical:
	case args[0] == "-L" && args[1] == r.logical:
	default:
		return nil, fmt.Errorf("managed stop escaped the exact app route: %v", args)
	}
	verb, rest := args[2], args[3:]
	switch verb {
	case "display-message":
		if target := flagValue(rest, "-t"); target != "" {
			if target != r.anchorPane {
				return nil, fmt.Errorf("anchor probe targeted %q, want %q", target, r.anchorPane)
			}
			return []byte(tmuxRowFormat(r.physical, r.pid, r.sessionID, "@1", r.anchorPane) + "\n"), nil
		}
		if rest[len(rest)-1] == "#{pid}" {
			return []byte(r.pid + "\n"), nil
		}
		return []byte(r.physical + "\n"), nil
	case "show-options":
		switch rest[len(rest)-1] {
		case tmuxopts.AppGlobal:
			return []byte("1\n"), nil
		case runtimeMutationSocketNameOption:
			return []byte(r.logical + "\n"), nil
		}
	case "list-sessions":
		if r.killed {
			return nil, nil
		}
		// The unmanaged observation asks for one more column than the managed
		// one; the requested format is what separates the two consumers.
		if strings.Contains(strings.Join(rest, " "), tmuxopts.EphemeralSession) {
			return []byte(tmuxRowFormat(r.sessionID, r.sessionName, "", "", "") + "\n"), nil
		}
		return []byte(tmuxRowFormat(r.sessionID, r.sessionName, r.rootUID, "") + "\n"), nil
	case "kill-session":
		r.killTarget = flagValue(rest, "-t")
		r.killed = true
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected managed stop command: %v", args)
}

func (r *sidebarPopupStopRunner) writes() int {
	writes := 0
	for _, call := range r.calls {
		if len(call.args) > 2 && call.args[2] == "kill-session" {
			writes++
		}
	}
	return writes
}

// sidebarPopupStopFixture builds a sidebar command in the popup shape: a valid
// inherited TMUX receipt and no pane environment variable of any kind.
func sidebarPopupStopFixture(t *testing.T, projectDir, socketPath, serverPID, anchorPane string, recent []string) (*switchCommand, *sidebarPopupStopRunner, *capturingSwitchSessionExecutor) {
	t.Helper()

	inherited := socketPath + "," + serverPID + ",0"
	env := map[string]string{"TMUX": inherited}
	lookup := func(name string) string { return env[name] }

	reader, _, _, _ := navigationFixtureReader(t, "1", inherited)
	stop := &sidebarPopupStopRunner{
		physical: socketPath, logical: "primary", pid: serverPID, anchorPane: anchorPane,
	}
	exists := map[string]bool{}
	for _, sessionName := range recent {
		exists[sessionName] = true
	}
	executor := &capturingSwitchSessionExecutor{exists: exists, recentSessions: recent}
	cmd := &switchCommand{
		discover:      func(candidates.Inputs) ([]string, error) { return []string{projectDir}, nil },
		pinStore:      func() (switchPinStore, error) { return newStubPinStore(), nil },
		sessions:      executor,
		validate:      func(string) error { return nil },
		homeDir:       func() (string, error) { return "/home/tester", nil },
		workingDir:    func() (string, error) { return "/src", nil },
		lookupEnv:     lookup,
		gitBranch:     func(string) string { return "" },
		executable:    func() (string, error) { return "/tmp/projmux", nil },
		rawExecutable: func() (string, error) { return "/tmp/projmux", nil },
		tmuxRunner:    stop,
		navigation: &registryNavigationCommand{
			reader:    reader,
			native:    &scriptedNavigationPicker{},
			homeDir:   func() (string, error) { return t.TempDir(), nil },
			lookupEnv: lookup,
		},
	}
	stopRegistry := &fakeResourceStore{
		registry: runtimeFixtureRegistry(),
		dirs:     map[string]bool{"/src/alpha": true},
		now:      resourceFixtureClock,
	}
	cmd.managedStopStore = stopRegistry.store()
	return cmd, stop, executor
}

// bindSidebarPopupManagedRow points the runner and the identity resolver at the
// one live managed Project row the navigation fixture owns.
func bindSidebarPopupManagedRow(t *testing.T, cmd *switchCommand, stop *sidebarPopupStopRunner, projectDir string) string {
	t.Helper()

	view, err := cmd.navigationView(context.Background())
	if err != nil {
		t.Fatalf("navigationView() error = %v", err)
	}
	var sessionID, sessionName, rootUID string
	for _, row := range projectRowsOf(view) {
		if row.Runtime == nil || strings.TrimSpace(row.UID) == "" {
			continue
		}
		sessionID, rootUID = row.Runtime.ID, row.UID
		sessionName = strings.TrimSpace(row.SessionName)
		if sessionName == "" {
			sessionName = strings.TrimSpace(row.Runtime.Name)
		}
		break
	}
	if sessionID == "" || sessionName == "" || rootUID == "" {
		t.Fatalf("fixture has no live managed Project row: %#v", projectRowsOf(view))
	}
	stop.sessionID, stop.sessionName, stop.rootUID = sessionID, sessionName, rootUID
	cmd.identity = sidebarPopupIdentity(projectDir, sessionName)
	return sessionID
}

// sidebarPopupIdentity answers the row under test with its exact session name
// and every other fixture row with its own basename, so a sidebar refresh never
// fails on an unrelated Project.
func sidebarPopupIdentity(projectDir, sessionName string) switchIdentityResolverFunc {
	return func(path string) (string, error) {
		if strings.TrimSpace(path) == projectDir {
			return sessionName, nil
		}
		return filepath.Base(strings.TrimSpace(path)), nil
	}
}

func sidebarPopupKillAction(t *testing.T, cmd *switchCommand, anchorPane string) intpicker.Action {
	t.Helper()

	for _, action := range cmd.switchSidebarKillActions(anchorPane) {
		if action.Key == switchKillExpectKey {
			return action
		}
	}
	t.Fatalf("sidebar exposes no mutable %q action", switchKillExpectKey)
	return intpicker.Action{}
}

// TestSwitchSidebarKillStopsManagedProjectFromAPopupWithoutTMUXPANE is C-1's
// managed enforcement: the popup's `--anchor %N` operand alone carries the
// route authority to one exact kill-session, and the client hands off to the
// previous active session.
func TestSwitchSidebarKillStopsManagedProjectFromAPopupWithoutTMUXPANE(t *testing.T) {
	t.Parallel()

	const (
		socketPath = "/tmp/fake-tmux/primary"
		serverPID  = "4242"
		anchorPane = "%9"
		projectDir = "/src/alpha"
	)

	cmd, stop, executor := sidebarPopupStopFixture(t, projectDir, socketPath, serverPID, anchorPane, []string{"fallback"})
	sessionID := bindSidebarPopupManagedRow(t, cmd, stop, projectDir)

	_, mutateErr := sidebarPopupKillAction(t, cmd, anchorPane).Mutate(intpicker.ActionContext{
		Key: switchKillExpectKey, Value: projectDir, SelectedIndex: 0,
	})

	if !stop.killed || stop.killTarget != sessionID {
		t.Fatalf("popup Ctrl-X on the managed Project = killed %t target %q, want one exact kill-session -t %s (mutate error: %v)",
			stop.killed, stop.killTarget, sessionID, mutateErr)
	}
	if mutateErr != nil {
		t.Fatalf("popup Ctrl-X mutate error = %v, want nil", mutateErr)
	}
	if got := stop.writes(); got != 1 {
		t.Fatalf("popup Ctrl-X tmux writes = %d, want exactly one: %#v", got, stop.calls)
	}
	if executor.openSessionName != "fallback" {
		t.Fatalf("fallback handoff = %q, want the previous active session", executor.openSessionName)
	}
}

// TestSwitchSidebarKillWithoutFallbackRefreshesWithoutWriting is the second
// half of the fallback contract: with no other live session the popup refreshes
// its rows instead of erroring, and it touches no runtime.
func TestSwitchSidebarKillWithoutFallbackRefreshesWithoutWriting(t *testing.T) {
	t.Parallel()

	const (
		socketPath = "/tmp/fake-tmux/primary"
		serverPID  = "4242"
		anchorPane = "%9"
		projectDir = "/src/alpha"
	)

	cmd, stop, executor := sidebarPopupStopFixture(t, projectDir, socketPath, serverPID, anchorPane, nil)
	bindSidebarPopupManagedRow(t, cmd, stop, projectDir)

	if _, err := sidebarPopupKillAction(t, cmd, anchorPane).Mutate(intpicker.ActionContext{
		Key: switchKillExpectKey, Value: projectDir, SelectedIndex: 0,
	}); err != nil {
		t.Fatalf("popup Ctrl-X without a fallback session = %v, want a plain refresh", err)
	}
	if stop.killed || stop.writes() != 0 {
		t.Fatalf("popup Ctrl-X without a fallback wrote to tmux: %#v", stop.calls)
	}
	if executor.openSessionName != "" {
		t.Fatalf("fallback handoff = %q, want none", executor.openSessionName)
	}
}

// TestSwitchSidebarKillStopsUnmanagedSiblingFromAPopupWithoutTMUXPANE is C-1's
// unmanaged enforcement: a discovered sidebar row owns no Registry UID, so its
// Ctrl-X reaches executeUnmanagedRuntimeStop, which must read the same operand.
func TestSwitchSidebarKillStopsUnmanagedSiblingFromAPopupWithoutTMUXPANE(t *testing.T) {
	t.Parallel()

	const (
		socketPath  = "/tmp/fake-tmux/primary"
		serverPID   = "4242"
		anchorPane  = "%9"
		projectDir  = "/src/discovered"
		sessionName = "discovered"
		sessionID   = "$7"
	)

	cmd, stop, executor := sidebarPopupStopFixture(t, projectDir, socketPath, serverPID, anchorPane, []string{"fallback"})
	stop.unmanaged = true
	stop.sessionID, stop.sessionName = sessionID, sessionName
	cmd.identity = sidebarPopupIdentity(projectDir, sessionName)
	executor.exists[sessionName] = true

	_, mutateErr := sidebarPopupKillAction(t, cmd, anchorPane).Mutate(intpicker.ActionContext{
		Key: switchKillExpectKey, Value: projectDir, SelectedIndex: 0,
	})

	if mutateErr != nil {
		t.Fatalf("popup Ctrl-X on the unmanaged sibling = %v, want nil", mutateErr)
	}
	if !stop.killed || stop.killTarget != sessionID {
		t.Fatalf("unmanaged popup Ctrl-X = killed %t target %q, want one exact kill-session -t %s",
			stop.killed, stop.killTarget, sessionID)
	}
	if got := stop.writes(); got != 1 {
		t.Fatalf("unmanaged popup Ctrl-X tmux writes = %d, want exactly one: %#v", got, stop.calls)
	}
	if executor.openSessionName != "fallback" {
		t.Fatalf("fallback handoff = %q, want the previous active session", executor.openSessionName)
	}
}

// TestSwitchSidebarKillWithoutAnchorOperandRefusesWithZeroWrites keeps the
// missing operand a typed refusal rather than a silent no-op. The blank pane
// environment must never be promoted into anchor authority.
func TestSwitchSidebarKillWithoutAnchorOperandRefusesWithZeroWrites(t *testing.T) {
	t.Parallel()

	const (
		socketPath = "/tmp/fake-tmux/primary"
		serverPID  = "4242"
		projectDir = "/src/alpha"
	)

	for _, tc := range []struct {
		name    string
		managed bool
	}{
		{name: "managed", managed: true},
		{name: "unmanaged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd, stop, executor := sidebarPopupStopFixture(t, projectDir, socketPath, serverPID, "%9", []string{"fallback"})
			if tc.managed {
				bindSidebarPopupManagedRow(t, cmd, stop, projectDir)
			} else {
				stop.unmanaged = true
				stop.sessionID, stop.sessionName = "$7", "discovered"
				cmd.identity = sidebarPopupIdentity(projectDir, "discovered")
				executor.exists["discovered"] = true
			}

			_, err := sidebarPopupKillAction(t, cmd, "").Mutate(intpicker.ActionContext{
				Key: switchKillExpectKey, Value: projectDir, SelectedIndex: 0,
			})

			if err == nil {
				t.Fatalf("popup Ctrl-X without an anchor operand = nil error, want a typed route refusal")
			}
			if !strings.Contains(err.Error(), "runtime mutation route") {
				t.Fatalf("popup Ctrl-X without an anchor operand error = %v, want a runtime mutation route refusal", err)
			}
			if stop.killed || stop.writes() != 0 {
				t.Fatalf("anchorless popup Ctrl-X wrote to tmux: %#v", stop.calls)
			}
			// The managed route is resolved before the fallback handoff, so a
			// refusal leaves the client exactly where it was. The unmanaged
			// path opens its fallback first; that ordering predates the anchor
			// transport and is not part of this contract.
			if tc.managed && executor.openSessionName != "" {
				t.Fatalf("anchorless managed Ctrl-X switched the client to %q before refusing", executor.openSessionName)
			}
		})
	}
}

// TestSwitchSidebarKillRefusesAMalformedAnchorOperand keeps a non-`%N` operand
// out of the route before any observation runs.
func TestSwitchSidebarKillRefusesAMalformedAnchorOperand(t *testing.T) {
	t.Parallel()

	const (
		socketPath = "/tmp/fake-tmux/primary"
		serverPID  = "4242"
		projectDir = "/src/alpha"
	)

	cmd, stop, _ := sidebarPopupStopFixture(t, projectDir, socketPath, serverPID, "%9", []string{"fallback"})
	bindSidebarPopupManagedRow(t, cmd, stop, projectDir)

	_, err := sidebarPopupKillAction(t, cmd, "pane-9").Mutate(intpicker.ActionContext{
		Key: switchKillExpectKey, Value: projectDir, SelectedIndex: 0,
	})

	if err == nil || !strings.Contains(err.Error(), "explicit anchor is not an exact TMUX Pane") {
		t.Fatalf("malformed anchor operand error = %v, want the exact %%N refusal", err)
	}
	if stop.killed || stop.writes() != 0 {
		t.Fatalf("malformed anchor operand wrote to tmux: %#v", stop.calls)
	}
}

// TestSidebarPopupAnchorSupplierAndStopConsumerShareOneTransport is the parity
// half of C-1: whatever transport buildPopupToggleWithStyle emits for
// sessionizer-sidebar must be the transport the stop route reads. The runner
// answers the containment probe for exactly one Pane, so a supplier that stops
// emitting the operand -- or a consumer that stops reading it -- fails here.
func TestSidebarPopupAnchorSupplierAndStopConsumerShareOneTransport(t *testing.T) {
	t.Parallel()

	const (
		socketPath = "/tmp/fake-tmux/primary"
		serverPID  = "4242"
		originPane = "%9"
		projectDir = "/src/alpha"
	)

	command, options, err := buildPopupToggleWithStyle(
		tmuxPopupToggleMode{Raw: "sessionizer-sidebar", Canonical: "sessionizer-sidebar", AnchorPane: originPane},
		"/tmp/projmux", "/tmp/marker",
		tmuxPopupContext{OriginPane: originPane, TargetClient: "/dev/pts/8"},
		func(string) string { return "" }, "",
	)
	if err != nil {
		t.Fatalf("buildPopupToggleWithStyle() error = %v", err)
	}
	if _, ok := options.Env[runtimeMutationAnchorPaneEnv]; ok {
		t.Fatalf("sidebar popup still supplies the private anchor env: %#v", options.Env)
	}

	operand := sidebarPopupAnchorOperand(t, command)
	if operand != originPane {
		t.Fatalf("sidebar popup anchor operand = %q, want %q", operand, originPane)
	}

	// The child process parses that operand into plan.Anchor; feed the exact
	// supplied value into the stop path the sidebar reaches.
	cmd, stop, _ := sidebarPopupStopFixture(t, projectDir, socketPath, serverPID, operand, []string{"fallback"})
	sessionID := bindSidebarPopupManagedRow(t, cmd, stop, projectDir)

	if _, err := sidebarPopupKillAction(t, cmd, operand).Mutate(intpicker.ActionContext{
		Key: switchKillExpectKey, Value: projectDir, SelectedIndex: 0,
	}); err != nil {
		t.Fatalf("supplied anchor operand did not reach the stop route: %v", err)
	}
	if stop.killTarget != sessionID {
		t.Fatalf("supplied anchor operand killed %q, want %q", stop.killTarget, sessionID)
	}
}

var sidebarPopupAnchorOperandPattern = regexp.MustCompile(`'--anchor' '([^']+)'`)

func sidebarPopupAnchorOperand(t *testing.T, command string) string {
	t.Helper()

	match := sidebarPopupAnchorOperandPattern.FindStringSubmatch(command)
	if match == nil {
		t.Fatalf("sidebar popup command carries no --anchor operand: %q", command)
	}
	return match[1]
}

// TestSwitchSidebarPlanCarriesTheAnchorOperandIntoItsKillAction pins the wiring
// between the parsed operand and the action that consumes it. completePlan runs
// the picker, so an anchor attached to the plan after plan() returns is already
// too late: the sidebar's Ctrl-X closure would capture a blank anchor and the
// stop would refuse even though the operand was supplied on the command line.
func TestSwitchSidebarPlanCarriesTheAnchorOperandIntoItsKillAction(t *testing.T) {
	t.Parallel()

	const (
		socketPath = "/tmp/fake-tmux/primary"
		serverPID  = "4242"
		anchorPane = "%9"
		projectDir = "/src/alpha"
	)

	cmd, stop, _ := sidebarPopupStopFixture(t, projectDir, socketPath, serverPID, anchorPane, []string{"fallback"})
	sessionID := bindSidebarPopupManagedRow(t, cmd, stop, projectDir)

	var mutateErr error
	killed := false
	cmd.nativePicker = pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
		for _, action := range options.Actions {
			if action.Key != switchKillExpectKey || action.Mutate == nil {
				continue
			}
			killed = true
			_, mutateErr = action.Mutate(intpicker.ActionContext{
				Key: switchKillExpectKey, Value: projectDir, SelectedIndex: 0,
			})
		}
		return intpicker.Result{}, nil
	})

	plan, err := cmd.plan(switchUISidebar, anchorPane)
	if err != nil {
		t.Fatalf("plan(sidebar, %s) error = %v", anchorPane, err)
	}
	if plan.Anchor != anchorPane {
		t.Fatalf("plan.Anchor = %q, want %q before the picker runs", plan.Anchor, anchorPane)
	}
	if !killed {
		t.Fatalf("the sidebar picker exposed no mutable %q action", switchKillExpectKey)
	}
	if mutateErr != nil {
		t.Fatalf("Ctrl-X through the real plan wiring = %v, want nil", mutateErr)
	}
	if stop.killTarget != sessionID {
		t.Fatalf("Ctrl-X through the real plan wiring killed %q, want %q", stop.killTarget, sessionID)
	}
}
