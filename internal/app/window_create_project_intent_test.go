package app

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/aisessions"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// window_create_project_intent_test.go is the Project scope of the canonical
// Window create intent: a caller with no Pane to anchor on names an exact
// Project UID, and the Window and the Agent the answer names commit in one
// transaction whether that Project is live or stopped. A stopped Project is
// started the way fresh `create window --project uid:<uid>` starts it.

// projectWindowIntentFixture is the canonical create over the shared resource
// fixture with no origin Pane at all. The runtime route binders only record
// which authority the create selected.
type projectWindowIntentFixture struct {
	store    *fakeResourceStore
	tmux     *fakeTmux
	create   *createCommand
	sessions *fakeSessionMaterializer
	natural  int
	explicit int
}

// newProjectWindowIntentFixture seeds either the live Project alpha (its
// session running) or leaves every session absent, so prj-beta is stopped.
func newProjectWindowIntentFixture(t *testing.T, live bool) *projectWindowIntentFixture {
	t.Helper()
	store, tmux := newFakeResourceStore(t), newFakeTmux()
	if live {
		store, tmux = aliveAlphaRuntime(t)
	}
	create, _ := newTestAgentCreateCommand(t, store, tmux)
	create.resumes = newFakeResumeLauncher()
	fx := &projectWindowIntentFixture{store: store, tmux: tmux, create: create,
		sessions: create.runtime.sessions.(*fakeSessionMaterializer)}
	create.bindRuntime = func(context.Context) error { fx.natural++; return nil }
	create.bindExplicitRuntime = func(context.Context) error { fx.explicit++; return nil }
	return fx
}

func (fx *projectWindowIntentFixture) windowUIDs() map[string]bool {
	uids := map[string]bool{}
	for _, window := range fx.store.registry.Windows {
		uids[window.Metadata.UID] = true
	}
	return uids
}

// agentOnlyWindow reuses the anchor route's end-state check: one new Window
// whose only Pane, live and in the Registry, is its one Agent's Pane.
func (fx *projectWindowIntentFixture) agentOnlyWindow(t *testing.T, before map[string]bool) (coremetadata.Window, coremetadata.Agent, string) {
	t.Helper()
	route := &windowCreateIntentRoute{canonicalRootFixture: canonicalRootFixture{store: fx.store, tmux: fx.tmux}}
	return route.createdAgentOnlyWindow(t, before)
}

func tmuxCallsNamed(tmux *fakeTmux, verb string) [][]string {
	var calls [][]string
	for _, call := range tmux.calls {
		if argv := tmuxCommandArgv(call); len(argv) > 0 && argv[0] == verb {
			calls = append(calls, argv)
		}
	}
	return calls
}

func resumeAnswer(provider, conversation string) agentPaneIntent {
	answer := agentPaneIntent{producer: canonicalProducerResumePicker, provider: provider, placement: "right", conversationID: conversation}
	if provider == aiModeCodex {
		answer.resumeSource = aisessions.SourceCodexRollout
	}
	return answer
}

