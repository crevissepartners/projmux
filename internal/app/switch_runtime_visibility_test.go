package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/candidates"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/i18n"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// The Projects sidebar's Runtime row, and when it is there.
//
// The row is a diagnostics escape hatch on a surface whose job is switching
// Projects, so its presence is a policy: `Always` is the shipped behavior and
// the default `When needed` keeps it for a refused class or an observation that
// could not be taken. Nothing here changes what the row says when it is there,
// what the Registry view emits, or what the direct runtime routes answer.

// runtimeVisibilityView is one bounded view stated directly, so the reducer is
// exercised against every counts x transport combination rather than against
// whichever combination a fake server happens to produce.
func runtimeVisibilityView(observed bool, unavailable []resourcegraph.Unavailability, counts registryview.RuntimeCounts) registryview.View {
	transport := resourcegraph.Transport{Kind: resourcegraph.TransportNone, Source: resourcegraph.TransportSourceNone}
	if observed {
		transport = resourcegraph.Transport{
			Kind:   resourcegraph.TransportSocketName,
			Value:  "primary",
			Source: resourcegraph.TransportSourceSocketName,
		}
	}
	return registryview.View{Transport: transport, Unavailable: unavailable, Runtime: counts}
}

// TestSwitchRuntimeRowVisibilityTable is acceptance (1), (2), (3) and (4) at the
// reducer: every preference x class x observability combination, with no gap.
func TestSwitchRuntimeRowVisibilityTable(t *testing.T) {
	t.Parallel()

	unavailable := []resourcegraph.Unavailability{{Scope: resourcegraph.ScopePanes, Reason: "list-panes failed"}}
	for _, test := range []struct {
		name        string
		observed    bool
		unavailable []resourcegraph.Unavailability
		counts      registryview.RuntimeCounts
		whenNeeded  bool
	}{
		// Nothing to act on: the row is not a Project and does not take a row.
		{name: "empty managed-only host", observed: true},
		{name: "control only", observed: true, counts: registryview.RuntimeCounts{Control: 1}},
		{name: "ephemeral only", observed: true, counts: registryview.RuntimeCounts{Ephemeral: 1}},
		{name: "control and ephemeral", observed: true, counts: registryview.RuntimeCounts{Control: 3, Ephemeral: 2}},

		// One refused class each, then a mixed tally.
		{name: "unattributed", observed: true, counts: registryview.RuntimeCounts{Unattributed: 1}, whenNeeded: true},
		{name: "foreign", observed: true, counts: registryview.RuntimeCounts{Foreign: 1}, whenNeeded: true},
		{name: "recoverable", observed: true, counts: registryview.RuntimeCounts{Recoverable: 1}, whenNeeded: true},
		{name: "conflict", observed: true, counts: registryview.RuntimeCounts{Conflict: 1}, whenNeeded: true},
		{
			name:       "mixed tally",
			observed:   true,
			counts:     registryview.RuntimeCounts{Control: 1, Ephemeral: 1, Unattributed: 2, Foreign: 1, Recoverable: 1, Conflict: 1},
			whenNeeded: true,
		},

		// Not seeing is not the same as nothing being there.
		{name: "no transport", counts: registryview.RuntimeCounts{}, whenNeeded: true},
		{name: "no transport with unavailable scopes", unavailable: unavailable, whenNeeded: true},
		{name: "observed with an unavailable scope", observed: true, unavailable: unavailable, whenNeeded: true},
	} {
		view := runtimeVisibilityView(test.observed, test.unavailable, test.counts)
		if got := switchRuntimeRowVisible(view, config.RuntimeDiagnosticsWhenNeeded); got != test.whenNeeded {
			t.Fatalf("%s: When needed visibility = %v, want %v", test.name, got, test.whenNeeded)
		}
		if !switchRuntimeRowVisible(view, config.RuntimeDiagnosticsAlways) {
			t.Fatalf("%s: Always hid the Runtime row", test.name)
		}
		// The default is the read-time default, not a separate policy.
		if got := switchRuntimeRowVisible(view, config.RuntimeDiagnosticsVisibilityDefault); got != test.whenNeeded {
			t.Fatalf("%s: default visibility = %v, want the When needed answer %v", test.name, got, test.whenNeeded)
		}
	}
}

