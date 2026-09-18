package app

import (
	"bytes"
	"errors"
	"maps"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

// window_create_one_transaction_test.go is the UI new Window's commit
// contract: the answer the operator gave -- a provider or a resumed session --
// is committed with the Window in one Registry transaction, in the same shape
// `create window --provider` commits, and an Agent that cannot be opened rolls
// the whole Window back and says so in one line.

// answeredWindowCreateRoute is the generated Window create over the real
// canonical producer and the fixture's fake server, with the saved launch
// default already answered.
func answeredWindowCreateRoute(t *testing.T, control bool, answer agentPaneIntent) *windowCreateIntentRoute {
	t.Helper()
	route := newWindowCreateIntentRoute(t, control, true)
	route.cmd.launchChoose = func(string, string) launchChoice { return launchChoice{intent: answer} }
	return route
}

// createdAgentOnlyWindow returns the one Window the route created and fails
// unless it holds exactly one Agent whose one managed Pane is the Window's
// anchor and only live Pane, with no default shell left.
func (r *windowCreateIntentRoute) createdAgentOnlyWindow(t *testing.T, before map[string]bool) (coremetadata.Window, coremetadata.Agent, string) {
	t.Helper()
	var created []coremetadata.Window
	for _, window := range r.store.registry.Windows {
		if !before[window.Metadata.UID] {
			created = append(created, window)
		}
	}
	if len(created) != 1 {
		t.Fatalf("created Windows = %d, want exactly one\n%s", len(created), r.store.snapshot())
	}
	window := created[0]
	if shells := r.store.registry.PanesOf(window.Metadata.UID); len(shells) != 0 {
		t.Fatalf("created Window still owns Panes %+v, want only the Agent's Pane\n%s", shells, r.store.snapshot())
	}
	agents := r.store.registry.AgentsOf(window.Metadata.UID)
	if len(agents) != 1 {
		t.Fatalf("created Window Agents = %+v, want exactly one", agents)
	}
	panes := r.store.registry.PanesOf(agents[0].Metadata.UID)
	if len(panes) != 1 || panes[0].Spec.Role != coremetadata.PaneRoleAgent || agents[0].Status.PaneRef != panes[0].Metadata.UID {
		t.Fatalf("Agent Panes = %+v (paneRef %q), want exactly its one Agent Pane", panes, agents[0].Status.PaneRef)
	}
	if window.Spec.AnchorPaneRef != panes[0].Metadata.UID || strings.TrimSpace(window.Spec.DefaultShellPaneRef) != "" {
		t.Fatalf("Window anchor = %q default shell = %q, want the Agent Pane %q and no shell",
			window.Spec.AnchorPaneRef, window.Spec.DefaultShellPaneRef, panes[0].Metadata.UID)
	}
	session, live := r.tmux.window(window.Status.RuntimeID)
	if session == nil || live == nil || len(live.panes) != 1 {
		t.Fatalf("live Window %s = %+v, want exactly one live Pane\n%s", window.Status.RuntimeID, live, r.tmux.state())
	}
	paneID := livePaneWithUID(t, r.tmux, panes[0].Metadata.UID)
	if live.panes[0].id != paneID {
		t.Fatalf("live Window Pane = %s, want the Agent Pane %s", live.panes[0].id, paneID)
	}
	return window, agents[0], paneID
}

// assertMovedOnceOnto checks the pressing client was moved exactly once, onto
// the committed Window, after the one commit.
func (r *windowCreateIntentRoute) assertMovedOnceOnto(t *testing.T, window coremetadata.Window) {
	t.Helper()
	moves, first := clientMovingCalls(r.tmux.calls)
	want := [][]string{
		{"switch-client", "-c", windowCreatePressingClient, "-t", window.Status.RuntimeSessionID},
		{"select-window", "-t", window.Status.RuntimeSessionID + ":" + window.Status.RuntimeID},
	}
	if !equalArgvs(moves, want) {
		t.Fatalf("client moves = %v, want exactly %v", moves, want)
	}
	if first < r.commitCall {
		t.Fatalf("client moved at call %d, before the commit returned at call %d", first, r.commitCall)
	}
}

// assertNothingCreated checks a refused Agent answer left the Registry and the
// live server exactly as they were, moved nobody, and said one line that names
// the reason and that no Window was created.
func (r *windowCreateIntentRoute) assertNothingCreated(t *testing.T, registryBefore, runtimeBefore string, reason string) {
	t.Helper()
	if got := r.store.snapshot(); got != registryBefore {
		t.Fatalf("refused Agent answer changed the Registry\nbefore=%s\nafter=%s", registryBefore, got)
	}
	if got := r.tmux.state(); got != runtimeBefore {
		t.Fatalf("refused Agent answer left live state\nbefore=%s\nafter=%s", runtimeBefore, got)
	}
	if moves, _ := clientMovingCalls(r.tmux.calls); len(moves) != 0 || r.tmux.argvContains("list-clients") {
		t.Fatalf("refused Agent answer reached the client move: %v", moves)
	}
	if len(r.tmux.clientMessages) != 1 || r.tmux.clientMessages[0].client != windowCreatePressingClient {
		t.Fatalf("client messages = %+v, want one line on the pressing client", r.tmux.clientMessages)
	}
	text := r.tmux.clientMessages[0].text
	if !strings.HasPrefix(text, "projmux Create Window failed: ") || !strings.HasSuffix(text, "; no Window was created") ||
		!strings.Contains(text, reason) || strings.Contains(text, "\n") || strings.Contains(text, windowCreatedMessage) {
		t.Fatalf("refusal line = %q, want one not-created line naming %q", text, reason)
	}
}

// TestWindowCreateAgentAnswerCommitsWindowAndAgentInOneTransaction is C-2
// acceptance 1: a provider answer is one Registry transaction that leaves one
// Window holding exactly its Agent Pane -- the anchor, no default shell, the
// only live Pane -- and the pressing client lands on it after the commit.
func TestWindowCreateAgentAnswerCommitsWindowAndAgentInOneTransaction(t *testing.T) {
	for _, control := range []bool{false, true} {
		rootName := "Project"
		if control {
			rootName = "ControlSession"
		}
		for _, provider := range []string{aiModeClaude, aiModeCodex} {
			t.Run(rootName+"/"+provider, func(t *testing.T) {
				route := answeredWindowCreateRoute(t, control, agentPaneIntent{
					producer: canonicalProducerSavedDefault, provider: provider, placement: "right",
				})
				before := route.windowUIDs()
				transactions := route.store.transactions

				if err := route.run(); err != nil {
					t.Fatalf("window-create route: %v", err)
				}
				if got := route.store.transactions - transactions; got != 1 {
					t.Fatalf("Registry transactions = %d, want exactly 1 for the Window and its Agent", got)
				}
				window, agent, _ := route.createdAgentOnlyWindow(t, before)
				if agent.Spec.Provider != provider || agent.Status.SessionRef != nil {
					t.Fatalf("created Agent = %+v, want a fresh %s Agent", agent, provider)
				}
				route.assertMovedOnceOnto(t, window)
				if len(route.tmux.clientMessages) != 1 || route.tmux.clientMessages[0].text != windowCreatedMessage {
					t.Fatalf("client messages = %+v, want the one created line", route.tmux.clientMessages)
				}
			})
		}
	}
}

// TestWindowCreateResumeAnswerOpensThatSessionInTheOneTransaction is C-2
// acceptance 2: a resumed session answer, a Codex app-server thread included,
// commits the same Window shape in the same one transaction, and the Agent in it
// is that session.
func TestWindowCreateResumeAnswerOpensThatSessionInTheOneTransaction(t *testing.T) {
	t.Run("Claude transcript", func(t *testing.T) {
		const conversation = "claude-picker-session"
		route := answeredWindowCreateRoute(t, false, agentPaneIntent{
			producer: canonicalProducerResumePicker, provider: aiModeClaude, placement: "right", conversationID: conversation,
		})
		before := route.windowUIDs()
		transactions := route.store.transactions

		if err := route.run(); err != nil {
			t.Fatalf("window-create route: %v", err)
		}
		if got := route.store.transactions - transactions; got != 1 {
			t.Fatalf("Registry transactions = %d, want exactly 1", got)
		}
		window, agent, _ := route.createdAgentOnlyWindow(t, before)
		if agent.Status.SessionRef == nil || agent.Status.SessionRef.Provider != aiModeClaude ||
			agent.Status.SessionRef.ConversationID() != conversation {
			t.Fatalf("created Agent sessionRef = %#v, want %s", agent.Status.SessionRef, conversation)
		}
		fresh := route.create.agents.(*fakeAgentLauncher)
		resume := route.create.resumes.(*fakeResumeLauncher)
		if len(fresh.plans) != 0 || len(resume.plans) != 1 || resume.plans[0].conversationID != conversation {
			t.Fatalf("launch plans fresh=%+v resume=%+v, want one resume of %s", fresh.plans, resume.plans, conversation)
		}
		route.assertMovedOnceOnto(t, window)
	})

	t.Run("Codex app-server thread", func(t *testing.T) {
		const id = "019f0000-0000-7000-8000-000000000043"
		endpoint := nativeTestRoute("generation-window", coremetadata.CodexGenerationCurrent)
		route := answeredWindowCreateRoute(t, false, agentPaneIntent{
			producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right",
			conversationID: id, resumeSource: aisessions.SourceCodexAppServer,
			resumeEndpoint: endpoint.Endpoint, resumeGenerationState: coremetadata.CodexGenerationCurrent,
		})
		native := &fakeNativeThreadController{resolvedRoute: endpoint, resumeBinding: codexappserver.ThreadBinding{ThreadID: id}}
		panes := &fakeNativePaneLauncher{}
		route.create.codexNative = native
		route.create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: panes}
		before := route.windowUIDs()
		transactions := route.store.transactions

		if err := route.run(); err != nil {
			t.Fatalf("window-create route: %v", err)
		}
		if got := route.store.transactions - transactions; got != 1 {
			t.Fatalf("Registry transactions = %d, want exactly 1", got)
		}
		window, agent, _ := route.createdAgentOnlyWindow(t, before)
		if len(native.creates) != 0 || len(native.resumes) != 1 || native.resumes[0].threadID != id ||
			len(panes.plans) != 1 || panes.plans[0].threadID != id || len(panes.bound) != 1 || panes.bound[0].threadID != id {
			t.Fatalf("native creates=%+v resumes=%+v plans=%+v bound=%+v, want one resume of %s",
				native.creates, native.resumes, panes.plans, panes.bound, id)
		}
		ref := agent.Status.SessionRef
		if ref == nil || ref.Codex == nil || ref.Codex.ThreadID != id || ref.Codex.Endpoint == nil ||
			!ref.Codex.Endpoint.Same(endpoint.Endpoint) {
			t.Fatalf("created Agent sessionRef = %#v, want thread %s on %+v", ref, id, endpoint.Endpoint)
		}
		route.assertMovedOnceOnto(t, window)
	})
}

