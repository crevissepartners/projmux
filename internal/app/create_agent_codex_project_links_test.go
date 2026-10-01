package app

import (
	"bytes"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentguidance"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// codexLinkRulesMarker is in alpha's rendered rules and nowhere else, so a
// tmux call or a Registry document that leaked them is impossible to miss.
const codexLinkRulesMarker = "jira.alpha.example.com"

// codexLinkRulesAgentLauncher is the Codex create fake whose Codex guidance
// and Codex rules seams are the real planner's, on the create's own home.
type codexLinkRulesAgentLauncher struct {
	*fakeAgentLauncher
	planner *aiCommand
}

func (l *codexLinkRulesAgentLauncher) PlanCodexAgentGuidance() agentGuidanceLaunch {
	return l.planner.PlanCodexAgentGuidance()
}

func (l *codexLinkRulesAgentLauncher) PlanCodexProjectLinks(project coremetadata.Project) projectLinksLaunch {
	return l.planner.PlanCodexProjectLinks(project)
}

// withCodexLinkRulesSeam gives create the real Codex guidance and rules seams
// on its home and returns that home's paths.
func withCodexLinkRulesSeam(t *testing.T, create *createCommand) config.Paths {
	t.Helper()
	planner := agentLaunchArgvTestCommand(t)
	planner.homeDir, planner.lookupEnv = create.homeDir, create.lookupEnv
	create.agents = &codexLinkRulesAgentLauncher{fakeAgentLauncher: create.agents.(*fakeAgentLauncher), planner: planner}
	return linkRulesPaths(t, planner)
}

// codexLinkRulesSetup is what a route hands its prepare step: the home's
// paths, its persona store, and alpha's minted Project UID and the Project
// its rules are rendered with.
type codexLinkRulesSetup struct {
	paths    config.Paths
	personas persona.Store
	alpha    string
	project  projectlinks.Project
}

// codexLinkRulesRoute is one way a Codex fresh create in alpha reaches the
// native lane: the typed `create agent` route or the UI create intent.
type codexLinkRulesRoute struct {
	name string
	run  func(t *testing.T, prepare func(codexLinkRulesSetup), withPersona bool) codexGuidanceResult
}

var codexLinkRulesRoutes = []codexLinkRulesRoute{
	{name: "create agent", run: func(t *testing.T, prepare func(codexLinkRulesSetup), withPersona bool) codexGuidanceResult {
		t.Helper()
		create, store, tmux, native, personas := newCodexPersonaCreate(t)
		alpha, _ := reUIDLinkRulesProjects(t, store)
		paths := withCodexLinkRulesSeam(t, create)
		prepare(codexLinkRulesSetup{paths: paths, personas: personas, alpha: alpha, project: linkRulesProject(t, store, alpha)})
		var flags []string
		if withPersona {
			flags = []string{"--instructions", "reviewer"}
		}
		_, stderr, err := runRoute(t, create, codexNativeCreateArgs(flags...)...)
		if err != nil {
			t.Fatalf("create: stderr=%q err=%v", stderr, err)
		}
		return codexGuidanceResult{creates: native.creates, agent: agentNamed(t, store, "win-alpha-main", "agent-test-1"),
			stderr: stderr, leaked: codexGuidanceLeakCheck(tmux, store)}
	}},
	{name: "create intent", run: func(t *testing.T, prepare func(codexLinkRulesSetup), withPersona bool) codexGuidanceResult {
		t.Helper()
		fx := canonicalFixture(t, false)
		alpha, _ := reUIDLinkRulesProjects(t, fx.store)
		// The live session names its Project by UID, so it moves with it.
		for _, session := range fx.tmux.sessions {
			if session.opts[tmuxopts.ProjectUIDSession] == "prj-alpha" {
				session.opts[tmuxopts.ProjectUIDSession] = alpha
			}
		}
		native := &fakeNativeThreadController{createBinding: codexappserver.ThreadBinding{ThreadID: "thread-intent-links", TurnID: "turn-intent-links"}}
		fx.create.codexNative = native
		fx.create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: &fakeNativePaneLauncher{}}
		personas, _ := personaTestHome(t, fx.create)
		paths := withCodexLinkRulesSeam(t, fx.create)
		prepare(codexLinkRulesSetup{paths: paths, personas: personas, alpha: alpha, project: linkRulesProject(t, fx.store, alpha)})
		flags := resourceCreateFlags{payload: []string{"review this"}}
		if withPersona {
			// The UI create takes instructions only through its profile.
			writeCodexProfile(t, profile.NewDefaultStore(paths), "instructed", "provider = \"codex\"\ninstructions = \"reviewer\"\n")
			flags.profile = "instructed"
		}
		intent := agentPaneIntent{producer: canonicalProducerDirectProvider, provider: aiModeCodex, placement: "right", anchorPaneID: fx.originID}
		scope, err := fx.create.resolveCanonicalIntentScope(intent)
		if err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if _, err := fx.create.createCanonicalIntentAgent(scope, intent, aiModeCodex, "", flags, &stdout, &stderr); err != nil {
			t.Fatalf("create: stderr=%q err=%v", stderr.String(), err)
		}
		agents := fx.store.registry.AgentsOf(fx.windowUID)
		return codexGuidanceResult{creates: native.creates, agent: agents[len(agents)-1],
			stderr: stderr.String(), leaked: codexGuidanceLeakCheck(fx.tmux, fx.store)}
	}},
}

