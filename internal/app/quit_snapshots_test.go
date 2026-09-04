package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	corelayout "github.com/crevissepartners/projmux/internal/core/layout"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func TestPlanQuitSnapshotBatchSelectsOnlyLiveManagedProjectsInStableOrder(t *testing.T) {
	t.Parallel()

	registry, projects, control := quitSnapshotRegistry(t, 3)
	base := resourcegraph.Inventory{
		Transport: resourcegraph.Transport{Kind: resourcegraph.TransportSocketPath, Value: "/tmp/app.sock"},
		HostMode:  resourcegraph.HostModeAppOwned,
		Sessions: []resourcegraph.Session{
			{ID: "$9", Name: "beta-live", ProjectUID: projects[1].Metadata.UID},
			{ID: "$2", Name: "alpha-live", ProjectUID: projects[0].Metadata.UID},
			{ID: "$3", Name: "home", Role: resourcegraph.ControlSessionRole},
			{ID: "$4", Name: "scratch", Ephemeral: true},
			{ID: "$5", Name: "plain"},
			{ID: "$6", Name: "orphan", ProjectUID: "project-not-in-this-registry"},
		},
	}

	var want []quitSnapshotTarget
	for _, session := range base.Sessions[:2] {
		want = append(want, quitSnapshotTarget{ProjectUID: session.ProjectUID, Session: session.Name, RuntimeID: session.ID})
	}
	slices.SortStableFunc(want, func(a, b quitSnapshotTarget) int {
		return strings.Compare(a.ProjectUID+"\x00"+a.Session+"\x00"+a.RuntimeID, b.ProjectUID+"\x00"+b.Session+"\x00"+b.RuntimeID)
	})

	orders := [][]resourcegraph.Session{
		slices.Clone(base.Sessions),
		slices.Clone(base.Sessions),
		slices.Clone(base.Sessions),
	}
	slices.Reverse(orders[1])
	orders[2] = append(orders[2][2:], orders[2][:2]...)
	for index, order := range orders {
		inventory := base.Clone()
		inventory.Sessions = order
		plan, err := planQuitSnapshotBatch(registry, inventory)
		if err != nil {
			t.Fatalf("order %d: planQuitSnapshotBatch() error = %v", index, err)
		}
		if !reflect.DeepEqual(plan.Targets, want) {
			t.Fatalf("order %d targets = %#v, want stable %#v", index, plan.Targets, want)
		}
		if got, expected := plan.Skipped, (quitSnapshotSkipped{Offline: 1, Control: 1, Ephemeral: 1, Unattributed: 1, Recoverable: 1}); got != expected {
			t.Fatalf("order %d skipped = %+v, want %+v (control uid %s)", index, got, expected, control.Metadata.UID)
		}
	}
}

func TestPlanQuitSnapshotBatchRefusesIncompleteAndConflictedObservation(t *testing.T) {
	t.Parallel()

	registry, projects, _ := quitSnapshotRegistry(t, 1)
	base := resourcegraph.Inventory{
		Transport: resourcegraph.Transport{Kind: resourcegraph.TransportSocketPath, Value: "/tmp/app.sock"},
		HostMode:  resourcegraph.HostModeAppOwned,
		Sessions:  []resourcegraph.Session{{ID: "$1", Name: "one", ProjectUID: projects[0].Metadata.UID}},
	}
	for _, tc := range []struct {
		name      string
		inventory resourcegraph.Inventory
		want      string
	}{
		{name: "unavailable", inventory: base.MarkUnavailable(resourcegraph.ScopeWindows, strings.Repeat("bounded ", 80)), want: "inventory scope windows unavailable"},
		{name: "duplicate Project claim", inventory: func() resourcegraph.Inventory {
			inventory := base.Clone()
			inventory.Sessions = append(inventory.Sessions, resourcegraph.Session{ID: "$2", Name: "other", ProjectUID: projects[0].Metadata.UID})
			return inventory
		}(), want: "managed Project identity conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := planQuitSnapshotBatch(registry, tc.inventory)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("planQuitSnapshotBatch() error = %v, want %q", err, tc.want)
			}
			if len(err.Error()) > 360 {
				t.Fatalf("preflight error is not bounded: %d bytes: %q", len(err.Error()), err)
			}
		})
	}
}

