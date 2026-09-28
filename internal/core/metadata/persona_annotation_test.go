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

// TestSetAgentEffortReplacesOnlyTheEffort pins the one mutation
// `agent resume --effort` makes: the effort is replaced, every other
// annotation stays, and recording the value already recorded is not a change.
func TestSetAgentEffortReplacesOnlyTheEffort(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	mutator := Mutator{Now: func() time.Time { return now }}
	reg := personaAnnotationFixture()

	if _, err := mutator.SetAgentEffort(&reg, "agent-1", "max", SettingSourceResume); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{AnnotationAgentTopic: "review the parser", AnnotationAgentEffort: "max", AnnotationAgentEffortSource: SettingSourceResume}
	stored, _ := reg.Agent("agent-1")
	if !maps.Equal(stored.Metadata.Annotations, want) || !reg.UpdatedAt.Equal(now) {
		t.Fatalf("annotations = %v updatedAt = %v, want %v at %v", stored.Metadata.Annotations, reg.UpdatedAt, want, now)
	}

	later := Mutator{Now: func() time.Time { return now.Add(time.Hour) }}
	if _, err := later.SetAgentEffort(&reg, "agent-1", "max", SettingSourceResume); err != nil {
		t.Fatal(err)
	}
	if !reg.UpdatedAt.Equal(now) {
		t.Fatalf("recording the same effort moved updatedAt to %v", reg.UpdatedAt)
	}

	if _, err := mutator.SetAgentEffort(&reg, "agent-1", " ", SettingSourceResume); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("empty effort = %v, want %v", err, ErrInvalidRegistry)
	}
	if _, err := mutator.SetAgentEffort(&reg, "agent-missing", "low", SettingSourceResume); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing agent = %v, want %v", err, ErrNotFound)
	}
}

// TestSetAgentModelReplacesOnlyTheModel pins the one mutation
// `agent resume --model` and `agent relaunch --model` make: the model is
// replaced as passed (trimmed, no alias normalization), every other annotation
// stays, and recording the value already recorded is not a change.
func TestSetAgentModelReplacesOnlyTheModel(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	mutator := Mutator{Now: func() time.Time { return now }}
	reg := personaAnnotationFixture()

	if _, err := mutator.SetAgentModel(&reg, "agent-1", " haiku ", SettingSourceRelaunch); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{AnnotationAgentTopic: "review the parser", AnnotationAgentModel: "haiku", AnnotationAgentModelSource: SettingSourceRelaunch}
	stored, _ := reg.Agent("agent-1")
	if !maps.Equal(stored.Metadata.Annotations, want) || !reg.UpdatedAt.Equal(now) {
		t.Fatalf("annotations = %v updatedAt = %v, want %v at %v", stored.Metadata.Annotations, reg.UpdatedAt, want, now)
	}

	later := Mutator{Now: func() time.Time { return now.Add(time.Hour) }}
	if _, err := later.SetAgentModel(&reg, "agent-1", "haiku", SettingSourceRelaunch); err != nil {
		t.Fatal(err)
	}
	if !reg.UpdatedAt.Equal(now) {
		t.Fatalf("recording the same model moved updatedAt to %v", reg.UpdatedAt)
	}

	if _, err := mutator.SetAgentModel(&reg, "agent-1", " ", SettingSourceRelaunch); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("empty model = %v, want %v", err, ErrInvalidRegistry)
	}
	if _, err := mutator.SetAgentModel(&reg, "agent-missing", "sonnet", SettingSourceRelaunch); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing agent = %v, want %v", err, ErrNotFound)
	}
}

