package app

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"strings"
	"testing"
)

// runtimeWindowFlagParseTestApp wires zero-value leaves for the runtime, window,
// switch, shell, quit, welcome, and setup routes. Every case fails in
// fs.Parse, before any leaf reaches tmux, the filesystem, or a picker.
func runtimeWindowFlagParseTestApp() *App {
	return &App{
		sessions: &sessionsCommand{},
		attach:   &attachCommand{},
		tag:      &tagCommand{},
		prune:    &pruneCommand{},
		window:   &windowCommand{recent: &recentWindowCommand{}},
		switcher: &switchCommand{lookupEnv: func(string) string { return "" }},
		shell:    &shellCommand{},
		quit:     &quitCommand{},
		welcome:  &welcomeCommand{},
		setup:    &setupCommand{terminal: newInitCommand()},
		// No interactive run-shell client: dispatch stays in-process.
		lookupEnv: func(string) string { return "" },
	}
}

// isolateRuntimeWindowFlagParseEnv keeps the pre-dispatch legacy hook migration and
// any stray tmux lookup away from the developer's HOME and tmux server.
func isolateRuntimeWindowFlagParseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")
	t.Setenv("PROJMUX_CWD", "")
}

// TestFlagParseErrorsAreUsageErrorsInRuntimeWindowSwitchSetupRoutes pins that a
// flag the parser rejects (unknown flag, missing value, bad value) exits 2 on
// the public routes, with the flag package's message unchanged.
func TestFlagParseErrorsAreUsageErrorsInRuntimeWindowSwitchSetupRoutes(t *testing.T) {
	isolateRuntimeWindowFlagParseEnv(t)

	const (
		unknown   = "flag provided but not defined: -zz-bogus-flag"
		needsArg  = "flag needs an argument"
		badValue  = `invalid value "abc" for flag -timeout`
		bogusFlag = "--zz-bogus-flag"
	)
	for _, test := range []struct {
		name string
		argv []string
		want string
	}{
		{name: "runtime sessions unknown", argv: []string{"runtime", "sessions", bogusFlag}, want: unknown},
		{name: "runtime sessions ui without value", argv: []string{"runtime", "sessions", "--ui"}, want: needsArg},
		{name: "runtime attach unknown", argv: []string{"runtime", "attach", bogusFlag}, want: unknown},
		{name: "runtime attach keep without value", argv: []string{"runtime", "attach", "--keep"}, want: needsArg},
		{name: "runtime attach keep bad value", argv: []string{"runtime", "attach", "--keep", "abc"}, want: `invalid value "abc" for flag -keep`},
		{name: "runtime tag unknown", argv: []string{"runtime", "tag", bogusFlag}, want: unknown},
		{name: "runtime prune unknown", argv: []string{"runtime", "prune", bogusFlag}, want: unknown},
		{name: "runtime prune keep without value", argv: []string{"runtime", "prune", "--keep"}, want: needsArg},
		{name: "window unknown", argv: []string{"window", bogusFlag}, want: unknown},
		{name: "window recent unknown", argv: []string{"window", "recent", bogusFlag}, want: unknown},
		{name: "window record unknown", argv: []string{"window", "record", bogusFlag}, want: unknown},
		{name: "switch unknown", argv: []string{"switch", bogusFlag}, want: unknown},
		{name: "switch ui without value", argv: []string{"switch", "--ui"}, want: needsArg},
		{name: "switch anchor without value", argv: []string{"switch", "--anchor"}, want: needsArg},
		{name: "switch toggle-tag unknown", argv: []string{"switch", "toggle-tag", bogusFlag}, want: unknown},
		{name: "switch toggle-pin unknown", argv: []string{"switch", "toggle-pin", bogusFlag}, want: unknown},
		{name: "switch kill unknown", argv: []string{"switch", "kill", bogusFlag}, want: unknown},
		{name: "switch open unknown", argv: []string{"switch", "open", bogusFlag}, want: unknown},
		{name: "switch preview unknown", argv: []string{"switch", "preview", bogusFlag}, want: unknown},
		{name: "switch preview ui without value", argv: []string{"switch", "preview", "--ui"}, want: needsArg},
		{name: "switch sidebar-open unknown", argv: []string{"switch", "sidebar-open", bogusFlag}, want: unknown},
		{name: "switch sidebar-open path without value", argv: []string{"switch", "sidebar-open", "--path"}, want: needsArg},
		{name: "shell unknown", argv: []string{"shell", bogusFlag}, want: unknown},
		{name: "shell socket without value", argv: []string{"shell", "--socket"}, want: needsArg},
		{name: "quit unknown", argv: []string{"quit", bogusFlag}, want: unknown},
		{name: "welcome unknown", argv: []string{"welcome", bogusFlag}, want: unknown},
		{name: "setup unknown", argv: []string{"setup", bogusFlag}, want: unknown},
		{name: "setup timeout without value", argv: []string{"setup", "--timeout"}, want: needsArg},
		{name: "setup timeout bad value", argv: []string{"setup", "--timeout", "abc"}, want: badValue},
		{name: "setup terminal unknown", argv: []string{"setup", "terminal", bogusFlag}, want: unknown},
		{name: "setup terminal config without value", argv: []string{"setup", "terminal", "--config"}, want: needsArg},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := runtimeWindowFlagParseTestApp().Run(test.argv, &stdout, &stderr)
			if err == nil {
				t.Fatalf("Run(%q) succeeded, want a flag parse error", test.argv)
			}
			if !IsUsageError(err) {
				t.Fatalf("Run(%q) error = %v, want a usage error (exit 2)", test.argv, err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Run(%q) error = %q, want it to contain %q", test.argv, err.Error(), test.want)
			}
		})
	}
}

