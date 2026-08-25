package app

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The C-5 sweep table.
//
// C-5 says a Registry has two root kinds, Project and ControlSession, and that
// every projection has to know both. The cost of that guarantee is paid one
// traversal at a time, and until this table existed nobody could say how many
// traversals there were, let alone which of them had paid. `#705` fixed exactly
// one -- the commit projection -- by inspection.
//
// So the unit of record here is a *site*: one non-test function that ranges
// over a root slice. The table below classifies every one of them, and
// TestRootKindProjectionSitesAreExhaustivelyClassified re-derives the site set
// from the source with go/ast on every run. A traversal added, removed, moved,
// or renamed fails that test until it is classified here, which is what makes
// the table a contract rather than a snapshot of one afternoon's grep.

// rootKindVerdict is how one site answers "does this see both root kinds".
type rootKindVerdict string

const (
	// rootKindBoth is a whole-Registry traversal that visits both root kinds in
	// the same function. This is what C-5 Enforcement means for a projection.
	rootKindBoth rootKindVerdict = "both-kinds"
	// rootKindPaired is a kind-scoped lookup whose ControlSession half is a
	// separate named function. The pair covers the roots; neither half alone
	// claims to.
	rootKindPaired rootKindVerdict = "paired"
	// rootKindProjectOnly is a traversal whose subject is Project semantics --
	// a root path, a Project session claim, a Project pin. A ControlSession has
	// no path and is not a managed root, so it is structurally absent rather
	// than forgotten. C-5's Non-Guarantee row is the licence for these.
	rootKindProjectOnly rootKindVerdict = "project-only"
	// rootKindGap is a whole-Registry traversal that still reads Project as the
	// only root. Every one of these is a live C-5 defect; the owner column says
	// which surface has to close it.
	rootKindGap rootKindVerdict = "gap"
)

// rootKindProjectionSite is one row of the sweep table.
type rootKindProjectionSite struct {
	// File is the repo-relative path of the traversal.
	File string
	// Func is the enclosing top-level function, `Type.Method` for a method.
	Func string
	// Source is the value whose `.Projects` field is ranged over. Only
	// `Registry` is a Registry root traversal; the rest read a projection that
	// has already chosen its roots upstream.
	Source string
	// Verdict is the classification.
	Verdict rootKindVerdict
	// Pair is the sibling function covering ControlSession, for rootKindPaired.
	Pair string
	// Owner is the surface answerable for a rootKindGap. Empty otherwise.
	Owner string
	// Why states the reason the verdict is what it is.
	Why string
}

