package cli

import (
	"fmt"
	"slices"
	"strings"
)

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

	// OutputModeReceipt prints one versioned OperationReceipt JSON document.
	//
	// It is deliberately outside the shared catalog. The shared modes are
	// projections of the *resources* a read or a create resolved, and every
	// route that offers one offers the same seven; a receipt is a projection of
	// what an *operation did*, which only the mutation routes have. Widening the
	// shared catalog would advertise `-o receipt` on `get` and `describe`, where
	// there is no operation to report.
	OutputModeReceipt OutputMode = "receipt"
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

// receiptOutputModes is the shared catalog plus the operation receipt. It is
// what a resource-backed create route advertises: every existing projection
// keeps its bytes and its position, and `receipt` is appended rather than
// interleaved so a caller reading the list positionally is unaffected.
var receiptOutputModes = append(append([]OutputMode{}, sharedOutputModes...), OutputModeReceipt)

// createProjectOutputModes is the Project bootstrap catalog: the Registry
// projections it can answer plus the receipt. `pane-id` stays absent because
// registration materializes no Pane to name.
var createProjectOutputModes = append(append([]OutputMode{}, readProjectionCatalog...), OutputModeReceipt)

// receiptOnlyOutputModes is the catalog of the routes whose result is the
// operation rather than a resource: the Project runtime lifecycle verbs and
// rename.
//
// Those routes resolve resources they do not create, so the Registry
// projections would print the same line whatever the operation did -- `-o uid`
// after a rename is the uid the caller already had. What is worth projecting is
// what happened, which is why the receipt and the explicit quiet mode are the
// whole catalog.
var receiptOnlyOutputModes = []OutputMode{OutputModeReceipt, OutputModeNone}

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

// AcceptedOutputTokens returns every `-o` token the canonical route accepts:
// its shared output modes first, then its route-local field projections.
func AcceptedOutputTokens(spelling string) []string {
	route, ok := LookupCanonicalRoute(spelling)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(route.Outputs)+len(route.Fields))
	for _, mode := range route.Outputs {
		out = append(out, string(mode))
	}
	for _, field := range route.Fields {
		out = append(out, string(field))
	}
	return out
}

// ResolveOutputToken maps one raw `-o` token onto the projection the canonical
// route accepts. Exactly one of the returned mode and field is non-empty.
//
// This is the single scope gate for projections outside the shared enum. `cwd`
// is declared on exactly one canonical route, so every other kind and every
// mutation route rejects it here rather than in per-route argument parsing.
//
// An unknown token, a shared mode the route does not declare, and a field
// projection owned by a different route all return an error, which the calling
// route reports as a usage error (exit 2).
func ResolveOutputToken(spelling, token string) (OutputMode, FieldProjection, error) {
	route, ok := LookupCanonicalRoute(spelling)
	if !ok {
		return "", "", fmt.Errorf("unknown canonical route %q", spelling)
	}
	for _, mode := range route.Outputs {
		if string(mode) == token {
			return mode, "", nil
		}
	}
	for _, field := range route.Fields {
		if string(field) == token {
			return "", field, nil
		}
	}
	return "", "", fmt.Errorf("invalid --output %q for %s; accepted values: %s",
		token, spelling, strings.Join(AcceptedOutputTokens(spelling), ", "))
}
