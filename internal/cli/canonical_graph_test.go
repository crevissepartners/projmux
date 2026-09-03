package cli

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCanonicalCommandGraphProjectionMatches013Baseline(t *testing.T) {
	t.Parallel()

	var baseline strings.Builder
	for _, route := range CanonicalRoutes() {
		fmt.Fprintf(&baseline, "%s\t%s\t%s\t%s\t%s\n",
			route.Spelling, route.Summary, strings.Join(route.Sources, ","),
			outputModesString(route.Outputs), fieldProjectionsString(route.Fields))
	}
	const want = "2f958d6bc3fbabc7b5db76ffb18c5db1f637ad1946b21d40d3923d6ea9749492"
	if got := fmt.Sprintf("%x", sha256.Sum256([]byte(baseline.String()))); got != want {
		t.Fatalf("canonical command projection digest = %s, want 0.13 baseline %s\n%s", got, want, baseline.String())
	}
}

func TestCanonicalCommandGraphHasOneOwnerPerSpelling(t *testing.T) {
	t.Parallel()

	owners := map[string][]string{}
	edges := map[string][]string{}
	familyOrders := map[int]string{}
	nodeOrders := map[string]map[int]string{}
	walkRoutes(Routes(), func(path []string, route Route) {
		pathSpelling := strings.Join(path, " ")
		for _, spelling := range route.Canonical {
			edges[spelling] = append(edges[spelling], pathSpelling)
			if spelling == pathSpelling {
				owners[spelling] = append(owners[spelling], pathSpelling)
			}
		}
		if route.CanonicalSummary != "" && !slices.Contains(route.Canonical, pathSpelling) {
			t.Errorf("non-owner route %q declares canonical summary override %q", pathSpelling, route.CanonicalSummary)
		}
		if route.AcceptedOutputs != nil && !slices.Contains(route.Canonical, pathSpelling) {
			t.Errorf("non-owner route %q declares parser output override %v", pathSpelling, route.AcceptedOutputs)
		}
		if len(path) == 1 && route.CanonicalOrder != 0 {
			if prior := familyOrders[route.CanonicalOrder]; prior != "" {
				t.Errorf("canonical family order %d is shared by %q and %q", route.CanonicalOrder, prior, pathSpelling)
			}
			familyOrders[route.CanonicalOrder] = pathSpelling
		}
		if route.CanonicalNodeOrder != 0 {
			family := path[0]
			if nodeOrders[family] == nil {
				nodeOrders[family] = map[int]string{}
			}
			if prior := nodeOrders[family][route.CanonicalNodeOrder]; prior != "" {
				t.Errorf("canonical node order %d in family %q is shared by %q and %q", route.CanonicalNodeOrder, family, prior, pathSpelling)
			}
			nodeOrders[family][route.CanonicalNodeOrder] = pathSpelling
		}
	})

	projected := CanonicalRoutes()
	if len(projected) == 0 {
		t.Fatal("canonical graph projection is empty")
	}
	for _, route := range projected {
		if got := owners[route.Spelling]; len(got) != 1 {
			t.Errorf("canonical spelling %q owners = %v, want exactly one", route.Spelling, got)
		}
		if len(edges[route.Spelling]) == 0 {
			t.Errorf("canonical spelling %q has no executable source edge", route.Spelling)
		}
		if order := canonicalFamilyOrder(strings.Fields(route.Spelling)[0]); order == 0 {
			t.Errorf("canonical spelling %q belongs to an unordered family", route.Spelling)
		}
		delete(owners, route.Spelling)
		delete(edges, route.Spelling)
	}
	for spelling, paths := range owners {
		t.Errorf("unmapped canonical owner %q at %v", spelling, paths)
	}
	for spelling, paths := range edges {
		t.Errorf("orphan canonical edge %q from %v", spelling, paths)
	}
}

func TestCanonicalProjectionHasNoSecondCommandManifest(t *testing.T) {
	t.Parallel()

	path := filepath.Join(repoRoot(t), "internal", "cli", "canonical.go")
	data, err := os.ReadFile(path) // #nosec G304 -- fixed repository source path
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"[]CanonicalRoute{", "var canonicalRoutes", `Spelling: "`} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("canonical.go contains %q; command facts must live only in the executable graph", forbidden)
		}
	}
}

func outputModesString(modes []OutputMode) string {
	out := make([]string, len(modes))
	for i := range modes {
		out[i] = string(modes[i])
	}
	return strings.Join(out, ",")
}

func fieldProjectionsString(fields []FieldProjection) string {
	out := make([]string, len(fields))
	for i := range fields {
		out[i] = string(fields[i])
	}
	return strings.Join(out, ",")
}
