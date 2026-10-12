package runtime

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
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

type PersistentSessionLifecycle interface {
	PreparePersistentSessionCreate(context.Context, string, string, string, map[string]string) (inttmux.PersistentSessionCreateRequest, bool, error)
	CompletePersistentSessionCreate(context.Context, inttmux.PersistentSessionCreateRequest, intmux.NewSessionResult) error
	AbortPersistentSessionCreate()
}

type SessionStartupFinalizer interface {
	FinalizeSessionStartup(ctx context.Context, result intmux.NewSessionResult, sessionName, cwd, operationMarker string) error
}

func (m *Materializer) FinalizeSessionStartup(ctx context.Context, result intmux.NewSessionResult, sessionName, cwd string, ledger *RuntimeLedger) error {
	if !result.Created {
		return nil
	}
	finalizer, ok := m.Sessions.(SessionStartupFinalizer)
	if !ok {
		return errors.New("materialize tmux session: startup finalization is unavailable")
	}
	action := MaterializeMutationAction(MutationFinalizeSession,
		m.BoundMutationTarget("session", result.SessionID, "session:"+sessionName),
		"same create-operation lease="+ledger.OperationMarker,
		"startup hook contract is finalized once",
		"-t", result.SessionID, FinalizeOperationEnvironment, ledger.OperationMarker)
	if err := m.RunMaterializeMutation(ctx, action, func() error {
		out, err := m.ReadTmux(ctx, "show-environment", "-t", result.SessionID)
		if err != nil {
			return err
		}
		if SessionEnvironmentValue(out, CreateOperationEnvironment) != ledger.OperationMarker {
			return errors.New("create-operation lease changed")
		}
		return nil
	}, func() error {
		if err := finalizer.FinalizeSessionStartup(ctx, result, sessionName, cwd, ledger.OperationMarker); err != nil {
			return err
		}
		_, err := RunRuntimeMutationCommand(ctx, m.MutationRunner(action), action)
		return err
	}, func(ctx context.Context) (bool, error) {
		out, err := m.ReadTmux(ctx, "list-panes", "-s", "-t", result.SessionID, "-F",
			TmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}",
				"#{E:"+CreateOperationEnvironment+"}", "#{E:"+FinalizeOperationEnvironment+"}"))
		if err != nil {
			return false, err
		}
		rows := SplitTmuxRows(out, 5)
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

type LiveSessionIdentity struct {
	ID   string
	Name string
	UID  string
	Root string
}

// Materializer turns offline Project/Window/Pane metadata into detached tmux
// objects.
//
// Every command it issues is detached. It never calls switch-client,
// attach-session, select-window, or select-pane, and it never reads which
// client or pane is focused, so the operator's view is byte-identical before
// and after a create.
type Materializer struct {
	MaterializeRuntime

	Mirror intmetadata.Mirror

	// executable resolves this build's own binary for the managed process
	// supervisor a launched pane execs. Nil means os.Executable.
	Executable func() (string, error)
	// lookupEnv is the environment probe used only as the last fallback when
	// the exact server reports no default-shell. Nil means os.Getenv.
	LookupEnv func(string) string
	// configPath optionally pins the generated app tmux config in tests and
	// embedded callers. Production resolves the same internal XDG path lazily.
	ConfigPath string

	// routeIdentityDisabled is a test seam that keeps openRouteIdentityCache
	// from opening a cache, so every guard reads tmux as it did before reuse
	// existed. Production leaves it false.
	RouteIdentityDisabled bool

	// paneDefaults is the default-shell/default-command pair a create read
	// before taking the Registry lock (prefetchPaneDefaults). It is nil outside
	// that create's transaction.
	PaneDefaults *PaneDefaultsMemo
}

// openRouteIdentityCache begins one transaction's identity-reuse scope. The
// first guard still proves the identity against tmux; only an exactly equal
// tuple may reuse that proof, and only until closeRouteIdentityCache.
func (m *Materializer) OpenRouteIdentityCache(operationID string) {
	if m == nil {
		return
	}
	if m.RouteIdentityDisabled {
		m.RouteIdentity = nil
		return
	}
	m.RouteIdentity = NewRuntimeRouteIdentityCache(operationID)
}

// closeRouteIdentityCache ends the scope. It is idempotent, and everything
// after it -- runtime ledger rollback included -- re-proves identity.
func (m *Materializer) CloseRouteIdentityCache() {
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
func (m *Materializer) ReproveReusedRouteIdentity(ctx context.Context) error {
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
func (m *Materializer) RevalidatedTargetRouteGuard(action PlannedRuntimeMutation) func(context.Context) error {
	guard := m.TargetRouteGuard(action)
	return func(ctx context.Context) error {
		m.InvalidateRouteIdentity("pre-write-revalidation")
		return guard(ctx)
	}
}

func (m *Materializer) GeneratedAppConfigPath() (string, error) {
	path := strings.TrimSpace(m.ConfigPath)
	if path == "" {
		resolved, err := GeneratedAppConfigDefaultPath(os.UserHomeDir, m.LookupEnv)
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

// SessionFirstPaneActivation issues the activation of the Pane a new session
// brings with it. It runs inside the caller's Registry transaction, and only
// once new-session is certain to run: a session that is already live keeps
// the generation its first Pane already runs under. A zero spec launches the
// Pane unsupervised, as before.
type SessionFirstPaneActivation func() (SuperviseSpec, error)

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
// ObserveActivationRuntime in the same transaction, after it has claimed the
// Pane. It is zero when the session already existed.
func (m *Materializer) EnsureSessionLaunching(
	ctx context.Context,
	project coremetadata.Project,
	sessionName, runtimeCWD, firstWindowName string,
	firstPane SessionFirstPaneActivation,
	ledger *RuntimeLedger,
	firstCommand ...[]string,
) (intmux.NewSessionResult, SuperviseSpec, error) {
	// Bind an existing invocation server to its physical socket before the
	// printable declaration is built. A genuinely absent server remains the
	// one create-session-only absent-before-create case.
	if err := m.GuardExactRoute(ctx, true); err != nil {
		return intmux.NewSessionResult{}, SuperviseSpec{}, err
	}
	startsFreshServer := m.ExpectedSocketPath == ""
	exists, err := m.Sessions.SessionExists(ctx, sessionName)
	if err != nil {
		return intmux.NewSessionResult{}, SuperviseSpec{}, fmt.Errorf("check tmux session %q: %w", sessionName, err)
	}
	if exists {
		identity, err := m.RequireOwnedSession(ctx, project, sessionName)
		if err != nil {
			return intmux.NewSessionResult{}, SuperviseSpec{}, err
		}
		if err := m.MarkCreateOperation(ctx, identity.ID, ledger); err != nil {
			return intmux.NewSessionResult{}, SuperviseSpec{}, err
		}
		return intmux.NewSessionResult{Created: false, SessionID: identity.ID}, SuperviseSpec{}, nil
	}
	if identity, found, err := m.PreflightSessionOwnership(ctx, project, sessionName); err != nil {
		return intmux.NewSessionResult{}, SuperviseSpec{}, err
	} else if found {
		if err := m.MarkCreateOperation(ctx, identity.ID, ledger); err != nil {
			return intmux.NewSessionResult{}, SuperviseSpec{}, err
		}
		return intmux.NewSessionResult{Created: false, SessionID: identity.ID}, SuperviseSpec{}, nil
	}
	lifecycle, ok := m.Sessions.(PersistentSessionLifecycle)
	if !ok {
		return intmux.NewSessionResult{}, SuperviseSpec{}, errors.New("materialize tmux session: typed persistent lifecycle seam is unavailable")
	}
	request, appeared, err := lifecycle.PreparePersistentSessionCreate(ctx, sessionName, runtimeCWD, project.Spec.Root,
		map[string]string{CreateOperationEnvironment: ledger.OperationMarker})
	if err != nil {
		return intmux.NewSessionResult{}, SuperviseSpec{}, err
	}
	if appeared {
		identity, err := m.RequireOwnedSession(ctx, project, sessionName)
		if err != nil {
			return intmux.NewSessionResult{}, SuperviseSpec{}, err
		}
		if err := m.MarkCreateOperation(ctx, identity.ID, ledger); err != nil {
			return intmux.NewSessionResult{}, SuperviseSpec{}, err
		}
		return intmux.NewSessionResult{Created: false, SessionID: identity.ID}, SuperviseSpec{}, nil
	}
	var (
		activation SuperviseSpec
		launch     []string
	)
	if firstPane != nil {
		issued, err := firstPane()
		if err != nil {
			lifecycle.AbortPersistentSessionCreate()
			return intmux.NewSessionResult{}, SuperviseSpec{}, err
		}
		if issued.Valid() {
			activation = issued
			var command []string
			if len(firstCommand) > 0 {
				command = firstCommand[0]
			}
			if startsFreshServer && len(command) == 0 {
				launch = m.SupervisedLaunchOnFreshServer(activation)
			} else {
				launch = m.SupervisedLaunch(ctx, activation, command)
			}
		}
	}
	args := []string{}
	if startsFreshServer {
		configPath, err := m.GeneratedAppConfigPath()
		if err != nil {
			lifecycle.AbortPersistentSessionCreate()
			return intmux.NewSessionResult{}, SuperviseSpec{}, err
		}
		args = append(args, "-f", configPath)
	}
	args = append(args, "-d", "-P", "-F", TmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}"), "-s", request.SessionName)
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
	action := MaterializeMutationAction(MutationCreateSession,
		m.BoundMutationTarget("project-declaration", sessionName, project.Metadata.UID),
		"unique Project uid/root or absent session name",
		"one detached owned session exists", args...)
	action.Command = launch
	var rawOutput []byte
	ensureErr := m.RunMaterializeMutation(ctx, action, func() error {
		_, found, guardErr := m.PreflightSessionOwnership(ctx, project, sessionName)
		if guardErr != nil {
			return guardErr
		}
		if found {
			return fmt.Errorf("session %q appeared after the create plan was built", sessionName)
		}
		return nil
	}, func() error {
		var runErr error
		rawOutput, runErr = RunRuntimeMutationCommand(ctx, m.Runner, action)
		return runErr
	}, func(ctx context.Context) (bool, error) {
		resultRows := SplitTmuxRows(string(rawOutput), 3)
		if len(resultRows) != 1 || ExactTmuxHandle(resultRows[0][0], "$") == "" ||
			ExactTmuxHandle(resultRows[0][1], "@") == "" || ExactTmuxHandle(resultRows[0][2], "%") == "" {
			return false, nil
		}
		out, err := m.ReadTmux(ctx, "list-panes", "-s", "-t", resultRows[0][0], "-F",
			TmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{session_name}",
				"#{E:"+CreateOperationEnvironment+"}"))
		if err != nil {
			return false, err
		}
		rows := SplitTmuxRows(out, 5)
		return len(rows) == 1 && rows[0][0] == resultRows[0][0] && rows[0][1] == resultRows[0][1] &&
			rows[0][2] == resultRows[0][2] && rows[0][3] == sessionName && rows[0][4] == ledger.OperationMarker, nil
	})
	rows := SplitTmuxRows(string(rawOutput), 3)
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
			if rollbackErr := m.RecoverCreatedProjectByLease(ctx, result, ledger.OperationMarker); rollbackErr != nil {
				ensureErr = errors.Join(ensureErr, fmt.Errorf("created Project owned rollback incomplete: %w", rollbackErr))
			}
		} else {
			m.RecordErrorCreatedSession(ctx, project, sessionName, result.SessionID, ledger)
		}
		lifecycle.AbortPersistentSessionCreate()
		return intmux.NewSessionResult{}, SuperviseSpec{}, fmt.Errorf("materialize tmux session %q: %w", sessionName, ensureErr)
	}
	if ExactTmuxHandle(result.SessionID, "$") == "" || ExactTmuxHandle(result.WindowID, "@") == "" || ExactTmuxHandle(result.PaneID, "%") == "" {
		if startsFreshServer {
			if rollbackErr := m.RecoverCreatedProjectByLease(ctx, result, ledger.OperationMarker); rollbackErr != nil {
				return intmux.NewSessionResult{}, SuperviseSpec{}, errors.Join(errors.New("materialize tmux session: atomic result is incomplete"), rollbackErr)
			}
		} else {
			m.RecordErrorCreatedSession(ctx, project, sessionName, result.SessionID, ledger)
		}
		lifecycle.AbortPersistentSessionCreate()
		return intmux.NewSessionResult{}, SuperviseSpec{}, fmt.Errorf("materialize tmux session %q: atomic result is incomplete", sessionName)
	}
	if startsFreshServer {
		if err := m.BindCreatedProjectRouteAuthority(ctx, result, ledger.OperationMarker); err != nil {
			if rollbackErr := m.RecoverCreatedProjectByLease(ctx, result, ledger.OperationMarker); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("created Project owned rollback incomplete: %w", rollbackErr))
			}
			lifecycle.AbortPersistentSessionCreate()
			return intmux.NewSessionResult{}, SuperviseSpec{}, err
		}
		if err := m.WriteCreatedProjectRouteMarker(ctx, result, ledger.OperationMarker); err != nil {
			if rollbackErr := m.RecoverCreatedProjectByLease(ctx, result, ledger.OperationMarker); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("created Project owned rollback incomplete: %w", rollbackErr))
			}
			lifecycle.AbortPersistentSessionCreate()
			return intmux.NewSessionResult{}, SuperviseSpec{}, err
		}
		if err := m.GuardExactRoute(ctx, false, m.ExpectedSocketPath); err != nil {
			if rollbackErr := m.RecoverCreatedProjectByLease(ctx, result, ledger.OperationMarker); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("created Project owned rollback incomplete: %w", rollbackErr))
			}
			lifecycle.AbortPersistentSessionCreate()
			return intmux.NewSessionResult{}, SuperviseSpec{}, err
		}
	}
	if err := m.WriteCreatedProjectAnchor(ctx, result, project, ledger.OperationMarker); err != nil {
		m.RecordErrorCreatedSession(ctx, project, sessionName, result.SessionID, ledger)
		lifecycle.AbortPersistentSessionCreate()
		return intmux.NewSessionResult{}, SuperviseSpec{}, err
	}
	if err := lifecycle.CompletePersistentSessionCreate(ctx, request, result); err != nil {
		m.RecordErrorCreatedSession(ctx, project, sessionName, result.SessionID, ledger)
		return intmux.NewSessionResult{}, SuperviseSpec{}, err
	}
	ledger.MarkSession(result.SessionID)
	if claimErr := m.ClaimRuntimeUIDForRollback(ctx, RuntimeSession, result.SessionID, project.Metadata.UID, ledger); claimErr != nil {
		return intmux.NewSessionResult{}, SuperviseSpec{}, claimErr
	}
	if err := m.MirrorProjectIdentity(ctx, result.SessionID, project); err != nil {
		return intmux.NewSessionResult{}, SuperviseSpec{}, err
	}
	return result, activation, nil
}

