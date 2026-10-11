package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/agentcatalog"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeProviderInvalid            Code = "provider-invalid"
	CodeModelsUnsupported          Code = "models-unsupported"
	CodeModelsUnavailable          Code = "models-unavailable"
	CodeModelsTimeout              Code = "models-timeout"
	CodeCapabilitiesRequestInvalid Code = "capabilities-request-invalid"
)

// CapabilitiesRequest selects a provider or one exact Agent by name or uid:.
// Exactly one field must be set. No active Pane is inferred.
type CapabilitiesRequest struct {
	Provider string
	Agent    string
}

// Capabilities returns provider support or exact Agent registry/local-helper
// evidence. LiveVerified remains false; control commands revalidate authority.
func (c *Client) Capabilities(ctx context.Context, input CapabilitiesRequest) (core.Capabilities, error) {
	if err := ctx.Err(); err != nil {
		return core.Capabilities{}, err
	}
	if strings.TrimSpace(input.Provider) == "" && strings.TrimSpace(input.Agent) == "" {
		return core.Capabilities{}, &Refusal{Code: CodeCapabilitiesRequestInvalid, message: "agent capabilities requires an explicit Agent reference or provider"}
	}
	if strings.TrimSpace(input.Provider) != "" && strings.TrimSpace(input.Agent) != "" {
		return core.Capabilities{}, &Refusal{Code: CodeCapabilitiesRequestInvalid, message: "agent capabilities accepts an Agent reference or --provider, not both"}
	}
	var projection agentcatalog.Capabilities
	if strings.TrimSpace(input.Provider) != "" {
		id, err := agentcatalog.Provider("agent capabilities", input.Provider)
		if err != nil {
			return core.Capabilities{}, catalogRefusal(err)
		}
		projection = agentcatalog.Static(id)
	} else {
		if c.pathsErr != nil {
			return core.Capabilities{}, resourceRefusal(c.pathsErr)
		}
		registry, err := resources.Load(c.paths)
		if err != nil {
			return core.Capabilities{}, resourceRefusal(err)
		}
		agent, err := agentcatalog.ResolveAgent(registry, input.Agent)
		if err != nil {
			return core.Capabilities{}, resourceRefusal(err)
		}
		id, err := agentcatalog.AgentProvider(agent)
		if err != nil {
			return core.Capabilities{}, catalogRefusal(err)
		}
		projection = agentcatalog.DefaultProbes().Project(registry, agent, id, store.PathFor(c.paths.StateDir))
	}
	if err := ctx.Err(); err != nil {
		return core.Capabilities{}, err
	}
	var out core.Capabilities
	bytes, err := json.Marshal(projection)
	if err == nil {
		err = json.Unmarshal(bytes, &out)
	}
	return out, err
}

// Models returns Claude's static list (empty provider defaults to Claude) or
// Codex's suggestions from the existing endpoint, bounded to 15 seconds.
func (c *Client) Models(ctx context.Context, provider string) (core.ModelSuggestions, error) {
	if provider == "" {
		provider = "claude"
	}
	result, err := agentcatalog.Models(ctx, provider, c.codexModelLister)
	if err != nil {
		return core.ModelSuggestions{}, catalogRefusal(err)
	}
	return core.ModelSuggestions{Provider: string(result.Provider), Models: result.Models, AcceptsUnlisted: result.AcceptsUnlisted}, nil
}
func catalogRefusal(err error) error {
	var failure *agentcatalog.Failure
	if errors.As(err, &failure) {
		return &Refusal{Code: Code(failure.Code), message: failure.Error()}
	}
	return err
}
