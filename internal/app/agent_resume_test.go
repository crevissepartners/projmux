package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/antigravity"
	"github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codex"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// resumeFixtureConversation is the Codex conversation the resume fixtures store
// on an Agent. It is deliberately not a substring of any other fixture value, so
// "the launched argv addresses this conversation" is assertable by search.
const resumeFixtureConversation = "thr-resume-0001"

// resumeFixtureRef builds the stored pointer the resume fixtures use.
func resumeFixtureRef(observedAt time.Time) *coremetadata.AgentSessionRef {
	return &coremetadata.AgentSessionRef{
		Provider:   "codex",
		ObservedAt: observedAt,
		Codex: &coremetadata.CodexSessionRef{
			ThreadID:  resumeFixtureConversation,
			SessionID: "sess-resume-0001",
		},
	}
}

// fakeResumeLauncher stands in for the provider resume-launch half of the AI
// command.
//
// It records what the route asked for rather than what it did, which is what
// makes the central property directly countable: every argv this route can
// possibly launch is built here, from a conversation id, and there is no method
// on it that builds a fresh-start launch at all.
type fakeResumeLauncher struct {
	mu sync.Mutex
	// disabled names the providers the Settings gate refuses.
	disabled map[string]bool
	// planErr fails the resume construction, the way an unusable conversation id
	// or a missing provider binary does.
	planErr error
	// plans records one entry per successful resume construction.
	plans []fakeResumeRequest
	// gated records one entry per Settings gate call.
	gated []string
	// bound records one entry per managed-pane binding.
	bound []fakeResumedPane
}

type fakeResumeRequest struct {
	provider       string
	contextDir     string
	additionalDirs []string
	conversationID string
}

type fakeResumedPane struct {
	paneID         string
	provider       string
	title          string
	conversationID string
}

type productionBindingResumeLauncher struct {
	*fakeResumeLauncher
	binder *aiCommand
}

type exactArgvResumeLauncher struct {
	*fakeResumeLauncher
	planner *aiCommand
	argv    [][]string
}

func (l *exactArgvResumeLauncher) PlanAgentResume(provider string, workspace coremetadata.AgentWorkspace, conversationID string) (string, []string, error) {
	title, argv, err := l.planner.PlanAgentResume(provider, workspace, conversationID)
	if err == nil {
		l.argv = append(l.argv, slices.Clone(argv))
	}
	return title, argv, err
}

// pinnedResumeTestLauncher gives a test explicit native endpoint authority
// while preserving its existing presentation recorder. It never launches the
// recorder's plain argv; PlanNativeCodexResume returns only the pinned route.
type pinnedResumeTestLauncher struct {
	base   agentResumeLauncher
	record *fakeResumeLauncher
	panes  *fakeNativePaneLauncher
}

func (l *pinnedResumeTestLauncher) RequireAgentEnabled(provider string) error {
	return l.base.RequireAgentEnabled(provider)
}

func (l *pinnedResumeTestLauncher) PlanAgentResume(provider string, workspace coremetadata.AgentWorkspace, conversationID string) (string, []string, error) {
	return l.base.PlanAgentResume(provider, workspace, conversationID)
}

func (l *pinnedResumeTestLauncher) BindResumedAgentPane(paneID, provider, contextDir, title, conversationID string) {
	l.base.BindResumedAgentPane(paneID, provider, contextDir, title, conversationID)
}

func (l *pinnedResumeTestLauncher) BindAgentPaneOnRoute(ctx context.Context, runner tmuxCommandRunner, binding agentPaneBinding) error {
	if binding.NativeCodex {
		if err := l.panes.BindAgentPaneOnRoute(ctx, runner, binding); err != nil {
			return err
		}
	}
	return l.base.BindAgentPaneOnRoute(ctx, runner, binding)
}

func (l *pinnedResumeTestLauncher) PlanNativeCodexResume(route codexNativeEndpointRoute, workspace coremetadata.AgentWorkspace, threadID string) (string, []string, error) {
	l.record.mu.Lock()
	if l.record.planErr != nil {
		err := l.record.planErr
		l.record.mu.Unlock()
		return "", nil, err
	}
	l.record.plans = append(l.record.plans, fakeResumeRequest{
		provider: aiModeCodex, contextDir: workspace.CWD,
		additionalDirs: append([]string(nil), workspace.AdditionalWritableRoots...), conversationID: threadID,
	})
	l.record.mu.Unlock()
	_, argv, err := l.panes.PlanNativeCodexResume(route, workspace, threadID)
	return "codex:resume", argv, err
}

func (l *pinnedResumeTestLauncher) BindNativeCodexPane(paneID, contextDir, title, threadID string) {
	l.panes.BindNativeCodexPane(paneID, contextDir, title, threadID)
}

func (l *pinnedResumeTestLauncher) startNativeCodexLifecycleObserver(target codexLifecycleObserverTarget) codexObserverStartupResult {
	return l.panes.startNativeCodexLifecycleObserver(target)
}

func enablePinnedNativeResumeFixture(t *testing.T, command *agentCommand, store *fakeResourceStore, agentUID string, recorder *fakeResumeLauncher) (*fakeNativeThreadController, *fakeNativePaneLauncher) {
	t.Helper()
	agent, ok := store.registry.Agent(agentUID)
	if !ok || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || strings.TrimSpace(agent.Status.SessionRef.Codex.ThreadID) == "" {
		t.Fatalf("native resume fixture Agent %q has no Codex thread", agentUID)
	}
	route := nativeTestRoute("generation-resume-fixture", coremetadata.CodexGenerationCurrent)
	endpoint := route.Endpoint
	agent.Status.SessionRef.Codex.Endpoint = &endpoint
	agent.Status.SessionRef.Codex.Lifecycle = &coremetadata.CodexGenerationLifecycleRef{State: coremetadata.CodexGenerationCurrent}
	controller := &fakeNativeThreadController{
		resolvedRoute: route, resumeBinding: codexappserver.ThreadBinding{ThreadID: agent.Status.SessionRef.Codex.ThreadID},
	}
	panes := &fakeNativePaneLauncher{}
	command.rebind.create.codexNative = controller
	command.rebind.launcher = &pinnedResumeTestLauncher{base: command.rebind.launcher, record: recorder, panes: panes}
	return controller, panes
}

func (l *productionBindingResumeLauncher) BindResumedAgentPaneOnRoute(
	ctx context.Context,
	runner tmuxCommandRunner,
	paneID, provider, contextDir, title, conversationID string,
) error {
	return l.binder.BindResumedAgentPaneOnRoute(ctx, runner, paneID, provider, contextDir, title, conversationID)
}

func (l *productionBindingResumeLauncher) BindAgentPaneOnRoute(ctx context.Context, runner tmuxCommandRunner, binding agentPaneBinding) error {
	return l.binder.BindAgentPaneOnRoute(ctx, runner, binding)
}

func newFakeResumeLauncher() *fakeResumeLauncher {
	return &fakeResumeLauncher{disabled: map[string]bool{}}
}

