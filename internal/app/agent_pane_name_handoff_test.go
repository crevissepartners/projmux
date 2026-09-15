package app

import (
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// paneNameReservationHolders lists the uids holding a Pane name reservation in
// one root scope.
func paneNameReservationHolders(registry coremetadata.Registry, scope, name string) []string {
	var holders []string
	for _, reservation := range registry.NameReservations {
		if reservation.Scope == scope && reservation.Kind == coremetadata.KindPane && reservation.Name == name {
			holders = append(holders, reservation.UID)
		}
	}
	return holders
}

// TestSelectAgentPaneNameHandoffTable pins the one name-selection rule Continue
// replay and `agent resume` share: only non-live, Agent-owned, non-automatic
// rows are candidates; several candidates are resolved only by the last
// termination receipt Pane; a name another resource reserves is never carried.
func TestSelectAgentPaneNameHandoffTable(t *testing.T) {
	const agentUID, otherAgentUID = "agt-handoff", "agt-other"
	agentPane := func(uid, name string) coremetadata.Pane {
		return coremetadata.Pane{
			Metadata: coremetadata.ObjectMeta{UID: uid, Name: name, OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindAgent, UID: agentUID}},
			Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleAgent},
		}
	}
	otherAgentPane := agentPane("pan-other", "notes")
	otherAgentPane.Metadata.OwnerRef.UID = otherAgentUID
	shellPane := coremetadata.Pane{
		Metadata: coremetadata.ObjectMeta{UID: "pan-shell", Name: "notes", OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-handoff"}},
		Spec:     coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell},
	}
	for _, test := range []struct {
		name         string
		panes        []coremetadata.Pane
		reservations map[string]string
		nonLive      []string
		receiptPane  string
		want         string
		wantSource   string
		wantReason   string
	}{
		{name: "no old rows carry nothing"},
		{
			name:    "an automatic UID name is never carried",
			panes:   []coremetadata.Pane{agentPane("pan-auto", "pan-auto")},
			nonLive: []string{"pan-auto"},
		},
		{
			name:    "an explicit name is carried",
			panes:   []coremetadata.Pane{agentPane("pan-a", "reviewer")},
			nonLive: []string{"pan-a"}, want: "reviewer", wantSource: "pan-a",
		},
		{
			name:    "the create agent default name is carried",
			panes:   []coremetadata.Pane{agentPane("pan-a", "reviewer-pane")},
			nonLive: []string{"pan-a"}, want: "reviewer-pane", wantSource: "pan-a",
		},
		{
			name:    "a flag-shaped name is carried",
			panes:   []coremetadata.Pane{agentPane("pan-a", "-L")},
			nonLive: []string{"pan-a"}, want: "-L", wantSource: "pan-a",
		},
		{
			name:  "a row not proven non-live is not a candidate",
			panes: []coremetadata.Pane{agentPane("pan-a", "reviewer")},
		},
		{
			name:    "another Agent's row is not a candidate",
			panes:   []coremetadata.Pane{otherAgentPane},
			nonLive: []string{"pan-other"},
		},
		{
			name:    "a Window-owned shell row is not a candidate",
			panes:   []coremetadata.Pane{shellPane},
			nonLive: []string{"pan-shell"},
		},
		{
			name:    "one named row beside an automatic receipt row is carried",
			panes:   []coremetadata.Pane{agentPane("pan-a", "reviewer"), agentPane("pan-c", "pan-c")},
			nonLive: []string{"pan-a", "pan-c"}, receiptPane: "pan-c", want: "reviewer", wantSource: "pan-a",
		},
		{
			name:    "several candidates carry the receipt Pane's name",
			panes:   []coremetadata.Pane{agentPane("pan-a", "first"), agentPane("pan-b", "second")},
			nonLive: []string{"pan-a", "pan-b"}, receiptPane: "pan-b", want: "second", wantSource: "pan-b",
		},
		{
			name:       "several candidates without a receipt carry nothing",
			panes:      []coremetadata.Pane{agentPane("pan-a", "first"), agentPane("pan-b", "second")},
			nonLive:    []string{"pan-a", "pan-b"},
			wantReason: "old Panes pane/first (uid:pan-a), pane/second (uid:pan-b) carry names and the last termination receipt selects none of them",
		},
		{
			name:        "several candidates whose receipt is an automatic row carry nothing",
			panes:       []coremetadata.Pane{agentPane("pan-a", "first"), agentPane("pan-b", "second"), agentPane("pan-c", "pan-c")},
			nonLive:     []string{"pan-a", "pan-b", "pan-c"},
			receiptPane: "pan-c",
			wantReason:  "old Panes pane/first (uid:pan-a), pane/second (uid:pan-b) carry names and the last termination receipt selects none of them",
		},
		{
			name:         "a name reserved by another resource is not carried",
			panes:        []coremetadata.Pane{agentPane("pan-a", "notes"), shellPane},
			reservations: map[string]string{"notes": "pan-shell"},
			nonLive:      []string{"pan-a"},
			wantReason:   `name "notes" is reserved by pan-shell`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := coremetadata.NewRegistry()
			registry.Projects = []coremetadata.Project{{Metadata: coremetadata.ObjectMeta{UID: "prj-handoff", Name: "handoff"}}}
			registry.Windows = []coremetadata.Window{{Metadata: coremetadata.ObjectMeta{
				UID: "win-handoff", Name: "main", OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: "prj-handoff"},
			}}}
			agent := coremetadata.Agent{Metadata: coremetadata.ObjectMeta{
				UID: agentUID, Name: "reviewer", OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-handoff"},
			}}
			if test.receiptPane != "" {
				agent.Status.LastTermination = &coremetadata.TerminationEvidence{AgentUID: agentUID, PaneUID: test.receiptPane}
			}
			registry.Agents = []coremetadata.Agent{agent, {Metadata: coremetadata.ObjectMeta{
				UID: otherAgentUID, Name: "other", OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-handoff"},
			}}}
			reserved := map[string]bool{}
			for name, uid := range test.reservations {
				registry.NameReservations = append(registry.NameReservations, coremetadata.NameReservation{
					Scope: "prj-handoff", Kind: coremetadata.KindPane, Name: name, UID: uid,
				})
				reserved[name] = true
			}
			for _, pane := range test.panes {
				registry.Panes = append(registry.Panes, pane)
				if !reserved[pane.Metadata.Name] {
					registry.NameReservations = append(registry.NameReservations, coremetadata.NameReservation{
						Scope: "prj-handoff", Kind: coremetadata.KindPane, Name: pane.Metadata.Name, UID: pane.Metadata.UID,
					})
					reserved[pane.Metadata.Name] = true
				}
			}

			got := selectAgentPaneNameHandoff(registry, agent, test.nonLive)
			if got.name != test.want || got.sourceUID != test.wantSource || got.reason != test.wantReason {
				t.Fatalf("handoff = %+v, want name=%q source=%q reason=%q", got, test.want, test.wantSource, test.wantReason)
			}
		})
	}
}

