package metadata

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/sessionstate"
)

// TestWindowAnchorEligibilityPredicateBranches pins every clause of the one
// anchor predicate Validate and the anchor writers share.
func TestWindowAnchorEligibilityPredicateBranches(t *testing.T) {
	t.Parallel()
	fixture := newAnchorSchemaFixture(t)
	tests := []struct {
		name   string
		window string
		pane   string
		mutate func(*Registry, *Pane)
		want   windowAnchorVerdict
	}{
		{name: "same-Window direct shell", window: fixture.windowUID, pane: fixture.shellUID, want: windowAnchorEligible},
		{name: "same-Window managed Agent Pane", window: fixture.windowUID, pane: fixture.agentPaneUID, want: windowAnchorEligible},
		{name: "membership: cross-Window shell", window: fixture.windowUID, pane: fixture.otherShellUID, want: windowAnchorNotSameWindowShellOrAgent},
		{name: "membership: cross-Window Agent Pane", window: fixture.otherWindowUID, pane: fixture.agentPaneUID, want: windowAnchorNotSameWindowShellOrAgent},
		{name: "membership: no ownerRef", window: fixture.windowUID, pane: fixture.shellUID, mutate: func(_ *Registry, p *Pane) {
			p.Metadata.OwnerRef = nil
		}, want: windowAnchorNotSameWindowShellOrAgent},
		{name: "membership: missing owner Agent", window: fixture.windowUID, pane: fixture.agentPaneUID, mutate: func(_ *Registry, p *Pane) {
			p.Metadata.OwnerRef = &OwnerRef{Kind: KindAgent, UID: "agent-missing"}
		}, want: windowAnchorNotSameWindowShellOrAgent},
		{name: "role: unsupported same-Window role", window: fixture.windowUID, pane: fixture.shellUID, mutate: func(_ *Registry, p *Pane) {
			p.Spec.Role = PaneRole("bogus")
		}, want: windowAnchorNotSameWindowShellOrAgent},
		{name: "managed: released Agent Pane", window: fixture.windowUID, pane: fixture.agentPaneUID, mutate: func(r *Registry, _ *Pane) {
			agent, _ := r.Agent(fixture.agentUID)
			agent.Status.Phase = PhaseOffline
			agent.Status.PaneRef = ""
		}, want: windowAnchorNotManagedAgentPane},
		{name: "managed: superseded Agent Pane", window: fixture.windowUID, pane: fixture.agentPaneUID, mutate: func(r *Registry, _ *Pane) {
			agent, _ := r.Agent(fixture.agentUID)
			agent.Status.PaneRef = "pane-newer-generation"
		}, want: windowAnchorNotManagedAgentPane},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reg := fixture.registry.Clone()
			pane, ok := reg.Pane(tt.pane)
			if !ok {
				t.Fatalf("fixture Pane %q missing", tt.pane)
			}
			if tt.mutate != nil {
				tt.mutate(&reg, pane)
			}
			if got := windowAnchorEligibility(reg, tt.window, *pane); got != tt.want {
				t.Fatalf("windowAnchorEligibility = %d, want %d", got, tt.want)
			}
			if got := reg.windowAnchorRefEligible(tt.window, tt.pane); got != (tt.want == windowAnchorEligible) {
				t.Fatalf("windowAnchorRefEligible = %t, want %t", got, tt.want == windowAnchorEligible)
			}
		})
	}
	if fixture.registry.windowAnchorRefEligible(fixture.windowUID, "pane-missing") {
		t.Fatal("a missing Pane was admitted as an anchor")
	}
}

// anchorWriterFixture registers one Project whose first Window owns shell S0
// as both anchor and default shell.
func anchorWriterFixture(t *testing.T) (Mutator, Registry, string, string) {
	t.Helper()
	m := testMutator(dirSet{"/src/projmux": true})
	reg := NewRegistry()
	registered, err := registerFixture(m, &reg, "/src/projmux")
	if err != nil {
		t.Fatal(err)
	}
	return m, reg, registered.Windows[0].Metadata.UID, registered.Panes[0].Metadata.UID
}

