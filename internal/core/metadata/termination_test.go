package metadata

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// terminationFixture builds a registry holding one shell Pane and one Agent
// with its managed Pane, both already materialized once.
func terminationFixture(t *testing.T) (Mutator, *Registry, string, string, string) {
	t.Helper()
	m := testMutator(dirSet{"/srv/alpha": true})
	reg := NewRegistry()
	result, err := registerFixture(m, &reg, "/srv/alpha")
	if err != nil {
		t.Fatalf("register fixture: %v", err)
	}
	shellPane := result.Panes[0].Metadata.UID
	agent, err := m.CreateAgent(&reg, result.Windows[0].Metadata.UID, CreateAgentOptions{
		Provider: "codex", OperationID: "op-fixture",
	})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	agentPane, err := m.AttachAgentPane(&reg, agent.Metadata.UID, BootstrapPane{CWD: "/srv/alpha"}, "op-fixture")
	if err != nil {
		t.Fatalf("attach agent pane: %v", err)
	}
	if _, err := m.RecordPaneActivation(&reg, shellPane, PaneActivationOptions{Generation: "gen-shell-1"}); err != nil {
		t.Fatalf("activate shell pane: %v", err)
	}
	if _, err := m.RecordPaneActivation(&reg, agentPane.Metadata.UID, PaneActivationOptions{
		Generation: "gen-agent-1", AgentUID: agent.Metadata.UID,
	}); err != nil {
		t.Fatalf("activate agent pane: %v", err)
	}
	return m, &reg, shellPane, agent.Metadata.UID, agentPane.Metadata.UID
}

func exitCode(code int) *int { return &code }

// TestClassifyProcessExitNeverPromotesExitZeroToIntent is acceptance criterion
// 4: a clean exit is evidence of a normal exit and nothing more.
func TestClassifyProcessExitNeverPromotesExitZeroToIntent(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		code   int
		signal string
		want   TerminationClassification
	}{
		{name: "clean exit is normal, not intentional", code: 0, want: TerminationNormal},
		{name: "non-zero exit is abnormal", code: 3, want: TerminationAbnormal},
		{name: "signal is abnormal", code: 0, signal: "TERM", want: TerminationAbnormal},
		{name: "signal wins over a reported code", code: 0, signal: "KILL", want: TerminationAbnormal},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := ClassifyProcessExit(test.code, test.signal); got != test.want {
				t.Fatalf("ClassifyProcessExit(%d, %q) = %q, want %q", test.code, test.signal, got, test.want)
			}
		})
	}
}

// TestSupervisorReceiptRequiresTheCurrentGeneration is acceptance criterion 3.
func TestSupervisorReceiptRequiresTheCurrentGeneration(t *testing.T) {
	t.Parallel()

	m, reg, shellPane, _, _ := terminationFixture(t)
	current, err := m.RecordTermination(reg, TerminationEvidence{
		Source: TerminationSourceSupervisor, Classification: TerminationNormal,
		PaneUID: shellPane, Generation: "gen-shell-1", ExitCode: exitCode(0),
	})
	if err != nil || !current.Applied {
		t.Fatalf("current-generation receipt = %#v, err=%v", current, err)
	}

	// A resume replaces the generation. The receipt the previous process is
	// still holding must not touch the new binding.
	if _, err := m.RecordPaneActivation(reg, shellPane, PaneActivationOptions{Generation: "gen-shell-2"}); err != nil {
		t.Fatalf("resume activation: %v", err)
	}
	pane, _ := reg.Pane(shellPane)
	if pane.Status.LastTermination != nil {
		t.Fatalf("a new generation kept the previous receipt: %#v", pane.Status.LastTermination)
	}
	late, err := m.RecordTermination(reg, TerminationEvidence{
		Source: TerminationSourceSupervisor, Classification: TerminationAbnormal,
		PaneUID: shellPane, Generation: "gen-shell-1", Signal: "HUP",
	})
	if err != nil {
		t.Fatalf("late receipt error = %v", err)
	}
	if late.Applied || !late.Stale || !strings.Contains(late.Reason, "gen-shell-1") {
		t.Fatalf("late receipt = %#v, want a stale no-op naming the replaced generation", late)
	}
	pane, _ = reg.Pane(shellPane)
	if pane.Status.LastTermination != nil {
		t.Fatalf("a stale receipt mutated the current binding: %#v", pane.Status.LastTermination)
	}
}

// TestDuplicateReceiptIsAByteIdenticalNoOp is the other half of criterion 3.
func TestDuplicateReceiptIsAByteIdenticalNoOp(t *testing.T) {
	t.Parallel()

	m, reg, shellPane, _, _ := terminationFixture(t)
	receipt := TerminationEvidence{
		Source: TerminationSourceSupervisor, Classification: TerminationAbnormal,
		PaneUID: shellPane, Generation: "gen-shell-1", ExitCode: exitCode(2),
	}
	if outcome, err := m.RecordTermination(reg, receipt); err != nil || !outcome.Applied {
		t.Fatalf("first receipt = %#v, err=%v", outcome, err)
	}
	before, err := json.Marshal(reg.Normalize())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	outcome, err := m.RecordTermination(reg, receipt)
	if err != nil {
		t.Fatalf("duplicate receipt error = %v", err)
	}
	if outcome.Applied || !outcome.Duplicate {
		t.Fatalf("duplicate receipt = %#v, want a no-op", outcome)
	}
	after, err := json.Marshal(reg.Normalize())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("a duplicate receipt rewrote the registry:\nbefore=%s\nafter=%s", before, after)
	}
}

