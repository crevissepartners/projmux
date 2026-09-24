package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/hooks"
)

// centralSettingsTestHome returns the only inputs the central settings take:
// a temp home resolver and an empty env resolver.
func centralSettingsTestHome(t *testing.T) (func() (string, error), func(string) string) {
	t.Helper()
	home := t.TempDir()
	return func() (string, error) { return home, nil }, func(string) string { return "" }
}

func TestCentralSettingsRoundTripWithoutSettingsCommand(t *testing.T) {
	t.Parallel()

	homeDir, lookupEnv := centralSettingsTestHome(t)

	setting, _, err := loadCentralLocaleSetting(homeDir, lookupEnv)
	if err != nil {
		t.Fatalf("loadCentralLocaleSetting() before save error = %v", err)
	}
	if setting != i18n.LocaleSettingAuto {
		t.Fatalf("locale before save = %q, want %q", setting, i18n.LocaleSettingAuto)
	}
	if got := loadCentralAgentQuestionAnswering(homeDir, lookupEnv); got != config.AgentQuestionAnsweringClaude {
		t.Fatalf("answering before save = %q, want %q", got, config.AgentQuestionAnsweringClaude)
	}
	if got := loadCentralAgentQuestionWindowSeconds(homeDir, lookupEnv); got != config.DefaultAgentQuestionWindowSeconds {
		t.Fatalf("window before save = %d, want %d", got, config.DefaultAgentQuestionWindowSeconds)
	}
	if config.DefaultAgentQuestionWindowSeconds != 900 {
		t.Fatalf("DefaultAgentQuestionWindowSeconds = %d, want 900", config.DefaultAgentQuestionWindowSeconds)
	}

	saved, err := saveCentralLocale(homeDir, lookupEnv, "ko-KR")
	if err != nil {
		t.Fatalf("saveCentralLocale(ko-KR) error = %v", err)
	}
	if saved != "ko-KR" {
		t.Fatalf("saveCentralLocale(ko-KR) = %q, want ko-KR", saved)
	}
	setting, source, err := loadCentralLocaleSetting(homeDir, lookupEnv)
	if err != nil {
		t.Fatalf("loadCentralLocaleSetting() error = %v", err)
	}
	if setting != "ko-KR" {
		t.Fatalf("locale after save = %q, want ko-KR", setting)
	}
	home, _ := homeDir()
	if want := filepath.Join(home, ".config", "projmux", "config.toml"); source != want {
		t.Fatalf("locale source = %q, want %q", source, want)
	}

	way, err := saveCentralAgentQuestionAnswering(homeDir, lookupEnv, config.AgentQuestionAnsweringProjmux)
	if err != nil {
		t.Fatalf("saveCentralAgentQuestionAnswering(projmux) error = %v", err)
	}
	if way != config.AgentQuestionAnsweringProjmux {
		t.Fatalf("saveCentralAgentQuestionAnswering(projmux) = %q, want projmux", way)
	}
	if got := loadCentralAgentQuestionAnswering(homeDir, lookupEnv); got != config.AgentQuestionAnsweringProjmux {
		t.Fatalf("answering after save = %q, want projmux", got)
	}

	for _, seconds := range []int{120, config.UnlimitedAgentQuestionWindowSeconds} {
		if err := saveCentralAgentQuestionWindowSeconds(homeDir, lookupEnv, seconds); err != nil {
			t.Fatalf("saveCentralAgentQuestionWindowSeconds(%d) error = %v", seconds, err)
		}
		if got := loadCentralAgentQuestionWindowSeconds(homeDir, lookupEnv); got != seconds {
			t.Fatalf("window after save = %d, want %d", got, seconds)
		}
	}
}

// centralSettingKind names one of the three central settings.
type centralSettingKind string

const (
	centralSettingLocale    centralSettingKind = "locale"
	centralSettingAnswering centralSettingKind = "answering"
	centralSettingWindow    centralSettingKind = "window"
)

