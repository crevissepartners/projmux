package usagecmd

import (
	"context"
	"errors"
	"os"
	"testing"
	"testing/synctest"
	"time"

	"github.com/crevissepartners/projmux/internal/core/usage"
	codexadapter "github.com/crevissepartners/projmux/internal/core/usage/adapters/codex"
)

func nativeWatcherRecoveryCommand(t *testing.T) (*Command, string) {
	t.Helper()
	stateDir := t.TempDir()
	command := New(time.Now)
	command.lookupEnv = func(name string) string {
		if name == StateDirEnvVar {
			return stateDir
		}
		return ""
	}
	command.executableFn = func() (string, error) { return "/test/projmux", nil }
	if err := touchNativeWatcherMarker(nativeWatcherPath(stateDir, nativeWatcherDemandName), time.Now()); err != nil {
		t.Fatal(err)
	}
	return command, stateDir
}

func nativeWatcherRecoveryRows(pct float64) []usage.Snapshot {
	return []usage.Snapshot{{Model: codexadapter.Name, Window: usage.Window5h, Pct: pct, UpdatedAt: time.Now(), Source: usage.SourceAppServer, RateLimit: &usage.RateLimitMetadata{Slot: "primary"}}}
}

func requireNativeWatcherReleased(t *testing.T, stateDir string, failed bool) {
	t.Helper()
	if _, err := os.Lstat(nativeWatcherPath(stateDir, nativeWatcherHeartbeatName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("heartbeat remains: %v", err)
	}
	_, err := os.Lstat(nativeWatcherPath(stateDir, nativeWatcherFailureName))
	if failed && err != nil || !failed && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failure marker failed=%v err=%v", failed, err)
	}
	release, acquired, err := acquireNativeWatcherLease(nativeWatcherPath(stateDir, nativeWatcherLeaseName))
	if err != nil || !acquired {
		t.Fatalf("lease retained: acquired=%v err=%v", acquired, err)
	}
	release()
}

func TestNativeWatcherProbeFailureUsesExistingBackoff(t *testing.T) {
	for _, rawDeadline := range []bool{false, true} {
		t.Run(map[bool]string{false: "adapter-timeout", true: "request-deadline"}[rawDeadline], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				command, stateDir := nativeWatcherRecoveryCommand(t)
				cache := codexadapter.NewNativeEventCache(stateDir, time.Now)
				var oldPublish func([]usage.Snapshot) error
				command.watchNativeRateLimitsFn = func(ctx context.Context, publish func([]usage.Snapshot) error) error {
					oldPublish = publish
					if err := publish(nativeWatcherRecoveryRows(11)); err != nil {
						return err
					}
					time.Sleep(30 * time.Second)
					requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
					defer cancel()
					<-requestCtx.Done()
					if rawDeadline {
						return requestCtx.Err()
					}
					return &usage.StaleReasonError{Reason: usage.ReasonAppServerTimeout, Err: errors.New("watcher read failed")}
				}
				done := make(chan error, 1)
				go func() { done <- command.runNativeWatcherContext(context.Background()) }()
				synctest.Wait()
				// Keep HUD demand alive across the first periodic read.
				for range 3 {
					time.Sleep(10 * time.Second)
					if err := touchNativeWatcherMarker(nativeWatcherPath(stateDir, nativeWatcherDemandName), time.Now()); err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
				}
				time.Sleep(2 * time.Second)
				if err := <-done; err == nil {
					t.Fatal("probe timeout became a clean stop")
				}
				requireNativeWatcherReleased(t, stateDir, true)
				failedAt := time.Now()
				failure, err := os.Stat(nativeWatcherPath(stateDir, nativeWatcherFailureName))
				if err != nil || !failure.ModTime().Equal(failedAt) {
					t.Fatalf("failure timestamp=%v err=%v", failure, err)
				}
				batch, err := cache.Load()
				if err != nil || len(batch.Snapshots) != 1 || batch.Snapshots[0].Pct != 11 {
					t.Fatalf("last good=%#v err=%v", batch, err)
				}
				starts := 0
				restarted := make(chan error, 1)
				command.watchNativeRateLimitsFn = func(ctx context.Context, publish func([]usage.Snapshot) error) error {
					if err := publish(nativeWatcherRecoveryRows(44)); err != nil {
						return err
					}
					<-ctx.Done()
					return ctx.Err()
				}
				command.startNativeWatcherFn = func(string) error {
					starts++
					go func() { restarted <- command.runNativeWatcherContext(context.Background()) }()
					return nil
				}
				for _, delay := range []time.Duration{0, 29 * time.Second, time.Second} {
					time.Sleep(delay)
					command.ensureNativeWatcher(stateDir)
					if starts != 0 {
						t.Fatalf("restart during backoff at %v", time.Since(failedAt))
					}
				}
				time.Sleep(time.Nanosecond)
				if starts != 0 {
					t.Fatal("watcher relaunched without demand")
				}
				command.ensureNativeWatcher(stateDir)
				synctest.Wait()
				if starts != 1 {
					t.Fatalf("next-demand starts=%d", starts)
				}
				command.ensureNativeWatcher(stateDir)
				if starts != 1 {
					t.Fatal("fresh heartbeat launched duplicate watcher")
				}
				if err := command.runNativeWatcherContext(context.Background()); err != nil {
					t.Fatal(err)
				}
				if err := oldPublish(nativeWatcherRecoveryRows(99)); !errors.Is(err, context.Canceled) {
					t.Fatalf("retired publisher=%v", err)
				}
				batch, err = cache.Load()
				if err != nil || batch.Snapshots[0].Pct != 44 || batch.Snapshots[0].Source != usage.SourceAppServer || !batch.ObservedAt.After(failedAt) {
					t.Fatalf("replacement batch=%#v err=%v", batch, err)
				}
				time.Sleep(16 * time.Second)
				if err := <-restarted; err != nil {
					t.Fatal(err)
				}
				requireNativeWatcherReleased(t, stateDir, false)
			})
		})
	}
}

func TestNativeWatcherDemandExpiryCancelsPendingProbe(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		command, stateDir := nativeWatcherRecoveryCommand(t)
		started := time.Now()
		var stopCause error
		command.watchNativeRateLimitsFn = func(ctx context.Context, publish func([]usage.Snapshot) error) error {
			if err := publish(nativeWatcherRecoveryRows(11)); err != nil {
				return err
			}
			time.Sleep(30 * time.Second)
			probe, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			<-probe.Done()
			stopCause = probe.Err()
			// A late result on a canceled demand cannot touch the cache.
			return publish(nativeWatcherRecoveryRows(99))
		}
		done := make(chan error, 1)
		go func() { done <- command.runNativeWatcherContext(context.Background()) }()
		synctest.Wait()
		time.Sleep(15 * time.Second)
		if err := touchNativeWatcherMarker(nativeWatcherPath(stateDir, nativeWatcherDemandName), time.Now()); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		time.Sleep(15 * time.Second)
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("stopped before TTL: %v", err)
		default:
		}
		time.Sleep(time.Second)
		if err := <-done; err != nil {
			t.Fatalf("demand expiry created failure: %v", err)
		}
		if !errors.Is(stopCause, context.Canceled) || time.Since(started) != 31*time.Second {
			t.Fatalf("stop=%v elapsed=%v", stopCause, time.Since(started))
		}
		requireNativeWatcherReleased(t, stateDir, false)
		batch, err := codexadapter.NewNativeEventCache(stateDir, time.Now).Load()
		if err != nil || batch.Snapshots[0].Pct != 11 {
			t.Fatalf("late publication=%#v err=%v", batch, err)
		}
	})
}
