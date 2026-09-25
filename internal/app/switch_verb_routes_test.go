package app

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/core/candidates"
	corepreview "github.com/crevissepartners/projmux/internal/core/preview"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

// switchVerbRoutes are the dispatch verbs `(*switchCommand).Run` hands to a
// handler of their own, each a public catalog child of `switch`.
var switchVerbRoutes = []string{
	"open", "toggle-tag", "toggle-pin", "kill", "preview",
	"settings", "cycle-pane", "cycle-window", "sidebar-focus", "sidebar-open",
}

// errSwitchVerbSeam stops a verb at its first operational step. It is the
// identity resolver's configuration error, which every open, kill, cycle,
// focus, and path preview checks before it reaches tmux or a store.
var errSwitchVerbSeam = errors.New("switch verb seam: stop before any operational step")

// switchVerbSeams counts every reach into a seam that could have an effect.
type switchVerbSeams struct {
	tmux    capturingSwitchVerbTmuxRunner
	pickers int
	anchors []string
}

type capturingSwitchVerbTmuxRunner struct {
	calls [][]string
}

func (r *capturingSwitchVerbTmuxRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return nil, nil
}

// newSwitchVerbSeamCommand builds a switch command whose reads are fakes and
// whose every effect is either counted or refused by errSwitchVerbSeam.
func newSwitchVerbSeamCommand(t *testing.T) (*switchCommand, *switchVerbSeams) {
	t.Helper()
	home := t.TempDir()
	seams := &switchVerbSeams{}
	_, native := scriptedPicker(t, []pickerStep{{observe: func(intpickercompat.Options) { seams.pickers++ }}})
	cmd := &switchCommand{
		discover: func(inputs candidates.Inputs) ([]string, error) {
			return []string{inputs.CurrentPath}, nil
		},
		pinStore:     func() (switchPinStore, error) { return newStubPinStore(), nil },
		identityErr:  errSwitchVerbSeam,
		validate:     func(string) error { return nil },
		homeDir:      func() (string, error) { return home, nil },
		workingDir:   func() (string, error) { return home, nil },
		lookupEnv:    func(string) string { return "" },
		nativePicker: native,
		tmuxRunner:   &seams.tmux,
		validateProjectOpenRoute: func(_ context.Context, anchor string) error {
			seams.anchors = append(seams.anchors, anchor)
			return errSwitchVerbSeam
		},
	}
	return cmd, seams
}

// TestSwitchVerbsAreCatalogChildrenOfSwitch holds the dispatch set to the
// catalog: every verb Run dispatches is a public child of `switch`, and every
// public child of `switch` is a verb Run dispatches.
func TestSwitchVerbsAreCatalogChildrenOfSwitch(t *testing.T) {
	t.Parallel()
	route, ok := cli.LookupRoute("switch")
	if !ok {
		t.Fatal("catalog has no switch route")
	}
	var children []string
	for _, child := range route.Children {
		if child.Hidden {
			t.Errorf("switch %s is hidden, want a public child", child.Name)
		}
		children = append(children, child.Name)
	}
	if !slices.Equal(children, switchVerbRoutes) {
		t.Fatalf("switch children = %q, want %q", children, switchVerbRoutes)
	}

	// The switch handler does not dispatch `help` as a verb: it is not a
	// declared child. `projmux switch help` is answered by the shared help
	// boundary before any handler runs (TestPublicParentHelpVerbMatchesHelpFlag),
	// so a `help` that does reach the handler is a refused positional operand.
	cmd, seams := newSwitchVerbSeamCommand(t)
	var stdout, stderr bytes.Buffer
	err := cmd.Run([]string{"help"}, &stdout, &stderr)
	if err == nil || !IsUsageError(err) || err.Error() != "switch does not accept positional arguments" {
		t.Fatalf("switch help err = %v, want the parent's positional usage error", err)
	}
	if stdout.Len() != 0 || seams.pickers != 0 {
		t.Fatalf("switch help stdout=%q pickers=%d, want nothing run", stdout.String(), seams.pickers)
	}
}

