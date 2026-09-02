package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/core/controller"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

func seedAuthorshipPromotionIncident(t *testing.T, store *fakeResourceStore, server *fakeTmux, root string) (*fakeTmuxPane, coremetadata.Window, coremetadata.Pane) {
	t.Helper()
	project, _ := store.registry.ProjectByRoot(root)
	window := store.registry.WindowsOf(project.Metadata.UID)[0]
	pane := store.registry.PanesOf(window.Metadata.UID)[0]
	if _, err := store.mutator().CreateAgent(&store.registry, window.Metadata.UID, coremetadata.CreateAgentOptions{
		Provider: "codex", OperationID: "op-existing-codex",
	}); err != nil {
		t.Fatalf("seed existing codex Agent: %v", err)
	}
	session := server.session("alpha")
	liveWindow, livePane := session.windows[0], session.windows[0].panes[0]
	session.opts[tmuxopts.ProjectUIDSession] = project.Metadata.UID
	session.opts[tmuxopts.ProjectNameSession] = project.Metadata.Name
	liveWindow.opts[tmuxopts.WindowUID] = window.Metadata.UID
	liveWindow.opts[tmuxopts.WindowName] = window.Metadata.Name
	liveWindow.opts[tmuxopts.AutomaticRenameWindow] = "off"
	livePane.opts[tmuxopts.PaneUID] = pane.Metadata.UID
	livePane.opts[tmuxopts.PaneName] = pane.Metadata.Name
	livePane.opts[tmuxopts.AgentProviderPane] = "codex"
	livePane.opts[tmuxopts.AgentLaunchAuthorshipPane] = "1"
	livePane.opts[aiPaneManagedOption] = "1"
	livePane.command = "codex"
	if _, err := store.mutator().ObserveWindowRuntimeBinding(&store.registry, window.Metadata.UID, session.id, liveWindow.id); err != nil {
		t.Fatalf("seed exact Window runtime binding: %v", err)
	}
	return livePane, window, pane
}

type promotionPlanStructure struct {
	Allocations []resourceAllocationSlot
	Items       []struct {
		Key, Action, Field, Authority, PromotionKind, AllocationSlot string
		Transitions                                                  []resourceRefTransition
		Guards                                                       []controller.Guard
	}
}

func promotionStructure(report resourceReconcileReport) promotionPlanStructure {
	out := promotionPlanStructure{Allocations: report.Allocations}
	for _, item := range report.Items {
		out.Items = append(out.Items, struct {
			Key, Action, Field, Authority, PromotionKind, AllocationSlot string
			Transitions                                                  []resourceRefTransition
			Guards                                                       []controller.Guard
		}{item.Key, item.Action, item.Field, item.Authority, item.PromotionKind, item.AllocationSlot, item.Transitions, item.Guards})
	}
	return out
}

func seedLiveWindowPromotion(t *testing.T, store *fakeResourceStore, session *fakeTmuxSession, liveWindow *fakeTmuxWindow, window coremetadata.Window, pane coremetadata.Pane, provider string) *fakeTmuxPane {
	t.Helper()
	livePane := liveWindow.panes[0]
	liveWindow.name = window.Metadata.Name
	liveWindow.opts[tmuxopts.WindowUID] = window.Metadata.UID
	liveWindow.opts[tmuxopts.WindowName] = window.Metadata.Name
	liveWindow.opts[tmuxopts.AutomaticRenameWindow] = "off"
	livePane.opts[tmuxopts.PaneUID] = pane.Metadata.UID
	livePane.opts[tmuxopts.PaneName] = pane.Metadata.Name
	livePane.opts[tmuxopts.AgentProviderPane] = provider
	livePane.opts[tmuxopts.AgentLaunchAuthorshipPane] = "1"
	livePane.opts[aiPaneManagedOption] = "1"
	if _, err := store.mutator().ObserveWindowRuntimeBinding(&store.registry, window.Metadata.UID, session.id, liveWindow.id); err != nil {
		t.Fatalf("seed exact Window runtime binding: %v", err)
	}
	return livePane
}

func TestAuthorshipPromotionRollbackUsesLockedPreimageAndPreservesConcurrentUnrelatedState(t *testing.T) {
	t.Parallel()
	command, store, server, _, root := newReconcileFixture(t, "-L", "primary")
	livePane, _, _ := seedAuthorshipPromotionIncident(t, store, server, root)
	registryBefore := store.registry.Clone()
	base := command.resources
	commitCalls := 0
	command.resources = &resourceStore{
		load: base.load, snapshot: base.snapshot, mutator: base.mutator,
		updateConvergent: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
			commitCalls++
			registry, changed, err := base.updateConvergent(fn)
			if commitCalls == 1 && err == nil {
				project := &store.registry.Projects[0]
				if project.Metadata.Annotations == nil {
					project.Metadata.Annotations = map[string]string{}
				}
				project.Metadata.Annotations["concurrent-review"] = "preserved"
				livePane.opts[tmuxopts.AgentLaunchAuthorshipPane] = "0"
			}
			return registry, changed, err
		},
	}
	if _, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err == nil {
		t.Fatal("semantic guard failure was not surfaced")
	}
	project := store.registry.Projects[0]
	if project.Metadata.Annotations["concurrent-review"] != "preserved" {
		t.Fatalf("concurrent unrelated Project state was lost: %+v", project.Metadata.Annotations)
	}
	afterWithoutConcurrent := store.registry.Clone()
	afterWithoutConcurrent.Projects[0].Metadata.Annotations = registryBefore.Projects[0].Metadata.Annotations
	livePane.opts[tmuxopts.AgentLaunchAuthorshipPane] = "1"
	if !reflect.DeepEqual(afterWithoutConcurrent.Normalize(), registryBefore.Normalize()) {
		t.Fatalf("exact promotion scope did not return to locked preimage:\nbefore=%+v\nafter=%+v", registryBefore, store.registry)
	}
}

func TestAuthorshipPromotionPreservesSameProjectSiblingWindowGraph(t *testing.T) {
	t.Parallel()
	command, store, server, _, root := newReconcileFixture(t, "-L", "primary")
	_, _, _ = seedAuthorshipPromotionIncident(t, store, server, root)
	project, _ := store.registry.ProjectByRoot(root)
	sibling, _, err := store.mutator().AddWindow(&store.registry, project.Metadata.UID,
		coremetadata.BootstrapWindow{Name: "same-project-sibling", Panes: []coremetadata.BootstrapPane{{CWD: root}}}, "/bin/zsh", "op-same-project-sibling")
	if err != nil {
		t.Fatal(err)
	}
	siblingBefore := mergeExactWindowGraphs(coremetadata.NewRegistry(), store.registry, map[string]bool{sibling.Metadata.UID: true})
	if stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("promotion: %v\n%s", err, stdout)
	}
	siblingAfter := mergeExactWindowGraphs(coremetadata.NewRegistry(), store.registry, map[string]bool{sibling.Metadata.UID: true})
	if !reflect.DeepEqual(siblingBefore, siblingAfter) {
		t.Fatalf("same-Project sibling Window ancestry changed:\nbefore=%+v\nafter=%+v", siblingBefore, siblingAfter)
	}
}

func TestPublicAuthorshipPromotionMultiAllocationOrderParity(t *testing.T) {
	t.Parallel()
	command, store, server, _, root := newReconcileFixture(t, "-L", "primary")
	_, firstWindow, _ := seedAuthorshipPromotionIncident(t, store, server, root)
	project, _ := store.registry.ProjectByRoot(root)
	secondWindow, secondPanes, err := store.mutator().AddWindow(&store.registry, project.Metadata.UID,
		coremetadata.BootstrapWindow{Name: "second", Panes: []coremetadata.BootstrapPane{{CWD: root}}}, "/bin/zsh", "op-second-promotion")
	if err != nil {
		t.Fatal(err)
	}
	session := server.session("alpha")
	liveSecond := &fakeTmuxWindow{id: server.mint("@"), name: "second", opts: map[string]string{}, panes: []*fakeTmuxPane{newFakeTmuxPane(server.mint("%"))}}
	session.windows = append(session.windows, liveSecond)
	seedLiveWindowPromotion(t, store, session, liveSecond, secondWindow, secondPanes[0], "claude")

	previewJSON, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	var preview resourceReconcileReport
	if err := json.Unmarshal([]byte(previewJSON), &preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.Allocations) != 2 || preview.Allocations[0].Slot != "<allocated-agent-1>" || preview.Allocations[1].Slot != "<allocated-agent-2>" ||
		preview.Allocations[0].PromotionKind != string(coremetadata.AgentLinkMinted) || preview.Allocations[1].PromotionKind != string(coremetadata.AgentLinkMinted) {
		t.Fatalf("multi allocation invocation order = %+v", preview.Allocations)
	}
	executeJSON, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("multi execute: %v\n%s", err, executeJSON)
	}
	var execute resourceReconcileReport
	if err := json.Unmarshal([]byte(executeJSON), &execute); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(promotionStructure(preview), promotionStructure(execute)) {
		t.Fatalf("multi promotion structure drifted:\npreview=%+v\nexecute=%+v", promotionStructure(preview), promotionStructure(execute))
	}
	if len(store.registry.AgentsOf(firstWindow.Metadata.UID)) != 2 || len(store.registry.AgentsOf(secondWindow.Metadata.UID)) != 1 {
		t.Fatalf("multi promotion Agent ownership drifted: first=%+v second=%+v", store.registry.AgentsOf(firstWindow.Metadata.UID), store.registry.AgentsOf(secondWindow.Metadata.UID))
	}
}

func TestPublicAuthorshipPromotionAttachesExistingAgentWithoutAllocation(t *testing.T) {
	t.Parallel()
	command, store, server, _, root := newReconcileFixture(t, "-L", "primary")
	project, _ := store.registry.ProjectByRoot(root)
	window := store.registry.WindowsOf(project.Metadata.UID)[0]
	pane := store.registry.PanesOf(window.Metadata.UID)[0]
	agent, err := store.mutator().CreateAgent(&store.registry, window.Metadata.UID, coremetadata.CreateAgentOptions{Provider: "codex", OperationID: "op-attach-existing"})
	if err != nil {
		t.Fatal(err)
	}
	storedAgent, _ := store.registry.Agent(agent.Metadata.UID)
	storedAgent.Status.SessionRef = codexConversationRef("attach-thread")
	livePane := seedLiveWindowPromotion(t, store, server.session("alpha"), server.session("alpha").windows[0], window, pane, "codex")
	livePane.opts[tmuxopts.AgentThreadIDPane] = "attach-thread"
	allocationsBefore := len(store.newUIDs)
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("attach promotion: %v\n%s", err, stdout)
	}
	var report resourceReconcileReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Allocations) != 0 || len(store.newUIDs) != allocationsBefore {
		t.Fatalf("attach allocated Agent: report=%+v allocations=%d->%d", report.Allocations, allocationsBefore, len(store.newUIDs))
	}
	found := false
	for _, item := range report.Items {
		if item.Action == "promote-authorship" {
			found = item.PromotionKind == string(coremetadata.AgentLinkAttached) && item.AllocationSlot == ""
		}
	}
	if !found {
		t.Fatalf("attach promotion was not distinguished in report: %+v", report.Items)
	}
	stored, _ := store.registry.Agent(agent.Metadata.UID)
	if stored.Status.PaneRef != pane.Metadata.UID {
		t.Fatalf("existing Agent paneRef = %q", stored.Status.PaneRef)
	}
}

func TestPublicAuthorshipPromotionPreservesUnknownRawProviderReceipt(t *testing.T) {
	t.Parallel()
	command, store, server, _, root := newReconcileFixture(t, "-L", "primary")
	project, _ := store.registry.ProjectByRoot(root)
	window := store.registry.WindowsOf(project.Metadata.UID)[0]
	pane := store.registry.PanesOf(window.Metadata.UID)[0]
	livePane := seedLiveWindowPromotion(t, store, server.session("alpha"), server.session("alpha").windows[0], window, pane, "Vendor-X")
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("unknown provider promotion: %v\n%s", err, stdout)
	}
	agents := store.registry.AgentsOf(window.Metadata.UID)
	if len(agents) != 1 || agents[0].Spec.Provider != "" {
		t.Fatalf("unknown provider Agent spec = %+v, want canonical empty provider", agents)
	}
	if livePane.opts[tmuxopts.AgentProviderPane] != "Vendor-X" || !strings.Contains(stdout, `"expect": "Vendor-X"`) {
		t.Fatalf("raw provider receipt was not preserved: opts=%v\n%s", livePane.opts, stdout)
	}
}

