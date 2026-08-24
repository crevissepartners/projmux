package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	coresessions "github.com/crevissepartners/projmux/internal/core/sessions"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/sessionstate"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/platformkeys"
	intpicker "github.com/crevissepartners/projmux/internal/ui/picker"
)

const (
	defaultAppSocket  = "projmux"
	defaultAppSession = "home"
)

type shellCommand struct {
	diagnostics  *diagnostics.LifecycleRecorder
	executable   func() (string, error)
	lookupEnv    func(string) string
	homeDir      func() (string, error)
	welcomeInput io.Reader
	writeFile    func(string, []byte, os.FileMode) error
	readFile     func(string) ([]byte, error)
	runCommand   func(ctx context.Context, env []string, name string, args ...string) error
	startCommand func(ctx context.Context, env []string, name string, args ...string) error
	tmuxRunner   tmuxRunner
	sessionStore func() (sessionstate.Store, error)
	update       *updateCommand
	nativePicker intpicker.Runner
	getwd        func() (string, error)
	goos         func() string
	nativeKeys   func() bool
	now          func() time.Time
	// controlSession builds the control-session convergence pass over this
	// invocation's tmux runner and configured shell.
	//
	// It is a factory rather than a value because the pass needs the resolved
	// shell path, which is a per-invocation lookup, and because a nil field must
	// disable the whole pass: a unit test that only measures the attach argv has
	// no tmux server to observe, and the control marker is not what it is
	// measuring. A nil pass degrades to exactly the pre-marker behavior.
	controlSession func(runner tmuxRunner, shell string) controlSessionPass
	projectSession func(context.Context, string, shellTarget) error
}

// controlSessionPass is the narrow seam `shell` drives the control-session
// convergence through. See internal/app/control_session.go for the contract.
type controlSessionPass interface {
	converge(ctx context.Context, socketName, sessionName string) (controlSessionConvergence, error)
}

type shellUpdateSkipState struct {
	Version   int       `json:"version"`
	TagName   string    `json:"tag_name"`
	SkippedAt time.Time `json:"skipped_at"`
}

func newShellCommand(update *updateCommand, recorders ...*diagnostics.LifecycleRecorder) *shellCommand {
	var recorder *diagnostics.LifecycleRecorder
	if len(recorders) > 0 {
		recorder = recorders[0]
	}
	command := &shellCommand{
		diagnostics:  recorder,
		executable:   resolveExecutablePath,
		lookupEnv:    os.Getenv,
		homeDir:      os.UserHomeDir,
		welcomeInput: os.Stdin,
		writeFile:    os.WriteFile,
		readFile:     os.ReadFile,
		runCommand:   runForegroundCommand,
		startCommand: startBackgroundCommand,
		tmuxRunner:   shellTmuxExecRunner{},
		update:       update,
		nativePicker: intpicker.NativeRunner{In: os.Stdin, Out: os.Stdout},
		getwd:        os.Getwd,
		goos:         func() string { return runtime.GOOS },
		nativeKeys:   platformkeys.Available,
		now:          time.Now,
		controlSession: func(runner tmuxRunner, shell string) controlSessionPass {
			return newControlSessionConverger(runner, shell)
		},
	}
	command.projectSession = command.materializeProjectDefaultSession
	return command
}

func (c *shellCommand) Run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("shell", flag.ContinueOnError)
	fs.SetOutput(stderr)
	socket := fs.String("socket", defaultAppSocket, "tmux socket name for the projmux app")
	session := fs.String("session", defaultAppSession, "tmux session name for the projmux app")
	configPath := fs.String("config", "", "tmux config path for the projmux app")
	binaryOverride := fs.String("bin", "", "projmux binary path to write into the app config")
	noInstall := fs.Bool("no-install", false, "run without writing the app tmux config")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		printShellUsage(stderr)
		return errors.New("shell does not accept positional arguments")
	}
	sessionExplicit := flagSetExplicitly(fs, "session")

	socketName := nonEmpty(strings.TrimSpace(*socket), defaultAppSocket)
	if c.insideAppSocket(socketName) {
		return fmt.Errorf("projmux shell cannot run inside the %q projmux tmux server", socketName)
	}

	if _, err := c.promptWelcome(stdout, stderr); err != nil {
		return err
	}

	binaryPath, err := c.resolveBinary(*binaryOverride)
	if err != nil {
		return err
	}
	config := c.expandHome(strings.TrimSpace(*configPath))
	if config == "" {
		config = c.defaultConfigPath()
	}
	if !*noInstall {
		if err := c.writeAppConfig(config, binaryPath); err != nil {
			return err
		}
	}
	target, err := c.resolveShellTarget(*session, sessionExplicit)
	if err != nil {
		return err
	}
	command := "tmux"
	runArgs := []string{"-L", socketName, "-f", config, "attach-session", "-t", "=" + target.SessionName}
	if target.CWD != "" {
		runArgs = append(runArgs, "-c", target.CWD)
	}
	var prepareErr error
	if target.ProjectDefault {
		if c.projectSession == nil {
			prepareErr = errors.New("canonical Project startup is not configured")
		} else {
			prepareErr = c.projectSession(context.Background(), socketName, target)
		}
	} else {
		prepareErr = c.prepareControlSession(context.Background(), socketName, config, target)
	}
	if prepareErr != nil {
		// Fail-open is allowed only for a session proven already present. If the
		// target is absent or its observation failed, foreground attachment must
		// not turn a refused plan into an unplanned create.
		exists, observeErr := c.appSessionExists(context.Background(), socketName, config, target.SessionName)
		if observeErr != nil || !exists {
			return fmt.Errorf("prepare declared session %q before attach: %w", target.SessionName, prepareErr)
		}
		_, _ = fmt.Fprint(stderr, controlSessionWarning(target.SessionName, prepareErr))
	}
	if c.shouldStartNativeKeyBroker() {
		if err := c.start(context.Background(), binaryPath, "internal", "key-broker", "--socket", socketName); err != nil {
			_, _ = fmt.Fprintf(stderr, "warning: start native macOS keybindings: %v\n", err)
		}
	}
	return c.executeShellSession(context.Background(), socketName, target.SessionName, command, runArgs...)
}

