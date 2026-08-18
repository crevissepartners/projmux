package resourcegraph

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestResolvePreservesRegistryRowsWhenAScopeIsUnavailable is acceptance for the
// failure direction: an observation that could not be taken downgrades status and
// states why, and never removes a row or invents a live one.
func TestResolvePreservesRegistryRowsWhenAScopeIsUnavailable(t *testing.T) {
	t.Parallel()
	registry := testRegistry(t)
	full := Resolve(registry, liveInventory(HostModeAppOwned))

	tests := []struct {
		name         string
		scope        Scope
		drop         func(*Inventory)
		wantProject  Status
		wantWindow   Status
		wantPane     Status
		wantAgent    Status
		wantLiveRows int
	}{
		{
			name:  "sessions query failed",
			scope: ScopeSessions,
			drop:  func(inv *Inventory) { inv.Sessions = nil },
			// The Window and Pane uids are still exact evidence of their own
			// runtime, so only the Project loses its answer.
			wantProject: StatusUnknown, wantWindow: StatusLive, wantPane: StatusLive, wantAgent: StatusLive,
		},
		{
			name:  "windows query failed",
			scope: ScopeWindows,
			drop:  func(inv *Inventory) { inv.Windows = nil },
			// A pane whose tmux pane is provably there is still live even when
			// the window inventory could not be read.
			wantProject: StatusLive, wantWindow: StatusUnknown, wantPane: StatusLive, wantAgent: StatusLive,
		},
		{
			name:        "panes query failed",
			scope:       ScopePanes,
			drop:        func(inv *Inventory) { inv.Panes = nil },
			wantProject: StatusLive, wantWindow: StatusLive, wantPane: StatusUnknown, wantAgent: StatusUnknown,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			inventory := liveInventory(HostModeAppOwned)
			test.drop(&inventory)
			inventory = inventory.MarkUnavailable(test.scope, "tmux exited 1")
			graph := Resolve(registry, inventory)

			if len(graph.Projects) != len(full.Projects) ||
				len(graph.Windows) != len(full.Windows) ||
				len(graph.Panes) != len(full.Panes) ||
				len(graph.Agents) != len(full.Agents) {
				t.Fatalf("row counts changed under failure: %d/%d/%d/%d",
					len(graph.Projects), len(graph.Windows), len(graph.Panes), len(graph.Agents))
			}
			if got := projectNode(t, graph, "project-alpha").Status; got != test.wantProject {
				t.Fatalf("project status = %q, want %q", got, test.wantProject)
			}
			if got := windowNode(t, graph, "win-alpha-1").Status; got != test.wantWindow {
				t.Fatalf("window status = %q, want %q", got, test.wantWindow)
			}
			if got := paneNode(t, graph, "pane-alpha-1").Status; got != test.wantPane {
				t.Fatalf("pane status = %q, want %q", got, test.wantPane)
			}
			if got := agentNode(t, graph, "agent-alpha-1").Status; got != test.wantAgent {
				t.Fatalf("agent status = %q, want %q", got, test.wantAgent)
			}
			reason, unavailable := graph.reason(test.scope)
			if !unavailable || reason == "" {
				t.Fatalf("graph carries no reason for %s: %+v", test.scope, graph.Unavailable)
			}
			// A rootless Project still outranks an unreadable observation.
			if got := projectNode(t, graph, "project-gone").Status; got != StatusMissingRoot {
				t.Fatalf("rootless project status = %q, want missing-root", got)
			}
		})
	}
}

// reason is a test-local accessor over the graph's stated failures.
func (g Graph) reason(scope Scope) (string, bool) {
	for _, entry := range g.Unavailable {
		if entry.Scope == scope {
			return entry.Reason, true
		}
	}
	return "", false
}

