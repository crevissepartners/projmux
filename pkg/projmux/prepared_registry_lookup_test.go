package projmux

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// lookupFixture prepares the process fixture with a labelled Agent Pane, so
// returned Panes carry a map and slices to change.
func lookupFixture(t *testing.T, change func(*metadata.Registry)) (*PreparedRegistrySnapshot, metadata.Registry) {
	t.Helper()
	c, paths := graphFixtureClient(t, mutatedGraphFixture(t, func(r *metadata.Registry) {
		r.Panes[1].Metadata.Labels = map[string]string{"role": "worker"}
		if change != nil {
			change(r)
		}
	}))
	prepared, _ := mustPrepare(t, c, nil)
	loaded, err := resources.Load(paths)
	if err != nil {
		t.Fatal(err)
	}
	return prepared, loaded
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The four lookups are the Registry's own answers for every Window, Pane and
// binding shape, and the public result serializes to the same bytes.
func TestPreparedRegistryLookupParity(t *testing.T) {
	good := core.ProcessBinding{HostInstanceID: "host-one", ProjectUID: "project-01", WindowUID: "window-01", AgentUID: "agent-01", PaneUID: "pane-02", Generation: "gen-one", OperationID: "op-one"}
	variants := []struct {
		name   string
		change func(*metadata.Registry)
	}{
		{"fixture", nil},
		{"process-anchor", func(r *metadata.Registry) { r.Windows[0].Spec.AnchorPaneRef = "pane-02" }},
		{"missing-anchor", func(r *metadata.Registry) { r.Windows[0].Spec.AnchorPaneRef = "pane-absent" }},
		{"foreign-anchor", func(r *metadata.Registry) {
			r.Windows[0].Spec.AnchorPaneRef = "pane-02"
			r.Agents[0].Metadata.OwnerRef = &metadata.OwnerRef{Kind: metadata.KindWindow, UID: "window-elsewhere"}
		}},
		{"generation-moved", func(r *metadata.Registry) { r.Panes[1].Status.Activation.Generation = "gen-two" }},
		{"retired", func(r *metadata.Registry) {
			r.Panes[1].Status.Activation = metadata.PaneActivation{}
			r.Agents[0].Status.Phase = metadata.PhaseOffline
		}},
		{"not-current-pane", func(r *metadata.Registry) { r.Agents[0].Status.PaneRef = "pane-01" }},
	}
	bindings := []struct {
		name    string
		binding core.ProcessBinding
	}{{"good", good}, {"zero", core.ProcessBinding{}}}
	for _, tc := range []struct {
		name   string
		mutate func(*core.ProcessBinding)
	}{
		{"other-generation", func(b *core.ProcessBinding) { b.Generation = "gen-other" }},
		{"other-operation", func(b *core.ProcessBinding) { b.OperationID = "op-other" }},
		{"other-project", func(b *core.ProcessBinding) { b.ProjectUID = "project-other" }},
		{"other-window", func(b *core.ProcessBinding) { b.WindowUID = "window-other" }},
		{"shell-pane", func(b *core.ProcessBinding) { b.PaneUID = "pane-01" }},
		{"other-agent", func(b *core.ProcessBinding) { b.AgentUID = "agent-other" }},
	} {
		b := good
		tc.mutate(&b)
		bindings = append(bindings, struct {
			name    string
			binding core.ProcessBinding
		}{tc.name, b})
	}
	seen := map[bool]int{}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			prepared, loaded := lookupFixture(t, variant.change)
			for _, w := range []string{"", "window-absent", "window-01", " window-01 "} {
				wantPane, wantOK := loaded.WindowAnchor(w)
				gotPane, gotOK := prepared.WindowAnchor(w)
				if gotOK != wantOK || (wantOK && !bytes.Equal(mustJSON(t, gotPane), mustJSON(t, wantPane))) || (!wantOK && !reflect.DeepEqual(gotPane, core.Pane{})) {
					t.Fatalf("WindowAnchor(%q): got %v, Registry %v", w, gotOK, wantOK)
				}
				seen[gotOK]++
				for _, p := range []string{"", "pane-absent", "pane-01", "pane-02", " pane-02 "} {
					wantPane, wantOK := loaded.PaneInWindow(w, p)
					gotPane, gotOK := prepared.PaneInWindow(w, p)
					if gotOK != wantOK || (wantOK && !bytes.Equal(mustJSON(t, gotPane), mustJSON(t, wantPane))) || (!wantOK && !reflect.DeepEqual(gotPane, core.Pane{})) {
						t.Fatalf("PaneInWindow(%q, %q): got %v, Registry %v", w, p, gotOK, wantOK)
					}
					seen[gotOK]++
				}
			}
			for _, tc := range bindings {
				bname, b := tc.name, tc.binding
				want, wantProvider, wantOK := loaded.CurrentProcessActivation(metadata.ProcessBinding(b))
				got, provider, ok := prepared.CurrentProcessActivation(b)
				if ok != wantOK || provider != wantProvider {
					t.Fatalf("CurrentProcessActivation(%s): got %v %q, Registry %v %q", bname, ok, provider, wantOK, wantProvider)
				}
				if !bytes.Equal(mustJSON(t, got), mustJSON(t, want)) {
					t.Fatalf("CurrentProcessActivation(%s): public %s, Registry %s", bname, mustJSON(t, got), mustJSON(t, want))
				}
				seen[ok]++
			}
		})
	}
	if seen[true] == 0 || seen[false] == 0 {
		t.Fatalf("variants must include found and refused lookups: %v", seen)
	}
}

