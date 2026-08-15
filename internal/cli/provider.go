package cli

import (
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/aiprovider"
)

// The canonical Agent namespace deliberately owns two separate closed sets:
// the Provider enum, which names an Agent implementation, and the picker
// adapters, which are interactive selection surfaces that end in a Provider.
//
// The distinction is the point of this file. The legacy `ai split --agent`
// flag mixed both into one spelling, so `selective`, `resume`, and `shell`
// looked like siblings of `codex` and `claude`. In the v2 model:
//
//   - `selective` and `resume` are pickers. They resolve to a Provider and then
//     dispatch the canonical create; they are never a Provider themselves and
//     never appear in a Provider enum, in `--provider`, or as a shortcut.
//   - `shell` is not an Agent at all. A plain shell split creates a Pane, so it
//     lives on the `create pane` route.
//
// Membership therefore comes from the provider catalog rather than from a
// hand-written list, which is what makes "selective is not a provider" a
// structural property instead of a rule someone has to remember.

// PickerAdapterSelective is the interactive Provider picker. It is an adapter
// over the canonical create route, not a Provider.
const PickerAdapterSelective = "selective"

// PickerAdapterResume is the interactive resume-session picker. Like
// PickerAdapterSelective it selects, it does not implement.
const PickerAdapterResume = "resume"

// pickerAdapters is the closed set of interactive selection surfaces that the
// legacy `--agent` flag spelled as if they were providers.
var pickerAdapters = []string{PickerAdapterSelective, PickerAdapterResume}

// IsPickerAdapter reports whether token names an interactive picker adapter.
func IsPickerAdapter(token string) bool {
	return slices.Contains(pickerAdapters, normalizeProviderToken(token))
}

// AgentProviders returns the canonical Agent provider enum in picker order.
//
// It is derived from the provider catalog, so a token that is not a registered
// provider — `selective`, `resume`, `shell` — cannot be a member.
func AgentProviders() []string {
	providers := aiprovider.PickerEligible()
	out := make([]string, 0, len(providers))
	for _, provider := range providers {
		out = append(out, string(provider.ID))
	}
	return out
}

// IsAgentProvider reports whether token is a member of the Agent provider enum.
func IsAgentProvider(token string) bool {
	_, ok := aiprovider.Lookup(normalizeProviderToken(token))
	return ok
}

// ProviderCreateShortcuts returns the provider tokens that own a top-level
// `projmux create <provider>` shortcut, in help order.
func ProviderCreateShortcuts() []string {
	providers := aiprovider.CreateShortcuts()
	out := make([]string, 0, len(providers))
	for _, provider := range providers {
		out = append(out, string(provider.ID))
	}
	return out
}

// reservedShortcutTokens are the resource kinds and shared verbs a provider
// shortcut may never shadow. A provider named `agent` or `delete` would make
// `create agent` ambiguous between a kind and a shortcut, so the catalog audit
// rejects the collision instead of resolving it by precedence.
var reservedShortcutTokens = []string{
	// Resource kinds.
	"project", "window", "pane", "agent", "session", "notification", "snapshot",
	// Shared verbs.
	"get", "describe", "create", "attach", "focus", "rename", "rebind",
	"delete", "restore", "pin", "tag", "prune",
}

func normalizeProviderToken(token string) string {
	return strings.ToLower(strings.TrimSpace(token))
}
