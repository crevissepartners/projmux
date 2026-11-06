package projmux

import (
	"github.com/crevissepartners/projmux/internal/core/agentmessage"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// IsOperatorMessageOrigin reports whether origin is the operator origin: the
// operator kind with a valid client name. It is the same judgement every
// message reader applies, so an Agent origin, an empty origin and an operator
// origin with a missing or invalid client are all false. It takes no Client and
// reads nothing.
func IsOperatorMessageOrigin(origin core.MessageOrigin) bool {
	return agentmessage.IsOperatorOrigin(origin.Kind, origin.Client)
}

// MessagePayloadSHA256 returns the digest a message history entry carries for
// its payload: SHA-256 of the payload's exact bytes as 64 lowercase hex
// characters. Compare a payload held elsewhere with it, not with a copy of the
// rule.
func MessagePayloadSHA256(payload string) string {
	return agentmessage.PayloadSHA256(payload)
}

// MatchesProcessWait reports whether receipt is the exact supervisor Wait of the
// process generation binding names: a supervisor-sourced receipt with an
// observation time, the binding's Pane, Agent, generation and operation, an
// exit shape of one exit code from 0 to 255 or one signal, and a classification
// that agrees with that shape. A nil receipt is false. It reads nothing and is
// the check the Registry applies before it records a Wait.
func MatchesProcessWait(binding core.ProcessBinding, receipt *core.TerminationEvidence) bool {
	if receipt == nil {
		return false
	}
	return metadata.MatchesProcessWait(metadata.ProcessBinding{
		HostInstanceID: binding.HostInstanceID, ProjectUID: binding.ProjectUID, WindowUID: binding.WindowUID,
		AgentUID: binding.AgentUID, PaneUID: binding.PaneUID, Generation: binding.Generation, OperationID: binding.OperationID,
	}, &metadata.TerminationEvidence{
		Source: metadata.TerminationSource(receipt.Source), Classification: metadata.TerminationClassification(receipt.Classification),
		ObservedAt: receipt.ObservedAt, PaneUID: receipt.PaneUID, AgentUID: receipt.AgentUID, Generation: receipt.Generation,
		ExitCode: receipt.ExitCode, Signal: receipt.Signal, OperationID: receipt.OperationID,
	})
}
