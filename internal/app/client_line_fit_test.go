package app

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

// argvFailureReason is shaped like the reason a UI new Window create shows
// when tmux refuses the Agent's split: commandError embeds the whole argv, and
// tmux's own cause is at the very end. It is 789 bytes, as the observed one
// was.
func argvFailureReason(t *testing.T) string {
	t.Helper()
	const (
		front = `split tmux pane "%9": tmux -S /tmp/x/t/tmux-1000/projmux split-window -d -P -F #{pane_id} -h -t %9 ` +
			`-c /home/p1 /bin/projmux internal supervise --pane-uid pane-0f3c --window-uid win-7a21 --project-uid prj-19be ` +
			`-- /bin/sh -lc export PATH=`
		back = ` && exec '/pbin/claude': exit status 1: injected: split-window refused`
	)
	path := strings.Repeat("/opt/tool/bin:", (789-len(front)-len(back))/len("/opt/tool/bin:"))
	path += strings.Repeat("x", 789-len(front)-len(back)-len(path))
	reason := front + path + back
	if len(reason) != 789 {
		t.Fatalf("fixture reason is %d bytes, want 789", len(reason))
	}
	return reason
}

// assertFittedClientLine checks the rules fitClientLine states, whatever the
// input: never wider than the width; a line that fits is unchanged; otherwise
// the head is whole, followed by a front and an end of the reason around one
// elision, balanced and at most one cell short. A width narrower than the
// head keeps the start of the head.
func assertFittedClientLine(t *testing.T, head, reason string, width int, got string) {
	t.Helper()
	cells := projmuxpicker.VisibleLen
	if cells(got) > width {
		t.Fatalf("fitClientLine(%q, %q, %d) = %q is %d cells wide", head, reason, width, got, cells(got))
	}
	if cells(head+reason) <= width {
		if got != head+reason {
			t.Fatalf("a line that fits changed: %q, want %q", got, head+reason)
		}
		return
	}
	if strings.Count(got, clientLineElision) != 1 {
		t.Fatalf("clipped line %q, want exactly one elision", got)
	}
	if cells(head)+1 > width {
		if !strings.HasPrefix(head, strings.TrimSuffix(got, clientLineElision)) {
			t.Fatalf("line %q narrower than its head does not keep the head's start %q", got, head)
		}
		return
	}
	body, ok := strings.CutPrefix(got, head)
	if !ok {
		t.Fatalf("line %q does not lead with the whole head %q", got, head)
	}
	front, back, _ := strings.Cut(body, clientLineElision)
	if !strings.HasPrefix(reason, front) || !strings.HasSuffix(reason, back) {
		t.Fatalf("line %q is not the reason's front %q and end %q", got, front, back)
	}
	if cells(got) < width-1 {
		t.Fatalf("line %q is %d cells, want at most one cell short of %d", got, cells(got), width)
	}
	if diff := cells(front) - cells(back); diff < -2 || diff > 2 {
		t.Fatalf("line %q keeps %d cells of the front and %d of the end, want them balanced", got, cells(front), cells(back))
	}
}

