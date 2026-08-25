package cli

import (
	"slices"
	"strings"
)

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
	// Name is the exact current argv token for this node. It is the canonical
	// spelling: everything a route prints about itself is built from it, and an
	// alias never replaces it anywhere.
	Name string
	// Aliases are extra argv tokens that reach this exact node.
	//
	// An alias is a spelling and never a second behavior. Dispatch normalizes it
	// to Name before the handler runs, so both spellings share one flag set, one
	// output-catalog lookup, one cardinality cell, and one error vocabulary --
	// which is what makes the two byte-identical rather than merely similar.
	//
	// The resource verbs use this to accept the singular and the plural of every
	// kind they implement, because `get panes` next to `describe pane` made the
	// operator memorize a form per verb for no gain. The one spelling that is
	// deliberately absent is `get pane`: it is not the singular of `get panes`
	// but a separate exact-one read that owns the `--current -o cwd` projection,
	// so it stays its own canonical child rather than becoming an alias of the
	// list.
	Aliases []string
	// Summary is the one-line description. For top-level visible routes this
	// string is byte-identical to the historical `printUsage` column so root
	// help stays stable while its source of truth moves into this manifest.
	Summary string
	// Disposition is the primary classification. Only top-level routes set it.
	Disposition Disposition
	// Hidden keeps a route out of the primary help listing. The internal
	// namespace invoked from generated popup/key payloads is hidden.
	Hidden bool
	// ProviderShortcut marks a `create <provider>` node. The contract keeps
	// provider shortcuts out of the resource-kind listing, so the shared help
	// renderer groups these separately and no reference or telemetry surface
	// counts a provider as a resource kind.
	ProviderShortcut bool
	// Namespace marks a child that groups sub-routes instead of naming a
	// resource kind of its parent verb.
	//
	// It exists for `get runtime`, whose children are tmux object kinds rather
	// than Projmux resource kinds. Without it the kind-parity contracts would
	// read `runtime` as a fifth kind of the read family and demand a `runtimes`
	// alias, a singular `runtime` read, and a Registry projection catalog for
	// objects that have no Registry identity at all -- which is exactly the
	// merge between runtime inventory and managed resources this surface exists
	// to keep apart.
	Namespace bool
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

// readProjectionCatalog is the shared `-o` catalog minus `pane-id`, and it is
// what the registry read routes advertise.
//
// A `%N` pane id is a live transport binding rather than stored metadata, so
// the read path answers `-o pane-id` with "needs a live transport binding,
// which is not wired yet" and exits 1. The parser still accepts the token --
// `ResolveOutputToken` consults the canonical manifest, whose Outputs list is
// parser input rather than advertising -- but a route may not advertise a
// projection whose only outcome today is an error. The create routes, where the
// projection does work, keep the full catalog.
//
// Splitting the two is what keeps the error classification stable. Dropping
// `pane-id` from the manifest would make the token malformed rather than
// unimplemented and move `get panes -o pane-id` from exit 1 to exit 2, which is
// a behavior change; narrowing only what the route lists changes nothing a
// caller can observe except the help text.
var readProjectionCatalog = []OutputMode{
	OutputModeUID,
	OutputModeName,
	OutputModeRef,
	OutputModeMetadata,
	OutputModeJSON,
	OutputModeNone,
}

// runtimeProjectionCatalog is the `-o` catalog of the runtime read route.
//
// It is deliberately two modes wide. `uid`, `name`, and `ref` are Registry
// projections of a Projmux resource, and most of what a runtime read returns is
// not one: an operator's own pane has no uid to print, no `metadata.name`, and
// no `kind/name` reference. Advertising those modes here would promise a
// projection that is empty for the majority of rows, which is worse than not
// offering it. The whole runtime object -- its exact handle, its attribution,
// and the reason for it -- is in the JSON document.
var runtimeProjectionCatalog = []OutputMode{
	OutputModeJSON,
	OutputModeNone,
}

