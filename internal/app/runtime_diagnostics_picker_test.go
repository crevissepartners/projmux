package app

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

// scriptedRuntimePicker answers a fixed sequence of selections and records every
// Options it was handed, so both halves of the surface are assertable: what an
// operator is shown, and what a selection reaches.
type scriptedRuntimePicker struct {
	answers  []intpicker.Result
	rendered []intpicker.Options
	index    int
}

func (p *scriptedRuntimePicker) Run(options intpicker.Options) (intpicker.Result, error) {
	p.rendered = append(p.rendered, options)
	if p.index >= len(p.answers) {
		return intpicker.Result{Closed: true}, nil
	}
	answer := p.answers[p.index]
	p.index++
	return answer, nil
}

// recordingSafeAction captures the argv one safe action forwarded to the route
// that already owns it. Recording rather than executing is the point: this
// surface must reach the shipped handler, not grow a second implementation.
type recordingSafeAction struct {
	calls [][]string
}

func (r *recordingSafeAction) Run(args []string, _, _ io.Writer) error {
	r.calls = append(r.calls, slices.Clone(args))
	return nil
}

func runtimePickerFixture(t *testing.T, host string, answers []intpicker.Result) (*runtimeDiagnosticsCommand, *scriptedRuntimePicker, *fakeTmux, *fakeTmux, map[string]*recordingSafeAction) {
	t.Helper()
	primary := runtimeFixtureServer(host)
	sibling := runtimeFixtureServer(host)
	sibling.socketPath = "/tmp/fake-tmux/sibling"
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{
		"-L\x00primary": primary,
		"-L\x00sibling": sibling,
	}}
	registry := runtimeFixtureRegistry()
	picker := &scriptedRuntimePicker{answers: answers}
	routes := map[string]*recordingSafeAction{
		"focus": {}, "attach": {}, "inspect": {},
	}
	command := &runtimeDiagnosticsCommand{
		reader: &runtimeDiagnosticsReader{
			runner:       runner,
			lookupEnv:    func(string) string { return "" },
			loadRegistry: func() (coremetadata.Registry, error) { return registry, nil },
			observe: func(ctx context.Context, transport resourcegraph.Transport) resourcegraph.Inventory {
				return intmetadata.NewInventoryObserver(runner, transport).Observe(ctx)
			},
		},
		native:    picker,
		homeDir:   func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(string) string { return "" },
		focus:     routes["focus"],
		attach:    routes["attach"],
		inspect:   routes["inspect"],
	}
	return command, picker, primary, sibling, routes
}

