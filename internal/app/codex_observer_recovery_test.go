package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentprogress"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func TestCodexNativeReasonNamesBrokerDrain(t *testing.T) {
	drain := &codexbroker.BrokerError{Refusal: codexbroker.RefusalDrainRequired}
	for _, err := range []error{drain, fmt.Errorf("open broker session: %w", drain)} {
		if got := codexNativeReason(err); got != codexObserverReasonDrainRequired {
			t.Fatalf("codexNativeReason(%v) = %q, want drain-required", err, got)
		}
	}
	// Only the typed refusal names the drain; an untyped string says nothing
	// about who refused.
	if got := codexNativeReason(errors.New("drain-required")); got != codexObserverReasonUnavailable {
		t.Fatalf("untyped drain text = %q, want unavailable", got)
	}
	if got := safeCodexAuthorityReason(string(codexObserverReasonDrainRequired)); got != "drain-required" {
		t.Fatalf("drain-required is not a bounded authority reason: %q", got)
	}
}

func TestCodexObserverSinkRetryableExcludesDecisions(t *testing.T) {
	for _, test := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errManagedAgentObservationIgnored, false},
		{fmt.Errorf("apply: %w", errManagedAgentObservationIgnored), false},
		{errCodexLifecycleInvalidationRejected, false},
		{fmt.Errorf("update registry: %w", intmetadata.ErrLockTimeout), true},
		{errors.New("write registry: no space left on device"), true},
	} {
		if got := codexObserverSinkRetryable(test.err); got != test.want {
			t.Errorf("codexObserverSinkRetryable(%v) = %t, want %t", test.err, got, test.want)
		}
	}
}

