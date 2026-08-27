package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
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
	plans                     []fakeNativePanePlan
	bound                     []fakeNativePaneBinding
	lifecycle                 []codexLifecycleObserverTarget
	lifecycleBindingCurrent   func(codexLifecycleIdentity) (registryCurrent, paneUIDCurrent bool)
	lifecycleObservedRegistry []bool
	lifecycleObservedPaneUID  []bool
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

func (f *fakeNativePaneLauncher) startNativeCodexLifecycleObserver(target codexLifecycleObserverTarget) codexObserverStartupResult {
	f.lifecycle = append(f.lifecycle, target)
	identity := target.Identity
	registryCurrent, paneUIDCurrent := false, false
	if f.lifecycleBindingCurrent != nil {
		registryCurrent, paneUIDCurrent = f.lifecycleBindingCurrent(identity)
	}
	f.lifecycleObservedRegistry = append(f.lifecycleObservedRegistry, registryCurrent)
	f.lifecycleObservedPaneUID = append(f.lifecycleObservedPaneUID, paneUIDCurrent)
	return codexObserverStartupResult{Status: codexObserverStartupReady, Epoch: "fake-epoch"}
}

func (f *fakeNativePaneLauncher) BindAgentPaneOnRoute(ctx context.Context, runner tmuxCommandRunner, binding agentPaneBinding) error {
	for _, option := range []string{aiPaneTopicOption, aiPaneTopicManualOption} {
		if _, err := runner.Run(ctx, "tmux", "set-option", "-p", "-u", "-t", binding.PaneID, option); err != nil {
			return err
		}
	}
	f.BindNativeCodexPane(binding.PaneID, binding.ContextDir, binding.Title, binding.ConversationID)
	return nil
}

type fakeNativeResumeLauncher struct {
	*fakeResumeLauncher
	*fakeNativePaneLauncher
	sources []string
}

func (f *fakeNativeResumeLauncher) BindAgentPaneOnRoute(ctx context.Context, runner tmuxCommandRunner, binding agentPaneBinding) error {
	if binding.NativeCodex {
		return f.fakeNativePaneLauncher.BindAgentPaneOnRoute(ctx, runner, binding)
	}
	if err := f.fakeResumeLauncher.BindAgentPaneOnRoute(ctx, runner, binding); err != nil {
		return err
	}
	if binding.ResumeSource != "" {
		f.sources = append(f.sources, binding.ResumeSource)
	}
	return nil
}

func (f *fakeNativeResumeLauncher) BindResumedAgentPaneWithSource(paneID, provider, contextDir, title, conversationID, source string) {
	f.fakeResumeLauncher.BindResumedAgentPane(paneID, provider, contextDir, title, conversationID)
	f.sources = append(f.sources, source)
}

