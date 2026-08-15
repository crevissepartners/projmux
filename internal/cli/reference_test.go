package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// referenceHeadingPattern matches one route heading in the generated page.
// The heading is the only place a route is declared, so parsing it back out is
// how the drift test learns what the checked-in file actually documents rather
// than what the renderer would have produced.
var referenceHeadingPattern = regexp.MustCompile("(?m)^#{2,6} `projmux ([^`]+)`$")

// referenceCanonicalPattern matches the canonical-spelling line of a section.
var referenceCanonicalPattern = regexp.MustCompile("(?m)^Canonical spelling: (.+)$")

// readGeneratedReference returns the checked-in generated reference.
func readGeneratedReference(t *testing.T) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), filepath.FromSlash(ReferencePath))
	data, err := os.ReadFile(path) // #nosec G304 -- repository-relative generated doc
	if err != nil {
		t.Fatalf("read %s: %v", ReferencePath, err)
	}
	return string(data)
}

// manifestRoutePaths returns every public route path in manifest order, parent
// before child. It lives in the test rather than in reference.go because the
// renderer has no use for it and an unreachable production helper is exactly
// what the deadcode gate exists to reject.
func manifestRoutePaths() [][]string {
	var out [][]string
	var walk func(prefix []string, nodes []Route)
	walk = func(prefix []string, nodes []Route) {
		for _, node := range nodes {
			path := append(append([]string{}, prefix...), node.Name)
			out = append(out, path)
			walk(path, node.Children)
		}
	}
	walk(nil, publicRoutes())
	return out
}

// TestGeneratedReferenceMatchesTheCommandManifest is the drift gate.
//
// The published reference is a build artifact of the command manifest, so a
// route, summary, usage line, output mode, or field projection that changes in
// catalog.go without a regeneration is a broken document. Making that a unit
// test failure rather than a review responsibility is what puts the check in
// CI: the `Unit Tests` job runs `make test`.
func TestGeneratedReferenceMatchesTheCommandManifest(t *testing.T) {
	t.Parallel()

	var want bytes.Buffer
	if err := RenderReference(&want); err != nil {
		t.Fatalf("RenderReference returned error: %v", err)
	}
	got := readGeneratedReference(t)
	if got == want.String() {
		return
	}
	gotLines := strings.Split(got, "\n")
	wantLines := strings.Split(want.String(), "\n")
	for i := range max(len(gotLines), len(wantLines)) {
		var gotLine, wantLine string
		if i < len(gotLines) {
			gotLine = gotLines[i]
		}
		if i < len(wantLines) {
			wantLine = wantLines[i]
		}
		if gotLine != wantLine {
			t.Fatalf("%s is stale at line %d; run %q\n--- checked in ---\n%s\n--- manifest ---\n%s",
				ReferencePath, i+1, ReferenceRegenCommand, gotLine, wantLine)
		}
	}
	t.Fatalf("%s is stale; run %q", ReferencePath, ReferenceRegenCommand)
}

// TestGeneratedReferenceIsDeterministic proves regeneration on an unchanged
// tree is a no-op, which is what makes the drift gate above a signal instead of
// noise. Rendering repeatedly must produce identical bytes: no timestamp, no
// version string, no map iteration order, no host path.
func TestGeneratedReferenceIsDeterministic(t *testing.T) {
	t.Parallel()

	var first, second bytes.Buffer
	if err := RenderReference(&first); err != nil {
		t.Fatalf("RenderReference returned error: %v", err)
	}
	if err := RenderReference(&second); err != nil {
		t.Fatalf("RenderReference returned error: %v", err)
	}
	if first.String() != second.String() {
		t.Fatal("RenderReference is not deterministic across two renders")
	}
	for _, forbidden := range []string{repoRoot(t), os.TempDir()} {
		if forbidden != "" && forbidden != "/" && strings.Contains(first.String(), forbidden) {
			t.Fatalf("the generated reference embeds the host path %q", forbidden)
		}
	}
}