func (c *shellCommand) materializeProjectDefaultSession(ctx context.Context, socketName string, target shellTarget) error {
	store := newResourceStore()
	if c.homeDir == nil {
		return errors.New("shell home directory resolver is not configured")
	}
	home, err := c.homeDir()
	if err != nil {
		return err
	}
	namer := coresessions.NewNamer(home)
	project, _, err := registerProjectRoot(ctx, store, c.defaultShell(), namer.SessionName, target.CWD)
	if err != nil {
		return err
	}
	if namer.SessionName(project.Spec.Root) != target.SessionName {
		return errors.New("canonical Project startup resolved a different session identity")
	}
	exact, err := tmuxSocketNameTarget(socketName)
	if err != nil {
		return err
	}
	return materializeProjectSessionCanonical(ctx, store, c.tmuxRunner, runtimeMutationRoute{
		target: exact, socketName: socketName,
	}, c.diagnostics, target.SessionName, target.CWD, project)
}

func (c *shellCommand) executeShellSession(ctx context.Context, socketName, sessionName, command string, args ...string) error {
	_ = socketName
	_ = sessionName
	if c.diagnostics != nil {
		// Both ControlSession and Project targets are materialized through their
		// canonical typed plan before this point. The foreground leg is the
		// non-creating attach-session command, so disappearance between preflight
		// and exec fails closed instead of silently provisioning an unmanaged
		// session.
		c.diagnostics.Mark(diagnostics.OperationSessionAttach)
	}
	return c.run(ctx, command, args...)
}

// prepareControlSession writes the control marker and Home's Window/Pane
// identity mirror before the client attaches.
//
// Two things make the timing work, and both are the reason this is a preflight
// rather than something layered onto the attach itself:
//
//   - The preflight provisions the session detached, so the brand-new-Home case
//     has a session to write options onto at all, and it moves no client. It is
//     idempotent: an already-live Home is probed and left exactly as it was
//     found, which is what makes the already-live backfill a re-entry with no
//     restart and no delete. The attach that follows is explicitly non-creating,
//     so a post-preflight disappearance is reported instead of recreating the
//     session outside the plan.
//   - The pass runs only for the app-session target. `resolveShellTarget` sets
//     ProjectDefault when the session it resolved is a Project's session, and a
//     session whose ownership goes to a Project must never carry the control
//     role: it is a Project's runtime projection, and marking it would give one
//     tmux session two mutually exclusive attributions.
func (c *shellCommand) prepareControlSession(ctx context.Context, socketName, configPath string, target shellTarget) (retErr error) {
	if target.ProjectDefault || c.controlSession == nil {
		return nil
	}
	pass := c.controlSession(c.tmuxRunner, c.defaultShell())
	if pass == nil {
		return nil
	}
	receipt, err := c.provisionAppSession(ctx, socketName, configPath, target)
	if err != nil {
		return err
	}
	if receipt.created {
		defer func() {
			if retErr != nil {
				if rollbackErr := c.rollbackControlBootstrap(ctx, socketName, receipt); rollbackErr != nil {
					retErr = errors.Join(retErr, fmt.Errorf("ControlSession bootstrap owned rollback incomplete: %w", rollbackErr))
				}
			}
		}()
	}
	before, err := c.observeControlBootstrapAtRoute(ctx, receipt.route, target.SessionName)
	if err != nil {
		return err
	}
	action := newRuntimeMutation(1, mutationConvergeControlIdentity, runtimeMutationTarget{
		Socket: "-L=" + socketName, PhysicalSocket: printableRuntimeMutationSocket(receipt.route.expectedSocketPath),
		RouteAuthority: receipt.route.authority.printable(),
		Kind:           "control-session", ID: before[0], Parent: before[2] + "/" + before[4],
	})
	bindRuntimeMutationGuard(&action, "exact bootstrap containment="+strings.Join(before, "/"))
	var result controlSessionConvergence
	err = executeRuntimeMutationPlan(ctx, []runtimeMutationStep{{
		Action: action,
		TargetRouteGuard: func(ctx context.Context) error {
			return guardPrintedRuntimeMutationRoute(ctx, c.tmuxRunner, receipt.route, action)
		},
		Reobserve: func(ctx context.Context) (bool, error) {
			if err := guardPrintedRuntimeMutationRoute(ctx, c.tmuxRunner, receipt.route, action); err != nil {
				return false, err
			}
			current, err := c.observeControlBootstrapAtRoute(ctx, receipt.route, target.SessionName)
			if err != nil {
				return false, err
			}
			return current[1] == resourcegraph.ControlSessionRole && current[3] != "" && current[5] != "", nil
		},
		Guard: func(ctx context.Context) error {
			if err := guardPrintedRuntimeMutationRoute(ctx, c.tmuxRunner, receipt.route, action); err != nil {
				return err
			}
			current, err := c.observeControlBootstrapAtRoute(ctx, receipt.route, target.SessionName)
			if err != nil || !slices.Equal(current, before) {
				return errors.New("ControlSession bootstrap containment drifted before identity convergence")
			}
			return nil
		},
		Apply: func(ctx context.Context) error {
			var err error
			result, err = pass.converge(ctx, socketName, target.SessionName)
			return err
		},
	}})
	if err != nil {
		return err
	}
	if result.skipped != "" {
		return fmt.Errorf("declarative control target refused: %s", result.skipped)
	}
	after, err := c.observeControlBootstrapAtRoute(ctx, receipt.route, target.SessionName)
	if err != nil || strings.TrimSpace(result.controlUID) == "" || after[1] != resourcegraph.ControlSessionRole ||
		strings.TrimSpace(after[3]) == "" || strings.TrimSpace(after[5]) == "" {
		return errors.New("ControlSession identity convergence did not yield exact root UID and Window/Pane mirrors")
	}
	if receipt.created {
		if err := c.clearControlBootstrapLease(ctx, socketName, receipt); err != nil {
			return err
		}
	}
	return nil
}

func (c *shellCommand) observeControlBootstrapAtRoute(ctx context.Context, route runtimeMutationRoute, sessionName string) ([]string, error) {
	out, err := c.controlBootstrapRunner(route).Run(ctx, "tmux", "display-message", "-p", "-t", sessionName, "-F",
		tmuxRowFormat("#{session_id}", "#{"+tmuxopts.SessionRole+"}", "#{window_id}", "#{"+tmuxopts.WindowUID+"}", "#{pane_id}", "#{"+tmuxopts.PaneUID+"}"))
	if err != nil {
		return nil, err
	}
	rows := splitTmuxRows(string(out), 6)
	if len(rows) != 1 || exactTmuxHandle(rows[0][0], "$") == "" || exactTmuxHandle(rows[0][2], "@") == "" || exactTmuxHandle(rows[0][4], "%") == "" {
		return nil, errors.New("ControlSession bootstrap containment observation is not exact")
	}
	return rows[0], nil
}

