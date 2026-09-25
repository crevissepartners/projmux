package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
)

// The public route argv guard.
//
// docs/cli-guide.md "Exit codes" promises exit 2 for a usage error. The
// per-site tests (cli_unknown_args_test.go, leftover_args_usage_exit_test.go)
// pin the refusals one handler at a time and flag_parse_usage_guard_test.go
// closes the FlagSet.Parse sites; this guard closes the set of public routes
// instead. For every route docs/cli.md renders as a heading it runs the
// production entrypoint in a child process and requires exit 2 for:
//
//	A      <route> --zz-argv-guard           an unknown flag
//	D-bare <route>                           a parent route with no subcommand
//	D-sub  <route> zz-argv-guard-sub         a parent route with an unknown subcommand
//	C      <route> zz-argv-guard-pos         a leaf route that takes no positional operand
//	V      <route> --<flag> zz-value-guard   a flag whose synopsis declares a closed value set
//
// A new public route is covered the moment it enters the catalog. A route that
// may not answer a form with exit 2 needs a reviewed row in
// publicRouteArgvGuardExceptions, and the guard fails when a row goes stale.
// Form V is keyed by route and flag rather than by route, so it has its own
// reviewed table, publicRouteArgvValueGuardRows, with the same staleness rule.

const (
	// publicRouteArgvGuardChildEnv carries one probe (JSON) to the re-executed
	// test binary; TestPublicRouteArgvGuardChildProcess runs it.
	publicRouteArgvGuardChildEnv = "PMX_TEST_PUBLIC_ROUTE_ARGV_GUARD_CHILD"
	// publicRouteArgvGuardMarker prefixes the child's one result line.
	publicRouteArgvGuardMarker = "public-route-argv-guard-result: "

	publicRouteArgvGuardFlag       = "--zz-argv-guard"
	publicRouteArgvGuardSubcommand = "zz-argv-guard-sub"
	publicRouteArgvGuardOperand    = "zz-argv-guard-pos"

	publicRouteArgvGuardProbeTimeout = 15 * time.Second
	publicRouteArgvGuardParallelism  = 4
)

// The argv forms. The names are the ones the per-site tests and the Task
// notes use.
const (
	publicRouteArgvFormUnknownFlag = "A"
	publicRouteArgvFormBareParent  = "D-bare"
	publicRouteArgvFormUnknownSub  = "D-sub"
	publicRouteArgvFormOperand     = "C"
)

var publicRouteArgvForms = []string{
	publicRouteArgvFormUnknownFlag,
	publicRouteArgvFormBareParent,
	publicRouteArgvFormUnknownSub,
	publicRouteArgvFormOperand,
}

// publicRouteArgvGuardRowKind says how a row is checked.
type publicRouteArgvGuardRowKind int

const (
	// publicRouteArgvOperandRow: a leaf whose catalog synopsis declares a
	// positional operand. Form C is not executed for it: validating an
	// operand's value is out of scope, and a well-formed-looking operand can
	// have effects. Static check: the synopsis still declares an operand.
	publicRouteArgvOperandRow publicRouteArgvGuardRowKind = iota
	// publicRouteArgvUndeclaredOperandRow: a leaf whose handler takes a
	// positional operand its catalog synopsis does not show. Not executed, for
	// the same reason. Static check: the synopsis still does not declare one
	// (once it does, the row must become an operand row).
	publicRouteArgvUndeclaredOperandRow
	// publicRouteArgvRunnableParentRow: a parent route whose catalog synopsis
	// documents the bare parent as a command of its own. Form D-bare is not
	// executed: it would run that command. Static check: the synopsis still
	// has the bare line.
	publicRouteArgvRunnableParentRow
	// publicRouteArgvBehaviorRow: a recorded exit other than 2. It is
	// executed, and the row is stale once the probe exits 2.
	publicRouteArgvBehaviorRow
)

func (k publicRouteArgvGuardRowKind) String() string {
	switch k {
	case publicRouteArgvOperandRow:
		return "operand"
	case publicRouteArgvUndeclaredOperandRow:
		return "undeclared-operand"
	case publicRouteArgvRunnableParentRow:
		return "runnable-parent"
	case publicRouteArgvBehaviorRow:
		return "behavior"
	default:
		return "unknown(" + strconv.Itoa(int(k)) + ")"
	}
}

type publicRouteArgvGuardRow struct {
	route  string // canonical route path without "projmux"
	form   string
	kind   publicRouteArgvGuardRowKind
	reason string
}

const (
	publicRouteArgvOperandReason = "synopsis declares a positional operand; validating its value is out of scope"

	// publicRouteArgvOperandVerbatimReason explains the form A behavior rows
	// of the single-operand pin and tag verbs.
	publicRouteArgvOperandVerbatimReason = "the verb parses no flags: a token with a leading dash, the probe flag included, is taken verbatim as its one operand, so the probe succeeds (J-13 K-2)"

	publicRouteArgvHelpReason = "root policy: `projmux help [anything]` prints the primary listing and exits 0 (internal/cli/root.go SetHelpCommand; docs/cli-guide.md Help boundary: `projmux help` keeps printing the top-level list)"
)

// publicRouteArgvOperandRoutes are the leaves whose catalog synopsis declares
// a positional operand. Each becomes one form C operand row; the guard fails
// when a listed synopsis stops declaring one, and when a new operand leaf is
// missing here.
var publicRouteArgvOperandRoutes = []string{
	"agent status",
	"agent topic",
	"agent resume",
	"agent instructions attach",
	"agent instructions detach",
	"agent persona attach",
	"agent persona detach",
	"agent turn start",
	"agent turn steer",
	"agent turn interrupt",
	"agent approval review",
	"agent review",
	"agent integrate",
	"agent capabilities",
	"agent message send",
	"agent message status",
	"agent message qualify",
	"agent wait",
	"agent question enable",
	"agent question disable",
	"agent question list",
	"agent question answer",
	"agent sessions list",
	"attention toggle",
	"attention clear",
	"attention arm",
	"attention window",
	"attach project",
	"delete project",
	"delete window",
	"delete pane",
	"delete agent",
	"delete notification",
	"describe project",
	"describe window",
	"describe pane",
	"describe agent",
	"focus project",
	"focus window",
	"focus pane",
	"hook edit",
	"hook trust",
	"hook untrust",
	"label project",
	"label window",
	"label pane",
	"label agent",
	"notification ack",
	"open project",
	"instructions show",
	"instructions edit",
	"instructions set",
	"instructions delete",
	"persona show",
	"persona edit",
	"persona set",
	"persona delete",
	"profile show",
	"profile set",
	"profile delete",
	"pin project add",
	"pin project remove",
	"pin project toggle",
	"rebind project",
	"rename project",
	"rename window",
	"rename pane",
	"rename agent",
	"runtime stop",
	"runtime tag toggle",
	"setup terminal",
	"start project",
	"stop project",
	"switch open",
	"switch toggle-tag",
	"switch toggle-pin",
	"switch kill",
	"switch preview",
	"switch cycle-pane",
	"switch cycle-window",
	"switch sidebar-focus",
	"unregister project",
}

// publicRouteArgvGuardExceptions is the closed, reviewed exception table.
var publicRouteArgvGuardExceptions = append(publicRouteArgvGuardSpecialRows(), publicRouteArgvGuardOperandRows()...)

