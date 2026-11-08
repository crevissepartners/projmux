// Package usageblock is the one definition of the `Usage:` block format the CLI
// prints under a route's reason line and `--help`. It imports only the
// standard library so the public SDK can call it without taking on the CLI
// command tree.
package usageblock

import (
	"fmt"
	"io"
)

// Write writes lines as the `Usage:` block: the heading, then each line
// indented two spaces. No lines write nothing.
func Write(w io.Writer, lines []string) {
	if len(lines) == 0 {
		return
	}
	fmt.Fprintln(w, "Usage:")
	for _, line := range lines {
		fmt.Fprintln(w, "  "+line)
	}
}
