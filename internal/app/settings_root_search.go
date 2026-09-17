package app

import (
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	"github.com/crevissepartners/projmux/internal/theme"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// The Settings root answers a query with one "path > label" row per catalogued
// setting of the active scope, so a user who knows what a setting is called
// does not have to know which container owns it.
//
// The walk is a pure projection of the static navigation catalog plus the
// in-process inventories the code-enumerated templates expand to. It reads no
// file, no Registry, no snapshot and no tmux state: a result row is a
// destination, not a rendered value, so nothing it shows can depend on runtime
// state. That is why this file is a set of package functions rather than
// settingsCommand methods — the command's seams are simply not in scope here.

// settingsRootResultSeparator joins the localized ancestor labels of a result
// row. It is the single right-pointing angle quotation mark (U+203A), not the
// ASCII greater-than sign.
const settingsRootResultSeparator = " › "

// settingsRootResultInstanceSeparator separates the catalog node ID from the
// instance chain inside a result value, and settingsRootResultInstanceJoin
// separates the instance keys of nested templates (outermost first).
const (
	settingsRootResultInstanceSeparator = "@"
	settingsRootResultInstanceJoin      = "/"
)

// settingsRootResultExcludedSubtrees are the user-data collection item
// templates. Their members are user data rather than settings, so a result row
// for "Pinned Projects > <some project> > Unpin Project" would put a per-item
// mutation in a settings search. The parent Views and the collection-level
// controls stay: those are settings.
var settingsRootResultExcludedSubtrees = []string{
	settingsNavProjectsExtraRoots + ".item",
	settingsNavProjectsPins + ".item",
	settingsNavProjectsCandidates + ".item",
}

// settingsRootResultInstance is one runtime member of a code-enumerated
// template node. Key identifies the member inside the result value; Label is
// the localized text substituted for the template's <placeholder>.
//
// GroupLabel and GroupValue carry a container the live UI renders between the
// template's parent View and the member row but the catalog does not model:
// Theme > Tokens opens a <group> View (Core / Surface / State / Chrome) and
// only that group lists its tokens (runThemeTokensSection, then
// runThemeTokenGroupSection). Adding or moving a catalog node is forbidden, so
// the instance carries the missing level instead, and both the rendered path
// and the landing chain stay exactly what the user clicks through.
type settingsRootResultInstance struct {
	Key        string
	Label      string
	GroupLabel string
	GroupValue string
}

// settingsRootResultEntries renders the global result rows for one scope tab.
// The rows are SearchOnly, so they are invisible until the user types.
func settingsRootResultEntries(tab settingsRootTab, locale i18n.Locale) []intpickercompat.Entry {
	entries, _ := settingsRootResultLandings(tab, locale)
	return entries
}

// settingsRootResultLandings renders the result rows and, in the same walk, the
// destination each one encodes. One walk produces both, so a row can never name
// a destination the walk did not derive, and the map is keyed by the row's own
// picker Value.
func settingsRootResultLandings(tab settingsRootTab, locale i18n.Locale) ([]intpickercompat.Entry, map[string]settingsRootResultLanding) {
	axis := settingsAxisGlobal
	scope := settingsNavScopeGlobal
	if tab == settingsRootTabProject {
		axis = settingsAxisProject
		scope = settingsNavScopeProject
	}
	walk := &settingsRootResultWalker{locale: locale, axis: axis, landings: map[string]settingsRootResultLanding{}}
	// The depth-1 children of the scope root are the visible root rows, so the
	// walk starts one level below them and never emits a duplicate category.
	for _, category := range settingsNavChildren(scope) {
		if category.Hidden || category.Axis&axis == 0 {
			continue
		}
		value, ok := settingsRootResultRowValue(category, nil)
		if !ok {
			continue
		}
		path := []string{settingsRootResultNodeLabel(locale, category)}
		walk.walk(category, path, []string{value}, nil)
	}
	return walk.entries, walk.landings
}

// settingsRootResultLanding is the destination one result row encodes: the
// chain of real picker Values that opens the owning View, and the row to focus
// once it is open.
type settingsRootResultLanding struct {
	// Navigation is the chain of picker Values, from the value pressed on the
	// Settings root frame down to the value that opens the owning View. Every
	// element is the Value a catalogued View node renders, so pressing one can
	// only navigate.
	Navigation []string
	// Focus identifies the target row inside the owning View. It is the row's
	// exact picker Value where the builder emits a fixed one, and its stable
	// leading segment where the value carries the state Enter would apply (a
	// Toggle row's value names the NEXT state, which this walk must not read).
	// Empty means the walk could not name the row by value; the landing then
	// falls back to FocusLabel.
	Focus string
	// FocusLabel is the localized text the target row renders in its name
	// column — the last segment of the path this result row displays. It is the
	// second way to name the same row, for the nodes whose picker Value is the
	// shared no-op sentinel or is only decidable at render time. The landing
	// consults it only after the value target found nothing, and only accepts it
	// when exactly one rendered row carries that name.
	FocusLabel string
}

type settingsRootResultWalker struct {
	locale   i18n.Locale
	axis     SettingsAxis
	entries  []intpickercompat.Entry
	landings map[string]settingsRootResultLanding
}

func (w *settingsRootResultWalker) walk(node settingsNavNode, path, chain []string, instances []settingsRootResultInstance) {
	for _, child := range settingsNavChildren(node.ID) {
		if child.Hidden || child.Axis&w.axis == 0 || settingsRootResultExcluded(child.ID) {
			continue
		}
		members, template := settingsRootResultInstances(child.ID, w.locale, instances)
		if !template {
			// Every other Dynamic node is a single row whose value is supplied
			// at runtime, not a 0..N template: it is emitted once, exactly like
			// a static node.
			w.emit(child, settingsRootResultNodeLabel(w.locale, child), path, chain, instances)
			continue
		}
		for _, member := range members {
			memberPath := path
			memberChain := chain
			if member.GroupLabel != "" {
				memberPath = settingsRootResultAppend(memberPath, member.GroupLabel)
			}
			if member.GroupValue != "" {
				memberChain = settingsRootResultAppend(memberChain, member.GroupValue)
			}
			w.emit(child, member.Label, memberPath, memberChain, append(append([]settingsRootResultInstance(nil), instances...), member))
		}
	}
}

// emit adds one result row and records where it lands. A child of a View node
// is reached by pressing that View's own row, so the chain grows by exactly the
// values whose node is a View and whose row value the walk could name; a node
// the walk cannot name contributes no step, and its descendants land on the
// nearest ancestor View instead of on an invented row.
func (w *settingsRootResultWalker) emit(child settingsNavNode, label string, path, chain []string, instances []settingsRootResultInstance) {
	childPath := settingsRootResultAppend(path, label)
	entry := settingsRootResultEntry(child.ID, instances, childPath)
	w.entries = append(w.entries, entry)

	value, named := settingsRootResultRowValue(child, instances)
	// The label is already in hand: it is the last segment of the path this row
	// renders, so the row and its label target cannot drift apart either.
	w.landings[entry.Value] = settingsRootResultLanding{Navigation: chain, Focus: value, FocusLabel: label}
	childChain := chain
	if named && child.Kind == settingsNavView {
		childChain = settingsRootResultAppend(chain, value)
	}
	w.walk(child, childPath, childChain, instances)
}

func settingsRootResultAppend(path []string, label string) []string {
	return append(append([]string(nil), path...), label)
}

func settingsRootResultExcluded(nodeID string) bool {
	return slices.Contains(settingsRootResultExcludedSubtrees, nodeID)
}

// settingsRootResultNodeLabel is the localized text of one catalog row. Nodes
// that declare a key project through it; the rest fall back to the shared
// literal registry, which is what the row builders themselves render through.
func settingsRootResultNodeLabel(locale i18n.Locale, node settingsNavNode) string {
	label, key := node.entryLabel()
	if key != "" {
		return localizeText(locale, key, label)
	}
	return settingsCatalogTextLocale(locale, label)
}

func settingsRootResultEntry(nodeID string, instances []settingsRootResultInstance, path []string) intpickercompat.Entry {
	label := settingsRootResultLabel(strings.Join(path, settingsRootResultSeparator))
	return intpickercompat.Entry{
		Label: label,
		Value: settingsRootResultValue(nodeID, instances),
		// The key is the rendered label with its escapes removed, so the Phase 0
		// join (withSettingsRenderedLabelSearchText) sees the label already
		// present and leaves the key alone instead of repeating the whole path.
		SearchKey:  stripSettingsLabelANSI(label),
		SearchOnly: true,
	}
}

// settingsRootResultLabel styles a result row like the other Settings rows but
// deliberately does not pad the name: a result row carries a whole path, and
// alignment and truncation belong to the picker's own VisibleLen accounting
// rather than to a second character-width table here.
func settingsRootResultLabel(text string) string {
	return settingsGlyphOpen + "  " + settingsColorType + text + settingsColorReset
}

// settingsRootResultValue encodes the target row so the owning View can be
// resolved from the picker result alone.
func settingsRootResultValue(nodeID string, instances []settingsRootResultInstance) string {
	value := settingsActionPrefixRootResult + nodeID
	if len(instances) == 0 {
		return value
	}
	keys := make([]string, 0, len(instances))
	for _, instance := range instances {
		keys = append(keys, instance.Key)
	}
	return value + settingsRootResultInstanceSeparator + strings.Join(keys, settingsRootResultInstanceJoin)
}

// parseSettingsRootResultValue splits a result value back into the catalog node
// ID and the instance keys of its enclosing templates, outermost first.
//
// TODO(slice 2): this is the seam the landing slice consumes. Slice 2 resolves
// the node ID to its owning View, replays the instance keys down the section
// loops and hands that loop a pending focus (`start:pos(N)`) for the target
// row; selecting a result must still never run the row's control.
func parseSettingsRootResultValue(value string) (nodeID string, instances []string, ok bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(value), settingsActionPrefixRootResult)
	if !found || rest == "" {
		return "", nil, false
	}
	nodeID, chain, hasChain := strings.Cut(rest, settingsRootResultInstanceSeparator)
	if nodeID == "" {
		return "", nil, false
	}
	if hasChain && chain != "" {
		instances = strings.Split(chain, settingsRootResultInstanceJoin)
	}
	return nodeID, instances, true
}

