package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/i18n"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

const settingsNavTreeGolden = "testdata/settings-nav-tree.golden"

// TestSettingsNavigationTreeGolden freezes the whole visible information
// architecture in one artifact. A Settings IA change is a diff on this file,
// which is what makes the cutover reviewable: the tree is data, not a shape
// spread across twenty picker loops.
func TestSettingsNavigationTreeGolden(t *testing.T) {
	t.Parallel()

	got := renderSettingsNavTree()
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(settingsNavTreeGolden, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(settingsNavTreeGolden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("settings navigation tree changed.\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestSettingsNavigationCatalogIsStructurallySound proves the catalog is a
// tree: unique ids, a resolvable parent for every node, and a scope root at the
// top of every path.
func TestSettingsNavigationCatalogIsStructurallySound(t *testing.T) {
	t.Parallel()

	seen := map[string]bool{}
	for _, node := range settingsNavCatalog {
		if node.ID == "" {
			t.Fatalf("navigation node %#v has no id", node)
		}
		if seen[node.ID] {
			t.Fatalf("navigation node id %q is declared twice", node.ID)
		}
		seen[node.ID] = true
		if node.Kind == "" {
			t.Fatalf("navigation node %q has no affordance kind", node.ID)
		}
		if node.Axis == 0 {
			t.Fatalf("navigation node %q has no scope axis", node.ID)
		}
	}
	for _, node := range settingsNavCatalog {
		if node.Parent == "" {
			if node.ID != settingsNavScopeGlobal && node.ID != settingsNavScopeProject {
				t.Fatalf("navigation node %q has no parent but is not a scope root", node.ID)
			}
			continue
		}
		if !seen[node.Parent] {
			t.Fatalf("navigation node %q has unknown parent %q", node.ID, node.Parent)
		}
		if !strings.HasPrefix(node.ID, node.Parent+".") {
			t.Fatalf("navigation node %q is not a path under its parent %q", node.ID, node.Parent)
		}
	}
}

// TestSettingsNavigationAffordanceExclusivity is the row-affordance contract:
// every control lives under exactly one owning View, and no row both navigates
// and mutates. A control whose parent is not a View would be a row that mutates
// from a navigation surface.
func TestSettingsNavigationAffordanceExclusivity(t *testing.T) {
	t.Parallel()

	for _, node := range settingsNavCatalog {
		if node.Parent == "" {
			continue
		}
		parent, ok := settingsNavByID(node.Parent)
		if !ok {
			t.Fatalf("navigation node %q has unknown parent %q", node.ID, node.Parent)
		}
		if parent.Kind != settingsNavView {
			t.Fatalf("navigation node %q hangs off a %s row %q; every control must be owned by a View", node.ID, parent.Kind, parent.ID)
		}
		if node.Kind != settingsNavView && len(settingsNavChildren(node.ID)) != 0 {
			t.Fatalf("navigation node %q is a %s but owns children; only a View is a navigation boundary", node.ID, node.Kind)
		}
	}
}

func TestSettingsDirectionalIntentCatalogKindMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		key       string
		value     string
		hasParent bool
		want      settingsDirectionalIntent
	}{
		{name: "Right opens static navigation", key: "right", value: settingsSectionNotifications, want: settingsDirectionalForward},
		{name: "Right opens dynamic navigation", key: "right", value: settingsActionPrefixAINotifyDiagnostic + "claude", hasParent: true, want: settingsDirectionalForward},
		{name: "Right does not execute actionable", key: "right", value: settingsActionPrefixDesktopNotifyMode + "notify", hasParent: true, want: settingsDirectionalStay},
		{name: "Right does not execute dynamic actionable", key: "right", value: settingsActionPrefixKeymap + "SettingsToggle:add", hasParent: true, want: settingsDirectionalStay},
		{name: "Right does not activate passive", key: "right", value: settingsNoopValue, hasParent: true, want: settingsDirectionalStay},
		{name: "Right does not activate Back", key: "right", value: settingsBackValue, hasParent: true, want: settingsDirectionalStay},
		{name: "Right does not activate unknown", key: "right", value: "unknown", hasParent: true, want: settingsDirectionalStay},
		{name: "Left backs one child boundary", key: "left", value: settingsNoopValue, hasParent: true, want: settingsDirectionalBack},
		{name: "Left is root no-op", key: "left", value: settingsSectionNotifications, want: settingsDirectionalStay},
		{name: "Enter remains owned by loops", key: "enter", value: settingsSectionNotifications, hasParent: true, want: settingsDirectionalStay},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := settingsDirectionalIntentFor(tc.key, tc.value, tc.hasParent); got != tc.want {
				t.Fatalf("settingsDirectionalIntentFor(%q, %q, %v) = %v, want %v", tc.key, tc.value, tc.hasParent, got, tc.want)
			}
		})
	}
}

