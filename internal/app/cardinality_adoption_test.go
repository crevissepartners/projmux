package app

import (
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

// routeCardinality is one canonical route that resolves an existing target set,
// together with the matrix cell it consumes.
type routeCardinality struct {
	spelling string
	target   selector.Target
	want     selector.Cardinality
}

// canonicalRouteCardinalities is the maintained map from the routes this Phase
// ships to the declared <verb, kind> matrix cells they resolve against.
//
// This is the adoption record: the shared matrix is the single cardinality
// authority, so a route may not invent its own "exactly one" or "at least one"
// rule, and a cell may not be silently absent.
//
// Membership is decided by whether a route resolves a set of existing resources,
// not by whether it is canonical. `config render standalone|app` and `config
// apply` are canonical routes with no rows here on purpose: they take no
// reference, no `--project`, and no `--selector`, and they resolve nothing out
// of the registry. Their positional token names a generated artifact, which is a
// fixed two-member enum owned by the route, not a Projmux resource kind with a
// uid. A cardinality cell for them would be a rule about a set that never
// exists. TestCanonicalRoutesWithoutAResourceSetHaveNoMatrixRow keeps that
// judgment falsifiable rather than leaving it as an omission.
var canonicalRouteCardinalities = []routeCardinality{
	{"get projects", selector.Target{Verb: selector.VerbGet, Kind: coremetadata.KindProject, List: true}, selector.CardinalityAny},
	{"get windows", selector.Target{Verb: selector.VerbGet, Kind: coremetadata.KindWindow, List: true}, selector.CardinalityAny},
	{"get panes", selector.Target{Verb: selector.VerbGet, Kind: coremetadata.KindPane, List: true}, selector.CardinalityAny},
	{"get agents", selector.Target{Verb: selector.VerbGet, Kind: coremetadata.KindAgent, List: true}, selector.CardinalityAny},
	{"get pane", selector.Target{Verb: selector.VerbGet, Kind: coremetadata.KindPane}, selector.CardinalityExactOne},
	{"describe project", selector.Target{Verb: selector.VerbDescribe, Kind: coremetadata.KindProject}, selector.CardinalityExactOne},
	{"describe window", selector.Target{Verb: selector.VerbDescribe, Kind: coremetadata.KindWindow}, selector.CardinalityExactOne},
	{"describe pane", selector.Target{Verb: selector.VerbDescribe, Kind: coremetadata.KindPane}, selector.CardinalityExactOne},
	{"describe agent", selector.Target{Verb: selector.VerbDescribe, Kind: coremetadata.KindAgent}, selector.CardinalityExactOne},
	{"rename project", selector.Target{Verb: selector.VerbRename, Kind: coremetadata.KindProject}, selector.CardinalityExactOne},
	{"rename window", selector.Target{Verb: selector.VerbRename, Kind: coremetadata.KindWindow}, selector.CardinalityExactOne},
	{"rename pane", selector.Target{Verb: selector.VerbRename, Kind: coremetadata.KindPane}, selector.CardinalityExactOne},
	{"rebind project", selector.Target{Verb: selector.VerbRebind, Kind: coremetadata.KindProject}, selector.CardinalityExactOne},
	{"delete window", selector.Target{Verb: selector.VerbDelete, Kind: coremetadata.KindWindow}, selector.CardinalityAtLeastOne},
	{"delete pane", selector.Target{Verb: selector.VerbDelete, Kind: coremetadata.KindPane}, selector.CardinalityAtLeastOne},
	{"delete agent", selector.Target{Verb: selector.VerbDelete, Kind: coremetadata.KindAgent}, selector.CardinalityAtLeastOne},
	{"agent resume", selector.Target{Verb: selector.VerbResume, Kind: coremetadata.KindAgent}, selector.CardinalityExactOne},
	// The resource-backed create routes resolve three cells: the Project scope
	// they create below, the parent Windows they fan out over, and the anchor
	// Pane they split inside each of those Windows.
	{"create window", selector.Target{Verb: selector.VerbCreate, Kind: coremetadata.KindProject}, selector.CardinalityExactOne},
	{"create pane", selector.Target{Verb: selector.VerbCreate, Kind: coremetadata.KindWindow}, selector.CardinalityAtLeastOne},
	{"create pane anchor", selector.Target{Verb: selector.VerbCreate, Kind: coremetadata.KindPane}, selector.CardinalityExactOne},
	// The Agent create shares the per-Window anchor with `create pane` but owns
	// its own fan-out cell: it never resolves an existing Agent, so the cell it
	// enforces is over the Agents it produces, one per resolved target Window.
	{"create agent", selector.Target{Verb: selector.VerbCreate, Kind: coremetadata.KindAgent}, selector.CardinalityAtLeastOne},
	{"create agent anchor", selector.Target{Verb: selector.VerbCreate, Kind: coremetadata.KindPane}, selector.CardinalityExactOne},
}

// TestCanonicalRoutesAdoptTheDeclaredCardinalityMatrix proves the routes consume
// the shared matrix rather than a parallel per-route rule.
func TestCanonicalRoutesAdoptTheDeclaredCardinalityMatrix(t *testing.T) {
	t.Parallel()

	matrix := selector.Matrix()
	for _, route := range canonicalRouteCardinalities {
		declared, ok := matrix[route.target]
		if !ok {
			t.Fatalf("route %q resolves %q, which the cardinality matrix does not declare", route.spelling, route.target)
		}
		if declared != route.want {
			t.Fatalf("route %q cardinality = %q, want %q", route.spelling, declared, route.want)
		}
		if lookup, ok := selector.CardinalityFor(route.target); !ok || lookup != declared {
			t.Fatalf("route %q lookup = %q/%v, want %q", route.spelling, lookup, ok, declared)
		}
	}
}

// TestEveryCanonicalRouteCardinalityIsEnforcedAtTheRoute closes the loop: each
// declared cell is actually enforced by the handler that owns it, so the matrix
// and the behavior cannot drift apart.
func TestEveryCanonicalRouteCardinalityIsEnforcedAtTheRoute(t *testing.T) {
	t.Parallel()

	// An ambiguous reference is the shared probe: an exact-one route must reject
	// it, and a 0..N list route must accept it.
	for _, test := range []struct {
		spelling string
		run      func(t *testing.T, store *fakeResourceStore) error
		wantFail bool
	}{
		{
			spelling: "get windows",
			run: func(t *testing.T, store *fakeResourceStore) error {
				_, _, err := runRoute(t, newTestListGetCommand(t, store), "windows", "--window", "main")
				return err
			},
		},
		{
			spelling: "get agents",
			run: func(t *testing.T, store *fakeResourceStore) error {
				_, _, err := runRoute(t, newTestListGetCommand(t, store), "agents")
				return err
			},
		},
		{
			spelling: "describe window",
			run: func(t *testing.T, store *fakeResourceStore) error {
				_, _, err := runRoute(t, newTestDescribeCommand(t, store), "window", "main")
				return err
			},
			wantFail: true,
		},
		{
			spelling: "describe agent",
			run: func(t *testing.T, store *fakeResourceStore) error {
				_, _, err := runRoute(t, newTestDescribeCommand(t, store), "agent", "codex")
				return err
			},
			wantFail: true,
		},
		{
			spelling: "rename window",
			run: func(t *testing.T, store *fakeResourceStore) error {
				_, _, err := runRoute(t, newTestRenameCommand(store), "window", "main", "--name", "x")
				return err
			},
			wantFail: true,
		},
		{
			spelling: "rebind project",
			run: func(t *testing.T, store *fakeResourceStore) error {
				_, _, err := runRoute(t, newTestRebindCommand(store), "project", "--root", "/srv/alpha")
				return err
			},
			wantFail: true,
		},
		{
			spelling: "delete pane",
			run: func(t *testing.T, store *fakeResourceStore) error {
				// 1..N accepts the ambiguous set; only an empty set fails.
				_, _, err := runRoute(t, newTestDeleteCommand(store, false, false, nil), "pane", "zsh", "--yes")
				return err
			},
		},
		{
			spelling: "agent resume",
			run: func(t *testing.T, store *fakeResourceStore) error {
				// `codex` names an Agent in two Windows, so the exact-one cell is
				// what stops resume from picking one.
				cmd, _, _ := newTestAgentCommand(t, store)
				_, _, err := runRoute(t, cmd, "resume", "codex")
				return err
			},
			wantFail: true,
		},
		{
			spelling: "create window",
			run: func(t *testing.T, store *fakeResourceStore) error {
				// The exact-one Project cell is what stops a create from fanning
				// out below every Project that matched.
				create, _ := newTestResourceCreateCommand(t, store, newFakeTmux())
				_, _, err := runRoute(t, create, "window", "--project", "nosuch")
				return err
			},
			wantFail: true,
		},
		{
			spelling: "create pane",
			run: func(t *testing.T, store *fakeResourceStore) error {
				// 1..N accepts the whole Project scope; only an empty Window set
				// fails.
				create, _ := newTestResourceCreateCommand(t, store, newFakeTmux())
				_, _, err := runRoute(t, create, "pane", "--project", "alpha")
				return err
			},
		},
		{
			spelling: "create pane empty",
			run: func(t *testing.T, store *fakeResourceStore) error {
				create, _ := newTestResourceCreateCommand(t, store, newFakeTmux())
				_, _, err := runRoute(t, create, "pane", "--project", "alpha", "--selector", "role=nosuch")
				return err
			},
			wantFail: true,
		},
		{
			spelling: "create agent",
			run: func(t *testing.T, store *fakeResourceStore) error {
				// 1..N accepts the whole Project scope: one Agent per Window.
				create, _ := newTestAgentCreateCommand(t, store, newFakeTmux())
				_, _, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha")
				return err
			},
		},
		{
			spelling: "create agent empty",
			run: func(t *testing.T, store *fakeResourceStore) error {
				// Only an empty target set fails. This is the row that makes the
				// declared <create, Agent> value load bearing: relaxing the cell
				// to 0..N would turn this into a success that created nothing.
				create, _ := newTestAgentCreateCommand(t, store, newFakeTmux())
				_, _, err := runRoute(t, create,
					"agent", "--provider", "codex", "--project", "alpha", "--selector", "role=nosuch")
				return err
			},
			wantFail: true,
		},
		{
			spelling: "create agent anchor",
			run: func(t *testing.T, store *fakeResourceStore) error {
				// Two anchors inside one Window violate the exact-one Pane cell,
				// which the Agent route shares with `create pane`.
				create, _ := newTestAgentCreateCommand(t, store, newFakeTmux())
				_, _, err := runRoute(t, create, "agent", "--provider", "codex",
					"--project", "alpha", "--window", "main", "--pane", "zsh", "--pane", "log")
				return err
			},
			wantFail: true,
		},
		{
			spelling: "create pane anchor",
			run: func(t *testing.T, store *fakeResourceStore) error {
				// Two anchors inside one Window violate the exact-one Pane cell.
				create, _ := newTestResourceCreateCommand(t, store, newFakeTmux())
				_, _, err := runRoute(t, create,
					"pane", "--project", "alpha", "--window", "main", "--pane", "zsh", "--pane", "log")
				return err
			},
			wantFail: true,
		},
		{
			spelling: "delete pane empty",
			run: func(t *testing.T, store *fakeResourceStore) error {
				_, _, err := runRoute(t, newTestDeleteCommand(store, false, false, nil), "pane", "--project", "gone", "--yes")
				return err
			},
			wantFail: true,
		},
	} {
		t.Run(test.spelling, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			err := test.run(t, store)
			if test.wantFail {
				if err == nil {
					t.Fatalf("%s accepted a cardinality violation", test.spelling)
				}
				if !IsUsageError(err) {
					t.Fatalf("%s cardinality failure is not a usage error: %v", test.spelling, err)
				}
				if !strings.Contains(err.Error(), "want ") {
					t.Fatalf("%s error does not report the required cardinality: %v", test.spelling, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s rejected a valid cardinality: %v", test.spelling, err)
			}
		})
	}
}

// TestCanonicalRoutesWithoutAResourceSetHaveNoMatrixRow states the negative half
// of the adoption record, so "this route needs no cardinality cell" is a checked
// claim rather than a gap.
//
// A route earns a matrix row by resolving existing resources. The config-domain
// routes do not: they accept no reference and no selector, and the only
// positional they read is a generated-artifact name. Adding a row for one of
// them would declare a cardinality over a target set the handler never builds,
// which is how a shared authority quietly turns into decoration.
func TestCanonicalRoutesWithoutAResourceSetHaveNoMatrixRow(t *testing.T) {
	t.Parallel()

	declared := map[string]bool{}
	for _, route := range canonicalRouteCardinalities {
		declared[route.spelling] = true
	}
	for _, spelling := range []string{
		"config render",
		"config render standalone",
		"config render app",
		"config apply",
	} {
		if declared[spelling] {
			t.Errorf("route %q declares a cardinality cell but resolves no resource set", spelling)
		}
	}

	// Non-vacuity: the record must still cover the routes that DO resolve a set,
	// or the exclusion above would be trivially satisfied by an empty record.
	for _, spelling := range []string{"get panes", "describe pane", "delete pane", "agent resume"} {
		if !declared[spelling] {
			t.Fatalf("the adoption record lost %q; the exclusion assertion is vacuous without it", spelling)
		}
	}
}
