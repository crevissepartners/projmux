package runtime

import (
	"context"
	"strings"
)

// tmuxCommandRunner is the narrow subprocess seam the materializer shares with
// the resource metadata mirror. Production wires the same ExecRunner into both,
// so a test can replace one object and observe every tmux call the operation
// makes.
type TmuxCommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

const FinalizeOperationEnvironment = "__projmux_finalize_operation"

func ExactTmuxHandle(output, prefix string) string {
	output = strings.TrimSpace(output)
	if len(output) < 2 || !strings.HasPrefix(output, prefix) {
		return ""
	}
	for _, r := range strings.TrimPrefix(output, prefix) {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return output
}

// The field separator of the materializer's own list queries, in the two
// spellings tmux distinguishes.
//
// A format must carry the escaped spelling. tmux renders a non-printable byte in
// list output as its octal escape, so a raw 0x1F in the format comes back raw on
// tmux 3.6 and as the four literal characters `\037` on tmux 3.5a, while the
// escaped spelling comes back identically on both. Parsing folds the escaped
// spelling back to the raw byte, which also accepts the raw form.
const (
	TmuxRowSep       = "\x1f"
	TmuxRowSepFormat = "\\037"
)

// tmuxRowFormat joins format fields with the separator tmux prints verbatim.
func TmuxRowFormat(fields ...string) string {
	return strings.Join(fields, TmuxRowSepFormat)
}

// splitTmuxRows parses a tmux list output into rows of exactly want fields.
func SplitTmuxRows(output string, want int) [][]string {
	output = strings.ReplaceAll(output, TmuxRowSepFormat, TmuxRowSep)
	var rows [][]string
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, TmuxRowSep)
		if len(fields) != want {
			continue
		}
		rows = append(rows, fields)
	}
	return rows
}
