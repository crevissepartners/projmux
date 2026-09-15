package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"
	"unicode"

	"github.com/crevissepartners/projmux/internal/core/pins"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// settingsSearchFrameFloor is the minimum number of searchable frames the
// default whole-tree walk must capture. The walk captured 223 when this guard
// was written; a much smaller number means the walker silently stopped
// entering part of the tree.
const settingsSearchFrameFloor = 200

// settingsSearchUnreachedNodes are the catalogued View/Choice nodes the walker
// cannot open without selecting a mutation or typing input. Every other
// visible View/Choice node must be the entry point of a captured frame, and an
// exemption that becomes reachable fails as stale.
var settingsSearchUnreachedNodes = map[string]string{
	settingsNavAppearanceTheme + ".tokens.item.fallback": "rendered as the direct theme color-set default mutation row inside the captured token View; there is no chooser frame",
	settingsNavSnapshots + ".autosave.interval":          "opens a typed input form, which the walker closes without answering",
	settingsNavSnapshots + ".storage":                    "rendered as a passive information row in Snapshots, not a View",
	settingsNavProjectSnapshots + ".autosave.choice":     "the Inherit/Enable/Disable choices are mutation rows inside the captured Auto-save override View",
	settingsNavProjectSnapshots + ".saved.item":          "needs a saved Project snapshot, which needs a live tmux session the fixture refuses",
}

// settingsSearchReadOnlyTmux are the tmux subcommands Settings Views may issue
// while rendering; the fixture refuses them, and any other subcommand is a
// write the walk must never trigger.
var settingsSearchReadOnlyTmux = []string{"list-panes", "has-session", "display-message", "show-options", "list-keys", "list-sessions", "show-environment"}

type settingsSearchScenario struct {
	name   string
	locale string
	// files are seeded under $XDG_CONFIG_HOME/projmux before the walk.
	files map[string]string
	// within limits the walk to catalog nodes on the path to, or under, these
	// prefixes; nil walks the whole tree.
	within []string
	// inject renders each Feedback summary once on the named e2e View through
	// the real runPicker (the walker answers the passive row, so the owning
	// loop re-renders with the Feedback row a mutation would leave).
	inject map[string][]string
}

type settingsSearchCapture struct {
	options      intpicker.Options
	node         string
	feedback     string
	viaRunPicker bool
}

type settingsSearchWalker struct {
	t        *testing.T
	cmd      *settingsCommand
	scenario settingsSearchScenario
	visited  map[string]map[string]bool
	viewNode map[string]string
	injected map[string]int
	pending  map[string]string
	captured map[string]bool
	frames   []settingsSearchCapture
	calls    int
	entered  int
	nextNode string
}

type settingsSearchViewOpener struct {
	ui    string
	match func(value string) bool
	node  func(parentNode string) string
}

func settingsSearchExact(want string) func(string) bool {
	return func(value string) bool { return value == want }
}

func settingsSearchPrefix(prefix string) func(string) bool {
	return func(value string) bool { return strings.HasPrefix(value, prefix) }
}

func settingsSearchFixedNode(id string) func(string) string {
	return func(string) string { return id }
}

