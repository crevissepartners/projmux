package cli

import (
	"reflect"
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
		"rename project", "rename window", "rename pane",
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
	var children []string
	for _, child := range route.Children {
		children = append(children, child.Name)
	}
	want := []string{"projects", "windows", "panes", "agents", "notifications", "snapshots", "pane"}
	if !reflect.DeepEqual(children, want) {
		t.Fatalf("get children = %v, want %v", children, want)
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
	if !reflect.DeepEqual(pane.Outputs, SharedOutputModes()) {
		t.Fatalf("get pane outputs = %v, want the shared catalog", pane.Outputs)
	}

	// The compatibility route it will eventually replace is untouched: the
	// canonical read is added alongside it and no old route is removed or
	// reclassified.
	current, ok := LookupRoute("current")
	if !ok {
		t.Fatal("current route missing")
	}
	if current.Disposition != DispositionCompatibility {
		t.Fatalf("current disposition = %q, want compatibility", current.Disposition)
	}
	if len(current.Children) != 0 {
		t.Fatalf("current grew sub-routes: %#v", current.Children)
	}
}
