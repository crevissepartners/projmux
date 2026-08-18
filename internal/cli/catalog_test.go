package cli

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestRouteCoverageHasExactlyOneDispositionAndNoOrphans audits the final tree:
// 31 public canonical/shortcut roots and the hidden internal namespace. Retired
// compatibility roots are absent rather than represented as dispatchable
// tombstones.
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

	if public != 31 {
		t.Fatalf("public route count = %d, want 31", public)
	}
	if hidden != 1 {
		t.Fatalf("hidden route count = %d, want 1", hidden)
	}
	wantPublicTally := map[Disposition]int{
		DispositionCanonical: 24,
		DispositionShortcut:  7,
	}
	if !reflect.DeepEqual(publicTally, wantPublicTally) {
		t.Fatalf("public disposition tally = %v, want %v", publicTally, wantPublicTally)
	}
	wantTally := map[Disposition]int{
		DispositionCanonical: 24,
		DispositionShortcut:  7,
		DispositionInternal:  1,
	}
	if !reflect.DeepEqual(tally, wantTally) {
		t.Fatalf("disposition tally = %v, want %v", tally, wantTally)
	}
}

// exactTransportRoots are the top-level routes allowed to name an exact tmux
// server on the command line.
//
// The guard below is about the mutation contract, not about the flag: the
// hidden `reconcile-bindings` spelling stays lifecycle plumbing, and no route
// may pick up an exact-server *repair* surface by accident. `reconcile` owns
// that repair. `get` and `runtime` are here for the read half -- the Runtime
// diagnostics escape hatch observes one exact server and writes nothing -- and
// listing them explicitly is what keeps a future mutation route from arriving
// under the same flag without anyone noticing.
//
// `delete` is here because its live half has to name a server and previously
// did not. It defaulted to the app's own `-L projmux` socket, which meant a
// delete issued against an isolated server inventoried one host and killed
// objects on another. The flags replace that default rather than widening the
// route: without one, and outside tmux, `delete` now refuses instead of
// guessing.
var exactTransportRoots = []string{"reconcile", "get", "runtime", "delete"}

func TestManagedBindingConvergenceStaysHiddenBehindPublicResourceRepair(t *testing.T) {
	t.Parallel()

	walkRoutes(Routes(), func(path []string, route Route) {
		publicRepair := len(path) > 0 && slices.Contains(exactTransportRoots, path[0])
		if route.Name == "reconcile-bindings" || slices.Contains(route.Usage, "--socket-path") && !publicRepair {
			t.Fatalf("binding convergence leaked into command catalog at %q: %#v", strings.Join(path, " "), route)
		}
		for _, usage := range route.Usage {
			if strings.Contains(usage, "reconcile-bindings") || strings.Contains(usage, "--socket-path") && !publicRepair {
				t.Fatalf("binding convergence leaked into command catalog usage at %q: %q", strings.Join(path, " "), usage)
			}
		}
	})
}

// TestNoInternalRouteIsPubliclyListed is the acceptance assertion of the
// internal isolation Phase, stated directly rather than inferred from a tally:
// a route classified as plumbing may not appear in the primary listing, and the
// `internal` namespace itself may not either.
func TestNoInternalRouteIsPubliclyListed(t *testing.T) {
	t.Parallel()

	for _, route := range Routes() {
		if route.Disposition == DispositionInternal && !route.Hidden {
			t.Errorf("internal route %q is visible in the primary help listing", route.Name)
		}
	}
	internal, ok := LookupRoute("internal")
	if !ok {
		t.Fatal("the internal namespace route is missing")
	}
	if !internal.Hidden || internal.Disposition != DispositionInternal {
		t.Fatalf("internal route hidden=%v disposition=%q, want a hidden internal node", internal.Hidden, internal.Disposition)
	}
	// Every plumbing spelling the canonical manifest declares must be reachable
	// as a real `internal <namespace>` route, or the generated tmux config would
	// be emitting a route that does not exist.
	for _, canonical := range CanonicalRoutes() {
		namespace, ok := strings.CutPrefix(canonical.Spelling, "internal ")
		if !ok {
			continue
		}
		path, _, resolved := Resolve([]string{"internal", namespace})
		if !resolved || !reflect.DeepEqual(path, []string{"internal", namespace}) {
			t.Errorf("canonical route %q does not resolve to an executable node (path=%v ok=%v)", canonical.Spelling, path, resolved)
		}
	}
}

// compatibilityRoutesWithoutACanonicalSpelling is deliberately empty. Phase 1
// closes every gap owned by a route classified as compatibility while leaving
// every old spelling executable for the separate removal Phase.
var compatibilityRoutesWithoutACanonicalSpelling = map[string]string{}