func (c *shellCommand) controlBootstrapRunner(route runtimeMutationRoute) tmuxCommandRunner {
	target := route.target
	if route.expectedSocketPath != "" {
		target = explicitTmuxTarget{flag: "-S", value: filepath.Clean(route.expectedSocketPath)}
	}
	return explicitTmuxRunner{runner: c.tmuxRunner, target: target}
}

// provisionAppSession creates the app session detached, or does nothing when it
// already exists.
//
// Its typed plan is deliberately an observed-absence guard followed by an exact
// detached `new-session`, and not an attach-or-create command. Measured on tmux:
// with `-A` and an existing session, `new-session` becomes an attach and `-d`
// does not suppress it -- outside a terminal it fails with "open terminal
// failed: not a terminal", and inside one it would seize the client here instead
// of at the attach below. Either way the marker would never be written, which is
// precisely the already-live backfill this preflight exists for.
//
// The `-f <config>` is carried on the creating call so a server this preflight
// starts is started with the generated app config -- the same config the
// foreground attach names. The two must agree about which server they mean and
// how it was configured, or the `@projmux_app` marker the converger checks would
// be missing on the server the attach joins.
//
// A session that appears between the probe and the create is reobserved through
// the operation lease; a same-name object without that ownership proof is drift,
// not a successful bootstrap.
type controlBootstrapReceipt struct {
	created                     bool
	sessionID, windowID, paneID string
	operationMarker             string
	route                       runtimeMutationRoute
}

