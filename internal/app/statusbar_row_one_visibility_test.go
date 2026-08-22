package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/systemstatus"
	"github.com/crevissepartners/projmux/internal/theme"
)

func TestStatusbarRowOneVisibilityDefaultSavedInvalidAndSource(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	paths, err := config.Homes{HomeDir: home}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range statusbarRowOneComponents {
		t.Run(string(component), func(t *testing.T) {
			state := loadStatusbarRowOneVisibilityState(func() (string, error) { return home, nil }, func(string) string { return "" }, component)
			if state.Effective != config.StatusbarVisibilityOn || state.Source != config.StatusbarVisibilitySourceDefault || state.Saved != "" || state.Invalid != "" {
				t.Fatalf("missing state = %#v, want on/default", state)
			}
			path, ok := statusbarRowOneVisibilityPath(paths, component)
			if !ok {
				t.Fatalf("component %s has no path", component)
			}
			if err := config.SaveStatusbarVisibilityFile(path, config.StatusbarVisibilityOff); err != nil {
				t.Fatal(err)
			}
			state = loadStatusbarRowOneVisibilityState(func() (string, error) { return home, nil }, func(string) string { return "" }, component)
			if state.Effective != config.StatusbarVisibilityOff || state.Source != config.StatusbarVisibilitySourceSaved || state.Saved != "off" {
				t.Fatalf("saved state = %#v, want off/saved", state)
			}
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("saved mode = %v, %v; want 0600", info, err)
			}
			if err := os.WriteFile(path, []byte("sometimes\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			state = loadStatusbarRowOneVisibilityState(func() (string, error) { return home, nil }, func(string) string { return "" }, component)
			if state.Effective != config.StatusbarVisibilityOn || state.Source != config.StatusbarVisibilitySourceDefault || state.Invalid != "sometimes" {
				t.Fatalf("invalid state = %#v, want on/default with invalid projection", state)
			}
		})
	}
	if _, ok := statusbarRowOneVisibilityPath(paths, statusbarRowOneComponent("resources")); ok {
		t.Fatal("Resources gained a duplicate row-one visibility file")
	}
}

func TestStatusbarRowOneDefaultAndIndependentSegmentOutput(t *testing.T) {
	t.Parallel()

	bin := "'/tmp/projmux'"
	roles := theme.RenderRolesFromEffective(theme.ResolveTheme(theme.ThemeConfig{}))
	defaults := defaultStatusbarRowOneVisibilitySet()
	wantLeft := statusbarSessionLeftFormat(bin, roles)
	if got := statusbarRowOneProjectFormat(bin, roles, defaults); got != wantLeft {
		t.Fatalf("default Project segment = %q, want exact %q", got, wantLeft)
	}
	wantRight := statusbarCwdSegmentFormat(roles) +
		"#[fg=" + roles.DividerFg + "]  " +
		"#[range=user|git]#(" + bin + " internal status git)#[norange]" +
		"#[fg=" + roles.StatusTextSecondary + "]   %Y-%m-%d %H:%M " +
		statusbarSettingsButton(statusbarSettingsIcon, roles)
	if got := statusbarRowOneRightFormat(bin, roles, config.LiveResourcesOff, defaults, statusbarSettingsIcon); got != wantRight {
		t.Fatalf("default row-one right = %q, want exact %q", got, wantRight)
	}

	tests := []struct {
		name      string
		mutate    func(*statusbarRowOneVisibilitySet)
		forbidden []string
	}{
		{name: "working-directory", mutate: func(v *statusbarRowOneVisibilitySet) { v.WorkingDirectory = config.StatusbarVisibilityOff }, forbidden: []string{"range=user|pwd", "pane_current_path", "#[fg=" + roles.DividerFg + "]  "}},
		{name: "git", mutate: func(v *statusbarRowOneVisibilitySet) { v.Git = config.StatusbarVisibilityOff }, forbidden: []string{"range=user|git", "status git"}},
		{name: "clock", mutate: func(v *statusbarRowOneVisibilitySet) { v.Clock = config.StatusbarVisibilityOff }, forbidden: []string{"", "%Y-%m-%d", "%H:%M"}},
		{name: "settings-launcher", mutate: func(v *statusbarRowOneVisibilitySet) { v.SettingsLauncher = config.StatusbarVisibilityOff }, forbidden: []string{"range=user|settings", statusbarSettingsIcon}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			visibility := defaults
			test.mutate(&visibility)
			got := statusbarRowOneRightFormat(bin, roles, config.LiveResourcesOff, visibility, statusbarSettingsIcon)
			for _, forbidden := range test.forbidden {
				if strings.Contains(got, forbidden) {
					t.Fatalf("hidden %s retained %q in %q", test.name, forbidden, got)
				}
			}
			if strings.Contains(strings.ToLower(got), "kube") {
				t.Fatalf("hidden %s generated retired Kube residue: %q", test.name, got)
			}
		})
	}

	projectOff := defaults
	projectOff.Project = config.StatusbarVisibilityOff
	if got := statusbarRowOneProjectFormat(bin, roles, projectOff); got != "" || strings.Contains(got, "range=user|session") {
		t.Fatalf("Project off = %q, want no range or residue", got)
	}
	resourcesOn := statusbarRowOneRightFormat(bin, roles, config.LiveResourcesOn, defaults, statusbarSettingsIcon)
	if !strings.Contains(resourcesOn, "#[range=user|resources]#("+bin+" internal status resources)#[norange]") {
		t.Fatalf("Resources on row = %q, want range and sampler job", resourcesOn)
	}
	resourcesOff := statusbarRowOneRightFormat(bin, roles, config.LiveResourcesOff, defaults, statusbarSettingsIcon)
	if strings.Contains(resourcesOff, "resources") {
		t.Fatalf("Resources off row retains range/job residue: %q", resourcesOff)
	}
}

