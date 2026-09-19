package app

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

const endedSessionOtherSocket = "/tmp/fake-tmux/secondary"

// addEndedSessionProject registers one named Project and records its session
// projection as live on socketPath.
func addEndedSessionProject(t *testing.T, store *fakeResourceStore, name, socketPath string) string {
	t.Helper()
	root := t.TempDir()
	store.dirs[root] = true
	result, err := store.mutator().RegisterProject(&store.registry, coremetadata.RegisterProjectOptions{
		Root: root, Name: name, DefaultShell: "/bin/zsh", OperationID: "op-ended-" + name,
	})
	if err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
	uid := result.Project.Metadata.UID
	if _, err := store.mutator().BindLiveProjectSession(&store.registry, uid, name, socketPath); err != nil {
		t.Fatalf("bind %s live on %q: %v", name, socketPath, err)
	}
	return uid
}

func storedSessionProjection(t *testing.T, store *fakeResourceStore, uid string) coremetadata.SessionProjection {
	t.Helper()
	project, ok := store.registry.Project(uid)
	if !ok || project.Status.Session == nil {
		t.Fatalf("Project %s has no session projection", uid)
	}
	return *project.Status.Session
}

// TestResourceReconcileLowersLiveProjectionWhoseSessionEndedOnTheExactSocket
// is the stale live flag after a raw kill-session: the Project was recorded
// live on the reconciled server, its session is gone from that server, and the
// ordinary reconcile lowers it -- previewed with a reason, committed with the
// name and socketPath kept, and a no-op on the next pass. Projects recorded on
// another server or on no known server are byte-identical, and one whose
// session is still present (untagged) stays live.
func TestResourceReconcileLowersLiveProjectionWhoseSessionEndedOnTheExactSocket(t *testing.T) {
	t.Parallel()

	command, store, server, _, _ := newReconcileFixture(t, "-L", "primary")
	if stdout, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json"); err != nil {
		t.Fatalf("seed reconcile: %v\n%s", err, stdout)
	}
	gone := addEndedSessionProject(t, store, "gone", server.socketPath)
	elsewhere := addEndedSessionProject(t, store, "elsewhere", endedSessionOtherSocket)
	unknown := addEndedSessionProject(t, store, "unknown", "")
	untagged := addEndedSessionProject(t, store, "untagged", server.socketPath)
	server.addSession("untagged")
	// A bare session named like a Project's recorded session is claimed by
	// the scoped pass itself, so only its projection is asserted below.
	untouched := map[string]bool{elsewhere: true, unknown: true}
	untouchedGraph := func() coremetadata.Registry { return resourceRegistryProjectGraph(store.registry, untouched) }
	graphBefore := untouchedGraph()

	registryBefore, writesBefore := store.snapshot(), store.writes
	preview, _, err := runReconcile(t, command, "resources", "--dry-run", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("dry-run: %v\n%s", err, preview)
	}
	var report resourceReconcileReport
	if err := json.Unmarshal([]byte(preview), &report); err != nil {
		t.Fatalf("dry-run json: %v\n%s", err, preview)
	}
	wantKey := "registry:update:project:project/gone"
	var lowerItem *resourceReconcileItem
	for i := range report.Items {
		item := report.Items[i]
		if strings.Contains(item.Target, "elsewhere") || strings.Contains(item.Target, "unknown") || item.Field == "status.session.live" && item.Key != wantKey {
			t.Fatalf("preview planned an item for a Project it must not touch: %+v", item)
		}
		if item.Key == wantKey {
			lowerItem = &report.Items[i]
		}
	}
	if lowerItem == nil {
		t.Fatalf("preview has no %s item:\n%s", wantKey, preview)
	}
	wantReason := `session "gone" is absent on the exact socket ` + server.socketPath
	if lowerItem.Field != "status.session.live" || lowerItem.Before != "true" || lowerItem.After != "false" ||
		!lowerItem.Divergence.Valid() || !strings.Contains(lowerItem.Reason, wantReason) || lowerItem.Outcome != "planned" {
		t.Fatalf("lower item = %+v, want status.session.live true -> false with reason %q", *lowerItem, wantReason)
	}
	if store.snapshot() != registryBefore || store.writes != writesBefore {
		t.Fatalf("dry-run wrote the Registry: writes %d -> %d", writesBefore, store.writes)
	}

	executed, _, err := runReconcile(t, command, "resources", "--socket", "primary", "-o", "json")
	if err != nil {
		t.Fatalf("execute: %v\n%s", err, executed)
	}
	if !strings.Contains(executed, `"`+wantKey+`"`) || !strings.Contains(executed, `"Registry commit"`) {
		t.Fatalf("execute did not commit the lower:\n%s", executed)
	}
	if got, want := storedSessionProjection(t, store, gone), (coremetadata.SessionProjection{Name: "gone", SocketPath: server.socketPath}); got != want {
		t.Fatalf("lowered projection = %+v, want %+v", got, want)
	}
	if got, want := storedSessionProjection(t, store, untagged), (coremetadata.SessionProjection{Name: "untagged", Live: true, SocketPath: server.socketPath}); got != want {
		t.Fatalf("present untagged session projection = %+v, want %+v", got, want)
	}
	if graphAfter := untouchedGraph(); !reflect.DeepEqual(graphBefore, graphAfter) {
		t.Fatalf("Projects on another, no, or a still-present session changed:\n--- before ---\n%#v\n--- after ---\n%#v", graphBefore, graphAfter)
	}

	settled, writesSettled := store.snapshot(), store.writes
	repeat, _, err := runReconcile(t, command, "resources", "--socket", "primary")
	if err != nil || !strings.Contains(repeat, "Registry commit (no-op)") || !strings.Contains(repeat, "outcome: no-op") {
		t.Fatalf("second reconcile is not a no-op: err=%v\n%s", err, repeat)
	}
	if store.snapshot() != settled || store.writes != writesSettled {
		t.Fatal("second reconcile wrote the Registry")
	}
}

