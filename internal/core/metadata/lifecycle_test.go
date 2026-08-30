package metadata

import (
	"reflect"
	"testing"
	"time"
)

// lifecycle_test.go covers the exit reconciliation projection: turning one
// termination receipt, or the absence of one, into durable Agent and Pane state.
//
// Every test states the registry as the thing being judged and the receipt as
// the only evidence available. Nothing here observes tmux, and that is the point:
// the projection is a consumer, and a consumer that needed a live server to
// decide what an already-recorded exit meant would be re-deciding it every time.

const (
	lifecycleGeneration = "gen-current"
	lifecyclePaneUID    = "pan-managed"
	lifecycleAgentUID   = "agt-codex"
	lifecycleShellUID   = "pan-shell"
)

var lifecycleClock = time.Date(2026, 8, 19, 6, 0, 0, 0, time.UTC)

func lifecycleMutator() Mutator {
	return Mutator{Now: func() time.Time { return lifecycleClock }}
}

// lifecycleFixture builds a registry with one Window, one shell Pane, and one
// Agent bound to its own managed Pane. Both Panes carry the same activation
// generation, so a receipt has to name a Pane to be applied to it.
func lifecycleFixture(t *testing.T) *Registry {
	t.Helper()

	registry := NewRegistry()
	registry.Projects = []Project{{
		APIVersion: APIVersion, Kind: KindProject,
		Metadata: ObjectMeta{UID: "prj-alpha", Name: "alpha", CreatedAt: lifecycleClock},
		Spec:     ProjectSpec{Root: "/srv/alpha", PrimaryWindowRef: "win-main"},
	}}
	registry.Windows = []Window{{
		APIVersion: APIVersion, Kind: KindWindow,
		Metadata: ObjectMeta{
			UID: "win-main", Name: "main", CreatedAt: lifecycleClock,
			OwnerRef: &OwnerRef{Kind: KindProject, UID: "prj-alpha"},
		},
		Spec: WindowSpec{AnchorPaneRef: lifecycleShellUID},
	}}
	registry.Panes = []Pane{
		{
			APIVersion: APIVersion, Kind: KindPane,
			Metadata: ObjectMeta{
				UID: lifecycleShellUID, Name: "zsh", CreatedAt: lifecycleClock,
				OwnerRef: &OwnerRef{Kind: KindWindow, UID: "win-main"},
			},
			Spec:   PaneSpec{Role: PaneRoleShell, CWD: "/srv/alpha"},
			Status: PaneStatus{Activation: PaneActivation{Generation: lifecycleGeneration, StartedAt: lifecycleClock}},
		},
		{
			APIVersion: APIVersion, Kind: KindPane,
			Metadata: ObjectMeta{
				UID: lifecyclePaneUID, Name: "codex-pane", CreatedAt: lifecycleClock,
				OwnerRef: &OwnerRef{Kind: KindAgent, UID: lifecycleAgentUID},
			},
			Spec: PaneSpec{Role: PaneRoleAgent, CWD: "/srv/alpha"},
			Status: PaneStatus{Activation: PaneActivation{
				Generation: lifecycleGeneration, AgentUID: lifecycleAgentUID, StartedAt: lifecycleClock,
			}},
		},
	}
	registry.Agents = []Agent{{
		APIVersion: APIVersion, Kind: KindAgent,
		Metadata: ObjectMeta{
			UID: lifecycleAgentUID, Name: "codex", CreatedAt: lifecycleClock,
			OwnerRef: &OwnerRef{Kind: KindWindow, UID: "win-main"},
		},
		Spec: AgentSpec{Provider: "codex"},
		Status: AgentStatus{
			Phase: PhaseRunning, PaneRef: lifecyclePaneUID, LastTransitionAt: lifecycleClock,
			SessionRef: &AgentSessionRef{
				Provider:   "codex",
				ObservedAt: lifecycleClock,
				Codex:      &CodexSessionRef{ThreadID: "thr-9", SessionID: "ses-9"},
			},
		},
	}}
	registry.NameReservations = []NameReservation{
		{Scope: "", Kind: KindProject, Name: "alpha", UID: "prj-alpha"},
		{Scope: "prj-alpha", Kind: KindWindow, Name: "main", UID: "win-main"},
		{Scope: "win-main", Kind: KindPane, Name: "zsh", UID: lifecycleShellUID},
		{Scope: lifecycleAgentUID, Kind: KindPane, Name: "codex-pane", UID: lifecyclePaneUID},
		{Scope: "win-main", Kind: KindAgent, Name: "codex", UID: lifecycleAgentUID},
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("lifecycle fixture is not a valid registry: %v", err)
	}
	return &registry
}

