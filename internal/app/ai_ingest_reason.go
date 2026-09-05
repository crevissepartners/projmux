package app

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
)

// aiIngestReason is the closed vocabulary the reason column of ai-ingest.log
// draws from.
//
// The column exists to be counted. Every diagnosis surface in this track reads
// it, and a bounded token is what lets a reader aggregate, compare and classify;
// a subprocess message there is a sentence nobody can group, and it carries
// whatever that subprocess chose to print -- a socket path, a home directory, an
// exit status. This track's change boundary excludes exactly that.
//
// THE BOUNDARY THIS ENFORCES IS RECORDING, NOT RECEIVING. Taking a foreign
// value in is not forbidden; writing it into a durable log is. Three things
// make that the right line. The standing instruction is itself phrased about
// what is left behind -- keep the reason a closed vocabulary and do not LEAVE a
// secret, a token, a prompt or a path in it. Every site folded here is a write:
// a hook payload's error field and its termination reason are ordinary input,
// and refusing to accept them would make classification impossible, so the fix
// is at the output. And the delivery path already drew the same line in code --
// codexHookDeliveryCause reads tmux's own words and returns none of them.
//
// aiIngestReasonFor has exactly that shape: it accepts any string, and answers
// with a vocabulary member or nothing at all. What a caller may not do is pass
// the original through.
//
// WHAT THIS TYPE DOES AND DOES NOT CLOSE. The type stops the two shapes that
// leaked: a raw error (`Reason: err.Error()`) and an arbitrary string variable
// no longer compile, because neither converts to a named string type. It does
// NOT stop a fresh literal: Go converts an untyped string constant implicitly,
// so `Reason: "any new sentence"` still compiles. Barring a new literal is the
// job of the vocabulary test, not of this type.
//
// The second limit is subtler and matters more. This type constrains what is
// ASSIGNED INTO the column, not what FLOWS TOWARD it. A function that returns
// aiIngestReason and computes something unbounded inside -- the shape
//
//	func boundedReason(err error) aiIngestReason { return aiIngestReason(err.Error()) }
//
// -- compiles, satisfies the type, and leaks exactly as before. What stands in
// that gap is aiIngestReasonFor, which does not trust its caller: it checks the
// value against the declared set and answers with nothing when it misses. The
// AST guards over the borrowed declarations cover the matching gap on the other
// side, where a producer this file does not own grows a token.
//
// Neither half closes the column alone, and this comment stays because a check
// that does not state its own limit is how this track lost eight phases of
// visibility.
type aiIngestReason string

// The failure vocabulary. Each token names the OPERATION that could not be
// completed, because that is the discriminator an operator can count. Today's
// discriminator is the error's own prose, which is unbounded and therefore
// uncountable; naming the step that failed produces instrumentation rather than
// destroying it.
//
// These are deliberately several tokens and not one. Folding every failure onto
// a single "unavailable" would satisfy a static audit while erasing the cause,
// which is the outcome Phase 0 of this track existed to prevent.
const (
	aiIngestReasonHookPayloadInvalid    aiIngestReason = "hook payload is malformed"
	aiIngestReasonNotifyStoreFailed     aiIngestReason = "notify store unavailable"
	aiIngestReasonNotifyPushFailed      aiIngestReason = "notify push failed"
	aiIngestReasonStatusApplyFailed     aiIngestReason = "pane status write failed"
	aiIngestReasonSemanticDeliverFailed aiIngestReason = "semantic delivery failed"
	aiIngestReasonAuthorityFenceFailed  aiIngestReason = "authority fence unavailable"
	aiIngestReasonReadinessWriteFailed  aiIngestReason = "startup readiness write failed"
)

// Provider-originated text used to be spliced into the reason. These tokens
// keep the classification and drop the wording, because the wording is provider
// output: a tool error reads `open /home/...: no such file` often enough that it
// was the live path by which an absolute path reached a durable log.
const (
	aiIngestReasonToolError          aiIngestReason = "tool reported an error"
	aiIngestReasonTerminationUnknown aiIngestReason = "unknown termination reason"
)

// Pane authority tokens. The authority is read back from a tmux pane option, so
// it is not a closed set at the moment it is read; mapping it through a switch
// over the declared authorities is what closes it. An unrecognised value gets
// its own token rather than being echoed.
const (
	aiIngestReasonAuthorityPending      aiIngestReason = "pane authority is pending"
	aiIngestReasonAuthorityControlPlane aiIngestReason = "pane authority is provider-control-plane"
	aiIngestReasonAuthorityInvalidating aiIngestReason = "pane authority is invalidating"
	aiIngestReasonAuthorityHook         aiIngestReason = "pane authority is provider-hook"
	aiIngestReasonAuthorityUnrecognized aiIngestReason = "pane authority is unrecognized"
	aiIngestReasonNativeAuthorityFailed aiIngestReason = "native authority unavailable"
)