func publicRouteArgvGuardOperandRows() []publicRouteArgvGuardRow {
	rows := make([]publicRouteArgvGuardRow, 0, len(publicRouteArgvOperandRoutes))
	for _, route := range publicRouteArgvOperandRoutes {
		rows = append(rows, publicRouteArgvGuardRow{route: route, form: publicRouteArgvFormOperand, kind: publicRouteArgvOperandRow, reason: publicRouteArgvOperandReason})
	}
	return rows
}

func publicRouteArgvGuardSpecialRows() []publicRouteArgvGuardRow {
	return []publicRouteArgvGuardRow{
		// Behavior rows (executed; stale once the probe exits 2).
		{route: "help", form: publicRouteArgvFormUnknownFlag, kind: publicRouteArgvBehaviorRow, reason: publicRouteArgvHelpReason},
		{route: "help", form: publicRouteArgvFormOperand, kind: publicRouteArgvBehaviorRow, reason: publicRouteArgvHelpReason},
		{route: "pin project add", form: publicRouteArgvFormUnknownFlag, kind: publicRouteArgvBehaviorRow, reason: publicRouteArgvOperandVerbatimReason},
		{route: "pin project remove", form: publicRouteArgvFormUnknownFlag, kind: publicRouteArgvBehaviorRow, reason: publicRouteArgvOperandVerbatimReason},
		{route: "pin project toggle", form: publicRouteArgvFormUnknownFlag, kind: publicRouteArgvBehaviorRow, reason: publicRouteArgvOperandVerbatimReason},
		{route: "runtime tag toggle", form: publicRouteArgvFormUnknownFlag, kind: publicRouteArgvBehaviorRow, reason: publicRouteArgvOperandVerbatimReason},

		// Runnable parents (not executed for D-bare).
		{route: "setup", form: publicRouteArgvFormBareParent, kind: publicRouteArgvRunnableParentRow, reason: "synopsis `projmux setup` documents the bare parent as the interactive terminal-key probe"},
		{route: "switch", form: publicRouteArgvFormBareParent, kind: publicRouteArgvRunnableParentRow, reason: "synopsis `projmux switch [--ui popup|sidebar] [--anchor <pane>]` documents the bare parent as the interactive project picker"},
	}
}

// publicRouteArgvGuardNode is one public route with the synopsis lines that
// name it: its own Usage plus every ancestor Usage line whose leading tokens
// spell its path.
type publicRouteArgvGuardNode struct {
	path     []string
	parent   bool
	synopsis []string
}

func (n publicRouteArgvGuardNode) name() string { return strings.Join(n.path, " ") }

// publicRouteArgvGuardRoutes walks cli.Routes() depth first, skipping Hidden
// nodes, the same set docs/cli.md renders as `projmux ...` headings.
func publicRouteArgvGuardRoutes() []publicRouteArgvGuardNode {
	var out []publicRouteArgvGuardNode
	var walk func(nodes []cli.Route, prefix []string, inherited []string)
	walk = func(nodes []cli.Route, prefix []string, inherited []string) {
		for _, route := range nodes {
			if route.Hidden {
				continue
			}
			path := append(append([]string{}, prefix...), route.Name)
			lines := append(append([]string{}, inherited...), route.Usage...)
			var synopsis []string
			for _, line := range lines {
				if slices.Contains(synopsis, line) {
					continue
				}
				if _, ok := publicRouteArgvSynopsisRest(line, path); ok {
					synopsis = append(synopsis, line)
				}
			}
			out = append(out, publicRouteArgvGuardNode{path: path, parent: len(route.Children) > 0, synopsis: synopsis})
			walk(route.Children, path, lines)
		}
	}
	walk(cli.Routes(), nil, nil)
	return out
}

// publicRouteArgvSynopsisRest matches a synopsis line against a route path and
// returns the tokens after it. A path token matches one `|` alternative, so
// `projmux create claude|antigravity ...` names both provider shortcuts.
func publicRouteArgvSynopsisRest(line string, path []string) ([]string, bool) {
	tokens := strings.Fields(line)
	if len(tokens) < len(path)+1 || tokens[0] != "projmux" {
		return nil, false
	}
	for i, name := range path {
		if !slices.Contains(strings.Split(tokens[i+1], "|"), name) {
			return nil, false
		}
	}
	return tokens[len(path)+1:], true
}

var publicRouteArgvBareWordGroup = regexp.MustCompile(`^\[[a-z][a-z0-9-]*\]$`)

// publicRouteArgvDeclaresOperand reports whether synopsis tokens (after the
// route path) declare a positional operand: a `<x>` placeholder (also inside
// `[<x>]`, `<x>...`, or `uid:<x>`) or a single bracketed bare word such as
// `[pane]`, that is not the value of a preceding flag. Everything after a bare
// `--` is payload, and plain literal words (`get|clear`, `-o json`) are not
// operands.
func publicRouteArgvDeclaresOperand(rest []string) bool {
	flagValuePending := false
	for _, token := range rest {
		if token == "|" {
			flagValuePending = false
			continue
		}
		core := strings.TrimRight(strings.TrimLeft(token, "[{"), "]}.")
		if core == "--" {
			return false
		}
		if strings.HasPrefix(core, "-") {
			// A flag that closes its bracket group, or carries its value
			// after `=`, leaves no value to consume.
			flagValuePending = !strings.HasSuffix(token, "]") && !strings.HasSuffix(token, "}") && !strings.Contains(core, "=")
			continue
		}
		if flagValuePending {
			// A value can alternate into another flag, as in
			// `[--enable <id>|--disable <id>]`.
			alternatives := strings.Split(core, "|")
			last := alternatives[len(alternatives)-1]
			flagValuePending = strings.HasPrefix(last, "-") && !strings.HasSuffix(token, "]") && !strings.HasSuffix(token, "}") && !strings.Contains(last, "=")
			continue
		}
		if strings.Contains(core, "<") || publicRouteArgvBareWordGroup.MatchString(token) {
			return true
		}
	}
	return false
}

func (n publicRouteArgvGuardNode) declaresOperand() bool {
	for _, line := range n.synopsis {
		rest, _ := publicRouteArgvSynopsisRest(line, n.path)
		if publicRouteArgvDeclaresOperand(rest) {
			return true
		}
	}
	return false
}

// runnableBare reports whether a synopsis line documents the bare route (no
// subcommand, at most flags) as a command.
func (n publicRouteArgvGuardNode) runnableBare() bool {
	for _, line := range n.synopsis {
		rest, _ := publicRouteArgvSynopsisRest(line, n.path)
		if len(rest) == 0 || strings.HasPrefix(strings.TrimLeft(rest[0], "[{"), "-") {
			return true
		}
	}
	return false
}

