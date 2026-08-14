package cli

import "slices"

// OutputMode is a member of the shared Projmux result projection catalog. The
// CLI information architecture v2 contract pins exactly these seven modes for
// the common `--output/-o` flag; new boolean `--print-*` flags are not added.
type OutputMode string

// Shared output projection modes. Order matches the output projection contract
// table so help, reference, and validation error text stay deterministic.
const (
	// OutputModeDefault is the human summary projection. It is the implicit
	// mode when `-o` is absent and is intentionally not part of the explicit
	// enum accepted on the command line.
	OutputModeDefault OutputMode = "default"

	// OutputModeUID prints Projmux `metadata.uid` values, one per line.
	OutputModeUID OutputMode = "uid"
	// OutputModeName prints `metadata.name` values, one per line.
	OutputModeName OutputMode = "name"
	// OutputModeRef prints `kind/<metadata.name>` references, one per line.
	OutputModeRef OutputMode = "ref"
	// OutputModeMetadata prints a metadata-only JSON envelope.
	OutputModeMetadata OutputMode = "metadata"
	// OutputModeJSON prints the full resource/result JSON document.
	OutputModeJSON OutputMode = "json"
	// OutputModePaneID prints validated raw tmux `%N` transport handles.
	OutputModePaneID OutputMode = "pane-id"
	// OutputModeNone prints nothing; explicit quiet automation.
	OutputModeNone OutputMode = "none"
)

// sharedOutputModes is the closed shared catalog accepted by `-o`.
var sharedOutputModes = []OutputMode{
	OutputModeUID,
	OutputModeName,
	OutputModeRef,
	OutputModeMetadata,
	OutputModeJSON,
	OutputModePaneID,
	OutputModeNone,
}

// SharedOutputModes returns the closed shared `-o` catalog in contract order.
// Callers receive a copy so the catalog cannot be mutated in place.
func SharedOutputModes() []OutputMode {
	out := make([]OutputMode, len(sharedOutputModes))
	copy(out, sharedOutputModes)
	return out
}

// IsSharedOutputMode reports whether mode belongs to the shared `-o` catalog.
// Route-local field projections such as `cwd` are deliberately excluded.
func IsSharedOutputMode(mode OutputMode) bool {
	return slices.Contains(sharedOutputModes, mode)
}

// FieldProjection is a route-local output field that is NOT a member of the
// shared output catalog. The contract keeps these scoped to the exact read
// route that owns them so `-o cwd` cannot leak into create or mutation routes.
type FieldProjection string

// FieldProjectionCWD is the Pane-read current working directory projection
// owned by `get pane --current -o cwd`. It is explicitly excluded from the
// shared create output enum; using it on another kind or on a mutation route is
// a usage error (exit 2).
const FieldProjectionCWD FieldProjection = "cwd"

// routeLocalFieldProjections is the closed set of route-local projections.
var routeLocalFieldProjections = []FieldProjection{FieldProjectionCWD}

// RouteLocalFieldProjections returns the closed route-local field projection
// catalog. Callers receive a copy.
func RouteLocalFieldProjections() []FieldProjection {
	out := make([]FieldProjection, len(routeLocalFieldProjections))
	copy(out, routeLocalFieldProjections)
	return out
}
