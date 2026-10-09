package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// processOwnerTrigger ends an owned lifetime by calling end once its condition
// holds. It must return promptly; a blocking watch runs in its own goroutine.
// A nil trigger adds nothing: the caller already watches, or the lifetime is a
// typed consumer's context.
type processOwnerTrigger func(end context.CancelFunc)

// processForegroundLifetime is the public CLI owner lifetime: SIGINT or
// SIGTERM ends it.
func processForegroundLifetime() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

// processStdinEOFTrigger is the public CLI trigger: EOF is owner shutdown.
// Provider content never uses the owner's stdout.
func processStdinEOFTrigger(end context.CancelFunc) {
	go func() { _, _ = io.Copy(io.Discard, os.Stdin); end() }()
}

// processGenerationHandle is an owned Handle that asks its host's
// Transactions.Current whether the Registry still holds its generation.
type processGenerationHandle interface {
	CheckGeneration(context.Context, processhost.Binding) error
}

// An owned Wait first checks its generation this long after the ownership line,
// then doubles the interval up to processGenerationCheckMax.
const (
	processGenerationCheckFirst = 30 * time.Second
	processGenerationCheckMax   = 2 * time.Minute
)

// errProcessGenerationAbandoned ends an owned Wait whose generation the
// Registry no longer holds: its provider was stopped and no Wait was recorded.
var errProcessGenerationAbandoned = errors.New("the Registry no longer holds this process generation; its provider was stopped and no Wait was recorded")

// processGenerationSchedule spaces an owned Wait's generation checks.
type processGenerationSchedule struct{ first, max time.Duration }

func (s processGenerationSchedule) next(previous time.Duration) time.Duration {
	first, most := s.first, s.max
	if first <= 0 {
		first, most = processGenerationCheckFirst, processGenerationCheckMax
	}
	if previous <= 0 {
		return first
	}
	return min(2*previous, max(most, first))
}

// checkGeneration reports processhost.ErrStale only when the Registry no longer
// holds this generation; a read failure decides nothing. A generation that is
// not abandoned keeps its lease directory younger than /tmp aging.
func (r *processAgentCreateResult) checkGeneration(ctx context.Context, handle processGenerationHandle) error {
	err := handle.CheckGeneration(ctx, r.Binding)
	if errors.Is(err, processhost.ErrStale) {
		return err
	}
	touchProcessLeaseDir(claudeActivationLeaseDir(r.registryPath, r.Binding.Pane, r.Binding.Generation), time.Now())
	return err
}

// touchProcessLeaseDir renews a live generation's lease directory and the
// sockets in it, so tmpfiles aging (`Q /tmp ... 10d`) never removes them under
// a long-lived owner. Only a private directory of this uid is touched; a
// missing one is not created.
func touchProcessLeaseDir(dir string, now time.Time) {
	if !privateClaudeLeaseDir(dir) {
		return
	}
	entries, _ := os.ReadDir(dir)
	for _, entry := range entries {
		if entry.Type()&fs.ModeSocket != 0 {
			_ = os.Chtimes(filepath.Join(dir, entry.Name()), now, now)
		}
	}
	_ = os.Chtimes(dir, now, now)
}

// processOwnedWait is the one owned Wait tail of a process generation after
// its ownership line: create, resume, deferred claim, relaunch, and deferred
// relaunch. Snapshots are synchronized until the lifetime ends, the actual
// Wait is recorded, and controls and attention are closed before the Wait
// exit is returned.
type processOwnedWait struct {
	owner     *processAgentCreateResult
	binding   processhost.Binding
	changed   func(processhost.Snapshot) error
	controls  func(context.Context) error
	attention func() error
	// endedElsewhere lets the Registry explain a failed end another process
	// took over before fail applies the path's own guidance.
	endedElsewhere bool
	// fail maps a failed end; nil returns the joined error unchanged.
	fail func(error) error
}

// run starts trigger with end, then owns the Wait until ctx ends or the child
// exits.
func (w processOwnedWait) run(ctx context.Context, end context.CancelFunc, trigger processOwnerTrigger, stderr io.Writer) error {
	if trigger != nil {
		trigger(end)
	}
	snapshot, waitErr := w.owner.waitProcessAgent(ctx, processSnapshotSynchronizer(w.changed, func(snapshot processhost.Snapshot) error {
		if len(snapshot.Pending) > 0 {
			return w.controls(context.WithoutCancel(ctx))
		}
		return nil
	}))
	// A closed authority still closes answer records and projects termination.
	controlErr := w.controls(context.Background())
	if errors.Is(controlErr, processhost.ErrClosed) || errors.Is(controlErr, processhost.ErrStale) {
		controlErr = nil
	}
	if err := errors.Join(waitErr, controlErr, w.attention()); err != nil {
		if errors.Is(waitErr, errProcessGenerationAbandoned) {
			// Another process owns what follows; this owner's cleanup guidance
			// would act on a generation it no longer holds.
			if ended, ok := processOwnerEnded(w.owner.registryPath, w.binding, false, snapshot, err, stderr); ok {
				return ended
			}
			return fmt.Errorf("agent uid:%s generation %s: %w", w.binding.Agent, w.binding.Generation, err)
		}
		if w.endedElsewhere {
			if ended, ok := processOwnerEnded(w.owner.registryPath, w.binding, w.owner.waitRecorded, snapshot, err, stderr); ok {
				return ended
			}
		}
		if w.fail != nil {
			return w.fail(err)
		}
		return err
	}
	return processWaitExit(snapshot)
}
