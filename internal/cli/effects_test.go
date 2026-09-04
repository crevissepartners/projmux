package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type currentEffectOutcome struct {
	Scenario     string
	Route        string
	Identity     IdentityEffect
	Address      AddressEffect
	Topology     TopologyEffect
	DesiredState DesiredStateEffect
	Runtime      RuntimeEffect
	Focus        FocusEffect
	Cardinality  CardinalityEffect
	DomainEffect *DomainEffect
}

func currentSchemaV3Outcomes() []currentEffectOutcome {
	return []currentEffectOutcome{
		{"create-project-new", "create project", IdentityCreated, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeUnchanged, FocusUnchanged, CardinalityExactOne, nil},
		{"create-project-reuse", "create project", IdentityReused, AddressUnchanged, TopologyUnchanged, DesiredStateReused, RuntimeUnchanged, FocusUnchanged, CardinalityExactOne, nil},
		{"create-window-offline", "create window", IdentityCreated, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeMaterialized, FocusUnchanged, CardinalityExactOne, nil},
		{"create-window-parent-live", "create window", IdentityCreated, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeMaterialized, FocusUnchanged, CardinalityExactOne, nil},
		{"create-pane", "create pane", IdentityCreated, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeMaterialized, FocusUnchanged, CardinalityOneOrMore, nil},
		{"create-agent", "create agent", IdentityCreated, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeMaterialized, FocusUnchanged, CardinalityOneOrMore, nil},
		{"create-codex", "create codex", IdentityCreated, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeMaterialized, FocusUnchanged, CardinalityOneOrMore, nil},
		{"create-claude", "create claude", IdentityCreated, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeMaterialized, FocusUnchanged, CardinalityOneOrMore, nil},
		{"create-antigravity", "create antigravity", IdentityCreated, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeMaterialized, FocusUnchanged, CardinalityOneOrMore, nil},
		{"agent-resume-existing-agent", "agent resume", IdentityReused, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeMaterialized, FocusUnchanged, CardinalityExactOne, nil},
		{"agent-resume-new-pane", "agent resume", IdentityCreated, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeMaterialized, FocusUnchanged, CardinalityExactOne, nil},
		{"start-project-offline", "start project", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeMaterialized, FocusUnchanged, CardinalityExactOne, nil},
		{"start-project-live", "start project", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeAlreadyLive, FocusUnchanged, CardinalityExactOne, nil},
		{"open-project-offline", "open project", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeMaterialized, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"open-project-live", "open project", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeAlreadyLive, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"stop-project-detached", "stop project", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeStopped, FocusUnchanged, CardinalityExactOne, nil},
		{"stop-project-attached-fallback", "stop project", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeStopped, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"attach-project-offline", "attach project", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeMaterialized, FocusAttachedCaller, CardinalityExactOne, nil},
		{"attach-project-live", "attach project", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeAlreadyLive, FocusAttachedCaller, CardinalityExactOne, nil},
		{"focus-project", "focus project", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"focus-window", "focus window", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"focus-pane", "focus pane", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"switch-cancel", "switch", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityUnchanged, nil},
		{"switch-sidebar-cancel-restore", "switch", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeAlreadyLive, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"switch-pin", "switch", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityUnchanged, nil},
		{"switch-settings", "switch", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityUnchanged, nil},
		{"switch-registry-read-only", "switch", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityUnchanged, nil},
		{"switch-candidate", "switch", IdentityCreated, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeMaterialized, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"switch-existing-inside", "switch", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeAlreadyLive, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"switch-existing-outside", "switch", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeAlreadyLive, FocusAttachedCaller, CardinalityExactOne, nil},
		{"switch-registered-offline-continue", "switch", IdentityReused, AddressUnchanged, TopologyUnchanged, DesiredStateReused, RuntimeMaterialized, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"switch-recreate-replace", "switch", IdentityReplaced, AddressReleased, TopologyReplaced, DesiredStateReplaced, RuntimeMaterialized, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"switch-kill-detached", "switch", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeStopped, FocusUnchanged, CardinalityExactOne, nil},
		{"switch-kill-attached-fallback", "switch", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeStopped, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"rename-project", "rename project", IdentityUnchanged, AddressRenamed, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityExactOne, nil},
		{"rename-window", "rename window", IdentityUnchanged, AddressRenamed, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityExactOne, nil},
		{"rename-pane", "rename pane", IdentityUnchanged, AddressRenamed, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityExactOne, nil},
		{"rename-agent", "rename agent", IdentityUnchanged, AddressRenamed, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityExactOne, nil},
		{"rebind-project", "rebind project", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateReplaced, RuntimeUnchanged, FocusUnchanged, CardinalityExactOne, nil},
		{"unregister-project-live", "unregister project", IdentityRemoved, AddressReleased, TopologyRemoved, DesiredStateRemoved, RuntimePreserved, FocusUnchanged, CardinalityOneOrMore, nil},
		{"delete-project-alias-live", "delete project", IdentityRemoved, AddressReleased, TopologyRemoved, DesiredStateRemoved, RuntimePreserved, FocusUnchanged, CardinalityOneOrMore, nil},
		{"delete-window-live", "delete window", IdentityRemoved, AddressReleased, TopologyRemoved, DesiredStateRemoved, RuntimeStopped, FocusUnchanged, CardinalityOneOrMore, nil},
		{"delete-pane-live-active", "delete pane", IdentityRemoved, AddressReleased, TopologyRemoved, DesiredStateRemoved, RuntimeStopped, FocusMovedCurrentClient, CardinalityOneOrMore, nil},
		{"delete-agent-offline", "delete agent", IdentityRemoved, AddressReleased, TopologyRemoved, DesiredStateRemoved, RuntimeUnchanged, FocusUnchanged, CardinalityOneOrMore, nil},
		{"prune-project", "prune project", IdentityRemoved, AddressReleased, TopologyRemoved, DesiredStateRemoved, RuntimeUnchanged, FocusUnchanged, CardinalityZeroOrMore, nil},
		{"shell-control-bootstrap", "shell", IdentityCreated, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeMaterialized, FocusAttachedCaller, CardinalityOneOrMore, nil},
		{"shell-project-reuse-materialize", "shell", IdentityReused, AddressUnchanged, TopologyUnchanged, DesiredStateReused, RuntimeMaterialized, FocusAttachedCaller, CardinalityExactOne, nil},
		{"shell-existing-runtime", "shell", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeAlreadyLive, FocusAttachedCaller, CardinalityExactOne, nil},
		{"reconcile-resources-noop", "reconcile resources", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityZeroOrMore, nil},
		{"reconcile-resources-orphan-import", "reconcile resources", IdentityCreated, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeUnchanged, FocusUnchanged, CardinalityZeroOrMore, nil},
		{"reconcile-resources-authorship-promotion", "reconcile resources", IdentityCreated, AddressAllocated, TopologyReparented, DesiredStateReplaced, RuntimeUnchanged, FocusUnchanged, CardinalityZeroOrMore, nil},
		{"reconcile-resources-materialize-project", "reconcile resources", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeMaterialized, FocusUnchanged, CardinalityExactOne, nil},
		{"reconcile-registry-noop", "reconcile registry", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityZeroOrMore, nil},
		{"reconcile-registry-restore-add", "reconcile registry", IdentityCreated, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeUnchanged, FocusUnchanged, CardinalityZeroOrMore, nil},
		{"reconcile-registry-restore-remove", "reconcile registry", IdentityRemoved, AddressReleased, TopologyRemoved, DesiredStateRemoved, RuntimeUnchanged, FocusUnchanged, CardinalityZeroOrMore, nil},
		{"reconcile-registry-restore-rewrite", "reconcile registry", IdentityReplaced, AddressRenamed, TopologyReparented, DesiredStateReplaced, RuntimeUnchanged, FocusUnchanged, CardinalityZeroOrMore, nil},
		{"reconcile-registry-restore-topology-replace", "reconcile registry", IdentityReplaced, AddressUnchanged, TopologyReplaced, DesiredStateReplaced, RuntimeUnchanged, FocusUnchanged, CardinalityZeroOrMore, nil},
		{"internal-focus-client", "internal focus", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"internal-focus-notify-only", "internal focus", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityExactOne, nil},
		{"statusbar-click-window", "internal statusbar click", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"statusbar-click-notify-only", "internal statusbar click", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityExactOne, nil},
		{"statusbar-click-noop", "internal statusbar click", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityUnchanged, nil},
		{"agent-pane-launch-default-create", "internal agent-pane launch-default", IdentityCreated, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeMaterialized, FocusUnchanged, CardinalityExactOne, nil},
		{"agent-pane-launch-default-picker", "internal agent-pane launch-default", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityUnchanged, nil},
		{"agent-pane-picker-create", "internal agent-pane picker", IdentityCreated, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeMaterialized, FocusUnchanged, CardinalityExactOne, nil},
		{"agent-pane-picker-resume", "internal agent-pane picker", IdentityReused, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeMaterialized, FocusUnchanged, CardinalityExactOne, nil},
		{"agent-pane-picker-cancel", "internal agent-pane picker", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityUnchanged, nil},
		{"window-recent-switch", "window recent", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"window-recent-attach", "window recent", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusAttachedCaller, CardinalityExactOne, nil},
		{"window-recent-current", "window recent", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityExactOne, nil},
		{"window-recent-cancel", "window recent", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityUnchanged, nil},
		{"window-recent-runtime-attach-live", "window recent", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeAlreadyLive, FocusAttachedCaller, CardinalityExactOne, nil},
		{"window-recent-runtime-attach-after-race", "window recent", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeMaterialized, FocusAttachedCaller, CardinalityExactOne, nil},
		{"runtime-sessions-cancel", "runtime sessions", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityUnchanged, nil},
		{"runtime-sessions-state-overview", "runtime sessions", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityUnchanged, nil},
		{"runtime-sessions-open-switch", "runtime sessions", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeAlreadyLive, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"runtime-sessions-open-attach", "runtime sessions", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeAlreadyLive, FocusAttachedCaller, CardinalityExactOne, nil},
		{"runtime-sessions-kill-detached", "runtime sessions", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeStopped, FocusUnchanged, CardinalityExactOne, nil},
		{"runtime-sessions-kill-attached-fallback", "runtime sessions", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeStopped, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"runtime-sessions-diagnostics-rematerialize", "runtime sessions", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeMaterialized, FocusAttachedCaller, CardinalityExactOne, nil},
		{"session-popup-open-switch", "internal session-popup open", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeAlreadyLive, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"session-popup-open-attach", "internal session-popup open", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeAlreadyLive, FocusAttachedCaller, CardinalityExactOne, nil},
		{"runtime-diagnostics-cancel", "runtime diagnostics", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityUnchanged, nil},
		{"runtime-diagnostics-focus", "runtime diagnostics", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusMovedCurrentClient, CardinalityExactOne, nil},
		{"runtime-diagnostics-attach-live", "runtime diagnostics", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeAlreadyLive, FocusAttachedCaller, CardinalityExactOne, nil},
		{"runtime-diagnostics-attach-after-race", "runtime diagnostics", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeMaterialized, FocusAttachedCaller, CardinalityExactOne, nil},
		{"restore-snapshot-dry-run", "restore snapshot", IdentityUnchanged, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeUnchanged, FocusUnchanged, CardinalityUnchanged, nil},
		{"restore-snapshot-preserve", "restore snapshot", IdentityReused, AddressUnchanged, TopologyUnchanged, DesiredStateUnchanged, RuntimeMaterialized, FocusMovedCurrentClient, CardinalityOneOrMore, nil},
		{"restore-snapshot-create", "restore snapshot", IdentityCreated, AddressAllocated, TopologyEstablished, DesiredStateCreated, RuntimeMaterialized, FocusAttachedCaller, CardinalityOneOrMore, nil},
		{"restore-snapshot-remove", "restore snapshot", IdentityRemoved, AddressReleased, TopologyRemoved, DesiredStateRemoved, RuntimeMaterialized, FocusMovedCurrentClient, CardinalityOneOrMore, nil},
		{"restore-snapshot-replace", "restore snapshot", IdentityReplaced, AddressUnchanged, TopologyReplaced, DesiredStateReplaced, RuntimeMaterialized, FocusMovedCurrentClient, CardinalityOneOrMore, nil},
		{"restore-snapshot-post-commit-materialize-failure", "restore snapshot", IdentityReplaced, AddressUnchanged, TopologyReplaced, DesiredStateReplaced, RuntimeUnchanged, FocusUnchanged, CardinalityOneOrMore, nil},
	}
}

