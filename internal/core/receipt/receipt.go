// Package receipt owns the transport-independent operation receipt vocabulary
// and data model. CLI catalog validation and output rendering remain in cli.
package receipt

import (
	"slices"
	"strings"
)

type IdentityEffect string
type AddressEffect string
type TopologyEffect string
type DesiredStateEffect string
type RuntimeEffect string
type FocusEffect string
type DomainEffectKind string

const (
	IdentityUnchanged IdentityEffect = "unchanged"
	IdentityCreated   IdentityEffect = "created"
	IdentityReused    IdentityEffect = "reused"
	IdentityRemoved   IdentityEffect = "removed"
	IdentityReplaced  IdentityEffect = "replaced"
)
const (
	AddressUnchanged AddressEffect = "unchanged"
	AddressAllocated AddressEffect = "allocated"
	AddressRenamed   AddressEffect = "renamed"
	AddressReleased  AddressEffect = "released"
)
const (
	TopologyUnchanged   TopologyEffect = "unchanged"
	TopologyEstablished TopologyEffect = "established"
	TopologyReparented  TopologyEffect = "reparented"
	TopologyRemoved     TopologyEffect = "removed"
	TopologyReplaced    TopologyEffect = "replaced"
)
const (
	DesiredStateUnchanged DesiredStateEffect = "unchanged"
	DesiredStateCreated   DesiredStateEffect = "created"
	DesiredStateReused    DesiredStateEffect = "reused"
	DesiredStateRemoved   DesiredStateEffect = "removed"
	DesiredStateReplaced  DesiredStateEffect = "replaced"
)
const (
	RuntimeUnchanged    RuntimeEffect = "unchanged"
	RuntimeMaterialized RuntimeEffect = "materialized"
	RuntimeAlreadyLive  RuntimeEffect = "already-live"
	RuntimeReparented   RuntimeEffect = "reparented"
	RuntimeStopped      RuntimeEffect = "stopped"
	RuntimePreserved    RuntimeEffect = "preserved"
)
const (
	FocusUnchanged          FocusEffect = "unchanged"
	FocusMovedCurrentClient FocusEffect = "moved-current-client"
	FocusAttachedCaller     FocusEffect = "attached-caller"
)
const DomainEffectAgentDelivery DomainEffectKind = "agent-delivery"

// ReceiptAPIVersion is the versioned envelope of one operation result.
//
// It is spelled the way the resource documents are: a schema name and a major
// version, so a consumer can pin the shape it parses and a later additive field
// cannot be mistaken for a different record.
const ReceiptAPIVersion = "OperationReceipt/v1"

// Operation is the closed set of receipt-emitting operations.
//
// The spelling is `<verb>.<kind>` rather than the argv spelling because a
// receipt records what happened, not which door it was asked through: the
// provider shortcuts and the canonical `create agent` produce the same
// `create.agent`, and both `unregister project` and its deprecated
// `delete project` alias produce `unregister.project`.
type Operation string

const (
	OperationCreateProject Operation = "create.project"
	OperationCreateWindow  Operation = "create.window"
	OperationCreatePane    Operation = "create.pane"
	OperationCreateAgent   Operation = "create.agent"
	OperationResumeAgent   Operation = "agent.resume"
	// OperationMarkOfflineAgent retires a lost host generation. It is never
	// a stop: no runtime was stopped and no Wait was observed.
	OperationMarkOfflineAgent Operation = "agent.mark-offline"

	OperationRenameProject Operation = "rename.project"
	OperationRenameWindow  Operation = "rename.window"
	OperationRenamePane    Operation = "rename.pane"
	OperationRenameAgent   Operation = "rename.agent"

	OperationDeleteWindow Operation = "delete.window"
	OperationDeletePane   Operation = "delete.pane"
	OperationDeleteAgent  Operation = "delete.agent"

	OperationStartProject      Operation = "start.project"
	OperationOpenProject       Operation = "open.project"
	OperationAttachProject     Operation = "attach.project"
	OperationFocusProject      Operation = "focus.project"
	OperationStopProject       Operation = "stop.project"
	OperationUnregisterProject Operation = "unregister.project"
	OperationRecreateProject   Operation = "recreate.project"
)

// ReceiptAction is the per-resource outcome recorded in AffectedUIDs. It is a
// closed vocabulary drawn from the same axes as the effect enums, so a reader
// does not have to learn a second set of words for the same events.
type ReceiptAction string

const (
	ActionCreated      ReceiptAction = "created"
	ActionReused       ReceiptAction = "reused"
	ActionRemoved      ReceiptAction = "removed"
	ActionReplaced     ReceiptAction = "replaced"
	ActionRenamed      ReceiptAction = "renamed"
	ActionMaterialized ReceiptAction = "materialized"
	ActionAlreadyLive  ReceiptAction = "already-live"
	ActionStopped      ReceiptAction = "stopped"
	ActionPreserved    ReceiptAction = "preserved"
	ActionUnchanged    ReceiptAction = "unchanged"
)

// ReceiptTarget is the primary resource the operation addressed.
type ReceiptTarget struct {
	Kind string `json:"kind"`
	UID  string `json:"uid"`
	Name string `json:"name"`
}

