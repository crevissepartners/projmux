package cli

import (
	"bytes"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/aiprovider"
)

// TestSelectiveIsAPickerAdapterAndNeverAMemberOfTheProviderEnum is acceptance
// criterion 3.
//
// The assertion is deliberately two-sided. It is not enough that `selective`
// happens to be absent from a hand-written list: the enum is derived from the
// provider catalog, so the test proves the derivation itself excludes every
// non-provider spelling the legacy `--agent` flag accepted, while the picker
// adapters remain a named, closed concept rather than simply "not a provider".
func TestSelectiveIsAPickerAdapterAndNeverAMemberOfTheProviderEnum(t *testing.T) {
	t.Parallel()

	providers := AgentProviders()
	if !reflect.DeepEqual(providers, []string{"codex", "claude", "antigravity"}) {
		t.Fatalf("agent provider enum = %v, want the three direct providers", providers)
	}

	// The legacy `ai split --agent` values that are not providers.
	for _, token := range []string{"selective", "resume", "shell"} {
		if IsAgentProvider(token) {
			t.Fatalf("%q resolved as an Agent provider", token)
		}
		if slices.Contains(providers, token) {
			t.Fatalf("%q is a member of the provider enum", token)
		}
		if slices.Contains(ProviderCreateShortcuts(), token) {
			t.Fatalf("%q owns a provider create shortcut", token)
		}
	}

	// `selective` and `resume` are pickers; `shell` is not a picker either,
	// because a shell surface is a Pane and reaches `create pane`.
	if !reflect.DeepEqual(pickerAdapters, []string{"selective", "resume"}) {
		t.Fatalf("picker adapters = %v, want [selective resume]", pickerAdapters)
	}
	for _, token := range []string{"selective", "Selective", "  resume  "} {
		if !IsPickerAdapter(token) {
			t.Fatalf("%q is not recognized as a picker adapter", token)
		}
	}
	if IsPickerAdapter("shell") {
		t.Fatal("shell was classified as a picker adapter")
	}
	for _, provider := range providers {
		if IsPickerAdapter(provider) {
			t.Fatalf("provider %q was classified as a picker adapter", provider)
		}
	}

	// The two sets never intersect, and every provider really is a catalog
	// entry rather than a string that only looks like one.
	for _, provider := range providers {
		if _, ok := aiprovider.Lookup(provider); !ok {
			t.Fatalf("provider enum member %q is not in the provider catalog", provider)
		}
	}
	for _, adapter := range pickerAdapters {
		if _, ok := aiprovider.Lookup(adapter); ok {
			t.Fatalf("picker adapter %q is registered as a provider", adapter)
		}
	}

	// The mutation the enum must survive: mutating a returned copy cannot
	// smuggle a picker into the enum.
	tampered := AgentProviders()
	tampered[0] = "selective"
	if IsAgentProvider("selective") || slices.Contains(AgentProviders(), "selective") {
		t.Fatal("AgentProviders returned a mutable view of the enum")
	}
}

// TestProviderCreateShortcutsAreOptInAndCollideWithNoKindOrVerb pins the
// shortcut registry rule and the reserved-word collision table.
func TestProviderCreateShortcutsAreOptInAndCollideWithNoKindOrVerb(t *testing.T) {
	t.Parallel()

	shortcuts := ProviderCreateShortcuts()
	if !reflect.DeepEqual(shortcuts, []string{"codex", "claude", "antigravity"}) {
		t.Fatalf("provider create shortcuts = %v, want [codex claude antigravity]", shortcuts)
	}

	// Opt-in: a shortcut exists only because the catalog entry says so, not
	// because the provider exists.
	for _, provider := range aiprovider.All() {
		got := slices.Contains(shortcuts, string(provider.ID))
		if got != provider.CreateShortcut {
			t.Fatalf("provider %q shortcut = %v, want %v from the catalog flag", provider.ID, got, provider.CreateShortcut)
		}
	}

	// Collision: no shortcut may shadow a resource kind or a shared verb.
	for _, shortcut := range shortcuts {
		if slices.Contains(reservedShortcutTokens, shortcut) {
			t.Fatalf("provider shortcut %q collides with a reserved resource kind or shared verb", shortcut)
		}
	}
	for _, reserved := range []string{"agent", "window", "pane", "session", "notification", "snapshot", "create", "delete", "get"} {
		if !slices.Contains(reservedShortcutTokens, reserved) {
			t.Fatalf("%q is not reserved against provider shortcut use", reserved)
		}
		if slices.Contains(shortcuts, reserved) {
			t.Fatalf("reserved token %q is used as a provider shortcut", reserved)
		}
	}

	// The manifest node must list exactly the shortcut set, and must mark every
	// one of them as a shortcut rather than a resource kind.
	create, ok := LookupRoute("create")
	if !ok {
		t.Fatal("create route missing")
	}
	var kinds, marked []string
	for _, child := range create.Children {
		if child.ProviderShortcut {
			marked = append(marked, child.Name)
			continue
		}
		kinds = append(kinds, child.Name)
	}
	if !reflect.DeepEqual(marked, shortcuts) {
		t.Fatalf("create shortcut children = %v, want %v", marked, shortcuts)
	}
	if !reflect.DeepEqual(kinds, []string{"window", "pane", "agent", "notification", "snapshot"}) {
		t.Fatalf("create kind children = %v, want [window pane agent notification snapshot]", kinds)
	}
	// Every shortcut resolves to its own canonical spelling, and that spelling
	// is the normalized `create agent --provider <id>` description rather than a
	// second resource kind.
	for _, shortcut := range shortcuts {
		canonical, ok := LookupCanonicalRoute("create " + shortcut)
		if !ok {
			t.Fatalf("canonical manifest is missing `create %s`", shortcut)
		}
		if !strings.Contains(canonical.Summary, "create agent --provider "+shortcut) {
			t.Fatalf("`create %s` summary = %q, want it to normalize onto create agent", shortcut, canonical.Summary)
		}
	}
}

