package cli

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/aiprovider"
)

func TestAgentCapabilityCatalogRoutesMatchExecutableHelpGraph(t *testing.T) {
	t.Parallel()

	agent, ok := LookupRoute("agent")
	if !ok {
		t.Fatal("agent route missing")
	}
	var groups []string
	for _, child := range agent.Children {
		if child.Name != "capabilities" {
			groups = append(groups, child.Name)
		}
	}
	if want := aiprovider.AgentGroups(); !reflect.DeepEqual(groups, want) {
		t.Fatalf("agent help groups = %v, capability groups = %v", groups, want)
	}
	for _, action := range aiprovider.AgentActions() {
		if !action.Callable {
			if action.Route != "" {
				t.Errorf("future action %s has route %q", action.ID, action.Route)
			}
			continue
		}
		if _, ok := LookupCanonicalRoute(action.Route); !ok {
			t.Errorf("callable action %s points at absent route %q", action.ID, action.Route)
		}
	}
	for _, provider := range aiprovider.AgentProviders() {
		if slices.ContainsFunc(agent.Children, func(child Route) bool { return child.Name == string(provider) }) {
			t.Errorf("provider-specific public agent namespace %q exists", provider)
		}
	}
	for _, future := range []string{"message", "wait"} {
		if slices.ContainsFunc(agent.Children, func(child Route) bool { return child.Name == future }) {
			t.Errorf("future placeholder agent %s is callable", future)
		}
	}
}

func TestAgentIntegrationAndGenerationLeafHelpMatchesCapabilityCatalog(t *testing.T) {
	t.Parallel()

	_, integrate, ok := Resolve([]string{"agent", "integrate"})
	if !ok {
		t.Fatal("agent integrate route missing")
	}
	help := strings.Join(integrate.Usage, "\n")
	for _, target := range aiprovider.IntegrationTargets() {
		if !strings.Contains(help, target.ID) {
			t.Errorf("agent integrate help omits target %q: %s", target.ID, help)
		}
	}
	for _, mode := range []string{"--remove", "--dry-run"} {
		if !strings.Contains(help, mode) {
			t.Errorf("agent integrate help omits %s: %s", mode, help)
		}
	}
	_, usage, ok := Resolve([]string{"agent", "usage"})
	if !ok {
		t.Fatal("agent usage route missing")
	}
	usageHelp := strings.Join(usage.Usage, "\n")
	for _, target := range aiprovider.UsageTargets() {
		if !strings.Contains(usageHelp, target) {
			t.Errorf("agent usage help omits target %q: %s", target, usageHelp)
		}
	}

	var generationRoutes []string
	for _, action := range aiprovider.AgentActions() {
		if strings.HasPrefix(action.ID, "app-server.") {
			generationRoutes = append(generationRoutes, action.Route)
		}
	}
	if len(generationRoutes) != 8 {
		t.Fatalf("generation leaf count = %d, want 8", len(generationRoutes))
	}
	for _, route := range generationRoutes {
		if _, ok := LookupCanonicalRoute(route); !ok {
			t.Errorf("generation route %q missing", route)
		}
	}
}
