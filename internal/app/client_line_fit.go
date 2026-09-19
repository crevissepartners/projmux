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
// its client is still shown, fitted to a common terminal. Only failure lines
// call it, so no successful intent pays for the read.
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