// TestWindowCreateAgentFailureRollsTheWindowBackAndSaysOneLine is C-2
// acceptance 3 and user decision 4: an Agent that cannot be opened after the
// Window already exists live rolls the whole transaction back -- no Window in
// the Registry or on the server -- moves nobody, and the pressing client reads
// one not-created line.
func TestWindowCreateAgentFailureRollsTheWindowBackAndSaysOneLine(t *testing.T) {
	claude := agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeClaude, placement: "right"}
	for _, tt := range []struct {
		name   string
		answer agentPaneIntent
		inject func(*windowCreateIntentRoute)
		reason string
	}{
		{
			name: "provider launch cannot be built", answer: claude,
			inject: func(r *windowCreateIntentRoute) {
				r.create.agents.(*fakeAgentLauncher).planErr = errors.New("injected missing provider binary")
			},
			reason: "injected missing provider binary",
		},
		{
			name: "Agent Pane split fails", answer: claude,
			inject: func(r *windowCreateIntentRoute) {
				r.tmux.fail = []string{"split-window"}
				r.tmux.failMessage = "injected split failure"
			},
			reason: "injected split failure",
		},
		{
			name: "resumed session refused",
			answer: agentPaneIntent{producer: canonicalProducerResumePicker, provider: aiModeClaude, placement: "right",
				conversationID: "claude-picker-session"},
			inject: func(r *windowCreateIntentRoute) {
				r.create.resumes.(*fakeResumeLauncher).planErr = errors.New("provider refused exact picker conversation")
			},
			reason: "provider refused exact picker conversation",
		},
		{
			name: "Registry commit fails after the runtime exists", answer: claude,
			inject: func(r *windowCreateIntentRoute) {
				r.create.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
					r.store.transactions++
					working := r.store.registry.Clone()
					if err := fn(&working); err != nil {
						return coremetadata.Registry{}, err
					}
					return coremetadata.Registry{}, errors.New("injected Registry commit failure")
				}
			},
			reason: "injected Registry commit failure",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			route := answeredWindowCreateRoute(t, false, tt.answer)
			registryBefore, runtimeBefore := route.store.snapshot(), route.tmux.state()
			tt.inject(route)

			if err := route.run(); err != nil {
				t.Fatalf("displayed refusal escaped as an exit code: %v", err)
			}
			if route.store.writes != 0 {
				t.Fatalf("Registry writes = %d, want none", route.store.writes)
			}
			route.assertNothingCreated(t, registryBefore, runtimeBefore, tt.reason)
		})
	}
}