// settingsSearchViewOpeners are values whose catalog meta is actionable (the
// prefix is shared with mutations) but whose owning loop only opens a View or
// chooser. Each is scoped to the UI that renders it.
var settingsSearchViewOpeners = []settingsSearchViewOpener{
	{"settings-projects-sidebar", settingsSearchExact(settingsSessionStateSidebarStartupPickerDetail), settingsSearchFixedNode(settingsNavProjectsSidebar + ".closed-startup")},
	{"settings-projects-sidebar", settingsSearchExact(settingsRuntimeDiagnosticsVisibilityDetail), settingsSearchFixedNode(settingsNavProjectsSidebar + ".runtime-diagnostics")},
	{"settings-notifications-desktop", settingsSearchExact(settingsActionPrefixDesktopNotifyMode + "choose"), settingsSearchFixedNode(settingsNavNotifyDesktop + ".mode")},
	{"settings-theme-global", settingsSearchExact(themeAction("preset")), settingsSearchFixedNode(settingsNavAppearanceTheme + ".preset")},
	{"settings-theme-global", settingsSearchExact(themeAction("tokens")), settingsSearchFixedNode(settingsNavAppearanceTheme + ".tokens")},
	{"settings-theme-tokens", settingsSearchPrefix(themeAction("group:")), settingsSearchFixedNode(settingsNavAppearanceTheme + ".tokens")},
	{"settings-theme-token-group", settingsSearchPrefix(themeAction("color:")), settingsSearchFixedNode(settingsNavAppearanceTheme + ".tokens.item")},
	{"settings-statusbar-detail", func(value string) bool {
		return strings.HasPrefix(value, settingsActionPrefixStatusbar) && strings.HasSuffix(value, ":icon")
	}, func(parent string) string { return parent + ".icon" }},
	{"settings-sessionstate", settingsSearchExact(settingsSessionStateAutosaveDetail), settingsSearchFixedNode(settingsNavSnapshots + ".autosave")},
	{"settings-project-sessionstate", settingsSearchExact(settingsProjectSessionStateAutosaveDetail), settingsSearchFixedNode(settingsNavProjectSnapshots + ".autosave")},
	{"settings-project-sessionstate", settingsSearchExact(settingsProjectSessionStateActionsDetail), settingsSearchFixedNode(settingsNavProjectSnapshots + ".saved")},
	{"settings-keybindings-category", settingsSearchPrefix(settingsActionPrefixKeymap), func(parent string) string { return parent + ".action" }},
	{"settings-keybindings-surface", settingsSearchPrefix(settingsActionPrefixKeymap), func(parent string) string { return parent + ".action" }},
	{"settings-keybinding-detail", func(value string) bool {
		rest, ok := strings.CutPrefix(value, settingsActionPrefixKeymap)
		return ok && (strings.Contains(rest, ":key:") || strings.Contains(rest, ":sequence:"))
	}, func(parent string) string { return parent + ".detail" }},
}

// settingsSearchDynamicTemplate returns the catalog template a dynamic
// navigation value projects onto.
func settingsSearchDynamicTemplate(value string) (string, bool) {
	if _, ok := settingsNavByValue(value); ok {
		return "", false
	}
	for _, candidate := range settingsDynamicEntryCatalog {
		if candidate.nodeID != "" && strings.HasPrefix(value, candidate.prefix) && len(value) > len(candidate.prefix) {
			return candidate.nodeID, true
		}
	}
	return "", false
}

func (w *settingsSearchWalker) allowed(node string) bool {
	if w.scenario.within == nil {
		return true
	}
	for _, prefix := range w.scenario.within {
		if strings.HasPrefix(node, prefix) || strings.HasPrefix(prefix, node+".") {
			return true
		}
	}
	return false
}

