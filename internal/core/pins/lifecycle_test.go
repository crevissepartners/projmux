package pins

import (
	"reflect"
	"testing"
)

func TestProjectDeletionPlanPreservesOrderAndPriorRootWithoutRetargeting(t *testing.T) {
	t.Parallel()

	input := Set{Format: FormatTyped, Pins: []Pin{
		{Kind: KindCandidate, Value: "/srv/first"},
		{Kind: KindProject, Value: "proj-deleted"},
		{Kind: KindProject, Value: "proj-reopened"},
		{Kind: KindCandidate, Value: "/srv/last"},
	}}
	before := Set{Format: input.Format, Pins: append([]Pin(nil), input.Pins...)}
	plan, err := PlanProjectDeletion(input, "proj-deleted", "/srv/prior-root")
	if err != nil {
		t.Fatalf("PlanProjectDeletion: %v", err)
	}
	if !reflect.DeepEqual(input, before) {
		t.Fatal("pure pin planning mutated the source set")
	}
	want := []Pin{
		{Kind: KindCandidate, Value: "/srv/first"},
		{Kind: KindCandidate, Value: "/srv/prior-root"},
		{Kind: KindProject, Value: "proj-reopened"},
		{Kind: KindCandidate, Value: "/srv/last"},
	}
	if !plan.Changed || plan.Replaced != 1 || !reflect.DeepEqual(plan.Desired.Pins, want) {
		t.Fatalf("plan = %+v, want pins %#v", plan, want)
	}
	for _, pin := range plan.Desired.Pins {
		if pin.Kind == KindProject && pin.Value == "proj-deleted" {
			t.Fatal("deleted Project UID remains dangling")
		}
	}
	if plan.Desired.Pins[1].Kind != KindCandidate || plan.Desired.Pins[1].Value != "/srv/prior-root" {
		t.Fatal("deleted pin was not replaced in the same order by the prior root")
	}
}

func TestProjectDeletionPlanIsAnExactManagedUIDNoOp(t *testing.T) {
	t.Parallel()

	input := Set{Format: FormatTyped, Pins: []Pin{
		{Kind: KindProject, Value: "proj-other"},
		{Kind: KindCandidate, Value: "/srv/prior-root"},
	}}
	plan, err := PlanProjectDeletion(input, "proj-deleted", "/srv/prior-root")
	if err != nil {
		t.Fatalf("PlanProjectDeletion: %v", err)
	}
	if plan.Changed || plan.Replaced != 0 || !reflect.DeepEqual(plan.Desired, input) {
		t.Fatalf("unmatched exact UID plan = %+v", plan)
	}
}

func TestProjectDeletionPlanDeduplicatesAnExistingPriorRootAtTheManagedSlot(t *testing.T) {
	t.Parallel()

	input := Set{Format: FormatTyped, Pins: []Pin{
		{Kind: KindCandidate, Value: "/srv/first"},
		{Kind: KindCandidate, Value: "/srv/prior-root"},
		{Kind: KindProject, Value: "proj-other"},
		{Kind: KindProject, Value: "proj-deleted"},
		{Kind: KindCandidate, Value: "/srv/last"},
	}}
	plan, err := PlanProjectDeletion(input, "proj-deleted", "/srv/prior-root")
	if err != nil {
		t.Fatalf("PlanProjectDeletion: %v", err)
	}
	want := []Pin{
		{Kind: KindCandidate, Value: "/srv/first"},
		{Kind: KindProject, Value: "proj-other"},
		{Kind: KindCandidate, Value: "/srv/prior-root"},
		{Kind: KindCandidate, Value: "/srv/last"},
	}
	if !reflect.DeepEqual(plan.Desired.Pins, want) {
		t.Fatalf("desired pins = %#v, want %#v", plan.Desired.Pins, want)
	}
	seen := 0
	for _, pin := range plan.Desired.Pins {
		if pin == (Pin{Kind: KindCandidate, Value: "/srv/prior-root"}) {
			seen++
		}
		if pin == (Pin{Kind: KindProject, Value: "proj-deleted"}) {
			t.Fatal("deleted managed UID remains dangling")
		}
	}
	if seen != 1 {
		t.Fatalf("prior-root candidate count = %d, want 1", seen)
	}
}

func TestProjectDeletionPlanRejectsMalformedUIDOrRoot(t *testing.T) {
	t.Parallel()

	if _, err := PlanProjectDeletion(Set{}, "pane-not-project", "/srv/root"); err == nil {
		t.Fatal("malformed Project UID accepted")
	}
	if _, err := PlanProjectDeletion(Set{}, "proj-deleted", "\n"); err == nil {
		t.Fatal("malformed prior root accepted")
	}
}
