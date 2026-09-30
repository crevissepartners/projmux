package app

import (
	"maps"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// replyOnlyAnnotations is what a reply-only Agent records and nothing else.
func replyOnlyAnnotations() map[string]string {
	return map[string]string{coremetadata.AnnotationAgentDialogueReplyOnly: coremetadata.DialogueReplyOnlyOn}
}

// newReplyOnlyFixture is the persona attach fixture with the reply-only lane
// wired in and the fixture Agent carrying annotations.
func newReplyOnlyFixture(t *testing.T, annotations map[string]string) (*personaAttachFixture, *replyOnlyResumeLauncher) {
	t.Helper()
	f := newPersonaAttachFixture(t)
	f.setAnnotations(annotations)
	launcher := &replyOnlyResumeLauncher{exactArgvResumeLauncher: f.launcher}
	f.command.rebind.launcher = launcher
	return f, launcher
}

// assertReplyOnlyLaunch checks that the run launched exactly one provider, the
// reply-only activation on the stored conversation, and no ordinary resume,
// and that the Agent still records the mode.
func assertReplyOnlyLaunch(t *testing.T, f *personaAttachFixture, launcher *replyOnlyResumeLauncher) {
	t.Helper()
	if !slices.Equal(launcher.conversations, []string{personaResumeConversation}) {
		t.Fatalf("reply-only launches = %q, want one on %s", launcher.conversations, personaResumeConversation)
	}
	if len(f.launcher.argv) != 0 {
		t.Fatalf("an ordinary resume was planned too: %q", f.launcher.argv)
	}
	calls := splitWindowCalls(f.tmux)
	if len(calls) != 1 {
		t.Fatalf("split-window calls = %v, want one", calls)
	}
	separator := slices.Index(calls[0], "--")
	if separator < 0 || !slices.Contains(calls[0][:separator], "--"+claudeDialogueReplyOnlyFlag) || !slices.Contains(calls[0][separator+1:], "claude-reply-only") {
		t.Fatalf("launched argv %q is not the supervised reply-only activation", calls[0])
	}
	agent := f.agent(t)
	if agent.Status.Phase != coremetadata.PhaseRunning || !coremetadata.RecordsDialogueReplyOnly(agent.Metadata.Annotations) {
		t.Fatalf("after the launch the Agent is %s with annotations %v, want Running and still reply-only", agent.Status.Phase, agent.Metadata.Annotations)
	}
}

// TestCreateAgentReplyOnlyRecordsTheModeOnTheAgent is acceptance 1 for a
// create: the reply-only create records the mode on the Agent, and only
// there, and an ordinary create records nothing.
func TestCreateAgentReplyOnlyRecordsTheModeOnTheAgent(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	create, launcher := newTestAgentCreateCommand(t, store, newFakeTmux())
	create.agents = &replyOnlyAgentLauncher{fakeAgentLauncher: launcher}
	if _, stderr, err := runRoute(t, create, "agent", "--provider", "claude", "--"+claudeDialogueReplyOnlyFlag, "--project", "alpha", "--window", "review"); err != nil {
		t.Fatalf("reply-only create: %v (stderr=%q)", err, stderr)
	}
	agent := agentNamed(t, store, "win-alpha-review", "agent-test-1")
	if !maps.Equal(agent.Metadata.Annotations, replyOnlyAnnotations()) {
		t.Fatalf("Agent annotations = %v, want only the reply-only record", agent.Metadata.Annotations)
	}
	pane, ok := store.registry.Pane(agent.Status.PaneRef)
	if !ok {
		t.Fatalf("Agent pane %q missing", agent.Status.PaneRef)
	}
	if _, found := pane.Metadata.Annotations[coremetadata.AnnotationAgentDialogueReplyOnly]; found {
		t.Fatalf("Pane carries the reply-only record: %v", pane.Metadata.Annotations)
	}
}

// replyOnlyAgentLauncher is the create fake launcher with the reply-only lane.
type replyOnlyAgentLauncher struct {
	*fakeAgentLauncher
}

func (l *replyOnlyAgentLauncher) PlanClaudeDialogueLaunch(_ coremetadata.AgentWorkspace, conversation string) (string, []string, error) {
	return "claude:reply-only", []string{"claude-reply-only", conversation}, nil
}

// TestAgentResumeReplyOnlyRecordsTheModeAndAPlainResumeKeepsIt is acceptance
// 1 and 2 for `agent resume`: --dialogue-reply-only records the mode in the
// rebind transaction, and after `delete pane` a resume without the flag
// launches the reply-only activation again.
func TestAgentResumeReplyOnlyRecordsTheModeAndAPlainResumeKeepsIt(t *testing.T) {
	f, launcher := newReplyOnlyFixture(t, nil)
	stopFixtureAgent(t, f)
	if stdout, stderr, err := runRoute(t, f.command, "resume", "uid:"+personaAttachAgent, "--"+claudeDialogueReplyOnlyFlag); err != nil {
		t.Fatalf("reply-only resume: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	assertReplyOnlyLaunch(t, f, launcher)
	if got := f.agent(t).Metadata.Annotations; !maps.Equal(got, replyOnlyAnnotations()) {
		t.Fatalf("annotations after the reply-only resume = %v, want only the reply-only record", got)
	}

	// The Pane goes away; the record stays with the Agent.
	f.tmux.calls, launcher.conversations = nil, nil
	if _, _, err := runRoute(t, f.command.paneDelete, "pane", "uid:"+f.agent(t).Status.PaneRef, "--yes"); err != nil {
		t.Fatal(err)
	}
	if stdout, stderr, err := runRoute(t, f.command, "resume", "uid:"+personaAttachAgent); err != nil {
		t.Fatalf("plain resume: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	assertReplyOnlyLaunch(t, f, launcher)
}

// TestAgentRelaunchOfAnOfflineReplyOnlyAgentResumesItReplyOnly is acceptance
// 2 for an Offline Agent, which has no managed Pane.
func TestAgentRelaunchOfAnOfflineReplyOnlyAgentResumesItReplyOnly(t *testing.T) {
	f, launcher := newReplyOnlyFixture(t, replyOnlyAnnotations())
	stopFixtureAgent(t, f)
	stdout, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent)
	if err != nil {
		t.Fatalf("relaunch: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if !strings.HasSuffix(stdout, "; resumed on the same conversation\n") {
		t.Fatalf("relaunch stdout = %q", stdout)
	}
	assertReplyOnlyLaunch(t, f, launcher)
}

// TestAgentRelaunchOfARunningReplyOnlyAgentKeepsItReplyOnly is acceptance 2
// for a Running Agent: a plain relaunch compares only what a reply-only
// launch carries, so it restarts nothing and the reply-only activation keeps
// running. It never comes back as an ordinary resume.
func TestAgentRelaunchOfARunningReplyOnlyAgentKeepsItReplyOnly(t *testing.T) {
	f, launcher := newReplyOnlyFixture(t, replyOnlyAnnotations())
	before := f.store.snapshot()
	stdout, stderr, err := runRoute(t, f.command, "relaunch", "uid:"+personaAttachAgent, "-o", "json")
	if err != nil {
		t.Fatalf("relaunch: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if !strings.Contains(stdout, `"outcome":"unchanged"`) || !strings.Contains(stdout, `"relaunchReasons":[]`) {
		t.Fatalf("relaunch of a Running reply-only Agent = %s, want unchanged with no reasons", stdout)
	}
	if len(launcher.conversations) != 0 || len(f.launcher.argv) != 0 || len(splitWindowCalls(f.tmux)) != 0 || len(f.deletes.killed) != 0 {
		t.Fatalf("an unchanged relaunch acted: reply-only=%q resumes=%q killed=%+v", launcher.conversations, f.launcher.argv, f.deletes.killed)
	}
	if f.store.snapshot() != before {
		t.Fatal("an unchanged relaunch changed the registry")
	}
}
