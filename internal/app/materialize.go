package app

import mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"

import (
	"context"
	"errors"
	"fmt"

	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/candidates"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type persistentSessionLifecycle interface {
	PreparePersistentSessionCreate(context.Context, string, string, string, map[string]string) (inttmux.PersistentSessionCreateRequest, bool, error)
	CompletePersistentSessionCreate(context.Context, inttmux.PersistentSessionCreateRequest, intmux.NewSessionResult) error
	AbortPersistentSessionCreate()
}

type sessionStartupFinalizer interface {
	FinalizeSessionStartup(ctx context.Context, result intmux.NewSessionResult, sessionName, cwd, operationMarker string) error
}

func (m *materializer) finalizeSessionStartup(ctx context.Context, result intmux.NewSessionResult, sessionName, cwd string, ledger *RuntimeLedger) error {
	if !result.Created {
		return nil
	}
	finalizer, ok := m.Sessions.(sessionStartupFinalizer)
	if !ok {
		return errors.New("materialize tmux session: startup finalization is unavailable")
	}
	action := MaterializeMutationAction(mutationFinalizeSession,
		m.BoundMutationTarget("session", result.SessionID, "session:"+sessionName),
		"same create-operation lease="+ledger.OperationMarker,
		"startup hook contract is finalized once",
		"-t", result.SessionID, finalizeOperationEnvironment, ledger.OperationMarker)
	if err := m.RunMaterializeMutation(ctx, action, func() error {
		out, err := m.ReadTmux(ctx, "show-environment", "-t", result.SessionID)
		if err != nil {
			return err
		}
		if sessionEnvironmentValue(out, createOperationEnvironment) != ledger.OperationMarker {
			return errors.New("create-operation lease changed")
		}
		return nil
	}, func() error {
		if err := finalizer.FinalizeSessionStartup(ctx, result, sessionName, cwd, ledger.OperationMarker); err != nil {
			return err
		}
		_, err := runRuntimeMutationCommand(ctx, m.MutationRunner(action), action)
		return err
	}, func(ctx context.Context) (bool, error) {
		out, err := m.ReadTmux(ctx, "list-panes", "-s", "-t", result.SessionID, "-F",
			tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}",
				"#{E:"+createOperationEnvironment+"}", "#{E:"+finalizeOperationEnvironment+"}"))
		if err != nil {
			return false, err
		}
		rows := splitTmuxRows(out, 5)
		matched := 0
		for _, row := range rows {
			if row[0] == result.SessionID && row[1] == result.WindowID && row[2] == result.PaneID &&
				row[3] == ledger.OperationMarker && row[4] == ledger.OperationMarker {
				matched++
			}
		}
		return matched == 1, nil
	}); err != nil {
		return fmt.Errorf("finalize tmux session %q startup: %w", sessionName, err)
	}
	return nil
}

type liveSessionIdentity struct {
	ID   string
	Name string
	UID  string
	Root string
}

// runtimeLedger records the tmux objects one operation created so a later
// failure can undo exactly them.
//
// Rollback is ownership checked, not best effort: an entry is only removed when
// the tmux object still carries the same Projmux uid this operation mirrored
// onto it. A pre-existing session, a window another operation created, and an
// id that tmux has since recycled are all left alone.
type RuntimeLedger = mutationruntime.RuntimeLedger

// materializer turns offline Project/Window/Pane metadata into detached tmux
// objects.
//
// Every command it issues is detached. It never calls switch-client,
// attach-session, select-window, or select-pane, and it never reads which
// client or pane is focused, so the operator's view is byte-identical before
// and after a create.
type materializer struct {
	mutationruntime.MaterializeRuntime

	mirror intmetadata.Mirror

	// executable resolves this build's own binary for the managed process
	// supervisor a launched pane execs. Nil means os.Executable.
	executable func() (string, error)
	// lookupEnv is the environment probe used only as the last fallback when
	// the exact server reports no default-shell. Nil means os.Getenv.
	lookupEnv func(string) string
	// configPath optionally pins the generated app tmux config in tests and
	// embedded callers. Production resolves the same internal XDG path lazily.
	configPath string

	// routeIdentityDisabled is a test seam that keeps openRouteIdentityCache
	// from opening a cache, so every guard reads tmux as it did before reuse
	// existed. Production leaves it false.
	routeIdentityDisabled bool

	// paneDefaults is the default-shell/default-command pair a create read
	// before taking the Registry lock (prefetchPaneDefaults). It is nil outside
	// that create's transaction.
	paneDefaults *paneDefaultsMemo
}

// openRouteIdentityCache begins one transaction's identity-reuse scope. The
// first guard still proves the identity against tmux; only an exactly equal
// tuple may reuse that proof, and only until closeRouteIdentityCache.
func (m *materializer) openRouteIdentityCache(operationID string) {
	if m == nil {
		return
	}
	if m.routeIdentityDisabled {
		m.RouteIdentity = nil
		return
	}
	m.RouteIdentity = newRuntimeRouteIdentityCache(operationID)
}

// closeRouteIdentityCache ends the scope. It is idempotent, and everything
// after it -- runtime ledger rollback included -- re-proves identity.
func (m *materializer) closeRouteIdentityCache() {
	if m == nil {
		return
	}
	m.RouteIdentity.Close()
	m.RouteIdentity = nil
}

// reproveReusedRouteIdentity bounds the window a reused proof leaves open. When
// this transaction reused any identity, the exact route is proved against tmux
// once more before the Registry commits. A drifted server refuses the commit,
// and the caller unwinds through the runtime ledger like any other failure. A
// transaction that reused nothing issues no extra tmux call.
func (m *materializer) reproveReusedRouteIdentity(ctx context.Context) error {
	if m == nil || m.RouteIdentity.Snapshot().Reuses == 0 {
		return nil
	}
	m.InvalidateRouteIdentity("commit-reproof")
	if err := m.GuardExactRoute(ctx, false, m.ExpectedSocketPath); err != nil {
		return fmt.Errorf("runtime mutation plan: route identity refused commit after reused proofs: %w", err)
	}
	return nil
}

// revalidatedTargetRouteGuard is targetRouteGuard for a guard whose contract is
// to re-prove the physical server generation at that exact point, such as the
// split-layout batch that revalidates immediately before its first resize. It
// drops any identity reused in this transaction first, so the guard always
// reads tmux; a successful proof is recorded again for later guards.
func (m *materializer) revalidatedTargetRouteGuard(action plannedRuntimeMutation) func(context.Context) error {
	guard := m.TargetRouteGuard(action)
	return func(ctx context.Context) error {
		m.InvalidateRouteIdentity("pre-write-revalidation")
		return guard(ctx)
	}
}

