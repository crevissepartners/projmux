package app

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/i18n"
)

func TestSharedFacadesLocalization(t *testing.T) {
	for _, tc := range []struct {
		locale         i18n.Locale
		key            i18n.Key
		fallback, want string
	}{
		{"en-US", i18n.KeyPickerRecorderCancelPending, "unused", "Not saved yet. Esc cancels without changes."},
		{"ko-KR", i18n.KeyPickerRecorderCancelPending, "unused", "아직 저장되지 않았습니다. Esc는 변경 없이 취소합니다."},
		{"ko-KR", "missing.shared.facade", "  exact fallback\n", "  exact fallback\n"},
	} {
		for name, localize := range map[string]func(i18n.Locale, i18n.Key, string) string{"facade": LocalizeText, "compat": localizeText} {
			if got := localize(tc.locale, tc.key, tc.fallback); got != tc.want {
				t.Errorf("%s(%q,%q)=%q want %q", name, tc.locale, tc.key, got, tc.want)
			}
		}
	}
}

func TestSharedFacadesUsageError(t *testing.T) {
	for name, factory := range map[string]func(string) error{"facade": NewUsageError, "compat": usageError} {
		original := factory("  invalid input\n")
		wrapped := fmt.Errorf("context: %w", original)
		var usage *UsageError
		if !errors.As(wrapped, &usage) || usage.Message != "  invalid input\n" || original.Error() != usage.Message {
			t.Fatalf("%s changed error: %#v", name, original)
		}
		if !usage.MetadataUsageError() || !coremetadata.IsUsageError(wrapped) || !IsUsageError(wrapped) {
			t.Fatalf("%s lost usage classification", name)
		}
		failure := cli.ClassifyFailure(wrapped, IsUsageError(wrapped))
		if failure.ExitCode != 2 || !failure.Print {
			t.Fatalf("%s failure=%+v", name, failure)
		}
	}
	var out, errout bytes.Buffer
	err := (&settingsCommand{}).Run([]string{"extra"}, &out, &errout)
	if err == nil || err.Error() != "settings does not accept positional arguments" || cli.ClassifyFailure(err, IsUsageError(err)).ExitCode != 2 || out.Len() != 0 || !strings.Contains(errout.String(), "settings") {
		t.Fatalf("settings usage: err=%v stdout=%q stderr=%q", err, out.String(), errout.String())
	}
}

func TestSharedFacadesInjectedEnvironment(t *testing.T) {
	t.Setenv("PROJMUX_CWD", "/global-must-not-be-used")
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	value := ""
	var homeErr error
	c := &settingsCommand{homeDir: func() (string, error) { return home, homeErr }, lookupEnv: func(key string) string {
		if key == "PROJMUX_CWD" {
			return value
		}
		return ""
	}}
	if got, err := c.HomeDir(); got != home || err != nil {
		t.Fatalf("home=(%q,%v)", got, err)
	}
	for _, next := range []string{"", filepath.Join(home, "first"), filepath.Join(home, "second"), ""} {
		value = next
		if got := c.LookupEnv("PROJMUX_CWD"); got != next {
			t.Fatalf("env=%q want %q", got, next)
		}
		if got := c.resolveSettingsProjectContext().Path; got != next {
			t.Fatalf("settings project=%q want %q", got, next)
		}
	}
	// The actual settings config writer must preserve the injected error before
	// attempting any filesystem or tmux work.
	homeErr = errors.New("injected home failure")
	if got, err := c.HomeDir(); got != home || err != homeErr {
		t.Fatalf("home error=(%q,%v)", got, err)
	}
	if got, err := c.writeTmuxAppConfig(); got != "" || !errors.Is(err, homeErr) || err.Error() != "resolve home directory: injected home failure" {
		t.Fatalf("settings writer=(%q,%v)", got, err)
	}
	homeErr = nil
	path, err := c.writeTmuxAppConfig()
	if want := filepath.Join(home, ".config", "projmux", "tmux.conf"); err != nil || path != want {
		t.Fatalf("settings writer=(%q,%v), want %q", path, err, want)
	}
}

func TestSharedFacadesHookCallerLocalization(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	child := filepath.Join(root, "nested")
	if err := os.MkdirAll(child, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".projmux", "config.toml"), "# empty project config\n")
	for _, tc := range []struct{ locale, prefix string }{{"en-US", "note: sessions created in "}, {"ko-KR", "참고: "}} {
		c := newHookCommand(func() (string, error) { return home, nil }, func(key string) string {
			if key == i18n.LocaleEnvName {
				return tc.locale
			}
			return ""
		}, func() (string, error) { return child, nil }, strings.NewReader(""), nil)
		var out, errout bytes.Buffer
		if err := c.Run([]string{"list", "--project"}, &out, &errout); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), tc.prefix+child) || !strings.Contains(out.String(), root) || errout.Len() != 0 {
			t.Fatalf("%s hook output=%q stderr=%q", tc.locale, out.String(), errout.String())
		}
	}
}
