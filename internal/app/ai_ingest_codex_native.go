package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/version"
)

const (
	aiPaneCodexAuthorityOption = "@projmux_codex_authority"
	aiPaneCodexEpochOption     = "@projmux_codex_authority_epoch"
	aiPaneCodexReasonOption    = "@projmux_codex_authority_reason"

	codexAuthorityPending      = "pending"
	codexAuthorityControlPlane = "provider-control-plane"
	codexAuthorityInvalidating = "invalidating"
	codexAuthorityHook         = "provider-hook"

	codexObserverReconnectDelay = time.Second
	codexObserverBindingDelay   = 25 * time.Millisecond
	codexObserverBindingTimeout = 3 * time.Second
)

type codexLifecycleConnection interface {
	Notifications() <-chan codexappserver.Notification
	LifecycleEventsAvailable() bool
	ReadLifecycleSnapshot(context.Context, string) (codexappserver.LifecycleSnapshot, error)
	Close() error
}

type codexLifecycleSink interface {
	BindingCurrent(codexLifecycleIdentity) bool
	SetAuthority(codexLifecycleIdentity, string, string, string) error
	Apply(codexLifecycleIdentity, codexLifecycleProjection) error
}

type codexNativeLifecycleStarter interface {
	startNativeCodexLifecycleObserver(codexLifecycleIdentity)
}

type codexNativeObserver struct {
	identity       codexLifecycleIdentity
	open           func(context.Context) (codexLifecycleConnection, error)
	sink           codexLifecycleSink
	delay          time.Duration
	bindingTimeout time.Duration
	sequence       uint64
	reducer        codexLifecycleReducer
	startControl   func(*codexControlEpoch) (*codexControlServer, error)
}