// Tokens that were previously written as bare literals at their record site.
// Naming them is what lets the vocabulary test see them; an inline literal is
// invisible to every reader in this file.
const (
	aiIngestReasonBlankPane       aiIngestReason = "blank pane"
	aiIngestReasonPaneNotFound    aiIngestReason = "pane not found"
	aiIngestReasonHighVolumeEvent aiIngestReason = "high-volume event"
	aiIngestReasonInvocationStart aiIngestReason = "invocation started"
	aiIngestReasonStatuslineBusy  aiIngestReason = "statusline agent_state is busy"
	aiIngestReasonStatuslineIdle  aiIngestReason = "statusline agent_state is idle; preserving existing completion or attention state"
	aiIngestReasonStatuslineLate  aiIngestReason = "late busy statusline; preserving existing completion or approval state"
)

// aiIngestReasonUnclassified is what a value outside the vocabulary becomes.
//
// It is deliberately not silent. A reason that falls out of the set means the
// set drifted behind a producer, and this token makes that visible in the log
// itself rather than dropping the record's cause on the floor. Seeing it in
// production is the signal to extend the vocabulary.
const aiIngestReasonUnclassified aiIngestReason = "unclassified"

// aiIngestDeliveryRouteReasons are the bounded route tokens the codex hook
// delivery path already names. They are referenced from their own declarations
// so this list cannot fall behind them.
//
// Admitting them matters for the same reason the operation tokens exist: the
// delivery path classifies its own failure into four bounded causes, and
// collapsing that onto a generic write-failed token would throw away a
// distinction someone deliberately built.
var aiIngestDeliveryRouteReasons = []aiIngestReason{
	codexHookRouteUnavailableReason,
	codexHookRouteForeignPaneReason,
	codexHookWriteRejectedReason,
}

// aiIngestPaneWriteReasons are the bounded tokens the reflection write path
// names when a marker write does not land. Referenced from their own
// declarations, like every other borrowed half.
var aiIngestPaneWriteReasons = []aiIngestReason{
	aiPaneWriteReasonUnavailable,
	aiPaneWriteReasonMarkerUnavailable,
}

// aiIngestNativeRouteReasons are produced as plain literals by the native hook
// routing helpers in agent_session_ref.go. They are listed rather than
// referenced because they are returned inline there and have no constants to
// point at; TestIngestReasonVocabularyCoversNativeRouteLiterals reads that file
// and fails if this list falls behind it.
var aiIngestNativeRouteReasons = []aiIngestReason{
	"native binding unavailable",
	"incomplete activation identity",
	"stale activation Pane",
	"stale activation generation",
	"stale Agent/Pane binding",
	"thread does not match native binding",
}

// aiIngestPaneMatchReasons mirrors the attribution vocabulary declared in
// ai_ingest.go. The constants are referenced, never redeclared: that file's
// declarations are read by the regression gate's own AST scan, and a phase
// under judgment must not move what judges it.
var aiIngestPaneMatchReasons = []aiIngestReason{
	aiPaneMatchReasonNoMatch,
	aiPaneMatchReasonNoInventory,
	aiPaneMatchReasonRegistryUnavailable,
	aiPaneMatchReasonExplicitUnknown,
	aiPaneMatchReasonExplicitNoRuntime,
	aiPaneMatchReasonExplicitStale,
	aiPaneMatchReasonConversationUnknown,
	aiPaneMatchReasonConversationShared,
	aiPaneMatchReasonExplicitForeign,
	aiPaneMatchReasonExplicitForeignOnly,
}

// aiIngestReasons is the whole vocabulary.
//
// It is assembled rather than typed out so the borrowed halves cannot drift:
// the observer tokens are taken from the observer's own list, and the
// attribution tokens from the attribution constants. What is spelled out here
// is only what this file owns.
var aiIngestReasons = func() []aiIngestReason {
	all := []aiIngestReason{
		aiIngestReasonHookPayloadInvalid,
		aiIngestReasonNotifyStoreFailed,
		aiIngestReasonNotifyPushFailed,
		aiIngestReasonStatusApplyFailed,
		aiIngestReasonSemanticDeliverFailed,
		aiIngestReasonAuthorityFenceFailed,
		aiIngestReasonReadinessWriteFailed,

		aiIngestReasonToolError,
		aiIngestReasonTerminationUnknown,

		aiIngestReasonAuthorityPending,
		aiIngestReasonAuthorityControlPlane,
		aiIngestReasonAuthorityInvalidating,
		aiIngestReasonAuthorityHook,
		aiIngestReasonAuthorityUnrecognized,
		aiIngestReasonNativeAuthorityFailed,

		aiIngestReasonBlankPane,
		aiIngestReasonPaneNotFound,
		aiIngestReasonHighVolumeEvent,
		aiIngestReasonInvocationStart,
		aiIngestReasonStatuslineBusy,
		aiIngestReasonStatuslineIdle,
		aiIngestReasonStatuslineLate,

		aiIngestReasonUnclassified,
	}
	all = append(all, aiIngestPaneMatchReasons...)
	all = append(all, aiIngestDeliveryRouteReasons...)
	all = append(all, aiIngestPaneWriteReasons...)
	all = append(all, aiIngestNativeRouteReasons...)
	for _, reason := range codexObserverReasons {
		all = append(all, aiIngestReason(reason))
	}
	all = append(all, aiIngestHookActionReasons()...)
	all = append(all, aiIngestSemanticReasons()...)
	slices.Sort(all)
	return slices.Compact(all)
}()

