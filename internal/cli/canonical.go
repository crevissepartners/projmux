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
	// Outputs pins the shared `-o` modes this route accepts.
	Outputs []OutputMode
	// Fields pins route-local field projections outside the shared catalog.
	Fields []FieldProjection
}

// projectionCatalog is the shared `-o` catalog shared by result-producing
// canonical routes.
var projectionCatalog = sharedOutputModes

// canonicalRoutes is the canonical namespace tree from the CLI information
// architecture v2 contract. Order follows the canonical tree draft.
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
	{Spelling: "create notification", Summary: "Create a pending notification row", Sources: []string{"notify"}, Outputs: projectionCatalog},
	{Spelling: "create snapshot", Summary: "Create a session snapshot", Sources: []string{"session-state"}, Outputs: projectionCatalog},
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
	{Spelling: "delete pane", Summary: "Delete a Pane resource and its live binding", Sources: []string{"delete"}},
	{Spelling: "delete agent", Summary: "Delete an Agent and its managed Panes", Sources: []string{"delete"}},
	{Spelling: "delete notification", Summary: "Delete pending notification rows", Sources: []string{"notify", "delete"}},
	{Spelling: "delete snapshot", Summary: "Delete saved session snapshots", Sources: []string{"session-state", "prune", "delete"}},
	{Spelling: "restore snapshot", Summary: "Restore a saved session snapshot", Sources: []string{"session-state", "restore"}},

	// classification
	{Spelling: "pin project", Summary: "Manage pinned Project resources", Sources: []string{"pin"}},
	{Spelling: "tag project", Summary: "Manage persistent Project tags", Sources: []string{"tag"}},

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

	// notification domain
	{Spelling: "notification ack", Summary: "Acknowledge notification rows", Sources: []string{"notify", "ai"}},
	{Spelling: "notification reconcile", Summary: "Reconcile the notification queue against live targets", Sources: []string{"notify"}},

	// hook domain
	{Spelling: "hook list", Summary: "List lifecycle hook config", Sources: []string{"hook"}},
	{Spelling: "hook edit", Summary: "Edit lifecycle hook config", Sources: []string{"hook"}},
	{Spelling: "hook validate", Summary: "Validate lifecycle hook config", Sources: []string{"hook"}},
	{Spelling: "hook trust", Summary: "Trust the current project hook config", Sources: []string{"hook"}},
	{Spelling: "hook untrust", Summary: "Revoke project hook config trust", Sources: []string{"hook"}},

	// config domain
	{Spelling: "config show", Summary: "Show effective projmux configuration"},
	{Spelling: "config edit", Summary: "Open the interactive configuration UI", Sources: []string{"settings", "ai"}},
	{Spelling: "config render", Summary: "Render the generated tmux configuration", Sources: []string{"tmux"}},
	{Spelling: "config apply", Summary: "Install or apply the generated tmux configuration", Sources: []string{"tmux"}},

	// runtime domain
	{Spelling: "runtime open", Summary: "Start or attach the app-owned tmux runtime", Sources: []string{"shell"}},
	{Spelling: "runtime quit", Summary: "Quit the app-owned tmux runtime", Sources: []string{"quit"}},
	{Spelling: "runtime sessions", Summary: "Pick a live or ephemeral tmux session", Sources: []string{"sessions", "runtime"}},
	{Spelling: "runtime attach", Summary: "Attach a live or ephemeral runtime without Project identity", Sources: []string{"attach", "runtime"}},
	{Spelling: "runtime stop", Summary: "Terminate live tmux sessions by tagged selection", Sources: []string{"kill", "runtime"}},
	{Spelling: "runtime tag", Summary: "Manage the ephemeral tagged session selection", Sources: []string{"tag", "runtime"}},
	{Spelling: "runtime prune", Summary: "Trim old ephemeral tmux sessions", Sources: []string{"prune", "runtime"}},

	// diagnostics domain
	{Spelling: "diagnostics doctor", Summary: "Run read-only runtime and integration diagnostics", Sources: []string{"doctor"}},
	{Spelling: "diagnostics log", Summary: "Read the bounded local operations journal", Sources: []string{"diagnostics"}},
	{Spelling: "diagnostics report", Summary: "Create an explicit redacted local support report", Sources: []string{"diagnostics"}},
	{Spelling: "diagnostics resources", Summary: "Inspect live Project/Window/Pane CPU and RSS attribution", Sources: []string{"resources"}},

	// setup domain
	{Spelling: "setup probe", Summary: "Probe terminal key delivery", Sources: []string{"setup"}},
	{Spelling: "setup terminal", Summary: "Show or apply terminal key remediation", Sources: []string{"setup", "tmux"}},
	{Spelling: "setup welcome", Summary: "Reopen the onboarding guide", Sources: []string{"welcome"}},

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