// TestSwitchVerbMisuseIsItsOwnRouteUsageError drives each verb with argv its
// parser refuses: the error is a usage error (exit 2) that keeps its message,
// stderr is the verb's own usage block (never the parent `switch` line or the
// parent's picker notes), and nothing past the parser runs.
func TestSwitchVerbMisuseIsItsOwnRouteUsageError(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		argv []string
		want string // the reason; empty for a flag error the flag package prints
	}{
		{argv: []string{"open"}, want: "switch open requires exactly 1 argument: <path>"},
		{argv: []string{"open", "/x/a", "/x/b"}, want: "switch open requires exactly 1 argument: <path>"},
		{argv: []string{"open", "--zz"}},
		{argv: []string{"toggle-tag", "/x/a", "/x/b"}, want: "switch toggle-tag accepts at most 1 [path] argument"},
		{argv: []string{"toggle-tag", "   "}, want: "switch toggle-tag requires a non-empty [path] argument"},
		{argv: []string{"toggle-tag", "--zz"}},
		{argv: []string{"toggle-pin", "/x/a", "/x/b"}, want: "switch toggle-pin accepts at most 1 [path] argument"},
		{argv: []string{"toggle-pin", ""}, want: "switch toggle-pin requires a non-empty [path] argument"},
		{argv: []string{"toggle-pin", "--zz"}},
		{argv: []string{"kill", "/x/a", "/x/b"}, want: "switch kill accepts at most 1 [path] argument"},
		{argv: []string{"kill", " "}, want: "switch kill requires a non-empty [path] argument"},
		{argv: []string{"kill", "--zz"}},
		{argv: []string{"preview", "/x/a", "/x/b"}, want: "switch preview accepts at most 1 [path] argument"},
		{argv: []string{"preview", "--ui=dialog", "/x/a"}, want: `invalid --ui value "dialog": expected "popup" or "sidebar"`},
		{argv: []string{"preview", "--zz"}},
		{argv: []string{"settings", "x"}, want: "switch settings does not accept positional arguments"},
		{argv: []string{"settings", "--zz"}},
		{argv: []string{"cycle-pane", "/x/a"}, want: "switch cycle-pane requires exactly 2 arguments: <path> <next|prev>"},
		{argv: []string{"cycle-pane", "/x/a", "next", "x"}, want: "switch cycle-pane requires exactly 2 arguments: <path> <next|prev>"},
		{argv: []string{"cycle-pane", "", "next"}, want: "switch cycle-pane requires a non-empty [path] argument"},
		{argv: []string{"cycle-pane", "/x/a", "sideways"}, want: `switch cycle-pane: direction must be <next|prev>, got "sideways"`},
		{argv: []string{"cycle-pane", "--zz"}},
		{argv: []string{"cycle-window", "/x/a"}, want: "switch cycle-window requires exactly 2 arguments: <path> <next|prev>"},
		{argv: []string{"cycle-window", "--zz", "/x/a", "next"}},
		{argv: []string{"sidebar-focus"}, want: "switch sidebar-focus requires exactly 1 argument: <path>"},
		{argv: []string{"sidebar-focus", "/x/a", "/x/b"}, want: "switch sidebar-focus requires exactly 1 argument: <path>"},
		{argv: []string{"sidebar-focus", "--zz"}},
		{argv: []string{"sidebar-open", "--zz"}},
		{argv: []string{"sidebar-open", "--path", "/x/a", "--anchor", "%1", "x"}, want: "switch sidebar-open does not accept positional arguments"},
		{argv: []string{"sidebar-open", "--path", "/x/a"}, want: "switch sidebar-open requires --anchor"},
		{argv: []string{"sidebar-open", "--anchor", "%1"}, want: "switch sidebar-open requires --path"},
	} {
		name := strings.Join(test.argv, " ")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cmd, seams := newSwitchVerbSeamCommand(t)
			var stdout, stderr bytes.Buffer
			err := cmd.Run(test.argv, &stdout, &stderr)
			if err == nil || !IsUsageError(err) {
				t.Fatalf("switch %s err = %v (%T), want a usage error (exit 2)", name, err, err)
			}
			route := "switch " + test.argv[0]
			var usage bytes.Buffer
			cli.WriteRouteUsage(&usage, route)
			if usage.Len() == 0 || !strings.Contains(usage.String(), "projmux "+route) {
				t.Fatalf("catalog usage of %q = %q, want its own line", route, usage.String())
			}
			printed := stderr.String()
			if test.want != "" {
				if got := err.Error(); got != test.want {
					t.Errorf("switch %s reason = %q, want %q", name, got, test.want)
				}
				if printed != usage.String() {
					t.Errorf("switch %s stderr = %q, want exactly the %s usage block %q", name, printed, route, usage.String())
				}
			} else {
				reason, rest, _ := strings.Cut(printed, "\n")
				if !strings.HasPrefix(reason, "flag provided but not defined: ") || rest != usage.String() {
					t.Errorf("switch %s stderr = %q, want one flag reason line and then exactly the %s usage block %q", name, printed, route, usage.String())
				}
			}
			for _, foreign := range []string{"projmux switch [--ui", "Picker Actions:", "Options:"} {
				if strings.Contains(printed, foreign) {
					t.Errorf("switch %s stderr prints the parent's %q: %q", name, foreign, printed)
				}
			}
			if stdout.Len() != 0 || seams.pickers != 0 || len(seams.tmux.calls) != 0 || len(seams.anchors) != 0 {
				t.Errorf("switch %s ran past its parser: stdout=%q pickers=%d tmux=%q anchors=%q", name, stdout.String(), seams.pickers, seams.tmux.calls, seams.anchors)
			}
		})
	}
}