// TestWindowCreateDisabledProviderAnswerCreatesNothingAndSaysOneLine is the
// Settings gate on the new Window: a provider the operator switched off is
// refused before any transaction or tmux mutation, with the same one line.
func TestWindowCreateDisabledProviderAnswerCreatesNothingAndSaysOneLine(t *testing.T) {
	route := answeredWindowCreateRoute(t, false, agentPaneIntent{
		producer: canonicalProducerSavedDefault, provider: aiModeClaude, placement: "right",
	})
	route.create.agents.(*fakeAgentLauncher).disabled = map[string]bool{aiModeClaude: true}
	registryBefore, runtimeBefore := route.store.snapshot(), route.tmux.state()
	transactions := route.store.transactions

	if err := route.run(); err != nil {
		t.Fatalf("displayed refusal escaped as an exit code: %v", err)
	}
	if route.store.transactions != transactions || route.store.writes != 0 {
		t.Fatalf("disabled provider opened %d transaction(s) and %d write(s)", route.store.transactions-transactions, route.store.writes)
	}
	for _, call := range route.tmux.calls {
		if argv := tmuxCommandArgv(call); len(argv) > 0 && argv[0] != "display-message" {
			t.Fatalf("disabled provider reached tmux: %v", call)
		}
	}
	route.assertNothingCreated(t, registryBefore, runtimeBefore, "disabled")
}

