package runtime

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

const (
	ProcessResumeNotResumable = "process-resume-not-resumable"
	ProcessResumeOwned        = "process-resume-owned"
	ProcessResumeRefused      = "process-resume-refused"
)

type ProcessResumeFilter struct {
	Project, Window string
	Labels          map[string]string
}

type ProcessResumePrevious struct {
	InterruptedTurn string
	Expired         []coremetadata.ProcessRecordedControl
	MayBeTruncated  bool
}

type ProcessResumeCandidate struct {
	Agent    coremetadata.Agent
	Pane     coremetadata.Pane
	Record   coremetadata.ProcessSessionRecord
	Previous ProcessResumePrevious
	HostLost *coremetadata.ProcessActivation
}

// ListResumableProcessAgents is a read-only durable candidate projection. It
// neither probes a host nor promises that the provider will accept resume.
// The consumer must prove exact owner liveness outside the Registry lock and
// revalidate the selected candidate in its resume transaction.
func ListResumableProcessAgents(registry coremetadata.Registry, filter ProcessResumeFilter) []ProcessResumeCandidate {
	var out []ProcessResumeCandidate
	if registry.Validate() != nil {
		return out
	}
	for _, agent := range registry.Agents {
		if ProcessResumeCandidateToken(registry, agent.Metadata.UID) != "" || !processResumeMatches(registry, agent, filter) {
			continue
		}
		pane, _ := ProcessAgentPane(registry, agent.Metadata.UID)
		record := pane.Status.ProcessSession.Clone()
		previous := ProcessResumePrevious{InterruptedTurn: record.TurnID, Expired: slices.Clone(record.Pending)}
		if previous.InterruptedTurn == "" && len(previous.Expired) == 0 && record.History != nil {
			previous = ProcessResumePrevious{InterruptedTurn: record.History.InterruptedTurnID, Expired: slices.Clone(record.History.Expired)}
		}
		out = append(out, ProcessResumeCandidate{Agent: agent.Clone(), Pane: pane.Clone(), Record: *record, Previous: previous})
	}
	slices.SortFunc(out, func(a, b ProcessResumeCandidate) int {
		return strings.Compare(a.Agent.Metadata.UID, b.Agent.Metadata.UID)
	})
	return out
}

// ownerAlive is an exact, independently verified host observation, never an
// inference from a stored PID. A live owner wins over durable availability.
// Every refusal wraps ErrResumeRefused so provider rejection and preflight
// rejection share one classification without granting fallback authority.
func ProcessResumeRefusal(registry coremetadata.Registry, agentUID string, ownerAlive bool) error {
	token := ""
	switch {
	case ownerAlive:
		token = ProcessResumeOwned
	case registry.Validate() != nil:
		token = ProcessResumeRefused
	default:
		token = ProcessResumeCandidateToken(registry, agentUID)
	}
	if token != "" {
		return fmt.Errorf("%s: %w", token, processhost.ErrResumeRefused)
	}
	return nil
}

func ProcessResumeCandidateToken(registry coremetadata.Registry, agentUID string) string {
	agent, found := registry.Agent(agentUID)
	pane, ambiguous := ProcessAgentPane(registry, agentUID)
	if ambiguous {
		return ProcessResumeRefused
	}
	if !found || pane == nil || pane.Status.ProcessSession == nil || pane.Status.ProcessSession.ResumeState != coremetadata.ProcessResumable {
		return ProcessResumeNotResumable
	}
	if agent.Status.Phase != coremetadata.PhaseOffline || !pane.Status.Activation.IsZero() || pane.Status.ProcessSession.Binding.AgentUID != agentUID || (agent.Status.PaneRef != "" && agent.Status.PaneRef != pane.Metadata.UID) {
		return ProcessResumeRefused
	}
	return ""
}

func processResumeMatches(registry coremetadata.Registry, agent coremetadata.Agent, filter ProcessResumeFilter) bool {
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

// The projection is read-only. Its original activation and session are retained
// for the atomic reservation's CAS and second kernel check.
func HostLostResumeCandidate(reg coremetadata.Registry, uid string, read ProcessIdentityReader) (ProcessResumeCandidate, bool) {
	pane, ambiguous := ProcessAgentPane(reg, uid)
	if ambiguous || pane == nil || pane.Status.Activation.Process == nil || reg.Validate() != nil {
		return ProcessResumeCandidate{}, false
	}
	a := *pane.Status.Activation.Process
	if !ProcessActivationAbsent(a, read) {
		return ProcessResumeCandidate{}, false
	}
	projected := reg.Clone()
	p, _ := projected.Pane(pane.Metadata.UID)
	agent, _ := projected.Agent(uid)
	record := p.Status.ProcessSession
	if record == nil || record.Binding != a.Binding || record.ConnectionID != a.Binding.OperationID || (record.Provider == processProviderClaude && record.SessionID == "") || (record.Provider == processProviderCodex && record.ThreadID == "") {
		return ProcessResumeCandidate{}, false
	}
	p.Status.Activation = coremetadata.PaneActivation{}
	agent.Status.Phase = coremetadata.PhaseOffline
	record.ResumeState = coremetadata.ProcessResumable
	for _, candidate := range ListResumableProcessAgents(projected, ProcessResumeFilter{}) {
		if candidate.Agent.Metadata.UID == uid {
			candidate.Record = *pane.Status.ProcessSession.Clone()
			candidate.HostLost = &a
			candidate.Previous.MayBeTruncated = true
			return candidate, true
		}
	}
	return ProcessResumeCandidate{}, false
}

func ProcessResumeRecordEqual(a, b *coremetadata.ProcessSessionRecord) bool {
	if a == nil || b == nil {
		return a == b
	}
	// StopRequested is advisory, not resume or deferred-claim authority.
	// Keep every other identity, history member and pending control exact.
	left, right := a.Clone(), b.Clone()
	left.StopRequested, right.StopRequested = false, false
	return reflect.DeepEqual(left, right)
}
