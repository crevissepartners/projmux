package app

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/crevissepartners/projmux/internal/i18n"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
)

type fakeExactControlWire struct {
	mu          sync.Mutex
	threadID    string
	turnID      string
	snapshot    codexappserver.LifecycleSnapshot
	snapshotErr error
	reads       int
	start       int
	steer       int
	interrupt   int
	responses   []json.RawMessage
	results     []any
	operations  []string
	err         error
}

func (w *fakeExactControlWire) ReadLifecycleSnapshot(_ context.Context, threadID string) (codexappserver.LifecycleSnapshot, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.reads++
	w.operations = append(w.operations, "lifecycle-read:"+threadID)
	if w.snapshot != (codexappserver.LifecycleSnapshot{}) {
		return w.snapshot, w.snapshotErr
	}
	return codexappserver.LifecycleSnapshot{ThreadID: w.resultThreadID(), ThreadState: codexappserver.ThreadStateIdle}, w.snapshotErr
}
func (w *fakeExactControlWire) StartExactTurn(_ context.Context, threadID, _ string) (codexappserver.ControlResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.start++
	w.operations = append(w.operations, "turn-start:"+threadID)
	return codexappserver.ControlResult{ThreadID: w.resultThreadID(), TurnID: "turn-new"}, w.err
}
func (w *fakeExactControlWire) SteerExactTurn(_ context.Context, threadID, turnID, _ string) (codexappserver.ControlResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.steer++
	w.operations = append(w.operations, "turn-steer:"+threadID+":"+turnID)
	return codexappserver.ControlResult{ThreadID: w.resultThreadID(), TurnID: w.resultTurnID()}, w.err
}
func (w *fakeExactControlWire) InterruptExactTurn(_ context.Context, threadID, turnID string) (codexappserver.ControlResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.interrupt++
	w.operations = append(w.operations, "turn-interrupt:"+threadID+":"+turnID)
	return codexappserver.ControlResult{ThreadID: w.resultThreadID(), TurnID: w.resultTurnID()}, w.err
}
func (w *fakeExactControlWire) RespondServerRequest(_ context.Context, id json.RawMessage, result any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.responses = append(w.responses, append(json.RawMessage(nil), id...))
	w.results = append(w.results, result)
	w.operations = append(w.operations, "approval-response")
	return w.err
}
func (w *fakeExactControlWire) writes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.start + w.steer + w.interrupt + len(w.responses)
}

func (w *fakeExactControlWire) resultThreadID() string {
	if w.threadID == "" {
		return "thread-1"
	}
	return w.threadID
}

func (w *fakeExactControlWire) resultTurnID() string {
	if w.turnID == "" {
		return "turn-1"
	}
	return w.turnID
}

func phase6Identity() codexLifecycleIdentity {
	return codexLifecycleIdentity{AgentUID: "agent-1", PaneUID: "pane-1", RuntimeID: "%7", Generation: "generation-1", ThreadID: "thread-1"}
}