// TestGeneratedReferenceRouteEntriesMatchTheCommandTreeExactly is the coverage
// assertion, stated as a two-way diff rather than a count.
//
// Left to right it proves nothing is missing: every public route and sub-route
// in the command tree -- canonical, shortcut, and compatibility alike -- has a
// section. Right to left it proves nothing is invented: every section in the
// document is a real node. A one-way check would pass a document that quietly
// documented a route the binary does not have, which is precisely the failure
// this Phase exists to prevent.
func TestGeneratedReferenceRouteEntriesMatchTheCommandTreeExactly(t *testing.T) {
	t.Parallel()

	var documented []string
	for _, match := range referenceHeadingPattern.FindAllStringSubmatch(readGeneratedReference(t), -1) {
		documented = append(documented, match[1])
	}
	var expected []string
	for _, path := range manifestRoutePaths() {
		expected = append(expected, strings.Join(path, " "))
	}

	if !reflect.DeepEqual(documented, expected) {
		documentedSet := map[string]bool{}
		for _, spelling := range documented {
			documentedSet[spelling] = true
		}
		expectedSet := map[string]bool{}
		for _, spelling := range expected {
			expectedSet[spelling] = true
		}
		var missing, extra []string
		for _, spelling := range expected {
			if !documentedSet[spelling] {
				missing = append(missing, spelling)
			}
		}
		for _, spelling := range documented {
			if !expectedSet[spelling] {
				extra = append(extra, spelling)
			}
		}
		t.Fatalf("generated reference route entries drifted from the command tree\nmissing: %v\nextra: %v\ndocumented order: %v\nmanifest order: %v",
			missing, extra, documented, expected)
	}

	// Non-vacuity: the diff above is only meaningful if the tree actually has
	// both a canonical and a shortcut public route to cover.
	var canonical, shortcut int
	for _, route := range publicRoutes() {
		switch route.Disposition {
		case DispositionCanonical:
			canonical++
		case DispositionShortcut:
			shortcut++
		case DispositionCompatibility, DispositionInternal:
		}
	}
	if canonical == 0 || shortcut == 0 {
		t.Fatalf("coverage diff is vacuous: canonical=%d shortcut=%d", canonical, shortcut)
	}
}

// TestGeneratedReferenceExcludesEveryHiddenRoute keeps the internal isolation
// boundary from leaking through the published document.
//
// Hiding a route from `projmux help` and then enumerating it in the reference
// would be the same disclosure with an extra step, so the document must contain
// neither a section for a hidden route nor a canonical-spelling pointer into
// one. The task guide is allowed to name these spellings; this page is not.
func TestGeneratedReferenceExcludesEveryHiddenRoute(t *testing.T) {
	t.Parallel()

	reference := readGeneratedReference(t)
	documented := map[string]bool{}
	for _, match := range referenceHeadingPattern.FindAllStringSubmatch(reference, -1) {
		documented[match[1]] = true
	}

	var hidden int
	for _, route := range routes {
		if !route.Hidden {
			continue
		}
		hidden++
		walkRoutes([]Route{route}, func(path []string, _ Route) {
			if documented[strings.Join(path, " ")] {
				t.Errorf("hidden route %q has a section in the public reference", strings.Join(path, " "))
			}
		})
	}
	if hidden == 0 {
		t.Fatal("no hidden route exists; this assertion would be vacuous")
	}

	for _, match := range referenceCanonicalPattern.FindAllStringSubmatch(reference, -1) {
		for spelling := range strings.SplitSeq(match[1], ", ") {
			spelling = strings.Trim(spelling, "`")
			spelling = strings.TrimPrefix(spelling, "projmux ")
			tokens := strings.Fields(spelling)
			if len(tokens) == 0 {
				t.Fatalf("unparseable canonical spelling line: %q", match[0])
			}
			top, ok := LookupRoute(tokens[0])
			if !ok {
				t.Errorf("the reference advertises canonical spelling %q, which is not a route", spelling)
				continue
			}
			if top.Hidden {
				t.Errorf("the reference advertises canonical spelling %q, which lives under a hidden route", spelling)
			}
			resolved, _, resolvedOK := Resolve(tokens)
			if !resolvedOK || len(resolved) != len(tokens) {
				t.Errorf("the reference advertises canonical spelling %q, which argv cannot reach today", spelling)
			}
		}
	}
}

// canonicalManifestOnlySummaries returns the canonical-manifest summaries that
// no command-tree node states verbatim.
//
// These are the strings that make the boundary necessary. `agent resume` is the
// clearest survivor: the canonical manifest calls it "Rebind an Offline or
// Failed Agent to a new managed Pane" while the handler resolves the Agent,
// applies the phase gate, and stops, because the rebind needs runtime
// materialization from another track. The command tree says what the route does
// today; the canonical manifest says what the contract will eventually require.
//
// The public-spelling Phase shrank this set on purpose. `tag project`'s "Manage
// persistent Project tags" used to be the headline entry; it was corrected
// rather than deferred, because the persistent Project-metadata tag is a
// permanently abandoned plan and not a feature waiting on a Phase.
func canonicalManifestOnlySummaries() []string {
	treeSummaries := map[string]bool{}
	walkRoutes(Routes(), func(_ []string, route Route) {
		treeSummaries[route.Summary] = true
	})
	var out []string
	for _, canonical := range CanonicalRoutes() {
		if !treeSummaries[canonical.Summary] {
			out = append(out, canonical.Summary)
		}
	}
	return out
}

