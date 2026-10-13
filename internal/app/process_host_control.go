package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/core/selector"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	"os"
	"regexp"
	"runtime/debug"
	"slices"
	"sync"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/agentquestion"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// The kernel's per-user socket credentials admit short-lived terminal clients.
// Requests carry exact target authority, never claimed caller credentials.
type processForegroundRequest struct {
	Authority                       processhost.Authority
	Action, Operation, Prompt, Turn string
	MessageRef                      string
	Token                           processForegroundToken
	Response                        processhost.Response
	Decision                        codexappserver.ApprovalDecision
	Selections                      map[int]agentquestion.Selection
}

// processQuestionRequest is a question-only private operation. The opaque ID
// selects the host's pending token; client selections never become argv or logs.
type processQuestionRequest struct {
	Binding    processhost.Binding
	QuestionID string
	Selections map[int]agentquestion.Selection
}

// Preserve the exact provider input bytes across JSON transport. RawMessage
// encoding compacts whitespace, which would invalidate the host's exact token.
type processForegroundToken struct {
	processhost.Request
	Input []byte
}

func (t processForegroundToken) request() processhost.Request {
	r := t.Request
	r.Input = t.Input
	return r
}

type processForegroundResult struct {
	Accepted            bool
	InvalidAnswer       bool
	Questions           []ExactProcessQuestion `json:",omitempty"`
	Stale, Busy, Closed bool
	// Unsupported names an action this owner does not implement. A client
	// built before it reads the refusal as stale.
	Unsupported  bool                          `json:",omitempty"`
	Observation  *processHostObservation       `json:",omitempty"`
	Receipt      *codexProcessReceipt          `json:",omitempty"`
	UserDelivery *processhost.UserTurnDelivery `json:",omitempty"`
	// Join is set only when a Claude operator input entered an already
	// running turn; Accepted keeps its meaning. BusyReason names a bounded
	// refusal (joined limit, pending answer) without changing Busy.
	Join          *processTurnJoin `json:",omitempty"`
	DeleteRefusal string           `json:",omitempty"`
	BusyReason    string           `json:",omitempty"`
}

// Control protocol v1. An owner reports these in its observation; an answer
// without Protocol comes from an owner built before the protocol had a version
// and is read as protocol 0.
const (
	processHostProtocol = 1
	// processHostOwnerForeground is the owner mode of a host whose lifetime is
	// its foreground creator, resumer, or relauncher.
	processHostOwnerForeground = "foreground"
	processHostOwnerDetached   = "detached"
)

// errProcessHostUnsupportedAction refuses an action the owner does not
// implement, so a newer client is not told its authority went stale.
var errProcessHostUnsupportedAction = errors.New("process-host-unsupported-action")

// The foreground actions each provider host implements, sorted. The same list
// is reported in observation and decides the unsupported-action refusal.
var (
	claudeForegroundActions = []string{"delete-stop", "interrupt", "respond", "stop", "turn"}
	codexForegroundActions  = []string{"approval", "delete-stop", "interrupt", "message", "question", "stop", "turn", "user-deliver", "user-steer", "validate"}
)

func processForegroundSupported(actions []string, action string) error {
	if !slices.Contains(actions, action) {
		return errProcessHostUnsupportedAction
	}
	return nil
}

var processRevisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// processHostRevision is the owner's own build revision: a 40-hex vcs.revision
// or empty when the binary was built without one.
var processHostRevision = sync.OnceValue(func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && processRevisionPattern.MatchString(setting.Value) {
			return setting.Value
		}
	}
	return ""
})

// processTurnJoin names the running turn an accepted input joined.
type processTurnJoin struct {
	Turn, Origin string
}

// Named Claude refusals that keep Busy and add an operator-visible reason.
const (
	processBusyJoinLimit        = "joined-input-limit"
	processBusyControlPending   = "control-pending"
	processBusyJoinUnsupported  = "join-unsupported"
	processBusyTurnNotOpen      = "turn-not-open"
	processBusyMessageHandoff   = "message-handoff-pending"
	processBusyHandoffExpired   = "message-handoff-expired"
	processBusyInterruptPending = "interrupt-pending"
	processBusyEventLimit       = "event-limit"
)

var processBusyReasons = []struct {
	err    error
	reason string
}{
	{processhost.ErrClaudeJoinLimit, processBusyJoinLimit},
	{processhost.ErrClaudeControlPending, processBusyControlPending},
	{processhost.ErrClaudeJoinUnsupported, processBusyJoinUnsupported},
	{processhost.ErrClaudeTurnNotOpen, processBusyTurnNotOpen},
	{processhost.ErrClaudeMessageHandoff, processBusyMessageHandoff},
	{processhost.ErrClaudeMessageHandoffExpired, processBusyHandoffExpired},
	{processhost.ErrClaudeInterruptPending, processBusyInterruptPending},
	{processhost.ErrClaudeEventLimit, processBusyEventLimit},
}

func processBusyReason(err error) string {
	for _, known := range processBusyReasons {
		if errors.Is(err, known.err) {
			return known.reason
		}
	}
	return ""
}

func controlProcessForeground(ctx context.Context, peer coremetadata.ProcessIdentity, request processForegroundRequest, current func(context.Context, processhost.Authority) error, apply func() error) processForegroundResult {
	result := processForegroundResult{Stale: true}
	if ctx.Err() != nil || !peer.Valid() || int64(peer.OwnerUID) != int64(os.Getuid()) {
		return result
	}
	identity, _, err := localipc.Process(peer.PID)
	if err != nil || identity != peer || current(ctx, request.Authority) != nil {
		return result
	}
	err = apply()
	var failure *mutations.Failure
	if errors.As(err, &failure) && failure.Code == mutations.CodeAgentProtected {
		return processForegroundResult{DeleteRefusal: err.Error()}
	}
	return processForegroundResult{Accepted: err == nil, Stale: errors.Is(err, processhost.ErrStale), Busy: errors.Is(err, processhost.ErrBusy), Closed: errors.Is(err, processhost.ErrClosed), Unsupported: errors.Is(err, errProcessHostUnsupportedAction)}
}