func (m *Materializer) BindCreatedProjectRouteAuthority(ctx context.Context, result intmux.NewSessionResult, marker string) error {
	if !filepath.IsAbs(m.ExpectedSocketPath) || filepath.Clean(m.ExpectedSocketPath) != m.ExpectedSocketPath {
		return errors.New("created Project route authority has no exact physical socket")
	}
	out, err := m.RunnerAtPhysicalSocket(m.ExpectedSocketPath).Run(ctx, "tmux", "display-message", "-p", "-t", result.PaneID, "-F",
		TmuxRowFormat("#{socket_path}", "#{pid}", "#{session_id}", "#{window_id}", "#{pane_id}", "#{E:"+CreateOperationEnvironment+"}"))
	if err != nil {
		return fmt.Errorf("observe created Project route authority: %w", err)
	}
	rows := SplitTmuxRows(string(out), 6)
	if len(rows) != 1 || rows[0][0] != m.ExpectedSocketPath ||
		rows[0][2] != result.SessionID || rows[0][3] != result.WindowID || rows[0][4] != result.PaneID || rows[0][5] != marker {
		return errors.New("created Project route authority socket or $/@/% receipt drifted")
	}
	pid, pidErr := strconv.Atoi(strings.TrimSpace(rows[0][1]))
	if pidErr != nil || pid <= 0 {
		return errors.New("created Project route authority server pid is invalid")
	}
	m.RouteAuthority = &RuntimeMutationRouteAuthority{
		Class: RuntimeMutationRouteApp, ServerPID: rows[0][1],
		SessionID: result.SessionID, WindowID: result.WindowID, PaneID: result.PaneID,
	}
	return nil
}

