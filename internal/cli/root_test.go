package cli

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type recordedInvocation struct {
	token string
	args  []string
}

// newTestRoot builds a root whose handlers only record the argv they receive, so
// bridge behavior can be asserted without touching tmux or the filesystem.
func newTestRoot(t *testing.T, stdout, stderr io.Writer) (*Root, *[]recordedInvocation) {
	t.Helper()
	var recorded []recordedInvocation
	handlers := map[string]Handler{}
	for _, route := range Routes() {
		if policyOwnedRoutes[route.Name] {
			continue
		}
		token := route.Name
		handlers[token] = func(args []string, _, _ io.Writer) error {
			recorded = append(recorded, recordedInvocation{token: token, args: args})
			return nil
		}
	}
	root, err := NewRoot(RootOptions{Stdout: stdout, Stderr: stderr, Version: "9.9.9", Handlers: handlers})
	if err != nil {
		t.Fatalf("NewRoot returned error: %v", err)
	}
	return root, &recorded
}

// TestNewRootRequiresEveryManifestHandler proves the wiring cannot silently drop
// a route: a missing handler is a construction error, not a runtime surprise.
func TestNewRootRequiresEveryManifestHandler(t *testing.T) {
	t.Parallel()

	_, err := NewRoot(RootOptions{Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}, Handlers: map[string]Handler{}})
	if err == nil {
		t.Fatal("NewRoot with no handlers returned no error")
	}
	for _, want := range []string{"internal", "window"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing-handler error does not name %q: %v", want, err)
		}
	}
	// `help` and `version` are answered by the root policy, so they must not be
	// reported as missing handlers.
	for _, policyOwned := range []string{" help", " version"} {
		if strings.Contains(err.Error(), policyOwned) {
			t.Fatalf("policy-owned route reported as a missing handler: %v", err)
		}
	}

	if _, err := NewRoot(RootOptions{Stderr: &bytes.Buffer{}}); err == nil {
		t.Fatal("NewRoot without stdout returned no error")
	}
}

// TestCommandTreeMatchesGolden is the command-tree golden. It freezes the Cobra
// surface: exactly the manifest routes, no injected commands, hidden flags
// preserved, flag parsing off on every bridge, and arbitrary args everywhere so
// raw argv survives.
func TestCommandTreeMatchesGolden(t *testing.T) {
	t.Parallel()

	root, _ := newTestRoot(t, &bytes.Buffer{}, &bytes.Buffer{})
	// Execute a trivial invocation first so Cobra performs the same lazy
	// initialization (help command, completion policy) it does in production.
	if err := root.Execute([]string{"version"}); err != nil {
		t.Fatalf("Execute(version) error = %v", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "root use=%q disableFlagParsing=%v silenceUsage=%v silenceErrors=%v disableSuggestions=%v completionDisabled=%v\n",
		root.cmd.Use, root.cmd.DisableFlagParsing, root.cmd.SilenceUsage, root.cmd.SilenceErrors,
		root.cmd.DisableSuggestions, root.cmd.CompletionOptions.DisableDefaultCmd)
	names := make([]string, 0, len(root.cmd.Commands()))
	for _, child := range root.cmd.Commands() {
		names = append(names, fmt.Sprintf("%s hidden=%v disableFlagParsing=%v args=%v children=%d",
			child.Name(), child.Hidden, child.DisableFlagParsing, child.Args != nil, len(child.Commands())))
	}
	sort.Strings(names)
	for _, name := range names {
		b.WriteString(name)
		b.WriteString("\n")
	}

	goldenPath := filepath.Join("testdata", "command-tree.golden")
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if b.String() != string(want) {
		t.Fatalf("cobra command tree drifted:\n--- got ---\n%s\n--- want ---\n%s", b.String(), want)
	}
}

// TestBridgeForwardsRawArgv is the parser/payload table. Every Phase 0 bridge
// must hand the existing handler the exact argv tail, preserving `--`,
// positional arguments, and unknown flags.
func TestBridgeForwardsRawArgv(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		argv  []string
		token string
		args  []string
	}{
		{name: "no tail", argv: []string{"doctor"}, token: "doctor", args: []string{}},
		{name: "positional", argv: []string{"current", "extra"}, token: "current", args: []string{"extra"}},
		{name: "unknown flag", argv: []string{"notify", "list", "--bogus-flag"}, token: "notify", args: []string{"list", "--bogus-flag"}},
		{name: "flag with value", argv: []string{"focus", "--target", "%3"}, token: "focus", args: []string{"--target", "%3"}},
		{name: "terminator with payload", argv: []string{"config", "edit", "--", "--help", "-h", "--"}, token: "config", args: []string{"edit", "--", "--help", "-h", "--"}},
		{name: "double dash equals flag", argv: []string{"prune", "ephemeral", "--keep=3"}, token: "prune", args: []string{"ephemeral", "--keep=3"}},
		{name: "session state delete", argv: []string{"prune", "session-state", "delete", "alpha", "beta"}, token: "prune", args: []string{"session-state", "delete", "alpha", "beta"}},
		{name: "negative number payload", argv: []string{"diagnostics", "log", "--tail", "-5"}, token: "diagnostics", args: []string{"log", "--tail", "-5"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			root, recorded := newTestRoot(t, &stdout, &stderr)
			if err := root.Execute(test.argv); err != nil {
				t.Fatalf("Execute(%q) error = %v", test.argv, err)
			}
			if len(*recorded) != 1 {
				t.Fatalf("Execute(%q) invoked %d handlers, want 1", test.argv, len(*recorded))
			}
			got := (*recorded)[0]
			if got.token != test.token {
				t.Fatalf("Execute(%q) routed to %q, want %q", test.argv, got.token, test.token)
			}
			if !reflect.DeepEqual(got.args, test.args) {
				t.Fatalf("Execute(%q) forwarded %#v, want %#v", test.argv, got.args, test.args)
			}
			if stderr.Len() != 0 {
				t.Fatalf("Execute(%q) wrote stderr %q", test.argv, stderr.String())
			}
		})
	}
}

