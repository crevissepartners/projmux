package processhost

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"sort"
	"sync"
	"syscall"
	"time"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

var (
	ErrStale  = errors.New("stale process binding or request")
	ErrBusy   = errors.New("process admission capacity exhausted")
	ErrClosed = errors.New("process control closed")
)

// Binding is immutable ownership authority. PID is intentionally absent.
type Binding struct {
	Host, Project, Window, Agent, Pane, Generation, Operation string
}

func (b Binding) valid(host string) bool {
	return b.Host == host && b.Host != "" && b.Project != "" && b.Window != "" && b.Agent != "" && b.Pane != "" && b.Generation != "" && b.Operation != ""
}

// Transactions validate the current ownership chain and reserve an operation,
// then compare-and-set its session binding. Implementations must honor context
// and must not hold registry locks while waiting for a provider.
type Transactions struct {
	Reserve func(context.Context, Binding) error
	Commit  func(context.Context, Binding, string) error
	Current func(context.Context, Binding) error
}

// Limits bound retained launches, frames, events, pending requests, diagnostic
// bytes and I/O delays. Launches are not evicted: forgetting an operation could
// turn a retry into a duplicate child. A new Host is a new instance/lifetime.
type Limits struct {
	Launches, FrameBytes, Events, Requests, DiagnosticBytes int
	Startup, Write, Grace, MessageReservation               time.Duration
}

func DefaultLimits() Limits {
	return Limits{Launches: 64, FrameBytes: 1 << 20, Events: 256, Requests: 32, DiagnosticBytes: 8192, Startup: 10 * time.Second, Write: time.Second, Grace: 2 * time.Second, MessageReservation: 30 * time.Second}
}

// Host owns all handles it creates. Supervisor is a resolved executable that
// invokes ServeSupervisor with inherited descriptors 3, 4 and 5. Its arguments
// and environment are supplied by the consumer; no user settings are edited.
type Host struct {
	mu         sync.Mutex
	instance   string
	supervisor Command
	tx         Transactions
	limits     Limits
	operations map[string]*Handle
	panes      map[string]*Handle
}

func NewHost(instance string, supervisor Command, tx Transactions, limits Limits) (*Host, error) {
	if instance == "" || supervisor.Path == "" || supervisor.Env == nil || tx.Reserve == nil || tx.Commit == nil || tx.Current == nil {
		return nil, errors.New("incomplete process host configuration")
	}
	if limits.Launches < 1 || limits.FrameBytes < 256 || limits.Events < 2 || limits.Requests < 1 || limits.DiagnosticBytes < 1 || limits.Startup <= 0 || limits.Write <= 0 || limits.Grace <= 0 || limits.Grace > time.Minute {
		return nil, errors.New("invalid process limits")
	}
	if limits.MessageReservation == 0 {
		limits.MessageReservation = DefaultLimits().MessageReservation
	}
	if limits.MessageReservation < 0 {
		return nil, errors.New("invalid message reservation limit")
	}
	supervisor.Args = slices.Clone(supervisor.Args)
	supervisor.Env = slices.Clone(supervisor.Env)
	return &Host{instance: instance, supervisor: supervisor, tx: tx, limits: limits, operations: make(map[string]*Handle), panes: make(map[string]*Handle)}, nil
}

// Launch fixes one operation's exact command and binding. Retrying with changed
// parameters is refused even if the earlier launch has already exited.
type Launch struct {
	Binding                  Binding
	Command                  Command
	Completion               *Completion
	Spawned                  *SpawnCallback
	TurnCompleted            *TurnCompletion
	provider                 string
	adapter                  adapterConfig
	resume                   *SessionRecord
	transfer                 *ClaudeTransfer
	codexTransfer            *CodexTransfer
	resumeTurn, resumePrompt string
}

// TurnCompletion observes an actual correlated Claude result after admission
// is cleared. Notify runs asynchronously outside host locks and never writes
// provider input; its pointer belongs to the immutable launch identity.
type TurnCompletion struct{ Notify func(Binding) }

// SpawnCallback publishes exact child birth before provider initialization.
// The pointer is launch identity; retries retain it and never publish twice.
// Publish runs outside Host/Handle locks and must honor the startup context.
type SpawnCallback struct {
	Publish func(context.Context, *Handle) error
}

