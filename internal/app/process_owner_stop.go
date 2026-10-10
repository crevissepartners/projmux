package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	if r.recorded {
		r.mu.Unlock()
		return
	}
	r.recorded = true
	ppid := os.Getppid()
	comm := processOwnerParentComm(ppid)
	r.journal.RecordOwnerStop(diagnostics.OwnerStopRecord{Reason: reason, AgentUID: r.binding.Agent, PaneUID: r.binding.Pane, Generation: r.binding.Generation, OwnerPID: os.Getpid(), OwnerPPID: ppid, ParentComm: comm})
	stderr := r.stderr
	line := fmt.Sprintf("agent owner stop: reason=%s agent=uid:%s pane=uid:%s generation=%s owner_pid=%d owner_ppid=%d parent_comm=%s\n", reason, r.binding.Agent, r.binding.Pane, r.binding.Generation, os.Getpid(), ppid, comm)
	r.mu.Unlock()
	processWriteOwnerStopStderr(stderr, line)
}

// A holder may leave stderr as an unread, full pipe. Give the supplemental
// line a short completion budget so it cannot prevent provider Stop. There
// is at most one write goroutine per owner generation; a stuck writer lasts
// only until its reader drains/closes or the owner process exits.
func processWriteOwnerStopStderr(stderr io.Writer, line string) {
	if stderr == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.WriteString(stderr, line)
		close(done)
	}()
	timer := time.NewTimer(25 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
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
