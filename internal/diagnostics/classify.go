package diagnostics

import (
	"strconv"
	"strings"
)

// CommandClass is the privacy-safe outcome classification. Only values in the
// static allowlist below can be emitted; paths and unknown argv are discarded.
type CommandClass struct {
	Command       string
	Subcommand    string
	StateChanging bool
}

var commandRules = map[string]commandRule{
	"ai":             {subcommands: stringSet("split", "picker", "settings", "status", "notify", "watch-title", "ingest", "integrate", "topic"), changing: stringSet("split", "picker", "settings", "notify", "watch-title", "integrate")},
	"attention":      {subcommands: stringSet("toggle", "clear", "arm", "list", "window"), changing: stringSet("toggle")},
	"attach":         {alwaysChanging: true, subcommands: stringSet("auto")},
	"current":        {alwaysChanging: true},
	"diagnostics":    {subcommands: stringSet("log", "report")},
	"doctor":         {},
	"focus":          {alwaysChanging: true},
	"hook":           {subcommands: stringSet("list", "edit", "validate", "trust", "untrust"), changing: stringSet("edit", "trust", "untrust")},
	"key-broker":     {alwaysChanging: true},
	"kill":           {alwaysChanging: true, subcommands: stringSet("tagged")},
	"notify":         {subcommands: stringSet("push", "list", "ack", "reconcile"), changing: stringSet("push", "ack", "reconcile")},
	"pin":            {subcommands: stringSet("list", "add", "remove", "toggle", "clear"), changing: stringSet("add", "remove", "toggle", "clear")},
	"popup-wait-key": {},
	"preview":        {subcommands: stringSet("cycle-pane", "cycle-window", "select"), changing: stringSet("cycle-pane", "cycle-window", "select")},
	"prune":          {subcommands: stringSet("ephemeral", "session-state"), changing: stringSet("ephemeral")},
	"quit":           {alwaysChanging: true},
	"resources":      {},
	"sessions":       {alwaysChanging: true},
	"session-state":  {subcommands: stringSet("status", "save", "delete", "restore", "preview", "popup"), changing: stringSet("save", "delete", "restore", "popup")},
	"session-popup":  {subcommands: stringSet("preview", "open", "cycle-pane", "cycle-window"), changing: stringSet("open", "cycle-pane", "cycle-window")},
	"settings":       {alwaysChanging: true},
	"setup":          {subcommands: stringSet("terminal")},
	"shell":          {alwaysChanging: true},
	"status":         {subcommands: stringSet("git", "project", "usage", "notify", "resources")},
	"statusbar":      {subcommands: stringSet("click", "usage-refresh"), changing: stringSet("click", "usage-refresh")},
	"switch":         {alwaysChanging: true, subcommands: stringSet("toggle-tag", "toggle-pin", "kill", "open", "sidebar-open", "settings", "preview", "cycle-pane", "cycle-window", "sidebar-focus")},
	"tag":            {subcommands: stringSet("list", "toggle", "clear"), changing: stringSet("toggle", "clear")},
	"tmux":           {subcommands: stringSet("hook-trust-prompt", "popup-preview", "popup-switch", "popup-sessions", "popup-toggle", "rebalance-panes", "converge", "rename-pane", "print-config", "print-app-config", "install", "install-app", "apply", "autosave-session-state"), changing: stringSet("hook-trust-prompt", "popup-preview", "popup-switch", "popup-sessions", "popup-toggle", "rebalance-panes", "converge", "rename-pane", "install", "install-app", "apply")},
	"update":         {subcommands: stringSet("status", "check", "apply"), changing: stringSet("apply")},
	"upgrade":        {alwaysChanging: true},
	"usage":          {},
	"version":        {},
	"welcome":        {},
	"window":         {subcommands: stringSet("record", "recent"), changing: stringSet("recent")},
}

type commandRule struct {
	alwaysChanging bool
	subcommands    map[string]struct{}
	changing       map[string]struct{}
}

// internalNamespaceToken is the hidden CLI namespace that owns machine-invoked
// plumbing (`internal statusbar click`, `internal tmux rebalance-panes`, ...).
const internalNamespaceToken = "internal"