func TestExactAgentControlActionGenerationEpochAndTurnTable(t *testing.T) {
	identity := phase6Identity()
	for _, test := range []struct {
		name       string
		snapshot   codexappserver.LifecycleSnapshot
		request    agentControlRequest
		current    bool
		wantOK     bool
		wantReads  int
		wantWrites int
	}{
		{name: "idle exact new turn", snapshot: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle, TurnID: "turn-old", TurnState: codexappserver.TurnStateCompleted}, request: agentControlRequest{Operation: agentControlOpStart, Text: "new"}, current: true, wantOK: true, wantReads: 1, wantWrites: 1},
		{name: "active exact steer", snapshot: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateActive, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress}, request: agentControlRequest{Operation: agentControlOpSteer, Text: "steer"}, current: true, wantOK: true, wantWrites: 1},
		{name: "idle steer refused", snapshot: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle, TurnID: "turn-1", TurnState: codexappserver.TurnStateCompleted}, request: agentControlRequest{Operation: agentControlOpSteer, Text: "steer"}, current: true},
		{name: "old epoch", snapshot: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle}, request: agentControlRequest{Operation: agentControlOpStart, Epoch: "old", Text: "new"}, current: true},
		{name: "old generation", snapshot: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle}, request: agentControlRequest{Operation: agentControlOpStart, Text: "new", Identity: codexLifecycleIdentity{AgentUID: "agent-1", PaneUID: "pane-1", RuntimeID: "%7", Generation: "old", ThreadID: "thread-1"}}, current: true},
		{name: "other Agent", snapshot: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle}, request: agentControlRequest{Operation: agentControlOpStart, Text: "new", Identity: codexLifecycleIdentity{AgentUID: "agent-other", PaneUID: "pane-1", RuntimeID: "%7", Generation: "generation-1", ThreadID: "thread-1"}}, current: true},
		{name: "other Pane", snapshot: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle}, request: agentControlRequest{Operation: agentControlOpStart, Text: "new", Identity: codexLifecycleIdentity{AgentUID: "agent-1", PaneUID: "pane-other", RuntimeID: "%7", Generation: "generation-1", ThreadID: "thread-1"}}, current: true},
		{name: "other thread", snapshot: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle}, request: agentControlRequest{Operation: agentControlOpStart, Text: "new", Identity: codexLifecycleIdentity{AgentUID: "agent-1", PaneUID: "pane-1", RuntimeID: "%7", Generation: "generation-1", ThreadID: "thread-other"}}, current: true},
		{name: "binding replaced", snapshot: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle}, request: agentControlRequest{Operation: agentControlOpStart, Text: "new"}, current: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := &fakeExactControlWire{}
			epoch := newCodexControlEpoch(wire, identity, "epoch-1", test.snapshot, func(codexLifecycleIdentity) bool { return test.current })
			request := test.request
			if request.Identity == (codexLifecycleIdentity{}) {
				request.Identity = identity
			}
			if request.Epoch == "" {
				request.Epoch = "epoch-1"
			}
			response := epoch.Handle(context.Background(), request)
			if response.OK != test.wantOK || wire.reads != test.wantReads || wire.writes() != test.wantWrites {
				t.Fatalf("response=%+v reads=%d writes=%d", response, wire.reads, wire.writes())
			}
		})
	}

	wire := &fakeExactControlWire{}
	epoch := newCodexControlEpoch(wire, identity, "epoch-1", codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateActive, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress}, func(codexLifecycleIdentity) bool { return true })
	request := agentControlRequest{Operation: agentControlOpInterrupt, Identity: identity, Epoch: "epoch-1"}
	if first := epoch.Handle(context.Background(), request); !first.OK {
		t.Fatalf("first interrupt = %+v", first)
	}
	if second := epoch.Handle(context.Background(), request); second.OK || wire.interrupt != 1 {
		t.Fatalf("second interrupt = %+v writes=%d", second, wire.interrupt)
	}
}