// rootKindProjectionSites is the sweep. Sorted by file then function; the
// exhaustiveness test re-sorts anyway, so the order here is for reading.
var rootKindProjectionSites = []rootKindProjectionSite{
	{
		File: "internal/core/metadata/schema.go", Func: "migrateV1ToV2",
		Source: "Registry", Verdict: rootKindProjectOnly,
		Why: "repairs the schema-v2 canonical shell anchor for Project roots; ControlSession remains a non-materializable control-plane root",
	},
	{
		File: "internal/app/control_session.go", Func: "controlTargetControllerState",
		Source: "Registry", Verdict: rootKindBoth,
		Why: "builds one declared control-target plan from exact ControlSession claimants and explicitly classifies any mirrored Project uid as a conflict",
	},
	{
		File: "internal/app/agent_workspace.go", Func: "resolveAgentWorkspaceFor",
		Source: "Registry", Verdict: rootKindProjectOnly,
		Why: "resolves an Agent workspace against Project roots; a ControlSession has no root to offer",
	},
	{
		File: "internal/app/doctor_registry.go", Func: "doctorCommand.evaluateRegistryInvariants",
		Source: "Registry", Verdict: rootKindProjectOnly,
		Why: "audits Project session projections and roots; the control-root invariants it needs live in Registry.Validate",
	},
	{
		File: "internal/app/pin_authority.go", Func: "projectRefsOf",
		Source: "Registry", Verdict: rootKindProjectOnly,
		Why: "a pin is uid-or-path and both spellings need a root; pins.KindProject is the only managed pin kind",
	},
	{
		File: "internal/app/pin_authority.go", Func: "pinAuthority.selection",
		Source: "Resolver", Verdict: rootKindProjectOnly,
		Why: "reads the ProjectRef set projectRefsOf already scoped",
	},
	{
		File: "internal/app/pin_authority.go", Func: "pinAuthority.pinnedRows",
		Source: "Resolver", Verdict: rootKindProjectOnly,
		Why: "reads the ProjectRef set projectRefsOf already scoped",
	},
	{
		File: "internal/app/pin_authority.go", Func: "pinAuthority.discoveryPaths",
		Source: "Resolver", Verdict: rootKindProjectOnly,
		Why: "contributes Project roots to discovery; a ControlSession contributes no path by construction",
	},
	{
		File: "internal/app/project_registry.go", Func: "registryReconciler.projectsBySessionName",
		Source: "Registry", Verdict: rootKindProjectOnly,
		Why: "indexes Project session claims; a control session is claimed by exact name through ControlSessionBySession",
	},
	{
		File: "internal/app/project_registry.go", Func: "registryReconciler.refreshSessionProjections",
		Source: "Registry", Verdict: rootKindProjectOnly,
		Why: "refreshes ProjectStatus.Session; ControlSessionSpec carries the session name itself and has no status projection to refresh",
	},
	{
		File: "internal/app/prune_project.go", Func: "pruneProjectCandidates",
		Source: "Registry", Verdict: rootKindProjectOnly,
		Why: "selects on the MissingRoot condition, which only a resource with spec.root can carry",
	},
	{
		File: "internal/app/resource_controller.go", Func: "graphHasOfflineRow",
		Source: "Graph", Verdict: rootKindBoth,
		Why: "checks offline rows beneath both graph root kinds",
	},
	{
		File: "internal/app/resource_reconcile_plan.go", Func: "resourceRegistryProjectGraph",
		Source: "Registry", Verdict: rootKindBoth,
		Why: "carries ControlSession roots whole so the scope projection never holds a root Validate rejects (#705)",
	},
	{
		File: "internal/app/resource_reconcile_plan.go", Func: "mergeScopedResourceRegistry",
		Source: "Registry", Verdict: rootKindProjectOnly,
		Why: "builds the Project removal set for the commit merge; control graphs are outside the mutation scope and are carried by Clone",
	},
	{
		File: "internal/app/resource_reconcile_plan.go", Func: "resourceRegistryWithoutProjectGraphs",
		Source: "Registry", Verdict: rootKindProjectOnly,
		Why: "the exact complement of the Project removal set; a control root is never in that set so it is never removed",
	},
	{
		File: "internal/app/resource_reconcile_plan.go", Func: "retainReservedResourceNames",
		Source: "Registry", Verdict: rootKindBoth,
		Why: "the reservation table names both root kinds, so dropping unbacked rows has to know both (#705)",
	},
	{
		File: "internal/app/resource_reconcile_plan.go", Func: "resourceSessionProjectClaims",
		Source: "Registry", Verdict: rootKindProjectOnly,
		Why: "counts Projects claiming one session name; a control session is not a Project claimant",
	},
	{
		File: "internal/app/resource_reconcile_plan.go", Func: "registryResourceRecords",
		Source: "Registry", Verdict: rootKindBoth,
		Why: "the flat record projection feeding registryUIDSet and registryReconcileItems; both ask a whole-Registry question",
	},
	{
		File: "internal/app/resources.go", Func: "resourceProjectRows",
		Source: "Snapshot", Verdict: rootKindProjectOnly,
		Why: "renders the `get projects` table, a kind-scoped route",
	},
	{
		File: "internal/app/resources.go", Func: "resourceSharedProjectUsage",
		Source: "Snapshot", Verdict: rootKindProjectOnly,
		Why: "sums Project usage rows for the Project view",
	},
	{
		File: "internal/app/sessions_registry.go", Func: "attributeSessionSummaries",
		Source: "Graph", Verdict: rootKindBoth,
		Why: "attributes exact session bindings through both graph root kinds while keeping Home out of Project rows",
	},
	{
		File: "internal/core/metadata/agent.go", Func: "Mutator.DeleteProject",
		Source: "Registry", Verdict: rootKindProjectOnly,
		Why: "deletes one Project and its owned graph; DeleteControlSession is not a route that exists",
	},
	{
		File: "internal/core/metadata/lifecycle_decision.go", Func: "PlanProjectFreshReplacement",
		Source: "Registry", Verdict: rootKindProjectOnly,
		Why: "proves exactly one Project claims the replaced filesystem root; ControlSessions have no root path by construction",
	},
	{
		File: "internal/core/metadata/model.go", Func: "Registry.Project",
		Source: "Registry", Verdict: rootKindPaired, Pair: "Registry.ControlSession",
		Why: "uid lookup, one function per root kind",
	},
	{
		File: "internal/core/metadata/model.go", Func: "Registry.ProjectByRoot",
		Source: "Registry", Verdict: rootKindProjectOnly,
		Why: "C-5 Non-Guarantee names ProjectByRoot explicitly: a ControlSession has no root and must never appear here",
	},
	{
		File: "internal/core/metadata/model.go", Func: "Registry.ProjectByName",
		Source: "Registry", Verdict: rootKindPaired, Pair: "Registry.ControlSessionBySession",
		Why: "identity lookup, one function per root kind; a control session's identity is its exact tmux session name",
	},
	{
		File: "internal/core/metadata/mutator.go", Func: "Mutator.ObserveProjectRoots",
		Source: "Registry", Verdict: rootKindProjectOnly,
		Why: "stats spec.root to maintain the MissingRoot condition; there is no control-root path to stat",
	},
	{
		File: "internal/core/metadata/schema.go", Func: "Registry.rebuildMissingReservations",
		Source: "Registry", Verdict: rootKindBoth,
		Why: "both root kinds hold a registry-scoped name reservation",
	},
	{
		File: "internal/core/metadata/schema.go", Func: "Registry.normalized",
		Source: "Registry", Verdict: rootKindBoth,
		Why: "stamps apiVersion/kind and UTC timestamps on every resource of every kind",
	},
	{
		File: "internal/core/metadata/snapshot.go", Func: "resolveSnapshotProject",
		Source: "Registry", Verdict: rootKindProjectOnly,
		Why: "matches a session-state snapshot to a Project root; snapshot replay has no control-session shape",
	},
	{
		File: "internal/core/metadata/snapshot_projection.go", Func: "PlanSnapshotProjection",
		Source: "Registry", Verdict: rootKindBoth,
		Why: "replaces one exact Project subtree while preserving every unrelated Project and ControlSession root",
	},
	{
		File: "internal/core/metadata/snapshot_projection.go", Func: "canonicalProjectShell",
		Source: "Registry", Verdict: rootKindBoth,
		Why: "chooses or allocates the exact minimum Project shell while rejecting uid collision with either root kind",
	},
	{
		File: "internal/core/metadata/transaction.go", Func: "Registry.removeCreated",
		Source: "Registry", Verdict: rootKindBoth,
		Why: "rollback removes whatever the transaction minted, and BindControlSession mints control roots",
	},
	{
		File: "internal/core/metadata/validate.go", Func: "Registry.Validate",
		Source: "Registry", Verdict: rootKindBoth,
		Why: "uid uniqueness and owner legality span both root kinds; Window owners are KindProject or KindControlSession",
	},
	{
		File: "internal/core/metadata/validate.go", Func: "Registry.validateReservations",
		Source: "Registry", Verdict: rootKindBoth,
		Why: "the reservation table must agree with the resource set in both directions, for both root kinds",
	},
	{
		File: "internal/core/pins/resolve.go", Func: "Resolver.Resolve",
		Source: "Resolver", Verdict: rootKindProjectOnly,
		Why: "migrates legacy path pins onto Project uids; a path can only ever name a Project root",
	},
	{
		File: "internal/core/registryview/view.go", Func: "builder.projects",
		Source: "Graph", Verdict: rootKindBoth,
		Why: "projects both root graphs while Home's user-facing row remains sidebar chrome",
	},
	{
		File: "internal/core/resourcegraph/graph.go", Func: "newResolver",
		Source: "Registry", Verdict: rootKindBoth,
		Why: "indexes both Registry root kinds before resolving exact claims",
	},
	{
		File: "internal/core/resourcegraph/divergence.go", Func: "ClassifyDivergences",
		Source: "Graph", Verdict: rootKindBoth,
		Why: "classifies divergence rows rooted beneath both Registry root kinds",
	},
	{
		File: "internal/core/resourcegraph/resolve.go", Func: "resolver.buildRegistryNodes",
		Source: "Registry", Verdict: rootKindBoth,
		Why: "emits nodes for both roots and carries root kind/uid through descendants",
	},
	{
		File: "internal/core/runtimediag/report.go", Func: "newIndex",
		Source: "Graph", Verdict: rootKindBoth,
		Why: "indexes both graph roots so a managed Home session resolves to its ControlSession identity",
	},
}

