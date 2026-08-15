package cli

// Disposition is the primary Phase 0 classification of a current public or
// hidden route. Every current route carries exactly one disposition; the
// compatibility contract keeps orphan routes at zero for every later Phase.
type Disposition string

// The four primary dispositions from the CLI information architecture v2
// compatibility contract.
const (
	// DispositionCanonical marks a route whose current noun/domain namespace
	// (or standard global command) already fits the v2 model.
	DispositionCanonical Disposition = "canonical"
	// DispositionShortcut marks a high-frequency product entrypoint kept as a
	// top-level alias over a canonical handler.
	DispositionShortcut Disposition = "shortcut"
	// DispositionCompatibility marks a currently public route whose name or
	// responsibility is ambiguous in the v2 model.
	DispositionCompatibility Disposition = "compatibility"
	// DispositionInternal marks plumbing invoked by generated tmux config,
	// hooks, or popups rather than by users.
	DispositionInternal Disposition = "internal"
)

// dispositions is the closed disposition set used by the coverage audit.
var dispositions = []Disposition{
	DispositionCanonical,
	DispositionShortcut,
	DispositionCompatibility,
	DispositionInternal,
}

// Dispositions returns the closed disposition set in contract order.
func Dispositions() []Disposition {
	out := make([]Disposition, len(dispositions))
	copy(out, dispositions)
	return out
}

// Route is one node of the current CLI surface. Top-level routes own the
// primary Disposition and Hidden flag; nested children describe the current
// sub-route tree used by the shared help renderer and carry no disposition of
// their own (the parent's disposition covers the whole node).
type Route struct {
	// Name is the exact current argv token for this node.
	Name string
	// Summary is the one-line description. For top-level visible routes this
	// string is byte-identical to the historical `printUsage` column so root
	// help stays stable while its source of truth moves into this manifest.
	Summary string
	// Disposition is the primary classification. Only top-level routes set it.
	Disposition Disposition
	// Hidden keeps a route out of the primary help listing. The two internal
	// helpers invoked from generated popup/key payloads are hidden.
	Hidden bool
	// Usage holds representative synopsis lines (not an exhaustive flag list).
	Usage []string
	// Canonical lists the canonical v2 route spellings this node maps onto.
	// Every entry must resolve against CanonicalRoutes.
	Canonical []string
	// Outputs pins the route-local shared output modes where the contract
	// fixes them for this node today.
	Outputs []OutputMode
	// Fields pins route-local field projections such as the Pane-read `cwd`.
	Fields []FieldProjection
	// Children is the current sub-route tree, in help display order.
	Children []Route
}