// TestSwitchRuntimeRowKeepsItsExactLabelWhenVisible is the other half of
// acceptance (2) and (3): visibility is the only thing the preference decides.
// A visible row carries the exact shipped label, tally and selection.
func TestSwitchRuntimeRowKeepsItsExactLabelWhenVisible(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		view      registryview.View
		wantLabel string
	}{
		{
			view:      runtimeVisibilityView(true, nil, registryview.RuntimeCounts{Unattributed: 2}),
			wantLabel: "Runtime - unattributed 2",
		},
		{
			view:      runtimeVisibilityView(true, nil, registryview.RuntimeCounts{Foreign: 1}),
			wantLabel: "Runtime - foreign 1",
		},
		{
			view:      runtimeVisibilityView(true, nil, registryview.RuntimeCounts{Recoverable: 1}),
			wantLabel: "Runtime - recoverable 1",
		},
		{
			view:      runtimeVisibilityView(true, nil, registryview.RuntimeCounts{Conflict: 1}),
			wantLabel: "Runtime - conflict 1",
		},
		{
			view: runtimeVisibilityView(true, nil, registryview.RuntimeCounts{
				Control: 1, Ephemeral: 1, Unattributed: 2, Foreign: 1, Recoverable: 1, Conflict: 1,
			}),
			wantLabel: "Runtime - control 1, ephemeral 1, unattributed 2, foreign 1, recoverable 1, conflict 1",
		},
		{
			view:      runtimeVisibilityView(false, nil, registryview.RuntimeCounts{}),
			wantLabel: "Runtime - no tmux transport",
		},
		{
			view:      runtimeVisibilityView(true, nil, registryview.RuntimeCounts{Control: 1}),
			wantLabel: "Runtime - control 1",
		},
	} {
		if !switchRuntimeRowVisible(test.view, config.RuntimeDiagnosticsAlways) {
			t.Fatalf("Always hid %q", test.wantLabel)
		}
		row := switchRuntimeRow(test.view, switchUISidebar)
		if row.DisplayPath != test.wantLabel {
			t.Fatalf("runtime row label = %q, want the exact shipped label %q", row.DisplayPath, test.wantLabel)
		}
		if row.Path != switchRuntimeSentinel || row.DisplayName != "Runtime" {
			t.Fatalf("runtime row selection = %+v, want the shipped sentinel and name", row)
		}
	}
}

// runtimeVisibilityHealthyServer is one exact host that mirrors precisely what
// the Registry desires: the Project session, its Window and its shell Pane, with
// nothing else running. Every class is zero, so this is the state acceptance (1)
// is about.
func runtimeVisibilityHealthyServer() *fakeTmux {
	server := newFakeTmux()
	server.appMarker = "1"
	server.socketPath = "/tmp/fake-tmux/primary"

	alpha := server.addSession("alpha")
	alpha.opts[tmuxopts.ProjectUIDSession] = runtimeFixtureProject
	alpha.opts[tmuxopts.ProjectNameSession] = "alpha"
	alpha.windows[0].name = "editor"
	alpha.windows[0].opts[tmuxopts.WindowUID] = runtimeFixtureWindow
	alpha.windows[0].panes[0].opts[tmuxopts.PaneUID] = runtimeFixturePane
	return server
}

// runtimeVisibilityConflictServer adds the one refused object acceptance (8)
// drives in the real client: a window mirroring a uid this Registry does not
// contain.
func runtimeVisibilityConflictServer() *fakeTmux {
	server := runtimeVisibilityHealthyServer()
	ghost := &fakeTmuxWindow{id: server.mint("@"), name: "ghost", opts: map[string]string{
		tmuxopts.WindowUID: "win-not-in-registry",
	}}
	ghost.panes = append(ghost.panes, newFakeTmuxPane(server.mint("%")))
	server.sessions[0].windows = append(server.sessions[0].windows, ghost)
	return server
}

