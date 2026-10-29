package projmux

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	"github.com/crevissepartners/projmux/internal/core/operatorclient"
	"github.com/crevissepartners/projmux/internal/services/agentmessages"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// OperatorMessageRequest is operator coordination input for one Claude Agent.
// Operator is the in-process operator client; it cannot be decoded from
// request data. There is no Source and no ReplyTo: the operator is not an Agent
// and an operator message is never a reply. Target is a name or uid: value
// resolved to exactly one Agent. TTL zero selects the CLI's ten-minute default;
// valid TTLs are at most 24 hours.
type OperatorMessageRequest struct {
	Operator   core.MessageOperator
	Target     string
	MessageRef string
	Payload    string
	TTL        time.Duration
}

// OperatorMessageResult is the receipt of an accepted operator message.
// ContentBytes is the rendered Claude push content size. Action is the action
// the receipt names, as the CLI prints it: the held action of a held receipt,
// the failure action of a terminal undelivered one, and empty otherwise.
type OperatorMessageResult struct {
	Receipt      core.MessageReceipt
	ContentBytes int
	Action       string
}

// SendOperatorMessage sends operator coordination input to one Claude Agent
// with the operator origin and authority, no source route and no reply. It runs
// the same target resolution, capability and route refusals, frame pre-check,
// acceptance, delivery or hold as a peer send. A target that is not Claude is
// refused before its route is resolved or anything is written, as
// ErrOperatorOriginTargetNotClaude with CodeOperatorOriginTargetNotClaude.
//
// When the error is not nil and the result's Receipt has a MessageRef, the
// message was accepted and the receipt and Action say why it was not
// delivered. Never resend an accepted message; an uncertain outcome is read
// with MessageStatus. A same-ref call never dispatches again, and a changed
// immutable envelope is refused.
func (c *Client) SendOperatorMessage(ctx context.Context, req OperatorMessageRequest) (OperatorMessageResult, error) {
	if err := ctx.Err(); err != nil {
		return OperatorMessageResult{}, err
	}
	client := req.Operator.Client()
	if client == "" || strings.TrimSpace(req.Target) == "" || strings.TrimSpace(req.Payload) == "" {
		return OperatorMessageResult{}, &Refusal{Code: CodeMessageInvalidInput, message: "SendOperatorMessage requires an Operator, a Target and non-empty Payload"}
	}
	if err := operatorclient.Validate(client); err != nil {
		return OperatorMessageResult{}, &Refusal{Code: CodeMessageInvalidInput, message: "SendOperatorMessage Operator names no valid operator client", cause: err}
	}
	if req.TTL == 0 {
		req.TTL = 10 * time.Minute
	}
	if req.TTL < 0 || req.TTL > message.MaxTTL {
		return OperatorMessageResult{}, &Refusal{Code: CodeMessageInvalidInput, message: "SendOperatorMessage TTL must be greater than zero and at most 24h"}
	}
	if c.pathsErr != nil {
		return OperatorMessageResult{}, resourceRefusal(c.pathsErr)
	}
	if c.projmuxExecutable != "" && !filepath.IsAbs(c.projmuxExecutable) {
		return OperatorMessageResult{}, &Refusal{Code: CodeMessageInvalidInput, message: "ProjmuxExecutable must be absolute"}
	}
	result, err := c.peerMessages().SendOperator(ctx, agentmessages.OperatorSendRequest{Client: client, Target: req.Target,
		Payload: req.Payload, MessageRef: req.MessageRef, TTL: req.TTL})
	out := OperatorMessageResult{Receipt: publicMessageReceipt(result.Receipt), ContentBytes: result.ContentBytes}
	if out.Receipt.MessageRef != "" {
		out.Action = agentmessages.ReceiptAction(result.Receipt, result.ContentBytes)
	}
	if err != nil && out.Receipt.MessageRef == "" && errors.Is(err, message.ErrOperatorOriginTargetNotClaude) {
		return out, &Refusal{Code: CodeOperatorOriginTargetNotClaude, message: err.Error(), cause: err}
	}
	return out, messageRefusal(err, out.Receipt)
}