func runRuntimePicker(t *testing.T, command *runtimeDiagnosticsCommand, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := command.Run(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func pickerValues(options intpicker.Options) []string {
	out := make([]string, 0, len(options.Items))
	for _, item := range options.Items {
		out = append(out, item.Value)
	}
	return out
}

// actionMenu returns the last action menu the picker rendered.
//
// It is found by the Back row rather than by position: closing an action menu
// returns to the list, so the last render is the list again, and asserting on
// it would silently pass whatever the menu actually offered.
func actionMenu(t *testing.T, picker *scriptedRuntimePicker) intpicker.Options {
	t.Helper()
	for i := len(picker.rendered) - 1; i >= 0; i-- {
		if slices.Contains(pickerValues(picker.rendered[i]), settingsBackValue) {
			return picker.rendered[i]
		}
	}
	t.Fatalf("no action menu among %d rendered pickers", len(picker.rendered))
	return intpicker.Options{}
}

func pickerLabels(options intpicker.Options) []string {
	out := make([]string, 0, len(options.Items))
	for _, item := range options.Items {
		out = append(out, item.EffectiveLabel())
	}
	return out
}

// TestRuntimePickerListsEveryClassIncludingTheRefusedOnes is the diagnostics-UI
// half of acceptance (1) and (3): the picker shows control, ephemeral,
// unattributed, and recoverable objects beside the managed ones.
func TestRuntimePickerListsEveryClassIncludingTheRefusedOnes(t *testing.T) {
	t.Parallel()

	command, picker, _, _, _ := runtimePickerFixture(t, "1", nil)
	if _, _, err := runRuntimePicker(t, command, "--socket", "primary"); err != nil {
		t.Fatalf("runtime diagnostics: %v", err)
	}
	if len(picker.rendered) != 1 {
		t.Fatalf("rendered %d pickers, want 1", len(picker.rendered))
	}
	options := picker.rendered[0]

	var selectable []string
	for _, value := range pickerValues(options) {
		if strings.HasPrefix(value, "runtime:") {
			selectable = append(selectable, value)
		}
	}
	want := []string{
		"runtime:session:$1", "runtime:session:$8", "runtime:session:$11",
		"runtime:window:@2", "runtime:window:@4", "runtime:window:@6",
		"runtime:window:@9", "runtime:window:@12",
		"runtime:pane:%3", "runtime:pane:%5", "runtime:pane:%7",
		"runtime:pane:%10", "runtime:pane:%13",
	}
	if !slices.Equal(selectable, want) {
		t.Fatalf("picker rows = %v, want %v", selectable, want)
	}

	labels := strings.Join(pickerLabels(options), "\n")
	for _, class := range []string{"managed", "control", "ephemeral", "unattributed", "recoverable"} {
		if !strings.Contains(labels, class) {
			t.Fatalf("picker never renders class %q:\n%s", class, labels)
		}
	}
	if !strings.Contains(labels, "host app-owned  transport tmux -L primary") {
		t.Fatalf("picker omits the exact-host header:\n%s", labels)
	}
	if !strings.Contains(labels, "Project/alpha") {
		t.Fatalf("picker omits the managed binding:\n%s", labels)
	}
	// The tally names only the classes present, in the closed declaration order.
	if !strings.Contains(labels, "managed 3  recoverable 1  control 1  ephemeral 1  unattributed 7") {
		t.Fatalf("picker omits the attribution tally:\n%s", labels)
	}
	if strings.Contains(labels, "conflict 0") || strings.Contains(labels, "foreign 0") {
		t.Fatalf("picker tally reports empty classes:\n%s", labels)
	}
}

// TestRuntimePickerReadsOneSocketAndWritesNothing is the UI half of acceptance
// (2): opening and rendering the surface issues reads on one server only.
func TestRuntimePickerReadsOneSocketAndWritesNothing(t *testing.T) {
	t.Parallel()

	command, _, primary, sibling, _ := runtimePickerFixture(t, "1", nil)
	before := primary.state()
	if _, _, err := runRuntimePicker(t, command, "--socket", "primary"); err != nil {
		t.Fatalf("runtime diagnostics: %v", err)
	}
	if primary.state() != before {
		t.Fatalf("picker mutated the server:\n--- before ---\n%s\n--- after ---\n%s", before, primary.state())
	}
	if len(sibling.calls) != 0 {
		t.Fatalf("picker contacted the sibling socket: %v", sibling.calls)
	}
	writeVerbs := []string{"set-option", "rename-window", "new-session", "new-window", "kill-session", "switch-client"}
	for _, call := range primary.calls {
		if len(call) > 0 && slices.Contains(writeVerbs, call[0]) {
			t.Fatalf("picker issued the write verb %q: %v", call[0], call)
		}
	}
}

// TestRuntimePickerFocusHandsTheExactCoordinateToTheExistingRoute proves the
// default action forwards rather than reimplements, and that it carries the
// server's own socket path instead of the flag the operator typed.
func TestRuntimePickerFocusHandsTheExactCoordinateToTheExistingRoute(t *testing.T) {
	t.Parallel()

	command, picker, _, _, routes := runtimePickerFixture(t, "1", []intpicker.Result{
		{Value: "runtime:pane:%7"},
		{Value: runtimeActionFocus},
	})
	if _, _, err := runRuntimePicker(t, command, "--socket", "primary"); err != nil {
		t.Fatalf("runtime diagnostics: %v", err)
	}
	if len(routes["focus"].calls) != 1 {
		t.Fatalf("focus calls = %v, want exactly one", routes["focus"].calls)
	}
	want := []string{"--target", "alpha:@6.%7", "--socket", "/tmp/fake-tmux/primary"}
	if !slices.Equal(routes["focus"].calls[0], want) {
		t.Fatalf("focus argv = %v, want %v", routes["focus"].calls[0], want)
	}
	if len(routes["attach"].calls) != 0 || len(routes["inspect"].calls) != 0 {
		t.Fatalf("focus also reached another route: attach=%v inspect=%v",
			routes["attach"].calls, routes["inspect"].calls)
	}
	if len(picker.rendered) != 2 {
		t.Fatalf("rendered %d pickers, want the list plus the action menu", len(picker.rendered))
	}
}

// TestRuntimePickerAttachIsOfferedOnlyForABoundSession is the authority
// boundary: the outside-tmux Project entry point is reachable from a managed
// session and from nothing else, and the refusals say why.
func TestRuntimePickerAttachIsOfferedOnlyForABoundSession(t *testing.T) {
	t.Parallel()

	// A managed session, outside tmux: attach is offered and forwards the uid.
	command, _, _, _, routes := runtimePickerFixture(t, "1", []intpicker.Result{
		{Value: "runtime:session:$1"},
		{Value: runtimeActionAttach},
	})
	if _, _, err := runRuntimePicker(t, command, "--socket", "primary"); err != nil {
		t.Fatalf("runtime diagnostics: %v", err)
	}
	want := []string{"project", "uid:" + runtimeFixtureProject}
	if len(routes["attach"].calls) != 1 || !slices.Equal(routes["attach"].calls[0], want) {
		t.Fatalf("attach argv = %v, want %v", routes["attach"].calls, want)
	}

	// Every other row states the refusal instead of offering the action.
	for _, test := range []struct {
		row  string
		want string
	}{
		{row: "runtime:session:$8", want: "no Registry Project claims this session"},
		{row: "runtime:session:$11", want: "no Registry Project claims this session"},
		{row: "runtime:window:@2", want: "only a session projects a Project runtime"},
		{row: "runtime:pane:%3", want: "only a session projects a Project runtime"},
	} {
		command, picker, _, _, routes := runtimePickerFixture(t, "1", []intpicker.Result{
			{Value: test.row},
			{Closed: true},
		})
		if _, _, err := runRuntimePicker(t, command, "--socket", "primary"); err != nil {
			t.Fatalf("%s: %v", test.row, err)
		}
		if len(routes["attach"].calls) != 0 {
			t.Fatalf("%s reached attach: %v", test.row, routes["attach"].calls)
		}
		menu := actionMenu(t, picker)
		labels := strings.Join(pickerLabels(menu), "\n")
		if !strings.Contains(labels, test.want) {
			t.Fatalf("%s action menu does not state %q:\n%s", test.row, test.want, labels)
		}
		if slices.Contains(pickerValues(menu), runtimeActionAttach) {
			t.Fatalf("%s offers attach: %v", test.row, pickerValues(menu))
		}
	}
}

// TestRuntimePickerInsideTmuxPrefersFocusOverAttach pins the one refusal that
// is about the operator's position rather than the object's identity.
func TestRuntimePickerInsideTmuxPrefersFocusOverAttach(t *testing.T) {
	t.Parallel()

	command, picker, _, _, _ := runtimePickerFixture(t, "1", []intpicker.Result{
		{Value: "runtime:session:$1"},
		{Closed: true},
	})
	command.lookupEnv = func(key string) string {
		if key == "TMUX" {
			return "/tmp/fake-tmux/primary,1,0"
		}
		return ""
	}
	if _, _, err := runRuntimePicker(t, command, "--socket", "primary"); err != nil {
		t.Fatalf("runtime diagnostics: %v", err)
	}
	menu := actionMenu(t, picker)
	if slices.Contains(pickerValues(menu), runtimeActionAttach) {
		t.Fatalf("attach is offered inside a tmux client: %v", pickerValues(menu))
	}
	if !strings.Contains(strings.Join(pickerLabels(menu), "\n"), "already inside a tmux client") {
		t.Fatalf("inside-tmux refusal is not stated:\n%s", strings.Join(pickerLabels(menu), "\n"))
	}
	if !slices.Contains(pickerValues(menu), runtimeActionFocus) {
		t.Fatalf("focus is not offered inside a tmux client: %v", pickerValues(menu))
	}
}

// TestRuntimePickerInspectReachesTheExistingResourceInspector proves the third
// safe action is the shipped inspector, invoked with no arguments of its own.
func TestRuntimePickerInspectReachesTheExistingResourceInspector(t *testing.T) {
	t.Parallel()

	command, _, _, _, routes := runtimePickerFixture(t, "1", []intpicker.Result{
		{Value: "runtime:window:@6"},
		{Value: runtimeActionInspect},
	})
	if _, _, err := runRuntimePicker(t, command, "--socket", "primary"); err != nil {
		t.Fatalf("runtime diagnostics: %v", err)
	}
	if len(routes["inspect"].calls) != 1 || len(routes["inspect"].calls[0]) != 0 {
		t.Fatalf("inspect argv = %v, want one call with no arguments", routes["inspect"].calls)
	}
}

// TestRuntimePickerOffersNoDestructiveOrAdoptingAction is the negative
// acceptance: the whole action vocabulary of every row is the three safe
// forwards and the inert rows.
func TestRuntimePickerOffersNoDestructiveOrAdoptingAction(t *testing.T) {
	t.Parallel()

	allowed := map[string]bool{
		settingsBackValue: true, settingsNoopValue: true,
		runtimeActionFocus: true, runtimeActionAttach: true, runtimeActionInspect: true,
	}
	for _, row := range []string{
		"runtime:session:$1", "runtime:session:$8", "runtime:session:$11",
		"runtime:window:@2", "runtime:window:@6", "runtime:pane:%3", "runtime:pane:%7",
	} {
		command, picker, primary, _, _ := runtimePickerFixture(t, "1", []intpicker.Result{
			{Value: row},
			{Closed: true},
		})
		if _, _, err := runRuntimePicker(t, command, "--socket", "primary"); err != nil {
			t.Fatalf("%s: %v", row, err)
		}
		for _, value := range pickerValues(actionMenu(t, picker)) {
			if !allowed[value] {
				t.Fatalf("%s action menu offers %q, which is outside the safe-action set", row, value)
			}
		}
		if primary.argvContains("kill-session") || primary.argvContains("kill-pane") || primary.argvContains("set-option") {
			t.Fatalf("%s: opening the action menu mutated the server: %v", row, primary.calls)
		}
	}
}

// TestRuntimePickerOutsideTmuxShowsTheUnavailableProjection is the UI half of
// acceptance (4): with no transport the surface opens, explains itself, and
// lists no objects.
func TestRuntimePickerOutsideTmuxShowsTheUnavailableProjection(t *testing.T) {
	t.Parallel()

	command, picker, primary, sibling, _ := runtimePickerFixture(t, "1", nil)
	if _, _, err := runRuntimePicker(t, command); err != nil {
		t.Fatalf("runtime diagnostics outside tmux: %v", err)
	}
	labels := strings.Join(pickerLabels(picker.rendered[0]), "\n")
	if !strings.Contains(labels, "no tmux transport") {
		t.Fatalf("no-transport header missing:\n%s", labels)
	}
	for _, scope := range resourcegraph.Scopes() {
		if !strings.Contains(labels, "unavailable "+string(scope)+": ") {
			t.Fatalf("no-transport picker omits scope %q:\n%s", scope, labels)
		}
	}
	for _, value := range pickerValues(picker.rendered[0]) {
		if strings.HasPrefix(value, "runtime:") {
			t.Fatalf("no-transport picker invented a row: %q", value)
		}
	}
	if len(primary.calls) != 0 || len(sibling.calls) != 0 {
		t.Fatalf("no-transport picker probed a server: primary=%v sibling=%v", primary.calls, sibling.calls)
	}
}

// TestRuntimePickerRefusesMalformedInvocations pins the flag surface.
func TestRuntimePickerRefusesMalformedInvocations(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "positional", args: []string{"extra"}, want: "does not accept positional arguments"},
		{name: "bad ui", args: []string{"--ui", "kiosk"}, want: "invalid --ui value"},
		{name: "both sockets", args: []string{"--socket", "a", "--socket-path", "/tmp/b"}, want: "mutually exclusive"},
		{name: "relative socket path", args: []string{"--socket-path", "relative"}, want: "must be absolute"},
	} {
		command, picker, primary, _, _ := runtimePickerFixture(t, "1", nil)
		_, _, err := runRuntimePicker(t, command, test.args...)
		if err == nil {
			t.Fatalf("%s: expected a refusal", test.name)
		}
		if !IsUsageError(err) || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%s: error = %v, want a usage error mentioning %q", test.name, err, test.want)
		}
		if len(picker.rendered) != 0 {
			t.Fatalf("%s: a refused invocation opened the picker", test.name)
		}
		if len(primary.calls) != 0 {
			t.Fatalf("%s: a refused invocation contacted tmux: %v", test.name, primary.calls)
		}
	}
}

