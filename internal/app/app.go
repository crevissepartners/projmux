package app

import (
	"fmt"
	"io"

	"github.com/es5h/projmux/internal/version"
)

// Run is the current CLI bootstrap. Feature commands will grow from here.
func Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}

	switch args[0] {
	case "version", "--version", "-version":
		_, err := fmt.Fprintf(stdout, "projmux %s\n", version.String())
		return err
	case "help", "--help", "-h":
		printUsage(stdout)
		return nil
	default:
		printUsage(stderr)
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "projmux")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  help      Show bootstrap help")
	fmt.Fprintln(w, "  version   Print the current version")
}