func (o *codexNativeObserver) Run(ctx context.Context) error {
	if !o.identity.valid() || o.open == nil || o.sink == nil {
		return errors.New("codex native lifecycle observer is not configured")
	}
	delay := o.delay
	if delay <= 0 {
		delay = codexObserverReconnectDelay
	}
	for ctx.Err() == nil {
		bindingTimeout := o.bindingTimeout
		if bindingTimeout <= 0 {
			bindingTimeout = codexObserverBindingTimeout
		}
		if !waitForCodexLifecycleBinding(ctx, o.sink, o.identity, bindingTimeout) {
			if ctx.Err() == nil {
				// SetAuthority repeats the exact binding predicate. A still-current
				// startup may fall back; a replaced runtime writes nothing.
				_ = o.sink.SetAuthority(o.identity, codexAuthorityHook, "", "observer-unavailable")
			}
			return nil
		}
		client, err := o.open(ctx)
		if err != nil {
			_ = o.sink.SetAuthority(o.identity, codexAuthorityHook, "", codexNativeReason(err))
			if !waitCodexObserver(ctx, delay) {
				return nil
			}
			continue
		}
		if !client.LifecycleEventsAvailable() {
			_ = client.Close()
			_ = o.sink.SetAuthority(o.identity, codexAuthorityHook, "", "unsupported")
			if !waitCodexObserver(ctx, delay) {
				return nil
			}
			continue
		}
		o.sequence++
		epoch := o.sequence
		epochLabel := fmt.Sprintf("%d-%d", os.Getpid(), epoch)
		snapshotCtx, cancel := context.WithTimeout(ctx, codexappserver.DefaultProbeTimeout)
		snapshot, snapshotErr := client.ReadLifecycleSnapshot(snapshotCtx, o.identity.ThreadID)
		cancel()
		if snapshotErr != nil || !o.sink.BindingCurrent(o.identity) {
			_ = client.Close()
			if snapshotErr != nil {
				_ = o.sink.SetAuthority(o.identity, codexAuthorityHook, "", codexNativeReason(snapshotErr))
			}
			if !waitCodexObserver(ctx, delay) {
				return nil
			}
			continue
		}
		projection := o.reducer.begin(epoch, o.identity, snapshot)
		if !projection.Accepted {
			_ = client.Close()
			_ = o.sink.SetAuthority(o.identity, codexAuthorityHook, "", "protocol-error")
			return errors.New("codex native lifecycle snapshot did not match exact binding")
		}
		if projection.Invalidated {
			transitionErr := o.applyInvalidationAndFallback(epochLabel, "thread-unloaded", projection)
			_ = client.Close()
			if transitionErr != nil {
				return transitionErr
			}
			if !waitCodexObserver(ctx, delay) {
				return nil
			}
			continue
		}
		if err := o.sink.Apply(o.identity, projection); err != nil {
			cleanupErr := o.invalidateAndFallback(epoch, epochLabel, "sink-error")
			_ = client.Close()
			return errors.Join(err, cleanupErr)
		}
		var control *codexControlServer
		if wire, ok := client.(agentControlWire); ok && o.startControl != nil {
			controlEpoch := newCodexControlEpoch(wire, o.identity, epochLabel, snapshot, o.sink.BindingCurrent)
			control, err = o.startControl(controlEpoch)
			if err != nil {
				controlEpoch.Revoke()
				control = nil
			}
		}
		if err := o.sink.SetAuthority(o.identity, codexAuthorityControlPlane, epochLabel, "ready"); err != nil {
			if control != nil {
				_ = control.Close()
			}
			cleanupErr := o.invalidateAndFallback(epoch, epochLabel, "sink-error")
			_ = client.Close()
			return errors.Join(err, cleanupErr)
		}

		reconnectReason := "disconnected"
		invalidated := false
		stopAfterTransition := false
		bindingTicker := time.NewTicker(codexObserverBindingDelay)
		notifications := client.Notifications()
	eventLoop:
		for {
			select {
			case <-ctx.Done():
				stopAfterTransition = true
				break eventLoop
			case <-bindingTicker.C:
				if !o.sink.BindingCurrent(o.identity) {
					bindingTicker.Stop()
					if control != nil {
						_ = control.Close()
					}
					_ = client.Close()
					return nil
				}
			case notification, open := <-notifications:
				if !open {
					break eventLoop
				}
				if control != nil {
					if controlErr := control.epoch.ApplyNotification(notification); controlErr != nil {
						reconnectReason = "protocol-error"
						break eventLoop
					}
				}
				event, recognized, decodeErr := codexappserver.DecodeLifecycleEvent(notification)
				if decodeErr != nil {
					reconnectReason = "protocol-error"
					break eventLoop
				}
				if !recognized {
					continue
				}
				projection = o.reducer.apply(epoch, event)
				if !projection.Accepted {
					continue
				}
				responderAvailable := false
				if control != nil {
					responderAvailable = control.epoch.HasActionableRequest(event.RequestID)
				}
				markCodexApprovalAvailability(&projection, responderAvailable)
				if projection.Invalidated {
					reconnectReason = "thread-unloaded"
					if control != nil {
						_ = control.Close()
						control = nil
					}
					if err := o.applyInvalidationAndFallback(epochLabel, reconnectReason, projection); err != nil {
						bindingTicker.Stop()
						_ = client.Close()
						return err
					}
					invalidated = true
					break eventLoop
				}
				if err := o.sink.Apply(o.identity, projection); err != nil {
					if control != nil {
						_ = control.Close()
						control = nil
					}
					cleanupErr := o.invalidateAndFallback(epoch, epochLabel, "sink-error")
					bindingTicker.Stop()
					_ = client.Close()
					return errors.Join(err, cleanupErr)
				}
			}
		}
		bindingTicker.Stop()
		if control != nil {
			_ = control.Close()
		}
		_ = client.Close()
		if !invalidated {
			if err := o.invalidateAndFallback(epoch, epochLabel, reconnectReason); err != nil {
				return err
			}
		}
		if stopAfterTransition {
			return nil
		}
		if !waitCodexObserver(ctx, delay) {
			return nil
		}
	}
	return nil
}