// TestReceiptForTheWrongOwnerChangesNothing covers the wrong-Agent, wrong-Pane,
// and released-binding guards.
func TestReceiptForTheWrongOwnerChangesNothing(t *testing.T) {
	t.Parallel()

	m, reg, shellPane, agentUID, agentPane := terminationFixture(t)
	for _, test := range []struct {
		name    string
		receipt TerminationEvidence
		want    string
	}{
		{
			name: "a shell Pane claimed by an Agent",
			receipt: TerminationEvidence{
				Source: TerminationSourceSupervisor, Classification: TerminationNormal,
				PaneUID: shellPane, AgentUID: agentUID, Generation: "gen-shell-1", ExitCode: exitCode(0),
			},
			want: "not owned by agent",
		},
		{
			name: "a Pane that is not in the registry",
			receipt: TerminationEvidence{
				Source: TerminationSourceSupervisor, Classification: TerminationNormal,
				PaneUID: "pane-gone", Generation: "gen-shell-1", ExitCode: exitCode(0),
			},
			want: "not in the registry",
		},
		{
			name: "an Agent that no longer binds the Pane",
			receipt: TerminationEvidence{
				Source: TerminationSourceSupervisor, Classification: TerminationNormal,
				PaneUID: agentPane, AgentUID: "agent-other", Generation: "gen-agent-1", ExitCode: exitCode(0),
			},
			want: "not owned by agent",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, err := json.Marshal(reg.Normalize())
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			outcome, err := m.RecordTermination(reg, test.receipt)
			if err != nil {
				t.Fatalf("receipt error = %v", err)
			}
			if outcome.Applied || !outcome.Stale || !strings.Contains(outcome.Reason, test.want) {
				t.Fatalf("outcome = %#v, want a stale no-op mentioning %q", outcome, test.want)
			}
			after, err := json.Marshal(reg.Normalize())
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(before) != string(after) {
				t.Fatalf("a stale receipt mutated the registry")
			}
		})
	}
}

// TestOnlyAControlActionMayRecordIntent proves the supervisor cannot promote
// what it observed into a statement about what anyone wanted.
func TestOnlyAControlActionMayRecordIntent(t *testing.T) {
	t.Parallel()

	m, reg, shellPane, _, _ := terminationFixture(t)
	_, err := m.RecordTermination(reg, TerminationEvidence{
		Source: TerminationSourceSupervisor, Classification: TerminationIntentional,
		PaneUID: shellPane, Generation: "gen-shell-1",
	})
	if err == nil || !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("supervisor-claimed intent error = %v, want ErrInvalidRegistry", err)
	}
	if pane, _ := reg.Pane(shellPane); pane.Status.LastTermination != nil {
		t.Fatalf("a refused receipt was stored: %#v", pane.Status.LastTermination)
	}
}

// TestRecordedIntentSurvivesTheObservedSignalItCauses is why the sticky rule
// exists: a canonical delete records intent and then kills the pane, and the
// supervisor watching that pane reports the resulting signal.
func TestRecordedIntentSurvivesTheObservedSignalItCauses(t *testing.T) {
	t.Parallel()

	m, reg, _, agentUID, agentPane := terminationFixture(t)
	if outcome, err := m.RecordTermination(reg, TerminationEvidence{
		Source: TerminationSourceControlAction, Classification: TerminationIntentional,
		PaneUID: agentPane, AgentUID: agentUID, Generation: "gen-agent-1", OperationID: "op-delete",
	}); err != nil || !outcome.Applied {
		t.Fatalf("intent receipt = %#v, err=%v", outcome, err)
	}
	// The Agent carries the same evidence, so it survives the Pane resource a
	// canonical delete removes.
	agent, _ := reg.Agent(agentUID)
	if agent.Status.LastTermination == nil || agent.Status.LastTermination.Classification != TerminationIntentional {
		t.Fatalf("Agent evidence = %#v", agent.Status.LastTermination)
	}

	outcome, err := m.RecordTermination(reg, TerminationEvidence{
		Source: TerminationSourceSupervisor, Classification: TerminationAbnormal,
		PaneUID: agentPane, AgentUID: agentUID, Generation: "gen-agent-1", Signal: "HUP",
	})
	if err != nil {
		t.Fatalf("observed receipt error = %v", err)
	}
	if outcome.Applied {
		t.Fatalf("the signal a deliberate delete caused overwrote the recorded intent")
	}
	pane, _ := reg.Pane(agentPane)
	if pane.Status.LastTermination.Classification != TerminationIntentional {
		t.Fatalf("stored classification = %q, want intentional", pane.Status.LastTermination.Classification)
	}
}

