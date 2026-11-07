package projmux_test

import (
	"slices"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentmessage"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func TestPublicConstantsEqualInternal(t *testing.T) {
	for name, pair := range map[string][2]string{
		"api version":       {core.ResourceAPIVersion, metadata.APIVersion},
		"profile":           {core.AnnotationAgentProfile, metadata.AnnotationAgentProfile},
		"model":             {core.AnnotationAgentModel, metadata.AnnotationAgentModel},
		"effort":            {core.AnnotationAgentEffort, metadata.AnnotationAgentEffort},
		"link rules digest": {core.AnnotationAgentProjectLinkRulesDigest, metadata.AnnotationAgentProjectLinkRulesDigest},
		"creator agent":     {core.AnnotationCreatorAgent, metadata.AnnotationCreatorAgent},
		"notify file":       {core.NotifyFileName, notify.NotifyFileName},
		"question claude":   {string(core.AgentQuestionAnsweringClaude), string(config.AgentQuestionAnsweringClaude)},
		"question projmux":  {string(core.AgentQuestionAnsweringProjmux), string(config.AgentQuestionAnsweringProjmux)},
		"approval claude":   {string(core.AgentApprovalAnsweringClaude), string(config.AgentApprovalAnsweringClaude)},
		"approval projmux":  {string(core.AgentApprovalAnsweringProjmux), string(config.AgentApprovalAnsweringProjmux)},
	} {
		if pair[0] != pair[1] {
			t.Fatalf("%s: public %q internal %q", name, pair[0], pair[1])
		}
	}
	if core.DefaultAgentQuestionWindowSeconds != config.DefaultAgentQuestionWindowSeconds ||
		core.DefaultAgentApprovalWindowSeconds != config.DefaultAgentApprovalWindowSeconds {
		t.Fatal("default window seconds differ from the internal values")
	}
	if projmux.MaxPersonaBytes != persona.MaxSize {
		t.Fatalf("persona limit %d != %d", projmux.MaxPersonaBytes, persona.MaxSize)
	}
	if projmux.MaxProfileBytes != profile.MaxSize {
		t.Fatalf("profile limit %d != %d", projmux.MaxProfileBytes, profile.MaxSize)
	}
	// The public limit is derived from the core constant, so a change of the
	// core value changes this one in the same build.
	if projmux.MaxMessagePayloadBytes != agentmessage.MaxPayloadBytes {
		t.Fatalf("message payload limit %d != %d", projmux.MaxMessagePayloadBytes, agentmessage.MaxPayloadBytes)
	}
}

func TestRecordsDialogueReplyOnlyEqualsInternal(t *testing.T) {
	for name, annotations := range map[string]map[string]string{
		"nil":         nil,
		"empty":       {},
		"on":          {metadata.AnnotationAgentDialogueReplyOnly: metadata.DialogueReplyOnlyOn},
		"other value": {metadata.AnnotationAgentDialogueReplyOnly: "off"},
		"other key":   {core.AnnotationAgentProfile: "x"},
	} {
		if got, want := projmux.RecordsDialogueReplyOnly(annotations), metadata.RecordsDialogueReplyOnly(annotations); got != want {
			t.Fatalf("%s: public %v internal %v", name, got, want)
		}
	}
	if !projmux.RecordsDialogueReplyOnly(map[string]string{metadata.AnnotationAgentDialogueReplyOnly: metadata.DialogueReplyOnlyOn}) || projmux.RecordsDialogueReplyOnly(nil) {
		t.Fatal("representative table is not the expected judgement")
	}
}

func TestAINewWindowModesEqualInternalAndAreCopied(t *testing.T) {
	want := slices.Clone(config.AINewWindowModes)
	got := projmux.AINewWindowModes()
	if !slices.Equal(got, want) {
		t.Fatalf("modes %v != %v", got, want)
	}
	if &got[0] == &config.AINewWindowModes[0] {
		t.Fatal("result aliases the internal slice")
	}
	got[0] = "changed"
	if !slices.Equal(config.AINewWindowModes, want) {
		t.Fatalf("mutating the result changed the internal modes: %v", config.AINewWindowModes)
	}
	if !slices.Equal(projmux.AINewWindowModes(), want) {
		t.Fatal("mutating the result changed the next call")
	}
}
