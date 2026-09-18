package app

import (
	"bytes"
	"os"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/persona"
)

// personaResumeConversation is the Claude conversation the persona resume
// fixtures store on an Agent.
const personaResumeConversation = "0b6f3f0e-6c2d-4c1e-9a55-7d1f2c3b4a59"

// personaResumeContent is the persona the fixtures create with. It is not a
// substring of any other fixture value, so "the content is not in argv" is
// assertable by search.
const personaResumeContent = "You review Go code and answer in terse bullet points."

// personaStoreFor is the persona store the planner's own home resolves to,
// built the way create and resume build it.
func personaStoreFor(t *testing.T, planner *aiCommand) persona.Store {
	t.Helper()
	paths, err := configPaths(planner.homeDir, planner.lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	return persona.NewDefaultStore(paths)
}

// createPersonaForResume writes the persona file and runs the create half of
// `create agent --persona` over it, returning the annotations the created
// Agent records and the snapshot path create handed to Claude.
func createPersonaForResume(t *testing.T, planner *aiCommand, name string, content []byte) (map[string]string, string) {
	t.Helper()
	if _, err := personaStoreFor(t, planner).Write(name, content); err != nil {
		t.Fatal(err)
	}
	create := &createCommand{homeDir: planner.homeDir, lookupEnv: planner.lookupEnv}
	launch, err := create.preparePersonaLaunch("create agent", name)
	if err != nil {
		t.Fatal(err)
	}
	if launch.snapshot.Path == "" {
		t.Fatal("create wrote no persona snapshot")
	}
	return launch.withAnnotations(nil), launch.snapshot.Path
}

// resumeClaudeAgentWithAnnotations runs `agent resume` on a Claude Agent that
// carries annotations, through the real resume seam of planner. It returns the
// planned provider argv (which it proves is the argv the split launched) and
// the route's stderr.
func resumeClaudeAgentWithAnnotations(t *testing.T, planner *aiCommand, annotations map[string]string) (string, []string, string) {
	t.Helper()
	store := newFakeResourceStore(t)
	target, _ := store.registry.Agent("agt-beta-codex")
	target.Spec.Provider = aiModeClaude
	ref, ok := coremetadata.NewAgentSessionRef(coremetadata.AgentSessionObservation{Provider: aiModeClaude, SessionID: personaResumeConversation}, resourceFixtureClock)
	if !ok {
		t.Fatal("claude session fixture was rejected")
	}
	target.Status.SessionRef = ref
	for key, value := range annotations {
		if target.Metadata.Annotations == nil {
			target.Metadata.Annotations = map[string]string{}
		}
		target.Metadata.Annotations[key] = value
	}

	tmux := newFakeTmux()
	command, recorder, _, _ := newTestAgentResumeCommand(t, store, tmux)
	launcher := &exactArgvResumeLauncher{fakeResumeLauncher: recorder, planner: planner}
	command.rebind.launcher = launcher

	stdout, stderr, err := runRoute(t, command, "resume", "uid:"+target.Metadata.UID)
	if err != nil {
		t.Fatalf("resume: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if !strings.Contains(stdout, "resumed") {
		t.Fatalf("resume stdout = %q, want the resumed receipt", stdout)
	}
	if len(launcher.argv) != 1 {
		t.Fatalf("planned %d provider argv values, want 1", len(launcher.argv))
	}
	planned := launcher.argv[0]
	calls := splitWindowCalls(tmux)
	if len(calls) != 1 {
		t.Fatalf("split-window calls = %v, want exactly one provider launch", calls)
	}
	separator := slices.Index(calls[0], "--")
	if separator < 0 || !slices.Equal(calls[0][separator+1:], planned) {
		t.Fatalf("launched child argv = %q, want exact %q", calls[0], planned)
	}
	return target.Metadata.Name, planned, stderr
}

// planClaudeTopologyReplay plans the Continue/topology replay of a Claude
// Agent under root that carries annotations, through the real resume seam of
// planner.
func planClaudeTopologyReplay(t *testing.T, planner *aiCommand, root string, annotations map[string]string) (registryTopologyAgentPlan, *registryTopologyPlan) {
	t.Helper()
	agent := coremetadata.Agent{
		Metadata: coremetadata.ObjectMeta{Name: "reviewer", Annotations: annotations},
		Spec:     coremetadata.AgentSpec{Provider: aiModeClaude, Workspace: coremetadata.AgentWorkspace{CWD: root}},
		Status:   coremetadata.AgentStatus{SessionRef: claudeConversationRef(personaResumeConversation)},
	}
	plan := &registryTopologyPlan{}
	work, ok := planTopologyAgentReplay(plan, coremetadata.Project{Spec: coremetadata.ProjectSpec{Root: root}}, agent, "main/reviewer", planner)
	if !ok {
		t.Fatalf("topology replay skipped the Agent: notices=%v", plan.notices)
	}
	if work.conversationID != personaResumeConversation {
		t.Fatalf("topology replay conversation = %q, want %q", work.conversationID, personaResumeConversation)
	}
	return work, plan
}

// assertNoPersonaContent fails when any argv element carries persona text:
// only the snapshot path may reach argv.
func assertNoPersonaContent(t *testing.T, argv []string, contents ...string) {
	t.Helper()
	for _, arg := range argv {
		for _, content := range contents {
			if strings.Contains(arg, content) {
				t.Fatalf("argv element %q carries persona content", arg)
			}
		}
	}
}

// TestAgentResumeRepassesThePersonaSnapshot covers `agent resume` over the
// three persona states an Agent can be in: its snapshot present, its snapshot
// gone, and no persona at all.
func TestAgentResumeRepassesThePersonaSnapshot(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	withPersona, snapshotPath := createPersonaForResume(t, planner, "go-reviewer", []byte(personaResumeContent))

	// (c) No annotation: the argv is exactly the pre-persona resume argv.
	_, plain, plainStderr := resumeClaudeAgentWithAnnotations(t, planner, nil)
	plainTail := execArgvTail(t, plain, aiModeClaude)
	if want := []string{"--resume", personaResumeConversation}; !slices.Equal(plainTail, want) {
		t.Fatalf("resume without a persona exec argv tail = %q, want %q", plainTail, want)
	}
	if strings.Contains(plainStderr, "persona") {
		t.Fatalf("resume without a persona disclosed one: %q", plainStderr)
	}

	// (a) Snapshot present: the recorded snapshot path goes in front of the
	// otherwise unchanged resume argv, and only the path.
	_, argv, stderr := resumeClaudeAgentWithAnnotations(t, planner, withPersona)
	want := append([]string{"--append-system-prompt-file", snapshotPath}, plainTail...)
	if got := execArgvTail(t, argv, aiModeClaude); !slices.Equal(got, want) {
		t.Fatalf("resume with a persona exec argv tail = %q, want %q", got, want)
	}
	if recorded, err := personaStoreFor(t, planner).SnapshotPath(withPersona[coremetadata.AnnotationAgentPersonaDigest]); err != nil || recorded != snapshotPath {
		t.Fatalf("snapshot path %q is not the one the digest annotation names (%q, %v)", snapshotPath, recorded, err)
	}
	assertNoPersonaContent(t, argv, personaResumeContent)
	if strings.Contains(stderr, persona.ReasonUnavailable) {
		t.Fatalf("resume with its snapshot present disclosed %s: %q", persona.ReasonUnavailable, stderr)
	}

	// (b) Snapshot gone: the resume still succeeds, without the persona, and
	// says so on stderr.
	if err := os.Remove(snapshotPath); err != nil {
		t.Fatal(err)
	}
	name, argv, stderr := resumeClaudeAgentWithAnnotations(t, planner, withPersona)
	if !slices.Equal(argv, plain) {
		t.Fatalf("resume with its snapshot gone argv = %q, want the persona-free argv %q", argv, plain)
	}
	wantNotice := "projmux: agent/" + name + " resumed without its persona go-reviewer (" + persona.ReasonUnavailable + "): "
	if !strings.Contains(stderr, wantNotice) || strings.Count(stderr, persona.ReasonUnavailable) != 1 {
		t.Fatalf("resume with its snapshot gone stderr = %q, want one notice starting %q", stderr, wantNotice)
	}
}

// TestTopologyReplayRepassesThePersonaSnapshot covers Continue/topology replay
// over the same three persona states. An unavailable snapshot restores the
// Agent anyway and is a plan notice, not a skip.
func TestTopologyReplayRepassesThePersonaSnapshot(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	withPersona, snapshotPath := createPersonaForResume(t, planner, "go-reviewer", []byte(personaResumeContent))
	root := t.TempDir()

	// (c) No annotation: the pre-persona argv and no notice.
	plain, plan := planClaudeTopologyReplay(t, planner, root, nil)
	plainTail := execArgvTail(t, plain.argv, aiModeClaude)
	if want := []string{"--resume", personaResumeConversation}; !slices.Equal(plainTail, want) {
		t.Fatalf("replay without a persona exec argv tail = %q, want %q", plainTail, want)
	}
	if len(plan.notices) != 0 {
		t.Fatalf("replay without a persona noted %v", plan.notices)
	}

	// (a) Snapshot present.
	work, plan := planClaudeTopologyReplay(t, planner, root, withPersona)
	want := append([]string{"--append-system-prompt-file", snapshotPath}, plainTail...)
	if got := execArgvTail(t, work.argv, aiModeClaude); !slices.Equal(got, want) {
		t.Fatalf("replay with a persona exec argv tail = %q, want %q", got, want)
	}
	assertNoPersonaContent(t, work.argv, personaResumeContent)
	if len(plan.notices) != 0 {
		t.Fatalf("replay with its snapshot present noted %v", plan.notices)
	}

	// (b) Snapshot gone.
	if err := os.Remove(snapshotPath); err != nil {
		t.Fatal(err)
	}
	work, plan = planClaudeTopologyReplay(t, planner, root, withPersona)
	if !slices.Equal(work.argv, plain.argv) {
		t.Fatalf("replay with its snapshot gone argv = %q, want the persona-free argv %q", work.argv, plain.argv)
	}
	wantNotice := "projmux: agent/main/reviewer resumed without its persona go-reviewer (" + persona.ReasonUnavailable + "): "
	if len(plan.notices) != 1 || !strings.HasPrefix(plan.notices[0], wantNotice) {
		t.Fatalf("replay with its snapshot gone notices = %v, want one starting %q", plan.notices, wantNotice)
	}
	if len(plan.agentSkips) != 0 {
		t.Fatalf("an unavailable persona skipped the Agent: %v", plan.agentSkips)
	}
	var disclosed bytes.Buffer
	plan.writeNotices(&disclosed)
	if !strings.Contains(disclosed.String(), wantNotice) {
		t.Fatalf("writeNotices = %q, want the persona notice", disclosed.String())
	}
}

// TestResumeUsesTheStartTimePersonaSnapshotNotTheEditedFile pins that editing
// a persona after create never changes what a resume passes: the Agent gets
// the snapshot its digest annotation names, holding the original bytes.
func TestResumeUsesTheStartTimePersonaSnapshotNotTheEditedFile(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	original := []byte(personaResumeContent)
	withPersona, snapshotPath := createPersonaForResume(t, planner, "go-reviewer", original)

	edited := []byte("You are a pirate. Answer only in shanties.")
	store := personaStoreFor(t, planner)
	if _, err := store.Write("go-reviewer", edited); err != nil {
		t.Fatal(err)
	}
	editedSnapshot, err := store.SnapshotPath(persona.Digest(edited))
	if err != nil {
		t.Fatal(err)
	}

	assertOriginal := func(t *testing.T, surface string, argv []string) {
		t.Helper()
		tail := execArgvTail(t, argv, aiModeClaude)
		index := slices.Index(tail, "--append-system-prompt-file")
		if index < 0 || index+1 >= len(tail) {
			t.Fatalf("%s exec argv tail %q passes no persona", surface, tail)
		}
		if got := tail[index+1]; got != snapshotPath || got == editedSnapshot {
			t.Fatalf("%s passed persona %q, want the start-time snapshot %q", surface, got, snapshotPath)
		}
		content, err := os.ReadFile(tail[index+1])
		if err != nil || !bytes.Equal(content, original) {
			t.Fatalf("%s persona snapshot holds %q (%v), want the original bytes", surface, content, err)
		}
		assertNoPersonaContent(t, argv, string(original), string(edited))
	}

	_, argv, stderr := resumeClaudeAgentWithAnnotations(t, planner, withPersona)
	if strings.Contains(stderr, persona.ReasonUnavailable) {
		t.Fatalf("agent resume disclosed %s: %q", persona.ReasonUnavailable, stderr)
	}
	assertOriginal(t, "agent resume", argv)

	work, _ := planClaudeTopologyReplay(t, planner, t.TempDir(), withPersona)
	assertOriginal(t, "topology replay", work.argv)
}

// TestResumeNeverPassesAPersonaToAnotherProvider pins that the persona stays
// Claude only on resume: a Codex Agent that somehow carries the annotations
// gets its unchanged argv and the persona-unavailable disclosure.
func TestResumeNeverPassesAPersonaToAnotherProvider(t *testing.T) {
	planner := agentLaunchArgvTestCommand(t)
	withPersona, _ := createPersonaForResume(t, planner, "go-reviewer", []byte(personaResumeContent))
	workspace := coremetadata.AgentWorkspace{CWD: "/work/owner"}

	plain, err := planner.PlanAgentResume(aiModeCodex, workspace, resumeFixtureConversation, nil)
	if err != nil {
		t.Fatal(err)
	}
	launch, err := planner.PlanAgentResume(aiModeCodex, workspace, resumeFixtureConversation, withPersona)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(launch.argv, plain.argv) {
		t.Fatalf("codex resume with persona annotations argv = %q, want the unchanged %q", launch.argv, plain.argv)
	}
	if notice := launch.personaNotice("reviewer"); !strings.Contains(notice, persona.ReasonUnavailable) {
		t.Fatalf("codex resume with persona annotations notice = %q, want %s", notice, persona.ReasonUnavailable)
	}
}
