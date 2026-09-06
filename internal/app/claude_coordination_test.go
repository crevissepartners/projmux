package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/agentdelivery"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

func coordinationEnvelope(ref string, deadline time.Time) claudeCoordinationEnvelope {
	return claudeCoordinationEnvelope{
		Version: claudeCoordinationVersion, MessageRef: ref,
		Source:  claudeCoordinationSource{Kind: "peer", Trust: "untrusted", Authority: "coordination-only"},
		Payload: "/status --plain-text", Deadline: deadline,
	}
}

func newLiveCoordinationTestHub() *claudeCoordinationHub {
	hub := newClaudeCoordinationHub()
	hub.processCurrent = func(coremetadata.ProcessIdentity) bool { return true }
	hub.assignmentWindow = time.Hour
	hub.receiptWindow = time.Hour
	return hub
}

func TestClaudeCoordinationHubTransitionsAreTerminalOnce(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	process := coremetadata.ProcessIdentity{PID: 101, OwnerUID: 1000, Start: "test:hook-1"}

	t.Run("held handoff exact receipt and no automatic resend", func(t *testing.T) {
		hub := newLiveCoordinationTestHub()
		hub.now = func() time.Time { return now }
		envelope := coordinationEnvelope("message-delivered", now.Add(time.Hour))
		if got := hub.submit(envelope); got.State != agentdelivery.StateHeld {
			t.Fatalf("submit state = %s, want held", got.State)
		}
		waiter, _ := hub.arm(process)
		response := <-waiter.result
		if response.Kind != "assignment" || response.Envelope == nil || response.Envelope.MessageRef != envelope.MessageRef {
			t.Fatalf("handoff response = %+v", response)
		}
		if got := hub.beginHandoff(envelope.MessageRef, waiter.ref, process); got.State != agentdelivery.StateHandoff {
			t.Fatalf("begin handoff = %+v", got)
		}
		if got := hub.receipt(envelope.MessageRef, "foreign-waiter", process, true); got.State != agentdelivery.StateHandoff {
			t.Fatalf("foreign receipt state = %s, want handoff", got.State)
		}
		if got := hub.receipt(envelope.MessageRef, waiter.ref, process, true); got.State != agentdelivery.StateDelivered || got.Ambiguous {
			t.Fatalf("delivery = %+v", got)
		}
		if got := hub.submit(envelope); got.State != agentdelivery.StateDelivered {
			t.Fatalf("duplicate submit changed terminal delivery: %+v", got)
		}
		second, _ := hub.arm(process)
		select {
		case got := <-second.result:
			t.Fatalf("terminal message was automatically resent: %+v", got)
		default:
		}
		hub.cancelWaiter(second, "test-cleanup")
	})

	t.Run("partial pipe receipt is ambiguous failure", func(t *testing.T) {
		hub := newLiveCoordinationTestHub()
		hub.now = func() time.Time { return now }
		waiter, _ := hub.arm(process)
		envelope := coordinationEnvelope("message-partial", now.Add(time.Hour))
		hub.submit(envelope)
		<-waiter.result
		hub.beginHandoff(envelope.MessageRef, waiter.ref, process)
		got := hub.receipt(envelope.MessageRef, waiter.ref, process, false)
		if got.State != agentdelivery.StateFailed || !got.Ambiguous || got.Reason != "provider-pipe-write-failed" {
			t.Fatalf("partial receipt = %+v", got)
		}
		second, _ := hub.arm(process)
		select {
		case response := <-second.result:
			t.Fatalf("ambiguous post-handoff failure was automatically resent: %+v", response)
		default:
		}
		hub.cancelWaiter(second, "test-cleanup")
	})

	t.Run("receipt timeout is terminal ambiguous failure", func(t *testing.T) {
		hub := newLiveCoordinationTestHub()
		hub.now = func() time.Time { return now }
		waiter, _ := hub.arm(process)
		envelope := coordinationEnvelope("message-timeout", now.Add(time.Hour))
		hub.submit(envelope)
		<-waiter.result
		hub.beginHandoff(envelope.MessageRef, waiter.ref, process)
		hub.expireHandoff(envelope.MessageRef, waiter.ref)
		got := hub.status(envelope.MessageRef)
		if got.State != agentdelivery.StateFailed || !got.Ambiguous || got.Reason != "observation-timeout" {
			t.Fatalf("receipt timeout = %+v", got)
		}
	})

	t.Run("ttl before handoff expires without ambiguity", func(t *testing.T) {
		hub := newLiveCoordinationTestHub()
		hub.now = func() time.Time { return now }
		envelope := coordinationEnvelope("message-ttl", now.Add(time.Minute))
		hub.submit(envelope)
		now = now.Add(2 * time.Minute)
		got := hub.status(envelope.MessageRef)
		if got.State != agentdelivery.StateExpired || got.Ambiguous {
			t.Fatalf("ttl result = %+v", got)
		}
		now = now.Add(-2 * time.Minute)
	})

	t.Run("assigned waiter cannot begin handoff after ttl", func(t *testing.T) {
		testNow := now
		hub := newLiveCoordinationTestHub()
		hub.now = func() time.Time { return testNow }
		waiter, _ := hub.arm(process)
		envelope := coordinationEnvelope("message-assigned-ttl", testNow.Add(time.Minute))
		if got := hub.submit(envelope); got.State != agentdelivery.StateQueued {
			t.Fatalf("assigned submit = %+v", got)
		}
		if response := <-waiter.result; response.Kind != "assignment" || response.WaiterRef != waiter.ref {
			t.Fatalf("assigned waiter response = %+v", response)
		}
		testNow = envelope.Deadline
		got := hub.status(envelope.MessageRef)
		if got.State != agentdelivery.StateExpired || got.Ambiguous || got.WaiterRef != "" || got.Reason != "ttl" {
			t.Fatalf("post-deadline status = %+v", got)
		}
		if handoff := hub.beginHandoff(envelope.MessageRef, waiter.ref, process); handoff != got {
			t.Fatalf("late handoff changed terminal expiry: got %+v want %+v", handoff, got)
		}
		if receipt := hub.receipt(envelope.MessageRef, waiter.ref, process, true); receipt != got {
			t.Fatalf("late receipt changed terminal expiry: got %+v want %+v", receipt, got)
		}
		second, _ := hub.arm(process)
		select {
		case response := <-second.result:
			t.Fatalf("expired assignment was automatically resent: %+v", response)
		default:
		}
		hub.cancelWaiter(second, "test-cleanup")
	})

	t.Run("handoff deadline fence does not depend on status or timer", func(t *testing.T) {
		testNow := now
		hub := newLiveCoordinationTestHub()
		hub.now = func() time.Time { return testNow }
		waiter, _ := hub.arm(process)
		envelope := coordinationEnvelope("message-direct-handoff-ttl", testNow.Add(time.Minute))
		hub.submit(envelope)
		<-waiter.result
		testNow = envelope.Deadline
		got := hub.beginHandoff(envelope.MessageRef, waiter.ref, process)
		if got.State != agentdelivery.StateExpired || got.Ambiguous || got.WaiterRef != "" || got.Reason != "ttl" {
			t.Fatalf("post-deadline handoff = %+v", got)
		}
	})

	t.Run("helper restart distinguishes held from handoff", func(t *testing.T) {
		heldHub := newLiveCoordinationTestHub()
		heldHub.now = func() time.Time { return now }
		heldHub.submit(coordinationEnvelope("message-held-restart", now.Add(time.Hour)))
		heldHub.close()
		if got := heldHub.status("message-held-restart"); got.State != agentdelivery.StateStale || got.Ambiguous {
			t.Fatalf("held restart = %+v", got)
		}

		handoffHub := newLiveCoordinationTestHub()
		handoffHub.now = func() time.Time { return now }
		waiter, _ := handoffHub.arm(process)
		handoffHub.submit(coordinationEnvelope("message-handoff-restart", now.Add(time.Hour)))
		<-waiter.result
		handoffHub.beginHandoff("message-handoff-restart", waiter.ref, process)
		handoffHub.close()
		if got := handoffHub.status("message-handoff-restart"); got.State != agentdelivery.StateFailed || !got.Ambiguous {
			t.Fatalf("handoff restart = %+v", got)
		}
	})
}

