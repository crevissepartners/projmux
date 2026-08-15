package cli

// CanonicalRoute pins one canonical CLI information architecture v2 route
// spelling. Phase 0 owns the spelling, the summary, the projection catalog, and
// the mapping back to the current routes that reach the same behavior today; it
// deliberately does not make these routes executable. Later Phases move
// handlers onto these spellings with parity-first aliases.
type CanonicalRoute struct {
	// Spelling is the canonical argv spelling, for example "rebind project".
	Spelling string
	// Summary is the one-line canonical description.
	Summary string
	// Sources lists the current top-level route tokens that reach this
	// behavior today. An empty list means no current public route exists and
	// the route is introduced by a later Phase.
	Sources []string
	// Outputs pins the shared `-o` modes this route accepts. It is parser
	// input, not advertising: ResolveOutputToken reads it to decide whether a
	// token is well-formed for the route, and what a well-formed token then
	// does is the handler's business.
	//
	// The distinction is load bearing for `pane-id`. A `%N` handle is a live
	// transport binding rather than stored metadata, so the registry read path
	// answers `-o pane-id` with "needs a live transport binding, which is not
	// wired yet" and exits 1 -- an unimplemented-but-valid token, owned by the
	// runtime materialization track. Dropping the token from this list would
	// not make the read routes honest; it would reclassify that invocation as
	// malformed input and move it to exit 2. The advertising was narrowed
	// instead, in catalog.go, where readProjectionCatalog omits `pane-id` from
	// what the read routes list in help and in the generated reference.
	Outputs []OutputMode
	// Fields pins route-local field projections outside the shared catalog.
	Fields []FieldProjection
}

// projectionCatalog is the shared `-o` catalog shared by result-producing
// canonical routes.
var projectionCatalog = sharedOutputModes

