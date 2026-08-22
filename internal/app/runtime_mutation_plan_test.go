package app

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

func closedRuntimeMutationVerbs() []runtimeMutationVerb {
	verbs := make([]runtimeMutationVerb, 0, len(runtimeMutationInventory))
	for verb := range runtimeMutationInventory {
		verbs = append(verbs, verb)
	}
	slices.Sort(verbs)
	return verbs
}

func printableRuntimeMutationInventory() runtimeMutationPlan {
	verbs := closedRuntimeMutationVerbs()
	actions := make([]plannedRuntimeMutation, 0, len(verbs))
	for i, verb := range verbs {
		target := runtimeMutationTarget{
			Socket: "-L=phase10-inventory", PhysicalSocket: "/tmp/phase10-inventory",
			Kind: "session", ID: "$1", Parent: "$1/@1/root",
			UID: "uid:" + string(verb),
		}
		action := newRuntimeMutation(i+1, verb, target)
		switch verb {
		case mutationCreateSession:
			action.Target.PhysicalSocket, action.Target.Kind, action.Target.ID = runtimeMutationSocketAbsentBeforeCreate, "project-declaration", "inventory"
			action.Operands = []string{"-d", "-s", "inventory"}
		case mutationBootstrapControlSession:
			action.Target.PhysicalSocket, action.Target.Kind, action.Target.ID = runtimeMutationSocketAbsentBeforeCreate, "control-session-declaration", "declaration:-L=phase10-inventory/session=home"
			action.Operands = []string{"-L", "phase10-inventory", "-f", "/tmp/config", "-d", "-s", "home"}
		case mutationWriteRouteMarker:
			action.Target.Kind, action.Target.ID, action.Target.UID = "app-server", "socket:/tmp/phase10-inventory", "logical:phase10-inventory"
			action.Operands = []string{"-L", "phase10-inventory", "-gq", runtimeMutationSocketNameOption, "phase10-inventory"}
		case mutationCreateWindow:
			action.Operands = []string{"-d", "-t", "$1:"}
		case mutationCreatePane:
			action.Target.Kind, action.Target.ID = "pane", "%1"
			action.Operands = []string{"-d", "-t", "%1"}
		case mutationWriteLayout:
			action.Target.Kind, action.Target.ID = "pane", "%1"
			action.Operands = []string{"-t", "%1", "-x", "40"}
		case mutationKillPane, mutationTombstonePane, mutationRestorePane, mutationWriteIdentity:
			action.Target.Kind, action.Target.ID = "pane", "%1"
			action.Operands = []string{"-t", "%1"}
			if verb == mutationTombstonePane || verb == mutationRestorePane || verb == mutationWriteIdentity {
				action.Operands = append(action.Operands, tmuxopts.PaneUID, action.Target.UID)
			}
		case mutationKillWindow, mutationRenameWindow:
			action.Target.Kind, action.Target.ID = "window", "@1"
			action.Operands = []string{"-t", "@1"}
			if verb == mutationRenameWindow {
				action.Operands = append(action.Operands, "inventory")
			}
		case mutationQueuePaneKill:
			action.Target.Kind, action.Target.ID, action.Target.UID, action.Target.Parent = "pane", "%1", "deleted:pan-1", "$1/@1/root"
			action.Queue = &runtimeMutationQueuedKill{PhysicalSocket: action.Target.PhysicalSocket, LogicalSocket: "phase10-inventory", ExpectedUID: action.Target.UID, SessionID: "$1", WindowID: "@1"}
			action.Queue.Marker = runtimeMutationQueueMarker(action)
		case mutationQueueWindowKill:
			action.Target.Kind, action.Target.ID, action.Target.UID, action.Target.Parent = "window", "@1", "deleted:win-1", "$1/root"
			action.Queue = &runtimeMutationQueuedKill{PhysicalSocket: action.Target.PhysicalSocket, LogicalSocket: "phase10-inventory", ExpectedUID: action.Target.UID, SessionID: "$1"}
			action.Queue.Marker = runtimeMutationQueueMarker(action)
		case mutationFinalizeSession:
			action.Operands = []string{"-t", "$1", finalizeOperationEnvironment, "op"}
		case mutationWriteLease:
			action.Operands = []string{"-t", "$1", createOperationEnvironment, "op"}
		case mutationClearLease:
			action.Operands = []string{"-u", "-t", "$1", createOperationEnvironment}
		case mutationWriteProjectAnchor:
			action.Operands = []string{"-t", "$1", inttmux.ProjectPathSessionOption, "/work"}
		case mutationStopManagedSession, mutationStopUnmanagedSession, mutationKillOwned:
			action.Operands = []string{"-t", "$1"}
		}
		bindRuntimeMutationGuard(&action, "inventory")
		actions = append(actions, action)
	}
	return newRuntimeMutationPlan(actions...)
}

func TestPlanOnlyMutationInventoryIsClosedAndPrintable(t *testing.T) {
	want := []runtimeMutationVerb{
		mutationBootstrapControlSession,
		mutationClearLease,
		mutationConvergeControlIdentity,
		mutationCreatePane,
		mutationCreateSession,
		mutationCreateWindow,
		mutationFinalizeSession,
		mutationKillOwned,
		mutationKillPane,
		mutationKillWindow,
		mutationQueuePaneKill,
		mutationQueueWindowKill,
		mutationRenameWindow,
		mutationRestorePane,
		mutationTombstonePane,
		mutationStopManagedSession,
		mutationStopUnmanagedSession,
		mutationWriteIdentity,
		mutationWriteLayout,
		mutationWriteLease,
		mutationWriteProjectAnchor,
		mutationWriteRouteMarker,
	}
	slices.Sort(want)
	if got := closedRuntimeMutationVerbs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime mutation vocabulary = %v, want closed inventory %v", got, want)
	}
	plan := printableRuntimeMutationInventory()
	first, err := plan.printableBytes()
	if err != nil {
		t.Fatalf("print inventory: %v", err)
	}
	second, err := newRuntimeMutationPlan(slices.Clone(plan.Actions)...).printableBytes()
	if err != nil {
		t.Fatalf("repeat print inventory: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("inventory bytes changed:\n%s\n---\n%s", first, second)
	}
	for _, action := range plan.Actions {
		if action.Verb == "" || action.Target.Socket == "" || action.Target.PhysicalSocket == "" || action.Target.Kind == "" ||
			(action.Target.ID == "" && action.Target.UID == "") || action.Guard.Kind == "" || action.Guard.Expect == "" || action.Order == 0 || action.Effect == "" {
			t.Fatalf("incomplete printable inventory row: %#v", action)
		}
		if !strings.Contains(action.Guard.Expect, action.Target.UID) {
			t.Fatalf("guard is not bound to stable target uid: %#v", action)
		}
	}
}

func TestPlanOnlyMutationProductSurfaceInventoryIsBidirectionalAndClosed(t *testing.T) {
	seen := map[string]bool{}
	for _, row := range runtimeMutationSurfaces {
		if row.ID == "" || row.Producer == "" || row.Handler == "" || row.SemanticClass == "" ||
			row.RootKinds == "" || row.OwnerRoute == "" || row.PlanVerb == "" || row.Guard == "" || row.Effect == "" {
			t.Fatalf("incomplete product mutation surface: %#v", row)
		}
		if seen[row.ID] {
			t.Fatalf("duplicate product mutation surface %q", row.ID)
		}
		seen[row.ID] = true
		if row.Disposition != runtimeMutationSurfacePlanned && row.Disposition != runtimeMutationSurfaceExempt {
			t.Fatalf("surface %q has open disposition %q", row.ID, row.Disposition)
		}
	}
	for _, required := range []string{
		"project.materialize", "control.bootstrap", "controller.identity", "pane.canonical-delete", "window.canonical-delete",
		"public.create-window", "public.create-pane", "project.delete-cascade-pane", "project.delete-cascade-window", "layout.auto-even-split",
		"startup.shell-project", "startup.sidebar-project", "startup.current-project", "startup.attach-project", "startup.session-picker-project",
		"pane-menu.split-right", "pane-menu.split-down", "pane-menu.kill", "pane-menu.resume", "pane-menu.swap-up",
		"pane-menu.swap-down", "pane-menu.mark", "pane-menu.zoom", "pane-menu.mouse-forward", "shell.foreground-attach",
		"app.quit", "attach.ensure-home", "attach.ephemeral-prune", "attach.ephemeral-create", "standalone.prune", "manual.tagged-kill", "switch.manual-kill", "sidebar.unmanaged-candidate-stop", "replay.retired-snapshot",
		"config.apply-source", "trigger.after-new-window", "trigger.after-split-window", "trigger.after-kill-pane", "trigger.pane-exited", "trigger.window-unlinked",
		"trigger.attention-focus", "trigger.recent-window-record", "trigger.client-attached-welcome", "config.generated-statusbar", "config.generated-key-sequences",
		"resource.rename-project", "resource.rename-window", "resource.rename-pane", "resource.rebind-project",
		"settings.desktop-notify-option", "settings.statusbar-decoration-option", "settings.ai-badge-option",
		"settings.generated-config-reload", "settings.key-sequence-retirement", "ai.integrate-tmux-bell", "notification.wsl-legacy-cleanup-marker",
	} {
		if !seen[required] {
			t.Errorf("closed product inventory is missing %q", required)
		}
	}
}

