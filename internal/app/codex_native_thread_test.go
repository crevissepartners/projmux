package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
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
	currentCalls    int
	resolveCalls    int
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

type orderedNativeThreadClient struct {
	events   *[]string
	closeErr error
}

func (c *orderedNativeThreadClient) StartThread(context.Context, string, []string) (codexappserver.ThreadBinding, error) {
	*c.events = append(*c.events, "thread/start")
	return codexappserver.ThreadBinding{ThreadID: "thread-production-order"}, nil
}

func (c *orderedNativeThreadClient) StartTurn(context.Context, string, string, string) (string, error) {
	*c.events = append(*c.events, "turn/start")
	return "turn-production-order", nil
}

func (c *orderedNativeThreadClient) BootstrapThread(context.Context, string, string, []string) (codexappserver.ThreadSnapshot, error) {
	*c.events = append(*c.events, "thread/resume", "thread/read")
	return codexappserver.ThreadSnapshot{ThreadID: "thread-production-order", RuntimeStatus: "idle"}, nil
}

func (c *orderedNativeThreadClient) Close() error {
	*c.events = append(*c.events, "creator/close")
	return c.closeErr
}

func (f *fakeNativeThreadController) Current(context.Context) (codexNativeEndpointRoute, error) {
	f.currentCalls++
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
	f.resolveCalls++
	if f.resolveStarted != nil {
		f.resolveStarted <- struct{}{}
	}
	if f.resolveContinue != nil {
		<-f.resolveContinue
	}
	resolved := f.resolvedRoute
	if !resolved.valid() && f.resolveErr == nil {
		if f.currentRoute.valid() && f.currentRoute.Endpoint.Same(endpoint) {
			resolved = f.currentRoute
		} else {
			resolved = nativeTestRoute(endpoint.EndpointGenerationID, coremetadata.CodexGenerationCurrent)
			resolved.Endpoint = endpoint
		}
	}
	return resolved, f.resolveErr
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

// TestPayloadFreeNativeCreateClosesCreatorBeforeIndependentDurableResumeBarrier
// is the production-order regression for Phase 7. It is deliberately distinct
// from the installed Pane-first capability smoke: Create must close the exact
// thread/start client, cross an independent resume/read barrier, and only then
// return the binding that lets the caller plan a TUI Pane.
func TestPayloadFreeNativeCreateClosesCreatorBeforeIndependentDurableResumeBarrier(t *testing.T) {
	events := []string{}
	controller := defaultCodexNativeThreadController{
		open: func(context.Context, codexNativeEndpointRoute, bool) (codexNativeThreadClient, error) {
			events = append(events, "creator/open")
			return &orderedNativeThreadClient{events: &events, closeErr: errors.New("owned stdio proxy reaped")}, nil
		},
		awaitDurable: func(_ context.Context, _ codexNativeEndpointRoute, _ coremetadata.AgentWorkspace, threadID string) error {
			if threadID != "thread-production-order" {
				t.Fatalf("readiness thread = %q", threadID)
			}
			events = append(events, "independent/resume-read-ready")
			return nil
		},
	}
	binding, err := controller.Create(context.Background(), nativeTestRoute("generation-production-order", coremetadata.CodexGenerationCurrent),
		coremetadata.AgentWorkspace{CWD: "/work/project"}, "", "generation-request")
	if err != nil || binding.ThreadID != "thread-production-order" || binding.TurnID != "" {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	want := []string{"creator/open", "thread/start", "creator/close", "independent/resume-read-ready"}
	if !slices.Equal(events, want) {
		t.Fatalf("production ordering = %v, want %v", events, want)
	}
}

func TestPayloadFreeDurableResumeBarrierKeepsDefaultAndPrivateRoutesExact(t *testing.T) {
	for _, test := range []struct {
		name  string
		route codexNativeEndpointRoute
	}{
		{name: "default daemon proxy", route: func() codexNativeEndpointRoute {
			route := nativeTestRoute("generation-qualified-default", coremetadata.CodexGenerationCurrent)
			route.Default, route.SocketPath = true, ""
			return route
		}()},
		{name: "private generation", route: nativeTestRoute("generation-qualified-private", coremetadata.CodexGenerationCurrent)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var opened []codexNativeEndpointRoute
			var experimental []bool
			events := []string{}
			controller := defaultCodexNativeThreadController{
				open: func(_ context.Context, route codexNativeEndpointRoute, negotiate bool) (codexNativeThreadClient, error) {
					opened = append(opened, route)
					experimental = append(experimental, negotiate)
					return &orderedNativeThreadClient{events: &events}, nil
				},
				guard: func(_ context.Context, route codexNativeEndpointRoute) error {
					if route.Default != test.route.Default || route.SocketPath != test.route.SocketPath || !route.Endpoint.Same(test.route.Endpoint) {
						t.Fatalf("readiness guard crossed route: got=%+v want=%+v", route, test.route)
					}
					return nil
				},
			}
			binding, err := controller.Create(context.Background(), test.route,
				coremetadata.AgentWorkspace{CWD: "/work/project"}, "", "generation-request")
			if err != nil || binding.ThreadID != "thread-production-order" {
				t.Fatalf("binding=%+v err=%v", binding, err)
			}
			if len(opened) != 2 || !opened[0].Endpoint.Same(test.route.Endpoint) || !opened[1].Endpoint.Same(test.route.Endpoint) ||
				opened[0].Default != test.route.Default || opened[1].Default != test.route.Default ||
				opened[0].SocketPath != test.route.SocketPath || opened[1].SocketPath != test.route.SocketPath ||
				!slices.Equal(experimental, []bool{false, true}) {
				t.Fatalf("route opens=%+v experimental=%v", opened, experimental)
			}
		})
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
	agent := agentNamed(t, store, "win-alpha-main", "agent-test-1")
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
	oldAgent := agentNamed(t, store, "win-alpha-main", "agent-test-1")
	oldRef := oldAgent.Status.SessionRef.Clone()
	native.currentRoute = newRoute
	if _, _, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--", "new generation prompt"); err != nil {
		t.Fatal(err)
	}
	newAgent := agentNamed(t, store, "win-alpha-main", "agent-test-3")

	oldAgent = agentNamed(t, store, "win-alpha-main", "agent-test-1")
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
	// Phase 4 publishes Draining without moving the live old-generation Agent.
	// Turn/control remains pinned to that endpoint while resume/new-create are
	// closed. Snapshot the Pane/TUI binding so the control assertion also proves
	// it did not smuggle in endpoint-ref CAS, Pane relaunch, or TUI replacement.
	draining := coremetadata.CodexGenerationLifecycleRef{
		State: coremetadata.CodexGenerationDraining,
		Operation: &coremetadata.CodexGenerationOperationRef{
			ID: "upgrade-one", Endpoint: oldRoute.Endpoint,
		},
	}
	if _, changed, err := store.mutator().SetCodexGenerationLifecycle(&store.registry, oldAgent.Metadata.UID, oldRoute.Endpoint, draining); err != nil || !changed {
		t.Fatalf("publish old Draining lifecycle: changed=%t err=%v", changed, err)
	}
	oldAgent = agentNamed(t, store, "win-alpha-main", "agent-test-1")
	beforeControlRef := oldAgent.Status.SessionRef.Clone()
	oldPane, ok := store.registry.Pane(oldAgent.Status.PaneRef)
	if !ok || oldPane.Status.Activation.Codex == nil {
		t.Fatalf("old Agent Pane lost native binding: %+v", oldAgent.Status)
	}
	beforePaneRef := oldAgent.Status.PaneRef
	beforePaneActivation := oldPane.Status.Activation
	beforePlans, beforeSplits := len(panes.plans), len(splitWindowCalls(tmux))
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
	oldAgent = agentNamed(t, store, "win-alpha-main", "agent-test-1")
	oldPane, ok = store.registry.Pane(oldAgent.Status.PaneRef)
	if !ok || oldAgent.Status.SessionRef == nil || oldAgent.Status.SessionRef.Codex == nil ||
		!oldAgent.Status.SessionRef.Codex.Endpoint.Same(oldRoute.Endpoint) || oldAgent.Status.SessionRef.Codex.Lifecycle == nil ||
		oldAgent.Status.SessionRef.Codex.Lifecycle.State != coremetadata.CodexGenerationDraining ||
		oldAgent.Status.SessionRef.Codex.Lifecycle.Operation == nil || oldAgent.Status.SessionRef.Codex.Lifecycle.Operation.ID != "upgrade-one" ||
		oldAgent.Status.PaneRef != beforePaneRef || oldPane.Status.Activation.Generation != beforePaneActivation.Generation ||
		oldPane.Status.Activation.RuntimeID != beforePaneActivation.RuntimeID || len(panes.plans) != beforePlans ||
		len(splitWindowCalls(tmux)) != beforeSplits || panes.plans[0].route.TUIExecutable != oldRoute.TUIExecutable {
		t.Fatalf("Draining old continuity mutated ref/Pane/TUI: before=%#v after=%#v pane=%+v plans=%+v", beforeControlRef, oldAgent.Status.SessionRef, oldPane, panes.plans)
	}
}

func TestDrainingOldCodexAgentKeepsExactTurnControlTUIAndPaneBinding(t *testing.T) {
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, _ := newTestAgentCreateCommand(t, store, tmux)
	oldRoute := nativeTestRoute("generation-old", coremetadata.CodexGenerationCurrent)
	newRoute := nativeTestRoute("generation-new", coremetadata.CodexGenerationCurrent)
	native := &fakeNativeThreadController{currentRoute: oldRoute}
	panes := &fakeNativePaneLauncher{}
	create.codexNative = native
	create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: panes}
	if _, _, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--", "old live turn"); err != nil {
		t.Fatal(err)
	}
	agent := agentNamed(t, store, "win-alpha-main", "agent-test-1")
	pane, ok := store.registry.Pane(agent.Status.PaneRef)
	if !ok || pane.Status.Activation.Codex == nil {
		t.Fatalf("old native Pane = %+v", pane)
	}
	pane.Status.Activation.Codex.Authority = &coremetadata.CodexAuthorityRef{
		StateDomainID: oldRoute.Endpoint.StateDomainID, EndpointGenerationID: oldRoute.Endpoint.EndpointGenerationID,
		BrokerRuntimeID: "broker-old", ConnectionEpoch: 1, BindingEpoch: 1,
	}
	draining := coremetadata.CodexGenerationLifecycleRef{
		State: coremetadata.CodexGenerationDraining,
		Operation: &coremetadata.CodexGenerationOperationRef{
			ID: "upgrade-one", Endpoint: oldRoute.Endpoint,
		},
	}
	if _, changed, err := store.mutator().SetCodexGenerationLifecycle(&store.registry, agent.Metadata.UID, oldRoute.Endpoint, draining); err != nil || !changed {
		t.Fatalf("publish Draining: changed=%t err=%v", changed, err)
	}
	native.currentRoute = newRoute
	agent = agentNamed(t, store, "win-alpha-main", "agent-test-1")
	beforeRef, beforePaneRef := agent.Status.SessionRef.Clone(), agent.Status.PaneRef
	beforeGeneration, beforeRuntime := pane.Status.Activation.Generation, pane.Status.Activation.RuntimeID
	beforePlans, beforeSplits := len(panes.plans), len(splitWindowCalls(tmux))

	control, _, _ := newTestAgentCommand(t, store)
	control.controlBinding = &staticAgentControlBinding{observed: true, live: agentControlLive{
		RuntimeID: beforeRuntime, PaneUID: pane.Metadata.UID, ThreadID: agent.Status.SessionRef.Codex.ThreadID,
		Authority: codexAuthorityControlPlane, Epoch: "old-epoch",
	}}
	control.controlPaths = func() (config.Paths, error) { return config.Paths{StateDir: "/tmp/projmux-draining-old-control"}, nil }
	var endpoints []coremetadata.CodexEndpointRef
	control.controlCall = func(_ context.Context, _ string, endpoint coremetadata.CodexEndpointRef, identity codexLifecycleIdentity, _ agentControlRequest) (agentControlResponse, error) {
		endpoints = append(endpoints, endpoint)
		return agentControlResponse{OK: true, ThreadID: identity.ThreadID, TurnID: "old-turn-while-draining"}, nil
	}
	if _, _, err := runRoute(t, control, "turn", "start", "uid:"+agent.Metadata.UID, "--", "continue exact old thread"); err != nil {
		t.Fatal(err)
	}
	agent = agentNamed(t, store, "win-alpha-main", "agent-test-1")
	pane, ok = store.registry.Pane(agent.Status.PaneRef)
	if !ok || len(endpoints) != 1 || !endpoints[0].Same(oldRoute.Endpoint) || endpoints[0].Same(newRoute.Endpoint) ||
		agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || !agent.Status.SessionRef.Codex.Endpoint.Same(oldRoute.Endpoint) ||
		agent.Status.SessionRef.Codex.Lifecycle == nil || agent.Status.SessionRef.Codex.Lifecycle.State != coremetadata.CodexGenerationDraining ||
		agent.Status.PaneRef != beforePaneRef || pane.Status.Activation.Generation != beforeGeneration || pane.Status.Activation.RuntimeID != beforeRuntime ||
		len(panes.plans) != beforePlans || len(splitWindowCalls(tmux)) != beforeSplits || panes.plans[0].route.TUIExecutable != oldRoute.TUIExecutable {
		t.Fatalf("Draining old control continuity drifted: before=%#v after=%#v pane=%+v endpoints=%+v plans=%+v", beforeRef, agent.Status.SessionRef, pane, endpoints, panes.plans)
	}
}

