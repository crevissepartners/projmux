package metadata

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func decisionEvent() TeardownEvent {
	return TeardownEvent{
		Kind:           TeardownEventPaneExited,
		Classification: TerminationNormal,
		Generation:     TeardownGenerationCurrent,
		Observation:    TeardownObservationExactSocket,
		Chain: TeardownOwnerChain{
			SocketIdentity: "/tmp/tmux-app/projmux", SessionHandle: "$1", PaneHandle: "%7", WindowHandle: "@4",
			PaneUID: "pane-1", WindowUID: "window-1", RootKind: KindProject,
			RootUID: "project-1", Generation: "gen-1",
		},
	}
}

func assertDecisionFilled(t *testing.T, decision TeardownDecision) {
	t.Helper()
	if decision.Action == "" || decision.RootAction == "" || decision.Reason == "" ||
		decision.ReopenIdentity == "" || decision.ExternalAssets.RootDirectory == "" ||
		decision.ExternalAssets.GitMetadata == "" || decision.ExternalAssets.Worktrees == "" ||
		decision.ExternalAssets.SnapshotBytes == "" {
		t.Fatalf("decision has an empty cell: %+v", decision)
	}
}

func TestTeardownDecisionTableIsTotal(t *testing.T) {
	t.Parallel()

	events := append(TeardownEventKinds(), TeardownEventKind("invalid-event"))
	classifications := []TerminationClassification{
		TerminationIntentional, TerminationInterrupted, TerminationNormal, TerminationKilled,
		TerminationAbnormal, TerminationUnknown, "invalid-classification",
	}
	observations := append(TeardownObservations(), TeardownObservation("invalid-observation"))
	generations := []TeardownGeneration{TeardownGenerationCurrent, TeardownGenerationStale, "invalid-generation"}
	roots := []Kind{KindProject, KindControlSession, Kind("invalid-root")}
	for _, eventKind := range events {
		for _, classification := range classifications {
			for _, generation := range generations {
				for _, observation := range observations {
					for _, siblingPane := range []bool{false, true} {
						for _, siblingWindow := range []bool{false, true} {
							for _, root := range roots {
								input := decisionEvent()
								input.Kind = eventKind
								input.Classification = classification
								input.Generation = generation
								input.Observation = observation
								input.LiveSiblingPane = siblingPane
								input.LiveSiblingRootWindow = siblingWindow
								input.Chain.RootKind = root
								decision := DecideTeardownEvent(input)
								assertDecisionFilled(t, decision)

								validClass := ValidTerminationClassification(classification)
								if eventKind == "invalid-event" || generation == "invalid-generation" ||
									observation == "invalid-observation" || root == "invalid-root" || !validClass {
									if decision.Action != TeardownRefuse {
										t.Fatalf("invalid input produced %+v for event=%q class=%q generation=%q observation=%q root=%q",
											decision, eventKind, classification, generation, observation, root)
									}
									continue
								}
								if generation == TeardownGenerationStale || observation != TeardownObservationExactSocket {
									if decision.Action != TeardownRefuse {
										t.Fatalf("non-authoritative input produced delete/retain action: %+v", decision)
									}
									continue
								}
								qualifying := classification == TerminationNormal || classification == TerminationIntentional
								if !qualifying && decision.Action != TeardownRetain {
									t.Fatalf("classification %q = %+v, want retain", classification, decision)
								}
								if qualifying && eventKind == TeardownEventPaneExited && decision.Action != TeardownDeletePaneAgent {
									t.Fatalf("qualifying pane event = %+v, want pane/agent delete", decision)
								}
								if qualifying && eventKind == TeardownEventWindowUnlinked && decision.Action != TeardownRetain {
									t.Fatalf("unpaired window event = %+v, want retain", decision)
								}
							}
						}
					}
				}
			}
		}
	}
}

func TestCleanShellAndProviderExitHaveTheSameNormalOutcome(t *testing.T) {
	t.Parallel()

	cleanShell := decisionEvent()
	providerExit := decisionEvent()
	// The two inputs are intentionally indistinguishable. No command, content,
	// prompt, history, or transcript field exists for a caller to fill.
	if got, want := DecideTeardownEvent(cleanShell), DecideTeardownEvent(providerExit); got != want {
		t.Fatalf("clean shell = %+v, provider /exit = %+v", got, want)
	}
}

func TestTeardownOwnerChainRequiresExactSocketAndRuntimeHandles(t *testing.T) {
	t.Parallel()

	for _, clear := range []struct {
		name string
		set  func(*TeardownOwnerChain)
	}{
		{name: "socket identity", set: func(chain *TeardownOwnerChain) { chain.SocketIdentity = " " }},
		{name: "Pane handle", set: func(chain *TeardownOwnerChain) { chain.PaneHandle = "" }},
		{name: "Window handle", set: func(chain *TeardownOwnerChain) { chain.WindowHandle = "\t" }},
	} {
		t.Run(clear.name, func(t *testing.T) {
			event := decisionEvent()
			clear.set(&event.Chain)
			got := DecideTeardownEvent(event)
			if got.Action != TeardownRefuse || got.Reason != TeardownReasonInvalidInput {
				t.Fatalf("missing exact identity = %+v, want invalid refusal", got)
			}
		})
	}
}

func TestTeardownAggregationIsOrderIndependentAndRootBounded(t *testing.T) {
	t.Parallel()

	pane := decisionEvent()
	window := pane
	window.Kind = TeardownEventWindowUnlinked

	forward := AggregateTeardownEvents([]TeardownEvent{pane, window})
	reverse := AggregateTeardownEvents([]TeardownEvent{window, pane})
	if !EqualTeardownPlans(forward, reverse) {
		t.Fatalf("event order changed plan: forward=%+v reverse=%+v", forward, reverse)
	}
	if forward.Action != TeardownDeleteWindow || forward.RootAction != RootTeardownRetainProject ||
		forward.ReopenIdentity != ReopenIdentitySameProjectUID {
		t.Fatalf("last Project Window plan = %+v", forward)
	}
	duplicates := AggregateTeardownEvents([]TeardownEvent{window, pane, window, pane})
	if !EqualTeardownPlans(forward, duplicates) {
		t.Fatalf("duplicate delivery changed plan: once=%+v duplicates=%+v", forward, duplicates)
	}

	oneOfMany := pane
	oneOfMany.LiveSiblingPane = true
	got := AggregateTeardownEvents([]TeardownEvent{oneOfMany})
	if got.Action != TeardownDeletePaneAgent || got.RootAction != RootTeardownRetainProject {
		t.Fatalf("one-of-many plan = %+v", got)
	}
	if got == forward {
		t.Fatal("one-of-many and last-Pane plans collapsed into one outcome")
	}

	projectSibling := pane
	projectSibling.LiveSiblingRootWindow = true
	projectSiblingWindow := window
	projectSiblingWindow.LiveSiblingRootWindow = true
	got = AggregateTeardownEvents([]TeardownEvent{projectSiblingWindow, projectSibling})
	if got.Action != TeardownDeleteWindow || got.RootAction != RootTeardownRetainProject ||
		got.ReopenIdentity != ReopenIdentitySameProjectUID {
		t.Fatalf("non-last Project Window plan = %+v", got)
	}

	controlPane := pane
	controlPane.Chain.RootKind = KindControlSession
	controlPane.Chain.RootUID = "control-1"
	controlWindow := controlPane
	controlWindow.Kind = TeardownEventWindowUnlinked
	got = AggregateTeardownEvents([]TeardownEvent{controlPane, controlWindow})
	if got.Action != TeardownDeleteWindow || got.RootAction != RootTeardownRetainControlSession ||
		got.ReopenIdentity != ReopenIdentityNotApplicable {
		t.Fatalf("ControlSession last-Window plan = %+v", got)
	}
	if got.ExternalAssets.RootDirectory != AssetNotApplicable {
		t.Fatalf("ControlSession acquired filesystem assets: %+v", got.ExternalAssets)
	}
}

