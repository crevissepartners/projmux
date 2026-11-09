package projmux

import (
	"reflect"
	"testing"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	runtime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// For an Agent with a row, ProcessAgentPane equals the internal
// runtime.ProcessAgentPane on the same Registry; the expected Pane and
// ambiguity are always built from that function's own result and
// publicPaneCopy, never from a literal.
func TestProcessAgentPaneEqualsInternal(t *testing.T) {
	prepared, reg := relaunchHostFixture(t)
	for _, tc := range []struct {
		name     string
		uid      string
		wantPane string // fixes the fixture shape, not the compared value
		wantAmb  bool
	}{
		{"no-pane", "agent-rh-none", "", false},
		{"tmux-pane-only", "agent-rh-tmux", "", false},
		{"one-process-pane", "agent-rh-proc1", "pane-rh-proc1", false},
		{"two-process-panes-ambiguous", "agent-rh-proc2", "", true},
		{"tmux-and-one-process-pane", "agent-rh-mixed", "pane-rh-mixed-p", false},
		{"fixture-tmux-agent", "agent-21", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found, wantAmb := runtime.ProcessAgentPane(reg, tc.uid)
			wantPane, wantOK := publicPaneCopy(found, found != nil)
			got, gotAmb, gotOK := prepared.ProcessAgentPane(tc.uid)
			if gotAmb != wantAmb || gotOK != wantOK || !reflect.DeepEqual(got, wantPane) {
				t.Fatalf("ProcessAgentPane(%q) = (%+v, %v, %v), internal = (%+v, %v, %v)",
					tc.uid, got, gotAmb, gotOK, wantPane, wantAmb, wantOK)
			}
			if wantOK && found.Metadata.UID != got.Metadata.UID {
				t.Fatalf("Pane UID = %q, internal = %q", got.Metadata.UID, found.Metadata.UID)
			}
			if got.Metadata.UID != tc.wantPane || gotAmb != tc.wantAmb || gotOK != (tc.wantPane != "") {
				t.Fatalf("fixture shape changed: pane %q ambiguous %v ok %v, want pane %q ambiguous %v",
					got.Metadata.UID, gotAmb, gotOK, tc.wantPane, tc.wantAmb)
			}
		})
	}
}

// A handle that was not prepared, an agentUID with no Agent row, and a uid that
// is not an Agent answer (zero, false, false) with no error.
func TestProcessAgentPaneNotFoundForUnpreparedHandleAndMissingAgent(t *testing.T) {
	prepared, _ := relaunchHostFixture(t)
	for _, uid := range []string{"agent-does-not-exist", "", "window-01", "pane-31"} {
		if pane, amb, ok := prepared.ProcessAgentPane(uid); ok || amb || !reflect.DeepEqual(pane, core.Pane{}) {
			t.Fatalf("ProcessAgentPane(%q) = (%+v, %v, %v), want the zero answer", uid, pane, amb, ok)
		}
	}
	var nilHandle *PreparedRegistrySnapshot
	if pane, amb, ok := nilHandle.ProcessAgentPane("agent-rh-proc1"); ok || amb || !reflect.DeepEqual(pane, core.Pane{}) {
		t.Fatalf("nil handle = (%+v, %v, %v), want the zero answer", pane, amb, ok)
	}
	if pane, amb, ok := (&PreparedRegistrySnapshot{}).ProcessAgentPane("agent-rh-proc1"); ok || amb || !reflect.DeepEqual(pane, core.Pane{}) {
		t.Fatalf("zero handle = (%+v, %v, %v), want the zero answer", pane, amb, ok)
	}
}

// A process Pane whose owner is a uid with no Agent row is an orphan. The
// internal rule does not check for the Agent row, so it finds that Pane; the
// public method answers the zero answer because there is no Agent row.
func TestProcessAgentPaneNotFoundForMissingAgentWithOrphanProcessPane(t *testing.T) {
	prepared, reg := cascadeFixture(t, func(r *metadata.Registry) {
		r.Panes = append(r.Panes, cascadePane("pane-pa-orphan",
			metadata.OwnerRef{Kind: metadata.KindAgent, UID: "agent-pa-gone"},
			metadata.PaneRoleAgent, metadata.RuntimeProcess))
	})
	if _, ok := reg.Agent("agent-pa-gone"); ok {
		t.Fatal("fixture shape changed: agent-pa-gone has an Agent row")
	}
	if found, amb := runtime.ProcessAgentPane(reg, "agent-pa-gone"); found == nil || amb || found.Metadata.UID != "pane-pa-orphan" {
		t.Fatalf("internal rule = (%v, %v), want pane-pa-orphan (it does not check for the Agent row)", found, amb)
	}
	if pane, amb, ok := prepared.ProcessAgentPane("agent-pa-gone"); ok || amb || !reflect.DeepEqual(pane, core.Pane{}) {
		t.Fatalf("ProcessAgentPane(agent-pa-gone) = (%+v, %v, %v), want the zero answer", pane, amb, ok)
	}
}

// Changing the returned Pane changes neither the next answer of the same handle
// nor Snapshot().
func TestProcessAgentPaneReturnsACopy(t *testing.T) {
	prepared, _ := relaunchHostFixture(t)
	before := prepared.Snapshot()
	first, _, ok := prepared.ProcessAgentPane("agent-rh-proc1")
	if !ok {
		t.Fatal("fixture shape changed: agent-rh-proc1 has no single process Pane")
	}
	want, _, _ := prepared.ProcessAgentPane("agent-rh-proc1")
	first.Metadata.Name = "changed"
	first.Metadata.Labels = map[string]string{"changed": "yes"}
	if first.Metadata.OwnerRef != nil {
		first.Metadata.OwnerRef.UID = "changed"
	}
	again, _, _ := prepared.ProcessAgentPane("agent-rh-proc1")
	if !reflect.DeepEqual(again, want) {
		t.Fatalf("next answer changed after the caller edited the copy: %+v, want %+v", again, want)
	}
	if !reflect.DeepEqual(prepared.Snapshot(), before) {
		t.Fatal("Snapshot() changed after the caller edited the copy")
	}
}