// shortcutRoutesWithoutACanonicalSpelling is the separate, explicit set of
// high-frequency shortcuts whose proposed long-form destinations are outside
// the legacy compatibility track. The general Settings shortcut is also here:
// Phase 1's executable `config edit` is intentionally AI-scoped and is not a
// replacement for the complete Settings UI.
//
// Every one of these routes works today and is unchanged. Their proposed
// long-form destinations are not Phase 1 compatibility work, so this record
// prevents an unrelated shortcut decision from being mistaken for a remaining
// compatibility gap.
//
// The set is written out rather than derived. Its whole job is to fail when some
// later change drops a canonical mapping that nobody adjudicated, so a route
// silently losing its v2 spelling is a test failure and never an omission.
var shortcutRoutesWithoutACanonicalSpelling = map[string]string{
	"doctor":    "diagnostics doctor",
	"quit":      "runtime quit",
	"resources": "diagnostics resources",
	"settings":  "",
	"shell":     "runtime open",
	"welcome":   "setup welcome",
}

// TestEveryRouteCanonicalSpellingResolves proves there is no dangling canonical
// reference: every canonical spelling named by a current route (at any depth)
// exists in the canonical manifest.
func TestEveryRouteCanonicalSpellingResolves(t *testing.T) {
	t.Parallel()

	walkRoutes(Routes(), func(path []string, route Route) {
		for _, spelling := range route.Canonical {
			if _, ok := LookupCanonicalRoute(spelling); !ok {
				t.Errorf("route %q references unknown canonical route %q", strings.Join(path, " "), spelling)
			}
		}
	})
}

// TestOnlyTheExplicitShortcutSetLeavesARouteUnmapped is the other half of the
// coverage claim, and it is stated as a two-way diff so it cannot rot into a
// permission slip.
//
// Left to right: every route the record names must really have no canonical
// mapping, so a spelling quietly restored to the manifest without updating the
// record fails here. Right to left: every unmapped route must be in the record,
// so a future change that drops a canonical mapping without adjudicating it
// fails here too. Coverage stays total for everything else.
func TestOnlyTheExplicitShortcutSetLeavesARouteUnmapped(t *testing.T) {
	t.Parallel()
	if len(compatibilityRoutesWithoutACanonicalSpelling) != 0 {
		t.Fatalf("compatibility gaps = %v, want zero", compatibilityRoutesWithoutACanonicalSpelling)
	}

	unmapped := map[string]bool{}
	walkRoutes(Routes(), func(path []string, route Route) {
		if len(route.Canonical) == 0 {
			unmapped[strings.Join(path, " ")] = true
		}
	})

	for spelling := range shortcutRoutesWithoutACanonicalSpelling {
		tokens := strings.Fields(spelling)
		if path, _, ok := Resolve(tokens); !ok || len(path) != len(tokens) {
			t.Errorf("record names %q, which is not a route", spelling)
			continue
		}
		if !unmapped[spelling] {
			t.Errorf("route %q now names a canonical spelling; drop it from routesWithoutACanonicalSpelling", spelling)
		}
	}
	for spelling := range unmapped {
		if _, ok := shortcutRoutesWithoutACanonicalSpelling[spelling]; !ok {
			t.Errorf("route %q has no canonical spelling and is not in the adjudicated record", spelling)
		}
	}
	for spelling := range shortcutRoutesWithoutACanonicalSpelling {
		top, ok := LookupRoute(strings.Fields(spelling)[0])
		if !ok || top.Disposition != DispositionShortcut {
			t.Errorf("unmapped shortcut %q belongs to disposition %q, want shortcut", spelling, top.Disposition)
		}
	}

	// The deferred shortcut destinations must really be absent, or their routes
	// should map to them instead of remaining in this record.
	for spelling, deleted := range shortcutRoutesWithoutACanonicalSpelling {
		if deleted == "" {
			continue
		}
		if _, ok := LookupCanonicalRoute(deleted); ok {
			t.Errorf("canonical route %q still exists, so route %q should name it", deleted, spelling)
		}
	}

	// Non-vacuity: the diff proves nothing if every route happens to be mapped.
	if len(unmapped) == 0 {
		t.Fatal("no route is unmapped, so this assertion is vacuous; delete it with the reason recorded")
	}
}