// TestClearTerminationOnlyWithdrawsItsOwnOperation guards the compensating
// write a refused control action performs.
func TestClearTerminationOnlyWithdrawsItsOwnOperation(t *testing.T) {
	t.Parallel()

	m, reg, shellPane, _, _ := terminationFixture(t)
	if _, err := m.RecordTermination(reg, TerminationEvidence{
		Source: TerminationSourceControlAction, Classification: TerminationIntentional,
		PaneUID: shellPane, Generation: "gen-shell-1", OperationID: "op-mine",
	}); err != nil {
		t.Fatalf("intent receipt: %v", err)
	}
	cleared, err := m.ClearTermination(reg, shellPane, "op-someone-else")
	if err != nil {
		t.Fatalf("foreign withdrawal error = %v", err)
	}
	if cleared {
		t.Fatal("a withdrawal removed another operation's receipt")
	}
	if pane, _ := reg.Pane(shellPane); pane.Status.LastTermination == nil {
		t.Fatal("a foreign withdrawal erased the receipt")
	}
	cleared, err = m.ClearTermination(reg, shellPane, "op-mine")
	if err != nil || !cleared {
		t.Fatalf("own withdrawal cleared=%t err=%v", cleared, err)
	}
	if pane, _ := reg.Pane(shellPane); pane.Status.LastTermination != nil {
		t.Fatal("the withdrawal left the receipt behind")
	}
	if _, err := m.ClearTermination(reg, shellPane, "  "); err == nil {
		t.Fatal("an unscoped withdrawal was accepted")
	}
}

// TestTerminationFieldsAreAbsentFromAnOlderRegistryDocument is the read
// compatibility contract: a registry written before this Phase decodes,
// re-encodes byte-identically, and validates.
func TestTerminationFieldsAreAbsentFromAnOlderRegistryDocument(t *testing.T) {
	t.Parallel()

	m, reg, shellPane, _, _ := terminationFixture(t)
	// Strip the additive blocks the way an older writer would have.
	for i := range reg.Panes {
		reg.Panes[i].Status.Activation = PaneActivation{}
		reg.Panes[i].Status.LastTermination = nil
	}
	for i := range reg.Agents {
		reg.Agents[i].Status.LastTermination = nil
	}
	encoded, err := json.Marshal(reg.Normalize())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"activation\":{\"generation", "lastTermination"} {
		if strings.Contains(string(encoded), key) {
			t.Fatalf("an untouched registry re-encoded %q:\n%s", key, encoded)
		}
	}
	var decoded Registry
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("an older document failed validation: %v", err)
	}
	again, err := json.Marshal(decoded.Normalize())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != string(again) {
		t.Fatalf("an older document did not round-trip:\nfirst=%s\nsecond=%s", encoded, again)
	}
	// A receipt against a Pane that never got a generation is refused rather
	// than applied to whatever the pane holds now.
	outcome, err := m.RecordTermination(&decoded, TerminationEvidence{
		Source: TerminationSourceSupervisor, Classification: TerminationNormal,
		PaneUID: shellPane, Generation: "gen-shell-1", ExitCode: exitCode(0),
	})
	if err != nil || outcome.Applied || !outcome.Stale {
		t.Fatalf("receipt against an ungenerated Pane = %#v, err=%v", outcome, err)
	}
}

// TestNewGenerationIsOpaqueAndUnique keeps the generation from becoming a
// derivable value.
func TestNewGenerationIsOpaqueAndUnique(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for range 64 {
		generation, err := NewGeneration()
		if err != nil {
			t.Fatalf("NewGeneration: %v", err)
		}
		if !strings.HasPrefix(generation, "gen-") || len(generation) < 20 {
			t.Fatalf("generation %q is not an opaque prefixed value", generation)
		}
		if seen[generation] {
			t.Fatalf("generation %q was minted twice", generation)
		}
		seen[generation] = true
	}
}

// TestActivationRuntimeHandleIsGenerationGuarded keeps a diagnostic handle from
// being written for a materialization the registry has already replaced.
func TestActivationRuntimeHandleIsGenerationGuarded(t *testing.T) {
	t.Parallel()

	m, reg, shellPane, _, _ := terminationFixture(t)
	changed, err := m.ObservePaneActivationRuntime(reg, shellPane, "gen-shell-1", "%42")
	if err != nil || !changed {
		t.Fatalf("current-generation handle changed=%t err=%v", changed, err)
	}
	changed, err = m.ObservePaneActivationRuntime(reg, shellPane, "gen-shell-0", "%99")
	if err != nil || changed {
		t.Fatalf("replaced-generation handle changed=%t err=%v", changed, err)
	}
	pane, _ := reg.Pane(shellPane)
	if pane.Status.Activation.RuntimeID != "%42" {
		t.Fatalf("runtime handle = %q, want the current generation's", pane.Status.Activation.RuntimeID)
	}
}
