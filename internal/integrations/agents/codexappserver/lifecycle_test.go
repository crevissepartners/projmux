package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestLifecycleDecisionTableExhaustive(t *testing.T) {
	triggers := []TriggerKind{TriggerNativeUserAction, TriggerDoctor, TriggerSettings, TriggerSupportReport}
	probes := []probeResult{probeReady, probeDaemonNotRunning, probeNotStartable}
	starts := []startResult{startNotRun, startSucceeded, startExecutableMissing, startManagedPayloadMissing, startNonzero, startTimedOut}
	readiness := []readinessResult{
		readinessNotRun,
		readinessReady,
		readinessExecutableMissing,
		readinessSocketUnavailable,
		readinessTimedOut,
		readinessUnsupported,
		readinessProtocolError,
		readinessEndpointError,
	}
	for _, trigger := range triggers {
		for _, probe := range probes {
			for _, start := range starts {
				for _, ready := range readiness {
					outcome, reason := decideLifecycle(trigger, probe, start, ready, LifecycleReasonProbeTimeout)
					wantOutcome, wantReason := expectedLifecycleDecision(trigger, probe, start, ready)
					if outcome != wantOutcome || reason != wantReason {
						t.Fatalf("decision(%q,%d,%d,%d) = %q/%q, want %q/%q", trigger, probe, start, ready, outcome, reason, wantOutcome, wantReason)
					}
				}
			}
		}
	}
}

func expectedLifecycleDecision(trigger TriggerKind, probe probeResult, start startResult, ready readinessResult) (LifecycleOutcome, LifecycleReason) {
	if trigger != TriggerNativeUserAction {
		return LifecycleNotAttempted, LifecycleReasonReadOnly
	}
	if probe == probeReady {
		return LifecycleAlreadyRunning, LifecycleReasonAlreadyReady
	}
	if probe == probeNotStartable {
		return LifecycleNotAttempted, LifecycleReasonProbeTimeout
	}
	switch start {
	case startNotRun:
		return LifecycleNotAttempted, LifecycleReasonProbeTimeout
	case startExecutableMissing:
		return LifecycleStartFailed, LifecycleReasonStartExecutableMissing
	case startManagedPayloadMissing:
		return LifecycleStartFailed, LifecycleReasonStartManagedPayloadMissing
	case startNonzero:
		return LifecycleStartFailed, LifecycleReasonStartNonzero
	case startTimedOut:
		return LifecycleStartFailed, LifecycleReasonStartTimeout
	}
	switch ready {
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

func TestLifecycleAlreadyRunningNeverStarts(t *testing.T) {
	var starts atomic.Int32
	policy := testLifecyclePolicy(
		func(context.Context) Health { return testProbeHealth(AvailabilityAvailable, ReasonNone) },
		func(context.Context) startResult {
			starts.Add(1)
			return startSucceeded
		},
	)
	health, err := policy.ensureReadyForHook(context.Background(), TriggerNativeUserAction, true)
	if err != nil {
		t.Fatal(err)
	}
	if health.Lifecycle != LifecycleAlreadyRunning || health.LifecycleReason != LifecycleReasonAlreadyReady {
		t.Fatalf("health = %+v", health)
	}
	if got := starts.Load(); got != 0 {
		t.Fatalf("start calls = %d, want 0", got)
	}
}

func TestLifecycleConcurrentColdCallersShareOneStartAndCancellation(t *testing.T) {
	const callers = 24
	var probes atomic.Int32
	var starts atomic.Int32
	var running atomic.Bool
	allInitialProbes := make(chan struct{})
	releaseInitialProbes := make(chan struct{})
	releaseStart := make(chan struct{})
	policy := testLifecyclePolicy(
		func(context.Context) Health {
			count := probes.Add(1)
			if count == callers {
				close(allInitialProbes)
			}
			if count <= callers {
				<-releaseInitialProbes
				return testProbeHealth(AvailabilityUnavailable, ReasonDaemonNotRunning)
			}
			if running.Load() {
				return testProbeHealth(AvailabilityAvailable, ReasonNone)
			}
			return testProbeHealth(AvailabilityUnavailable, ReasonDaemonNotRunning)
		},
		func(context.Context) startResult {
			starts.Add(1)
			<-releaseStart
			running.Store(true)
			return startSucceeded
		},
	)

	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		health Health
		err    error
	}
	results := make(chan result, callers)
	for i := range callers {
		callCtx := context.Background()
		if i == 0 {
			callCtx = ctx
		}
		go func() {
			health, err := policy.ensureReadyForHook(callCtx, TriggerNativeUserAction, true)
			results <- result{health: health, err: err}
		}()
	}

	select {
	case <-allInitialProbes:
	case <-time.After(time.Second):
		t.Fatal("initial probes did not converge")
	}
	close(releaseInitialProbes)
	cancel()
	cancelled := <-results
	if !errors.Is(cancelled.err, context.Canceled) {
		t.Fatalf("cancelled caller error = %v", cancelled.err)
	}
	close(releaseStart)
	for i := 1; i < callers; i++ {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatal(got.err)
			}
			if got.health.Lifecycle != LifecycleStarted {
				t.Fatalf("shared health = %+v", got.health)
			}
		case <-time.After(time.Second):
			t.Fatal("waiter leaked")
		}
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("start calls = %d, want 1", got)
	}
}