// TestSetAgentSettingRecordsTheSourceWithTheValue pins that the model and
// effort mutations write the value and its source together: a new source for
// the same value is a change, an unknown or missing source is refused before
// anything is written, and the source vocabulary is the public one.
func TestSetAgentSettingRecordsTheSourceWithTheValue(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	mutator := Mutator{Now: func() time.Time { return now }}
	reg := personaAnnotationFixture()
	agent := &reg.Agents[0]
	agent.Metadata.Annotations[AnnotationAgentEffort] = "high"
	agent.Metadata.Annotations[AnnotationAgentEffortSource] = SettingSourceProfile

	if _, err := mutator.SetAgentEffort(&reg, "agent-1", "high", SettingSourceRelaunch); err != nil {
		t.Fatal(err)
	}
	stored, _ := reg.Agent("agent-1")
	if got := stored.Metadata.Annotations[AnnotationAgentEffortSource]; got != SettingSourceRelaunch || !reg.UpdatedAt.Equal(now) {
		t.Fatalf("effort source = %q updatedAt = %v, want %q at %v", got, reg.UpdatedAt, SettingSourceRelaunch, now)
	}

	before := maps.Clone(stored.Metadata.Annotations)
	for _, source := range []string{"", "role", "cli", " resume"} {
		if _, err := mutator.SetAgentEffort(&reg, "agent-1", "low", source); !errors.Is(err, ErrInvalidRegistry) {
			t.Fatalf("effort source %q = %v, want %v", source, err, ErrInvalidRegistry)
		}
		if _, err := mutator.SetAgentModel(&reg, "agent-1", "haiku", source); !errors.Is(err, ErrInvalidRegistry) {
			t.Fatalf("model source %q = %v, want %v", source, err, ErrInvalidRegistry)
		}
	}
	stored, _ = reg.Agent("agent-1")
	if !maps.Equal(stored.Metadata.Annotations, before) {
		t.Fatalf("a refused source changed annotations to %v, want %v", stored.Metadata.Annotations, before)
	}

	for key, want := range map[string]string{
		AnnotationAgentProfileSource:      "projmux.io/profile-source",
		AnnotationAgentInstructionsSource: "projmux.io/instructions-source",
		AnnotationAgentModelSource:        "projmux.io/model-source",
		AnnotationAgentEffortSource:       "projmux.io/effort-source",
	} {
		if key != want {
			t.Fatalf("source key = %q, want %q", key, want)
		}
	}
	for _, source := range []string{"profile", "flag", "resume", "relaunch", "attach", "inherited"} {
		if !ValidSettingSource(source) {
			t.Fatalf("ValidSettingSource(%q) = false", source)
		}
	}
	if ValidSettingSource("role") || ValidSettingSource("") {
		t.Fatal("a source outside its key's vocabulary was accepted")
	}
}

// TestSetAgentPersonaWritesTheInstructionsSourceInTheSameMutation pins that
// attach, detach, and their restore move the instructions source with the
// persona, that a detach keeps the source without a persona, and that
// SameLaunch ignores the source.
func TestSetAgentPersonaWritesTheInstructionsSourceInTheSameMutation(t *testing.T) {
	t.Parallel()
	mutator := Mutator{Now: func() time.Time { return time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC) }}
	reg := personaAnnotationFixture()
	reg.Agents[0].Metadata.Annotations[AnnotationAgentPersona] = "lead-ship"
	reg.Agents[0].Metadata.Annotations[AnnotationAgentPersonaDigest] = "sha256:aa"
	reg.Agents[0].Metadata.Annotations[AnnotationAgentInstructionsSource] = SettingSourceProfile
	stored, _ := reg.Agent("agent-1")
	previous := PersonaAnnotationsOf(*stored)
	if previous.InstructionsSource != SettingSourceProfile {
		t.Fatalf("PersonaAnnotationsOf source = %q", previous.InstructionsSource)
	}

	detached := AgentPersonaAnnotations{SystemPromptSnapshot: SystemPromptSnapshotOff, InstructionsSource: SettingSourceAttach}
	if _, err := mutator.SetAgentPersona(&reg, "agent-1", detached); err != nil {
		t.Fatal(err)
	}
	stored, _ = reg.Agent("agent-1")
	want := map[string]string{
		AnnotationAgentTopic:                "review the parser",
		AnnotationAgentSystemPromptSnapshot: SystemPromptSnapshotOff,
		AnnotationAgentInstructionsSource:   SettingSourceAttach,
	}
	if !maps.Equal(stored.Metadata.Annotations, want) {
		t.Fatalf("detached annotations = %v, want %v", stored.Metadata.Annotations, want)
	}

	if _, err := mutator.SetAgentPersona(&reg, "agent-1", previous); err != nil {
		t.Fatal(err)
	}
	stored, _ = reg.Agent("agent-1")
	if got := PersonaAnnotationsOf(*stored); got != previous {
		t.Fatalf("restored = %+v, want %+v", got, previous)
	}
	if _, err := mutator.SetAgentPersona(&reg, "agent-1", AgentPersonaAnnotations{InstructionsSource: "role"}); !errors.Is(err, ErrInvalidRegistry) {
		t.Fatalf("instructions source role = %v, want %v", err, ErrInvalidRegistry)
	}

	attach := previous
	attach.InstructionsSource = SettingSourceAttach
	if !previous.SameLaunch(attach) || previous.SameLaunch(detached) {
		t.Fatal("SameLaunch must compare the launch fields and ignore the source")
	}
}

