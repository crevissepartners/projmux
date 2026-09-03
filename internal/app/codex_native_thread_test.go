package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

var errFakeNativeUnavailable = errors.New("fake app-server unavailable")

type fakeNativeThreadController struct {
	currentRoute    codexNativeEndpointRoute
	catalogRoutes   []codexNativeEndpointRoute
	resolvedRoute   codexNativeEndpointRoute
	currentErr      error
	resolveErr      error
	createBinding   codexappserver.ThreadBinding
	resumeBinding   codexappserver.ThreadBinding
	createErr       error
	resumeErr       error
	fallback        bool
	nextThread      int
	creates         []fakeNativeCreate
	resumes         []fakeNativeResume
	resolveStarted  chan struct{}
	resolveContinue chan struct{}
}

type fakeNativeCreate struct {
	route      codexNativeEndpointRoute
	workspace  coremetadata.AgentWorkspace
	prompt     string
	generation string
}

type fakeNativeResume struct {
	route     codexNativeEndpointRoute
	workspace coremetadata.AgentWorkspace
	threadID  string
}

func (f *fakeNativeThreadController) Current(context.Context) (codexNativeEndpointRoute, error) {
	if !f.currentRoute.valid() && f.currentErr == nil {
		f.currentRoute = nativeTestRoute("generation-current", coremetadata.CodexGenerationCurrent)
	}
	return f.currentRoute, f.currentErr
}

func (f *fakeNativeThreadController) CatalogRoutes(ctx context.Context) ([]codexNativeEndpointRoute, error) {
	if len(f.catalogRoutes) != 0 {
		return append([]codexNativeEndpointRoute(nil), f.catalogRoutes...), f.currentErr
	}
	route, err := f.Current(ctx)
	if err != nil {
		return nil, err
	}
	return []codexNativeEndpointRoute{route}, nil
}

func (f *fakeNativeThreadController) Resolve(_ context.Context, endpoint coremetadata.CodexEndpointRef) (codexNativeEndpointRoute, error) {
	if f.resolveStarted != nil {
		f.resolveStarted <- struct{}{}
	}
	if f.resolveContinue != nil {
		<-f.resolveContinue
	}
	if !f.resolvedRoute.valid() && f.resolveErr == nil {
		f.resolvedRoute = nativeTestRoute(endpoint.EndpointGenerationID, coremetadata.CodexGenerationCurrent)
		f.resolvedRoute.Endpoint = endpoint
	}
	return f.resolvedRoute, f.resolveErr
}

func (f *fakeNativeThreadController) Create(_ context.Context, route codexNativeEndpointRoute, workspace coremetadata.AgentWorkspace, prompt, generation string) (codexappserver.ThreadBinding, error) {
	f.creates = append(f.creates, fakeNativeCreate{route: route, workspace: workspace, prompt: prompt, generation: generation})
	binding := f.createBinding
	if binding.ThreadID == "" && f.createErr == nil {
		f.nextThread++
		binding.ThreadID = fmt.Sprintf("thread-native-test-%d", f.nextThread)
		if prompt != "" {
			binding.TurnID = fmt.Sprintf("turn-native-test-%d", f.nextThread)
		}
	}
	return binding, f.createErr
}

func (f *fakeNativeThreadController) Resume(_ context.Context, route codexNativeEndpointRoute, workspace coremetadata.AgentWorkspace, threadID string) (codexappserver.ThreadBinding, error) {
	f.resumes = append(f.resumes, fakeNativeResume{route: route, workspace: workspace, threadID: threadID})
	return f.resumeBinding, f.resumeErr
}

func (f *fakeNativeThreadController) CanFallback(error) bool { return f.fallback }

func nativeTestRoute(generation string, state coremetadata.CodexGenerationState) codexNativeEndpointRoute {
	return codexNativeEndpointRoute{
		Endpoint: coremetadata.CodexEndpointRef{StateDomainID: "test-domain", EndpointGenerationID: generation},
		State:    state, SocketPath: "/test/" + generation + ".sock", TUIExecutable: "/test/" + generation + "/codex",
	}
}

func TestCodexNativeEndpointRouteTransportShapeIsClosed(t *testing.T) {
	exactPrivate := nativeTestRoute("generation-shape", coremetadata.CodexGenerationCurrent)
	exactDefault := exactPrivate
	exactDefault.Default = true
	exactDefault.SocketPath = ""
	for _, test := range []struct {
		name   string
		mutate func(*codexNativeEndpointRoute)
		base   codexNativeEndpointRoute
		want   bool
	}{
		{name: "private exact", base: exactPrivate, want: true},
		{name: "default exact", base: exactDefault, want: true},
		{name: "default with socket", base: exactDefault, mutate: func(route *codexNativeEndpointRoute) { route.SocketPath = "/tmp/ignored.sock" }},
		{name: "private missing socket", base: exactPrivate, mutate: func(route *codexNativeEndpointRoute) { route.SocketPath = "" }},
		{name: "private relative socket", base: exactPrivate, mutate: func(route *codexNativeEndpointRoute) { route.SocketPath = "relative.sock" }},
		{name: "missing state domain", base: exactPrivate, mutate: func(route *codexNativeEndpointRoute) { route.Endpoint.StateDomainID = "" }},
		{name: "missing generation", base: exactPrivate, mutate: func(route *codexNativeEndpointRoute) { route.Endpoint.EndpointGenerationID = "" }},
		{name: "missing state", base: exactPrivate, mutate: func(route *codexNativeEndpointRoute) { route.State = "" }},
		{name: "relative executable", base: exactPrivate, mutate: func(route *codexNativeEndpointRoute) { route.TUIExecutable = "codex" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			route := test.base
			if test.mutate != nil {
				test.mutate(&route)
			}
			if got := route.valid(); got != test.want {
				t.Fatalf("route.valid()=%t want=%t route=%+v", got, test.want, route)
			}
		})
	}
}

