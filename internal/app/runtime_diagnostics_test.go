package app

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// The Runtime diagnostics escape hatch, at the route boundary.
//
// Every fixture here runs the production observation adapter against a fake
// tmux server, so what is asserted is the argv projmux actually issues -- which
// is the only way "this read writes nothing and never looks at a second socket"
// can be a checked property rather than a claim about the code's shape.

const (
	runtimeFixtureProject = "project-alpha"
	runtimeFixtureWindow  = "win-alpha-1"
	runtimeFixturePane    = "pane-alpha-1"
)

// runtimeFixtureRegistry is one Project with a Window and its shell Pane.
func runtimeFixtureRegistry() coremetadata.Registry {
	created := time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC)
	meta := func(uid, name string, owner *coremetadata.OwnerRef) coremetadata.ObjectMeta {
		return coremetadata.ObjectMeta{UID: uid, Name: name, OwnerRef: owner, CreatedAt: created}
	}
	own := func(kind coremetadata.Kind, uid string) *coremetadata.OwnerRef {
		return &coremetadata.OwnerRef{Kind: kind, UID: uid}
	}
	registry := coremetadata.NewRegistry()
	registry.Projects = []coremetadata.Project{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
		Metadata: meta(runtimeFixtureProject, "alpha", nil),
		Spec:     coremetadata.ProjectSpec{Root: "/src/alpha", PrimaryWindowRef: runtimeFixtureWindow},
	}}
	registry.Windows = []coremetadata.Window{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: meta(runtimeFixtureWindow, "editor", own(coremetadata.KindProject, runtimeFixtureProject)),
		Spec:     coremetadata.WindowSpec{AnchorPaneRef: runtimeFixturePane},
	}}
	registry.Panes = []coremetadata.Pane{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: meta(runtimeFixturePane, "shell", own(coremetadata.KindWindow, runtimeFixtureWindow)),
		Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell},
	}}
	registry.NameReservations = []coremetadata.NameReservation{
		{Kind: coremetadata.KindProject, Name: "alpha", UID: runtimeFixtureProject},
		{Scope: runtimeFixtureProject, Kind: coremetadata.KindWindow, Name: "editor", UID: runtimeFixtureWindow},
		{Scope: runtimeFixtureProject, Kind: coremetadata.KindPane, Name: "shell", UID: runtimeFixturePane},
	}
	return registry
}

// runtimeFixtureServer is one server carrying every class the surface must
// distinguish: the managed Project session (a reconcile no-op), the Home
// control session, a scratch session, an unmarked window and pane, and a window
// mirroring a uid this Registry does not contain.
func runtimeFixtureServer(host string) *fakeTmux {
	server := newFakeTmux()
	server.appMarker = host
	server.socketPath = "/tmp/fake-tmux/primary"

	alpha := server.addSession("alpha")
	alpha.opts[tmuxopts.ProjectUIDSession] = runtimeFixtureProject
	alpha.opts[tmuxopts.ProjectNameSession] = "alpha"
	alpha.windows[0].name = "editor"
	alpha.windows[0].opts[tmuxopts.WindowUID] = runtimeFixtureWindow
	alpha.windows[0].panes[0].opts[tmuxopts.PaneUID] = runtimeFixturePane

	notes := &fakeTmuxWindow{id: server.mint("@"), name: "notes", opts: map[string]string{}}
	notes.panes = append(notes.panes, newFakeTmuxPane(server.mint("%")))
	alpha.windows = append(alpha.windows, notes)

	ghost := &fakeTmuxWindow{id: server.mint("@"), name: "ghost", opts: map[string]string{
		tmuxopts.WindowUID: "win-not-in-registry",
	}}
	ghost.panes = append(ghost.panes, newFakeTmuxPane(server.mint("%")))
	alpha.windows = append(alpha.windows, ghost)

	home := server.addSession("Home")
	home.opts[tmuxopts.SessionRole] = resourcegraph.ControlSessionRole

	scratch := server.addSession("scratch")
	scratch.opts[tmuxopts.EphemeralSession] = resourcegraph.EphemeralMarker
	return server
}