func TestGeneratedCatalogMutationAndNavigationArtifactsHaveOneSurfaceRow(t *testing.T) {
	rows := map[string][]runtimeMutationSurface{}
	for _, row := range runtimeMutationSurfaces {
		if after, ok := strings.CutPrefix(row.ID, "catalog."); ok {
			rows[after] = append(rows[after], row)
		}
	}
	seenCanonical := map[string]string{}
	seenLegacy := map[string]string{}
	for _, action := range defaultKeyBindingCatalog() {
		if strings.TrimSpace(action.CanonicalID) == "" {
			t.Errorf("generated catalog artifact %q has no public canonical id", action.ID)
			continue
		}
		if prior := seenCanonical[action.CanonicalID]; prior != "" {
			t.Errorf("generated catalog canonical id %q is shared by legacy ids %q and %q", action.CanonicalID, prior, action.ID)
		}
		seenCanonical[action.CanonicalID] = action.ID
		if prior := seenLegacy[action.ID]; prior != "" {
			t.Errorf("generated catalog legacy id %q is shared by canonical ids %q and %q", action.ID, prior, action.CanonicalID)
		}
		seenLegacy[action.ID] = action.CanonicalID
		matched := rows[action.CanonicalID]
		if len(matched) != 1 {
			t.Errorf("generated catalog artifact %q canonical=%q has %d product-surface rows, want one", action.ID, action.CanonicalID, len(matched))
		} else if matched[0].LegacyID != action.ID {
			t.Errorf("catalog surface %q legacy alias = %q, want shipped id %q", matched[0].ID, matched[0].LegacyID, action.ID)
		}
		for field := range strings.FieldsSeq(action.TmuxBody + " " + strings.Join(action.TmuxBodyAliases, " ")) {
			field = strings.Trim(field, `{};'"`)
			if closedTmuxTopologyMutationVerbs[field] {
				t.Errorf("generated catalog artifact %q embeds managed topology verb %q instead of its classified handler", action.ID, field)
			}
		}
		delete(rows, action.CanonicalID)
	}
	for id, unmatched := range rows {
		t.Errorf("surface row catalog.%s x%d has no generated catalog artifact", id, len(unmatched))
	}
}

func TestCanonicalCreateProducerInventoryMapsEveryNativeAndGeneratedSurface(t *testing.T) {
	rowsByID := map[string]bool{}
	for _, row := range runtimeMutationSurfaces {
		rowsByID[row.ID] = true
	}
	want := map[canonicalCreateProducer][]string{
		canonicalProducerPaneMenu:       {"pane-menu.split-right", "pane-menu.split-down"},
		canonicalProducerSavedDefault:   {"catalog.agent-pane.launch-default.right", "catalog.agent-pane.launch-default.down"},
		canonicalProducerProviderPicker: {"native-picker.provider-create"},
		canonicalProducerResumePicker:   {"native-picker.resume-create"},
		canonicalProducerDirectProvider: {"catalog.agent.create.codex.right", "catalog.agent.create.codex.down", "catalog.agent.create.claude.right", "catalog.agent.create.claude.down"},
		canonicalProducerDirectShell:    {"catalog.pane.create.shell.right", "catalog.pane.create.shell.down"},
	}
	if len(want) != len(canonicalCreateProducers) {
		t.Fatalf("canonical producer surface table has %d rows, source inventory has %d: %v", len(want), len(canonicalCreateProducers), canonicalCreateProducers)
	}
	for _, producer := range canonicalCreateProducers {
		ids := want[producer]
		if len(ids) == 0 {
			t.Errorf("canonical create producer %q has no product-surface mapping", producer)
			continue
		}
		for _, id := range ids {
			if !rowsByID[id] {
				t.Errorf("canonical create producer %q maps missing product-surface row %q", producer, id)
			}
		}
	}
	windowWant := map[canonicalCreateProducer]string{
		canonicalProducerWindowCreate: "catalog.window.create",
		canonicalProducerWindowRename: "catalog.window.rename",
	}
	if len(windowWant) != len(canonicalWindowMutationProducers) {
		t.Fatalf("canonical Window producer surface table has %d rows, source inventory has %d: %v", len(windowWant), len(canonicalWindowMutationProducers), canonicalWindowMutationProducers)
	}
	for _, producer := range canonicalWindowMutationProducers {
		id := windowWant[producer]
		if id == "" || !rowsByID[id] {
			t.Errorf("canonical Window producer %q maps missing product-surface row %q", producer, id)
		}
	}
}

func TestGeneratedWindowLifecycleActionsReachTypedHandlersWithoutRawManagedVerbs(t *testing.T) {
	wantRoute := map[string]string{
		"new-window":    "internal tmux window-create --client #{client_tty} --anchor #{pane_id}",
		"rename-window": "internal tmux window-rename --client #{client_tty} --anchor #{pane_id}",
	}
	seen := map[string]bool{}
	for _, action := range defaultKeyBindingCatalog() {
		route, ok := wantRoute[action.ID]
		if !ok {
			continue
		}
		seen[action.ID] = true
		wantCanonical := "window.create"
		if action.ID == "rename-window" {
			wantCanonical = "window.rename"
		}
		if action.CanonicalID != wantCanonical {
			t.Fatalf("generated %s canonical id = %q, want %q", action.ID, action.CanonicalID, wantCanonical)
		}
		body := renderTmuxBindingBody("/usr/local/bin/projmux", action)
		if !strings.Contains(body, route) {
			t.Fatalf("generated %s body = %q, want typed handler %q", action.ID, body, route)
		}
		for _, raw := range []string{" new-window ", " rename-window ", " split-window ", " kill-session ", " kill-window ", " kill-pane "} {
			if strings.Contains(" "+body+" ", raw) {
				t.Fatalf("generated %s body bypasses typed handler with %q: %s", action.ID, raw, body)
			}
		}
	}
	for id := range wantRoute {
		if !seen[id] {
			t.Fatalf("generated lifecycle producer %q disappeared without product inventory review", id)
		}
	}
}

func TestGeneratedPaneMenuArtifactsHaveOneClosedSurfaceRow(t *testing.T) {
	rendered := strings.Join(tmuxPaneContextMenuBindings("/usr/local/bin/projmux"), "\n")
	managed := regexp.MustCompile(`pane-menu --client #\{client_tty\} ([a-z-]+) #\{pane_id\}`).FindAllStringSubmatch(rendered, -1)
	got := map[string]bool{"pane-menu.resume": strings.Contains(rendered, "ai-split-resume-right"),
		"pane-menu.swap-up": strings.Contains(rendered, "swap-pane -U"), "pane-menu.swap-down": strings.Contains(rendered, "swap-pane -D"),
		"pane-menu.mark": strings.Contains(rendered, "select-pane -m"), "pane-menu.zoom": strings.Contains(rendered, "resize-pane -Z"),
		"pane-menu.mouse-forward": strings.Contains(rendered, "send-keys -M")}
	for _, match := range managed {
		got["pane-menu."+match[1]] = true
	}
	want := []string{"pane-menu.resume", "pane-menu.split-right", "pane-menu.split-down", "pane-menu.kill", "pane-menu.swap-up", "pane-menu.swap-down", "pane-menu.mark", "pane-menu.zoom", "pane-menu.mouse-forward"}
	rows := map[string]int{}
	for _, row := range runtimeMutationSurfaces {
		if strings.HasPrefix(row.ID, "pane-menu.") {
			rows[row.ID]++
		}
	}
	for _, id := range want {
		if !got[id] || rows[id] != 1 {
			t.Errorf("generated menu surface %q: artifact=%t rows=%d, want true/1", id, got[id], rows[id])
		}
		delete(got, id)
		delete(rows, id)
	}
	if len(got) != 0 || len(rows) != 0 {
		t.Fatalf("unclassified generated/menu surface delta: artifacts=%v rows=%v", got, rows)
	}
	for verb := range closedTmuxTopologyMutationVerbs {
		if verb == "swap-pane" || verb == "resize-pane" {
			continue // exact presentation rows above
		}
		if strings.Contains(" "+rendered+" ", " "+verb+" ") {
			t.Fatalf("generated Pane menu contains unmanaged lifecycle verb %q", verb)
		}
	}
}

