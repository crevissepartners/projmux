package theme

import "testing"

func TestEvaluateFontApplicationUnsupportedIsNotApplied(t *testing.T) {
	t.Parallel()

	effective := ResolveTheme(ThemeConfig{
		FontFamily: "Cascadia Mono",
		FontSize:   "12",
	})
	got := EvaluateFontApplication(effective, NoFontCapability())

	if got.Status != FontApplyNotApplied {
		t.Fatalf("font apply status = %q, want %q", got.Status, FontApplyNotApplied)
	}
	if got.Desired() != "Cascadia Mono 12" {
		t.Fatalf("desired font = %q, want Cascadia Mono 12", got.Desired())
	}
	if got.Reason == "" {
		t.Fatalf("font apply reason empty, want unsupported reason")
	}
}

func TestEvaluateFontApplicationUnsetIsNotRequested(t *testing.T) {
	t.Parallel()

	got := EvaluateFontApplication(ResolveTheme(ThemeConfig{}), NoFontCapability())

	if got.Status != FontApplyNotRequested {
		t.Fatalf("font apply status = %q, want %q", got.Status, FontApplyNotRequested)
	}
	if got.Desired() != "(unset)" {
		t.Fatalf("desired font = %q, want unset", got.Desired())
	}
}