// Completion binds owned cleanup to a launch before any child can exit. Its
// pointer is part of the launch identity: retries must retain the same owner.
// Cleanup is immutable after Start, runs without Handle/Host locks, and receives
// the host's bounded write budget. It must honor cancellation.
type Completion struct {
	Cleanup func(context.Context) error
}

// Handle is tied to one owned supervisor/child pair and cannot adopt a PID.
type Handle struct {
	host                   *Host
	launch                 Launch
	mu                     sync.Mutex
	state                  string
	session                string
	hookSession            string
	connection             string
	turn                   string
	turnOrigin             string
	turnOpen               bool
	providerTurns          uint64
	joined                 []string
	joinedBytes            int
	carriedJoined          []string
	unattributed           []string
	messageReservation     string
	messageOutcomeRecorded bool
	interrupt              string
	interruptAck           bool
	usedTurns              map[string]bool
	usedTurnOrder          []string
	requests               map[string]Request
	usedRequests           map[string]bool
	seq                    uint64
	droppedThrough         uint64
	events                 []Event
	critical               []Event
	diagnostics            []byte
	failure                string
	exit                   *Exit
	pid                    int
	supervisorPID          int
	stdin                  *os.File
	lifetime               *os.File
	ready                  chan struct{}
	done                   chan struct{}
	statusDone             chan struct{}
	spawnErr               error
	stopOnce               sync.Once
	adapter                providerAdapter
	completionContext      context.Context

	messageReservationTimer *time.Timer
}

// Snapshot is a bounded resynchronization view. Turn results do not set Exit.
type Snapshot struct {
	Binding                                   Binding
	State, Session, Connection, Turn, Failure string
	MessageReservation, Provider              string
	PID, SupervisorPID                        int
	Sequence                                  uint64
	Pending                                   []Request
	Diagnostic                                string
	Exit                                      *Exit
	Resume                                    *ResumeHistory
}

// Event carries host-local sequence and exact binding; Raw is bounded by the
// frame limit. A gap forces snapshot resynchronization rather than pretending
// output or a control transition was observed.
type Event struct {
	Binding                   Binding
	Session, Connection, Turn string
	Sequence                  uint64
	Kind                      string
	Raw                       json.RawMessage
	Request                   *Request
	Exit                      *Exit
}

func (h *Host) Start(ctx context.Context, launch Launch) (*Handle, error) {
	if launch.provider == "" {
		launch.provider = "claude"
	}
	if !launch.Binding.valid(h.instance) || launch.Command.Path == "" || launch.Command.Env == nil {
		return nil, errors.New("invalid launch or implicit environment")
	}
	p, joined, err := h.register(ctx, launch)
	if joined || err != nil {
		return p, err
	}
	err = p.boot(ctx)
	p.spawnErr = err
	close(p.ready)
	if err != nil && p.lifetime == nil {
		p.mu.Lock()
		p.state, p.failure = "unknown", err.Error()
		p.mu.Unlock()
		p.finish()
	}
	return p, err
}

// register admits one new operation, or joins an identical in-flight launch of
// the same operation and reports its outcome.
func (h *Host) register(ctx context.Context, launch Launch) (*Handle, bool, error) {
	h.mu.Lock()
	if old := h.operations[launch.Binding.Operation]; old != nil {
		h.mu.Unlock()
		if !reflect.DeepEqual(old.launch, launch) {
			return nil, true, ErrStale
		}
		select {
		case <-old.ready:
			return old, true, old.spawnErr
		case <-ctx.Done():
			return nil, true, ctx.Err()
		}
	}
	if h.panes[launch.Binding.Pane] != nil || len(h.operations) >= h.limits.Launches {
		h.mu.Unlock()
		return nil, true, ErrBusy
	}
	// Clone caller-owned slices before the launch can race a caller mutation.
	launch.Command.Args = slices.Clone(launch.Command.Args)
	launch.Command.Env = slices.Clone(launch.Command.Env)
	if launch.resume != nil {
		record := *launch.resume
		record.Pending = slices.Clone(record.Pending)
		launch.resume = &record
	}
	if launch.transfer != nil {
		transfer := *launch.transfer
		launch.transfer = &transfer
	}
	if launch.codexTransfer != nil {
		transfer := *launch.codexTransfer
		launch.codexTransfer = &transfer
	}
	if launch.adapter != nil {
		launch.adapter = launch.adapter.clone()
	}
	p := &Handle{host: h, launch: launch, completionContext: context.WithoutCancel(ctx), state: "starting", connection: launch.Binding.Operation, ready: make(chan struct{}), done: make(chan struct{}), statusDone: make(chan struct{}), usedTurns: make(map[string]bool), requests: make(map[string]Request), usedRequests: make(map[string]bool)}
	if launch.adapter != nil {
		p.adapter = launch.adapter.newAdapter(p)
	}
	h.operations[launch.Binding.Operation], h.panes[launch.Binding.Pane] = p, p
	h.mu.Unlock()
	return p, false, nil
}