func TestSettingsDirectionalNativeActionMatrixAndLocalizedHints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		locale     i18n.Locale
		items      []intpicker.Item
		key        string
		value      string
		wantKey    string
		wantValue  string
		wantNoop   bool
		footerPart string
	}{
		{
			name:  "root Right opens navigation",
			items: []intpicker.Item{{Value: settingsSectionNotifications}}, key: "right", value: settingsSectionNotifications,
			wantKey: "enter", wantValue: settingsSectionNotifications, footerPart: "→: open row",
		},
		{
			name:  "child Right leaves actionable in place",
			items: []intpicker.Item{{Value: settingsBackValue}, {Value: settingsActionPrefixDesktopNotifyMode + "notify"}},
			key:   "right", value: settingsActionPrefixDesktopNotifyMode + "notify", wantNoop: true, footerPart: "←: back",
		},
		{
			name:  "child Right leaves passive in place",
			items: []intpicker.Item{{Value: settingsBackValue}, {Value: settingsNoopValue}},
			key:   "right", value: settingsNoopValue, wantNoop: true, footerPart: "←: back",
		},
		{
			name:  "child Right leaves Back in place",
			items: []intpicker.Item{{Value: settingsBackValue}}, key: "right", value: settingsBackValue,
			wantNoop: true, footerPart: "←: back",
		},
		{
			name:   "child Left backs exactly one level in Korean",
			locale: i18n.Locale("ko-KR"), items: []intpicker.Item{{Value: settingsBackValue}, {Value: settingsNoopValue}},
			key: "left", value: settingsNoopValue, wantKey: "enter", wantValue: settingsBackValue, footerPart: "←: 뒤로",
		},
		{
			name:  "root Left stays in place",
			items: []intpicker.Item{{Value: settingsSectionNotifications}}, key: "left", value: settingsSectionNotifications,
			wantNoop: true, footerPart: "→: open row",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			called := false
			runner := settingsDirectionalPickerRunner{next: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
				called = true
				if !strings.Contains(options.Footer, tc.footerPart) {
					t.Fatalf("footer = %q, want localized directional hint %q", options.Footer, tc.footerPart)
				}
				action, ok := pickerAction(options.Actions, tc.key)
				if !ok || action.Intent != intpicker.ActionCustom || action.Mutate == nil {
					t.Fatalf("%s action = %#v, want Settings-local mutable action", tc.key, action)
				}
				update, err := action.Mutate(intpicker.ActionContext{Key: tc.key, Value: tc.value, Query: "kept-query"})
				if err != nil {
					t.Fatalf("%s action error = %v", tc.key, err)
				}
				if tc.wantNoop {
					if update.Result != nil {
						t.Fatalf("%s on %q result = %#v, want in-picker no-op", tc.key, tc.value, update.Result)
					}
				} else if update.Result == nil || update.Result.Key != tc.wantKey || update.Result.Value != tc.wantValue || update.Result.Query != "kept-query" {
					t.Fatalf("%s on %q result = %#v, want key=%q value=%q with query preserved", tc.key, tc.value, update.Result, tc.wantKey, tc.wantValue)
				}
				return intpicker.Result{Key: "esc", Closed: true}, nil
			})}
			_, err := runner.Run(intpicker.Options{UI: "settings-directional-fixture", Items: tc.items, Locale: tc.locale, Footer: "existing"})
			if err != nil {
				t.Fatalf("directional runner error = %v", err)
			}
			if !called {
				t.Fatal("directional runner did not call the native picker")
			}
		})
	}
}

func TestSettingsDirectionalPolicyPreservesTransientInputArrows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*intpicker.Options)
	}{
		{name: "typed query", mutate: func(options *intpicker.Options) { options.AcceptQuery = true }},
		{name: "color grid", mutate: func(options *intpicker.Options) { options.ColorGrid = true }},
		{name: "key recorder", mutate: func(options *intpicker.Options) { options.Recorder = &intpicker.RecorderOptions{} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runner := settingsDirectionalPickerRunner{next: pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
				for _, key := range []string{"left", "right"} {
					if action, ok := pickerAction(options.Actions, key); ok && action.Mutate != nil {
						t.Fatalf("transient %s gained Settings hierarchy action %#v", tc.name, action)
					}
				}
				if strings.Contains(options.Footer, "→: open row") || strings.Contains(options.Footer, "←: back") {
					t.Fatalf("transient %s footer = %q, want native-input hint unchanged", tc.name, options.Footer)
				}
				return intpicker.Result{Key: "esc", Closed: true}, nil
			})}
			options := intpicker.Options{UI: "settings-transient", Items: []intpicker.Item{{Value: settingsNoopValue}}, Footer: "native input"}
			tc.mutate(&options)
			if _, err := runner.Run(options); err != nil {
				t.Fatalf("transient runner error = %v", err)
			}
		})
	}
}

func TestSettingsDirectionalNativeHierarchyBackAndAllDepthClose(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "keymap.toml"), `[bindings.SettingsToggle]
keys = ["M-s"]
`)
	cmd := settingsNavTestCommand(t, home)
	var frames []string
	step := 0
	cmd.nativePicker = pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
		frames = append(frames, options.Prompt)
		for _, closeKey := range []string{"esc", "alt-s"} {
			action, ok := pickerAction(options.Actions, closeKey)
			if !ok || action.Intent != intpicker.ActionClose {
				t.Fatalf("frame %q close action %q = %#v, want close", options.Prompt, closeKey, action)
			}
		}

		key, value := "", ""
		switch step {
		case 0:
			key, value = "right", settingsSectionNotifications
		case 1:
			key, value = "right", settingsNotificationsProviders
		case 2, 3:
			key, value = "left", settingsNoopValue
		case 4:
			// Root Left must not return a result (and therefore cannot close the
			// picker). The following Esc is the explicit all-Settings close.
			action, _ := pickerAction(options.Actions, "left")
			update, err := action.Mutate(intpicker.ActionContext{Key: "left", Value: settingsSectionNotifications})
			if err != nil || update.Result != nil {
				t.Fatalf("root Left update = %#v, err = %v, want in-picker no-op", update, err)
			}
			step++
			return intpicker.Result{Key: "esc", Closed: true}, nil
		default:
			t.Fatalf("unexpected Settings frame %d: %q", step, options.Prompt)
		}
		step++
		action, ok := pickerAction(options.Actions, key)
		if !ok || action.Mutate == nil {
			t.Fatalf("frame %q action %q missing", options.Prompt, key)
		}
		update, err := action.Mutate(intpicker.ActionContext{Key: key, Value: value})
		if err != nil || update.Result == nil {
			t.Fatalf("frame %q action %q update = %#v, err = %v", options.Prompt, key, update, err)
		}
		return *update.Result, nil
	})

	if err := cmd.Run(nil, &strings.Builder{}, &strings.Builder{}); err != nil {
		t.Fatalf("Settings hierarchy run error = %v", err)
	}
	if step != 5 {
		t.Fatalf("picker steps = %d, want root→child→grandchild→child→root then close", step)
	}
	wantPrompts := []string{
		"Settings > ",
		"Settings > Notifications > ",
		"Settings > Notifications > Provider Integrations > ",
		"Settings > Notifications > ",
		"Settings > ",
	}
	if strings.Join(frames, "\n") != strings.Join(wantPrompts, "\n") {
		t.Fatalf("Settings hierarchy frames = %#v, want %#v", frames, wantPrompts)
	}
}