func (m *materializer) generatedAppConfigPath() (string, error) {
	path := strings.TrimSpace(m.configPath)
	if path == "" {
		resolved, err := generatedAppConfigDefaultPath(os.UserHomeDir, m.lookupEnv)
		if err != nil {
			return "", fmt.Errorf("resolve generated app tmux config: %w", err)
		}
		path = resolved
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("generated app tmux config path is not absolute and clean: %q", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("generated app tmux config %q is unavailable: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("generated app tmux config %q is not a regular file", path)
	}
	return path, nil
}

// sessionFirstPaneActivation issues the activation of the Pane a new session
// brings with it. It runs inside the caller's Registry transaction, and only
// once new-session is certain to run: a session that is already live keeps
// the generation its first Pane already runs under. A zero spec launches the
// Pane unsupervised, as before.
type sessionFirstPaneActivation func() (superviseSpec, error)

// ensureSessionLaunching makes the Project's persistent tmux session live.
//
// A session that already exists is reused untouched, which is what keeps the
// pre-create/post-create hooks on their documented trigger: they fire when a
// session is created, and only then.
//
// firstWindowName is the Registry name of the Window the caller adopts as the
// new session's own first Window. mirrorWindow turns automatic-rename off, so
// that Window has to be born with its Registry name; a blank name keeps tmux's
// default name for a caller with no Window to adopt.
//
// runtimeCWD is the initial shell Pane's cwd. Registry topology
// materialization starts a Project's first stored shell Pane in that Pane's
// own recorded cwd, while the Project identity, the hook contract's
// PROJMUX_CWD, and the session path anchor all stay on the canonical
// spec.root. Passing spec.root here is exactly the ordinary create path.
//
// A new session's first Pane is launched the way every other Pane of the pass
// is: under the supervisor of its own generation, issued by firstPane. The
// activation is returned so the caller records the Pane's %N with
// observeActivationRuntime in the same transaction, after it has claimed the
// Pane. It is zero when the session already existed.
func (m *materializer) ensureSessionLaunching(
	ctx context.Context,
	project coremetadata.Project,
	sessionName, runtimeCWD, firstWindowName string,
	firstPane sessionFirstPaneActivation,
	ledger *RuntimeLedger,
	firstCommand ...[]string,
) (intmux.NewSessionResult, superviseSpec, error) {
	// Bind an existing invocation server to its physical socket before the
	// printable declaration is built. A genuinely absent server remains the
	// one create-session-only absent-before-create case.
	if err := m.GuardExactRoute(ctx, true); err != nil {
		return intmux.NewSessionResult{}, superviseSpec{}, err
	}
	startsFreshServer := m.ExpectedSocketPath == ""
	exists, err := m.Sessions.SessionExists(ctx, sessionName)
	if err != nil {
		return intmux.NewSessionResult{}, superviseSpec{}, fmt.Errorf("check tmux session %q: %w", sessionName, err)
	}
	if exists {
		identity, err := m.requireOwnedSession(ctx, project, sessionName)
		if err != nil {
			return intmux.NewSessionResult{}, superviseSpec{}, err
		}
		if err := m.MarkCreateOperation(ctx, identity.ID, ledger); err != nil {
			return intmux.NewSessionResult{}, superviseSpec{}, err
		}
		return intmux.NewSessionResult{Created: false, SessionID: identity.ID}, superviseSpec{}, nil
	}
	if identity, found, err := m.preflightSessionOwnership(ctx, project, sessionName); err != nil {
		return intmux.NewSessionResult{}, superviseSpec{}, err
	} else if found {
		if err := m.MarkCreateOperation(ctx, identity.ID, ledger); err != nil {
			return intmux.NewSessionResult{}, superviseSpec{}, err
		}
		return intmux.NewSessionResult{Created: false, SessionID: identity.ID}, superviseSpec{}, nil
	}
	lifecycle, ok := m.Sessions.(persistentSessionLifecycle)
	if !ok {
		return intmux.NewSessionResult{}, superviseSpec{}, errors.New("materialize tmux session: typed persistent lifecycle seam is unavailable")
	}
	request, appeared, err := lifecycle.PreparePersistentSessionCreate(ctx, sessionName, runtimeCWD, project.Spec.Root,
		map[string]string{createOperationEnvironment: ledger.OperationMarker})
	if err != nil {
		return intmux.NewSessionResult{}, superviseSpec{}, err
	}
	if appeared {
		identity, err := m.requireOwnedSession(ctx, project, sessionName)
		if err != nil {
			return intmux.NewSessionResult{}, superviseSpec{}, err
		}
		if err := m.MarkCreateOperation(ctx, identity.ID, ledger); err != nil {
			return intmux.NewSessionResult{}, superviseSpec{}, err
		}
		return intmux.NewSessionResult{Created: false, SessionID: identity.ID}, superviseSpec{}, nil
	}
	var (
		activation superviseSpec
		launch     []string
	)
	if firstPane != nil {
		issued, err := firstPane()
		if err != nil {
			lifecycle.AbortPersistentSessionCreate()
			return intmux.NewSessionResult{}, superviseSpec{}, err
		}
		if issued.valid() {
			activation = issued
			var command []string
			if len(firstCommand) > 0 {
				command = firstCommand[0]
			}
			if startsFreshServer && len(command) == 0 {
				launch = m.supervisedLaunchOnFreshServer(activation)
			} else {
				launch = m.supervisedLaunch(ctx, activation, command)
			}
		}
	}
	args := []string{}
	if startsFreshServer {
		configPath, err := m.generatedAppConfigPath()
		if err != nil {
			lifecycle.AbortPersistentSessionCreate()
			return intmux.NewSessionResult{}, superviseSpec{}, err
		}
		args = append(args, "-f", configPath)
	}
	args = append(args, "-d", "-P", "-F", tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}"), "-s", request.SessionName)
	if strings.TrimSpace(firstWindowName) != "" {
		args = append(args, "-n", firstWindowName)
	}
	args = append(args, "-c", request.RuntimeCWD)
	keys := make([]string, 0, len(request.Environment))
	for key := range request.Environment {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		args = append(args, "-e", key+"="+request.Environment[key])
	}
	action := MaterializeMutationAction(mutationCreateSession,
		m.BoundMutationTarget("project-declaration", sessionName, project.Metadata.UID),
		"unique Project uid/root or absent session name",
		"one detached owned session exists", args...)
	action.Command = launch
	var rawOutput []byte
	ensureErr := m.RunMaterializeMutation(ctx, action, func() error {
		_, found, guardErr := m.preflightSessionOwnership(ctx, project, sessionName)
		if guardErr != nil {
			return guardErr
		}
		if found {
			return fmt.Errorf("session %q appeared after the create plan was built", sessionName)
		}
		return nil
	}, func() error {
		var runErr error
		rawOutput, runErr = runRuntimeMutationCommand(ctx, m.Runner, action)
		return runErr
	}, func(ctx context.Context) (bool, error) {
		resultRows := splitTmuxRows(string(rawOutput), 3)
		if len(resultRows) != 1 || exactTmuxHandle(resultRows[0][0], "$") == "" ||
			exactTmuxHandle(resultRows[0][1], "@") == "" || exactTmuxHandle(resultRows[0][2], "%") == "" {
			return false, nil
		}
		out, err := m.ReadTmux(ctx, "list-panes", "-s", "-t", resultRows[0][0], "-F",
			tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{session_name}",
				"#{E:"+createOperationEnvironment+"}"))
		if err != nil {
			return false, err
		}
		rows := splitTmuxRows(out, 5)
		return len(rows) == 1 && rows[0][0] == resultRows[0][0] && rows[0][1] == resultRows[0][1] &&
			rows[0][2] == resultRows[0][2] && rows[0][3] == sessionName && rows[0][4] == ledger.OperationMarker, nil
	})
	rows := splitTmuxRows(string(rawOutput), 3)
	result := intmux.NewSessionResult{Created: true}
	if len(rows) == 1 {
		result.SessionID, result.WindowID, result.PaneID = rows[0][0], rows[0][1], rows[0][2]
	}
	if ensureErr != nil {
		// A pre-create hook refusal lands here with nothing created. A later
		// synchronous tmux hook can instead fail after new-session created the
		// session. The -e lease is then exact ownership evidence, so establish
		// the Project uid and ledger entry before surfacing the original error.
		if startsFreshServer {
			if rollbackErr := m.recoverCreatedProjectByLease(ctx, result, ledger.OperationMarker); rollbackErr != nil {
				ensureErr = errors.Join(ensureErr, fmt.Errorf("created Project owned rollback incomplete: %w", rollbackErr))
			}
		} else {
			m.recordErrorCreatedSession(ctx, project, sessionName, result.SessionID, ledger)
		}
		lifecycle.AbortPersistentSessionCreate()
		return intmux.NewSessionResult{}, superviseSpec{}, fmt.Errorf("materialize tmux session %q: %w", sessionName, ensureErr)
	}
	if exactTmuxHandle(result.SessionID, "$") == "" || exactTmuxHandle(result.WindowID, "@") == "" || exactTmuxHandle(result.PaneID, "%") == "" {
		if startsFreshServer {
			if rollbackErr := m.recoverCreatedProjectByLease(ctx, result, ledger.OperationMarker); rollbackErr != nil {
				return intmux.NewSessionResult{}, superviseSpec{}, errors.Join(errors.New("materialize tmux session: atomic result is incomplete"), rollbackErr)
			}
		} else {
			m.recordErrorCreatedSession(ctx, project, sessionName, result.SessionID, ledger)
		}
		lifecycle.AbortPersistentSessionCreate()
		return intmux.NewSessionResult{}, superviseSpec{}, fmt.Errorf("materialize tmux session %q: atomic result is incomplete", sessionName)
	}
	if startsFreshServer {
		if err := m.bindCreatedProjectRouteAuthority(ctx, result, ledger.OperationMarker); err != nil {
			if rollbackErr := m.recoverCreatedProjectByLease(ctx, result, ledger.OperationMarker); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("created Project owned rollback incomplete: %w", rollbackErr))
			}
			lifecycle.AbortPersistentSessionCreate()
			return intmux.NewSessionResult{}, superviseSpec{}, err
		}
		if err := m.writeCreatedProjectRouteMarker(ctx, result, ledger.OperationMarker); err != nil {
			if rollbackErr := m.recoverCreatedProjectByLease(ctx, result, ledger.OperationMarker); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("created Project owned rollback incomplete: %w", rollbackErr))
			}
			lifecycle.AbortPersistentSessionCreate()
			return intmux.NewSessionResult{}, superviseSpec{}, err
		}
		if err := m.GuardExactRoute(ctx, false, m.ExpectedSocketPath); err != nil {
			if rollbackErr := m.recoverCreatedProjectByLease(ctx, result, ledger.OperationMarker); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("created Project owned rollback incomplete: %w", rollbackErr))
			}
			lifecycle.AbortPersistentSessionCreate()
			return intmux.NewSessionResult{}, superviseSpec{}, err
		}
	}
	if err := m.writeCreatedProjectAnchor(ctx, result, project, ledger.OperationMarker); err != nil {
		m.recordErrorCreatedSession(ctx, project, sessionName, result.SessionID, ledger)
		lifecycle.AbortPersistentSessionCreate()
		return intmux.NewSessionResult{}, superviseSpec{}, err
	}
	if err := lifecycle.CompletePersistentSessionCreate(ctx, request, result); err != nil {
		m.recordErrorCreatedSession(ctx, project, sessionName, result.SessionID, ledger)
		return intmux.NewSessionResult{}, superviseSpec{}, err
	}
	ledger.MarkSession(result.SessionID)
	if claimErr := m.ClaimRuntimeUIDForRollback(ctx, runtimeSession, result.SessionID, project.Metadata.UID, ledger); claimErr != nil {
		return intmux.NewSessionResult{}, superviseSpec{}, claimErr
	}
	if err := m.mirrorProject(ctx, result.SessionID, project); err != nil {
		return intmux.NewSessionResult{}, superviseSpec{}, err
	}
	return result, activation, nil
}

