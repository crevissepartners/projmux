package cli

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestEveryCanonicalSpellingIsExecutableToday is the acceptance assertion of the
// public-spelling Phase, and it is the whole point of the manifest audit: after
// this Phase the canonical manifest holds no spelling argv cannot reach.
//
// Before, fourteen spellings resolved to nothing at all. Two of them (`config
// render`, `config apply`) named real behavior that simply had no public door,
// and this Phase built it. The other twelve named renames that each needed their
// own slice, so they were deleted rather than parked; keeping them would have
// left a plan in the tree that nobody owned and no test could falsify.
//
// A spelling is executable when every one of its tokens resolves against the
// command tree. Hidden routes count -- `internal tmux` is a real node a
// generated payload invokes -- but a partial resolution does not, because
// "`config` resolves and `render` is ignored" is exactly the failure the naive
// version of this check would miss.
func TestEveryCanonicalSpellingIsExecutableToday(t *testing.T) {
	t.Parallel()

	canonical := CanonicalRoutes()
	if len(canonical) == 0 {
		t.Fatal("the canonical manifest is empty, so this assertion proves nothing")
	}
	var unreachable []string
	for _, route := range canonical {
		tokens := strings.Fields(route.Spelling)
		if len(tokens) == 0 {
			t.Errorf("canonical route with an empty spelling: %#v", route)
			continue
		}
		path, _, ok := Resolve(tokens)
		if !ok || len(path) != len(tokens) {
			unreachable = append(unreachable, route.Spelling)
		}
	}
	if len(unreachable) > 0 {
		t.Fatalf("the canonical manifest holds %d spelling(s) argv cannot reach: %v\n"+
			"a manifest-only spelling is a promise with no owner; either build it or delete it",
			len(unreachable), unreachable)
	}

	// Non-vacuity: the walk must actually have exercised a multi-token spelling
	// and a hidden one, or a resolver that answered "ok" for everything would
	// pass.
	if _, _, ok := Resolve([]string{"config", "render"}); !ok {
		t.Fatal("`config render` does not resolve; the check above cannot be trusted")
	}
	if _, _, ok := Resolve([]string{"nosuch", "route"}); ok {
		t.Fatal("Resolve accepts an unknown route, so this check is vacuous")
	}
}

// targetStateSummary is one canonical summary that deliberately describes more
// than the handler does today.
type targetStateSummary struct {
	// summary is the exact canonical string, pinned so that editing it forces
	// whoever edits it back through this record.
	summary string
	// owner names the track building the missing half. An entry with no owner
	// is an abandoned plan, and abandoned plans do not live in the manifest.
	owner string
}

// canonicalTargetStateSummaries is the closed record of canonical summaries that
// outrun the shipped handler, each with the track that owns the rest.
//
// This record is the line between a deferred feature and an abandoned plan.
// Every entry names work an owning track is actually building, and every one has
// a command-tree summary in catalog.go stating the half that ships -- which is
// the string users read, in help and in the generated reference. A summary that
// belonged to nobody was corrected or deleted instead. `tag project`'s "Manage
// persistent Project tags" is the worked example: it was retired rather than
// recorded here, because a persistent Project-metadata tag is a decision that
// was made against, not a Phase that has not landed.
var canonicalTargetStateSummaries = map[string]targetStateSummary{
	"agent resume": {
		summary: "Rebind an Offline or Failed Agent to a new managed Pane",
		owner:   "runtime materialization -- the rebind onto a new managed Pane",
	},
	"delete pane": {
		summary: "Delete a Pane resource and its live binding",
		owner:   "runtime materialization -- the tmux call that kills the live pane",
	},
	"restore snapshot": {
		summary: "Restore a saved session snapshot",
		owner:   "session-state -- replay outside --dry-run",
	},
}

