package app

import (
	"fmt"
	"io"
	"strings"
)

// runtimeSubcommands lists the runtime-domain routes, in help order.
var runtimeSubcommands = []string{"sessions", "attach", "stop", "tag", "prune"}

// runtimeCommand owns the runtime domain: the live and ephemeral tmux inventory
// that is not a Projmux resource.
//
// The split this route makes is the contract's, not a new behavior. A live tmux
// session has no uid, no name reservation, and no ownerRef, so listing,
// attaching, stopping, tagging, and trimming sessions belong to a runtime
// namespace rather than to a resource verb. Every subcommand here forwards raw
// argv to the handler that already owns the behavior, so stdout, stderr, the
// exit code, and the side effects are identical to the current spelling.
//
// `runtime attach` deliberately reaches the auto-attach policy and never the
// Project entry point: entering a Project runtime, including materializing it,
// is `attach project`.
type runtimeCommand struct {
	sessions rawArgvCommand
	attach   rawArgvCommand
	kill     rawArgvCommand
	tag      rawArgvCommand
	prune    rawArgvCommand
}

func newRuntimeCommand() *runtimeCommand {
	return &runtimeCommand{}
}

// Run dispatches one `runtime <subcommand>` invocation.
func (c *runtimeCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("runtime requires a subcommand: %s", strings.Join(runtimeSubcommands, ", ")))
	}
	rest := args[1:]
	switch args[0] {
	case "sessions":
		return forwardRawArgv(c.sessions, "runtime sessions", "sessions", nil, rest, stdout, stderr)
	case "attach":
		return forwardRawArgv(c.attach, "runtime attach", "attach", []string{"auto"}, rest, stdout, stderr)
	case "stop":
		return forwardRawArgv(c.kill, "runtime stop", "kill", []string{"tagged"}, rest, stdout, stderr)
	case "tag":
		return forwardRawArgv(c.tag, "runtime tag", "tag", nil, rest, stdout, stderr)
	case "prune":
		return forwardRawArgv(c.prune, "runtime prune", "prune", []string{"ephemeral"}, rest, stdout, stderr)
	default:
		return usageError(fmt.Sprintf("runtime %s is not available; this release implements: %s",
			args[0], strings.Join(runtimeSubcommands, ", ")))
	}
}

// forwardRawArgv prefixes the current spelling's leading tokens and hands the
// rest of argv through untouched.
func forwardRawArgv(target rawArgvCommand, spelling, route string, prefix, args []string, stdout, stderr io.Writer) error {
	if target == nil {
		return fmt.Errorf("%s: the %s handler is not configured", spelling, route)
	}
	forwarded := make([]string, 0, len(prefix)+len(args))
	forwarded = append(forwarded, prefix...)
	forwarded = append(forwarded, args...)
	return target.Run(forwarded, stdout, stderr)
}

// restoreCommand implements the canonical `restore` verb. Snapshot restore is
// owned by the session-state handler, so this is a parity alias over it.
type restoreCommand struct {
	snapshots rawArgvCommand
}

func newRestoreCommand() *restoreCommand {
	return &restoreCommand{}
}

// Run dispatches one `restore <kind>` invocation.
func (c *restoreCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError("restore requires a resource kind: snapshot")
	}
	if args[0] != "snapshot" {
		return usageError(fmt.Sprintf("restore %s is not available; this release implements: snapshot", args[0]))
	}
	return forwardRawArgv(c.snapshots, "restore snapshot", "session-state", []string{"restore"}, args[1:], stdout, stderr)
}
