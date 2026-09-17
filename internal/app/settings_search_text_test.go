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
	{"settings-projects-sidebar", settingsSearchExact(settingsSidebarStartupPickerDetail), settingsSearchFixedNode(settingsNavProjectsSidebar + ".closed-startup")},
	{"settings-projects-sidebar", settingsSearchExact(settingsRuntimeDiagnosticsVisibilityDetail), settingsSearchFixedNode(settingsNavProjectsSidebar + ".runtime-diagnostics")},
	{"settings-notifications-desktop", settingsSearchExact(settingsActionPrefixDesktopNotifyMode + "choose"), settingsSearchFixedNode(settingsNavNotifyDesktop + ".mode")},
	{"settings-theme-global", settingsSearchExact(themeAction("preset")), settingsSearchFixedNode(settingsNavAppearanceTheme + ".preset")},
	{"settings-theme-global", settingsSearchExact(themeAction("tokens")), settingsSearchFixedNode(settingsNavAppearanceTheme + ".tokens")},
	{"settings-theme-tokens", settingsSearchPrefix(themeAction("group:")), settingsSearchFixedNode(settingsNavAppearanceTheme + ".tokens")},
	{"settings-theme-token-group", settingsSearchPrefix(themeAction("color:")), settingsSearchFixedNode(settingsNavAppearanceTheme + ".tokens.item")},
	{"settings-statusbar-detail", func(value string) bool {
		return strings.HasPrefix(value, settingsActionPrefixStatusbar) && strings.HasSuffix(value, ":icon")
	}, func(parent string) string { return parent + ".icon" }},
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
// settingsSearchFixture seeds the temporary HOME every Settings tree walk runs
// against and wires the refusing seams: no command, no tmux write, no Registry.
// `writes` records every refused attempt so a caller can prove none happened.
func settingsSearchFixture(t *testing.T, scenario settingsSearchScenario) (cmd *settingsCommand, home string, writes *[]string) {
	t.Helper()

	home = t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "source", "repos", "app", ".git"), 0o755); err != nil {
		t.Fatalf("seed project marker: %v", err)
	}
	for name, content := range scenario.files {
		writeFile(t, filepath.Join(home, ".config", "projmux", name), content)
	}
	store := &stubSwitchPinStore{set: pins.Set{Format: pins.FormatTyped}.
		With(pins.Pin{Kind: pins.KindProject, Value: "proj-seeded"}).
		With(pins.Pin{Kind: pins.KindCandidate, Value: filepath.Join(home, "source", "candidate")})}
	cmd = settingsNavTestCommand(t, home)
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
	recorded := &[]string{}
	cmd.runCommand = func(name string, args ...string) error {
		*recorded = append(*recorded, name+" "+strings.Join(args, " "))
		return errors.New("settings search walk refuses commands")
	}
	cmd.runOutput = func(name string, args ...string) ([]byte, error) {
		*recorded = append(*recorded, name+" "+strings.Join(args, " "))
		return nil, errors.New("settings search walk refuses commands")
	}
	cmd.tmuxRunner = settingsDirectionalTmuxRunnerFunc(func(_ context.Context, name string, args ...string) ([]byte, error) {
		if len(args) == 0 || !slices.Contains(settingsSearchReadOnlyTmux, args[0]) {
			*recorded = append(*recorded, name+" "+strings.Join(args, " "))
		}
		return nil, errors.New("settings search walk refuses tmux")
	})
	return cmd, home, recorded
}

