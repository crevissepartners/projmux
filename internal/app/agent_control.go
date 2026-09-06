package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	localstate "github.com/crevissepartners/projmux/internal/state"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
	intpickercompat "github.com/crevissepartners/projmux/internal/ui/pickercompat"
)

const (
	agentActionSendTurn       = "Send new turn"
	agentActionSteerTurn      = "Steer current turn"
	agentActionInterruptTurn  = "Interrupt current turn"
	agentActionReviewApproval = "Review pending approval"
	agentActionOpenCodex      = "Open Codex"
)

type agentControlLive struct {
	RuntimeID string
	PaneUID   string
	ThreadID  string
	Authority string
	Epoch     string
	Reason    string
}

type agentControlCaller func(context.Context, string, coremetadata.CodexEndpointRef, codexLifecycleIdentity, agentControlRequest) (agentControlResponse, error)
type agentControlPathResolver func() (config.Paths, error)
type agentControlPicker interface {
	Run(intpicker.Options) (intpicker.Result, error)
}

type agentControlBindingLookup interface {
	Live(context.Context, string) (agentControlLive, bool, error)
}

type tmuxAgentControlBindingLookup struct {
	lookup intmetadata.Mirror
	runner tmuxCommandRunner
}

// agentControlBindingFrameError is the typed refusal for a live tmux binding
// that cannot be proven to be one exact six-field frame. Control callers use
// this boundary before resolving the private app-server route, so malformed or
// ambiguous output can never become a partial identity or a control write.
type agentControlBindingFrameError struct {
	Reason     string
	FieldCount int
}

func (e *agentControlBindingFrameError) Error() string {
	if e.FieldCount > 0 {
		return fmt.Sprintf("live Codex control binding is malformed: %s (%d fields, want 6)", e.Reason, e.FieldCount)
	}
	return "live Codex control binding is malformed: " + e.Reason
}

// exactAgentControlBindingError is the typed Registry/live-activation refusal.
// Its text remains the public recovery prefix consumed by the CLI while
// errors.As can distinguish an identity refusal from transport failure.
type exactAgentControlBindingError struct{ Reason string }

func (e *exactAgentControlBindingError) Error() string {
	return "exact Agent native control unavailable: " + e.Reason
}

// parseAgentControlBindingFrame accepts only the two separator spellings tmux
// has emitted for this contract: the literal octal spelling used by tmux 3.4
// and the raw unit-separator used by the supported compatibility fixture. It
// deliberately does not perform general escape decoding. Apart from the one
// command-output line ending, field bytes are returned unchanged.
func parseAgentControlBindingFrame(out []byte) (agentControlLive, error) {
	frame := string(out)
	if before, ok := strings.CutSuffix(frame, "\n"); ok {
		frame = before
		frame = strings.TrimSuffix(frame, "\r")
	}
	if strings.ContainsAny(frame, "\r\n") {
		return agentControlLive{}, &agentControlBindingFrameError{Reason: "multiple output lines"}
	}
	hasEscaped := strings.Contains(frame, tmuxRowSepFormat)
	hasRaw := strings.Contains(frame, tmuxRowSep)
	if hasEscaped && hasRaw {
		return agentControlLive{}, &agentControlBindingFrameError{Reason: "mixed separator spellings"}
	}
	separator := tmuxRowSepFormat
	if hasRaw {
		separator = tmuxRowSep
	} else if !hasEscaped {
		return agentControlLive{}, &agentControlBindingFrameError{Reason: "separator is missing", FieldCount: 1}
	}
	fields := strings.Split(frame, separator)
	if len(fields) != 6 {
		return agentControlLive{}, &agentControlBindingFrameError{Reason: "field count is not exact", FieldCount: len(fields)}
	}
	return agentControlLive{RuntimeID: fields[0], PaneUID: fields[1], ThreadID: fields[2], Authority: fields[3], Epoch: fields[4], Reason: fields[5]}, nil
}

func (l *tmuxAgentControlBindingLookup) Live(ctx context.Context, paneUID string) (agentControlLive, bool, error) {
	target, found, err := l.lookup.FindPaneTargetForUID(ctx, paneUID)
	if err != nil || !found {
		return agentControlLive{}, found, err
	}
	format := tmuxRowFormat("#{pane_id}", "#{@projmux_pane_uid}", "#{"+aiPaneThreadIDOption+"}", "#{"+aiPaneCodexAuthorityOption+"}", "#{"+aiPaneCodexEpochOption+"}", "#{"+aiPaneCodexReasonOption+"}")
	out, err := l.runner.Run(ctx, "tmux", "display-message", "-p", "-t", target, format)
	if err != nil {
		return agentControlLive{}, false, err
	}
	live, err := parseAgentControlBindingFrame(out)
	if err != nil {
		return agentControlLive{}, false, err
	}
	return live, true, nil
}

