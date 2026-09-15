package app

import (
	"strings"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
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
	settingsNavProjectSnapshots + ".saved.item",
}

// settingsRootResultInstance is one runtime member of a code-enumerated
// template node. Key identifies the member inside the result value; Label is
// the localized text substituted for the template's <placeholder>.
type settingsRootResultInstance struct {
	Key   string
	Label string
}

// settingsRootResultEntries renders the global result rows for one scope tab.
// The rows are SearchOnly, so they are invisible until the user types.
func settingsRootResultEntries(tab settingsRootTab, locale i18n.Locale) []intpickercompat.Entry {
	axis := settingsAxisGlobal
	scope := settingsNavScopeGlobal
	if tab == settingsRootTabProject {
		axis = settingsAxisProject
		scope = settingsNavScopeProject
	}
	var entries []intpickercompat.Entry
	// The depth-1 children of the scope root are the visible root rows, so the
	// walk starts one level below them and never emits a duplicate category.
	for _, category := range settingsNavChildren(scope) {
		if category.Hidden || category.Axis&axis == 0 {
			continue
		}
		path := []string{settingsRootResultNodeLabel(locale, category)}
		entries = settingsRootResultWalk(entries, locale, axis, category, path, nil)
	}
	return entries
}

func settingsRootResultWalk(entries []intpickercompat.Entry, locale i18n.Locale, axis SettingsAxis, node settingsNavNode, path []string, instances []settingsRootResultInstance) []intpickercompat.Entry {
	for _, child := range settingsNavChildren(node.ID) {
		if child.Hidden || child.Axis&axis == 0 || settingsRootResultExcluded(child.ID) {
			continue
		}
		members, template := settingsRootResultInstances(child.ID, locale, instances)
		if !template {
			// Every other Dynamic node is a single row whose value is supplied
			// at runtime, not a 0..N template: it is emitted once, exactly like
			// a static node.
			childPath := settingsRootResultAppend(path, settingsRootResultNodeLabel(locale, child))
			entries = append(entries, settingsRootResultEntry(child.ID, instances, childPath))
			entries = settingsRootResultWalk(entries, locale, axis, child, childPath, instances)
			continue
		}
		for _, member := range members {
			childPath := settingsRootResultAppend(path, member.Label)
			childInstances := append(append([]settingsRootResultInstance(nil), instances...), member)
			entries = append(entries, settingsRootResultEntry(child.ID, childInstances, childPath))
			entries = settingsRootResultWalk(entries, locale, axis, child, childPath, childInstances)
		}
	}
	return entries
}

func settingsRootResultAppend(path []string, label string) []string {
	return append(append([]string(nil), path...), label)
}

func settingsRootResultExcluded(nodeID string) bool {
	for _, excluded := range settingsRootResultExcludedSubtrees {
		if nodeID == excluded {
			return true
		}
	}
	return false
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
		var out []settingsRootResultInstance
		for _, group := range themeTokenGroups {
			for _, token := range group.Tokens {
				out = append(out, settingsRootResultInstance{
					Key:   string(token),
					Label: settingsCatalogTextLocale(locale, themeColorLabel(token)),
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