// recordSupervisorExit files the receipt a supervisor that reaped a child files.
func recordSupervisorExit(t *testing.T, registry *Registry, paneUID, agentUID string, exitCode int, signal string) {
	t.Helper()
	receipt := TerminationEvidence{
		Source:         TerminationSourceSupervisor,
		Classification: ClassifyProcessExit(exitCode, signal),
		ObservedAt:     lifecycleClock,
		PaneUID:        paneUID,
		AgentUID:       agentUID,
		Generation:     lifecycleGeneration,
		Signal:         signal,
	}
	if signal == "" {
		code := exitCode
		receipt.ExitCode = &code
	}
	outcome, err := lifecycleMutator().RecordTermination(registry, receipt)
	if err != nil {
		t.Fatalf("RecordTermination: %v", err)
	}
	if !outcome.Applied {
		t.Fatalf("supervisor receipt was not applied: %+v", outcome)
	}
}

// recordIntent files the receipt a canonical control action files before it
// mutates anything live.
func recordIntent(t *testing.T, registry *Registry, paneUID, agentUID, operationID string) {
	t.Helper()
	outcome, err := lifecycleMutator().RecordTermination(registry, TerminationEvidence{
		Source:         TerminationSourceControlAction,
		Classification: TerminationIntentional,
		ObservedAt:     lifecycleClock,
		PaneUID:        paneUID,
		AgentUID:       agentUID,
		Generation:     lifecycleGeneration,
		OperationID:    operationID,
	})
	if err != nil {
		t.Fatalf("RecordTermination: %v", err)
	}
	if !outcome.Applied {
		t.Fatalf("intent receipt was not applied: %+v", outcome)
	}
}

// TestTheClassificationMatrixConvergesDeterministically is acceptance criterion
// 1: every producer of a managed-process death lands the Agent in exactly one
// phase, with a reason that names which kind of evidence produced it.
//
// The phases deliberately collapse classifications, so the reason column is
// the assertion that actually separates them. An operator
// reading `Offline` needs to know whether they closed it, it finished, or it
// simply vanished, and only one of those three is a reason to go looking.
func TestTheClassificationMatrixConvergesDeterministically(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name               string
		record             func(*testing.T, *Registry)
		wantClassification TerminationClassification
		wantPhase          AgentPhase
		wantReason         string
	}{
		{
			name: "an external hangup is killed, never intentional",
			record: func(t *testing.T, registry *Registry) {
				recordSupervisorExit(t, registry, lifecyclePaneUID, lifecycleAgentUID, 0, "HUP")
			},
			wantClassification: TerminationKilled,
			wantPhase:          PhaseOffline,
			wantReason:         TerminationReasonKilled,
		},
		{
			name: "an explicit control action is intentional",
			record: func(t *testing.T, registry *Registry) {
				recordIntent(t, registry, lifecyclePaneUID, lifecycleAgentUID, "op-delete")
			},
			wantClassification: TerminationIntentional,
			wantPhase:          PhaseOffline,
			wantReason:         TerminationReasonIntentional,
		},
		{
			name: "an exact Project stop is interrupted",
			record: func(t *testing.T, registry *Registry) {
				outcome, err := lifecycleMutator().RecordTermination(registry, TerminationEvidence{
					Source: TerminationSourceControlAction, Classification: TerminationInterrupted,
					PaneUID: lifecyclePaneUID, AgentUID: lifecycleAgentUID,
					Generation: lifecycleGeneration, OperationID: "op-project-stop",
				})
				if err != nil || !outcome.Applied {
					t.Fatalf("interrupted receipt = %+v, %v", outcome, err)
				}
			},
			wantClassification: TerminationInterrupted,
			wantPhase:          PhaseOffline,
			wantReason:         TerminationReasonInterrupted,
		},
		{
			name: "a supervised exit 0 is normal, never intentional",
			record: func(t *testing.T, registry *Registry) {
				recordSupervisorExit(t, registry, lifecyclePaneUID, lifecycleAgentUID, 0, "")
			},
			wantClassification: TerminationNormal,
			wantPhase:          PhaseOffline,
			wantReason:         TerminationReasonNormal,
		},
		{
			name: "a supervised exit 42 is abnormal",
			record: func(t *testing.T, registry *Registry) {
				recordSupervisorExit(t, registry, lifecyclePaneUID, lifecycleAgentUID, 42, "")
			},
			wantClassification: TerminationAbnormal,
			wantPhase:          PhaseFailed,
			wantReason:         TerminationReasonAbnormal,
		},
		{
			name: "a death by signal is abnormal whatever code came with it",
			record: func(t *testing.T, registry *Registry) {
				recordSupervisorExit(t, registry, lifecyclePaneUID, lifecycleAgentUID, 0, "SIGKILL")
			},
			wantClassification: TerminationAbnormal,
			wantPhase:          PhaseFailed,
			wantReason:         TerminationReasonAbnormal,
		},
		{
			name:               "a disappearance with no receipt is unknown",
			record:             func(*testing.T, *Registry) {},
			wantClassification: TerminationUnknown,
			wantPhase:          PhaseOffline,
			wantReason:         TerminationReasonUnknown,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			registry := lifecycleFixture(t)
			test.record(t, registry)

			projection, err := lifecycleMutator().ProjectTermination(registry,
				TerminationProjectionInput{PaneUID: lifecyclePaneUID, ObservedAt: lifecycleClock})
			if err != nil {
				t.Fatalf("ProjectTermination: %v", err)
			}
			if !projection.Changed {
				t.Fatalf("projection changed nothing: %+v", projection)
			}
			if projection.Classification != test.wantClassification {
				t.Fatalf("classification = %q, want %q", projection.Classification, test.wantClassification)
			}
			if projection.Phase != test.wantPhase {
				t.Fatalf("phase = %q, want %q", projection.Phase, test.wantPhase)
			}
			if !projection.PaneRetained {
				t.Fatal("hook-driven termination claimed canonical Pane deletion authority")
			}

			agent, ok := registry.Agent(lifecycleAgentUID)
			if !ok {
				t.Fatal("the projection deleted the Agent; a released Agent survives as a resumable resource")
			}
			if agent.Status.Phase != test.wantPhase {
				t.Fatalf("agent phase = %q, want %q", agent.Status.Phase, test.wantPhase)
			}
			if agent.Status.Reason != test.wantReason {
				t.Fatalf("agent reason = %q, want %q", agent.Status.Reason, test.wantReason)
			}
			if agent.Status.PaneRef != "" {
				t.Fatalf("paneRef = %q, want the current binding released", agent.Status.PaneRef)
			}
			if agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil ||
				agent.Status.SessionRef.Codex.ThreadID != "thr-9" {
				t.Fatalf("sessionRef = %+v, want the conversation pointer preserved", agent.Status.SessionRef)
			}
			receipt := agent.Status.LastTermination
			if receipt == nil {
				t.Fatal("the released Agent kept no evidence at all")
			}
			if receipt.Classification != test.wantClassification {
				t.Fatalf("stored evidence = %+v, want classification %q", receipt, test.wantClassification)
			}
			if receipt.ObservedAt.IsZero() {
				t.Fatal("the stored evidence carries no observation instant")
			}
			if pane, ok := registry.Pane(lifecyclePaneUID); !ok || pane.Status.LastTermination == nil {
				t.Fatal("hook-driven termination failed to preserve the Pane row and evidence")
			}
			if err := registry.Validate(); err != nil {
				t.Fatalf("the projected registry does not validate: %v", err)
			}
		})
	}
}