// runtimeVisibilityControlServer is the healthy host plus the app's own control
// session: something is running that projmux does not manage as a Project, and
// none of it is anything to act on.
func runtimeVisibilityControlServer() *fakeTmux {
	server := runtimeVisibilityHealthyServer()
	home := server.addSession("Home")
	home.opts[tmuxopts.SessionRole] = resourcegraph.ControlSessionRole
	scratch := server.addSession("scratch")
	scratch.opts[tmuxopts.EphemeralSession] = resourcegraph.EphemeralMarker
	return server
}

// runtimeVisibilityFixture wires a Projects picker over one exact host and one
// isolated config home, so the saved choice is the only preference in play and
// the operator's real config can never reach the assertion.
func runtimeVisibilityFixture(t *testing.T, server *fakeTmux, inherited string, saved string) (*switchCommand, *oneShotSwitchRunner, string) {
	t.Helper()

	home := t.TempDir()
	if saved != "" {
		dir := filepath.Join(home, ".config", "projmux")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create config dir: %v", err)
		}
		path := filepath.Join(dir, config.RuntimeDiagnosticsVisibilityFileName)
		if err := os.WriteFile(path, []byte(saved+"\n"), 0o644); err != nil {
			t.Fatalf("write saved visibility: %v", err)
		}
	}

	registry := runtimeFixtureRegistry()
	sibling := newFakeTmux()
	sibling.appMarker = "1"
	sibling.socketPath = "/tmp/fake-tmux/sibling"
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{
		"-L\x00primary":                server,
		"-S\x00/tmp/fake-tmux/primary": server,
		"-S\x00/tmp/fake-tmux/sibling": sibling,
	}}
	lookupEnv := func(name string) string { return map[string]string{"TMUX": inherited}[name] }
	reader := &registryNavigationReader{reader: &runtimeDiagnosticsReader{
		runner:       runner,
		lookupEnv:    lookupEnv,
		loadRegistry: func() (coremetadata.Registry, error) { return registry, nil },
		observe: func(ctx context.Context, transport resourcegraph.Transport) resourcegraph.Inventory {
			return intmetadata.NewInventoryObserver(runner, transport).Observe(ctx)
		},
	}}

	command, pickerRunner := switchRegistryFixture(t, inherited, []string{"/src/alpha"}, "")
	command.homeDir = func() (string, error) { return home, nil }
	command.lookupEnv = lookupEnv
	command.navigation.reader = reader
	command.navigation.homeDir = func() (string, error) { return home, nil }
	command.navigation.lookupEnv = lookupEnv
	command.discover = func(candidates.Inputs) ([]string, error) {
		return []string{"/src/alpha", switchSettingsSentinel}, nil
	}
	return command, pickerRunner, home
}

func runtimeVisibilitySidebarValues(t *testing.T, command *switchCommand, runner *oneShotSwitchRunner) []string {
	t.Helper()
	if err := command.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	return switchEntryValues(runner.last.Entries)
}

// TestSwitchSidebarHidesRuntimeOnAHealthyHostByDefault is acceptance (1) at the
// rendered surface: with nothing saved, a host that is exactly what the Registry
// desires lists the Project and Settings and no diagnostics row.
func TestSwitchSidebarHidesRuntimeOnAHealthyHostByDefault(t *testing.T) {
	t.Parallel()

	command, runner, _ := runtimeVisibilityFixture(t, runtimeVisibilityHealthyServer(), "/tmp/fake-tmux/primary,0,0", "")

	// The fixture is only meaningful if the view really carries no refused
	// class: a hidden row over an anomalous host would prove nothing.
	view, err := command.navigationView(context.Background())
	if err != nil {
		t.Fatalf("navigation view: %v", err)
	}
	counts := view.Runtime
	if counts != (registryview.RuntimeCounts{}) {
		t.Fatalf("fixture is not healthy: %+v", counts)
	}
	if len(view.Unavailable) != 0 || !view.Observed() {
		t.Fatalf("fixture observation is not complete: observed=%v unavailable=%#v", view.Observed(), view.Unavailable)
	}

	want := []string{"/src/alpha", switchSettingsSentinel}
	if got := runtimeVisibilitySidebarValues(t, command, runner); !reflect.DeepEqual(got, want) {
		t.Fatalf("default sidebar rows = %#v, want the Runtime row withheld %#v", got, want)
	}
}