// routes is the maintained Phase 0 manifest of the current CLI surface: 33
// public top-level routes plus the 2 hidden internal helpers. Top-level order
// is the historical primary help order and is load bearing for root help
// byte-identity.
var routes = []Route{
	{
		Name:        "ai",
		Summary:     "Manage tmux AI split launch and settings",
		Disposition: DispositionCompatibility,
		Usage:       []string{"projmux ai split|picker|settings|status|notify|watch-title|ingest|integrate|topic"},
		Canonical:   []string{"create agent", "create pane", "get agents", "describe agent", "agent status", "agent topic", "agent resume", "agent integrate", "internal agent-hook"},
		Children: []Route{
			{
				Name:      "split",
				Summary:   "Launch an AI agent or shell pane split",
				Usage:     []string{"projmux ai split [--agent <provider>] [right|down] [--print-pane-id] [-- <prompt>]"},
				Canonical: []string{"create agent", "create pane"},
				Outputs:   []OutputMode{OutputModePaneID},
			},
			{Name: "picker", Summary: "Open the interactive AI agent picker", Usage: []string{"projmux ai picker"}, Canonical: []string{"create agent"}},
			{Name: "settings", Summary: "Open the AI settings surface", Usage: []string{"projmux ai settings"}, Canonical: []string{"config edit"}},
			{Name: "status", Summary: "Read or set the AI pane status state", Usage: []string{"projmux ai status [set ...]"}, Canonical: []string{"agent status"}},
			{Name: "notify", Summary: "Dispatch an AI pane notification", Usage: []string{"projmux ai notify ..."}, Canonical: []string{"notification ack"}},
			{Name: "watch-title", Summary: "Run the AI pane title watcher", Usage: []string{"projmux ai watch-title ..."}, Canonical: []string{"internal agent-hook"}},
			{Name: "ingest", Summary: "Ingest provider hook and log events", Usage: []string{"projmux ai ingest <source>"}, Canonical: []string{"internal agent-hook"}},
			{Name: "integrate", Summary: "Install or remove provider hook integrations", Usage: []string{"projmux ai integrate <provider> [--dry-run]"}, Canonical: []string{"agent integrate"}},
			{Name: "topic", Summary: "Read, set, or clear the AI pane topic", Usage: []string{"projmux ai topic [set|clear] ..."}, Canonical: []string{"agent topic"}},
		},
	},
	{
		Name:        "attention",
		Summary:     "View and manage live tmux pane attention state",
		Disposition: DispositionCanonical,
		Usage:       []string{"projmux attention toggle|clear|arm|list|window"},
		Canonical:   []string{"attention list", "attention toggle", "attention clear", "attention arm", "attention window"},
		Children: []Route{
			{Name: "toggle", Summary: "Toggle attention state for a pane", Canonical: []string{"attention toggle"}},
			{Name: "clear", Summary: "Clear attention state for a pane", Canonical: []string{"attention clear"}},
			{Name: "arm", Summary: "Arm focus-only attention consumption", Canonical: []string{"attention arm"}},
			{Name: "list", Summary: "List live pane attention state", Canonical: []string{"attention list"}},
			{Name: "window", Summary: "Render window-scoped attention badges", Canonical: []string{"attention window"}},
		},
	},
	{
		Name:        "attach",
		Summary:     "Open tmux lifecycle entry helpers",
		Disposition: DispositionCompatibility,
		Usage: []string{
			"projmux attach auto [--keep=N] [--fallback=home|ephemeral]",
			"projmux attach project <ref>",
		},
		Canonical: []string{"attach project", "runtime attach"},
		Children: []Route{
			{Name: "auto", Summary: "Auto-attach with keep and fallback policy", Usage: []string{"projmux attach auto [--keep=N] [--fallback=home|ephemeral]"}, Canonical: []string{"runtime attach"}},
			{
				Name:      "project",
				Summary:   "Enter a Project runtime from outside tmux, materializing it when offline",
				Usage:     []string{"projmux attach project <ref>"},
				Canonical: []string{"attach project"},
			},
		},
	},
	{
		Name:        "current",
		Summary:     "Resolve the active tmux pane path",
		Disposition: DispositionCompatibility,
		Usage:       []string{"projmux current"},
		Canonical:   []string{"get pane"},
		Fields:      []FieldProjection{FieldProjectionCWD},
	},
	{
		// The registry-backed kinds own the cascade planner; `notification` and
		// `snapshot` forward raw argv to the handlers that already own them.
		Name:        "delete",
		Summary:     "Delete Projmux resources with an explicit cascade plan",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux delete window <ref>... [--project <ref>] [--selector key=value]... [--dry-run] [--yes]",
			"projmux delete pane <ref>... [--project <ref>] [--window <ref>]... [--dry-run] [--yes]",
			"projmux delete agent <ref>... [--project <ref>] [--window <ref>]... [--dry-run] [--yes]",
		},
		Canonical: []string{"delete window", "delete pane", "delete agent", "delete notification", "delete snapshot"},
		Children: []Route{
			{Name: "window", Summary: "Delete Windows and every descendant Agent and Pane", Canonical: []string{"delete window"}},
			{Name: "pane", Summary: "Delete Panes; an Agent-owned current Pane leaves its Agent Offline", Canonical: []string{"delete pane"}},
			{Name: "agent", Summary: "Delete Agents and their managed Panes", Canonical: []string{"delete agent"}},
			{Name: "notification", Summary: "Delete pending notification rows", Canonical: []string{"delete notification"}},
			{Name: "snapshot", Summary: "Delete saved session snapshots", Canonical: []string{"delete snapshot"}},
		},
	},
	{
		Name:        "describe",
		Summary:     "Describe one Projmux resource",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux describe project <ref> [-o <mode>]",
			"projmux describe window <ref> [--project <ref>] [-o <mode>]",
			"projmux describe pane <ref> [--project <ref>] [--window <ref>]... [-o <mode>]",
			"projmux describe agent <ref> [--project <ref>] [--window <ref>]... [-o <mode>]",
		},
		Canonical: []string{"describe project", "describe window", "describe pane", "describe agent"},
		Children: []Route{
			{Name: "project", Summary: "Describe one Project resource", Canonical: []string{"describe project"}, Outputs: projectionCatalog},
			{Name: "window", Summary: "Describe one Window resource", Canonical: []string{"describe window"}, Outputs: projectionCatalog},
			{Name: "pane", Summary: "Describe one Pane resource", Canonical: []string{"describe pane"}, Outputs: projectionCatalog},
			{Name: "agent", Summary: "Describe one Agent resource", Canonical: []string{"describe agent"}, Outputs: projectionCatalog},
		},
	},
	{
		Name:        "doctor",
		Summary:     "Run read-only runtime and integration diagnostics",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux doctor [--json] [--section <name>] [--verbose]"},
		Canonical:   []string{"diagnostics doctor"},
	},
	{
		Name:        "diagnostics",
		Summary:     "Read operational events or create an explicit local support report",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux diagnostics log [--json] [--tail <n>]",
			"projmux diagnostics report [--output <path>]",
		},
		Canonical: []string{"diagnostics log", "diagnostics report"},
		Children: []Route{
			{Name: "log", Summary: "Read the bounded local operations journal", Canonical: []string{"diagnostics log"}},
			{Name: "report", Summary: "Create an explicit redacted local support report", Canonical: []string{"diagnostics report"}},
		},
	},
	{
		Name:        "focus",
		Summary:     "Switch the active client to a session/window/pane target",
		Disposition: DispositionCompatibility,
		Usage: []string{
			"projmux focus --target <target>",
			"projmux focus --uri <uri>",
			"projmux focus project <ref>",
			"projmux focus window <ref> --project <ref>",
			"projmux focus pane <ref> --project <ref> --window <ref>",
		},
		Canonical: []string{"focus project", "focus window", "focus pane"},
		Children: []Route{
			{
				Name:      "project",
				Summary:   "Move the current client to an already-live Project; never materializes",
				Usage:     []string{"projmux focus project <ref> [--socket <path>] [--client <tty>] [--json]"},
				Canonical: []string{"focus project"},
			},
			{
				Name:      "window",
				Summary:   "Move the current client to an already-live Window; never materializes",
				Usage:     []string{"projmux focus window <ref> --project <ref> [--socket <path>] [--client <tty>] [--json]"},
				Canonical: []string{"focus window"},
			},
			{
				Name:      "pane",
				Summary:   "Move the current client to an already-live Pane; never materializes",
				Usage:     []string{"projmux focus pane <ref> --project <ref> --window <ref> [--socket <path>] [--json]"},
				Canonical: []string{"focus pane"},
			},
		},
	},
	{
		// The plural kinds are 0..N reads over the resource registry; the
		// singular `pane` is the exact-one read that also owns the `cwd` field
		// projection. `notifications` and `snapshots` forward raw argv to the
		// handlers that already own them.
		Name:        "get",
		Summary:     "Read Projmux resources by selector",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux get projects|windows|panes|agents [--project <ref>] [--selector key=value]... [-o <mode>]",
			"projmux get pane --current -o cwd",
			"projmux get pane --project <ref> [--window <ref>]... [--pane <ref>]... [--selector key=value]... [-o <mode>]",
		},
		Canonical: []string{"get projects", "get windows", "get panes", "get agents", "get notifications", "get snapshots", "get pane"},
		Children: []Route{
			{Name: "projects", Summary: "List Project resources", Canonical: []string{"get projects"}, Outputs: projectionCatalog},
			{Name: "windows", Summary: "List Window resources", Canonical: []string{"get windows"}, Outputs: projectionCatalog},
			{Name: "panes", Summary: "List Pane resources", Canonical: []string{"get panes"}, Outputs: projectionCatalog},
			{Name: "agents", Summary: "List Agent resources", Canonical: []string{"get agents"}, Outputs: projectionCatalog},
			{Name: "notifications", Summary: "List pending notification rows", Canonical: []string{"get notifications"}},
			{Name: "snapshots", Summary: "List saved session snapshots", Canonical: []string{"get snapshots"}},
			{
				Name:      "pane",
				Summary:   "Read one Pane resource",
				Usage:     []string{"projmux get pane [--current] [--project <ref>] [--window <ref>]... [--pane <ref>]... [--selector key=value]... [-o <mode>]"},
				Canonical: []string{"get pane"},
				Outputs:   projectionCatalog,
				Fields:    []FieldProjection{FieldProjectionCWD},
			},
		},
	},
	{
		Name:        "hook",
		Summary:     "List, edit, validate, and trust lifecycle hook config",
		Disposition: DispositionCanonical,
		Usage:       []string{"projmux hook list|edit|validate|trust|untrust"},
		Canonical:   []string{"hook list", "hook edit", "hook validate", "hook trust", "hook untrust"},
		Children: []Route{
			{Name: "list", Summary: "List global and project lifecycle hooks", Canonical: []string{"hook list"}},
			{Name: "edit", Summary: "Edit lifecycle hook config", Canonical: []string{"hook edit"}},
			{Name: "validate", Summary: "Validate lifecycle hook config", Canonical: []string{"hook validate"}},
			{Name: "trust", Summary: "Trust the current project hook config", Canonical: []string{"hook trust"}},
			{Name: "untrust", Summary: "Revoke project hook config trust", Canonical: []string{"hook untrust"}},
		},
	},
	{
		Name:        "kill",
		Summary:     "Terminate tagged tmux sessions",
		Disposition: DispositionCompatibility,
		Usage:       []string{"projmux kill tagged [<session>...]"},
		Canonical:   []string{"runtime stop"},
		Children: []Route{
			{Name: "tagged", Summary: "Terminate the tagged session selection", Canonical: []string{"runtime stop"}},
		},
	},
	{
		Name:        "notify",
		Summary:     "Manage the pending AI notify queue (push/list/ack/reconcile)",
		Disposition: DispositionCompatibility,
		Usage:       []string{"projmux notify push|list|ack|reconcile"},
		Canonical:   []string{"create notification", "get notifications", "delete notification", "notification ack", "notification reconcile"},
		Children: []Route{
			{Name: "push", Summary: "Push a pending notification row", Canonical: []string{"create notification"}},
			{Name: "list", Summary: "List pending notification rows", Canonical: []string{"get notifications"}},
			{Name: "ack", Summary: "Acknowledge notification rows", Canonical: []string{"notification ack"}},
			{Name: "reconcile", Summary: "Reconcile the notification queue against live targets", Canonical: []string{"notification reconcile"}},
		},
	},
	{
		Name:        "pin",
		Summary:     "Manage pinned project directories",
		Disposition: DispositionCompatibility,
		Usage: []string{
			"projmux pin list|add|remove|toggle|clear",
			"projmux pin project list|add|remove|toggle|clear",
		},
		Canonical: []string{"pin project"},
		Children: []Route{
			{Name: "project", Summary: "Manage pinned Project resources (canonical spelling)", Usage: []string{"projmux pin project list|add|remove|toggle|clear"}, Canonical: []string{"pin project"}},
			{Name: "list", Summary: "List pinned project directories", Canonical: []string{"pin project"}},
			{Name: "add", Summary: "Pin a project directory", Canonical: []string{"pin project"}},
			{Name: "remove", Summary: "Unpin a project directory", Canonical: []string{"pin project"}},
			{Name: "toggle", Summary: "Toggle a project directory pin", Canonical: []string{"pin project"}},
			{Name: "clear", Summary: "Clear all project directory pins", Canonical: []string{"pin project"}},
		},
	},
	{
		Name:        "preview",
		Summary:     "Manage persisted tmux preview selection",
		Disposition: DispositionInternal,
		Usage:       []string{"projmux preview cycle-pane|cycle-window|select"},
		Canonical:   []string{"internal preview"},
		Children: []Route{
			{Name: "cycle-pane", Summary: "Advance the persisted preview pane cursor", Canonical: []string{"internal preview"}},
			{Name: "cycle-window", Summary: "Advance the persisted preview window cursor", Canonical: []string{"internal preview"}},
			{Name: "select", Summary: "Persist the preview selection", Canonical: []string{"internal preview"}},
		},
	},
	{
		Name:        "prune",
		Summary:     "Trim stale lifecycle state and inspect preserved snapshots",
		Disposition: DispositionCompatibility,
		Usage: []string{
			"projmux prune ephemeral [--keep=N]",
			"projmux prune session-state [--older-than <duration>]",
			"projmux prune session-state delete <session>...",
			"projmux prune snapshot [--older-than <duration>]",
			"projmux prune project --missing --older-than <duration> [--yes]",
		},
		Canonical: []string{"runtime prune", "prune project", "prune snapshot"},
		Children: []Route{
			{Name: "ephemeral", Summary: "Trim old ephemeral tmux sessions", Canonical: []string{"runtime prune"}},
			{Name: "session-state", Summary: "Inspect or delete preserved session snapshots", Canonical: []string{"prune snapshot", "delete snapshot"}},
			{
				Name:      "project",
				Summary:   "Delete Projects whose spec.root has been missing for a bounded age",
				Usage:     []string{"projmux prune project --missing --older-than <duration> [--yes]"},
				Canonical: []string{"prune project"},
			},
			{
				Name:      "snapshot",
				Summary:   "Inspect or delete preserved session snapshots (canonical spelling)",
				Usage:     []string{"projmux prune snapshot [--older-than <duration>]", "projmux prune snapshot delete <session>..."},
				Canonical: []string{"prune snapshot", "delete snapshot"},
			},
		},
	},
	{
		Name:        "quit",
		Summary:     "Quit the app-owned projmux tmux runtime",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux quit [--yes|--force]"},
		Canonical:   []string{"runtime quit"},
	},
	{
		Name:        "rebind",
		Summary:     "Rebind a Project to a new absolute root without moving files",
		Disposition: DispositionCanonical,
		Usage:       []string{"projmux rebind project <ref> --root <absolute-path>"},
		Canonical:   []string{"rebind project"},
		Children: []Route{
			{
				Name:      "project",
				Summary:   "Rewrite one Project spec.root; no filesystem move, no heuristic uid merge",
				Usage:     []string{"projmux rebind project <ref> --root <absolute-path>"},
				Canonical: []string{"rebind project"},
			},
		},
	},
	{
		Name:        "rename",
		Summary:     "Rename a Projmux resource metadata.name",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux rename project <ref> --name <name>",
			"projmux rename window <ref> --name <name> [--project <ref>]",
			"projmux rename pane <ref> --name <name> [--project <ref>] [--window <ref>]...",
		},
		Canonical: []string{"rename project", "rename window", "rename pane"},
		Children: []Route{
			{Name: "project", Summary: "Rename a Projmux Project resource", Canonical: []string{"rename project"}},
			{Name: "window", Summary: "Rename a Projmux Window resource", Canonical: []string{"rename window"}},
			{Name: "pane", Summary: "Rename a Projmux Pane resource; does not change tmux pane_title", Canonical: []string{"rename pane"}},
		},
	},
	{
		Name:        "resources",
		Summary:     "Inspect live Project, Window, and Pane CPU/RSS attribution",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux resources"},
		Canonical:   []string{"diagnostics resources"},
	},
	{
		Name:        "restore",
		Summary:     "Restore a saved session snapshot",
		Disposition: DispositionCanonical,
		Usage:       []string{"projmux restore snapshot <session> [--dry-run]"},
		Canonical:   []string{"restore snapshot"},
		Children: []Route{
			{Name: "snapshot", Summary: "Restore a saved session snapshot", Canonical: []string{"restore snapshot"}},
		},
	},
	{
		// The runtime domain owns the live and ephemeral tmux inventory, which
		// carries no uid, name reservation, or ownerRef and is therefore not a
		// Projmux resource. Every subcommand forwards raw argv to the handler
		// that already owns the behavior.
		Name:        "runtime",
		Summary:     "Manage the live and ephemeral tmux runtime inventory",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux runtime sessions [--ui=popup|sidebar]",
			"projmux runtime attach [--keep=N] [--fallback=home|ephemeral]",
			"projmux runtime stop [<session>...]",
			"projmux runtime tag list|toggle|clear",
			"projmux runtime prune [--keep=N]",
		},
		Canonical: []string{"runtime sessions", "runtime attach", "runtime stop", "runtime tag", "runtime prune"},
		Children: []Route{
			{Name: "sessions", Summary: "Pick a live or ephemeral tmux session", Canonical: []string{"runtime sessions"}},
			{Name: "attach", Summary: "Attach a live or ephemeral runtime without Project identity", Canonical: []string{"runtime attach"}},
			{Name: "stop", Summary: "Terminate live tmux sessions by tagged selection", Canonical: []string{"runtime stop"}},
			{Name: "tag", Summary: "Manage the ephemeral tagged session selection", Canonical: []string{"runtime tag"}},
			{Name: "prune", Summary: "Trim old ephemeral tmux sessions", Canonical: []string{"runtime prune"}},
		},
	},
	{
		Name:        "sessions",
		Summary:     "Pick and open an existing tmux session",
		Disposition: DispositionCompatibility,
		Usage:       []string{"projmux sessions [--ui=popup|sidebar]"},
		Canonical:   []string{"runtime sessions"},
	},
	{
		Name:        "session-state",
		Summary:     "Inspect and manage saved tmux session snapshots",
		Disposition: DispositionCompatibility,
		Usage:       []string{"projmux session-state status|save|delete|restore|preview|popup"},
		Canonical:   []string{"get snapshots", "create snapshot", "delete snapshot", "restore snapshot"},
		Children: []Route{
			{Name: "status", Summary: "Show saved snapshot status", Canonical: []string{"get snapshots"}},
			{Name: "save", Summary: "Save a session snapshot", Canonical: []string{"create snapshot"}},
			{Name: "delete", Summary: "Delete saved snapshots", Canonical: []string{"delete snapshot"}},
			{Name: "restore", Summary: "Restore a snapshot (CLI allows --dry-run only)", Canonical: []string{"restore snapshot"}},
			{Name: "preview", Summary: "Review a restore plan", Canonical: []string{"restore snapshot"}},
			{Name: "popup", Summary: "Open the snapshot review popup", Canonical: []string{"restore snapshot"}},
		},
	},
	{
		Name:        "session-popup",
		Summary:     "Read tmux popup preview state",
		Disposition: DispositionInternal,
		Usage:       []string{"projmux session-popup preview|open|cycle-pane|cycle-window"},
		Canonical:   []string{"internal session-popup"},
		Children: []Route{
			{Name: "preview", Summary: "Render the session popup preview", Canonical: []string{"internal session-popup"}},
			{Name: "open", Summary: "Open the previewed session", Canonical: []string{"internal session-popup"}},
			{Name: "cycle-pane", Summary: "Advance the popup pane cursor", Canonical: []string{"internal session-popup"}},
			{Name: "cycle-window", Summary: "Advance the popup window cursor", Canonical: []string{"internal session-popup"}},
		},
	},
	{
		Name:        "settings",
		Summary:     "Configure projmux",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux settings"},
		Canonical:   []string{"config edit"},
	},
	{
		Name:        "setup",
		Summary:     "Probe terminal keys or remediate them with setup terminal",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux setup",
			"projmux setup terminal [terminal] [--apply]",
		},
		Canonical: []string{"setup probe", "setup terminal"},
		Children: []Route{
			{Name: "terminal", Summary: "Show or apply terminal key remediation", Usage: []string{"projmux setup terminal [terminal] [--apply] [--config <path>] [--allow-symlink]"}, Canonical: []string{"setup terminal"}},
		},
	},
	{
		Name:        "shell",
		Summary:     "Open the isolated projmux tmux app",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux shell [--session <name>]"},
		Canonical:   []string{"runtime open"},
	},
	{
		Name:        "status",
		Summary:     "Render tmux status bar segments",
		Disposition: DispositionInternal,
		Usage:       []string{"projmux status git|project|kube|usage|notify|resources"},
		Canonical:   []string{"internal status", "agent usage"},
		Children: []Route{
			{Name: "git", Summary: "Render the git status segment", Canonical: []string{"internal status"}},
			{Name: "project", Summary: "Render the project status segment", Canonical: []string{"internal status"}},
			{Name: "kube", Summary: "Render the kube status segment", Canonical: []string{"internal status"}},
			{Name: "usage", Summary: "Render the AI usage status segment", Canonical: []string{"agent usage"}},
			{Name: "notify", Summary: "Render the notify status segment", Canonical: []string{"internal status"}},
			{Name: "resources", Summary: "Render the live resource status segment", Canonical: []string{"internal status"}},
		},
	},
	{
		Name:        "statusbar",
		Summary:     "Dispatch projmux status bar clicks and shortcuts",
		Disposition: DispositionInternal,
		Usage: []string{
			"projmux statusbar click <range-id> ...",
			"projmux statusbar usage-refresh",
		},
		Canonical: []string{"internal statusbar"},
		Children: []Route{
			{Name: "click", Summary: "Dispatch a status bar click range", Canonical: []string{"internal statusbar"}},
			{Name: "usage-refresh", Summary: "Refresh the AI usage snapshot", Canonical: []string{"internal statusbar"}},
		},
	},
	{
		Name:        "switch",
		Summary:     "Pick and open a project tmux session",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux switch [<project>]"},
		Canonical:   []string{"focus project"},
	},
	{
		Name:        "tag",
		Summary:     "Manage tagged tmux sessions",
		Disposition: DispositionCompatibility,
		Usage: []string{
			"projmux tag list|toggle|clear",
			"projmux tag project list|toggle|clear",
		},
		Canonical: []string{"tag project", "runtime tag"},
		Children: []Route{
			// The contract splits `tag` into persistent Project-metadata tags and
			// the ephemeral live-only selection. Only the second half exists
			// today: the persistent half needs a Project registry writer, which
			// arrives with a later Phase. So this canonical spelling is a pure
			// parity alias over the tagged session selection, and its summary
			// describes that rather than the capability it will eventually gain.
			{Name: "project", Summary: "Manage the tagged session selection (canonical spelling)", Usage: []string{"projmux tag project list|toggle|clear"}, Canonical: []string{"tag project"}},
			{Name: "list", Summary: "List the tagged session selection", Canonical: []string{"runtime tag"}},
			{Name: "toggle", Summary: "Toggle a session tag", Canonical: []string{"runtime tag"}},
			{Name: "clear", Summary: "Clear the tagged session selection", Canonical: []string{"runtime tag"}},
		},
	},
	{
		Name:        "tmux",
		Summary:     "Open tmux popup entry helpers",
		Disposition: DispositionInternal,
		Usage: []string{
			"projmux tmux print-config|print-app-config",
			"projmux tmux install|install-app|apply",
			"projmux tmux popup-preview|popup-switch|popup-sessions|popup-toggle",
			"projmux tmux hook-trust-prompt|rebalance-panes|rename-pane|autosave-session-state",
		},
		Canonical: []string{"config render", "config apply", "setup terminal", "internal tmux"},
		Children: []Route{
			{Name: "hook-trust-prompt", Summary: "Show the project hook trust prompt", Canonical: []string{"internal tmux"}},
			{Name: "popup-preview", Summary: "Open the preview popup", Canonical: []string{"internal tmux"}},
			{Name: "popup-switch", Summary: "Open the project switch popup", Canonical: []string{"internal tmux"}},
			{Name: "popup-sessions", Summary: "Open the sessions popup", Canonical: []string{"internal tmux"}},
			{Name: "popup-toggle", Summary: "Toggle a client-scoped popup surface", Canonical: []string{"internal tmux"}},
			{Name: "rebalance-panes", Summary: "Rebalance panes after a pane exit", Canonical: []string{"internal tmux"}},
			{Name: "rename-pane", Summary: "Run the pane rename prompt helper", Canonical: []string{"rename pane"}},
			{Name: "print-config", Summary: "Print the generated tmux config", Canonical: []string{"config render"}},
			{Name: "print-app-config", Summary: "Print the generated app tmux config", Canonical: []string{"config render"}},
			{Name: "install", Summary: "Install the generated tmux config", Canonical: []string{"config apply"}},
			{Name: "install-app", Summary: "Install the generated app tmux config", Canonical: []string{"config apply"}},
			{Name: "apply", Summary: "Apply the generated tmux config to a live server", Canonical: []string{"config apply"}},
			{Name: "autosave-session-state", Summary: "Run the debounced snapshot autosave hook", Canonical: []string{"internal tmux"}},
		},
	},
	{
		Name:        "update",
		Summary:     "Check installer-aware release update status",
		Disposition: DispositionCanonical,
		Usage:       []string{"projmux update status|check|apply"},
		Canonical:   []string{"update status", "update check", "update apply"},
		Children: []Route{
			{Name: "status", Summary: "Show read-only update status", Canonical: []string{"update status"}},
			{Name: "check", Summary: "Check for a newer release and refresh the cache", Canonical: []string{"update check"}},
			{Name: "apply", Summary: "Apply an available update", Canonical: []string{"update apply"}},
		},
	},
	{
		Name:        "upgrade",
		Summary:     "Self-update projmux via go install",
		Disposition: DispositionCompatibility,
		Usage:       []string{"projmux upgrade [--ref <ref>] [--target <path>] [--no-apply] [--dry-run]"},
		Canonical:   []string{"update apply"},
	},
	{
		Name:        "usage",
		Summary:     "Report AI token usage across 5h and weekly windows",
		Disposition: DispositionCompatibility,
		Usage:       []string{"projmux usage [--model <name>] [--window <name>] [--json] [--force]"},
		Canonical:   []string{"agent usage"},
	},
	{
		Name:        "welcome",
		Summary:     "Reprint the shell welcome guide",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux welcome [--popup [--force]]"},
		Canonical:   []string{"setup welcome"},
	},
	{
		Name:        "window",
		Summary:     "Open recent window navigation surfaces",
		Disposition: DispositionCanonical,
		Usage:       []string{"projmux window record|recent"},
		Canonical:   []string{"get windows", "describe window", "create window", "focus window", "rename window"},
		Children: []Route{
			{Name: "record", Summary: "Record the current window into the MRU store", Canonical: []string{"get windows"}},
			{Name: "recent", Summary: "Open the recent-window navigation picker", Canonical: []string{"get windows"}},
		},
	},
	{
		Name:        "help",
		Summary:     "Show bootstrap help",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux help",
			"projmux --help",
			"projmux <route> --help",
		},
		Canonical: []string{"help"},
	},
	{
		Name:        "version",
		Summary:     "Print the current version",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux version",
			"projmux --version",
		},
		Canonical: []string{"version"},
	},
	// Hidden internal helpers. They are dispatched but intentionally absent
	// from the primary help listing.
	{
		Name:        "key-broker",
		Summary:     "Forward captured physical key chords into the tmux root table",
		Disposition: DispositionInternal,
		Hidden:      true,
		Usage:       []string{"projmux key-broker [--once]"},
		Canonical:   []string{"internal key-broker"},
	},
	{
		Name:        "popup-wait-key",
		Summary:     "Read a single key for a display-only tmux popup",
		Disposition: DispositionInternal,
		Hidden:      true,
		Usage:       []string{"projmux popup-wait-key"},
		Canonical:   []string{"internal popup-wait-key"},
	},
}