func (f *fakeResumeLauncher) RequireAgentEnabled(provider string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gated = append(f.gated, provider)
	if f.disabled[provider] {
		return errors.New("AI agent " + provider + " is disabled in Settings > AI Settings > Enabled agents")
	}
	return nil
}

func (f *fakeResumeLauncher) PlanAgentResume(provider string, workspace coremetadata.AgentWorkspace, conversationID string) (string, []string, error) {
	contextDir := workspace.CWD
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.planErr != nil {
		return "", nil, f.planErr
	}
	f.plans = append(f.plans, fakeResumeRequest{
		provider:       provider,
		contextDir:     contextDir,
		additionalDirs: append([]string(nil), workspace.AdditionalWritableRoots...),
		conversationID: conversationID,
	})
	// The shape mirrors the real seam: a shell wrapper whose exec tail is the
	// provider's own resume argv, so the conversation id is observable in the
	// argv the split actually receives.
	return provider + ":resume", []string{"sh", "-lc", "exec " + provider + " resume " + conversationID}, nil
}

func (f *fakeResumeLauncher) BindResumedAgentPane(paneID, provider, _, title, conversationID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bound = append(f.bound, fakeResumedPane{
		paneID: paneID, provider: provider, title: title, conversationID: conversationID,
	})
}

func (f *fakeResumeLauncher) BindAgentPaneOnRoute(ctx context.Context, runner tmuxCommandRunner, binding agentPaneBinding) error {
	if binding.Topic != "" {
		if _, err := runner.Run(ctx, "tmux", "set-option", "-p", "-t", binding.PaneID, aiPaneTopicOption, binding.Topic); err != nil {
			return err
		}
		if _, err := runner.Run(ctx, "tmux", "set-option", "-p", "-t", binding.PaneID, aiPaneTopicManualOption, "on"); err != nil {
			return err
		}
	} else {
		for _, option := range []string{aiPaneTopicOption, aiPaneTopicManualOption} {
			if _, err := runner.Run(ctx, "tmux", "set-option", "-p", "-u", "-t", binding.PaneID, option); err != nil {
				return err
			}
		}
	}
	f.BindResumedAgentPane(binding.PaneID, binding.Provider, binding.ContextDir, binding.Title, binding.ConversationID)
	return nil
}

// newTestAgentResumeCommand wires the Agent namespace onto the in-memory
// registry, the in-memory tmux server, and a recording resume launcher.
func newTestAgentResumeCommand(t *testing.T, store *fakeResourceStore, tmux *fakeTmux) (
	*agentCommand, *fakeResumeLauncher, *recordingArgv, *recordingArgv,
) {
	t.Helper()
	create, _ := newTestResourceCreateCommand(t, store, tmux)
	launcher := newFakeResumeLauncher()
	ai := &recordingArgv{}
	usage := &recordingArgv{}
	rebinder := newAgentRebinder(create, launcher)
	rebinder.resolveWorkspace = testAgentWorkspaceResolver
	return &agentCommand{
		ai:               ai,
		usage:            usage,
		loadRegistry:     store.store().load,
		store:            store.store(),
		now:              func() time.Time { return resourceFixtureClock.Add(time.Minute) },
		resolveWorkspace: testAgentWorkspaceResolver,
		rebind:           rebinder,
	}, launcher, ai, usage
}

type fakeDrainingHandoverRequester struct {
	operationRef string
	endpoints    []coremetadata.CodexEndpointRef
}

func (requester *fakeDrainingHandoverRequester) RequestHandover(_ context.Context, endpoint coremetadata.CodexEndpointRef) (string, bool, error) {
	requester.endpoints = append(requester.endpoints, endpoint)
	return requester.operationRef, len(requester.endpoints) == 1, nil
}

func markResumeFixtureHandoverState(t *testing.T, store *fakeResourceStore, agentUID, operationRef string, state coremetadata.CodexGenerationState) coremetadata.CodexEndpointRef {
	t.Helper()
	agent, ok := store.registry.Agent(agentUID)
	if !ok || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || agent.Status.SessionRef.Codex.Endpoint == nil {
		t.Fatal("native resume fixture is missing its exact endpoint")
	}
	endpoint := *agent.Status.SessionRef.Codex.Endpoint
	agent.Status.SessionRef.Codex.Lifecycle = &coremetadata.CodexGenerationLifecycleRef{
		State:     state,
		Operation: &coremetadata.CodexGenerationOperationRef{ID: operationRef, Endpoint: endpoint},
	}
	if err := store.registry.Validate(); err != nil {
		t.Fatal(err)
	}
	return endpoint
}

func TestAgentResumeDrainingGenerationFailsClosedWhenHandoverRequesterIsUnconfigured(t *testing.T) {
	for _, state := range []coremetadata.CodexGenerationState{coremetadata.CodexGenerationDraining, coremetadata.CodexGenerationHandoverPending} {
		t.Run(string(state), func(t *testing.T) {
			store := newFakeResourceStore(t)
			setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
			tmux := newFakeTmux()
			command, launcher, _, _ := newTestAgentResumeCommand(t, store, tmux)
			enablePinnedNativeResumeFixture(t, command, store, "agt-beta-codex", launcher)
			markResumeFixtureHandoverState(t, store, "agt-beta-codex", "upgrade-one", state)
			before := store.snapshot()
			_, _, err := runRoute(t, command, "resume", "uid:agt-beta-codex")
			if err == nil || !strings.Contains(err.Error(), "handover-required operation=upgrade-one") || !strings.Contains(err.Error(), "not configured") {
				t.Fatalf("%s resume error = %v", state, err)
			}
			if store.snapshot() != before || store.transactions != 0 || store.writes != 0 || len(tmux.calls) != 0 || len(launcher.plans) != 0 {
				t.Fatalf("fail-closed %s resume mutated state: transactions=%d writes=%d tmux=%v provider=%v", state, store.transactions, store.writes, tmux.calls, launcher.plans)
			}
		})
	}
}

func TestAgentResumeDrainingGenerationReusesOneGenerationWideOperationRef(t *testing.T) {
	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
	tmux := newFakeTmux()
	command, launcher, _, _ := newTestAgentResumeCommand(t, store, tmux)
	enablePinnedNativeResumeFixture(t, command, store, "agt-beta-codex", launcher)
	endpoint := markResumeFixtureHandoverState(t, store, "agt-beta-codex", "upgrade-one", coremetadata.CodexGenerationDraining)
	requester := &fakeDrainingHandoverRequester{operationRef: "upgrade-one"}
	command.handover = requester
	before := store.snapshot()
	for i := range 2 {
		_, _, err := runRoute(t, command, "resume", "uid:agt-beta-codex")
		if err == nil || !strings.Contains(err.Error(), "handover-required operation=upgrade-one") {
			t.Fatalf("resume %d error = %v", i, err)
		}
	}
	if len(requester.endpoints) != 2 || requester.endpoints[0] != endpoint || requester.endpoints[1] != endpoint || store.snapshot() != before || store.transactions != 0 || len(tmux.calls) != 0 || len(launcher.plans) != 0 {
		t.Fatalf("handover reuse effects endpoints=%+v transactions=%d tmux=%v provider=%v", requester.endpoints, store.transactions, tmux.calls, launcher.plans)
	}
}