func TestClaudeCoordinationSingleWaiterCASAndSupersede(t *testing.T) {
	hub := newLiveCoordinationTestHub()
	first, _ := hub.arm(coremetadata.ProcessIdentity{PID: 101, OwnerUID: 1000, Start: "test:first"})
	second, superseded := hub.arm(coremetadata.ProcessIdentity{PID: 102, OwnerUID: 1000, Start: "test:second"})
	if superseded != first {
		t.Fatal("second waiter did not CAS-supersede first")
	}
	if got := <-first.result; got.Kind != "superseded" || got.WaiterRef != first.ref {
		t.Fatalf("first waiter result = %+v", got)
	}
	envelope := coordinationEnvelope("message-single-waiter", time.Now().Add(time.Hour))
	if got := hub.submit(envelope); got.State != agentdelivery.StateQueued || got.WaiterRef != "" {
		t.Fatalf("submit chose wrong waiter: %+v", got)
	}
	if got := <-second.result; got.Kind != "assignment" || got.WaiterRef != second.ref {
		t.Fatalf("second waiter result = %+v", got)
	}
	if got := hub.beginHandoff(envelope.MessageRef, second.ref, second.process); got.State != agentdelivery.StateHandoff || got.WaiterRef != second.ref {
		t.Fatalf("second waiter did not begin handoff: %+v", got)
	}
}