func (c *shellCommand) provisionAppSession(ctx context.Context, socketName, configPath string, target shellTarget) (controlBootstrapReceipt, error) {
	if c.tmuxRunner == nil {
		return controlBootstrapReceipt{}, errors.New("shell tmux runner is not configured")
	}
	route, err := c.resolveControlBootstrapRoute(ctx, socketName)
	if err != nil {
		return controlBootstrapReceipt{}, err
	}
	exists, err := c.appSessionExists(ctx, socketName, configPath, target.SessionName)
	if err != nil {
		return controlBootstrapReceipt{}, err
	}
	if exists {
		return controlBootstrapReceipt{route: route}, nil
	}
	operationID, err := newCreateOperationID()
	if err != nil {
		return controlBootstrapReceipt{}, err
	}
	operationMarker := newCreateOperationMarker(operationID)
	resultFormat := tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{"+tmuxopts.AppGlobal+"}")
	args := []string{"-L", socketName, "-f", configPath, "-d", "-P", "-F", resultFormat, "-s", target.SessionName}
	if target.CWD != "" {
		args = append(args, "-c", target.CWD)
	}
	args = append(args, "-e", createOperationEnvironment+"="+operationMarker)
	action := newRuntimeMutation(1, mutationBootstrapControlSession, runtimeMutationTarget{
		Socket:         "-L=" + socketName,
		PhysicalSocket: printableRuntimeMutationSocket(route.expectedSocketPath),
		RouteAuthority: route.authority.printable(),
		Kind:           "control-session-declaration",
		ID:             "declaration:-L=" + socketName + "/session=" + target.SessionName,
	})
	bindRuntimeMutationGuard(&action, "logical socket=-L="+socketName+"; ControlSession declaration absent="+target.SessionName)
	action.Operands = slices.Clone(args)
	var createdOutput []byte
	err = executeRuntimeMutationPlan(ctx, []runtimeMutationStep{{
		Action: action,
		TargetRouteGuard: func(ctx context.Context) error {
			if action.Target.PhysicalSocket == runtimeMutationSocketAbsentBeforeCreate {
				if route.expectedSocketPath != "" || route.authority != nil || action.Target.RouteAuthority != "" ||
					action.Target.Socket != route.target.flag+"="+route.target.value {
					return errors.New("ControlSession bootstrap absent authority disagrees with printable declaration")
				}
				return c.guardControlBootstrapAbsentBeforeCreate(ctx, route)
			}
			return guardPrintedRuntimeMutationRoute(ctx, c.tmuxRunner, route, action)
		},
		Reobserve: func(ctx context.Context) (bool, error) {
			exists, err := c.appSessionExists(ctx, socketName, configPath, target.SessionName)
			if err != nil || !exists {
				return false, err
			}
			if err := c.guardControlBootstrapRoute(ctx, &route, false, false); err != nil {
				return false, err
			}
			rows, err := c.observeControlBootstrapByLease(ctx, route, operationMarker)
			return err == nil && len(rows) == 1, err
		},
		Guard: func(ctx context.Context) error {
			if action.Target.PhysicalSocket == runtimeMutationSocketAbsentBeforeCreate {
				if err := c.guardControlBootstrapAbsentBeforeCreate(ctx, route); err != nil {
					return err
				}
			} else if err := c.guardControlBootstrapRoute(ctx, &route, false, true); err != nil {
				return err
			}
			exists, err := c.appSessionExists(ctx, socketName, configPath, target.SessionName)
			if err != nil {
				return err
			}
			if exists {
				return fmt.Errorf("control session %q appeared after planning", target.SessionName)
			}
			return nil
		},
		Apply: func(ctx context.Context) error {
			var err error
			createdOutput, err = runRuntimeMutationCommand(ctx, c.tmuxRunner, action)
			return err
		},
	}})
	if err != nil {
		if tmuxDuplicateSession(err) {
			return controlBootstrapReceipt{}, nil
		}
		return controlBootstrapReceipt{}, c.recoverControlBootstrapCreateError(ctx, socketName, operationMarker, route, err)
	}
	rows := splitTmuxRows(string(createdOutput), 4)
	if len(rows) != 1 || exactTmuxHandle(rows[0][0], "$") == "" || exactTmuxHandle(rows[0][1], "@") == "" ||
		exactTmuxHandle(rows[0][2], "%") == "" {
		rows, err = c.observeControlBootstrapByLease(ctx, route, operationMarker)
		if err != nil {
			return controlBootstrapReceipt{}, errors.New("ControlSession bootstrap did not produce one exact app-owned session/window/pane containment")
		}
	}
	if err := c.bindControlBootstrapRouteAuthority(ctx, &route, rows[0][0], rows[0][1], rows[0][2], operationMarker); err != nil {
		receipt := controlBootstrapReceipt{created: true, sessionID: rows[0][0], windowID: rows[0][1], paneID: rows[0][2], operationMarker: operationMarker, route: route}
		if rollbackErr := c.rollbackControlBootstrap(ctx, socketName, receipt); rollbackErr != nil {
			return receipt, errors.Join(err, fmt.Errorf("ControlSession bootstrap owned rollback incomplete: %w", rollbackErr))
		}
		return receipt, err
	}
	receipt := controlBootstrapReceipt{created: true, sessionID: rows[0][0], windowID: rows[0][1], paneID: rows[0][2], operationMarker: operationMarker, route: route}
	if err := c.guardControlBootstrapRoute(ctx, &route, false, false); err != nil {
		receipt.route = route
		if rollbackErr := c.rollbackControlBootstrap(ctx, socketName, receipt); rollbackErr != nil {
			return receipt, errors.Join(err, fmt.Errorf("ControlSession bootstrap owned rollback incomplete: %w", rollbackErr))
		}
		return receipt, err
	}
	receipt.route = route
	if rows[0][3] != "1" {
		original := errors.New("ControlSession bootstrap created exact containment on a server without app ownership")
		if rollbackErr := c.rollbackControlBootstrap(ctx, socketName, receipt); rollbackErr != nil {
			return receipt, errors.Join(original, fmt.Errorf("ControlSession bootstrap owned rollback incomplete: %w", rollbackErr))
		}
		return receipt, original
	}
	marker := newRuntimeMutation(1, mutationWriteRouteMarker, runtimeMutationTarget{
		Socket: "-S=" + route.expectedSocketPath, PhysicalSocket: printableRuntimeMutationSocket(route.expectedSocketPath),
		RouteAuthority: route.authority.printable(),
		Kind:           "session", ID: rows[0][0], UID: "logical:" + socketName, Parent: rows[0][1] + "/" + rows[0][2],
	})
	bindRuntimeMutationGuard(&marker, "exact app-owned bootstrap="+strings.Join(rows[0], "/"))
	marker.Operands = []string{"-S", route.expectedSocketPath, "-gq", runtimeMutationSocketNameOption, socketName}
	if err := executeRuntimeMutationPlan(ctx, []runtimeMutationStep{{
		Action: marker,
		TargetRouteGuard: func(ctx context.Context) error {
			return guardPrintedRuntimeMutationRouteBeforeMarkerWrite(ctx, c.tmuxRunner, route, marker)
		},
		Reobserve: func(ctx context.Context) (bool, error) {
			if err := c.guardControlBootstrapRoute(ctx, &route, false, false); err != nil {
				return false, err
			}
			out, err := c.tmuxRunner.Run(ctx, "tmux", "-S", marker.Target.PhysicalSocket, "show-options", "-gqv", runtimeMutationSocketNameOption)
			return err == nil && strings.TrimSpace(string(out)) == socketName, err
		},
		Guard: func(ctx context.Context) error {
			if err := c.guardControlBootstrapRoute(ctx, &route, false, false); err != nil {
				return err
			}
			current, err := c.controlBootstrapRunner(route).Run(ctx, "tmux", "display-message", "-p", "-t", rows[0][0],
				"-F", tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{"+tmuxopts.AppGlobal+"}"))
			currentRows := splitTmuxRows(string(current), 4)
			if err != nil || len(currentRows) != 1 || !slices.Equal(currentRows[0], rows[0]) {
				return errors.New("ControlSession bootstrap containment drifted before route marker")
			}
			return nil
		},
		Apply: func(ctx context.Context) error {
			_, err := runRuntimeMutationCommand(ctx, c.tmuxRunner, marker)
			return err
		},
	}}); err != nil {
		if rollbackErr := c.rollbackControlBootstrap(ctx, socketName, receipt); rollbackErr != nil {
			return receipt, errors.Join(err, fmt.Errorf("ControlSession bootstrap owned rollback incomplete: %w", rollbackErr))
		}
		return receipt, err
	}
	if err := guardResolvedRuntimeMutationRoute(ctx, c.tmuxRunner, route); err != nil {
		if rollbackErr := c.rollbackControlBootstrap(ctx, socketName, receipt); rollbackErr != nil {
			return receipt, errors.Join(err, fmt.Errorf("ControlSession bootstrap owned rollback incomplete: %w", rollbackErr))
		}
		return receipt, err
	}
	return receipt, nil
}

func (c *shellCommand) observeControlBootstrapByLease(ctx context.Context, route runtimeMutationRoute, marker string) ([][]string, error) {
	matched, err := c.listControlBootstrapsByLease(ctx, route, marker)
	if err != nil {
		return nil, err
	}
	if len(matched) != 1 {
		return nil, errors.New("ControlSession bootstrap lease does not identify one exact created containment")
	}
	return matched, nil
}

// listControlBootstrapsByLease is deliberately non-destructive. Callers may
// kill only when it returns exactly one complete $/@/% containment; zero means
// the failed create had no observed effect, while more than one is ambiguous
// and must be preserved for explicit recovery.
func (c *shellCommand) listControlBootstrapsByLease(ctx context.Context, route runtimeMutationRoute, marker string) ([][]string, error) {
	format := tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{"+tmuxopts.AppGlobal+"}", "#{"+createOperationEnvironment+"}")
	out, err := c.controlBootstrapRunner(route).Run(ctx, "tmux", "list-sessions", "-F", format)
	if err != nil {
		return nil, err
	}
	rows := splitTmuxRows(string(out), 5)
	var matched [][]string
	for _, row := range rows {
		if row[4] == marker && exactTmuxHandle(row[0], "$") != "" && exactTmuxHandle(row[1], "@") != "" && exactTmuxHandle(row[2], "%") != "" {
			matched = append(matched, row[:4])
		}
	}
	return matched, nil
}

func (c *shellCommand) recoverControlBootstrapCreateError(ctx context.Context, socketName, marker string, route runtimeMutationRoute, createErr error) error {
	if route.target.flag == "" {
		route = runtimeMutationRoute{target: explicitTmuxTarget{flag: "-L", value: socketName}, socketName: socketName}
	}
	if bindErr := c.bindControlBootstrapPhysicalRoute(ctx, &route, false); bindErr != nil {
		if tmuxSessionAbsent(bindErr) || tmuxServerUnreachable(bindErr) {
			return createErr
		}
		return errors.Join(createErr, fmt.Errorf("ControlSession bootstrap error-after-effect physical route is unknown; no rollback attempted: %w", bindErr))
	}
	rows, observeErr := c.listControlBootstrapsByLease(ctx, route, marker)
	if observeErr != nil {
		if tmuxSessionAbsent(observeErr) || tmuxServerUnreachable(observeErr) {
			return createErr
		}
		return errors.Join(createErr, fmt.Errorf("ControlSession bootstrap error-after-effect ownership is unknown; no rollback attempted: %w", observeErr))
	}
	switch len(rows) {
	case 0:
		return createErr
	case 1:
		if bindErr := c.bindControlBootstrapRouteAuthority(ctx, &route, rows[0][0], rows[0][1], rows[0][2], marker); bindErr != nil {
			return errors.Join(createErr, fmt.Errorf("ControlSession bootstrap error-after-effect generation is unknown; no rollback attempted: %w", bindErr))
		}
		receipt := controlBootstrapReceipt{
			created: true, sessionID: rows[0][0], windowID: rows[0][1], paneID: rows[0][2], operationMarker: marker,
			route: route,
		}
		if rollbackErr := c.rollbackControlBootstrap(ctx, socketName, receipt); rollbackErr != nil {
			return errors.Join(createErr, fmt.Errorf("ControlSession bootstrap owned rollback incomplete: %w", rollbackErr))
		}
		return createErr
	default:
		return errors.Join(createErr, fmt.Errorf("ControlSession bootstrap lease matched %d exact containments; no ambiguous rollback attempted", len(rows)))
	}
}

func (c *shellCommand) resolveControlBootstrapRoute(ctx context.Context, socketName string) (runtimeMutationRoute, error) {
	target, err := tmuxSocketNameTarget(socketName)
	if err != nil {
		return runtimeMutationRoute{}, err
	}
	route := runtimeMutationRoute{target: target, socketName: socketName}
	if err := c.guardControlBootstrapRoute(ctx, &route, true, true); err != nil {
		return runtimeMutationRoute{}, err
	}
	if route.expectedSocketPath != "" {
		bound, err := resolveExistingRuntimeMutationRoute(ctx, c.tmuxRunner, target, nil)
		if err != nil {
			return runtimeMutationRoute{}, err
		}
		route = bound
	}
	return route, nil
}

func (c *shellCommand) bindControlBootstrapRouteAuthority(
	ctx context.Context,
	route *runtimeMutationRoute,
	sessionID, windowID, paneID, marker string,
) error {
	if route == nil || !filepath.IsAbs(route.expectedSocketPath) || filepath.Clean(route.expectedSocketPath) != route.expectedSocketPath {
		return errors.New("ControlSession bootstrap route authority has no exact physical socket")
	}
	routed := c.controlBootstrapRunner(*route)
	out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-t", paneID, "-F", tmuxRowFormat(
		"#{socket_path}", "#{pid}", "#{session_id}", "#{window_id}", "#{pane_id}", "#{E:"+createOperationEnvironment+"}"))
	if err != nil {
		return fmt.Errorf("ControlSession bootstrap: observe exact route authority: %w", err)
	}
	rows := splitTmuxRows(string(out), 6)
	if len(rows) != 1 || rows[0][0] != route.expectedSocketPath || rows[0][2] != sessionID || rows[0][3] != windowID ||
		rows[0][4] != paneID || rows[0][5] != marker {
		return errors.New("ControlSession bootstrap route authority socket/$/@/%/lease receipt drifted")
	}
	pid, pidErr := strconv.Atoi(strings.TrimSpace(rows[0][1]))
	if pidErr != nil || pid <= 0 {
		return errors.New("ControlSession bootstrap route authority server pid is invalid")
	}
	route.authority = &runtimeMutationRouteAuthority{
		Class: runtimeMutationRouteApp, ServerPID: rows[0][1], SessionID: sessionID, WindowID: windowID, PaneID: paneID,
	}
	return nil
}

