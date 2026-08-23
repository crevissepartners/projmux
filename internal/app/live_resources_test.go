package app

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/systemstatus"
	"github.com/crevissepartners/projmux/internal/theme"
)

func intPointer(value int) *int { return &value }

func TestClassifyLiveResourceSeverityBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     int
		warningAt int
		want      liveResourceSeverity
	}{
		{name: "CPU 69", value: 69, warningAt: liveResourceCPUWarningAt, want: liveResourceNormal},
		{name: "CPU 70", value: 70, warningAt: liveResourceCPUWarningAt, want: liveResourceWarning},
		{name: "CPU 89", value: 89, warningAt: liveResourceCPUWarningAt, want: liveResourceWarning},
		{name: "CPU 90", value: 90, warningAt: liveResourceCPUWarningAt, want: liveResourceCritical},
		{name: "memory 74", value: 74, warningAt: liveResourceMemoryWarningAt, want: liveResourceNormal},
		{name: "memory 75", value: 75, warningAt: liveResourceMemoryWarningAt, want: liveResourceWarning},
		{name: "memory 89", value: 89, warningAt: liveResourceMemoryWarningAt, want: liveResourceWarning},
		{name: "memory 90", value: 90, warningAt: liveResourceMemoryWarningAt, want: liveResourceCritical},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyLiveResourceSeverity(intPointer(test.value), test.warningAt); got != test.want {
				t.Fatalf("classifyLiveResourceSeverity(%d, %d) = %d, want %d", test.value, test.warningAt, got, test.want)
			}
		})
	}
	if got := classifyLiveResourceSeverity(nil, liveResourceCPUWarningAt); got != liveResourceUnknown {
		t.Fatalf("classifyLiveResourceSeverity(nil) = %d, want unknown", got)
	}
}

