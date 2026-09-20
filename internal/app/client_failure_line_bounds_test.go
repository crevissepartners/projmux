package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

// Contract C-1 for the whole of the interactive tmux adapter layer: every
// variable line it puts on a client is fitted to that client, and what the
// line was for survives the fit.
//
// The file is named for the failure lines it was written for, and the tests
// below keep those names, but the set is wider than that now: the producers
// here are *every* non-transport row of committedResultDisplaySites(), success
// and notice lines included. A committed split's start notice, a committed
// delete's summary and a committed pane-menu split's notice are as unbounded as
// any refusal -- the resume seam writes one holder per Agent -- and tmux clips
// all of them the same way. Each is driven through its own route, because a
// projection that fits proves nothing about a producer that never calls it. The
// shared Window-intent row is driven once per intent it carries: one line there
// is the failure of a create, two renames and a delete.
//
// committed_result_test.go asserts that this list covers that closed set, so a
// row with no surface here fails there -- and deleting this file stops that
// file compiling rather than quietly retiring the whole contract.
//
// Two line shapes are checked, not one. Most carry tmux's own stderr at the end
// of their reason, so fitting keeps both of its ends. The kept-split and
// unshown-Window lines carry their cause at the front with a disclosure behind
// it, so the disclosure is what yields: keeping it instead would cut away the
// only sentence that says what failed.

// clientBoundsClient is the exact client every surface below addresses.
const clientBoundsClient = "/dev/pts/9"

// clientBoundsNotice is a resume-seam disclosure of the kind a committed split
// or Window create rides behind its cause.
const clientBoundsNotice = "launch-values-ambiguous: the resume seam could not tell which launch values to inherit; persona-unavailable"

// clientBoundsWidths are the widths every surface is judged at: the
// conventional terminal, and a wide one.
var clientBoundsWidths = []int{80, 120}

// clientBoundsWidthKey is the one width read a fitted failure line costs.
var clientBoundsWidthKey = recordedTmuxCallKey("tmux", "display-message", "-p", "-c", clientBoundsClient, "-F", clientLineWidthFormat)

// clientLineWidthRead reports whether an argv is that width read rather than
// the line itself.
func clientLineWidthRead(args []string) bool {
	return len(args) > 0 && args[0] == "display-message" &&
		slices.Contains(args, "-p") && slices.Contains(args, clientLineWidthFormat)
}

// clientBoundsCalls splits a recorded runner's calls into the width reads and
// the lines shown, counting only what was addressed at the exact client.
func clientBoundsCalls(calls []recordedTmuxCall, client string) (reads int, lines []string) {
	for _, call := range calls {
		if len(call.args) == 0 || call.args[0] != "display-message" || !containsTmuxArgPair(call.args, "-c", client) {
			continue
		}
		if clientLineWidthRead(call.args) {
			reads++
			continue
		}
		lines = append(lines, call.args[len(call.args)-1])
	}
	return reads, lines
}

// clientBoundsRendered undoes the escaping tmuxLiteralMessage applies, so a
// line is measured in the cells tmux paints rather than the bytes it is given.
func clientBoundsRendered(line string, escaped bool) string {
	if !escaped {
		return line
	}
	return strings.NewReplacer("##", "#", "%%", "%").Replace(line)
}

// clientBoundsRunner is a recording runner that answers the client-width read
// with width.
func clientBoundsRunner(width int) *recordingTmuxRunner {
	return &recordingTmuxRunner{
		outputs: map[string]string{clientBoundsWidthKey: strconv.Itoa(width) + "\n"},
		errors:  map[string]error{},
	}
}

// clientBoundsCreator is a split-UI creator that reports one committed Pane and
// writes one disclosure, or refuses with one reason.
type clientBoundsCreator struct {
	created createdPaneRuntime
	err     error
	notice  string
}

func (c *clientBoundsCreator) createFromIntent(_ agentPaneIntent, _, stderr io.Writer) (createdPaneRuntime, error) {
	if c.notice != "" {
		_, _ = io.WriteString(stderr, c.notice)
	}
	return c.created, c.err
}

// clientBoundsSplitCommand wires one split-funnel run onto the recording runner
// and the given creator.
func clientBoundsSplitCommand(t *testing.T, runner *recordingTmuxRunner, creator *clientBoundsCreator) *aiCommand {
	t.Helper()
	home := t.TempDir()
	cmd := testAICommand(home)
	cmd.panes = creator
	cmd.lookupEnv = func(key string) string {
		switch key {
		case "HOME":
			return home
		case canonicalCreateTargetClientEnv:
			return clientBoundsClient
		default:
			return ""
		}
	}
	cmd.runCommand = func(ctx context.Context, name string, args ...string) error {
		_, err := runner.Run(ctx, name, args...)
		return err
	}
	cmd.readCommand = runner.Run
	return cmd
}

