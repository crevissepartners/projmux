package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/theme"
)

func TestAppRunTmuxPopupPreviewUsesDefaultOptions(t *testing.T) {
	t.Parallel()

	popup := &stubTmuxPopupClient{}
	app := &App{
		tmux: &tmuxCommand{
			popup: popup,
			executable: func() (string, error) {
				return "/tmp/proj mux/bin/projmux", nil
			},
			popupOptions: defaultPopupPreviewOptions,
		},
	}

	var stdout bytes.Buffer
	if err := app.Run([]string{"tmux", "popup-preview", "dev"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	const wantCommand = "exec '/tmp/proj mux/bin/projmux' 'session-popup' 'preview' 'dev'"
	if popup.command != wantCommand {
		t.Fatalf("popup command = %q, want %q", popup.command, wantCommand)
	}

	wantOptions := inttmux.PopupOptions{
		Width:         "80%",
		Height:        "80%",
		CloseBehavior: inttmux.PopupCloseOnExit,
	}
	if !reflect.DeepEqual(popup.options, wantOptions) {
		t.Fatalf("popup options = %#v, want %#v", popup.options, wantOptions)
	}
}

func TestAppRunTmuxPopupSwitchUsesCurrentPanePathAndDefaultOptions(t *testing.T) {
	t.Parallel()

	popup := &stubTmuxPopupClient{currentPanePath: "/tmp/work tree"}
	app := &App{
		tmux: &tmuxCommand{
			popup:       popup,
			executable:  func() (string, error) { return "/tmp/proj mux/bin/projmux", nil },
			switchPopup: defaultPopupSwitchOptions,
		},
	}

	var stdout bytes.Buffer
	if err := app.Run([]string{"tmux", "popup-switch"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	const wantCommand = "cd -- '/tmp/work tree' && exec '/tmp/proj mux/bin/projmux' 'switch' '--ui=popup'"
	if popup.command != wantCommand {
		t.Fatalf("popup command = %q, want %q", popup.command, wantCommand)
	}

	wantOptions := inttmux.PopupOptions{
		Width:  "80%",
		Height: "70%",
		Env: map[string]string{
			hookTrustInlineEnv: "1",
		},
		NoBorder:      true,
		CloseBehavior: inttmux.PopupCloseOnExit,
	}
	if !reflect.DeepEqual(popup.options, wantOptions) {
		t.Fatalf("popup options = %#v, want %#v", popup.options, wantOptions)
	}
}

func TestAppRunTmuxPopupSessionsUsesDefaultOptions(t *testing.T) {
	t.Parallel()

	popup := &stubTmuxPopupClient{}
	app := &App{
		tmux: &tmuxCommand{
			popup:         popup,
			executable:    func() (string, error) { return "/tmp/proj mux/bin/projmux", nil },
			sessionsPopup: defaultPopupSessionsOptions,
		},
	}

	var stdout bytes.Buffer
	if err := app.Run([]string{"tmux", "popup-sessions"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	const wantCommand = "exec '/tmp/proj mux/bin/projmux' 'sessions' '--ui=popup'"
	if popup.command != wantCommand {
		t.Fatalf("popup command = %q, want %q", popup.command, wantCommand)
	}

	wantOptions := inttmux.PopupOptions{
		Width:         "80%",
		Height:        "75%",
		NoBorder:      true,
		CloseBehavior: inttmux.PopupCloseOnExit,
	}
	if !reflect.DeepEqual(popup.options, wantOptions) {
		t.Fatalf("popup options = %#v, want %#v", popup.options, wantOptions)
	}
}

func TestAppRunTmuxPopupCommandsUseMinimumReadableSizes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		args        []string
		popup       *stubTmuxPopupClient
		wantOptions inttmux.PopupOptions
	}{
		{
			name:  "preview",
			args:  []string{"tmux", "popup-preview", "dev"},
			popup: &stubTmuxPopupClient{},
			wantOptions: inttmux.PopupOptions{
				Width:         "120",
				Height:        "30",
				CloseBehavior: inttmux.PopupCloseOnExit,
			},
		},
		{
			name:  "switch",
			args:  []string{"tmux", "popup-switch"},
			popup: &stubTmuxPopupClient{currentPanePath: "/tmp/work tree"},
			wantOptions: inttmux.PopupOptions{
				Width:  "120",
				Height: "28",
				Env: map[string]string{
					hookTrustInlineEnv: "1",
				},
				NoBorder:      true,
				CloseBehavior: inttmux.PopupCloseOnExit,
			},
		},
		{
			name:  "sessions",
			args:  []string{"tmux", "popup-sessions"},
			popup: &stubTmuxPopupClient{},
			wantOptions: inttmux.PopupOptions{
				Width:         "120",
				Height:        "28",
				NoBorder:      true,
				CloseBehavior: inttmux.PopupCloseOnExit,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := &App{
				tmux: &tmuxCommand{
					popup:      tt.popup,
					executable: func() (string, error) { return "/tmp/projmux", nil },
					runner: &recordingTmuxRunner{formats: map[string]string{
						"#{client_tty}":    "/dev/pts/7",
						"#{pane_id}":       "%9",
						"#S":               "dev",
						"#{client_width}":  "140",
						"#{client_height}": "36",
					}},
				},
			}

			if err := app.Run(tt.args, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if !reflect.DeepEqual(tt.popup.options, tt.wantOptions) {
				t.Fatalf("popup options = %#v, want %#v", tt.popup.options, tt.wantOptions)
			}
		})
	}
}

func TestAppRunTmuxPopupToggleOpensStandaloneSidebar(t *testing.T) {
	t.Parallel()

	marker := popupMarkerPath(sanitizePopupKey("/dev/pts/projmux-test-sidebar"), "sessionizer-sidebar")
	_ = os.Remove(marker)
	defer os.Remove(marker)

	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}":        "/dev/pts/projmux-test-sidebar",
		"#{pane_id}":           "%1",
		"#S":                   "work",
		"#{pane_current_path}": "/tmp/work tree",
		"#{client_width}":      "200",
		"#{client_height}":     "50",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/proj mux/bin/projmux", nil },
		homeDir:    func() (string, error) { return t.TempDir(), nil },
		lookupEnv:  func(string) string { return "" },
	}

	if err := cmd.Run([]string{"popup-toggle", "sessionizer-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	wantPrefix := []string{
		"display-popup",
		"-t", "%1",
		"-E",
		"-B",
		"-d", "/tmp/work tree",
		"-e", "PROJMUX_HOOK_TRUST_TARGET_CLIENT=/dev/pts/projmux-test-sidebar",
		"-e", "PROJMUX_NATIVE_LAUNCH_KEY=alt-1",
		"-e", "PROJMUX_SWITCH_TARGET_CLIENT=/dev/pts/projmux-test-sidebar",
		"-e", "TMUX_SESSIONIZER_CONTEXT_DIR=/tmp/work tree",
		"-e", "TMUX_SESSIONIZER_CONTEXT_PANE=%1",
		"-e", "TMUX_SESSIONIZER_CONTEXT_SESSION=work",
		"-x", "0",
		"-y", "0",
		"-w", "40",
		"-h", "48",
	}
	if got.name != "tmux" || len(got.args) < len(wantPrefix)+1 || !reflect.DeepEqual(got.args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("display call = %#v, want prefix %#v", got, wantPrefix)
	}
	command := got.args[len(got.args)-1]
	for _, want := range []string{
		"cd -- '/tmp/work tree'",
		"PROJMUX_HOOK_TRUST_TARGET_CLIENT='/dev/pts/projmux-test-sidebar'",
		"PROJMUX_SWITCH_TARGET_CLIENT='/dev/pts/projmux-test-sidebar'",
		"TMUX_SESSIONIZER_CONTEXT_SESSION='work'",
		"TMUX_SESSIONIZER_CONTEXT_PANE='%1'",
		"'/tmp/proj mux/bin/projmux' 'switch' '--ui=sidebar'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("popup command = %q, want substring %q", command, want)
		}
	}
	for _, unwanted := range []string{
		"PROJMUX_HOOK_TRUST_INLINE",
		"PROJMUX_HOOK_TRUST_TARGET_PANE",
	} {
		if strings.Contains(command, unwanted) {
			t.Fatalf("popup command = %q, unwanted substring %q", command, unwanted)
		}
	}
	content, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker error = %v", err)
	}
	if got, want := string(content), "%1\nwork\n"; got != want {
		t.Fatalf("marker content = %q, want %q", got, want)
	}
}

func TestAppRunTmuxPopupToggleUsesBorderlessNativePopup(t *testing.T) {
	marker := popupMarkerPath(sanitizePopupKey("/dev/pts/projmux-test-native-border"), "sessionizer-sidebar")
	_ = os.Remove(marker)
	defer os.Remove(marker)

	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}":        "/dev/pts/projmux-test-native-border",
		"#{pane_id}":           "%1",
		"#S":                   "work",
		"#{pane_current_path}": "/tmp/work tree",
		"#{client_width}":      "200",
		"#{client_height}":     "50",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "sessionizer-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	if !slices.Contains(got.args, "-B") {
		t.Fatalf("display call = %#v, want -B so only native picker draws the frame", got)
	}
	if !containsTmuxArgPair(got.args, "-w", "40") {
		t.Fatalf("display call = %#v, want native sidebar to keep compact responsive width", got)
	}
}

func TestAppRunTmuxPopupToggleUsesNativePopupBodyStyleFromGlobalTheme(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "source", "repos", "app")
	mkdirAll(t, filepath.Join(project, ".git"))
	writeFile(t, filepath.Join(home, ".config", "projmux", "config.toml"), `
[theme]
background = "#ff0000"
foreground = "#00ff00"
`)
	writeFile(t, filepath.Join(project, ".projmux", "config.toml"), `
[theme]
background = "#010203"
foreground = "#aabbcc"
`)

	marker := popupMarkerPath(sanitizePopupKey("/dev/pts/projmux-test-native-style"), "sessionizer-sidebar")
	_ = os.Remove(marker)
	defer os.Remove(marker)

	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}":        "/dev/pts/projmux-test-native-style",
		"#{pane_id}":           "%1",
		"#S":                   "work",
		"#{pane_current_path}": filepath.Join(project, "subdir"),
		"#{client_width}":      "200",
		"#{client_height}":     "50",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
	}

	if err := cmd.Run([]string{"popup-toggle", "sessionizer-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// Theme is global-only: the popup body style derives from the global
	// theme and the project-local [theme] must be ignored.
	wantStyle := nativePickerPopupBodyStyleFromEffective(theme.ResolveTheme(
		theme.ThemeConfig{Background: "#ff0000", Foreground: "#00ff00"},
	))
	projectStyle := nativePickerPopupBodyStyleFromEffective(theme.ResolveTheme(
		theme.ThemeConfig{Background: "#010203", Foreground: "#aabbcc"},
	))
	if wantStyle == "" || wantStyle == projectStyle {
		t.Fatalf("test styles not distinct: global=%q project=%q", wantStyle, projectStyle)
	}

	got := runner.calls[len(runner.calls)-1]
	if !containsTmuxArgPair(got.args, "-s", wantStyle) {
		t.Fatalf("display call = %#v, want native popup body style from global theme %q", got, wantStyle)
	}
	if containsTmuxArgPair(got.args, "-s", projectStyle) {
		t.Fatalf("display call = %#v, leaked ignored project popup body style %q", got, projectStyle)
	}
}

func TestTmuxPrintAppConfigAppliesGlobalThemeToPaneChrome(t *testing.T) {
	home := t.TempDir()
	project := filepath.Join(home, "source", "repos", "app")
	mkdirAll(t, filepath.Join(project, ".git"))
	// Global theme drives tmux pane chrome; the project-local [theme] is
	// global-only-ignored migration data and must not leak into the config.
	writeFile(t, filepath.Join(home, ".config", "projmux", "config.toml"), `
[theme]
background = "#0000ff"
pane_active_bg = "#41eb3a"
focus = "#ff0000"
`)
	writeFile(t, filepath.Join(project, ".projmux", "config.toml"), `
[theme]
background = "#102030"
pane_active_bg = "#abcdef"
focus = "#fedcba"
`)

	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-app-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	got := stdout.String()

	globalRoles := theme.RenderRolesFromEffective(theme.ResolveTheme(theme.ThemeConfig{
		Background: "#0000ff", PaneActiveBg: "#41eb3a", Focus: "#ff0000",
	}))
	fallbackRoles := theme.RenderRolesFromEffective(theme.ResolveTheme(theme.ThemeConfig{}))
	projectRoles := theme.RenderRolesFromEffective(theme.ResolveTheme(theme.ThemeConfig{
		Background: "#102030", PaneActiveBg: "#abcdef", Focus: "#fedcba",
	}))

	// The global theme must repaint the active-pane border, the active-pane
	// tint, and the inactive pane body — these used to be hardcoded to the
	// fallback because config generation ignored the global config entirely.
	for _, want := range []string{
		"set -g pane-active-border-style \"fg=" + globalRoles.FocusBorder + ",bold\"",
		"set -g window-active-style \"bg=" + globalRoles.FocusPaneActiveBg + "\"",
		"set -g window-style \"bg=" + globalRoles.PaneInactiveBg + "\"",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("print-app-config missing global-theme chrome line %q\n--- got ---\n%s", want, got)
		}
	}

	// Guard against regression to the fallback chrome and against the ignored
	// project theme leaking in.
	for _, unwanted := range []string{
		"set -g window-active-style \"bg=" + fallbackRoles.FocusPaneActiveBg + "\"",
		"set -g pane-active-border-style \"fg=" + fallbackRoles.FocusBorder + ",bold\"",
		"set -g window-active-style \"bg=" + projectRoles.FocusPaneActiveBg + "\"",
		"set -g pane-active-border-style \"fg=" + projectRoles.FocusBorder + ",bold\"",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("print-app-config leaked fallback/project chrome line %q\n--- got ---\n%s", unwanted, got)
		}
	}
}

func TestBuildPopupToggleAppliesNativeBodyStyle(t *testing.T) {
	t.Parallel()

	mode := tmuxPopupToggleMode{Raw: "sessionizer-sidebar", Canonical: "sessionizer-sidebar"}
	ctx := tmuxPopupContext{
		OriginPane:   "%1",
		ContextDir:   "/workspace/project",
		ClientWidth:  200,
		ClientHeight: 50,
	}

	_, nativeOptions, err := buildPopupToggleWithStyle(
		mode,
		"/tmp/projmux",
		"/tmp/marker",
		ctx,
		func(string) string { return "" },
		" bg=colour235,fg=colour245 ",
	)
	if err != nil {
		t.Fatalf("buildPopupToggleWithStyle() error = %v", err)
	}
	if got, want := nativeOptions.BodyStyle, "bg=colour235,fg=colour245"; got != want {
		t.Fatalf("native BodyStyle = %q, want %q", got, want)
	}
}