// TestBothEventOrdersReachTheSameFinalState is acceptance criterion 2.
//
// A supervisor writes its receipt before its own process exits, so the receipt is
// durable before tmux tears the pane down. What varies is which trigger reaches
// the reconciler first, and the reconciler re-reads both the registry and the
// host at transition time -- so a receipt-then-absence delivery and an
// absence-then-late-receipt delivery must preserve the same row boundary.
//
// A same-generation late supervisor receipt refines the temporary unknown
// evidence and its already-unbound Agent disposition. A resumed Agent is still
// excluded by the non-empty paneRef guard; this case is the exact binding the
// absence pass itself released.
func TestBothEventOrdersReachTheSameFinalState(t *testing.T) {
	t.Parallel()

	receiptFirst := lifecycleFixture(t)
	recordSupervisorExit(t, receiptFirst, lifecyclePaneUID, lifecycleAgentUID, 42, "")
	if _, err := lifecycleMutator().ProjectTermination(receiptFirst,
		TerminationProjectionInput{PaneUID: lifecyclePaneUID, ObservedAt: lifecycleClock}); err != nil {
		t.Fatalf("receipt-first projection: %v", err)
	}

	absenceFirst := lifecycleFixture(t)
	// The absence is observed while the receipt is still in flight, so the
	// receipt lands after status projection released the Agent binding. The Pane
	// row remains as the generation-safe refinement target.
	if _, err := lifecycleMutator().ProjectTermination(absenceFirst,
		TerminationProjectionInput{PaneUID: lifecyclePaneUID, ObservedAt: lifecycleClock}); err != nil {
		t.Fatalf("absence-first projection: %v", err)
	}
	lateReceipt := TerminationEvidence{
		Source:         TerminationSourceSupervisor,
		Classification: TerminationAbnormal,
		ObservedAt:     lifecycleClock,
		PaneUID:        lifecyclePaneUID,
		AgentUID:       lifecycleAgentUID,
		Generation:     lifecycleGeneration,
		ExitCode:       exitCode(42),
	}
	outcome, err := lifecycleMutator().RecordTermination(absenceFirst, lateReceipt)
	if err != nil {
		t.Fatalf("late RecordTermination: %v", err)
	}
	if !outcome.Applied || outcome.Stale {
		t.Fatalf("late receipt outcome = %+v, want same-generation refinement applied", outcome)
	}
	refined, err := lifecycleMutator().ProjectTermination(absenceFirst,
		TerminationProjectionInput{PaneUID: lifecyclePaneUID, ObservedAt: lifecycleClock})
	if err != nil {
		t.Fatalf("late refinement projection: %v", err)
	}
	if !refined.Changed || refined.Classification != TerminationAbnormal || refined.Phase != PhaseFailed {
		t.Fatalf("late refinement = %+v, want abnormal Failed", refined)
	}

	for label, registry := range map[string]*Registry{"receipt-first": receiptFirst, "absence-first": absenceFirst} {
		agent, _ := registry.Agent(lifecycleAgentUID)
		pane, ok := registry.Pane(lifecyclePaneUID)
		if !ok || pane.Status.LastTermination == nil {
			t.Fatalf("%s deleted the Pane row or its evidence", label)
		}
		if agent.Status.PaneRef != "" || agent.Status.Phase != PhaseFailed ||
			agent.Status.Reason != TerminationReasonAbnormal || agent.Status.SessionRef == nil {
			t.Fatalf("%s Agent status = %+v, want released Failed/abnormal with session", label, agent.Status)
		}
		for owner, evidence := range map[string]*TerminationEvidence{
			"Pane": pane.Status.LastTermination, "Agent": agent.Status.LastTermination,
		} {
			if evidence == nil || evidence.Source != TerminationSourceSupervisor ||
				evidence.Classification != TerminationAbnormal || evidence.ExitCode == nil || *evidence.ExitCode != 42 {
				t.Fatalf("%s %s evidence = %+v, want abnormal/supervisor exit=42", label, owner, evidence)
			}
		}
		if err := registry.Validate(); err != nil {
			t.Fatalf("%s registry invalid: %v", label, err)
		}
	}
	if !reflect.DeepEqual(receiptFirst.Normalize(), absenceFirst.Normalize()) {
		t.Fatal("receipt-first and absence-first did not converge byte-for-byte")
	}

	settled := absenceFirst.Clone().Normalize()
	duplicate, err := lifecycleMutator().RecordTermination(absenceFirst, lateReceipt)
	if err != nil || duplicate.Applied || (!duplicate.Duplicate && !duplicate.Stale) {
		t.Fatalf("repeat receipt = %+v, %v; want guarded no-op", duplicate, err)
	}
	repeat, err := lifecycleMutator().ProjectTermination(absenceFirst,
		TerminationProjectionInput{PaneUID: lifecyclePaneUID, ObservedAt: lifecycleClock})
	if err != nil || repeat.Changed {
		t.Fatalf("repeat projection = %+v, %v; want no-op", repeat, err)
	}
	if !reflect.DeepEqual(absenceFirst.Normalize(), settled) {
		t.Fatal("repeat receipt/projection changed the settled registry")
	}
}

