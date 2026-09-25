package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// superviseArgv wraps one child argv in the managed process supervisor.
//
// The result is an argv, not a shell string: tmux execs `split-window`'s
// trailing words directly, so the child's own argv survives verbatim through
// the wrapper instead of being re-quoted through a second shell.
func superviseArgv(binary string, spec superviseSpec, argv0 string, child []string) []string {
	argv := []string{binary, "internal", "supervise", "--pane-uid", spec.PaneUID, "--generation", spec.Generation}
	if spec.AgentUID != "" {
		argv = append(argv, "--agent-uid", spec.AgentUID)
	}
	if spec.OperationID != "" {
		argv = append(argv, "--operation-id", spec.OperationID)
	}
	if spec.RegistryPath != "" {
		argv = append(argv, "--registry-path", spec.RegistryPath)
	}
	if spec.DialogueReplyOnly {
		argv = append(argv, "--"+claudeDialogueReplyOnlyFlag)
	}
	if argv0 != "" {
		argv = append(argv, "--argv0", argv0)
	}
	argv = append(argv, "--")
	return append(argv, child...)
}

// resolvedPaneCommand is the exact process tmux would have started for a pane
// created with no command of its own.
type resolvedPaneCommand struct {
	argv  []string
	argv0 string
}

// executablePath resolves this build's own binary, which the supervised child
// argv has to name.
func (m *materializer) executablePath() (string, error) {
	resolve := m.executable
	if resolve == nil {
		resolve = os.Executable
	}
	path, err := resolve()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" {
		return "", errors.New("resolved an empty executable path")
	}
	return path, nil
}

func (m *materializer) environment(name string) string {
	lookup := m.lookupEnv
	if lookup == nil {
		lookup = os.Getenv
	}
	return strings.TrimSpace(lookup(name))
}

// defaultPaneCommand reproduces the process tmux itself would have started for
// a pane created without a command.
//
// tmux's documented rule is the one implemented here: `default-command`, when
// non-empty, is run as an sh(1) command by `default-shell`; when empty, the
// pane gets a *login* shell, which is spelled by prefixing argv[0] with a
// dash. Getting either half wrong would change what the operator's shell reads
// at startup, so the values are read from the same exact server the pane is
// created on rather than guessed from this process's environment.
func (m *materializer) defaultPaneCommand(ctx context.Context) (resolvedPaneCommand, error) {
	if memo := m.paneDefaults; memo != nil && m.expectedSocketPath != "" && memo.socket == filepath.Clean(m.expectedSocketPath) {
		return m.resolvePaneCommand(memo.shell, memo.command), nil
	}
	shell, err := m.read(ctx, "show-options", "-gv", "default-shell")
	if err != nil {
		return resolvedPaneCommand{}, fmt.Errorf("read tmux default-shell: %w", err)
	}
	command, err := m.read(ctx, "show-options", "-gv", "default-command")
	if err != nil {
		return resolvedPaneCommand{}, fmt.Errorf("read tmux default-command: %w", err)
	}
	return m.resolvePaneCommand(shell, command), nil
}

func (m *materializer) resolvePaneCommand(shell, command string) resolvedPaneCommand {
	shell = strings.TrimSpace(shell)
	if shell == "" {
		shell = m.environment("SHELL")
	}
	if shell == "" {
		shell = "/bin/sh"
	}
	if command = strings.TrimSpace(command); command != "" {
		return resolvedPaneCommand{argv: []string{shell, "-c", command}, argv0: filepath.Base(shell)}
	}
	return resolvedPaneCommand{argv: []string{shell}, argv0: "-" + filepath.Base(shell)}
}

// paneDefaultsMemo is the default-shell/default-command pair read before the
// Registry lock, keyed by the exact physical socket it was read through.
type paneDefaultsMemo struct {
	socket  string
	shell   string
	command string
}

var (
	paneDefaultsReadShell   = routeRead{"show-options", "-gv", "default-shell"}
	paneDefaultsReadCommand = routeRead{"show-options", "-gv", "default-command"}
)

// prefetchPaneDefaults reads, before a create takes the Registry lock, the two
// options defaultPaneCommand would otherwise read while the lock is held, in
// one tmux invocation through the same routed runner, and returns the clear
// the caller defers to the end of its transaction.
//
// It is best effort and silent. No bound physical socket yet (no server before
// the first create) or a failed read leaves no memo, and defaultPaneCommand
// then reads inside the lock exactly as it always did, with the same error
// texts. The memo answers only while the route still reads through the socket
// it was taken from.
//
// Trade-off: the values are read a few milliseconds earlier than before, ahead
// of the lock rather than under it. They are best-effort launch configuration
// for the new pane's process, not a Registry decision, so nothing the lock
// protects depends on them being read under it.
func (m *materializer) prefetchPaneDefaults(ctx context.Context) func() {
	clearMemo := func() {
		if m != nil {
			m.paneDefaults = nil
		}
	}
	if m == nil || m.expectedSocketPath == "" || !filepath.IsAbs(m.expectedSocketPath) {
		return clearMemo
	}
	socket := filepath.Clean(m.expectedSocketPath)
	sections, err := readTmuxSequence(ctx, m.routedRunner(), paneDefaultsReadShell, paneDefaultsReadCommand)
	if err != nil {
		return clearMemo
	}
	m.paneDefaults = &paneDefaultsMemo{socket: socket, shell: strings.TrimSpace(sections[0]), command: strings.TrimSpace(sections[1])}
	return clearMemo
}