// settingsRootResultInstances expands one code-enumerated template node. The
// second return value distinguishes a template (0..N instances) from every
// other Dynamic node, which is a single runtime-valued row.
//
// Every inventory below is an in-process constant or an embedded default.
// Nothing here consults the on-disk override, the Registry or tmux: the
// diagnostic probes (notifyProviderDiagnostics / doctorAINotifyDiagnostics) and
// the keymap store are deliberately not used.
func settingsRootResultInstances(nodeID string, locale i18n.Locale, enclosing []settingsRootResultInstance) ([]settingsRootResultInstance, bool) {
	switch nodeID {
	case settingsNavAIProviders + ".item":
		return settingsRootResultProviderInstances(locale, aiprovider.SettingsVisible()), true

	case settingsNavNotifyProviders + ".item":
		return settingsRootResultProviderInstances(locale, aiprovider.HookDiagnosticSupported()), true

	case settingsNavNotifyAgentEvents + ".item":
		providers := settingsAgentEventProviders()
		out := make([]settingsRootResultInstance, 0, len(providers))
		for _, provider := range providers {
			out = append(out, settingsRootResultInstance{
				Key:   provider,
				Label: settingsCatalogTextLocale(locale, aiHookProviderLabel(provider)),
			})
		}
		return out, true

	case settingsNavNotifyAgentEvents + ".item.event":
		return settingsRootResultHookEventInstances(locale, settingsRootResultNearestKey(enclosing)), true

	case settingsNavAutomationLifecycle + ".event", settingsNavProjectHooks + ".lifecycle.event":
		out := make([]settingsRootResultInstance, 0, len(settingsAutomationLifecycleEvents))
		for _, event := range settingsAutomationLifecycleEvents {
			out = append(out, settingsRootResultInstance{
				Key:   event,
				Label: settingsCatalogTextLocale(locale, settingsAutomationEventLabel(event)),
			})
		}
		return out, true

	case settingsNavAppearanceTheme + ".tokens.item":
		// Tokens opens a <group> View first and only that group lists its
		// tokens. The catalog does not model the group level and must not gain
		// a node for it, so each token instance carries its group: the result
		// path reads "Theme > Tokens > Core > background", exactly the rows the
		// user clicks, and the landing chain can press the group row.
		var out []settingsRootResultInstance
		for _, group := range themeTokenGroups {
			for _, token := range group.Tokens {
				out = append(out, settingsRootResultInstance{
					Key:        string(token),
					Label:      settingsCatalogTextLocale(locale, themeColorLabel(token)),
					GroupLabel: settingsCatalogTextLocale(locale, themeGroupLabel(group.Prefix)),
					GroupValue: themeAction("group:" + group.Prefix),
				})
			}
		}
		return out, true

	case settingsNavStatusBar + ".agent-usage-hud.provider":
		capabilities := usagecmd.HUDProviderCapabilities()
		out := make([]settingsRootResultInstance, 0, len(capabilities))
		for _, capability := range capabilities {
			out = append(out, settingsRootResultInstance{
				Key:   string(capability.ID),
				Label: settingsCatalogTextLocale(locale, capability.DisplayName),
			})
		}
		return out, true

	case settingsNavStatusBar + ".agent-usage-hud.provider.window":
		provider := settingsRootResultNearestKey(enclosing)
		var out []settingsRootResultInstance
		for _, capability := range usagecmd.HUDProviderCapabilities() {
			if string(capability.ID) != provider {
				continue
			}
			for _, window := range capability.Windows {
				out = append(out, settingsRootResultInstance{
					Key:   window.Key,
					Label: settingsCatalogTextLocale(locale, window.Label),
				})
			}
		}
		return out, true

	case settingsNavKeybindings + "." + keyBindingCategorySurfaces + ".surface":
		members := keybindingActionsInCategory(defaultKeyBindingCatalog(), keyBindingCategorySurfaces)
		var out []settingsRootResultInstance
		for _, surface := range keyBindingSurfaceOrder {
			// A surface with no catalogued member renders no row, exactly as
			// keybindingCategoryEntries skips it.
			if len(keybindingActionsInSurface(members, surface.ID)) == 0 {
				continue
			}
			out = append(out, settingsRootResultInstance{
				Key:   surface.ID,
				Label: settingsCatalogTextLocale(locale, surface.Label),
			})
		}
		return out, true

	case settingsNavKeybindings + "." + keyBindingCategorySurfaces + ".surface.action":
		members := keybindingActionsInCategory(defaultKeyBindingCatalog(), keyBindingCategorySurfaces)
		return settingsRootResultActionInstances(locale, keybindingActionsInSurface(members, settingsRootResultNearestKey(enclosing))), true
	}

	if category, ok := settingsRootResultKeybindingCategory(nodeID); ok {
		return settingsRootResultActionInstances(locale, keybindingActionsInCategory(defaultKeyBindingCatalog(), category)), true
	}
	return nil, false
}