func TestStatusbarRowOneIconOffKeepsWorkingDirectoryAndGitText(t *testing.T) {
	t.Parallel()

	effective := theme.ResolveTheme(theme.ThemeConfig{})
	generated := tmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibility(
		"/tmp/projmux",
		statusbarDecorationSet{Cwd: config.StatusbarDecorationOff, Git: config.StatusbarDecorationOff},
		config.AIBadgeStyleDot,
		config.DefaultDesktopNotifyMode,
		config.LiveResourcesOff,
		defaultStatusbarHUDVisibilitySet(),
		defaultStatusbarRowOneVisibilitySet(),
		defaultKeyBindingCatalog(),
		false,
		effective,
	)
	for _, want := range []string{"range=user|pwd", "pane_current_path", "range=user|git", "internal status git"} {
		if !strings.Contains(generated, want) {
			t.Fatalf("icon-off generated config lost text segment %q", want)
		}
	}
	for _, option := range []string{
		"set -g " + statusbarDecorationCwdTmuxOption + " off",
		"set -g " + statusbarDecorationGitTmuxOption + " off",
	} {
		if !strings.Contains(generated, option) {
			t.Fatalf("icon-off generated config lacks %q", option)
		}
	}
}

func TestGeneratedAppAndStandaloneStatusbarHaveNoKubeSurface(t *testing.T) {
	t.Parallel()

	visibility := defaultStatusbarRowOneVisibilitySet()
	visibility.Project = config.StatusbarVisibilityOff
	visibility.Git = config.StatusbarVisibilityOff
	visibility.Clock = config.StatusbarVisibilityOff
	effective := theme.ResolveTheme(theme.ThemeConfig{})
	configs := map[string]string{
		"standalone": tmuxStandaloneConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibility(
			"/tmp/projmux", statusbarDecorationSet{}, config.AIBadgeStyleDot, config.DefaultDesktopNotifyMode,
			config.LiveResourcesOn, defaultStatusbarHUDVisibilitySet(), visibility, defaultKeyBindingCatalog(), false, effective),
		"app": tmuxAppConfigWithKeymapThemeAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibility(
			"/tmp/projmux", "/bin/sh", statusbarDecorationSet{}, config.AIBadgeStyleDot, config.DefaultDesktopNotifyMode,
			config.LiveResourcesOn, defaultStatusbarHUDVisibilitySet(), visibility, defaultKeyBindingCatalog(), false, effective),
	}
	for surface, generated := range configs {
		for _, absent := range []string{"range=user|session", "range=user|git", "internal status git", " %Y-%m-%d %H:%M", "range=user|kube", "status kube"} {
			if strings.Contains(generated, absent) {
				t.Fatalf("%s mixed config retains %q", surface, absent)
			}
		}
		for _, present := range []string{"range=user|pwd", "range=user|resources", "range=user|settings"} {
			if !strings.Contains(generated, present) {
				t.Fatalf("%s mixed config lacks %q", surface, present)
			}
		}
	}
}