func TestDefaultCodexStateDomainIdentityUsesExactCanonicalCodexHome(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	lookup := func(root string) func(string) string {
		return func(key string) string {
			if key == "CODEX_HOME" {
				return root
			}
			return ""
		}
	}
	firstID, err := defaultCodexStateDomainID(lookup(first), func() (string, error) { return "", errors.New("must not read home") })
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := defaultCodexStateDomainID(lookup(second), func() (string, error) { return "", errors.New("must not read home") })
	if err != nil {
		t.Fatal(err)
	}
	if firstID == secondID || strings.Contains(firstID, first) || strings.Contains(secondID, second) {
		t.Fatalf("state domains alias or leak paths: first=%q second=%q", firstID, secondID)
	}
	link := filepath.Join(t.TempDir(), "codex-home-link")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	linkID, err := defaultCodexStateDomainID(lookup(link), func() (string, error) { return "", errors.New("must not read home") })
	if err != nil || linkID != firstID {
		t.Fatalf("canonical alias id=%q err=%v want=%q", linkID, err, firstID)
	}
	if _, err := defaultCodexStateDomainID(lookup("relative/codex-home"), func() (string, error) { return "", nil }); err == nil {
		t.Fatal("relative CODEX_HOME gained a durable state domain")
	}
}

func TestDefaultCodexControllerRefusesStoredEndpointAfterCodexHomeChanges(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	domainID := func(root string) string {
		t.Helper()
		id, err := defaultCodexStateDomainID(func(key string) string {
			if key == "CODEX_HOME" {
				return root
			}
			return ""
		}, func() (string, error) { return "", errors.New("must not read home") })
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	stored := nativeTestRoute("codex-0.152.1", coremetadata.CodexGenerationCurrent)
	stored.Endpoint.StateDomainID = domainID(first)
	current := stored
	current.Endpoint.StateDomainID = domainID(second)
	controller := defaultCodexNativeThreadController{current: func(context.Context) (codexNativeEndpointRoute, error) { return current, nil }}
	if _, err := controller.Resolve(context.Background(), stored.Endpoint); err == nil || !strings.Contains(err.Error(), codexNativeReasonGenerationUnavailable) {
		t.Fatalf("changed CODEX_HOME reattached stored endpoint: %v", err)
	}
}

func nativeTestSessionRef(route codexNativeEndpointRoute, threadID string) *coremetadata.AgentSessionRef {
	endpoint := route.Endpoint
	lifecycle := &coremetadata.CodexGenerationLifecycleRef{State: route.State}
	if route.State == coremetadata.CodexGenerationDraining || route.State == coremetadata.CodexGenerationHandoverPending {
		lifecycle.Operation = &coremetadata.CodexGenerationOperationRef{ID: "test-handover", Endpoint: endpoint}
	}
	return &coremetadata.AgentSessionRef{
		Provider: aiModeCodex,
		Codex: &coremetadata.CodexSessionRef{
			ThreadID: threadID, Endpoint: &endpoint,
			Lifecycle: lifecycle,
		},
	}
}

func TestCodexNativeResumeRouteUsesDurableEndpointAndRefusesLegacyOrDraining(t *testing.T) {
	current := nativeTestRoute("generation-current", coremetadata.CodexGenerationCurrent)
	controller := &fakeNativeThreadController{resolvedRoute: current}
	if got, err := resolveCodexNativeResumeRoute(context.Background(), controller, nativeTestSessionRef(current, "thread-exact")); err != nil || !got.Endpoint.Same(current.Endpoint) {
		t.Fatalf("current route = %+v, %v", got, err)
	}
	if len(controller.resumes) != 0 {
		t.Fatalf("route selection wrote provider: %+v", controller.resumes)
	}
	for _, test := range []struct {
		name string
		ref  *coremetadata.AgentSessionRef
		want string
	}{
		{name: "legacy", ref: &coremetadata.AgentSessionRef{Provider: aiModeCodex, Codex: &coremetadata.CodexSessionRef{ThreadID: "thread-exact"}}, want: codexNativeReasonLegacyEndpointMissing},
		{name: "draining", ref: nativeTestSessionRef(nativeTestRoute("generation-old", coremetadata.CodexGenerationDraining), "thread-exact"), want: codexNativeReasonHandoverRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := resolveCodexNativeResumeRoute(context.Background(), controller, test.ref)
			var routeErr *codexNativeRouteError
			if !errors.As(err, &routeErr) || routeErr.Reason != test.want {
				t.Fatalf("error=%v, want %s", err, test.want)
			}
		})
	}
}

type fakeNativePaneLauncher struct {
	plans                     []fakeNativePanePlan
	bound                     []fakeNativePaneBinding
	lifecycle                 []codexLifecycleObserverTarget
	lifecycleBindingCurrent   func(codexLifecycleIdentity) (registryCurrent, paneUIDCurrent bool)
	lifecycleObservedRegistry []bool
	lifecycleObservedPaneUID  []bool
}

type fakeNativePanePlan struct {
	route     codexNativeEndpointRoute
	workspace coremetadata.AgentWorkspace
	threadID  string
}

type fakeNativePaneBinding struct {
	paneID, contextDir, title, threadID string
}