func TestNativeCodexCreateBindsExactThreadAndSubmitsPromptOnce(t *testing.T) {
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, legacy := newTestAgentCreateCommand(t, store, tmux)
	native := &fakeNativeThreadController{createBinding: codexappserver.ThreadBinding{ThreadID: "thread-native-1", TurnID: "turn-native-1"}}
	panes := &fakeNativePaneLauncher{}
	panes.lifecycleBindingCurrent = func(identity codexLifecycleIdentity) (bool, bool) {
		paneUID, _ := tmux.Run(context.Background(), "tmux", "display-message", "-p", "-t", identity.RuntimeID, "-F", "#{@projmux_pane_uid}")
		return exactCodexLifecycleBinding(store.registry, identity), strings.TrimSpace(string(paneUID)) == identity.PaneUID
	}
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
	if len(panes.lifecycle) != 1 || !slices.Equal(panes.lifecycleObservedRegistry, []bool{true}) || !slices.Equal(panes.lifecycleObservedPaneUID, []bool{true}) {
		t.Fatalf("lifecycle startup did not observe committed exact binding: identities=%+v registry=%v paneUID=%v", panes.lifecycle, panes.lifecycleObservedRegistry, panes.lifecycleObservedPaneUID)
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

func TestEmptyPromptCodexCreateUsesOnePlainCLILaneAndNoNativeBinding(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "canonical", args: []string{"agent", "--provider", "codex", "--project", "alpha", "--window", "main", "-o", "pane-id"}},
		{name: "provider shortcut", args: []string{"codex", "--project", "alpha", "--window", "main", "-o", "pane-id"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, legacy := newTestAgentCreateCommand(t, store, tmux)
			native := &fakeNativeThreadController{createErr: errFakeNativeUnavailable, fallback: true}
			panes := &fakeNativePaneLauncher{}
			create.codexNative = native
			create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: panes}

			stdout, stderr, err := runRoute(t, create, test.args...)
			if err != nil || strings.TrimSpace(stdout) == "" || stderr != "" {
				t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
			}
			if len(native.creates) != 1 || native.creates[0].prompt != "" || len(native.resumes) != 0 {
				t.Fatalf("native creates=%+v resumes=%+v", native.creates, native.resumes)
			}
			if len(legacy.plans) != 1 || len(legacy.plans[0].payload) != 0 || len(legacy.bound) != 1 || len(legacy.activationPanes) != 0 {
				t.Fatalf("plain plans=%+v bindings=%+v activation probes=%v", legacy.plans, legacy.bound, legacy.activationPanes)
			}
			calls := splitWindowCalls(tmux)
			if len(calls) != 1 {
				t.Fatalf("plain CLI split calls = %v, want exactly one", calls)
			}
			joined := strings.Join(calls[0], " ")
			if !strings.Contains(joined, "exec codex") || strings.Contains(joined, " resume ") || strings.Contains(joined, "--remote") {
				t.Fatalf("empty create launch = %v, want one fresh plain Codex CLI", calls[0])
			}
			if len(panes.plans) != 0 || len(panes.bound) != 0 || len(panes.lifecycle) != 0 {
				t.Fatalf("empty fallback gained native Pane state: plans=%+v bindings=%+v lifecycle=%+v", panes.plans, panes.bound, panes.lifecycle)
			}
			agent := agentNamed(t, store, "win-alpha-main", "codex-1")
			pane, ok := store.registry.Pane(agent.Status.PaneRef)
			if !ok || pane.Status.Activation.Codex != nil || agent.Status.SessionRef != nil ||
				agent.Status.Activation.State != coremetadata.ActivationNotRequested || agent.Status.Activation.Source != "" {
				t.Fatalf("fallback Agent/Pane binding = agent:%#v pane:%#v", agent.Status, pane.Status)
			}
		})
	}
}

func TestEmptyPromptCodexSplitProducersKeepOnePlainCLILane(t *testing.T) {
	for _, producer := range []canonicalCreateProducer{
		canonicalProducerSavedDefault,
		canonicalProducerProviderPicker,
		canonicalProducerDirectProvider,
	} {
		t.Run(string(producer), func(t *testing.T) {
			fx := canonicalFixture(t, false)
			native := &fakeNativeThreadController{createErr: errFakeNativeUnavailable, fallback: true}
			legacy := newFakeResumeLauncher()
			panes := &fakeNativePaneLauncher{}
			fx.create.codexNative = native
			fx.create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: panes}

			err := fx.create.createFromIntent(agentPaneIntent{
				producer: producer, provider: aiModeCodex, placement: "right", anchorPaneID: fx.originID,
			}, ioDiscard{}, ioDiscard{})
			if err != nil {
				t.Fatal(err)
			}
			if len(native.creates) != 0 || len(native.resumes) != 0 || len(legacy.plans) != 0 || len(panes.plans) != 0 || len(panes.bound) != 0 {
				t.Fatalf("split producer changed lane: native create=%+v resume=%+v legacy resume=%+v native plans=%+v bindings=%+v",
					native.creates, native.resumes, legacy.plans, panes.plans, panes.bound)
			}
			calls := splitWindowCalls(fx.tmux)
			if len(calls) != 1 {
				t.Fatalf("split calls = %v, want exactly one", calls)
			}
			joined := strings.Join(calls[0], " ")
			if !strings.Contains(joined, "exec codex") || strings.Contains(joined, " resume ") || strings.Contains(joined, "--remote") {
				t.Fatalf("split producer launch = %v, want one fresh plain Codex CLI", calls[0])
			}
		})
	}
}

