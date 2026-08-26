package app

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	corenotify "github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/i18n"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

func TestStatusbarHUDRowFourVisibilityCombinations(t *testing.T) {
	t.Parallel()

	bin := "'/tmp/projmux'"
	autosave := "#(" + bin + " internal tmux autosave-session-state --quiet)"
	both := statusbarAuxLineFormat(bin, false)
	notifyOnly := "#[align=left range=user|notify]#(" + bin + " internal status notify --max-width #{client_width})#[norange]"
	usageOnly := "#[align=right range=user|usage]#(" + bin + " internal status usage --max-width #{client_width})#[norange]"

	cases := []struct {
		name       string
		visibility statusbarHUDVisibilitySet
		wantAux    string
		wantStatus int
		wantNotify bool
		wantUsage  bool
	}{
		{"both-on", statusbarHUDVisibilitySet{Notifications: config.StatusbarVisibilityOn, AgentUsage: config.StatusbarVisibilityOn}, both, 2, true, true},
		{"notifications-only", statusbarHUDVisibilitySet{Notifications: config.StatusbarVisibilityOn, AgentUsage: config.StatusbarVisibilityOff}, notifyOnly, 2, true, false},
		{"usage-only", statusbarHUDVisibilitySet{Notifications: config.StatusbarVisibilityOff, AgentUsage: config.StatusbarVisibilityOn}, usageOnly, 2, false, true},
		{"all-off", statusbarHUDVisibilitySet{Notifications: config.StatusbarVisibilityOff, AgentUsage: config.StatusbarVisibilityOff}, "", 1, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotAux := statusbarAuxLineFormatWithVisibility(bin, false, tc.visibility)
			if gotAux != tc.wantAux {
				t.Fatalf("aux row = %q, want exact %q", gotAux, tc.wantAux)
			}
			if got := statusbarRowCount(tc.visibility); got != tc.wantStatus {
				t.Fatalf("status row count = %d, want %d", got, tc.wantStatus)
			}
			if got := strings.Contains(gotAux, "range=user|notify"); got != tc.wantNotify {
				t.Fatalf("notify range present = %v, want %v in %q", got, tc.wantNotify, gotAux)
			}
			if got := strings.Contains(gotAux, "range=user|usage"); got != tc.wantUsage {
				t.Fatalf("usage range present = %v, want %v in %q", got, tc.wantUsage, gotAux)
			}
			if !tc.wantNotify && strings.Contains(gotAux, "align=left") {
				t.Fatalf("notify-off row retains left alignment: %q", gotAux)
			}
			if !tc.wantUsage && strings.Contains(gotAux, "align=right") {
				t.Fatalf("usage-off row retains right alignment: %q", gotAux)
			}

			rows := statusbarRowFormatLines(bin, true, tc.visibility)
			joined := strings.Join(rows, "\n")
			if tc.wantStatus == 1 {
				if !strings.Contains(rows[0], statusbarWindowLineFormat()+autosave) {
					t.Fatalf("all-off row 0 must retain Window row plus autosave: %#v", rows)
				}
				if !strings.Contains(joined, "set -gu status-format[1]") || strings.Contains(joined, "range=user|notify") || strings.Contains(joined, "range=user|usage") {
					t.Fatalf("all-off rows retain HUD residue: %#v", rows)
				}
			} else if !strings.Contains(rows[0], tc.wantAux+autosave) {
				t.Fatalf("visible HUD row must carry exact aux output plus autosave: %#v", rows)
			}
		})
	}
}

