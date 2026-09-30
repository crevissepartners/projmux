package metadata

import (
	"slices"
	"strings"
	"time"
)

// AgentInteractionFreshFor is the maximum age at which a durable observation
// may be presented as current after a process restart. Provider hooks refresh
// active states; an abandoned response must not leave a live badge forever.
const AgentInteractionFreshFor = 30 * time.Minute

// ValidAgentInteractionKind reports whether kind is in the public closed set.
func ValidAgentInteractionKind(kind AgentInteractionKind) bool {
	return slices.Contains(AgentInteractionKinds(), kind)
}

// ValidAgentInteractionSource reports whether source is safe durable metadata.
// Empty remains readable for registries written before sources were introduced,
// but every new mutation must choose one of the closed values.
func ValidAgentInteractionSource(source string) bool {
	return slices.Contains(AgentInteractionSources(), AgentInteractionSource(strings.TrimSpace(source)))
}

// EffectiveInteraction returns the current read model without erasing durable
// history. Non-running lifecycle, missing pane binding, and stale observations
// are all unknown and therefore cannot retain a response-complete badge.
func (a Agent) EffectiveInteraction(now time.Time) AgentInteraction {
	observed := a.Status.Interaction
	if a.Status.Phase != PhaseRunning || strings.TrimSpace(a.Status.PaneRef) == "" {
		return AgentInteraction{Kind: InteractionUnknown}
	}
	if !ValidAgentInteractionKind(observed.Kind) || observed.Kind == InteractionUnknown {
		return AgentInteraction{Kind: InteractionUnknown}
	}
	if observed.ObservedAt.IsZero() || (!now.IsZero() && now.Sub(observed.ObservedAt) > AgentInteractionFreshFor) {
		return AgentInteraction{Kind: InteractionUnknown}
	}
	return observed
}

// SetAgentInteraction stores one semantic observation on exactly one Agent.
// Transport vocabulary is normalized by callers before reaching this mutator.
func (m Mutator) SetAgentInteraction(reg *Registry, agentUID string, kind AgentInteractionKind, source string) (Agent, error) {
	const op = "set agent interaction"
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	if !ValidAgentInteractionKind(kind) {
		return Agent{}, inputErr(op, ErrInvalidPhase, "unsupported interaction kind %q", kind)
	}
	source = strings.TrimSpace(source)
	if !ValidAgentInteractionSource(source) {
		return Agent{}, inputErr(op, ErrInvalidPhase, "unsupported interaction source %q", source)
	}
	now := m.clock()().UTC()
	agent.Status.Interaction = AgentInteraction{Kind: kind, ObservedAt: now, Source: source}
	reg.UpdatedAt = now
	return agent.Clone(), nil
}

// SetAgentTopic mutates only the non-identifying topic annotation.
func (m Mutator) SetAgentTopic(reg *Registry, agentUID, topic string) (Agent, error) {
	const op = "set agent topic"
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	if agent.Metadata.Annotations == nil {
		agent.Metadata.Annotations = map[string]string{}
	}
	topic = strings.TrimSpace(topic)
	if topic == "" {
		delete(agent.Metadata.Annotations, AnnotationAgentTopic)
		if len(agent.Metadata.Annotations) == 0 {
			agent.Metadata.Annotations = nil
		}
	} else {
		agent.Metadata.Annotations[AnnotationAgentTopic] = topic
	}
	reg.UpdatedAt = m.clock()().UTC()
	return agent.Clone(), nil
}

// QuestionChannelEnabled reports whether an Agent is opted into answering its
// AskUserQuestion prompts from the command line.
func QuestionChannelEnabled(agent Agent) bool {
	return agent.Metadata.Annotations[AnnotationAgentQuestionChannel] == QuestionChannelOn
}

// SetAgentQuestionChannel sets or clears the question channel annotation of one
// existing Agent. Every other annotation is left as it was.
func (m Mutator) SetAgentQuestionChannel(reg *Registry, agentUID string, on bool) (Agent, error) {
	const op = "set agent question channel"
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	if on {
		if agent.Metadata.Annotations == nil {
			agent.Metadata.Annotations = map[string]string{}
		}
		agent.Metadata.Annotations[AnnotationAgentQuestionChannel] = QuestionChannelOn
	} else {
		delete(agent.Metadata.Annotations, AnnotationAgentQuestionChannel)
		if len(agent.Metadata.Annotations) == 0 {
			agent.Metadata.Annotations = nil
		}
	}
	reg.UpdatedAt = m.clock()().UTC()
	return agent.Clone(), nil
}