// TestRuntimeDiagnosticsIsReachableThroughTheRuntimeNamespace proves the route
// is wired where the manifest says it is.
func TestRuntimeDiagnosticsIsReachableThroughTheRuntimeNamespace(t *testing.T) {
	t.Parallel()

	command, picker, _, _, _ := runtimePickerFixture(t, "1", nil)
	namespace := &runtimeCommand{diagnostics: command}
	var stdout, stderr bytes.Buffer
	if err := namespace.Run([]string{"diagnostics", "--socket", "primary"}, &stdout, &stderr); err != nil {
		t.Fatalf("runtime diagnostics through the namespace: %v", err)
	}
	if len(picker.rendered) != 1 {
		t.Fatalf("namespace forward rendered %d pickers, want 1", len(picker.rendered))
	}
	// The unknown-subcommand refusal advertises it.
	err := (&runtimeCommand{}).Run([]string{"nope"}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "diagnostics") {
		t.Fatalf("runtime refusal does not advertise diagnostics: %v", err)
	}
}

// TestRuntimePickerLocalizesChromeButNeverTheEvidence is the locale contract of
// the surface: the words projmux chose translate, and the words the machine
// reported do not.
//
// The split matters more here than on most pickers. A tmux id, a mirrored uid,
// a session name, and an attribution class are evidence an operator pastes into
// another command or a bug report; translating any of them would produce a
// handle that resolves to nothing.
func TestRuntimePickerLocalizesChromeButNeverTheEvidence(t *testing.T) {
	t.Parallel()

	command, picker, _, _, _ := runtimePickerFixture(t, "1", []intpicker.Result{
		{Value: "runtime:session:$8"},
		{Closed: true},
	})
	command.lookupEnv = func(key string) string {
		if key == "LANG" {
			return "ko_KR.UTF-8"
		}
		return ""
	}
	if _, _, err := runRuntimePicker(t, command, "--socket", "primary"); err != nil {
		t.Fatalf("runtime diagnostics: %v", err)
	}

	list := picker.rendered[0]
	if list.Title == "Runtime diagnostics" || list.Footer == "Enter: actions | Esc: close" {
		t.Fatalf("list chrome stayed English under ko-KR: title=%q footer=%q", list.Title, list.Footer)
	}
	labels := strings.Join(pickerLabels(list), "\n")
	for _, evidence := range []string{"$8", "@2", "%3", "control", "unattributed", "recoverable", "Project/alpha", "alpha"} {
		if !strings.Contains(labels, evidence) {
			t.Fatalf("ko-KR list lost the evidence %q:\n%s", evidence, labels)
		}
	}

	menu := actionMenu(t, picker)
	if menu.Title == "Runtime > Actions" {
		t.Fatalf("action menu title stayed English under ko-KR: %q", menu.Title)
	}
	menuLabels := strings.Join(pickerLabels(menu), "\n")
	if strings.Contains(menuLabels, "no Registry Project claims this session") {
		t.Fatalf("action refusal stayed English under ko-KR:\n%s", menuLabels)
	}
	if !strings.Contains(menuLabels, "$8") {
		t.Fatalf("ko-KR action menu lost the exact handle:\n%s", menuLabels)
	}
	// The same invocation under the default locale keeps the English wording,
	// so the assertion above is about localization and not about the strings
	// simply having been removed.
	english, englishPicker, _, _, _ := runtimePickerFixture(t, "1", []intpicker.Result{
		{Value: "runtime:session:$8"},
		{Closed: true},
	})
	if _, _, err := runRuntimePicker(t, english, "--socket", "primary"); err != nil {
		t.Fatalf("runtime diagnostics (en-US): %v", err)
	}
	if got := englishPicker.rendered[0].Title; got != "Runtime diagnostics" {
		t.Fatalf("en-US list title = %q", got)
	}
	if !strings.Contains(strings.Join(pickerLabels(actionMenu(t, englishPicker)), "\n"),
		"no Registry Project claims this session") {
		t.Fatalf("en-US action refusal missing")
	}
}
