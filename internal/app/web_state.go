package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
	"github.com/crevissepartners/projmux/internal/systemstatus"
	"github.com/crevissepartners/projmux/internal/web"
)

// The status reads: the notify queue, cached usage, and host load. Each goes to
// the store projmux itself renders from. None of them collects anything:
// usage in particular is read from the snapshot cache, because collection
// calls provider APIs, and a page refreshing on a timer must never do that.

func (b *webBackend) statePaths() (config.Paths, error) {
	if b.paths != nil {
		return b.paths()
	}
	return config.DefaultPathsFromEnv()
}

func (b *webBackend) Notifications(context.Context) (any, error) {
	paths, err := b.statePaths()
	if err != nil {
		return nil, err
	}
	rows, err := notify.NewDefaultStore(paths).List()
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []notify.Notification{}
	}
	return map[string]any{"items": rows}, nil
}

func (b *webBackend) AckNotification(_ context.Context, id string) (any, error) {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, " \t\r\n") {
		return nil, web.InvalidRequest("notification id is empty or has whitespace")
	}
	paths, err := b.statePaths()
	if err != nil {
		return nil, err
	}
	b.mutations.Lock()
	defer b.mutations.Unlock()
	if err := notify.NewDefaultStore(paths).Ack(id); err != nil {
		if errors.Is(err, notify.ErrNotFound) {
			return nil, web.NotFound("no notification " + id)
		}
		return nil, err
	}
	// `notify ack` does not tell subscribers the queue moved; the status bar
	// and every other open client would otherwise keep showing the row.
	_ = newNotifyQueueRefreshTransport(paths.StateDir).Publish()
	return map[string]any{"id": id, "acked": true}, nil
}

// webUsage is the cached usage the status bar renders.
type webUsage struct {
	Snapshots   any       `json:"snapshots"`
	Unsupported any       `json:"unsupported,omitempty"`
	CachedAt    time.Time `json:"cachedAt,omitzero"`
}

func (b *webBackend) Usage(context.Context) (any, error) {
	command := usagecmd.New(nil)
	state, unsupported, cachedAt, err := command.CachedState()
	if err != nil {
		return nil, err
	}
	snapshots := state.Snapshots
	if snapshots == nil {
		return webUsage{Snapshots: []any{}, Unsupported: unsupported, CachedAt: cachedAt}, nil
	}
	return webUsage{Snapshots: snapshots, Unsupported: unsupported, CachedAt: cachedAt}, nil
}

// webSystem is host load as the status bar shows it. A nil value is a
// platform or read that has no answer, not zero.
type webSystem struct {
	Supported     bool `json:"supported"`
	CPUPercent    *int `json:"cpuPercent"`
	MemoryPercent *int `json:"memoryPercent"`
}

func (b *webBackend) System(context.Context) (any, error) {
	if !systemstatus.Supported() {
		return webSystem{}, nil
	}
	paths, err := b.statePaths()
	if err != nil {
		return nil, err
	}
	metrics := (systemstatus.Sampler{CachePath: paths.LiveResourcesSampleFile()}).Sample()
	return webSystem{Supported: true, CPUPercent: metrics.CPUPercent, MemoryPercent: metrics.MemoryPercent}, nil
}
