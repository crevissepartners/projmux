package runtime

import (
	"errors"
	"fmt"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/profile"
)

// ResumeLaunchValuesReasonAmbiguous is the reason token of a resume-picker create
// that inherited nothing because the Agents recording its conversation
// disagree about their launch values.
const ResumeLaunchValuesReasonAmbiguous = "launch-values-ambiguous"

// claudeResumeLaunchValueKeys are the Agent annotations a Claude resume turns
// into launch options: the persona and the digest of its start-time snapshot,
// the system prompt snapshot mode, and the effort. The creator keys, the topic
// and the labels are deliberately not in it: they describe who made an Agent
// and what it is about, not how its provider session was launched.
var claudeResumeLaunchValueKeys = []string{
	coremetadata.AnnotationAgentPersona,
	coremetadata.AnnotationAgentPersonaDigest,
	coremetadata.AnnotationAgentSystemPromptSnapshot,
	coremetadata.AnnotationAgentEffort,
}

// codexResumeLaunchValueKeys are the two keys a Codex resume-picker create
// inherits: which persona the picked thread was started with, and the digest
// of that snapshot. They change no argv -- the thread carries the developer
// instructions itself -- and are inherited so the new Agent records the same
// answer to "which persona is this conversation running" as the Agents that
// already hold it. The snapshot mode and the effort are Claude launch options
// with no Codex meaning, so they stay out.
var codexResumeLaunchValueKeys = []string{
	coremetadata.AnnotationAgentPersona,
	coremetadata.AnnotationAgentPersonaDigest,
}

// ResumeLaunchValueKeysFor is the bundle one provider's resume-picker create
// inherits, or nil for a provider that inherits nothing.
func ResumeLaunchValueKeysFor(provider string) []string {
	switch provider {
	case aiModeClaude:
		return claudeResumeLaunchValueKeys
	case aiModeCodex:
		return codexResumeLaunchValueKeys
	default:
		return nil
	}
}