// TestCodexNativeObserverStartupDrainBacksOffAndNamesReason pins the
// observer side of a broker drain: the Pane names drain-required instead of a
// generic unavailable, the retry backs off to its cap instead of spinning, and
// the first bind after the drain ends commits a ready epoch.
func TestCodexNativeObserverStartupDrainBacksOffAndNamesReason(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	conn := &fakeCodexLifecycleConnection{
		snapshot: codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateIdle},
		events:   make(chan codexappserver.Notification),
	}
	sink := newRecordingCodexLifecycleSink()
	startup := make(chan codexObserverStartupResult, 16)
	var mu sync.Mutex
	var waits []time.Duration
	refusals := 5
	observer := codexNativeObserver{
		identity: identity, sink: sink, delay: time.Millisecond, maxDelay: 4 * time.Millisecond,
		open: func(context.Context) (codexLifecycleConnection, error) {
			mu.Lock()
			defer mu.Unlock()
			if refusals > 0 {
				refusals--
				return nil, fmt.Errorf("broker handshake: %w", &codexbroker.BrokerError{Refusal: codexbroker.RefusalDrainRequired})
			}
			return conn, nil
		},
		waitRecovery: func(ctx context.Context, delay time.Duration) bool {
			mu.Lock()
			waits = append(waits, delay)
			mu.Unlock()
			return ctx.Err() == nil
		},
		reportStartup: func(result codexObserverStartupResult) { startup <- result },
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()
	giveUp := codexObserverGiveUp(t)
	for !slices.Contains(sink.authoritySnapshot(), codexAuthorityControlPlane+":ready") {
		select {
		case <-sink.wake:
		case err := <-done:
			t.Fatalf("observer ended during a drain: %v", err)
		case <-giveUp:
			t.Fatalf("observer never bound after the drain: %#v", sink.authoritySnapshot())
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if first := <-startup; first.Status != codexObserverStartupRetrying || first.Reason != "drain-required" {
		t.Fatalf("first startup report = %+v, want retrying drain-required", first)
	}
	authorities := sink.authoritySnapshot()
	if authorities[0] != codexAuthorityHook+":drain-required" {
		t.Fatalf("drain authority = %#v, want provider-hook:drain-required first", authorities)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []time.Duration{time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond, 4 * time.Millisecond}
	if !slices.Equal(waits, want) {
		t.Fatalf("drain retry waits = %v, want capped backoff %v", waits, want)
	}
}

// TestCodexNativeObserverRecoversAfterRegistryLockTimeout is the observed
// failure: one Registry write timed out behind another command's long lock
// hold and the observer ended for good. It must hold the Pane invalidating,
// name sink-error, and commit a replacement ready epoch once writes succeed.
func TestCodexNativeObserverRecoversAfterRegistryLockTimeout(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	conn := &fakeCodexLifecycleConnection{
		snapshot: codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateActive, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress},
		events:   make(chan codexappserver.Notification, 1),
	}
	conn.events <- codexappserver.Notification{Method: "thread/status/changed", Params: []byte(`{"threadId":"thread-1","status":{"type":"idle"}}`)}
	sink := &lockTimeoutCodexLifecycleSink{recordingCodexLifecycleSink: newRecordingCodexLifecycleSink(), failCalls: map[int]bool{2: true}}
	var journal transitionCodexObserverJournal
	observer := codexNativeObserver{identity: identity, delay: time.Millisecond, sink: sink, transitions: &journal,
		open: func(context.Context) (codexLifecycleConnection, error) { return conn, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- observer.Run(ctx) }()
	giveUp := codexObserverGiveUp(t)
	for countCodexAuthority(sink.authoritySnapshot(), codexAuthorityControlPlane+":ready") < 2 {
		select {
		case <-sink.wake:
		case err := <-done:
			t.Fatalf("observer ended on a Registry lock timeout: %v; authorities=%#v", err, sink.authoritySnapshot())
		case <-giveUp:
			t.Fatalf("observer did not recover: %#v", sink.authoritySnapshot())
		}
	}
	// Shutdown falls back on purpose, so the recovery is judged before it.
	authorities := sink.authoritySnapshot()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	want := []string{codexAuthorityControlPlane + ":ready", codexAuthorityInvalidating + ":sink-error", codexAuthorityControlPlane + ":ready"}
	if !slices.Equal(authorities[:3], want) {
		t.Fatalf("authority sequence = %#v, want %#v", authorities, want)
	}
	if slices.ContainsFunc(authorities, func(a string) bool { return strings.HasPrefix(a, codexAuthorityHook+":") }) {
		t.Fatalf("a transient write failure exposed hook fallback: %#v", authorities)
	}
	if got := journal.kinds(); !slices.Contains(got, "observer.disconnected:sink-error") || !slices.Contains(got, "observer.reconnecting:sink-error") {
		t.Fatalf("journal lacks the sink-error disconnect and recovery: %#v", got)
	}
}

type lockTimeoutCodexLifecycleSink struct {
	*recordingCodexLifecycleSink
	mu        sync.Mutex
	calls     int
	failCalls map[int]bool
}

func (s *lockTimeoutCodexLifecycleSink) Apply(identity codexLifecycleIdentity, projection codexLifecycleProjection) error {
	s.mu.Lock()
	s.calls++
	fail := s.failCalls[s.calls]
	s.mu.Unlock()
	if fail {
		s.record("apply-failed")
		return fmt.Errorf("update registry: %w", intmetadata.ErrLockTimeout)
	}
	return s.recordingCodexLifecycleSink.Apply(identity, projection)
}

func countCodexAuthority(authorities []string, want string) int {
	count := 0
	for _, authority := range authorities {
		if authority == want {
			count++
		}
	}
	return count
}

type transitionCodexObserverJournal struct {
	mu      sync.Mutex
	records []string
}

func (j *transitionCodexObserverJournal) RecordObserverTransition(_ codexLifecycleIdentity, kind codexObserverTransition, _ string, reason codexObserverReason) {
	j.mu.Lock()
	j.records = append(j.records, string(kind)+":"+string(reason))
	j.mu.Unlock()
}

func (j *transitionCodexObserverJournal) kinds() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.records...)
}

func TestExplainCodexRouteReasonNamesLifecycleRecovery(t *testing.T) {
	agent := coremetadata.Agent{Spec: coremetadata.AgentSpec{Provider: aiModeCodex}, Status: coremetadata.AgentStatus{PaneRef: "pane-1"}}
	lookup := func(reason string) codexLifecycleAuthorityLookup {
		return func(paneUID string) codexLifecycleAuthorityDiagnostic {
			if paneUID != "pane-1" {
				t.Fatalf("lookup pane = %q", paneUID)
			}
			return codexLifecycleAuthorityDiagnostic{Source: codexAuthorityHook, Reason: reason}
		}
	}
	reason := coremetadata.CodexCompositeAuthorityUnavailableReason
	got := explainCodexRouteReason(lookup("drain-required"), agent, reason)
	if !strings.HasPrefix(got, reason+"; lifecycle provider-hook drain-required: ") || !strings.Contains(got, "draining after a projmux install") {
		t.Fatalf("drain explanation = %q", got)
	}
	if got := explainCodexRouteReason(lookup("sink-error"), agent, reason); !strings.Contains(got, "sink-error: the lifecycle observer could not write projmux state") {
		t.Fatalf("sink-error explanation = %q", got)
	}
	if got := explainCodexRouteReason(lookup("no active native epoch"), agent, reason); got != reason+"; lifecycle provider-hook no active native epoch" {
		t.Fatalf("unexplained reason = %q", got)
	}
	if got := explainCodexRouteReason(lookup("drain-required"), agent, "no current Running Agent activation"); got != "no current Running Agent activation" {
		t.Fatalf("other refusal was rewritten: %q", got)
	}
}

type argvTmuxRunner struct {
	mu    sync.Mutex
	calls [][]string
	out   string
}

func (r *argvTmuxRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string{name}, args...))
	r.mu.Unlock()
	return []byte(r.out), nil
}

