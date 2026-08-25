package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type canonicalProducerCase struct {
	producer canonicalCreateProducer
	intent   agentPaneIntent
	wantKind coremetadata.Kind
}

var canonicalProducerCases = []canonicalProducerCase{
	{canonicalProducerPaneMenu, agentPaneIntent{placement: "right"}, coremetadata.KindPane},
	{canonicalProducerSavedDefault, agentPaneIntent{provider: aiModeCodex, placement: "down"}, coremetadata.KindAgent},
	{canonicalProducerProviderPicker, agentPaneIntent{provider: aiModeClaude, placement: "right"}, coremetadata.KindAgent},
	{canonicalProducerResumePicker, agentPaneIntent{provider: aiModeCodex, placement: "down", conversationID: "thread-phase12"}, coremetadata.KindAgent},
	{canonicalProducerDirectProvider, agentPaneIntent{provider: aiModeClaude, placement: "right"}, coremetadata.KindAgent},
	{canonicalProducerDirectShell, agentPaneIntent{placement: "down"}, coremetadata.KindPane},
}

type canonicalRootFixture struct {
	store     *fakeResourceStore
	tmux      *fakeTmux
	create    *createCommand
	originID  string
	windowUID string
	rootKind  coremetadata.Kind
	rootUID   string
}

type routeBindingAssertionRunner struct {
	t     *testing.T
	bound *bool
	inner tmuxCommandRunner
}

type canonicalRenameRecycleRunner struct {
	inner         *fakeTmux
	anchorPaneID  string
	identityReads int
}

func (r *canonicalRenameRecycleRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	argv := tmuxCommandArgv(args)
	if len(argv) > 0 && argv[0] == "display-message" {
		format := flagValue(argv, "-F")
		if strings.Contains(format, tmuxopts.WindowUID) && strings.Contains(format, tmuxopts.ProjectUIDSession) &&
			strings.Contains(format, tmuxopts.SessionRole) {
			r.identityReads++
			if r.identityReads == 2 {
				session, _, _ := r.inner.pane(r.anchorPaneID)
				session.opts[tmuxopts.ProjectUIDSession] = "prj-recycled-foreign"
			}
		}
	}
	return r.inner.Run(ctx, name, args...)
}

func (r routeBindingAssertionRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.t.Helper()
	if !*r.bound {
		r.t.Fatal("canonical Window producer used its materializer before binding the exact invocation route")
	}
	return r.inner.Run(ctx, name, args...)
}

func canonicalFixture(t *testing.T, control bool) canonicalRootFixture {
	t.Helper()
	var store *fakeResourceStore
	var tmux *fakeTmux
	var windowUID, rootUID string
	rootKind := coremetadata.KindProject
	if control {
		store = newFakeResourceStore(t)
		tmux = newFakeTmux()
		seed := seedControlOwnedGraph(t, store, tmux, "home")
		windowUID = seed.window.Metadata.UID
		rootUID = seed.binding.ControlSession.Metadata.UID
		rootKind = coremetadata.KindControlSession
	} else {
		store, tmux = aliveAlphaRuntime(t)
		windowUID = "win-alpha-main"
		rootUID = "prj-alpha"
	}
	create, _ := newTestAgentCreateCommand(t, store, tmux)
	create.resumes = newFakeResumeLauncher()
	originUID := "pan-alpha-zsh"
	if control {
		originUID = store.registry.WindowsOf(rootUID)[0].Spec.AnchorPaneRef
	}
	originID := livePaneWithUID(t, tmux, originUID)
	withPopupOrigin(create, tmux, popupEnv(originID))
	return canonicalRootFixture{store: store, tmux: tmux, create: create, originID: originID,
		windowUID: windowUID, rootKind: rootKind, rootUID: rootUID}
}