func TestAuthorshipPromotionPreservesSiblingProjectSocketAndOtherHostDesiredState(t *testing.T) {
	t.Parallel()
	command, store, primary, routed, root := newReconcileFixture(t, "-L", "primary")
	_, _, _ = seedAuthorshipPromotionIncident(t, store, primary, root)

	siblingRoot := t.TempDir()
	store.dirs[siblingRoot] = true
	siblingProject, err := store.mutator().RegisterProject(&store.registry, coremetadata.RegisterProjectOptions{
		Root: siblingRoot, DefaultShell: "/bin/zsh", OperationID: "op-sibling-project",
	})
	if err != nil {
		t.Fatalf("seed sibling Project: %v", err)
	}
	if _, err := store.mutator().BindProjectSession(&store.registry, siblingProject.Project.Metadata.UID, "donus", true); err != nil {
		t.Fatalf("bind donus D5 session: %v", err)
	}
	siblingProjectRow, _ := store.registry.Project(siblingProject.Project.Metadata.UID)
	siblingWindow := store.registry.WindowsOf(siblingProjectRow.Metadata.UID)[0]
	siblingPane := store.registry.PanesOf(siblingWindow.Metadata.UID)[0]
	donusSession := primary.addSession("donus")
	donusSession.opts[inttmux.ProjectPathSessionOption] = siblingRoot
	donusSession.opts[tmuxopts.ProjectUIDSession] = siblingProjectRow.Metadata.UID
	donusSession.opts[tmuxopts.ProjectNameSession] = siblingProjectRow.Metadata.Name
	donusSession.windows[0].opts[tmuxopts.WindowUID] = siblingWindow.Metadata.UID
	donusSession.windows[0].opts[tmuxopts.WindowName] = siblingWindow.Metadata.Name
	donusSession.windows[0].opts[tmuxopts.AutomaticRenameWindow] = "off"
	if storedSiblingWindow, ok := store.registry.Window(siblingWindow.Metadata.UID); ok {
		storedSiblingWindow.Metadata.DisplayName = donusSession.windows[0].name
	}
	donusSession.windows[0].panes[0].opts[tmuxopts.PaneUID] = siblingPane.Metadata.UID
	donusSession.windows[0].panes[0].opts[tmuxopts.PaneName] = siblingPane.Metadata.Name
	if _, err := store.mutator().ObserveWindowRuntimeBinding(&store.registry, siblingWindow.Metadata.UID,
		donusSession.id, donusSession.windows[0].id); err != nil {
		t.Fatalf("seed sibling Window runtime binding: %v", err)
	}
	siblingProjectBefore := resourceRegistryProjectGraph(store.registry, map[string]bool{siblingProject.Project.Metadata.UID: true})
	donusD5State := func() string {
		return fmt.Sprintf("%s|%s|%s|%v|%v|%v", donusSession.id, donusSession.windows[0].id,
			donusSession.windows[0].panes[0].id, donusSession.opts, donusSession.windows[0].opts,
			donusSession.windows[0].panes[0].opts)
	}
	donusD5Before := donusD5State()

	siblingSocket := newFakeTmux()
	siblingSocket.socketPath = filepath.Join("/tmp/fake-tmux", "sibling")
	siblingSession := siblingSocket.addSession("donus")
	siblingSession.opts[tmuxopts.ProjectUIDSession] = "donus-d5"
	siblingSession.windows[0].opts[tmuxopts.WindowUID] = "donus-window"
	siblingSession.windows[0].panes[0].opts[tmuxopts.PaneUID] = "donus-pane"
	siblingSession.windows[0].panes[0].opts[tmuxopts.PaneName] = "drifted-d5"

	// The same shared Registry also carries a Project whose only positive live
	// evidence belongs to the sibling socket/host. Its desired anchor/default
	// refs and runtime status are global bytes, but absence from the selected
	// primary socket is explicitly not authority to clear or re-anchor them.
	otherHostRoot := t.TempDir()
	store.dirs[otherHostRoot] = true
	otherHostProject, err := store.mutator().RegisterProject(&store.registry, coremetadata.RegisterProjectOptions{
		Root: otherHostRoot, DefaultShell: "/bin/zsh", OperationID: "op-other-host-project",
	})
	if err != nil {
		t.Fatalf("seed other-host Project: %v", err)
	}
	if _, err := store.mutator().BindProjectSession(&store.registry, otherHostProject.Project.Metadata.UID, "other-host", true); err != nil {
		t.Fatalf("bind other-host Project: %v", err)
	}
	otherHostWindow := store.registry.WindowsOf(otherHostProject.Project.Metadata.UID)[0]
	otherHostPane := store.registry.PanesOf(otherHostWindow.Metadata.UID)[0]
	otherHostSession := siblingSocket.addSession("other-host")
	otherHostSession.opts[tmuxopts.ProjectUIDSession] = otherHostProject.Project.Metadata.UID
	otherHostSession.windows[0].opts[tmuxopts.WindowUID] = otherHostWindow.Metadata.UID
	otherHostSession.windows[0].panes[0].opts[tmuxopts.PaneUID] = otherHostPane.Metadata.UID
	if _, err := store.mutator().ObserveWindowRuntimeBinding(&store.registry, otherHostWindow.Metadata.UID,
		otherHostSession.id, otherHostSession.windows[0].id); err != nil {
		t.Fatalf("observe other-host Window: %v", err)
	}
	if _, err := store.mutator().RecordPaneActivation(&store.registry, otherHostPane.Metadata.UID, coremetadata.PaneActivationOptions{
		Generation: "gen-other-host", RuntimeID: otherHostSession.windows[0].panes[0].id, OperationID: "op-other-host",
	}); err != nil {
		t.Fatalf("record other-host Pane activation: %v", err)
	}
	otherHostBefore := resourceRegistryProjectGraph(store.registry, map[string]bool{otherHostProject.Project.Metadata.UID: true})
	routed.servers["-L\x00sibling"] = siblingSocket
	siblingSocketBefore := siblingSocket.state()

	if stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("promotion with sibling Project/socket: %v\n%s", err, stdout)
	}
	if got := resourceRegistryProjectGraph(store.registry, map[string]bool{siblingProject.Project.Metadata.UID: true}); !reflect.DeepEqual(got, siblingProjectBefore) {
		t.Fatalf("sibling Project bytes changed:\nbefore=%+v\nafter=%+v", siblingProjectBefore, got)
	}
	if got := resourceRegistryProjectGraph(store.registry, map[string]bool{otherHostProject.Project.Metadata.UID: true}); !reflect.DeepEqual(got, otherHostBefore) {
		t.Fatalf("other-host-only desired refs/status changed:\nbefore=%+v\nafter=%+v", otherHostBefore, got)
	}
	if got := donusD5State(); got != donusD5Before {
		t.Fatalf("donus D5 handles/options changed:\nbefore=%s\nafter=%s", donusD5Before, got)
	}
	if siblingSocket.state() != siblingSocketBefore || tmuxMutationCallCount(siblingSocket) != 0 {
		t.Fatalf("sibling socket handles/options changed:\nbefore=%s\nafter=%s", siblingSocketBefore, siblingSocket.state())
	}
	writesAfter, allocationsAfter, mutationsAfter := store.writes, len(store.newUIDs), tmuxMutationCallCount(primary)
	registryAfterFirst := store.snapshot()
	repeat, _, repeatErr := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if repeatErr != nil || !strings.Contains(repeat, `"outcome": "no-op"`) || store.writes != writesAfter ||
		len(store.newUIDs) != allocationsAfter || tmuxMutationCallCount(primary) != mutationsAfter {
		t.Fatalf("successful incident repeat was not zero-write: err=%v writes=%d->%d allocations=%d->%d mutations=%d->%d registryChanged=%t\n%s\nbefore=%s\nafter=%s",
			repeatErr, writesAfter, store.writes, allocationsAfter, len(store.newUIDs), mutationsAfter, tmuxMutationCallCount(primary),
			store.snapshot() != registryAfterFirst, repeat, registryAfterFirst, store.snapshot())
	}
	if got := donusD5State(); got != donusD5Before || siblingSocket.state() != siblingSocketBefore ||
		!reflect.DeepEqual(resourceRegistryProjectGraph(store.registry, map[string]bool{otherHostProject.Project.Metadata.UID: true}), otherHostBefore) {
		t.Fatalf("repeat absorbed sibling D5/socket/other-host state: donus=%s want=%s siblingChanged=%t", got, donusD5Before, siblingSocket.state() != siblingSocketBefore)
	}
}

func TestAuthorshipPromotionExactOptionAndContainmentGuardsRefuseBeforeFirstWrite(t *testing.T) {
	t.Parallel()
	command, store, server, _, root := newReconcileFixture(t, "-L", "primary")
	livePane, _, _ := seedAuthorshipPromotionIncident(t, store, server, root)
	registryBefore := store.snapshot()
	runtimeBefore := server.state()
	mutationsBefore := tmuxMutationCallCount(server)
	base := command.resources
	commitCalls := 0
	command.resources = &resourceStore{
		load: base.load, snapshot: base.snapshot, mutator: base.mutator,
		updateConvergent: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
			commitCalls++
			registry, changed, err := base.updateConvergent(fn)
			if commitCalls == 1 && err == nil {
				livePane.opts[tmuxopts.AgentLaunchAuthorshipPane] = "0"
			}
			return registry, changed, err
		},
	}
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), tmuxopts.AgentLaunchAuthorshipPane) || !strings.Contains(stdout, `"outcome": "failed"`) {
		t.Fatalf("semantic launch guard did not refuse: err=%v\n%s", err, stdout)
	}
	if store.snapshot() != registryBefore || tmuxMutationCallCount(server) != mutationsBefore {
		t.Fatalf("guard refusal changed Registry or executed a tmux write: registryChanged=%t mutations=%d->%d",
			store.snapshot() != registryBefore, mutationsBefore, tmuxMutationCallCount(server))
	}
	livePane.opts[tmuxopts.AgentLaunchAuthorshipPane] = "1"
	if server.state() != runtimeBefore {
		t.Fatalf("guard refusal left mixed runtime options:\nbefore=%s\nafter=%s", runtimeBefore, server.state())
	}
}