func TestSettingsRightOnActionableDoesNotMutateOrClose(t *testing.T) {
	home := t.TempDir()
	configHome := filepath.Join(home, ".config")
	cmd := settingsNavTestCommand(t, home)
	cmd.runCommand = func(name string, args ...string) error {
		t.Fatalf("actionable Right ran command %q with args %v", name, args)
		return nil
	}
	cmd.runOutput = func(name string, args ...string) ([]byte, error) {
		t.Fatalf("actionable Right ran output command %q with args %v", name, args)
		return nil, nil
	}
	cmd.tmuxRunner = settingsDirectionalTmuxRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		t.Fatalf("actionable Right ran tmux command %q with args %v", name, args)
		return nil, nil
	})
	before := settingsNavConfigSnapshot(t, configHome)
	step := 0
	cmd.nativePicker = pickerRunnerFunc(func(options intpicker.Options) (intpicker.Result, error) {
		switch step {
		case 0:
			step++
			action, _ := pickerAction(options.Actions, "right")
			update, err := action.Mutate(intpicker.ActionContext{Key: "right", Value: settingsSectionAI})
			if err != nil || update.Result == nil {
				t.Fatalf("root Right navigation update = %#v, err = %v", update, err)
			}
			return *update.Result, nil
		case 1:
			step++
			action, _ := pickerAction(options.Actions, "right")
			update, err := action.Mutate(intpicker.ActionContext{Key: "right", Value: settingsActionPrefixAI + "claude"})
			if err != nil || update.Result != nil {
				t.Fatalf("actionable Right update = %#v, err = %v, want in-picker no-op", update, err)
			}
			// Returning Esc from the same native invocation proves Right itself
			// neither closed the View nor escaped into the owner mutation loop.
			return intpicker.Result{Key: "esc", Closed: true}, nil
		default:
			t.Fatalf("unexpected picker call %d", step)
		}
		return intpicker.Result{}, nil
	})

	if err := cmd.Run(nil, &strings.Builder{}, &strings.Builder{}); err != nil {
		t.Fatalf("Settings actionable Right run error = %v", err)
	}
	if step != 2 {
		t.Fatalf("picker calls = %d, want root and still-open AI View", step)
	}
	if after := settingsNavConfigSnapshot(t, configHome); after != before {
		t.Fatalf("actionable Right mutated Settings state:\nbefore=%q\nafter=%q", before, after)
	}
}

type settingsDirectionalTmuxRunnerFunc func(context.Context, string, ...string) ([]byte, error)

func (f settingsDirectionalTmuxRunnerFunc) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return f(ctx, name, args...)
}

// TestSettingsNavigationControlOwnerCardinality pins the control-owner
// cardinality: a rendered picker value belongs to exactly one navigation node,
// and that node's owning loop is the one the reachability table names.
func TestSettingsNavigationControlOwnerCardinality(t *testing.T) {
	t.Parallel()

	owners := map[string]string{}
	for _, node := range settingsNavCatalog {
		if node.Value == "" || node.Value == settingsNoopValue {
			continue
		}
		if previous, ok := owners[node.Value]; ok {
			t.Fatalf("picker value %q is claimed by both %q and %q; a control has exactly one owner", node.Value, previous, node.ID)
		}
		owners[node.Value] = node.ID

		meta, ok := settingsEntryMetaForValue(node.Value)
		if !ok {
			t.Fatalf("navigation node %q renders value %q with no catalog metadata", node.ID, node.Value)
		}
		if meta.Owner == settingsOwnerNone || !settingsEntryOwnerHandles(meta.Owner, node.Value) {
			t.Fatalf("navigation node %q renders value %q with no reachable owner loop: %#v", node.ID, node.Value, meta)
		}
		if meta.Axis&node.Axis == 0 {
			t.Fatalf("navigation node %q axis %b does not intersect catalog axis %b for value %q", node.ID, node.Axis, meta.Axis, node.Value)
		}
	}
}

// TestSettingsRenderedRowsMapOntoNavigationCatalog walks every static Settings
// surface and requires each rendered value to be a node the tree declares. It
// is the drift guard in the other direction: a loop cannot grow a row that the
// golden does not show.
func TestSettingsRenderedRowsMapOntoNavigationCatalog(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := settingsNavTestCommand(t, home)

	surfaces := map[string][]intpickercompat.Entry{
		"global root":           cmd.rootEntriesForAxisLocale(settingsAxisGlobal, i18n.FallbackLocale),
		"projects":              cmd.projectPickerEntries(),
		"project sidebar":       cmd.projectSidebarEntries(),
		"ai":                    cmd.aiRootEntries(),
		"ai resume picker":      cmd.aiResumePickerEntries(),
		"notifications":         cmd.notificationsEntries(),
		"desktop delivery":      cmd.desktopNotifyEntries(),
		"provider integrations": cmd.notifyDiagnosticCollectionEntries(cmd.notifyProviderDiagnostics()),
		"tmux event source":     cmd.tmuxEventSourceEntries(),
		"agent event behavior":  cmd.aiHookProviderEntries(),
		"agent event provider":  cmd.aiHookEventEntries(aiHookProviderClaude),
		"automation":            cmd.automationEntries(),
		"automation events":     cmd.hookLifecycleEntries(hookScopeGlobal),
		"appearance":            cmd.statusbarEntries(),
		"status bar":            cmd.statusBarEntries(),
		"snapshots":             cmd.sessionStateEntries(),
		"about":                 cmd.aboutEntries(),
		"about updates":         cmd.aboutUpdateEntries(),
	}
	keybindingRoot, err := cmd.keybindingEntries()
	if err != nil {
		t.Fatalf("keybindingEntries() error = %v", err)
	}
	surfaces["keybindings"] = keybindingRoot

	for name, entries := range surfaces {
		for _, entry := range entries {
			value := strings.TrimSpace(entry.Value)
			if value == "" || value == settingsBackValue || value == settingsNoopValue {
				continue
			}
			if _, ok := settingsNavByValue(value); ok {
				continue
			}
			// Dynamic rows (collection items, chooser values, toggles whose
			// value carries the next state) are declared as templates, so they
			// are matched by their owning prefix instead.
			if settingsNavDynamicValueDeclared(value) {
				continue
			}
			t.Fatalf("%s surface renders value %q with no navigation node", name, value)
		}
	}
}