// TestCanonicalCreateProducerRootOutcomeTable is the closed producer x root x
// outcome contract. Every row is executable and every producer appears exactly
// once in the declared producer inventory, so there is no unclassified cell.
func TestCanonicalCreateProducerRootOutcomeTable(t *testing.T) {
	seen := map[canonicalCreateProducer]int{}
	for _, row := range canonicalProducerCases {
		seen[row.producer]++
		for _, control := range []bool{false, true} {
			rootName := "Project"
			if control {
				rootName = "ControlSession"
			}
			t.Run(string(row.producer)+"/"+rootName+"/created", func(t *testing.T) {
				fx := canonicalFixture(t, control)
				before := paneUIDsByWindow(fx.store)
				intent := row.intent
				intent.producer = row.producer
				intent.anchorPaneID = fx.originID
				var stdout, stderr bytes.Buffer
				if err := fx.create.createFromIntent(intent, &stdout, &stderr); err != nil {
					t.Fatalf("createFromIntent(%+v) error = %v (stderr=%q)", intent, err, stderr.String())
				}
				added := addedPaneUIDs(before, paneUIDsByWindow(fx.store))[fx.windowUID]
				if len(added) != 1 {
					t.Fatalf("origin Window gained %v, want exactly one managed Pane\n%s", added, fx.store.snapshot())
				}
				pane, ok := fx.store.registry.Pane(added[0])
				if !ok || livePaneWithUID(t, fx.tmux, added[0]) == "" {
					t.Fatalf("created Pane %q has no Registry/live mirror", added[0])
				}
				window, _ := fx.store.registry.Window(fx.windowUID)
				if window.Metadata.OwnerRef == nil || window.Metadata.OwnerRef.Kind != fx.rootKind || window.Metadata.OwnerRef.UID != fx.rootUID {
					t.Fatalf("Window owner = %+v, want %s/%s", window.Metadata.OwnerRef, fx.rootKind, fx.rootUID)
				}
				if row.wantKind == coremetadata.KindAgent {
					if pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent {
						t.Fatalf("Agent producer Pane owner = %+v, want Agent", pane.Metadata.OwnerRef)
					}
					agent, ok := fx.store.registry.Agent(pane.Metadata.OwnerRef.UID)
					if !ok || agent.Metadata.OwnerUID() != fx.windowUID {
						t.Fatalf("Agent owner chain does not end at origin Window: %+v", agent)
					}
				} else if pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindWindow || pane.Metadata.OwnerRef.UID != fx.windowUID {
					t.Fatalf("shell Pane owner = %+v, want origin Window %s", pane.Metadata.OwnerRef, fx.windowUID)
				}
				assertCanonicalCreateLeaseBracketsSplit(t, fx.tmux.calls)
			})
		}
	}
	if len(seen) != len(canonicalCreateProducers) {
		t.Fatalf("classified producers = %v, inventory = %v", seen, canonicalCreateProducers)
	}
	for _, producer := range canonicalCreateProducers {
		if seen[producer] != 1 {
			t.Errorf("producer %q classified %d times, want exactly 1", producer, seen[producer])
		}
	}
}

func assertCanonicalCreateLeaseBracketsSplit(t *testing.T, calls [][]string) {
	t.Helper()
	leaseSet, split, leaseClear := -1, -1, -1
	leaseTarget := ""
	for i, call := range calls {
		argv := tmuxCommandArgv(call)
		if len(argv) == 0 {
			continue
		}
		switch argv[0] {
		case "split-window":
			split = i
		case "set-environment":
			if argv[len(argv)-1] == createOperationEnvironment && slices.Contains(argv, "-u") {
				leaseClear = i
			} else if len(argv) >= 2 && argv[len(argv)-2] == createOperationEnvironment {
				leaseSet = i
				leaseTarget = flagValue(argv, "-t")
			}
		}
	}
	if leaseSet < 0 || split <= leaseSet || leaseClear <= split {
		t.Fatalf("canonical create lease order set=%d split=%d clear=%d; calls=%v", leaseSet, split, leaseClear, calls)
	}
	if exactTmuxHandle(leaseTarget, "$") == "" {
		t.Fatalf("canonical create lease target = %q, want stable runtime session id; calls=%v", leaseTarget, calls)
	}
}

func TestCanonicalProjectIntentDerivesLiveSessionWhenStatusSessionIsNil(t *testing.T) {
	fx := canonicalFixture(t, false)
	project, _ := fx.store.registry.Project(fx.rootUID)
	project.Status.Session = nil
	for i := range fx.store.registry.Projects {
		if fx.store.registry.Projects[i].Metadata.UID == project.Metadata.UID {
			fx.store.registry.Projects[i] = *project
		}
	}
	if err := fx.create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerDirectShell, placement: "right", anchorPaneID: fx.originID,
	}, ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatalf("canonical Project create with nil status.session: %v", err)
	}
	assertCanonicalCreateLeaseBracketsSplit(t, fx.tmux.calls)
}

func TestCanonicalWindowCreateBindsInvocationRouteBeforeRuntimeMutationObservation(t *testing.T) {
	fx := canonicalFixture(t, false)
	bound := false
	bindCalls := 0
	fx.create.runtimeBound = false
	fx.create.runtime.runner = routeBindingAssertionRunner{t: t, bound: &bound, inner: fx.tmux}
	fx.create.bindRuntime = func(context.Context) error {
		bindCalls++
		bound = true
		return nil
	}
	if err := fx.create.createWindowFromIntent(windowCreateIntent{anchorPaneID: fx.originID}, ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatalf("canonical Window create: %v", err)
	}
	if bindCalls != 1 {
		t.Fatalf("runtime route bind calls = %d, want exactly one", bindCalls)
	}
}