// Validate is the Registry's verdict. A Registry that fails it still prepares,
// because the read is a degraded one, and the refusal reaches the cause.
func TestPreparedRegistryValidateVerdict(t *testing.T) {
	valid, loaded := lookupFixture(t, nil)
	if err := loaded.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid Registry refused: %v", err)
	}
	invalid, loadedInvalid := lookupFixture(t, func(r *metadata.Registry) {
		r.Panes[0].Metadata.OwnerRef = &metadata.OwnerRef{Kind: metadata.KindWindow, UID: "window-absent"}
	})
	want := loadedInvalid.Validate()
	if want == nil {
		t.Fatal("fixture must fail validation")
	}
	err := invalid.Validate()
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodePreparedRegistryInvalid {
		t.Fatalf("invalid Registry: %v", err)
	}
	var state *metadata.StateError
	if !errors.Is(err, metadata.ErrInvalidRegistry) || !errors.As(err, &state) || state.Error() != want.Error() {
		t.Fatalf("cause not reachable: %v", err)
	}
	if rows := invalid.Snapshot(); len(rows.Panes) == 0 {
		t.Fatal("an invalid Registry must stay readable")
	}
}

// Nil, zero and unprepared handles answer false, or the unprepared refusal.
func TestPreparedRegistryLookupUnprepared(t *testing.T) {
	for name, h := range map[string]*PreparedRegistrySnapshot{"nil": nil, "zero": {}} {
		t.Run(name, func(t *testing.T) {
			var refusal *Refusal
			if err := h.Validate(); !errors.As(err, &refusal) || refusal.Code != CodePreparedSnapshotInvalid {
				t.Fatalf("Validate: %v", err)
			}
			if p, ok := h.PaneInWindow("window-01", "pane-01"); ok || !reflect.DeepEqual(p, core.Pane{}) {
				t.Fatal("PaneInWindow")
			}
			if p, ok := h.WindowAnchor("window-01"); ok || !reflect.DeepEqual(p, core.Pane{}) {
				t.Fatal("WindowAnchor")
			}
			b := core.ProcessBinding{HostInstanceID: "host-one", ProjectUID: "project-01", WindowUID: "window-01", AgentUID: "agent-01", PaneUID: "pane-02", Generation: "gen-one", OperationID: "op-one"}
			if a, provider, ok := h.CurrentProcessActivation(b); ok || provider != "" || !reflect.DeepEqual(a, core.ProcessActivation{}) {
				t.Fatal("CurrentProcessActivation")
			}
		})
	}
}

// Changing a returned value changes neither the handle nor the next call.
func TestPreparedRegistryLookupResultsAreCopies(t *testing.T) {
	prepared, _ := lookupFixture(t, nil)
	before := mustJSON(t, prepared.Snapshot())
	first, ok := prepared.PaneInWindow("window-01", "pane-02")
	if !ok || first.Metadata.Labels["role"] != "worker" || first.Status.ProcessSession == nil || first.Status.Activation.Process == nil {
		t.Fatalf("fixture Pane: %v %+v", ok, first)
	}
	want := mustJSON(t, first)
	first.Metadata.Labels["role"] = "changed"
	first.Metadata.Labels["added"] = "x"
	first.Status.ProcessSession.Binding.PaneUID = "changed"
	first.Status.ProcessSession.Pending[0].ID = "changed"
	first.Status.Activation.Process.Binding.Generation = "changed"
	anchor, ok := prepared.WindowAnchor("window-01")
	if !ok {
		t.Fatal("anchor")
	}
	anchor.Metadata.Name = "changed"
	binding := first.Status.ProcessSession.Binding
	binding.PaneUID, binding.Generation = "pane-02", "gen-one"
	activation, _, ok := prepared.CurrentProcessActivation(binding)
	if !ok {
		t.Fatal("activation")
	}
	activation.Binding.Generation = "changed"
	activation.Child.PID = -1
	again, _ := prepared.PaneInWindow("window-01", "pane-02")
	if !bytes.Equal(mustJSON(t, again), want) {
		t.Fatal("a changed result altered the next call")
	}
	if !bytes.Equal(mustJSON(t, prepared.Snapshot()), before) {
		t.Fatal("a changed result altered the handle")
	}
	if anchor2, _ := prepared.WindowAnchor("window-01"); anchor2.Metadata.Name == "changed" {
		t.Fatal("a changed anchor altered the handle")
	}
	if again, _, _ := prepared.CurrentProcessActivation(binding); again.Binding.Generation != "gen-one" || again.Child.PID == -1 {
		t.Fatal("a changed activation altered the next call")
	}
}

// One handle serves every lookup from many goroutines.
func TestPreparedRegistryLookupConcurrent(t *testing.T) {
	prepared, _ := lookupFixture(t, nil)
	b := core.ProcessBinding{HostInstanceID: "host-one", ProjectUID: "project-01", WindowUID: "window-01", AgentUID: "agent-01", PaneUID: "pane-02", Generation: "gen-one", OperationID: "op-one"}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if err := prepared.Validate(); err != nil {
				t.Error(err)
			}
			if p, ok := prepared.PaneInWindow("window-01", "pane-02"); !ok {
				t.Error("PaneInWindow")
			} else {
				p.Metadata.Labels["role"] = "changed"
			}
			if _, ok := prepared.WindowAnchor("window-01"); !ok {
				t.Error("WindowAnchor")
			}
			if _, provider, ok := prepared.CurrentProcessActivation(b); !ok || provider != "codex" {
				t.Error("CurrentProcessActivation")
			}
		})
	}
	wg.Wait()
}