func (m *materializer) bindCreatedProjectRouteAuthority(ctx context.Context, result intmux.NewSessionResult, marker string) error {
	if !filepath.IsAbs(m.ExpectedSocketPath) || filepath.Clean(m.ExpectedSocketPath) != m.ExpectedSocketPath {
		return errors.New("created Project route authority has no exact physical socket")
	}
	out, err := m.RunnerAtPhysicalSocket(m.ExpectedSocketPath).Run(ctx, "tmux", "display-message", "-p", "-t", result.PaneID, "-F",
		tmuxRowFormat("#{socket_path}", "#{pid}", "#{session_id}", "#{window_id}", "#{pane_id}", "#{E:"+createOperationEnvironment+"}"))
	if err != nil {
		return fmt.Errorf("observe created Project route authority: %w", err)
	}
	rows := splitTmuxRows(string(out), 6)
	if len(rows) != 1 || rows[0][0] != m.ExpectedSocketPath ||
		rows[0][2] != result.SessionID || rows[0][3] != result.WindowID || rows[0][4] != result.PaneID || rows[0][5] != marker {
		return errors.New("created Project route authority socket or $/@/% receipt drifted")
	}
	pid, pidErr := strconv.Atoi(strings.TrimSpace(rows[0][1]))
	if pidErr != nil || pid <= 0 {
		return errors.New("created Project route authority server pid is invalid")
	}
	m.RouteAuthority = &runtimeMutationRouteAuthority{
		Class: runtimeMutationRouteApp, ServerPID: rows[0][1],
		SessionID: result.SessionID, WindowID: result.WindowID, PaneID: result.PaneID,
	}
	return nil
}