// TestSwitchRuntimeRowFollowsTheClosedClassTaxonomy is the reevaluation guard on
// the one thing the predicate cannot decide for itself.
//
// `Control` and `Ephemeral` are deliberately outside the needed sum, but the
// taxonomy attributes a *session* that way and leaves its unmirrored Window and
// Pane as `Unattributed` -- objects inside an app-owned host with no mirrored
// identity. So an un-mirrored control or scratch session does bring the row
// back, through its descendants rather than through its own class. A real
// app-owned host mirrors its control session, which is why the default is quiet
// there; if that ever stops being true, or if descendants start inheriting the
// session's class, this assertion is where it surfaces instead of Alt-1 quietly
// changing.
func TestSwitchRuntimeRowFollowsTheClosedClassTaxonomy(t *testing.T) {
	t.Parallel()

	command, runner, _ := runtimeVisibilityFixture(t, runtimeVisibilityControlServer(), "/tmp/fake-tmux/primary,0,0", "")
	view, err := command.navigationView(context.Background())
	if err != nil {
		t.Fatalf("navigation view: %v", err)
	}
	if got, want := view.Runtime, (registryview.RuntimeCounts{Control: 1, Ephemeral: 1, Unattributed: 4}); got != want {
		t.Fatalf("un-mirrored control/scratch tally = %+v, want %+v", got, want)
	}
	// The session classes alone are not the signal: strip the descendants and
	// the same host is quiet.
	sessionsOnly := runtimeVisibilityView(true, nil, registryview.RuntimeCounts{Control: 1, Ephemeral: 1})
	if switchRuntimeRowVisible(sessionsOnly, config.RuntimeDiagnosticsWhenNeeded) {
		t.Fatal("a control/ephemeral-only tally brought the Runtime row back")
	}
	want := []string{"/src/alpha", switchRuntimeSentinel, switchSettingsSentinel}
	if got := runtimeVisibilitySidebarValues(t, command, runner); !reflect.DeepEqual(got, want) {
		t.Fatalf("un-mirrored control/scratch sidebar rows = %#v, want the Runtime row offered %#v", got, want)
	}
}

// TestSwitchSidebarShowsRuntimeWhenNeeded is acceptance (2) and (3) at the
// rendered surface: a refused class brings the row back, and so does an
// observation that could not be taken at all.
func TestSwitchSidebarShowsRuntimeWhenNeeded(t *testing.T) {
	t.Parallel()

	conflict, conflictRunner, _ := runtimeVisibilityFixture(t, runtimeVisibilityConflictServer(), "/tmp/fake-tmux/primary,0,0", "")
	want := []string{"/src/alpha", switchRuntimeSentinel, switchSettingsSentinel}
	if got := runtimeVisibilitySidebarValues(t, conflict, conflictRunner); !reflect.DeepEqual(got, want) {
		t.Fatalf("anomaly sidebar rows = %#v, want the Runtime row offered %#v", got, want)
	}
	labels := strings.Join(entryLabels(conflictRunner.last.Entries), "\n")
	if !strings.Contains(labels, "Runtime - ") {
		t.Fatalf("anomaly Runtime row does not carry its tally:\n%s", labels)
	}

	dark, darkRunner, _ := runtimeVisibilityFixture(t, runtimeVisibilityHealthyServer(), "", "")
	if got := runtimeVisibilitySidebarValues(t, dark, darkRunner); !reflect.DeepEqual(got, want) {
		t.Fatalf("no-transport sidebar rows = %#v, want the Runtime row offered %#v", got, want)
	}
	if labels := strings.Join(entryLabels(darkRunner.last.Entries), "\n"); !strings.Contains(labels, "Runtime - no tmux transport") {
		t.Fatalf("no-transport Runtime row does not say why it is empty:\n%s", labels)
	}
}