func TestCanonicalWindowCreateCommitsExactOwnerPairAndInitialActivationForManagedRoots(t *testing.T) {
	for _, control := range []bool{false, true} {
		rootName := "Project"
		if control {
			rootName = "ControlSession"
		}
		t.Run(rootName, func(t *testing.T) {
			fx := canonicalFixture(t, control)
			before := map[string]bool{}
			for _, window := range fx.store.registry.Windows {
				before[window.Metadata.UID] = true
			}
			bindingCalls := 0
			fx.create.bindWindowRuntime = func(mutator coremetadata.Mutator, registry *coremetadata.Registry,
				windowUID, sessionID, windowID string,
			) (coremetadata.Window, error) {
				bindingCalls++
				return mutator.ObserveWindowRuntimeBinding(registry, windowUID, sessionID, windowID)
			}

			if err := fx.create.createWindowFromIntent(windowCreateIntent{anchorPaneID: fx.originID}, ioDiscard{}, ioDiscard{}); err != nil {
				t.Fatalf("canonical %s Window create: %v", rootName, err)
			}
			if bindingCalls != 1 {
				t.Fatalf("producer owner-pair binding calls = %d, want exactly 1", bindingCalls)
			}

			var created coremetadata.Window
			for _, window := range fx.store.registry.Windows {
				if !before[window.Metadata.UID] {
					created = window
				}
			}
			if created.Metadata.UID == "" || created.Metadata.OwnerRef == nil ||
				created.Metadata.OwnerRef.Kind != fx.rootKind || created.Metadata.OwnerRef.UID != fx.rootUID {
				t.Fatalf("created Window owner = %+v, want %s/%s", created.Metadata.OwnerRef, fx.rootKind, fx.rootUID)
			}
			session, liveWindow := fx.tmux.window(created.Status.RuntimeID)
			if session == nil || liveWindow == nil || created.Status.RuntimeSessionID != session.id ||
				exactTmuxHandle(created.Status.RuntimeSessionID, "$") == "" || exactTmuxHandle(created.Status.RuntimeID, "@") == "" ||
				liveWindow.opts[tmuxopts.WindowUID] != created.Metadata.UID {
				t.Fatalf("created Window binding = %q/%q live=%+v/%+v", created.Status.RuntimeSessionID, created.Status.RuntimeID, session, liveWindow)
			}
			panes := fx.store.registry.PanesOf(created.Metadata.UID)
			if len(panes) != 1 || panes[0].Status.Activation.Generation == "" ||
				exactTmuxHandle(panes[0].Status.Activation.RuntimeID, "%") == "" {
				t.Fatalf("initial Pane activation = %+v, want one exact generated %%N activation", panes)
			}
			if got := livePaneWithUID(t, fx.tmux, panes[0].Metadata.UID); got != panes[0].Status.Activation.RuntimeID {
				t.Fatalf("initial Pane runtime = %q, activation = %q", got, panes[0].Status.Activation.RuntimeID)
			}
		})
	}
}

func TestCanonicalWindowOwnerPairBindingFailureRollsBackRegistryAndRuntime(t *testing.T) {
	for _, control := range []bool{false, true} {
		rootName := "Project"
		if control {
			rootName = "ControlSession"
		}
		t.Run(rootName, func(t *testing.T) {
			fx := canonicalFixture(t, control)
			registryBefore, runtimeBefore := fx.store.snapshot(), fx.tmux.state()
			bindingCalls := 0
			fx.create.bindWindowRuntime = func(_ coremetadata.Mutator, _ *coremetadata.Registry,
				windowUID, sessionID, windowID string,
			) (coremetadata.Window, error) {
				bindingCalls++
				if windowUID == "" || exactTmuxHandle(sessionID, "$") == "" || exactTmuxHandle(windowID, "@") == "" {
					t.Fatalf("binding failure seam received incomplete identity %q/%q/%q", windowUID, sessionID, windowID)
				}
				return coremetadata.Window{}, errors.New("injected canonical owner-pair binding failure")
			}

			var stdout bytes.Buffer
			err := fx.create.createWindowFromIntent(windowCreateIntent{anchorPaneID: fx.originID}, &stdout, ioDiscard{})
			if err == nil || !strings.Contains(err.Error(), "injected canonical owner-pair binding failure") {
				t.Fatalf("binding failure = %v", err)
			}
			if bindingCalls != 1 || stdout.Len() != 0 {
				t.Fatalf("binding calls=%d stdout=%q, want 1/empty", bindingCalls, stdout.String())
			}
			if fx.store.writes != 0 || fx.store.snapshot() != registryBefore {
				t.Fatalf("binding failure committed a Registry claimant: writes=%d", fx.store.writes)
			}
			if got := fx.tmux.state(); got != runtimeBefore {
				t.Fatalf("binding failure retained a runtime claimant:\n--- got ---\n%s\n--- want ---\n%s", got, runtimeBefore)
			}
		})
	}
}

