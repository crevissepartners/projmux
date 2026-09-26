package main

import (
	"bytes"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/app"
	"github.com/crevissepartners/projmux/internal/cli"
)

// flagParseShapeProbe is an unknown flag no route defines.
const flagParseShapeProbe = "--zz-probe"

// flagParseShapeDefaultListing matches a line of the flag package's own
// PrintDefaults listing (`  -name` or `  -name type`); a catalog Usage line
// starts with `  projmux `.
var flagParseShapeDefaultListing = regexp.MustCompile(`(?m)^  -[A-Za-z]`)

// flagParseShapeProblems judges the stderr of a flag parse failure on a public
// route against usage, the `Usage:` clause `projmux <route> --help` prints:
// the reason line exactly once and first, then that clause exactly once and
// nothing else; no flag package `Usage of <name>:` header and no default flag
// listing. It returns one problem per broken rule.
func flagParseShapeProblems(stderr, usage string) []string {
	var problems []string
	if !strings.HasPrefix(usage, "Usage:\n") {
		return []string{"the route --help prints no `Usage:` clause to compare with: " + strconv.Quote(usage)}
	}
	if strings.Contains(stderr, "Usage of ") {
		problems = append(problems, "prints the flag package default `Usage of <name>:` header")
	}
	if flagParseShapeDefaultListing.MatchString(stderr) {
		problems = append(problems, "prints the flag package default flag listing")
	}
	reason, _, _ := strings.Cut(stderr, "\n")
	switch {
	case reason == "":
		problems = append(problems, "the first stderr line is not a reason")
	case strings.HasPrefix(reason, "Usage"):
		problems = append(problems, "prints the usage before the reason")
	default:
		if n := strings.Count("\n"+stderr, "\n"+reason+"\n"); n != 1 {
			problems = append(problems, "prints the reason "+strconv.Itoa(n)+" times, want once")
		}
	}
	if n := strings.Count(stderr, usage); n != 1 {
		problems = append(problems, "prints the catalog Usage clause "+strconv.Itoa(n)+" times, want once")
	}
	if n := strings.Count("\n"+stderr, "\nUsage:\n"); n != 1 {
		problems = append(problems, "prints "+strconv.Itoa(n)+" `Usage:` blocks, want one")
	}
	if len(problems) == 0 && stderr != reason+"\n"+usage {
		problems = append(problems, "prints more than the reason line and the catalog Usage clause")
	}
	return problems
}

// flagParseShapeUsageClause returns the `Usage:` clause (from `Usage:` to the
// blank line) of what `projmux <argv> --help` prints on stdout.
func flagParseShapeUsageClause(t *testing.T, argv []string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := app.New().Run(append(slices.Clone(argv), "--help"), &stdout, &stderr); err != nil || stderr.Len() != 0 {
		t.Fatalf("%s --help: err = %v, stderr = %q", strings.Join(argv, " "), err, stderr.String())
	}
	help := stdout.String()
	start := strings.Index(help, "\nUsage:\n")
	if start < 0 {
		return ""
	}
	clause := help[start+1:]
	if end := strings.Index(clause, "\n\n"); end >= 0 {
		clause = clause[:end+1]
	}
	return clause
}

// flagParseShapeRun runs argv through the real app and the entrypoint and
// returns the exit code, stdout, stderr, and whether the error handed to the
// diagnostics journal is a usage error.
func flagParseShapeRun(argv []string) (code int, stdout, stderr string, usage bool) {
	var out, errOut bytes.Buffer
	code = executeCLI(
		func() error { return app.New().Run(argv, &out, &errOut) },
		func(err error) { usage = err != nil && app.IsUsageError(err) },
		&errOut,
	)
	return code, out.String(), errOut.String(), usage
}