func attachFixtureAgent(t *testing.T, m Mutator, reg *Registry, windowUID, operationID string) (string, string) {
	t.Helper()
	agent, err := m.CreateAgent(reg, windowUID, CreateAgentOptions{Provider: "codex", OperationID: operationID})
	if err != nil {
		t.Fatal(err)
	}
	pane, err := m.AttachAgentPane(reg, agent.Metadata.UID, BootstrapPane{CWD: "/src/projmux"}, operationID+"-attach")
	if err != nil {
		t.Fatal(err)
	}
	return agent.Metadata.UID, pane.Metadata.UID
}

// Acceptance 1: delete reselection skips a released Agent Pane listed first.
func TestDeletePaneReanchorSkipsReleasedAgentPaneForEligibleShell(t *testing.T) {
	t.Parallel()
	m, reg, windowUID, firstShell := anchorWriterFixture(t)
	agentUID, deadPane := attachFixtureAgent(t, m, &reg, windowUID, "dead-agent")
	if _, err := m.TransitionAgent(&reg, agentUID, PhaseOffline, "exited"); err != nil {
		t.Fatal(err)
	}
	shell, err := m.AddPane(&reg, windowUID, BootstrapPane{}, "/bin/zsh", "second-shell")
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent(agentUID)
	order := paneOrder(reg)
	if agent.Status.PaneRef != "" || !slices.Equal(order, []string{firstShell, deadPane, shell.Metadata.UID}) {
		t.Fatalf("fixture agent paneRef=%q order=%v", agent.Status.PaneRef, order)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	if err := m.DeletePane(&reg, firstShell); err != nil {
		t.Fatalf("delete anchor: %v", err)
	}
	window, _ := reg.Window(windowUID)
	if window.Spec.AnchorPaneRef != shell.Metadata.UID {
		t.Fatalf("anchorPaneRef = %q, want eligible shell %q (dead Agent Pane %q skipped)", window.Spec.AnchorPaneRef, shell.Metadata.UID, deadPane)
	}
	if _, ok := reg.Pane(deadPane); !ok {
		t.Fatal("ineligible Agent Pane row was removed")
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("post delete: %v", err)
	}
}

// Acceptance 2: termination release keeps paneRef empty and moves the anchor.
func TestTransitionAgentReleasingAnchorPaneMovesAnchorToEligiblePane(t *testing.T) {
	t.Parallel()
	for _, phase := range []AgentPhase{PhaseOffline, PhaseFailed} {
		t.Run(string(phase), func(t *testing.T) {
			t.Parallel()
			m, reg, windowUID, shellUID := anchorWriterFixture(t)
			agentUID, agentPane := attachFixtureAgent(t, m, &reg, windowUID, "anchor-agent")
			window, _ := reg.Window(windowUID)
			window.Spec.AnchorPaneRef = agentPane
			if err := reg.Validate(); err != nil {
				t.Fatalf("fixture: %v", err)
			}

			released, err := m.TransitionAgent(&reg, agentUID, phase, "terminated")
			if err != nil {
				t.Fatalf("transition: %v", err)
			}
			window, _ = reg.Window(windowUID)
			if released.Status.Phase != phase || released.Status.PaneRef != "" {
				t.Fatalf("released Agent = %+v, want %s with empty paneRef", released.Status, phase)
			}
			if window.Spec.AnchorPaneRef != shellUID || window.Spec.DefaultShellPaneRef != shellUID {
				t.Fatalf("Window refs = anchor %q default %q, want %q", window.Spec.AnchorPaneRef, window.Spec.DefaultShellPaneRef, shellUID)
			}
			if _, ok := reg.Pane(agentPane); !ok {
				t.Fatal("released Agent Pane row was not retained")
			}
			if err := reg.Validate(); err != nil {
				t.Fatalf("post transition: %v", err)
			}
			// A subsequent unrelated Registry write still commits.
			project := reg.Projects[0]
			if _, _, err := m.AddWindow(&reg, project.Metadata.UID, BootstrapWindow{Name: "later"}, "/bin/zsh", "unrelated"); err != nil {
				t.Fatalf("unrelated write: %v", err)
			}
			if err := reg.Validate(); err != nil {
				t.Fatalf("post unrelated write: %v", err)
			}
		})
	}
}

// Acceptance 3: no eligible candidate lands the anchor on a new default shell
// and preserves every ineligible Pane row.
func TestAnchorWritersCloseOnNewDefaultShellWhenNoPaneIsEligible(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		run  func(*testing.T, Mutator, *Registry, string, string) []string
	}{
		{name: "delete pane", run: func(t *testing.T, m Mutator, reg *Registry, windowUID, shellUID string) []string {
			agentUID, deadPane := attachFixtureAgent(t, m, reg, windowUID, "dead-agent")
			if _, err := m.TransitionAgent(reg, agentUID, PhaseOffline, "exited"); err != nil {
				t.Fatal(err)
			}
			if err := reg.Validate(); err != nil {
				t.Fatalf("fixture: %v", err)
			}
			if err := m.DeletePane(reg, shellUID); err != nil {
				t.Fatalf("delete last eligible Pane: %v", err)
			}
			return []string{deadPane}
		}},
		{name: "termination release", run: func(t *testing.T, m Mutator, reg *Registry, windowUID, shellUID string) []string {
			anchorAgent, anchorPane := attachFixtureAgent(t, m, reg, windowUID, "anchor-agent")
			deadAgent, deadPane := attachFixtureAgent(t, m, reg, windowUID, "dead-agent")
			if _, err := m.TransitionAgent(reg, deadAgent, PhaseFailed, "abnormal"); err != nil {
				t.Fatal(err)
			}
			if err := m.DeletePane(reg, shellUID); err != nil {
				t.Fatal(err)
			}
			if window, _ := reg.Window(windowUID); window.Spec.AnchorPaneRef != anchorPane {
				t.Fatalf("fixture anchor = %q, want Agent-only anchor %q", window.Spec.AnchorPaneRef, anchorPane)
			}
			if err := reg.Validate(); err != nil {
				t.Fatalf("fixture: %v", err)
			}
			released, err := m.TransitionAgent(reg, anchorAgent, PhaseOffline, "exited")
			if err != nil {
				t.Fatalf("transition: %v", err)
			}
			if released.Status.PaneRef != "" {
				t.Fatalf("released paneRef = %q, want empty", released.Status.PaneRef)
			}
			return []string{anchorPane, deadPane}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, reg, windowUID, shellUID := anchorWriterFixture(t)
			preserved := tt.run(t, m, &reg, windowUID, shellUID)
			window, _ := reg.Window(windowUID)
			anchor, ok := reg.Pane(window.Spec.AnchorPaneRef)
			if !ok || anchor.Spec.Role != PaneRoleShell || anchor.Metadata.OwnerRef == nil ||
				anchor.Metadata.OwnerRef.Kind != KindWindow || anchor.Metadata.OwnerUID() != windowUID ||
				slices.Contains(preserved, anchor.Metadata.UID) || anchor.Metadata.UID == shellUID {
				t.Fatalf("anchor = %+v, want a new direct Window shell", anchor)
			}
			if window.Spec.DefaultShellPaneRef != anchor.Metadata.UID {
				t.Fatalf("defaultShellPaneRef = %q, want new shell %q", window.Spec.DefaultShellPaneRef, anchor.Metadata.UID)
			}
			for _, uid := range preserved {
				if _, ok := reg.Pane(uid); !ok {
					t.Fatalf("ineligible Pane row %q was removed", uid)
				}
			}
			if err := reg.Validate(); err != nil {
				t.Fatalf("post close: %v", err)
			}
		})
	}
}