func testAgentWorkspaceResolver(spelling string, registry coremetadata.Registry, owner coremetadata.Project, provider, cwd string, additional []string) (coremetadata.AgentWorkspace, error) {
	effective := strings.TrimSpace(cwd)
	if effective == "" {
		effective = owner.Spec.Root
	}
	if strings.HasPrefix(effective, "/srv/") {
		return coremetadata.AgentWorkspace{CWD: effective, AdditionalWritableRoots: slices.Clone(additional)}, nil
	}
	return resolveAgentWorkspaceFor(spelling, registry, owner, provider, cwd, additional)
}

// splitWindowCalls returns every tmux split-window argv a run issued.
//
// A split-window is the only way this process starts a provider conversation, so
// counting them is how "a failed resume started no conversation" is measured as a
// number rather than as an error string.
func splitWindowCalls(tmux *fakeTmux) [][]string {
	var out [][]string
	for _, call := range tmux.calls {
		command := call
		if len(command) >= 3 && (command[0] == "-L" || command[0] == "-S") {
			command = command[2:]
		}
		if len(command) > 0 && command[0] == "split-window" {
			out = append(out, command)
		}
	}
	return out
}

// assertOnlyResumeLaunches pins both halves of the launch contract: how many
// conversations this run started, and that every one of them was the stored
// conversation rather than a fresh one.
func assertOnlyResumeLaunches(t *testing.T, tmux *fakeTmux, conversationID string, want int) {
	t.Helper()
	calls := splitWindowCalls(tmux)
	if len(calls) != want {
		t.Fatalf("the run issued %d split-window calls, want %d: %v", len(calls), want, calls)
	}
	for _, call := range calls {
		if !slices.ContainsFunc(call, func(arg string) bool { return strings.Contains(arg, conversationID) }) {
			t.Fatalf("a launched pane did not address the stored conversation %q: %v", conversationID, call)
		}
	}
}

// addFixtureAgent appends one more Agent to the fixture registry.
func addFixtureAgent(t *testing.T, store *fakeResourceStore, uid, name, windowUID string, phase coremetadata.AgentPhase, ref *coremetadata.AgentSessionRef) {
	t.Helper()
	store.registry.Agents = append(store.registry.Agents, coremetadata.Agent{
		APIVersion: coremetadata.APIVersion,
		Kind:       coremetadata.KindAgent,
		Metadata: coremetadata.ObjectMeta{
			UID: uid, Name: name,
			OwnerRef:  &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: windowUID},
			CreatedAt: resourceFixtureClock,
		},
		Spec: coremetadata.AgentSpec{Provider: "codex"},
		Status: coremetadata.AgentStatus{
			Phase: phase, LastTransitionAt: resourceFixtureClock, SessionRef: ref,
		},
	})
	rootUID := "prj-alpha"
	if windowUID == "win-beta-main" {
		rootUID = "prj-beta"
	} else if windowUID == "win-gone-main" {
		rootUID = "prj-gone"
	}
	store.registry.NameReservations = append(store.registry.NameReservations, coremetadata.NameReservation{
		Scope: rootUID, Kind: coremetadata.KindAgent, Name: name, UID: uid,
	})
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("fixture registry invalid after adding agent %q: %v", uid, err)
	}
}

