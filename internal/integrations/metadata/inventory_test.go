package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// inventoryRunner records the exact argv of every tmux invocation and replays
// per-verb output. Recording the whole argv rather than a summary is the point:
// the transport prefix is what the isolation assertions are made against.
type inventoryRunner struct {
	calls   [][]string
	outputs map[string]string
	errs    map[string]error
}

func (r *inventoryRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	verb := inventoryVerb(args)
	if err, ok := r.errs[verb]; ok {
		return nil, err
	}
	return []byte(r.outputs[verb]), nil
}

// inventoryVerb names the tmux subcommand of one call, skipping the transport
// prefix so a fixture keyed by verb works for every socket shape.
func inventoryVerb(args []string) string {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-L", "-S":
			i++
		default:
			return args[i]
		}
	}
	return ""
}

// tmuxRow renders one list row the way tmux does: fields joined by the escaped
// unit separator the formats ask for.
func tmuxRow(fields ...string) string {
	return strings.Join(fields, escapedFieldSep)
}

func liveServerOutputs() map[string]string {
	return map[string]string{
		"show-options": "1\n",
		"list-sessions": strings.Join([]string{
			tmuxRow("$1", "alpha", "project-alpha", "alpha", "/src/alpha", "", ""),
			tmuxRow("$2", "home", "", "", "", "control", ""),
			tmuxRow("$3", "scratch-20260818-120000", "", "", "", "", "1"),
		}, "\n") + "\n",
		"list-windows": strings.Join([]string{
			tmuxRow("@1", "$1", "0", "editor", "win-alpha-1", "editor"),
			tmuxRow("@2", "$2", "0", "zsh", "", ""),
		}, "\n") + "\n",
		"list-panes": strings.Join([]string{
			tmuxRow("%1", "@1", "pane-alpha-1", "shell", "", "alpha"),
			tmuxRow("%2", "@1", "pane-alpha-agent", "claude", "claude", "claude"),
			tmuxRow("%3", "@2", "", "", "", "zsh"),
		}, "\n") + "\n",
	}
}

// TestInventoryObserverReadsOneBoundedSnapshotThroughOneSocket is the core adapter
// contract: a fixed query budget, every call pinned to the exact server, no write
// verb anywhere, and one observation per instance however often it is asked.
func TestInventoryObserverReadsOneBoundedSnapshotThroughOneSocket(t *testing.T) {
	t.Parallel()
	runner := &inventoryRunner{outputs: liveServerOutputs()}
	transport := resourcegraph.Transport{
		Kind: resourcegraph.TransportSocketName, Value: "projmux",
		Source: resourcegraph.TransportSourceSocketName,
	}
	observer := NewInventoryObserver(runner, transport)

	first := observer.Observe(context.Background())
	for range 4 {
		observer.Observe(context.Background())
	}

	if len(runner.calls) != 4 {
		t.Fatalf("tmux calls = %d, want the fixed budget of 4: %v", len(runner.calls), runner.calls)
	}
	for _, call := range runner.calls {
		if len(call) < 3 || call[0] != "tmux" || call[1] != "-L" || call[2] != "projmux" {
			t.Fatalf("call %v is not pinned to -L projmux", call)
		}
		for _, arg := range call {
			switch arg {
			case "set-option", "set", "kill-server", "kill-session", "kill-pane", "kill-window",
				"new-session", "new-window", "split-window", "rename-window", "rename-session",
				"send-keys", "run-shell", "respawn-pane":
				t.Fatalf("observation issued the mutating verb %q: %v", arg, call)
			}
		}
	}

	if first.HostMode != resourcegraph.HostModeAppOwned {
		t.Fatalf("host mode = %q, want app-owned", first.HostMode)
	}
	if len(first.Unavailable) != 0 {
		t.Fatalf("healthy observation reported %+v", first.Unavailable)
	}
	if len(first.Sessions) != 3 || len(first.Windows) != 2 || len(first.Panes) != 3 {
		t.Fatalf("observed %d sessions / %d windows / %d panes",
			len(first.Sessions), len(first.Windows), len(first.Panes))
	}
	want := resourcegraph.Session{
		ID: "$1", Name: "alpha", ProjectUID: "project-alpha", ProjectName: "alpha", Root: "/src/alpha",
	}
	if first.Sessions[0] != want {
		t.Fatalf("session[0] = %+v, want %+v", first.Sessions[0], want)
	}
	if first.Sessions[1].Role != resourcegraph.ControlSessionRole || first.Sessions[1].Ephemeral {
		t.Fatalf("control session = %+v", first.Sessions[1])
	}
	if !first.Sessions[2].Ephemeral || first.Sessions[2].Role != "" {
		t.Fatalf("ephemeral session = %+v", first.Sessions[2])
	}
	wantWindow := resourcegraph.Window{
		ID: "@1", SessionID: "$1", Index: "0", DisplayName: "editor",
		UID: "win-alpha-1", MirroredName: "editor",
	}
	if first.Windows[0] != wantWindow {
		t.Fatalf("window[0] = %+v, want %+v", first.Windows[0], wantWindow)
	}
	wantPane := resourcegraph.Pane{
		ID: "%2", WindowID: "@1", UID: "pane-alpha-agent",
		MirroredName: "claude", AgentProvider: "claude", Title: "claude",
	}
	if first.Panes[1] != wantPane {
		t.Fatalf("pane[1] = %+v, want %+v", first.Panes[1], wantPane)
	}

	// The memoized value must not be reachable for mutation by a caller.
	first.Sessions[0].ProjectUID = "rewritten"
	if again := observer.Observe(context.Background()); again.Sessions[0].ProjectUID != "project-alpha" {
		t.Fatalf("caller mutation leaked into the memoized observation: %+v", again.Sessions[0])
	}
}

