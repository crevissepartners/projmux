package metadata

import (
	"crypto/rand"
	"fmt"
	"strings"
	"time"
)

// generationEntropyBytes is the random payload behind one activation
// generation. It is the same width as a uid payload: a generation is compared
// for exact equality by an independently running supervisor, so a value that
// could be guessed or replayed would let a stale process overwrite the current
// binding's evidence.
const generationEntropyBytes = 16

// NewGeneration mints one opaque activation generation.
//
// A generation names one *materialization* of a Pane, not the Pane. The uid
// survives kill/recreate and resume; the generation does not, and that is the
// whole point: a receipt carries the generation it was launched with, so a
// receipt from the process a resume replaced can be recognized as stale instead
// of being applied to the Pane that now holds the uid.
func NewGeneration() (string, error) {
	buf := make([]byte, generationEntropyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("metadata: read activation generation entropy: %w", err)
	}
	return "gen-" + strings.ToLower(uidEncoding.EncodeToString(buf)), nil
}

// TerminationSource is the closed provenance vocabulary of one receipt.
//
// It is deliberately not caller-supplied free text. Provenance decides whether
// a receipt may claim intent, and an open string here would let anything that
// can write the registry manufacture "the operator asked for this".
type TerminationSource string

const (
	// TerminationSourceSupervisor is the managed process supervisor that
	// actually reaped the child and read its wait status.
	TerminationSourceSupervisor TerminationSource = "supervisor"
	// TerminationSourceControlAction is a canonical control-plane lifecycle
	// action recording its own intent before it mutates anything live.
	TerminationSourceControlAction TerminationSource = "control-action"
)

// ValidTerminationSource reports whether source is in the closed set.
func ValidTerminationSource(source TerminationSource) bool {
	switch source {
	case TerminationSourceSupervisor, TerminationSourceControlAction:
		return true
	default:
		return false
	}
}

// TerminationClassification is the closed evidence vocabulary.
//
// The four values are not a severity ladder, they are four different *kinds of
// proof*. Intentional means a canonical control action said so in writing
// before it acted. Normal and Abnormal mean a supervisor actually reaped the
// child and read its wait status. Unknown means nothing proved anything, and it
// is a legal, expected answer rather than a failure.
type TerminationClassification string

const (
	// TerminationIntentional is a canonical control-plane action's own record
	// of its intent. Only TerminationSourceControlAction may carry it.
	TerminationIntentional TerminationClassification = "intentional"
	// TerminationNormal is an observed exit status 0.
	//
	// It is emphatically NOT intent. A provider that exits 0 because the
	// operator typed a quit command and a provider that exits 0 because it
	// finished a batch produce byte-identical wait statuses, so promoting
	// exit 0 to "intentional" would invent evidence nobody produced.
	TerminationNormal TerminationClassification = "normal"
	// TerminationAbnormal is an observed non-zero exit status or a death by
	// signal.
	TerminationAbnormal TerminationClassification = "abnormal"
	// TerminationUnknown is an explicitly evidence-free record. Nothing in
	// this phase writes it; it exists so a later consumer has a vocabulary for
	// "the process is gone and no receipt explains why" that is a value rather
	// than an absence being re-read as normality.
	TerminationUnknown TerminationClassification = "unknown"
)

// ValidTerminationClassification reports whether classification is in the
// closed set.
func ValidTerminationClassification(classification TerminationClassification) bool {
	switch classification {
	case TerminationIntentional, TerminationNormal, TerminationAbnormal, TerminationUnknown:
		return true
	default:
		return false
	}
}

// ClassifyProcessExit maps one reaped wait status onto observed evidence.
//
// signal is the empty string for a child that exited on its own. A signalled
// child is abnormal regardless of the code the platform reports alongside it,
// and exit status 0 is the only normal outcome.
func ClassifyProcessExit(exitCode int, signal string) TerminationClassification {
	if strings.TrimSpace(signal) != "" {
		return TerminationAbnormal
	}
	if exitCode == 0 {
		return TerminationNormal
	}
	return TerminationAbnormal
}