// TestFlagParseHandlersKeepHelpAndMarkTopLevelParseErrors covers the leaf
// FlagSets the public dispatch cannot hand a leading flag to (the `attach` and
// `prune` group parsers sit behind a forwarded subcommand), and pins that
// --help at each handler still yields its previous result, never a usage
// error.
func TestFlagParseHandlersKeepHelpAndMarkTopLevelParseErrors(t *testing.T) {
	isolateRuntimeWindowFlagParseEnv(t)

	app := runtimeWindowFlagParseTestApp()
	type handler func([]string, io.Writer, io.Writer) error
	for _, test := range []struct {
		name    string
		run     handler
		args    []string
		wantErr error // flag.ErrHelp or nil for help cases
		usage   string
	}{
		{name: "attach unknown", run: app.attach.Run, args: []string{"--zz-bogus-flag"}, usage: "flag provided but not defined: -zz-bogus-flag"},
		{name: "prune unknown", run: app.prune.Run, args: []string{"--zz-bogus-flag"}, usage: "flag provided but not defined: -zz-bogus-flag"},
		{name: "sessions help", run: app.sessions.Run, args: []string{"--help"}, wantErr: flag.ErrHelp},
		{name: "attach help", run: app.attach.Run, args: []string{"-h"}, wantErr: flag.ErrHelp},
		{name: "attach auto help", run: app.attach.Run, args: []string{"auto", "--help"}, wantErr: flag.ErrHelp},
		{name: "tag help", run: app.tag.Run, args: []string{"-h"}, wantErr: flag.ErrHelp},
		{name: "prune help", run: app.prune.Run, args: []string{"-h"}, wantErr: flag.ErrHelp},
		{name: "prune ephemeral help", run: app.prune.Run, args: []string{"ephemeral", "--help"}, wantErr: flag.ErrHelp},
		{name: "window help", run: app.window.Run, args: []string{"-h"}, wantErr: flag.ErrHelp},
		{name: "window recent help", run: app.window.Run, args: []string{"recent", "--help"}, wantErr: flag.ErrHelp},
		{name: "window record help", run: app.window.Run, args: []string{"record", "--help"}, wantErr: flag.ErrHelp},
		{name: "switch help", run: app.switcher.Run, args: []string{"--help"}, wantErr: flag.ErrHelp},
		{name: "switch preview help", run: app.switcher.Run, args: []string{"preview", "--help"}, wantErr: flag.ErrHelp},
		{name: "switch sidebar-open help", run: app.switcher.Run, args: []string{"sidebar-open", "--help"}, wantErr: flag.ErrHelp},
		{name: "shell help", run: app.shell.Run, args: []string{"--help"}, wantErr: nil},
		{name: "quit help", run: app.quit.Run, args: []string{"--help"}, wantErr: nil},
		{name: "welcome help", run: app.welcome.Run, args: []string{"--help"}, wantErr: flag.ErrHelp},
		{name: "setup help", run: app.setup.Run, args: []string{"--help"}, wantErr: flag.ErrHelp},
		{name: "setup terminal help", run: app.setup.Run, args: []string{"terminal", "--help"}, wantErr: flag.ErrHelp},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.run(test.args, &bytes.Buffer{}, &bytes.Buffer{})
			if test.usage != "" {
				if !IsUsageError(err) || !strings.Contains(err.Error(), test.usage) {
					t.Fatalf("Run(%q) error = %v, want usage error containing %q", test.args, err, test.usage)
				}
				return
			}
			if IsUsageError(err) {
				t.Fatalf("Run(%q) error = %v is a usage error, want the help result", test.args, err)
			}
			if test.wantErr == nil {
				if err != nil {
					t.Fatalf("Run(%q) error = %v, want nil", test.args, err)
				}
				return
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Run(%q) error = %v, want %v", test.args, err, test.wantErr)
			}
		})
	}
}