func TestFullRenderedTmuxConfigsHaveClosedGeneratedMutationSurfaces(t *testing.T) {
	rows := map[string]runtimeMutationSurfaceDisposition{}
	for _, row := range runtimeMutationSurfaces {
		rows[row.ID] = row.Disposition
	}
	for _, id := range []string{
		"trigger.attention-focus", "trigger.pane-exited", "trigger.after-kill-pane", "trigger.window-unlinked",
		"trigger.recent-window-record", "trigger.after-new-window", "trigger.after-split-window",
		"trigger.client-attached-welcome", "config.generated-statusbar", "config.generated-key-sequences",
		"pane-menu.swap-up", "pane-menu.swap-down", "pane-menu.zoom",
	} {
		if rows[id] == "" {
			t.Fatalf("generated config producer %q has no closed surface row", id)
		}
	}

	overridden, err := mergeKeymapOverrides(defaultKeyBindingCatalog(), keymapFile{Bindings: map[string]keymapOverride{
		"window.create": {KeysSet: true, Keys: []string{"M-N"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	defaultWindow, _ := keyBindingActionByID(defaultKeyBindingCatalog(), "new-window")
	overriddenWindow, _ := keyBindingActionByID(overridden, "new-window")
	if overriddenWindow.TmuxKind != defaultWindow.TmuxKind || overriddenWindow.TmuxBody != defaultWindow.TmuxBody ||
		!reflect.DeepEqual(overriddenWindow.TmuxBodyAliases, defaultWindow.TmuxBodyAliases) {
		t.Fatal("Settings key override changed the closed generated action handler")
	}
	decorations := statusbarDecorationSetFromGlobal(config.StatusbarDecorationOff)
	configs := map[string]string{
		"standalone":                   tmuxStandaloneConfig("/usr/local/bin/projmux", config.StatusbarDecorationOff),
		"app":                          tmuxAppConfig("/usr/local/bin/projmux", "/bin/sh", config.StatusbarDecorationOff),
		"standalone-settings-override": tmuxStandaloneConfigWithKeymap("/usr/local/bin/projmux", decorations, overridden, true),
		"app-settings-override":        tmuxAppConfigWithKeymap("/usr/local/bin/projmux", "/bin/sh", decorations, overridden, true),
	}
	requiredArtifacts := map[string]string{
		"trigger.attention-focus":        "set-hook -g pane-focus-out",
		"trigger.pane-exited":            "set-hook -g pane-exited",
		"trigger.after-kill-pane":        "set-hook -g after-kill-pane",
		"trigger.window-unlinked":        "set-hook -g window-unlinked",
		"trigger.recent-window-record":   "set-hook -g after-select-window",
		"config.generated-statusbar":     "set -g status-right",
		"config.generated-key-sequences": tmuxSequenceRootsOption,
		"pane-menu.swap-up":              "swap-pane -U",
		"pane-menu.swap-down":            "swap-pane -D",
		"pane-menu.zoom":                 "resize-pane -Z",
	}
	appOnlyArtifacts := map[string]string{
		"trigger.after-new-window":        "set-hook -g after-new-window",
		"trigger.after-split-window":      "set-hook -g after-split-window",
		"trigger.client-attached-welcome": "set-hook -g client-attached",
	}
	expectedArtifactCounts := map[string]map[string]int{
		"standalone": {
			"trigger.attention-focus": 1, "trigger.pane-exited": 1, "trigger.after-kill-pane": 1,
			"trigger.window-unlinked": 1, "trigger.recent-window-record": 1,
			"config.generated-statusbar": 2, "config.generated-key-sequences": 1,
			"pane-menu.swap-up": 1, "pane-menu.swap-down": 1, "pane-menu.zoom": 1,
		},
		"app": {
			"trigger.attention-focus": 1, "trigger.pane-exited": 1, "trigger.after-kill-pane": 1,
			"trigger.window-unlinked": 1, "trigger.recent-window-record": 1,
			"config.generated-statusbar": 4, "config.generated-key-sequences": 2,
			"pane-menu.swap-up": 1, "pane-menu.swap-down": 1, "pane-menu.zoom": 1,
		},
		"standalone-settings-override": {
			"trigger.attention-focus": 1, "trigger.pane-exited": 1, "trigger.after-kill-pane": 1,
			"trigger.window-unlinked": 1, "trigger.recent-window-record": 1,
			"config.generated-statusbar": 2, "config.generated-key-sequences": 1,
			"pane-menu.swap-up": 1, "pane-menu.swap-down": 1, "pane-menu.zoom": 1,
		},
		"app-settings-override": {
			"trigger.attention-focus": 1, "trigger.pane-exited": 1, "trigger.after-kill-pane": 1,
			"trigger.window-unlinked": 1, "trigger.recent-window-record": 1,
			"config.generated-statusbar": 4, "config.generated-key-sequences": 2,
			"pane-menu.swap-up": 1, "pane-menu.swap-down": 1, "pane-menu.zoom": 1,
		},
	}
	for kind, rendered := range configs {
		for id, signature := range requiredArtifacts {
			want := expectedArtifactCounts[kind][id]
			if got := strings.Count(rendered, signature); got != want {
				t.Errorf("%s generated artifact %q signature %q count=%d, want exact %d", kind, id, signature, got, want)
			}
		}
		for id, signature := range appOnlyArtifacts {
			want := 0
			if strings.HasPrefix(kind, "app") {
				want = 1
			}
			if got := strings.Count(rendered, signature); got != want {
				t.Errorf("%s generated artifact %q signature %q count=%d, want %d", kind, id, signature, got, want)
			}
		}
		for verb := range closedTmuxTopologyMutationVerbs {
			occurrences := regexp.MustCompile(`(^|[[:space:];{}'\"])(`+regexp.QuoteMeta(verb)+`)([[:space:];{}'\"]|$)`).FindAllStringIndex(rendered, -1)
			if len(occurrences) == 0 {
				continue
			}
			if verb != "swap-pane" && verb != "resize-pane" {
				t.Errorf("%s generated config embeds unmanaged lifecycle/topology verb %q", kind, verb)
				continue
			}
			classified := 0
			if verb == "swap-pane" {
				classified = strings.Count(rendered, "swap-pane -U") + strings.Count(rendered, "swap-pane -D")
			} else {
				classified = strings.Count(rendered, "resize-pane -Z")
			}
			if classified != len(occurrences) {
				t.Errorf("%s generated config has %d %s occurrence(s), but only %d match exact closed presentation rows", kind, len(occurrences), verb, classified)
			}
		}
	}
}

func TestPropertyPlannedRuntimeMutationIsGuardedOrderedAndIdempotent(t *testing.T) {
	actions := []plannedRuntimeMutation{
		newRuntimeMutation(3, mutationWriteLayout, runtimeMutationTarget{Socket: "-L=property", PhysicalSocket: "/tmp/property", Kind: "pane", ID: "%3", UID: "pan-3"}),
		newRuntimeMutation(1, mutationWriteLease, runtimeMutationTarget{Socket: "-L=property", PhysicalSocket: "/tmp/property", Kind: "session", ID: "$1", UID: "prj-1"}),
		newRuntimeMutation(2, mutationCreatePane, runtimeMutationTarget{Socket: "-L=property", PhysicalSocket: "/tmp/property", Kind: "pane", ID: "%2", UID: "pan-2"}),
	}
	actions[0].Operands = []string{"-t", "%3", "-x", "40"}
	actions[1].Operands = []string{"-t", "$1", createOperationEnvironment, "op-property"}
	actions[2].Operands = []string{"-d", "-t", "%2"}
	left, err := newRuntimeMutationPlan(actions...).printableBytes()
	if err != nil {
		t.Fatal(err)
	}
	right, err := newRuntimeMutationPlan(actions[1], actions[2], actions[0]).printableBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("shuffled input changed plan bytes:\n%s\n---\n%s", left, right)
	}

	writes := 0
	guardFailure := []runtimeMutationStep{
		{Action: actions[2], Reobserve: func(context.Context) (bool, error) { return false, nil }, Guard: func(context.Context) error { return errPropertyDrift }, Apply: func(context.Context) error { writes++; return nil }},
		{Action: actions[0], Reobserve: func(context.Context) (bool, error) { return false, nil }, Guard: func(context.Context) error { return nil }, Apply: func(context.Context) error { writes++; return nil }},
		{Action: actions[1], Reobserve: func(context.Context) (bool, error) { return false, nil }, Guard: func(context.Context) error { return nil }, Apply: func(context.Context) error { writes++; return nil }},
	}
	if err := executeRuntimeMutationPlan(context.Background(), guardFailure); err == nil || !strings.Contains(err.Error(), "before first write") {
		t.Fatalf("guard drift error = %v", err)
	}
	if writes != 0 {
		t.Fatalf("guard drift performed %d writes, want zero", writes)
	}

	var applied, undone []int
	steps := make([]runtimeMutationStep, 0, len(actions))
	for _, action := range actions {
		order := action.Order
		step := runtimeMutationStep{
			Action:    action,
			Reobserve: func(context.Context) (bool, error) { return false, nil },
			Guard:     func(context.Context) error { return nil },
			Apply: func(context.Context) error {
				applied = append(applied, order)
				if order == 3 {
					return errPropertyApply
				}
				return nil
			},
		}
		// Order 2 represents a pre-existing effect that this operation does not
		// own. Its absent Undo is the structural rollback authority check.
		if order != 2 {
			step.Undo = func(context.Context) error { undone = append(undone, order); return nil }
		}
		steps = append(steps, step)
	}
	if err := executeRuntimeMutationPlan(context.Background(), steps); err == nil {
		t.Fatal("partial failure unexpectedly succeeded")
	}
	if !reflect.DeepEqual(applied, []int{1, 2, 3}) || !reflect.DeepEqual(undone, []int{3, 1}) {
		t.Fatalf("apply/owned rollback order = %v/%v, want [1 2 3]/[3 1]", applied, undone)
	}

	plan := newRuntimeMutationPlan(actions...)
	observed := map[string]bool{}
	var appliedEffects []string
	success := make([]runtimeMutationStep, 0, len(actions))
	for _, action := range actions {
		success = append(success, runtimeMutationStep{
			Action: action,
			Reobserve: func(context.Context) (bool, error) {
				return observed[runtimeMutationEffectKey(action)], nil
			},
			Guard: func(context.Context) error { return nil },
			Apply: func(context.Context) error {
				key := runtimeMutationEffectKey(action)
				appliedEffects = append(appliedEffects, key)
				observed[key] = true
				return nil
			},
		})
	}
	if err := executeRuntimeMutationPlan(context.Background(), success); err != nil {
		t.Fatalf("successful execute: %v", err)
	}
	wantEffects := make([]string, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		wantEffects = append(wantEffects, runtimeMutationEffectKey(action))
	}
	if !reflect.DeepEqual(appliedEffects, wantEffects) {
		t.Fatalf("successful applied effects = %v, want exact total order %v", appliedEffects, wantEffects)
	}
	beforeRepeat := len(appliedEffects)
	if err := executeRuntimeMutationPlan(context.Background(), success); err != nil {
		t.Fatalf("repeat execute: %v", err)
	}
	if len(appliedEffects) != beforeRepeat {
		t.Fatalf("repeat execute performed %d new writes, want zero", len(appliedEffects)-beforeRepeat)
	}
	repeat, err := plan.withoutEffects(observed)
	if err != nil {
		t.Fatal(err)
	}
	if len(repeat.Actions) != 0 {
		t.Fatalf("successful reobserve/replan = %#v, want empty", repeat.Actions)
	}
}

func TestRuntimeMutationArgvBindsPrintableTargetToExecutableOperand(t *testing.T) {
	action := newRuntimeMutation(1, mutationKillPane, runtimeMutationTarget{
		Socket: "-L=property", PhysicalSocket: "/tmp/property", Kind: "pane", ID: "%1", UID: "pan-1", Parent: "$1/@1",
	})
	action.Operands = []string{"-t", "%2"}
	if _, err := newRuntimeMutationPlan(action).printableBytes(); err == nil || !strings.Contains(err.Error(), "does not match printable target") {
		t.Fatalf("printable plan accepted mismatched executable target: %v", err)
	}
	if _, err := runtimeMutationArgv(action); err == nil || !strings.Contains(err.Error(), "does not match printable target") {
		t.Fatalf("mismatched executable target error = %v", err)
	}

	layout := newRuntimeMutation(1, mutationWriteLayout, runtimeMutationTarget{
		Socket: "-L=property", PhysicalSocket: "/tmp/property", Kind: "pane", ID: "%3", UID: "layout:%3", Parent: "@1",
	})
	layout.Operands = []string{"-t", "%3", "-x", "40"}
	argv, err := runtimeMutationArgv(layout)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"resize-pane", "-t", "%3", "-x", "40"}; !reflect.DeepEqual(argv, want) {
		t.Fatalf("typed layout argv = %v, want %v", argv, want)
	}
}

func TestRuntimeMutationArgvRejectsDuplicateTargetRouteAndSeparatorOperands(t *testing.T) {
	base := newRuntimeMutation(1, mutationKillPane, runtimeMutationTarget{
		Socket: "-L=property", PhysicalSocket: "/tmp/property", Kind: "pane", ID: "%7", UID: "pan-7",
	})
	tests := []struct {
		name     string
		operands []string
	}{
		{name: "duplicate exact target", operands: []string{"-t", "%7", "-t", "%7"}},
		{name: "duplicate overriding target", operands: []string{"-t", "%7", "-t", "%8"}},
		{name: "command separator", operands: []string{"-t", "%7", ";", "kill-server"}},
		{name: "escaped command separator", operands: []string{"-t", "%7", "\\;", "kill-server"}},
		{name: "embedded logical route", operands: []string{"-L", "foreign", "-t", "%7"}},
		{name: "embedded physical route", operands: []string{"-S", "/tmp/foreign", "-t", "%7"}},
		{name: "attached overriding target", operands: []string{"-t", "%7", "-t%8"}},
		{name: "attached logical route", operands: []string{"-t", "%7", "-Lforeign"}},
		{name: "attached physical route", operands: []string{"-t", "%7", "-S/tmp/foreign"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action := base
			action.Operands = tt.operands
			if _, err := runtimeMutationArgv(action); err == nil {
				t.Fatalf("runtimeMutationArgv(%v) accepted hidden execution authority", tt.operands)
			}
		})
	}
	child := newRuntimeMutation(1, mutationCreatePane, runtimeMutationTarget{
		Socket: "-L=property", PhysicalSocket: "/tmp/property", Kind: "pane", ID: "%7", UID: "pan-new", Parent: "$1/@2",
	})
	child.Operands = []string{"-d", "-P", "-F", "#{pane_id}", "-t", "%7"}
	for _, command := range [][]string{{";", "kill-server"}, {"/bin/sh", "\\;", "kill-server"}, {"-t", "%8"}, {"-S/tmp/foreign"}} {
		candidate := child
		candidate.Command = command
		if _, err := runtimeMutationArgv(candidate); err == nil {
			t.Fatalf("runtimeMutationArgv child command %q accepted hidden execution authority", command)
		}
	}
}

func TestRuntimeMutationPlanRejectsMalformedLaterActionBeforeFirstWrite(t *testing.T) {
	first := newRuntimeMutation(1, mutationKillPane, runtimeMutationTarget{
		Socket: "-L=property", PhysicalSocket: "/tmp/property", Kind: "pane", ID: "%7", UID: "pan-7",
	})
	first.Operands = []string{"-t", "%7"}
	malformed := newRuntimeMutation(2, mutationKillWindow, runtimeMutationTarget{
		Socket: "-L=property", PhysicalSocket: "/tmp/property", Kind: "window", ID: "@8", UID: "win-8",
	})
	malformed.Operands = []string{"-t", "@8", "-t", "@9"}
	writes := 0
	step := func(action plannedRuntimeMutation) runtimeMutationStep {
		return runtimeMutationStep{
			Action:    action,
			Reobserve: func(context.Context) (bool, error) { return false, nil },
			Guard:     func(context.Context) error { return nil },
			Apply:     func(context.Context) error { writes++; return nil },
		}
	}
	if err := executeRuntimeMutationPlan(context.Background(), []runtimeMutationStep{step(first), step(malformed)}); err == nil {
		t.Fatal("plan accepted a malformed later action")
	}
	if writes != 0 {
		t.Fatalf("invalid plan performed %d write(s), want zero", writes)
	}
}

func TestQueuedMutationBindsPrintableRouteContainmentAndConditionalCleanup(t *testing.T) {
	action := newRuntimeMutation(1, mutationQueuePaneKill, runtimeMutationTarget{
		Socket: "-L=property", PhysicalSocket: "/tmp/property.sock", Kind: "pane",
		ID: "%7", UID: "deleted:pan-7", Parent: "$1/@2/prj-1",
	})
	action.Queue = &runtimeMutationQueuedKill{
		PhysicalSocket: "/tmp/property.sock", LogicalSocket: "property", ExpectedUID: "deleted:pan-7",
		SessionID: "$1", WindowID: "@2",
	}
	action.Queue.Marker = runtimeMutationQueueMarker(action)
	argv, err := runtimeMutationArgv(action)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{"set-environment -g", "run-shell -b", "tmux' -S '/tmp/property.sock'", tmuxopts.AppGlobal, runtimeMutationSocketNameOption, "if-shell -F", "#{E:" + action.Queue.Marker + "}", action.Queue.ExpectedUID, "set-environment -gu"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("queued typed argv = %q, want conditional exact-route fragment %q", joined, want)
		}
	}
	if strings.Contains(joined, "; 'tmux' -S '/tmp/property.sock' set-environment -gu") {
		t.Fatalf("queued cleanup is an unconditional foreign-server write: %q", joined)
	}

	for _, mutate := range []func(*plannedRuntimeMutation){
		func(a *plannedRuntimeMutation) { a.Queue.PhysicalSocket = "/tmp/other.sock" },
		func(a *plannedRuntimeMutation) { a.Queue.LogicalSocket = "other" },
		func(a *plannedRuntimeMutation) { a.Queue.ExpectedUID = "deleted:foreign" },
		func(a *plannedRuntimeMutation) { a.Queue.WindowID = "@9" },
		func(a *plannedRuntimeMutation) { a.Target.Parent = "$9/@2/prj-1" },
	} {
		candidate := action
		queue := *action.Queue
		candidate.Queue = &queue
		mutate(&candidate)
		if _, err := runtimeMutationArgv(candidate); err == nil {
			t.Fatalf("queued mutation accepted printable/executable authority mismatch: %#v", candidate)
		}
	}

	runner := &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-S", action.Target.PhysicalSocket, "display-message", "-p", "-F", "#{socket_path}"):         action.Target.PhysicalSocket + "\n",
		recordedTmuxCallKey("tmux", "-S", action.Target.PhysicalSocket, "show-options", "-gqv", tmuxopts.AppGlobal):              "1\n",
		recordedTmuxCallKey("tmux", "-S", action.Target.PhysicalSocket, "show-options", "-gqv", runtimeMutationSocketNameOption): action.Queue.LogicalSocket + "\n",
		recordedTmuxCallKey("tmux", "-S", action.Target.PhysicalSocket, "show-environment", "-g"):                                action.Queue.Marker + "=deleted:newer-operation\n",
	}}
	if err := clearRuntimeMutationQueueMarker(context.Background(), runner, action); err == nil || !strings.Contains(err.Error(), "newer-operation") {
		t.Fatalf("owned queue-marker clear mismatch error = %v", err)
	}
	for _, call := range runner.calls {
		if slices.Contains(call.args, "-gu") {
			t.Fatalf("foreign/newer queue marker was cleared: %#v", runner.calls)
		}
	}
}