// canonicalRoutes is the canonical namespace tree from the CLI information
// architecture v2 contract. Order follows the canonical tree draft.
//
// This manifest is deliberately absent from every user-visible surface. Runtime
// help renders `routes` in catalog.go; so does the generated reference in
// docs/cli.md. Neither reads a summary from here. The public-spelling Phase
// kept that boundary and closed the manifest itself, so the two are now honest
// for different reasons rather than for the same one.
//
// The rule this manifest now holds to: a spelling stays only when argv can
// reach it today, or when the behavior it names is a feature another track is
// actually building. A spelling that would need its own slice -- its own leaf
// parser, its own cardinality-matrix rows, its own tests -- is not a plan this
// file gets to keep on someone else's behalf. Twelve such spellings were
// deleted rather than aliased or left standing:
//
//   - `config show` named a non-interactive effective-config printer that
//     exists nowhere, under no spelling. The only effective merge view is a page
//     inside the Settings popup.
//   - `config edit`, `runtime open`, `runtime quit`, `diagnostics doctor`,
//     `diagnostics resources`, `setup probe`, `setup welcome`, `create
//     notification`, `create snapshot`, `notification ack`, and `notification
//     reconcile` each named behavior that ships today under its legacy spelling
//     (`settings`, `shell`, `quit`, `doctor`, `resources`, `setup`, `welcome`,
//     `notify push`, `session-state save`, `notify ack`, `notify reconcile`).
//     Nothing was removed from the binary; only the renaming plan was, because
//     each rename is a slice of its own and two of them belong to a separately
//     tracked Settings information-architecture effort.
//
// The routes those deletions leave without a canonical mapping are enumerated
// and enforced in TestOnlyTheDeletedCanonicalNamespacesLeaveARouteUnmapped, so
// "this route has no canonical v2 spelling" is a checked statement rather than
// an omission nobody notices.
//
// Two divergences remain on purpose, and both name a feature an owning track is
// building rather than an abandoned plan:
//
//   - `agent resume` says "Rebind an Offline or Failed Agent to a new managed
//     Pane" while the handler resolves the Agent, applies the phase gate, and
//     stops. The rebind needs runtime materialization.
//   - `restore snapshot` and `delete pane` likewise describe the whole
//     operation while only part of it is wired.
//
// Their command-tree summaries in catalog.go state the half that ships, which
// is what users read.
//
// TestGeneratedReferenceCarriesNoCanonicalManifestOnlySummary derives the
// divergent summary set from these two manifests at run time and fails if any
// of it reaches docs/cli.md, so the boundary is checked rather than trusted.
var canonicalRoutes = []CanonicalRoute{
	// get
	{Spelling: "get projects", Summary: "List Project resources", Sources: []string{"get"}, Outputs: projectionCatalog},
	{Spelling: "get windows", Summary: "List Window resources", Sources: []string{"window", "get"}, Outputs: projectionCatalog},
	{Spelling: "get panes", Summary: "List Pane resources", Sources: []string{"get"}, Outputs: projectionCatalog},
	{Spelling: "get agents", Summary: "List Agent resources", Sources: []string{"ai", "get"}, Outputs: projectionCatalog},
	{Spelling: "get notifications", Summary: "List pending notification rows", Sources: []string{"notify", "get"}, Outputs: projectionCatalog},
	{Spelling: "get snapshots", Summary: "List saved session snapshots", Sources: []string{"session-state", "get"}, Outputs: projectionCatalog},
	{
		Spelling: "get pane",
		Summary:  "Read one Pane resource",
		Sources:  []string{"current", "get"},
		Outputs:  projectionCatalog,
		// `cwd` is a Pane-read route-local field projection. It is not a
		// member of the shared create output enum, and using it on another
		// kind or on a mutation route is a usage error.
		Fields: []FieldProjection{FieldProjectionCWD},
	},

	// describe
	{Spelling: "describe project", Summary: "Describe one Project resource", Sources: []string{"describe"}, Outputs: projectionCatalog},
	{Spelling: "describe window", Summary: "Describe one Window resource", Sources: []string{"window", "describe"}, Outputs: projectionCatalog},
	{Spelling: "describe pane", Summary: "Describe one Pane resource", Sources: []string{"describe"}, Outputs: projectionCatalog},
	{Spelling: "describe agent", Summary: "Describe one Agent resource", Sources: []string{"ai", "describe"}, Outputs: projectionCatalog},

	// create
	{Spelling: "create window", Summary: "Create a Window with its initial Pane", Sources: []string{"window"}, Outputs: projectionCatalog},
	{Spelling: "create pane", Summary: "Create a shell Pane in an existing Window", Sources: []string{"ai", "create"}, Outputs: projectionCatalog},
	{Spelling: "create agent", Summary: "Create an Agent and its managed Pane", Sources: []string{"ai", "create"}, Outputs: projectionCatalog},
	{Spelling: "create codex", Summary: "Provider shortcut for create agent --provider codex", Sources: []string{"create"}, Outputs: projectionCatalog},
	{Spelling: "create claude", Summary: "Provider shortcut for create agent --provider claude", Sources: []string{"create"}, Outputs: projectionCatalog},
	{Spelling: "create antigravity", Summary: "Provider shortcut for create agent --provider antigravity", Sources: []string{"create"}, Outputs: projectionCatalog},

	// navigation and binding
	{Spelling: "attach project", Summary: "Enter a Project runtime from outside tmux", Sources: []string{"attach"}},
	{Spelling: "focus project", Summary: "Move the current client to a live Project", Sources: []string{"focus", "switch"}},
	{Spelling: "focus window", Summary: "Move the current client to a live Window", Sources: []string{"focus"}},
	{Spelling: "focus pane", Summary: "Move the current client to a live Pane", Sources: []string{"focus"}},

	// rename / rebind
	{Spelling: "rename project", Summary: "Rename a Projmux Project resource", Sources: []string{"rename"}},
	{Spelling: "rename window", Summary: "Rename a Projmux Window resource", Sources: []string{"window", "rename"}},
	{Spelling: "rename pane", Summary: "Rename a Projmux Pane resource; does not change tmux pane_title", Sources: []string{"tmux", "rename"}},
	{Spelling: "rebind project", Summary: "Rebind one Project spec.root to a new absolute directory", Sources: []string{"rebind"}},

	// delete / restore
	{Spelling: "delete window", Summary: "Delete a Window and its descendants", Sources: []string{"delete"}},
	// The live binding half is a feature the runtime materialization track owns:
	// the handler is registry-only today and makes zero tmux calls, so the live
	// pane survives a delete. `internal/app/delete.go` and the command-tree
	// summary in catalog.go both describe the registry half that ships.
	{Spelling: "delete pane", Summary: "Delete a Pane resource and its live binding", Sources: []string{"delete"}},
	{Spelling: "delete agent", Summary: "Delete an Agent and its managed Panes", Sources: []string{"delete"}},
	{Spelling: "delete notification", Summary: "Delete pending notification rows", Sources: []string{"notify", "delete"}},
	{Spelling: "delete snapshot", Summary: "Delete saved session snapshots", Sources: []string{"session-state", "prune", "delete"}},
	// Only the preview half ships: the handler rejects an invocation without
	// `--dry-run`. The actual replay is a feature the session-state track owns.
	{Spelling: "restore snapshot", Summary: "Restore a saved session snapshot", Sources: []string{"session-state", "restore"}},

	// classification
	//
	// Both of these name the classification store the route has always managed,
	// and neither is a Projmux resource. The pin store is a lines file of
	// directory paths; the tag store is a lines file of live session names. There
	// is no uid, no ownerRef, and no registry document behind either, and
	// `pin project` / `tag project` are self-recursive spellings of the same flat
	// actions rather than a second, resource-aware implementation.
	//
	// The persistent Project-metadata tag the earlier draft of this manifest
	// promised is not a deferred feature. The product decision of 2026-08-15
	// settled that a tag is an ephemeral session-scoped marker and that the
	// persistent half will never be built, so the summary states what the route
	// manages instead of a capability with no owner.
	{Spelling: "pin project", Summary: "Manage pinned project directories", Sources: []string{"pin"}},
	{Spelling: "tag project", Summary: "Manage the ephemeral tagged session selection", Sources: []string{"tag"}},

	// prune
	{Spelling: "prune project", Summary: "Prune Projects whose spec.root has been missing for a bounded age", Sources: []string{"prune"}},
	{Spelling: "prune snapshot", Summary: "Prune preserved session snapshots", Sources: []string{"prune"}},

	// agent domain
	{Spelling: "agent status", Summary: "Read or set Agent status state", Sources: []string{"ai", "agent"}},
	{Spelling: "agent topic", Summary: "Read, set, or clear the Agent topic annotation", Sources: []string{"ai", "agent"}},
	{Spelling: "agent resume", Summary: "Rebind an Offline or Failed Agent to a new managed Pane", Sources: []string{"ai", "agent"}},
	{Spelling: "agent integrate", Summary: "Install or remove provider hook integrations", Sources: []string{"ai", "agent"}},
	{Spelling: "agent usage", Summary: "Read provider account usage quota snapshots", Sources: []string{"usage", "status", "agent"}},

	// attention domain
	{Spelling: "attention list", Summary: "List live Pane attention state", Sources: []string{"attention"}},
	{Spelling: "attention toggle", Summary: "Toggle live Pane attention state", Sources: []string{"attention"}},
	{Spelling: "attention clear", Summary: "Clear live Pane attention state", Sources: []string{"attention"}},
	{Spelling: "attention arm", Summary: "Arm focus-only attention consumption", Sources: []string{"attention"}},
	{Spelling: "attention window", Summary: "Render window-scoped attention badges", Sources: []string{"attention"}},

	// hook domain
	{Spelling: "hook list", Summary: "List lifecycle hook config", Sources: []string{"hook"}},
	{Spelling: "hook edit", Summary: "Edit lifecycle hook config", Sources: []string{"hook"}},
	{Spelling: "hook validate", Summary: "Validate lifecycle hook config", Sources: []string{"hook"}},
	{Spelling: "hook trust", Summary: "Trust the current project hook config", Sources: []string{"hook"}},
	{Spelling: "hook untrust", Summary: "Revoke project hook config trust", Sources: []string{"hook"}},

	// config domain
	//
	// Two spellings, both executable. `render` is one canonical spelling over two
	// artifacts, which is exactly how the `tmux` route already maps: both
	// `print-config` and `print-app-config` declare `config render`, because
	// projmux generates two different tmux configs. The public route takes the
	// artifact as a positional kind (`config render standalone|app`) so both
	// halves are reachable and neither is silently the default; the summary says
	// "a generated tmux config" rather than naming one of them.
	//
	// `apply` is 1:1 with `tmux apply`: one artifact, written and reloaded.
	//
	// The `tmux` route's remaining two config spellings -- `install` and
	// `install-app` -- have no canonical entry and no public spelling. They are
	// installer plumbing whose canonical home is `internal tmux`, which is where
	// they already resolve.
	{Spelling: "config render", Summary: "Print a generated tmux config to stdout", Sources: []string{"tmux", "config"}},
	{Spelling: "config apply", Summary: "Write the generated app tmux config and reload the live server", Sources: []string{"tmux", "config"}},

	// runtime domain
	{Spelling: "runtime sessions", Summary: "Pick a live or ephemeral tmux session", Sources: []string{"sessions", "runtime"}},
	{Spelling: "runtime attach", Summary: "Attach a live or ephemeral runtime without Project identity", Sources: []string{"attach", "runtime"}},
	{Spelling: "runtime stop", Summary: "Terminate live tmux sessions by tagged selection", Sources: []string{"kill", "runtime"}},
	{Spelling: "runtime tag", Summary: "Manage the ephemeral tagged session selection", Sources: []string{"tag", "runtime"}},
	{Spelling: "runtime prune", Summary: "Trim old ephemeral tmux sessions", Sources: []string{"prune", "runtime"}},

	// diagnostics domain
	{Spelling: "diagnostics log", Summary: "Read the bounded local operations journal", Sources: []string{"diagnostics"}},
	{Spelling: "diagnostics report", Summary: "Create an explicit redacted local support report", Sources: []string{"diagnostics"}},

	// setup domain
	{Spelling: "setup terminal", Summary: "Show or apply terminal key remediation", Sources: []string{"setup", "tmux"}},

	// update domain
	{Spelling: "update status", Summary: "Show read-only update status", Sources: []string{"update"}},
	{Spelling: "update check", Summary: "Check for a newer release and refresh the cache", Sources: []string{"update"}},
	{Spelling: "update apply", Summary: "Apply an available update", Sources: []string{"update", "upgrade"}},

	// global information
	{Spelling: "help", Summary: "Show help for projmux or one route", Sources: []string{"help"}},
	{Spelling: "version", Summary: "Print the current version", Sources: []string{"version"}},

	// hidden internal namespace
	//
	// The internal plumbing Phase made these executable. Each spelling now names
	// the hidden `internal` route as a source alongside the current spelling it
	// aliases, and both remain dispatchable until the separate breaking-change
	// Phase removes the compatibility half.
	{Spelling: "internal tmux", Summary: "Generated tmux config and popup plumbing", Sources: []string{"tmux", "internal"}},
	{Spelling: "internal status", Summary: "tmux status segment renderer", Sources: []string{"status", "internal"}},
	{Spelling: "internal statusbar", Summary: "tmux status bar click and key dispatcher", Sources: []string{"statusbar", "internal"}},
	{Spelling: "internal preview", Summary: "Persisted preview cursor plumbing", Sources: []string{"preview", "internal"}},
	{Spelling: "internal session-popup", Summary: "Generated session popup payload", Sources: []string{"session-popup", "internal"}},
	{Spelling: "internal agent-hook", Summary: "Provider hook ingest and title watcher plumbing", Sources: []string{"ai", "internal"}},
	{Spelling: "internal key-broker", Summary: "Darwin physical key transport", Sources: []string{"key-broker", "internal"}},
	{Spelling: "internal popup-wait-key", Summary: "Display-only popup single-key reader", Sources: []string{"popup-wait-key", "internal"}},
}

// CanonicalRoutes returns the canonical route manifest in contract order.
func CanonicalRoutes() []CanonicalRoute {
	out := make([]CanonicalRoute, len(canonicalRoutes))
	copy(out, canonicalRoutes)
	return out
}

// LookupCanonicalRoute returns the canonical route for spelling.
func LookupCanonicalRoute(spelling string) (CanonicalRoute, bool) {
	for _, route := range canonicalRoutes {
		if route.Spelling == spelling {
			return route, true
		}
	}
	return CanonicalRoute{}, false
}