func TestCanonicalWindowCreateFeedsExactLastPaneCausalCleanupForManagedRoots(t *testing.T) {
	for _, control := range []bool{false, true} {
		rootName := "Project"
		if control {
			rootName = "ControlSession"
		}
		t.Run(rootName, func(t *testing.T) {
			fx := canonicalFixture(t, control)
			before := map[string]bool{}
			for _, window := range fx.store.registry.Windows {
				before[window.Metadata.UID] = true
			}
			if err := fx.create.createWindowFromIntent(windowCreateIntent{anchorPaneID: fx.originID}, ioDiscard{}, ioDiscard{}); err != nil {
				t.Fatalf("canonical %s Window create: %v", rootName, err)
			}

			var target coremetadata.Window
			for _, window := range fx.store.registry.Windows {
				if !before[window.Metadata.UID] {
					target = window
				}
			}
			panes := fx.store.registry.PanesOf(target.Metadata.UID)
			if target.Metadata.UID == "" || len(panes) != 1 {
				t.Fatalf("canonical target = %+v panes=%+v", target, panes)
			}
			pane := panes[0]
			receipt := phase2NormalReceipt(pane.Metadata.UID, "", pane.Status.Activation.Generation)
			livePanes, deadPanes := map[string]bool{}, map[string]bool{pane.Metadata.UID: true}
			for _, candidate := range fx.store.registry.Panes {
				if candidate.Metadata.UID != pane.Metadata.UID {
					livePanes[candidate.Metadata.UID] = true
				}
			}
			liveWindows := map[string]bool{}
			windowSessions := map[string]int{}
			for _, session := range fx.tmux.sessions {
				for _, window := range session.windows {
					if uid := window.opts[tmuxopts.WindowUID]; uid != "" {
						liveWindows[uid] = true
						windowSessions[session.id]++
					}
				}
			}
			inventory := &exactPaneExitInventory{
				uids: livePanes, dead: deadPanes, windowUID: target.Metadata.UID,
				windows: liveWindows, windowSessions: windowSessions,
			}
			event := lifecycleDirtyEvent{
				target:        explicitTmuxTarget{flag: "-S", value: fx.tmux.socketPath},
				runtimePaneID: pane.Status.Activation.RuntimeID,
				teardownKind:  coremetadata.TeardownEventPaneExited,
				receipts:      []coremetadata.TerminationEvidence{receipt},
			}
			pending, err := reconcileLifecycle(context.Background(), event, inventory, fx.store.store())
			if err != nil || len(pending.pending) != 1 || inventory.cleanups != 1 {
				t.Fatalf("canonical pane-exited result=%+v cleanups=%d err=%v", pending, inventory.cleanups, err)
			}

			unlinked := event
			unlinked.teardownKind = coremetadata.TeardownEventWindowUnlinked
			unlinked.runtimePaneID = ""
			unlinked.runtimeSessionID = target.Status.RuntimeSessionID
			unlinked.runtimeWindowID = target.Status.RuntimeID
			closed, err := reconcileLifecycle(context.Background(), unlinked, inventory, fx.store.store())
			if err != nil || len(closed.rootCascaded) != 1 || closed.rootCascaded[0].WindowUID != target.Metadata.UID {
				t.Fatalf("canonical window-unlinked result=%+v err=%v", closed, err)
			}
			if _, ok := fx.store.registry.Window(target.Metadata.UID); ok {
				t.Fatalf("canonical target Window %s survived", target.Metadata.UID)
			}
			if _, ok := fx.store.registry.Pane(pane.Metadata.UID); ok {
				t.Fatalf("canonical target Pane %s survived", pane.Metadata.UID)
			}
			if _, ok := fx.store.registry.Window(fx.windowUID); !ok {
				t.Fatalf("sibling Window %s disappeared", fx.windowUID)
			}
			switch fx.rootKind {
			case coremetadata.KindProject:
				if _, ok := fx.store.registry.Project(fx.rootUID); !ok {
					t.Fatalf("owning Project %s disappeared", fx.rootUID)
				}
			case coremetadata.KindControlSession:
				if _, ok := fx.store.registry.ControlSession(fx.rootUID); !ok {
					t.Fatalf("owning ControlSession %s disappeared", fx.rootUID)
				}
			}
		})
	}
}

