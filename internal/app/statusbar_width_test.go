package app

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// statusbar_width_test.go covers the width budgets `status-format[0]` hands its
// two HUD segments. The budgets are tmux formats now, not literals, so the
// tests evaluate the emitted formats with a miniature interpreter (below) whose
// semantics were measured against a real tmux server rather than assumed.

// evalTmuxFormat evaluates the small subset of the tmux format language the
// statusbar aux line uses, for a client of the given width.
//
// It is deliberately a model of tmux 3.6's ACTUAL behaviour, including the trap
// this Phase walked into. Measured on a throwaway tmux 3.6 server:
//
//	#{>:191,80}       -> 0     bare comparisons are STRING comparisons
//	#{>:9,10}         -> 1     ... which is why "191" loses to "80"
//	#{e|>:191,80}     -> 1     the `e|` modifier is the numeric one
//	#{e|-:191,80}     -> 111
//
// so a spelling that silently never fires fails here instead of shipping.
func evalTmuxFormat(t *testing.T, format string, clientWidth int) string {
	t.Helper()

	var b strings.Builder
	for i := 0; i < len(format); {
		if strings.HasPrefix(format[i:], "#{") {
			body, size := tmuxFormatBody(t, format[i:])
			b.WriteString(evalTmuxFormatBody(t, body, clientWidth))
			i += size
			continue
		}
		b.WriteByte(format[i])
		i++
	}
	return b.String()
}

// tmuxFormatBody returns the body of the `#{...}` starting at s[0], and the
// number of bytes the whole construct occupies. Nested `#{` are balanced.
func tmuxFormatBody(t *testing.T, s string) (string, int) {
	t.Helper()

	depth := 1
	for i := 2; i < len(s); i++ {
		switch {
		case strings.HasPrefix(s[i:], "#{"):
			depth++
			i++
		case s[i] == '}':
			depth--
			if depth == 0 {
				return s[2:i], i + 1
			}
		}
	}
	t.Fatalf("unbalanced tmux format: %q", s)
	return "", 0
}