// TestARepeatProjectionIsWriteFree is the other half of acceptance criterion 2.
//
// The pane-exit hooks fire on every pane exit in every session, so a
// disappearance that has already been reconciled must cost nothing at all. This
// asserts the two independent properties that make that true: the dirty check
// stops selecting the Pane, and running the projection anyway leaves the registry
// byte-identical.
func TestARepeatProjectionIsWriteFree(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		paneUID string
		record  func(*testing.T, *Registry)
	}{
		{
			name:    "an agent pane with a supervised receipt",
			paneUID: lifecyclePaneUID,
			record: func(t *testing.T, registry *Registry) {
				recordSupervisorExit(t, registry, lifecyclePaneUID, lifecycleAgentUID, 0, "")
			},
		},
		{
			name:    "an agent pane with no receipt at all",
			paneUID: lifecyclePaneUID,
			record:  func(*testing.T, *Registry) {},
		},
		{
			name:    "a shell pane whose runtime simply vanished",
			paneUID: lifecycleShellUID,
			record:  func(*testing.T, *Registry) {},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			registry := lifecycleFixture(t)
			test.record(t, registry)
			input := TerminationProjectionInput{PaneUID: test.paneUID, ObservedAt: lifecycleClock}
			if _, err := lifecycleMutator().ProjectTermination(registry, input); err != nil {
				t.Fatalf("first ProjectTermination: %v", err)
			}
			if NeedsTerminationProjection(*registry, test.paneUID) {
				t.Fatal("the dirty check still selects a Pane that has been fully reconciled")
			}

			settled := registry.Clone().Normalize()
			second, err := lifecycleMutator().ProjectTermination(registry, input)
			if err != nil {
				t.Fatalf("second ProjectTermination: %v", err)
			}
			if second.Changed {
				t.Fatalf("a repeat projection reported a change: %+v", second)
			}
			if !reflect.DeepEqual(registry.Normalize(), settled) {
				t.Fatal("a repeat projection rewrote the registry")
			}
		})
	}
}

