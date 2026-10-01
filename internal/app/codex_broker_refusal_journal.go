package app

import (
	"errors"
	"sync/atomic"

	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
)

// codexBrokerRefusalJournal is this process's codex.broker.refusal recorder.
// It is process-wide because the refusals arrive deep inside long-lived
// observer and probe paths that hold no invocation recorder of their own; nil
// records nothing.
var codexBrokerRefusalJournal atomic.Pointer[diagnostics.CodexBrokerRecorder]

// observeCodexBrokerRefusals installs the invocation's recorder for every
// broker refusal this process receives. An invocation that must never append
// to the journal -- Doctor, the support report, and the retired no-write argv
// -- installs nothing. The returned function restores the previous recorder.
func observeCodexBrokerRefusals(lifecycle *diagnostics.LifecycleRecorder, args []string) (restore func()) {
	if lifecycle == nil || diagnostics.JournalForbidden(args) {
		return func() {}
	}
	previous := codexBrokerRefusalJournal.Swap(lifecycle.CodexBroker())
	return func() { codexBrokerRefusalJournal.Store(previous) }
}

// recordCodexBrokerRefusal is the one place a received broker refusal reaches
// the journal. It records only a typed *codexbroker.BrokerError, as its closed
// Refusal token and, for a refused dial, its closed DialStage; any other error
// -- a context deadline, a local identity check -- records nothing. The
// append is best effort and never changes what the caller does with err.
func recordCodexBrokerRefusal(role diagnostics.CodexBrokerRole, operation diagnostics.CodexBrokerOperation, err error) {
	var refusal *codexbroker.BrokerError
	if !errors.As(err, &refusal) {
		return
	}
	recorder := codexBrokerRefusalJournal.Load()
	if recorder == nil {
		return
	}
	recorder.RecordRefusal(diagnostics.CodexBrokerRefusal{
		Role: role, Operation: operation, Reason: string(refusal.Refusal), DialStage: string(codexbroker.DialStageOf(err)),
	})
}