// boot reserves the binding, spawns the dedicated child, and initializes its
// provider. An initialization failure waits for the owned child to exit.
func (p *Handle) boot(ctx context.Context) error {
	h := p.host
	reserveCtx, cancelReserve := context.WithTimeout(ctx, h.limits.Startup)
	err := h.tx.Reserve(reserveCtx, p.launch.Binding)
	cancelReserve()
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return err
	}
	if err = p.spawn(ctx); err != nil {
		return err
	}
	if err = p.initializeProvider(ctx); err != nil {
		p.protocolFailure(err)
		waitCtx, cancel := context.WithTimeout(context.Background(), 5*h.limits.Grace)
		_, _ = p.Wait(waitCtx, p.launch.Binding)
		cancel()
	}
	return err
}

// initializeProvider publishes the spawned child before provider handshake or
// recorded-session resume initialization.
func (p *Handle) initializeProvider(ctx context.Context) error {
	launch := p.launch
	if launch.Spawned != nil && launch.Spawned.Publish != nil {
		if err := launch.Spawned.Publish(ctx, p); err != nil {
			return err
		}
	}
	switch {
	case p.adapter != nil:
		return p.adapter.initialize(ctx)
	case launch.expectedResumeSession() != "":
		return p.initializeResume(ctx)
	}
	return nil
}