func TestPublicResourceReconcilePromotesCanonicalLaunchAuthorshipAtomicallyAndRepeatsEmpty(t *testing.T) {
	t.Parallel()
	command, store, server, _, root := newReconcileFixture(t, "-L", "primary")
	livePane, windowBefore, paneBefore := seedAuthorshipPromotionIncident(t, store, server, root)
	registryBefore, runtimeBefore := store.snapshot(), server.state()
	writesBefore, allocationsBefore := store.writes, len(store.newUIDs)
	mutationsBefore := tmuxMutationCallCount(server)

	previewJSON, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("promotion dry-run: %v\n%s", err, previewJSON)
	}
	var preview resourceReconcileReport
	if err := json.Unmarshal([]byte(previewJSON), &preview); err != nil {
		t.Fatalf("decode promotion preview: %v\n%s", err, previewJSON)
	}
	if len(preview.Allocations) != 1 || preview.Allocations[0].Slot != "<allocated-agent-1>" || preview.Allocations[0].Name != "codex-1" {
		t.Fatalf("promotion allocation slots = %+v, want one symbolic codex-1 Agent", preview.Allocations)
	}
	if store.snapshot() != registryBefore || server.state() != runtimeBefore || store.writes != writesBefore ||
		len(store.newUIDs) != allocationsBefore || tmuxMutationCallCount(server) != mutationsBefore {
		t.Fatal("promotion dry-run allocated a UID or wrote Registry/tmux state")
	}
	human, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary")
	if err != nil {
		t.Fatalf("print promotion dry-run: %v\n%s", err, human)
	}
	for _, want := range []string{
		"allocation slots:", "<allocated-agent-1> Agent codex-1",
		"authority=launch-authorship allocation-slot=<allocated-agent-1>",
		"ref Pane.metadata.ownerRef:", "ref Window.spec.defaultShellPaneRef:",
		"guard " + tmuxopts.PaneUID + "=" + paneBefore.Metadata.UID,
		"guard session_id=" + server.session("alpha").id,
		"guard window_id=" + server.session("alpha").windows[0].id,
	} {
		if !strings.Contains(human, want) {
			t.Fatalf("printable promotion table missing %q:\n%s", want, human)
		}
	}

	executeJSON, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("promotion execute: %v\n%s", err, executeJSON)
	}
	var executed resourceReconcileReport
	if err := json.Unmarshal([]byte(executeJSON), &executed); err != nil {
		t.Fatalf("decode promotion execute: %v\n%s", err, executeJSON)
	}
	if executed.Counts.Changed == 0 || !reflect.DeepEqual(promotionStructure(preview), promotionStructure(executed)) {
		t.Fatalf("dry-run/execute promotion structure drifted:\npreview=%+v\nexecute=%+v", promotionStructure(preview), promotionStructure(executed))
	}
	agents := store.registry.AgentsOf(windowBefore.Metadata.UID)
	if len(agents) != 2 || agents[1].Metadata.Name != "codex-1" {
		t.Fatalf("promoted Agents = %+v, want existing codex plus codex-1", agents)
	}
	paneAfter, _ := store.registry.Pane(paneBefore.Metadata.UID)
	windowAfter, _ := store.registry.Window(windowBefore.Metadata.UID)
	if paneAfter.Metadata.OwnerRef == nil || paneAfter.Metadata.OwnerRef.Kind != coremetadata.KindAgent || paneAfter.Spec.Role != coremetadata.PaneRoleAgent ||
		agents[1].Status.PaneRef != paneBefore.Metadata.UID || windowAfter.Spec.AnchorPaneRef != paneBefore.Metadata.UID || windowAfter.Spec.DefaultShellPaneRef != "" {
		t.Fatalf("invalid promotion post-state: pane=%+v agent=%+v window=%+v", paneAfter, agents[1], windowAfter.Spec)
	}
	for field, want := range map[string]string{
		tmuxopts.PaneOwnerKind: string(coremetadata.KindAgent), tmuxopts.PaneOwnerUID: agents[1].Metadata.UID,
		tmuxopts.PaneRole: string(coremetadata.PaneRoleAgent), tmuxopts.AgentUIDPane: agents[1].Metadata.UID,
		tmuxopts.AgentProviderPane: "codex",
	} {
		if got := livePane.opts[field]; got != want {
			t.Fatalf("promoted runtime option %s = %q, want %q", field, got, want)
		}
	}
	wantWriteOrder := []string{tmuxopts.AgentUIDPane, tmuxopts.PaneOwnerKind, tmuxopts.PaneOwnerUID, tmuxopts.PaneRole}
	var writeOrder []string
	for _, call := range server.calls {
		if !slices.Contains(call, "set-option") {
			continue
		}
		for _, field := range wantWriteOrder {
			if slices.Contains(call, field) {
				writeOrder = append(writeOrder, field)
			}
		}
	}
	if !slices.Equal(writeOrder, wantWriteOrder) {
		t.Fatalf("promotion option action order = %v, want %v; calls=%#v", writeOrder, wantWriteOrder, server.calls)
	}

	writesAfter, allocationsAfter, mutationsAfter := store.writes, len(store.newUIDs), tmuxMutationCallCount(server)
	repeatJSON, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil || !strings.Contains(repeatJSON, `"outcome": "no-op"`) || store.writes != writesAfter ||
		len(store.newUIDs) != allocationsAfter || tmuxMutationCallCount(server) != mutationsAfter {
		t.Fatalf("promotion repeat was not zero-write: err=%v writes=%d->%d allocations=%d->%d mutations=%d->%d\n%s",
			err, writesAfter, store.writes, allocationsAfter, len(store.newUIDs), mutationsAfter, tmuxMutationCallCount(server), repeatJSON)
	}
}

func TestPublicResourceReconcilePromotionOptionFailuresRollbackRegistryAndRuntime(t *testing.T) {
	t.Parallel()
	for cut, field := range []string{tmuxopts.AgentUIDPane, tmuxopts.PaneOwnerKind, tmuxopts.PaneOwnerUID, tmuxopts.PaneRole} {
		for _, afterEffect := range []bool{false, true} {
			mode := "before-effect"
			if afterEffect {
				mode = "after-effect"
			}
			t.Run(fmt.Sprintf("cut-%d-%s-%s", cut+1, field, mode), func(t *testing.T) {
				t.Parallel()
				command, store, server, _, root := newReconcileFixture(t, "-L", "primary")
				_, _, _ = seedAuthorshipPromotionIncident(t, store, server, root)
				registryBefore, runtimeBefore := store.snapshot(), server.state()
				server.fail = []string{"set-option", field}
				server.failAfterMutation = afterEffect
				stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
				if err == nil || !strings.Contains(stdout, `"outcome": "failed"`) {
					t.Fatalf("injected %s %s failure was not reported: err=%v\n%s", field, mode, err, stdout)
				}
				if store.snapshot() != registryBefore || server.state() != runtimeBefore {
					t.Fatalf("injected %s %s failure left mixed Registry/runtime state\nregistry before=%s\nafter=%s\nruntime before=%s\nafter=%s",
						field, mode, registryBefore, store.snapshot(), runtimeBefore, server.state())
				}
			})
		}
	}
}

func TestPublicAuthorshipPromotionPrecommitAndGuardFaultMatrixLeavesNoMixedState(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		inject func(*resourceReconcileCommand, *fakeResourceStore, *fakeTmux, *fakeTmuxPane)
	}{
		{
			name: "uid-allocation",
			inject: func(command *resourceReconcileCommand, _ *fakeResourceStore, _ *fakeTmux, _ *fakeTmuxPane) {
				base := command.resources
				command.resources = &resourceStore{load: base.load, snapshot: base.snapshot, updateConvergent: base.updateConvergent,
					mutator: func() coremetadata.Mutator {
						mutator := base.mutator()
						mutator.NewUID = func(kind coremetadata.Kind) (string, error) {
							if kind == coremetadata.KindAgent {
								return "", errors.New("injected Agent UID allocation failure")
							}
							return coremetadata.NewUID(kind)
						}
						return mutator
					},
				}
			},
		},
		{
			name: "registry-commit",
			inject: func(command *resourceReconcileCommand, _ *fakeResourceStore, _ *fakeTmux, _ *fakeTmuxPane) {
				base := command.resources
				command.resources = &resourceStore{load: base.load, snapshot: base.snapshot, mutator: base.mutator,
					updateConvergent: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
						working, err := base.load()
						if err == nil {
							err = fn(&working)
						}
						if err != nil {
							return coremetadata.Registry{}, false, err
						}
						return coremetadata.Registry{}, false, errors.New("injected promotion Registry commit failure")
					},
				}
			},
		},
		{
			name: "pane-containment-guard",
			inject: func(command *resourceReconcileCommand, _ *fakeResourceStore, _ *fakeTmux, livePane *fakeTmuxPane) {
				base := command.resources
				calls := 0
				command.resources = &resourceStore{load: base.load, snapshot: base.snapshot, mutator: base.mutator,
					updateConvergent: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
						calls++
						registry, changed, err := base.updateConvergent(fn)
						if calls == 1 && err == nil {
							livePane.opts[tmuxopts.PaneUID] = "pane-recycled"
						}
						return registry, changed, err
					},
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			command, store, server, _, root := newReconcileFixture(t, "-L", "primary")
			livePane, _, _ := seedAuthorshipPromotionIncident(t, store, server, root)
			registryBefore, runtimeBefore := store.registry.Clone(), server.state()
			originalPaneUID := livePane.opts[tmuxopts.PaneUID]
			testCase.inject(command, store, server, livePane)
			stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
			if err == nil || !strings.Contains(stdout, `"outcome": "failed"`) {
				t.Fatalf("injected cut was not reported: err=%v\n%s", err, stdout)
			}
			livePane.opts[tmuxopts.PaneUID] = originalPaneUID
			if !reflect.DeepEqual(store.registry.Normalize(), registryBefore.Normalize()) || server.state() != runtimeBefore {
				t.Fatalf("injected cut left mixed state:\nregistry before=%+v\nafter=%+v\nruntime before=%s\nafter=%s", registryBefore, store.registry, runtimeBefore, server.state())
			}
		})
	}
}

func TestPublicResourceReconcileRecordsAmbiguousLaunchRefusalWithZeroWrites(t *testing.T) {
	t.Parallel()

	command, store, server, _, root := newReconcileFixture(t, "-L", "primary")
	project, _ := store.registry.ProjectByRoot(root)
	window := store.registry.WindowsOf(project.Metadata.UID)[0]
	pane := store.registry.PanesOf(window.Metadata.UID)[0]
	session := server.session("alpha")
	liveWindow, livePane := session.windows[0], session.windows[0].panes[0]
	session.opts[tmuxopts.ProjectUIDSession] = project.Metadata.UID
	session.opts[tmuxopts.ProjectNameSession] = project.Metadata.Name
	liveWindow.opts[tmuxopts.WindowUID] = window.Metadata.UID
	liveWindow.opts[tmuxopts.WindowName] = window.Metadata.Name
	liveWindow.opts[tmuxopts.AutomaticRenameWindow] = "off"
	livePane.opts[tmuxopts.PaneUID] = pane.Metadata.UID
	livePane.opts[tmuxopts.PaneName] = pane.Metadata.Name
	livePane.opts[tmuxopts.AgentProviderPane] = "codex"
	livePane.opts[tmuxopts.AgentLaunchAuthorshipPane] = "yes"
	if _, err := store.mutator().ObserveWindowRuntimeBinding(&store.registry, window.Metadata.UID, session.id, liveWindow.id); err != nil {
		t.Fatalf("seed exact ambiguous Window binding: %v", err)
	}
	registryBefore, runtimeBefore := store.snapshot(), server.state()
	writesBefore, allocationsBefore := store.writes, len(store.newUIDs)

	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil || !strings.Contains(stdout, `"outcome": "refused"`) ||
		!strings.Contains(stdout, "launch authorship marker and provider do not form one canonical receipt") {
		t.Fatalf("ambiguous launch was not a recorded refusal: err=%v\n%s", err, stdout)
	}
	if store.snapshot() != registryBefore || server.state() != runtimeBefore || store.writes != writesBefore ||
		len(store.newUIDs) != allocationsBefore {
		t.Fatal("ambiguous launch refusal wrote Registry, allocated Agent UID, or changed tmux")
	}
}

func TestPublicLaunchProviderConflictFreezesExactTargetAndConvergesUnrelatedTarget(t *testing.T) {
	t.Parallel()
	command, store, server, _, root := newReconcileFixture(t, "-L", "primary")
	livePane, window, _ := seedAuthorshipPromotionIncident(t, store, server, root)
	if stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("seed promotion: %v\n%s", err, stdout)
	}
	siblingPane, err := store.mutator().AddPane(&store.registry, window.Metadata.UID,
		coremetadata.BootstrapPane{CWD: root, Command: "zsh"}, "/bin/zsh", "op-conflict-sibling-pane")
	if err != nil {
		t.Fatal(err)
	}
	liveWindow := server.session("alpha").windows[0]
	liveSibling := newFakeTmuxPane(server.mint("%"))
	liveSibling.opts[tmuxopts.PaneUID] = siblingPane.Metadata.UID
	liveSibling.opts[tmuxopts.PaneName] = "stale-unrelated"
	liveWindow.panes = append(liveWindow.panes, liveSibling)

	livePane.opts[tmuxopts.AgentProviderPane] = "claude"
	livePane.opts[tmuxopts.PaneName] = "protected-topology-drift"
	livePane.opts[aiPaneStateOption] = "thinking"
	protectedBefore := fmt.Sprintf("%v", livePane.opts)
	callsBefore := len(server.calls)
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil || !strings.Contains(stdout, "launch provider conflicts with the exact Agent owner provider") {
		t.Fatalf("launch/provider conflict was not refused: err=%v\n%s", err, stdout)
	}
	if fmt.Sprintf("%v", livePane.opts) != protectedBefore {
		t.Fatalf("conflict target received topology/presentation writes:\nbefore=%s\nafter=%v", protectedBefore, livePane.opts)
	}
	for _, call := range server.calls[callsBefore:] {
		if slices.Contains(call, "set-option") && slices.Contains(call, livePane.id) {
			t.Fatalf("conflict target received mutation argv: %v", call)
		}
	}
	if got := liveSibling.opts[tmuxopts.PaneName]; got != siblingPane.Metadata.Name {
		t.Fatalf("unrelated #759 Pane convergence was suppressed: got=%q want=%q", got, siblingPane.Metadata.Name)
	}
}