// TestSwitchSidebarAlwaysKeepsRuntimeLastBeforeSettings is acceptance (4): the
// operator who chose `Always` keeps the shipped behavior, in the shipped
// position, on a host with nothing to act on.
func TestSwitchSidebarAlwaysKeepsRuntimeLastBeforeSettings(t *testing.T) {
	t.Parallel()

	command, runner, _ := runtimeVisibilityFixture(t, runtimeVisibilityHealthyServer(), "/tmp/fake-tmux/primary,0,0",
		string(config.RuntimeDiagnosticsAlways))
	got := runtimeVisibilitySidebarValues(t, command, runner)
	want := []string{"/src/alpha", switchRuntimeSentinel, switchSettingsSentinel}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Always sidebar rows = %#v, want Runtime last before Settings %#v", got, want)
	}
	if labels := strings.Join(entryLabels(runner.last.Entries), "\n"); !strings.Contains(labels, "Runtime - nothing here that projmux does not manage") {
		t.Fatalf("Always Runtime row lost its shipped empty-state label:\n%s", labels)
	}
}

// TestSwitchSidebarInvalidSavedVisibilityFailsSafeWithoutWriting is acceptance
// (5) at the sidebar: a saved value the choice does not name behaves as the
// default and changes nothing on disk.
func TestSwitchSidebarInvalidSavedVisibilityFailsSafeWithoutWriting(t *testing.T) {
	t.Parallel()

	command, runner, home := runtimeVisibilityFixture(t, runtimeVisibilityHealthyServer(), "/tmp/fake-tmux/primary,0,0", "sometimes")
	want := []string{"/src/alpha", switchSettingsSentinel}
	if got := runtimeVisibilitySidebarValues(t, command, runner); !reflect.DeepEqual(got, want) {
		t.Fatalf("invalid-preference sidebar rows = %#v, want the default behavior %#v", got, want)
	}
	path := filepath.Join(home, ".config", "projmux", config.RuntimeDiagnosticsVisibilityFileName)
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("re-read saved visibility: %v", err)
	}
	if string(content) != "sometimes\n" {
		t.Fatalf("rendering rewrote the saved value to %q, want it untouched", string(content))
	}
}

// TestRuntimeDiagnosticsVisibilitySettingsResolution is the settings half of
// acceptance (5): the effective value and the source annotation an operator
// reads, for all three origins.
func TestRuntimeDiagnosticsVisibilitySettingsResolution(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		saved      string
		wantMode   config.RuntimeDiagnosticsVisibility
		wantSource string
	}{
		{name: "unset", wantMode: config.RuntimeDiagnosticsWhenNeeded, wantSource: "default"},
		{name: "saved when-needed", saved: "when-needed", wantMode: config.RuntimeDiagnosticsWhenNeeded, wantSource: "saved"},
		{name: "saved always", saved: "always", wantMode: config.RuntimeDiagnosticsAlways, wantSource: "saved"},
		{
			name:       "invalid",
			saved:      "sometimes",
			wantMode:   config.RuntimeDiagnosticsWhenNeeded,
			wantSource: runtimeDiagnosticsVisibilitySourceInvalid,
		},
	} {
		home := t.TempDir()
		if test.saved != "" {
			dir := filepath.Join(home, ".config", "projmux")
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatalf("%s: create config dir: %v", test.name, err)
			}
			if err := os.WriteFile(filepath.Join(dir, config.RuntimeDiagnosticsVisibilityFileName), []byte(test.saved+"\n"), 0o644); err != nil {
				t.Fatalf("%s: write saved visibility: %v", test.name, err)
			}
		}
		state := currentRuntimeDiagnosticsVisibility(func() (string, error) { return home, nil }, func(string) string { return "" })
		if state.Mode != test.wantMode {
			t.Fatalf("%s: mode = %q, want %q", test.name, state.Mode, test.wantMode)
		}
		if state.Source() != test.wantSource {
			t.Fatalf("%s: source = %q, want %q", test.name, state.Source(), test.wantSource)
		}
	}
}

