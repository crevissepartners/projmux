package cli

import (
	"slices"
	"strings"
	"testing"
)

func TestResourceScopeHelpAdvertisesLongAndShortOptionsTogether(t *testing.T) {
	t.Parallel()

	walkResourceScopeUsage(Routes(), nil, func(path []string, usage string) {
		t.Helper()
		spelling := strings.Join(path, " ")
		if strings.Contains(usage, "--project") && !strings.Contains(usage, "-p <ref>") {
			t.Errorf("%s usage advertises --project without -p: %q", spelling, usage)
		}
		if strings.Contains(usage, "--window") && !strings.Contains(usage, "projmux agent usage") && !strings.Contains(usage, "-w <ref>") {
			t.Errorf("%s usage advertises resource --window without -w: %q", spelling, usage)
		}
		if strings.Contains(usage, "--all-projects") && !strings.Contains(usage, "-A") {
			t.Errorf("%s usage advertises --all-projects without -A: %q", spelling, usage)
		}
	})
}

func TestAllProjectsShortOptionIsAdvertisedOnlyOnThePluralReads(t *testing.T) {
	t.Parallel()

	allowed := []string{"get", "get windows", "get panes", "get agents"}
	walkResourceScopeUsage(Routes(), nil, func(path []string, usage string) {
		if !strings.Contains(usage, "-A") {
			return
		}
		spelling := strings.Join(path, " ")
		if !slices.Contains(allowed, spelling) {
			t.Errorf("%s advertises -A outside get windows|panes|agents: %q", spelling, usage)
		}
		if !strings.Contains(usage, "--all-projects") {
			t.Errorf("%s advertises -A without its long spelling: %q", spelling, usage)
		}
	})
}

func TestNonResourceWindowHelpDoesNotAcquireTheResourceAlias(t *testing.T) {
	t.Parallel()

	_, route, ok := Resolve([]string{"agent", "usage"})
	if !ok {
		t.Fatal("agent usage route is missing")
	}
	joined := strings.Join(route.Usage, "\n")
	if !strings.Contains(joined, "--window <name>") || strings.Contains(joined, "[-w") || strings.Contains(joined, "| -w") {
		t.Fatalf("agent usage help changed its non-resource Window filter: %q", joined)
	}
}

func walkResourceScopeUsage(routes []Route, prefix []string, visit func([]string, string)) {
	for _, route := range routes {
		path := append(append([]string(nil), prefix...), route.Name)
		for _, usage := range route.Usage {
			visit(path, usage)
		}
		walkResourceScopeUsage(route.Children, path, visit)
	}
}
