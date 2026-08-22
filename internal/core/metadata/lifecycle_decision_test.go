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
			SocketIdentity: "/tmp/tmux-app/projmux", PaneHandle: "%7", WindowHandle: "@4",
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
		TerminationIntentional, TerminationNormal, TerminationKilled,
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
	if forward.Action != TeardownDeleteWindow || forward.RootAction != RootTeardownDeleteProject ||
		forward.ReopenIdentity != ReopenIdentityNewProjectUID {
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