// publicRouteArgvDocHeadings parses docs/cli.md's route headings.
func publicRouteArgvDocHeadings(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", cli.ReferencePath))
	if err != nil {
		t.Fatal(err)
	}
	heading := regexp.MustCompile("^#+ `projmux (.+)`$")
	var out []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		if match := heading.FindStringSubmatch(scanner.Text()); match != nil {
			out = append(out, match[1])
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// publicRouteArgvExitCode mirrors executeCLI in cmd/projmux/main.go: nil is 0,
// an error that carries ExitCode() is that code, a usage error is 2, and
// anything else is 1. Keep the two in step.
func publicRouteArgvExitCode(err error) int {
	if err == nil {
		return 0
	}
	var coded interface {
		error
		ExitCode() int
	}
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	if IsUsageError(err) {
		return 2
	}
	return 1
}

type publicRouteArgvChildRequest struct {
	Argv []string          `json:"argv"`
	Env  map[string]string `json:"env"`
}

type publicRouteArgvChildResult struct {
	Code  int    `json:"code"`
	Error string `json:"error"`
	// Stderr and StdoutBytes are what the handler wrote; form V reads them.
	Stderr      string `json:"stderr"`
	StdoutBytes int    `json:"stdout_bytes"`
}

// TestPublicRouteArgvGuardChildProcess is the re-executed half of the guard.
// It is a no-op unless publicRouteArgvGuardChildEnv carries a probe. The
// live-machine guard in TestMain has already moved HOME and the XDG layout to
// its shared private root, so the probe's own per-probe directories are
// applied here, then the production entrypoint runs.
func TestPublicRouteArgvGuardChildProcess(t *testing.T) {
	raw := os.Getenv(publicRouteArgvGuardChildEnv)
	if raw == "" {
		return
	}
	var request publicRouteArgvChildRequest
	if err := json.Unmarshal([]byte(raw), &request); err != nil {
		t.Fatalf("decode probe: %v", err)
	}
	for key, value := range request.Env {
		t.Setenv(key, value)
	}
	var stdout, stderr bytes.Buffer
	err := RunWithLifecycleDiagnostics(request.Argv, &stdout, &stderr, nil)
	result := publicRouteArgvChildResult{Code: publicRouteArgvExitCode(err), Stderr: stderr.String(), StdoutBytes: stdout.Len()}
	if err != nil {
		result.Error = err.Error()
	}
	encoded, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	fmt.Fprintf(os.Stdout, "\n%s%s\n", publicRouteArgvGuardMarker, encoded)
}

type publicRouteArgvProbe struct {
	route    string
	form     string
	flag     string // form V only
	argv     []string
	behavior *publicRouteArgvGuardRow
	result   publicRouteArgvChildResult
	failure  string // non-empty when the child could not report a result
}

func (p publicRouteArgvProbe) spelling() string {
	return "projmux " + strings.Join(p.argv, " ")
}

// publicRouteArgvStandIns are fail-closed stand-ins for tools a regressed
// probe could reach: tmux (so no server can start), and the provider CLIs.
var publicRouteArgvStandIns = []string{"tmux", "codex", "claude"}

// runPublicRouteArgvProbes runs every probe in a child process with a scrubbed
// environment and a fresh per-probe HOME/XDG/TMUX_TMPDIR, at bounded
// parallelism.
func runPublicRouteArgvProbes(t *testing.T, probes []publicRouteArgvProbe) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Short base: TMUX_TMPDIR and any socket under it must stay within the
	// unix socket path bound.
	base, err := os.MkdirTemp("/tmp", "pmx-argv-guard-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	bin := filepath.Join(base, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range publicRouteArgvStandIns {
		script := "#!/bin/sh\necho 'public route argv guard: refused to run " + name + "' >&2\nexit 1\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil { // #nosec G306 -- the stand-in must be executable.
			t.Fatal(err)
		}
	}

	// Only what the child needs: its liveguard root (the child joins it
	// instead of making one) and the go command locations the guard would
	// otherwise resolve by running `go env`.
	var inherited []string
	for _, key := range []string{"PMX_TEST_LIVE_MACHINE_GUARD_ROOT", "GOPATH", "GOCACHE", "GOMODCACHE", "GOENV"} {
		if value, ok := os.LookupEnv(key); ok {
			inherited = append(inherited, key+"="+value)
		}
	}

	var wg sync.WaitGroup
	slots := make(chan struct{}, publicRouteArgvGuardParallelism)
	for i := range probes {
		wg.Add(1)
		slots <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-slots }()
			probes[i].result, probes[i].failure = runPublicRouteArgvProbe(t.Context(), executable, filepath.Join(base, "p"+strconv.Itoa(i)), bin, inherited, probes[i].argv)
		}(i)
	}
	wg.Wait()
}

func runPublicRouteArgvProbe(ctx context.Context, executable, dir, bin string, inherited, argv []string) (publicRouteArgvChildResult, string) {
	dirs := map[string]string{
		"HOME":            "h",
		"XDG_CONFIG_HOME": "c",
		"XDG_STATE_HOME":  "s",
		"XDG_DATA_HOME":   "d",
		"XDG_CACHE_HOME":  "k",
		"XDG_RUNTIME_DIR": "r",
		"TMUX_TMPDIR":     "t",
		"TMPDIR":          "tmp",
	}
	overrides := map[string]string{}
	for key, leaf := range dirs {
		path := filepath.Join(dir, leaf)
		if err := os.MkdirAll(path, 0o700); err != nil {
			return publicRouteArgvChildResult{}, err.Error()
		}
		overrides[key] = path
	}
	request, err := json.Marshal(publicRouteArgvChildRequest{Argv: argv, Env: overrides})
	if err != nil {
		return publicRouteArgvChildResult{}, err.Error()
	}
	env := append([]string{
		publicRouteArgvGuardChildEnv + "=" + string(request),
		"PATH=" + bin + string(os.PathListSeparator) + "/usr/bin" + string(os.PathListSeparator) + "/bin",
		"LANG=en_US.UTF-8",
		"EDITOR=false",
		"VISUAL=false",
		// A regressed `update apply` or `update check` cannot reach the network.
		"HTTP_PROXY=http://127.0.0.1:9",
		"HTTPS_PROXY=http://127.0.0.1:9",
		"http_proxy=http://127.0.0.1:9",
		"https_proxy=http://127.0.0.1:9",
		"NO_PROXY=",
	}, inherited...)
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}

	ctx, cancel := context.WithTimeout(ctx, publicRouteArgvGuardProbeTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestPublicRouteArgvGuardChildProcess$") // #nosec G204 -- re-executes this test binary as the guard's own child.
	command.Env = env
	command.Dir = overrides["HOME"]
	command.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	runErr := command.Run()
	if ctx.Err() != nil {
		return publicRouteArgvChildResult{}, fmt.Sprintf("timed out after %s", publicRouteArgvGuardProbeTimeout)
	}
	for line := range strings.SplitSeq(stdout.String(), "\n") {
		payload, ok := strings.CutPrefix(line, publicRouteArgvGuardMarker)
		if !ok {
			continue
		}
		var result publicRouteArgvChildResult
		if err := json.Unmarshal([]byte(payload), &result); err != nil {
			return publicRouteArgvChildResult{}, "undecodable result " + strconv.Quote(payload)
		}
		return result, ""
	}
	return publicRouteArgvChildResult{}, fmt.Sprintf("child reported no result (%v); stdout tail %q; stderr tail %q", runErr, publicRouteArgvTail(stdout.String()), publicRouteArgvTail(stderr.String()))
}

func publicRouteArgvTail(text string) string {
	const limit = 400
	if len(text) > limit {
		return "..." + text[len(text)-limit:]
	}
	return text
}