func TestEmptyPromptCodexFallbackFirstInputConvergesSessionRefAndRouting(t *testing.T) {
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, planner := newTestAgentCreateCommand(t, store, tmux)
	binder := testAICommand(t.TempDir())
	create.agents = &productionBindingAgentLauncher{fakeAgentLauncher: planner, binder: binder}
	create.codexNative = &fakeNativeThreadController{createErr: errFakeNativeUnavailable, fallback: true}
	create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: &fakeNativePaneLauncher{}}

	stdout, stderr, err := runRoute(t, create,
		"agent", "--provider", "codex", "--project", "alpha", "--window", "main", "-o", "pane-id")
	if err != nil || stderr != "" {
		t.Fatalf("empty create stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	paneID := strings.TrimSpace(stdout)
	agent := agentNamed(t, store, "win-alpha-main", "codex-1")
	pane, ok := store.registry.Pane(agent.Status.PaneRef)
	if !ok || pane.Status.Activation.Codex != nil || pane.Status.Activation.Generation == "" || pane.Status.Activation.RuntimeID != paneID {
		t.Fatalf("empty fallback Pane activation = %#v", pane.Status.Activation)
	}

	home := t.TempDir()
	ingest := testAICommand(home)
	ingest.lookupEnv = func(name string) string {
		switch name {
		case "HOME":
			return home
		case "TMUX_PANE":
			return paneID
		case internalActivationPaneUIDEnv:
			return pane.Metadata.UID
		case internalActivationGenerationEnv:
			return pane.Status.Activation.Generation
		default:
			return ""
		}
	}
	backing := store.store()
	ingest.loadRegistry = backing.load
	ingest.updateRegistry = backing.update
	ingest.now = store.mutator().Now
	ingest.runCommand = func(ctx context.Context, name string, args ...string) error {
		_, err := tmux.Run(ctx, name, args...)
		return err
	}
	ingest.readCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "tmux" && len(args) > 0 && args[0] == "display-message" {
			format := args[len(args)-1]
			if strings.HasPrefix(format, "#{@") && strings.HasSuffix(format, "}") {
				_, _, target := tmux.pane(flagValue(args, "-t"))
				return []byte(target.opts[strings.TrimSuffix(strings.TrimPrefix(format, "#{"), "}")] + "\n"), nil
			}
		}
		return tmux.Run(ctx, name, args...)
	}
	if binding, managed, err := ingest.managedAgentBindingForPane(paneID); err != nil || !managed || !ingest.exactProviderActivationEvidence(binding, paneID) {
		t.Fatalf("first-input managed binding = %#v managed=%t err=%v", binding, managed, err)
	}
	ingest.stdin = strings.NewReader(`{"hook_event_name":"UserPromptSubmit","thread_id":"thread-first-input","session_id":"session-first-input","cwd":"/srv/alpha"}`)
	if err := ingest.runIngest([]string{"codex-hook"}, ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatal(err)
	}

	agent = agentNamed(t, store, "win-alpha-main", "codex-1")
	if agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || agent.Status.SessionRef.Codex.ThreadID != "thread-first-input" {
		t.Fatalf("first-input sessionRef = %#v (transactions=%d writes=%d)", agent.Status.SessionRef, store.transactions, store.writes)
	}
	_, _, livePane := tmux.pane(paneID)
	if livePane.opts[aiPaneThreadIDOption] != "thread-first-input" || livePane.opts[aiPaneAgentOption] != aiModeCodex ||
		livePane.opts[aiPaneManagedOption] != "1" {
		t.Fatalf("first-input live routing options = %#v", livePane.opts)
	}
}

