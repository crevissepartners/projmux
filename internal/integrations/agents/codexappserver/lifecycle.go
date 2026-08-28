package codexappserver

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	DefaultStartTimeout     = 12 * time.Second
	DefaultReadinessTimeout = 4 * time.Second
	defaultReadinessDelay   = 25 * time.Millisecond
	maxReadinessDelay       = 250 * time.Millisecond
)

// TriggerKind is the closed caller-intent vocabulary for daemon lifecycle.
// Only TriggerNativeUserAction is allowed to mutate daemon state.
type TriggerKind string

const (
	TriggerNativeUserAction TriggerKind = "native-user-action"
	TriggerDoctor           TriggerKind = "doctor"
	TriggerSettings         TriggerKind = "settings"
	TriggerSupportReport    TriggerKind = "support-report"
)

// LifecycleOutcome is the closed, content-free result of one ensure-ready
// decision. It never contains process output or endpoint paths.
type LifecycleOutcome string

const (
	LifecycleAlreadyRunning LifecycleOutcome = "already-running"
	LifecycleStarted        LifecycleOutcome = "started"
	LifecycleStartFailed    LifecycleOutcome = "start-failed"
	LifecycleNotAttempted   LifecycleOutcome = "not-attempted"
	LifecycleRefused        LifecycleOutcome = "refused"
)

// LifecycleReason explains a LifecycleOutcome without retaining command
// output, socket paths, prompts, tokens, or response payloads.
type LifecycleReason string

const (
	LifecycleReasonReadOnly                   LifecycleReason = "read-only"
	LifecycleReasonAlreadyReady               LifecycleReason = "already-ready"
	LifecycleReasonReadyAfterStart            LifecycleReason = "ready-after-start"
	LifecycleReasonProbeExecutableMissing     LifecycleReason = "probe-executable-missing"
	LifecycleReasonProbeTimeout               LifecycleReason = "probe-timeout"
	LifecycleReasonProbeUnsupported           LifecycleReason = "probe-unsupported"
	LifecycleReasonProbeProtocolError         LifecycleReason = "probe-protocol-error"
	LifecycleReasonProbeEndpointError         LifecycleReason = "probe-endpoint-error"
	LifecycleReasonProbeUnavailable           LifecycleReason = "probe-unavailable"
	LifecycleReasonStartExecutableMissing     LifecycleReason = "start-executable-missing"
	LifecycleReasonStartManagedPayloadMissing LifecycleReason = "start-managed-payload-missing"
	LifecycleReasonStartNonzero               LifecycleReason = "start-nonzero"
	LifecycleReasonStartTimeout               LifecycleReason = "start-timeout"
	LifecycleReasonReadinessExecutableMissing LifecycleReason = "readiness-executable-missing"
	LifecycleReasonReadinessSocketUnavailable LifecycleReason = "readiness-socket-unavailable"
	LifecycleReasonReadinessTimeout           LifecycleReason = "readiness-timeout"
	LifecycleReasonReadinessUnsupported       LifecycleReason = "readiness-unsupported"
	LifecycleReasonReadinessProtocolError     LifecycleReason = "readiness-protocol-error"
	LifecycleReasonReadinessEndpointError     LifecycleReason = "readiness-endpoint-error"
	LifecycleReasonUnsafeUnmanaged            LifecycleReason = "unsafe-unmanaged"
	LifecycleReasonUnsafeVersionSkew          LifecycleReason = "unsafe-version-skew"
	LifecycleReasonUnsafeOwnershipUnknown     LifecycleReason = "unsafe-ownership-unknown"
	LifecycleReasonUnsafeRuntimeUnknown       LifecycleReason = "unsafe-runtime-version-unknown"
)

type startResult uint8

const (
	startNotRun startResult = iota
	startSucceeded
	startExecutableMissing
	startManagedPayloadMissing
	startNonzero
	startTimedOut
)

type readinessResult uint8

const (
	readinessNotRun readinessResult = iota
	readinessReady
	readinessExecutableMissing
	readinessSocketUnavailable
	readinessTimedOut
	readinessUnsupported
	readinessProtocolError
	readinessEndpointError
)

type probeResult uint8

const (
	probeReady probeResult = iota
	probeDaemonNotRunning
	probeNotStartable
)

type lifecycleFlight struct {
	done   chan struct{}
	health Health
}

type lifecycleFlights struct {
	mu         sync.Mutex
	current    *lifecycleFlight
	generation uint64
	last       Health
}

type lifecyclePolicy struct {
	probe            func(context.Context) Health
	start            func(context.Context) startResult
	startTimeout     time.Duration
	probeTimeout     time.Duration
	readinessTimeout time.Duration
	readinessDelay   time.Duration
	flights          *lifecycleFlights
}