func TestExactAgentControlStartFreshLifecycleAdmissionTable(t *testing.T) {
	identity := phase6Identity()
	active := codexappserver.LifecycleSnapshot{
		ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateActive,
		TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress,
	}
	idle := codexappserver.LifecycleSnapshot{
		ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle,
		TurnID: "turn-old", TurnState: codexappserver.TurnStateCompleted,
	}
	for _, test := range []struct {
		name       string
		cached     codexappserver.LifecycleSnapshot
		fresh      codexappserver.LifecycleSnapshot
		freshErr   error
		wantOK     bool
		wantCode   string
		wantReads  int
		wantStarts int
		wantOrder  []string
	}{
		{name: "cached active fresh same active", cached: active, fresh: active, wantCode: "turn-in-progress", wantReads: 1, wantOrder: []string{"lifecycle-read:thread-1"}},
		{name: "cached active fresh later active", cached: active, fresh: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateActive, TurnID: "turn-later", TurnState: codexappserver.TurnStateInProgress}, wantCode: "turn-in-progress", wantReads: 1, wantOrder: []string{"lifecycle-read:thread-1"}},
		{name: "cached idle fresh later active", cached: idle, fresh: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateWaitingOnApproval, TurnID: "turn-later", TurnState: codexappserver.TurnStateInProgress}, wantCode: "turn-in-progress", wantReads: 1, wantOrder: []string{"lifecycle-read:thread-1"}},
		{name: "cached idle fresh waiting on input", cached: idle, fresh: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateWaitingOnUserInput, TurnID: "turn-later", TurnState: codexappserver.TurnStateInProgress}, wantCode: "turn-in-progress", wantReads: 1, wantOrder: []string{"lifecycle-read:thread-1"}},
		{name: "cached active fresh same terminal", cached: active, fresh: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle, TurnID: "turn-1", TurnState: codexappserver.TurnStateCompleted}, wantOK: true, wantReads: 1, wantStarts: 1, wantOrder: []string{"lifecycle-read:thread-1", "turn-start:thread-1"}},
		{name: "cached active fresh later terminal", cached: active, fresh: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle, TurnID: "turn-later", TurnState: codexappserver.TurnStateInterrupted}, wantOK: true, wantReads: 1, wantStarts: 1, wantOrder: []string{"lifecycle-read:thread-1", "turn-start:thread-1"}},
		{name: "cached idle fresh failed terminal", cached: idle, fresh: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle, TurnID: "turn-later", TurnState: codexappserver.TurnStateFailed}, wantOK: true, wantReads: 1, wantStarts: 1, wantOrder: []string{"lifecycle-read:thread-1", "turn-start:thread-1"}},
		{name: "cached active fresh never started", cached: active, fresh: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle}, wantOK: true, wantReads: 1, wantStarts: 1, wantOrder: []string{"lifecycle-read:thread-1", "turn-start:thread-1"}},
		{name: "fresh read error", cached: idle, freshErr: errors.New("private provider detail"), wantCode: "turn-state-unavailable", wantReads: 1, wantOrder: []string{"lifecycle-read:thread-1"}},
		{name: "fresh read timeout", cached: idle, freshErr: context.DeadlineExceeded, wantCode: "turn-state-unavailable", wantReads: 1, wantOrder: []string{"lifecycle-read:thread-1"}},
		{name: "fresh wrong thread", cached: idle, fresh: codexappserver.LifecycleSnapshot{ThreadID: "thread-other", ThreadState: codexappserver.ThreadStateIdle}, wantCode: "turn-state-unavailable", wantReads: 1, wantOrder: []string{"lifecycle-read:thread-1"}},
		{name: "fresh active without turn", cached: idle, fresh: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateActive, TurnState: codexappserver.TurnStateInProgress}, wantCode: "turn-state-unavailable", wantReads: 1, wantOrder: []string{"lifecycle-read:thread-1"}},
		{name: "fresh idle with active turn", cached: idle, fresh: activeWithThreadState(codexappserver.ThreadStateIdle), wantCode: "turn-state-unavailable", wantReads: 1, wantOrder: []string{"lifecycle-read:thread-1"}},
		{name: "fresh active with terminal turn", cached: idle, fresh: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateActive, TurnID: "turn-1", TurnState: codexappserver.TurnStateCompleted}, wantCode: "turn-state-unavailable", wantReads: 1, wantOrder: []string{"lifecycle-read:thread-1"}},
		{name: "fresh terminal without turn", cached: idle, fresh: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle, TurnState: codexappserver.TurnStateCompleted}, wantCode: "turn-state-unavailable", wantReads: 1, wantOrder: []string{"lifecycle-read:thread-1"}},
		{name: "fresh system error", cached: idle, fresh: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateSystemError}, wantCode: "turn-state-unavailable", wantReads: 1, wantOrder: []string{"lifecycle-read:thread-1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := &fakeExactControlWire{snapshot: test.fresh, snapshotErr: test.freshErr}
			epoch := newCodexControlEpoch(wire, identity, "epoch-1", test.cached, func(codexLifecycleIdentity) bool { return true })
			response := epoch.Handle(context.Background(), agentControlRequest{Operation: agentControlOpStart, Identity: identity, Epoch: "epoch-1", Text: "private prompt"})
			if response.OK != test.wantOK || response.Code != test.wantCode || wire.reads != test.wantReads || wire.start != test.wantStarts || !slices.Equal(wire.operations, test.wantOrder) {
				t.Fatalf("response=%+v reads=%d starts=%d order=%q", response, wire.reads, wire.start, wire.operations)
			}
			if wire.steer != 0 || wire.interrupt != 0 || len(wire.responses) != 0 || strings.Contains(response.Message, "private") || strings.Contains(strings.Join(wire.operations, " "), "private") {
				t.Fatalf("unexpected mutation or private detail: response=%+v wire=%+v", response, wire)
			}
		})
	}
}

func activeWithThreadState(state codexappserver.ThreadState) codexappserver.LifecycleSnapshot {
	return codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: state, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress}
}

func TestExactAgentControlStartRechecksBindingAfterFreshLifecycleRead(t *testing.T) {
	identity := phase6Identity()
	currentCalls := 0
	wire := &fakeExactControlWire{snapshot: codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle, TurnID: "turn-old", TurnState: codexappserver.TurnStateCompleted}}
	epoch := newCodexControlEpoch(wire, identity, "epoch-1", codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateActive, TurnID: "turn-old", TurnState: codexappserver.TurnStateInProgress}, func(codexLifecycleIdentity) bool {
		currentCalls++
		return currentCalls == 1
	})
	response := epoch.Handle(context.Background(), agentControlRequest{Operation: agentControlOpStart, Identity: identity, Epoch: "epoch-1", Text: "new"})
	if response.OK || response.Code != "stale-binding" || wire.reads != 1 || wire.writes() != 0 || !slices.Equal(wire.operations, []string{"lifecycle-read:thread-1"}) {
		t.Fatalf("response=%+v current-calls=%d reads=%d writes=%d order=%q", response, currentCalls, wire.reads, wire.writes(), wire.operations)
	}
}

