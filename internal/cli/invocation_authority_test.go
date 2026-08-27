package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestInvocationAuthorityCensusIsACompleteBijection is the Phase 1 mechanical
// completeness gate. Every node of the unified executable graph and every
// parser-owned root bridge contributes one unique row with exactly one member
// of the closed four-class set.
func TestInvocationAuthorityCensusIsACompleteBijection(t *testing.T) {
	t.Parallel()

	allowed := map[InvocationAuthority]bool{}
	for _, class := range InvocationAuthorities() {
		if class == "" || allowed[class] {
			t.Fatalf("closed invocation authority set contains invalid class %q", class)
		}
		allowed[class] = true
	}
	if len(allowed) != 4 {
		t.Fatalf("invocation authority classes = %v, want exactly four", InvocationAuthorities())
	}

	rows := InvocationCensus()
	seen := map[string]InvocationCensusRow{}
	var catalogRows int
	for _, row := range rows {
		if strings.TrimSpace(row.Spelling) == "" {
			t.Fatal("invocation census contains a blank spelling")
		}
		if !allowed[row.Authority] {
			t.Errorf("invocation census row %q has unclassified/conflicting class %q", row.Spelling, row.Authority)
		}
		if prior, duplicate := seen[row.Spelling]; duplicate {
			t.Errorf("invocation census duplicates %q as %+v and %+v", row.Spelling, prior, row)
		}
		seen[row.Spelling] = row
		if row.Catalog {
			catalogRows++
		}
	}

	var graphRows int
	// Inspect the authoring graph directly. A projection must never be able to
	// fill a newly added child's omitted classification and hide the gap.
	walkInvocationGraph(routes, nil, func(path []string, route Route) {
		graphRows++
		spelling := strings.Join(path, " ")
		row, ok := seen[spelling]
		if !ok || !row.Catalog || row.Authority != route.Invocation {
			t.Errorf("graph route %q projects census row %+v", spelling, row)
		}
	})
	if catalogRows != graphRows {
		t.Fatalf("catalog census rows = %d, graph nodes = %d", catalogRows, graphRows)
	}
	wantBridges := 1 + len(helpFlagNames) + len(rootVersionFlags)
	if len(rows)-catalogRows != wantBridges {
		t.Fatalf("root bridge rows = %d, parser-owned bridges = %d", len(rows)-catalogRows, wantBridges)
	}
}

func TestInvocationAuthorityRejectsANewBlankChildBeforeProjection(t *testing.T) {
	t.Parallel()

	synthetic := []Route{{
		Name:       "parent",
		Invocation: InvocationNatural,
		Children:   []Route{{Name: "new-command"}},
	}}
	err := validateInvocationGraph(synthetic, nil)
	if err == nil || !strings.Contains(err.Error(), `route "parent new-command"`) || !strings.Contains(err.Error(), `authority ""`) {
		t.Fatalf("blank child validation = %v, want exact missing-class failure", err)
	}
}

// TestCanonicalAndAliasProjectionsKeepOneInvocationAuthority proves the class
// remains a projection of one graph node when canonical/source aliases are
// traversed. An alias never creates another semantic row.
func TestCanonicalAndAliasProjectionsKeepOneInvocationAuthority(t *testing.T) {
	t.Parallel()

	for _, canonical := range CanonicalRoutes() {
		path, node, ok := Resolve(strings.Fields(canonical.Spelling))
		if !ok || strings.Join(path, " ") != canonical.Spelling {
			t.Fatalf("canonical route %q does not resolve to its owner", canonical.Spelling)
		}
		if canonical.Invocation == "" || canonical.Invocation != node.Invocation {
			t.Errorf("canonical route %q authority = %q, graph = %q", canonical.Spelling, canonical.Invocation, node.Invocation)
		}
	}
	walkInvocationGraph(Routes(), nil, func(path []string, node Route) {
		if len(node.Aliases) == 0 {
			return
		}
		for _, alias := range node.Aliases {
			argv := slices.Clone(path)
			argv[len(argv)-1] = alias
			resolved, aliasNode, ok := Resolve(argv)
			if !ok || !slices.Equal(resolved, path) || aliasNode.Invocation != node.Invocation {
				t.Errorf("alias %q resolved path=%q authority=%q, want path=%q authority=%q",
					strings.Join(argv, " "), strings.Join(resolved, " "), aliasNode.Invocation,
					strings.Join(path, " "), node.Invocation)
			}
		}
	})
}

// TestMixedCommandFamiliesOverrideTheirNamespaceClass pins representative leaf
// rows from every semantic class. This prevents a broad namespace default from
// hiding a newly mixed command family.
func TestMixedCommandFamiliesOverrideTheirNamespaceClass(t *testing.T) {
	t.Parallel()

	want := map[string]InvocationAuthority{
		"agent status":             InvocationNatural,
		"agent turn steer":         InvocationExplicit,
		"create pane":              InvocationNatural,
		"create notification":      InvocationExplicit,
		"get projects":             InvocationFanOut,
		"get panes":                InvocationNatural,
		"notification ack":         InvocationExplicit,
		"rename window":            InvocationNatural,
		"runtime stop":             InvocationFanOut,
		"focus pane":               InvocationExplicit,
		"internal activation-exec": InvocationExplicit,
	}
	for spelling, class := range want {
		path, node, ok := Resolve(strings.Fields(spelling))
		if !ok || strings.Join(path, " ") != spelling || node.Invocation != class {
			t.Errorf("%q authority = %q (path %q), want %q", spelling, node.Invocation, strings.Join(path, " "), class)
		}
	}
}

// TestInvocationAuthorityHasNoSecondRouteManifest keeps classification on the
// unified Route graph. The projection file may walk and sort graph nodes but
// cannot regain a hand-maintained route-to-class table.
func TestInvocationAuthorityHasNoSecondRouteManifest(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	for _, rel := range []string{"internal/cli/canonical.go", "internal/cli/reference.go", "internal/cli/root.go"} {
		body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"map[string]InvocationAuthority", "[]struct {\n\t\tSpelling", "var invocationRoutes"} {
			if strings.Contains(string(body), forbidden) {
				t.Errorf("%s contains a second invocation authority manifest marker %q", rel, forbidden)
			}
		}
	}
}