// clientBoundsSurface is one client-line producer and what its line has to
// keep.
type clientBoundsSurface struct {
	name string
	// ledgerFile and ledgerSnippet name the committedResultDisplaySites() row
	// this surface drives, byte for byte as that row spells them. They are what
	// committed_result_test.go maps the closed set onto: a row no surface names
	// has no width test, and a surface that names no row is measuring something
	// the ledger does not know about.
	ledgerFile    string
	ledgerSnippet string
	// head leads the line and is never cut.
	head string
	// noticeBehindCause is true for the shapes whose cause leads a disclosure
	// instead of ending the line.
	noticeBehindCause bool
	// causeLead is whatever the route wraps around the injected reason before
	// the reason itself starts.
	causeLead string
	// unescaped is true for the one transport that does not pass its line
	// through tmuxLiteralMessage.
	unescaped bool
	// drive runs the real route with reason as its injected failure.
	drive func(t *testing.T, runner *recordingTmuxRunner, reason string)
}

// paneMenuBoundsSurface is one pane-menu action refused before committing.
func paneMenuBoundsSurface(action, label string) clientBoundsSurface {
	return clientBoundsSurface{
		name:       "pane menu " + label + " refused",
		ledgerFile: "tmux.go", ledgerSnippet: "return c.displayPaneMenuMessage(strings.TrimSpace(*client), fitLineToClient(",
		head: "projmux " + label + " failed: ",
		drive: func(t *testing.T, runner *recordingTmuxRunner, reason string) {
			t.Helper()
			cmd := &tmuxCommand{runner: runner,
				paneMenuCreate: func(agentPaneIntent, io.Writer, io.Writer) (createdPaneRuntime, error) {
					return createdPaneRuntime{}, errors.New(reason)
				},
				paneMenuDelete: func(string, io.Writer, io.Writer) error { return errors.New(reason) },
			}
			_ = cmd.Run([]string{"pane-menu", "--client", clientBoundsClient, action, "%17"}, io.Discard, io.Discard)
		},
	}
}

// windowIntentBoundsSurface is one of the four intents that share
// finishWindowIntent's failure half.
func windowIntentBoundsSurface(label string, argv []string, wire func(*tmuxCommand, string)) clientBoundsSurface {
	return clientBoundsSurface{
		name:       "Window intent " + label + " refused",
		ledgerFile: "tmux.go", ledgerSnippet: "return c.displayPaneMenuMessage(strings.TrimSpace(client), fitLineToClient(",
		head: "projmux " + label + " failed: ",
		drive: func(t *testing.T, runner *recordingTmuxRunner, reason string) {
			t.Helper()
			cmd := &tmuxCommand{runner: runner}
			wire(cmd, reason)
			_ = cmd.Run(argv, io.Discard, io.Discard)
		},
	}
}

