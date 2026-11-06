package projmux_test

import (
	"testing"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/pkg/projmux"
)

func TestUsageHUDProvidersEqualInternal(t *testing.T) {
	want := usagecmd.HUDProviderCapabilities()
	got := projmux.UsageHUDProviders()
	if len(got) == 0 || len(got) != len(want) {
		t.Fatalf("providers %d, want %d", len(got), len(want))
	}
	for i, provider := range want {
		g := got[i]
		if g.ID != string(provider.ID) || g.Model != provider.Model || g.DisplayName != provider.DisplayName || len(g.Windows) != len(provider.Windows) {
			t.Fatalf("provider %d = %+v, want %+v", i, g, provider)
		}
		for j, window := range provider.Windows {
			w := g.Windows[j]
			if string(w.Window) != string(window.Window) || w.Key != window.Key || w.Label != window.Label || string(w.DefaultVisibility) != string(window.DefaultVisibility) {
				t.Fatalf("provider %s window %d = %+v, want %+v", g.ID, j, w, window)
			}
		}
	}
}

func TestUsageHUDProvidersAreCopiedPerCall(t *testing.T) {
	first := projmux.UsageHUDProviders()
	want := projmux.UsageHUDProviders()
	for i := range first {
		first[i].ID, first[i].Model, first[i].DisplayName = "x", "x", "x"
		for j := range first[i].Windows {
			first[i].Windows[j].Key, first[i].Windows[j].DefaultVisibility = "x", "x"
		}
		first[i].Windows = nil
	}
	got := projmux.UsageHUDProviders()
	if len(got) != len(want) {
		t.Fatalf("providers %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID || got[i].DisplayName != want[i].DisplayName || len(got[i].Windows) != len(want[i].Windows) {
			t.Fatalf("provider %d changed by a caller: %+v, want %+v", i, got[i], want[i])
		}
		for j := range want[i].Windows {
			if got[i].Windows[j] != want[i].Windows[j] {
				t.Fatalf("window %d.%d changed by a caller: %+v, want %+v", i, j, got[i].Windows[j], want[i].Windows[j])
			}
		}
	}
}

func TestUsageModelLabelEqualsInternal(t *testing.T) {
	for _, model := range []string{"", "claude", "CLAUDE", "Claude", "codex", "CODEX", "Codex", "gemini", "Gemini", "GEMINI", "élan", "Élan", "x", "ß"} {
		if got, want := projmux.UsageModelLabel(model), usagecmd.ModelDisplayLabel(model); got != want {
			t.Errorf("UsageModelLabel(%q) = %q, want %q", model, got, want)
		}
	}
	if got := projmux.UsageModelLabel(""); got != "?" {
		t.Errorf("empty = %q, want ?", got)
	}
}
