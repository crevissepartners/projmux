package projmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	message "github.com/crevissepartners/projmux/internal/core/agentmessage"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agentcontrol"
	store "github.com/crevissepartners/projmux/internal/integrations/agents/agentmessage"
	registry "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/agentmessages"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeMessageInvalidInput     Code = "message-invalid-input"
	CodeMessageNotFound         Code = "message-not-found"
	CodeMessageRetryMismatch    Code = "message-retry-mismatch"
	CodeMessageRouteUnavailable Code = "message-route-unavailable"
	CodeMessageDeliveryFailed   Code = "message-delivery-failed"
)

// CodeOperatorOriginTargetNotClaude classifies the refusal of operator input
// to any target but Claude. It is the reason string that refusal carries.
const CodeOperatorOriginTargetNotClaude Code = message.ReasonOperatorOriginTargetNotClaude

// ErrOperatorOriginTargetNotClaude matches, under errors.Is, the existing
// refusal of operator input to any target but Claude. It classifies an error
// only; the SDK adds no operator send path.
var ErrOperatorOriginTargetNotClaude = message.ErrOperatorOriginTargetNotClaude

// SendMessageRequest names exact source and target Agents explicitly. References
// are names or uid: values, resolved to exactly one Agent. Source is an unverified
// coordination anchor, never caller authentication or operator authority.
// TTL zero selects the CLI's ten-minute default; valid TTLs are at most 24 hours.
type SendMessageRequest struct {
	Source, Target, Text string
	MessageRef, ReplyTo  string
	TTL                  time.Duration
}

// SendMessage submits untrusted peer coordination using the existing exact
// native/process channel. Same-ref retries never dispatch again; a changed
// immutable envelope refuses. Inspect a partial receipt on error and never
// automatically retry an uncertain outcome.
func (c *Client) SendMessage(ctx context.Context, req SendMessageRequest) (core.MessageReceipt, error) {
	if err := ctx.Err(); err != nil {
		return core.MessageReceipt{}, err
	}
	if strings.TrimSpace(req.Source) == "" || strings.TrimSpace(req.Target) == "" || strings.TrimSpace(req.Text) == "" {
		return core.MessageReceipt{}, &Refusal{Code: CodeMessageInvalidInput, message: "SendMessage requires explicit Source, Target and non-empty Text"}
	}
	if req.TTL == 0 {
		req.TTL = 10 * time.Minute
	}
	if req.TTL < 0 || req.TTL > message.MaxTTL {
		return core.MessageReceipt{}, &Refusal{Code: CodeMessageInvalidInput, message: "SendMessage TTL must be greater than zero and at most 24h"}
	}
	if c.pathsErr != nil {
		return core.MessageReceipt{}, resourceRefusal(c.pathsErr)
	}
	if c.projmuxExecutable != "" && !filepath.IsAbs(c.projmuxExecutable) {
		return core.MessageReceipt{}, &Refusal{Code: CodeMessageInvalidInput, message: "ProjmuxExecutable must be absolute"}
	}
	result, err := c.peerMessages().Send(ctx, agentmessages.SendRequest{Source: req.Source, Target: req.Target, Text: req.Text, MessageRef: req.MessageRef, ReplyTo: req.ReplyTo, TTL: req.TTL})
	out := publicMessageReceipt(result.Receipt)
	return out, messageRefusal(err, out)
}

// MessageStatus reads the original receipt without resending. As in the CLI,
// nonterminal reads may persist expiry, stale routing or provider status.
func (c *Client) MessageStatus(ctx context.Context, ref string) (core.MessageReceipt, error) {
	if err := ctx.Err(); err != nil {
		return core.MessageReceipt{}, err
	}
	if !message.ValidRef(ref) {
		return core.MessageReceipt{}, &Refusal{Code: CodeMessageInvalidInput, message: "MessageStatus requires one valid message reference"}
	}
	if c.pathsErr != nil {
		return core.MessageReceipt{}, resourceRefusal(c.pathsErr)
	}
	result, err := c.peerMessages().Status(ctx, ref)
	out := publicMessageReceipt(result)
	return out, messageRefusal(err, out)
}