// TestProjectWindowIntentStartsAStoppedProjectWithTheResumedAgent is A1: on a
// Project with no session, one transaction starts the session, commits one new
// Window whose only Pane is the resumed Agent's, records the answer's
// conversation as the Agent's sessionRef, and marks the Project live.
func TestProjectWindowIntentStartsAStoppedProjectWithTheResumedAgent(t *testing.T) {
	for _, test := range []struct{ provider, conversation string }{
		{aiModeClaude, "claude-picker-session"},
		{aiModeCodex, "codex-picker-thread"},
	} {
		t.Run(test.provider, func(t *testing.T) {
			fx := newProjectWindowIntentFixture(t, false)
			before := fx.windowUIDs()
			var stdout bytes.Buffer

			placement, err := fx.create.createWindowFromIntent(windowCreateIntent{
				projectUID: "prj-beta", answer: resumeAnswer(test.provider, test.conversation),
			}, &stdout, ioDiscard{})
			if err != nil {
				t.Fatalf("Project Window intent: %v", err)
			}
			if fx.store.transactions != 1 || fx.store.writes != 1 {
				t.Fatalf("Registry transactions=%d writes=%d, want exactly one for the session, Window, and Agent",
					fx.store.transactions, fx.store.writes)
			}
			if !slices.Equal(fx.sessions.created, []string{"beta"}) {
				t.Fatalf("started sessions = %v, want [beta]", fx.sessions.created)
			}
			window, agent, paneID := fx.agentOnlyWindow(t, before)
			if window.Metadata.OwnerUID() != "prj-beta" {
				t.Fatalf("created Window owner = %q, want prj-beta", window.Metadata.OwnerUID())
			}
			ref := agent.Status.SessionRef
			if ref == nil || ref.Provider != test.provider || ref.ConversationID() != test.conversation {
				t.Fatalf("created Agent sessionRef = %#v, want %s %s", ref, test.provider, test.conversation)
			}
			project, _ := fx.store.registry.Project("prj-beta")
			if project.Status.Session == nil || !project.Status.Session.Live || project.Status.Session.Name != "beta" {
				t.Fatalf("Project status.session = %+v, want beta live", project.Status.Session)
			}
			session := fx.tmux.session("beta")
			if session == nil {
				t.Fatalf("no live beta session:\n%s", fx.tmux.state())
			}
			if placement != (createdWindowRuntime{sessionID: session.id, windowID: window.Status.RuntimeID, paneID: paneID}) ||
				window.Status.RuntimeSessionID != session.id {
				t.Fatalf("placement = %+v, Window binding %s/%s, want session %s and Agent Pane %s",
					placement, window.Status.RuntimeSessionID, window.Status.RuntimeID, session.id, paneID)
			}
			if got := session.opts[tmuxopts.ProjectUIDSession]; got != "prj-beta" {
				t.Fatalf("session Project uid mirror = %q, want prj-beta", got)
			}
			if got := session.windows[0].opts[tmuxopts.WindowUID]; got != "win-beta-main" {
				t.Fatalf("new-session's own Window adopted %q, want the first stored Window win-beta-main", got)
			}
			if got := session.env[createOperationEnvironment]; got != "" {
				t.Fatalf("create-operation lease %q left on the started session", got)
			}
			resume := fx.create.resumes.(*fakeResumeLauncher)
			if fresh := fx.create.agents.(*fakeAgentLauncher); len(fresh.plans) != 0 || len(resume.plans) != 1 ||
				resume.plans[0].conversationID != test.conversation {
				t.Fatalf("launch plans fresh=%+v resume=%+v, want one resume of %s", fresh.plans, resume.plans, test.conversation)
			}
			if fx.explicit != 1 || fx.natural != 0 {
				t.Fatalf("runtime route binds explicit=%d natural=%d, want the explicit target authority once", fx.explicit, fx.natural)
			}
			if !strings.Contains(stdout.String(), window.Metadata.Name) {
				t.Fatalf("result output %q does not name the created Window %s", stdout.String(), window.Metadata.Name)
			}
			assertNoClientMovement(t, fx.tmux)
		})
	}
}

// TestProjectWindowIntentReusesALiveProjectSession is A2: the same intent on a
// Project whose session is running adds the Window to that session and starts
// no other.
func TestProjectWindowIntentReusesALiveProjectSession(t *testing.T) {
	fx := newProjectWindowIntentFixture(t, true)
	sessionsBefore := len(fx.tmux.sessions)
	alpha := fx.tmux.session("alpha")
	before := fx.windowUIDs()

	placement, err := fx.create.createWindowFromIntent(windowCreateIntent{
		projectUID: "prj-alpha", answer: resumeAnswer(aiModeClaude, "claude-picker-session"),
	}, ioDiscard{}, ioDiscard{})
	if err != nil {
		t.Fatalf("Project Window intent on a live Project: %v", err)
	}
	if len(fx.sessions.created) != 0 || len(tmuxCallsNamed(fx.tmux, "new-session")) != 0 || len(fx.tmux.sessions) != sessionsBefore {
		t.Fatalf("live Project started sessions %v (new-session calls %d, sessions %d->%d), want none",
			fx.sessions.created, len(tmuxCallsNamed(fx.tmux, "new-session")), sessionsBefore, len(fx.tmux.sessions))
	}
	window, agent, paneID := fx.agentOnlyWindow(t, before)
	if window.Metadata.OwnerUID() != "prj-alpha" || window.Status.RuntimeSessionID != alpha.id || placement.sessionID != alpha.id ||
		placement.paneID != paneID {
		t.Fatalf("created Window %s owner %q bound in %s (placement %+v), want prj-alpha in %s",
			window.Metadata.UID, window.Metadata.OwnerUID(), window.Status.RuntimeSessionID, placement, alpha.id)
	}
	if agent.Status.SessionRef == nil || agent.Status.SessionRef.ConversationID() != "claude-picker-session" {
		t.Fatalf("created Agent sessionRef = %#v", agent.Status.SessionRef)
	}
	if got := alpha.env[createOperationEnvironment]; got != "" {
		t.Fatalf("create-operation lease %q left on the live session", got)
	}
	assertNoClientMovement(t, fx.tmux)
}

