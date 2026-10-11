package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/crevissepartners/projmux/internal/services/agentcatalog"
	"io"
	"strings"
)

// agentModelProjection is the `agent models -o json` shape. Models is a
// suggestion in order, not an allowlist: AcceptsUnlisted states that
// `create --model` and a Profile `model` take any other well-formed name too.
type agentModelProjection = agentcatalog.ModelSuggestions

// runModels lists the requested provider's model suggestions. The omitted
// provider keeps the static Claude list; Codex reads a running app-server.
func (c *agentCommand) runModels(args []string, stdout, stderr io.Writer) error {
	const spelling = "agent models"
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	setRouteUsage(fs)
	var provider, output string
	fs.StringVar(&provider, "provider", "claude", "provider id: claude (default) or codex")
	fs.StringVar(&output, "o", "", "output mode: json")
	rest, err := parseWithPositionals(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return flagParseError(err)
	}
	if len(rest) > 0 {
		return usageError(fmt.Sprintf("%s accepts no positional arguments; got %q", spelling, rest[0]))
	}
	if output != "" && output != "json" {
		return usageError(fmt.Sprintf("%s: unsupported output mode %q", spelling, output))
	}
	projection, err := agentcatalog.Models(context.Background(), provider, c.listCodexModels)
	if err != nil {
		return MapMetadataError(err)
	}
	if output == "json" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(projection)
	}
	_, err = fmt.Fprintln(stdout, strings.Join(projection.Models, "\n"))
	return err
}