func TestAgentUsageVisibilityParentChildMatrixPreservesSavedLeaves(t *testing.T) {
	t.Parallel()

	for _, overall := range []config.StatusbarVisibility{config.StatusbarVisibilityOn, config.StatusbarVisibilityOff} {
		for _, provider := range []config.StatusbarVisibility{config.StatusbarVisibilityOn, config.StatusbarVisibilityOff} {
			for _, window := range []config.StatusbarVisibility{config.StatusbarVisibilityOn, config.StatusbarVisibilityOff} {
				name := string(overall) + "/" + string(provider) + "/" + string(window)
				t.Run(name, func(t *testing.T) {
					overallState := config.StatusbarVisibilityState{Effective: overall, Saved: string(overall), Source: config.StatusbarVisibilitySourceSaved}
					providerState := config.StatusbarVisibilityState{Effective: provider, Saved: string(provider), Source: config.StatusbarVisibilitySourceSaved}
					windowState := config.StatusbarVisibilityState{Effective: window, Saved: string(window), Source: config.StatusbarVisibilitySourceSaved}
					gotProvider := gatedStatusbarVisibility(providerState, overallState)
					gotWindow := gatedStatusbarVisibility(windowState, overallState, providerState)
					wantProvider := provider
					if overall == config.StatusbarVisibilityOff {
						wantProvider = config.StatusbarVisibilityOff
					}
					wantWindow := window
					if overall == config.StatusbarVisibilityOff || provider == config.StatusbarVisibilityOff {
						wantWindow = config.StatusbarVisibilityOff
					}
					if gotProvider.Effective != wantProvider || gotWindow.Effective != wantWindow {
						t.Fatalf("effective provider/window = %s/%s, want %s/%s", gotProvider.Effective, gotWindow.Effective, wantProvider, wantWindow)
					}
					if gotProvider.Saved != string(provider) || gotWindow.Saved != string(window) {
						t.Fatalf("parent gate rewrote saved values: provider=%#v window=%#v", gotProvider, gotWindow)
					}
				})
			}
		}
	}
}

func TestAgentUsageSettingsCapabilityOrderRowsAndLocaleProjection(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := settingsNavTestCommand(t, home)
	rows := cmd.agentUsageHUDEntries()
	positions := make([]int, 3)
	for i, provider := range []string{"Claude", "Codex", "Antigravity"} {
		positions[i] = entryLabelIndex(rows, provider)
		if positions[i] < 0 {
			t.Fatalf("missing provider row %s: %#v", provider, rows)
		}
	}
	if !(positions[0] < positions[1] && positions[1] < positions[2]) {
		t.Fatalf("provider order = %v, want UsageSupported declared order", positions)
	}
	if got := cmd.agentUsageProviderEntries("antigravity"); !hasEntryLabelContainingAll(got, "Weekly") || hasEntryLabelContainingAll(got, "5h") {
		t.Fatalf("Antigravity rows = %#v, want Weekly only", got)
	}
	claudeRows := cmd.agentUsageProviderEntries("claude")
	if !hasEntryLabelContainingAll(claudeRows, "5h", "saved on", "effective on", "default") || !hasEntryLabelContainingAll(claudeRows, "Weekly", "saved on", "effective on", "default") {
		t.Fatalf("missing leaf defaults = %#v, want on/default", claudeRows)
	}
	codexRows := cmd.agentUsageProviderEntries("codex")
	if !hasEntryLabelContainingAll(codexRows, "5h", "saved off", "effective off", "default") || !hasEntryLabelContainingAll(codexRows, "Weekly", "saved on", "effective on", "default") ||
		!hasEntryValue(codexRows, settingsActionPrefixHUDVisibility+agentUsageWindowVisibilityAction+":codex:5h:on") {
		t.Fatalf("Codex leaf defaults = %#v, want 5h off/default with on action and Weekly on/default", codexRows)
	}
	paths, err := config.Homes{HomeDir: home, ConfigHome: filepath.Join(home, ".config")}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.StatusbarAgentUsageWindowVisibilityFile("claude", "5h"), []byte("broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveStatusbarVisibilityFile(paths.StatusbarAgentUsageWindowVisibilityFile("codex", "5h"), config.StatusbarVisibilityOn); err != nil {
		t.Fatal(err)
	}
	claudeRows = cmd.agentUsageProviderEntries("claude")
	if !hasEntryLabelContainingAll(claudeRows, "5h", "saved on", "effective on", "default", "invalid saved value ignored") {
		t.Fatalf("invalid leaf projection = %#v", claudeRows)
	}
	codexRows = cmd.agentUsageProviderEntries("codex")
	if !hasEntryLabelContainingAll(codexRows, "5h", "saved on", "effective on", "saved") {
		t.Fatalf("saved Codex 5h override = %#v, want on/saved", codexRows)
	}
	for _, locale := range []i18n.Locale{i18n.FallbackLocale, i18n.Locale("ko-KR")} {
		text := agentUsageVisibilityStateText(locale, config.DefaultStatusbarVisibilityState(), config.DefaultStatusbarVisibilityState())
		invalidState := config.DefaultStatusbarVisibilityState()
		invalidState.Invalid = "broken"
		invalidText := agentUsageVisibilityStateText(locale, invalidState, invalidState)
		overallInfo := settingsLabelInfoLocale(locale, "Current", text, settingsCatalogTextLocale(locale, "ambient status usage projection"))
		providerInfo := settingsLabelInfoLocale(locale, "Current", text, settingsCatalogTextLocale(locale, "ambient provider usage projection")+" - Claude")
		if locale == i18n.Locale("ko-KR") {
			if !strings.Contains(text, "저장됨") || !strings.Contains(text, "적용 중") || !strings.Contains(text, "기본값") || strings.Contains(text, "effective") ||
				!strings.Contains(invalidText, "잘못된 저장값 무시됨") ||
				!strings.Contains(overallInfo, "상태바 사용량 표시") || !strings.Contains(providerInfo, "상태바 제공자 사용량 표시 - Claude") {
				t.Fatalf("Korean state/info text = %q / %q / %q / %q", text, invalidText, overallInfo, providerInfo)
			}
		} else if !strings.Contains(text, "saved") || !strings.Contains(text, "effective") || !strings.Contains(text, "default") ||
			!strings.Contains(invalidText, "invalid saved value ignored") ||
			!strings.Contains(overallInfo, "ambient status usage projection") || !strings.Contains(providerInfo, "ambient provider usage projection - Claude") {
			t.Fatalf("English state/info text = %q / %q / %q / %q", text, invalidText, overallInfo, providerInfo)
		}
	}
}