func (c *shellCommand) guardControlBootstrapAbsentBeforeCreate(ctx context.Context, route runtimeMutationRoute) error {
	if route.target.flag != "-L" || route.target.value == "" || route.expectedSocketPath != "" || route.authority != nil {
		return errors.New("ControlSession bootstrap absent authority is not an exact logical declaration")
	}
	probe := explicitTmuxRunner{runner: c.tmuxRunner, target: route.target}
	if _, err := probe.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}"); err != nil {
		if tmuxSessionAbsent(err) || tmuxServerUnreachable(err) {
			return nil
		}
		return fmt.Errorf("ControlSession bootstrap absent authority is unknown: %w", err)
	}
	return errors.New("ControlSession bootstrap absent server appeared after planning")
}

// guardControlBootstrapRoute binds one physical socket_path for the whole
// operation. A no-server create may begin unbound; the first post-create guard
// records the exact path before route-marker, identity, or rollback actions.
func (c *shellCommand) guardControlBootstrapRoute(ctx context.Context, route *runtimeMutationRoute, allowNoServer, requireLogical bool) error {
	if route == nil || route.target.flag != "-L" || route.target.value == "" || route.socketName == "" {
		return errors.New("ControlSession bootstrap route is not exact")
	}
	if err := c.bindControlBootstrapPhysicalRoute(ctx, route, allowNoServer); err != nil {
		return err
	}
	if route.expectedSocketPath == "" {
		return nil
	}
	routed := c.controlBootstrapRunner(*route)
	if route.authority != nil {
		pidOut, err := routed.Run(ctx, "tmux", "display-message", "-p", "-F", "#{pid}")
		if err != nil || strings.TrimSpace(string(pidOut)) != route.authority.ServerPID {
			return errors.New("ControlSession bootstrap server generation drifted")
		}
	}
	owned, err := routed.Run(ctx, "tmux", "show-options", "-gqv", tmuxopts.AppGlobal)
	if err != nil || strings.TrimSpace(string(owned)) != "1" {
		return errors.New("ControlSession bootstrap: exact server is not app-owned")
	}
	if requireLogical {
		if err := guardRuntimeMutationServerOwnership(ctx, routed, route.target); err != nil {
			return fmt.Errorf("ControlSession bootstrap: %w", err)
		}
	}
	return nil
}

func (c *shellCommand) bindControlBootstrapPhysicalRoute(ctx context.Context, route *runtimeMutationRoute, allowNoServer bool) error {
	if route == nil || route.target.flag != "-L" || route.target.value == "" {
		return errors.New("ControlSession bootstrap route is not exact")
	}
	routed := c.controlBootstrapRunner(*route)
	out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		if tmuxSessionAbsent(err) || tmuxServerUnreachable(err) {
			if allowNoServer && route.expectedSocketPath == "" {
				return nil
			}
			return errors.New("ControlSession bootstrap planned socket disappeared")
		}
		return fmt.Errorf("ControlSession bootstrap: observe exact logical route: %w", err)
	}
	observed := filepath.Clean(strings.TrimSpace(string(out)))
	if observed == "." {
		return errors.New("ControlSession bootstrap observed an empty socket path")
	}
	if route.expectedSocketPath == "" {
		route.expectedSocketPath = observed
	} else if observed != filepath.Clean(route.expectedSocketPath) {
		return fmt.Errorf("ControlSession bootstrap socket drifted: observed %q, planned %q", observed, filepath.Clean(route.expectedSocketPath))
	}
	return nil
}