// runtimeFixture wires the production observer over a routed fake so both the
// primary and a sibling server exist and only one of them may be touched.
func runtimeFixture(t *testing.T, host string) (*getCommand, *fakeTmux, *fakeTmux, *routedTmuxRunner) {
	t.Helper()
	primary := runtimeFixtureServer(host)
	sibling := runtimeFixtureServer(host)
	sibling.socketPath = "/tmp/fake-tmux/sibling"
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{
		"-L\x00primary": primary,
		"-L\x00sibling": sibling,
	}}
	registry := runtimeFixtureRegistry()
	command := newGetCommand()
	command.runtimeDiag = &runtimeDiagnosticsReader{
		runner:       runner,
		lookupEnv:    func(string) string { return "" },
		loadRegistry: func() (coremetadata.Registry, error) { return registry, nil },
		observe: func(ctx context.Context, transport resourcegraph.Transport) resourcegraph.Inventory {
			return intmetadata.NewInventoryObserver(runner, transport).Observe(ctx)
		},
	}
	return command, primary, sibling, runner
}

func runGetRuntime(t *testing.T, command *getCommand, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := command.Run(append([]string{"runtime"}, args...), &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func decodeRuntimeReport(t *testing.T, payload string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		t.Fatalf("decode runtime report: %v\n%s", err, payload)
	}
	return out
}

func runtimeReportItems(t *testing.T, payload string) []map[string]any {
	t.Helper()
	raw, ok := decodeRuntimeReport(t, payload)["items"].([]any)
	if !ok {
		t.Fatalf("runtime report has no items array:\n%s", payload)
	}
	items := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		item, ok := entry.(map[string]any)
		if !ok {
			t.Fatalf("runtime report item is not an object:\n%s", payload)
		}
		items = append(items, item)
	}
	return items
}

// TestGetRuntimeProjectsEveryObjectOnTheExactServer is acceptance (1): every
// live Session, Window, and Pane is printed with its attribution and its exact
// handle, managed no-ops included.
func TestGetRuntimeProjectsEveryObjectOnTheExactServer(t *testing.T) {
	t.Parallel()

	command, primary, _, _ := runtimeFixture(t, "1")
	for _, test := range []struct {
		kind  string
		count int
		want  map[string]string
	}{
		{kind: "sessions", count: 3, want: map[string]string{
			"$1": "managed", "$8": "control", "$11": "ephemeral",
		}},
		{kind: "windows", count: 5, want: map[string]string{
			"@2": "managed", "@4": "unattributed", "@6": "recoverable",
			"@9": "unattributed", "@12": "unattributed",
		}},
		{kind: "panes", count: 5, want: map[string]string{
			"%3": "managed", "%5": "unattributed", "%7": "unattributed",
			"%10": "unattributed", "%13": "unattributed",
		}},
	} {
		stdout, stderr, err := runGetRuntime(t, command, test.kind, "--socket", "primary", "-o", "json")
		if err != nil || stderr != "" {
			t.Fatalf("get runtime %s: err=%v stderr=%q", test.kind, err, stderr)
		}
		items := runtimeReportItems(t, stdout)
		if len(items) != test.count {
			t.Fatalf("get runtime %s returned %d items, want %d:\n%s", test.kind, len(items), test.count, stdout)
		}
		byID := map[string]map[string]any{}
		for _, item := range items {
			id, _ := item["id"].(string)
			byID[id] = item
			if item["class"] == "" || item["class"] == nil {
				t.Fatalf("get runtime %s item %q carries no class:\n%s", test.kind, id, stdout)
			}
			if reason, _ := item["reason"].(string); strings.TrimSpace(reason) == "" {
				t.Fatalf("get runtime %s item %q carries no reason:\n%s", test.kind, id, stdout)
			}
		}
		for id, class := range test.want {
			item, ok := byID[id]
			if !ok {
				t.Fatalf("get runtime %s is missing %q:\n%s", test.kind, id, stdout)
			}
			if item["class"] != class {
				t.Fatalf("get runtime %s %q class = %v, want %q", test.kind, id, item["class"], class)
			}
		}
	}
	// The managed rows are the reconcile no-ops, and they are exactly what a
	// drift-only report would have omitted.
	if primary.socketPath != "/tmp/fake-tmux/primary" {
		t.Fatalf("fixture socket drifted: %q", primary.socketPath)
	}
}

