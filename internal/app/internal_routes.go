package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
)

// internalSubcommands lists the hidden internal plumbing namespaces, in help
// order.
var internalSubcommands = []string{
	"tmux",
	"status",
	"statusbar",
	"preview",
	"session-popup",
	"agent-pane",
	"agent-hook",
	"focus",
	"key-broker",
	"popup-wait-key",
	"supervise",
	"activation-exec",
	"codex-broker",
	"codex-generation-launch",
	"install-residue",
}

// internalAgentHookSubcommands lists the provider hook plumbing routes.
var internalAgentHookSubcommands = []string{"ingest", "watch-title"}

// internalAgentPaneSubcommands are generated-config launch bridges. They keep
// the saved default and interactive picker behavior available to tmux without
// restoring the retired public `ai` namespace.
var internalAgentPaneSubcommands = []string{"launch-default", "launch-provider", "launch-shell", "picker"}

// internalCommand owns the hidden `internal` namespace: the plumbing invoked by
// generated tmux config, tmux hooks, popup payloads, and provider hook commands
// rather than by a user typing at a prompt.
//
// The split this route makes is the contract's, not a new behavior. A status
// segment renderer, a status bar click dispatcher, a preview cursor, a popup
// payload, a hook ingest sink, and two single-purpose key helpers are all
// machine-invoked surfaces with no place in a user-facing noun/verb model, so
// they belong behind one hidden namespace instead of sitting in the primary
// listing next to `switch` and `doctor`.
//
// Every subcommand forwards raw argv to the handler that already owns the
// behavior, so stdout, stderr, the exit code, and the side effects remain on
// the manual leaf parser. Phase 2 removed the old pre-namespace entrypoints;
// generated plumbing reaches these handlers only through `internal`.
type internalCommand struct {
	tmux         rawArgvCommand
	status       rawArgvCommand
	statusbar    rawArgvCommand
	preview      rawArgvCommand
	sessionPopup rawArgvCommand
	// ai owns both provider hook ingest and the pane title watcher today, so
	// `internal agent-hook` forwards into it with the current leading token.
	ai           rawArgvCommand
	focus        rawArgvCommand
	keyBroker    rawArgvCommand
	popupWaitKey rawArgvCommand
	// supervise is the managed process supervisor a launched Pane execs. It is
	// the only internal route whose caller is a pane's own argv rather than a
	// tmux command string.
	supervise      rawArgvCommand
	activationExec rawArgvCommand
	// codexBroker is the Codex endpoint broker runtime. It is the only
	// internal route whose process outlives the invocation that started it.
	codexBroker rawArgvCommand
}

func newInternalCommand() *internalCommand {
	return &internalCommand{}
}

// Run dispatches one `internal <namespace>` invocation.
func (c *internalCommand) Run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("internal requires a subcommand: %s", strings.Join(internalSubcommands, ", ")))
	}
	rest := args[1:]
	switch args[0] {
	case "tmux":
		return forwardRawArgv(c.tmux, "internal tmux", "tmux", nil, rest, stdout, stderr)
	case "status":
		return forwardRawArgv(c.status, "internal status", "status", nil, rest, stdout, stderr)
	case "statusbar":
		return forwardRawArgv(c.statusbar, "internal statusbar", "statusbar", nil, rest, stdout, stderr)
	case "preview":
		return forwardRawArgv(c.preview, "internal preview", "preview", nil, rest, stdout, stderr)
	case "session-popup":
		return forwardRawArgv(c.sessionPopup, "internal session-popup", "session-popup", nil, rest, stdout, stderr)
	case "agent-pane":
		return c.runAgentPane(rest, stdout, stderr)
	case "agent-hook":
		return c.runAgentHook(rest, stdout, stderr)
	case "focus":
		return forwardRawArgv(c.focus, "internal focus", "focus", nil, rest, stdout, stderr)
	case "key-broker":
		return forwardRawArgv(c.keyBroker, "internal key-broker", "key-broker", nil, rest, stdout, stderr)
	case "popup-wait-key":
		return forwardRawArgv(c.popupWaitKey, "internal popup-wait-key", "popup-wait-key", nil, rest, stdout, stderr)
	case "supervise":
		return forwardRawArgv(c.supervise, "internal supervise", "supervise", nil, rest, stdout, stderr)
	case "activation-exec":
		return forwardRawArgv(c.activationExec, "internal activation-exec", "activation-exec", nil, rest, stdout, stderr)
	case "claude-endpoint-register":
		return runClaudeEndpointRegistration(rest)
	case "claude-endpoint-helper":
		return runClaudeEndpointHelper(rest)
	case "claude-message-wait":
		return runClaudeMessageWait(rest, stderr)
	case "codex-broker":
		return forwardRawArgv(c.codexBroker, "internal codex-broker", "codex-broker", nil, rest, stdout, stderr)
	case "codex-generation-launch":
		return codexgenerationhost.RunDurableLaunchSupervisor(rest)
	case "install-residue":
		// The install residue census. It is machine-invoked plumbing -- the
		// last step of `make install` and of the npm wrapper's first
		// interactive run after an install -- rather than a command a user
		// types, which is exactly what this namespace is for. It reads the
		// process table, appends one ledger row, and never fails.
		return runInstallResidueReport(rest, stdout, stderr)
	default:
		return usageError(fmt.Sprintf("internal %s is not available; this release implements: %s",
			args[0], strings.Join(internalSubcommands, ", ")))
	}
}

func (c *internalCommand) runAgentPane(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("internal agent-pane requires a subcommand: %s",
			strings.Join(internalAgentPaneSubcommands, ", ")))
	}
	switch args[0] {
	case "launch-default":
		return forwardRawArgv(c.ai, "internal agent-pane launch-default", "ai", []string{"launch-default"}, args[1:], stdout, stderr)
	case "launch-provider":
		return forwardRawArgv(c.ai, "internal agent-pane launch-provider", "ai", []string{"launch-provider"}, args[1:], stdout, stderr)
	case "launch-shell":
		return forwardRawArgv(c.ai, "internal agent-pane launch-shell", "ai", []string{"launch-shell"}, args[1:], stdout, stderr)
	case "picker":
		return forwardRawArgv(c.ai, "internal agent-pane picker", "ai", []string{"picker"}, args[1:], stdout, stderr)
	default:
		return usageError(fmt.Sprintf("internal agent-pane %s is not available; this release implements: %s",
			args[0], strings.Join(internalAgentPaneSubcommands, ", ")))
	}
}

// runAgentHook dispatches the provider hook plumbing. Both routes forward into
// the `ai` handler with its current leading token, so the ingest payload
// contract and the watcher behavior are untouched.
func (c *internalCommand) runAgentHook(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return usageError(fmt.Sprintf("internal agent-hook requires a subcommand: %s",
			strings.Join(internalAgentHookSubcommands, ", ")))
	}
	switch args[0] {
	case "ingest":
		return forwardRawArgv(c.ai, "internal agent-hook ingest", "ai", []string{"ingest"}, args[1:], stdout, stderr)
	case "watch-title":
		return forwardRawArgv(c.ai, "internal agent-hook watch-title", "ai", []string{"watch-title"}, args[1:], stdout, stderr)
	default:
		return usageError(fmt.Sprintf("internal agent-hook %s is not available; this release implements: %s",
			args[0], strings.Join(internalAgentHookSubcommands, ", ")))
	}
}
