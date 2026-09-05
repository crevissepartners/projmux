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
	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
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
	// codexNativeDeclaredInteractiveOnly marks the explicit --interactive-only
	// opt-out.
	codexNativeDeclaredInteractiveOnly = "interactive-only"
	// codexNativeDeclaredPayloadFreeFallback marks the permanent safe fallback
	// for a fresh create with no payload. It is chosen before any app-server
	// route or thread is touched and makes the reduced native-control state
	// visible without storing provider content.
	codexNativeDeclaredPayloadFreeFallback = "payload-free-fallback"
	// codexNativeDeclaredRolloutCatalogResume marks a picker row whose source is the
	// rollout store. It can launch the provider's legacy resume command, but it
	// never acquires app-server thread or endpoint authority.
	codexNativeDeclaredRolloutCatalogResume = "rollout-catalog-resume"

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

// codexObserverReason is the closed vocabulary for every reason a managed
// Codex observer publishes: the value of the @projmux_codex_authority_reason
// pane option, and the reason column of the observer transition records this
// process appends to ai-ingest.log.
//
// It is deliberately one vocabulary. Before this type existed the event loop
// carried a literal default that meant "nothing recorded why this epoch
// ended", and a separate four-token set derived from open errors lived beside
// it, so a Pane could publish a bucket name as though it were an observation.
// Every producer now names a token from this list and codexObserverReasonFor
// is the only door a foreign string comes through.
type codexObserverReason string

const (
	// Steady-state tokens.
	codexObserverReasonReady      codexObserverReason = "ready"
	codexObserverReasonConnecting codexObserverReason = "connecting"

	// Open, snapshot, control, and sink failures. The first four are what
	// codexNativeReason maps one transport error onto.
	codexObserverReasonUnsupported           codexObserverReason = "unsupported"
	codexObserverReasonProtocolError         codexObserverReason = "protocol-error"
	codexObserverReasonTimeout               codexObserverReason = "timeout"
	codexObserverReasonUnavailable           codexObserverReason = "unavailable"
	codexObserverReasonSinkError             codexObserverReason = "sink-error"
	codexObserverReasonControlUnavailable    codexObserverReason = "control-unavailable"
	codexObserverReasonGenerationUnavailable codexObserverReason = codexNativeReasonGenerationUnavailable
	codexObserverReasonThreadUnloaded        codexObserverReason = "thread-unloaded"

	// Event-loop exits. Each token names exactly one way out of the loop, and
	// the three the observer used to collapse into one bucket are separated
	// here: a cancelled observer, a notification stream that closed for a
	// cause the broker epoch recorded, and a stream that closed with no cause
	// recorded at all.
	codexObserverReasonCancelled    codexObserverReason = "observer-cancelled"
	codexObserverReasonStreamClosed codexObserverReason = "stream-closed"

	// Notification-stream close causes. These come from the broker epoch, not
	// from a guess made above it, and they are what separates "the upstream
	// connection went away" from "the broker replaced this epoch" from "the
	// binding was revoked". Which one appears is the answer to who closed the
	// stream.
	codexObserverReasonEndpointSuspended codexObserverReason = "endpoint-suspended"
	codexObserverReasonEpochRotated      codexObserverReason = "epoch-rotated"
	codexObserverReasonBindingRevoked    codexObserverReason = "binding-revoked"
	codexObserverReasonBacklogOverflow   codexObserverReason = "backlog-overflow"
	codexObserverReasonEpochClosed       codexObserverReason = "epoch-closed"

	// codexObserverReasonBindingReplaced is the exact binding this observer
	// serves no longer being current, observed from the sink predicate rather
	// than from the broker epoch.
	//
	// It is deliberately not one of the four tokens above. Those answer "who
	// closed the stream" and come from the epoch itself; here the stream was
	// never closed at all - the activation under it was replaced while its
	// epoch was still live and ready. Spelling that with binding-revoked would
	// make a broker-epoch cause appear from a producer that is not the broker
	// epoch, which is the one property of this vocabulary that has to hold.
	codexObserverReasonBindingReplaced codexObserverReason = "binding-replaced"

	// Supervisor-side tokens. The parent process writes these when the child
	// observer never reached its own authority write.
	codexObserverReasonObserverTimeout     codexObserverReason = "observer-timeout"
	codexObserverReasonObserverStartFailed codexObserverReason = "observer-start-failed"
	codexObserverReasonObserverExited      codexObserverReason = "observer-exited"
	codexObserverReasonObserverUnavailable codexObserverReason = "observer-unavailable"
	codexObserverReasonNoActiveEpoch       codexObserverReason = "no active native epoch"

	// codexObserverReasonUnrecorded is the explicit "nothing recorded why"
	// bucket. It exists so that state is nameable and countable instead of
	// being disguised as an endpoint disconnect. No exit path may map to it;
	// codexObserverExitReasons pins that, and seeing it in the field means a
	// producer was added without a token.
	codexObserverReasonUnrecorded codexObserverReason = "unrecorded"

	// codexObserverReasonRetired is the pre-instrumentation literal default.
	// It is retained for reading only: pane options written by an older binary
	// still carry it, and reading them truthfully is better than rendering
	// them as out-of-vocabulary. Nothing in this package emits it.
	codexObserverReasonRetired codexObserverReason = "disconnected"
)