func (p *Handle) spawn(ctx context.Context) error {
	// Cleanup after a pipe allocation error is best effort: preserve the
	// allocation error rather than replace it with a secondary close error.
	// os.File pipes permit bounded write deadlines and explicit close independent
	// of exec.Wait; descendants holding an output FD cannot hold Wait hostage.
	inR, inW, err := os.Pipe()
	if err != nil {
		return err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		return err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
		return err
	}
	lifeR, lifeW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
		_ = errR.Close()
		_ = errW.Close()
		return err
	}
	specR, specW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
		_ = errR.Close()
		_ = errW.Close()
		_ = lifeR.Close()
		_ = lifeW.Close()
		return err
	}
	statusR, statusW, err := os.Pipe()
	if err != nil {
		_ = inR.Close()
		_ = inW.Close()
		_ = outR.Close()
		_ = outW.Close()
		_ = errR.Close()
		_ = errW.Close()
		_ = lifeR.Close()
		_ = lifeW.Close()
		_ = specR.Close()
		_ = specW.Close()
		return err
	}
	all := []*os.File{inR, inW, outR, outW, errR, errW, lifeR, lifeW, specR, specW, statusR, statusW}
	cleanup := func() {
		for _, f := range all {
			_ = f.Close()
		}
	}
	s := p.host.supervisor
	// #nosec G204 -- the consumer supplies a resolved executable/argv for this dedicated child; no shell interpolation or PID adoption.
	cmd := exec.Command(s.Path, s.Args...)
	cmd.Dir, cmd.Env = s.Dir, s.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	cmd.ExtraFiles = []*os.File{lifeR, specR, statusW}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err = cmd.Start(); err != nil {
		cleanup()
		return err
	}
	_ = inR.Close()
	_ = outW.Close()
	_ = errW.Close()
	_ = lifeR.Close()
	_ = specR.Close()
	_ = statusW.Close()
	_ = statusR.SetReadDeadline(time.Now().Add(p.host.limits.Startup))
	var prepared processStatus
	if err := json.NewDecoder(statusR).Decode(&prepared); err != nil || !prepared.Prepared {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		cleanup()
		return errors.New("supervisor preparation handshake failed")
	}
	_ = statusR.SetReadDeadline(time.Time{})
	p.stdin, p.lifetime, p.supervisorPID = inW, lifeW, cmd.Process.Pid
	_ = specW.SetWriteDeadline(time.Now().Add(p.host.limits.Startup))
	err = json.NewEncoder(specW).Encode(supervisorSpec{Command: p.launch.Command, Grace: p.host.limits.Grace})
	_ = specW.Close()
	if err != nil {
		_ = lifeW.Close()
	}
	first := make(chan error, 1)
	outputDone := make(chan struct{})
	if p.adapter != nil {
		p.adapter.attach(&ownedStream{in: p.stdin, out: outR, lifetime: p.lifetime, reader: bufio.NewReaderSize(outR, 4096), limit: p.host.limits.FrameBytes, write: p.host.limits.Write})
	}
	go func() {
		defer close(outputDone)
		if p.adapter != nil {
			p.adapter.readOutput()
		} else {
			p.readOutput(outR)
		}
	}()
	diagnosticDone := make(chan struct{})
	go func() { defer close(diagnosticDone); p.readDiagnostics(errR) }()
	go p.readStatus(cmd, statusR, outR, errR, first, outputDone, diagnosticDone)
	timer := time.NewTimer(p.host.limits.Startup)
	defer timer.Stop()
	select {
	case err = <-first:
		if err != nil {
			p.rollback(cmd)
		}
		return err
	case <-ctx.Done():
		p.rollback(cmd)
		return ctx.Err()
	case <-timer.C:
		p.rollback(cmd)
		return errors.New("supervisor startup timeout")
	}
}

// rollback also handles an owned helper stopped before it can consume the
// launch. Continue it so the lifetime EOF can be observed; a nonconforming
// executable is forcibly reaped and yields unknown, never a child exit receipt.
func (p *Handle) rollback(cmd *exec.Cmd) {
	p.stop()
	_ = cmd.Process.Signal(syscall.SIGCONT)
	// Allow EOF grace, TERM grace, group reaping and stream drain, plus
	// one grace of scheduling margin before killing a nonconforming helper.
	timer := time.NewTimer(5 * p.host.limits.Grace)
	defer timer.Stop()
	select {
	case <-p.done:
		return
	case <-timer.C:
		_ = cmd.Process.Kill()
		<-p.done
	}
}

func (p *Handle) readStatus(cmd *exec.Cmd, r, stdout, stderr *os.File, first chan<- error, outputDone, diagnosticDone <-chan struct{}) {
	defer r.Close()
	decoder := json.NewDecoder(io.LimitReader(r, 16384))
	var status processStatus
	err := decoder.Decode(&status)
	if err == nil && status.Error != "" {
		err = errors.New(status.Error)
	}
	if err == nil && status.PID <= 0 {
		err = errors.New("missing owned child handshake")
	}
	p.mu.Lock()
	p.pid = status.PID
	p.mu.Unlock()
	first <- err
	var actual *Exit
	if err == nil {
		err = decoder.Decode(&status)
		if err == nil {
			actual = status.Exit
			if actual == nil {
				err = errors.New("missing child Wait evidence")
			}
		}
	}
	// Publish completed status observation before waiting for the helper's own
	// exit, so ordinary provider EOF need not wait for an artificial grace delay.
	close(p.statusDone)
	waitErr := cmd.Wait()
	if waitErr != nil {
		_ = stdout.Close()
		_ = stderr.Close()
	}
	_ = p.stdin.Close()
	_ = p.lifetime.Close()
	// An escaped descendant may retain either write FD after the owned group
	// and supervisor are gone. Drain both concurrently under one grace budget.
	output, diagnostic := outputDone, diagnosticDone
	timer := time.NewTimer(p.host.limits.Grace)
	defer timer.Stop()
	for output != nil || diagnostic != nil {
		select {
		case <-output:
			output = nil
		case <-diagnostic:
			diagnostic = nil
		case <-timer.C:
			_ = stdout.Close()
			_ = stderr.Close()
			p.mu.Lock()
			p.emitLocked("stream-gap", []byte(`{"reason":"drain-timeout"}`), nil)
			p.mu.Unlock()
			output, diagnostic = nil, nil
		}
	}
	<-outputDone
	<-diagnosticDone
	_ = stdout.Close()
	_ = stderr.Close()
	p.mu.Lock()
	p.expireLocked()
	if actual != nil && waitErr == nil {
		p.exit, p.state = actual, "exited"
		p.clearMessageReservationLocked()
		p.emitLocked("process-exited", nil, nil)
	} else {
		p.state = "unknown"
		p.failure = fmt.Sprintf("supervisor evidence unavailable: %v; wait: %v", err, waitErr)
		p.emitLocked("host-unknown", nil, nil)
	}
	p.mu.Unlock()
	p.finish()
}

