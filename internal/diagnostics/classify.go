package diagnostics

import "strings"

// CommandClass is the privacy-safe outcome classification. Only values in the
// static allowlist below can be emitted; paths and unknown argv are discarded.
type CommandClass struct {
	Command       string
	Subcommand    string
	StateChanging bool
}

var commandRules = map[string]commandRule{
	"ai":            {subcommands: stringSet("split", "picker", "settings", "status", "notify", "watch-title", "ingest", "integrate", "topic"), changing: stringSet("split", "picker", "settings", "status", "notify", "watch-title", "ingest", "integrate")},
	"attention":     {subcommands: stringSet("toggle", "clear", "arm", "list", "window"), changing: stringSet("toggle", "clear", "arm", "window")},
	"attach":        {alwaysChanging: true, subcommands: stringSet("auto")},
	"current":       {alwaysChanging: true},
	"diagnostics":   {subcommands: stringSet("log")},
	"doctor":        {},
	"focus":         {alwaysChanging: true},
	"hook":          {subcommands: stringSet("list", "edit", "validate", "trust", "untrust"), changing: stringSet("edit", "trust", "untrust")},
	"init":          {},
	"kill":          {alwaysChanging: true, subcommands: stringSet("tagged")},
	"notify":        {subcommands: stringSet("push", "list", "ack", "reconcile"), changing: stringSet("push", "ack", "reconcile")},
	"pin":           {subcommands: stringSet("list", "add", "remove", "toggle", "clear"), changing: stringSet("add", "remove", "toggle", "clear")},
	"preview":       {subcommands: stringSet("cycle-pane", "cycle-window", "select"), changing: stringSet("cycle-pane", "cycle-window", "select")},
	"prune":         {subcommands: stringSet("ephemeral", "session-state"), changing: stringSet("ephemeral")},
	"quit":          {alwaysChanging: true},
	"resources":     {},
	"sessions":      {alwaysChanging: true},
	"session-state": {subcommands: stringSet("status", "save", "delete", "restore", "preview", "popup"), changing: stringSet("save", "delete", "restore", "popup")},
	"session-popup": {subcommands: stringSet("preview", "open", "cycle-pane", "cycle-window"), changing: stringSet("open", "cycle-pane", "cycle-window")},
	"settings":      {alwaysChanging: true},
	"setup":         {subcommands: stringSet("terminal")},
	"shell":         {alwaysChanging: true},
	"status":        {subcommands: stringSet("git", "project", "kube", "usage", "notify", "resources")},
	"statusbar":     {subcommands: stringSet("click", "usage-refresh"), changing: stringSet("click", "usage-refresh")},
	"switch":        {alwaysChanging: true, subcommands: stringSet("toggle-tag", "toggle-pin", "kill", "open", "sidebar-open", "settings", "preview", "cycle-pane", "cycle-window", "sidebar-focus")},
	"tag":           {subcommands: stringSet("list", "toggle", "clear"), changing: stringSet("toggle", "clear")},
	"tmux":          {subcommands: stringSet("hook-trust-prompt", "popup-preview", "popup-switch", "popup-sessions", "popup-toggle", "rebalance-panes", "rename-pane", "print-config", "print-app-config", "install", "install-app", "apply", "autosave-session-state"), changing: stringSet("hook-trust-prompt", "popup-preview", "popup-switch", "popup-sessions", "popup-toggle", "rebalance-panes", "rename-pane", "install", "install-app", "apply", "autosave-session-state")},
	"update":        {subcommands: stringSet("status", "check", "apply"), changing: stringSet("apply")},
	"upgrade":       {alwaysChanging: true},
	"usage":         {},
	"version":       {},
	"welcome":       {},
	"window":        {subcommands: stringSet("record", "recent"), changing: stringSet("record")},
}

type commandRule struct {
	alwaysChanging bool
	subcommands    map[string]struct{}
	changing       map[string]struct{}
}

// Classify discards every non-allowlisted argv value. It may inspect known
// flags to decide whether a command mutates state, but it never returns them.
func Classify(args []string) CommandClass {
	if len(args) == 0 {
		return CommandClass{}
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
	if command == "setup" && out.Subcommand == "terminal" {
		out.StateChanging = hasExactFlag(args[2:], "--apply")
	}
	if command == "init" {
		out.StateChanging = hasExactFlag(args[1:], "--apply")
	}
	if command == "doctor" {
		out.StateChanging = hasExactFlag(args[1:], "--install-missing")
	}
	if command == "prune" && out.Subcommand == "session-state" && len(args) > 2 {
		out.StateChanging = args[2] == "delete"
	}
	if command == "ai" && out.Subcommand == "topic" && len(args) > 2 {
		out.StateChanging = args[2] == "set" || args[2] == "clear"
	}
	if command == "update" && out.Subcommand == "check" {
		out.StateChanging = true // refreshes the local update cache
	}
	return out
}

func hasExactFlag(args []string, flag string) bool {
	for _, arg := range args {
		if arg == flag || strings.HasPrefix(arg, flag+"=") {
			return true
		}
	}
	return false
}