// Routes returns the current-surface manifest. The slice header is copied so
// callers cannot reorder the manifest; nested Route values stay shared and are
// treated as read-only data.
func Routes() []Route {
	out := make([]Route, len(routes))
	copy(out, routes)
	return out
}

// LookupRoute returns the top-level route for token.
func LookupRoute(token string) (Route, bool) {
	for _, route := range routes {
		if route.Name == token {
			return route, true
		}
	}
	return Route{}, false
}

// Resolve walks tokens against the manifest and returns the path of the
// deepest matched route plus that route. It stops at the first token that does
// not match a child of the current node, so unknown trailing tokens and flags
// resolve to their nearest documented ancestor. ok is false when the first
// token is not a known top-level route.
func Resolve(tokens []string) (path []string, route Route, ok bool) {
	if len(tokens) == 0 {
		return nil, Route{}, false
	}
	current, found := LookupRoute(tokens[0])
	if !found {
		return nil, Route{}, false
	}
	path = []string{current.Name}
	for _, token := range tokens[1:] {
		child, childFound := findChild(current, token)
		if !childFound {
			break
		}
		current = child
		path = append(path, child.Name)
	}
	return path, current, true
}

func findChild(parent Route, token string) (Route, bool) {
	for _, child := range parent.Children {
		if child.Name == token {
			return child, true
		}
	}
	return Route{}, false
}