// TestGetRuntimeReadsOneSocketAndWritesNothing is acceptance (2): the read
// issues the bounded query set through one transport and no write verb, and the
// second server is never contacted.
func TestGetRuntimeReadsOneSocketAndWritesNothing(t *testing.T) {
	t.Parallel()

	command, primary, sibling, runner := runtimeFixture(t, "1")
	before := primary.state()
	for _, kind := range []string{"sessions", "windows", "panes"} {
		if _, _, err := runGetRuntime(t, command, kind, "--socket", "primary", "-o", "json"); err != nil {
			t.Fatalf("get runtime %s: %v", kind, err)
		}
	}
	if primary.state() != before {
		t.Fatalf("runtime read mutated the server:\n--- before ---\n%s\n--- after ---\n%s", before, primary.state())
	}
	if len(sibling.calls) != 0 {
		t.Fatalf("runtime read contacted the sibling socket: %v", sibling.calls)
	}
	for _, call := range runner.calls {
		if call.flag != "-L" || call.value != "primary" {
			t.Fatalf("runtime read routed to %s %s, want -L primary", call.flag, call.value)
		}
	}
	writeVerbs := []string{
		"set-option", "rename-window", "new-session", "new-window", "split-window",
		"kill-session", "kill-window", "kill-pane", "set-environment", "switch-client", "attach-session",
	}
	readVerbs := map[string]bool{}
	for _, call := range primary.calls {
		if len(call) == 0 {
			continue
		}
		if slices.Contains(writeVerbs, call[0]) {
			t.Fatalf("runtime read issued the write verb %q: %v", call[0], call)
		}
		readVerbs[call[0]] = true
	}
	want := []string{"show-options", "list-sessions", "list-windows", "list-panes"}
	for _, verb := range want {
		if !readVerbs[verb] {
			t.Fatalf("runtime read never issued %q; observed %v", verb, readVerbs)
		}
	}
	if len(readVerbs) != len(want) {
		t.Fatalf("runtime read issued unexpected verbs: %v, want exactly %v", readVerbs, want)
	}
	// Three reads of three kinds cost three bounded observations, not one per
	// row: four queries each.
	if got, want := len(primary.calls), 12; got != want {
		t.Fatalf("runtime reads issued %d tmux calls, want %d", got, want)
	}
}

// TestGetRuntimeAttributionSplitsHomeUnknownUIDAndOrdinaryObjects is
// acceptance (3), stated as the three answers that must stay different.
func TestGetRuntimeAttributionSplitsHomeUnknownUIDAndOrdinaryObjects(t *testing.T) {
	t.Parallel()

	command, _, _, _ := runtimeFixture(t, "1")
	sessions, _, err := runGetRuntime(t, command, "sessions", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("get runtime sessions: %v", err)
	}
	windows, _, err := runGetRuntime(t, command, "windows", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("get runtime windows: %v", err)
	}

	home := runtimeItemByID(t, runtimeReportItems(t, sessions), "$8")
	if home["class"] != "control" {
		t.Fatalf("Home class = %v, want control", home["class"])
	}
	if home["resource"] != nil {
		t.Fatalf("Home is bound to %v; the control session is not a Project", home["resource"])
	}

	ghost := runtimeItemByID(t, runtimeReportItems(t, windows), "@6")
	if ghost["class"] != "recoverable" {
		t.Fatalf("unknown-uid window class = %v, want recoverable", ghost["class"])
	}
	if ghost["uid"] != "win-not-in-registry" {
		t.Fatalf("unknown-uid window uid = %v", ghost["uid"])
	}
	if ghost["resource"] != nil {
		t.Fatalf("unknown-uid window is bound to %v, want nothing", ghost["resource"])
	}

	ordinary := runtimeItemByID(t, runtimeReportItems(t, windows), "@4")
	if ordinary["class"] != "unattributed" {
		t.Fatalf("uid-less window class = %v, want unattributed", ordinary["class"])
	}
	if _, present := ordinary["uid"]; present {
		t.Fatalf("uid-less window reports a uid: %v", ordinary["uid"])
	}
}

