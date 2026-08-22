package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

var errFakeNativeUnavailable = errors.New("fake app-server unavailable")

type fakeNativeThreadController struct {
	createBinding codexappserver.ThreadBinding
	resumeBinding codexappserver.ThreadBinding
	createErr     error
	resumeErr     error
	fallback      bool
	creates       []fakeNativeCreate
	resumes       []fakeNativeResume
}

type fakeNativeCreate struct {
	workspace  coremetadata.AgentWorkspace
	prompt     string
	generation string
}

type fakeNativeResume struct {
	workspace coremetadata.AgentWorkspace
	threadID  string
}

func (f *fakeNativeThreadController) Create(_ context.Context, workspace coremetadata.AgentWorkspace, prompt, generation string) (codexappserver.ThreadBinding, error) {
	f.creates = append(f.creates, fakeNativeCreate{workspace: workspace, prompt: prompt, generation: generation})
	return f.createBinding, f.createErr
}

func (f *fakeNativeThreadController) Resume(_ context.Context, workspace coremetadata.AgentWorkspace, threadID string) (codexappserver.ThreadBinding, error) {
	f.resumes = append(f.resumes, fakeNativeResume{workspace: workspace, threadID: threadID})
	return f.resumeBinding, f.resumeErr
}

func (f *fakeNativeThreadController) CanFallback(error) bool { return f.fallback }

type fakeNativePaneLauncher struct {
	plans []fakeNativePanePlan
	bound []fakeNativePaneBinding
}

type fakeNativePanePlan struct {
	workspace coremetadata.AgentWorkspace
	threadID  string
}

type fakeNativePaneBinding struct {
	paneID, contextDir, title, threadID string
}

func (f *fakeNativePaneLauncher) PlanNativeCodexResume(workspace coremetadata.AgentWorkspace, threadID string) (string, []string, error) {
	f.plans = append(f.plans, fakeNativePanePlan{workspace: workspace, threadID: threadID})
	return "codex:native", []string{"codex", "resume", "--remote", "unix://", threadID}, nil
}

func (f *fakeNativePaneLauncher) BindNativeCodexPane(paneID, contextDir, title, threadID string) {
	f.bound = append(f.bound, fakeNativePaneBinding{paneID: paneID, contextDir: contextDir, title: title, threadID: threadID})
}

type fakeNativeResumeLauncher struct {
	*fakeResumeLauncher
	*fakeNativePaneLauncher
}

func TestNativeCodexCreateBindsExactThreadAndSubmitsPromptOnce(t *testing.T) {
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, legacy := newTestAgentCreateCommand(t, store, tmux)
	native := &fakeNativeThreadController{createBinding: codexappserver.ThreadBinding{ThreadID: "thread-native-1", TurnID: "turn-native-1"}}
	panes := &fakeNativePaneLauncher{}
	create.codexNative = native
	create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: panes}

	stdout, stderr, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--window", "main", "-o", "pane-id", "--", "  exact prompt  ")
	if err != nil {
		t.Fatal(err)
	}
	if stdout == "" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	if len(native.creates) != 1 || native.creates[0].prompt != "  exact prompt  " || native.creates[0].generation == "" {
		t.Fatalf("native create calls = %+v", native.creates)
	}
	if len(native.resumes) != 0 {
		t.Fatalf("native resume calls = %+v", native.resumes)
	}
	if len(legacy.activationPanes) != 0 {
		t.Fatalf("hook prompt acknowledgement ran after native turn: %v", legacy.activationPanes)
	}
	if len(panes.plans) != 1 || panes.plans[0].threadID != "thread-native-1" || len(panes.bound) != 1 || panes.bound[0].threadID != "thread-native-1" {
		t.Fatalf("native pane plans=%+v bindings=%+v", panes.plans, panes.bound)
	}
	for _, call := range splitWindowCalls(tmux) {
		joined := strings.Join(call, " ")
		if !strings.Contains(joined, "--remote unix:// thread-native-1") || strings.Contains(joined, "exact prompt") {
			t.Fatalf("split argv submitted a second prompt or missed exact thread: %v", call)
		}
	}
	agent := agentNamed(t, store, "win-alpha-main", "codex-1")
	if agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || agent.Status.SessionRef.Codex.ThreadID != "thread-native-1" {
		t.Fatalf("sessionRef = %#v", agent.Status.SessionRef)
	}
	pane, ok := store.registry.Pane(agent.Status.PaneRef)
	if !ok || pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.ThreadID != "thread-native-1" || pane.Status.Activation.Codex.TurnID != "turn-native-1" {
		t.Fatalf("Pane activation = %#v", pane.Status.Activation)
	}
	if agent.Status.Activation.State != coremetadata.ActivationAcknowledged || agent.Status.Activation.Source != string(coremetadata.InteractionSourceProviderControl) {
		t.Fatalf("Agent activation = %#v", agent.Status.Activation)
	}
	described, _, err := runRoute(t, newTestDescribeCommand(t, store), "pane", "uid:"+pane.Metadata.UID)
	if err != nil {
		t.Fatal(err)
	}
	rows := describeRows(t, described)
	for key, want := range map[string]string{
		"BindingSource": "native-app-server", "BindingGeneration": pane.Status.Activation.Generation,
		"ThreadID": "thread-native-1", "TurnID": "turn-native-1",
	} {
		if got := rows[key]; len(got) != 1 || got[0] != want {
			t.Fatalf("describe row %s=%v want %q\n%s", key, got, want, described)
		}
	}
}