// TestRuntimeDiagnosticsVisibilitySettingsRoundTripAppliesOnTheNextRender is the
// integration half: saving through the Settings action changes what the next
// picker invocation renders, and nothing before it.
func TestRuntimeDiagnosticsVisibilitySettingsRoundTripAppliesOnTheNextRender(t *testing.T) {
	t.Parallel()

	command, runner, home := runtimeVisibilityFixture(t, runtimeVisibilityHealthyServer(), "/tmp/fake-tmux/primary,0,0", "")
	if got, want := runtimeVisibilitySidebarValues(t, command, runner), []string{"/src/alpha", switchSettingsSentinel}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first render = %#v, want the default %#v", got, want)
	}

	settings := &settingsCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
	}
	if err := settings.execute(settingsActionPrefixRuntimeDiagnostics+string(config.RuntimeDiagnosticsAlways), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("save Always: %v", err)
	}
	if got, want := runtimeVisibilitySidebarValues(t, command, runner), []string{"/src/alpha", switchRuntimeSentinel, switchSettingsSentinel}; !reflect.DeepEqual(got, want) {
		t.Fatalf("render after saving Always = %#v, want the Runtime row %#v", got, want)
	}

	if err := settings.execute(settingsActionPrefixRuntimeDiagnostics+string(config.RuntimeDiagnosticsWhenNeeded), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("save When needed: %v", err)
	}
	if got, want := runtimeVisibilitySidebarValues(t, command, runner), []string{"/src/alpha", switchSettingsSentinel}; !reflect.DeepEqual(got, want) {
		t.Fatalf("render after saving When needed = %#v, want the row withheld again %#v", got, want)
	}
	if err := settings.execute(settingsActionPrefixRuntimeDiagnostics+"sometimes", &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("the Settings action accepted a value the choice does not name")
	}
}

// TestRuntimeDiagnosticsVisibilitySettingsRowsAreLocalized is the locale parity
// half: the row, both choices, the state row and its source annotation all
// resolve through the catalog in ko-KR as well as en-US.
func TestRuntimeDiagnosticsVisibilitySettingsRowsAreLocalized(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := filepath.Join(home, ".config", "projmux")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, config.RuntimeDiagnosticsVisibilityFileName), []byte("sometimes\n"), 0o644); err != nil {
		t.Fatalf("write saved visibility: %v", err)
	}

	for _, test := range []struct {
		locale i18n.Locale
		want   []string
	}{
		{locale: i18n.FallbackLocale, want: []string{
			"Runtime diagnostics", "When needed", "Always",
			"invalid saved value; using default",
			"show the Runtime row only for a refused class or an observation that could not be taken",
			"show the Runtime row on every render, before Settings",
		}},
		{locale: i18n.Locale("ko-KR"), want: []string{
			"런타임 진단", "필요할 때만", "항상",
			"저장값이 유효하지 않아 기본값 사용",
			"거부된 분류가 있거나 관측을 할 수 없을 때만 Runtime 행을 보여준다",
			"설정 행 바로 앞에 Runtime 행을 항상 보여준다",
		}},
	} {
		command := &settingsCommand{
			homeDir:   func() (string, error) { return home, nil },
			lookupEnv: func(name string) string { return map[string]string{"PROJMUX_LOCALE": string(test.locale)}[name] },
		}
		rendered := strings.Join(entryLabels(command.runtimeDiagnosticsVisibilityEntries(
			currentRuntimeDiagnosticsVisibility(command.homeDir, command.lookupEnv))), "\n")
		for _, want := range test.want {
			if !strings.Contains(rendered, want) {
				t.Fatalf("%s Runtime diagnostics rows do not name %q:\n%s", test.locale, want, rendered)
			}
		}
		values := make([]string, 0, 4)
		for _, entry := range command.runtimeDiagnosticsVisibilityEntries(
			currentRuntimeDiagnosticsVisibility(command.homeDir, command.lookupEnv)) {
			values = append(values, entry.Value)
		}
		want := []string{
			settingsBackValue,
			settingsNoopValue,
			settingsActionPrefixRuntimeDiagnostics + string(config.RuntimeDiagnosticsWhenNeeded),
			settingsActionPrefixRuntimeDiagnostics + string(config.RuntimeDiagnosticsAlways),
		}
		if !reflect.DeepEqual(values, want) {
			t.Fatalf("%s Runtime diagnostics values = %#v, want %#v", test.locale, values, want)
		}
	}
}