// settingsNavDynamicValueDeclared reports whether a runtime value belongs to a
// dynamic template the catalog declares.
func settingsNavDynamicValueDeclared(value string) bool {
	for _, prefix := range []string{
		settingsActionPrefixHooks,
		settingsActionPrefixHookEvent,
		settingsActionPrefixLiveResources,
		settingsActionPrefixHUDVisibility,
		settingsActionPrefixKeymapCategory,
		settingsActionPrefixKeymapSurface,
		settingsActionPrefixWorkdirItem,
		settingsActionPrefixPinItem,
		settingsActionPrefixDesktopNotifyMode,
		settingsActionPrefixSessionStateSidebarStartup,
		settingsActionPrefixAINotifyDiagnostic,
		settingsActionPrefixAINotifyCheck,
		settingsActionPrefixAINotifyCommand,
		settingsActionPrefixAIHookProvider,
		settingsActionPrefixAIHookEvent,
		settingsActionPrefixAIHookSet,
		settingsActionPrefixSessionState,
		settingsActionPrefixStatusbar,
		settingsActionPrefixTheme,
		settingsActionPrefixWorkdir,
		settingsActionPrefixSwitch,
		settingsActionPrefixProjdir,
	} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

// TestSettingsRemovedRoutesAreUnreachable is the removed-path negative guard.
// Labs, the separate Theme root, the Project recipe and the Effective merge
// view are gone from navigation entirely: no rendered row, no catalog
// metadata, and no section route.
func TestSettingsRemovedRoutesAreUnreachable(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := settingsNavTestCommand(t, home)

	rendered := settingsNavAllRenderedEntries(t, cmd)
	for _, removed := range settingsNavRemovedRoots {
		for _, entry := range rendered {
			if strings.TrimSpace(entry.Value) == removed {
				t.Fatalf("removed route %q is still rendered: %#v", removed, entry)
			}
		}
		if _, ok := settingsEntryMetaForValue(removed); ok {
			t.Fatalf("removed route %q still has catalog metadata", removed)
		}
		if _, err := cmd.sectionOptions(removed); err == nil {
			t.Fatalf("removed route %q still resolves to section options", removed)
		}
	}
}

func TestProjectSettingsTreeHasOnlyAutomationAndSnapshotsAndRejectsRetiredRoutes(t *testing.T) {
	t.Parallel()

	children := settingsNavChildren(settingsNavScopeProject)
	if len(children) != 2 || children[0].ID != settingsNavProjectAutomation || children[1].ID != settingsNavProjectSnapshots {
		t.Fatalf("Project Settings children = %#v, want only Automation and Snapshots", children)
	}
	home := t.TempDir()
	cmd := settingsNavTestCommand(t, home)
	rendered := settingsNavAllRenderedEntries(t, cmd)
	for _, retired := range []string{"section:project-config", "section:effective-merge", "project-config:kube", "project-config:kube:context:set", "project-config:env", "project-config:startup"} {
		for _, entry := range rendered {
			if strings.TrimSpace(entry.Value) == retired {
				t.Fatalf("retired Project route %q is still rendered: %#v", retired, entry)
			}
		}
		if _, ok := settingsEntryMetaForValue(retired); ok {
			t.Fatalf("retired Project route %q still has catalog metadata", retired)
		}
	}
	for _, retiredSection := range []string{"section:project-config", "section:effective-merge"} {
		if _, err := cmd.sectionOptions(retiredSection); err == nil {
			t.Fatalf("retired Project section %q still resolves", retiredSection)
		}
	}
}

// TestSettingsLegacyVisibleCopyIsRetired is the legacy-copy negative guard.
// The retired vocabulary must not survive as a navigation label or as a
// rendered Settings row.
//
// Keybinding action labels are checked separately, because they name runtime
// surfaces rather than Settings destinations: the rename map deliberately keeps
// `Project Picker` as the name of the execution popup an action opens, while
// retiring it as the name of a Settings root.
func TestSettingsLegacyVisibleCopyIsRetired(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := settingsNavTestCommand(t, home)

	for _, node := range settingsNavCatalog {
		for _, legacy := range settingsNavRemovedVisibleCopy {
			if strings.Contains(node.Label, legacy) {
				t.Fatalf("navigation node %q label %q still carries retired copy %q", node.ID, node.Label, legacy)
			}
		}
	}

	for _, entry := range settingsNavAllRenderedEntries(t, cmd) {
		if strings.HasPrefix(entry.Value, settingsActionPrefixKeymap) ||
			strings.HasPrefix(entry.Value, settingsActionPrefixKeymapCategory) ||
			strings.HasPrefix(entry.Value, settingsActionPrefixKeymapSurface) {
			continue
		}
		label := stripANSI(entry.Label)
		for _, legacy := range settingsNavRemovedVisibleCopy {
			if strings.Contains(label, legacy) {
				t.Fatalf("legacy visible copy %q still renders: %q", legacy, label)
			}
		}
	}

	// The keymap action labels carry the canonical resource nouns too: the
	// retired Settings spellings that are not runtime surface names are gone
	// from them as well.
	for _, action := range defaultKeyBindingCatalog() {
		label := keyBindingDisplayName(action)
		for _, legacy := range []string{
			"Kill Session", "New window", "AI panes", "Notify Sidebar", "Session Popup",
			"Session State", "Delete Session",
		} {
			if strings.Contains(label, legacy) {
				t.Fatalf("keymap action %q label %q still carries retired copy %q", action.ID, label, legacy)
			}
		}
	}
}

// TestSettingsDisplayLabelsKeepMachineIdentifiers is the display-label →
// unchanged-ID parity check. Phase 0 renames what the user reads; it renames
// nothing a saved keymap, config file or runtime route depends on.
func TestSettingsDisplayLabelsKeepMachineIdentifiers(t *testing.T) {
	t.Parallel()

	// Keymap action IDs keep their shipped spelling even though every display
	// label changed.
	catalog := defaultKeyBindingCatalog()
	for _, id := range []string{
		"new-window", "Sidebar:KillSession", "ai-split-right", "ai-split-claude-down",
		"current-project-session", "SessionPopup:OpenState", "NotifySidebar:ClearAll",
		"Settings:SwitchTabNext", "previous-window", "select-pane-left",
	} {
		action, ok := keyBindingActionByID(catalog, id)
		if !ok {
			t.Fatalf("keymap action %q disappeared from the catalog", id)
		}
		if action.ID != id {
			t.Fatalf("keymap action id = %q, want unchanged %q", action.ID, id)
		}
		if label := keyBindingDisplayName(action); label == "" || label == id {
			t.Fatalf("keymap action %q display label = %q, want a distinct product label", id, label)
		}
	}

	// Config and runtime spellings are unchanged behind the renamed rows.
	for _, pair := range []struct{ label, value string }{
		{"Snapshots", settingsSectionSessionState},
		{"Snapshots", settingsActionPrefixSessionState + "autosave:on"},
		{"Closed Project startup", settingsActionPrefixSessionStateSidebarStartup + "on"},
		{"Project automation policy", settingsActionPrefixHooks + "off"},
		{"Resources", settingsActionPrefixLiveResources + "on"},
		{"Status Bar", settingsActionPrefixStatusbar + "git:symbol"},
		{"Additional discovery roots", settingsActionPrefixWorkdir + "add:/tmp/example"},
		{"Primary discovery root", settingsActionPrefixProjdir + "clear"},
	} {
		if _, ok := settingsEntryMetaForValue(pair.value); !ok {
			t.Fatalf("%s row lost its compatibility action spelling %q", pair.label, pair.value)
		}
	}
	for _, prefix := range []string{"sessionstate:", "project-hooks:", "live-resources:", "statusbar-decoration:", "workdir:", "projdir:", "switch:", "keymap:", "theme:"} {
		if _, ok := settingsEntryMetaForValue(prefix + "contract-fixture"); !ok {
			t.Fatalf("compatibility action prefix %q lost its owner contract", prefix)
		}
	}
}

// TestSettingsKeybindingCategoryExhaustiveness proves every catalog action
// belongs to exactly one category, that the assignment is explicit rather than
// prefix-derived, and that the sidebar/picker category covers every surface.
func TestSettingsKeybindingCategoryExhaustiveness(t *testing.T) {
	t.Parallel()

	catalog := defaultKeyBindingCatalog()
	assigned := map[string]int{}
	for _, action := range catalog {
		category, ok := keyBindingActionCategory(action)
		if !ok {
			t.Fatalf("keymap action %q has no navigation category", action.ID)
		}
		if category == keyBindingCategoryInput {
			t.Fatalf("keymap action %q is assigned to the input-delivery category, which holds no keymap action", action.ID)
		}
		if _, ok := keyBindingCategoryLabelByID(category); !ok {
			t.Fatalf("keymap action %q names unknown category %q", action.ID, category)
		}
		assigned[action.ID]++
		if action.Surface != "" {
			if category != keyBindingCategorySurfaces {
				t.Fatalf("surface action %q is in category %q, want the sidebar/picker category", action.ID, category)
			}
			if _, ok := keyBindingSurfaceLabel(action.Surface); !ok {
				t.Fatalf("surface action %q has surface %q with no display label", action.ID, action.Surface)
			}
		}
	}
	for id := range keyBindingCategoryByActionID {
		if _, ok := keyBindingActionByID(catalog, id); !ok {
			t.Fatalf("category assignment names unknown action %q", id)
		}
	}
	if len(keyBindingCategoryByActionID) != len(catalog) {
		t.Fatalf("category assignments = %d, want one per catalog action (%d)", len(keyBindingCategoryByActionID), len(catalog))
	}
	if len(keyBindingDisplayNames) != len(catalog) {
		t.Fatalf("display labels = %d, want one per catalog action (%d)", len(keyBindingDisplayNames), len(catalog))
	}

	// The rendered categories cover the catalog exactly once, and the sidebar
	// category reaches its actions through the surface level.
	cmd := settingsNavTestCommand(t, t.TempDir())
	rows := settingsKeybindingActionRows(t, cmd)
	rendered := map[string]int{}
	for _, row := range rows {
		if id, ok := strings.CutPrefix(row.Value, settingsActionPrefixKeymap); ok {
			rendered[id]++
		}
	}
	for _, action := range catalog {
		if rendered[action.ID] != 1 {
			t.Fatalf("keymap action %q rendered %d times across categories, want exactly once", action.ID, rendered[action.ID])
		}
	}
	if len(rendered) != len(catalog) {
		t.Fatalf("rendered action rows = %d, want %d", len(rendered), len(catalog))
	}
}

// TestSettingsCategoryEnterDoesNotMutate is the root/category no-mutation
// contract: opening the root, a category and a container never writes config.
func TestSettingsCategoryEnterDoesNotMutate(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := filepath.Join(home, ".config")
	cmd := settingsNavTestCommand(t, home)
	cmd.runCommand = func(name string, args ...string) error {
		t.Fatalf("opening a navigation row ran %s %v", name, args)
		return nil
	}

	before := settingsNavConfigSnapshot(t, configHome)
	settingsNavAllRenderedEntries(t, cmd)
	for _, section := range []string{
		settingsSectionProject, settingsSectionAI, settingsSectionNotifications,
		settingsSectionAutomation, settingsSectionStatusbar, settingsSectionSessionState,
		settingsSectionKeybindings, settingsSectionAbout,
	} {
		if _, err := cmd.sectionOptions(section); err != nil {
			t.Fatalf("sectionOptions(%q) error = %v", section, err)
		}
	}
	if after := settingsNavConfigSnapshot(t, configHome); after != before {
		t.Fatalf("navigation wrote config state:\nbefore=%q\nafter=%q", before, after)
	}
}

// TestSettingsPassiveRowsConsumeEnter pins the passive-Enter contract for the
// new state rows: every one of them carries the catalogued no-op value, so
// Enter can never fall through to an unknown-action error.
func TestSettingsPassiveRowsConsumeEnter(t *testing.T) {
	t.Parallel()

	for _, node := range settingsNavCatalog {
		if node.Kind != settingsNavState {
			continue
		}
		if node.Value != settingsNoopValue {
			t.Fatalf("state row %q renders value %q, want the passive no-op value", node.ID, node.Value)
		}
	}

	cmd := settingsNavTestCommand(t, t.TempDir())
	for _, entries := range [][]intpickercompat.Entry{
		cmd.hookEventDetailEntries(hookScopeGlobal, "post-create"),
		cmd.desktopNotifyEntries(),
		cmd.aiResumePickerEntries(),
		cmd.aboutUpdateEntries(),
		cmd.statusbarDecorationTargetEntries(statusbarDecorationTargetGit),
	} {
		if err := validateSettingsEntryContracts(intpickercompat.Options{UI: "settings-nav-fixture", Entries: entries}); err != nil {
			t.Fatalf("passive row contract: %v", err)
		}
	}
}

// TestSettingsPromotedRowsKeepTheirOldState is the old-state → new-route
// parity check for the three promoted/moved surfaces: Theme, Resources and the
// project automation policy. A user's saved value is read at the new
// destination exactly as the retired one read it.
func TestSettingsPromotedRowsKeepTheirOldState(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".config", "projmux", "live-resources"), "on\n")
	writeFile(t, filepath.Join(home, ".config", "projmux", "project-hooks"), "off\n")
	cmd := settingsNavTestCommand(t, home)

	// Resources reads the same saved file it read from Labs.
	statusBar := cmd.statusBarEntries()
	if !hasEntryLabelContainingAll(statusBar, "Resources", "on") {
		t.Fatalf("status bar entries = %#v, want the saved Resources state", statusBar)
	}
	if !hasEntryValue(statusBar, settingsActionPrefixLiveResources+"off") {
		t.Fatalf("status bar entries = %#v, want the toggle to offer the opposite state", statusBar)
	}

	// The project automation policy reads the same saved file it read from Labs.
	automation := cmd.automationEntries()
	if !hasEntryLabelContainingAll(automation, "Project automation policy", "off") {
		t.Fatalf("automation entries = %#v, want the saved project hooks policy", automation)
	}
	if !hasEntryValue(automation, settingsActionPrefixHooks+"on") {
		t.Fatalf("automation entries = %#v, want the toggle to offer the opposite state", automation)
	}

	// Theme is reachable from Appearance and still renders the token rows.
	appearance := cmd.statusbarEntries()
	if !hasEntryValue(appearance, settingsAppearanceTheme) {
		t.Fatalf("appearance entries = %#v, want the Theme view", appearance)
	}
	themeEntries, err := cmd.themeEntries()
	if err != nil {
		t.Fatalf("themeEntries() error = %v", err)
	}
	if !hasEntryValue(themeEntries, themeAction("preset")) || !hasEntryValue(themeEntries, themeAction("tokens")) {
		t.Fatalf("theme entries = %#v, want preset and tokens rows", themeEntries)
	}
}

