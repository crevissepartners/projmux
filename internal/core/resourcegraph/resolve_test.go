package resourcegraph

import (
	"slices"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// TestResolveClassifiesObservedObjectsFromExactEvidence is the attribution
// matrix. Each case states one machine and asserts the class of one observed
// object, so a rule change shows up as exactly the rows it changed.
func TestResolveClassifiesObservedObjectsFromExactEvidence(t *testing.T) {
	t.Parallel()
	registry := testRegistry(t)

	tests := []struct {
		name      string
		inventory func() Inventory
		id        string
		want      Class
		wantUID   string
		wantOwned string
	}{
		{
			name:      "managed session mirroring a Registry Project",
			inventory: func() Inventory { return liveInventory(HostModeAppOwned) },
			id:        "$1", want: ClassManaged, wantUID: "project-alpha", wantOwned: "project-alpha",
		},
		{
			name:      "managed window inside its own Project session",
			inventory: func() Inventory { return liveInventory(HostModeAppOwned) },
			id:        "@1", want: ClassManaged, wantUID: "win-alpha-1", wantOwned: "win-alpha-1",
		},
		{
			name:      "managed Agent-owned pane verified through the Agent's Window",
			inventory: func() Inventory { return liveInventory(HostModeAppOwned) },
			id:        "%2", want: ClassManaged, wantUID: "pane-alpha-agent", wantOwned: "pane-alpha-agent",
		},
		{
			name: "app-owned session carrying the exact control role",
			inventory: func() Inventory {
				inv := liveInventory(HostModeAppOwned)
				inv.Sessions = append(inv.Sessions, Session{ID: "$9", Name: "home", Role: ControlSessionRole})
				return inv
			},
			id: "$9", want: ClassControl,
		},
		{
			name: "control role on a standalone host is not a control session",
			inventory: func() Inventory {
				inv := liveInventory(HostModeStandalone)
				inv.Sessions = append(inv.Sessions, Session{ID: "$9", Name: "home", Role: ControlSessionRole})
				return inv
			},
			id: "$9", want: ClassForeign,
		},
		{
			name: "unknown role value is not a control session",
			inventory: func() Inventory {
				inv := liveInventory(HostModeAppOwned)
				inv.Sessions = append(inv.Sessions, Session{ID: "$9", Name: "home", Role: "supervisor"})
				return inv
			},
			id: "$9", want: ClassUnattributed,
		},
		{
			name: "ephemeral scratch session",
			inventory: func() Inventory {
				inv := liveInventory(HostModeAppOwned)
				inv.Sessions = append(inv.Sessions, Session{ID: "$9", Name: "scratch-20260818-120000", Ephemeral: true})
				return inv
			},
			id: "$9", want: ClassEphemeral,
		},
		{
			name: "ephemeral outranks a control marker on the same session",
			inventory: func() Inventory {
				inv := liveInventory(HostModeAppOwned)
				inv.Sessions = append(inv.Sessions, Session{ID: "$9", Ephemeral: true, Role: ControlSessionRole})
				return inv
			},
			id: "$9", want: ClassEphemeral,
		},
		{
			name: "unmarked session on an app-owned server is unattributed",
			inventory: func() Inventory {
				inv := liveInventory(HostModeAppOwned)
				inv.Sessions = append(inv.Sessions, Session{ID: "$9", Name: "scratch"})
				return inv
			},
			id: "$9", want: ClassUnattributed,
		},
		{
			name: "unmarked session on a standalone host is foreign",
			inventory: func() Inventory {
				inv := liveInventory(HostModeStandalone)
				inv.Sessions = append(inv.Sessions, Session{ID: "$9", Name: "dotfiles"})
				return inv
			},
			id: "$9", want: ClassForeign,
		},
		{
			name: "unmarked window inside a managed session is unattributed on a standalone host",
			inventory: func() Inventory {
				inv := liveInventory(HostModeStandalone)
				inv.Windows = append(inv.Windows, Window{ID: "@9", SessionID: "$1", Index: "3", DisplayName: "zsh"})
				return inv
			},
			id: "@9", want: ClassUnattributed,
		},
		{
			name: "unmarked window in a foreign session stays foreign on a standalone host",
			inventory: func() Inventory {
				inv := liveInventory(HostModeStandalone)
				inv.Sessions = append(inv.Sessions, Session{ID: "$9", Name: "dotfiles"})
				inv.Windows = append(inv.Windows, Window{ID: "@9", SessionID: "$9", Index: "0", DisplayName: "zsh"})
				return inv
			},
			id: "@9", want: ClassForeign,
		},
		{
			name: "unmarked pane inside a managed window is unattributed",
			inventory: func() Inventory {
				inv := liveInventory(HostModeStandalone)
				inv.Panes = append(inv.Panes, Pane{ID: "%9", WindowID: "@1"})
				return inv
			},
			id: "%9", want: ClassUnattributed,
		},
		{
			name: "unmarked pane in an unmarked window of a managed session is unattributed",
			inventory: func() Inventory {
				inv := liveInventory(HostModeStandalone)
				inv.Windows = append(inv.Windows, Window{ID: "@9", SessionID: "$1", Index: "3"})
				inv.Panes = append(inv.Panes, Pane{ID: "%9", WindowID: "@9"})
				return inv
			},
			id: "%9", want: ClassUnattributed,
		},
		{
			name: "unknown Window uid is recoverable, not managed",
			inventory: func() Inventory {
				inv := liveInventory(HostModeAppOwned)
				inv.Windows = append(inv.Windows, Window{ID: "@9", SessionID: "$1", Index: "3", UID: "win-vanished"})
				return inv
			},
			id: "@9", want: ClassRecoverable, wantUID: "win-vanished",
		},
		{
			name: "unknown Pane uid is recoverable, not managed",
			inventory: func() Inventory {
				inv := liveInventory(HostModeAppOwned)
				inv.Panes = append(inv.Panes, Pane{ID: "%9", WindowID: "@1", UID: "pane-vanished"})
				return inv
			},
			id: "%9", want: ClassRecoverable, wantUID: "pane-vanished",
		},
		{
			name: "unknown Project uid on a session is recoverable",
			inventory: func() Inventory {
				inv := liveInventory(HostModeAppOwned)
				inv.Sessions = append(inv.Sessions, Session{ID: "$9", Name: "ghost", ProjectUID: "project-vanished"})
				return inv
			},
			id: "$9", want: ClassRecoverable, wantUID: "project-vanished",
		},
		{
			name: "duplicate claim on one Window uid refuses both claimants",
			inventory: func() Inventory {
				inv := liveInventory(HostModeAppOwned)
				inv.Windows = append(inv.Windows, Window{ID: "@9", SessionID: "$1", Index: "3", UID: "win-alpha-1"})
				return inv
			},
			id: "@9", want: ClassConflict, wantUID: "win-alpha-1",
		},
		{
			name: "window whose session mirrors another Project is a conflict",
			inventory: func() Inventory {
				inv := liveInventory(HostModeAppOwned)
				inv.Windows[0].SessionID = "$2"
				return inv
			},
			id: "@1", want: ClassConflict, wantUID: "win-alpha-1",
		},
		{
			name: "pane whose window mirrors another Window is a conflict",
			inventory: func() Inventory {
				inv := liveInventory(HostModeAppOwned)
				inv.Panes[0].WindowID = "@2"
				return inv
			},
			id: "%1", want: ClassConflict, wantUID: "pane-alpha-1",
		},
		{
			name: "Pane uid mirrored onto a window option is a kind mismatch",
			inventory: func() Inventory {
				inv := liveInventory(HostModeAppOwned)
				inv.Windows = append(inv.Windows, Window{ID: "@9", SessionID: "$1", Index: "3", UID: "pane-alpha-1"})
				return inv
			},
			id: "@9", want: ClassConflict, wantUID: "pane-alpha-1",
		},
		{
			name: "Agent uid mirrored onto a pane option is a kind mismatch",
			inventory: func() Inventory {
				inv := liveInventory(HostModeAppOwned)
				inv.Panes = append(inv.Panes, Pane{ID: "%9", WindowID: "@1", UID: "agent-alpha-1"})
				return inv
			},
			id: "%9", want: ClassConflict, wantUID: "agent-alpha-1",
		},
		{
			name: "absent containment evidence does not refuse an exact uid",
			inventory: func() Inventory {
				inv := liveInventory(HostModeAppOwned)
				inv.Sessions[0].ProjectUID = ""
				inv.Windows[0].UID = ""
				return inv
			},
			id: "%1", want: ClassManaged, wantUID: "pane-alpha-1", wantOwned: "pane-alpha-1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			graph := Resolve(registry, test.inventory())
			node := runtimeNode(t, graph, test.id)
			if node.Class != test.want {
				t.Fatalf("runtime %s class = %q, want %q (reason %q)", test.id, node.Class, test.want, node.Reason)
			}
			if node.UID != test.wantUID {
				t.Fatalf("runtime %s mirrored uid = %q, want %q", test.id, node.UID, test.wantUID)
			}
			if node.ResourceUID != test.wantOwned {
				t.Fatalf("runtime %s resource uid = %q, want %q", test.id, node.ResourceUID, test.wantOwned)
			}
			if node.Class != ClassManaged && node.ResourceUID != "" {
				t.Fatalf("runtime %s is %q yet carries resource uid %q", test.id, node.Class, node.ResourceUID)
			}
			if node.Class != ClassManaged && node.Reason == "" {
				t.Fatalf("runtime %s is %q with no stated reason", test.id, node.Class)
			}
		})
	}
}