// TestInventoryCallBudgetIsIndependentOfServerSize is the reason the budget is a
// fixed set of list queries: a big machine must not cost more reads than a small
// one.
func TestInventoryCallBudgetIsIndependentOfServerSize(t *testing.T) {
	t.Parallel()
	big := liveServerOutputs()
	var sessions, windows, panes []string
	for i := range 200 {
		sessions = append(sessions, tmuxRow(fmt.Sprintf("$%d", i), fmt.Sprintf("s%d", i), "", "", "", "", ""))
		windows = append(windows, tmuxRow(fmt.Sprintf("@%d", i), fmt.Sprintf("$%d", i), "0", "zsh", "", ""))
		panes = append(panes, tmuxRow(fmt.Sprintf("%%%d", i), fmt.Sprintf("@%d", i), "", "", "", ""))
	}
	big["list-sessions"] = strings.Join(sessions, "\n") + "\n"
	big["list-windows"] = strings.Join(windows, "\n") + "\n"
	big["list-panes"] = strings.Join(panes, "\n") + "\n"

	transport := resourcegraph.Transport{Kind: resourcegraph.TransportSocketName, Value: "projmux"}
	small := &inventoryRunner{outputs: liveServerOutputs()}
	NewInventoryObserver(small, transport).Observe(context.Background())
	large := &inventoryRunner{outputs: big}
	observed := NewInventoryObserver(large, transport).Observe(context.Background())

	if len(small.calls) != len(large.calls) {
		t.Fatalf("call count scaled with server size: %d vs %d", len(small.calls), len(large.calls))
	}
	if len(observed.Sessions) != 200 || len(observed.Panes) != 200 {
		t.Fatalf("large server observed %d sessions / %d panes", len(observed.Sessions), len(observed.Panes))
	}
}

