package cli

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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

// helpFlagSpellings enumerates every argv spelling the shared boundary must
// intercept: both dash prefixes of both names, bare and with any `=value`.
func helpFlagSpellings() []string {
	var out []string
	for _, dashes := range []string{"-", "--"} {
		for _, name := range helpFlagNames {
			out = append(out,
				dashes+name,
				dashes+name+"=true",
				dashes+name+"=false",
				dashes+name+"=",
			)
		}
	}
	return out
}

// TestHelpFlagMatchingTracksTheFlagPackage pins the boundary to exactly what the
// standard library treats as help, proven against the real `flag` package rather
// than against a comment.
//
// flag.FlagSet.parseOne strips one or two leading dashes, splits on the first
// `=`, and looks the remaining name up; an undefined `h`/`help` returns
// flag.ErrHelp whatever the value is. Every spelling below must therefore route
// through the boundary, and the near-miss spellings must not.
func TestHelpFlagMatchingTracksTheFlagPackage(t *testing.T) {
	t.Parallel()

	if !reflect.DeepEqual(helpFlagNames, []string{"help", "h"}) {
		t.Fatalf("helpFlagNames = %q, want [help h]", helpFlagNames)
	}

	for _, spelling := range helpFlagSpellings() {
		if !isHelpFlag(spelling) {
			t.Errorf("isHelpFlag(%q) = false, want true", spelling)
		}
		// The flag package must agree that this is a help request.
		set := flag.NewFlagSet("probe", flag.ContinueOnError)
		set.SetOutput(io.Discard)
		set.Bool("apply", false, "")
		set.String("section", "", "")
		if err := set.Parse([]string{spelling}); !errors.Is(err, flag.ErrHelp) {
			t.Errorf("flag.Parse(%q) error = %v, want flag.ErrHelp", spelling, err)
		}
	}

	// Near misses must stay out of the boundary so ordinary flags, payload, and
	// the bare `help` word keep their existing behavior.
	for _, spelling := range []string{
		"help", "-", "--", "-hello", "--helper", "-h5", "-help-me",
		"---help", "--=help", "-apply", "--section=help", "", "h", "=help",
	} {
		if isHelpFlag(spelling) {
			t.Errorf("isHelpFlag(%q) = true, want false", spelling)
		}
	}
}

