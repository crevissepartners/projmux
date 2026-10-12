package projmux

import (
	"context"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
	"strings"
)

// PreflightDelete checks Registry deletion protection without runtime access or
// writes, including process Agents and Project graphs. A frontend must call it
// before stopping a provider for deletion. It does not authorize later effects:
// execution must recheck protection under its admission lock.
func (c *Client) PreflightDelete(ctx context.Context, req core.DeleteRequest) (core.DeleteResult, error) {
	out := core.DeleteResult{}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if req.Kind != core.KindProject && req.Kind != core.KindWindow && req.Kind != core.KindPane && req.Kind != core.KindAgent {
		return out, &Refusal{Code: CodeDeleteKindUnsupported, message: "PreflightDelete requires Project, Window, Pane or Agent"}
	}
	if strings.TrimSpace(req.Ref) == "" {
		return out, &Refusal{Code: CodeSelectorInvalid, message: "DeleteRequest.Ref must name one resource"}
	}
	if c.pathsErr != nil {
		return out, resourceRefusal(c.pathsErr)
	}
	query, err := deleteQuery(req)
	if err != nil {
		return out, resourceRefusal(err)
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return out, resourceRefusal(err)
	}
	resolution, err := resources.Resolve(reg, metadata.RuntimeObservation{}, nil, metadata.Kind(req.Kind), query)
	if err != nil {
		return out, resourceRefusal(err)
	}
	if err := resources.Enforce(selector.Target{Verb: selector.VerbDelete, Kind: metadata.Kind(req.Kind)}, query, resolution); err != nil {
		return out, resourceRefusal(err)
	}
	plan := mutations.BuildDeletePlan(reg, metadata.Kind(req.Kind), resolution)
	if err := mutations.ValidateDeleteProtection(reg, plan); err != nil {
		return out, deleteRefusal(CodeAgentProtected, err)
	}
	return out, unregisterCopy(mutations.PreviewDelete(plan), &out.Plan)
}
