package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// These tests are the Automation / Notification ownership contract. Phase 0
// froze where the rows live; the assertions here are about which product
// concept owns which row, so a future refactor cannot quietly merge a user
// script with a Provider integration, or a live Pane attention with a durable
// Notification.

// settingsOwnershipDiagnostics is the notify diagnostic set used across these
// tests: the three Agent Providers plus the tmux bell producer, which is
// deliberately not a Provider.
func settingsOwnershipDiagnostics() []doctorAINotifyIntegration {
	claude := aiprovider.IntegrationCommand(string(aiprovider.Claude))
	codex := aiprovider.IntegrationCommand(string(aiprovider.Codex))
	antigravity := aiprovider.IntegrationCommand(string(aiprovider.Antigravity))
	return []doctorAINotifyIntegration{
		{
			ID: "claude-hooks", Name: "Claude hooks", ProviderID: aiHookProviderClaude,
			Status: doctorAINotifyStatusInstalled, ConfigPath: "/home/tester/.claude/settings.json",
			InstallCommand: claude,
			RemoveCommand:  claude + " --remove",
			DryRunCommand:  claude + " --dry-run",
		},
		{
			ID: "codex-hooks", Name: "Codex hooks", ProviderID: aiHookProviderCodex,
			Status: doctorAINotifyStatusConflict, ConfigPath: "/home/tester/.codex/config.toml",
			ConflictReason: "unmanaged notify command",
			InstallCommand: codex,
			RemoveCommand:  codex + " --remove",
			DryRunCommand:  codex + " --dry-run",
		},
		{
			ID: "antigravity-hooks", Name: "Antigravity hooks", ProviderID: aiHookProviderAntigravity,
			Status: doctorAINotifyStatusMissing, ConfigPath: "/home/tester/.gemini/config/hooks.json",
			InstallCommand: antigravity,
			RemoveCommand:  antigravity + " --remove",
			DryRunCommand:  antigravity + " --dry-run",
		},
		{
			ID: settingsTmuxBellDiagnosticID, Name: "tmux bell",
			Status: doctorAINotifyStatusInstalled, Guidance: "run projmux setup to wire the bell",
		},
	}
}

func settingsOwnershipCommand(t *testing.T, home, configHome string) *settingsCommand {
	t.Helper()

	return &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "XDG_CONFIG_HOME" {
				return configHome
			}
			return ""
		},
		ai:                  testAICommand(home),
		aiNotifyDiagnostics: settingsOwnershipDiagnostics,
	}
}

func settingsOwnershipLabels(entries []intpickercompat.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, stripANSI(entry.Label))
	}
	return out
}

// TestSettingsAutomationAndProviderIntegrationReadAsDifferentConcepts is the
// first acceptance criterion: the Settings label alone has to tell a
// Projmux-owned user script apart from Provider wiring. Neither container may
// borrow the other's vocabulary.
func TestSettingsAutomationAndProviderIntegrationReadAsDifferentConcepts(t *testing.T) {
	t.Parallel()

	cmd := settingsOwnershipCommand(t, t.TempDir(), t.TempDir())

	automation := strings.Join(settingsOwnershipLabels(cmd.automationEntries()), "\n")
	for _, want := range []string{"Projmux session lifecycle", "After notification queued", "Project automation policy"} {
		if !strings.Contains(automation, want) {
			t.Fatalf("Automation labels = %q, want %q", automation, want)
		}
	}
	// Automation is user scripts and the policy that gates them. A Provider
	// name appearing here would make wiring look like something the user
	// authors.
	for _, forbidden := range []string{"Claude", "Codex", "Antigravity", "Provider", "integration"} {
		if strings.Contains(automation, forbidden) {
			t.Fatalf("Automation labels = %q, must not name Provider wiring %q", automation, forbidden)
		}
	}

	notifications := strings.Join(settingsOwnershipLabels(cmd.notificationsEntries()), "\n")
	for _, want := range []string{"Desktop delivery", "Provider Integrations", "tmux event source", "Agent event behavior"} {
		if !strings.Contains(notifications, want) {
			t.Fatalf("Notifications labels = %q, want %q", notifications, want)
		}
	}
	// Conversely, Notifications never claims the lifecycle scripts.
	for _, forbidden := range []string{"session lifecycle", "Project automation policy", "[hooks."} {
		if strings.Contains(notifications, forbidden) {
			t.Fatalf("Notifications labels = %q, must not name user automation %q", notifications, forbidden)
		}
	}
}