func TestCodexFanOutKeepsCurrentPlainCLILaneWithoutNativeCreate(t *testing.T) {
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, legacy := newTestAgentCreateCommand(t, store, tmux)
	native := &fakeNativeThreadController{createBinding: codexappserver.ThreadBinding{ThreadID: "must-not-create", TurnID: "must-not-turn"}}
	create.codexNative = native
	create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: &fakeNativePaneLauncher{}}

	stdout, stderr, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "-o", "pane-id")
	if err != nil || stderr != "" || len(strings.Fields(stdout)) != 2 {
		t.Fatalf("fan-out stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if len(native.creates) != 0 || len(native.resumes) != 0 || len(legacy.plans) != 1 || len(legacy.bound) != 2 || len(splitWindowCalls(tmux)) != 2 {
		t.Fatalf("fan-out native=%+v/%+v plain plans=%+v bindings=%+v splits=%v",
			native.creates, native.resumes, legacy.plans, legacy.bound, splitWindowCalls(tmux))
	}
}

func TestNativeCodexResumeReusesStoredThreadAndCreatesZeroThreads(t *testing.T) {
	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
	tmux := newFakeTmux()
	agentCommand, legacy, _, _ := newTestAgentResumeCommand(t, store, tmux)
	native := &fakeNativeThreadController{resumeBinding: codexappserver.ThreadBinding{ThreadID: resumeFixtureConversation}}
	panes := &fakeNativePaneLauncher{}
	panes.lifecycleBindingCurrent = func(identity codexLifecycleIdentity) (bool, bool) {
		paneUID, _ := tmux.Run(context.Background(), "tmux", "display-message", "-p", "-t", identity.RuntimeID, "-F", "#{@projmux_pane_uid}")
		return exactCodexLifecycleBinding(store.registry, identity), strings.TrimSpace(string(paneUID)) == identity.PaneUID
	}
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
	if len(panes.lifecycle) != 1 || !slices.Equal(panes.lifecycleObservedRegistry, []bool{true}) || !slices.Equal(panes.lifecycleObservedPaneUID, []bool{true}) {
		t.Fatalf("resume lifecycle startup did not observe committed exact binding: identities=%+v registry=%v paneUID=%v", panes.lifecycle, panes.lifecycleObservedRegistry, panes.lifecycleObservedPaneUID)
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

func TestNativeCatalogPickerResumeUsesExactThreadAndCreatesZeroThreads(t *testing.T) {
	fx := canonicalFixture(t, false)
	id := "019f0000-0000-7000-8000-000000000041"
	native := &fakeNativeThreadController{resumeBinding: codexappserver.ThreadBinding{ThreadID: id}}
	panes := &fakeNativePaneLauncher{}
	fx.create.codexNative = native
	fx.create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: panes}

	err := fx.create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right",
		conversationID: id, resumeSource: aisessions.SourceCodexAppServer, anchorPaneID: fx.originID,
	}, ioDiscard{}, ioDiscard{})
	if err != nil {
		t.Fatal(err)
	}
	if len(native.creates) != 0 || len(native.resumes) != 1 || native.resumes[0].threadID != id ||
		len(panes.plans) != 1 || panes.plans[0].threadID != id || len(panes.bound) != 1 || panes.bound[0].threadID != id {
		t.Fatalf("creates=%+v resumes=%+v plans=%+v bound=%+v", native.creates, native.resumes, panes.plans, panes.bound)
	}
}

