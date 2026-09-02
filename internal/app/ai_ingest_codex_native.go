package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/agentprogress"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

const (
	aiPaneCodexAuthorityOption = "@projmux_codex_authority"
	aiPaneCodexEpochOption     = "@projmux_codex_authority_epoch"
	aiPaneCodexReasonOption    = "@projmux_codex_authority_reason"
	aiPaneCodexDroppedOption   = "@projmux_codex_progress_dropped"
	aiPaneCodexUnknownOption   = "@projmux_codex_progress_unknown"
	aiPaneCodexOverflowOption  = "@projmux_codex_progress_overflow"
	aiPaneCodexDeclaredOption  = "@projmux_codex_native_declared"

	codexAuthorityPending      = "pending"
	codexAuthorityControlPlane = "provider-control-plane"
	codexAuthorityInvalidating = "invalidating"
	codexAuthorityHook         = "provider-hook"

	// codexNativeUnexplainedReason is the hook-observation reason a managed
	// Codex Pane carries when nothing declared why it has no native binding.
	// It is the only reason that counts as an unexplained native fallback, so
	// a real regression stays visible instead of being absorbed by a
	// by-design exception.
	codexNativeUnexplainedReason = "native-fallback"

	// The declared reasons below are the by-design plain-CLI lanes. Each one
	// is a decision the operator or an upstream gate made before any provider
	// conversation existed, so an Agent carrying one is on hook observation on
	// purpose and is not counted as an unexplained native fallback.
	//
	// codexNativeDeclaredEmptyPrompt marks an empty-prompt default create. A
	// turnless thread cannot carry a live Pane on current upstream Codex, so
	// this lane is gated on upstream rather than chosen.
	codexNativeDeclaredEmptyPrompt = "empty-prompt-upstream-gate"
	// codexNativeDeclaredInteractiveOnly marks the explicit --interactive-only
	// opt-out.
	codexNativeDeclaredInteractiveOnly = "interactive-only"

	// The reconnect delay bounds are the observer's own pacing between one
	// closed epoch and the next open attempt. There is deliberately no attempt
	// ceiling: the endpoint broker owns the reconnect and keeps retrying with
	// capped backoff for as long as a binding exists, so a second, smaller
	// count here could only strand a live activation on hook fallback while
	// its endpoint was still being served.
	codexObserverReconnectDelay    = 100 * time.Millisecond
	codexObserverReconnectMaxDelay = time.Second
	codexObserverBindingDelay      = 25 * time.Millisecond
	codexObserverBindingTimeout    = 3 * time.Second
	codexObserverStartupTimeout    = 3 * time.Second
	codexObserverStartupSettle     = 75 * time.Millisecond

	codexObserverStartupEnvironment = "PROJMUX_INTERNAL_CODEX_OBSERVER_STARTUP"
	codexObserverStartupPrefix      = "projmux-codex-observer-v1"

	// codexNativeLifecycleIngestRoute is the hidden ingest route one managed
	// activation's native lifecycle producer runs under. It names the broker
	// binding it consumes, not the app-server proxy the retired per-Agent
	// observer used to open for itself.
	codexNativeLifecycleIngestRoute = "codex-broker-watch"
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

type codexProgressSink interface {
	ApplyProgress(codexLifecycleIdentity, coremetadata.AgentProgress, agentprogress.Diagnostics) error
}

type codexNativeLifecycleStarter interface {
	startNativeCodexLifecycleObserver(codexLifecycleObserverTarget) codexObserverStartupResult
}

type codexObserverStartupStatus string

const (
	codexObserverStartupReady    codexObserverStartupStatus = "ready"
	codexObserverStartupFallback codexObserverStartupStatus = "fallback"
	codexObserverStartupStale    codexObserverStartupStatus = "stale"
)

type codexObserverStartupResult struct {
	Status codexObserverStartupStatus
	Epoch  string
	Reason string
	// committed is true only for a result reported by the child after its exact
	// authority write. Parent-side launch failures still need convergence.
	committed bool
}

type codexLifecycleObserverTarget struct {
	Identity codexLifecycleIdentity
	Route    tmuxTransport
}

func (t codexLifecycleObserverTarget) valid() bool {
	return t.Identity.valid() && t.Route.Flag() != "" && t.Route.Value != ""
}

type codexNativeObserver struct {
	identity       codexLifecycleIdentity
	open           func(context.Context) (codexLifecycleConnection, error)
	sink           codexLifecycleSink
	delay          time.Duration
	maxDelay       time.Duration
	waitRecovery   func(context.Context, time.Duration) bool
	bindingTimeout time.Duration
	openTimeout    time.Duration
	sequence       uint64
	reducer        codexLifecycleReducer
	startControl   func(*codexControlEpoch) (*codexControlServer, error)
	requireControl bool
	reportStartup  func(codexObserverStartupResult)
	progress       agentprogress.Reducer
	now            func() time.Time
}

func (o *codexNativeObserver) Run(ctx context.Context) error {
	if !o.identity.valid() || o.open == nil || o.sink == nil {
		return errors.New("codex native lifecycle observer is not configured")
	}
	delay := o.delay
	if delay <= 0 {
		delay = codexObserverReconnectDelay
	}
	recovering := false
	recoveryAttempts := 0
	for ctx.Err() == nil {
		bindingTimeout := o.bindingTimeout
		if bindingTimeout <= 0 {
			bindingTimeout = codexObserverBindingTimeout
		}
		if !waitForCodexLifecycleBinding(ctx, o.sink, o.identity, bindingTimeout) {
			if ctx.Err() == nil {
				// SetAuthority repeats the exact binding predicate. A still-current
				// startup may fall back; a replaced runtime writes nothing.
				o.setStartupFallback("observer-timeout")
			} else {
				o.reportStartupResult(codexObserverStartupResult{Status: codexObserverStartupStale})
			}
			return nil
		}
		if err := o.clearProgress(); err != nil {
			o.setStartupFallback("sink-error")
			return err
		}
		openTimeout := o.openTimeout
		if openTimeout <= 0 {
			openTimeout = codexappserver.DefaultProbeTimeout
		}
		openCtx, cancelOpen := context.WithTimeout(ctx, openTimeout)
		client, err := o.open(openCtx)
		cancelOpen()
		if err != nil {
			reason := codexNativeReason(err)
			if recovering {
				recoveryAttempts++
				if retry, recoveryErr := o.continueRecovery(ctx, recoveryAttempts); recoveryErr != nil {
					return recoveryErr
				} else if !retry {
					return nil
				}
				continue
			}
			o.setStartupFallback(reason)
			if !waitCodexObserver(ctx, delay) {
				return nil
			}
			continue
		}
		if !client.LifecycleEventsAvailable() {
			_ = client.Close()
			if recovering {
				recoveryAttempts++
				if retry, recoveryErr := o.continueRecovery(ctx, recoveryAttempts); recoveryErr != nil {
					return recoveryErr
				} else if !retry {
					return nil
				}
				continue
			}
			o.setStartupFallback("unsupported")
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
			if !o.sink.BindingCurrent(o.identity) {
				o.reportStartupResult(codexObserverStartupResult{Status: codexObserverStartupStale})
				return nil
			}
			if recovering {
				recoveryAttempts++
				if retry, recoveryErr := o.continueRecovery(ctx, recoveryAttempts); recoveryErr != nil {
					return recoveryErr
				} else if !retry {
					return nil
				}
				continue
			}
			if snapshotErr != nil {
				o.setStartupFallback(codexNativeReason(snapshotErr))
			}
			if !waitCodexObserver(ctx, delay) {
				return nil
			}
			continue
		}
		projection := o.reducer.begin(epoch, o.identity, snapshot)
		if !projection.Accepted {
			_ = client.Close()
			if recovering {
				recoveryAttempts++
				if retry, recoveryErr := o.continueRecovery(ctx, recoveryAttempts); recoveryErr != nil {
					return recoveryErr
				} else if !retry {
					return nil
				}
				continue
			}
			o.setStartupFallback("protocol-error")
			return errors.New("codex native lifecycle snapshot did not match exact binding")
		}
		if projection.Invalidated {
			if recovering {
				// A replacement snapshot that cannot re-establish the exact
				// thread is not a new status transition. The disconnect already
				// published the one unavailable projection, so discard this
				// candidate without making the Pane flap through hook fallback.
				o.discardRecoveryEpoch(epoch)
				_ = client.Close()
				recoveryAttempts++
				if retry, recoveryErr := o.continueRecovery(ctx, recoveryAttempts); recoveryErr != nil {
					return recoveryErr
				} else if !retry {
					return nil
				}
				continue
			}
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
		if snapshot.TurnID != "" && snapshot.TurnState == codexappserver.TurnStateInProgress {
			o.progress.Begin(snapshot.TurnID, snapshot.StartedAt, o.currentTime())
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
		if control == nil && o.requireControl {
			if recovering {
				// Do not publish the replacement snapshot until its exact
				// control endpoint is proved. A failed candidate is invisible:
				// the Pane remains on the one unavailable projection emitted at
				// disconnect, and no hook/badge write can interleave.
				o.discardRecoveryEpoch(epoch)
				_ = client.Close()
				recoveryAttempts++
				if retry, recoveryErr := o.continueRecovery(ctx, recoveryAttempts); recoveryErr != nil {
					return recoveryErr
				} else if !retry {
					return nil
				}
				continue
			}
			cleanupErr := o.invalidateAndFallback(epoch, epochLabel, "control-unavailable")
			_ = client.Close()
			if cleanupErr != nil {
				return cleanupErr
			}
			if recovering {
				recoveryAttempts++
				if retry, recoveryErr := o.continueRecovery(ctx, recoveryAttempts); recoveryErr != nil {
					return recoveryErr
				} else if !retry {
					return nil
				}
				continue
			}
			if !waitCodexObserver(ctx, delay) {
				return nil
			}
			continue
		}
		// A replacement snapshot becomes observable only after its matching
		// control endpoint exists. The live Pane authority is still
		// invalidating here, so public callers cannot reach the new socket until
		// SetAuthority commits the exact replacement epoch below.
		if err := o.sink.Apply(o.identity, projection); err != nil {
			if control != nil {
				_ = control.Close()
			}
			cleanupErr := o.invalidateAndFallback(epoch, epochLabel, "sink-error")
			_ = client.Close()
			return errors.Join(err, cleanupErr)
		}
		if err := o.sink.SetAuthority(o.identity, codexAuthorityControlPlane, epochLabel, "ready"); err != nil {
			if control != nil {
				_ = control.Close()
			}
			cleanupErr := o.invalidateAndFallback(epoch, epochLabel, "sink-error")
			_ = client.Close()
			return errors.Join(err, cleanupErr)
		}
		if err := o.flushProgress(); err != nil {
			if control != nil {
				_ = control.Close()
				control = nil
			}
			cleanupErr := o.invalidateAndFallback(epoch, epochLabel, "sink-error")
			_ = client.Close()
			return errors.Join(err, cleanupErr)
		}
		o.reportStartupResult(codexObserverStartupResult{Status: codexObserverStartupReady, Epoch: epochLabel})
		recovering = false
		recoveryAttempts = 0

		reconnectReason := "disconnected"
		invalidated := false
		stopAfterTransition := false
		bindingTicker := time.NewTicker(codexObserverBindingDelay)
		progressTicker := time.NewTicker(25 * time.Millisecond)
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
			case <-progressTicker.C:
				if err := o.flushProgress(); err != nil {
					if control != nil {
						_ = control.Close()
						control = nil
					}
					cleanupErr := o.invalidateAndFallback(epoch, epochLabel, "sink-error")
					bindingTicker.Stop()
					progressTicker.Stop()
					_ = client.Close()
					return errors.Join(err, cleanupErr)
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
					progressEvent, progressRecognized, progressErr := codexappserver.DecodeProgressEvent(notification, o.currentTime())
					if progressErr != nil {
						reconnectReason = "protocol-error"
						break eventLoop
					}
					if !progressRecognized {
						continue
					}
					if !o.observeProgress(progressEvent) {
						continue
					}
					if err := o.flushProgress(); err != nil {
						if control != nil {
							_ = control.Close()
							control = nil
						}
						cleanupErr := o.invalidateAndFallback(epoch, epochLabel, "sink-error")
						bindingTicker.Stop()
						progressTicker.Stop()
						_ = client.Close()
						return errors.Join(err, cleanupErr)
					}
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
				if progressEvent, progressRecognized, progressErr := codexappserver.DecodeProgressEvent(notification, o.currentTime()); progressErr != nil {
					reconnectReason = "protocol-error"
					break eventLoop
				} else if progressRecognized {
					if !o.observeProgress(progressEvent) {
						continue
					}
					if progressEvent.Kind == agentprogress.EventTurnTerminal {
						_, _ = o.progress.Flush(o.currentTime())
						if err := o.clearProgress(); err != nil {
							if control != nil {
								_ = control.Close()
								control = nil
							}
							cleanupErr := o.invalidateAndFallback(epoch, epochLabel, "sink-error")
							bindingTicker.Stop()
							progressTicker.Stop()
							_ = client.Close()
							return errors.Join(err, cleanupErr)
						}
					} else if err := o.flushProgress(); err != nil {
						if control != nil {
							_ = control.Close()
							control = nil
						}
						cleanupErr := o.invalidateAndFallback(epoch, epochLabel, "sink-error")
						bindingTicker.Stop()
						progressTicker.Stop()
						_ = client.Close()
						return errors.Join(err, cleanupErr)
					}
				}
			}
		}
		bindingTicker.Stop()
		progressTicker.Stop()
		if control != nil {
			_ = control.Close()
		}
		_ = client.Close()
		if !invalidated {
			transition := o.invalidateAndFallback
			if !stopAfterTransition && reconnectReason == "disconnected" {
				// The broker binding survives a transient endpoint disconnect and
				// owns the reconnect. Keep one deterministic unavailable
				// projection until its replacement barrier and control endpoint
				// are both ready; provider-hook is not reconnect authority.
				transition = o.invalidateAndHold
			}
			if err := transition(epoch, epochLabel, reconnectReason); err != nil {
				return err
			}
		}
		if stopAfterTransition {
			return nil
		}
		recovering = true
		recoveryAttempts = 0
		if retry, recoveryErr := o.continueRecovery(ctx, recoveryAttempts); recoveryErr != nil {
			return recoveryErr
		} else if !retry {
			return nil
		}
	}
	return nil
}

// continueRecovery is the only retry scheduler after a ready epoch is lost.
// The old control endpoint is already revoked and the one unavailable
// invalidation projection is published before this method is called. Every
// caller increments the attempt count only after one failed replacement open,
// snapshot, or control proof, so the count paces the backoff; it never
// terminates the recovery.
//
// The only exits are a binding that is no longer current and a cancelled
// context. A live activation whose endpoint is merely away remains
// invalidating instead of being exposed to hook fallback, because the broker
// beneath this loop is still reconnecting on its own capped backoff for as long
// as the binding exists.
func (o *codexNativeObserver) continueRecovery(ctx context.Context, attempts int) (bool, error) {
	if !o.sink.BindingCurrent(o.identity) {
		o.reportStartupResult(codexObserverStartupResult{Status: codexObserverStartupStale})
		return false, nil
	}
	delay := o.delay
	if delay <= 0 {
		delay = codexObserverReconnectDelay
	}
	maximum := o.maxDelay
	if maximum <= 0 {
		maximum = codexObserverReconnectMaxDelay
	}
	if maximum < delay {
		maximum = delay
	}
	for index := 0; index < attempts && delay < maximum; index++ {
		if delay > maximum/2 {
			delay = maximum
			break
		}
		delay *= 2
	}
	wait := o.waitRecovery
	if wait == nil {
		wait = waitCodexObserver
	}
	return wait(ctx, delay), nil
}

func (o *codexNativeObserver) currentTime() time.Time {
	if o.now == nil {
		return time.Now().UTC()
	}
	return o.now().UTC()
}

func (o *codexNativeObserver) reportStartupResult(result codexObserverStartupResult) {
	if o.reportStartup != nil {
		o.reportStartup(result)
	}
}

func (o *codexNativeObserver) setStartupFallback(reason string) {
	if err := o.sink.SetAuthority(o.identity, codexAuthorityHook, "", reason); err == nil {
		o.reportStartupResult(codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: reason})
	} else if !o.sink.BindingCurrent(o.identity) {
		o.reportStartupResult(codexObserverStartupResult{Status: codexObserverStartupStale})
	}
}

func (o *codexNativeObserver) observeProgress(event agentprogress.Event) bool {
	// ThreadRef is an opaque routing identity, not content. Refuse mismatches
	// before the reducer can mutate either progress or diagnostic counters.
	if event.ThreadRef == "" || event.ThreadRef != o.identity.ThreadID {
		return false
	}
	return o.progress.Observe(event)
}

func (o *codexNativeObserver) flushProgress() error {
	sink, ok := o.sink.(codexProgressSink)
	if !ok {
		return nil
	}
	progress, changed := o.progress.Flush(o.currentTime())
	if !changed {
		return nil
	}
	return sink.ApplyProgress(o.identity, progress, o.progress.Diagnostics())
}

func (o *codexNativeObserver) clearProgress() error {
	sink, ok := o.sink.(codexProgressSink)
	if !ok {
		return nil
	}
	return sink.ApplyProgress(o.identity, coremetadata.AgentProgress{}, agentprogress.Diagnostics{})
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
	return o.applyInvalidation(epochLabel, reason, projection, true)
}

func (o *codexNativeObserver) applyInvalidationAndFallback(epochLabel, reason string, projection codexLifecycleProjection) error {
	return o.applyInvalidation(epochLabel, reason, projection, true)
}

// invalidateAndHold publishes the reconnect gap exactly once and deliberately
// leaves provider-hook suppressed. The same durable broker binding is still
// reconnecting, so only a replacement snapshot and exact control proof may
// move the Pane out of this unavailable projection.
func (o *codexNativeObserver) invalidateAndHold(epoch uint64, epochLabel, reason string) error {
	projection := o.reducer.invalidate(epoch)
	if !projection.Accepted {
		return errors.New("codex native lifecycle epoch could not be invalidated")
	}
	return o.applyInvalidation(epochLabel, reason, projection, false)
}

func (o *codexNativeObserver) applyInvalidation(epochLabel, reason string, projection codexLifecycleProjection, fallback bool) error {
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
	o.progress.Invalidate()
	_, _ = o.progress.Flush(o.currentTime())
	if err := o.clearProgress(); err != nil {
		return err
	}
	if !fallback {
		return nil
	}
	if err := o.sink.SetAuthority(o.identity, codexAuthorityHook, "", reason); err != nil {
		return err
	}
	o.reportStartupResult(codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: reason})
	return nil
}