// TestFourTransportModes is the transport matrix: the two supported hosts, the
// absent-transport case that must not touch tmux at all, and an explicit target
// that must not read a sibling socket.
func TestFourTransportModes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		transport  resourcegraph.Transport
		marker     string
		wantPrefix []string
		wantHost   resourcegraph.HostMode
		wantCalls  int
	}{
		{
			name:      "app-owned socket name",
			transport: resourcegraph.Transport{Kind: resourcegraph.TransportSocketName, Value: "projmux"},
			marker:    "1\n", wantPrefix: []string{"-L", "projmux"},
			wantHost: resourcegraph.HostModeAppOwned, wantCalls: 4,
		},
		{
			name:      "standalone host on the operator's own server",
			transport: resourcegraph.Transport{Kind: resourcegraph.TransportSocketName, Value: "default"},
			marker:    "", wantPrefix: []string{"-L", "default"},
			wantHost: resourcegraph.HostModeStandalone, wantCalls: 4,
		},
		{
			name:      "explicit absolute socket path",
			transport: resourcegraph.Transport{Kind: resourcegraph.TransportSocketPath, Value: "/tmp/pmx/tmux-1000/pmx-a"},
			marker:    "1\n", wantPrefix: []string{"-S", "/tmp/pmx/tmux-1000/pmx-a"},
			wantHost: resourcegraph.HostModeAppOwned, wantCalls: 4,
		},
		{
			name:      "no transport issues no tmux call at all",
			transport: resourcegraph.Transport{Kind: resourcegraph.TransportNone},
			wantHost:  resourcegraph.HostModeUnknown, wantCalls: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			outputs := liveServerOutputs()
			outputs["show-options"] = test.marker
			runner := &inventoryRunner{outputs: outputs}
			observed := NewInventoryObserver(runner, test.transport).Observe(context.Background())

			if len(runner.calls) != test.wantCalls {
				t.Fatalf("calls = %d, want %d: %v", len(runner.calls), test.wantCalls, runner.calls)
			}
			if observed.HostMode != test.wantHost {
				t.Fatalf("host mode = %q, want %q", observed.HostMode, test.wantHost)
			}
			if test.wantCalls == 0 {
				for _, scope := range resourcegraph.Scopes() {
					if observed.Available(scope) {
						t.Fatalf("scope %s reported available with no transport", scope)
					}
				}
				if len(observed.Sessions)+len(observed.Windows)+len(observed.Panes) != 0 {
					t.Fatalf("transport-free observation invented objects: %+v", observed)
				}
				return
			}
			for _, call := range runner.calls {
				if call[1] != test.wantPrefix[0] || call[2] != test.wantPrefix[1] {
					t.Fatalf("call %v is not routed through %v", call, test.wantPrefix)
				}
			}
		})
	}
}

// TestTwoSocketsStayIsolated pins the sibling-socket property directly: two
// observers over the same runner never borrow each other's target, and neither
// ever issues an unprefixed call that would land on the default server.
func TestTwoSocketsStayIsolated(t *testing.T) {
	t.Parallel()
	runner := &inventoryRunner{outputs: liveServerOutputs()}
	first := NewInventoryObserver(runner, resourcegraph.Transport{
		Kind: resourcegraph.TransportSocketPath, Value: "/tmp/pmx/tmux-1000/pmx-a"})
	second := NewInventoryObserver(runner, resourcegraph.Transport{
		Kind: resourcegraph.TransportSocketPath, Value: "/tmp/pmx/tmux-1000/pmx-b"})

	first.Observe(context.Background())
	firstCalls := len(runner.calls)
	second.Observe(context.Background())

	for i, call := range runner.calls {
		want := "/tmp/pmx/tmux-1000/pmx-a"
		if i >= firstCalls {
			want = "/tmp/pmx/tmux-1000/pmx-b"
		}
		if call[1] != "-S" || call[2] != want {
			t.Fatalf("call %d %v did not target %s", i, call, want)
		}
		other := "/tmp/pmx/tmux-1000/pmx-b"
		if i >= firstCalls {
			other = "/tmp/pmx/tmux-1000/pmx-a"
		}
		if strings.Contains(strings.Join(call, " "), other) {
			t.Fatalf("call %d %v read the sibling socket %s", i, call, other)
		}
	}
}