// PaneActivation binds one Pane materialization to an opaque generation.
//
// RuntimeID is recorded for operator diagnostics only. tmux recycles `%N`
// handles, so the generation -- not the handle -- is what a receipt is matched
// against.
type PaneActivation struct {
	Generation  string    `json:"generation"`
	RuntimeID   string    `json:"runtimeID,omitempty"`
	AgentUID    string    `json:"agentUID,omitempty"`
	OperationID string    `json:"operationID,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitzero"`
}

// IsZero lets registry documents written before activation generations existed
// re-encode without the additive block.
func (a PaneActivation) IsZero() bool {
	return a.Generation == "" && a.RuntimeID == "" && a.AgentUID == "" &&
		a.OperationID == "" && a.StartedAt.IsZero()
}

// TerminationEvidence is the minimal durable record of why one managed process
// stopped.
//
// It is a pointer field with omitempty everywhere it is stored, which is the
// entire read-compatibility story: a registry written before this field existed
// decodes to nil, a nil value re-encodes to an absent key, and the document
// round-trips byte-identically. It is additive inside schemaVersion 1 and needs
// no migration step -- bumping the envelope would make every already installed
// build reject the file fail-closed with ErrSchemaTooNew.
//
// It carries no command text, no pane content, and no provider conversation
// data. Everything here is either a closed vocabulary value, a uid this build
// minted, or a numeric wait status.
type TerminationEvidence struct {
	Source         TerminationSource         `json:"source"`
	Classification TerminationClassification `json:"classification"`
	ObservedAt     time.Time                 `json:"observedAt,omitzero"`
	PaneUID        string                    `json:"paneUID,omitempty"`
	AgentUID       string                    `json:"agentUID,omitempty"`
	Generation     string                    `json:"generation,omitempty"`
	// ExitCode is a pointer so "exited with status 0" and "never exited on its
	// own" are different documents rather than the same zero value.
	ExitCode    *int   `json:"exitCode,omitempty"`
	Signal      string `json:"signal,omitempty"`
	OperationID string `json:"operationID,omitempty"`
}

// IsZero reports an entirely empty receipt.
func (t TerminationEvidence) IsZero() bool {
	return t.Source == "" && t.Classification == "" && t.ObservedAt.IsZero() &&
		t.PaneUID == "" && t.AgentUID == "" && t.Generation == "" &&
		t.ExitCode == nil && t.Signal == "" && t.OperationID == ""
}

// Clone returns a deep copy, including the exit-code pointer.
func (t *TerminationEvidence) Clone() *TerminationEvidence {
	if t == nil {
		return nil
	}
	out := *t
	if t.ExitCode != nil {
		code := *t.ExitCode
		out.ExitCode = &code
	}
	return &out
}

// sameEvidence reports whether two receipts say exactly the same thing.
func sameEvidence(a, b *TerminationEvidence) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	}
	if a.Source != b.Source || a.Classification != b.Classification ||
		a.PaneUID != b.PaneUID || a.AgentUID != b.AgentUID ||
		a.Generation != b.Generation || a.Signal != b.Signal ||
		a.OperationID != b.OperationID {
		return false
	}
	switch {
	case a.ExitCode == nil && b.ExitCode == nil:
		return true
	case a.ExitCode == nil || b.ExitCode == nil:
		return false
	default:
		return *a.ExitCode == *b.ExitCode
	}
}

// PaneActivationOptions is the input to one generation issue.
type PaneActivationOptions struct {
	// Generation is the opaque value the launched process will quote back.
	Generation string
	// RuntimeID is the exact tmux pane handle at issue time, for diagnostics.
	RuntimeID string
	// AgentUID is set for an Agent-managed Pane and empty for a shell Pane.
	AgentUID string
	// OperationID labels the create/resume transaction that issued it.
	OperationID string
}