func TestCanonicalWindowCreateLifecycleAuthorityMatrixIsClosed(t *testing.T) {
	for _, control := range []bool{false, true} {
		rootName := "Project"
		if control {
			rootName = "ControlSession"
		}
		t.Run(rootName, func(t *testing.T) {
			fx := canonicalFixture(t, control)
			before := map[string]bool{}
			for _, window := range fx.store.registry.Windows {
				before[window.Metadata.UID] = true
			}
			if err := fx.create.createWindowFromIntent(windowCreateIntent{anchorPaneID: fx.originID}, ioDiscard{}, ioDiscard{}); err != nil {
				t.Fatal(err)
			}
			var window coremetadata.Window
			for _, candidate := range fx.store.registry.Windows {
				if !before[candidate.Metadata.UID] {
					window = candidate
				}
			}
			panes := fx.store.registry.PanesOf(window.Metadata.UID)
			if window.Metadata.UID == "" || len(panes) != 1 {
				t.Fatalf("canonical lifecycle fixture Window=%+v Panes=%+v", window, panes)
			}
			pane := panes[0]
			baseEvent := coremetadata.TeardownEvent{
				Kind: coremetadata.TeardownEventPaneExited, Classification: coremetadata.TerminationNormal,
				Generation: coremetadata.TeardownGenerationCurrent, Observation: coremetadata.TeardownObservationExactSocket,
				Chain: coremetadata.TeardownOwnerChain{
					SocketIdentity: fx.tmux.socketPath, SessionHandle: window.Status.RuntimeSessionID,
					WindowHandle: window.Status.RuntimeID, PaneHandle: pane.Status.Activation.RuntimeID,
					PaneUID: pane.Metadata.UID, WindowUID: window.Metadata.UID,
					RootKind: fx.rootKind, RootUID: fx.rootUID, Generation: pane.Status.Activation.Generation,
				},
				LiveSiblingRootWindow: true,
			}
			for _, test := range []struct {
				name       string
				prepare    func(*coremetadata.Registry, *coremetadata.TeardownEvent, *coremetadata.TeardownEvent)
				wantDelete bool
			}{
				{name: "normal exact causal pair", wantDelete: true},
				{name: "missing binding", prepare: func(registry *coremetadata.Registry, _, _ *coremetadata.TeardownEvent) {
					stored, _ := registry.Window(window.Metadata.UID)
					stored.Status.RuntimeSessionID, stored.Status.RuntimeID = "", ""
				}},
				{name: "abnormal", prepare: func(_ *coremetadata.Registry, paneEvent, unlinkEvent *coremetadata.TeardownEvent) {
					paneEvent.Classification, unlinkEvent.Classification = coremetadata.TerminationAbnormal, coremetadata.TerminationAbnormal
				}},
				{name: "killed", prepare: func(_ *coremetadata.Registry, paneEvent, unlinkEvent *coremetadata.TeardownEvent) {
					paneEvent.Classification, unlinkEvent.Classification = coremetadata.TerminationKilled, coremetadata.TerminationKilled
				}},
				{name: "unknown", prepare: func(_ *coremetadata.Registry, paneEvent, unlinkEvent *coremetadata.TeardownEvent) {
					paneEvent.Classification, unlinkEvent.Classification = coremetadata.TerminationUnknown, coremetadata.TerminationUnknown
				}},
				{name: "stale generation", prepare: func(_ *coremetadata.Registry, paneEvent, unlinkEvent *coremetadata.TeardownEvent) {
					paneEvent.Chain.Generation, unlinkEvent.Chain.Generation = "gen-stale", "gen-stale"
				}},
				{name: "foreign socket", prepare: func(_ *coremetadata.Registry, _ *coremetadata.TeardownEvent, unlinkEvent *coremetadata.TeardownEvent) {
					unlinkEvent.Chain.SocketIdentity = "/tmp/foreign.sock"
				}},
				{name: "mismatched owner pair", prepare: func(_ *coremetadata.Registry, paneEvent, unlinkEvent *coremetadata.TeardownEvent) {
					paneEvent.Chain.WindowHandle, unlinkEvent.Chain.WindowHandle = "@999", "@999"
				}},
			} {
				t.Run(test.name, func(t *testing.T) {
					registry := fx.store.registry.Clone()
					paneEvent, unlinkEvent := baseEvent, baseEvent
					unlinkEvent.Kind = coremetadata.TeardownEventWindowUnlinked
					if test.prepare != nil {
						test.prepare(&registry, &paneEvent, &unlinkEvent)
					}
					receipt := phase2NormalReceipt(pane.Metadata.UID, "", pane.Status.Activation.Generation)
					receipt.Classification = paneEvent.Classification
					switch receipt.Classification {
					case coremetadata.TerminationAbnormal:
						code := 42
						receipt.ExitCode = &code
					case coremetadata.TerminationKilled:
						receipt.ExitCode, receipt.Signal = nil, "HUP"
					case coremetadata.TerminationUnknown:
						receipt.ExitCode, receipt.Source = nil, coremetadata.TerminationSourceReconcile
					}
					if _, err := fx.store.mutator().RecordTermination(&registry, receipt); err != nil {
						t.Fatalf("record matrix receipt: %v", err)
					}
					pending, err := coremetadata.PlanPaneTeardownEvidence(registry, paneEvent, resourceFixtureClock.Add(time.Minute))
					if err != nil {
						t.Fatalf("plan pane evidence: %v", err)
					}
					candidate := registry
					if pending.Changed {
						candidate = pending.Desired
					}
					plan, err := coremetadata.PlanWindowRootCascadeDelete(candidate, paneEvent, unlinkEvent, resourceFixtureClock.Add(2*time.Minute))
					if err != nil {
						t.Fatalf("plan Window cascade: %v", err)
					}
					if plan.Changed != test.wantDelete {
						t.Fatalf("delete changed=%t decision=%+v, want changed=%t", plan.Changed, plan.Decision, test.wantDelete)
					}
					if !test.wantDelete {
						if _, ok := candidate.Window(window.Metadata.UID); !ok {
							t.Fatal("negative authority removed the canonical Window")
						}
					}
				})
			}
		})
	}
}