// clientBoundsSurfaces is the closed set of client-line producers, in the
// order committedResultDisplaySites() registers them. Every non-transport row
// of that ledger is here; committed_result_test.go is what says so.
func clientBoundsSurfaces() []clientBoundsSurface {
	return []clientBoundsSurface{
		// The split funnel's kept split: the focus error leads, the split start
		// notice rides behind it.
		{
			name:       "split funnel kept a split it could not focus",
			ledgerFile: "ai.go", ledgerSnippet: "c.showCommittedSplitResult(diagnostics.SurfaceSiteSplitFocus",
			head: paneCreatedUnfocusedMessage, noticeBehindCause: true,
			causeLead: "read the Window of Pane %42: ",
			drive: func(t *testing.T, runner *recordingTmuxRunner, reason string) {
				t.Helper()
				runner.outputs[splitFocusClientsKey] = clientBoundsClient + focusFieldSeparator + "@7\n"
				runner.errors[splitFocusPaneWindowKey("%42")] = errors.New(reason)
				cmd := clientBoundsSplitCommand(t, runner, &clientBoundsCreator{
					created: createdPaneRuntime{paneID: "%42"}, notice: clientBoundsNotice,
				})
				if err := cmd.createShellPane(canonicalProducerDirectShell, "right"); err != nil {
					t.Fatalf("createShellPane() error = %v", err)
				}
			},
		},
		// The split funnel's committed split: it says nothing at all unless the
		// create seam wrote a start notice, and then that notice is the whole
		// of the line behind the head.
		{
			name:       "split funnel showed a committed split's start notice",
			ledgerFile: "ai.go", ledgerSnippet: "c.showCommittedSplitResult(diagnostics.SurfaceSiteSplitNotice",
			head: splitNoticeHead,
			drive: func(t *testing.T, runner *recordingTmuxRunner, reason string) {
				t.Helper()
				// An empty committed Pane id is create's inherited-target path:
				// the focus step has nothing to move, so the notice is the only
				// line this split shows.
				cmd := clientBoundsSplitCommand(t, runner, &clientBoundsCreator{notice: reason})
				if err := cmd.createShellPane(canonicalProducerDirectShell, "right"); err != nil {
					t.Fatalf("createShellPane() error = %v", err)
				}
			},
		},
		// The canonical create's refusal, the one line this layer shows without
		// passing it through tmuxLiteralMessage.
		{
			name:       "canonical create refused",
			ledgerFile: "ai.go", ledgerSnippet: `c.run("tmux", "display-message", "-c", intent.targetClient, "-d", "10000", reason)`,
			head: canonicalCreateFailureHead, unescaped: true,
			drive: func(t *testing.T, runner *recordingTmuxRunner, reason string) {
				t.Helper()
				cmd := clientBoundsSplitCommand(t, runner, &clientBoundsCreator{err: errors.New(reason)})
				if err := cmd.createShellPane(canonicalProducerDirectShell, "right"); err != nil {
					t.Fatalf("createShellPane() error = %v", err)
				}
			},
		},
		paneMenuBoundsSurface("split-right", "Horizontal Split"),
		paneMenuBoundsSurface("split-down", "Vertical Split"),
		paneMenuBoundsSurface("kill", "Kill"),
		// The pane menu's committed delete: the Pane is gone, and the canonical
		// route's own summary of what went with it is all that is left. This
		// layer does not own that summary's length -- the cascade count and the
		// kind spellings are the delete route's -- so the fixture is what says
		// the bound holds whatever that route hands over.
		{
			name:       "pane menu kill kept the committed delete's summary",
			ledgerFile: "tmux.go", ledgerSnippet: "c.showCommittedIntentResult(diagnostics.SurfaceSitePaneMenuKill",
			head: paneMenuSummaryHead,
			drive: func(t *testing.T, runner *recordingTmuxRunner, reason string) {
				t.Helper()
				cmd := &tmuxCommand{runner: runner,
					paneMenuDelete: func(_ string, stdout, _ io.Writer) error {
						// Only the first projection line reaches the client.
						_, _ = io.WriteString(stdout, reason+"\npane/log uid=pan-log\n")
						return nil
					},
				}
				_ = cmd.Run([]string{"pane-menu", "--client", clientBoundsClient, "kill", "%19"}, io.Discard, io.Discard)
			},
		},
		// The pane menu's kept split: the same two-cause shape as the split
		// funnel, reached by a different producer through a different transport.
		{
			name:       "pane menu kept a split it could not focus",
			ledgerFile: "tmux.go", ledgerSnippet: "c.showCommittedIntentResult(diagnostics.SurfaceSitePaneMenuSplit, strings.TrimSpace(*client), splitFocusFailureLine(",
			head: paneCreatedUnfocusedMessage, noticeBehindCause: true,
			causeLead: "read the Window of Pane %42: ",
			drive: func(t *testing.T, runner *recordingTmuxRunner, reason string) {
				t.Helper()
				runner.outputs[splitFocusClientsKey] = clientBoundsClient + focusFieldSeparator + "@7\n"
				runner.errors[splitFocusPaneWindowKey("%42")] = errors.New(reason)
				cmd := &tmuxCommand{runner: runner,
					paneMenuCreate: func(_ agentPaneIntent, _, stderr io.Writer) (createdPaneRuntime, error) {
						_, _ = io.WriteString(stderr, clientBoundsNotice)
						return createdPaneRuntime{paneID: "%42"}, nil
					},
				}
				_ = cmd.Run([]string{"pane-menu", "--client", clientBoundsClient, "split-right", "%17"}, io.Discard, io.Discard)
			},
		},
		// The pane menu's committed and focused split: the success constant,
		// carrying whatever the create seam disclosed behind it.
		{
			name:       "pane menu split carried a committed split's start notice",
			ledgerFile: "tmux.go", ledgerSnippet: "c.showCommittedIntentResult(diagnostics.SurfaceSitePaneMenuSplit, strings.TrimSpace(*client), message)",
			head: paneMenuCreatedMessage + ": ",
			drive: func(t *testing.T, runner *recordingTmuxRunner, reason string) {
				t.Helper()
				cmd := &tmuxCommand{runner: runner,
					paneMenuCreate: func(_ agentPaneIntent, _, stderr io.Writer) (createdPaneRuntime, error) {
						_, _ = io.WriteString(stderr, reason)
						return createdPaneRuntime{}, nil
					},
				}
				_ = cmd.Run([]string{"pane-menu", "--client", clientBoundsClient, "split-right", "%17"}, io.Discard, io.Discard)
			},
		},
		// The committed Window the pressing client could not be moved onto: the
		// move error leads, the create's own disclosure rides behind it.
		{
			name:       "Window committed and the client could not be moved onto it",
			ledgerFile: "tmux.go", ledgerSnippet: "c.showCommittedIntentResult(diagnostics.SurfaceSiteWindowIntent, pressing, line)",
			head: windowCreatedUnshownMessage, noticeBehindCause: true,
			causeLead: `focus: switch-client to "$1": `,
			drive: func(t *testing.T, runner *recordingTmuxRunner, reason string) {
				t.Helper()
				clients := recordedTmuxCallKey("tmux", "list-clients", "-F", "#{client_name}"+focusFieldSeparator+"#{client_session}")
				runner.outputs[clients] = clientBoundsClient + focusFieldSeparator + "$1\n"
				runner.errors[recordedTmuxCallKey("tmux", "switch-client", "-c", clientBoundsClient, "-t", "$1")] = errors.New(reason)
				cmd := &tmuxCommand{runner: runner,
					windowCreate: func(_ windowCreateIntent, _, stderr io.Writer) (createdWindowRuntime, error) {
						_, _ = io.WriteString(stderr, clientBoundsNotice)
						return createdWindowRuntime{sessionID: "$1", windowID: "@5"}, nil
					},
				}
				_ = cmd.Run([]string{"window-create", "--client", clientBoundsClient, "--anchor", "%9"}, io.Discard, io.Discard)
			},
		},
		// A Window create that committed nothing at all: the picker could not be
		// asked, so the reason is whatever asking failed with.
		//
		// client_line_fit_test.go drives this same producer and asserts more
		// than width -- the exact tmux argv as well. It stays there; this
		// surface exists so the ledger mapping below is a structural check with
		// no row exempted, and one duplicated width row is cheap.
		{
			name:       "Window create committed nothing",
			ledgerFile: "tmux.go", ledgerSnippet: "return c.displayPaneMenuMessage(client, notCreatedLine(reason, readClientLineWidth(",
			head: windowNotCreatedHead,
			drive: func(t *testing.T, runner *recordingTmuxRunner, reason string) {
				t.Helper()
				cmd := &tmuxCommand{runner: runner,
					// The route checks its create seam is configured before it
					// asks anything; asking is what fails here, so the seam is
					// wired and never reached.
					windowCreate: func(windowCreateIntent, io.Writer, io.Writer) (createdWindowRuntime, error) {
						t.Fatal("the Window create ran after the picker could not be asked")
						return createdWindowRuntime{}, nil
					},
					launchChoose: func(string, string) launchChoice { return launchChoice{problem: reason} },
				}
				_ = cmd.Run([]string{"window-create", "--client", clientBoundsClient, "--anchor", "%9"}, io.Discard, io.Discard)
			},
		},
		windowIntentBoundsSurface("Create Window", []string{"window-create", "--client", clientBoundsClient, "--anchor", "%9"},
			func(cmd *tmuxCommand, reason string) {
				cmd.windowCreate = func(windowCreateIntent, io.Writer, io.Writer) (createdWindowRuntime, error) {
					return createdWindowRuntime{}, errors.New(reason)
				}
			}),
		windowIntentBoundsSurface("Rename Window", []string{"window-rename", "--client", clientBoundsClient, "--anchor", "%9", "--", "notes"},
			func(cmd *tmuxCommand, reason string) {
				cmd.windowRename = func(windowRenameIntent, io.Writer, io.Writer) error { return errors.New(reason) }
			}),
		windowIntentBoundsSurface("Rename Pane", []string{"pane-rename", "--client", clientBoundsClient, "--anchor", "%9", "--", "notes"},
			func(cmd *tmuxCommand, reason string) {
				cmd.paneRename = func(paneRenameIntent, io.Writer, io.Writer) error { return errors.New(reason) }
			}),
		windowIntentBoundsSurface("Delete Window", []string{"window-delete", "--client", clientBoundsClient, "--anchor", "%9"},
			func(cmd *tmuxCommand, reason string) {
				cmd.windowDelete = func(string, io.Writer, io.Writer) error { return errors.New(reason) }
			}),
		// The Window create that committed and was shown: a bounded constant,
		// disclosing what it could not carry over. tmux clips that disclosure at
		// the client's edge with no elision of its own.
		{
			name:       "Window committed with a disclosure",
			ledgerFile: "tmux.go", ledgerSnippet: "c.showCommittedIntentResult(diagnostics.SurfaceSiteWindowIntent, strings.TrimSpace(client), success)",
			head: windowCreatedMessage + ": ",
			drive: func(t *testing.T, runner *recordingTmuxRunner, reason string) {
				t.Helper()
				clients := recordedTmuxCallKey("tmux", "list-clients", "-F", "#{client_name}"+focusFieldSeparator+"#{client_session}")
				runner.outputs[clients] = clientBoundsClient + focusFieldSeparator + "$1\n"
				cmd := &tmuxCommand{runner: runner,
					windowCreate: func(_ windowCreateIntent, _, stderr io.Writer) (createdWindowRuntime, error) {
						_, _ = io.WriteString(stderr, reason)
						return createdWindowRuntime{sessionID: "$1", windowID: "@5"}, nil
					},
				}
				_ = cmd.Run([]string{"window-create", "--client", clientBoundsClient, "--anchor", "%9"}, io.Discard, io.Discard)
			},
		},
	}
}