func TestRoutedCodexLifecycleAuthorityLookupUsesProjectSocketOutsideTmux(t *testing.T) {
	registry := resourceFixtureRegistry(t)
	project, _ := registry.Project("prj-alpha")
	project.Status.Session.SocketPath = "/tmp/pmx-exact/projmux"
	load := func() (coremetadata.Registry, error) { return registry, nil }
	row := "pan-alpha-codex\x1fprovider-control-plane\x1f123-1\x1fready"
	env := func(tmux string) func(string) string {
		return func(key string) string {
			if key == "TMUX" {
				return tmux
			}
			return ""
		}
	}

	outside := &argvTmuxRunner{out: row}
	diagnostic := routedCodexLifecycleAuthorityLookup(env(""), load, outside)("pan-alpha-codex")
	if len(outside.calls) != 1 || !slices.Contains(outside.calls[0], "-S") || !slices.Contains(outside.calls[0], "/tmp/pmx-exact/projmux") {
		t.Fatalf("outside-tmux lookup was not routed to the Project session socket: %#v", outside.calls)
	}
	if diagnostic.Source != codexAuthorityControlPlane || diagnostic.Reason != "ready" {
		t.Fatalf("outside-tmux diagnostic = %+v", diagnostic)
	}

	inside := &argvTmuxRunner{out: row}
	routedCodexLifecycleAuthorityLookup(env("/tmp/attached,1,0"), load, inside)("pan-alpha-codex")
	if len(inside.calls) != 1 || slices.Contains(inside.calls[0], "-S") {
		t.Fatalf("attached reader must observe its own server: %#v", inside.calls)
	}

	project.Status.Session.SocketPath = ""
	unrouted := &argvTmuxRunner{out: row}
	diagnostic = routedCodexLifecycleAuthorityLookup(env(""), load, unrouted)("pan-alpha-codex")
	if len(unrouted.calls) != 0 || diagnostic.Source != "unavailable" || diagnostic.Reason != "no exact tmux route" {
		t.Fatalf("unrouted lookup calls=%#v diagnostic=%+v", unrouted.calls, diagnostic)
	}
}

