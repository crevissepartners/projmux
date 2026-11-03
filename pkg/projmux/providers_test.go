package projmux_test

import (
	"slices"
	"testing"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestListSettingsProvidersEqualsInternal(t *testing.T) {
	want := aiprovider.SettingsVisible()
	got := projmux.ListSettingsProviders()
	if len(got) != len(want) {
		t.Fatalf("got %d providers, want %d", len(got), len(want))
	}
	for i, provider := range want {
		if got[i].ID != string(provider.ID) || got[i].DisplayName != provider.DisplayName || got[i].SettingsVisible != provider.SettingsVisible {
			t.Fatalf("provider %d: public %+v internal %+v", i, got[i], provider)
		}
		if got[i].IntegrationCommand != provider.Integrate.Command {
			t.Fatalf("%s: command %q != %q", got[i].ID, got[i].IntegrationCommand, provider.Integrate.Command)
		}
	}
}

func TestListSettingsProviderIDsEqualKnownAIAgentProviders(t *testing.T) {
	var got []string
	for _, provider := range projmux.ListSettingsProviders() {
		got = append(got, provider.ID)
	}
	var want []string
	for _, provider := range config.KnownAIAgentProviders() {
		want = append(want, string(provider))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ids %v != %v", got, want)
	}
}

func TestFindSettingsProviderEqualsNormalizeAIEnabledAgents(t *testing.T) {
	for _, input := range []string{
		"claude", "Claude", " CODEX ", "\tantigravity\n", "antigravity",
		"", "   ", "gemini", "claude code", "clau de",
	} {
		normalized := config.NormalizeAIEnabledAgents([]string{input})
		provider, ok := projmux.FindSettingsProvider(input)
		if ok != (len(normalized) == 1) {
			t.Fatalf("%q: found %v, internal normalized %v", input, ok, normalized)
		}
		if ok && provider.ID != string(normalized[0]) {
			t.Fatalf("%q: id %q != %q", input, provider.ID, normalized[0])
		}
		if !ok && provider != (core.SettingsProvider{}) {
			t.Fatalf("%q: not found but returned %+v", input, provider)
		}
	}
}

func TestSettingsProviderIDConstantsAreTheInternalIDs(t *testing.T) {
	for name, pair := range map[string][2]string{
		"claude":      {core.ProviderClaude, string(aiprovider.Claude)},
		"codex":       {core.ProviderCodex, string(aiprovider.Codex)},
		"antigravity": {core.ProviderAntigravity, string(aiprovider.Antigravity)},
	} {
		if pair[0] != pair[1] || pair[0] != name {
			t.Fatalf("%s: public %q internal %q", name, pair[0], pair[1])
		}
	}
}

func TestClaudeIntegrationCommandEqualsInternal(t *testing.T) {
	internal, ok := aiprovider.Lookup(string(aiprovider.Claude))
	if !ok {
		t.Fatal("claude is not registered")
	}
	provider, ok := projmux.FindSettingsProvider(core.ProviderClaude)
	if !ok || provider.IntegrationCommand == "" || provider.IntegrationCommand != internal.Integrate.Command {
		t.Fatalf("public %+v internal command %q", provider, internal.Integrate.Command)
	}
}

func TestSettingsProviderResultsAreCallerOwned(t *testing.T) {
	want := aiprovider.SettingsVisible()
	got := projmux.ListSettingsProviders()
	got[0].ID, got[0].DisplayName, got[0].IntegrationCommand = "changed", "changed", "changed"
	again := projmux.ListSettingsProviders()
	if len(again) != len(want) || again[0].ID != string(want[0].ID) || again[0].DisplayName != want[0].DisplayName {
		t.Fatalf("a changed result leaked into the next call: %+v", again)
	}
	if aiprovider.SettingsVisible()[0].DisplayName != want[0].DisplayName {
		t.Fatal("a changed result reached the internal registry")
	}
	found, _ := projmux.FindSettingsProvider(core.ProviderClaude)
	found.DisplayName = "changed"
	if again, _ := projmux.FindSettingsProvider(core.ProviderClaude); again.DisplayName == "changed" {
		t.Fatal("a changed found provider leaked into the next call")
	}
}
