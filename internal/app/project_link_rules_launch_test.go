package app

import (
	"bytes"
	"encoding/json"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/projectlinks"
)

// The two Projects of the label link rules fixtures. Each has its own rules,
// so an Agent that got the other Project's rules is visible in its argv.
var (
	linkRulesAlpha = projectlinks.Rules{
		Jira:  []string{"https://jira.alpha.example.com"},
		Links: []projectlinks.Link{{LabelKey: "jira", Template: "{jira}/browse/{value}"}},
	}
	linkRulesBeta = projectlinks.Rules{
		Repo:  []string{"https://github.com/example/beta"},
		Links: []projectlinks.Link{{LabelKey: "pr", Template: "{repo}/pull/{value}"}},
	}
	linkRulesBetaChanged = projectlinks.Rules{
		Repo: []string{"https://github.com/example/beta"},
		Links: []projectlinks.Link{
			{LabelKey: "pr", Template: "{repo}/pull/{value}"},
			{LabelKey: "issue", Template: "{repo}/issues/{value}"},
		},
	}
)

// projectLinksAgentLauncher is the create fake with the production rules
// seam of planner.
type projectLinksAgentLauncher struct {
	*fakeAgentLauncher
	planner *aiCommand
}

func (l *projectLinksAgentLauncher) PlanProjectLinks(provider string, project coremetadata.Project, recorded map[string]string) projectLinksLaunch {
	return l.planner.PlanProjectLinks(provider, project, recorded)
}

// projectLinksResumeLauncher is the exact-argv resume recorder with the
// production rules seam of its planner.
type projectLinksResumeLauncher struct {
	*exactArgvResumeLauncher
}

func (l *projectLinksResumeLauncher) PlanProjectLinks(provider string, project coremetadata.Project, recorded map[string]string) projectLinksLaunch {
	return l.planner.PlanProjectLinks(provider, project, recorded)
}

// linkRulesPaths are the projmux paths planner's own home resolves to.
func linkRulesPaths(t *testing.T, planner *aiCommand) config.Paths {
	t.Helper()
	paths, err := configPaths(planner.homeDir, planner.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// reUIDLinkRulesProjects gives the fixture's alpha and beta Projects minted
// Project UIDs -- the only shape a rules file can be stored under -- and
// returns them. Every reference moves with the UID.
func reUIDLinkRulesProjects(t *testing.T, store *fakeResourceStore) (alpha, beta string) {
	t.Helper()
	var err error
	for _, target := range []*string{&alpha, &beta} {
		if *target, err = coremetadata.NewUID(coremetadata.KindProject); err != nil {
			t.Fatal(err)
		}
	}
	reUIDLinkRulesProjectsTo(t, store, alpha, beta)
	return alpha, beta
}

// reUIDLinkRulesProjectsTo is reUIDLinkRulesProjects onto given UIDs, so a
// second fresh fixture can share the first one's rules files.
func reUIDLinkRulesProjectsTo(t *testing.T, store *fakeResourceStore, alpha, beta string) {
	t.Helper()
	raw, err := json.Marshal(store.registry)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(raw), `"prj-alpha"`, `"`+alpha+`"`)
	text = strings.ReplaceAll(text, `"prj-beta"`, `"`+beta+`"`)
	var registry coremetadata.Registry
	if err := json.Unmarshal([]byte(text), &registry); err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("re-UID'd fixture is not a valid registry: %v", err)
	}
	store.registry = registry
}

// writeLinkRules stores rules as projectUID's rules file.
func writeLinkRules(t *testing.T, paths config.Paths, projectUID string, rules projectlinks.Rules) {
	t.Helper()
	if err := projectlinks.NewDefaultStore(paths).Write(projectUID, rules); err != nil {
		t.Fatal(err)
	}
}

// linkRulesRegistryProject is the Registry Project uid in store.
func linkRulesRegistryProject(t *testing.T, store *fakeResourceStore, uid string) coremetadata.Project {
	t.Helper()
	project, ok := store.registry.Project(uid)
	if !ok {
		t.Fatalf("no project %q in the fixture registry", uid)
	}
	return project.Clone()
}

// linkRulesProject is what the rules of the Registry Project uid in store are
// rendered with.
func linkRulesProject(t *testing.T, store *fakeResourceStore, uid string) projectlinks.Project {
	t.Helper()
	return projectlinks.ProjectOf(linkRulesRegistryProject(t, store, uid))
}

// linkRulesSnapshot is the rules snapshot path and digest a launch with rules
// is expected to use.
func linkRulesSnapshot(t *testing.T, paths config.Paths, rules projectlinks.Rules, project projectlinks.Project) (string, string) {
	t.Helper()
	digest := projectlinks.Digest(projectlinks.Render(rules, project))
	path, err := projectlinks.NewDefaultSnapshotStore(paths).SnapshotPath(digest)
	if err != nil {
		t.Fatal(err)
	}
	return path, digest
}

// linkRulesComposite is the composite bytes a persona and rules make.
func linkRulesComposite(personaContent []byte, rules projectlinks.Rules, project projectlinks.Project) []byte {
	out := append([]byte{}, personaContent...)
	out = append(out, projectlinks.CompositeSeparator...)
	return append(out, projectlinks.Render(rules, project)...)
}