// aiIngestHookActionReasons enumerates every reason the hook action helpers can
// produce, by running them over their whole input domain.
//
// Executing the producers beats transcribing their strings. Two of them build a
// reason by concatenation, so no literal scan could recover the result, and a
// transcribed copy is exactly the shape that falls behind its source.
func aiIngestHookActionReasons() []aiIngestReason {
	var reasons []aiIngestReason
	for _, action := range []string{aiHookActionNotify, aiHookActionQuiet, aiHookActionState} {
		for _, source := range []string{aiHookActionSourceCatalog, aiHookActionSourceRuntime} {
			for _, known := range []bool{true, false} {
				resolution := aiHookActionResolution{Action: action, Source: source, Known: known}
				reasons = append(reasons,
					aiIngestReason(aiHookQuietReason(resolution)),
					aiIngestReason(aiHookStateReason(resolution)),
					aiIngestReason(aiHookNoHandlerReason(resolution)),
				)
			}
		}
	}
	return reasons
}

// aiIngestSemanticReasons enumerates the codex semantic policy reasons the same
// way, over the policies and the fallback override flag.
func aiIngestSemanticReasons() []aiIngestReason {
	var reasons []aiIngestReason
	policies := []config.AISemanticPolicy{
		config.AISemanticNotify,
		config.AISemanticStateOnly,
		config.AISemanticQuiet,
	}
	for _, policy := range policies {
		for _, override := range []bool{true, false} {
			_, reason := codexHookSemanticLogResult(policy, override)
			reasons = append(reasons, aiIngestReason(reason))
		}
	}
	return reasons
}

// aiIngestReasonFor admits one foreign string into the vocabulary.
//
// It returns the empty reason for anything outside the set, so a caller has to
// decide what an unknown value means rather than letting it through. Callers
// writing a record use aiIngestRecordReason, which turns that decision into the
// unclassified token.
func aiIngestReasonFor(value string) aiIngestReason {
	candidate := aiIngestReason(strings.TrimSpace(value))
	if candidate == "" {
		return ""
	}
	if slices.Contains(aiIngestReasons, candidate) {
		return candidate
	}
	return ""
}

// aiIngestRecordReason is the write boundary for a reason that arrives as a
// plain string from a producer this package does not own.
//
// An empty input stays empty -- a record with no reason is a real state, not a
// failure to classify. Anything else that misses the vocabulary becomes the
// unclassified token, which keeps the record honest about the fact that the
// cause was not recognised.
func aiIngestRecordReason(value string) aiIngestReason {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if admitted := aiIngestReasonFor(value); admitted != "" {
		return admitted
	}
	return aiIngestReasonUnclassified
}

// aiIngestAuthorityReason names one pane authority without echoing the value
// read back from tmux.
func aiIngestAuthorityReason(authority string) aiIngestReason {
	switch strings.TrimSpace(authority) {
	case codexAuthorityPending:
		return aiIngestReasonAuthorityPending
	case codexAuthorityControlPlane:
		return aiIngestReasonAuthorityControlPlane
	case codexAuthorityInvalidating:
		return aiIngestReasonAuthorityInvalidating
	case codexAuthorityHook:
		return aiIngestReasonAuthorityHook
	default:
		return aiIngestReasonAuthorityUnrecognized
	}
}

// aiIngestFailureReason refines one operation token by the error's class.
//
// The operation is what an operator counts, so it is the token; the class only
// sharpens it where the distinction is already carried by a sentinel. An error
// that matches nothing keeps the operation token, which means no failure ever
// degrades into a less specific reason than the call site already knew.
func aiIngestFailureReason(operation aiIngestReason, err error) aiIngestReason {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return aiIngestReason(codexObserverReasonTimeout)
	}
	// A reflection write that did not land names its own token, and that token
	// is the more proximate cause: the operation says which step was running,
	// this says the write underneath it never reached the pane. The write path
	// built the sentinel precisely so the reason column would stay bounded, and
	// preferring it keeps that contract intact now that the call sites no longer
	// pass the error's own message through.
	if errors.Is(err, errAIPaneWriteUnavailable) {
		return aiPaneWriteReasonUnavailable
	}
	// A delivery failure already carries a bounded route token chosen by the
	// path that failed. Prefer it: it is strictly more specific than the
	// operation, and it is the classification the delivery path exists to make.
	// Its Detail is deliberately not consulted -- that half carries the tmux
	// message and the socket path this column must not hold.
	var delivery *codexHookDeliveryError
	if errors.As(err, &delivery) {
		if admitted := aiIngestReasonFor(delivery.Reason); admitted != "" {
			return admitted
		}
	}
	return operation
}