// Acceptance 4: rebind moves an anchor on the previous binding Pane onto the
// new managed Pane, including the snapshot-projection Offline binding shape.
func TestAttachAgentPaneMovesAnchorFromPreviousBindingToNewManagedPane(t *testing.T) {
	t.Parallel()
	t.Run("snapshot projection Offline binding", func(t *testing.T) {
		t.Parallel()
		reg, targetUID, _, _ := projectionFixture(t)
		project, _ := reg.Project(targetUID)
		window, _ := reg.Window(project.Spec.PrimaryWindowRef)
		agents := reg.AgentsOf(window.Metadata.UID)
		if len(agents) != 1 {
			t.Fatalf("fixture Agents=%d, want 1", len(agents))
		}
		window.Spec.AnchorPaneRef = agents[0].Status.PaneRef
		snap := buildCurrentSnapshot(&reg, targetUID, "one")
		for wi := range snap.Windows {
			for pi := range snap.Windows[wi].Panes {
				if meta := snap.Windows[wi].Panes[pi].Metadata; meta != nil && meta.OwnerKind == string(KindAgent) {
					snap.Windows[wi].Panes[pi].Recipe = sessionstate.AgentRecipe("codex", "projection-thread", "projection")
				}
			}
		}
		projected, err := PlanSnapshotProjection(reg, targetUID, snap, fixedNow.Add(time.Minute), sequentialUIDs())
		if err != nil {
			t.Fatal(err)
		}
		desired := projected.Desired
		desiredWindow, ok := desired.Window(window.Metadata.UID)
		if !ok {
			t.Fatalf("projected Window %q missing", window.Metadata.UID)
		}
		desiredAgents := desired.AgentsOf(desiredWindow.Metadata.UID)
		if len(desiredAgents) != 1 || desiredAgents[0].Status.Phase != PhaseOffline ||
			desiredAgents[0].Status.PaneRef == "" || desiredAgents[0].Status.PaneRef != desiredWindow.Spec.AnchorPaneRef {
			t.Fatalf("projection shape = agents:%+v anchor:%q, want an Offline Agent bound to the anchor", desiredAgents, desiredWindow.Spec.AnchorPaneRef)
		}
		if err := desired.Validate(); err != nil {
			t.Fatalf("projected fixture: %v", err)
		}
		agentUID, previous := desiredAgents[0].Metadata.UID, desiredAgents[0].Status.PaneRef

		m := testMutator(dirSet{"/src/one": true})
		m.NewUID = func(kind Kind) (string, error) { return strings.ToLower(string(kind)) + "-rebind", nil }
		pane, err := m.AttachAgentPane(&desired, agentUID, BootstrapPane{CWD: "/src/one"}, "continue")
		if err != nil {
			t.Fatalf("attach: %v", err)
		}
		assertAnchorFollowedRebind(t, desired, desiredWindow.Metadata.UID, agentUID, previous, pane.Metadata.UID)
	})
	t.Run("Running re-attach", func(t *testing.T) {
		t.Parallel()
		m, reg, windowUID, _ := anchorWriterFixture(t)
		agentUID, previous := attachFixtureAgent(t, m, &reg, windowUID, "anchor-agent")
		window, _ := reg.Window(windowUID)
		window.Spec.AnchorPaneRef = previous
		if err := reg.Validate(); err != nil {
			t.Fatalf("fixture: %v", err)
		}
		pane, err := m.AttachAgentPane(&reg, agentUID, BootstrapPane{CWD: "/src/projmux"}, "reattach")
		if err != nil {
			t.Fatalf("attach: %v", err)
		}
		assertAnchorFollowedRebind(t, reg, windowUID, agentUID, previous, pane.Metadata.UID)
	})
}

