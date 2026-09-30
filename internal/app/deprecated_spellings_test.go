package app

import (
	"bytes"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/core/persona"
)

// runDeprecatedSpellingRoute runs one argv through the App.Run the binary
// uses, in the isolated environment of the settings layer guard.
func runDeprecatedSpellingRoute(env *settingsLayerGuardEnv, argv ...string) (string, string, error) {
	app := New()
	app.update.client = &http.Client{Transport: refusingTransport{}}
	var stdout, stderr bytes.Buffer
	err := app.Run(env.expand(argv), &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

// TestDeprecatedSpellingsRunAsBeforeWithOneNotice is the regression table of
// the deprecated spellings: `projmux persona ...`, `agent persona|instructions
// attach|detach`, and `create ... --persona`. Each still reaches its handler
// through the whole App and ends the way it did while it was listed -- the
// same stdout, the same refusal and reason token, the same usage or runtime
// exit -- and its stderr gains exactly one line, first, that names the
// replacement. A `-o json`, `--dry-run`, or refused call gets that one line
// too, and never on stdout.
//
// No t.Parallel: the environment swaps process environment, working directory
// and stdin.
func TestDeprecatedSpellingsRunAsBeforeWithOneNotice(t *testing.T) {
	env := newSettingsLayerGuardEnv(t)
	run := func(argv ...string) (string, string, error) {
		t.Helper()
		return runDeprecatedSpellingRoute(env, argv...)
	}

	if stdout, stderr, err := run("instructions", "set", "reviewer", "--file", "{tmp}/persona.md"); err != nil || stderr != "" || !strings.HasPrefix(stdout, "set instructions reviewer sha256:") {
		t.Fatalf("instructions set = stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	listed, stderr, err := run("instructions", "list")
	if err != nil || stderr != "" || !strings.Contains(listed, "\nreviewer ") {
		t.Fatalf("instructions list = stdout=%q stderr=%q err=%v", listed, stderr, err)
	}

	personaNotice := func(verb string) string {
		return "projmux: persona " + verb + " is deprecated; use `projmux instructions " + verb + "` instead."
	}
	const (
		attachUse    = "use `projmux agent relaunch <agent-ref> --instructions <name>` instead."
		detachUse    = "use `projmux agent relaunch <agent-ref> --instructions none` instead."
		namespaceUse = "use `projmux agent relaunch <agent-ref> --instructions <name>|none` instead."
		// noRuntime is where an Agent create stops in this environment, which
		// has no tmux: after its flags are parsed and before anything exists.
		noRuntime = "runtime mutation route: probe default logical socket"
	)
	for _, test := range []struct {
		name   string
		argv   []string
		notice string
		// stdout is the exact stdout; stdoutPrefix, when set, is compared
		// instead, for the lines that carry a path.
		stdout       string
		stdoutPrefix string
		// errText is a substring of the returned error, "" for success.
		errText string
		usage   bool
		// restStderr is a substring of what follows the notice on stderr, ""
		// when the notice is all of it.
		restStderr string
	}{
		{name: "persona list", argv: []string{"persona", "list"}, notice: personaNotice("list"), stdout: listed},
		{name: "persona show", argv: []string{"persona", "show", "reviewer"}, notice: personaNotice("show"), stdout: "You review.\n"},
		{name: "persona show missing", argv: []string{"persona", "show", "absent"}, notice: personaNotice("show"),
			errText: "persona show: " + persona.ReasonNotFound + ": persona \"absent\" does not exist"},
		{name: "persona show bad name", argv: []string{"persona", "show", "-x"}, notice: personaNotice("show"),
			errText: "persona show: " + persona.ReasonNameInvalid, usage: true},
		{name: "persona set", argv: []string{"persona", "set", "second", "--file", "{tmp}/persona.md"}, notice: personaNotice("set"),
			stdoutPrefix: "set persona second sha256:"},
		{name: "persona set rejected flag", argv: []string{"persona", "set", "second", "--bogus"}, notice: personaNotice("set"),
			errText: "flag provided but not defined: -bogus", usage: true, restStderr: "Usage:\n  projmux persona set <name> [--file <path> | -]\n"},
		{name: "persona edit without editor", argv: []string{"persona", "edit", "second"}, notice: personaNotice("edit"),
			errText: "persona edit: editor "},
		{name: "persona delete unconfirmed", argv: []string{"persona", "delete", "second"}, notice: personaNotice("delete"),
			errText: "persona delete second requires --yes; nothing was deleted", usage: true},
		{name: "persona delete", argv: []string{"persona", "delete", "second", "--yes"}, notice: personaNotice("delete"),
			stdout: "deleted persona second\n"},
		{name: "persona without a verb", argv: []string{"persona"},
			notice:  "projmux: persona is deprecated; use `projmux instructions <subcommand>` instead.",
			errText: "persona requires a subcommand", usage: true, restStderr: "Usage:\n  projmux persona list\n"},
		{name: "persona unknown verb", argv: []string{"persona", "bogus"},
			notice:  "projmux: persona is deprecated; use `projmux instructions <subcommand>` instead.",
			errText: "unknown persona subcommand: bogus", usage: true, restStderr: "Usage:\n  projmux persona list\n"},

		{name: "agent persona attach dry-run json", argv: []string{"agent", "persona", "attach", "no-such-agent", "reviewer", "--dry-run", "-o", "json"},
			notice: "projmux: agent persona attach is deprecated; " + attachUse, errText: "agent no-such-agent matched no agents", usage: true},
		{name: "agent persona detach", argv: []string{"agent", "persona", "detach", "no-such-agent", "--yes"},
			notice: "projmux: agent persona detach is deprecated; " + detachUse, errText: "agent no-such-agent matched no agents", usage: true},
		{name: "agent instructions attach dry-run", argv: []string{"agent", "instructions", "attach", "no-such-agent", "reviewer", "--dry-run"},
			notice: "projmux: agent instructions attach is deprecated; " + attachUse, errText: "agent no-such-agent matched no agents", usage: true},
		{name: "agent instructions detach json", argv: []string{"agent", "instructions", "detach", "no-such-agent", "-o", "json"},
			notice: "projmux: agent instructions detach is deprecated; " + detachUse, errText: "agent no-such-agent matched no agents", usage: true},
		{name: "agent persona attach without operands", argv: []string{"agent", "persona", "attach"},
			notice:  "projmux: agent persona attach is deprecated; " + attachUse,
			errText: "agent persona attach requires <agent-ref> <persona>", usage: true},
		{name: "agent instructions attach bad name", argv: []string{"agent", "instructions", "attach", "no-such-agent", "../x"},
			notice:  "projmux: agent instructions attach is deprecated; " + attachUse,
			errText: "agent instructions attach: " + persona.ReasonNameInvalid, usage: true},
		{name: "agent persona without a verb", argv: []string{"agent", "persona"},
			notice:  "projmux: agent persona is deprecated; " + namespaceUse,
			errText: "agent persona requires attach or detach", usage: true},
		{name: "agent instructions unknown verb", argv: []string{"agent", "instructions", "bogus"},
			notice:  "projmux: agent instructions is deprecated; " + namespaceUse,
			errText: "agent instructions requires attach or detach", usage: true},

		{name: "create agent --persona", argv: []string{"create", "agent", "--provider", "claude", "--persona", "reviewer", "-p", "no-such-project"},
			notice: cli.DeprecatedPersonaFlagNotice, errText: noRuntime},
		{name: "create claude --persona=", argv: []string{"create", "claude", "--persona=reviewer", "-p", "no-such-project", "-o", "json"},
			notice: cli.DeprecatedPersonaFlagNotice, errText: noRuntime},
		{name: "create agent --persona twice", argv: []string{"create", "agent", "--provider", "claude", "--persona", "a", "--persona", "reviewer", "-p", "no-such-project"},
			notice: cli.DeprecatedPersonaFlagNotice, errText: noRuntime},
		{name: "create agent --persona and --instructions", argv: []string{"create", "agent", "--provider", "claude", "--instructions", "reviewer", "--persona", "reviewer"},
			notice: cli.DeprecatedPersonaFlagNotice, errText: "create agent accepts only one of --instructions and --persona", usage: true},
		{name: "create agent --persona then a rejected flag", argv: []string{"create", "agent", "--persona", "reviewer", "--bogus"},
			notice: cli.DeprecatedPersonaFlagNotice, errText: "flag provided but not defined: -bogus", usage: true, restStderr: "Usage:\n  projmux create agent "},
		{name: "create agent a rejected flag then --persona", argv: []string{"create", "agent", "--bogus", "--persona", "reviewer"},
			notice: cli.DeprecatedPersonaFlagNotice, errText: "flag provided but not defined: -bogus", usage: true, restStderr: "Usage:\n  projmux create agent "},
		{name: "create agent -persona single dash", argv: []string{"create", "agent", "--provider", "claude", "-persona", "reviewer", "-p", "no-such-project"},
			notice: cli.DeprecatedPersonaFlagNotice, errText: noRuntime},
		{name: "create antigravity --persona", argv: []string{"create", "antigravity", "--persona", "reviewer", "-p", "no-such-project"},
			notice: cli.DeprecatedPersonaFlagNotice, errText: "--persona", usage: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			stdout, stderr, err := run(test.argv...)
			switch {
			case test.stdoutPrefix != "":
				if !strings.HasPrefix(stdout, test.stdoutPrefix) {
					t.Errorf("stdout = %q, want prefix %q", stdout, test.stdoutPrefix)
				}
			case stdout != test.stdout:
				t.Errorf("stdout = %q, want %q", stdout, test.stdout)
			}
			if strings.Contains(stdout, "deprecated") {
				t.Errorf("the notice reached stdout: %q", stdout)
			}
			switch {
			case test.errText == "" && err != nil:
				t.Errorf("error = %v, want success", err)
			case test.errText != "" && (err == nil || !strings.Contains(err.Error(), test.errText)):
				t.Errorf("error = %v, want one containing %q", err, test.errText)
			case err != nil && IsUsageError(err) != test.usage:
				t.Errorf("error %q usage = %v, want %v", err, IsUsageError(err), test.usage)
			}
			rest, found := strings.CutPrefix(stderr, test.notice+"\n")
			if !found {
				t.Fatalf("stderr = %q, want it to start with the one line %q", stderr, test.notice)
			}
			if strings.Contains(rest, "is deprecated") {
				t.Errorf("stderr carries more than one notice: %q", stderr)
			}
			if test.restStderr == "" && rest != "" {
				t.Errorf("stderr after the notice = %q, want nothing", rest)
			}
			if !strings.Contains(rest, test.restStderr) {
				t.Errorf("stderr after the notice = %q, want it to contain %q", rest, test.restStderr)
			}
		})
	}

	// The spellings that replace them print no notice.
	for _, argv := range [][]string{
		{"instructions", "list"},
		{"instructions", "show", "reviewer"},
		{"instructions", "show", "absent"},
		{"instructions"},
		{"agent", "relaunch", "no-such-agent", "--instructions", "reviewer", "--dry-run", "-o", "json"},
		{"agent", "relaunch", "no-such-agent", "--instructions", "none"},
		{"agent", "resume", "no-such-agent"},
		{"create", "agent", "--provider", "claude", "--instructions", "reviewer", "-p", "no-such-project"},
		{"create", "claude", "--instructions=reviewer", "-p", "no-such-project"},
		{"create", "pane", "-p", "no-such-project"},
		// The spelling as another flag's value, and on a route that does not
		// register the flag, is not the deprecated flag.
		{"create", "agent", "--provider", "claude", "--name", "--persona", "-p", "no-such-project"},
		{"create", "agent", "--provider", "claude", "--name=--persona", "-p", "no-such-project"},
		{"create", "pane", "--persona", "reviewer"},
		{"create", "agent", "--provider", "claude", "-p", "no-such-project", "--", "--persona", "reviewer"},
	} {
		if stdout, stderr, _ := run(argv...); strings.Contains(stderr+stdout, "deprecated") {
			t.Errorf("%q printed a deprecation notice: stdout=%q stderr=%q", argv, stdout, stderr)
		}
	}
}

// TestDeprecatedPersonaFlagRowsAreTheClosedList holds the category (b) rows of
// the synopsis flag guard to `--persona` on the three Agent creates that accept
// it: each parses the flag, prints its notice once, and names only
// `--instructions` in its Usage. A fourth row, or a row for another flag, fails
// here rather than widening the exception.
func TestDeprecatedPersonaFlagRowsAreTheClosedList(t *testing.T) {
	t.Parallel()

	var rows []string
	for key, row := range synopsisFlagExceptions {
		if row.refusedBy == "" {
			rows = append(rows, key)
			if row.reason != synopsisDeprecatedPersonaReason {
				t.Errorf("flag exception %q has no refusing function and is not a deprecated --persona row", key)
			}
		}
	}
	slices.Sort(rows)
	want := []string{"create agent persona", "create claude persona", "create codex persona"}
	if !slices.Equal(rows, want) {
		t.Fatalf("deprecated flag rows = %q, want %q", rows, want)
	}
	for _, key := range want {
		route := strings.TrimSuffix(key, " persona")
		var stderr bytes.Buffer
		flags, err := parseResourceCreateFlags(route, []string{"--persona", "reviewer"}, &stderr, resourceCreateShape{split: true, provider: true})
		if err != nil || flags.persona != "reviewer" || flags.personaOption != "persona" {
			t.Errorf("%s --persona reviewer = %+v, %v; want the persona parsed as before", route, flags, err)
		}
		if stderr.String() != cli.DeprecatedPersonaFlagNotice+"\n" {
			t.Errorf("%s --persona stderr = %q, want the one notice line", route, stderr.String())
		}
		usage, public, ok := synopsisCatalogUsage(route)
		joined := strings.Join(usage, "\n")
		if !ok || !public || strings.Contains(joined, "--persona") || !strings.Contains(joined, "[--instructions <name>]") {
			t.Errorf("%s Usage = %q, want a public synopsis that names --instructions and not --persona", route, usage)
		}
	}
}
