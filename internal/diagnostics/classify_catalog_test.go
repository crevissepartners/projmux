package diagnostics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
)

// catalogSpelling is one argv spelling of a route graph node: argv is what a
// caller types (possibly with an alias token) and canonical is the node's path
// of canonical Names.
type catalogSpelling struct {
	argv      []string
	canonical []string
}

// catalogSpellings walks the route graph with the same recursion as the CLI's
// own invocation-graph walk, adding each alias spelling of each node.
func catalogSpellings(nodes []cli.Route, prefix []string) []catalogSpelling {
	var out []catalogSpelling
	for _, node := range nodes {
		path := append(append([]string{}, prefix...), node.Name)
		out = append(out, catalogSpelling{argv: path, canonical: path})
		for _, alias := range node.Aliases {
			spelled := append(append([]string{}, prefix...), alias)
			out = append(out, catalogSpelling{argv: spelled, canonical: path})
		}
		out = append(out, catalogSpellings(node.Children, path)...)
	}
	return out
}

// catalogClassificationFailures returns every route graph spelling that does
// not classify to its allowlisted command and canonical subcommand.
func catalogClassificationFailures() []string {
	var failures []string
	for _, spelling := range catalogSpellings(cli.Routes(), nil) {
		argv := strings.Join(spelling.argv, " ")
		// Bare `internal` names no namespace; the documented contract keeps it
		// the empty (unrecorded) class like any unknown argv.
		if len(spelling.canonical) == 1 && spelling.canonical[0] == internalNamespaceToken {
			continue
		}
		got := Classify(spelling.argv)
		if got.Command == "" {
			failures = append(failures, fmt.Sprintf("%q: empty command", argv))
			continue
		}
		recorded := classifyRecorded([]string{got.Command, got.Subcommand})
		if recorded.Command != got.Command || recorded.Subcommand != got.Subcommand {
			failures = append(failures, fmt.Sprintf("%q: reader rejects %q/%q (read back as %q/%q)", argv, got.Command, got.Subcommand, recorded.Command, recorded.Subcommand))
		}
		if !slices.Equal(normalizeCanonicalCompatibility(spelling.argv), spelling.argv) {
			continue // a compatibility wrapper owns this route's class
		}
		canonical := spelling.canonical
		if canonical[0] == internalNamespaceToken {
			namespace := canonical[1]
			if alias, ok := internalNamespaceAliases[namespace]; ok {
				namespace = alias
			}
			canonical = append([]string{namespace}, canonical[2:]...)
		}
		if got.Command != canonical[0] {
			failures = append(failures, fmt.Sprintf("%q: command %q, want %q", argv, got.Command, canonical[0]))
		}
		if len(canonical) >= 2 && got.Subcommand != canonical[1] {
			failures = append(failures, fmt.Sprintf("%q: subcommand %q, want %q", argv, got.Subcommand, canonical[1]))
		}
	}
	return failures
}

// TestEveryCatalogRouteClassifiesItsCommand's negative control mutates the
// package-global commandRules, so neither it nor its subtest may call t.Parallel.
func TestEveryCatalogRouteClassifiesItsCommand(t *testing.T) {
	if failures := catalogClassificationFailures(); len(failures) > 0 {
		t.Fatalf("%d route graph spellings do not classify:\n%s", len(failures), strings.Join(failures, "\n"))
	}

	t.Run("negative control: a missing rule is reported", func(t *testing.T) {
		removed, ok := commandRules["get"]
		if !ok {
			t.Fatal(`control expects a "get" rule`)
		}
		delete(commandRules, "get")
		t.Cleanup(func() { commandRules["get"] = removed })
		failures := catalogClassificationFailures()
		for _, failure := range failures {
			if strings.HasPrefix(failure, `"get`) {
				return
			}
		}
		t.Fatalf("sweep did not report the removed get rule; failures = %q", failures)
	})
}

