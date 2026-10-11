package cli

import (
	"encoding/json"
	"fmt"
	core "github.com/crevissepartners/projmux/internal/core/receipt"
	"io"
	"slices"
	"strings"
)

type Operation = core.Operation
type ReceiptAction = core.ReceiptAction
type ReceiptTarget = core.ReceiptTarget
type ReceiptEffects = core.ReceiptEffects
type ReceiptCardinality = core.ReceiptCardinality
type ReceiptResource = core.ReceiptResource
type ReceiptDomainEffect = core.ReceiptDomainEffect
type OperationReceipt core.OperationReceipt
type ReceiptProfile = core.ReceiptProfile
type ReceiptProfileItem = core.ReceiptProfileItem

const OperationCreateProject = core.OperationCreateProject
const OperationCreateWindow = core.OperationCreateWindow
const OperationCreatePane = core.OperationCreatePane
const OperationCreateAgent = core.OperationCreateAgent
const OperationResumeAgent = core.OperationResumeAgent
const OperationRenameProject = core.OperationRenameProject
const OperationRenameWindow = core.OperationRenameWindow
const OperationRenamePane = core.OperationRenamePane
const OperationRenameAgent = core.OperationRenameAgent
const OperationDeleteWindow = core.OperationDeleteWindow
const OperationDeletePane = core.OperationDeletePane
const OperationDeleteAgent = core.OperationDeleteAgent
const OperationStartProject = core.OperationStartProject
const OperationOpenProject = core.OperationOpenProject
const OperationAttachProject = core.OperationAttachProject
const OperationFocusProject = core.OperationFocusProject
const OperationStopProject = core.OperationStopProject
const OperationUnregisterProject = core.OperationUnregisterProject
const OperationRecreateProject = core.OperationRecreateProject
const ActionCreated = core.ActionCreated
const ActionReused = core.ActionReused
const ActionRemoved = core.ActionRemoved
const ActionReplaced = core.ActionReplaced
const ActionRenamed = core.ActionRenamed
const ActionMaterialized = core.ActionMaterialized
const ActionAlreadyLive = core.ActionAlreadyLive
const ActionStopped = core.ActionStopped
const ActionPreserved = core.ActionPreserved
const ActionUnchanged = core.ActionUnchanged
const ReceiptAPIVersion = core.ReceiptAPIVersion

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
	OperationResumeAgent:       "agent resume",
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
	OperationCreateProject, OperationCreateWindow, OperationCreatePane, OperationCreateAgent, OperationResumeAgent,
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

var receiptActions = []ReceiptAction{
	ActionCreated, ActionReused, ActionRemoved, ActionReplaced, ActionRenamed,
	ActionMaterialized, ActionAlreadyLive, ActionStopped, ActionPreserved, ActionUnchanged,
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

func NewReceipt(op Operation, target ReceiptTarget, effects ReceiptEffects) OperationReceipt {
	return OperationReceipt(core.NewReceipt(op, target, effects))
}
func (r *OperationReceipt) Add(kind, uid, name string, action ReceiptAction) {
	(*core.OperationReceipt)(r).Add(kind, uid, name, action)
}
func (r *OperationReceipt) Warn(warning string) { (*core.OperationReceipt)(r).Warn(warning) }
func (r *OperationReceipt) SelectWindows(uids ...string) {
	(*core.OperationReceipt)(r).SelectWindows(uids...)
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

// WriteHuman writes the default human projection: the receipt line, then the
// profile disclosure when the operation applied a profile.
func (r OperationReceipt) WriteHuman(w io.Writer) error {
	if _, err := fmt.Fprintln(w, r.HumanLine()); err != nil {
		return err
	}
	for _, line := range r.Profile.HumanLines() {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	return nil
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