func TestPlanQuitSnapshotBatchSkipsMissingRootEvenWithStaleExactRuntimeRef(t *testing.T) {
	t.Parallel()

	registry, projects, _ := quitSnapshotRegistry(t, 1)
	registry.Projects[0].Status.Conditions = append(registry.Projects[0].Status.Conditions, coremetadata.Condition{
		Type:             coremetadata.ConditionMissingRoot,
		Status:           coremetadata.ConditionTrue,
		Reason:           "RootMissing",
		FirstObservedAt:  resourceFixtureClock,
		LastTransitionAt: resourceFixtureClock,
	})
	if err := registry.Validate(); err != nil {
		t.Fatalf("missing-root fixture Validate() error = %v", err)
	}
	inventory := resourcegraph.Inventory{
		Transport: resourcegraph.Transport{Kind: resourcegraph.TransportSocketPath, Value: "/tmp/app.sock"},
		HostMode:  resourcegraph.HostModeAppOwned,
		Sessions: []resourcegraph.Session{{
			ID: "$1", Name: "stale-live-session", ProjectUID: projects[0].Metadata.UID,
		}},
	}

	plan, err := planQuitSnapshotBatch(registry, inventory)
	if err != nil {
		t.Fatalf("planQuitSnapshotBatch() error = %v", err)
	}
	if len(plan.Targets) != 0 || plan.Skipped.Offline != 1 {
		t.Fatalf("plan = %+v, want missing-root Project skipped as offline despite stale exact Runtime ref", plan)
	}
}

func TestPlanQuitSnapshotBatchRefusesLiveProjectWithIncompleteRuntimeIdentity(t *testing.T) {
	t.Parallel()

	registry, projects, _ := quitSnapshotRegistry(t, 1)
	inventory := resourcegraph.Inventory{
		Transport: resourcegraph.Transport{Kind: resourcegraph.TransportSocketPath, Value: "/tmp/app.sock"},
		HostMode:  resourcegraph.HostModeAppOwned,
		Sessions: []resourcegraph.Session{{
			ID: "$1", Name: "", ProjectUID: projects[0].Metadata.UID,
		}},
	}

	_, err := planQuitSnapshotBatch(registry, inventory)
	if err == nil || !strings.Contains(err.Error(), "incomplete identity") {
		t.Fatalf("planQuitSnapshotBatch() error = %v, want incomplete live Runtime identity refusal", err)
	}
}

func TestQuitSaveBarrierContinuesAfterMiddleFailureAndDoesNotKill(t *testing.T) {
	t.Parallel()

	registry, projects, _ := quitSnapshotRegistry(t, 3)
	inventory := quitSnapshotLiveInventory(projects)
	runner := quitOwnedRunner("/tmp/projmux-save-failure.sock")
	store := sessionstate.NewStore(t.TempDir())
	var attempted []quitSnapshotTarget
	cmd := &quitCommand{runner: runner, snapshots: &quitSnapshotDependencies{
		loadRegistry: func() (coremetadata.Registry, error) { return registry, nil },
		observe:      func(context.Context, resourcegraph.Transport) resourcegraph.Inventory { return inventory },
		store:        func() (sessionstate.Store, error) { return store, nil },
		now:          func() time.Time { return time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC) },
		capture: func(_ context.Context, _ tmuxRunner, targetStore sessionstate.Store, _ coremetadata.Registry, target quitSnapshotTarget, at time.Time) (sessionstate.Snapshot, error) {
			attempted = append(attempted, target)
			if len(attempted) == 2 {
				return sessionstate.Snapshot{}, errors.New("injected capture failure")
			}
			snap := quitTestSnapshot(target.Session, at)
			return snap, targetStore.Save(snap)
		},
	}}
	var stdout bytes.Buffer
	err := cmd.saveProjectSnapshotsAndQuit(context.Background(), defaultAppSocket, &stdout)
	if err == nil || !strings.Contains(err.Error(), attempted[1].Session) || !strings.Contains(err.Error(), "saved=2 failed=1") {
		t.Fatalf("saveProjectSnapshotsAndQuit() error = %v, want exact middle failure ledger", err)
	}
	if len(attempted) != 3 {
		t.Fatalf("attempted = %#v, want all three targets after middle failure", attempted)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("tmux calls = %#v, want only initial route preflight and no kill", runner.calls)
	}
	for index, target := range attempted {
		if index == 1 {
			continue
		}
		loaded, loadErr := store.LoadReadOnly(target.Session)
		if loadErr != nil || loaded.Session != target.Session {
			t.Fatalf("successful atomic snapshot %q = %#v, %v", target.Session, loaded, loadErr)
		}
	}
	if _, loadErr := store.LoadReadOnly(attempted[1].Session); !errors.Is(loadErr, sessionstate.ErrNotFound) {
		t.Fatalf("failed target snapshot error = %v, want no partial file", loadErr)
	}
	if !strings.Contains(stdout.String(), "targets=3 saved=2 failed=1") {
		t.Fatalf("stdout = %q, want bounded batch counts", stdout.String())
	}
}

