package projmux

import (
	"context"
	"errors"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeProcessOwnerMarkStale   Code = mutations.ProcessOwnerMarkStale
	CodeProcessOwnerMarkForeign Code = mutations.ProcessOwnerMarkForeign
)

// MarkProcessOwner writes the process-owner label on one process Agent, only
// while req.Binding is that Agent's current process activation, in one locked
// Registry update. The same owner succeeds unchanged, a different non-empty
// owner is refused with CodeProcessOwnerMarkForeign unless ReplaceForeign is
// set, and a binding that is not current (replaced, retired, tmux or unknown
// shape) is refused with CodeProcessOwnerMarkStale. A missing Agent is
// CodeResourceNotFound. Refusals carry AgentUID and leave the Registry
// unchanged. It performs no runtime calls and writes no journal.
func (c *Client) MarkProcessOwner(ctx context.Context, req core.MarkProcessOwnerRequest) (core.MarkProcessOwnerResult, error) {
	var out core.MarkProcessOwnerResult
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if strings.TrimSpace(req.Owner) == "" {
		return out, &Refusal{Code: CodeSelectorInvalid, Field: "MarkProcessOwnerRequest.Owner", message: "MarkProcessOwnerRequest.Owner must not be empty"}
	}
	b := req.Binding
	for _, f := range []struct{ name, value string }{
		{"HostInstanceID", b.HostInstanceID}, {"ProjectUID", b.ProjectUID}, {"WindowUID", b.WindowUID}, {"AgentUID", b.AgentUID},
		{"PaneUID", b.PaneUID}, {"Generation", b.Generation}, {"OperationID", b.OperationID},
	} {
		if f.value == "" {
			field := "MarkProcessOwnerRequest.Binding." + f.name
			return out, &Refusal{Code: CodeSelectorInvalid, Field: field, message: field + " is required"}
		}
	}
	if c.pathsErr != nil {
		return out, resourceRefusal(c.pathsErr)
	}
	var binding metadata.ProcessBinding
	if err := unregisterCopy(b, &binding); err != nil {
		return out, err
	}
	result, err := mutations.MarkProcessOwner(ctx, mutations.NewStore(store.PathFor(c.paths.StateDir)), binding, req.Owner, req.ReplaceForeign)
	if err != nil {
		var f *mutations.Failure
		if errors.As(err, &f) {
			return out, &Refusal{Code: Code(f.Code), AgentUID: f.AgentUID, message: f.Error(), cause: err}
		}
		return out, renameRefusal(err)
	}
	value, err := publicResource(result.Registry, metadata.KindAgent, binding.AgentUID)
	if err != nil {
		return out, err
	}
	out.Agent = value.(core.Agent)
	out.Changed = result.Changed
	out.Previous = result.Previous
	return out, nil
}