func (c *shellCommand) guardControlBootstrapRollbackRoute(ctx context.Context, receipt controlBootstrapReceipt) error {
	if receipt.route.target.flag == "" {
		return nil
	}
	routed := c.controlBootstrapRunner(receipt.route)
	out, err := routed.Run(ctx, "tmux", "display-message", "-p", "-t", receipt.sessionID, "-F", "#{socket_path}")
	if err != nil {
		return err
	}
	if receipt.route.expectedSocketPath != "" && filepath.Clean(strings.TrimSpace(string(out))) != filepath.Clean(receipt.route.expectedSocketPath) {
		return errors.New("ControlSession bootstrap rollback socket drifted")
	}
	return nil
}

func (c *shellCommand) guardControlBootstrapOwnedLease(ctx context.Context, receipt controlBootstrapReceipt) error {
	if err := c.guardControlBootstrapRecoveryRoute(ctx, receipt); err != nil {
		return err
	}
	if err := c.guardControlBootstrapRollbackRoute(ctx, receipt); err != nil {
		return err
	}
	out, err := c.controlBootstrapRunner(receipt.route).Run(ctx, "tmux", "show-environment", "-t", receipt.sessionID)
	if err != nil || sessionEnvironmentValue(string(out), createOperationEnvironment) != receipt.operationMarker {
		return errors.New("ControlSession bootstrap ownership lease is absent or changed")
	}
	return nil
}

func (c *shellCommand) guardControlBootstrapRecoveryRoute(ctx context.Context, receipt controlBootstrapReceipt) error {
	route := receipt.route
	if !filepath.IsAbs(route.expectedSocketPath) || filepath.Clean(route.expectedSocketPath) != route.expectedSocketPath ||
		route.authority == nil || route.authority.Class != runtimeMutationRouteApp || route.authority.ServerPID == "" {
		return errors.New("ControlSession bootstrap recovery route has no exact physical server generation")
	}
	exact := explicitTmuxRunner{runner: c.tmuxRunner, target: explicitTmuxTarget{flag: "-S", value: route.expectedSocketPath}}
	pathOut, err := exact.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil || strings.TrimSpace(string(pathOut)) != route.expectedSocketPath {
		return errors.New("ControlSession bootstrap rollback socket drifted")
	}
	pidOut, err := exact.Run(ctx, "tmux", "display-message", "-p", "-F", "#{pid}")
	if err != nil || strings.TrimSpace(string(pidOut)) != route.authority.ServerPID {
		return errors.New("ControlSession bootstrap rollback server generation drifted")
	}
	return nil
}

func (c *shellCommand) rollbackControlBootstrap(ctx context.Context, _ string, receipt controlBootstrapReceipt) error {
	if !receipt.created || exactTmuxHandle(receipt.sessionID, "$") == "" {
		return nil
	}
	action := newRuntimeMutation(1, mutationKillOwned, runtimeMutationTarget{
		Socket: "-S=" + receipt.route.expectedSocketPath, PhysicalSocket: printableRuntimeMutationSocket(receipt.route.expectedSocketPath),
		RouteAuthority: receipt.route.authority.printable(),
		Kind:           "session", ID: receipt.sessionID, UID: receipt.operationMarker,
		Parent: receipt.windowID + "/" + receipt.paneID,
	})
	bindRuntimeMutationGuard(&action, "same exact bootstrap lease="+receipt.operationMarker)
	action.Operands = []string{"-t", receipt.sessionID}
	return executeRuntimeMutationPlan(ctx, []runtimeMutationStep{{
		Action: action,
		TargetRouteGuard: func(ctx context.Context) error {
			if action.Target.Socket != "-S="+receipt.route.expectedSocketPath ||
				action.Target.PhysicalSocket != receipt.route.expectedSocketPath || receipt.route.authority == nil ||
				action.Target.RouteAuthority != receipt.route.authority.printable() {
				return errors.New("ControlSession bootstrap rollback printable route disagrees with exact recovery authority")
			}
			return c.guardControlBootstrapRecoveryRoute(ctx, receipt)
		},
		Reobserve: func(ctx context.Context) (bool, error) {
			out, err := c.tmuxRunner.Run(ctx, "tmux", "-S", action.Target.PhysicalSocket, "list-sessions", "-F", "#{session_id}")
			if err != nil {
				if inttmux.IsNoServerFailure(err) {
					return true, nil
				}
				return false, err
			}
			for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
				if strings.TrimSpace(line) == receipt.sessionID {
					return false, nil
				}
			}
			return true, nil
		},
		Guard: func(ctx context.Context) error {
			return c.guardControlBootstrapOwnedLease(ctx, receipt)
		},
		Apply: func(ctx context.Context) error {
			_, err := runRuntimeMutationCommand(ctx, explicitTmuxRunner{runner: c.tmuxRunner, target: explicitTmuxTarget{flag: "-S", value: receipt.route.expectedSocketPath}}, action)
			return err
		},
	}})
}

func (c *shellCommand) clearControlBootstrapLease(ctx context.Context, socketName string, receipt controlBootstrapReceipt) error {
	action := newRuntimeMutation(1, mutationClearLease, runtimeMutationTarget{
		Socket: "-L=" + socketName, PhysicalSocket: printableRuntimeMutationSocket(receipt.route.expectedSocketPath),
		RouteAuthority: receipt.route.authority.printable(),
		Kind:           "session", ID: receipt.sessionID, Parent: receipt.windowID + "/" + receipt.paneID,
	})
	bindRuntimeMutationGuard(&action, "same bootstrap operation lease="+receipt.operationMarker)
	action.Operands = []string{"-u", "-t", receipt.sessionID, createOperationEnvironment}
	return executeRuntimeMutationPlan(ctx, []runtimeMutationStep{{
		Action: action,
		TargetRouteGuard: func(ctx context.Context) error {
			return guardPrintedRuntimeMutationRoute(ctx, c.tmuxRunner, receipt.route, action)
		},
		Reobserve: func(ctx context.Context) (bool, error) {
			if err := c.guardControlBootstrapOwnedLease(ctx, receipt); err != nil {
				out, readErr := c.tmuxRunner.Run(ctx, "tmux", "-S", action.Target.PhysicalSocket, "show-environment", "-t", receipt.sessionID)
				if readErr != nil {
					return false, readErr
				}
				return sessionEnvironmentValue(string(out), createOperationEnvironment) == "", nil
			}
			return false, nil
		},
		Guard: func(ctx context.Context) error {
			if err := guardPrintedRuntimeMutationRoute(ctx, c.tmuxRunner, receipt.route, action); err != nil {
				return err
			}
			return c.guardControlBootstrapOwnedLease(ctx, receipt)
		},
		Apply: func(ctx context.Context) error {
			_, err := runRuntimeMutationCommand(ctx, explicitTmuxRunner{runner: c.tmuxRunner, target: explicitTmuxTarget{flag: "-S", value: receipt.route.expectedSocketPath}}, action)
			return err
		},
	}})
}