// catalogHelpShapes are the help spellings appended to every route graph
// spelling, including a help flag after a boolean flag and after a flag value.
var catalogHelpShapes = [][]string{
	{"--help"},
	{"-h"},
	{"--help=x"},
	{"--json", "--help"},
	{"--yes", "--help"},
	{"--file", "y", "--help"},
}

// catalogHelpClassificationFailures returns every route graph spelling and
// help shape that the CLI help boundary answers as help but Classify scores as
// a state change. It reads the boundary through cli.HelpRequested directly, so
// disabling Classify's own use of the boundary cannot shrink the checked set.
//
// A route whose class leaves the subcommand open (`pin project`, `runtime tag`)
// dispatches its verbs in the handler, below the route graph, so the sweep also
// extends it with each subcommand its classification rule allowlists
// (`pin project add --json --help`).
func catalogHelpClassificationFailures() (failures []string, checked, helpTrue int) {
	seen := map[string]bool{}
	check := func(argv []string) {
		key := strings.Join(argv, " ")
		if seen[key] {
			return
		}
		seen[key] = true
		checked++
		if !cli.HelpRequested(argv) {
			return
		}
		helpTrue++
		if got := Classify(argv); got.StateChanging {
			failures = append(failures, fmt.Sprintf("%q: help request classified as a state change (%q/%q)", strings.Join(argv, " "), got.Command, got.Subcommand))
		}
	}
	var visit func(nodes []cli.Route, prefix []string)
	visit = func(nodes []cli.Route, prefix []string) {
		for _, node := range nodes {
			spellings := append([]string{node.Name}, node.Aliases...)
			for _, token := range spellings {
				path := append(slices.Clone(prefix), token)
				for _, shape := range catalogHelpShapes {
					check(append(slices.Clone(path), shape...))
				}
				if len(node.Children) > 0 {
					check(append(slices.Clone(path), "help"))
				}
				class := Classify(path)
				if class.Command == "" || class.Subcommand != "" {
					continue
				}
				rule := commandRules[class.Command]
				verbs := slices.Sorted(maps.Keys(rule.subcommands))
				verbs = append(verbs, slices.Sorted(maps.Keys(rule.aliases))...)
				for _, verb := range verbs {
					for _, shape := range catalogHelpShapes {
						check(append(append(slices.Clone(path), verb), shape...))
					}
				}
			}
			visit(node.Children, append(slices.Clone(prefix), node.Name))
		}
	}
	visit(cli.Routes(), nil)
	return failures, checked, helpTrue
}

// TestCatalogHelpIsNeverAStateChange's negative control swaps the
// package-global boundaryHelpRequested, so neither it nor its subtest may call
// t.Parallel.
func TestCatalogHelpIsNeverAStateChange(t *testing.T) {
	failures, checked, helpTrue := catalogHelpClassificationFailures()
	t.Logf("checked %d argv; the help boundary answers %d as help", checked, helpTrue)
	if failures != nil {
		t.Fatalf("%d help requests classify as state changes:\n%s", len(failures), strings.Join(failures, "\n"))
	}
	// Every spelling answers at least the three bare help flag shapes, so a
	// smaller help set means the sweep stopped covering the catalog.
	if spellings := len(catalogSpellings(cli.Routes(), nil)); helpTrue < 3*spellings {
		t.Fatalf("help set has %d argv, want at least %d (3 per each of %d spellings)", helpTrue, 3*spellings, spellings)
	}

	t.Run("negative control: a classifier ignoring the boundary is reported", func(t *testing.T) {
		original := boundaryHelpRequested
		boundaryHelpRequested = func([]string) bool { return false }
		t.Cleanup(func() { boundaryHelpRequested = original })
		failures, _, _ := catalogHelpClassificationFailures()
		for _, want := range []string{`"update apply --yes --help"`, `"update check --json --help"`, `"hook trust --yes --help"`, `"pin project add --json --help"`} {
			if !slices.ContainsFunc(failures, func(failure string) bool { return strings.HasPrefix(failure, want+":") }) {
				t.Errorf("sweep did not report %s; failures = %q", want, failures)
			}
		}
	})
}

