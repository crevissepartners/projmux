package cli

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// outputFlagToken matches a `-o` flag token in a synopsis line: `-o` standing
// alone, after the start of the line, whitespace, `[`, or `|`, and followed by
// whitespace or the end of the line. `--output`, `-oX`, and `--no-o` do not
// match.
var outputFlagToken = regexp.MustCompile(`(^|[\s\[|])-o(\s|$)`)

// equalsFlagSpelling matches a long flag written with its value glued on by
// `=`. The parsers accept both `--name value` and `--name=value`; this is a
// spelling rule, not a parser rule: the catalog uses one spelling, the value
// as its own token.
var equalsFlagSpelling = regexp.MustCompile(`--[a-z][a-z0-9-]*=`)

// outputSynopsisSubject is one canonical route as the output guard judges it:
// whether its projection declares any `-o` token, and the lines help prints.
type outputSynopsisSubject struct {
	route    string
	declares bool
	usage    []string
}

// canonicalOutputSubjects returns one subject per public node that owns its
// own canonical spelling. The declared projection is the one CanonicalRoutes
// derives -- AcceptedOutputs when set, else Outputs, plus Fields -- because
// that is the list the parser consults. Hidden subtrees and the internal
// namespace are skipped like every other public-surface guard.
func canonicalOutputSubjects(routes []Route) []outputSynopsisSubject {
	var out []outputSynopsisSubject
	for _, node := range publicRouteNodes(routes) {
		spelling := strings.Join(node.path, " ")
		if !slices.Contains(node.route.Canonical, spelling) {
			continue
		}
		outputs := node.route.Outputs
		if node.route.AcceptedOutputs != nil {
			outputs = node.route.AcceptedOutputs
		}
		out = append(out, outputSynopsisSubject{
			route:    spelling,
			declares: len(outputs)+len(node.route.Fields) > 0,
			usage:    effectiveUsage(node.path, node.route),
		})
	}
	return out
}

// outputSynopsisViolations holds the synopsis to the projection in both
// directions: a route that declares outputs shows `-o` on every usage line,
// and a route that declares none shows `-o` on no line. There is no exception
// table; every current route satisfies the rule.
func outputSynopsisViolations(subjects []outputSynopsisSubject) []string {
	var violations []string
	for _, subject := range subjects {
		for _, line := range subject.usage {
			shows := outputFlagToken.MatchString(line)
			switch {
			case subject.declares && !shows:
				violations = append(violations, fmt.Sprintf("%s: projection declares outputs but synopsis line lacks -o: %q", subject.route, line))
			case !subject.declares && shows:
				violations = append(violations, fmt.Sprintf("%s: synopsis shows -o but projection declares no outputs: %q", subject.route, line))
			}
		}
	}
	return violations
}

// equalsFlagViolations reports every usage line, parent lines included, that
// spells a long flag as `--name=value`. It walks the whole tree, hidden and
// internal nodes too: no line anywhere needs the glued spelling.
func equalsFlagViolations(routes []Route) []string {
	var violations []string
	walkInvocationGraph(routes, nil, func(path []string, route Route) {
		for _, line := range route.Usage {
			if equalsFlagSpelling.MatchString(line) {
				violations = append(violations, fmt.Sprintf("%s usage %q spells a flag as --name=; write the value as its own token",
					strings.Join(path, " "), line))
			}
		}
	})
	return violations
}

// TestCanonicalSynopsisShowsOutputFlagExactlyWhenProjectionDeclaresOutputs
// keeps help from advertising `-o` on a route whose parser rejects it, and
// from hiding `-o` on a route whose parser takes it.
func TestCanonicalSynopsisShowsOutputFlagExactlyWhenProjectionDeclaresOutputs(t *testing.T) {
	t.Parallel()

	subjects := canonicalOutputSubjects(Routes())
	for _, violation := range outputSynopsisViolations(subjects) {
		t.Error(violation)
	}

	// The subjects are the public part of CanonicalRoutes, and each one's
	// declaration is the projection the parser reads.
	canonical := map[string]CanonicalRoute{}
	for _, route := range CanonicalRoutes() {
		canonical[route.Spelling] = route
	}
	if len(subjects) == 0 {
		t.Fatal("no public canonical route was judged")
	}
	for _, subject := range subjects {
		route, ok := canonical[subject.route]
		if !ok {
			t.Errorf("%s is judged but is not a CanonicalRoutes spelling", subject.route)
			continue
		}
		if got := len(route.Outputs)+len(route.Fields) > 0; got != subject.declares {
			t.Errorf("%s: guard reads declares=%v, CanonicalRoutes reads %v", subject.route, subject.declares, got)
		}
	}
}

// TestUsageLinesNeverGlueAFlagValueWithEquals pins the space-separated flag
// spelling across every usage line of the catalog.
func TestUsageLinesNeverGlueAFlagValueWithEquals(t *testing.T) {
	t.Parallel()

	for _, violation := range equalsFlagViolations(Routes()) {
		t.Error(violation)
	}
}

func TestGetNotificationsAcceptsNoOutputToken(t *testing.T) {
	t.Parallel()

	if _, ok := LookupCanonicalRoute("get notifications"); !ok {
		t.Fatal("get notifications is not a canonical route")
	}
	if got := AcceptedOutputTokens("get notifications"); len(got) != 0 {
		t.Fatalf("AcceptedOutputTokens(get notifications) = %q, want none", got)
	}
}

