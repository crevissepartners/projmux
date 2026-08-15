package app

import (
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
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
		{name: "bare config", args: nil, want: "config requires a subcommand: render, apply"},
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
	// The hidden spellings `make install` and every already-running tmux server
	// depend on are untouched.
	for _, token := range []string{"tmux", "internal"} {
		if _, ok := app.routeHandlers()[token]; !ok {
			t.Fatalf("the %q route lost its handler", token)
		}
	}
}
