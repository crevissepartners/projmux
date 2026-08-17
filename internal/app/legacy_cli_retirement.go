package app

import (
	"fmt"
	"io"
	"slices"
	"strings"
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
