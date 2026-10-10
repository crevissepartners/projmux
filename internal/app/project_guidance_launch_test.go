package app

import (
	"bytes"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

func writeProjectGuidance(t *testing.T, planner *aiCommand, uid string, text []byte) {
	t.Helper()
	if err := agentguidance.NewDefaultProjectStore(linkRulesPaths(t, planner)).Save(uid, text); err != nil {
		t.Fatal(err)
	}
}

func assertPromptContent(t *testing.T, args []string, want []byte) {
	t.Helper()
	path := systemPromptFileOf(t, args)
	if path == "" {
		t.Fatalf("missing prompt file: %q", args)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("prompt=%q want=%q", got, want)
	}
}

func TestProjectGuidanceCreateIsScopedAndAbsentBytesAreUnchanged(t *testing.T) {
	for _, text := range [][]byte{nil, []byte(" \t\n"), []byte("Project-specific policy.\n")} {
		t.Run(string(text), func(t *testing.T) {
			f := newAgentGuidanceCreate(t)
			writeProjectGuidance(t, f.planner, f.alpha, text)
			tail, annotations, stderr := f.run(t)
			want := agentguidance.Default()
			if len(bytes.TrimSpace(text)) != 0 {
				want = joinSystemPrompt(want, text)
			}
			assertPromptContent(t, tail, want)
			if strings.Contains(stderr, projectGuidanceReasonUnavailable) {
				t.Fatal(stderr)
			}
			digest := annotations[coremetadata.AnnotationAgentProjectGuidanceDigest]
			if len(bytes.TrimSpace(text)) != 0 && digest != agentguidance.Digest(text) {
				t.Fatal("wrong Project digest", digest)
			}
			if len(bytes.TrimSpace(text)) == 0 && digest != "" {
				t.Fatal("empty Project digest", digest)
			}
			var beta *coremetadata.Project
			for i := range f.store.registry.Projects {
				if f.store.registry.Projects[i].Metadata.Name == "beta" {
					beta = &f.store.registry.Projects[i]
					break
				}
			}
			if beta == nil {
				t.Fatal("missing beta")
			}
			other := f.planner.PlanProjectLinks(aiModeClaude, *beta, nil)
			if other.project.on() || other.project.digest != "" {
				t.Fatal("Project guidance leaked to beta")
			}
		})
	}
}

func TestProjectGuidanceResumeRefreshesDigestAndKeepsOtherLayers(t *testing.T) {
	for _, scenario := range []string{"added", "same", "changed", "removed", "unavailable", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			planner := agentLaunchArgvTestCommand(t)
			turnAgentGuidanceOff(t, planner)
			store := newFakeResourceStore(t)
			_, beta := reUIDLinkRulesProjects(t, store)
			paths := linkRulesPaths(t, planner)
			projectStore := agentguidance.NewDefaultProjectStore(paths)
			old := []byte("old Project\n")
			next := []byte("new Project\n")
			annotations := map[string]string{"custom": "preserve"}
			if scenario != "added" {
				annotations[coremetadata.AnnotationAgentProjectGuidanceDigest] = agentguidance.Digest(old)
			}
			if scenario == "same" {
				next = old
			}
			if scenario == "removed" {
				next = nil
			}
			writeProjectGuidance(t, planner, beta, next)
			if scenario == "unavailable" || scenario == "oversized" {
				path, _ := projectStore.Path(beta)
				if scenario == "unavailable" {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.WriteFile(path, bytes.Repeat([]byte("x"), agentguidance.MaxSize+1), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			tail, stderr := resumeClaudeAgentWithGuidance(t, planner, store, annotations)
			got := recordedAgentAnnotations(store, "agt-beta-codex")
			switch scenario {
			case "same":
				assertPromptContent(t, tail, old)
				if !maps.Equal(got, annotations) || slices.Contains(tail, "--system-prompt-snapshot") {
					t.Fatal("unchanged digest mutated", got, tail)
				}
			case "unavailable", "oversized":
				if systemPromptFileOf(t, tail) != "" || !maps.Equal(got, annotations) || strings.Count(stderr, projectGuidanceReasonUnavailable) != 1 {
					t.Fatalf("failure fallback: %v %v %q", tail, got, stderr)
				}
			default:
				if len(next) > 0 {
					assertPromptContent(t, tail, next)
				} else if systemPromptFileOf(t, tail) != "" {
					t.Fatal("removed still attached")
				}
				wantDigest := ""
				if len(next) > 0 {
					wantDigest = agentguidance.Digest(next)
				}
				if got[coremetadata.AnnotationAgentProjectGuidanceDigest] != wantDigest || got[coremetadata.AnnotationAgentSystemPromptSnapshot] != "off" || got["custom"] != "preserve" {
					t.Fatal("bad refreshed annotations", got)
				}
			}
		})
	}
}

// Test the JSON contract through the actual relaunch route, including dry-run.
func TestProjectGuidanceRelaunchJSONReportsChange(t *testing.T) {
	for _, dry := range []bool{true, false} {
		t.Run(map[bool]string{true: "dry", false: "execute"}[dry], func(t *testing.T) {
			f := newRelaunchFixture(t)
			alpha, _ := reUIDLinkRulesProjects(t, f.store)
			writeProjectGuidance(t, f.planner, alpha, []byte("Project relaunch\n"))
			f.command.rebind.launcher = &agentGuidanceResumeLauncher{&projectLinksResumeLauncher{f.launcher}}
			args := []string{"relaunch", "uid:" + personaAttachAgent, "-o", "json"}
			if dry {
				args = append(args, "--dry-run")
			}
			stdout, stderr, err := runRoute(t, f.command, args...)
			if err != nil || !strings.Contains(stdout, `"project-guidance-changed"`) {
				t.Fatalf("JSON: %s %s %v", stdout, stderr, err)
			}
			if !dry {
				got := f.agent(t).Metadata.Annotations
				if got[coremetadata.AnnotationAgentProjectGuidanceDigest] != agentguidance.Digest([]byte("Project relaunch\n")) {
					t.Fatal("relaunch did not record Project", got)
				}
			}
		})
	}
}

func (l *codexGuidanceAgentLauncher) PlanCodexProjectLinks(project coremetadata.Project) projectLinksLaunch {
	return l.planner.PlanCodexProjectLinks(project)
}

func TestProjectGuidanceCodexFreshCreateOrderAndGlobalOff(t *testing.T) {
	for _, global := range []bool{false, true} {
		t.Run(map[bool]string{true: "global", false: "off"}[global], func(t *testing.T) {
			create, store, tmux, native, personas := newCodexPersonaCreate(t)
			alpha, _ := reUIDLinkRulesProjects(t, store)
			paths := withCodexGuidanceSeam(t, create)
			project := []byte("Codex Project instructions\n")
			if err := agentguidance.NewDefaultProjectStore(paths).Save(alpha, project); err != nil {
				t.Fatal(err)
			}
			writeCodexGuidancePersona(t, personas)
			writeLinkRules(t, paths, alpha, linkRulesAlpha)
			if !global {
				writeAgentGuidance(t, paths, nil)
			}
			_, stderr, err := runRoute(t, create, codexNativeCreateArgs("--persona", "reviewer")...)
			if err != nil {
				t.Fatal(err, stderr)
			}
			agent := agentNamed(t, store, "win-alpha-main", "agent-test-1")
			result := codexGuidanceResult{creates: native.creates, agent: agent, stderr: stderr, leaked: codexGuidanceLeakCheck(tmux, store)}
			parts := [][]byte{}
			if global {
				parts = append(parts, agentguidance.Default(), []byte(codexAgentIdentity(agent.Metadata.UID)))
			}
			links := create.agents.(*codexGuidanceAgentLauncher).planner.PlanCodexProjectLinks(linkRulesRegistryProject(t, store, alpha))
			parts = append(parts, []byte(codexPersonaContent), project, links.text)
			if got := onlyNativeInstructions(t, result); got != string(joinSystemPrompt(parts...)) {
				t.Fatalf("Codex instructions=%q want=%q", got, joinSystemPrompt(parts...))
			}
			if agent.Metadata.Annotations[coremetadata.AnnotationAgentProjectGuidanceDigest] != agentguidance.Digest(project) {
				t.Fatal("missing Codex Project digest")
			}
			if result.leaked(string(project)) {
				t.Fatal("instructions leaked to argv or Registry")
			}
		})
	}
}

func TestProjectGuidanceProcessCreateAndResume(t *testing.T) {
	f := newAgentGuidanceCreate(t)
	project := linkRulesRegistryProject(t, f.store, f.alpha)
	turnAgentGuidanceOff(t, f.planner)
	content := []byte("Process Project\n")
	writeProjectGuidance(t, f.planner, f.alpha, content)
	if _, err := persona.NewDefaultStore(f.paths).Write("reviewer", []byte("Persona\n")); err != nil {
		t.Fatal(err)
	}
	f.create.agents = f.planner
	plan := processAgentCreatePlan{project: project, workspace: coremetadata.AgentWorkspace{CWD: project.Spec.Root}, flags: resourceCreateFlags{persona: "reviewer"}}
	if err := f.create.prepareProcessCreateLaunch(&plan, aiModeClaude); err != nil {
		t.Fatal(err)
	}
	assertPromptContent(t, plan.command.Args, joinSystemPrompt([]byte("Persona\n"), content))
	// Reservation records exactly the Project layer delivered to the provider.
	opts := processAgentCreateOptions{Project: selector.Ref{Kind: coremetadata.KindProject, UID: f.alpha}, Window: selector.Ref{Kind: coremetadata.KindWindow, UID: "win-alpha-review"}, Provider: aiModeClaude, Name: "project-process"}
	plan.window = mustFixtureWindow(t, f.store, "win-alpha-review")
	result, err := f.create.reserveProcessAgent(t.Context(), plan, opts, "operation", "generation")
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := f.store.registry.Agent(result.Binding.Agent)
	if agent.Metadata.Annotations[coremetadata.AnnotationAgentProjectGuidanceDigest] != agentguidance.Digest(content) {
		t.Fatal("process create digest absent")
	}
	// Resume re-reads the owning Project and records the new digest, not the old snapshot.
	command, _, _, _ := newTestAgentResumeCommand(t, f.store, newFakeTmux())
	command.ai = f.planner
	command.resolveWorkspace = func(_ string, _ coremetadata.Registry, _ coremetadata.Project, _ string, cwd string, roots []string) (coremetadata.AgentWorkspace, error) {
		return coremetadata.AgentWorkspace{CWD: cwd, AdditionalWritableRoots: roots}, nil
	}
	changed := []byte("Process Project changed\n")
	writeProjectGuidance(t, f.planner, f.alpha, changed)
	pane, _ := f.store.registry.Pane(result.Binding.Pane)
	candidate := processResumeCandidate{Agent: agent.Clone(), Pane: pane.Clone(), Record: *pane.Status.ProcessSession.Clone()}
	launched, _, settings, err := command.planProcessResume(candidate, processAgentResumeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertPromptContent(t, launched.Args, joinSystemPrompt([]byte("Persona\n"), changed))
	if err := settings.record(&f.store.registry, coremetadata.Mutator{}, agent.Metadata.UID); err != nil {
		t.Fatal(err)
	}
	got, _ := f.store.registry.Agent(agent.Metadata.UID)
	if got.Metadata.Annotations[coremetadata.AnnotationAgentProjectGuidanceDigest] != agentguidance.Digest(changed) {
		t.Fatal("process resume digest stale", got.Metadata.Annotations)
	}
	// A failed read drops only the Project part and preserves its durable digest.
	path, _ := agentguidance.NewDefaultProjectStore(f.paths).Path(f.alpha)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	candidate.Agent = got.Clone()
	fallback, _, unavailable, err := command.planProcessResume(candidate, processAgentResumeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	assertPromptContent(t, fallback.Args, []byte("Persona\n"))
	if !strings.Contains(unavailable.projectGuidance.notice(got.Metadata.Name), projectGuidanceReasonUnavailable) {
		t.Fatal("process read failure had no notice")
	}
	before := maps.Clone(got.Metadata.Annotations)
	if err := unavailable.record(&f.store.registry, coremetadata.Mutator{}, got.Metadata.UID); err != nil {
		t.Fatal(err)
	}
	after, _ := f.store.registry.Agent(got.Metadata.UID)
	if !maps.Equal(before, after.Metadata.Annotations) {
		t.Fatal("failed process read changed recorded digest")
	}

}

func mustFixtureWindow(t *testing.T, store *fakeResourceStore, uid string) coremetadata.Window {
	t.Helper()
	w, ok := store.registry.Window(uid)
	if !ok {
		t.Fatal("missing Window")
	}
	return w.Clone()
}

func TestProjectGuidanceTopologyReplayUsesCurrentOwnerProject(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	turnAgentGuidanceOff(t, planner)
	uid, err := coremetadata.NewUID(coremetadata.KindProject)
	if err != nil {
		t.Fatal(err)
	}
	text := []byte("Replay Project\n")
	writeProjectGuidance(t, planner, uid, text)
	project := coremetadata.Project{Metadata: coremetadata.ObjectMeta{UID: uid, Name: "project"}, Spec: coremetadata.ProjectSpec{Root: t.TempDir()}}
	agent := coremetadata.Agent{Metadata: coremetadata.ObjectMeta{UID: "agent-1", Name: "replay"}, Spec: coremetadata.AgentSpec{Provider: aiModeClaude, Workspace: coremetadata.AgentWorkspace{CWD: project.Spec.Root}}, Status: coremetadata.AgentStatus{SessionRef: claudeConversationRef(personaResumeConversation)}}
	plan := &registryTopologyPlan{}
	replay, ok := planTopologyAgentReplay(plan, project, agent, "main/replay", planner)
	if !ok || len(plan.notices) != 0 {
		t.Fatal("replay refused", plan.notices)
	}
	assertPromptContent(t, execArgvTail(t, replay.argv, aiModeClaude), text)
	reg := coremetadata.NewRegistry()
	reg.Agents = []coremetadata.Agent{agent}
	if err := replay.links.record(&reg, coremetadata.Mutator{}, agent.Metadata.UID); err != nil {
		t.Fatal(err)
	}
	if reg.Agents[0].Metadata.Annotations[coremetadata.AnnotationAgentProjectGuidanceDigest] != agentguidance.Digest(text) {
		t.Fatal("replay record absent")
	}
}

func TestProjectGuidanceResumePickerUsesNewProjectSnapshot(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	turnAgentGuidanceOff(t, planner)
	store := newFakeResourceStore(t)
	alpha, _ := reUIDLinkRulesProjects(t, store)
	text := []byte("Picker Project\n")
	writeProjectGuidance(t, planner, alpha, text)
	tmux := newFakeTmux()
	session := tmux.addSession("alpha")
	seedOwnedSession(session, alpha, "/srv/alpha")
	seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-zsh")
	create, _ := newTestAgentCreateCommand(t, store, tmux)
	launcher := &exactArgvResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), planner: planner}
	create.resumes = &agentGuidanceResumeLauncher{&projectLinksResumeLauncher{launcher}}
	origin := livePaneWithUID(t, tmux, "pan-alpha-zsh")
	withPopupOrigin(create, tmux, popupEnv(origin))
	f := pickerLaunchValuesFixture{canonicalRootFixture: canonicalRootFixture{store: store, tmux: tmux, create: create, originID: origin, windowUID: "win-alpha-main", rootKind: coremetadata.KindProject, rootUID: alpha}, planner: planner, launcher: launcher}
	agent, argv, stderr := f.pick(t, false, aiModeClaude, personaResumeConversation, "")
	if stderr != "" {
		t.Fatal(stderr)
	}
	tail := execArgvTail(t, argv, aiModeClaude)
	assertPromptContent(t, tail, text)
	if !slices.Contains(tail, "--system-prompt-snapshot") || agent.Metadata.Annotations[coremetadata.AnnotationAgentProjectGuidanceDigest] != agentguidance.Digest(text) {
		t.Fatal("picker snapshot contract", tail, agent.Metadata.Annotations)
	}
}

func TestProjectGuidanceUnavailableCreateKeepsPersonaGlobalAndRules(t *testing.T) {
	for _, failure := range []string{"directory", "oversized"} {
		t.Run(failure, func(t *testing.T) {
			f := newAgentGuidanceCreate(t)
			if _, err := persona.NewDefaultStore(f.paths).Write("reviewer", []byte("Persona retained\n")); err != nil {
				t.Fatal(err)
			}
			writeLinkRules(t, f.paths, f.alpha, linkRulesAlpha)
			writeProjectGuidance(t, f.planner, f.alpha, []byte("placeholder"))
			path, _ := agentguidance.NewDefaultProjectStore(f.paths).Path(f.alpha)
			if failure == "directory" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.WriteFile(path, bytes.Repeat([]byte("x"), agentguidance.MaxSize+1), 0600); err != nil {
					t.Fatal(err)
				}
			}
			tail, annotations, stderr := f.run(t, "--persona", "reviewer")
			rules := f.planner.PlanProjectLinks(aiModeClaude, linkRulesRegistryProject(t, f.store, f.alpha), nil).text
			assertPromptContent(t, tail, joinSystemPrompt(agentguidance.Default(), []byte("Persona retained\n"), rules))
			if annotations[coremetadata.AnnotationAgentProjectGuidanceDigest] != "" || strings.Count(stderr, projectGuidanceReasonUnavailable) != 1 {
				t.Fatal("unavailable disclosure", annotations, stderr)
			}
		})
	}
}
