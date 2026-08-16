package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	corenotify "github.com/crevissepartners/projmux/internal/core/notify"
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
		!hasEntryValue(rows, settingsActionPrefixHUDVisibility+string(statusbarHUDAgentUsage)+":off") {
		t.Fatalf("default status bar rows = %#v, want both HUDs on/default with off actions", rows)
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
	if !hasEntryLabelContainingAll(rows, "Agent Usage HUD", "off", "saved") ||
		!hasEntryValue(rows, settingsActionPrefixHUDVisibility+string(statusbarHUDAgentUsage)+":on") {
		t.Fatalf("saved Agent Usage row = %#v, want off/saved with on action", rows)
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
				return "isolated-client"
			default:
				return ""
			}
		},
		runCommand: func(name string, args ...string) error {
			calls = append(calls, append([]string{name}, args...))
			return nil
		},
	}

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