func TestLifecycleInitiatingCallerCancellationDoesNotCancelSharedFlight(t *testing.T) {
	var probes atomic.Int32
	var starts atomic.Int32
	var running atomic.Bool
	startEntered := make(chan struct{})
	releaseStart := make(chan struct{})
	secondInitialProbe := make(chan struct{})
	policy := testLifecyclePolicy(
		func(context.Context) Health {
			count := probes.Add(1)
			if count == 3 {
				close(secondInitialProbe)
			}
			if running.Load() {
				return testProbeHealth(AvailabilityAvailable, ReasonNone)
			}
			return testProbeHealth(AvailabilityUnavailable, ReasonDaemonNotRunning)
		},
		func(ctx context.Context) startResult {
			starts.Add(1)
			close(startEntered)
			select {
			case <-releaseStart:
				running.Store(true)
				return startSucceeded
			case <-ctx.Done():
				return startTimedOut
			}
		},
	)

	initiatorCtx, cancelInitiator := context.WithCancel(context.Background())
	initiatorDone := make(chan error, 1)
	go func() {
		_, err := policy.ensureReadyForHook(initiatorCtx, TriggerNativeUserAction, true)
		initiatorDone <- err
	}()
	<-startEntered
	cancelInitiator()
	if err := <-initiatorDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("initiator error = %v, want context canceled", err)
	}

	waiterDone := make(chan struct {
		health Health
		err    error
	}, 1)
	go func() {
		health, err := policy.ensureReadyForHook(context.Background(), TriggerNativeUserAction, true)
		waiterDone <- struct {
			health Health
			err    error
		}{health: health, err: err}
	}()
	select {
	case <-secondInitialProbe:
	case <-time.After(time.Second):
		t.Fatal("second caller did not reach its initial probe")
	}
	close(releaseStart)
	select {
	case got := <-waiterDone:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.health.Lifecycle != LifecycleStarted {
			t.Fatalf("waiter health = %+v", got.health)
		}
	case <-time.After(time.Second):
		t.Fatal("shared flight did not finish")
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("start calls = %d, want 1", got)
	}
}