// appSessionExists probes one exact socket for the app session.
//
// An absent server is an absent session rather than a failure: `projmux shell`
// starting the server is the ordinary first-terminal case, and reporting it as an
// error would refuse the entry it exists to perform.
func (c *shellCommand) appSessionExists(ctx context.Context, socketName, configPath, sessionName string) (bool, error) {
	_, err := c.tmuxRunner.Run(ctx, "tmux", "-L", socketName, "-f", configPath, "has-session", "-t", sessionName)
	if err == nil {
		return true, nil
	}
	if tmuxSessionAbsent(err) || tmuxServerUnreachable(err) {
		return false, nil
	}
	return false, err
}

// tmuxSessionAbsent recognizes the stderr signatures tmux uses when the session
// or the server it was asked about is not there. It is the classification
// tmuxSessionExists has always applied, factored out so the two callers cannot
// disagree about what "absent" looks like.
func tmuxSessionAbsent(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "can't find session") ||
		strings.Contains(message, "no server running") ||
		strings.Contains(message, "can't find server")
}

// tmuxServerUnreachable recognizes the *socket-level* answer, which is a
// different sentence from the ones above and is the one measured on tmux 3.5a
// when the socket file itself does not exist yet: `error connecting to
// <path> (No such file or directory)`.
//
// It is deliberately a separate predicate rather than another clause inside
// tmuxSessionAbsent. Only the app-session preflight may read this as "absent",
// because starting the server is exactly what it is about to do; a caller asking
// whether a session exists on a server it does not own must keep reporting the
// unreachable socket as a failure.
func tmuxServerUnreachable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "failed to connect to server") ||
		(strings.Contains(message, "error connecting to ") && strings.Contains(message, "(no such file or directory)"))
}

// tmuxDuplicateSession recognizes the answer tmux gives when the session this
// preflight was about to create already exists.
func tmuxDuplicateSession(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "duplicate session")
}

type shellTarget struct {
	SessionName    string
	CWD            string
	ProjectDefault bool
}

func (c *shellCommand) resolveShellTarget(rawSession string, sessionExplicit bool) (shellTarget, error) {
	home, err := c.home()
	if err != nil {
		return shellTarget{}, fmt.Errorf("resolve shell home directory: %w", err)
	}
	home = filepath.Clean(home)

	sessionName := nonEmpty(strings.TrimSpace(rawSession), defaultAppSession)
	if sessionExplicit {
		return shellTarget{SessionName: sessionName, CWD: home}, nil
	}

	projectRoot, err := c.resolveShellProjectContext()
	if err != nil {
		return shellTarget{}, fmt.Errorf("resolve shell project context: %w", err)
	}
	if projectRoot == "" {
		return shellTarget{SessionName: sessionName, CWD: home}, nil
	}
	projectRoot = filepath.Clean(projectRoot)
	return shellTarget{
		SessionName:    coresessions.NewNamer(home).SessionName(projectRoot),
		CWD:            projectRoot,
		ProjectDefault: true,
	}, nil
}

func flagSetExplicitly(fs *flag.FlagSet, name string) bool {
	explicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			explicit = true
		}
	})
	return explicit
}

func (c *shellCommand) resolveShellProjectContext() (string, error) {
	if c.lookupEnv != nil {
		if raw := strings.TrimSpace(c.lookupEnv("PROJMUX_CWD")); raw != "" {
			return filepath.Clean(raw), nil
		}
	}
	if c.getwd == nil {
		return "", nil
	}
	wd, err := c.getwd()
	if err != nil {
		return "", err
	}
	wd = filepath.Clean(wd)
	if root := nearestProjectMarker(wd, os.TempDir()); root != "" {
		return root, nil
	}
	return "", nil
}

func tmuxSessionExists(ctx context.Context, runner tmuxRunner, sessionName string) (bool, error) {
	if runner == nil {
		return false, errors.New("tmux runner is not configured")
	}
	_, err := runner.Run(ctx, "tmux", "has-session", "-t", sessionName)
	if err == nil {
		return true, nil
	}
	if tmuxSessionAbsent(err) {
		return false, nil
	}
	return false, err
}

type shellTmuxExecRunner struct {
	env func() []string
}

func (r shellTmuxExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = withoutEnv(r.environ(), "TMUX")
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed != "" {
			return nil, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, trimmed)
		}
		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return output, nil
}

func (r shellTmuxExecRunner) environ() []string {
	if r.env != nil {
		return r.env()
	}
	return os.Environ()
}

func shouldPromptShellUpdate(status updateStatus) bool {
	if status.UpdateState != "update_available" {
		return false
	}
	if status.CacheState != "fresh" {
		return false
	}
	return strings.TrimSpace(status.LatestVersion) != ""
}

func shellUpdateCanUpgrade(status updateStatus) bool {
	switch status.Installer.Source {
	case "npm", "go", "github-release":
		return true
	default:
		return false
	}
}

func (c *shellCommand) updatePromptSkipped(status updateStatus) bool {
	path, err := c.updateSkipPath()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var skip shellUpdateSkipState
	if err := json.Unmarshal(data, &skip); err != nil {
		return false
	}
	return strings.TrimSpace(skip.TagName) == strings.TrimSpace(status.LatestVersion)
}

func (c *shellCommand) writeUpdateSkip(status updateStatus) error {
	path, err := c.updateSkipPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create update skip dir: %w", err)
	}
	skip := shellUpdateSkipState{
		Version:   1,
		TagName:   strings.TrimSpace(status.LatestVersion),
		SkippedAt: c.update.clock().UTC(),
	}
	data, err := json.MarshalIndent(skip, "", "  ")
	if err != nil {
		return fmt.Errorf("encode update skip state: %w", err)
	}
	if err := c.writeFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write update skip state: %w", err)
	}
	return nil
}

