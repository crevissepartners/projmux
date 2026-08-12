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
	"diagnostics":    {subcommands: stringSet("log")},
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
	"status":         {subcommands: stringSet("git", "project", "kube", "usage", "notify", "resources")},
	"statusbar":      {subcommands: stringSet("click", "usage-refresh"), changing: stringSet("click", "usage-refresh")},
	"switch":         {alwaysChanging: true, subcommands: stringSet("toggle-tag", "toggle-pin", "kill", "open", "sidebar-open", "settings", "preview", "cycle-pane", "cycle-window", "sidebar-focus")},
	"tag":            {subcommands: stringSet("list", "toggle", "clear"), changing: stringSet("toggle", "clear")},
	"tmux":           {subcommands: stringSet("hook-trust-prompt", "popup-preview", "popup-switch", "popup-sessions", "popup-toggle", "rebalance-panes", "rename-pane", "print-config", "print-app-config", "install", "install-app", "apply", "autosave-session-state"), changing: stringSet("hook-trust-prompt", "popup-preview", "popup-switch", "popup-sessions", "popup-toggle", "rebalance-panes", "rename-pane", "install", "install-app", "apply")},
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
	// A direct help intent is never a mutation. Keep this check at the first
	// command argument: scanning later argv would misread flag values (for
	// example, `upgrade --ref --help`) as help and could suppress a real
	// successful mutation.
	if len(args) > 1 && isDirectHelpArg(strings.TrimSpace(args[1])) {
		out.StateChanging = false
		return out
	}
	if command == "setup" && out.Subcommand == "terminal" {
		out.StateChanging = boolFlagEnabled(args[2:], "apply")
	}
	if command == "doctor" {
		out.StateChanging = boolFlagEnabled(args[1:], "install-missing") && !boolFlagEnabled(args[1:], "dry-run")
	}
	if command == "prune" && out.Subcommand == "session-state" && len(args) > 2 {
		out.StateChanging = args[2] == "delete"
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

func isDirectHelpArg(arg string) bool {
	switch arg {
	case "help", "-h", "--help", "-help":
		return true
	default:
		return false
	}
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
