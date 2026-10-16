package agentmessages

import (
	"errors"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
)

type peerPushOutcome struct {
	delivered bool
	reason    string
	unknown   bool
	err       error
}

func classifyPeerPush(r agentcontrol.Response, err error) peerPushOutcome {
	if err != nil {
		var binding *agentcontrol.BindingError
		if errors.As(err, &binding) {
			return peerPushOutcome{reason: codexPushRefusedReason, err: err}
		}
		return peerPushOutcome{reason: "codex-turn-push-outcome-unknown", unknown: true, err: err}
	}
	if err := r.Error(); err != nil {
		switch r.Code {
		case "stale-epoch", "stale-binding", "unavailable", "no-active-turn", "turn-state-unavailable", "lifecycle-retry", "lifecycle-busy", "drain-required", "payload-too-large", "invalid-operation", "peer-injection-unsupported", "peer-busy", "peer-invalid", "provider-refused":
			return peerPushOutcome{reason: codexPushRefusedReason, err: err}
		}
		return peerPushOutcome{reason: "codex-turn-push-outcome-unknown", unknown: true, err: err}
	}
	return peerPushOutcome{delivered: true}
}
