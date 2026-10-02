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
	Startup, Write, Grace                                   time.Duration
}

func DefaultLimits() Limits {
	return Limits{Launches: 64, FrameBytes: 1 << 20, Events: 256, Requests: 32, DiagnosticBytes: 8192, Startup: 10 * time.Second, Write: time.Second, Grace: 2 * time.Second}
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
	if instance == "" || supervisor.Path == "" || tx.Reserve == nil || tx.Commit == nil || tx.Current == nil {
		return nil, errors.New("incomplete process host configuration")
	}
	if limits.Launches < 1 || limits.FrameBytes < 256 || limits.Events < 2 || limits.Requests < 1 || limits.DiagnosticBytes < 1 || limits.Startup <= 0 || limits.Write <= 0 || limits.Grace <= 0 || limits.Grace > time.Minute {
		return nil, errors.New("invalid process limits")
	}
	supervisor.Args = slices.Clone(supervisor.Args)
	supervisor.Env = slices.Clone(supervisor.Env)
	return &Host{instance: instance, supervisor: supervisor, tx: tx, limits: limits, operations: make(map[string]*Handle), panes: make(map[string]*Handle)}, nil
}

// Launch fixes one operation's exact command and binding. Retrying with changed
// parameters is refused even if the earlier launch has already exited.
type Launch struct {
	Binding Binding
	Command Command
}

// Handle is tied to one owned supervisor/child pair and cannot adopt a PID.
type Handle struct {
	host           *Host
	launch         Launch
	mu             sync.Mutex
	state          string
	session        string
	connection     string
	turn           string
	interrupt      string
	interruptAck   bool
	usedTurns      map[string]bool
	requests       map[string]Request
	usedRequests   map[string]bool
	seq            uint64
	droppedThrough uint64
	events         []Event
	critical       []Event
	diagnostics    []byte
	failure        string
	exit           *Exit
	pid            int
	supervisorPID  int
	stdin          *os.File
	lifetime       *os.File
	ready          chan struct{}
	done           chan struct{}
	spawnErr       error
	stopOnce       sync.Once
}

// Snapshot is a bounded resynchronization view. Turn results do not set Exit.
type Snapshot struct {
	Binding                                   Binding
	State, Session, Connection, Turn, Failure string
	PID, SupervisorPID                        int
	Sequence                                  uint64
	Pending                                   []Request
	Diagnostic                                string
	Exit                                      *Exit
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
	if !launch.Binding.valid(h.instance) || launch.Command.Path == "" || launch.Command.Env == nil {
		return nil, errors.New("invalid launch or implicit environment")
	}
	h.mu.Lock()
	if old := h.operations[launch.Binding.Operation]; old != nil {
		h.mu.Unlock()
		if !reflect.DeepEqual(old.launch, launch) {
			return nil, ErrStale
		}
		select {
		case <-old.ready:
			return old, old.spawnErr
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if h.panes[launch.Binding.Pane] != nil || len(h.operations) >= h.limits.Launches {
		h.mu.Unlock()
		return nil, ErrBusy
	}
	// Clone caller-owned slices before the launch can race a caller mutation.
	launch.Command.Args = slices.Clone(launch.Command.Args)
	launch.Command.Env = slices.Clone(launch.Command.Env)
	p := &Handle{host: h, launch: launch, state: "starting", connection: launch.Binding.Operation, ready: make(chan struct{}), done: make(chan struct{}), usedTurns: make(map[string]bool), requests: make(map[string]Request), usedRequests: make(map[string]bool)}
	h.operations[launch.Binding.Operation], h.panes[launch.Binding.Pane] = p, p
	h.mu.Unlock()
	reserveCtx, cancelReserve := context.WithTimeout(ctx, h.limits.Startup)
	err := h.tx.Reserve(reserveCtx, launch.Binding)
	cancelReserve()
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = p.spawn(ctx)
	}
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

func (p *Handle) spawn(ctx context.Context) error {
	// os.File pipes permit bounded write deadlines and explicit close independent
	// of exec.Wait; descendants holding an output FD cannot hold Wait hostage.
	inR, inW, err := os.Pipe()
	if err != nil {
		return err
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		return err
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return err
	}
	lifeR, lifeW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
		return err
	}
	specR, specW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
		lifeR.Close()
		lifeW.Close()
		return err
	}
	statusR, statusW, err := os.Pipe()
	if err != nil {
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		errR.Close()
		errW.Close()
		lifeR.Close()
		lifeW.Close()
		specR.Close()
		specW.Close()
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
	go func() { defer close(outputDone); p.readOutput(outR) }()
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
	timer := time.NewTimer(2 * p.host.limits.Grace)
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
	waitErr := cmd.Wait()
	if waitErr != nil {
		_ = stdout.Close()
		_ = stderr.Close()
	}
	_ = p.stdin.Close()
	_ = p.lifetime.Close()
	<-outputDone
	<-diagnosticDone
	_ = stdout.Close()
	_ = stderr.Close()
	p.mu.Lock()
	p.expireLocked()
	if actual != nil && waitErr == nil {
		p.exit, p.state = actual, "exited"
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
		if p.state != "exited" && p.state != "unknown" {
			p.state = "stopping"
		}
		p.expireLocked()
		if p.lifetime != nil {
			_ = p.lifetime.Close()
		}
		if p.stdin != nil {
			_ = p.stdin.Close()
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
	s := Snapshot{Binding: p.launch.Binding, State: p.state, Session: p.session, Connection: p.connection, Turn: p.turn, PID: p.pid, SupervisorPID: p.supervisorPID, Sequence: p.seq, Failure: p.failure, Diagnostic: string(p.diagnostics)}
	if p.exit != nil {
		e := *p.exit
		s.Exit = &e
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
	if len(p.events) > 0 && p.droppedThrough > after {
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

func (p *Handle) emitLocked(kind string, raw []byte, request *Request) {
	p.seq++
	event := Event{Binding: p.launch.Binding, Session: p.session, Connection: p.connection, Turn: p.turn, Sequence: p.seq, Kind: kind, Raw: bytes.Clone(raw), Request: request, Exit: p.exit}
	if kind != "output" && kind != "provider-event" {
		p.critical = append(p.critical, event)
		return
	}
	if len(p.events) == p.host.limits.Events {
		p.droppedThrough = p.events[0].Sequence
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
		p.mu.Lock()
		closed := p.state == "exited" || p.state == "stopping" || p.state == "unknown"
		p.mu.Unlock()
		if !closed {
			p.protocolFailure(err)
		}
		return
	}
	// EOF ends the control connection, not the process. Stop and actual Wait
	// determine the exit; this path never manufactures success.
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
