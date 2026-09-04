package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

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

// operationRoutes maps every operation onto the canonical route whose allowed
// effect record bounds it. This is the join that makes the manifest and the
// runtime receipt speak one vocabulary instead of two parallel ones.
//
// `recreate.project` maps onto `switch` because the identity replacement is a
// UI action of that shortcut rather than a canonical route of its own; the
// track deliberately did not add a `recreate project` command.
var operationRoutes = map[Operation]string{
	OperationCreateProject:     "create project",
	OperationCreateWindow:      "create window",
	OperationCreatePane:        "create pane",
	OperationCreateAgent:       "create agent",
	OperationRenameProject:     "rename project",
	OperationRenameWindow:      "rename window",
	OperationRenamePane:        "rename pane",
	OperationRenameAgent:       "rename agent",
	OperationDeleteWindow:      "delete window",
	OperationDeletePane:        "delete pane",
	OperationDeleteAgent:       "delete agent",
	OperationStartProject:      "start project",
	OperationOpenProject:       "open project",
	OperationAttachProject:     "attach project",
	OperationFocusProject:      "focus project",
	OperationStopProject:       "stop project",
	OperationUnregisterProject: "unregister project",
	OperationRecreateProject:   "switch",
}

// operations is the closed set in contract order.
var operations = []Operation{
	OperationCreateProject, OperationCreateWindow, OperationCreatePane, OperationCreateAgent,
	OperationRenameProject, OperationRenameWindow, OperationRenamePane, OperationRenameAgent,
	OperationDeleteWindow, OperationDeletePane, OperationDeleteAgent,
	OperationStartProject, OperationOpenProject, OperationAttachProject, OperationFocusProject,
	OperationStopProject, OperationUnregisterProject, OperationRecreateProject,
}

// RouteForOperation returns the canonical route spelling whose allowed effects
// bound this operation.
func RouteForOperation(operation Operation) (string, bool) {
	spelling, ok := operationRoutes[operation]
	return spelling, ok
}

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

var receiptActions = []ReceiptAction{
	ActionCreated, ActionReused, ActionRemoved, ActionReplaced, ActionRenamed,
	ActionMaterialized, ActionAlreadyLive, ActionStopped, ActionPreserved, ActionUnchanged,
}

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

// Validate proves the receipt is a member of the closed vocabularies and that
// its actual effect tuple is one the route manifest already allows.
//
// The second half is the point of the whole record. A route that starts
// materializing a runtime it never declared, or a compatibility alias that
// quietly acquires a destructive effect, fails here rather than in a reader's
// expectations.
func (r OperationReceipt) Validate() error {
	if r.APIVersion != ReceiptAPIVersion {
		return fmt.Errorf("operation receipt: apiVersion %q, want %q", r.APIVersion, ReceiptAPIVersion)
	}
	spelling, ok := RouteForOperation(r.Operation)
	if !ok {
		return fmt.Errorf("operation receipt: unknown operation %q", r.Operation)
	}
	if !slices.Contains(identityEffects, r.Effects.Identity) ||
		!slices.Contains(addressEffects, r.Effects.Address) ||
		!slices.Contains(topologyEffects, r.Effects.Topology) ||
		!slices.Contains(desiredEffects, r.Effects.DesiredState) ||
		!slices.Contains(runtimeEffects, r.Effects.Runtime) ||
		!slices.Contains(focusEffects, r.Effects.Focus) {
		return fmt.Errorf("operation receipt %s: effect tuple holds a value outside the closed enums: %+v", r.Operation, r.Effects)
	}
	for _, resource := range r.AffectedUIDs {
		if !slices.Contains(receiptActions, resource.Action) {
			return fmt.Errorf("operation receipt %s: unknown action %q on %s/%s", r.Operation, resource.Action, resource.Kind, resource.Name)
		}
	}
	if r.DomainEffect != nil && !slices.Contains(domainEffectKinds, r.DomainEffect.Kind) {
		return fmt.Errorf("operation receipt %s: unknown domain effect %q", r.Operation, r.DomainEffect.Kind)
	}
	allowed, ok := allowedEffectsFor(spelling)
	if !ok {
		return fmt.Errorf("operation receipt %s: route %q has no effect manifest row", r.Operation, spelling)
	}
	for _, mismatch := range []struct {
		axis string
		ok   bool
		got  string
	}{
		{"identity", slices.Contains(allowed.Identity, r.Effects.Identity), string(r.Effects.Identity)},
		{"address", slices.Contains(allowed.Address, r.Effects.Address), string(r.Effects.Address)},
		{"topology", slices.Contains(allowed.Topology, r.Effects.Topology), string(r.Effects.Topology)},
		{"desired-state", slices.Contains(allowed.DesiredState, r.Effects.DesiredState), string(r.Effects.DesiredState)},
		{"runtime", slices.Contains(allowed.Runtime, r.Effects.Runtime), string(r.Effects.Runtime)},
		{"focus", slices.Contains(allowed.Focus, r.Effects.Focus), string(r.Effects.Focus)},
	} {
		if !mismatch.ok {
			return fmt.Errorf("operation receipt %s: %s=%s is not allowed by route %q", r.Operation, mismatch.axis, mismatch.got, spelling)
		}
	}
	return nil
}

// allowedEffectsFor returns the manifest row of one canonical route spelling.
func allowedEffectsFor(spelling string) (AllowedEffects, bool) {
	for _, row := range EffectManifest() {
		if row.Route == spelling {
			return row.Effects, true
		}
	}
	return AllowedEffects{}, false
}

// EffectTuple renders the actual six mutation axes in manifest order. It is the
// shared spelling of the human line, so the words a result prints are the same
// words `--help` and the generated reference advertise.
func (r OperationReceipt) EffectTuple() string {
	return strings.Join([]string{
		"identity=" + string(r.Effects.Identity),
		"address=" + string(r.Effects.Address),
		"topology=" + string(r.Effects.Topology),
		"desired-state=" + string(r.Effects.DesiredState),
		"runtime=" + string(r.Effects.Runtime),
		"focus=" + string(r.Effects.Focus),
	}, " ")
}

// Counts renders the per-kind cardinality in resource-graph order.
func (r OperationReceipt) Counts() string {
	return fmt.Sprintf("projects=%d windows=%d panes=%d agents=%d",
		r.Cardinality.Projects, r.Cardinality.Windows, r.Cardinality.Panes, r.Cardinality.Agents)
}

// HumanLine is the default projection: one line naming the operation, the exact
// effect tuple, and the counts.
func (r OperationReceipt) HumanLine() string {
	line := "receipt operation=" + string(r.Operation) + " " + r.EffectTuple() + " " + r.Counts()
	if r.DomainEffect != nil {
		line += " domain-effect=" + string(r.DomainEffect.Kind)
	}
	return line
}

// WriteHuman writes the default human projection.
func (r OperationReceipt) WriteHuman(w io.Writer) error {
	_, err := fmt.Fprintln(w, r.HumanLine())
	return err
}

// WriteJSON writes the `-o receipt` projection: one indented JSON document
// followed by a newline.
func (r OperationReceipt) WriteJSON(w io.Writer) error {
	encoded, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encode operation receipt: %w", err)
	}
	if _, err := w.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return nil
}
