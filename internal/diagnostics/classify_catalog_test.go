package diagnostics

import (
	"bytes"
	"encoding/json"
	"fmt"
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
