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
			// No provider recorded is not another provider. Leaving it
			// unclassified keeps the foreign count meaning exactly one thing.
			continue
		}
		health.Classified++
		health.Foreign++
	}
	return health
}

// paneRecordsAnyProvider reports whether the Registry positively records a
// provider for this Pane.
func paneRecordsAnyProvider(activation coremetadata.PaneActivation) bool {
	return activation.Codex != nil || activation.Claude != nil
}