func TestLifecycleSharedFailureProjectsFallbackPerCaller(t *testing.T) {
	var probes atomic.Int32
	var starts atomic.Int32
	initialProbesDone := make(chan struct{})
	releaseInitialProbes := make(chan struct{})
	policy := testLifecyclePolicy(
		func(context.Context) Health {
			count := probes.Add(1)
			if count <= 2 {
				if count == 2 {
					close(initialProbesDone)
				}
				<-releaseInitialProbes
			}
			return testProbeHealth(AvailabilityUnavailable, ReasonDaemonNotRunning)
		},
		func(context.Context) startResult {
			starts.Add(1)
			return startNonzero
		},
	)

	type result struct {
		health Health
		err    error
	}
	withHook := make(chan result, 1)
	withoutHook := make(chan result, 1)
	go func() {
		health, err := policy.ensureReadyForHook(context.Background(), TriggerNativeUserAction, true)
		withHook <- result{health: health, err: err}
	}()
	go func() {
		health, err := policy.ensureReadyForHook(context.Background(), TriggerNativeUserAction, false)
		withoutHook <- result{health: health, err: err}
	}()
	select {
	case <-initialProbesDone:
	case <-time.After(time.Second):
		t.Fatal("initial probes did not converge")
	}
	close(releaseInitialProbes)

	gotWithHook := <-withHook
	gotWithoutHook := <-withoutHook
	if gotWithHook.err != nil || gotWithoutHook.err != nil {
		t.Fatalf("errors = %v, %v", gotWithHook.err, gotWithoutHook.err)
	}
	if gotWithHook.health.Source != SourceHookFallback || gotWithHook.health.Reason != ReasonDaemonNotRunning {
		t.Fatalf("with hook health = %+v", gotWithHook.health)
	}
	if gotWithoutHook.health.Source != SourceUnavailable || gotWithoutHook.health.Reason != ReasonHookUnavailable {
		t.Fatalf("without hook health = %+v", gotWithoutHook.health)
	}
	if gotWithHook.health.Lifecycle != LifecycleStartFailed || gotWithoutHook.health.Lifecycle != LifecycleStartFailed {
		t.Fatalf("lifecycle results = %+v, %+v", gotWithHook.health, gotWithoutHook.health)
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("start calls = %d, want 1", got)
	}
}

func TestLifecycleCancellationDuringInitialProbe(t *testing.T) {
	for _, trigger := range []TriggerKind{TriggerDoctor, TriggerNativeUserAction} {
		t.Run(string(trigger), func(t *testing.T) {
			probeEntered := make(chan struct{})
			var starts atomic.Int32
			policy := testLifecyclePolicy(
				func(ctx context.Context) Health {
					close(probeEntered)
					<-ctx.Done()
					return testProbeHealth(AvailabilityUnavailable, ReasonEndpointUnavailable)
				},
				func(context.Context) startResult {
					starts.Add(1)
					return startSucceeded
				},
			)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				_, err := policy.ensureReadyForHook(ctx, trigger, true)
				done <- err
			}()
			<-probeEntered
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error = %v, want context canceled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("canceled initial probe did not return")
			}
			if got := starts.Load(); got != 0 {
				t.Fatalf("start calls = %d, want 0", got)
			}
		})
	}
}

func TestLifecycleLateColdCallerUsesCompletedGeneration(t *testing.T) {
	var probes atomic.Int32
	var starts atomic.Int32
	startEntered := make(chan struct{})
	releaseStart := make(chan struct{})
	lateProbeEntered := make(chan struct{})
	releaseLateProbe := make(chan struct{})
	policy := testLifecyclePolicy(
		func(context.Context) Health {
			if probes.Add(1) == 3 {
				close(lateProbeEntered)
				<-releaseLateProbe
			}
			return testProbeHealth(AvailabilityUnavailable, ReasonDaemonNotRunning)
		},
		func(context.Context) startResult {
			starts.Add(1)
			close(startEntered)
			<-releaseStart
			return startNonzero
		},
	)
	type result struct {
		health Health
		err    error
	}
	first := make(chan result, 1)
	late := make(chan result, 1)
	go func() {
		health, err := policy.ensureReadyForHook(context.Background(), TriggerNativeUserAction, true)
		first <- result{health: health, err: err}
	}()
	<-startEntered
	go func() {
		health, err := policy.ensureReadyForHook(context.Background(), TriggerNativeUserAction, true)
		late <- result{health: health, err: err}
	}()
	<-lateProbeEntered
	close(releaseStart)
	firstResult := <-first
	close(releaseLateProbe)
	lateResult := <-late
	if firstResult.err != nil || lateResult.err != nil {
		t.Fatalf("errors = %v, %v", firstResult.err, lateResult.err)
	}
	if firstResult.health.LifecycleReason != LifecycleReasonStartNonzero || lateResult.health.LifecycleReason != LifecycleReasonStartNonzero {
		t.Fatalf("results = %+v, %+v", firstResult.health, lateResult.health)
	}
	if got := starts.Load(); got != 1 {
		t.Fatalf("start calls = %d, want 1", got)
	}
}

