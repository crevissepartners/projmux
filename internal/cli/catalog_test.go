package cli

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestRouteCoverageHasExactlyOneDispositionAndNoOrphans is the Phase 0 coverage
// audit. The compatibility contract requires all 33 current public routes plus
// the 2 hidden internal helpers to hold exactly one primary disposition, with
// zero orphan routes, before any later Phase may move a namespace.
func TestRouteCoverageHasExactlyOneDispositionAndNoOrphans(t *testing.T) {
	t.Parallel()

	all := Routes()
	closed := map[Disposition]bool{}
	for _, disposition := range Dispositions() {
		closed[disposition] = true
	}

	seen := map[string]bool{}
	tally := map[Disposition]int{}
	publicTally := map[Disposition]int{}
	public, hidden := 0, 0
	for _, route := range all {
		if route.Name == "" {
			t.Fatalf("route with empty token: %#v", route)
		}
		if seen[route.Name] {
			t.Fatalf("duplicate route token %q", route.Name)
		}
		seen[route.Name] = true
		if !closed[route.Disposition] {
			t.Fatalf("route %q disposition %q is outside the closed set %v", route.Name, route.Disposition, Dispositions())
		}
		tally[route.Disposition]++
		if route.Hidden {
			hidden++
		} else {
			public++
			publicTally[route.Disposition]++
		}
		if route.Summary == "" {
			t.Fatalf("route %q has no summary", route.Name)
		}
	}

	if public != 33 {
		t.Fatalf("public route count = %d, want 33", public)
	}
	if hidden != 2 {
		t.Fatalf("hidden helper count = %d, want 2", hidden)
	}
	// The classification tally from the compatibility contract, counted over
	// the 33 public top-level routes.
	wantPublicTally := map[Disposition]int{
		DispositionCanonical:     8,
		DispositionShortcut:      7,
		DispositionCompatibility: 13,
		DispositionInternal:      5,
	}
	if !reflect.DeepEqual(publicTally, wantPublicTally) {
		t.Fatalf("public disposition tally = %v, want %v", publicTally, wantPublicTally)
	}
	// Both hidden helpers are internal plumbing.
	wantTally := map[Disposition]int{
		DispositionCanonical:     8,
		DispositionShortcut:      7,
		DispositionCompatibility: 13,
		DispositionInternal:      7,
	}
	if !reflect.DeepEqual(tally, wantTally) {
		t.Fatalf("disposition tally = %v, want %v", tally, wantTally)
	}
}

// TestEveryRouteCanonicalSpellingResolves proves there is no dangling canonical
// reference: every canonical spelling named by a current route (at any depth)
// exists in the canonical manifest.
func TestEveryRouteCanonicalSpellingResolves(t *testing.T) {
	t.Parallel()

	walkRoutes(Routes(), func(path []string, route Route) {
		if len(route.Canonical) == 0 {
			t.Errorf("route %q names no canonical route", strings.Join(path, " "))
			return
		}
		for _, spelling := range route.Canonical {
			if _, ok := LookupCanonicalRoute(spelling); !ok {
				t.Errorf("route %q references unknown canonical route %q", strings.Join(path, " "), spelling)
			}
		}
	})
}

// TestEveryCanonicalSourceRouteBackReferences proves the mapping is closed in
// both directions: a canonical route that claims a current source must be named
// by that source route (or one of its sub-routes).
func TestEveryCanonicalSourceRouteBackReferences(t *testing.T) {
	t.Parallel()

	claimed := map[string][]string{}
	walkRoutes(Routes(), func(path []string, route Route) {
		for _, spelling := range route.Canonical {
			claimed[spelling] = append(claimed[spelling], path[0])
		}
	})

	for _, canonical := range CanonicalRoutes() {
		if canonical.Spelling == "" || canonical.Summary == "" {
			t.Errorf("canonical route with empty spelling or summary: %#v", canonical)
		}
		for _, source := range canonical.Sources {
			if _, ok := LookupRoute(source); !ok {
				t.Errorf("canonical route %q names unknown source route %q", canonical.Spelling, source)
				continue
			}
			if !slices.Contains(claimed[canonical.Spelling], source) {
				t.Errorf("canonical route %q claims source %q but route %q does not name it", canonical.Spelling, source, source)
			}
		}
	}
}

// TestCanonicalManifestPinsRequiredPhaseZeroSpellings pins the exact canonical
// spellings the Phase 0 contract calls out by name.
func TestCanonicalManifestPinsRequiredPhaseZeroSpellings(t *testing.T) {
	t.Parallel()

	for _, spelling := range []string{"rebind project", "prune project", "agent usage", "get pane"} {
		if _, ok := LookupCanonicalRoute(spelling); !ok {
			t.Fatalf("canonical manifest is missing %q", spelling)
		}
	}

	// `rebind project` and `prune project` are new canonical surface with no
	// current public route. Phase 0 pins the spelling only; it must not become
	// executable here.
	for _, spelling := range []string{"rebind project", "prune project"} {
		canonical, _ := LookupCanonicalRoute(spelling)
		if len(canonical.Sources) != 0 {
			t.Fatalf("%q sources = %v, want none in Phase 0", spelling, canonical.Sources)
		}
	}

	usage, _ := LookupCanonicalRoute("agent usage")
	if !slices.Contains(usage.Sources, "usage") || !slices.Contains(usage.Sources, "status") {
		t.Fatalf("agent usage sources = %v, want the legacy usage and status usage spellings", usage.Sources)
	}

	pane, _ := LookupCanonicalRoute("get pane")
	if !slices.Contains(pane.Fields, FieldProjectionCWD) {
		t.Fatalf("get pane fields = %v, want the Pane-read cwd projection", pane.Fields)
	}
}