// retiredCanonicalSummaries is the closed set of manifest strings the roadmap
// owner adjudicated out of existence: promises of capability nobody is building,
// and renaming plans handed back to the roadmap hub as Backlog candidates rather
// than parked here. Re-introducing any of them is what this list catches.
var retiredCanonicalSummaries = []string{
	// Abandoned by the 2026-08-15 product decision: a tag is an ephemeral
	// session-scoped marker and the persistent half will never be built.
	"Manage persistent Project tags",
	// Corrected: the pin store is a lines file of directory paths, not Projects.
	"Manage pinned Project resources",
	// Deleted: no non-interactive effective-config printer exists under any
	// spelling, and the namespace belongs to the Settings IA track.
	"Show effective projmux configuration",
	"Open the interactive configuration UI",
	// Deleted: each is a rename of a shipping route and needs its own slice.
	"Start or attach the app-owned tmux runtime",
	"Quit the app-owned tmux runtime",
	"Run read-only runtime and integration diagnostics",
	"Inspect live Project/Window/Pane CPU and RSS attribution",
	"Probe terminal key delivery",
	"Reopen the onboarding guide",
	"Create a pending notification row",
	"Create a session snapshot",
	"Acknowledge notification rows",
	"Reconcile the notification queue against live targets",
}

// promiseMarkers is the maintained vocabulary of forward-looking words. A
// one-line route summary is a description of what a command does; a summary that
// needs one of these is describing a plan instead, and a plan belongs in the
// roadmap rather than in a manifest the parser reads.
var promiseMarkers = []string{
	"not yet",
	"will be",
	"planned",
	"future",
	"eventually",
	"coming soon",
	"TODO",
	"persistent Project",
}

// TestNoCanonicalSummaryPromisesUnownedWork is the negative audit.
//
// Verbatim divergence from the command tree is deliberately NOT the test.
// Roughly thirty canonical summaries word the same behavior differently from
// their tree node ("Read or set Agent status state" against "Read or set the
// Agent status state"), and flagging those would drown the three that matter.
// What is checkable, and what actually distinguishes a promise from a
// description, is three things:
//
//  1. no summary carries forward-looking vocabulary,
//  2. no adjudicated-and-retired string comes back, and
//  3. every recorded target-state summary still has an owning track, still
//     reads exactly as recorded, and still has a command-tree node telling
//     users the honest, shorter story.
//
// Point 3 is the one that keeps the record from becoming a permission slip: an
// entry only stays legitimate while the surface users read is more conservative
// than the manifest.
func TestNoCanonicalSummaryPromisesUnownedWork(t *testing.T) {
	t.Parallel()

	canonical := CanonicalRoutes()
	if len(canonical) == 0 {
		t.Fatal("the canonical manifest is empty, so this assertion proves nothing")
	}

	recorded := map[string]bool{}
	for spelling := range canonicalTargetStateSummaries {
		recorded[spelling] = true
	}

	for _, route := range canonical {
		if slices.Contains(retiredCanonicalSummaries, route.Summary) {
			t.Errorf("canonical route %q reintroduces the retired summary %q", route.Spelling, route.Summary)
		}
		if recorded[route.Spelling] {
			continue
		}
		for _, marker := range promiseMarkers {
			if strings.Contains(route.Summary, marker) {
				t.Errorf("canonical route %q says %q, which promises rather than describes (marker %q); "+
					"correct it to today's behavior, delete it, or record the track that owns the missing half",
					route.Spelling, route.Summary, marker)
			}
		}
	}

	treeSummaries := map[string]bool{}
	walkRoutes(Routes(), func(_ []string, route Route) {
		treeSummaries[route.Summary] = true
	})

	for spelling, record := range canonicalTargetStateSummaries {
		if record.owner == "" {
			t.Errorf("target-state record for %q names no owning track; an unowned plan must be deleted, not recorded", spelling)
		}
		route, ok := LookupCanonicalRoute(spelling)
		if !ok {
			t.Errorf("canonicalTargetStateSummaries names %q, which is not a canonical route", spelling)
			continue
		}
		if route.Summary != record.summary {
			t.Errorf("canonical route %q says %q, but the target-state record pins %q; re-adjudicate before editing it",
				spelling, route.Summary, record.summary)
		}
		// The record is only defensible while the surface users read stays more
		// conservative than the manifest. The moment a command-tree node states
		// the target-state sentence verbatim, the promise has reached help and
		// the generated reference.
		if treeSummaries[record.summary] {
			t.Errorf("a command-tree node now states the target-state summary %q verbatim; "+
				"the honest half must stay in catalog.go", record.summary)
		}
	}

	// Non-vacuity: the scan has to be able to fire.
	probe := CanonicalRoute{Spelling: "probe", Summary: "Manage persistent Project tags"}
	if !slices.Contains(retiredCanonicalSummaries, probe.Summary) {
		t.Fatal("the retired-summary guard cannot fire; it proves nothing")
	}
	if len(canonicalTargetStateSummaries) == 0 {
		t.Fatal("no target-state summary is recorded, so point 3 proves nothing; " +
			"if the manifest has genuinely converged, delete this half with the reason recorded")
	}
}

