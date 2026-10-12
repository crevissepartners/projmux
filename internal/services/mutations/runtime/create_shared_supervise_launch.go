package runtime

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

// SuperviseArgv wraps one child argv in the managed process supervisor.
//
// The result is an argv, not a shell string: tmux execs `split-window`'s
// trailing words directly, so the child's own argv survives verbatim through
// the wrapper instead of being re-quoted through a second shell.
func SuperviseArgv(binary string, spec SuperviseSpec, argv0 string, child []string) []string {
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
		argv = append(argv, "--"+DialogueReplyOnlyFlag)
	}
	if spec.ServerDefaultCommand {
		argv = append(argv, "--"+SuperviseServerDefaultCommandFlag)
	}
	if argv0 != "" {
		argv = append(argv, "--argv0", argv0)
	}
	argv = append(argv, "--")
	return append(argv, child...)
}

// ResolvedPaneCommand is the exact process tmux would have started for a pane
// created with no command of its own.
type ResolvedPaneCommand struct {
	Argv  []string
	Argv0 string
}

// executablePath resolves this build's own binary, which the supervised child
// argv has to name.
func (m *Materializer) ExecutablePath() (string, error) {
	resolve := m.Executable
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

func (m *Materializer) Environment(name string) string {
	lookup := m.LookupEnv
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
func (m *Materializer) DefaultPaneCommand(ctx context.Context) (ResolvedPaneCommand, error) {
	if memo := m.PaneDefaults; memo != nil && m.ExpectedSocketPath != "" && memo.Socket == filepath.Clean(m.ExpectedSocketPath) {
		return ResolvePaneCommand(memo.Shell, memo.Command, m.Environment("SHELL")), nil
	}
	shell, err := m.ReadTmux(ctx, "show-options", "-gv", "default-shell")
	if err != nil {
		return ResolvedPaneCommand{}, fmt.Errorf("read tmux default-shell: %w", err)
	}
	command, err := m.ReadTmux(ctx, "show-options", "-gv", "default-command")
	if err != nil {
		return ResolvedPaneCommand{}, fmt.Errorf("read tmux default-command: %w", err)
	}
	return ResolvePaneCommand(shell, command, m.Environment("SHELL")), nil
}

// ResolvePaneCommand applies tmux's rule to one default-shell/default-command
// pair; envShell stands in for an empty default-shell, as tmux's own SHELL does.
func ResolvePaneCommand(shell, command, envShell string) ResolvedPaneCommand {
	shell = strings.TrimSpace(shell)
	if shell == "" {
		shell = strings.TrimSpace(envShell)
	}
	if shell == "" {
		shell = "/bin/sh"
	}
	if command = strings.TrimSpace(command); command != "" {
		return ResolvedPaneCommand{Argv: []string{shell, "-c", command}, Argv0: filepath.Base(shell)}
	}
	return ResolvedPaneCommand{Argv: []string{shell}, Argv0: "-" + filepath.Base(shell)}
}

// PaneDefaultsMemo is the default-shell/default-command pair read before the
// Registry lock, keyed by the exact physical socket it was read through.
type PaneDefaultsMemo struct {
	Socket  string
	Shell   string
	Command string
}

var (
	paneDefaultsReadShell   = RouteRead{"show-options", "-gv", "default-shell"}
	paneDefaultsReadCommand = RouteRead{"show-options", "-gv", "default-command"}
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
func (m *Materializer) PrefetchPaneDefaults(ctx context.Context) func() {
	clearMemo := func() {
		if m != nil {
			m.PaneDefaults = nil
		}
	}
	if m == nil || m.ExpectedSocketPath == "" || !filepath.IsAbs(m.ExpectedSocketPath) {
		return clearMemo
	}
	socket := filepath.Clean(m.ExpectedSocketPath)
	sections, err := ReadTmuxSequence(ctx, m.RoutedRunner(), paneDefaultsReadShell, paneDefaultsReadCommand)
	if err != nil {
		return clearMemo
	}
	m.PaneDefaults = &PaneDefaultsMemo{Socket: socket, Shell: strings.TrimSpace(sections[0]), Command: strings.TrimSpace(sections[1])}
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
func (m *Materializer) SupervisedLaunch(ctx context.Context, spec SuperviseSpec, command []string) []string {
	if m == nil || !spec.Valid() {
		return command
	}
	binary, err := m.ExecutablePath()
	if err != nil {
		m.WarnUnsupervised(spec, fmt.Sprintf("resolve the projmux binary: %v", err))
		return command
	}
	if spec.AgentUID != "" && spec.RegistryPath == "" {
		registryPath, pathErr := CreatorRegistryPath()
		if pathErr != nil {
			// Keep the internal supervisor in place with an empty authority. Its
			// Agent admission fails closed before provider start; silently falling
			// back to an ungated provider here would reopen the ownership race.
			if m.Warn != nil {
				fmt.Fprintf(m.Warn, "projmux: pane %s Agent activation will fail closed because the creator Registry authority cannot be resolved: %v\n", spec.PaneUID, pathErr)
			}
		} else {
			spec.RegistryPath = registryPath
		}
	}
	child := command
	argv0 := ""
	if len(child) == 0 {
		resolved, err := m.DefaultPaneCommand(ctx)
		if err != nil {
			m.WarnUnsupervised(spec, err.Error())
			return command
		}
		child, argv0 = resolved.Argv, resolved.Argv0
	}
	return SuperviseArgv(binary, spec, argv0, child)
}

// supervisedLaunchOnFreshServer is supervisedLaunch for the first Pane of a
// server this new-session starts. No server exists to read default-shell and
// default-command from yet, so the supervisor reads them from the server it
// runs in. The fallback is supervisedLaunch's: an unsupervised default Pane.
func (m *Materializer) SupervisedLaunchOnFreshServer(spec SuperviseSpec) []string {
	if m == nil || !spec.Valid() || spec.AgentUID != "" {
		return nil
	}
	binary, err := m.ExecutablePath()
	if err != nil {
		m.WarnUnsupervised(spec, fmt.Sprintf("resolve the projmux binary: %v", err))
		return nil
	}
	spec.ServerDefaultCommand = true
	return SuperviseArgv(binary, spec, "", nil)
}

func CreatorRegistryPath() (string, error) {
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

func (m *Materializer) WarnUnsupervised(spec SuperviseSpec, reason string) {
	if m == nil || m.Warn == nil {
		return
	}
	fmt.Fprintf(m.Warn, "projmux: pane %s starts without termination evidence because %s\n", spec.PaneUID, reason)
}

// IssuePaneActivation is the route-independent half: mint, record, and return
// the spec the launch argv is built from.
func IssuePaneActivation(
	mint func() (string, error),
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	paneUID, agentUID, operationID string,
) (SuperviseSpec, error) {
	if mint == nil {
		mint = coremetadata.NewGeneration
	}
	generation, err := mint()
	if err != nil {
		return SuperviseSpec{}, err
	}
	if _, err := mutator.RecordPaneActivation(working, paneUID, coremetadata.PaneActivationOptions{
		Generation:  generation,
		AgentUID:    agentUID,
		OperationID: operationID,
	}); err != nil {
		return SuperviseSpec{}, MapMetadataError(err)
	}
	return SuperviseSpec{PaneUID: paneUID, AgentUID: agentUID, Generation: generation, OperationID: operationID}, nil
}

// ObserveActivationRuntime records the exact tmux handle the generation landed
// on. A failure is a diagnostic, never a create failure: the binding the
// receipt guard uses is the generation, and it is already durable.
func ObserveActivationRuntime(
	working *coremetadata.Registry,
	mutator coremetadata.Mutator,
	spec SuperviseSpec,
	paneID string,
	warn io.Writer,
) {
	if !spec.Valid() || strings.TrimSpace(paneID) == "" {
		return
	}
	if _, err := mutator.ObservePaneActivationRuntime(working, spec.PaneUID, spec.Generation, paneID); err != nil && warn != nil {
		fmt.Fprintf(warn, "projmux: pane %s activation runtime handle was not recorded: %v\n", spec.PaneUID, err)
	}
}