// TestAttachAgentPaneWithNameFallsBackToAutomaticName is the mutation-side
// half of the rule: a name the Registry refuses when the new Pane is attached
// yields an automatic name and one reason, never a failure.
func TestAttachAgentPaneWithNameFallsBackToAutomaticName(t *testing.T) {
	for _, test := range []struct {
		name, paneName, wantReason string
		automatic                  bool
	}{
		{name: "a free name is carried", paneName: "reviewer"},
		{name: "a flag-shaped name is carried", paneName: "-L"},
		{name: "no name stays automatic", automatic: true},
		{name: "a name another Pane holds falls back", paneName: "zsh", automatic: true, wantReason: `pane name "zsh" is already used by pan-beta-zsh`},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newFakeResourceStore(t)
			registry := store.registry.Clone()
			pane, reason, err := attachAgentPaneWithName(&registry, store.mutator(), "agt-beta-codex", "/srv/beta", test.paneName, "op-name-handoff")
			if err != nil {
				t.Fatalf("attach error = %v", err)
			}
			want := test.paneName
			if test.automatic {
				want = pane.Metadata.UID
			}
			if pane.Metadata.Name != want || !strings.Contains(reason, test.wantReason) || (test.wantReason == "") != (reason == "") {
				t.Fatalf("attached pane/%s (uid:%s) reason=%q, want name %q reason %q", pane.Metadata.Name, pane.Metadata.UID, reason, want, test.wantReason)
			}
			if agent, _ := registry.Agent("agt-beta-codex"); agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != pane.Metadata.UID {
				t.Fatalf("Agent status = %+v, want Running on the attached Pane", agent.Status)
			}
			if err := registry.Validate(); err != nil {
				t.Fatalf("attached Registry is invalid: %v", err)
			}
		})
	}
}