func TestClaudeCoordinationOverlappingHookChildrenLeaveOneActiveWaiter(t *testing.T) {
	hub := newLiveCoordinationTestHub()
	const count = 32
	waiters := make(chan *claudeCoordinationWaiter, count)
	var group sync.WaitGroup
	for index := range count {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			waiter, _ := hub.arm(coremetadata.ProcessIdentity{PID: index + 1, OwnerUID: 1000, Start: "test:overlap"})
			waiters <- waiter
		}(index)
	}
	group.Wait()
	close(waiters)
	hub.mu.Lock()
	active := hub.waiter
	hub.mu.Unlock()
	if active == nil {
		t.Fatal("overlapping hook children left no active waiter")
	}
	superseded := 0
	for waiter := range waiters {
		if waiter == active {
			continue
		}
		select {
		case result := <-waiter.result:
			if result.Kind != "superseded" || result.WaiterRef != waiter.ref {
				t.Fatalf("superseded result = %+v", result)
			}
			superseded++
		default:
			t.Fatalf("inactive waiter %s did not receive supersede", waiter.ref)
		}
	}
	if superseded != count-1 {
		t.Fatalf("superseded = %d, want %d", superseded, count-1)
	}
	hub.cancelWaiter(active, "test-cleanup")
}

func TestClaudeCoordinationDeadHookChildNeverBeginsHandoff(t *testing.T) {
	hub := newLiveCoordinationTestHub()
	hub.processCurrent = func(coremetadata.ProcessIdentity) bool { return false }
	process := coremetadata.ProcessIdentity{PID: 404, OwnerUID: 1000, Start: "test:dead-hook"}
	waiter, _ := hub.arm(process)
	delivery := hub.submit(coordinationEnvelope("message-dead-hook", time.Now().Add(time.Hour)))
	if delivery.State != agentdelivery.StateHeld || delivery.WaiterRef != "" || delivery.Ambiguous {
		t.Fatalf("dead hook delivery = %+v, want held before handoff", delivery)
	}
	if result := <-waiter.result; result.Kind != "stale" || result.WaiterRef != waiter.ref {
		t.Fatalf("dead hook waiter result = %+v", result)
	}
	if hub.hasWaiter() {
		t.Fatal("dead hook remained the active waiter")
	}
	hub.close()
}