func TestNativeCodexResumeReusesStoredThreadAndCreatesZeroThreads(t *testing.T) {
	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
	tmux := newFakeTmux()
	agentCommand, legacy, _, _ := newTestAgentResumeCommand(t, store, tmux)
	native := &fakeNativeThreadController{resumeBinding: codexappserver.ThreadBinding{ThreadID: resumeFixtureConversation}}
	panes := &fakeNativePaneLauncher{}
	launcher := &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: panes}
	agentCommand.rebind.launcher = launcher
	agentCommand.rebind.create.codexNative = native

	stdout, stderr, err := runRoute(t, agentCommand, "resume", "uid:agt-beta-codex")
	if err != nil || stdout != "agent/codex resumed\n" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if len(native.creates) != 0 || len(native.resumes) != 1 || native.resumes[0].threadID != resumeFixtureConversation {
		t.Fatalf("native creates=%+v resumes=%+v", native.creates, native.resumes)
	}
	if len(panes.plans) != 1 || panes.plans[0].threadID != resumeFixtureConversation || len(panes.bound) != 1 {
		t.Fatalf("native pane plans=%+v bindings=%+v", panes.plans, panes.bound)
	}
	calls := splitWindowCalls(tmux)
	if len(calls) != 1 || !slices.ContainsFunc(calls[0], func(arg string) bool { return strings.Contains(arg, resumeFixtureConversation) }) {
		t.Fatalf("resume split did not address stored thread: %v", calls)
	}
	after, _ := store.registry.Agent("agt-beta-codex")
	if !after.Status.SessionRef.SameConversation(resumeFixtureRef(resourceFixtureClock)) {
		t.Fatalf("resume rewrote stored thread: %#v", after.Status.SessionRef)
	}
	pane, ok := store.registry.Pane(after.Status.PaneRef)
	if !ok || pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.ThreadID != resumeFixtureConversation || pane.Status.Activation.Codex.TurnID != "" {
		t.Fatalf("resumed Pane native binding = %#v", pane.Status.Activation)
	}
}

func TestNativeUnavailablePreservesCurrentCreateFallback(t *testing.T) {
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, legacy := newTestAgentCreateCommand(t, store, tmux)
	native := &fakeNativeThreadController{createErr: errFakeNativeUnavailable, fallback: true}
	create.codexNative = native
	create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: &fakeNativePaneLauncher{}}

	stdout, stderr, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--", "fallback prompt")
	if err != nil || stdout != "agent/codex-1 created\n" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if len(native.creates) != 1 || len(legacy.activationPanes) != 1 || len(legacy.bound) != 1 {
		t.Fatalf("native calls=%d activation=%v legacy bindings=%v", len(native.creates), legacy.activationPanes, legacy.bound)
	}
	if calls := splitWindowCalls(tmux); len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "fallback prompt") {
		t.Fatalf("fallback split calls = %v", calls)
	}
	agent := agentNamed(t, store, "win-alpha-main", "codex-1")
	pane, _ := store.registry.Pane(agent.Status.PaneRef)
	if pane.Status.Activation.Codex != nil || agent.Status.Activation.Source != string(coremetadata.InteractionSourceProviderHook) {
		t.Fatalf("fallback changed hook contract: agent=%#v pane=%#v", agent.Status.Activation, pane.Status.Activation)
	}
}

func TestNativeUnavailablePreservesCurrentResumeFallback(t *testing.T) {
	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
	tmux := newFakeTmux()
	agentCommand, legacy, _, _ := newTestAgentResumeCommand(t, store, tmux)
	native := &fakeNativeThreadController{resumeErr: errFakeNativeUnavailable, fallback: true}
	panes := &fakeNativePaneLauncher{}
	agentCommand.rebind.launcher = &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: panes}
	agentCommand.rebind.create.codexNative = native

	stdout, stderr, err := runRoute(t, agentCommand, "resume", "uid:agt-beta-codex")
	if err != nil || stdout != "agent/codex resumed\n" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if len(native.creates) != 0 || len(native.resumes) != 1 || len(legacy.plans) != 1 || len(legacy.bound) != 1 || len(panes.plans) != 0 {
		t.Fatalf("native=%+v legacy plans=%+v bindings=%+v native pane plans=%+v", native.resumes, legacy.plans, legacy.bound, panes.plans)
	}
	assertOnlyResumeLaunches(t, tmux, resumeFixtureConversation, 1)
}

func TestIndeterminateNativeCreateRefusesASecondLaneAndWritesZero(t *testing.T) {
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, _ := newTestAgentCreateCommand(t, store, tmux)
	create.codexNative = &fakeNativeThreadController{createErr: errors.New("response lost after thread start")}
	create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: &fakeNativePaneLauncher{}}

	stdout, _, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--", "one prompt")
	if err == nil || stdout != "" || !strings.Contains(err.Error(), "refusing a second CLI lane") {
		t.Fatalf("stdout=%q err=%v", stdout, err)
	}
	if calls := splitWindowCalls(tmux); len(calls) != 0 {
		t.Fatalf("indeterminate native create synthesized a lane: %v", calls)
	}
	if store.writes != 0 {
		t.Fatalf("indeterminate native create writes=%d, want zero", store.writes)
	}
}

func TestCodexNativeLaunchOutcomeTableIsClosed(t *testing.T) {
	if len(codexNativeLaunchOutcomeTable) != 4 {
		t.Fatalf("outcome rows=%d, want 4: %+v", len(codexNativeLaunchOutcomeTable), codexNativeLaunchOutcomeTable)
	}
	if codexNativeLaunchOutcomeTable[0].Action != "create" || codexNativeLaunchOutcomeTable[1].Action != "resume" ||
		!strings.Contains(codexNativeLaunchOutcomeTable[2].Launch, "current CLI") ||
		!strings.Contains(codexNativeLaunchOutcomeTable[3].Launch, "none") {
		t.Fatalf("outcome table drifted: %+v", codexNativeLaunchOutcomeTable)
	}
}
