package usagecmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
	codexadapter "github.com/crevissepartners/projmux/internal/core/usage/adapters/codex"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

const (
	nativeWatcherInternalFlag  = "--watch-codex-rate-limits"
	nativeWatcherLeaseName     = ".codex-native-rate-limit-watcher.lock"
	nativeWatcherDemandName    = ".codex-native-rate-limit-watcher.demand"
	nativeWatcherHeartbeatName = ".codex-native-rate-limit-watcher.heartbeat"
	nativeWatcherFailureName   = ".codex-native-rate-limit-watcher.failure"

	nativeWatcherHeartbeatEvery = time.Second
	nativeWatcherHeartbeatFresh = 4 * time.Second
	nativeWatcherDemandTTL      = 15 * time.Second
	nativeEventBatchMaxAge      = 30 * time.Second
	nativeWatcherFailureBackoff = 30 * time.Second
)

var errNativeWatcherGoTestExecutable = errors.New("codex native watcher cannot launch a Go test executable")

type nativeWatcherTimings struct {
	heartbeatEvery time.Duration
	heartbeatFresh time.Duration
	demandTTL      time.Duration
	batchMaxAge    time.Duration
	failureBackoff time.Duration
}

func defaultNativeWatcherTimings() nativeWatcherTimings {
	return nativeWatcherTimings{
		heartbeatEvery: nativeWatcherHeartbeatEvery,
		heartbeatFresh: nativeWatcherHeartbeatFresh,
		demandTTL:      nativeWatcherDemandTTL,
		batchMaxAge:    nativeEventBatchMaxAge,
		failureBackoff: nativeWatcherFailureBackoff,
	}
}

func nativeWatcherPath(stateDir, name string) string {
	return filepath.Join(stateDir, name)
}

func touchNativeWatcherMarker(path string, now time.Time) error {
	dir := filepath.Dir(path)
	if err := localstate.EnsurePrivateDir(dir); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("codex native watcher marker is a symlink")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// #nosec G304 -- every caller supplies the resolved usage-state directory joined with a fixed watcher marker name; the private parent and preceding symlink check are enforced here.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, localstate.PrivateFileMode)
	if err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	localstate.RepairPrivateFile(path)
	return os.Chtimes(path, now, now)
}