// TestEveryClientFailureLineFitsThePressingClient is C-1 Guarantee (b) and (c)
// at every producer at once: one line, no wider than the client that pressed
// the key, still naming the cause, for a reason long enough to bury it.
func TestEveryClientFailureLineFitsThePressingClient(t *testing.T) {
	t.Parallel()

	argv := argvFailureReason(t)
	// The sentence tmux itself refused with. It is asserted whole only on the
	// wider client: behind the longest head, an 80-cell client has 17 cells
	// left for the reason and the end of it is all that can survive.
	const argvCause = "split-window refused"
	if projmuxpicker.VisibleLen(argv) < 677 {
		t.Fatalf("fixture reason is %d cells, want at least the 677 measured live", projmuxpicker.VisibleLen(argv))
	}
	for _, surface := range clientBoundsSurfaces() {
		for _, width := range clientBoundsWidths {
			t.Run(surface.name+"/"+strconv.Itoa(width), func(t *testing.T) {
				t.Parallel()

				runner := clientBoundsRunner(width)
				surface.drive(t, runner, argv)

				reads, lines := clientBoundsCalls(runner.calls, clientBoundsClient)
				if len(lines) != 1 {
					t.Fatalf("client lines = %q, want exactly one", lines)
				}
				if reads != 1 {
					t.Fatalf("client width reads = %d, want exactly one", reads)
				}
				line := clientBoundsRendered(lines[0], !surface.unescaped)
				if cells := projmuxpicker.VisibleLen(line); cells > width {
					t.Fatalf("line %q is %d cells on a %d-cell client", line, cells, width)
				}
				body, led := strings.CutPrefix(line, surface.head)
				if !led {
					t.Fatalf("line %q does not lead with the whole outcome %q", line, surface.head)
				}
				// The reason was far too long for either client, so the line has
				// to say so once and end in the part of the reason tmux's own
				// cause is in.
				front, back, clipped := strings.Cut(body, clientLineElision)
				if !clipped || strings.Contains(back, clientLineElision) {
					t.Fatalf("line %q, want exactly one elision for a %d-cell reason", line, projmuxpicker.VisibleLen(argv))
				}
				if !strings.HasPrefix(surface.causeLead+argv, front) {
					t.Fatalf("line %q does not keep the front of the reason", line)
				}
				if back == "" || !strings.HasSuffix(argv, back) {
					t.Fatalf("line %q ends %q, which is not the end of the reason: tmux's own cause is there", line, back)
				}
				// A conventional terminal cannot hold the whole cause behind
				// every head, but a 120-cell one holds the sentence itself.
				if width >= 120 && !strings.Contains(line, argvCause) {
					t.Fatalf("line %q lost the cause %q on a %d-cell client", line, argvCause, width)
				}
				if surface.noticeBehindCause && strings.Contains(line, clientBoundsNotice) {
					t.Fatalf("line %q kept the whole disclosure while clipping the cause", line)
				}
			})
		}
	}
}

