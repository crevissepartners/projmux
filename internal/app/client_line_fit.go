package app

import (
	"context"
	"strings"

	"github.com/crevissepartners/projmux/internal/ui/projmuxpicker"
)

// One failure line on the client that pressed the key.
//
// tmux shows a `display-message` on the status line and clips it at the
// client's right edge. A failure reason can be a whole tmux argv -- a create
// that could not split its Pane names the socket, every flag, and the
// supervisor command line before tmux's own stderr at the very end -- so a
// line that is simply shown loses both what did not happen and why. The line
// is fitted instead: the outcome leads and is never cut, and a reason that
// does not fit loses its middle, keeping the step that failed at its front and
// tmux's cause at its end.

// defaultClientLineWidth is the width a client line is fitted to when the
// pressing client's own width cannot be read.
const defaultClientLineWidth = 80

// clientLineElision stands in for the part of a reason that did not fit.
const clientLineElision = "…"

// clientLineWidthFormat is the one read a failure line costs.
const clientLineWidthFormat = "#{client_width}"

// readClientLineWidth reads the exact client's width, in cells. Anything that
// is not a positive width -- no runner, a failed read, an empty or non-numeric
// answer -- is defaultClientLineWidth: a line that cannot be measured against
// its client is still shown, fitted to a common terminal. Only a line that can
// be arbitrarily long calls it -- every failure line, and the one success line
// that carries a disclosure -- so a bounded constant never pays for the read.
func readClientLineWidth(ctx context.Context, runner tmuxRunner, client string) int {
	if runner == nil || strings.TrimSpace(client) == "" {
		return defaultClientLineWidth
	}
	out, err := runner.Run(ctx, "tmux", "display-message", "-p", "-c", strings.TrimSpace(client), "-F", clientLineWidthFormat)
	if err != nil {
		return defaultClientLineWidth
	}
	if width := parsePositiveInt(string(out)); width > 0 {
		return width
	}
	return defaultClientLineWidth
}

// fitClientLine is head followed by reason, no wider than width cells (a width
// that is not positive is defaultClientLineWidth).
//
// A line that fits is returned byte for byte. Otherwise head is kept whole and
// the middle of reason is replaced by one elision, the remaining cells split
// evenly between the reason's front and its end. A width narrower than head
// and the elision keeps the start of head and ends in the elision. Width is
// measured as rendered: before tmuxLiteralMessage doubles `#` and `%`, each of
// which still renders as one cell. No rune is split, and a two-cell rune that
// does not fit is dropped, so a line may come out one cell short but never
// over.
func fitClientLine(head, reason string, width int) string {
	if width <= 0 {
		width = defaultClientLineWidth
	}
	line := head + reason
	if projmuxpicker.VisibleLen(line) <= width {
		return line
	}
	elision := projmuxpicker.VisibleLen(clientLineElision)
	room := width - projmuxpicker.VisibleLen(head) - elision
	if room < 0 {
		front, _ := clientLineFront(line, width-elision)
		return front + clientLineElision
	}
	front, used := clientLineFront(reason, room-room/2)
	return head + front + clientLineElision + clientLineBack(reason, room-used)
}

// clientLineFront is the longest prefix of value no wider than cells, and its
// width.
func clientLineFront(value string, cells int) (string, int) {
	used := 0
	for index, r := range value {
		width := projmuxpicker.RuneWidth(r)
		if used+width > cells {
			return value[:index], used
		}
		used += width
	}
	return value, used
}

// clientLineBack is the longest suffix of value no wider than cells.
func clientLineBack(value string, cells int) string {
	runes := []rune(value)
	used, start := 0, len(runes)
	for start > 0 {
		width := projmuxpicker.RuneWidth(runes[start-1])
		if used+width > cells {
			break
		}
		used += width
		start--
	}
	return string(runes[start:])
}

// clientLineWords is the one-space normalization the transports apply before
// tmux renders a line. Fitting has to measure what is rendered, so a producer
// normalizes first and the transport's own pass is then a no-op.
func clientLineWords(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// fitLineToClient is head followed by reason, fitted to the exact client that
// pressed the key. It is the whole of what a producer of an unbounded line
// does about width: the read is the only tmux call it adds.
func fitLineToClient(ctx context.Context, runner tmuxRunner, client, head, reason string) string {
	return fitClientLine(head, clientLineWords(reason), readClientLineWidth(ctx, runner, client))
}

// clientLineNoticeSeparator joins a notice to the cause it rides behind.
const clientLineNoticeSeparator = "; "

// fitClientCauseFirstLine is head, cause and a trailing notice, no wider than
// width cells: the line shape whose cause is not at its end.
//
// fitClientLine keeps a reason's front and its end because tmux's own stderr
// is at the end of it. A mutation that committed and could not be shown
// reverses that. What failed is the focus or move error right after the head,
// and what follows the separator is a notice that only adds context, so
// fitting the two as one reason would spend the line's last cells on the
// notice and elide the middle of the cause. The notice yields instead: it is
// cut back to whatever the cause left over, and it is gone once the cause
// alone needs the line. The cause is then fitted exactly as every other
// failure line is.
func fitClientCauseFirstLine(head, cause, notice string, width int) string {
	if width <= 0 {
		width = defaultClientLineWidth
	}
	cause, notice = clientLineWords(cause), clientLineWords(notice)
	if notice == "" {
		return fitClientLine(head, cause, width)
	}
	if line := head + cause + clientLineNoticeSeparator + notice; projmuxpicker.VisibleLen(line) <= width {
		return line
	}
	fitted := fitClientLine(head, cause, width)
	room := width - projmuxpicker.VisibleLen(fitted) -
		projmuxpicker.VisibleLen(clientLineNoticeSeparator) - projmuxpicker.VisibleLen(clientLineElision)
	if room <= 0 {
		return fitted
	}
	front, used := clientLineFront(notice, room)
	if used == 0 {
		return fitted
	}
	return fitted + clientLineNoticeSeparator + front + clientLineElision
}

// fitCauseFirstLineToClient is fitClientCauseFirstLine for the exact client
// that pressed the key.
func fitCauseFirstLineToClient(ctx context.Context, runner tmuxRunner, client, head, cause, notice string) string {
	return fitClientCauseFirstLine(head, cause, notice, readClientLineWidth(ctx, runner, client))
}