// codexObserverReasons is the whole vocabulary. safeCodexAuthorityReason and
// every record writer validate against exactly this list.
var codexObserverReasons = []codexObserverReason{
	codexObserverReasonReady,
	codexObserverReasonConnecting,
	codexObserverReasonUnsupported,
	codexObserverReasonProtocolError,
	codexObserverReasonTimeout,
	codexObserverReasonUnavailable,
	codexObserverReasonSinkError,
	codexObserverReasonControlUnavailable,
	codexObserverReasonGenerationUnavailable,
	codexObserverReasonThreadUnloaded,
	codexObserverReasonCancelled,
	codexObserverReasonStreamClosed,
	codexObserverReasonEndpointSuspended,
	codexObserverReasonEpochRotated,
	codexObserverReasonBindingRevoked,
	codexObserverReasonBacklogOverflow,
	codexObserverReasonEpochClosed,
	codexObserverReasonBindingReplaced,
	codexObserverReasonObserverTimeout,
	codexObserverReasonObserverStartFailed,
	codexObserverReasonObserverExited,
	codexObserverReasonObserverUnavailable,
	codexObserverReasonNoActiveEpoch,
	codexObserverReasonUnrecorded,
	codexObserverReasonRetired,
}

// codexObserverReasonFor admits one foreign string into the vocabulary. It
// returns the empty reason for anything outside it, so a caller must decide
// what an unknown value means rather than letting it through.
func codexObserverReasonFor(value string) codexObserverReason {
	candidate := codexObserverReason(strings.TrimSpace(value))
	if slices.Contains(codexObserverReasons, candidate) {
		return candidate
	}
	return ""
}

// codexObserverExit is one way out of the observer event loop: the vocabulary
// token that names that path, whether the observer itself is stopping, and
// which recovery transition the path takes.
//
// hold is a stored decision rather than a comparison against the reason. The
// diagnosis and the strategy used to be the same value, so making the
// diagnosis more precise would have silently moved the strategy; they are now
// decided together at each exit and kept apart afterwards.
type codexObserverExit struct {
	reason codexObserverReason
	// hold suppresses provider-hook fallback and keeps the one unavailable
	// projection published while the durable broker binding reconnects
	// underneath this loop.
	hold bool
	// stopping means the observer process is going away, so no recovery
	// attempt follows the transition.
	stopping bool
}

var (
	// codexObserverExitCancelled is the ctx cancellation exit. It is not an
	// endpoint disconnect and no longer shares a token with one.
	codexObserverExitCancelled = codexObserverExit{reason: codexObserverReasonCancelled, stopping: true}
	// codexObserverExitProtocolError is every exit taken because a delivered
	// frame could not be decoded or applied.
	codexObserverExitProtocolError = codexObserverExit{reason: codexObserverReasonProtocolError}
	// codexObserverExitThreadUnloaded is the exit taken when the reduced
	// projection says the bound thread is gone.
	codexObserverExitThreadUnloaded = codexObserverExit{reason: codexObserverReasonThreadUnloaded}
)

// codexObserverStreamExit is the exit for a closed notification stream.
//
// hold is true for every cause because the binding beneath a closed stream is
// still reconnecting on the broker's own backoff, which is exactly the
// strategy this path took before its cause was observable. The cause only
// changes what is reported, never what is done.
func codexObserverStreamExit(reason codexObserverReason) codexObserverExit {
	if reason == "" {
		reason = codexObserverReasonStreamClosed
	}
	return codexObserverExit{reason: reason, hold: true}
}

// codexLifecycleStreamCause is implemented by a connection that records why
// its notification stream closed. Without it the observer can only report that
// the stream ended; with it the report names who ended it.
type codexLifecycleStreamCause interface {
	NotificationsClosedCause() codexObserverReason
}

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

type codexGenerationLifecycleSink interface {
	SetGenerationAuthority(codexLifecycleIdentity, coremetadata.CodexEndpointRef, coremetadata.CodexGenerationState, coremetadata.CodexAuthorityRef) error
}

type codexGenerationLifecycleSource interface {
	GenerationLifecycle(codexLifecycleIdentity, coremetadata.CodexEndpointRef) (coremetadata.CodexGenerationLifecycleRef, bool)
}

type codexGenerationAuthorityConnection interface {
	GenerationAuthority() (coremetadata.CodexAuthorityRef, error)
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
	Identity    codexLifecycleIdentity
	Route       tmuxTransport
	NativeRoute codexNativeEndpointRoute
}

func (t codexLifecycleObserverTarget) valid() bool {
	return t.Identity.valid() && t.Route.Flag() != "" && t.Route.Value != "" && t.NativeRoute.valid()
}

type codexNativeObserver struct {
	identity        codexLifecycleIdentity
	endpoint        coremetadata.CodexEndpointRef
	generationState coremetadata.CodexGenerationState
	authority       *coremetadata.CodexAuthorityRef
	open            func(context.Context) (codexLifecycleConnection, error)
	sink            codexLifecycleSink
	delay           time.Duration
	maxDelay        time.Duration
	waitRecovery    func(context.Context, time.Duration) bool
	bindingTimeout  time.Duration
	openTimeout     time.Duration
	sequence        uint64
	reducer         codexLifecycleReducer
	startControl    func(*codexControlEpoch) (*codexControlServer, error)
	requireControl  bool
	reportStartup   func(codexObserverStartupResult)
	progress        agentprogress.Reducer
	now             func() time.Time
	// transitions is the durable history sink. The pane option holds one
	// current value, so without this an observer's connect/disconnect
	// sequence is unobservable between two samples.
	transitions codexObserverJournal
	// recovery is the identity of the epoch loss the scheduler is currently
	// recovering from. It is carried so a terminal recovery record can name
	// the epoch and the cause that started it, which is the whole content of
	// the distinction between a Pane that is still reconnecting and one that
	// is stuck.
	recovery codexObserverRecovery
}

