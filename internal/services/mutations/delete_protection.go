package mutations

import (
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
)

const CodeAgentProtected = "agent-protected"

// ValidateDeleteProtection is a read-only preflight for every Agent the plan
// removes. Plain Pane deletion retains its Agent and is intentionally allowed.
// Callers must repeat this check with a freshly built plan under execution's
// Registry lock before admitting any Registry or runtime effect.
func ValidateDeleteProtection(reg coremetadata.Registry, plan DeletePlan) error {
	check := func(kind coremetadata.Kind, uid string) error {
		if kind != coremetadata.KindAgent {
			return nil
		}
		agent, ok := reg.Agent(uid)
		if !ok || !coremetadata.AgentProtected(*agent) {
			return nil
		}
		return &Failure{Code: CodeAgentProtected, AgentUID: uid, Cause: coremetadata.AgentProtectionError(*agent)}
	}
	for _, target := range plan.Targets {
		if err := check(plan.Kind, target.Match.UID); err != nil {
			return err
		}
		for _, child := range target.Descendants {
			if err := check(child.Kind, child.UID); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateDeleteTargets rebuilds cascades from the supplied current snapshot.
func ValidateDeleteTargets(reg coremetadata.Registry, kind coremetadata.Kind, uids []string) error {
	resolution := selector.Resolution{Kind: kind}
	for _, uid := range uids {
		resolution.Matches = append(resolution.Matches, selector.Match{UID: uid})
	}
	return ValidateDeleteProtection(reg, BuildDeletePlan(reg, kind, resolution))
}

// ValidatePruneTargets adds recovery guidance to the common whole-plan refusal.
func ValidatePruneTargets(reg coremetadata.Registry, kind coremetadata.Kind, uids []string) error {
	if err := ValidateDeleteTargets(reg, kind, uids); err != nil {
		return fmt.Errorf("%w; narrow the selected scope or remove protection before retrying", err)
	}
	return nil
}
