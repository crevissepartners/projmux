package app

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/core/profile"
)

// agentModelProjection is the `agent models -o json` shape. Models is a
// suggestion in order, not an allowlist: AcceptsUnlisted states that
// `create --model` and a Profile `model` take any other well-formed name too.
type agentModelProjection struct {
	Provider        aiprovider.ID `json:"provider"`
	Models          []string      `json:"models"`
	AcceptsUnlisted bool          `json:"acceptsUnlisted"`
}

// runModels prints the model names projmux can launch Claude with. It is a
// static read of the core list: no Registry, tmux, provider process, network,
// or file access.
func (c *agentCommand) runModels(args []string, stdout, stderr io.Writer) error {
	const spelling = "agent models"
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var provider, output string
	fs.StringVar(&provider, "provider", string(aiprovider.Claude), "provider id: claude")
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
	metadata, ok := aiprovider.Lookup(provider)
	if !ok {
		return usageError(fmt.Sprintf("%s: unsupported provider %q", spelling, provider))
	}
	if metadata.ID != aiprovider.Claude {
		return usageError(fmt.Sprintf("%s: projmux does not apply a model to provider %s; --model and a Profile model apply only to --provider %s",
			spelling, metadata.ID, aiprovider.Claude))
	}
	projection := agentModelProjection{Provider: metadata.ID, Models: profile.ClaudeModels(), AcceptsUnlisted: true}
	if output == "json" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(projection)
	}
	_, err = fmt.Fprintln(stdout, strings.Join(projection.Models, "\n"))
	return err
}