// TestSettingsResourceVocabularyGolden freezes the canonical nouns the visible
// tree uses. It is the positive half of the vocabulary contract; the negative
// half is TestSettingsLegacyVisibleCopyIsRetired.
func TestSettingsResourceVocabularyGolden(t *testing.T) {
	t.Parallel()

	want := map[string]string{
		settingsNavProjects:                         "Projects",
		settingsNavProjectsPrimaryRoot:              "Primary discovery root",
		settingsNavProjectsExtraRoots:               "Additional discovery roots",
		settingsNavProjectsPins:                     "Pinned Projects",
		settingsNavProjectsSidebar:                  "Project Sidebar",
		settingsNavAI:                               "AI",
		settingsNavAI + ".launch-target":            "Default launch target",
		settingsNavAIProviders:                      "Enabled providers",
		settingsNavAIResumePicker:                   "Agent Resume Picker",
		settingsNavNotifications:                    "Notifications",
		settingsNavNotifyDesktop:                    "Desktop delivery",
		settingsNavNotifyProviders:                  "Provider Integrations",
		settingsNavNotifyTmuxSource:                 "tmux event source",
		settingsNavNotifyAgentEvents:                "Agent event behavior",
		settingsNavAutomation:                       "Automation",
		settingsNavAutomationLifecycle:              "Projmux session lifecycle",
		settingsNavAutomationSendNoti:               "After notification queued",
		settingsNavAppearance:                       "Appearance",
		settingsNavAppearanceTheme:                  "Theme",
		settingsNavStatusBar:                        "Status Bar",
		settingsNavStatusBar + ".working-directory": "Working directory",
		settingsNavStatusBar + ".notifications-hud": "Notifications HUD",
		settingsNavStatusBar + ".resources":         "Resources",
		settingsNavSnapshots:                        "Snapshots",
		settingsNavKeybindings:                      "Keybindings",
		settingsNavAbout:                            "About",
		settingsNavAbout + ".quit":                  "Quit Projmux",
		settingsNavProjectAutomation:                "Automation",
		settingsNavProjectSnapshots:                 "Snapshots",
	}
	for id, label := range want {
		if got := settingsNavLabel(id); got != label {
			t.Fatalf("navigation label for %q = %q, want %q", id, got, label)
		}
	}
}

