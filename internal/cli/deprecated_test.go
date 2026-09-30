package cli

import (
	"bytes"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// deprecatedSpellings is the closed list of deprecated routes and the
// replacement each one names. A route joins or leaves it only by an edit here.
var deprecatedSpellings = map[string]string{
	"agent instructions":        "projmux agent relaunch <agent-ref> --instructions <name>|none",
	"agent instructions attach": "projmux agent relaunch <agent-ref> --instructions <name>",
	"agent instructions detach": "projmux agent relaunch <agent-ref> --instructions none",
	"agent persona":             "projmux agent relaunch <agent-ref> --instructions <name>|none",
	"agent persona attach":      "projmux agent relaunch <agent-ref> --instructions <name>",
	"agent persona detach":      "projmux agent relaunch <agent-ref> --instructions none",
	"persona":                   "projmux instructions <subcommand>",
	"persona list":              "projmux instructions list",
	"persona show":              "projmux instructions show",
	"persona edit":              "projmux instructions edit",
	"persona set":               "projmux instructions set",
	"persona delete":            "projmux instructions delete",
}

// hiddenNodes is the closed list of nodes that carry Hidden themselves. Every
// node under one is out of the listings with it.
var hiddenNodes = []string{"agent instructions", "agent persona", "persona", "internal"}

// deprecatedSpellingPattern matches a deprecated spelling wherever public text
// could advertise it: the `persona` noun group, the `--persona` flag, and the
// attach and detach verbs under either Agent noun.
var deprecatedSpellingPattern = regexp.MustCompile(`--persona|projmux persona\b|\bagent persona\b|\bagent instructions (attach|detach)\b|\bpersona (list|show|edit|set|delete|attach|detach)\b`)

// TestDeprecatedAndHiddenNodesAreTheClosedLists pins which spellings are
// deprecated, what each names as its replacement, and which nodes are hidden.
// A deprecated node that no hidden node covers would still be listed, and a
// hidden node outside `internal` that is not deprecated would lose its help
// verb and catalog usage like plumbing.
func TestDeprecatedAndHiddenNodesAreTheClosedLists(t *testing.T) {
	t.Parallel()

	deprecated := map[string]string{}
	var hidden []string
	walkRoutes(Routes(), func(path []string, route Route) {
		spelling := strings.Join(path, " ")
		if route.Hidden {
			hidden = append(hidden, spelling)
			if path[0] != "internal" && route.Deprecated == "" {
				t.Errorf("hidden route %q is neither internal plumbing nor deprecated", spelling)
			}
		}
		if route.Deprecated == "" {
			return
		}
		deprecated[spelling] = route.Deprecated
		if !unlisted(spelling) {
			t.Errorf("deprecated route %q has no hidden node on its path, so it is still listed", spelling)
		}
		if unlisted(strings.TrimPrefix(route.Deprecated, "projmux ")) {
			t.Errorf("deprecated route %q names the hidden replacement %q", spelling, route.Deprecated)
		}
		if _, _, ok := Resolve(strings.Fields(strings.TrimPrefix(route.Deprecated, "projmux "))); !ok {
			t.Errorf("deprecated route %q names the replacement %q, which is not a route", spelling, route.Deprecated)
		}
	})
	if !reflect.DeepEqual(deprecated, deprecatedSpellings) {
		t.Errorf("deprecated routes = %v, want %v", deprecated, deprecatedSpellings)
	}
	if !reflect.DeepEqual(hidden, hiddenNodes) {
		t.Errorf("hidden nodes = %v, want %v", hidden, hiddenNodes)
	}
}

// TestPublicSurfaceNamesNoDeprecatedSpelling is the public surface guard: no
// listed route advertises a deprecated spelling in its summary, usage, notes,
// or canonical hints, and neither do the three renderings built from them --
// root help, every listed route's help, and the generated reference.
func TestPublicSurfaceNamesNoDeprecatedSpelling(t *testing.T) {
	t.Parallel()

	report := func(where, text string) {
		t.Helper()
		if match := deprecatedSpellingPattern.FindString(text); match != "" {
			t.Errorf("%s names the deprecated spelling %q", where, match)
		}
	}

	var listed, positive int
	walkRoutes(Routes(), func(path []string, route Route) {
		spelling := strings.Join(path, " ")
		fields := append(append([]string{route.Summary, route.CanonicalSummary}, route.Usage...), route.Notes...)
		if unlisted(spelling) {
			// Positive control: the pattern does match what it exists to keep
			// out, on the nodes that are allowed to say it.
			if deprecatedSpellingPattern.MatchString(strings.Join(fields, "\n")) {
				positive++
			}
			return
		}
		listed++
		for _, field := range fields {
			report("route "+spelling, field)
		}
		var help bytes.Buffer
		if err := RenderRouteHelp(&help, path, route); err != nil {
			t.Fatalf("RenderRouteHelp(%q) error = %v", spelling, err)
		}
		report("help of "+spelling, help.String())
		for _, child := range route.Children {
			if child.Hidden && strings.Contains(help.String(), "\n  "+child.Name+" ") {
				t.Errorf("help of %s lists the hidden subcommand %q", spelling, child.Name)
			}
		}
	})
	if listed == 0 || positive != len(deprecatedSpellings) {
		t.Fatalf("guard is vacuous: %d listed routes, pattern matched %d of %d deprecated routes", listed, positive, len(deprecatedSpellings))
	}

	var root bytes.Buffer
	if err := RenderRootHelp(&root); err != nil {
		t.Fatalf("RenderRootHelp error = %v", err)
	}
	report("root help", root.String())
	if strings.Contains(root.String(), "\n  persona ") {
		t.Error("root help lists the hidden persona route")
	}
	if !strings.Contains(root.String(), "\n  instructions ") {
		t.Error("root help lost the instructions route")
	}

	var reference bytes.Buffer
	if err := RenderReference(&reference); err != nil {
		t.Fatalf("RenderReference error = %v", err)
	}
	report("generated reference", reference.String())
	for _, want := range []string{"`projmux instructions list`", "`projmux agent relaunch`", "[--instructions <name>]"} {
		if !strings.Contains(reference.String(), want) {
			t.Errorf("generated reference lost %s", want)
		}
	}
}

// TestDeprecationNoticeNamesTheReplacementOncePerCall is the notice table: the
// deepest deprecated node owns the sentence, and no other argv gets one.
func TestDeprecationNoticeNamesTheReplacementOncePerCall(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		argv []string
		want string
	}{
		{[]string{"persona"}, "projmux: persona is deprecated; use `projmux instructions <subcommand>` instead."},
		{[]string{"persona", "bogus"}, "projmux: persona is deprecated; use `projmux instructions <subcommand>` instead."},
		{[]string{"persona", "help"}, "projmux: persona is deprecated; use `projmux instructions <subcommand>` instead."},
		{[]string{"persona", "list"}, "projmux: persona list is deprecated; use `projmux instructions list` instead."},
		{[]string{"persona", "show", "x"}, "projmux: persona show is deprecated; use `projmux instructions show` instead."},
		{[]string{"persona", "edit", "x"}, "projmux: persona edit is deprecated; use `projmux instructions edit` instead."},
		{[]string{"persona", "set", "x", "-"}, "projmux: persona set is deprecated; use `projmux instructions set` instead."},
		{[]string{"persona", "delete", "x", "--yes"}, "projmux: persona delete is deprecated; use `projmux instructions delete` instead."},
		{[]string{"persona", "list", "--help"}, "projmux: persona list is deprecated; use `projmux instructions list` instead."},
		{[]string{"agent", "persona"}, "projmux: agent persona is deprecated; use `projmux agent relaunch <agent-ref> --instructions <name>|none` instead."},
		{[]string{"agent", "persona", "attach", "uid:a", "x", "--dry-run", "-o", "json"}, "projmux: agent persona attach is deprecated; use `projmux agent relaunch <agent-ref> --instructions <name>` instead."},
		{[]string{"agent", "persona", "detach", "uid:a"}, "projmux: agent persona detach is deprecated; use `projmux agent relaunch <agent-ref> --instructions none` instead."},
		{[]string{"agent", "instructions"}, "projmux: agent instructions is deprecated; use `projmux agent relaunch <agent-ref> --instructions <name>|none` instead."},
		{[]string{"agent", "instructions", "attach", "uid:a", "x"}, "projmux: agent instructions attach is deprecated; use `projmux agent relaunch <agent-ref> --instructions <name>` instead."},
		{[]string{"agent", "instructions", "detach", "uid:a", "--yes"}, "projmux: agent instructions detach is deprecated; use `projmux agent relaunch <agent-ref> --instructions none` instead."},
		{[]string{"agent", "instructions", "--help"}, "projmux: agent instructions is deprecated; use `projmux agent relaunch <agent-ref> --instructions <name>|none` instead."},

		{nil, ""},
		{[]string{"nosuchcmd", "persona"}, ""},
		{[]string{"instructions"}, ""},
		{[]string{"instructions", "list"}, ""},
		{[]string{"instructions", "show", "persona"}, ""},
		{[]string{"agent"}, ""},
		{[]string{"agent", "help", "persona"}, ""},
		{[]string{"agent", "relaunch", "uid:a", "--instructions", "persona"}, ""},
		{[]string{"agent", "resume", "persona"}, ""},
		{[]string{"agent", "--", "persona", "attach"}, ""},
		{[]string{"agent", "message", "send", "uid:a", "--", "persona", "list"}, ""},
		{[]string{"create", "agent", "--instructions", "x"}, ""},
		{[]string{"create", "agent", "--persona", "x"}, ""},
		{[]string{"internal", "popup-wait-key"}, ""},
	} {
		got, ok := DeprecationNotice(test.argv)
		if got != test.want || ok != (test.want != "") {
			t.Errorf("DeprecationNotice(%q) = %q, %v; want %q", test.argv, got, ok, test.want)
		}
		if strings.Contains(got, "\n") {
			t.Errorf("DeprecationNotice(%q) = %q spans more than one line", test.argv, got)
		}
	}
	if !strings.HasPrefix(DeprecatedPersonaFlagNotice, "projmux: --persona is deprecated; use `--instructions`") || strings.Contains(DeprecatedPersonaFlagNotice, "\n") {
		t.Errorf("DeprecatedPersonaFlagNotice = %q, want one line naming --instructions", DeprecatedPersonaFlagNotice)
	}
}