// newLinkRulesCreate is `create agent` over the re-UID'd fixture, with the
// create's persona home and the rules seam both on planner's home.
func newLinkRulesCreate(t *testing.T) (*createCommand, *projectLinksAgentLauncher, *fakeResourceStore, *aiCommand, string, string) {
	t.Helper()
	store := newFakeResourceStore(t)
	alpha, beta := reUIDLinkRulesProjects(t, store)
	create, fake := newTestAgentCreateCommand(t, store, newFakeTmux())
	planner := agentLaunchArgvTestCommand(t)
	create.homeDir, create.lookupEnv = planner.homeDir, planner.lookupEnv
	launcher := &projectLinksAgentLauncher{fakeAgentLauncher: fake, planner: planner}
	create.agents = launcher
	return create, launcher, store, planner, alpha, beta
}

// resumeClaudeAgentWithLinkRules runs `agent resume` on the fixture's beta
// Agent as a Claude Agent carrying annotations, through the real resume seam
// and rules seam of planner, and returns the launched argv and stderr.
func resumeClaudeAgentWithLinkRules(t *testing.T, planner *aiCommand, store *fakeResourceStore, annotations map[string]string, flags ...string) ([]string, string) {
	t.Helper()
	target, _ := store.registry.Agent("agt-beta-codex")
	target.Spec.Provider = aiModeClaude
	target.Status.SessionRef = claudeConversationRef(personaResumeConversation)
	target.Metadata.Annotations = maps.Clone(annotations)

	tmux := newFakeTmux()
	command, recorder, _, _ := newTestAgentResumeCommand(t, store, tmux)
	launcher := &projectLinksResumeLauncher{&exactArgvResumeLauncher{fakeResumeLauncher: recorder, planner: planner}}
	command.rebind.launcher = launcher

	args := append([]string{"resume", "uid:agt-beta-codex"}, flags...)
	stdout, stderr, err := runRoute(t, command, args...)
	if err != nil || !strings.Contains(stdout, "resumed") {
		t.Fatalf("resume %v: stdout=%q stderr=%q err=%v", flags, stdout, stderr, err)
	}
	if len(launcher.argv) != 1 {
		t.Fatalf("planned %d provider argv values, want 1", len(launcher.argv))
	}
	calls := splitWindowCalls(tmux)
	separator := -1
	if len(calls) == 1 {
		separator = slices.Index(calls[0], "--")
	}
	if separator < 0 || !slices.Equal(calls[0][separator+1:], launcher.argv[0]) {
		t.Fatalf("launched child argv = %q, want exact %q", calls, launcher.argv[0])
	}
	return launcher.argv[0], stderr
}

// systemPromptFileOf returns the one --append-system-prompt-file of a Claude
// exec tail, failing when the flag appears more than once: Claude keeps only
// the last one.
func systemPromptFileOf(t *testing.T, tail []string) string {
	t.Helper()
	file := ""
	count := 0
	for i, arg := range tail {
		if arg == "--append-system-prompt-file" && i+1 < len(tail) {
			file = tail[i+1]
			count++
		}
	}
	if count > 1 {
		t.Fatalf("exec tail %q passes --append-system-prompt-file %d times; Claude keeps only the last", tail, count)
	}
	return file
}

func recordedAgentAnnotations(store *fakeResourceStore, uid string) map[string]string {
	agent, _ := store.registry.Agent(uid)
	return agent.Metadata.Annotations
}

// TestCreateClaudeAgentWithProjectLinkRulesLaunchesTheRulesSnapshotAndRecordsTheDigest
// is the rules-only create: the launch is given the rules snapshot and the
// Agent records exactly its digest, with no snapshot mode.
func TestCreateClaudeAgentWithProjectLinkRulesLaunchesTheRulesSnapshotAndRecordsTheDigest(t *testing.T) {
	t.Parallel()
	create, launcher, store, planner, alpha, beta := newLinkRulesCreate(t)
	paths := linkRulesPaths(t, planner)
	writeLinkRules(t, paths, alpha, linkRulesAlpha)
	writeLinkRules(t, paths, beta, linkRulesBeta)

	if _, stderr, err := runRoute(t, create,
		"agent", "--provider", "claude", "--project", "alpha", "--window", "review", "--", "review this"); err != nil || strings.Contains(stderr, projectLinksReasonUnavailable) {
		t.Fatalf("create: stderr=%q err=%v", stderr, err)
	}
	wantPath, digest := linkRulesSnapshot(t, paths, linkRulesAlpha, linkRulesProject(t, store, alpha))
	if len(launcher.plans) != 1 || launcher.plans[0].personaFile != wantPath {
		t.Fatalf("plans = %+v, want the rules snapshot %s", launcher.plans, wantPath)
	}
	if content, err := os.ReadFile(wantPath); err != nil || !bytes.Equal(content, projectlinks.Render(linkRulesAlpha, linkRulesProject(t, store, alpha))) {
		t.Fatalf("rules snapshot = %q, %v", content, err)
	}
	agent := agentNamed(t, store, "win-alpha-review", "agent-test-1")
	if want := map[string]string{coremetadata.AnnotationAgentProjectLinkRulesDigest: digest}; !maps.Equal(agent.Metadata.Annotations, want) {
		t.Fatalf("Agent annotations = %v, want %v", agent.Metadata.Annotations, want)
	}
}