// switchVerbShellArgv reads the argv of a generated `exec '<bin>' 'switch' …`
// or `<env> '<bin>' 'switch' …` shell command: the tokens from `switch` to the
// end or to a trailing `|| :`, with one level of single quotes removed.
func switchVerbShellArgv(t *testing.T, command string) []string {
	t.Helper()
	var tokens []string
	var current strings.Builder
	inQuote, started := false, false
	for i := 0; i < len(command); i++ {
		ch := command[i]
		switch {
		case inQuote && ch == '\'':
			inQuote = false
		case inQuote:
			current.WriteByte(ch)
		case ch == '\'':
			inQuote, started = true, true
		case ch == '\\' && i+1 < len(command):
			i++
			current.WriteByte(command[i])
			started = true
		case ch == ' ':
			if started {
				tokens = append(tokens, current.String())
			}
			current.Reset()
			started = false
		default:
			current.WriteByte(ch)
			started = true
		}
	}
	if started {
		tokens = append(tokens, current.String())
	}
	start := slices.Index(tokens, "switch")
	if start < 0 {
		t.Fatalf("generated command %q runs no switch route", command)
	}
	argv := tokens[start:]
	if end := slices.Index(argv, "||"); end >= 0 {
		argv = argv[:end]
	}
	return argv
}

