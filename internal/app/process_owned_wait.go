package app

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"

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
