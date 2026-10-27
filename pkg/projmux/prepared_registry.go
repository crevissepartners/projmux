package projmux

import (
	"context"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

// CodePreparedSnapshotInvalid refuses a prepared Registry snapshot that this
// Client did not prepare: nil, the zero value, or a handle prepared by a Client
// with a different state root.
const CodePreparedSnapshotInvalid Code = "prepared-snapshot-invalid"

// PreparedRegistrySnapshot is one decoded Registry read, prepared once and
// immutable afterwards. Only PrepareRegistrySnapshot makes a usable one; the
// zero value is refused. It is safe for concurrent use. It grants no authority
// and is not serialized: it names the state root it was read from, and only a
// Client with that root accepts it.
type PreparedRegistrySnapshot struct {
	prepared bool
	root     string
	identity *core.RegistryFileIdentity
	registry metadata.Registry
	snapshot core.RegistrySnapshot
}

// SnapshotReuse describes one PrepareRegistrySnapshot result. Identity is the
// stat identity of the Registry file the handle was read from, nil when that
// read could not be attributed to one file state. Reused reports that the
// previous handle was returned without reading the file.
type SnapshotReuse struct {
	Identity *core.RegistryFileIdentity
	Reused   bool
}

// PrepareRegistrySnapshot returns a prepared handle of the Registry, reusing
// previous when it is still current.
//
// previous is reused, and returned as the same pointer with nothing read or
// decoded, only when it was prepared from this Client's state root and its
// identity equals the file's current stat. Otherwise (nil or foreign
// previous, a nil identity, a file that cannot be stat'ed or has changed) the
// Registry is read once between two stats exactly as ReadRegistrySnapshotCached
// reads it; the identity is set only when both stats succeed and agree. A
// first-use missing file prepares empty rows with a nil identity, so it is
// never reused.
//
// A failure returns no handle and leaves previous unchanged and usable. The
// SDK keeps no cache; the caller holds the handle. Registry failures are the
// registry-unavailable refusal of ReadRegistrySnapshotCached, with the
// loader's cause reachable through errors.Is and errors.As. Cancellation is
// checked on entry and before returning success.
func (c *Client) PrepareRegistrySnapshot(ctx context.Context, previous *PreparedRegistrySnapshot) (*PreparedRegistrySnapshot, SnapshotReuse, error) {
	if err := ctx.Err(); err != nil {
		return nil, SnapshotReuse{}, err
	}
	if c.pathsErr != nil {
		return nil, SnapshotReuse{}, resourceRefusal(c.pathsErr)
	}
	root := c.preparedRoot()
	before, beforeErr := statSnapshotRegistry(c.paths)
	if previous != nil && previous.prepared && previous.root == root && previous.identity != nil &&
		beforeErr == nil && publicRegistryFileIdentity(before) == *previous.identity {
		if err := ctx.Err(); err != nil {
			return nil, SnapshotReuse{}, err
		}
		return previous, SnapshotReuse{Identity: copyRegistryFileIdentity(previous.identity), Reused: true}, nil
	}
	registry, err := loadSnapshotRegistry(c.paths)
	if err != nil {
		return nil, SnapshotReuse{}, causalResourceRefusal(err)
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
		return nil, SnapshotReuse{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, SnapshotReuse{}, err
	}
	prepared := &PreparedRegistrySnapshot{prepared: true, root: root, identity: identity, registry: registry, snapshot: snapshot}
	return prepared, SnapshotReuse{Identity: copyRegistryFileIdentity(identity)}, nil
}

// Snapshot returns an independent clone of the prepared rows, equal to what
// ReadRegistrySnapshot read from the same file state. Changing it never
// changes the handle. A caller may call it once per handle and share the
// result read-only. A handle that was not prepared returns the zero value.
func (p *PreparedRegistrySnapshot) Snapshot() core.RegistrySnapshot {
	if !p.valid() {
		return core.RegistrySnapshot{}
	}
	return p.snapshot.Clone()
}

// IsVirtualWindow reports whether windowUID is a virtual Window: its exact
// eligible anchor Pane is process-hosted. Agent phase and host liveness do not
// matter, so an Offline process Agent's Window stays virtual. An unknown
// Window, or a handle that was not prepared, is not virtual.
func (p *PreparedRegistrySnapshot) IsVirtualWindow(windowUID string) bool {
	return p.valid() && p.registry.IsVirtualWindow(windowUID)
}

func (p *PreparedRegistrySnapshot) valid() bool {
	return p != nil && p.prepared
}

// preparedRoot is the Registry path of the Client's captured state root. It is
// compared as a value only; nothing is resolved on disk.
func (c *Client) preparedRoot() string {
	return store.PathFor(c.paths.StateDir)
}

func copyRegistryFileIdentity(identity *core.RegistryFileIdentity) *core.RegistryFileIdentity {
	if identity == nil {
		return nil
	}
	value := *identity
	return &value
}