func TestNativeCodexCreateRechecksAdmissionCurrentBeforeEveryMutation(t *testing.T) {
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, _ := newTestAgentCreateCommand(t, store, tmux)
	oldRoute := nativeTestRoute("generation-old", coremetadata.CodexGenerationCurrent)
	native := &fakeNativeThreadController{
		currentRoute: oldRoute, resolveStarted: make(chan struct{}), resolveContinue: make(chan struct{}),
	}
	create.codexNative = native
	create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: &fakeNativePaneLauncher{}}
	before := store.snapshot()
	done := make(chan error, 1)
	go func() {
		_, _, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--", "must not be sent")
		done <- err
	}()
	<-native.resolveStarted
	draining := oldRoute
	draining.State = coremetadata.CodexGenerationDraining
	native.resolvedRoute = draining
	close(native.resolveContinue)
	err := <-done
	if err == nil || !strings.Contains(err.Error(), codexNativeReasonGenerationUnavailable) {
		t.Fatalf("create admission error = %v", err)
	}
	if store.snapshot() != before || store.writes != 0 || len(native.creates) != 0 || tmuxMutationCallCount(tmux) != 0 {
		t.Fatalf("Draining create mutated state: writes=%d provider=%+v tmux=%+v", store.writes, native.creates, tmux.calls)
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
			if native.currentCalls != 0 || native.resolveCalls != 0 || len(native.creates) != 0 || len(native.resumes) != 0 {
				t.Fatalf("provider route was consulted: current=%d resolve=%d creates=%+v resumes=%+v",
					native.currentCalls, native.resolveCalls, native.creates, native.resumes)
			}
			if len(legacy.plans) != 1 || len(legacy.plans[0].payload) != 0 || len(legacy.bound) != 1 || len(legacy.activationPanes) != 0 {
				t.Fatalf("legacy planning/binding=%+v/%+v activation probes=%v", legacy.plans, legacy.bound, legacy.activationPanes)
			}
			calls := splitWindowCalls(tmux)
			if len(calls) != 1 {
				t.Fatalf("plain TUI split calls = %v, want exactly one", calls)
			}
			joined := strings.Join(calls[0], " ")
			if !strings.Contains(joined, "exec codex") || strings.Contains(joined, "--remote") || strings.Contains(joined, "thread-empty-native") {
				t.Fatalf("empty create launch = %v, want one plain Codex process", calls[0])
			}
			if len(panes.plans) != 0 || len(panes.bound) != 0 || len(panes.lifecycle) != 0 {
				t.Fatalf("empty fallback gained native Pane state: plans=%+v bindings=%+v lifecycle=%+v", panes.plans, panes.bound, panes.lifecycle)
			}
			agent := agentNamed(t, store, "win-alpha-main", "agent-test-1")
			pane, ok := store.registry.Pane(agent.Status.PaneRef)
			if !ok || pane.Status.Activation.Codex != nil || agent.Status.SessionRef != nil ||
				agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.Activation.State != coremetadata.ActivationNotRequested ||
				agent.Status.Activation.Source != "" || pane.Status.Activation.RuntimeID == "" {
				t.Fatalf("empty plain Agent/Pane binding = agent:%#v pane:%#v", agent.Status, pane.Status)
			}
			if obligation, projected := codexgeneration.ProjectAgentObligation(agent, false); projected {
				t.Fatalf("plain fallback projected a native obligation=%+v", obligation)
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
			beforeAgents, beforePanes := len(fx.store.registry.Agents), len(fx.store.registry.Panes)
			native := &fakeNativeThreadController{createBinding: codexappserver.ThreadBinding{ThreadID: "thread-empty-" + string(producer)}}
			legacyResume := newFakeResumeLauncher()
			plain := fx.create.agents.(*fakeAgentLauncher)
			panes := &fakeNativePaneLauncher{}
			fx.create.codexNative = native
			fx.create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: legacyResume, fakeNativePaneLauncher: panes}

			err := fx.create.createFromIntent(agentPaneIntent{
				producer: producer, provider: aiModeCodex, placement: "right", anchorPaneID: fx.originID,
			}, ioDiscard{}, ioDiscard{})
			if err != nil {
				t.Fatal(err)
			}
			if native.currentCalls != 0 || native.resolveCalls != 0 || len(native.creates) != 0 || len(native.resumes) != 0 ||
				len(plain.plans) != 1 || len(plain.bound) != 1 || len(legacyResume.plans) != 0 || len(panes.plans) != 0 || len(panes.bound) != 0 {
				t.Fatalf("split producer changed lane: current=%d resolve=%d native create=%+v resume=%+v plain=%+v/%+v native plans=%+v bindings=%+v",
					native.currentCalls, native.resolveCalls, native.creates, native.resumes, plain.plans, plain.bound, panes.plans, panes.bound)
			}
			calls := splitWindowCalls(fx.tmux)
			if len(calls) != 1 {
				t.Fatalf("split calls = %v, want exactly one", calls)
			}
			if len(fx.store.registry.Agents) != beforeAgents+1 || len(fx.store.registry.Panes) != beforePanes+1 {
				t.Fatalf("split producer cardinality agents=%d panes=%d, want 1/1",
					len(fx.store.registry.Agents)-beforeAgents, len(fx.store.registry.Panes)-beforePanes)
			}
			joined := strings.Join(calls[0], " ")
			if !strings.Contains(joined, "exec codex") || strings.Contains(joined, "--remote") || strings.Contains(joined, "thread-empty-") {
				t.Fatalf("split producer launch = %v, want one plain Codex TUI", calls[0])
			}
			var running int
			for _, agent := range fx.store.registry.Agents {
				if agent.Spec.Provider == aiModeCodex && agent.Status.Phase == coremetadata.PhaseRunning && agent.Status.PaneRef != "" {
					running++
				}
			}
			if running == 0 {
				t.Fatal("split producer did not return a Running managed Codex Agent")
			}
		})
	}
}