// centralSettingFile is where kind is stored under the given resolvers.
func centralSettingFile(t *testing.T, kind centralSettingKind, homeDir func() (string, error), lookupEnv func(string) string) string {
	t.Helper()
	if kind == centralSettingLocale {
		path, err := hooks.GlobalConfigPath(lookupEnv, homeDir)
		if err != nil {
			t.Fatal(err)
		}
		return path
	}
	paths, err := configPaths(homeDir, lookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	if kind == centralSettingAnswering {
		return paths.AgentQuestionAnsweringFile()
	}
	return paths.AgentQuestionWindowSecondsFile()
}

func centralSettingWindowValue(t *testing.T, value string) int {
	t.Helper()
	if value == config.AgentQuestionWindowUnlimitedWord {
		return config.UnlimitedAgentQuestionWindowSeconds
	}
	seconds, err := strconv.Atoi(value)
	if err != nil {
		t.Fatal(err)
	}
	return seconds
}

// saveCentralSettingDirect stores value through the central API alone.
func saveCentralSettingDirect(t *testing.T, kind centralSettingKind, homeDir func() (string, error), lookupEnv func(string) string, value string) error {
	t.Helper()
	switch kind {
	case centralSettingLocale:
		_, err := saveCentralLocale(homeDir, lookupEnv, value)
		return err
	case centralSettingAnswering:
		_, err := saveCentralAgentQuestionAnswering(homeDir, lookupEnv, config.AgentQuestionAnswering(value))
		return err
	default:
		return saveCentralAgentQuestionWindowSeconds(homeDir, lookupEnv, centralSettingWindowValue(t, value))
	}
}

// saveCentralSettingThroughTUI stores value through the settings command.
func saveCentralSettingThroughTUI(t *testing.T, kind centralSettingKind, homeDir func() (string, error), lookupEnv func(string) string, value string) error {
	t.Helper()
	cmd := &settingsCommand{
		homeDir:    homeDir,
		lookupEnv:  lookupEnv,
		runCommand: func(string, ...string) error { return nil },
	}
	switch kind {
	case centralSettingLocale:
		return cmd.setGlobalLocale(value)
	case centralSettingAnswering:
		return cmd.setAgentQuestionAnswering(config.AgentQuestionAnswering(value), &bytes.Buffer{})
	default:
		return cmd.setAgentQuestionWindowSeconds(centralSettingWindowValue(t, value), &bytes.Buffer{})
	}
}

func TestCentralSettingsStoreTheSameBytesAsTheSettingsCommand(t *testing.T) {
	t.Parallel()

	unlimited := config.AgentQuestionWindowUnlimitedWord
	tests := []struct {
		name    string
		kind    centralSettingKind
		prior   string // saved first through the same path when set
		value   string
		wantErr bool
	}{
		{name: "locale auto", kind: centralSettingLocale, value: i18n.LocaleSettingAuto},
		{name: "locale en-US", kind: centralSettingLocale, value: string(i18n.FallbackLocale)},
		{name: "locale ko-KR", kind: centralSettingLocale, value: "ko-KR"},
		{name: "locale trimmed", kind: centralSettingLocale, value: " ko-KR "},
		{name: "answering claude", kind: centralSettingAnswering, value: string(config.AgentQuestionAnsweringClaude)},
		{name: "answering projmux", kind: centralSettingAnswering, value: string(config.AgentQuestionAnsweringProjmux)},
		{name: "answering upper case", kind: centralSettingAnswering, value: "PROJMUX"},
		{name: "answering whitespace", kind: centralSettingAnswering, value: "  Projmux \n"},
		{name: "window 60", kind: centralSettingWindow, value: "60"},
		{name: "window 900", kind: centralSettingWindow, value: "900"},
		{name: "window 3600", kind: centralSettingWindow, value: "3600"},
		{name: "window unlimited", kind: centralSettingWindow, value: unlimited},
		{name: "locale fr-FR refused", kind: centralSettingLocale, value: "fr-FR", wantErr: true},
		{name: "locale empty refused", kind: centralSettingLocale, value: "", wantErr: true},
		{name: "window 59 refused", kind: centralSettingWindow, value: "59", wantErr: true},
		{name: "window 3601 refused", kind: centralSettingWindow, value: "3601", wantErr: true},
		{name: "window 0 refused", kind: centralSettingWindow, value: "0", wantErr: true},
		{name: "window -1 refused", kind: centralSettingWindow, value: "-1", wantErr: true},
		{name: "locale refused keeps saved", kind: centralSettingLocale, prior: "ko-KR", value: "fr-FR", wantErr: true},
		{name: "window refused keeps saved", kind: centralSettingWindow, prior: "120", value: "59", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fronts := []struct {
				name string
				save func(*testing.T, centralSettingKind, func() (string, error), func(string) string, string) error
			}{
				{name: "central", save: saveCentralSettingDirect},
				{name: "settings command", save: saveCentralSettingThroughTUI},
			}
			stored := make([][]byte, len(fronts))
			for i, front := range fronts {
				homeDir, lookupEnv := centralSettingsTestHome(t)
				path := centralSettingFile(t, tt.kind, homeDir, lookupEnv)
				var before []byte
				if tt.prior != "" {
					if err := front.save(t, tt.kind, homeDir, lookupEnv, tt.prior); err != nil {
						t.Fatalf("%s: prior save %q error = %v", front.name, tt.prior, err)
					}
					data, err := os.ReadFile(path)
					if err != nil {
						t.Fatalf("%s: read prior file: %v", front.name, err)
					}
					before = data
				}

				err := front.save(t, tt.kind, homeDir, lookupEnv, tt.value)
				if tt.wantErr {
					if err == nil {
						t.Fatalf("%s: save %q error = nil, want refusal", front.name, tt.value)
					}
					if tt.prior == "" {
						if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
							t.Fatalf("%s: refused save left %s (stat error = %v), want no file", front.name, path, statErr)
						}
						continue
					}
					after, readErr := os.ReadFile(path)
					if readErr != nil {
						t.Fatalf("%s: read file after refusal: %v", front.name, readErr)
					}
					if !bytes.Equal(after, before) {
						t.Fatalf("%s: refused save changed the file:\nbefore %q\nafter  %q", front.name, before, after)
					}
					stored[i] = after
					continue
				}
				if err != nil {
					t.Fatalf("%s: save %q error = %v", front.name, tt.value, err)
				}
				data, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatalf("%s: read saved file: %v", front.name, readErr)
				}
				stored[i] = data
			}
			if !bytes.Equal(stored[0], stored[1]) {
				t.Fatalf("stored bytes differ:\ncentral          %q\nsettings command %q", stored[0], stored[1])
			}
		})
	}
}