// TestAgentResumeRebindsTheExistingAgentToANewManagedPane is acceptance
// criterion 1.
//
// The measurement is deliberately about identity rather than about success: the
// Agent count does not change, the uid and metadata.name are the ones that were
// already there, and the argv the split actually received addresses the stored
// conversation. A route that had quietly fallen through to a create would fail
// every one of those, not just the last.
func TestAgentResumeRebindsTheExistingAgentToANewManagedPane(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
	before, _ := store.registry.Agent("agt-beta-codex")
	before.Metadata.Annotations = map[string]string{coremetadata.AnnotationAgentTopic: "resume topic"}
	before.Spec.Workspace = coremetadata.AgentWorkspace{CWD: "/srv/beta", AdditionalWritableRoots: []string{"/srv/alpha"}}
	beforeUID, beforeName := before.Metadata.UID, before.Metadata.Name
	beforeAgentCount := len(store.registry.Agents)

	tmux := newFakeTmux()
	agent, launcher, ai, usage := newTestAgentResumeCommand(t, store, tmux)
	enablePinnedNativeResumeFixture(t, agent, store, "agt-beta-codex", launcher)

	stdout, stderr, err := runRoute(t, agent, "resume", "codex", "--project", "beta")
	if err != nil {
		t.Fatalf("agent resume error = %v", err)
	}
	if stdout != "agent/codex resumed\n" {
		t.Fatalf("stdout = %q, want the resumed result line", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want none for a conversation no other Agent shares", stderr)
	}
	if len(ai.calls) != 0 || len(usage.calls) != 0 {
		t.Fatalf("resume forwarded to ai=%q usage=%q, want neither", ai.calls, usage.calls)
	}

	// Identity is preserved. This is the half `create agent` can never satisfy.
	if got := len(store.registry.Agents); got != beforeAgentCount {
		t.Fatalf("agent count = %d, want %d: resume must never mint an Agent", got, beforeAgentCount)
	}
	after, ok := store.registry.Agent(beforeUID)
	if !ok {
		t.Fatalf("agent %q disappeared", beforeUID)
	}
	if after.Metadata.UID != beforeUID || after.Metadata.Name != beforeName {
		t.Fatalf("identity changed: uid %q->%q name %q->%q",
			beforeUID, after.Metadata.UID, beforeName, after.Metadata.Name)
	}
	if after.Status.Phase != coremetadata.PhaseRunning {
		t.Fatalf("phase = %q, want Running", after.Status.Phase)
	}
	if after.Status.PaneRef == "" {
		t.Fatal("status.paneRef is empty, so nothing was rebound")
	}
	pane, ok := store.registry.Pane(after.Status.PaneRef)
	if !ok {
		t.Fatalf("status.paneRef %q resolves to no Pane", after.Status.PaneRef)
	}
	if pane.Metadata.OwnerUID() != beforeUID || pane.Spec.Role != coremetadata.PaneRoleAgent {
		t.Fatalf("the new Pane is not this Agent's managed Pane: %#v", pane.Metadata)
	}
	// The durable conversation pointer is consumed, never rewritten, by resume.
	if !after.Status.SessionRef.SameConversation(before.Status.SessionRef) {
		t.Fatalf("resume changed the stored session ref: %#v", after.Status.SessionRef)
	}

	if store.transactions != 1 || store.writes != 1 {
		t.Fatalf("transactions=%d writes=%d, want 1/1", store.transactions, store.writes)
	}
	// Exactly one conversation was started, and it was the stored one.
	assertOnlyResumeLaunches(t, tmux, resumeFixtureConversation, 1)
	if len(launcher.plans) != 2 {
		t.Fatalf("the pinned resume was planned %d times, want preflight plus post-resume", len(launcher.plans))
	}
	if got := (launcher.plans[0]); got.provider != "codex" || got.conversationID != resumeFixtureConversation || got.contextDir != "/srv/beta" || !reflect.DeepEqual(got.additionalDirs, []string{"/srv/alpha"}) {
		t.Fatalf("resume launch request = %+v, want the stored codex conversation rooted at /srv/beta", got)
	}
	if len(launcher.bound) != 1 || launcher.bound[0].conversationID != resumeFixtureConversation {
		t.Fatalf("managed-pane bindings = %+v, want one seeded with the stored conversation", launcher.bound)
	}
	if launcher.bound[0].paneID == "" || launcher.bound[0].title != "codex:resume" {
		t.Fatalf("managed-pane binding = %+v, want the resumed pane id and the resume title", launcher.bound[0])
	}
	_, _, livePane := tmux.pane(launcher.bound[0].paneID)
	if livePane == nil || livePane.opts[aiPaneTopicOption] != "resume topic" || livePane.opts[aiPaneTopicManualOption] != "on" {
		t.Fatalf("resumed Pane topic = %+v, want stored topic/manual projection", livePane)
	}
	// The Settings enabled-agents gate is the same one `create agent` runs, and
	// it runs exactly once, before the launch is constructed.
	if !reflect.DeepEqual(launcher.gated, []string{"codex"}) {
		t.Fatalf("enabled-agents gate calls = %v, want exactly one for codex", launcher.gated)
	}
	// Resume is detached like create: it rebinds a pane, it does not move the
	// operator's view onto it.
	assertNoClientMovement(t, tmux)
}

func TestAgentResumeRoutesProductionAIPresentationWritesThroughExactRuntime(t *testing.T) {
	t.Parallel()
	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
	tmux := newFakeTmux()
	agent, planner, _, _ := newTestAgentResumeCommand(t, store, tmux)
	binder := testAICommand(t.TempDir())
	agent.rebind.launcher = &productionBindingResumeLauncher{fakeResumeLauncher: planner, binder: binder}
	enablePinnedNativeResumeFixture(t, agent, store, "agt-beta-codex", planner)
	bindTestCreateRuntimeRoute(agent.rebind.create, tmux, func(string) string { return "" })

	stdout, stderr, err := runRoute(t, agent, "resume", "codex", "--project", "uid:prj-beta")
	if err != nil || stdout != "agent/codex resumed\n" || stderr != "" {
		t.Fatalf("production-bound resume = stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if commands := cmdRecorder(binder).commands; len(commands) != 0 {
		t.Fatalf("production resume binder used ambient subprocess runner: %#v", commands)
	}
	for _, option := range []string{aiPaneManagedOption, aiPaneSessionIDOption, aiPaneResumeIDOption, aiPaneResumeUpdatedAtOption} {
		found := false
		for _, call := range tmux.calls {
			if slices.Contains(call, "set-option") && slices.Contains(call, option) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("production resume option %s was not written through exact route: %#v", option, tmux.calls)
		}
	}
	assertEveryTmuxCallHasExactRoute(t, tmux.calls)
}

func TestAgentResumeRefusesForeignSelectedSessionBeforeReconcileOrLaunch(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
	tmux := newFakeTmux()
	foreign := tmux.addSession("beta")
	foreign.opts[tmuxopts.ProjectUIDSession] = "prj-foreign"
	foreign.opts[tmuxopts.ProjectPathSession] = "/srv/beta"
	registryBefore, runtimeBefore := store.snapshot(), tmux.state()
	agent, launcher, _, _ := newTestAgentResumeCommand(t, store, tmux)
	enablePinnedNativeResumeFixture(t, agent, store, "agt-beta-codex", launcher)

	stdout, _, err := runRoute(t, agent, "resume", "codex", "--project", "beta")
	if err == nil || stdout != "" || !strings.Contains(err.Error(), "refuse foreign tmux session") {
		t.Fatalf("stdout/error = %q / %v", stdout, err)
	}
	if store.snapshot() != registryBefore || store.writes != 0 || tmux.state() != runtimeBefore {
		t.Fatal("foreign-session resume refusal mutated Registry or tmux")
	}
	if len(splitWindowCalls(tmux)) != 0 || len(launcher.bound) != 0 {
		t.Fatalf("foreign-session resume launched or bound a conversation: splits=%v bound=%v", splitWindowCalls(tmux), launcher.bound)
	}
	for _, call := range tmux.calls {
		if len(call) > 0 && slices.Contains([]string{"set-environment", "set-option", "rename-window", "new-window", "split-window"}, call[0]) {
			t.Fatalf("foreign-session resume refusal issued a tmux mutation: %v", call)
		}
	}
}

// TestAgentResumeFailuresStartNoConversationAtAll is acceptance criterion 2 and
// the single most important measurement in this Phase.
//
// Every row is a different reason a rebind cannot happen. What is asserted is
// not the wording but the count: zero new Agents, zero registry writes, and zero
// split-window calls, which together are "no conversation was started" -- neither
// a resumed one nor, crucially, a fresh one. A route that fell back to `create`
// on any of these rows would show a nonzero split count here.
func TestAgentResumeFailuresStartNoConversationAtAll(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		// prepare mutates the fixture into the failing shape.
		prepare func(t *testing.T, store *fakeResourceStore, launcher *fakeResumeLauncher)
		args    []string
		// wantUsage is true when the failure classifies as invalid input (exit 2)
		// rather than as a state or runtime failure (exit 1).
		wantUsage bool
		want      string
	}{
		{
			name:    "an Agent whose provider hook never ran has nothing to resume",
			prepare: func(*testing.T, *fakeResourceStore, *fakeResumeLauncher) {},
			args:    []string{"resume", "codex", "--project", "beta"},
			want:    "has no provider session ref",
		},
		{
			name:    "and it names create rather than performing it",
			prepare: func(*testing.T, *fakeResourceStore, *fakeResumeLauncher) {},
			args:    []string{"resume", "codex", "--project", "beta"},
			want:    "projmux create agent --provider <provider>",
		},
		{
			name: "a legacy stored conversation is never guessed current",
			prepare: func(t *testing.T, store *fakeResourceStore, launcher *fakeResumeLauncher) {
				setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
				launcher.planErr = errors.New("invalid codex resume id: contains control character")
			},
			args: []string{"resume", "codex", "--project", "beta"},
			want: codexNativeReasonLegacyEndpointMissing,
		},
		{
			name: "legacy endpoint refusal precedes provider launch planning",
			prepare: func(t *testing.T, store *fakeResourceStore, launcher *fakeResumeLauncher) {
				setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
				launcher.planErr = errors.New("selected runner is not installed: codex")
			},
			args: []string{"resume", "codex", "--project", "beta"},
			want: codexNativeReasonLegacyEndpointMissing,
		},
		{
			name: "a provider switched off in Settings is not resumable either",
			prepare: func(t *testing.T, store *fakeResourceStore, launcher *fakeResumeLauncher) {
				setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
				launcher.disabled["codex"] = true
			},
			args: []string{"resume", "codex", "--project", "beta"},
			want: "is disabled in Settings",
		},
		{
			name: "a Running Agent is refused exactly as before",
			prepare: func(t *testing.T, store *fakeResourceStore, _ *fakeResumeLauncher) {
				setFixtureSessionRef(t, store, "agt-alpha-codex", resumeFixtureRef(resourceFixtureClock))
			},
			args:      []string{"resume", "codex", "--project", "alpha"},
			wantUsage: true,
			want:      "already owns a managed Pane",
		},
		{
			name: "an Agent that still owns a Pane is refused rather than given a second one",
			prepare: func(t *testing.T, store *fakeResourceStore, _ *fakeResumeLauncher) {
				setFixtureSessionRef(t, store, "agt-alpha-codex", resumeFixtureRef(resourceFixtureClock))
				agent, _ := store.registry.Agent("agt-alpha-codex")
				agent.Status.Phase = coremetadata.PhaseFailed
			},
			args: []string{"resume", "codex", "--project", "alpha"},
			want: "refusing to bind a second managed Pane",
		},
		{
			name: "a ref that contradicts the Agent's declared provider is refused",
			prepare: func(t *testing.T, store *fakeResourceStore, _ *fakeResumeLauncher) {
				agent, _ := store.registry.Agent("agt-beta-codex")
				agent.Spec.Provider = "claude"
				agent.Status.SessionRef = resumeFixtureRef(resourceFixtureClock)
			},
			args: []string{"resume", "codex", "--project", "beta"},
			want: "refusing to resume a mismatched conversation",
		},
		{
			name: "a Project whose root disappeared is refused before any tmux call",
			prepare: func(t *testing.T, store *fakeResourceStore, _ *fakeResumeLauncher) {
				addFixtureAgent(t, store, "agt-gone-codex", "codex", "win-gone-main",
					coremetadata.PhaseOffline, resumeFixtureRef(resourceFixtureClock))
			},
			args:      []string{"resume", "uid:agt-gone-codex"},
			wantUsage: true,
			want:      "carries a MissingRoot condition",
		},
		{
			name:      "an ambiguous reference is still refused rather than guessed",
			prepare:   func(*testing.T, *fakeResourceStore, *fakeResumeLauncher) {},
			args:      []string{"resume", "codex"},
			wantUsage: true,
			want:      "want exactly one",
		},
		{
			name:      "a no-match is still refused",
			prepare:   func(*testing.T, *fakeResourceStore, *fakeResumeLauncher) {},
			args:      []string{"resume", "nosuch"},
			wantUsage: true,
			want:      "matched no agents",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			agent, launcher, ai, usage := newTestAgentResumeCommand(t, store, tmux)
			test.prepare(t, store, launcher)
			before := store.snapshot()
			beforeAgents := len(store.registry.Agents)
			beforeTmux := tmux.state()

			stdout, _, err := runRoute(t, agent, test.args...)
			if err == nil {
				t.Fatalf("agent %v succeeded, want a refusal", test.args)
			}
			if got := IsUsageError(err); got != test.wantUsage {
				t.Fatalf("agent %v usage-error = %v, want %v (err = %v)", test.args, got, test.wantUsage, err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("agent %v error = %q, want it to mention %q", test.args, err, test.want)
			}
			if stdout != "" {
				t.Fatalf("agent %v wrote %q to stdout, want 0 bytes", test.args, stdout)
			}

			// The counted properties. Each is the same claim from a different
			// layer, and all three must read zero.
			if got := len(store.registry.Agents) - beforeAgents; got != 0 {
				t.Fatalf("a failed resume created %d Agents, want 0", got)
			}
			if store.transactions != 0 || store.writes != 0 {
				t.Fatalf("a failed resume opened %d transactions and committed %d writes, want 0/0",
					store.transactions, store.writes)
			}
			if got := len(splitWindowCalls(tmux)); got != 0 {
				t.Fatalf("a failed resume started %d conversations, want 0: %v", got, splitWindowCalls(tmux))
			}
			if len(tmux.calls) != 0 {
				t.Fatalf("a failed resume issued %d tmux calls, want 0: %v", len(tmux.calls), tmux.calls)
			}
			if len(launcher.bound) != 0 {
				t.Fatalf("a failed resume bound %d managed panes, want 0", len(launcher.bound))
			}
			if store.snapshot() != before {
				t.Fatalf("a failed resume mutated the registry:\n--- before ---\n%s\n--- after ---\n%s", before, store.snapshot())
			}
			if tmux.state() != beforeTmux {
				t.Fatalf("a failed resume mutated the tmux server")
			}
			if len(ai.calls) != 0 || len(usage.calls) != 0 {
				t.Fatalf("a failed resume forwarded to ai=%q usage=%q, want neither", ai.calls, usage.calls)
			}
		})
	}
}

// TestAResumeRuntimeFailureRollsBackAndStartsNoFreshConversation is the second
// half of acceptance criterion 2: the failure that happens *after* the store is
// open.
//
// The tmux split is made to fail, which is the one class of failure that can
// reach a half-applied state. What must hold is that the transaction commits
// nothing, the Agent is still Offline with no Pane, and the only launch that was
// ever attempted addressed the stored conversation -- there is no second,
// fresh-start attempt anywhere on the path.
func TestAResumeRuntimeFailureRollsBackAndStartsNoFreshConversation(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(resourceFixtureClock))
	beforeAgents := len(store.registry.Agents)
	beforePanes := len(store.registry.Panes)

	tmux := newFakeTmux()
	tmux.fail = []string{"split-window"}
	tmux.failMessage = "no space for new pane"
	agent, launcher, _, _ := newTestAgentResumeCommand(t, store, tmux)
	enablePinnedNativeResumeFixture(t, agent, store, "agt-beta-codex", launcher)

	stdout, _, err := runRoute(t, agent, "resume", "codex", "--project", "beta")
	if err == nil {
		t.Fatal("agent resume succeeded despite a failing split")
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want 0 bytes", stdout)
	}
	if store.writes != 0 {
		t.Fatalf("a rolled-back resume committed %d writes, want 0", store.writes)
	}
	if got := len(store.registry.Agents); got != beforeAgents {
		t.Fatalf("agent count = %d, want %d", got, beforeAgents)
	}
	if got := len(store.registry.Panes); got != beforePanes {
		t.Fatalf("pane count = %d, want %d", got, beforePanes)
	}
	after, _ := store.registry.Agent("agt-beta-codex")
	if after.Status.Phase != coremetadata.PhaseOffline || after.Status.PaneRef != "" {
		t.Fatalf("the Agent did not stay Offline: phase=%q paneRef=%q", after.Status.Phase, after.Status.PaneRef)
	}
	if after.Status.SessionRef == nil || after.Status.SessionRef.Codex == nil ||
		after.Status.SessionRef.Codex.ThreadID != resumeFixtureConversation || after.Status.SessionRef.Codex.Endpoint == nil {
		t.Fatal("a failed resume disturbed the stored conversation pointer")
	}
	// Exactly one launch was attempted and it was the resume. No fallback ran.
	assertOnlyResumeLaunches(t, tmux, resumeFixtureConversation, 1)
	if len(launcher.bound) != 0 {
		t.Fatalf("a failed split still bound %d managed panes, want 0", len(launcher.bound))
	}
}

