package picker

import "testing"

func TestItemEffectiveSearchTextPrefersExplicitSearchText(t *testing.T) {
	t.Parallel()

	item := Item{
		Title:      "project-title",
		SearchText: "project alias",
	}

	if got, want := item.EffectiveSearchText(), "project alias"; got != want {
		t.Fatalf("EffectiveSearchText() = %q, want %q", got, want)
	}
}

func TestItemEffectiveSearchTextFallsBackToTitle(t *testing.T) {
	t.Parallel()

	item := Item{Title: " project-title "}

	if got, want := item.EffectiveSearchText(), "project-title"; got != want {
		t.Fatalf("EffectiveSearchText() = %q, want %q", got, want)
	}
}

func TestItemEffectiveLabelPrefersExplicitLabel(t *testing.T) {
	t.Parallel()

	item := Item{
		Label: " card\n  context ",
		Title: "project-title",
		Value: "/tmp/project",
	}

	if got, want := item.EffectiveLabel(), "card\n  context"; got != want {
		t.Fatalf("EffectiveLabel() = %q, want %q", got, want)
	}
}

func TestItemEffectiveLabelFallsBackToTitleThenValue(t *testing.T) {
	t.Parallel()

	if got, want := (Item{Title: " project-title ", Value: "/tmp/project"}).EffectiveLabel(), "project-title"; got != want {
		t.Fatalf("EffectiveLabel() with title = %q, want %q", got, want)
	}
	if got, want := (Item{Value: " /tmp/project "}).EffectiveLabel(), "/tmp/project"; got != want {
		t.Fatalf("EffectiveLabel() with value = %q, want %q", got, want)
	}
}