// codexLinkRulesNoticeLines are the stderr lines that disclose the rules.
func codexLinkRulesNoticeLines(stderr string) []string {
	var lines []string
	for line := range strings.Lines(stderr) {
		if strings.Contains(line, projectLinksReasonUnavailable) {
			lines = append(lines, line)
		}
	}
	return lines
}

// codexLinkRulesCase names one guidance x instructions combination.
type codexLinkRulesCase struct {
	name                  string
	guidance, withPersona bool
}

var codexLinkRulesCases = []codexLinkRulesCase{
	{name: "guidance", guidance: true},
	{name: "guidance and instructions", guidance: true, withPersona: true},
	{name: "rules alone"},
	{name: "instructions", withPersona: true},
}

// codexInstructionsWithout is the developer instructions a create of test
// sends without rules: the guidance and the identity of agentUID, then the
// instructions.
func codexInstructionsWithout(test codexLinkRulesCase, agentUID string) [][]byte {
	var parts [][]byte
	if test.guidance {
		parts = append(parts, agentguidance.Default(), []byte(codexAgentIdentity(agentUID)))
	}
	if test.withPersona {
		parts = append(parts, []byte(codexPersonaContent))
	}
	return parts
}

func prepareCodexLinkRulesCase(t *testing.T, test codexLinkRulesCase, setup codexLinkRulesSetup) {
	t.Helper()
	if !test.guidance {
		writeAgentGuidance(t, setup.paths, []byte(" \n"))
	}
	if test.withPersona {
		writeCodexGuidancePersona(t, setup.personas)
	}
}

// TestCreateCodexAgentInAProjectWithLinkRulesSendsThemLastInTheDeveloperInstructionsAndRecordsTheDigest
// is the Codex half of the rules: a Codex fresh create starts its thread with
// its Project's rendered rules after the guidance and the instructions, each
// joined by the separator a Claude composite uses, byte for byte the rules
// snapshot, and the Agent records the rules digest. The rules reach the
// app-server and nothing else.
func TestCreateCodexAgentInAProjectWithLinkRulesSendsThemLastInTheDeveloperInstructionsAndRecordsTheDigest(t *testing.T) {
	for _, route := range codexLinkRulesRoutes {
		for _, test := range codexLinkRulesCases {
			t.Run(route.name+"/"+test.name, func(t *testing.T) {
				var snapshotPath, digest string
				result := route.run(t, func(setup codexLinkRulesSetup) {
					prepareCodexLinkRulesCase(t, test, setup)
					writeLinkRules(t, setup.paths, setup.alpha, linkRulesAlpha)
					snapshotPath, digest = linkRulesSnapshot(t, setup.paths, linkRulesAlpha, setup.project)
				}, test.withPersona)
				rules, err := os.ReadFile(snapshotPath)
				if err != nil || len(rules) == 0 || !bytes.Contains(rules, []byte(codexLinkRulesMarker)) {
					t.Fatalf("rules snapshot %s = %q, %v", snapshotPath, rules, err)
				}
				want := string(joinSystemPrompt(append(codexInstructionsWithout(test, result.agent.Metadata.UID), rules)...))
				if got := onlyNativeInstructions(t, result); got != want {
					t.Fatalf("developer instructions = %q, want %q", got, want)
				}
				annotations := result.agent.Metadata.Annotations
				if got := annotations[coremetadata.AnnotationAgentProjectLinkRulesDigest]; got != digest {
					t.Fatalf("Agent annotations = %v, want rules digest %s", annotations, digest)
				}
				if _, ok := annotations[coremetadata.AnnotationAgentGuidanceDigest]; ok != test.guidance {
					t.Fatalf("Agent annotations = %v, guidance digest recorded = %v, want %v", annotations, ok, test.guidance)
				}
				if result.leaked(codexLinkRulesMarker) {
					t.Fatal("rules text reached a tmux call or the Registry")
				}
				if lines := codexLinkRulesNoticeLines(result.stderr); len(lines) != 0 {
					t.Fatalf("stderr = %q, want no rules notice", result.stderr)
				}
			})
		}
	}
}

