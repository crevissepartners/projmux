//go:build linux

package tmux

import (
	"context"

	"github.com/crevissepartners/projmux/internal/core/resources"
	"github.com/crevissepartners/projmux/internal/integrations/procfsresources"
)

// CollectResourceSnapshot is the single Phase 1 consumer seam. It reads the
// tmux topology, performs one procfs scan, and returns both the immutable view
// and the current sample required for a later CPU delta. No telemetry is
// persisted. The collector is Linux-only and unsupported platforms expose no
// fake-zero method.
func (c *Client) CollectResourceSnapshot(ctx context.Context, previous *resources.Sample) (resources.Snapshot, resources.Sample, error) {
	inventory, err := c.ListResourcePanes(ctx)
	if err != nil {
		return resources.Snapshot{}, resources.Sample{}, err
	}
	snapshot, current := (procfsresources.Collector{}).Collect(ctx, inventory, previous)
	return snapshot, current, nil
}
