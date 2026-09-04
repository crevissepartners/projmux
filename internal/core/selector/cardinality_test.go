package selector

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// TestCardinalityMatrixPinsTheContractCells pins the <verb, kind> cells the
// selector contract fixes by name, plus the shape of the rest of the table.
func TestCardinalityMatrixPinsTheContractCells(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		target Target
		want   Cardinality
	}{
		// The three cells the contract states verbatim.
		{target: Target{Verb: VerbGet, Kind: metadata.KindWindow, List: true}, want: CardinalityAny},
		{target: Target{Verb: VerbRename, Kind: metadata.KindWindow}, want: CardinalityExactOne},
		{target: Target{Verb: VerbRename, Kind: metadata.KindAgent}, want: CardinalityExactOne},
		{target: Target{Verb: VerbCreate, Kind: metadata.KindWindow}, want: CardinalityAtLeastOne},

		// The read family this Phase implements.
		{target: Target{Verb: VerbGet, Kind: metadata.KindPane}, want: CardinalityExactOne},
		{target: Target{Verb: VerbGet, Kind: metadata.KindPane, List: true}, want: CardinalityAny},
		{target: Target{Verb: VerbDescribe, Kind: metadata.KindPane}, want: CardinalityExactOne},

		// Navigation and rebinding address exactly one resource.
		{target: Target{Verb: VerbAttach, Kind: metadata.KindProject}, want: CardinalityExactOne},
		{target: Target{Verb: VerbFocus, Kind: metadata.KindPane}, want: CardinalityExactOne},
		{target: Target{Verb: VerbRebind, Kind: metadata.KindProject}, want: CardinalityExactOne},

		// create scopes to exactly one Project and anchors on exactly one Pane
		// inside each resolved Window.
		{target: Target{Verb: VerbCreate, Kind: metadata.KindProject}, want: CardinalityExactOne},
		{target: Target{Verb: VerbCreate, Kind: metadata.KindPane}, want: CardinalityExactOne},

		// The Agent create yields one Agent per resolved target Window, so an
		// invocation that resolves nothing is a usage error rather than a
		// success that created nothing.
		{target: Target{Verb: VerbCreate, Kind: metadata.KindAgent}, want: CardinalityAtLeastOne},
		{target: Target{Verb: VerbReview, Kind: metadata.KindAgent}, want: CardinalityExactOne},

		// delete fans out.
		{target: Target{Verb: VerbDelete, Kind: metadata.KindProject}, want: CardinalityAtLeastOne},
		{target: Target{Verb: VerbDelete, Kind: metadata.KindAgent}, want: CardinalityAtLeastOne},
	} {
		got, ok := CardinalityFor(test.target)
		if !ok {
			t.Fatalf("no cardinality declared for %q", test.target)
		}
		if got != test.want {
			t.Fatalf("%q cardinality = %q, want %q", test.target, got, test.want)
		}
	}

	// Every declared cell uses a member of the closed cardinality set and a
	// member of the closed kind set.
	kinds := map[metadata.Kind]bool{}
	for _, kind := range metadata.Kinds() {
		kinds[kind] = true
	}
	closed := map[Cardinality]bool{
		CardinalityAny:        true,
		CardinalityExactOne:   true,
		CardinalityAtLeastOne: true,
	}
	for target, cardinality := range Matrix() {
		if !kinds[target.Kind] {
			t.Errorf("matrix cell %q targets a kind outside the closed set", target)
		}
		if !closed[cardinality] {
			t.Errorf("matrix cell %q declares cardinality %q outside the closed set", target, cardinality)
		}
	}

	// Matrix returns a copy: tampering with it cannot corrupt the table.
	copied := Matrix()
	key := Target{Verb: VerbGet, Kind: metadata.KindPane}
	copied[key] = "tampered"
	if got, _ := CardinalityFor(key); got != CardinalityExactOne {
		t.Fatalf("Matrix returned a mutable view of the table: %q", got)
	}

	// An undeclared cell is reported rather than silently defaulting.
	if _, ok := CardinalityFor(Target{Verb: VerbRebind, Kind: metadata.KindPane}); ok {
		t.Fatal("an undeclared cell returned a cardinality")
	}
}