// TestSettingsProviderInventoryIsTheProviderEnumOnly pins the Provider
// inventory to Claude/Codex/Antigravity and keeps the tmux bell out of it. The
// bell is an event source, so it lives at its own destination with its own
// wiring state.
func TestSettingsProviderInventoryIsTheProviderEnumOnly(t *testing.T) {
	t.Parallel()

	cmd := settingsOwnershipCommand(t, t.TempDir(), t.TempDir())

	var providerIDs []string
	for _, diag := range cmd.notifyProviderDiagnostics() {
		providerIDs = append(providerIDs, diag.ID)
	}
	want := []string{"claude-hooks", "codex-hooks", "antigravity-hooks"}
	if strings.Join(providerIDs, ",") != strings.Join(want, ",") {
		t.Fatalf("Provider inventory = %v, want %v", providerIDs, want)
	}

	providerRows := strings.Join(settingsOwnershipLabels(cmd.notifyDiagnosticCollectionEntries(cmd.notifyProviderDiagnostics())), "\n")
	if strings.Contains(providerRows, "tmux bell") {
		t.Fatalf("Provider Integrations rows = %q, must not list the tmux bell producer", providerRows)
	}

	sourceRows := strings.Join(settingsOwnershipLabels(cmd.tmuxEventSourceEntries()), "\n")
	if !strings.Contains(sourceRows, "Bell wiring status") {
		t.Fatalf("tmux event source rows = %q, want the bell wiring state", sourceRows)
	}
	if !strings.Contains(sourceRows, "not an Agent Provider") {
		t.Fatalf("tmux event source rows = %q, want the row to say the bell is not a Provider", sourceRows)
	}
	for _, forbidden := range []string{"Claude", "Codex", "Antigravity"} {
		if strings.Contains(sourceRows, forbidden) {
			t.Fatalf("tmux event source rows = %q, must not list Provider %q", sourceRows, forbidden)
		}
	}
}

// TestSettingsAgentEventBehaviorUsesTheSameProviderEnum keeps the two Provider
// destinations in step: a Provider that can be wired can also have its event
// behavior configured, so neither view may ship a Provider the other is
// missing.
func TestSettingsAgentEventBehaviorUsesTheSameProviderEnum(t *testing.T) {
	t.Parallel()

	cmd := settingsOwnershipCommand(t, t.TempDir(), t.TempDir())

	var rendered []string
	for _, entry := range cmd.aiHookProviderEntries() {
		if after, ok := strings.CutPrefix(entry.Value, settingsActionPrefixAIHookProvider); ok {
			rendered = append(rendered, after)
		}
	}
	want := settingsAgentEventProviders()
	if strings.Join(rendered, ",") != strings.Join(want, ",") {
		t.Fatalf("Agent event behavior providers = %v, want %v", rendered, want)
	}

	wiring := map[string]bool{}
	for _, diag := range cmd.notifyProviderDiagnostics() {
		wiring[diag.ProviderID] = true
	}
	for _, provider := range want {
		if !wiring[provider] {
			t.Fatalf("Provider %q has event behavior but no wiring destination: %v", provider, wiring)
		}
	}
}

// TestSettingsProviderWiringAndEventBehaviorStaySeparate is the destination
// split: install/conflict/source belongs to Provider Integrations, and the
// notify/state/quiet projection belongs to Agent event behavior. Neither
// surface offers the other's controls.
func TestSettingsProviderWiringAndEventBehaviorStaySeparate(t *testing.T) {
	t.Parallel()

	cmd := settingsOwnershipCommand(t, t.TempDir(), t.TempDir())

	diag, ok := cmd.aiNotifyDiagnosticByID("codex-hooks")
	if !ok {
		t.Fatal("codex-hooks diagnostic missing")
	}
	wiring := aiNotifyDiagnosticDetailEntriesLocale(i18n.FallbackLocale, diag)
	wiringText := strings.Join(settingsOwnershipLabels(wiring), "\n")
	for _, want := range []string{"Status", "Conflict", "Config path", "Install command", "Remove command", "Check integration"} {
		if !strings.Contains(wiringText, want) {
			t.Fatalf("Provider wiring detail = %q, want %q", wiringText, want)
		}
	}
	for _, entry := range wiring {
		if strings.HasPrefix(entry.Value, settingsActionPrefixAIHookSet) {
			t.Fatalf("Provider wiring detail = %#v, must not own an event behavior choice", entry)
		}
	}

	behavior := cmd.aiHookEventEntries(aiHookProviderCodex)
	behaviorText := strings.Join(settingsOwnershipLabels(behavior), "\n")
	for _, forbidden := range []string{"Install command", "Remove command", "Conflict", "Check integration"} {
		if strings.Contains(behaviorText, forbidden) {
			t.Fatalf("Agent event behavior = %q, must not own provider wiring row %q", behaviorText, forbidden)
		}
	}
	for _, entry := range behavior {
		if strings.HasPrefix(entry.Value, settingsActionPrefixAINotifyCommand) ||
			strings.HasPrefix(entry.Value, settingsActionPrefixAINotifyCheck) {
			t.Fatalf("Agent event behavior = %#v, must not own a provider wiring action", entry)
		}
	}
}

