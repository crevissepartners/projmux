package controller

import (
	"testing"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
)

func controlTargetState(root, role, window, pane bool) ControlTargetState {
	state := ControlTargetState{
		Declaration: ControlTargetDeclaration{
			Transport: resourcegraph.Transport{Kind: resourcegraph.TransportSocketName, Value: "isolated", Source: resourcegraph.TransportSourceSocketName},
			Session:   "home", Declared: true,
		},
		AppOwned: true,
		Windows: []ControlWindowClaim{{
			ControlMirrorClaim: ControlMirrorClaim{Handle: "@1"},
			Panes:              []ControlMirrorClaim{{Handle: "%1"}},
		}},
	}
	if root {
		state.RootUIDs = []string{"ctl-home"}
	}
	if role {
		state.Role = resourcegraph.ControlSessionRole
	}
	if window {
		state.Windows[0].UID = "win-home"
		state.Windows[0].Known = root
		state.Windows[0].RootKind = "ControlSession"
		state.Windows[0].RootUID = "ctl-home"
	}
	if pane {
		state.Windows[0].Panes[0].UID = "pan-home"
		state.Windows[0].Panes[0].Known = root
		state.Windows[0].Panes[0].RootKind = "ControlSession"
		state.Windows[0].Panes[0].RootUID = "ctl-home"
		state.Windows[0].Panes[0].WindowUID = "win-home"
	}
	return state
}

func convergeControlTargetState(state ControlTargetState, plan ControlTargetPlan) ControlTargetState {
	if plan.Refused() {
		return state
	}
	if len(state.RootUIDs) == 0 {
		state.RootUIDs = []string{"ctl-home"}
	}
	state.Role = resourcegraph.ControlSessionRole
	for wi := range state.Windows {
		window := &state.Windows[wi]
		window.UID, window.Known, window.RootKind, window.RootUID = "win-home", true, "ControlSession", "ctl-home"
		for pi := range window.Panes {
			pane := &window.Panes[pi]
			pane.UID, pane.Known, pane.RootKind, pane.RootUID, pane.WindowUID = "pan-home", true, "ControlSession", "ctl-home", "win-home"
		}
	}
	return state
}

// FuzzControlTargetConvergenceIsClosedAndIdempotent is the Phase 11 contract:
// every existence combination has one decision and every allowed combination
// reaches an empty second plan.
func FuzzControlTargetConvergenceIsClosedAndIdempotent(f *testing.F) {
	for bits := range byte(16) {
		f.Add(bits)
	}
	f.Fuzz(func(t *testing.T, bits byte) {
		state := controlTargetState(bits&1 != 0, bits&2 != 0, bits&4 != 0, bits&8 != 0)
		plan := PlanControlTargetConvergence(state)
		if plan.Refused() && len(plan.Actions) > 0 {
			t.Fatalf("plan is not closed: %+v", plan)
		}
		if plan.Refused() {
			return
		}
		second := PlanControlTargetConvergence(convergeControlTargetState(state, plan))
		if !second.Converged() {
			t.Fatalf("second plan = %+v, want empty", second)
		}
	})
}

func TestControlTargetRefusalsAreExactAndWriteNothing(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*ControlTargetState)
		want string
	}{
		{"foreign claimant", func(s *ControlTargetState) {
			s.RootUIDs = []string{"ctl-home"}
			s.Windows[0].UID, s.Windows[0].Known = "win-foreign", false
		}, "Window @1 carries foreign uid claimant \"win-foreign\" while ControlSession \"ctl-home\" already exists"},
		{"multiple claimant", func(s *ControlTargetState) { s.Windows = append(s.Windows, s.Windows[0]); s.Windows[1].Handle = "@2" }, "multiple Window claimants carry uid \"win-home\": @1, @2"},
		{"project uid conflict", func(s *ControlTargetState) { s.ProjectUID, s.ProjectKnown = "proj-work", true }, "declared control target carries Project uid conflict \"proj-work\""},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := controlTargetState(true, true, true, true)
			test.edit(&state)
			plan := PlanControlTargetConvergence(state)
			if plan.Reason != test.want || len(plan.Actions) != 0 {
				t.Fatalf("plan = %+v, want zero-write refusal %q", plan, test.want)
			}
		})
	}
}
