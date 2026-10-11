package agentcatalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/version"
)

type ModelSuggestions struct {
	Provider        aiprovider.ID `json:"provider"`
	Models          []string      `json:"models"`
	AcceptsUnlisted bool          `json:"acceptsUnlisted"`
}

// Failure preserves CLI messages and identifies SDK failures without text matching.
type Failure struct {
	Code  string
	Cause error
	Usage bool
}

func (e *Failure) Error() string            { return e.Cause.Error() }
func (e *Failure) Unwrap() error            { return e.Cause }
func (e *Failure) MetadataUsageError() bool { return e.Usage }
func invalid(code, message string) error {
	return &Failure{Code: code, Cause: errors.New(message), Usage: true}
}
func Provider(spelling, provider string) (aiprovider.ID, error) {
	metadata, ok := aiprovider.Lookup(provider)
	if !ok {
		return "", invalid("provider-invalid", fmt.Sprintf("%s: unsupported provider %q", spelling, provider))
	}
	return metadata.ID, nil
}
func Models(ctx context.Context, provider string, list func(context.Context) ([]string, error)) (ModelSuggestions, error) {
	if err := ctx.Err(); err != nil {
		return ModelSuggestions{}, err
	}
	id, err := Provider("agent models", provider)
	if err != nil {
		return ModelSuggestions{}, err
	}
	var models []string
	switch id {
	case aiprovider.Claude:
		models = profile.ClaudeModels()
	case aiprovider.Codex:
		if list == nil {
			list = ListDefaultCodexModels
		}
		bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		models, err = list(bounded)
		if err != nil {
			code := "models-unavailable"
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(bounded.Err(), context.DeadlineExceeded) {
				code = "models-timeout"
			}
			return ModelSuggestions{}, &Failure{Code: code, Cause: fmt.Errorf("agent models: Codex app-server model/list failed: %w", err)}
		}
	default:
		return ModelSuggestions{}, invalid("models-unsupported", fmt.Sprintf("agent models: model listing is unavailable for provider %s", id))
	}
	return ModelSuggestions{Provider: id, Models: slices.Clone(models), AcceptsUnlisted: true}, nil
}

// ListDefaultCodexModels attaches to the existing endpoint through its official
// read-only probe/proxy; it never starts or configures the daemon.
func ListDefaultCodexModels(ctx context.Context) ([]string, error) {
	client, _, err := codexappserver.AttachDefaultEndpoint(ctx, version.String(), codexappserver.AttachOptions{Timeout: codexappserver.DefaultProbeTimeout})
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return codexappserver.ListModelsOn(ctx, client)
}
