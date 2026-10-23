package usage

import (
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/usage"
)

// TestHUDProviderCapabilitiesTable pins the moved capability authority: the
// usage-supported provider order, each window's key and default (Codex 5h
// off) and fresh slices on every call.
func TestHUDProviderCapabilitiesTable(t *testing.T) {
	type window struct {
		Window usage.Window
		Key    string
		Label  string
		Def    config.StatusbarVisibility
	}
	want := map[aiprovider.ID][]window{
		aiprovider.Claude: {{usage.Window5h, "5h", "5h", config.StatusbarVisibilityOn}, {usage.WindowWeekly, "weekly", "Weekly", config.StatusbarVisibilityOn}},
		aiprovider.Codex:  {{usage.Window5h, "5h", "5h", config.StatusbarVisibilityOff}, {usage.WindowWeekly, "weekly", "Weekly", config.StatusbarVisibilityOn}},
	}
	got := HUDProviderCapabilities()
	supported := aiprovider.UsageSupported()
	if len(got) != len(supported) {
		t.Fatalf("capabilities = %d, want %d usage-supported providers", len(got), len(supported))
	}
	for i, capability := range got {
		provider := supported[i]
		if capability.ID != provider.ID || capability.Model != provider.UsageModel || capability.DisplayName != provider.DisplayName {
			t.Fatalf("capability %d = %+v, want provider %+v", i, capability, provider)
		}
		windows := []window{}
		for _, w := range capability.Windows {
			windows = append(windows, window{w.Window, w.Key, w.Label, w.DefaultVisibility})
		}
		if !reflect.DeepEqual(windows, want[capability.ID]) {
			t.Fatalf("%s windows = %+v, want %+v", capability.ID, windows, want[capability.ID])
		}
	}
	got[0].Windows[0].DefaultVisibility = config.StatusbarVisibilityOff
	got[0].Windows = nil
	if again := HUDProviderCapabilities(); again[0].Windows[0].DefaultVisibility != config.StatusbarVisibilityOn {
		t.Fatalf("a caller's change leaked into the next call: %+v", again[0])
	}
}