func TestRouteEffectManifestIsABijection(t *testing.T) {
	t.Parallel()
	if err := validateEffectBijection(Routes(), EffectManifest()); err != nil {
		t.Fatal(err)
	}
}

func TestRouteEffectManifestRejectsMissingDuplicateAndOrphanRows(t *testing.T) {
	t.Parallel()
	rows := EffectManifest()
	for _, test := range []struct {
		name string
		edit func([]RouteEffectRow) []RouteEffectRow
		want string
	}{
		{"missing", func(rows []RouteEffectRow) []RouteEffectRow { return rows[1:] }, "has no effect row"},
		{"duplicate", func(rows []RouteEffectRow) []RouteEffectRow { return append(rows, rows[0]) }, "duplicate effect row"},
		{"orphan", func(rows []RouteEffectRow) []RouteEffectRow {
			return append(rows, RouteEffectRow{Route: "send", Effects: cloneAllowedEffects(unchangedEffects(CardinalityUnchanged))})
		}, "orphan effect row"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateEffectBijection(Routes(), test.edit(slices.Clone(rows)))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

// validateEffectBijection proves a candidate flat projection contains exactly
// one row for every route and no invented route. Production rows are always a
// graph projection; this test helper makes missing, duplicate, and orphan
// failure modes directly exercisable without adding a second manifest.
func validateEffectBijection(nodes []Route, rows []RouteEffectRow) error {
	want := make(map[string]bool)
	walkInvocationGraph(nodes, nil, func(path []string, _ Route) {
		want[strings.Join(path, " ")] = true
	})
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		if !want[row.Route] {
			return fmt.Errorf("orphan effect row %q", row.Route)
		}
		if seen[row.Route] {
			return fmt.Errorf("duplicate effect row %q", row.Route)
		}
		if err := validateAllowedEffects(row.Route, &row.Effects); err != nil {
			return err
		}
		seen[row.Route] = true
	}
	for route := range want {
		if !seen[route] {
			return fmt.Errorf("route %q has no effect row", route)
		}
	}
	return nil
}

func TestRouteEffectEnumsRejectMissingUnknownAndDuplicateValues(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		edit func(*AllowedEffects)
		want string
	}{
		{"missing record", func(effects *AllowedEffects) {}, "no effect record"},
		{"missing axis", func(effects *AllowedEffects) { effects.Address = nil }, "missing value"},
		{"unknown enum", func(effects *AllowedEffects) { effects.Runtime = []RuntimeEffect{"teleported"} }, "unknown value"},
		{"duplicate enum", func(effects *AllowedEffects) { effects.Focus = []FocusEffect{FocusUnchanged, FocusUnchanged} }, "duplicate value"},
		{"unknown domain", func(effects *AllowedEffects) { effects.DomainEffect = &DomainEffect{Kind: "provider-write"} }, "unknown domain effect"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var effects *AllowedEffects
			if test.name != "missing record" {
				copy := cloneAllowedEffects(unchangedEffects(CardinalityUnchanged))
				effects = &copy
			}
			test.edit(effects)
			err := validateAllowedEffects("fixture", effects)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestCreateRenameAndDeleteKeepIdentityAddressAndTopologySeparate(t *testing.T) {
	t.Parallel()
	byRoute := effectRowsByRoute(t)
	for _, route := range []string{"rename project", "rename window", "rename pane", "rename agent"} {
		effects := byRoute[route]
		if !reflect.DeepEqual(effects.Identity, []IdentityEffect{IdentityUnchanged}) ||
			!reflect.DeepEqual(effects.Address, []AddressEffect{AddressRenamed}) ||
			!reflect.DeepEqual(effects.Topology, []TopologyEffect{TopologyUnchanged}) {
			t.Errorf("%s conflates rename axes: %+v", route, effects)
		}
	}
	for _, route := range []string{"delete project", "delete window", "delete pane", "delete agent"} {
		effects := byRoute[route]
		if !reflect.DeepEqual(effects.Identity, []IdentityEffect{IdentityRemoved}) ||
			!reflect.DeepEqual(effects.Address, []AddressEffect{AddressReleased}) ||
			!reflect.DeepEqual(effects.Topology, []TopologyEffect{TopologyRemoved}) {
			t.Errorf("%s conflates delete axes: %+v", route, effects)
		}
	}
	for _, route := range []string{"create window", "create pane", "create agent", "create codex", "create claude", "create antigravity"} {
		effects := byRoute[route]
		if !reflect.DeepEqual(effects.Identity, []IdentityEffect{IdentityCreated}) ||
			!reflect.DeepEqual(effects.Address, []AddressEffect{AddressAllocated}) ||
			!reflect.DeepEqual(effects.Topology, []TopologyEffect{TopologyEstablished}) {
			t.Errorf("%s conflates create axes: %+v", route, effects)
		}
	}
}

func TestPhaseZeroAddressVocabularyAndCreateFocusBoundary(t *testing.T) {
	t.Parallel()
	if want := []AddressEffect{AddressUnchanged, AddressAllocated, AddressRenamed, AddressReleased}; !reflect.DeepEqual(addressEffects, want) {
		t.Fatalf("address vocabulary = %v, want %v", addressEffects, want)
	}
	byRoute := effectRowsByRoute(t)
	for _, route := range []string{"create project", "create window", "create pane", "create agent", "create codex", "create claude", "create antigravity"} {
		if got := byRoute[route].Focus; !reflect.DeepEqual(got, []FocusEffect{FocusUnchanged}) {
			t.Errorf("%s focus effects = %v, want [unchanged]", route, got)
		}
	}
	for _, route := range []string{"create window", "create pane", "create agent", "create codex", "create claude", "create antigravity", "agent resume"} {
		if got := byRoute[route].Runtime; !reflect.DeepEqual(got, []RuntimeEffect{RuntimeMaterialized}) {
			t.Errorf("%s runtime effects = %v, want [materialized]; parent already-live is a precondition, not a child effect", route, got)
		}
	}
	if slices.Contains(byRoute["create agent"].Identity, IdentityReused) {
		t.Fatal("create agent permits reuse; create and resume must stay separate")
	}
	if !slices.Contains(byRoute["agent resume"].Identity, IdentityReused) {
		t.Fatal("agent resume lost the existing Agent identity outcome")
	}
}

func TestApparentlyMutatingDeferredAndPresentationRoutesStayOutsideSevenAxes(t *testing.T) {
	t.Parallel()
	// These handlers were audited because their names contain verbs such as
	// open, rename, select, or supervise. Their current writes are presentation,
	// cursor, MRU, or evidence state; any resource mutation is performed by a
	// later independently cataloged invocation.
	reasons := map[string]string{
		"window record":                       "writes only the recent-Window MRU store",
		"internal tmux popup-preview":         "opens a display-only popup",
		"internal tmux popup-switch":          "opens a popup whose later selection invokes switch",
		"internal tmux popup-sessions":        "opens a popup whose later selection invokes session-popup open",
		"internal tmux popup-toggle":          "opens or closes only the client popup surface",
		"internal tmux rebalance-panes":       "changes layout presentation without ownerRef or runtime membership changes",
		"internal tmux rename-pane":           "writes the v3 presentation label option, not Registry metadata.name",
		"internal preview cycle-pane":         "writes only the preview cursor",
		"internal preview cycle-window":       "writes only the preview cursor",
		"internal preview select":             "writes only the preview cursor",
		"internal session-popup preview":      "renders the stored cursor without opening a target",
		"internal session-popup cycle-pane":   "writes only the popup preview cursor",
		"internal session-popup cycle-window": "writes only the popup preview cursor",
		"internal supervise":                  "appends termination evidence; later controller convergence owns resource projection",
		"internal activation-exec":            "validates a committed activation read-only before provider exec",
	}
	byRoute := effectRowsByRoute(t)
	for route, reason := range reasons {
		effects, ok := byRoute[route]
		if !ok {
			t.Errorf("audited route %q is absent (%s)", route, reason)
			continue
		}
		if !reflect.DeepEqual(effects, *unchangedEffects(CardinalityUnchanged)) {
			t.Errorf("audited route %q no longer has its out-of-axis classification (%s): %+v", route, reason, effects)
		}
	}
}

func TestNonRouteCompletenessFixtures(t *testing.T) {
	t.Parallel()
	move := allowedEffects(
		[]IdentityEffect{IdentityUnchanged}, []AddressEffect{AddressUnchanged},
		[]TopologyEffect{TopologyReparented}, []DesiredStateEffect{DesiredStateUnchanged},
		[]RuntimeEffect{RuntimeReparented}, []FocusEffect{FocusUnchanged},
		[]CardinalityEffect{CardinalityExactOne},
	)
	if err := validateAllowedEffects("<future same-root move>", move); err != nil {
		t.Fatalf("future same-root move fixture: %v", err)
	}
	send := unchangedEffects(CardinalityExactOne)
	send.DomainEffect = &DomainEffect{Kind: DomainEffectAgentDelivery}
	if err := validateAllowedEffects("<future send>", send); err != nil {
		t.Fatalf("future send fixture: %v", err)
	}
	if got, want := effectProjection(send), []string{
		"identity=unchanged", "address=unchanged", "topology=unchanged", "desired-state=unchanged",
		"runtime=unchanged", "focus=unchanged", "cardinality=exact-one", "domain-effect=agent-delivery",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("future send effects = %v, want %v", got, want)
	}
	for _, row := range EffectManifest() {
		if row.Route == "send" || strings.HasPrefix(row.Route, "move ") || strings.HasPrefix(row.Route, "delivery ") {
			t.Errorf("non-route fixture leaked into executable catalog as %q", row.Route)
		}
	}
}

func TestRuntimeHelpAndGeneratedReferenceProjectTheSameEffects(t *testing.T) {
	t.Parallel()
	path, route, ok := Resolve([]string{"create", "project"})
	if !ok {
		t.Fatal("create project did not resolve")
	}
	var help bytes.Buffer
	if err := RenderRouteHelp(&help, path, route); err != nil {
		t.Fatal(err)
	}
	var reference bytes.Buffer
	if err := RenderReference(&reference); err != nil {
		t.Fatal(err)
	}
	wants := effectProjection(route.Effects)
	for _, want := range wants {
		if !strings.Contains(help.String(), "  "+want+"\n") {
			t.Errorf("runtime help lost %q", want)
		}
		if !strings.Contains(reference.String(), "- `"+want+"`\n") {
			t.Errorf("generated reference lost %q", want)
		}
	}
	if strings.Contains(help.String(), "rebound") || strings.Contains(reference.String(), "rebound") {
		t.Fatal("Phase 0 help introduced an address spelling outside allocated/renamed/released/unchanged")
	}
}

func TestCurrentSchemaV3HandlerOutcomesMatchAllowedEffectsAndGolden(t *testing.T) {
	t.Parallel()
	byRoute := effectRowsByRoute(t)
	var rendered strings.Builder
	rendered.WriteString("schema=v3\n")
	for _, outcome := range currentSchemaV3Outcomes() {
		effects, ok := byRoute[outcome.Route]
		if !ok {
			t.Fatalf("scenario %q refers to orphan route %q", outcome.Scenario, outcome.Route)
		}
		if !outcomeAllowed(outcome, effects) {
			t.Errorf("scenario %q outcome is not allowed by %q: outcome=%+v effects=%+v", outcome.Scenario, outcome.Route, outcome, effects)
		}
		rendered.WriteString(renderCurrentOutcome(outcome))
	}
	assertGolden(t, "current-handler-effects-schema-v3.golden", rendered.String())
}

func TestCurrentSchemaV3RouteEffectManifestMatchesGolden(t *testing.T) {
	t.Parallel()
	var rendered strings.Builder
	rendered.WriteString("schema=v3\n")
	for _, row := range EffectManifest() {
		rendered.WriteString("route=" + row.Route + " " + strings.Join(effectProjection(&row.Effects), " ") + "\n")
	}
	assertGolden(t, "route-effects-manifest-schema-v3.golden", rendered.String())
}

func TestCorrectedHandlerEffectsAreFixtureCovered(t *testing.T) {
	t.Parallel()
	byRoute := effectRowsByRoute(t)
	fixtures := make(map[string][]currentEffectOutcome)
	for _, outcome := range currentSchemaV3Outcomes() {
		fixtures[outcome.Route] = append(fixtures[outcome.Route], outcome)
	}
	for _, route := range []string{
		"create window", "create pane", "create agent", "create codex", "create claude", "create antigravity",
		"start project", "open project", "stop project", "unregister project", "delete project",
		"agent resume", "shell", "reconcile resources", "reconcile registry", "restore snapshot",
		"switch", "runtime sessions", "runtime diagnostics", "window recent", "internal statusbar click", "internal session-popup open",
		"internal agent-pane launch-default", "internal agent-pane picker", "internal focus",
	} {
		effects, ok := byRoute[route]
		if !ok {
			t.Fatalf("corrected handler route %q has no manifest row", route)
		}
		outcomes := fixtures[route]
		if len(outcomes) == 0 {
			t.Fatalf("corrected handler route %q has no schema-v3 fixture", route)
		}
		assertAllowedValuesCovered(t, route, "identity", effects.Identity, outcomes, func(outcome currentEffectOutcome) IdentityEffect { return outcome.Identity })
		assertAllowedValuesCovered(t, route, "address", effects.Address, outcomes, func(outcome currentEffectOutcome) AddressEffect { return outcome.Address })
		assertAllowedValuesCovered(t, route, "topology", effects.Topology, outcomes, func(outcome currentEffectOutcome) TopologyEffect { return outcome.Topology })
		assertAllowedValuesCovered(t, route, "desired-state", effects.DesiredState, outcomes, func(outcome currentEffectOutcome) DesiredStateEffect { return outcome.DesiredState })
		assertAllowedValuesCovered(t, route, "runtime", effects.Runtime, outcomes, func(outcome currentEffectOutcome) RuntimeEffect { return outcome.Runtime })
		assertAllowedValuesCovered(t, route, "focus", effects.Focus, outcomes, func(outcome currentEffectOutcome) FocusEffect { return outcome.Focus })
		assertAllowedValuesCovered(t, route, "cardinality", effects.Cardinality, outcomes, func(outcome currentEffectOutcome) CardinalityEffect { return outcome.Cardinality })
	}
}

func assertAllowedValuesCovered[T comparable](
	t *testing.T,
	route, axis string,
	allowed []T,
	outcomes []currentEffectOutcome,
	value func(currentEffectOutcome) T,
) {
	t.Helper()
	for _, want := range allowed {
		if !slices.ContainsFunc(outcomes, func(outcome currentEffectOutcome) bool { return value(outcome) == want }) {
			t.Errorf("route %q %s effect %v has no schema-v3 fixture", route, axis, want)
		}
	}
}

type handlerEffectAnchor struct {
	route, handlerFile, handlerSymbol, testFile, testSymbol string
}

func TestCorrectedHandlerEffectsKeepSourceAndTestAnchors(t *testing.T) {
	t.Parallel()
	anchors := []handlerEffectAnchor{
		{"create window", "create_resource.go", "func (c *createCommand) runResourceWindow", "create_agent_test.go", "TestExactProjectCreateWindowUsesAppRouteDespiteStaleInheritedPane"},
		{"create pane", "create_resource.go", "func (c *createCommand) runResourcePane", "window_anchor_consumers_test.go", "TestAnchorAwareCreatePaneAndAgentUseExactLiveShellAnchorDetached"},
		{"create agent", "create_agent.go", "func (c *createCommand) runResourceAgent", "create_agent_test.go", "TestCreateAgentAndProviderShortcutsShareScopedEqualization"},
		{"start project", "project_lifecycle_verbs.go", "func (c *projectLifecycleCommand) execute", "project_lifecycle_verbs_test.go", "TestStartProjectMaterializesDetachedAndReportsAlreadyLive"},
		{"open project", "project_lifecycle_verbs.go", "func (c *projectLifecycleCommand) materialize", "project_lifecycle_verbs_test.go", "TestOpenProjectMovesTheCurrentClientAndRefusesOutsideTmux"},
		{"stop project", "project_lifecycle_verbs.go", "func (c *projectLifecycleCommand) resolveProject", "project_lifecycle_verbs_test.go", "TestStopProjectEndsOnlyTheRuntimeAndRefusesAnOfflineTarget"},
		{"unregister project", "delete.go", "func (c *deleteCommand) runProjectUnregister", "delete_test.go", "TestUnregisterProjectAndItsDeprecatedDeleteAliasAreByteIdenticalOnStdout"},
		{"delete project", "delete.go", "func warnDeprecatedProjectDeleteAlias", "delete_test.go", "TestDeleteProjectIsTheOnlyRegistryUnregisterAndPreservesExternalAssets"},
		{"agent resume", "agent.go", "func (c *agentCommand) runResume", "agent_resume_test.go", "TestAgentResumeRebindsTheExistingAgentToANewManagedPane"},
		{"shell", "shell.go", "func (c *shellCommand) Run", "shell_control_session_test.go", "TestShellProvisionsAndConvergesTheControlSession"},
		{"switch", "switch.go", "func (c *switchCommand) execute", "switch_test.go", "TestSwitchCommandAllowsEmptySelection"},
		{"switch", "switch.go", "func (c *switchCommand) cancelSidebarPreview", "switch_test.go", "TestSwitchSidebarCancelRestoresOriginSession"},
		{"switch", "switch.go", "func (c *switchCommand) runTogglePin", "switch_test.go", "TestSwitchCommandTogglePinSnapsExplicitPathToCandidate"},
		{"switch", "switch.go", "func (c *switchCommand) runSettings", "switch_test.go", "TestSwitchCommandSettingsSubcommandRunsSettingsMenu"},
		{"switch", "switch_registry_rows.go", "func (c *switchCommand) openRegistryHierarchy", "registry_navigation_test.go", "TestRegistryNavigationHierarchyListsOneProjectSubtree"},
		{"switch", "switch.go", "func (c *switchCommand) runKill", "switch_test.go", "TestSwitchCommandPickerCtrlXSwitchesToPreviousActiveSessionBeforeKill"},
		{"switch", "project_startup.go", "func (c *switchCommand) openProjectSession", "switch_test.go", "TestAppRunSwitchDefaultsToPopupAndOpensSelectedSession"},
		{"switch", "project_startup.go", "func (c *switchCommand) prepareProjectContinue", "project_startup_mode_test.go", "TestSidebarOpenKeepsRegisteredRootOnContinue"},
		{"switch", "project_startup_fresh.go", "func (c *switchCommand) startProjectFresh", "project_startup_fresh_test.go", "TestProjectFreshStartPruneScope"},
		{"runtime sessions", "sessions.go", "func (c *sessionsCommand) Run", "sessions_test.go", "TestAppRunSessionsDefaultsToPopupAndOpensSelectedSession"},
		{"runtime sessions", "sessions.go", "func (c *sessionsCommand) Run", "sessions_test.go", "TestSessionsCommandAllowsEmptySelection"},
		{"runtime sessions", "sessions.go", "func (c *sessionsCommand) runSessionStateOverview", "sessions_test.go", "TestSessionsStateOverviewShowsReadModelWithoutImmediateMutation"},
		{"runtime sessions", "sessions.go", "func (c *sessionsCommand) killFocusedSession", "sessions_test.go", "TestSessionsCommandCtrlXSwitchesToFallbackBeforeKillingAttachedSession"},
		{"reconcile resources", "resource_reconcile.go", "func (c *resourceReconcileCommand) Run", "registry_topology_materialize_test.go", "TestRegistryTopologyMaterializationDryRunExecuteAndRepeatNoop"},
		{"reconcile registry", "registry_recovery.go", "func (c *registryRecoveryCommand) Run", "registry_recovery_test.go", "TestReconcileRegistryRestoresOnlyAnExplicitSourceAndRepeatsAsANoOp"},
		{"internal focus", "focus.go", "func (c *focusCommand) execute", "focus_test.go", "TestFocus_NoClientNotifyOnly"},
		{"internal statusbar click", "statusbar.go", "func (c *statusbarCommand) runClick", "statusbar_test.go", "TestStatusbarClickEmptyRangeWithMouseWindowSelectsWindow"},
		{"internal agent-pane launch-default", "ai.go", "func (c *aiCommand) runLaunchDefault", "agent_pane_intent_test.go", "TestSavedDefaultSplitStatesOneCanonicalIntent"},
		{"internal agent-pane picker", "ai.go", "func (c *aiCommand) runPicker", "create_intent_control_test.go", "TestResumePickerCreateCommitsExactSessionRefBeforeAnyHook"},
		{"window recent", "recent_window.go", "func (c *recentWindowCommand) openRecentWindow", "recent_window_test.go", "TestRecentWindowRunSwitchesCrossSessionWindowWithoutPaneRestore"},
		{"internal session-popup open", "session_popup.go", "func (c *sessionPopupCommand) runOpen", "session_popup_test.go", "TestAppRunSessionPopupOpen"},
		{"runtime diagnostics", "runtime_diagnostics_picker.go", "func (c *runtimeDiagnosticsCommand) runActions", "runtime_diagnostics_picker_test.go", "TestRuntimePickerFocusHandsTheExactCoordinateToTheExistingRoute"},
		{"restore snapshot", "session_state.go", "func (c *sessionStateCommand) commitSnapshotProjection", "session_state_projection_test.go", "TestRestoreSnapshotRecordsCountsAndFinalExplicitClientHandoff"},
	}
	byRoute := effectRowsByRoute(t)
	for _, anchor := range anchors {
		if _, ok := byRoute[anchor.route]; !ok {
			t.Errorf("anchored route %q is absent from the effect manifest", anchor.route)
		}
		assertFileContains(t, anchor.handlerFile, anchor.handlerSymbol)
		assertFileContains(t, anchor.testFile, anchor.testSymbol)
	}
}

func TestOpenSessionFocusEffectsKeepTmuxDependencyAndBehaviorAnchors(t *testing.T) {
	t.Parallel()
	for _, anchor := range []struct {
		route, sourceSymbol, insideTest, outsideTest string
	}{
		{"switch", "func (c *Client) OpenSession", "TestClientOpenSessionSwitchesInsideTmux", "TestClientOpenSessionAttachesOutsideTmux"},
		{"runtime sessions", "func (c *Client) OpenSessionTarget", "TestClientOpenSessionTargetSwitchesToPaneInsideTmux", "TestClientOpenSessionTargetAttachesToWindowOutsideTmux"},
		{"internal session-popup open", "func (c *Client) OpenSessionTarget", "TestClientOpenSessionTargetSwitchesToPaneInsideTmux", "TestClientOpenSessionTargetAttachesToWindowOutsideTmux"},
	} {
		t.Run(anchor.route, func(t *testing.T) {
			assertInternalFileContains(t, "integrations/tmux/client.go", anchor.sourceSymbol)
			assertInternalFileContains(t, "integrations/tmux/client_test.go", anchor.insideTest)
			assertInternalFileContains(t, "integrations/tmux/client_test.go", anchor.outsideTest)
		})
	}
}

func assertFileContains(t *testing.T, name, symbol string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "app", name))
	if err != nil {
		t.Fatalf("read handler anchor %s: %v", name, err)
	}
	if !bytes.Contains(content, []byte(symbol)) {
		t.Errorf("handler anchor %s no longer contains %q", name, symbol)
	}
}

func assertInternalFileContains(t *testing.T, name, symbol string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", name))
	if err != nil {
		t.Fatalf("read internal anchor %s: %v", name, err)
	}
	if !bytes.Contains(content, []byte(symbol)) {
		t.Errorf("internal anchor %s no longer contains %q", name, symbol)
	}
}

type mismatchLedgerRow struct {
	ID               string
	CurrentScenario  string
	ComparedRoute    string
	ComparedScenario string
	ComparedValue    string
	Axes             []string
}

// currentMismatchLedger records the differences that remain after the lifecycle
// Phase, not the ones it closed.
//
// Two Phase 0 rows are gone because the behavior they described is gone with
// them. `create-reuse-result-word` said a same-root repeat printed "created";
// it now prints `reused` and carries a receipt whose identity axis says the
// same. `switch-is-not-focus` said the shortcut claimed `focus project` as its
// canonical spelling while it could materialize and replace identity; it now
// composes `create project` and `open project`, which is what it always did.
//
// The remaining row is not a defect. `unregister project` preserving a runtime
// that `delete window` terminates is the deliberate asymmetry the two spellings
// exist to make readable, and recording it here is what keeps a later change
// from "fixing" it.
func currentMismatchLedger() []mismatchLedgerRow {
	return []mismatchLedgerRow{
		{"project-unregister-preserves-runtime", "unregister-project-live", "", "delete-window-live", "", []string{"runtime"}},
	}
}

func TestCurrentSchemaV3MismatchLedgerIsFixtureBackedAndGolden(t *testing.T) {
	t.Parallel()
	outcomes := make(map[string]currentEffectOutcome)
	for _, outcome := range currentSchemaV3Outcomes() {
		outcomes[outcome.Scenario] = outcome
	}
	byRoute := effectRowsByRoute(t)
	var rendered strings.Builder
	rendered.WriteString("schema=v3\n")
	for _, row := range currentMismatchLedger() {
		if row.CurrentScenario != "" {
			if _, ok := outcomes[row.CurrentScenario]; !ok {
				t.Fatalf("ledger %q has orphan current scenario %q", row.ID, row.CurrentScenario)
			}
		}
		if row.ComparedScenario != "" {
			if _, ok := outcomes[row.ComparedScenario]; !ok {
				t.Fatalf("ledger %q has orphan compared scenario %q", row.ID, row.ComparedScenario)
			}
		}
		if row.ComparedRoute != "" {
			if _, ok := byRoute[row.ComparedRoute]; !ok {
				t.Fatalf("ledger %q has orphan compared route %q", row.ID, row.ComparedRoute)
			}
		}
		if !ledgerAxesDiffer(row, outcomes, byRoute) {
			t.Errorf("ledger %q no longer differs on every declared axis %v", row.ID, row.Axes)
		}
		rendered.WriteString("id=" + row.ID + " current=" + valueOrNull(row.CurrentScenario) +
			" compared-route=" + valueOrNull(row.ComparedRoute) + " compared-scenario=" + valueOrNull(row.ComparedScenario) +
			" compared-value=" + valueOrNull(row.ComparedValue) + " axes=" + strings.Join(row.Axes, ",") + "\n")
	}
	assertGolden(t, "route-effect-mismatches-schema-v3.golden", rendered.String())
}

func effectRowsByRoute(t *testing.T) map[string]AllowedEffects {
	t.Helper()
	out := make(map[string]AllowedEffects)
	for _, row := range EffectManifest() {
		out[row.Route] = row.Effects
	}
	return out
}

func outcomeAllowed(outcome currentEffectOutcome, effects AllowedEffects) bool {
	if !slices.Contains(effects.Identity, outcome.Identity) || !slices.Contains(effects.Address, outcome.Address) ||
		!slices.Contains(effects.Topology, outcome.Topology) || !slices.Contains(effects.DesiredState, outcome.DesiredState) ||
		!slices.Contains(effects.Runtime, outcome.Runtime) || !slices.Contains(effects.Focus, outcome.Focus) ||
		!slices.Contains(effects.Cardinality, outcome.Cardinality) {
		return false
	}
	if outcome.DomainEffect == nil {
		return effects.DomainEffect == nil
	}
	return effects.DomainEffect != nil && effects.DomainEffect.Kind == outcome.DomainEffect.Kind
}

func renderCurrentOutcome(outcome currentEffectOutcome) string {
	domain := "null"
	if outcome.DomainEffect != nil {
		domain = string(outcome.DomainEffect.Kind)
	}
	return "scenario=" + outcome.Scenario + " route=" + outcome.Route +
		" identity=" + string(outcome.Identity) + " address=" + string(outcome.Address) +
		" topology=" + string(outcome.Topology) + " desired-state=" + string(outcome.DesiredState) +
		" runtime=" + string(outcome.Runtime) + " focus=" + string(outcome.Focus) +
		" cardinality=" + string(outcome.Cardinality) + " domain-effect=" + domain + "\n"
}

func ledgerAxesDiffer(row mismatchLedgerRow, outcomes map[string]currentEffectOutcome, routes map[string]AllowedEffects) bool {
	var current currentEffectOutcome
	if row.CurrentScenario != "" {
		current = outcomes[row.CurrentScenario]
	} else {
		effects := routes[row.ComparedRoute]
		current = currentEffectOutcome{
			Identity: effects.Identity[0], Address: effects.Address[0], Topology: effects.Topology[0],
			DesiredState: effects.DesiredState[0], Runtime: effects.Runtime[0], Focus: effects.Focus[0], Cardinality: effects.Cardinality[0],
		}
	}
	compared := outcomes[row.ComparedScenario]
	for _, axis := range row.Axes {
		switch axis {
		case "identity":
			if current.Identity == compared.Identity {
				return false
			}
		case "address":
			if current.Address == compared.Address {
				return false
			}
		case "topology":
			if current.Topology == compared.Topology {
				return false
			}
		case "desired-state":
			if current.DesiredState == compared.DesiredState {
				return false
			}
		case "runtime":
			if current.Runtime == compared.Runtime {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func valueOrNull(value string) string {
	if value == "" {
		return "null"
	}
	return value
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if got != string(want) {
		t.Fatalf("%s drifted:\n--- got ---\n%s--- want ---\n%s", name, got, want)
	}
}