type paneOptionTmuxRunner struct {
	mu      sync.Mutex
	options map[string]string
}

func (r *paneOptionTmuxRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case name == "tmux" && len(args) == 5 && args[0] == "show-options" && args[1] == "-pqv":
		return []byte(r.options[args[4]] + "\n"), nil
	case name == "tmux" && len(args) == 6 && args[0] == "set-option" && args[1] == "-p" && args[2] == "-u":
		delete(r.options, args[5])
		return nil, nil
	case name == "tmux" && len(args) == 6 && args[0] == "set-option" && args[1] == "-p":
		r.options[args[4]] = args[5]
		return nil, nil
	}
	return nil, fmt.Errorf("unexpected tmux call %q", args)
}

// TestCodexObserverSlowStartupFallsBackOnlyFromPending pins the creator side
// of a detached slow start: hook observation replaces the creator's own
// pending authority, and never an epoch the observer committed meanwhile.
func TestCodexObserverSlowStartupFallsBackOnlyFromPending(t *testing.T) {
	store := newFakeResourceStore(t)
	mutator := store.mutator()
	if _, err := mutator.RecordPaneActivation(&store.registry, "pan-alpha-codex", coremetadata.PaneActivationOptions{
		Generation: "generation-1", RuntimeID: "%7", AgentUID: "agt-alpha-codex", OperationID: "slow-start-test",
	}); err != nil {
		t.Fatal(err)
	}
	bindNativeCodexTestFixture(t, store, mutator, coremetadata.CodexActivationObservation{
		AgentUID: "agt-alpha-codex", PaneUID: "pan-alpha-codex", Generation: "generation-1", ThreadID: "thread-1",
	})
	cmd := testAICommand(t.TempDir())
	cmd.loadRegistry = store.store().load
	cmd.acquireCodexAuthority = func(string) (func(), error) { return func() {}, nil }
	runner := &paneOptionTmuxRunner{options: map[string]string{
		"@projmux_pane_uid":        "pan-alpha-codex",
		aiPaneCodexAuthorityOption: codexAuthorityPending,
		aiPaneCodexReasonOption:    string(codexObserverReasonConnecting),
	}}
	sink := aiCodexLifecycleSink{command: cmd, runner: runner}
	identity := codexLifecycleIdentity{AgentUID: "agt-alpha-codex", PaneUID: "pan-alpha-codex", RuntimeID: "%7", Generation: "generation-1", ThreadID: "thread-1"}
	if !sink.BindingCurrent(identity) {
		t.Fatal("fixture binding is not current")
	}
	slow := codexObserverStartupResult{Status: codexObserverStartupRetrying, Reason: string(codexObserverReasonObserverTimeout)}

	if got := convergeCodexObserverSlowStartup(sink, identity, slow); got != slow {
		t.Fatalf("slow startup result = %+v", got)
	}
	if runner.options[aiPaneCodexAuthorityOption] != codexAuthorityHook || runner.options[aiPaneCodexReasonOption] != "observer-timeout" {
		t.Fatalf("pending was not replaced by hook observation: %#v", runner.options)
	}

	runner.options[aiPaneCodexAuthorityOption] = codexAuthorityControlPlane
	runner.options[aiPaneCodexEpochOption] = "4242-1"
	runner.options[aiPaneCodexReasonOption] = "ready"
	convergeCodexObserverSlowStartup(sink, identity, slow)
	if runner.options[aiPaneCodexAuthorityOption] != codexAuthorityControlPlane || runner.options[aiPaneCodexEpochOption] != "4242-1" {
		t.Fatalf("creator fallback overwrote a committed epoch: %#v", runner.options)
	}
}

type progressLockTimeoutSink struct {
	*recordingCodexLifecycleSink
}

