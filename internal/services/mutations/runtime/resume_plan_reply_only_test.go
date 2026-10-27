package runtime

import (
	"fmt"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestResumePlanPreservesRecordedReplyOnly(t *testing.T) {
	counts := map[coremetadata.Kind]int{}
	mutator := coremetadata.Mutator{
		Now: func() time.Time { return time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC) },
		NewUID: func(kind coremetadata.Kind) (string, error) {
			counts[kind]++
			return fmt.Sprintf("%s-%d", kind, counts[kind]), nil
		},
		DirExists: func(string) (bool, error) { return true, nil },
	}
	registry := coremetadata.NewRegistry()
	project, err := mutator.RegisterProject(&registry, coremetadata.RegisterProjectOptions{
		Root: "/fixture/resume-plan", Name: "resume-plan", OperationID: "register",
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := mutator.CreateAgent(&registry, project.Windows[0].Metadata.UID, coremetadata.CreateAgentOptions{
		Name: "reply-only", Provider: "claude", OperationID: "create",
	})
	if err != nil {
		t.Fatal(err)
	}
	agent.Status.Phase = coremetadata.PhaseOffline
	agent.Status.SessionRef = &coremetadata.AgentSessionRef{
		Provider: "claude", Claude: &coremetadata.ClaudeSessionRef{SessionID: "fixture-session"},
	}
	for _, recorded := range []bool{false, true} {
		t.Run(fmt.Sprint(recorded), func(t *testing.T) {
			agent.Metadata.Annotations = map[string]string{}
			if recorded {
				agent.Metadata.Annotations[coremetadata.AnnotationAgentDialogueReplyOnly] = coremetadata.DialogueReplyOnlyOn
			}
			plan, err := BuildAgentResumePlan("agent relaunch", registry, &agent)
			if err != nil {
				t.Fatal(err)
			}
			if plan.DialogueReplyOnly != recorded {
				t.Errorf("resume plan reply-only=%t, want recorded=%t", plan.DialogueReplyOnly, recorded)
			}
		})
	}
}