// TestAgentResumeNeverBuildsAFreshStartLaunch is the structural half of the
// no-fallback contract.
//
// The route holds an agentResumeLauncher, whose only launch method takes a
// conversation id. It deliberately does not hold the agentLauncher `create agent`
// uses, so there is no fresh-start argv it could build even by mistake. This
// pins that by reflection, which is what stops a future edit from re-adding the
// create seam to the resume path and reintroducing the silent fallback.
func TestAgentResumeNeverBuildsAFreshStartLaunch(t *testing.T) {
	t.Parallel()

	resume := reflect.TypeFor[agentResumeLauncher]()
	create := reflect.TypeFor[agentLauncher]()
	if resume.Implements(create) {
		t.Fatal("the resume launch seam also satisfies the create launch seam; a fresh-start argv is reachable from the resume path")
	}
	if _, ok := resume.MethodByName("PlanAgentLaunch"); ok {
		t.Fatal("the resume launch seam exposes the fresh-start launch builder")
	}
	method, ok := resume.MethodByName("PlanAgentResume")
	if !ok {
		t.Fatal("the resume launch seam has no resume launch builder")
	}
	// (provider, contextDir, conversationID): the conversation is a required
	// input, so an argv cannot be built without one.
	if got := method.Type.NumIn(); got != 3 {
		t.Fatalf("PlanAgentResume takes %d inputs, want 3 including the conversation id", got)
	}

	rebinder := reflect.TypeFor[agentRebinder]()
	for i := range rebinder.NumField() {
		if rebinder.Field(i).Type == create {
			t.Fatal("the rebinder holds the create launch seam")
		}
	}
}

