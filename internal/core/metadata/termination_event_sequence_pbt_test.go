package metadata

import (
	"encoding/json"
	"testing"
)

// FuzzTerminationEventSequencePreservesCanonicalDeleteAndHookRows is the named
// Phase 6 event-sequence property. Observed termination may change evidence and
// status but never owns row deletion; canonical delete removes the exact Pane,
// and every later duplicate/stale event remains a byte no-op.
func FuzzTerminationEventSequencePreservesCanonicalDeleteAndHookRows(f *testing.F) {
	f.Add([]byte{0, 1, 2})
	f.Add([]byte{1, 0, 3, 0, 2})
	f.Add([]byte{4, 2, 1, 3, 4})

	f.Fuzz(func(t *testing.T, events []byte) {
		mutator, registry, _, agentUID, paneUID := terminationFixture(t)
		deleted := false
		for index, event := range events {
			generation := "gen-agent-1"
			switch event % 5 {
			case 0: // clean voluntary exit evidence
				code := 0
				_, _ = mutator.RecordTermination(registry, TerminationEvidence{
					Source: TerminationSourceSupervisor, Classification: TerminationNormal,
					PaneUID: paneUID, AgentUID: agentUID, Generation: generation, ExitCode: &code,
				})
				_, _ = mutator.ProjectTermination(registry, TerminationProjectionInput{PaneUID: paneUID})
			case 1: // external kill evidence
				_, _ = mutator.RecordTermination(registry, TerminationEvidence{
					Source: TerminationSourceSupervisor, Classification: TerminationKilled,
					PaneUID: paneUID, AgentUID: agentUID, Generation: generation, Signal: "HUP",
				})
				_, _ = mutator.ProjectTermination(registry, TerminationProjectionInput{PaneUID: paneUID})
			case 2: // evidence-free hook projection
				_, _ = mutator.ProjectTermination(registry, TerminationProjectionInput{PaneUID: paneUID})
			case 3: // canonical delete authority
				if !deleted {
					_, _ = mutator.RecordTermination(registry, TerminationEvidence{
						Source: TerminationSourceControlAction, Classification: TerminationIntentional,
						PaneUID: paneUID, AgentUID: agentUID, Generation: generation, OperationID: "op-delete",
					})
					if err := mutator.DeletePane(registry, paneUID); err != nil {
						t.Fatalf("event %d canonical delete: %v", index, err)
					}
					deleted = true
				}
			case 4: // a replaced/stale journal row
				_, _ = mutator.RecordTermination(registry, TerminationEvidence{
					Source: TerminationSourceSupervisor, Classification: TerminationKilled,
					PaneUID: paneUID, AgentUID: agentUID, Generation: "gen-stale", Signal: "HUP",
				})
			}

			if err := registry.Validate(); err != nil {
				t.Fatalf("event %d left invalid Registry: %v", index, err)
			}
			_, exists := registry.Pane(paneUID)
			if exists == deleted {
				t.Fatalf("event %d Pane exists=%t deleted=%t", index, exists, deleted)
			}
			if deleted {
				before, err := json.Marshal(registry.Normalize())
				if err != nil {
					t.Fatalf("marshal before repeat: %v", err)
				}
				_ = mutator.DeletePane(registry, paneUID)
				_, _ = mutator.ProjectTermination(registry, TerminationProjectionInput{PaneUID: paneUID})
				after, err := json.Marshal(registry.Normalize())
				if err != nil {
					t.Fatalf("marshal after repeat: %v", err)
				}
				if string(before) != string(after) {
					t.Fatalf("event %d changed Registry after canonical delete", index)
				}
			}
		}
	})
}
