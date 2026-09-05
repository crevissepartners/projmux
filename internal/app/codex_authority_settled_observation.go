package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// How one authority observation stood with respect to the per-Pane fence the
// writer holds while it publishes (authority, epoch, reason) as three separate
// tmux set-option calls.
//
// The three are not degrees of health. They say which question the observation
// is allowed to answer: only a settled read may be judged for coherence, a
// contended read caught a transition in progress and says nothing, and an
// unfenced Pane has never had a transition published under a fence at all —
// which on a machine that installed the fenced build means the process writing
// that Pane predates it.
const (
	codexAuthorityFenceSettled   = "settled"
	codexAuthorityFenceContended = "contended"
	codexAuthorityFenceUnfenced  = "unfenced"
	// codexAuthorityFenceUnavailable is a fence this reader could not evaluate,
	// such as an unresolvable state directory. It is distinct from unfenced so
	// that a reader failure is never reported as a producer fact.
	codexAuthorityFenceUnavailable = "unavailable"
)

// codexAuthorityObservationBudget bounds how long one diagnostics observation
// waits for a transition in flight to complete.
//
// It is short because a diagnostics read must not stall behind a wedged writer,
// and it does not need to be long: the writer holds the fence across three tmux
// calls, so a transition that has not settled inside this budget is not a
// transition this snapshot was going to describe anyway.
const codexAuthorityObservationBudget = 250 * time.Millisecond

// codexAuthorityObservationPoll is how often the shared acquisition retries
// inside that budget.
const codexAuthorityObservationPoll = 10 * time.Millisecond

// codexAuthorityFenceObserver reports how a Pane's fence stood and releases it.
// It is the seam a test drives to produce one exact interleaving instead of
// racing for it.
type codexAuthorityFenceObserver func(context.Context, string) (string, func())

// defaultSettledCodexLifecycleAuthorityLookup reads each Pane's authority
// triple under the writer's own fence.
//
// Doctor's earlier reader took the triple with a plain tmux read. That is the
// same unfenced sample C-5 removed from the admission path: with a control
// plane cycling once a second it will eventually land between two of the three
// writes and render a pairing that no completed transition produced. Reporting
// that as a fault would train an operator to ignore the row, and reporting it
// as health would hide a producer that really did stop fencing. Taking the
// fence is what makes the difference decidable.
func defaultSettledCodexLifecycleAuthorityLookup(stateDir string) codexLifecycleAuthorityLookup {
	runner := inttmux.ExecRunner{}
	observe := defaultCodexAuthorityFenceObserver(stateDir)
	return func(paneUID string) codexLifecycleAuthorityDiagnostic {
		ctx, cancel := context.WithTimeout(context.Background(), codexAuthorityObservationBudget)
		defer cancel()
		return observeSettledCodexLifecycleAuthority(ctx, runner, observe, paneUID)
	}
}

// observeSettledCodexLifecycleAuthority samples one Pane and classifies the
// sample.
func observeSettledCodexLifecycleAuthority(
	ctx context.Context,
	runner tmuxRunner,
	observe codexAuthorityFenceObserver,
	paneUID string,
) codexLifecycleAuthorityDiagnostic {
	fence := codexAuthorityFenceUnavailable
	var release func()
	if observe != nil {
		fence, release = observe(ctx, paneUID)
	}
	if release != nil {
		defer release()
	}
	diagnostic := observeCodexLifecycleAuthority(ctx, runner, paneUID)
	diagnostic.Fence = fence
	if fence == codexAuthorityFenceSettled {
		diagnostic.Torn = codexAuthorityTripleTorn(diagnostic.Source, diagnostic.Epoch)
	}
	return diagnostic
}

// codexAuthorityTripleTorn reports whether an authority and epoch pairing is
// one no completed transition produces.
//
// A control-plane or invalidating authority names a live native epoch, so an
// empty epoch beside it is the state between the authority write and the epoch
// write. A hook authority is published with the epoch cleared, so an epoch
// beside it is the same interval seen from the other direction. Both are the
// exact shape that refused a live Pane its turn before C-5 fenced the read.
func codexAuthorityTripleTorn(source, epoch string) bool {
	epoch = strings.TrimSpace(epoch)
	switch source {
	case codexAuthorityControlPlane, codexAuthorityInvalidating:
		return epoch == ""
	case codexAuthorityHook:
		return epoch != ""
	default:
		return false
	}
}

// defaultCodexAuthorityFenceObserver takes the writer's fence in shared mode
// without creating it.
//
// Two properties are deliberate. It never creates the file or its directory, so
// asking whether a Pane's transitions are fenced cannot manufacture the answer;
// and it takes a shared lock rather than the writer's exclusive one, so two
// concurrent diagnostics reads do not serialize behind each other while both
// still exclude a writer mid-transition.
func defaultCodexAuthorityFenceObserver(stateDir string) codexAuthorityFenceObserver {
	return func(ctx context.Context, paneUID string) (string, func()) {
		if strings.TrimSpace(stateDir) == "" {
			return codexAuthorityFenceUnavailable, nil
		}
		path, err := codexAuthorityFenceFileIn(stateDir, paneUID)
		if err != nil {
			return codexAuthorityFenceUnavailable, nil
		}
		// #nosec G304 -- codexAuthorityFenceFileIn returns the private state
		// directory plus a digest of the Registry-authenticated Pane uid.
		file, err := os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			return codexAuthorityFenceUnfenced, nil
		}
		if err != nil {
			return codexAuthorityFenceUnavailable, nil
		}
		if !acquireSharedFence(ctx, file) {
			_ = file.Close()
			return codexAuthorityFenceContended, nil
		}
		var once sync.Once
		return codexAuthorityFenceSettled, func() {
			once.Do(func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			})
		}
	}
}

// acquireSharedFence polls for the shared lock inside the caller's deadline.
func acquireSharedFence(ctx context.Context, file *os.File) bool {
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH|syscall.LOCK_NB)
		if err == nil {
			return true
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return false
		}
		timer := time.NewTimer(codexAuthorityObservationPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}