// TestCreateCodexAgentInAProjectWithoutLinkRulesSendsWhatItSentBefore pins
// the invariant: a Codex fresh create in a Project without rules sends the
// developer instructions and records the annotations it did before the rules
// reached Codex.
func TestCreateCodexAgentInAProjectWithoutLinkRulesSendsWhatItSentBefore(t *testing.T) {
	for _, route := range codexLinkRulesRoutes {
		for _, test := range codexLinkRulesCases {
			t.Run(route.name+"/"+test.name, func(t *testing.T) {
				result := route.run(t, func(setup codexLinkRulesSetup) {
					prepareCodexLinkRulesCase(t, test, setup)
				}, test.withPersona)
				want := string(joinSystemPrompt(codexInstructionsWithout(test, result.agent.Metadata.UID)...))
				if got := onlyNativeInstructions(t, result); got != want {
					t.Fatalf("developer instructions = %q, want %q", got, want)
				}
				annotations := result.agent.Metadata.Annotations
				if _, ok := annotations[coremetadata.AnnotationAgentProjectLinkRulesDigest]; ok {
					t.Fatalf("Agent annotations = %v, want no rules digest", annotations)
				}
				if route.name == "create agent" {
					var wantAnnotations map[string]string
					if test.guidance {
						wantAnnotations = map[string]string{coremetadata.AnnotationAgentGuidanceDigest: agentguidance.Digest(agentguidance.Default())}
					}
					if test.withPersona {
						if wantAnnotations == nil {
							wantAnnotations = map[string]string{}
						}
						wantAnnotations[coremetadata.AnnotationAgentPersona] = "reviewer"
						wantAnnotations[coremetadata.AnnotationAgentPersonaDigest] = persona.Digest([]byte(codexPersonaContent))
						wantAnnotations[coremetadata.AnnotationAgentInstructionsSource] = coremetadata.SettingSourceFlag
					}
					if !maps.Equal(annotations, wantAnnotations) || (wantAnnotations == nil) != (annotations == nil) {
						t.Fatalf("Agent annotations = %#v, want %#v", annotations, wantAnnotations)
					}
				}
				if lines := codexLinkRulesNoticeLines(result.stderr); len(lines) != 0 {
					t.Fatalf("stderr = %q, want no rules notice", result.stderr)
				}
			})
		}
	}
}

// unreadableCodexLinkRules are rule files a launch cannot use: content that
// is not valid JSON, and a directory where the file belongs.
var unreadableCodexLinkRules = map[string]func(t *testing.T, paths config.Paths, uid string){
	"invalid json": func(t *testing.T, paths config.Paths, uid string) {
		writeLinkRules(t, paths, uid, linkRulesAlpha)
		path, err := projectlinks.NewDefaultStore(paths).Path(uid)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatal(err)
		}
	},
	"directory": func(t *testing.T, paths config.Paths, uid string) {
		path, err := projectlinks.NewDefaultStore(paths).Path(uid)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	},
}