func assertAnchorFollowedRebind(t *testing.T, reg Registry, windowUID, agentUID, previous, current string) {
	t.Helper()
	window, _ := reg.Window(windowUID)
	agent, _ := reg.Agent(agentUID)
	if agent.Status.Phase != PhaseRunning || agent.Status.PaneRef != current || window.Spec.AnchorPaneRef != current {
		t.Fatalf("rebind = agent:%+v anchor:%q, want Running bound to and anchored on %q", agent.Status, window.Spec.AnchorPaneRef, current)
	}
	if _, ok := reg.Pane(previous); !ok {
		t.Fatalf("previous binding Pane %q row was removed", previous)
	}
	if err := reg.Validate(); err != nil {
		t.Fatalf("post rebind: %v", err)
	}
}

func paneOrder(reg Registry) []string {
	out := make([]string, 0, len(reg.Panes))
	for _, pane := range reg.Panes {
		out = append(out, pane.Metadata.UID)
	}
	return out
}

// anchorWriterPropertySeed and anchorWriterPropertyIterations are reported by
// the property test so a counterexample is reproducible.
const (
	anchorWriterPropertySeed       = int64(20260913)
	anchorWriterPropertyIterations = 512
)

// Acceptance 5: from arbitrary valid Window configurations, every anchor
// writer commits only Registries Validate accepts.
func TestAnchorWritersPreserveValidationOverRandomWindowConfigurations(t *testing.T) {
	t.Parallel()
	random := rand.New(rand.NewSource(anchorWriterPropertySeed)) // #nosec G404 -- deterministic property input.
	writes := map[string]int{}
	moved := map[string]int{}
	closures := 0
	for iteration := range anchorWriterPropertyIterations {
		m, reg := randomValidAnchorRegistry(t, random, iteration)
		for step := range 1 + random.Intn(4) {
			before := reg.Clone()
			writer := ""
			switch random.Intn(3) {
			case 0:
				if len(reg.Panes) == 0 {
					continue
				}
				writer = "delete pane"
				pane := reg.Panes[random.Intn(len(reg.Panes))]
				if err := m.DeletePane(&reg, pane.Metadata.UID); err != nil {
					t.Fatalf("seed=%d iteration=%d step=%d delete %s: %v", anchorWriterPropertySeed, iteration, step, pane.Metadata.UID, err)
				}
			case 1:
				if len(reg.Agents) == 0 {
					continue
				}
				agent := reg.Agents[random.Intn(len(reg.Agents))]
				phase := []AgentPhase{PhaseOffline, PhaseFailed, PhasePending}[random.Intn(3)]
				if !CanTransitionAgent(agent.Status.Phase, phase) {
					continue
				}
				writer = "transition agent"
				if _, err := m.TransitionAgent(&reg, agent.Metadata.UID, phase, "pbt"); err != nil {
					t.Fatalf("seed=%d iteration=%d step=%d transition %s -> %s: %v", anchorWriterPropertySeed, iteration, step, agent.Metadata.UID, phase, err)
				}
			case 2:
				if len(reg.Agents) == 0 {
					continue
				}
				agent := reg.Agents[random.Intn(len(reg.Agents))]
				writer = "attach agent pane"
				if _, err := m.AttachAgentPane(&reg, agent.Metadata.UID, BootstrapPane{}, fmt.Sprintf("pbt-attach-%d", step)); err != nil {
					t.Fatalf("seed=%d iteration=%d step=%d attach %s: %v", anchorWriterPropertySeed, iteration, step, agent.Metadata.UID, err)
				}
			}
			writes[writer]++
			if err := reg.Validate(); err != nil {
				t.Fatalf("seed=%d iteration=%d step=%d %s committed an invalid Registry: %v\nbefore=%s", anchorWriterPropertySeed, iteration, step, writer, err, mustJSONForProperty(before))
			}
			for _, window := range before.Windows {
				after, ok := reg.Window(window.Metadata.UID)
				if !ok || after.Spec.AnchorPaneRef == window.Spec.AnchorPaneRef {
					continue
				}
				moved[writer]++
				if _, existed := before.Pane(after.Spec.AnchorPaneRef); !existed {
					closures++
				}
			}
		}
	}
	for _, writer := range []string{"delete pane", "transition agent", "attach agent pane"} {
		if writes[writer] == 0 || moved[writer] == 0 {
			t.Fatalf("seed=%d generator never exercised %s anchor movement: writes=%v moved=%v", anchorWriterPropertySeed, writer, writes, moved)
		}
	}
	if closures == 0 {
		t.Fatalf("seed=%d generator never reached the default-shell close", anchorWriterPropertySeed)
	}
	t.Logf("seed=%d iterations=%d writes=%v anchorMoves=%v defaultShellCloses=%d counterexamples=0",
		anchorWriterPropertySeed, anchorWriterPropertyIterations, writes, moved, closures)
}

