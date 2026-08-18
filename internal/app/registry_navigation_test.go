package app

import (
	"bytes"
	"context"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/registryview"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

// The Registry-first primary navigation, at the route boundary.
//
// Every fixture here runs the production observation adapter against the same
// fake tmux servers the Runtime escape hatch is tested with, so what is asserted
// is the argv projmux actually issues. "A navigation refresh writes nothing,
// reconciles nothing, and never looks at a second socket" is a checked property
// of the calls, not a claim about the code's shape.

// navigationFixtureReader wires the production observer over a routed fake with
// a primary and a sibling server, so only one of them may ever be touched.
func navigationFixtureReader(t *testing.T, host string, inherited string) (*registryNavigationReader, *fakeTmux, *fakeTmux, *routedTmuxRunner) {
	t.Helper()
	primary := runtimeFixtureServer(host)
	sibling := runtimeFixtureServer(host)
	sibling.socketPath = "/tmp/fake-tmux/sibling"
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{
		"-L\x00primary":                primary,
		"-L\x00sibling":                sibling,
		"-S\x00/tmp/fake-tmux/primary": primary,
		"-S\x00/tmp/fake-tmux/sibling": sibling,
	}}
	registry := runtimeFixtureRegistry()
	reader := &registryNavigationReader{reader: &runtimeDiagnosticsReader{
		runner:       runner,
		lookupEnv:    func(name string) string { return map[string]string{"TMUX": inherited}[name] },
		loadRegistry: func() (coremetadata.Registry, error) { return registry, nil },
		observe: func(ctx context.Context, transport resourcegraph.Transport) resourcegraph.Inventory {
			return intmetadata.NewInventoryObserver(runner, transport).Observe(ctx)
		},
	}}
	return reader, primary, sibling, runner
}

func navigationRowIDs(view registryview.View) []string {
	out := make([]string, 0, len(view.Rows))
	for _, row := range view.Rows {
		out = append(out, row.ID)
	}
	return out
}

// TestRegistryNavigationReadIsZeroWriteAndSingleSocket is acceptance (4) and the
// transport half of acceptance (5): a refresh issues the bounded read set
// through one inherited socket, never a write verb, never a reconcile, and never
// the sibling server.
func TestRegistryNavigationReadIsZeroWriteAndSingleSocket(t *testing.T) {
	t.Parallel()

	reader, primary, sibling, runner := navigationFixtureReader(t, "1", "/tmp/fake-tmux/primary,0,0")
	before := primary.state()

	for range 3 {
		if _, err := reader.view(context.Background(), nil); err != nil {
			t.Fatalf("navigation view: %v", err)
		}
	}

	if primary.state() != before {
		t.Fatalf("navigation refresh mutated the server:\n--- before ---\n%s\n--- after ---\n%s", before, primary.state())
	}
	if len(sibling.calls) != 0 {
		t.Fatalf("navigation refresh contacted the sibling socket: %v", sibling.calls)
	}
	for _, call := range runner.calls {
		if call.flag != "-S" || call.value != "/tmp/fake-tmux/primary" {
			t.Fatalf("navigation refresh routed to %s %s, want the inherited -S path", call.flag, call.value)
		}
	}
	writeVerbs := []string{
		"set-option", "rename-window", "new-session", "new-window", "split-window",
		"kill-session", "kill-window", "kill-pane", "set-environment", "switch-client", "attach-session",
	}
	verbs := map[string]bool{}
	for _, call := range primary.calls {
		if len(call) == 0 {
			continue
		}
		if slices.Contains(writeVerbs, call[0]) {
			t.Fatalf("navigation refresh issued the write verb %q: %v", call[0], call)
		}
		verbs[call[0]] = true
	}
	want := []string{"show-options", "list-sessions", "list-windows", "list-panes"}
	for _, verb := range want {
		if !verbs[verb] {
			t.Fatalf("navigation refresh never issued %q; observed %v", verb, verbs)
		}
	}
	if len(verbs) != len(want) {
		t.Fatalf("navigation refresh issued unexpected verbs: %v, want exactly %v", verbs, want)
	}
	// Three refreshes cost three bounded observations of four queries each, not
	// one query per row.
	if got, want := len(primary.calls), 12; got != want {
		t.Fatalf("three refreshes issued %d tmux calls, want %d", got, want)
	}
}