func TestTagProjectIsRemovedWithTheTagCompatibilityRoot(t *testing.T) {
	t.Parallel()
	if _, ok := LookupCanonicalRoute("tag project"); ok {
		t.Fatal("tag project remains a false canonical target")
	}
	if path, route, ok := Resolve([]string{"tag", "project"}); ok {
		t.Fatalf("tag project resolved to %v (%#v), want the removed root", path, route)
	}
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

// TestEveryRouteCanonicalReferenceIsClaimedByItsSource closes the reverse edge
// of TestEveryCanonicalSourceRouteBackReferences. A route may not point at a
// canonical spelling whose source list omits that handler namespace.
func TestEveryRouteCanonicalReferenceIsClaimedByItsSource(t *testing.T) {
	t.Parallel()
	walkRoutes(Routes(), func(path []string, route Route) {
		for _, spelling := range route.Canonical {
			canonical, ok := LookupCanonicalRoute(spelling)
			if !ok {
				continue
			}
			if !slices.Contains(canonical.Sources, path[0]) {
				t.Errorf("route %q maps to %q, whose sources omit %q", strings.Join(path, " "), spelling, path[0])
			}
		}
	})
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

	// `rebind project` and `prune project` were manifest-only spellings in
	// Phase 0 because no handler could give them parity. The public
	// verb-to-kind Phase supplies those handlers, so both now resolve to a real
	// route and must name it.
	for spelling, want := range map[string]string{
		"rebind project": "rebind",
		"prune project":  "prune",
	} {
		canonical, _ := LookupCanonicalRoute(spelling)
		if !slices.Contains(canonical.Sources, want) {
			t.Fatalf("%q sources = %v, want the executable %q route", spelling, canonical.Sources, want)
		}
		kind := strings.Fields(spelling)[1]
		path, _, ok := Resolve([]string{want, kind})
		if !ok || !reflect.DeepEqual(path, []string{want, kind}) {
			t.Fatalf("%q resolved to %v (ok=%v), want the executable route node", spelling, path, ok)
		}
	}

	usage, _ := LookupCanonicalRoute("agent usage")
	if !reflect.DeepEqual(usage.Sources, []string{"internal", "agent"}) {
		t.Fatalf("agent usage sources = %v, want only internal and agent", usage.Sources)
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

// TestRouteLocalOutputCatalogIsPinnedWhereContractFixesIt asserts the
// projections on the surviving canonical routes after retirement.
func TestRouteLocalOutputCatalogIsPinnedWhereContractFixesIt(t *testing.T) {
	t.Parallel()

	get, _ := LookupRoute("get")
	pane, ok := findChild(get, "pane")
	if !ok {
		t.Fatal("get pane route missing")
	}
	if !reflect.DeepEqual(pane.Fields, []FieldProjection{FieldProjectionCWD}) {
		t.Fatalf("get pane fields = %v, want [cwd]", pane.Fields)
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
		{name: "top level", tokens: []string{"agent"}, want: []string{"agent"}, ok: true},
		{name: "unknown child stops at ancestor", tokens: []string{"agent", "bogus"}, want: []string{"agent"}, ok: true},
		{name: "flag after child", tokens: []string{"setup", "terminal", "--apply"}, want: []string{"setup", "terminal"}, ok: true},
		{name: "removed ai root", tokens: []string{"ai", "ingest", "codex-hook"}, ok: false},
		{name: "removed internal alias", tokens: []string{"popup-wait-key"}, ok: false},
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

// TestRetirementCatalogMatchesTheRemainingLedger pins the final breaking tree:
// every removed root disappears completely and takes the ordinary root
// unknown-command path.
func TestRetirementCatalogMatchesTheRemainingLedger(t *testing.T) {
	t.Parallel()

	for _, token := range []string{
		"ai", "current", "kill", "notify", "sessions", "session-state", "tag", "upgrade", "usage",
		"tmux", "status", "statusbar", "preview", "session-popup", "key-broker", "popup-wait-key",
	} {
		if _, ok := LookupRoute(token); ok {
			t.Errorf("retired top-level alias %q remains in the command catalog", token)
		}
	}
	for _, token := range []string{"attach", "focus", "pin", "prune"} {
		route, ok := LookupRoute(token)
		if !ok {
			t.Fatalf("surviving canonical root %q is missing", token)
		}
		if route.Disposition != DispositionCanonical || route.Hidden {
			t.Fatalf("route %q disposition=%q hidden=%v, want a public canonical node", token, route.Disposition, route.Hidden)
		}
	}
}

// TestOnlyCanonicalChildrenSurviveOnMixedLegacyRoots keeps the Phase 2 leaf
// boundary exact without migrating any leaf parser into Cobra.
func TestOnlyCanonicalChildrenSurviveOnMixedLegacyRoots(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		parent string
		want   []string
	}{
		{parent: "pin", want: []string{"project"}},
		{parent: "prune", want: []string{"project", "snapshot"}},
		{parent: "attach", want: []string{"project"}},
		{parent: "focus", want: []string{"project", "window", "pane"}},
	} {
		route, ok := LookupRoute(test.parent)
		if !ok {
			t.Fatalf("route %q missing", test.parent)
		}
		var got []string
		for _, child := range route.Children {
			got = append(got, child.Name)
		}
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("%s children = %v, want %v", test.parent, got, test.want)
		}
	}
}