// TestEveryManifestRouteDispatchesToItsHandler proves there are no orphan routes
// at runtime: each manifest route reaches exactly its own handler.
func TestEveryManifestRouteDispatchesToItsHandler(t *testing.T) {
	t.Parallel()

	for _, route := range Routes() {
		if policyOwnedRoutes[route.Name] {
			continue
		}
		var stdout, stderr bytes.Buffer
		root, recorded := newTestRoot(t, &stdout, &stderr)
		if err := root.Execute([]string{route.Name, "probe-arg"}); err != nil {
			t.Fatalf("Execute(%q) error = %v", route.Name, err)
		}
		if len(*recorded) != 1 || (*recorded)[0].token != route.Name {
			t.Fatalf("route %q dispatched to %#v", route.Name, *recorded)
		}
		if !reflect.DeepEqual((*recorded)[0].args, []string{"probe-arg"}) {
			t.Fatalf("route %q forwarded %#v", route.Name, (*recorded)[0].args)
		}
	}
}

// TestHelpInvocationsInvokeNoHandler is the no-side-effect negative test. Public,
// nested, and hidden internal help must exit 0, write only to stdout, and reach
// no handler at all, so there is no tmux or runtime access and no parser error.
func TestHelpInvocationsInvokeNoHandler(t *testing.T) {
	t.Parallel()

	var argvs [][]string
	argvs = append(argvs, nil)
	for _, flag := range helpFlagSpellings() {
		argvs = append(argvs, []string{flag})
	}
	walkRoutes(Routes(), func(path []string, route Route) {
		if route.Retired {
			return
		}
		for _, flag := range helpFlagSpellings() {
			argvs = append(argvs, append(append([]string{}, path...), flag))
		}
	})
	for _, flag := range helpFlagSpellings() {
		argvs = append(argvs, []string{"setup", "terminal", "--apply", flag})
	}

	for _, argv := range argvs {
		var stdout, stderr bytes.Buffer
		root, recorded := newTestRoot(t, &stdout, &stderr)
		if err := root.Execute(argv); err != nil {
			t.Fatalf("Execute(%q) error = %v, want nil", argv, err)
		}
		if len(*recorded) != 0 {
			t.Fatalf("Execute(%q) invoked handlers %#v, want none", argv, *recorded)
		}
		if stdout.Len() == 0 {
			t.Fatalf("Execute(%q) wrote no help to stdout", argv)
		}
		if stderr.Len() != 0 {
			t.Fatalf("Execute(%q) wrote stderr %q, want help on stdout only", argv, stderr.String())
		}
	}
}

// TestUnknownCommandKeepsHistoricalContract pins the default branch: the primary
// listing goes to stderr and the error is a plain runtime error, not a usage
// error, so the process exit code stays 1.
func TestUnknownCommandKeepsHistoricalContract(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		argv  []string
		token string
	}{
		{argv: []string{"nosuchcmd"}, token: "nosuchcmd"},
		{argv: []string{"nosuchcmd", "--help"}, token: "nosuchcmd"},
		{argv: []string{"init"}, token: "init"},
		{argv: []string{"--json"}, token: "--json"},
		{argv: []string{"-x"}, token: "-x"},
		{argv: []string{"__complete", "ai"}, token: "__complete"},
		{argv: []string{"__completeNoDesc", "ai"}, token: "__completeNoDesc"},
		{argv: []string{"completion", "bash"}, token: "completion"},
		{argv: []string{"tmux", "print-config"}, token: "tmux"},
		{argv: []string{"ai", "ingest", "codex-hook"}, token: "ai"},
		{argv: []string{"statusbar", "click"}, token: "statusbar"},
		{argv: []string{"key-broker"}, token: "key-broker"},
		{argv: []string{"popup-wait-key"}, token: "popup-wait-key"},
	} {
		var stdout, stderr bytes.Buffer
		root, recorded := newTestRoot(t, &stdout, &stderr)
		err := root.Execute(test.argv)
		if err == nil || err.Error() != "unknown command: "+test.token {
			t.Fatalf("Execute(%q) error = %v, want unknown command: %s", test.argv, err, test.token)
		}
		if len(*recorded) != 0 {
			t.Fatalf("Execute(%q) invoked handlers %#v", test.argv, *recorded)
		}
		if stdout.Len() != 0 {
			t.Fatalf("Execute(%q) wrote stdout %q, want the listing on stderr", test.argv, stdout.String())
		}
		if !strings.Contains(stderr.String(), "Commands:") {
			t.Fatalf("Execute(%q) stderr missing the primary listing:\n%s", test.argv, stderr.String())
		}
	}
}