func TestRuntimeMutationArgvBindsCreateSessionDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		verb   runtimeMutationVerb
		target runtimeMutationTarget
		args   []string
	}{
		{name: "project mismatch", verb: mutationCreateSession,
			target: runtimeMutationTarget{Socket: "-L=property", PhysicalSocket: runtimeMutationSocketAbsentBeforeCreate, Kind: "project-declaration", ID: "project-a", UID: "prj-a"},
			args:   []string{"-d", "-s", "project-b"}},
		{name: "project duplicate", verb: mutationCreateSession,
			target: runtimeMutationTarget{Socket: "-L=property", PhysicalSocket: runtimeMutationSocketAbsentBeforeCreate, Kind: "project-declaration", ID: "project-a", UID: "prj-a"},
			args:   []string{"-d", "-s", "project-a", "-s", "project-a"}},
		{name: "control mismatch", verb: mutationBootstrapControlSession,
			target: runtimeMutationTarget{Socket: "-L=property", PhysicalSocket: runtimeMutationSocketAbsentBeforeCreate, Kind: "control-session-declaration", ID: "declaration:-L=property/session=home"},
			args:   []string{"-L", "property", "-f", "/tmp/config", "-d", "-s", "foreign"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			action := newRuntimeMutation(1, tc.verb, tc.target)
			action.Operands = tc.args
			if _, err := runtimeMutationArgv(action); err == nil {
				t.Fatalf("runtimeMutationArgv(%s) accepted mismatched declaration", tc.name)
			}
		})
	}
	action := newRuntimeMutation(1, mutationCreateSession, runtimeMutationTarget{
		Socket: "-L=property", PhysicalSocket: runtimeMutationSocketAbsentBeforeCreate, Kind: "project-declaration", ID: "project-a", UID: "prj-a",
	})
	action.Operands = []string{"-d", "-s", "project-a"}
	argv, err := runtimeMutationArgv(action)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"new-session", "-d", "-s", "project-a"}; !reflect.DeepEqual(argv, want) {
		t.Fatalf("typed create argv = %v, want %v", argv, want)
	}
}