// TestResolveWithoutTransportIsARegistryOnlySnapshot is the no-transport corner:
// every row survives, every runtime answer is unknown, and nothing is offline --
// because "offline" would be a claim about a server nobody looked at.
func TestResolveWithoutTransportIsARegistryOnlySnapshot(t *testing.T) {
	t.Parallel()
	registry := testRegistry(t)
	inventory := Inventory{Transport: Transport{Kind: TransportNone, Source: TransportSourceNone}, HostMode: HostModeUnknown}
	for _, scope := range Scopes() {
		inventory = inventory.MarkUnavailable(scope, "no exact tmux transport")
	}
	graph := Resolve(registry, inventory)

	if len(graph.Runtime) != 0 || len(graph.Conflicts) != 0 {
		t.Fatalf("registry-only graph observed %d runtime objects and %d conflicts",
			len(graph.Runtime), len(graph.Conflicts))
	}
	if len(graph.Projects) != 3 || len(graph.Windows) != 4 || len(graph.Panes) != 4 || len(graph.Agents) != 2 {
		t.Fatalf("registry-only graph dropped rows: %d/%d/%d/%d",
			len(graph.Projects), len(graph.Windows), len(graph.Panes), len(graph.Agents))
	}
	for _, node := range graph.Projects {
		want := StatusUnknown
		if node.MissingRoot {
			want = StatusMissingRoot
		}
		if node.Status != want {
			t.Fatalf("project %s status = %q, want %q", node.Project.Metadata.UID, node.Status, want)
		}
		if node.Runtime != nil {
			t.Fatalf("project %s carries a runtime ref with no transport", node.Project.Metadata.UID)
		}
	}
	for _, node := range graph.Windows {
		want := StatusUnknown
		if node.MissingRoot {
			want = StatusMissingRoot
		}
		if node.Status != want {
			t.Fatalf("window %s status = %q, want %q", node.Window.Metadata.UID, node.Status, want)
		}
	}
	// An Agent the Registry itself records as holding no Pane is offline, not
	// unknown: there is no runtime object to be uncertain about.
	if got := agentNode(t, graph, "agent-alpha-2").Status; got != StatusOffline {
		t.Fatalf("paneless agent status = %q, want offline", got)
	}
	if got := agentNode(t, graph, "agent-alpha-1").Status; got != StatusUnknown {
		t.Fatalf("agent with a paneRef status = %q, want unknown", got)
	}
}

// TestResolveProducesTheSameManagedIdentityOnBothHosts is the host-parity
// acceptance: one Registry and one machine produce identical managed rows under
// app-owned and standalone hosting. Only the attribution of objects projmux does
// not own may differ.
func TestResolveProducesTheSameManagedIdentityOnBothHosts(t *testing.T) {
	t.Parallel()
	registry := testRegistry(t)
	build := func(host HostMode) Inventory {
		inv := liveInventory(host)
		inv.Sessions = append(inv.Sessions, Session{ID: "$9", Name: "dotfiles"})
		inv.Windows = append(inv.Windows, Window{ID: "@9", SessionID: "$9", Index: "0", DisplayName: "zsh"})
		return inv
	}
	app := Resolve(registry, build(HostModeAppOwned))
	standalone := Resolve(registry, build(HostModeStandalone))

	appRows, err := json.Marshal([]any{app.Projects, app.Windows, app.Panes, app.Agents})
	if err != nil {
		t.Fatalf("marshal app rows: %v", err)
	}
	standaloneRows, err := json.Marshal([]any{standalone.Projects, standalone.Windows, standalone.Panes, standalone.Agents})
	if err != nil {
		t.Fatalf("marshal standalone rows: %v", err)
	}
	if string(appRows) != string(standaloneRows) {
		t.Fatalf("managed rows differ between hosts:\n app=%s\n std=%s", appRows, standaloneRows)
	}
	for _, host := range []Graph{app, standalone} {
		managed := host.RuntimeOfClass(ClassManaged)
		if len(managed) != 7 {
			t.Fatalf("host %s bound %d objects, want 7", host.HostMode, len(managed))
		}
	}
	if got := runtimeNode(t, app, "$9").Class; got != ClassUnattributed {
		t.Fatalf("app-owned unmarked session = %q, want unattributed", got)
	}
	if got := runtimeNode(t, standalone, "$9").Class; got != ClassForeign {
		t.Fatalf("standalone unmarked session = %q, want foreign", got)
	}
}