// TestKeptSplitAndUnshownWindowLinesKeepTheCauseAheadOfTheDisclosure is the
// half fitClientLine alone gets wrong. These two lines put what failed at the
// front and a disclosure at the end, so the middle-eliding rule that saves
// tmux's stderr everywhere else would here save the disclosure and eat the
// cause.
func TestKeptSplitAndUnshownWindowLinesKeepTheCauseAheadOfTheDisclosure(t *testing.T) {
	t.Parallel()

	argv := argvFailureReason(t)
	cause := "focus: select-pane %42: " + argv
	whole := paneCreatedUnfocusedMessage + cause + clientLineNoticeSeparator + clientBoundsNotice
	causeAlone := projmuxpicker.VisibleLen(paneCreatedUnfocusedMessage + cause)
	for _, tt := range []struct {
		name  string
		width int
		want  string
	}{
		{name: "everything fits", width: projmuxpicker.VisibleLen(whole), want: whole},
		{name: "the disclosure is cut back to what the cause left over", width: causeAlone + 40},
		{name: "the disclosure is gone once the cause needs the line", width: causeAlone},
		{name: "a client that has to clip the cause itself", width: 120},
		{name: "a conventional terminal", width: 80},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := fitClientCauseFirstLine(paneCreatedUnfocusedMessage, cause, clientBoundsNotice, tt.width)
			if cells := projmuxpicker.VisibleLen(got); cells > tt.width {
				t.Fatalf("line %q is %d cells wide, want at most %d", got, cells, tt.width)
			}
			if tt.want != "" {
				if got != tt.want {
					t.Fatalf("line = %q, want %q", got, tt.want)
				}
				return
			}
			if !strings.HasPrefix(got, paneCreatedUnfocusedMessage) {
				t.Fatalf("line %q does not lead with the outcome", got)
			}
			// Whatever the width, what failed reads before the disclosure does.
			withoutNotice := fitClientCauseFirstLine(paneCreatedUnfocusedMessage, cause, "", tt.width)
			causeCells := projmuxpicker.VisibleLen(withoutNotice)
			if noticeCells := projmuxpicker.VisibleLen(got) - causeCells; noticeCells > 0 &&
				!strings.HasPrefix(got, withoutNotice) {
				t.Fatalf("line %q does not keep the whole of the cause it shows alone as %q", got, withoutNotice)
			}
			if strings.Contains(got, clientBoundsNotice) {
				t.Fatalf("line %q kept the whole disclosure at %d cells", got, tt.width)
			}
			if !strings.Contains(got, "window refused") {
				t.Fatalf("line %q lost the end of the cause", got)
			}
		})
	}

	// Every width, for both shapes: the line never exceeds the client, and the
	// notice never displaces the cause.
	for width := 1; width <= 400; width++ {
		got := fitClientCauseFirstLine(paneCreatedUnfocusedMessage, cause, clientBoundsNotice, width)
		if cells := projmuxpicker.VisibleLen(got); cells > width {
			t.Fatalf("line %q is %d cells wide at width %d", got, cells, width)
		}
		alone := fitClientCauseFirstLine(paneCreatedUnfocusedMessage, cause, "", width)
		if !strings.HasPrefix(got, alone) {
			t.Fatalf("at width %d the notice displaced the cause: %q does not start with %q", width, got, alone)
		}
	}
}