// TestSharedOutputCatalogIsClosedAndExcludesCWD pins the shared `-o` catalog and
// keeps the route-local `cwd` projection out of it.
func TestSharedOutputCatalogIsClosedAndExcludesCWD(t *testing.T) {
	t.Parallel()

	want := []OutputMode{
		OutputModeUID,
		OutputModeName,
		OutputModeRef,
		OutputModeMetadata,
		OutputModeJSON,
		OutputModePaneID,
		OutputModeNone,
	}
	if !reflect.DeepEqual(SharedOutputModes(), want) {
		t.Fatalf("shared output modes = %v, want %v", SharedOutputModes(), want)
	}
	if IsSharedOutputMode(OutputMode(FieldProjectionCWD)) {
		t.Fatal("cwd must not be a member of the shared create output enum")
	}
	if IsSharedOutputMode(OutputModeDefault) {
		t.Fatal("default is implicit and must not be an explicit -o value")
	}
	if got := RouteLocalFieldProjections(); !reflect.DeepEqual(got, []FieldProjection{FieldProjectionCWD}) {
		t.Fatalf("route-local field projections = %v, want [cwd]", got)
	}

	// Mutating the returned slices must not corrupt the catalog.
	modes := SharedOutputModes()
	modes[0] = "tampered"
	if SharedOutputModes()[0] != OutputModeUID {
		t.Fatal("SharedOutputModes returned a mutable view of the catalog")
	}
}

// TestRouteLocalOutputCatalogIsPinnedWhereContractFixesIt asserts the two
// route-local projections Phase 0 pins on current routes.
func TestRouteLocalOutputCatalogIsPinnedWhereContractFixesIt(t *testing.T) {
	t.Parallel()

	current, ok := LookupRoute("current")
	if !ok {
		t.Fatal("current route missing")
	}
	if !reflect.DeepEqual(current.Fields, []FieldProjection{FieldProjectionCWD}) {
		t.Fatalf("current fields = %v, want [cwd]", current.Fields)
	}
	if len(current.Outputs) != 0 {
		t.Fatalf("current outputs = %v, want none: cwd is a field projection, not a shared mode", current.Outputs)
	}

	ai, _ := LookupRoute("ai")
	split, ok := findChild(ai, "split")
	if !ok {
		t.Fatal("ai split route missing")
	}
	if !reflect.DeepEqual(split.Outputs, []OutputMode{OutputModePaneID}) {
		t.Fatalf("ai split outputs = %v, want [pane-id] for the --print-pane-id bridge", split.Outputs)
	}

	// Every route-local output mode must belong to the shared catalog.
	walkRoutes(Routes(), func(path []string, route Route) {
		for _, mode := range route.Outputs {
			if !IsSharedOutputMode(mode) {
				t.Errorf("route %q pins non-shared output mode %q", strings.Join(path, " "), mode)
			}
		}
		for _, field := range route.Fields {
			if !slices.Contains(RouteLocalFieldProjections(), field) {
				t.Errorf("route %q pins unknown field projection %q", strings.Join(path, " "), field)
			}
		}
	})
}

// TestResolveReturnsDeepestMatchedRoute covers the shared route resolution used
// by the help boundary: unknown trailing tokens and flags fall back to the
// nearest documented ancestor, and an unknown leading token does not resolve.
func TestResolveReturnsDeepestMatchedRoute(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		tokens []string
		want   []string
		ok     bool
	}{
		{name: "top level", tokens: []string{"ai"}, want: []string{"ai"}, ok: true},
		{name: "nested", tokens: []string{"ai", "settings"}, want: []string{"ai", "settings"}, ok: true},
		{name: "unknown child", tokens: []string{"ai", "bogus"}, want: []string{"ai"}, ok: true},
		{name: "flag after child", tokens: []string{"setup", "terminal", "--apply"}, want: []string{"setup", "terminal"}, ok: true},
		{name: "hidden helper", tokens: []string{"popup-wait-key"}, want: []string{"popup-wait-key"}, ok: true},
		{name: "unknown top level", tokens: []string{"nosuchcmd"}, ok: false},
		{name: "empty", tokens: nil, ok: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path, route, ok := Resolve(test.tokens)
			if ok != test.ok {
				t.Fatalf("Resolve(%q) ok = %v, want %v", test.tokens, ok, test.ok)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(path, test.want) {
				t.Fatalf("Resolve(%q) path = %q, want %q", test.tokens, path, test.want)
			}
			if route.Name != test.want[len(test.want)-1] {
				t.Fatalf("Resolve(%q) route = %q, want %q", test.tokens, route.Name, test.want[len(test.want)-1])
			}
		})
	}
}

// walkRoutes visits every manifest node with its full path.
func walkRoutes(nodes []Route, visit func(path []string, route Route)) {
	var walk func(prefix []string, nodes []Route)
	walk = func(prefix []string, nodes []Route) {
		for _, node := range nodes {
			path := append(append([]string{}, prefix...), node.Name)
			visit(path, node)
			walk(path, node.Children)
		}
	}
	walk(nil, nodes)
}
