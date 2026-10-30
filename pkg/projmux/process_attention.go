package projmux

import (
	"context"
	"errors"

	"github.com/crevissepartners/projmux/internal/services/processattention"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// CodeProcessAttentionDamaged refuses a read of a process attention store that
// is present but not a record map. The read changed nothing: the store is
// repaired only by its writer.
const CodeProcessAttentionDamaged Code = "process-attention-damaged"

// ErrProcessAttentionDamaged is the sentinel a CodeProcessAttentionDamaged
// refusal reaches through Unwrap. It is the same error the CLI classifies a
// damaged store with.
var ErrProcessAttentionDamaged = processattention.ErrDamaged

// readProcessAttentionStore is the one store read of ReadProcessAttention.
var readProcessAttentionStore = processattention.ReadStore

// ReadProcessAttention returns the badge of every process Agent that has one,
// in one call whose cost does not grow with the number of Agents: one read of
// the process attention store and, when it holds any record, one read of the
// Registry. It never reads an owner. A missing or empty store is an empty map
// and no Registry read; a damaged one is a CodeProcessAttentionDamaged refusal.
//
// An Agent is listed when its current declaration has a record of the same
// binding and provider, a nonempty generation and no Terminal mark, and the
// record shows in_progress, approval_required, input_required or
// response_complete. See core.ProcessAttention for how this differs from
// AgentProcessSnapshot.Attention.
func (c *Client) ReadProcessAttention(ctx context.Context, _ core.ProcessAttentionRequest) (core.ProcessAttention, error) {
	out := core.ProcessAttention{Agents: map[string]core.ProcessAttentionBadge{}}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if c.pathsErr != nil {
		return out, resourceRefusal(c.pathsErr)
	}
	records, err := readProcessAttentionStore(c.paths.StateDir)
	if errors.Is(err, processattention.ErrDamaged) {
		return out, &Refusal{Code: CodeProcessAttentionDamaged, message: "process attention store is damaged: " + err.Error(), cause: errors.Join(ErrProcessAttentionDamaged, err)}
	}
	if err != nil {
		return out, err
	}
	if len(records) == 0 {
		return out, nil
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return out, resourceRefusal(err)
	}
	declared := processattention.Declarations(reg)
	for _, binding := range declared.Bindings {
		r, ok := records[binding.Pane]
		if !ok || r.Binding != binding || binding.Generation == "" || r.Provider != declared.Providers[binding.Pane] || r.Terminal {
			continue
		}
		if badge, shown := publicProcessAttentionBadge(r.Effective()); shown {
			out.Agents[binding.Agent] = badge
		}
	}
	return out, nil
}

func publicProcessAttentionBadge(badge string) (core.ProcessAttentionBadge, bool) {
	switch b := core.ProcessAttentionBadge(badge); b {
	case core.ProcessAttentionInProgress, core.ProcessAttentionApprovalRequired, core.ProcessAttentionInputRequired, core.ProcessAttentionResponseComplete:
		return b, true
	}
	return "", false
}