// discardRecoveryEpoch retires a replacement candidate without writing the
// already-stable unavailable projection again. It is used only before that
// candidate has published semantic state or ready authority.
func (o *codexNativeObserver) discardRecoveryEpoch(epoch uint64) {
	_ = o.reducer.invalidate(epoch)
	o.progress.Invalidate()
	_, _ = o.progress.Flush(o.currentTime())
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

type aiCodexLifecycleSink struct {
	command *aiCommand
	runner  tmuxCommandRunner
}

func (s aiCodexLifecycleSink) BindingCurrent(identity codexLifecycleIdentity) bool {
	c := s.command
	if c == nil || c.loadRegistry == nil || !identity.valid() {
		return false
	}
	registry, err := c.loadRegistry()
	if err != nil {
		return false
	}
	if s.runner == nil {
		return false
	}
	out, err := s.runner.Run(context.Background(), "tmux", "show-options", "-pqv", "-t", identity.RuntimeID, tmuxopts.PaneUID)
	return err == nil && strings.TrimSpace(string(out)) == identity.PaneUID && exactCodexLifecycleBinding(registry, identity)
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
	if s.command == nil {
		return errors.New("codex lifecycle authority requires command")
	}
	release, err := s.command.acquireCodexAuthorityFence(identity.PaneUID)
	if err != nil {
		return err
	}
	defer release()
	if !s.BindingCurrent(identity) {
		return errManagedAgentObservationIgnored
	}
	fields := []struct{ option, value string }{
		{aiPaneCodexAuthorityOption, source}, {aiPaneCodexEpochOption, epoch}, {aiPaneCodexReasonOption, reason},
	}
	before := make(map[string]string, len(fields))
	for _, field := range fields {
		value, err := readAgentPaneOptionOnRoute(context.Background(), s.runner, identity.RuntimeID, field.option)
		if err != nil {
			return err
		}
		before[field.option] = value
	}
	applied := make([]string, 0, len(fields))
	for _, field := range fields {
		args := []string{"set-option", "-p", "-t", identity.RuntimeID, field.option, field.value}
		if field.value == "" {
			args = []string{"set-option", "-p", "-u", "-t", identity.RuntimeID, field.option}
		}
		if _, err := s.runner.Run(context.Background(), "tmux", args...); err != nil {
			var compensation []error
			for i := len(applied) - 1; i >= 0; i-- {
				prior := before[applied[i]]
				if restoreErr := writeAgentPaneOptionOnRoute(context.Background(), s.runner, identity.RuntimeID, agentPaneOptionWrite{option: applied[i], value: prior, unset: prior == ""}); restoreErr != nil {
					compensation = append(compensation, restoreErr)
				}
			}
			if joined := errors.Join(compensation...); joined != nil {
				return errors.Join(err, fmt.Errorf("restore Codex authority: %w", joined))
			}
			return err
		}
		applied = append(applied, field.option)
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
		if _, err := mutator.SetAgentInteraction(registry, identity.AgentUID, delivery.RegistryInteraction, string(coremetadata.InteractionSourceProviderControl)); err != nil {
			return err
		}
		if projection.ClearProgress {
			_, _, err := mutator.SetAgentProgress(registry, identity.AgentUID, "", coremetadata.AgentProgress{})
			return err
		}
		return nil
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
		if _, err := s.runner.Run(context.Background(), "tmux", args...); err != nil {
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
			PaneID: identity.RuntimeID, Lookup: routedAINotifyLookup{runner: s.runner}, ID: notice.ID, Text: text,
			Severity: notice.Severity, Metadata: metadata, Force: true, BadgeKind: badge,
		}
		_ = s.notifyAIWithInput(identity.RuntimeID, input)
		c.notifyProducer().PushReplyReady(input)
	}
	return nil
}

type routedAINotifyLookup struct{ runner tmuxCommandRunner }

func (l routedAINotifyLookup) PaneOption(paneID, option string) string {
	if l.runner == nil {
		return ""
	}
	out, err := l.runner.Run(context.Background(), "tmux", "show-options", "-pqv", "-t", paneID, option)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (l routedAINotifyLookup) PaneFormat(paneID, format string) string {
	if l.runner == nil {
		return ""
	}
	out, err := l.runner.Run(context.Background(), "tmux", "display-message", "-p", "-t", paneID, format)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (s aiCodexLifecycleSink) notifyAIWithInput(paneID string, in attentionNotifyInput) error {
	c := s.command
	if c == nil || s.runner == nil || strings.TrimSpace(in.Text) == "" {
		return nil
	}
	text := strings.TrimSpace(in.Text)
	key := aiNotificationKey("hook", text)
	lookup := routedAINotifyLookup{runner: s.runner}
	if !in.Force && lookup.PaneOption(paneID, "@projmux_desktop_notification_key") == key {
		lastAt := parsePositiveInt(lookup.PaneOption(paneID, "@projmux_desktop_notification_at"))
		if lastAt > 0 && c.now().Unix()-int64(lastAt) < int64(c.aiNotifyDedupeSeconds()) {
			return s.recordAINotification(paneID, key)
		}
	}
	rendered := renderAINotifyText(text, in.Metadata, c.locale())
	summary, detail := rendered.Summary, rendered.Detail
	if summary == "" {
		summary = text
	}
	path := lookup.PaneFormat(paneID, "#{pane_current_path}")
	notification := aiNotification{
		Summary: summary,
		Body:    aiNotificationBody(detail, aiProjectName(path), c.gitBranchForPath(path), lookup.PaneFormat(paneID, "#S"), lookup.PaneFormat(paneID, "#W")),
		Urgency: aiOSNotificationUrgency(in.Severity), ExpireMS: c.notificationExpireMS(), AppName: desktopAppID,
		Icon: c.notificationIcon(aiNotificationTextAgentWithMetadata(text, in.Metadata)), Tag: paneID,
		Group:              lookup.PaneFormat(paneID, "#S"),
		diagnosticProvider: notifyLabels(notify.SourceAI, in.Metadata).provider,
		diagnosticCategory: notifyLabels(notify.SourceAI, in.Metadata).category,
	}
	if err := c.notificationNotifier().Notify(notification); err != nil {
		return nil
	}
	return s.recordAINotification(paneID, key)
}

func (s aiCodexLifecycleSink) recordAINotification(paneID, key string) error {
	for _, field := range []struct{ option, value string }{
		{"@projmux_desktop_notified", "1"},
		{"@projmux_desktop_notification_key", key},
		{"@projmux_desktop_notification_at", fmt.Sprintf("%d", s.command.now().Unix())},
	} {
		if field.value == "" {
			continue
		}
		if _, err := s.runner.Run(context.Background(), "tmux", "set-option", "-p", "-t", paneID, field.option, field.value); err != nil {
			return err
		}
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

func (s aiCodexLifecycleSink) ApplyProgress(identity codexLifecycleIdentity, progress coremetadata.AgentProgress, diagnostic agentprogress.Diagnostics) error {
	c := s.command
	if !s.BindingCurrent(identity) || c.updateRegistry == nil {
		return errManagedAgentObservationIgnored
	}
	writeRegistry := true
	if progress.IsZero() {
		registry, err := c.loadRegistry()
		if err != nil {
			return err
		}
		agent, ok := registry.Agent(identity.AgentUID)
		if !ok || agent.Status.Progress.IsZero() {
			writeRegistry = false
		}
	}
	mutator := intmetadata.DefaultMutator()
	mutator.Now = c.sessionRefClock()
	if writeRegistry {
		if _, err := c.updateRegistry(func(registry *coremetadata.Registry) error {
			if !exactCodexLifecycleBinding(*registry, identity) {
				return errManagedAgentObservationIgnored
			}
			if progress.IsZero() {
				_, _, err := mutator.SetAgentProgress(registry, identity.AgentUID, "", progress)
				return err
			}
			agent, _ := registry.Agent(identity.AgentUID)
			pane, _ := registry.Pane(agent.Status.PaneRef)
			if pane == nil || pane.Status.Activation.Codex == nil {
				return errManagedAgentObservationIgnored
			}
			if pane.Status.Activation.Codex.TurnID != progress.TurnRef {
				changed, refineErr := mutator.RefineCodexActivation(registry, coremetadata.CodexActivationObservation{
					AgentUID: identity.AgentUID, PaneUID: identity.PaneUID, Generation: identity.Generation,
					ThreadID: identity.ThreadID, TurnID: progress.TurnRef,
				})
				if refineErr != nil || !changed {
					return refineErr
				}
			}
			_, _, err := mutator.SetAgentProgress(registry, identity.AgentUID, progress.TurnRef, progress)
			return err
		}); err != nil {
			return err
		}
	}
	for _, field := range []struct {
		option string
		value  uint32
	}{
		{aiPaneCodexDroppedOption, diagnostic.Dropped},
		{aiPaneCodexUnknownOption, diagnostic.Unknown},
		{aiPaneCodexOverflowOption, diagnostic.Overflow},
	} {
		args := []string{"set-option", "-p", "-u", "-t", identity.RuntimeID, field.option}
		if field.value > 0 {
			args = []string{"set-option", "-p", "-t", identity.RuntimeID, field.option, strconv.FormatUint(uint64(field.value), 10)}
		}
		if _, err := s.runner.Run(context.Background(), "tmux", args...); err != nil {
			return err
		}
	}
	return nil
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

func (c *aiCommand) startNativeCodexLifecycleObserver(target codexLifecycleObserverTarget) codexObserverStartupResult {
	if c == nil || !target.valid() {
		return codexObserverStartupResult{Status: codexObserverStartupStale}
	}
	identity := target.Identity
	runner := explicitTmuxRunner{runner: aiCommandMuxBackend{runCommand: c.runCommand, readCommand: c.readCommand}, target: target.Route}
	sink := aiCodexLifecycleSink{command: c, runner: runner}
	// The activation may have been replaced between create/resume committing
	// its binding and reaching this synchronous transition. Pending is itself
	// an authority write, so prove the same exact Registry + tmux Pane identity
	// used by every observer projection before touching the runtime.
	if !sink.BindingCurrent(identity) {
		return codexObserverStartupResult{Status: codexObserverStartupStale}
	}
	if err := sink.SetAuthority(identity, codexAuthorityPending, "", "connecting"); err != nil {
		return codexObserverStartupResult{Status: codexObserverStartupStale}
	}
	executable := c.executable
	if executable == nil {
		executable = os.Executable
	}
	path, err := executable()
	if err != nil {
		return convergeCodexObserverStartupFallback(sink, identity, "observer-start-failed")
	}
	result := startCodexLifecycleObserverProcess(path, target, codexObserverStartupTimeout)
	if result.Status == codexObserverStartupFallback && result.Reason != "" && !result.committed {
		return convergeCodexObserverStartupFallback(sink, identity, result.Reason)
	}
	return result
}

func convergeCodexObserverStartupFallback(sink codexLifecycleSink, identity codexLifecycleIdentity, reason string) codexObserverStartupResult {
	result := codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: reason}
	if !sink.BindingCurrent(identity) {
		return codexObserverStartupResult{Status: codexObserverStartupStale}
	}
	if err := sink.SetAuthority(identity, codexAuthorityHook, "", reason); err != nil && !sink.BindingCurrent(identity) {
		return codexObserverStartupResult{Status: codexObserverStartupStale}
	}
	return result
}

func startCodexLifecycleObserverProcess(executable string, target codexLifecycleObserverTarget, timeout time.Duration) codexObserverStartupResult {
	executable, err := validateCodexLifecycleObserverExecutable(executable)
	if err != nil {
		return codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: "observer-start-failed"}
	}
	identity := target.Identity
	args := []string{"internal", "agent-hook", "ingest", codexNativeLifecycleIngestRoute,
		"--agent-uid", identity.AgentUID, "--pane-uid", identity.PaneUID, "--pane", identity.RuntimeID,
		"--generation", identity.Generation, "--thread", identity.ThreadID}
	if target.Route.Flag() == "-L" {
		args = append(args, "--tmux-socket-name", target.Route.Value)
	} else if target.Route.Flag() == "-S" {
		args = append(args, "--tmux-socket-path", target.Route.Value)
	} else {
		return codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: "observer-start-failed"}
	}
	// #nosec G204 -- executable is an absolute, existing, regular executable validated above; argv is a fixed internal route plus bounded identity values and never enters a shell.
	cmd := exec.Command(executable, args...)
	cmd.Env = append(withoutInheritedTmuxEnvironment(os.Environ()), codexObserverStartupEnvironment+"=1")
	configureCodexObserverProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: "observer-start-failed"}
	}
	if err := cmd.Start(); err != nil {
		return codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: "observer-start-failed"}
	}
	if timeout <= 0 {
		timeout = codexObserverStartupTimeout
	}
	line := make(chan string, 2)
	go func() {
		reader := bufio.NewReader(io.LimitReader(stdout, 512))
		value, _ := reader.ReadString('\n')
		line <- value
		value, _ = reader.ReadString('\n')
		line <- value
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case value := <-line:
		result, ok := parseCodexObserverStartupLine(value)
		if !ok {
			terminateCodexObserverProcess(cmd)
			return codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: "observer-exited"}
		}
		if result.Status == codexObserverStartupReady {
			settled := time.NewTimer(codexObserverStartupSettle)
			defer settled.Stop()
			select {
			case <-line:
				terminateCodexObserverProcess(cmd)
				return codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: "observer-exited"}
			case <-settled.C:
				_ = stdout.Close()
				if err := cmd.Process.Release(); err != nil {
					terminateCodexObserverProcess(cmd)
					return codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: "observer-start-failed"}
				}
				return result
			}
		}
		terminateCodexObserverProcess(cmd)
		return result
	case <-timer.C:
		terminateCodexObserverProcess(cmd)
		return codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: "observer-timeout"}
	}
}