// TestCreateClaudeAgentWithPersonaAndProjectLinkRulesLaunchesOneCompositeFile
// pins the measured Claude rule that only the last --append-system-prompt-file
// counts: persona and rules reach it as one composite file (persona bytes,
// separator, rules), while the persona snapshot, digest and annotations stay
// exactly what a persona-only create writes.
func TestCreateClaudeAgentWithPersonaAndProjectLinkRulesLaunchesOneCompositeFile(t *testing.T) {
	t.Parallel()
	create, launcher, store, planner, alpha, _ := newLinkRulesCreate(t)
	paths := linkRulesPaths(t, planner)
	writeLinkRules(t, paths, alpha, linkRulesAlpha)
	content := []byte("PERSONA-BODY-MARKER: you review diffs tersely.\n")
	personas := persona.NewDefaultStore(paths)
	if _, err := personas.Write("reviewer", content); err != nil {
		t.Fatal(err)
	}

	if _, stderr, err := runRoute(t, create,
		"agent", "--provider", "claude", "--persona", "reviewer",
		"--project", "alpha", "--window", "review", "--", "review this"); err != nil || strings.Contains(stderr, projectLinksReasonUnavailable) {
		t.Fatalf("create: stderr=%q err=%v", stderr, err)
	}
	if len(launcher.plans) != 1 {
		t.Fatalf("plans = %+v", launcher.plans)
	}
	composite := launcher.plans[0].personaFile
	wantComposite := linkRulesComposite(content, linkRulesAlpha, linkRulesProject(t, store, alpha))
	if got, err := os.ReadFile(composite); err != nil || !bytes.Equal(got, wantComposite) {
		t.Fatalf("composite %s = %q, %v; want %q", composite, got, err, wantComposite)
	}
	personaDigest := persona.Digest(content)
	personaPath, err := personas.SnapshotPath(personaDigest)
	if err != nil {
		t.Fatal(err)
	}
	if composite == personaPath {
		t.Fatal("the launch was given the persona snapshot, not the composite")
	}
	if got, err := os.ReadFile(personaPath); err != nil || !bytes.Equal(got, content) {
		t.Fatalf("persona snapshot = %q, %v; want the untouched persona bytes", got, err)
	}
	_, digest := linkRulesSnapshot(t, paths, linkRulesAlpha, linkRulesProject(t, store, alpha))
	agent := agentNamed(t, store, "win-alpha-review", "agent-test-1")
	want := map[string]string{
		coremetadata.AnnotationAgentPersona:                "reviewer",
		coremetadata.AnnotationAgentPersonaDigest:          personaDigest,
		coremetadata.AnnotationAgentProjectLinkRulesDigest: digest,
		coremetadata.AnnotationAgentInstructionsSource:     coremetadata.SettingSourceFlag,
	}
	if !maps.Equal(agent.Metadata.Annotations, want) {
		t.Fatalf("Agent annotations = %v, want %v", agent.Metadata.Annotations, want)
	}

	// The real launcher spells the composite once, before the workspace.
	_, argv, err := planner.PlanAgentLaunchWithOptions(aiModeClaude,
		coremetadata.AgentWorkspace{CWD: "/work/owner", AdditionalWritableRoots: []string{"/work/extra"}}, []string{"go"}, "", "", composite)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := execArgvTail(t, argv, aiModeClaude), []string{"--append-system-prompt-file", composite, "--add-dir", "/work/extra", "--", "go"}; !slices.Equal(got, want) {
		t.Fatalf("argv tail = %q, want %q", got, want)
	}
}

// TestCreateInAProjectWithoutProjectLinkRulesIsUnchanged pins the invariant:
// a Project without rules creates with the argv and annotations of before.
func TestCreateInAProjectWithoutProjectLinkRulesIsUnchanged(t *testing.T) {
	t.Parallel()
	create, launcher, store, _, _, _ := newLinkRulesCreate(t)
	if _, stderr, err := runRoute(t, create,
		"agent", "--provider", "claude", "--project", "alpha", "--window", "review", "--", "review this"); err != nil || strings.Contains(stderr, projectLinksReasonUnavailable) {
		t.Fatalf("create: stderr=%q err=%v", stderr, err)
	}
	if len(launcher.plans) != 1 || launcher.plans[0].personaFile != "" {
		t.Fatalf("plans = %+v, want no system prompt file", launcher.plans)
	}
	if agent := agentNamed(t, store, "win-alpha-review", "agent-test-1"); agent.Metadata.Annotations != nil {
		t.Fatalf("Agent annotations = %v, want none", agent.Metadata.Annotations)
	}
}