func TestQuitSaveBarrierHandlesZeroAndOneTarget(t *testing.T) {
	for _, projectCount := range []int{0, 1} {
		t.Run(fmt.Sprintf("targets-%d", projectCount), func(t *testing.T) {
			registry, projects, _ := quitSnapshotRegistry(t, projectCount)
			runner := quitOwnedRunner(fmt.Sprintf("/tmp/projmux-target-count-%d.sock", projectCount))
			stores := 0
			captures := 0
			cmd := &quitCommand{runner: runner, snapshots: &quitSnapshotDependencies{
				loadRegistry: func() (coremetadata.Registry, error) { return registry, nil },
				observe: func(context.Context, resourcegraph.Transport) resourcegraph.Inventory {
					return quitSnapshotLiveInventory(projects)
				},
				store: func() (sessionstate.Store, error) {
					stores++
					return sessionstate.NewStore(t.TempDir()), nil
				},
				capture: func(_ context.Context, _ tmuxRunner, _ sessionstate.Store, _ coremetadata.Registry, _ quitSnapshotTarget, _ time.Time) (sessionstate.Snapshot, error) {
					captures++
					return sessionstate.Snapshot{}, nil
				},
			}}
			if err := cmd.saveProjectSnapshotsAndQuit(context.Background(), defaultAppSocket, &bytes.Buffer{}); err != nil {
				t.Fatalf("saveProjectSnapshotsAndQuit() error = %v", err)
			}
			if captures != projectCount {
				t.Fatalf("captures = %d, want %d", captures, projectCount)
			}
			wantStores := 0
			if projectCount > 0 {
				wantStores = 1
			}
			if stores != wantStores {
				t.Fatalf("store opens = %d, want %d", stores, wantStores)
			}
		})
	}
}

func TestQuitSaveBarrierRetryRecapturesEveryTargetThenRunsOneGuardedShutdown(t *testing.T) {
	t.Parallel()

	registry, projects, _ := quitSnapshotRegistry(t, 2)
	inventory := quitSnapshotLiveInventory(projects)
	path := "/tmp/projmux-save-retry.sock"
	runner := quitOwnedRunner(path)
	store := sessionstate.NewStore(t.TempDir())
	first := true
	var attempted []quitSnapshotTarget
	clockCalls := 0
	cmd := &quitCommand{runner: runner, snapshots: &quitSnapshotDependencies{
		loadRegistry: func() (coremetadata.Registry, error) { return registry, nil },
		observe:      func(context.Context, resourcegraph.Transport) resourcegraph.Inventory { return inventory },
		store:        func() (sessionstate.Store, error) { return store, nil },
		now: func() time.Time {
			clockCalls++
			return time.Date(2026, 8, 27, 10+clockCalls, 0, 0, 0, time.UTC)
		},
		capture: func(_ context.Context, _ tmuxRunner, targetStore sessionstate.Store, _ coremetadata.Registry, target quitSnapshotTarget, at time.Time) (sessionstate.Snapshot, error) {
			attempted = append(attempted, target)
			if first && target == quitTargetsForInventory(t, registry, inventory)[1] {
				return sessionstate.Snapshot{}, errors.New("retryable failure")
			}
			snap := quitTestSnapshot(target.Session, at)
			return snap, targetStore.Save(snap)
		},
	}}

	if err := cmd.saveProjectSnapshotsAndQuit(context.Background(), defaultAppSocket, &bytes.Buffer{}); err == nil {
		t.Fatal("first saveProjectSnapshotsAndQuit() succeeded, want injected failure")
	}
	first = false
	if err := cmd.saveProjectSnapshotsAndQuit(context.Background(), defaultAppSocket, &bytes.Buffer{}); err != nil {
		t.Fatalf("retry saveProjectSnapshotsAndQuit() error = %v", err)
	}
	if len(attempted) != 4 {
		t.Fatalf("capture attempts = %d, want both targets recaptured on retry", len(attempted))
	}
	for _, target := range quitTargetsForInventory(t, registry, inventory) {
		loaded, err := store.LoadReadOnly(target.Session)
		if err != nil || loaded.SavedAt.Hour() != 12 {
			t.Fatalf("retry snapshot %q = %#v, %v; want second invocation timestamp", target.Session, loaded, err)
		}
	}
	kills := 0
	for _, call := range runner.calls {
		if slices.Contains(call.args, "kill-server") {
			kills++
		}
	}
	if kills != 1 {
		t.Fatalf("tmux calls = %#v, want exactly one guarded shutdown after retry", runner.calls)
	}
	last := runner.calls[len(runner.calls)-1]
	if !reflect.DeepEqual(last.args[:4], []string{"-S", path, "if-shell", "-F"}) || last.args[5] != "kill-server" {
		t.Fatalf("terminal call = %#v, want existing exact guarded shutdown argv", last)
	}
}