// checkPublicRouteArgvGuardRows returns every integrity problem in rows
// against the public route set.
func checkPublicRouteArgvGuardRows(rows []publicRouteArgvGuardRow, nodes []publicRouteArgvGuardNode) []string {
	byName := map[string]publicRouteArgvGuardNode{}
	for _, node := range nodes {
		byName[node.name()] = node
	}
	var problems []string
	seen := map[string]bool{}
	for _, row := range rows {
		label := fmt.Sprintf("row {%q, %s, %s}", row.route, row.form, row.kind)
		if strings.TrimSpace(row.reason) == "" {
			problems = append(problems, label+": empty reason")
		}
		if !slices.Contains(publicRouteArgvForms, row.form) {
			problems = append(problems, label+": unknown form")
		}
		key := row.route + "\x00" + row.form
		if seen[key] {
			problems = append(problems, label+": duplicate route/form")
		}
		seen[key] = true
		node, ok := byName[row.route]
		if !ok {
			problems = append(problems, label+": route is not in the public route set")
			continue
		}
		switch row.form {
		case publicRouteArgvFormOperand:
			if node.parent {
				problems = append(problems, label+": form C applies only to a leaf route, and this route has children")
			}
		case publicRouteArgvFormBareParent, publicRouteArgvFormUnknownSub:
			if !node.parent {
				problems = append(problems, label+": form "+row.form+" applies only to a route with children")
			}
		}
		switch row.kind {
		case publicRouteArgvOperandRow:
			if row.form != publicRouteArgvFormOperand {
				problems = append(problems, label+": an operand row must be form C")
			}
			if !node.declaresOperand() {
				problems = append(problems, label+": stale: the synopsis no longer declares a positional operand "+strconv.Quote(strings.Join(node.synopsis, " / ")))
			}
		case publicRouteArgvUndeclaredOperandRow:
			if row.form != publicRouteArgvFormOperand {
				problems = append(problems, label+": an undeclared-operand row must be form C")
			}
			if node.declaresOperand() {
				problems = append(problems, label+": stale: the synopsis now declares an operand; make it an operand row")
			}
		case publicRouteArgvRunnableParentRow:
			if row.form != publicRouteArgvFormBareParent {
				problems = append(problems, label+": a runnable-parent row must be form D-bare")
			}
			if !node.runnableBare() {
				problems = append(problems, label+": stale: the synopsis no longer documents the bare route")
			}
		case publicRouteArgvBehaviorRow:
		default:
			problems = append(problems, label+": unknown kind")
		}
	}
	// Reverse closure: a new operand leaf or runnable parent forces a
	// reviewed row.
	for _, node := range nodes {
		if !node.parent && node.declaresOperand() && !seen[node.name()+"\x00"+publicRouteArgvFormOperand] {
			problems = append(problems, fmt.Sprintf("route %q: its synopsis declares a positional operand but no form C row exists; add an operand row (synopsis %q)", node.name(), strings.Join(node.synopsis, " / ")))
		}
		if node.parent && node.runnableBare() && !seen[node.name()+"\x00"+publicRouteArgvFormBareParent] {
			problems = append(problems, fmt.Sprintf("route %q: its synopsis documents the bare parent as a command but no form D-bare row exists", node.name()))
		}
	}
	return problems
}

// TestPublicRouteArgvGuardEveryRouteRejectsBadArgvWithUsageExit is the guard.
func TestPublicRouteArgvGuardEveryRouteRejectsBadArgvWithUsageExit(t *testing.T) {
	t.Parallel()
	started := time.Now()
	nodes := publicRouteArgvGuardRoutes()

	// Closure: the walked set is exactly docs/cli.md's route headings.
	var walked []string
	for _, node := range nodes {
		walked = append(walked, node.name())
	}
	headings := publicRouteArgvDocHeadings(t)
	var missing, extra []string
	for _, name := range headings {
		if !slices.Contains(walked, name) {
			missing = append(missing, name)
		}
	}
	for _, name := range walked {
		if !slices.Contains(headings, name) {
			extra = append(extra, name)
		}
	}
	if len(missing) > 0 || len(extra) > 0 || len(walked) != len(headings) {
		t.Fatalf("public route walk and docs/cli.md headings disagree (walked %d, headings %d)\nin docs only: %q\nin walk only: %q", len(walked), len(headings), missing, extra)
	}

	if problems := checkPublicRouteArgvGuardRows(publicRouteArgvGuardExceptions, nodes); len(problems) > 0 {
		t.Fatalf("exception table integrity:\n  %s", strings.Join(problems, "\n  "))
	}
	rows := map[string]*publicRouteArgvGuardRow{}
	counts := map[publicRouteArgvGuardRowKind]int{}
	for i := range publicRouteArgvGuardExceptions {
		row := &publicRouteArgvGuardExceptions[i]
		rows[row.route+"\x00"+row.form] = row
		counts[row.kind]++
	}

	var probes []publicRouteArgvProbe
	add := func(node publicRouteArgvGuardNode, form string, extra ...string) {
		probe := publicRouteArgvProbe{route: node.name(), form: form, argv: append(append([]string{}, node.path...), extra...)}
		if row := rows[probe.route+"\x00"+form]; row != nil {
			if row.kind != publicRouteArgvBehaviorRow {
				return
			}
			probe.behavior = row
		}
		probes = append(probes, probe)
	}
	for _, node := range nodes {
		add(node, publicRouteArgvFormUnknownFlag, publicRouteArgvGuardFlag)
		if node.parent {
			add(node, publicRouteArgvFormBareParent)
			add(node, publicRouteArgvFormUnknownSub, publicRouteArgvGuardSubcommand)
		} else {
			add(node, publicRouteArgvFormOperand, publicRouteArgvGuardOperand)
		}
	}
	runPublicRouteArgvProbes(t, probes)

	perForm := map[string]int{}
	var failures []string
	for _, probe := range probes {
		perForm[probe.form]++
		if probe.failure != "" {
			failures = append(failures, fmt.Sprintf("%s %s: %s did not report an exit class: %s", probe.route, probe.form, probe.spelling(), probe.failure))
			continue
		}
		got := probe.result
		if probe.behavior != nil {
			if got.Code == 2 {
				failures = append(failures, fmt.Sprintf("%s %s: %s now exits 2 (%s); the %s row is stale, remove it (reason was: %s)", probe.route, probe.form, probe.spelling(), got.Error, probe.behavior.kind, probe.behavior.reason))
			}
			continue
		}
		if got.Code != 2 {
			failures = append(failures, fmt.Sprintf("%s %s: %s exited %d (%s), want 2 (usage error)", probe.route, probe.form, probe.spelling(), got.Code, got.Error))
		}
	}
	sort.Strings(failures)
	var formCounts []string
	for _, form := range publicRouteArgvForms {
		formCounts = append(formCounts, fmt.Sprintf("%s=%d", form, perForm[form]))
	}
	t.Logf("public route argv guard: %d public routes; probes %s (total %d); exceptions: operand=%d undeclared-operand=%d runnable-parent=%d behavior=%d; wall %s",
		len(nodes), strings.Join(formCounts, " "), len(probes),
		counts[publicRouteArgvOperandRow], counts[publicRouteArgvUndeclaredOperandRow], counts[publicRouteArgvRunnableParentRow], counts[publicRouteArgvBehaviorRow],
		time.Since(started).Round(time.Millisecond))
	if len(failures) > 0 {
		t.Fatalf("%d public route argv probe(s) did not end as a usage error (exit 2):\n  %s", len(failures), strings.Join(failures, "\n  "))
	}
}