// TestUnreadableCodexProjectLinkRulesCreateWithoutThemWithOneNoticeAndRecordNothing
// covers a rule file that cannot be read: the Codex create still succeeds,
// its thread starts with the guidance and the instructions alone, one notice
// line names the reason, and no rules digest is recorded.
func TestUnreadableCodexProjectLinkRulesCreateWithoutThemWithOneNoticeAndRecordNothing(t *testing.T) {
	for breakName, breakFile := range unreadableCodexLinkRules {
		for _, route := range codexLinkRulesRoutes {
			for _, test := range []codexLinkRulesCase{codexLinkRulesCases[0], codexLinkRulesCases[1]} {
				t.Run(breakName+"/"+route.name+"/"+test.name, func(t *testing.T) {
					result := route.run(t, func(setup codexLinkRulesSetup) {
						prepareCodexLinkRulesCase(t, test, setup)
						breakFile(t, setup.paths, setup.alpha)
					}, test.withPersona)
					want := string(joinSystemPrompt(codexInstructionsWithout(test, result.agent.Metadata.UID)...))
					if got := onlyNativeInstructions(t, result); got != want {
						t.Fatalf("developer instructions = %q, want %q", got, want)
					}
					wantNotice := "agent/" + result.agent.Metadata.Name + " launched without its Project's label link rules (" + projectLinksReasonUnavailable + "): "
					if lines := codexLinkRulesNoticeLines(result.stderr); len(lines) != 1 || !strings.Contains(lines[0], wantNotice) {
						t.Fatalf("stderr = %q, want one line carrying %q", result.stderr, wantNotice)
					}
					if _, ok := result.agent.Metadata.Annotations[coremetadata.AnnotationAgentProjectLinkRulesDigest]; ok {
						t.Fatalf("Agent annotations = %v, want no rules digest", result.agent.Metadata.Annotations)
					}
				})
			}
		}
	}
}

// TestCodexCreatesOffTheNativeFreshLaneGetNoProjectLinkRules pins that a
// Codex create without a prompt, or with --interactive-only, starts no thread
// of its own, so it plans no rules and records no digest even though its
// Project has rules.
func TestCodexCreatesOffTheNativeFreshLaneGetNoProjectLinkRules(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "no prompt", args: []string{"agent", "--provider", "codex", "--project", "alpha", "--window", "main"}},
		{name: "interactive-only", args: []string{"agent", "--provider", "codex", "--interactive-only",
			"--project", "alpha", "--window", "main", "--", "review this"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			create, store, tmux, native, _ := newCodexPersonaCreate(t)
			alpha, _ := reUIDLinkRulesProjects(t, store)
			paths := withCodexLinkRulesSeam(t, create)
			writeLinkRules(t, paths, alpha, linkRulesAlpha)
			_, stderr, err := runRoute(t, create, test.args...)
			if err != nil {
				t.Fatalf("create: stderr=%q err=%v", stderr, err)
			}
			if len(native.creates) != 0 {
				t.Fatalf("native creates = %+v, want none", native.creates)
			}
			agent := agentNamed(t, store, "win-alpha-main", "agent-test-1")
			if _, ok := agent.Metadata.Annotations[coremetadata.AnnotationAgentProjectLinkRulesDigest]; ok {
				t.Fatalf("Agent annotations = %v, want no rules digest", agent.Metadata.Annotations)
			}
			if codexGuidanceLeakCheck(tmux, store)(codexLinkRulesMarker) || len(codexLinkRulesNoticeLines(stderr)) != 0 {
				t.Fatalf("rules reached the create: stderr=%q", stderr)
			}
		})
	}

	// The same lanes, prepared directly, plan nothing.
	planner := agentLaunchArgvTestCommand(t)
	store := newFakeResourceStore(t)
	alpha, _ := reUIDLinkRulesProjects(t, store)
	writeLinkRules(t, linkRulesPaths(t, planner), alpha, linkRulesAlpha)
	create := &createCommand{agents: &codexLinkRulesAgentLauncher{fakeAgentLauncher: newFakeAgentLauncher(), planner: planner}}
	for name, flags := range map[string]resourceCreateFlags{
		"no prompt":        {},
		"interactive-only": {interactiveOnly: true, payload: []string{"review this"}},
		"resume picker":    {resumeConversation: "conversation-1", payload: []string{"review this"}},
	} {
		create.prepareProjectLinks(aiModeCodex, linkRulesRegistryProject(t, store, alpha), &flags)
		if flags.projectLinks.active || flags.resumeLaunchValues != nil {
			t.Fatalf("%s: prepared project link rules %+v", name, flags.projectLinks)
		}
	}
	fresh := resourceCreateFlags{payload: []string{"review this"}}
	create.prepareProjectLinks(aiModeCodex, linkRulesRegistryProject(t, store, alpha), &fresh)
	if !fresh.projectLinks.active || fresh.projectLinks.digest == "" || fresh.projectLinks.systemPromptFile != "" {
		t.Fatalf("codex fresh create prepared %+v, want the rules and no file", fresh.projectLinks)
	}
}

