package aiprovider

import (
	"reflect"
	"slices"
	"testing"
)

func TestAgentCapabilityCatalogIsClosedCartesianMatrix(t *testing.T) {
	t.Parallel()

	providers := AgentProviders()
	wantProviders := []ID{Codex, Claude, Antigravity}
	if !reflect.DeepEqual(providers, wantProviders) {
		t.Fatalf("providers = %v, want %v", providers, wantProviders)
	}
	seen := map[string]bool{}
	for _, action := range AgentActions() {
		if action.ID == "" || action.Group == "" || seen[action.ID] {
			t.Fatalf("invalid or duplicate action %#v", action)
		}
		seen[action.ID] = true
		if action.Callable != (action.Route != "") {
			t.Errorf("action %s callable=%t route=%q", action.ID, action.Callable, action.Route)
		}
		cells := map[ID]AgentCapabilityCell{}
		for _, cell := range action.Cells {
			if _, duplicate := cells[cell.Provider]; duplicate {
				t.Errorf("action %s has duplicate %s cell", action.ID, cell.Provider)
			}
			cells[cell.Provider] = cell
			if cell.Mode == "" || cell.CompletionPrecision == "" {
				t.Errorf("action %s has incomplete cell %#v", action.ID, cell)
			}
			if (cell.Mode == SupportUnsupported) != (cell.CompletionPrecision == CompletionNone) {
				t.Errorf("action %s unsupported/precision mismatch: %#v", action.ID, cell)
			}
		}
		if len(cells) != len(providers) {
			t.Errorf("action %s cells=%d, want %d", action.ID, len(cells), len(providers))
		}
		for _, provider := range providers {
			if _, ok := cells[provider]; !ok {
				t.Errorf("action %s has no %s cell", action.ID, provider)
			}
		}
	}
	if len(seen) != 27 {
		t.Fatalf("action count = %d, want 27", len(seen))
	}
}

func TestAgentCapabilityCatalogPinsCurrentGroupsAndDeferredVocabulary(t *testing.T) {
	t.Parallel()

	wantGroups := []string{"status", "topic", "resume", "turn", "approval", "review", "integrate", "usage", "app-server", "message", "wait"}
	if got := AgentGroups(); !reflect.DeepEqual(got, wantGroups) {
		t.Fatalf("groups = %v, want %v", got, wantGroups)
	}
	for _, id := range []string{"message.send", "message.wait", "message.status", "wait.idle"} {
		for _, provider := range AgentProviders() {
			action, _, ok := LookupAgentCapability(id, provider)
			if !ok || !action.Callable || action.Route == "" {
				t.Errorf("%s/%s = action %#v", id, provider, action)
			}
		}
	}
	_, antigravitySend, _ := LookupAgentCapability("message.send", Antigravity)
	_, claudeWait, _ := LookupAgentCapability("message.wait", Claude)
	if antigravitySend.Mode != SupportUnsupported || claudeWait.Mode != SupportUnsupported {
		t.Fatalf("unsupported coordination directions changed: antigravity send=%#v claude wait=%#v", antigravitySend, claudeWait)
	}
}

func TestAgentCapabilityCatalogPinsCodexNativeAndSharedFamilies(t *testing.T) {
	t.Parallel()

	for _, id := range []string{
		"turn.start", "turn.steer", "turn.interrupt", "approval.review", "review",
		"app-server.upgrade.plan", "app-server.upgrade.apply", "app-server.upgrade.resume", "app-server.upgrade.abort",
		"app-server.handover.plan", "app-server.handover.apply", "app-server.handover.resume", "app-server.handover.abort",
	} {
		for _, provider := range AgentProviders() {
			_, cell, ok := LookupAgentCapability(id, provider)
			if !ok {
				t.Fatalf("missing %s/%s", id, provider)
			}
			want := SupportUnsupported
			if provider == Codex {
				want = SupportNativeExact
			}
			if cell.Mode != want {
				t.Errorf("%s/%s mode=%s, want %s", id, provider, cell.Mode, want)
			}
		}
	}
	for _, id := range []string{"status.get", "status.set", "topic.get", "topic.set", "topic.clear"} {
		for _, provider := range AgentProviders() {
			_, cell, _ := LookupAgentCapability(id, provider)
			if cell.Mode != SupportGenericRegistry {
				t.Errorf("%s/%s mode=%s", id, provider, cell.Mode)
			}
		}
	}
	for _, id := range []string{"integrate.install", "integrate.remove", "integrate.dry-run"} {
		for _, provider := range AgentProviders() {
			_, cell, _ := LookupAgentCapability(id, provider)
			if cell.Mode != SupportProviderHook {
				t.Errorf("%s/%s mode=%s", id, provider, cell.Mode)
			}
		}
	}
	targets := IntegrationTargets()
	if got := []string{targets[0].ID, targets[1].ID, targets[2].ID, targets[3].ID}; !reflect.DeepEqual(got, []string{"codex", "claude", "antigravity", "tmux-bell"}) {
		t.Fatalf("integration targets = %v", got)
	}
	if targets[3].Provider != "" || !slices.ContainsFunc(targets, func(target IntegrationTarget) bool { return target.ID == "tmux-bell" }) {
		t.Fatalf("tmux-bell target = %#v", targets[3])
	}
}

func TestAgentCapabilityCatalogMatchesProviderIntegrationAndUsageSurfaces(t *testing.T) {
	t.Parallel()

	for _, target := range IntegrationTargets() {
		base := IntegrationCommand(target.ID)
		if base == "" {
			t.Fatalf("integration target %q has no command", target.ID)
		}
		if target.Provider == "" {
			continue
		}
		provider, ok := Lookup(string(target.Provider))
		if !ok || !provider.Integrate.Supported || provider.Integrate.Command != base {
			t.Errorf("integration target %q metadata = %#v", target.ID, provider.Integrate)
		}
	}

	wantUsage := []string{"codex", "claude", "antigravity", "all"}
	if got := UsageTargets(); !reflect.DeepEqual(got, wantUsage) {
		t.Fatalf("usage targets = %v, want %v", got, wantUsage)
	}
	for _, provider := range AgentProviders() {
		metadata, ok := Lookup(string(provider))
		if !ok || !metadata.UsageSupported || metadata.UsageModel != string(provider) {
			t.Errorf("usage target %q metadata = %#v", provider, metadata)
		}
		_, cell, ok := LookupAgentCapability("usage", provider)
		if !ok || cell.Mode != SupportReadOnlyAdapter {
			t.Errorf("usage target %q capability = %#v", provider, cell)
		}
	}
}