// phase4Boundary is the exact product surface this slice may edit. A gap inside
// it is this slice's to close; a gap outside it is a finding handed back with
// its path, not an edit made anyway.
var phase4Boundary = []string{
	"internal/app/resource_reconcile",
	"internal/app/prune_project",
	"internal/app/registry_topology_materialize",
	"internal/core/pins/",
}

func withinPhase4Boundary(file string) bool {
	for _, prefix := range phase4Boundary {
		if strings.HasPrefix(file, prefix) {
			return true
		}
	}
	return false
}

// TestRootKindProjectionSitesAreExhaustivelyClassified is acceptance criterion
// 1. The table is not allowed to be a list somebody kept up to date by hand: the
// site set is re-derived from the source on every run and has to match exactly.
func TestRootKindProjectionSitesAreExhaustivelyClassified(t *testing.T) {
	t.Parallel()

	scanned := scanRootSliceTraversals(t)

	tabled := map[string]rootKindProjectionSite{}
	for _, site := range rootKindProjectionSites {
		key := site.File + ":" + site.Func
		if previous, ok := tabled[key]; ok {
			t.Fatalf("%s is listed twice in the sweep table (%q and %q)", key, previous.Why, site.Why)
		}
		tabled[key] = site
	}

	for key := range scanned.projects {
		if _, ok := tabled[key]; !ok {
			t.Errorf("%s ranges over a root slice and is not classified in rootKindProjectionSites", key)
		}
	}
	for key := range tabled {
		if !scanned.projects[key] {
			t.Errorf("%s is classified in rootKindProjectionSites but no longer ranges over `.Projects`", key)
		}
	}

	for key, site := range tabled {
		switch site.Verdict {
		case rootKindBoth:
			if !scanned.controls[key] {
				t.Errorf("%s is classified %s but does not range over `.ControlSessions`", key, site.Verdict)
			}
		case rootKindPaired:
			pair := site.File + ":" + site.Pair
			if site.Pair == "" {
				t.Errorf("%s is classified %s with no Pair", key, site.Verdict)
			} else if !scanned.controls[pair] {
				t.Errorf("%s names pair %s, which does not range over `.ControlSessions`", key, pair)
			}
		case rootKindProjectOnly, rootKindGap:
			if scanned.controls[key] {
				t.Errorf("%s is classified %s but does range over `.ControlSessions`", key, site.Verdict)
			}
		default:
			t.Errorf("%s carries unknown verdict %q", key, site.Verdict)
		}
		if strings.TrimSpace(site.Why) == "" {
			t.Errorf("%s carries no reason", key)
		}
		if (site.Verdict == rootKindGap) != (strings.TrimSpace(site.Owner) != "") {
			t.Errorf("%s must name an Owner if and only if it is a %s", key, rootKindGap)
		}
	}

	// Every function that visits ControlSessions has to be reachable from the
	// table too, either as a site or as the named pair of one, or a root-kind
	// traversal exists that the sweep cannot see.
	pairs := map[string]bool{}
	for _, site := range rootKindProjectionSites {
		if site.Pair != "" {
			pairs[site.File+":"+site.Pair] = true
		}
	}
	for key := range scanned.controls {
		if scanned.projects[key] || pairs[key] {
			continue
		}
		t.Errorf("%s ranges over `.ControlSessions` and appears in the sweep table neither as a site nor as a pair", key)
	}
}