// TestSettingsPinnedProjectDetailUsesCanonicalProjectIdentity covers the
// Project identity half of the vocabulary contract. The managed pin item View is
// addressed by uid, so a uid the Registry no longer answers for is stated as
// exactly that -- not as a path that might or might not be a Project -- and the
// remediation rows reflect it.
func TestSettingsPinnedProjectDetailUsesCanonicalProjectIdentity(t *testing.T) {
	t.Parallel()

	cmd := settingsNavTestCommand(t, t.TempDir())
	entries, err := cmd.pinnedProjectDetailEntries("uid:proj-missing")
	if err != nil {
		t.Fatalf("pinnedProjectDetailEntries() error = %v", err)
	}
	if !hasEntryLabelContaining(entries, "pinned UID with no Registry Project") {
		t.Fatalf("pin item entries = %#v, want the missing-Project state", entries)
	}
	// A uid the Registry does not carry cannot be rebound: the row is disabled
	// with its reason rather than silently doing nothing.
	rebind := entryWithLabelContaining(entries, "Rebind Project root")
	if rebind == nil || rebind.Value != settingsNoopValue {
		t.Fatalf("rebind row = %#v, want a disabled row for an unknown uid", rebind)
	}
	if !hasEntryValue(entries, settingsActionPrefixSwitch+"pin:uid:proj-missing") {
		t.Fatalf("pin item entries = %#v, want the item-owned unpin action addressed by uid", entries)
	}
}