// TestResumeWithChangedProjectLinkRulesPassesTheNewFileAndTurnsTheSnapshotOff
// covers rules added after the conversation started and then changed: each
// resume passes the current rules with --system-prompt-snapshot off, and the
// same transaction records exactly the digest that launch used, with the
// sticky snapshot mode off.
func TestResumeWithChangedProjectLinkRulesPassesTheNewFileAndTurnsTheSnapshotOff(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	paths := linkRulesPaths(t, planner)
	store := newFakeResourceStore(t)
	alpha, beta := reUIDLinkRulesProjects(t, store)
	writeLinkRules(t, paths, beta, linkRulesBeta)
	topic := map[string]string{coremetadata.AnnotationAgentTopic: "review"}

	argv, stderr := resumeClaudeAgentWithLinkRules(t, planner, store, topic)
	path, digest := linkRulesSnapshot(t, paths, linkRulesBeta, linkRulesProject(t, store, beta))
	want := []string{"--append-system-prompt-file", path, "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	tail := execArgvTail(t, argv, aiModeClaude)
	if !slices.Equal(tail, want) || stderr != "" {
		t.Fatalf("added rules: exec argv tail = %q stderr = %q, want %q", tail, stderr, want)
	}
	recorded := recordedAgentAnnotations(store, "agt-beta-codex")
	wantRecorded := map[string]string{
		coremetadata.AnnotationAgentTopic:                  "review",
		coremetadata.AnnotationAgentProjectLinkRulesDigest: digest,
		coremetadata.AnnotationAgentSystemPromptSnapshot:   coremetadata.SystemPromptSnapshotOff,
	}
	if !maps.Equal(recorded, wantRecorded) {
		t.Fatalf("added rules: recorded %v, want %v", recorded, wantRecorded)
	}
	assertRecordedEqualsLaunched(t, systemPromptFileOf(t, tail), recorded)

	// The next resume is of an Offline Agent recording what this one did.
	writeLinkRules(t, paths, beta, linkRulesBetaChanged)
	store = newFakeResourceStore(t)
	reUIDLinkRulesProjectsTo(t, store, alpha, beta)
	argv, _ = resumeClaudeAgentWithLinkRules(t, planner, store, recorded)
	changedPath, changedDigest := linkRulesSnapshot(t, paths, linkRulesBetaChanged, linkRulesProject(t, store, beta))
	want = []string{"--append-system-prompt-file", changedPath, "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if tail = execArgvTail(t, argv, aiModeClaude); !slices.Equal(tail, want) {
		t.Fatalf("changed rules: exec argv tail = %q, want %q", tail, want)
	}
	recorded = recordedAgentAnnotations(store, "agt-beta-codex")
	wantRecorded[coremetadata.AnnotationAgentProjectLinkRulesDigest] = changedDigest
	if !maps.Equal(recorded, wantRecorded) {
		t.Fatalf("changed rules: recorded %v, want %v", recorded, wantRecorded)
	}
	assertRecordedEqualsLaunched(t, systemPromptFileOf(t, tail), recorded)
}

// assertRecordedEqualsLaunched proves the digest the transaction recorded is
// the digest of the file the launch passed.
func assertRecordedEqualsLaunched(t *testing.T, launchedFile string, recorded map[string]string) {
	t.Helper()
	content, err := os.ReadFile(launchedFile)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := recorded[coremetadata.AnnotationAgentProjectLinkRulesDigest], projectlinks.Digest(content); got != want {
		t.Fatalf("recorded digest %q, launched file %s digests %q", got, launchedFile, want)
	}
}

// TestResumeWithUnchangedProjectLinkRulesKeepsTheSnapshotMode pins that rules
// equal to the recorded digest pass the same file and change nothing else:
// no snapshot mode is added and nothing is recorded.
func TestResumeWithUnchangedProjectLinkRulesKeepsTheSnapshotMode(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	paths := linkRulesPaths(t, planner)
	store := newFakeResourceStore(t)
	_, beta := reUIDLinkRulesProjects(t, store)
	writeLinkRules(t, paths, beta, linkRulesBeta)
	path, digest := linkRulesSnapshot(t, paths, linkRulesBeta, linkRulesProject(t, store, beta))
	annotations := map[string]string{coremetadata.AnnotationAgentProjectLinkRulesDigest: digest}

	argv, stderr := resumeClaudeAgentWithLinkRules(t, planner, store, annotations)
	want := []string{"--append-system-prompt-file", path, "--resume", personaResumeConversation}
	if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, want) || stderr != "" {
		t.Fatalf("exec argv tail = %q stderr = %q, want %q", got, stderr, want)
	}
	if recorded := recordedAgentAnnotations(store, "agt-beta-codex"); !maps.Equal(recorded, annotations) {
		t.Fatalf("recorded %v, want unchanged %v", recorded, annotations)
	}
}

// TestResumeWithRemovedProjectLinkRulesPassesNoFileAndRemovesTheDigest pins
// rules removed after the Agent recorded some: no file, the snapshot off so
// the recorded prompt with the old rules is not replayed, and the digest
// annotation removed.
func TestResumeWithRemovedProjectLinkRulesPassesNoFileAndRemovesTheDigest(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	store := newFakeResourceStore(t)
	_, beta := reUIDLinkRulesProjects(t, store)
	annotations := map[string]string{coremetadata.AnnotationAgentProjectLinkRulesDigest: projectlinks.Digest(projectlinks.Render(linkRulesBeta, linkRulesProject(t, store, beta)))}

	argv, stderr := resumeClaudeAgentWithLinkRules(t, planner, store, annotations)
	want := []string{"--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, want) || stderr != "" {
		t.Fatalf("exec argv tail = %q stderr = %q, want %q", got, stderr, want)
	}
	wantRecorded := map[string]string{coremetadata.AnnotationAgentSystemPromptSnapshot: coremetadata.SystemPromptSnapshotOff}
	if recorded := recordedAgentAnnotations(store, "agt-beta-codex"); !maps.Equal(recorded, wantRecorded) {
		t.Fatalf("recorded %v, want %v", recorded, wantRecorded)
	}
}