func TestExactAgentControlFreshTerminalReconcileClearsCachedApproval(t *testing.T) {
	identity := phase6Identity()
	wire := &fakeExactControlWire{snapshot: codexappserver.LifecycleSnapshot{
		ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle,
		TurnID: "turn-1", TurnState: codexappserver.TurnStateCompleted,
	}}
	epoch := newCodexControlEpoch(wire, identity, "epoch-1", codexappserver.LifecycleSnapshot{
		ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateWaitingOnApproval,
		TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress,
	}, func(codexLifecycleIdentity) bool { return true })
	approval := codexappserver.Notification{
		Method: "item/commandExecution/requestApproval", RequestID: "7", RawRequestID: json.RawMessage(`7`),
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"old","cwd":"/work","availableDecisions":["accept"]}`),
	}
	if err := epoch.ApplyNotification(approval); err != nil || !epoch.HasActionableRequest("7") {
		t.Fatalf("seed cached approval: actionable=%v err=%v", epoch.HasActionableRequest("7"), err)
	}
	start := epoch.Handle(context.Background(), agentControlRequest{Operation: agentControlOpStart, Identity: identity, Epoch: "epoch-1", Text: "new"})
	if !start.OK || wire.reads != 1 || wire.writes() != 1 || epoch.HasActionableRequest("7") {
		t.Fatalf("start=%+v reads=%d writes=%d cached-approval=%v", start, wire.reads, wire.writes(), epoch.HasActionableRequest("7"))
	}
	review := epoch.Handle(context.Background(), agentControlRequest{Operation: agentControlOpReview, Identity: identity, Epoch: "epoch-1", RequestKey: "7", Decision: "accept"})
	if review.OK || wire.writes() != 1 {
		t.Fatalf("stale approval survived reconcile: response=%+v writes=%d", review, wire.writes())
	}
}

func TestApprovalResponseOnceRawIDCollisionResolutionAndReconnect(t *testing.T) {
	identity := phase6Identity()
	newEpoch := func(wire *fakeExactControlWire) *codexControlEpoch {
		return newCodexControlEpoch(wire, identity, "epoch-1", codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateWaitingOnApproval, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress}, func(codexLifecycleIdentity) bool { return true })
	}
	request := func(raw string) codexappserver.Notification {
		return codexappserver.Notification{Method: "item/commandExecution/requestApproval", RequestID: "7", RawRequestID: json.RawMessage(raw), Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"make test","cwd":"/work","availableDecisions":["accept","decline","cancel"]}`)}
	}

	wire := &fakeExactControlWire{}
	epoch := newEpoch(wire)
	if err := epoch.ApplyNotification(request(`7`)); err != nil {
		t.Fatal(err)
	}
	if err := epoch.ApplyNotification(request(`7`)); err != nil {
		t.Fatal(err)
	}
	review := agentControlRequest{Operation: agentControlOpReview, Identity: identity, Epoch: "epoch-1", RequestKey: "7", Decision: "accept"}
	var wg sync.WaitGroup
	responses := make(chan agentControlResponse, 2)
	for range 2 {
		wg.Go(func() { ; responses <- epoch.Handle(context.Background(), review) })
	}
	wg.Wait()
	close(responses)
	ok := 0
	for response := range responses {
		if response.OK {
			ok++
		}
	}
	if ok != 1 || len(wire.responses) != 1 || string(wire.responses[0]) != "7" {
		t.Fatalf("ok=%d raw=%q writes=%d", ok, wire.responses, len(wire.responses))
	}

	wire = &fakeExactControlWire{}
	epoch = newEpoch(wire)
	_ = epoch.ApplyNotification(request(`7`))
	_ = epoch.ApplyNotification(request(`"7"`))
	if response := epoch.Handle(context.Background(), review); response.OK || wire.writes() != 0 {
		t.Fatalf("raw-id collision response=%+v writes=%d", response, wire.writes())
	}

	wire = &fakeExactControlWire{}
	epoch = newEpoch(wire)
	first := request(`7`)
	conflicting := request(`7`)
	conflicting.Params = json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"make test","cwd":"/work","approvalId":"changed","availableDecisions":["accept","decline","cancel"]}`)
	if err := epoch.ApplyNotification(first); err != nil {
		t.Fatal(err)
	}
	if err := epoch.ApplyNotification(conflicting); err != nil {
		t.Fatal(err)
	}
	if err := epoch.ApplyNotification(first); err != nil {
		t.Fatal(err)
	}
	if response := epoch.Handle(context.Background(), review); response.OK || wire.writes() != 0 {
		t.Fatalf("conflicting reused raw id response=%+v writes=%d", response, wire.writes())
	}

	wire = &fakeExactControlWire{}
	epoch = newEpoch(wire)
	_ = epoch.ApplyNotification(request(`7`))
	resolved := codexappserver.Notification{Method: "serverRequest/resolved", Params: json.RawMessage(`{"threadId":"thread-1","requestId":7}`)}
	if err := epoch.ApplyNotification(resolved); err != nil {
		t.Fatal(err)
	}
	if response := epoch.Handle(context.Background(), review); response.OK || wire.writes() != 0 {
		t.Fatalf("resolved response=%+v writes=%d", response, wire.writes())
	}
	epoch.Revoke()
	if response := epoch.Handle(context.Background(), review); response.OK || wire.writes() != 0 {
		t.Fatalf("reconnected response=%+v writes=%d", response, wire.writes())
	}
}

func TestExactAgentControlTransportPathAndCollisionSafety(t *testing.T) {
	identity := phase6Identity()
	endpoint := phase6CLIEndpoint()
	shortRoot, err := os.MkdirTemp("/tmp", "p6-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shortRoot) })
	path, err := agentControlSocketPath(shortRoot, endpoint, identity)
	if err != nil {
		t.Fatal(err)
	}
	newActivation := identity
	newActivation.Generation = "generation-2"
	activationPath, err := agentControlSocketPath(shortRoot, endpoint, newActivation)
	if err != nil {
		t.Fatal(err)
	}
	if activationPath != path {
		t.Fatalf("Pane activation generation retargeted durable endpoint: got %q want %q", activationPath, path)
	}
	newEndpoint := endpoint
	newEndpoint.EndpointGenerationID = "endpoint-generation-2"
	endpointPath, err := agentControlSocketPath(shortRoot, newEndpoint, identity)
	if err != nil {
		t.Fatal(err)
	}
	if endpointPath == path {
		t.Fatalf("different durable endpoint generations collided at %q", path)
	}
	if filepath.Ext(path) != ".sock" || strings.Contains(path, identity.AgentUID) {
		t.Fatalf("socket path is not content-free: %q", path)
	}
	long := filepath.Join("/", strings.Repeat("long", 40))
	if _, err := agentControlSocketPath(long, endpoint, identity); err == nil {
		t.Fatal("overlong state path selected TMPDIR-dependent fallback")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "target"), path); err != nil {
		t.Fatal(err)
	}
	if err := prepareAgentControlSocket(path); err == nil {
		t.Fatal("symlink collision was removed or accepted")
	}
}

func TestExactAgentControlTransportEpochAndFrameBoundaries(t *testing.T) {
	shortRoot, err := os.MkdirTemp("/tmp", "p6-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(shortRoot) })
	identity := phase6Identity()
	endpoint := phase6CLIEndpoint()
	wire := &fakeExactControlWire{}
	epoch := newCodexControlEpoch(wire, identity, "epoch-1", codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle}, func(codexLifecycleIdentity) bool { return true })
	server, err := startCodexControlServer(shortRoot, endpoint, epoch)
	if errors.Is(err, syscall.EPERM) {
		t.Skip("Unix sockets are unavailable in this sandbox")
	}
	if err != nil {
		t.Fatal(err)
	}
	path := server.path
	if info, statErr := os.Stat(filepath.Dir(path)); statErr != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("control dir mode info=%v err=%v", info, statErr)
	}
	if info, statErr := os.Stat(path); statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("control socket mode info=%v err=%v", info, statErr)
	}
	if err := prepareAgentControlSocket(path); err == nil {
		t.Fatal("active control endpoint collision accepted")
	}
	request := agentControlRequest{Operation: agentControlOpStart, Identity: identity, Epoch: "old", Text: "exact text"}
	if response, err := callCodexControl(context.Background(), shortRoot, endpoint, identity, request); err != nil || response.OK || wire.writes() != 0 {
		t.Fatalf("stale transport response=%+v err=%v writes=%d", response, err, wire.writes())
	}
	request.Epoch = "epoch-1"
	if response, err := callCodexControl(context.Background(), shortRoot, endpoint, identity, request); err != nil || !response.OK || wire.writes() != 1 {
		t.Fatalf("exact transport response=%+v err=%v writes=%d", response, err, wire.writes())
	}
	for _, frame := range [][]byte{
		append([]byte(`{"operation":"status"}`), []byte(` {}`)...),
		[]byte(strings.Repeat("x", agentControlMaxFrame+1)),
	} {
		conn, dialErr := net.Dial("unix", path)
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		_, _ = conn.Write(frame)
		if unixConn, ok := conn.(*net.UnixConn); ok {
			_ = unixConn.CloseWrite()
		}
		var refused agentControlResponse
		decodeErr := json.NewDecoder(conn).Decode(&refused)
		_ = conn.Close()
		if decodeErr != nil || refused.OK || refused.Code != "invalid-frame" || wire.writes() != 1 {
			t.Fatalf("bad frame response=%+v err=%v writes=%d", refused, decodeErr, wire.writes())
		}
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("epoch exit left endpoint: %v", err)
	}
	if _, err := callCodexControl(context.Background(), shortRoot, endpoint, identity, request); err == nil || wire.writes() != 1 {
		t.Fatalf("closed epoch accepted request: err=%v writes=%d", err, wire.writes())
	}
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()
	if err := prepareAgentControlSocket(path); err != nil {
		t.Fatalf("owned stale endpoint was not cleaned: %v", err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale endpoint remains: %v", err)
	}
}

func TestApprovalDetailIsBoundedAndTerminalSafe(t *testing.T) {
	private := "secret\n\x1b[31m" + strings.Repeat("x", 400)
	label := approvalDetailLabel(agentPendingApproval{Kind: codexappserver.ApprovalCommand, RequestID: "r", ThreadID: "t", TurnID: "u", ItemID: "i", Command: private, CWD: private})
	if strings.ContainsAny(label, "\n\r\x1b") || len([]rune(label)) > 700 || !strings.Contains(label, "[truncated]") {
		t.Fatalf("unsafe detail label len=%d: %q", len([]rune(label)), label)
	}
	wire := &fakeExactControlWire{err: errors.New(private)}
	epoch := newCodexControlEpoch(wire, phase6Identity(), "epoch-1", codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle}, func(codexLifecycleIdentity) bool { return true })
	response := epoch.Handle(context.Background(), agentControlRequest{Operation: agentControlOpStart, Identity: phase6Identity(), Epoch: "epoch-1", Text: private})
	if strings.Contains(response.Message, "secret") {
		t.Fatalf("contentful provider error escaped control response: %+v", response)
	}
	controls := strings.Repeat("\x00\x01\x02\x1b", 100)
	if got := safeApprovalDetail(controls); len([]rune(got)) > 340 || strings.ContainsAny(got, "\x00\x01\x02\x1b") {
		t.Fatalf("post-escape bound failed len=%d value=%q", len([]rune(got)), got)
	}
}

func TestApprovalDecisionRowsCarryBoundedSafeTargetAndEffect(t *testing.T) {
	private := "run\n\x1b[31m" + strings.Repeat("x", 400)
	rows := []struct {
		pending  agentPendingApproval
		decision codexappserver.ApprovalDecision
		want     []string
	}{
		{agentPendingApproval{Kind: codexappserver.ApprovalCommand, Command: private, CWD: "/work", NetworkProtocol: "https", NetworkHost: "example.test"}, codexappserver.DecisionAccept, []string{"Allow once", "command=", "cwd=", "network="}},
		{agentPendingApproval{Kind: codexappserver.ApprovalFileChange, ItemID: "item-1", Reason: private}, codexappserver.DecisionDecline, []string{"Decline", "item=", "reason="}},
		{agentPendingApproval{Kind: codexappserver.ApprovalPermissions, RequestCWD: "/work", Permissions: json.RawMessage(`{"network":{"enabled":true}}`)}, codexappserver.DecisionGrantTurn, []string{"scope=turn", "strictAutoReview=null", "cwd=", "permissions="}},
	}
	for _, row := range rows {
		label := approvalDecisionLabelLocale(i18n.FallbackLocale, row.pending, row.decision)
		if strings.ContainsAny(label, "\n\r\x1b") || len([]rune(label)) > 900 {
			t.Fatalf("unsafe actionable label len=%d: %q", len([]rune(label)), label)
		}
		for _, want := range row.want {
			if !strings.Contains(label, want) {
				t.Fatalf("label %q missing %q", label, want)
			}
		}
	}
}

func TestApprovalAvailabilityProjectionIsResponderOrFocusOnly(t *testing.T) {
	projection := codexLifecycleProjection{Notices: []codexLifecycleNotice{{Category: "approval_required"}, {Category: "response_complete"}}}
	markCodexApprovalAvailability(&projection, false)
	if projection.Notices[0].ResponderAvailable || projection.Notices[1].ResponderAvailable {
		t.Fatalf("unavailable projection = %#v", projection.Notices)
	}
	markCodexApprovalAvailability(&projection, true)
	if !projection.Notices[0].ResponderAvailable || projection.Notices[1].ResponderAvailable {
		t.Fatalf("available projection = %#v", projection.Notices)
	}
}

func TestExactAgentControlRejectsEmptyTextBeforeWire(t *testing.T) {
	for _, operation := range []string{agentControlOpStart, agentControlOpSteer} {
		wire := &fakeExactControlWire{}
		snapshot := codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateIdle}
		if operation == agentControlOpSteer {
			snapshot = codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateActive, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress}
		}
		epoch := newCodexControlEpoch(wire, phase6Identity(), "epoch-1", snapshot, func(codexLifecycleIdentity) bool { return true })
		response := epoch.Handle(context.Background(), agentControlRequest{Operation: operation, Identity: phase6Identity(), Epoch: "epoch-1", Text: " \t\n"})
		if response.OK || wire.writes() != 0 {
			t.Fatalf("operation=%s response=%+v writes=%d", operation, response, wire.writes())
		}
	}
}

func TestLostSnapshotApprovalEnvelopeIsFocusOnlyAndWritesZero(t *testing.T) {
	wire := &fakeExactControlWire{}
	epoch := newCodexControlEpoch(wire, phase6Identity(), "epoch-1", codexappserver.LifecycleSnapshot{
		ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateWaitingOnApproval,
		TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress,
	}, func(codexLifecycleIdentity) bool { return true })
	identity := phase6Identity()
	list := epoch.Handle(context.Background(), agentControlRequest{Operation: agentControlOpApprovals, Identity: identity, Epoch: "epoch-1"})
	if !list.OK || list.Availability.Review || len(list.Approvals) != 0 {
		t.Fatalf("lost-envelope availability = %+v", list)
	}
	review := epoch.Handle(context.Background(), agentControlRequest{Operation: agentControlOpReview, Identity: identity, Epoch: "epoch-1", RequestKey: "7", Decision: "accept"})
	if review.OK || wire.writes() != 0 {
		t.Fatalf("lost-envelope review=%+v writes=%d", review, wire.writes())
	}
}

func TestIncompleteApprovalEnvelopeKeepsFocusFallbackAndWritesZero(t *testing.T) {
	wire := &fakeExactControlWire{}
	epoch := newCodexControlEpoch(wire, phase6Identity(), "epoch-1", codexappserver.LifecycleSnapshot{
		ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateWaitingOnApproval,
		TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress,
	}, func(codexLifecycleIdentity) bool { return true })
	notification := codexappserver.Notification{
		Method: "item/permissions/requestApproval", RequestID: "lost-envelope", RawRequestID: json.RawMessage(`"lost-envelope"`),
		Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1"}`),
	}
	if err := epoch.ApplyNotification(notification); err != nil {
		t.Fatalf("incomplete responder envelope invalidated lifecycle fallback: %v", err)
	}
	if epoch.HasActionableRequest("lost-envelope") || epoch.availability().Review {
		t.Fatal("incomplete responder envelope became actionable")
	}
	response := epoch.Handle(context.Background(), agentControlRequest{
		Operation: agentControlOpReview, Identity: phase6Identity(), Epoch: "epoch-1",
		RequestKey: "lost-envelope", Decision: "grant-turn",
	})
	if response.OK || wire.writes() != 0 {
		t.Fatalf("incomplete responder response=%+v writes=%d", response, wire.writes())
	}
}

