package projmux

import (
	"flag"
	"io"

	"github.com/crevissepartners/projmux/internal/cli/usageblock"
)

// WriteUsage writes lines as the `Usage:` block the CLI prints under a
// rejected call's reason and in `--help`, for a caller whose route is not in
// the public catalog and keeps its own Usage lines. The block format is the
// CLI's single definition; the lines are the caller's. No lines write nothing.
func WriteUsage(w io.Writer, lines []string) {
	usageblock.Write(w, lines)
}

// SetUsage makes fs answer a flag parse failure with the Usage block of lines,
// as WriteUsage writes it, instead of the flag package's default listing. The
// lines are captured as given, so a later change to the caller's slice does
// not alter the output.
func SetUsage(fs *flag.FlagSet, lines []string) {
	lines = append([]string(nil), lines...)
	fs.Usage = func() { usageblock.Write(fs.Output(), lines) }
}