func TestWindowUnlinkAloneAndAdversarialObservationsDeleteNothing(t *testing.T) {
	t.Parallel()

	window := decisionEvent()
	window.Kind = TeardownEventWindowUnlinked
	if got := AggregateTeardownEvents([]TeardownEvent{window}); got.Action != TeardownRetain {
		t.Fatalf("window-unlinked alone = %+v, want retain", got)
	}

	for _, observation := range TeardownObservations() {
		if observation == TeardownObservationExactSocket {
			continue
		}
		event := decisionEvent()
		event.Observation = observation
		got := AggregateTeardownEvents([]TeardownEvent{event})
		if got.Action == TeardownDeletePaneAgent || got.Action == TeardownDeleteWindow ||
			got.RootAction == RootTeardownDeleteProject {
			t.Fatalf("observation %q produced a delete plan: %+v", observation, got)
		}
	}
	stale := decisionEvent()
	stale.Generation = TeardownGenerationStale
	if got := AggregateTeardownEvents([]TeardownEvent{stale}); got.Action != TeardownRefuse {
		t.Fatalf("stale generation = %+v, want refuse", got)
	}
}

func TestControlSessionLastWindowCausalPairDeletesWindowAndRetainsRootIdentity(t *testing.T) {
	t.Parallel()
	mutator := testMutator(dirSet{})
	registry := NewRegistry()
	_, err := mutator.BindControlSession(&registry, ControlSessionObservation{
		Session: "home", Windows: []ControlSessionWindow{{DisplayName: "shell", RuntimeSessionID: "$1", RuntimeID: "@2", Panes: []ControlSessionPane{{Command: "zsh", CWD: "/tmp"}}}},
	}, "/bin/zsh", "bind-control", nil)
	if err != nil {
		t.Fatal(err)
	}
	controlUID := registry.ControlSessions[0].Metadata.UID
	windowUID := registry.Windows[0].Metadata.UID
	paneUID := registry.Panes[0].Metadata.UID
	if _, err := mutator.RecordPaneActivation(&registry, paneUID, PaneActivationOptions{Generation: "gen-control", RuntimeID: "%7"}); err != nil {
		t.Fatal(err)
	}
	zero := 0
	if _, err := mutator.RecordTermination(&registry, TerminationEvidence{
		Source: TerminationSourceSupervisor, Classification: TerminationNormal,
		PaneUID: paneUID, Generation: "gen-control", ExitCode: &zero,
	}); err != nil {
		t.Fatal(err)
	}
	paneEvent := TeardownEvent{
		Kind: TeardownEventPaneExited, Classification: TerminationNormal,
		Generation: TeardownGenerationCurrent, Observation: TeardownObservationExactSocket,
		Chain: TeardownOwnerChain{SocketIdentity: "/tmp/control.sock", SessionHandle: "$1", PaneHandle: "%7", WindowHandle: "@2",
			PaneUID: paneUID, WindowUID: windowUID, RootKind: KindControlSession, RootUID: controlUID, Generation: "gen-control"},
	}
	pending, err := PlanPaneTeardownEvidence(registry, paneEvent, fixedNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !pending.Changed || pending.Decision.RootAction != RootTeardownRetainControlSession {
		t.Fatalf("control pending plan = %+v", pending)
	}
	unlinked := paneEvent
	unlinked.Kind = TeardownEventWindowUnlinked
	plan, err := PlanWindowRootCascadeDelete(pending.Desired, paneEvent, unlinked, fixedNow.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := plan.Desired.ControlSession(controlUID); !ok {
		t.Fatal("ControlSession identity was deleted")
	}
	if _, ok := plan.Desired.Window(windowUID); ok {
		t.Fatal("causal pair retained the ControlSession Window")
	}
	if _, ok := plan.Desired.Pane(paneUID); ok || plan.DeletedWindows != 1 || plan.DeletedPanes != 1 {
		t.Fatalf("control cascade = %+v", plan)
	}
	if err := plan.Desired.Validate(); err != nil {
		t.Fatalf("desired control Registry: %v", err)
	}
}

func TestTeardownAggregationRefusesMixedOrConflictingOwnerEvidence(t *testing.T) {
	t.Parallel()

	pane := decisionEvent()
	window := pane
	window.Kind = TeardownEventWindowUnlinked
	window.Chain.WindowUID = "window-foreign"
	for _, ordered := range [][]TeardownEvent{{pane, window}, {window, pane}} {
		got := AggregateTeardownEvents(ordered)
		if got.Action != TeardownRefuse || got.Reason != TeardownReasonMixedOwnerChain {
			t.Fatalf("mixed chain = %+v", got)
		}
	}
	foreignRoot := window
	foreignRoot.Chain.RootKind = KindControlSession
	foreignRoot.Chain.RootUID = "control-foreign"
	mixedForward := AggregateTeardownEvents([]TeardownEvent{pane, foreignRoot})
	mixedReverse := AggregateTeardownEvents([]TeardownEvent{foreignRoot, pane})
	if !EqualTeardownPlans(mixedForward, mixedReverse) || mixedForward.Reason != TeardownReasonMixedOwnerChain {
		t.Fatalf("mixed root conflict depends on order: forward=%+v reverse=%+v", mixedForward, mixedReverse)
	}
	for _, mutate := range []struct {
		name string
		set  func(*TeardownEvent)
	}{
		{name: "different exact socket", set: func(event *TeardownEvent) {
			event.Chain.SocketIdentity = "/tmp/tmux-sibling/projmux"
		}},
		{name: "different Pane runtime handle", set: func(event *TeardownEvent) {
			event.Chain.PaneHandle = "%99"
		}},
		{name: "different Window runtime handle", set: func(event *TeardownEvent) {
			event.Chain.WindowHandle = "@99"
		}},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			foreign := pane
			foreign.Kind = TeardownEventWindowUnlinked
			mutate.set(&foreign)
			forward := AggregateTeardownEvents([]TeardownEvent{pane, foreign})
			reverse := AggregateTeardownEvents([]TeardownEvent{foreign, pane})
			if !EqualTeardownPlans(forward, reverse) || forward.Action != TeardownRefuse ||
				forward.RootAction == RootTeardownDeleteProject || forward.Reason != TeardownReasonMixedOwnerChain {
				t.Fatalf("foreign exact identity depends on order or deletes: forward=%+v reverse=%+v", forward, reverse)
			}
		})
	}

	window = pane
	window.Kind = TeardownEventWindowUnlinked
	window.LiveSiblingPane = true
	got := AggregateTeardownEvents([]TeardownEvent{pane, window})
	if got.Action != TeardownRefuse || got.Reason != TeardownReasonConflictingOwnerFacts {
		t.Fatalf("conflicting owner facts = %+v", got)
	}
}

