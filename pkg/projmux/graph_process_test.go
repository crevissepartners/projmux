package projmux_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func graphProcessInternalInventory(t *testing.T, in core.GraphProcessInventory) resourcegraph.ProcessInventory {
	t.Helper()
	// Built from the public value's own fields, not through the SDK adapter.
	out := resourcegraph.ProcessInventory{}
	for _, k := range in.Declared {
		out.Declared = append(out.Declared, resourcegraph.ProcessKey{Host: k.Host, Pane: k.Pane, Generation: k.Generation})
	}
	for _, o := range in.Observed {
		out.Observed = append(out.Observed, resourcegraph.ProcessObservation{
			Key:    resourcegraph.ProcessKey{Host: o.Key.Host, Pane: o.Key.Pane, Generation: o.Key.Generation},
			Status: resourcegraph.Status(o.Status),
		})
	}
	return out
}

func graphProcessInternalPane(t *testing.T, in core.Pane) metadata.Pane {
	t.Helper()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out metadata.Pane
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The fixtures are values the internal judgement reads directly; none is
// validated as a Registry row, so each field is chosen to take a branch of
// ProcessInventory.current / Declares / AdmitProcess.
func TestAdmitGraphProcessEqualsInternal(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	zero := 0
	binding := core.ProcessBinding{HostInstanceID: "host", ProjectUID: "proj", WindowUID: "win", AgentUID: "agent", PaneUID: "pane-p", Generation: "gen-s", OperationID: "op"}
	receipt := func(edit func(*core.TerminationEvidence)) *core.TerminationEvidence {
		r := &core.TerminationEvidence{Source: core.TerminationSourceSupervisor, Classification: "normal", ObservedAt: at, PaneUID: "pane-p", AgentUID: "agent", Generation: "gen-s", OperationID: "op", ExitCode: &zero}
		if edit != nil {
			edit(r)
		}
		return r
	}
	pane := func(edit func(*core.Pane)) core.Pane {
		p := core.Pane{}
		p.Metadata.UID = "pane-p"
		if edit != nil {
			edit(&p)
		}
		return p
	}
	activation := func(p *core.Pane) { p.Status.Activation = core.PaneActivation{Generation: "gen-1"} }
	asProcess := func(p *core.Pane) { p.Spec.Runtime.Kind = core.RuntimeProcess }
	session := func(p *core.Pane) {
		asProcess(p)
		p.Status.ProcessSession = &core.ProcessSessionRecord{Provider: "claude", Binding: binding}
	}
	panes := []struct {
		name string
		pane core.Pane
	}{
		{"tmux without activation", pane(nil)},
		{"tmux with activation", pane(activation)},
		{"process without activation or session", pane(asProcess)},
		{"process with activation", pane(func(p *core.Pane) { asProcess(p); activation(p) })},
		{"process with runtime id", pane(func(p *core.Pane) {
			asProcess(p)
			p.Status.Activation = core.PaneActivation{Generation: "gen-1", RuntimeID: "%1"}
		})},
		{"process session without receipt", pane(session)},
		{"process session with matching wait", pane(func(p *core.Pane) { session(p); p.Status.LastTermination = receipt(nil) })},
		{"process session with other wait", pane(func(p *core.Pane) {
			session(p)
			p.Status.LastTermination = receipt(func(r *core.TerminationEvidence) { r.OperationID = "other" })
		})},
	}
	key := func(host, gen string) core.GraphProcessKey {
		return core.GraphProcessKey{Host: host, Pane: "pane-p", Generation: gen}
	}
	obs := func(k core.GraphProcessKey, status string) core.GraphProcessObservation {
		return core.GraphProcessObservation{Key: k, Status: status}
	}
	inventories := []struct {
		name string
		in   core.GraphProcessInventory
	}{
		{"empty", core.GraphProcessInventory{}},
		{"declared current only", core.GraphProcessInventory{Declared: []core.GraphProcessKey{key("h", "gen-1")}}},
		{"declared and live", core.GraphProcessInventory{Declared: []core.GraphProcessKey{key("h", "gen-1")}, Observed: []core.GraphProcessObservation{obs(key("h", "gen-1"), "live")}}},
		{"declared and offline", core.GraphProcessInventory{Declared: []core.GraphProcessKey{key("h", "gen-1")}, Observed: []core.GraphProcessObservation{obs(key("h", "gen-1"), "offline")}}},
		{"declared and unknown", core.GraphProcessInventory{Declared: []core.GraphProcessKey{key("h", "gen-1")}, Observed: []core.GraphProcessObservation{obs(key("h", "gen-1"), "unknown")}}},
		{"declared and unlisted status", core.GraphProcessInventory{Declared: []core.GraphProcessKey{key("h", "gen-1")}, Observed: []core.GraphProcessObservation{obs(key("h", "gen-1"), "running")}}},
		{"duplicate observation", core.GraphProcessInventory{Declared: []core.GraphProcessKey{key("h", "gen-1")}, Observed: []core.GraphProcessObservation{obs(key("h", "gen-1"), "live"), obs(key("h", "gen-1"), "live")}}},
		{"duplicate declaration", core.GraphProcessInventory{Declared: []core.GraphProcessKey{key("h", "gen-1"), key("h2", "gen-1")}, Observed: []core.GraphProcessObservation{obs(key("h", "gen-1"), "live")}}},
		{"old generation", core.GraphProcessInventory{Declared: []core.GraphProcessKey{key("h", "gen-0")}, Observed: []core.GraphProcessObservation{obs(key("h", "gen-0"), "live")}}},
		{"incomplete key", core.GraphProcessInventory{Declared: []core.GraphProcessKey{key("", "gen-1")}, Observed: []core.GraphProcessObservation{obs(key("", "gen-1"), "live")}}},
		{"other pane", core.GraphProcessInventory{Declared: []core.GraphProcessKey{{Host: "h", Pane: "pane-q", Generation: "gen-1"}}}},
		{"session generation offline", core.GraphProcessInventory{Declared: []core.GraphProcessKey{key("h", "gen-s")}, Observed: []core.GraphProcessObservation{obs(key("h", "gen-s"), "offline")}}},
		{"session generation live", core.GraphProcessInventory{Declared: []core.GraphProcessKey{key("h", "gen-s")}, Observed: []core.GraphProcessObservation{obs(key("h", "gen-s"), "live")}}},
	}
	actions := []core.GraphProcessAction{
		core.GraphProcessAttach, core.GraphProcessFocus, core.GraphProcessKeys, core.GraphProcessCapture,
		core.GraphProcessPopup, core.GraphProcessRelaunch, core.GraphProcessCreatePane, core.GraphProcessSplit,
		core.GraphProcessTurn, core.GraphProcessInterrupt, core.GraphProcessStop, "no-such-action", "",
	}
	var keys, declared, notDeclared, undeclaredAction, hostUnavailable, capability, anchor, unknownAction int
	for _, pc := range panes {
		for _, ic := range inventories {
			internalPane := graphProcessInternalPane(t, pc.pane)
			internalInventory := graphProcessInternalInventory(t, ic.in)
			if got, want := projmux.GraphProcessDeclares(ic.in, pc.pane), internalInventory.Declares(internalPane); got != want {
				t.Fatalf("%s / %s: Declares public %v internal %v", pc.name, ic.name, got, want)
			} else if got {
				declared++
			} else {
				notDeclared++
			}
			for _, action := range actions {
				name := pc.name + " / " + ic.name + " / " + string(action)
				got, gotErr := projmux.AdmitGraphProcess(ic.in, pc.pane, action)
				want, wantErr := internalInventory.AdmitProcess(internalPane, resourcegraph.ProcessAction(action))
				if (gotErr == nil) != (wantErr == nil) || (gotErr != nil && gotErr.Error() != wantErr.Error()) {
					t.Fatalf("%s: error public %v internal %v", name, gotErr, wantErr)
				}
				if got.Host != want.Host || got.Pane != want.Pane || got.Generation != want.Generation {
					t.Fatalf("%s: key public %+v internal %+v", name, got, want)
				}
				if wantErr == nil {
					keys++
				} else {
					text := wantErr.Error()
					switch {
					case text == "process target is not declared for this Pane generation":
						undeclaredAction++
					case strings.HasPrefix(text, "process-host-unavailable:"):
						hostUnavailable++
					case strings.HasPrefix(text, "process-capability-unsupported:"):
						unknownAction++
					case strings.Contains(text, "has no tmux target"):
						anchor++
					case strings.HasSuffix(text, "use host turn, interrupt, or Stop"):
						capability++
					default:
						t.Fatalf("%s: unclassified error %q", name, text)
					}
				}
			}
		}
	}
	if keys < 3 || declared < 10 || notDeclared < 10 || undeclaredAction < 10 || hostUnavailable < 10 || capability < 20 || anchor < 4 || unknownAction < 4 {
		t.Fatalf("table too narrow: keys=%d declared=%d notDeclared=%d undeclaredAction=%d hostUnavailable=%d capability=%d anchor=%d unknownAction=%d", keys, declared, notDeclared, undeclaredAction, hostUnavailable, capability, anchor, unknownAction)
	}
}

func TestGraphProcessActionsEqualInternal(t *testing.T) {
	pairs := []struct {
		public   core.GraphProcessAction
		internal resourcegraph.ProcessAction
	}{
		{core.GraphProcessAttach, resourcegraph.ProcessAttach},
		{core.GraphProcessFocus, resourcegraph.ProcessFocus},
		{core.GraphProcessKeys, resourcegraph.ProcessKeys},
		{core.GraphProcessCapture, resourcegraph.ProcessCapture},
		{core.GraphProcessPopup, resourcegraph.ProcessPopup},
		{core.GraphProcessRelaunch, resourcegraph.ProcessRelaunch},
		{core.GraphProcessCreatePane, resourcegraph.ProcessCreatePane},
		{core.GraphProcessSplit, resourcegraph.ProcessSplit},
		{core.GraphProcessTurn, resourcegraph.ProcessTurn},
		{core.GraphProcessInterrupt, resourcegraph.ProcessInterrupt},
		{core.GraphProcessStop, resourcegraph.ProcessStop},
	}
	seen := map[core.GraphProcessAction]bool{}
	for _, p := range pairs {
		if string(p.public) != string(p.internal) {
			t.Fatalf("public %q internal %q", p.public, p.internal)
		}
		seen[p.public] = true
	}
	if len(pairs) != 11 || len(seen) != 11 {
		t.Fatalf("expected 11 distinct actions, got %d pairs and %d distinct", len(pairs), len(seen))
	}
}

func TestGraphProcessInventoryCloneIsIndependent(t *testing.T) {
	key := core.GraphProcessKey{Host: "h", Pane: "p", Generation: "g"}
	original := core.GraphProcessInventory{
		Declared: []core.GraphProcessKey{key},
		Observed: []core.GraphProcessObservation{{Key: key, Status: "live"}},
	}
	want := core.GraphProcessInventory{
		Declared: []core.GraphProcessKey{key},
		Observed: []core.GraphProcessObservation{{Key: key, Status: "live"}},
	}
	clone := original.Clone()
	if !reflect.DeepEqual(clone, original) {
		t.Fatalf("clone %+v differs from %+v", clone, original)
	}
	clone.Declared[0].Host = "changed"
	clone.Observed[0].Status = "offline"
	clone.Declared = append(clone.Declared, key)
	if !reflect.DeepEqual(original, want) {
		t.Fatalf("original changed: %+v", original)
	}
	nilClone := core.GraphProcessInventory{}.Clone()
	if nilClone.Declared != nil || nilClone.Observed != nil {
		t.Fatalf("nil slices did not stay nil: %+v", nilClone)
	}
	empty := core.GraphProcessInventory{Declared: []core.GraphProcessKey{}, Observed: []core.GraphProcessObservation{}}.Clone()
	if empty.Declared == nil || empty.Observed == nil {
		t.Fatalf("empty slices became nil: %+v", empty)
	}
	if internalClone := (resourcegraph.ProcessInventory{}).Clone(); internalClone.Declared != nil || internalClone.Observed != nil {
		t.Fatalf("internal nil clone is not nil: %+v", internalClone)
	}
}
