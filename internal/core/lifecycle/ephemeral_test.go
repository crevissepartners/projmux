package lifecycle

import (
	"errors"
	"reflect"
	"testing"
)

func TestSelectReusableEphemeralPrefersNewestUnattachedEphemeral(t *testing.T) {
	t.Parallel()

	got, ok := SelectReusableEphemeral([]SessionInventory{
		{Name: "home", LastAttached: 99},
		{Name: "ephemeral-old", Ephemeral: true, LastAttached: 10},
		{Name: "ephemeral-new", Ephemeral: true, LastAttached: 20},
		{Name: "ephemeral-attached", Ephemeral: true, Attached: true, LastAttached: 30},
	})
	if !ok {
		t.Fatal("SelectReusableEphemeral() ok = false, want true")
	}
	if got != "ephemeral-new" {
		t.Fatalf("SelectReusableEphemeral() = %q, want %q", got, "ephemeral-new")
	}
}

func TestSelectReusableEphemeralReturnsFalseWithoutCandidate(t *testing.T) {
	t.Parallel()

	got, ok := SelectReusableEphemeral([]SessionInventory{
		{Name: "home"},
		{Name: "busy", Ephemeral: true, Attached: true},
	})
	if ok {
		t.Fatalf("SelectReusableEphemeral() ok = true with %q, want false", got)
	}
}

func TestPruneEphemeralTargetsKeepsMostRecentUnattachedSessions(t *testing.T) {
	t.Parallel()

	got, err := PruneEphemeralTargets([]SessionInventory{
		{Name: "older", Ephemeral: true, LastAttached: 10},
		{Name: "busy", Ephemeral: true, Attached: true, LastAttached: 999},
		{Name: "newest", Ephemeral: true, LastAttached: 30},
		{Name: "middle", Ephemeral: true, LastAttached: 20},
	}, 2)
	if err != nil {
		t.Fatalf("PruneEphemeralTargets() error = %v", err)
	}

	if want := []string{"older"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("PruneEphemeralTargets() = %#v, want %#v", got, want)
	}
}

func TestPruneEphemeralTargetsRejectsNegativeKeepCount(t *testing.T) {
	t.Parallel()

	_, err := PruneEphemeralTargets(nil, -1)
	if !errors.Is(err, ErrEphemeralKeepCountInvalid) {
		t.Fatalf("PruneEphemeralTargets() error = %v, want %v", err, ErrEphemeralKeepCountInvalid)
	}
}

func TestPlanAutoAttachReusesEphemeralSessionBeforePruning(t *testing.T) {
	t.Parallel()

	got, err := PlanAutoAttach(AutoAttachInputs{
		Sessions: []SessionInventory{
			{Name: "ephemeral", Ephemeral: true, LastAttached: 20},
			{Name: "older", Ephemeral: true, LastAttached: 10},
		},
		HomeSession: "home",
		KeepCount:   1,
	})
	if err != nil {
		t.Fatalf("PlanAutoAttach() error = %v", err)
	}

	want := AutoAttachPlan{
		AttachTarget: "ephemeral",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PlanAutoAttach() = %#v, want %#v", got, want)
	}
}

func TestPlanAutoAttachFallsBackToHomeAndPrunes(t *testing.T) {
	t.Parallel()

	got, err := PlanAutoAttach(AutoAttachInputs{
		Sessions: []SessionInventory{
			{Name: "newest", Ephemeral: true, Attached: true, LastAttached: 30},
			{Name: "middle", Ephemeral: true, Attached: true, LastAttached: 20},
			{Name: "older", Ephemeral: true, Attached: true, LastAttached: 10},
		},
		HomeSession: "home",
		KeepCount:   1,
	})
	if err != nil {
		t.Fatalf("PlanAutoAttach() error = %v", err)
	}

	want := AutoAttachPlan{
		AttachTarget:      "home",
		EnsureHomeSession: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("PlanAutoAttach() = %#v, want %#v", got, want)
	}
}
