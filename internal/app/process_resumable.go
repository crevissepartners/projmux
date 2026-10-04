package app

import (
	"fmt"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

const (
	processResumeNotResumable = "process-resume-not-resumable"
	processResumeOwned        = "process-resume-owned"
	processResumeRefused      = "process-resume-refused"
)

type processResumeFilter struct {
	Project, Window string
	Labels          map[string]string
}

type processResumePrevious struct {
	InterruptedTurn string
	Expired         []coremetadata.ProcessRecordedControl
}

type processResumeCandidate struct {
	Agent    coremetadata.Agent
	Pane     coremetadata.Pane
	Record   coremetadata.ProcessSessionRecord
	Previous processResumePrevious
}

// listResumableProcessAgents is a read-only durable candidate projection. It
// neither probes a host nor promises that the provider will accept resume.
// The consumer must prove exact owner liveness outside the Registry lock and
// revalidate the selected candidate in its resume transaction.
func listResumableProcessAgents(registry coremetadata.Registry, filter processResumeFilter) []processResumeCandidate {
	var out []processResumeCandidate
	if registry.Validate() != nil {
		return out
	}
	for _, agent := range registry.Agents {
		if processResumeCandidateToken(registry, agent.Metadata.UID) != "" || !processResumeMatches(registry, agent, filter) {
			continue
		}
		pane, _ := processResumePane(registry, agent.Metadata.UID)
		record := pane.Status.ProcessSession.Clone()
		out = append(out, processResumeCandidate{Agent: agent.Clone(), Pane: pane.Clone(), Record: *record, Previous: processResumePrevious{InterruptedTurn: record.TurnID, Expired: slices.Clone(record.Pending)}})
	}
	slices.SortFunc(out, func(a, b processResumeCandidate) int {
		return strings.Compare(a.Agent.Metadata.UID, b.Agent.Metadata.UID)
	})
	return out
}

// ownerAlive is an exact, independently verified host observation, never an
// inference from a stored PID. A live owner wins over durable availability.
// Every refusal wraps ErrResumeRefused so provider rejection and preflight
// rejection share one classification without granting fallback authority.
func processResumeRefusal(registry coremetadata.Registry, agentUID string, ownerAlive bool) error {
	token := ""
	switch {
	case ownerAlive:
		token = processResumeOwned
	case registry.Validate() != nil:
		token = processResumeRefused
	default:
		token = processResumeCandidateToken(registry, agentUID)
	}
	if token != "" {
		return fmt.Errorf("%s: %w", token, processhost.ErrResumeRefused)
	}
	return nil
}

func processResumeCandidateToken(registry coremetadata.Registry, agentUID string) string {
	agent, found := registry.Agent(agentUID)
	pane, ambiguous := processResumePane(registry, agentUID)
	if ambiguous {
		return processResumeRefused
	}
	if !found || pane == nil || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.ResumeState != coremetadata.ProcessResumable {
		return processResumeNotResumable
	}
	if agent.Status.Phase != coremetadata.PhaseOffline || !pane.Status.Activation.IsZero() || pane.Status.ProcessSession.Binding.AgentUID != agentUID || (agent.Status.PaneRef != "" && agent.Status.PaneRef != pane.Metadata.UID) {
		return processResumeRefused
	}
	return ""
}

func processResumePane(registry coremetadata.Registry, agentUID string) (*coremetadata.Pane, bool) {
	var found *coremetadata.Pane
	for i := range registry.Panes {
		pane := &registry.Panes[i]
		if pane.Metadata.OwnerUID() == agentUID && pane.Spec.Runtime.EffectiveKind() == coremetadata.RuntimeProcess {
			if found != nil {
				return nil, true // Ambiguous retired generations cannot select authority.
			}
			found = pane
		}
	}
	return found, false
}

func processResumeMatches(registry coremetadata.Registry, agent coremetadata.Agent, filter processResumeFilter) bool {
	window, ok := registry.Window(agent.Metadata.OwnerUID())
	if !ok || (filter.Window != "" && window.Metadata.UID != filter.Window) || (filter.Project != "" && window.Metadata.OwnerUID() != filter.Project) {
		return false
	}
	for key, value := range filter.Labels {
		if actual, ok := agent.Metadata.Labels[key]; !ok || actual != value {
			return false
		}
	}
	return true
}