// RecordPaneActivation stamps a new activation generation onto one Pane.
//
// Issuing a generation clears any receipt the previous materialization left
// behind. A Pane that has just been relaunched has no termination evidence, and
// keeping the old receipt visible would present a dead process's exit status as
// the current one's.
func (m Mutator) RecordPaneActivation(reg *Registry, paneUID string, opts PaneActivationOptions) (Pane, error) {
	const op = "record pane activation"

	generation := strings.TrimSpace(opts.Generation)
	if generation == "" {
		return Pane{}, inputErr(op, ErrInvalidRegistry, "activation generation must not be empty")
	}
	pane, ok := reg.Pane(paneUID)
	if !ok {
		return Pane{}, stateErr(op, ErrNotFound, "pane %q does not exist", paneUID)
	}
	if agentUID := strings.TrimSpace(opts.AgentUID); agentUID != "" {
		if _, ok := reg.Agent(agentUID); !ok {
			return Pane{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
		}
	}
	now := m.clock()().UTC()
	pane.Status.Activation = PaneActivation{
		Generation:  generation,
		RuntimeID:   strings.TrimSpace(opts.RuntimeID),
		AgentUID:    strings.TrimSpace(opts.AgentUID),
		OperationID: strings.TrimSpace(opts.OperationID),
		StartedAt:   now,
	}
	pane.Status.LastTermination = nil
	if agentUID := strings.TrimSpace(opts.AgentUID); agentUID != "" {
		if agent, ok := reg.Agent(agentUID); ok {
			agent.Status.LastTermination = nil
		}
	}
	reg.UpdatedAt = now
	return pane.Clone(), nil
}

// TerminationOutcome is the result of offering one receipt to the registry.
//
// A refused receipt is not an error. Duplicate delivery, a receipt from a
// replaced generation, and a receipt naming a Pane that has since been deleted
// are all *expected* on this transport, and the honest answer to each of them
// is "nothing changed, here is why".
type TerminationOutcome struct {
	// Applied reports whether the registry now stores this receipt.
	Applied bool
	// Duplicate marks a receipt the registry already stores verbatim.
	Duplicate bool
	// Stale marks a receipt that lost a guard check.
	Stale bool
	// Reason is the operator-facing diagnostic for a refused receipt.
	Reason string
}

func staleTermination(format string, args ...any) TerminationOutcome {
	return TerminationOutcome{Stale: true, Reason: fmt.Sprintf(format, args...)}
}

// RecordTermination applies one receipt under the activation generation guard.
//
// The guards, in order, are the whole contract of this transport:
//
//   - the named Pane must still exist;
//   - the receipt's generation must be the Pane's *current* generation;
//   - a receipt naming an Agent must name the Agent that owns the Pane, and
//     that Agent's current pane binding must still be this Pane;
//   - a receipt the registry already stores verbatim changes nothing;
//   - recorded intent is sticky for its generation.
//
// The last one is not an optimization. A canonical delete records intent and
// then kills the live Pane; the supervisor watching that Pane sees its child
// die on a signal and reports abnormal. Letting the observation overwrite the
// intent would turn every deliberate deletion into a crash report.
//
// Nothing here changes an Agent phase, a paneRef, or a Pane's existence.
// Consuming this evidence is a separate concern with its own review.
func (m Mutator) RecordTermination(reg *Registry, receipt TerminationEvidence) (TerminationOutcome, error) {
	const op = "record termination"

	if !ValidTerminationSource(receipt.Source) {
		return TerminationOutcome{}, inputErr(op, ErrInvalidRegistry, "unsupported termination source %q", string(receipt.Source))
	}
	if !ValidTerminationClassification(receipt.Classification) {
		return TerminationOutcome{}, inputErr(op, ErrInvalidRegistry, "unsupported termination classification %q", string(receipt.Classification))
	}
	if receipt.Classification == TerminationIntentional && receipt.Source != TerminationSourceControlAction {
		return TerminationOutcome{}, inputErr(op, ErrInvalidRegistry,
			"only a canonical control action may record intentional termination, got source %q", string(receipt.Source))
	}
	paneUID := strings.TrimSpace(receipt.PaneUID)
	if paneUID == "" {
		return TerminationOutcome{}, inputErr(op, ErrInvalidRegistry, "termination receipt must name a pane")
	}
	receipt.PaneUID = paneUID
	receipt.AgentUID = strings.TrimSpace(receipt.AgentUID)
	receipt.Generation = strings.TrimSpace(receipt.Generation)
	receipt.Signal = strings.TrimSpace(receipt.Signal)
	receipt.OperationID = strings.TrimSpace(receipt.OperationID)

	pane, ok := reg.Pane(paneUID)
	if !ok {
		return staleTermination("pane %q is not in the registry", paneUID), nil
	}
	if pane.Status.Activation.Generation != receipt.Generation {
		return staleTermination("receipt generation %q is not pane %s current generation %q",
			receipt.Generation, paneUID, pane.Status.Activation.Generation), nil
	}

	var agent *Agent
	if receipt.AgentUID != "" {
		owner := pane.Metadata.OwnerRef
		if owner == nil || owner.Kind != KindAgent || owner.UID != receipt.AgentUID {
			return staleTermination("pane %s is not owned by agent %q", paneUID, receipt.AgentUID), nil
		}
		found, ok := reg.Agent(receipt.AgentUID)
		if !ok {
			return staleTermination("agent %q is not in the registry", receipt.AgentUID), nil
		}
		if found.Status.PaneRef != paneUID {
			return staleTermination("agent %s no longer binds pane %s", receipt.AgentUID, paneUID), nil
		}
		agent = found
	}

	if stored := pane.Status.LastTermination; stored != nil {
		if sameEvidence(stored, &receipt) {
			return TerminationOutcome{Duplicate: true, Reason: "receipt is already recorded verbatim"}, nil
		}
		if stored.Classification == TerminationIntentional && receipt.Classification != TerminationIntentional {
			return TerminationOutcome{
				Duplicate: true,
				Reason: fmt.Sprintf("pane %s already records intentional termination for generation %q",
					paneUID, receipt.Generation),
			}, nil
		}
	}

	now := m.clock()().UTC()
	if receipt.ObservedAt.IsZero() {
		receipt.ObservedAt = now
	} else {
		receipt.ObservedAt = receipt.ObservedAt.UTC()
	}
	pane.Status.LastTermination = receipt.Clone()
	if agent != nil {
		agent.Status.LastTermination = receipt.Clone()
	}
	reg.UpdatedAt = now
	return TerminationOutcome{Applied: true}, nil
}

// ObservePaneActivationRuntime records the exact tmux handle one activation
// generation was materialized onto.
//
// It is guarded by the generation rather than by the Pane uid alone: a handle
// observed for a generation the registry has already replaced describes a
// process that no longer holds the Pane, and writing it would make the
// diagnostic point at the wrong runtime object. A mismatch is a no-op.
func (m Mutator) ObservePaneActivationRuntime(reg *Registry, paneUID, generation, runtimeID string) (bool, error) {
	const op = "observe pane activation runtime"

	pane, ok := reg.Pane(paneUID)
	if !ok {
		return false, stateErr(op, ErrNotFound, "pane %q does not exist", paneUID)
	}
	generation = strings.TrimSpace(generation)
	runtimeID = strings.TrimSpace(runtimeID)
	if generation == "" || pane.Status.Activation.Generation != generation {
		return false, nil
	}
	if pane.Status.Activation.RuntimeID == runtimeID {
		return false, nil
	}
	pane.Status.Activation.RuntimeID = runtimeID
	reg.UpdatedAt = m.clock()().UTC()
	return true, nil
}

// ClearTermination removes a receipt this exact operation wrote.
//
// It is the compensating half of a control action that recorded its intent and
// then failed before carrying it out. The operation id guard is what makes the
// compensation safe: a receipt written by anything else -- another delete, a
// supervisor observing a real exit in the meantime -- is left alone, so a
// failed delete can never erase evidence it did not produce.
func (m Mutator) ClearTermination(reg *Registry, paneUID, operationID string) (bool, error) {
	const op = "clear termination"

	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return false, inputErr(op, ErrInvalidRegistry, "clearing a termination receipt requires an operation id")
	}
	pane, ok := reg.Pane(paneUID)
	if !ok {
		return false, nil
	}
	cleared := false
	if stored := pane.Status.LastTermination; stored != nil && stored.OperationID == operationID {
		pane.Status.LastTermination = nil
		cleared = true
	}
	if owner := pane.Metadata.OwnerRef; owner != nil && owner.Kind == KindAgent {
		if agent, ok := reg.Agent(owner.UID); ok {
			if stored := agent.Status.LastTermination; stored != nil && stored.OperationID == operationID {
				agent.Status.LastTermination = nil
				cleared = true
			}
		}
	}
	if cleared {
		reg.UpdatedAt = m.clock()().UTC()
	}
	return cleared, nil
}
