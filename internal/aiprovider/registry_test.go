package aiprovider

import (
	"reflect"
	"testing"
)

func TestProviderRegistryOrdersSurfaces(t *testing.T) {
	t.Parallel()

	if got, want := providerIDs(SettingsVisible()), []ID{Claude, Codex, Antigravity}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SettingsVisible() = %#v, want %#v", got, want)
	}
	if got, want := providerIDs(PickerEligible()), []ID{Codex, Claude, Antigravity}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PickerEligible() = %#v, want %#v", got, want)
	}
	if got, want := providerIDs(UsageSupported()), []ID{Claude, Codex}; !reflect.DeepEqual(got, want) {
		t.Fatalf("UsageSupported() = %#v, want %#v", got, want)
	}
	if got, want := providerIDs(HookDiagnosticSupported()), []ID{Claude, Codex, Antigravity}; !reflect.DeepEqual(got, want) {
		t.Fatalf("HookDiagnosticSupported() = %#v, want %#v", got, want)
	}
}

func TestProviderRegistryMetadataForCurrentAgents(t *testing.T) {
	t.Parallel()

	for _, id := range []ID{Claude, Codex, Antigravity} {
		provider, ok := Lookup(string(id))
		if !ok {
			t.Fatalf("Lookup(%q) missing", id)
		}
		if provider.DisplayName == "" || provider.BinaryName == "" {
			t.Fatalf("provider %#v missing display/binary metadata", provider)
		}
		if !provider.SettingsVisible || !provider.PickerEligible {
			t.Fatalf("provider %#v missing enabled-agent surface support", provider)
		}
	}

	for _, id := range []ID{Claude, Codex} {
		provider, ok := Lookup(string(id))
		if !ok {
			t.Fatalf("Lookup(%q) missing", id)
		}
		if provider.UsageModel == "" || !provider.UsageSupported {
			t.Fatalf("provider %#v missing usage metadata", provider)
		}
		if !provider.Integrate.Supported || provider.Integrate.Command == "" {
			t.Fatalf("provider %#v missing integrate metadata", provider)
		}
		if !provider.HookDiagnostics.Supported || provider.HookDiagnostics.ID == "" || provider.HookDiagnostics.Name == "" {
			t.Fatalf("provider %#v missing hook diagnostic metadata", provider)
		}
	}

	antigravity, ok := Lookup(string(Antigravity))
	if !ok {
		t.Fatalf("Lookup(antigravity) missing")
	}
	// Antigravity participates in managed hook integration and hook
	// diagnostics surfaces. It has no usage source: the statusLine bridge that
	// fed one was removed.
	if antigravity.UsageSupported || antigravity.UsageModel != "" {
		t.Fatalf("Antigravity metadata = %#v, want no usage support", antigravity)
	}
	if !antigravity.Integrate.Supported || antigravity.Integrate.Command != "projmux agent integrate antigravity" {
		t.Fatalf("Antigravity metadata = %#v, want managed integration support", antigravity)
	}
	if !antigravity.HookDiagnostics.Supported || antigravity.HookDiagnostics.ID != "antigravity-hooks" || antigravity.HookDiagnostics.Name != "Antigravity hooks" || antigravity.HookProvider != "antigravity" {
		t.Fatalf("Antigravity hook metadata = %#v, want managed hook diagnostics support", antigravity)
	}
}

func providerIDs(providers []Metadata) []ID {
	out := make([]ID, 0, len(providers))
	for _, provider := range providers {
		out = append(out, provider.ID)
	}
	return out
}