func TestRuntimeMutationArgvBindsBootstrapAndRouteMarkerToPrintableRoute(t *testing.T) {
	bootstrap := newRuntimeMutation(1, mutationBootstrapControlSession, runtimeMutationTarget{
		Socket: "-L=app", PhysicalSocket: runtimeMutationSocketAbsentBeforeCreate, Kind: "control-session-declaration", ID: "declaration:-L=app/session=home",
	})
	bootstrap.Operands = []string{"-L", "other", "-f", "/tmp/config", "-d", "-s", "home"}
	if _, err := runtimeMutationArgv(bootstrap); err == nil || !strings.Contains(err.Error(), "does not match printable route") {
		t.Fatalf("bootstrap route mismatch error = %v", err)
	}
	bootstrap.Operands = []string{"-L", "app", "-S", "/tmp/extra", "-f", "/tmp/config", "-d", "-s", "home"}
	if _, err := runtimeMutationArgv(bootstrap); err == nil {
		t.Fatal("bootstrap accepted an extra executable route")
	}

	marker := newRuntimeMutation(1, mutationWriteRouteMarker, runtimeMutationTarget{
		Socket: "-L=app", PhysicalSocket: "/tmp/app", Kind: "app-server", ID: "socket:/tmp/app", UID: "logical:app",
	})
	marker.Operands = []string{"-L", "app", "-gq", runtimeMutationSocketNameOption, "other"}
	if _, err := runtimeMutationArgv(marker); err == nil || !strings.Contains(err.Error(), "logical identity") {
		t.Fatalf("route-marker logical mismatch error = %v", err)
	}
	marker.Operands[len(marker.Operands)-1] = "app"
	argv, err := runtimeMutationArgv(marker)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"-L", "app", "set-option", "-gq", runtimeMutationSocketNameOption, "app"}; !reflect.DeepEqual(argv, want) {
		t.Fatalf("typed route-marker argv = %v, want %v", argv, want)
	}
}

func TestPrintedPhysicalSocketIsExecutionAuthority(t *testing.T) {
	action := newRuntimeMutation(1, mutationKillPane, runtimeMutationTarget{
		Socket: "-L=logical", PhysicalSocket: "/tmp/printed.sock", Kind: "pane", ID: "%7", UID: "pan-7", Parent: "$1/@2",
	})
	action.Operands = []string{"-t", "%7"}
	runner := &recordingTmuxRunner{outputs: map[string]string{}}
	logical, err := tmuxSocketNameTarget("logical")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runRuntimeMutationCommand(context.Background(), explicitTmuxRunner{runner: runner, target: logical}, action); err != nil {
		t.Fatal(err)
	}
	want := recordedTmuxCall{name: "tmux", args: []string{"-S", "/tmp/printed.sock", "kill-pane", "-t", "%7"}}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0], want) {
		t.Fatalf("execution route = %#v, want printable physical authority %#v", runner.calls, want)
	}

	route := runtimeMutationRoute{target: logical, expectedSocketPath: "/tmp/bound.sock", socketName: "logical"}
	runner.calls = nil
	if err := guardPrintedRuntimeMutationRoute(context.Background(), runner, route, action); err == nil || !strings.Contains(err.Error(), "disagrees") {
		t.Fatalf("print/execution mismatch guard = %v", err)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("print/execution mismatch reached tmux: %#v", runner.calls)
	}
}

func TestMaterializerSocketDriftRefusesBeforeFirstWrite(t *testing.T) {
	production := newCreateCommand().runtime
	if _, route, err := production.exactMutationRoute(); err != nil || route != "-L="+defaultAppSocket {
		t.Fatalf("production materializer route = %q, %v; want -L=%s", route, err, defaultAppSocket)
	}
	if got := production.boundMutationTarget("session", "session-name", "uid:project").Socket; got != "-L="+defaultAppSocket {
		t.Fatalf("production printable target route = %q, want -L=%s", got, defaultAppSocket)
	}

	server := newFakeTmux()
	sessions := &fakeSessionMaterializer{tmux: server}
	session := server.addSession("drift")
	runtime := &materializer{
		runner: server, mirror: intmetadata.NewMirror(server), sessions: sessions,
		target: explicitTmuxTarget{flag: "-S", value: server.socketPath},
	}
	server.calls = nil
	server.socketPath = "/tmp/fake-tmux/foreign"
	err := runtime.markCreateOperation(context.Background(), session.id, newRuntimeLedger("op-drift"))
	if err == nil || !strings.Contains(err.Error(), "socket drifted") {
		t.Fatalf("socket drift guard error = %v", err)
	}
	for _, call := range server.calls {
		if slices.Contains(call, "set-environment") {
			t.Fatalf("socket drift reached runtime write: %#v", server.calls)
		}
	}
}

func TestMaterializerFirstSessionBindsPhysicalRouteBeforeFollowUpWrites(t *testing.T) {
	server := newFakeTmux()
	target := explicitTmuxTarget{flag: "-L", value: defaultAppSocket}
	routed := explicitTmuxRunner{runner: server, target: target}
	sessions := &fakeSessionMaterializer{tmux: server}
	runtime := &materializer{
		runner: routed, mirror: intmetadata.NewMirror(routed), sessions: sessions, target: target,
	}
	project := coremetadata.Project{
		Metadata: coremetadata.ObjectMeta{UID: "prj-first", Name: "first"},
		Spec:     coremetadata.ProjectSpec{Root: "/work/first"},
	}
	result, err := runtime.ensureSessionAt(context.Background(), project, "first", project.Spec.Root, newRuntimeLedger("op-first"))
	if err != nil {
		t.Fatalf("first-session materialize: %v", err)
	}
	if exactTmuxHandle(result.SessionID, "$") == "" || runtime.expectedSocketPath != server.socketPath {
		t.Fatalf("created result/physical binding = %#v / %q", result, runtime.expectedSocketPath)
	}
	created := false
	for _, call := range server.calls {
		if slices.Contains(call, "new-session") {
			created = true
			continue
		}
		if !created || (!slices.Contains(call, "set-option") && !slices.Contains(call, "set-environment")) {
			continue
		}
		if len(call) < 2 || call[0] != "-S" || call[1] != server.socketPath {
			t.Fatalf("post-create write escaped printed physical route: %#v", call)
		}
	}
}

func TestMaterializerDefaultRouteForgedServerRefusesBeforeFirstWrite(t *testing.T) {
	path := "/tmp/projmux-route/cloned-default.sock"
	base := &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "display-message", "-p", "-F", "#{socket_path}"): path + "\n",
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", tmuxopts.AppGlobal):      "0\n",
		recordedTmuxCallKey("tmux", "-S", path, "show-options", "-gqv", tmuxopts.AppGlobal):                  "0\n",
	}}
	target := explicitTmuxTarget{flag: "-L", value: defaultAppSocket}
	runtime := &materializer{
		runner: explicitTmuxRunner{runner: base, target: target}, target: target, expectedSocketPath: path,
	}
	err := runtime.markCreateOperation(context.Background(), "$1", newRuntimeLedger("op-forged-default"))
	if err == nil || !strings.Contains(err.Error(), "not app-owned") {
		t.Fatalf("forged default materializer error = %v", err)
	}
	for _, call := range base.calls {
		if slices.Contains(call.args, "set-environment") || slices.Contains(call.args, "set-option") {
			t.Fatalf("forged default materializer reached a write: %#v", base.calls)
		}
	}
}