// settingsRootResultKeybindingCategory recognizes the per-category
// `<action detail>` templates, which the catalog declares as
// global.keybindings.<category>.action.
func settingsRootResultKeybindingCategory(nodeID string) (string, bool) {
	rest, ok := strings.CutPrefix(nodeID, settingsNavKeybindings+".")
	if !ok {
		return "", false
	}
	category, ok := strings.CutSuffix(rest, ".action")
	if !ok || category == "" || strings.Contains(category, ".") {
		return "", false
	}
	if _, found := settingsNavByID(settingsNavKeybindings + "." + category); !found {
		return "", false
	}
	return category, true
}

func settingsRootResultActionInstances(locale i18n.Locale, actions []keyBindingAction) []settingsRootResultInstance {
	out := make([]settingsRootResultInstance, 0, len(actions))
	for _, action := range actions {
		out = append(out, settingsRootResultInstance{
			Key:   action.ID,
			Label: settingsCatalogTextLocale(locale, keyBindingDisplayName(action)),
		})
	}
	return out
}

func settingsRootResultProviderInstances(locale i18n.Locale, providers []aiprovider.Metadata) []settingsRootResultInstance {
	out := make([]settingsRootResultInstance, 0, len(providers))
	for _, provider := range providers {
		out = append(out, settingsRootResultInstance{
			Key:   string(provider.ID),
			Label: settingsCatalogTextLocale(locale, provider.DisplayName),
		})
	}
	return out
}

