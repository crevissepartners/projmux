package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/version"
)

// agentModelProjection is the `agent models -o json` shape. Models is a
// suggestion in order, not an allowlist: AcceptsUnlisted states that
// `create --model` and a Profile `model` take any other well-formed name too.
type agentModelProjection struct {
	Provider        aiprovider.ID `json:"provider"`
	Models          []string      `json:"models"`
	AcceptsUnlisted bool          `json:"acceptsUnlisted"`
}

// runModels lists the requested provider's model suggestions. The omitted
// provider keeps the static Claude list; Codex reads a running app-server.
func (c *agentCommand) runModels(args []string, stdout, stderr io.Writer) error {
	const spelling = "agent models"
	fs := flag.NewFlagSet(spelling, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var provider, output string
	fs.StringVar(&provider, "provider", string(aiprovider.Claude), "provider id: claude (default) or codex")
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
	var models []string
	switch metadata.ID {
	case aiprovider.Claude:
		models = profile.ClaudeModels()
	case aiprovider.Codex:
		list := c.listCodexModels
		if list == nil {
			list = listDefaultCodexModels
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		models, err = list(ctx)
		if err != nil {
			return fmt.Errorf("%s: Codex app-server model/list failed: %w", spelling, err)
		}
	default:
		return usageError(fmt.Sprintf("%s: model listing is unavailable for provider %s", spelling, metadata.ID))
	}
	projection := agentModelProjection{Provider: metadata.ID, Models: models, AcceptsUnlisted: true}
	if output == "json" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(projection)
	}
	_, err = fmt.Fprintln(stdout, strings.Join(projection.Models, "\n"))
	return err
}

func listDefaultCodexModels(ctx context.Context) ([]string, error) {
	client, _, err := codexappserver.AttachDefaultEndpoint(ctx, version.String(), codexappserver.AttachOptions{
		Timeout: codexappserver.DefaultProbeTimeout,
	})
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return codexappserver.ListModelsOn(ctx, client)
}