// invalidateAndFallback is the only active-epoch cleanup path. Fallback is
// enabled only after the first accepted invalidation projection clears stale
// Registry/tmux/queue state. If either write fails, invalidating remains the
// current hook-suppressing authority.
func (o *codexNativeObserver) invalidateAndFallback(epoch uint64, epochLabel, reason string) error {
	projection := o.reducer.invalidate(epoch)
	if !projection.Accepted {
		return errors.New("codex native lifecycle epoch could not be invalidated")
	}
	return o.applyInvalidationAndFallback(epochLabel, reason, projection)
}

func (o *codexNativeObserver) applyInvalidationAndFallback(epochLabel, reason string, projection codexLifecycleProjection) error {
	if !projection.Accepted || !projection.Invalidated {
		return errors.New("codex native lifecycle invalidation projection is not accepted")
	}
	if err := o.sink.SetAuthority(o.identity, codexAuthorityInvalidating, epochLabel, reason); err != nil {
		return err
	}
	if err := o.sink.Apply(o.identity, projection); err != nil {
		// The clear may have failed after invalidating became current. Keep hooks
		// suppressed and make the bounded diagnostic truthful; never expose
		// provider-hook while stale state may remain.
		_ = o.sink.SetAuthority(o.identity, codexAuthorityInvalidating, epochLabel, "sink-error")
		return err
	}
	return o.sink.SetAuthority(o.identity, codexAuthorityHook, "", reason)
}

func waitForCodexLifecycleBinding(ctx context.Context, sink codexLifecycleSink, identity codexLifecycleIdentity, timeout time.Duration) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(codexObserverBindingDelay)
	defer ticker.Stop()
	for {
		if sink.BindingCurrent(identity) {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return false
		case <-ticker.C:
		}
	}
}