func TestReconcileRoutesAcceptOnlyJSONOutput(t *testing.T) {
	t.Parallel()

	for _, spelling := range []string{"reconcile resources", "reconcile registry"} {
		if got := AcceptedOutputTokens(spelling); !slices.Equal(got, []string{"json"}) {
			t.Errorf("AcceptedOutputTokens(%s) = %q, want [json]", spelling, got)
		}
	}
}

func TestOutputSynopsisGuardReportsBothDirections(t *testing.T) {
	t.Parallel()

	tree := []Route{
		{
			Name:      "make",
			Usage:     []string{"projmux make [--flag <value>]"},
			Canonical: []string{"make"},
			Outputs:   receiptOnlyOutputModes,
		},
		{
			Name:      "show",
			Usage:     []string{"projmux show [-o <mode>]"},
			Canonical: []string{"show"},
		},
		{
			Name:      "fine",
			Usage:     []string{"projmux fine [-o json]", "projmux fine --all -o json"},
			Canonical: []string{"fine"},
			Outputs:   jsonOnlyOutputModes,
		},
		{
			Name:      "field",
			Usage:     []string{"projmux field --current -o cwd"},
			Canonical: []string{"field"},
			Fields:    []FieldProjection{FieldProjectionCWD},
		},
		{
			Name:      "wider",
			Usage:     []string{"projmux wider [-o <mode>]"},
			Canonical: []string{"wider"},
			// Acceptance alone counts: it is what the parser consults.
			AcceptedOutputs: sharedOutputModes,
		},
		{Name: "plain", Usage: []string{"projmux plain [--output-dir <path>]"}, Canonical: []string{"plain"}},
		{Name: "secret", Hidden: true, Usage: []string{"projmux secret"}, Canonical: []string{"secret"}, Outputs: jsonOnlyOutputModes},
	}
	got := outputSynopsisViolations(canonicalOutputSubjects(tree))
	if len(got) != 2 {
		t.Fatalf("want exactly two violations, got %q", got)
	}
	if !strings.HasPrefix(got[0], "make: ") || !strings.Contains(got[0], "projection declares outputs but synopsis line lacks -o") {
		t.Errorf("outputs without -o: want make named with that direction, got %q", got[0])
	}
	if !strings.HasPrefix(got[1], "show: ") || !strings.Contains(got[1], "synopsis shows -o but projection declares no outputs") {
		t.Errorf("-o without outputs: want show named with that direction, got %q", got[1])
	}

	// One line of several lacking -o is enough to fail.
	tree = []Route{{
		Name:      "multi",
		Usage:     []string{"projmux multi [-o <mode>]", "projmux multi --all"},
		Canonical: []string{"multi"},
		Outputs:   receiptOnlyOutputModes,
	}}
	got = outputSynopsisViolations(canonicalOutputSubjects(tree))
	if len(got) != 1 || !strings.Contains(got[0], `"projmux multi --all"`) {
		t.Errorf("partial -o: want one violation naming the bare line, got %q", got)
	}

	// The same regression on the real catalog must fail too. Routes clones
	// the nodes, so assigning a field does not write into the package catalog.
	routes := Routes()
	mutated := false
	for i := range routes {
		if routes[i].Name != "get" {
			continue
		}
		for j := range routes[i].Children {
			if routes[i].Children[j].Name == "notifications" {
				routes[i].Children[j].AcceptedOutputs = sharedOutputModes
				mutated = true
			}
		}
	}
	if !mutated {
		t.Fatal("get has no notifications child to mutate")
	}
	got = outputSynopsisViolations(canonicalOutputSubjects(routes))
	if len(got) != 1 || !strings.HasPrefix(got[0], "get notifications: projection declares outputs but synopsis line lacks -o") {
		t.Fatalf("get notifications with accepted outputs: want one violation, got %q", got)
	}
}

func TestEqualsFlagGuardReportsGluedFlagValues(t *testing.T) {
	t.Parallel()

	tree := []Route{
		{
			Name:  "runtime",
			Usage: []string{"projmux runtime diagnostics [--ui=popup|sidebar]", "projmux runtime sessions [--ui popup|sidebar]"},
			Children: []Route{
				{Name: "diagnostics", Usage: []string{"projmux runtime diagnostics [--ui=popup|sidebar]"}},
				{Name: "sessions", Usage: []string{"projmux runtime sessions [--ui popup|sidebar] [-- <payload>]"}},
			},
		},
		{Name: "internal", Hidden: true, Children: []Route{{Name: "payload", Usage: []string{"projmux internal payload --kind=x"}}}},
	}
	got := equalsFlagViolations(tree)
	want := []string{
		`runtime usage "projmux runtime diagnostics [--ui=popup|sidebar]"`,
		`runtime diagnostics usage "projmux runtime diagnostics [--ui=popup|sidebar]"`,
		`internal payload usage "projmux internal payload --kind=x"`,
	}
	if len(got) != len(want) {
		t.Fatalf("want %d violations, got %q", len(want), got)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("violation %d: want prefix %q, got %q", i, want[i], got[i])
		}
	}
}