func TestLifecycleSequentialColdInvocationsDoNotReuseCompletedFlight(t *testing.T) {
	var starts atomic.Int32
	policy := testLifecyclePolicy(
		func(context.Context) Health {
			return testProbeHealth(AvailabilityUnavailable, ReasonDaemonNotRunning)
		},
		func(context.Context) startResult {
			starts.Add(1)
			return startNonzero
		},
	)
	for range 2 {
		health, err := policy.ensureReadyForHook(context.Background(), TriggerNativeUserAction, true)
		if err != nil {
			t.Fatal(err)
		}
		if health.LifecycleReason != LifecycleReasonStartNonzero {
			t.Fatalf("health = %+v", health)
		}
	}
	if got := starts.Load(); got != 2 {
		t.Fatalf("start calls = %d, want 2", got)
	}
}

func TestLifecycleFailureMatrix(t *testing.T) {
	tests := []struct {
		name       string
		start      startResult
		readiness  Health
		wantReason LifecycleReason
	}{
		{name: "command missing", start: startExecutableMissing, wantReason: LifecycleReasonStartExecutableMissing},
		{name: "managed payload missing", start: startManagedPayloadMissing, wantReason: LifecycleReasonStartManagedPayloadMissing},
		{name: "nonzero", start: startNonzero, wantReason: LifecycleReasonStartNonzero},
		{name: "start timeout", start: startTimedOut, wantReason: LifecycleReasonStartTimeout},
		{name: "socket not created", start: startSucceeded, readiness: testProbeHealth(AvailabilityUnavailable, ReasonDaemonNotRunning), wantReason: LifecycleReasonReadinessSocketUnavailable},
		{name: "protocol incompatible", start: startSucceeded, readiness: testProbeHealth(AvailabilityProtocolError, ReasonProtocolError), wantReason: LifecycleReasonReadinessProtocolError},
		{name: "unsupported initialize", start: startSucceeded, readiness: testProbeHealth(AvailabilityUnsupported, ReasonUnsupported), wantReason: LifecycleReasonReadinessUnsupported},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var probeCalls atomic.Int32
			policy := testLifecyclePolicy(
				func(context.Context) Health {
					if probeCalls.Add(1) <= 2 || tc.readiness.Reason == "" {
						return testProbeHealth(AvailabilityUnavailable, ReasonDaemonNotRunning)
					}
					return tc.readiness
				},
				func(context.Context) startResult { return tc.start },
			)
			health, err := policy.ensureReadyForHook(context.Background(), TriggerNativeUserAction, true)
			if err != nil {
				t.Fatal(err)
			}
			if health.Lifecycle != LifecycleStartFailed || health.LifecycleReason != tc.wantReason {
				t.Fatalf("health = %+v", health)
			}
		})
	}
}