func TestQuitSaveAndQuitIntegrationCapturesRegistryGraphBeforeGuardedShutdown(t *testing.T) {
	t.Parallel()

	registry, projects, _ := quitSnapshotRegistry(t, 2)
	path := "/tmp/projmux-save-integration.sock"
	runner := &quitIntegrationRunner{path: path, projects: projects, registry: registry}
	store := sessionstate.NewStore(t.TempDir())
	at := time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)
	cmd := &quitCommand{runner: runner, snapshots: &quitSnapshotDependencies{
		loadRegistry: func() (coremetadata.Registry, error) { return registry, nil },
		store:        func() (sessionstate.Store, error) { return store, nil },
		now:          func() time.Time { return at },
		capture: func(ctx context.Context, exact tmuxRunner, targetStore sessionstate.Store, registry coremetadata.Registry, target quitSnapshotTarget, capturedAt time.Time) (sessionstate.Snapshot, error) {
			return newQuitSnapshotDependencies().capture(ctx, exact, targetStore, registry, target, capturedAt)
		},
	}}

	if err := cmd.saveProjectSnapshotsAndQuit(context.Background(), defaultAppSocket, &bytes.Buffer{}); err != nil {
		t.Fatalf("saveProjectSnapshotsAndQuit() error = %v", err)
	}
	for index := range projects {
		session := fmt.Sprintf("project-session-%d", index+1)
		snap, err := store.LoadReadOnly(session)
		if err != nil {
			t.Fatalf("LoadReadOnly(%q) error = %v", session, err)
		}
		if snap.SavedAt != at || len(snap.Windows) != 1 || len(snap.Windows[0].Panes) != 1 || snap.Windows[0].Panes[0].Recipe.Kind != sessionstate.RecipeKindShell {
			t.Fatalf("snapshot %q = %#v, want exact captured topology and shell recipe", session, snap)
		}
		if snap.Metadata == nil || snap.Metadata.UID != projects[index].Metadata.UID || snap.Metadata.RegistrySchemaVersion != coremetadata.SchemaVersion {
			t.Fatalf("snapshot %q Project metadata = %+v, want current Registry provenance", session, snap.Metadata)
		}
		window, _ := registry.Window(projects[index].Spec.PrimaryWindowRef)
		pane, _ := registry.Pane(window.Spec.AnchorPaneRef)
		if got := snap.Windows[0].Metadata; got == nil || got.UID != window.Metadata.UID || got.RegistrySchemaVersion != coremetadata.SchemaVersion {
			t.Fatalf("snapshot %q Window metadata = %+v, want %s at schema v%d", session, got, window.Metadata.UID, coremetadata.SchemaVersion)
		}
		if got := snap.Windows[0].Panes[0].Metadata; got == nil || got.UID != pane.Metadata.UID || got.RegistrySchemaVersion != coremetadata.SchemaVersion {
			t.Fatalf("snapshot %q Pane metadata = %+v, want %s at schema v%d", session, got, pane.Metadata.UID, coremetadata.SchemaVersion)
		}
	}
	kills := 0
	for _, call := range runner.calls {
		if slices.Contains(call.args, "kill-server") {
			kills++
		}
	}
	if kills != 1 {
		t.Fatalf("calls = %#v, want one terminal guarded shutdown", runner.calls)
	}
	last := runner.calls[len(runner.calls)-1]
	if !reflect.DeepEqual(last.args[:4], []string{"-S", path, "if-shell", "-F"}) || last.args[5] != "kill-server" {
		t.Fatalf("terminal call = %#v, want existing guarded shutdown argv", last)
	}
}