// TestOneFailedQueryDegradesOnlyItsOwnScope is acceptance for partial failure: the
// surviving halves keep their observation, the failed one carries a reason, and
// nothing is reported as live that was not seen.
func TestOneFailedQueryDegradesOnlyItsOwnScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		verb  string
		scope resourcegraph.Scope
	}{
		{verb: "list-sessions", scope: resourcegraph.ScopeSessions},
		{verb: "list-windows", scope: resourcegraph.ScopeWindows},
		{verb: "list-panes", scope: resourcegraph.ScopePanes},
		{verb: "show-options", scope: resourcegraph.ScopeHostMode},
	}
	for _, test := range tests {
		t.Run(test.verb, func(t *testing.T) {
			t.Parallel()
			runner := &inventoryRunner{
				outputs: liveServerOutputs(),
				errs:    map[string]error{test.verb: errors.New("tmux: exit status 1")},
			}
			observed := NewInventoryObserver(runner, resourcegraph.Transport{
				Kind: resourcegraph.TransportSocketName, Value: "projmux"}).Observe(context.Background())

			failure, unavailable := observed.Unavailability(test.scope)
			if !unavailable || failure.Reason == "" {
				t.Fatalf("scope %s carries no failure: %+v", test.scope, observed.Unavailable)
			}
			if !strings.Contains(failure.Reason, "exit status 1") {
				t.Fatalf("reason %q drops the underlying cause", failure.Reason)
			}
			if len(observed.Unavailable) != 1 {
				t.Fatalf("one failed query degraded %d scopes: %+v", len(observed.Unavailable), observed.Unavailable)
			}
			if len(runner.calls) != 4 {
				t.Fatalf("calls = %d, want the full budget even with one failure", len(runner.calls))
			}
			if test.scope == resourcegraph.ScopeHostMode && observed.HostMode != resourcegraph.HostModeUnknown {
				t.Fatalf("unreadable ownership resolved to %q", observed.HostMode)
			}
			if test.scope != resourcegraph.ScopeSessions && len(observed.Sessions) == 0 {
				t.Fatalf("a %s failure discarded the session observation", test.verb)
			}
			if test.scope != resourcegraph.ScopePanes && len(observed.Panes) == 0 {
				t.Fatalf("a %s failure discarded the pane observation", test.verb)
			}
		})
	}
}

// TestAbsentServerIsNothingLiveRatherThanUnknown separates the two failures an
// operator experiences differently: a socket with no server behind it is definite
// knowledge that nothing is running, and it costs one call to learn.
func TestAbsentServerIsNothingLiveRatherThanUnknown(t *testing.T) {
	t.Parallel()
	runner := &inventoryRunner{
		outputs: liveServerOutputs(),
		errs: map[string]error{"show-options": errors.New(
			"run tmux: exit status 1: no server running on /tmp/tmux-1000/pmx")},
	}
	observed := NewInventoryObserver(runner, resourcegraph.Transport{
		Kind: resourcegraph.TransportSocketName, Value: "pmx"}).Observe(context.Background())

	if len(runner.calls) != 1 {
		t.Fatalf("absent server cost %d calls, want 1: %v", len(runner.calls), runner.calls)
	}
	if _, unavailable := observed.Unavailability(resourcegraph.ScopeHostMode); !unavailable {
		t.Fatalf("absent server left host ownership available")
	}
	for _, scope := range []resourcegraph.Scope{
		resourcegraph.ScopeSessions, resourcegraph.ScopeWindows, resourcegraph.ScopePanes} {
		if !observed.Available(scope) {
			t.Fatalf("scope %s was marked unavailable on a server that is provably not running", scope)
		}
	}
	graph := resourcegraph.Resolve(coremetadata.NewRegistry(), observed)
	if len(graph.Runtime) != 0 {
		t.Fatalf("absent server produced %d runtime objects", len(graph.Runtime))
	}
}