func TestAgentUsageLeafPersistenceLiveApplyAndParentPreservation(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := filepath.Join(home, ".config")
	stateHome := filepath.Join(home, ".local", "state")
	var calls [][]string
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "HOME" {
				return home
			}
			if name == "XDG_CONFIG_HOME" {
				return configHome
			}
			if name == "XDG_STATE_HOME" {
				return stateHome
			}
			if name == "TMUX" {
				return "/tmp/tmux-test/projmux,1,0"
			}
			return ""
		},
		runCommand: func(name string, args ...string) error {
			calls = append(calls, append([]string{name}, args...))
			return nil
		},
	}
	wireSettingsLiveTestRunner(cmd)
	paths, err := config.Homes{HomeDir: home, ConfigHome: configHome, StateHome: stateHome}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	usagePath := filepath.Join(paths.StateDir, "usage", "snapshots.json")
	if err := os.MkdirAll(filepath.Dir(usagePath), 0o700); err != nil {
		t.Fatal(err)
	}
	usageSentinel := []byte(`{"snapshots":[{"model":"claude","window":"5h","pct":42}],"last_collect":{"claude":"2026-08-16T12:00:00Z"},"backoff":{"claude":{"until":"2026-08-16T13:00:00Z","consecutive":2}}}`)
	if err := os.WriteFile(usagePath, usageSentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.SaveAIEnabledAgentsFile(paths.AIEnabledAgentsFile(), []config.AIAgentProvider{config.AIAgentClaude, config.AIAgentCodex}); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC)
	for _, path := range []string{usagePath, paths.AIEnabledAgentsFile()} {
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	usageBefore := statFileSentinel(t, usagePath)
	enabledBefore := statFileSentinel(t, paths.AIEnabledAgentsFile())
	if err := cmd.setAgentUsageVisibility(agentUsageVisibilityLeaf{provider: "claude", window: "weekly"}, config.StatusbarVisibilityOff); err != nil {
		t.Fatal(err)
	}
	generatedBefore, err := os.ReadFile(filepath.Join(paths.ConfigDir, "tmux.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.setAgentUsageVisibility(agentUsageVisibilityLeaf{provider: "claude"}, config.StatusbarVisibilityOff); err != nil {
		t.Fatal(err)
	}
	generatedAfter, err := os.ReadFile(filepath.Join(paths.ConfigDir, "tmux.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if string(generatedAfter) != string(generatedBefore) {
		t.Fatal("presentation leaf changed generated tmux bytes instead of the status command's ambient projection")
	}
	windowPath := paths.StatusbarAgentUsageWindowVisibilityFile("claude", "weekly")
	windowState, err := config.LoadStatusbarVisibilityFile(windowPath)
	if err != nil || windowState.Effective != config.StatusbarVisibilityOff {
		t.Fatalf("window state = %#v, %v", windowState, err)
	}
	info, err := os.Stat(windowPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("window leaf mode = %v; want 0600", info.Mode().Perm())
	}
	if got := loadAgentUsageVisibilityState(cmd.homeDir, cmd.lookupEnv, agentUsageVisibilityLeaf{provider: "claude", window: "weekly"}); got.Effective != config.StatusbarVisibilityOff || got.Source != config.StatusbarVisibilitySourceSaved {
		t.Fatalf("child after provider off = %#v, want saved off preserved", got)
	}
	if err := cmd.setAgentUsageVisibility(agentUsageVisibilityLeaf{provider: "claude"}, config.StatusbarVisibilityOn); err != nil {
		t.Fatal(err)
	}
	if got := loadAgentUsageVisibilityState(cmd.homeDir, cmd.lookupEnv, agentUsageVisibilityLeaf{provider: "claude", window: "weekly"}); got.Effective != config.StatusbarVisibilityOff || got.Source != config.StatusbarVisibilitySourceSaved {
		t.Fatalf("child after provider restore = %#v, want saved off restored", got)
	}
	if err := cmd.setStatusbarHUDVisibility(statusbarHUDAgentUsage, config.StatusbarVisibilityOff); err != nil {
		t.Fatal(err)
	}
	if err := cmd.setStatusbarHUDVisibility(statusbarHUDAgentUsage, config.StatusbarVisibilityOn); err != nil {
		t.Fatal(err)
	}
	if got := loadAgentUsageVisibilityState(cmd.homeDir, cmd.lookupEnv, agentUsageVisibilityLeaf{provider: "claude", window: "weekly"}); got.Effective != config.StatusbarVisibilityOff || got.Source != config.StatusbarVisibilitySourceSaved {
		t.Fatalf("child after overall restore = %#v, want saved off restored", got)
	}
	if len(calls) != 5 {
		t.Fatalf("live source-file calls = %#v, want 5", calls)
	}
	if got := statFileSentinel(t, usagePath); got != usageBefore {
		t.Fatalf("usage cache/backoff sentinel changed:\n before=%#v\n after=%#v", usageBefore, got)
	}
	if got := statFileSentinel(t, paths.AIEnabledAgentsFile()); got != enabledBefore {
		t.Fatalf("provider enablement sentinel changed:\n before=%#v\n after=%#v", enabledBefore, got)
	}
}

type fileSentinel struct {
	Hash    [32]byte
	Inode   uint64
	Size    int64
	Mode    os.FileMode
	ModTime int64
}

func statFileSentinel(t *testing.T, path string) fileSentinel {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("%s has no syscall.Stat_t", path)
	}
	return fileSentinel{Hash: sha256.Sum256(body), Inode: stat.Ino, Size: info.Size(), Mode: info.Mode().Perm(), ModTime: info.ModTime().UnixNano()}
}

func entryLabelIndex(entries []intpickercompat.Entry, needle string) int {
	for i, entry := range entries {
		if strings.Contains(entry.Label, needle) {
			return i
		}
	}
	return -1
}

func TestStatusbarHUDDefaultGeneratedConfigIsByteIdentical(t *testing.T) {
	t.Parallel()

	effective := fallbackRenderThemeSource().effective
	catalog := defaultKeyBindingCatalog()
	standaloneBefore := tmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeAndLiveResources(
		"/tmp/projmux", statusbarDecorationSet{}, config.AIBadgeStyleDot, config.DefaultDesktopNotifyMode, config.LiveResourcesOff, catalog, false, effective)
	standaloneAfter := tmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndHUDVisibility(
		"/tmp/projmux", statusbarDecorationSet{}, config.AIBadgeStyleDot, config.DefaultDesktopNotifyMode, config.LiveResourcesOff, defaultStatusbarHUDVisibilitySet(), catalog, false, effective)
	if standaloneAfter != standaloneBefore {
		t.Fatal("default standalone config changed when no HUD visibility setting exists")
	}

	appBefore := tmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeAndLiveResources(
		"/tmp/projmux", "/bin/sh", statusbarDecorationSet{}, config.AIBadgeStyleDot, config.DefaultDesktopNotifyMode, config.LiveResourcesOff, catalog, false, effective)
	appAfter := tmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndHUDVisibility(
		"/tmp/projmux", "/bin/sh", statusbarDecorationSet{}, config.AIBadgeStyleDot, config.DefaultDesktopNotifyMode, config.LiveResourcesOff, defaultStatusbarHUDVisibilitySet(), catalog, false, effective)
	if appAfter != appBefore {
		t.Fatal("default app config changed when no HUD visibility setting exists")
	}
}

func TestStatusbarHUDGeneratedAppAndStandaloneRowsConvergeForAllCombinations(t *testing.T) {
	t.Parallel()

	effective := fallbackRenderThemeSource().effective
	catalog := defaultKeyBindingCatalog()
	for _, notifications := range []config.StatusbarVisibility{config.StatusbarVisibilityOn, config.StatusbarVisibilityOff} {
		for _, usage := range []config.StatusbarVisibility{config.StatusbarVisibilityOn, config.StatusbarVisibilityOff} {
			visibility := statusbarHUDVisibilitySet{Notifications: notifications, AgentUsage: usage}
			name := string(notifications) + "-" + string(usage)
			t.Run(name, func(t *testing.T) {
				standalone := tmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndHUDVisibility(
					"/tmp/projmux", statusbarDecorationSet{}, config.AIBadgeStyleDot, config.DefaultDesktopNotifyMode, config.LiveResourcesOff, visibility, catalog, false, effective)
				app := tmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndHUDVisibility(
					"/tmp/projmux", "/bin/sh", statusbarDecorationSet{}, config.AIBadgeStyleDot, config.DefaultDesktopNotifyMode, config.LiveResourcesOff, visibility, catalog, false, effective)
				for surface, generated := range map[string]string{"standalone": standalone, "app": app} {
					if !strings.Contains(generated, "set -g status "+statusbarRowCountOption(visibility)) {
						t.Fatalf("%s config has wrong status row count", surface)
					}
					wantNotify := notifications == config.StatusbarVisibilityOn
					wantUsage := usage == config.StatusbarVisibilityOn
					if got := strings.Contains(generated, "range=user|notify"); got != wantNotify {
						t.Fatalf("%s notify range present = %v, want %v", surface, got, wantNotify)
					}
					if got := strings.Contains(generated, "range=user|usage"); got != wantUsage {
						t.Fatalf("%s usage range present = %v, want %v", surface, got, wantUsage)
					}
					if !wantNotify && !wantUsage {
						if !strings.Contains(generated, "set -gu status-format[1]") {
							t.Fatalf("%s all-off config does not clear stale row 1", surface)
						}
					} else if !strings.Contains(generated, "set -g status-format[1]") {
						t.Fatalf("%s visible-HUD config does not retain the structural row 1", surface)
					}
				}
			})
		}
	}
}

func TestStatusbarHUDSettingsRowsProjectEffectiveSourceAndOppositeAction(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := settingsNavTestCommand(t, home)
	rows := cmd.statusBarEntries()
	if !hasEntryLabelContainingAll(rows, "Notifications HUD", "on", "default") ||
		!hasEntryValue(rows, settingsAppearanceAgentUsageHUD) {
		t.Fatalf("default status bar rows = %#v, want both HUDs on/default with off actions", rows)
	}
	usageRows := cmd.agentUsageHUDEntries()
	if !hasEntryValue(usageRows, settingsActionPrefixHUDVisibility+string(statusbarHUDAgentUsage)+":off") {
		t.Fatalf("default Agent Usage HUD detail = %#v, want Visible off action", usageRows)
	}

	paths, err := config.Homes{HomeDir: home, ConfigHome: filepath.Join(home, ".config")}.Paths()
	if err != nil {
		t.Fatalf("resolve config paths: %v", err)
	}
	if err := os.MkdirAll(paths.ConfigDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	if err := os.WriteFile(paths.StatusbarNotificationsHUDVisibilityFile(), []byte("broken\n"), 0o644); err != nil {
		t.Fatalf("write invalid Notifications visibility: %v", err)
	}
	if err := config.SaveStatusbarVisibilityFile(paths.StatusbarAgentUsageHUDVisibilityFile(), config.StatusbarVisibilityOff); err != nil {
		t.Fatalf("save Agent Usage visibility: %v", err)
	}
	rows = cmd.statusBarEntries()
	if !hasEntryLabelContainingAll(rows, "Notifications HUD", "on", "default", "invalid saved value ignored") {
		t.Fatalf("invalid Notifications row = %#v, want on/default fallback with invalid projection", rows)
	}
	if !hasEntryLabelContainingAll(rows, "Agent Usage HUD", "saved off", "effective off", "saved") ||
		!hasEntryValue(rows, settingsAppearanceAgentUsageHUD) {
		t.Fatalf("saved Agent Usage row = %#v, want off/saved with on action", rows)
	}
	usageRows = cmd.agentUsageHUDEntries()
	if !hasEntryValue(usageRows, settingsActionPrefixHUDVisibility+string(statusbarHUDAgentUsage)+":on") {
		t.Fatalf("saved Agent Usage detail = %#v, want on action", usageRows)
	}
	detail := cmd.statusbarDecorationTargetEntries(statusbarDecorationTargetNotify)
	if !hasEntryValue(detail, settingsActionPrefixHUDVisibility+string(statusbarHUDNotifications)+":off") {
		t.Fatalf("Notifications detail = %#v, want Visible toggle alongside icon choice", detail)
	}
}

func TestStatusbarHUDSettingsPersistenceLiveApplyAndProducerIsolation(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configHome := filepath.Join(home, ".config")
	stateHome := filepath.Join(home, ".local", "state")
	queuePath := filepath.Join(stateHome, "projmux", corenotify.NotifyFileName)
	usagePath := filepath.Join(stateHome, "projmux", "usage", "snapshots.json")
	for path, body := range map[string]string{queuePath: "queue-sentinel\n", usagePath: "usage-sentinel\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir producer state: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write producer state: %v", err)
		}
	}

	var calls [][]string
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "HOME":
				return home
			case "XDG_CONFIG_HOME":
				return configHome
			case "XDG_STATE_HOME":
				return stateHome
			case "TMUX":
				return "/tmp/tmux-test/projmux,1,0"
			default:
				return ""
			}
		},
		runCommand: func(name string, args ...string) error {
			calls = append(calls, append([]string{name}, args...))
			return nil
		},
	}
	wireSettingsLiveTestRunner(cmd)

	if err := cmd.setStatusbarHUDVisibility(statusbarHUDNotifications, config.StatusbarVisibilityOff); err != nil {
		t.Fatalf("hide Notifications HUD: %v", err)
	}
	if err := cmd.setStatusbarHUDVisibility(statusbarHUDAgentUsage, config.StatusbarVisibilityOff); err != nil {
		t.Fatalf("hide Agent Usage HUD: %v", err)
	}
	paths, err := config.Homes{HomeDir: home, ConfigHome: configHome, StateHome: stateHome}.Paths()
	if err != nil {
		t.Fatalf("resolve paths: %v", err)
	}
	for component, path := range map[statusbarHUDComponent]string{
		statusbarHUDNotifications: paths.StatusbarNotificationsHUDVisibilityFile(),
		statusbarHUDAgentUsage:    paths.StatusbarAgentUsageHUDVisibilityFile(),
	} {
		state, err := config.LoadStatusbarVisibilityFile(path)
		if err != nil || state.Effective != config.StatusbarVisibilityOff || state.Source != config.StatusbarVisibilitySourceSaved {
			t.Fatalf("%s persisted state = %#v, %v; want off/saved", component, state, err)
		}
	}
	generated, err := os.ReadFile(filepath.Join(paths.ConfigDir, "tmux.conf"))
	if err != nil {
		t.Fatalf("read generated app config: %v", err)
	}
	text := string(generated)
	if !strings.Contains(text, "set -g status on") || !strings.Contains(text, "set -gu status-format[1]") || strings.Contains(text, "range=user|notify") || strings.Contains(text, "range=user|usage") {
		t.Fatalf("all-off generated config has residue: %s", text)
	}
	if err := cmd.setStatusbarHUDVisibility(statusbarHUDNotifications, config.StatusbarVisibilityOn); err != nil {
		t.Fatalf("show Notifications HUD: %v", err)
	}
	if err := cmd.setStatusbarHUDVisibility(statusbarHUDAgentUsage, config.StatusbarVisibilityOn); err != nil {
		t.Fatalf("show Agent Usage HUD: %v", err)
	}
	generated, err = os.ReadFile(filepath.Join(paths.ConfigDir, "tmux.conf"))
	if err != nil {
		t.Fatalf("read re-enabled app config: %v", err)
	}
	text = string(generated)
	if !strings.Contains(text, "set -g status 2") || !strings.Contains(text, "range=user|notify") || !strings.Contains(text, "range=user|usage") {
		t.Fatalf("re-enabled generated config did not restore both HUDs: %s", text)
	}
	if len(calls) != 4 {
		t.Fatalf("live apply calls = %#v, want one source-file reload per mutation", calls)
	}
	for _, call := range calls {
		if len(call) != 3 || call[0] != "tmux" || call[1] != "source-file" || call[2] != filepath.Join(paths.ConfigDir, "tmux.conf") {
			t.Fatalf("live apply call = %#v, want exact generated config source-file", call)
		}
	}
	for path, want := range map[string]string{queuePath: "queue-sentinel\n", usagePath: "usage-sentinel\n"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("producer state %s = %q, %v; want unchanged %q", path, got, err, want)
		}
	}
}