// TestCorruptProjectLinkRulesLaunchWithoutThemWithOneNoticeAndRecordNothing
// pins the failure rule on resume and create: the Agent still launches,
// without rules, one notice names the reason, and nothing is recorded.
func TestCorruptProjectLinkRulesLaunchWithoutThemWithOneNoticeAndRecordNothing(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	paths := linkRulesPaths(t, planner)
	store := newFakeResourceStore(t)
	_, beta := reUIDLinkRulesProjects(t, store)
	writeLinkRules(t, paths, beta, linkRulesBeta)
	_, digest := linkRulesSnapshot(t, paths, linkRulesBeta, linkRulesProject(t, store, beta))
	rulesFile, err := projectlinks.NewDefaultStore(paths).Path(beta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rulesFile, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	annotations := map[string]string{coremetadata.AnnotationAgentProjectLinkRulesDigest: digest}

	argv, stderr := resumeClaudeAgentWithLinkRules(t, planner, store, annotations)
	if got, want := execArgvTail(t, argv, aiModeClaude), []string{"--resume", personaResumeConversation}; !slices.Equal(got, want) {
		t.Fatalf("resume exec argv tail = %q, want %q", got, want)
	}
	wantNotice := "projmux: agent/codex launched without its Project's label link rules (" + projectLinksReasonUnavailable + "): "
	if !strings.HasPrefix(stderr, wantNotice) || strings.Count(stderr, projectLinksReasonUnavailable) != 1 || strings.Count(stderr, "\n") != 1 {
		t.Fatalf("resume stderr = %q, want one line starting %q", stderr, wantNotice)
	}
	if recorded := recordedAgentAnnotations(store, "agt-beta-codex"); !maps.Equal(recorded, annotations) {
		t.Fatalf("recorded %v, want unchanged %v", recorded, annotations)
	}

	create, launcher, createStore, createPlanner, alpha, _ := newLinkRulesCreate(t)
	createPaths := linkRulesPaths(t, createPlanner)
	alphaFile, err := projectlinks.NewDefaultStore(createPaths).Path(alpha)
	if err != nil {
		t.Fatal(err)
	}
	writeLinkRules(t, createPaths, alpha, linkRulesAlpha)
	if err := os.WriteFile(alphaFile, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, stderr, err = runRoute(t, create, "agent", "--provider", "claude", "--project", "alpha", "--window", "review", "--", "review this")
	if err != nil {
		t.Fatalf("create with corrupt rules: %v", err)
	}
	if len(launcher.plans) != 1 || launcher.plans[0].personaFile != "" {
		t.Fatalf("create plans = %+v, want no system prompt file", launcher.plans)
	}
	if strings.Count(stderr, projectLinksReasonUnavailable) != 1 || !strings.Contains(stderr, "agent/agent-test-1 launched without its Project's label link rules") {
		t.Fatalf("create stderr = %q, want one notice", stderr)
	}
	if agent := agentNamed(t, createStore, "win-alpha-review", "agent-test-1"); agent.Metadata.Annotations != nil {
		t.Fatalf("create with corrupt rules recorded %v", agent.Metadata.Annotations)
	}
}

// TestProjectLinkRulesComeFromTheRegistryOwnerProjectNotTheWorkingDirectory is
// the two-Project fixture: the beta Agent whose workspace cwd is alpha's root
// gets beta's rules -- its Window's Project in the Registry -- and never
// alpha's.
func TestProjectLinkRulesComeFromTheRegistryOwnerProjectNotTheWorkingDirectory(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	paths := linkRulesPaths(t, planner)
	store := newFakeResourceStore(t)
	alpha, beta := reUIDLinkRulesProjects(t, store)
	writeLinkRules(t, paths, alpha, linkRulesAlpha)
	writeLinkRules(t, paths, beta, linkRulesBeta)
	target, _ := store.registry.Agent("agt-beta-codex")
	target.Spec.Workspace = coremetadata.AgentWorkspace{CWD: "/srv/alpha"}

	argv, _ := resumeClaudeAgentWithLinkRules(t, planner, store, nil)
	tail := execArgvTail(t, argv, aiModeClaude)
	betaPath, betaDigest := linkRulesSnapshot(t, paths, linkRulesBeta, linkRulesProject(t, store, beta))
	alphaPath, _ := linkRulesSnapshot(t, paths, linkRulesAlpha, linkRulesProject(t, store, alpha))
	if got := systemPromptFileOf(t, tail); got != betaPath || slices.Contains(tail, alphaPath) {
		t.Fatalf("exec argv tail = %q, want beta's rules %s and never alpha's %s", tail, betaPath, alphaPath)
	}
	if got := recordedAgentAnnotations(store, "agt-beta-codex")[coremetadata.AnnotationAgentProjectLinkRulesDigest]; got != betaDigest {
		t.Fatalf("recorded digest %q, want beta's %q", got, betaDigest)
	}

	// The create side reads the resolved Project too.
	create, launcher, createStore, createPlanner, createAlpha, createBeta := newLinkRulesCreate(t)
	createPaths := linkRulesPaths(t, createPlanner)
	writeLinkRules(t, createPaths, createAlpha, linkRulesAlpha)
	writeLinkRules(t, createPaths, createBeta, linkRulesBeta)
	if _, _, err := runRoute(t, create, "agent", "--provider", "claude", "--project", "beta", "--window", "main", "--", "go"); err != nil {
		t.Fatal(err)
	}
	if wantPath, _ := linkRulesSnapshot(t, createPaths, linkRulesBeta, linkRulesProject(t, createStore, createBeta)); len(launcher.plans) != 1 || launcher.plans[0].personaFile != wantPath {
		t.Fatalf("create plans = %+v, want beta's rules %s", launcher.plans, wantPath)
	}
}

// TestAgentResumeWithModelEffortAndProjectLinkRulesPassesAllBeforeTheWorkspace
// pins `agent resume --model X --effort Y` in a Project with rules: the model,
// the effort, the rules file and the snapshot mode are in the one argv, ahead
// of the workspace arguments.
func TestAgentResumeWithModelEffortAndProjectLinkRulesPassesAllBeforeTheWorkspace(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	paths := linkRulesPaths(t, planner)
	store := newFakeResourceStore(t)
	_, beta := reUIDLinkRulesProjects(t, store)
	writeLinkRules(t, paths, beta, linkRulesBeta)
	target, _ := store.registry.Agent("agt-beta-codex")
	target.Spec.Workspace = coremetadata.AgentWorkspace{CWD: "/srv/beta", AdditionalWritableRoots: []string{"/srv/beta/extra"}}

	argv, _ := resumeClaudeAgentWithLinkRules(t, planner, store, nil, "--model", "opus", "--effort", "max")
	path, digest := linkRulesSnapshot(t, paths, linkRulesBeta, linkRulesProject(t, store, beta))
	want := []string{"--model", "opus", "--effort", "max", "--append-system-prompt-file", path,
		"--system-prompt-snapshot", "off", "--add-dir", "/srv/beta/extra", "--resume", personaResumeConversation}
	if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, want) {
		t.Fatalf("exec argv tail = %q, want %q", got, want)
	}
	recorded := recordedAgentAnnotations(store, "agt-beta-codex")
	if recorded[coremetadata.AnnotationAgentEffort] != "max" || recorded[coremetadata.AnnotationAgentProjectLinkRulesDigest] != digest {
		t.Fatalf("recorded %v, want the effort and the rules digest", recorded)
	}
}

// TestResumeWithPersonaAndProjectLinkRulesPassesOneCompositeFile pins the
// resume side of the measured Claude rule: a persona Agent with rules gets one
// composite file, and its persona snapshot and annotations stay untouched.
func TestResumeWithPersonaAndProjectLinkRulesPassesOneCompositeFile(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	paths := linkRulesPaths(t, planner)
	withPersona, personaPath := createPersonaForResume(t, planner, "go-reviewer", []byte(personaResumeContent))
	store := newFakeResourceStore(t)
	_, beta := reUIDLinkRulesProjects(t, store)
	writeLinkRules(t, paths, beta, linkRulesBeta)

	argv, _ := resumeClaudeAgentWithLinkRules(t, planner, store, withPersona)
	tail := execArgvTail(t, argv, aiModeClaude)
	file := systemPromptFileOf(t, tail)
	if file == personaPath {
		t.Fatalf("exec argv tail = %q passes the persona alone", tail)
	}
	if got, err := os.ReadFile(file); err != nil || !bytes.Equal(got, linkRulesComposite([]byte(personaResumeContent), linkRulesBeta, linkRulesProject(t, store, beta))) {
		t.Fatalf("composite %s = %q, %v", file, got, err)
	}
	if got, err := os.ReadFile(personaPath); err != nil || string(got) != personaResumeContent {
		t.Fatalf("persona snapshot = %q, %v; want untouched", got, err)
	}
	recorded := recordedAgentAnnotations(store, "agt-beta-codex")
	for key, value := range withPersona {
		if recorded[key] != value {
			t.Fatalf("recorded %v changed the persona annotations %v", recorded, withPersona)
		}
	}
}

// TestProjectLinkRulesLeaveCodexAndReplyOnlyLaunchesUnchanged pins that the
// rules are Claude-only: Codex, and the Claude reply-only lane, launch with
// the argv they had before, even with a digest annotation or rules present.
func TestProjectLinkRulesLeaveCodexAndReplyOnlyLaunchesUnchanged(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	paths := linkRulesPaths(t, planner)
	store := newFakeResourceStore(t)
	_, beta := reUIDLinkRulesProjects(t, store)
	writeLinkRules(t, paths, beta, linkRulesBeta)

	for _, provider := range []string{aiModeCodex, aiModeAntigravity} {
		if links := planner.PlanProjectLinks(provider, linkRulesRegistryProject(t, store, beta), nil); links.active {
			t.Fatalf("%s planned project link rules: %+v", provider, links)
		}
	}
	workspace := coremetadata.AgentWorkspace{CWD: "/work/owner", AdditionalWritableRoots: []string{"/work/extra"}}
	_, digest := linkRulesSnapshot(t, paths, linkRulesBeta, linkRulesProject(t, store, beta))
	if links := planner.PlanProjectLinks(aiModeClaude, linkRulesRegistryProject(t, store, beta), nil); links.digest != digest {
		t.Fatalf("claude rules digest = %q, want %q", links.digest, digest)
	}
	annotations := map[string]string{coremetadata.AnnotationAgentProjectLinkRulesDigest: digest}
	before, err := planner.PlanAgentResume(aiModeCodex, workspace, resumeFixtureConversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, err := planner.PlanAgentResume(aiModeCodex, workspace, resumeFixtureConversation, annotations)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(after.argv, before.argv) || after.projectLinksUnavailable != nil {
		t.Fatalf("codex resume argv with a rules digest = %q, want %q", after.argv, before.argv)
	}

	create := &createCommand{agents: &projectLinksAgentLauncher{fakeAgentLauncher: newFakeAgentLauncher(), planner: planner}}
	for name, flags := range map[string]resourceCreateFlags{
		"codex":      {},
		"reply-only": {dialogueReplyOnly: true},
	} {
		provider := aiModeClaude
		if name == "codex" {
			provider = aiModeCodex
		}
		create.prepareProjectLinks(provider, linkRulesRegistryProject(t, store, beta), &flags)
		if flags.projectLinks.active || flags.resumeLaunchValues != nil {
			t.Fatalf("%s: prepared project link rules %+v", name, flags.projectLinks)
		}
	}
}

// TestTopologyReplayPassesChangedProjectLinkRulesAndRecordsThem covers the
// Continue/topology replay: the plan reads the replayed Project's rules, the
// launch passes them with the snapshot off, and the materialization record
// writes exactly that digest.
func TestTopologyReplayPassesChangedProjectLinkRulesAndRecordsThem(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	turnAgentGuidanceOff(t, planner)
	paths := linkRulesPaths(t, planner)
	projectUID, err := coremetadata.NewUID(coremetadata.KindProject)
	if err != nil {
		t.Fatal(err)
	}
	writeLinkRules(t, paths, projectUID, linkRulesBeta)
	root := t.TempDir()
	project := coremetadata.Project{Metadata: coremetadata.ObjectMeta{UID: projectUID, Name: "beta"}, Spec: coremetadata.ProjectSpec{Root: root}}
	agent := coremetadata.Agent{
		Metadata: coremetadata.ObjectMeta{UID: "agent-1", Name: "reviewer"},
		Spec:     coremetadata.AgentSpec{Provider: aiModeClaude, Workspace: coremetadata.AgentWorkspace{CWD: root}},
		Status:   coremetadata.AgentStatus{SessionRef: claudeConversationRef(personaResumeConversation)},
	}
	plan := &registryTopologyPlan{}
	work, ok := planTopologyAgentReplay(plan, project, agent, "main/reviewer", planner)
	if !ok || len(plan.notices) != 0 {
		t.Fatalf("replay planned %v, notices %v", ok, plan.notices)
	}
	path, digest := linkRulesSnapshot(t, paths, linkRulesBeta, projectlinks.ProjectOf(project))
	want := []string{"--append-system-prompt-file", path, "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := execArgvTail(t, work.argv, aiModeClaude); !slices.Equal(got, want) {
		t.Fatalf("replay exec argv tail = %q, want %q", got, want)
	}
	registry := coremetadata.NewRegistry()
	registry.Agents = []coremetadata.Agent{agent}
	if err := work.links.record(&registry, coremetadata.Mutator{}, "agent-1"); err != nil {
		t.Fatal(err)
	}
	wantRecorded := map[string]string{
		coremetadata.AnnotationAgentProjectLinkRulesDigest: digest,
		coremetadata.AnnotationAgentSystemPromptSnapshot:   coremetadata.SystemPromptSnapshotOff,
	}
	if got := registry.Agents[0].Metadata.Annotations; !maps.Equal(got, wantRecorded) {
		t.Fatalf("replay recorded %v, want %v", got, wantRecorded)
	}
}

// TestResumePickerInAProjectWithLinkRulesLaunchesThemWithTheSnapshotOff pins
// the resume-picker create: the picked conversation recorded a system prompt
// without the rules, so the new Agent launches the rules with the snapshot
// off and records both.
func TestResumePickerInAProjectWithLinkRulesLaunchesThemWithTheSnapshotOff(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	paths := linkRulesPaths(t, planner)
	store := newFakeResourceStore(t)
	alpha, _ := reUIDLinkRulesProjects(t, store)
	writeLinkRules(t, paths, alpha, linkRulesAlpha)
	tmux := newFakeTmux()
	session := tmux.addSession("alpha")
	seedOwnedSession(session, alpha, "/srv/alpha")
	seedLiveWindow(t, tmux, session, "win-alpha-main", "pan-alpha-zsh")
	create, _ := newTestAgentCreateCommand(t, store, tmux)
	launcher := &exactArgvResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), planner: planner}
	create.resumes = &projectLinksResumeLauncher{launcher}
	originID := livePaneWithUID(t, tmux, "pan-alpha-zsh")
	withPopupOrigin(create, tmux, popupEnv(originID))
	f := pickerLaunchValuesFixture{
		canonicalRootFixture: canonicalRootFixture{store: store, tmux: tmux, create: create, originID: originID,
			windowUID: "win-alpha-main", rootKind: coremetadata.KindProject, rootUID: alpha},
		planner: planner, launcher: launcher,
	}

	agent, argv, stderr := f.pick(t, false, aiModeClaude, personaResumeConversation, "")
	path, digest := linkRulesSnapshot(t, paths, linkRulesAlpha, linkRulesProject(t, store, alpha))
	want := []string{"--append-system-prompt-file", path, "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, want) || stderr != "" {
		t.Fatalf("picker exec argv tail = %q stderr = %q, want %q", got, stderr, want)
	}
	wantRecorded := map[string]string{
		coremetadata.AnnotationAgentProjectLinkRulesDigest: digest,
		coremetadata.AnnotationAgentSystemPromptSnapshot:   coremetadata.SystemPromptSnapshotOff,
	}
	if !maps.Equal(agent.Metadata.Annotations, withUICreator(wantRecorded)) {
		t.Fatalf("picker Agent annotations = %v, want %v", agent.Metadata.Annotations, wantRecorded)
	}
}

