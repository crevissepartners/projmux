package app

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/app/usagecmd"
	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/notify"
	coreusage "github.com/crevissepartners/projmux/internal/core/usage"
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

// webUsage is the cached usage the status bar renders: every snapshot, and
// the compact HUD row the bar draws from them.
type webUsage struct {
	Snapshots   []coreusage.Snapshot           `json:"snapshots"`
	HUD         []webUsageCell                 `json:"hud"`
	Unsupported []usagecmd.UnsupportedProvider `json:"unsupported,omitempty"`
	CachedAt    time.Time                      `json:"cachedAt,omitzero"`
}

// webUsageCell is one meter in the bar. Only the rolling 5h and weekly
// windows are drawn there; quota rows repeat the same numbers.
type webUsageCell struct {
	Model  string `json:"model"`
	Window string `json:"window"`
	Pct    int    `json:"pct"`
	Stale  bool   `json:"stale"`
}

func (b *webBackend) Usage(context.Context) (any, error) {
	command := usagecmd.New(nil)
	state, unsupported, cachedAt, err := command.CachedState()
	if err != nil {
		return nil, err
	}
	out := webUsage{Snapshots: state.Snapshots, HUD: []webUsageCell{}, Unsupported: unsupported, CachedAt: cachedAt}
	if out.Snapshots == nil {
		out.Snapshots = []coreusage.Snapshot{}
	}
	for _, snap := range coreusage.SortedSnapshots(state.Snapshots) {
		if snap.Window != coreusage.Window5h && snap.Window != coreusage.WindowWeekly {
			continue
		}
		out.HUD = append(out.HUD, webUsageCell{
			Model:  usagecmd.ModelDisplayLabel(snap.Model),
			Window: string(snap.Window),
			Pct:    int(math.Round(snap.Pct)),
			Stale:  strings.TrimSpace(string(snap.StaleReason)) != "",
		})
	}
	return out, nil
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