func TestQuitSaveBarrierKeepsNamedRegistryAndSiblingBytesInvariant(t *testing.T) {
	for _, failMiddle := range []bool{false, true} {
		name := "success"
		if failMiddle {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			registry, projects, _ := quitSnapshotRegistry(t, 2)
			registryBefore, err := json.Marshal(registry)
			if err != nil {
				t.Fatal(err)
			}
			namedStore := corelayout.NewStore(projects[0].Spec.Root)
			namedPath, err := namedStore.Path("named-sentinel")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(namedStore.Dir(), 0o755); err != nil {
				t.Fatal(err)
			}
			namedBefore := []byte("named snapshot bytes must stay exact\n")
			if err := os.WriteFile(namedPath, namedBefore, 0o644); err != nil {
				t.Fatal(err)
			}

			appPath := "/tmp/projmux-invariant-app.sock"
			siblingPath := "/tmp/projmux-invariant-sibling.sock"
			runner := &quitSiblingGuardRunner{
				app:          quitOwnedRunner(appPath),
				siblingPath:  siblingPath,
				siblingBytes: []byte("sibling topology bytes\n"),
			}
			siblingBefore := slices.Clone(runner.siblingBytes)
			latestStore := sessionstate.NewStore(t.TempDir())
			attempt := 0
			cmd := &quitCommand{runner: runner, snapshots: &quitSnapshotDependencies{
				loadRegistry: func() (coremetadata.Registry, error) { return registry, nil },
				observe: func(context.Context, resourcegraph.Transport) resourcegraph.Inventory {
					return quitSnapshotLiveInventory(projects)
				},
				store: func() (sessionstate.Store, error) { return latestStore, nil },
				now:   func() time.Time { return time.Date(2026, 8, 27, 15, 0, 0, 0, time.UTC) },
				capture: func(_ context.Context, _ tmuxRunner, store sessionstate.Store, _ coremetadata.Registry, target quitSnapshotTarget, at time.Time) (sessionstate.Snapshot, error) {
					attempt++
					if failMiddle && attempt == 2 {
						return sessionstate.Snapshot{}, errors.New("sentinel failure")
					}
					snap := quitTestSnapshot(target.Session, at)
					return snap, store.Save(snap)
				},
			}}
			runErr := cmd.saveProjectSnapshotsAndQuit(context.Background(), defaultAppSocket, &bytes.Buffer{})
			if failMiddle && runErr == nil {
				t.Fatal("failure path error = nil")
			}
			if !failMiddle && runErr != nil {
				t.Fatalf("success path error = %v", runErr)
			}

			registryAfter, err := json.Marshal(registry)
			if err != nil || !bytes.Equal(registryAfter, registryBefore) {
				t.Fatalf("Registry bytes changed\nbefore=%s\nafter=%s\nerr=%v", registryBefore, registryAfter, err)
			}
			namedAfter, err := os.ReadFile(namedPath)
			if err != nil || !bytes.Equal(namedAfter, namedBefore) {
				t.Fatalf("named snapshot bytes changed: after=%q err=%v", namedAfter, err)
			}
			if runner.siblingCalls != 0 || !bytes.Equal(runner.siblingBytes, siblingBefore) {
				t.Fatalf("sibling server changed: calls=%d before=%q after=%q", runner.siblingCalls, siblingBefore, runner.siblingBytes)
			}
		})
	}
}