func waitCodexObserver(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func codexNativeReason(err error) string {
	switch {
	case errors.Is(err, codexappserver.ErrUnsupported):
		return "unsupported"
	case errors.Is(err, codexappserver.ErrProtocol):
		return "protocol-error"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	default:
		return "unavailable"
	}
}

type aiCodexLifecycleSink struct{ command *aiCommand }

func (s aiCodexLifecycleSink) BindingCurrent(identity codexLifecycleIdentity) bool {
	c := s.command
	if c == nil || c.loadRegistry == nil || !identity.valid() {
		return false
	}
	registry, err := c.loadRegistry()
	if err != nil {
		return false
	}
	return exactCodexLifecycleBinding(registry, identity) && c.readTmuxPaneOption(identity.RuntimeID, tmuxopts.PaneUID) == identity.PaneUID
}

func exactCodexLifecycleBinding(registry coremetadata.Registry, identity codexLifecycleIdentity) bool {
	agent, ok := registry.Agent(identity.AgentUID)
	if !ok || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != identity.PaneUID || agent.Spec.Provider != aiModeCodex {
		return false
	}
	pane, ok := registry.Pane(identity.PaneUID)
	return ok && pane.Metadata.OwnerUID() == identity.AgentUID && pane.Status.Activation.AgentUID == identity.AgentUID &&
		pane.Status.Activation.Generation == identity.Generation && pane.Status.Activation.RuntimeID == identity.RuntimeID &&
		pane.Status.Activation.Codex != nil && pane.Status.Activation.Codex.ThreadID == identity.ThreadID
}

func (s aiCodexLifecycleSink) SetAuthority(identity codexLifecycleIdentity, source, epoch, reason string) error {
	if !s.BindingCurrent(identity) {
		return errManagedAgentObservationIgnored
	}
	for _, field := range []struct{ option, value string }{
		{aiPaneCodexAuthorityOption, source}, {aiPaneCodexEpochOption, epoch}, {aiPaneCodexReasonOption, reason},
	} {
		args := []string{"set-option", "-p", "-t", identity.RuntimeID, field.option, field.value}
		if field.value == "" {
			args = []string{"set-option", "-p", "-u", "-t", identity.RuntimeID, field.option}
		}
		if err := s.command.run("tmux", args...); err != nil {
			return err
		}
	}
	return nil
}

func (s aiCodexLifecycleSink) Apply(identity codexLifecycleIdentity, projection codexLifecycleProjection) error {
	c := s.command
	if !projection.Accepted || !s.BindingCurrent(identity) || c.updateRegistry == nil {
		return errManagedAgentObservationIgnored
	}
	policy := c.codexSemanticPolicyForInteraction(projection.Interaction)
	delivery := codexSemanticDeliveryFor(policy, projection.Interaction)
	mutator := intmetadata.DefaultMutator()
	mutator.Now = c.sessionRefClock()
	if _, err := c.updateRegistry(func(registry *coremetadata.Registry) error {
		if !exactCodexLifecycleBinding(*registry, identity) {
			return errManagedAgentObservationIgnored
		}
		_, err := mutator.SetAgentInteraction(registry, identity.AgentUID, delivery.RegistryInteraction, string(coremetadata.InteractionSourceProviderControl))
		return err
	}); err != nil {
		return err
	}
	if !s.BindingCurrent(identity) {
		return errManagedAgentObservationIgnored
	}
	clearNoticeIDs := append([]string(nil), projection.ClearNoticeIDs...)
	if !delivery.Notify {
		for _, notice := range projection.Notices {
			clearNoticeIDs = append(clearNoticeIDs, notice.ID)
		}
	}
	if len(clearNoticeIDs) > 0 {
		store, err := c.aiNotifyStore()
		if err != nil {
			return err
		}
		for _, id := range clearNoticeIDs {
			if err := store.Ack(id); err != nil && !errors.Is(err, notify.ErrNotFound) {
				return err
			}
		}
		c.publishNotifyQueueRefreshBestEffort()
	}
	if !s.BindingCurrent(identity) {
		return errManagedAgentObservationIgnored
	}

	state, badge, attention := delivery.State, delivery.Badge, delivery.Attention
	for _, field := range []struct{ option, value string }{
		{aiPaneStateOption, state}, {aiPaneBadgeKindOption, badge}, {attentionStateOption, attention},
	} {
		args := []string{"set-option", "-p", "-t", identity.RuntimeID, field.option, field.value}
		if field.value == "" {
			args = []string{"set-option", "-p", "-u", "-t", identity.RuntimeID, field.option}
		}
		if err := c.run("tmux", args...); err != nil {
			return err
		}
	}
	if !delivery.Notify {
		return nil
	}
	if !s.BindingCurrent(identity) {
		return errManagedAgentObservationIgnored
	}
	for _, notice := range projection.Notices {
		metadata := map[string]string{
			notify.MetaAgent: aiModeCodex, notify.MetaCategory: notice.Category,
			"thread_id": notice.ThreadID, "turn_id": notice.TurnID,
		}
		if notice.ItemID != "" {
			metadata["item_id"] = notice.ItemID
		}
		if notice.RequestID != "" {
			metadata["request_id"] = notice.RequestID
		}
		if notice.Kind != "" {
			metadata["approval_kind"] = string(notice.Kind)
		}
		text := "Ready"
		if notice.Category == "approval_required" {
			approvalRequired := localizeText(c.locale(), i18n.KeyNotifyAIApprovalRequired, "Approval required")
			openCodex := localizeText(c.locale(), i18n.KeyAgentControlOpenCodex, agentActionOpenCodex)
			reviewApproval := localizeText(c.locale(), i18n.KeyAgentControlReviewApproval, agentActionReviewApproval)
			metadata["focus_available"] = "true"
			metadata["action_label"] = openCodex
			text = approvalRequired + " — " + openCodex
			if notice.ResponderAvailable {
				metadata["action_label"] = reviewApproval
				text = approvalRequired + " — " + reviewApproval
			}
		}
		input := attentionNotifyInput{
			PaneID: identity.RuntimeID, Lookup: c.notifyLookup(), ID: notice.ID, Text: text,
			Severity: notice.Severity, Metadata: metadata, Force: true, BadgeKind: badge,
		}
		_ = c.notifyAIWithInput(identity.RuntimeID, input)
		c.notifyProducer().PushReplyReady(input)
	}
	return nil
}

func markCodexApprovalAvailability(projection *codexLifecycleProjection, responderAvailable bool) {
	if projection == nil {
		return
	}
	for i := range projection.Notices {
		if projection.Notices[i].Category == "approval_required" {
			projection.Notices[i].ResponderAvailable = responderAvailable
		}
	}
}

type codexSemanticDelivery struct {
	RegistryInteraction coremetadata.AgentInteractionKind
	State               string
	Badge               string
	Attention           string
	Notify              bool
}

func codexSemanticDeliveryFor(policy config.AISemanticPolicy, interaction coremetadata.AgentInteractionKind) codexSemanticDelivery {
	state, badge, attention := agentTmuxProjection(interaction)
	delivery := codexSemanticDelivery{RegistryInteraction: interaction, State: state, Badge: badge, Attention: attention}
	switch policy {
	case config.AISemanticQuiet:
		// Registry interaction is itself a badge input for aggregate views. Quiet
		// preserves control-plane provenance at the write site but not a visible kind.
		delivery.RegistryInteraction = coremetadata.InteractionUnknown
		delivery.Badge = ""
		delivery.Attention = ""
	case config.AISemanticStateOnly:
		delivery.Attention = ""
	default:
		delivery.Notify = true
	}
	return delivery
}

func (c *aiCommand) codexSemanticPolicyForInteraction(kind coremetadata.AgentInteractionKind) config.AISemanticPolicy {
	event := config.AISemanticEvent("")
	switch kind {
	case coremetadata.InteractionApprovalRequired:
		event = config.AISemanticApprovalRequired
	case coremetadata.InteractionResponseComplete:
		event = config.AISemanticResponseComplete
	default:
		return config.AISemanticNotify
	}
	paths, err := configPaths(c.homeDir, c.lookupEnv)
	if err != nil {
		return config.AISemanticNotify
	}
	policies, err := config.LoadAISemanticPoliciesFile(paths.AISemanticPoliciesFile())
	if err != nil {
		return config.AISemanticNotify
	}
	return policies.Events[event]
}

func (c *aiCommand) startNativeCodexLifecycleObserver(identity codexLifecycleIdentity) {
	if c == nil || !identity.valid() {
		return
	}
	// The activation may have been replaced between create/resume committing
	// its binding and reaching this synchronous transition. Pending is itself
	// an authority write, so prove the same exact Registry + tmux Pane identity
	// used by every observer projection before touching the runtime.
	if !(aiCodexLifecycleSink{command: c}).BindingCurrent(identity) {
		return
	}
	// Pending is written synchronously before the child can observe or emit any
	// event, closing the startup dual-authority gap.
	if err := c.run("tmux", "set-option", "-p", "-u", "-t", identity.RuntimeID, aiPaneCodexEpochOption); err != nil {
		return
	}
	for _, field := range []struct{ option, value string }{
		{aiPaneCodexAuthorityOption, codexAuthorityPending},
		{aiPaneCodexReasonOption, "connecting"},
	} {
		if err := c.run("tmux", "set-option", "-p", "-t", identity.RuntimeID, field.option, field.value); err != nil {
			_ = c.run("tmux", "set-option", "-p", "-t", identity.RuntimeID, aiPaneCodexAuthorityOption, codexAuthorityHook)
			_ = c.run("tmux", "set-option", "-p", "-t", identity.RuntimeID, aiPaneCodexReasonOption, "observer-unavailable")
			return
		}
	}
	executable := c.executable
	if executable == nil {
		executable = os.Executable
	}
	path, err := executable()
	if err == nil {
		err = startCodexLifecycleObserverProcess(path, identity)
	}
	if err != nil {
		_ = c.run("tmux", "set-option", "-p", "-t", identity.RuntimeID, aiPaneCodexAuthorityOption, codexAuthorityHook)
		_ = c.run("tmux", "set-option", "-p", "-t", identity.RuntimeID, aiPaneCodexReasonOption, "observer-unavailable")
	}
}

func startCodexLifecycleObserverProcess(executable string, identity codexLifecycleIdentity) error {
	executable, err := validateCodexLifecycleObserverExecutable(executable)
	if err != nil {
		return err
	}
	// #nosec G204 -- executable is an absolute, existing, regular executable validated above; argv is a fixed internal route plus bounded identity values and never enters a shell.
	cmd := exec.Command(executable, "internal", "agent-hook", "ingest", "codex-appserver-watch",
		"--agent-uid", identity.AgentUID, "--pane-uid", identity.PaneUID, "--pane", identity.RuntimeID,
		"--generation", identity.Generation, "--thread", identity.ThreadID)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func validateCodexLifecycleObserverExecutable(executable string) (string, error) {
	executable = strings.TrimSpace(executable)
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(executable)), ".exe")
	if executable == "" || strings.HasSuffix(name, ".test") {
		return "", errors.New("codex native lifecycle observer executable is unavailable")
	}
	if !filepath.IsAbs(executable) {
		return "", errors.New("codex native lifecycle observer executable must be absolute")
	}
	executable = filepath.Clean(executable)
	info, err := os.Stat(executable)
	if err != nil {
		return "", fmt.Errorf("stat Codex native lifecycle observer executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("codex native lifecycle observer executable must be a regular executable file")
	}
	return executable, nil
}

func (c *aiCommand) runCodexNativeLifecycleObserver(identity codexLifecycleIdentity) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	observer := codexNativeObserver{
		identity: identity,
		open: func(ctx context.Context) (codexLifecycleConnection, error) {
			return codexappserver.OpenDefaultProxy(ctx, codexappserver.DefaultProbeTimeout, version.String())
		},
		sink: aiCodexLifecycleSink{command: c},
	}
	if paths, err := config.DefaultPathsFromEnv(); err == nil {
		observer.startControl = func(epoch *codexControlEpoch) (*codexControlServer, error) {
			return startCodexControlServer(paths.StateDir, epoch)
		}
	}
	return observer.Run(ctx)
}

func parseCodexNativeLifecycleIdentity(args []string) (codexLifecycleIdentity, error) {
	identity := codexLifecycleIdentity{}
	for len(args) > 0 {
		if len(args) < 2 {
			return identity, errors.New("codex app-server watcher has an incomplete flag")
		}
		value := strings.TrimSpace(args[1])
		switch args[0] {
		case "--agent-uid":
			identity.AgentUID = value
		case "--pane-uid":
			identity.PaneUID = value
		case "--pane":
			identity.RuntimeID = value
		case "--generation":
			identity.Generation = value
		case "--thread":
			identity.ThreadID = value
		default:
			return identity, fmt.Errorf("unknown Codex app-server watcher flag: %s", args[0])
		}
		args = args[2:]
	}
	if !identity.valid() {
		return identity, errors.New("codex app-server watcher requires exact Agent, Pane, runtime, generation, and thread identity")
	}
	return identity, nil
}

func codexAuthoritySuppressesHooks(source string) bool {
	switch strings.TrimSpace(source) {
	case codexAuthorityPending, codexAuthorityControlPlane, codexAuthorityInvalidating:
		return true
	default:
		return false
	}
}