func TestLifecycleReadOnlyTriggersNeverStart(t *testing.T) {
	var starts atomic.Int32
	policy := testLifecyclePolicy(
		func(context.Context) Health { return testProbeHealth(AvailabilityUnavailable, ReasonDaemonNotRunning) },
		func(context.Context) startResult {
			starts.Add(1)
			return startSucceeded
		},
	)
	for _, trigger := range []TriggerKind{TriggerDoctor, TriggerSettings, TriggerSupportReport} {
		for range 10 {
			health, err := policy.ensureReadyForHook(context.Background(), trigger, true)
			if err != nil {
				t.Fatal(err)
			}
			if health.Lifecycle != LifecycleNotAttempted || health.LifecycleReason != LifecycleReasonReadOnly {
				t.Fatalf("%s health = %+v", trigger, health)
			}
		}
	}
	if got := starts.Load(); got != 0 {
		t.Fatalf("daemon mutation calls = %d, want 0", got)
	}
}

func TestLifecycleOnlyExactDaemonNotRunningCanStart(t *testing.T) {
	for _, reason := range []Reason{ReasonExecutableMissing, ReasonEndpointUnavailable, ReasonTimeout, ReasonUnsupported, ReasonProtocolError, ReasonDisconnected, ReasonHookUnavailable} {
		t.Run(string(reason), func(t *testing.T) {
			var starts atomic.Int32
			policy := testLifecyclePolicy(
				func(context.Context) Health { return testProbeHealth(AvailabilityUnavailable, reason) },
				func(context.Context) startResult {
					starts.Add(1)
					return startSucceeded
				},
			)
			health, err := policy.ensureReadyForHook(context.Background(), TriggerNativeUserAction, true)
			if err != nil {
				t.Fatal(err)
			}
			if health.Lifecycle != LifecycleNotAttempted || starts.Load() != 0 {
				t.Fatalf("reason %q health=%+v starts=%d", reason, health, starts.Load())
			}
		})
	}
}

func TestFakeCLIAndProxyColdThenAlreadyRunning(t *testing.T) {
	var running atomic.Bool
	var mu sync.Mutex
	var argv []string
	probe := func(ctx context.Context) Health {
		scenario := "missing-socket"
		if running.Load() {
			scenario = "healthy"
		}
		return probeProxy(ctx, 3*time.Second, "0.13.0", true,
			func(string) (string, error) { return os.Args[0], nil },
			func(commandCtx context.Context) *exec.Cmd {
				cmd := exec.CommandContext(commandCtx, os.Args[0], "-test.run=TestProxyProbeHelperProcess", "--", scenario)
				cmd.Env = append(os.Environ(), "GO_WANT_PROXY_HELPER=1")
				return cmd
			}, func() bool { return !running.Load() })
	}
	start := func(ctx context.Context) startResult {
		result := runDaemonStart(ctx, 3*time.Second,
			func(string) (string, error) { return os.Args[0], nil },
			func(commandCtx context.Context, name string, args ...string) *exec.Cmd {
				mu.Lock()
				argv = append([]string{name}, args...)
				mu.Unlock()
				cmd := exec.CommandContext(commandCtx, os.Args[0], "-test.run=TestDaemonStartHelperProcess")
				cmd.Env = append(os.Environ(), "GO_WANT_DAEMON_START_HELPER=1")
				return cmd
			})
		if result == startSucceeded {
			running.Store(true)
		}
		return result
	}
	policy := testLifecyclePolicy(probe, start)
	policy.startTimeout = 3 * time.Second
	policy.readinessTimeout = 4 * time.Second
	cold, err := policy.ensureReadyForHook(context.Background(), TriggerNativeUserAction, true)
	if err != nil {
		t.Fatal(err)
	}
	warm, err := policy.ensureReadyForHook(context.Background(), TriggerNativeUserAction, true)
	if err != nil {
		t.Fatal(err)
	}
	if cold.Lifecycle != LifecycleStarted || warm.Lifecycle != LifecycleAlreadyRunning {
		t.Fatalf("cold=%+v warm=%+v", cold, warm)
	}
	mu.Lock()
	gotArgv := append([]string(nil), argv...)
	mu.Unlock()
	wantArgv := []string{os.Args[0], "app-server", "daemon", "start"}
	if !reflect.DeepEqual(gotArgv, wantArgv) {
		t.Fatalf("start argv = %#v, want %#v", gotArgv, wantArgv)
	}
}

func TestDaemonStartHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_DAEMON_START_HELPER") != "1" {
		return
	}
	switch os.Getenv("DAEMON_START_SCENARIO") {
	case "nonzero":
		_, _ = os.Stdout.WriteString("/secret/path prompt=never-expose token=never-expose")
		_, _ = os.Stderr.WriteString("process output is not a diagnostic")
		os.Exit(17)
	case "managed-payload-missing":
		_, _ = os.Stderr.WriteString("managed standalone Codex install not found at /private/host/path token=never-expose prompt=never-expose")
		os.Exit(17)
	case "large-managed-payload-missing":
		_, _ = os.Stderr.WriteString(strings.Repeat("x", maxStartStderrBytes))
		_, _ = os.Stderr.WriteString("managed standalone Codex install not found")
		os.Exit(17)
	case "timeout":
		time.Sleep(10 * time.Second)
	}
	os.Exit(0)
}

func TestRunDaemonStartFailureClassificationAndExactArgv(t *testing.T) {
	tests := []struct {
		name     string
		scenario string
		timeout  time.Duration
		missing  bool
		want     startResult
	}{
		{name: "missing", missing: true, timeout: time.Second, want: startExecutableMissing},
		{name: "nonzero", scenario: "nonzero", timeout: time.Second, want: startNonzero},
		{name: "managed payload missing", scenario: "managed-payload-missing", timeout: time.Second, want: startManagedPayloadMissing},
		{name: "signature beyond capture bound", scenario: "large-managed-payload-missing", timeout: time.Second, want: startNonzero},
		{name: "timeout", scenario: "timeout", timeout: 20 * time.Millisecond, want: startTimedOut},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var argv []string
			lookPath := func(string) (string, error) {
				if tc.missing {
					return "", exec.ErrNotFound
				}
				return os.Args[0], nil
			}
			got := runDaemonStart(context.Background(), tc.timeout, lookPath,
				func(commandCtx context.Context, name string, args ...string) *exec.Cmd {
					argv = append([]string{name}, args...)
					cmd := exec.CommandContext(commandCtx, os.Args[0], "-test.run=TestDaemonStartHelperProcess")
					cmd.Env = append(os.Environ(), "GO_WANT_DAEMON_START_HELPER=1", "DAEMON_START_SCENARIO="+tc.scenario)
					return cmd
				})
			if got != tc.want {
				t.Fatalf("start result = %d, want %d", got, tc.want)
			}
			if tc.missing {
				if len(argv) != 0 {
					t.Fatalf("missing executable argv = %#v", argv)
				}
				return
			}
			wantArgv := []string{os.Args[0], "app-server", "daemon", "start"}
			if !reflect.DeepEqual(argv, wantArgv) {
				t.Fatalf("start argv = %#v, want %#v", argv, wantArgv)
			}
		})
	}
}

func TestKnownManagedPayloadFailureProjectsClosedHealthWithoutRawProcessOutput(t *testing.T) {
	policy := testLifecyclePolicy(
		func(context.Context) Health {
			health := testProbeHealth(AvailabilityUnavailable, ReasonDaemonNotRunning)
			health.InstallCapability = InstallCapabilityExternalCLIOnly
			return health
		},
		func(ctx context.Context) startResult {
			return runDaemonStart(ctx, time.Second,
				func(string) (string, error) { return os.Args[0], nil },
				func(commandCtx context.Context, _ string, _ ...string) *exec.Cmd {
					cmd := exec.CommandContext(commandCtx, os.Args[0], "-test.run=TestDaemonStartHelperProcess")
					cmd.Env = append(os.Environ(), "GO_WANT_DAEMON_START_HELPER=1", "DAEMON_START_SCENARIO=managed-payload-missing")
					return cmd
				})
		},
	)
	health, err := policy.ensureReadyForHook(context.Background(), TriggerNativeUserAction, false)
	if err != nil {
		t.Fatal(err)
	}
	if health.Reason != ReasonHookUnavailable || health.ProbeReason != ReasonDaemonNotRunning || health.InstallCapability != InstallCapabilityExternalCLIOnly || health.LifecycleReason != LifecycleReasonStartManagedPayloadMissing {
		t.Fatalf("health axes = %+v", health)
	}
	data, err := json.Marshal(health)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"/private/host/path", "token=never-expose", "prompt=never-expose", "managed standalone Codex install not found"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("closed health leaked %q: %s", forbidden, data)
		}
	}
}