// TestResolveOverlaysRuntimeStatusOntoRegistryRows pins the status derivation for
// every kind, including the two rows that have no live object and the Project
// whose root disappeared.
func TestResolveOverlaysRuntimeStatusOntoRegistryRows(t *testing.T) {
	t.Parallel()
	registry := testRegistry(t)
	graph := Resolve(registry, liveInventory(HostModeAppOwned))

	if got := len(graph.Projects); got != 3 {
		t.Fatalf("Project rows = %d, want 3", got)
	}
	alpha := projectNode(t, graph, "project-alpha")
	if alpha.Status != StatusLive || alpha.Class != ClassManaged {
		t.Fatalf("alpha = %q/%q, want live/managed", alpha.Status, alpha.Class)
	}
	if alpha.Runtime == nil || alpha.Runtime.ID != "$1" || alpha.Runtime.Target != "alpha" {
		t.Fatalf("alpha runtime ref = %+v, want $1/alpha", alpha.Runtime)
	}
	gone := projectNode(t, graph, "project-gone")
	if gone.Status != StatusMissingRoot || !gone.MissingRoot {
		t.Fatalf("gone = %q missingRoot=%v, want missing-root/true", gone.Status, gone.MissingRoot)
	}
	if gone.Runtime != nil {
		t.Fatalf("gone has runtime ref %+v, want none", gone.Runtime)
	}

	live := windowNode(t, graph, "win-alpha-1")
	if live.Status != StatusLive || live.Runtime == nil || live.Runtime.ID != "@1" || live.Runtime.Target != "alpha:0" {
		t.Fatalf("win-alpha-1 = %q ref %+v, want live @1 alpha:0", live.Status, live.Runtime)
	}
	if live.ProjectUID != "project-alpha" {
		t.Fatalf("win-alpha-1 project = %q, want project-alpha", live.ProjectUID)
	}
	offline := windowNode(t, graph, "win-alpha-2")
	if offline.Status != StatusOffline || offline.Runtime != nil {
		t.Fatalf("win-alpha-2 = %q ref %+v, want offline/none", offline.Status, offline.Runtime)
	}
	// A Window under a rootless Project reports the Project's problem, not its
	// own absence.
	if node := windowNode(t, graph, "win-gone-1"); node.Status != StatusMissingRoot {
		t.Fatalf("win-gone-1 = %q, want missing-root", node.Status)
	}

	agentPane := paneNode(t, graph, "pane-alpha-agent")
	if agentPane.Status != StatusLive || agentPane.AgentUID != "agent-alpha-1" || agentPane.WindowUID != "win-alpha-1" {
		t.Fatalf("pane-alpha-agent = %q agent %q window %q, want live/agent-alpha-1/win-alpha-1",
			agentPane.Status, agentPane.AgentUID, agentPane.WindowUID)
	}
	if agentPane.ProjectUID != "project-alpha" {
		t.Fatalf("pane-alpha-agent project = %q, want project-alpha", agentPane.ProjectUID)
	}

	running := agentNode(t, graph, "agent-alpha-1")
	if running.Status != StatusLive || running.Runtime == nil || running.Runtime.ID != "%2" {
		t.Fatalf("agent-alpha-1 = %q ref %+v, want live %%2", running.Status, running.Runtime)
	}
	if running.Agent.Status.Phase != coremetadata.PhaseRunning {
		t.Fatalf("agent-alpha-1 phase = %q, want the Registry value verbatim", running.Agent.Status.Phase)
	}
	paneless := agentNode(t, graph, "agent-alpha-2")
	if paneless.Status != StatusOffline || paneless.Runtime != nil || paneless.PaneUID != "" {
		t.Fatalf("agent-alpha-2 = %q ref %+v pane %q, want offline/none/empty",
			paneless.Status, paneless.Runtime, paneless.PaneUID)
	}
}