// TestCodexResumeNeverPlansProjectLinkRules pins that only a Codex fresh
// create asks for the rules: the Claude seam every resume, relaunch and
// replay reads stays inactive for Codex, a Project UID that cannot name a
// rule file plans nothing, and a Codex resume with a recorded digest launches
// the argv it had without one.
func TestCodexResumeNeverPlansProjectLinkRules(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	paths := linkRulesPaths(t, planner)
	store := newFakeResourceStore(t)
	alpha, _ := reUIDLinkRulesProjects(t, store)
	writeLinkRules(t, paths, alpha, linkRulesAlpha)
	project := linkRulesRegistryProject(t, store, alpha)
	_, digest := linkRulesSnapshot(t, paths, linkRulesAlpha, linkRulesProject(t, store, alpha))

	if links := planner.PlanProjectLinks(aiModeCodex, project, nil); links.active {
		t.Fatalf("codex planned project link rules through the Claude seam: %+v", links)
	}
	if links := planner.PlanCodexProjectLinks(project); !links.active || links.digest != digest || links.recorded != "" {
		t.Fatalf("codex fresh create rules = %+v, want digest %s", links, digest)
	}
	unshaped := project.Clone()
	unshaped.Metadata.UID = "prj-alpha"
	if links := planner.PlanCodexProjectLinks(unshaped); links.active {
		t.Fatalf("a Project UID that names no rule file planned %+v", links)
	}

	workspace := coremetadata.AgentWorkspace{CWD: "/work/owner"}
	before, err := planner.PlanAgentResume(aiModeCodex, workspace, resumeFixtureConversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, err := planner.PlanAgentResume(aiModeCodex, workspace, resumeFixtureConversation,
		map[string]string{coremetadata.AnnotationAgentProjectLinkRulesDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(after.argv, before.argv) || after.projectLinksUnavailable != nil {
		t.Fatalf("codex resume argv with a rules digest = %q, want %q", after.argv, before.argv)
	}
}

// TestProjectLinksLaunchDeveloperInstructionsAppendsTheRulesAfterThePersona
// is the composition table: rules go after the persona, separated by the
// composite separator, and a launch without usable rules returns the persona
// itself.
func TestProjectLinksLaunchDeveloperInstructionsAppendsTheRulesAfterThePersona(t *testing.T) {
	t.Parallel()
	rules := []byte("RULES\n")
	usable := projectLinksLaunch{active: true, digest: "sha256-rules", text: rules}
	unavailable := usable
	unavailable.unavailable = os.ErrPermission
	for _, test := range []struct {
		name    string
		launch  projectLinksLaunch
		persona string
		want    string
	}{
		{name: "inactive", launch: projectLinksLaunch{text: rules, digest: "sha256-rules"}, persona: "PERSONA", want: "PERSONA"},
		{name: "unavailable", launch: unavailable, persona: "PERSONA", want: "PERSONA"},
		{name: "no rules", launch: projectLinksLaunch{active: true}, persona: "PERSONA", want: "PERSONA"},
		{name: "no rules and no persona", launch: projectLinksLaunch{active: true}, want: ""},
		{name: "rules without persona", launch: usable, want: "RULES\n"},
		{name: "rules after persona", launch: usable, persona: "PERSONA", want: "PERSONA" + projectlinks.CompositeSeparator + "RULES\n"},
	} {
		if got := test.launch.developerInstructions(test.persona); got != test.want {
			t.Errorf("%s: developerInstructions(%q) = %q, want %q", test.name, test.persona, got, test.want)
		}
	}
}