func runtimeItemByID(t *testing.T, items []map[string]any, id string) map[string]any {
	t.Helper()
	for _, item := range items {
		if item["id"] == id {
			return item
		}
	}
	t.Fatalf("no runtime item %q in %d items", id, len(items))
	return nil
}

// TestGetRuntimeStandaloneKeepsManagedRowsAndRefusesUnmarkedObjects is
// acceptance (4)'s standalone half: the same Registry produces the same managed
// bindings on the operator's own server, and everything projmux did not mark
// there stays the operator's.
func TestGetRuntimeStandaloneKeepsManagedRowsAndRefusesUnmarkedObjects(t *testing.T) {
	t.Parallel()

	appOwned, _, _, _ := runtimeFixture(t, "1")
	standalone, _, _, _ := runtimeFixture(t, "")

	appWindows, _, err := runGetRuntime(t, appOwned, "windows", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("app-owned get runtime windows: %v", err)
	}
	standaloneWindows, _, err := runGetRuntime(t, standalone, "windows", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("standalone get runtime windows: %v", err)
	}

	if got := decodeRuntimeReport(t, appWindows)["hostMode"]; got != "app-owned" {
		t.Fatalf("app-owned host mode = %v", got)
	}
	if got := decodeRuntimeReport(t, standaloneWindows)["hostMode"]; got != "standalone" {
		t.Fatalf("standalone host mode = %v", got)
	}
	// Identical managed identity under both hosts.
	for _, payload := range []string{appWindows, standaloneWindows} {
		managed := runtimeItemByID(t, runtimeReportItems(t, payload), "@2")
		if managed["class"] != "managed" {
			t.Fatalf("managed window class = %v under host %v", managed["class"], decodeRuntimeReport(t, payload)["hostMode"])
		}
		resource, _ := managed["resource"].(map[string]any)
		if resource["uid"] != runtimeFixtureWindow {
			t.Fatalf("managed window resource = %v, want %q", managed["resource"], runtimeFixtureWindow)
		}
	}
	// The Home marker is refused on a server projmux does not own, and the
	// unmarked objects there belong to the operator.
	standaloneSessions, _, err := runGetRuntime(t, standalone, "sessions", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("standalone get runtime sessions: %v", err)
	}
	if got := runtimeItemByID(t, runtimeReportItems(t, standaloneSessions), "$8")["class"]; got != "foreign" {
		t.Fatalf("standalone Home marker class = %v, want foreign", got)
	}
	if got := runtimeItemByID(t, runtimeReportItems(t, standaloneWindows), "@4")["class"]; got != "unattributed" {
		t.Fatalf("standalone window inside a managed session class = %v, want unattributed", got)
	}
}

// TestGetRuntimeOutsideTmuxReturnsTheUnavailableProjection is acceptance (4)'s
// no-transport half: the read succeeds, names every scope it could not take,
// and issues no tmux call at all.
func TestGetRuntimeOutsideTmuxReturnsTheUnavailableProjection(t *testing.T) {
	t.Parallel()

	command, primary, sibling, _ := runtimeFixture(t, "1")
	for _, kind := range []string{"sessions", "windows", "panes"} {
		stdout, stderr, err := runGetRuntime(t, command, kind, "-o", "json")
		if err != nil || stderr != "" {
			t.Fatalf("get runtime %s outside tmux: err=%v stderr=%q", kind, err, stderr)
		}
		report := decodeRuntimeReport(t, stdout)
		if report["hostMode"] != "unknown" {
			t.Fatalf("no-transport host mode = %v, want unknown", report["hostMode"])
		}
		transport, _ := report["transport"].(map[string]any)
		if transport["kind"] != "none" || transport["source"] != "none" {
			t.Fatalf("no-transport transport = %v", report["transport"])
		}
		if items := runtimeReportItems(t, stdout); len(items) != 0 {
			t.Fatalf("no-transport read invented %d items:\n%s", len(items), stdout)
		}
		unavailable, _ := report["unavailable"].([]any)
		if len(unavailable) != len(resourcegraph.Scopes()) {
			t.Fatalf("no-transport unavailable = %v, want every scope", report["unavailable"])
		}
	}
	if len(primary.calls) != 0 || len(sibling.calls) != 0 {
		t.Fatalf("no-transport read probed a server: primary=%v sibling=%v", primary.calls, sibling.calls)
	}
}

