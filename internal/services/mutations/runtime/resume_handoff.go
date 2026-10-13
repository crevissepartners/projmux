package runtime

import (
	"context"
	"fmt"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// HandOffResumedPaneName chooses the name the resumed Agent's new Pane carries
// and releases the old Pane row that held it.
//
// A restart's stopped Pane comes first: its name is carried once the stop has
// dropped its row.
//
// Resume keeps an Offline Agent's old Pane rows as evidence, and each row still
// holds its name reservation. The candidates are the rows proven live nowhere
// by the same server-wide owner inventory Continue replay uses; only the
// selected source row is released, and only when releasing it leaves the
// Window's stored anchor where it is. Every outcome that
// carries no non-automatic name comes back as a reason instead of an error.
//
// When there is neither a stopped Pane nor any candidate row -- a resume after
// `delete pane` dropped the old row -- the name is DerivedAgentPaneName's
// `<agent>-pane`, the one `create agent` gives. A candidate row that cannot be
// carried keeps its reason and the automatic name.
func (r *AgentResumeExecution) HandOffResumedPaneName(
	ctx context.Context,
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	agent coremetadata.Agent,
	plan AgentResumePlan,
) (string, string) {
	if stopped := plan.StoppedPane; stopped.Name != "" {
		if _, ok := working.Pane(stopped.UID); !ok {
			// The restart's stop dropped the row and released its name, so
			// no old row holds it. A holder that took it since is disclosed
			// by attachAgentPaneWithName.
			return stopped.Name, ""
		}
	}
	var owned []coremetadata.Pane
	named := false
	for _, pane := range working.PanesOf(agent.Metadata.UID) {
		if pane.Metadata.UID == agent.Status.PaneRef {
			continue
		}
		owned = append(owned, pane)
		named = named || AgentPaneNameCandidate(pane, agent.Metadata.UID)
	}
	if !named {
		// Nothing could be carried, so no runtime inventory is taken. With no
		// restart behind it either, the new Pane gets the name `create agent`
		// gives an Agent's Pane; attachAgentPaneWithName discloses a holder
		// or an invalid name and falls back to the automatic one.
		if plan.StoppedPane.UID == "" {
			return DerivedAgentPaneName(agent.Metadata.Name), ""
		}
		return "", ""
	}
	guard, err := NewTopologyOwnerGuard(ctx, r.Execution.Runtime)
	if err != nil {
		return "", fmt.Sprintf("old Pane rows could not be proven live nowhere on this socket: %v", err)
	}
	var nonLive []string
	var liveCandidate error
	for _, pane := range owned {
		if claimErr := RequireSoleUIDClaim("pane", plan.AgentName, pane.Metadata.UID, "", guard.PaneUIDs[pane.Metadata.UID]); claimErr != nil {
			if liveCandidate == nil && AgentPaneNameCandidate(pane, agent.Metadata.UID) {
				liveCandidate = claimErr
			}
			continue
		}
		nonLive = append(nonLive, pane.Metadata.UID)
	}
	handoff := SelectAgentPaneNameHandoff(*working, agent, nonLive)
	if handoff.Name == "" {
		if handoff.Reason == "" && liveCandidate != nil {
			return "", liveCandidate.Error()
		}
		return "", handoff.Reason
	}
	window, ok := working.Window(plan.WindowUID)
	if !ok {
		return "", fmt.Sprintf("window %q is not in the Registry", plan.WindowUID)
	}
	anchor := window.Spec.AnchorPaneRef
	before := working.Clone()
	if err := mutator.DeletePane(working, handoff.SourceUID); err != nil {
		*working = before
		return "", fmt.Sprintf("old pane/%s (uid:%s) could not be released: %v", handoff.Name, handoff.SourceUID, err)
	}
	// The rebind splits the Window's stored anchor, so a release that re-anchored
	// the Window would turn a name into a refused resume. Keep the row instead.
	if window, ok := working.Window(plan.WindowUID); !ok || window.Spec.AnchorPaneRef != anchor {
		*working = before
		return "", fmt.Sprintf("releasing old pane/%s (uid:%s) would move window %s's anchor", handoff.Name, handoff.SourceUID, plan.WindowUID)
	}
	return handoff.Name, ""
}