func (w *settingsSearchWalker) Run(options intpicker.Options) (intpicker.Result, error) {
	w.calls++
	if w.calls > 20000 {
		w.t.Fatalf("%s: settings walk exceeded 20000 picker calls (navigation cycle?)", w.scenario.name)
	}
	viewKey := strings.Join([]string{options.UI, options.Title, options.Prompt}, "\x00")
	if _, ok := w.viewNode[viewKey]; !ok {
		node := w.nextNode
		if node == "" {
			node = "(root)"
		}
		w.viewNode[viewKey] = node
	}
	node := w.viewNode[viewKey]
	w.nextNode = ""
	feedback := w.pending[viewKey]
	delete(w.pending, viewKey)

	values := make([]string, 0, len(options.Items))
	for _, item := range options.Items {
		values = append(values, item.Value)
	}
	sort.Strings(values)
	captureKey := strings.Join([]string{viewKey, feedback, strings.Join(values, "\x01")}, "\x00")
	if !w.captured[captureKey] {
		w.captured[captureKey] = true
		w.frames = append(w.frames, settingsSearchCapture{
			options:      options,
			node:         node,
			feedback:     feedback,
			viaRunPicker: settingsSearchStackHas("(*settingsCommand).runPicker"),
		})
	}

	// Transient inputs and pickers outside Settings are closed unanswered.
	if options.AcceptQuery || options.Recorder != nil || options.ColorGrid || !strings.HasPrefix(options.UI, "settings") {
		return intpicker.Result{Key: "esc", Closed: true}, nil
	}

	for view, summaries := range w.scenario.inject {
		if settingsSearchE2EViews[view](options) && w.injected[viewKey] < len(summaries) {
			summary := summaries[w.injected[viewKey]]
			w.injected[viewKey]++
			w.pending[viewKey] = summary
			w.cmd.feedback = &settingsFeedback{Summary: summary, Detail: "saved"}
			return intpicker.Result{Key: "enter", Value: settingsNoopValue}, nil
		}
	}

	seen := w.visited[viewKey]
	if seen == nil {
		seen = map[string]bool{}
		w.visited[viewKey] = seen
	}
	type candidate struct{ value, node string }
	var candidates []candidate
	hasBack := false
	for _, item := range options.Items {
		value := strings.TrimSpace(item.Value)
		switch value {
		case "", settingsNoopValue, settingsQuitOpen:
			// quit:open is a process-exit flow, not a searchable View.
			continue
		case settingsBackValue:
			hasBack = true
			continue
		}
		if meta, ok := settingsEntryMetaForValue(value); ok && meta.Kind == settingsEntryNavigation {
			if template, dynamic := settingsSearchDynamicTemplate(value); dynamic {
				// A dynamic prefix can also cover a mutation value inside the
				// item View (candidate-pin-item:<path>:register), so a template
				// is entered only from its catalog parent.
				if parent, ok := settingsNavByID(template); !ok || parent.Parent != node {
					continue
				}
				candidates = append(candidates, candidate{value, template})
				continue
			}
			id := value
			if static, ok := settingsNavByValue(value); ok {
				id = static.ID
			}
			candidates = append(candidates, candidate{value, id})
			continue
		}
		for _, opener := range settingsSearchViewOpeners {
			if opener.ui == options.UI && opener.match(value) {
				candidates = append(candidates, candidate{value, opener.node(node)})
				break
			}
		}
	}
	for _, chip := range options.TitleChips {
		if value := strings.TrimSpace(chip.ClickValue); value != "" && !chip.Disabled && !chip.Active {
			id := value
			if static, ok := settingsNavByValue(value); ok {
				id = static.ID
			}
			candidates = append(candidates, candidate{value, id})
		}
	}
	for _, next := range candidates {
		if seen[next.value] || !w.allowed(next.node) {
			continue
		}
		seen[next.value] = true
		w.entered++
		w.nextNode = next.node
		return intpicker.Result{Key: "enter", Value: next.value}, nil
	}
	if hasBack {
		return intpicker.Result{Key: "enter", Value: settingsBackValue}, nil
	}
	return intpicker.Result{Key: "esc", Closed: true}, nil
}

func settingsSearchStackHas(fragment string) bool {
	pcs := make([]uintptr, 64)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs)])
	for {
		frame, more := frames.Next()
		if strings.Contains(frame.Function, fragment) {
			return true
		}
		if !more {
			return false
		}
	}
}

