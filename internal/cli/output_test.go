package cli

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestCWDFieldProjectionIsAcceptedOnlyByThePaneCurrentRead is the manifest half
// of the `cwd` scope rule: `cwd` is a Pane-read route-local field projection, so
// exactly one canonical route may accept it and every other kind and every
// mutation route must reject it.
func TestCWDFieldProjectionIsAcceptedOnlyByThePaneCurrentRead(t *testing.T) {
	t.Parallel()

	var accepting []string
	for _, route := range CanonicalRoutes() {
		mode, field, err := ResolveOutputToken(route.Spelling, string(FieldProjectionCWD))
		if err == nil {
			accepting = append(accepting, route.Spelling)
		}
		if route.Spelling == "get pane" {
			if err != nil || field != FieldProjectionCWD || mode != "" {
				t.Fatalf("get pane rejected its own cwd projection: mode=%q field=%q err=%v", mode, field, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("canonical route %q accepted -o cwd", route.Spelling)
		}
	}
	if !reflect.DeepEqual(accepting, []string{"get pane"}) {
		t.Fatalf("routes accepting cwd = %v, want only [get pane]", accepting)
	}

	// Spelled out for the mutation family the contract calls out by name, so a
	// future Phase that adds `cwd` to a mutation route fails here loudly.
	for _, spelling := range []string{
		"create window", "create pane", "create agent", "create codex",
		"rename project", "rename window", "rename pane", "rename agent",
		"rebind project", "delete window", "delete pane", "delete agent",
		"focus pane", "attach project", "restore snapshot", "prune project",
	} {
		if _, _, err := ResolveOutputToken(spelling, string(FieldProjectionCWD)); err == nil {
			t.Errorf("mutation route %q resolved -o cwd", spelling)
		}
	}

	// Other read kinds reject it too: the projection is Pane-scoped, not
	// read-scoped.
	for _, spelling := range []string{"get projects", "get windows", "get panes", "get agents", "describe pane"} {
		if _, _, err := ResolveOutputToken(spelling, string(FieldProjectionCWD)); err == nil {
			t.Errorf("read route %q resolved -o cwd", spelling)
		}
	}

	// An unknown route never grants a projection.
	if _, _, err := ResolveOutputToken("no such route", string(FieldProjectionCWD)); err == nil {
		t.Fatal("an unknown canonical route granted the cwd projection")
	}
}

// TestResolveOutputTokenAcceptsSharedModesAndRejectsUnknownTokens is the output
// table for the read route: every shared mode resolves, the route-local field
// resolves as a field rather than a mode, and anything else is an error whose
// text lists the accepted values.
func TestResolveOutputTokenAcceptsSharedModesAndRejectsUnknownTokens(t *testing.T) {
	t.Parallel()

	for _, want := range SharedOutputModes() {
		mode, field, err := ResolveOutputToken("get pane", string(want))
		if err != nil {
			t.Fatalf("get pane rejected shared mode %q: %v", want, err)
		}
		if mode != want || field != "" {
			t.Fatalf("token %q resolved to mode=%q field=%q", want, mode, field)
		}
	}

	// `default` is implicit and is not an accepted explicit token.
	if _, _, err := ResolveOutputToken("get pane", string(OutputModeDefault)); err == nil {
		t.Fatal("-o default resolved; the default projection is implicit")
	}

	_, _, err := ResolveOutputToken("get pane", "bogus")
	if err == nil {
		t.Fatal("an unknown -o token resolved")
	}
	if !strings.Contains(err.Error(), "accepted values:") || !strings.Contains(err.Error(), "cwd") {
		t.Fatalf("error text does not list the accepted values: %v", err)
	}

	if _, _, err := ResolveOutputToken("no such route", "uid"); err == nil {
		t.Fatal("an unknown canonical route resolved an output token")
	}

	want := []string{"uid", "name", "ref", "metadata", "json", "pane-id", "none", "cwd"}
	if got := AcceptedOutputTokens("get pane"); !reflect.DeepEqual(got, want) {
		t.Fatalf("accepted tokens = %v, want %v", got, want)
	}
	if got := AcceptedOutputTokens("no such route"); got != nil {
		t.Fatalf("unknown route accepted tokens = %v, want none", got)
	}
}

// TestGetRouteOwnsTheReadKindFamily pins the shape of the `get` node: the six
// kinds of the read family plus the singular Pane read that owns the route-local
// `cwd` projection, and nothing else. It also proves the compatibility routes
// this family will eventually replace are still intact.
func TestGetRouteOwnsTheReadKindFamily(t *testing.T) {
	t.Parallel()

	route, ok := LookupRoute("get")
	if !ok {
		t.Fatal("get route missing")
	}
	if route.Disposition != DispositionCanonical || route.Hidden {
		t.Fatalf("get route disposition=%q hidden=%v", route.Disposition, route.Hidden)
	}
	var children, namespaces []string
	for _, child := range route.Children {
		if child.Namespace {
			namespaces = append(namespaces, child.Name)
			continue
		}
		children = append(children, child.Name)
	}
	want := []string{"projects", "windows", "panes", "agents", "notifications", "snapshots", "pane"}
	if !reflect.DeepEqual(children, want) {
		t.Fatalf("get kind children = %v, want %v", children, want)
	}
	// The read family owns exactly one namespace child. `runtime` groups tmux
	// object kinds, which carry no Registry identity, and pinning it here is
	// what keeps a later kind from being added as a namespace to dodge the
	// parity contract above.
	if !reflect.DeepEqual(namespaces, []string{"runtime"}) {
		t.Fatalf("get namespace children = %v, want [runtime]", namespaces)
	}
	runtime, ok := findChild(route, "runtime")
	if !ok {
		t.Fatal("get runtime child missing")
	}
	var runtimeKinds []string
	for _, child := range runtime.Children {
		runtimeKinds = append(runtimeKinds, child.Name)
	}
	if !reflect.DeepEqual(runtimeKinds, []string{"sessions", "windows", "panes"}) {
		t.Fatalf("get runtime children = %v, want [sessions windows panes]", runtimeKinds)
	}
	// `cwd` stays scoped to the singular Pane read. No plural kind may declare
	// it, which is what keeps the field projection from leaking across kinds.
	for _, child := range route.Children {
		if child.Name == "pane" {
			continue
		}
		if len(child.Fields) != 0 {
			t.Fatalf("get %s declares field projections %v, want none", child.Name, child.Fields)
		}
	}

	pane, ok := findChild(route, "pane")
	if !ok {
		t.Fatal("get pane child missing")
	}
	if !reflect.DeepEqual(pane.Fields, []FieldProjection{FieldProjectionCWD}) {
		t.Fatalf("get pane fields = %v, want [cwd]", pane.Fields)
	}
	// The read routes advertise the shared catalog minus `pane-id`. The token is
	// still a member of the shared enum and the canonical manifest still pins it
	// on this route, so `ResolveOutputToken` keeps accepting it; what changed is
	// that the route no longer advertises a projection whose only outcome on a
	// read is "needs a live transport binding, which is not wired yet".
	if !reflect.DeepEqual(pane.Outputs, readProjectionCatalog) {
		t.Fatalf("get pane outputs = %v, want the read catalog %v", pane.Outputs, readProjectionCatalog)
	}
	if slices.Contains(pane.Outputs, OutputModePaneID) {
		t.Fatal("a read route advertises -o pane-id, which it answers with an error")
	}
	if !IsSharedOutputMode(OutputModePaneID) {
		t.Fatal("pane-id left the shared catalog; only the read-route advertisement was meant to change")
	}

	// The compatibility root has been removed; the canonical exact-one read is
	// the only command-manifest owner of the cwd projection.
	if _, ok := LookupRoute("current"); ok {
		t.Fatal("retired current root remains in the command manifest")
	}
}

func TestWideOutputIsScopedToColumnarListReads(t *testing.T) {
	t.Parallel()
	owners := map[string]bool{
		"get projects": true, "get windows": true, "get panes": true, "get agents": true,
		"get runtime sessions": true, "get runtime windows": true, "get runtime panes": true,
	}
	for _, route := range CanonicalRoutes() {
		mode, field, err := ResolveOutputToken(route.Spelling, "wide")
		if owners[route.Spelling] {
			if err != nil || mode != OutputModeWide || field != "" {
				t.Errorf("%s rejects wide: mode=%q field=%q err=%v", route.Spelling, mode, field, err)
			}
		} else if err == nil {
			t.Errorf("wide escaped onto %s", route.Spelling)
		}
	}
	if IsSharedOutputMode(OutputModeWide) {
		t.Fatal("wide leaked into the shared mutation/singular catalog")
	}
}
