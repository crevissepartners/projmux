package app

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/cli"
)

// legacyRouteGate keeps the manual leaf parsers in their existing handlers
// while retiring selected pre-v2 argv. Canonical children are forwarded byte
// for byte; rejected compatibility argv never reaches the old handler.
type legacyRouteGate struct {
	name         string
	target       rawArgvCommand
	allowedFirst []string
	replacement  func([]string) string
}

func (c legacyRouteGate) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && slices.Contains(c.allowedFirst, args[0]) {
		return c.target.Run(args, stdout, stderr)
	}
	return retiredCLIUsage(c.name, args, c.replacement(args))
}

type retiredRoute struct {
	name        string
	replacement func([]string) string
}

func (c retiredRoute) Run(args []string, _, _ io.Writer) error {
	return retiredCLIUsage(c.name, args, c.replacement(args))
}

func retiredCLIUsage(route string, args []string, replacement string) error {
	spelling := strings.TrimSpace(strings.Join(append([]string{route}, args...), " "))
	return usageError(fmt.Sprintf("legacy command `projmux %s` was removed; use %s", spelling, replacement))
}

// legacyAIIngestGate is the one Phase 2 compatibility exception. It admits
// only producer argv that a pre-migration installer emitted. The same bytes
// are accepted regardless of whether a machine or a human typed them; every
// other `ai` argv is rejected before the AI handler can read stdin, touch tmux,
// write diagnostics, or produce stdout.
type legacyAIIngestGate struct{ target rawArgvCommand }

func (c legacyAIIngestGate) Run(args []string, stdout, stderr io.Writer) error {
	if cli.IsLegacyAIProducerArgv(args) {
		return c.target.Run(args, stdout, stderr)
	}
	return retiredCLIUsage("ai", args, legacyAIReplacement(args))
}

func legacyAIReplacement(args []string) string {
	if len(args) == 0 {
		return "the canonical `projmux create`, `projmux agent`, `projmux config`, or `projmux diagnostics` route"
	}
	switch args[0] {
	case "split", "picker":
		return "`projmux create agent ...` or `projmux create pane ...`"
	case "settings":
		return "`projmux config edit ...`"
	case "status":
		return "`projmux agent status ...`"
	case "topic":
		return "`projmux agent topic ...`"
	case "integrate":
		return "`projmux agent integrate ...`"
	case "notify":
		return legacyAINotifyReplacement(args[1:])
	case "watch-title":
		return "`projmux internal agent-hook watch-title ...`"
	case "ingest":
		if len(args) > 1 && args[1] == "log" {
			return "`projmux diagnostics agent-hook ...`"
		}
		return "`projmux internal agent-hook ingest ...`"
	default:
		return "the canonical `projmux create`, `projmux agent`, `projmux config`, or `projmux diagnostics` route"
	}
}

func legacyAINotifyReplacement(args []string) string {
	if len(args) > 0 && args[0] == "reset" {
		return "`projmux notification ack ...` or `projmux notification reconcile ...` for queue maintenance; desktop-notification dedupe reset has no direct replacement"
	}
	return "`projmux create notification ...` after translating the pane/payload to `--target`/`--text` (input and semantics changed)"
}

func notifyReplacement(args []string) string {
	if len(args) == 0 {
		return "`projmux create notification`, `projmux get notifications`, or `projmux notification ...`"
	}
	switch args[0] {
	case "push":
		return "`projmux create notification ...`"
	case "list":
		return "`projmux get notifications ...`"
	case "ack":
		return "`projmux notification ack ...`"
	case "reconcile":
		return "`projmux notification reconcile ...`"
	default:
		return "`projmux create notification`, `projmux get notifications`, or `projmux notification ...`"
	}
}

func sessionStateReplacement(args []string) string {
	if len(args) == 0 {
		return "`projmux get snapshots`, `projmux create snapshot`, `projmux delete snapshot`, or `projmux restore snapshot`"
	}
	switch args[0] {
	case "status":
		return "`projmux get snapshots ...`"
	case "save":
		return "`projmux create snapshot ...`"
	case "delete":
		return "`projmux delete snapshot ...`"
	case "restore", "preview", "popup":
		return "`projmux restore snapshot ...`"
	default:
		return "`projmux get snapshots`, `projmux create snapshot`, `projmux delete snapshot`, or `projmux restore snapshot`"
	}
}

func pruneReplacement(args []string) string {
	if len(args) > 0 && args[0] == "ephemeral" {
		return "`projmux runtime prune ...`"
	}
	if len(args) > 1 && args[0] == "session-state" && args[1] == "delete" {
		return "`projmux delete snapshot ...`"
	}
	return "`projmux prune snapshot ...`"
}
