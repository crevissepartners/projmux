package metadata

import (
	"errors"
	"maps"
	"testing"
	"time"
)

func personaAnnotationFixture() Registry {
	reg := NewRegistry()
	reg.Agents = []Agent{{
		APIVersion: APIVersion, Kind: KindAgent,
		Metadata: ObjectMeta{UID: "agent-1", Name: "reviewer", Annotations: map[string]string{
			AnnotationAgentTopic: "review the parser",
		}},
		Spec:   AgentSpec{Provider: "claude"},
		Status: AgentStatus{Phase: PhaseRunning, PaneRef: "pane-1"},
	}}
	return reg
}

// TestSetAgentPersonaWritesTheThreeKeysTogetherAndKeepsOtherAnnotations pins
// the one mutation `agent persona attach|detach` makes: persona, digest, and
// the sticky system prompt snapshot mode move together, and nothing else on
// the Agent changes.
func TestSetAgentPersonaWritesTheThreeKeysTogetherAndKeepsOtherAnnotations(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 19, 3, 0, 0, 0, time.UTC)
	mutator := Mutator{Now: func() time.Time { return now }}
	reg := personaAnnotationFixture()
	const digest = "sha256:0000000000000000000000000000000000000000000000000000000000000001"

	attached, err := mutator.SetAgentPersona(&reg, "agent-1", AgentPersonaAnnotations{
		Persona: "go-reviewer", PersonaDigest: digest, SystemPromptSnapshot: SystemPromptSnapshotOff,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		AnnotationAgentTopic:                "review the parser",
		AnnotationAgentPersona:              "go-reviewer",
		AnnotationAgentPersonaDigest:        digest,
		AnnotationAgentSystemPromptSnapshot: SystemPromptSnapshotOff,
	}
	stored, _ := reg.Agent("agent-1")
	if !maps.Equal(stored.Metadata.Annotations, want) || !maps.Equal(attached.Metadata.Annotations, want) {
		t.Fatalf("attach annotations = %v (returned %v), want %v", stored.Metadata.Annotations, attached.Metadata.Annotations, want)
	}
	if !reg.UpdatedAt.Equal(now) {
		t.Fatalf("registry updatedAt = %v, want %v", reg.UpdatedAt, now)
	}
	if got := PersonaAnnotationsOf(*stored); got != (AgentPersonaAnnotations{Persona: "go-reviewer", PersonaDigest: digest, SystemPromptSnapshot: SystemPromptSnapshotOff}) {
		t.Fatalf("PersonaAnnotationsOf = %+v", got)
	}

	// Detach clears the persona pair and keeps the snapshot mode.
	if _, err := mutator.SetAgentPersona(&reg, "agent-1", AgentPersonaAnnotations{SystemPromptSnapshot: SystemPromptSnapshotOff}); err != nil {
		t.Fatal(err)
	}
	stored, _ = reg.Agent("agent-1")
	want = map[string]string{
		AnnotationAgentTopic:                "review the parser",
		AnnotationAgentSystemPromptSnapshot: SystemPromptSnapshotOff,
	}
	if !maps.Equal(stored.Metadata.Annotations, want) {
		t.Fatalf("detach annotations = %v, want %v", stored.Metadata.Annotations, want)
	}

	// Clearing every key of an Agent that holds nothing else leaves nil, not
	// an empty map, so the durable form is the one an unannotated Agent has.
	reg.Agents[0].Metadata.Annotations = map[string]string{AnnotationAgentSystemPromptSnapshot: SystemPromptSnapshotOff}
	if _, err := mutator.SetAgentPersona(&reg, "agent-1", AgentPersonaAnnotations{}); err != nil {
		t.Fatal(err)
	}
	if stored, _ := reg.Agent("agent-1"); stored.Metadata.Annotations != nil {
		t.Fatalf("fully cleared annotations = %#v, want nil", stored.Metadata.Annotations)
	}
}

func TestSetAgentPersonaRefusesAHalfPairAnUnknownModeAndAMissingAgent(t *testing.T) {
	t.Parallel()
	mutator := Mutator{Now: func() time.Time { return time.Date(2026, 9, 19, 3, 0, 0, 0, time.UTC) }}
	for name, test := range map[string]struct {
		uid  string
		want AgentPersonaAnnotations
		err  error
	}{
		"name without digest": {"agent-1", AgentPersonaAnnotations{Persona: "go-reviewer"}, ErrInvalidRegistry},
		"digest without name": {"agent-1", AgentPersonaAnnotations{PersonaDigest: "sha256:ab"}, ErrInvalidRegistry},
		"unknown mode":        {"agent-1", AgentPersonaAnnotations{SystemPromptSnapshot: "on"}, ErrInvalidRegistry},
		"missing agent":       {"agent-9", AgentPersonaAnnotations{SystemPromptSnapshot: SystemPromptSnapshotOff}, ErrNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			reg := personaAnnotationFixture()
			before := reg.Clone()
			if _, err := mutator.SetAgentPersona(&reg, test.uid, test.want); !errors.Is(err, test.err) {
				t.Fatalf("err = %v, want %v", err, test.err)
			}
			stored, _ := reg.Agent("agent-1")
			original, _ := before.Agent("agent-1")
			if !maps.Equal(stored.Metadata.Annotations, original.Metadata.Annotations) || !reg.UpdatedAt.Equal(before.UpdatedAt) {
				t.Fatalf("a refused mutation changed the Agent: %v", stored.Metadata.Annotations)
			}
		})
	}
}