// TestSettingsCandidatePinDetailStaysACandidate is the other half of the split.
// A pinned path that no Project claims says so, offers the explicit registration
// route, and offers nothing that would imply it is already managed.
func TestSettingsCandidatePinDetailStaysACandidate(t *testing.T) {
	t.Parallel()

	const path = "/tmp/not-a-registered-project"
	cmd := settingsNavTestCommand(t, t.TempDir())
	entries, err := cmd.candidatePinDetailEntries(path)
	if err != nil {
		t.Fatalf("candidatePinDetailEntries() error = %v", err)
	}
	if !hasEntryLabelContaining(entries, "no Registry Project claims this path") {
		t.Fatalf("candidate item entries = %#v, want the unregistered state", entries)
	}
	if !hasEntryValue(entries, settingsActionPrefixCandidatePinItem+path+":register") {
		t.Fatalf("candidate item entries = %#v, want the explicit register action", entries)
	}
	if !hasEntryValue(entries, settingsActionPrefixSwitch+"pin:"+path) {
		t.Fatalf("candidate item entries = %#v, want the item-owned unpin action", entries)
	}
	if hasEntryLabelContaining(entries, "Rebind Project root") {
		t.Fatalf("candidate item entries = %#v, want no rebind affordance on a candidate", entries)
	}
}

// settingsNavTestCommand builds a Settings command wired for entry rendering.
func settingsNavTestCommand(t *testing.T, home string) *settingsCommand {
	t.Helper()

	return &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_CONFIG_HOME":
				return filepath.Join(home, ".config")
			case "XDG_STATE_HOME":
				return filepath.Join(home, ".local", "state")
			default:
				return ""
			}
		},
		ai:                  testAICommand(home),
		switcher:            testSettingsSwitchCommandWithHome(t, home, newStubPinStore()),
		aiNotifyDiagnostics: func() []doctorAINotifyIntegration { return nil },
	}
}

// settingsNavAllRenderedEntries renders every static Settings surface once.
func settingsNavAllRenderedEntries(t *testing.T, cmd *settingsCommand) []intpickercompat.Entry {
	t.Helper()

	var all []intpickercompat.Entry
	all = append(all, cmd.rootEntriesForAxisLocale(settingsAxisGlobal, i18n.FallbackLocale)...)
	all = append(all, cmd.projectTabEntries()...)
	all = append(all, cmd.projectPickerEntries()...)
	all = append(all, cmd.projectSidebarEntries()...)
	all = append(all, cmd.aiRootEntries()...)
	all = append(all, cmd.aiResumePickerEntries()...)
	all = append(all, cmd.aiEnabledAgentEntries()...)
	all = append(all, cmd.notificationsEntries()...)
	all = append(all, cmd.desktopNotifyEntries()...)
	all = append(all, cmd.desktopNotifyModeEntries()...)
	all = append(all, cmd.notifyDiagnosticCollectionEntries(cmd.notifyProviderDiagnostics())...)
	all = append(all, cmd.tmuxEventSourceEntries()...)
	all = append(all, cmd.aiHookProviderEntries()...)
	all = append(all, cmd.aiHookEventEntries(aiHookProviderClaude)...)
	all = append(all, cmd.automationEntries()...)
	all = append(all, cmd.hookLifecycleEntries(hookScopeGlobal)...)
	all = append(all, cmd.hookEventDetailEntries(hookScopeGlobal, "post-create")...)
	all = append(all, cmd.statusbarEntries()...)
	all = append(all, cmd.statusBarEntries()...)
	all = append(all, cmd.statusbarDecorationTargetEntries(statusbarDecorationTargetGit)...)
	all = append(all, cmd.sessionStateEntries()...)
	all = append(all, cmd.aboutEntries()...)
	all = append(all, cmd.aboutUpdateEntries()...)
	all = append(all, settingsKeybindingActionRows(t, cmd)...)
	if entries, err := cmd.keybindingEntries(); err == nil {
		all = append(all, entries...)
	}
	if entries, err := cmd.themeEntries(); err == nil {
		all = append(all, entries...)
	}
	if entries, err := cmd.workdirListEntries(); err == nil {
		all = append(all, entries...)
	}
	if entries, err := cmd.pinnedProjectEntries(); err == nil {
		all = append(all, entries...)
	}
	if entries, err := cmd.projectRootEntries(); err == nil {
		all = append(all, entries...)
	}
	return all
}

// settingsNavConfigSnapshot renders the config tree as a comparable string.
func settingsNavConfigSnapshot(t *testing.T, root string) string {
	t.Helper()

	var b strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b.WriteString(path)
		b.WriteString("=")
		b.Write(data)
		b.WriteString("\n")
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("walk config: %v", err)
	}
	return b.String()
}

