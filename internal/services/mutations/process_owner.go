package mutations

import (
	"context"
	"fmt"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// Process owner mark refusal codes.
const (
	ProcessOwnerMarkStale   = "process-owner-mark-stale"
	ProcessOwnerMarkForeign = "process-owner-mark-foreign"
)

// ProcessOwnerResult holds the metadata and snapshot from the same locked
// commit. Previous is the label value before the call, whether or not it changed.
type ProcessOwnerResult struct {
	Registry metadata.Registry
	Changed  bool
	Previous string
}

// MarkProcessOwner writes process-owner=owner only while binding is the
// Agent's current process activation. The activation check, the comparison with
// the existing value and the write share one Registry update, so a refusal
// leaves the Registry untouched.
func MarkProcessOwner(ctx context.Context, s Store, binding metadata.ProcessBinding, owner string, replaceForeign bool) (ProcessOwnerResult, error) {
	var result ProcessOwnerResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if s.UpdateConvergentContext == nil || s.Mutator == nil {
		return result, Classify(fmt.Errorf("resource registry store is not configured"))
	}
	callbackEntered := false
	committed, _, err := s.UpdateConvergentContext(ctx, func(working *metadata.Registry) error {
		callbackEntered = true
		if err := ctx.Err(); err != nil {
			return err
		}
		agent, ok := working.Agent(binding.AgentUID)
		if !ok {
			return &Failure{Code: "resource-not-found", AgentUID: binding.AgentUID, Cause: fmt.Errorf("agent %q disappeared before the mutation ran", binding.AgentUID)}
		}
		if _, _, current := working.CurrentProcessActivation(binding); !current {
			return &Failure{Code: ProcessOwnerMarkStale, AgentUID: binding.AgentUID, Cause: fmt.Errorf("%s: the process activation is not current", ProcessOwnerMarkStale)}
		}
		result.Previous = agent.Metadata.Labels[metadata.LabelProcessOwner]
		if result.Previous != "" && result.Previous != owner && !replaceForeign {
			return &Failure{Code: ProcessOwnerMarkForeign, AgentUID: binding.AgentUID, Cause: fmt.Errorf("%s: another owner already marked this Agent", ProcessOwnerMarkForeign)}
		}
		var err error
		_, result.Changed, err = s.Mutator().SetLabels(working, metadata.KindAgent, binding.AgentUID, map[string]string{metadata.LabelProcessOwner: owner}, nil)
		return err
	})
	if err != nil {
		if !callbackEntered {
			if cancelErr := ctx.Err(); cancelErr != nil {
				return ProcessOwnerResult{}, cancelErr
			}
		}
		return ProcessOwnerResult{}, Classify(err)
	}
	result.Registry = committed.Clone()
	return result, nil
}
