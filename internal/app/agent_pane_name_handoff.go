package app

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// agentPaneNameHandoff is the verdict of selectAgentPaneNameHandoff: the
// metadata.name a new managed Agent Pane carries over from one of that Agent's
// released old Pane rows.
type agentPaneNameHandoff struct {
	// name is the carried non-automatic name. Empty means the new Pane gets
	// the automatic name its minted UID gives it.
	name string
	// sourceUID is the old Pane row whose name is carried. The caller releases
	// that row before attaching the new Pane, so the only Pane holding the name
	// afterwards is the new one.
	sourceUID string
	// reason says why a non-automatic name exists but is not carried. It is
	// empty whenever name is set, and also when there was simply nothing to
	// carry.
	reason string
}

// agentPaneNameCandidate reports whether pane is an old Pane row whose name a
// new managed Pane of agentUID may carry: an Agent-owned `agent` role Pane of
// that exact Agent whose name is not its own automatic UID name.
func agentPaneNameCandidate(pane coremetadata.Pane, agentUID string) bool {
	owner := pane.Metadata.OwnerRef
	return owner != nil && owner.Kind == coremetadata.KindAgent && owner.UID == agentUID &&
		pane.Spec.Role == coremetadata.PaneRoleAgent &&
		pane.Metadata.Name != "" && pane.Metadata.Name != pane.Metadata.UID
}

// selectAgentPaneNameHandoff is the single name-selection rule shared by
// Continue topology replay and `agent resume`.
//
// nonLive lists the Agent's old Pane row uids the caller has shown are live
// nowhere; the caller also owns releasing the selected row under that proof.
// Only those rows are candidates, and a row whose name is its own UID is
// automatic and never a candidate. One candidate is carried. Several candidates
// are resolved only by the Agent's last termination receipt Pane; when that
// does not select exactly one, no name is carried. A name the Registry reserves
// for a resource other than the selected row is never carried either. Every
// branch that leaves a non-automatic name behind explains itself in reason, and
// none of them refuses the caller.
func selectAgentPaneNameHandoff(registry coremetadata.Registry, agent coremetadata.Agent, nonLive []string) agentPaneNameHandoff {
	var candidates []coremetadata.Pane
	for _, uid := range nonLive {
		pane, ok := registry.Pane(uid)
		if !ok || !agentPaneNameCandidate(*pane, agent.Metadata.UID) ||
			slices.ContainsFunc(candidates, func(seen coremetadata.Pane) bool { return seen.Metadata.UID == uid }) {
			continue
		}
		candidates = append(candidates, pane.Clone())
	}
	if len(candidates) == 0 {
		return agentPaneNameHandoff{}
	}
	chosen := candidates[0]
	if len(candidates) > 1 {
		receiptPaneUID := ""
		if receipt := agent.Status.LastTermination; receipt != nil {
			receiptPaneUID = strings.TrimSpace(receipt.PaneUID)
		}
		index := slices.IndexFunc(candidates, func(pane coremetadata.Pane) bool {
			return receiptPaneUID != "" && pane.Metadata.UID == receiptPaneUID
		})
		if index < 0 {
			described := make([]string, 0, len(candidates))
			for _, pane := range candidates {
				described = append(described, fmt.Sprintf("pane/%s (uid:%s)", pane.Metadata.Name, pane.Metadata.UID))
			}
			return agentPaneNameHandoff{reason: fmt.Sprintf(
				"old Panes %s carry names and the last termination receipt selects none of them",
				strings.Join(described, ", "))}
		}
		chosen = candidates[index]
	}
	if holder, taken := registry.PaneNameHolder(agent.Metadata.UID, chosen.Metadata.Name); taken && holder != chosen.Metadata.UID {
		return agentPaneNameHandoff{reason: fmt.Sprintf("name %q is reserved by %s", chosen.Metadata.Name, holder)}
	}
	return agentPaneNameHandoff{name: chosen.Metadata.Name, sourceUID: chosen.Metadata.UID}
}

// agentPaneNameNotice renders the one diagnostic line both callers emit when a
// name could not be carried.
func agentPaneNameNotice(label, reason string) string {
	return fmt.Sprintf("projmux: agent/%s new Pane keeps an automatic name: %s", label, reason)
}

// attachAgentPaneWithName attaches the new managed Pane under the carried name.
// If the Registry still refuses that name at the mutation, the Pane is attached
// under its automatic name instead and the refusal comes back as a reason: a
// name is never a reason to fail the materialization.
func attachAgentPaneWithName(registry *coremetadata.Registry, mutator coremetadata.Mutator, agentUID, cwd, name, operationID string) (coremetadata.Pane, string, error) {
	pane, err := mutator.AttachAgentPane(registry, agentUID, coremetadata.BootstrapPane{CWD: cwd, Name: name}, operationID)
	if err == nil || name == "" ||
		(!errors.Is(err, coremetadata.ErrNameConflict) && !errors.Is(err, coremetadata.ErrInvalidName)) {
		return pane, "", err
	}
	pane, retryErr := mutator.AttachAgentPane(registry, agentUID, coremetadata.BootstrapPane{CWD: cwd}, operationID)
	if retryErr != nil {
		return pane, "", retryErr
	}
	return pane, err.Error(), nil
}