func TestResourceReconcileProjectsAllAgentFieldsFromRegistryAuthority(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	registry := resourceFixtureRegistry(t)
	agent, _ := registry.Agent("agt-alpha-codex")
	if agent.Metadata.Annotations == nil {
		agent.Metadata.Annotations = map[string]string{}
	}
	agent.Metadata.Annotations[coremetadata.AnnotationAgentTopic] = "registry topic"
	agent.Status.Interaction = coremetadata.AgentInteraction{
		Kind: coremetadata.InteractionResponseComplete, ObservedAt: now.Add(-time.Minute), Source: "provider-hook",
	}
	server := newFakeTmux()
	pane := server.addSession("alpha").windows[0].panes[0]
	pane.opts[tmuxopts.PaneUID] = "pan-alpha-codex"
	pane.opts[aiPaneTopicOption] = "foreign live topic"
	pane.opts[aiPaneStateOption] = "thinking"
	pane.opts[aiPaneBadgeKindOption] = aiBadgeKindInProgress
	pane.opts[attentionStateOption] = attentionStateBusy

	plan := func(reg coremetadata.Registry) *resourcePlanTmuxRunner {
		recorder := newResourcePlanTmuxRunner(server)
		if err := planResourceAgentProjections(context.Background(), recorder, reg, now); err != nil {
			t.Fatalf("plan Agent projection: %v", err)
		}
		return recorder
	}
	recorder := plan(registry)
	want := map[string]string{
		aiPaneTopicOption:       "registry topic",
		aiPaneTopicManualOption: "on",
		aiPaneStateOption:       "waiting",
		aiPaneBadgeKindOption:   aiBadgeKindResponseComplete,
		attentionStateOption:    attentionStateReply,
	}
	if len(recorder.writes) != len(want) {
		t.Fatalf("writes = %#v, want one for every Agent projection", recorder.writes)
	}
	for _, write := range recorder.writes {
		if want[write.field] != write.after {
			t.Fatalf("write %s = %q, want %q", write.field, write.after, want[write.field])
		}
		if class, ok := controllerRuntimeMutationFieldClassFor("pane", write.field); !ok || class != controllerRuntimeMutationPresentation {
			t.Fatalf("Agent projection field %s is not bound to the typed presentation exemption: class=%q ok=%v", write.field, class, ok)
		}
		if _, err := server.Run(context.Background(), "tmux", write.args...); err != nil {
			t.Fatalf("execute %v: %v", write.args, err)
		}
	}
	if repeat := plan(registry); len(repeat.writes) != 0 {
		t.Fatalf("repeat projection imported or rewrote live values: %#v", repeat.writes)
	}

	// An Offline Agent may retain durable history, but a surviving stale Pane
	// projects current unknown and cannot keep the completed badge live.
	agent.Status.Phase = coremetadata.PhaseOffline
	agent.Status.PaneRef = ""
	recorder = plan(registry)
	for _, write := range recorder.writes {
		if _, err := server.Run(context.Background(), "tmux", write.args...); err != nil {
			t.Fatalf("execute offline clear %v: %v", write.args, err)
		}
	}
	if pane.opts[aiPaneStateOption] != "" {
		t.Fatalf("offline state = %q", pane.opts[aiPaneStateOption])
	}
	for _, field := range []string{aiPaneTopicOption, aiPaneTopicManualOption, aiPaneBadgeKindOption, attentionStateOption} {
		if got := pane.opts[field]; got != "" {
			t.Fatalf("offline field %s retained %q", field, got)
		}
	}
	if agent.Status.Interaction.Kind != coremetadata.InteractionResponseComplete {
		t.Fatalf("reconcile imported/erased durable history: %+v", agent.Status.Interaction)
	}
}

func TestGenerationLifecycleProjectionReconcileWritesOnceThenZero(t *testing.T) {
	for _, test := range []struct {
		state     codexgeneration.GenerationState
		wantState string
		wantBadge string
	}{
		{codexgeneration.StateDraining, codexgeneration.LifecycleStateDraining, codexgeneration.LifecycleBadgeDraining},
		{codexgeneration.StateHandoverPending, codexgeneration.LifecycleStateDraining, codexgeneration.LifecycleBadgeHandoverPending},
		{codexgeneration.StateRecovering, codexgeneration.LifecycleStateRecovering, codexgeneration.LifecycleBadgeRecovering},
		{codexgeneration.StateBlocked, codexgeneration.LifecycleStateBlocked, codexgeneration.LifecycleBadgeBlocked},
	} {
		t.Run(string(test.state), func(t *testing.T) {
			registry := resourceFixtureRegistry(t)
			server := newFakeTmux()
			pane := server.addSession("phase1-" + string(test.state)).windows[0].panes[0]
			pane.opts[tmuxopts.PaneUID] = "pan-alpha-codex"
			pane.opts[aiPaneStateOption] = codexgeneration.LifecycleStateThinking
			pane.opts[aiPaneBadgeKindOption] = codexgeneration.LifecycleBadgeInProgress
			pane.opts[attentionStateOption] = codexgeneration.LifecycleAttentionBusy
			endpoint := &coremetadata.CodexEndpointRef{StateDomainID: "reconcile-domain", EndpointGenerationID: "reconcile-generation"}
			operation := &coremetadata.CodexGenerationOperationRef{ID: "reconcile-operation", Endpoint: *endpoint}
			authority := &coremetadata.CodexAuthorityRef{
				StateDomainID: endpoint.StateDomainID, EndpointGenerationID: endpoint.EndpointGenerationID,
				BrokerRuntimeID: "reconcile-broker", ConnectionEpoch: 2, BindingEpoch: 3,
			}
			agent, _ := registry.Agent("agt-alpha-codex")
			agent.Status.Interaction = coremetadata.AgentInteraction{Kind: coremetadata.InteractionIdle, ObservedAt: resourceFixtureClock}
			agent.Status.SessionRef = &coremetadata.AgentSessionRef{
				Provider: aiModeCodex, ObservedAt: resourceFixtureClock,
				Codex: &coremetadata.CodexSessionRef{
					ThreadID: "reconcile-thread", Endpoint: endpoint,
					Lifecycle: &coremetadata.CodexGenerationLifecycleRef{State: test.state, Operation: operation},
				},
			}
			registryPane, _ := registry.Pane("pan-alpha-codex")
			registryPane.Status.Activation = coremetadata.PaneActivation{
				Generation: "materialization-generation", RuntimeID: pane.id, AgentUID: agent.Metadata.UID,
				Codex: &coremetadata.CodexActivationBinding{ThreadID: "reconcile-thread", Authority: authority},
			}
			if err := registry.Validate(); err != nil {
				t.Fatal(err)
			}
			first := newResourcePlanTmuxRunner(server)
			if err := planResourceAgentProjections(context.Background(), first, registry, resourceFixtureClock); err != nil {
				t.Fatal(err)
			}
			if len(first.writes) != 3 {
				t.Fatalf("first full production reconcile writes=%#v, want exact state/badge/attention", first.writes)
			}
			for _, write := range first.writes {
				if _, err := server.Run(context.Background(), "tmux", write.args...); err != nil {
					t.Fatalf("execute %v: %v", write.args, err)
				}
			}
			if pane.opts[aiPaneStateOption] != test.wantState || pane.opts[aiPaneBadgeKindOption] != test.wantBadge || pane.opts[attentionStateOption] != "" {
				t.Fatalf("projected options=%#v", pane.opts)
			}
			second := newResourcePlanTmuxRunner(server)
			if err := planResourceAgentProjections(context.Background(), second, registry, resourceFixtureClock); err != nil {
				t.Fatal(err)
			}
			if len(second.writes) != 0 {
				t.Fatalf("second full production reconcile erased/repeated authoritative tuple: writes=%#v", second.writes)
			}
		})
	}
}

func TestGenerationLifecycleProductionReconcileRejectsForeignOrSiblingAuthorityWithZeroWrites(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*coremetadata.Pane, *coremetadata.CodexAuthorityRef)
	}{
		{
			name: "foreign endpoint generation",
			mutate: func(_ *coremetadata.Pane, authority *coremetadata.CodexAuthorityRef) {
				authority.EndpointGenerationID = "foreign-generation"
			},
		},
		{
			name: "sibling runtime",
			mutate: func(pane *coremetadata.Pane, _ *coremetadata.CodexAuthorityRef) {
				pane.Status.Activation.RuntimeID = "%999"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := resourceFixtureRegistry(t)
			server := newFakeTmux()
			live := server.addSession("phase1-refused").windows[0].panes[0]
			live.opts[tmuxopts.PaneUID] = "pan-alpha-codex"
			endpoint := &coremetadata.CodexEndpointRef{StateDomainID: "domain", EndpointGenerationID: "generation"}
			authority := &coremetadata.CodexAuthorityRef{
				StateDomainID: "domain", EndpointGenerationID: "generation", BrokerRuntimeID: "broker", ConnectionEpoch: 1, BindingEpoch: 1,
			}
			agent, _ := registry.Agent("agt-alpha-codex")
			agent.Status.SessionRef = &coremetadata.AgentSessionRef{Provider: aiModeCodex, Codex: &coremetadata.CodexSessionRef{
				ThreadID: "thread", Endpoint: endpoint,
				Lifecycle: &coremetadata.CodexGenerationLifecycleRef{
					State:     coremetadata.CodexGenerationRecovering,
					Operation: &coremetadata.CodexGenerationOperationRef{ID: "recover", Endpoint: *endpoint},
				},
			}}
			registryPane, _ := registry.Pane("pan-alpha-codex")
			registryPane.Status.Activation = coremetadata.PaneActivation{
				Generation: "materialization", RuntimeID: live.id, AgentUID: agent.Metadata.UID,
				Codex: &coremetadata.CodexActivationBinding{ThreadID: "thread", Authority: authority},
			}
			test.mutate(registryPane, authority)
			if err := registry.Validate(); err != nil {
				t.Fatal(err)
			}
			recorder := newResourcePlanTmuxRunner(server)
			if err := planResourceAgentProjections(context.Background(), recorder, registry, resourceFixtureClock); err != nil {
				t.Fatal(err)
			}
			if len(recorder.writes) != 0 {
				t.Fatalf("refused full production reconcile writes=%#v", recorder.writes)
			}
		})
	}
}