func settingsSearchWalk(t *testing.T, scenario settingsSearchScenario) *settingsSearchWalker {
	t.Helper()

	cmd, home, recorded := settingsSearchFixture(t, scenario)
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
	if len(*recorded) > 0 {
		t.Fatalf("%s: settings walk attempted writes: %q", scenario.name, *recorded)
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
	// The root-result landing: one deep query at the root, one surviving row,
	// and the Git View opens three levels down with Icon focused.
	{"default", "root", "", "Git Icon", "root-result:global.appearance.status-bar.git.icon"},
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

// TestSettingsSearchTextSplitCWDOptionNamesSelectTheirRow types the config
// name of each New splits start in option into the chooser. Both options share
// one description that mentions the Pane directory, so a join that appended it
// after the Project root key let "split_cwd_from pane" select Project root first
// and Enter applied the sibling.
func TestSettingsSearchTextSplitCWDOptionNamesSelectTheirRow(t *testing.T) {
	t.Parallel()

	seeds := []struct {
		name   string
		files  map[string]string
		source splitCWDSource
	}{
		{"default", nil, splitCWDFromProject},
		{"global-pane", map[string]string{"config.toml": "[ai]\nsplit_cwd_from = \"pane\"\n"}, splitCWDFromPane},
	}
	for _, locale := range []string{"en-US", "ko-KR"} {
		for _, seed := range seeds {
			name := locale + "-" + seed.name
			t.Run(name, func(t *testing.T) {
				t.Parallel()

				walker := settingsSearchWalk(t, settingsSearchScenario{name: name, locale: locale, files: seed.files, within: []string{settingsNavAISplitCWD}})
				if got := walker.cmd.currentSplitCWDFrom().Source; got != seed.source {
					t.Fatalf("%s: seeded split start source = %q, want %q", name, got, seed.source)
				}
				var chooser []settingsSearchCapture
				for _, frame := range walker.frames {
					if frame.options.UI == "settings-ai-split-cwd-from" {
						chooser = append(chooser, frame)
					}
				}
				if len(chooser) != 1 {
					t.Fatalf("%s: New splits start in chooser matched %d frames, want 1", name, len(chooser))
				}
				for _, source := range []splitCWDSource{splitCWDFromProject, splitCWDFromPane} {
					query := "split_cwd_from " + string(source)
					want := settingsActionPrefixAISplitCWD + string(source)
					got := "(no row)"
					if survivors := intpicker.FilterItems(chooser[0].options.Items, query); len(survivors) > 0 {
						got = survivors[0].Value
					}
					if got != want {
						t.Errorf("%s: New splits start in query %q selects %q first, want %q", name, query, got, want)
					}
				}
			})
		}
	}
}

// settingsSearchPreJoinKey recovers the SearchKey a row builder wrote before
// runPicker's rendered-label join. The join delimits the builder key with tabs
// ("key\tafter" or "before\tkey\tafter"); the earlier join form, key + " " +
// label, is still recognised so the guard below measures that rule too.
func settingsSearchPreJoinKey(item intpicker.Item) string {
	text := item.SearchText
	if strings.TrimSpace(text) == "" {
		return ""
	}
	switch fields := strings.Split(text, "\t"); len(fields) {
	case 2:
		return fields[0]
	case 3:
		return fields[1]
	}
	label := strings.TrimSpace(stripSettingsLabelANSI(item.Label))
	if cut, ok := strings.CutSuffix(text, " "+label); ok && label != "" && !strings.Contains(cut, label) {
		return cut
	}
	return text
}

// TestSettingsSearchTextKeepsOptionNamesOnTheirRow walks every searchable
// Settings View in en-US and ko-KR and types each row's rendered name (en-US
// also in lowercase). When that row is the first survivor over the pre-join
// texts, it must still be the first survivor over the joined rows the native
// picker receives. The pre-join text of a keyed row is its builder SearchKey
// alone, and separately its name column + " " + SearchKey; a row without a
// SearchKey keeps its picker text. The join may make a row newly first, but it
// may never move Enter off a row the typed name already selected.
func TestSettingsSearchTextKeepsOptionNamesOnTheirRow(t *testing.T) {
	t.Parallel()

	for _, scenario := range []settingsSearchScenario{
		{name: "en-US", locale: "en-US", inject: map[string][]string{
			"usage-claude":           {"Claude Weekly complete"},
			"detail-project-sidebar": {"Keybinding complete"},
		}},
		{name: "ko-KR", locale: "ko-KR", inject: map[string][]string{"usage-claude": {"Claude Weekly complete"}}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()

			settingsSearchAssertOptionNamesKeepFirstRow(t, settingsSearchWalk(t, scenario))
		})
	}
}

func settingsSearchAssertOptionNamesKeepFirstRow(t *testing.T, walker *settingsSearchWalker) {
	t.Helper()

	baselines := []string{"SearchKey alone", "name column + SearchKey"}
	violations := make([][]string, len(baselines))
	var lostKeyMatches []string
	frames, queries := 0, 0
	for _, frame := range walker.frames {
		items := frame.options.Items
		// A View without a keyed row stays in the picker's scored mode and the
		// join does not touch it.
		if !settingsSearchSearchable(frame.options) || !slices.ContainsFunc(items, func(item intpicker.Item) bool { return strings.TrimSpace(item.SearchText) != "" }) {
			continue
		}
		frames++
		baseItems := make([][]intpicker.Item, len(baselines))
		for i, item := range items {
			texts := make([]string, len(baselines))
			if key := settingsSearchPreJoinKey(item); strings.TrimSpace(key) != "" {
				texts[0] = key
				texts[1] = settingsSearchLabelName(settingsSearchRenderedLabel(item)) + " " + key
			} else {
				texts[0] = item.EffectiveSearchText()
				texts[1] = texts[0]
			}
			for b, text := range texts {
				if strings.TrimSpace(text) != "" {
					baseItems[b] = append(baseItems[b], intpicker.Item{Value: fmt.Sprint(i), SearchText: text})
				}
			}
		}
		// Keyed Views keep input order, so survivors map back to row indexes by
		// walking the rows once.
		survivorIndexes := func(query string) []int {
			survivors := intpicker.FilterItems(items, query)
			var indexes []int
			for i, item := range items {
				if len(indexes) < len(survivors) {
					next := survivors[len(indexes)]
					if item.Value == next.Value && item.Label == next.Label && item.SearchText == next.SearchText {
						indexes = append(indexes, i)
					}
				}
			}
			return indexes
		}
		baseIndexes := func(b int, query string) []int {
			var indexes []int
			for _, survivor := range intpicker.FilterItems(baseItems[b], query) {
				var index int
				fmt.Sscan(survivor.Value, &index)
				indexes = append(indexes, index)
			}
			return indexes
		}
		for i, item := range items {
			name := settingsSearchLabelName(settingsSearchRenderedLabel(item))
			if name == "" || settingsSearchIsFeedbackRow(item) {
				continue
			}
			typed := []string{name}
			if walker.scenario.locale != "ko-KR" && strings.ToLower(name) != name {
				typed = append(typed, strings.ToLower(name))
			}
			for _, query := range typed {
				queries++
				after := survivorIndexes(query)
				first := -1
				if len(after) > 0 {
					first = after[0]
				}
				for b := range baselines {
					before := baseIndexes(b, query)
					if b == 0 {
						// Every row the builder SearchKey finds is still found.
						for _, index := range before {
							if !slices.Contains(after, index) {
								lostKeyMatches = append(lostKeyMatches, fmt.Sprintf("%s %q: %q no longer finds %q", frame.options.UI, frame.options.Prompt, query, items[index].Value))
							}
						}
					}
					if len(before) == 0 || before[0] != i || first == i {
						continue
					}
					got := "(no row)"
					if first >= 0 {
						got = items[first].Value
					}
					violations[b] = append(violations[b], fmt.Sprintf("%s %q: %q selects %q instead of %q", frame.options.UI, frame.options.Prompt, query, got, item.Value))
				}
			}
		}
	}
	if frames == 0 || queries == 0 {
		t.Fatalf("%s: no keyed View or name query was checked", walker.scenario.name)
	}
	if len(lostKeyMatches) > 0 {
		t.Errorf("%s: %d typed option names no longer find a row their builder SearchKey found; first: %s",
			walker.scenario.name, len(lostKeyMatches), strings.Join(lostKeyMatches[:min(len(lostKeyMatches), 8)], "; "))
	}
	for b, found := range violations {
		if len(found) > 0 {
			t.Errorf("%s: %d of %d typed option names over %d keyed Views lose their first row against pre-join %s; first: %s",
				walker.scenario.name, len(found), queries, frames, baselines[b], strings.Join(found[:min(len(found), 8)], "; "))
		}
	}
}

// TestSettingsSearchTextJoinGuardsLaterOptionNames pins the guard on small row
// lists: a label column that would let a row match an option name a later row
// already selects goes before the key instead, is joined word by word, or is
// left out, and the typed names keep their first row.
func TestSettingsSearchTextJoinGuardsLaterOptionNames(t *testing.T) {
	t.Parallel()

	const splitDescription = "Pane directory is used only inside the Project root; the CLI does not follow this setting"
	tests := []struct {
		name    string
		entries []intpickercompat.Entry
		want    []string
		first   map[string]string
	}{
		{
			name: "a shared description does not hand the sibling name to the row above",
			entries: []intpickercompat.Entry{
				{Label: "←  Back", Value: settingsBackValue},
				{Label: "·  New splits start in    Project root  (default)", Value: settingsNoopValue},
				{Label: "◉  Project root              " + splitDescription, Value: "ai-split-cwd:project", SearchKey: "new splits start in split_cwd_from project"},
				{Label: "○  Current Pane directory    " + splitDescription, Value: "ai-split-cwd:pane", SearchKey: "new splits start in split_cwd_from pane"},
			},
			// The description after the Project root key would supply "pane" to
			// "split_cwd_from pane", which the Current Pane directory row selects,
			// so it goes before the key; the last row has no later row to guard.
			want: []string{
				"",
				"",
				splitDescription + "\tnew splits start in split_cwd_from project\t◉  Project root",
				"new splits start in split_cwd_from pane\t○  Current Pane directory    " + splitDescription,
			},
			first: map[string]string{
				"split_cwd_from pane":    "ai-split-cwd:pane",
				"split_cwd_from project": "ai-split-cwd:project",
				"Current Pane directory": "ai-split-cwd:pane",
			},
		},
		{
			name: "a name column goes before the key when after it would spell a later name",
			entries: []intpickercompat.Entry{
				{Label: "·  Name", Value: settingsNoopValue, SearchKey: "git co"},
				{Label: "▸  Icon", Value: "icon", SearchKey: "icon"},
			},
			// "git co" + "Name" spells "icon"; "Name" + "git co" does not.
			want:  []string{"Name\tgit co\t·", "icon\t▸  Icon"},
			first: map[string]string{"icon": "icon", "Name": settingsNoopValue},
		},
		{
			name: "a column blocked on both sides is joined word by word",
			entries: []intpickercompat.Entry{
				{Label: "◉  PermissionRequest         폴백에서만 - notify - catalog - in-app queue + OS toast supported by specialized handler", Value: "permission-request", SearchKey: "codex PermissionRequest notify catalog quiet notify state"},
				{Label: "◉  Stop                      notify - catalog", Value: "stop", SearchKey: "codex Stop notify catalog quiet notify state"},
			},
			// "OS toast supported" spells "Stop" on either side of the key, so the
			// description is joined word by word: "supported" is left out, two
			// words fit only before the key, and the Korean word still joins. The
			// name column spells "stop" on both sides and the key already carries it.
			want: []string{
				"in-app specialized\tcodex PermissionRequest notify catalog quiet notify state\t◉         폴백에서만 - notify - catalog - queue + OS toast by handler",
				"codex Stop notify catalog quiet notify state\t◉  Stop                      notify - catalog",
			},
			first: map[string]string{"Stop": "stop", "폴백에서만": "permission-request", "PermissionRequest": "permission-request"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			original := slices.Clone(tt.entries)
			once := withSettingsRenderedLabelSearchText(intpickercompat.Options{Entries: tt.entries})
			if !slices.Equal(tt.entries, original) {
				t.Fatalf("input entries mutated")
			}
			for i, entry := range once.Entries {
				if entry.SearchKey != tt.want[i] {
					t.Errorf("row %d SearchKey = %q, want %q", i, entry.SearchKey, tt.want[i])
				}
				if key := original[i].SearchKey; key != "" && settingsSearchPreJoinKey(intpicker.Item{Label: entry.Label, SearchText: entry.SearchKey}) != key {
					t.Errorf("row %d SearchKey %q does not keep the builder key %q as its delimited field", i, entry.SearchKey, key)
				}
			}
			twice := withSettingsRenderedLabelSearchText(once)
			if !slices.Equal(twice.Entries, once.Entries) {
				t.Fatalf("second join changed the rows: %#v, want idempotent %#v", twice.Entries, once.Entries)
			}
			items := intpickercompat.PickerOptions(once).Items
			for query, want := range tt.first {
				got := "(no row)"
				if survivors := intpicker.FilterItems(items, query); len(survivors) > 0 {
					got = survivors[0].Value
				}
				if got != want {
					t.Errorf("query %q selects %q first, want %q", query, got, want)
				}
			}
		})
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
			want:  "status bar components\t▸  상태 표시줄  on - default",
		},
		{
			name:  "bare escape bytes are dropped",
			entry: intpickercompat.Entry{Label: "Git\x1bM  branch\x1b", Value: "git", SearchKey: "git branch working tree"},
			want:  "git branch working tree\tGit  branch",
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

// BenchmarkSettingsSearchTextJoinScale keeps the join's cost visible on a
// keyed View whose rows come from user data, shaped like Pinned Projects: a
// Back row and n keyed rows, each with a unique path or one shared description.
func BenchmarkSettingsSearchTextJoinScale(b *testing.B) {
	for _, shared := range []bool{false, true} {
		for _, n := range []int{50, 200, 500} {
			entries := []intpickercompat.Entry{{Label: "↩  Back", Value: settingsBackValue}}
			for i := range n {
				description := fmt.Sprintf("/home/user/source/repos/project-%03d", i)
				if shared {
					description = "Pinned Project root; opens the project session and focuses the first pane"
				}
				entries = append(entries, intpickercompat.Entry{
					Label:     settingsResolvedLabelLocale("en-US", "▸", "", fmt.Sprintf("project-%03d", i), description),
					Value:     fmt.Sprintf("switch:pin:uid:proj-%03d", i),
					SearchKey: fmt.Sprintf("pinned project switch pin project-%03d", i),
				})
			}
			variant := "unique-description"
			if shared {
				variant = "shared-description"
			}
			b.Run(fmt.Sprintf("rows=%d/%s", n, variant), func(b *testing.B) {
				for b.Loop() {
					withSettingsRenderedLabelSearchText(intpickercompat.Options{Entries: entries})
				}
			})
		}
	}
}