// TestPublicRouteArgvGuardSynopsisOperandDetector pins the detector on real
// catalog synopsis lines.
func TestPublicRouteArgvGuardSynopsisOperandDetector(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		line string
		path string
		want bool
	}{
		{"projmux switch [<project>]", "switch", true},
		{"projmux attention toggle [pane]", "attention toggle", true},
		{"projmux setup terminal [terminal] [--apply] [--config <path>] [--allow-symlink]", "setup terminal", true},
		{"projmux runtime stop [<session>...]", "runtime stop", true},
		{"projmux agent topic set <text> [<agent-ref>] [--agent <ref>]", "agent topic", true},
		{"projmux agent message status <message-ref> [-o json]", "agent message status", true},
		{"projmux notification ack <id> | --all", "notification ack", true},
		{"projmux focus window uid:<uid> [--project <ref> | -p <ref>]", "focus window", true},
		{"projmux label project [<ref>] <key=value|key->... [--project <ref> | -p <ref>]", "label project", true},
		{"projmux agent status [get [<agent-ref>] | set <unknown|idle> [<agent-ref>]] [--agent <ref>]", "agent status", true},
		{"projmux instructions set <name> [--file <path> | -]", "instructions set", true},
		// Flag values, literal verbs, and payload are not operands.
		{"projmux config apply [--bin <path>] [--config <path>] [--socket <name>]", "config apply", false},
		{"projmux config edit [--get|--set <mode>]", "config edit", false},
		{"projmux create notification --text <s> --target <SESSION[:WINDOW[.PANE]]> [--socket <s>]", "create notification", false},
		{"projmux prune agent --older-than <duration> [--no-session-ref] [--no-pane] [--exclude <agent-ref>]... [--yes]", "prune agent", false},
		{"projmux create window [--project <ref> | -p <ref>] [--provider shell|<provider>] [-o <mode>] [-- <payload>]", "create window", false},
		{"projmux agent message qualify <claude-agent-ref> --evidence <absolute-private-json> --confirm-isolated-provider-push -o json", "agent message qualify", true},
		{"projmux agent usage [--model <codex|claude|all>] [--window <name>] [--json] [--force]", "agent usage", false},
		{"projmux runtime sessions [--ui=popup|sidebar]", "runtime sessions", false},
		{"projmux welcome [--popup [--force]]", "welcome", false},
		{"projmux pin project list|add|remove|toggle|clear|migrate", "pin project", false},
		{"projmux focus pane <ref> {--project <ref> | -p <ref>} {--window <ref> | -w <ref>}", "focus pane", true},
		{"projmux get pane --current -o cwd", "get pane", false},
		{"projmux config providers [--enable <id>|--disable <id>]", "config providers", false},
		{"projmux agent integrate <codex|claude|antigravity|tmux-bell> [--remove] [--dry-run]", "agent integrate", true},
	} {
		rest, ok := publicRouteArgvSynopsisRest(test.line, strings.Fields(test.path))
		if !ok {
			t.Errorf("%q does not name route %q", test.line, test.path)
			continue
		}
		if got := publicRouteArgvDeclaresOperand(rest); got != test.want {
			t.Errorf("declares operand(%q) = %v, want %v", test.line, got, test.want)
		}
	}
	// Path matching: an alternative names each spelling, a listing line
	// names only the routes it spells, and help's `<route>` names none.
	for _, test := range []struct {
		line, path string
		want       bool
	}{
		{"projmux create claude|antigravity [--cwd <path>]", "create antigravity", true},
		{"projmux runtime stop [<session>...]", "runtime tag", false},
		{"projmux <route> --help", "help", false},
		{"projmux attention toggle|clear|arm|list|window", "attention window", true},
	} {
		if _, ok := publicRouteArgvSynopsisRest(test.line, strings.Fields(test.path)); ok != test.want {
			t.Errorf("synopsis %q names %q = %v, want %v", test.line, test.path, ok, test.want)
		}
	}
}

// TestPublicRouteArgvGuardRowIntegrityChecks proves each self-check fires.
func TestPublicRouteArgvGuardRowIntegrityChecks(t *testing.T) {
	t.Parallel()
	nodes := []publicRouteArgvGuardNode{
		{path: []string{"leaf"}, synopsis: []string{"projmux leaf"}},
		{path: []string{"op"}, synopsis: []string{"projmux op <ref>"}},
		{path: []string{"par"}, parent: true, synopsis: []string{"projmux par a|b"}},
		{path: []string{"run"}, parent: true, synopsis: []string{"projmux run", "projmux run a"}},
		{path: []string{"par", "a"}},
		{path: []string{"par", "b"}},
		{path: []string{"run", "a"}},
	}
	base := []publicRouteArgvGuardRow{
		{route: "op", form: publicRouteArgvFormOperand, kind: publicRouteArgvOperandRow, reason: "r"},
		{route: "run", form: publicRouteArgvFormBareParent, kind: publicRouteArgvRunnableParentRow, reason: "r"},
	}
	if problems := checkPublicRouteArgvGuardRows(base, nodes); len(problems) != 0 {
		t.Fatalf("clean table reported %q", problems)
	}
	for _, test := range []struct {
		name string
		rows []publicRouteArgvGuardRow
		want string
	}{
		{"empty reason", []publicRouteArgvGuardRow{{route: "leaf", form: "A", kind: publicRouteArgvBehaviorRow, reason: " "}}, "empty reason"},
		{"unknown form", []publicRouteArgvGuardRow{{route: "leaf", form: "Z", kind: publicRouteArgvBehaviorRow, reason: "r"}}, "unknown form"},
		{"unknown route", []publicRouteArgvGuardRow{{route: "nope", form: "A", kind: publicRouteArgvBehaviorRow, reason: "r"}}, "not in the public route set"},
		{"duplicate", []publicRouteArgvGuardRow{base[0]}, "duplicate route/form"},
		{"stale operand", []publicRouteArgvGuardRow{{route: "leaf", form: "C", kind: publicRouteArgvOperandRow, reason: "r"}}, "no longer declares a positional operand"},
		{"D row on a leaf", []publicRouteArgvGuardRow{{route: "leaf", form: "D-sub", kind: publicRouteArgvBehaviorRow, reason: "r"}}, "only to a route with children"},
		{"C row on a parent", []publicRouteArgvGuardRow{{route: "par", form: "C", kind: publicRouteArgvBehaviorRow, reason: "r"}}, "only to a leaf route"},
		{"stale runnable parent", []publicRouteArgvGuardRow{{route: "par", form: "D-bare", kind: publicRouteArgvRunnableParentRow, reason: "r"}}, "no longer documents the bare route"},
		{"operand row wrong form", []publicRouteArgvGuardRow{{route: "op", form: "A", kind: publicRouteArgvOperandRow, reason: "r"}}, "operand row must be form C"},
		{"unknown kind", []publicRouteArgvGuardRow{{route: "leaf", form: "A", kind: 99, reason: "r"}}, "unknown kind"},
	} {
		problems := checkPublicRouteArgvGuardRows(append(slices.Clone(base), test.rows...), nodes)
		if !strings.Contains(strings.Join(problems, "\n"), test.want) {
			t.Errorf("%s: problems %q, want one containing %q", test.name, problems, test.want)
		}
	}
	// Reverse closure: dropping a required row is reported.
	if problems := checkPublicRouteArgvGuardRows(base[1:], nodes); !strings.Contains(strings.Join(problems, "\n"), `route "op": its synopsis declares a positional operand`) {
		t.Errorf("missing operand row not reported: %q", problems)
	}
	if problems := checkPublicRouteArgvGuardRows(base[:1], nodes); !strings.Contains(strings.Join(problems, "\n"), `route "run": its synopsis documents the bare parent`) {
		t.Errorf("missing runnable-parent row not reported: %q", problems)
	}
	// Undeclared-operand staleness, on its own.
	nodes = append(nodes, publicRouteArgvGuardNode{path: []string{"op2"}, synopsis: []string{"projmux op2 [<x>]"}})
	rows := append(slices.Clone(base), publicRouteArgvGuardRow{route: "op2", form: "C", kind: publicRouteArgvUndeclaredOperandRow, reason: "r"})
	if problems := checkPublicRouteArgvGuardRows(rows, nodes); !strings.Contains(strings.Join(problems, "\n"), "now declares an operand") {
		t.Errorf("stale undeclared-operand row not reported: %q", problems)
	}
}