// TestResolveRecordsContradictionsAndRefusesToBind checks the other half of a
// conflict: the Registry row is preserved, is never reported live, and never
// hands a consumer a transport handle it could mutate.
func TestResolveRecordsContradictionsAndRefusesToBind(t *testing.T) {
	t.Parallel()
	registry := testRegistry(t)
	inventory := liveInventory(HostModeAppOwned)
	inventory.Windows = append(inventory.Windows,
		Window{ID: "@9", SessionID: "$1", Index: "3", UID: "win-alpha-1"})
	graph := Resolve(registry, inventory)

	row := windowNode(t, graph, "win-alpha-1")
	if row.Class != ClassConflict {
		t.Fatalf("contradicted row class = %q, want conflict", row.Class)
	}
	if row.Status == StatusLive {
		t.Fatalf("contradicted row reported live")
	}
	if row.Runtime != nil {
		t.Fatalf("contradicted row handed out transport handle %+v", row.Runtime)
	}
	if len(graph.Conflicts) != 1 {
		t.Fatalf("conflicts = %d, want 1: %+v", len(graph.Conflicts), graph.Conflicts)
	}
	conflict := graph.Conflicts[0]
	if conflict.Reason != ConflictDuplicateClaim || conflict.UID != "win-alpha-1" || conflict.Kind != ObjectWindow {
		t.Fatalf("conflict = %+v, want duplicate window claim on win-alpha-1", conflict)
	}
	if !slices.Equal(conflict.Targets, []string{"@1", "@9"}) {
		t.Fatalf("conflict targets = %v, want both claimants sorted", conflict.Targets)
	}
	if conflict.Detail == "" {
		t.Fatalf("conflict carries no detail")
	}
}