func TestClaudeCoordinationDisconnectedHookResponseFailsBeforeHandoff(t *testing.T) {
	hub := newLiveCoordinationTestHub()
	process := coremetadata.ProcessIdentity{PID: 101, OwnerUID: 1000, Start: "test:disconnected-hook"}
	waiter, _ := hub.arm(process)
	envelope := coordinationEnvelope("message-disconnected-hook", time.Now().Add(time.Hour))
	if delivery := hub.submit(envelope); delivery.State != agentdelivery.StateQueued || delivery.WaiterRef != "" {
		t.Fatalf("disconnected assignment leaked handoff: %+v", delivery)
	}
	response := <-waiter.result
	server := &claudeCoordinationServer{hub: hub}
	if err := server.writeWaiterResponse(errorWriter{err: io.ErrClosedPipe}, response, process); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("forced response-write error = %v", err)
	}
	status := hub.status(envelope.MessageRef)
	if status.State != agentdelivery.StateFailed || status.Ambiguous || status.WaiterRef != "" || status.Reason != "helper-response-write-failed" {
		t.Fatalf("disconnected hook terminal = %+v", status)
	}
}

func TestClaudeCoordinationAssignmentTimeoutFailsBeforeHandoff(t *testing.T) {
	hub := newLiveCoordinationTestHub()
	process := coremetadata.ProcessIdentity{PID: 102, OwnerUID: 1000, Start: "test:assignment-timeout"}
	waiter, _ := hub.arm(process)
	envelope := coordinationEnvelope("message-assignment-timeout", time.Now().Add(time.Hour))
	if delivery := hub.submit(envelope); delivery.State != agentdelivery.StateQueued || delivery.WaiterRef != "" {
		t.Fatalf("assignment leaked handoff: %+v", delivery)
	}
	response := <-waiter.result
	if response.Kind != "assignment" || response.WaiterRef != waiter.ref {
		t.Fatalf("assignment response = %+v", response)
	}
	hub.expireAssignment(envelope.MessageRef, waiter.ref)
	status := hub.status(envelope.MessageRef)
	if status.State != agentdelivery.StateFailed || status.Ambiguous || status.WaiterRef != "" || status.Reason != "waiter-disconnected" {
		t.Fatalf("assignment timeout terminal = %+v", status)
	}
}

type errorWriter struct{ err error }

func (w errorWriter) Write([]byte) (int, error) { return 0, w.err }

type partialPipeWriter struct {
	written int
	err     error
}

func (w *partialPipeWriter) Write(payload []byte) (int, error) {
	if w.written != 0 {
		return 0, w.err
	}
	n := len(payload) / 2
	if n == 0 {
		n = 1
	}
	w.written = n
	return n, w.err
}

func TestWriteFullProviderFrameHandlesPartialAndEPIPE(t *testing.T) {
	payload := []byte("bounded provider frame\n")
	var chunked bytes.Buffer
	if err := writeFullProviderFrame(writerLimitedTo{writer: &chunked, limit: 3}, payload); err != nil || !bytes.Equal(chunked.Bytes(), payload) {
		t.Fatalf("chunked full write bytes=%q err=%v", chunked.Bytes(), err)
	}
	partial := &partialPipeWriter{err: io.ErrClosedPipe}
	if err := writeFullProviderFrame(partial, payload); !errors.Is(err, io.ErrClosedPipe) || partial.written == len(payload) {
		t.Fatalf("partial EPIPE written=%d err=%v", partial.written, err)
	}
	if err := writeFullProviderFrame(zeroWriter{}, payload); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("zero write err=%v, want short write", err)
	}
}

type writerLimitedTo struct {
	writer io.Writer
	limit  int
}

func (w writerLimitedTo) Write(payload []byte) (int, error) {
	if len(payload) > w.limit {
		payload = payload[:w.limit]
	}
	return w.writer.Write(payload)
}

type zeroWriter struct{}

func (zeroWriter) Write([]byte) (int, error) { return 0, nil }

type claudeCoordinationTestFixture struct {
	registryPath string
	route        coremetadata.AgentRouteRef
	target       claudeCoordinationTarget
	server       *claudeCoordinationServer
	sessionID    string
	paneUID      string
	generation   string
}

