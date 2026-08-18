package controller

import (
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// The policy is a table, so it is tested as one. Every assertion below is about
// the whole cross product rather than a chosen example: a new attribution class
// or a new intent that nobody wired into Decide fails here instead of shipping
// with a silently permissive default.

// policyTable renders the whole cross product the way the package decides it.
// Building it here rather than exporting it keeps the production surface to what
// production calls, while the review question -- "what does this policy say?" --
// still has one answer that fits on a screen.
func policyTable() []Verdict {
	out := make([]Verdict, 0, len(Intents())*len(resourcegraph.Classes()))
	for _, intent := range Intents() {
		for _, class := range resourcegraph.Classes() {
			out = append(out, Decide(intent, Subject{Class: class}))
		}
	}
	return out
}

func TestPolicyTableCoversEveryIntentAndClassExactlyOnce(t *testing.T) {
	t.Parallel()

	seen := map[string]int{}
	for _, verdict := range policyTable() {
		seen[string(verdict.Intent)+"/"+string(verdict.Class)]++
	}
	if want := len(Intents()) * len(resourcegraph.Classes()); len(seen) != want {
		t.Fatalf("policy table covers %d cells, want %d", len(seen), want)
	}
	for _, intent := range Intents() {
		for _, class := range resourcegraph.Classes() {
			key := string(intent) + "/" + string(class)
			if seen[key] != 1 {
				t.Fatalf("policy cell %s appears %d times, want exactly 1", key, seen[key])
			}
			if strings.TrimSpace(Decide(intent, Subject{Class: class}).Reason) == "" {
				t.Fatalf("policy cell %s has no stated reason", key)
			}
		}
	}
}

func TestPolicyRefusesEveryLifecycleIntentForEveryClass(t *testing.T) {
	t.Parallel()

	// This is acceptance criterion 2 expressed at its source. An offline
	// resource, Home, and an unattributed Pane are not protected by the
	// reconciler happening not to plan a start; they are protected because
	// there is no cell in this table that permits one.
	for _, intent := range []Intent{IntentStart, IntentImport, IntentDelete} {
		for _, class := range resourcegraph.Classes() {
			// The grant buys repair on a foreign host and nothing else. A
			// lifecycle intent stays refused with or without it.
			for _, grant := range []Grant{{}, {OperatorTargeted: true}} {
				verdict := Decide(intent, Subject{Class: class, Grant: grant})
				if verdict.Authority != AuthorityRefuse {
					t.Fatalf("Decide(%s, %s, %+v) = %s, want refuse", intent, class, grant, verdict.Authority)
				}
				if strings.TrimSpace(verdict.Reason) == "" {
					t.Fatalf("Decide(%s, %s) refused without a reason", intent, class)
				}
			}
		}
	}
}

func TestPolicyAllowsRepairOnlyWhereTheRegistryOwnsTheIdentity(t *testing.T) {
	t.Parallel()

	want := map[resourcegraph.Class]Authority{
		// The Registry owns this object outright.
		resourcegraph.ClassManaged: AuthorityAllow,
		// No competing identity to overwrite: restoring a mirror the Registry
		// already owns cannot take an object away from anybody.
		resourcegraph.ClassUnattributed: AuthorityAllow,
		// Carries a uid this Registry does not own. Overwriting it destroys the
		// only evidence an operator-driven recovery would work from.
		resourcegraph.ClassRecoverable: AuthorityRefuse,
		resourcegraph.ClassControl:     AuthorityObserve,
		resourcegraph.ClassEphemeral:   AuthorityObserve,
		// Unmarked on somebody else's server, and nobody asked about that
		// server. Refusing is the default.
		resourcegraph.ClassForeign:  AuthorityRefuse,
		resourcegraph.ClassConflict: AuthorityRefuse,
	}
	for _, intent := range []Intent{IntentRepairBinding, IntentRepairMirror} {
		for _, class := range resourcegraph.Classes() {
			got := Decide(intent, Subject{Class: class}).Authority
			if got != want[class] {
				t.Fatalf("Decide(%s, %s) = %s, want %s", intent, class, got, want[class])
			}
		}
	}
}

func TestOperatorGrantChangesExactlyTheForeignRepairCell(t *testing.T) {
	t.Parallel()

	granted := Grant{OperatorTargeted: true}
	for _, intent := range Intents() {
		for _, class := range resourcegraph.Classes() {
			plain := Decide(intent, Subject{Class: class})
			withGrant := Decide(intent, Subject{Class: class, Grant: granted})
			differs := plain.Authority != withGrant.Authority
			wantDiffers := intent.Repairs() && class == resourcegraph.ClassForeign
			if differs != wantDiffers {
				t.Fatalf("grant changed Decide(%s, %s) from %s to %s; want changed=%t",
					intent, class, plain.Authority, withGrant.Authority, wantDiffers)
			}
		}
	}
	if got := Decide(IntentRepairBinding, Subject{Class: resourcegraph.ClassForeign, Grant: granted}); got.Authority != AuthorityAllow {
		t.Fatalf("granted foreign repair = %s, want allow", got.Authority)
	}
	if got := Decide(IntentImport, Subject{Class: resourcegraph.ClassForeign, Grant: granted}); got.Authority != AuthorityRefuse {
		t.Fatalf("the grant bought an import: %+v", got)
	}
}

func TestPolicyFailsClosedOnAnUnknownClass(t *testing.T) {
	t.Parallel()

	for _, intent := range Intents() {
		verdict := Decide(intent, Subject{Class: resourcegraph.Class("invented-later")})
		if verdict.Authority != AuthorityRefuse {
			t.Fatalf("Decide(%s, unknown class) = %s, want refuse", intent, verdict.Authority)
		}
	}
}

func TestPolicyTableOrderIsStable(t *testing.T) {
	t.Parallel()

	first, second := policyTable(), policyTable()
	if len(first) != len(second) {
		t.Fatalf("table length changed between calls")
	}
	for index := range first {
		if first[index] != second[index] {
			t.Fatalf("table row %d changed between calls: %+v vs %+v", index, first[index], second[index])
		}
	}
	shuffled := []Verdict{first[len(first)-1], first[0], first[len(first)/2]}
	SortVerdicts(shuffled)
	if intentRank(shuffled[0].Intent) > intentRank(shuffled[len(shuffled)-1].Intent) {
		t.Fatalf("SortVerdicts did not restore intent order: %+v", shuffled)
	}
}

func TestIntentRepairsNamesExactlyTheConvergenceIntents(t *testing.T) {
	t.Parallel()

	repairs := 0
	for _, intent := range Intents() {
		if intent.Repairs() {
			repairs++
		}
	}
	if repairs != 2 {
		t.Fatalf("repair intents = %d, want exactly repair-binding and repair-mirror", repairs)
	}
}