func TestCanonicalWindowRenameAlreadyMatchingRefusesRecycledParentBeforeWrite(t *testing.T) {
	fx := canonicalFixture(t, false)
	_, window, _ := fx.tmux.pane(fx.originID)
	window.name = "renamed"
	runner := &canonicalRenameRecycleRunner{inner: fx.tmux, anchorPaneID: fx.originID}
	fx.create.runtime.runner = runner
	fx.create.runtime.expectedSocketPath = fx.tmux.socketPath
	fx.create.runtime.socketName = defaultAppSocket
	fx.create.runtime.routeAuthority = &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"}

	err := fx.create.renameWindowFromIntent(windowRenameIntent{
		anchorPaneID: fx.originID,
		displayName:  "renamed",
	}, ioDiscard{}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "identity parent drifted") {
		t.Fatalf("canonical rename recycled parent error = %v", err)
	}
	for _, call := range fx.tmux.calls {
		argv := tmuxCommandArgv(call)
		if len(argv) > 0 && argv[0] == "rename-window" {
			t.Fatalf("canonical rename reached runtime write after parent recycle: %#v", fx.tmux.calls)
		}
	}
}

func TestCanonicalCreateRefusesSameNameRuntimeSessionReplacementBeforeLeaseWrite(t *testing.T) {
	fx := canonicalFixture(t, false)
	before := fx.store.snapshot()
	baseUpdate := fx.create.store.update
	fx.create.store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		oldSession := fx.tmux.session("alpha")
		fx.tmux.sessions = slices.DeleteFunc(fx.tmux.sessions, func(candidate *fakeTmuxSession) bool {
			return candidate == oldSession
		})
		replacement := fx.tmux.addSession("alpha")
		seedOwnedSession(replacement, fx.rootUID, "/srv/alpha")
		replacement.windows[0].opts[tmuxopts.WindowUID] = fx.windowUID
		replacement.windows[0].panes[0].id = fx.originID
		replacement.windows[0].panes[0].opts[tmuxopts.PaneUID] = "pan-alpha-zsh"
		return baseUpdate(fn)
	}
	err := fx.create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerDirectShell, placement: "right", anchorPaneID: fx.originID,
	}, ioDiscard{}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "runtime session changed before commit") {
		t.Fatalf("same-name replacement error = %v, want stable-session refusal", err)
	}
	if fx.store.writes != 0 || fx.store.snapshot() != before {
		t.Fatalf("same-name replacement committed Registry state: writes=%d", fx.store.writes)
	}
	for _, call := range fx.tmux.calls {
		if len(call) > 0 && call[0] == "set-environment" && slices.Contains(call, createOperationEnvironment) {
			t.Fatalf("same-name replacement received a create lease: %v", call)
		}
		if len(call) > 0 && slices.Contains([]string{"split-window", "new-window"}, call[0]) {
			t.Fatalf("same-name replacement received a runtime create: %v", call)
		}
	}
}

func TestCanonicalCreateConflictAndLostAnchorHaveZeroWritesAndExactReason(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*canonicalRootFixture)
		want   string
	}{
		{
			name: "lost pane mirror",
			mutate: func(fx *canonicalRootFixture) {
				_, _, pane := fx.tmux.pane(fx.originID)
				delete(pane.opts, tmuxopts.PaneUID)
			},
			want: "exact UI origin was lost",
		},
		{
			name: "conflicting owner chain",
			mutate: func(fx *canonicalRootFixture) {
				_, _, pane := fx.tmux.pane(fx.originID)
				pane.opts[tmuxopts.PaneUID] = "pan-alpha-review"
			},
			want: "identity evidence conflicts",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fx := canonicalFixture(t, false)
			test.mutate(&fx)
			beforeRegistry := fx.store.snapshot()
			beforeCalls := len(fx.tmux.calls)
			err := fx.create.createFromIntent(agentPaneIntent{producer: canonicalProducerProviderPicker,
				provider: aiModeCodex, placement: "right", anchorPaneID: fx.originID}, ioDiscard{}, ioDiscard{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want exact reason containing %q", err, test.want)
			}
			if fx.store.transactions != 0 || fx.store.writes != 0 || fx.store.snapshot() != beforeRegistry {
				t.Fatalf("refusal mutated Registry: transactions=%d writes=%d", fx.store.transactions, fx.store.writes)
			}
			for _, call := range fx.tmux.calls[beforeCalls:] {
				if len(call) > 0 && slices.Contains([]string{"set-option", "set-environment", "rename-window", "new-window", "split-window", "kill-pane", "kill-window", "kill-session"}, call[0]) {
					t.Fatalf("refusal issued tmux write: %v", call)
				}
			}
		})
	}
}

