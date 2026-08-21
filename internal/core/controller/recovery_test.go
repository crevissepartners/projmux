package controller

import (
	"fmt"
	"slices"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

var recoveryLadderGolden = []RecoveryStep{
	{RecoveryAnnounce, "announce only", "none", false},
	{RecoveryReobserve, "reobserve", "time", false},
	{RecoveryRepairMirror, "repair mirror from Registry", "none", false},
	{RecoveryProjectStatus, "project observed state into Registry status", "none", false},
	{RecoveryMaterialize, "materialize item", "none", false},
	{RecoverySkipItem, "skip item and announce", "item-restoration", false},
	{RecoveryRepairInvariant, "repair Registry invariant", "incorrect-field-value", false},
	{RecoveryImport, "import runtime object into Registry", "identity-provenance", false},
	{RecoveryDiscardMirror, "discard runtime identity mirror", "management-history", false},
	{RecoveryDiscardRow, "discard Registry row", "registry-row-conversation-pointer-snapshot-recoverability", true},
	{RecoveryRestoreRegistry, "restore Registry", "intervening-registry-changes", true},
}

var recoveryAuthorityGolden = []RecoveryAuthorityRow{
	{resourcegraph.DivergenceUnrealized, RecoveryHookConverge, []RecoveryLevel{RecoveryAnnounce}, RecoveryAnnounce},
	{resourcegraph.DivergenceUnrealized, RecoveryExplicit, []RecoveryLevel{RecoveryRepairMirror, RecoveryProjectStatus}, RecoveryProjectStatus},
	{resourcegraph.DivergenceUnrealized, RecoveryProjectOpen, []RecoveryLevel{RecoveryMaterialize, RecoverySkipItem}, RecoverySkipItem},
	{resourcegraph.DivergenceUnattributed, RecoveryHookConverge, []RecoveryLevel{RecoveryAnnounce}, RecoveryAnnounce},
	{resourcegraph.DivergenceUnattributed, RecoveryExplicit, []RecoveryLevel{RecoveryAnnounce}, RecoveryAnnounce},
	{resourcegraph.DivergenceUnattributed, RecoveryProjectOpen, []RecoveryLevel{RecoveryAnnounce}, RecoveryAnnounce},
	{resourcegraph.DivergenceOrphanMirror, RecoveryHookConverge, []RecoveryLevel{RecoveryDiscardMirror}, RecoveryDiscardMirror},
	{resourcegraph.DivergenceOrphanMirror, RecoveryExplicit, []RecoveryLevel{RecoveryDiscardMirror}, RecoveryDiscardMirror},
	{resourcegraph.DivergenceOrphanMirror, RecoveryProjectOpen, []RecoveryLevel{RecoveryDiscardMirror}, RecoveryDiscardMirror},
	{resourcegraph.DivergenceContaminated, RecoveryHookConverge, []RecoveryLevel{RecoveryAnnounce}, RecoveryAnnounce},
	{resourcegraph.DivergenceContaminated, RecoveryExplicit, []RecoveryLevel{RecoveryAnnounce}, RecoveryAnnounce},
	{resourcegraph.DivergenceContaminated, RecoveryProjectOpen, []RecoveryLevel{RecoveryAnnounce}, RecoveryAnnounce},
	{resourcegraph.DivergenceDrifted, RecoveryHookConverge, []RecoveryLevel{RecoveryRepairMirror, RecoveryProjectStatus}, RecoveryProjectStatus},
	{resourcegraph.DivergenceDrifted, RecoveryExplicit, []RecoveryLevel{RecoveryRepairMirror, RecoveryProjectStatus}, RecoveryProjectStatus},
	{resourcegraph.DivergenceDrifted, RecoveryProjectOpen, []RecoveryLevel{RecoveryRepairMirror, RecoveryProjectStatus}, RecoveryProjectStatus},
	{resourcegraph.DivergenceUnknown, RecoveryHookConverge, []RecoveryLevel{RecoveryReobserve}, RecoveryReobserve},
	{resourcegraph.DivergenceUnknown, RecoveryExplicit, []RecoveryLevel{RecoveryReobserve}, RecoveryReobserve},
	{resourcegraph.DivergenceUnknown, RecoveryProjectOpen, []RecoveryLevel{RecoveryReobserve}, RecoveryReobserve},
}

func TestRecoveryLadderL0ThroughL10IsClosedLossOrderedAndPrintable(t *testing.T) {
	steps := RecoveryLadder()
	if !slices.Equal(steps, recoveryLadderGolden) {
		t.Fatalf("RecoveryLadder() =\n%+v\nwant exact action/loss contract:\n%+v", steps, recoveryLadderGolden)
	}
	t.Logf("recovery ladder: %v", steps)
}

func TestRecoveryAuthorityClassificationTriggerTableIsClosedAndPrintable(t *testing.T) {
	rows := RecoveryAuthorityTable()
	if got, want := len(rows), len(recoveryAuthorityGolden); got != want {
		t.Fatalf("table rows = %d, want %d", got, want)
	}
	for i, want := range recoveryAuthorityGolden {
		got := rows[i]
		if got.Divergence != want.Divergence || got.Trigger != want.Trigger || got.Max != want.Max || !slices.Equal(got.Automatic, want.Automatic) {
			t.Fatalf("authority cell %s/%s = %+v, want exact %+v", want.Divergence, want.Trigger, got, want)
		}
	}
	t.Logf("recovery authority: %v", rows)
}

func TestPropertyEveryClassificationTriggerAutomaticAllowanceIsExact(t *testing.T) {
	for _, divergence := range resourcegraph.Divergences() {
		for _, trigger := range RecoveryTriggers() {
			for _, step := range RecoveryLadder() {
				verdict := AuthorizeRecovery(divergence, trigger, step.Level, false, 1)
				row := slices.IndexFunc(recoveryAuthorityGolden, func(row RecoveryAuthorityRow) bool {
					return row.Divergence == divergence && row.Trigger == trigger
				})
				if row < 0 {
					t.Fatalf("missing cell %s/%s", divergence, trigger)
				}
				wantAutomatic := slices.Contains(recoveryAuthorityGolden[row].Automatic, step.Level) && !step.UserData
				if got := verdict.Decision == RecoveryAllowAutomatic; got != wantAutomatic {
					t.Fatalf("%s/%s/%s automatic=%t, want %t: %+v", divergence, trigger, step.Level, got, wantAutomatic, verdict)
				}
			}
		}
	}
	for _, malformed := range []RecoveryVerdict{
		AuthorizeRecovery("D7", RecoveryHookConverge, RecoveryAnnounce, false, 1),
		AuthorizeRecovery(resourcegraph.DivergenceOrphanMirror, "timer", RecoveryDiscardMirror, false, 1),
		AuthorizeRecovery(resourcegraph.DivergenceOrphanMirror, RecoveryHookConverge, "L11", false, 1),
	} {
		if malformed.Decision != RecoveryRefuse {
			t.Fatalf("off-table recovery did not fail closed: %+v", malformed)
		}
	}
}

func TestD3AutomaticL8DoesNotAuthorizeLowerOrdinalL7Import(t *testing.T) {
	for _, trigger := range RecoveryTriggers() {
		discard := AuthorizeRecovery(resourcegraph.DivergenceOrphanMirror, trigger, RecoveryDiscardMirror, false, 2)
		if discard.Decision != RecoveryAllowAutomatic {
			t.Fatalf("%s D3 L8 = %+v", trigger, discard)
		}
		adopt := AuthorizeRecovery(resourcegraph.DivergenceOrphanMirror, trigger, RecoveryImport, false, 2)
		if adopt.Decision != RecoveryRequireApproval || adopt.LossKind != "identity-provenance" || adopt.LossCount != 2 {
			t.Fatalf("%s D3 L7 = %+v", trigger, adopt)
		}
	}
}

func TestApprovalRequiredRecoveryNamesLossKindAndCount(t *testing.T) {
	for _, level := range []RecoveryLevel{RecoveryImport, RecoveryDiscardRow, RecoveryRestoreRegistry} {
		verdict := AuthorizeRecovery(resourcegraph.DivergenceOrphanMirror, RecoveryExplicit, level, false, 3)
		if verdict.Decision != RecoveryRequireApproval || verdict.LossKind == "" || verdict.LossCount != 3 {
			t.Fatalf("approval disclosure for %s = %+v", level, verdict)
		}
		if got := fmt.Sprint(verdict); !slices.ContainsFunc([]string{got}, func(value string) bool {
			return value != "" && verdict.LossKind != "" && verdict.LossCount == 3
		}) {
			t.Fatalf("unprintable approval: %s", got)
		}
	}
}

func TestAutomaticRecoveryProducerInventoryIsClosedAndEveryPathIsAuthorized(t *testing.T) {
	seen := map[string]bool{}
	for _, path := range AutomaticRecoveryPaths() {
		if path.Name == "" || seen[path.Name] {
			t.Fatalf("blank or duplicate automatic path: %+v", path)
		}
		seen[path.Name] = true
		if err := RequireAutomaticRecoveryPath(path.Name); err != nil {
			t.Fatal(err)
		}
	}
	if err := RequireAutomaticRecoveryPath("unclassified-new-producer"); err == nil {
		t.Fatal("an unclassified producer obtained automatic authority")
	}
}

func TestAutomaticRecoveryNeverDeletesConversationSnapshotOrRegistryRow(t *testing.T) {
	for _, divergence := range resourcegraph.Divergences() {
		for _, trigger := range RecoveryTriggers() {
			for _, level := range []RecoveryLevel{RecoveryDiscardRow, RecoveryRestoreRegistry} {
				if verdict := AuthorizeRecovery(divergence, trigger, level, false, 1); verdict.Decision == RecoveryAllowAutomatic {
					t.Fatalf("automatic %s/%s authorized user-data loss at %s: %+v", divergence, trigger, level, verdict)
				}
			}
		}
	}
}