func (f *fakeNativePaneLauncher) PlanNativeCodexResume(route codexNativeEndpointRoute, workspace coremetadata.AgentWorkspace, threadID string) (string, []string, error) {
	f.plans = append(f.plans, fakeNativePanePlan{route: route, workspace: workspace, threadID: threadID})
	return "codex:native", []string{route.TUIExecutable, "resume", "--remote", "unix://" + route.SocketPath, threadID}, nil
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
		if err := f.fakeNativePaneLauncher.BindAgentPaneOnRoute(ctx, runner, binding); err != nil {
			return err
		}
		return nil
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
		if !strings.Contains(joined, "--remote unix:///test/generation-current.sock thread-native-1") || strings.Contains(joined, "exact prompt") {
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

func TestCodexNativeCurrentChangePinsExistingAgentAndAdmitsNewCreateOnlyToNew(t *testing.T) {
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, _ := newTestAgentCreateCommand(t, store, tmux)
	oldRoute := nativeTestRoute("generation-old", coremetadata.CodexGenerationCurrent)
	newRoute := nativeTestRoute("generation-new", coremetadata.CodexGenerationCurrent)
	native := &fakeNativeThreadController{currentRoute: oldRoute}
	panes := &fakeNativePaneLauncher{}
	create.codexNative = native
	create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: panes}

	if _, _, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--", "old generation prompt"); err != nil {
		t.Fatal(err)
	}
	oldAgent := agentNamed(t, store, "win-alpha-main", "codex-1")
	oldRef := oldAgent.Status.SessionRef.Clone()
	native.currentRoute = newRoute
	if _, _, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--", "new generation prompt"); err != nil {
		t.Fatal(err)
	}
	newAgent := agentNamed(t, store, "win-alpha-main", "codex-2")

	oldAgent = agentNamed(t, store, "win-alpha-main", "codex-1")
	if oldAgent.Status.SessionRef == nil || oldAgent.Status.SessionRef.Codex == nil ||
		oldAgent.Status.SessionRef.Codex.Endpoint == nil || !oldAgent.Status.SessionRef.Codex.Endpoint.Same(oldRoute.Endpoint) ||
		!oldAgent.Status.SessionRef.SameConversation(oldRef) {
		t.Fatalf("existing Agent was retargeted by current change: before=%#v after=%#v", oldRef, oldAgent.Status.SessionRef)
	}
	if newAgent.Status.SessionRef == nil || newAgent.Status.SessionRef.Codex == nil ||
		newAgent.Status.SessionRef.Codex.Endpoint == nil || !newAgent.Status.SessionRef.Codex.Endpoint.Same(newRoute.Endpoint) {
		t.Fatalf("new Agent did not use new current route: %#v", newAgent.Status.SessionRef)
	}
	if len(native.creates) != 2 || !native.creates[0].route.Endpoint.Same(oldRoute.Endpoint) ||
		!native.creates[1].route.Endpoint.Same(newRoute.Endpoint) || len(panes.plans) != 2 ||
		!panes.plans[0].route.Endpoint.Same(oldRoute.Endpoint) || !panes.plans[1].route.Endpoint.Same(newRoute.Endpoint) ||
		panes.plans[0].route.TUIExecutable != oldRoute.TUIExecutable || panes.plans[0].route.SocketPath != oldRoute.SocketPath ||
		panes.plans[1].route.TUIExecutable != newRoute.TUIExecutable || panes.plans[1].route.SocketPath != newRoute.SocketPath {
		t.Fatalf("cross-generation create ledger: creates=%+v plans=%+v", native.creates, panes.plans)
	}
	splits := splitWindowCalls(tmux)
	if len(splits) != 2 || !strings.Contains(strings.Join(splits[0], " "), oldRoute.TUIExecutable+" resume --remote unix://"+oldRoute.SocketPath) ||
		!strings.Contains(strings.Join(splits[1], " "), newRoute.TUIExecutable+" resume --remote unix://"+newRoute.SocketPath) {
		t.Fatalf("generation-pinned TUI argv drifted after current change: %v", splits)
	}
	// Changing admission-current is not itself route authority for an existing
	// Agent. Its durable ref still resolves through the old exact route.
	native.resolvedRoute = oldRoute
	if got, err := resolveCodexNativeResumeRoute(context.Background(), native, oldAgent.Status.SessionRef); err != nil || !got.Endpoint.Same(oldRoute.Endpoint) {
		t.Fatalf("old durable ref after current change = %+v, %v", got, err)
	}
	oldPane, ok := store.registry.Pane(oldAgent.Status.PaneRef)
	if !ok || oldPane.Status.Activation.Codex == nil {
		t.Fatalf("old Agent Pane lost native binding: %+v", oldAgent.Status)
	}
	oldPane.Status.Activation.Codex.Authority = &coremetadata.CodexAuthorityRef{
		StateDomainID: oldRoute.Endpoint.StateDomainID, EndpointGenerationID: oldRoute.Endpoint.EndpointGenerationID,
		BrokerRuntimeID: "broker-old", ConnectionEpoch: 1, BindingEpoch: 1,
	}
	control, _, _ := newTestAgentCommand(t, store)
	control.controlBinding = &staticAgentControlBinding{observed: true, live: agentControlLive{
		RuntimeID: oldPane.Status.Activation.RuntimeID, PaneUID: oldPane.Metadata.UID,
		ThreadID: oldAgent.Status.SessionRef.Codex.ThreadID, Authority: codexAuthorityControlPlane, Epoch: "old-epoch",
	}}
	control.controlPaths = func() (config.Paths, error) { return config.Paths{StateDir: "/tmp/projmux-generation-routing"}, nil }
	var controlEndpoints []coremetadata.CodexEndpointRef
	control.controlCall = func(_ context.Context, _ string, endpoint coremetadata.CodexEndpointRef, identity codexLifecycleIdentity, request agentControlRequest) (agentControlResponse, error) {
		controlEndpoints = append(controlEndpoints, endpoint)
		return agentControlResponse{OK: true, ThreadID: identity.ThreadID, TurnID: "old-turn-after-switch"}, nil
	}
	if _, _, err := runRoute(t, control, "turn", "start", "uid:"+oldAgent.Metadata.UID, "--", "old Agent message after current switch"); err != nil {
		t.Fatal(err)
	}
	if len(controlEndpoints) != 1 || !controlEndpoints[0].Same(oldRoute.Endpoint) || controlEndpoints[0].Same(newRoute.Endpoint) {
		t.Fatalf("old Agent message crossed generation routes: %+v", controlEndpoints)
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
			native := &fakeNativeThreadController{createBinding: codexappserver.ThreadBinding{ThreadID: "thread-empty-native"}}
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
			if len(legacy.plans) != 1 || len(legacy.plans[0].payload) != 0 || len(legacy.bound) != 0 || len(legacy.activationPanes) != 0 {
				t.Fatalf("legacy planning/binding=%+v/%+v activation probes=%v", legacy.plans, legacy.bound, legacy.activationPanes)
			}
			calls := splitWindowCalls(tmux)
			if len(calls) != 1 {
				t.Fatalf("native TUI split calls = %v, want exactly one", calls)
			}
			joined := strings.Join(calls[0], " ")
			if !strings.Contains(joined, "resume --remote unix:///test/generation-current.sock thread-empty-native") {
				t.Fatalf("empty create launch = %v, want pinned native thread", calls[0])
			}
			if len(panes.plans) != 1 || len(panes.bound) != 1 || len(panes.lifecycle) != 1 {
				t.Fatalf("empty native Pane state: plans=%+v bindings=%+v lifecycle=%+v", panes.plans, panes.bound, panes.lifecycle)
			}
			agent := agentNamed(t, store, "win-alpha-main", "codex-1")
			pane, ok := store.registry.Pane(agent.Status.PaneRef)
			if !ok || pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.ThreadID != "thread-empty-native" || pane.Status.Activation.Codex.TurnID != "" ||
				agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || agent.Status.SessionRef.Codex.ThreadID != "thread-empty-native" ||
				agent.Status.SessionRef.Codex.Endpoint == nil || !agent.Status.SessionRef.Codex.Endpoint.Same(native.creates[0].route.Endpoint) || agent.Status.SessionRef.Codex.HasStartedTurn ||
				agent.Status.Activation.State != coremetadata.ActivationNotRequested || agent.Status.Activation.Source != "" {
				t.Fatalf("empty native Agent/Pane binding = agent:%#v pane:%#v", agent.Status, pane.Status)
			}
			obligation, projected := codexgeneration.ProjectAgentObligation(agent, false)
			if !projected || obligation.State != codexgeneration.ObligationNoTurn || obligation.EndpointGenerationID != native.creates[0].route.Endpoint.EndpointGenerationID {
				t.Fatalf("empty create obligation=%+v projected=%t", obligation, projected)
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
			native := &fakeNativeThreadController{createBinding: codexappserver.ThreadBinding{ThreadID: "thread-empty-" + string(producer)}}
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
			if len(native.creates) != 1 || native.creates[0].prompt != "" || len(native.resumes) != 0 || len(legacy.plans) != 0 || len(panes.plans) != 1 || len(panes.bound) != 1 {
				t.Fatalf("split producer changed lane: native create=%+v resume=%+v legacy resume=%+v native plans=%+v bindings=%+v",
					native.creates, native.resumes, legacy.plans, panes.plans, panes.bound)
			}
			calls := splitWindowCalls(fx.tmux)
			if len(calls) != 1 {
				t.Fatalf("split calls = %v, want exactly one", calls)
			}
			joined := strings.Join(calls[0], " ")
			if !strings.Contains(joined, "resume --remote unix:///test/generation-current.sock thread-empty-"+string(producer)) {
				t.Fatalf("split producer launch = %v, want one pinned native TUI", calls[0])
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
	native := &fakeNativeThreadController{createBinding: codexappserver.ThreadBinding{ThreadID: "thread-first-input"}}
	create.codexNative = native
	create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: &fakeNativePaneLauncher{}}

	stdout, stderr, err := runRoute(t, create,
		"agent", "--provider", "codex", "--project", "alpha", "--window", "main", "-o", "pane-id")
	if err != nil || stderr != "" {
		t.Fatalf("empty create stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	paneID := strings.TrimSpace(stdout)
	agent := agentNamed(t, store, "win-alpha-main", "codex-1")
	pane, ok := store.registry.Pane(agent.Status.PaneRef)
	if !ok || pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.ThreadID != "thread-first-input" || pane.Status.Activation.Codex.TurnID != "" ||
		pane.Status.Activation.Generation == "" || pane.Status.Activation.RuntimeID != paneID || agent.Status.SessionRef.Codex.HasStartedTurn {
		t.Fatalf("empty native Pane activation = %#v ref=%#v", pane.Status.Activation, agent.Status.SessionRef)
	}
	endpoint := *agent.Status.SessionRef.Codex.Endpoint
	authority := coremetadata.CodexAuthorityRef{
		StateDomainID: endpoint.StateDomainID, EndpointGenerationID: endpoint.EndpointGenerationID,
		BrokerRuntimeID: "broker-first", ConnectionEpoch: 1, BindingEpoch: 1,
	}
	pane.Status.Activation.Codex.Authority = &authority
	control, _, _ := newTestAgentCommand(t, store)
	control.controlBinding = &staticAgentControlBinding{observed: true, live: agentControlLive{
		RuntimeID: paneID, PaneUID: pane.Metadata.UID, ThreadID: "thread-first-input",
		Authority: codexAuthorityControlPlane, Epoch: "epoch-first", Reason: "ready",
	}}
	control.controlPaths = func() (config.Paths, error) { return config.Paths{StateDir: "/tmp/projmux-empty-first"}, nil }
	var calls []agentControlRequest
	control.controlCall = func(_ context.Context, _ string, routedEndpoint coremetadata.CodexEndpointRef, identity codexLifecycleIdentity, request agentControlRequest) (agentControlResponse, error) {
		calls = append(calls, request)
		if !routedEndpoint.Same(endpoint) || identity.ThreadID != "thread-first-input" || request.Text != "first real input" {
			t.Fatalf("first input route endpoint=%+v identity=%+v request=%+v", routedEndpoint, identity, request)
		}
		return agentControlResponse{OK: true, ThreadID: identity.ThreadID, TurnID: "turn-first"}, nil
	}
	if _, _, err := runRoute(t, control, "turn", "start", "uid:"+agent.Metadata.UID, "--", "first real input"); err != nil {
		t.Fatal(err)
	}

	agent = agentNamed(t, store, "win-alpha-main", "codex-1")
	if len(calls) != 1 || len(native.creates) != 1 || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil ||
		agent.Status.SessionRef.Codex.ThreadID != "thread-first-input" || !agent.Status.SessionRef.Codex.Endpoint.Same(endpoint) || !agent.Status.SessionRef.Codex.HasStartedTurn {
		t.Fatalf("first-input sessionRef = %#v (transactions=%d writes=%d)", agent.Status.SessionRef, store.transactions, store.writes)
	}
	pane, _ = store.registry.Pane(agent.Status.PaneRef)
	if pane.Status.Activation.Codex.TurnID != "turn-first" {
		t.Fatalf("first input did not refine same-thread turn: %#v", pane.Status.Activation.Codex)
	}
}

func TestCodexFanOutKeepsCurrentPlainCLILaneWithoutNativeCreate(t *testing.T) {
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, legacy := newTestAgentCreateCommand(t, store, tmux)
	native := &fakeNativeThreadController{createBinding: codexappserver.ThreadBinding{ThreadID: "must-not-create", TurnID: "must-not-turn"}}
	create.codexNative = native
	create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: &fakeNativePaneLauncher{}}

	stdout, stderr, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--interactive-only", "-o", "pane-id")
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
	route := nativeTestRoute("generation-resume", coremetadata.CodexGenerationCurrent)
	ref := nativeTestSessionRef(route, resumeFixtureConversation)
	ref.ObservedAt = resourceFixtureClock
	setFixtureSessionRef(t, store, "agt-beta-codex", ref)
	tmux := newFakeTmux()
	agentCommand, legacy, _, _ := newTestAgentResumeCommand(t, store, tmux)
	native := &fakeNativeThreadController{resolvedRoute: route, resumeBinding: codexappserver.ThreadBinding{ThreadID: resumeFixtureConversation}}
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
	if len(panes.plans) != 2 || panes.plans[0].threadID != resumeFixtureConversation || panes.plans[1].threadID != resumeFixtureConversation || len(panes.bound) != 1 {
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
	if !after.Status.SessionRef.SameConversation(ref) || !after.Status.SessionRef.Codex.Endpoint.Same(route.Endpoint) {
		t.Fatalf("resume rewrote stored thread: %#v", after.Status.SessionRef)
	}
	pane, ok := store.registry.Pane(after.Status.PaneRef)
	if !ok || pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.ThreadID != resumeFixtureConversation || pane.Status.Activation.Codex.TurnID != "" {
		t.Fatalf("resumed Pane native binding = %#v", pane.Status.Activation)
	}
}

func TestDrainingOfflineCodexResumeIsGenerationWideHandoverRequiredWithZeroWrites(t *testing.T) {
	store := newFakeResourceStore(t)
	oldRoute := nativeTestRoute("generation-draining", coremetadata.CodexGenerationDraining)
	ref := nativeTestSessionRef(oldRoute, resumeFixtureConversation)
	ref.ObservedAt = resourceFixtureClock
	setFixtureSessionRef(t, store, "agt-beta-codex", ref)
	tmux := newFakeTmux()
	command, legacy, _, _ := newTestAgentResumeCommand(t, store, tmux)
	native := &fakeNativeThreadController{
		resolvedRoute: nativeTestRoute("generation-new", coremetadata.CodexGenerationCurrent),
		resumeBinding: codexappserver.ThreadBinding{ThreadID: resumeFixtureConversation},
	}
	panes := &fakeNativePaneLauncher{}
	command.rebind.launcher = &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: panes}
	command.rebind.create.codexNative = native
	before, paneCount := store.snapshot(), tmux.paneCount()

	stdout, stderr, err := runRoute(t, command, "resume", "uid:agt-beta-codex")
	if err == nil || stdout != "" || stderr != "" || !strings.Contains(err.Error(), codexNativeReasonHandoverRequired) {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if len(native.resumes) != 0 || len(native.creates) != 0 || len(panes.plans) != 0 || len(panes.bound) != 0 ||
		len(legacy.plans) != 0 || len(legacy.bound) != 0 || len(splitWindowCalls(tmux)) != 0 ||
		store.writes != 0 || store.snapshot() != before || tmux.paneCount() != paneCount {
		t.Fatalf("Draining resume crossed the generation-wide barrier: native=%+v/%+v plans=%+v bound=%+v legacy=%+v/%+v splits=%v writes=%d",
			native.creates, native.resumes, panes.plans, panes.bound, legacy.plans, legacy.bound, splitWindowCalls(tmux), store.writes)
	}
}

func TestOfflineCodexResumeRechecksDrainingInsideTransactionBeforeProviderWrite(t *testing.T) {
	store := newFakeResourceStore(t)
	oldRoute := nativeTestRoute("generation-race-old", coremetadata.CodexGenerationCurrent)
	ref := nativeTestSessionRef(oldRoute, resumeFixtureConversation)
	ref.ObservedAt = resourceFixtureClock
	setFixtureSessionRef(t, store, "agt-beta-codex", ref)
	tmux := newFakeTmux()
	command, legacy, _, _ := newTestAgentResumeCommand(t, store, tmux)
	started := make(chan struct{}, 1)
	resumePreflight := make(chan struct{})
	native := &fakeNativeThreadController{
		resolvedRoute: oldRoute, resumeBinding: codexappserver.ThreadBinding{ThreadID: resumeFixtureConversation},
		resolveStarted: started, resolveContinue: resumePreflight,
	}
	panes := &fakeNativePaneLauncher{}
	command.rebind.launcher = &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: panes}
	command.rebind.create.codexNative = native
	type outcome struct {
		stdout, stderr string
		err            error
	}
	result := make(chan outcome, 1)
	go func() {
		stdout, stderr, err := runRoute(t, command, "resume", "uid:agt-beta-codex")
		result <- outcome{stdout: stdout, stderr: stderr, err: err}
	}()
	<-started
	agent, _ := store.registry.Agent("agt-beta-codex")
	agent.Status.SessionRef.Codex.Lifecycle = &coremetadata.CodexGenerationLifecycleRef{
		State: coremetadata.CodexGenerationDraining,
		Operation: &coremetadata.CodexGenerationOperationRef{
			ID: "drain-race-operation", Endpoint: oldRoute.Endpoint,
		},
	}
	afterTransition := store.snapshot()
	close(resumePreflight)
	got := <-result
	if got.err == nil || got.stdout != "" || got.stderr != "" || !strings.Contains(got.err.Error(), codexNativeReasonHandoverRequired) {
		t.Fatalf("stdout=%q stderr=%q err=%v", got.stdout, got.stderr, got.err)
	}
	if len(native.resumes) != 0 || len(native.creates) != 0 || len(legacy.bound) != 0 || len(panes.bound) != 0 ||
		len(splitWindowCalls(tmux)) != 0 || store.writes != 0 || store.snapshot() != afterTransition {
		t.Fatalf("transaction race crossed Draining barrier: native=%+v/%+v bound=%+v/%+v splits=%v writes=%d",
			native.creates, native.resumes, legacy.bound, panes.bound, splitWindowCalls(tmux), store.writes)
	}
}

func TestNativeCatalogPickerResumeUsesExactThreadAndCreatesZeroThreads(t *testing.T) {
	fx := canonicalFixture(t, false)
	id := "019f0000-0000-7000-8000-000000000041"
	route := nativeTestRoute("generation-picker", coremetadata.CodexGenerationCurrent)
	native := &fakeNativeThreadController{resolvedRoute: route, resumeBinding: codexappserver.ThreadBinding{ThreadID: id}}
	panes := &fakeNativePaneLauncher{}
	fx.create.codexNative = native
	fx.create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: panes}

	err := fx.create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right",
		conversationID: id, resumeSource: aisessions.SourceCodexAppServer, anchorPaneID: fx.originID,
		resumeEndpoint: route.Endpoint, resumeGenerationState: coremetadata.CodexGenerationCurrent,
	}, ioDiscard{}, ioDiscard{})
	if err != nil {
		t.Fatal(err)
	}
	if len(native.creates) != 0 || len(native.resumes) != 1 || native.resumes[0].threadID != id ||
		len(panes.plans) != 1 || panes.plans[0].threadID != id || len(panes.bound) != 1 || panes.bound[0].threadID != id {
		t.Fatalf("creates=%+v resumes=%+v plans=%+v bound=%+v", native.creates, native.resumes, panes.plans, panes.bound)
	}
}