func TestFormatLiveResourcesStatusIndependentStyles(t *testing.T) {
	t.Parallel()

	roles := theme.RenderRoles{
		StatusTextSecondary: "secondary-role",
		StateWarning:        "warning-role",
		StateCritical:       "critical-role",
	}
	tests := []struct {
		name    string
		metrics systemstatus.Metrics
		want    string
	}{
		{
			name:    "CPU critical memory normal",
			metrics: systemstatus.Metrics{CPUPercent: intPointer(90), MemoryPercent: intPointer(74)},
			want:    " #[fg=critical-role,bold]CPU  90%#[default]  #[fg=secondary-role]MEM  74%#[default]",
		},
		{
			name:    "CPU normal memory warning",
			metrics: systemstatus.Metrics{CPUPercent: intPointer(69), MemoryPercent: intPointer(75)},
			want:    " #[fg=secondary-role]CPU  69%#[default]  #[fg=warning-role]MEM  75%#[default]",
		},
		{
			name:    "CPU unknown memory critical",
			metrics: systemstatus.Metrics{MemoryPercent: intPointer(90)},
			want:    " #[fg=secondary-role]CPU  --%#[default]  #[fg=critical-role,bold]MEM  90%#[default]",
		},
		{
			name:    "CPU warning memory unknown",
			metrics: systemstatus.Metrics{CPUPercent: intPointer(70)},
			want:    " #[fg=warning-role]CPU  70%#[default]  #[fg=secondary-role]MEM  --%#[default]",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := formatLiveResourcesStatusWithRoles(test.metrics, roles); got != test.want {
				t.Fatalf("formatLiveResourcesStatusWithRoles() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFormatLiveResourcesStatusUnknownUsesOnlySecondaryStyle(t *testing.T) {
	t.Parallel()

	roles := theme.RenderRoles{
		StatusTextSecondary: "secondary-role",
		StateWarning:        "warning-role",
		StateCritical:       "critical-role",
	}
	if got := formatLiveResourcesStatusWithRoles(systemstatus.Metrics{}, roles); got != "" {
		t.Fatalf("formatLiveResourcesStatusWithRoles(empty) = %q, want empty", got)
	}
	for _, test := range []struct {
		metrics systemstatus.Metrics
		want    string
	}{
		{
			metrics: systemstatus.Metrics{MemoryPercent: intPointer(41)},
			want:    " #[fg=secondary-role]CPU  --%#[default]  #[fg=secondary-role]MEM  41%#[default]",
		},
		{
			metrics: systemstatus.Metrics{CPUPercent: intPointer(12)},
			want:    " #[fg=secondary-role]CPU  12%#[default]  #[fg=secondary-role]MEM  --%#[default]",
		},
	} {
		got := formatLiveResourcesStatusWithRoles(test.metrics, roles)
		if got != test.want {
			t.Fatalf("unknown metric output = %q, want %q", got, test.want)
		}
		if strings.Contains(got, "warning-role") || strings.Contains(got, "critical-role") || strings.Contains(got, "bold") {
			t.Fatalf("unknown metric gained warning/critical style: %q", got)
		}
		for _, forbidden := range []string{"normal", "warning", "critical", "unknown"} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("unknown metric output leaked severity word %q: %q", forbidden, got)
			}
		}
	}
}

func TestFormatLiveResourcesStatusUsesResolvedThemeRoles(t *testing.T) {
	t.Parallel()

	metrics := systemstatus.Metrics{CPUPercent: intPointer(75), MemoryPercent: intPointer(95)}
	for _, preset := range []string{"", "daylight"} {
		roles := theme.RenderRolesFromEffective(theme.ResolveTheme(theme.ThemeConfig{Preset: preset}))
		want := " #[fg=" + roles.StateWarning + "]CPU  75%#[default]  #[fg=" + roles.StateCritical + ",bold]MEM  95%#[default]"
		if got := formatLiveResourcesStatusWithRoles(metrics, roles); got != want {
			t.Fatalf("preset %q output = %q, want semantic roles %q", preset, got, want)
		}
	}
}

func TestLiveResourcePercentSlotsAndSegmentWidthStayFixed(t *testing.T) {
	t.Parallel()
	values := []*int{nil, intPointer(0), intPointer(9), intPointer(15), intPointer(69), intPointer(70), intPointer(74), intPointer(75), intPointer(89), intPointer(90), intPointer(99), intPointer(100)}
	for _, cfg := range []theme.ThemeConfig{{}, {Warning: "#112233", Critical: "#cc2233", Muted: "#667788", TextPrimary: "#ddeeff"}} {
		roles := theme.RenderRolesFromEffective(theme.ResolveTheme(cfg))
		var metricWidth, segmentWidth int
		for _, value := range values {
			cpu := formatLiveResourceMetric("CPU", value, liveResourceCPUWarningAt, roles)
			memory := formatLiveResourceMetric("MEM", value, liveResourceMemoryWarningAt, roles)
			segment := " " + cpu + "  " + memory
			cpuPlain, memoryPlain, segmentPlain := stripTmuxStyles(cpu), stripTmuxStyles(memory), stripTmuxStyles(segment)
			if !regexp.MustCompile(`^(CPU|MEM) ( {0,2}[0-9]{1,3}| --)%$`).MatchString(cpuPlain) || !regexp.MustCompile(`^(CPU|MEM) ( {0,2}[0-9]{1,3}| --)%$`).MatchString(memoryPlain) {
				t.Fatalf("value=%v slots cpu=%q memory=%q", value, cpuPlain, memoryPlain)
			}
			if metricWidth == 0 {
				metricWidth, segmentWidth = len([]rune(cpuPlain)), len([]rune(segmentPlain))
			}
			if len([]rune(cpuPlain)) != metricWidth || len([]rune(memoryPlain)) != metricWidth || len([]rune(segmentPlain)) != segmentWidth {
				t.Fatalf("value=%v visible widths cpu=%d memory=%d segment=%d, want %d/%d/%d (%q)", value, len([]rune(cpuPlain)), len([]rune(memoryPlain)), len([]rune(segmentPlain)), metricWidth, metricWidth, segmentWidth, segmentPlain)
			}
			for _, forbidden := range []string{"normal", "warning", "critical", "unknown"} {
				if strings.Contains(segmentPlain, forbidden) {
					t.Fatalf("value=%v segment leaked %q: %q", value, forbidden, segmentPlain)
				}
			}
		}
	}
}

var tmuxStylePattern = regexp.MustCompile(`#\[[^]]*\]`)

func stripTmuxStyles(value string) string { return tmuxStylePattern.ReplaceAllString(value, "") }

func TestSettingsLiveResourcesTogglePersistsAndUpdatesTmux(t *testing.T) {
	if !systemstatus.Supported() {
		t.Skip("live resources unsupported on this build")
	}
	home := t.TempDir()
	var calls [][]string
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "TMUX" {
				return "/tmp/tmux"
			}
			return ""
		},
		runCommand: func(name string, args ...string) error {
			calls = append(calls, append([]string{name}, args...))
			return nil
		},
	}
	wireSettingsLiveTestRunner(cmd)
	if mode, source, supported := cmd.currentLiveResourcesMode(); mode != config.LiveResourcesOff || source != "default" || !supported {
		t.Fatalf("default currentLiveResourcesMode() = %q, %q, %v", mode, source, supported)
	}
	if !hasEntryValue(cmd.statusBarEntries(), settingsActionPrefixLiveResources+string(config.LiveResourcesOn)) {
		t.Fatalf("Status Bar entries do not expose the Resources on action: %#v", cmd.statusBarEntries())
	}
	if err := cmd.setLiveResourcesMode("on"); err != nil {
		t.Fatalf("setLiveResourcesMode(on) error = %v", err)
	}
	paths, err := config.Homes{HomeDir: home}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := config.LoadLiveResourcesFile(paths.LiveResourcesFile()); err != nil || got != config.LiveResourcesOn {
		t.Fatalf("saved live resources = %q, %v, want on", got, err)
	}
	configPath := filepath.Join(home, ".config", "projmux", "tmux.conf")
	if !reflect.DeepEqual(calls, [][]string{{"tmux", "source-file", configPath}}) {
		t.Fatalf("tmux calls = %#v", calls)
	}
	if generated, err := os.ReadFile(configPath); err != nil || !strings.Contains(string(generated), "range=user|resources") {
		t.Fatalf("generated live-resources config = %q, %v; want enabled segment", generated, err)
	}
	if !hasEntryValue(cmd.statusBarEntries(), settingsActionPrefixLiveResources+string(config.LiveResourcesOff)) {
		t.Fatalf("Status Bar entries do not expose the Resources off action: %#v", cmd.statusBarEntries())
	}
	if err := os.WriteFile(paths.LiveResourcesFile(), []byte("sometimes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if mode, source, supported := cmd.currentLiveResourcesMode(); mode != config.LiveResourcesOff || source != "default (invalid saved value ignored)" || !supported {
		t.Fatalf("invalid currentLiveResourcesMode() = %q, %q, %v", mode, source, supported)
	}
}

func TestSettingsLiveResourcesUsesKoreanCatalog(t *testing.T) {
	if !systemstatus.Supported() {
		t.Skip("live resources unsupported on this build")
	}
	home := t.TempDir()
	cmd := &settingsCommand{
		homeDir: func() (string, error) { return home, nil },
		lookupEnv: func(name string) string {
			if name == "PROJMUX_LOCALE" {
				return "ko-KR"
			}
			return ""
		},
	}
	entries := cmd.statusBarEntries()
	if !hasEntryLabelContainingAll(entries, "리소스", "꺼짐") {
		t.Fatalf("Korean Status Bar entries = %#v", entries)
	}
}

func TestTmuxPrintConfigLoadsLiveResourcesModeAndPlacesSegment(t *testing.T) {
	if !systemstatus.Supported() {
		t.Skip("live resources unsupported on this build")
	}
	home := t.TempDir()
	paths, err := config.Homes{HomeDir: home}.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveLiveResourcesFile(paths.LiveResourcesFile(), config.LiveResourcesOn); err != nil {
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
		t.Fatalf("print-config error = %v", err)
	}
	output := stdout.String()
	for _, want := range []string{
		"set -g " + liveResourcesTmuxOption + " on",
		"#[range=user|resources]#('/tmp/projmux' internal status resources)#[norange]",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("print-config output missing %q: %s", want, output)
		}
	}
	gitAt := strings.Index(output, "status git")
	resourcesAt := strings.Index(output, "status resources")
	clockAt := strings.Index(output, " %Y-%m-%d %H:%M")
	if gitAt < 0 || resourcesAt < gitAt || clockAt < resourcesAt {
		t.Fatalf("status-right ordering git=%d resources=%d clock=%d", gitAt, resourcesAt, clockAt)
	}
}