// TestPublicConfigRouteReachesBothRenderTargetsAndApply pins the shape of the
// route this Phase adds, at the manifest level.
//
// The two render children are the load-bearing part. `internal tmux
// print-config` and `internal tmux print-app-config` both declare the canonical
// spelling `config render`, so a public `render` with one child would have left
// one generated artifact with no public spelling -- the gap this Phase closes,
// reopened one route over.
func TestPublicConfigRouteReachesBothRenderTargetsAndApply(t *testing.T) {
	t.Parallel()

	config, ok := LookupRoute("config")
	if !ok {
		t.Fatal("the public config route is missing")
	}
	if config.Hidden || config.Disposition != DispositionCanonical {
		t.Fatalf("config hidden=%v disposition=%q, want a public canonical node", config.Hidden, config.Disposition)
	}
	var children []string
	for _, child := range config.Children {
		children = append(children, child.Name)
	}
	if !reflect.DeepEqual(children, []string{"render", "apply"}) {
		t.Fatalf("config children = %v, want [render apply]", children)
	}

	render, ok := findChild(config, "render")
	if !ok {
		t.Fatal("config render is missing")
	}
	var artifacts []string
	for _, child := range render.Children {
		artifacts = append(artifacts, child.Name)
	}
	if !reflect.DeepEqual(artifacts, []string{"standalone", "app"}) {
		t.Fatalf("config render children = %v, want [standalone app]", artifacts)
	}

	// Every node of the new route resolves as a full path, so help, the
	// generated reference, and argv all agree it exists.
	for _, spelling := range [][]string{
		{"config"},
		{"config", "render"},
		{"config", "render", "standalone"},
		{"config", "render", "app"},
		{"config", "apply"},
	} {
		path, _, resolved := Resolve(spelling)
		if !resolved || !reflect.DeepEqual(path, spelling) {
			t.Errorf("%v resolved to %v (ok=%v), want the full path", spelling, path, resolved)
		}
	}

	// The hidden routes `make install` and every already-running tmux server
	// invoke are untouched: still present, still dispatchable, still hidden.
	for _, spelling := range [][]string{
		{"tmux", "print-config"},
		{"tmux", "print-app-config"},
		{"tmux", "install"},
		{"tmux", "install-app"},
		{"tmux", "apply"},
		{"internal", "tmux", "print-config"},
		{"internal", "tmux", "print-app-config"},
		{"internal", "tmux", "install"},
		{"internal", "tmux", "install-app"},
		{"internal", "tmux", "apply"},
	} {
		path, _, resolved := Resolve(spelling)
		if !resolved || !reflect.DeepEqual(path, spelling) {
			t.Errorf("%v resolved to %v (ok=%v), want the untouched hidden route", spelling, path, resolved)
		}
	}
	for _, token := range []string{"tmux", "internal"} {
		route, found := LookupRoute(token)
		if !found {
			t.Fatalf("hidden route %q was removed", token)
		}
		if !route.Hidden || route.Disposition != DispositionInternal {
			t.Fatalf("route %q hidden=%v disposition=%q, want an unchanged hidden internal node",
				token, route.Hidden, route.Disposition)
		}
	}

	// `install` and `install-app` deliberately have no public spelling. They are
	// installer plumbing, so their canonical home is the hidden namespace.
	for _, token := range []string{"install", "install-app"} {
		tmux, _ := LookupRoute("tmux")
		child, found := findChild(tmux, token)
		if !found {
			t.Fatalf("hidden route `tmux %s` was removed", token)
		}
		if !slices.Contains(child.Canonical, "internal tmux") {
			t.Errorf("`tmux %s` canonical = %v, want the hidden `internal tmux` home", token, child.Canonical)
		}
		if slices.Contains(child.Canonical, "config apply") || slices.Contains(child.Canonical, "config render") {
			t.Errorf("`tmux %s` claims a public config spelling that does not perform it", token)
		}
	}
}