// TestAStaleEventLeavesTheCurrentBindingAlone covers the generation guard on the
// event itself.
//
// A dirty event names one *materialization*. An event for the process a resume
// already replaced describes something that is no longer running in that Pane,
// and honoring it would release an Agent that is live right now.
func TestAStaleEventLeavesTheCurrentBindingAlone(t *testing.T) {
	t.Parallel()

	registry := lifecycleFixture(t)
	before := registry.Clone().Normalize()

	projection, err := lifecycleMutator().ProjectTermination(registry, TerminationProjectionInput{
		PaneUID: lifecyclePaneUID, Generation: "gen-replaced", ObservedAt: lifecycleClock,
	})
	if err != nil {
		t.Fatalf("ProjectTermination: %v", err)
	}
	if projection.Changed {
		t.Fatalf("a stale event changed the registry: %+v", projection)
	}
	if projection.Reason == "" {
		t.Fatal("a refused projection reported no reason")
	}
	agent, _ := registry.Agent(lifecycleAgentUID)
	if agent.Status.Phase != PhaseRunning || agent.Status.PaneRef != lifecyclePaneUID {
		t.Fatalf("agent status = %+v, want the current binding untouched", agent.Status)
	}
	if !reflect.DeepEqual(registry.Normalize(), before) {
		t.Fatal("a stale event wrote to the registry")
	}
}

// TestAReceiptForAPaneTheAgentNoLongerBindsIsEvidenceOnly covers the resume
// case.
//
// After a resume the Agent binds a new Pane while the old Pane resource can
// still be around long enough for its death to be observed. Ownership alone does
// not make that death the Agent's: only status.paneRef does, and applying the old
// process's exit to the new one would report a live Agent as offline.
func TestAReceiptForAPaneTheAgentNoLongerBindsIsEvidenceOnly(t *testing.T) {
	t.Parallel()

	registry := lifecycleFixture(t)
	// The resume moved the binding to a second managed Pane.
	registry.Panes = append(registry.Panes, Pane{
		APIVersion: APIVersion, Kind: KindPane,
		Metadata: ObjectMeta{
			UID: "pan-managed-2", Name: "codex-pane-2", CreatedAt: lifecycleClock,
			OwnerRef: &OwnerRef{Kind: KindAgent, UID: lifecycleAgentUID},
		},
		Spec:   PaneSpec{Role: PaneRoleAgent, CWD: "/srv/alpha"},
		Status: PaneStatus{Activation: PaneActivation{Generation: "gen-resumed", StartedAt: lifecycleClock}},
	})
	registry.NameReservations = append(registry.NameReservations,
		NameReservation{Scope: lifecycleAgentUID, Kind: KindPane, Name: "codex-pane-2", UID: "pan-managed-2"})
	agent, _ := registry.Agent(lifecycleAgentUID)
	agent.Status.PaneRef = "pan-managed-2"
	if err := registry.Validate(); err != nil {
		t.Fatalf("resumed fixture does not validate: %v", err)
	}

	projection, err := lifecycleMutator().ProjectTermination(registry,
		TerminationProjectionInput{PaneUID: lifecyclePaneUID, ObservedAt: lifecycleClock})
	if err != nil {
		t.Fatalf("ProjectTermination: %v", err)
	}
	if projection.AgentUID != "" || projection.Phase != "" {
		t.Fatalf("projection moved an Agent it does not bind: %+v", projection)
	}
	if !projection.PaneRetained {
		t.Fatal("the superseded Pane resource was released with an Agent that no longer binds it")
	}
	agent, _ = registry.Agent(lifecycleAgentUID)
	if agent.Status.Phase != PhaseRunning || agent.Status.PaneRef != "pan-managed-2" {
		t.Fatalf("agent status = %+v, want the resumed binding untouched", agent.Status)
	}
	stale, ok := registry.Pane(lifecyclePaneUID)
	if !ok {
		t.Fatal("the superseded Pane resource was deleted")
	}
	if stale.Status.LastTermination == nil ||
		stale.Status.LastTermination.Classification != TerminationUnknown {
		t.Fatalf("superseded pane evidence = %+v, want an unknown receipt", stale.Status.LastTermination)
	}
}