// TestProjectSidebarOffersTheRuntimeDiagnosticsChoice pins where the control
// lives: inside the exact `Project Sidebar` view the IA already has, and not in
// a new global Advanced or Troubleshooting container.
func TestProjectSidebarOffersTheRuntimeDiagnosticsChoice(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	command := &settingsCommand{
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
	}
	values := make([]string, 0, 3)
	for _, entry := range command.projectSidebarEntries() {
		values = append(values, entry.Value)
	}
	want := []string{settingsBackValue, settingsSessionStateSidebarStartupPickerDetail, settingsRuntimeDiagnosticsVisibilityDetail}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("Project Sidebar rows = %#v, want the Runtime diagnostics choice beside the startup choice %#v", values, want)
	}
	rendered := strings.Join(entryLabels(command.projectSidebarEntries()), "\n")
	if !strings.Contains(rendered, "When needed") || !strings.Contains(rendered, "default") {
		t.Fatalf("Project Sidebar row does not carry the effective value and its source:\n%s", rendered)
	}
	for _, forbidden := range []string{"Advanced", "Troubleshooting", "Expert"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("Project Sidebar row introduced the %q container:\n%s", forbidden, rendered)
		}
	}
}

// TestRuntimeDiagnosticsVisibilityDoesNotReachTheDirectRuntimeRoutes is
// acceptance (6): `runtime diagnostics` and `get runtime` answer identically in
// both modes, and neither the Registry nor the server changes.
func TestRuntimeDiagnosticsVisibilityDoesNotReachTheDirectRuntimeRoutes(t *testing.T) {
	t.Parallel()

	outputs := map[config.RuntimeDiagnosticsVisibility]string{}
	states := map[config.RuntimeDiagnosticsVisibility]string{}
	registries := map[config.RuntimeDiagnosticsVisibility]string{}

	for _, mode := range []config.RuntimeDiagnosticsVisibility{config.RuntimeDiagnosticsWhenNeeded, config.RuntimeDiagnosticsAlways} {
		home := t.TempDir()
		dir := filepath.Join(home, ".config", "projmux")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create config dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, config.RuntimeDiagnosticsVisibilityFileName), []byte(string(mode)+"\n"), 0o644); err != nil {
			t.Fatalf("write saved visibility: %v", err)
		}

		server := runtimeVisibilityConflictServer()
		registry := runtimeFixtureRegistry()
		runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00primary": server}}
		command := newGetCommand()
		command.runtimeDiag = &runtimeDiagnosticsReader{
			runner:       runner,
			lookupEnv:    func(string) string { return "" },
			loadRegistry: func() (coremetadata.Registry, error) { return registry, nil },
			observe: func(ctx context.Context, transport resourcegraph.Transport) resourcegraph.Inventory {
				return intmetadata.NewInventoryObserver(runner, transport).Observe(ctx)
			},
		}
		before := server.state()

		var out strings.Builder
		for _, scope := range []string{"sessions", "windows", "panes"} {
			stdout, stderr, err := runGetRuntime(t, command, scope, "--socket", "primary")
			if err != nil {
				t.Fatalf("%s: get runtime %s: %v (%s)", mode, scope, err, stderr)
			}
			out.WriteString(stdout)
		}
		outputs[mode] = out.String()
		if server.state() != before {
			t.Fatalf("%s: get runtime mutated the server", mode)
		}
		states[mode] = server.state()
		registries[mode] = registryFingerprint(registry)
	}

	if outputs[config.RuntimeDiagnosticsWhenNeeded] != outputs[config.RuntimeDiagnosticsAlways] {
		t.Fatalf("get runtime output changed with the sidebar preference:\n--- when-needed ---\n%s\n--- always ---\n%s",
			outputs[config.RuntimeDiagnosticsWhenNeeded], outputs[config.RuntimeDiagnosticsAlways])
	}
	if states[config.RuntimeDiagnosticsWhenNeeded] != states[config.RuntimeDiagnosticsAlways] {
		t.Fatal("the exact server differs between the two sidebar preferences")
	}
	if registries[config.RuntimeDiagnosticsWhenNeeded] != registries[config.RuntimeDiagnosticsAlways] {
		t.Fatal("the Registry differs between the two sidebar preferences")
	}
}

