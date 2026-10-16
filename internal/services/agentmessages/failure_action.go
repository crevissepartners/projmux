package agentmessages

import (
	"fmt"
	"github.com/crevissepartners/projmux/internal/aiprovider"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
)

const codexPushRefusedReason = "codex-turn-push-refused"

func ReceiptFailureAction(receipt Receipt, claudeContentBytes int) string {
	if receipt.Target.Provider != string(aiprovider.Claude) {
		claudeContentBytes = 0
	}
	if receipt.ReplyTo != "" {
		return agentMessageReplyFailureAction(messagestore.Record{Envelope: coremessage.Envelope{
			ReplyTo: receipt.ReplyTo, Deadline: receipt.Deadline}, Adapter: adapterForReplyReceipt(receipt), Delivery: receipt.Delivery},
			claudeContentBytes)
	}
	return SendFailureAction(receipt.Delivery, claudeContentBytes)
}

func adapterForReplyReceipt(receipt Receipt) string {
	if receipt.Target.Provider == "claude" {
		return "claude-coordination"
	}
	return "codex-inbox"
}

func agentMessageReplyFailureAction(record messagestore.Record, claudeContentBytes int) string {
	if messagestore.KnownZeroReply(record) {
		return SendFailureAction(record.Delivery, claudeContentBytes) + "; retry manually with the same --reply-to and a new --message-ref (or omit --message-ref); original deadline and exact routes must remain valid"
	}
	return "inspect original and previous reply status and provider outcome; do not resend"
}

// agentMessageSendFailureAction names the mixed-version cause of an
// invalid-content failure: an older target helper still caps push content at
// the 4096-byte payload limit. The persisted reason is never changed.
func SendFailureAction(delivery coremessage.Delivery, claudeContentBytes int) string {
	if !delivery.OutcomeUnknown && delivery.Reason == "provider-frame-invalid-content" &&
		claudeContentBytes > coremessage.MaxPayloadBytes {
		return fmt.Sprintf("rendered push content is %d bytes, above the %d-byte content limit of an older target helper; "+
			"re-activate (restart) the target Agent to load the current helper before retrying",
			claudeContentBytes, coremessage.MaxPayloadBytes)
	}
	return FailureAction(delivery)
}

func FailureAction(delivery coremessage.Delivery) string {
	if delivery.OutcomeUnknown {
		return "inspect provider outcome; do not resend while unknown; automatic resend disabled"
	}
	if isClaudeProviderFrameSizeReason(delivery.Reason) {
		return "reduce payload before retrying; frame bytes include auth and serialized content"
	}
	switch delivery.Reason {
	case "provider-frame-invalid-auth":
		return "check provider auth configuration before retrying"
	case "provider-frame-invalid-content", "provider-frame-build-failed", "provider-frame-unsupported",
		"claude-private-frame-unsupported", "codex-turn-content-build-failed":
		return "correct message content or configuration before retrying"
	case "codex-native-control-unconfigured", "codex-native-binding-unavailable":
		return "check exact Agent native control availability before retrying"
	case codexPushRefusedReason:
		return "no turn was written; retry manually once the target thread state allows it"
	case "provider-prewrite-refused":
		return "check current provider route before retrying"
	case "provider-write-zero":
		return "check provider connection before retrying"
	default:
		return "check message status and target availability before retrying"
	}
}
