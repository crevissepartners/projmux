package app

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebPollSignalsOnlyWhenTheMarkMoves(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mark atomic.Value
	mark.Store("a")
	changes := webPoll(ctx, 5*time.Millisecond, 0, func() string { return mark.Load().(string) })

	select {
	case <-changes:
		t.Fatal("signalled with no change")
	case <-time.After(50 * time.Millisecond):
	}
	mark.Store("b")
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("no signal after the mark moved")
	}
	cancel()
	for range changes {
	}
}

func TestWebPollFallbackTicksWithoutAChange(t *testing.T) {
	ctx := t.Context()
	changes := webPoll(ctx, 5*time.Millisecond, 20*time.Millisecond, func() string { return "same" })
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("fallback never signalled")
	}
}

func TestWebFileMarkFollowsRewrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify.json")
	if webFileMark(path) != "missing" {
		t.Fatal("a missing file has a mark")
	}
	if err := os.WriteFile(path, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := webFileMark(path)
	if err := os.WriteFile(path, []byte(`[{"id":"x"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if webFileMark(path) == first {
		t.Fatal("a rewrite of a different size kept the same mark")
	}
}