// splitTmuxFormatArgs splits on top-level commas, ignoring commas nested inside
// a `#{...}`.
func splitTmuxFormatArgs(t *testing.T, s string) []string {
	t.Helper()

	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch {
		case strings.HasPrefix(s[i:], "#{"):
			depth++
			i++
		case s[i] == '}':
			depth--
		case s[i] == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// tmuxNumericOps are the `e|` arithmetic and comparison operators the aux line
// uses, keyed by the operator token.
var tmuxNumericOps = map[string]func(a, b int) int{
	"-": func(a, b int) int { return a - b },
	"/": func(a, b int) int { return a / b },
	"<": func(a, b int) int {
		if a < b {
			return 1
		}
		return 0
	},
}

func evalTmuxFormatBody(t *testing.T, body string, clientWidth int) string {
	t.Helper()

	switch {
	case body == "client_width":
		return strconv.Itoa(clientWidth)
	case strings.HasPrefix(body, "?"):
		args := splitTmuxFormatArgs(t, body[1:])
		if len(args) != 3 {
			t.Fatalf("#{?...} wants 3 arguments, got %d in %q", len(args), body)
		}
		// tmux treats an empty string and "0" as false.
		if cond := evalTmuxFormat(t, args[0], clientWidth); cond == "" || cond == "0" {
			return evalTmuxFormat(t, args[2], clientWidth)
		}
		return evalTmuxFormat(t, args[1], clientWidth)
	case strings.HasPrefix(body, "e|"), strings.HasPrefix(body, "<:"):
		// A bare `#{<:a,b}` is tmux's STRING comparison; only the `e|` form is
		// numeric. Modelling both is what lets the trap be asserted.
		numeric := strings.HasPrefix(body, "e|")
		expr := strings.TrimPrefix(body, "e|")
		op, rest, ok := strings.Cut(expr, ":")
		if !ok {
			t.Fatalf("malformed operator in %q", body)
		}
		args := splitTmuxFormatArgs(t, rest)
		if len(args) != 2 {
			t.Fatalf("operator %q wants 2 arguments, got %d in %q", op, len(args), body)
		}
		left := evalTmuxFormat(t, args[0], clientWidth)
		right := evalTmuxFormat(t, args[1], clientWidth)
		if !numeric {
			if left < right {
				return "1"
			}
			return "0"
		}
		fn, known := tmuxNumericOps[op]
		if !known {
			t.Fatalf("the statusbar aux line grew the tmux operator %q, which this test cannot evaluate", op)
		}
		return strconv.Itoa(fn(mustAtoi(t, left), mustAtoi(t, right)))
	}
	t.Fatalf("the statusbar aux line grew a tmux format this test cannot evaluate: %q", body)
	return ""
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()

	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatalf("not a number: %q", s)
	}
	return n
}

// TestEvalTmuxFormatModelsTheStringComparisonTrap proves the interpreter above
// is not a rubber stamp: it reproduces the measured tmux behaviour that a BARE
// `#{<:a,b}` compares as strings, so a threshold written that way never fires
// on a wide terminal.
func TestEvalTmuxFormatModelsTheStringComparisonTrap(t *testing.T) {
	t.Parallel()

	bare := strings.ReplaceAll(statusbarNotifyBudgetFormat(), "#{e|<:", "#{<:")
	// "90" > "40", "160" and "220" as strings, so the string spelling walks every
	// false branch on a 90-column client and reserves 80 of its 90 cells for
	// notify — the exact over-commit this derivation exists to prevent.
	if got := evalTmuxFormat(t, bare, 90); got != "80" {
		t.Fatalf("string-comparison spelling should misfire at 90, got %q", got)
	}
	if got := evalTmuxFormat(t, statusbarNotifyBudgetFormat(), 90); got != "20" {
		t.Fatalf("numeric-comparison spelling should reserve notify's floor on a 90-column row, got %q", got)
	}
	// And the reported width resolves to the reservation, not the design cap.
	if got := evalTmuxFormat(t, statusbarNotifyBudgetFormat(), 191); got != "51" {
		t.Fatalf("numeric-comparison spelling should reserve 191-140 at 191, got %q", got)
	}
}

// statusbarBudgetCases are the exact values a tmux 3.6 server passed to a `#()`
// job when the emitted formats were rendered for a client of each width. The
// expected columns are measured, not derived from the code under test.
var statusbarBudgetCases = []struct{ clientWidth, notify, usage int }{
	{clientWidth: 400, notify: 80, usage: 320},
	{clientWidth: 240, notify: 80, usage: 160},
	{clientWidth: 220, notify: 80, usage: 140},
	{clientWidth: 219, notify: 79, usage: 140},
	{clientWidth: 200, notify: 60, usage: 140},
	{clientWidth: 191, notify: 51, usage: 140},
	{clientWidth: 180, notify: 40, usage: 140},
	{clientWidth: 160, notify: 20, usage: 140},
	{clientWidth: 159, notify: 20, usage: 139},
	{clientWidth: 144, notify: 20, usage: 124},
	{clientWidth: 140, notify: 20, usage: 120},
	{clientWidth: 120, notify: 20, usage: 100},
	{clientWidth: 100, notify: 20, usage: 80},
	{clientWidth: 80, notify: 20, usage: 60},
	{clientWidth: 60, notify: 20, usage: 40},
	{clientWidth: 40, notify: 20, usage: 20},
	{clientWidth: 39, notify: 19, usage: 20},
	{clientWidth: 20, notify: 10, usage: 10},
}

// TestStatusbarSegmentBudgetsMatchMeasuredTmuxExpansion pins the derivation
// itself against the measured expansion.
func TestStatusbarSegmentBudgetsMatchMeasuredTmuxExpansion(t *testing.T) {
	t.Parallel()

	notifyFormat := statusbarNotifyBudgetFormat()
	usageFormat := statusbarUsageBudgetFormat()
	for _, tc := range statusbarBudgetCases {
		notify := mustAtoi(t, evalTmuxFormat(t, notifyFormat, tc.clientWidth))
		usage := mustAtoi(t, evalTmuxFormat(t, usageFormat, tc.clientWidth))
		if notify != tc.notify || usage != tc.usage {
			t.Errorf("client width %d: notify=%d usage=%d, want notify=%d usage=%d",
				tc.clientWidth, notify, usage, tc.notify, tc.usage)
		}
	}
}

// TestStatusbarSegmentBudgetsSplitTheRowExactly is the invariant the whole
// design rests on. tmux draws the row's left section in full and clips the
// right one from its left edge, so the moment the two budgets add up to more
// than the row, the usage segment loses its leading provider blocks outright
// instead of degrading through its own tier ladder.
func TestStatusbarSegmentBudgetsSplitTheRowExactly(t *testing.T) {
	t.Parallel()

	notifyFormat := statusbarNotifyBudgetFormat()
	usageFormat := statusbarUsageBudgetFormat()
	for clientWidth := 2; clientWidth <= 400; clientWidth++ {
		notify := mustAtoi(t, evalTmuxFormat(t, notifyFormat, clientWidth))
		usage := mustAtoi(t, evalTmuxFormat(t, usageFormat, clientWidth))
		if notify+usage != clientWidth {
			t.Fatalf("client width %d: notify=%d + usage=%d = %d, want the row exactly",
				clientWidth, notify, usage, notify+usage)
		}
		if notify < 1 || usage < 1 {
			t.Fatalf("client width %d: a segment was budgeted out of existence (notify=%d usage=%d)",
				clientWidth, notify, usage)
		}
		// `--max-width 0` means "no truncation" to both segment renderers, so a
		// budget of 0 would be read as "unbounded" and overflow the row.
		if usage == 0 {
			t.Fatalf("client width %d: usage budget 0 would be read as unbounded", clientWidth)
		}
		// On any row that can afford it, notify still reaches exactly its
		// historical cap, so notify's bytes are unchanged on a large terminal.
		if clientWidth >= statusbarNotifySurplusCapWidth && notify != statusbarNotifyMaxWidth {
			t.Fatalf("client width %d: notify budget = %d, want the unchanged %d",
				clientWidth, notify, statusbarNotifyMaxWidth)
		}
		if notify > statusbarNotifyMaxWidth {
			t.Fatalf("client width %d: notify budget = %d, above its design cap", clientWidth, notify)
		}
		// And notify is never squeezed below its reservation on a row that can
		// host it, so the usage floor can never erase the notify segment.
		if clientWidth >= statusbarEvenSplitMaxWidth && notify < statusbarNotifyMinWidth {
			t.Fatalf("client width %d: notify budget = %d, below its %d reservation",
				clientWidth, notify, statusbarNotifyMinWidth)
		}
	}
}

// TestStatusbarUsageBudgetNeverRegressesBelowItsHistoricalWidth is the
// corrective's regression guard. PR #624 derived both budgets from the client
// but reserved notify's design CAP, so every client narrower than 200 columns
// ended up with less usage budget than the hardcoded 120 it replaced — the
// reported 191-column terminal dropped from 120 to 111 and lost Claude's weekly
// bar. usage must never again come out below the constant it used to have.
//
// The guard is stated as `min(client_width - notifyMin, usageMin)` and not a
// flat 120 because of a physical conflict this change cannot dissolve: a
// 120-column row cannot hold 120 usage cells AND any notify cells, and a notify
// budget of 0 is read by the renderer as "unbounded" rather than "nothing", so
// it would overflow the row and erase usage entirely. Below 140 columns notify's
// 20-cell floor wins and usage gets the rest.
func TestStatusbarUsageBudgetNeverRegressesBelowItsHistoricalWidth(t *testing.T) {
	t.Parallel()

	usageFormat := statusbarUsageBudgetFormat()
	for clientWidth := statusbarEvenSplitMaxWidth; clientWidth <= 400; clientWidth++ {
		usage := mustAtoi(t, evalTmuxFormat(t, usageFormat, clientWidth))
		want := min(clientWidth-statusbarNotifyMinWidth, statusbarUsageMinWidth)
		if usage < want {
			t.Fatalf("client width %d: usage budget %d, want at least %d", clientWidth, usage, want)
		}
	}
	// Spelled out at the widths the reports and the sweep used, so a reviewer
	// reads the outcome instead of re-deriving it. The `vs 120` column is the
	// pre-#624 hardcoded constant.
	for _, tc := range []struct{ clientWidth, usage int }{
		{clientWidth: 80, usage: 60},
		{clientWidth: 100, usage: 80},
		{clientWidth: 120, usage: 100},
		{clientWidth: 140, usage: 120},
		{clientWidth: 160, usage: 140},
		{clientWidth: 180, usage: 140},
		{clientWidth: 191, usage: 140},
		{clientWidth: 200, usage: 140},
		{clientWidth: 240, usage: 160},
	} {
		if got := mustAtoi(t, evalTmuxFormat(t, usageFormat, tc.clientWidth)); got != tc.usage {
			t.Errorf("client width %d: usage budget %d, want %d", tc.clientWidth, got, tc.usage)
		}
	}
}

// statusbarReportedClientWidth is the terminal the defect was reported from, and
// statusbarClaudeWeeklyBarBudget is the measured budget at which the installed
// provider shape's second-bar tier fits (see
// internal/app/usagecmd/status_golden_test.go, which pins the renderer side of
// the same number against a fixture).
const (
	statusbarReportedClientWidth   = 191
	statusbarClaudeWeeklyBarBudget = 124
)

// TestStatusbarUsageBudgetReachesTheClaudeWeeklyBarOnTheReportedTerminal is the
// product acceptance. PR #624 met its stated mechanism — both budgets derive
// from `#{client_width}` — while inverting its own goal: on the 191-column
// terminal the track exists for, the Claude weekly bar was still missing
// afterwards, because 191-80 = 111 is below the 124 the tier needs.
func TestStatusbarUsageBudgetReachesTheClaudeWeeklyBarOnTheReportedTerminal(t *testing.T) {
	t.Parallel()

	usage := mustAtoi(t, evalTmuxFormat(t, statusbarUsageBudgetFormat(), statusbarReportedClientWidth))
	if usage < statusbarClaudeWeeklyBarBudget {
		t.Fatalf("a %d-column client gets %d usage cells, below the %d Claude's weekly bar needs",
			statusbarReportedClientWidth, usage, statusbarClaudeWeeklyBarBudget)
	}
	// And the first width that clears the threshold is low enough to be an
	// ordinary terminal, not only a very wide one.
	first := 0
	for clientWidth := 2; clientWidth <= 400; clientWidth++ {
		if mustAtoi(t, evalTmuxFormat(t, statusbarUsageBudgetFormat(), clientWidth)) >= statusbarClaudeWeeklyBarBudget {
			first = clientWidth
			break
		}
	}
	// 144: the reservation is 20 up to 160, so usage first reaches 124 at 144.
	if first != 144 {
		t.Fatalf("the Claude weekly bar first fits on a %d-column client; the derivation moved", first)
	}
}

// TestStatusbarUsageBudgetGrowsWithTheClient states the point of the Phase: a
// wider terminal must hand the usage segment more cells, never the same
// constant. Monotonicity also rules out a derivation that happens to be right
// at the measured widths and wrong between them.
func TestStatusbarUsageBudgetGrowsWithTheClient(t *testing.T) {
	t.Parallel()

	usageFormat := statusbarUsageBudgetFormat()
	previous := 0
	for clientWidth := 2; clientWidth <= 400; clientWidth++ {
		usage := mustAtoi(t, evalTmuxFormat(t, usageFormat, clientWidth))
		if usage < previous {
			t.Fatalf("client width %d: usage budget %d shrank from %d", clientWidth, usage, previous)
		}
		previous = usage
	}
	// The old hardcoded 120 is passed by an ordinary terminal instead of being
	// the ceiling for every terminal.
	if got := mustAtoi(t, evalTmuxFormat(t, usageFormat, 140)); got != statusbarUsageMinWidth {
		t.Fatalf("a 140-column client should reach the old constant exactly, got %d", got)
	}
	if got := mustAtoi(t, evalTmuxFormat(t, usageFormat, 400)); got <= statusbarUsageMinWidth {
		t.Fatalf("a 400-column client is still capped at the old constant: %d", got)
	}
}

// TestStatusbarNotifyBudgetFallsBackToItsDesignCap pins the fail-safe direction
// of the comparisons. tmux treats an unevaluable condition as false, so the
// innermost else branch is what a tmux that cannot evaluate `e|<` would use —
// and it must be the historical literal 80, not half a row and not the floor.
func TestStatusbarNotifyBudgetFallsBackToItsDesignCap(t *testing.T) {
	t.Parallel()

	format := statusbarNotifyBudgetFormat()
	for depth := 0; ; depth++ {
		body, size := tmuxFormatBody(t, format)
		if size != len(format) {
			t.Fatalf("the notify budget is no longer a single #{...} construct: %q", format)
		}
		if !strings.HasPrefix(body, "?") {
			t.Fatalf("the notify budget is no longer a conditional: %q", body)
		}
		args := splitTmuxFormatArgs(t, body[1:])
		if len(args) != 3 {
			t.Fatalf("conditional arity changed: %q", body)
		}
		if !strings.HasPrefix(args[0], "#{e|<:") {
			t.Fatalf("depth %d condition %q is not the fail-safe numeric `<` form", depth, args[0])
		}
		if !strings.HasPrefix(args[2], "#{?") {
			if args[2] != strconv.Itoa(statusbarNotifyMaxWidth) {
				t.Fatalf("innermost false branch = %q, want the literal %d so an unevaluable condition degrades to today's cap",
					args[2], statusbarNotifyMaxWidth)
			}
			return
		}
		format = args[2]
		if depth > 8 {
			t.Fatalf("the notify budget conditional nests deeper than expected: %q", statusbarNotifyBudgetFormat())
		}
	}
}

// statusbarSegmentBudget extracts the `--max-width` argument the aux line hands
// one of its two segments.
var statusbarSegmentBudget = regexp.MustCompile(`internal status (notify|usage) --max-width (.*?)\)#\[norange\]`)

func auxLineBudgets(t *testing.T, line string) map[string]string {
	t.Helper()

	out := map[string]string{}
	for _, match := range statusbarSegmentBudget.FindAllStringSubmatch(line, -1) {
		out[match[1]] = match[2]
	}
	if len(out) != 2 {
		t.Fatalf("expected a notify and a usage budget in %q, got %#v", line, out)
	}
	return out
}

// TestStatusbarAuxLineBudgetsDeriveFromTheClient is the acceptance assertion
// that no segment-specific width constant survives in the generated config:
// both budgets are client-derived formats, in both the standalone and app
// spellings of the row.
func TestStatusbarAuxLineBudgetsDeriveFromTheClient(t *testing.T) {
	t.Parallel()

	for _, autosave := range []bool{false, true} {
		budgets := auxLineBudgets(t, statusbarAuxLineFormat("'/usr/bin/projmux'", autosave))
		if want := statusbarNotifyBudgetFormat(); budgets["notify"] != want {
			t.Fatalf("notify budget = %q, want %q", budgets["notify"], want)
		}
		if want := statusbarUsageBudgetFormat(); budgets["usage"] != want {
			t.Fatalf("usage budget = %q, want %q", budgets["usage"], want)
		}
		for segment, budget := range budgets {
			if _, err := strconv.Atoi(strings.TrimSpace(budget)); err == nil {
				t.Errorf("%s budget %q is a literal cell count, not a client-derived format", segment, budget)
			}
			if !strings.Contains(budget, statusbarClientWidthFormat) {
				t.Errorf("%s budget %q does not read the client width", segment, budget)
			}
		}
	}
}
