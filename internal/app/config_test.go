package app

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/diagnostics"
)

// TestConfigDomainForwardsRawArgvToTheTmuxHandler is the config-domain parity
// table.
//
// The claim under test is that the public spelling is a second door onto one
// implementation, not a second implementation. So the assertion is on the exact
// argv the tmux handler receives: prefix the current spelling's leading tokens,
// hand the entire remainder through untouched. If that holds, stdout, stderr,
// the exit code, and the side effects are the current route's by construction.
func TestConfigDomainForwardsRawArgvToTheTmuxHandler(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "render standalone",
			args: []string{"render", "standalone"},
			want: []string{"print-config"},
		},
		{
			name: "render app",
			args: []string{"render", "app"},
			want: []string{"print-app-config"},
		},
		{
			name: "apply",
			args: []string{"apply"},
			want: []string{"apply"},
		},
		// Flags are the leaf parser's business. Nothing in the config node reads
		// them, so they arrive in order, unmodified, after the prefix.
		{
			name: "render standalone relays --bin",
			args: []string{"render", "standalone", "--bin", "/opt/projmux"},
			want: []string{"print-config", "--bin", "/opt/projmux"},
		},
		{
			name: "render app relays --bin",
			args: []string{"render", "app", "--bin", "/opt/projmux"},
			want: []string{"print-app-config", "--bin", "/opt/projmux"},
		},
		{
			name: "apply relays every flag",
			args: []string{"apply", "--bin", "/opt/projmux", "--config", "/tmp/app.conf", "--socket", "alt"},
			want: []string{"apply", "--bin", "/opt/projmux", "--config", "/tmp/app.conf", "--socket", "alt"},
		},
		// A bare `--` and everything after it is payload the alias must not
		// interpret, exactly as the runtime and agent aliases guarantee.
		{
			name: "payload tails survive",
			args: []string{"apply", "--", "--bin"},
			want: []string{"apply", "--", "--bin"},
		},
		// An unknown flag is the leaf parser's error to raise, not the alias's.
		// The alias must forward it rather than classify it.
		{
			name: "unknown flags reach the leaf parser",
			args: []string{"render", "standalone", "--nosuch"},
			want: []string{"print-config", "--nosuch"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tmux := &recordingRawArgv{stdout: "tmux-out\n", stderr: "tmux-err\n"}
			cmd := &configCommand{tmux: tmux}
			stdout, stderr, err := runRoute(t, cmd, test.args...)
			if err != nil {
				t.Fatalf("config %v error = %v", test.args, err)
			}
			if len(tmux.calls) != 1 {
				t.Fatalf("config %v reached the tmux handler %d times, want 1", test.args, len(tmux.calls))
			}
			if strings.Join(tmux.calls[0], "\x00") != strings.Join(test.want, "\x00") {
				t.Fatalf("config %v forwarded %#v, want %#v", test.args, tmux.calls[0], test.want)
			}
			if stdout != tmux.stdout || stderr != tmux.stderr {
				t.Fatalf("config %v relayed stdout=%q stderr=%q, want %q/%q",
					test.args, stdout, stderr, tmux.stdout, tmux.stderr)
			}
		})
	}

	// The handler's error crosses the alias unchanged, so the public spelling
	// exits with the current route's exit code rather than a translated one.
	t.Run("errors cross unchanged", func(t *testing.T) {
		t.Parallel()
		failing := &recordingRawArgv{err: usageError("tmux apply does not accept positional arguments")}
		if _, _, err := runRoute(t, &configCommand{tmux: failing}, "apply", "extra"); err == nil || !IsUsageError(err) {
			t.Fatalf("relayed error = %v, want the handler's usage error", err)
		}
	})
}

