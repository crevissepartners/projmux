// Command gendocs renders the generated CLI reference to stdout.
//
// It is the regeneration entrypoint behind `make docs`, which captures stdout
// into docs/cli.md through a temporary file so a failed render can never
// truncate the checked-in page. Writing to stdout rather than to a path keeps
// the generator free of any filesystem policy and keeps its output a pure
// function of the command manifest.
package main

import (
	"fmt"
	"os"

	"github.com/crevissepartners/projmux/internal/cli"
)

func main() {
	if err := cli.RenderReference(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "gendocs: %v\n", err)
		os.Exit(1)
	}
}
