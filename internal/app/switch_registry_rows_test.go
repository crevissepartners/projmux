package app

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/candidates"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// The Projects picker, with the Registry as its row source.
//
// The assertions here are about which rows exist, in which order, and what a
// selection carries -- not about the card's glyphs. A Registry Project is a row
// whether or not tmux answered; a discovered directory is a row in its own
// section; and the Runtime link is the last thing on the list.

// switchRegistryFixture wires a Projects picker over the navigation fixture's
// exact host so the rows come from the same Registry the navigation tests use.
func switchRegistryFixture(t *testing.T, inherited string, discovered []string, result string) (*switchCommand, *oneShotSwitchRunner) {
	t.Helper()
	reader, _, _, _ := navigationFixtureReader(t, "1", inherited)
	runner := &oneShotSwitchRunner{result: intpickercompat.Result{Value: result}}
	return &switchCommand{
		discover: func(candidates.Inputs) ([]string, error) { return discovered, nil },
		pinStore: func() (switchPinStore, error) { return &stubSwitchPinStore{}, nil },
		runner:   runner,
		// The picker is driven through the compat shim the shipped surfaces use
		// so the entries asserted are the entries a real run would render.
		nativePicker: nativePickerFromCompatRunner(runner),
		sessions:     &capturingSwitchSessionExecutor{},
		identity: switchIdentityResolverFunc(func(path string) (string, error) {
			if strings.TrimSpace(path) == "" {
				return "", errors.New("empty path")
			}
			return strings.ReplaceAll(strings.TrimPrefix(path, "/"), "/", "-"), nil
		}),
		validate:      func(string) error { return nil },
		homeDir:       func() (string, error) { return "/home/tester", nil },
		workingDir:    func() (string, error) { return "/src", nil },
		lookupEnv:     func(name string) string { return map[string]string{"TMUX": inherited}[name] },
		gitBranch:     func(string) string { return "" },
		executable:    func() (string, error) { return "/tmp/projmux", nil },
		rawExecutable: func() (string, error) { return "/tmp/projmux", nil },
		navigation: &registryNavigationCommand{
			reader:    reader,
			native:    &scriptedNavigationPicker{},
			homeDir:   func() (string, error) { return t.TempDir(), nil },
			lookupEnv: func(name string) string { return map[string]string{"TMUX": inherited}[name] },
		},
	}, runner
}

func switchEntryValues(entries []intpickercompat.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Value)
	}
	return out
}

// TestSwitchRowsAreRegistryFirstAndSectioned pins the row source: the Registry
// Project leads, the discovered directory that no Project claims follows, and
// the Runtime link is last.
func TestSwitchRowsAreRegistryFirstAndSectioned(t *testing.T) {
	t.Parallel()

	command, runner := switchRegistryFixture(t, "/tmp/fake-tmux/primary,0,0",
		[]string{"/src/gamma", "/src/alpha"}, "")
	if err := command.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []string{"/src/alpha", "/src/gamma", switchRuntimeSentinel}
	if got := switchEntryValues(runner.last.Entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("entry values = %#v, want %#v", got, want)
	}
}

// TestSwitchRowsListRegistryProjectsWithoutATmuxServer is acceptance (1) at the
// picker: the Registry Project is a row with no transport at all, and the
// discovered directory it shadows is still not duplicated.
func TestSwitchRowsListRegistryProjectsWithoutATmuxServer(t *testing.T) {
	t.Parallel()

	command, runner := switchRegistryFixture(t, "", []string{"/src/alpha", "/src/gamma"}, "")
	if err := command.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []string{"/src/alpha", "/src/gamma", switchRuntimeSentinel}
	if got := switchEntryValues(runner.last.Entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("no-server entry values = %#v, want %#v", got, want)
	}
	labels := strings.Join(entryLabels(runner.last.Entries), "\n")
	if !strings.Contains(labels, "Runtime - no tmux transport") {
		t.Fatalf("no-server runtime link does not say why it is empty:\n%s", labels)
	}
}