// codexObserverRecovery names the lost epoch one recovery pass is working
// back from.
type codexObserverRecovery struct {
	epochLabel string
	reason     codexObserverReason
}

// journal appends one lifecycle transition to the durable sink, if one is
// configured. A missing sink never blocks a transition.
func (o *codexNativeObserver) journal(kind codexObserverTransition, epochLabel string, reason codexObserverReason) {
	if o.transitions == nil {
		return
	}
	o.transitions.RecordObserverTransition(o.identity, kind, epochLabel, reason)
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
				o.setStartupFallback(codexObserverReasonObserverTimeout)
			} else {
				o.reportStartupResult(codexObserverStartupResult{Status: codexObserverStartupStale})
			}
			return nil
		}
		if err := o.clearProgress(); err != nil {
			o.setStartupFallback(codexObserverReasonSinkError)
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
				if retry, recoveryErr := o.continueRecovery(ctx, recoveryAttempts, reason); recoveryErr != nil {
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
				if retry, recoveryErr := o.continueRecovery(ctx, recoveryAttempts, codexObserverReasonUnsupported); recoveryErr != nil {
					return recoveryErr
				} else if !retry {
					return nil
				}
				continue
			}
			o.setStartupFallback(codexObserverReasonUnsupported)
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
				if retry, recoveryErr := o.continueRecovery(ctx, recoveryAttempts, codexNativeReason(snapshotErr)); recoveryErr != nil {
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
				if retry, recoveryErr := o.continueRecovery(ctx, recoveryAttempts, codexObserverReasonProtocolError); recoveryErr != nil {
					return recoveryErr
				} else if !retry {
					return nil
				}
				continue
			}
			o.setStartupFallback(codexObserverReasonProtocolError)
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
				if retry, recoveryErr := o.continueRecovery(ctx, recoveryAttempts, codexObserverReasonThreadUnloaded); recoveryErr != nil {
					return recoveryErr
				} else if !retry {
					return nil
				}
				continue
			}
			transitionErr := o.applyInvalidationAndFallback(epochLabel, codexObserverReasonThreadUnloaded, projection)
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
		if o.endpoint.Valid() {
			authoritySource, ok := client.(codexGenerationAuthorityConnection)
			authority, authorityErr := coremetadata.CodexAuthorityRef{}, errors.New("generation broker authority is unavailable")
			if ok {
				authority, authorityErr = authoritySource.GenerationAuthority()
			}
			generationSink, ok := o.sink.(codexGenerationLifecycleSink)
			lifecycle, lifecycleOK := o.generationLifecycle()
			if authorityErr != nil || !authority.Valid() || !authority.Endpoint().Same(o.endpoint) || !ok || !lifecycleOK ||
				generationSink.SetGenerationAuthority(o.identity, o.endpoint, lifecycle.State, authority) != nil {
				_ = client.Close()
				o.setStartupFallback(codexObserverReasonGenerationUnavailable)
				return nil
			}
			o.authority = &authority
			projection = o.decorateGenerationProjection(projection)
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
				if retry, recoveryErr := o.continueRecovery(ctx, recoveryAttempts, codexObserverReasonControlUnavailable); recoveryErr != nil {
					return recoveryErr
				} else if !retry {
					return nil
				}
				continue
			}
			cleanupErr := o.invalidateAndFallback(epoch, epochLabel, codexObserverReasonControlUnavailable)
			_ = client.Close()
			if cleanupErr != nil {
				return cleanupErr
			}
			if recovering {
				recoveryAttempts++
				if retry, recoveryErr := o.continueRecovery(ctx, recoveryAttempts, codexObserverReasonControlUnavailable); recoveryErr != nil {
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
			cleanupErr := o.invalidateAndFallback(epoch, epochLabel, codexObserverReasonSinkError)
			_ = client.Close()
			return errors.Join(err, cleanupErr)
		}
		if err := o.sink.SetAuthority(o.identity, codexAuthorityControlPlane, epochLabel, string(codexObserverReasonReady)); err != nil {
			if control != nil {
				_ = control.Close()
			}
			cleanupErr := o.invalidateAndFallback(epoch, epochLabel, codexObserverReasonSinkError)
			_ = client.Close()
			return errors.Join(err, cleanupErr)
		}
		if err := o.flushProgress(); err != nil {
			if control != nil {
				_ = control.Close()
				control = nil
			}
			cleanupErr := o.invalidateAndFallback(epoch, epochLabel, codexObserverReasonSinkError)
			_ = client.Close()
			return errors.Join(err, cleanupErr)
		}
		o.journal(codexObserverTransitionConnected, epochLabel, codexObserverReasonReady)
		o.reportStartupResult(codexObserverStartupResult{Status: codexObserverStartupReady, Epoch: epochLabel})
		recovering = false
		recoveryAttempts = 0

		// The zero exit is the unrecorded bucket on purpose: if a new break
		// were added without naming its path, the record and the pane option
		// would both say "unrecorded" instead of impersonating a disconnect.
		exit := codexObserverExit{reason: codexObserverReasonUnrecorded}
		invalidated := false
		bindingTicker := time.NewTicker(codexObserverBindingDelay)
		progressTicker := time.NewTicker(25 * time.Millisecond)
		notifications := client.Notifications()
	eventLoop:
		for {
			select {
			case <-ctx.Done():
				exit = codexObserverExitCancelled
				break eventLoop
			case <-bindingTicker.C:
				if o.sink.BindingCurrent(o.identity) {
					continue
				}
				// BindingCurrent answers one false for two different questions:
				// this activation was replaced, and the binding could not be
				// read right now - a Registry load that failed, or a tmux
				// invocation that did. Ending the live epoch on a single sample
				// retired a healthy producer on one failed read. The loop head
				// and the recovery scheduler both wait that window out, so this
				// path makes the same bounded wait.
				if waitForCodexLifecycleBinding(ctx, o.sink, o.identity, bindingTimeout) {
					continue
				}
				// The exact binding is provably gone, so ending here is right:
				// a replaced activation has its own observer. What was wrong is
				// that this exit was invisible. It publishes no authority - and
				// must not, because SetAuthority refuses that write once the
				// predicate is false - so the Pane keeps the ready projection
				// this epoch committed with no producer left behind it. That is
				// the inverse of a stuck Pane: nothing looks wrong at all. The
				// terminal record is the only surface the two differ on, and it
				// is the same record the recovery scheduler writes.
				o.journal(codexObserverTransitionStopped, epochLabel, codexObserverReasonBindingReplaced)
				o.reportStartupResult(codexObserverStartupResult{Status: codexObserverStartupStale})
				bindingTicker.Stop()
				progressTicker.Stop()
				if control != nil {
					_ = control.Close()
				}
				_ = client.Close()
				return nil
			case <-progressTicker.C:
				if err := o.flushProgress(); err != nil {
					if control != nil {
						_ = control.Close()
						control = nil
					}
					cleanupErr := o.invalidateAndFallback(epoch, epochLabel, codexObserverReasonSinkError)
					bindingTicker.Stop()
					progressTicker.Stop()
					_ = client.Close()
					return errors.Join(err, cleanupErr)
				}
			case notification, open := <-notifications:
				if !open {
					exit = o.streamCloseExit(client)
					break eventLoop
				}
				if control != nil {
					if controlErr := control.epoch.ApplyNotification(notification); controlErr != nil {
						exit = codexObserverExitProtocolError
						break eventLoop
					}
				}
				event, recognized, decodeErr := codexappserver.DecodeLifecycleEvent(notification)
				if decodeErr != nil {
					exit = codexObserverExitProtocolError
					break eventLoop
				}
				if !recognized {
					progressEvent, progressRecognized, progressErr := codexappserver.DecodeProgressEvent(notification, o.currentTime())
					if progressErr != nil {
						exit = codexObserverExitProtocolError
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
						cleanupErr := o.invalidateAndFallback(epoch, epochLabel, codexObserverReasonSinkError)
						bindingTicker.Stop()
						progressTicker.Stop()
						_ = client.Close()
						return errors.Join(err, cleanupErr)
					}
					continue
				}
				projection = o.decorateGenerationProjection(o.reducer.apply(epoch, event))
				if !projection.Accepted {
					continue
				}
				responderAvailable := false
				if control != nil {
					responderAvailable = control.epoch.HasActionableRequest(event.RequestID)
				}
				markCodexApprovalAvailability(&projection, responderAvailable)
				if projection.Invalidated {
					exit = codexObserverExitThreadUnloaded
					if control != nil {
						_ = control.Close()
						control = nil
					}
					if err := o.applyInvalidationAndFallback(epochLabel, exit.reason, projection); err != nil {
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
					cleanupErr := o.invalidateAndFallback(epoch, epochLabel, codexObserverReasonSinkError)
					bindingTicker.Stop()
					_ = client.Close()
					return errors.Join(err, cleanupErr)
				}
				if progressEvent, progressRecognized, progressErr := codexappserver.DecodeProgressEvent(notification, o.currentTime()); progressErr != nil {
					exit = codexObserverExitProtocolError
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
							cleanupErr := o.invalidateAndFallback(epoch, epochLabel, codexObserverReasonSinkError)
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
						cleanupErr := o.invalidateAndFallback(epoch, epochLabel, codexObserverReasonSinkError)
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
		o.journal(codexObserverTransitionDisconnected, epochLabel, exit.reason)
		if !invalidated {
			// The broker binding survives a transient endpoint disconnect and
			// owns the reconnect, so a closed stream keeps one deterministic
			// unavailable projection until its replacement barrier and control
			// endpoint are both ready; provider-hook is not reconnect
			// authority. Every other exit falls back. The exit carries that
			// decision, so refining a reason token cannot move it.
			transition := o.invalidateAndFallback
			if exit.hold {
				transition = o.invalidateAndHold
			}
			if err := transition(epoch, epochLabel, exit.reason); err != nil {
				return err
			}
		}
		if exit.stopping {
			return nil
		}
		recovering = true
		recoveryAttempts = 0
		o.recovery = codexObserverRecovery{epochLabel: epochLabel, reason: exit.reason}
		o.journal(codexObserverTransitionReconnecting, epochLabel, exit.reason)
		if retry, recoveryErr := o.continueRecovery(ctx, recoveryAttempts, exit.reason); recoveryErr != nil {
			return recoveryErr
		} else if !retry {
			return nil
		}
	}
	return nil
}

func (o *codexNativeObserver) decorateGenerationProjection(projection codexLifecycleProjection) codexLifecycleProjection {
	if !projection.Accepted || !o.endpoint.Valid() {
		return projection
	}
	lifecycle, ok := o.generationLifecycle()
	if !ok {
		return codexLifecycleProjection{}
	}
	projection.Endpoint = &o.endpoint
	projection.GenerationState = lifecycle.State
	if lifecycle.Operation != nil {
		operation := *lifecycle.Operation
		projection.Operation = &operation
	}
	if o.authority == nil || !o.authority.Valid() || !o.authority.Endpoint().Same(o.endpoint) {
		return codexLifecycleProjection{}
	}
	authority := *o.authority
	projection.Authority = &authority
	return projection
}

// generationLifecycle reads the durable planned-state operation at projection
// time. A watcher may be born while its endpoint is Current and remain alive
// after admission changes that exact Agent to Draining; the route's launch
// state is therefore only a compatibility fallback for ordinary Current rows,
// never authority for a planned state.
func (o *codexNativeObserver) generationLifecycle() (coremetadata.CodexGenerationLifecycleRef, bool) {
	if source, ok := o.sink.(codexGenerationLifecycleSource); ok {
		return source.GenerationLifecycle(o.identity, o.endpoint)
	}
	lifecycle := coremetadata.CodexGenerationLifecycleRef{State: o.generationState}
	return lifecycle, lifecycle.ValidFor(&o.endpoint)
}

// continueRecovery is the only retry scheduler after a ready epoch is lost.
// The old control endpoint is already revoked and the one unavailable
// invalidation projection is published before this method is called. Every
// caller increments the attempt count only after one failed replacement open,
// snapshot, or control proof, so the count paces the backoff; it never
// terminates the recovery. A non-zero count therefore means one attempt failed,
// and the token that names that failure is recorded before the next wait.
//
// The only exits are a binding that is no longer current and a cancelled
// context. A live activation whose endpoint is merely away remains
// invalidating instead of being exposed to hook fallback, because the broker
// beneath this loop is still reconnecting on its own capped backoff for as long
// as the binding exists.
//
// The binding check is the same bounded wait the loop head makes, not a single
// instantaneous sample. BindingCurrent folds two different answers into one
// false: the binding was genuinely replaced, and the binding could not be read
// right now - a Registry load that failed, or a tmux invocation that did. The
// loop head has always tolerated the second for bindingTimeout; taking the
// terminal exit on one sample here meant a transient read failure retired a
// live activation permanently, leaving its Pane on the invalidating projection
// this method was called after with no producer left to move it.
func (o *codexNativeObserver) continueRecovery(ctx context.Context, attempts int, failed codexObserverReason) (bool, error) {
	if attempts > 0 {
		// One replacement attempt just failed. Every caller already computed
		// the vocabulary token for its own failure and this scheduler used to
		// discard it, so a recovery that could not make progress produced no
		// record at all: the Pane held the one invalidating projection and an
		// operator could not tell a retrying observer from a stuck one in
		// either direction. Naming the failure on the retry transition is the
		// whole difference, and the journal's coalescing window keeps a long
		// outage to one line plus a repeat count.
		//
		// The reason column of the disconnect that opened recovery is
		// untouched, so which side closed the stream is still read there.
		o.journal(codexObserverTransitionReconnecting, o.recovery.epochLabel, failed)
	}
	bindingTimeout := o.bindingTimeout
	if bindingTimeout <= 0 {
		bindingTimeout = codexObserverBindingTimeout
	}
	if !waitForCodexLifecycleBinding(ctx, o.sink, o.identity, bindingTimeout) {
		if ctx.Err() == nil {
			// The exact binding is provably gone, so no later transition can
			// move this Pane and SetAuthority would refuse the write anyway.
			// Recording the stop is what separates it from a Pane still
			// reconnecting on the same invalidating projection.
			o.journal(codexObserverTransitionStopped, o.recovery.epochLabel, o.recovery.reason)
		}
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

func (o *codexNativeObserver) setStartupFallback(reason codexObserverReason) {
	if err := o.sink.SetAuthority(o.identity, codexAuthorityHook, "", string(reason)); err == nil {
		o.journal(codexObserverTransitionFallback, "", reason)
		o.reportStartupResult(codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: string(reason)})
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
func (o *codexNativeObserver) invalidateAndFallback(epoch uint64, epochLabel string, reason codexObserverReason) error {
	projection := o.decorateGenerationProjection(o.reducer.invalidate(epoch))
	if !projection.Accepted {
		return errors.New("codex native lifecycle epoch could not be invalidated")
	}
	return o.applyInvalidation(epochLabel, reason, projection, true)
}

func (o *codexNativeObserver) applyInvalidationAndFallback(epochLabel string, reason codexObserverReason, projection codexLifecycleProjection) error {
	return o.applyInvalidation(epochLabel, reason, projection, true)
}

// invalidateAndHold publishes the reconnect gap exactly once and deliberately
// leaves provider-hook suppressed. The same durable broker binding is still
// reconnecting, so only a replacement snapshot and exact control proof may
// move the Pane out of this unavailable projection.
func (o *codexNativeObserver) invalidateAndHold(epoch uint64, epochLabel string, reason codexObserverReason) error {
	projection := o.decorateGenerationProjection(o.reducer.invalidate(epoch))
	if !projection.Accepted {
		return errors.New("codex native lifecycle epoch could not be invalidated")
	}
	return o.applyInvalidation(epochLabel, reason, projection, false)
}

func (o *codexNativeObserver) applyInvalidation(epochLabel string, reason codexObserverReason, projection codexLifecycleProjection, fallback bool) error {
	if !projection.Accepted || !projection.Invalidated {
		return errors.New("codex native lifecycle invalidation projection is not accepted")
	}
	if err := o.sink.SetAuthority(o.identity, codexAuthorityInvalidating, epochLabel, string(reason)); err != nil {
		return err
	}
	if err := o.sink.Apply(o.identity, projection); err != nil {
		// The clear may have failed after invalidating became current. Keep hooks
		// suppressed and make the bounded diagnostic truthful; never expose
		// provider-hook while stale state may remain.
		_ = o.sink.SetAuthority(o.identity, codexAuthorityInvalidating, epochLabel, string(codexObserverReasonSinkError))
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
	if err := o.sink.SetAuthority(o.identity, codexAuthorityHook, "", string(reason)); err != nil {
		return err
	}
	o.journal(codexObserverTransitionFallback, epochLabel, reason)
	o.reportStartupResult(codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: string(reason)})
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

// codexNativeReason maps one transport error onto the shared vocabulary. Its
// four tokens are not a second vocabulary: they are members of
// codexObserverReasons, so an open failure and a loop exit are comparable.
func codexNativeReason(err error) codexObserverReason {
	switch {
	case errors.Is(err, codexappserver.ErrUnsupported):
		return codexObserverReasonUnsupported
	case errors.Is(err, codexappserver.ErrProtocol):
		return codexObserverReasonProtocolError
	case errors.Is(err, context.DeadlineExceeded):
		return codexObserverReasonTimeout
	default:
		return codexObserverReasonUnavailable
	}
}

// streamCloseExit reads the close cause from the connection that owns it. A
// connection that records no cause yields the plain stream-closed token, which
// is still distinct from a cancelled observer and from an endpoint disconnect.
func (o *codexNativeObserver) streamCloseExit(client codexLifecycleConnection) codexObserverExit {
	source, ok := client.(codexLifecycleStreamCause)
	if !ok {
		return codexObserverStreamExit(codexObserverReasonStreamClosed)
	}
	return codexObserverStreamExit(codexObserverReasonFor(string(source.NotificationsClosedCause())))
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

func (s aiCodexLifecycleSink) GenerationLifecycle(identity codexLifecycleIdentity, endpoint coremetadata.CodexEndpointRef) (coremetadata.CodexGenerationLifecycleRef, bool) {
	if s.command == nil || s.command.loadRegistry == nil || !endpoint.Valid() {
		return coremetadata.CodexGenerationLifecycleRef{}, false
	}
	registry, err := s.command.loadRegistry()
	if err != nil || !exactCodexLifecycleBinding(registry, identity) {
		return coremetadata.CodexGenerationLifecycleRef{}, false
	}
	agent, ok := registry.Agent(identity.AgentUID)
	if !ok || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil ||
		agent.Status.SessionRef.Codex.Endpoint == nil || !agent.Status.SessionRef.Codex.Endpoint.Same(endpoint) ||
		agent.Status.SessionRef.Codex.Lifecycle == nil || !agent.Status.SessionRef.Codex.Lifecycle.ValidFor(&endpoint) {
		return coremetadata.CodexGenerationLifecycleRef{}, false
	}
	lifecycle := *agent.Status.SessionRef.Codex.Lifecycle
	if lifecycle.Operation != nil {
		operation := *lifecycle.Operation
		lifecycle.Operation = &operation
	}
	return lifecycle, true
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

func (p codexLifecycleProjection) generationAware() bool {
	return p.Endpoint != nil || p.GenerationState != "" || p.Operation != nil || p.Authority != nil
}

func (p codexLifecycleProjection) generationInput() codexgeneration.LifecycleProjectionInput {
	return codexgeneration.LifecycleProjectionInput{
		Interaction: p.Interaction, Endpoint: p.Endpoint,
		GenerationState: p.GenerationState, Operation: p.Operation,
	}
}

// exactCodexGenerationMutation closes every content-free authority dimension
// before either the Registry or tmux presentation writer may run.
func exactCodexGenerationMutation(registry coremetadata.Registry, identity codexLifecycleIdentity, projection codexLifecycleProjection) bool {
	if !projection.generationAware() || !projection.generationInput().Authoritative() || projection.Authority == nil ||
		!projection.Endpoint.Same(projection.Authority.Endpoint()) || !exactCodexLifecycleBinding(registry, identity) {
		return false
	}
	agent, ok := registry.Agent(identity.AgentUID)
	if !ok || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil {
		return false
	}
	storedLifecycle := agent.Status.SessionRef.Codex.Lifecycle
	if storedLifecycle == nil || storedLifecycle.State != projection.GenerationState ||
		!sameCodexGenerationOperation(storedLifecycle.Operation, projection.Operation) {
		return false
	}
	pane, ok := registry.Pane(identity.PaneUID)
	if !ok || pane.Status.Activation.Codex == nil {
		return false
	}
	decision := codexgeneration.ApplyRuntimeMutation(codexgeneration.RuntimeMutationInput{
		DurableEndpoint: agent.Status.SessionRef.Codex.Endpoint,
		StoredAuthority: pane.Status.Activation.Codex.Authority, PresentedAuthority: projection.Authority,
		TargetRuntimeID: pane.Status.Activation.RuntimeID, EventRuntimeID: identity.RuntimeID,
	}, nil, nil)
	return decision.Class.Effect == codexgeneration.MutationSemanticEffect
}

func sameCodexGenerationOperation(stored, presented *coremetadata.CodexGenerationOperationRef) bool {
	if stored == nil || presented == nil {
		return stored == nil && presented == nil
	}
	return *stored == *presented
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

func (s aiCodexLifecycleSink) SetGenerationAuthority(identity codexLifecycleIdentity, endpoint coremetadata.CodexEndpointRef, state coremetadata.CodexGenerationState, authority coremetadata.CodexAuthorityRef) error {
	if s.command == nil || s.command.updateRegistry == nil || !endpoint.Valid() || !authority.Valid() ||
		!authority.Endpoint().Same(endpoint) {
		return errManagedAgentObservationIgnored
	}
	release, err := s.command.acquireCodexAuthorityFence(identity.PaneUID)
	if err != nil {
		return err
	}
	defer release()
	if !s.BindingCurrent(identity) {
		return errManagedAgentObservationIgnored
	}
	_, err = s.command.updateRegistry(func(registry *coremetadata.Registry) error {
		if !exactCodexLifecycleBinding(*registry, identity) {
			return errManagedAgentObservationIgnored
		}
		agent, ok := registry.Agent(identity.AgentUID)
		if !ok || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil ||
			agent.Status.SessionRef.Codex.Endpoint == nil || !agent.Status.SessionRef.Codex.Endpoint.Same(endpoint) ||
			agent.Status.SessionRef.Codex.Lifecycle == nil || agent.Status.SessionRef.Codex.Lifecycle.State != state ||
			!agent.Status.SessionRef.Codex.Lifecycle.ValidFor(&endpoint) {
			return errManagedAgentObservationIgnored
		}
		pane, ok := registry.Pane(identity.PaneUID)
		if !ok || pane.Status.Activation.Codex == nil {
			return errManagedAgentObservationIgnored
		}
		stored := authority
		pane.Status.Activation.Codex.Authority = &stored
		return nil
	})
	return err
}

func (s aiCodexLifecycleSink) Apply(identity codexLifecycleIdentity, projection codexLifecycleProjection) error {
	c := s.command
	if c == nil || !projection.Accepted || c.updateRegistry == nil {
		return errManagedAgentObservationIgnored
	}
	if projection.generationAware() {
		if !projection.generationInput().Authoritative() || projection.Authority == nil {
			return errManagedAgentObservationIgnored
		}
	}
	// Registry interaction, queue cleanup, and Pane presentation form one
	// native semantic write set. Every projection owns the same exact-Pane
	// fence as SetAuthority, including the legacy/non-generation-aware lane;
	// otherwise invalidation can land between the Registry and tmux halves and
	// an older Apply can restore stale Pane semantics after the clear.
	release, err := c.acquireCodexAuthorityFence(identity.PaneUID)
	if err != nil {
		return err
	}
	defer release()
	if !s.BindingCurrent(identity) {
		return errManagedAgentObservationIgnored
	}
	if projection.generationAware() {
		registry, err := c.loadRegistry()
		if err != nil {
			return err
		}
		if !exactCodexGenerationMutation(registry, identity, projection) {
			return errManagedAgentObservationIgnored
		}
	}
	policy := c.codexSemanticPolicyForInteraction(projection.Interaction)
	delivery := codexSemanticDeliveryForLifecycle(policy, projection.generationInput())
	mutator := intmetadata.DefaultMutator()
	mutator.Now = c.sessionRefClock()
	if _, err := c.updateRegistry(func(registry *coremetadata.Registry) error {
		if !exactCodexLifecycleBinding(*registry, identity) {
			return errManagedAgentObservationIgnored
		}
		if projection.generationAware() && !exactCodexGenerationMutation(*registry, identity, projection) {
			return errManagedAgentObservationIgnored
		}
		if _, err := mutator.SetAgentInteraction(registry, identity.AgentUID, delivery.RegistryInteraction, string(coremetadata.InteractionSourceProviderControl)); err != nil {
			return err
		}
		if projection.HasStartedTurn {
			agent, ok := registry.Agent(identity.AgentUID)
			if !ok || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil ||
				agent.Status.SessionRef.Codex.Endpoint == nil || projection.Endpoint == nil ||
				!agent.Status.SessionRef.Codex.Endpoint.Same(*projection.Endpoint) ||
				strings.TrimSpace(agent.Status.SessionRef.Codex.ThreadID) != identity.ThreadID {
				return errManagedAgentObservationIgnored
			}
			agent.Status.SessionRef.Codex.HasStartedTurn = true
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
	if projection.generationAware() {
		registry, err := c.loadRegistry()
		if err != nil {
			return err
		}
		if !exactCodexGenerationMutation(registry, identity, projection) {
			return errManagedAgentObservationIgnored
		}
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
		consumer := codexgeneration.ConsumerProjection{}
		if projection.generationAware() {
			registry, err := c.loadRegistry()
			if err != nil {
				return err
			}
			if !exactCodexGenerationMutation(registry, identity, projection) {
				return errManagedAgentObservationIgnored
			}
			consumer = codexgeneration.ProjectConsumers(projection.generationInput(), codexgeneration.RuntimeMutationInput{
				DurableEndpoint: projection.Endpoint, StoredAuthority: projection.Authority, PresentedAuthority: projection.Authority,
				TargetRuntimeID: identity.RuntimeID, EventRuntimeID: identity.RuntimeID,
			}, true)
			if consumer.Effect != codexgeneration.MutationSemanticEffect || !consumer.Notification {
				continue
			}
		}
		metadata := map[string]string{
			notify.MetaAgent: aiModeCodex, notify.MetaCategory: notice.Category,
			"thread_id": notice.ThreadID, "turn_id": notice.TurnID,
		}
		if projection.generationAware() {
			metadata[notify.MetaAgentUID] = identity.AgentUID
			metadata[notify.MetaPaneUID] = identity.PaneUID
			metadata[notify.MetaStateDomainID] = consumer.Endpoint.StateDomainID
			metadata[notify.MetaEndpointGenerationID] = consumer.Endpoint.EndpointGenerationID
			metadata[notify.MetaAuthorityFence] = consumer.Fence
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
			if pane == nil || pane.Status.Activation.Codex == nil || agent.Status.SessionRef == nil ||
				agent.Status.SessionRef.Codex == nil || agent.Status.SessionRef.Codex.Endpoint == nil ||
				!agent.Status.SessionRef.Codex.Endpoint.Valid() {
				return errManagedAgentObservationIgnored
			}
			if pane.Status.Activation.Codex.TurnID != progress.TurnRef {
				changed, refineErr := mutator.RefineCodexActivation(registry, coremetadata.CodexActivationObservation{
					AgentUID: identity.AgentUID, PaneUID: identity.PaneUID, Generation: identity.Generation,
					ThreadID: identity.ThreadID, TurnID: progress.TurnRef, Endpoint: *agent.Status.SessionRef.Codex.Endpoint,
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
	return codexSemanticDeliveryForLifecycle(policy, codexgeneration.LifecycleProjectionInput{Interaction: interaction})
}

func codexSemanticDeliveryForLifecycle(policy config.AISemanticPolicy, input codexgeneration.LifecycleProjectionInput) codexSemanticDelivery {
	projection := codexgeneration.ProjectLifecycle(input)
	delivery := codexSemanticDelivery{
		RegistryInteraction: input.Interaction,
		State:               projection.State, Badge: projection.Badge, Attention: projection.Attention,
	}
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
	if err := sink.SetAuthority(identity, codexAuthorityPending, "", string(codexObserverReasonConnecting)); err != nil {
		return codexObserverStartupResult{Status: codexObserverStartupStale}
	}
	executable := c.executable
	if executable == nil {
		executable = os.Executable
	}
	path, err := executable()
	if err != nil {
		return convergeCodexObserverStartupFallback(sink, identity, string(codexObserverReasonObserverStartFailed))
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
		return codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: string(codexObserverReasonObserverStartFailed)}
	}
	identity := target.Identity
	args := []string{"internal", "agent-hook", "ingest", codexNativeLifecycleIngestRoute,
		"--agent-uid", identity.AgentUID, "--pane-uid", identity.PaneUID, "--pane", identity.RuntimeID,
		"--generation", identity.Generation, "--thread", identity.ThreadID,
		"--state-domain", target.NativeRoute.Endpoint.StateDomainID,
		"--endpoint-generation", target.NativeRoute.Endpoint.EndpointGenerationID,
		"--endpoint-state", string(target.NativeRoute.State),
		"--tui-executable", target.NativeRoute.TUIExecutable}
	if target.NativeRoute.Default {
		args = append(args, "--endpoint-default", "true")
	} else {
		args = append(args, "--endpoint-socket", target.NativeRoute.SocketPath)
	}
	if target.Route.Flag() == "-L" {
		args = append(args, "--tmux-socket-name", target.Route.Value)
	} else if target.Route.Flag() == "-S" {
		args = append(args, "--tmux-socket-path", target.Route.Value)
	} else {
		return codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: string(codexObserverReasonObserverStartFailed)}
	}
	// #nosec G204 -- executable is an absolute, existing, regular executable validated above; argv is a fixed internal route plus bounded identity values and never enters a shell.
	cmd := exec.Command(executable, args...)
	cmd.Env = append(withoutInheritedTmuxEnvironment(os.Environ()), codexObserverStartupEnvironment+"=1")
	configureCodexObserverProcess(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: string(codexObserverReasonObserverStartFailed)}
	}
	if err := cmd.Start(); err != nil {
		return codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: string(codexObserverReasonObserverStartFailed)}
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
			return codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: string(codexObserverReasonObserverExited)}
		}
		if result.Status == codexObserverStartupReady {
			settled := time.NewTimer(codexObserverStartupSettle)
			defer settled.Stop()
			select {
			case <-line:
				terminateCodexObserverProcess(cmd)
				return codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: string(codexObserverReasonObserverExited)}
			case <-settled.C:
				_ = stdout.Close()
				if err := cmd.Process.Release(); err != nil {
					terminateCodexObserverProcess(cmd)
					return codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: string(codexObserverReasonObserverStartFailed)}
				}
				return result
			}
		}
		terminateCodexObserverProcess(cmd)
		return result
	case <-timer.C:
		terminateCodexObserverProcess(cmd)
		return codexObserverStartupResult{Status: codexObserverStartupFallback, Reason: string(codexObserverReasonObserverTimeout)}
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
	session, sessionErr := newCodexBrokerObserverSessionForRoute(target.Identity, "", nil, target.NativeRoute)
	if sessionErr != nil {
		result := convergeCodexObserverStartupFallback(sink, target.Identity, string(codexNativeReason(sessionErr)))
		if report := codexObserverStartupReporter(); report != nil {
			report(result)
		}
		return nil
	}
	defer func() { _ = session.Close() }()
	observer := codexNativeObserver{
		identity:        target.Identity,
		endpoint:        target.NativeRoute.Endpoint,
		generationState: target.NativeRoute.State,
		requireControl:  true,
		sink:            sink,
		reportStartup:   codexObserverStartupReporter(),
		open:            session.Open,
		openTimeout:     codexBrokerObserverOpenTimeout,
		transitions:     newCodexObserverLogJournal(c.appendAIIngestLog, c.now),
	}
	if paths, err := config.DefaultPathsFromEnv(); err == nil {
		observer.startControl = func(epoch *codexControlEpoch) (*codexControlServer, error) {
			return startCodexControlServer(paths.StateDir, target.NativeRoute.Endpoint, epoch)
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
		case "--state-domain":
			target.NativeRoute.Endpoint.StateDomainID = value
		case "--endpoint-generation":
			target.NativeRoute.Endpoint.EndpointGenerationID = value
		case "--endpoint-state":
			target.NativeRoute.State = codexgeneration.GenerationState(value)
		case "--endpoint-socket":
			target.NativeRoute.SocketPath = value
		case "--endpoint-default":
			target.NativeRoute.Default = value == "true"
		case "--tui-executable":
			target.NativeRoute.TUIExecutable = value
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
	codexNativeDeclaredInteractiveOnly,
	codexNativeDeclaredPayloadFreeFallback,
	codexNativeDeclaredRolloutCatalogResume,
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
