package lifecycle

import (
	"errors"
	"testing"
)

func TestPlanTaggedKillSwitchSkipsWhenCurrentSessionIsNotKilled(t *testing.T) {
	t.Parallel()

	got, err := PlanTaggedKillSwitch(TaggedKillInputs{
		CurrentSession: "repo-a",
		KillTargets:    []string{"repo-b", "repo-c"},
		RecentSessions: []string{"repo-b", "repo-d"},
		HomeSession:    "home",
	})
	if err != nil {
		t.Fatalf("PlanTaggedKillSwitch() error = %v", err)
	}

	if got.SwitchNeeded {
		t.Fatalf("PlanTaggedKillSwitch() SwitchNeeded = true, want false")
	}

	if got.Target != "" {
		t.Fatalf("PlanTaggedKillSwitch() Target = %q, want empty", got.Target)
	}
}

func TestPlanTaggedKillSwitchChoosesMostRecentSurvivor(t *testing.T) {
	t.Parallel()

	got, err := PlanTaggedKillSwitch(TaggedKillInputs{
		CurrentSession: "repo-a",
		KillTargets:    []string{"repo-a", "repo-b"},
		RecentSessions: []string{"repo-b", "repo-c", "repo-d"},
		HomeSession:    "home",
	})
	if err != nil {
		t.Fatalf("PlanTaggedKillSwitch() error = %v", err)
	}

	if !got.SwitchNeeded {
		t.Fatalf("PlanTaggedKillSwitch() SwitchNeeded = false, want true")
	}

	if got.Target != "repo-c" {
		t.Fatalf("PlanTaggedKillSwitch() Target = %q, want %q", got.Target, "repo-c")
	}
}

func TestPlanTaggedKillSwitchFallsBackToHome(t *testing.T) {
	t.Parallel()

	got, err := PlanTaggedKillSwitch(TaggedKillInputs{
		CurrentSession: "repo-a",
		KillTargets:    []string{"repo-a", "repo-b"},
		RecentSessions: []string{"repo-b", "repo-a"},
		HomeSession:    "home",
	})
	if err != nil {
		t.Fatalf("PlanTaggedKillSwitch() error = %v", err)
	}

	if !got.SwitchNeeded {
		t.Fatalf("PlanTaggedKillSwitch() SwitchNeeded = false, want true")
	}

	if got.Target != "home" {
		t.Fatalf("PlanTaggedKillSwitch() Target = %q, want %q", got.Target, "home")
	}
}

func TestPlanTaggedKillSwitchRequiresHomeFallbackWhenNeeded(t *testing.T) {
	t.Parallel()

	_, err := PlanTaggedKillSwitch(TaggedKillInputs{
		CurrentSession: "repo-a",
		KillTargets:    []string{"repo-a"},
	})
	if err == nil {
		t.Fatal("PlanTaggedKillSwitch() error = nil, want non-nil")
	}

	if !errors.Is(err, ErrHomeSessionRequired) {
		t.Fatalf("PlanTaggedKillSwitch() error = %v, want %v", err, ErrHomeSessionRequired)
	}
}

func TestPlanTaggedKillSwitchIgnoresBlankEntries(t *testing.T) {
	t.Parallel()

	got, err := PlanTaggedKillSwitch(TaggedKillInputs{
		CurrentSession: "repo-a",
		KillTargets:    []string{"", "repo-a", "   "},
		RecentSessions: []string{"", "   ", "repo-b"},
		HomeSession:    "home",
	})
	if err != nil {
		t.Fatalf("PlanTaggedKillSwitch() error = %v", err)
	}

	if !got.SwitchNeeded {
		t.Fatalf("PlanTaggedKillSwitch() SwitchNeeded = false, want true")
	}

	if got.Target != "repo-b" {
		t.Fatalf("PlanTaggedKillSwitch() Target = %q, want %q", got.Target, "repo-b")
	}
}