// randomValidAnchorRegistry builds a valid Registry through the shipped
// Mutator, then places each Window anchor on a random eligible Pane and shuffles
// Registry Pane order. Agents cover Pending, Running, Running with a superseded
// retained Pane, released Offline/Failed with a retained Pane, and the
// snapshot-projection Offline/Failed shape that still binds its Pane.
func randomValidAnchorRegistry(t *testing.T, random *rand.Rand, iteration int) (Mutator, Registry) {
	t.Helper()
	const root = "/src/pbt"
	counters := map[Kind]int{}
	m := Mutator{
		Now: func() time.Time { return fixedNow },
		NewUID: func(kind Kind) (string, error) {
			counters[kind]++
			return fmt.Sprintf("%s-g%04d", strings.ToLower(string(kind)), counters[kind]), nil
		},
		DirExists: func(path string) (bool, error) { return path == root, nil },
	}
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("seed=%d iteration=%d generator: %s", anchorWriterPropertySeed, iteration, fmt.Sprintf(format, args...))
	}
	reg := NewRegistry()
	registered, err := m.RegisterProject(&reg, RegisterProjectOptions{Root: root, OperationID: "pbt-register"})
	if err != nil {
		fail("register: %v", err)
	}
	windows := []Window{registered.Windows[0]}
	for range random.Intn(3) {
		window, _, err := m.AddWindow(&reg, registered.Project.Metadata.UID, BootstrapWindow{}, "sh", "pbt-window")
		if err != nil {
			fail("add window: %v", err)
		}
		windows = append(windows, window)
	}
	for _, window := range windows {
		windowUID := window.Metadata.UID
		initialShell := window.Spec.DefaultShellPaneRef
		for range random.Intn(3) {
			if _, err := m.AddPane(&reg, windowUID, BootstrapPane{}, "sh", "pbt-shell"); err != nil {
				fail("add pane: %v", err)
			}
		}
		for range random.Intn(4) {
			agent, err := m.CreateAgent(&reg, windowUID, CreateAgentOptions{Provider: "codex", OperationID: "pbt-agent"})
			if err != nil {
				fail("create agent: %v", err)
			}
			uid := agent.Metadata.UID
			attach := func() {
				if _, err := m.AttachAgentPane(&reg, uid, BootstrapPane{}, "pbt-attach"); err != nil {
					fail("attach: %v", err)
				}
			}
			transition := func(phase AgentPhase) {
				if _, err := m.TransitionAgent(&reg, uid, phase, "pbt"); err != nil {
					fail("transition %s: %v", phase, err)
				}
			}
			switch random.Intn(6) {
			case 0: // Pending, never attached.
			case 1: // Running.
				attach()
			case 2: // Running with a superseded retained Pane.
				attach()
				attach()
			case 3: // Released Offline/Failed with a retained Pane.
				attach()
				transition([]AgentPhase{PhaseOffline, PhaseFailed}[random.Intn(2)])
			case 4: // Snapshot-projection shape: non-Running and still bound.
				attach()
				stored, _ := reg.Agent(uid)
				stored.Status.Phase = []AgentPhase{PhaseOffline, PhaseFailed}[random.Intn(2)]
			case 5: // Released, then Pending again.
				attach()
				transition(PhaseOffline)
				transition(PhasePending)
			}
		}
		if random.Intn(3) == 0 {
			if err := m.DeletePane(&reg, initialShell); err != nil {
				fail("delete initial shell: %v", err)
			}
		}
	}
	if random.Intn(2) == 0 {
		random.Shuffle(len(reg.Panes), func(i, j int) { reg.Panes[i], reg.Panes[j] = reg.Panes[j], reg.Panes[i] })
	}
	for i := range reg.Windows {
		windowUID := reg.Windows[i].Metadata.UID
		var eligible, shells []string
		for _, pane := range reg.Panes {
			if windowAnchorEligibility(reg, windowUID, pane) != windowAnchorEligible {
				continue
			}
			eligible = append(eligible, pane.Metadata.UID)
			if pane.Spec.Role == PaneRoleShell {
				shells = append(shells, pane.Metadata.UID)
			}
		}
		if len(eligible) == 0 {
			fail("window %s has no eligible anchor", windowUID)
		}
		reg.Windows[i].Spec.AnchorPaneRef = eligible[random.Intn(len(eligible))]
		reg.Windows[i].Spec.DefaultShellPaneRef = ""
		if len(shells) > 0 && random.Intn(3) != 0 {
			reg.Windows[i].Spec.DefaultShellPaneRef = shells[random.Intn(len(shells))]
		}
	}
	if err := reg.Validate(); err != nil {
		fail("start Registry is invalid: %v", err)
	}
	return m, reg
}

func mustJSONForProperty(reg Registry) string {
	data, err := json.Marshal(reg)
	if err != nil {
		return err.Error()
	}
	return string(data)
}