// settingsSearchWalk seeds a fixture, drives the real Settings Run through the
// scripted walker until a pass enters nothing new, and proves the walk wrote
// nothing: the temporary HOME is byte-identical and only read-only tmux
// observations were attempted.
func settingsSearchWalk(t *testing.T, scenario settingsSearchScenario) *settingsSearchWalker {
	t.Helper()

	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "source", "repos", "app", ".git"), 0o755); err != nil {
		t.Fatalf("seed project marker: %v", err)
	}
	for name, content := range scenario.files {
		writeFile(t, filepath.Join(home, ".config", "projmux", name), content)
	}
	store := &stubSwitchPinStore{set: pins.Set{Format: pins.FormatTyped}.
		With(pins.Pin{Kind: pins.KindProject, Value: "proj-seeded"}).
		With(pins.Pin{Kind: pins.KindCandidate, Value: filepath.Join(home, "source", "candidate")})}
	cmd := settingsNavTestCommand(t, home)
	switcher := testSettingsSwitchCommandWithHome(t, home, store)
	switcher.loadWorkdirs = func(string) ([]string, error) { return []string{filepath.Join(home, "source", "extra")}, nil }
	cmd.switcher = switcher
	cmd.aiNotifyDiagnostics = func() []doctorAINotifyIntegration {
		return []doctorAINotifyIntegration{
			{ID: "claude", Name: "Claude", ProviderID: "claude", Status: doctorAINotifyStatusInstalled},
			{ID: settingsTmuxBellDiagnosticID, Name: "tmux bell", Status: doctorAINotifyStatusInstalled},
		}
	}
	baseEnv := cmd.lookupEnv
	cmd.lookupEnv = func(name string) string {
		if name == "PROJMUX_LOCALE" {
			return scenario.locale
		}
		return baseEnv(name)
	}
	var writes []string
	cmd.runCommand = func(name string, args ...string) error {
		writes = append(writes, name+" "+strings.Join(args, " "))
		return errors.New("settings search walk refuses commands")
	}
	cmd.runOutput = func(name string, args ...string) ([]byte, error) {
		writes = append(writes, name+" "+strings.Join(args, " "))
		return nil, errors.New("settings search walk refuses commands")
	}
	cmd.tmuxRunner = settingsDirectionalTmuxRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if len(args) == 0 || !slices.Contains(settingsSearchReadOnlyTmux, args[0]) {
			writes = append(writes, name+" "+strings.Join(args, " "))
		}
		return nil, errors.New("settings search walk refuses tmux")
	})
	walker := &settingsSearchWalker{
		t:        t,
		cmd:      cmd,
		scenario: scenario,
		visited:  map[string]map[string]bool{},
		viewNode: map[string]string{},
		injected: map[string]int{},
		pending:  map[string]string{},
		captured: map[string]bool{},
	}
	cmd.nativePicker = walker

	before := settingsNavConfigSnapshot(t, home)
	for pass := 1; ; pass++ {
		if pass > 20 {
			t.Fatalf("%s: settings walk did not converge", scenario.name)
		}
		walker.entered = 0
		if err := cmd.Run(nil, &strings.Builder{}, &strings.Builder{}); err != nil {
			t.Fatalf("%s: settings walk pass %d: %v", scenario.name, pass, err)
		}
		if walker.entered == 0 {
			break
		}
	}
	if after := settingsNavConfigSnapshot(t, home); after != before {
		t.Fatalf("%s: settings walk changed the temporary HOME", scenario.name)
	}
	if len(writes) > 0 {
		t.Fatalf("%s: settings walk attempted writes: %q", scenario.name, writes)
	}
	for _, frame := range walker.frames {
		// A frame that reached the native picker without runPicker would also
		// bypass the SearchKey join; the row assertions below would catch the
		// missing label, and this names the cause.
		if !frame.viaRunPicker {
			t.Errorf("%s: %s %q reached the native picker outside runPicker", scenario.name, frame.options.UI, frame.options.Prompt)
		}
	}
	return walker
}

func settingsSearchSearchable(options intpicker.Options) bool {
	return !options.DisableSearch && options.Recorder == nil && !options.ColorGrid
}

var settingsSearchANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

func settingsSearchRenderedLabel(item intpicker.Item) string {
	return strings.TrimSpace(settingsSearchANSI.ReplaceAllString(item.EffectiveLabel(), ""))
}

// settingsSearchKoreanQueries derives what a Korean user types for a rendered
// label: word is the first run of at least two Hangul runes (else the first
// run), phrase the first Hangul words joined by the single spaces between them.
func settingsSearchKoreanQueries(label string) (word, phrase string) {
	runes := []rune(label)
	var runs []string
	for i := 0; i < len(runes); {
		if !unicode.Is(unicode.Hangul, runes[i]) {
			i++
			continue
		}
		j := i
		for j < len(runes) && unicode.Is(unicode.Hangul, runes[j]) {
			j++
		}
		runs = append(runs, string(runes[i:j]))
		if phrase == "" {
			k := j
			for k+1 < len(runes) && runes[k] == ' ' && unicode.Is(unicode.Hangul, runes[k+1]) {
				k++
				for k < len(runes) && unicode.Is(unicode.Hangul, runes[k]) {
					k++
				}
			}
			phrase = string(runes[i:k])
		}
		i = j
	}
	for _, run := range runs {
		if len([]rune(run)) >= 2 {
			return run, phrase
		}
	}
	if len(runs) > 0 {
		return runs[0], phrase
	}
	return "", ""
}