func (m *materializer) writeCreatedProjectAnchor(ctx context.Context, result intmux.NewSessionResult, project coremetadata.Project, marker string) error {
	action := MaterializeMutationAction(mutationWriteProjectAnchor,
		m.BoundMutationTarget("session", result.SessionID, project.Metadata.UID),
		"exact created tuple and operation lease="+marker,
		"Project path anchor equals Registry root",
		"-t", result.SessionID, "-q", inttmux.ProjectPathSessionOption, project.Spec.Root)
	observeReceipt := func(ctx context.Context) (bool, error) {
		out, err := m.ReadTmux(ctx, "list-panes", "-s", "-t", result.SessionID, "-F",
			tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{E:"+createOperationEnvironment+"}"))
		rows := splitTmuxRows(out, 4)
		if err != nil {
			return false, err
		}
		return len(rows) == 1 && rows[0][0] == result.SessionID && rows[0][1] == result.WindowID &&
			rows[0][2] == result.PaneID && rows[0][3] == marker, nil
	}
	return m.RunMaterializeMutation(ctx, action, func() error {
		observed, err := observeReceipt(ctx)
		if err != nil || !observed {
			return errors.New("created Project tuple or operation lease drifted before path anchor")
		}
		return nil
	}, func() error {
		_, err := runRuntimeMutationCommand(ctx, m.Runner, action)
		return err
	}, observeReceipt)
}

func (m *materializer) observeCreatedProjectReceipt(ctx context.Context, result intmux.NewSessionResult, marker string) (bool, error) {
	out, err := m.ReadTmux(ctx, "list-panes", "-s", "-t", result.SessionID, "-F",
		tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{E:"+createOperationEnvironment+"}"))
	if err != nil {
		return false, err
	}
	rows := splitTmuxRows(out, 4)
	return len(rows) == 1 && rows[0][0] == result.SessionID && rows[0][1] == result.WindowID &&
		rows[0][2] == result.PaneID && rows[0][3] == marker, nil
}

func (m *materializer) writeCreatedProjectRouteMarker(ctx context.Context, result intmux.NewSessionResult, marker string) error {
	target, _, err := m.ExactMutationRoute()
	if err != nil {
		return err
	}
	if target.Flag() != "-L" || strings.TrimSpace(target.Value) == "" || !filepath.IsAbs(m.ExpectedSocketPath) {
		return errors.New("created Project route marker requires an exact logical and physical route")
	}
	printableTarget := m.BoundMutationTarget("session", result.SessionID, "logical:"+target.Value)
	printableTarget.Socket = "-S=" + m.ExpectedSocketPath
	printableTarget.Parent = result.WindowID + "/" + result.PaneID
	action := newRuntimeMutation(1, mutationWriteRouteMarker, printableTarget)
	bindRuntimeMutationGuard(&action, "exact created Project tuple and operation lease="+marker)
	action.Operands = []string{"-S", m.ExpectedSocketPath, "-gq", runtimeMutationSocketNameOption, target.Value}
	observeReceipt := func(ctx context.Context) (bool, error) {
		if err := m.GuardExactAppRoute(ctx, false, action.Target.PhysicalSocket); err != nil {
			return false, err
		}
		return m.observeCreatedProjectReceipt(ctx, result, marker)
	}
	return executeRuntimeMutationPlan(ctx, m.GuardedWriteSteps([]runtimeMutationStep{{
		Action:           action,
		TargetRouteGuard: m.TargetRouteGuard(action),
		Reobserve: func(ctx context.Context) (bool, error) {
			owned, err := observeReceipt(ctx)
			if err != nil || !owned {
				return false, err
			}
			out, err := m.RoutedRunner().Run(ctx, "tmux", "show-options", "-gqv", runtimeMutationSocketNameOption)
			if err != nil {
				return false, err
			}
			current := strings.TrimSpace(string(out))
			if current != "" && current != target.Value {
				return false, fmt.Errorf("created Project logical route marker is %q, want %q", current, target.Value)
			}
			return current == target.Value, nil
		},
		Guard: func(ctx context.Context) error {
			owned, err := observeReceipt(ctx)
			if err != nil || !owned {
				return errors.New("created Project tuple or operation lease drifted before route marker")
			}
			out, err := m.RoutedRunner().Run(ctx, "tmux", "show-options", "-gqv", runtimeMutationSocketNameOption)
			if err != nil {
				return err
			}
			current := strings.TrimSpace(string(out))
			if current != "" && current != target.Value {
				return fmt.Errorf("created Project logical route marker is %q, want blank or exact", current)
			}
			return nil
		},
		Apply: func(ctx context.Context) error {
			_, err := runRuntimeMutationCommand(ctx, m.Runner, action)
			return err
		},
	}}))
}

func (m *materializer) recoverCreatedProjectByLease(ctx context.Context, result intmux.NewSessionResult, marker string) error {
	if strings.TrimSpace(marker) == "" {
		return nil
	}
	bound, err := m.bindCreatedProjectRecoveryRoute(ctx)
	if err != nil || !bound {
		return err
	}
	out, err := m.RoutedRunner().Run(ctx, "tmux", "list-sessions", "-F",
		tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{E:"+createOperationEnvironment+"}"))
	if err != nil {
		if inttmux.IsNoServerFailure(err) {
			return nil
		}
		return fmt.Errorf("observe created Project lease for rollback: %w", err)
	}
	var matched [][]string
	for _, row := range splitTmuxRows(string(out), 4) {
		if row[3] == marker && exactTmuxHandle(row[0], "$") != "" && exactTmuxHandle(row[1], "@") != "" && exactTmuxHandle(row[2], "%") != "" {
			matched = append(matched, row)
		}
	}
	if len(matched) == 0 {
		return nil
	}
	if len(matched) != 1 {
		return fmt.Errorf("created Project lease matched %d exact containments; no ambiguous rollback attempted", len(matched))
	}
	observed := intmux.NewSessionResult{Created: true, SessionID: matched[0][0], WindowID: matched[0][1], PaneID: matched[0][2]}
	if exactTmuxHandle(result.SessionID, "$") != "" && exactTmuxHandle(result.WindowID, "@") != "" && exactTmuxHandle(result.PaneID, "%") != "" {
		if result.SessionID != observed.SessionID || result.WindowID != observed.WindowID || result.PaneID != observed.PaneID {
			return fmt.Errorf("created Project result tuple %s/%s/%s disagrees with unique lease tuple %s/%s/%s; no rollback attempted",
				result.SessionID, result.WindowID, result.PaneID, observed.SessionID, observed.WindowID, observed.PaneID)
		}
	}
	result = observed
	if err := m.bindCreatedProjectRouteAuthority(ctx, result, marker); err != nil {
		return fmt.Errorf("bind created Project recovery authority: %w", err)
	}
	target := m.BoundMutationTarget("session", result.SessionID, "lease:"+marker)
	target.Parent = result.WindowID + "/" + result.PaneID
	action := MaterializeMutationAction(mutationKillOwned,
		target,
		"exact created Project tuple and operation lease="+marker,
		"lease-owned created Project session is absent", "-t", result.SessionID)
	routeGuard := m.TargetRouteGuard(action)
	observeAbsent := func(ctx context.Context) (bool, error) {
		out, err := m.RoutedRunner().Run(ctx, "tmux", "list-sessions", "-F", "#{session_id}")
		if err != nil {
			if inttmux.IsNoServerFailure(err) {
				return true, nil
			}
			return false, err
		}
		if slices.Contains(strings.Fields(string(out)), result.SessionID) {
			return false, nil
		}
		return true, nil
	}
	return executeRuntimeMutationPlan(ctx, m.GuardedWriteSteps([]runtimeMutationStep{{
		Action:           action,
		TargetRouteGuard: routeGuard,
		Reobserve:        observeAbsent,
		Guard: func(ctx context.Context) error {
			owned, err := m.observeCreatedProjectReceipt(ctx, result, marker)
			if err != nil || !owned {
				err = errors.New("created Project lease rollback containment is absent or changed")
			}
			// The exact $N gone from the same printed server generation is
			// this kill's goal, so a concurrent removal is converged. The
			// route guard runs first, as it does before Reobserve.
			return runtimeMutationAbsenceConverged(ctx, err, func(ctx context.Context) (bool, error) {
				if err := routeGuard(ctx); err != nil {
					return false, err
				}
				return observeAbsent(ctx)
			})
		},
		Apply: func(ctx context.Context) error {
			_, err := runRuntimeMutationCommand(ctx, m.MutationRunner(action), action)
			return err
		},
	}}))
}

// bindCreatedProjectRecoveryRoute is deliberately weaker than the normal app
// route guard: a synchronous tmux hook may return an error after new-session
// created a fresh server but before its logical route marker was written. The
// immutable logical declaration discovers only the physical socket; the
// unique exact $/@/% tuple plus operation lease is the destructive authority.
func (m *materializer) bindCreatedProjectRecoveryRoute(ctx context.Context) (bool, error) {
	if filepath.IsAbs(m.ExpectedSocketPath) && filepath.Clean(m.ExpectedSocketPath) == m.ExpectedSocketPath {
		return true, nil
	}
	target, _, err := m.ExactMutationRoute()
	if err != nil {
		return false, err
	}
	if target.Flag() == "-S" {
		if !filepath.IsAbs(target.Value) || filepath.Clean(target.Value) != target.Value {
			return false, fmt.Errorf("created Project recovery route is not an absolute clean socket: %q", target.Value)
		}
		m.ExpectedSocketPath = target.Value
		return true, nil
	}
	if target.Flag() != "-L" {
		return false, errors.New("created Project recovery requires an exact logical or physical route")
	}
	out, err := m.RunnerAtTarget(target).Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if err != nil {
		if inttmux.IsNoServerFailure(err) {
			return false, nil
		}
		return false, fmt.Errorf("reobserve created Project recovery socket: %w", err)
	}
	observed := strings.TrimSpace(string(out))
	if !filepath.IsAbs(observed) || filepath.Clean(observed) != observed {
		return false, fmt.Errorf("created Project recovery observed non-absolute or unclean socket %q", observed)
	}
	m.ExpectedSocketPath = observed
	return true, nil
}

// requireOwnedSession validates the complete server-wide ownership proof for
// one existing session before an operation lease or identity mirror is written.
func (m *materializer) requireOwnedSession(ctx context.Context, project coremetadata.Project, sessionName string) (liveSessionIdentity, error) {
	identity, found, err := m.preflightSessionOwnership(ctx, project, sessionName)
	if err != nil {
		return liveSessionIdentity{}, err
	}
	if !found {
		return liveSessionIdentity{}, fmt.Errorf("create: tmux session %q disappeared during ownership preflight", sessionName)
	}
	return identity, nil
}

// preflightSessionOwnership rejects duplicate Project UID/root claims even
// when the selected session is absent. An absent selected name is available
// only when no other live session already owns either identity edge.
func (m *materializer) preflightSessionOwnership(ctx context.Context, project coremetadata.Project, sessionName string) (liveSessionIdentity, bool, error) {
	identities, err := m.sessionIdentities(ctx)
	if err != nil {
		if inttmux.IsNoServerFailure(err) {
			return liveSessionIdentity{}, false, nil
		}
		return liveSessionIdentity{}, false, err
	}
	wantRoot := candidates.CanonicalPath(project.Spec.Root)
	var named []liveSessionIdentity
	uidClaims, rootClaims := 0, 0
	for _, identity := range identities {
		if identity.Name == sessionName {
			named = append(named, identity)
		}
		if strings.TrimSpace(identity.UID) == project.Metadata.UID {
			uidClaims++
		}
		if root := candidates.CanonicalPath(identity.Root); root != "" && root == wantRoot {
			rootClaims++
		}
	}
	if len(named) != 1 {
		if len(named) == 0 && uidClaims == 0 && rootClaims == 0 {
			return liveSessionIdentity{}, false, nil
		}
		return liveSessionIdentity{}, false, fmt.Errorf("create: tmux session %q ownership is unavailable or ambiguous: found %d same-name sessions, uid claims=%d, root claims=%d", sessionName, len(named), uidClaims, rootClaims)
	}
	identity := named[0]
	gotRoot := candidates.CanonicalPath(identity.Root)
	if strings.TrimSpace(identity.UID) == "" || identity.UID != project.Metadata.UID || gotRoot == "" || gotRoot != wantRoot || uidClaims != 1 || rootClaims != 1 {
		return liveSessionIdentity{}, false, fmt.Errorf(
			"create: refuse foreign tmux session %q: project uid=%q root=%q, want unique uid=%q root=%q (uid claims=%d, root claims=%d)",
			sessionName, identity.UID, identity.Root, project.Metadata.UID, project.Spec.Root, uidClaims, rootClaims)
	}
	return identity, true, nil
}

// refuseUnregisteredSessionClaims is the read-only first-use variant of the
// ownership preflight. There is no durable Project UID to prove yet, so any
// same-name session or any session already claiming the discovered canonical
// root is foreign to this create and must be handled explicitly first.
func (m *materializer) refuseUnregisteredSessionClaims(ctx context.Context, sessionName, root string) error {
	identities, err := m.sessionIdentities(ctx)
	if err != nil {
		if inttmux.IsNoServerFailure(err) {
			return nil
		}
		return err
	}
	wantRoot := candidates.CanonicalPath(root)
	for _, identity := range identities {
		if identity.Name == sessionName {
			return fmt.Errorf("create: refuse live tmux session %q for an unregistered Project; import or reconcile it explicitly before create", sessionName)
		}
		if wantRoot != "" && candidates.CanonicalPath(identity.Root) == wantRoot {
			return fmt.Errorf("create: refuse unregistered Project root %q because tmux session %q already claims it", root, identity.Name)
		}
	}
	return nil
}

func (m *materializer) sessionIdentities(ctx context.Context) ([]liveSessionIdentity, error) {
	out, err := m.ReadTmux(ctx, "list-sessions", "-F", tmuxRowFormat(
		"#{session_id}", "#{session_name}", "#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.ProjectPathSession+"}"))
	if err != nil {
		if inttmux.IsNoServerFailure(err) {
			return nil, err
		}
		return nil, fmt.Errorf("list tmux session identities: %w", err)
	}
	rows, parseErr := strictTmuxRows(out, 4)
	if parseErr != nil {
		return nil, fmt.Errorf("list tmux session identities: %w", parseErr)
	}
	identities := make([]liveSessionIdentity, 0, len(rows))
	for _, fields := range rows {
		if exactTmuxHandle(fields[0], "$") == "" || strings.TrimSpace(fields[1]) == "" {
			return nil, fmt.Errorf("list tmux session identities: malformed session row")
		}
		identities = append(identities, liveSessionIdentity{ID: fields[0], Name: fields[1], UID: strings.TrimSpace(fields[2]), Root: strings.TrimSpace(fields[3])})
	}
	return identities, nil
}

func (m *materializer) mirrorWindow(ctx context.Context, target string, window coremetadata.Window) error {
	return m.RunIdentityWrites(ctx, "window", target, window.Metadata.UID, []IdentityPlanWrite{
		{Operands: []string{"-w", "-t", target, tmuxopts.AutomaticRenameWindow, "off"}, Effect: "automatic rename disabled"},
		{Operands: []string{"-w", "-t", target, "-q", tmuxopts.WindowUID, window.Metadata.UID}, Effect: "Window UID mirror equals Registry"},
		{Operands: []string{"-w", "-t", target, "-q", tmuxopts.WindowName, window.Metadata.Name}, Effect: "Window stable-name mirror equals Registry"},
	})
}

type IdentityPlanWrite = mutationruntime.IdentityPlanWrite

func (m *materializer) mirrorProject(ctx context.Context, target string, project coremetadata.Project) error {
	return m.RunIdentityWrites(ctx, "session", target, project.Metadata.UID, []IdentityPlanWrite{
		{Operands: []string{"-t", target, "-q", tmuxopts.ProjectUIDSession, project.Metadata.UID}, Effect: "Project UID mirror equals Registry"},
		{Operands: []string{"-t", target, "-q", tmuxopts.ProjectNameSession, project.Metadata.Name}, Effect: "Project name mirror equals Registry"},
	})
}

func (m *materializer) recordErrorCreatedSession(
	ctx context.Context,
	project coremetadata.Project,
	sessionName string,
	exactSessionID string,
	ledger *RuntimeLedger,
) {
	if ledger == nil || strings.TrimSpace(ledger.OperationMarker) == "" {
		return
	}
	target := exactTmuxHandle(exactSessionID, "$")
	if target == "" {
		exists, err := m.Sessions.SessionExists(ctx, sessionName)
		if err != nil || !exists {
			return
		}
		target = sessionName
	}
	out, err := m.RoutedRunner().Run(ctx, "tmux", "show-environment", "-t", target)
	if err != nil || sessionEnvironmentValue(string(out), createOperationEnvironment) != ledger.OperationMarker {
		if exactTmuxHandle(exactSessionID, "$") != "" {
			m.WarnUnclaimedHandle(runtimeSession, exactSessionID)
		}
		return
	}
	ledger.MarkSession(target)
	sessionID := exactTmuxHandle(exactSessionID, "$")
	if sessionID == "" {
		sessionID = m.Option(ctx, target, "#{session_id}")
	}
	if sessionID == "" {
		if m.Warn != nil {
			fmt.Fprintf(m.Warn, "projmux: create failed with an owned but unidentifiable tmux session %s; preserved it\n", sessionName)
		}
		return
	}
	if claimErr := m.ClaimRuntimeUIDForRollback(ctx, runtimeSession, sessionID, project.Metadata.UID, ledger); claimErr != nil {
		return
	}
	_ = m.mirrorProject(ctx, sessionID, project)
}

// windowIDForUID returns the tmux window id inside sessionName that mirrors uid.
func (m *materializer) windowIDForUID(ctx context.Context, sessionName, uid string) (string, error) {
	owner, found, err := m.windowRuntimeForUID(ctx, sessionName, uid)
	if err != nil || !found {
		return "", err
	}
	return owner.WindowID, nil
}

// windowRuntimeForUID resolves one stable Window UID to its current exact
// $N/@N containment. Duplicate claims are conflicting identity evidence and
// never degrade into first-row selection.
func (m *materializer) windowRuntimeForUID(ctx context.Context, sessionName, uid string) (RuntimeOwner, bool, error) {
	if strings.TrimSpace(uid) == "" {
		return RuntimeOwner{}, false, nil
	}
	inventory, err := m.windowRuntimeInventory(ctx, sessionName)
	if err != nil {
		return RuntimeOwner{}, false, err
	}
	return resolveWindowRuntimeUID(inventory, uid)
}

// windowRuntimeInventory takes one exact current snapshot for every stable
// Window UID in a session. Create transactions use the shared snapshot to
// project all existing-live Windows without holding the Registry lock across
// one tmux round trip per Window.
func (m *materializer) windowRuntimeInventory(ctx context.Context, sessionName string) (map[string][]RuntimeOwner, error) {
	out, err := m.ReadTmux(ctx, "list-windows", "-t", sessionName, "-F",
		tmuxRowFormat("#{"+tmuxopts.WindowUID+"}", "#{session_id}", "#{window_id}"))
	if err != nil {
		return nil, fmt.Errorf("list tmux windows of session %q: %w", sessionName, err)
	}
	inventory := make(map[string][]RuntimeOwner)
	for _, fields := range splitTmuxRows(out, 3) {
		uid := strings.TrimSpace(fields[0])
		if uid != "" {
			inventory[uid] = append(inventory[uid], RuntimeOwner{SessionID: fields[1], WindowID: fields[2]})
		}
	}
	return inventory, nil
}

func resolveWindowRuntimeUID(inventory map[string][]RuntimeOwner, uid string) (RuntimeOwner, bool, error) {
	owners := inventory[strings.TrimSpace(uid)]
	if len(owners) == 0 {
		return RuntimeOwner{}, false, nil
	}
	if len(owners) != 1 {
		return RuntimeOwner{}, false, fmt.Errorf("window uid %q has ambiguous current runtime containment", uid)
	}
	owner := owners[0]
	if exactTmuxHandle(owner.SessionID, "$") == "" || exactTmuxHandle(owner.WindowID, "@") == "" {
		return RuntimeOwner{}, false, fmt.Errorf("window uid %q has malformed current runtime containment", uid)
	}
	return owner, true, nil
}

// panesOf lists the panes of a tmux window as (mirrored uid, pane id) rows in
// tmux order.
func (m *materializer) panesOf(ctx context.Context, windowID string) ([][2]string, error) {
	out, err := m.ReadTmux(ctx, "list-panes", "-t", windowID, "-F",
		tmuxRowFormat("#{"+tmuxopts.PaneUID+"}", "#{pane_id}"))
	if err != nil {
		return nil, fmt.Errorf("list tmux panes of window %q: %w", windowID, err)
	}
	rows := splitTmuxRows(out, 2)
	panes := make([][2]string, 0, len(rows))
	for _, fields := range rows {
		panes = append(panes, [2]string{fields[0], fields[1]})
	}
	return panes, nil
}

type windowCreateResult struct {
	WindowID string
	PaneID   string
}

type RuntimeOwner = mutationruntime.RuntimeOwner

type runtimeOwnerSet map[RuntimeOwner]struct{}

type runtimeOwners map[string]runtimeOwnerSet

// newWindow creates one detached tmux Window and accepts it only when the
// composite output, global before/after inventory, and exact owner relation all
// identify the same sole new Window and sole new primary Pane.
func (m *materializer) newWindow(ctx context.Context, sessionID, name, cwd string, command []string) (windowCreateResult, error) {
	beforeWindows, beforePanes, beforeErr := m.runtimeOwners(ctx)
	if beforeErr != nil {
		return windowCreateResult{}, fmt.Errorf("inventory tmux runtime before window create: %w", beforeErr)
	}
	args := []string{"-d", "-P", "-F", tmuxRowFormat("#{window_id}", "#{pane_id}"), "-t", sessionID + ":"}
	if strings.TrimSpace(name) != "" {
		args = append(args, "-n", name)
	}
	if strings.TrimSpace(cwd) != "" {
		args = append(args, "-c", cwd)
	}
	action := MaterializeMutationAction(mutationCreateWindow,
		m.BoundMutationTarget("window-parent", sessionID, "window-name:"+name),
		"exact owned session containment="+sessionID,
		"one detached owned Window and primary Pane exist",
		args...)
	action.Command = slices.Clone(command)
	var rawOutput []byte
	// observedAfter is the inventory the post-effect observer read after the
	// write. Nothing of ours writes between that observation and the
	// attribution below, so the attribution reuses it instead of reading the
	// same two server-wide inventories again.
	var observedAfter *[2]runtimeOwners
	createErr := m.RunMaterializeMutation(ctx, action, func() error {
		currentWindows, currentPanes, err := m.runtimeOwners(ctx)
		if err != nil {
			return err
		}
		if !equalRuntimeOwners(beforeWindows, currentWindows) || !equalRuntimeOwners(beforePanes, currentPanes) {
			return fmt.Errorf("runtime owner inventory drifted before Window create")
		}
		return m.GuardMutationAction(ctx, action)
	}, func() error {
		var err error
		rawOutput, err = runRuntimeMutationCommand(ctx, m.Runner, action)
		return err
	}, func(ctx context.Context) (bool, error) {
		output := strings.TrimSpace(string(rawOutput))
		if output == "" {
			return false, nil
		}
		afterWindows, afterPanes, err := m.runtimeOwners(ctx)
		if err != nil {
			observedAfter = nil
			return false, err
		}
		observedAfter = &[2]runtimeOwners{afterWindows, afterPanes}
		_, err = attributeCreatedWindow(output, sessionID, beforeWindows, beforePanes, afterWindows, afterPanes)
		return err == nil, err
	})
	output := strings.TrimSpace(string(rawOutput))
	var afterWindows, afterPanes runtimeOwners
	var inventoryErr error
	if observedAfter != nil {
		afterWindows, afterPanes = observedAfter[0], observedAfter[1]
	} else {
		afterWindows, afterPanes, inventoryErr = m.runtimeOwners(ctx)
	}
	if inventoryErr != nil {
		m.warnCompositeWindowResult(output)
		inventoryFailure := fmt.Errorf("inventory tmux runtime after window create: %w", inventoryErr)
		if createErr != nil {
			return windowCreateResult{}, errors.Join(fmt.Errorf("create tmux window in session %q: %w", sessionID, createErr), inventoryFailure)
		}
		return windowCreateResult{}, inventoryFailure
	}
	result, attributionErr := attributeCreatedWindow(output, sessionID, beforeWindows, beforePanes, afterWindows, afterPanes)
	if attributionErr != nil {
		m.warnUnclaimedOwners("window", beforeWindows, afterWindows)
		m.warnUnclaimedOwners("pane", beforePanes, afterPanes)
		if createErr != nil {
			return windowCreateResult{}, errors.Join(fmt.Errorf("create tmux window in session %q: %w", sessionID, createErr), attributionErr)
		}
		return windowCreateResult{}, attributionErr
	}
	if createErr != nil {
		return result, fmt.Errorf("create tmux window in session %q: %w", sessionID, createErr)
	}
	return result, nil
}

func (m *materializer) runtimeOwners(ctx context.Context) (runtimeOwners, runtimeOwners, error) {
	windowsOut, err := m.ReadTmux(ctx, "list-windows", "-a", "-F", tmuxRowFormat("#{session_id}", "#{window_id}"))
	if err != nil {
		return nil, nil, err
	}
	panesOut, err := m.ReadTmux(ctx, "list-panes", "-a", "-F", tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}"))
	if err != nil {
		return nil, nil, err
	}
	windows := runtimeOwners{}
	windowRows, err := strictTmuxRows(windowsOut, 2)
	if err != nil {
		return nil, nil, fmt.Errorf("malformed Window owner inventory: %w", err)
	}
	for _, row := range windowRows {
		if exactTmuxHandle(row[0], "$") == "" || exactTmuxHandle(row[1], "@") == "" {
			return nil, nil, fmt.Errorf("malformed Window owner inventory")
		}
		owner := RuntimeOwner{SessionID: row[0], WindowID: row[1]}
		if windows[row[1]] == nil {
			windows[row[1]] = runtimeOwnerSet{}
		}
		windows[row[1]][owner] = struct{}{}
	}
	panes := runtimeOwners{}
	paneRows, err := strictTmuxRows(panesOut, 3)
	if err != nil {
		return nil, nil, fmt.Errorf("malformed Pane owner inventory: %w", err)
	}
	for _, row := range paneRows {
		if exactTmuxHandle(row[0], "$") == "" || exactTmuxHandle(row[1], "@") == "" || exactTmuxHandle(row[2], "%") == "" {
			return nil, nil, fmt.Errorf("malformed Pane owner inventory")
		}
		owner := RuntimeOwner{SessionID: row[0], WindowID: row[1]}
		if panes[row[2]] == nil {
			panes[row[2]] = runtimeOwnerSet{}
		}
		panes[row[2]][owner] = struct{}{}
	}
	return windows, panes, nil
}

func attributeCreatedWindow(
	output, sessionID string,
	beforeWindows, beforePanes, afterWindows, afterPanes runtimeOwners,
) (windowCreateResult, error) {
	rows := splitTmuxRows(output, 2)
	if len(rows) != 1 || exactTmuxHandle(rows[0][0], "@") == "" || exactTmuxHandle(rows[0][1], "%") == "" {
		return windowCreateResult{}, fmt.Errorf("create tmux window in session %q: malformed composite result %q", sessionID, output)
	}
	result := windowCreateResult{WindowID: rows[0][0], PaneID: rows[0][1]}
	newWindows := newRuntimeIDs(beforeWindows, afterWindows)
	newPanes := newRuntimeIDs(beforePanes, afterPanes)
	windowOwners, windowPresent := afterWindows[result.WindowID]
	paneOwners, panePresent := afterPanes[result.PaneID]
	wantWindowOwner := RuntimeOwner{SessionID: sessionID, WindowID: result.WindowID}
	wantPaneOwner := RuntimeOwner{SessionID: sessionID, WindowID: result.WindowID}
	if len(newWindows) != 1 || newWindows[0] != result.WindowID || len(newPanes) != 1 || newPanes[0] != result.PaneID ||
		!windowPresent || !windowOwners.contains(wantWindowOwner) || !panePresent || !paneOwners.contains(wantPaneOwner) ||
		!paneOwnerSetMatchesWindow(paneOwners, windowOwners, result.WindowID) {
		return windowCreateResult{}, fmt.Errorf(
			"create tmux window in session %q: attribution mismatch output=%s/%s new-windows=%v new-panes=%v window-owners=%v pane-owners=%v",
			sessionID, result.WindowID, result.PaneID, newWindows, newPanes, sortedRuntimeOwners(windowOwners), sortedRuntimeOwners(paneOwners))
	}
	return result, nil
}

func paneOwnerSetMatchesWindow(paneOwners, windowOwners runtimeOwnerSet, windowID string) bool {
	if len(paneOwners) == 0 {
		return false
	}
	for owner := range paneOwners {
		if owner.WindowID != windowID || !windowOwners.contains(owner) {
			return false
		}
	}
	return true
}

func (owners runtimeOwnerSet) contains(owner RuntimeOwner) bool {
	_, ok := owners[owner]
	return ok
}

func sortedRuntimeOwners(owners runtimeOwnerSet) []string {
	values := make([]string, 0, len(owners))
	for owner := range owners {
		values = append(values, owner.SessionID+"/"+owner.WindowID)
	}
	slices.Sort(values)
	return values
}

func newRuntimeIDs(before, after runtimeOwners) []string {
	var ids []string
	for id := range after {
		if _, existed := before[id]; !existed {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

func equalRuntimeOwners(left, right runtimeOwners) bool {
	if len(left) != len(right) {
		return false
	}
	for id, leftOwners := range left {
		rightOwners, ok := right[id]
		if !ok || len(leftOwners) != len(rightOwners) {
			return false
		}
		for owner := range leftOwners {
			if !rightOwners.contains(owner) {
				return false
			}
		}
	}
	return true
}

func (m *materializer) warnUnclaimedOwners(kind string, before, after runtimeOwners) {
	if m.Warn == nil {
		return
	}
	if residual := newRuntimeIDs(before, after); len(residual) > 0 {
		fmt.Fprintf(m.Warn, "projmux: create attribution failed with unclaimed tmux %s drift; preserved %s; inspect these exact handles before retry or cleanup\n",
			kind, strings.Join(residual, ", "))
	}
}

func (m *materializer) warnCompositeWindowResult(output string) {
	rows := splitTmuxRows(output, 2)
	if len(rows) != 1 {
		return
	}
	if id := exactTmuxHandle(rows[0][0], "@"); id != "" {
		m.WarnUnclaimedHandle(runtimeWindow, id)
	}
	if id := exactTmuxHandle(rows[0][1], "%"); id != "" {
		m.WarnUnclaimedHandle(runtimePane, id)
	}
}

// admitSplitAnchor limits the invocation target to the anchor being split.
// A process declaration has no tmux handle; an exact tmux sibling keeps its
// existing transport guard and split behavior on the same materializer.
func (t *processTerminalTarget) admitSplitAnchor(anchorPaneID string) error {
	if t == nil {
		return nil
	}
	pane, ok := t.registry.Pane(t.paneUID)
	if ok && anchorPaneID != pane.Status.Activation.RuntimeID {
		return nil
	}
	return t.admit(resourcegraph.ProcessSplit)
}

// retireOwnedPane removes one tmux pane this operation created, guarded by the
// exact Projmux uid it mirrored onto that pane.
//
// It is the same kill-owned action rollback issues, and for the same reason: the
// only pane this materializer may remove is one it can prove is still the pane
// it made. The guard re-reads the ownership option immediately before the write,
// so a pane that tmux renumbered, that a hook moved, or that something else
// replaced fails the guard instead of being killed. Unlike rollback this runs on
// the success path, where a failed kill must fail the operation rather than be
// warned about -- a Window left holding a shell the create promised to remove is
// not the result the caller was told it would get.
func (m *materializer) retireOwnedPane(ctx context.Context, paneID, paneUID string) error {
	action := MaterializeMutationAction(mutationKillOwned,
		m.BoundMutationTarget(string(runtimePane), paneID, paneUID),
		"same mirrored ownership uid="+paneUID,
		"owned created pane is absent",
		"-t", paneID)
	_, err := m.RunMutation(ctx, action)
	return err
}

// equalizeSplitLayout applies the same scoped, best-effort sizing used by the
// legacy AI split. It intentionally returns no error: layout observation is
// outside the create transaction's failure and rollback contract.
func (m *materializer) equalizeSplitLayout(ctx context.Context, anchorPaneID, placement string) {
	if m == nil || m.Runner == nil {
		return
	}
	observed, err := m.observeSplitLayoutBatch(ctx, anchorPaneID)
	if err != nil {
		return
	}
	resizes := planEvenSplitResizes(anchorPaneID, placement, observed.geometry())
	steps := make([]runtimeMutationStep, 0, len(resizes))
	if len(resizes) == 0 {
		return
	}

	// A layout is one observation/guard/effect batch with N individually
	// printable resize actions. The executor still validates and orders every
	// exact argv, but route and inventory evidence are shared per phase rather
	// than re-probed for every peer. This keeps the synchronous layout inside the
	// create transaction (the next split needs its capacity) without holding the
	// cross-process Registry lock for O(N) read/guard round trips.
	var routeChecked, semanticChecked, effectCached bool
	var routeErr, semanticErr, effectErr error
	var effect splitLayoutBatchReceipt
	batchTarget := m.BoundMutationTarget("pane", anchorPaneID, observed.paneUID(anchorPaneID))
	batchTarget.Parent = observed.sessionID + "/" + observed.windowID
	readEffect := func(ctx context.Context) (splitLayoutBatchReceipt, error) {
		if !effectCached {
			effect, effectErr = m.observeSplitLayoutBatch(ctx, anchorPaneID)
			effectCached = true
		}
		return effect, effectErr
	}
	guardRoute := func(ctx context.Context, action plannedRuntimeMutation) error {
		if action.Target.Socket != batchTarget.Socket || action.Target.PhysicalSocket != batchTarget.PhysicalSocket ||
			action.Target.RouteAuthority != batchTarget.RouteAuthority {
			return errors.New("split layout action route disagrees with printable batch authority")
		}
		if !routeChecked {
			routeErr = m.revalidatedTargetRouteGuard(action)(ctx)
			routeChecked = true
		}
		return routeErr
	}
	guardBatch := func(ctx context.Context, action plannedRuntimeMutation) error {
		if !semanticChecked {
			// Revalidate the same physical generation immediately before the first
			// write, then require the complete ordered identity/geometry receipt to
			// be byte-identical to planning. Unknown or drifted state writes zero.
			semanticErr = m.revalidatedTargetRouteGuard(action)(ctx)
			if semanticErr == nil {
				var current splitLayoutBatchReceipt
				current, semanticErr = m.observeSplitLayoutBatch(ctx, anchorPaneID)
				if semanticErr == nil && current.raw != observed.raw {
					semanticErr = errors.New("split layout inventory drifted before resize")
				}
			}
			semanticChecked = true
		}
		return semanticErr
	}
	for index, resize := range resizes {
		paneUID := observed.paneUID(resize.paneID)
		target := m.BoundMutationTarget("pane", resize.paneID, paneUID)
		target.Parent = observed.sessionID + "/" + observed.windowID
		action := MaterializeMutationAction(mutationWriteLayout,
			target,
			"exact ordered layout inventory="+observed.raw,
			"Pane "+resize.paneID+" size "+resize.axis+"="+fmt.Sprintf("%d", resize.size),
			"-t", resize.paneID, resize.axis, fmt.Sprintf("%d", resize.size))
		action.Order = index + 1
		steps = append(steps, runtimeMutationStep{
			Action:           action,
			TargetRouteGuard: func(ctx context.Context) error { return guardRoute(ctx, action) },
			Reobserve: func(ctx context.Context) (bool, error) {
				current, err := readEffect(ctx)
				if err != nil {
					return false, err
				}
				return current.effectObserved(action)
			},
			Guard: func(ctx context.Context) error { return guardBatch(ctx, action) },
			Apply: func(ctx context.Context) error {
				// The first Apply separates the pre-effect receipt cache from the
				// single post-effect receipt consumed after every ordered write.
				effectCached = false
				// Preserve the established presentation contract: each peer resize
				// is attempted even when an earlier best-effort resize failed.
				_, _ = runRuntimeMutationCommand(ctx, m.MutationRunner(action), action)
				return nil
			},
		})
	}
	// Layout remains best effort: a failed or partial resize cannot turn an
	// already successful managed create into a topology rollback. The important
	// boundary is that every attempted write came from one fully guarded plan.
	_ = executeRuntimeMutationPlan(ctx, m.GuardedWriteSteps(steps))
}

var splitLayoutBatchFormat = tmuxRowFormat(
	"#{session_id}", "#{window_id}", "#{pane_id}", "#{"+tmuxopts.PaneUID+"}",
	"#{pane_left}", "#{pane_top}", "#{pane_width}", "#{pane_height}",
)

type splitLayoutBatchPane struct {
	uid      string
	geometry aiPaneGeometry
}

type splitLayoutBatchReceipt struct {
	raw       string
	sessionID string
	windowID  string
	panes     map[string]splitLayoutBatchPane
	order     []string
}

func (m *materializer) observeSplitLayoutBatch(ctx context.Context, anchorPaneID string) (splitLayoutBatchReceipt, error) {
	out, err := m.ReadTmux(ctx, "list-panes", "-t", anchorPaneID, "-F", splitLayoutBatchFormat)
	if err != nil {
		return splitLayoutBatchReceipt{}, fmt.Errorf("observe split layout receipt: %w", err)
	}
	rows, err := strictTmuxRows(out, 8)
	if err != nil {
		return splitLayoutBatchReceipt{}, fmt.Errorf("observe split layout receipt: %w", err)
	}
	receipt := splitLayoutBatchReceipt{raw: out, panes: map[string]splitLayoutBatchPane{}}
	seenUIDs := map[string]string{}
	for _, row := range rows {
		sessionID, windowID, paneID := exactTmuxHandle(row[0], "$"), exactTmuxHandle(row[1], "@"), exactTmuxHandle(row[2], "%")
		uid := strings.TrimSpace(row[3])
		pane := aiPaneGeometry{
			id: paneID, left: parsePositiveInt(row[4]), top: parsePositiveInt(row[5]),
			width: parsePositiveInt(row[6]), height: parsePositiveInt(row[7]),
		}
		if sessionID == "" || windowID == "" || paneID == "" || uid == "" || pane.width <= 0 || pane.height <= 0 {
			return splitLayoutBatchReceipt{}, errors.New("split layout receipt contains incomplete identity or geometry")
		}
		if receipt.sessionID == "" {
			receipt.sessionID, receipt.windowID = sessionID, windowID
		}
		if sessionID != receipt.sessionID || windowID != receipt.windowID {
			return splitLayoutBatchReceipt{}, errors.New("split layout receipt crosses Window containment")
		}
		if _, duplicate := receipt.panes[paneID]; duplicate {
			return splitLayoutBatchReceipt{}, fmt.Errorf("split layout receipt repeats Pane %s", paneID)
		}
		if priorPane := seenUIDs[uid]; priorPane != "" {
			return splitLayoutBatchReceipt{}, fmt.Errorf("split layout receipt repeats Pane uid %q on %s and %s", uid, priorPane, paneID)
		}
		seenUIDs[uid] = paneID
		receipt.panes[paneID] = splitLayoutBatchPane{uid: uid, geometry: pane}
		receipt.order = append(receipt.order, paneID)
	}
	if _, ok := receipt.panes[anchorPaneID]; !ok {
		return splitLayoutBatchReceipt{}, fmt.Errorf("split layout receipt does not contain exact anchor %s", anchorPaneID)
	}
	return receipt, nil
}

func (r splitLayoutBatchReceipt) paneUID(paneID string) string {
	return r.panes[paneID].uid
}

func (r splitLayoutBatchReceipt) geometry() []aiPaneGeometry {
	panes := make([]aiPaneGeometry, 0, len(r.order))
	for _, paneID := range r.order {
		panes = append(panes, r.panes[paneID].geometry)
	}
	return panes
}

func (r splitLayoutBatchReceipt) effectObserved(action plannedRuntimeMutation) (bool, error) {
	if len(action.Operands) != 4 || action.Operands[0] != "-t" || action.Operands[1] != action.Target.ID {
		return false, errors.New("split layout action operands are incomplete")
	}
	pane, ok := r.panes[action.Target.ID]
	if !ok {
		return false, fmt.Errorf("split layout target %s disappeared", action.Target.ID)
	}
	if pane.uid != action.Target.UID || action.Target.Parent != r.sessionID+"/"+r.windowID {
		return false, errors.New("split layout target identity or containment drifted")
	}
	want, err := strconv.Atoi(action.Operands[3])
	if err != nil || want <= 0 {
		return false, errors.New("split layout action has an invalid desired size")
	}
	switch action.Operands[2] {
	case "-x":
		return pane.geometry.width == want, nil
	case "-y":
		return pane.geometry.height == want, nil
	default:
		return false, errors.New("split layout action has an invalid axis")
	}
}

// strictTmuxRows is the identity-boundary variant of splitTmuxRows. Inventory
// attribution must not silently discard a malformed row and then conclude an
// identity is absent or unique from the incomplete set.
func strictTmuxRows(output string, want int) ([][]string, error) {
	output = strings.ReplaceAll(output, tmuxRowSepFormat, tmuxRowSep)
	var rows [][]string
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, tmuxRowSep)
		if len(fields) != want {
			return nil, fmt.Errorf("row has %d fields, want %d", len(fields), want)
		}
		rows = append(rows, fields)
	}
	return rows, nil
}