// codexAuthorityFenceReadPoll is the retry step used while an admission read
// waits for an in-flight authority transition to complete. It only bounds how
// often the wait re-tests the kernel lock; the wait itself ends at the caller's
// control deadline.
const codexAuthorityFenceReadPoll = 2 * time.Millisecond

// codexAuthorityFenceAcquirer takes the exact per-Pane authority fence and
// returns its release. It is the seam that lets admission tests drive one exact
// writer/reader interleaving instead of racing for it.
type codexAuthorityFenceAcquirer func(context.Context, string) (func(), error)

// acquireCodexAuthorityReadFenceIn takes the same kernel fence that
// aiCodexLifecycleSink.SetAuthority holds while it publishes
// @projmux_codex_authority, @projmux_codex_authority_epoch and
// @projmux_codex_authority_reason as three separate tmux set-option calls.
// That fence serializes writers against each other only, so a reader that
// skips it can sample the Pane between two of those three writes and see an
// authority that is already provider-control-plane paired with the epoch or
// reason of the transition being replaced.
//
// Unlike the writer, this acquisition is bounded by ctx. The writer owns the
// transition and must be allowed to finish it, while an admission read that
// cannot reach a settled snapshot inside the control budget refuses with a
// reason instead of stalling `projmux agent turn` behind a wedged transition.
func acquireCodexAuthorityReadFenceIn(ctx context.Context, stateDir, paneUID string) (func(), error) {
	path, err := codexAuthorityFencePathIn(stateDir, paneUID)
	if err != nil {
		return nil, err
	}
	// #nosec G304 -- codexAuthorityFencePathIn returns the private state
	// directory plus a digest of the Registry-authenticated Pane uid.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, localstate.PrivateFileMode)
	if err != nil {
		return nil, fmt.Errorf("open Codex authority fence: %w", err)
	}
	localstate.RepairPrivateFile(path)
	for {
		lockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if lockErr == nil {
			var once sync.Once
			return func() {
				once.Do(func() {
					_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
					_ = file.Close()
				})
			}, nil
		}
		if !errors.Is(lockErr, syscall.EWOULDBLOCK) && !errors.Is(lockErr, syscall.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("lock Codex authority fence: %w", lockErr)
		}
		timer := time.NewTimer(codexAuthorityFenceReadPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = file.Close()
			return nil, fmt.Errorf("wait for a settled Codex authority transition: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

// readSettledAgentControlBinding samples the live tmux binding under the
// writer's per-Pane authority fence, so the (authority, epoch, reason) triple
// handed to admission is one completed transition rather than a torn mid-write
// snapshot. Without it a turn can be refused for an unavailable epoch while the
// native connection is alive, purely because the read landed between the
// authority write and the epoch write of the transition that established it.
//
// The fence deliberately covers the read only, not the admission judgment that
// follows. Widening it would buy nothing this contract does not already
// exclude: the Registry half of the judgment is snapshotted before this read,
// and any change after the fence is released is TOCTOU, which C-5 explicitly
// does not guarantee. Holding a cross-process kernel lock across Registry
// projection would only stall the observer that owns the next transition.
func readSettledAgentControlBinding(ctx context.Context, lookup agentControlBindingLookup, acquire codexAuthorityFenceAcquirer, paneUID string) (agentControlLive, bool, error) {
	if strings.TrimSpace(paneUID) == "" {
		// An Agent with no current Pane has nothing to fence and no live
		// binding to sample. Leaving the read unfenced here keeps the refusal
		// with the Registry judgment that owns it, instead of replacing it
		// with a fence-path error.
		return lookup.Live(ctx, paneUID)
	}
	release, err := acquire(ctx, paneUID)
	if err != nil {
		return agentControlLive{}, false, err
	}
	defer release()
	return lookup.Live(ctx, paneUID)
}

type exactAgentControlBinding struct {
	Identity   codexLifecycleIdentity
	Endpoint   coremetadata.CodexEndpointRef
	Epoch      string
	StateDir   string
	ProjectUID string
	WindowUID  string
	Fence      string
}

func resolveExactAgentControlBinding(registry coremetadata.Registry, agent coremetadata.Agent, live agentControlLive, observed bool, stateDir string) (exactAgentControlBinding, error) {
	refusal := func(reason string) (exactAgentControlBinding, error) {
		return exactAgentControlBinding{}, &exactAgentControlBindingError{Reason: reason}
	}
	if agent.Spec.Provider != aiModeCodex {
		return refusal("the selected Agent is not Codex")
	}
	if agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef == "" {
		return refusal("the selected Agent has no current Running Pane")
	}
	pane, ok := registry.Pane(agent.Status.PaneRef)
	if !ok || pane.Metadata.OwnerUID() != agent.Metadata.UID || pane.Status.Activation.AgentUID != agent.Metadata.UID {
		return refusal("Agent to Pane ownership is not exact")
	}
	threadID := ""
	if pane.Status.Activation.Codex != nil {
		threadID = strings.TrimSpace(pane.Status.Activation.Codex.ThreadID)
	}
	if threadID == "" || pane.Status.Activation.Generation == "" || pane.Status.Activation.RuntimeID == "" {
		return refusal("activation generation or Codex thread identity is missing")
	}
	if agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil ||
		agent.Status.SessionRef.Codex.Endpoint == nil || !agent.Status.SessionRef.Codex.Endpoint.Valid() {
		return refusal(codexNativeReasonLegacyEndpointMissing)
	}
	durableThreadID := strings.TrimSpace(agent.Status.SessionRef.Codex.ThreadID)
	if durableThreadID == "" || durableThreadID != threadID {
		return refusal("durable Codex thread does not match the Pane activation")
	}
	endpoint := *agent.Status.SessionRef.Codex.Endpoint
	if agent.Status.SessionRef.Codex.Lifecycle != nil &&
		!agent.Status.SessionRef.Codex.Lifecycle.ValidFor(&endpoint) {
		return refusal("durable endpoint lifecycle is invalid")
	}
	if pane.Status.Activation.Codex.Authority == nil || !pane.Status.Activation.Codex.Authority.Valid() ||
		!pane.Status.Activation.Codex.Authority.Endpoint().Same(endpoint) {
		return refusal("live activation authority does not match the durable endpoint generation")
	}
	if !observed || live.RuntimeID != pane.Status.Activation.RuntimeID || live.PaneUID != pane.Metadata.UID || live.ThreadID != threadID {
		return refusal("live Pane identity no longer matches the activation")
	}
	if live.Authority != codexAuthorityControlPlane || strings.TrimSpace(live.Epoch) == "" {
		reason := strings.TrimSpace(live.Reason)
		if reason == "" {
			reason = "not-ready"
		}
		return refusal("the native connection epoch is unavailable (" + reason + "); Open Codex")
	}
	window, ok := registry.Window(agent.Metadata.OwnerUID())
	if !ok {
		return refusal("owning Window is missing")
	}
	project, ok := registry.Project(window.Metadata.OwnerUID())
	if !ok {
		return refusal("owning Project is missing")
	}
	lifecycle, durable, authorized := resourceAgentLifecycleProjectionInput(
		registry, agent, pane.Metadata.UID, live.RuntimeID, agent.EffectiveInteraction(time.Now()).Kind,
	)
	consumer := codexgeneration.ProjectConsumers(lifecycle, codexgeneration.RuntimeMutationInput{
		DurableEndpoint: agent.Status.SessionRef.Codex.Endpoint,
		StoredAuthority: pane.Status.Activation.Codex.Authority, PresentedAuthority: pane.Status.Activation.Codex.Authority,
		TargetRuntimeID: pane.Status.Activation.RuntimeID, EventRuntimeID: live.RuntimeID,
	}, true)
	if !durable || !authorized || consumer.Effect != codexgeneration.MutationSemanticEffect ||
		!consumer.Endpoint.Same(endpoint) || consumer.Fence == "" {
		return refusal("canonical generation consumer fence is unavailable")
	}
	return exactAgentControlBinding{
		Identity: codexLifecycleIdentity{AgentUID: agent.Metadata.UID, PaneUID: pane.Metadata.UID, RuntimeID: pane.Status.Activation.RuntimeID, Generation: pane.Status.Activation.Generation, ThreadID: threadID},
		Endpoint: endpoint, Epoch: live.Epoch, StateDir: stateDir, ProjectUID: project.Metadata.UID, WindowUID: window.Metadata.UID,
		Fence: consumer.Fence,
	}, nil
}

func (c *agentCommand) resolveControlBinding(spelling, ref string) (exactAgentControlBinding, error) {
	registry, agent, err := c.resolveOneAgent(spelling, ref, selector.VerbReview)
	if err != nil {
		return exactAgentControlBinding{}, err
	}
	// Static provider support is decided before live tmux lookup, private-path
	// resolution, or provider transport. Unsupported providers therefore cannot
	// acquire runtime authority as a side effect of probing for it.
	if err := requireStaticNativeAgentCapability(spelling, agent); err != nil {
		return exactAgentControlBinding{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.controlTimeoutValue())
	defer cancel()
	lookup := c.controlBinding
	if lookup == nil {
		if c.controlRoute == nil {
			return exactAgentControlBinding{}, errors.New("exact Agent native control unavailable: logical tmux route resolver is not configured")
		}
		route, routeErr := c.controlRoute(ctx)
		if routeErr != nil {
			return exactAgentControlBinding{}, fmt.Errorf("exact Agent native control unavailable: resolve logical tmux route: %w", routeErr)
		}
		if c.controlRunner == nil {
			return exactAgentControlBinding{}, errors.New("exact Agent native control unavailable: exact tmux runner is not configured")
		}
		routed := explicitTmuxRunner{runner: c.controlRunner, target: route.target}
		lookup = &tmuxAgentControlBindingLookup{lookup: intmetadata.NewMirror(routed), runner: routed}
	}
	paths, err := c.controlPaths()
	if err != nil {
		return exactAgentControlBinding{}, fmt.Errorf("exact Agent native control unavailable: resolve private control path: %w", err)
	}
	// The private control path is resolved before the live read because the
	// same state directory holds the authority fence the read must take.
	acquire := func(ctx context.Context, paneUID string) (func(), error) {
		return acquireCodexAuthorityReadFenceIn(ctx, paths.StateDir, paneUID)
	}
	live, observed, err := readSettledAgentControlBinding(ctx, lookup, acquire, agent.Status.PaneRef)
	if err != nil {
		return exactAgentControlBinding{}, fmt.Errorf("exact Agent native control unavailable: read live binding: %w", err)
	}
	binding, err := resolveExactAgentControlBinding(registry, agent, live, observed, paths.StateDir)
	if err != nil {
		return exactAgentControlBinding{}, addOpenCodexRecovery(err, registry, agent)
	}
	return binding, nil
}

func (c *agentCommand) runTurn(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError("agent turn requires start, steer, or interrupt")
	}
	switch args[0] {
	case "start", "steer":
		before, text, err := splitAgentTurnText(args[1:])
		if err != nil {
			return err
		}
		binding, err := c.resolveControlBinding("agent turn "+args[0], before[0])
		if err != nil {
			return err
		}
		op := agentControlOpStart
		label := agentActionSendTurn
		if args[0] == "steer" {
			op, label = agentControlOpSteer, agentActionSteerTurn
		}
		response, err := c.callControl(binding, agentControlRequest{Operation: op, Text: text})
		if err != nil {
			return err
		}
		if err := response.Error(); err != nil {
			return addOpenCodexBindingRecovery(err, binding)
		}
		if op == agentControlOpSteer && (response.Acceptance != agentControlAcceptanceProvider || response.Delivery != agentControlDeliveryUnconfirmed) {
			return addOpenCodexBindingRecovery(&exactAgentControlBindingError{Reason: "turn/steer response did not carry the exact provider-acceptance receipt"}, binding)
		}
		if op == agentControlOpStart {
			if err := c.recordStartedCodexTurn(binding, response); err != nil {
				return err
			}
		}
		if op == agentControlOpSteer {
			_, err = fmt.Fprintf(stdout, "%s thread=%s turn=%s acceptance=%s delivery=%s\n", c.agentActionText(label), safeApprovalDetail(response.ThreadID), safeApprovalDetail(response.TurnID), response.Acceptance, response.Delivery)
			return err
		}
		_, err = fmt.Fprintf(stdout, "%s thread=%s turn=%s\n", c.agentActionText(label), safeApprovalDetail(response.ThreadID), safeApprovalDetail(response.TurnID))
		return err
	case "interrupt":
		if len(args) != 2 {
			return usageError("agent turn interrupt requires exactly one <agent-ref>")
		}
		binding, err := c.resolveControlBinding("agent turn interrupt", args[1])
		if err != nil {
			return err
		}
		response, err := c.callControl(binding, agentControlRequest{Operation: agentControlOpInterrupt})
		if err != nil {
			return err
		}
		if err := response.Error(); err != nil {
			return addOpenCodexBindingRecovery(err, binding)
		}
		_, err = fmt.Fprintf(stdout, "%s thread=%s turn=%s\n", c.agentActionText(agentActionInterruptTurn), safeApprovalDetail(response.ThreadID), safeApprovalDetail(response.TurnID))
		return err
	default:
		return usageError("agent turn requires start, steer, or interrupt")
	}
}

// recordStartedCodexTurn commits only monotonic, content-free evidence after
// the exact control epoch accepted a first input. It reuses the durable
// endpoint/thread and Pane generation from the pre-write binding, so a current
// pointer change cannot retarget the acknowledgement.
func (c *agentCommand) recordStartedCodexTurn(binding exactAgentControlBinding, response agentControlResponse) error {
	if strings.TrimSpace(response.ThreadID) != binding.Identity.ThreadID || strings.TrimSpace(response.TurnID) == "" {
		return addOpenCodexBindingRecovery(&exactAgentControlBindingError{Reason: "turn response did not preserve the exact thread identity"}, binding)
	}
	return c.mutateAgent(binding.Identity.AgentUID, func(registry *coremetadata.Registry, mutator coremetadata.Mutator) error {
		changed, err := mutator.RefineCodexActivation(registry, coremetadata.CodexActivationObservation{
			AgentUID: binding.Identity.AgentUID, PaneUID: binding.Identity.PaneUID,
			Generation: binding.Identity.Generation, ThreadID: binding.Identity.ThreadID,
			TurnID: response.TurnID, Endpoint: binding.Endpoint,
		})
		if err != nil {
			return err
		}
		if !changed {
			agent, ok := registry.Agent(binding.Identity.AgentUID)
			if !ok || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || !agent.Status.SessionRef.Codex.HasStartedTurn {
				return &exactAgentControlBindingError{Reason: "first-turn evidence no longer matches the exact native binding"}
			}
		}
		return nil
	})
}

func splitAgentTurnText(args []string) ([]string, string, error) {
	separator := slices.Index(args, "--")
	if separator != 1 || len(args) != 3 || strings.TrimSpace(args[0]) == "" || strings.TrimSpace(args[2]) == "" {
		return nil, "", usageError("agent turn start|steer requires <agent-ref> -- <text>; quote text as one argument")
	}
	return args[:1], args[2], nil
}

func (c *agentCommand) runApproval(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "review" {
		return usageError("agent approval requires review")
	}
	fs := flag.NewFlagSet("agent approval review", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var requestID string
	fs.StringVar(&requestID, "request", "", "normalized pending request id")
	refs, err := parseWithPositionals(fs, args[1:])
	if err != nil {
		return usageError(err.Error())
	}
	if len(refs) != 1 {
		return usageError("agent approval review requires exactly one <agent-ref>")
	}
	binding, err := c.resolveControlBinding("agent approval review", refs[0])
	if err != nil {
		return err
	}
	response, err := c.callControl(binding, agentControlRequest{Operation: agentControlOpApprovals})
	if err != nil {
		return err
	}
	if err := response.Error(); err != nil {
		return addOpenCodexBindingRecovery(err, binding)
	}
	pending, err := c.selectPendingApproval(response.Approvals, requestID, stderr)
	if err != nil {
		return addOpenCodexBindingRecovery(err, binding)
	}
	decision, open, err := c.selectApprovalDecision(pending, stderr)
	if err != nil {
		return err
	}
	if open {
		return c.focusExactCodex(binding, stdout, stderr)
	}
	response, err = c.callControl(binding, agentControlRequest{Operation: agentControlOpReview, RequestKey: pending.RequestID, Decision: string(decision)})
	if err != nil {
		return err
	}
	if err := response.Error(); err != nil {
		return addOpenCodexBindingRecovery(err, binding)
	}
	_, err = fmt.Fprintf(stdout, "approval resolved request=%s decision=%s\n", safeApprovalDetail(pending.RequestID), safeApprovalDetail(string(decision)))
	return err
}

func (c *agentCommand) callControl(binding exactAgentControlBinding, request agentControlRequest) (agentControlResponse, error) {
	if err := c.revalidateControlConsumerFence(binding); err != nil {
		return agentControlResponse{}, addOpenCodexBindingRecovery(err, binding)
	}
	request.Identity, request.Epoch = binding.Identity, binding.Epoch
	ctx, cancel := context.WithTimeout(context.Background(), c.controlTimeoutValue())
	defer cancel()
	call := c.controlCall
	if call == nil {
		call = callCodexControl
	}
	response, err := call(ctx, binding.StateDir, binding.Endpoint, binding.Identity, request)
	if err != nil {
		return agentControlResponse{}, addOpenCodexBindingRecovery(fmt.Errorf("exact Agent native control unavailable: %w", err), binding)
	}
	return response, nil
}

// revalidateControlConsumerFence consumes the same canonical endpoint/fence
// projection as notification, sidebar, and statusbar immediately before a
// reply, steer, approval, or message reaches the control transport. The
// control epoch still owns provider-side fencing; this read-only guard closes
// the Registry-authority race before that transport is called at all.
func (c *agentCommand) revalidateControlConsumerFence(binding exactAgentControlBinding) error {
	if c.loadRegistry == nil {
		return &exactAgentControlBindingError{Reason: "canonical generation consumer Registry is unavailable"}
	}
	registry, err := c.loadRegistry()
	if err != nil {
		return &exactAgentControlBindingError{Reason: "canonical generation consumer Registry read failed"}
	}
	agent, ok := registry.Agent(binding.Identity.AgentUID)
	if !ok {
		return &exactAgentControlBindingError{Reason: "canonical generation consumer Agent is missing"}
	}
	pane, ok := registry.Pane(binding.Identity.PaneUID)
	if !ok || pane.Status.Activation.Codex == nil || pane.Status.Activation.Codex.Authority == nil {
		return &exactAgentControlBindingError{Reason: "canonical generation consumer Pane authority is missing"}
	}
	lifecycle, durable, authorized := resourceAgentLifecycleProjectionInput(
		registry, *agent, binding.Identity.PaneUID, binding.Identity.RuntimeID, agent.EffectiveInteraction(time.Now()).Kind,
	)
	authority := pane.Status.Activation.Codex.Authority
	consumer := codexgeneration.ProjectConsumers(lifecycle, codexgeneration.RuntimeMutationInput{
		DurableEndpoint: agent.Status.SessionRef.Codex.Endpoint,
		StoredAuthority: authority, PresentedAuthority: authority,
		TargetRuntimeID: pane.Status.Activation.RuntimeID, EventRuntimeID: binding.Identity.RuntimeID,
	}, true)
	if !durable || !authorized || consumer.Effect != codexgeneration.MutationSemanticEffect ||
		!consumer.Endpoint.Same(binding.Endpoint) || consumer.Fence == "" || consumer.Fence != binding.Fence {
		return &exactAgentControlBindingError{Reason: "canonical generation consumer fence is stale"}
	}
	return nil
}

func (c *agentCommand) controlTimeoutValue() time.Duration {
	if c.controlTimeout > 0 {
		return c.controlTimeout
	}
	return 10 * time.Second
}

func (c *agentCommand) selectPendingApproval(pending []agentPendingApproval, requestID string, stderr io.Writer) (agentPendingApproval, error) {
	if requestID != "" {
		pending = slices.DeleteFunc(pending, func(p agentPendingApproval) bool { return p.RequestID != requestID })
	}
	if len(pending) == 0 {
		return agentPendingApproval{}, errors.New("no exact unresolved approval request matches; response write refused")
	}
	counts := map[string]int{}
	for _, p := range pending {
		counts[p.RequestID]++
	}
	if requestID != "" && counts[requestID] != 1 {
		return agentPendingApproval{}, errors.New("normalized request id is ambiguous across raw scalar identities; response write refused")
	}
	if len(pending) == 1 && counts[pending[0].RequestID] == 1 {
		return pending[0], nil
	}
	entries := []intpickercompat.Entry{}
	for i, p := range pending {
		if counts[p.RequestID] > 1 {
			continue
		}
		entries = append(entries, intpickercompat.Entry{Label: boundApprovalLabel(fmt.Sprintf("%s request=%s item=%s", safeApprovalDetail(string(p.Kind)), safeApprovalDetail(p.RequestID), safeApprovalDetail(p.ItemID))), Value: fmt.Sprintf("%d", i)})
	}
	if len(entries) == 0 {
		return agentPendingApproval{}, errors.New("all pending request ids are ambiguous; Open Codex")
	}
	reviewLabel := c.agentActionText(agentActionReviewApproval)
	result, err := runNativePickerOption(os.UserHomeDir, os.Getenv, c.controlPicker, intpickercompat.Options{UI: switchUIPopup, Entries: entries, Title: reviewLabel, Prompt: reviewLabel + " > ", DisableSearch: false})
	if err != nil {
		return agentPendingApproval{}, err
	}
	var index int
	if _, err := fmt.Sscanf(result.Value, "%d", &index); err != nil || index < 0 || index >= len(pending) {
		return agentPendingApproval{}, errors.New("approval selection closed without an exact request")
	}
	return pending[index], nil
}

func (c *agentCommand) selectApprovalDecision(p agentPendingApproval, stderr io.Writer) (codexappserver.ApprovalDecision, bool, error) {
	entries := []intpickercompat.Entry{{Label: approvalDetailLabel(p), Value: settingsNoopValue}}
	for _, decision := range p.Decisions {
		entries = append(entries, intpickercompat.Entry{Label: approvalDecisionLabelLocale(appLocale(os.UserHomeDir, os.Getenv), p, decision), Value: "decision:" + string(decision)})
	}
	entries = append(entries, intpickercompat.Entry{Label: c.agentActionText(agentActionOpenCodex) + " — focus exact Agent; send no response", Value: "open"})
	reviewLabel := c.agentActionText(agentActionReviewApproval)
	result, err := runNativePickerOption(os.UserHomeDir, os.Getenv, c.controlPicker, intpickercompat.Options{UI: switchUIPopup, Entries: entries, Title: reviewLabel, Prompt: reviewLabel + " > ", DisableSearch: true})
	if err != nil {
		return "", false, err
	}
	if result.Value == "open" {
		return "", true, nil
	}
	if !strings.HasPrefix(result.Value, "decision:") {
		return "", false, errors.New("approval decision picker closed without a decision")
	}
	decision := codexappserver.ApprovalDecision(strings.TrimPrefix(result.Value, "decision:"))
	if !slices.Contains(p.Decisions, decision) {
		return "", false, errors.New("selected approval decision is no longer available")
	}
	return decision, false, nil
}

func approvalDetailLabel(p agentPendingApproval) string {
	base := fmt.Sprintf("%s request=%s thread=%s turn=%s item=%s", p.Kind, safeApprovalDetail(p.RequestID), safeApprovalDetail(p.ThreadID), safeApprovalDetail(p.TurnID), safeApprovalDetail(p.ItemID))
	if p.ApprovalID != nil {
		base += " approvalId=" + safeApprovalDetail(*p.ApprovalID)
	}
	if p.Reason != "" {
		base += " reason=" + safeApprovalDetail(p.Reason)
	}
	switch p.Kind {
	case codexappserver.ApprovalCommand:
		base += " command=" + safeApprovalDetail(p.Command) + " cwd=" + safeApprovalDetail(p.CWD)
		if p.NetworkHost != "" {
			base += " network=" + safeApprovalDetail(p.NetworkProtocol+"://"+p.NetworkHost)
		}
	case codexappserver.ApprovalFileChange:
		if p.GrantRoot != nil {
			base += " unstableGrantRoot=" + safeApprovalDetail(*p.GrantRoot)
		}
	case codexappserver.ApprovalPermissions:
		base += " cwd=" + safeApprovalDetail(p.RequestCWD) + " permissions=" + safeApprovalDetail(string(p.Permissions))
	}
	return boundRenderedText(base, 640)
}

func approvalDecisionLabelLocale(locale i18n.Locale, p agentPendingApproval, decision codexappserver.ApprovalDecision) string {
	target := approvalDecisionTarget(p)
	switch decision {
	case codexappserver.DecisionAccept:
		effect := localizeText(locale, i18n.KeyAgentControlDecisionAccept, "Allow once — only this {kind} request")
		return boundApprovalLabel(strings.ReplaceAll(effect, "{kind}", string(p.Kind)) + "; " + target)
	case codexappserver.DecisionDecline:
		return boundApprovalLabel(localizeText(locale, i18n.KeyAgentControlDecisionDecline, "Decline — deny once and continue the turn") + "; " + target)
	case codexappserver.DecisionCancel:
		return boundApprovalLabel(localizeText(locale, i18n.KeyAgentControlDecisionCancel, "Decline and interrupt — deny once and interrupt this exact turn") + "; " + target)
	case codexappserver.DecisionGrantTurn:
		return boundApprovalLabel(localizeText(locale, i18n.KeyAgentControlDecisionGrant, "Grant requested permissions for this turn — scope=turn; strictAutoReview=null") + "; " + target)
	default:
		return boundApprovalLabel(safeApprovalDetail(string(decision)) + " — unavailable; " + target)
	}
}

func approvalDecisionTarget(p agentPendingApproval) string {
	switch p.Kind {
	case codexappserver.ApprovalCommand:
		target := "command=" + safeApprovalDetail(p.Command) + " cwd=" + safeApprovalDetail(p.CWD)
		if p.NetworkHost != "" || p.NetworkProtocol != "" {
			target += " network=" + safeApprovalDetail(p.NetworkProtocol+"://"+p.NetworkHost)
		}
		return target
	case codexappserver.ApprovalFileChange:
		target := "item=" + safeApprovalDetail(p.ItemID)
		if p.Reason != "" {
			target += " reason=" + safeApprovalDetail(p.Reason)
		}
		if p.GrantRoot != nil {
			target += " unstableGrantRoot=" + safeApprovalDetail(*p.GrantRoot)
		}
		return target
	case codexappserver.ApprovalPermissions:
		return "cwd=" + safeApprovalDetail(p.RequestCWD) + " permissions=" + safeApprovalDetail(string(p.Permissions))
	default:
		return "request=" + safeApprovalDetail(p.RequestID)
	}
}

func safeApprovalDetail(value string) string {
	runes := []rune(value)
	truncated := false
	if len(runes) > 256 {
		runes, truncated = runes[:256], true
	}
	quoted := strconv.QuoteToGraphic(string(runes))
	if truncated {
		quoted += "…[truncated]"
	}
	return boundRenderedText(quoted, 320)
}

func boundApprovalLabel(value string) string { return boundRenderedText(value, 768) }

func boundRenderedText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	suffix := []rune("…[truncated]")
	if limit <= len(suffix) {
		return string(suffix[:limit])
	}
	return string(runes[:limit-len(suffix)]) + string(suffix)
}

func (c *agentCommand) agentActionText(fallback string) string {
	key := map[string]i18n.Key{
		agentActionSendTurn: i18n.KeyAgentControlSendTurn, agentActionSteerTurn: i18n.KeyAgentControlSteerTurn,
		agentActionInterruptTurn: i18n.KeyAgentControlInterruptTurn, agentActionReviewApproval: i18n.KeyAgentControlReviewApproval,
		agentActionOpenCodex: i18n.KeyAgentControlOpenCodex,
	}[fallback]
	if key == "" {
		return fallback
	}
	return localizeText(appLocale(os.UserHomeDir, os.Getenv), key, fallback)
}

func (c *agentCommand) focusExactCodex(binding exactAgentControlBinding, stdout, stderr io.Writer) error {
	if c.focus == nil {
		return fmt.Errorf("%s unavailable: exact focus route is not configured", c.agentActionText(agentActionOpenCodex))
	}
	return c.focus.Run([]string{"pane", "uid:" + binding.Identity.PaneUID, "--project", "uid:" + binding.ProjectUID, "--window", "uid:" + binding.WindowUID}, stdout, stderr)
}

func addOpenCodexBindingRecovery(err error, binding exactAgentControlBinding) error {
	return fmt.Errorf("%w; %s: `projmux focus pane uid:%s --project uid:%s --window uid:%s`", err, agentActionOpenCodex, binding.Identity.PaneUID, binding.ProjectUID, binding.WindowUID)
}

func addOpenCodexRecovery(err error, registry coremetadata.Registry, agent coremetadata.Agent) error {
	pane, ok := registry.Pane(agent.Status.PaneRef)
	if !ok {
		return err
	}
	window, ok := registry.Window(agent.Metadata.OwnerUID())
	if !ok {
		return err
	}
	project, ok := registry.Project(window.Metadata.OwnerUID())
	if !ok {
		return err
	}
	return addOpenCodexBindingRecovery(err, exactAgentControlBinding{Identity: codexLifecycleIdentity{PaneUID: pane.Metadata.UID}, ProjectUID: project.Metadata.UID, WindowUID: window.Metadata.UID})
}