// resumeLaunchValues is the launch-value bundle one Agent records under keys,
// or nil when it records none of them.
func resumeLaunchValues(keys []string, annotations map[string]string) map[string]string {
	var out map[string]string
	for _, key := range keys {
		value, ok := annotations[key]
		if !ok {
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[key] = value
	}
	return out
}

// sameResumeLaunchValues compares two bundles key by key. A key one bundle
// records and the other does not is a difference, even when the recorded
// value is empty.
func sameResumeLaunchValues(keys []string, a, b map[string]string) bool {
	for _, key := range keys {
		left, leftOK := a[key]
		right, rightOK := b[key]
		if leftOK != rightOK || left != right {
			return false
		}
	}
	return true
}

// InheritedResumeLaunchValues is what a resume-picker create of one Claude or
// Codex conversation inherits from the Agents that already recorded it,
// anywhere in registry.
//
// Every recorded holder counts, live or not, so the new Agent launches the way
// any of them would resume. When they all record the same bundle it is the
// answer, verbatim -- a snapshot that is gone or an effort Claude would not
// take included, since the new Agent should resume exactly like the old one.
// When they disagree nothing is inherited and the returned notice says so:
// picking one of them, the latest say, would be a guess the operator never
// made. The notice carries no `projmux: ` prefix: the CLI writer of intent
// notices adds it, as it does for the split start notice.
// No holder, a common empty bundle, or a provider that inherits no keys at all
// inherits nothing, which keeps the create what it was before.
//
// A Codex conversation inherits only the two persona keys, and inherits them
// as a record rather than as a launch: its thread already carries the
// developer instructions, so the argv is the one it always was.
//
// Nothing about the holders changes. The picker still creates a new Agent;
// the old ones keep their conversation, Panes and annotations.
func InheritedResumeLaunchValues(registry *coremetadata.Registry, provider, conversation string) (map[string]string, string) {
	conversation = strings.TrimSpace(conversation)
	keys := ResumeLaunchValueKeysFor(provider)
	if registry == nil || len(keys) == 0 || conversation == "" {
		return nil, ""
	}
	holders := registry.AgentsRecordingConversation(PickerResumeSessionObservation(provider, conversation))
	if len(holders) == 0 {
		return nil, ""
	}
	common := resumeLaunchValues(keys, holders[0].Metadata.Annotations)
	for _, holder := range holders[1:] {
		if !sameResumeLaunchValues(keys, common, holder.Metadata.Annotations) {
			names := make([]string, 0, len(holders))
			for _, h := range holders {
				names = append(names, fmt.Sprintf("agent/%s (uid:%s)", h.Metadata.Name, h.Metadata.UID))
			}
			return nil, fmt.Sprintf(
				"%s conversation %s opened without inherited launch values (%s): %s record different %s",
				provider, conversation, ResumeLaunchValuesReasonAmbiguous, strings.Join(names, ", "),
				ResumeAmbiguousLaunchValueSubject(provider))
		}
	}
	return common, ""
}

// ResumeAmbiguousLaunchValueSubject names what the holders of one conversation
// disagreed about, which is exactly the bundle that provider inherits.
func ResumeAmbiguousLaunchValueSubject(provider string) string {
	if provider == aiModeCodex {
		return "instructions values"
	}
	return "instructions, system prompt snapshot or effort values"
}

// InheritedResumeProfile is the profile pair a resume-picker create of one
// Claude or Codex conversation inherits from the Agents that already recorded
// it. When every holder records the same profile name, the new Agent records
// that profile and its resume applies it; the digest is refreshed by that
// resume.
// Holders that disagree about the profile refuse the create: picking one, or
// none, could launch the conversation without the permissions it runs with.
// A Codex conversation inherits on the same terms: its native catalog resume
// re-sends the profile's current policy (NativeCodexResumeProfile), and its rollout
// resume passes the policy as CLI flags (resumeProfileSettings). Any
// other provider, and a conversation no holder records a profile for,
// inherits nothing.
func InheritedResumeProfile(registry *coremetadata.Registry, provider, conversation string) (map[string]string, error) {
	conversation = strings.TrimSpace(conversation)
	if registry == nil || (provider != aiModeClaude && provider != aiModeCodex) || conversation == "" {
		return nil, nil
	}
	holders := registry.AgentsRecordingConversation(PickerResumeSessionObservation(provider, conversation))
	if len(holders) == 0 {
		return nil, nil
	}
	first := holders[0].Metadata.Annotations
	for _, holder := range holders[1:] {
		if holder.Metadata.Annotations[coremetadata.AnnotationAgentProfile] != first[coremetadata.AnnotationAgentProfile] {
			names := make([]string, 0, len(holders))
			for _, h := range holders {
				names = append(names, fmt.Sprintf("agent/%s (uid:%s)", h.Metadata.Name, h.Metadata.UID))
			}
			return nil, errors.New((&ProfileResumeError{Name: first[coremetadata.AnnotationAgentProfile], Reason: ResumeLaunchValuesReasonAmbiguous,
				Detail: fmt.Sprintf("%s conversation %s is recorded with different profiles by %s", provider, conversation, strings.Join(names, ", "))}).Error() +
				"; nothing was created")
		}
	}
	name := first[coremetadata.AnnotationAgentProfile]
	if name == "" {
		return nil, nil
	}
	return map[string]string{
		coremetadata.AnnotationAgentProfile:       name,
		coremetadata.AnnotationAgentProfileDigest: first[coremetadata.AnnotationAgentProfileDigest],
	}, nil
}

// PickerResumeSessionObservation projects the provider-discriminated picker
// identity onto the existing durable sessionRef input shape. The picker owns
// only the exact resume id: it never reads or persists transcripts, caches, or
// provider-private turn identity.
func PickerResumeSessionObservation(provider, conversation string) coremetadata.AgentSessionObservation {
	observation := coremetadata.AgentSessionObservation{Provider: provider}
	switch provider {
	case aiModeCodex, aiModeAntigravity:
		observation.ThreadID = conversation
	default:
		observation.SessionID = conversation
	}
	return observation
}

// WithInheritedSettingSources adds to base the source of every launch setting
// a resume-picker create inherited from the Agents that already recorded its
// conversation: inherited is the launch-value bundle and profile pair it
// inherited, and each of the profile, the instructions, and the effort found
// there is recorded as coremetadata.SettingSourceInherited. The source keys
// are never part of the bundle itself, so holders that differ only in where
// their values came from still agree.
//
// Nothing inherited returns base itself; otherwise it returns a new map and
// never writes into base.
func WithInheritedSettingSources(inherited, base map[string]string) map[string]string {
	sources := map[string]string{}
	for valueKey, sourceKey := range map[string]string{
		coremetadata.AnnotationAgentProfile: coremetadata.AnnotationAgentProfileSource,
		coremetadata.AnnotationAgentPersona: coremetadata.AnnotationAgentInstructionsSource,
		coremetadata.AnnotationAgentEffort:  coremetadata.AnnotationAgentEffortSource,
	} {
		if inherited[valueKey] != "" {
			sources[sourceKey] = coremetadata.SettingSourceInherited
		}
	}
	return WithSettingSources(base, sources)
}

// ResolvePickerSettings resolves the settings of the Agent a resume-picker
// create records from inherited, the launch values and profile pair it
// inherited. resolveProfile re-reads a recorded profile by name; resolve
// resolves the layered settings of the inherited annotations and reports
// false when the launcher has no layered settings. A profile that is gone or
// invalid leaves the settings unlayered, so the launch refuses exactly as it
// did before layers existed; one out of the Project's scope refuses. The
// snapshot of new instructions content is written here, inside the create's
// transaction.
func ResolvePickerSettings(
	resolveProfile func(name, projectUID string) error,
	resolve func(annotations map[string]string, request AgentSettingsRequest) (AgentSettingsLaunch, bool, error),
	inherited map[string]string,
	projectUID ...string,
) (AgentSettingsLaunch, error) {
	request := AgentSettingsRequest{}
	if len(projectUID) != 0 {
		request.ProjectUID = projectUID[0]
	}
	if name := inherited[coremetadata.AnnotationAgentProfile]; name != "" {
		if err := resolveProfile(name, request.ProjectUID); profile.ReasonOf(err) == profile.ReasonOutOfScope {
			return AgentSettingsLaunch{}, err
		}
	}
	annotations := WithInheritedSettingSources(inherited, inherited)
	settings, layered, err := resolve(annotations, request)
	if !layered {
		settings, err = AgentSettingsLaunch{}, nil
	}
	if err != nil {
		if profile.ReasonOf(err) == profile.ReasonOutOfScope {
			return AgentSettingsLaunch{}, err
		}
		return AgentSettingsLaunch{}, nil
	}
	if settings = settings.WriteSnapshot(); settings.SnapshotErr != nil {
		return AgentSettingsLaunch{}, settings.SnapshotErr
	}
	return settings, nil
}
