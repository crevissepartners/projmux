package app

import (
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

// Contract C-1 for the whole of the interactive tmux adapter layer: every
// failure line it puts on a client is fitted to that client, and the cause
// survives the fit.
//
// The producers below are the failure rows of committedResultDisplaySites() --
// the pane menu's refusal and its kept split, the shared Window-intent refusal
// and the committed Window that could not be shown, the split funnel's kept
// split and the canonical create's refusal. Each is driven through its own
// route, because a projection that fits proves nothing about a producer that
// never calls it. The shared Window-intent row is driven once per intent it
// carries: one line there is the failure of a create, two renames and a delete.
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

// clientBoundsSurface is one failure-line producer and what its line has to
// keep.
type clientBoundsSurface struct {
	name string
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
		name: "pane menu " + label + " refused",
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
		name: "Window intent " + label + " refused",
		head: "projmux " + label + " failed: ",
		drive: func(t *testing.T, runner *recordingTmuxRunner, reason string) {
			t.Helper()
			cmd := &tmuxCommand{runner: runner}
			wire(cmd, reason)
			_ = cmd.Run(argv, io.Discard, io.Discard)
		},
	}
}

// clientBoundsSurfaces is the closed set of failure-line producers, in the
// order committedResultDisplaySites() registers them.
func clientBoundsSurfaces() []clientBoundsSurface {
	return []clientBoundsSurface{
		// The split funnel's kept split: the focus error leads, the split start
		// notice rides behind it.
		{
			name: "split funnel kept a split it could not focus",
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
		// The canonical create's refusal, the one line this layer shows without
		// passing it through tmuxLiteralMessage.
		{
			name: "canonical create refused", head: canonicalCreateFailureHead, unescaped: true,
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
		// The pane menu's kept split: the same two-cause shape as the split
		// funnel, reached by a different producer through a different transport.
		{
			name: "pane menu kept a split it could not focus",
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
		// The committed Window the pressing client could not be moved onto: the
		// move error leads, the create's own disclosure rides behind it.
		{
			name: "Window committed and the client could not be moved onto it",
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
		// The one success line of this layer that is not a bounded constant: a
		// committed Window create discloses what it could not carry over, and
		// tmux clips that at the client's edge with no elision of its own.
		{
			name: "Window committed with a disclosure", head: windowCreatedMessage + ": ",
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