func newClaudeCoordinationTestFixture(t *testing.T) *claudeCoordinationTestFixture {
	t.Helper()
	h := newSessionRefHarness(t, aiModeClaude)
	provider, _, err := localipc.Process(os.Getppid())
	if err != nil {
		t.Fatal(err)
	}
	helper, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	mutator := intmetadata.DefaultMutator()
	if err := mutator.RecordClaudeProcess(h.registry, h.paneUID, h.agentUID, h.envGeneration, provider); err != nil {
		t.Fatal(err)
	}
	authority := coremetadata.ClaudeAuthorityRef{SessionID: "synthetic-session", Process: provider,
		RegistrationGeneration: "synthetic-registration", LeaseProcess: helper}
	if err := mutator.BeginClaudeRegistration(h.registry, h.paneUID, h.agentUID, h.envGeneration, authority); err != nil {
		t.Fatal(err)
	}
	if err := mutator.RecordClaudeRegistration(h.registry, h.paneUID, h.agentUID, h.envGeneration,
		coremetadata.ClaudeRegistration{Authority: authority}); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("", "pmx-coordination-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	registryPath := intmetadata.PathFor(filepath.Join(root, "state"))
	store := intmetadata.NewStore(registryPath)
	if _, err := store.Update(func(reg *coremetadata.Registry) error { *reg = h.registry.Clone(); return nil }); err != nil {
		t.Fatal(err)
	}
	route, reason := coremetadata.ResolveAgentRoute(*h.registry, h.agentUID)
	if reason != "" {
		t.Fatal(reason)
	}
	target, ok := claudeTargetForRoute(route)
	if !ok {
		t.Fatal("exact Claude target unavailable")
	}
	listener, err := localipc.Listen(claudeCoordinationSocket(registryPath, target))
	if err != nil {
		t.Fatal(err)
	}
	server := startClaudeCoordinationServer(listener, route, func() bool { return true })
	t.Cleanup(func() {
		server.Close()
		_ = os.RemoveAll(claudeActivationLeaseDir(registryPath, h.paneUID, h.envGeneration))
	})
	return &claudeCoordinationTestFixture{registryPath: registryPath, route: route, target: target, server: server,
		sessionID: authority.SessionID, paneUID: h.paneUID, generation: h.envGeneration}
}

func (f *claudeCoordinationTestFixture) call(t *testing.T, request claudeCoordinationRequest) claudeCoordinationResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := callClaudeCoordination(ctx, f.registryPath, f.route, request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestClaudeCoordinationHookOwnedUDSAndProviderPipe(t *testing.T) {
	for _, event := range []string{"SessionStart", "Stop"} {
		t.Run(event, func(t *testing.T) {
			fixture := newClaudeCoordinationTestFixture(t)
			getenv := func(key string) string {
				switch key {
				case internalClaudeRegistryPathEnv:
					return fixture.registryPath
				case internalActivationPaneUIDEnv:
					return fixture.paneUID
				case internalActivationGenerationEnv:
					return fixture.generation
				default:
					return ""
				}
			}
			input, _ := json.Marshal(map[string]string{"hook_event_name": event, "session_id": fixture.sessionID})
			readPipe, writePipe, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer readPipe.Close()
			output := make(chan []byte, 1)
			go func() {
				data, _ := io.ReadAll(readPipe)
				output <- data
			}()
			hookDone := make(chan error, 1)
			go func() {
				hookDone <- runClaudeMessageWaitInput(nil, bytes.NewReader(input), writePipe, getenv)
				_ = writePipe.Close()
			}()
			select {
			case <-fixture.server.hub.waiterReady:
			case <-time.After(3 * time.Second):
				t.Fatal("hook waiter-ready barrier timed out")
			}
			envelope := coordinationEnvelope("message-"+strings.ToLower(event), time.Now().Add(time.Minute))
			envelope.Target = fixture.target
			response := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion, Operation: "submit",
				Target: fixture.target, Envelope: &envelope})
			if response.Delivery.State != agentdelivery.StateQueued {
				t.Fatalf("submit response = %+v", response)
			}
			var hookErr error
			select {
			case hookErr = <-hookDone:
			case <-time.After(3 * time.Second):
				t.Fatal("hook handoff timed out")
			}
			var exitCode interface{ ExitCode() int }
			if !errors.As(hookErr, &exitCode) || exitCode.ExitCode() != 2 {
				t.Fatalf("hook error = %v, want typed exit 2", hookErr)
			}
			providerFrame := <-output
			if len(providerFrame) == 0 || providerFrame[len(providerFrame)-1] != '\n' {
				t.Fatalf("provider frame = %q", providerFrame)
			}
			var got claudeCoordinationEnvelope
			if err := json.Unmarshal(providerFrame, &got); err != nil || got.MessageRef != envelope.MessageRef || got.Payload != "/status --plain-text" || got.Source.Trust != "untrusted" {
				t.Fatalf("provider frame = %+v err=%v", got, err)
			}
			status := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion, Operation: "status",
				Target: fixture.target, MessageRef: envelope.MessageRef})
			if status.Delivery.State != agentdelivery.StateDelivered || status.Delivery.Reason != "provider-pipe-full-frame" {
				t.Fatalf("structured receipt = %+v", status.Delivery)
			}
		})
	}
}