func TestMarkerlessCrashAndVersionDriftRemainOrdinaryFailureThroughProductionReconcile(t *testing.T) {
	registry := resourceFixtureRegistry(t)
	server := newFakeTmux()
	live := server.addSession("phase1-markerless-crash").windows[0].panes[0]
	live.opts[tmuxopts.PaneUID] = "pan-alpha-codex"
	live.opts[aiPaneStateOption] = codexgeneration.LifecycleStateRecovering
	live.opts[aiPaneBadgeKindOption] = codexgeneration.LifecycleBadgeRecovering
	live.opts[attentionStateOption] = codexgeneration.LifecycleAttentionBusy

	agent, _ := registry.Agent("agt-alpha-codex")
	if agent.Metadata.Annotations == nil {
		agent.Metadata.Annotations = map[string]string{}
	}
	agent.Metadata.Annotations["test.projmux.io/codex-version-drift"] = "0.151.0->0.152.1"
	agent.Status.SessionRef = &coremetadata.AgentSessionRef{
		Provider: aiModeCodex, ObservedAt: resourceFixtureClock,
		Codex: &coremetadata.CodexSessionRef{
			ThreadID: "crashed-thread",
			Endpoint: &coremetadata.CodexEndpointRef{StateDomainID: "domain", EndpointGenerationID: "generation"},
			// No Lifecycle marker: endpoint or version evidence alone cannot
			// manufacture a planned maintenance operation.
		},
	}
	registryPane, _ := registry.Pane("pan-alpha-codex")
	registryPane.Status.Activation = coremetadata.PaneActivation{
		Generation: "crashed-materialization", RuntimeID: live.id, AgentUID: agent.Metadata.UID,
		Codex: &coremetadata.CodexActivationBinding{ThreadID: "crashed-thread"},
	}
	exitCode := 23
	mutator := coremetadata.Mutator{Now: func() time.Time { return resourceFixtureClock }}
	if _, err := mutator.RecordTermination(&registry, coremetadata.TerminationEvidence{
		Source: coremetadata.TerminationSourceSupervisor, Classification: coremetadata.TerminationAbnormal,
		ObservedAt: resourceFixtureClock, PaneUID: registryPane.Metadata.UID, AgentUID: agent.Metadata.UID,
		Generation: "crashed-materialization", ExitCode: &exitCode,
	}); err != nil {
		t.Fatal(err)
	}
	if projection, err := mutator.ProjectTermination(&registry, coremetadata.TerminationProjectionInput{
		PaneUID: registryPane.Metadata.UID, Generation: "crashed-materialization", ObservedAt: resourceFixtureClock,
	}); err != nil || projection.Phase != coremetadata.PhaseFailed {
		t.Fatalf("project abnormal crash: projection=%#v err=%v", projection, err)
	}
	failedBefore := registry.Clone()
	recorder := newResourcePlanTmuxRunner(server)
	if err := planResourceAgentProjections(context.Background(), recorder, registry, resourceFixtureClock); err != nil {
		t.Fatal(err)
	}
	for _, write := range recorder.writes {
		if write.after == codexgeneration.LifecycleBadgeDraining || write.after == codexgeneration.LifecycleBadgeHandoverPending ||
			write.after == codexgeneration.LifecycleBadgeRecovering || write.after == codexgeneration.LifecycleBadgeBlocked {
			t.Fatalf("markerless crash planned maintenance write=%#v", write)
		}
		if _, err := server.Run(context.Background(), "tmux", write.args...); err != nil {
			t.Fatal(err)
		}
	}
	failed, _ := registry.Agent(agent.Metadata.UID)
	if failed.Status.Phase != coremetadata.PhaseFailed || failed.Status.LastTermination == nil ||
		failed.Status.LastTermination.Classification != coremetadata.TerminationAbnormal {
		t.Fatalf("ordinary abnormal failure changed: phase=%s receipt=%#v", failed.Status.Phase, failed.Status.LastTermination)
	}
	if failed.Status.SessionRef.Codex.Lifecycle != nil {
		t.Fatalf("markerless crash invented durable lifecycle=%#v", failed.Status.SessionRef.Codex.Lifecycle)
	}
	if !reflect.DeepEqual(registry, failedBefore) {
		t.Fatal("production presentation reconcile mutated the failed Registry")
	}
	maintenanceBadges := 0
	for _, badge := range []string{
		codexgeneration.LifecycleBadgeDraining, codexgeneration.LifecycleBadgeHandoverPending,
		codexgeneration.LifecycleBadgeRecovering, codexgeneration.LifecycleBadgeBlocked,
	} {
		if live.opts[aiPaneBadgeKindOption] == badge {
			maintenanceBadges++
		}
	}
	if maintenanceBadges != 0 || live.opts[aiPaneStateOption] != "" || live.opts[attentionStateOption] != "" {
		t.Fatalf("markerless abnormal failure left maintenance tuple=%#v count=%d", live.opts, maintenanceBadges)
	}
}

func TestPublicResourceReconcileRetriesExactAgentProjection(t *testing.T) {
	t.Parallel()
	command, store, server, _, _ := newReconcileFixture(t, "-L", "primary")
	if _, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("bootstrap reconciliation: %v", err)
	}
	store.now = time.Now().UTC()
	mutator := store.mutator()
	window := store.registry.Windows[0]
	agent, err := mutator.CreateAgent(&store.registry, window.Metadata.UID, coremetadata.CreateAgentOptions{
		Provider: "codex", OperationID: "op-agent-reconcile",
	})
	if err != nil {
		t.Fatal(err)
	}
	managed, err := mutator.AttachAgentPane(&store.registry, agent.Metadata.UID, coremetadata.BootstrapPane{
		CWD: store.registry.Projects[0].Spec.Root,
	}, "op-agent-reconcile")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mutator.SetAgentTopic(&store.registry, agent.Metadata.UID, "public topic"); err != nil {
		t.Fatal(err)
	}
	if _, err := mutator.SetAgentInteraction(&store.registry, agent.Metadata.UID, coremetadata.InteractionApprovalRequired, string(coremetadata.InteractionSourceProviderHook)); err != nil {
		t.Fatal(err)
	}
	liveWindow := server.sessions[0].windows[0]
	livePane := newFakeTmuxPane("%99")
	livePane.opts[tmuxopts.PaneUID] = managed.Metadata.UID
	livePane.opts[aiPaneTopicOption] = "stale"
	livePane.opts[aiPaneStateOption] = "idle"
	liveWindow.panes = append(liveWindow.panes, livePane)

	preview, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("Agent projection preview: %v\n%s", err, preview)
	}
	for _, field := range []string{aiPaneTopicOption, aiPaneTopicManualOption, aiPaneStateOption, aiPaneBadgeKindOption, attentionStateOption} {
		if !strings.Contains(preview, `"field": "`+field+`"`) {
			t.Fatalf("public preview missing %s:\n%s", field, preview)
		}
	}

	server.fail = []string{"set-option", aiPaneStateOption}
	failed, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil || !strings.Contains(failed, `"retry": "projmux reconcile resources --socket 'primary'"`) {
		t.Fatalf("projection failure did not expose public exact retry: err=%v\n%s", err, failed)
	}
	if _, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("Agent projection retry: %v", err)
	}
	if livePane.opts[aiPaneTopicOption] != "public topic" || livePane.opts[aiPaneTopicManualOption] != "on" ||
		livePane.opts[aiPaneStateOption] != "waiting" || livePane.opts[aiPaneBadgeKindOption] != aiBadgeKindApprovalRequired ||
		livePane.opts[attentionStateOption] != attentionStateReply {
		t.Fatalf("Agent projection did not converge: %+v", livePane.opts)
	}
	repeat, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil || !strings.Contains(repeat, `"outcome": "no-op"`) {
		t.Fatalf("Agent projection repeat not no-op: err=%v\n%s", err, repeat)
	}
}

func reconcileFixtureReconciler(root, sessionName string) func(tmuxCommandRunner, sessionLister) *registryReconciler {
	return func(runner tmuxCommandRunner, sessions sessionLister) *registryReconciler {
		mirror := intmetadata.NewMirror(runner)
		return &registryReconciler{
			discoverRoots: func() ([]string, error) { return []string{root}, nil },
			liveSessions:  sessions.ExistingSessions,
			observeLegacy: mirror.ObserveLegacySessionTargets,
			mirror:        mirror,
			mirrorProject: func(context.Context, string, coremetadata.Project) error { return nil },
			mirrorWindow:  mirror.MirrorWindow,
			mirrorPane: func(ctx context.Context, target, _ string, pane coremetadata.Pane) error {
				return mirror.MirrorPane(ctx, target, pane)
			},
			shell: "/bin/zsh",
			sessionNameFor: func(string) string {
				return sessionName
			},
		}
	}
}

func newReconcileFixture(t *testing.T, socketFlag, socketValue string) (*resourceReconcileCommand, *fakeResourceStore, *fakeTmux, *routedTmuxRunner, string) {
	t.Helper()
	root := t.TempDir()
	server := newFakeTmux()
	server.socketPath = filepath.Join("/tmp/fake-tmux", socketValue)
	if socketFlag == "-S" {
		server.socketPath = socketValue
	}
	session := server.addSession("alpha")
	session.opts[inttmux.ProjectPathSessionOption] = root
	store := &fakeResourceStore{registry: coremetadata.NewRegistry(), dirs: map[string]bool{root: true}, now: resourceFixtureClock}
	// Most reconciliation tests exercise repair of Registry-owned identity, not
	// legacy import. Seed that authority explicitly in the fixture; the public
	// command must no longer turn this live D2 topology into Registry rows.
	_, err := store.mutator().ImportLegacySession(&store.registry, coremetadata.LegacySession{
		Session: "alpha",
		Root:    root,
		Windows: []coremetadata.LegacyWindow{{
			Name:  session.windows[0].name,
			Panes: []coremetadata.LegacyPane{{Command: "zsh", CWD: root}},
		}},
	}, "/bin/zsh", "op-fixture-authority", coremetadata.NewBindingMatcher(coremetadata.RuntimeObservation{}))
	if err != nil {
		t.Fatalf("seed authoritative reconcile fixture: %v", err)
	}
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{socketFlag + "\x00" + socketValue: server}}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		newReconciler: reconcileFixtureReconciler(root, "alpha"),
	}
	return command, store, server, runner, root
}

func TestResourceReconcileRefusesCanonicalShellAgentMarkerAndContinuesUnrelatedDrift(t *testing.T) {
	t.Parallel()

	command, store, server, _, root := newReconcileFixture(t, "-L", "primary")
	alphaProject, _ := store.registry.ProjectByRoot(root)
	alphaWindow := store.registry.WindowsOf(alphaProject.Metadata.UID)[0]
	alphaPane := store.registry.PanesOf(alphaWindow.Metadata.UID)[0]
	alphaSession := server.session("alpha")
	alphaLiveWindow, alphaLivePane := alphaSession.windows[0], alphaSession.windows[0].panes[0]
	alphaSession.opts[tmuxopts.ProjectUIDSession] = alphaProject.Metadata.UID
	alphaSession.opts[tmuxopts.ProjectNameSession] = alphaProject.Metadata.Name
	alphaLiveWindow.opts[tmuxopts.WindowUID] = alphaWindow.Metadata.UID
	alphaLiveWindow.opts[tmuxopts.WindowName] = alphaWindow.Metadata.Name
	alphaLiveWindow.opts[tmuxopts.AutomaticRenameWindow] = "off"
	alphaLivePane.opts[tmuxopts.PaneUID] = alphaPane.Metadata.UID
	alphaLivePane.opts[tmuxopts.PaneName] = alphaPane.Metadata.Name
	alphaLivePane.opts[tmuxopts.AgentProviderPane] = "codex"
	alphaLivePane.command = "codex"

	betaRoot := t.TempDir()
	store.dirs[betaRoot] = true
	betaResult, err := store.mutator().RegisterProject(&store.registry, coremetadata.RegisterProjectOptions{
		Root: betaRoot, DefaultShell: "/bin/zsh", OperationID: "op-canonical-shell-beta",
	})
	if err != nil {
		t.Fatalf("register unrelated beta Project: %v", err)
	}
	if _, err := store.mutator().BindProjectSession(&store.registry, betaResult.Project.Metadata.UID, "beta", true); err != nil {
		t.Fatalf("bind unrelated beta session: %v", err)
	}
	betaProject, _ := store.registry.Project(betaResult.Project.Metadata.UID)
	betaWindow := store.registry.WindowsOf(betaProject.Metadata.UID)[0]
	betaPane := store.registry.PanesOf(betaWindow.Metadata.UID)[0]
	betaSession := server.addSession("beta")
	betaSession.opts[inttmux.ProjectPathSessionOption] = betaRoot
	betaSession.opts[tmuxopts.ProjectUIDSession] = betaProject.Metadata.UID
	betaSession.opts[tmuxopts.ProjectNameSession] = betaProject.Metadata.Name
	betaSession.windows[0].opts[tmuxopts.WindowUID] = betaWindow.Metadata.UID
	betaSession.windows[0].opts[tmuxopts.WindowName] = betaWindow.Metadata.Name
	betaSession.windows[0].opts[tmuxopts.AutomaticRenameWindow] = "off"
	for index := range store.registry.Windows {
		if store.registry.Windows[index].Metadata.UID == betaWindow.Metadata.UID {
			store.registry.Windows[index].Metadata.DisplayName = betaSession.windows[0].name
		}
	}
	betaSession.windows[0].panes[0].opts[tmuxopts.PaneUID] = betaPane.Metadata.UID
	betaSession.windows[0].panes[0].opts[tmuxopts.PaneName] = "stale-beta-pane"
	if _, err := store.mutator().ObserveWindowRuntimeBinding(&store.registry, alphaWindow.Metadata.UID, alphaSession.id, alphaLiveWindow.id); err != nil {
		t.Fatalf("seed exact alpha Window runtime binding: %v", err)
	}
	if _, err := store.mutator().ObserveWindowRuntimeBinding(&store.registry, betaWindow.Metadata.UID, betaSession.id, betaSession.windows[0].id); err != nil {
		t.Fatalf("seed exact beta Window runtime binding: %v", err)
	}

	alphaState := func() string {
		return fmt.Sprintf("%s|%s|%s|%v|%v|%v|%s", alphaSession.id, alphaLiveWindow.id, alphaLivePane.id,
			alphaSession.opts, alphaLiveWindow.opts, alphaLivePane.opts, alphaLivePane.command)
	}
	alphaBefore, registryBefore, writesBefore := alphaState(), store.snapshot(), store.writes
	preview, stderr, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary", "-o", "json")
	if err != nil || stderr != "" {
		t.Fatalf("canonical shell preview: err=%v stderr=%q\n%s", err, stderr, preview)
	}
	for _, want := range []string{
		`"key": "tmux:refuse:agent-marker:` + alphaLivePane.id + `"`,
		`"field": "` + tmuxopts.AgentProviderPane + `"`,
		`"before": "codex"`,
		`"divergence": "D2-unattributed"`,
		`"reason": "runtime Agent marker cannot reparent the canonical Window default shell Pane"`,
		`"target": "` + betaSession.windows[0].panes[0].id + `"`,
		`"after": "` + betaPane.Metadata.Name + `"`,
	} {
		if !strings.Contains(preview, want) {
			t.Fatalf("canonical shell preview missing %q:\n%s", want, preview)
		}
	}
	for _, forbidden := range []string{"registry:create:agent", "registry:update:pane"} {
		if strings.Contains(preview, forbidden) {
			t.Fatalf("canonical shell preview planned forbidden %q:\n%s", forbidden, preview)
		}
	}
	if store.snapshot() != registryBefore || store.writes != writesBefore || alphaState() != alphaBefore || tmuxMutationCallCount(server) != 0 {
		t.Fatal("canonical shell dry-run changed Registry or live tmux state")
	}

	executed, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil || !strings.Contains(executed, `"outcome": "refused"`) {
		t.Fatalf("canonical shell execute did not retain typed refusal: err=%v\n%s", err, executed)
	}
	if got := betaSession.windows[0].panes[0].opts[tmuxopts.PaneName]; got != betaPane.Metadata.Name {
		t.Fatalf("unrelated beta Pane name = %q, want %q", got, betaPane.Metadata.Name)
	}
	if store.snapshot() != registryBefore || store.writes != writesBefore || alphaState() != alphaBefore {
		t.Fatalf("canonical shell refusal changed Registry bytes or protected live options/handles: registryChanged=%t writes=%d->%d alphaChanged=%t",
			store.snapshot() != registryBefore, writesBefore, store.writes, alphaState() != alphaBefore)
	}

	mutationsBefore := tmuxMutationCallCount(server)
	firstRepeat, _, firstErr := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	secondRepeat, _, secondErr := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if firstErr == nil || secondErr == nil || firstRepeat != secondRepeat || tmuxMutationCallCount(server) != mutationsBefore ||
		store.snapshot() != registryBefore || store.writes != writesBefore || alphaState() != alphaBefore {
		t.Fatalf("stable refusal repeat drifted: firstErr=%v secondErr=%v mutations=%d->%d\nfirst=%s\nsecond=%s",
			firstErr, secondErr, mutationsBefore, tmuxMutationCallCount(server), firstRepeat, secondRepeat)
	}
}