func TestDrainingNativePickerRowIsHandoverRequiredBeforeOldOrNewWrite(t *testing.T) {
	fx := canonicalFixture(t, false)
	id := "019f0000-0000-7000-8000-000000000044"
	oldRoute := nativeTestRoute("generation-picker-draining", coremetadata.CodexGenerationDraining)
	native := &fakeNativeThreadController{
		currentRoute:  nativeTestRoute("generation-picker-new", coremetadata.CodexGenerationCurrent),
		resolvedRoute: oldRoute,
		resumeBinding: codexappserver.ThreadBinding{ThreadID: id},
	}
	legacy := newFakeResumeLauncher()
	panes := &fakeNativePaneLauncher{}
	fx.create.codexNative = native
	fx.create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: panes}
	before, paneCount := fx.store.snapshot(), fx.tmux.paneCount()

	err := fx.create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right",
		conversationID: id, resumeSource: aisessions.SourceCodexAppServer, anchorPaneID: fx.originID,
		resumeEndpoint: oldRoute.Endpoint, resumeGenerationState: coremetadata.CodexGenerationDraining,
	}, ioDiscard{}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), codexNativeReasonHandoverRequired) {
		t.Fatalf("Draining picker row err=%v, want handover-required", err)
	}
	if len(native.creates) != 0 || len(native.resumes) != 0 || len(legacy.plans) != 0 || len(legacy.bound) != 0 ||
		len(panes.plans) != 0 || len(panes.bound) != 0 || len(splitWindowCalls(fx.tmux)) != 0 ||
		fx.store.transactions != 0 || fx.store.writes != 0 || fx.store.snapshot() != before || fx.tmux.paneCount() != paneCount {
		t.Fatalf("Draining picker row wrote old/new state: native=%+v/%+v legacy=%+v/%+v plans=%+v bound=%+v tx=%d writes=%d",
			native.creates, native.resumes, legacy.plans, legacy.bound, panes.plans, panes.bound, fx.store.transactions, fx.store.writes)
	}
}

