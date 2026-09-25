package app

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
)

// leftoverArgsTestApp extends the runtime/window fixture with the remaining
// public routes whose leftover-argument and unknown-subcommand refusals are
// pinned below. Every refusal returns before a leaf reaches tmux, a store, a
// picker, the network, or a provider config.
func leftoverArgsTestApp() *App {
	app := runtimeWindowFlagParseTestApp()
	app.agent = &agentCommand{
		ai:    &aiCommand{},
		usage: usagecmd.New(func() time.Time { return time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC) }),
	}
	app.diagnostics = &diagnosticsCommand{ai: &aiCommand{}}
	app.pin = &pinCommand{}
	app.update = &updateCommand{}
	app.settings = &settingsCommand{}
	return app
}

// TestPublicRoutesLeftoverArgsAndUnknownSubcommandsAreUsageErrors pins that a
// public route handed an argument it does not accept, an unknown subcommand, or
// a bare parent call exits 2 (usage error) with its message unchanged. The
// forms are the ones measured at exit 1 before the fix: A `<route> --zz`,
// B `<route> x --zz`, C `<route> x`, D bare parent.
func TestPublicRoutesLeftoverArgsAndUnknownSubcommandsAreUsageErrors(t *testing.T) {
	isolateRuntimeWindowFlagParseEnv(t)

	for _, test := range []struct {
		name string
		argv []string
		want string
	}{
		// agent integrate: a leading flag or unknown token is read as the agent kind.
		{name: "agent integrate D", argv: []string{"agent", "integrate"}, want: "agent integrate requires <agent-kind>"},
		{name: "agent integrate A", argv: []string{"agent", "integrate", "--zz"}, want: "unknown agent integrate agent-kind: --zz"},
		{name: "agent integrate B", argv: []string{"agent", "integrate", "x", "--zz"}, want: "unknown agent integrate agent-kind: x"},
		{name: "agent integrate C", argv: []string{"agent", "integrate", "x"}, want: "unknown agent integrate agent-kind: x"},
		{name: "agent integrate claude positional", argv: []string{"agent", "integrate", "claude", "x"}, want: "agent integrate claude does not accept positional arguments"},
		{name: "agent integrate codex positional", argv: []string{"agent", "integrate", "codex", "x"}, want: "agent integrate codex does not accept positional arguments"},
		{name: "agent integrate tmux-bell positional", argv: []string{"agent", "integrate", "tmux-bell", "x"}, want: "agent integrate tmux-bell does not accept positional arguments"},
		{name: "agent integrate antigravity positional", argv: []string{"agent", "integrate", "antigravity", "x"}, want: "agent integrate antigravity does not accept positional arguments"},
		// agent usage (usagecmd marks its own refusal through the metadata marker).
		{name: "agent usage B", argv: []string{"agent", "usage", "x", "--zz"}, want: "agent usage does not accept positional arguments"},
		{name: "agent usage C", argv: []string{"agent", "usage", "x"}, want: "agent usage does not accept positional arguments"},
		// diagnostics agent-hook
		{name: "diagnostics agent-hook B", argv: []string{"diagnostics", "agent-hook", "x", "--zz"}, want: "diagnostics agent-hook does not accept positional arguments"},
		{name: "diagnostics agent-hook C", argv: []string{"diagnostics", "agent-hook", "x"}, want: "diagnostics agent-hook does not accept positional arguments"},
		// pin project
		{name: "pin project D", argv: []string{"pin", "project"}, want: "pin project requires a subcommand"},
		{name: "pin project B", argv: []string{"pin", "project", "x", "--zz"}, want: "unknown pin project subcommand: x"},
		{name: "pin project C", argv: []string{"pin", "project", "x"}, want: "unknown pin project subcommand: x"},
		{name: "pin project project", argv: []string{"pin", "project", "project"}, want: "unknown pin project subcommand: project"},
		{name: "pin project list positional", argv: []string{"pin", "project", "list", "x"}, want: "pin project list does not accept positional arguments"},
		{name: "pin project clear positional", argv: []string{"pin", "project", "clear", "x"}, want: "pin project clear does not accept positional arguments"},
		{name: "pin project migrate positional", argv: []string{"pin", "project", "migrate", "x"}, want: "pin project migrate does not accept positional arguments"},
		// quit
		{name: "quit B", argv: []string{"quit", "x", "--zz"}, want: "quit does not accept positional arguments"},
		{name: "quit C", argv: []string{"quit", "x"}, want: "quit does not accept positional arguments"},
		// runtime
		{name: "runtime sessions B", argv: []string{"runtime", "sessions", "x", "--zz"}, want: "runtime sessions does not accept positional arguments"},
		{name: "runtime sessions C", argv: []string{"runtime", "sessions", "x"}, want: "runtime sessions does not accept positional arguments"},
		{name: "runtime attach B", argv: []string{"runtime", "attach", "x", "--zz"}, want: "runtime attach does not accept positional arguments"},
		{name: "runtime attach C", argv: []string{"runtime", "attach", "x"}, want: "runtime attach does not accept positional arguments"},
		{name: "runtime tag B", argv: []string{"runtime", "tag", "x", "--zz"}, want: "unknown runtime tag subcommand: x"},
		{name: "runtime tag C", argv: []string{"runtime", "tag", "x"}, want: "unknown runtime tag subcommand: x"},
		{name: "runtime tag project project", argv: []string{"runtime", "tag", "project", "project"}, want: "unknown runtime tag subcommand: project"},
		{name: "runtime tag list positional", argv: []string{"runtime", "tag", "list", "x"}, want: "runtime tag list does not accept positional arguments"},
		{name: "runtime tag clear positional", argv: []string{"runtime", "tag", "clear", "x"}, want: "runtime tag clear does not accept positional arguments"},
		{name: "runtime prune B", argv: []string{"runtime", "prune", "x", "--zz"}, want: "runtime prune does not accept positional arguments"},
		{name: "runtime prune C", argv: []string{"runtime", "prune", "x"}, want: "runtime prune does not accept positional arguments"},
		// settings: any argument, including an unknown flag, is a leftover.
		{name: "settings A", argv: []string{"settings", "--zz"}, want: "settings does not accept positional arguments"},
		{name: "settings B", argv: []string{"settings", "x", "--zz"}, want: "settings does not accept positional arguments"},
		{name: "settings C", argv: []string{"settings", "x"}, want: "settings does not accept positional arguments"},
		// shell, welcome
		{name: "shell B", argv: []string{"shell", "x", "--zz"}, want: "shell does not accept positional arguments"},
		{name: "shell C", argv: []string{"shell", "x"}, want: "shell does not accept positional arguments"},
		{name: "welcome B", argv: []string{"welcome", "x", "--zz"}, want: "welcome does not accept positional arguments"},
		{name: "welcome C", argv: []string{"welcome", "x"}, want: "welcome does not accept positional arguments"},
		// update
		{name: "update A", argv: []string{"update", "--zz"}, want: "unknown update subcommand: --zz"},
		{name: "update B", argv: []string{"update", "x", "--zz"}, want: "unknown update subcommand: x"},
		{name: "update C", argv: []string{"update", "x"}, want: "unknown update subcommand: x"},
		{name: "update D", argv: []string{"update"}, want: "update requires a subcommand"},
		{name: "update status positional", argv: []string{"update", "status", "x"}, want: "update status does not accept positional arguments"},
		{name: "update check positional", argv: []string{"update", "check", "x"}, want: "update check does not accept positional arguments"},
		{name: "update apply positional", argv: []string{"update", "apply", "x"}, want: "update apply does not accept positional arguments"},
		// window
		{name: "window B", argv: []string{"window", "x", "--zz"}, want: "unknown window subcommand: x"},
		{name: "window C", argv: []string{"window", "x"}, want: "unknown window subcommand: x"},
		{name: "window D", argv: []string{"window"}, want: "window requires a subcommand"},
		{name: "window record positional", argv: []string{"window", "record", "x"}, want: "window record does not accept positional arguments"},
		{name: "window recent positional", argv: []string{"window", "recent", "x"}, want: "window recent does not accept positional arguments"},
		// setup
		{name: "setup positional", argv: []string{"setup", "x"}, want: "setup does not accept positional arguments"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := leftoverArgsTestApp().Run(test.argv, &stdout, &stderr)
			if err == nil {
				t.Fatalf("Run(%q) succeeded, want a usage error", test.argv)
			}
			if !IsUsageError(err) {
				t.Fatalf("Run(%q) error = %v (%T), want a usage error (exit 2)", test.argv, err, err)
			}
			if got := err.Error(); got != test.want {
				t.Fatalf("Run(%q) error text = %q, want %q", test.argv, got, test.want)
			}
		})
	}
}

// TestPublicRoutesHelpSubcommandStillSucceeds pins the in-handler help verbs
// next to the refusals above: they keep printing usage and returning nil.
func TestPublicRoutesHelpSubcommandStillSucceeds(t *testing.T) {
	isolateRuntimeWindowFlagParseEnv(t)

	for _, argv := range [][]string{
		{"agent", "integrate", "help"},
		{"pin", "project", "help"},
		{"runtime", "tag", "help"},
		{"update", "help"},
		{"window", "help"},
	} {
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := leftoverArgsTestApp().Run(argv, &stdout, &stderr); err != nil {
				t.Fatalf("Run(%q) error = %v, want nil", argv, err)
			}
			if stdout.Len() == 0 {
				t.Fatalf("Run(%q) printed no usage on stdout", argv)
			}
		})
	}
}