// TestDeprecatedSpellingsStillDispatch fails when a deprecated spelling leaves
// dispatch: each one still reaches its top-level handler with the raw argv
// tail, writes nothing to stdout on the way, and prints exactly its notice on
// stderr. The spellings that replace them reach the same handlers silently.
func TestDeprecatedSpellingsStillDispatch(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		argv       []string
		token      string
		deprecated bool
	}{
		{[]string{"persona"}, "persona", true},
		{[]string{"persona", "list"}, "persona", true},
		{[]string{"persona", "show", "x"}, "persona", true},
		{[]string{"persona", "edit", "x"}, "persona", true},
		{[]string{"persona", "set", "x", "--file", "f"}, "persona", true},
		{[]string{"persona", "delete", "x", "--yes"}, "persona", true},
		{[]string{"agent", "persona", "attach", "uid:a", "x", "--dry-run", "-o", "json"}, "agent", true},
		{[]string{"agent", "persona", "detach", "uid:a", "--yes"}, "agent", true},
		{[]string{"agent", "instructions", "attach", "uid:a", "x"}, "agent", true},
		{[]string{"agent", "instructions", "detach", "uid:a"}, "agent", true},
		{[]string{"agent", "persona"}, "agent", true},
		{[]string{"agent", "instructions", "bogus"}, "agent", true},

		{[]string{"instructions", "list"}, "instructions", false},
		{[]string{"agent", "relaunch", "uid:a", "--instructions", "x"}, "agent", false},
		{[]string{"create", "agent", "--instructions", "x"}, "create", false},
	} {
		var stdout, stderr bytes.Buffer
		root, recorded := newTestRoot(t, &stdout, &stderr)
		if err := root.Execute(test.argv); err != nil {
			t.Fatalf("Execute(%q) error = %v", test.argv, err)
		}
		if len(*recorded) != 1 || (*recorded)[0].token != test.token || !reflect.DeepEqual((*recorded)[0].args, test.argv[1:]) {
			t.Errorf("Execute(%q) invoked %#v, want the %s handler with the raw argv tail", test.argv, *recorded, test.token)
		}
		if stdout.Len() != 0 {
			t.Errorf("Execute(%q) wrote stdout %q before its handler", test.argv, stdout.String())
		}
		lines := strings.Count(stderr.String(), "\n")
		switch {
		case !test.deprecated && stderr.Len() != 0:
			t.Errorf("Execute(%q) wrote stderr %q, want none", test.argv, stderr.String())
		case test.deprecated && (lines != 1 || !strings.HasPrefix(stderr.String(), "projmux: ") || !strings.Contains(stderr.String(), " is deprecated; use `projmux ")):
			t.Errorf("Execute(%q) wrote stderr %q, want exactly one deprecation line", test.argv, stderr.String())
		}
	}
}