func TestRolloutCatalogPickerResumeStaysOnCurrentCLILane(t *testing.T) {
	fx := canonicalFixture(t, false)
	id := "019f0000-0000-7000-8000-000000000042"
	native := &fakeNativeThreadController{resumeBinding: codexappserver.ThreadBinding{ThreadID: id}}
	legacy := newFakeResumeLauncher()
	fx.create.codexNative = native
	launcher := &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: &fakeNativePaneLauncher{}}
	fx.create.resumes = launcher

	err := fx.create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right",
		conversationID: id, resumeSource: aisessions.SourceCodexRollout, anchorPaneID: fx.originID,
	}, ioDiscard{}, ioDiscard{})
	if err != nil {
		t.Fatal(err)
	}
	if len(native.creates) != 0 || len(native.resumes) != 0 || len(legacy.plans) != 1 || legacy.plans[0].conversationID != id ||
		len(launcher.sources) != 1 || launcher.sources[0] != aisessions.SourceCodexRollout {
		t.Fatalf("native creates=%+v resumes=%+v legacy=%+v sources=%v", native.creates, native.resumes, legacy.plans, launcher.sources)
	}
	agents := fx.store.registry.AgentsOf(fx.windowUID)
	created := agents[len(agents)-1]
	if created.Status.SessionRef == nil || created.Status.SessionRef.Codex == nil || created.Status.SessionRef.Codex.ThreadID != id ||
		created.Status.SessionRef.Codex.Endpoint != nil {
		t.Fatalf("rollout catalog row gained native endpoint authority: %#v", created.Status.SessionRef)
	}
}