// TestCardinalityViolationsAreBoundedUsageErrors is acceptance criterion 4 at
// the engine level: a violated cell returns a usage error carrying at most
// MaxCandidates rows and writes nothing anywhere.
func TestCardinalityViolationsAreBoundedUsageErrors(t *testing.T) {
	t.Parallel()

	resolver := New(standardRegistry(t))

	// exact-one, ambiguous: `zsh` names three Panes in distinct roots.
	ambiguous := Query{Panes: []Ref{mustRef(t, metadata.KindPane, "zsh")}}
	resolution, err := resolver.ResolvePanes(ambiguous)
	if err != nil {
		t.Fatalf("ResolvePanes error = %v", err)
	}
	err = Enforce(Target{Verb: VerbGet, Kind: metadata.KindPane}, DescribeSelector(ambiguous), resolution)
	if err == nil {
		t.Fatal("an ambiguous exact-one resolution succeeded")
	}
	if !metadata.IsUsageError(err) {
		t.Fatalf("cardinality violation is not a usage error: %v", err)
	}
	var selectorErr *SelectorError
	if !asSelectorError(err, &selectorErr) {
		t.Fatalf("cardinality violation is not a *SelectorError: %v", err)
	}
	if selectorErr.Want != CardinalityExactOne || selectorErr.Got != 3 {
		t.Fatalf("violation = want %q got %d", selectorErr.Want, selectorErr.Got)
	}
	if len(selectorErr.Candidates) != 3 || selectorErr.Omitted != 0 {
		t.Fatalf("candidates = %d omitted = %d, want 3/0", len(selectorErr.Candidates), selectorErr.Omitted)
	}
	// Candidate rows carry name, non-authoritative context, and owner
	// context -- exactly what a human needs to disambiguate.
	first := selectorErr.Candidates[0].String()
	if !strings.Contains(first, "pane/zsh") || !strings.Contains(first, "context=zsh") ||
		!strings.Contains(first, "contextSource=command-executable") ||
		!strings.Contains(first, "owner=project/alpha window/main") {
		t.Fatalf("candidate row = %q", first)
	}
	if !strings.Contains(err.Error(), "want exactly one") {
		t.Fatalf("error text = %q", err)
	}

	// exact-one, no match.
	missing := Query{Panes: []Ref{mustRef(t, metadata.KindPane, "nosuch")}}
	resolution, err = resolver.ResolvePanes(missing)
	if err != nil {
		t.Fatalf("ResolvePanes error = %v", err)
	}
	err = Enforce(Target{Verb: VerbGet, Kind: metadata.KindPane}, DescribeSelector(missing), resolution)
	if err == nil {
		t.Fatal("a no-match exact-one resolution succeeded")
	}
	if !metadata.IsUsageError(err) {
		t.Fatalf("no-match violation is not a usage error: %v", err)
	}
	if !strings.Contains(err.Error(), "matched no panes") {
		t.Fatalf("no-match error text = %q", err)
	}

	// 1..N rejects only the empty set.
	if err := Enforce(Target{Verb: VerbDelete, Kind: metadata.KindPane}, DescribeSelector(missing), resolution); err == nil {
		t.Fatal("an empty 1..N resolution succeeded")
	}
	all, err := resolver.ResolvePanes(ambiguous)
	if err != nil {
		t.Fatalf("ResolvePanes error = %v", err)
	}
	if err := Enforce(Target{Verb: VerbDelete, Kind: metadata.KindPane}, DescribeSelector(ambiguous), all); err != nil {
		t.Fatalf("a 4-match 1..N resolution failed: %v", err)
	}

	// 0..N accepts the empty set.
	empty, err := resolver.ResolvePanes(missing)
	if err != nil {
		t.Fatalf("ResolvePanes error = %v", err)
	}
	if err := Enforce(Target{Verb: VerbGet, Kind: metadata.KindPane, List: true}, DescribeSelector(missing), empty); err != nil {
		t.Fatalf("an empty 0..N resolution failed: %v", err)
	}
}

// TestAmbiguityListingIsBoundedToFiveCandidates pins the bound itself.
func TestAmbiguityListingIsBoundedToFiveCandidates(t *testing.T) {
	t.Parallel()

	b := newBuilder(t)
	for i, suffix := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		projectUID := "prj-wide-" + suffix
		windowUID := "win-wide-" + suffix
		b.project(projectUID, "wide-"+suffix, "", "/srv/wide-"+suffix, nil, false)
		b.window(windowUID, "w"+suffix, projectUID, nil)
		b.shellPane("pan-wide-"+suffix, "zsh", "display", windowUID, "/srv/wide", nil)
		_ = i
	}
	resolver := New(b.build())

	query := Query{Panes: []Ref{mustRef(t, metadata.KindPane, "zsh")}}
	resolution, err := resolver.ResolvePanes(query)
	if err != nil {
		t.Fatalf("ResolvePanes error = %v", err)
	}
	if len(resolution.Matches) != 7 {
		t.Fatalf("fixture resolved %d panes, want 7", len(resolution.Matches))
	}

	err = Enforce(Target{Verb: VerbGet, Kind: metadata.KindPane}, DescribeSelector(query), resolution)
	var selectorErr *SelectorError
	if !asSelectorError(err, &selectorErr) {
		t.Fatalf("error = %v", err)
	}
	if len(selectorErr.Candidates) != MaxCandidates {
		t.Fatalf("candidates = %d, want the %d bound", len(selectorErr.Candidates), MaxCandidates)
	}
	if selectorErr.Omitted != 2 {
		t.Fatalf("omitted = %d, want 2", selectorErr.Omitted)
	}
	lines := strings.Split(err.Error(), "\n")
	if len(lines) != 1+MaxCandidates+1 {
		t.Fatalf("error rendered %d lines:\n%s", len(lines), err)
	}
	if !strings.Contains(lines[len(lines)-1], "... 2 more omitted") {
		t.Fatalf("last line = %q", lines[len(lines)-1])
	}
}