func runReconcile(t *testing.T, command *resourceReconcileCommand, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := command.Run(args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func tmuxMutationCallCount(server *fakeTmux) int {
	count := 0
	for _, call := range server.calls {
		if len(call) > 0 && slices.Contains([]string{"set-option", "rename-window"}, call[0]) {
			count++
		}
	}
	return count
}

func TestResourceReconcileDryRunIsDeterministicAndByteStable(t *testing.T) {
	t.Parallel()

	command, store, server, _, _ := newReconcileFixture(t, "-L", "primary")
	registryBefore, tmuxBefore := store.snapshot(), server.state()
	first, stderr, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary", "-o", "json")
	if err != nil || stderr != "" {
		t.Fatalf("first dry-run error=%v stderr=%q", err, stderr)
	}
	second, stderr, err := runReconcile(t, command, "resources", "--socket", "primary", "--dry-run", "-o", "json")
	if err != nil || stderr != "" {
		t.Fatalf("second dry-run error=%v stderr=%q", err, stderr)
	}
	if first != second {
		t.Fatalf("dry-run output changed across identical plans:\n--- first ---\n%s\n--- second ---\n%s", first, second)
	}
	for _, want := range []string{`"drift": "missing"`, `"surface": "tmux"`, `"after": "project-test-1"`, `"tmuxFlag": "-L"`} {
		if !strings.Contains(first, want) {
			t.Fatalf("dry-run JSON missing %q:\n%s", want, first)
		}
	}
	if got := store.snapshot(); got != registryBefore || store.writes != 0 || store.transactions != 0 {
		t.Fatalf("dry-run mutated Registry: transactions=%d writes=%d\n%s", store.transactions, store.writes, got)
	}
	if got := server.state(); got != tmuxBefore || tmuxMutationCallCount(server) != 0 {
		t.Fatalf("dry-run mutated tmux:\n--- got ---\n%s\n--- want ---\n%s", got, tmuxBefore)
	}
}

func TestResourceReconcileHumanPlanShowsTargetCountsAndWrites(t *testing.T) {
	t.Parallel()

	command, _, _, _, _ := newReconcileFixture(t, "-L", "primary")
	stdout, stderr, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary")
	if err != nil || stderr != "" {
		t.Fatalf("human dry-run error=%v stderr=%q", err, stderr)
	}
	for _, want := range []string{
		"target: tmux -L primary",
		"outcome: planned",
		"counts: changed=",
		"tmux set-option",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human plan missing %q:\n%s", want, stdout)
		}
	}
}

func TestResourceReconcileRegistryItemsClassifyStaleAndOrphan(t *testing.T) {
	t.Parallel()

	before := bindingFixture(t, t.TempDir())
	after := before.Clone()
	after.Windows[0].Metadata.DisplayName = "renamed-runtime"
	after.Windows[1].Status.Conditions = append(after.Windows[1].Status.Conditions, coremetadata.Condition{
		Type: coremetadata.ConditionMissingRuntime, Status: coremetadata.ConditionTrue, Reason: coremetadata.ReasonRuntimeUnbound,
	})
	items := registryReconcileItems(before, after, resourcePlanUIDNormalizer{created: map[string]string{}})
	found := map[resourceDriftKind]bool{}
	for _, item := range items {
		found[item.Drift] = true
	}
	if !found[resourceDriftStale] || !found[resourceDriftOrphan] {
		t.Fatalf("registry drift classifications = %#v, want stale and orphan", items)
	}
}

func TestResourceReconcileRepairsStaleMirrorsForExactBindings(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	registry := bindingFixture(t, root)
	server := bindingFixtureServer()
	session := server.session(driftedSessionName)
	session.opts[tmuxopts.ProjectUIDSession] = registry.Projects[0].Metadata.UID
	session.opts[tmuxopts.ProjectNameSession] = "stale-project"
	for index := range session.windows {
		session.windows[index].opts[tmuxopts.WindowUID] = registry.Windows[index].Metadata.UID
		session.windows[index].opts[tmuxopts.WindowName] = "stale-window"
		session.windows[index].opts[tmuxopts.AutomaticRenameWindow] = "on"
		session.windows[index].panes[0].opts[tmuxopts.PaneUID] = registry.Panes[index].Metadata.UID
		session.windows[index].panes[0].opts[tmuxopts.PaneName] = "stale-pane"
	}
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00primary": server}}
	store := &fakeResourceStore{registry: registry, dirs: map[string]bool{root: true}, now: resourceFixtureClock}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		newReconciler: bindingFixtureReconciler(root),
	}
	preview, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("stale mirror preview: %v", err)
	}
	for _, field := range []string{tmuxopts.ProjectNameSession, tmuxopts.WindowName, tmuxopts.PaneName, tmuxopts.AutomaticRenameWindow} {
		if !strings.Contains(preview, `"field": "`+field+`"`) || !strings.Contains(preview, `"drift": "stale"`) {
			t.Fatalf("stale mirror preview missing %s:\n%s", field, preview)
		}
	}
	if _, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("repair stale mirrors: %v", err)
	}
	if session.opts[tmuxopts.ProjectNameSession] != registry.Projects[0].Metadata.Name {
		t.Fatal("Project name mirror did not converge")
	}
	for index := range session.windows {
		if session.windows[index].opts[tmuxopts.WindowName] != registry.Windows[index].Metadata.Name ||
			session.windows[index].opts[tmuxopts.AutomaticRenameWindow] != "off" ||
			session.windows[index].panes[0].opts[tmuxopts.PaneName] != registry.Panes[index].Metadata.Name {
			t.Fatalf("Window/Pane mirror %d did not converge: %+v %+v", index, session.windows[index].opts, session.windows[index].panes[0].opts)
		}
	}
}

func TestResourceReconcileInitialPaneMirrorCarriesExactObservedLabelBefore(t *testing.T) {
	t.Parallel()

	command, store, server, _, _ := newReconcileFixture(t, "-L", "primary")
	pane := server.session("alpha").windows[0].panes[0]
	pane.opts[tmuxopts.PaneName] = "buildlog"
	want := store.registry.Panes[0].Metadata.Name
	mutationsBefore := tmuxMutationCallCount(server)

	preview, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("preview initial Pane label mirror: %v\n%s", err, preview)
	}
	report := parseControllerReport(t, preview)
	found := false
	for _, item := range report.Items {
		if item["field"] != tmuxopts.PaneName || item["target"] != pane.id {
			continue
		}
		found = true
		if item["before"] != "buildlog" || item["after"] != want {
			t.Fatalf("Pane label receipt = before:%v after:%v, want buildlog -> %q\n%s",
				item["before"], item["after"], want, preview)
		}
	}
	if !found {
		t.Fatalf("preview omitted the exact Pane label mirror receipt:\n%s", preview)
	}
	if got := tmuxMutationCallCount(server); got != mutationsBefore {
		t.Fatalf("planning the Pane label executed %d mutation(s)", got-mutationsBefore)
	}

	executed, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("execute initial Pane label mirror: %v\n%s", err, executed)
	}
	if got := pane.opts[tmuxopts.PaneName]; got != want {
		t.Fatalf("Pane label = %q, want %q", got, want)
	}
}

func TestResourceReconcilePaneLabelDriftAfterPlanningRefusesBeforeFirstWrite(t *testing.T) {
	t.Parallel()

	command, _, server, _, _ := newReconcileFixture(t, "-L", "primary")
	pane := server.session("alpha").windows[0].panes[0]
	pane.opts[tmuxopts.PaneName] = "buildlog"
	base := command.resources
	command.resources = &resourceStore{
		load: base.load, snapshot: base.snapshot, mutator: base.mutator,
		updateConvergent: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
			registry, changed, err := base.updateConvergent(fn)
			pane.opts[tmuxopts.PaneName] = "operator-drift"
			return registry, changed, err
		},
	}
	mutationsBefore := tmuxMutationCallCount(server)

	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "option "+tmuxopts.PaneName+" drifted before write") {
		t.Fatalf("Pane label drift was not refused by its exact Before guard: err=%v\n%s", err, stdout)
	}
	if got := tmuxMutationCallCount(server); got != mutationsBefore {
		t.Fatalf("Pane label drift executed %d mutation(s) before refusal", got-mutationsBefore)
	}
	if got := pane.opts[tmuxopts.PaneName]; got != "operator-drift" {
		t.Fatalf("refusal overwrote the concurrently changed Pane label: %q", got)
	}
}

func TestResourcePlanRecorderRefusesUnreadableOrdinaryOptionBefore(t *testing.T) {
	t.Parallel()

	server := newFakeTmux()
	pane := server.addSession("alpha").windows[0].panes[0]
	server.fail = []string{"display-message", "#{" + tmuxopts.PaneName + "}"}
	recorder := newResourcePlanTmuxRunner(server)
	_, err := recorder.Run(context.Background(), "tmux", "set-option", "-p", "-t", pane.id, "-q", tmuxopts.PaneName, "desired")
	if err == nil || !strings.Contains(err.Error(), "cannot observe "+tmuxopts.PaneName+" on exact target "+pane.id) {
		t.Fatalf("unreadable Pane label Before error = %v", err)
	}
	if len(recorder.writes) != 0 || tmuxMutationCallCount(server) != 0 {
		t.Fatalf("unreadable Before recorded/executed a write: recorded=%d mutations=%d", len(recorder.writes), tmuxMutationCallCount(server))
	}
}