// TestRegistryNavigationOutsideTmuxProbesNoDefaultServer is the rest of
// acceptance (5): with no transport the rows are still the Registry's rows and
// the cost is zero tmux calls -- not a bare `tmux`, which would answer about the
// default socket.
func TestRegistryNavigationOutsideTmuxProbesNoDefaultServer(t *testing.T) {
	t.Parallel()

	reader, primary, sibling, runner := navigationFixtureReader(t, "1", "")

	view, err := reader.view(context.Background(), nil)
	if err != nil {
		t.Fatalf("navigation view: %v", err)
	}
	if len(runner.calls) != 0 || len(primary.calls) != 0 || len(sibling.calls) != 0 {
		t.Fatalf("no-transport read issued tmux calls: routed=%v primary=%v sibling=%v",
			runner.calls, primary.calls, sibling.calls)
	}
	if view.Observed() {
		t.Fatalf("no-transport view reports an observed server: %+v", view.Transport)
	}
	want := []string{
		"uid:" + runtimeFixtureProject,
		"uid:" + runtimeFixtureWindow,
		"uid:" + runtimeFixturePane,
		registryview.RuntimeLinkID,
	}
	if got := navigationRowIDs(view); !reflect.DeepEqual(got, want) {
		t.Fatalf("no-transport rows = %#v, want the Registry rows %#v", got, want)
	}
	for _, row := range view.Rows {
		if row.Kind == registryview.RowKindRuntimeLink {
			continue
		}
		if row.Status != resourcegraph.StatusUnknown {
			t.Fatalf("row %q status = %q, want unknown outside tmux", row.ID, row.Status)
		}
		if !row.Allows(registryview.ActionStart) && !row.Allows(registryview.ActionResume) {
			t.Fatalf("row %q actions = %v, want the offline revive action", row.ID, row.Actions)
		}
	}
}

// TestRegistryNavigationRowsAreIdenticalOnBothHostModes is the row half of
// acceptance (5): identity, order, and eligibility do not depend on which of the
// two supported hosts answered.
func TestRegistryNavigationRowsAreIdenticalOnBothHostModes(t *testing.T) {
	t.Parallel()

	appOwned, _, _, _ := navigationFixtureReader(t, "1", "/tmp/fake-tmux/primary,0,0")
	guest, _, _, _ := navigationFixtureReader(t, "", "/tmp/fake-tmux/primary,0,0")

	appView, err := appOwned.view(context.Background(), nil)
	if err != nil {
		t.Fatalf("app-owned navigation view: %v", err)
	}
	guestView, err := guest.view(context.Background(), nil)
	if err != nil {
		t.Fatalf("standalone navigation view: %v", err)
	}
	if appView.HostMode != resourcegraph.HostModeAppOwned {
		t.Fatalf("host mode = %q, want app-owned", appView.HostMode)
	}
	if guestView.HostMode != resourcegraph.HostModeStandalone {
		t.Fatalf("host mode = %q, want standalone", guestView.HostMode)
	}
	if got, want := navigationRowIDs(guestView), navigationRowIDs(appView); !reflect.DeepEqual(got, want) {
		t.Fatalf("standalone rows = %#v, want %#v", got, want)
	}
	for _, row := range appView.Rows {
		other, ok := guestView.Row(row.ID)
		if !ok {
			t.Fatalf("row %q is absent on the standalone host", row.ID)
		}
		if !reflect.DeepEqual(other.Actions, row.Actions) || other.Status != row.Status {
			t.Fatalf("row %q differs across hosts: %+v vs %+v", row.ID, other, row)
		}
	}
}

// navigationArgvRecorder records the argv one action forwarded to an existing
// route. Every action on this surface must reach a shipped route, so the
// recorded argv is what proves nothing here reimplemented one.
type navigationArgvRecorder struct {
	calls [][]string
}

func (c *navigationArgvRecorder) Run(args []string, _, _ io.Writer) error {
	c.calls = append(c.calls, slices.Clone(args))
	return nil
}

// scriptedNavigationPicker returns a picker runner that answers each successive
// Run with the next scripted value and records the options it was given.
type scriptedNavigationPicker struct {
	values  []string
	seen    []intpicker.Options
	current int
}

func (p *scriptedNavigationPicker) Run(options intpicker.Options) (intpicker.Result, error) {
	p.seen = append(p.seen, options)
	if p.current >= len(p.values) {
		return intpicker.Result{}, nil
	}
	value := p.values[p.current]
	p.current++
	return intpicker.Result{Value: value, Key: "enter"}, nil
}

func navigationCommandFixture(t *testing.T, inherited string, values ...string) (*registryNavigationCommand, *scriptedNavigationPicker) {
	t.Helper()
	reader, _, _, _ := navigationFixtureReader(t, "1", inherited)
	picker := &scriptedNavigationPicker{values: values}
	return &registryNavigationCommand{
		reader:    reader,
		native:    picker,
		homeDir:   func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(name string) string { return map[string]string{"TMUX": inherited}[name] },
	}, picker
}