// TestSeveralAgentsOnOneConversationResolveDeterministically is acceptance
// criterion 5.
//
// Phase 0 declared several Agents pointing at one conversation to be legal state
// and left the tie-break to this Phase. The rule this pins is that the
// conversation is never a selector: the reference decides, duplicates are
// disclosed in uid order and never redirect or block the rebind. The registry
// order is permuted so a rule that accidentally depended on it would fail.
func TestSeveralAgentsOnOneConversationResolveDeterministically(t *testing.T) {
	t.Parallel()

	shared := func() *coremetadata.AgentSessionRef {
		ref := resumeFixtureRef(resourceFixtureClock)
		endpoint := nativeTestRoute("generation-resume-fixture", coremetadata.CodexGenerationCurrent).Endpoint
		ref.Codex.Endpoint = &endpoint
		ref.Codex.Lifecycle = &coremetadata.CodexGenerationLifecycleRef{State: coremetadata.CodexGenerationCurrent}
		return ref
	}

	var firstStderr string
	var firstArgv []string
	for permutation := range 3 {
		store := newFakeResourceStore(t)
		// Three Agents record one conversation: the Running one in alpha, a
		// second Offline one in alpha/review, and the target in beta.
		setFixtureSessionRef(t, store, "agt-alpha-codex", shared())
		setFixtureSessionRef(t, store, "agt-beta-codex", shared())
		addFixtureAgent(t, store, "agt-review-codex", "codex-review", "win-alpha-review", coremetadata.PhaseOffline, shared())

		// Rotate the registry order. A conversation-keyed selection, or an
		// unsorted disclosure, would move with it.
		for range permutation {
			store.registry.Agents = append(store.registry.Agents[1:], store.registry.Agents[0])
		}

		tmux := newFakeTmux()
		agent, launcher, _, _ := newTestAgentResumeCommand(t, store, tmux)
		enablePinnedNativeResumeFixture(t, agent, store, "agt-beta-codex", launcher)
		stdout, stderr, err := runRoute(t, agent, "resume", "uid:agt-beta-codex")
		if err != nil {
			t.Fatalf("permutation %d: agent resume error = %v", permutation, err)
		}
		if stdout != "agent/codex resumed\n" {
			t.Fatalf("permutation %d: stdout = %q", permutation, stdout)
		}
		// The referenced Agent is the one that got the Pane, every time.
		target, _ := store.registry.Agent("agt-beta-codex")
		if target.Status.Phase != coremetadata.PhaseRunning || target.Status.PaneRef == "" {
			t.Fatalf("permutation %d: the referenced Agent was not the one rebound: %+v", permutation, target.Status)
		}
		// And none of the conversation's other Agents moved.
		for _, uid := range []string{"agt-alpha-codex", "agt-review-codex"} {
			other, _ := store.registry.Agent(uid)
			if uid == "agt-review-codex" && other.Status.Phase != coremetadata.PhaseOffline {
				t.Fatalf("permutation %d: a conversation sibling changed phase: %+v", permutation, other.Status)
			}
			if !other.Status.SessionRef.SameConversation(shared()) {
				t.Fatalf("permutation %d: resume rewrote a sibling's conversation pointer", permutation)
			}
		}
		if len(launcher.plans) != 2 {
			t.Fatalf("permutation %d: %d pinned plans, want preflight plus post-resume", permutation, len(launcher.plans))
		}
		argv := splitWindowCalls(tmux)[0]

		if permutation == 0 {
			firstStderr, firstArgv = stderr, argv
			// The disclosure names both siblings, in uid order, and says which
			// Agent was actually rebound.
			for _, want := range []string{
				"agent/codex (uid:agt-alpha-codex)",
				"agent/codex-review (uid:agt-review-codex)",
				"rebinds only agent/codex",
			} {
				if !strings.Contains(stderr, want) {
					t.Fatalf("disclosure = %q, want it to mention %q", stderr, want)
				}
			}
			if got := strings.Count(stderr, "\n"); got != 2 {
				t.Fatalf("disclosure has %d lines, want one per sibling: %q", got, stderr)
			}
			if strings.Index(stderr, "agt-alpha-codex") > strings.Index(stderr, "agt-review-codex") {
				t.Fatalf("disclosure is not in uid order: %q", stderr)
			}
			continue
		}
		if stderr != firstStderr {
			t.Fatalf("permutation %d disclosure diverged:\n%q\nwant\n%q", permutation, stderr, firstStderr)
		}
		if !reflect.DeepEqual(argv, firstArgv) {
			t.Fatalf("permutation %d launched a different argv:\n%v\nwant\n%v", permutation, argv, firstArgv)
		}
	}
}