func (p *Handle) finish() {
	p.mu.Lock()
	p.stopMessageReservationTimerLocked()
	p.mu.Unlock()
	if completion := p.launch.Completion; completion != nil && completion.Cleanup != nil {
		ctx, cancel := context.WithTimeout(p.completionContext, p.host.limits.Write)
		result := make(chan error, 1)
		go func() { result <- completion.Cleanup(ctx) }()
		var cleanupErr error
		select {
		case cleanupErr = <-result:
		case <-ctx.Done():
			cleanupErr = ctx.Err()
		}
		cancel()
		if cleanupErr != nil {
			p.mu.Lock()
			p.diagnostics = append(p.diagnostics, fmt.Appendf(nil, "\nowned completion cleanup: %v\n", cleanupErr)...)
			if len(p.diagnostics) > p.host.limits.DiagnosticBytes {
				p.diagnostics = bytes.Clone(p.diagnostics[len(p.diagnostics)-p.host.limits.DiagnosticBytes:])
			}
			p.mu.Unlock()
		}
	}

	p.host.mu.Lock()
	if p.host.panes[p.launch.Binding.Pane] == p {
		delete(p.host.panes, p.launch.Binding.Pane)
	}
	p.host.mu.Unlock()
	close(p.done)
}

func (p *Handle) stop() {
	p.stopOnce.Do(func() {
		p.mu.Lock()
		p.stopMessageReservationTimerLocked()
		if p.state != "exited" && p.state != "unknown" {
			p.state = "stopping"
		}
		p.expireLocked()
		// Deliver provider EOF before asking the supervisor to start its bounded
		// shutdown. Admission and pending control are already closed above.
		if p.stdin != nil {
			_ = p.stdin.Close()
		}
		if p.lifetime != nil {
			_ = p.lifetime.Close()
		}
		p.mu.Unlock()
	})
}

func (p *Handle) Stop(binding Binding) error {
	if binding != p.launch.Binding {
		return ErrStale
	}
	p.stop()
	return nil
}

func (p *Handle) Wait(ctx context.Context, binding Binding) (Snapshot, error) {
	if binding != p.launch.Binding {
		return Snapshot{}, ErrStale
	}
	select {
	case <-p.done:
		return p.Observe(binding)
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	}
}