// TestRegistryNavigationHierarchyListsOneProjectSubtree pins what the surface
// renders, including the header that names which server the status came from.
func TestRegistryNavigationHierarchyListsOneProjectSubtree(t *testing.T) {
	t.Parallel()

	command, picker := navigationCommandFixture(t, "/tmp/fake-tmux/primary,0,0", "")
	if err := command.runProject(context.Background(), switchUIPopup, runtimeFixtureProject,
		&bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("runProject: %v", err)
	}
	if len(picker.seen) != 1 {
		t.Fatalf("picker ran %d times, want once", len(picker.seen))
	}
	labels := make([]string, 0, len(picker.seen[0].Items))
	for _, item := range picker.seen[0].Items {
		labels = append(labels, item.EffectiveLabel())
	}
	joined := strings.Join(labels, "\n")
	for _, want := range []string{
		"host app-owned", "transport tmux -S /tmp/fake-tmux/primary",
		"alpha", "editor", "shell", "live", "open,delete",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("hierarchy list is missing %q:\n%s", want, joined)
		}
	}
	for _, forbidden := range []string{"Home", "scratch", "ghost", "notes"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("hierarchy list names the runtime-only object %q:\n%s", forbidden, joined)
		}
	}
}

// TestRegistryNavigationHierarchyRefusesAnUnknownProject keeps the entry point
// honest: the surface is entered by uid and a uid the Registry does not carry is
// an error rather than an empty list.
func TestRegistryNavigationHierarchyRefusesAnUnknownProject(t *testing.T) {
	t.Parallel()

	command, _ := navigationCommandFixture(t, "", "")
	err := command.runProject(context.Background(), switchUIPopup, "proj-nope", &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "proj-nope") {
		t.Fatalf("runProject error = %v, want a refusal naming the uid", err)
	}
}

// TestRegistryNavigationProjectUIDWalksToTheOwningProject pins the owner walk
// the rebind and start-project actions address a row's Project through.
func TestRegistryNavigationProjectUIDWalksToTheOwningProject(t *testing.T) {
	t.Parallel()

	reader, _, _, _ := navigationFixtureReader(t, "1", "/tmp/fake-tmux/primary,0,0")
	view, err := reader.view(context.Background(), nil)
	if err != nil {
		t.Fatalf("navigation view: %v", err)
	}
	pane, ok := view.Row("uid:" + runtimeFixturePane)
	if !ok {
		t.Fatalf("pane row is absent from %v", navigationRowIDs(view))
	}
	if got := registryNavigationProjectUID(view, pane); got != runtimeFixtureProject {
		t.Fatalf("owning project of the pane row = %q, want %q", got, runtimeFixtureProject)
	}
}

// TestRegistryNavigationActionsForwardToShippedRoutes pins the whole action
// boundary: a live row focuses, an offline Project starts through the one route
// that materializes one, an Agent resumes through `agent resume`, and the
// Runtime link opens the escape hatch. Nothing here writes on its own.
func TestRegistryNavigationActionsForwardToShippedRoutes(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		inherit  string
		rowID    string
		sentinel string
		route    string
		wantArgv []string
	}{
		{
			name:     "live pane focuses through the exact socket",
			inherit:  "/tmp/fake-tmux/primary,0,0",
			rowID:    "uid:" + runtimeFixturePane,
			sentinel: navActionOpen,
			route:    "focus",
			wantArgv: []string{"--target", "%3", "--socket", "/tmp/fake-tmux/primary"},
		},
		{
			name:     "offline project starts through attach project",
			inherit:  "",
			rowID:    "uid:" + runtimeFixtureProject,
			sentinel: navActionStart,
			route:    "attach",
			wantArgv: []string{"project", "uid:" + runtimeFixtureProject},
		},
		{
			name:     "a row beneath a project starts its owning project",
			inherit:  "",
			rowID:    "uid:" + runtimeFixtureWindow,
			sentinel: navActionStartProject,
			route:    "attach",
			wantArgv: []string{"project", "uid:" + runtimeFixtureProject},
		},
		{
			name:     "the runtime link opens the escape hatch",
			inherit:  "",
			rowID:    "uid:" + runtimeFixtureProject,
			sentinel: navActionRuntime,
			route:    "runtime",
			wantArgv: []string{"diagnostics"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			command, _ := navigationCommandFixture(t, test.inherit, test.rowID, test.sentinel)
			routes := map[string]*navigationArgvRecorder{
				"focus": {}, "attach": {}, "agent": {}, "runtime": {},
			}
			command.focus = routes["focus"]
			command.attach = routes["attach"]
			command.agent = routes["agent"]
			command.runtime = routes["runtime"]

			if err := command.runProject(context.Background(), switchUIPopup, runtimeFixtureProject,
				&bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("runProject: %v", err)
			}
			for name, recorder := range routes {
				switch {
				case name == test.route:
					if len(recorder.calls) != 1 || !reflect.DeepEqual(recorder.calls[0], test.wantArgv) {
						t.Fatalf("%s route calls = %#v, want one call with %#v", name, recorder.calls, test.wantArgv)
					}
				case len(recorder.calls) != 0:
					t.Fatalf("%s route was called with %#v; only %s should have run", name, recorder.calls, test.route)
				}
			}
		})
	}
}