func TestRolloutCatalogPickerResumeStaysOnCurrentCLILane(t *testing.T) {
	fx := canonicalFixture(t, false)
	id := "019f0000-0000-7000-8000-000000000042"
	native := &fakeNativeThreadController{resumeBinding: codexappserver.ThreadBinding{ThreadID: id}}
	legacy := newFakeResumeLauncher()
	fx.create.codexNative = native
	fx.create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: &fakeNativePaneLauncher{}}

	err := fx.create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right",
		conversationID: id, resumeSource: aisessions.SourceCodexRollout, anchorPaneID: fx.originID,
	}, ioDiscard{}, ioDiscard{})
	if err != nil {
		t.Fatal(err)
	}
	if len(native.creates) != 0 || len(native.resumes) != 0 || len(legacy.plans) != 1 || legacy.plans[0].conversationID != id {
		t.Fatalf("native creates=%+v resumes=%+v legacy=%+v", native.creates, native.resumes, legacy.plans)
	}
}

func TestNativeCatalogPickerResumeUnavailableFallsBackToOneCLIPlan(t *testing.T) {
	fx := canonicalFixture(t, false)
	id := "019f0000-0000-7000-8000-000000000043"
	native := &fakeNativeThreadController{resumeErr: errFakeNativeUnavailable, fallback: true}
	legacy := newFakeResumeLauncher()
	panes := &fakeNativePaneLauncher{}
	launcher := &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: panes}
	fx.create.codexNative = native
	fx.create.resumes = launcher

	err := fx.create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right",
		conversationID: id, resumeSource: aisessions.SourceCodexAppServer, anchorPaneID: fx.originID,
	}, ioDiscard{}, ioDiscard{})
	if err != nil {
		t.Fatal(err)
	}
	if len(native.creates) != 0 || len(native.resumes) != 1 || native.resumes[0].threadID != id ||
		len(legacy.plans) != 1 || legacy.plans[0].conversationID != id || len(panes.plans) != 0 || len(panes.bound) != 0 ||
		len(launcher.sources) != 1 || launcher.sources[0] != aisessions.SourceCodexRollout {
		t.Fatalf("native=%+v legacy=%+v nativePlans=%+v nativeBindings=%+v sources=%v",
			native.resumes, legacy.plans, panes.plans, panes.bound, launcher.sources)
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

func TestNativeLifecycleStarterIsNotCalledWhenCreateOrResumeTransactionFails(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		tmux.fail = []string{"set-option", aiPaneTopicOption}
		create, _ := newTestAgentCreateCommand(t, store, tmux)
		native := &fakeNativeThreadController{createBinding: codexappserver.ThreadBinding{ThreadID: "thread-native-fail", TurnID: "turn-native-fail"}}
		panes := &fakeNativePaneLauncher{}
		create.codexNative = native
		create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: panes}

		if _, _, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--", "exact prompt"); err == nil {
			t.Fatal("native create transaction unexpectedly succeeded")
		}
		if len(panes.lifecycle) != 0 {
			t.Fatalf("rolled-back create started lifecycle observer: %+v", panes.lifecycle)
		}
	})

	t.Run("resume", func(t *testing.T) {
		store := newFakeResourceStore(t)
		setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
		tmux := newFakeTmux()
		tmux.fail = []string{"set-option", aiPaneTopicOption}
		agentCommand, legacy, _, _ := newTestAgentResumeCommand(t, store, tmux)
		native := &fakeNativeThreadController{resumeBinding: codexappserver.ThreadBinding{ThreadID: resumeFixtureConversation}}
		panes := &fakeNativePaneLauncher{}
		agentCommand.rebind.launcher = &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: panes}
		agentCommand.rebind.create.codexNative = native

		if _, _, err := runRoute(t, agentCommand, "resume", "uid:agt-beta-codex"); err == nil {
			t.Fatal("native resume transaction unexpectedly succeeded")
		}
		if len(panes.lifecycle) != 0 {
			t.Fatalf("rolled-back resume started lifecycle observer: %+v", panes.lifecycle)
		}
	})
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