// flagParseShapeIsolate points every state root at a fresh temporary
// directory, clears the tmux and anchor environment, and empties PATH, so no
// route can reach a live tmux server, a provider, or a lifecycle hook.
func flagParseShapeIsolate(t *testing.T) {
	t.Helper()
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "TMUX_TMPDIR"} {
		t.Setenv(key, t.TempDir())
	}
	for _, key := range []string{"TMUX", "TMUX_PANE", "__PROJMUX_RUNTIME_ANCHOR_PANE", "PROJMUX_CWD"} {
		t.Setenv(key, "")
	}
	t.Setenv("PATH", t.TempDir())
}

// flagParseShapeRoute is one public spelling of a catalog route.
type flagParseShapeRoute struct {
	argv  []string // the spelling, aliases included
	route string   // the canonical path
}

// flagParseShapeRoutes walks the catalog: every public route, parents and
// leaves, under every spelling (name or alias) at every level. The hidden
// `internal` namespace and hidden routes are not public.
func flagParseShapeRoutes() []flagParseShapeRoute {
	var out []flagParseShapeRoute
	var walk func(nodes []cli.Route, argv, canonical []string)
	walk = func(nodes []cli.Route, argv, canonical []string) {
		for _, node := range nodes {
			if node.Hidden {
				continue
			}
			for _, spelling := range append([]string{node.Name}, node.Aliases...) {
				path := append(slices.Clone(argv), spelling)
				canon := append(slices.Clone(canonical), node.Name)
				out = append(out, flagParseShapeRoute{argv: path, route: strings.Join(canon, " ")})
				walk(node.Children, path, canon)
			}
		}
	}
	walk(cli.Routes(), nil, nil)
	return out
}

// flagParseShapeExclusion is a public spelling that does not answer an unknown
// flag with a flag parse failure. run is false for a spelling whose handler
// would change state with the probe as its operand, so the guard never
// executes it.
type flagParseShapeExclusion struct {
	run    bool
	reason string
}

// Exclusion reasons.
const (
	flagParseShapeVerbDispatch = "parent verb dispatch: the probe is read as an unknown child verb, a refusal outside the flag parse contract"
	flagParseShapeOperand      = "operand route: the probe is read as a positional operand and refused by operand validation, outside the flag parse contract"
	flagParseShapeLegacy       = "legacy spelling: the probe reaches a removed-command notice, outside the flag parse contract"
	flagParseShapeDashOperand  = "dash operand taken verbatim: the route parses no flags and would store the probe (exit 0); not executed, since it mutates state"
)