// TestResourceReconcileNeverLowersWithoutAVerifiedSocketPath pins the empty
// judgement input: a planner with no exact socket path lowers nothing, even
// a live projection whose recorded session is absent.
func TestResourceReconcileNeverLowersWithoutAVerifiedSocketPath(t *testing.T) {
	t.Parallel()

	command, store, server, _, _ := newReconcileFixture(t, "-L", "primary")
	gone := addEndedSessionProject(t, store, "gone", server.socketPath)
	target, err := tmuxSocketNameTarget("primary")
	if err != nil {
		t.Fatal(err)
	}
	planner := resourceReconcilePlanner{
		reader: explicitTmuxRunner{runner: command.runner, target: target}, store: command.resources,
		newReconciler: command.newReconciler,
	}
	plan, err := planner.build(context.Background(), store.registry.Clone())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	project, _ := plan.registry.Project(gone)
	if project.Status.Session == nil || !project.Status.Session.Live {
		t.Fatalf("planner without a verified socket lowered %+v", project.Status.Session)
	}
	for _, item := range plan.items {
		if item.Field == "status.session.live" {
			t.Fatalf("planner without a verified socket planned %+v", item)
		}
	}

	planner.exactSocketPath = server.socketPath
	plan, err = planner.build(context.Background(), store.registry.Clone())
	if err != nil {
		t.Fatalf("plan with socket: %v", err)
	}
	if project, _ := plan.registry.Project(gone); project.Status.Session.Live {
		t.Fatalf("planner with the verified socket did not lower %+v", project.Status.Session)
	}
}