// Form V: a flag value outside the closed value set the catalog synopsis
// declares for it. docs/cli-guide.md "Exit codes" lists a bad enum as a usage
// error, so the probe must exit 2 with nothing on stdout, and its stderr must
// name the probe value, which proves the refusal is about this value and not an
// earlier unrelated usage error (a missing required flag, say).
const (
	publicRouteArgvFormValue  = "V"
	publicRouteArgvGuardValue = "zz-value-guard"
)

// publicRouteArgvValueSet is one flag whose synopsis declares a closed value
// set, such as `[--ui popup|sidebar]`.
type publicRouteArgvValueSet struct {
	flag   string // "--ui"
	values []string
}

var publicRouteArgvValueLiteral = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// publicRouteArgvDeclaredValueSets returns the flags in synopsis tokens (after
// the route path) whose value is spelled as two or more literal words joined by
// `|`: `--ui popup|sidebar` or `--ui=popup|sidebar`. A `<...>` placeholder is
// not a closed set, even with `|` inside (`<name|absolute-path>`,
// `<seconds|unlimited>`), and neither is a set with a placeholder alternative
// (`shell|<provider>`).
func publicRouteArgvDeclaredValueSets(rest []string) []publicRouteArgvValueSet {
	var out []publicRouteArgvValueSet
	for i, token := range rest {
		core := strings.TrimRight(strings.TrimLeft(token, "[{"), "]}.")
		if core == "--" {
			return out
		}
		if !strings.HasPrefix(core, "--") {
			continue
		}
		name, value, inline := strings.Cut(core, "=")
		if !inline {
			// A flag that closes its bracket group has no value.
			if strings.HasSuffix(token, "]") || strings.HasSuffix(token, "}") || i+1 >= len(rest) {
				continue
			}
			value = strings.TrimRight(strings.TrimLeft(rest[i+1], "[{"), "]}.")
		}
		alternatives := strings.Split(value, "|")
		if len(alternatives) < 2 || !strings.HasPrefix(name, "--") || len(name) < 3 {
			continue
		}
		closed := true
		for _, alternative := range alternatives {
			if !publicRouteArgvValueLiteral.MatchString(alternative) {
				closed = false
				break
			}
		}
		if closed {
			out = append(out, publicRouteArgvValueSet{flag: name, values: alternatives})
		}
	}
	return out
}

// valueSets returns the closed value-set flags the route's own synopsis lines
// declare. On a parent route only a line that documents the bare route (no
// subcommand word) counts, so `projmux switch preview [--ui ...]` is the
// preview leaf's flag, not the switch parent's.
func (n publicRouteArgvGuardNode) valueSets() []publicRouteArgvValueSet {
	var out []publicRouteArgvValueSet
	for _, line := range n.synopsis {
		rest, _ := publicRouteArgvSynopsisRest(line, n.path)
		if n.parent && len(rest) > 0 && !strings.HasPrefix(strings.TrimLeft(rest[0], "[{"), "-") {
			continue
		}
		for _, set := range publicRouteArgvDeclaredValueSets(rest) {
			if !slices.ContainsFunc(out, func(seen publicRouteArgvValueSet) bool { return seen.flag == set.flag }) {
				out = append(out, set)
			}
		}
	}
	return out
}

// publicRouteArgvValueRowKind says how a form V row is checked.
type publicRouteArgvValueRowKind int

const (
	// publicRouteArgvValueStaticRow: not executed, because a probe that got
	// past the value check could launch a provider, reach tmux, or write
	// state. The value check is pinned by the per-site test named in the
	// reason. Static check: the synopsis still declares the value set.
	publicRouteArgvValueStaticRow publicRouteArgvValueRowKind = iota
	// publicRouteArgvValueUnechoedRow: executed, but the route's reason text
	// (which must not change) does not name the refused value. The probe must
	// still exit 2 with nothing on stdout, and its stderr must contain echo,
	// the reason text that identifies this flag's value refusal. The row is
	// stale once stderr names the probe value.
	publicRouteArgvValueUnechoedRow
)

func (k publicRouteArgvValueRowKind) String() string {
	switch k {
	case publicRouteArgvValueStaticRow:
		return "static"
	case publicRouteArgvValueUnechoedRow:
		return "unechoed"
	default:
		return "unknown(" + strconv.Itoa(int(k)) + ")"
	}
}

type publicRouteArgvValueRow struct {
	route  string
	flag   string
	kind   publicRouteArgvValueRowKind
	reason string
	echo   string // unechoed rows only
}

const (
	publicRouteArgvValueCreateReason = "a create route that gets past its flag checks reaches a provider launch and tmux; the value check is pinned by the per-site create tests (create_agent_test.go, create_resource_test.go, create_test.go, split_cwd_test.go)"
	publicRouteArgvValueNotifyReason = "the value check follows the required --text/--target checks, so the probe would have to spell them and would enqueue a notification if the check regressed; pinned by notify_test.go `bad severity`"
)