// TestDeprecatedSpellingsKeepHelpAndUsage pins what separates a deprecated
// spelling from plumbing: `--help` and the `help` verb still answer with the
// route's help and exit 0, and a rejected flag still prints its catalog usage.
func TestDeprecatedSpellingsKeepHelpAndUsage(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		argv []string
		head string
	}{
		{[]string{"persona", "--help"}, "projmux persona\n"},
		{[]string{"persona", "help"}, "projmux persona\n"},
		{[]string{"persona", "list", "-h"}, "projmux persona list\n"},
		{[]string{"agent", "persona", "--help"}, "projmux agent persona\n"},
		{[]string{"agent", "persona", "help"}, "projmux agent persona\n"},
		{[]string{"agent", "persona", "attach", "--help"}, "projmux agent persona attach\n"},
		{[]string{"agent", "instructions", "--help"}, "projmux agent instructions\n"},
		{[]string{"agent", "instructions", "help"}, "projmux agent instructions\n"},
		{[]string{"agent", "instructions", "detach", "--help"}, "projmux agent instructions detach\n"},
	} {
		var stdout, stderr bytes.Buffer
		root, recorded := newTestRoot(t, &stdout, &stderr)
		if err := root.Execute(test.argv); err != nil {
			t.Fatalf("Execute(%q) error = %v, want help", test.argv, err)
		}
		if len(*recorded) != 0 {
			t.Errorf("Execute(%q) invoked handlers %#v, want help only", test.argv, *recorded)
		}
		if !strings.HasPrefix(stdout.String(), test.head) {
			t.Errorf("Execute(%q) stdout starts %q, want %q", test.argv, firstLine(stdout.String()), test.head)
		}
		if strings.Contains(stdout.String(), "deprecated") {
			t.Errorf("Execute(%q) put the deprecation on stdout:\n%s", test.argv, stdout.String())
		}
		if notice, _ := DeprecationNotice(test.argv); stderr.String() != notice+"\n" {
			t.Errorf("Execute(%q) stderr = %q, want the one notice line %q", test.argv, stderr.String(), notice)
		}
	}

	for _, route := range []string{"persona list", "persona delete", "agent persona attach", "agent instructions detach"} {
		if !publicRoute(route) {
			t.Errorf("deprecated route %q lost its catalog usage on a rejected flag", route)
		}
	}
	if publicRoute("internal popup-wait-key") {
		t.Error("internal plumbing gained catalog usage on a rejected flag")
	}

	// No listed route names a hidden spelling as its canonical route, so the
	// help of a public route never sends a caller to a deprecated one.
	walkRoutes(Routes(), func(path []string, route Route) {
		if unlisted(strings.Join(path, " ")) {
			return
		}
		for _, spelling := range route.Canonical {
			if unlisted(spelling) {
				t.Errorf("listed route %q names the hidden canonical route %q", strings.Join(path, " "), spelling)
			}
		}
	})
}

func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line + "\n"
}
