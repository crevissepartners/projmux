package projmux

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// ReadRegistrySnapshot reads the captured Registry once without runtime or
// provider contact, locks, writes, or permission repair. The stored envelope
// and rows (including duplicates and Offline Agents) retain their order and
// values. The returned non-nil slices and all nested values belong to the
// caller.
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

// The Registry loader and stat used by ReadRegistrySnapshotCached are replaced
// only by package tests, which count them.
var (
	loadSnapshotRegistry = resources.Load
	statSnapshotRegistry = func(paths config.Paths) (store.RegistryFileIdentity, error) {
		return store.NewDefaultStore(paths).RegistryFileIdentity()
	}
)

// ReadRegistrySnapshotCached returns the Registry snapshot together with the
// identity of the file it was read from, reusing previous when that file is
// unchanged.
//
// When previous carries a RegistryIdentity equal to the file's current stat,
// the Registry is not read or decoded: previous.Snapshot is returned as is,
// sharing its memory, with Reused set. Otherwise (nil previous, nil or
// different identity, or a file that cannot be stat'ed) the Registry is read
// once as ReadRegistrySnapshot reads it, between two stats; RegistryIdentity is
// set only when both stats succeed and agree, and the new snapshot belongs to
// the caller. The SDK keeps no cache. Treat previous as read-only and use
// Clone for an independent copy; a snapshot the caller changed is not
// re-validated against the file.
//
// A first-use missing file reads empty with a nil identity, so it is never
// reused. Registry failures are the registry-unavailable refusal and detail
// of ReadRegistrySnapshot, and errors.Is and errors.As reach the loader's
// cause. Cancellation is checked on entry and before returning success.
func (c *Client) ReadRegistrySnapshotCached(ctx context.Context, previous *core.RegistrySnapshotRead) (core.RegistrySnapshotRead, error) {
	if err := ctx.Err(); err != nil {
		return core.RegistrySnapshotRead{}, err
	}
	if c.pathsErr != nil {
		return core.RegistrySnapshotRead{}, resourceRefusal(c.pathsErr)
	}
	before, beforeErr := statSnapshotRegistry(c.paths)
	if previous != nil && previous.RegistryIdentity != nil && beforeErr == nil && publicRegistryFileIdentity(before) == *previous.RegistryIdentity {
		if err := ctx.Err(); err != nil {
			return core.RegistrySnapshotRead{}, err
		}
		identity := *previous.RegistryIdentity
		return core.RegistrySnapshotRead{Snapshot: previous.Snapshot, RegistryIdentity: &identity, Reused: true}, nil
	}
	registry, err := loadSnapshotRegistry(c.paths)
	if err != nil {
		return core.RegistrySnapshotRead{}, causalResourceRefusal(err)
	}
	var identity *core.RegistryFileIdentity
	if beforeErr == nil {
		if after, afterErr := statSnapshotRegistry(c.paths); afterErr == nil && after == before {
			value := publicRegistryFileIdentity(after)
			identity = &value
		}
	}
	snapshot, err := registrySnapshotRows(registry)
	if err != nil {
		return core.RegistrySnapshotRead{}, err
	}
	if err := ctx.Err(); err != nil {
		return core.RegistrySnapshotRead{}, err
	}
	return core.RegistrySnapshotRead{Snapshot: snapshot, RegistryIdentity: identity}, nil
}

// causalResourceRefusal is resourceRefusal that keeps err as the refusal's
// cause, so errors.Is and errors.As still reach the loader failure.
func causalResourceRefusal(err error) error {
	var failed *resources.Failure
	if errors.As(resources.Classify(err), &failed) {
		return &Refusal{Code: Code(failed.Code), message: failed.Error(), cause: err}
	}
	return err
}

func registrySnapshotRows(registry metadata.Registry) (core.RegistrySnapshot, error) {
	out := core.RegistrySnapshot{APIVersion: registry.APIVersion, SchemaVersion: registry.SchemaVersion, UpdatedAt: registry.UpdatedAt}
	for _, pair := range []struct{ source, target any }{
		{registry.Projects, &out.Projects}, {registry.ControlSessions, &out.ControlSessions}, {registry.Windows, &out.Windows},
		{registry.Panes, &out.Panes}, {registry.Agents, &out.Agents}, {registry.NameReservations, &out.NameReservations},
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
	if out.ControlSessions == nil {
		out.ControlSessions = []core.ControlSession{}
	}
	if out.Windows == nil {
		out.Windows = []core.Window{}
	}
	if out.Panes == nil {
		out.Panes = []core.Pane{}
	}
	if out.Agents == nil {
		out.Agents = []core.Agent{}
	}
	if out.NameReservations == nil {
		out.NameReservations = []core.NameReservation{}
	}
	return out, nil
}
