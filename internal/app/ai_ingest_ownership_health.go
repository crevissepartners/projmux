package app

import (
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// Attribution can succeed and still be wrong.
//
// A hook is handed a Pane identity explicitly, and the process that runs it may
// have inherited that identity from whatever Pane happened to launch it rather
// than from the Pane that owns the conversation. The event then lands on a real
// Pane, is recorded as attributed, and belongs to somebody else. Counting it as
// a success is the failure this surface exists to make visible.

// aiIngestOwnershipHealth is the content-free projection of whether attributed
// hook events landed on a Pane of their own provider.
type aiIngestOwnershipHealth struct {
	// Observed reports whether both a log and a Registry were readable. One
	// without the other answers nothing, and answering nothing must not read
	// as answering well.
	Observed bool `json:"observed"`
	// Classified counts attributions this reader could judge: the Pane was
	// found in the Registry and records a provider.
	Classified int `json:"classified"`
	// Unresolved counts attributions to a Pane the Registry no longer holds.
	// A Pane that is gone is outside the attribution contract, so this is
	// reported beside the verdict rather than inside it.
	Unresolved int `json:"unresolved"`
	// Foreign counts attributions to a Pane the Registry records under another
	// provider. This is the number the contract requires to be zero.
	Foreign int `json:"foreign"`
	// Unrecorded counts attributions to a Pane the Registry holds but records
	// no provider for.
	//
	// These are the ones this verdict cannot judge, and the count exists so
	// that a zero above it cannot be read as "nothing was misattributed". The
	// ownership guarantee closed the case where a Pane positively records
	// another provider; a Pane recording none resolves the old way, so an
	// attribution landing there is neither proven right nor proven wrong.
	// Rendering only the foreign count would let this diagnosis certify past
	// the guarantee it is reporting on -- a check overstating what it checked,
	// which is the failure this whole section was built to end.
	Unrecorded int `json:"unrecorded"`
	// Directions breaks Foreign down by which way the mismatch runs.
	//
	// The two directions have different causes and must not be read as one
	// number. A hook attributing onto the Pane that launched its host is the
	// identity leak the contract closes. The opposite direction — a provider's
	// events arriving under another provider's source — means the event was
	// misrouted before attribution ever ran, which a stale hook config on the
	// machine can produce and no code change here would fix. Reporting a single
	// count would let one be mistaken for the other, and this machine has
	// carried both.
	Directions []aiIngestAttributionReason `json:"directions,omitempty"`
}

// codexHookProviderBinding answers, for one hook source, whether a Pane records
// that provider.
//
// It is a table rather than a branch on purpose. The attribution contract is
// provider-neutral, and a reader that special-cased one provider would reopen
// the asymmetry the hook identity work closed.
var codexHookProviderBinding = map[string]func(coremetadata.PaneActivation) bool{
	"codex-hook":  func(activation coremetadata.PaneActivation) bool { return activation.Codex != nil },
	"claude-hook": func(activation coremetadata.PaneActivation) bool { return activation.Claude != nil },
}

// projectAIIngestOwnershipHealth judges each attributed hook event against the
// Registry's record of the Pane it landed on.
//
// The predicate is deliberately one-sided: an attribution is foreign only when
// the Pane positively records a different provider. A Pane that records no
// provider at all is not a contradiction, and calling it one would turn every
// ordinary shell Pane into a finding and bury the case that matters.
func projectAIIngestOwnershipHealth(entries []aiIngestLogEntry, registry coremetadata.Registry, observed bool) aiIngestOwnershipHealth {
	health := aiIngestOwnershipHealth{Observed: observed}
	if !observed {
		return health
	}
	directions := map[string]int{}
	byRuntime := make(map[string]coremetadata.PaneActivation, len(registry.Panes))
	for _, pane := range registry.Panes {
		if runtime := strings.TrimSpace(pane.Status.Activation.RuntimeID); runtime != "" {
			byRuntime[runtime] = pane.Status.Activation
		}
	}
	for _, entry := range entries {
		source := strings.TrimSpace(entry.Source)
		binds, known := codexHookProviderBinding[source]
		if !known {
			continue
		}
		runtime := strings.TrimSpace(entry.Pane)
		if runtime == "" {
			continue
		}
		activation, found := byRuntime[runtime]
		if !found {
			health.Unresolved++
			continue
		}
		if binds(activation) {
			health.Classified++
			continue
		}
		if !paneRecordsAnyProvider(activation) {
			// No provider recorded is not another provider. Counting it apart
			// keeps the foreign number meaning exactly one thing while still
			// showing how much of the window that number does not cover.
			health.Unrecorded++
			continue
		}
		health.Classified++
		health.Foreign++
		directions[source+" onto "+paneRecordedProvider(activation)]++
	}
	health.Directions = aiIngestAttributionReasons(directions)
	return health
}

// paneRecordedProvider names the provider the Registry records for one Pane.
// It is only ever called for a Pane that records one.
func paneRecordedProvider(activation coremetadata.PaneActivation) string {
	if activation.Codex != nil {
		return "codex"
	}
	return "claude"
}

// paneRecordsAnyProvider reports whether the Registry positively records a
// provider for this Pane.
func paneRecordsAnyProvider(activation coremetadata.PaneActivation) bool {
	return activation.Codex != nil || activation.Claude != nil
}