// TestSettingsActionDetailProjectsAgentAndAnchorSemantics covers the Agent
// create/resume distinction and the current-Pane anchor contract in the
// internal semantic model while proving that model is no longer projected as
// passive action-detail rows.
func TestSettingsActionDetailProjectsAgentAndAnchorSemantics(t *testing.T) {
	t.Parallel()

	cmd := settingsNavTestCommand(t, t.TempDir())

	for _, tc := range []struct {
		id    string
		wants []string
	}{
		{"ai-split-claude-right", []string{"Agent", "always a new Agent", "right", "current Pane %N transport id (explicit split target"}},
		{"ai-split-claude-down", []string{"down", "current Pane %N transport id (explicit split target"}},
		{"ai-split-shell-right", []string{"Pane", "Shell Pane", "current Pane %N transport id (explicit split target"}},
		{"ai-split-right", []string{"default launch target", "current Pane %N transport id (explicit split target"}},
		{"AIResumePickerToggle", []string{"Agent", "resume one existing Offline or Failed Agent", "never creates an Agent"}},
		{"new-window", []string{"Window", "new Window with its initial Pane"}},
		{"Sidebar:KillSession", []string{"Project", "Project metadata is kept"}},
	} {
		action, ok := keyBindingActionByID(defaultKeyBindingCatalog(), tc.id)
		if !ok {
			t.Fatalf("catalog missing %q", tc.id)
		}
		semantics, ok := keyBindingActionSemanticsFor(action)
		if !ok {
			t.Fatalf("action %q has no declared semantics", tc.id)
		}
		internal := strings.Join([]string{semantics.TargetKind, semantics.ResultKind, semantics.Placement, semantics.Anchor}, "\n")
		for _, want := range tc.wants {
			if !strings.Contains(internal, want) {
				t.Fatalf("action semantics %q = %#v, want %q", tc.id, semantics, want)
			}
		}

		entries, _, err := cmd.keybindingDetailEntries(tc.id)
		if err != nil {
			t.Fatalf("keybindingDetailEntries(%q) error = %v", tc.id, err)
		}
		for _, want := range []string{"Single Keys", "Sequences"} {
			if !hasEntryLabelContaining(entries, want) {
				t.Fatalf("action detail %q = %#v, want binding state %q", tc.id, entries, want)
			}
		}
		for _, forbidden := range []string{"Target kind", "Result kind", "Placement", "Anchor", "Handler", "manifest", "boundary"} {
			if hasEntryLabelContaining(entries, forbidden) {
				t.Fatalf("action detail %q = %#v, internal semantic copy %q became visible", tc.id, entries, forbidden)
			}
		}
	}

	// Every interactive split declares an explicit anchor: none of them may
	// leave the anchor to a stale primary Pane or to the focused Pane.
	catalog := defaultKeyBindingCatalog()
	for _, id := range []string{
		"ai-split-right", "ai-split-down", "ai-split-codex-right", "ai-split-codex-down",
		"ai-split-claude-right", "ai-split-claude-down", "ai-split-shell-right", "ai-split-shell-down",
	} {
		action, ok := keyBindingActionByID(catalog, id)
		if !ok {
			t.Fatalf("catalog missing %q", id)
		}
		semantics, ok := keyBindingActionSemanticsFor(action)
		if !ok {
			t.Fatalf("interactive split %q has no declared semantics", id)
		}
		if semantics.Anchor != keyBindingAnchorCurrentPaneSplitTarget {
			t.Fatalf("interactive split %q anchor = %q, want the explicit current Pane split target", id, semantics.Anchor)
		}
		if semantics.Placement != keyBindingPlacementRight && semantics.Placement != keyBindingPlacementDown {
			t.Fatalf("interactive split %q placement = %q, want right or down", id, semantics.Placement)
		}
	}
}

// TestSettingsAgentUsageOwnershipStaysWithAgentUsage pins the Agent Usage HUD
// ownership: it is a projection of the canonical `agent usage` command, not an
// addressable `Usage` resource, and no Settings surface says otherwise.
func TestSettingsAgentUsageOwnershipStaysWithAgentUsage(t *testing.T) {
	t.Parallel()

	cmd := settingsNavTestCommand(t, t.TempDir())
	for _, entry := range settingsNavAllRenderedEntries(t, cmd) {
		text := stripANSI(entry.Label) + " " + entry.SearchKey
		for _, forbidden := range []string{"get usage", "Usage resource"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("settings row %q must not carry %q", text, forbidden)
			}
		}
		if strings.TrimSpace(stripANSI(entry.Label)) == "Usage HUD" {
			t.Fatalf("settings row %q must use the canonical Agent Usage HUD label", text)
		}
	}

	ia, err := os.ReadFile("../../docs/settings-ia.md")
	if err != nil {
		t.Fatalf("read docs/settings-ia.md: %v", err)
	}
	doc := string(ia)
	if !strings.Contains(doc, "`agent usage`") {
		t.Fatal("docs/settings-ia.md must name `agent usage` as the canonical owner of the Agent Usage HUD")
	}
	if strings.Contains(doc, "get usage") {
		t.Fatal("docs/settings-ia.md must not reintroduce a `get usage` spelling")
	}
}

// TestSettingsConfirmRowsNameTargetAndResult covers the confirm contract: every
// destructive row says what it acts on and what happens, so a confirmation is
// never a bare yes/no.
func TestSettingsConfirmRowsNameTargetAndResult(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := settingsNavTestCommand(t, home)

	for _, tc := range []struct {
		name    string
		entries []intpickercompat.Entry
		label   string
		wants   []string
	}{
		{
			name:    "unpin project",
			entries: settingsNavMust(t, func() ([]intpickercompat.Entry, error) { return cmd.pinnedProjectDetailEntries("/tmp/example") }),
			label:   "Unpin Project",
			wants:   []string{"removes the pin", "Project metadata is kept"},
		},
		{
			name:    "remove automation command",
			entries: cmd.hookEventDetailEntries(hookScopeGlobal, "post-create"),
			label:   "Remove command",
			wants:   []string{"read-only here", "projmux hook edit post-create"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := entryWithLabelContaining(tc.entries, tc.label)
			if row == nil {
				t.Fatalf("%s rows = %#v, want a %q row", tc.name, tc.entries, tc.label)
			}
			label := stripANSI(row.Label)
			for _, want := range tc.wants {
				if !strings.Contains(label, want) {
					t.Fatalf("%s row = %q, want %q", tc.name, label, want)
				}
			}
		})
	}
}

func settingsNavMust(t *testing.T, load func() ([]intpickercompat.Entry, error)) []intpickercompat.Entry {
	t.Helper()

	entries, err := load()
	if err != nil {
		t.Fatalf("load entries: %v", err)
	}
	return entries
}