// routes is the maintained manifest of the current CLI surface: the canonical
// and Shortcut top-level nodes plus the hidden internal plumbing namespace.
// Top-level order is the historical primary help order and is load bearing for
// root help byte-identity.
//
// Hidden is not removal: the `internal` namespace remains live. Retired roots
// are absent entirely so every old spelling follows the ordinary unknown-root
// contract instead of retaining a second dispatch surface.
var routes = []Route{
	{
		// The Agent domain namespace. An Agent is a Window-owned workload with a
		// provider conversation and an Offline/resumable life of its own, so its
		// workflow verbs sit in a noun-first domain; the CRUD half of the Agent
		// surface stays with the shared verbs (`create`, `get`, `describe`,
		// `delete agent`).
		//
		// `integrate` and `usage` remain compatibility aliases. Semantic status
		// and topic are Registry-owned Agent workflows with exact-one resolution;
		// the compatibility `ai` routes forward their managed-Pane mutations into
		// the same authority. `agent usage` forwards to the existing usage
		// handler unchanged: provider account quota is a read-only Agent-domain
		// workflow, not an addressable `usage` resource.
		//
		// The namespace is not read-only as a whole -- `status set`, `topic
		// set|clear`, and `integrate` all write -- so the summary says "manage"
		// rather than "read".
		//
		// `resume` is the one route with logic of its own: it resolves exactly
		// one existing Agent, applies the phase gate, and rebinds it to a new
		// managed Pane launched with the provider's resume argv.
		Name:        "agent",
		Summary:     "Manage Agent state, topic, integrations, and account usage",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux agent status [get [<agent-ref>] | set <unknown|idle|in_progress|approval_required|input_required|response_complete> [<agent-ref>]] [--agent <ref>]",
			"projmux agent topic get|clear [<agent-ref>] [--agent <ref>]",
			"projmux agent topic set <text> [<agent-ref>] [--agent <ref>]",
			"projmux agent resume <ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]...",
			"projmux agent turn start|steer <agent-ref> -- <text>",
			"projmux agent turn interrupt <agent-ref>",
			"projmux agent approval review <agent-ref> [--request <normalized-id>]",
			"projmux agent review [<agent-ref>] [--agent <ref>] [--base <branch> | --commit <sha> | --instructions <text>]",
			"projmux agent integrate <provider> [--dry-run]",
			"projmux agent usage [--model <name>] [--window <name>] [--json] [--force]",
		},
		Canonical: []string{"agent status", "agent topic", "agent resume", "agent turn start", "agent turn steer", "agent turn interrupt", "agent approval review", "agent review", "agent integrate", "agent usage"},
		Children: []Route{
			{Name: "status", Summary: "Read or set semantic Agent interaction independently of lifecycle", Usage: []string{"projmux agent status [get [<agent-ref>] | set <unknown|idle|in_progress|approval_required|input_required|response_complete> [<agent-ref>]] [--agent <ref>]"}, Canonical: []string{"agent status"}},
			{Name: "topic", Summary: "Read, set, or clear one exact Agent topic annotation", Usage: []string{"projmux agent topic get|clear [<agent-ref>] [--agent <ref>]", "projmux agent topic set <text> [<agent-ref>] [--agent <ref>]"}, Canonical: []string{"agent topic"}},
			{
				// This route resolves exactly one existing Agent, refuses a
				// Running one, and rebinds an Offline or Failed one to a new
				// managed Pane built from the conversation its `status.sessionRef`
				// records. The command tree and the canonical manifest now state
				// the same sentence, because the route does what the contract
				// asked for.
				Name:      "resume",
				Summary:   "Rebind an Offline or Failed Agent detached on its Window's exact shell or Agent anchor",
				Usage:     []string{"projmux agent resume <ref> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]..."},
				Canonical: []string{"agent resume"},
			},
			{
				Name:      "turn",
				Summary:   "Send, steer, or interrupt one exact native Codex turn",
				Usage:     []string{"projmux agent turn start|steer <agent-ref> -- <text>", "projmux agent turn interrupt <agent-ref>"},
				Canonical: []string{"agent turn start", "agent turn steer", "agent turn interrupt"},
				Children: []Route{
					{Name: "start", Summary: "Send a new turn to one exact idle Codex thread", Usage: []string{"projmux agent turn start <agent-ref> -- <text>"}, Canonical: []string{"agent turn start"}},
					{Name: "steer", Summary: "Steer one exact current Codex turn", Usage: []string{"projmux agent turn steer <agent-ref> -- <text>"}, Canonical: []string{"agent turn steer"}},
					{Name: "interrupt", Summary: "Interrupt one exact current Codex turn", Usage: []string{"projmux agent turn interrupt <agent-ref>"}, Canonical: []string{"agent turn interrupt"}},
				},
			},
			{
				Name:      "approval",
				Summary:   "Review one exact pending native Codex approval",
				Usage:     []string{"projmux agent approval review <agent-ref> [--request <normalized-id>]"},
				Canonical: []string{"agent approval review"},
				Children:  []Route{{Name: "review", Summary: "Review one exact pending native Codex approval", Usage: []string{"projmux agent approval review <agent-ref> [--request <normalized-id>]"}, Canonical: []string{"agent approval review"}}},
			},
			{Name: "review", Summary: "Start a native review on an exact-bound Codex Agent", Usage: []string{"projmux agent review [<agent-ref>] [--agent <ref>] [--base <branch> | --commit <sha> | --instructions <text>]"}, Canonical: []string{"agent review"}},
			{Name: "integrate", Summary: "Install or remove provider hook integrations", Usage: []string{"projmux agent integrate <provider> [--dry-run]"}, Canonical: []string{"agent integrate"}},
			{
				Name:      "usage",
				Summary:   "Read provider account usage quota snapshots",
				Usage:     []string{"projmux agent usage [--model <name>] [--window <name>] [--json] [--force]"},
				Canonical: []string{"agent usage"},
			},
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
		Summary:     "Enter a Project runtime from outside tmux",
		Disposition: DispositionCanonical,
		Usage:       []string{"projmux attach project <ref>"},
		Canonical:   []string{"attach project"},
		Children: []Route{
			{
				Name:      "project",
				Summary:   "Enter a Project runtime from outside tmux, materializing it when offline",
				Usage:     []string{"projmux attach project <ref>"},
				Canonical: []string{"attach project"},
			},
		},
	},
	{
		// The public config domain. It closes the gap the CLI information
		// architecture v2 track left open: generated tmux config was reachable
		// only through `tmux`, a route the internal isolation Phase classified as
		// plumbing and took out of the primary listing, so the two operations an
		// operator genuinely runs by hand had no public spelling at all.
		//
		// Every route is a parity alias forwarding raw argv to the tmux handler,
		// so stdout, stderr, the exit code, and the side effects are identical to
		// `tmux print-config`, `tmux print-app-config`, and `tmux apply`.
		//
		// `render` takes the artifact as a positional kind because projmux
		// generates two different tmux configs and the `tmux` route has always
		// had two printers for them. Both `print-config` and `print-app-config`
		// declare the canonical spelling `config render`, so a public `render`
		// that reached only one of them would leave the other with no public door
		// at all -- the same gap this route exists to close. Naming the artifact
		// also keeps the node a pure forwarder: dispatch reads leading tokens and
		// passes the remainder through untouched, so `DisableFlagParsing` stays
		// on everywhere and `--bin` remains the leaf parser's business.
		//
		// `apply` takes no kind because there is one: it writes the app config and
		// reloads the live server. `install` and `install-app` deliberately get no
		// public spelling -- they are installer plumbing, reachable only through
		// the hidden `tmux` / `internal tmux` routes, which are unchanged and
		// undeprecated. The summaries below name the exact artifact each route
		// touches so the public surface cannot be read as covering them.
		Name:        "config",
		Summary:     "Edit AI split-mode settings; render or apply generated tmux configuration",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux config edit [--get|--set <mode>]",
			"projmux config render standalone|app [--bin <path>]",
			"projmux config apply [--bin <path>] [--config <path>] [--socket <name>]",
		},
		Canonical: []string{"config edit", "config render", "config apply"},
		Children: []Route{
			{
				Name:      "edit",
				Summary:   "Edit the AI split-mode configuration",
				Usage:     []string{"projmux config edit [--get|--set <mode>]"},
				Canonical: []string{"config edit"},
			},
			{
				Name:    "render",
				Summary: "Print a generated tmux config to stdout; writes nothing",
				Usage: []string{
					"projmux config render standalone [--bin <path>]",
					"projmux config render app [--bin <path>]",
				},
				Canonical: []string{"config render"},
				Children: []Route{
					{
						Name:      "standalone",
						Summary:   "Print the snippet you source from your own tmux.conf",
						Usage:     []string{"projmux config render standalone [--bin <path>]"},
						Canonical: []string{"config render"},
					},
					{
						Name:      "app",
						Summary:   "Print the config the app-owned projmux tmux server runs from",
						Usage:     []string{"projmux config render app [--bin <path>]"},
						Canonical: []string{"config render"},
					},
				},
			},
			{
				Name:      "apply",
				Summary:   "Write the generated app tmux config and reload the live projmux server",
				Usage:     []string{"projmux config apply [--bin <path>] [--config <path>] [--socket <name>]"},
				Canonical: []string{"config apply"},
			},
		},
	},
	{
		// The create verb. An Agent split is `create agent`, and a shell split
		// is `create pane`, because a plain shell surface is a Pane and never
		// an Agent.
		//
		// Every kind is resource-backed and shares one parser. `--project` is a
		// scope flag, not a mode selector: omitted, the scope comes from the
		// active managed runtime, and there is no second product model behind
		// its absence.
		//
		// The kinds and the provider shortcuts share one handler and one schema.
		// A shortcut is a spelling of `create agent --provider <id>`, so it is
		// listed apart from the resource kinds and is never counted as one.
		Name:        "create",
		Summary:     "Create Projmux resources",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux create project --root <absolute-path> [--name <name>] [--label key=value]... [-o <mode>]",
			"projmux create window [--project <ref> | -p <ref>] [--name <name>] [--label key=value]... [-o <mode>] [-- <payload>]",
			"projmux create pane [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--create-window] [--placement right|down] [-o <mode>] [-- <payload>]",
			"projmux create agent --provider <provider> [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--window <ref> | -w <ref>]... [--pane <ref>]... [--create-window] [--placement right|down] [-o <mode>] [-- <payload>]",
			"projmux create codex|claude|antigravity [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--window <ref> | -w <ref>]... [--create-window] [--placement right|down] [-o <mode>] [-- <payload>]",
			"projmux create notification --text <s> --target <SESSION[:WINDOW[.PANE]]> [--socket <s>]",
			"projmux create snapshot",
		},
		Canonical: []string{"create project", "create window", "create pane", "create agent", "create notification", "create snapshot", "create codex", "create claude", "create antigravity"},
		Children: []Route{
			{
				// The explicit Project bootstrap. It is the only route that adds a
				// Project, which is what makes a filesystem scan stop being one:
				// discovery finds candidate directories and this decides that one
				// exact path is a managed resource.
				//
				// Registration is Registry-only -- no session, window or pane is
				// materialized -- so `-o pane-id` is deliberately absent from the
				// projections it advertises.
				Name:    "project",
				Summary: "Register one exact filesystem path as a Registry Project; no runtime is materialized",
				Usage: []string{
					"projmux create project --root <absolute-path> [--name <name>] [--label key=value]... [-o <mode>]",
				},
				Outputs:   readProjectionCatalog,
				Canonical: []string{"create project"},
			},
			{
				// A Window is always created together with the initial Pane it
				// owns, and that Pane's uid is stored as the Window's
				// compatibility shell ref -- the anchor a later `create pane` splits
				// when no explicit --pane is given.
				Name:    "window",
				Summary: "Create a Window and its initial Pane below one Project; the runtime is materialized detached",
				Usage: []string{
					"projmux create window [--project <ref> | -p <ref>] [--name <name>] [--label key=value]... [-o <mode>] [-- <payload>]",
				},
				Outputs:   sharedOutputModes,
				Canonical: []string{"create window"},
			},
			{
				// One resource-backed spelling. It resolves Windows from the
				// registry, anchors on an explicit Pane or each Window's exact
				// role-agnostic anchorPaneRef, splits
				// detached, and never moves the client. With no scope flag at
				// all the Project, the Window, and the anchor come from the
				// active managed runtime, so the split lands where the operator
				// is looking.
				Name:    "pane",
				Summary: "Create a shell Pane detached on an explicit Pane or the Window's exact shell or Agent anchor",
				Usage: []string{
					"projmux create pane [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]",
				},
				Outputs:   sharedOutputModes,
				Canonical: []string{"create pane"},
			},
			{
				// One resource-backed spelling, sharing `create pane`'s scope
				// rule. It allocates a Window-owned Agent plus its managed Pane,
				// splits the resolved Windows detached, and never moves the
				// client.
				Name:    "agent",
				Summary: "Create an Agent detached on an explicit Pane or the Window's exact shell or Agent anchor; --provider is required",
				Usage: []string{
					"projmux create agent --provider <provider> [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]",
				},
				Outputs:   sharedOutputModes,
				Canonical: []string{"create agent"},
			},
			{
				Name:      "notification",
				Summary:   "Create a pending notification row",
				Usage:     []string{"projmux create notification --text <s> --target <SESSION[:WINDOW[.PANE]]> [--socket <s>]"},
				Canonical: []string{"create notification"},
			},
			{
				Name:      "snapshot",
				Summary:   "Create a session snapshot",
				Usage:     []string{"projmux create snapshot"},
				Canonical: []string{"create snapshot"},
			},
			{
				Name:    "codex",
				Summary: "Provider shortcut for create agent --provider codex",
				Usage: []string{
					"projmux create codex [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]",
				},
				ProviderShortcut: true,
				Outputs:          sharedOutputModes,
				Canonical:        []string{"create codex"},
			},
			{
				Name:    "claude",
				Summary: "Provider shortcut for create agent --provider claude",
				Usage: []string{
					"projmux create claude [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]",
				},
				ProviderShortcut: true,
				Outputs:          sharedOutputModes,
				Canonical:        []string{"create claude"},
			},
			{
				Name:    "antigravity",
				Summary: "Provider shortcut for create agent --provider antigravity",
				Usage: []string{
					"projmux create antigravity [--project <ref> | -p <ref>] [--cwd <path>] [--add-dir <path>]... [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--create-window] [--name <name>] [--label key=value]... [--placement right|down] [-o <mode>] [-- <payload>]",
				},
				ProviderShortcut: true,
				Outputs:          sharedOutputModes,
				Canonical:        []string{"create antigravity"},
			},
		},
	},
	{
		// The registry-backed kinds own the cascade planner; `notification` and
		// `snapshot` forward raw argv to the handlers that already own them.
		Name:        "delete",
		Summary:     "Delete Projmux resources with an explicit cascade plan",
		Disposition: DispositionCanonical,
		// The `<ref>` is bracketed for the same reason it is on `describe`:
		// omitting the whole selector is a meaningful invocation inside tmux,
		// where it addresses the active target. It is spelled `[<ref>...]`
		// rather than `<ref>...` and paired with `[--all]` because the two
		// bracketed forms are not interchangeable here -- an omitted selector
		// deletes exactly one resource, and the whole-registry fan-out has to be
		// asked for by name.
		//
		// Every summary below says "in the registry" rather than a bare "all",
		// deliberately and permanently. `--all` is the all-within-current-scope
		// spelling, and projmux has exactly one scope today: the registry. If a
		// later change ever gives the CLI a narrower default scope, "all" would
		// start reading as "all within that scope" while this route kept
		// deleting registry-wide, and that drift would be silent on a
		// destructive verb. Naming the scope in the string it prints is what
		// makes the drift loud instead.
		Usage: []string{
			"projmux delete project [<ref>...] [--selector key=value]... [--all] [--dry-run] [--yes]",
			"projmux delete window [<ref>...] [--project <ref> | -p <ref>] [--selector key=value]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]",
			"projmux delete pane [<ref>...] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]",
			"projmux delete agent [<ref>...] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]",
		},
		Canonical: []string{"delete project", "delete window", "delete pane", "delete agent", "delete notification", "delete snapshot"},
		Children: []Route{
			{
				Name:      "project",
				Summary:   "Explicitly unregister Projects and Registry descendants while preserving roots, Git/worktrees, snapshots, and runtime",
				Aliases:   []string{"projects"},
				Usage:     []string{"projmux delete project [<ref>...] [--selector key=value]... [--all] [--dry-run] [--yes]"},
				Canonical: []string{"delete project"},
			},
			{
				Name:      "window",
				Summary:   "Delete Registry Windows and every descendant Agent and Pane, killing an exact live tmux mirror when present; no selector inside tmux means the active Window, and --all means every Window in the registry",
				Aliases:   []string{"windows"},
				Usage:     []string{"projmux delete window [<ref>...] [--project <ref> | -p <ref>] [--selector key=value]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]"},
				Canonical: []string{"delete window"},
			},
			{
				Name:      "pane",
				Summary:   "Delete Panes; an Agent-owned current Pane leaves its Agent Offline; no selector inside tmux means the active Pane, and --all means every Pane in the registry",
				Aliases:   []string{"panes"},
				Usage:     []string{"projmux delete pane [<ref>...] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]"},
				Canonical: []string{"delete pane"},
			},
			{
				Name:      "agent",
				Summary:   "Delete Agents and their managed Panes; no selector inside tmux means the active Agent, and --all means every Agent in the registry",
				Aliases:   []string{"agents"},
				Usage:     []string{"projmux delete agent [<ref>...] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--all] [--socket <name> | --socket-path <absolute>] [--dry-run] [--yes]"},
				Canonical: []string{"delete agent"},
			},
			{Name: "notification", Summary: "Delete pending notification rows", Aliases: []string{"notifications"}, Canonical: []string{"delete notification"}},
			{Name: "snapshot", Summary: "Delete saved session snapshots", Aliases: []string{"snapshots"}, Canonical: []string{"delete snapshot"}},
		},
	},
	{
		Name:        "describe",
		Summary:     "Describe one Projmux resource",
		Disposition: DispositionCanonical,
		// The `<ref>` is bracketed because omitting the whole selector is a
		// meaningful invocation inside tmux, where it addresses the active
		// target. Outside tmux it stays the ambiguity error it always was, so
		// the per-kind summaries say "inside tmux" rather than implying the
		// reference is optional everywhere.
		Usage: []string{
			"projmux describe project [<ref>] [--project <ref> | -p <ref>] [-o <mode>]",
			"projmux describe window [<ref>] [--project <ref> | -p <ref>] [-o <mode>]",
			"projmux describe pane [<ref>] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [-o <mode>]",
			"projmux describe agent [<ref>] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [-o <mode>]",
		},
		Canonical: []string{"describe project", "describe window", "describe pane", "describe agent"},
		Children: []Route{
			{Name: "project", Summary: "Describe one Project resource; with no selector inside tmux, the active Project", Aliases: []string{"projects"}, Usage: []string{"projmux describe project [<ref>] [--project <ref> | -p <ref>] [-o <mode>]"}, Canonical: []string{"describe project"}, Outputs: readProjectionCatalog},
			{Name: "window", Summary: "Describe one Window resource; inside tmux a reference resolves within the active Project and no selector means the active Window", Aliases: []string{"windows"}, Usage: []string{"projmux describe window [<ref>] [--project <ref> | -p <ref>] [-o <mode>]"}, Canonical: []string{"describe window"}, Outputs: readProjectionCatalog},
			{Name: "pane", Summary: "Describe one Pane resource; inside tmux a reference resolves within the active Project and no selector means the active Pane", Aliases: []string{"panes"}, Usage: []string{"projmux describe pane [<ref>] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [-o <mode>]"}, Canonical: []string{"describe pane"}, Outputs: readProjectionCatalog},
			{Name: "agent", Summary: "Describe one Agent resource; inside tmux a reference resolves within the active Project and no selector means the Agent owning the active Pane", Aliases: []string{"agents"}, Usage: []string{"projmux describe agent [<ref>] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [-o <mode>]"}, Canonical: []string{"describe agent"}, Outputs: readProjectionCatalog},
		},
	},
	{
		Name:        "doctor",
		Summary:     "Run read-only runtime and integration diagnostics",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux doctor [--json] [--section <name>] [--verbose]"},
	},
	{
		Name:        "diagnostics",
		Summary:     "Read operational events or create an explicit local support report",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux diagnostics log [--json] [--tail <n>]",
			"projmux diagnostics agent-hook [--tail <n>] [--json] [--path]",
			"projmux diagnostics report [--output <path>]",
		},
		Canonical: []string{"diagnostics log", "diagnostics agent-hook", "diagnostics report"},
		Children: []Route{
			{Name: "log", Summary: "Read the bounded local operations journal", Canonical: []string{"diagnostics log"}},
			{Name: "agent-hook", Summary: "Read the bounded Agent hook ingest journal", Usage: []string{"projmux diagnostics agent-hook [--tail <n>] [--json] [--path]"}, Canonical: []string{"diagnostics agent-hook"}},
			{Name: "report", Summary: "Create an explicit redacted local support report", Canonical: []string{"diagnostics report"}},
		},
	},
	{
		Name:        "focus",
		Summary:     "Move the current client to a live resource",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux focus project <ref>",
			"projmux focus window <ref> {--project <ref> | -p <ref>}",
			"projmux focus pane <ref> {--project <ref> | -p <ref>} {--window <ref> | -w <ref>}",
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
				Summary:   "Move the current client to an already-live Window in an exact live root session; never materializes",
				Usage:     []string{"projmux focus window <ref> {--project <ref> | -p <ref>} [--socket <path>] [--client <tty>] [--json]"},
				Canonical: []string{"focus window"},
			},
			{
				Name:      "pane",
				Summary:   "Move the current client to an already-live Pane in an exact live root session; never materializes",
				Usage:     []string{"projmux focus pane <ref> {--project <ref> | -p <ref>} {--window <ref> | -w <ref>} [--socket <path>] [--json]"},
				Canonical: []string{"focus pane"},
			},
		},
	},
	{
		// The plural kinds are 0..N reads over the resource registry; the
		// singular `pane` is the exact-one read that also owns the `cwd` field
		// projection. `notifications` and `snapshots` forward raw argv to the
		// handlers that already own them.
		//
		// Every plural kind here accepts its singular as an alias except
		// `panes`. `pane` is already taken by the exact-one read below, and that
		// read is not the singular of this list: it resolves one Pane resource,
		// it owns `--current -o cwd`, and it is what `projmux current` maps onto.
		// Aliasing `pane` onto `panes` would delete a shipped route's meaning, so
		// the two stay separate canonical children and the asymmetry is
		// deliberate rather than an omission.
		Name:        "get",
		Summary:     "Read Projmux resources by selector",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux get projects [--project <ref> | -p <ref>] [--selector key=value]... [-o <mode>]",
			"projmux get windows [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--all-projects | -A] [-o <mode>]",
			"projmux get panes [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--all-projects | -A] [-o <mode>]",
			"projmux get agents [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--all-projects | -A] [-o <mode>]",
			"projmux get pane --current -o cwd",
			"projmux get pane [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [-o <mode>]",
			"projmux get runtime sessions|windows|panes [--socket <name> | --socket-path <absolute>] [-o json|none]",
		},
		Canonical: []string{"get projects", "get windows", "get panes", "get agents",
			"get runtime sessions", "get runtime windows", "get runtime panes",
			"get notifications", "get snapshots", "get pane"},
		Children: []Route{
			{Name: "projects", Summary: "List Project resources", Aliases: []string{"project"}, Usage: []string{"projmux get projects [--project <ref> | -p <ref>] [--selector key=value]... [-o <mode>]"}, Canonical: []string{"get projects"}, Outputs: readProjectionCatalog},
			{
				Name: "windows", Summary: "List Window resources; inside tmux defaults to the active managed root, and --all-projects lists the whole Registry",
				Aliases: []string{"window"}, Usage: []string{"projmux get windows [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--all-projects | -A] [-o <mode>]"},
				Canonical: []string{"get windows"}, Outputs: readProjectionCatalog,
			},
			{
				Name: "panes", Summary: "List Pane resources; inside tmux defaults to the active managed root, and --all-projects lists the whole Registry",
				Usage:     []string{"projmux get panes [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [--all-projects | -A] [-o <mode>]"},
				Canonical: []string{"get panes"}, Outputs: readProjectionCatalog,
			},
			{
				Name: "agents", Summary: "List Agent resources; inside tmux defaults to the active managed root, and --all-projects lists the whole Registry",
				Aliases: []string{"agent"}, Usage: []string{"projmux get agents [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--selector key=value]... [--all-projects | -A] [-o <mode>]"},
				Canonical: []string{"get agents"}, Outputs: readProjectionCatalog,
			},
			{
				// The Runtime diagnostics escape hatch. It is a child of `get`
				// rather than of the `runtime` domain because it is a read with a
				// projection, and every other `get` child is too; the `runtime`
				// domain owns the surfaces that act on the inventory.
				//
				// Its kinds are tmux object kinds, not resource kinds. A tmux
				// session is not a Projmux resource, a window here is any window
				// on the server rather than a Registry Window, and neither
				// accepts a selector -- most of what this route reports has no
				// name to resolve. The plural spellings stand alone with no
				// singular alias for the same reason `get pane` is not the
				// singular of `get panes`: an exact-one runtime read would have
				// to resolve an identity these objects do not have.
				Name:      "runtime",
				Namespace: true,
				Summary:   "List every tmux Session, Window, and Pane on one exact server with its attribution",
				Usage: []string{
					"projmux get runtime sessions [--socket <name> | --socket-path <absolute>] [-o json|none]",
					"projmux get runtime windows [--socket <name> | --socket-path <absolute>] [-o json|none]",
					"projmux get runtime panes [--socket <name> | --socket-path <absolute>] [-o json|none]",
				},
				Canonical: []string{"get runtime sessions", "get runtime windows", "get runtime panes"},
				Children: []Route{
					{
						Name: "sessions", Summary: "List every tmux session on one exact server with its attribution",
						Usage:     []string{"projmux get runtime sessions [--socket <name> | --socket-path <absolute>] [-o json|none]"},
						Canonical: []string{"get runtime sessions"}, Outputs: runtimeProjectionCatalog,
					},
					{
						Name: "windows", Summary: "List every tmux window on one exact server with its attribution",
						Usage:     []string{"projmux get runtime windows [--socket <name> | --socket-path <absolute>] [-o json|none]"},
						Canonical: []string{"get runtime windows"}, Outputs: runtimeProjectionCatalog,
					},
					{
						Name: "panes", Summary: "List every tmux pane on one exact server with its attribution",
						Usage:     []string{"projmux get runtime panes [--socket <name> | --socket-path <absolute>] [-o json|none]"},
						Canonical: []string{"get runtime panes"}, Outputs: runtimeProjectionCatalog,
					},
				},
			},
			{Name: "notifications", Summary: "List pending notification rows", Aliases: []string{"notification"}, Canonical: []string{"get notifications"}},
			{Name: "snapshots", Summary: "List saved session snapshots", Aliases: []string{"snapshot"}, Canonical: []string{"get snapshots"}},
			{
				Name:      "pane",
				Summary:   "Read one Pane resource; with no selector inside tmux, the active Pane",
				Usage:     []string{"projmux get pane [--current] [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]... [--pane <ref>]... [--selector key=value]... [-o <mode>]"},
				Canonical: []string{"get pane"},
				Outputs:   readProjectionCatalog,
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
		Name:        "notification",
		Summary:     "Manage pending notification workflow state",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux notification ack <id> | --all",
			"projmux notification reconcile [--json]",
		},
		Canonical: []string{"notification ack", "notification reconcile"},
		Children: []Route{
			{Name: "ack", Summary: "Acknowledge notification rows", Usage: []string{"projmux notification ack <id> | --all"}, Canonical: []string{"notification ack"}},
			{Name: "reconcile", Summary: "Reconcile the notification queue against live targets", Usage: []string{"projmux notification reconcile [--json]"}, Canonical: []string{"notification reconcile"}},
		},
	},
	{
		Name:        "pin",
		Summary:     "Manage pinned project directories",
		Disposition: DispositionCanonical,
		Usage:       []string{"projmux pin project list|add|remove|toggle|clear"},
		Canonical:   []string{"pin project"},
		Children: []Route{
			// The store behind this route is a lines file of directory paths, not
			// a Project registry: there is no uid, no ownerRef, and no resource
			// document. So the summary says "project directories" like every
			// sibling below it, rather than "Project resources", which would name
			// a resource kind the route never touches.
			{Name: "project", Summary: "Manage pinned project directories (canonical spelling)", Usage: []string{"projmux pin project list|add|remove|toggle|clear"}, Canonical: []string{"pin project"}},
		},
	},
	{
		Name:        "prune",
		Summary:     "Prune stale Projects and snapshots",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux prune snapshot [--older-than <duration>]",
			"projmux prune project --missing --older-than <duration> [--yes]",
		},
		Canonical: []string{"prune project", "prune snapshot"},
		Children: []Route{
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
	},
	{
		Name:        "reconcile",
		Summary:     "Preview or repair Registry and exact tmux resource drift",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux reconcile resources [--dry-run] [--materialize-project <name|uid:uid>] [--socket <name> | --socket-path <absolute>] [-o json]",
			"projmux reconcile registry [--dry-run] [--source <name|absolute-path>] [--expect-source-checksum <sha256:hex>] [--expect-current-checksum <sha256:hex>] [--socket <name> | --socket-path <absolute>] [-o json]",
		},
		Canonical: []string{"reconcile resources", "reconcile registry"},
		Children: []Route{
			{
				Name:      "resources",
				Summary:   "Preview or repair exact anchor-aware Registry and tmux topology on one exact socket",
				Usage:     []string{"projmux reconcile resources [--dry-run] [--materialize-project <name|uid:uid>] [--socket <name> | --socket-path <absolute>] [-o json]"},
				Canonical: []string{"reconcile resources"},
			},
			{
				Name:      "registry",
				Summary:   "Plan Registry state-loss recovery with zero writes, then restore one explicitly named verified source",
				Usage:     []string{"projmux reconcile registry [--dry-run] [--source <name|absolute-path>] [--expect-source-checksum <sha256:hex>] [--expect-current-checksum <sha256:hex>] [--socket <name> | --socket-path <absolute>] [-o json]"},
				Canonical: []string{"reconcile registry"},
			},
		},
	},
	{
		Name:        "rebind",
		Summary:     "Rebind a Project to a new absolute root without moving files",
		Disposition: DispositionCanonical,
		Usage:       []string{"projmux rebind project [<ref>] [--project <ref> | -p <ref>] --root <absolute-path>"},
		Canonical:   []string{"rebind project"},
		Children: []Route{
			{
				Name:      "project",
				Summary:   "Rewrite one Project spec.root; no filesystem move, no heuristic uid merge",
				Usage:     []string{"projmux rebind project [<ref>] [--project <ref> | -p <ref>] --root <absolute-path>"},
				Canonical: []string{"rebind project"},
			},
		},
	},
	{
		Name:        "rename",
		Summary:     "Rename a Projmux resource metadata.name",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux rename project [<ref>] [--project <ref> | -p <ref>] --name <name>",
			"projmux rename window [<ref>] --name <name> [--project <ref> | -p <ref>]",
			"projmux rename pane [<ref>] --name <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]...",
			"projmux rename agent [<ref>] --name <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]...",
		},
		Canonical: []string{"rename project", "rename window", "rename pane", "rename agent"},
		Children: []Route{
			{Name: "project", Summary: "Rename a Projmux Project resource; with no selector inside tmux, the active Project", Aliases: []string{"projects"}, Usage: []string{"projmux rename project [<ref>] [--project <ref> | -p <ref>] --name <name>"}, Canonical: []string{"rename project"}},
			{Name: "window", Summary: "Rename a Projmux Window resource; inside tmux a reference resolves within the active Project or ControlSession and no selector means the active Window", Aliases: []string{"windows"}, Usage: []string{"projmux rename window [<ref>] --name <name> [--project <ref> | -p <ref>]"}, Canonical: []string{"rename window"}},
			{Name: "pane", Summary: "Rename a Projmux Pane resource; inside tmux a reference resolves within the active Project or ControlSession and no selector means the active Pane; does not change tmux pane_title", Aliases: []string{"panes"}, Usage: []string{"projmux rename pane [<ref>] --name <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]..."}, Canonical: []string{"rename pane"}},
			{Name: "agent", Summary: "Rename an Agent stable resource name within the active Project or ControlSession without changing its topic, provider, or managed Pane", Aliases: []string{"agents"}, Usage: []string{"projmux rename agent [<ref>] --name <name> [--project <ref> | -p <ref>] [--window <ref> | -w <ref>]..."}, Canonical: []string{"rename agent"}},
		},
	},
	{
		Name:        "resources",
		Summary:     "Inspect live Project, Window, and Pane CPU/RSS attribution",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux resources"},
	},
	{
		Name:        "restore",
		Summary:     "Project a saved snapshot into one exact closed Project desired state",
		Disposition: DispositionCanonical,
		Usage:       []string{"projmux restore snapshot --session <name> [--project <ref> | -p <ref>] [--dry-run | --yes] [--client <tmux-client>]"},
		Canonical:   []string{"restore snapshot"},
		Children: []Route{
			{
				Name:      "snapshot",
				Summary:   "Project a saved snapshot into one exact closed Project desired state",
				Usage:     []string{"projmux restore snapshot --session <name> [--project <ref> | -p <ref>] [--dry-run | --yes] [--client <tmux-client>]"},
				Canonical: []string{"restore snapshot"},
			},
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
			"projmux runtime diagnostics [--socket <name> | --socket-path <absolute>] [--ui=popup|sidebar]",
			"projmux runtime attach [--keep=N] [--fallback=home|ephemeral]",
			"projmux runtime stop [<session>...]",
			"projmux runtime tag list|toggle|clear",
			"projmux runtime prune [--keep=N]",
		},
		Canonical: []string{"runtime sessions", "runtime diagnostics", "runtime attach", "runtime stop", "runtime tag", "runtime prune"},
		Children: []Route{
			{Name: "sessions", Summary: "Pick a live or ephemeral tmux session", Canonical: []string{"runtime sessions"}},
			{
				// The diagnostics escape hatch, kept separate from `runtime
				// sessions` on purpose. That picker lists recent sessions to open
				// one; this one lists every object on the server -- control,
				// ephemeral, unattributed, foreign, contradictory -- to explain
				// what they are. Merging them would put an operator's own shell
				// into the open-a-session list, which is the adoption this track
				// refuses.
				Name:      "diagnostics",
				Summary:   "Inspect every tmux object on one exact server, with attribution and safe actions",
				Usage:     []string{"projmux runtime diagnostics [--socket <name> | --socket-path <absolute>] [--ui=popup|sidebar]"},
				Canonical: []string{"runtime diagnostics"},
			},
			{Name: "attach", Summary: "Attach a live or ephemeral runtime without Project identity", Canonical: []string{"runtime attach"}},
			{Name: "stop", Summary: "Terminate live tmux sessions by tagged selection", Canonical: []string{"runtime stop"}},
			{Name: "tag", Summary: "Manage the ephemeral tagged session selection", Canonical: []string{"runtime tag"}},
			{Name: "prune", Summary: "Trim old ephemeral tmux sessions", Canonical: []string{"runtime prune"}},
		},
	},
	{
		Name:        "settings",
		Summary:     "Configure projmux",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux settings"},
	},
	{
		Name:        "setup",
		Summary:     "Probe terminal keys or remediate them with setup terminal",
		Disposition: DispositionCanonical,
		Usage: []string{
			"projmux setup",
			"projmux setup terminal [terminal] [--apply]",
		},
		Canonical: []string{"setup terminal"},
		Children: []Route{
			{Name: "terminal", Summary: "Show or apply terminal key remediation", Usage: []string{"projmux setup terminal [terminal] [--apply] [--config <path>] [--allow-symlink]"}, Canonical: []string{"setup terminal"}},
		},
	},
	{
		Name:        "shell",
		Summary:     "Open the isolated projmux tmux app",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux shell [--session <name>]"},
	},
	{
		Name:        "switch",
		Summary:     "Pick and open a project tmux session",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux switch [<project>]"},
		Canonical:   []string{"focus project"},
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
		Name:        "welcome",
		Summary:     "Reprint the shell welcome guide",
		Disposition: DispositionShortcut,
		Usage:       []string{"projmux welcome [--popup [--force]]"},
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
	{
		// The hidden `internal` namespace. Everything under it is plumbing
		// invoked by generated tmux config, tmux hooks, popup payloads, or
		// provider hook commands -- never by a user typing at a prompt. Keeping
		// it in one hidden namespace is what lets the primary help listing carry
		// zero internal routes.
		//
		// Every subcommand forwards raw argv to the handler that owns the
		// behavior. Old pre-namespace spellings are absent from the public and
		// hidden route graph; generated config uses only this namespace.
		Name:        "internal",
		Summary:     "Internal plumbing invoked by generated tmux config, hooks, and popups",
		Disposition: DispositionInternal,
		Hidden:      true,
		Usage: []string{
			"projmux internal tmux print-config|apply|popup-toggle|rebalance-panes|autosave-session-state ...",
			"projmux internal status git|project|usage|notify|resources",
			"projmux internal statusbar click|usage-refresh ...",
			"projmux internal preview cycle-pane|cycle-window|select ...",
			"projmux internal session-popup preview|open|cycle-pane|cycle-window ...",
			"projmux internal agent-pane launch-default|picker ...",
			"projmux internal agent-hook ingest|watch-title ...",
			"projmux internal focus --target <target> ...",
			"projmux internal key-broker [--once]",
			"projmux internal popup-wait-key",
			"projmux internal supervise --pane-uid <uid> --generation <gen> -- <command> ...",
		},
		Canonical: []string{
			"internal tmux",
			"internal status",
			"internal statusbar",
			"internal preview",
			"internal session-popup",
			"internal agent-pane",
			"internal agent-hook",
			"internal focus",
			"internal key-broker",
			"internal popup-wait-key",
			"internal supervise",
		},
		Children: []Route{
			{
				Name:      "tmux",
				Summary:   "Generated tmux config, popup, and pane plumbing",
				Usage:     []string{"projmux internal tmux print-config|print-app-config|install|install-app|apply", "projmux internal tmux popup-preview|popup-switch|popup-sessions|popup-toggle", "projmux internal tmux hook-trust-prompt|rebalance-panes|rename-pane|autosave-session-state"},
				Canonical: []string{"internal tmux"},
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
					{Name: "install", Summary: "Install the generated tmux config", Canonical: []string{"internal tmux"}},
					{Name: "install-app", Summary: "Install the generated app tmux config", Canonical: []string{"internal tmux"}},
					{Name: "apply", Summary: "Apply the generated tmux config to a live server", Canonical: []string{"config apply"}},
					{Name: "autosave-session-state", Summary: "Run the debounced snapshot autosave hook", Canonical: []string{"internal tmux"}},
				},
			},
			{
				Name:      "status",
				Summary:   "Render tmux status bar segments",
				Usage:     []string{"projmux internal status git|project|usage|notify|resources"},
				Canonical: []string{"internal status", "agent usage"},
				Children: []Route{
					{Name: "git", Summary: "Render the git status segment", Canonical: []string{"internal status"}},
					{Name: "project", Summary: "Render the project status segment", Canonical: []string{"internal status"}},
					{Name: "usage", Summary: "Render the AI usage status segment", Canonical: []string{"agent usage"}},
					{Name: "notify", Summary: "Render the notify status segment", Canonical: []string{"internal status"}},
					{Name: "resources", Summary: "Render the live resource status segment", Canonical: []string{"internal status"}},
				},
			},
			{
				Name:      "statusbar",
				Summary:   "Dispatch projmux status bar clicks and shortcuts",
				Usage:     []string{"projmux internal statusbar click <range-id> ...", "projmux internal statusbar usage-refresh"},
				Canonical: []string{"internal statusbar"},
				Children: []Route{
					{Name: "click", Summary: "Dispatch a status bar click range", Canonical: []string{"internal statusbar"}},
					{Name: "usage-refresh", Summary: "Refresh the AI usage snapshot", Canonical: []string{"internal statusbar"}},
				},
			},
			{
				Name:      "preview",
				Summary:   "Manage persisted tmux preview selection",
				Usage:     []string{"projmux internal preview cycle-pane|cycle-window|select"},
				Canonical: []string{"internal preview"},
				Children: []Route{
					{Name: "cycle-pane", Summary: "Advance the persisted preview pane cursor", Canonical: []string{"internal preview"}},
					{Name: "cycle-window", Summary: "Advance the persisted preview window cursor", Canonical: []string{"internal preview"}},
					{Name: "select", Summary: "Persist the preview selection", Canonical: []string{"internal preview"}},
				},
			},
			{
				Name:      "session-popup",
				Summary:   "Read tmux popup preview state",
				Usage:     []string{"projmux internal session-popup preview|open|cycle-pane|cycle-window"},
				Canonical: []string{"internal session-popup"},
				Children: []Route{
					{Name: "preview", Summary: "Render the session popup preview", Canonical: []string{"internal session-popup"}},
					{Name: "open", Summary: "Open the previewed session", Canonical: []string{"internal session-popup"}},
					{Name: "cycle-pane", Summary: "Advance the popup pane cursor", Canonical: []string{"internal session-popup"}},
					{Name: "cycle-window", Summary: "Advance the popup window cursor", Canonical: []string{"internal session-popup"}},
				},
			},
			{
				Name:      "agent-pane",
				Summary:   "Generated Agent and Pane launch plumbing",
				Usage:     []string{"projmux internal agent-pane launch-default <right|down>", "projmux internal agent-pane picker [--resume] --inside <right|down>"},
				Canonical: []string{"internal agent-pane"},
				Children: []Route{
					{Name: "launch-default", Summary: "Launch the saved default target in a new Pane", Canonical: []string{"internal agent-pane"}},
					{Name: "picker", Summary: "Run the Agent launch or resume picker inside its popup", Canonical: []string{"internal agent-pane"}},
				},
			},
			{
				// Provider hook plumbing is invoked by provider hook commands
				// and by the pane title watcher, never by a user, so the Agent
				// decomposition parks it here rather than in the public `agent`
				// namespace.
				Name:      "agent-hook",
				Summary:   "Provider hook ingest and Agent pane title watcher plumbing",
				Usage:     []string{"projmux internal agent-hook ingest <source> ...", "projmux internal agent-hook watch-title [pane]"},
				Canonical: []string{"internal agent-hook"},
				Children: []Route{
					{Name: "ingest", Summary: "Ingest provider hook and log events", Canonical: []string{"internal agent-hook"}},
					{Name: "watch-title", Summary: "Run the Agent pane title watcher", Canonical: []string{"internal agent-hook"}},
				},
			},
			{
				Name:      "focus",
				Summary:   "Machine focus ingress",
				Usage:     []string{"projmux internal focus --target <target> [--socket <path>] [--client <tty>] [--source <source>] [--kind <kind>]", "projmux internal focus --uri <uri>"},
				Canonical: []string{"internal focus"},
			},
			{
				Name:      "key-broker",
				Summary:   "Forward captured physical key chords into the tmux root table",
				Usage:     []string{"projmux internal key-broker [--once]"},
				Canonical: []string{"internal key-broker"},
			},
			{
				Name:      "popup-wait-key",
				Summary:   "Read a single key for a display-only tmux popup",
				Usage:     []string{"projmux internal popup-wait-key"},
				Canonical: []string{"internal popup-wait-key"},
			},
			{
				Name:      "supervise",
				Summary:   "Supervise one managed Pane process and record its exit evidence",
				Usage:     []string{"projmux internal supervise --pane-uid <uid> --generation <gen> [--agent-uid <uid>] [--operation-id <id>] [--argv0 <name>] -- <command> ..."},
				Canonical: []string{"internal supervise"},
			},
		},
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

// findChild resolves one child token, canonical spelling first.
//
// The two passes are ordered rather than merged so a canonical name always
// wins: `get pane` is a child in its own right and must never be reached as
// some other child's alias, whatever order the manifest happens to list them
// in. TestNoChildAliasShadowsACanonicalSpelling makes that a checked property
// instead of a comment.
func findChild(parent Route, token string) (Route, bool) {
	for _, child := range parent.Children {
		if child.Name == token {
			return child, true
		}
	}
	for _, child := range parent.Children {
		if slices.Contains(child.Aliases, token) {
			return child, true
		}
	}
	return Route{}, false
}

// CanonicalChildToken normalizes one child token of a top-level route onto the
// canonical spelling of the node it addresses.
//
// This is the single normalization point the resource verbs dispatch through.
// Returning the canonical token rather than a bool-plus-original is what makes
// an alias byte-identical to what it aliases: everything downstream -- the flag
// set name, the `-o` catalog lookup keyed by `<verb> <kind>`, every usage and
// selector message that interpolates the spelling -- is built from the returned
// token, so no per-alias string can exist to drift.
func CanonicalChildToken(parent, token string) (string, bool) {
	route, ok := LookupRoute(parent)
	if !ok {
		return "", false
	}
	child, ok := findChild(route, token)
	if !ok {
		return "", false
	}
	return child.Name, true
}

// ChildSpellings renders the accepted child tokens of a top-level route in
// manifest order, each canonical spelling followed by its aliases joined with
// `|`.
//
// The unknown-kind refusals are built from this, so a spelling the manifest
// accepts can never be missing from the list the refusal offers. A child with
// no alias renders as the bare canonical token, which is what keeps `get pane`
// visibly distinct from `panes` in the same line.
func ChildSpellings(parent string) []string {
	route, ok := LookupRoute(parent)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(route.Children))
	for _, child := range route.Children {
		out = append(out, strings.Join(append([]string{child.Name}, child.Aliases...), "|"))
	}
	return out
}

// CanonicalGrandchildToken normalizes one token of a two-level route onto the
// canonical spelling of the node it addresses.
//
// It is the depth-two sibling of CanonicalChildToken and exists for the same
// reason: `get runtime sessions` is one route with three kinds, and the kind
// token has to reach the manifest that owns its spellings rather than a second
// hand-written list inside the handler.
func CanonicalGrandchildToken(parent, child, token string) (string, bool) {
	route, ok := LookupRoute(parent)
	if !ok {
		return "", false
	}
	node, ok := findChild(route, child)
	if !ok {
		return "", false
	}
	grandchild, ok := findChild(node, token)
	if !ok {
		return "", false
	}
	return grandchild.Name, true
}

// GrandchildSpellings renders the accepted tokens of a two-level route in
// manifest order, each canonical spelling followed by its aliases joined with
// `|`.
func GrandchildSpellings(parent, child string) []string {
	route, ok := LookupRoute(parent)
	if !ok {
		return nil
	}
	node, ok := findChild(route, child)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(node.Children))
	for _, grandchild := range node.Children {
		out = append(out, strings.Join(append([]string{grandchild.Name}, grandchild.Aliases...), "|"))
	}
	return out
}