// clientBoundsConstantSurface is one committed line that says nothing at all
// unless its route had something to disclose.
type clientBoundsConstantSurface struct {
	name string
	// want is the whole line when there is nothing to disclose, or "" for the
	// route that then shows no line at all.
	want  string
	drive func(t *testing.T, runner *recordingTmuxRunner, disclosure string)
}

// clientBoundsConstantSurfaces are the three committed lines this contract
// added a width read to. The read is what they now cost, so the rule that
// bounds it is here: a route with nothing to disclose pays nothing.
func clientBoundsConstantSurfaces() []clientBoundsConstantSurface {
	return []clientBoundsConstantSurface{
		{
			name: "split funnel with no start notice",
			drive: func(t *testing.T, runner *recordingTmuxRunner, disclosure string) {
				t.Helper()
				cmd := clientBoundsSplitCommand(t, runner, &clientBoundsCreator{notice: disclosure})
				if err := cmd.createShellPane(canonicalProducerDirectShell, "right"); err != nil {
					t.Fatalf("createShellPane() error = %v", err)
				}
			},
		},
		{
			name: "pane menu kill with no delete projection", want: paneMenuDeletedMessage,
			drive: func(t *testing.T, runner *recordingTmuxRunner, disclosure string) {
				t.Helper()
				cmd := &tmuxCommand{runner: runner,
					paneMenuDelete: func(_ string, stdout, _ io.Writer) error {
						_, _ = io.WriteString(stdout, disclosure)
						return nil
					},
				}
				_ = cmd.Run([]string{"pane-menu", "--client", clientBoundsClient, "kill", "%19"}, io.Discard, io.Discard)
			},
		},
		{
			name: "pane menu split with no start notice", want: paneMenuCreatedMessage,
			drive: func(t *testing.T, runner *recordingTmuxRunner, disclosure string) {
				t.Helper()
				cmd := &tmuxCommand{runner: runner,
					paneMenuCreate: func(_ agentPaneIntent, _, stderr io.Writer) (createdPaneRuntime, error) {
						_, _ = io.WriteString(stderr, disclosure)
						return createdPaneRuntime{}, nil
					},
				}
				_ = cmd.Run([]string{"pane-menu", "--client", clientBoundsClient, "split-right", "%17"}, io.Discard, io.Discard)
			},
		},
	}
}

