package projmux

import (
	"bytes"
	"reflect"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

func cascadeWindow(uid, anchor string) metadata.Window {
	return metadata.Window{
		APIVersion: metadata.APIVersion, Kind: metadata.KindWindow,
		Metadata: metadata.ObjectMeta{UID: uid, Name: uid + "-name", OwnerRef: &metadata.OwnerRef{Kind: metadata.KindProject, UID: "project-01"}},
		Spec:     metadata.WindowSpec{AnchorPaneRef: anchor},
	}
}

func cascadeAgent(uid, windowUID, paneRef string) metadata.Agent {
	return metadata.Agent{
		APIVersion: metadata.APIVersion, Kind: metadata.KindAgent,
		Metadata: metadata.ObjectMeta{UID: uid, Name: uid + "-name", OwnerRef: &metadata.OwnerRef{Kind: metadata.KindWindow, UID: windowUID}},
		Spec:     metadata.AgentSpec{Provider: "codex"},
		Status:   metadata.AgentStatus{Phase: metadata.PhaseRunning, PaneRef: paneRef},
	}
}

func cascadePane(uid string, owner metadata.OwnerRef, role metadata.PaneRole, runtime metadata.RuntimeKind) metadata.Pane {
	return metadata.Pane{
		APIVersion: metadata.APIVersion, Kind: metadata.KindPane,
		Metadata: metadata.ObjectMeta{UID: uid, Name: uid + "-name", OwnerRef: &owner},
		Spec:     metadata.PaneSpec{Role: role, Runtime: metadata.PaneRuntimeSpec{Kind: runtime}},
	}
}

// cascadeFixture prepares the process fixture and adds Windows covering each
// cascade shape:
//
//	window-01: Agent agent-01 with pane-02, shell pane-01 (not virtual)
//	window-02: Agents agent-21 and agent-22 with pane-23 and pane-24, shell
//	           pane-21, and a second Window-owned shell pane-22 (not virtual)
//	window-03: virtual, anchored on pane-32, the only Pane of agent-31
//	window-04: virtual, anchored on pane-42; agent-42 keeps pane-43
//
// It returns the prepared handle and the Registry the same file loads as.
func cascadeFixture(t *testing.T, change func(*metadata.Registry)) (*PreparedRegistrySnapshot, metadata.Registry) {
	t.Helper()
	win := func(uid string) metadata.OwnerRef { return metadata.OwnerRef{Kind: metadata.KindWindow, UID: uid} }
	agt := func(uid string) metadata.OwnerRef { return metadata.OwnerRef{Kind: metadata.KindAgent, UID: uid} }
	c, paths := graphFixtureClient(t, mutatedGraphFixture(t, func(r *metadata.Registry) {
		r.Windows = append(r.Windows,
			cascadeWindow("window-02", "pane-21"),
			cascadeWindow("window-03", "pane-32"),
			cascadeWindow("window-04", "pane-42"),
		)
		r.Agents = append(r.Agents,
			cascadeAgent("agent-21", "window-02", "pane-23"),
			cascadeAgent("agent-22", "window-02", "pane-24"),
			cascadeAgent("agent-31", "window-03", "pane-32"),
			cascadeAgent("agent-41", "window-04", "pane-42"),
			cascadeAgent("agent-42", "window-04", "pane-43"),
		)
		r.Panes = append(r.Panes,
			cascadePane("pane-21", win("window-02"), metadata.PaneRoleShell, metadata.RuntimeTmux),
			cascadePane("pane-22", win("window-02"), metadata.PaneRoleShell, metadata.RuntimeTmux),
			cascadePane("pane-23", agt("agent-21"), metadata.PaneRoleAgent, metadata.RuntimeTmux),
			cascadePane("pane-24", agt("agent-22"), metadata.PaneRoleAgent, metadata.RuntimeTmux),
			cascadePane("pane-32", agt("agent-31"), metadata.PaneRoleAgent, metadata.RuntimeProcess),
			cascadePane("pane-42", agt("agent-41"), metadata.PaneRoleAgent, metadata.RuntimeProcess),
			cascadePane("pane-43", agt("agent-42"), metadata.PaneRoleAgent, metadata.RuntimeProcess),
		)
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

// internalCascade is the expected value: the internal collector's own result
// moved item by item, never a hand-written list.
func internalCascade(reg metadata.Registry, kind core.Kind, uid string) []core.DeleteResource {
	cascade := mutations.CascadeOf(reg, metadata.Kind(kind), uid)
	if cascade == nil {
		return nil
	}
	out := make([]core.DeleteResource, 0, len(cascade))
	for _, item := range cascade {
		out = append(out, core.DeleteResource{Kind: core.Kind(item.Kind), UID: item.UID, Name: item.Name})
	}
	return out
}

// DeleteCascade returns the internal CascadeOf items in the same order and with
// the same nil-ness for every kind and for each virtual Window shape.
func TestDeleteCascadeEqualsInternal(t *testing.T) {
	prepared, reg := cascadeFixture(t, nil)
	for _, tc := range []struct {
		name string
		kind core.Kind
		uid  string
		want int // expected item count; fixes the fixture shape, not the value
	}{
		{"project", core.KindProject, "project-01", 19},
		{"window-agent-and-direct-panes", core.KindWindow, "window-02", 6},
		{"window-single-agent", core.KindWindow, "window-01", 3},
		{"agent", core.KindAgent, "agent-21", 1},
		{"pane-leaf", core.KindPane, "pane-23", 0},
		{"virtual-last-agent", core.KindAgent, "agent-31", 2},
		{"virtual-last-pane", core.KindPane, "pane-32", 2},
		{"virtual-sibling-remains-agent", core.KindAgent, "agent-41", 1},
		{"virtual-sibling-remains-pane", core.KindPane, "pane-42", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := prepared.DeleteCascade(tc.kind, tc.uid)
			want := internalCascade(reg, tc.kind, tc.uid)
			if !reflect.DeepEqual(got, want) || (got == nil) != (want == nil) {
				t.Fatalf("DeleteCascade(%s, %s) = %+v, internal = %+v", tc.kind, tc.uid, got, want)
			}
			if len(got) != tc.want {
				t.Fatalf("fixture shape changed: %d items, want %d: %+v", len(got), tc.want, got)
			}
		})
	}
}

// The virtual Window tail is the parent Window followed by the Agents that stay
// in it, and only a Window whose last Pane goes has one. Deleting the last
// Agent lists its Pane and then the Window; deleting the last Pane lists the
// Window and then the Agent.
func TestDeleteCascadeVirtualWindowTail(t *testing.T) {
	prepared, _ := cascadeFixture(t, nil)
	window3 := core.DeleteResource{Kind: core.KindWindow, UID: "window-03", Name: "window-03-name"}
	pane32 := core.DeleteResource{Kind: core.KindPane, UID: "pane-32", Name: "pane-32-name"}
	if got, want := prepared.DeleteCascade(core.KindAgent, "agent-31"), []core.DeleteResource{pane32, window3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("last Agent of a virtual Window: %+v, want %+v", got, want)
	}
	if got, want := prepared.DeleteCascade(core.KindPane, "pane-32"), []core.DeleteResource{window3, {Kind: core.KindAgent, UID: "agent-31", Name: "agent-31-name"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("last Pane of a virtual Window: %+v, want %+v", got, want)
	}
	if got, want := prepared.DeleteCascade(core.KindAgent, "agent-41"), []core.DeleteResource{{Kind: core.KindPane, UID: "pane-42", Name: "pane-42-name"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Agent with a sibling Pane left has no Window tail: %+v, want %+v", got, want)
	}
	if got := prepared.DeleteCascade(core.KindPane, "pane-42"); got != nil {
		t.Fatalf("Pane with a sibling Pane left: %+v", got)
	}
}

// A handle that was not prepared, an unknown uid and an unknown kind return nil
// and never fail.
func TestDeleteCascadeNilForNothingToCascade(t *testing.T) {
	prepared, _ := cascadeFixture(t, nil)
	var nilHandle *PreparedRegistrySnapshot
	for name, handle := range map[string]*PreparedRegistrySnapshot{"nil": nilHandle, "zero": {}} {
		for _, kind := range []core.Kind{core.KindProject, core.KindWindow, core.KindAgent, core.KindPane} {
			if got := handle.DeleteCascade(kind, "project-01"); got != nil {
				t.Fatalf("%s handle, %s: %+v", name, kind, got)
			}
		}
	}
	for _, tc := range []struct {
		name string
		kind core.Kind
		uid  string
	}{
		{"absent-project", core.KindProject, "project-absent"},
		{"absent-window", core.KindWindow, "window-absent"},
		{"absent-agent", core.KindAgent, "agent-absent"},
		{"absent-pane", core.KindPane, "pane-absent"},
		{"empty-uid", core.KindProject, ""},
		{"unknown-kind", core.Kind("Unknown"), "project-01"},
		{"empty-kind", core.Kind(""), "project-01"},
		{"uid-of-another-kind", core.KindWindow, "project-01"},
	} {
		if got := prepared.DeleteCascade(tc.kind, tc.uid); got != nil {
			t.Fatalf("%s: %+v", tc.name, got)
		}
	}
}

// The result is the caller's: changing it does not change the handle or the
// next answer.
func TestDeleteCascadeResultIsCopied(t *testing.T) {
	prepared, _ := cascadeFixture(t, nil)
	before := mustJSON(t, prepared.Snapshot())
	want := prepared.DeleteCascade(core.KindProject, "project-01")
	if len(want) == 0 {
		t.Fatal("fixture Project has no cascade")
	}
	got := prepared.DeleteCascade(core.KindProject, "project-01")
	for i := range got {
		got[i].UID, got[i].Name = "changed", "changed"
	}
	if again := prepared.DeleteCascade(core.KindProject, "project-01"); !reflect.DeepEqual(again, want) {
		t.Fatalf("a changed result altered the next call: %+v", again)
	}
	if !bytes.Equal(mustJSON(t, prepared.Snapshot()), before) {
		t.Fatal("a changed result altered the handle")
	}
}

// The method collects by owner references like CascadeOf and does not check that
// the target is an actual resource: a Window whose Project is missing is still
// listed under that Project uid.
func TestDeleteCascadeOrphanWindowFollowsCascadeOf(t *testing.T) {
	win := func(uid string) metadata.OwnerRef { return metadata.OwnerRef{Kind: metadata.KindWindow, UID: uid} }
	agt := func(uid string) metadata.OwnerRef { return metadata.OwnerRef{Kind: metadata.KindAgent, UID: uid} }
	prepared, reg := cascadeFixture(t, func(r *metadata.Registry) {
		orphan := cascadeWindow("window-90", "pane-91")
		orphan.Metadata.OwnerRef = &metadata.OwnerRef{Kind: metadata.KindProject, UID: "project-absent"}
		r.Windows = append(r.Windows, orphan)
		r.Agents = append(r.Agents, cascadeAgent("agent-91", "window-90", "pane-92"))
		r.Panes = append(r.Panes,
			cascadePane("pane-91", win("window-90"), metadata.PaneRoleShell, metadata.RuntimeTmux),
			cascadePane("pane-92", agt("agent-91"), metadata.PaneRoleAgent, metadata.RuntimeTmux),
		)
	})
	got := prepared.DeleteCascade(core.KindProject, "project-absent")
	want := internalCascade(reg, core.KindProject, "project-absent")
	if len(got) == 0 {
		t.Fatalf("an orphan Window cascade was dropped: %+v", got)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeleteCascade(Project, project-absent) = %+v, internal = %+v", got, want)
	}
	if got[0].Kind != core.KindWindow || got[0].UID != "window-90" {
		t.Fatalf("first item: %+v", got[0])
	}
}

// A uid of the wrong kind is collected by owner references too: an Agent lookup
// with a Window uid returns the Panes that Window owns directly.
func TestDeleteCascadeWrongKindUIDFollowsCascadeOf(t *testing.T) {
	prepared, reg := cascadeFixture(t, nil)
	got := prepared.DeleteCascade(core.KindAgent, "window-02")
	want := internalCascade(reg, core.KindAgent, "window-02")
	if len(got) == 0 {
		t.Fatalf("a wrong-kind uid cascade was dropped: %+v", got)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeleteCascade(Agent, window-02) = %+v, internal = %+v", got, want)
	}
	for _, item := range got {
		if item.Kind != core.KindPane {
			t.Fatalf("non-Pane item for an Agent lookup: %+v", item)
		}
	}
}