func TestDaemonStartStderrClassifierIsClosedAndNarrow(t *testing.T) {
	for _, signature := range []string{
		"managed standalone Codex install not found at /private/host/path",
		"This command requires the standalone install managed by the Codex installer, because token=private",
	} {
		if got := classifyDaemonStartStderr([]byte(signature)); got != startManagedPayloadMissing {
			t.Fatalf("known signature classified as %d", got)
		}
	}
	for _, unknown := range []string{
		"package not found",
		"standalone installer failed",
		"/private/host/path token=private prompt=private",
		strings.ToUpper("managed standalone Codex install not found"),
	} {
		if got := classifyDaemonStartStderr([]byte(unknown)); got != startNonzero {
			t.Fatalf("unknown stderr %q classified as %d", unknown, got)
		}
	}

	capture := boundedStartCapture{remaining: 8}
	payload := []byte("12345678/private/path token=private prompt=private")
	if n, err := capture.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("bounded write = %d, %v", n, err)
	}
	if got := string(capture.Bytes()); got != "12345678" {
		t.Fatalf("bounded capture = %q", got)
	}
}

func TestDefaultDaemonNotRunningClassifiesOnlyMissingOrRefusedSocket(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	if !defaultDaemonNotRunning() {
		t.Fatal("missing official socket was not classified daemon-not-running")
	}
	socketPath := filepath.Join(codexHome, "app-server-control", "app-server-control.sock")
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(socketPath, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if defaultDaemonNotRunning() {
		t.Fatal("regular file was classified daemon-not-running")
	}
	socketInfo := fakeSocketInfo{}
	if !daemonNotRunningAt(socketPath,
		func(string) (os.FileInfo, error) { return socketInfo, nil },
		func(string, string, time.Duration) (net.Conn, error) { return nil, syscall.ECONNREFUSED },
	) {
		t.Fatal("refused official socket was not classified daemon-not-running")
	}
}

type fakeSocketInfo struct{}

func (fakeSocketInfo) Name() string       { return "app-server-control.sock" }
func (fakeSocketInfo) Size() int64        { return 0 }
func (fakeSocketInfo) Mode() os.FileMode  { return os.ModeSocket | 0o600 }
func (fakeSocketInfo) ModTime() time.Time { return time.Time{} }
func (fakeSocketInfo) IsDir() bool        { return false }
func (fakeSocketInfo) Sys() any           { return nil }

func testLifecyclePolicy(probe func(context.Context) Health, start func(context.Context) startResult) lifecyclePolicy {
	return lifecyclePolicy{
		probe:            probe,
		start:            start,
		startTimeout:     100 * time.Millisecond,
		probeTimeout:     3 * time.Second,
		readinessTimeout: 20 * time.Millisecond,
		readinessDelay:   time.Millisecond,
		flights:          &lifecycleFlights{},
	}
}

func testProbeHealth(availability Availability, reason Reason) Health {
	connection := ConnectionDisconnected
	if availability == AvailabilityAvailable {
		connection = ConnectionReady
	}
	if availability == AvailabilityTimeout {
		connection = ConnectionTimedOut
	}
	if availability == AvailabilityProtocolError {
		connection = ConnectionProtocolErr
	}
	return Decide(availability, reason, "codex-cli/0.149.0", EndpointStdioProxy, connection, true)
}