func registryFingerprint(registry coremetadata.Registry) string {
	var b strings.Builder
	for _, project := range registry.Projects {
		b.WriteString("project " + project.Metadata.UID + " " + project.Spec.Root + "\n")
	}
	for _, window := range registry.Windows {
		b.WriteString("window " + window.Metadata.UID + "\n")
	}
	for _, pane := range registry.Panes {
		b.WriteString("pane " + pane.Metadata.UID + "\n")
	}
	return b.String()
}

// TestRuntimeDiagnosticsVisibilityLeavesTheOtherNavigationSurfacesAlone is
// acceptance (7): the preference owns one row on one surface. The Registry view
// still emits its complete Runtime row and counts, the Resource Inspector's
// action set is unchanged, and the Sessions surface's own Runtime link is
// byte-identical in both modes.
func TestRuntimeDiagnosticsVisibilityLeavesTheOtherNavigationSurfacesAlone(t *testing.T) {
	t.Parallel()

	views := map[string]string{}
	sessionLinks := map[string]string{}
	for _, saved := range []string{string(config.RuntimeDiagnosticsWhenNeeded), string(config.RuntimeDiagnosticsAlways)} {
		server := runtimeVisibilityControlServer()
		command, _, _ := runtimeVisibilityFixture(t, server, "/tmp/fake-tmux/primary,0,0", saved)
		view, err := command.navigationView(context.Background())
		if err != nil {
			t.Fatalf("%s: navigation view: %v", saved, err)
		}

		// The projection boundary: the view keeps the Runtime row and the
		// complete tally whichever way the sidebar renders.
		runtimeRows := 0
		for _, row := range view.Rows {
			if row.Kind == registryview.RowKindRuntimeLink {
				runtimeRows++
				if !row.Allows(registryview.ActionRuntime) {
					t.Fatalf("%s: the Registry view's Runtime row lost its action", saved)
				}
			}
		}
		if runtimeRows != 1 {
			t.Fatalf("%s: the Registry view emitted %d Runtime rows, want exactly one", saved, runtimeRows)
		}
		if got, want := view.Runtime, (registryview.RuntimeCounts{Control: 1, Ephemeral: 1, Unattributed: 4}); got != want {
			t.Fatalf("%s: Registry view counts = %+v, want the complete tally %+v", saved, got, want)
		}

		var b strings.Builder
		for _, row := range view.Rows {
			b.WriteString(string(row.Section) + " " + string(row.Kind) + " " + row.ID + " " +
				string(row.Status) + " " + registryNavigationActionList(row) + " " + row.Reason + "\n")
		}
		views[saved] = b.String()

		// The Sessions surface owns its own withheld tally and its own link.
		summaries := make([]inttmux.RecentSessionSummary, 0, len(server.sessions))
		for _, session := range server.sessions {
			summaries = append(summaries, inttmux.RecentSessionSummary{
				ID: session.id, Name: session.name, WindowCount: len(session.windows),
			})
		}
		sessions := &sessionsCommand{navigation: command.navigation.reader}
		attribution := sessions.attributeSessions(context.Background(), summaries)
		entry, ok := attribution.runtimeLinkEntry()
		if !ok {
			t.Fatalf("%s: the Sessions surface stopped offering its Runtime link", saved)
		}
		if entry.Value != sessionsRuntimeSentinel || !strings.Contains(entry.Label, "control 1") {
			t.Fatalf("%s: Sessions Runtime link = %+v, want the shipped sentinel and tally", saved, entry)
		}
		sessionLinks[saved] = entry.Label + "\x00" + entry.Value + "\x00" + entry.SearchKey
	}
	if views[string(config.RuntimeDiagnosticsWhenNeeded)] != views[string(config.RuntimeDiagnosticsAlways)] {
		t.Fatalf("the Registry navigation view changed with the sidebar preference:\n--- when-needed ---\n%s\n--- always ---\n%s",
			views[string(config.RuntimeDiagnosticsWhenNeeded)], views[string(config.RuntimeDiagnosticsAlways)])
	}
	if sessionLinks[string(config.RuntimeDiagnosticsWhenNeeded)] != sessionLinks[string(config.RuntimeDiagnosticsAlways)] {
		t.Fatal("the Sessions surface's Runtime link changed with the sidebar preference")
	}
}