func (m *Materializer) WriteCreatedProjectAnchor(ctx context.Context, result intmux.NewSessionResult, project coremetadata.Project, marker string) error {
	action := MaterializeMutationAction(MutationWriteProjectAnchor,
		m.BoundMutationTarget("session", result.SessionID, project.Metadata.UID),
		"exact created tuple and operation lease="+marker,
		"Project path anchor equals Registry root",
		"-t", result.SessionID, "-q", inttmux.ProjectPathSessionOption, project.Spec.Root)
	observeReceipt := func(ctx context.Context) (bool, error) {
		out, err := m.ReadTmux(ctx, "list-panes", "-s", "-t", result.SessionID, "-F",
			TmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{E:"+CreateOperationEnvironment+"}"))
		rows := SplitTmuxRows(out, 4)
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
		_, err := RunRuntimeMutationCommand(ctx, m.Runner, action)
		return err
	}, observeReceipt)
}

func (m *Materializer) ObserveCreatedProjectReceipt(ctx context.Context, result intmux.NewSessionResult, marker string) (bool, error) {
	out, err := m.ReadTmux(ctx, "list-panes", "-s", "-t", result.SessionID, "-F",
		TmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{E:"+CreateOperationEnvironment+"}"))
	if err != nil {
		return false, err
	}
	rows := SplitTmuxRows(out, 4)
	return len(rows) == 1 && rows[0][0] == result.SessionID && rows[0][1] == result.WindowID &&
		rows[0][2] == result.PaneID && rows[0][3] == marker, nil
}

func (m *Materializer) WriteCreatedProjectRouteMarker(ctx context.Context, result intmux.NewSessionResult, marker string) error {
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
	action := NewRuntimeMutation(1, MutationWriteRouteMarker, printableTarget)
	BindRuntimeMutationGuard(&action, "exact created Project tuple and operation lease="+marker)
	action.Operands = []string{"-S", m.ExpectedSocketPath, "-gq", RuntimeMutationSocketNameOption, target.Value}
	observeReceipt := func(ctx context.Context) (bool, error) {
		if err := m.GuardExactAppRoute(ctx, false, action.Target.PhysicalSocket); err != nil {
			return false, err
		}
		return m.ObserveCreatedProjectReceipt(ctx, result, marker)
	}
	return ExecuteRuntimeMutationPlan(ctx, m.GuardedWriteSteps([]RuntimeMutationStep{{
		Action:           action,
		TargetRouteGuard: m.TargetRouteGuard(action),
		Reobserve: func(ctx context.Context) (bool, error) {
			owned, err := observeReceipt(ctx)
			if err != nil || !owned {
				return false, err
			}
			out, err := m.RoutedRunner().Run(ctx, "tmux", "show-options", "-gqv", RuntimeMutationSocketNameOption)
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
			out, err := m.RoutedRunner().Run(ctx, "tmux", "show-options", "-gqv", RuntimeMutationSocketNameOption)
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
			_, err := RunRuntimeMutationCommand(ctx, m.Runner, action)
			return err
		},
	}}))
}

func (m *Materializer) RecoverCreatedProjectByLease(ctx context.Context, result intmux.NewSessionResult, marker string) error {
	if strings.TrimSpace(marker) == "" {
		return nil
	}
	bound, err := m.BindCreatedProjectRecoveryRoute(ctx)
	if err != nil || !bound {
		return err
	}
	out, err := m.RoutedRunner().Run(ctx, "tmux", "list-sessions", "-F",
		TmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{E:"+CreateOperationEnvironment+"}"))
	if err != nil {
		if inttmux.IsNoServerFailure(err) {
			return nil
		}
		return fmt.Errorf("observe created Project lease for rollback: %w", err)
	}
	var matched [][]string
	for _, row := range SplitTmuxRows(string(out), 4) {
		if row[3] == marker && ExactTmuxHandle(row[0], "$") != "" && ExactTmuxHandle(row[1], "@") != "" && ExactTmuxHandle(row[2], "%") != "" {
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
	if ExactTmuxHandle(result.SessionID, "$") != "" && ExactTmuxHandle(result.WindowID, "@") != "" && ExactTmuxHandle(result.PaneID, "%") != "" {
		if result.SessionID != observed.SessionID || result.WindowID != observed.WindowID || result.PaneID != observed.PaneID {
			return fmt.Errorf("created Project result tuple %s/%s/%s disagrees with unique lease tuple %s/%s/%s; no rollback attempted",
				result.SessionID, result.WindowID, result.PaneID, observed.SessionID, observed.WindowID, observed.PaneID)
		}
	}
	result = observed
	if err := m.BindCreatedProjectRouteAuthority(ctx, result, marker); err != nil {
		return fmt.Errorf("bind created Project recovery authority: %w", err)
	}
	target := m.BoundMutationTarget("session", result.SessionID, "lease:"+marker)
	target.Parent = result.WindowID + "/" + result.PaneID
	action := MaterializeMutationAction(MutationKillOwned,
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
	return ExecuteRuntimeMutationPlan(ctx, m.GuardedWriteSteps([]RuntimeMutationStep{{
		Action:           action,
		TargetRouteGuard: routeGuard,
		Reobserve:        observeAbsent,
		Guard: func(ctx context.Context) error {
			owned, err := m.ObserveCreatedProjectReceipt(ctx, result, marker)
			if err != nil || !owned {
				err = errors.New("created Project lease rollback containment is absent or changed")
			}
			// The exact $N gone from the same printed server generation is
			// this kill's goal, so a concurrent removal is converged. The
			// route guard runs first, as it does before Reobserve.
			return RuntimeMutationAbsenceConverged(ctx, err, func(ctx context.Context) (bool, error) {
				if err := routeGuard(ctx); err != nil {
					return false, err
				}
				return observeAbsent(ctx)
			})
		},
		Apply: func(ctx context.Context) error {
			_, err := RunRuntimeMutationCommand(ctx, m.MutationRunner(action), action)
			return err
		},
	}}))
}

// bindCreatedProjectRecoveryRoute is deliberately weaker than the normal app
// route guard: a synchronous tmux hook may return an error after new-session
// created a fresh server but before its logical route marker was written. The
// immutable logical declaration discovers only the physical socket; the
// unique exact $/@/% tuple plus operation lease is the destructive authority.
func (m *Materializer) BindCreatedProjectRecoveryRoute(ctx context.Context) (bool, error) {
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
func (m *Materializer) RequireOwnedSession(ctx context.Context, project coremetadata.Project, sessionName string) (LiveSessionIdentity, error) {
	identity, found, err := m.PreflightSessionOwnership(ctx, project, sessionName)
	if err != nil {
		return LiveSessionIdentity{}, err
	}
	if !found {
		return LiveSessionIdentity{}, fmt.Errorf("create: tmux session %q disappeared during ownership preflight", sessionName)
	}
	return identity, nil
}

// preflightSessionOwnership rejects duplicate Project UID/root claims even
// when the selected session is absent. An absent selected name is available
// only when no other live session already owns either identity edge.
func (m *Materializer) PreflightSessionOwnership(ctx context.Context, project coremetadata.Project, sessionName string) (LiveSessionIdentity, bool, error) {
	identities, err := m.SessionIdentities(ctx)
	if err != nil {
		if inttmux.IsNoServerFailure(err) {
			return LiveSessionIdentity{}, false, nil
		}
		return LiveSessionIdentity{}, false, err
	}
	wantRoot := candidates.CanonicalPath(project.Spec.Root)
	var named []LiveSessionIdentity
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
			return LiveSessionIdentity{}, false, nil
		}
		return LiveSessionIdentity{}, false, fmt.Errorf("create: tmux session %q ownership is unavailable or ambiguous: found %d same-name sessions, uid claims=%d, root claims=%d", sessionName, len(named), uidClaims, rootClaims)
	}
	identity := named[0]
	gotRoot := candidates.CanonicalPath(identity.Root)
	if strings.TrimSpace(identity.UID) == "" || identity.UID != project.Metadata.UID || gotRoot == "" || gotRoot != wantRoot || uidClaims != 1 || rootClaims != 1 {
		return LiveSessionIdentity{}, false, fmt.Errorf(
			"create: refuse foreign tmux session %q: project uid=%q root=%q, want unique uid=%q root=%q (uid claims=%d, root claims=%d)",
			sessionName, identity.UID, identity.Root, project.Metadata.UID, project.Spec.Root, uidClaims, rootClaims)
	}
	return identity, true, nil
}

// refuseUnregisteredSessionClaims is the read-only first-use variant of the
// ownership preflight. There is no durable Project UID to prove yet, so any
// same-name session or any session already claiming the discovered canonical
// root is foreign to this create and must be handled explicitly first.
func (m *Materializer) RefuseUnregisteredSessionClaims(ctx context.Context, sessionName, root string) error {
	identities, err := m.SessionIdentities(ctx)
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

func (m *Materializer) SessionIdentities(ctx context.Context) ([]LiveSessionIdentity, error) {
	out, err := m.ReadTmux(ctx, "list-sessions", "-F", TmuxRowFormat(
		"#{session_id}", "#{session_name}", "#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.ProjectPathSession+"}"))
	if err != nil {
		if inttmux.IsNoServerFailure(err) {
			return nil, err
		}
		return nil, fmt.Errorf("list tmux session identities: %w", err)
	}
	rows, parseErr := StrictTmuxRows(out, 4)
	if parseErr != nil {
		return nil, fmt.Errorf("list tmux session identities: %w", parseErr)
	}
	identities := make([]LiveSessionIdentity, 0, len(rows))
	for _, fields := range rows {
		if ExactTmuxHandle(fields[0], "$") == "" || strings.TrimSpace(fields[1]) == "" {
			return nil, fmt.Errorf("list tmux session identities: malformed session row")
		}
		identities = append(identities, LiveSessionIdentity{ID: fields[0], Name: fields[1], UID: strings.TrimSpace(fields[2]), Root: strings.TrimSpace(fields[3])})
	}
	return identities, nil
}

func (m *Materializer) MirrorWindowIdentity(ctx context.Context, target string, window coremetadata.Window) error {
	return m.RunIdentityWrites(ctx, "window", target, window.Metadata.UID, []IdentityPlanWrite{
		{Operands: []string{"-w", "-t", target, tmuxopts.AutomaticRenameWindow, "off"}, Effect: "automatic rename disabled"},
		{Operands: []string{"-w", "-t", target, "-q", tmuxopts.WindowUID, window.Metadata.UID}, Effect: "Window UID mirror equals Registry"},
		{Operands: []string{"-w", "-t", target, "-q", tmuxopts.WindowName, window.Metadata.Name}, Effect: "Window stable-name mirror equals Registry"},
	})
}

func (m *Materializer) MirrorProjectIdentity(ctx context.Context, target string, project coremetadata.Project) error {
	return m.RunIdentityWrites(ctx, "session", target, project.Metadata.UID, []IdentityPlanWrite{
		{Operands: []string{"-t", target, "-q", tmuxopts.ProjectUIDSession, project.Metadata.UID}, Effect: "Project UID mirror equals Registry"},
		{Operands: []string{"-t", target, "-q", tmuxopts.ProjectNameSession, project.Metadata.Name}, Effect: "Project name mirror equals Registry"},
	})
}

func (m *Materializer) RecordErrorCreatedSession(
	ctx context.Context,
	project coremetadata.Project,
	sessionName string,
	exactSessionID string,
	ledger *RuntimeLedger,
) {
	if ledger == nil || strings.TrimSpace(ledger.OperationMarker) == "" {
		return
	}
	target := ExactTmuxHandle(exactSessionID, "$")
	if target == "" {
		exists, err := m.Sessions.SessionExists(ctx, sessionName)
		if err != nil || !exists {
			return
		}
		target = sessionName
	}
	out, err := m.RoutedRunner().Run(ctx, "tmux", "show-environment", "-t", target)
	if err != nil || SessionEnvironmentValue(string(out), CreateOperationEnvironment) != ledger.OperationMarker {
		if ExactTmuxHandle(exactSessionID, "$") != "" {
			m.WarnUnclaimedHandle(RuntimeSession, exactSessionID)
		}
		return
	}
	ledger.MarkSession(target)
	sessionID := ExactTmuxHandle(exactSessionID, "$")
	if sessionID == "" {
		sessionID = m.Option(ctx, target, "#{session_id}")
	}
	if sessionID == "" {
		if m.Warn != nil {
			fmt.Fprintf(m.Warn, "projmux: create failed with an owned but unidentifiable tmux session %s; preserved it\n", sessionName)
		}
		return
	}
	if claimErr := m.ClaimRuntimeUIDForRollback(ctx, RuntimeSession, sessionID, project.Metadata.UID, ledger); claimErr != nil {
		return
	}
	_ = m.MirrorProjectIdentity(ctx, sessionID, project)
}

// windowIDForUID returns the tmux window id inside sessionName that mirrors uid.
func (m *Materializer) WindowIDForUID(ctx context.Context, sessionName, uid string) (string, error) {
	owner, found, err := m.WindowRuntimeForUID(ctx, sessionName, uid)
	if err != nil || !found {
		return "", err
	}
	return owner.WindowID, nil
}

// windowRuntimeForUID resolves one stable Window UID to its current exact
// $N/@N containment. Duplicate claims are conflicting identity evidence and
// never degrade into first-row selection.
func (m *Materializer) WindowRuntimeForUID(ctx context.Context, sessionName, uid string) (RuntimeOwner, bool, error) {
	if strings.TrimSpace(uid) == "" {
		return RuntimeOwner{}, false, nil
	}
	inventory, err := m.WindowRuntimeInventory(ctx, sessionName)
	if err != nil {
		return RuntimeOwner{}, false, err
	}
	return ResolveWindowRuntimeUID(inventory, uid)
}

// windowRuntimeInventory takes one exact current snapshot for every stable
// Window UID in a session. Create transactions use the shared snapshot to
// project all existing-live Windows without holding the Registry lock across
// one tmux round trip per Window.
func (m *Materializer) WindowRuntimeInventory(ctx context.Context, sessionName string) (map[string][]RuntimeOwner, error) {
	out, err := m.ReadTmux(ctx, "list-windows", "-t", sessionName, "-F",
		TmuxRowFormat("#{"+tmuxopts.WindowUID+"}", "#{session_id}", "#{window_id}"))
	if err != nil {
		return nil, fmt.Errorf("list tmux windows of session %q: %w", sessionName, err)
	}
	inventory := make(map[string][]RuntimeOwner)
	for _, fields := range SplitTmuxRows(out, 3) {
		uid := strings.TrimSpace(fields[0])
		if uid != "" {
			inventory[uid] = append(inventory[uid], RuntimeOwner{SessionID: fields[1], WindowID: fields[2]})
		}
	}
	return inventory, nil
}

func ResolveWindowRuntimeUID(inventory map[string][]RuntimeOwner, uid string) (RuntimeOwner, bool, error) {
	owners := inventory[strings.TrimSpace(uid)]
	if len(owners) == 0 {
		return RuntimeOwner{}, false, nil
	}
	if len(owners) != 1 {
		return RuntimeOwner{}, false, fmt.Errorf("window uid %q has ambiguous current runtime containment", uid)
	}
	owner := owners[0]
	if ExactTmuxHandle(owner.SessionID, "$") == "" || ExactTmuxHandle(owner.WindowID, "@") == "" {
		return RuntimeOwner{}, false, fmt.Errorf("window uid %q has malformed current runtime containment", uid)
	}
	return owner, true, nil
}

// panesOf lists the panes of a tmux window as (mirrored uid, pane id) rows in
// tmux order.
func (m *Materializer) PanesOf(ctx context.Context, windowID string) ([][2]string, error) {
	out, err := m.ReadTmux(ctx, "list-panes", "-t", windowID, "-F",
		TmuxRowFormat("#{"+tmuxopts.PaneUID+"}", "#{pane_id}"))
	if err != nil {
		return nil, fmt.Errorf("list tmux panes of window %q: %w", windowID, err)
	}
	rows := SplitTmuxRows(out, 2)
	panes := make([][2]string, 0, len(rows))
	for _, fields := range rows {
		panes = append(panes, [2]string{fields[0], fields[1]})
	}
	return panes, nil
}

type WindowCreateResult struct {
	WindowID string
	PaneID   string
}

type RuntimeOwnerSet map[RuntimeOwner]struct{}

type RuntimeOwners map[string]RuntimeOwnerSet

// newWindow creates one detached tmux Window and accepts it only when the
// composite output, global before/after inventory, and exact owner relation all
// identify the same sole new Window and sole new primary Pane.
func (m *Materializer) NewWindow(ctx context.Context, sessionID, name, cwd string, command []string) (WindowCreateResult, error) {
	beforeWindows, beforePanes, beforeErr := m.RuntimeOwners(ctx)
	if beforeErr != nil {
		return WindowCreateResult{}, fmt.Errorf("inventory tmux runtime before window create: %w", beforeErr)
	}
	args := []string{"-d", "-P", "-F", TmuxRowFormat("#{window_id}", "#{pane_id}"), "-t", sessionID + ":"}
	if strings.TrimSpace(name) != "" {
		args = append(args, "-n", name)
	}
	if strings.TrimSpace(cwd) != "" {
		args = append(args, "-c", cwd)
	}
	action := MaterializeMutationAction(MutationCreateWindow,
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
	var observedAfter *[2]RuntimeOwners
	createErr := m.RunMaterializeMutation(ctx, action, func() error {
		currentWindows, currentPanes, err := m.RuntimeOwners(ctx)
		if err != nil {
			return err
		}
		if !EqualRuntimeOwners(beforeWindows, currentWindows) || !EqualRuntimeOwners(beforePanes, currentPanes) {
			return fmt.Errorf("runtime owner inventory drifted before Window create")
		}
		return m.GuardMutationAction(ctx, action)
	}, func() error {
		var err error
		rawOutput, err = RunRuntimeMutationCommand(ctx, m.Runner, action)
		return err
	}, func(ctx context.Context) (bool, error) {
		output := strings.TrimSpace(string(rawOutput))
		if output == "" {
			return false, nil
		}
		afterWindows, afterPanes, err := m.RuntimeOwners(ctx)
		if err != nil {
			observedAfter = nil
			return false, err
		}
		observedAfter = &[2]RuntimeOwners{afterWindows, afterPanes}
		_, err = AttributeCreatedWindow(output, sessionID, beforeWindows, beforePanes, afterWindows, afterPanes)
		return err == nil, err
	})
	output := strings.TrimSpace(string(rawOutput))
	var afterWindows, afterPanes RuntimeOwners
	var inventoryErr error
	if observedAfter != nil {
		afterWindows, afterPanes = observedAfter[0], observedAfter[1]
	} else {
		afterWindows, afterPanes, inventoryErr = m.RuntimeOwners(ctx)
	}
	if inventoryErr != nil {
		m.WarnCompositeWindowResult(output)
		inventoryFailure := fmt.Errorf("inventory tmux runtime after window create: %w", inventoryErr)
		if createErr != nil {
			return WindowCreateResult{}, errors.Join(fmt.Errorf("create tmux window in session %q: %w", sessionID, createErr), inventoryFailure)
		}
		return WindowCreateResult{}, inventoryFailure
	}
	result, attributionErr := AttributeCreatedWindow(output, sessionID, beforeWindows, beforePanes, afterWindows, afterPanes)
	if attributionErr != nil {
		m.WarnUnclaimedOwners("window", beforeWindows, afterWindows)
		m.WarnUnclaimedOwners("pane", beforePanes, afterPanes)
		if createErr != nil {
			return WindowCreateResult{}, errors.Join(fmt.Errorf("create tmux window in session %q: %w", sessionID, createErr), attributionErr)
		}
		return WindowCreateResult{}, attributionErr
	}
	if createErr != nil {
		return result, fmt.Errorf("create tmux window in session %q: %w", sessionID, createErr)
	}
	return result, nil
}

func (m *Materializer) RuntimeOwners(ctx context.Context) (RuntimeOwners, RuntimeOwners, error) {
	windowsOut, err := m.ReadTmux(ctx, "list-windows", "-a", "-F", TmuxRowFormat("#{session_id}", "#{window_id}"))
	if err != nil {
		return nil, nil, err
	}
	panesOut, err := m.ReadTmux(ctx, "list-panes", "-a", "-F", TmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}"))
	if err != nil {
		return nil, nil, err
	}
	windows := RuntimeOwners{}
	windowRows, err := StrictTmuxRows(windowsOut, 2)
	if err != nil {
		return nil, nil, fmt.Errorf("malformed Window owner inventory: %w", err)
	}
	for _, row := range windowRows {
		if ExactTmuxHandle(row[0], "$") == "" || ExactTmuxHandle(row[1], "@") == "" {
			return nil, nil, fmt.Errorf("malformed Window owner inventory")
		}
		owner := RuntimeOwner{SessionID: row[0], WindowID: row[1]}
		if windows[row[1]] == nil {
			windows[row[1]] = RuntimeOwnerSet{}
		}
		windows[row[1]][owner] = struct{}{}
	}
	panes := RuntimeOwners{}
	paneRows, err := StrictTmuxRows(panesOut, 3)
	if err != nil {
		return nil, nil, fmt.Errorf("malformed Pane owner inventory: %w", err)
	}
	for _, row := range paneRows {
		if ExactTmuxHandle(row[0], "$") == "" || ExactTmuxHandle(row[1], "@") == "" || ExactTmuxHandle(row[2], "%") == "" {
			return nil, nil, fmt.Errorf("malformed Pane owner inventory")
		}
		owner := RuntimeOwner{SessionID: row[0], WindowID: row[1]}
		if panes[row[2]] == nil {
			panes[row[2]] = RuntimeOwnerSet{}
		}
		panes[row[2]][owner] = struct{}{}
	}
	return windows, panes, nil
}

func AttributeCreatedWindow(
	output, sessionID string,
	beforeWindows, beforePanes, afterWindows, afterPanes RuntimeOwners,
) (WindowCreateResult, error) {
	rows := SplitTmuxRows(output, 2)
	if len(rows) != 1 || ExactTmuxHandle(rows[0][0], "@") == "" || ExactTmuxHandle(rows[0][1], "%") == "" {
		return WindowCreateResult{}, fmt.Errorf("create tmux window in session %q: malformed composite result %q", sessionID, output)
	}
	result := WindowCreateResult{WindowID: rows[0][0], PaneID: rows[0][1]}
	newWindows := NewRuntimeIDs(beforeWindows, afterWindows)
	newPanes := NewRuntimeIDs(beforePanes, afterPanes)
	windowOwners, windowPresent := afterWindows[result.WindowID]
	paneOwners, panePresent := afterPanes[result.PaneID]
	wantWindowOwner := RuntimeOwner{SessionID: sessionID, WindowID: result.WindowID}
	wantPaneOwner := RuntimeOwner{SessionID: sessionID, WindowID: result.WindowID}
	if len(newWindows) != 1 || newWindows[0] != result.WindowID || len(newPanes) != 1 || newPanes[0] != result.PaneID ||
		!windowPresent || !windowOwners.Contains(wantWindowOwner) || !panePresent || !paneOwners.Contains(wantPaneOwner) ||
		!PaneOwnerSetMatchesWindow(paneOwners, windowOwners, result.WindowID) {
		return WindowCreateResult{}, fmt.Errorf(
			"create tmux window in session %q: attribution mismatch output=%s/%s new-windows=%v new-panes=%v window-owners=%v pane-owners=%v",
			sessionID, result.WindowID, result.PaneID, newWindows, newPanes, SortedRuntimeOwners(windowOwners), SortedRuntimeOwners(paneOwners))
	}
	return result, nil
}

func PaneOwnerSetMatchesWindow(paneOwners, windowOwners RuntimeOwnerSet, windowID string) bool {
	if len(paneOwners) == 0 {
		return false
	}
	for owner := range paneOwners {
		if owner.WindowID != windowID || !windowOwners.Contains(owner) {
			return false
		}
	}
	return true
}

func (owners RuntimeOwnerSet) Contains(owner RuntimeOwner) bool {
	_, ok := owners[owner]
	return ok
}

func SortedRuntimeOwners(owners RuntimeOwnerSet) []string {
	values := make([]string, 0, len(owners))
	for owner := range owners {
		values = append(values, owner.SessionID+"/"+owner.WindowID)
	}
	slices.Sort(values)
	return values
}

func NewRuntimeIDs(before, after RuntimeOwners) []string {
	var ids []string
	for id := range after {
		if _, existed := before[id]; !existed {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

func EqualRuntimeOwners(left, right RuntimeOwners) bool {
	if len(left) != len(right) {
		return false
	}
	for id, leftOwners := range left {
		rightOwners, ok := right[id]
		if !ok || len(leftOwners) != len(rightOwners) {
			return false
		}
		for owner := range leftOwners {
			if !rightOwners.Contains(owner) {
				return false
			}
		}
	}
	return true
}

func (m *Materializer) WarnUnclaimedOwners(kind string, before, after RuntimeOwners) {
	if m.Warn == nil {
		return
	}
	if residual := NewRuntimeIDs(before, after); len(residual) > 0 {
		fmt.Fprintf(m.Warn, "projmux: create attribution failed with unclaimed tmux %s drift; preserved %s; inspect these exact handles before retry or cleanup\n",
			kind, strings.Join(residual, ", "))
	}
}

func (m *Materializer) WarnCompositeWindowResult(output string) {
	rows := SplitTmuxRows(output, 2)
	if len(rows) != 1 {
		return
	}
	if id := ExactTmuxHandle(rows[0][0], "@"); id != "" {
		m.WarnUnclaimedHandle(RuntimeWindow, id)
	}
	if id := ExactTmuxHandle(rows[0][1], "%"); id != "" {
		m.WarnUnclaimedHandle(RuntimePane, id)
	}
}

// retireOwnedPane removes one tmux pane this operation created, guarded by the
// exact Projmux uid it mirrored onto that pane.
//
// It is the same kill-owned action rollback issues, and for the same reason: the
// only pane this Materializer may remove is one it can prove is still the pane
// it made. The guard re-reads the ownership option immediately before the write,
// so a pane that tmux renumbered, that a hook moved, or that something else
// replaced fails the guard instead of being killed. Unlike rollback this runs on
// the success path, where a failed kill must fail the operation rather than be
// warned about -- a Window left holding a shell the create promised to remove is
// not the result the caller was told it would get.
func (m *Materializer) RetireOwnedPane(ctx context.Context, paneID, paneUID string) error {
	action := MaterializeMutationAction(MutationKillOwned,
		m.BoundMutationTarget(string(RuntimePane), paneID, paneUID),
		"same mirrored ownership uid="+paneUID,
		"owned created pane is absent",
		"-t", paneID)
	_, err := m.RunMutation(ctx, action)
	return err
}

// equalizeSplitLayout applies the same scoped, best-effort sizing used by the
// legacy AI split. It intentionally returns no error: layout observation is
// outside the create transaction's failure and rollback contract.
func (m *Materializer) EqualizeSplitLayout(ctx context.Context, anchorPaneID, placement string) {
	if m == nil || m.Runner == nil {
		return
	}
	observed, err := m.ObserveSplitLayoutBatch(ctx, anchorPaneID)
	if err != nil {
		return
	}
	resizes := PlanEvenSplitResizes(anchorPaneID, placement, observed.Geometry())
	steps := make([]RuntimeMutationStep, 0, len(resizes))
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
	var effect SplitLayoutBatchReceipt
	batchTarget := m.BoundMutationTarget("pane", anchorPaneID, observed.PaneUID(anchorPaneID))
	batchTarget.Parent = observed.SessionID + "/" + observed.WindowID
	readEffect := func(ctx context.Context) (SplitLayoutBatchReceipt, error) {
		if !effectCached {
			effect, effectErr = m.ObserveSplitLayoutBatch(ctx, anchorPaneID)
			effectCached = true
		}
		return effect, effectErr
	}
	guardRoute := func(ctx context.Context, action PlannedRuntimeMutation) error {
		if action.Target.Socket != batchTarget.Socket || action.Target.PhysicalSocket != batchTarget.PhysicalSocket ||
			action.Target.RouteAuthority != batchTarget.RouteAuthority {
			return errors.New("split layout action route disagrees with printable batch authority")
		}
		if !routeChecked {
			routeErr = m.RevalidatedTargetRouteGuard(action)(ctx)
			routeChecked = true
		}
		return routeErr
	}
	guardBatch := func(ctx context.Context, action PlannedRuntimeMutation) error {
		if !semanticChecked {
			// Revalidate the same physical generation immediately before the first
			// write, then require the complete ordered identity/geometry receipt to
			// be byte-identical to planning. Unknown or drifted state writes zero.
			semanticErr = m.RevalidatedTargetRouteGuard(action)(ctx)
			if semanticErr == nil {
				var current SplitLayoutBatchReceipt
				current, semanticErr = m.ObserveSplitLayoutBatch(ctx, anchorPaneID)
				if semanticErr == nil && current.Raw != observed.Raw {
					semanticErr = errors.New("split layout inventory drifted before resize")
				}
			}
			semanticChecked = true
		}
		return semanticErr
	}
	for index, resize := range resizes {
		paneUID := observed.PaneUID(resize.PaneID)
		target := m.BoundMutationTarget("pane", resize.PaneID, paneUID)
		target.Parent = observed.SessionID + "/" + observed.WindowID
		action := MaterializeMutationAction(MutationWriteLayout,
			target,
			"exact ordered layout inventory="+observed.Raw,
			"Pane "+resize.PaneID+" size "+resize.Axis+"="+fmt.Sprintf("%d", resize.Size),
			"-t", resize.PaneID, resize.Axis, fmt.Sprintf("%d", resize.Size))
		action.Order = index + 1
		steps = append(steps, RuntimeMutationStep{
			Action:           action,
			TargetRouteGuard: func(ctx context.Context) error { return guardRoute(ctx, action) },
			Reobserve: func(ctx context.Context) (bool, error) {
				current, err := readEffect(ctx)
				if err != nil {
					return false, err
				}
				return current.EffectObserved(action)
			},
			Guard: func(ctx context.Context) error { return guardBatch(ctx, action) },
			Apply: func(ctx context.Context) error {
				// The first Apply separates the pre-effect receipt cache from the
				// single post-effect receipt consumed after every ordered write.
				effectCached = false
				// Preserve the established presentation contract: each peer resize
				// is attempted even when an earlier best-effort resize failed.
				_, _ = RunRuntimeMutationCommand(ctx, m.MutationRunner(action), action)
				return nil
			},
		})
	}
	// Layout remains best effort: a failed or partial resize cannot turn an
	// already successful managed create into a topology rollback. The important
	// boundary is that every attempted write came from one fully guarded plan.
	_ = ExecuteRuntimeMutationPlan(ctx, m.GuardedWriteSteps(steps))
}

var SplitLayoutBatchFormat = TmuxRowFormat(
	"#{session_id}", "#{window_id}", "#{pane_id}", "#{"+tmuxopts.PaneUID+"}",
	"#{pane_left}", "#{pane_top}", "#{pane_width}", "#{pane_height}",
)

type SplitLayoutBatchPane struct {
	Uid      string
	Geometry AiPaneGeometry
}

type SplitLayoutBatchReceipt struct {
	Raw       string
	SessionID string
	WindowID  string
	Panes     map[string]SplitLayoutBatchPane
	Order     []string
}

func (m *Materializer) ObserveSplitLayoutBatch(ctx context.Context, anchorPaneID string) (SplitLayoutBatchReceipt, error) {
	out, err := m.ReadTmux(ctx, "list-panes", "-t", anchorPaneID, "-F", SplitLayoutBatchFormat)
	if err != nil {
		return SplitLayoutBatchReceipt{}, fmt.Errorf("observe split layout receipt: %w", err)
	}
	rows, err := StrictTmuxRows(out, 8)
	if err != nil {
		return SplitLayoutBatchReceipt{}, fmt.Errorf("observe split layout receipt: %w", err)
	}
	receipt := SplitLayoutBatchReceipt{Raw: out, Panes: map[string]SplitLayoutBatchPane{}}
	seenUIDs := map[string]string{}
	for _, row := range rows {
		sessionID, windowID, paneID := ExactTmuxHandle(row[0], "$"), ExactTmuxHandle(row[1], "@"), ExactTmuxHandle(row[2], "%")
		uid := strings.TrimSpace(row[3])
		pane := AiPaneGeometry{
			Id: paneID, Left: ParsePositiveInt(row[4]), Top: ParsePositiveInt(row[5]),
			Width: ParsePositiveInt(row[6]), Height: ParsePositiveInt(row[7]),
		}
		if sessionID == "" || windowID == "" || paneID == "" || uid == "" || pane.Width <= 0 || pane.Height <= 0 {
			return SplitLayoutBatchReceipt{}, errors.New("split layout receipt contains incomplete identity or geometry")
		}
		if receipt.SessionID == "" {
			receipt.SessionID, receipt.WindowID = sessionID, windowID
		}
		if sessionID != receipt.SessionID || windowID != receipt.WindowID {
			return SplitLayoutBatchReceipt{}, errors.New("split layout receipt crosses Window containment")
		}
		if _, duplicate := receipt.Panes[paneID]; duplicate {
			return SplitLayoutBatchReceipt{}, fmt.Errorf("split layout receipt repeats Pane %s", paneID)
		}
		if priorPane := seenUIDs[uid]; priorPane != "" {
			return SplitLayoutBatchReceipt{}, fmt.Errorf("split layout receipt repeats Pane uid %q on %s and %s", uid, priorPane, paneID)
		}
		seenUIDs[uid] = paneID
		receipt.Panes[paneID] = SplitLayoutBatchPane{Uid: uid, Geometry: pane}
		receipt.Order = append(receipt.Order, paneID)
	}
	if _, ok := receipt.Panes[anchorPaneID]; !ok {
		return SplitLayoutBatchReceipt{}, fmt.Errorf("split layout receipt does not contain exact anchor %s", anchorPaneID)
	}
	return receipt, nil
}

func (r SplitLayoutBatchReceipt) PaneUID(paneID string) string {
	return r.Panes[paneID].Uid
}

func (r SplitLayoutBatchReceipt) Geometry() []AiPaneGeometry {
	panes := make([]AiPaneGeometry, 0, len(r.Order))
	for _, paneID := range r.Order {
		panes = append(panes, r.Panes[paneID].Geometry)
	}
	return panes
}

func (r SplitLayoutBatchReceipt) EffectObserved(action PlannedRuntimeMutation) (bool, error) {
	if len(action.Operands) != 4 || action.Operands[0] != "-t" || action.Operands[1] != action.Target.ID {
		return false, errors.New("split layout action operands are incomplete")
	}
	pane, ok := r.Panes[action.Target.ID]
	if !ok {
		return false, fmt.Errorf("split layout target %s disappeared", action.Target.ID)
	}
	if pane.Uid != action.Target.UID || action.Target.Parent != r.SessionID+"/"+r.WindowID {
		return false, errors.New("split layout target identity or containment drifted")
	}
	want, err := strconv.Atoi(action.Operands[3])
	if err != nil || want <= 0 {
		return false, errors.New("split layout action has an invalid desired size")
	}
	switch action.Operands[2] {
	case "-x":
		return pane.Geometry.Width == want, nil
	case "-y":
		return pane.Geometry.Height == want, nil
	default:
		return false, errors.New("split layout action has an invalid axis")
	}
}

// StrictTmuxRows is the identity-boundary variant of SplitTmuxRows. Inventory
// attribution must not silently discard a malformed row and then conclude an
// identity is absent or unique from the incomplete set.
func StrictTmuxRows(output string, want int) ([][]string, error) {
	output = strings.ReplaceAll(output, TmuxRowSepFormat, TmuxRowSep)
	var rows [][]string
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, TmuxRowSep)
		if len(fields) != want {
			return nil, fmt.Errorf("row has %d fields, want %d", len(fields), want)
		}
		rows = append(rows, fields)
	}
	return rows, nil
}