func (c *Client) peerMessages() agentmessages.Service {
	load := func() (metadata.Registry, error) { return resources.Load(c.paths) }
	service := agentmessages.New(agentmessages.Options{RegistryPath: registry.PathFor(c.paths.StateDir), StateDir: c.paths.StateDir, LoadRegistry: load,
		Control: agentcontrol.Client{Runner: frozenTurnRunner{c}, StateDir: c.paths.StateDir, Transport: c.transport, LoadRegistry: load},
		Executable: func() (string, error) {
			if c.projmuxExecutable == "" {
				return "", errors.New("claude peer messages require Options.ProjmuxExecutable for held-message release")
			}
			return c.projmuxExecutable, nil
		},
		Release: func(agent string) error {
			return agentmessages.LaunchRelease(agentmessages.ReleaseOptions{Executable: c.projmuxExecutable, ConfigDir: c.paths.ConfigDir, StateDir: c.paths.StateDir, HomeDir: c.discoveryHome, SocketPath: c.runtimeSocketPath, Path: c.agentEnvironment["PATH"]}, agent)
		},
	})
	// Validate SDK-only prerequisites immediately after exact target selection,
	// before live route probes, deferred acceptance or receipt writes. The CLI
	// retains its existing executable discovery and release failure behavior.
	resolve := service.ResolveAgent
	service.ResolveAgent = func(ctx context.Context, reg metadata.Registry, ref, spelling string) (metadata.Agent, error) {
		agent, err := resolve(ctx, reg, ref, spelling)
		if err == nil && agent.Spec.Provider == "claude" {
			info, executableErr := os.Stat(c.projmuxExecutable)
			if executableErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				return metadata.Agent{}, &Refusal{Code: CodeMessageInvalidInput, message: "Claude peer messages require an existing executable absolute Options.ProjmuxExecutable of this build", cause: executableErr}
			}
		}
		return agent, err
	}
	return service
}
func publicMessageReceipt(r agentmessages.Receipt) core.MessageReceipt {
	route := func(r message.Route) core.MessageRoute {
		return core.MessageRoute{AgentUID: r.AgentUID, PaneUID: r.PaneUID, ActivationGeneration: r.ActivationGeneration, Provider: r.Provider, Incarnation: r.Incarnation}
	}
	d := r.Delivery
	return core.MessageReceipt{Version: r.Version, MessageRef: r.MessageRef, ConversationRef: r.ConversationRef, ReplyTo: r.ReplyTo, Origin: core.MessageOrigin{Kind: r.Origin.Kind, Client: r.Origin.Client}, Source: route(r.Source), Target: route(r.Target), Deadline: r.Deadline,
		Delivery: core.MessageDelivery{MessageRef: d.MessageRef, ConversationRef: d.ConversationRef, State: core.MessageState(d.State), Reason: d.Reason, OutcomeUnknown: d.OutcomeUnknown, AcceptedAt: d.AcceptedAt, TerminalAt: d.TerminalAt}}
}
func messageRefusal(err error, r core.MessageReceipt) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	mapped := resourceRefusal(err)
	var refusal *Refusal
	if errors.As(mapped, &refusal) {
		return mapped
	}
	code := CodeMessageRouteUnavailable
	switch {
	case r.MessageRef != "":
		// A confirmed acceptance or uncertain RPC result is a delivery failure,
		// even when its later receipt read reports not-found. It must not look
		// like a refusal before any message existed.
		code = CodeMessageDeliveryFailed
	case errors.Is(err, store.ErrNotFound):
		code = CodeMessageNotFound
	case errors.Is(err, message.ErrRetryMismatch):
		code = CodeMessageRetryMismatch
	case errors.Is(err, message.ErrInvalidEnvelope):
		code = CodeMessageInvalidInput
	}
	return &Refusal{Code: code, message: err.Error(), cause: err}
}