// TestSwitchVerbCallerArgvParsesUnderItsRoute builds every argv a generated
// caller emits into a switch verb -- from the picker builders, the key binding
// catalog, the sidebar continuation, and the attach project forward -- and
// runs it through Run on seams: it must pass the verb's parser (no usage
// error, no usage printed) and stop at the seam without an effect.
func TestSwitchVerbCallerArgvParsesUnderItsRoute(t *testing.T) {
	t.Parallel()
	const bin = "/usr/local/bin/projmux"
	build := func(command string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return command
	}
	type callerArgv struct {
		name string
		argv []string
	}
	var cases []callerArgv
	substitute := func(name, command string, selections ...string) {
		for _, selection := range selections {
			argv := switchVerbShellArgv(t, command)
			for i, token := range argv {
				if token == "{2}" {
					argv[i] = selection
				}
			}
			cases = append(cases, callerArgv{name: name + " " + selection, argv: argv[1:]})
		}
	}
	// The native picker never runs a preview for an empty selection, so the
	// preview argv always carries a target; a focus action may carry an empty
	// one, which sidebar-focus accepts as a no-op.
	for _, ui := range []string{switchUIPopup, switchUISidebar} {
		substitute("preview --ui="+ui, build(inttmux.BuildSwitchPreviewCommand(bin, ui)), "/abs/path", "uid:proj-x", switchSettingsSentinel)
	}
	for _, direction := range []corepreview.Direction{corepreview.DirectionPrev, corepreview.DirectionNext} {
		substitute("cycle-window "+string(direction), build(inttmux.BuildSwitchCycleWindowCommand(bin, string(direction))), "/abs/path")
		substitute("cycle-pane "+string(direction), build(inttmux.BuildSwitchCyclePaneCommand(bin, string(direction))), "/abs/path")
	}
	substitute("sidebar-focus", build(inttmux.BuildSwitchSidebarFocusCommand(bin)), "/abs/path", switchSettingsSentinel, "")

	for _, action := range defaultKeyBindingCatalog() {
		body, ok := strings.CutPrefix(action.TmuxBody, "switch ")
		if !ok {
			continue
		}
		argv := strings.Fields(body)
		for i, token := range argv {
			if token == "#{q:pane_current_path}" {
				argv[i] = "/abs/path"
			}
		}
		cases = append(cases, callerArgv{name: "key binding " + action.ID, argv: argv})
	}

	for _, client := range []string{"/dev/pts/1", ""} {
		cmd := &switchCommand{
			tmuxRunner:    &capturingSwitchVerbTmuxRunner{},
			rawExecutable: func() (string, error) { return bin, nil },
			lookupEnv: func(name string) string {
				if name == inttmux.SwitchTargetClientEnv {
					return client
				}
				return ""
			},
		}
		plan := switchPlan{Selection: "/p", SessionName: "s", Query: "q", Anchor: "%1"}
		for _, kind := range []string{projectStartupKindNew, projectStartupKindTopology} {
			runner := &capturingSwitchVerbTmuxRunner{}
			cmd.tmuxRunner = runner
			if err := cmd.launchSidebarOpenContinuation(context.Background(), plan, projectStartupCandidate{Kind: kind}); err != nil {
				t.Fatal(err)
			}
			if len(runner.calls) != 1 || len(runner.calls[0]) != 4 || runner.calls[0][1] != "run-shell" {
				t.Fatalf("sidebar continuation tmux calls = %q, want one run-shell", runner.calls)
			}
			argv := switchVerbShellArgv(t, runner.calls[0][3])
			if client != "" && !slices.Contains(argv, "--client") {
				t.Fatalf("sidebar continuation argv %q carries no --client", argv)
			}
			cases = append(cases, callerArgv{name: "sidebar-open continuation " + kind + " client=" + client, argv: argv[1:]})
		}
	}
	if len(cases) < 18 {
		t.Fatalf("collected %d caller argv, want at least 18", len(cases))
	}

	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if !slices.Contains(switchVerbRoutes, test.argv[0]) {
				t.Fatalf("caller argv %q does not reach a switch verb", test.argv)
			}
			cmd, seams := newSwitchVerbSeamCommand(t)
			var stdout, stderr bytes.Buffer
			err := cmd.Run(test.argv, &stdout, &stderr)
			if err != nil && (IsUsageError(err) || !errors.Is(err, errSwitchVerbSeam)) {
				t.Fatalf("switch %q err = %v (usage error %v), want nil or the seam stop", test.argv, err, IsUsageError(err))
			}
			if stderr.Len() != 0 {
				t.Errorf("switch %q printed %q on stderr, want nothing", test.argv, stderr.String())
			}
			if seams.pickers != 0 || len(seams.tmux.calls) != 0 {
				t.Errorf("switch %q reached an effect: pickers=%d tmux=%q", test.argv, seams.pickers, seams.tmux.calls)
			}
			if test.argv[0] == "sidebar-open" && !slices.Equal(seams.anchors, []string{"%1"}) {
				t.Errorf("switch %q validated anchors %q, want the parsed --anchor %%1", test.argv, seams.anchors)
			}
		})
	}

	t.Run("attach project forward", func(t *testing.T) {
		t.Parallel()
		cmd, seams := newSwitchVerbSeamCommand(t)
		attach := &attachCommand{lookupEnv: func(string) string { return "" }, switcher: cmd}
		var stdout, stderr bytes.Buffer
		err := attach.Run([]string{"project", "alpha"}, &stdout, &stderr)
		if err == nil || IsUsageError(err) || !errors.Is(err, errSwitchVerbSeam) {
			t.Fatalf("attach project alpha err = %v (usage error %v), want the switch open seam stop", err, IsUsageError(err))
		}
		if stderr.Len() != 0 || seams.pickers != 0 || len(seams.tmux.calls) != 0 {
			t.Errorf("attach project alpha stderr=%q pickers=%d tmux=%q, want no usage and no effect", stderr.String(), seams.pickers, seams.tmux.calls)
		}
	})
}