// TestFitClientLineKeepsTheOutcomeAndBothEndsOfTheReason is the pure
// projection of owner ruling T1-2.
func TestFitClientLineKeepsTheOutcomeAndBothEndsOfTheReason(t *testing.T) {
	t.Parallel()

	argv := argvFailureReason(t)
	const (
		argvFront = `split tmux pane "%9"`
		argvCause = "injected: split-window refused"
	)
	for _, tt := range []struct {
		name      string
		head      string
		reason    string
		width     int
		want      string
		wantFront string
		wantCause string
	}{
		{name: "a short reason that fits is unchanged", head: windowNotCreatedHead,
			reason: "could not open the launch picker: injected", width: 120,
			want: windowNotCreatedHead + "could not open the launch picker: injected"},
		{name: "a line exactly as wide as the client is unchanged", head: "ab: ", reason: "cdef", width: 8, want: "ab: cdef"},
		{name: "the argv reason on a 120-cell client", head: windowNotCreatedHead, reason: argv, width: 120,
			wantFront: argvFront, wantCause: argvCause},
		{name: "the argv reason on an 80-cell client", head: windowNotCreatedHead, reason: argv, width: 80},
		{name: "the argv reason after the fresh-open head on 120 cells", head: keptOriginShellHead, reason: argv, width: 120,
			wantFront: argvFront, wantCause: argvCause},
		{name: "the argv reason alone on 80 cells", reason: argv, width: 80, wantFront: argvFront, wantCause: argvCause},
		{name: "no width is 80 cells", head: windowNotCreatedHead, reason: argv, width: 0,
			want: fitClientLine(windowNotCreatedHead, argv, 80)},
		{name: "a negative width is 80 cells", head: windowNotCreatedHead, reason: argv, width: -3,
			want: fitClientLine(windowNotCreatedHead, argv, 80)},
		{name: "a client narrower than the head keeps the head's start", head: windowNotCreatedHead, reason: argv, width: 12,
			want: "projmux Cre…"},
		{name: "a one-cell client shows the elision", head: windowNotCreatedHead, reason: argv, width: 1, want: "…"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := fitClientLine(tt.head, tt.reason, tt.width)
			width := tt.width
			if width <= 0 {
				width = defaultClientLineWidth
			}
			assertFittedClientLine(t, tt.head, tt.reason, width, got)
			if tt.want != "" && got != tt.want {
				t.Fatalf("fitClientLine() = %q, want %q", got, tt.want)
			}
			if !strings.Contains(got, tt.wantFront) || !strings.HasSuffix(got, tt.wantCause) {
				t.Fatalf("fitClientLine() = %q, want the failed step %q and the cause %q", got, tt.wantFront, tt.wantCause)
			}
		})
	}

	// Every width, for reasons whose runes are one cell, two cells, and mixed:
	// a two-cell rune that does not fit is dropped, never split and never
	// allowed to push the line past the client.
	reasons := []string{
		argv,
		strings.Repeat("새 창을 열 수 없습니다 ", 30),
		strings.Repeat("無法建立視窗", 40),
		`split tmux pane "%9": 프로젝트 /home/사용자/작업 에서 tmux 가 거절했습니다: exit status 1: 分割できません`,
	}
	for _, head := range []string{"", keptOriginShellHead, windowNotCreatedHead, "창 없음: "} {
		for _, reason := range reasons {
			for width := 1; width <= 160; width++ {
				assertFittedClientLine(t, head, reason, width, fitClientLine(head, reason, width))
			}
		}
	}
}

// failureLineWidthAnswers are the ways the pressing client's width can come
// back, and the width the line must then fit.
var failureLineWidthAnswers = []struct {
	name   string
	answer string
	err    error
	want   int
}{
	{name: "80-cell client", answer: "80\n", want: 80},
	{name: "120-cell client", answer: "120\n", want: 120},
	{name: "width read fails", err: errors.New("injected: can't find client"), want: defaultClientLineWidth},
	{name: "width is not a number", answer: "wide\n", want: defaultClientLineWidth},
	{name: "width is empty", answer: "\n", want: defaultClientLineWidth},
	{name: "width is zero", answer: "0\n", want: defaultClientLineWidth},
}

// TestWindowCreateFailureLineLeadsWithTheOutcomeAndFitsTheClient is contract
// C-1 on both not-created branches of the UI new Window: a question that could
// not be asked, and an Agent answer whose create did not commit. The line says
// no Window was created before it says why, fits the pressing client, and
// costs exactly one tmux call more than the line itself -- the width read, on
// that exact client.
func TestWindowCreateFailureLineLeadsWithTheOutcomeAndFitsTheClient(t *testing.T) {
	argv := argvFailureReason(t)
	claude := launchChoice{intent: agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeClaude, placement: "right"}}
	for _, branch := range []struct {
		name       string
		choice     launchChoice
		createErr  error
		wantReason string
	}{
		{name: "question not asked", choice: launchChoice{problem: argv}, wantReason: argv},
		{name: "Agent answer not committed", choice: claude, createErr: errors.New(argv),
			wantReason: argv + ": injected detail"},
	} {
		for _, width := range failureLineWidthAnswers {
			t.Run(branch.name+"/"+width.name, func(t *testing.T) {
				route := newOrderedWindowCreateRoute(t, branch.choice, branch.createErr, true)
				route.runner.outputs[launchDefaultClientWidthKey] = width.answer
				if width.err != nil {
					route.runner.errors = map[string]error{launchDefaultClientWidthKey: width.err}
				}

				route.run(t)

				want := notCreatedLine(branch.wantReason, width.want)
				assertFittedClientLine(t, windowNotCreatedHead, branch.wantReason, width.want, want)
				if !strings.HasPrefix(want, "projmux Create Window failed; no Window was created: ") {
					t.Fatalf("line %q does not lead with the outcome", want)
				}
				wantCalls := [][]string{
					{"display-message", "-p", "-c", launchDefaultClient, "-F", "#{client_width}"},
					{"display-message", "-c", launchDefaultClient, "-d", "10000", tmuxLiteralMessage(want)},
				}
				var calls [][]string
				for _, call := range route.runner.calls {
					calls = append(calls, call.args)
				}
				if !equalArgvs(calls, wantCalls) {
					t.Fatalf("tmux calls = %q, want the width read and the one line %q", calls, wantCalls)
				}
			})
		}
	}
}