func TestSessionizerSidebarWidthUsesCompactMinimum(t *testing.T) {
	t.Parallel()

	if got, want := sessionizerSidebarWidth(200), "40"; got != want {
		t.Fatalf("sessionizerSidebarWidth(200) = %q, want compact %q", got, want)
	}
	if got, want := sessionizerSidebarWidth(0), "20%"; got != want {
		t.Fatalf("sessionizerSidebarWidth(unknown client) = %q, want %q", got, want)
	}
}

func TestNotifySidebarWidthUsesProductContract(t *testing.T) {
	t.Parallel()

	if got, want := notifySidebarWidth(200), "64"; got != want {
		t.Fatalf("notifySidebarWidth(200) = %q, want minimum %q", got, want)
	}
	if got, want := notifySidebarWidth(300), "72"; got != want {
		t.Fatalf("notifySidebarWidth(300) = %q, want percentage result %q", got, want)
	}
	if got, want := notifySidebarWidth(0), "24%"; got != want {
		t.Fatalf("notifySidebarWidth(unknown) = %q, want percentage fallback %q", got, want)
	}
}

func TestSidebarPopupHeightLeavesStatusbarRows(t *testing.T) {
	t.Parallel()

	if got, want := sidebarPopupHeight(50), "48"; got != want {
		t.Fatalf("sidebarPopupHeight(50) = %q, want %q", got, want)
	}
	if got, want := sidebarPopupHeight(1), "1"; got != want {
		t.Fatalf("sidebarPopupHeight(1) = %q, want minimum %q", got, want)
	}
	if got, want := sidebarPopupHeight(0), "100%"; got != want {
		t.Fatalf("sidebarPopupHeight(unknown) = %q, want fallback %q", got, want)
	}
}

func TestAppRunTmuxPopupToggleIgnoresRetiredBackendArtifacts(t *testing.T) {
	home := t.TempDir()
	stalePath := filepath.Join(home, ".config", "projmux", "picker-backend")
	if err := os.MkdirAll(filepath.Dir(stalePath), 0o755); err != nil {
		t.Fatal(err)
	}
	staleContent := []byte("retired-value\n")
	if err := os.WriteFile(stalePath, staleContent, 0o600); err != nil {
		t.Fatal(err)
	}

	marker := popupMarkerPath(sanitizePopupKey("/dev/pts/projmux-test-saved-native-border"), "sessionizer-sidebar")
	_ = os.Remove(marker)
	defer os.Remove(marker)

	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}":        "/dev/pts/projmux-test-saved-native-border",
		"#{pane_id}":           "%1",
		"#S":                   "work",
		"#{pane_current_path}": "/tmp/work tree",
		"#{client_width}":      "200",
		"#{client_height}":     "50",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "PROJMUX_PICKER_BACKEND" {
				t.Fatal("retired picker backend env was looked up")
			}
			return ""
		},
	}

	if err := cmd.Run([]string{"popup-toggle", "sessionizer-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	if !slices.Contains(got.args, "-B") {
		t.Fatalf("display call = %#v, want native borderless popup", got)
	}
	command := got.args[len(got.args)-1]
	if strings.Contains(command, "PROJMUX_PICKER_BACKEND") || containsTmuxArgPair(got.args, "-e", "PROJMUX_PICKER_BACKEND=retired-value") {
		t.Fatalf("display call = %#v, want no retired backend propagation", got)
	}
	if content, err := os.ReadFile(stalePath); err != nil || string(content) != string(staleContent) {
		t.Fatalf("retired picker backend file = %q, %v; want unchanged %q", content, err, staleContent)
	}
}

func TestAppRunTmuxPopupToggleKeepsNotifySidebarSizing(t *testing.T) {
	clientKey := "/dev/pts/projmux-test-native-notify"
	marker := popupMarkerPath(sanitizePopupKey(clientKey), "notify-sidebar")
	_ = os.Remove(marker)
	defer os.Remove(marker)

	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}":    clientKey,
		"#{pane_id}":       "%1",
		"#{client_width}":  "200",
		"#{client_height}": "50",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "notify-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	if !slices.Contains(got.args, "-B") {
		t.Fatalf("display call = %#v, want native notify popup to keep native-owned frame", got)
	}
	if !containsTmuxArgPair(got.args, "-w", "64") {
		t.Fatalf("display call = %#v, want configured notify sidebar width", got)
	}
	if !containsTmuxArgPair(got.args, "-x", "136") {
		t.Fatalf("display call = %#v, want right edge position based on sidebar width", got)
	}
	if !containsTmuxArgPair(got.args, "-h", "48") {
		t.Fatalf("display call = %#v, want native notify sidebar to leave statusbar rows uncovered", got)
	}
}

func TestAppRunTmuxPopupToggleOpensNotifySidebarOnRight(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		decoration string
	}{
		{name: "off", decoration: "off"},
		{name: "symbol", decoration: "symbol"},
		{name: "emoji", decoration: "emoji"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clientKey := "/dev/pts/projmux-test-notify-" + tt.name
			marker := popupMarkerPath(sanitizePopupKey(clientKey), "notify-sidebar")
			_ = os.Remove(marker)
			defer os.Remove(marker)

			runner := &recordingTmuxRunner{formats: map[string]string{
				"#{client_tty}":                    clientKey,
				"#{pane_id}":                       "%1",
				"#S":                               "work",
				"#{client_width}":                  "200",
				"#{client_height}":                 "50",
				"#{@projmux_statusbar_decoration}": tt.decoration,
			}}
			cmd := &tmuxCommand{
				runner:     runner,
				executable: func() (string, error) { return "/tmp/proj mux/bin/projmux", nil },
			}

			if err := cmd.Run([]string{"popup-toggle", "notify-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			got := runner.calls[len(runner.calls)-1]
			wantPrefix := []string{
				"display-popup",
				"-c", clientKey,
				"-E",
				"-B",
				"-e", "PROJMUX_NATIVE_LAUNCH_KEY=alt-2",
				"-x", "136",
				"-y", "0",
				"-w", "64",
				"-h", "48",
			}
			if got.name != "tmux" || len(got.args) < len(wantPrefix)+1 || !reflect.DeepEqual(got.args[:len(wantPrefix)], wantPrefix) {
				t.Fatalf("display call = %#v, want prefix %#v", got, wantPrefix)
			}
			if slices.Contains(got.args, "-T") {
				t.Fatalf("display call = %#v, want no title option", got)
			}
			command := got.args[len(got.args)-1]
			if !strings.Contains(command, "'/tmp/proj mux/bin/projmux' 'notify' 'list' '--ui=sidebar' '--client' '"+clientKey+"'") {
				t.Fatalf("popup command = %q, want notify sidebar command", command)
			}
		})
	}
}

func TestAppRunTmuxPopupToggleClosesNotifySidebarWithMarkerPane(t *testing.T) {
	t.Parallel()

	clientKey := "/dev/pts/projmux-test-notify-close"
	marker := popupMarkerPath(sanitizePopupKey(clientKey), "notify-sidebar")
	_ = os.Remove(marker)
	defer os.Remove(marker)
	if err := os.WriteFile(marker, []byte("%original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}": clientKey,
		"#{pane_id}":    "%active",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "--client", clientKey, "notify-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	want := recordedTmuxCall{name: "tmux", args: []string{"display-popup", "-c", clientKey, "-C"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("close call = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker stat error = %v, want not exist", err)
	}
}

func TestPopupRightXFallsBackWhenClientWidthUnknown(t *testing.T) {
	t.Parallel()

	if got, want := popupRightX(0, "64"), "R"; got != want {
		t.Fatalf("popupRightX = %q, want %q", got, want)
	}
	if got, want := popupRightX(200, "64"), "136"; got != want {
		t.Fatalf("popupRightX = %q, want %q", got, want)
	}
}

func TestAppRunTmuxPopupToggleOpensSettingsHub(t *testing.T) {
	t.Parallel()

	marker := popupMarkerPath(sanitizePopupKey("/dev/pts/projmux-test-settings"), "ai-split-settings")
	_ = os.Remove(marker)
	defer os.Remove(marker)

	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}":    "/dev/pts/projmux-test-settings",
		"#{pane_id}":       "%1",
		"#{client_width}":  "200",
		"#{client_height}": "50",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "ai-split-settings"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	if !containsTmuxArgPair(got.args, "-e", "PROJMUX_NATIVE_LAUNCH_KEY=alt-5") {
		t.Fatalf("display call = %#v, want native launch key env", got)
	}
	command := got.args[len(got.args)-1]
	if !strings.Contains(command, "'/tmp/projmux' 'settings'") {
		t.Fatalf("popup command = %q, want settings command", command)
	}
	if strings.Contains(command, "'ai' 'settings'") {
		t.Fatalf("popup command = %q, want unified settings hub", command)
	}
}

func TestAppRunTmuxPopupToggleOpensRecentWindows(t *testing.T) {
	t.Parallel()

	clientKey := "/dev/pts/projmux-test-recent-windows"
	marker := popupMarkerPath(sanitizePopupKey(clientKey), "recent-windows")
	_ = os.Remove(marker)
	defer os.Remove(marker)

	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}":    clientKey,
		"#{pane_id}":       "%1",
		"#{client_width}":  "200",
		"#{client_height}": "50",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "--client", clientKey, "recent-windows"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	wantPrefix := []string{
		"display-popup",
		"-t", "%1",
		"-E",
		"-B",
		"-e", "PROJMUX_NATIVE_LAUNCH_KEY=alt-3",
		"-w", "160",
		"-h", "35",
	}
	if got.name != "tmux" || len(got.args) < len(wantPrefix)+1 || !reflect.DeepEqual(got.args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("display call = %#v, want prefix %#v", got, wantPrefix)
	}
	command := got.args[len(got.args)-1]
	if !strings.Contains(command, "'/tmp/projmux' 'window' 'recent'") {
		t.Fatalf("popup command = %q, want recent windows command", command)
	}
	if strings.Contains(command, "tmux popup-toggle") {
		t.Fatalf("popup command = %q, want popup body to run native picker command directly", command)
	}
	content, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker error = %v", err)
	}
	if got, want := string(content), "%1\n\n"; got != want {
		t.Fatalf("marker content = %q, want %q", got, want)
	}
}