func (s *progressLockTimeoutSink) ApplyProgress(_ codexLifecycleIdentity, progress coremetadata.AgentProgress, _ agentprogress.Diagnostics) error {
	if progress.TurnRef != "" {
		return fmt.Errorf("progress write: %w", intmetadata.ErrLockTimeout)
	}
	return nil
}

// TestCodexNativeObserverReplacementReadyThenProgressFailureInvalidates pins
// the epoch that published ready and then failed its first progress write.
// Its control endpoint is closed, so the Pane must leave ready during
// recovery too, instead of the replacement being discarded silently.
func TestCodexNativeObserverReplacementReadyThenProgressFailureInvalidates(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	conn := &fakeCodexLifecycleConnection{
		snapshot: codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateActive, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress},
		events:   make(chan codexappserver.Notification),
	}
	sink := &progressLockTimeoutSink{newRecordingCodexLifecycleSink()}
	sink.failApplyAt = 1
	waits := 0
	observer := codexNativeObserver{identity: identity, sink: sink,
		open: func(context.Context) (codexLifecycleConnection, error) { return conn, nil },
		waitRecovery: func(context.Context, time.Duration) bool {
			waits++
			return waits < 2
		},
	}
	if err := observer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if waits != 2 {
		t.Fatalf("observer did not reach the replacement progress failure: waits=%d", waits)
	}
	authorities := sink.authoritySnapshot()
	if !slices.Contains(authorities, codexAuthorityControlPlane+":ready") {
		t.Fatalf("replacement never published ready: %#v", authorities)
	}
	if got := authorities[len(authorities)-1]; got != codexAuthorityInvalidating+":sink-error" {
		t.Fatalf("replacement progress failure left authority %q, want invalidating:sink-error (%#v)", got, authorities)
	}
}

type invalidatingLockTimeoutSink struct {
	*recordingCodexLifecycleSink
	mu       sync.Mutex
	failures int
}

func (s *invalidatingLockTimeoutSink) SetAuthority(identity codexLifecycleIdentity, source, epoch, reason string) error {
	s.mu.Lock()
	fail := source == codexAuthorityInvalidating && s.failures > 0
	if fail {
		s.failures--
	}
	s.mu.Unlock()
	if fail {
		return fmt.Errorf("set authority: %w", intmetadata.ErrLockTimeout)
	}
	return s.recordingCodexLifecycleSink.SetAuthority(identity, source, epoch, reason)
}

// TestCodexNativeObserverOwedInvalidationLandsBeforeRetry pins a ready epoch
// whose write failed and whose invalidating write then failed too. The Pane
// must not keep saying ready: recovery retries the owed invalidation before
// it waits for the next attempt.
func TestCodexNativeObserverOwedInvalidationLandsBeforeRetry(t *testing.T) {
	identity := testCodexLifecycleIdentity()
	conn := &fakeCodexLifecycleConnection{
		snapshot: codexappserver.LifecycleSnapshot{ThreadID: identity.ThreadID, ThreadState: codexappserver.ThreadStateActive, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress},
		events:   make(chan codexappserver.Notification, 1),
	}
	conn.events <- codexappserver.Notification{Method: "thread/status/changed", Params: []byte(`{"threadId":"thread-1","status":{"type":"idle"}}`)}
	sink := &invalidatingLockTimeoutSink{recordingCodexLifecycleSink: newRecordingCodexLifecycleSink(), failures: 1}
	sink.failApplyAt = 2
	observer := codexNativeObserver{identity: identity, sink: sink,
		open:         func(context.Context) (codexLifecycleConnection, error) { return conn, nil },
		waitRecovery: func(context.Context, time.Duration) bool { return false },
	}
	if err := observer.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	authorities := sink.authoritySnapshot()
	want := []string{codexAuthorityControlPlane + ":ready", codexAuthorityInvalidating + ":sink-error"}
	if !slices.Equal(authorities, want) {
		t.Fatalf("authorities = %#v, want %#v", authorities, want)
	}
}