var defaultLifecycleFlights lifecycleFlights

// EnsureDefaultProxyReady is the minimal trigger seam for later native Codex
// consumers. Read-only triggers probe only. A native user action may invoke the
// official idempotent daemon start command once, and only after an exact
// daemon-not-running probe reason. Concurrent callers share one in-flight
// start/readiness result within this process.
func EnsureDefaultProxyReady(ctx context.Context, trigger TriggerKind, projmuxVersion string, hookAvailable bool) (Health, error) {
	policy := lifecyclePolicy{
		probe: func(probeCtx context.Context) Health {
			// Keep the shared probe result independent from any caller's fallback
			// capability. Each caller projects the result after joining the flight.
			return ProbeDefaultProxy(probeCtx, DefaultProbeTimeout, projmuxVersion, true)
		},
		start: func(startCtx context.Context) startResult {
			return runDaemonStart(startCtx, DefaultStartTimeout, exec.LookPath, exec.CommandContext)
		},
		startTimeout:     DefaultStartTimeout,
		probeTimeout:     DefaultProbeTimeout,
		readinessTimeout: DefaultReadinessTimeout,
		readinessDelay:   defaultReadinessDelay,
		flights:          &defaultLifecycleFlights,
	}
	return policy.ensureReadyForHook(ctx, trigger, hookAvailable)
}

func (p lifecyclePolicy) ensureReadyForHook(ctx context.Context, trigger TriggerKind, hookAvailable bool) (Health, error) {
	if err := ctx.Err(); err != nil {
		return Health{}, err
	}
	p.flights.mu.Lock()
	observedGeneration := p.flights.generation
	p.flights.mu.Unlock()
	initial := p.probe(ctx)
	if err := ctx.Err(); err != nil {
		return Health{}, err
	}
	probeState := classifyProbe(initial)
	if trigger != TriggerNativeUserAction {
		return projectLifecycleHealth(withLifecycle(initial, LifecycleNotAttempted, LifecycleReasonReadOnly), hookAvailable), nil
	}
	if initial.NativeAction == NativeActionRefused {
		return projectLifecycleHealth(withLifecycle(initial, LifecycleRefused, nativeActionLifecycleReason(initial.NativeRefusal)), hookAvailable), nil
	}
	if probeState == probeReady {
		return projectLifecycleHealth(withLifecycle(initial, LifecycleAlreadyRunning, LifecycleReasonAlreadyReady), hookAvailable), nil
	}
	if probeState != probeDaemonNotRunning {
		return projectLifecycleHealth(withLifecycle(initial, LifecycleNotAttempted, probeLifecycleReason(initial)), hookAvailable), nil
	}
	if err := ctx.Err(); err != nil {
		return Health{}, err
	}
	return p.joinFlight(ctx, observedGeneration, hookAvailable)
}

func (p lifecyclePolicy) joinFlight(ctx context.Context, observedGeneration uint64, hookAvailable bool) (Health, error) {
	p.flights.mu.Lock()
	if p.flights.generation != observedGeneration {
		health := p.flights.last
		p.flights.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return Health{}, err
		}
		return projectLifecycleHealth(health, hookAvailable), nil
	}
	flight := p.flights.current
	if flight == nil {
		flight = &lifecycleFlight{done: make(chan struct{})}
		p.flights.current = flight
		go p.runFlight(context.WithoutCancel(ctx), flight)
	}
	p.flights.mu.Unlock()

	select {
	case <-ctx.Done():
		return Health{}, ctx.Err()
	case <-flight.done:
		return projectLifecycleHealth(flight.health, hookAvailable), nil
	}
}

