package cli

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestRouteCoverageHasExactlyOneDispositionAndNoOrphans is the route coverage
// audit. The compatibility contract requires every current route, public or
// hidden, to hold exactly one primary disposition, with zero orphan routes,
// before any later Phase may move a namespace.
//
// The public count is 37: the 33 routes inventoried by the compatibility
// contract plus 9 canonical nodes added by later Phases -- `get` from the
// selector Phase, `delete`, `describe`, `rebind`, `rename`, `restore`, and
// `runtime` from the public verb-to-kind Phase, and `agent` and `create` from
// the Agent namespace Phase -- minus the 5 internal plumbing routes the
// internal isolation Phase moved out of the primary listing. Every one of the
// 33 inventoried routes is still present, still dispatchable, and still holds
// exactly one disposition; hiding is not removal.
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

	if public != 37 {
		t.Fatalf("public route count = %d, want 37", public)
	}
	// The 2 original hidden helpers, the 5 plumbing routes the internal
	// isolation Phase hid, and the `internal` namespace itself.
	if hidden != 8 {
		t.Fatalf("hidden route count = %d, want 8", hidden)
	}
	// The classification tally from the compatibility contract, counted over
	// the 37 public top-level routes. Canonical is 17 rather than the
	// contract's 8 because the 9 canonical verb/domain nodes are new surface,
	// not members of the 33 inventoried current routes.
	//
	// The public internal count is 0: that is the whole point of the internal
	// isolation Phase, and it is the assertion that fails the moment a plumbing
	// route leaks back into the primary listing. The shortcut and compatibility
	// counts are unchanged, which is the invariant that proves no user-facing
	// route was removed or reclassified along the way -- in particular `ai` and
	// `usage` are still public compatibility routes.
	wantPublicTally := map[Disposition]int{
		DispositionCanonical:     17,
		DispositionShortcut:      7,
		DispositionCompatibility: 13,
	}
	if !reflect.DeepEqual(publicTally, wantPublicTally) {
		t.Fatalf("public disposition tally = %v, want %v", publicTally, wantPublicTally)
	}
	// Every hidden route is internal plumbing.
	wantTally := map[Disposition]int{
		DispositionCanonical:     17,
		DispositionShortcut:      7,
		DispositionCompatibility: 13,
		DispositionInternal:      8,
	}
	if !reflect.DeepEqual(tally, wantTally) {
		t.Fatalf("disposition tally = %v, want %v", tally, wantTally)
	}
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

// inventoriedRoutes is the compatibility contract's original inventory: the 33
// public top-level routes plus the 2 hidden internal helpers, each with the
// primary disposition Phase 0 assigned it and the listing visibility it holds
// after the internal isolation Phase.
//
// It is written out in full rather than derived, because its whole job is to
// fail when a later Phase removes or reclassifies one of them.
//
// The `hidden` column changed for the 5 plumbing routes (`preview`,
// `session-popup`, `status`, `statusbar`, `tmux`) when the internal isolation
// Phase moved them under the hidden `internal` namespace. Hiding is a listing
// decision, not a removal: TestEveryInventoriedRouteSurvivesUnchanged still
// requires every one of them to resolve and dispatch, because a tmux server
// that is already running holds config generated by a previously installed
// binary and keeps invoking the current spellings. Their disposition is
// unchanged, and no route ever moved between dispositions.
var inventoriedRoutes = map[string]struct {
	disposition Disposition
	hidden      bool
}{
	"ai":             {DispositionCompatibility, false},
	"attention":      {DispositionCanonical, false},
	"attach":         {DispositionCompatibility, false},
	"current":        {DispositionCompatibility, false},
	"doctor":         {DispositionShortcut, false},
	"diagnostics":    {DispositionCanonical, false},
	"focus":          {DispositionCompatibility, false},
	"hook":           {DispositionCanonical, false},
	"kill":           {DispositionCompatibility, false},
	"notify":         {DispositionCompatibility, false},
	"pin":            {DispositionCompatibility, false},
	"preview":        {DispositionInternal, true},
	"prune":          {DispositionCompatibility, false},
	"quit":           {DispositionShortcut, false},
	"resources":      {DispositionShortcut, false},
	"sessions":       {DispositionCompatibility, false},
	"session-state":  {DispositionCompatibility, false},
	"session-popup":  {DispositionInternal, true},
	"settings":       {DispositionShortcut, false},
	"setup":          {DispositionCanonical, false},
	"shell":          {DispositionShortcut, false},
	"status":         {DispositionInternal, true},
	"statusbar":      {DispositionInternal, true},
	"switch":         {DispositionShortcut, false},
	"tag":            {DispositionCompatibility, false},
	"tmux":           {DispositionInternal, true},
	"update":         {DispositionCanonical, false},
	"upgrade":        {DispositionCompatibility, false},
	"usage":          {DispositionCompatibility, false},
	"welcome":        {DispositionShortcut, false},
	"window":         {DispositionCanonical, false},
	"help":           {DispositionCanonical, false},
	"version":        {DispositionCanonical, false},
	"key-broker":     {DispositionInternal, true},
	"popup-wait-key": {DispositionInternal, true},
}

// TestEveryInventoriedRouteSurvivesUnchanged is the compatibility half of the
// coverage invariant: adding canonical surface must never remove, hide, or
// reclassify one of the routes the contract inventoried.
//
// Removal is a separate breaking-change Phase, so until then this test is the
// guard that no relocation Phase quietly drops a public spelling.
func TestEveryInventoriedRouteSurvivesUnchanged(t *testing.T) {
	t.Parallel()

	if len(inventoriedRoutes) != 35 {
		t.Fatalf("inventory size = %d, want the 33 public routes plus 2 hidden helpers", len(inventoriedRoutes))
	}
	for token, want := range inventoriedRoutes {
		route, ok := LookupRoute(token)
		if !ok {
			t.Fatalf("inventoried route %q was removed", token)
		}
		if route.Disposition != want.disposition {
			t.Fatalf("route %q disposition = %q, want %q", token, route.Disposition, want.disposition)
		}
		if route.Hidden != want.hidden {
			t.Fatalf("route %q hidden = %v, want %v", token, route.Hidden, want.hidden)
		}
	}

	// The routes this Phase adds are new surface, not inventory members, and all
	// of them are canonical.
	for _, token := range []string{"get", "describe", "delete", "rebind", "rename", "restore", "runtime", "agent", "create"} {
		if _, ok := inventoriedRoutes[token]; ok {
			t.Fatalf("%q is listed as an inventoried route; it is new canonical surface", token)
		}
		route, ok := LookupRoute(token)
		if !ok {
			t.Fatalf("canonical route %q is missing", token)
		}
		if route.Disposition != DispositionCanonical || route.Hidden {
			t.Fatalf("route %q disposition=%q hidden=%v, want a public canonical node", token, route.Disposition, route.Hidden)
		}
	}
}

// TestCanonicalSubRoutesDoNotShadowAnExistingSubcommand keeps the compatibility
// spellings intact where a canonical kind token was added next to them.
func TestCanonicalSubRoutesDoNotShadowAnExistingSubcommand(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		parent string
		want   []string
	}{
		{parent: "pin", want: []string{"project", "list", "add", "remove", "toggle", "clear"}},
		{parent: "tag", want: []string{"project", "list", "toggle", "clear"}},
		{parent: "prune", want: []string{"ephemeral", "session-state", "project", "snapshot"}},
		{parent: "attach", want: []string{"auto", "project"}},
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