// TestObservedAtIsNotAResumeGate records the adjudication of the second Phase 0
// handoff item.
//
// `observedAt` is when projmux last saw the conversation, not a provider
// timestamp, so it cannot answer "is this ref stale". The route therefore never
// reads it, and the proof is that two refs identical except for an observedAt a
// decade apart produce byte-identical outcomes -- including the far-future one,
// which any wall-clock heuristic would have to treat differently from the
// ancient one.
func TestObservedAtIsNotAResumeGate(t *testing.T) {
	t.Parallel()

	run := func(t *testing.T, observedAt time.Time) (string, string, []string) {
		t.Helper()
		store := newFakeResourceStore(t)
		setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(observedAt))
		tmux := newFakeTmux()
		agent, launcher, _, _ := newTestAgentResumeCommand(t, store, tmux)
		enablePinnedNativeResumeFixture(t, agent, store, "agt-beta-codex", launcher)
		stdout, stderr, err := runRoute(t, agent, "resume", "codex", "--project", "beta")
		if err != nil {
			t.Fatalf("observedAt %s: agent resume error = %v", observedAt, err)
		}
		after, _ := store.registry.Agent("agt-beta-codex")
		if after.Status.Phase != coremetadata.PhaseRunning {
			t.Fatalf("observedAt %s: phase = %q, want Running", observedAt, after.Status.Phase)
		}
		return stdout, stderr, splitWindowCalls(tmux)[0]
	}

	ancient := resourceFixtureClock.AddDate(-10, 0, 0)
	future := resourceFixtureClock.AddDate(10, 0, 0)
	wantOut, wantErr, wantArgv := run(t, ancient)
	gotOut, gotErr, gotArgv := run(t, future)
	if gotOut != wantOut || gotErr != wantErr {
		t.Fatalf("observedAt changed the streams: %q/%q vs %q/%q", gotOut, gotErr, wantOut, wantErr)
	}
	if !reflect.DeepEqual(gotArgv, wantArgv) {
		t.Fatalf("observedAt changed the launched argv:\n%v\nvs\n%v", gotArgv, wantArgv)
	}
	// And the value itself survives the rebind untouched: resume consumes the
	// pointer, it does not re-observe it.
	store := newFakeResourceStore(t)
	setFixtureSessionRef(t, store, "agt-beta-codex", resumeFixtureRef(ancient))
	agent, launcher, _, _ := newTestAgentResumeCommand(t, store, newFakeTmux())
	enablePinnedNativeResumeFixture(t, agent, store, "agt-beta-codex", launcher)
	if _, _, err := runRoute(t, agent, "resume", "codex", "--project", "beta"); err != nil {
		t.Fatalf("agent resume error = %v", err)
	}
	after, _ := store.registry.Agent("agt-beta-codex")
	if !after.Status.SessionRef.ObservedAt.Equal(ancient) {
		t.Fatalf("observedAt = %s, want the stored %s: resume must not rewrite the observation",
			after.Status.SessionRef.ObservedAt, ancient)
	}
}

// TestProviderResumeArgvAddressesTheConversationAndCarriesNoTurnID records the
// adjudication of the third Phase 0 handoff item.
//
// Codex reports a turn id and Phase 0 deliberately did not store it. This Phase
// does not need it: `codex resume <thread-id>` has no turn slot at all, so
// resume is a conversation-granularity operation for every provider, uniformly.
// Turn-level resume would need a provider CLI that accepts a turn on resume, a
// durable home for a value that changes every turn, and a product decision about
// what rewinding means -- none of which is this Phase's, and none of which is
// implemented here. The argv shapes are pinned so a future turn slot cannot
// appear unnoticed.
func TestProviderResumeArgvAddressesTheConversationAndCarriesNoTurnID(t *testing.T) {
	t.Parallel()

	const uuid = "7ceaf499-728a-482e-97cd-1c0420efc7e2"
	for _, test := range []struct {
		provider string
		id       string
		want     []string
	}{
		{provider: aiModeClaude, id: uuid, want: []string{"claude", "--resume", uuid}},
		{provider: aiModeCodex, id: "thr-1", want: []string{"codex", "resume", "thr-1"}},
		{provider: aiModeAntigravity, id: uuid, want: []string{"agy", "--conversation", uuid}},
	} {
		t.Run(test.provider, func(t *testing.T) {
			t.Parallel()
			_, cell, ok := aiprovider.LookupAgentCapability("resume", aiprovider.ID(test.provider))
			if !ok || cell.Mode != aiprovider.SupportProviderResume {
				t.Fatalf("resume capability for %s = %#v", test.provider, cell)
			}
			got, err := resumeArgsForAgent(test.provider, test.id)
			if err != nil {
				t.Fatalf("resumeArgsForAgent(%s) error = %v", test.provider, err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("resume argv = %v, want %v", got, test.want)
			}
			// Three elements exactly: binary, verb/flag, conversation. A turn id
			// would have to be a fourth, and there is nowhere to put it.
			if len(got) != 3 {
				t.Fatalf("resume argv has %d elements, want 3 with no turn slot: %v", len(got), got)
			}

			store := newFakeResourceStore(t)
			agent, _ := store.registry.Agent("agt-beta-codex")
			agent.Spec.Provider = test.provider
			observation := coremetadata.AgentSessionObservation{Provider: test.provider}
			if test.provider == aiModeCodex {
				observation.ThreadID = test.id
			} else {
				observation.SessionID = test.id
			}
			ref, valid := coremetadata.NewAgentSessionRef(observation, resourceFixtureClock)
			if !valid {
				t.Fatalf("session fixture for %s was rejected", test.provider)
			}
			agent.Status.SessionRef = ref
			plan, err := planAgentResume("agent resume", store.registry, agent)
			if err != nil {
				t.Fatalf("planAgentResume(%s): %v", test.provider, err)
			}
			if plan.agentUID != agent.Metadata.UID || plan.provider != test.provider || plan.conversationID != test.id || len(store.registry.Agents) != 2 {
				t.Fatalf("resume plan changed identity or conversation: %#v", plan)
			}
		})
	}

	// An unusable conversation id is an error rather than a fresh start. These
	// are the failures the app layer surfaces as "cannot resume <conversation>".
	for _, test := range []struct {
		name     string
		provider string
		id       string
	}{
		{name: "empty claude id", provider: aiModeClaude, id: "  "},
		{name: "control character in a codex id", provider: aiModeCodex, id: "thr\x00"},
		{name: "an antigravity id that is not a conversation uuid", provider: aiModeAntigravity, id: "thr-1"},
		{name: "an unknown provider", provider: "gpt", id: uuid},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if argv, err := resumeArgsForAgent(test.provider, test.id); err == nil {
				t.Fatalf("resumeArgsForAgent(%s, %q) = %v, want an error", test.provider, test.id, argv)
			}
		})
	}

	// The sentinel identity is what the app layer relies on to classify these.
	for _, err := range []error{claude.ErrInvalidResumeID, codex.ErrInvalidResumeID, antigravity.ErrInvalidResumeID} {
		if err == nil {
			t.Fatal("a provider lost its invalid-resume-id sentinel")
		}
	}
}