func TestProjectCascadeDeletePlanPreservesExternalAssetsAndReopensWithNewUID(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/srv/alpha": true, "/srv/bravo": true}
	mutator := testMutator(roots)
	registry := NewRegistry()
	alpha, err := registerFixture(mutator, &registry, "/srv/alpha")
	if err != nil {
		t.Fatalf("register alpha: %v", err)
	}
	if _, _, err := mutator.AddWindow(&registry, alpha.Project.Metadata.UID,
		BootstrapWindow{Name: "second"}, "/bin/zsh", "add-second"); err != nil {
		t.Fatalf("add second Window: %v", err)
	}
	agent, err := mutator.CreateAgent(&registry, alpha.Windows[0].Metadata.UID,
		CreateAgentOptions{Provider: "codex", OperationID: "add-agent"})
	if err != nil {
		t.Fatalf("create Agent: %v", err)
	}
	if _, err := mutator.AttachAgentPane(&registry, agent.Metadata.UID,
		BootstrapPane{CWD: "/srv/alpha"}, "attach-agent"); err != nil {
		t.Fatalf("attach Agent Pane: %v", err)
	}
	bravo, err := registerFixture(mutator, &registry, "/srv/bravo")
	if err != nil {
		t.Fatalf("register bravo: %v", err)
	}
	before := registry.Clone()
	deletedUIDs := []string{alpha.Project.Metadata.UID}
	for _, window := range registry.WindowsOf(alpha.Project.Metadata.UID) {
		deletedUIDs = append(deletedUIDs, window.Metadata.UID)
		for _, pane := range registry.PanesOf(window.Metadata.UID) {
			deletedUIDs = append(deletedUIDs, pane.Metadata.UID)
		}
		for _, ownedAgent := range registry.AgentsOf(window.Metadata.UID) {
			deletedUIDs = append(deletedUIDs, ownedAgent.Metadata.UID)
			for _, pane := range registry.PanesOf(ownedAgent.Metadata.UID) {
				deletedUIDs = append(deletedUIDs, pane.Metadata.UID)
			}
		}
	}
	external := map[string][]byte{
		"root": []byte("root-bytes"), "git": []byte("git-bytes"),
		"worktrees": []byte("worktree-bytes"), "snapshot": []byte("snapshot-bytes"),
	}
	externalBefore := map[string][]byte{}
	for key, value := range external {
		externalBefore[key] = slices.Clone(value)
	}

	plan, err := PlanProjectCascadeDelete(registry, alpha.Project.Metadata.UID, fixedNow.Add(1))
	if err != nil {
		t.Fatalf("PlanProjectCascadeDelete: %v", err)
	}
	if !reflect.DeepEqual(registry, before) {
		t.Fatal("pure delete planning mutated the source Registry")
	}
	if !plan.Changed || plan.DeletedProjects != 1 || plan.DeletedWindows != 2 ||
		plan.DeletedAgents != 1 || plan.ReopenIdentity != ReopenIdentityNewProjectUID {
		t.Fatalf("delete plan counts/outcome = %+v", plan)
	}
	if _, ok := plan.Desired.Project(alpha.Project.Metadata.UID); ok {
		t.Fatal("deleted Project UID remains in desired Registry")
	}
	for _, resource := range deletedUIDs {
		for _, reservation := range plan.Desired.NameReservations {
			if reservation.UID == resource || reservation.Scope == resource {
				t.Fatalf("reservation for deleted graph remains: %+v", reservation)
			}
		}
	}
	if _, ok := plan.Desired.Project(bravo.Project.Metadata.UID); !ok {
		t.Fatal("sibling Project was removed")
	}
	if err := plan.Desired.Validate(); err != nil {
		t.Fatalf("desired Registry invalid: %v", err)
	}
	if !reflect.DeepEqual(external, externalBefore) {
		t.Fatalf("external assets changed: before=%v after=%v", externalBefore, external)
	}
	if plan.ExternalAssets != projectPreservedAssets() {
		t.Fatalf("external asset policy = %+v", plan.ExternalAssets)
	}

	reopenMutator := Mutator{
		Now: func() time.Time { return fixedNow.Add(2) },
		NewUID: func(kind Kind) (string, error) {
			return fmt.Sprintf("%s-reopened", strings.ToLower(string(kind))), nil
		},
		DirExists: roots.exists,
	}
	reopened := plan.Desired.Clone()
	result, err := registerFixture(reopenMutator, &reopened, plan.Root)
	if err != nil {
		t.Fatalf("reopen deleted root: %v", err)
	}
	if result.Project.Metadata.UID == alpha.Project.Metadata.UID || result.Reused {
		t.Fatalf("reopen reused deleted identity: %+v", result)
	}
	if !reflect.DeepEqual(external["snapshot"], externalBefore["snapshot"]) {
		t.Fatal("reopen changed snapshot bytes")
	}
}