// AgentPersonaAnnotations is the persona state of one existing Agent: the
// persona name and digest (both set or both empty), the system prompt
// snapshot mode ("" or SystemPromptSnapshotOff), and where the instructions
// came from (AnnotationAgentInstructionsSource: "" or a value
// ValidSettingSource accepts). An empty field removes its annotation.
type AgentPersonaAnnotations struct {
	Persona              string
	PersonaDigest        string
	SystemPromptSnapshot string
	InstructionsSource   string
}

// SameLaunch reports whether a and b launch a provider session the same way:
// the same persona, digest, and snapshot mode. Where they came from does not
// change the launch.
func (a AgentPersonaAnnotations) SameLaunch(b AgentPersonaAnnotations) bool {
	a.InstructionsSource, b.InstructionsSource = "", ""
	return a == b
}

// PersonaAnnotationsOf reads the persona state an Agent records.
func PersonaAnnotationsOf(agent Agent) AgentPersonaAnnotations {
	return AgentPersonaAnnotations{
		Persona:              agent.Metadata.Annotations[AnnotationAgentPersona],
		PersonaDigest:        agent.Metadata.Annotations[AnnotationAgentPersonaDigest],
		SystemPromptSnapshot: agent.Metadata.Annotations[AnnotationAgentSystemPromptSnapshot],
		InstructionsSource:   agent.Metadata.Annotations[AnnotationAgentInstructionsSource],
	}
}

// SetAgentPersona replaces the persona annotations of one existing Agent in a
// single mutation: the persona name, its digest, the system prompt snapshot
// mode, and the instructions source are written together or not at all, so no
// reader ever sees a persona without its digest, a new persona without the
// snapshot mode that makes a resume honor it, or a new persona with the
// source of the old one. Every other annotation is left as it was.
func (m Mutator) SetAgentPersona(reg *Registry, agentUID string, want AgentPersonaAnnotations) (Agent, error) {
	const op = "set agent instructions"
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	name := strings.TrimSpace(want.Persona)
	digest := strings.TrimSpace(want.PersonaDigest)
	snapshot := strings.TrimSpace(want.SystemPromptSnapshot)
	source := strings.TrimSpace(want.InstructionsSource)
	if (name == "") != (digest == "") {
		return Agent{}, inputErr(op, ErrInvalidRegistry, "instructions %q and digest %q must be set or cleared together", name, digest)
	}
	if snapshot != "" && snapshot != SystemPromptSnapshotOff {
		return Agent{}, inputErr(op, ErrInvalidRegistry, "unsupported system prompt snapshot mode %q", snapshot)
	}
	if source != "" && !ValidSettingSource(source) {
		return Agent{}, inputErr(op, ErrInvalidRegistry, "unsupported instructions source %q", source)
	}
	if agent.Metadata.Annotations == nil {
		agent.Metadata.Annotations = map[string]string{}
	}
	for key, value := range map[string]string{
		AnnotationAgentPersona:              name,
		AnnotationAgentPersonaDigest:        digest,
		AnnotationAgentSystemPromptSnapshot: snapshot,
		AnnotationAgentInstructionsSource:   source,
	} {
		if value == "" {
			delete(agent.Metadata.Annotations, key)
		} else {
			agent.Metadata.Annotations[key] = value
		}
	}
	if len(agent.Metadata.Annotations) == 0 {
		agent.Metadata.Annotations = nil
	}
	reg.UpdatedAt = m.clock()().UTC()
	return agent.Clone(), nil
}