// internalNamespaceAliases maps an `internal` sub-namespace onto the top-level
// classification rule that already owns it. Only the sub-namespace whose name
// differs from its current top-level spelling needs an entry.
var internalNamespaceAliases = map[string]string{"agent-hook": "ai"}

// stripInternalNamespace rewrites `internal <namespace> ...` onto the current
// top-level spelling the namespace aliases.
//
// The internal namespace is a pure relocation: `internal statusbar click` and
// `statusbar click` reach the same handler with the same side effects, so they
// must also produce the same privacy-safe classification. Classifying the
// relocated spelling separately would split one operation across two command
// names in the journal and would silently drop the state-changing verdict for
// every plumbing route the moment generated config moved onto the new
// spelling.
//
// An `internal` invocation with no namespace, or with an unknown one, yields
// nil, which classifies as the empty (unrecorded) class exactly like any other
// unknown argv.
func stripInternalNamespace(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	namespace := strings.TrimSpace(args[0])
	if alias, ok := internalNamespaceAliases[namespace]; ok {
		return append([]string{alias}, args[1:]...)
	}
	return append([]string{namespace}, args[1:]...)
}

// Classify discards every non-allowlisted argv value. It may inspect known
// flags to decide whether a command mutates state, but it never returns them.
func Classify(args []string) CommandClass {
	if len(args) == 0 {
		return CommandClass{}
	}
	args = normalizeCanonicalCompatibility(args)
	internal := strings.TrimSpace(args[0]) == internalNamespaceToken
	if internal {
		args = stripInternalNamespace(args[1:])
		if len(args) == 0 {
			return CommandClass{}
		}
	}
	command := strings.TrimSpace(args[0])
	if command == "--version" || command == "-version" {
		command = "version"
	}
	rule, ok := commandRules[command]
	if !ok {
		return CommandClass{}
	}
	out := CommandClass{Command: command, StateChanging: rule.alwaysChanging}
	if len(args) > 1 {
		candidate := strings.TrimSpace(args[1])
		if _, ok := rule.subcommands[candidate]; ok {
			out.Subcommand = candidate
			_, out.StateChanging = rule.changing[candidate]
			out.StateChanging = out.StateChanging || rule.alwaysChanging
		}
	}
	// A direct help intent is never a mutation.
	if directHelpIntent(args[1:]) {
		out.StateChanging = false
		return out
	}
	if command == "setup" && out.Subcommand == "terminal" {
		out.StateChanging = boolFlagEnabled(args[2:], "apply")
	}
	if command == "prune" && out.Subcommand == "session-state" && len(args) > 2 {
		out.StateChanging = args[2] == "delete"
	}
	// Generated lifecycle hooks invoke this hidden convergence route
	// automatically. Successful automatic work is zero-volume in the top-level
	// journal; failures still pass through RecordOutcome's error branch. Keep
	// the non-internal spelling state-changing so this exception cannot suppress
	// an operator-invoked mutation.
	if internal && command == "tmux" && out.Subcommand == "converge" {
		out.StateChanging = false
	}
	if command == "ai" && out.Subcommand == "topic" && len(args) > 2 {
		out.StateChanging = args[2] == "set" || args[2] == "clear"
	}
	if command == "ai" && out.Subcommand == "status" && len(args) > 2 {
		out.StateChanging = args[2] == "set"
	}
	if command == "ai" && out.Subcommand == "integrate" && len(args) > 2 && isAIIntegrationProvider(args[2]) {
		out.StateChanging = !boolFlagEnabled(args[3:], "dry-run")
	}
	if command == "session-state" && out.Subcommand == "restore" {
		out.StateChanging = !boolFlagEnabled(args[2:], "dry-run")
	}
	if command == "update" && out.Subcommand == "check" {
		out.StateChanging = true // refreshes the local update cache
	}
	if command == "update" && out.Subcommand == "apply" && boolFlagEnabled(args[2:], "dry-run") {
		out.StateChanging = false
	}
	if command == "upgrade" && boolFlagEnabled(args[1:], "dry-run") {
		out.StateChanging = false
	}
	if command == "welcome" {
		out.StateChanging = boolFlagEnabled(args[1:], "popup") || boolFlagEnabled(args[1:], "force")
	}
	return out
}