func TestPaneAgentCascadeDeletePlanReleasesPaneAndRetainsAgentWindow(t *testing.T) {
	t.Parallel()

	mutator := testMutator(dirSet{"/srv/alpha": true})
	registry := NewRegistry()
	registered, err := registerFixture(mutator, &registry, "/srv/alpha")
	if err != nil {
		t.Fatalf("register fixture: %v", err)
	}
	window := registered.Windows[0]
	agent, err := mutator.CreateAgent(&registry, window.Metadata.UID,
		CreateAgentOptions{Provider: "codex", OperationID: "create-agent"})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	pane, err := mutator.AttachAgentPane(&registry, agent.Metadata.UID,
		BootstrapPane{CWD: "/srv/alpha"}, "attach-agent")
	if err != nil {
		t.Fatalf("AttachAgentPane: %v", err)
	}
	if _, err := mutator.RecordPaneActivation(&registry, pane.Metadata.UID, PaneActivationOptions{
		Generation: "gen-clean", RuntimeID: "%9", AgentUID: agent.Metadata.UID,
	}); err != nil {
		t.Fatalf("RecordPaneActivation: %v", err)
	}
	if _, _, err := mutator.RecordAgentSessionRef(&registry, agent.Metadata.UID, AgentSessionObservation{
		Provider: "codex", ThreadID: "thread-clean",
	}); err != nil {
		t.Fatalf("RecordAgentSessionRef: %v", err)
	}
	zero := 0
	if _, err := mutator.RecordTermination(&registry, TerminationEvidence{
		Source: TerminationSourceSupervisor, Classification: TerminationNormal,
		PaneUID: pane.Metadata.UID, AgentUID: agent.Metadata.UID, Generation: "gen-clean", ExitCode: &zero,
	}); err != nil {
		t.Fatalf("RecordTermination: %v", err)
	}
	event := decisionEvent()
	event.LiveSiblingPane = true
	event.Chain.PaneUID = pane.Metadata.UID
	event.Chain.WindowUID = window.Metadata.UID
	event.Chain.RootUID = registered.Project.Metadata.UID
	event.Chain.Generation = "gen-clean"
	event.Chain.PaneHandle = "%9"
	before := registry.Clone()
	plan, err := PlanPaneAgentCascadeDelete(registry, event, fixedNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("PlanPaneAgentCascadeDelete: %v", err)
	}
	if !reflect.DeepEqual(registry, before) {
		t.Fatal("pure Pane/Agent planning mutated its source Registry")
	}
	if !plan.Changed || plan.PaneUID != pane.Metadata.UID || plan.AgentUID != agent.Metadata.UID ||
		plan.DeletedPanes != 1 || plan.DeletedAgents != 0 || plan.Evidence == nil ||
		plan.Evidence.Classification != TerminationNormal {
		t.Fatalf("Pane/Agent plan = %+v", plan)
	}
	if _, ok := plan.Desired.Pane(pane.Metadata.UID); ok {
		t.Fatal("qualifying Pane remains in desired Registry")
	}
	if retained, ok := plan.Desired.Agent(agent.Metadata.UID); !ok || retained.Status.Phase != PhaseOffline || retained.Status.PaneRef != "" {
		t.Fatalf("current Agent was not retained offline: %+v", retained)
	}
	if _, ok := plan.Desired.Window(window.Metadata.UID); !ok {
		t.Fatal("Phase 2 plan deleted the owning Window")
	}
	if _, ok := plan.Desired.Project(registered.Project.Metadata.UID); !ok {
		t.Fatal("Phase 2 plan deleted the owning Project")
	}
	for _, sibling := range registered.Panes {
		if _, ok := plan.Desired.Pane(sibling.Metadata.UID); !ok {
			t.Fatalf("Phase 2 plan deleted sibling Pane %s", sibling.Metadata.UID)
		}
	}
	if err := plan.Desired.Validate(); err != nil {
		t.Fatalf("desired Registry invalid: %v", err)
	}

	// The generic absence projection may win the lock before the exact dead
	// observation. It releases the binding but deliberately retains the Pane
	// row. The same clean generation must still be eligible for Pane-only retry.
	released := before.Clone()
	projection, err := mutator.ProjectTermination(&released, TerminationProjectionInput{
		PaneUID: pane.Metadata.UID, Generation: "gen-clean", ObservedAt: fixedNow.Add(30 * time.Second),
	})
	if err != nil {
		t.Fatalf("release before exact cleanup: %v", err)
	}
	if !projection.Changed || projection.Phase != PhaseOffline {
		t.Fatalf("released projection = %+v", projection)
	}
	beforeReleased := released.Clone()
	retry, err := PlanPaneAgentCascadeDelete(released, event, fixedNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("released same-generation retry: %v", err)
	}
	if !reflect.DeepEqual(released, beforeReleased) || !retry.Changed || retry.DeletedPanes != 1 || retry.DeletedAgents != 0 {
		t.Fatalf("released same-generation retry = %+v", retry)
	}
	releasedAgent, _ := beforeReleased.Agent(agent.Metadata.UID)
	if retained, ok := retry.Desired.Agent(agent.Metadata.UID); !ok || retained.Status.Phase != PhaseOffline ||
		retained.Status.PaneRef != "" || !retained.Status.SessionRef.SameConversation(releasedAgent.Status.SessionRef) {
		t.Fatalf("released retry changed retained Agent = %+v", retained)
	}
	if _, ok := retry.Desired.Pane(pane.Metadata.UID); ok {
		t.Fatal("released same-generation retry retained the Pane residual")
	}

	resumed := beforeReleased.Clone()
	if _, err := mutator.AttachAgentPane(&resumed, agent.Metadata.UID,
		BootstrapPane{CWD: "/srv/alpha"}, "resume-agent"); err != nil {
		t.Fatalf("attach resumed Pane: %v", err)
	}
	stale, err := PlanPaneAgentCascadeDelete(resumed, event, fixedNow.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("stale PlanPaneAgentCascadeDelete: %v", err)
	}
	if stale.Changed || stale.Decision.Action != TeardownRefuse || stale.Decision.Reason != TeardownReasonStaleOwnerBinding {
		t.Fatalf("resumed owner plan = %+v, want zero-write refusal", stale)
	}
}

