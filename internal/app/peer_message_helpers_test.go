package app

import (
	"encoding/json"
	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremessage "github.com/crevissepartners/projmux/internal/core/agentmessage"
	messagestore "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	"github.com/crevissepartners/projmux/internal/services/agentmessages"
)

func claudePrivateCoordinationEnvelope(target claudeCoordinationTarget, envelope coremessage.Envelope) claudeCoordinationEnvelope {
	return claudeCoordinationEnvelope{Version: claudeCoordinationVersion, MessageRef: envelope.MessageRef, Target: target,
		Source:   claudeCoordinationSourceOf(envelope.Authority),
		Deadline: envelope.Deadline, BrokerEnvelope: &envelope}
}

func receiptFor(r messagestore.Record) agentMessageReceipt { return agentmessages.ReceiptFor(r) }
func claudeResponseDelivery(ref string, response claudeCoordinationResponse) (agentdelivery.Delivery, bool) {
	data, err := json.Marshal(response)
	if err != nil {
		return agentdelivery.Delivery{}, false
	}
	var shared agentmessages.ClaudeResponse
	if json.Unmarshal(data, &shared) != nil {
		return agentdelivery.Delivery{}, false
	}
	return agentmessages.ClaudeResponseDelivery(ref, shared)
}
func validClaudeProviderPushContent(content string) bool {
	return agentmessages.ValidClaudePushContent(content)
}
func coordinationOperatorSourceNotice(client string) string {
	return agentmessages.OperatorSourceNotice(client)
}
func agentMessageSendFailureAction(d coremessage.Delivery, n int) string {
	return agentmessages.SendFailureAction(d, n)
}
func agentMessageFailureAction(d coremessage.Delivery) string { return agentmessages.FailureAction(d) }

func explicitReplyRefusal(response claudeCoordinationResponse) error {
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}
	var shared agentmessages.ClaudeResponse
	if err := json.Unmarshal(data, &shared); err != nil {
		return err
	}
	return agentmessages.ExplicitReplyRefusal(shared)
}
