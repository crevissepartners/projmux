package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestRenderRootHelpMatchesHistoricalGolden pins the primary command listing to
// the exact bytes the hand-written printUsage produced before the manifest
// became its source of truth.
func TestRenderRootHelpMatchesHistoricalGolden(t *testing.T) {
	t.Parallel()

	want, err := os.ReadFile(filepath.Join("testdata", "root-help.golden"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	var got bytes.Buffer
	if err := RenderRootHelp(&got); err != nil {
		t.Fatalf("RenderRootHelp returned error: %v", err)
	}
	if got.String() != string(want) {
		t.Fatalf("root help drifted from the golden fixture:\n--- got ---\n%s\n--- want ---\n%s", got.String(), want)
	}
}

// TestRootHelpListsEveryPublicRouteAndNoHiddenHelper proves the listing is
// generated from the manifest: every public route appears exactly once, in
// manifest order, and neither hidden helper leaks into primary help.
func TestRootHelpListsEveryPublicRouteAndNoHiddenHelper(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer
	if err := RenderRootHelp(&out); err != nil {
		t.Fatalf("RenderRootHelp returned error: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) < 3 || lines[0] != "projmux" || lines[1] != "" || lines[2] != "Commands:" {
		t.Fatalf("unexpected help header: %#v", lines[:min(3, len(lines))])
	}
	body := lines[3:]

	var wantTokens []string
	for _, route := range Routes() {
		if route.Hidden {
			continue
		}
		wantTokens = append(wantTokens, route.Name)
	}
	if len(body) != len(wantTokens) {
		t.Fatalf("help body rows = %d, want %d", len(body), len(wantTokens))
	}
	for i, line := range body {
		route, ok := LookupRoute(wantTokens[i])
		if !ok {
			t.Fatalf("manifest lost route %q", wantTokens[i])
		}
		want := "  " + padName(route.Name) + route.Summary
		if line != want {
			t.Fatalf("help row %d = %q, want %q", i, line, want)
		}
	}
	for _, hidden := range []string{"key-broker", "popup-wait-key"} {
		if strings.Contains(out.String(), "  "+hidden) {
			t.Fatalf("primary help exposes hidden helper %q:\n%s", hidden, out.String())
		}
	}
}

// TestPadNameMatchesHistoricalColumnRule pins the exact historical padding: names
// shorter than the column are padded to it, and names at or past the column get
// exactly two trailing spaces.
func TestPadNameMatchesHistoricalColumnRule(t *testing.T) {
	t.Parallel()

	for _, test := range []struct{ name, want string }{
		{name: "ai", want: "ai        "},
		{name: "attention", want: "attention "},
		{name: "resources", want: "resources "},
		{name: "diagnostics", want: "diagnostics  "},
		{name: "session-state", want: "session-state  "},
		{name: "session-popup", want: "session-popup  "},
	} {
		if got := padName(test.name); got != test.want {
			t.Fatalf("padName(%q) = %q, want %q", test.name, got, test.want)
		}
	}
}

// TestRequestedHelpDetection covers the shared help-token contract, including
// the payload boundary: a help flag after the first bare `--` is data.
func TestRequestedHelpDetection(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		args []string
		ok   bool
		root bool
		path []string
	}{
		{name: "no args is root help", args: nil, ok: true, root: true},
		{name: "long root flag", args: []string{"--help"}, ok: true, root: true},
		{name: "short root flag", args: []string{"-h"}, ok: true, root: true},
		{name: "root flag with trailing tokens", args: []string{"--help", "ai"}, ok: true, root: true},
		{name: "top level route", args: []string{"ai", "--help"}, ok: true, path: []string{"ai"}},
		{name: "short flag on route", args: []string{"ai", "-h"}, ok: true, path: []string{"ai"}},
		{name: "nested route", args: []string{"ai", "settings", "--help"}, ok: true, path: []string{"ai", "settings"}},
		{name: "deep unknown token falls back", args: []string{"ai", "bogus", "--help"}, ok: true, path: []string{"ai"}},
		{name: "hidden helper", args: []string{"popup-wait-key", "--help"}, ok: true, path: []string{"popup-wait-key"}},
		{name: "hidden broker", args: []string{"key-broker", "--help"}, ok: true, path: []string{"key-broker"}},
		{name: "flag between route and help", args: []string{"setup", "terminal", "--apply", "--help"}, ok: true, path: []string{"setup", "terminal"}},
		{name: "help after terminator is payload", args: []string{"notify", "push", "--", "--help"}, ok: false},
		{name: "short help after terminator is payload", args: []string{"ai", "split", "--", "-h"}, ok: false},
		{name: "bare terminator only", args: []string{"tmux", "print-config", "--"}, ok: false},
		{name: "bare help word is not a help flag", args: []string{"pin", "help"}, ok: false},
		{name: "unknown command keeps its error", args: []string{"nosuchcmd", "--help"}, ok: false},
		{name: "no help token", args: []string{"doctor", "--json"}, ok: false},
		{name: "help=false is not an exact help token", args: []string{"doctor", "--help=false"}, ok: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			target, ok := RequestedHelp(test.args)
			if ok != test.ok {
				t.Fatalf("RequestedHelp(%q) ok = %v, want %v", test.args, ok, test.ok)
			}
			if ok != HelpRequested(test.args) {
				t.Fatalf("HelpRequested(%q) disagrees with RequestedHelp", test.args)
			}
			if !ok {
				return
			}
			if target.Root != test.root {
				t.Fatalf("RequestedHelp(%q) root = %v, want %v", test.args, target.Root, test.root)
			}
			if !reflect.DeepEqual(target.Path, test.path) {
				t.Fatalf("RequestedHelp(%q) path = %q, want %q", test.args, target.Path, test.path)
			}
		})
	}
}

// TestRenderRouteHelpProjectsManifestMetadata pins the nested help shape and
// proves the output is manifest-driven: usage synopsis, sub-route listing,
// route-local output/field catalogs, and canonical migration guidance.
func TestRenderRouteHelpProjectsManifestMetadata(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		args  []string
		wants []string
		nots  []string
	}{
		{
			name:  "compatibility route lists subcommands and canonical spelling",
			args:  []string{"ai", "--help"},
			wants: []string{"projmux ai\n", "Manage tmux AI split launch and settings", "Usage:", "Subcommands:", "  split", "Canonical route:", "  projmux create agent"},
		},
		{
			name:  "pane read route pins the cwd field projection",
			args:  []string{"current", "--help"},
			wants: []string{"projmux current\n", "Field projections:\n  cwd\n", "Canonical route:\n  projmux get pane\n"},
			nots:  []string{"Output modes:"},
		},
		{
			name:  "ai split pins the pane-id bridge output",
			args:  []string{"ai", "split", "--help"},
			wants: []string{"projmux ai split\n", "Output modes:\n  pane-id\n"},
		},
		{
			name:  "canonical route omits a redundant canonical block",
			args:  []string{"version", "--help"},
			wants: []string{"projmux version\n", "Print the current version"},
			nots:  []string{"Canonical route:"},
		},
		{
			name:  "hidden helper renders help instead of reading a tty",
			args:  []string{"popup-wait-key", "--help"},
			wants: []string{"projmux popup-wait-key\n", "Read a single key for a display-only tmux popup", "projmux internal popup-wait-key"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			target, ok := RequestedHelp(test.args)
			if !ok {
				t.Fatalf("RequestedHelp(%q) reported no help", test.args)
			}
			var out bytes.Buffer
			if err := RenderHelp(&out, target); err != nil {
				t.Fatalf("RenderHelp returned error: %v", err)
			}
			for _, want := range test.wants {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("help for %q missing %q:\n%s", test.args, want, out.String())
				}
			}
			for _, not := range test.nots {
				if strings.Contains(out.String(), not) {
					t.Fatalf("help for %q unexpectedly contains %q:\n%s", test.args, not, out.String())
				}
			}
			if !strings.HasSuffix(out.String(), "Run 'projmux help' for the full command list.\n") {
				t.Fatalf("help for %q lost the shared footer:\n%s", test.args, out.String())
			}
		})
	}
}

// TestRenderHelpForEveryManifestRouteIsNonEmpty proves the help boundary can
// answer every documented route, including hidden helpers and every sub-route,
// so no route can fall through to a handler for help.
func TestRenderHelpForEveryManifestRouteIsNonEmpty(t *testing.T) {
	t.Parallel()

	walkRoutes(Routes(), func(path []string, _ Route) {
		args := append(append([]string{}, path...), "--help")
		target, ok := RequestedHelp(args)
		if !ok {
			t.Errorf("RequestedHelp(%q) reported no help", args)
			return
		}
		if !reflect.DeepEqual(target.Path, path) {
			t.Errorf("RequestedHelp(%q) path = %q, want %q", args, target.Path, path)
		}
		var out bytes.Buffer
		if err := RenderHelp(&out, target); err != nil {
			t.Errorf("RenderHelp(%q) error = %v", args, err)
			return
		}
		if !strings.HasPrefix(out.String(), "projmux "+strings.Join(path, " ")+"\n") {
			t.Errorf("help for %q has unexpected header:\n%s", args, out.String())
		}
	})
}
