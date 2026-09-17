package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/internal/core/notify"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/web"
)

// Change signals for the event stream.
//
// Every projmux producer -- the CLI, hook ingest, the usage adapters --
// finishes by writing a file, so a cheap stat of that file is the trigger and
// the expensive read happens only when it moved. The Registry has no revision
// counter; its file identity changes on every atomic rewrite, which is the
// same probe the Claude endpoint uses to notice Registry writes.
//
// Some changes never touch a file: a tmux pane dying before its exit hook
// runs, or host load. Those topics also tick, and the stream's digest keeps a
// tick that changed nothing off the wire.

var _ web.Watcher = (*webBackend)(nil)

const (
	webStatInterval  = 150 * time.Millisecond
	webGraphFallback = 2 * time.Second
	webSystemTick    = 2 * time.Second
)

func (b *webBackend) Changes(ctx context.Context, topic string) (<-chan struct{}, error) {
	paths, err := b.statePaths()
	if err != nil {
		return nil, err
	}
	switch topic {
	case web.TopicGraph:
		store := intmetadata.NewDefaultStore(paths)
		identity := func() string {
			id, err := store.RegistryFileIdentity()
			if err != nil {
				return "missing"
			}
			return fmt.Sprint(id)
		}
		return webPoll(ctx, webStatInterval, webGraphFallback, identity), nil
	case web.TopicNotifications:
		file := filepath.Join(paths.StateDir, notify.NotifyFileName)
		changes := webPoll(ctx, webStatInterval, 0, func() string { return webFileMark(file) })
		// The queue also publishes an explicit refresh on every producer
		// write, which arrives sooner than the next stat.
		pushed, err := newNotifyQueueRefreshTransport(paths.StateDir).Subscribe(ctx)
		if err != nil {
			return changes, nil
		}
		return webMerge(ctx, changes, pushed), nil
	case web.TopicUsage:
		// The Usage route reads through usagecmd, so the watcher takes the
		// cache file from the same resolver instead of rebuilding the path.
		file, err := usagecmd.New(nil).SnapshotCacheFile()
		if err != nil {
			return nil, err
		}
		return webPoll(ctx, time.Second, 0, func() string { return webFileMark(file) }), nil
	case web.TopicSystem:
		return webPoll(ctx, webSystemTick, webSystemTick, func() string { return "" }), nil
	}
	return nil, web.InvalidRequest("unknown topic " + topic)
}

// webFileMark is a file's stat identity, or "missing".
func webFileMark(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "missing"
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}

// webPoll signals when mark changes, checked every interval, and also every
// fallback when fallback is positive.
func webPoll(ctx context.Context, interval, fallback time.Duration, mark func() string) <-chan struct{} {
	out := make(chan struct{}, 1)
	go func() {
		defer close(out)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		last := mark()
		lastSignal := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				current := mark()
				due := fallback > 0 && now.Sub(lastSignal) >= fallback
				if current == last && !due {
					continue
				}
				last = current
				lastSignal = now
				select {
				case out <- struct{}{}:
				default: // a signal is already pending; one is enough
				}
			}
		}
	}()
	return out
}

func webMerge(ctx context.Context, a, b <-chan struct{}) <-chan struct{} {
	out := make(chan struct{}, 1)
	forward := func(in <-chan struct{}) {
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-in:
				if !ok {
					return
				}
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}
	go forward(a)
	go forward(b)
	return out
}