// TestAShellPaneSurvivesItsRuntimeAndRecordsWhy is the shell half of the
// contract.
//
// Runtime loss is not a statement about desired topology. A shell Pane whose
// tmux pane died keeps its logical existence, gains a MissingRuntime condition,
// and gains the evidence -- so it is offline for a stated reason instead of
// silently absent. Only a canonical delete removes the resource.
func TestAShellPaneSurvivesItsRuntimeAndRecordsWhy(t *testing.T) {
	t.Parallel()

	registry := lifecycleFixture(t)
	projection, err := lifecycleMutator().ProjectTermination(registry,
		TerminationProjectionInput{PaneUID: lifecycleShellUID, ObservedAt: lifecycleClock})
	if err != nil {
		t.Fatalf("ProjectTermination: %v", err)
	}
	if !projection.Changed || !projection.PaneRetained {
		t.Fatalf("projection = %+v, want a retained Pane with recorded evidence", projection)
	}
	if projection.AgentUID != "" {
		t.Fatalf("a shell Pane released Agent %q", projection.AgentUID)
	}
	pane, ok := registry.Pane(lifecycleShellUID)
	if !ok {
		t.Fatal("the logical shell Pane was deleted by a runtime disappearance")
	}
	if pane.Status.LastTermination == nil ||
		pane.Status.LastTermination.Classification != TerminationUnknown ||
		pane.Status.LastTermination.Source != TerminationSourceReconcile {
		t.Fatalf("shell evidence = %+v, want an unknown receipt from the reconciler", pane.Status.LastTermination)
	}
	if !hasCondition(pane.Status.Conditions, ConditionMissingRuntime) {
		t.Fatalf("conditions = %+v, want MissingRuntime recorded", pane.Status.Conditions)
	}
	if _, ok := registry.Window("win-main"); !ok {
		t.Fatal("the owning Window was removed")
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("the projected registry does not validate: %v", err)
	}
}

// TestAPaneWithNoActivationGenerationConvergesOnUnknown covers the adopted
// initial Pane.
//
// The pane tmux creates alongside a new session is not one projmux built an argv
// for, so it is not supervised and never receives a generation. Every Project
// session has one. Its disappearance therefore has no receipt to find, and the
// honest answer is unknown -- not normal, which would claim a clean exit nobody
// observed.
func TestAPaneWithNoActivationGenerationConvergesOnUnknown(t *testing.T) {
	t.Parallel()

	registry := lifecycleFixture(t)
	pane, _ := registry.Pane(lifecycleShellUID)
	pane.Status.Activation = PaneActivation{}
	if err := registry.Validate(); err != nil {
		t.Fatalf("adopted fixture does not validate: %v", err)
	}

	projection, err := lifecycleMutator().ProjectTermination(registry,
		TerminationProjectionInput{PaneUID: lifecycleShellUID, ObservedAt: lifecycleClock})
	if err != nil {
		t.Fatalf("ProjectTermination: %v", err)
	}
	if projection.Classification != TerminationUnknown || !projection.Changed {
		t.Fatalf("projection = %+v, want a recorded unknown termination", projection)
	}
	pane, ok := registry.Pane(lifecycleShellUID)
	if !ok {
		t.Fatal("an adopted Pane was deleted rather than recorded")
	}
	if pane.Status.LastTermination.Generation != "" {
		t.Fatalf("evidence generation = %q, want the empty generation it was launched with",
			pane.Status.LastTermination.Generation)
	}
	// The empty generation is not a wildcard: it matched because the Pane holds
	// it, and a repeat pass still finds nothing to do.
	if NeedsTerminationProjection(*registry, lifecycleShellUID) {
		t.Fatal("a generation-less Pane is re-projected forever")
	}
}

// TestRecordedIntentIsNotOverwrittenByTheObservationItCaused is the sticky-intent
// rule read through the projection.
//
// A canonical delete records intent and then kills the pane; the supervisor
// watching that pane reports a signal death. If the observation won, every
// deliberate deletion would be filed as a crash and the Agent would land in
// Failed.
func TestRecordedIntentIsNotOverwrittenByTheObservationItCaused(t *testing.T) {
	t.Parallel()

	registry := lifecycleFixture(t)
	recordIntent(t, registry, lifecyclePaneUID, lifecycleAgentUID, "op-delete")
	recordSupervisorExit := func() {
		outcome, err := lifecycleMutator().RecordTermination(registry, TerminationEvidence{
			Source:         TerminationSourceSupervisor,
			Classification: TerminationAbnormal,
			ObservedAt:     lifecycleClock,
			PaneUID:        lifecyclePaneUID,
			AgentUID:       lifecycleAgentUID,
			Generation:     lifecycleGeneration,
			Signal:         "SIGTERM",
		})
		if err != nil {
			t.Fatalf("RecordTermination: %v", err)
		}
		if outcome.Applied {
			t.Fatal("a signal death overwrote recorded intent")
		}
	}
	recordSupervisorExit()

	projection, err := lifecycleMutator().ProjectTermination(registry,
		TerminationProjectionInput{PaneUID: lifecyclePaneUID, ObservedAt: lifecycleClock})
	if err != nil {
		t.Fatalf("ProjectTermination: %v", err)
	}
	if projection.Classification != TerminationIntentional || projection.Phase != PhaseOffline {
		t.Fatalf("projection = %+v, want an intentional Offline transition", projection)
	}
	agent, _ := registry.Agent(lifecycleAgentUID)
	if agent.Status.Reason != TerminationReasonIntentional {
		t.Fatalf("reason = %q, want %q", agent.Status.Reason, TerminationReasonIntentional)
	}
}