func nativeWatcherMarkerFresh(path string, now time.Time, maxAge time.Duration) bool {
	if maxAge <= 0 {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	age := now.UTC().Sub(info.ModTime().UTC())
	return age >= -time.Second && age <= maxAge
}

func startNativeWatcherProcess(executable string) error {
	if err := validateNativeWatcherExecutable(executable); err != nil {
		return err
	}
	cmd := exec.Command(executable, "internal", "status", "usage", nativeWatcherInternalFlag)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func validateNativeWatcherExecutable(executable string) error {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		return errors.New("codex native watcher executable is empty")
	}
	name := strings.ToLower(filepath.Base(executable))
	name = strings.TrimSuffix(name, ".exe")
	if strings.HasSuffix(name, ".test") {
		return errNativeWatcherGoTestExecutable
	}
	return nil
}

func (c *Command) ensureNativeWatcher(stateDir string) {
	if strings.TrimSpace(stateDir) == "" {
		return
	}
	now := c.now().UTC()
	_ = touchNativeWatcherMarker(nativeWatcherPath(stateDir, nativeWatcherDemandName), now)
	timings := c.nativeWatcherTimings()
	if nativeWatcherMarkerFresh(nativeWatcherPath(stateDir, nativeWatcherHeartbeatName), now, timings.heartbeatFresh) {
		return
	}
	if nativeWatcherMarkerFresh(nativeWatcherPath(stateDir, nativeWatcherFailureName), now, timings.failureBackoff) {
		return
	}
	executable := c.executableFn
	if executable == nil {
		executable = os.Executable
	}
	path, err := executable()
	if err != nil {
		return
	}
	if validateNativeWatcherExecutable(path) != nil {
		return
	}
	start := c.startNativeWatcherFn
	if start == nil {
		start = startNativeWatcherProcess
	}
	if err := start(path); err != nil {
		_ = touchNativeWatcherMarker(nativeWatcherPath(stateDir, nativeWatcherFailureName), now)
	}
}

func (c *Command) runNativeWatcher() error {
	stateDir, err := c.resolveStateDir()
	if err != nil {
		return err
	}
	release, acquired, err := acquireNativeWatcherLease(nativeWatcherPath(stateDir, nativeWatcherLeaseName))
	if err != nil || !acquired {
		return err
	}
	defer release()
	heartbeatPath := nativeWatcherPath(stateDir, nativeWatcherHeartbeatName)
	defer os.Remove(heartbeatPath)

	timings := c.nativeWatcherTimings()
	now := c.now().UTC()
	if !nativeWatcherMarkerFresh(nativeWatcherPath(stateDir, nativeWatcherDemandName), now, timings.demandTTL) {
		return nil
	}
	if err := touchNativeWatcherMarker(heartbeatPath, now); err != nil {
		return err
	}

	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	watchCtx, cancelWatch := context.WithCancel(signalCtx)
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(timings.heartbeatEvery)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				now := c.now().UTC()
				if !nativeWatcherMarkerFresh(nativeWatcherPath(stateDir, nativeWatcherDemandName), now, timings.demandTTL) {
					cancelWatch()
					return
				}
				if touchNativeWatcherMarker(heartbeatPath, now) != nil {
					cancelWatch()
					return
				}
			}
		}
	}()

	cache := codexadapter.NewNativeEventCache(stateDir, c.now)
	failurePath := nativeWatcherPath(stateDir, nativeWatcherFailureName)
	published := false
	publish := func(snapshots []usage.Snapshot) error {
		if err := cache.Publish(snapshots); err != nil {
			return err
		}
		if !published {
			published = true
			_ = os.Remove(failurePath)
		}
		return nil
	}
	watch := c.watchNativeRateLimitsFn
	if watch == nil {
		adapter := codexadapter.NewReadOnly()
		watch = func(ctx context.Context, publish func([]usage.Snapshot) error) error {
			return adapter.WatchNativeRateLimits(ctx, publish)
		}
	}
	watchErr := watch(watchCtx, publish)
	cancelWatch()
	<-monitorDone
	if errors.Is(watchErr, context.Canceled) || errors.Is(watchErr, context.DeadlineExceeded) {
		return nil
	}
	if watchErr != nil {
		_ = touchNativeWatcherMarker(failurePath, c.now().UTC())
		return errors.New("codex native rate-limit watcher stopped")
	}
	_ = touchNativeWatcherMarker(failurePath, c.now().UTC())
	return errors.New("codex native rate-limit watcher stopped")
}

func (c *Command) nativeWatcherTimings() nativeWatcherTimings {
	timings := c.watcherTimings
	defaults := defaultNativeWatcherTimings()
	if timings.heartbeatEvery <= 0 {
		timings.heartbeatEvery = defaults.heartbeatEvery
	}
	if timings.heartbeatFresh <= 0 {
		timings.heartbeatFresh = defaults.heartbeatFresh
	}
	if timings.demandTTL <= 0 {
		timings.demandTTL = defaults.demandTTL
	}
	if timings.batchMaxAge <= 0 {
		timings.batchMaxAge = defaults.batchMaxAge
	}
	if timings.failureBackoff <= 0 {
		timings.failureBackoff = defaults.failureBackoff
	}
	return timings
}

// applyFreshNativeEventBatch returns accepted=true only after a live watcher
// produced a valid, newer native-only batch. Once accepted, the caller must
// not invoke Codex's native-read/rollout source decision in that refresh even
// if the public store write fails.
func (c *Command) applyFreshNativeEventBatch(manager *usage.Manager, stateDir string) (accepted bool, err error) {
	if manager == nil {
		return false, nil
	}
	now := c.now().UTC()
	timings := c.nativeWatcherTimings()
	if !nativeWatcherMarkerFresh(nativeWatcherPath(stateDir, nativeWatcherHeartbeatName), now, timings.heartbeatFresh) {
		return false, nil
	}
	batch, err := codexadapter.NewNativeEventCache(stateDir, c.now).Load()
	if err != nil {
		return false, nil
	}
	age := now.Sub(batch.ObservedAt)
	if age < -time.Second || age > timings.batchMaxAge {
		return false, nil
	}
	state, err := manager.LoadState()
	if err != nil {
		return false, nil
	}
	latest := time.Time{}
	for _, snapshot := range state.Snapshots {
		if strings.EqualFold(strings.TrimSpace(snapshot.Model), codexadapter.Name) && snapshot.UpdatedAt.After(latest) {
			latest = snapshot.UpdatedAt
		}
	}
	if !batch.ObservedAt.After(latest) {
		return false, nil
	}
	_, err = manager.ApplySnapshots(codexadapter.Name, batch.Snapshots)
	return true, err
}