// TestDescribeSelectorRendersEveryOccurrence covers the error-text helper.
func TestDescribeSelectorRendersEveryOccurrence(t *testing.T) {
	t.Parallel()

	query := Query{
		Project: refPtr(mustRef(t, metadata.KindProject, "alpha")),
		Windows: []Ref{mustRef(t, metadata.KindWindow, "main"), mustRef(t, metadata.KindWindow, "review")},
		Panes:   []Ref{mustRef(t, metadata.KindPane, UIDPrefix+"pan-alpha-zsh")},
		Labels:  []Label{mustLabel(t, "role=shell")},
	}
	want := "--project alpha --window main --window review --pane uid:pan-alpha-zsh --selector role=shell"
	if got := DescribeSelector(query); got != want {
		t.Fatalf("DescribeSelector = %q, want %q", got, want)
	}
}

// TestTheCanonicalStageOrderIsPinnedLiterally is the one assertion a reordering
// of the stage table cannot pass.
//
// It replaces the retired StageOrder accessor's test. The copy half of that test
// is gone with the accessor: nothing outside this package can reach the table
// any more, and Resolution.Trace is built from TraceStep literals, so no caller
// is ever handed a view of it to mutate. The half that was load bearing is this
// one -- every other stage-order test compares a resolution's trace *against*
// this table, so all of them stay green if the table itself is reordered.
func TestTheCanonicalStageOrderIsPinnedLiterally(t *testing.T) {
	t.Parallel()

	if !reflect.DeepEqual(stageOrder, []Stage{StageNameUIDUnion, StageLabelFilter, StageUIDDedupe}) {
		t.Fatalf("stage order = %v", stageOrder)
	}
}

// TestOnlyANoMatchFailureWrapsErrNotFound pins the error classification the
// mutation Phases need when they resolve targets before mutating.
//
// metadata.ErrNotFound marks an unresolvable resource. Exactly one selector
// failure means that: a cardinality violation that resolved zero targets. A
// violation that resolved too many is the opposite condition, and a malformed
// selector value never got as far as looking, so neither may wrap it even
// though both are usage errors that exit 2.
func TestOnlyANoMatchFailureWrapsErrNotFound(t *testing.T) {
	t.Parallel()

	b := newBuilder(t)
	for _, suffix := range []string{"a", "b"} {
		projectUID := "prj-dup-" + suffix
		windowUID := "win-dup-" + suffix
		b.project(projectUID, "dup-"+suffix, "", "/srv/dup-"+suffix, nil, false)
		b.window(windowUID, "w"+suffix, projectUID, nil)
		b.shellPane("pan-dup-"+suffix, "zsh", "display", windowUID, "/srv/dup", nil)
	}
	resolver := New(b.build())
	target := Target{Verb: VerbGet, Kind: metadata.KindPane}

	resolveErr := func(t *testing.T, name string) error {
		t.Helper()
		query := Query{Panes: []Ref{mustRef(t, metadata.KindPane, name)}}
		resolution, err := resolver.ResolvePanes(query)
		if err != nil {
			t.Fatalf("ResolvePanes error = %v", err)
		}
		return Enforce(target, DescribeSelector(query), resolution)
	}

	// A parse failure: --selector without an "=" never reaches resolution.
	_, parseErr := ParseLabel("nosuchequals")
	if parseErr == nil {
		t.Fatal("malformed label parsed")
	}

	for _, test := range []struct {
		name        string
		err         error
		wantNoMatch bool
		wantGot     int
	}{
		{name: "no match", err: resolveErr(t, "absent"), wantNoMatch: true, wantGot: 0},
		{name: "too many", err: resolveErr(t, "zsh"), wantNoMatch: false, wantGot: 2},
		{name: "parse failure", err: parseErr, wantNoMatch: false, wantGot: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if test.err == nil {
				t.Fatal("expected a selector error")
			}
			// Every case stays a usage error, so exit code 2 is unaffected by
			// the ErrNotFound classification.
			if !metadata.IsUsageError(test.err) {
				t.Fatalf("not a usage error: %v", test.err)
			}
			var selectorErr *SelectorError
			if !asSelectorError(test.err, &selectorErr) {
				t.Fatalf("not a *SelectorError: %v", test.err)
			}
			if selectorErr.Got != test.wantGot {
				t.Fatalf("Got = %d, want %d", selectorErr.Got, test.wantGot)
			}
			if got := selectorErr.IsNoMatch(); got != test.wantNoMatch {
				t.Fatalf("IsNoMatch() = %t, want %t (%v)", got, test.wantNoMatch, test.err)
			}
			if got := errors.Is(test.err, metadata.ErrNotFound); got != test.wantNoMatch {
				t.Fatalf("errors.Is(err, ErrNotFound) = %t, want %t (%v)", got, test.wantNoMatch, test.err)
			}
		})
	}
}

func asSelectorError(err error, target **SelectorError) bool {
	selectorErr, ok := err.(*SelectorError)
	if !ok {
		return false
	}
	*target = selectorErr
	return true
}