func TestCanonicalCreateLeaseFailureHasZeroRegistryOrRuntimeCreateWrites(t *testing.T) {
	for _, control := range []bool{false, true} {
		rootName := "Project"
		if control {
			rootName = "ControlSession"
		}
		t.Run(rootName, func(t *testing.T) {
			fx := canonicalFixture(t, control)
			fx.tmux.fail = []string{"set-environment", createOperationEnvironment}
			fx.tmux.failMessage = "phase12 create lease refusal"
			before := fx.store.snapshot()
			err := fx.create.createFromIntent(agentPaneIntent{
				producer: canonicalProducerDirectShell, placement: "right", anchorPaneID: fx.originID,
			}, ioDiscard{}, ioDiscard{})
			if err == nil || !strings.Contains(err.Error(), "phase12 create lease refusal") {
				t.Fatalf("error = %v, want exact lease refusal", err)
			}
			if fx.store.writes != 0 || fx.store.snapshot() != before {
				t.Fatalf("lease refusal committed Registry state: writes=%d", fx.store.writes)
			}
			for _, call := range fx.tmux.calls {
				if len(call) > 0 && slices.Contains([]string{"split-window", "new-window"}, call[0]) {
					t.Fatalf("lease refusal issued runtime create: %v", call)
				}
			}
		})
	}
}

// TestHomePopupOriginSplitResumeDefaultAndShellUseCanonicalCreate exercises the
// real AI producer methods, rather than only their intent values. Each starts
// in a popup environment with no TMUX_PANE and must still land below the exact
// control-owned Window mirrored by TMUX_SPLIT_TARGET_PANE.
func TestHomePopupOriginSplitResumeDefaultAndShellUseCanonicalCreate(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*testing.T, *aiCommand, string) error
	}{
		{
			name: "provider picker",
			run: func(_ *testing.T, ai *aiCommand, _ string) error {
				stubAIPickerSelection(ai, aiModeCodex)
				return ai.runAgentPickerSelection("right")
			},
		},
		{
			name: "resume picker",
			run: func(_ *testing.T, ai *aiCommand, _ string) error {
				return ai.createResumedAgentPane(canonicalProducerResumePicker, aiModeCodex, "down", "thread-home")
			},
		},
		{
			name: "saved default",
			run: func(t *testing.T, ai *aiCommand, _ string) error {
				if err := ai.setMode(aiModeClaude); err != nil {
					t.Fatalf("set saved mode: %v", err)
				}
				return ai.runLaunchDefault([]string{"right"}, ioDiscard{})
			},
		},
		{
			name: "shell split",
			run: func(_ *testing.T, ai *aiCommand, _ string) error {
				return ai.runDirectShell([]string{"down"}, ioDiscard{})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fx := canonicalFixture(t, true)
			home := t.TempDir()
			enableAgents(t, home, "codex", "claude")
			ai := testAICommand(home)
			ai.lookupEnv = popupOriginLookupEnv(home, fx.originID)
			ai.panes = fx.create
			before := paneUIDsByWindow(fx.store)
			if err := test.run(t, ai, fx.originID); err != nil {
				t.Fatalf("Home %s error = %v", test.name, err)
			}
			added := addedPaneUIDs(before, paneUIDsByWindow(fx.store))[fx.windowUID]
			if len(added) != 1 {
				t.Fatalf("Home origin Window gained %v, want one managed Pane", added)
			}
			if livePaneWithUID(t, fx.tmux, added[0]) == "" {
				t.Fatalf("Home %s Pane %s has no live managed mirror", test.name, added[0])
			}
		})
	}
}

func TestHomePaneMenuSplitUsesCanonicalCreate(t *testing.T) {
	fx := canonicalFixture(t, true)
	before := paneUIDsByWindow(fx.store)
	command := &tmuxCommand{
		runner: fx.tmux,
		paneMenuCreate: func(intent agentPaneIntent, stdout, stderr io.Writer) error {
			return fx.create.createFromIntent(intent, stdout, stderr)
		},
	}
	if err := command.runPaneMenuAction([]string{"--client", "/dev/pts/home", "split-right", fx.originID}, ioDiscard{}, ioDiscard{}); err != nil {
		t.Fatalf("Home pane menu split error = %v", err)
	}
	added := addedPaneUIDs(before, paneUIDsByWindow(fx.store))[fx.windowUID]
	if len(added) != 1 || livePaneWithUID(t, fx.tmux, added[0]) == "" {
		t.Fatalf("Home pane menu added %v, want one managed live Pane", added)
	}
}