// TestSetAgentProjectLinkRulesRecordsTheDigestWithTheSnapshotOff pins the one
// mutation a resume makes when its Project's label link rules changed: the
// digest is replaced (or removed) together with the sticky snapshot mode off,
// and every other annotation stays.
func TestSetAgentProjectLinkRulesRecordsTheDigestWithTheSnapshotOff(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	mutator := Mutator{Now: func() time.Time { return now }}
	reg := personaAnnotationFixture()
	const digest = "0000000000000000000000000000000000000000000000000000000000000001"

	if _, err := mutator.SetAgentProjectLinkRules(&reg, "agent-1", digest); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		AnnotationAgentTopic:                  "review the parser",
		AnnotationAgentProjectLinkRulesDigest: digest,
		AnnotationAgentSystemPromptSnapshot:   SystemPromptSnapshotOff,
	}
	stored, _ := reg.Agent("agent-1")
	if !maps.Equal(stored.Metadata.Annotations, want) || !reg.UpdatedAt.Equal(now) {
		t.Fatalf("annotations = %v updatedAt = %v, want %v at %v", stored.Metadata.Annotations, reg.UpdatedAt, want, now)
	}

	later := Mutator{Now: func() time.Time { return now.Add(time.Hour) }}
	if _, err := later.SetAgentProjectLinkRules(&reg, "agent-1", digest); err != nil {
		t.Fatal(err)
	}
	if !reg.UpdatedAt.Equal(now) {
		t.Fatalf("recording the same digest moved updatedAt to %v", reg.UpdatedAt)
	}

	// Removed rules delete the digest; the snapshot mode stays off.
	if _, err := later.SetAgentProjectLinkRules(&reg, "agent-1", ""); err != nil {
		t.Fatal(err)
	}
	delete(want, AnnotationAgentProjectLinkRulesDigest)
	if stored, _ := reg.Agent("agent-1"); !maps.Equal(stored.Metadata.Annotations, want) {
		t.Fatalf("removed rules annotations = %v, want %v", stored.Metadata.Annotations, want)
	}
	if _, err := mutator.SetAgentProjectLinkRules(&reg, "agent-missing", digest); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing agent = %v, want %v", err, ErrNotFound)
	}
}

// TestSetAgentGuidanceRecordsTheDigestWithTheSnapshotOff pins the one mutation
// a resume makes when the agent guidance changed: the digest is replaced (or
// removed, when the guidance was turned off) together with the sticky snapshot
// mode off, and every other annotation stays.
func TestSetAgentGuidanceRecordsTheDigestWithTheSnapshotOff(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	mutator := Mutator{Now: func() time.Time { return now }}
	reg := personaAnnotationFixture()
	const digest = "0000000000000000000000000000000000000000000000000000000000000002"

	if _, err := mutator.SetAgentGuidance(&reg, "agent-1", digest); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		AnnotationAgentTopic:                "review the parser",
		AnnotationAgentGuidanceDigest:       digest,
		AnnotationAgentSystemPromptSnapshot: SystemPromptSnapshotOff,
	}
	stored, _ := reg.Agent("agent-1")
	if !maps.Equal(stored.Metadata.Annotations, want) || !reg.UpdatedAt.Equal(now) {
		t.Fatalf("annotations = %v updatedAt = %v, want %v at %v", stored.Metadata.Annotations, reg.UpdatedAt, want, now)
	}

	later := Mutator{Now: func() time.Time { return now.Add(time.Hour) }}
	if _, err := later.SetAgentGuidance(&reg, "agent-1", digest); err != nil {
		t.Fatal(err)
	}
	if !reg.UpdatedAt.Equal(now) {
		t.Fatalf("recording the same digest moved updatedAt to %v", reg.UpdatedAt)
	}

	// Guidance turned off deletes the digest; the snapshot mode stays off.
	if _, err := later.SetAgentGuidance(&reg, "agent-1", ""); err != nil {
		t.Fatal(err)
	}
	delete(want, AnnotationAgentGuidanceDigest)
	if stored, _ := reg.Agent("agent-1"); !maps.Equal(stored.Metadata.Annotations, want) {
		t.Fatalf("guidance off annotations = %v, want %v", stored.Metadata.Annotations, want)
	}
	if _, err := mutator.SetAgentGuidance(&reg, "agent-missing", digest); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing agent = %v, want %v", err, ErrNotFound)
	}
}