// SetAgentProfileDigest records the digest of the profile content a resume
// just applied to one existing Agent. The Agent must already record the
// profile name, and the digest is replaced in place, so the name/digest pair
// is never broken. Every other annotation is left as it was.
func (m Mutator) SetAgentProfileDigest(reg *Registry, agentUID, name, digest string) (Agent, error) {
	const op = "set agent profile digest"
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	name = strings.TrimSpace(name)
	digest = strings.TrimSpace(digest)
	if name == "" || digest == "" {
		return Agent{}, inputErr(op, ErrInvalidRegistry, "profile %q and digest %q must both be set", name, digest)
	}
	if recorded := agent.Metadata.Annotations[AnnotationAgentProfile]; recorded != name {
		return Agent{}, inputErr(op, ErrInvalidRegistry, "agent %q records profile %q, not %q", agentUID, recorded, name)
	}
	if agent.Metadata.Annotations[AnnotationAgentProfileDigest] == digest {
		return agent.Clone(), nil
	}
	agent.Metadata.Annotations[AnnotationAgentProfileDigest] = digest
	reg.UpdatedAt = m.clock()().UTC()
	return agent.Clone(), nil
}

// SetAgentProfile replaces the profile one existing Agent records, as an
// `agent relaunch --profile` launches it: the name, the digest of the content
// applied, and where the profile came from (AnnotationAgentProfileSource) are
// written together. An empty name removes all three: the Agent records no
// profile. Every other annotation is left as it was.
func (m Mutator) SetAgentProfile(reg *Registry, agentUID, name, digest, source string) (Agent, error) {
	const op = "set agent profile"
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	name, digest, source = strings.TrimSpace(name), strings.TrimSpace(digest), strings.TrimSpace(source)
	if name != "" && (digest == "" || !ValidProfileSource(source)) {
		return Agent{}, inputErr(op, ErrInvalidRegistry, "profile %q needs a digest and a profile source, got %q and %q", name, digest, source)
	}
	if name == "" {
		digest, source = "", ""
	}
	want := map[string]string{
		AnnotationAgentProfile:       name,
		AnnotationAgentProfileDigest: digest,
		AnnotationAgentProfileSource: source,
	}
	changed := false
	for key, value := range want {
		if agent.Metadata.Annotations[key] != value {
			changed = true
		}
	}
	if !changed {
		return agent.Clone(), nil
	}
	if agent.Metadata.Annotations == nil {
		agent.Metadata.Annotations = map[string]string{}
	}
	for key, value := range want {
		if value == "" {
			delete(agent.Metadata.Annotations, key)
		} else {
			agent.Metadata.Annotations[key] = value
		}
	}
	if len(agent.Metadata.Annotations) == 0 {
		agent.Metadata.Annotations = nil
	}
	reg.UpdatedAt = m.clock()().UTC()
	return agent.Clone(), nil
}

// ClearAgentEffort removes the effort one existing Agent records and its
// source: a launch without a profile that no longer passes an effort. Every
// other annotation is left as it was.
func (m Mutator) ClearAgentEffort(reg *Registry, agentUID string) (Agent, error) {
	return m.clearAgentSetting(reg, "clear agent effort", agentUID, AnnotationAgentEffort, AnnotationAgentEffortSource)
}

// ClearAgentModel is ClearAgentEffort for the model.
func (m Mutator) ClearAgentModel(reg *Registry, agentUID string) (Agent, error) {
	return m.clearAgentSetting(reg, "clear agent model", agentUID, AnnotationAgentModel, AnnotationAgentModelSource)
}

func (m Mutator) clearAgentSetting(reg *Registry, op, agentUID, valueKey, sourceKey string) (Agent, error) {
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	_, hasValue := agent.Metadata.Annotations[valueKey]
	_, hasSource := agent.Metadata.Annotations[sourceKey]
	if !hasValue && !hasSource {
		return agent.Clone(), nil
	}
	delete(agent.Metadata.Annotations, valueKey)
	delete(agent.Metadata.Annotations, sourceKey)
	if len(agent.Metadata.Annotations) == 0 {
		agent.Metadata.Annotations = nil
	}
	reg.UpdatedAt = m.clock()().UTC()
	return agent.Clone(), nil
}

// SetAgentEffort records the effort an `agent resume --effort` or
// `agent relaunch --effort` is about to launch one existing Agent with,
// replacing the one it recorded, and records where it came from
// (AnnotationAgentEffortSource) in the same mutation. Recording the value and
// source it already records is not a change. Every other annotation is left as
// it was.
func (m Mutator) SetAgentEffort(reg *Registry, agentUID, effort, source string) (Agent, error) {
	return m.setAgentSetting(reg, "set agent effort", agentUID, "effort", AnnotationAgentEffort, AnnotationAgentEffortSource, effort, source)
}