// TestVersionRoutes covers the three historical version spellings.
func TestVersionRoutes(t *testing.T) {
	t.Parallel()

	for _, argv := range [][]string{{"version"}, {"--version"}, {"-version"}, {"version", "extra"}, {"--version", "extra"}} {
		var stdout, stderr bytes.Buffer
		root, recorded := newTestRoot(t, &stdout, &stderr)
		if err := root.Execute(argv); err != nil {
			t.Fatalf("Execute(%q) error = %v", argv, err)
		}
		if stdout.String() != "projmux 9.9.9\n" {
			t.Fatalf("Execute(%q) stdout = %q", argv, stdout.String())
		}
		if len(*recorded) != 0 {
			t.Fatalf("Execute(%q) invoked handlers %#v", argv, *recorded)
		}
	}
}

// TestHelpRouteWordPrintsPrimaryListing pins `projmux help` (and `projmux help
// <anything>`) to the primary listing on stdout, matching history.
func TestHelpRouteWordPrintsPrimaryListing(t *testing.T) {
	t.Parallel()

	for _, argv := range [][]string{{"help"}, {"help", "ai"}, {"help", "nosuchcmd"}} {
		var stdout, stderr bytes.Buffer
		root, recorded := newTestRoot(t, &stdout, &stderr)
		if err := root.Execute(argv); err != nil {
			t.Fatalf("Execute(%q) error = %v", argv, err)
		}
		var want bytes.Buffer
		if err := RenderRootHelp(&want); err != nil {
			t.Fatalf("RenderRootHelp returned error: %v", err)
		}
		if stdout.String() != want.String() {
			t.Fatalf("Execute(%q) stdout drifted from the primary listing", argv)
		}
		if stderr.Len() != 0 || len(*recorded) != 0 {
			t.Fatalf("Execute(%q) stderr=%q handlers=%#v", argv, stderr.String(), *recorded)
		}
	}
}

type stubExitCoder struct{ code int }

func (s stubExitCoder) Error() string { return "stub exit" }
func (s stubExitCoder) ExitCode() int { return s.code }

// TestHandlerErrorsSurviveTheCobraBoundary proves the bridge does not let Cobra
// rewrite handler outcomes. In particular a handler-returned flag.ErrHelp must
// stay an error instead of being converted into a successful help print, and
// typed errors used by the process entrypoint must remain inspectable.
func TestHandlerErrorsSurviveTheCobraBoundary(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("handler failed")
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "flag.ErrHelp", err: flag.ErrHelp},
		{name: "wrapped flag.ErrHelp", err: fmt.Errorf("parse doctor flags: %w", flag.ErrHelp)},
		{name: "plain error", err: sentinel},
		{name: "exit coder", err: stubExitCoder{code: 3}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handlers := map[string]Handler{}
			for _, route := range Routes() {
				if policyOwnedRoutes[route.Name] {
					continue
				}
				handlers[route.Name] = func(_ []string, _, _ io.Writer) error { return nil }
			}
			handlers["doctor"] = func(_ []string, _, _ io.Writer) error { return test.err }
			var stdout, stderr bytes.Buffer
			root, err := NewRoot(RootOptions{Stdout: &stdout, Stderr: &stderr, Handlers: handlers})
			if err != nil {
				t.Fatalf("NewRoot returned error: %v", err)
			}
			got := root.Execute([]string{"doctor", "--json"})
			if got == nil {
				t.Fatal("Execute swallowed the handler error")
			}
			if got.Error() != test.err.Error() {
				t.Fatalf("Execute error = %v, want %v", got, test.err)
			}
			if stdout.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("Execute wrote stdout=%q stderr=%q, want the handler to own its output", stdout.String(), stderr.String())
			}
			var coded interface{ ExitCode() int }
			if errors.As(test.err, &coded) {
				var gotCoded interface{ ExitCode() int }
				if !errors.As(got, &gotCoded) || gotCoded.ExitCode() != coded.ExitCode() {
					t.Fatalf("exit coder did not survive the bridge: %v", got)
				}
			}
		})
	}
}