func TestQuitSaveBarrierMarkerRouteAndSocketDriftNeverKill(t *testing.T) {
	for _, drift := range []string{"marker", "route", "socket"} {
		t.Run(drift, func(t *testing.T) {
			registry, projects, _ := quitSnapshotRegistry(t, 1)
			runner := &quitRouteDriftRunner{drift: drift, initialPath: "/tmp/projmux-drift-initial.sock", driftedPath: "/tmp/projmux-drift-next.sock"}
			captures := 0
			cmd := &quitCommand{runner: runner, snapshots: &quitSnapshotDependencies{
				loadRegistry: func() (coremetadata.Registry, error) { return registry, nil },
				observe: func(context.Context, resourcegraph.Transport) resourcegraph.Inventory {
					return quitSnapshotLiveInventory(projects)
				},
				store: func() (sessionstate.Store, error) { return sessionstate.NewStore(t.TempDir()), nil },
				capture: func(_ context.Context, _ tmuxRunner, _ sessionstate.Store, _ coremetadata.Registry, _ quitSnapshotTarget, _ time.Time) (sessionstate.Snapshot, error) {
					captures++
					return sessionstate.Snapshot{}, nil
				},
			}}
			err := cmd.saveProjectSnapshotsAndQuit(context.Background(), defaultAppSocket, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), map[string]string{"marker": "ownership marker", "route": "logical route marker", "socket": "physical socket generation"}[drift]) {
				t.Fatalf("saveProjectSnapshotsAndQuit() error = %v, want %s drift refusal", err, drift)
			}
			if captures != 1 || runner.kills != 0 {
				t.Fatalf("captures=%d kills=%d, want completed capture and fail-closed no-kill", captures, runner.kills)
			}
		})
	}
}

func TestQuitUnsavedActionsAndFlagsAreSnapshotFreeAndByteEquivalent(t *testing.T) {
	t.Parallel()

	run := func(t *testing.T, args []string, interactive bool) []recordedTmuxCall {
		t.Helper()
		runner := quitOwnedRunner("/tmp/projmux-unsaved.sock")
		calls := 0
		cmd := &quitCommand{
			runner: runner,
			snapshots: &quitSnapshotDependencies{loadRegistry: func() (coremetadata.Registry, error) {
				calls++
				return coremetadata.Registry{}, errors.New("must not be called")
			}},
		}
		if interactive {
			cmd.nativePicker = nativePickerFromCompatRunner(switchRunnerFunc(func(intpickercompat.Options) (intpickercompat.Result, error) {
				return intpickercompat.Result{Key: "enter", Value: quitActionQuit}, nil
			}))
		}
		if err := cmd.Run(args, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("Run(%v) error = %v", args, err)
		}
		if calls != 0 {
			t.Fatalf("snapshot inventory calls = %d, want zero", calls)
		}
		return runner.calls
	}

	yesCalls := run(t, []string{"--yes"}, false)
	forceCalls := run(t, []string{"--force"}, false)
	interactiveCalls := run(t, nil, true)
	if !reflect.DeepEqual(forceCalls, yesCalls) || !reflect.DeepEqual(interactiveCalls, yesCalls) {
		t.Fatalf("unsaved shutdown argv drifted\nyes=%#v\nforce=%#v\ninteractive=%#v", yesCalls, forceCalls, interactiveCalls)
	}
}

func TestQuitSavePreflightUnavailableOrConflictPerformsZeroCaptureAndKill(t *testing.T) {
	t.Parallel()

	registry, projects, _ := quitSnapshotRegistry(t, 1)
	base := quitSnapshotLiveInventory(projects)
	conflict := base.Clone()
	conflict.Sessions = append(conflict.Sessions, resourcegraph.Session{ID: "$99", Name: "duplicate", ProjectUID: projects[0].Metadata.UID})
	for _, tc := range []struct {
		name      string
		inventory resourcegraph.Inventory
	}{
		{name: "unavailable", inventory: base.MarkUnavailable(resourcegraph.ScopePanes, "pane inventory failed")},
		{name: "conflict", inventory: conflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := quitOwnedRunner("/tmp/projmux-preflight.sock")
			captures := 0
			stores := 0
			cmd := &quitCommand{runner: runner, snapshots: &quitSnapshotDependencies{
				loadRegistry: func() (coremetadata.Registry, error) { return registry, nil },
				observe:      func(context.Context, resourcegraph.Transport) resourcegraph.Inventory { return tc.inventory },
				store: func() (sessionstate.Store, error) {
					stores++
					return sessionstate.NewStore(t.TempDir()), nil
				},
				capture: func(context.Context, tmuxRunner, sessionstate.Store, coremetadata.Registry, quitSnapshotTarget, time.Time) (sessionstate.Snapshot, error) {
					captures++
					return sessionstate.Snapshot{}, nil
				},
			}}
			if err := cmd.saveProjectSnapshotsAndQuit(context.Background(), defaultAppSocket, &bytes.Buffer{}); err == nil {
				t.Fatal("saveProjectSnapshotsAndQuit() error = nil, want preflight refusal")
			}
			if stores != 0 || captures != 0 || len(runner.calls) != 3 {
				t.Fatalf("stores=%d captures=%d tmux=%#v, want preflight reads only", stores, captures, runner.calls)
			}
		})
	}
}