// TestObservedInventoryResolvesToTheSameManagedRowsOnBothHosts is the end-to-end
// half of the host-parity acceptance: identical tmux output under app-owned and
// standalone hosting yields identical managed rows, and only the attribution of
// objects projmux does not own differs.
func TestObservedInventoryResolvesToTheSameManagedRowsOnBothHosts(t *testing.T) {
	t.Parallel()
	registry := coremetadata.NewRegistry()
	registry.Projects = []coremetadata.Project{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindProject,
		Metadata: coremetadata.ObjectMeta{UID: "project-alpha", Name: "alpha"},
		Spec:     coremetadata.ProjectSpec{Root: "/src/alpha"},
	}}
	registry.Windows = []coremetadata.Window{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindWindow,
		Metadata: coremetadata.ObjectMeta{UID: "win-alpha-1", Name: "editor",
			OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: "project-alpha"}},
	}}
	registry.Panes = []coremetadata.Pane{{
		APIVersion: coremetadata.APIVersion, Kind: coremetadata.KindPane,
		Metadata: coremetadata.ObjectMeta{UID: "pane-alpha-1", Name: "shell",
			OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-alpha-1"}},
		Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell},
	}}

	rows := func(marker string) (string, resourcegraph.Graph) {
		outputs := liveServerOutputs()
		outputs["show-options"] = marker
		runner := &inventoryRunner{outputs: outputs}
		observed := NewInventoryObserver(runner, resourcegraph.Transport{
			Kind: resourcegraph.TransportSocketName, Value: "projmux"}).Observe(context.Background())
		graph := resourcegraph.Resolve(registry, observed)
		encoded, err := json.Marshal([]any{graph.Projects, graph.Windows, graph.Panes, graph.Agents})
		if err != nil {
			t.Fatalf("marshal rows: %v", err)
		}
		return string(encoded), graph
	}

	appRows, app := rows("1\n")
	standaloneRows, standalone := rows("")
	if appRows != standaloneRows {
		t.Fatalf("managed rows differ between hosts:\n app=%s\n std=%s", appRows, standaloneRows)
	}
	if app.HostMode == standalone.HostMode {
		t.Fatalf("both fixtures resolved to host mode %q", app.HostMode)
	}
	// The Home control session is control on the app-owned host and refused on
	// the operator's own server, which is the only difference the marker buys.
	var appControl, standaloneControl int
	for _, node := range app.RuntimeOfClass(resourcegraph.ClassControl) {
		if node.Ref.ID == "$2" {
			appControl++
		}
	}
	for _, node := range standalone.RuntimeOfClass(resourcegraph.ClassControl) {
		if node.Ref.ID == "$2" {
			standaloneControl++
		}
	}
	if appControl != 1 || standaloneControl != 0 {
		t.Fatalf("control attribution app=%d standalone=%d, want 1/0", appControl, standaloneControl)
	}
}

// TestTransportRunnerRefusesEveryUnroutableCall keeps the routing layer from
// degrading into a default-server probe when it is misconfigured.
func TestTransportRunnerRefusesEveryUnroutableCall(t *testing.T) {
	t.Parallel()
	runner := &inventoryRunner{outputs: liveServerOutputs()}
	tests := []struct {
		name    string
		routed  transportRunner
		command string
	}{
		{
			name:    "absent transport",
			routed:  transportRunner{runner: runner, transport: resourcegraph.Transport{Kind: resourcegraph.TransportNone}},
			command: "tmux",
		},
		{
			name: "non-tmux executable",
			routed: transportRunner{runner: runner, transport: resourcegraph.Transport{
				Kind: resourcegraph.TransportSocketName, Value: "projmux"}},
			command: "sh",
		},
		{
			name: "missing runner",
			routed: transportRunner{transport: resourcegraph.Transport{
				Kind: resourcegraph.TransportSocketName, Value: "projmux"}},
			command: "tmux",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := len(runner.calls)
			if _, err := test.routed.Run(context.Background(), test.command, "list-sessions"); err == nil {
				t.Fatalf("routed an unroutable call")
			}
			if len(runner.calls) != before {
				t.Fatalf("refused call still reached the runner: %v", runner.calls)
			}
		})
	}
}