func TestResourceReconcileRepairsRebindPathFromAuthoritativeProjectUID(t *testing.T) {
	t.Parallel()

	oldRoot, newRoot := t.TempDir(), t.TempDir()
	registry := bindingFixture(t, newRoot)
	server := bindingFixtureServer()
	session := server.session(driftedSessionName)
	project := registry.Projects[0]
	session.opts[tmuxopts.ProjectUIDSession] = project.Metadata.UID
	session.opts[tmuxopts.ProjectNameSession] = project.Metadata.Name
	session.opts[tmuxopts.ProjectPathSession] = oldRoot
	for index := range session.windows {
		session.windows[index].opts[tmuxopts.WindowUID] = registry.Windows[index].Metadata.UID
		session.windows[index].opts[tmuxopts.WindowName] = registry.Windows[index].Metadata.Name
		session.windows[index].opts[tmuxopts.AutomaticRenameWindow] = "off"
		session.windows[index].panes[0].opts[tmuxopts.PaneUID] = registry.Panes[index].Metadata.UID
		session.windows[index].panes[0].opts[tmuxopts.PaneName] = registry.Panes[index].Metadata.Name
	}
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00primary": server}}
	store := &fakeResourceStore{registry: registry, dirs: map[string]bool{newRoot: true}, now: resourceFixtureClock}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		newReconciler: bindingFixtureReconciler(newRoot),
	}

	preview, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("rebind drift preview: %v\n%s", err, preview)
	}
	for _, want := range []string{
		`"field": "` + tmuxopts.ProjectPathSession + `"`,
		`"before": "` + oldRoot + `"`,
		`"after": "` + newRoot + `"`,
	} {
		if !strings.Contains(preview, want) {
			t.Fatalf("rebind preview missing %q:\n%s", want, preview)
		}
	}
	if strings.Contains(preview, `"action": "refuse"`) {
		t.Fatalf("valid exact Project UID was refused because its path was stale:\n%s", preview)
	}
	if _, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("repair rebind drift: %v", err)
	}
	if got := session.opts[tmuxopts.ProjectPathSession]; got != newRoot {
		t.Fatalf("project path mirror = %q, want %q", got, newRoot)
	}
	if session.name != driftedSessionName || session.opts[tmuxopts.ProjectUIDSession] != project.Metadata.UID {
		t.Fatalf("rebind retry changed session identity: name=%q opts=%v", session.name, session.opts)
	}
	server.calls = nil
	repeat, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil || !strings.Contains(repeat, `"outcome": "no-op"`) || tmuxMutationCallCount(server) != 0 {
		t.Fatalf("repeat reconcile was not a tmux-write-free no-op: err=%v mutations=%d\n%s", err, tmuxMutationCallCount(server), repeat)
	}
}

func TestResourceReconcileExecuteConvergesOneSocketAndRepeatsNoop(t *testing.T) {
	t.Parallel()

	command, store, primary, runner, _ := newReconcileFixture(t, "-L", "primary")
	secondary := newFakeTmux()
	secondary.socketPath = "/tmp/fake-tmux/secondary"
	secondary.addSession("other")
	secondaryBefore := secondary.state()
	runner.servers["-L\x00secondary"] = secondary

	first, stderr, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil || stderr != "" {
		t.Fatalf("execute error=%v stderr=%q\n%s", err, stderr, first)
	}
	if !strings.Contains(first, `"outcome": "changed"`) || store.writes != 1 {
		t.Fatalf("execute result/writes mismatch: writes=%d\n%s", store.writes, first)
	}
	session := primary.session("alpha")
	if got := session.opts[tmuxopts.ProjectUIDSession]; got == "" {
		t.Fatal("execute did not mirror Project uid")
	}
	if got := session.windows[0].opts[tmuxopts.WindowUID]; got == "" {
		t.Fatal("execute did not mirror Window uid")
	}
	if got := session.windows[0].panes[0].opts[tmuxopts.PaneUID]; got == "" {
		t.Fatal("execute did not mirror Pane uid")
	}
	if got := secondary.state(); got != secondaryBefore {
		t.Fatalf("unrelated socket changed:\n%s", got)
	}

	primary.calls = nil
	second, stderr, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil || stderr != "" {
		t.Fatalf("repeat error=%v stderr=%q\n%s", err, stderr, second)
	}
	if !strings.Contains(second, `"outcome": "no-op"`) || !strings.Contains(second, `"noOp": 1`) {
		t.Fatalf("repeat was not a no-op:\n%s", second)
	}
	if store.writes != 1 || tmuxMutationCallCount(primary) != 0 {
		t.Fatalf("repeat mutated state: Registry writes=%d tmux mutations=%d", store.writes, tmuxMutationCallCount(primary))
	}
}

func TestResourceReconcileBlankD2PaneRemainsUnattributed(t *testing.T) {
	t.Parallel()

	command, store, primary, _, _ := newReconcileFixture(t, "-L", "primary")
	if _, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("seed reconcile: %v", err)
	}
	raw := newFakeTmuxPane(primary.mint("%"))
	raw.opts[aiPaneAgentOption] = "antigravity"
	primary.session("alpha").windows[0].panes = append(primary.session("alpha").windows[0].panes, raw)
	primary.calls = nil

	first, stderr, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil || stderr != "" {
		t.Fatalf("blank orphan reconcile error=%v stderr=%q\n%s", err, stderr, first)
	}
	if raw.opts[tmuxopts.PaneUID] != "" {
		t.Fatalf("blank D2 Pane %s received Registry identity %q", raw.id, raw.opts[tmuxopts.PaneUID])
	}
	if !strings.Contains(first, `"outcome": "no-op"`) {
		t.Fatalf("blank D2 reconcile report:\n%s", first)
	}

	primary.calls = nil
	repeat, stderr, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil || stderr != "" || !strings.Contains(repeat, `"outcome": "no-op"`) || tmuxMutationCallCount(primary) != 0 {
		t.Fatalf("blank orphan repeat error=%v stderr=%q mutations=%d\n%s", err, stderr, tmuxMutationCallCount(primary), repeat)
	}
	if store.writes != 1 {
		t.Fatalf("Registry writes = %d, want only the initial live Window handle observation and no D2 import", store.writes)
	}
}

func TestApprovedD3ModeStillPlansNoBlankD2Import(t *testing.T) {
	t.Parallel()

	command, store, primary, _, _ := newReconcileFixture(t, "-L", "primary")
	if _, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("seed reconcile: %v", err)
	}
	raw := newFakeTmuxPane(primary.mint("%"))
	raw.opts[aiPaneAgentOption] = "antigravity"
	primary.session("alpha").windows[0].panes = append(primary.session("alpha").windows[0].panes, raw)
	primary.calls = nil

	target, err := tmuxSocketNameTarget("primary")
	if err != nil {
		t.Fatalf("socket target: %v", err)
	}
	planner := resourceReconcilePlanner{
		reader:        explicitTmuxRunner{runner: command.runner, target: target},
		store:         command.resources,
		newReconciler: command.newReconciler,
	}
	planner.approvedOrphanImport = true
	registryBefore, writesBefore := store.snapshot(), store.writes
	plan, err := planner.build(context.Background(), store.registry.Clone())
	if err != nil {
		t.Fatalf("plan blank orphan: %v", err)
	}
	for _, write := range plan.writes {
		if write.target == raw.id && write.field == tmuxopts.PaneUID {
			t.Fatalf("approved D3 mode planned blank D2 import: %+v", write)
		}
	}
	if store.snapshot() != registryBefore || store.writes != writesBefore || tmuxMutationCallCount(primary) != 0 {
		t.Fatalf("D2 planning mutated state: Registry writes=%d want=%d tmux mutations=%d", store.writes, writesBefore, tmuxMutationCallCount(primary))
	}
}

func TestResourceReconcilePreservesRegistryGraphOwnedByAnotherSocket(t *testing.T) {
	t.Parallel()

	command, store, _, runner, _ := newReconcileFixture(t, "-L", "primary")
	otherRoot := t.TempDir()
	store.dirs[otherRoot] = true
	project, err := store.mutator().RegisterProject(&store.registry, coremetadata.RegisterProjectOptions{
		Root: otherRoot, DefaultShell: "/bin/zsh", OperationID: "op-unrelated-secondary",
	})
	if err != nil {
		t.Fatalf("seed unrelated Registry graph: %v", err)
	}
	window := store.registry.Windows[len(store.registry.Windows)-1]
	pane := store.registry.Panes[len(store.registry.Panes)-1]
	secondary := newFakeTmux()
	secondary.socketPath = "/tmp/fake-tmux/secondary"
	secondarySession := secondary.addSession("secondary")
	secondarySession.opts[inttmux.ProjectPathSessionOption] = otherRoot
	secondarySession.opts[tmuxopts.ProjectUIDSession] = project.Project.Metadata.UID
	secondarySession.opts[tmuxopts.ProjectNameSession] = project.Project.Metadata.Name
	secondarySession.windows[0].opts[tmuxopts.WindowUID] = window.Metadata.UID
	secondarySession.windows[0].opts[tmuxopts.WindowName] = window.Metadata.Name
	secondarySession.windows[0].panes[0].opts[tmuxopts.PaneUID] = pane.Metadata.UID
	secondarySession.windows[0].panes[0].opts[tmuxopts.PaneName] = pane.Metadata.Name
	runner.servers["-L\x00secondary"] = secondary

	beforeGraph := resourceRegistryProjectGraph(store.registry, map[string]bool{project.Project.Metadata.UID: true})
	secondaryBefore := secondary.state()
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("primary reconcile: %v\n%s", err, stdout)
	}
	afterGraph := resourceRegistryProjectGraph(store.registry, map[string]bool{project.Project.Metadata.UID: true})
	if !reflect.DeepEqual(beforeGraph, afterGraph) {
		t.Fatalf("unrelated Registry graph changed:\n--- before ---\n%#v\n--- after ---\n%#v", beforeGraph, afterGraph)
	}
	if got := secondary.state(); got != secondaryBefore {
		t.Fatalf("secondary socket changed:\n--- before ---\n%s\n--- after ---\n%s", secondaryBefore, got)
	}
}

func TestResourceReconcileTargetRulesArePreMutationAndExact(t *testing.T) {
	t.Parallel()

	command, store, server, runner, _ := newReconcileFixture(t, "-S", filepath.Join(t.TempDir(), "inside.sock"))
	path := ""
	for key := range runner.servers {
		path = strings.TrimPrefix(key, "-S\x00")
	}
	command.lookupEnv = func(name string) string {
		if name == "TMUX" {
			return path + ",1234,7"
		}
		return ""
	}
	if _, _, err := runReconcile(t, command, "resources", "--dry-run"); err != nil {
		t.Fatalf("inherited target: %v", err)
	}
	for _, call := range runner.calls {
		if call.flag != "-S" || call.value != path {
			t.Fatalf("inherited target escaped exact socket: %+v", call)
		}
	}
	runner.calls = nil
	command.lookupEnv = func(string) string { return "" }
	for _, args := range [][]string{
		{"resources"},
		{"resources", "--socket-path", "relative"},
		{"resources", "--socket", "one", "--socket-path", path},
	} {
		if _, _, err := runReconcile(t, command, args...); !IsUsageError(err) {
			t.Fatalf("args %v error=%v, want usage error", args, err)
		}
	}
	if store.transactions != 0 || store.writes != 0 || tmuxMutationCallCount(server) != 0 || len(runner.calls) != 0 {
		t.Fatalf("invalid target reached mutation/read: transactions=%d writes=%d tmux=%d routed=%d", store.transactions, store.writes, tmuxMutationCallCount(server), len(runner.calls))
	}
}