func (c *shellCommand) updateSkipPath() (string, error) {
	if c.update == nil {
		return "", errors.New("shell update prompt is not configured")
	}
	cachePath, err := c.update.cachePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(cachePath), "update-skip.json"), nil
}

func (c *shellCommand) insideAppSocket(socketName string) bool {
	tmuxEnv := strings.TrimSpace(c.env("TMUX"))
	if tmuxEnv == "" {
		return false
	}
	socketPath := strings.SplitN(tmuxEnv, ",", 2)[0]
	return filepath.Base(socketPath) == socketName
}

func (c *shellCommand) resolveBinary(override string) (string, error) {
	if binaryPath := strings.TrimSpace(override); binaryPath != "" {
		return binaryPath, nil
	}
	if c.executable == nil {
		return "", errors.New("configure shell executable: executable resolver is not configured")
	}
	binaryPath, err := c.executable()
	if err != nil {
		return "", fmt.Errorf("resolve shell executable: %w", err)
	}
	// Canonicalize here because this path outlives the process: it is written
	// into a tmux config file and live hooks that keep running long after an
	// npm update has deleted the retired staging directory a resolved path
	// may point into.
	return canonicalNpmBinaryPath(binaryPath), nil
}

func (c *shellCommand) writeAppConfig(path, binaryPath string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("shell app config path is required")
	}
	if c.writeFile == nil {
		return errors.New("configure shell app config writer: file writer is not configured")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create shell app config directory: %w", err)
	}
	keyBindings, keymapPresent, err := loadMergedKeyBindingCatalog(keymapLoader{
		homeDir:   c.homeDir,
		lookupEnv: c.lookupEnv,
		readFile:  c.readFile,
	})
	if err != nil {
		return err
	}
	if err := c.writeFile(path, []byte(c.appConfigThemeSource().tmuxAppConfigWithAIBadgeStyleDesktopNotifyModeLiveResourcesAndVisibility(binaryPath, c.defaultShell(), loadStatusbarDecorationSet(c.homeDir, c.lookupEnv), loadAIBadgeStyle(c.homeDir, c.lookupEnv), loadDesktopNotifyModeForTmuxConfig(c.homeDir, c.lookupEnv), loadLiveResourcesMode(c.homeDir, c.lookupEnv), loadStatusbarHUDVisibilitySet(c.homeDir, c.lookupEnv), loadStatusbarRowOneVisibilitySet(c.homeDir, c.lookupEnv), keyBindings, keymapPresent)), 0o644); err != nil {
		return fmt.Errorf("write shell app config: %w", err)
	}
	return nil
}

// appConfigThemeSource resolves the global user theme for the shell-start writer,
// mirroring tmuxCommand.appConfigThemeSource: an explicit global `[theme]`
// repaints the generated tmux chrome on `projmux shell` start instead of
// clobbering a themed config with the built-in fallback. It degrades to the
// fallback when the global config cannot be read so shell start never fails on a
// missing or malformed user config. Theme is global-only, so no project path
// participates.
func (c *shellCommand) appConfigThemeSource() renderThemeSource {
	source, err := configRenderThemeSource(c.homeDir, c.lookupEnv, "")
	if err != nil {
		return fallbackRenderThemeSource()
	}
	return source
}

func (c *shellCommand) defaultShell() string {
	return defaultInteractiveShell(c.lookupEnv)
}

func (c *shellCommand) defaultConfigPath() string {
	configHome := strings.TrimRight(c.env("XDG_CONFIG_HOME"), string(os.PathSeparator))
	if configHome == "" {
		homeDir, err := c.home()
		if err != nil || strings.TrimSpace(homeDir) == "" {
			configHome = ".config"
		} else {
			configHome = filepath.Join(homeDir, ".config")
		}
	}
	return filepath.Join(configHome, "projmux", "tmux.conf")
}

func (c *shellCommand) expandHome(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		homeDir, err := c.home()
		if err != nil || strings.TrimSpace(homeDir) == "" {
			return path
		}
		if path == "~" {
			return homeDir
		}
		return filepath.Join(homeDir, strings.TrimPrefix(path, "~/"))
	}
	return path
}

func (c *shellCommand) home() (string, error) {
	if c.homeDir == nil {
		return "", errors.New("shell home directory resolver is not configured")
	}
	return c.homeDir()
}

func (c *shellCommand) env(name string) string {
	if c.lookupEnv == nil {
		return ""
	}
	return c.lookupEnv(name)
}

func (c *shellCommand) run(ctx context.Context, name string, args ...string) error {
	if c.runCommand == nil {
		return errors.New("shell command runner is not configured")
	}
	return c.runCommand(ctx, withoutEnv(os.Environ(), "TMUX"), name, args...)
}

func (c *shellCommand) start(ctx context.Context, name string, args ...string) error {
	if c.startCommand == nil {
		return errors.New("shell background command runner is not configured")
	}
	return c.startCommand(ctx, withoutEnv(os.Environ(), "TMUX"), name, args...)
}

func (c *shellCommand) shouldStartNativeKeyBroker() bool {
	return c.goos != nil &&
		c.goos() == "darwin" &&
		c.nativeKeys != nil &&
		c.nativeKeys() &&
		nativeKeysEnabled(c.lookupEnv, c.homeDir)
}

func runForegroundCommand(ctx context.Context, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func startBackgroundCommand(ctx context.Context, env []string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		_ = cmd.Wait()
	}()
	return nil
}

func withoutEnv(env []string, name string) []string {
	prefix := name + "="
	filtered := make([]string, 0, len(env))
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

const fallbackInteractiveShell = "/bin/sh"

func defaultInteractiveShell(lookupEnv func(string) string) string {
	if lookupEnv == nil {
		return fallbackInteractiveShell
	}
	shell := strings.TrimSpace(lookupEnv("SHELL"))
	if shell == "" || !filepath.IsAbs(shell) || strings.ContainsAny(shell, "\x00\r\n") {
		return fallbackInteractiveShell
	}
	return shell
}

func posixCommandShell(lookupEnv func(string) string) string {
	shell := defaultInteractiveShell(lookupEnv)
	switch filepath.Base(shell) {
	case "bash", "dash", "ksh", "mksh", "sh", "zsh":
		return shell
	default:
		return fallbackInteractiveShell
	}
}

func loginShellCommand(shell string) []string {
	switch filepath.Base(shell) {
	case "bash", "ksh", "mksh", "zsh":
		return []string{shell, "-l"}
	default:
		return []string{shell}
	}
}

func printShellUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  projmux shell [--socket <name>] [--session <name>] [--config <path>] [--bin <path>] [--no-install]")
}