// TestConfigRejectsUnknownArgvWithoutReachingAHandler pins the two usage
// boundaries.
//
// The bare-node shape is not invented here: `get`, `create`, `delete`,
// `describe`, `restore`, `runtime`, and `agent` all answer a bare invocation
// with a usage error naming the kinds, and this route matches them. The reason
// `config render` refuses to pick a default matters -- projmux generates two
// different tmux configs, and silently choosing one would put the other back
// behind the hidden route this Phase exists to give a public door.
func TestConfigRejectsUnknownArgvWithoutReachingAHandler(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{name: "bare config", args: nil, want: "config requires a subcommand: edit, providers, locale, agent-questions, render, apply"},
		{name: "unknown subcommand", args: []string{"show"}, want: "config show is not available"},
		{name: "bare render", args: []string{"render"}, want: "config render requires an artifact: standalone, app"},
		{name: "unknown artifact", args: []string{"render", "bogus"}, want: "config render bogus is not available"},
		// `--bin` in the artifact position is a missing artifact, not a flag the
		// node should consume. It must fail here rather than silently render.
		{name: "flag in the artifact position", args: []string{"render", "--bin", "/opt/projmux"}, want: "config render --bin is not available"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tmux := &recordingRawArgv{}
			_, stderr, err := runRoute(t, &configCommand{tmux: tmux}, test.args...)
			if err == nil || !IsUsageError(err) {
				t.Fatalf("config %v error = %v, want a usage error", test.args, err)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("config %v error = %q, want it to contain %q", test.args, err.Error(), test.want)
			}
			if len(tmux.calls) != 0 {
				t.Fatalf("config %v reached the tmux handler: %#v", test.args, tmux.calls)
			}
			if stderr != "" {
				t.Fatalf("config %v wrote %q to stderr; the usage error carries the message", test.args, stderr)
			}
		})
	}
}

// TestConfigRenderReachesBothGeneratedArtifacts is the acceptance assertion of
// the public config spelling, stated as a two-way diff against the command tree.
//
// Two distinct tmux printers declare the canonical spelling `config render`,
// because projmux generates two different tmux configs. A public `render` that
// reached only one of them would leave the other with no public door at all --
// the same spelling gap this route exists to close -- so the test derives the
// tmux targets from the command tree rather than trusting the handler's switch,
// and requires the mapping to be a bijection.
func TestConfigRenderReachesBothGeneratedArtifacts(t *testing.T) {
	t.Parallel()

	// Left to right: every hidden tmux printer that claims `config render` must
	// be reachable through exactly one public artifact token.
	tmuxPrinters := map[string]bool{}
	internal, ok := cli.LookupRoute("internal")
	if !ok {
		t.Fatal("the internal namespace route is missing")
	}
	for _, child := range internal.Children {
		if child.Name != "tmux" {
			continue
		}
		for _, printer := range child.Children {
			for _, spelling := range printer.Canonical {
				if spelling == "config render" {
					tmuxPrinters[printer.Name] = true
				}
			}
		}
	}
	if len(tmuxPrinters) != 2 {
		t.Fatalf("internal tmux printers claiming `config render` = %v, want exactly the two generated artifacts", tmuxPrinters)
	}

	reached := map[string]string{}
	for _, artifact := range configRenderArtifacts {
		tmux := &recordingRawArgv{}
		if _, _, err := runRoute(t, &configCommand{tmux: tmux}, "render", artifact); err != nil {
			t.Fatalf("config render %s error = %v", artifact, err)
		}
		if len(tmux.calls) != 1 || len(tmux.calls[0]) != 1 {
			t.Fatalf("config render %s forwarded %#v, want exactly one tmux subcommand", artifact, tmux.calls)
		}
		target := tmux.calls[0][0]
		if !tmuxPrinters[target] {
			t.Fatalf("config render %s forwards to %q, which is not a tmux printer claiming `config render`", artifact, target)
		}
		if previous, seen := reached[target]; seen {
			t.Fatalf("config render %s and config render %s both forward to %q", previous, artifact, target)
		}
		reached[target] = artifact
	}

	// Right to left: nothing is left behind.
	for printer := range tmuxPrinters {
		if _, ok := reached[printer]; !ok {
			t.Errorf("`internal tmux %s` claims the canonical spelling `config render` but no public artifact token reaches it", printer)
		}
	}
}

