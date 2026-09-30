// Command gennotices renders the licence notices of what the projmux binary
// is made of to stdout: projmux itself, the Go runtime and standard library,
// and every Go module linked into it.
//
// It is the regeneration entrypoint behind `make notices`, which captures
// stdout into THIRD_PARTY_NOTICES at the repository root through a temporary
// file, as `make docs` does for the CLI reference. The release archives and
// the npm platform packages ship that file next to the binary. Run from the
// repository root.
package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gennotices: %v\n", err)
		os.Exit(1)
	}
	parts, err := collect(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gennotices: %v\n", err)
		os.Exit(1)
	}
	// The text is read back before it is written out: each part has its
	// section, once, and nothing else has one.
	var text bytes.Buffer
	if err := render(&text, parts); err != nil {
		fmt.Fprintf(os.Stderr, "gennotices: %v\n", err)
		os.Exit(1)
	}
	if found := problems(text.String(), parts); len(found) != 0 {
		fmt.Fprintf(os.Stderr, "gennotices: the notices do not say what the binary is made of:\n  %s\n", strings.Join(found, "\n  "))
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(text.Bytes()); err != nil {
		fmt.Fprintf(os.Stderr, "gennotices: %v\n", err)
		os.Exit(1)
	}
}