// publicRouteArgvValueGuardRows is the closed, reviewed form V exception table.
var publicRouteArgvValueGuardRows = []publicRouteArgvValueRow{
	{route: "create pane", flag: "--placement", kind: publicRouteArgvValueStaticRow, reason: publicRouteArgvValueCreateReason},
	{route: "create pane", flag: "--cwd-from", kind: publicRouteArgvValueStaticRow, reason: publicRouteArgvValueCreateReason},
	{route: "create agent", flag: "--placement", kind: publicRouteArgvValueStaticRow, reason: publicRouteArgvValueCreateReason},
	{route: "create agent", flag: "--cwd-from", kind: publicRouteArgvValueStaticRow, reason: publicRouteArgvValueCreateReason},
	{route: "create codex", flag: "--placement", kind: publicRouteArgvValueStaticRow, reason: publicRouteArgvValueCreateReason},
	{route: "create codex", flag: "--cwd-from", kind: publicRouteArgvValueStaticRow, reason: publicRouteArgvValueCreateReason},
	{route: "create claude", flag: "--placement", kind: publicRouteArgvValueStaticRow, reason: publicRouteArgvValueCreateReason},
	{route: "create claude", flag: "--cwd-from", kind: publicRouteArgvValueStaticRow, reason: publicRouteArgvValueCreateReason},
	{route: "create antigravity", flag: "--placement", kind: publicRouteArgvValueStaticRow, reason: publicRouteArgvValueCreateReason},
	{route: "create antigravity", flag: "--cwd-from", kind: publicRouteArgvValueStaticRow, reason: publicRouteArgvValueCreateReason},
	{route: "create notification", flag: "--severity", kind: publicRouteArgvValueStaticRow, reason: publicRouteArgvValueNotifyReason},

	{route: "diagnostics log", flag: "--level", kind: publicRouteArgvValueUnechoedRow, echo: "diagnostics log --level must be info or error", reason: "the reason text names the allowed values, not the refused one"},
	{route: "get notifications", flag: "--ui", kind: publicRouteArgvValueUnechoedRow, echo: "get notifications --ui must be table or sidebar", reason: "the reason text names the allowed values, not the refused one"},
	{route: "runtime attach", flag: "--fallback", kind: publicRouteArgvValueUnechoedRow, echo: "runtime attach fallback must be one of: home, ephemeral", reason: "the reason text names the allowed values, not the refused one"},
}

// checkPublicRouteArgvValueRows returns every integrity problem in rows
// against the declared value-set flags of the public route set.
func checkPublicRouteArgvValueRows(rows []publicRouteArgvValueRow, nodes []publicRouteArgvGuardNode) []string {
	declared := map[string]bool{}
	for _, node := range nodes {
		for _, set := range node.valueSets() {
			declared[node.name()+"\x00"+set.flag] = true
		}
	}
	var problems []string
	seen := map[string]bool{}
	for _, row := range rows {
		label := fmt.Sprintf("value row {%q, %s, %s}", row.route, row.flag, row.kind)
		if strings.TrimSpace(row.reason) == "" {
			problems = append(problems, label+": empty reason")
		}
		key := row.route + "\x00" + row.flag
		if seen[key] {
			problems = append(problems, label+": duplicate route/flag")
		}
		seen[key] = true
		if !declared[key] {
			problems = append(problems, label+": stale: the synopsis no longer declares a closed value set for this flag")
		}
		switch row.kind {
		case publicRouteArgvValueStaticRow:
			if row.echo != "" {
				problems = append(problems, label+": a static row is not executed and carries no echo text")
			}
		case publicRouteArgvValueUnechoedRow:
			if strings.TrimSpace(row.echo) == "" {
				problems = append(problems, label+": an unechoed row needs the reason text that identifies the refusal")
			}
		default:
			problems = append(problems, label+": unknown kind")
		}
	}
	return problems
}

// publicRouteArgvValueVerdict judges one executed form V probe. It returns ""
// for a pass.
func publicRouteArgvValueVerdict(probe publicRouteArgvProbe, row *publicRouteArgvValueRow) string {
	label := fmt.Sprintf("%s %s %s", probe.route, probe.flag, publicRouteArgvFormValue)
	if probe.failure != "" {
		return fmt.Sprintf("%s: %s did not report an exit class: %s", label, probe.spelling(), probe.failure)
	}
	got := probe.result
	if got.Code != 2 {
		return fmt.Sprintf("%s: %s exited %d (%s), want 2 (usage error)", label, probe.spelling(), got.Code, got.Error)
	}
	if got.StdoutBytes != 0 {
		return fmt.Sprintf("%s: %s wrote %d stdout bytes, want 0", label, probe.spelling(), got.StdoutBytes)
	}
	// The entrypoint prints the returned reason after the handler's stderr.
	stderr := got.Stderr + "\n" + got.Error
	named := strings.Contains(stderr, publicRouteArgvGuardValue)
	if row == nil {
		if !named {
			return fmt.Sprintf("%s: %s exited 2 but stderr does not name %q, so the usage error may not be this flag's value refusal (reason %q)", label, probe.spelling(), publicRouteArgvGuardValue, got.Error)
		}
		return ""
	}
	if named {
		return fmt.Sprintf("%s: %s now names %q on stderr; the %s row is stale, remove it (reason was: %s)", label, probe.spelling(), publicRouteArgvGuardValue, row.kind, row.reason)
	}
	if !strings.Contains(stderr, row.echo) {
		return fmt.Sprintf("%s: %s exited 2 but stderr does not carry %q, so the usage error may not be this flag's value refusal (reason %q)", label, probe.spelling(), row.echo, got.Error)
	}
	return ""
}

// TestPublicRouteArgvGuardEveryValueSetFlagRejectsBadValueWithUsageExit is the
// form V guard.
func TestPublicRouteArgvGuardEveryValueSetFlagRejectsBadValueWithUsageExit(t *testing.T) {
	t.Parallel()
	started := time.Now()
	nodes := publicRouteArgvGuardRoutes()
	if problems := checkPublicRouteArgvValueRows(publicRouteArgvValueGuardRows, nodes); len(problems) > 0 {
		t.Fatalf("value exception table integrity:\n  %s", strings.Join(problems, "\n  "))
	}
	rows := map[string]*publicRouteArgvValueRow{}
	for i := range publicRouteArgvValueGuardRows {
		row := &publicRouteArgvValueGuardRows[i]
		rows[row.route+"\x00"+row.flag] = row
	}

	declared, static, routes := 0, 0, map[string]bool{}
	var probes []publicRouteArgvProbe
	for _, node := range nodes {
		for _, set := range node.valueSets() {
			declared++
			routes[node.name()] = true
			if row := rows[node.name()+"\x00"+set.flag]; row != nil && row.kind == publicRouteArgvValueStaticRow {
				static++
				continue
			}
			if slices.Contains(set.values, publicRouteArgvGuardValue) {
				t.Fatalf("%s %s declares the probe value %q as allowed", node.name(), set.flag, publicRouteArgvGuardValue)
			}
			probes = append(probes, publicRouteArgvProbe{
				route: node.name(),
				form:  publicRouteArgvFormValue,
				flag:  set.flag,
				argv:  append(append([]string{}, node.path...), set.flag, publicRouteArgvGuardValue),
			})
		}
	}
	if declared == 0 {
		t.Fatal("no public route declares a closed flag value set; the synopsis detector is broken")
	}
	runPublicRouteArgvProbes(t, probes)

	var failures []string
	for _, probe := range probes {
		if failure := publicRouteArgvValueVerdict(probe, rows[probe.route+"\x00"+probe.flag]); failure != "" {
			failures = append(failures, failure)
		}
	}
	sort.Strings(failures)
	t.Logf("public route value guard: %d value-set flags on %d routes; executed %d, static %d, unechoed rows %d; wall %s",
		declared, len(routes), len(probes), static, len(publicRouteArgvValueGuardRows)-static, time.Since(started).Round(time.Millisecond))
	if len(failures) > 0 {
		t.Fatalf("%d public route value probe(s) did not end as this flag's usage error (exit 2):\n  %s", len(failures), strings.Join(failures, "\n  "))
	}
}

