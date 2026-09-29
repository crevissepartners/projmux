//go:build linux

package tmux

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
)

// TestFormatFieldsSurviveCallerWithoutUTF8Locale runs the production
// ExecRunner against an isolated real tmux server from a caller with no UTF-8
// locale. Such a tmux client rewrites the \x1f and \t field separators (and
// any non-ASCII byte) of format output to "_", so the created-session tuple
// check failed with "malformed owner row" and list rows lost their fields.
// The runner starts tmux through tmuxexec, whose UTF-8 client flag makes the
// output match a UTF-8 caller's.
func TestFormatFieldsSurviveCallerWithoutUTF8Locale(t *testing.T) {
	requireIsolatedTmuxSmoke(t, "PROJMUX_FORMAT_LOCALE_SMOKE")

	locales := []struct {
		name string
		set  map[string]string
	}{
		{name: "UTF-8", set: map[string]string{"LANG": "C.UTF-8"}},
		{name: "no locale"},
		{name: "LC_ALL=C over a UTF-8 LANG", set: map[string]string{"LC_ALL": "C", "LANG": "C.UTF-8"}},
	}
	for _, locale := range locales {
		t.Run(locale.name, func(t *testing.T) {
			smokeRoot := isolatedTmuxSmokeRoot(t, "pmx-locale-")
			for _, name := range []string{"LANG", "LC_ALL", "LC_CTYPE", "TMUX", "TMUX_PANE", "__PROJMUX_RUNTIME_ANCHOR_PANE"} {
				t.Setenv(name, "")
				if err := os.Unsetenv(name); err != nil {
					t.Fatal(err)
				}
			}
			for name, value := range locale.set {
				t.Setenv(name, value)
			}
			t.Setenv("TMUX_TMPDIR", smokeRoot)

			ctx := context.Background()
			runner := ExecRunner{}
			const sessionName = "locale-é"
			const marker = "op-locale-smoke"
			created, err := runner.Run(ctx, "tmux", "-f", "/dev/null", "new-session", "-d", "-s", sessionName, "-x", "80", "-y", "24",
				"-P", "-F", tmuxFormat("#{session_id}", "#{window_id}", "#{pane_id}"), "/usr/bin/sleep 60")
			if err != nil {
				t.Fatalf("start isolated tmux: %v: %s", err, created)
			}
			t.Cleanup(func() { cleanupLocaleSmoke(t, runner, smokeRoot) })
			tuple := splitTmuxFields(strings.TrimSpace(string(created)), 3)
			if len(tuple) != 3 {
				t.Fatalf("new-session -P row %q split into %d fields, want 3", strings.TrimSpace(string(created)), len(tuple))
			}
			if output, err := runner.Run(ctx, "tmux", "set-environment", "-t", tuple[0], createOperationEnvironment, marker); err != nil {
				t.Fatalf("mark isolated session: %v: %s", err, output)
			}
			client := NewClient(runner)
			result := intmux.NewSessionResult{Created: true, SessionID: tuple[0], WindowID: tuple[1], PaneID: tuple[2]}
			if err := client.verifyCreatedSessionOwnership(ctx, result, marker); err != nil {
				t.Fatalf("verify created session tuple: %v", err)
			}
			summaries, err := client.RecentSessionSummaries(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(summaries) != 1 || summaries[0].Name != sessionName {
				t.Fatalf("recent session summaries = %#v, want one named %q", summaries, sessionName)
			}
			// Tab-separated rows (the list-windows and list-clients readers) and
			// \x1f rows both keep their fields and the UTF-8 value. tmux 3.4
			// prints a raw \x1f in a display-message format as the four
			// characters \037 and 3.6 prints the byte; splitTmuxFields reads both.
			tabRow, err := runner.Run(ctx, "tmux", "display-message", "-p", "-t", tuple[0], "#{session_name}\t#{window_id}\t#{pane_id}")
			if err != nil {
				t.Fatal(err)
			}
			want := []string{sessionName, tuple[1], tuple[2]}
			if fields := strings.Split(strings.TrimRight(string(tabRow), "\n"), "\t"); !slices.Equal(fields, want) {
				t.Fatalf("tab row %q split into %q, want %q", tabRow, fields, want)
			}
			sepRow, err := runner.Run(ctx, "tmux", "display-message", "-p", "-t", tuple[0], tmuxFormat("#{session_name}", "#{window_id}", "#{pane_id}"))
			if err != nil {
				t.Fatal(err)
			}
			if fields := splitTmuxFields(strings.TrimRight(string(sepRow), "\n"), 3); !slices.Equal(fields, want) {
				t.Fatalf("separator row %q split into %q, want %q", sepRow, fields, want)
			}
		})
	}
}

func TestFormatLocaleSmokeSkipsOnlyThroughTheGate(t *testing.T) {
	auditIsolatedTmuxSmokeGate(t, "format_locale_smoke_linux_test.go", map[string]string{
		"TestFormatFieldsSurviveCallerWithoutUTF8Locale": "PROJMUX_FORMAT_LOCALE_SMOKE",
	})
}

func cleanupLocaleSmoke(t *testing.T, runner ExecRunner, smokeRoot string) {
	t.Helper()
	output, err := runner.Run(context.Background(), "tmux", "display-message", "-p", "#{socket_path}")
	if err != nil {
		t.Errorf("query isolated tmux socket before cleanup: %v: %s", err, output)
		return
	}
	socketPath := strings.TrimSpace(string(output))
	rel, err := filepath.Rel(smokeRoot, socketPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Errorf("refuse cleanup outside smoke root %q: socket=%q", smokeRoot, socketPath)
		return
	}
	if output, err := runner.Run(context.Background(), "tmux", "kill-server"); err != nil {
		t.Errorf("kill isolated tmux server %q: %v: %s", socketPath, err, output)
	}
}