// TestSwitchRowIdentityAndOrderSurviveARuntimeTransition is acceptance (2) at the
// picker: the same Registry renders the same values in the same order whether or
// not the runtime is up, so a selection cannot move under an operator.
func TestSwitchRowIdentityAndOrderSurviveARuntimeTransition(t *testing.T) {
	t.Parallel()

	live, liveRunner := switchRegistryFixture(t, "/tmp/fake-tmux/primary,0,0",
		[]string{"/src/gamma", "/src/alpha"}, "")
	if err := live.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("live Run() error = %v", err)
	}
	dark, darkRunner := switchRegistryFixture(t, "", []string{"/src/gamma", "/src/alpha"}, "")
	if err := dark.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("no-server Run() error = %v", err)
	}

	if got, want := switchEntryValues(darkRunner.last.Entries), switchEntryValues(liveRunner.last.Entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("no-server entry values = %#v, want the live values %#v", got, want)
	}
}

// TestSwitchRowsWithdrawRuntimeOnlyObjects is acceptance (3) at the picker: the
// Home control session, a scratch session and a hand-opened window are on the
// fixture server and none of them is a row.
func TestSwitchRowsWithdrawRuntimeOnlyObjects(t *testing.T) {
	t.Parallel()

	command, runner := switchRegistryFixture(t, "/tmp/fake-tmux/primary,0,0", []string{"/src/alpha"}, "")
	if err := command.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	labels := strings.Join(entryLabels(runner.last.Entries), "\n")
	for _, forbidden := range []string{"Home", "scratch", "ghost", "notes"} {
		if strings.Contains(labels, forbidden) {
			t.Fatalf("Projects list names the runtime-only object %q:\n%s", forbidden, labels)
		}
	}
	if !strings.Contains(labels, "Runtime - ") {
		t.Fatalf("Projects list has no Runtime link to find them through:\n%s", labels)
	}
	for _, want := range []string{"control 1", "ephemeral 1", "unattributed"} {
		if !strings.Contains(labels, want) {
			t.Fatalf("Runtime link does not name %q:\n%s", want, labels)
		}
	}
}

// TestSwitchMissingRootProjectSelectsByUID pins the one row whose selection is
// not a path. Before this, a Project whose spec.root vanished failed the whole
// picker on directory validation instead of offering the repair.
func TestSwitchMissingRootProjectSelectsByUID(t *testing.T) {
	t.Parallel()

	reader, _, _, _ := navigationFixtureReader(t, "1", "")
	registry := runtimeFixtureRegistry()
	registry.Projects[0].Status.Conditions = []coremetadata.Condition{{
		Type:   coremetadata.ConditionMissingRoot,
		Status: coremetadata.ConditionTrue,
	}}
	reader.reader.loadRegistry = func() (coremetadata.Registry, error) { return registry, nil }

	command, runner := switchRegistryFixture(t, "", []string{"/src/gamma"}, "")
	command.navigation.reader = reader
	command.validate = func(string) error { return errors.New("directory does not exist") }
	if err := command.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []string{"uid:" + runtimeFixtureProject, "/src/gamma", switchRuntimeSentinel}
	if got := switchEntryValues(runner.last.Entries); !reflect.DeepEqual(got, want) {
		t.Fatalf("entry values = %#v, want the missing-root Project selected by uid %#v", got, want)
	}
	if labels := strings.Join(entryLabels(runner.last.Entries), "\n"); !strings.Contains(labels, "missing root") {
		t.Fatalf("missing-root row does not say so:\n%s", labels)
	}
}