func TestStatusbarRowOneSettingsRowsAndLiveApply(t *testing.T) {
	if !systemstatus.Supported() {
		t.Skip("Resources settings are unavailable on this platform")
	}
	home := t.TempDir()
	configHome := filepath.Join(home, ".config")
	var calls [][]string
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			switch name {
			case "HOME":
				return home
			case "XDG_CONFIG_HOME":
				return configHome
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
	rows := cmd.statusBarEntries()
	for _, component := range []statusbarRowOneComponent{statusbarRowOneProject, statusbarRowOneClock, statusbarRowOneSettingsLauncher} {
		if !hasEntryValue(rows, settingsActionPrefixHUDVisibility+string(component)+":off") {
			t.Fatalf("default Status Bar rows lack %s off action: %#v", component, rows)
		}
	}
	for target, component := range map[statusbarDecorationTarget]statusbarRowOneComponent{
		statusbarDecorationTargetCwd: statusbarRowOneWorkingDirectory,
		statusbarDecorationTargetGit: statusbarRowOneGit,
	} {
		detail := cmd.statusbarDecorationTargetEntries(target)
		if !hasEntryValue(detail, settingsActionPrefixHUDVisibility+string(component)+":off") || !hasEntryValue(detail, statusbarDecorationIconValue(target)) {
			t.Fatalf("%s detail does not own Visible and Icon: %#v", target, detail)
		}
	}

	for _, component := range statusbarRowOneComponents {
		action := settingsActionPrefixHUDVisibility + string(component) + ":off"
		if err := cmd.execute(action, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
			t.Fatalf("hide %s: %v", component, err)
		}
	}
	paths, err := config.Homes{HomeDir: home, ConfigHome: configHome}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(paths.ConfigDir, "tmux.conf")
	if len(calls) != len(statusbarRowOneComponents) {
		t.Fatalf("live calls = %#v, want one per mutation", calls)
	}
	for _, call := range calls {
		if len(call) != 3 || call[0] != "tmux" || call[1] != "source-file" || call[2] != configPath {
			t.Fatalf("live call = %#v, want exact generated source-file", call)
		}
	}
	generated, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(generated)
	for _, residue := range []string{"range=user|session", "range=user|pwd", "range=user|git", "range=user|settings", "pane_current_path", " %Y-%m-%d %H:%M", "range=user|kube", "status kube"} {
		if strings.Contains(text, residue) {
			t.Fatalf("all-hidden generated config retains %q", residue)
		}
	}
	// Hiding the mouse launcher must not touch the default Settings keybinding.
	if !strings.Contains(text, "bind-key -n M-5") || !strings.Contains(text, "ai-split-settings") {
		t.Fatal("Settings launcher off removed keybinding Settings entry")
	}
	if route, ok := cli.LookupRoute("settings"); !ok || route.Hidden {
		t.Fatalf("Settings launcher off lost public CLI route: route=%#v ok=%v", route, ok)
	}
	if handler, ok := New().routeHandlers()["settings"]; !ok || handler == nil {
		t.Fatal("Settings launcher off lost CLI Settings handler")
	}
}

func TestStatusResourcesOffDoesNotMutateSamplerCache(t *testing.T) {
	if !systemstatus.Supported() {
		t.Skip("live resources unsupported on this build")
	}
	home := t.TempDir()
	stateHome := filepath.Join(home, "state")
	paths, err := config.Homes{HomeDir: home, StateHome: stateHome}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.LiveResourcesSampleFile()), 0o700); err != nil {
		t.Fatal(err)
	}
	const sentinel = "sampler-cache-sentinel\n"
	if err := os.WriteFile(paths.LiveResourcesSampleFile(), []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := &statusCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "XDG_STATE_HOME" {
				return stateHome
			}
			return ""
		},
		readCommand: func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist },
	}
	if err := cmd.runResources(nil, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(paths.LiveResourcesSampleFile())
	if err != nil || string(got) != sentinel {
		t.Fatalf("Resources off cache = %q, %v; want unchanged sentinel", got, err)
	}
}