func (p lifecyclePolicy) runFlight(parent context.Context, flight *lifecycleFlight) {
	startTimeout := positiveDuration(p.startTimeout, DefaultStartTimeout)
	readinessTimeout := positiveDuration(p.readinessTimeout, DefaultReadinessTimeout)
	probeTimeout := positiveDuration(p.probeTimeout, DefaultProbeTimeout)
	flightCtx, cancel := context.WithTimeout(parent, probeTimeout+startTimeout+readinessTimeout)
	defer cancel()

	// Re-probe under the single-flight boundary. This closes the race where
	// several callers observed a cold socket just before the first start.
	probeHealth := p.probe(flightCtx)
	probeState := classifyProbe(probeHealth)
	if probeHealth.NativeAction == NativeActionRefused {
		p.finishFlight(flight, withLifecycle(probeHealth, LifecycleRefused, nativeActionLifecycleReason(probeHealth.NativeRefusal)))
		return
	}
	if probeState == probeReady {
		p.finishFlight(flight, withLifecycle(probeHealth, LifecycleAlreadyRunning, LifecycleReasonAlreadyReady))
		return
	}
	if probeState != probeDaemonNotRunning {
		p.finishFlight(flight, withLifecycle(probeHealth, LifecycleNotAttempted, probeLifecycleReason(probeHealth)))
		return
	}

	startCtx, startCancel := context.WithTimeout(flightCtx, startTimeout)
	startState := p.start(startCtx)
	startCancel()
	if startState != startSucceeded {
		outcome, reason := decideLifecycle(TriggerNativeUserAction, probeState, startState, readinessNotRun, LifecycleReasonProbeUnavailable)
		p.finishFlight(flight, withLifecycle(probeHealth, outcome, reason))
		return
	}

	health, readinessState := p.waitReady(flightCtx, readinessTimeout)
	if health.NativeAction == NativeActionRefused {
		p.finishFlight(flight, withLifecycle(health, LifecycleRefused, nativeActionLifecycleReason(health.NativeRefusal)))
		return
	}
	outcome, reason := decideLifecycle(TriggerNativeUserAction, probeState, startState, readinessState, LifecycleReasonProbeUnavailable)
	p.finishFlight(flight, withLifecycle(health, outcome, reason))
}

func (p lifecyclePolicy) waitReady(parent context.Context, timeout time.Duration) (Health, readinessResult) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	delay := positiveDuration(p.readinessDelay, defaultReadinessDelay)
	for {
		last := p.probe(ctx)
		lastState := classifyReadiness(last)
		switch lastState {
		case readinessReady, readinessExecutableMissing, readinessUnsupported, readinessProtocolError, readinessEndpointError:
			return last, lastState
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if lastState == readinessSocketUnavailable {
				return last, readinessSocketUnavailable
			}
			return last, readinessTimedOut
		case <-timer.C:
		}
		if delay < maxReadinessDelay {
			delay *= 2
			if delay > maxReadinessDelay {
				delay = maxReadinessDelay
			}
		}
	}
}

func (p lifecyclePolicy) finishFlight(flight *lifecycleFlight, health Health) {
	p.flights.mu.Lock()
	flight.health = health
	if p.flights.current == flight {
		p.flights.current = nil
	}
	p.flights.generation++
	p.flights.last = health
	close(flight.done)
	p.flights.mu.Unlock()
}

func runDaemonStart(ctx context.Context, timeout time.Duration, lookPath func(string) (string, error), command func(context.Context, string, ...string) *exec.Cmd) startResult {
	path, err := lookPath("codex")
	if err != nil {
		return startExecutableMissing
	}
	startCtx, cancel := context.WithTimeout(ctx, positiveDuration(timeout, DefaultStartTimeout))
	defer cancel()
	cmd := command(startCtx, path, "app-server", "daemon", "start")
	// Only a bounded prefix of stderr is consumed in-memory for the closed known
	// error classifier. The bytes are discarded when this function returns and
	// never enter Health, operational diagnostics, or support reports.
	stderr := boundedStartCapture{remaining: maxStartStderrBytes}
	cmd.Stdout = discardWriter{}
	cmd.Stderr = &stderr
	err = cmd.Run()
	if err == nil {
		return startSucceeded
	}
	if errors.Is(startCtx.Err(), context.DeadlineExceeded) {
		return startTimedOut
	}
	if errors.Is(err, exec.ErrNotFound) {
		return startExecutableMissing
	}
	if classifyDaemonStartStderr(stderr.Bytes()) == startManagedPayloadMissing {
		return startManagedPayloadMissing
	}
	return startNonzero
}

const maxStartStderrBytes = 32 * 1024

type boundedStartCapture struct {
	buffer    bytes.Buffer
	remaining int
}

func (w *boundedStartCapture) Write(p []byte) (int, error) {
	originalLen := len(p)
	if w.remaining > 0 {
		keep := min(len(p), w.remaining)
		_, _ = w.buffer.Write(p[:keep])
		w.remaining -= keep
	}
	return originalLen, nil
}

func (w *boundedStartCapture) Bytes() []byte { return w.buffer.Bytes() }

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func classifyDaemonStartStderr(stderr []byte) startResult {
	text := string(stderr)
	for _, signature := range []string{
		"managed standalone Codex install not found",
		"requires the standalone install managed by the Codex installer",
	} {
		if strings.Contains(text, signature) {
			return startManagedPayloadMissing
		}
	}
	return startNonzero
}

