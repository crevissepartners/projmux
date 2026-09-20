package app

import (
	"fmt"
	"io"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// launchValuesReasonAmbiguous is the reason token of a resume-picker create
// that inherited nothing because the Agents recording its conversation
// disagree about their launch values.
const launchValuesReasonAmbiguous = "launch-values-ambiguous"

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

// resumeLaunchValueKeysFor is the bundle one provider's resume-picker create
// inherits, or nil for a provider that inherits nothing.
func resumeLaunchValueKeysFor(provider string) []string {
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

// inheritedResumeLaunchValues is what a resume-picker create of one Claude or
// Codex conversation inherits from the Agents that already recorded it,
// anywhere in registry.
//
// Every recorded holder counts, live or not, so the new Agent launches the way
// any of them would resume. When they all record the same bundle it is the
// answer, verbatim -- a snapshot that is gone or an effort Claude would not
// take included, since the new Agent should resume exactly like the old one.
// When they disagree nothing is inherited and the returned notice says so:
// picking one of them, the latest say, would be a guess the operator never
// made. The notice carries no `projmux: ` prefix: the consumers of
// writeIntentAgentNotices add it, as they do for the split start notice.
// No holder, a common empty bundle, or a provider that inherits no keys at all
// inherits nothing, which keeps the create what it was before.
//
// A Codex conversation inherits only the two persona keys, and inherits them
// as a record rather than as a launch: its thread already carries the
// developer instructions, so the argv is the one it always was.
//
// Nothing about the holders changes. The picker still creates a new Agent;
// the old ones keep their conversation, Panes and annotations.
func inheritedResumeLaunchValues(registry *coremetadata.Registry, provider, conversation string) (map[string]string, string) {
	conversation = strings.TrimSpace(conversation)
	keys := resumeLaunchValueKeysFor(provider)
	if registry == nil || len(keys) == 0 || conversation == "" {
		return nil, ""
	}
	holders := registry.AgentsRecordingConversation(pickerResumeSessionObservation(provider, conversation))
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
				provider, conversation, launchValuesReasonAmbiguous, strings.Join(names, ", "),
				ambiguousLaunchValueSubject(provider))
		}
	}
	return common, ""
}

// ambiguousLaunchValueSubject names what the holders of one conversation
// disagreed about, which is exactly the bundle that provider inherits.
func ambiguousLaunchValueSubject(provider string) string {
	if provider == aiModeCodex {
		return "persona values"
	}
	return "persona, system prompt snapshot or effort values"
}

// writeIntentAgentNotices discloses what a committed UI Agent create could not
// carry over. Like agent resume's notices, a lost disclosure must not turn a
// committed create into a failure.
//
// Each notice is written without a leading `projmux: `. The intent path's
// consumers add that prefix themselves -- the split funnel shows create's
// stderr on the pressing client as one `projmux: ` line, exactly as it shows
// the split start notice -- so the seam's persona and effort notices, which
// `agent resume` prints with the prefix, would otherwise say it twice.
func writeIntentAgentNotices(stderr io.Writer, notices []string) {
	if stderr == nil {
		return
	}
	for _, notice := range notices {
		_, _ = fmt.Fprintln(stderr, strings.TrimPrefix(notice, "projmux: "))
	}
}