// TestFullReconcilePassRecordsItsSocketPathLiveAndKeepsItOffline is the full
// pass writer: a live projection records the pass's own verified path, and a
// projection the pass computes not live keeps the path it had.
func TestFullReconcilePassRecordsItsSocketPathLiveAndKeepsItOffline(t *testing.T) {
	t.Parallel()

	_, store, server, _, root := newReconcileFixture(t, "-L", "primary")
	project, _ := store.registry.ProjectByRoot(root)
	uid := project.Metadata.UID
	reconciler := reconcileFixtureReconciler(root, "alpha")(server, inttmux.NewClient(server))
	const passSocket = "/tmp/fake-tmux/pass"
	reconciler.sessionSocketPath = func() string { return passSocket }

	if err := reconciler.reconcile(context.Background(), &store.registry, store.mutator(), "op-full-live"); err != nil {
		t.Fatalf("live pass: %v", err)
	}
	if got, want := storedSessionProjection(t, store, uid), (coremetadata.SessionProjection{Name: "alpha", Live: true, SocketPath: passSocket}); got != want {
		t.Fatalf("live full pass projection = %+v, want %+v", got, want)
	}

	server.sessions = slices.DeleteFunc(server.sessions, func(session *fakeTmuxSession) bool { return session.name == "alpha" })
	reconciler.sessionSocketPath = func() string { return "/tmp/fake-tmux/unrelated" }
	if err := reconciler.reconcile(context.Background(), &store.registry, store.mutator(), "op-full-offline"); err != nil {
		t.Fatalf("offline pass: %v", err)
	}
	if got, want := storedSessionProjection(t, store, uid), (coremetadata.SessionProjection{Name: "alpha", SocketPath: passSocket}); got != want {
		t.Fatalf("offline full pass projection = %+v, want %+v", got, want)
	}
}

// TestCreateRecordsTheVerifiedSocketPathOnLiveProjectProjections drives the
// production route bind: the create's own live write and its full passes
// record the physical socket the route verified.
func TestCreateRecordsTheVerifiedSocketPathOnLiveProjectProjections(t *testing.T) {
	t.Parallel()

	fixture := newCallBudgetFixture(t, 1)
	if stdout, stderr, err := runRoute(t, fixture.create, "window", "--project", "uid:prj-target", "--name", "probe", "-o", "uid"); err != nil {
		t.Fatalf("create window: %v (stderr %q)\n%s", err, stderr, stdout)
	}
	for _, uid := range []string{"prj-target", "prj-other1"} {
		project, _ := fixture.store.registry.Project(uid)
		want := coremetadata.SessionProjection{Name: strings.TrimPrefix(uid, "prj-"), Live: true, SocketPath: callBudgetSocket}
		if project.Status.Session == nil || *project.Status.Session != want {
			t.Fatalf("%s projection = %+v, want %+v", uid, project.Status.Session, want)
		}
	}
}

// TestTopologyMaterializationRecordsTheVerifiedSocketPath covers the
// `reconcile resources --materialize-project` live writer.
func TestTopologyMaterializationRecordsTheVerifiedSocketPath(t *testing.T) {
	command, store, server, _, _, _ := newTopologyMaterializeFixture(t)
	if result, stderr, err := runReconcile(t, command, "resources", "--socket", "topology", "--materialize-project", "uid:prj-beta", "-o", "json"); err != nil {
		t.Fatalf("materialize: %v (stderr %q)\n%s", err, stderr, result)
	}
	if got, want := storedSessionProjection(t, store, "prj-beta"), (coremetadata.SessionProjection{Name: "beta", Live: true, SocketPath: server.socketPath}); got != want {
		t.Fatalf("materialized projection = %+v, want %+v", got, want)
	}
}

// TestClosedProjectStartupRecordsTheVerifiedSocketPath covers the Project
// startup live writer.
func TestClosedProjectStartupRecordsTheVerifiedSocketPath(t *testing.T) {
	activation, store, server, root, _ := newProjectStartupTopologyFixture(t)
	if materialized, err := activation.MaterializeProjectTopology(context.Background(), projectTopologyMaterializeRequest{Root: root, SessionName: "beta"}); err != nil || !materialized {
		t.Fatalf("MaterializeProjectTopology() = %t, %v", materialized, err)
	}
	if got, want := storedSessionProjection(t, store, "prj-beta"), (coremetadata.SessionProjection{Name: "beta", Live: true, SocketPath: server.socketPath}); got != want {
		t.Fatalf("startup projection = %+v, want %+v", got, want)
	}
}