// TestACommittedLineWithNothingToDiscloseReadsNoClientWidth is the trade-off
// half of C-1: fitting costs one `#{client_width}` read per line, and a
// bounded constant must not pay it.
//
// It is also what keeps the e2e assertions on "Created Pane" true. Those four
// surfaces are successful splits with nothing to say, so the line they wait for
// is the constant byte for byte -- a fit that ran anyway could only make it
// something else.
func TestACommittedLineWithNothingToDiscloseReadsNoClientWidth(t *testing.T) {
	t.Parallel()

	for _, surface := range clientBoundsConstantSurfaces() {
		for _, disclosure := range []string{"", "   ", "\n", " \n\t "} {
			t.Run(surface.name+"/"+strconv.Quote(disclosure), func(t *testing.T) {
				t.Parallel()

				runner := clientBoundsRunner(80)
				surface.drive(t, runner, disclosure)

				reads, lines := clientBoundsCalls(runner.calls, clientBoundsClient)
				if reads != 0 {
					t.Fatalf("client width reads = %d, want none: there was nothing to fit", reads)
				}
				if surface.want == "" {
					if len(lines) != 0 {
						t.Fatalf("client lines = %q, want none", lines)
					}
					return
				}
				if len(lines) != 1 || clientBoundsRendered(lines[0], true) != surface.want {
					t.Fatalf("client lines = %q, want exactly one %q", lines, surface.want)
				}
			})
		}
	}
}

// clientBoundsResumeHolder is one Agent in the disclosure's middle, spelled the
// way inheritedResumeLaunchValues spells it.
func clientBoundsResumeHolder(index int) string {
	return fmt.Sprintf("agent/lead-ship-notice-bounds-%02d (uid:agent-%02dwuq3zue5gzkt2jgla)", index, index)
}

// clientBoundsResumeDisclosure is the resume seam's launch-value disclosure for
// holders Agents, byte for byte the sentence inheritedResumeLaunchValues builds:
// the provider and conversation with the reason token at its front, one holder
// per Agent in its middle, and what they disagreed about at its end. It grows
// with the holders, which is why the line that carries it has no bound of its
// own.
func clientBoundsResumeDisclosure(holders int) string {
	names := make([]string, 0, holders)
	for index := range holders {
		names = append(names, clientBoundsResumeHolder(index))
	}
	return fmt.Sprintf("%s conversation %s opened without inherited launch values (%s): %s record different %s",
		aiModeClaude, "b3cbaa25-bd88-4f8a-b236-26e760ce0e61", launchValuesReasonAmbiguous,
		strings.Join(names, ", "), ambiguousLaunchValueSubject(aiModeClaude))
}

// clientBoundsNoticeSurfaces are the two producers that carry that disclosure.
func clientBoundsNoticeSurfaces() []clientBoundsSurface {
	var out []clientBoundsSurface
	for _, surface := range clientBoundsSurfaces() {
		if strings.Contains(surface.name, "start notice") {
			out = append(out, surface)
		}
	}
	return out
}