// TestConfigRouteIsWiredIntoTheApplicationGraph proves the route is dispatchable
// from the real graph rather than only from a hand-built struct, and that the
// handler it binds to is the same tmux command the hidden routes use.
func TestConfigRouteIsWiredIntoTheApplicationGraph(t *testing.T) {
	t.Parallel()

	app := New()
	if _, ok := app.routeHandlers()["config"]; !ok {
		t.Fatal("the config route has no handler in the application graph")
	}
	if app.config == nil {
		t.Fatal("the config command is not constructed")
	}
	if app.config.tmux != rawArgvCommand(app.tmux) {
		t.Fatal("config does not forward to the same tmux handler the hidden routes use")
	}
	if _, ok := app.routeHandlers()["internal"]; !ok {
		t.Fatal("the internal route lost its handler")
	}
	if _, ok := app.routeHandlers()["tmux"]; ok {
		t.Fatal("the retired pre-namespace tmux route still has a handler")
	}
}

// configForwarderFixture is one App graph whose `config` alias and hidden
// `internal tmux` spellings reach the same real ai and tmux handlers, with a
// temp HOME, a recording tmux runner, and a lifecycle writer that observes any
// apply diagnostics mark.
type configForwarderFixture struct {
	home   string
	app    *App
	ai     *aiCommand
	runner *recordingTmuxRunner
	writer *appLifecycleWriter
}

func newConfigForwarderFixture(t *testing.T) *configForwarderFixture {
	t.Helper()
	home := t.TempDir()
	writer := &appLifecycleWriter{}
	recorder := diagnostics.NewLifecycleRecorder(writer, "config-forwarder", "0.10.0", "tmux")
	runner := &recordingTmuxRunner{}
	tmux := &tmuxCommand{
		diagnostics: recorder,
		executable:  func() (string, error) { return "/tmp/projmux", nil },
		homeDir:     func() (string, error) { return home, nil },
		lookupEnv:   func(string) string { return "" },
		readFile:    os.ReadFile,
		writeFile:   os.WriteFile,
		runner:      runner,
	}
	ai := testAICommand(home)
	app := &App{
		lifecycle: recorder,
		tmux:      tmux,
		ai:        ai,
		config: &configCommand{
			tmux:      tmux,
			ai:        ai,
			homeDir:   func() (string, error) { return home, nil },
			lookupEnv: func(string) string { return "" },
		},
	}
	return &configForwarderFixture{home: home, app: app, ai: ai, runner: runner, writer: writer}
}

func (f *configForwarderFixture) files(t *testing.T) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(f.home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// TestConfigEditSetRejectsUnknownAIMode pins that `config edit --set` refuses a
// word outside the AI split modes as a usage error (exit 2) before it touches
// the mode file: an absent file and its directory stay absent, and a saved
// file stays byte-identical. Matching is case-sensitive.
func TestConfigEditSetRejectsUnknownAIMode(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"bogus", "Claude", "selectivex", " shellx "} {
		for _, seeded := range []bool{false, true} {
			name := value + "/unseeded"
			if seeded {
				name = value + "/seeded"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				fixture := newConfigForwarderFixture(t)
				path := fixture.ai.configFile()
				const saved = "codex\n"
				if seeded {
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(saved), 0o644); err != nil {
						t.Fatal(err)
					}
				}

				var stdout bytes.Buffer
				err := fixture.app.Run([]string{"config", "edit", "--set", value}, &stdout, &bytes.Buffer{})
				if err == nil || !IsUsageError(err) {
					t.Fatalf("config edit --set %q error = %v, want a usage error", value, err)
				}
				want := fmt.Sprintf("unknown AI mode %q; --set must be one of: claude, codex, antigravity, selective, resume, shell", strings.TrimSpace(value))
				if err.Error() != want {
					t.Fatalf("config edit --set %q error text = %q, want %q", value, err.Error(), want)
				}
				if stdout.Len() != 0 {
					t.Fatalf("config edit --set %q wrote stdout %q, want none", value, stdout.String())
				}
				if commands := cmdRecorder(fixture.ai).commands; len(commands) != 0 {
					t.Fatalf("config edit --set %q issued commands %#v, want none", value, commands)
				}

				if seeded {
					got, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					if string(got) != saved {
						t.Fatalf("mode file = %q after refused --set %q, want it unchanged %q", got, value, saved)
					}
					return
				}
				if files := fixture.files(t); len(files) != 0 {
					t.Fatalf("config edit --set %q wrote files %v, want none", value, files)
				}
				if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("mode file directory stat error = %v, want it still absent", err)
				}
			})
		}
	}
}