// TestFreshOpenFailureLineLeadsWithTheOutcomeAndFitsTheClient is the fresh
// Project open's half of C-1. A fill that kept the shell Pane says so first;
// every other line the fill returns is fitted whole. Both fit the pressing
// client, whose width is read once, on the same exact app socket the line is
// shown through.
func TestFreshOpenFailureLineLeadsWithTheOutcomeAndFitsTheClient(t *testing.T) {
	t.Parallel()

	argv := argvFailureReason(t)
	claude := launchChoice{intent: agentPaneIntent{producer: canonicalProducerProviderPicker, provider: aiModeClaude, placement: "right"}}
	bothStay := "projmux opened the Agent, but could not remove the shell Pane %31: " + argv + "; both Panes stay"
	for _, line := range []struct {
		name       string
		choice     launchChoice
		applied    launchDefaultResult
		wantHead   string
		wantReason string
		wantEnd    string
	}{
		{name: "question not asked keeps the shell", choice: launchChoice{problem: argv},
			wantHead: keptOriginShellHead, wantReason: argv, wantEnd: "refused"},
		{name: "refused fill keeps the shell", choice: claude, applied: launchDefaultResult{problem: keptOriginShellLine(argv)},
			wantHead: keptOriginShellHead, wantReason: argv, wantEnd: "refused"},
		{name: "a line without that head is fitted whole", choice: claude, applied: launchDefaultResult{problem: bothStay},
			wantReason: bothStay, wantEnd: "; both Panes stay"},
	} {
		for _, width := range failureLineWidthAnswers {
			t.Run(line.name+"/"+width.name, func(t *testing.T) {
				t.Parallel()
				open := newFreshAskOpen(t, false, freshSidebarEnv())
				open.choice, open.applied = line.choice, line.applied
				open.runner.width, open.runner.widthErr = width.answer, width.err

				open.start(t, freshLaunchPressedPane)

				want := fitClientLine(line.wantHead, line.wantReason, width.want)
				assertFittedClientLine(t, line.wantHead, line.wantReason, width.want, want)
				if !strings.HasPrefix(want, line.wantHead) || !strings.HasSuffix(want, line.wantEnd) {
					t.Fatalf("line %q, want it led by %q and ending in %q", want, line.wantHead, line.wantEnd)
				}
				if !slices.Equal(open.runner.lines, []string{tmuxLiteralMessage(want)}) {
					t.Fatalf("client lines = %q, want %q", open.runner.lines, []string{tmuxLiteralMessage(want)})
				}
				if len(open.runner.displays) != 1 || len(open.runner.widthReads) != 1 {
					t.Fatalf("displays = %q, width reads = %q, want one of each", open.runner.displays, open.runner.widthReads)
				}
				socket := open.runner.displays[0][:2]
				if socket[0] != "-L" && socket[0] != "-S" {
					t.Fatalf("line %q is not addressed at the exact app socket", open.runner.displays[0])
				}
				wantRead := append(slices.Clone(socket), "display-message", "-p", "-c", freshLaunchDefaultClient, "-F", "#{client_width}")
				if !slices.Equal(open.runner.widthReads[0], wantRead) {
					t.Fatalf("width read = %q, want %q", open.runner.widthReads[0], wantRead)
				}
			})
		}
	}
}
