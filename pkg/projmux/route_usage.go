package projmux

import (
	"flag"
	"io"

	"github.com/crevissepartners/projmux/internal/cli"
)

// WriteRouteUsage writes the catalog Usage of route, the exact canonical path
// of a public catalog route, as the `Usage:` block the CLI prints under a
// rejected call's reason. A route the catalog does not resolve exactly, or one
// that declares no Usage, writes nothing. The output is the one
// `projmux <route> --help` prints: the wording comes from the CLI catalog, not
// from a copy.
func WriteRouteUsage(w io.Writer, route string) {
	cli.WriteRouteUsage(w, route)
}

// SetRouteUsage makes fs answer a flag parse failure with the catalog Usage of
// the public route fs is named after, instead of the flag package's default
// listing. The FlagSet name must be the exact canonical route. A FlagSet named
// after a plumbing, hidden or unknown route keeps the flag package default.
func SetRouteUsage(fs *flag.FlagSet) {
	cli.SetRouteUsage(fs)
}

// WriteUsage writes lines as the same `Usage:` block WriteRouteUsage writes
// for a catalog route, for a caller whose route is not in the public catalog
// and keeps its own Usage lines. The block format is the CLI's single
// definition; the lines are the caller's. No lines write nothing.
func WriteUsage(w io.Writer, lines []string) {
	cli.WriteUsageLines(w, lines)
}

// SetUsage makes fs answer a flag parse failure with the Usage block of lines,
// as WriteUsage writes it. Unlike SetRouteUsage it does not judge fs's name
// against the public catalog. The lines are captured as given.
func SetUsage(fs *flag.FlagSet, lines []string) {
	lines = append([]string(nil), lines...)
	fs.Usage = func() { cli.WriteUsageLines(fs.Output(), lines) }
}