// settingsSearchLabelName is the name column a user reads and types: the label
// without its leading glyph, cut at the first column gap.
func settingsSearchLabelName(label string) string {
	label = strings.TrimLeftFunc(label, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	if idx := strings.Index(label, "  "); idx >= 0 {
		label = label[:idx]
	}
	if idx := strings.Index(label, "\t"); idx >= 0 {
		label = label[:idx]
	}
	return strings.TrimSpace(label)
}

func settingsSearchFinds(items []intpicker.Item, query string, want intpicker.Item) bool {
	for _, item := range intpicker.FilterItems(items, query) {
		if item.Value == want.Value && item.Label == want.Label {
			return true
		}
	}
	return false
}

// settingsSearchIsFeedbackRow reports the transient Feedback row that
// withSettingsFeedback inserts after a mutation. It is a mutation result line,
// not a View row: runPicker joins labels before inserting it, so it keeps its
// pre-existing key and is the one row exempt from the rendered-label queries.
func settingsSearchIsFeedbackRow(item intpicker.Item) bool {
	probe := (&settingsCommand{feedback: &settingsFeedback{Summary: "probe"}}).withSettingsFeedback(intpickercompat.Options{UI: "settings"})
	return item.Value == settingsNoopValue && len(probe.Entries) == 1 && item.SearchText == probe.Entries[0].SearchKey
}

// settingsSearchE2EViews identify, by UI and row values, the Views
// test/e2e/linux-smoke.sh types into.
var settingsSearchE2EViews = map[string]func(intpicker.Options) bool{
	"root": func(o intpicker.Options) bool {
		return o.UI == "settings" && settingsSearchHasValue(o, "section:keybindings")
	},
	"appearance":    func(o intpicker.Options) bool { return o.UI == "settings-statusbar" },
	"status-bar":    func(o intpicker.Options) bool { return o.UI == "settings-status-bar" },
	"notifications": func(o intpicker.Options) bool { return o.UI == "settings-notifications" },
	"tmux-source":   func(o intpicker.Options) bool { return o.UI == "settings-notifications-tmux-source" },
	"usage-hud":     func(o intpicker.Options) bool { return o.UI == "settings-agent-usage-hud" },
	"usage-claude": func(o intpicker.Options) bool {
		return o.UI == "settings-agent-usage-provider" && settingsSearchHasValue(o, ":claude:")
	},
	"keybindings": func(o intpicker.Options) bool { return o.UI == "settings-keybindings" },
	"launch-category": func(o intpicker.Options) bool {
		return o.UI == "settings-keybindings-category" && settingsSearchHasValue(o, "keymap:ProjectSidebarToggle")
	},
	"sidebar-category": func(o intpicker.Options) bool {
		return o.UI == "settings-keybindings-category" && settingsSearchHasValue(o, "keymap-surface:NotifySidebar")
	},
	"notify-surface": func(o intpicker.Options) bool {
		return o.UI == "settings-keybindings-surface" && settingsSearchHasValue(o, "keymap:NotifySidebar:FocusAndAck")
	},
	"detail-project-sidebar": func(o intpicker.Options) bool {
		return o.UI == "settings-keybinding-detail" && settingsSearchHasValue(o, "keymap:ProjectSidebarToggle:")
	},
	"detail-focus-ack": func(o intpicker.Options) bool {
		return o.UI == "settings-keybinding-detail" && settingsSearchHasValue(o, "keymap:NotifySidebar:FocusAndAck:")
	},
	"key-detail-cr": func(o intpicker.Options) bool {
		return o.UI == "settings-keybinding-key-detail" && settingsSearchHasValue(o, ":test:C-r")
	},
	"sequence-detail": func(o intpicker.Options) bool { return o.UI == "settings-keybinding-sequence-detail" },
}

func settingsSearchHasValue(options intpicker.Options, fragment string) bool {
	for _, item := range options.Items {
		if strings.Contains(item.Value, fragment) {
			return true
		}
	}
	return false
}

const (
	settingsSearchKeymapCR        = "schema_version = 2\n\n[bindings.ProjectSidebarToggle]\nkeys = [\"M-1\", \"C-r\"]\n"
	settingsSearchKeymapCRSeq     = "schema_version = 2\n\n[bindings.ProjectSidebarToggle]\nkeys = [\"M-1\", \"C-r\"]\nsequences = [\"C-o o\"]\n"
	settingsSearchKeymapUnbound   = "schema_version = 2\n\n[bindings.ProjectSidebarToggle]\nkeys = []\n"
	settingsSearchWeeklyOffConfig = "statusbar-visibility-agent-usage-window-claude-weekly"
)

// settingsSearchE2ESteps is the query sequence test/e2e/linux-smoke.sh types
// into the Settings popup, each in the keymap/visibility state and with the
// Feedback row that sequence leaves on screen at that moment. want is the
// first surviving row measured on the pre-join code (4f0df7ba); Enter selects
// it, so a change here breaks the e2e walk. Keep this table in step with
// the smoke script.
var settingsSearchE2ESteps = []struct {
	scenario, view, feedback, query, want string
}{
	{"default", "root", "", "Status Bar", "section:statusbar"},
	{"default", "appearance", "", "status bar components", "appearance:status-bar"},
	{"default", "status-bar", "", "Back", "__settings_back__"},
	{"default", "appearance", "", "Back", "__settings_back__"},
	{"default", "root", "", "Provider Integrations", "section:notifications"},
	{"default", "notifications", "", "tmux event source bell producer", "notifications:tmux-event-source"},
	{"default", "tmux-source", "", "Back", "__settings_back__"},
	{"default", "notifications", "", "Back", "__settings_back__"},
	{"default", "root", "", "Appearance", "section:statusbar"},
	{"default", "appearance", "", "agent usage hud providers windows", "appearance:status-bar"},
	{"default", "status-bar", "", "agent usage hud providers windows", "appearance:status-bar:agent-usage-hud"},
	{"default", "usage-hud", "", "claude", "appearance:status-bar:agent-usage-provider:claude"},
	{"default", "usage-claude", "", "weekly", "statusbar-visibility:agent-usage-window:claude:weekly:off"},
	{"weekly-off", "usage-claude", "Claude Weekly complete", "weekly", "statusbar-visibility:agent-usage-window:claude:weekly:on"},
	{"default", "usage-claude", "Claude Weekly complete", "Back", "__settings_back__"},
	{"default", "usage-hud", "", "Back", "__settings_back__"},
	{"default", "root", "", "Keybindings", "section:keybindings"},
	{"default", "keybindings", "", "Open / close Project Sidebar", "keymap-category:launch-and-popups"},
	{"default", "launch-category", "", "Open / close Project Sidebar", "keymap:ProjectSidebarToggle"},
	{"default", "detail-project-sidebar", "", "+ Add binding", "keymap:ProjectSidebarToggle:add"},
	{"keymap-cr", "detail-project-sidebar", "Keybinding complete", "+ Add binding", "keymap:ProjectSidebarToggle:add"},
	{"keymap-cr", "detail-project-sidebar", "Keybinding cancelled", "key:C-r", "keymap:ProjectSidebarToggle:key:C-r"},
	{"keymap-cr", "key-detail-cr", "", "Test delivery", "keymap:ProjectSidebarToggle:test:C-r"},
	{"keymap-cr", "key-detail-cr", "Test delivery complete", "Back", "__settings_back__"},
	{"keymap-cr", "detail-project-sidebar", "", "+ Add binding", "keymap:ProjectSidebarToggle:add"},
	{"keymap-cr-seq", "detail-project-sidebar", "Keybinding complete", "sequence:C-o o", "keymap:ProjectSidebarToggle:sequence:C-o o"},
	{"keymap-cr-seq", "sequence-detail", "", "Remove sequence", "keymap:ProjectSidebarToggle:sequence-remove:C-o o"},
	{"keymap-cr", "detail-project-sidebar", "Keybinding complete", "Unbind single keys", "keymap:ProjectSidebarToggle:unbind"},
	{"keymap-unbound", "detail-project-sidebar", "Keybinding complete", "Use default", "keymap:ProjectSidebarToggle:reset"},
	{"default", "detail-project-sidebar", "Keybinding complete", "Back", "__settings_back__"},
	{"default", "launch-category", "", "Back", "__settings_back__"},
	{"default", "keybindings", "", "Focus source and acknowledge Notification", "keymap-category:sidebar-and-picker-actions"},
	{"default", "sidebar-category", "", "Focus source and acknowledge Notification", "keymap-surface:NotifySidebar"},
	{"default", "notify-surface", "", "Focus source and acknowledge Notification", "keymap:NotifySidebar:FocusAndAck"},
	{"default", "detail-focus-ack", "", "key:Enter", "keymap:NotifySidebar:FocusAndAck:key:Enter"},
}

// TestSettingsSearchTextCoversLocalizedLabels walks every reachable Settings
// View through the real runPicker and filters the exact rows the native picker
// receives: a Korean query built from a ko-KR rendered label and an en-US
// label-name query must each find their row, and the queries the e2e smoke
// types must keep selecting the same first row.
func TestSettingsSearchTextCoversLocalizedLabels(t *testing.T) {
	t.Parallel()

	keybindings := []string{settingsNavKeybindings}
	scenarios := []settingsSearchScenario{
		{name: "ko-KR", locale: "ko-KR", inject: map[string][]string{"usage-claude": {"Claude Weekly complete"}}},
		{name: "default", locale: "en-US", inject: map[string][]string{
			"usage-claude":           {"Claude Weekly complete"},
			"detail-project-sidebar": {"Keybinding complete"},
		}},
		{name: "weekly-off", locale: "en-US", files: map[string]string{settingsSearchWeeklyOffConfig: "off\n"},
			within: []string{settingsNavStatusBar + ".agent-usage-hud"}, inject: map[string][]string{"usage-claude": {"Claude Weekly complete"}}},
		{name: "keymap-cr", locale: "en-US", files: map[string]string{"keymap.toml": settingsSearchKeymapCR}, within: keybindings,
			inject: map[string][]string{"detail-project-sidebar": {"Keybinding complete", "Keybinding cancelled"}, "key-detail-cr": {"Test delivery complete"}}},
		{name: "keymap-cr-seq", locale: "en-US", files: map[string]string{"keymap.toml": settingsSearchKeymapCRSeq}, within: keybindings,
			inject: map[string][]string{"detail-project-sidebar": {"Keybinding complete"}}},
		{name: "keymap-unbound", locale: "en-US", files: map[string]string{"keymap.toml": settingsSearchKeymapUnbound}, within: keybindings,
			inject: map[string][]string{"detail-project-sidebar": {"Keybinding complete"}}},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()

			walker := settingsSearchWalk(t, scenario)
			if scenario.within == nil {
				settingsSearchAssertCoverage(t, walker)
			}
			settingsSearchAssertRows(t, walker)
			settingsSearchAssertE2EFirstRows(t, walker)
		})
	}
}