// TestMutationsTheBoundaryDoesNotAnswerStayStateChanging pins that following
// the help boundary suppresses only real help: argv the boundary leaves to a
// handler still records its mutation.
func TestMutationsTheBoundaryDoesNotAnswerStayStateChanging(t *testing.T) {
	t.Parallel()
	for _, argv := range [][]string{
		{"pin", "project", "add", "/tmp/x"},
		// A leaf's `help` word is an operand: a directory named "help".
		{"pin", "project", "add", "help"},
		{"pin", "add", "help"},
		{"update", "apply", "--yes"},
		{"update", "check"},
		{"update", "check", "--json"},
		{"hook", "trust"},
		{"hook", "trust", "--yes"},
		// A help flag after `--` is payload, not a help request.
		{"update", "apply", "--", "--help"},
		{"pin", "project", "add", "--", "--help"},
		// Near-miss spellings are ordinary flags.
		{"update", "apply", "--helper"},
		{"hook", "trust", "-h5"},
	} {
		if cli.HelpRequested(argv) {
			t.Fatalf("cli.HelpRequested(%q) = true; the control needs argv the boundary does not answer", argv)
		}
		if got := Classify(argv); !got.StateChanging {
			t.Errorf("Classify(%q) = %#v, want a state change", argv, got)
		}
	}
}

func TestUnknownArgvStaysUnclassified(t *testing.T) {
	for _, argv := range [][]string{{"nosuchcmd"}, {"internal"}, {"internal", "nosuch"}, {"get", "nosuchkind"}, {"supervise"}, {"agent-pane", "picker"}, {"activation-exec"}} {
		got := Classify(argv)
		if argv[0] == "get" {
			if got.Command != "get" || got.Subcommand != "" {
				t.Fatalf("Classify(%q) = %#v, want get with no subcommand", argv, got)
			}
			continue
		}
		if got != (CommandClass{}) {
			t.Fatalf("Classify(%q) = %#v, want empty", argv, got)
		}
	}
}

// TestReaderAcceptsOldAndCatalogCommandNames pins that the journal reader keeps
// every historical record and every record carrying a newly allowlisted route
// name; none is skipped as unsafe.
func TestReaderAcceptsOldAndCatalogCommandNames(t *testing.T) {
	fixture := func(id, level, command, subcommand string) Event {
		event := Event{At: "2026-09-25T00:00:00Z", Level: "info", Component: "cli", Event: "command.outcome", Result: "success", DurationMS: 1, RunID: id, Version: "0.8.4", MuxBackend: "tmux", Command: command, Subcommand: subcommand}
		if level == "error" {
			event.Level, event.Result, event.Kind, event.Message = "error", "error", "runtime", "command failed"
		}
		return event
	}
	events := []Event{
		fixture("old-unnamed", "error", "", ""),
		fixture("old-switch", "info", "switch", ""),
		fixture("old-session-state", "info", "session-state", "save"),
		fixture("old-prune-session-state", "info", "prune", "session-state"),
		fixture("new-get-pane", "error", "get", "pane"),
		fixture("new-create-window", "error", "create", "window"),
		fixture("new-agent-message", "error", "agent", "message"),
		fixture("new-agent-pane-picker", "error", "agent-pane", "picker"),
	}
	path := filepath.Join(t.TempDir(), "logs", LogFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var journal bytes.Buffer
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		journal.Write(append(line, '\n'))
	}
	if err := os.WriteFile(path, journal.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := NewStore(path).Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(events) {
		t.Fatalf("reader returned %d records, want %d: %#v", len(got), len(events), got)
	}
	for i, want := range events {
		if got[i].RunID != want.RunID || got[i].Command != want.Command || got[i].Subcommand != want.Subcommand {
			t.Fatalf("record %d = %q %q/%q, want %q %q/%q", i, got[i].RunID, got[i].Command, got[i].Subcommand, want.RunID, want.Command, want.Subcommand)
		}
	}
}