// TestProjectWindowIntentStartsAStoppedProjectLikeFreshCreateWindow is A3: on
// the same stopped Project, fresh `create window --project uid:prj-beta` and
// the Project-scoped intent start the session in the same shape -- the same
// new-session, the same first stored Window adopted into it, the same Project
// session binding. With a shell answer the whole result is identical.
func TestProjectWindowIntentStartsAStoppedProjectLikeFreshCreateWindow(t *testing.T) {
	type started struct {
		store *fakeResourceStore
		tmux  *fakeTmux
	}
	fresh := func(t *testing.T, args ...string) started {
		t.Helper()
		fx := newProjectWindowIntentFixture(t, false)
		if _, _, err := runRoute(t, fx.create, append([]string{"window", "--project", "uid:prj-beta"}, args...)...); err != nil {
			t.Fatalf("fresh create window: %v", err)
		}
		return started{fx.store, fx.tmux}
	}
	intent := func(t *testing.T, answer agentPaneIntent) started {
		t.Helper()
		fx := newProjectWindowIntentFixture(t, false)
		if _, err := fx.create.createWindowFromIntent(windowCreateIntent{projectUID: "prj-beta", answer: answer}, ioDiscard{}, ioDiscard{}); err != nil {
			t.Fatalf("Project Window intent: %v", err)
		}
		return started{fx.store, fx.tmux}
	}
	assertSameStart := func(t *testing.T, a, b started) {
		t.Helper()
		freshNew, intentNew := tmuxCallsNamed(a.tmux, "new-session"), tmuxCallsNamed(b.tmux, "new-session")
		if len(freshNew) != 1 || len(intentNew) != 1 || !slices.Equal(freshNew[0], intentNew[0]) {
			t.Fatalf("new-session fresh=%q intent=%q, want one identical call", freshNew, intentNew)
		}
		if got := flagValue(intentNew[0], "-n"); got != "main" {
			t.Fatalf("new-session -n = %q, want the first stored Window's name main", got)
		}
		for name, s := range map[string]started{"fresh": a, "intent": b} {
			session := s.tmux.session("beta")
			if session == nil || len(session.windows) != 2 || session.windows[0].opts[tmuxopts.WindowUID] != "win-beta-main" {
				t.Fatalf("%s beta session does not adopt win-beta-main and add one Window:\n%s", name, s.tmux.state())
			}
			project, _ := s.store.registry.Project("prj-beta")
			if project.Status.Session == nil || project.Status.Session.Name != "beta" || !project.Status.Session.Live {
				t.Fatalf("%s Project session binding = %+v, want beta live", name, project.Status.Session)
			}
			adopted, _ := s.store.registry.Window("win-beta-main")
			if adopted.Status.RuntimeSessionID != session.id || adopted.Status.RuntimeID != session.windows[0].id {
				t.Fatalf("%s adopted Window binding = %s/%s, want %s/%s", name,
					adopted.Status.RuntimeSessionID, adopted.Status.RuntimeID, session.id, session.windows[0].id)
			}
		}
	}

	t.Run("shell answer", func(t *testing.T) {
		a := fresh(t)
		b := intent(t, agentPaneIntent{})
		assertSameStart(t, a, b)
		if a.store.snapshot() != b.store.snapshot() {
			t.Fatalf("Registry differs\nfresh=%s\nintent=%s", a.store.snapshot(), b.store.snapshot())
		}
		if a.tmux.state() != b.tmux.state() {
			t.Fatalf("runtime differs\nfresh=%s\nintent=%s", a.tmux.state(), b.tmux.state())
		}
	})

	t.Run("Agent answer", func(t *testing.T) {
		assertSameStart(t, fresh(t, "--provider", aiModeClaude),
			intent(t, agentPaneIntent{producer: canonicalProducerSavedDefault, provider: aiModeClaude, placement: "right"}))
	})
}

