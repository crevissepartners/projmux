package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/version"
)

type processStopCause diagnostics.OwnerStopReason

func (r processStopCause) Error() string { return string(r) }

type processOwnerStopKey struct{}
type processForegroundCauseKey struct{}

// One recorder is shared by the Wait tail and the admitted control socket.
// The first shutdown path wins; subsequent Stop calls do not rewrite its cause.
type processOwnerStopRecorder struct {
	mu       sync.Mutex
	recorded bool
	binding  processhost.Binding
	journal  *diagnostics.LifecycleRecorder
	stderr   io.Writer
}

func newProcessOwnerStop(ctx context.Context, path string, binding processhost.Binding) (context.Context, *processOwnerStopRecorder) {
	logPath := filepath.Join(filepath.Dir(filepath.Dir(path)), diagnostics.LogDirName, diagnostics.LogFileName)
	r := &processOwnerStopRecorder{binding: binding, journal: diagnostics.NewLifecycleRecorder(diagnostics.NewStore(logPath), binding.Operation, version.String(), diagnostics.MuxBackend()), stderr: os.Stderr}
	return context.WithValue(ctx, processOwnerStopKey{}, r), r
}

func processStopRecorder(ctx context.Context) *processOwnerStopRecorder {
	r, _ := ctx.Value(processOwnerStopKey{}).(*processOwnerStopRecorder)
	return r
}

func (r *processOwnerStopRecorder) setStderr(stderr io.Writer) {
	if r == nil || stderr == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stderr = stderr
}

func (r *processOwnerStopRecorder) record(reason diagnostics.OwnerStopReason) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recorded {
		return
	}
	r.recorded = true
	ppid := os.Getppid()
	comm := processOwnerParentComm(ppid)
	r.journal.RecordOwnerStop(diagnostics.OwnerStopRecord{Reason: reason, AgentUID: r.binding.Agent, PaneUID: r.binding.Pane, Generation: r.binding.Generation, OwnerPID: os.Getpid(), OwnerPPID: ppid, ParentComm: comm})
	if r.stderr != nil {
		_, _ = fmt.Fprintf(r.stderr, "agent owner stop: reason=%s agent=uid:%s pane=uid:%s generation=%s owner_pid=%d owner_ppid=%d parent_comm=%s\n", reason, r.binding.Agent, r.binding.Pane, r.binding.Generation, os.Getpid(), ppid, comm)
	}
}

// /proc is optional (absent on macOS). No command line or environment is read.
func processOwnerParentComm(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	value := strings.TrimSpace(string(data))
	if !diagnostics.ValidOwnerParentComm(value) {
		return ""
	}
	return value
}

func processContextStopReason(ctx context.Context) diagnostics.OwnerStopReason {
	if cause, ok := context.Cause(ctx).(processStopCause); ok {
		return diagnostics.OwnerStopReason(cause)
	}
	if foreground, ok := ctx.Value(processForegroundCauseKey{}).(context.Context); ok {
		if cause, ok := context.Cause(foreground).(processStopCause); ok {
			return diagnostics.OwnerStopReason(cause)
		}
	}
	return diagnostics.OwnerStopOther
}

func processStartStdinEOF(ctx context.Context, end context.CancelFunc) {
	if lifetime, ok := ctx.Value(processOwnerLifetimeKey{}).(processOwnerLifetime); ok && lifetime.stdinEOF != nil {
		lifetime.stdinEOF(end)
		return
	}
	processStdinEOFTrigger(end)
}