// settingsRootResultHookEventInstances expands the per-provider Agent event
// rows from the embedded default catalog. Codex additionally carries the two
// fixed native semantic rows the view renders above its hook events.
func settingsRootResultHookEventInstances(locale i18n.Locale, provider string) []settingsRootResultInstance {
	var out []settingsRootResultInstance
	if provider == aiHookProviderCodex {
		out = append(out,
			settingsRootResultInstance{
				Key:   string(config.AISemanticApprovalRequired),
				Label: settingsCatalogTextLocale(locale, "Approval required"),
			},
			settingsRootResultInstance{
				Key:   string(config.AISemanticResponseComplete),
				Label: settingsCatalogTextLocale(locale, "Response complete"),
			},
		)
	}
	catalog, err := defaultAIHookCatalog(provider)
	if err != nil {
		return out
	}
	for _, event := range catalog.Events {
		out = append(out, settingsRootResultInstance{Key: event.Name, Label: event.Name})
	}
	return out
}

func settingsRootResultNearestKey(instances []settingsRootResultInstance) string {
	if len(instances) == 0 {
		return ""
	}
	return instances[len(instances)-1].Key
}

// settingsRootResultRowValue names the picker Value the owning builder renders
// for one catalog node. It is the seam between the static IA catalog and the
// twelve section loops: the catalog says where a row belongs, this says what
// the row's value actually is, and every case below was read off the builder
// rather than inferred from the node ID.
//
// The second return value is false when the row's value cannot be named without
// reading runtime state the result walk must not touch (a user path, a saved
// command, a resolved hex). Such a node still gets a result row; it just lands
// on the nearest ancestor View the walk can reach instead of on a guessed row.
//
// Rows whose value carries the state Enter would apply (every Toggle, whose
// value names the NEXT state) return the stable leading segment instead, which
// the landing matches as a prefix.
func settingsRootResultRowValue(node settingsNavNode, instances []settingsRootResultInstance) (string, bool) {
	// A passive State row's value is the shared no-op sentinel, and a single
	// State node routinely stands for several rendered rows ("Effective / Saved
	// / Source"). There is no row to name, so these land on the owning View.
	if node.Value == settingsNoopValue {
		return "", false
	}
	if !node.Dynamic && node.Value != "" {
		return node.Value, true
	}

	key := settingsRootResultNearestKey(instances)
	enclosing := ""
	if len(instances) >= 2 {
		enclosing = instances[len(instances)-2].Key
	}

	switch node.ID {
	// Projects -------------------------------------------------------------
	case settingsNavProjectsSidebar + ".closed-startup":
		return settingsSidebarStartupPickerDetail, true
	case settingsNavProjectsSidebar + ".runtime-diagnostics":
		return settingsRuntimeDiagnosticsVisibilityDetail, true
	case settingsNavProjectsPins + ".pin-current":
		// The row's value ends in the current Project's path, which is runtime
		// state, but the segment in front of it is not and no other row of the
		// Pinned Projects View starts with it, so the prefix rule names the row
		// exactly. The label cannot: the catalog calls the node "Pin current
		// Project" and the builder renders "Add Current Project".
		return settingsActionPrefixSwitch + "add", true

	// AI -------------------------------------------------------------------
	case settingsNavAIProviders + ".item":
		return settingsActionPrefixAIEnabledAgent + key, true

	// Notifications --------------------------------------------------------
	case settingsNavNotifyDesktop + ".mode":
		return settingsActionPrefixDesktopNotifyMode + "choose", true
	case settingsNavNotifyProviders + ".item":
		return settingsActionPrefixAINotifyDiagnostic + key, true
	case settingsNavNotifyProviders + ".item.check":
		return settingsActionPrefixAINotifyCheck + key, true
	case settingsNavNotifyProviders + ".item.setup":
		// The node covers the install / remove / dry-run copy rows; the install
		// row is the first of them (aiNotifyDiagnosticCommandEntryLocale).
		return settingsActionPrefixAINotifyCommand + key + ":install", true
	case settingsNavNotifyTmuxSource + ".check":
		return settingsActionPrefixAINotifyCheck + settingsTmuxBellDiagnosticID, true
	case settingsNavNotifyAgentEvents + ".item":
		return settingsActionPrefixAIHookProvider + key, true
	case settingsNavNotifyAgentEvents + ".item.event":
		// Codex renders its two native semantic rows above the hook events and
		// gives them their own prefix.
		if key == string(config.AISemanticApprovalRequired) || key == string(config.AISemanticResponseComplete) {
			return settingsActionPrefixAISemanticEvent + key, true
		}
		return settingsActionPrefixAIHookEvent + enclosing + ":" + key, true

	// Automation -----------------------------------------------------------
	case settingsNavAutomationLifecycle + ".event":
		return settingsActionPrefixHookEvent + hookScopeGlobal + ":" + key, true
	case settingsNavAutomation + ".project-policy":
		return settingsRootResultTogglePrefix(settingsActionPrefixHooks), true

	// Appearance -----------------------------------------------------------
	case settingsNavAppearanceTheme + ".preset":
		return themeAction("preset"), true
	case settingsNavAppearanceTheme + ".tokens":
		return themeAction("tokens"), true
	case settingsNavAppearanceTheme + ".tokens.item":
		return themeAction("color:" + key), true
	case settingsNavAppearanceTheme + ".tokens.item.set":
		// "Set value" covers the typed-hex row, the 256-colour grid and one
		// preset row per preset; the typed-hex row is the direct editor.
		return themeAction("color-type:" + key), true
	case settingsNavAppearanceTheme + ".tokens.item.fallback":
		// "Use preset fallback" is the "Use preset value" row, rendered only
		// while a preset is saved. Its value is the empty-hex color-set form,
		// ending in the colon with nothing after it (themeColorEntries), and
		// "Terminal default" carries a value after that colon, so the exact
		// value passes it by and its prefix form `…::` matches no row.
		//
		// A built-in preset that leaves the token at the terminal default has
		// no hex, so its "Set <preset>" row renders that same empty-hex value.
		// On those tokens the value names two rows, and with no preset saved
		// only the preset row is left to take the focus. Naming nothing opens
		// the View on its first row instead of on a control the user did not
		// search for.
		if settingsRootResultPresetRepeatsFallback(theme.ColorToken(key)) {
			return "", false
		}
		return themeAction("color-set:" + key + ":"), true
	case settingsNavAppearanceTheme + ".reset":
		return themeAction("reset"), true

	case settingsNavStatusBar + ".notifications-hud.visible":
		return settingsRootResultVisibilityPrefix(string(statusbarHUDNotifications)), true
	case settingsNavStatusBar + ".notifications-hud.icon":
		return settingsActionPrefixStatusbar + string(statusbarDecorationTargetNotify) + ":icon", true
	case settingsNavStatusBar + ".agent-usage-hud.visible":
		return settingsRootResultVisibilityPrefix(string(statusbarHUDAgentUsage)), true
	case settingsNavStatusBar + ".agent-usage-hud.provider":
		return settingsAppearanceAgentUsageProviderPrefix + key, true
	case settingsNavStatusBar + ".agent-usage-hud.provider.visible":
		return settingsRootResultVisibilityPrefix(agentUsageProviderVisibilityAction + ":" + key), true
	case settingsNavStatusBar + ".agent-usage-hud.provider.window":
		return settingsRootResultVisibilityPrefix(agentUsageWindowVisibilityAction + ":" + enclosing + ":" + key), true
	case settingsNavStatusBar + ".project":
		return settingsRootResultVisibilityPrefix(string(statusbarRowOneProject)), true
	case settingsNavStatusBar + ".working-directory.visible":
		return settingsRootResultVisibilityPrefix(string(statusbarRowOneWorkingDirectory)), true
	case settingsNavStatusBar + ".working-directory.icon":
		return settingsActionPrefixStatusbar + string(statusbarDecorationTargetCwd) + ":icon", true
	case settingsNavStatusBar + ".git.visible":
		return settingsRootResultVisibilityPrefix(string(statusbarRowOneGit)), true
	case settingsNavStatusBar + ".git.icon":
		return settingsActionPrefixStatusbar + string(statusbarDecorationTargetGit) + ":icon", true
	case settingsNavStatusBar + ".resources":
		return settingsRootResultTogglePrefix(settingsActionPrefixLiveResources), true
	case settingsNavStatusBar + ".clock":
		return settingsRootResultVisibilityPrefix(string(statusbarRowOneClock)), true
	case settingsNavStatusBar + ".settings-launcher":
		return settingsRootResultVisibilityPrefix(string(statusbarRowOneSettingsLauncher)), true

	// Project scope --------------------------------------------------------
	case settingsNavProjectTrust + ".revoke":
		return settingsTrustUntrust, true
	case settingsNavProjectHooks + ".lifecycle.event":
		return settingsActionPrefixHookEvent + hookScopeProject + ":" + key, true
	case settingsNavProjectHooks + ".lifecycle.event.remove":
		return settingsActionPrefixHookRemove + hookScopeProject + ":" + key, true
	case settingsNavProjectHooks + ".send-noti.remove":
		return settingsActionPrefixHookRemove + hookScopeProject + ":" + string(hooks.EventSendNoti), true
	}

	// Keybindings ----------------------------------------------------------
	switch node.ID {
	case settingsNavKeybindings + "." + keyBindingCategorySurfaces + ".surface":
		return settingsActionPrefixKeymapSurface + key, true
	case settingsNavKeybindings + "." + keyBindingCategorySurfaces + ".surface.action":
		return settingsActionPrefixKeymap + key, true
	}
	// Every other per-category `<action detail>` template is one row in its
	// category View, keyed by the action ID.
	if _, ok := settingsRootResultKeybindingCategory(node.ID); ok {
		return settingsActionPrefixKeymap + key, true
	}
	return "", false
}

// settingsRootResultPresetRepeatsFallback reports whether a built-in preset
// renders its "Set <preset>" row for token with the empty hex, the value the
// "Use preset value" row also carries. It reads the static preset table the
// token View is built from, never the saved theme.
func settingsRootResultPresetRepeatsFallback(token theme.ColorToken) bool {
	for _, preset := range theme.PresetNames() {
		if hex, ok := theme.PresetColorHex(preset, token); ok && hex == "" {
			return true
		}
	}
	return false
}

// settingsRootResultVisibilityPrefix is the leading segment of a Status Bar
// visibility Toggle's value. The full value ends in the state the row would
// apply, which depends on what is saved, so only this part is stable.
func settingsRootResultVisibilityPrefix(component string) string {
	return settingsActionPrefixHUDVisibility + component
}

func settingsRootResultTogglePrefix(prefix string) string {
	return strings.TrimSuffix(prefix, ":")
}