func TestC2StableUIDGenerationAuthorityAcceptsReboundCurrentLocators(t *testing.T) {
	registry, event, paneUID, agentUID := exactAgentCascadeFixture(t)
	event.LiveSiblingPane = true
	// The Registry retains diagnostics from an older tmux server generation.
	// Current observed locators are deliberately different.
	pane, _ := registry.Pane(paneUID)
	pane.Status.Activation.RuntimeID = "%6"
	window, _ := registry.Window(event.Chain.WindowUID)
	window.Status.RuntimeSessionID, window.Status.RuntimeID = "$2", "@6"
	event.Chain.SessionHandle, event.Chain.WindowHandle, event.Chain.PaneHandle = "$2", "@2", "%2"

	plan, err := PlanPaneAgentCascadeDelete(registry, event, fixedNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Changed || plan.PaneUID != paneUID || plan.AgentUID != agentUID ||
		plan.Decision.Action != TeardownDeletePaneAgent {
		t.Fatalf("stable-authority rebound plan = %+v", plan)
	}
}

func TestLastPaneCausalPairDeletesWindowAndPreservesZeroWindowProject(t *testing.T) {
	t.Parallel()

	mutator := testMutator(dirSet{"/srv/alpha": true})
	registry := NewRegistry()
	registered, err := registerFixture(mutator, &registry, "/srv/alpha")
	if err != nil {
		t.Fatalf("register fixture: %v", err)
	}
	if len(registered.Windows) != 1 || len(registered.Panes) != 1 {
		t.Fatalf("fixture topology = %d Windows/%d Panes, want one last shell", len(registered.Windows), len(registered.Panes))
	}
	window := registered.Windows[0]
	pane := registered.Panes[0]
	for i := range registry.Windows {
		if registry.Windows[i].Metadata.UID == window.Metadata.UID {
			registry.Windows[i].Status.RuntimeSessionID = "$1"
			registry.Windows[i].Status.RuntimeID = "@4"
		}
	}
	if _, err := mutator.RecordPaneActivation(&registry, pane.Metadata.UID, PaneActivationOptions{
		Generation: "gen-last-shell", RuntimeID: "%9",
	}); err != nil {
		t.Fatalf("RecordPaneActivation: %v", err)
	}
	zero := 0
	if _, err := mutator.RecordTermination(&registry, TerminationEvidence{
		Source: TerminationSourceSupervisor, Classification: TerminationNormal,
		PaneUID: pane.Metadata.UID, Generation: "gen-last-shell", ExitCode: &zero,
	}); err != nil {
		t.Fatalf("RecordTermination: %v", err)
	}
	event := decisionEvent()
	event.Chain.PaneUID = pane.Metadata.UID
	event.Chain.WindowUID = window.Metadata.UID
	event.Chain.RootUID = registered.Project.Metadata.UID
	event.Chain.Generation = "gen-last-shell"
	event.Chain.PaneHandle = "%9"
	event.LiveSiblingPane = false
	before := registry.Clone()

	paneOnly, err := PlanPaneAgentCascadeDelete(registry, event, fixedNow.Add(time.Minute))
	if err != nil {
		t.Fatalf("PlanPaneAgentCascadeDelete: %v", err)
	}
	if paneOnly.Changed || paneOnly.Decision.Action != TeardownRetain ||
		paneOnly.Decision.Reason != TeardownReasonAwaitingWindowUnlink {
		t.Fatalf("last-Pane pane-only plan = %+v, want pending receipt", paneOnly)
	}
	if !reflect.DeepEqual(registry, before) {
		t.Fatal("last-Pane pane-only planning mutated its source Registry")
	}
	pending, err := PlanPaneTeardownEvidence(registry, event, fixedNow.Add(2*time.Minute))
	if err != nil || !pending.Changed {
		t.Fatalf("PlanPaneTeardownEvidence = %+v, %v", pending, err)
	}
	pendingBytes := mustJSON(t, pending.Desired)
	repeat, err := PlanPaneTeardownEvidence(pending.Desired, event, fixedNow.Add(3*time.Minute))
	if err != nil || repeat.Changed || !reflect.DeepEqual(pending.Desired, repeat.Desired) ||
		mustJSON(t, repeat.Desired) != pendingBytes || repeat.Evidence.ObservedAt != pending.Evidence.ObservedAt {
		t.Fatalf("later-clock pending repeat = %+v, %v", repeat, err)
	}
	unlinked := event
	unlinked.Kind = TeardownEventWindowUnlinked
	plan, err := PlanWindowRootCascadeDelete(pending.Desired, event, unlinked, fixedNow.Add(4*time.Minute))
	if err != nil || !plan.Changed {
		t.Fatalf("PlanWindowRootCascadeDelete = %+v, %v", plan, err)
	}
	if plan.Operation != ProjectLifecycleOperationCloseWindow {
		t.Fatalf("causal Window plan operation = %q, want close-window", plan.Operation)
	}
	project, ok := plan.Desired.Project(registered.Project.Metadata.UID)
	if !ok || project.Spec.PrimaryWindowRef != "" || len(plan.Desired.WindowsOf(project.Metadata.UID)) != 0 {
		t.Fatalf("zero-Window Project = %+v windows=%v", project, plan.Desired.WindowsOf(registered.Project.Metadata.UID))
	}
	if _, ok := plan.Desired.Window(window.Metadata.UID); ok {
		t.Fatal("causal pair retained target Window")
	}
	if err := plan.Desired.Validate(); err != nil {
		t.Fatalf("zero-Window desired Registry: %v", err)
	}
}

func TestCausalPrimaryWindowClosureReanchorsToExistingSibling(t *testing.T) {
	t.Parallel()

	mutator := testMutator(dirSet{"/srv/alpha": true})
	registry := NewRegistry()
	registered, err := registerFixture(mutator, &registry, "/srv/alpha")
	if err != nil {
		t.Fatal(err)
	}
	target := registered.Windows[0]
	targetPane := registered.Panes[0]
	sibling, _, err := mutator.AddWindow(&registry, registered.Project.Metadata.UID,
		BootstrapWindow{Name: "sibling"}, "sh", "add-sibling")
	if err != nil {
		t.Fatal(err)
	}
	for i := range registry.Windows {
		if registry.Windows[i].Metadata.UID == target.Metadata.UID {
			registry.Windows[i].Status.RuntimeSessionID = "$1"
			registry.Windows[i].Status.RuntimeID = "@4"
		}
	}
	if _, err := mutator.RecordPaneActivation(&registry, targetPane.Metadata.UID,
		PaneActivationOptions{Generation: "gen-primary-close", RuntimeID: "%9"}); err != nil {
		t.Fatal(err)
	}
	zero := 0
	if _, err := mutator.RecordTermination(&registry, TerminationEvidence{
		Source: TerminationSourceSupervisor, Classification: TerminationNormal,
		PaneUID: targetPane.Metadata.UID, Generation: "gen-primary-close", ExitCode: &zero,
	}); err != nil {
		t.Fatal(err)
	}
	siblingBefore, _ := registry.Window(sibling.Metadata.UID)
	siblingPanesBefore := registry.PanesOf(sibling.Metadata.UID)
	event := TeardownEvent{
		Kind: TeardownEventPaneExited, Classification: TerminationNormal,
		Generation: TeardownGenerationCurrent, Observation: TeardownObservationExactSocket,
		Chain: TeardownOwnerChain{
			SocketIdentity: "/tmp/primary.sock", SessionHandle: "$1", PaneHandle: "%9", WindowHandle: "@4",
			PaneUID: targetPane.Metadata.UID, WindowUID: target.Metadata.UID, RootKind: KindProject,
			RootUID: registered.Project.Metadata.UID, Generation: "gen-primary-close",
		},
		LiveSiblingRootWindow: true,
	}
	pending, err := PlanPaneTeardownEvidence(registry, event, fixedNow.Add(time.Minute))
	if err != nil || !pending.Changed {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
	unlinked := event
	unlinked.Kind = TeardownEventWindowUnlinked
	plan, err := PlanWindowRootCascadeDelete(pending.Desired, event, unlinked, fixedNow.Add(2*time.Minute))
	if err != nil || !plan.Changed {
		t.Fatalf("Window cascade = %+v, %v", plan, err)
	}
	project, _ := plan.Desired.Project(registered.Project.Metadata.UID)
	siblingAfter, ok := plan.Desired.Window(sibling.Metadata.UID)
	if !ok || project.Spec.PrimaryWindowRef != sibling.Metadata.UID ||
		!reflect.DeepEqual(siblingBefore, siblingAfter) ||
		!reflect.DeepEqual(siblingPanesBefore, plan.Desired.PanesOf(sibling.Metadata.UID)) {
		t.Fatalf("primary reanchor changed sibling: project=%+v before=%+v after=%+v", project, siblingBefore, siblingAfter)
	}
	if _, ok := plan.Desired.Window(target.Metadata.UID); ok {
		t.Fatal("causal primary target survived")
	}
}

func TestWindowCascadeRefusesReceiptAfterAgentResumedOnDifferentPane(t *testing.T) {
	t.Parallel()

	registry, event, paneUID, agentUID := exactAgentCascadeFixture(t)
	pending, err := PlanPaneTeardownEvidence(registry, event, fixedNow.Add(time.Minute))
	if err != nil || !pending.Changed {
		t.Fatalf("PlanPaneTeardownEvidence = %+v, %v", pending, err)
	}
	mutator := testMutator(dirSet{"/srv/alpha": true})
	resumed := pending.Desired.Clone()
	newPane, err := mutator.AttachAgentPane(&resumed, agentUID, BootstrapPane{CWD: "/srv/alpha"}, "resume-after-receipt")
	if err != nil {
		t.Fatalf("AttachAgentPane: %v", err)
	}
	window, _ := resumed.Window(event.Chain.WindowUID)
	window.Spec.AnchorPaneRef = newPane.Metadata.UID
	agent, _ := resumed.Agent(agentUID)
	if agent.Status.PaneRef == "" || agent.Status.PaneRef == paneUID {
		t.Fatalf("resumed binding = %q, want a distinct current Pane", agent.Status.PaneRef)
	}
	before := resumed.Clone()
	unlinked := event
	unlinked.Kind = TeardownEventWindowUnlinked
	plan, err := PlanWindowRootCascadeDelete(resumed, event, unlinked, fixedNow.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Changed || plan.Decision.Action != TeardownRefuse || plan.Decision.Reason != TeardownReasonStaleOwnerBinding ||
		!reflect.DeepEqual(resumed, before) {
		t.Fatalf("resumed stale cascade = %+v, want zero-write refusal", plan)
	}
}

func exactAgentCascadeFixture(t *testing.T) (Registry, TeardownEvent, string, string) {
	t.Helper()
	mutator := testMutator(dirSet{"/srv/alpha": true})
	registry := NewRegistry()
	registered, err := registerFixture(mutator, &registry, "/srv/alpha")
	if err != nil {
		t.Fatal(err)
	}
	windowUID := registered.Windows[0].Metadata.UID
	agent, err := mutator.CreateAgent(&registry, windowUID, CreateAgentOptions{Provider: "codex", OperationID: "create-cascade-agent"})
	if err != nil {
		t.Fatal(err)
	}
	pane, err := mutator.AttachAgentPane(&registry, agent.Metadata.UID, BootstrapPane{CWD: "/srv/alpha"}, "attach-cascade-agent")
	if err != nil {
		t.Fatal(err)
	}
	if !registry.deletePane(registered.Panes[0].Metadata.UID) {
		t.Fatal("delete fixture shell")
	}
	window, _ := registry.Window(windowUID)
	window.Spec.AnchorPaneRef = pane.Metadata.UID
	window.Spec.DefaultShellPaneRef = ""
	window.Status.RuntimeSessionID = "$1"
	window.Status.RuntimeID = "@4"
	if _, err := mutator.RecordPaneActivation(&registry, pane.Metadata.UID, PaneActivationOptions{
		Generation: "gen-agent-last", RuntimeID: "%9", AgentUID: agent.Metadata.UID,
	}); err != nil {
		t.Fatal(err)
	}
	zero := 0
	if _, err := mutator.RecordTermination(&registry, TerminationEvidence{
		Source: TerminationSourceSupervisor, Classification: TerminationNormal,
		PaneUID: pane.Metadata.UID, AgentUID: agent.Metadata.UID, Generation: "gen-agent-last", ExitCode: &zero,
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("agent cascade fixture: %v", err)
	}
	event := TeardownEvent{
		Kind: TeardownEventPaneExited, Classification: TerminationNormal,
		Generation: TeardownGenerationCurrent, Observation: TeardownObservationExactSocket,
		Chain: TeardownOwnerChain{
			SocketIdentity: "/tmp/agent.sock", SessionHandle: "$1", PaneHandle: "%9", WindowHandle: "@4",
			PaneUID: pane.Metadata.UID, WindowUID: windowUID, RootKind: KindProject,
			RootUID: registered.Project.Metadata.UID, Generation: "gen-agent-last",
		},
	}
	return registry, event, pane.Metadata.UID, agent.Metadata.UID
}

func TestProjectOpenStateTableHasNoBlankCells(t *testing.T) {
	t.Parallel()

	states := []ProjectReopenState{
		ProjectReopenLive, ProjectReopenClosed, ProjectReopenDeletedWithSnapshot,
		ProjectReopenDeletedWithoutSnapshot,
	}
	actions := []ProjectOpenAction{ProjectOpenContinue, ProjectOpenFresh}
	for _, state := range states {
		for _, action := range actions {
			plan := DecideProjectOpen(state, action)
			if plan.Source == "" || plan.Reason == "" || len(plan.AtomicWriteSet) == 0 {
				t.Fatalf("state=%q action=%q has blank cell: %+v", state, action, plan)
			}
			for _, write := range plan.AtomicWriteSet {
				if write == "" {
					t.Fatalf("state=%q action=%q has blank write: %+v", state, action, plan)
				}
			}
			if plan.ExternalAssets != projectPreservedAssets() || plan.AdditionalConfirm {
				t.Fatalf("state=%q action=%q crossed asset/confirmation boundary: %+v", state, action, plan)
			}
		}
	}

	without := DecideProjectOpen(ProjectReopenDeletedWithoutSnapshot, ProjectOpenContinue)
	if without.Available || without.Source != ProjectOpenSourceNone || without.NewProjectUID ||
		!slices.Equal(without.AtomicWriteSet, []ProjectStartupWrite{ProjectStartupWriteNone}) {
		t.Fatalf("deleted without snapshot silently fell back: %+v", without)
	}
	with := DecideProjectOpen(ProjectReopenDeletedWithSnapshot, ProjectOpenContinue)
	if !with.Available || with.Source != ProjectOpenSourceSnapshot || !with.NewProjectUID {
		t.Fatalf("deleted with snapshot continue = %+v", with)
	}
	for _, state := range states {
		fresh := DecideProjectOpen(state, ProjectOpenFresh)
		if !fresh.Available || fresh.Source != ProjectOpenSourceRoot || !fresh.NewProjectUID ||
			slices.Contains(fresh.AtomicWriteSet, ProjectStartupWriteRestoreSnapshotGraph) {
			t.Fatalf("fresh state %q reads snapshot or lacks new identity: %+v", state, fresh)
		}
	}
	invalid := DecideProjectOpen("invalid", "invalid")
	if invalid.Reason != ProjectOpenReasonInvalid || len(invalid.AtomicWriteSet) == 0 {
		t.Fatalf("invalid table cell = %+v", invalid)
	}
}

func TestProjectLifecycleStateTableHasTwelveClosedExclusiveCells(t *testing.T) {
	t.Parallel()

	states := []ProjectLifecycleState{
		ProjectLifecycleRetainedWindows,
		ProjectLifecycleZeroWindows,
		ProjectLifecycleDeleted,
	}
	actions := []ProjectLifecycleAction{
		ProjectLifecycleStop,
		ProjectLifecycleContinue,
		ProjectLifecycleFresh,
		ProjectLifecycleDeleteProject,
	}
	for _, state := range states {
		for _, action := range actions {
			plan := DecideProjectLifecycle(state, action, ProjectLifecyclePreconditions{})
			if plan.State != state || plan.Action != action || plan.Operation == ProjectLifecycleOperationNone || plan.ProjectUID == "" ||
				plan.DescendantUIDs == "" || plan.Reason == "" || len(plan.AtomicWriteSet) == 0 {
				t.Fatalf("state=%q action=%q has a blank cell: %+v", state, action, plan)
			}
			if plan.ExternalAssets != projectPreservedAssets() {
				t.Fatalf("state=%q action=%q external assets = %+v", state, action, plan.ExternalAssets)
			}
			seen := map[ProjectStartupWrite]bool{}
			for _, write := range plan.AtomicWriteSet {
				if write == "" || seen[write] {
					t.Fatalf("state=%q action=%q invalid write set: %+v", state, action, plan.AtomicWriteSet)
				}
				seen[write] = true
			}
			if seen[ProjectStartupWriteStopRuntime] &&
				(seen[ProjectStartupWriteDeleteProjectGraph] || seen[ProjectStartupWriteCreateProject]) {
				t.Fatalf("state=%q action=%q mixed stop and identity writes: %+v", state, action, plan.AtomicWriteSet)
			}
			if action != ProjectLifecycleFresh && seen[ProjectStartupWriteCreateProject] {
				t.Fatalf("state=%q action=%q unexpectedly creates Project identity: %+v", state, action, plan.AtomicWriteSet)
			}
		}
	}
	projectOperations := map[ProjectLifecycleOperation]bool{}
	for _, action := range actions {
		projectOperations[DecideProjectLifecycle(ProjectLifecycleRetainedWindows, action, ProjectLifecyclePreconditions{}).Operation] = true
	}
	if projectOperations[ProjectLifecycleOperationCloseWindow] || len(projectOperations) != len(actions) {
		t.Fatalf("Project lifecycle operations overlap close-window or each other: %+v", projectOperations)
	}

	retainedContinue := DecideProjectLifecycle(ProjectLifecycleRetainedWindows, ProjectLifecycleContinue, ProjectLifecyclePreconditions{})
	if retainedContinue.ProjectUID != ProjectUIDPreserved || retainedContinue.DescendantUIDs != ProjectDescendantUIDsPreserved {
		t.Fatalf("retained Continue identity = %+v", retainedContinue)
	}
	zeroContinue := DecideProjectLifecycle(ProjectLifecycleZeroWindows, ProjectLifecycleContinue, ProjectLifecyclePreconditions{})
	if zeroContinue.ProjectUID != ProjectUIDPreserved || zeroContinue.DescendantUIDs != ProjectDescendantUIDsCreated ||
		!slices.Equal(zeroContinue.AtomicWriteSet, []ProjectStartupWrite{ProjectStartupWriteCreateCanonicalWindow, ProjectStartupWriteCreateCanonicalShell}) {
		t.Fatalf("zero-Window Continue identity = %+v", zeroContinue)
	}
	for _, state := range []ProjectLifecycleState{ProjectLifecycleRetainedWindows, ProjectLifecycleZeroWindows} {
		fresh := DecideProjectLifecycle(state, ProjectLifecycleFresh, ProjectLifecyclePreconditions{})
		if !fresh.Available || fresh.ProjectUID != ProjectUIDReplaced || !slices.Equal(fresh.AtomicWriteSet, freshReplacementWriteSet()) {
			t.Fatalf("state=%q Fresh identity = %+v", state, fresh)
		}
	}
	deletedContinueUnavailable := DecideProjectLifecycle(ProjectLifecycleDeleted, ProjectLifecycleContinue, ProjectLifecyclePreconditions{})
	if deletedContinueUnavailable.Available || deletedContinueUnavailable.Reason != "no-usable-snapshot" ||
		!slices.Equal(deletedContinueUnavailable.AtomicWriteSet, []ProjectStartupWrite{ProjectStartupWriteNone}) {
		t.Fatalf("deleted Continue without snapshot evidence = %+v", deletedContinueUnavailable)
	}
	deletedContinue := DecideProjectLifecycle(ProjectLifecycleDeleted, ProjectLifecycleContinue, ProjectLifecyclePreconditions{UsableSnapshot: true})
	if !deletedContinue.Available || deletedContinue.ProjectUID != ProjectUIDCreated || deletedContinue.DescendantUIDs != ProjectDescendantUIDsCreated ||
		!slices.Equal(deletedContinue.AtomicWriteSet, []ProjectStartupWrite{ProjectStartupWriteCreateProject, ProjectStartupWriteRestoreSnapshotGraph}) {
		t.Fatalf("deleted Continue with usable snapshot = %+v", deletedContinue)
	}
}

func TestPlanProjectFreshReplacementCreatesOneNewClaimantAndPreservesPreimage(t *testing.T) {
	t.Parallel()

	mutator := testMutator(dirSet{"/srv/alpha": true, "/srv/sibling": true})
	registry := NewRegistry()
	registry.UpdatedAt = fixedNow
	target, err := registerFixture(mutator, &registry, "/srv/alpha")
	if err != nil {
		t.Fatal(err)
	}
	sibling, err := registerFixture(mutator, &registry, "/srv/sibling")
	if err != nil {
		t.Fatal(err)
	}
	before := registry.Clone()
	plan, err := PlanProjectFreshReplacement(registry, target.Project.Metadata.UID, RegisterProjectOptions{
		SessionName: "alpha", DefaultShell: "/bin/zsh",
	}, mutator)
	if err != nil {
		t.Fatalf("PlanProjectFreshReplacement() error = %v", err)
	}
	if !reflect.DeepEqual(registry, before) || !reflect.DeepEqual(plan.Preimage, before) {
		t.Fatal("Fresh planning mutated or failed to retain the old graph preimage")
	}
	if plan.OldProjectUID != target.Project.Metadata.UID || plan.NewProjectUID == "" || plan.NewProjectUID == plan.OldProjectUID {
		t.Fatalf("Fresh Project identity = old %q new %q", plan.OldProjectUID, plan.NewProjectUID)
	}
	claimants := 0
	for _, project := range plan.Desired.Projects {
		if project.Spec.Root == "/srv/alpha" {
			claimants++
		}
	}
	if claimants != 1 || len(plan.Desired.WindowsOf(plan.NewProjectUID)) != 1 {
		t.Fatalf("Fresh claimant/topology = claimants %d windows %+v", claimants, plan.Desired.WindowsOf(plan.NewProjectUID))
	}
	if got, ok := plan.Desired.Project(sibling.Project.Metadata.UID); !ok || !reflect.DeepEqual(got, &sibling.Project) {
		t.Fatalf("Fresh changed unrelated Project: %+v", got)
	}
	if err := plan.Desired.Validate(); err != nil {
		t.Fatalf("Fresh desired Registry invalid: %v", err)
	}
}

func TestPlanProjectFreshReplacementSupportsZeroWindowAndFailureRetainsOldGraph(t *testing.T) {
	t.Parallel()

	mutator := testMutator(dirSet{"/srv/alpha": true})
	registry := NewRegistry()
	registry.UpdatedAt = fixedNow
	target, err := registerFixture(mutator, &registry, "/srv/alpha")
	if err != nil {
		t.Fatal(err)
	}
	for _, window := range registry.WindowsOf(target.Project.Metadata.UID) {
		if err := mutator.DeleteWindow(&registry, window.Metadata.UID); err != nil {
			t.Fatal(err)
		}
	}
	before := registry.Clone()
	plan, err := PlanProjectFreshReplacement(registry, target.Project.Metadata.UID, RegisterProjectOptions{
		SessionName: "alpha", DefaultShell: "/bin/zsh",
	}, mutator)
	if err != nil || plan.NewProjectUID == plan.OldProjectUID || len(plan.Desired.WindowsOf(plan.NewProjectUID)) != 1 {
		t.Fatalf("zero-Window Fresh = %+v, err %v", plan, err)
	}
	if !reflect.DeepEqual(registry, before) {
		t.Fatal("zero-Window Fresh planning mutated its input")
	}

	failing := mutator
	failing.NewUID = func(Kind) (string, error) { return "", fmt.Errorf("injected uid allocation failure") }
	if _, err := PlanProjectFreshReplacement(registry, target.Project.Metadata.UID, RegisterProjectOptions{
		SessionName: "alpha", DefaultShell: "/bin/zsh",
	}, failing); err == nil || !strings.Contains(err.Error(), "injected uid allocation failure") {
		t.Fatalf("Fresh failure = %v", err)
	}
	if !reflect.DeepEqual(registry, before) {
		t.Fatal("Fresh plan failure did not retain the old graph preimage")
	}
}

func FuzzPlanProjectFreshReplacementIsScopedValidAndAlwaysMintsIdentity(f *testing.F) {
	f.Add(false, uint8(0))
	f.Add(false, uint8(3))
	f.Add(true, uint8(0))
	f.Add(true, uint8(2))
	f.Fuzz(func(t *testing.T, zeroWindows bool, retainedShape uint8) {
		mutator := testMutator(dirSet{"/srv/alpha": true, "/srv/sibling": true})
		registry := NewRegistry()
		registry.UpdatedAt = fixedNow
		target, err := registerFixture(mutator, &registry, "/srv/alpha")
		if err != nil {
			t.Fatal(err)
		}
		sibling, err := registerFixture(mutator, &registry, "/srv/sibling")
		if err != nil {
			t.Fatal(err)
		}
		control, err := mutator.BindControlSession(&registry, ControlSessionObservation{
			Session: "home",
			Windows: []ControlSessionWindow{{
				DisplayName: "control",
				Panes:       []ControlSessionPane{{Command: "/bin/zsh", CWD: "/srv/sibling"}},
			}},
		}, "/bin/zsh", "op-control", nil)
		if err != nil {
			t.Fatal(err)
		}

		for index := 0; index < int(retainedShape%4); index++ {
			if _, _, err := mutator.AddWindow(&registry, target.Project.Metadata.UID, BootstrapWindow{
				Name: fmt.Sprintf("retained-%d", index),
				Panes: []BootstrapPane{{
					Name:    fmt.Sprintf("shell-%d", index),
					Command: "/bin/zsh",
				}},
			}, "/bin/zsh", fmt.Sprintf("op-retained-%d", index)); err != nil {
				t.Fatal(err)
			}
		}
		if retainedShape&1 != 0 {
			agent, err := mutator.CreateAgent(&registry, target.Project.Spec.PrimaryWindowRef, CreateAgentOptions{
				Provider: "codex", Workspace: AgentWorkspace{CWD: "/srv/alpha"}, OperationID: "op-agent",
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := mutator.AttachAgentPane(&registry, agent.Metadata.UID, BootstrapPane{
				Command: "codex", CWD: "/srv/alpha",
			}, "op-agent-pane"); err != nil {
				t.Fatal(err)
			}
		}
		if zeroWindows {
			for _, window := range registry.WindowsOf(target.Project.Metadata.UID) {
				if err := mutator.DeleteWindow(&registry, window.Metadata.UID); err != nil {
					t.Fatal(err)
				}
			}
		}
		if err := registry.Validate(); err != nil {
			t.Fatalf("fuzz fixture is invalid: %v", err)
		}

		before := registry.Clone()
		plan, err := PlanProjectFreshReplacement(registry, target.Project.Metadata.UID, RegisterProjectOptions{
			SessionName: "alpha", DefaultShell: "/bin/zsh",
		}, mutator)
		if err != nil {
			t.Fatalf("PlanProjectFreshReplacement() error = %v", err)
		}
		if !reflect.DeepEqual(registry, before) || !reflect.DeepEqual(plan.Preimage, before) {
			t.Fatal("Fresh planning mutated its input or did not retain the exact preimage")
		}
		if plan.OldProjectUID != target.Project.Metadata.UID || plan.NewProjectUID == "" || plan.NewProjectUID == plan.OldProjectUID {
			t.Fatalf("Fresh Project identity = old %q new %q", plan.OldProjectUID, plan.NewProjectUID)
		}
		claimants := 0
		for _, project := range plan.Desired.Projects {
			if project.Spec.Root == target.Project.Spec.Root {
				claimants++
			}
		}
		if claimants != 1 {
			t.Fatalf("same-root Project claimants = %d, want exactly 1", claimants)
		}
		freshWindows := plan.Desired.WindowsOf(plan.NewProjectUID)
		if len(freshWindows) != 1 || len(plan.Desired.PanesOf(freshWindows[0].Metadata.UID)) != 1 {
			t.Fatalf("Fresh desired topology = windows %+v panes %+v, want one canonical Window/shell", freshWindows, func() []Pane {
				if len(freshWindows) == 0 {
					return nil
				}
				return plan.Desired.PanesOf(freshWindows[0].Metadata.UID)
			}())
		}
		if err := plan.Desired.Validate(); err != nil {
			t.Fatalf("Fresh desired Registry invalid: %v", err)
		}
		if got, ok := plan.Desired.Project(sibling.Project.Metadata.UID); !ok || !reflect.DeepEqual(got, &sibling.Project) {
			t.Fatalf("Fresh changed unrelated Project: %+v", got)
		}
		if got, ok := plan.Desired.ControlSession(control.ControlSession.Metadata.UID); !ok || !reflect.DeepEqual(got, &control.ControlSession) {
			t.Fatalf("Fresh changed unrelated ControlSession: %+v", got)
		}

		// Removing the new graph from Desired must leave exactly the same
		// unrelated Project, ControlSession, descendant, and reservation state as
		// removing the old graph from the input preimage.
		wantRemainder := before.Clone()
		if err := mutator.DeleteProject(&wantRemainder, plan.OldProjectUID); err != nil {
			t.Fatal(err)
		}
		gotRemainder := plan.Desired.Clone()
		if err := mutator.DeleteProject(&gotRemainder, plan.NewProjectUID); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotRemainder, wantRemainder) {
			t.Fatal("Fresh replacement changed unrelated graph or reservation state")
		}

		failing := mutator
		failing.NewUID = func(Kind) (string, error) {
			return "", fmt.Errorf("injected fuzz uid allocation failure")
		}
		failed, err := PlanProjectFreshReplacement(registry, target.Project.Metadata.UID, RegisterProjectOptions{
			SessionName: "alpha", DefaultShell: "/bin/zsh",
		}, failing)
		if err == nil || !strings.Contains(err.Error(), "injected fuzz uid allocation failure") {
			t.Fatalf("Fresh allocation failure = %v", err)
		}
		if !reflect.DeepEqual(registry, before) || !reflect.DeepEqual(failed.Preimage, before) {
			t.Fatal("failed Fresh planning mutated its input or lost its old graph preimage")
		}
	})
}