// flagParseShapeExclusions is keyed by the spelled argv. It is closed: a row
// whose spelling the catalog no longer has, or whose route now answers the
// probe with the flag parse shape, is stale.
var flagParseShapeExclusions = map[string]flagParseShapeExclusion{
	"help":                 {run: true, reason: "the root help route prints the root help for any token (exit 0)"},
	"agent":                {run: true, reason: flagParseShapeVerbDispatch},
	"agent approval":       {run: true, reason: flagParseShapeVerbDispatch},
	"agent instructions":   {run: true, reason: flagParseShapeVerbDispatch},
	"agent integrate":      {run: true, reason: flagParseShapeOperand},
	"agent message":        {run: true, reason: flagParseShapeVerbDispatch},
	"agent message send":   {run: true, reason: flagParseShapeOperand},
	"agent persona":        {run: true, reason: flagParseShapeVerbDispatch},
	"agent question":       {run: true, reason: flagParseShapeVerbDispatch},
	"agent sessions":       {run: true, reason: flagParseShapeVerbDispatch},
	"agent topic":          {run: true, reason: flagParseShapeVerbDispatch},
	"agent turn":           {run: true, reason: flagParseShapeVerbDispatch},
	"agent turn interrupt": {run: true, reason: flagParseShapeOperand},
	"agent turn start":     {run: true, reason: flagParseShapeOperand},
	"agent turn steer":     {run: true, reason: flagParseShapeOperand},
	"attach":               {run: true, reason: flagParseShapeLegacy},
	"attention":            {run: true, reason: flagParseShapeVerbDispatch},
	"config":               {run: true, reason: flagParseShapeVerbDispatch},
	"config render":        {run: true, reason: flagParseShapeVerbDispatch},
	"create":               {run: true, reason: flagParseShapeVerbDispatch},
	"delete":               {run: true, reason: flagParseShapeVerbDispatch},
	"describe":             {run: true, reason: flagParseShapeVerbDispatch},
	"diagnostics":          {run: true, reason: flagParseShapeVerbDispatch},
	"focus":                {run: true, reason: flagParseShapeLegacy},
	"get":                  {run: true, reason: flagParseShapeVerbDispatch},
	"get runtime":          {run: true, reason: flagParseShapeVerbDispatch},
	"instructions":         {run: true, reason: flagParseShapeVerbDispatch},
	"instructions delete":  {run: true, reason: flagParseShapeOperand},
	"instructions edit":    {run: true, reason: flagParseShapeOperand},
	"instructions list":    {run: true, reason: flagParseShapeOperand},
	"instructions set":     {run: true, reason: flagParseShapeOperand},
	"instructions show":    {run: true, reason: flagParseShapeOperand},
	"label":                {run: true, reason: flagParseShapeVerbDispatch},
	"notification":         {run: true, reason: flagParseShapeVerbDispatch},
	"open":                 {run: true, reason: flagParseShapeVerbDispatch},
	"persona":              {run: true, reason: flagParseShapeVerbDispatch},
	"persona delete":       {run: true, reason: flagParseShapeOperand},
	"persona edit":         {run: true, reason: flagParseShapeOperand},
	"persona list":         {run: true, reason: flagParseShapeOperand},
	"persona set":          {run: true, reason: flagParseShapeOperand},
	"persona show":         {run: true, reason: flagParseShapeOperand},
	"pin":                  {run: true, reason: flagParseShapeLegacy},
	"profile":              {run: true, reason: flagParseShapeVerbDispatch},
	"profile delete":       {run: true, reason: flagParseShapeOperand},
	"profile list":         {run: true, reason: flagParseShapeOperand},
	"profile set":          {run: true, reason: flagParseShapeOperand},
	"profile show":         {run: true, reason: flagParseShapeOperand},
	"prune":                {run: true, reason: flagParseShapeLegacy},
	"rebind":               {run: true, reason: flagParseShapeVerbDispatch},
	"reconcile":            {run: true, reason: flagParseShapeVerbDispatch},
	"rename":               {run: true, reason: flagParseShapeVerbDispatch},
	"runtime":              {run: true, reason: flagParseShapeVerbDispatch},
	"settings":             {run: true, reason: flagParseShapeOperand},
	"start":                {run: true, reason: flagParseShapeVerbDispatch},
	"stop":                 {run: true, reason: flagParseShapeVerbDispatch},
	"unregister":           {run: true, reason: flagParseShapeVerbDispatch},
	"update":               {run: true, reason: flagParseShapeVerbDispatch},
	"version":              {run: true, reason: flagParseShapeOperand},
	"pin project add":      {reason: flagParseShapeDashOperand},
	"pin project remove":   {reason: flagParseShapeDashOperand},
	"pin project toggle":   {reason: flagParseShapeDashOperand},
	"runtime tag toggle":   {reason: flagParseShapeDashOperand},
}