// TestTheResumeDisclosureLosesItsHoldersAndNotItsEnds is the half of C-1 that
// says which part of a disclosure is worth keeping.
//
// The sentence is built front, middle, end: the reason token and the
// conversation lead it, one holder per Agent fills its middle, and what those
// holders disagreed about ends it. The middle is the part that grows without
// bound and the part an operator can do least with, so middle elision is not a
// coincidence here -- it drops exactly the holders.
//
// What survives is a budget, not a promise of particular words. A conventional
// terminal has 35 cells for each end of a sentence whose front clause alone is
// 96, so at 80 and 120 cells both ends are themselves clipped; the widths are
// asserted at all three anyway, because the rule under test is that the line
// never exceeds its client and never keeps the middle over the ends.
func TestTheResumeDisclosureLosesItsHoldersAndNotItsEnds(t *testing.T) {
	t.Parallel()

	const holders = 12
	notice := clientBoundsResumeDisclosure(holders)
	subject := "record different " + ambiguousLaunchValueSubject(aiModeClaude)
	if !strings.HasSuffix(notice, subject) || !strings.Contains(notice, launchValuesReasonAmbiguous) {
		t.Fatalf("fixture %q is not the disclosure this contract is about", notice)
	}
	// The front clause is everything through the reason token, and each end of
	// a fitted line gets half of what the head and the elision leave. So the
	// width at which the token reads as itself is twice that clause, and it is
	// derived rather than picked: a guessed width silently stops proving this
	// the moment the sentence changes.
	lead := notice[:strings.Index(notice, "("+launchValuesReasonAmbiguous+")")+len(launchValuesReasonAmbiguous)+2]
	wide := 2*projmuxpicker.VisibleLen(lead) + projmuxpicker.VisibleLen(paneMenuCreatedMessage+": ") + 1
	surfaces := clientBoundsNoticeSurfaces()
	if len(surfaces) != 2 {
		t.Fatalf("disclosure-carrying surfaces = %d, want the split funnel's and the pane menu's", len(surfaces))
	}
	for _, surface := range surfaces {
		for _, width := range append(append([]int{}, clientBoundsWidths...), wide) {
			t.Run(surface.name+"/"+strconv.Itoa(width), func(t *testing.T) {
				t.Parallel()

				runner := clientBoundsRunner(width)
				surface.drive(t, runner, notice)

				reads, lines := clientBoundsCalls(runner.calls, clientBoundsClient)
				if len(lines) != 1 || reads != 1 {
					t.Fatalf("client lines = %q and width reads = %d, want one of each", lines, reads)
				}
				line := clientBoundsRendered(lines[0], !surface.unescaped)
				t.Logf("%d cells: %q", projmuxpicker.VisibleLen(line), line)
				if cells := projmuxpicker.VisibleLen(line); cells > width {
					t.Fatalf("line %q is %d cells on a %d-cell client", line, cells, width)
				}
				body, led := strings.CutPrefix(line, surface.head)
				if !led {
					t.Fatalf("line %q does not lead with the whole head %q", line, surface.head)
				}
				front, back, clipped := strings.Cut(body, clientLineElision)
				if !clipped || strings.Contains(back, clientLineElision) {
					t.Fatalf("line %q, want exactly one elision for a %d-cell disclosure", line, projmuxpicker.VisibleLen(notice))
				}
				if !strings.HasPrefix(notice, front) {
					t.Fatalf("line %q does not keep the front of the disclosure", line)
				}
				if back == "" || !strings.HasSuffix(notice, back) {
					t.Fatalf("line %q ends %q, which is not the end of the disclosure", line, back)
				}
				if width < wide {
					// The middle is what went: on a client this narrow not one
					// holder survives whole, however many of them the seam wrote.
					for index := range holders {
						if holder := clientBoundsResumeHolder(index); strings.Contains(line, holder) {
							t.Fatalf("line %q kept holder %q while clipping the ends of the sentence", line, holder)
						}
					}
					return
				}
				// Given the cells for it, both ends read as themselves. What a
				// narrower client loses is the middle first and then the ends
				// from the inside out; it never loses an end while keeping the
				// middle, which is what the prefix and suffix checks above say
				// at every width.
				for _, want := range []string{launchValuesReasonAmbiguous, "record different"} {
					if !strings.Contains(line, want) {
						t.Fatalf("line %q lost %q on a %d-cell client", line, want, width)
					}
				}
			})
		}
	}
}