// TestRootKindProjectionSweepLeavesNoGapInsideThePhase4Boundary is acceptance
// criterion 1's other half. A gap this slice owns is a defect it did not fix; a
// gap it does not own is a finding, and the test prints the exact paths so the
// hand-back names them rather than describing them.
func TestRootKindProjectionSweepLeavesNoGapInsideThePhase4Boundary(t *testing.T) {
	t.Parallel()

	var outside []string
	for _, site := range rootKindProjectionSites {
		if site.Verdict != rootKindGap {
			continue
		}
		if withinPhase4Boundary(site.File) {
			t.Errorf("%s:%s is a root-kind gap inside the Phase 4 boundary and must be closed by this slice", site.File, site.Func)
			continue
		}
		outside = append(outside, fmt.Sprintf("%s:%s owner=%s -- %s", site.File, site.Func, site.Owner, site.Why))
	}
	slices.Sort(outside)
	if len(outside) > 0 {
		t.Logf("root-kind gaps outside the Phase 4 boundary (hand back, do not edit):\n%s", strings.Join(outside, "\n"))
	}
}

// TestRootKindProjectionSweepTableIsPrintable renders the sweep. The rendering
// is the deliverable: `go test ./internal/app -run RootKindProjectionSweepTable
// -v` prints the whole table with no other tooling.
func TestRootKindProjectionSweepTableIsPrintable(t *testing.T) {
	t.Parallel()

	table := renderRootKindProjectionSweep()
	t.Logf("C-5 root kind projection sweep\n%s", table)

	counts := map[rootKindVerdict]int{}
	for _, site := range rootKindProjectionSites {
		counts[site.Verdict]++
	}
	for verdict, want := range map[rootKindVerdict]int{
		rootKindBoth:        18,
		rootKindPaired:      2,
		rootKindProjectOnly: 21,
		rootKindGap:         0,
	} {
		if counts[verdict] != want {
			t.Errorf("%s rows = %d, want %d; update the count with the table and say why in the commit", verdict, counts[verdict], want)
		}
	}
	if got, want := len(rootKindProjectionSites), 41; got != want {
		t.Errorf("sweep rows = %d, want %d", got, want)
	}
	for _, want := range []string{"SITE", "SOURCE", "KIND HANDLING", "NOTE"} {
		if !strings.Contains(table, want) {
			t.Errorf("rendered sweep is missing column %q", want)
		}
	}
}

