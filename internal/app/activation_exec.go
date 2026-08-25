package app

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// activationExecCommand is the commit gate immediately supervised by an Agent
// Pane. It starts as the supervisor's child, waits on the creator's Registry
// lock, validates the exact committed activation, then replaces itself with the
// provider. Keeping the gate inside the child means the supervisor can always
// reap an external HUP, even while create still owns the commit boundary.
type activationExecCommand struct {
	store     *resourceStore
	lookupEnv func(string) (string, bool)
	exec      func(argv []string, argv0 string, spec superviseSpec) error
	failure   io.Writer
}

func newActivationExecCommand() *activationExecCommand {
	return &activationExecCommand{lookupEnv: os.LookupEnv, exec: execCommittedActivation}
}

func (c *activationExecCommand) Run(args []string, stdout, stderr io.Writer) (runErr error) {
	fs := flag.NewFlagSet("internal activation-exec", flag.ContinueOnError)
	fs.SetOutput(stderr)
	paneUID := fs.String("pane-uid", "", "Pane resource uid this process is the runtime of")
	agentUID := fs.String("agent-uid", "", "Agent resource uid owning the Pane")
	generation := fs.String("generation", "", "activation generation this launch was issued")
	operationID := fs.String("operation-id", "", "create/resume operation that issued the generation")
	registryPath := fs.String("registry-path", "", "private creator-resolved Registry authority")
	argv0 := fs.String("argv0", "", "argv[0] the provider is exec'd with")
	failureFD := fs.Int("failure-fd", -1, "private supervisor admission failure descriptor")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return usageError(err.Error())
	}
	failure := c.failure
	var failureFile *os.File
	if failure == nil && *failureFD >= 3 {
		failureFile = os.NewFile(uintptr(*failureFD), "projmux-activation-failure")
		if failureFile == nil {
			return errors.New("internal activation-exec: invalid failure descriptor")
		}
		markActivationFailureCloseOnExec(*failureFD)
		defer failureFile.Close()
		failure = failureFile
	}
	defer func() {
		if runErr != nil && failure != nil {
			// The content is intentionally typed and constant: the supervisor only
			// needs to distinguish pre-exec failure from provider exit, and no argv,
			// path, or provider payload belongs in the transport.
			_, _ = io.WriteString(failure, "activation-failed\n")
		}
	}()
	child := fs.Args()
	if len(child) == 0 {
		return usageError("internal activation-exec requires a command after --")
	}
	spec := superviseSpec{
		PaneUID: strings.TrimSpace(*paneUID), AgentUID: strings.TrimSpace(*agentUID),
		Generation: strings.TrimSpace(*generation), OperationID: strings.TrimSpace(*operationID),
		RegistryPath: *registryPath,
	}
	if !spec.valid() || spec.AgentUID == "" || spec.OperationID == "" {
		return usageError("internal activation-exec requires --pane-uid, --agent-uid, --generation, and --operation-id")
	}
	if err := exactActivationRegistryPath(spec.RegistryPath); err != nil {
		return fmt.Errorf("internal activation-exec: %w", err)
	}
	if err := c.awaitCommittedActivation(spec); err != nil {
		return fmt.Errorf("internal activation-exec: activation admission: %w", err)
	}
	execProvider := c.exec
	if execProvider == nil {
		execProvider = execCommittedActivation
	}
	return execProvider(child, strings.TrimSpace(*argv0), spec)
}

// awaitCommittedActivation closes the ownership handoff between a create
// transaction and the managed provider it has just materialized.
//
// tmux starts this command while the creator still holds the Registry
// transaction. Entering UpdateConvergent therefore waits on that same
// cross-process lock. The callback admits the provider only after the committed
// Registry contains the exact Agent/Pane/runtime/generation/operation binding;
// it deliberately changes no field, so success performs zero Registry writes.
func (c *activationExecCommand) awaitCommittedActivation(spec superviseSpec) error {
	store := c.store
	if store == nil {
		if err := exactActivationRegistryPath(spec.RegistryPath); err != nil {
			return err
		}
		store = resourceStoreAtPath(spec.RegistryPath)
	}
	if store.updateConvergent == nil {
		return errors.New("resource registry convergence store is not configured")
	}
	lookupEnv := c.lookupEnv
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	runtimeID, ok := lookupEnv("TMUX_PANE")
	runtimeID = strings.TrimSpace(runtimeID)
	if !ok || runtimeID == "" {
		return errors.New("TMUX_PANE is empty")
	}
	if exactTmuxHandle(runtimeID, "%") == "" {
		return fmt.Errorf("TMUX_PANE %q is not an exact tmux Pane handle", runtimeID)
	}

	_, changed, err := store.updateConvergent(func(registry *coremetadata.Registry) error {
		pane, ok := registry.Pane(spec.PaneUID)
		if !ok {
			return fmt.Errorf("pane %s is not committed", spec.PaneUID)
		}
		activation := pane.Status.Activation
		if activation.Generation != spec.Generation || activation.OperationID != spec.OperationID ||
			activation.RuntimeID != runtimeID || activation.AgentUID != spec.AgentUID {
			return fmt.Errorf("pane %s does not carry the exact committed activation", spec.PaneUID)
		}
		if pane.Spec.Role != coremetadata.PaneRoleAgent || pane.Metadata.OwnerRef == nil ||
			pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent || pane.Metadata.OwnerRef.UID != spec.AgentUID {
			return fmt.Errorf("pane %s is not owned by agent %s", spec.PaneUID, spec.AgentUID)
		}
		agent, ok := registry.Agent(spec.AgentUID)
		if !ok || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef != spec.PaneUID {
			return fmt.Errorf("agent %s does not carry the exact Running pane binding", spec.AgentUID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if changed {
		return errors.New("activation admission unexpectedly changed the Registry")
	}
	return nil
}

func activationExecArgv(binary string, spec superviseSpec, argv0 string, failureFD int, child []string) []string {
	argv := []string{binary, "internal", "activation-exec",
		"--pane-uid", spec.PaneUID,
		"--agent-uid", spec.AgentUID,
		"--generation", spec.Generation,
		"--operation-id", spec.OperationID,
		"--registry-path", spec.RegistryPath,
	}
	if failureFD >= 3 {
		argv = append(argv, "--failure-fd", fmt.Sprintf("%d", failureFD))
	}
	if argv0 != "" {
		argv = append(argv, "--argv0", argv0)
	}
	argv = append(argv, "--")
	return append(argv, child...)
}

func activationEnvironment(spec superviseSpec) []string {
	return []string{
		internalActivationPaneUIDEnv + "=" + spec.PaneUID,
		internalActivationGenerationEnv + "=" + spec.Generation,
	}
}