// TestNoLeafParserDefinesAHelpFlag guards the invariant the name-level boundary
// depends on: because no command defines a real `help`/`h` flag, intercepting
// every dash/`=value` spelling of those two names cannot swallow a meaningful
// flag. If a future command defines one, this fails and the boundary must be
// revisited before that flag ships.
func TestNoLeafParserDefinesAHelpFlag(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	pattern := regexp.MustCompile(`\.(Bool|String|Int|Int64|Uint|Uint64|Float64|Duration|Var|Func|BoolFunc|TextVar)(Var)?\(\s*(?:&?[\w.\[\]]+,\s*)?"(h|help)"`)
	var offenders []string
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path) // #nosec G304 -- walked repository source
		if readErr != nil {
			return readErr
		}
		if pattern.Match(data) {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("a leaf parser defines a help/h flag, which the shared help boundary would swallow: %v", offenders)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate the repository root")
		}
		dir = parent
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
		// The flag package strips one or two leading dashes before matching, so
		// all four spellings are equivalent help requests to every leaf parser.
		{name: "single dash long root flag", args: []string{"-help"}, ok: true, root: true},
		{name: "double dash short root flag", args: []string{"--h"}, ok: true, root: true},
		{name: "single dash long on route", args: []string{"switch", "-help"}, ok: true, path: []string{"switch"}},
		{name: "double dash short on route", args: []string{"doctor", "--h"}, ok: true, path: []string{"doctor"}},
		{name: "single dash long nested", args: []string{"setup", "terminal", "-help"}, ok: true, path: []string{"setup", "terminal"}},
		{name: "removed hidden helper", args: []string{"popup-wait-key", "--h"}, ok: false},
		{name: "single dash long after terminator is payload", args: []string{"ai", "split", "--", "-help"}, ok: false},
		{name: "double dash short after terminator is payload", args: []string{"ai", "split", "--", "--h"}, ok: false},
		{name: "root flag with trailing tokens", args: []string{"--help", "ai"}, ok: true, root: true},
		{name: "retired top level route", args: []string{"ai", "--help"}, ok: false},
		{name: "retired short flag", args: []string{"ai", "-h"}, ok: false},
		{name: "retired nested route", args: []string{"ai", "settings", "--help"}, ok: false},
		{name: "retired unknown token", args: []string{"ai", "bogus", "--help"}, ok: false},
		{name: "removed hidden helper long", args: []string{"popup-wait-key", "--help"}, ok: false},
		{name: "removed hidden broker", args: []string{"key-broker", "--help"}, ok: false},
		{name: "flag between route and help", args: []string{"setup", "terminal", "--apply", "--help"}, ok: true, path: []string{"setup", "terminal"}},
		{name: "help after terminator is payload", args: []string{"notify", "push", "--", "--help"}, ok: false},
		{name: "short help after terminator is payload", args: []string{"ai", "split", "--", "-h"}, ok: false},
		{name: "bare terminator only", args: []string{"tmux", "print-config", "--"}, ok: false},
		{name: "bare help word is not a help flag", args: []string{"pin", "help"}, ok: false},
		{name: "unknown command keeps its error", args: []string{"nosuchcmd", "--help"}, ok: false},
		{name: "no help token", args: []string{"doctor", "--json"}, ok: false},
		// `=value` spellings are help requests to the flag package too, so the
		// boundary answers them rather than letting them fail in the leaf.
		{name: "long flag with false value", args: []string{"doctor", "--help=false"}, ok: true, path: []string{"doctor"}},
		{name: "long flag with true value", args: []string{"doctor", "--help=true"}, ok: true, path: []string{"doctor"}},
		{name: "single dash name with value", args: []string{"switch", "-help=true"}, ok: true, path: []string{"switch"}},
		{name: "retired short name with value", args: []string{"current", "--h=false"}, ok: false},
		{name: "value spelling after terminator is payload", args: []string{"ai", "split", "--", "--help=true"}, ok: false},
		{name: "near miss flag is not help", args: []string{"doctor", "--helper"}, ok: false},
		{name: "other flag whose value looks like help", args: []string{"doctor", "--section=help"}, ok: false},
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
			name:  "pane read route pins the cwd field projection",
			args:  []string{"get", "pane", "--help"},
			wants: []string{"projmux get pane\n", "Field projections:\n  cwd\n"},
		},
		{
			name:  "attach exposes only the canonical project child",
			args:  []string{"attach", "--help"},
			wants: []string{"projmux attach\n", "Subcommands:", "  project"},
			nots:  []string{"  auto"},
		},
		{
			name:  "canonical route omits a redundant canonical block",
			args:  []string{"version", "--help"},
			wants: []string{"projmux version\n", "Print the current version"},
			nots:  []string{"Canonical route:"},
		},
		{name: "hidden internal helper remains documented internally", args: []string{"internal", "popup-wait-key", "--help"}, wants: []string{"projmux internal popup-wait-key\n", "Read a single key"}},
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

	walkRoutes(Routes(), func(path []string, route Route) {
		if route.Retired {
			return
		}
		for _, flag := range helpFlagSpellings() {
			args := append(append([]string{}, path...), flag)
			target, ok := RequestedHelp(args)
			if !ok {
				t.Errorf("RequestedHelp(%q) reported no help", args)
				continue
			}
			if !reflect.DeepEqual(target.Path, path) {
				t.Errorf("RequestedHelp(%q) path = %q, want %q", args, target.Path, path)
			}
			var out bytes.Buffer
			if err := RenderHelp(&out, target); err != nil {
				t.Errorf("RenderHelp(%q) error = %v", args, err)
				continue
			}
			if !strings.HasPrefix(out.String(), "projmux "+strings.Join(path, " ")+"\n") {
				t.Errorf("help for %q has unexpected header:\n%s", args, out.String())
			}
		}
	})
}