func TestAppRunTmuxPopupToggleResourcesIsClientScopedAndIsolated(t *testing.T) {
	t.Parallel()
	clientKey := "/dev/pts/projmux-test-resources"
	otherClient := "/dev/pts/projmux-test-resources-other"
	marker := popupMarkerPath(sanitizePopupKey(clientKey), resourceInspectorPopupMode)
	otherMarker := popupMarkerPath(sanitizePopupKey(otherClient), resourceInspectorPopupMode)
	_ = os.Remove(marker)
	_ = os.Remove(otherMarker)
	defer os.Remove(marker)
	defer os.Remove(otherMarker)
	if err := os.WriteFile(otherMarker, []byte("%other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}": clientKey, "#{pane_id}": "%1", "#{client_width}": "200", "#{client_height}": "50",
	}}
	cmd := &tmuxCommand{runner: runner, executable: func() (string, error) { return "/tmp/projmux", nil }}
	if err := cmd.Run([]string{"popup-toggle", "--client", clientKey, resourceInspectorPopupMode}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	got := runner.calls[len(runner.calls)-1]
	if got.name != "tmux" || !containsTmuxArgPair(got.args, "-c", clientKey) {
		t.Fatalf("display call = %#v, want client-scoped -c %s", got, clientKey)
	}
	command := got.args[len(got.args)-1]
	if !strings.Contains(command, "'/tmp/projmux' 'resources'") {
		t.Fatalf("popup command = %q, want native resources CLI", command)
	}
	if _, err := os.Stat(otherMarker); err != nil {
		t.Fatalf("other-client marker changed: %v", err)
	}

	if err := cmd.Run([]string{"popup-toggle", "--client", clientKey, resourceInspectorPopupMode}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	got = runner.calls[len(runner.calls)-1]
	if want := (recordedTmuxCall{name: "tmux", args: []string{"display-popup", "-c", clientKey, "-C"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("same-client close = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(otherMarker); err != nil {
		t.Fatalf("same-client close removed other marker: %v", err)
	}
}

func TestAppRunTmuxPopupToggleClosesRecentWindowsWithClientMarker(t *testing.T) {
	t.Parallel()

	clientKey := "/dev/pts/projmux-test-recent-close"
	marker := popupMarkerPath(sanitizePopupKey(clientKey), "recent-windows")
	_ = os.Remove(marker)
	defer os.Remove(marker)
	if err := os.WriteFile(marker, []byte("%original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}": clientKey,
		"#{pane_id}":    "%active",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "--client", clientKey, "recent-windows"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	want := recordedTmuxCall{name: "tmux", args: []string{"display-popup", "-t", "%original", "-C"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("close call = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker stat error = %v, want not exist", err)
	}
}

func TestAppRunTmuxPopupToggleOpensWideAIPicker(t *testing.T) {
	t.Parallel()

	marker := popupMarkerPath(sanitizePopupKey("/dev/pts/projmux-test-ai-picker"), "ai-split-picker")
	_ = os.Remove(marker)
	defer os.Remove(marker)

	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}":        "/dev/pts/projmux-test-ai-picker",
		"#{pane_id}":           "%1",
		"#{pane_current_path}": "/tmp/work tree",
		"#{client_width}":      "200",
		"#{client_height}":     "50",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "ai-split-picker-right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	wantPrefix := []string{
		"display-popup",
		"-t", "%1",
		"-E",
		"-B",
		"-d", "/tmp/work tree",
		"-e", "PROJMUX_NATIVE_LAUNCH_KEY=alt-7",
		"-e", "TMUX_SPLIT_CONTEXT_DIR=/tmp/work tree",
		"-e", "TMUX_SPLIT_TARGET_PANE=%1",
		"-w", "96",
		"-h", "22",
	}
	if got.name != "tmux" || len(got.args) < len(wantPrefix)+1 || !reflect.DeepEqual(got.args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("display call = %#v, want prefix %#v", got, wantPrefix)
	}
	command := got.args[len(got.args)-1]
	if !strings.Contains(command, "'/tmp/projmux' 'ai' 'picker' '--inside' 'right'") {
		t.Fatalf("popup command = %q, want ai picker command", command)
	}
}

func TestAppRunTmuxPopupToggleClosesExistingMarkerWithClientOverride(t *testing.T) {
	t.Parallel()

	clientKey := "/dev/pts/original-client"
	marker := popupMarkerPath(sanitizePopupKey(clientKey), "ai-split-picker")
	_ = os.Remove(marker)
	defer os.Remove(marker)
	if err := os.WriteFile(marker, []byte("%original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}": "/dev/pts/popup-client",
		"#{pane_id}":    "%popup",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "--client", clientKey, "ai-split-picker-right"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	want := recordedTmuxCall{name: "tmux", args: []string{"display-popup", "-t", "%original", "-C"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("close call = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker stat error = %v, want not exist", err)
	}
}

func TestAppRunTmuxPopupToggleRecoversStaleSettingsMarkerAndReopens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{name: "exit status one", err: errors.New("tmux display-popup -t %20 -C: exit status 1")},
		{name: "target not found", err: errors.New("tmux display-popup -t %20 -C: can't find pane: %20")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clientKey := "/dev/pts/projmux-test-stale-settings-" + sanitizePopupKey(tt.name)
			marker := popupMarkerPath(sanitizePopupKey(clientKey), "ai-split-settings")
			_ = os.Remove(marker)
			defer os.Remove(marker)
			if err := os.WriteFile(marker, []byte("%20\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			runner := &recordingTmuxRunner{
				formats: map[string]string{
					"#{client_tty}":    clientKey,
					"#{pane_id}":       "%active",
					"#{client_width}":  "200",
					"#{client_height}": "50",
				},
				errors: map[string]error{
					recordedTmuxCallKey("tmux", "display-popup", "-t", "%20", "-C"): tt.err,
				},
			}
			cmd := &tmuxCommand{
				runner:     runner,
				executable: func() (string, error) { return "/tmp/projmux", nil },
			}

			if err := cmd.Run([]string{"popup-toggle", "--client", clientKey, "ai-split-settings"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v, want nil", err)
			}

			closeIdx := -1
			openIdx := -1
			for i, call := range runner.calls {
				if reflect.DeepEqual(call, recordedTmuxCall{name: "tmux", args: []string{"display-popup", "-t", "%20", "-C"}}) {
					closeIdx = i
					continue
				}
				if call.name == "tmux" && len(call.args) > 0 && call.args[0] == "display-popup" && !slices.Contains(call.args, "-C") {
					openIdx = i
				}
			}
			if closeIdx < 0 {
				t.Fatalf("tmux calls = %#v, want stale close attempt", runner.calls)
			}
			if openIdx <= closeIdx {
				t.Fatalf("tmux calls = %#v, want open display-popup after stale close recovery", runner.calls)
			}
			content, err := os.ReadFile(marker)
			if err != nil {
				t.Fatalf("read marker error = %v", err)
			}
			if got, want := string(content), "%active\n\n"; got != want {
				t.Fatalf("marker content = %q, want recovered current pane marker %q", got, want)
			}
		})
	}
}

func TestAppRunTmuxPopupToggleKeepsGenuineCloseFailure(t *testing.T) {
	t.Parallel()

	clientKey := "/dev/pts/projmux-test-settings-close-failure"
	marker := popupMarkerPath(sanitizePopupKey(clientKey), "ai-split-settings")
	_ = os.Remove(marker)
	defer os.Remove(marker)
	if err := os.WriteFile(marker, []byte("%20\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingTmuxRunner{
		formats: map[string]string{
			"#{client_tty}": clientKey,
			"#{pane_id}":    "%active",
		},
		errors: map[string]error{
			recordedTmuxCallKey("tmux", "display-popup", "-t", "%20", "-C"): errors.New("tmux display-popup -t %20 -C: exit status 2: bad command"),
		},
	}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "--client", clientKey, "ai-split-settings"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
		t.Fatal("Run() error = nil, want close failure")
	}
	for _, call := range runner.calls {
		if call.name == "tmux" && len(call.args) > 0 && call.args[0] == "display-popup" && !slices.Contains(call.args, "-C") {
			t.Fatalf("tmux calls = %#v, want no reopen for genuine close failure", runner.calls)
		}
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "%20\n" {
		t.Fatalf("marker content = %q, err = %v; want stale marker preserved on genuine close failure", string(content), err)
	}
}

func TestAppRunTmuxPopupToggleTreatsClosedPopupAsNoOp(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{
		formats: map[string]string{
			"#{client_tty}": "/dev/pts/projmux-test-close",
		},
		err: errors.New("tmux display-popup: exit status 129"),
	}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "session-popup"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
}

func TestNativeLaunchKeyForPopupMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mode string
		want string
	}{
		{mode: "sessionizer-sidebar", want: "alt-1"},
		{mode: "notify-sidebar", want: "alt-2"},
		{mode: "recent-windows", want: "alt-3"},
		{mode: "session-popup", want: ""},
		{mode: "ai-split-picker-right", want: "alt-7"},
		{mode: "ai-split-picker-down", want: "alt-7"},
		{mode: "ai-split-resume-right", want: "alt-4"},
		{mode: "ai-split-resume-down", want: "alt-4"},
		{mode: "ai-split-settings", want: "alt-5"},
		{mode: "sessionizer", want: "alt-6"},
		{mode: "unknown", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			t.Parallel()

			if got := nativeLaunchKeyForPopupMode(tt.mode); got != tt.want {
				t.Fatalf("nativeLaunchKeyForPopupMode(%q) = %q, want %q", tt.mode, got, tt.want)
			}
		})
	}
}

func TestBuildPopupTogglePropagatesNativePickerEnvironmentWithoutRetiredBackend(t *testing.T) {
	t.Setenv("PROJMUX_PICKER_BACKEND", "retired-value")
	t.Setenv("PROJMUX_NATIVE_DEBUG_LOG", "/tmp/projmux-popup.log")
	t.Setenv("PROJMUX_NATIVE_TTY_FALLBACK", "0")
	t.Setenv("PROJMUX_PROJDIR", "/workspace/projects")
	t.Setenv("PROJMUX_MANAGED_ROOTS", "/workspace/projects")

	command, options, err := buildPopupToggle(
		tmuxPopupToggleMode{Raw: "sessionizer-sidebar", Canonical: "sessionizer-sidebar"},
		"/tmp/projmux",
		"/tmp/marker",
		tmuxPopupContext{
			OriginPane:    "%1",
			TargetClient:  "/dev/pts/7",
			OriginSession: "main",
			ContextDir:    "/workspace/projects/alpha-api",
			ClientWidth:   200,
			ClientHeight:  50,
		},
	)
	if err != nil {
		t.Fatalf("buildPopupToggle() error = %v", err)
	}
	for _, want := range []string{
		"PROJMUX_NATIVE_DEBUG_LOG='/tmp/projmux-popup.log'",
		"PROJMUX_NATIVE_TTY_FALLBACK='0'",
		"PROJMUX_PROJDIR='/workspace/projects'",
		"PROJMUX_MANAGED_ROOTS='/workspace/projects'",
		hookTrustPopupTargetClientEnv + "='/dev/pts/7'",
		inttmux.SwitchTargetClientEnv + "='/dev/pts/7'",
	} {
		if !strings.Contains(command, want) {
			t.Fatalf("popup command = %q, want substring %q", command, want)
		}
	}
	for _, unwanted := range []string{hookTrustPopupTargetPaneEnv, "PROJMUX_PICKER_BACKEND"} {
		if strings.Contains(command, unwanted) {
			t.Fatalf("popup command = %q, unwanted substring %q", command, unwanted)
		}
	}
	for key, want := range map[string]string{
		"PROJMUX_NATIVE_DEBUG_LOG":    "/tmp/projmux-popup.log",
		"PROJMUX_NATIVE_TTY_FALLBACK": "0",
		"PROJMUX_PROJDIR":             "/workspace/projects",
		"PROJMUX_MANAGED_ROOTS":       "/workspace/projects",
		hookTrustPopupTargetClientEnv: "/dev/pts/7",
		inttmux.SwitchTargetClientEnv: "/dev/pts/7",
	} {
		if got := options.Env[key]; got != want {
			t.Fatalf("options.Env[%q] = %q, want %q", key, got, want)
		}
	}
	for _, key := range []string{hookTrustInlineEnv, hookTrustPopupTargetPaneEnv, "PROJMUX_PICKER_BACKEND"} {
		if value, ok := options.Env[key]; ok {
			t.Fatalf("options.Env[%q] = %q, want absent", key, value)
		}
	}
}

func TestBuildPopupToggleSessionizerTrustEnvUsesClientOnly(t *testing.T) {
	command, options, err := buildPopupToggleWithStyle(
		tmuxPopupToggleMode{Raw: "sessionizer", Canonical: "sessionizer"},
		"/tmp/projmux",
		"/tmp/marker",
		tmuxPopupContext{
			TargetClient:  "/dev/pts/8",
			OriginPane:    "%2",
			OriginSession: "main",
			ContextDir:    "/workspace/projects/alpha-api",
			ClientWidth:   200,
			ClientHeight:  50,
		},
		func(string) string { return "" },
		"",
	)
	if err != nil {
		t.Fatalf("buildPopupToggleWithStyle() error = %v", err)
	}
	if !strings.Contains(command, hookTrustPopupTargetClientEnv+"='/dev/pts/8'") {
		t.Fatalf("popup command = %q, want target client env", command)
	}
	if !strings.Contains(command, inttmux.SwitchTargetClientEnv+"='/dev/pts/8'") {
		t.Fatalf("popup command = %q, want switch target client env", command)
	}
	if !strings.Contains(command, hookTrustInlineEnv+"='1'") {
		t.Fatalf("popup command = %q, want inline trust env", command)
	}
	for _, unwanted := range []string{hookTrustPopupTargetPaneEnv} {
		if strings.Contains(command, unwanted) {
			t.Fatalf("popup command = %q, unwanted substring %q", command, unwanted)
		}
		if value, ok := options.Env[unwanted]; ok {
			t.Fatalf("options.Env[%q] = %q, want absent", unwanted, value)
		}
	}
	if got := options.Env[hookTrustPopupTargetClientEnv]; got != "/dev/pts/8" {
		t.Fatalf("options.Env[%q] = %q, want target client", hookTrustPopupTargetClientEnv, got)
	}
	if got := options.Env[inttmux.SwitchTargetClientEnv]; got != "/dev/pts/8" {
		t.Fatalf("options.Env[%q] = %q, want target client", inttmux.SwitchTargetClientEnv, got)
	}
	if got := options.Env[hookTrustInlineEnv]; got != "1" {
		t.Fatalf("options.Env[%q] = %q, want inline trust", hookTrustInlineEnv, got)
	}
}

func TestBuildPopupToggleSessionPopupPropagatesSwitchTargetClient(t *testing.T) {
	command, options, err := buildPopupToggleWithStyle(
		tmuxPopupToggleMode{Raw: "session-popup", Canonical: "session-popup"},
		"/tmp/projmux",
		"/tmp/marker",
		tmuxPopupContext{
			TargetClient: "/dev/pts/8",
			OriginPane:   "%2",
			ClientWidth:  200,
			ClientHeight: 50,
		},
		func(string) string { return "" },
		"",
	)
	if err != nil {
		t.Fatalf("buildPopupToggleWithStyle() error = %v", err)
	}
	if !strings.Contains(command, inttmux.SwitchTargetClientEnv+"='/dev/pts/8'") {
		t.Fatalf("popup command = %q, want switch target client env", command)
	}
	if got := options.Env[inttmux.SwitchTargetClientEnv]; got != "/dev/pts/8" {
		t.Fatalf("options.Env[%q] = %q, want target client", inttmux.SwitchTargetClientEnv, got)
	}
}

func TestAppRunTmuxPopupTogglePropagatesLegacySessionizerRoots(t *testing.T) {
	t.Parallel()

	marker := popupMarkerPath(sanitizePopupKey("/dev/pts/projmux-test-legacy-roots"), "sessionizer-sidebar")
	_ = os.Remove(marker)
	defer os.Remove(marker)

	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}":        "/dev/pts/projmux-test-legacy-roots",
		"#{pane_id}":           "%1",
		"#S":                   "work",
		"#{pane_current_path}": "/tmp/work tree",
		"#{client_width}":      "200",
		"#{client_height}":     "50",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
		lookupEnv: func(name string) string {
			if name == "TMUX_SESSIONIZER_ROOTS" {
				return "/legacy/projects"
			}
			return ""
		},
	}

	if err := cmd.Run([]string{"popup-toggle", "sessionizer-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := runner.calls[len(runner.calls)-1]
	if !containsTmuxArgPair(got.args, "-e", "TMUX_SESSIONIZER_ROOTS=/legacy/projects") {
		t.Fatalf("display call = %#v, want legacy roots env propagated", got)
	}
	command := got.args[len(got.args)-1]
	if !strings.Contains(command, "TMUX_SESSIONIZER_ROOTS='/legacy/projects'") {
		t.Fatalf("popup command = %q, want legacy roots env assignment", command)
	}
}

func TestTmuxPrintConfigUsesStandaloneBindings(t *testing.T) {
	t.Parallel()

	cmd := &tmuxCommand{executable: func() (string, error) { return "/tmp/proj mux/bin/projmux", nil }}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	for _, want := range []string{
		"bind-key -n M-1 run-shell",
		"'/tmp/proj mux/bin/projmux' tmux popup-toggle --client #{client_tty} sessionizer-sidebar",
		"bind-key -n M-2 run-shell",
		"'/tmp/proj mux/bin/projmux' tmux popup-toggle --client #{client_tty} notify-sidebar",
		"bind-key -n M-3 run-shell",
		"'/tmp/proj mux/bin/projmux' tmux popup-toggle --client #{client_tty} recent-windows",
		"unbind-key -q F",
		"set-hook -g pane-focus-out",
		"'/tmp/proj mux/bin/projmux' attention arm #{hook_pane} >/dev/null 2>&1 || true",
		"set-hook -g pane-focus-in",
		"'/tmp/proj mux/bin/projmux' attention clear #{hook_pane} >/dev/null 2>&1 || true",
		"set-hook -g after-select-pane",
		"'/tmp/proj mux/bin/projmux' attention clear #{pane_id} >/dev/null 2>&1 || true",
		"set-hook -g pane-exited",
		"sleep 0.05; '/tmp/proj mux/bin/projmux' tmux rebalance-panes >/dev/null 2>&1 || true",
		"set-hook -g after-kill-pane",
		"'/tmp/proj mux/bin/projmux' attention window #{window_id}",
		"set-hook -g after-select-window",
		"set-hook -g client-session-changed",
		"run-shell -b \"'/tmp/proj mux/bin/projmux' window record >/dev/null 2>&1 || true\"",
		"#[bold,fg=colour230,bg=colour29]#[range=user|settings]  projmux #[norange]#[default]",
		"'/tmp/proj mux/bin/projmux' status kube",
		"'/tmp/proj mux/bin/projmux' status git",
		"set -g @projmux_statusbar_decoration off",
		"set -g @projmux_statusbar_decoration_cwd off",
		"set -g @projmux_statusbar_decoration_git off",
		"set -g @projmux_statusbar_decoration_notify off",
		"set -g status 2",
		"set -g status-left-length 20",
		"#[range=user|session]#[bold,fg=colour254,bg=colour60] #('/tmp/proj mux/bin/projmux' status project) #[default]#[norange] ",
		"#{n:window_name}",
		"#{=/7/...:window_name}",
		"@projmux_statusbar_decoration_cwd",
		"#[fg=colour220] ",
		"#[fg=colour220]📁 ",
		"#[fg=colour245]#{=-28/...:pane_current_path}#[norange]",
		"#[fg=colour245]   %Y-%m-%d %H:%M",
		"range=user|notify",
		"range=user|usage",
		"set -g status-format[0]",
		"set -g status-format[1]",
		"#[align=left range=user|notify]#('/tmp/proj mux/bin/projmux' status notify --max-width 80)#[norange]#[align=right range=user|usage]#('/tmp/proj mux/bin/projmux' status usage --max-width 120)#[norange]",
		"align=left",
		"align=right",
		"set -gu status-format[2]",
		"unbind-key -q -n M-6",
		"unbind-key -q -n C-n",
		"unbind-key -q -n M-r",
		"unbind-key -q -n C-t",
		"unbind-key -q -n User11",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("print-config output = %q, want substring %q", output, want)
		}
	}
	for _, banned := range []string{
		"set -g status 3",
		"set -g status-format[2] \"",
		"tmux autosave-session-state --quiet",
		"bind-key R command-prompt",
		"'/tmp/proj mux/bin/projmux' window recent",
		"bind-key -n M-3 run-shell \"'/tmp/proj mux/bin/projmux' tmux popup-toggle --client #{client_tty} session-popup\"",
		"set-hook -g session-window-changed",
		"set -s user-keys",
		"bind-key -n User",
		"\\033[900",
		"\\033[901",
		"range=user|sessionstate",
		"statusbar click sessionstate",
	} {
		if strings.Contains(output, banned) {
			t.Fatalf("print-config output = %q, did not expect substring %q", output, banned)
		}
	}
}

func TestTmuxPrintConfigCanonicalizesNpmStagingBinaryPath(t *testing.T) {
	t.Parallel()

	stagingPath := "/home/u/.nvm/versions/node/v24.15.0/lib/node_modules/.projmux-lvpOxyM9/node_modules/@projmux/linux-x64/bin/projmux"
	cmd := &tmuxCommand{executable: func() (string, error) { return stagingPath, nil }}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	want := "/home/u/.nvm/versions/node/v24.15.0/lib/node_modules/projmux/node_modules/@projmux/linux-x64/bin/projmux"
	if !strings.Contains(output, want) {
		t.Fatalf("print-config output = %q, want substring %q", output, want)
	}
	if strings.Contains(output, ".projmux-lvpOxyM9") {
		t.Fatalf("print-config output = %q, did not expect npm retire/staging segment %q", output, ".projmux-lvpOxyM9")
	}
}

func TestStatusbarSettingsButtonPaintsCompactPaddingInsideRange(t *testing.T) {
	t.Parallel()

	got := statusbarSettingsButton(statusbarSettingsIcon, statusSegmentRoles)
	want := "#[bold,fg=colour230,bg=colour29]#[range=user|settings]   #[norange]#[default]"
	if got != want {
		t.Fatalf("statusbarSettingsButton() = %q, want %q", got, want)
	}
}

func TestStatusbarSettingsButtonPaintsStandalonePaddingInsideRange(t *testing.T) {
	t.Parallel()

	got := statusbarSettingsButton(statusbarSettingsIcon+" projmux", statusSegmentRoles)
	want := "#[bold,fg=colour230,bg=colour29]#[range=user|settings]  projmux #[norange]#[default]"
	if got != want {
		t.Fatalf("statusbarSettingsButton() = %q, want %q", got, want)
	}
}

func TestTmuxPrintConfigMissingKeymapKeepsDefaultOutput(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := tmuxStandaloneConfig("/tmp/projmux", loadStatusbarDecoration(cmd.homeDir, cmd.lookupEnv))
	if got := stdout.String(); got != want {
		t.Fatalf("print-config output changed without keymap\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestTmuxConfigWithFallbackEffectiveThemeMatchesDefaultOutput(t *testing.T) {
	t.Parallel()

	effective := theme.ResolveTheme(theme.ThemeConfig{})
	got := tmuxStandaloneConfigWithKeymapTheme("/tmp/projmux", statusbarDecorationSetFromGlobal(config.StatusbarDecorationOff), defaultKeyBindingCatalog(), false, effective)
	want := tmuxStandaloneConfigWithKeymap("/tmp/projmux", statusbarDecorationSetFromGlobal(config.StatusbarDecorationOff), defaultKeyBindingCatalog(), false)
	if got != want {
		t.Fatalf("fallback themed standalone config changed\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	gotApp := tmuxAppConfigWithKeymapTheme("/tmp/projmux", "/bin/sh", statusbarDecorationSetFromGlobal(config.StatusbarDecorationOff), defaultKeyBindingCatalog(), false, effective)
	wantApp := tmuxAppConfigWithKeymap("/tmp/projmux", "/bin/sh", statusbarDecorationSetFromGlobal(config.StatusbarDecorationOff), defaultKeyBindingCatalog(), false)
	if gotApp != wantApp {
		t.Fatalf("fallback themed app config changed\n--- got ---\n%s\n--- want ---\n%s", gotApp, wantApp)
	}
}

func TestTmuxConfigThemeUsesGlobal256ColorBackgroundWithoutFallbackLeak(t *testing.T) {
	t.Parallel()

	globalCfg := theme.ThemeConfig{
		Background:    "#010203",
		SurfaceActive: "#040506",
		Foreground:    "#aabbcc",
	}
	effective := theme.ResolveTheme(globalCfg)
	tokens := theme.TmuxRenderTokensFromEffective(effective)

	output := tmuxAppConfigWithKeymapTheme("/tmp/projmux", "/bin/sh", statusbarDecorationSetFromGlobal(config.StatusbarDecorationOff), defaultKeyBindingCatalog(), false, effective)
	for _, want := range []string{
		"set -g status-style \"bg=" + tokens.StatusBg + ",fg=" + tokens.StatusFg + "\"",
		"#[fg=" + tokens.WindowInactiveFg + ",bg=" + tokens.WindowInactiveBg + "] #('/tmp/projmux' attention window #{window_id} #{@projmux_ai_badge_style})",
		"#[bold,fg=" + tokens.WindowActiveFg + ",bg=" + tokens.WindowActiveBg + "] #('/tmp/projmux' attention window #{window_id} #{@projmux_ai_badge_style})",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("themed app config missing %q\n%s", want, output)
		}
	}
}

// Phase 6b+: an explicit `background` (surface/status unset) must repaint the
// inactive pane body (window-style follows PaneInactiveBg from background) while
// the bottom status bg (status-style from StatusBg) stays at the fallback
// literal, proving pane body and status bg are separated.
func TestTmuxConfigExplicitBackgroundRepaintsWindowStyleNotStatus(t *testing.T) {
	t.Parallel()

	effective := theme.ResolveTheme(theme.ThemeConfig{Background: "#010203"})
	roles := theme.RenderRolesFromEffective(effective)
	fallbackRoles := theme.RenderRolesFromEffective(theme.ResolveTheme(theme.ThemeConfig{}))

	output := tmuxAppConfigWithKeymapTheme("/tmp/projmux", "/bin/sh", statusbarDecorationSetFromGlobal(config.StatusbarDecorationOff), defaultKeyBindingCatalog(), false, effective)

	// Pane body repaints: window-style carries the background-derived color,
	// not the historical "bg=default".
	if roles.PaneInactiveBg == "default" {
		t.Fatalf("PaneInactiveBg = %q, want background-derived color", roles.PaneInactiveBg)
	}
	if want := "set -g window-style \"bg=" + roles.PaneInactiveBg + "\""; !strings.Contains(output, want) {
		t.Fatalf("themed app config missing %q\n%s", want, output)
	}
	if strings.Contains(output, "set -g window-style \"bg=default\"") {
		t.Fatalf("window-style still bg=default with explicit background\n%s", output)
	}
	// Popup/chrome bg does NOT follow background: status-style keeps the surface
	// fallback literal.
	if want := "set -g status-style \"bg=" + fallbackRoles.StatusBg + ",fg="; !strings.Contains(output, want) {
		t.Fatalf("status-style should keep surface fallback bg %q (popup must not follow background)\n%s", fallbackRoles.StatusBg, output)
	}
}

// An explicit `status_background` (background/surface unset) must repaint the
// bottom status bg while the popup surface and general pane body stay separate.
func TestTmuxConfigExplicitStatusBackgroundRepaintsStatusNotWindowStyle(t *testing.T) {
	t.Parallel()

	effective := theme.ResolveTheme(theme.ThemeConfig{StatusBackground: "#ff00ff"})
	roles := theme.RenderRolesFromEffective(effective)
	fallbackRoles := theme.RenderRolesFromEffective(theme.ResolveTheme(theme.ThemeConfig{}))

	output := tmuxAppConfigWithKeymapTheme("/tmp/projmux", "/bin/sh", statusbarDecorationSetFromGlobal(config.StatusbarDecorationOff), defaultKeyBindingCatalog(), false, effective)

	// Status repaints: status-style follows status_background, not the fallback.
	if roles.StatusBg == fallbackRoles.StatusBg {
		t.Fatalf("StatusBg = %q, want repainted from explicit status_background", roles.StatusBg)
	}
	if want := "set -g status-style \"bg=" + roles.StatusBg + ",fg="; !strings.Contains(output, want) {
		t.Fatalf("themed app config missing status_background-driven status-style %q\n%s", want, output)
	}
	// Pane body untouched: window-style stays bg=default.
	if want := "set -g window-style \"bg=default\""; !strings.Contains(output, want) {
		t.Fatalf("window-style should stay bg=default with background unset\n%s", output)
	}
}

func TestTmuxPrintConfigNilHomeDirIgnoresRelativeKeymap(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	keymapPath := filepath.Join(tmp, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keymapPath, []byte("[bindings.sessionizer-sidebar]\nplain = \"M-a\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		readFile:   os.ReadFile,
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "bind-key -n M-1 run-shell") {
		t.Fatalf("print-config output = %q, want default M-1 binding", output)
	}
	if strings.Contains(output, "bind-key -n M-a run-shell") {
		t.Fatalf("print-config output = %q, did not expect relative keymap override", output)
	}
}

func TestTmuxPrintConfigKeymapOverrideChangesBindAndUnbindsStaleDefault(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymapPath := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keymapPath, []byte("[bindings.ProjectSidebarToggle]\nplain = \"M-a\"\nprefix = \"A\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	for _, want := range []string{
		"unbind-key -q -n M-1",
		"unbind-key -q -n M-a",
		"unbind-key -q F",
		"unbind-key -q A",
		"bind-key -n M-a run-shell",
		"'/tmp/projmux' tmux popup-toggle --client #{client_tty} sessionizer-sidebar",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("print-config output = %q, want substring %q", output, want)
		}
	}
	if strings.Contains(output, "bind-key -n M-1 run-shell") {
		t.Fatalf("print-config output = %q, did not expect stale M-1 bind", output)
	}
	if strings.Contains(output, "bind-key F run-shell") {
		t.Fatalf("print-config output = %q, did not expect stale F bind", output)
	}
	if strings.Contains(output, "bind-key A run-shell") {
		t.Fatalf("print-config output = %q, did not expect prefix override bind", output)
	}
}

func TestTmuxPrintConfigInvalidKeymapReportsUsefulErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "invalid chord",
			body: "[bindings.ProjectSidebarToggle]\nplain = \"M x\"\n",
			want: "contains unsupported tmux config characters",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			keymapPath := filepath.Join(home, ".config", "projmux", "keymap.toml")
			if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(keymapPath, []byte(tt.body), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := &tmuxCommand{
				executable: func() (string, error) { return "/tmp/projmux", nil },
				homeDir:    func() (string, error) { return home, nil },
				lookupEnv:  func(string) string { return "" },
				readFile:   os.ReadFile,
			}
			err := cmd.Run([]string{"print-config"}, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "keymap.toml") {
				t.Fatalf("Run() error = %v, want %q with keymap path", err, tt.want)
			}
		})
	}
}

func TestTmuxPrintConfigRejectsRetiredPaneRenameActionWithMigrationGuidance(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymapPath := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[bindings.rename-pane-topic]\nkeys = [\"M-r\"]\n"
	if err := os.WriteFile(keymapPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
	}

	err := cmd.Run([]string{"print-config"}, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("Run() = nil, want retired pane rename action error")
	}
	for _, want := range []string{
		keymapPath + ":1",
		`keybinding action "rename-pane-topic" was removed`,
		"replace [bindings.rename-pane-topic] with [bindings.rename-pane-label]",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Run() error = %q, want %q", err, want)
		}
	}
}

func TestTmuxPrintConfigCanonicalPaneRenameWritesOnlyLabelBinding(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymapPath := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keymapPath, []byte("[bindings.rename-pane-label]\nkeys = [\"M-r\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	var binding string
	for line := range strings.SplitSeq(output, "\n") {
		if strings.Contains(line, "bind-key -n M-r command-prompt") {
			binding = line
			break
		}
	}
	if binding == "" {
		t.Fatalf("print-config output = %q, want canonical pane rename binding", output)
	}
	for _, want := range []string{
		"bind-key -n M-r command-prompt",
		`-p "pane label:"`,
		"set-option -p @projmux_pane_label",
	} {
		if !strings.Contains(binding, want) {
			t.Fatalf("pane rename binding = %q, want %q", binding, want)
		}
	}
	for _, forbidden := range []string{
		retiredPaneRenameActionID,
		aiPaneTopicOption,
		aiPaneTopicManualOption,
		"select-pane -T",
	} {
		if strings.Contains(binding, forbidden) {
			t.Fatalf("pane rename binding contains forbidden producer %q: %s", forbidden, binding)
		}
	}
}

func TestTmuxPrintConfigGracefullyIgnoresDroppedLegacyIDs(t *testing.T) {
	t.Parallel()

	// An old keymap.toml referencing only hard-dropped legacy ids must not
	// error or crash; the dropped bindings are silently ignored and the
	// defaults are emitted unchanged.
	home := t.TempDir()
	keymapPath := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keymapPath, []byte("[bindings.sessionizer-sidebar]\nplain = \"M-a\"\n\n[bindings.session-popup]\nplain = \"M-s\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v, want no error for dropped legacy ids", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "bind-key -n M-1 run-shell") {
		t.Fatalf("print-config output = %q, want default M-1 sidebar binding", output)
	}
	if strings.Contains(output, "bind-key -n M-a") {
		t.Fatalf("print-config output = %q, did not expect dropped legacy override M-a", output)
	}
	if strings.Contains(output, "bind-key -n M-s") {
		t.Fatalf("print-config output = %q, did not expect dropped legacy override M-s", output)
	}
}

func TestTmuxPrintConfigUsesSavedStatusbarDecoration(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	paths, err := config.Homes{HomeDir: home}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveStatusbarDecorationFile(paths.StatusbarDecorationFile(), config.StatusbarDecorationSymbol); err != nil {
		t.Fatal(err)
	}

	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if got, want := stdout.String(), "set -g @projmux_statusbar_decoration symbol"; !strings.Contains(got, want) {
		t.Fatalf("print-config output = %q, want substring %q", got, want)
	}
	for _, want := range []string{
		"set -g @projmux_statusbar_decoration_cwd symbol",
		"set -g @projmux_statusbar_decoration_git symbol",
		"set -g @projmux_statusbar_decoration_notify symbol",
	} {
		if got := stdout.String(); !strings.Contains(got, want) {
			t.Fatalf("print-config output = %q, want substring %q", got, want)
		}
	}
}

func TestTmuxPrintConfigShortCircuitsWindowListClicksToNativeSelectWindow(t *testing.T) {
	t.Parallel()

	// Empirically tmux 3.4+ fires `MouseDown1Status` with
	// `#{mouse_status_range}` set to the bare string "window" and an empty
	// `#{mouse_window}` for window-list clicks. The `select-window -t =`
	// idiom only resolves through tmux's internal mouse context, which a
	// `run-shell` subprocess cannot see — so the projmux dispatcher would
	// no-op silently. The bind must short-circuit that case via
	// `if-shell -F` to a native `select-window -t =` *before* invoking
	// projmux. This test pins the rendered config so the regression cannot
	// silently come back.
	cmd := &tmuxCommand{executable: func() (string, error) { return "/tmp/proj mux/bin/projmux", nil }}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	for _, want := range []string{
		// Must short-circuit the bare "window" range to native select-window
		// using tmux block syntax (avoids invalid triple-escaped quoting that
		// breaks the tmux config parser).
		"bind-key -n MouseDown1Status if-shell -F \"#{==:#{mouse_status_range},window}\"",
		"{ select-window -t = }",
		// Projmux fallback path for non-window ranges still goes through run-shell,
		// now wrapped in a `{ ... }` block instead of an extra layer of quoting.
		`{ run-shell "'/tmp/proj mux/bin/projmux' statusbar click \"#{mouse_status_range}\" --client \"#{client_tty}\" --mouse-window \"#{mouse_window}\"" }`,
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("print-config output = %q, want substring %q", output, want)
		}
	}
	// The previous unconditional `run-shell` bind (no if-shell guard) must be gone.
	for _, banned := range []string{
		"bind-key -n MouseDown1Status run-shell ",
	} {
		if strings.Contains(output, banned) {
			t.Fatalf("print-config output = %q, did not expect substring %q", output, banned)
		}
	}
}

func TestTmuxPrintConfigBindsHardcodedStatusbarUsageRefresh(t *testing.T) {
	t.Parallel()

	cmd := &tmuxCommand{executable: func() (string, error) { return "/tmp/proj mux/bin/projmux", nil }}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := `bind-key -T projmux-status r run-shell "'/tmp/proj mux/bin/projmux' statusbar usage-refresh"`
	if !strings.Contains(stdout.String(), want) {
		t.Fatalf("print-config output = %q, want substring %q", stdout.String(), want)
	}
}

func TestTmuxPrintConfigBindsPaneContextMenu(t *testing.T) {
	t.Parallel()

	cmd := &tmuxCommand{executable: func() (string, error) { return "/tmp/proj mux/bin/projmux", nil }}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	for _, want := range []string{
		// Stock-style guard: forward the click to mouse-aware applications and
		// non-copy pane modes instead of opening the menu.
		"bind-key -n MouseDown3Pane if-shell -F -t = \"#{||:#{mouse_any_flag},#{&&:#{pane_in_mode},#{?#{m/r:(copy|view)-mode,#{pane_mode}},0,1}}}\"",
		"{ select-pane -t = ; send-keys -M }",
		// Menu chrome mirrors tmux 3.4's stock MouseDown3Pane menu.
		"display-menu -T \"#[align=centre]#{pane_index} (#{pane_id})\" -t = -x M -y M",
		// projmux entry: selects the clicked pane, then opens the AI resume
		// picker via the same popup-toggle entrypoint as the C-r keybinding
		// (`ai picker` directly would fail: run-shell has no TMUX env, so the
		// picker cannot resolve its context cwd).
		`"AI Resume Picker" a { select-pane -t = ; run-shell "'/tmp/proj mux/bin/projmux' tmux popup-toggle --client #{client_tty} ai-split-resume-right" }`,
		// Stock split items (stock names + key shortcuts).
		"\"Horizontal Split\" h { split-window -h }",
		"\"Vertical Split\" v { split-window -v }",
		// A few more stock items with their dim/toggle format conditions.
		"\"#{?#{>:#{window_panes},1},,-}Swap Up\" u { swap-pane -U }",
		"\"#{?#{>:#{window_panes},1},,-}Swap Down\" d { swap-pane -D }",
		"\"Kill\" X { kill-pane }",
		"\"Respawn\" R { respawn-pane -k }",
		"\"#{?pane_marked,Unmark,Mark}\" m { select-pane -m }",
		"\"#{?#{>:#{window_panes},1},,-}#{?window_zoomed_flag,Unzoom,Zoom}\" z { resize-pane -Z }",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("print-config output = %q, want substring %q", output, want)
		}
	}
}

func TestTmuxPrintConfigUnbindsPaneContextMenuBeforeBinding(t *testing.T) {
	t.Parallel()

	// Same unbind-then-reinstall contract as the statusbar MouseDown1Status
	// binding: the quiet unbind must precede the bind so re-sourcing the config
	// on a live server deterministically replaces any stale handler.
	cmd := &tmuxCommand{executable: func() (string, error) { return "/tmp/projmux", nil }}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	unbindIdx := strings.Index(output, "unbind-key -q -n MouseDown3Pane")
	bindIdx := strings.Index(output, "bind-key -n MouseDown3Pane")
	if unbindIdx == -1 {
		t.Fatalf("print-config output = %q, want substring %q", output, "unbind-key -q -n MouseDown3Pane")
	}
	if bindIdx == -1 {
		t.Fatalf("print-config output = %q, want substring %q", output, "bind-key -n MouseDown3Pane")
	}
	if unbindIdx > bindIdx {
		t.Fatalf("unbind-key -q -n MouseDown3Pane (index %d) must precede bind-key -n MouseDown3Pane (index %d)", unbindIdx, bindIdx)
	}

	// The statusbar binding follows the identical pattern; pin both so the
	// surfaces cannot drift apart silently.
	statusUnbindIdx := strings.Index(output, "unbind-key -q -n MouseDown1Status")
	statusBindIdx := strings.Index(output, "bind-key -n MouseDown1Status")
	if statusUnbindIdx == -1 || statusBindIdx == -1 || statusUnbindIdx > statusBindIdx {
		t.Fatalf("statusbar unbind/bind ordering broken: unbind index %d, bind index %d", statusUnbindIdx, statusBindIdx)
	}
}

func TestTmuxPrintAppConfigBindsPaneContextMenu(t *testing.T) {
	t.Parallel()

	// The app config embeds the standalone config lines, so `projmux shell`
	// sessions must carry the same pane context menu.
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/proj mux/bin/projmux", nil },
		homeDir:    func() (string, error) { return t.TempDir(), nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-app-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	for _, want := range []string{
		"unbind-key -q -n MouseDown3Pane",
		"bind-key -n MouseDown3Pane if-shell -F -t = ",
		"display-menu -T \"#[align=centre]#{pane_index} (#{pane_id})\" -t = -x M -y M",
		`"AI Resume Picker" a { select-pane -t = ; run-shell "'/tmp/proj mux/bin/projmux' tmux popup-toggle --client #{client_tty} ai-split-resume-right" }`,
		"\"Horizontal Split\" h { split-window -h }",
		"\"Vertical Split\" v { split-window -v }",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("print-app-config output = %q, want substring %q", output, want)
		}
	}
}

func TestTmuxRebalancePanesSelectsMultiPaneWindows(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			strings.Join([]string{"tmux", "list-windows", "-a", "-F", "#{window_id}\t#{window_panes}"}, "\x00"): "@1\t1\n@2\t3\n@3\t2\n",
		},
	}
	cmd := &tmuxCommand{runner: runner}

	if err := cmd.Run([]string{"rebalance-panes"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []recordedTmuxCall{
		{name: "tmux", args: []string{"list-windows", "-a", "-F", "#{window_id}\t#{window_panes}"}},
		{name: "tmux", args: []string{"select-layout", "-t", "@2", "-E"}},
		{name: "tmux", args: []string{"select-layout", "-t", "@3", "-E"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestTmuxRenamePaneSetsOnlyUserLabel(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{}
	cmd := &tmuxCommand{runner: runner}

	if err := cmd.Run([]string{"rename-pane", "%42", "projmux-2"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []recordedTmuxCall{
		{name: "tmux", args: []string{"set-option", "-p", "-t", "%42", paneLabelOption, "projmux-2"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestTmuxRenamePaneEmptyClearsOnlyUserLabel(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{}
	cmd := &tmuxCommand{runner: runner}

	if err := cmd.Run([]string{"rename-pane", "%42", ""}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []recordedTmuxCall{
		{name: "tmux", args: []string{"set-option", "-p", "-u", "-t", "%42", paneLabelOption}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, want)
	}
}

func TestTmuxPrintAppConfigUsesIsolatedAppSettings(t *testing.T) {
	t.Parallel()

	// Isolate HOME so generated chrome derives from the built-in fallback, not
	// from a developer's real global ~/.config/projmux/config.toml [theme].
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return t.TempDir(), nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-app-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	for _, want := range []string{
		"Generated by projmux. Used by `projmux shell`.",
		"set -g @projmux_app 1",
		"set -g history-limit 10000",
		"set -g set-clipboard on",
		"set -g default-shell \"/bin/sh\"",
		"set -g default-command \"\"",
		"set -ga update-environment \"WSL_DISTRO_NAME\"",
		"set -ga update-environment \"VSCODE_IPC_HOOK_CLI\"",
		"set -ga update-environment \"TERM_PROGRAM_VERSION\"",
		"set -ga update-environment \"PROJMUX_WELCOME\"",
		"set-hook -g client-attached",
		"run-shell -b",
		"'/tmp/projmux' welcome --popup >/dev/null 2>&1",
		"set-hook -g after-select-window",
		"set-hook -g client-session-changed",
		"run-shell -b \"'/tmp/projmux' window record >/dev/null 2>&1 || true\"",
		"set -g status-position bottom",
		"set -g status-keys vi",
		"set -g window-status-separator \" \"",
		"set -g allow-rename off",
		"set -g automatic-rename on",
		"set -g automatic-rename-format " + tmuxConfigQuote(tmuxVisiblePaneLabelFormat()),
		"set -g mode-keys vi",
		"set -sg escape-time 100",
		"set -g pane-border-style \"fg=colour236\"",
		"set -g pane-active-border-style \"fg=colour51,bold\"",
		"set -g pane-border-status top",
		"set -g pane-border-format \"#{?pane_active,#[bold#,fg=colour16#,bg=colour45] > ",
		"#[bold#,fg=" + tmuxAIBadgeProgressFg + "] ● ",
		"#[bold#,fg=" + tmuxAIBadgeSuccessFg + "] ● ",
		"#[bold#,fg=" + tmuxAIBadgeActionRequiredFg + "] ● ",
		"#[fg=colour244] ",
		"#{@projmux_ai_topic}",
		"#{pane_current_command},#{pane_title}",
		"bind-key -n M-Left select-pane -L",
		"bind-key -n M-Right select-pane -R",
		"bind-key -n M-Up select-pane -U",
		"bind-key -n M-Down select-pane -D",
		"bind-key -n M-S-Left previous-window",
		"bind-key -n M-S-Right next-window",
		"set -g status-left-length 20",
		"set -g status-left \"#[range=user|session]#[bold,fg=colour254,bg=colour60] #('/tmp/projmux' status project) #[default]#[norange] \"",
		"#[bold,fg=colour254,bg=colour60] #('/tmp/projmux' status project) #[default]",
		"#{n:window_name}",
		"#{=/7/...:window_name}",
		"set -g @projmux_statusbar_decoration off",
		"set -g @projmux_statusbar_decoration_cwd off",
		"set -g @projmux_statusbar_decoration_git off",
		"set -g @projmux_statusbar_decoration_notify off",
		"@projmux_statusbar_decoration_cwd",
		"#[fg=colour220] ",
		"#[fg=colour220]📁 ",
		"#[fg=colour245]#{=-28/...:pane_current_path}#[norange]",
		"'/tmp/projmux' tmux popup-toggle --client #{client_tty} sessionizer-sidebar",
		"#[fg=colour245]   %Y-%m-%d %H:%M #[bold,fg=colour230,bg=colour29]#[range=user|settings]   #[norange]#[default]",
		"set -g status 2",
		"range=user|notify",
		"range=user|usage",
		"set -g status-format[0]",
		"set -g status-format[1]",
		"#[align=left range=user|notify]#('/tmp/projmux' status notify --max-width 80)#[norange]#[align=right range=user|usage]#('/tmp/projmux' status usage --max-width 120)#[norange]",
		"#('/tmp/projmux' tmux autosave-session-state --quiet)",
		"align=left",
		"align=right",
		"set -gu status-format[2]",
		"unbind-key -q -n M-6",
		"unbind-key -q -n C-n",
		"unbind-key -q -n M-r",
		"unbind-key -q -n C-t",
		"unbind-key -q -n User11",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("print-app-config output = %q, want substring %q", output, want)
		}
	}
	if strings.Contains(output, "#[bold,fg=colour16,bg=colour45] app #[default]") {
		t.Fatalf("print-app-config output = %q, did not expect duplicate app status badge", output)
	}
	for _, banned := range []string{
		"set -g status 3",
		"set -g status-format[2] \"",
		"set -s user-keys",
		"set -s user-keys[",
		"\\033[900",
		"\\033[901",
		"bind-key -n User",
		"bind-key -n User8",
		"bind-key -n User9",
		"9009u",
		"9010u",
		"set-hook -g session-window-changed",
		"range=user|sessionstate",
		"statusbar click sessionstate",
		"$env:PROJMUX_PICKER_BACKEND",
		"$env:PROJMUX_NATIVE_LINE_MODE",
	} {
		if strings.Contains(output, banned) {
			t.Fatalf("print-app-config output = %q, did not expect substring %q", output, banned)
		}
	}
}

func TestTmuxPrintConfigUsesSavedAIBadgeStyle(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	paths, err := config.Homes{HomeDir: home}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveAIBadgeStyleFile(paths.AIBadgeStyleFile(), config.AIBadgeStyleEmoji); err != nil {
		t.Fatalf("SaveAIBadgeStyleFile() error = %v", err)
	}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-app-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	for _, want := range []string{
		"set -g " + aiBadgeStyleTmuxOption + " emoji",
		"⏳",
		"✅",
		"🔄",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("print-app-config output = %q, want substring %q", output, want)
		}
	}
}

func TestTmuxAIBadgeStyleOffPreservesPaneBorderSpacing(t *testing.T) {
	t.Parallel()

	roles := theme.RenderRolesFromEffective(theme.EffectiveTheme{})
	dot := tmuxPaneBorderFormatWithAIBadgeStyle(config.AIBadgeStyleDot, roles)
	off := tmuxPaneBorderFormatWithAIBadgeStyle(config.AIBadgeStyleOff, roles)

	for _, banned := range []string{" ● ", "⏳", "✅", "🔄"} {
		if strings.Contains(off, banned) {
			t.Fatalf("off pane border format = %q, did not expect marker %q", off, banned)
		}
	}
	if strings.Count(off, "]   ") < 3 {
		t.Fatalf("off pane border format = %q, want blank marker lanes", off)
	}
	if got := len([]rune(off)) - len([]rune(dot)); got != 0 {
		t.Fatalf("off pane border rune-length delta = %d, want stable marker lane width", got)
	}
}

func TestTmuxAppNamingFormatsUseVisiblePaneLabel(t *testing.T) {
	t.Parallel()

	visibleLabel := tmuxVisiblePaneLabelFormat()
	styledVisibleLabel := tmuxStyledVisiblePaneLabelFormat()
	shellLabel := tmuxShellPaneLabelFormat()
	paneBorder := tmuxPaneBorderFormat()
	configText := tmuxAppConfig("/tmp/projmux", "/bin/sh", config.StatusbarDecorationOff)

	wantShellLabel := "#{?#{||:#{||:#{||:#{==:#{pane_current_command},zsh},#{==:#{pane_current_command},bash}},#{||:#{==:#{pane_current_command},fish},#{==:#{pane_current_command},sh}}},#{||:#{==:#{pane_current_command},nu},#{==:#{pane_current_command},xonsh}}},#{pane_current_command},#{pane_title}}"
	if shellLabel != wantShellLabel {
		t.Fatalf("shell label format = %q, want %q", shellLabel, wantShellLabel)
	}
	if zshIndex, titleIndex := strings.Index(shellLabel, "#{==:#{pane_current_command},zsh}"), strings.Index(shellLabel, "#{pane_title}"); zshIndex < 0 || titleIndex < 0 || zshIndex > titleIndex {
		t.Fatalf("shell label format = %q, want zsh command match before raw pane_title fallback", shellLabel)
	}

	wantVisibleLabel := "#{?#{!=:#{@projmux_pane_label},},#{@projmux_pane_label},#{?#{&&:#{!=:#{@projmux_ai_agent},},#{!=:#{@projmux_ai_topic},}},#{@projmux_ai_topic}," + shellLabel + "}}"
	if visibleLabel != wantVisibleLabel {
		t.Fatalf("visible pane label format = %q, want AI topic before shell fallback: %q", visibleLabel, wantVisibleLabel)
	}
	if labelIndex, topicIndex, shellIndex := strings.Index(visibleLabel, "#{@projmux_pane_label}"), strings.Index(visibleLabel, "#{@projmux_ai_topic}"), strings.Index(visibleLabel, shellLabel); labelIndex < 0 || topicIndex < 0 || shellIndex < 0 || labelIndex > topicIndex || topicIndex > shellIndex {
		t.Fatalf("visible pane label format = %q, want label before topic before shell/current-command fallback", visibleLabel)
	}
	if !strings.Contains(paneBorder, tmuxStyledVisiblePaneLabelFormatFor(tmuxAIBadgeProgressFg)) {
		t.Fatalf("pane border format = %q, want progress-styled visible label", paneBorder)
	}
	if !strings.Contains(paneBorder, tmuxStyledVisiblePaneLabelFormatFor(tmuxAIBadgeSuccessFg)) {
		t.Fatalf("pane border format = %q, want ready-styled visible label", paneBorder)
	}
	if !strings.Contains(paneBorder, tmuxStyledVisiblePaneLabelFormatFor(tmuxAIBadgeActionRequiredFg)) {
		t.Fatalf("pane border format = %q, want prompt-styled visible label", paneBorder)
	}
	activePaneBorderPrefix := "#{?pane_active,#[bold#,fg=colour16#,bg=colour45] > " + visibleLabel
	if !strings.Contains(paneBorder, activePaneBorderPrefix) {
		t.Fatalf("pane border format = %q, want active pane label to keep block chip surface %q", paneBorder, activePaneBorderPrefix)
	}
	if !strings.Contains(paneBorder, "#[bold#,fg="+tmuxAIBadgeProgressFg+"] ● ") {
		t.Fatalf("pane border format = %q, want busy marker to use state.progress", paneBorder)
	}
	if !strings.Contains(paneBorder, "#[bold#,fg="+tmuxAIBadgeSuccessFg+"] ● ") {
		t.Fatalf("pane border format = %q, want reply/ready marker to use state.success", paneBorder)
	}
	if !strings.Contains(paneBorder, "#[bold#,fg="+tmuxAIBadgeActionRequiredFg+"] ● ") {
		t.Fatalf("pane border format = %q, want prompt marker to use state.action_required", paneBorder)
	}
	for _, want := range []string{aiBadgeKindApprovalRequired, aiBadgeKindInputRequired, aiBadgeKindResponseComplete, aiBadgeKindInProgress} {
		if !strings.Contains(paneBorder, "@projmux_ai_badge_kind},"+want) {
			t.Fatalf("pane border format = %q, want semantic badge kind %q", paneBorder, want)
		}
	}
	if !strings.Contains(styledVisibleLabel, "#[bold#,fg="+tmuxAIBadgeProgressFg+"]#{@projmux_ai_topic}#[pop-default]") {
		t.Fatalf("styled visible label format = %q, want renderer-level lead topic styling", styledVisibleLabel)
	}
	readyStyledVisibleLabel := tmuxStyledVisiblePaneLabelFormatFor(tmuxAIBadgeSuccessFg)
	if !strings.Contains(readyStyledVisibleLabel, "#[bold#,fg="+tmuxAIBadgeSuccessFg+"]#{@projmux_ai_topic}#[pop-default]") {
		t.Fatalf("ready styled visible label format = %q, want renderer-level lead topic styling to match ready marker", readyStyledVisibleLabel)
	}
	if !strings.Contains(styledVisibleLabel, `^\\[[Ll]ead:[Ss]hip\\]`) {
		t.Fatalf("styled visible label format = %q, want literal bracket escapes to survive tmux config parsing", styledVisibleLabel)
	}

	wantPaneBorderLine := "set -g pane-border-format " + tmuxConfigQuote(paneBorder)
	if !strings.Contains(configText, wantPaneBorderLine+"\n") {
		t.Fatalf("app config = %q, want pane border to use exact shared visible label line %q", configText, wantPaneBorderLine)
	}
	wantAutomaticRenameLine := "set -g automatic-rename-format " + tmuxConfigQuote(visibleLabel)
	if !strings.Contains(configText, wantAutomaticRenameLine+"\n") {
		t.Fatalf("app config = %q, want automatic rename to use exact visible label helper line %q", configText, wantAutomaticRenameLine)
	}
	if strings.Contains(configText, "set -g automatic-rename-format \"#{pane_title}\"") {
		t.Fatalf("app config = %q, automatic rename must not depend solely on raw pane_title", configText)
	}
}

func TestTmuxPaneBorderAIBadgeRepaintsFromExplicitThemeKeepingActionRequiredSeparate(t *testing.T) {
	t.Parallel()

	fallbackRoles := theme.RenderRolesFromEffective(theme.EffectiveTheme{})
	fallbackBorder := tmuxPaneBorderFormatWithAIBadgeStyle(config.AIBadgeStyleDot, fallbackRoles)

	// Fallback border keeps the historical literals (byte-identity).
	if !strings.Contains(fallbackBorder, "#[bold#,fg="+fallbackRoles.AIProgress+"]") {
		t.Fatalf("fallback pane border = %q, want progress role %q", fallbackBorder, fallbackRoles.AIProgress)
	}

	// An explicit critical/warning theme must NOT repaint action_required
	// (Tier C, independent of critical) — its color stays the literal.
	explicitRoles := theme.RenderRolesFromEffective(theme.ResolveTheme(theme.ThemeConfig{
		Critical: "#0000ff",
		Warning:  "#00ff00",
	}))
	if explicitRoles.AIActionRequired != fallbackRoles.AIActionRequired {
		t.Fatalf("ai.action_required repainted to %q, must stay independent of critical at %q", explicitRoles.AIActionRequired, fallbackRoles.AIActionRequired)
	}
	explicitBorder := tmuxPaneBorderFormatWithAIBadgeStyle(config.AIBadgeStyleDot, explicitRoles)
	if !strings.Contains(explicitBorder, "#[bold#,fg="+fallbackRoles.AIActionRequired+"]") {
		t.Fatalf("explicit pane border = %q, want action_required marker to keep literal %q", explicitBorder, fallbackRoles.AIActionRequired)
	}
}

func TestTmuxAppShellTitlePolicyDisablesProgramWindowRename(t *testing.T) {
	t.Parallel()

	configText := tmuxAppConfig("/tmp/projmux", "/bin/sh", config.StatusbarDecorationOff)
	for _, want := range []string{
		"set -g allow-rename off",
		"set -g automatic-rename on",
		"set -g automatic-rename-format " + tmuxConfigQuote(tmuxVisiblePaneLabelFormat()),
	} {
		if !strings.Contains(configText, want) {
			t.Fatalf("app config = %q, want shell title policy line %q", configText, want)
		}
	}
}

func TestTmuxAutosaveSessionStateForceCapturesAndStoresCurrentSession(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC)
	dir := t.TempDir()
	windowFormat := strings.Join([]string{"#{window_index}", "#{window_name}", "#{window_layout}"}, "\x1f")
	paneFormat := strings.Join([]string{
		"#{window_index}",
		"#{pane_index}",
		"#{pane_title}",
		"#{@projmux_pane_label}",
		"#{?pane_active,1,0}",
		"#{pane_current_path}",
		"#{@projmux_recipe_kind}",
		"#{@projmux_startup_command}",
		"#{@projmux_ai_managed}",
		"#{@projmux_ai_agent}",
		"#{@projmux_ai_topic}",
		"#{@projmux_ai_topic_manual}",
		"#{@projmux_ai_resume_id}",
		"#{@projmux_ai_resume_source}",
		"#{@projmux_ai_resume_updated_at}",
	}, "\x1f")
	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			strings.Join([]string{"tmux", "display-message", "-p", "#{session_name}"}, "\x00"):              "workspace\n",
			strings.Join([]string{"tmux", "list-windows", "-t", "workspace", "-F", windowFormat}, "\x00"):   "0\x1fshell\x1flayout\n",
			strings.Join([]string{"tmux", "list-panes", "-s", "-t", "workspace", "-F", paneFormat}, "\x00"): "0\x1f0\x1fshell\x1f1\x1f/tmp\x1f\x1f\x1f\x1f\x1f\x1f\n",
		},
	}
	cmd := &tmuxCommand{
		runner:  runner,
		now:     func() time.Time { return now },
		homeDir: func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(name string) string {
			if name == sessionStateAutosaveEnv {
				return "on"
			}
			return ""
		},
		sessionStore: func() (sessionstate.Store, error) {
			return sessionstate.NewStore(dir), nil
		},
	}

	if err := cmd.Run([]string{"autosave-session-state", "--force"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	loaded, err := sessionstate.NewStore(dir).Load("workspace")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Session != "workspace" || loaded.DefaultCWD != "/tmp" || len(loaded.Windows) != 1 {
		t.Fatalf("snapshot = %#v, want current workspace session snapshot", loaded)
	}
	wantGate := recordedTmuxCall{name: "tmux", args: []string{"set-option", "-t", "workspace", "-q", "@projmux_sessionstate_autosave_at", "1778555045"}}
	if got := runner.calls[len(runner.calls)-1]; !reflect.DeepEqual(got, wantGate) {
		t.Fatalf("last tmux call = %#v, want %#v", got, wantGate)
	}
}

func TestTmuxAutosaveSessionStateSkipsWhenDebounceGateIsFresh(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC)
	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			strings.Join([]string{"tmux", "display-message", "-p", "#{session_name}"}, "\x00"):                                         "workspace\n",
			strings.Join([]string{"tmux", "display-message", "-p", "-t", "workspace", "#{@projmux_sessionstate_source}"}, "\x00"):      "\n",
			strings.Join([]string{"tmux", "display-message", "-p", "-t", "workspace", "#{@projmux_sessionstate_autosave_at}"}, "\x00"): "1778555030\n",
		},
	}
	cmd := &tmuxCommand{
		runner:  runner,
		now:     func() time.Time { return now },
		homeDir: func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(name string) string {
			if name == sessionStateAutosaveEnv {
				return "on"
			}
			return ""
		},
		sessionStore: func() (sessionstate.Store, error) { return sessionstate.NewStore(t.TempDir()), nil },
	}

	if err := cmd.Run([]string{"autosave-session-state"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("tmux calls = %#v, want session resolution, source marker, and debounce gate read only", runner.calls)
	}
}

func TestTmuxAutosaveSessionStateUsesConfiguredInterval(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC)
	configHome := t.TempDir()
	paths := config.DefaultPaths(configHome, t.TempDir())
	if err := config.SaveSessionStateDurationFile(paths.SessionStateAutosaveIntervalFile(), 10*time.Second); err != nil {
		t.Fatalf("SaveSessionStateDurationFile() error = %v", err)
	}
	dir := t.TempDir()
	windowFormat := strings.Join([]string{"#{window_index}", "#{window_name}", "#{window_layout}"}, "\x1f")
	paneFormat := strings.Join([]string{
		"#{window_index}",
		"#{pane_index}",
		"#{pane_title}",
		"#{@projmux_pane_label}",
		"#{?pane_active,1,0}",
		"#{pane_current_path}",
		"#{@projmux_recipe_kind}",
		"#{@projmux_startup_command}",
		"#{@projmux_ai_managed}",
		"#{@projmux_ai_agent}",
		"#{@projmux_ai_topic}",
		"#{@projmux_ai_topic_manual}",
		"#{@projmux_ai_resume_id}",
		"#{@projmux_ai_resume_source}",
		"#{@projmux_ai_resume_updated_at}",
	}, "\x1f")
	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			strings.Join([]string{"tmux", "display-message", "-p", "#{session_name}"}, "\x00"):                                         "workspace\n",
			strings.Join([]string{"tmux", "display-message", "-p", "-t", "workspace", "#{@projmux_sessionstate_source}"}, "\x00"):      "\n",
			strings.Join([]string{"tmux", "display-message", "-p", "-t", "workspace", "#{@projmux_sessionstate_autosave_at}"}, "\x00"): "1778555030\n",
			strings.Join([]string{"tmux", "list-windows", "-t", "workspace", "-F", windowFormat}, "\x00"):                              "0\x1fshell\x1flayout\n",
			strings.Join([]string{"tmux", "list-panes", "-s", "-t", "workspace", "-F", paneFormat}, "\x00"):                            "0\x1f0\x1fshell\x1f1\x1f/tmp\x1f\x1f\x1f\x1f\x1f\x1f\n",
		},
	}
	cmd := &tmuxCommand{
		runner:  runner,
		now:     func() time.Time { return now },
		homeDir: func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(name string) string {
			switch name {
			case "XDG_CONFIG_HOME":
				return configHome
			case sessionStateAutosaveEnv:
				return "on"
			default:
				return ""
			}
		},
		sessionStore: func() (sessionstate.Store, error) { return sessionstate.NewStore(dir), nil },
	}

	if err := cmd.Run([]string{"autosave-session-state"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := sessionstate.NewStore(dir).Load("workspace"); err != nil {
		t.Fatalf("Load() error = %v, want autosave after configured interval", err)
	}
	wantGate := recordedTmuxCall{name: "tmux", args: []string{"set-option", "-t", "workspace", "-q", "@projmux_sessionstate_autosave_at", "1778555045"}}
	if got := runner.calls[len(runner.calls)-1]; !reflect.DeepEqual(got, wantGate) {
		t.Fatalf("last tmux call = %#v, want %#v", got, wantGate)
	}
}

func TestTmuxAutosaveSessionStateSkipsFreshSource(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			strings.Join([]string{"tmux", "display-message", "-p", "#{session_name}"}, "\x00"):                                    "workspace\n",
			strings.Join([]string{"tmux", "display-message", "-p", "-t", "workspace", "#{@projmux_sessionstate_source}"}, "\x00"): "fresh\n",
		},
	}
	cmd := &tmuxCommand{
		runner:  runner,
		now:     func() time.Time { return time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC) },
		homeDir: func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(name string) string {
			if name == sessionStateAutosaveEnv {
				return "on"
			}
			return ""
		},
		sessionStore: func() (sessionstate.Store, error) { return sessionstate.NewStore(t.TempDir()), nil },
	}

	if err := cmd.Run([]string{"autosave-session-state", "--force"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []recordedTmuxCall{
		{name: "tmux", args: []string{"display-message", "-p", "#{session_name}"}},
		{name: "tmux", args: []string{"display-message", "-p", "-t", "workspace", "#{@projmux_sessionstate_source}"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("tmux calls = %#v, want fresh source gate only", runner.calls)
	}
}

func TestTmuxAutosaveSessionStateSkipsWhenDisabled(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			strings.Join([]string{"tmux", "display-message", "-p", "#{session_name}"}, "\x00"): "workspace\n",
		},
	}
	cmd := &tmuxCommand{
		runner:  runner,
		now:     func() time.Time { return time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC) },
		homeDir: func() (string, error) { return t.TempDir(), nil },
		lookupEnv: func(name string) string {
			if name == sessionStateAutosaveEnv {
				return "off"
			}
			return ""
		},
		sessionStore: func() (sessionstate.Store, error) { return sessionstate.NewStore(t.TempDir()), nil },
	}

	if err := cmd.Run([]string{"autosave-session-state"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []recordedTmuxCall{{name: "tmux", args: []string{"display-message", "-p", "#{session_name}"}}}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("tmux calls = %#v, want session resolution only when autosave disabled", runner.calls)
	}
}

func TestTmuxAutosaveSessionStateProjectOverridePrecedence(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name        string
		global      config.SessionStateToggle
		env         string
		project     config.SessionStateProjectToggle
		wantSaved   bool
		wantTmuxMin int
	}{
		{name: "project off global on", global: config.SessionStateToggleOn, project: config.SessionStateProjectOff, wantSaved: false, wantTmuxMin: 1},
		{name: "project on global off", global: config.SessionStateToggleOff, project: config.SessionStateProjectOn, wantSaved: true, wantTmuxMin: 5},
		{name: "project inherit global off", global: config.SessionStateToggleOff, project: config.SessionStateProjectInherit, wantSaved: false, wantTmuxMin: 1},
		{name: "project on env off", global: config.SessionStateToggleOn, env: "off", project: config.SessionStateProjectOn, wantSaved: true, wantTmuxMin: 5},
		{name: "project off env on", global: config.SessionStateToggleOff, env: "on", project: config.SessionStateProjectOff, wantSaved: false, wantTmuxMin: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			home := t.TempDir()
			dir := filepath.Join(home, "state")
			saveGlobalAutosaveForTest(t, home, tc.global)
			saveProjectAutosaveForTest(t, home, "workspace", tc.project)
			runner := autosaveCaptureRunner("workspace", "/repo")
			cmd := &tmuxCommand{
				runner:  runner,
				now:     func() time.Time { return time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC) },
				homeDir: func() (string, error) { return home, nil },
				lookupEnv: func(name string) string {
					switch name {
					case "XDG_CONFIG_HOME":
						return filepath.Join(home, "config")
					case sessionStateAutosaveEnv:
						return tc.env
					default:
						return ""
					}
				},
				sessionStore: func() (sessionstate.Store, error) {
					return sessionstate.NewStore(dir), nil
				},
			}

			if err := cmd.Run([]string{"autosave-session-state", "--force"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			_, err := sessionstate.NewStore(dir).Load("workspace")
			if tc.wantSaved && err != nil {
				t.Fatalf("Load() error = %v, want saved snapshot", err)
			}
			if !tc.wantSaved && err == nil {
				t.Fatalf("Load() succeeded, want no autosaved snapshot")
			}
			if len(runner.calls) < tc.wantTmuxMin {
				t.Fatalf("tmux calls = %#v, want at least %d calls", runner.calls, tc.wantTmuxMin)
			}
		})
	}
}

func TestTmuxAutosaveSessionStateNoProjectSettingUsesGlobalFallback(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dir := filepath.Join(home, "state")
	saveGlobalAutosaveForTest(t, home, config.SessionStateToggleOn)
	runner := autosaveCaptureRunner("workspace", "/repo")
	cmd := &tmuxCommand{
		runner:  runner,
		now:     func() time.Time { return time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC) },
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "XDG_CONFIG_HOME" {
				return filepath.Join(home, "config")
			}
			return ""
		},
		sessionStore: func() (sessionstate.Store, error) {
			return sessionstate.NewStore(dir), nil
		},
	}

	if err := cmd.Run([]string{"autosave-session-state", "--force"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := sessionstate.NewStore(dir).Load("workspace"); err != nil {
		t.Fatalf("Load() error = %v, want global fallback autosave", err)
	}
}

func TestTmuxAutosaveSessionStateQuietSwallowsRuntimeErrors(t *testing.T) {
	t.Parallel()

	runner := &recordingTmuxRunner{err: errors.New("tmux unavailable")}
	cmd := &tmuxCommand{
		runner:       runner,
		now:          func() time.Time { return time.Date(2026, 5, 12, 3, 4, 5, 0, time.UTC) },
		homeDir:      func() (string, error) { return t.TempDir(), nil },
		lookupEnv:    func(string) string { return "" },
		sessionStore: func() (sessionstate.Store, error) { return sessionstate.NewStore(t.TempDir()), nil },
	}

	var stderr bytes.Buffer
	if err := cmd.Run([]string{"autosave-session-state", "--quiet"}, &bytes.Buffer{}, &stderr); err != nil {
		t.Fatalf("Run() error = %v, want quiet nil", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want quiet", stderr.String())
	}
}

func TestTmuxPrintAppConfigKeepsStandaloneAndAppKeymapScopesSeparated(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymapPath := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keymapPath, []byte("[bindings.ProjectSidebarToggle]\nplain = \"M-a\"\n\n[bindings.new-window]\nplain = \"C-t\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-app-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	for _, want := range []string{
		"bind-key -n M-a run-shell",
		"'/tmp/projmux' tmux popup-toggle --client #{client_tty} sessionizer-sidebar",
		"bind-key -n C-t new-window -c \"#{pane_current_path}\"",
		"unbind-key -q -n C-t",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("print-app-config output = %q, want substring %q", output, want)
		}
	}
	if strings.Contains(output, "bind-key -n C-t run-shell") {
		t.Fatalf("print-app-config output = %q, app chord unexpectedly used standalone action", output)
	}
	if strings.Contains(output, "bind-key -n M-a new-window") {
		t.Fatalf("print-app-config output = %q, standalone chord unexpectedly used app action", output)
	}
	unbindIdx := strings.LastIndex(output, "unbind-key -q -n C-t")
	bindIdx := strings.Index(output, "bind-key -n C-t new-window")
	if unbindIdx < 0 || bindIdx < 0 || unbindIdx > bindIdx {
		t.Fatalf("C-t cleanup/rebind order = (%d, %d), want every cleanup before current new-window bind\n%s", unbindIdx, bindIdx, output)
	}
	for line := range strings.SplitSeq(output, "\n") {
		if strings.HasPrefix(line, "bind-key -n C-t ") && strings.Contains(line, "pane label:") {
			t.Fatalf("current C-t binding retained stale pane-label body: %s", line)
		}
	}
}

func TestTmuxApplyRetiresStalePaneLabelChordWithoutRewritingKeymap(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymapPath := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	keymap := []byte(`# keep comments and spacing byte-for-byte
[bindings.rename-pane-label]
keys = ["M-p"]

[bindings.ProjectSidebarToggle]
keys = ["M-a"] # unrelated current binding
`)
	if err := os.WriteFile(keymapPath, keymap, 0o644); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	probeKey := strings.Join([]string{"tmux", "-L", "projmux-test", "list-sessions", "-F", "#{session_id}"}, "\x00")
	sourceKey := strings.Join([]string{"tmux", "-L", "projmux-test", "source-file", configPath}, "\x00")
	runner := &recordingTmuxRunner{outputs: map[string]string{probeKey: "$0\n", sourceKey: ""}}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
		writeFile:  os.WriteFile,
		runner:     runner,
	}

	var firstConfig []byte
	for attempt := 1; attempt <= 2; attempt++ {
		var stdout, stderr bytes.Buffer
		if err := cmd.Run([]string{"apply", "--config", configPath, "--socket", "projmux-test"}, &stdout, &stderr); err != nil {
			t.Fatalf("apply attempt %d error = %v; stderr = %q", attempt, err, stderr.String())
		}
		gotKeymap, err := os.ReadFile(keymapPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(gotKeymap, keymap) {
			t.Fatalf("apply attempt %d rewrote keymap bytes\ngot:  %q\nwant: %q", attempt, gotKeymap, keymap)
		}
		gotConfig, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if attempt == 1 {
			firstConfig = append([]byte(nil), gotConfig...)
		} else if !bytes.Equal(gotConfig, firstConfig) {
			t.Fatalf("repeated apply changed generated config bytes")
		}
	}

	output := string(firstConfig)
	unbindIdx := strings.Index(output, "unbind-key -q -n C-t")
	renameIdx := strings.Index(output, "bind-key -n M-p command-prompt")
	if unbindIdx < 0 || renameIdx < 0 || unbindIdx > renameIdx {
		t.Fatalf("C-t cleanup/M-p bind order = (%d, %d), want cleanup before current bind\n%s", unbindIdx, renameIdx, output)
	}
	if strings.Contains(output, "bind-key -n C-t ") {
		t.Fatalf("generated config rebound retired C-t:\n%s", output)
	}
	for _, want := range []string{`-p "pane label:"`, "set-option -p @projmux_pane_label", "bind-key -n M-a run-shell"} {
		if !strings.Contains(output, want) {
			t.Fatalf("generated config missing preserved current binding %q\n%s", want, output)
		}
	}
	if got := len(runner.calls); got != 4 {
		t.Fatalf("repeated apply tmux calls = %d, want two probe/source pairs: %#v", got, runner.calls)
	}
}

func TestTmuxPrintAppConfigAddsPlainAliasForTransportAction(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	keymapPath := filepath.Join(home, ".config", "projmux", "keymap.toml")
	if err := os.MkdirAll(filepath.Dir(keymapPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keymapPath, []byte("[bindings.previous-window]\nkeys = [\"M-[\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		readFile:   os.ReadFile,
	}
	var stdout bytes.Buffer
	if err := cmd.Run([]string{"print-app-config"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	output := stdout.String()
	for _, want := range []string{
		"bind-key -n M-S-Left previous-window",
		"bind-key -n M-[ previous-window",
		"bind-key -n M-S-Right next-window",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("print-app-config output = %q, want substring %q", output, want)
		}
	}
}

func TestTmuxInstallWritesSnippetAndIncludesIt(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configPath := filepath.Join(home, ".tmux.conf")
	includePath := filepath.Join(home, ".config", "tmux", "projmux.conf")
	if err := os.WriteFile(configPath, []byte("set -g mouse on\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		lookupEnv:  func(name string) string { return home },
		writeFile:  os.WriteFile,
		readFile:   os.ReadFile,
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"install", "--config", configPath, "--include", includePath}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	snippet, err := os.ReadFile(includePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(snippet), "'/tmp/projmux' tmux popup-toggle --client #{client_tty} sessionizer") {
		t.Fatalf("snippet = %q, want projmux binding", string(snippet))
	}
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(config), "source-file \""+includePath+"\"") {
		t.Fatalf("config = %q, want source-file include", string(config))
	}
	if !strings.Contains(stdout.String(), "included from "+configPath) {
		t.Fatalf("stdout = %q, want install summary", stdout.String())
	}
}

func TestTmuxInstallAppWritesAppConfig(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		lookupEnv:  func(name string) string { return home },
		writeFile:  os.WriteFile,
	}

	var stdout bytes.Buffer
	if err := cmd.Run([]string{"install-app", "--config", configPath}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "set -g @projmux_app 1") {
		t.Fatalf("config = %q, want app marker", string(content))
	}
	if !strings.Contains(stdout.String(), "wrote "+configPath) {
		t.Fatalf("stdout = %q, want write summary", stdout.String())
	}
}

func TestTmuxApplySkipsReloadWhenServerMissing(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	runner := &recordingTmuxRunner{err: errors.New("no server running on /tmp/tmux-1000/projmux")}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		lookupEnv:  func(name string) string { return home },
		writeFile:  os.WriteFile,
		runner:     runner,
	}

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"apply", "--config", configPath, "--socket", "projmux-test"}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "set -g @projmux_app 1") {
		t.Fatalf("config = %q, want app marker", string(content))
	}
	if !strings.Contains(stdout.String(), "wrote "+configPath) {
		t.Fatalf("stdout = %q, want wrote line", stdout.String())
	}
	if !strings.Contains(stdout.String(), "skipped reload: no live tmux server -L projmux-test") {
		t.Fatalf("stdout = %q, want skip message", stdout.String())
	}

	if len(runner.calls) == 0 {
		t.Fatalf("expected at least one tmux probe call")
	}
	first := runner.calls[0]
	if first.name != "tmux" || len(first.args) < 4 || first.args[0] != "-L" || first.args[1] != "projmux-test" || first.args[2] != "list-sessions" {
		t.Fatalf("first call = %+v, want list-sessions probe", first)
	}
	for _, c := range runner.calls {
		if len(c.args) >= 3 && c.args[2] == "source-file" {
			t.Fatalf("source-file should not run when probe failed: %+v", c)
		}
	}
}

func TestTmuxApplyUsesSavedDesktopNotifyMode(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	paths, err := config.Homes{HomeDir: home}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveDesktopNotifyModeFile(paths.DesktopNotifyModeFile(), config.DesktopNotifyModeOff); err != nil {
		t.Fatalf("SaveDesktopNotifyModeFile() error = %v", err)
	}
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	runner := &recordingTmuxRunner{err: errors.New("no server running on /tmp/tmux-1000/projmux")}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		homeDir:    func() (string, error) { return home, nil },
		lookupEnv:  func(string) string { return "" },
		writeFile:  os.WriteFile,
		runner:     runner,
	}

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"apply", "--config", configPath}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content), "set -g "+desktopNotifyModeTmuxOption+" off"; !strings.Contains(got, want) {
		t.Fatalf("app config missing %q\n%s", want, got)
	}
}

func TestTmuxApplyReloadsLiveServer(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	probeKey := strings.Join([]string{"tmux", "-L", "projmux", "list-sessions", "-F", "#{session_id}"}, "\x00")
	sourceKey := strings.Join([]string{"tmux", "-L", "projmux", "source-file", configPath}, "\x00")
	runner := &recordingTmuxRunner{
		outputs: map[string]string{
			probeKey:  "$0\n$1\n$2\n",
			sourceKey: "",
		},
	}
	cmd := &tmuxCommand{
		executable: func() (string, error) { return "/tmp/projmux", nil },
		lookupEnv:  func(name string) string { return home },
		writeFile:  os.WriteFile,
		runner:     runner,
	}

	var stdout, stderr bytes.Buffer
	if err := cmd.Run([]string{"apply", "--config", configPath}, &stdout, &stderr); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if !strings.Contains(stdout.String(), "wrote "+configPath) {
		t.Fatalf("stdout = %q, want wrote line", stdout.String())
	}
	if !strings.Contains(stdout.String(), "reloaded tmux server -L projmux: 3 sessions") {
		t.Fatalf("stdout = %q, want reload summary with 3 sessions", stdout.String())
	}

	var sawSource bool
	for _, c := range runner.calls {
		if len(c.args) >= 4 && c.args[2] == "source-file" && c.args[3] == configPath {
			sawSource = true
		}
	}
	if !sawSource {
		t.Fatalf("expected source-file call, calls = %+v", runner.calls)
	}
}

func TestTmuxCommandRejectsInvalidUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing subcommand", args: nil, want: "tmux requires a subcommand"},
		{name: "unknown subcommand", args: []string{"nope"}, want: "unknown tmux subcommand: nope"},
		{name: "missing popup args", args: []string{"popup-preview"}, want: "tmux popup-preview requires exactly 1 argument"},
		{name: "blank session", args: []string{"popup-preview", " "}, want: "tmux popup-preview requires a non-empty <session> argument"},
		{name: "popup-switch extra args", args: []string{"popup-switch", "extra"}, want: "tmux popup-switch accepts no arguments"},
		{name: "popup-sessions extra args", args: []string{"popup-sessions", "extra"}, want: "tmux popup-sessions accepts no arguments"},
		{name: "missing popup-toggle mode", args: []string{"popup-toggle"}, want: "tmux popup-toggle requires exactly 1 argument"},
		{name: "unknown popup-toggle mode", args: []string{"popup-toggle", "nope"}, want: "unknown tmux popup-toggle mode: nope"},
		{name: "missing rename-pane args", args: []string{"rename-pane", "%1"}, want: "tmux rename-pane requires <pane> <label>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			err := (&tmuxCommand{}).Run(tt.args, &bytes.Buffer{}, &stderr)
			if err == nil {
				t.Fatal("expected error")
			}
			if !contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
			if !contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage text", stderr.String())
			}
		})
	}
}