func TestResourceReconcileRefusesForeignAndAmbiguousBindings(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	registry := bindingFixture(t, root)
	server := bindingFixtureServer()
	session := server.session(driftedSessionName)
	session.opts[tmuxopts.ProjectUIDSession] = "prj-foreign"
	session.windows[0].opts[tmuxopts.WindowUID] = "win-foreign"
	session.windows[1].opts[tmuxopts.WindowUID] = "win-foreign"
	before := server.state()
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00primary": server}}
	store := &fakeResourceStore{registry: registry, dirs: map[string]bool{root: true}, now: resourceFixtureClock}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		newReconciler: bindingFixtureReconciler(root),
	}
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil {
		t.Fatal("foreign/ambiguous execute unexpectedly succeeded")
	}
	for _, want := range []string{`"drift": "foreign"`, `"action": "refuse"`, `"outcome": "refused"`, `"retry": "projmux reconcile resources --socket 'primary'"`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("negative report missing %q:\n%s", want, stdout)
		}
	}
	if got := session.opts[tmuxopts.ProjectUIDSession]; got != "" {
		t.Fatalf("automatic D3 L8 did not clear foreign Project mirror: %q", got)
	}
	if session.windows[0].opts[tmuxopts.WindowUID] != "win-foreign" || session.windows[1].opts[tmuxopts.WindowUID] != "win-foreign" {
		t.Fatalf("D4 Window claims changed: %s", server.state())
	}
	if got := server.state(); got == before || tmuxMutationCallCount(server) != 1 {
		t.Fatalf("expected only D3 Project L8 mutation:\n--- got ---\n%s\n--- before ---\n%s", got, before)
	}
}

func TestResourceReconcileRefusesDuplicateUIDLessProjectClaims(t *testing.T) {
	t.Parallel()

	command, store, server, _, root := newReconcileFixture(t, "-L", "primary")
	if _, err := store.mutator().RegisterProject(&store.registry, coremetadata.RegisterProjectOptions{
		Root: root, DefaultShell: "/bin/zsh", OperationID: "op-duplicate-project-claim",
	}); err != nil {
		t.Fatalf("seed authoritative Project: %v", err)
	}
	second := server.addSession("beta")
	second.opts[inttmux.ProjectPathSessionOption] = root
	registryBefore, tmuxBefore := store.snapshot(), server.state()

	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil {
		t.Fatal("duplicate UID-less Project claims unexpectedly succeeded")
	}
	for _, want := range []string{
		`"failed": 2`,
		`"reason": "multiple live sessions resolve to the same exact Registry Project"`,
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("duplicate Project claim report missing %q:\n%s", want, stdout)
		}
	}
	if store.writes != 0 || store.snapshot() != registryBefore {
		t.Fatalf("duplicate Project claims mutated Registry: writes=%d snapshot=%s", store.writes, store.snapshot())
	}
	if got := server.state(); got != tmuxBefore || tmuxMutationCallCount(server) != 0 {
		t.Fatalf("duplicate Project claims mutated tmux:\n--- got ---\n%s\n--- want ---\n%s", got, tmuxBefore)
	}
}

func TestResourceReconcileForeignProjectRunsAutomaticL8WithoutRegistryLoss(t *testing.T) {
	t.Parallel()

	command, store, server, _, _ := newReconcileFixture(t, "-L", "primary")
	session := server.session("alpha")
	session.opts[tmuxopts.ProjectUIDSession] = "prj-foreign"
	registryBefore := store.snapshot()
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("foreign Project L8 failed: %v\n%s", err, stdout)
	}
	if !strings.Contains(stdout, `"action": "L8-discard-mirror"`) {
		t.Fatalf("foreign Project L8 missing:\n%s", stdout)
	}
	if store.snapshot() != registryBefore || store.writes != 0 {
		t.Fatalf("automatic L8 changed Registry: writes=%d snapshot=%s", store.writes, store.snapshot())
	}
	if got := session.opts[tmuxopts.ProjectUIDSession]; got != "" {
		t.Fatalf("foreign Project mirror survived L8: %q", got)
	}
}

func TestResourceReconcileRefusesAmbiguousRootlessProjectScope(t *testing.T) {
	t.Parallel()

	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	store := &fakeResourceStore{registry: coremetadata.NewRegistry(), dirs: map[string]bool{firstRoot: true, secondRoot: true}, now: resourceFixtureClock}
	mutator := store.mutator()
	for index, root := range []string{firstRoot, secondRoot} {
		result, err := mutator.RegisterProject(&store.registry, coremetadata.RegisterProjectOptions{
			Root: root, DefaultShell: "/bin/zsh", OperationID: fmt.Sprintf("op-ambiguous-%d", index),
		})
		if err != nil {
			t.Fatalf("seed ambiguous Project %d: %v", index, err)
		}
		project, _ := store.registry.Project(result.Project.Metadata.UID)
		project.Status.Session = &coremetadata.SessionProjection{Name: "shared", Live: true}
	}
	server := newFakeTmux()
	server.addSession("shared")
	before := server.state()
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00primary": server}}
	command := &resourceReconcileCommand{
		runner: runner, resources: store.store(), lookupEnv: func(string) string { return "" },
		newReconciler: func(runner tmuxCommandRunner, sessions sessionLister) *registryReconciler {
			mirror := intmetadata.NewMirror(runner)
			return &registryReconciler{
				discoverRoots: func() ([]string, error) { return nil, nil }, liveSessions: sessions.ExistingSessions,
				observeLegacy: mirror.ObserveLegacySessionTargets, mirror: mirror, shell: "/bin/zsh",
				sessionNameFor: func(string) string { return "shared" },
			}
		},
	}
	registryBefore := store.snapshot()
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil || !strings.Contains(stdout, "multiple Registry Projects claim the live session-name edge") || !strings.Contains(stdout, `"outcome": "refused"`) {
		t.Fatalf("ambiguous rootless scope was not explicitly refused: err=%v\n%s", err, stdout)
	}
	if store.writes != 0 || store.snapshot() != registryBefore || server.state() != before || tmuxMutationCallCount(server) != 0 {
		t.Fatal("ambiguous rootless scope mutated Registry or tmux")
	}
}

func TestResourceReconcileCommitFailureDoesNotPublishAllocatedUIDs(t *testing.T) {
	t.Parallel()

	command, store, server, _, _ := newReconcileFixture(t, "-L", "primary")
	registryBefore, tmuxBefore := store.snapshot(), server.state()
	base := command.resources
	command.resources = &resourceStore{
		load:    base.load,
		mutator: base.mutator,
		updateConvergent: func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error) {
			working := store.registry.Clone()
			if err := fn(&working); err != nil {
				return coremetadata.Registry{}, false, err
			}
			return coremetadata.Registry{}, false, errors.New("injected registry commit failure")
		},
	}
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil || !strings.Contains(stdout, `"outcome": "failed"`) || !strings.Contains(stdout, "registry commit") {
		t.Fatalf("commit failure was not reported: err=%v\n%s", err, stdout)
	}
	if got := store.snapshot(); got != registryBefore {
		t.Fatalf("failed commit changed Registry:\n%s", got)
	}
	if got := server.state(); got != tmuxBefore || tmuxMutationCallCount(server) != 0 {
		t.Fatalf("failed commit published a planned uid:\n--- got ---\n%s\n--- want ---\n%s", got, tmuxBefore)
	}
}

func TestResourceReconcileTmuxFailureLeavesRetryableRegistryAuthority(t *testing.T) {
	t.Parallel()

	command, store, server, _, _ := newReconcileFixture(t, "-L", "primary")
	server.fail = []string{"set-option", tmuxopts.WindowUID}
	first, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err == nil {
		t.Fatal("injected tmux failure unexpectedly succeeded")
	}
	if store.writes != 1 || len(store.registry.Projects) != 1 || len(store.registry.Windows) != 1 || len(store.registry.Panes) != 1 {
		t.Fatalf("Registry authority was not committed before tmux failure: writes=%d snapshot=%s", store.writes, store.snapshot())
	}
	for _, want := range []string{`"outcome": "failed"`, `"completedStages"`, `"remainingDrift"`, `"retry": "projmux reconcile resources --socket 'primary'"`} {
		if !strings.Contains(first, want) {
			t.Fatalf("partial report missing %q:\n%s", want, first)
		}
	}

	second, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("exact retry failed: %v\n%s", err, second)
	}
	if !strings.Contains(second, `"outcome": "changed"`) {
		t.Fatalf("retry did not converge:\n%s", second)
	}
	if len(store.registry.Projects) != 1 || len(store.registry.Windows) != 1 || len(store.registry.Panes) != 1 {
		t.Fatalf("retry duplicated Registry identity: %s", store.snapshot())
	}
	third, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil || !strings.Contains(third, `"outcome": "no-op"`) {
		t.Fatalf("post-retry repeat is not no-op: err=%v\n%s", err, third)
	}
}

func TestFreshResourceReconcileCapturesAutomaticRenameBeforeAndRepeatsEmpty(t *testing.T) {
	command, _, server, _, _ := newReconcileFixture(t, "-L", "primary")
	window := server.sessions[0].windows[0]
	window.opts[tmuxopts.AutomaticRenameWindow] = "on"

	first, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("fresh reconcile with live automatic-rename=on: %v\n%s", err, first)
	}
	for _, want := range []string{`"field": "automatic-rename"`, `"before": "on"`, `"after": "off"`} {
		if !strings.Contains(first, want) {
			t.Fatalf("fresh plan omitted exact automatic-rename receipt %s:\n%s", want, first)
		}
	}
	if got := window.opts[tmuxopts.AutomaticRenameWindow]; got != "off" {
		t.Fatalf("automatic-rename = %q, want converged off", got)
	}
	automaticWrites, uidWriteIndex, automaticObserveIndex, automaticWriteIndex := 0, -1, -1, -1
	for index, call := range server.calls {
		if slices.Contains(call, "set-option") && slices.Contains(call, tmuxopts.WindowUID) && uidWriteIndex < 0 {
			uidWriteIndex = index
		}
		if slices.Contains(call, "display-message") && slices.Contains(call, "#{"+tmuxopts.AutomaticRenameWindow+"}") && automaticObserveIndex < 0 {
			automaticObserveIndex = index
		}
		if slices.Contains(call, "set-option") && slices.Contains(call, tmuxopts.AutomaticRenameWindow) {
			automaticWrites++
			if automaticWriteIndex < 0 {
				automaticWriteIndex = index
			}
		}
	}
	if automaticWrites != 1 {
		t.Fatalf("automatic-rename writes = %d, want one exact planned transition", automaticWrites)
	}
	if uidWriteIndex < 0 || automaticObserveIndex < 0 || automaticWriteIndex < 0 || automaticObserveIndex >= automaticWriteIndex || uidWriteIndex >= automaticWriteIndex {
		t.Fatalf("fresh sibling order uid-write=%d automatic-observe=%d automatic-write=%d calls=%#v", uidWriteIndex, automaticObserveIndex, automaticWriteIndex, server.calls)
	}

	writesBefore := tmuxMutationCallCount(server)
	repeat, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil || !strings.Contains(repeat, `"outcome": "no-op"`) {
		t.Fatalf("repeat reconcile = err %v\n%s", err, repeat)
	}
	if got := tmuxMutationCallCount(server); got != writesBefore {
		t.Fatalf("repeat executed %d unsafe tmux write(s)", got-writesBefore)
	}
}

func TestResourceReconcileHumanFailureListsRemainingDriftItems(t *testing.T) {
	t.Parallel()

	command, _, server, _, _ := newReconcileFixture(t, "-L", "primary")
	server.fail = []string{"set-option", tmuxopts.WindowUID}
	stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary")
	if err == nil {
		t.Fatal("injected human-output failure unexpectedly succeeded")
	}
	for _, want := range []string{"remaining drift:", "tmux:set-option:", "[missing] tmux set-option", "retry: projmux reconcile resources --socket 'primary'"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("human partial report missing %q:\n%s", want, stdout)
		}
	}
}

func TestResourceReconcileKeepsReadAndDiagnosticRoutesOutsideMutationMigration(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"doctor"}, {"get", "projects"}, {"describe", "project", "alpha"}, {"reconcile", "resources", "--dry-run"}} {
		if shouldRunLegacyHookMigrations(args) {
			t.Fatalf("read/repair boundary %v triggered unrelated legacy migration", args)
		}
	}
}

func TestResourceRepairMatcherRefusesForeignWithoutChangingLifecycleMatcher(t *testing.T) {
	t.Parallel()

	registry := bindingFixture(t, t.TempDir())
	lifecycle := coremetadata.NewBindingMatcher(coremetadata.RuntimeObservation{}).MatchWindow(&registry, registry.Projects[0].Metadata.UID, "win-foreign")
	repair := coremetadata.NewRepairBindingMatcher(coremetadata.RuntimeObservation{}).MatchWindow(&registry, registry.Projects[0].Metadata.UID, "win-foreign")
	if lifecycle.Kind != coremetadata.AdoptionForeign || repair.Kind != coremetadata.AdoptionRefused {
		t.Fatalf("matcher modes lifecycle=%+v repair=%+v", lifecycle, repair)
	}
}