func TestInvocationRoutePreservesNonDefaultLogicalSocketWithoutBasenameInference(t *testing.T) {
	path := "/tmp/projmux-route/not-the-logical-name.sock"
	name := "projmux-it-exact"
	runner := &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-S", path, "display-message", "-p", "-F", "#{socket_path}"):         path + "\n",
		recordedTmuxCallKey("tmux", "-S", path, "show-options", "-gqv", tmuxopts.AppGlobal):              "1\n",
		recordedTmuxCallKey("tmux", "-S", path, "show-options", "-gqv", runtimeMutationSocketNameOption): name + "\n",
		recordedTmuxCallKey("tmux", "-L", name, "display-message", "-p", "-F", "#{socket_path}"):         path + "\n",
	}}
	route, err := resolveInvocationRuntimeMutationRoute(context.Background(), runner, func(key string) string {
		if key == "TMUX" {
			return path + ",123,0"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if route.target.flag != "-L" || route.target.value != name || route.expectedSocketPath != path || route.socketName != name {
		t.Fatalf("resolved route = %#v, want logical -L %s over exact path %s", route, name, path)
	}
	if filepath.Base(path) == name {
		t.Fatal("fixture accidentally permits basename inference")
	}
}

func TestDefaultInvocationAliasRecoversCanonicalAppRouteBidirectionally(t *testing.T) {
	path, name := "/tmp/projmux-route/canonical.sock", "projmux-it-canonical"
	runner := &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "display-message", "-p", "-F", "#{socket_path}"):         path + "\n",
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", tmuxopts.AppGlobal):              "1\n",
		recordedTmuxCallKey("tmux", "-L", defaultAppSocket, "show-options", "-gqv", runtimeMutationSocketNameOption): name + "\n",
		recordedTmuxCallKey("tmux", "-L", name, "display-message", "-p", "-F", "#{socket_path}"):                     path + "\n",
	}}
	route, err := resolveInvocationRuntimeMutationRoute(context.Background(), runner, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if route.target.flag != "-L" || route.target.value != name || route.socketName != name || route.expectedSocketPath != path {
		t.Fatalf("canonical alias route = %#v", route)
	}
}

func TestForgedLogicalRouteMarkerOnForeignServerRefuses(t *testing.T) {
	path := "/tmp/projmux-route/foreign.sock"
	runner := &recordingTmuxRunner{outputs: map[string]string{
		recordedTmuxCallKey("tmux", "-S", path, "display-message", "-p", "-F", "#{socket_path}"): path + "\n",
		recordedTmuxCallKey("tmux", "-S", path, "show-options", "-gqv", tmuxopts.AppGlobal):      "0\n",
	}}
	_, err := resolveInvocationRuntimeMutationRoute(context.Background(), runner, func(string) string { return path + ",1,0" })
	if err == nil || !strings.Contains(err.Error(), "not app-owned") {
		t.Fatalf("foreign route error = %v", err)
	}
	for _, call := range runner.calls {
		joined := strings.Join(call.args, " ")
		if strings.Contains(joined, "set-option") || strings.Contains(joined, "new-") || strings.Contains(joined, "kill-") {
			t.Fatalf("forged marker refusal wrote tmux: %#v", runner.calls)
		}
	}
}

var (
	errPropertyDrift = &planPropertyError{"guard drift"}
	errPropertyApply = &planPropertyError{"injected partial failure"}
)

type planPropertyError struct{ text string }

func (e *planPropertyError) Error() string { return e.text }

func TestObservationFailureNeverPlansRegistryDeletion(t *testing.T) {
	plan := printableRuntimeMutationInventory()
	repeat, err := plan.withoutEffects(nil)
	if err == nil || !strings.Contains(err.Error(), "observation is unknown") {
		t.Fatalf("unknown observation = %#v, %v", repeat, err)
	}
	if len(repeat.Actions) != 0 {
		t.Fatalf("unknown observation planned actions: %#v", repeat.Actions)
	}
	for verb := range runtimeMutationInventory {
		if strings.Contains(string(verb), "registry") || strings.Contains(string(verb), "delete-row") {
			t.Fatalf("runtime inventory can delete Registry state on observation failure: %q", verb)
		}
	}

	store := newFakeResourceStore(t)
	runtime, runner, _ := newPaneRuntimeFixture(t, paneRuntimeInventory())
	format := tmuxRowFormat("#{session_id}", "#{session_name}", "#{window_id}", "#{pane_id}",
		"#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.WindowUID+"}", "#{"+tmuxopts.PaneUID+"}")
	listKey := recordedTmuxCallKey("tmux", "-S", testDeleteTarget.value, "list-panes", "-a", "-F", format)
	runner.errors = map[string]error{listKey: errPropertyDrift}
	command := newTestDeleteCommand(store, false, false, nil)
	command.panes = runtime
	before := store.snapshot()
	out, _, routeErr := runRoute(t, command, "pane", "uid:pan-alpha-log", "--yes")
	if routeErr == nil || !strings.Contains(routeErr.Error(), "inventory exact tmux socket") {
		t.Fatalf("production observation failure = %v", routeErr)
	}
	if out != "" || store.snapshot() != before || store.transactions != 0 {
		t.Fatalf("observation failure changed Registry/output: out=%q transactions=%d", out, store.transactions)
	}
	for _, call := range runner.calls {
		joined := strings.Join(call.args, " ")
		for _, forbidden := range []string{"kill-pane", "set-option", "run-shell"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("observation failure reached runtime mutation %q: %#v", forbidden, runner.calls)
			}
		}
	}
}

func TestNativePhase2FilesContainNoRawLifecycleTopologyProducer(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root := filepath.Dir(thisFile)
	for _, name := range []string{"create_agent.go", "agent_resume.go", "supervise_launch.go"} {
		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			if literal, ok := node.(*ast.BasicLit); ok && literal.Kind == token.STRING {
				value, err := strconv.Unquote(literal.Value)
				if err == nil && closedTmuxTopologyMutationVerbs[value] {
					t.Errorf("Phase 2-owned file %s embeds raw lifecycle/topology producer %q", name, value)
				}
			}
			if call, ok := node.(*ast.CallExpr); ok {
				if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
					switch selector.Sel.Name {
					case "NewSession", "NewWindow", "NewEphemeralSession", "KillSession":
						t.Errorf("Phase 2-owned file %s calls raw lifecycle helper %s", name, selector.Sel.Name)
					}
				}
			}
			return true
		})
	}
}