func settingsSearchAssertCoverage(t *testing.T, walker *settingsSearchWalker) {
	t.Helper()

	reached := map[string]bool{}
	searchable := 0
	for _, frame := range walker.frames {
		reached[frame.node] = true
		if settingsSearchSearchable(frame.options) {
			searchable++
		}
	}
	reached[settingsNavScopeGlobal] = reached["(root)"]
	reached[settingsNavScopeProject] = reached[settingsNavInternalTabProject]
	if searchable < settingsSearchFrameFloor {
		t.Errorf("%s: walk captured %d searchable frames, want at least %d", walker.scenario.name, searchable, settingsSearchFrameFloor)
	}
	for _, node := range settingsNodeCatalog {
		if node.Hidden || (node.Kind != settingsNavView && node.Kind != settingsNavChoice) {
			continue
		}
		_, exempt := settingsSearchUnreachedNodes[node.ID]
		switch {
		case !reached[node.ID] && !exempt:
			t.Errorf("%s: catalog %s node %q was not reached by the walk", walker.scenario.name, node.Kind, node.ID)
		case reached[node.ID] && exempt:
			t.Errorf("%s: catalog node %q is reached; drop its stale exemption", walker.scenario.name, node.ID)
		}
	}
}

func settingsSearchAssertRows(t *testing.T, walker *settingsSearchWalker) {
	t.Helper()

	var misses []string
	rows := 0
	for _, frame := range walker.frames {
		if !settingsSearchSearchable(frame.options) {
			continue
		}
		items := frame.options.Items
		for _, item := range items {
			if settingsSearchIsFeedbackRow(item) {
				continue
			}
			label := settingsSearchRenderedLabel(item)
			where := fmt.Sprintf("%s %q row %q", frame.options.UI, frame.options.Prompt, item.Value)
			if walker.scenario.locale == "ko-KR" {
				word, phrase := settingsSearchKoreanQueries(label)
				if word == "" {
					continue
				}
				rows++
				for _, query := range []string{word, phrase} {
					if !settingsSearchFinds(items, query, item) {
						misses = append(misses, fmt.Sprintf("%s: Korean query %q", where, query))
					}
				}
				continue
			}
			name := settingsSearchLabelName(label)
			if name == "" {
				continue
			}
			rows++
			for _, query := range []string{name, strings.ToLower(name)} {
				if !settingsSearchFinds(items, query, item) {
					misses = append(misses, fmt.Sprintf("%s: label query %q", where, query))
				}
			}
		}
	}
	if rows == 0 {
		t.Fatalf("%s: no rendered rows were checked", walker.scenario.name)
	}
	if len(misses) > 0 {
		t.Errorf("%s: %d of %d rendered-label queries miss their row; first: %s", walker.scenario.name, len(misses), rows, strings.Join(misses[:min(len(misses), 10)], "; "))
	}
}