func quitSnapshotRegistry(t *testing.T, projectCount int) (coremetadata.Registry, []coremetadata.Project, coremetadata.ControlSession) {
	t.Helper()
	registry := coremetadata.NewRegistry()
	mutator := reconcileFixtureMutator()
	projects := make([]coremetadata.Project, 0, projectCount)
	for index := range projectCount {
		root := filepath.Join(t.TempDir(), fmt.Sprintf("project-%d", index))
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		project := registerFixtureProject(t, &registry, mutator, root)
		window, ok := registry.Window(project.Spec.PrimaryWindowRef)
		if !ok {
			t.Fatalf("fixture primary Window %q missing", project.Spec.PrimaryWindowRef)
		}
		window.Status.RuntimeSessionID = fmt.Sprintf("$%d", index+1)
		window.Status.RuntimeID = fmt.Sprintf("@%d", index+1)
		pane, ok := registry.Pane(window.Spec.AnchorPaneRef)
		if !ok {
			t.Fatalf("fixture anchor Pane %q missing", window.Spec.AnchorPaneRef)
		}
		pane.Status.Activation.Generation = fmt.Sprintf("quit-snapshot-generation-%d", index+1)
		pane.Status.Activation.RuntimeID = fmt.Sprintf("%%%d", index+1)
		projects = append(projects, project)
	}
	binding, err := mutator.BindControlSession(&registry, coremetadata.ControlSessionObservation{Session: "home"}, "/bin/zsh", "op-control", nil)
	if err != nil {
		t.Fatalf("BindControlSession() error = %v", err)
	}
	if err := registry.Validate(); err != nil {
		t.Fatalf("fixture Registry Validate() error = %v", err)
	}
	return registry, projects, binding.ControlSession
}

func quitSnapshotLiveInventory(projects []coremetadata.Project) resourcegraph.Inventory {
	inventory := resourcegraph.Inventory{
		Transport: resourcegraph.Transport{Kind: resourcegraph.TransportSocketPath, Value: "/tmp/app.sock"},
		HostMode:  resourcegraph.HostModeAppOwned,
	}
	for index, project := range projects {
		inventory.Sessions = append(inventory.Sessions, resourcegraph.Session{
			ID: "$" + fmt.Sprint(index+1), Name: "session-" + fmt.Sprint(index+1), ProjectUID: project.Metadata.UID,
		})
	}
	return inventory
}

func quitTargetsForInventory(t *testing.T, registry coremetadata.Registry, inventory resourcegraph.Inventory) []quitSnapshotTarget {
	t.Helper()
	plan, err := planQuitSnapshotBatch(registry, inventory)
	if err != nil {
		t.Fatalf("planQuitSnapshotBatch() error = %v", err)
	}
	return plan.Targets
}

func quitOwnedRunner(path string) *recordingTmuxRunner {
	return &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "display-message", "-p", "-F", "#{socket_path}"):         path + "\n",
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", "@projmux_app"):                  "1\n",
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", runtimeMutationSocketNameOption): defaultAppSocket + "\n",
	}}
}

func quitTestSnapshot(session string, at time.Time) sessionstate.Snapshot {
	return sessionstate.Snapshot{
		Version: sessionstate.Version, Session: session, DefaultCWD: "/tmp", SavedAt: at,
		Windows: []sessionstate.Window{{
			Index: 0, Name: "shell", Layout: "layout", ActivePaneIndex: 0,
			Panes: []sessionstate.Pane{{Index: 0, CWD: "/tmp", Recipe: sessionstate.ShellRecipe()}},
		}},
	}
}

type quitIntegrationRunner struct {
	path     string
	projects []coremetadata.Project
	registry coremetadata.Registry
	calls    []recordedTmuxCall
}

type quitSiblingGuardRunner struct {
	app          *recordingTmuxRunner
	siblingPath  string
	siblingBytes []byte
	siblingCalls int
}

type quitRouteDriftRunner struct {
	drift       string
	initialPath string
	driftedPath string
	pathReads   int
	markerReads int
	routeReads  int
	kills       int
}