// switchVerbFakeRoutes returns a deep enough copy of the catalog with the
// switch child verb removed, and, when dropLine is set, its parent line too.
func switchVerbFakeRoutes(verb string, dropLine bool) []cli.Route {
	routes := slices.Clone(cli.Routes())
	for i, route := range routes {
		if route.Name != "switch" {
			continue
		}
		route.Children = slices.DeleteFunc(slices.Clone(route.Children), func(child cli.Route) bool { return child.Name == verb })
		if dropLine {
			route.Usage = slices.DeleteFunc(slices.Clone(route.Usage), func(line string) bool {
				return strings.HasPrefix(line, "projmux switch "+verb+" ") || line == "projmux switch "+verb
			})
		}
		routes[i] = route
	}
	return routes
}

// switchVerbFakeUsage resolves a route path against routes the way
// synopsisCatalogUsage resolves it against the real catalog.
func switchVerbFakeUsage(routes []cli.Route) func(string) ([]string, bool, bool) {
	return func(route string) ([]string, bool, bool) {
		nodes, hidden := routes, false
		var node cli.Route
		for token := range strings.FieldsSeq(route) {
			found := false
			for _, candidate := range nodes {
				if candidate.Name == token {
					node, found = candidate, true
					break
				}
			}
			if !found {
				return nil, false, false
			}
			hidden = hidden || node.Hidden
			nodes = node.Children
		}
		return node.Usage, !hidden, true
	}
}

// TestSwitchVerbGuardsCatchAMissingCatalogNode is the negative control: a
// catalog without the `switch kill` node fails the synopsis flag guard (the
// FlagSet `switch kill` names no catalog route), and, with its parent line
// gone too, fails the route verb guard (the dispatcher's `kill` is missing
// from the switch Usage). Each failure names the route.
func TestSwitchVerbGuardsCatchAMissingCatalogNode(t *testing.T) {
	t.Parallel()

	pkgs := flagParseGuardLoadRepo(t, filepath.Join("..", ".."))
	report := flagParseGuardAnalyze(pkgs)
	eval, problems := newFlagSetNameEval(pkgs)
	if len(problems) != 0 {
		t.Fatalf("flag set name evaluator: %q", problems)
	}
	checked, _ := synopsisFlagProblems(newSynopsisFlagEngine(eval), report, synopsisFlagInputs{
		hidden:         flagParseGuardExceptions,
		names:          flagSetNameExceptions,
		siteExceptions: synopsisFlagSiteExceptions,
		flagExceptions: synopsisFlagExceptions,
		aliases:        synopsisFlagAliases,
		usage:          switchVerbFakeUsage(switchVerbFakeRoutes("kill", false)),
	})
	want := `route "switch kill" is not a catalog route`
	if !slices.ContainsFunc(checked, func(p string) bool { return strings.Contains(p, "runKill") && strings.Contains(p, want) }) {
		t.Errorf("synopsis flag guard over a catalog without switch kill: missing %q in:\n%s", want, strings.Join(checked, "\n"))
	}

	var lines []routeVerbUsageLine
	var walk func(nodes []cli.Route, prefix []string)
	walk = func(nodes []cli.Route, prefix []string) {
		for _, node := range nodes {
			path := append(slices.Clone(prefix), node.Name)
			if path[0] == "internal" {
				continue
			}
			for _, usage := range node.Usage {
				lines = append(lines, routeVerbUsageLine{run: usageVerbRun(usage), source: strings.Join(path, " ")})
			}
			walk(node.Children, path)
		}
	}
	walk(switchVerbFakeRoutes("kill", true), nil)
	positions := routeVerbPositions(lines, routeVerbPinnedPositions...)
	d := routeVerbDispatchers["switch"]
	dispatched, problem := dispatcherVerbs(t, filepath.Join("..", ".."), d)
	if problem != "" {
		t.Fatal(problem)
	}
	drift := routeVerbDrift("switch", positions["switch"], d, dispatched)
	if !strings.Contains(drift, `verb position "switch"`) || !strings.Contains(drift, "missing from Usage [kill]") {
		t.Errorf("route verb guard over a catalog without switch kill: drift = %q, want the switch position missing kill", drift)
	}
	if clean := routeVerbDrift("switch", catalogRouteVerbUsage(t)["switch"], d, dispatched); clean != "" {
		t.Errorf("route verb guard over the real catalog: %s", clean)
	}
}