func settingsSearchAssertE2EFirstRows(t *testing.T, walker *settingsSearchWalker) {
	t.Helper()

	for _, step := range settingsSearchE2ESteps {
		if step.scenario != walker.scenario.name {
			continue
		}
		var matched []settingsSearchCapture
		for _, frame := range walker.frames {
			if frame.feedback == step.feedback && settingsSearchE2EViews[step.view](frame.options) {
				matched = append(matched, frame)
			}
		}
		if len(matched) != 1 {
			t.Errorf("%s: e2e View %q with feedback %q matched %d frames, want 1", step.scenario, step.view, step.feedback, len(matched))
			continue
		}
		got := "(no row)"
		if survivors := intpicker.FilterItems(matched[0].options.Items, step.query); len(survivors) > 0 {
			got = survivors[0].Value
		}
		if got != step.want {
			t.Errorf("%s: e2e query %q in View %q (feedback %q) selects %q first, want %q", step.scenario, step.query, step.view, step.feedback, got, step.want)
		}
	}
}

func TestSettingsSearchTextJoinsRenderedLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		entry intpickercompat.Entry
		want  string
	}{
		{
			name:  "CSI colour label is stripped and joined",
			entry: intpickercompat.Entry{Label: "\x1b[38;5;81m▸\x1b[0m  상태 표시줄  \x1b[2mon - default\x1b[0m", Value: "appearance:status-bar", SearchKey: "status bar components"},
			want:  "status bar components ▸  상태 표시줄  on - default",
		},
		{
			name:  "bare escape bytes are dropped",
			entry: intpickercompat.Entry{Label: "Git\x1bM  branch\x1b", Value: "git", SearchKey: "git branch working tree"},
			want:  "git branch working tree Git  branch",
		},
		{
			name:  "empty key stays empty",
			entry: intpickercompat.Entry{Label: "▸  프로젝트", Value: "section:project"},
			want:  "",
		},
		{
			name:  "blank key stays blank",
			entry: intpickercompat.Entry{Label: "▸  프로젝트", Value: "section:project", SearchKey: "   "},
			want:  "   ",
		},
		{
			name:  "label already in key is not repeated",
			entry: intpickercompat.Entry{Label: "\x1b[1mBack\x1b[0m", Value: settingsBackValue, SearchKey: "go Back to parent"},
			want:  "go Back to parent",
		},
		{
			name:  "escape-only label adds nothing",
			entry: intpickercompat.Entry{Label: "\x1b[0m", Value: settingsNoopValue, SearchKey: "noop"},
			want:  "noop",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			input := []intpickercompat.Entry{tt.entry}
			original := slices.Clone(input)
			once := withSettingsRenderedLabelSearchText(intpickercompat.Options{Entries: input})
			if !slices.Equal(input, original) {
				t.Fatalf("input entries mutated: %#v, want %#v", input, original)
			}
			got := once.Entries[0].SearchKey
			if got != tt.want {
				t.Fatalf("SearchKey = %q, want %q", got, tt.want)
			}
			if strings.ContainsRune(got, '\x1b') {
				t.Fatalf("SearchKey %q contains an escape byte", got)
			}
			if !strings.HasPrefix(got, tt.entry.SearchKey) {
				t.Fatalf("SearchKey %q does not keep the original key %q verbatim", got, tt.entry.SearchKey)
			}
			if strings.TrimSpace(tt.entry.SearchKey) != "" && !strings.Contains(got, stripSettingsLabelANSI(strings.TrimSpace(tt.entry.Label))) {
				t.Fatalf("SearchKey %q does not contain the stripped label", got)
			}
			twice := withSettingsRenderedLabelSearchText(once)
			if twice.Entries[0].SearchKey != got {
				t.Fatalf("second join = %q, want idempotent %q", twice.Entries[0].SearchKey, got)
			}
		})
	}
}