// applyClaudeForeground records how an accepted operator input entered the
// stream in admission; other actions leave it untouched.
func applyClaudeForeground(ctx context.Context, handle *processhost.Handle, r processForegroundRequest, admission *processhost.TurnAdmission) error {
	if err := processForegroundSupported(claudeForegroundActions, r.Action); err != nil {
		return err
	}
	switch r.Action {
	case "turn":
		value, err := handle.UserInput(ctx, r.Authority, r.Operation, r.Prompt)
		if err == nil && admission != nil {
			*admission = value
		}
		return err
	case "interrupt":
		return handle.Interrupt(ctx, r.Authority, r.Turn)
	case "respond":
		return handle.Respond(ctx, r.Authority, r.Token.request(), r.Response)
	case "stop", "delete-stop":
		return handle.Stop(r.Authority.Binding)
	default:
		return processhost.ErrStale
	}
}

func applyCodexForeground(ctx context.Context, handle *processhost.CodexHandle, r processForegroundRequest) error {
	switch r.Action {
	case "turn":
		return handle.Turn(ctx, r.Authority, r.Operation, r.Prompt)
	case "interrupt":
		return handle.Interrupt(ctx, r.Authority, r.Turn)
	case "question":
		return handle.RespondQuestion(ctx, r.Authority, r.Token.request(), r.Selections)
	case "approval":
		return handle.RespondApproval(ctx, r.Authority, r.Token.request(), r.Decision)
	case "stop", "delete-stop":
		return handle.Stop(r.Authority.Binding)
	default:
		return processhost.ErrStale
	}
}

// Both sides verify exact kernel birth identity. Socket inode replacement and
// PID reuse are refusal, never a reason to rediscover another host or replay.
func callProcessForeground(ctx context.Context, socket string, socketIdentity localipc.SocketIdentity, host coremetadata.ProcessIdentity, request any) (processForegroundResult, error) {
	var result processForegroundResult
	err := localipc.ExchangeOwnedJSON(ctx, socket, socketIdentity, host, request, &result)
	if errors.Is(err, localipc.ErrSocketReplaced) {
		err = processhost.ErrStale
	}
	return result, err
}

// Delete Stop is a separate internal admission action: ordinary Stop keeps its
// existing contract. The host takes the Registry lock only around the bounded
// Stop admission, then releases it before its asynchronous Wait writer runs.
type processDeleteAdmissionKey struct{}
type processDeleteAdmission struct {
	Kind      coremetadata.Kind
	UIDs      []string
	Signature string
	// Admitted is carried only by the same in-process delete operation after its
	// first owner accepted Stop. It never comes from a user-facing bypass flag.
	Admitted bool
}

func admitProcessDeleteStop(path string, request processForegroundRequest, apply func() error) error {
	if request.Action != "delete-stop" {
		return apply()
	}
	var admission processDeleteAdmission
	if err := json.Unmarshal([]byte(request.Prompt), &admission); err != nil {
		return processhost.ErrStale
	}
	if len(admission.UIDs) == 0 {
		return processhost.ErrStale
	}
	return intmetadata.NewStore(path).WithAdmissionBarrier(func(reg coremetadata.Registry) error {
		binding := metadataProcessBinding(request.Authority.Binding)
		if _, _, ok := reg.CurrentProcessActivation(binding); !ok {
			// Starting hosts have a durable reservation before SessionStart
			// publishes a complete activation. Keep their existing Stop admission.
			pane, paneOK := reg.Pane(binding.PaneUID)
			agent, agentOK := reg.Agent(binding.AgentUID)
			if !paneOK || !agentOK || !pane.Status.Activation.IsZero() || pane.Spec.Runtime.EffectiveKind() != coremetadata.RuntimeProcess ||
				pane.Status.ProcessSession == nil || pane.Status.ProcessSession.Binding != binding ||
				pane.Metadata.OwnerUID() != binding.AgentUID || agent.Status.PaneRef != binding.PaneUID ||
				agent.Metadata.OwnerUID() != binding.WindowUID || agent.Status.Phase != coremetadata.PhaseRunning {
				return processhost.ErrStale
			}
		}
		resolution := selector.Resolution{Kind: admission.Kind}
		for _, uid := range admission.UIDs {
			resolution.Matches = append(resolution.Matches, selector.Match{UID: uid})
		}
		plan := mutations.BuildDeletePlan(reg, admission.Kind, resolution)
		if err := mutations.ValidateDeleteProtection(reg, plan); !admission.Admitted && err != nil {
			return err
		}
		if plan.Signature() != admission.Signature {
			return processhost.ErrStale
		}
		// The requested host must be one of the Agents this exact plan removes.
		included := false
		for _, t := range plan.Targets {
			if plan.Kind == coremetadata.KindAgent && t.Match.UID == request.Authority.Binding.Agent {
				included = true
			}
			for _, d := range t.Descendants {
				if d.Kind == coremetadata.KindAgent && d.UID == request.Authority.Binding.Agent {
					included = true
				}
			}
		}
		if !included {
			return fmt.Errorf("process delete admission does not include this Agent: %w", processhost.ErrStale)
		}
		return apply()
	})
}