// TestUnavailableNativePickerResumeRefusesInsteadOfRebindingOntoTheRolloutLane
// pins the split-UI picker half of the stored-resume contract.
//
// The picker row names a thread the app-server owns. Silently reopening it
// through the rollout CLI lane produced an Agent that reported a resume while
// answering no native turn control, so an unproven native resume now refuses,
// launches nothing, and offers no second lane.
func TestUnavailableNativePickerResumeRefusesInsteadOfRebindingOntoTheRolloutLane(t *testing.T) {
	fx := canonicalFixture(t, false)
	id := "019f0000-0000-7000-8000-000000000043"
	native := &fakeNativeThreadController{
		resolvedRoute: nativeTestRoute("generation-picker", coremetadata.CodexGenerationCurrent),
		resumeErr:     &codexappserver.ThreadActionError{Reason: "daemon-not-running", SafeFallback: true},
		fallback:      true,
	}
	legacy := newFakeResumeLauncher()
	panes := &fakeNativePaneLauncher{}
	launcher := &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: panes}
	fx.create.codexNative = native
	fx.create.resumes = launcher

	err := fx.create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right",
		conversationID: id, resumeSource: aisessions.SourceCodexAppServer, anchorPaneID: fx.originID,
		resumeEndpoint: native.resolvedRoute.Endpoint, resumeGenerationState: coremetadata.CodexGenerationCurrent,
	}, ioDiscard{}, ioDiscard{})
	if err == nil {
		t.Fatal("unavailable native picker resume silently rebound onto the rollout lane")
	}
	for _, required := range []string{"cannot be resumed natively", "daemon-not-running"} {
		if !strings.Contains(err.Error(), required) {
			t.Fatalf("picker resume refusal = %q, missing %q", err, required)
		}
	}
	if strings.Contains(err.Error(), interactiveOnlyFlag) {
		t.Fatalf("stored resume offered a launch-mode escape hatch it does not have: %q", err)
	}
	if len(native.creates) != 0 || len(native.resumes) != 1 || native.resumes[0].threadID != id {
		t.Fatalf("native creates=%+v resumes=%+v", native.creates, native.resumes)
	}
	// The provider argv is constructed before the native attempt, but nothing
	// may be bound, sourced, or launched from it once the resume refuses.
	if len(legacy.bound) != 0 || len(panes.plans) != 0 || len(panes.bound) != 0 || len(launcher.sources) != 0 {
		t.Fatalf("refused picker resume still bound a Pane: legacy=%+v nativePlans=%+v nativeBindings=%+v sources=%v",
			legacy.bound, panes.plans, panes.bound, launcher.sources)
	}
	if calls := splitWindowCalls(fx.tmux); len(calls) != 0 {
		t.Fatalf("refused picker resume split a Pane: %v", calls)
	}
}