// linkRulesProjectName links a branch label through the Project's name.
var linkRulesProjectName = projectlinks.Rules{
	Repo:  []string{"https://github.com/example/beta"},
	Links: []projectlinks.Link{{LabelKey: "branch", Template: "{repo}/tree/{project.name}/{value}"}},
}

// TestProjectLinkRulesRenderTheRegistryProjectNameAndARenameIsARulesChange
// pins that the rules are rendered with the Registry Project's variables: a
// create's snapshot carries the Project's name, a resume under the same name
// changes nothing, and a resume after the Project is renamed in the Registry
// passes a new rendering, records its digest and turns the snapshot off.
func TestProjectLinkRulesRenderTheRegistryProjectNameAndARenameIsARulesChange(t *testing.T) {
	create, launcher, createStore, planner, alpha, beta := newLinkRulesCreate(t)
	paths := linkRulesPaths(t, planner)
	writeLinkRules(t, paths, beta, linkRulesProjectName)

	if _, stderr, err := runRoute(t, create, "agent", "--provider", "claude", "--project", "beta", "--window", "main", "--", "go"); err != nil || strings.Contains(stderr, projectLinksReasonUnavailable) {
		t.Fatalf("create: stderr=%q err=%v", stderr, err)
	}
	if len(launcher.plans) != 1 {
		t.Fatalf("plans = %+v", launcher.plans)
	}
	created, err := os.ReadFile(launcher.plans[0].personaFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`- {project.name}: "beta"`, "https://github.com/example/beta/tree/beta/EXAMPLE-123"} {
		if !strings.Contains(string(created), want) {
			t.Fatalf("created snapshot =\n%s\nwant it to contain %q", created, want)
		}
	}
	createdDigest := projectlinks.Digest(created)
	agent := agentNamed(t, createStore, "win-beta-main", "agent-test-1")
	if got := agent.Metadata.Annotations[coremetadata.AnnotationAgentProjectLinkRulesDigest]; got != createdDigest {
		t.Fatalf("create recorded digest %q, want the launched snapshot's %q", got, createdDigest)
	}
	annotations := map[string]string{coremetadata.AnnotationAgentProjectLinkRulesDigest: createdDigest}

	// The same name renders the same rules: the resume changes nothing.
	store := newFakeResourceStore(t)
	reUIDLinkRulesProjectsTo(t, store, alpha, beta)
	argv, _ := resumeClaudeAgentWithLinkRules(t, planner, store, annotations)
	if got, want := execArgvTail(t, argv, aiModeClaude), []string{"--append-system-prompt-file", launcher.plans[0].personaFile, "--resume", personaResumeConversation}; !slices.Equal(got, want) {
		t.Fatalf("unrenamed resume exec argv tail = %q, want %q", got, want)
	}

	// Renaming the Project in the Registry is a rules change for the next resume.
	store = newFakeResourceStore(t)
	reUIDLinkRulesProjectsTo(t, store, alpha, beta)
	if _, err := (coremetadata.Mutator{}).RenameProject(&store.registry, beta, "beta-renamed"); err != nil {
		t.Fatal(err)
	}
	argv, stderr := resumeClaudeAgentWithLinkRules(t, planner, store, annotations)
	path, digest := linkRulesSnapshot(t, paths, linkRulesProjectName, linkRulesProject(t, store, beta))
	want := []string{"--append-system-prompt-file", path, "--system-prompt-snapshot", "off", "--resume", personaResumeConversation}
	if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, want) || stderr != "" {
		t.Fatalf("renamed resume exec argv tail = %q stderr = %q, want %q", got, stderr, want)
	}
	if digest == createdDigest {
		t.Fatal("renaming the Project kept the rules digest")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`- {project.name}: "beta-renamed"`, "https://github.com/example/beta/tree/beta-renamed/EXAMPLE-123"} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("renamed snapshot =\n%s\nwant it to contain %q", content, want)
		}
	}
	wantRecorded := map[string]string{
		coremetadata.AnnotationAgentProjectLinkRulesDigest: digest,
		coremetadata.AnnotationAgentSystemPromptSnapshot:   coremetadata.SystemPromptSnapshotOff,
	}
	if recorded := recordedAgentAnnotations(store, "agt-beta-codex"); !maps.Equal(recorded, wantRecorded) {
		t.Fatalf("renamed resume recorded %v, want %v", recorded, wantRecorded)
	}
}