// TestSetAgentSettingFromProfileKeepsTheSourceWithoutAValue pins the profile
// layer writer: the value follows the profile, none included, and the source
// stays profile so the item goes on following it.
func TestSetAgentSettingFromProfileKeepsTheSourceWithoutAValue(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	mutator := Mutator{Now: func() time.Time { return now }}
	reg := personaAnnotationFixture()
	agent := &reg.Agents[0]
	agent.Metadata.Annotations[AnnotationAgentEffort] = "high"
	agent.Metadata.Annotations[AnnotationAgentModel] = "opus"

	if _, err := mutator.SetAgentEffortFromProfile(&reg, "agent-1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := mutator.SetAgentModelFromProfile(&reg, "agent-1", "sonnet"); err != nil {
		t.Fatal(err)
	}
	stored, _ := reg.Agent("agent-1")
	got := stored.Metadata.Annotations
	if _, ok := got[AnnotationAgentEffort]; ok || got[AnnotationAgentEffortSource] != SettingSourceProfile ||
		got[AnnotationAgentModel] != "sonnet" || got[AnnotationAgentModelSource] != SettingSourceProfile || !reg.UpdatedAt.Equal(now) {
		t.Fatalf("annotations = %v updatedAt = %v", got, reg.UpdatedAt)
	}

	later := now.Add(time.Hour)
	mutator.Now = func() time.Time { return later }
	if _, err := mutator.SetAgentEffortFromProfile(&reg, "agent-1", ""); err != nil {
		t.Fatal(err)
	}
	if !reg.UpdatedAt.Equal(now) {
		t.Fatalf("recording what the Agent records already moved updatedAt to %v", reg.UpdatedAt)
	}
}

// TestSetAgentProfileReplacesOrClearsTheProfileWithItsSource pins the writer of
// an `agent relaunch --profile` switch: name, digest and source move together,
// an empty name removes all three, and a name without a digest or with an
// item-only source is refused.
func TestSetAgentProfileReplacesOrClearsTheProfileWithItsSource(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	mutator := Mutator{Now: func() time.Time { return now }}
	reg := personaAnnotationFixture()
	agent := &reg.Agents[0]
	agent.Metadata.Annotations[AnnotationAgentProfile] = "role"
	agent.Metadata.Annotations[AnnotationAgentProfileDigest] = "sha256:old"
	agent.Metadata.Annotations[AnnotationAgentProfileSource] = SettingSourceRole

	if _, err := mutator.SetAgentProfile(&reg, "agent-1", "review", "sha256:new", SettingSourceRelaunch); err != nil {
		t.Fatal(err)
	}
	stored, _ := reg.Agent("agent-1")
	got := stored.Metadata.Annotations
	if got[AnnotationAgentProfile] != "review" || got[AnnotationAgentProfileDigest] != "sha256:new" || got[AnnotationAgentProfileSource] != SettingSourceRelaunch || !reg.UpdatedAt.Equal(now) {
		t.Fatalf("annotations = %v updatedAt = %v", got, reg.UpdatedAt)
	}
	for _, bad := range [][3]string{{"review", "", SettingSourceRelaunch}, {"review", "sha256:new", SettingSourceProfile}, {"review", "sha256:new", ""}} {
		if _, err := mutator.SetAgentProfile(&reg, "agent-1", bad[0], bad[1], bad[2]); err == nil {
			t.Fatalf("SetAgentProfile%q succeeded", bad)
		}
	}
	if _, err := mutator.SetAgentProfile(&reg, "agent-1", "", "", ""); err != nil {
		t.Fatal(err)
	}
	stored, _ = reg.Agent("agent-1")
	for _, key := range []string{AnnotationAgentProfile, AnnotationAgentProfileDigest, AnnotationAgentProfileSource} {
		if _, ok := stored.Metadata.Annotations[key]; ok {
			t.Fatalf("clearing the profile left %s in %v", key, stored.Metadata.Annotations)
		}
	}
}

// TestClearAgentSettingRemovesTheValueAndItsSource pins the writer of an item
// left with no layer: value and source go together, and clearing nothing is
// not a change.
func TestClearAgentSettingRemovesTheValueAndItsSource(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	mutator := Mutator{Now: func() time.Time { return now }}
	reg := personaAnnotationFixture()
	agent := &reg.Agents[0]
	agent.Metadata.Annotations[AnnotationAgentEffort] = "high"
	agent.Metadata.Annotations[AnnotationAgentEffortSource] = SettingSourceRelaunch
	agent.Metadata.Annotations[AnnotationAgentModel] = "opus"

	if _, err := mutator.ClearAgentEffort(&reg, "agent-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := mutator.ClearAgentModel(&reg, "agent-1"); err != nil {
		t.Fatal(err)
	}
	stored, _ := reg.Agent("agent-1")
	for _, key := range []string{AnnotationAgentEffort, AnnotationAgentEffortSource, AnnotationAgentModel, AnnotationAgentModelSource} {
		if _, ok := stored.Metadata.Annotations[key]; ok {
			t.Fatalf("clearing left %s in %v", key, stored.Metadata.Annotations)
		}
	}
	later := now.Add(time.Hour)
	mutator.Now = func() time.Time { return later }
	if _, err := mutator.ClearAgentEffort(&reg, "agent-1"); err != nil {
		t.Fatal(err)
	}
	if !reg.UpdatedAt.Equal(now) {
		t.Fatalf("clearing nothing moved updatedAt to %v", reg.UpdatedAt)
	}
}