// TestSettingsSendNotiAndExternalSenderAreNotAlternatives is the parity check
// with `[hooks.send-noti]` and `PROJMUX_NOTIFY_HOOK` set at the same time. Both
// are configured, both keep their own value, and each row says from its own
// side that it does not replace the other.
func TestSettingsSendNotiAndExternalSenderAreNotAlternatives(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := t.TempDir()
	configPath := filepath.Join(configHome, "projmux", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`
[hooks.send-noti]
run = "echo queued"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_CONFIG_HOME":
				return configHome
			case "PROJMUX_NOTIFY_HOOK":
				return "/usr/local/bin/my-sender"
			default:
				return ""
			}
		},
		ai:                  testAICommand(home),
		aiNotifyDiagnostics: settingsOwnershipDiagnostics,
	}

	// Both settings resolve independently: the external sender does not clear
	// the fan-out command, and the fan-out command does not change the sender.
	command, gotPath, _ := cmd.hookEventState(hookScopeGlobal, string(hooks.EventSendNoti))
	if command != "echo queued" {
		t.Fatalf("send-noti command = %q, want %q", command, "echo queued")
	}
	if gotPath != configPath {
		t.Fatalf("send-noti config path = %q, want %q", gotPath, configPath)
	}

	desktop := strings.Join(settingsOwnershipLabels(cmd.desktopNotifyEntries()), "\n")
	if !strings.Contains(desktop, "/usr/local/bin/my-sender") {
		t.Fatalf("Desktop delivery = %q, want the external sender value", desktop)
	}
	if !strings.Contains(desktop, settingsExternalSenderBoundary) {
		t.Fatalf("Desktop delivery = %q, want the sender row to disclaim the [hooks.send-noti] fan-out", desktop)
	}
	if strings.Contains(desktop, "echo queued") {
		t.Fatalf("Desktop delivery = %q, must not render the Automation fan-out command", desktop)
	}

	sendNoti := strings.Join(settingsOwnershipLabels(cmd.hookEventDetailEntries(hookScopeGlobal, string(hooks.EventSendNoti))), "\n")
	if !strings.Contains(sendNoti, "run = echo queued") {
		t.Fatalf("After notification queued = %q, want the declared command", sendNoti)
	}
	if !strings.Contains(sendNoti, settingsSendNotiBoundary) {
		t.Fatalf("After notification queued = %q, want the row to disclaim the desktop sender", sendNoti)
	}
	if strings.Contains(sendNoti, "PROJMUX_NOTIFY_HOOK") {
		t.Fatalf("After notification queued = %q, must not render the desktop sender env", sendNoti)
	}

	// The session lifecycle events are unaffected by either setting.
	for _, event := range settingsAutomationLifecycleEvents {
		if got, _, _ := cmd.hookEventState(hookScopeGlobal, event); got != "" {
			t.Fatalf("%s command = %q, want empty", event, got)
		}
	}
}

// TestSettingsAutomationEventScopeMatrix walks automation event × scope. Every
// pair is reachable exactly once, carries its own config path, and the project
// scope additionally carries the trust state that gates execution.
func TestSettingsAutomationEventScopeMatrix(t *testing.T) {
	t.Parallel()

	cmd := settingsOwnershipCommand(t, t.TempDir(), t.TempDir())

	events := append(append([]string{}, settingsAutomationLifecycleEvents...), string(hooks.EventSendNoti))
	for _, scope := range []string{hookScopeGlobal, hookScopeProject} {
		for _, event := range events {
			entries := cmd.hookEventDetailEntries(scope, event)
			text := strings.Join(settingsOwnershipLabels(entries), "\n")
			if scope == hookScopeProject {
				// No project context in this fixture: the detail must say so
				// instead of silently rendering a global value.
				if !strings.Contains(text, "no project context") {
					t.Fatalf("%s/%s detail = %q, want the missing-project reason", scope, event, text)
				}
				continue
			}
			if !strings.Contains(text, "[hooks."+event+"]") {
				t.Fatalf("%s/%s detail = %q, want the technical config key preserved", scope, event, text)
			}
			if !strings.Contains(text, "Command") {
				t.Fatalf("%s/%s detail = %q, want the command state row", scope, event, text)
			}
		}
	}

	// The lifecycle list owns exactly the three session events; `send-noti` is a
	// sibling of the lifecycle view, never a fourth member of it.
	lifecycle := cmd.hookLifecycleEntries(hookScopeGlobal)
	var lifecycleEvents []string
	for _, entry := range lifecycle {
		if scope, event, ok := parseSettingsHookEventValue(entry.Value); ok && scope == hookScopeGlobal {
			lifecycleEvents = append(lifecycleEvents, event)
		}
	}
	if strings.Join(lifecycleEvents, ",") != strings.Join(settingsAutomationLifecycleEvents, ",") {
		t.Fatalf("lifecycle events = %v, want %v", lifecycleEvents, settingsAutomationLifecycleEvents)
	}
	if !hasEntryValue(cmd.automationEntries(), settingsAutomationSendNoti) {
		t.Fatalf("Automation entries = %#v, want After notification queued as a sibling row", cmd.automationEntries())
	}
}

// TestSettingsAgentEventBehaviorMatrix walks provider × event behavior. Each
// provider reaches its own event list, and each event reaches the four-way
// Default / Notify / State only / Quiet choice.
func TestSettingsAgentEventBehaviorMatrix(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := settingsOwnershipCommand(t, home, t.TempDir())

	for _, provider := range settingsAgentEventProviders() {
		events := cmd.aiHookEventEntries(provider)
		var names []string
		for _, entry := range events {
			if p, event, ok := parseAIHookSettingsPair(strings.TrimPrefix(entry.Value, settingsActionPrefixAIHookEvent)); ok &&
				strings.HasPrefix(entry.Value, settingsActionPrefixAIHookEvent) {
				if p != provider {
					t.Fatalf("provider %q event row carries provider %q", provider, p)
				}
				names = append(names, event)
			}
		}
		if len(names) == 0 {
			t.Fatalf("provider %q has no reachable events", provider)
		}
		for _, event := range names {
			choices := cmd.aiHookActionChoiceEntries(provider, event)
			var actions []string
			for _, entry := range choices {
				if !strings.HasPrefix(entry.Value, settingsActionPrefixAIHookSet) {
					continue
				}
				p, e, action, ok := parseAIHookSettingsTriple(strings.TrimPrefix(entry.Value, settingsActionPrefixAIHookSet))
				if !ok || p != provider || e != event {
					t.Fatalf("provider %q event %q choice %#v is misaddressed", provider, event, entry)
				}
				actions = append(actions, action)
			}
			want := []string{"default", aiHookActionNotify, aiHookActionState, aiHookActionQuiet}
			if strings.Join(actions, ",") != strings.Join(want, ",") {
				t.Fatalf("provider %q event %q actions = %v, want %v", provider, event, actions, want)
			}
		}
	}
}

// TestSettingsNotifyDiagnosticCheckIsObservableAndReadOnly proves the Check
// row is a real action rather than a placeholder: it reports the status it
// observed and writes nothing while doing so.
func TestSettingsNotifyDiagnosticCheckIsObservableAndReadOnly(t *testing.T) {
	t.Parallel()

	configHome := t.TempDir()
	cmd := settingsOwnershipCommand(t, t.TempDir(), configHome)
	cmd.runCommand = func(string, ...string) error {
		t.Fatal("a wiring check must not execute external commands")
		return nil
	}

	before := settingsNavConfigSnapshot(t, configHome)
	var stdout, stderr bytes.Buffer
	if err := cmd.runNotifyDiagnosticCheck("codex-hooks", &stdout, &stderr); err != nil {
		t.Fatalf("runNotifyDiagnosticCheck() error = %v", err)
	}
	if cmd.feedback == nil {
		t.Fatal("check produced no observable result")
	}
	if !strings.Contains(cmd.feedback.Summary, "Codex hooks") {
		t.Fatalf("check feedback summary = %q, want the diagnostic name", cmd.feedback.Summary)
	}
	for _, want := range []string{"conflict", "/home/tester/.codex/config.toml", "unmanaged notify command"} {
		if !strings.Contains(cmd.feedback.Detail, want) {
			t.Fatalf("check feedback detail = %q, want %q", cmd.feedback.Detail, want)
		}
	}
	if after := settingsNavConfigSnapshot(t, configHome); after != before {
		t.Fatalf("a wiring check wrote config:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}

	if err := cmd.runNotifyDiagnosticCheck("nope", &stdout, &stderr); err == nil {
		t.Fatal("runNotifyDiagnosticCheck() on an unknown id = nil, want error")
	}
}

// TestSettingsAttentionStaysSeparateFromNotificationQueue keeps live Pane
// attention and the durable queue apart. Attention presentation is an
// Appearance concern; nothing under Notifications may call a live Pane badge a
// Notification.
func TestSettingsAttentionStaysSeparateFromNotificationQueue(t *testing.T) {
	t.Parallel()

	cmd := settingsOwnershipCommand(t, t.TempDir(), t.TempDir())

	surfaces := [][]intpickercompat.Entry{
		cmd.notificationsEntries(),
		cmd.desktopNotifyEntries(),
		cmd.tmuxEventSourceEntries(),
		cmd.aiHookProviderEntries(),
		cmd.aiHookEventEntries(aiHookProviderClaude),
		cmd.notifyDiagnosticCollectionEntries(cmd.notifyProviderDiagnostics()),
	}
	for _, entries := range surfaces {
		text := strings.Join(settingsOwnershipLabels(entries), "\n")
		for _, forbidden := range []string{"attention badge", "Attention badge", "badge style"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("Notifications surface = %q, must not own live Pane attention row %q", text, forbidden)
			}
		}
	}

	// The badge style keeps its Appearance home.
	appearance := strings.Join(settingsOwnershipLabels(cmd.statusbarEntries()), "\n")
	if !strings.Contains(appearance, "Agent attention badge style") {
		t.Fatalf("Appearance labels = %q, want the attention badge style row", appearance)
	}
}

// TestSettingsHookQuietPolicyVocabularyIsRetired is the forbidden-term guard.
// `Hook quiet policy` described an implementation layer rather than a product
// concept; the destination is now `Agent event behavior`, and the retired
// spelling must not survive as display vocabulary in either locale.
func TestSettingsHookQuietPolicyVocabularyIsRetired(t *testing.T) {
	t.Parallel()

	const retired = "Hook quiet policy"

	for literal := range uiTextKeys {
		if strings.Contains(strings.ToLower(literal), strings.ToLower(retired)) {
			t.Fatalf("UI text catalog still registers retired copy %q", literal)
		}
	}

	home := t.TempDir()
	for _, locale := range []i18n.Locale{i18n.FallbackLocale, i18n.Locale("ko-KR")} {
		cmd := settingsOwnershipCommand(t, home, t.TempDir())
		cmd.lookupEnv = func(name string) string {
			if name == "LANG" {
				return string(locale)
			}
			return ""
		}
		surfaces := [][]intpickercompat.Entry{
			cmd.notificationsEntries(),
			cmd.aiHookProviderEntries(),
			cmd.aiHookEventEntries(aiHookProviderClaude),
			cmd.automationEntries(),
		}
		for _, entries := range surfaces {
			for _, label := range settingsOwnershipLabels(entries) {
				if strings.Contains(strings.ToLower(label), strings.ToLower(retired)) {
					t.Fatalf("locale %s still renders retired copy in %q", locale, label)
				}
			}
		}
	}

	// The term stays in the negative guard list so a future change cannot
	// reintroduce it unnoticed.
	found := false
	for _, legacy := range settingsNavRemovedVisibleCopy {
		if legacy == retired {
			found = true
		}
	}
	if !found {
		t.Fatalf("retired copy %q is no longer guarded by settingsNavRemovedVisibleCopy", retired)
	}
}