// TestGetRuntimeHumanProjectionIsAStableTable is the golden half: the default
// projection always states which server answered, and its columns are pinned.
func TestGetRuntimeHumanProjectionIsAStableTable(t *testing.T) {
	t.Parallel()

	command, _, _, _ := runtimeFixture(t, "1")
	stdout, stderr, err := runGetRuntime(t, command, "sessions", "--socket", "primary", "-o", "wide")
	if err != nil || stderr != "" {
		t.Fatalf("get runtime sessions: err=%v stderr=%q", err, stderr)
	}
	want := strings.Join([]string{
		"host app-owned  transport tmux -L primary  source explicit-socket-name",
		"SESSION  NAME     CLASS      UID            RESOURCE       REASON",
		"$1       alpha    managed    project-alpha  Project/alpha  bound to Project/alpha by mirrored uid project-alpha",
		"$8       Home     control    -              -              app-owned session carrying role control",
		"$11      scratch  ephemeral  -              -              auto-attach ephemeral session, never part of the Project hierarchy",
		"",
	}, "\n")
	if stdout != want {
		t.Fatalf("human projection drifted:\n--- got ---\n%s\n--- want ---\n%s", stdout, want)
	}
}

// TestGetRuntimeHumanProjectionStatesWhyAnEmptyListIsEmpty keeps the two empty
// answers apart on the human surface too.
func TestGetRuntimeHumanProjectionStatesWhyAnEmptyListIsEmpty(t *testing.T) {
	t.Parallel()

	command, _, _, _ := runtimeFixture(t, "1")
	stdout, _, err := runGetRuntime(t, command, "panes")
	if err != nil {
		t.Fatalf("get runtime panes outside tmux: %v", err)
	}
	if !strings.HasPrefix(stdout, "host unknown  transport no tmux transport  source none\n") {
		t.Fatalf("no-transport header missing:\n%s", stdout)
	}
	for _, scope := range resourcegraph.Scopes() {
		if !strings.Contains(stdout, "unavailable "+string(scope)+": ") {
			t.Fatalf("no-transport human projection omits scope %q:\n%s", scope, stdout)
		}
	}
	if strings.Contains(stdout, "PANE") {
		t.Fatalf("no-transport human projection printed a table header:\n%s", stdout)
	}
}

// TestGetRuntimeRefusesMalformedInvocations pins the error surface: a bad kind,
// a positional argument, a projection the route does not own, and two socket
// flags at once are all usage errors that write nothing to stdout.
func TestGetRuntimeRefusesMalformedInvocations(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no kind", args: nil, want: "get runtime requires an object kind"},
		{name: "unknown kind", args: []string{"agents"}, want: "get runtime agents is not available"},
		{name: "singular", args: []string{"session"}, want: "get runtime session is not available"},
		{name: "positional", args: []string{"panes", "extra"}, want: "does not accept positional arguments"},
		{name: "registry projection", args: []string{"panes", "-o", "uid"}, want: "accepted values: wide, json, none"},
		{name: "route-local projection", args: []string{"panes", "-o", "cwd"}, want: "accepted values: wide, json, none"},
		{name: "both sockets", args: []string{"panes", "--socket", "a", "--socket-path", "/tmp/b"}, want: "mutually exclusive"},
		{name: "relative socket path", args: []string{"panes", "--socket-path", "relative"}, want: "must be absolute"},
	} {
		command, primary, _, _ := runtimeFixture(t, "1")
		stdout, _, err := runGetRuntime(t, command, test.args...)
		if err == nil {
			t.Fatalf("%s: expected a refusal, got stdout %q", test.name, stdout)
		}
		if !strings.Contains(err.Error(), test.want) {
			t.Fatalf("%s: error = %q, want it to mention %q", test.name, err, test.want)
		}
		if !IsUsageError(err) {
			t.Fatalf("%s: error %v is not a usage error", test.name, err)
		}
		if stdout != "" {
			t.Fatalf("%s: refusal wrote to stdout: %q", test.name, stdout)
		}
		if len(primary.calls) != 0 {
			t.Fatalf("%s: refusal contacted tmux: %v", test.name, primary.calls)
		}
	}
}