func TestUnsafeApprovalDecisionTableWritesZero(t *testing.T) {
	for _, test := range []struct {
		name     string
		method   string
		params   string
		decision string
	}{
		{name: "session command", method: "item/commandExecution/requestApproval", params: `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"x","availableDecisions":["acceptForSession"]}`, decision: "acceptForSession"},
		{name: "execpolicy amendment", method: "item/commandExecution/requestApproval", params: `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"x","availableDecisions":["acceptWithExecpolicyAmendment"]}`, decision: "acceptWithExecpolicyAmendment"},
		{name: "network amendment", method: "item/commandExecution/requestApproval", params: `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"x","availableDecisions":[{"applyNetworkPolicyAmendment":{"network_policy_amendment":{"host":"example.test","action":"allow"}}}]}`, decision: "applyNetworkPolicyAmendment"},
		{name: "additional permissions accept", method: "item/commandExecution/requestApproval", params: `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"x","additionalPermissions":{"network":{"enabled":true}},"availableDecisions":["accept"]}`, decision: "accept"},
		{name: "invalid network accept", method: "item/commandExecution/requestApproval", params: `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"x","networkApprovalContext":{"host":"example.test","protocol":"future"},"availableDecisions":["accept"]}`, decision: "accept"},
		{name: "unstable grantRoot accept", method: "item/fileChange/requestApproval", params: `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"grantRoot":"/root"}`, decision: "accept"},
		{name: "empty permission grant", method: "item/permissions/requestApproval", params: `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"cwd":"/work","permissions":{}}`, decision: "grant-turn"},
		{name: "synthetic permission denial", method: "item/permissions/requestApproval", params: `{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"cwd":"/work","permissions":{"network":{"enabled":true}}}`, decision: "decline"},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire := &fakeExactControlWire{}
			epoch := newCodexControlEpoch(wire, phase6Identity(), "epoch-1", codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateWaitingOnApproval, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress}, func(codexLifecycleIdentity) bool { return true })
			n := codexappserver.Notification{Method: test.method, RequestID: "31", RawRequestID: json.RawMessage(`31`), Params: json.RawMessage(test.params)}
			if err := epoch.ApplyNotification(n); err != nil {
				t.Fatal(err)
			}
			response := epoch.Handle(context.Background(), agentControlRequest{Operation: agentControlOpReview, Identity: phase6Identity(), Epoch: "epoch-1", RequestKey: "31", Decision: test.decision})
			if response.OK || wire.writes() != 0 {
				t.Fatalf("response=%+v writes=%d", response, wire.writes())
			}
		})
	}
}

