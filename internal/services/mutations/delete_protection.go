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
		return &Failure{Code: CodeAgentProtected, Cause: fmt.Errorf("%s: Agent %q uid=%s is protected; remove protection with: projmux label agent uid:%s %s-", CodeAgentProtected, agent.Metadata.Name, uid, uid, coremetadata.LabelProtected)}
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
