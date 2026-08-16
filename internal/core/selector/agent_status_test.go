package selector

import (
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// agentStatusRegistry is the fixture the Agent observation contract is stated
// against. Every Agent below lives under the *same* Window on purpose: the
// defect this replaces read the Window's status, so a fixture that spread the
// Agents over several Windows could pass by accident.
//
//   - "runner" is Running with a live managed Pane.
//   - "ghost" is Running with a managed Pane whose tmux pane is gone.
//   - "released" is Offline with no Pane at all, which is what every released
//     Agent looks like.
//   - "pending" has never been attached.
//   - "failed" ended abnormally.
//
// The Window itself is observed live throughout, which is the condition that
// used to make all five report live.
func agentStatusRegistry(t *testing.T, missingRoot bool) metadata.Registry {
	t.Helper()
	b := newBuilder(t)

	b.project("prj-alpha", "alpha", "alpha", "/srv/alpha",
		&metadata.SessionProjection{Name: "alpha", Live: true}, missingRoot)
	b.window("win-main", "main", "prj-alpha", nil)
	b.shellPane("pan-shell", "zsh", "zsh", "win-main", "/srv/alpha", nil)

	b.agentWithPane("agt-runner", "runner", "win-main", "pan-runner", "runner-pane", nil)
	b.agentWithPane("agt-ghost", "ghost", "win-main", "pan-ghost", "ghost-pane", nil)
	b.paneLessAgent("agt-released", "released", "win-main", metadata.PhaseOffline)
	b.paneLessAgent("agt-pending", "pending", "win-main", metadata.PhasePending)
	b.paneLessAgent("agt-failed", "failed", "win-main", metadata.PhaseFailed)

	return b.build()
}

// agentStatuses resolves every Agent and returns name -> reported status.
func agentStatuses(t *testing.T, registry metadata.Registry, observed metadata.RuntimeObservation) map[string]Status {
	t.Helper()
	resolution, err := NewObserved(registry, observed).ResolveAgents(Query{})
	if err != nil {
		t.Fatalf("ResolveAgents: %v", err)
	}
	out := map[string]Status{}
	for _, match := range resolution.Matches {
		out[match.Name] = match.Status
	}
	return out
}

func TestAgentStatusIsObservedFromItsOwnManagedPaneNotTheOwnerWindow(t *testing.T) {
	t.Parallel()

	registry := agentStatusRegistry(t, false)
	// The Window and the shell Pane are live, and so is exactly one managed
	// Pane. Everything else has no runtime object on this machine.
	observed := observing([]string{"win-main"}, []string{"pan-shell", "pan-runner"})

	want := map[string]Status{
		"runner":   StatusLive,
		"ghost":    StatusOffline,
		"released": StatusOffline,
		"pending":  StatusOffline,
		"failed":   StatusOffline,
	}
	got := agentStatuses(t, registry, observed)
	for name, expected := range want {
		if got[name] != expected {
			t.Errorf("agent %q status = %q, want %q", name, got[name], expected)
		}
	}

	// The Window under which all five live is itself live. If any Agent were
	// still inheriting, this is the assertion that catches it.
	windows, err := NewObserved(registry, observed).ResolveWindows(Query{})
	if err != nil {
		t.Fatalf("ResolveWindows: %v", err)
	}
	if len(windows.Matches) != 1 || windows.Matches[0].Status != StatusLive {
		t.Fatalf("fixture window must be live, got %+v", windows.Matches)
	}
}

func TestAgentWithAnEmptyPaneRefIsOfflineNotLive(t *testing.T) {
	t.Parallel()

	registry := agentStatusRegistry(t, false)
	// Everything on the machine is live, including every Pane the registry
	// knows. An Agent with no paneRef still has no runtime object to observe,
	// so a maximally live machine must not lift it to live.
	observed := observing(
		[]string{"win-main"},
		[]string{"pan-shell", "pan-runner", "pan-ghost"},
	)
	got := agentStatuses(t, registry, observed)
	for _, name := range []string{"released", "pending", "failed"} {
		if got[name] != StatusOffline {
			t.Errorf("pane-less agent %q status = %q, want offline", name, got[name])
		}
	}
	if got["runner"] != StatusLive || got["ghost"] != StatusLive {
		t.Errorf("agents with a live managed pane must stay live, got %q/%q", got["runner"], got["ghost"])
	}
}

func TestAgentPhaseAndObservedStatusNeverContradict(t *testing.T) {
	t.Parallel()

	registry := agentStatusRegistry(t, false)
	phases := map[string]metadata.AgentPhase{}
	for _, agent := range registry.Agents {
		phases[agent.Metadata.Name] = agent.Status.Phase
	}

	// Every subset of the machine, not one hand-picked one. Status is derived
	// per Agent from one observation, so enumerating the observations is
	// enumerating the reachable answers.
	paneUIDs := []string{"pan-shell", "pan-runner", "pan-ghost"}
	for mask := 0; mask < 1<<len(paneUIDs); mask++ {
		var live []string
		for i, uid := range paneUIDs {
			if mask&(1<<i) != 0 {
				live = append(live, uid)
			}
		}
		got := agentStatuses(t, registry, observing([]string{"win-main"}, live))
		for name, status := range got {
			if status == StatusLive && phases[name] != metadata.PhaseRunning {
				t.Fatalf("agent %q is %s but reported %s with live panes %v",
					name, phases[name], status, live)
			}
		}
	}
}

func TestAgentMissingRootOutranksALiveManagedPane(t *testing.T) {
	t.Parallel()

	// The owning Project lost spec.root. The Agent's managed Pane is still
	// mirrored by a live tmux pane, and MissingRoot still wins: the resource
	// needs an explicit rebind regardless of what tmux is doing, and it must
	// stay selectable rather than be hidden behind a stale live reading.
	registry := agentStatusRegistry(t, true)
	observed := observing([]string{"win-main"}, []string{"pan-shell", "pan-runner", "pan-ghost"})

	got := agentStatuses(t, registry, observed)
	for name, status := range got {
		if status != StatusMissingRoot {
			t.Errorf("agent %q status = %q, want missing-root", name, status)
		}
	}
}

func TestAgentStatusReadsNoStoredLivenessBool(t *testing.T) {
	t.Parallel()

	// The Project's session projection says live and the observation is empty:
	// the machine has no tmux server at all. Nothing may report live from the
	// stored bool, which is the exact defect the observation contract replaced.
	registry := agentStatusRegistry(t, false)
	got := agentStatuses(t, registry, metadata.RuntimeObservation{})
	for name, status := range got {
		if status != StatusOffline {
			t.Errorf("agent %q status = %q with no observation, want offline", name, status)
		}
	}
}

func TestAgentStatusIgnoresAPaneRefTheRegistryNoLongerHolds(t *testing.T) {
	t.Parallel()

	// A dangling paneRef is not a runtime observation of anything. Reporting
	// live from one would make `get agents` claim a Pane `get panes` has no row
	// for, which is the cross-verb contradiction this contract forbids.
	registry := agentStatusRegistry(t, false)
	for i := range registry.Agents {
		if registry.Agents[i].Metadata.Name == "runner" {
			registry.Agents[i].Status.PaneRef = "pan-does-not-exist"
		}
	}
	got := agentStatuses(t, registry, observing([]string{"win-main"}, []string{"pan-does-not-exist"}))
	if got["runner"] != StatusOffline {
		t.Fatalf("agent with a dangling paneRef = %q, want offline", got["runner"])
	}
}

func TestAgentStatusIsInvariantUnderTheOwnerWindowObservation(t *testing.T) {
	t.Parallel()

	// The "zero inheritance paths" assertion, stated as behavior rather than as
	// a grep. The owner Window's own liveness is flipped while every other input
	// is held fixed; if any path still routes a Window status into an Agent
	// status, at least one Agent answer has to move.
	registry := agentStatusRegistry(t, false)
	panes := []string{"pan-shell", "pan-runner"}

	withWindow := agentStatuses(t, registry, observing([]string{"win-main"}, panes))
	withoutWindow := agentStatuses(t, registry, observing(nil, panes))

	if len(withWindow) != len(registry.Agents) {
		t.Fatalf("resolved %d agents, want %d", len(withWindow), len(registry.Agents))
	}
	for name, status := range withWindow {
		if withoutWindow[name] != status {
			t.Errorf("agent %q moved from %q to %q when only the owner Window observation changed",
				name, status, withoutWindow[name])
		}
	}

	// And the Window really did move, so the invariance above is not vacuous.
	live, err := NewObserved(registry, observing([]string{"win-main"}, panes)).ResolveWindows(Query{})
	if err != nil {
		t.Fatalf("ResolveWindows: %v", err)
	}
	gone, err := NewObserved(registry, observing(nil, panes)).ResolveWindows(Query{})
	if err != nil {
		t.Fatalf("ResolveWindows: %v", err)
	}
	if live.Matches[0].Status != StatusLive || gone.Matches[0].Status != StatusOffline {
		t.Fatalf("owner window statuses = %q/%q, want live/offline", live.Matches[0].Status, gone.Matches[0].Status)
	}
}