// TestCreateHelpListsProviderShortcutsApartFromResourceKinds proves the help
// grouping the contract requires: a provider is never rendered as a kind.
func TestCreateHelpListsProviderShortcutsApartFromResourceKinds(t *testing.T) {
	t.Parallel()

	create, _ := LookupRoute("create")
	var b bytes.Buffer
	if err := RenderRouteHelp(&b, []string{"create"}, create); err != nil {
		t.Fatalf("RenderRouteHelp: %v", err)
	}
	out := b.String()

	kindsAt := strings.Index(out, "\nSubcommands:\n")
	shortcutsAt := strings.Index(out, "\nProvider shortcuts:\n")
	if kindsAt < 0 || shortcutsAt < 0 {
		t.Fatalf("create help is missing one of the two groups:\n%s", out)
	}
	if shortcutsAt < kindsAt {
		t.Fatalf("provider shortcuts are listed before the resource kinds:\n%s", out)
	}
	kindBlock := out[kindsAt:shortcutsAt]
	for _, shortcut := range ProviderCreateShortcuts() {
		if strings.Contains(kindBlock, "\n  "+shortcut+" ") {
			t.Fatalf("provider %q is listed among the resource kinds:\n%s", shortcut, out)
		}
		if !strings.Contains(out[shortcutsAt:], "\n  "+shortcut+" ") {
			t.Fatalf("provider %q is missing from the shortcut group:\n%s", shortcut, out)
		}
	}
	for _, kind := range []string{"agent", "pane"} {
		if !strings.Contains(kindBlock, "\n  "+kind+" ") {
			t.Fatalf("resource kind %q is missing from the kind group:\n%s", kind, out)
		}
	}

	// A route with no provider shortcut never grows an empty group.
	agent, _ := LookupRoute("agent")
	var agentHelp bytes.Buffer
	if err := RenderRouteHelp(&agentHelp, []string{"agent"}, agent); err != nil {
		t.Fatalf("RenderRouteHelp(agent): %v", err)
	}
	if strings.Contains(agentHelp.String(), "Provider shortcuts:") {
		t.Fatalf("agent help rendered an empty provider shortcut group:\n%s", agentHelp.String())
	}
}

// TestAgentDomainNamespaceOwnsTheAgentWorkflowSpellings pins the shape of the
// `agent` node and proves the compatibility routes it aliases are untouched.
func TestAgentDomainNamespaceOwnsTheAgentWorkflowSpellings(t *testing.T) {
	t.Parallel()

	route, ok := LookupRoute("agent")
	if !ok {
		t.Fatal("agent route missing")
	}
	if route.Disposition != DispositionCanonical || route.Hidden {
		t.Fatalf("agent route disposition=%q hidden=%v", route.Disposition, route.Hidden)
	}
	var children []string
	for _, child := range route.Children {
		children = append(children, child.Name)
	}
	if want := []string{"status", "topic", "resume", "integrate", "usage"}; !reflect.DeepEqual(children, want) {
		t.Fatalf("agent children = %v, want %v", children, want)
	}

	// Provider account usage is an Agent-domain workflow, not a resource: it is
	// reachable as `agent usage` and there is deliberately no `get usage`.
	usage, ok := LookupCanonicalRoute("agent usage")
	if !ok {
		t.Fatal("canonical manifest is missing `agent usage`")
	}
	for _, source := range []string{"internal", "agent"} {
		if !slices.Contains(usage.Sources, source) {
			t.Fatalf("agent usage sources = %v, want it to name %q", usage.Sources, source)
		}
	}
	if _, ok := LookupCanonicalRoute("get usage"); ok {
		t.Fatal("`get usage` exists; provider quota is not an addressable resource kind")
	}
	get, _ := LookupRoute("get")
	if _, ok := findChild(get, "usage"); ok {
		t.Fatal("the get node grew a usage kind")
	}

	// The two old public spellings remain only as hidden error tombstones.
	for _, token := range []string{"ai", "usage"} {
		compat, ok := LookupRoute(token)
		if !ok {
			t.Fatalf("compatibility route %q was removed", token)
		}
		if compat.Disposition != DispositionCompatibility || !compat.Hidden || !compat.Retired {
			t.Fatalf("route %q = %#v, want a retired compatibility tombstone", token, compat)
		}
	}
	if _, ok := LookupRoute("status"); ok {
		t.Fatal("old top-level `status usage` alias remains")
	}
}
