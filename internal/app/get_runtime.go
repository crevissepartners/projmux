package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/cli"
	"github.com/crevissepartners/projmux/internal/core/runtimediag"
)

// runtimeReadKinds lists the object kinds `get runtime` projects, in help order.
var runtimeReadKinds = cli.GrandchildSpellings("get", "runtime")

// runRuntime answers one `get runtime <kind>` read.
//
// It is a sibling of the registry reads rather than a mode of them. `get panes`
// enumerates Registry Pane resources and answers a selector; this enumerates one
// tmux server and answers no selector at all, because the objects it reports
// mostly have no name a selector could resolve. Sharing a flag set would have
// meant teaching the selector grammar about objects that deliberately have no
// identity, which is the merge this track exists to prevent.
func (c *getCommand) runRuntime(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("get runtime requires an object kind: %s",
			strings.Join(runtimeReadKinds, ", ")))
	}
	token, ok := cli.CanonicalGrandchildToken("get", "runtime", args[0])
	if !ok {
		return usageError(fmt.Sprintf("get runtime %s is not available; this release implements: %s",
			args[0], strings.Join(runtimeReadKinds, ", ")))
	}
	kind, ok := runtimeKindTokens[token]
	if !ok {
		return usageError(fmt.Sprintf("get runtime %s is not available; this release implements: %s",
			args[0], strings.Join(runtimeReadKinds, ", ")))
	}
	spelling := "get runtime " + token

	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var request runtimeTransportRequest
	var output string
	fs.StringVar(&request.socket, "socket", "", "exact tmux socket name (tmux -L)")
	fs.StringVar(&request.socketPath, "socket-path", "", "exact absolute tmux socket path (tmux -S)")
	fs.StringVar(&output, "output", "", "result projection")
	fs.StringVar(&output, "o", "", "result projection (alias of --output)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	if fs.NArg() != 0 {
		return usageError(fmt.Sprintf("%s does not accept positional arguments; got %q", spelling, fs.Arg(0)))
	}

	mode, field, err := resolveOutputMode(spelling, output)
	if err != nil {
		return err
	}
	if field != "" {
		return usageError(fmt.Sprintf("-o %s is not a %s projection", field, spelling))
	}

	reader := c.runtimeReader()
	transport, err := reader.transport(request)
	if err != nil {
		// A malformed or contradictory routing request is operator input, not a
		// machine state, so it exits 2 like every other usage error rather than
		// being reported as an unavailable server.
		return usageError(spelling + ": " + err.Error())
	}
	graph, err := reader.resolve(context.Background(), transport)
	if err != nil {
		return err
	}
	report := runtimediag.Project(graph, kind)
	switch mode {
	case cli.OutputModeNone:
		return nil
	case cli.OutputModeJSON, cli.OutputModeDefault, cli.OutputModeWide:
		return writeRuntimeReport(stdout, report, mode)
	default:
		return fmt.Errorf("%s: unsupported output mode %q", spelling, mode)
	}
}

// runtimeReader returns the configured read seam, building the production one
// on demand so the narrow test fixtures that construct a getCommand literal
// keep working without wiring a tmux runner they do not exercise.
func (c *getCommand) runtimeReader() *runtimeDiagnosticsReader {
	if c.runtimeDiag == nil {
		c.runtimeDiag = newRuntimeDiagnosticsReader(nil)
	}
	return c.runtimeDiag
}