// renderRootKindProjectionSweep renders the sweep as a fixed-width table.
func renderRootKindProjectionSweep() string {
	rows := make([][4]string, 0, len(rootKindProjectionSites)+1)
	rows = append(rows, [4]string{"SITE", "SOURCE", "KIND HANDLING", "NOTE"})

	sites := slices.Clone(rootKindProjectionSites)
	sort.SliceStable(sites, func(i, j int) bool {
		if sites[i].File != sites[j].File {
			return sites[i].File < sites[j].File
		}
		return sites[i].Func < sites[j].Func
	})
	for _, site := range sites {
		handling := string(site.Verdict)
		switch {
		case site.Verdict == rootKindPaired:
			handling += " with " + site.Pair
		case site.Verdict == rootKindGap:
			handling += " owner=" + site.Owner
		}
		rows = append(rows, [4]string{site.File + ":" + site.Func, site.Source, handling, site.Why})
	}

	width := [4]int{}
	for _, row := range rows {
		for i, cell := range row {
			width[i] = max(width[i], len(cell))
		}
	}
	var b strings.Builder
	for _, row := range rows {
		for i, cell := range row {
			if i == len(row)-1 {
				b.WriteString(cell)
				break
			}
			b.WriteString(cell)
			b.WriteString(strings.Repeat(" ", width[i]-len(cell)+2))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// rootSliceTraversals is the derived site set: which non-test functions range
// over `.Projects`, and which range over `.ControlSessions`.
type rootSliceTraversals struct {
	projects map[string]bool
	controls map[string]bool
}

// scanRootSliceTraversals re-derives the sweep's subject from the source tree.
//
// It is deliberately syntactic. A type-checked pass would tell us the receiver
// really is a metadata.Registry, but it would also make this test the only one
// in the package that needs the module to load, and the question it answers --
// "which functions walk a root slice" -- is a question about the text. The
// Source column carries the type judgement instead, where a human can read it.
func scanRootSliceTraversals(t *testing.T) rootSliceTraversals {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	out := rootSliceTraversals{projects: map[string]bool{}, controls: map[string]bool{}}
	fset := token.NewFileSet()

	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".wt", "vendor", "node_modules", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := rel + ":" + funcSiteName(fn)
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				stmt, ok := node.(*ast.RangeStmt)
				if !ok {
					return true
				}
				selector, ok := stmt.X.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch selector.Sel.Name {
				case "Projects":
					out.projects[key] = true
				case "ControlSessions":
					out.controls[key] = true
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan repository: %v", err)
	}
	if len(out.projects) == 0 {
		t.Fatalf("scanned %s and found no root traversal at all; the walk is broken", root)
	}
	return out
}

// funcSiteName renders a declaration as `Name` or `Type.Method`.
func funcSiteName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}
