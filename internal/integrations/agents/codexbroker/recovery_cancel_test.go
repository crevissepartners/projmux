package codexbroker

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestEnsureCancellationDuringStartupLockNeverLaunches(t *testing.T) {
	discovery := newRuntimeDiscovery(t)
	if err := prepareDiscoveryDir(discovery); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(discovery.lockPath(), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	held, err := tryLockExclusive(lock)
	if err != nil || !held {
		t.Fatalf("lock=%t err=%v", held, err)
	}
	defer func() { _ = unlockFile(lock) }()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	var launches atomic.Int32
	done := make(chan error, 1)
	go func() {
		conn, err := Ensure(ctx, discovery, EnsureConfig{Launch: func(ctx context.Context) error { launches.Add(1); return ctx.Err() }})
		if conn != nil {
			_ = conn.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) || launches.Load() != 0 {
			t.Fatalf("cancelled startup err=%v launches=%d", err, launches.Load())
		}
	case <-time.After(time.Second):
		_ = unlockFile(lock)
		<-done
		t.Fatal("cancelled Ensure stayed behind the startup lock")
	}
}