// TestUnavailableNativeCreateRefusesInsteadOfSilentlyCreatingAPlainAgent is the
// pre-mutation half of the prompted-create refusal matrix.
//
// A prompted Codex create whose native authority cannot be proven used to
// degrade to the hook/plain lane, producing a managed Agent with no thread
// binding and no native turn control. It now refuses at the provider-mutation
// boundary -- before the split, before the hook probe, and before the Registry
// commit -- and names the one explicit way to ask for that plain Agent.
func TestUnavailableNativeCreateRefusesInsteadOfSilentlyCreatingAPlainAgent(t *testing.T) {
	for _, test := range []struct {
		name string
		// safeFallback mirrors the adapter's own classification: an
		// unreachable endpoint reports one, while the fail-closed additional
		// writable roots row deliberately reports none. Both are raised before
		// any provider conversation exists, so both must refuse the same way.
		reason       string
		safeFallback bool
	}{
		{name: "endpoint unavailable", reason: "daemon-not-running", safeFallback: true},
		{name: "additional writable roots unsupported", reason: codexappserver.ReasonAdditionalRootsUnsupported},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, legacy := newTestAgentCreateCommand(t, store, tmux)
			native := &fakeNativeThreadController{
				createErr: &codexappserver.ThreadActionError{Reason: test.reason, SafeFallback: test.safeFallback},
				fallback:  test.safeFallback,
			}
			create.codexNative = native
			create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: &fakeNativePaneLauncher{}}
			before, panes := store.snapshot(), tmux.paneCount()

			stdout, stderr, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--", "fallback prompt")
			if err == nil || stdout != "" || stderr != "" {
				t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
			}
			for _, required := range []string{test.reason, interactiveOnlyFlag, "no native thread binding"} {
				if !strings.Contains(err.Error(), required) {
					t.Fatalf("refusal = %q, missing %q", err, required)
				}
			}
			if len(native.creates) != 1 || native.creates[0].prompt != "fallback prompt" {
				t.Fatalf("native creates = %+v", native.creates)
			}
			if len(legacy.plans) != 1 || len(legacy.bound) != 0 || len(legacy.activationPanes) != 0 {
				t.Fatalf("refused create still launched or acknowledged: plans=%+v bound=%+v activation=%v",
					legacy.plans, legacy.bound, legacy.activationPanes)
			}
			if store.snapshot() != before || store.writes != 0 || tmux.paneCount() != panes ||
				tmux.argvContains("split-window") || tmux.argvContains("new-window") {
				t.Fatalf("refused create mutated state: writes=%d panes=%d registry=%s",
					store.writes, tmux.paneCount()-panes, store.snapshot())
			}
		})
	}
}