// TestProjectWindowIntentAgentFailureRollsBackTheWholeOperation is A4: an
// Agent that cannot be opened after the Window -- and, for a stopped Project,
// the session -- already exists rolls every one of them back. The Registry is
// byte-identical, no tmux object this operation made is left, and the error
// says nothing was created.
func TestProjectWindowIntentAgentFailureRollsBackTheWholeOperation(t *testing.T) {
	const codexThread = "019f0000-0000-7000-8000-000000000077"
	endpoint := nativeTestRoute("generation-project-window", coremetadata.CodexGenerationCurrent)
	for _, tt := range []struct {
		name   string
		answer agentPaneIntent
		inject func(*projectWindowIntentFixture)
		reason string
	}{
		{
			name: "resumed session refused", answer: resumeAnswer(aiModeClaude, "claude-picker-session"),
			inject: func(fx *projectWindowIntentFixture) {
				fx.create.resumes.(*fakeResumeLauncher).planErr = errors.New("provider refused exact picker conversation")
			},
			reason: "provider refused exact picker conversation",
		},
		{
			name: "Codex app-server resume refused",
			answer: agentPaneIntent{producer: canonicalProducerResumePicker, provider: aiModeCodex, placement: "right",
				conversationID: codexThread, resumeSource: aisessions.SourceCodexAppServer,
				resumeEndpoint: endpoint.Endpoint, resumeGenerationState: coremetadata.CodexGenerationCurrent},
			inject: func(fx *projectWindowIntentFixture) {
				fx.create.codexNative = &fakeNativeThreadController{resolvedRoute: endpoint,
					resumeBinding: codexappserver.ThreadBinding{ThreadID: codexThread},
					resumeErr:     errors.New("injected native resume refusal")}
				fx.create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: &fakeNativePaneLauncher{}}
			},
			reason: "injected native resume refusal",
		},
		{
			name: "Agent Pane split fails", answer: resumeAnswer(aiModeClaude, "claude-picker-session"),
			inject: func(fx *projectWindowIntentFixture) {
				fx.tmux.fail = []string{"split-window"}
				fx.tmux.failMessage = "injected split failure"
			},
			reason: "injected split failure",
		},
		{
			name: "Registry commit fails after the runtime exists", answer: resumeAnswer(aiModeClaude, "claude-picker-session"),
			inject: func(fx *projectWindowIntentFixture) {
				fx.create.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
					fx.store.transactions++
					working := fx.store.registry.Clone()
					if err := fn(&working); err != nil {
						return coremetadata.Registry{}, err
					}
					return coremetadata.Registry{}, errors.New("injected Registry commit failure")
				}
			},
			reason: "injected Registry commit failure",
		},
	} {
		for _, live := range []bool{false, true} {
			projectUID, state := "prj-beta", "stopped"
			if live {
				projectUID, state = "prj-alpha", "live"
			}
			t.Run(state+"/"+tt.name, func(t *testing.T) {
				fx := newProjectWindowIntentFixture(t, live)
				tt.inject(fx)
				registryBefore, runtimeBefore := fx.store.snapshot(), fx.tmux.state()

				_, err := fx.create.createWindowFromIntent(windowCreateIntent{projectUID: projectUID, answer: tt.answer}, ioDiscard{}, ioDiscard{})
				if err == nil {
					t.Fatal("injected Agent failure reported success")
				}
				if text := err.Error(); !strings.Contains(text, tt.reason) || !strings.Contains(text, "nothing was created") || strings.Contains(text, "\n") {
					t.Fatalf("error = %q, want one line naming %q and that nothing was created", text, tt.reason)
				}
				if fx.store.transactions != 1 || fx.store.writes != 0 || fx.store.snapshot() != registryBefore {
					t.Fatalf("Registry transactions=%d writes=%d changed=%t, want one rolled-back transaction",
						fx.store.transactions, fx.store.writes, fx.store.snapshot() != registryBefore)
				}
				if got := fx.tmux.state(); got != runtimeBefore {
					t.Fatalf("runtime objects of this operation survived\nbefore=%s\nafter=%s", runtimeBefore, got)
				}
				// The operation really made the runtime it removed: the Window
				// always, and on the stopped Project the session too.
				if len(tmuxCallsNamed(fx.tmux, "new-window")) != 1 {
					t.Fatalf("new-window calls = %d, want the one Window this operation made", len(tmuxCallsNamed(fx.tmux, "new-window")))
				}
				if !live && (len(tmuxCallsNamed(fx.tmux, "new-session")) != 1 || fx.tmux.session("beta") != nil) {
					t.Fatalf("stopped Project: new-session calls %d, beta left live %t, want one started and removed",
						len(tmuxCallsNamed(fx.tmux, "new-session")), fx.tmux.session("beta") != nil)
				}
			})
		}
	}
}