// supervisedLaunch returns the argv a managed pane should be created with.
//
// Shell Pane compatibility keeps the historical fallback: if its supervisor
// cannot be constructed, the caller's original command is returned and a later
// consumer resolves the missing receipt as `unknown`. Agent launches have one
// stricter boundary. Once the supervisor binary is available, failure to
// resolve the creator's Registry authority keeps that supervisor in the argv
// with an empty authority so admission fails closed before provider start.
func (m *materializer) supervisedLaunch(ctx context.Context, spec superviseSpec, command []string) []string {
	if m == nil || !spec.valid() {
		return command
	}
	binary, err := m.executablePath()
	if err != nil {
		m.warnUnsupervised(spec, fmt.Sprintf("resolve the projmux binary: %v", err))
		return command
	}
	if spec.AgentUID != "" {
		registryPath, pathErr := creatorRegistryPath()
		if pathErr != nil {
			// Keep the internal supervisor in place with an empty authority. Its
			// Agent admission fails closed before provider start; silently falling
			// back to an ungated provider here would reopen the ownership race.
			if m.warn != nil {
				fmt.Fprintf(m.warn, "projmux: pane %s Agent activation will fail closed because the creator Registry authority cannot be resolved: %v\n", spec.PaneUID, pathErr)
			}
		} else {
			spec.RegistryPath = registryPath
		}
	}
	child := command
	argv0 := ""
	if len(child) == 0 {
		resolved, err := m.defaultPaneCommand(ctx)
		if err != nil {
			m.warnUnsupervised(spec, err.Error())
			return command
		}
		child, argv0 = resolved.argv, resolved.argv0
	}
	return superviseArgv(binary, spec, argv0, child)
}

func creatorRegistryPath() (string, error) {
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(intmetadata.PathFor(paths.StateDir))
	if err != nil {
		return "", fmt.Errorf("resolve absolute Registry path: %w", err)
	}
	return filepath.Clean(path), nil
}

func (m *materializer) warnUnsupervised(spec superviseSpec, reason string) {
	if m == nil || m.warn == nil {
		return
	}
	fmt.Fprintf(m.warn, "projmux: pane %s starts without termination evidence because %s\n", spec.PaneUID, reason)
}

// mintGeneration issues one activation generation for a create/resume route.
func (c *createCommand) mintGeneration() (string, error) {
	mint := c.newGeneration
	if mint == nil {
		mint = coremetadata.NewGeneration
	}
	return mint()
}

// issuePaneActivation mints a generation and binds it to one allocated Pane
// inside the caller's open transaction.
//
// It runs in the metadata phase, before any tmux call, because the generation
// is an *input* to the launch argv: the process has to be started already
// knowing the value it will quote back. Recording it first also means a create
// that fails during materialization leaves a Pane whose generation matches
// nothing that ever ran, which is exactly the state a receipt guard rejects.
func (c *createCommand) issuePaneActivation(
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	paneUID, agentUID, operationID string,
) (superviseSpec, error) {
	return issuePaneActivation(c.mintGeneration, working, mutator, paneUID, agentUID, operationID)
}

// issuePaneActivation is the route-independent half: mint, record, and return
// the spec the launch argv is built from.
func issuePaneActivation(
	mint func() (string, error),
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	paneUID, agentUID, operationID string,
) (superviseSpec, error) {
	if mint == nil {
		mint = coremetadata.NewGeneration
	}
	generation, err := mint()
	if err != nil {
		return superviseSpec{}, err
	}
	if _, err := mutator.RecordPaneActivation(working, paneUID, coremetadata.PaneActivationOptions{
		Generation:  generation,
		AgentUID:    agentUID,
		OperationID: operationID,
	}); err != nil {
		return superviseSpec{}, MapMetadataError(err)
	}
	return superviseSpec{PaneUID: paneUID, AgentUID: agentUID, Generation: generation, OperationID: operationID}, nil
}

// observeActivationRuntime records the exact tmux handle the generation landed
// on. A failure is a diagnostic, never a create failure: the binding the
// receipt guard uses is the generation, and it is already durable.
func observeActivationRuntime(
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	spec superviseSpec,
	paneID string,
	warn io.Writer,
) {
	if !spec.valid() || strings.TrimSpace(paneID) == "" {
		return
	}
	if _, err := mutator.ObservePaneActivationRuntime(working, spec.PaneUID, spec.Generation, paneID); err != nil && warn != nil {
		fmt.Fprintf(warn, "projmux: pane %s activation runtime handle was not recorded: %v\n", spec.PaneUID, err)
	}
}