// SetAgentModel records the model an `agent resume --model` or
// `agent relaunch --model` is about to launch one existing Agent with,
// replacing the one it recorded, and records where it came from
// (AnnotationAgentModelSource) in the same mutation. Recording the value and
// source it already records is not a change. Every other annotation is left as
// it was.
func (m Mutator) SetAgentModel(reg *Registry, agentUID, model, source string) (Agent, error) {
	return m.setAgentSetting(reg, "set agent model", agentUID, "model", AnnotationAgentModel, AnnotationAgentModelSource, model, source)
}

// SetAgentEffortFromProfile records that one existing Agent's effort follows
// its profile: effort is what a resume is about to launch it with from the
// profile ("" when the profile sets none, which removes the value), and the
// source is SettingSourceProfile, kept even without a value so the effort goes
// on following the profile. Every other annotation is left as it was.
func (m Mutator) SetAgentEffortFromProfile(reg *Registry, agentUID, effort string) (Agent, error) {
	return m.setAgentProfileSetting(reg, "set agent effort", agentUID, AnnotationAgentEffort, AnnotationAgentEffortSource, effort)
}

// SetAgentModelFromProfile is SetAgentEffortFromProfile for the model.
func (m Mutator) SetAgentModelFromProfile(reg *Registry, agentUID, model string) (Agent, error) {
	return m.setAgentProfileSetting(reg, "set agent model", agentUID, AnnotationAgentModel, AnnotationAgentModelSource, model)
}

func (m Mutator) setAgentProfileSetting(reg *Registry, op, agentUID, valueKey, sourceKey, value string) (Agent, error) {
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	value = strings.TrimSpace(value)
	recorded, hasValue := agent.Metadata.Annotations[valueKey]
	if (hasValue && recorded == value || !hasValue && value == "") && agent.Metadata.Annotations[sourceKey] == SettingSourceProfile {
		return agent.Clone(), nil
	}
	if agent.Metadata.Annotations == nil {
		agent.Metadata.Annotations = map[string]string{}
	}
	if value == "" {
		delete(agent.Metadata.Annotations, valueKey)
	} else {
		agent.Metadata.Annotations[valueKey] = value
	}
	agent.Metadata.Annotations[sourceKey] = SettingSourceProfile
	reg.UpdatedAt = m.clock()().UTC()
	return agent.Clone(), nil
}

// setAgentSetting writes one launch setting and its source together: both are
// required, and the source must be one ValidSettingSource accepts.
func (m Mutator) setAgentSetting(reg *Registry, op, agentUID, item, valueKey, sourceKey, value, source string) (Agent, error) {
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return Agent{}, inputErr(op, ErrInvalidRegistry, "%s must be set", item)
	}
	if !ValidSettingSource(source) {
		return Agent{}, inputErr(op, ErrInvalidRegistry, "unsupported %s source %q", item, source)
	}
	if agent.Metadata.Annotations[valueKey] == value && agent.Metadata.Annotations[sourceKey] == source {
		return agent.Clone(), nil
	}
	if agent.Metadata.Annotations == nil {
		agent.Metadata.Annotations = map[string]string{}
	}
	agent.Metadata.Annotations[valueKey] = value
	agent.Metadata.Annotations[sourceKey] = source
	reg.UpdatedAt = m.clock()().UTC()
	return agent.Clone(), nil
}

// SetAgentProjectLinkRules records the Project label link rules digest a
// resume is about to launch one existing Agent with, replacing the one it
// recorded; an empty digest removes the annotation (the Project has no rules
// any more). It always records AnnotationAgentSystemPromptSnapshot off in the
// same mutation: Claude replays the system prompt a conversation recorded, so a
// changed rule set reaches it only with the snapshot off, and the mode is
// sticky. Every other annotation is left as it was.
func (m Mutator) SetAgentProjectLinkRules(reg *Registry, agentUID, digest string) (Agent, error) {
	const op = "set agent project link rules"
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	digest = strings.TrimSpace(digest)
	recorded, hasRecorded := agent.Metadata.Annotations[AnnotationAgentProjectLinkRulesDigest]
	sameDigest := hasRecorded && recorded == digest || !hasRecorded && digest == ""
	if sameDigest && agent.Metadata.Annotations[AnnotationAgentSystemPromptSnapshot] == SystemPromptSnapshotOff {
		return agent.Clone(), nil
	}
	if agent.Metadata.Annotations == nil {
		agent.Metadata.Annotations = map[string]string{}
	}
	if digest == "" {
		delete(agent.Metadata.Annotations, AnnotationAgentProjectLinkRulesDigest)
	} else {
		agent.Metadata.Annotations[AnnotationAgentProjectLinkRulesDigest] = digest
	}
	agent.Metadata.Annotations[AnnotationAgentSystemPromptSnapshot] = SystemPromptSnapshotOff
	reg.UpdatedAt = m.clock()().UTC()
	return agent.Clone(), nil
}