func TestPayloadFreeCodexFallbackCarriesContentFreeDeclaredAuthority(t *testing.T) {
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, planner := newTestAgentCreateCommand(t, store, tmux)
	binder := testAICommand(t.TempDir())
	create.agents = &productionBindingAgentLauncher{fakeAgentLauncher: planner, binder: binder}
	native := &fakeNativeThreadController{
		currentErr: errors.New("payload-free fallback must not inspect Current"),
		createErr:  errors.New("payload-free fallback must not start a thread"),
	}
	create.codexNative = native
	create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: &fakeNativePaneLauncher{}}

	stdout, stderr, err := runRoute(t, create,
		"agent", "--provider", "codex", "--project", "alpha", "--window", "main", "-o", "pane-id")
	if err != nil || stderr != "" {
		t.Fatalf("payload-free fallback stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	paneID := strings.TrimSpace(stdout)
	agent := agentNamed(t, store, "win-alpha-main", "agent-test-1")
	pane, ok := store.registry.Pane(agent.Status.PaneRef)
	if !ok || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.SessionRef != nil ||
		pane.Status.Activation.Codex != nil || pane.Status.Activation.RuntimeID != paneID {
		t.Fatalf("payload-free fallback identity = agent:%#v pane:%#v", agent.Status, pane.Status)
	}
	if native.currentCalls != 0 || native.resolveCalls != 0 || len(native.creates) != 0 || len(native.resumes) != 0 {
		t.Fatalf("payload-free fallback touched native provider state: current=%d resolve=%d creates=%+v resumes=%+v",
			native.currentCalls, native.resolveCalls, native.creates, native.resumes)
	}
	_, _, runtimePane := tmux.pane(paneID)
	if runtimePane == nil || runtimePane.opts[aiPaneCodexAuthorityOption] != codexAuthorityHook ||
		runtimePane.opts[aiPaneCodexReasonOption] != codexNativeUnexplainedReason ||
		runtimePane.opts[aiPaneCodexDeclaredOption] != codexNativeDeclaredPayloadFreeFallback {
		t.Fatalf("payload-free fallback content-free authority signal = %#v", runtimePane)
	}
}

// TestPhase7PayloadFreeReadinessFailureIsNegativeSafetyEvidenceOnly retains
// the typed historical outcome as a safety witness, not as a successful fresh
// create. Phase 0 never reaches this post-provider state for payload-free
// input because its lane is decided before Current/Resolve/Create.
func TestPhase7PayloadFreeReadinessFailureIsNegativeSafetyEvidenceOnly(t *testing.T) {
	providerCause := errors.New("secret-provider-content-must-not-survive")
	readinessErr := codexappserver.NewDurableResumeError(
		codexappserver.DurableResumeTimeout, "thread-readiness-failure", 4, providerCause)
	var readiness *codexappserver.DurableResumeError
	if !errors.As(readinessErr, &readiness) {
		t.Fatalf("historical readiness error lost its type: %v", readinessErr)
	}
	if readiness.Outcome != codexappserver.DurableResumeTimeout || readiness.ThreadID != "thread-readiness-failure" ||
		strings.Contains(readiness.Error(), "secret-provider-content") {
		t.Fatalf("historical safety outcome is not content-free: %+v", readiness)
	}
	for _, row := range codexNativeLaunchOutcomeTable {
		if row.Action == "create" && row.NativeResult == "thread started; readiness deadline" {
			t.Fatalf("historical safe failure is still classified as functional create success: %+v", row)
		}
	}
}

var phase3EmptyPromptSymbolMigrationReceipt = map[string]string{
	"TestEmptyPromptCodexCreateUsesOnePlainCLILaneAndNoNativeBinding":                 "payload-free-pre-provider-plain-success",
	"TestEmptyPromptCodexSplitProducersKeepOnePlainCLILane":                           "canonical-intent-payload-free-parity",
	"TestPayloadFreeCodexFallbackCarriesContentFreeDeclaredAuthority":                 "content-free-reduced-native-control",
	"TestPayloadFreeCodexCreateUsesSafePlainFallbackAndInteractiveOnlyEquivalentLane": "canonical-shortcut-interactive-argv-parity",
	"TestInstalledPayloadFreePlainFallbackOutcomeSmoke":                               "installed-payload-free-plain-success",
	"TestInstalledIsolatedGenerationPinnedEmptyPromptCreateSmoke":                     "historical-negative-safety-evidence",
	"TestInstalledDefaultUpgradeOrdinaryCreatesActivateManagedGeneration":             "historical-generation-fixture-negative-parity",
}

var payloadFreeInstalledOutcomeSymbolRefs = []func(*testing.T){
	TestInstalledPayloadFreePlainFallbackOutcomeSmoke,
	TestInstalledIsolatedGenerationPinnedEmptyPromptCreateSmoke,
	TestInstalledDefaultUpgradeOrdinaryCreatesActivateManagedGeneration,
}

func TestPhase3EmptyPromptSymbolsHaveExplicitDurableResumeMigrationReceipt(t *testing.T) {
	want := map[string]string{
		"TestEmptyPromptCodexCreateUsesOnePlainCLILaneAndNoNativeBinding":                 "payload-free-pre-provider-plain-success",
		"TestEmptyPromptCodexSplitProducersKeepOnePlainCLILane":                           "canonical-intent-payload-free-parity",
		"TestPayloadFreeCodexFallbackCarriesContentFreeDeclaredAuthority":                 "content-free-reduced-native-control",
		"TestPayloadFreeCodexCreateUsesSafePlainFallbackAndInteractiveOnlyEquivalentLane": "canonical-shortcut-interactive-argv-parity",
		"TestInstalledPayloadFreePlainFallbackOutcomeSmoke":                               "installed-payload-free-plain-success",
		"TestInstalledIsolatedGenerationPinnedEmptyPromptCreateSmoke":                     "historical-negative-safety-evidence",
		"TestInstalledDefaultUpgradeOrdinaryCreatesActivateManagedGeneration":             "historical-generation-fixture-negative-parity",
	}
	if !reflect.DeepEqual(phase3EmptyPromptSymbolMigrationReceipt, want) {
		t.Fatalf("Phase 3 symbol migration receipt = %v, want %v", phase3EmptyPromptSymbolMigrationReceipt, want)
	}
	if len(payloadFreeInstalledOutcomeSymbolRefs) != 3 || payloadFreeInstalledOutcomeSymbolRefs[0] == nil ||
		payloadFreeInstalledOutcomeSymbolRefs[1] == nil || payloadFreeInstalledOutcomeSymbolRefs[2] == nil {
		t.Fatalf("installed symbol migration refs = %v", payloadFreeInstalledOutcomeSymbolRefs)
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
	if len(codexNativeLaunchOutcomeTable) != 11 {
		t.Fatalf("outcome rows=%d, want 11: %+v", len(codexNativeLaunchOutcomeTable), codexNativeLaunchOutcomeTable)
	}
	var plainLane, refused, negative []string
	for _, row := range codexNativeLaunchOutcomeTable {
		switch {
		case row.Action == "negative fixture":
			negative = append(negative, row.NativeResult)
		case strings.Contains(row.Launch, "current CLI"):
			plainLane = append(plainLane, row.NativeResult)
		case row.Launch == "none":
			refused = append(refused, row.NativeResult)
		}
	}
	if !slices.Equal(plainLane, []string{"no payload", "explicit " + interactiveOnlyFlag, "rollout picker source"}) {
		t.Fatalf("plain CLI rows drifted: %v", plainLane)
	}
	if len(refused) != 5 {
		t.Fatalf("functional refusal rows = %v, want the five unproven-authority rows", refused)
	}
	if !slices.Equal(negative, []string{"payload-free thread not durably resumable"}) {
		t.Fatalf("negative safety rows = %v, want the one historical Phase-7 fixture", negative)
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

// codexInventoryLedger records every lifecycle-capable call a read-only route
// inventory could make, so "start/stop/admit/drain 0" is an assertion rather
// than a claim.
type codexInventoryLedger struct {
	activations  int
	observations []coremetadata.CodexEndpointRef
	journalBytes []byte
	journalMod   time.Time
}

func (ledger *codexInventoryLedger) Ensure(context.Context) error {
	ledger.activations++
	return nil
}

func snapshotCodexJournal(t *testing.T, store *codexupgrade.Store) ([]byte, time.Time) {
	t.Helper()
	body, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat journal: %v", err)
	}
	return body, info.ModTime()
}

// codexPoolInventoryFixture writes one Current + one Draining private route so
// the catalog inventory has more than the admission-current slot to project.
func codexPoolInventoryFixture(t *testing.T) (*codexupgrade.Store, codexupgrade.GenerationRoute, codexupgrade.GenerationRoute) {
	t.Helper()
	root := t.TempDir()
	store := codexupgrade.NewStateStore(filepath.Join(root, "state"))
	stateDomain := "test-domain-inventory"
	route := func(version, bundle string, state codexgeneration.GenerationState) codexupgrade.GenerationRoute {
		endpoint := coremetadata.CodexEndpointRef{StateDomainID: stateDomain, EndpointGenerationID: "codex-" + version}
		config := codexupgrade.GenerationConfig{
			Endpoint: endpoint, StateDomainPath: filepath.Join(root, "domain"), PrivateRoot: filepath.Join(root, "runtime", version),
			SocketPath: filepath.Join(root, "runtime", version, "s"), LeaseRoot: filepath.Join(root, "lease", version),
			RequiredProtocol: codexbundle.ProtocolRange{Min: 2, Max: 2},
		}
		return codexupgrade.GenerationRoute{
			Generation: codexgeneration.Generation{Endpoint: endpoint, State: state, Owner: codexgeneration.OwnerProjmuxPrivate, BundleID: bundle},
			Version:    version, Config: config, TUIPath: filepath.Join(root, "lease", version, "bin", "codex"), Ready: true,
			Proof: &codexgenerationhost.LaunchProof{
				Endpoint:   codexgenerationhost.EndpointIdentity{StateDomainID: endpoint.StateDomainID, EndpointGenerationID: endpoint.EndpointGenerationID},
				SocketPath: config.SocketPath, BundleID: bundle,
			},
		}
	}
	draining := route("0.152.1", "sha256-draining", codexgeneration.StateDraining)
	current := route("0.153.0", "sha256-current", codexgeneration.StateCurrent)
	if _, err := store.Update(context.Background(), func(journal *codexupgrade.Journal, _ bool) error {
		*journal = codexupgrade.Journal{
			Version: codexupgrade.JournalVersion, StateDomainID: stateDomain,
			CurrentGenerationID: current.Generation.Endpoint.EndpointGenerationID,
			Routes:              []codexupgrade.GenerationRoute{draining, current},
		}
		return nil
	}); err != nil {
		t.Fatalf("seed pool inventory: %v", err)
	}
	return store, draining, current
}

func TestCatalogRoutesProjectsCurrentAndDrainingWithZeroLifecycleWrites(t *testing.T) {
	store, draining, current := codexPoolInventoryFixture(t)
	ledger := &codexInventoryLedger{}
	ledger.journalBytes, ledger.journalMod = snapshotCodexJournal(t, store)
	controller := rollingCodexNativeThreadController{
		journal: store,
		fallback: defaultCodexNativeThreadController{current: func(context.Context) (codexNativeEndpointRoute, error) {
			t.Error("pool inventory fell through to the ambient default endpoint")
			return codexNativeEndpointRoute{}, errFakeNativeUnavailable
		}},
		activator: ledger,
		observe: func(_ context.Context, route codexupgrade.GenerationRoute) error {
			ledger.observations = append(ledger.observations, route.Generation.Endpoint)
			return nil
		},
	}

	routes, err := controller.CatalogRoutes(context.Background())
	if err != nil {
		t.Fatalf("catalog routes: %v", err)
	}
	want := []codexupgrade.GenerationRoute{draining, current}
	if len(routes) != len(want) {
		t.Fatalf("catalog inventory = %+v want %d routes", routes, len(want))
	}
	for i, route := range routes {
		if !route.Endpoint.Same(want[i].Generation.Endpoint) || route.State != want[i].Generation.State ||
			route.SocketPath != want[i].Config.SocketPath || route.TUIExecutable != want[i].TUIPath || route.Default {
			t.Fatalf("route %d identity drifted: got=%+v want=%+v", i, route, want[i])
		}
	}
	if ledger.activations != 0 || len(ledger.observations) != 2 ||
		!ledger.observations[0].Same(draining.Generation.Endpoint) || !ledger.observations[1].Same(current.Generation.Endpoint) {
		t.Fatalf("inventory lifecycle ledger: activations=%d observations=%+v", ledger.activations, ledger.observations)
	}
	body, modified := snapshotCodexJournal(t, store)
	if !reflect.DeepEqual(body, ledger.journalBytes) || !modified.Equal(ledger.journalMod) {
		t.Fatalf("read-only inventory rewrote the admission journal: bytes-equal=%t mtime %s -> %s",
			reflect.DeepEqual(body, ledger.journalBytes), ledger.journalMod, modified)
	}
	journal, exists, err := store.Load()
	if err != nil || !exists || journal.CurrentGenerationID != current.Generation.Endpoint.EndpointGenerationID ||
		len(journal.Routes) != 2 || journal.Operation != nil {
		t.Fatalf("admission-current or pool shape changed: exists=%t err=%v journal=%+v", exists, err, journal)
	}
}

func TestUnobservableCatalogRoutesRefuseWithoutMutatingThePool(t *testing.T) {
	store, _, _ := codexPoolInventoryFixture(t)
	ledger := &codexInventoryLedger{}
	ledger.journalBytes, ledger.journalMod = snapshotCodexJournal(t, store)
	controller := rollingCodexNativeThreadController{
		journal: store,
		fallback: defaultCodexNativeThreadController{current: func(context.Context) (codexNativeEndpointRoute, error) {
			t.Error("unobservable pool fell through to the ambient default endpoint")
			return codexNativeEndpointRoute{}, errFakeNativeUnavailable
		}},
		activator: ledger,
		observe:   func(context.Context, codexupgrade.GenerationRoute) error { return errFakeNativeUnavailable },
	}

	routes, err := controller.CatalogRoutes(context.Background())
	var routeErr *codexNativeRouteError
	if len(routes) != 0 || !errors.As(err, &routeErr) || routeErr.Reason != codexNativeReasonGenerationUnavailable {
		t.Fatalf("unobservable inventory = %+v, %v", routes, err)
	}
	if ledger.activations != 0 {
		t.Fatalf("refusal ran %d activations", ledger.activations)
	}
	body, modified := snapshotCodexJournal(t, store)
	if !reflect.DeepEqual(body, ledger.journalBytes) || !modified.Equal(ledger.journalMod) {
		t.Fatal("refused inventory rewrote the admission journal")
	}
}

func TestDefaultCatalogRoutesIsOneAttachOnlyReadWithoutLifecycleCalls(t *testing.T) {
	route := nativeTestRoute("generation-default-inventory", coremetadata.CodexGenerationCurrent)
	route.Default, route.SocketPath = true, ""
	currentCalls := 0
	controller := defaultCodexNativeThreadController{
		current: func(context.Context) (codexNativeEndpointRoute, error) {
			currentCalls++
			return route, nil
		},
		open: func(context.Context, codexNativeEndpointRoute, bool) (codexNativeThreadClient, error) {
			t.Error("read-only inventory opened a thread client")
			return nil, errFakeNativeUnavailable
		},
		awaitDurable: func(context.Context, codexNativeEndpointRoute, coremetadata.AgentWorkspace, string) error {
			t.Error("read-only inventory awaited a durable binding")
			return nil
		},
		guard: func(context.Context, codexNativeEndpointRoute) error {
			t.Error("read-only inventory ran an admission guard")
			return nil
		},
	}

	routes, err := controller.CatalogRoutes(context.Background())
	if err != nil || len(routes) != 1 || !routes[0].Endpoint.Same(route.Endpoint) ||
		routes[0].State != route.State || !routes[0].Default || currentCalls != 1 {
		t.Fatalf("default inventory = %+v, %v (current calls %d)", routes, err, currentCalls)
	}
}
