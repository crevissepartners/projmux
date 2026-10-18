package projmux

import (
	"context"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// SetAgentProtection changes only the reserved deletion protection label. True
// stores "true"; false removes the key, including legacy values. Session
// authorization and consumer mutation locks remain the caller's responsibility.
// It performs no runtime calls. Cancellation is honored before the locked write.
func (c *Client) SetAgentProtection(ctx context.Context, req core.SetAgentProtectionRequest) (core.SetAgentProtectionResult, error) {
	var out core.SetAgentProtectionResult
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if strings.TrimSpace(req.Ref) == "" {
		return out, &Refusal{Code: CodeSelectorInvalid, message: "SetAgentProtectionRequest.Ref must name one Agent"}
	}
	query, err := mutationQuery(core.KindAgent, req.Ref, req.Project, req.Window, "SetAgentProtectionRequest")
	if err != nil {
		return out, resourceRefusal(err)
	}
	if query.Agents[0].UID == "" && req.Project == "" && req.Window == "" {
		return out, &Refusal{Code: CodeSelectorInvalid, message: "SetAgentProtectionRequest.Ref requires uid: or explicit Project/Window scope"}
	}
	if c.pathsErr != nil {
		return out, resourceRefusal(c.pathsErr)
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return out, resourceRefusal(err)
	}
	resolved, err := resources.Resolve(reg, metadata.RuntimeObservation{}, nil, metadata.KindAgent, query)
	if err != nil {
		return out, resourceRefusal(err)
	}
	if err := resources.Enforce(selector.Target{Verb: selector.VerbLabel, Kind: metadata.KindAgent}, query, resolved); err != nil {
		return out, resourceRefusal(err)
	}
	var set map[string]string
	var remove []string
	if req.Protected {
		set = map[string]string{metadata.LabelProtected: "true"}
	} else {
		remove = []string{metadata.LabelProtected}
	}
	result, err := mutations.SetLabels(ctx, mutations.NewStore(store.PathFor(c.paths.StateDir)), metadata.KindAgent, resolved.Matches[0].UID, set, remove)
	if err != nil {
		return out, renameRefusal(err)
	}
	value, err := publicResource(result.Registry, metadata.KindAgent, resolved.Matches[0].UID)
	if err != nil {
		return out, err
	}
	out.Agent = value.(core.Agent)
	out.Changed = result.Changed
	return out, nil
}