func TestGeneratedTmuxConfigsPreserveLiveResourcesModesAndOrdering(t *testing.T) {
	t.Parallel()

	source := fallbackRenderThemeSource()
	for _, mode := range []config.LiveResourcesMode{config.LiveResourcesOff, config.LiveResourcesOn} {
		configs := map[string]string{
			"standalone": source.tmuxStandaloneConfigWithAIBadgeStyleDesktopNotifyModeAndLiveResources(
				"/tmp/projmux", statusbarDecorationSet{}, config.AIBadgeStyleDot, config.DefaultDesktopNotifyMode, mode, defaultKeyBindingCatalog(), false,
			),
			"app": source.tmuxAppConfigWithAIBadgeStyleDesktopNotifyModeAndLiveResources(
				"/tmp/projmux", "/bin/sh", statusbarDecorationSet{}, config.AIBadgeStyleDot, config.DefaultDesktopNotifyMode, mode, defaultKeyBindingCatalog(), false,
			),
		}
		for name, output := range configs {
			t.Run(name+"/"+string(mode), func(t *testing.T) {
				if !strings.Contains(output, "set -g "+liveResourcesTmuxOption+" "+string(mode)) {
					t.Fatalf("generated config missing %s mode %q", name, mode)
				}
				segment := "#[range=user|resources]#('/tmp/projmux' internal status resources)#[norange]"
				if got := strings.Contains(output, segment); got != (mode == config.LiveResourcesOn) {
					t.Fatalf("generated config resources segment present = %v for mode %s", got, mode)
				}
				gitAt := strings.Index(output, "status git")
				resourcesAt := strings.Index(output, "status resources")
				clockAt := strings.Index(output, " %Y-%m-%d %H:%M")
				if gitAt < 0 || clockAt < gitAt || (mode == config.LiveResourcesOn && (resourcesAt < gitAt || clockAt < resourcesAt)) || (mode == config.LiveResourcesOff && resourcesAt >= 0) {
					t.Fatalf("%s/%s status-right ordering git=%d resources=%d clock=%d", name, mode, gitAt, resourcesAt, clockAt)
				}
			})
		}
	}
}