func parseCodexObserverStartupLine(line string) (codexObserverStartupResult, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 || fields[0] != codexObserverStartupPrefix {
		return codexObserverStartupResult{}, false
	}
	switch codexObserverStartupStatus(fields[1]) {
	case codexObserverStartupReady:
		if len(fields) != 3 || fields[2] == "" {
			return codexObserverStartupResult{}, false
		}
		return codexObserverStartupResult{Status: codexObserverStartupReady, Epoch: fields[2], committed: true}, true
	case codexObserverStartupFallback:
		if len(fields) != 3 || fields[2] == "" {
			return codexObserverStartupResult{}, false
		}
		return codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: fields[2], committed: true}, true
	case codexObserverStartupStale:
		if len(fields) != 2 {
			return codexObserverStartupResult{}, false
		}
		return codexObserverStartupResult{Status: codexObserverStartupStale, committed: true}, true
	default:
		return codexObserverStartupResult{}, false
	}
}

func codexObserverStartupReporter() func(codexObserverStartupResult) {
	if os.Getenv(codexObserverStartupEnvironment) != "1" {
		return nil
	}
	var reported bool
	return func(result codexObserverStartupResult) {
		if reported {
			return
		}
		reported = true
		switch result.Status {
		case codexObserverStartupReady:
			_, _ = fmt.Fprintf(os.Stdout, "%s %s %s\n", codexObserverStartupPrefix, result.Status, result.Epoch)
		case codexObserverStartupFallback:
			_, _ = fmt.Fprintf(os.Stdout, "%s %s %s\n", codexObserverStartupPrefix, result.Status, result.Reason)
		case codexObserverStartupStale:
			_, _ = fmt.Fprintf(os.Stdout, "%s %s\n", codexObserverStartupPrefix, result.Status)
		}
	}
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

func withoutInheritedTmuxEnvironment(env []string) []string {
	clean := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if key == "TMUX" || key == "TMUX_PANE" || key == runtimeMutationAnchorPaneEnv || key == codexObserverStartupEnvironment {
			continue
		}
		clean = append(clean, entry)
	}
	return clean
}