func TestCanonicalProducerNegativeAuditHasZeroHiddenStderr(t *testing.T) {
	fx := canonicalFixture(t, true)
	fx.tmux.fail = []string{"split-window"}
	fx.tmux.failMessage = "phase12 exact split refusal"
	before := fx.store.snapshot()
	err := fx.create.createFromIntent(agentPaneIntent{
		producer: canonicalProducerDirectShell, placement: "right", anchorPaneID: fx.originID,
	}, ioDiscard{}, ioDiscard{})
	if err == nil || !strings.Contains(err.Error(), "phase12 exact split refusal") {
		t.Fatalf("error = %v, want exact materializer reason", err)
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		t.Fatalf("canonical UI error exposes subprocess ExitCode %d; cmd/projmux would hide stderr", coded.ExitCode())
	}
	if fx.store.writes != 0 || fx.store.snapshot() != before {
		t.Fatalf("failed canonical materialization committed Registry state")
	}
}

// ioDiscard is local to keep the refusal test explicit about observing only
// the returned reason; production callers project that same reason to their
// popup stderr or exact pane-menu client.
type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

type diagnosticPaneCreator struct {
	err    error
	detail string
}

func (d diagnosticPaneCreator) createFromIntent(_ agentPaneIntent, _ io.Writer, stderr io.Writer) error {
	_, _ = io.WriteString(stderr, d.detail)
	return d.err
}

func TestPopupCanonicalFailureIsProjectedToExactOriginatingClient(t *testing.T) {
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.lookupEnv = func(key string) string {
		switch key {
		case "HOME":
			return home
		case "TMUX":
			return "/tmp/tmux/projmux,1,0"
		case "TMUX_SPLIT_TARGET_PANE":
			return "%17"
		case canonicalCreateTargetClientEnv:
			return "/dev/pts/exact-origin"
		default:
			return ""
		}
	}
	cmd.panes = diagnosticPaneCreator{err: errors.New("generic create refusal"), detail: "exact owner-chain conflict"}
	cmdRecorder(cmd).commands = nil
	// The refusal reaches the operator as a client message and the process
	// exits zero: a foreground `run-shell` producer that exits non-zero makes
	// tmux paint "returned 1" over the pane the split was launched from.
	if err := cmd.createShellPane(canonicalProducerProviderPicker, "right"); err != nil {
		t.Fatalf("displayed refusal escaped as an exit code: %v", err)
	}
	want := recordedAICommand{name: "tmux", args: []string{
		"display-message", "-c", "/dev/pts/exact-origin", "-d", "10000",
		"projmux create failed: generic create refusal: exact owner-chain conflict",
	}}
	if !slices.ContainsFunc(cmdRecorder(cmd).commands, func(got recordedAICommand) bool {
		return got.name == want.name && slices.Equal(got.args, want.args)
	}) {
		t.Fatalf("commands = %#v, want exact client projection %#v", cmdRecorder(cmd).commands, want)
	}
}

func FuzzCanonicalUIIntentStaysManaged(f *testing.F) {
	f.Add(uint8(0), false)
	f.Add(uint8(1), true)
	f.Add(uint8(5), true)
	f.Fuzz(func(t *testing.T, raw uint8, control bool) {
		row := canonicalProducerCases[int(raw)%len(canonicalProducerCases)]
		fx := canonicalFixture(t, control)
		before := paneUIDsByWindow(fx.store)
		intent := row.intent
		intent.producer = row.producer
		intent.anchorPaneID = fx.originID
		if err := fx.create.createFromIntent(intent, ioDiscard{}, ioDiscard{}); err != nil {
			t.Fatalf("producer=%s control=%t: %v", row.producer, control, err)
		}
		added := addedPaneUIDs(before, paneUIDsByWindow(fx.store))[fx.windowUID]
		if len(added) != 1 {
			t.Fatalf("producer=%s root=%s added=%v", row.producer, fx.rootKind, added)
		}
		if live := livePaneWithUID(t, fx.tmux, added[0]); live == "" {
			t.Fatalf("created Pane %s has no managed mirror", added[0])
		}
		if got := fmt.Sprint(fx.store.registry.WindowsOf(fx.rootUID)[0].Metadata.OwnerRef.Kind); got != string(fx.rootKind) {
			t.Fatalf("root kind = %s, want %s", got, fx.rootKind)
		}
	})
}