// TestWindowCreateFromAnAgentPaneNeverRecordsTheCreator is owner ruling T3-1:
// the new-Window key pressed inside an Agent's Pane is the operator's act, so
// the Agent it opens carries no creator annotation -- while the explicit
// `create window --provider` from the same Pane still records one.
func TestWindowCreateFromAnAgentPaneNeverRecordsTheCreator(t *testing.T) {
	fx := newCreatorFixture(t)
	withPopupOrigin(fx.command, fx.tmux, func(key string) string { return fx.env[key] })
	before := fx.agentUIDs()
	var stdout, stderr bytes.Buffer
	if _, err := fx.command.createWindowFromIntent(windowCreateIntent{
		anchorPaneID: fx.creatorID,
		answer:       agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeClaude, placement: "right"},
	}, &stdout, &stderr); err != nil {
		t.Fatalf("UI Window create: %v (stderr=%q)", err, stderr.String())
	}
	if agents, _ := fx.newAgentsSince(t, before); len(agents) != 1 {
		t.Fatalf("UI Window create opened %d Agents, want 1", len(agents))
	}
	assertNoCreatorKeysAnywhere(t, fx.store)
	if got := creatorQueryCount(fx.tmux); got != 0 || strings.Contains(stderr.String(), "creator not recorded") {
		t.Fatalf("UI Window create observed the creator: queries=%d stderr=%q", got, stderr.String())
	}

	// Control: the same command, Pane, and seams record on the explicit route.
	before = fx.agentUIDs()
	if out, errOut, err := runRoute(t, fx.command, "window", "--provider", "claude", "--project", "uid:prj-alpha"); err != nil || errOut != "" {
		t.Fatalf("explicit control create: stdout=%q stderr=%q err=%v", out, errOut, err)
	}
	agents, _ := fx.newAgentsSince(t, before)
	if len(agents) != 1 || !maps.Equal(creatorKeysOf(agents[0].Metadata), coremetadata.CreatorAnnotations(fx.creatorAgent, fx.creatorPane)) {
		t.Fatalf("explicit control create did not record the creator: %+v", agents)
	}
}
