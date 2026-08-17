package app

import (
	"fmt"
	"io"
	"strings"
)

// notificationCommand owns queue workflows whose noun-first canonical
// spelling does not fit the shared resource verbs. It is deliberately only a
// dispatcher: the notify command remains the single parser and implementation.
type notificationCommand struct {
	notify rawArgvCommand
}

var notificationSubcommands = []string{"ack", "reconcile"}

func newNotificationCommand() *notificationCommand {
	return &notificationCommand{}
}

func (c *notificationCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("notification requires a subcommand: %s", strings.Join(notificationSubcommands, ", ")))
	}
	switch args[0] {
	case "ack":
		return forwardRawArgv(c.notify, "notification ack", "notify", []string{"ack"}, args[1:], stdout, stderr)
	case "reconcile":
		return forwardRawArgv(c.notify, "notification reconcile", "notify", []string{"reconcile"}, args[1:], stdout, stderr)
	default:
		return usageError(fmt.Sprintf("notification %s is not available; this release implements: %s",
			args[0], strings.Join(notificationSubcommands, ", ")))
	}
}