// TestGeneratedReferenceCarriesNoCanonicalManifestOnlySummary is the enforced
// boundary between the two manifests.
//
// The generator reads the command tree and never the canonical manifest, but
// "the current code happens not to import it" is not a contract. This test
// falsifies the claim directly against the artifact: it derives every summary
// that exists only in canonical.go and asserts none of them reached the page.
// Point the renderer at the canonical manifest -- even for one route -- and
// this fails.
func TestGeneratedReferenceCarriesNoCanonicalManifestOnlySummary(t *testing.T) {
	t.Parallel()

	divergent := canonicalManifestOnlySummaries()
	if len(divergent) == 0 {
		t.Fatal("no canonical-manifest-only summary exists, so this assertion proves nothing; " +
			"if the two manifests have genuinely converged, delete this test with the reason recorded")
	}

	// The known divergences the roadmap owner still has to close, each naming a
	// feature an owning track is building rather than an abandoned plan. Pinning
	// them keeps the test from silently degrading into a tautology if the derived
	// set ever shrinks to wording differences alone: if one of these is corrected
	// or deleted, whoever does it has to come back here and re-derive the
	// boundary rather than let the assertion quietly cover nothing.
	knownTargetStateSummaries := []string{
		// Owned by the runtime materialization track.
		"Rebind an Offline or Failed Agent to a new managed Pane",
		"Delete a Pane resource and its live binding",
		// Owned by the session-state track.
		"Restore a saved session snapshot",
	}
	for _, summary := range knownTargetStateSummaries {
		if !slices.Contains(divergent, summary) {
			t.Fatalf("the canonical manifest no longer diverges on %q; re-derive the boundary before relaxing it", summary)
		}
	}

	// Compare on whole rendered strings rather than on substrings. Several
	// command-tree summaries legitimately start with the canonical wording and
	// then say more -- `create agent` extends "Create an Agent and its managed
	// Pane" with the `--provider` and `--project` requirements -- so a substring
	// match would flag the honest, longer text as a leak.
	rendered := renderedStrings(t)
	for _, summary := range divergent {
		if rendered[summary] {
			t.Errorf("the generated reference renders the canonical-manifest-only summary %q verbatim; "+
				"the reference must be rendered from the command tree alone", summary)
		}
	}

	// The extraction must actually see the summaries, or the loop above proves
	// nothing about a document it failed to parse.
	treeSummary := publicRoutes()[0].Summary
	if !rendered[treeSummary] {
		t.Fatalf("summary extraction is broken: the reference does not appear to render %q", treeSummary)
	}
}

// renderedStrings returns every standalone string the generated page renders:
// each table cell and each non-table line, trimmed. A summary reaches the page
// only as one of those, so equality against this set is an exact test of
// whether a given summary string was published.
func renderedStrings(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for line := range strings.SplitSeq(readGeneratedReference(t), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "|") {
			for cell := range strings.SplitSeq(strings.Trim(line, "|"), "|") {
				out[strings.TrimSpace(cell)] = true
			}
			continue
		}
		out[line] = true
	}
	return out
}

// TestGeneratedReferenceLinksTheTaskGuide keeps the split surface reachable
// from either half, so neither document becomes an orphan.
func TestGeneratedReferenceLinksTheTaskGuide(t *testing.T) {
	t.Parallel()

	reference := readGeneratedReference(t)
	if !strings.Contains(reference, "("+referenceGuidePath+")") {
		t.Fatalf("the generated reference does not link the task guide %q", referenceGuidePath)
	}
	if !strings.Contains(reference, ReferenceRegenCommand) {
		t.Fatalf("the generated reference does not name its regeneration command %q", ReferenceRegenCommand)
	}

	guidePath := filepath.Join(repoRoot(t), "docs", referenceGuidePath)
	guide, err := os.ReadFile(guidePath) // #nosec G304 -- repository-relative doc
	if err != nil {
		t.Fatalf("read %s: %v", guidePath, err)
	}
	if !strings.Contains(string(guide), "(cli.md)") {
		t.Fatalf("the task guide does not link back to the generated reference")
	}
}