func TestPlanOnlyMutationNegativeAuditHasZeroBypass(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root := filepath.Dir(thisFile)
	excludedFiles := map[string]string{
		"create_agent.go": "Phase 2 native create owner", "agent_resume.go": "Phase 2 native resume owner",
		"supervise_launch.go": "Phase 2 native launch owner", "runtime_mutation_plan.go": "the typed executor itself",
		"runtime_mutation_surface.go": "the closed inventory declaration",
	}
	var files []string
	for _, scanRoot := range []string{root, filepath.Join(root, "..", "core", "controller"), filepath.Join(root, "..", "integrations", "mux"), filepath.Join(root, "..", "integrations", "metadata"), filepath.Join(root, "..", "integrations", "sessionstate"), filepath.Join(root, "..", "integrations", "tmux")} {
		if err := filepath.WalkDir(scanRoot, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			if _, excluded := excludedFiles[rel]; !excluded {
				files = append(files, rel)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(files)
	mutating := map[string]bool{
		"set-option": true, "set-environment": true, "select-layout": true, "rename-window": true,
		"set-hook": true, "source-file": true, "unbind-key": true,
	}
	for verb := range closedTmuxTopologyMutationVerbs {
		mutating[verb] = true
	}
	mutatingHelpers := map[string]bool{
		"NewEphemeralSession": true, "KillSession": true,
		"MirrorProject": true, "MirrorWindow": true, "MirrorPane": true, "RenameWindow": true,
		"MirrorControlSessionRole": true, "DisableAutomaticRename": true, "RebindProject": true,
		"SetOption": true, "SetHook": true,
	}
	requiredPlanSites := map[string]bool{
		"materialize.go:finalizeSessionStartup":                 true,
		"materialize.go:rollback":                               true,
		"materialize.go:ensureSessionAt":                        true,
		"materialize.go:claimRuntimeUID":                        true,
		"materialize.go:mirrorWindow":                           true,
		"materialize.go:mirrorPane":                             true,
		"materialize.go:recordErrorCreatedSession":              true,
		"materialize.go:markCreateOperation":                    true,
		"materialize.go:clearCreateOperations":                  true,
		"materialize.go:newWindow":                              true,
		"materialize.go:splitPane":                              true,
		"materialize.go:equalizeSplitLayout":                    true,
		"delete_pane_runtime.go:killAll":                        true,
		"delete_pane_runtime.go:tombstoneSelfKill":              true,
		"delete_pane_runtime.go:restoreSelfKill":                true,
		"delete_pane_runtime.go:queueSelfKill":                  true,
		"delete_window_runtime.go:killAll":                      true,
		"delete_window_runtime.go:queueSelfKill":                true,
		"runtime_stop.go:executeManagedRuntimeStop":             true,
		"shell.go:provisionAppSession":                          true,
		"shell.go:prepareControlSession":                        true,
		"shell.go:rollbackControlBootstrap":                     true,
		"create_intent.go:renameWindowFromIntent":               true,
		"rename.go:renameRuntimeWindow":                         true,
		"unmanaged_runtime_stop.go:executeUnmanagedRuntimeStop": true,
		"tmux.go:writeRuntimeMutationRouteMarker":               true,
		"control_session.go:executeControlSessionIdentityPlan":  true,
		"runtime_metadata_mirror.go:MirrorWindow":               true,
		"runtime_metadata_mirror.go:MirrorPane":                 true,
		"runtime_metadata_mirror.go:MirrorProject":              true,
	}
	// These are semantic exemptions, keyed to one exact source function and
	// verb. They are intentionally not a broad verb allowlist.
	exemptRawSites := map[string]string{
		"../integrations/sessionstate/replay.go:replay:rename-window":                     "replay.retired-snapshot",
		"../integrations/sessionstate/replay.go:replay:select-layout":                     "replay.retired-snapshot",
		"../integrations/tmux/client.go:CreateEphemeralSession:set-option":                "attach.ephemeral-create",
		"../integrations/tmux/client.go:applyProjectSessionEnv:set-environment":           "attach.ephemeral-create",
		"../integrations/tmux/client.go:setProjectPathAnchor:set-option":                  "attach.ephemeral-create",
		"../integrations/tmux/client.go:markStartupPane:set-option":                       "startup.presentation",
		"../integrations/tmux/client.go:KillSession:kill-session":                         "attach.ephemeral-prune",
		"tmux.go:runAutosaveSessionState:set-option":                                      "sessionstate.autosave-marker",
		"tmux.go:runRebalancePanes:select-layout":                                         "pane.rebalance",
		"tmux.go:runRenamePane:set-option":                                                "catalog.pane.rename",
		"../integrations/tmux/client.go:createDetachedSession:helper:NewEphemeralSession": "attach.ephemeral-create",
		"attach.go:executeAutoAttachPlan:helper:KillSession":                              "attach.ephemeral-prune",
		"prune.go:runEphemeral:helper:KillSession":                                        "standalone.prune",
		"materialize.go:read:variable-argv":                                               "runtime.observation",
		"materialize.go:equalizeSplitLayout:variable-argv":                                "runtime.observation",
		"tmux.go:managedIngestMigrationAIForRoute:variable-argv":                          "config.migration",
		"tmux.go:runApply:source-file":                                                    "config.apply-source",
		"tmux.go:retireGeneratedKeySequenceState:variable-argv":                           "key-sequence.retirement",
		"tmux.go:restoreSidebarOriginSession:variable-argv":                               "sidebar.origin-restore",
		"ai.go:readMux:variable-argv":                                                     "runtime.observation",
		"ai.go:readTrimmed:variable-argv":                                                 "runtime.observation",
		"../integrations/mux/lifecycle.go:SetHook:variable-argv":                          "config.migration",
		"../integrations/mux/lifecycle.go:SetOption:variable-argv":                        "config.migration",
		"../integrations/mux/lifecycle.go:SetHook:helper:SetHook":                         "ai.integrate-tmux-bell",
		"../integrations/mux/lifecycle.go:SetOption:helper:SetOption":                     "ai.integrate-tmux-bell",
		"../integrations/mux/lifecycle.go:NewEphemeralSession:variable-argv":              "attach.ephemeral-create",
		"../integrations/sessionstate/replay.go:replay:variable-argv":                     "replay.retired-snapshot",
		"../integrations/sessionstate/replay.go:replayPaneIdentityMetadata:variable-argv": "replay.retired-snapshot",
		"../integrations/tmux/client.go:OpenSession:variable-argv":                        "sidebar.origin-restore",
		"../integrations/tmux/client.go:OpenSessionTarget:variable-argv":                  "sidebar.origin-restore",
		"../integrations/tmux/client.go:DisplayPopupWithOptions:variable-argv":            "popup.display",
		"../integrations/metadata/inventory.go:Run:variable-argv":                         "runtime.observation",
		"../integrations/mux/interactive.go:DisplayPopup:variable-argv":                   "popup.display",
		"../integrations/mux/interactive.go:ClosePopup:variable-argv":                     "popup.display",
		"../integrations/mux/interactive.go:SwitchClient:variable-argv":                   "sidebar.origin-restore",
		"../integrations/mux/interactive.go:SelectPane:variable-argv":                     "sidebar.origin-restore",
		"../integrations/mux/interactive.go:SelectWindow:variable-argv":                   "sidebar.origin-restore",
		"../integrations/mux/runner.go:Run:variable-argv":                                 "runtime.observation",
		"../integrations/mux/runner.go:SetPaneOption:variable-argv":                       "agent.presentation",
		"../integrations/mux/runner.go:UnsetPaneOption:variable-argv":                     "agent.presentation",
		"../integrations/mux/runner.go:Read:variable-argv":                                "runtime.observation",
		"../integrations/tmux/sessionstate.go:MarkSessionStateSource:set-option":          "sessionstate.replay-metadata",
		"../integrations/tmux/sessionstate.go:setSessionStateAIResumeMetadata:set-option": "sessionstate.replay-metadata",
		"agent_interaction.go:WriteTopic:set-option":                                      "agent.presentation",
		"agent_interaction.go:WriteInteraction:set-option":                                "agent.presentation",
		"attention.go:run:variable-argv":                                                  "agent.presentation",
		"binding_convergence.go:Run:variable-argv":                                        "binding.convergence",
		"create_reentrancy.go:deferBindingConvergence:set-environment":                    "create.reentrancy",
		"focus.go:listTargets:variable-argv":                                              "runtime.observation",
		"focus.go:translatePaneIDToTarget:variable-argv":                                  "runtime.observation",
		"focus.go:listSessionInventory:variable-argv":                                     "runtime.observation",
		"focus.go:listClients:variable-argv":                                              "runtime.observation",
		"hook_trust_popup.go:runTmuxHookTrustPopup:variable-argv":                         "popup.display",
		"notify.go:focusNotification:variable-argv":                                       "notification.focus",
		"registry_topology_agents.go:mirrorTopologyAgentTopic:set-option":                 "agent.presentation",
		"runtime_diagnostics.go:socketPath:variable-argv":                                 "runtime.observation",
		"status.go:readTrimmed:variable-argv":                                             "runtime.observation",
		"status.go:Run:variable-argv":                                                     "runtime.observation",
		"statusbar.go:handlePopupToggleWithClient:variable-argv":                          "popup.display",
		"statusbar.go:handleNotify:variable-argv":                                         "notification.focus",
		"statusbar.go:runTmuxNoFallback:variable-argv":                                    "runtime.observation",
		"welcome_command.go:runPopup:variable-argv":                                       "popup.display",
		"quit.go:shutdownAppRuntime:kill-server":                                          "app.quit",
		"ai_integrate.go:runTmuxBellCommand:helper:SetOption":                             "ai.integrate-tmux-bell",
		"ai_integrate.go:runTmuxBellCommand:helper:SetHook":                               "ai.integrate-tmux-bell",
		"ai_integrate.go:runTmuxBellCommand:variable-argv":                                "ai.integrate-tmux-bell",
		"notification.go:ensureWSLLegacyAppIDCleaned:set-option":                          "notification.wsl-legacy-cleanup-marker",
		"settings_keybindings.go:regenerateAndReloadTmuxConfig:source-file":               "settings.generated-config-reload",
		"settings_keybindings.go:retireCurrentTmuxKeySequenceState:variable-argv":         "settings.key-sequence-retirement",
		"settings_notifications.go:setDesktopNotifyMode:set-option":                       "settings.desktop-notify-option",
		"settings_appearance.go:setStatusbarDecoration:set-option":                        "settings.statusbar-decoration-option",
		"settings_appearance.go:setAIBadgeStyle:set-option":                               "settings.ai-badge-option",
		"ai.go:applyAIStatusInternalWithActivationPolicy:set-option":                      "agent.presentation",
		"ai.go:setAIPaneBadgeKind:set-option":                                             "agent.presentation",
		"ai.go:resetAINotification:set-option":                                            "agent.presentation",
		"ai.go:runTopic:set-option":                                                       "agent.presentation",
		"ai.go:BindNativeCodexPane:set-option":                                            "agent.presentation",
		"ai.go:configureAIPane:set-option":                                                "agent.presentation",
		"ai.go:configureAIPaneResumeMetadata:set-option":                                  "agent.presentation",
		"ai.go:bootstrapAIWatchMetadata:set-option":                                       "agent.presentation",
		"ai.go:recordAITopic:set-option":                                                  "agent.presentation",
		"ai.go:projectManagedAgentInteraction:variable-argv":                              "agent.presentation",
		"ai.go:projectManagedAgentTopic:variable-argv":                                    "agent.presentation",
		"ai_ingest.go:recordBellNotification:set-option":                                  "agent.presentation",
		"ai_ingest.go:markAIHookPane:set-option":                                          "agent.presentation",
		"ai_ingest.go:writeAIHookResumeMetadata:set-option":                               "agent.presentation",
		"../integrations/metadata/tmuxmirror.go:RenameProject:set-option":                 "resource.rename-project",
		"../integrations/metadata/tmuxmirror.go:RebindProject:set-option":                 "resource.rebind-project",
		"../integrations/metadata/tmuxmirror.go:writePaneName:set-option":                 "resource.rename-pane",
		"../integrations/metadata/tmuxmirror.go:MirrorPane:set-option":                    "agent.presentation",
		"rebind.go:runProject:helper:RebindProject":                                       "resource.rebind-project",
	}
	// These exact sites are subordinate argv transports reached only from a
	// closed typed producer/controller plan. They are not semantic exemptions:
	// their cited surface must remain planned and their source key is audited
	// bidirectionally just like the app-owned runtimeMutationArgv seam.
	plannedTypedSites := map[string]string{
		"controller_trigger.go:runAutomaticMirrorRecovery:variable-argv":                        "controller.identity",
		"kill.go:KillSession:helper:KillSession":                                                "manual.tagged-kill",
		"resource_reconcile_plan.go:planResourceProjectMirrors:helper:MirrorProject":            "controller.identity",
		"resource_reconcile_plan.go:planResourceProjectMirrors:helper:RebindProject":            "controller.identity",
		"resource_reconcile_plan.go:planResourceBoundMirrorDrift:helper:DisableAutomaticRename": "controller.identity",
		"resource_reconcile_plan.go:planResourceBoundMirrorDrift:set-option":                    "controller.identity",
		"resource_controller.go:converge:variable-argv":                                         "controller.identity",
		"resource_reconcile_plan.go:planExactPaneOption:variable-argv":                          "controller.identity",
		"resource_reconcile_plan.go:Run:variable-argv":                                          "controller.identity",
		"registry_topology_materialize.go:execute:variable-argv":                                "project.materialize",
		"project_registry.go:newRegistryReconciler:helper:MirrorPane":                           "controller.identity",
		"../integrations/metadata/tmuxmirror.go:MirrorProject:set-option":                       "controller.identity",
		"../integrations/metadata/tmuxmirror.go:MirrorControlSessionRole:set-option":            "controller.identity",
		"../integrations/metadata/tmuxmirror.go:MirrorWindow:set-option":                        "controller.identity",
		"../integrations/metadata/tmuxmirror.go:disableAutomaticRename:set-option":              "controller.identity",
		"../integrations/metadata/tmuxmirror.go:writeWindowIdentityName:set-option":             "controller.identity",
		"../integrations/metadata/tmuxmirror.go:writeWindowDisplayName:rename-window":           "controller.identity",
	}
	surfaceDispositions := map[string]runtimeMutationSurfaceDisposition{}
	for _, row := range runtimeMutationSurfaces {
		surfaceDispositions[row.ID] = row.Disposition
	}
	seenExemptions := map[string]bool{}
	seenPlannedTypedSites := map[string]bool{}
	seenPlanSites := map[string]bool{}
	for _, name := range files {
		path := filepath.Join(root, name)
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			argvCallbacks := map[string]bool{}
			if function.Type.Params != nil {
				for _, field := range function.Type.Params.List {
					callback, ok := field.Type.(*ast.FuncType)
					if !ok || callback.Params == nil || len(callback.Params.List) != 1 {
						continue
					}
					variadic, ok := callback.Params.List[0].Type.(*ast.Ellipsis)
					if !ok {
						continue
					}
					name, stringArgs := variadic.Elt.(*ast.Ident)
					if !stringArgs || name.Name != "string" {
						continue
					}
					for _, fieldName := range field.Names {
						argvCallbacks[fieldName.Name] = true
					}
				}
			}
			usesPlanSeam := false
			var rawMutations []string
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if ident, ok := call.Fun.(*ast.Ident); ok {
					switch ident.Name {
					case "executeRuntimeMutationPlan", "runMaterializeMutation":
						usesPlanSeam = true
					}
					if argvCallbacks[ident.Name] {
						for _, argument := range call.Args {
							literal, ok := argument.(*ast.BasicLit)
							if !ok || literal.Kind != token.STRING {
								continue
							}
							value, unquoteErr := strconv.Unquote(literal.Value)
							if unquoteErr == nil && mutating[value] {
								rawMutations = append(rawMutations, value)
							}
						}
					}
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if ok && (selector.Sel.Name == "Command" || selector.Sel.Name == "CommandContext") {
					owner, directExec := selector.X.(*ast.Ident)
					if directExec && owner.Name == "exec" {
						start := 0
						if selector.Sel.Name == "CommandContext" {
							start = 1
						}
						if len(call.Args) > start {
							executable, literalExec := call.Args[start].(*ast.BasicLit)
							name := ""
							if literalExec && executable.Kind == token.STRING {
								name, _ = strconv.Unquote(executable.Value)
							}
							if name == "tmux" {
								before := len(rawMutations)
								variable := false
								for _, argument := range call.Args[start+1:] {
									literal, ok := argument.(*ast.BasicLit)
									if !ok || literal.Kind != token.STRING {
										variable = true
										continue
									}
									value, err := strconv.Unquote(literal.Value)
									if err == nil && mutating[value] {
										rawMutations = append(rawMutations, value)
									}
								}
								if variable && len(rawMutations) == before {
									rawMutations = append(rawMutations, "process-variable-argv")
								}
							}
						}
					}
				}
				if ok && (selector.Sel.Name == "runMutation" || selector.Sel.Name == "runMaterializeMutation" ||
					selector.Sel.Name == "runIdentityWrites" || selector.Sel.Name == "claimRuntimeUIDForRollback" ||
					selector.Sel.Name == "mirrorProject") {
					usesPlanSeam = true
				}
				if ok && (selector.Sel.Name == "run" || selector.Sel.Name == "runCommand") {
					beforeRaw := len(rawMutations)
					directTmux := false
					if len(call.Args) > 0 {
						if executable, literal := call.Args[0].(*ast.BasicLit); literal && executable.Kind == token.STRING {
							name, _ := strconv.Unquote(executable.Value)
							directTmux = name == "tmux"
						}
					}
					for _, argument := range call.Args {
						literal, ok := argument.(*ast.BasicLit)
						if !ok || literal.Kind != token.STRING {
							continue
						}
						value, unquoteErr := strconv.Unquote(literal.Value)
						if unquoteErr == nil && mutating[value] {
							rawMutations = append(rawMutations, value)
						}
					}
					if directTmux && call.Ellipsis.IsValid() && len(rawMutations) == beforeRaw {
						rawMutations = append(rawMutations, "variable-argv")
					}
				}
				if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "executeUnmanagedRuntimeStop" {
					usesPlanSeam = true
				}
				if ok && mutatingHelpers[selector.Sel.Name] {
					registryRename := false
					if receiver, ident := selector.X.(*ast.Ident); ident {
						registryRename = selector.Sel.Name == "RenameWindow" && receiver.Name == "mutator"
					}
					if !registryRename {
						rawMutations = append(rawMutations, "helper:"+selector.Sel.Name)
					}
				}
				if !ok || (selector.Sel.Name != "Run" && selector.Sel.Name != "read") {
					return true
				}
				beforeRaw := len(rawMutations)
				for _, argument := range call.Args {
					literal, ok := argument.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					value, unquoteErr := strconv.Unquote(literal.Value)
					if unquoteErr == nil && mutating[value] {
						rawMutations = append(rawMutations, value)
					}
				}
				if call.Ellipsis.IsValid() && len(rawMutations) == beforeRaw {
					rawMutations = append(rawMutations, "variable-argv")
				}
				return true
			})
			// killCommand is a pure closed argv projection; it cannot execute.
			pureArgvBuilder := name == "materialize.go" && function.Name.Name == "killCommand"
			transportOnly := name == "../integrations/metadata/tmuxmirror.go" && function.Name.Name == "run"
			if len(rawMutations) > 0 && !pureArgvBuilder && !transportOnly {
				for _, verb := range rawMutations {
					key := name + ":" + function.Name.Name + ":" + verb
					if surfaceID := plannedTypedSites[key]; surfaceID != "" {
						if got := surfaceDispositions[surfaceID]; got != runtimeMutationSurfacePlanned {
							t.Errorf("%s cites product-surface row %q with disposition %q, want planned typed transport", key, surfaceID, got)
						}
						seenPlannedTypedSites[key] = true
						continue
					}
					if surfaceID := exemptRawSites[key]; surfaceID != "" {
						if got := surfaceDispositions[surfaceID]; got != runtimeMutationSurfaceExempt {
							t.Errorf("%s cites product-surface row %q with disposition %q, want exact semantic exemption", key, surfaceID, got)
						}
						seenExemptions[key] = true
						continue
					}
					t.Errorf("%s:%s calls lifecycle/topology tmux verb %q outside the typed execution seam and closed semantic exemptions", name, function.Name.Name, verb)
				}
			}
			key := name + ":" + function.Name.Name
			if requiredPlanSites[key] {
				seenPlanSites[key] = true
				if !usesPlanSeam {
					t.Errorf("%s does not enter the typed execution seam", key)
				}
			}
		}
	}
	for site := range requiredPlanSites {
		if !seenPlanSites[site] {
			t.Errorf("required plan-only mutation site disappeared without inventory review: %s", site)
		}
	}
	for site := range exemptRawSites {
		if !seenExemptions[site] {
			t.Errorf("classified raw mutation exemption disappeared or changed without inventory review: %s", site)
		}
	}
	for site := range plannedTypedSites {
		if !seenPlannedTypedSites[site] {
			t.Errorf("classified planned typed transport disappeared or changed without inventory review: %s", site)
		}
	}
}

func TestGenericWindowPaneMirrorIsRecorderOnlyOutsideNativePhase2(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	root := filepath.Dir(thisFile)
	allowedCalls := map[string]bool{
		"project_registry.go:newRegistryReconciler:MirrorPane": true,
	}
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
			entry.Name() == "create_agent.go" || entry.Name() == "agent_resume.go" {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (selector.Sel.Name != "MirrorWindow" && selector.Sel.Name != "MirrorPane") {
					return true
				}
				key := entry.Name() + ":" + function.Name.Name + ":" + selector.Sel.Name
				if !allowedCalls[key] {
					t.Errorf("generic %s call escaped recorder-only/native closure at %s", selector.Sel.Name, key)
				}
				seen[key] = true
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for key := range allowedCalls {
		if !seen[key] {
			t.Errorf("recorder-only generic mirror call %q disappeared without inventory review", key)
		}
	}
	source, err := os.ReadFile(filepath.Join(root, "project_registry.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if strings.Count(text, "reconciler.mirrorWindow = mirror.MirrorWindow") != 1 ||
		!strings.Contains(text, "if _, recording := runner.(*resourcePlanTmuxRunner); recording {") ||
		strings.Contains(text, "r.mirror.MirrorWindow(ctx") || strings.Contains(text, "r.mirror.MirrorPane(ctx") {
		t.Fatal("project Registry generic Window/Pane mirror is not structurally recorder-only")
	}
}
