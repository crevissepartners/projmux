package projmux

import (
	"context"
	"encoding/json"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ReadRegistrySnapshot reads the captured Registry once without runtime or
// provider contact, locks, writes, or permission repair. Stored rows (including
// duplicates and Offline Agents) retain their order and values. The returned
// non-nil slices and all nested values belong to the caller.
//
// Cancellation is checked on entry and before returning success; it does not
// interrupt file I/O. A loader or projection failure takes precedence over
// cancellation occurring during that work. Registry failures retain the same
// registry-unavailable refusal and detail as the other resource reads.
func (c *Client) ReadRegistrySnapshot(ctx context.Context) (core.RegistrySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return core.RegistrySnapshot{}, err
	}
	if c.pathsErr != nil {
		return core.RegistrySnapshot{}, resourceRefusal(c.pathsErr)
	}
	registry, err := resources.Load(c.paths)
	if err != nil {
		return core.RegistrySnapshot{}, resourceRefusal(err)
	}
	// Convert the rows directly, never ResourceFor's UID lookup or contextual
	// defaults. The public types mirror stored JSON, and decoding allocates every
	// nested map, slice, and pointer independently of the loaded Registry.
	out, err := registrySnapshotRows(registry)
	if err != nil {
		return core.RegistrySnapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return core.RegistrySnapshot{}, err
	}
	return out, nil
}

func registrySnapshotRows(registry metadata.Registry) (core.RegistrySnapshot, error) {
	out := core.RegistrySnapshot{}
	for _, pair := range []struct{ source, target any }{
		{registry.Projects, &out.Projects}, {registry.Windows, &out.Windows}, {registry.Agents, &out.Agents},
	} {
		raw, err := json.Marshal(pair.source)
		if err != nil {
			return core.RegistrySnapshot{}, err
		}
		if err := json.Unmarshal(raw, pair.target); err != nil {
			return core.RegistrySnapshot{}, err
		}
	}
	if out.Projects == nil {
		out.Projects = []core.Project{}
	}
	if out.Windows == nil {
		out.Windows = []core.Window{}
	}
	if out.Agents == nil {
		out.Agents = []core.Agent{}
	}
	return out, nil
}