// TestPublicRouteArgvGuardValueSetDetector pins the value-set detector on real
// catalog synopsis lines.
func TestPublicRouteArgvGuardValueSetDetector(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		line, path string
		want       []string
	}{
		{"projmux switch [--ui popup|sidebar] [--anchor <pane>]", "switch", []string{"--ui=popup|sidebar"}},
		{"projmux runtime sessions [--ui=popup|sidebar]", "runtime sessions", []string{"--ui=popup|sidebar"}},
		{"projmux runtime attach [--keep <n>] [--fallback home|ephemeral]", "runtime attach", []string{"--fallback=home|ephemeral"}},
		{"projmux create notification --text <s> --target <SESSION[:WINDOW[.PANE]]> [--socket <s>] [--severity info|warn|critical] [--source <source>]", "create notification", []string{"--severity=info|warn|critical"}},
		{"projmux create pane [--name <name>] [--label key=value]... [--placement right|down] [--cwd-from project|pane] [-o <mode>] [-- <payload>]", "create pane", []string{"--placement=right|down", "--cwd-from=project|pane"}},
		// Placeholders, open sets, and payload are not closed value sets.
		{"projmux agent usage [--model <codex|claude|all>] [--window <name>] [--json] [--force]", "agent usage", nil},
		{"projmux config agent-questions [--answering <claude|projmux>] [--window <seconds|unlimited>]", "config agent-questions", nil},
		{"projmux reconcile registry [--dry-run] [--source <name|absolute-path>] [-o json]", "reconcile registry", nil},
		{"projmux create window [--project <ref> | -p <ref>] [--provider shell|<provider>] [-o <mode>] [-- <payload>]", "create window", nil},
		{"projmux config edit [--get|--set <mode>]", "config edit", nil},
		{"projmux create pane [-- --ui popup|sidebar]", "create pane", nil},
	} {
		rest, ok := publicRouteArgvSynopsisRest(test.line, strings.Fields(test.path))
		if !ok {
			t.Errorf("%q does not name route %q", test.line, test.path)
			continue
		}
		var got []string
		for _, set := range publicRouteArgvDeclaredValueSets(rest) {
			got = append(got, set.flag+"="+strings.Join(set.values, "|"))
		}
		if !slices.Equal(got, test.want) {
			t.Errorf("value sets(%q) = %q, want %q", test.line, got, test.want)
		}
	}
	// A parent route owns only the flags of its bare synopsis line.
	parent := publicRouteArgvGuardNode{path: []string{"switch"}, parent: true, synopsis: []string{
		"projmux switch [--ui popup|sidebar]",
		"projmux switch preview [--mode a|b] [path]",
	}}
	if got := parent.valueSets(); len(got) != 1 || got[0].flag != "--ui" {
		t.Errorf("parent value sets = %+v, want only --ui", got)
	}
}

// TestPublicRouteArgvGuardValueRowIntegrityChecks proves each form V
// self-check and verdict fires.
func TestPublicRouteArgvGuardValueRowIntegrityChecks(t *testing.T) {
	t.Parallel()
	nodes := []publicRouteArgvGuardNode{
		{path: []string{"leaf"}, synopsis: []string{"projmux leaf [--ui a|b] [--n <x>]"}},
	}
	base := []publicRouteArgvValueRow{{route: "leaf", flag: "--ui", kind: publicRouteArgvValueStaticRow, reason: "r"}}
	if problems := checkPublicRouteArgvValueRows(base, nodes); len(problems) != 0 {
		t.Fatalf("clean table reported %q", problems)
	}
	for _, test := range []struct {
		name string
		rows []publicRouteArgvValueRow
		want string
	}{
		{"empty reason", []publicRouteArgvValueRow{{route: "leaf", flag: "--ui", kind: publicRouteArgvValueStaticRow, reason: " "}}, "empty reason"},
		{"duplicate", []publicRouteArgvValueRow{base[0], base[0]}, "duplicate route/flag"},
		{"stale flag", []publicRouteArgvValueRow{{route: "leaf", flag: "--n", kind: publicRouteArgvValueStaticRow, reason: "r"}}, "no longer declares a closed value set"},
		{"stale route", []publicRouteArgvValueRow{{route: "gone", flag: "--ui", kind: publicRouteArgvValueStaticRow, reason: "r"}}, "no longer declares a closed value set"},
		{"unechoed without echo", []publicRouteArgvValueRow{{route: "leaf", flag: "--ui", kind: publicRouteArgvValueUnechoedRow, reason: "r"}}, "needs the reason text"},
		{"static with echo", []publicRouteArgvValueRow{{route: "leaf", flag: "--ui", kind: publicRouteArgvValueStaticRow, reason: "r", echo: "e"}}, "carries no echo text"},
		{"unknown kind", []publicRouteArgvValueRow{{route: "leaf", flag: "--ui", kind: 99, reason: "r"}}, "unknown kind"},
	} {
		if problems := checkPublicRouteArgvValueRows(test.rows, nodes); !strings.Contains(strings.Join(problems, "\n"), test.want) {
			t.Errorf("%s: problems %q, want one containing %q", test.name, problems, test.want)
		}
	}

	probe := func(code, stdout int, stderr, reason string) publicRouteArgvProbe {
		return publicRouteArgvProbe{route: "leaf", form: publicRouteArgvFormValue, flag: "--ui", argv: []string{"leaf", "--ui", publicRouteArgvGuardValue},
			result: publicRouteArgvChildResult{Code: code, StdoutBytes: stdout, Stderr: stderr, Error: reason}}
	}
	unechoed := &publicRouteArgvValueRow{route: "leaf", flag: "--ui", kind: publicRouteArgvValueUnechoedRow, reason: "r", echo: "--ui must be a or b"}
	for _, test := range []struct {
		name  string
		probe publicRouteArgvProbe
		row   *publicRouteArgvValueRow
		want  string
	}{
		{"pass", probe(2, 0, "Usage:\n", `bad --ui "zz-value-guard"`), nil, ""},
		{"exit 1", probe(1, 0, "", `bad --ui "zz-value-guard"`), nil, "exited 1"},
		{"stdout", probe(2, 3, "", `bad --ui "zz-value-guard"`), nil, "stdout bytes"},
		{"unrelated usage error", probe(2, 0, "Usage:\n", "leaf requires --text"), nil, "does not name"},
		{"unechoed pass", probe(2, 0, "", "leaf --ui must be a or b"), unechoed, ""},
		{"unechoed unrelated", probe(2, 0, "", "leaf requires --text"), unechoed, "does not carry"},
		{"unechoed stale", probe(2, 0, "", `leaf --ui must be a or b, got "zz-value-guard"`), unechoed, "row is stale"},
		{"no result", publicRouteArgvProbe{route: "leaf", flag: "--ui", argv: []string{"leaf"}, failure: "timed out"}, nil, "did not report"},
	} {
		got := publicRouteArgvValueVerdict(test.probe, test.row)
		if (test.want == "") != (got == "") || !strings.Contains(got, test.want) {
			t.Errorf("%s: verdict %q, want %q", test.name, got, test.want)
		}
	}
}