// ReceiptEffects is the actual seven-axis outcome, minus cardinality, which the
// receipt reports as counts rather than as a class.
type ReceiptEffects struct {
	Identity     IdentityEffect     `json:"identity"`
	Address      AddressEffect      `json:"address"`
	Topology     TopologyEffect     `json:"topology"`
	DesiredState DesiredStateEffect `json:"desiredState"`
	Runtime      RuntimeEffect      `json:"runtime"`
	Focus        FocusEffect        `json:"focus"`
}

// ReceiptCardinality is the per-kind count of resources the operation touched.
type ReceiptCardinality struct {
	Projects int `json:"projects"`
	Windows  int `json:"windows"`
	Panes    int `json:"panes"`
	Agents   int `json:"agents"`
}

// ReceiptResource is one resource the operation touched and what happened to it.
type ReceiptResource struct {
	Kind   string        `json:"kind"`
	UID    string        `json:"uid"`
	Name   string        `json:"name"`
	Action ReceiptAction `json:"action"`
}

// ReceiptDomainEffect is the typed extension seam for effects outside the
// Projmux resource graph. No current operation populates it; it exists so the
// downstream delivery verb can add `agent-delivery` without reinterpreting any
// of the seven resource axes.
type ReceiptDomainEffect struct {
	Kind  DomainEffectKind `json:"kind"`
	Ref   string           `json:"ref,omitempty"`
	State string           `json:"state,omitempty"`
}

// OperationReceipt is the one typed record the default human line and the
// `-o receipt` JSON projection both render.
//
// It is deliberately not a second result model: the human summary is a
// projection of this value, so a route cannot print one story and hand
// automation another.
type OperationReceipt struct {
	APIVersion            string               `json:"apiVersion"`
	Operation             Operation            `json:"operation"`
	Target                ReceiptTarget        `json:"target"`
	Effects               ReceiptEffects       `json:"effects"`
	Cardinality           ReceiptCardinality   `json:"cardinality"`
	SelectedWindowUIDs    []string             `json:"selectedWindowUIDs"`
	AffectedUIDs          []ReceiptResource    `json:"affectedUIDs"`
	CompatibilityWarnings []string             `json:"compatibilityWarnings"`
	DomainEffect          *ReceiptDomainEffect `json:"domainEffect"`
	// Profile is the named Agent profile a create applied, and the items of
	// it that were not applied. It is absent from every receipt of an
	// operation that applied no profile, so those stay byte-identical.
	Profile *ReceiptProfile `json:"profile,omitempty"`
}

// ReceiptProfile is the profile one Agent create applied: its name, the
// digest of the content applied, and every item that was not applied, in
// profile order. An applied item is not listed.
type ReceiptProfile struct {
	Name       string               `json:"name"`
	Digest     string               `json:"digest"`
	NotApplied []ReceiptProfileItem `json:"notApplied"`
}

// ReceiptProfileItem is one profile item that was not applied, the provider
// it was not applied on, and the stable reason token saying why.
type ReceiptProfileItem struct {
	Item     string `json:"item"`
	Provider string `json:"provider"`
	Reason   string `json:"reason"`
}

// HumanLines is the human disclosure of the applied profile: one line naming
// it, then one line per item that was not applied. It is empty for nil.
func (p *ReceiptProfile) HumanLines() []string {
	if p == nil {
		return nil
	}
	lines := []string{"profile name=" + p.Name + " digest=" + p.Digest}
	for _, item := range p.NotApplied {
		lines = append(lines, "profile-not-applied item="+item.Item+" provider="+item.Provider+" reason="+item.Reason)
	}
	return lines
}

// NewReceipt returns a receipt with the versioned envelope and the non-nil
// empty collections the JSON projection promises.
func NewReceipt(operation Operation, target ReceiptTarget, effects ReceiptEffects) OperationReceipt {
	return OperationReceipt{
		APIVersion:            ReceiptAPIVersion,
		Operation:             operation,
		Target:                target,
		Effects:               effects,
		SelectedWindowUIDs:    []string{},
		AffectedUIDs:          []ReceiptResource{},
		CompatibilityWarnings: []string{},
	}
}

// Add records one affected resource and counts it.
func (r *OperationReceipt) Add(kind, uid, name string, action ReceiptAction) {
	r.AffectedUIDs = append(r.AffectedUIDs, ReceiptResource{Kind: kind, UID: uid, Name: name, Action: action})
	switch strings.ToLower(kind) {
	case "project":
		r.Cardinality.Projects++
	case "window":
		r.Cardinality.Windows++
	case "pane":
		r.Cardinality.Panes++
	case "agent":
		r.Cardinality.Agents++
	}
}

// Warn records one compatibility warning without duplicating it.
func (r *OperationReceipt) Warn(warning string) {
	warning = strings.TrimSpace(warning)
	if warning == "" || slices.Contains(r.CompatibilityWarnings, warning) {
		return
	}
	r.CompatibilityWarnings = append(r.CompatibilityWarnings, warning)
}

// SelectWindows records the exact Window UIDs a fan-out planner chose.
func (r *OperationReceipt) SelectWindows(uids ...string) {
	for _, uid := range uids {
		uid = strings.TrimSpace(uid)
		if uid == "" || slices.Contains(r.SelectedWindowUIDs, uid) {
			continue
		}
		r.SelectedWindowUIDs = append(r.SelectedWindowUIDs, uid)
	}
}