// TestConfigEditSetWritesEachAllowedAIMode pins that every AI split mode is
// still accepted by `config edit --set`, trimmed, and saved as one line.
func TestConfigEditSetWritesEachAllowedAIMode(t *testing.T) {
	t.Parallel()

	for _, value := range []string{aiModeClaude, aiModeCodex, aiModeAntigravity, aiModeSelective, aiModeResume, aiModeShell, " claude "} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			fixture := newConfigForwarderFixture(t)
			if err := fixture.app.Run([]string{"config", "edit", "--set", value}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
				t.Fatalf("config edit --set %q error = %v", value, err)
			}
			got, err := os.ReadFile(fixture.ai.configFile())
			if err != nil {
				t.Fatal(err)
			}
			if want := strings.TrimSpace(value) + "\n"; string(got) != want {
				t.Fatalf("mode file after --set %q = %q, want %q", value, got, want)
			}
		})
	}
}

// TestConfigForwarderRejectsFlagAndArgErrorsAsUsage pins that a bad flag or an
// extra positional argument on `config edit|apply|render <artifact>` is a usage
// error (exit 2), classified by the receiving handler so the public alias and
// the hidden spellings answer with the same exit code and the same text, and
// that a rejected call has no effect.
func TestConfigForwarderRejectsFlagAndArgErrorsAsUsage(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "config edit unknown flag", args: []string{"config", "edit", "--bogus"}, wantErr: "flag provided but not defined: -bogus"},
		{name: "config edit --set without value", args: []string{"config", "edit", "--set"}, wantErr: "flag needs an argument: -set"},
		{name: "config edit extra arg", args: []string{"config", "edit", "extra"}, wantErr: "ai settings does not accept positional arguments"},
		{name: "config apply unknown flag", args: []string{"config", "apply", "--bogus"}, wantErr: "flag provided but not defined: -bogus"},
		{name: "config apply extra arg", args: []string{"config", "apply", "extra"}, wantErr: "tmux apply does not accept positional arguments"},
		{name: "config render standalone unknown flag", args: []string{"config", "render", "standalone", "--bogus"}, wantErr: "flag provided but not defined: -bogus"},
		{name: "config render standalone extra arg", args: []string{"config", "render", "standalone", "extra"}, wantErr: "tmux print-config does not accept positional arguments"},
		{name: "config render app unknown flag", args: []string{"config", "render", "app", "--bogus"}, wantErr: "flag provided but not defined: -bogus"},
		{name: "config render app extra arg", args: []string{"config", "render", "app", "extra"}, wantErr: "tmux print-app-config does not accept positional arguments"},
		// Hidden spellings. `ai settings` has no hidden root spelling since the
		// ai root was retired, so `config edit` is its only door.
		{name: "internal tmux apply unknown flag", args: []string{"internal", "tmux", "apply", "--bogus"}, wantErr: "flag provided but not defined: -bogus"},
		{name: "internal tmux apply extra arg", args: []string{"internal", "tmux", "apply", "extra"}, wantErr: "tmux apply does not accept positional arguments"},
		{name: "internal tmux print-config unknown flag", args: []string{"internal", "tmux", "print-config", "--bogus"}, wantErr: "flag provided but not defined: -bogus"},
		{name: "internal tmux print-config extra arg", args: []string{"internal", "tmux", "print-config", "extra"}, wantErr: "tmux print-config does not accept positional arguments"},
		{name: "internal tmux print-app-config unknown flag", args: []string{"internal", "tmux", "print-app-config", "--bogus"}, wantErr: "flag provided but not defined: -bogus"},
		{name: "internal tmux print-app-config extra arg", args: []string{"internal", "tmux", "print-app-config", "extra"}, wantErr: "tmux print-app-config does not accept positional arguments"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := newConfigForwarderFixture(t)
			var stdout, stderr bytes.Buffer
			err := fixture.app.Run(test.args, &stdout, &stderr)
			if err == nil || !IsUsageError(err) {
				t.Fatalf("%v error = %v, want a usage error", test.args, err)
			}
			if err.Error() != test.wantErr {
				t.Fatalf("%v error text = %q, want %q", test.args, err.Error(), test.wantErr)
			}
			if stdout.Len() != 0 {
				t.Fatalf("%v wrote stdout %q, want none", test.args, stdout.String())
			}
			if files := fixture.files(t); len(files) != 0 {
				t.Fatalf("%v wrote files %v, want none", test.args, files)
			}
			if len(fixture.runner.calls) != 0 {
				t.Fatalf("%v issued tmux calls %#v, want none", test.args, fixture.runner.calls)
			}
			if commands := cmdRecorder(fixture.ai).commands; len(commands) != 0 {
				t.Fatalf("%v issued commands %#v, want none", test.args, commands)
			}
			if len(fixture.writer.events) != 0 {
				t.Fatalf("%v recorded diagnostics %#v, want no apply mark", test.args, fixture.writer.events)
			}
		})
	}

	t.Run("config edit --get still prints the mode", func(t *testing.T) {
		t.Parallel()
		fixture := newConfigForwarderFixture(t)
		var stdout bytes.Buffer
		if err := fixture.app.Run([]string{"config", "edit", "--get"}, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("config edit --get error = %v", err)
		}
		if got, want := stdout.String(), "selective\n"; got != want {
			t.Fatalf("config edit --get stdout = %q, want %q", got, want)
		}
	})

	t.Run("config render standalone still renders", func(t *testing.T) {
		t.Parallel()
		fixture := newConfigForwarderFixture(t)
		var stdout bytes.Buffer
		if err := fixture.app.Run([]string{"config", "render", "standalone"}, &stdout, &bytes.Buffer{}); err != nil {
			t.Fatalf("config render standalone error = %v", err)
		}
		if !strings.Contains(stdout.String(), "/tmp/projmux") {
			t.Fatalf("config render standalone stdout does not name the fake executable:\n%s", stdout.String())
		}
	})

	// Runtime failures stay non-usage: a binary that cannot be resolved and a
	// config write that fails are not the caller's argv mistakes.
	t.Run("binary resolution failure is not a usage error", func(t *testing.T) {
		t.Parallel()
		fixture := newConfigForwarderFixture(t)
		fixture.app.tmux.executable = func() (string, error) { return "", errors.New("no executable") }
		err := fixture.app.Run([]string{"config", "render", "app"}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || IsUsageError(err) {
			t.Fatalf("config render app error = %v, want a non-usage runtime error", err)
		}
	})
	t.Run("apply write failure is not a usage error", func(t *testing.T) {
		t.Parallel()
		fixture := newConfigForwarderFixture(t)
		fixture.app.tmux.writeFile = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
		err := fixture.app.Run([]string{"config", "apply", "--config", filepath.Join(fixture.home, "tmux.conf")}, &bytes.Buffer{}, &bytes.Buffer{})
		if err == nil || IsUsageError(err) {
			t.Fatalf("config apply error = %v, want a non-usage runtime error", err)
		}
	})

	// Help is intercepted by the root before these handlers run: it renders the
	// catalog help on stdout and exits 0, so the handlers' flag.ErrHelp branch
	// is never the path a user reaches. Pin that it stays that way.
	for _, args := range [][]string{
		{"config", "edit", "--help"},
		{"config", "apply", "--help"},
		{"config", "render", "standalone", "--help"},
		{"internal", "tmux", "apply", "--help"},
		{"internal", "tmux", "print-config", "-h"},
	} {
		t.Run("help "+strings.Join(args, " "), func(t *testing.T) {
			t.Parallel()
			fixture := newConfigForwarderFixture(t)
			var stdout bytes.Buffer
			if err := fixture.app.Run(args, &stdout, &bytes.Buffer{}); err != nil {
				t.Fatalf("%v error = %v, want help to exit 0", args, err)
			}
			if !strings.HasPrefix(stdout.String(), "projmux ") {
				t.Fatalf("%v stdout = %q, want the catalog help", args, stdout.String())
			}
			if files := fixture.files(t); len(files) != 0 {
				t.Fatalf("%v wrote files %v", args, files)
			}
		})
	}
}