func TestApprovalCancelImmediatelyMakesExactTurnNonMutable(t *testing.T) {
	wire := &fakeExactControlWire{}
	epoch := newCodexControlEpoch(wire, phase6Identity(), "epoch-1", codexappserver.LifecycleSnapshot{ThreadID: "thread-1", ThreadState: codexappserver.ThreadStateWaitingOnApproval, TurnID: "turn-1", TurnState: codexappserver.TurnStateInProgress}, func(codexLifecycleIdentity) bool { return true })
	n := codexappserver.Notification{Method: "item/commandExecution/requestApproval", RequestID: "41", RawRequestID: json.RawMessage(`41`), Params: json.RawMessage(`{"threadId":"thread-1","turnId":"turn-1","itemId":"item-1","startedAtMs":1,"command":"x","availableDecisions":["cancel"]}`)}
	if err := epoch.ApplyNotification(n); err != nil {
		t.Fatal(err)
	}
	identity := phase6Identity()
	if response := epoch.Handle(context.Background(), agentControlRequest{Operation: agentControlOpReview, Identity: identity, Epoch: "epoch-1", RequestKey: "41", Decision: "cancel"}); !response.OK {
		t.Fatalf("cancel response=%+v", response)
	}
	if response := epoch.Handle(context.Background(), agentControlRequest{Operation: agentControlOpSteer, Identity: identity, Epoch: "epoch-1", Text: "late"}); response.OK || wire.writes() != 1 {
		t.Fatalf("post-cancel steer=%+v writes=%d", response, wire.writes())
	}
}

func TestExactAgentControlCanonicalLabelsAreLocalized(t *testing.T) {
	labels := []struct {
		key i18n.Key
		en  string
	}{
		{i18n.KeyAgentControlSendTurn, agentActionSendTurn},
		{i18n.KeyAgentControlSteerTurn, agentActionSteerTurn},
		{i18n.KeyAgentControlInterruptTurn, agentActionInterruptTurn},
		{i18n.KeyAgentControlReviewApproval, agentActionReviewApproval},
		{i18n.KeyAgentControlOpenCodex, agentActionOpenCodex},
	}
	for _, label := range labels {
		if got := localizeText(i18n.FallbackLocale, label.key, "missing"); got != label.en {
			t.Fatalf("en-US %s = %q, want %q", label.key, got, label.en)
		}
		if got := localizeText(i18n.Locale("ko-KR"), label.key, label.en); got == label.en {
			t.Fatalf("ko-KR %s remained %q", label.key, got)
		}
	}
}
