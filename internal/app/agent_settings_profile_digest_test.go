package app

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/agentsettings"
	m "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

func TestTmuxSameNameProfileDigestRelaunchAndResume(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, route := range []string{"relaunch", "resume"} {
			t.Run(provider+"/"+route, func(t *testing.T) {
				var f *personaAttachFixture
				var old, next string
				if provider == aiModeClaude {
					layers := newRelaunchLayersFixture(t)
					f = layers.personaAttachFixture
					before := layers.onRoleWithOverrides(t)
					old = before[m.AnnotationAgentProfileDigest]
					next = writeCodexProfile(t, layers.profiles, "role", "instructions = \"lead\"\nmodel = \"opus\"\neffort = \"high\"\n[permissions]\nallow = [\"Read\"]\n")
				} else {
					fixture, profiles, _, before := codexRelaunchFixture(t)
					f = fixture
					old = before[m.AnnotationAgentProfileDigest]
					next = writeCodexProfile(t, profiles, "guard", "instructions = \"lead\"\nmodel = \"gpt-6\"\n[permissions]\nsandbox = \"workspace-write\"\napproval = \"on-request\"\n")
				}
				before := maps.Clone(f.agent(t).Metadata.Annotations)
				if old == next {
					t.Fatal("profile content fixture unchanged")
				}
				if route == "relaunch" {
					snapshot := f.store.snapshot()
					if _, _, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "--dry-run"); err != nil {
						t.Fatal(err)
					}
					if f.store.snapshot() != snapshot || !maps.Equal(before, f.agent(t).Metadata.Annotations) {
						t.Fatal("dry-run recorded profile")
					}
				} else {
					stopFixtureAgent(t, f)
				}
				out, stderr, err := runRoute(t, f.command, route, "uid:"+personaAttachAgent)
				if err != nil {
					t.Fatalf("%s: %v stdout=%q stderr=%q", route, err, out, stderr)
				}
				after := f.agent(t).Metadata.Annotations
				if after[m.AnnotationAgentProfileDigest] != next || after[m.AnnotationAgentProfile] != before[m.AnnotationAgentProfile] || after[m.AnnotationAgentProfileSource] != before[m.AnnotationAgentProfileSource] {
					t.Fatalf("recorded digest=%q want=%q; profile name/source preserved=%t", after[m.AnnotationAgentProfileDigest], next, after[m.AnnotationAgentProfileSource] == before[m.AnnotationAgentProfileSource])
				}
			})
		}
	}
}

func TestRecordedProfileDigestKeepsCodexInstructionsNotApplied(t *testing.T) {
	base := map[string]string{m.AnnotationAgentProfile: "role", m.AnnotationAgentProfileDigest: "old", m.AnnotationAgentProfileSource: m.SettingSourceFlag, m.AnnotationAgentPersona: "lead", m.AnnotationAgentPersonaDigest: "lead-digest", m.AnnotationAgentInstructionsSource: m.SettingSourceProfile}
	profile := &agentsettings.Profile{Name: "role", Digest: "recipe", Instructions: "reviewer"}
	input := agentsettings.Input{Annotations: base, Profile: profile, InstructionsFixed: true, InstructionsDigest: func(string) (string, bool) { return "reviewer-digest", true }}
	launch := agentSettingsLaunch{layered: true, resolution: agentsettings.Resolve(input)}
	if !launch.resolution.InstructionsNotApplied {
		t.Fatal("fixture instructions were applied")
	}
	reg := m.Registry{Agents: []m.Agent{{Metadata: m.ObjectMeta{UID: "agent", Annotations: maps.Clone(base)}}}}
	if err := launch.record(&reg, m.Mutator{}, "agent"); err != nil {
		t.Fatal(err)
	}
	a, _ := reg.Agent("agent")
	input.Annotations = a.Metadata.Annotations
	after := agentsettings.Resolve(input)
	if a.Metadata.Annotations[m.AnnotationAgentProfileDigest] != "recipe" || a.Metadata.Annotations[m.AnnotationAgentPersona] != "lead" || a.Metadata.Annotations[m.AnnotationAgentPersonaDigest] != "lead-digest" || !after.InstructionsNotApplied || slices.Contains(after.Reasons, agentsettings.ReasonProfileChanged) || !strings.Contains(launch.notice("worker"), "codex-instructions-immutable") {
		t.Fatal("profile digest update hid unapplied Codex instructions")
	}
	profile.Digest = "later"
	if !slices.Contains(agentsettings.Resolve(input).Reasons, agentsettings.ReasonProfileChanged) {
		t.Fatal("later profile change hidden")
	}
}