func (p *Handle) Observe(binding Binding) (Snapshot, error) {
	if binding != p.launch.Binding {
		return Snapshot{}, ErrStale
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshotLocked(), nil
}

func (p *Handle) snapshotLocked() Snapshot {
	s := Snapshot{Provider: p.launch.provider, Binding: p.launch.Binding, State: p.state, Session: p.session, Connection: p.connection, Turn: p.turn, MessageReservation: p.messageReservation, PID: p.pid, SupervisorPID: p.supervisorPID, Sequence: p.seq, Failure: p.failure, Diagnostic: string(p.diagnostics)}
	if p.exit != nil {
		e := *p.exit
		s.Exit = &e
	}
	if p.launch.resume != nil {
		s.Resume = p.launch.resume.history()
	}
	for _, req := range p.requests {
		s.Pending = append(s.Pending, cloneRequest(req))
	}
	return s
}

// Events returns a bounded batch and a snapshot from the same lock. Subscribers
// own only a sequence cursor, never a queue/goroutine that can block the host.
func (p *Handle) Events(binding Binding, after uint64) ([]Event, Snapshot, error) {
	if binding != p.launch.Binding {
		return nil, Snapshot{}, ErrStale
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	var result []Event
	if p.droppedThrough > after {
		result = append(result, Event{Binding: binding, Sequence: p.droppedThrough, Kind: "stream-gap"})
	}
	combined := append(append([]Event{}, p.events...), p.critical...)
	sort.Slice(combined, func(i, j int) bool { return combined[i].Sequence < combined[j].Sequence })
	for _, event := range combined {
		if event.Sequence > after {
			event.Raw = bytes.Clone(event.Raw)
			if event.Request != nil {
				req := cloneRequest(*event.Request)
				event.Request = &req
			}
			if event.Exit != nil {
				e := *event.Exit
				event.Exit = &e
			}
			result = append(result, event)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Sequence < result[j].Sequence })
	return result, p.snapshotLocked(), nil
}

// Completed operation IDs are a bounded stale fence, not lifetime admission.
// The current operation stays in the fence until it completes.
func (p *Handle) rememberTurnLocked(operation string) {
	p.fenceOperationLocked(operation)
	// Request IDs belong to the exact active turn, never the whole session.
	clear(p.usedRequests)
}

// fenceOperationLocked consumes an operation ID without starting a turn, so a
// joined input keeps the running turn's request identities.
func (p *Handle) fenceOperationLocked(operation string) {
	for len(p.usedTurnOrder) >= p.host.limits.Events {
		oldest := p.usedTurnOrder[0]
		p.usedTurnOrder = p.usedTurnOrder[1:]
		delete(p.usedTurns, oldest)
	}
	p.usedTurns[operation] = true
	p.usedTurnOrder = append(p.usedTurnOrder, operation)
}

func (p *Handle) forgetUnwrittenTurnLocked(operation string) {
	delete(p.usedTurns, operation)
	p.usedTurnOrder = slices.DeleteFunc(p.usedTurnOrder, func(id string) bool { return id == operation })
}

// Control history is independent of output. Preserve live request evidence
// and current-turn transitions; only completed history can become a gap.
func (p *Handle) trimCriticalLocked() {
	for len(p.critical) > p.host.limits.Events {
		index := -1
		for i, event := range p.critical {
			if event.Kind == "process-exited" || event.Kind == "stream-gap" ||
				(p.turn != "" && event.Turn == p.turn) {
				continue
			}
			if event.Request != nil {
				if _, pending := p.requests[event.Request.ID]; pending {
					continue
				}
			}
			index = i
			break
		}
		if index < 0 {
			return
		}
		p.droppedThrough = max(p.droppedThrough, p.critical[index].Sequence)
		p.critical = append(p.critical[:index], p.critical[index+1:]...)
	}
}

func (p *Handle) activeCriticalLocked() int {
	count := 0
	for _, event := range p.critical {
		if p.turn != "" && event.Turn == p.turn {
			count++
		}
	}
	return count
}

func (p *Handle) emitLocked(kind string, raw []byte, request *Request) {
	p.seq++
	event := Event{Binding: p.launch.Binding, Session: p.session, Connection: p.connection, Turn: p.turn, Sequence: p.seq, Kind: kind, Raw: bytes.Clone(raw), Request: request, Exit: p.exit}
	if kind != "output" && kind != "provider-event" {
		p.critical = append(p.critical, event)
		p.trimCriticalLocked()
		return
	}
	if len(p.events) == p.host.limits.Events {
		p.droppedThrough = max(p.droppedThrough, p.events[0].Sequence)
		copy(p.events, p.events[1:])
		p.events = p.events[:len(p.events)-1]
	}
	p.events = append(p.events, event)
}

func (p *Handle) readDiagnostics(r *os.File) {
	defer r.Close()
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			p.mu.Lock()
			p.diagnostics = append(p.diagnostics, buf[:n]...)
			if len(p.diagnostics) > p.host.limits.DiagnosticBytes {
				p.diagnostics = bytes.Clone(p.diagnostics[len(p.diagnostics)-p.host.limits.DiagnosticBytes:])
			}
			p.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (p *Handle) readOutput(r *os.File) {
	defer r.Close()
	reader := bufio.NewScanner(r)
	reader.Buffer(make([]byte, min(4096, p.host.limits.FrameBytes)), p.host.limits.FrameBytes)
	for reader.Scan() {
		if err := p.consume(reader.Bytes()); err != nil {
			p.protocolFailure(err)
			return
		}
	}
	if err := reader.Err(); err != nil {
		select {
		case <-p.statusDone:
			return // closing a bounded drain is reported as a gap by readStatus
		default:
		}
		p.mu.Lock()
		closed := p.state == "exited" || p.state == "stopping" || p.state == "unknown"
		p.mu.Unlock()
		if !closed {
			p.protocolFailure(err)
		}
		return
	}
	// EOF is not exit evidence. Give the independent status reader a bounded
	// chance to report real Wait before treating an orphaned stream as failure.
	timer := time.NewTimer(p.host.limits.Grace)
	defer timer.Stop()
	select {
	case <-p.statusDone:
		return
	case <-timer.C:
	}
	p.mu.Lock()
	active := p.state != "exited" && p.state != "unknown" && p.state != "stopping"
	p.mu.Unlock()
	if active {
		p.protocolFailure(errors.New("provider stdout EOF"))
	}
}

func (p *Handle) protocolFailure(err error) {
	p.mu.Lock()
	p.failure = err.Error()
	p.emitLocked("protocol-error", nil, nil)
	p.mu.Unlock()
	p.stop()
}

// Termination uses the existing pure classifier; the caller offers this exact
// generation's evidence through metadata.Mutator.RecordTermination. No hook or
// turn event invokes that writer, and no registry is mutated by this package.
func (s Snapshot) Termination(at time.Time) (metadata.TerminationEvidence, bool) {
	if s.State != "exited" || s.Exit == nil {
		return metadata.TerminationEvidence{}, false
	}
	e := metadata.TerminationEvidence{Source: metadata.TerminationSourceSupervisor, Classification: metadata.ClassifyProcessExit(s.Exit.Code, s.Exit.Signal), ObservedAt: at, PaneUID: s.Binding.Pane, AgentUID: s.Binding.Agent, Generation: s.Binding.Generation, OperationID: s.Binding.Operation, Signal: s.Exit.Signal}
	if s.Exit.Signal == "" {
		code := s.Exit.Code
		e.ExitCode = &code
	}
	return e, true
}

// adapterConfig is immutable launch identity; clones freeze caller-owned slices.
type adapterConfig interface {
	clone() adapterConfig
	newAdapter(*Handle) providerAdapter
}
type providerAdapter interface {
	attach(io.ReadWriteCloser)
	initialize(context.Context) error
	readOutput()
}

// ownedStream leaves child Wait to the supervisor. Close cancels only this
// transport and owner lifetime; it unblocks a stalled reader/writer immediately.
type ownedStream struct {
	in, out, lifetime *os.File
	reader            *bufio.Reader
	frame             []byte
	limit             int
	write             time.Duration
	once              sync.Once
}

func (s *ownedStream) Read(b []byte) (int, error) {
	if len(s.frame) == 0 {
		for {
			part, err := s.reader.ReadSlice('\n')
			if len(s.frame)+len(part) > s.limit {
				return 0, errors.New("provider frame exceeds host limit")
			}
			s.frame = append(s.frame, part...)
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if err != nil {
				return 0, err
			}
			break
		}
	}
	n := copy(b, s.frame)
	s.frame = s.frame[n:]
	return n, nil
}
func (s *ownedStream) Write(b []byte) (int, error) {
	if len(b) > s.limit {
		return 0, errors.New("provider control frame exceeds host limit")
	}
	if err := s.in.SetWriteDeadline(time.Now().Add(s.write)); err != nil {
		return 0, err
	}
	return s.in.Write(b)
}
func (s *ownedStream) Close() error {
	s.once.Do(func() { _ = s.in.Close(); _ = s.out.Close(); _ = s.lifetime.Close() })
	return nil
}
