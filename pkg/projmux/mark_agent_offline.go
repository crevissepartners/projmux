package projmux

import (
	"context"
	"errors"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeMarkAgentOfflineProcessUnsupported      Code = mutationruntime.MarkOfflineProcessUnsupported
	CodeMarkAgentOfflineOwnerPresent            Code = mutationruntime.MarkOfflineOwnerPresent
	CodeMarkAgentOfflineNotHostLost             Code = mutationruntime.MarkOfflineNotHostLost
	CodeMarkAgentOfflineConversationUnavailable Code = mutationruntime.MarkOfflineConversationUnavailable
	CodeMarkAgentOfflineStale                   Code = mutationruntime.MarkOfflineStale
)

// MarkAgentOffline shares the CLI agent mark-offline executor. It uses only
// frozen explicit Client paths, never a CLI subprocess, and starts or signals
// no provider. Both recorded identities must be proven absent by the kernel.
func (c *Client) MarkAgentOffline(ctx context.Context, req core.MarkAgentOfflineRequest) (core.MarkAgentOfflineResult, error) {
	var result core.MarkAgentOfflineResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if strings.TrimSpace(req.Ref) == "" {
		return result, &Refusal{Code: CodeSelectorInvalid, message: "MarkAgentOfflineRequest.Ref must name one Agent"}
	}
	query, err := mutationQuery(core.KindAgent, req.Ref, req.Project, req.Window, "MarkAgentOfflineRequest")
	if err != nil {
		return result, resourceRefusal(err)
	}
	if c.pathsErr != nil {
		return result, resourceRefusal(c.pathsErr)
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return result, resourceRefusal(err)
	}
	resolution, err := resources.Resolve(reg, metadata.RuntimeObservation{}, nil, metadata.KindAgent, query)
	if err != nil {
		return result, resourceRefusal(err)
	}
	if err = resources.Enforce(selector.Target{Verb: selector.VerbReview, Kind: metadata.KindAgent}, query, resolution); err != nil {
		return result, resourceRefusal(err)
	}
	result.AgentUID = resolution.Matches[0].UID
	if pane, ambiguous := mutationruntime.ProcessAgentPane(reg, result.AgentUID); pane != nil && !ambiguous && pane.Status.ProcessSession != nil && req.Binding != (core.ProcessBinding{}) {
		var recorded core.ProcessBinding
		if err = unregisterCopy(pane.Status.ProcessSession.Binding, &recorded); err != nil {
			return result, err
		}
		if recorded != req.Binding {
			result.PaneUID = pane.Metadata.UID
			result.Binding = recorded
			return result, &Refusal{Code: CodeMarkAgentOfflineStale, AgentUID: result.AgentUID, message: mutationruntime.MarkOfflineStale + ": recorded generation differs from the requested Binding"}
		}
	}
	storage := store.NewStore(store.PathFor(c.paths.StateDir))
	marker := mutationruntime.ProcessAgentMarkOffline{Update: storage.UpdateConvergent, Mutator: store.DefaultMutator()}
	err = marker.MarkExact(ctx, reg, result.AgentUID)
	result.PaneUID, result.Changed = marker.PaneUID, marker.Changed
	if copyErr := errors.Join(unregisterCopy(marker.Binding, &result.Binding), unregisterCopy(marker.Termination, &result.Termination)); copyErr != nil {
		return result, errors.Join(err, copyErr)
	}
	if err != nil {
		return result, markAgentOfflineRefusal(err, result)
	}
	if err = unregisterCopy(mutationruntime.MarkOfflineReceipt(marker.Latest, result.AgentUID, result.PaneUID, result.Changed), &result.Receipt); err != nil {
		return result, err
	}
	return result, nil
}

func markAgentOfflineRefusal(err error, result core.MarkAgentOfflineResult) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var refusal *mutationruntime.MarkOfflineRefusal
	if errors.As(err, &refusal) {
		return &Refusal{Code: Code(refusal.Code), AgentUID: result.AgentUID, message: err.Error(), cause: err}
	}
	return resourceRefusal(err)
}