// TestSwitchRuntimeSelectionOpensTheEscapeHatch pins where the Runtime link
// goes: the shipped diagnostics route, with no path validation on the way.
func TestSwitchRuntimeSelectionOpensTheEscapeHatch(t *testing.T) {
	t.Parallel()

	command, _ := switchRegistryFixture(t, "", []string{"/src/gamma"}, switchRuntimeSentinel)
	route := &navigationArgvRecorder{}
	command.navigation.runtime = route
	command.validate = func(string) error { return errors.New("the runtime link is not a directory") }

	if err := command.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := route.calls, [][]string{{"diagnostics"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime route calls = %#v, want %#v", got, want)
	}
}

// TestSwitchHierarchyKeyOpensTheSelectedProject pins the dedicated key: it
// resolves the selected row's Project and opens the read-only hierarchy.
func TestSwitchHierarchyKeyOpensTheSelectedProject(t *testing.T) {
	t.Parallel()

	command, runner := switchRegistryFixture(t, "/tmp/fake-tmux/primary,0,0",
		[]string{"/src/alpha"}, "/src/alpha")
	runner.result.Key = registryNavigationExpectKey
	hierarchy := &scriptedNavigationPicker{values: []string{""}}
	command.navigation.native = hierarchy

	if err := command.Run([]string{"--ui=sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(hierarchy.seen) == 0 {
		t.Fatal("the hierarchy surface never opened")
	}
	if got, want := hierarchy.seen[0].Title, "Projects > Resources"; got != want {
		t.Fatalf("hierarchy title = %q, want %q", got, want)
	}
}

// TestRegistryProjectUIDForSelectionDeclinesAnUnregisteredPath is the honesty
// guard on the path lookup: an unregistered directory has no Project, and the
// lookup says so rather than choosing the nearest one.
func TestRegistryProjectUIDForSelectionDeclinesAnUnregisteredPath(t *testing.T) {
	t.Parallel()

	command, _ := switchRegistryFixture(t, "", nil, "")
	uid, err := command.registryProjectUIDForSelection(context.Background(), "/src/gamma")
	if err != nil {
		t.Fatalf("registryProjectUIDForSelection: %v", err)
	}
	if uid != "" {
		t.Fatalf("unregistered path resolved to Project %q, want no Project", uid)
	}
	uid, err = command.registryProjectUIDForSelection(context.Background(), "/src/alpha")
	if err != nil {
		t.Fatalf("registryProjectUIDForSelection: %v", err)
	}
	if uid != runtimeFixtureProject {
		t.Fatalf("Project root resolved to %q, want %q", uid, runtimeFixtureProject)
	}
}

// TestSwitchRegistryWindowTabsComeFromTheRegistry pins the card's window tabs:
// they are the Registry's Windows with the runtime deciding only which is live,
// so an offline Project still shows the topology the Registry knows.
func TestSwitchRegistryWindowTabsComeFromTheRegistry(t *testing.T) {
	t.Parallel()

	reader, _, _, _ := navigationFixtureReader(t, "1", "")
	view, err := reader.view(context.Background(), nil)
	if err != nil {
		t.Fatalf("navigation view: %v", err)
	}
	project, ok := view.Row("uid:" + runtimeFixtureProject)
	if !ok {
		t.Fatal("the fixture Project row is absent")
	}
	tabs := switchRegistryWindowTabs(view, project, "dot")
	if len(tabs) != 1 || tabs[0].Name != "editor" {
		t.Fatalf("window tabs = %#v, want the Registry Window with no transport", tabs)
	}
	if tabs[0].Active {
		t.Fatalf("window tab is marked active with no observed runtime: %#v", tabs[0])
	}
}

func entryLabels(entries []intpickercompat.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Label)
	}
	return out
}

var _ intpicker.Runner = (*scriptedNavigationPicker)(nil)

// oneShotSwitchRunner answers the first picker run with its scripted result and
// every later run with a cancel.
//
// The Projects picker reopens itself after an action that returns to the list,
// which is correct behavior and an infinite loop for a runner that answers the
// same way forever. Consuming the result once is what lets a test assert what
// one selection did.
type oneShotSwitchRunner struct {
	last     intpickercompat.Options
	result   intpickercompat.Result
	consumed bool
}

func (r *oneShotSwitchRunner) Run(options intpickercompat.Options) (intpickercompat.Result, error) {
	r.last = options
	if r.consumed {
		return intpickercompat.Result{}, nil
	}
	r.consumed = true
	return r.result, nil
}