func (c *aiCommand) runCodexNativeLifecycleObserver(target codexLifecycleObserverTarget) error {
	if !target.valid() {
		return errors.New("codex native lifecycle observer requires exact identity and tmux route")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runner := explicitTmuxRunner{runner: aiCommandMuxBackend{runCommand: c.runCommand, readCommand: c.readCommand}, target: target.Route}
	sink := aiCodexLifecycleSink{command: c, runner: runner}
	// The endpoint broker is the whole native producer for this activation
	// generation. The per-Agent app-server proxy observer that used to share
	// this seam is retired: it opened one upstream connection per Agent, owned
	// a second private control endpoint alongside the broker's, and gave up
	// after a fixed reconnect budget.
	//
	// A broker session that cannot be prepared is therefore not a reason to
	// open a second lane. It converges this activation onto the declared hook
	// fallback with the typed reason and exits, which is the same answer the
	// observer publishes for every other unavailable endpoint.
	session, sessionErr := newCodexBrokerObserverSession(target.Identity, "", nil)
	if sessionErr != nil {
		result := convergeCodexObserverStartupFallback(sink, target.Identity, codexNativeReason(sessionErr))
		if report := codexObserverStartupReporter(); report != nil {
			report(result)
		}
		return nil
	}
	defer func() { _ = session.Close() }()
	observer := codexNativeObserver{
		identity:       target.Identity,
		requireControl: true,
		sink:           sink,
		reportStartup:  codexObserverStartupReporter(),
		open:           session.Open,
		openTimeout:    codexBrokerObserverOpenTimeout,
	}
	if paths, err := config.DefaultPathsFromEnv(); err == nil {
		observer.startControl = func(epoch *codexControlEpoch) (*codexControlServer, error) {
			return startCodexControlServer(paths.StateDir, epoch)
		}
	}
	return observer.Run(ctx)
}

func parseCodexNativeLifecycleTarget(args []string) (codexLifecycleObserverTarget, error) {
	target := codexLifecycleObserverTarget{}
	for len(args) > 0 {
		if len(args) < 2 {
			return target, errors.New("codex app-server watcher has an incomplete flag")
		}
		value := strings.TrimSpace(args[1])
		switch args[0] {
		case "--agent-uid":
			target.Identity.AgentUID = value
		case "--pane-uid":
			target.Identity.PaneUID = value
		case "--pane":
			target.Identity.RuntimeID = value
		case "--generation":
			target.Identity.Generation = value
		case "--thread":
			target.Identity.ThreadID = value
		case "--tmux-socket-name":
			if target.Route.Flag() != "" {
				return target, errors.New("codex app-server watcher accepts exactly one tmux route")
			}
			var err error
			target.Route, err = tmuxSocketNameTarget(value)
			if err != nil {
				return target, err
			}
		case "--tmux-socket-path":
			if target.Route.Flag() != "" {
				return target, errors.New("codex app-server watcher accepts exactly one tmux route")
			}
			var err error
			target.Route, err = tmuxSocketPathTarget(value)
			if err != nil {
				return target, err
			}
		default:
			return target, fmt.Errorf("unknown Codex app-server watcher flag: %s", args[0])
		}
		args = args[2:]
	}
	if !target.valid() {
		return target, errors.New("codex app-server watcher requires exact Agent, Pane, runtime, generation, thread, and tmux route")
	}
	return target, nil
}

// codexNativeDeclaredReasons is the closed declared-reason vocabulary. A value
// outside it is treated as undeclared, so a stale or forged tmux option cannot
// silence an unexplained native fallback.
var codexNativeDeclaredReasons = []string{
	codexNativeDeclaredEmptyPrompt,
	codexNativeDeclaredInteractiveOnly,
}

// codexNativeDeclaredReason normalizes one declared reason, returning "" when
// the value is not in the closed vocabulary.
func codexNativeDeclaredReason(value string) string {
	value = strings.TrimSpace(value)
	if slices.Contains(codexNativeDeclaredReasons, value) {
		return value
	}
	return ""
}

func codexAuthoritySuppressesHooks(source string) bool {
	switch strings.TrimSpace(source) {
	case codexAuthorityPending, codexAuthorityControlPlane, codexAuthorityInvalidating:
		return true
	default:
		return false
	}
}
