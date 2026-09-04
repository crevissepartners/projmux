package cli

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// TestEveryOperationNamesAnExecutableRouteWithAnEffectRow is the bijection the
// receipt depends on.
//
// A receipt is only meaningful if the route it names still exists and still
// declares what it is allowed to do. An operation whose route was renamed or
// removed would produce a record nothing could validate, which is the failure
// this closes.
func TestEveryOperationNamesAnExecutableRouteWithAnEffectRow(t *testing.T) {
	t.Parallel()

	manifest := make(map[string]bool)
	for _, row := range EffectManifest() {
		manifest[row.Route] = true
	}
	for _, operation := range operations {
		spelling, ok := RouteForOperation(operation)
		if !ok {
			t.Errorf("operation %q maps to no route", operation)
			continue
		}
		if !manifest[spelling] {
			t.Errorf("operation %q names route %q, which has no effect manifest row", operation, spelling)
		}
		if _, _, resolved := Resolve(strings.Fields(spelling)); !resolved {
			t.Errorf("operation %q names route %q, which argv cannot reach", operation, spelling)
		}
	}
	if len(operationRoutes) != len(operations) {
		t.Fatalf("the operation route table has %d entries for %d operations", len(operationRoutes), len(operations))
	}
}

// TestReceiptValidateRejectsEffectsTheRouteDoesNotAllow is the guard that makes
// the manifest and the runtime result one contract rather than two.
func TestReceiptValidateRejectsEffectsTheRouteDoesNotAllow(t *testing.T) {
	t.Parallel()

	base := func() OperationReceipt {
		receipt := NewReceipt(OperationFocusProject,
			ReceiptTarget{Kind: "Project", UID: "prj-a", Name: "alpha"},
			ReceiptEffects{
				Identity: IdentityUnchanged, Address: AddressUnchanged, Topology: TopologyUnchanged,
				DesiredState: DesiredStateUnchanged, Runtime: RuntimeUnchanged, Focus: FocusMovedCurrentClient,
			})
		return receipt
	}
	if err := base().Validate(); err != nil {
		t.Fatalf("the honest focus receipt did not validate: %v", err)
	}

	for _, test := range []struct {
		name string
		edit func(*OperationReceipt)
		want string
	}{
		{"focus never materializes", func(r *OperationReceipt) { r.Effects.Runtime = RuntimeMaterialized }, "runtime=materialized is not allowed"},
		{"focus never replaces identity", func(r *OperationReceipt) { r.Effects.Identity = IdentityReplaced }, "identity=replaced is not allowed"},
		{"unknown enum", func(r *OperationReceipt) { r.Effects.Address = "teleported" }, "outside the closed enums"},
		{"unknown operation", func(r *OperationReceipt) { r.Operation = "detonate.project" }, "unknown operation"},
		{"unknown action", func(r *OperationReceipt) { r.Add("Project", "prj-a", "alpha", "vaporized") }, "unknown action"},
		{"unknown domain effect", func(r *OperationReceipt) { r.DomainEffect = &ReceiptDomainEffect{Kind: "provider-write"} }, "unknown domain effect"},
		{"wrong envelope", func(r *OperationReceipt) { r.APIVersion = "OperationReceipt/v2" }, "apiVersion"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			receipt := base()
			test.edit(&receipt)
			err := receipt.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want an error containing %q", err, test.want)
			}
		})
	}
}

// TestProjectLifecycleReceiptsMatchTheDecidedEffectMatrix pins the tuple of
// every Project lifecycle outcome against the row the design fixed.
//
// This is the table an operator reads the verbs by: open materializes and
// moves, focus only moves, start only materializes, stop only ends, unregister
// removes the Registry graph and leaves the runtime alone. Any two of those
// becoming the same tuple is the defect.
func TestProjectLifecycleReceiptsMatchTheDecidedEffectMatrix(t *testing.T) {
	t.Parallel()

	rows := []struct {
		operation Operation
		effects   ReceiptEffects
	}{
		{OperationStartProject, ReceiptEffects{IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeMaterialized, FocusUnchanged}},
		{OperationOpenProject, ReceiptEffects{IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeMaterialized, FocusMovedCurrentClient}},
		{OperationAttachProject, ReceiptEffects{IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeMaterialized, FocusAttachedCaller}},
		{OperationFocusProject, ReceiptEffects{IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusMovedCurrentClient}},
		{OperationStopProject, ReceiptEffects{IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeStopped, FocusUnchanged}},
		{OperationUnregisterProject, ReceiptEffects{IdentityRemoved, AddressReleased, TopologyRemoved, DesiredStateRemoved, RuntimePreserved, FocusUnchanged}},
		{OperationRecreateProject, ReceiptEffects{IdentityReplaced, AddressReleased, TopologyReplaced, DesiredStateReplaced, RuntimeMaterialized, FocusMovedCurrentClient}},
		{OperationCreateProject, ReceiptEffects{IdentityCreated, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeUnchanged, FocusUnchanged}},
	}
	seen := make(map[ReceiptEffects]Operation, len(rows))
	for _, row := range rows {
		receipt := NewReceipt(row.operation, ReceiptTarget{Kind: "Project", UID: "prj-a", Name: "alpha"}, row.effects)
		if err := receipt.Validate(); err != nil {
			t.Errorf("%s: %v", row.operation, err)
		}
		if prior, ok := seen[row.effects]; ok {
			t.Errorf("%s and %s declare the same effect tuple; the verbs would be indistinguishable", prior, row.operation)
		}
		seen[row.effects] = row.operation
	}

	// The two the design separates most sharply, stated directly.
	if rows[0].effects.Focus == rows[1].effects.Focus {
		t.Fatal("start project and open project agree on focus")
	}
	if rows[3].effects.Runtime == rows[1].effects.Runtime {
		t.Fatal("focus project and open project agree on runtime")
	}
	if rows[5].effects.Runtime != RuntimePreserved {
		t.Fatal("unregister project stopped preserving the runtime")
	}
}