// TestResolveIsDeterministic pins the property a cached read model needs: the
// same inputs produce byte-identical output, whatever order tmux happened to list
// its objects in. tmux list order is not a contract, so the graph must not
// inherit it.
func TestResolveIsDeterministic(t *testing.T) {
	t.Parallel()
	registry := testRegistry(t)
	inventory := liveInventory(HostModeAppOwned)
	inventory.Sessions = append(inventory.Sessions,
		Session{ID: "$10", Name: "home", Role: ControlSessionRole},
		Session{ID: "$9", Name: "scratch", Ephemeral: true})
	inventory.Windows = append(inventory.Windows,
		Window{ID: "@10", SessionID: "$1", Index: "9", UID: "win-vanished"},
		Window{ID: "@9", SessionID: "$9", Index: "0"})
	inventory.Panes = append(inventory.Panes,
		Pane{ID: "%10", WindowID: "@9"},
		Pane{ID: "%9", WindowID: "@1", UID: "pane-vanished"})

	want, err := json.Marshal(Resolve(registry, inventory))
	if err != nil {
		t.Fatalf("marshal graph: %v", err)
	}
	source := rand.New(rand.NewSource(7))
	for round := range 16 {
		shuffled := inventory.Clone()
		source.Shuffle(len(shuffled.Sessions), func(i, j int) {
			shuffled.Sessions[i], shuffled.Sessions[j] = shuffled.Sessions[j], shuffled.Sessions[i]
		})
		source.Shuffle(len(shuffled.Windows), func(i, j int) {
			shuffled.Windows[i], shuffled.Windows[j] = shuffled.Windows[j], shuffled.Windows[i]
		})
		source.Shuffle(len(shuffled.Panes), func(i, j int) {
			shuffled.Panes[i], shuffled.Panes[j] = shuffled.Panes[j], shuffled.Panes[i]
		})
		got, err := json.Marshal(Resolve(registry, shuffled))
		if err != nil {
			t.Fatalf("marshal shuffled graph: %v", err)
		}
		if string(got) != string(want) {
			t.Fatalf("round %d produced a different graph:\n want=%s\n  got=%s", round, want, got)
		}
	}

	// Runtime order is ascending by tmux id within each kind, so %9 precedes %10.
	var order []string
	for _, node := range Resolve(registry, inventory).Runtime {
		order = append(order, node.Ref.ID)
	}
	wantOrder := []string{"$1", "$2", "$9", "$10", "@1", "@2", "@9", "@10", "%1", "%2", "%3", "%9", "%10"}
	if !slices.Equal(order, wantOrder) {
		t.Fatalf("runtime order = %v, want %v", order, wantOrder)
	}
}

// TestResolveDoesNotMutateItsInputs guards the read-only contract at the value
// level: a caller's Registry and Inventory are the same after a resolve.
func TestResolveDoesNotMutateItsInputs(t *testing.T) {
	t.Parallel()
	registry := testRegistry(t)
	inventory := liveInventory(HostModeAppOwned)
	registryBefore, err := json.Marshal(registry)
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	inventoryBefore, err := json.Marshal(inventory)
	if err != nil {
		t.Fatalf("marshal inventory: %v", err)
	}

	graph := Resolve(registry, inventory)
	// Mutating the graph must not reach back into the caller's registry either.
	graph.Projects[0].Project.Spec.Root = "/rewritten"
	graph.Projects[0].Project.Metadata.UID = "rewritten"

	registryAfter, err := json.Marshal(registry)
	if err != nil {
		t.Fatalf("marshal registry: %v", err)
	}
	inventoryAfter, err := json.Marshal(inventory)
	if err != nil {
		t.Fatalf("marshal inventory: %v", err)
	}
	if string(registryBefore) != string(registryAfter) {
		t.Fatalf("Resolve mutated the registry:\n before=%s\n  after=%s", registryBefore, registryAfter)
	}
	if string(inventoryBefore) != string(inventoryAfter) {
		t.Fatalf("Resolve mutated the inventory:\n before=%s\n  after=%s", inventoryBefore, inventoryAfter)
	}
}

// TestPackageCannotWriteAnything is the zero-write proof, enforced structurally
// rather than by review: the package's own imports are audited, so no filesystem,
// process, or tmux dependency can be added to the pure read model without this
// test failing.
func TestPackageCannotWriteAnything(t *testing.T) {
	t.Parallel()
	allowed := map[string]bool{
		"errors":        true,
		"fmt":           true,
		"path/filepath": true,
		"slices":        true,
		"strconv":       true,
		"strings":       true,
		"github.com/crevissepartners/projmux/internal/core/metadata": true,
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fileSet := token.NewFileSet()
	audited := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		audited++
		for _, imported := range file.Imports {
			path := strings.Trim(imported.Path.Value, `"`)
			if !allowed[path] {
				t.Fatalf("%s imports %q; the pure read model may not depend on it", name, path)
			}
		}
	}
	if audited == 0 {
		t.Fatalf("audited no package files")
	}
}
