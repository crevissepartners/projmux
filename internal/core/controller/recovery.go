package controller

import (
	"fmt"
	"slices"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

// RecoveryLevel is the closed loss-ordered recovery ladder. The numeric part
// is presentation, not authorization: D3 deliberately permits automatic L8
// mirror discard while keeping L7 import behind explicit human approval.
type RecoveryLevel string

const (
	RecoveryAnnounce        RecoveryLevel = "L0-announce"
	RecoveryReobserve       RecoveryLevel = "L1-reobserve"
	RecoveryRepairMirror    RecoveryLevel = "L2-repair-mirror"
	RecoveryProjectStatus   RecoveryLevel = "L3-project-status"
	RecoveryMaterialize     RecoveryLevel = "L4-materialize"
	RecoverySkipItem        RecoveryLevel = "L5-skip-item"
	RecoveryRepairInvariant RecoveryLevel = "L6-repair-invariant"
	RecoveryImport          RecoveryLevel = "L7-import"
	RecoveryDiscardMirror   RecoveryLevel = "L8-discard-mirror"
	RecoveryDiscardRow      RecoveryLevel = "L9-discard-registry-row"
	RecoveryRestoreRegistry RecoveryLevel = "L10-restore-registry"
)

// RecoveryStep states the loss paid by one ladder step. LossKind is stable
// machine-readable vocabulary used by destructive approval messages.
type RecoveryStep struct {
	Level    RecoveryLevel `json:"level"`
	Action   string        `json:"action"`
	LossKind string        `json:"lossKind"`
	UserData bool          `json:"userData"`
}

var recoveryLadder = []RecoveryStep{
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

// RecoveryLadder returns the complete declaration order used by reports.
func RecoveryLadder() []RecoveryStep { return slices.Clone(recoveryLadder) }

func (l RecoveryLevel) Valid() bool {
	return slices.ContainsFunc(RecoveryLadder(), func(step RecoveryStep) bool { return step.Level == l })
}

func (l RecoveryLevel) Step() (RecoveryStep, bool) {
	for _, step := range recoveryLadder {
		if step.Level == l {
			return step, true
		}
	}
	return RecoveryStep{}, false
}

// RecoveryTrigger is the complete set of producers that may request recovery.
type RecoveryTrigger string

const (
	RecoveryHookConverge RecoveryTrigger = "hook-converge"
	RecoveryExplicit     RecoveryTrigger = "explicit-reconcile"
	RecoveryProjectOpen  RecoveryTrigger = "project-open"
)

func RecoveryTriggers() []RecoveryTrigger {
	return []RecoveryTrigger{RecoveryHookConverge, RecoveryExplicit, RecoveryProjectOpen}
}

func (t RecoveryTrigger) Valid() bool { return slices.Contains(RecoveryTriggers(), t) }

// RecoveryAuthorityRow is one decided classification x trigger cell.
// Automatic is an explicit set rather than a <= comparison so the D3 L8/L7
// non-monotonic authority boundary cannot be flattened accidentally.
type RecoveryAuthorityRow struct {
	Divergence resourcegraph.Divergence `json:"divergence"`
	Trigger    RecoveryTrigger          `json:"trigger"`
	Automatic  []RecoveryLevel          `json:"automatic"`
	Max        RecoveryLevel            `json:"maxAutomaticLevel"`
}

var recoveryAuthorityTable = func() []RecoveryAuthorityRow {
	rows := make([]RecoveryAuthorityRow, 0, len(resourcegraph.Divergences())*len(RecoveryTriggers()))
	add := func(divergence resourcegraph.Divergence, trigger RecoveryTrigger, max RecoveryLevel, automatic ...RecoveryLevel) {
		rows = append(rows, RecoveryAuthorityRow{Divergence: divergence, Trigger: trigger, Automatic: automatic, Max: max})
	}
	add(resourcegraph.DivergenceUnrealized, RecoveryHookConverge, RecoveryAnnounce, RecoveryAnnounce)
	add(resourcegraph.DivergenceUnrealized, RecoveryExplicit, RecoveryProjectStatus, RecoveryRepairMirror, RecoveryProjectStatus)
	add(resourcegraph.DivergenceUnrealized, RecoveryProjectOpen, RecoverySkipItem, RecoveryMaterialize, RecoverySkipItem)
	for _, trigger := range RecoveryTriggers() {
		add(resourcegraph.DivergenceUnattributed, trigger, RecoveryAnnounce, RecoveryAnnounce)
		add(resourcegraph.DivergenceOrphanMirror, trigger, RecoveryDiscardMirror, RecoveryDiscardMirror)
		add(resourcegraph.DivergenceContaminated, trigger, RecoveryAnnounce, RecoveryAnnounce)
		add(resourcegraph.DivergenceDrifted, trigger, RecoveryProjectStatus, RecoveryRepairMirror, RecoveryProjectStatus)
		add(resourcegraph.DivergenceUnknown, trigger, RecoveryReobserve, RecoveryReobserve)
	}
	return rows
}()

// RecoveryAuthorityTable returns the complete printable table in D1-D6 then
// hook/explicit/open order.
func RecoveryAuthorityTable() []RecoveryAuthorityRow {
	out := make([]RecoveryAuthorityRow, len(recoveryAuthorityTable))
	for i, row := range recoveryAuthorityTable {
		out[i] = row
		out[i].Automatic = slices.Clone(row.Automatic)
	}
	slices.SortStableFunc(out, func(a, b RecoveryAuthorityRow) int {
		if rank := slices.Index(resourcegraph.Divergences(), a.Divergence) - slices.Index(resourcegraph.Divergences(), b.Divergence); rank != 0 {
			return rank
		}
		return slices.Index(RecoveryTriggers(), a.Trigger) - slices.Index(RecoveryTriggers(), b.Trigger)
	})
	return out
}

// RecoveryDecision is fail-closed. Off-table classifications, triggers, and
// levels are refused; on-table levels outside Automatic require approval.
type RecoveryDecision string

const (
	RecoveryAllowAutomatic  RecoveryDecision = "allow-automatic"
	RecoveryAllowApproved   RecoveryDecision = "allow-approved"
	RecoveryRequireApproval RecoveryDecision = "require-approval"
	RecoveryRefuse          RecoveryDecision = "refuse"
)

type RecoveryVerdict struct {
	Divergence resourcegraph.Divergence `json:"divergence"`
	Trigger    RecoveryTrigger          `json:"trigger"`
	Level      RecoveryLevel            `json:"level"`
	Decision   RecoveryDecision         `json:"decision"`
	LossKind   string                   `json:"lossKind,omitempty"`
	LossCount  int                      `json:"lossCount,omitempty"`
	Reason     string                   `json:"reason"`
}

// AuthorizeRecovery answers one requested recovery action. Approval never
// makes an unknown/off-table value executable, and automatic mode never loses
// a user-data-bearing Registry row or Registry history.
func AuthorizeRecovery(divergence resourcegraph.Divergence, trigger RecoveryTrigger, level RecoveryLevel, approved bool, lossCount int) RecoveryVerdict {
	verdict := RecoveryVerdict{Divergence: divergence, Trigger: trigger, Level: level, Decision: RecoveryRefuse}
	step, validLevel := level.Step()
	if !divergence.Valid() || !trigger.Valid() || !validLevel {
		verdict.Reason = "recovery request is outside the closed authority table"
		return verdict
	}
	var row *RecoveryAuthorityRow
	table := RecoveryAuthorityTable()
	for i := range table {
		candidate := &table[i]
		if candidate.Divergence == divergence && candidate.Trigger == trigger {
			row = candidate
			break
		}
	}
	if row == nil {
		verdict.Reason = "recovery request is outside the closed authority table"
		return verdict
	}
	verdict.LossKind = step.LossKind
	verdict.LossCount = lossCount
	if slices.Contains(row.Automatic, level) {
		if step.UserData {
			verdict.Decision = RecoveryRequireApproval
			verdict.Reason = "automatic recovery cannot lose user data"
			return verdict
		}
		verdict.Decision = RecoveryAllowAutomatic
		verdict.Reason = "classification and trigger permit this exact automatic recovery level"
		return verdict
	}
	verdict.Decision = RecoveryRequireApproval
	verdict.Reason = fmt.Sprintf("%s requires human approval; loss kind %s, count %d", level, step.LossKind, lossCount)
	if approved {
		verdict.Decision = RecoveryAllowApproved
		verdict.Reason = fmt.Sprintf("human approved %s; loss kind %s, count %d", level, step.LossKind, lossCount)
	}
	return verdict
}
