package metadata

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"testing"
	"time"
)

// TestSetAgentDialogueReplyOnlyRecordsTheModeOnceAndKeepsOtherAnnotations pins
// the one write of the reply-only record: it adds the key with its one value,
// leaves every other annotation as it was, and is a no-op on an Agent that
// records it already. There is no clearing write.
func TestSetAgentDialogueReplyOnlyRecordsTheModeOnceAndKeepsOtherAnnotations(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	mutator := Mutator{Now: func() time.Time { return now }}
	reg := personaAnnotationFixture()

	agent, err := mutator.SetAgentDialogueReplyOnly(&reg, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{AnnotationAgentTopic: "review the parser", AnnotationAgentDialogueReplyOnly: DialogueReplyOnlyOn}
	if !maps.Equal(agent.Metadata.Annotations, want) || !RecordsDialogueReplyOnly(agent.Metadata.Annotations) || !reg.UpdatedAt.Equal(now) {
		t.Fatalf("annotations = %v updatedAt = %v, want %v at %v", agent.Metadata.Annotations, reg.UpdatedAt, want, now)
	}

	reg.UpdatedAt = time.Time{}
	if _, err := mutator.SetAgentDialogueReplyOnly(&reg, "agent-1"); err != nil {
		t.Fatal(err)
	}
	if !reg.UpdatedAt.IsZero() {
		t.Fatal("recording the mode again touched the registry")
	}
	if _, err := mutator.SetAgentDialogueReplyOnly(&reg, "agent-missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Agent = %v, want ErrNotFound", err)
	}
}

// TestAnAgentWithoutTheReplyOnlyRecordReencodesByteForByte is acceptance 5:
// the record is an additive annotation, so a registry written before it
// existed decodes and re-encodes to the same bytes, and a recorded Agent keeps
// the record through the same round trip.
func TestAnAgentWithoutTheReplyOnlyRecordReencodesByteForByte(t *testing.T) {
	t.Parallel()
	for _, recorded := range []bool{false, true} {
		reg := personaAnnotationFixture()
		if recorded {
			reg.Agents[0].Metadata.Annotations[AnnotationAgentDialogueReplyOnly] = DialogueReplyOnlyOn
		}
		before, err := json.MarshalIndent(reg.Normalize(), "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		var decoded Registry
		if err := json.Unmarshal(before, &decoded); err != nil {
			t.Fatal(err)
		}
		after, err := json.MarshalIndent(decoded.Normalize(), "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("recorded=%t re-encoded bytes differ:\n%s\n---\n%s", recorded, before, after)
		}
		if got := RecordsDialogueReplyOnly(decoded.Agents[0].Metadata.Annotations); got != recorded {
			t.Fatalf("recorded=%t decoded record = %t", recorded, got)
		}
		if got := bytes.Contains(after, []byte(AnnotationAgentDialogueReplyOnly)); got != recorded {
			t.Fatalf("recorded=%t encoding carries the key = %t", recorded, got)
		}
	}
}
