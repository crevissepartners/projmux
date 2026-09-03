package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

func TestCodexUpgradeCLIRequiresExplicitRequestPathOrOperationRef(t *testing.T) {
	command := &codexUpgradeCommand{}
	for _, args := range [][]string{
		{"plan"}, {"plan", "--request", "relative.json"}, {"apply"},
		{"resume"}, {"resume", "--operation", ""}, {"abort"},
	} {
		if err := command.Run(args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !IsUsageError(err) {
			t.Fatalf("argv %q error = %v, want usage refusal before coordinator", args, err)
		}
	}
}

func TestPrivateRollingAdmissionReceiptRendersAllSevenPhase5EffectsAtZero(t *testing.T) {
	op, err := codexgeneration.NewRollingUpgradeOperation("upgrade-one", "domain-one", "generation-old", "generation-new")
	if err != nil {
		t.Fatal(err)
	}
	journal := codexupgrade.Journal{
		Version: codexupgrade.JournalVersion, StateDomainID: "domain-one", CurrentGenerationID: "generation-old",
		Routes: []codexupgrade.GenerationRoute{{Generation: codexgeneration.Generation{
			Endpoint: metadata.CodexEndpointRef{StateDomainID: "domain-one", EndpointGenerationID: "generation-old"},
			State:    codexgeneration.StateCurrent, Owner: codexgeneration.OwnerProjmuxPrivate, BundleID: "bundle-old",
		}}},
		Operation: &op,
	}
	body, err := json.Marshal(contentFreeUpgradeReceipt(journal))
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(body)
	for _, field := range []string{"oldEndpointStop", "successorResume", "endpointRefCAS", "paneRelaunch", "retirement", "leaseRelease", "foreignAdoption"} {
		if !strings.Contains(encoded, `"`+field+`":0`) {
			t.Fatalf("private rolling receipt omitted zero %s: %s", field, encoded)
		}
	}
	for _, secret := range []string{"socketPath", "privateRoot", "leaseRoot", "tuiPath", "executablePath"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("content-free receipt exposed %s: %s", secret, encoded)
		}
	}
}

func TestCodexHandoverCLIRequiresExplicitRequestPathOrOperationRef(t *testing.T) {
	command := &codexHandoverCommand{}
	for _, args := range [][]string{
		{"plan"}, {"plan", "--request", "relative.json"}, {"apply"},
		{"resume"}, {"resume", "--operation", ""}, {"abort"},
	} {
		if err := command.Run(args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !IsUsageError(err) {
			t.Fatalf("argv %q error = %v, want usage refusal before coordinator", args, err)
		}
	}
}

func TestGenerationHandoverReceiptIsContentFreeAndKeepsZeroFields(t *testing.T) {
	op, err := codexgeneration.NewHandoverOperation("handover-one", "upgrade-one", "domain-one", "generation-old", "generation-new",
		codexgeneration.OwnerProjmuxPrivate, []codexgeneration.HandoverTarget{{AgentUID: "agent-one", PaneUID: "pane-one",
			PaneRuntimeID: "%21", PaneGeneration: "pane-generation-one", RelaunchGeneration: "handover-generation-one", ThreadID: "thread-one"}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(contentFreeHandoverReceipt(codexupgrade.Journal{Handover: &op}))
	if err != nil {
		t.Fatal(err)
	}
	encoded := string(body)
	for _, field := range []string{"noTurnChoice", "admissionFence", "bindingFence", "successorAbsence", "oldEndpointStop", "successorResume", "successorSnapshot", "endpointRefCAS", "paneRelaunch", "retirement", "leaseRelease", "foreignLifecycle"} {
		if !strings.Contains(encoded, `"`+field+`":0`) {
			t.Fatalf("handover receipt omitted zero %s: %s", field, encoded)
		}
	}
	for _, forbidden := range []string{"prompt", "content", "approval", "response", "privateRoot", "socketPath", "leaseRoot", "workspace", "cwd"} {
		if strings.Contains(strings.ToLower(encoded), strings.ToLower(forbidden)) {
			t.Fatalf("content-free receipt exposed %s: %s", forbidden, encoded)
		}
	}
}
