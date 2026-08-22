package metadata

import (
	"errors"
	"reflect"
	"testing"
)

func TestRenameAgentChangesOnlyStableNameAndReservation(t *testing.T) {
	t.Parallel()

	m := testMutator(dirSet{"/src/projmux": true})
	reg := NewRegistry()
	registered, err := registerFixture(m, &reg, "/src/projmux")
	if err != nil {
		t.Fatal(err)
	}
	agent, err := m.CreateAgent(&reg, registered.Windows[0].Metadata.UID, CreateAgentOptions{
		Provider: "codex", Annotations: map[string]string{AnnotationAgentTopic: "keep-topic"}, OperationID: "op-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	pane, err := m.AttachAgentPane(&reg, agent.Metadata.UID, BootstrapPane{Command: "codex", CWD: "/src/projmux"}, "op-agent")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := reg.Agent(agent.Metadata.UID)
	paneBefore, _ := reg.Pane(pane.Metadata.UID)

	renamed, err := m.RenameAgent(&reg, agent.Metadata.UID, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Metadata.Name != "reviewer" || renamed.Metadata.UID != before.Metadata.UID {
		t.Fatalf("renamed identity = %+v", renamed.Metadata)
	}
	if renamed.Spec.Provider != before.Spec.Provider ||
		!reflect.DeepEqual(renamed.Metadata.Annotations, before.Metadata.Annotations) ||
		!reflect.DeepEqual(renamed.Status, before.Status) {
		t.Fatalf("rename changed provider/topic/status: before=%+v after=%+v", before, renamed)
	}
	paneAfter, _ := reg.Pane(pane.Metadata.UID)
	if !reflect.DeepEqual(paneAfter, paneBefore) {
		t.Fatalf("rename changed managed Pane: before=%+v after=%+v", paneBefore, paneAfter)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("registry invalid after rename: %v", err)
	}
}

func TestAgentPhaseSetIsClosedAndExitClassificationResolvesOfflineOrFailed(t *testing.T) {
	t.Parallel()

	if got := AgentPhases(); len(got) != 4 {
		t.Fatalf("phases = %v, want exactly Pending/Running/Offline/Failed", got)
	}
	tests := []struct {
		name      string
		exit      AgentExit
		wantPhase AgentPhase
		wantOK    bool
	}{
		{name: "normal managed pane exit is offline", exit: AgentExitNormal, wantPhase: PhaseOffline, wantOK: true},
		{name: "explicit pane deletion is offline", exit: AgentExitDeleted, wantPhase: PhaseOffline, wantOK: true},
		{name: "abnormal exit is failed", exit: AgentExitAbnormal, wantPhase: PhaseFailed, wantOK: true},
		{name: "launch failure is failed", exit: AgentExitLaunchFailure, wantPhase: PhaseFailed, wantOK: true},
		{name: "unknown exit is rejected", exit: AgentExit("weird"), wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			phase, ok := tt.exit.Phase()
			if ok != tt.wantOK || (tt.wantOK && phase != tt.wantPhase) {
				t.Fatalf("Phase() = %q,%v want %q,%v", phase, ok, tt.wantPhase, tt.wantOK)
			}
		})
	}
}

func TestAgentTransitionTableAcceptsResumeAndRejectsUnknownPhases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		from AgentPhase
		to   AgentPhase
		want bool
	}{
		{name: "pending starts running", from: PhasePending, to: PhaseRunning, want: true},
		{name: "pending fails to launch", from: PhasePending, to: PhaseFailed, want: true},
		{name: "running exits normally", from: PhaseRunning, to: PhaseOffline, want: true},
		{name: "running exits abnormally", from: PhaseRunning, to: PhaseFailed, want: true},
		{name: "running cannot go back to pending", from: PhaseRunning, to: PhasePending, want: false},
		{name: "offline resumes", from: PhaseOffline, to: PhaseRunning, want: true},
		{name: "failed resumes", from: PhaseFailed, to: PhaseRunning, want: true},
		{name: "unknown source phase is rejected", from: AgentPhase("Zombie"), to: PhaseRunning, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := CanTransitionAgent(tt.from, tt.to); got != tt.want {
				t.Fatalf("CanTransitionAgent(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}

func TestAgentOwnsItsManagedPaneAndSurvivesTheManagedPaneExit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		exit      AgentExit
		wantPhase AgentPhase
	}{
		{name: "normal exit leaves a resumable offline agent", exit: AgentExitNormal, wantPhase: PhaseOffline},
		{name: "abnormal exit leaves a failed agent", exit: AgentExitAbnormal, wantPhase: PhaseFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			roots := dirSet{"/src/projmux": true}
			m := testMutator(roots)
			reg := NewRegistry()
			registered, err := registerFixture(m, &reg, "/src/projmux")
			if err != nil {
				t.Fatalf("register: %v", err)
			}
			windowUID := registered.Windows[0].Metadata.UID

			agent, err := m.CreateAgent(&reg, windowUID, CreateAgentOptions{Provider: "codex", OperationID: "op-agent"})
			if err != nil {
				t.Fatalf("create agent: %v", err)
			}
			if agent.Metadata.Name != "codex" || agent.Status.Phase != PhasePending {
				t.Fatalf("agent = %q/%q, want codex/Pending", agent.Metadata.Name, agent.Status.Phase)
			}
			if agent.Metadata.OwnerRef == nil || agent.Metadata.OwnerRef.Kind != KindWindow || agent.Metadata.OwnerRef.UID != windowUID {
				t.Fatalf("agent ownerRef = %+v, want the window", agent.Metadata.OwnerRef)
			}

			pane, err := m.AttachAgentPane(&reg, agent.Metadata.UID, BootstrapPane{Command: "codex", CWD: "/src/projmux"}, "op-agent")
			if err != nil {
				t.Fatalf("attach managed pane: %v", err)
			}
			if pane.Metadata.Name != "codex-pane" {
				t.Fatalf("managed pane name = %q, want codex-pane", pane.Metadata.Name)
			}
			if pane.Metadata.OwnerRef.Kind != KindAgent || pane.Metadata.OwnerRef.UID != agent.Metadata.UID {
				t.Fatalf("managed pane ownerRef = %+v, want the agent", pane.Metadata.OwnerRef)
			}
			running, _ := reg.Agent(agent.Metadata.UID)
			if running.Status.Phase != PhaseRunning || running.Status.PaneRef != pane.Metadata.UID {
				t.Fatalf("running agent = %+v", running.Status)
			}
			if err := reg.Validate(); err != nil {
				t.Fatalf("registry invalid with a running agent: %v", err)
			}

			released, err := m.ReleaseAgentPane(&reg, agent.Metadata.UID, tt.exit, "test")
			if err != nil {
				t.Fatalf("release: %v", err)
			}
			if released.Status.Phase != tt.wantPhase || released.Status.PaneRef != "" {
				t.Fatalf("released agent = %+v, want %q with no paneRef", released.Status, tt.wantPhase)
			}
			if _, ok := reg.Agent(agent.Metadata.UID); !ok {
				t.Fatal("the agent must survive its pane")
			}
			if _, ok := reg.Pane(pane.Metadata.UID); ok {
				t.Fatal("the managed pane resource must be removed")
			}
			if err := reg.Validate(); err != nil {
				t.Fatalf("registry invalid after release: %v", err)
			}

			// The freed managed-pane name is available again for a resume.
			resumed, err := m.AttachAgentPane(&reg, agent.Metadata.UID, BootstrapPane{Command: "codex", CWD: "/src/projmux"}, "op-resume")
			if err != nil {
				t.Fatalf("resume: %v", err)
			}
			if resumed.Metadata.Name != "codex-pane" {
				t.Fatalf("resumed pane name = %q, want codex-pane", resumed.Metadata.Name)
			}
		})
	}
}

func TestDeletingAManagedPaneMovesItsAgentOffline(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/src/projmux": true}
	m := testMutator(roots)
	reg := NewRegistry()
	registered, err := registerFixture(m, &reg, "/src/projmux")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	agent, err := m.CreateAgent(&reg, registered.Windows[0].Metadata.UID, CreateAgentOptions{Provider: "claude"})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	pane, err := m.AttachAgentPane(&reg, agent.Metadata.UID, BootstrapPane{CWD: "/src/projmux"}, "op")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	if err := m.DeletePane(&reg, pane.Metadata.UID); err != nil {
		t.Fatalf("delete pane: %v", err)
	}
	stored, ok := reg.Agent(agent.Metadata.UID)
	if !ok {
		t.Fatal("the agent must survive an explicit pane deletion")
	}
	if stored.Status.Phase != PhaseOffline {
		t.Fatalf("phase = %q, want Offline", stored.Status.Phase)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("registry invalid after pane deletion: %v", err)
	}
}

func TestDeletingRetainedManagedPanePreservesCurrentAgentBinding(t *testing.T) {
	t.Parallel()

	m := testMutator(dirSet{"/src/projmux": true})
	reg := NewRegistry()
	registered, err := registerFixture(m, &reg, "/src/projmux")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	agent, err := m.CreateAgent(&reg, registered.Windows[0].Metadata.UID, CreateAgentOptions{Provider: "codex"})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}
	retained, err := m.AttachAgentPane(&reg, agent.Metadata.UID, BootstrapPane{CWD: "/src/projmux"}, "op-old")
	if err != nil {
		t.Fatalf("attach retained pane: %v", err)
	}

	current := retained.Clone()
	current.Metadata.UID = "pane-current-generation"
	current.Metadata.Name = "codex-pane-current"
	current.Status.Activation.Generation = "generation-current"
	reg.Panes = append(reg.Panes, current)
	reg.NameReservations = append(reg.NameReservations, NameReservation{
		Scope: agent.Metadata.UID, Kind: KindPane, Name: current.Metadata.Name, UID: current.Metadata.UID,
	})
	stored, _ := reg.Agent(agent.Metadata.UID)
	stored.Status.Phase = PhaseRunning
	stored.Status.PaneRef = current.Metadata.UID
	if err := reg.Validate(); err != nil {
		t.Fatalf("multi-generation fixture: %v", err)
	}

	if err := m.DeletePane(&reg, retained.Metadata.UID); err != nil {
		t.Fatalf("delete retained pane: %v", err)
	}
	stored, ok := reg.Agent(agent.Metadata.UID)
	if !ok || stored.Status.Phase != PhaseRunning || stored.Status.PaneRef != current.Metadata.UID {
		t.Fatalf("current Agent binding changed: %#v", stored)
	}
	if _, ok := reg.Pane(current.Metadata.UID); !ok {
		t.Fatal("current Pane generation was removed")
	}
	if _, ok := reg.Pane(retained.Metadata.UID); ok {
		t.Fatal("retained Pane was not removed")
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("registry invalid after retained pane deletion: %v", err)
	}
}

func TestAgentTransitionRejectsUnsupportedPhasesAsUsageErrors(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/src/projmux": true}
	m := testMutator(roots)
	reg := NewRegistry()
	registered, err := registerFixture(m, &reg, "/src/projmux")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	agent, err := m.CreateAgent(&reg, registered.Windows[0].Metadata.UID, CreateAgentOptions{Provider: "codex"})
	if err != nil {
		t.Fatalf("create agent: %v", err)
	}

	if _, err := m.TransitionAgent(&reg, agent.Metadata.UID, AgentPhase("Zombie"), ""); !errors.Is(err, ErrInvalidPhase) || !IsUsageError(err) {
		t.Fatalf("unsupported phase error = %v", err)
	}
	if _, err := m.TransitionAgent(&reg, agent.Metadata.UID, PhaseOffline, "manual"); err != nil {
		t.Fatalf("pending -> offline: %v", err)
	}
	stored, _ := reg.Agent(agent.Metadata.UID)
	if stored.Status.Phase != PhaseOffline || stored.Status.Reason != "manual" {
		t.Fatalf("status = %+v", stored.Status)
	}
}

func TestDuplicateAgentsInOneWindowGetTheLowestFreeSuffix(t *testing.T) {
	t.Parallel()

	roots := dirSet{"/src/projmux": true}
	m := testMutator(roots)
	reg := NewRegistry()
	registered, err := registerFixture(m, &reg, "/src/projmux")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	windowUID := registered.Windows[0].Metadata.UID

	var names []string
	for range 3 {
		agent, err := m.CreateAgent(&reg, windowUID, CreateAgentOptions{Provider: "codex"})
		if err != nil {
			t.Fatalf("create agent: %v", err)
		}
		names = append(names, agent.Metadata.Name)
	}
	if !equalStrings(names, []string{"codex", "codex-1", "codex-2"}) {
		t.Fatalf("names = %v, want codex/codex-1/codex-2", names)
	}
}