// TestCentralSettingsReadNoFrontSetting holds the central API to its layer:
// saving and loading all three central settings reports no front read.
//
// No t.Parallel: it installs the package-level front-read observer.
func TestCentralSettingsReadNoFrontSetting(t *testing.T) {
	homeDir, lookupEnv := centralSettingsTestHome(t)

	recorder := &frontReadRecorder{}
	restore := config.ObserveFrontReads(recorder.record)
	t.Cleanup(restore)

	if _, err := saveCentralLocale(homeDir, lookupEnv, "ko-KR"); err != nil {
		t.Fatalf("saveCentralLocale() error = %v", err)
	}
	if _, _, err := loadCentralLocaleSetting(homeDir, lookupEnv); err != nil {
		t.Fatalf("loadCentralLocaleSetting() error = %v", err)
	}
	if _, err := saveCentralAgentQuestionAnswering(homeDir, lookupEnv, config.AgentQuestionAnsweringProjmux); err != nil {
		t.Fatalf("saveCentralAgentQuestionAnswering() error = %v", err)
	}
	_ = loadCentralAgentQuestionAnswering(homeDir, lookupEnv)
	if err := saveCentralAgentQuestionWindowSeconds(homeDir, lookupEnv, 120); err != nil {
		t.Fatalf("saveCentralAgentQuestionWindowSeconds() error = %v", err)
	}
	_ = loadCentralAgentQuestionWindowSeconds(homeDir, lookupEnv)

	if reads := recorder.take(); len(reads) != 0 {
		t.Fatalf("central settings made %d front reads, want none: %+v", len(reads), reads)
	}

	// The zero above counts only if the observer is live.
	config.NoteFrontRead(config.TmuxAISplitModeFileName, "probe")
	if reads := recorder.take(); len(reads) != 1 {
		t.Fatalf("probe front read recorded %d reads, want 1", len(reads))
	}
}