// TestResolveNeverBindsAcrossAProjectBoundary is the owner-chain invariant. It
// crosses every level of the runtime containment on purpose and then asserts that
// no surviving binding disagrees with the Registry owner chain.
func TestResolveNeverBindsAcrossAProjectBoundary(t *testing.T) {
	t.Parallel()
	registry := testRegistry(t)
	inventory := liveInventory(HostModeAppOwned)
	// alpha's Window is moved under beta's session and beta's Pane under alpha's
	// Window: every crossing exact evidence can express.
	inventory.Windows[0].SessionID = "$2"
	inventory.Panes[2].WindowID = "@1"
	graph := Resolve(registry, inventory)

	projectOfSession := map[string]string{}
	for _, session := range inventory.Sessions {
		projectOfSession[session.ID] = session.ProjectUID
	}
	windowByID := map[string]Window{}
	for _, window := range inventory.Windows {
		windowByID[window.ID] = window
	}

	for _, node := range graph.Windows {
		if node.Runtime == nil {
			continue
		}
		window := windowByID[node.Runtime.ID]
		if observed := projectOfSession[window.SessionID]; observed != "" && observed != node.ProjectUID {
			t.Fatalf("Window %s bound to %s while its session mirrors %s",
				node.Window.Metadata.UID, node.ProjectUID, observed)
		}
	}
	for _, node := range graph.Panes {
		if node.Runtime == nil {
			continue
		}
		var pane Pane
		for _, candidate := range inventory.Panes {
			if candidate.ID == node.Runtime.ID {
				pane = candidate
			}
		}
		window := windowByID[pane.WindowID]
		if window.UID != "" && window.UID != node.WindowUID {
			t.Fatalf("Pane %s bound under window %s while its tmux window mirrors %s",
				node.Pane.Metadata.UID, node.WindowUID, window.UID)
		}
		if observed := projectOfSession[window.SessionID]; observed != "" && observed != node.ProjectUID {
			t.Fatalf("Pane %s bound under project %s while its session mirrors %s",
				node.Pane.Metadata.UID, node.ProjectUID, observed)
		}
	}
	// Moving one window across a session boundary crosses its panes with it, so
	// the crossing is reported for the window and for every pane inside it, plus
	// the beta pane moved the other way.
	if len(graph.Conflicts) != 4 {
		t.Fatalf("conflicts = %d, want one per crossed object: %+v", len(graph.Conflicts), graph.Conflicts)
	}
	for _, conflict := range graph.Conflicts {
		if conflict.Reason != ConflictOwnerMismatch {
			t.Fatalf("conflict %+v, want owner-mismatch", conflict)
		}
	}
}