func decideLifecycle(trigger TriggerKind, probeState probeResult, startState startResult, readinessState readinessResult, probeReason LifecycleReason) (LifecycleOutcome, LifecycleReason) {
	if trigger != TriggerNativeUserAction {
		return LifecycleNotAttempted, LifecycleReasonReadOnly
	}
	switch probeState {
	case probeReady:
		return LifecycleAlreadyRunning, LifecycleReasonAlreadyReady
	case probeNotStartable:
		return LifecycleNotAttempted, probeReason
	}
	switch startState {
	case startExecutableMissing:
		return LifecycleStartFailed, LifecycleReasonStartExecutableMissing
	case startManagedPayloadMissing:
		return LifecycleStartFailed, LifecycleReasonStartManagedPayloadMissing
	case startNonzero:
		return LifecycleStartFailed, LifecycleReasonStartNonzero
	case startTimedOut:
		return LifecycleStartFailed, LifecycleReasonStartTimeout
	case startNotRun:
		return LifecycleNotAttempted, probeReason
	}
	switch readinessState {
	case readinessReady:
		return LifecycleStarted, LifecycleReasonReadyAfterStart
	case readinessExecutableMissing:
		return LifecycleStartFailed, LifecycleReasonReadinessExecutableMissing
	case readinessSocketUnavailable:
		return LifecycleStartFailed, LifecycleReasonReadinessSocketUnavailable
	case readinessUnsupported:
		return LifecycleStartFailed, LifecycleReasonReadinessUnsupported
	case readinessProtocolError:
		return LifecycleStartFailed, LifecycleReasonReadinessProtocolError
	case readinessEndpointError:
		return LifecycleStartFailed, LifecycleReasonReadinessEndpointError
	default:
		return LifecycleStartFailed, LifecycleReasonReadinessTimeout
	}
}

func classifyProbe(health Health) probeResult {
	if health.Availability == AvailabilityAvailable {
		return probeReady
	}
	if healthCause(health) == ReasonDaemonNotRunning {
		return probeDaemonNotRunning
	}
	return probeNotStartable
}

func classifyReadiness(health Health) readinessResult {
	if health.Availability == AvailabilityAvailable {
		return readinessReady
	}
	switch healthCause(health) {
	case ReasonDaemonNotRunning:
		return readinessSocketUnavailable
	case ReasonExecutableMissing:
		return readinessExecutableMissing
	case ReasonTimeout:
		return readinessTimedOut
	case ReasonUnsupported:
		return readinessUnsupported
	case ReasonProtocolError:
		return readinessProtocolError
	default:
		return readinessEndpointError
	}
}

func probeLifecycleReason(health Health) LifecycleReason {
	switch healthCause(health) {
	case ReasonExecutableMissing:
		return LifecycleReasonProbeExecutableMissing
	case ReasonTimeout:
		return LifecycleReasonProbeTimeout
	case ReasonUnsupported:
		return LifecycleReasonProbeUnsupported
	case ReasonProtocolError:
		return LifecycleReasonProbeProtocolError
	case ReasonEndpointUnavailable:
		return LifecycleReasonProbeEndpointError
	default:
		return LifecycleReasonProbeUnavailable
	}
}

func healthCause(health Health) Reason {
	if health.ProbeReason != "" {
		return health.ProbeReason
	}
	return health.Reason
}

func withLifecycle(health Health, outcome LifecycleOutcome, reason LifecycleReason) Health {
	health.Lifecycle = outcome
	health.LifecycleReason = reason
	return health
}

func projectLifecycleHealth(health Health, hookAvailable bool) Health {
	projected := Decide(health.Availability, healthCause(health), health.Version, health.Endpoint, health.Connection, hookAvailable)
	projected.InstallCapability = health.InstallCapability
	projected.EndpointReadiness = health.EndpointReadiness
	projected.RunningExecutable = health.RunningExecutable
	projected.VersionRelation = health.VersionRelation
	projected.CLIVersion = health.CLIVersion
	projected.ManagedVersion = health.ManagedVersion
	projected.RunningVersion = health.RunningVersion
	projected.ManagerOwnership = health.ManagerOwnership
	projected.RemoteControl = health.RemoteControl
	projected.NativeAction = health.NativeAction
	projected.NativeRefusal = health.NativeRefusal
	projected.InterruptionRisk = health.InterruptionRisk
	projected.OperatorRecovery = health.OperatorRecovery
	projected.Lifecycle = health.Lifecycle
	projected.LifecycleReason = health.LifecycleReason
	return projected
}

func nativeActionLifecycleReason(reason NativeActionRefusal) LifecycleReason {
	switch reason {
	case NativeActionRefusalUnmanaged, NativeActionRefusalUnmanagedVersionSkew:
		return LifecycleReasonUnsafeUnmanaged
	case NativeActionRefusalVersionSkew:
		return LifecycleReasonUnsafeVersionSkew
	case NativeActionRefusalRuntimeVersionUnknown:
		return LifecycleReasonUnsafeRuntimeUnknown
	default:
		return LifecycleReasonUnsafeOwnershipUnknown
	}
}

func positiveDuration(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}