// TestGetRuntimeNoneProjectionIsSilent pins the quiet-automation mode.
func TestGetRuntimeNoneProjectionIsSilent(t *testing.T) {
	t.Parallel()

	command, _, _, _ := runtimeFixture(t, "1")
	stdout, stderr, err := runGetRuntime(t, command, "panes", "--socket", "primary", "-o", "none")
	if err != nil || stdout != "" || stderr != "" {
		t.Fatalf("-o none wrote output: err=%v stdout=%q stderr=%q", err, stdout, stderr)
	}
}

// TestGetRuntimeInheritsTheAttachedSocketWhenNoFlagIsGiven proves the inherited
// $TMUX path resolves to the same exact server as the explicit flag, and still
// touches nothing else.
func TestGetRuntimeInheritsTheAttachedSocketWhenNoFlagIsGiven(t *testing.T) {
	t.Parallel()

	command, primary, sibling, runner := runtimeFixture(t, "1")
	command.runtimeDiag.lookupEnv = func(key string) string {
		if key == "TMUX" {
			return "/tmp/fake-tmux/primary,1234,0"
		}
		return ""
	}
	runner.servers["-S\x00/tmp/fake-tmux/primary"] = primary

	stdout, _, err := runGetRuntime(t, command, "sessions", "-o", "json")
	if err != nil {
		t.Fatalf("inherited-transport read: %v", err)
	}
	transport, _ := decodeRuntimeReport(t, stdout)["transport"].(map[string]any)
	if transport["kind"] != "socket-path" || transport["source"] != "inherited-tmux-env" {
		t.Fatalf("inherited transport = %v", transport)
	}
	if len(runtimeReportItems(t, stdout)) != 3 {
		t.Fatalf("inherited-transport read returned %d sessions:\n%s", len(runtimeReportItems(t, stdout)), stdout)
	}
	if len(sibling.calls) != 0 {
		t.Fatalf("inherited-transport read contacted the sibling socket: %v", sibling.calls)
	}
}

// TestGetRuntimeJSONSchemaIsStable is the schema guard: a consumer branches on
// these field names, so the envelope is pinned rather than described.
func TestGetRuntimeJSONSchemaIsStable(t *testing.T) {
	t.Parallel()

	command, _, _, _ := runtimeFixture(t, "1")
	stdout, _, err := runGetRuntime(t, command, "windows", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("get runtime windows: %v", err)
	}
	report := decodeRuntimeReport(t, stdout)
	for _, key := range []string{"apiVersion", "kind", "transport", "hostMode", "items"} {
		if _, ok := report[key]; !ok {
			t.Fatalf("runtime report is missing %q:\n%s", key, stdout)
		}
	}
	if report["apiVersion"] != coremetadata.APIVersion || report["kind"] != "RuntimeWindowList" {
		t.Fatalf("runtime envelope = (%v, %v)", report["apiVersion"], report["kind"])
	}
	managed := runtimeItemByID(t, runtimeReportItems(t, stdout), "@2")
	for _, key := range []string{"kind", "id", "target", "name", "class", "uid", "containerID", "sessionID", "resource", "reason"} {
		if _, ok := managed[key]; !ok {
			t.Fatalf("managed window row is missing %q:\n%s", key, stdout)
		}
	}
	if managed["target"] != "alpha:@2" {
		t.Fatalf("managed window target = %v, want the qualified coordinate alpha:@2", managed["target"])
	}
	// Repeating the read is byte-identical: nothing in the projection depends on
	// a clock, a map order, or a host path.
	repeat, _, err := runGetRuntime(t, command, "windows", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("repeat get runtime windows: %v", err)
	}
	if repeat != stdout {
		t.Fatalf("runtime report is not byte-stable:\n--- first ---\n%s\n--- second ---\n%s", stdout, repeat)
	}
}