// TestOnlyTheReconcilerMayClaimUnknown pins the source/classification pairing.
//
// The two refused pairings are the two that would let a writer claim knowledge it
// does not have: a supervisor filing "nobody knows" could erase the wait status
// it actually read, and anything but a control action filing "intentional" would
// manufacture an operator's purpose.
func TestOnlyTheReconcilerMayClaimUnknown(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name           string
		source         TerminationSource
		classification TerminationClassification
		wantAccepted   bool
	}{
		{"the reconciler may record unknown", TerminationSourceReconcile, TerminationUnknown, true},
		{"a supervisor may not record unknown", TerminationSourceSupervisor, TerminationUnknown, false},
		{"a control action may not record unknown", TerminationSourceControlAction, TerminationUnknown, false},
		{"the reconciler may not record normal", TerminationSourceReconcile, TerminationNormal, false},
		{"the reconciler may not record abnormal", TerminationSourceReconcile, TerminationAbnormal, false},
		{"the reconciler may not record killed", TerminationSourceReconcile, TerminationKilled, false},
		{"the reconciler may not record intentional", TerminationSourceReconcile, TerminationIntentional, false},
		{"a control action still records intentional", TerminationSourceControlAction, TerminationIntentional, true},
		{"a supervisor still records normal", TerminationSourceSupervisor, TerminationNormal, true},
		{"a supervisor still records abnormal", TerminationSourceSupervisor, TerminationAbnormal, true},
		{"a control action may not record normal", TerminationSourceControlAction, TerminationNormal, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := validTerminationPairing(test.source, test.classification); got != test.wantAccepted {
				t.Fatalf("validTerminationPairing(%q, %q) = %v, want %v",
					test.source, test.classification, got, test.wantAccepted)
			}

			registry := lifecycleFixture(t)
			_, err := lifecycleMutator().RecordTermination(registry, TerminationEvidence{
				Source:         test.source,
				Classification: test.classification,
				PaneUID:        lifecyclePaneUID,
				Generation:     lifecycleGeneration,
			})
			if test.wantAccepted {
				if err != nil {
					t.Fatalf("RecordTermination = %v, want the pairing accepted", err)
				}
				return
			}
			if err == nil {
				t.Fatal("RecordTermination accepted a refused pairing")
			}

			// A registry that already stores the refused pairing is refused too,
			// so the guard cannot be bypassed by hand-editing the file.
			pane, _ := registry.Pane(lifecyclePaneUID)
			pane.Status.LastTermination = &TerminationEvidence{
				Source:         test.source,
				Classification: test.classification,
				PaneUID:        lifecyclePaneUID,
				Generation:     lifecycleGeneration,
			}
			if err := registry.Validate(); err == nil {
				t.Fatal("Validate accepted a stored refused pairing")
			}
		})
	}
}

// TestTheSummaryRendersEveryEvidenceShape pins the one compact clause the
// columnar reads and the Registry-first UI share.
func TestTheSummaryRendersEveryEvidenceShape(t *testing.T) {
	t.Parallel()

	code := func(value int) *int { return &value }
	for _, test := range []struct {
		name    string
		receipt *TerminationEvidence
		want    string
	}{
		{name: "no receipt renders nothing", receipt: nil, want: ""},
		{name: "an empty receipt renders nothing", receipt: &TerminationEvidence{}, want: ""},
		{
			name: "an external kill renders its signal",
			receipt: &TerminationEvidence{
				Source: TerminationSourceSupervisor, Classification: TerminationKilled, Signal: "HUP",
			},
			want: "killed/supervisor signal=HUP",
		},
		{
			name:    "an intentional receipt names its control action",
			receipt: &TerminationEvidence{Source: TerminationSourceControlAction, Classification: TerminationIntentional},
			want:    "intentional/control-action",
		},
		{
			name: "a clean exit renders its status",
			receipt: &TerminationEvidence{
				Source: TerminationSourceSupervisor, Classification: TerminationNormal, ExitCode: code(0),
			},
			want: "normal/supervisor exit=0",
		},
		{
			name: "a non-zero exit renders its code",
			receipt: &TerminationEvidence{
				Source: TerminationSourceSupervisor, Classification: TerminationAbnormal, ExitCode: code(42),
			},
			want: "abnormal/supervisor exit=42",
		},
		{
			name: "a signal wins over any code reported beside it",
			receipt: &TerminationEvidence{
				Source: TerminationSourceSupervisor, Classification: TerminationAbnormal,
				ExitCode: code(0), Signal: "SIGKILL",
			},
			want: "abnormal/supervisor signal=SIGKILL",
		},
		{
			name:    "an unknown receipt names the reconciler and no status",
			receipt: &TerminationEvidence{Source: TerminationSourceReconcile, Classification: TerminationUnknown},
			want:    "unknown/reconcile",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := test.receipt.Summary(); got != test.want {
				t.Fatalf("Summary() = %q, want %q", got, test.want)
			}
		})
	}
}

