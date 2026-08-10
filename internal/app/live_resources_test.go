package app

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/systemstatus"
)

func intPointer(value int) *int { return &value }

func TestFormatLiveResourcesStatus(t *testing.T) {
	t.Parallel()

	if got := formatLiveResourcesStatus(systemstatus.Metrics{}); got != "" {
		t.Fatalf("formatLiveResourcesStatus(empty) = %q, want empty", got)
	}
	got := formatLiveResourcesStatus(systemstatus.Metrics{MemoryPercent: intPointer(41)})
	if !strings.Contains(got, "CPU --%  MEM 41%") || !strings.HasSuffix(got, "#[default]") {
		t.Fatalf("first resource status = %q", got)
	}
	got = formatLiveResourcesStatus(systemstatus.Metrics{CPUPercent: intPointer(12), MemoryPercent: intPointer(41)})
	if !strings.Contains(got, "CPU 12%  MEM 41%") {
		t.Fatalf("resource status = %q", got)
	}
}

func TestSettingsLiveResourcesTogglePersistsAndUpdatesTmux(t *testing.T) {
	if !systemstatus.Supported() {
		t.Skip("Linux/WSL-only setting")
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
	if mode, source, supported := cmd.currentLiveResourcesMode(); mode != config.LiveResourcesOff || source != "default" || !supported {
		t.Fatalf("default currentLiveResourcesMode() = %q, %q, %v", mode, source, supported)
	}
	if !hasEntryValue(cmd.labsEntries(), settingsActionPrefixLiveResources+string(config.LiveResourcesOn)) {
		t.Fatalf("Labs entries do not expose live resources on action: %#v", cmd.labsEntries())
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
	if !reflect.DeepEqual(calls, [][]string{{"tmux", "set-option", "-g", liveResourcesTmuxOption, "on"}}) {
		t.Fatalf("tmux calls = %#v", calls)
	}
	if !hasEntryValue(cmd.labsEntries(), settingsActionPrefixLiveResources+string(config.LiveResourcesOff)) {
		t.Fatalf("Labs entries do not expose live resources off action: %#v", cmd.labsEntries())
	}
}

func TestSettingsLiveResourcesUsesKoreanCatalog(t *testing.T) {
	if !systemstatus.Supported() {
		t.Skip("Linux/WSL-only setting")
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
	entries := cmd.labsEntries()
	if !hasEntryLabelContainingAll(entries, "실시간 시스템 리소스", "꺼짐", "Linux/WSL 게스트 기준") {
		t.Fatalf("Korean Labs entries = %#v", entries)
	}
}

func TestTmuxPrintConfigLoadsLiveResourcesModeAndPlacesSegment(t *testing.T) {
	if !systemstatus.Supported() {
		t.Skip("Linux/WSL-only config")
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
		"#{?#{==:#{" + liveResourcesTmuxOption + "},on},#('/tmp/projmux' status resources),}",
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