// TestUnavailableNativeResumeRefusesWithoutAProviderResumeLane pins the
// generation-pinned post-create boundary.
//
// The rule governs routes that *create* a managed Agent. `agent resume`
// creates none -- it reattaches a Pane to an Agent that already exists -- and
// the Agent it reattaches may never have carried an app-server binding at all,
// because a native binding lives on the activation-scoped
// `Pane.Status.Activation.Codex` and dies with the Pane it described. Making
// this route require native authority would make every Codex resume fail
// wherever no app-server is reachable. That refusal is intentional: no exact
// endpoint means there is no honest native authority and no silent plain lane.
func TestUnavailableNativeResumeRefusesWithoutAProviderResumeLane(t *testing.T) {
	store := newFakeResourceStore(t)
	route := nativeTestRoute("generation-resume", coremetadata.CodexGenerationCurrent)
	ref := nativeTestSessionRef(route, resumeFixtureConversation)
	ref.ObservedAt = resourceFixtureClock
	setFixtureSessionRef(t, store, "agt-beta-codex", ref)
	tmux := newFakeTmux()
	agentCommand, legacy, _, _ := newTestAgentResumeCommand(t, store, tmux)
	native := &fakeNativeThreadController{
		resolvedRoute: route,
		resumeErr:     &codexappserver.ThreadActionError{Reason: "daemon-not-running", SafeFallback: true},
		fallback:      true,
	}
	panes := &fakeNativePaneLauncher{}
	agentCommand.rebind.launcher = &fakeNativeResumeLauncher{fakeResumeLauncher: legacy, fakeNativePaneLauncher: panes}
	agentCommand.rebind.create.codexNative = native

	before := store.snapshot()
	stdout, stderr, err := runRoute(t, agentCommand, "resume", "uid:agt-beta-codex")
	if err == nil || stdout != "" || stderr != "" || !strings.Contains(err.Error(), "daemon-not-running") {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	// Exactly one native resume attempt, no native create, no native Pane and no
	// provider resume lane after the failed exact endpoint write.
	if len(native.creates) != 0 || len(native.resumes) != 1 || len(panes.plans) != 1 || len(panes.bound) != 0 {
		t.Fatalf("native creates=%+v resumes=%+v nativePlans=%+v nativeBindings=%+v",
			native.creates, native.resumes, panes.plans, panes.bound)
	}
	if len(legacy.plans) != 0 || len(legacy.bound) != 0 || len(splitWindowCalls(tmux)) != 0 || store.snapshot() != before || store.writes != 0 {
		t.Fatalf("refused resume mutated: plans:%+v bound:%+v splits=%v writes=%d", legacy.plans, legacy.bound, splitWindowCalls(tmux), store.writes)
	}
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
		route := nativeTestRoute("generation-resume-fail", coremetadata.CodexGenerationCurrent)
		ref := nativeTestSessionRef(route, resumeFixtureConversation)
		ref.ObservedAt = resourceFixtureClock
		setFixtureSessionRef(t, store, "agt-beta-codex", ref)
		tmux := newFakeTmux()
		tmux.fail = []string{"set-option", aiPaneTopicOption}
		agentCommand, legacy, _, _ := newTestAgentResumeCommand(t, store, tmux)
		native := &fakeNativeThreadController{resolvedRoute: route, resumeBinding: codexappserver.ThreadBinding{ThreadID: resumeFixtureConversation}}
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

func TestPostThreadPreCommitFailureKeepsRegistryBytesAndRefusesASecondLane(t *testing.T) {
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	tmux.fail = []string{"set-option", aiPaneTopicOption}
	create, legacy := newTestAgentCreateCommand(t, store, tmux)
	native := &fakeNativeThreadController{createBinding: codexappserver.ThreadBinding{ThreadID: "thread-created-before-commit", TurnID: "turn-created-before-commit"}}
	panes := &fakeNativePaneLauncher{}
	create.codexNative = native
	create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: panes}
	before, paneCount := store.snapshot(), tmux.paneCount()

	stdout, stderr, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--", "exactly once")
	if err == nil || stdout != "" || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if len(native.creates) != 1 || len(panes.plans) != 1 || panes.plans[0].threadID != "thread-created-before-commit" ||
		len(legacy.bound) != 0 || len(legacy.activationPanes) != 0 || len(panes.lifecycle) != 0 ||
		store.writes != 0 || store.snapshot() != before || tmux.paneCount() != paneCount {
		t.Fatalf("post-thread rollback drifted: native=%+v plans=%+v legacy=%+v activation=%v lifecycle=%+v writes=%d panes=%d",
			native.creates, panes.plans, legacy.bound, legacy.activationPanes, panes.lifecycle, store.writes, tmux.paneCount()-paneCount)
	}
	if calls := splitWindowCalls(tmux); len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "thread-created-before-commit") {
		t.Fatalf("post-thread failure launched a second/plain lane: %v", calls)
	}
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
	// Provider identity is already indeterminate, so this refusal must offer no
	// second lane at all -- the explicit opt-out included. Starting another
	// Codex process now could submit the same prompt twice.
	if strings.Contains(err.Error(), interactiveOnlyFlag) {
		t.Fatalf("post-mutation refusal offered a second lane: %q", err)
	}
	if calls := splitWindowCalls(tmux); len(calls) != 0 {
		t.Fatalf("indeterminate native create synthesized a lane: %v", calls)
	}
	if store.writes != 0 {
		t.Fatalf("indeterminate native create writes=%d, want zero", store.writes)
	}
}

// TestCodexNativeLaunchOutcomeTableIsClosed keeps the documented outcome table
// describing reality: exactly which rows may still reach the plain CLI lane,
// exactly which must launch nothing, and that only a create ever names the
// `--interactive-only` escape hatch.
func TestCodexNativeLaunchOutcomeTableIsClosed(t *testing.T) {
	if len(codexNativeLaunchOutcomeTable) != 10 {
		t.Fatalf("outcome rows=%d, want 10: %+v", len(codexNativeLaunchOutcomeTable), codexNativeLaunchOutcomeTable)
	}
	var plainLane, refused []string
	for _, row := range codexNativeLaunchOutcomeTable {
		switch {
		case strings.Contains(row.Launch, "current CLI"):
			plainLane = append(plainLane, row.NativeResult)
		case row.Launch == "none":
			refused = append(refused, row.NativeResult)
		}
	}
	if !slices.Equal(plainLane, []string{"explicit " + interactiveOnlyFlag, "rollout picker source"}) {
		t.Fatalf("plain CLI rows drifted: %v", plainLane)
	}
	if len(refused) != 5 {
		t.Fatalf("refusal rows = %v, want the five unproven-authority rows", refused)
	}
	for _, row := range codexNativeLaunchOutcomeTable {
		if row.Action == "resume" && strings.Contains(row.Binding, interactiveOnlyFlag) {
			t.Fatalf("a resume row offers a launch-mode escape hatch: %+v", row)
		}
		if strings.Contains(row.NativeResult, "indeterminate") && strings.Contains(row.Binding, interactiveOnlyFlag) {
			t.Fatalf("the post-mutation row offers a second lane: %+v", row)
		}
	}
}