// TestProjectWindowIntentRefusalsCreateNothing is A5: a scope that is not
// exactly one Registry Project -- two scopes, none, an unknown or spelled
// selector UID, a Project whose root is gone, a ControlSession -- is refused in
// one line before any transaction, Registry write, or tmux mutation.
func TestProjectWindowIntentRefusalsCreateNothing(t *testing.T) {
	for _, tt := range []struct {
		name   string
		intent func(controlUID string) windowCreateIntent
		reason string
	}{
		{"anchor and Project together", func(string) windowCreateIntent {
			return windowCreateIntent{anchorPaneID: "%1", projectUID: "prj-beta"}
		}, "exactly one scope"},
		{"neither scope", func(string) windowCreateIntent { return windowCreateIntent{} }, "exact Project UID"},
		{"unknown Project UID", func(string) windowCreateIntent { return windowCreateIntent{projectUID: "prj-missing"} }, `"prj-missing"`},
		{"selector-spelled UID", func(string) windowCreateIntent { return windowCreateIntent{projectUID: "uid:prj-beta"} }, `"uid:prj-beta"`},
		{"Project name", func(string) windowCreateIntent { return windowCreateIntent{projectUID: "beta"} }, `"beta"`},
		{"MissingRoot Project", func(string) windowCreateIntent { return windowCreateIntent{projectUID: "prj-gone"} }, "MissingRoot"},
		{"ControlSession UID", func(uid string) windowCreateIntent { return windowCreateIntent{projectUID: uid} }, "ControlSession"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fx := newProjectWindowIntentFixture(t, false)
			control := seedControlOwnedGraph(t, fx.store, fx.tmux, "home")
			fx.tmux.calls = nil
			registryBefore, runtimeBefore := fx.store.snapshot(), fx.tmux.state()
			intent := tt.intent(control.binding.ControlSession.Metadata.UID)
			intent.answer = resumeAnswer(aiModeClaude, "claude-picker-session")

			_, err := fx.create.createWindowFromIntent(intent, ioDiscard{}, ioDiscard{})
			if err == nil {
				t.Fatalf("intent %+v was not refused", intent)
			}
			if text := err.Error(); !strings.Contains(text, tt.reason) || !strings.Contains(text, "nothing was created") || strings.Contains(text, "\n") {
				t.Fatalf("refusal = %q, want one line naming %q and that nothing was created", text, tt.reason)
			}
			if fx.store.transactions != 0 || fx.store.writes != 0 || fx.store.snapshot() != registryBefore {
				t.Fatalf("refusal touched the Registry: transactions=%d writes=%d", fx.store.transactions, fx.store.writes)
			}
			if writes := tmuxMutationCallCount(fx.tmux); writes != 0 || fx.tmux.state() != runtimeBefore {
				t.Fatalf("refusal touched tmux: mutations=%d calls=%q", writes, fx.tmux.calls)
			}
			if plans := fx.create.resumes.(*fakeResumeLauncher).plans; len(plans) != 0 || len(fx.sessions.created) != 0 {
				t.Fatalf("refusal planned %+v and started sessions %v", plans, fx.sessions.created)
			}
		})
	}
}