func TestDeferredProfileDigestPlansFromFixedResolution(t *testing.T) {
	f := newRelaunchLayersFixture(t)
	base := f.onRoleWithOverrides(t)
	layer := &agentsettings.Profile{Name: "role", Digest: "frozen", Instructions: "lead", Model: "opus", Effort: "high"}
	settings := agentSettingsLaunch{layered: true, resolution: agentsettings.Resolve(agentsettings.Input{Annotations: base, Profile: layer, InstructionsDigest: func(string) (string, bool) { return f.leadDigest, true }})}
	candidate := processResumeCandidate{Agent: f.agent(t).Clone(), Record: m.ProcessSessionRecord{Provider: aiModeClaude, SessionID: personaResumeConversation}}
	recipe := processRelaunchRecipe{restart: &agentRestart{settings: settings}, workspace: candidate.Agent.Spec.Workspace}
	launch := processRelaunchLaunch{settings: settings, command: processhost.Command{Args: []string{"--model", "opus", "--permission-mode", "auto"}}}
	layer.Digest = "later-file"
	record, err := f.command.planDeferredRelaunchRecord(candidate, recipe, launch, true)
	if err != nil {
		t.Fatal(err)
	}
	if record.NewAnnotations[m.AnnotationAgentProfileDigest] != "frozen" || record.OldAnnotations[m.AnnotationAgentProfileDigest] != base[m.AnnotationAgentProfileDigest] || record.NewAnnotations[m.AnnotationAgentProfileSource] != base[m.AnnotationAgentProfileSource] || f.agent(t).Metadata.Annotations[m.AnnotationAgentProfileDigest] != base[m.AnnotationAgentProfileDigest] {
		t.Fatal("planning stamped current file or mutated source")
	}
}

func TestProfileDigestRollbackRequiresExactRecordedRecipe(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "restore", true: "competing-change"}[changed], func(t *testing.T) {
			f := newRelaunchLayersFixture(t)
			base := f.onRoleWithOverrides(t)
			agent, _ := f.store.registry.Agent(personaAttachAgent)
			agent.Status.Phase = m.PhaseOffline
			window, _ := f.store.registry.Window(agent.Metadata.OwnerUID())
			binding := processhost.Binding{Project: window.Metadata.OwnerUID(), Window: window.Metadata.UID, Agent: personaAttachAgent, Pane: personaAttachPane, Host: "host", Generation: "generation", Operation: "operation"}
			oldRecord := m.ProcessSessionRecord{Provider: aiModeClaude, Binding: metadataProcessBinding(binding), SessionID: personaResumeConversation, ConnectionID: "connection", ResumeState: m.ProcessResumable}
			pane, _ := f.store.registry.Pane(personaAttachPane)
			pane.Spec.Runtime.Kind = m.RuntimeProcess
			pane.Status.Activation = m.PaneActivation{}
			pane.Status.ProcessSession = oldRecord.Clone()
			if err := f.store.registry.Validate(); err != nil {
				t.Fatal(err)
			}
			previous := processResumeCandidate{Agent: agent.Clone(), Record: oldRecord}
			settings := agentSettingsLaunch{layered: true, resolution: agentsettings.Resolve(agentsettings.Input{Annotations: base, Profile: &agentsettings.Profile{Name: "role", Digest: "reserved", Instructions: "lead", Model: "opus", Effort: "high"}, InstructionsDigest: func(string) (string, bool) { return f.leadDigest, true }})}
			if err := settings.record(&f.store.registry, m.Mutator{}, personaAttachAgent); err != nil {
				t.Fatal(err)
			}
			if changed {
				agent, _ = f.store.registry.Agent(personaAttachAgent)
				agent.Metadata.Annotations[m.AnnotationAgentProfileDigest] = "competing"
			}
			before := f.store.registry.Clone()
			beforeAgent, _ := before.Agent(personaAttachAgent)
			err := f.command.restoreProcessRelaunchSettings(previous, binding, settings, agentGuidanceLaunch{}, projectLinksLaunch{})
			if changed {
				if err == nil || !strings.Contains(err.Error(), "restoration refused") || !maps.Equal(f.agent(t).Metadata.Annotations, beforeAgent.Metadata.Annotations) {
					t.Fatalf("competing recipe overwritten: %v", err)
				}
			} else if err != nil || !maps.Equal(f.agent(t).Metadata.Annotations, base) {
				t.Fatalf("exact recipe rollback failed: %v", err)
			}
		})
	}
}