func (r *quitRouteDriftRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "display-message -p -F #{socket_path}"):
		r.pathReads++
		if r.drift == "socket" && r.pathReads > 1 {
			return []byte(r.driftedPath + "\n"), nil
		}
		return []byte(r.initialPath + "\n"), nil
	case strings.Contains(joined, "show-options -gqv @projmux_app"):
		r.markerReads++
		if r.drift == "marker" && r.markerReads > 1 {
			return []byte("0\n"), nil
		}
		return []byte("1\n"), nil
	case strings.Contains(joined, "show-options -gqv "+runtimeMutationSocketNameOption):
		r.routeReads++
		if r.drift == "route" && r.routeReads > 1 {
			return []byte("other\n"), nil
		}
		return []byte(defaultAppSocket + "\n"), nil
	case slices.Contains(args, "kill-server"):
		r.kills++
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected drift runner call: %v", args)
	}
}

func (r *quitSiblingGuardRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if len(args) >= 2 && args[0] == "-S" && args[1] == r.siblingPath {
		r.siblingCalls++
		if slices.Contains(args, "kill-server") {
			r.siblingBytes = nil
		}
		return nil, errors.New("test refused sibling route")
	}
	return r.app.Run(ctx, name, args...)
}

func (r *quitIntegrationRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recordedTmuxCall{name: name, args: slices.Clone(args)})
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "display-message -p -F #{socket_path}"):
		return []byte(r.path + "\n"), nil
	case strings.Contains(joined, "show-options -gqv @projmux_app"), strings.Contains(joined, "show-options -gv @projmux_app"):
		return []byte("1\n"), nil
	case strings.Contains(joined, "show-options -gqv "+runtimeMutationSocketNameOption):
		return []byte(defaultAppSocket + "\n"), nil
	case strings.Contains(joined, "list-sessions -F"):
		var out strings.Builder
		for index, project := range r.projects {
			fmt.Fprintf(&out, "$%d\x1fproject-session-%d\x1f%s\x1f%s\x1f%s\x1f\x1f\n", index+1, index+1, project.Metadata.UID, project.Metadata.Name, project.Spec.Root)
		}
		return []byte(out.String()), nil
	case strings.Contains(joined, "list-windows -a -F"), strings.Contains(joined, "list-panes -a -F"):
		return nil, nil
	case strings.Contains(joined, "list-panes -s -t") && strings.Contains(joined, "#{pane_id}") && !strings.Contains(joined, "#{window_index}"):
		return []byte("\n"), nil
	case strings.Contains(joined, "list-windows -t"):
		project, window, _, ok := r.projectSnapshotResources(args)
		if !ok {
			return nil, fmt.Errorf("unknown snapshot target: %v", args)
		}
		return fmt.Appendf(nil, "0\x1fshell\x1flayout\x1f%s\x1f%s\n", window.Status.RuntimeID, project.Spec.PrimaryWindowRef), nil
	case strings.Contains(joined, "list-panes -s -t"):
		_, _, pane, ok := r.projectSnapshotResources(args)
		if !ok {
			return nil, fmt.Errorf("unknown snapshot target: %v", args)
		}
		return fmt.Appendf(nil, "0\x1f0\x1fshell\x1fprimary\x1f1\x1f/tmp\x1f\x1f\x1f\x1f\x1f\x1f\x1f\x1f\x1f\x1f%s\x1f%s\n", pane.Status.Activation.RuntimeID, pane.Metadata.UID), nil
	case strings.Contains(joined, "display-message -p -t"):
		return []byte("manual\n"), nil
	case strings.Contains(joined, "if-shell -F") && strings.Contains(joined, "kill-server"):
		return nil, nil
	default:
		return nil, fmt.Errorf("unexpected tmux call: %s %v", name, args)
	}
}

func (r *quitIntegrationRunner) projectSnapshotResources(args []string) (coremetadata.Project, coremetadata.Window, coremetadata.Pane, bool) {
	target := flagValue(args, "-t")
	for index, project := range r.projects {
		if target != fmt.Sprintf("$%d", index+1) {
			continue
		}
		window, windowOK := r.registry.Window(project.Spec.PrimaryWindowRef)
		if !windowOK {
			return coremetadata.Project{}, coremetadata.Window{}, coremetadata.Pane{}, false
		}
		pane, paneOK := r.registry.Pane(window.Spec.AnchorPaneRef)
		return project, *window, *pane, paneOK
	}
	return coremetadata.Project{}, coremetadata.Window{}, coremetadata.Pane{}, false
}