// TestTheProjectionNeverStartsAnything is the negative audit at the core layer.
//
// The projection is handed a registry and nothing else: no runner, no clock but
// the mutator's, no filesystem. This states the consequence in terms of the
// registry itself -- an Offline Agent stays Offline with no paneRef, and no Pane
// resource is created for it -- so a later change that made the projection
// materialize a replacement would fail here rather than in an e2e.
func TestTheProjectionNeverStartsAnything(t *testing.T) {
	t.Parallel()

	registry := lifecycleFixture(t)
	if _, err := lifecycleMutator().ProjectTermination(registry,
		TerminationProjectionInput{PaneUID: lifecyclePaneUID, ObservedAt: lifecycleClock}); err != nil {
		t.Fatalf("ProjectTermination: %v", err)
	}
	settled := registry.Clone().Normalize()

	// A second pass over an Offline Agent with no Pane at all.
	for _, paneUID := range []string{lifecyclePaneUID, "pan-managed-gone"} {
		if _, err := lifecycleMutator().ProjectTermination(registry,
			TerminationProjectionInput{PaneUID: paneUID, ObservedAt: lifecycleClock}); err != nil {
			t.Fatalf("ProjectTermination(%s): %v", paneUID, err)
		}
	}
	if !reflect.DeepEqual(registry.Normalize(), settled) {
		t.Fatal("projecting an already-offline Agent changed the registry")
	}
	agent, _ := registry.Agent(lifecycleAgentUID)
	if agent.Status.Phase != PhaseOffline || agent.Status.PaneRef != "" {
		t.Fatalf("agent status = %+v, want it left Offline and unbound", agent.Status)
	}
	panes := registry.PanesOf(lifecycleAgentUID)
	if len(panes) != 1 || panes[0].Metadata.UID != lifecyclePaneUID {
		t.Fatalf("projection changed the preserved Pane row: %+v", panes)
	}
}

// TestAStaleStoredIntentIsProjectedAsWhatTheRegistrySays covers the one case
// where the offered receipt loses and the stored one wins.
//
// A registry whose stored receipt names a generation the Pane no longer holds is
// unreachable through the mutators -- RecordPaneActivation clears the old receipt
// and RecordTermination refuses to write a mismatched one -- so this is the
// hand-edited or downgraded file. The projection still has to be coherent: it
// reports the classification the document ends up holding, not the one it
// offered and had refused.
func TestAStaleStoredIntentIsProjectedAsWhatTheRegistrySays(t *testing.T) {
	t.Parallel()

	registry := lifecycleFixture(t)
	pane, _ := registry.Pane(lifecyclePaneUID)
	pane.Status.LastTermination = &TerminationEvidence{
		Source:         TerminationSourceControlAction,
		Classification: TerminationIntentional,
		ObservedAt:     lifecycleClock,
		PaneUID:        lifecyclePaneUID,
		Generation:     "gen-replaced",
		OperationID:    "op-older-delete",
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("hand-edited fixture does not validate: %v", err)
	}

	projection, err := lifecycleMutator().ProjectTermination(registry,
		TerminationProjectionInput{PaneUID: lifecyclePaneUID, ObservedAt: lifecycleClock})
	if err != nil {
		t.Fatalf("ProjectTermination: %v", err)
	}
	// Sticky intent refused the unknown receipt, so the stored document is
	// unchanged and the projection says so.
	if projection.Classification != TerminationIntentional {
		t.Fatalf("classification = %q, want the stored intent the registry kept", projection.Classification)
	}
	agent, _ := registry.Agent(lifecycleAgentUID)
	if agent.Status.Phase != PhaseOffline || agent.Status.Reason != TerminationReasonIntentional {
		t.Fatalf("agent status = %+v, want Offline for the intentional clause", agent.Status)
	}
}