// TestPublicFlagParseErrorsPrintReasonThenCatalogUsage drives every public
// catalog spelling with an unknown flag through the real app and the
// entrypoint, plus missing-value and bad-boolean samples: stderr is the reason
// line and then the route's catalog `Usage:` clause (byte-identical to its
// --help), stdout is empty, the exit code is 2, and the journal records a
// usage error. A spelling that does not reach a flag parse sits in the closed
// flagParseShapeExclusions table.
func TestPublicFlagParseErrorsPrintReasonThenCatalogUsage(t *testing.T) {
	flagParseShapeIsolate(t)
	routes := flagParseShapeRoutes()
	spelled := map[string]bool{}
	checked := 0
	for _, route := range routes {
		key := strings.Join(route.argv, " ")
		spelled[key] = true
		exclusion, excluded := flagParseShapeExclusions[key]
		if excluded && !exclusion.run {
			continue
		}
		usage := flagParseShapeUsageClause(t, route.argv)
		code, stdout, stderr, usageErr := flagParseShapeRun(append(slices.Clone(route.argv), flagParseShapeProbe))
		problems := flagParseShapeProblems(stderr, usage)
		if excluded {
			if len(problems) == 0 || strings.Contains(stderr, "flag provided but not defined") {
				t.Errorf("%s: stale exclusion row: the route now answers %s with a flag parse failure; drop the row (stderr=%q)", key, flagParseShapeProbe, stderr)
			}
			continue
		}
		checked++
		for _, problem := range problems {
			t.Errorf("%s %s: %s\nstderr=%q", key, flagParseShapeProbe, problem, stderr)
		}
		if code != 2 || stdout != "" || !usageErr {
			t.Errorf("%s %s: exit %d, stdout %q, journaled as usage %v; want exit 2, empty stdout, usage", key, flagParseShapeProbe, code, stdout, usageErr)
		}
	}
	for key := range flagParseShapeExclusions {
		if !spelled[key] {
			t.Errorf("%s: stale exclusion row: the catalog has no such public spelling", key)
		}
	}
	if checked == 0 {
		t.Fatal("no public route was checked; the catalog walk has regressed")
	}

	// Missing values and bad booleans take the same path as an unknown flag.
	for _, argv := range [][]string{
		{"get", "agents", "-o"},
		{"get", "agents", "--all-projects=maybe"},
		{"runtime", "attach", "--keep"},
		{"diagnostics", "log", "--json=maybe"},
		{"config", "providers", "--enable"},
	} {
		usage := flagParseShapeUsageClause(t, argv[:len(argv)-1])
		code, stdout, stderr, usageErr := flagParseShapeRun(argv)
		for _, problem := range flagParseShapeProblems(stderr, usage) {
			t.Errorf("%s: %s\nstderr=%q", strings.Join(argv, " "), problem, stderr)
		}
		if code != 2 || stdout != "" || !usageErr {
			t.Errorf("%s: exit %d, stdout %q, journaled as usage %v; want exit 2, empty stdout, usage", strings.Join(argv, " "), code, stdout, usageErr)
		}
	}
	t.Logf("flag parse shape: %d public spellings checked, %d excluded", checked, len(flagParseShapeExclusions))
}

// TestFlagParseShapeJudgeRejectsDrift is the negative control of the shape
// judge: the flag package default usage, a missing, duplicated, or leading
// Usage, a reason printed twice, and trailing text must each fail; the
// contract shape must pass.
func TestFlagParseShapeJudgeRejectsDrift(t *testing.T) {
	t.Parallel()
	const reason = "flag provided but not defined: -zz\n"
	const usage = "Usage:\n  projmux get agents [-o <mode>]\n"
	if problems := flagParseShapeProblems(reason+usage, usage); len(problems) != 0 {
		t.Fatalf("judge rejected the contract shape: %q", problems)
	}
	for name, stderr := range map[string]string{
		"go default usage":   reason + "Usage of get agents:\n  -o string\n    \tresult projection\n",
		"go default added":   reason + usage + "Usage of get agents:\n  -o string\n",
		"missing usage":      reason,
		"duplicated usage":   reason + usage + usage,
		"reason twice":       reason + reason + usage,
		"usage before":       usage + reason,
		"trailing text":      reason + usage + "\nEvents:\n  post-create\n",
		"entrypoint reprint": reason + usage + reason,
	} {
		if problems := flagParseShapeProblems(stderr, usage); len(problems) == 0 {
			t.Errorf("%s: judge accepted %q", name, stderr)
		}
	}
}