func TestProviderResumeExecutionPreservesExactAgentAndLaunchesOneExactArgv(t *testing.T) {
	const uuid = "7ceaf499-728a-482e-97cd-1c0420efc7e2"
	for _, test := range []struct {
		provider string
		id       string
	}{
		{provider: aiModeCodex, id: resumeFixtureConversation},
		{provider: aiModeClaude, id: uuid},
		{provider: aiModeAntigravity, id: uuid},
	} {
		t.Run(test.provider, func(t *testing.T) {
			store := newFakeResourceStore(t)
			target, _ := store.registry.Agent("agt-beta-codex")
			target.Spec.Provider = test.provider
			observation := coremetadata.AgentSessionObservation{Provider: test.provider, SessionID: test.id}
			if test.provider == aiModeCodex {
				observation.SessionID = ""
				observation.ThreadID = test.id
			}
			ref, ok := coremetadata.NewAgentSessionRef(observation, resourceFixtureClock)
			if !ok {
				t.Fatalf("session fixture for %s was rejected", test.provider)
			}
			target.Status.SessionRef = ref
			beforeUID, beforeName := target.Metadata.UID, target.Metadata.Name
			beforeAgentCount := len(store.registry.Agents)

			tmux := newFakeTmux()
			command, recorder, _, _ := newTestAgentResumeCommand(t, store, tmux)
			var wantChild []string
			if test.provider == aiModeCodex {
				enablePinnedNativeResumeFixture(t, command, store, beforeUID, recorder)
				route := nativeTestRoute("generation-resume-fixture", coremetadata.CodexGenerationCurrent)
				wantChild = []string{route.TUIExecutable, "resume", "--remote", "unix://" + route.SocketPath, test.id}
			} else {
				launcher := &exactArgvResumeLauncher{fakeResumeLauncher: recorder, planner: agentLaunchArgvTestCommand(t)}
				command.rebind.launcher = launcher
			}

			stdout, stderr, err := runRoute(t, command, "resume", "uid:"+beforeUID)
			if err != nil {
				t.Fatalf("resume %s: stdout=%q stderr=%q err=%v", test.provider, stdout, stderr, err)
			}
			calls := splitWindowCalls(tmux)
			if len(calls) != 1 {
				t.Fatalf("%s split-window calls = %v, want exactly one provider launch", test.provider, calls)
			}
			separator := -1
			for index, arg := range calls[0] {
				if arg == "--" {
					separator = index
				}
			}
			if separator < 0 {
				t.Fatalf("%s supervised launch has no child argv separator: %v", test.provider, calls[0])
			}
			if test.provider != aiModeCodex {
				planned := command.rebind.launcher.(*exactArgvResumeLauncher).argv
				if len(planned) != 1 {
					t.Fatalf("%s planned %d provider argv values, want 1", test.provider, len(planned))
				}
				wantChild = planned[0]
			}
			if got := calls[0][separator+1:]; !slices.Equal(got, wantChild) {
				t.Fatalf("%s launched child argv = %q, want exact %q", test.provider, got, wantChild)
			}
			after, ok := store.registry.Agent(beforeUID)
			if !ok || after.Metadata.UID != beforeUID || after.Metadata.Name != beforeName || len(store.registry.Agents) != beforeAgentCount {
				t.Fatalf("%s resume changed Agent identity or created a fresh Agent: before=%s/%s count=%d after=%#v count=%d",
					test.provider, beforeUID, beforeName, beforeAgentCount, after.Metadata, len(store.registry.Agents))
			}
		})
	}
}

// TestPlanAgentResumeReadsOnlyWhatTheHookAlreadyGaveUs is the permanent-exclusion
// guard.
//
// Reading a provider's own configuration or transcript store is out of scope for
// good. A Claude ref carries a transcript *path*; the plan must ignore it
// entirely, so a run whose transcript path points at a file that does not exist,
// or at nothing at all, behaves identically.
func TestPlanAgentResumeReadsOnlyWhatTheHookAlreadyGaveUs(t *testing.T) {
	t.Parallel()

	plan := func(t *testing.T, transcript string) agentResumePlan {
		t.Helper()
		store := newFakeResourceStore(t)
		agent, _ := store.registry.Agent("agt-beta-codex")
		agent.Spec.Provider = "claude"
		agent.Status.SessionRef = &coremetadata.AgentSessionRef{
			Provider:   "claude",
			ObservedAt: resourceFixtureClock,
			Claude: &coremetadata.ClaudeSessionRef{
				SessionID:      "sess-1",
				TranscriptPath: transcript,
			},
		}
		got, err := planAgentResume("agent resume", store.registry, agent)
		if err != nil {
			t.Fatalf("planAgentResume error = %v", err)
		}
		return got
	}

	absent := plan(t, filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	none := plan(t, "")
	if absent.conversationID != "sess-1" || none.conversationID != "sess-1" {
		t.Fatalf("the conversation id depends on the transcript path: %q vs %q", absent.conversationID, none.conversationID)
	}
	if absent.provider != "claude" || none.provider != "claude" {
		t.Fatalf("provider = %q/%q, want claude", absent.provider, none.provider)
	}
	if !reflect.DeepEqual(absent.shared, none.shared) {
		t.Fatalf("the transcript path reached the tie-break: %v vs %v", absent.shared, none.shared)
	}
}

// TestResumeReusesTheCreateRuntimeRatherThanASecondMaterializer pins that the
// rebind rides the create command's own transaction, ledger, and detached
// materializer. A second implementation of any of those would be a second set of
// rollback bugs, and it would also be a second place a fresh-start launch could
// appear.
func TestResumeReusesTheCreateRuntimeRatherThanASecondMaterializer(t *testing.T) {
	t.Parallel()

	app := New()
	if app.agent == nil || app.agent.rebind == nil {
		t.Fatal("the application graph does not wire the resume rebinder")
	}
	if app.agent.rebind.create != app.create {
		t.Fatal("agent resume does not share the create command's runtime")
	}
	if app.agent.rebind.launcher != agentResumeLauncher(app.ai) {
		t.Fatal("agent resume does not share the AI command's provider seam")
	}
	// The materializer is the object that owns `-d`, so sharing it is what makes
	// "resume never moves the client" the same guarantee create already has.
	if app.create.runtime == nil || app.create.runtime.runner == nil || app.create.runtime.sessions == nil {
		t.Fatal("the shared runtime is not fully wired")
	}
}

// TestTheRealResumeSeamRefusesAnUnusableConversationBeforeTouchingTheProvider
// covers the production seam rather than the fake.
//
// Both rows fail inside the provider's own resume-argv builder, which runs
// before anything looks for a binary on disk, so the assertion is filesystem
// independent. What it pins is that the seam has no path that turns an unusable
// conversation id into a launch of any kind.
func TestTheRealResumeSeamRefusesAnUnusableConversationBeforeTouchingTheProvider(t *testing.T) {
	t.Parallel()

	c := &aiCommand{}
	for _, test := range []struct {
		name           string
		provider       string
		conversationID string
	}{
		{name: "a malformed codex conversation id", provider: aiModeCodex, conversationID: "thr\x00"},
		{name: "an empty claude conversation id", provider: aiModeClaude, conversationID: "   "},
		{name: "a provider that is not an agent provider", provider: "gpt", conversationID: "whatever"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			title, argv, err := c.PlanAgentResume(test.provider, coremetadata.AgentWorkspace{CWD: "/srv/beta"}, test.conversationID)
			if err == nil {
				t.Fatalf("PlanAgentResume returned title=%q argv=%v, want an error", title, argv)
			}
			if title != "" || argv != nil {
				t.Fatalf("a failed resume plan still produced title=%q argv=%v", title, argv)
			}
		})
	}
}
