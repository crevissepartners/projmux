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

func retiredCLIUsage(route string, args []string, replacement string) error {
	spelling := strings.TrimSpace(strings.Join(append([]string{route}, args...), " "))
	return usageError(fmt.Sprintf("legacy command `projmux %s` was removed; use %s", spelling, replacement))
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