// TestReceiptJSONProjectionIsStableAndCollectionsAreNeverNull pins the wire
// shape a consumer parses.
func TestReceiptJSONProjectionIsStableAndCollectionsAreNeverNull(t *testing.T) {
	t.Parallel()

	receipt := NewReceipt(OperationCreateProject,
		ReceiptTarget{Kind: "Project", UID: "prj-a", Name: "alpha"},
		ReceiptEffects{IdentityCreated, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeUnchanged, FocusUnchanged})
	receipt.Add("Project", "prj-a", "alpha", ActionCreated)
	receipt.Add("Window", "win-a", "main", ActionCreated)
	receipt.Add("Pane", "pan-a", "shell", ActionCreated)
	receipt.SelectWindows("win-a", "win-a", "")
	receipt.Warn("a warning")
	receipt.Warn("a warning")

	if receipt.Cardinality != (ReceiptCardinality{Projects: 1, Windows: 1, Panes: 1}) {
		t.Fatalf("cardinality = %+v", receipt.Cardinality)
	}
	if !slices.Equal(receipt.SelectedWindowUIDs, []string{"win-a"}) {
		t.Fatalf("selected windows = %v, want the deduplicated non-empty set", receipt.SelectedWindowUIDs)
	}
	if len(receipt.CompatibilityWarnings) != 1 {
		t.Fatalf("warnings = %v, want one deduplicated entry", receipt.CompatibilityWarnings)
	}

	var buf bytes.Buffer
	if err := receipt.WriteJSON(&buf); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("decode %q: %v", buf.String(), err)
	}
	for _, key := range []string{
		"apiVersion", "operation", "target", "effects", "cardinality",
		"selectedWindowUIDs", "affectedUIDs", "compatibilityWarnings", "domainEffect",
	} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("receipt JSON is missing %q: %s", key, buf.String())
		}
	}
	if decoded["domainEffect"] != nil {
		t.Fatalf("domainEffect = %v, want an explicit null", decoded["domainEffect"])
	}
	effects, _ := decoded["effects"].(map[string]any)
	if effects["desiredState"] != "created" {
		t.Fatalf("effects.desiredState = %v", effects["desiredState"])
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte("}\n")) {
		t.Fatalf("receipt JSON does not end in one newline: %q", buf.String())
	}
}

// TestReceiptHumanLineUsesTheSameVocabularyAsHelp keeps the result and the
// advertised contract spelled identically, so an operator can compare what a
// route said it might do with what it did without translating.
func TestReceiptHumanLineUsesTheSameVocabularyAsHelp(t *testing.T) {
	t.Parallel()

	receipt := NewReceipt(OperationOpenProject,
		ReceiptTarget{Kind: "Project", UID: "prj-a", Name: "alpha"},
		ReceiptEffects{IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeMaterialized, FocusMovedCurrentClient})
	receipt.Add("Project", "prj-a", "alpha", ActionMaterialized)

	line := receipt.HumanLine()
	for _, want := range []string{
		"receipt operation=open.project",
		"identity=unchanged", "address=unchanged", "topology=unchanged",
		"desired-state=unchanged", "runtime=materialized", "focus=moved-current-client",
		"projects=1 windows=0 panes=0 agents=0",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("human line %q is missing %q", line, want)
		}
	}

	route, ok := LookupRoute("open")
	if !ok {
		t.Fatal("open route missing")
	}
	child, ok := findChild(route, "project")
	if !ok {
		t.Fatal("open project route missing")
	}
	for _, axis := range effectProjection(child.Effects) {
		name, values, _ := strings.Cut(axis, "=")
		if name == "cardinality" || name == "domain-effect" {
			continue
		}
		if !strings.Contains(line, name+"=") {
			t.Errorf("the receipt omits the %q axis the route advertises (%s)", name, values)
		}
	}
}
