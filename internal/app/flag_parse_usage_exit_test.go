package app

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
)

// flagParseRoute drives one public route through its namespace dispatcher so
// the test pins the route spelling, not only the handler function.
type flagParseRoute struct {
	name string
	run  func(t *testing.T, args []string) error
	argv []string
}

func flagParseAgentRoute(name string, argv ...string) flagParseRoute {
	return flagParseRoute{name: name, argv: argv, run: func(t *testing.T, args []string) error {
		// A zero agentCommand has no message store or registry reader: a parse
		// failure must return before either is consulted.
		return (&agentCommand{}).Run(args, io.Discard, io.Discard)
	}}
}

func flagParseDiagnosticsRoute(name string, argv ...string) flagParseRoute {
	return flagParseRoute{name: name, argv: argv, run: func(t *testing.T, args []string) error {
		// A zero aiCommand has no home or state resolver: a parse failure must
		// return before the ingest log path is resolved.
		return (&diagnosticsCommand{ai: &aiCommand{}}).Run(args, io.Discard, io.Discard)
	}}
}

// flagParseUpdateCommand fails the test on any installer detection, network,
// cache, or external command reach, proving the parse error returns first.
func flagParseUpdateCommand(t *testing.T) *updateCommand {
	t.Helper()
	cmd, _ := testUpdateCommand(t, time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC))
	cmd.getenv = func(name string) string {
		t.Errorf("update reached getenv(%q) after a flag parse error", name)
		return ""
	}
	cmd.executable = func() (string, error) {
		t.Error("update reached executable() after a flag parse error")
		return "", errors.New("unexpected")
	}
	cmd.lookPath = func(name string) (string, error) {
		t.Errorf("update reached lookPath(%q) after a flag parse error", name)
		return "", errors.New("unexpected")
	}
	cmd.cacheDir = func() (string, error) {
		t.Error("update reached cacheDir() after a flag parse error")
		return "", errors.New("unexpected")
	}
	cmd.runExternal = func(name string, _ []string, _, _ io.Writer) error {
		t.Errorf("update ran external command %q after a flag parse error", name)
		return errors.New("unexpected")
	}
	cmd.client = &http.Client{Transport: updateRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Errorf("update sent a request to %s after a flag parse error", req.URL)
		return nil, errors.New("unexpected")
	})}
	return cmd
}

func flagParseUpdateRoute(name string, argv ...string) flagParseRoute {
	return flagParseRoute{name: name, argv: argv, run: func(t *testing.T, args []string) error {
		return flagParseUpdateCommand(t).Run(args, io.Discard, io.Discard)
	}}
}

func TestAgentDiagnosticsUpdateFlagParseErrorsAreUsageErrors(t *testing.T) {
	t.Parallel()

	type parseCase struct {
		route    flagParseRoute
		tail     []string
		wantText string
	}
	routes := []flagParseRoute{
		flagParseAgentRoute("agent message status", "message", "status"),
		flagParseAgentRoute("agent wait", "wait"),
		flagParseAgentRoute("agent models", "models"),
		flagParseAgentRoute("agent message qualify", "message", "qualify"),
		flagParseDiagnosticsRoute("diagnostics agent-hook", "agent-hook"),
		flagParseUpdateRoute("update apply", "apply"),
		flagParseUpdateRoute("update status", "status"),
		flagParseUpdateRoute("update check", "check"),
	}
	byName := map[string]flagParseRoute{}
	var cases []parseCase
	for _, route := range routes {
		byName[route.name] = route
		cases = append(cases, parseCase{route: route, tail: []string{"--zz-bogus-flag"}, wantText: "flag provided but not defined: -zz-bogus-flag"})
	}
	cases = append(cases,
		parseCase{route: byName["agent message status"], tail: []string{"-o"}, wantText: "flag needs an argument: -o"},
		parseCase{route: byName["agent wait"], tail: []string{"--timeout"}, wantText: "flag needs an argument: -timeout"},
		parseCase{route: byName["agent models"], tail: []string{"--provider"}, wantText: "flag needs an argument: -provider"},
		parseCase{route: byName["agent message qualify"], tail: []string{"--timeout"}, wantText: "flag needs an argument: -timeout"},
		parseCase{route: byName["diagnostics agent-hook"], tail: []string{"--tail"}, wantText: "flag needs an argument: -tail"},
		parseCase{route: byName["update apply"], tail: []string{"--dry-run=maybe"}, wantText: `invalid boolean value "maybe" for -dry-run: parse error`},
		parseCase{route: byName["update status"], tail: []string{"--json=maybe"}, wantText: `invalid boolean value "maybe" for -json: parse error`},
		parseCase{route: byName["update check"], tail: []string{"--json=maybe"}, wantText: `invalid boolean value "maybe" for -json: parse error`},
	)
	// `agent usage` is pinned here through the agent dispatcher; its --help
	// returns nil rather than flag.ErrHelp, which usagecmd's own test covers.
	usageRoute := flagParseRoute{name: "agent usage", argv: []string{"usage"}, run: func(t *testing.T, args []string) error {
		return (&agentCommand{usage: usagecmd.New(nil)}).Run(args, io.Discard, io.Discard)
	}}
	cases = append(cases,
		parseCase{route: usageRoute, tail: []string{"--zz-bogus-flag"}, wantText: "flag provided but not defined: -zz-bogus-flag"},
		parseCase{route: usageRoute, tail: []string{"--model"}, wantText: "flag needs an argument: -model"},
	)

	for _, tc := range cases {
		argv := append(append([]string{}, tc.route.argv...), tc.tail...)
		t.Run(tc.route.name+" "+tc.tail[0], func(t *testing.T) {
			t.Parallel()
			err := tc.route.run(t, argv)
			if err == nil {
				t.Fatalf("Run(%q) error = nil, want a flag parse error", argv)
			}
			if !IsUsageError(err) {
				t.Fatalf("Run(%q) error = %v (%T), want a usage error", argv, err, err)
			}
			if got := err.Error(); got != tc.wantText {
				t.Fatalf("Run(%q) error text = %q, want %q", argv, got, tc.wantText)
			}
		})
	}

	for _, route := range routes {
		argv := append(append([]string{}, route.argv...), "--help")
		t.Run(route.name+" --help", func(t *testing.T) {
			t.Parallel()
			err := route.run(t, argv)
			if !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("Run(%q) error = %v, want flag.ErrHelp", argv, err)
			}
			if IsUsageError(err) {
				t.Fatalf("Run(%q) help classified as a usage error", argv)
			}
		})
	}
}

// The hidden `internal agent-hook ingest` FlagSets are machine plumbing and keep
// returning the bare flag error; only the public routes above changed.
func TestInternalAgentHookIngestFlagParseErrorsStayBare(t *testing.T) {
	t.Parallel()
	for _, source := range []string{"codex-hook", "claude-hook", "antigravity-hook", "bell"} {
		argv := []string{"agent-hook", "ingest", source, "--zz-bogus-flag"}
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			cmd := &internalCommand{ai: &aiCommand{stdin: bytes.NewReader(nil)}}
			err := cmd.Run(argv, io.Discard, io.Discard)
			if err == nil {
				t.Fatalf("Run(%q) error = nil, want a flag parse error", argv)
			}
			if IsUsageError(err) {
				t.Fatalf("Run(%q) error = %v, want a non-usage error", argv, err)
			}
			if got, want := err.Error(), "flag provided but not defined: -zz-bogus-flag"; got != want {
				t.Fatalf("Run(%q) error text = %q, want %q", argv, got, want)
			}
		})
	}
}