// normalizeCanonicalCompatibility maps canonical namespace wrappers onto the
// established privacy-safe class that owns their leaf handler. It never copies
// caller-supplied values into the result.
func normalizeCanonicalCompatibility(args []string) []string {
	if len(args) < 2 {
		return args
	}
	switch args[0] {
	case "pin":
		if args[1] == "project" {
			return append([]string{"pin"}, args[2:]...)
		}
	case "runtime":
		switch args[1] {
		case "sessions":
			return append([]string{"sessions"}, args[2:]...)
		case "attach":
			return append([]string{"attach", "auto"}, args[2:]...)
		case "stop":
			return append([]string{"kill", "tagged"}, args[2:]...)
		case "tag":
			return append([]string{"tag"}, args[2:]...)
		case "prune":
			return append([]string{"prune", "ephemeral"}, args[2:]...)
		}
	case "notification":
		if args[1] == "ack" || args[1] == "reconcile" {
			return append([]string{"notify", args[1]}, args[2:]...)
		}
	case "create":
		switch args[1] {
		case "notification":
			return append([]string{"notify", "push"}, args[2:]...)
		case "snapshot":
			return append([]string{"session-state", "save"}, args[2:]...)
		}
	case "delete":
		if args[1] == "snapshot" {
			return append([]string{"session-state", "delete"}, args[2:]...)
		}
	case "restore":
		if args[1] == "snapshot" {
			return append([]string{"session-state", "restore"}, args[2:]...)
		}
	case "prune":
		if args[1] == "snapshot" {
			return append([]string{"prune", "session-state"}, args[2:]...)
		}
	}
	return args
}

// directHelpIntent reports whether the arguments after the top-level command
// express a direct help intent.
//
// The scan walks positional subcommand words so nested help is recognized
// (`ai settings --help`, `ai topic set --help`), and stops at the first
// flag-looking token: a token after a flag can be that flag's value, and
// misreading a value as help would suppress a real mutation record (for example
// `upgrade --ref --help`). It also stops at a bare `--`, which begins payload.
//
// The bare `help` word counts only in the first position. Later on it can be a
// real positional value — `pin add help` pins a directory named "help" — so
// treating it as help there would hide an actual mutation.
func directHelpIntent(args []string) bool {
	for i, arg := range args {
		arg = strings.TrimSpace(arg)
		switch {
		case arg == "--":
			return false
		case i == 0 && arg == "help":
			return true
		case strings.HasPrefix(arg, "-"):
			return isHelpFlagArg(arg)
		}
	}
	return false
}

// isHelpFlagArg reports whether arg is a flag-form help request.
//
// Matching is name-level and mirrors the flag package: strip one or two leading
// dashes, take the part before the first `=`, and compare against `h`/`help`.
// The flag package returns flag.ErrHelp for every such spelling regardless of
// any value, and the CLI help boundary answers all of them with exit 0, so none
// of them may be scored as a state change. Missing a spelling here records a
// phantom state-changing success row for a mutation that never ran.
func isHelpFlagArg(arg string) bool {
	if !strings.HasPrefix(arg, "-") {
		return false
	}
	name := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
	name, _, _ = strings.Cut(name, "=")
	return name == "h" || name == "help"
}

func isAIIntegrationProvider(arg string) bool {
	switch strings.TrimSpace(arg) {
	case "codex", "claude", "antigravity", "tmux-bell":
		return true
	default:
		return false
	}
}

// boolFlagEnabled mirrors the standard flag package's accepted one- and
// two-dash boolean forms and last-value-wins behavior. Invalid values lead to
// a command error, which is recorded regardless of this success classifier.
func boolFlagEnabled(args []string, name string) bool {
	enabled := false
	for _, arg := range args {
		trimmed := strings.TrimPrefix(arg, "-")
		if trimmed == arg {
			continue
		}
		trimmed = strings.TrimPrefix(trimmed, "-")
		if trimmed == name {
			enabled = true
			continue
		}
		value, ok := strings.CutPrefix(trimmed, name+"=")
		if !ok {
			continue
		}
		parsed, err := strconv.ParseBool(value)
		if err == nil {
			enabled = parsed
		}
	}
	return enabled
}