// SetAgentGuidance records the agent guidance digest a resume is about to
// launch one existing Agent with, replacing the one it recorded; an empty
// digest removes the annotation (the guidance is off). It always records
// AnnotationAgentSystemPromptSnapshot off in the same mutation, for the reason
// SetAgentProjectLinkRules does. Every other annotation is left as it was.
func (m Mutator) SetAgentGuidance(reg *Registry, agentUID, digest string) (Agent, error) {
	const op = "set agent guidance"
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	digest = strings.TrimSpace(digest)
	recorded, hasRecorded := agent.Metadata.Annotations[AnnotationAgentGuidanceDigest]
	sameDigest := hasRecorded && recorded == digest || !hasRecorded && digest == ""
	if sameDigest && agent.Metadata.Annotations[AnnotationAgentSystemPromptSnapshot] == SystemPromptSnapshotOff {
		return agent.Clone(), nil
	}
	if agent.Metadata.Annotations == nil {
		agent.Metadata.Annotations = map[string]string{}
	}
	if digest == "" {
		delete(agent.Metadata.Annotations, AnnotationAgentGuidanceDigest)
	} else {
		agent.Metadata.Annotations[AnnotationAgentGuidanceDigest] = digest
	}
	agent.Metadata.Annotations[AnnotationAgentSystemPromptSnapshot] = SystemPromptSnapshotOff
	reg.UpdatedAt = m.clock()().UTC()
	return agent.Clone(), nil
}

// SetAgentActivation records bounded launch acknowledgement metadata.
func (m Mutator) SetAgentActivation(reg *Registry, agentUID string, state AgentActivationState, source, reason string) (Agent, error) {
	const op = "set agent activation"
	agent, ok := reg.Agent(agentUID)
	if !ok {
		return Agent{}, stateErr(op, ErrNotFound, "agent %q does not exist", agentUID)
	}
	switch state {
	case ActivationNotRequested, ActivationPending, ActivationAcknowledged, ActivationUnconfirmed:
	default:
		return Agent{}, inputErr(op, ErrInvalidPhase, "unsupported activation state %q", state)
	}
	source = strings.TrimSpace(source)
	reason = strings.TrimSpace(reason)
	if source != "" && source != string(InteractionSourceProviderHook) && source != string(InteractionSourceProviderControl) {
		return Agent{}, inputErr(op, ErrInvalidPhase, "unsupported activation source %q", source)
	}
	if !ValidAgentActivationReason(reason) {
		return Agent{}, inputErr(op, ErrInvalidPhase, "unsupported activation reason %q", reason)
	}
	// Activation is a refinement lattice, not a freely assignable status field.
	// A timeout may make pending evidence unconfirmed, and an exact provider hook
	// may later refine either pending or unconfirmed to acknowledged. Nothing may
	// move acknowledged backwards: in particular, the timeout writer racing a
	// provider hook must become a no-op instead of erasing stronger evidence.
	current := agent.Status.Activation.State
	if current == "" {
		current = ActivationNotRequested
	}
	if !validAgentActivationTransition(current, state) {
		return agent.Clone(), nil
	}
	now := m.clock()().UTC()
	agent.Status.Activation = AgentActivation{State: state, ObservedAt: now, Source: source, Reason: reason}
	reg.UpdatedAt = now
	return agent.Clone(), nil
}

func validAgentActivationTransition(from, to AgentActivationState) bool {
	if from == to {
		return true
	}
	switch from {
	case ActivationPending:
		return to == ActivationUnconfirmed || to == ActivationAcknowledged
	case ActivationUnconfirmed:
		return to == ActivationAcknowledged
	default:
		return false
	}
}