func TestTmuxCommandReportsConfigurationAndRuntimeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cmd  *tmuxCommand
		want string
	}{
		{name: "missing popup client", cmd: &tmuxCommand{executable: func() (string, error) { return "/tmp/projmux", nil }}, want: "configure tmux popup client"},
		{name: "missing executable resolver", cmd: &tmuxCommand{popup: &stubTmuxPopupClient{}}, want: "configure tmux popup executable"},
		{name: "resolve executable", cmd: &tmuxCommand{popup: &stubTmuxPopupClient{}, executable: func() (string, error) { return "", errors.New("not found") }}, want: "resolve tmux popup executable"},
		{name: "display popup", cmd: &tmuxCommand{popup: &stubTmuxPopupClient{err: errors.New("tmux failed")}, executable: func() (string, error) { return "/tmp/projmux", nil }}, want: "display tmux popup preview"},
		{name: "resolve current pane", cmd: &tmuxCommand{popup: &stubTmuxPopupClient{currentPaneErr: errors.New("tmux unavailable")}, executable: func() (string, error) { return "/tmp/projmux", nil }}, want: "resolve tmux popup switch cwd"},
		{name: "display sessions popup", cmd: &tmuxCommand{popup: &stubTmuxPopupClient{err: errors.New("tmux failed")}, executable: func() (string, error) { return "/tmp/projmux", nil }}, want: "display tmux popup sessions"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			args := []string{"popup-preview", "dev"}
			if tt.want == "resolve tmux popup switch cwd" {
				args = []string{"popup-switch"}
			}
			if tt.want == "display tmux popup sessions" {
				args = []string{"popup-sessions"}
			}

			err := tt.cmd.Run(args, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("expected error")
			}
			if !contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func saveGlobalAutosaveForTest(t *testing.T, home string, mode config.SessionStateToggle) {
	t.Helper()

	paths, err := config.Homes{HomeDir: home, ConfigHome: filepath.Join(home, "config")}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSessionStateToggleFile(paths.SessionStateAutosaveFile(), mode); err != nil {
		t.Fatalf("SaveSessionStateToggleFile(autosave) error = %v", err)
	}
}

func saveProjectAutosaveForTest(t *testing.T, home, sessionName string, mode config.SessionStateProjectToggle) {
	t.Helper()

	paths, err := config.Homes{HomeDir: home, ConfigHome: filepath.Join(home, "config")}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSessionStateProjectToggleFile(paths.ProjectSessionStateAutosaveFile(sessionName), mode); err != nil {
		t.Fatalf("SaveSessionStateProjectToggleFile() error = %v", err)
	}
}

func autosaveCaptureRunner(sessionName, cwd string) *recordingTmuxRunner {
	windowFormat := strings.Join([]string{"#{window_index}", "#{window_name}", "#{window_layout}"}, "\x1f")
	paneFormat := strings.Join([]string{
		"#{window_index}",
		"#{pane_index}",
		"#{pane_title}",
		"#{@projmux_pane_label}",
		"#{?pane_active,1,0}",
		"#{pane_current_path}",
		"#{@projmux_recipe_kind}",
		"#{@projmux_startup_command}",
		"#{@projmux_ai_managed}",
		"#{@projmux_ai_agent}",
		"#{@projmux_ai_topic}",
		"#{@projmux_ai_topic_manual}",
		"#{@projmux_ai_resume_id}",
		"#{@projmux_ai_resume_source}",
		"#{@projmux_ai_resume_updated_at}",
	}, "\x1f")
	return &recordingTmuxRunner{
		outputs: map[string]string{
			strings.Join([]string{"tmux", "display-message", "-p", "#{session_name}"}, "\x00"):                                    sessionName + "\n",
			strings.Join([]string{"tmux", "display-message", "-p", "-t", sessionName, "#{@projmux_sessionstate_source}"}, "\x00"): "\n",
			strings.Join([]string{"tmux", "list-windows", "-t", sessionName, "-F", windowFormat}, "\x00"):                         "0\x1fshell\x1flayout\n",
			strings.Join([]string{"tmux", "list-panes", "-s", "-t", sessionName, "-F", paneFormat}, "\x00"):                       "0\x1f0\x1fshell\x1f1\x1f" + cwd + "\x1f\x1f\x1f\x1f\x1f\x1f\n",
		},
	}
}

type stubTmuxPopupClient struct {
	currentPanePath string
	currentPaneErr  error
	command         string
	options         inttmux.PopupOptions
	err             error
}

func (s *stubTmuxPopupClient) CurrentPanePath(context.Context) (string, error) {
	if s.currentPaneErr != nil {
		return "", s.currentPaneErr
	}
	return s.currentPanePath, nil
}

func (s *stubTmuxPopupClient) DisplayPopupWithOptions(_ context.Context, command string, options inttmux.PopupOptions) error {
	s.command = command
	s.options = options
	return s.err
}

type recordingTmuxRunner struct {
	formats map[string]string
	outputs map[string]string
	errors  map[string]error
	calls   []recordedTmuxCall
	err     error
}

type recordedTmuxCall struct {
	name string
	args []string
}

func (r *recordingTmuxRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, recordedTmuxCall{name: name, args: append([]string(nil), args...)})
	if name == "tmux" && len(args) == 4 && reflect.DeepEqual(args[:3], []string{"display-message", "-p", "-F"}) {
		return []byte(r.formats[args[3]] + "\n"), nil
	}
	key := recordedTmuxCallKey(name, args...)
	if err, ok := r.errors[key]; ok {
		return nil, err
	}
	if output, ok := r.outputs[key]; ok {
		return []byte(output), nil
	}
	if r.err != nil {
		return nil, r.err
	}
	return nil, nil
}

func recordedTmuxCallKey(name string, args ...string) string {
	return strings.Join(append([]string{name}, args...), "\x00")
}

func containsTmuxArgPair(args []string, key, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestParsePopupMarkerContent(t *testing.T) {
	t.Parallel()

	pane, session := parsePopupMarkerContent([]byte("%1\nwork\n"))
	if pane != "%1" || session != "work" {
		t.Fatalf("parsePopupMarkerContent(two lines) = (%q, %q), want (%q, %q)", pane, session, "%1", "work")
	}
	pane, session = parsePopupMarkerContent([]byte("%1\n"))
	if pane != "%1" || session != "" {
		t.Fatalf("parsePopupMarkerContent(legacy single line) = (%q, %q), want (%q, %q)", pane, session, "%1", "")
	}
	pane, session = parsePopupMarkerContent(nil)
	if pane != "" || session != "" {
		t.Fatalf("parsePopupMarkerContent(empty) = (%q, %q), want empty", pane, session)
	}
}

func TestIsSidebarPreviewActive(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())

	if isSidebarPreviewActive() {
		t.Fatal("isSidebarPreviewActive() = true, want false without markers")
	}
	other := popupMarkerPath("tty0", "recent-windows")
	if err := os.WriteFile(other, []byte("%1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isSidebarPreviewActive() {
		t.Fatal("isSidebarPreviewActive() = true, want false for non-sidebar popup markers")
	}
	marker := popupMarkerPath("tty0", "sessionizer-sidebar")
	if err := os.WriteFile(marker, []byte("%1\nwork\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isSidebarPreviewActive() {
		t.Fatal("isSidebarPreviewActive() = false, want true while a sidebar marker exists")
	}
}

func TestAppRunTmuxPopupToggleCloseRestoresSidebarOriginSession(t *testing.T) {
	t.Parallel()

	clientKey := "/dev/pts/projmux-test-sidebar-cancel"
	marker := popupMarkerPath(sanitizePopupKey(clientKey), "sessionizer-sidebar")
	_ = os.Remove(marker)
	defer os.Remove(marker)
	if err := os.WriteFile(marker, []byte("%original\nwork\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}": clientKey,
		"#{pane_id}":    "%active",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "--client", clientKey, "sessionizer-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("marker stat error = %v, want not exist", err)
	}
	var got []recordedTmuxCall
	for _, call := range runner.calls {
		if len(call.args) > 0 && (call.args[0] == "has-session" || call.args[0] == "switch-client") {
			got = append(got, call)
		}
	}
	want := []recordedTmuxCall{
		{name: "tmux", args: []string{"has-session", "-t", "=work"}},
		{name: "tmux", args: []string{"switch-client", "-c", clientKey, "-t", "=work"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cancel restore calls = %#v, want origin has-session then switch-client %#v", got, want)
	}
}

func TestAppRunTmuxPopupToggleCloseSkipsSidebarRestoreWhenOriginGone(t *testing.T) {
	t.Parallel()

	clientKey := "/dev/pts/projmux-test-sidebar-cancel-gone"
	marker := popupMarkerPath(sanitizePopupKey(clientKey), "sessionizer-sidebar")
	_ = os.Remove(marker)
	defer os.Remove(marker)
	if err := os.WriteFile(marker, []byte("%original\nwork\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingTmuxRunner{
		formats: map[string]string{
			"#{client_tty}": clientKey,
			"#{pane_id}":    "%active",
		},
		errors: map[string]error{
			recordedTmuxCallKey("tmux", "has-session", "-t", "=work"): errors.New("can't find session"),
		},
	}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "--client", clientKey, "sessionizer-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, call := range runner.calls {
		if len(call.args) > 0 && call.args[0] == "switch-client" {
			t.Fatalf("tmux calls = %#v, want no switch-client when origin session is gone", runner.calls)
		}
	}
}

func TestAppRunTmuxPopupToggleCloseSkipsSidebarRestoreForLegacyMarker(t *testing.T) {
	t.Parallel()

	clientKey := "/dev/pts/projmux-test-sidebar-cancel-legacy"
	marker := popupMarkerPath(sanitizePopupKey(clientKey), "sessionizer-sidebar")
	_ = os.Remove(marker)
	defer os.Remove(marker)
	if err := os.WriteFile(marker, []byte("%original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingTmuxRunner{formats: map[string]string{
		"#{client_tty}": clientKey,
		"#{pane_id}":    "%active",
	}}
	cmd := &tmuxCommand{
		runner:     runner,
		executable: func() (string, error) { return "/tmp/projmux", nil },
	}

	if err := cmd.Run([]string{"popup-toggle", "--client", clientKey, "sessionizer-sidebar"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, call := range runner.calls {
		if len(call.args) > 0 && (call.args[0] == "has-session" || call.args[0] == "switch-client") {
			t.Fatalf("tmux calls = %#v, want no restore attempt without a recorded origin session", runner.calls)
		}
	}
}

func TestTmuxPopupPreviewUsesRawUncanonicalizedExecutable(t *testing.T) {
	t.Parallel()

	stagingPath := "/home/u/.nvm/versions/node/v24.15.0/lib/node_modules/.projmux-lvpOxyM9/node_modules/@projmux/linux-x64/bin/projmux"
	canonicalPath := "/home/u/.nvm/versions/node/v24.15.0/lib/node_modules/projmux/node_modules/@projmux/linux-x64/bin/projmux"
	popup := &stubTmuxPopupClient{}
	app := &App{
		tmux: &tmuxCommand{
			popup:         popup,
			executable:    func() (string, error) { return canonicalPath, nil },
			rawExecutable: func() (string, error) { return stagingPath, nil },
			runner: &recordingTmuxRunner{formats: map[string]string{
				"#{client_tty}":    "/dev/pts/7",
				"#{pane_id}":       "%9",
				"#S":               "dev",
				"#{client_width}":  "140",
				"#{client_height}": "36",
			}},
		},
	}
	if err := app.Run([]string{"tmux", "popup-preview", "dev"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// Immediate in-process re-exec must use the running (raw) path, which is
	// guaranteed to exist; canonicalizing could point at a not-yet-materialized
	// npm tree during an update window and fail with "... returned 127".
	if !strings.Contains(popup.command, stagingPath) {
		t.Fatalf("popup preview command = %q, want raw staging path %q", popup.command, stagingPath)
	}
	if strings.Contains(popup.command, canonicalPath) {
		t.Fatalf("popup preview command = %q, must not canonicalize for immediate re-exec", popup.command)
	}
}

func TestNewTmuxCommandWiresRawExecutable(t *testing.T) {
	t.Parallel()

	if newTmuxCommand().rawExecutable == nil {
		t.Fatal("newTmuxCommand().rawExecutable = nil; popup re-exec would fall back to canonicalized path")
	}
}