func TestClaudeCoordinationRefusesHookIdentityMismatches(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	for _, test := range []struct {
		name    string
		event   string
		session string
	}{
		{name: "foreign event", event: "Notification", session: fixture.sessionID},
		{name: "foreign session", event: "Stop", session: "other-session"},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := fixture.call(t, claudeCoordinationRequest{Version: claudeCoordinationVersion, Operation: "wait",
				Target: fixture.target, HookEvent: test.event, SessionID: test.session, WaitUntil: time.Now()})
			if response.Kind != "refused" {
				t.Fatalf("mismatched hook response = %+v", response)
			}
		})
	}
	staleTarget := fixture.target
	staleTarget.Generation += "-replacement"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := callClaudeCoordination(ctx, fixture.registryPath, fixture.route, claudeCoordinationRequest{
		Version: claudeCoordinationVersion, Operation: "probe", Target: staleTarget}); err == nil {
		t.Fatal("stale activation target accepted")
	}
}

func TestClaudeCoordinationEnvelopeBoundsAndPlainTextAuthority(t *testing.T) {
	fixture := newClaudeCoordinationTestFixture(t)
	envelope := coordinationEnvelope("message-bounds", time.Now().Add(time.Minute))
	envelope.Target = fixture.target
	if !envelope.valid(time.Now(), fixture.route) {
		t.Fatal("bounded exact envelope refused")
	}
	envelope.Payload = strings.Repeat("x", claudeProviderFrameMaxBytes)
	if envelope.valid(time.Now(), fixture.route) {
		t.Fatal("oversized provider frame accepted")
	}
	envelope = coordinationEnvelope("message-command", time.Now().Add(time.Minute))
	envelope.Target = fixture.target
	envelope.Payload = "/dangerous-looking-command --still-plain-text"
	data, err := json.Marshal(envelope)
	if err != nil || !bytes.Contains(data, []byte(`/dangerous-looking-command`)) || envelope.Source.Authority != "coordination-only" {
		t.Fatalf("slash command was not preserved as untrusted plaintext: %s", data)
	}
}

func TestClaudeCoordinationHookRouteIsHiddenQuietAndShellSafe(t *testing.T) {
	if shouldRunLegacyHookMigrations([]string{"internal", "claude-message-wait"}) {
		t.Fatal("private hook route would write migration diagnostics to the provider pipe")
	}
	for _, command := range internalSubcommands {
		if command == "claude-message-wait" {
			t.Fatal("private hook route was exposed in the public internal catalog")
		}
	}
	if claudeCoordinationHookCommand != "exec projmux internal claude-message-wait # "+claudeCoordinationManagedMarker {
		t.Fatalf("hook command is not the fixed shell-safe exec route: %q", claudeCoordinationHookCommand)
	}
	for _, input := range []string{"{", `{"hook_event_name":"Notification","session_id":"foreign"}`} {
		var providerPipe bytes.Buffer
		if err := runClaudeMessageWaitInput(nil, strings.NewReader(input), &providerPipe, func(string) string { return "" }); err != nil || providerPipe.Len() != 0 {
			t.Fatalf("invalid hook input err=%v output=%q, want quiet refusal", err, providerPipe.String())
		}
	}
}
