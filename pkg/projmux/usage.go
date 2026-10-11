package projmux

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/crevissepartners/projmux/internal/core/usage"
	savedusage "github.com/crevissepartners/projmux/internal/services/usage"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// GetUsage reads the statusbar's saved snapshot state, never a provider API.
// Existing private-permission repair remains; snapshot bytes and collection
// timestamps are never written. Results are independent deep copies.
func (c *Client) GetUsage(ctx context.Context) (core.UsageCache, error) {
	if err := ctx.Err(); err != nil {
		return core.UsageCache{}, err
	}
	if c.pathsErr != nil {
		return core.UsageCache{}, c.pathsErr
	}
	paths, err := c.instructions.Paths()
	if err != nil {
		return core.UsageCache{}, err
	}
	models, unsupported := savedusage.ProviderScope(paths)
	store := usage.NewStore(c.usageDir)
	result, err := savedusage.Cached(ctx, store.LoadState, store.FilePath(), models, unsupported)
	if err != nil {
		return core.UsageCache{}, err
	}
	out := core.UsageCache{CacheModTime: result.ModTime, LastCollect: result.State.LastCollect, Unsupported: make([]core.UnsupportedUsageProvider, len(result.Unsupported))}
	for i, v := range result.Unsupported {
		out.Unsupported[i] = core.UnsupportedUsageProvider(v)
	}
	if err := copyUsage(usage.SortedSnapshots(result.State.Snapshots), &out.Snapshots); err != nil {
		return core.UsageCache{}, err
	}
	if err := copyUsage(result.State.Backoff, &out.Backoff); err != nil {
		return core.UsageCache{}, err
	}
	return out, nil
}

// GetUsageHistory reads the same retained metric history as agent usage
// --history. It retains the existing reader's lock and permission repair;
// no metric observations, segments or collection metadata are added.
func (c *Client) GetUsageHistory(ctx context.Context, f core.UsageHistoryFilter) ([]core.UsageMetricPoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.pathsErr != nil {
		return nil, c.pathsErr
	}
	points, err := savedusage.History(ctx, c.usageDir, usage.HistoryFilter{Provider: f.Provider, Window: f.Window, Metric: f.Metric, Now: f.Now})
	if err != nil {
		return nil, err
	}
	var out []core.UsageMetricPoint
	if err := copyUsage(points, &out); err != nil {
		return nil, err
	}
	return out, nil
}
func copyUsage(source, target any) error {
	b, err := json.Marshal(source)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}

// defaultUsageDir captures the CLI override at construction, unless an explicit
// StateDir selects that client's independent root.
func defaultUsageDir(stateDir, override string) string {
	if override != "" {
		return override
	}
	return filepath.Join(stateDir, "usage")
}
