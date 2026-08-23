package app

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// tmuxError renders a tmux subprocess failure as a plain error.
//
// The cause is deliberately not wrapped with %w. A failed tmux command carries
// an *exec.ExitError, and *exec.ExitError satisfies the `error + ExitCode() int`
// interface cmd/projmux uses to let a command pick its own exit code -- which
// also suppresses main's default stderr print, because a command that chose its
// own code is expected to have printed its own diagnostic. Propagating that
// wrap would turn every tmux failure on this path into a silent exit 1.
func tmuxError(format string, args ...any) error {
	// fmt.Errorf without a %w verb returns a plain error, which is exactly the
	// point: the cause's text is preserved, its identity is not.
	return fmt.Errorf(format, args...)
}

// tmuxCommandRunner is the narrow subprocess seam the materializer shares with
// the resource metadata mirror. Production wires the same ExecRunner into both,
// so a test can replace one object and observe every tmux call the operation
// makes.
type tmuxCommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// sessionMaterializer is the observation half of the persistent runtime. The
// concrete tmux Client also implements persistentSessionLifecycle below: it
// prepares and completes the public hook contract, while this package alone
// plans and executes the managed new-session mutation.
type sessionMaterializer interface {
	SessionExists(ctx context.Context, sessionName string) (bool, error)
	EnsureSession(ctx context.Context, sessionName, cwd string) error
}

type persistentSessionLifecycle interface {
	PreparePersistentSessionCreate(context.Context, string, string, string, map[string]string) (inttmux.PersistentSessionCreateRequest, bool, error)
	CompletePersistentSessionCreate(context.Context, inttmux.PersistentSessionCreateRequest, intmux.NewSessionResult) error
	AbortPersistentSessionCreate()
}

type sessionStartupFinalizer interface {
	FinalizeSessionStartup(ctx context.Context, result intmux.NewSessionResult, sessionName, cwd, operationMarker string) error
}

const finalizeOperationEnvironment = "__projmux_finalize_operation"

func (m *materializer) finalizeSessionStartup(ctx context.Context, result intmux.NewSessionResult, sessionName, cwd string, ledger *runtimeLedger) error {
	if !result.Created {
		return nil
	}
	finalizer, ok := m.sessions.(sessionStartupFinalizer)
	if !ok {
		return errors.New("materialize tmux session: startup finalization is unavailable")
	}
	action := materializeMutationAction(mutationFinalizeSession,
		m.boundMutationTarget("session", result.SessionID, "session:"+sessionName),
		"same create-operation lease="+ledger.operationMarker,
		"startup hook contract is finalized once",
		"-t", result.SessionID, finalizeOperationEnvironment, ledger.operationMarker)
	if err := m.runMaterializeMutation(ctx, action, func() error {
		out, err := m.read(ctx, "show-environment", "-t", result.SessionID)
		if err != nil {
			return err
		}
		if sessionEnvironmentValue(out, createOperationEnvironment) != ledger.operationMarker {
			return errors.New("create-operation lease changed")
		}
		return nil
	}, func() error {
		if err := finalizer.FinalizeSessionStartup(ctx, result, sessionName, cwd, ledger.operationMarker); err != nil {
			return err
		}
		_, err := runRuntimeMutationCommand(ctx, m.mutationRunner(action), action)
		return err
	}, func(ctx context.Context) (bool, error) {
		out, err := m.read(ctx, "list-panes", "-s", "-t", result.SessionID, "-F",
			tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}",
				"#{E:"+createOperationEnvironment+"}", "#{E:"+finalizeOperationEnvironment+"}"))
		if err != nil {
			return false, err
		}
		rows := splitTmuxRows(out, 5)
		matched := 0
		for _, row := range rows {
			if row[0] == result.SessionID && row[1] == result.WindowID && row[2] == result.PaneID &&
				row[3] == ledger.operationMarker && row[4] == ledger.operationMarker {
				matched++
			}
		}
		return matched == 1, nil
	}); err != nil {
		return tmuxError("finalize tmux session %q startup: %v", sessionName, err)
	}
	return nil
}

type liveSessionIdentity struct {
	ID   string
	Name string
	UID  string
	Root string
}

// runtimeObjectKind names one class of tmux object an operation can create.
type runtimeObjectKind string

const (
	runtimeSession runtimeObjectKind = "session"
	runtimeWindow  runtimeObjectKind = "window"
	runtimePane    runtimeObjectKind = "pane"
)

// runtimeObject is one ledger entry: a tmux object this operation created,
// pinned by its stable tmux id and by the Projmux uid mirrored onto it.
type runtimeObject struct {
	Kind runtimeObjectKind
	// ID is the stable tmux handle ($N, @N, %N). Indexes are deliberately not
	// used: they shift when a sibling is created or destroyed.
	ID string
	// UID is the Projmux uid mirrored onto ID at creation time.
	UID string
}

// runtimeLedger records the tmux objects one operation created so a later
// failure can undo exactly them.
//
// Rollback is ownership checked, not best effort: an entry is only removed when
// the tmux object still carries the same Projmux uid this operation mirrored
// onto it. A pre-existing session, a window another operation created, and an
// id that tmux has since recycled are all left alone.
type runtimeLedger struct {
	created          []runtimeObject
	operationMarker  string
	markedSessionIDs []string
}

func newRuntimeLedger(operationID string) *runtimeLedger {
	return &runtimeLedger{operationMarker: newCreateOperationMarker(operationID)}
}

func (l *runtimeLedger) record(kind runtimeObjectKind, id, uid string) {
	if l == nil || strings.TrimSpace(id) == "" {
		return
	}
	l.created = append(l.created, runtimeObject{Kind: kind, ID: id, UID: uid})
}

// entries returns the ledger in creation order.
func (l *runtimeLedger) entries() []runtimeObject {
	if l == nil {
		return nil
	}
	return l.created
}

func (l *runtimeLedger) markSession(session string) {
	if l == nil || strings.TrimSpace(session) == "" {
		return
	}
	if slices.Contains(l.markedSessionIDs, session) {
		return
	}
	l.markedSessionIDs = append(l.markedSessionIDs, session)
}

// ownershipOption is the mirrored option that proves this operation owns a
// runtime object.
func (o runtimeObject) ownershipOption() string {
	switch o.Kind {
	case runtimeSession:
		return tmuxopts.ProjectUIDSession
	case runtimeWindow:
		return tmuxopts.WindowUID
	default:
		return tmuxopts.PaneUID
	}
}

// materializer turns offline Project/Window/Pane metadata into detached tmux
// objects.
//
// Every command it issues is detached. It never calls switch-client,
// attach-session, select-window, or select-pane, and it never reads which
// client or pane is focused, so the operator's view is byte-identical before
// and after a create.
type materializer struct {
	runner   tmuxCommandRunner
	mirror   intmetadata.Mirror
	sessions sessionMaterializer
	// target is the immutable logical route shared by runner, mirror, and
	// sessions. Every printable action carries it and every write reobserves
	// the server's #{socket_path} through that same route first.
	target explicitTmuxTarget
	// expectedSocketPath is the exact invocation identity resolved before the
	// plan. It closes the gap where an -L name could be retargeted between
	// planning and execution.
	expectedSocketPath string
	// warn receives non-fatal rollback diagnostics. Progress and warnings are
	// stderr-only; stdout stays empty until the operation succeeds.
	warn io.Writer
	// executable resolves this build's own binary for the managed process
	// supervisor a launched pane execs. Nil means os.Executable.
	executable func() (string, error)
	// lookupEnv is the environment probe used only as the last fallback when
	// the exact server reports no default-shell. Nil means os.Getenv.
	lookupEnv func(string) string
	// configPath optionally pins the generated app tmux config in tests and
	// embedded callers. Production resolves the same internal XDG path lazily.
	configPath     string
	socketName     string
	routeAuthority *runtimeMutationRouteAuthority
}

func (m *materializer) read(ctx context.Context, args ...string) (string, error) {
	out, err := m.routedRunner().Run(ctx, "tmux", args...)
	return strings.TrimSpace(string(out)), err
}

func (m *materializer) routedRunner() tmuxCommandRunner {
	if m.expectedSocketPath != "" {
		return m.runnerAtPhysicalSocket(m.expectedSocketPath)
	}
	return m.runner
}

func (m *materializer) exactMutationRoute() (explicitTmuxTarget, string, error) {
	target := m.target
	if target.flag == "" || target.value == "" {
		switch runner := m.runner.(type) {
		case explicitTmuxRunner:
			target = runner.target
		case *explicitTmuxRunner:
			target = runner.target
		}
	}
	switch target.flag {
	case "-L":
		if strings.TrimSpace(target.value) == "" {
			break
		}
		return target, "-L=" + target.value, nil
	case "-S":
		if filepath.IsAbs(target.value) {
			target.value = filepath.Clean(target.value)
			return target, "-S=" + target.value, nil
		}
	}
	return explicitTmuxTarget{}, "", errors.New("materializer requires an exact -L socket name or absolute -S socket path")
}

func (m *materializer) boundMutationTarget(kind, id, uid string) runtimeMutationTarget {
	_, route, _ := m.exactMutationRoute()
	target := runtimeMutationTarget{Socket: route, PhysicalSocket: printableRuntimeMutationSocket(m.expectedSocketPath), Kind: kind, ID: id, UID: uid}
	if m.routeAuthority != nil {
		target.RouteAuthority = m.routeAuthority.printable()
	}
	return target
}

// mutationRunner returns the execution authority printed by one action.  The
// only action allowed to execute before a physical socket exists is the
// create-session declaration; every later action is transported through the
// exact absolute -S identity captured by the first post-create observation.
func (m *materializer) mutationRunner(action plannedRuntimeMutation) tmuxCommandRunner {
	if filepath.IsAbs(action.Target.PhysicalSocket) {
		return m.runnerAtPhysicalSocket(action.Target.PhysicalSocket)
	}
	return m.runner
}

func (m *materializer) targetRouteGuard(action plannedRuntimeMutation) func(context.Context) error {
	return func(ctx context.Context) error {
		target, _, err := m.exactMutationRoute()
		if err != nil {
			return err
		}
		if action.Target.PhysicalSocket == runtimeMutationSocketAbsentBeforeCreate {
			if m.expectedSocketPath != "" || m.routeAuthority != nil ||
				action.Target.Socket != target.flag+"="+target.value || action.Target.RouteAuthority != "" {
				return errors.New("materializer absent-before-create authority disagrees with printable target")
			}
			probe := m.runnerAtTarget(target)
			if _, err := probe.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}"); err != nil {
				if inttmux.IsNoServerFailure(err) {
					return nil
				}
				return fmt.Errorf("materializer absent-before-create authority is unknown: %w", err)
			}
			return errors.New("materializer absent-before-create server appeared after planning")
		}
		wantSocket := target.flag + "=" + target.value
		if action.Verb == mutationWriteRouteMarker {
			wantSocket = "-S=" + m.expectedSocketPath
		}
		if m.routeAuthority == nil || action.Target.PhysicalSocket != m.expectedSocketPath ||
			action.Target.RouteAuthority != m.routeAuthority.printable() || action.Target.Socket != wantSocket {
			return errors.New("materializer printable route authority disagrees with captured server generation")
		}
		if action.Verb == mutationWriteRouteMarker || action.Verb == mutationKillOwned {
			probe := m.runnerAtPhysicalSocket(m.expectedSocketPath)
			pathOut, err := probe.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
			if err != nil || strings.TrimSpace(string(pathOut)) != m.expectedSocketPath {
				return errors.New("materializer printed physical socket drifted")
			}
			pidOut, err := probe.Run(ctx, "tmux", "display-message", "-p", "-F", "#{pid}")
			if err != nil || strings.TrimSpace(string(pidOut)) != m.routeAuthority.ServerPID {
				return errors.New("materializer printed server generation drifted")
			}
			if action.Verb == mutationWriteRouteMarker {
				owned, err := probe.Run(ctx, "tmux", "show-options", "-gqv", tmuxopts.AppGlobal)
				if err != nil || strings.TrimSpace(string(owned)) != "1" {
					return errors.New("materializer route-marker target is not app-owned")
				}
			}
			return nil
		}
		route := runtimeMutationRoute{
			target: target, expectedSocketPath: m.expectedSocketPath,
			socketName: m.logicalSocketName(target), authority: m.routeAuthority,
		}
		return guardPrintedRuntimeMutationRoute(ctx, m.baseRunner(), route, action)
	}
}

func (m *materializer) runnerAtPhysicalSocket(path string) tmuxCommandRunner {
	return m.runnerAtTarget(explicitTmuxTarget{flag: "-S", value: filepath.Clean(path)})
}

func (m *materializer) runnerAtTarget(target explicitTmuxTarget) tmuxCommandRunner {
	return explicitTmuxRunner{runner: m.baseRunner(), target: target}
}

func (m *materializer) baseRunner() tmuxCommandRunner {
	base := m.runner
	for {
		switch routed := base.(type) {
		case explicitTmuxRunner:
			base = routed.runner
			continue
		case *explicitTmuxRunner:
			base = routed.runner
			continue
		}
		break
	}
	return base
}

func (m *materializer) guardExactRoute(ctx context.Context, allowNoServer bool, plannedPhysical ...string) error {
	return m.guardExactRouteOwnership(ctx, allowNoServer, true, plannedPhysical...)
}

func (m *materializer) guardExactAppRoute(ctx context.Context, allowNoServer bool, plannedPhysical ...string) error {
	return m.guardExactRouteOwnership(ctx, allowNoServer, false, plannedPhysical...)
}

func (m *materializer) guardExactRouteOwnership(ctx context.Context, allowNoServer, requireLogical bool, plannedPhysical ...string) error {
	if len(plannedPhysical) == 1 {
		planned := strings.TrimSpace(plannedPhysical[0])
		if planned == runtimeMutationSocketAbsentBeforeCreate {
			if !allowNoServer || m.expectedSocketPath != "" {
				return errors.New("printed absent-before-create socket disagrees with bound materializer route")
			}
		} else if filepath.Clean(planned) != filepath.Clean(m.expectedSocketPath) {
			return fmt.Errorf("printed physical socket %q disagrees with materializer route %q", planned, m.expectedSocketPath)
		}
	}
	target, route, err := m.exactMutationRoute()
	if err != nil {
		return err
	}
	probe := m.runner
	if m.expectedSocketPath != "" {
		probe = m.mutationRunner(plannedRuntimeMutation{Target: runtimeMutationTarget{PhysicalSocket: m.expectedSocketPath}})
	}
	observedOut, observeErr := probe.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	if observeErr != nil {
		if allowNoServer && m.expectedSocketPath == "" && m.routeAuthority == nil && inttmux.IsNoServerFailure(observeErr) {
			return nil
		}
		return fmt.Errorf("reobserve exact tmux route %s: %w", route, observeErr)
	}
	if len(plannedPhysical) == 1 && strings.TrimSpace(plannedPhysical[0]) == runtimeMutationSocketAbsentBeforeCreate {
		return errors.New("materializer absent-before-create server appeared after planning")
	}
	observed := strings.TrimSpace(string(observedOut))
	if !filepath.IsAbs(observed) || filepath.Clean(observed) != observed {
		return fmt.Errorf("reobserve exact tmux route %s: socket path %q is not absolute and clean", route, observed)
	}
	if m.expectedSocketPath == "" {
		// A create-session plan may begin with no server. The first successful
		// observation after creation binds the immutable physical identity; no
		// -L basename inference participates.
		m.expectedSocketPath = observed
	}
	if m.expectedSocketPath != "" && observed != filepath.Clean(m.expectedSocketPath) {
		return fmt.Errorf("tmux socket drifted: observed %q, planned invocation %q", observed, filepath.Clean(m.expectedSocketPath))
	}
	// Once the logical declaration has resolved to a physical server, every
	// ownership read must use that exact physical authority. Reusing the -L
	// probe here would permit an alias replacement between socket observation
	// and the app/logical marker reads.
	probe = m.runnerAtPhysicalSocket(m.expectedSocketPath)
	if m.routeAuthority == nil && requireLogical {
		bound, err := resolveExistingRuntimeMutationRoute(ctx, m.baseRunner(), target, nil)
		if err != nil {
			return err
		}
		if bound.expectedSocketPath != m.expectedSocketPath || bound.authority == nil {
			return errors.New("materializer exact route has no server-generation authority")
		}
		m.target = bound.target
		target = bound.target
		m.socketName = bound.socketName
		m.routeAuthority = bound.authority
	}
	if m.routeAuthority != nil {
		if m.routeAuthority.Class == runtimeMutationRouteApp && !requireLogical {
			pidOut, err := probe.Run(ctx, "tmux", "display-message", "-p", "-F", "#{pid}")
			if err != nil || strings.TrimSpace(string(pidOut)) != m.routeAuthority.ServerPID {
				return errors.New("runtime mutation route: exact app server generation drifted")
			}
			owned, err := probe.Run(ctx, "tmux", "show-options", "-gqv", tmuxopts.AppGlobal)
			if err != nil || strings.TrimSpace(string(owned)) != "1" {
				return errors.New("runtime mutation route: exact server is not app-owned")
			}
			return nil
		}
		return guardResolvedRuntimeMutationRoute(ctx, m.baseRunner(), runtimeMutationRoute{
			target: target, expectedSocketPath: m.expectedSocketPath,
			socketName: m.logicalSocketName(target), authority: m.routeAuthority,
		})
	}
	if requireLogical {
		if err := guardRuntimeMutationServerOwnership(ctx, probe, target); err != nil {
			return err
		}
	} else {
		owned, err := probe.Run(ctx, "tmux", "show-options", "-gqv", tmuxopts.AppGlobal)
		if err != nil {
			return fmt.Errorf("runtime mutation route: read app ownership marker: %w", err)
		}
		if strings.TrimSpace(string(owned)) != "1" {
			return errors.New("runtime mutation route: exact server is not app-owned")
		}
	}
	switch target.flag {
	case "-S":
		if observed != target.value {
			return fmt.Errorf("tmux socket drifted: observed %q, planned %q", observed, target.value)
		}
	case "-L":
		// The logical route is proved by expectedSocketPath, which was resolved
		// independently or bound at the first post-create observation.
	}
	return nil
}

func (m *materializer) logicalSocketName(target explicitTmuxTarget) string {
	if strings.TrimSpace(m.socketName) != "" {
		return strings.TrimSpace(m.socketName)
	}
	if target.flag == "-L" {
		return target.value
	}
	return ""
}

func (m *materializer) generatedAppConfigPath() (string, error) {
	path := strings.TrimSpace(m.configPath)
	if path == "" {
		paths, err := configPaths(os.UserHomeDir, m.lookupEnv)
		if err != nil {
			return "", fmt.Errorf("resolve generated app tmux config: %w", err)
		}
		path = filepath.Join(paths.ConfigDir, "tmux.conf")
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

func materializeMutationAction(kind runtimeMutationVerb, target runtimeMutationTarget, guard, _ string, args ...string) plannedRuntimeMutation {
	action := newRuntimeMutation(1, kind, target)
	bindRuntimeMutationGuard(&action, guard)
	action.Operands = slices.Clone(args)
	return action
}

func (m *materializer) runMutation(ctx context.Context, action plannedRuntimeMutation) ([]byte, error) {
	var output []byte
	err := executeRuntimeMutationPlan(ctx, []runtimeMutationStep{{
		Action:           action,
		TargetRouteGuard: m.targetRouteGuard(action),
		Reobserve:        func(ctx context.Context) (bool, error) { return m.observeMutationEffect(ctx, action) },
		Guard: func(ctx context.Context) error {
			if err := m.guardExactRoute(ctx, false, action.Target.PhysicalSocket); err != nil {
				return err
			}
			return m.guardMutationAction(ctx, action)
		},
		Apply: func(ctx context.Context) error {
			var err error
			output, err = runRuntimeMutationCommand(ctx, m.mutationRunner(action), action)
			return err
		},
	}})
	return output, err
}

func (m *materializer) runMaterializeMutation(ctx context.Context, action plannedRuntimeMutation, guard, execute func() error, observer ...func(context.Context) (bool, error)) error {
	return executeRuntimeMutationPlan(ctx, []runtimeMutationStep{{
		Action:           action,
		TargetRouteGuard: m.targetRouteGuard(action),
		Reobserve: func(ctx context.Context) (bool, error) {
			observed, supported, err := m.observeMaterializeMutationEffect(ctx, action)
			if err != nil {
				return observed, err
			}
			if supported {
				if !observed {
					return false, nil
				}
				// Receipt-bearing effects need their call-site invariant as part
				// of Reobserve. Otherwise a matching marker/name/value can skip
				// Guard on a recycled target or mismatched create receipt.
				if materializeEffectRequiresInvariant(action.Verb) {
					if len(observer) != 1 || observer[0] == nil {
						return false, errors.New("materialize mutation receipt observer is unavailable")
					}
					return observer[0](ctx)
				}
				return true, nil
			}
			if len(observer) != 1 || observer[0] == nil {
				return false, fmt.Errorf("materializer has no live effect observer for %q", action.Verb)
			}
			return observer[0](ctx)
		},
		Guard: func(context.Context) error {
			if err := m.guardExactRoute(ctx, action.Verb == mutationCreateSession, action.Target.PhysicalSocket); err != nil {
				return err
			}
			if guard == nil {
				return errors.New("materialize mutation guard is unavailable")
			}
			return guard()
		},
		Apply: func(context.Context) error {
			return execute()
		},
	}})
}

func materializeEffectRequiresInvariant(verb runtimeMutationVerb) bool {
	switch verb {
	case mutationCreateSession, mutationFinalizeSession, mutationWriteProjectAnchor, mutationRenameWindow:
		return true
	default:
		return false
	}
}

func (m *materializer) observeMutationEffect(ctx context.Context, action plannedRuntimeMutation) (bool, error) {
	observed, supported, err := m.observeMaterializeMutationEffect(ctx, action)
	if err != nil {
		return false, err
	}
	if !supported {
		return false, fmt.Errorf("materializer has no effect observer for %q", action.Verb)
	}
	return observed, nil
}

func (m *materializer) observeMaterializeMutationEffect(ctx context.Context, action plannedRuntimeMutation) (bool, bool, error) {
	if action.Target.PhysicalSocket != runtimeMutationSocketAbsentBeforeCreate {
		if err := m.guardExactRoute(ctx, false, action.Target.PhysicalSocket); err != nil {
			return false, true, err
		}
	}
	switch action.Verb {
	case mutationCreateSession:
		exists, err := m.sessions.SessionExists(ctx, action.Target.ID)
		if err != nil || !exists {
			return false, true, err
		}
		// The create declaration may print absent-before-create, but its first
		// successful post-effect observation must bind and prove the physical
		// app-owned socket before any follow-up plan row can be constructed.
		if err := m.guardExactAppRoute(ctx, false); err != nil {
			return false, true, err
		}
		marker := ""
		for i := 0; i+1 < len(action.Operands); i++ {
			if action.Operands[i] == "-e" && strings.HasPrefix(action.Operands[i+1], createOperationEnvironment+"=") {
				marker = strings.TrimPrefix(action.Operands[i+1], createOperationEnvironment+"=")
			}
		}
		out, err := m.mutationRunner(plannedRuntimeMutation{Target: m.boundMutationTarget("session", action.Target.ID, action.Target.UID)}).Run(ctx, "tmux", "show-environment", "-t", action.Target.ID)
		return err == nil && marker != "" && sessionEnvironmentValue(string(out), createOperationEnvironment) == marker, true, err
	case mutationFinalizeSession:
		out, err := m.routedRunner().Run(ctx, "tmux", "show-environment", "-t", action.Target.ID)
		if err != nil {
			return false, true, err
		}
		want := ""
		if len(action.Operands) > 0 {
			want = action.Operands[len(action.Operands)-1]
		}
		return want != "" && sessionEnvironmentValue(string(out), finalizeOperationEnvironment) == want, true, nil
	case mutationWriteIdentity, mutationWriteProjectAnchor, mutationTombstonePane, mutationRestorePane:
		option, want, unset, err := runtimeMutationOptionEffect(action)
		if err != nil {
			return false, true, err
		}
		got, err := m.readMutationEffectOption(ctx, action.Target.ID, option)
		if err != nil {
			return false, true, err
		}
		if action.Verb == mutationWriteIdentity {
			matches, err := m.observeMaterializeIdentityTarget(ctx, action.Target)
			if err != nil {
				return false, true, err
			}
			if !matches {
				return false, true, nil
			}
		}
		if unset {
			if got != "" {
				return false, true, nil
			}
			return got == "", true, nil
		}
		return materializeOptionEffectObserved(option, want, got), true, nil
	case mutationRenameWindow:
		if len(action.Operands) == 0 {
			return false, true, errors.New("rename effect operands are incomplete")
		}
		matches, err := m.observeMaterializeIdentityTarget(ctx, action.Target)
		if err != nil {
			return false, true, err
		}
		if !matches {
			return false, true, nil
		}
		got, err := m.readMutationEffectOption(ctx, action.Target.ID, "window_name")
		return got == action.Operands[len(action.Operands)-1], true, err
	case mutationWriteLease:
		if len(action.Operands) < 2 {
			return false, true, errors.New("lease effect operands are incomplete")
		}
		out, err := m.routedRunner().Run(ctx, "tmux", "show-environment", "-t", action.Target.ID)
		return err == nil && sessionEnvironmentValue(string(out), action.Operands[len(action.Operands)-2]) == action.Operands[len(action.Operands)-1], true, err
	case mutationClearLease:
		out, err := m.routedRunner().Run(ctx, "tmux", "show-environment", "-t", action.Target.ID)
		if err != nil {
			return false, true, err
		}
		if len(action.Operands) == 0 {
			return false, true, errors.New("clear-lease effect operands are incomplete")
		}
		return sessionEnvironmentValue(string(out), action.Operands[len(action.Operands)-1]) == "", true, nil
	case mutationWriteLayout:
		if len(action.Operands) < 4 {
			return false, true, errors.New("layout effect operands are incomplete")
		}
		format := "#{pane_width}"
		if action.Operands[len(action.Operands)-2] == "-y" {
			format = "#{pane_height}"
		}
		got, err := m.read(ctx, "display-message", "-p", "-t", action.Target.ID, "-F", format)
		if err != nil {
			return false, true, fmt.Errorf("reobserve mutation effect %s on %s: %w", format, action.Target.ID, err)
		}
		return got == action.Operands[len(action.Operands)-1], true, nil
	case mutationKillOwned:
		command, format := "list-sessions", "#{session_id}"
		switch action.Target.Kind {
		case "window":
			command, format = "list-windows", "#{window_id}"
		case "pane":
			command, format = "list-panes", "#{pane_id}"
		case "session":
		default:
			return false, true, errors.New("owned kill effect has an unknown target kind")
		}
		out, err := m.routedRunner().Run(ctx, "tmux", command, "-a", "-F", format)
		if err != nil {
			if inttmux.IsNoServerFailure(err) {
				return true, true, nil
			}
			return false, true, err
		}
		for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
			if strings.TrimSpace(line) == action.Target.ID {
				return false, true, nil
			}
		}
		return true, true, nil
	}
	return false, false, nil
}

func materializeOptionEffectObserved(option, want, got string) bool {
	if option != tmuxopts.AutomaticRenameWindow {
		return got == want
	}
	wantValue, wantOK := exactTmuxBoolean(want)
	gotValue, gotOK := exactTmuxBoolean(got)
	return wantOK && gotOK && wantValue == gotValue
}

func exactTmuxBoolean(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "on", "yes", "true":
		return true, true
	case "0", "off", "no", "false":
		return false, true
	default:
		return false, false
	}
}

func runtimeMutationOptionEffect(action plannedRuntimeMutation) (option, want string, unset bool, err error) {
	if slices.Contains(action.Operands, "-u") {
		if len(action.Operands) < 1 {
			return "", "", false, errors.New("identity unset effect operands are incomplete")
		}
		option = action.Operands[len(action.Operands)-1]
		if strings.TrimSpace(option) == "" || strings.HasPrefix(option, "-") {
			return "", "", false, errors.New("identity unset effect has no exact option")
		}
		return option, "", true, nil
	}
	if len(action.Operands) < 2 {
		return "", "", false, errors.New("identity effect operands are incomplete")
	}
	option, want = action.Operands[len(action.Operands)-2], action.Operands[len(action.Operands)-1]
	if strings.TrimSpace(option) == "" || strings.HasPrefix(option, "-") {
		return "", "", false, errors.New("identity effect has no exact option")
	}
	return option, want, false, nil
}

// readMutationEffectOption is the strict observation half of the plan seam.
// Unlike option, it never maps an unreadable target to an absent option: an
// observation error is unknown authority and executeRuntimeMutationPlan must
// refuse before the first write.
func (m *materializer) readMutationEffectOption(ctx context.Context, target, option string) (string, error) {
	got, err := m.read(ctx, "display-message", "-p", "-t", target, "-F", "#{"+option+"}")
	if err != nil {
		return "", fmt.Errorf("reobserve mutation effect option %s on %s: %w", option, target, err)
	}
	return got, nil
}

func materializeIdentityOwnershipOption(kind string) (string, error) {
	switch runtimeObjectKind(kind) {
	case runtimeSession:
		return tmuxopts.ProjectUIDSession, nil
	case runtimeWindow:
		return tmuxopts.WindowUID, nil
	case runtimePane:
		return tmuxopts.PaneUID, nil
	default:
		return "", fmt.Errorf("identity effect has unsupported target kind %q", kind)
	}
}

type materializeIdentityObservation struct {
	uid    string
	parent string
}

func (m *materializer) readMaterializeIdentityObservation(ctx context.Context, kind, target string) (materializeIdentityObservation, error) {
	ownershipOption, err := materializeIdentityOwnershipOption(kind)
	if err != nil {
		return materializeIdentityObservation{}, err
	}
	var format string
	switch runtimeObjectKind(kind) {
	case runtimeSession:
		format = tmuxRowFormat("#{session_id}", "#{"+ownershipOption+"}", "#{"+tmuxopts.SessionRole+"}")
	case runtimeWindow:
		format = tmuxRowFormat("#{session_id}", "#{window_id}", "#{"+ownershipOption+"}",
			"#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.SessionRole+"}")
	case runtimePane:
		format = tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{"+ownershipOption+"}",
			"#{"+tmuxopts.WindowUID+"}", "#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.SessionRole+"}")
	}
	out, err := m.read(ctx, "display-message", "-p", "-t", target, "-F", format)
	if err != nil {
		return materializeIdentityObservation{}, fmt.Errorf("observe exact %s identity target %s: %w", kind, target, err)
	}
	switch runtimeObjectKind(kind) {
	case runtimeSession:
		rows := splitTmuxRows(out, 3)
		if len(rows) != 1 || rows[0][0] != target || exactTmuxHandle(rows[0][0], "$") == "" {
			return materializeIdentityObservation{}, errors.New("session identity target containment is unavailable")
		}
		return materializeIdentityObservation{uid: rows[0][1], parent: "session-role=" + rows[0][2]}, nil
	case runtimeWindow:
		rows := splitTmuxRows(out, 5)
		if len(rows) != 1 || exactTmuxHandle(rows[0][0], "$") == "" || rows[0][1] != target || exactTmuxHandle(rows[0][1], "@") == "" {
			return materializeIdentityObservation{}, errors.New("window identity target containment is unavailable")
		}
		return materializeIdentityObservation{uid: rows[0][2], parent: rows[0][0] + "/root=" + rows[0][3] + "/role=" + rows[0][4]}, nil
	case runtimePane:
		rows := splitTmuxRows(out, 7)
		if len(rows) != 1 || exactTmuxHandle(rows[0][0], "$") == "" || exactTmuxHandle(rows[0][1], "@") == "" ||
			rows[0][2] != target || exactTmuxHandle(rows[0][2], "%") == "" || strings.TrimSpace(rows[0][4]) == "" {
			return materializeIdentityObservation{}, errors.New("pane identity target containment is unavailable")
		}
		return materializeIdentityObservation{uid: rows[0][3], parent: rows[0][0] + "/" + rows[0][1] +
			"/window-uid=" + rows[0][4] + "/root=" + rows[0][5] + "/role=" + rows[0][6]}, nil
	default:
		return materializeIdentityObservation{}, fmt.Errorf("identity effect has unsupported target kind %q", kind)
	}
}

func (m *materializer) bindMaterializeIdentityTarget(ctx context.Context, kind, id, uid string) (runtimeMutationTarget, error) {
	observation, err := m.readMaterializeIdentityObservation(ctx, kind, id)
	if err != nil {
		return runtimeMutationTarget{}, err
	}
	if observation.uid != "" && observation.uid != uid {
		return runtimeMutationTarget{}, fmt.Errorf("%s %s carries foreign uid %q, want %q", kind, id, observation.uid, uid)
	}
	target := m.boundMutationTarget(kind, id, uid)
	target.Parent = observation.parent
	return target, nil
}

func (m *materializer) observeMaterializeIdentityTarget(ctx context.Context, target runtimeMutationTarget) (bool, error) {
	observation, err := m.readMaterializeIdentityObservation(ctx, target.Kind, target.ID)
	if err != nil {
		return false, err
	}
	if observation.parent != target.Parent {
		return false, fmt.Errorf("%s %s identity parent drifted: observed %q, planned %q", target.Kind, target.ID, observation.parent, target.Parent)
	}
	if observation.uid == "" {
		return false, nil
	}
	if observation.uid != target.UID {
		return false, fmt.Errorf("%s %s carries foreign uid %q, want %q", target.Kind, target.ID, observation.uid, target.UID)
	}
	return true, nil
}

func (m *materializer) guardMaterializeIdentityTarget(ctx context.Context, target runtimeMutationTarget, allowBlankUID bool) error {
	observation, err := m.readMaterializeIdentityObservation(ctx, target.Kind, target.ID)
	if err != nil {
		return err
	}
	if observation.parent != target.Parent {
		return fmt.Errorf("%s %s identity parent drifted: observed %q, planned %q", target.Kind, target.ID, observation.parent, target.Parent)
	}
	if observation.uid == "" && allowBlankUID {
		return nil
	}
	if observation.uid != target.UID {
		return fmt.Errorf("%s uid is %q, want %q", target.Kind, observation.uid, target.UID)
	}
	return nil
}

func (m *materializer) guardMutationAction(ctx context.Context, action plannedRuntimeMutation) error {
	switch action.Verb {
	case mutationKillOwned:
		entry := runtimeObject{Kind: runtimeObjectKind(action.Target.Kind), ID: action.Target.ID, UID: action.Target.UID}
		got, err := m.read(ctx, "display-message", "-p", "-t", entry.ID, "-F", "#{"+entry.ownershipOption()+"}")
		if err != nil {
			return err
		}
		if got != entry.UID {
			return fmt.Errorf("ownership uid is %q, want %q", got, entry.UID)
		}
	case mutationWriteIdentity:
		return m.guardMaterializeIdentityTarget(ctx, action.Target, true)
	case mutationCreateWindow:
		got, err := m.read(ctx, "display-message", "-p", "-t", action.Target.ID, "-F", "#{session_id}")
		if err != nil {
			return err
		}
		if exactTmuxHandle(got, "$") == "" {
			return fmt.Errorf("session target %q is no longer exact", action.Target.ID)
		}
	case mutationWriteLease:
		got, err := m.read(ctx, "display-message", "-p", "-t", action.Target.ID, "-F", "#{session_id}")
		if err != nil {
			return err
		}
		if got != action.Target.ID {
			return fmt.Errorf("session identity drifted: got %q, want id=%s", got, action.Target.ID)
		}
	case mutationCreatePane, mutationWriteLayout:
		got, err := m.read(ctx, "display-message", "-p", "-t", action.Target.ID, "-F", "#{pane_id}")
		if err != nil {
			return err
		}
		if got != action.Target.ID {
			return fmt.Errorf("anchor Pane is %q, want %q", got, action.Target.ID)
		}
	default:
		return fmt.Errorf("materialize mutation %q has no executable guard", action.Verb)
	}
	return nil
}

// option reads one tmux format value for a target, treating an unreadable
// target as an empty value rather than an error. Ownership checks want "this is
// not ours" for a target that has already disappeared.
func (m *materializer) option(ctx context.Context, target, format string) string {
	out, err := m.read(ctx, "display-message", "-p", "-t", target, "-F", format)
	if err != nil {
		return ""
	}
	return out
}

// rollback removes, in reverse creation order, only the tmux objects this
// operation created that still carry the uid it mirrored onto them.
func (m *materializer) rollback(ctx context.Context, ledger *runtimeLedger) {
	entries := ledger.entries()
	var steps []runtimeMutationStep
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		got, err := m.read(ctx, "display-message", "-p", "-t", entry.ID, "-F", "#{"+entry.ownershipOption()+"}")
		if err != nil {
			// The object is already gone, so the desired rollback state holds.
			continue
		}
		if got != entry.UID {
			// The object still exists but no longer belongs to this operation.
			// Preserve it and make the residual drift explicit.
			if m.warn != nil {
				fmt.Fprintf(m.warn, "projmux: rollback preserved %s %s because its ownership uid is %q, want %q\n",
					entry.Kind, entry.ID, got, entry.UID)
			}
			continue
		}
		action := materializeMutationAction(mutationKillOwned,
			m.boundMutationTarget(string(entry.Kind), entry.ID, entry.UID),
			"same mirrored ownership uid="+entry.UID,
			"owned created "+string(entry.Kind)+" is absent",
			"-t", entry.ID)
		action.Order = len(steps) + 1
		steps = append(steps, runtimeMutationStep{
			Action:           action,
			TargetRouteGuard: m.targetRouteGuard(action),
			Reobserve:        func(ctx context.Context) (bool, error) { return m.observeMutationEffect(ctx, action) },
			Guard: func(ctx context.Context) error {
				if err := m.guardExactRoute(ctx, false, action.Target.PhysicalSocket); err != nil {
					return err
				}
				got, err := m.read(ctx, "display-message", "-p", "-t", entry.ID, "-F", "#{"+entry.ownershipOption()+"}")
				if err != nil {
					return err
				}
				if got != entry.UID {
					return fmt.Errorf("ownership uid is %q, want %q", got, entry.UID)
				}
				return nil
			},
			Apply: func(ctx context.Context) error {
				_, err := runRuntimeMutationCommand(ctx, m.runner, action)
				return err
			},
		})
	}
	if err := executeRuntimeMutationPlan(ctx, steps); err != nil && m.warn != nil {
		fmt.Fprintf(m.warn, "projmux: rollback stopped before an unguarded runtime write: %v\n", err)
	}
}

// ensureSession makes the Project's persistent tmux session live.
//
// A session that already exists is reused untouched, which is what keeps the
// pre-create/post-create hooks on their documented trigger: they fire when a
// session is created, and only then.
func (m *materializer) ensureSession(
	ctx context.Context,
	project coremetadata.Project,
	sessionName string,
	ledger *runtimeLedger,
) (intmux.NewSessionResult, error) {
	return m.ensureSessionAt(ctx, project, sessionName, project.Spec.Root, ledger)
}

// ensureSessionAt is ensureSession with an explicit initial-shell-Pane cwd.
//
// Registry topology materialization starts a Project's first stored shell Pane
// in that Pane's own recorded cwd, while the Project identity, the hook
// contract's PROJMUX_CWD, and the session path anchor all stay on the canonical
// spec.root. Passing spec.root here is exactly the ordinary create path.
func (m *materializer) ensureSessionAt(
	ctx context.Context,
	project coremetadata.Project,
	sessionName, runtimeCWD string,
	ledger *runtimeLedger,
) (intmux.NewSessionResult, error) {
	// Bind an existing invocation server to its physical socket before the
	// printable declaration is built. A genuinely absent server remains the
	// one create-session-only absent-before-create case.
	if err := m.guardExactRoute(ctx, true); err != nil {
		return intmux.NewSessionResult{}, err
	}
	startsFreshServer := m.expectedSocketPath == ""
	exists, err := m.sessions.SessionExists(ctx, sessionName)
	if err != nil {
		return intmux.NewSessionResult{}, tmuxError("check tmux session %q: %v", sessionName, err)
	}
	if exists {
		identity, err := m.requireOwnedSession(ctx, project, sessionName)
		if err != nil {
			return intmux.NewSessionResult{}, err
		}
		if err := m.markCreateOperation(ctx, identity.ID, ledger); err != nil {
			return intmux.NewSessionResult{}, err
		}
		return intmux.NewSessionResult{Created: false, SessionID: identity.ID}, nil
	}
	if identity, found, err := m.preflightSessionOwnership(ctx, project, sessionName); err != nil {
		return intmux.NewSessionResult{}, err
	} else if found {
		if err := m.markCreateOperation(ctx, identity.ID, ledger); err != nil {
			return intmux.NewSessionResult{}, err
		}
		return intmux.NewSessionResult{Created: false, SessionID: identity.ID}, nil
	}
	lifecycle, ok := m.sessions.(persistentSessionLifecycle)
	if !ok {
		return intmux.NewSessionResult{}, errors.New("materialize tmux session: typed persistent lifecycle seam is unavailable")
	}
	request, appeared, err := lifecycle.PreparePersistentSessionCreate(ctx, sessionName, runtimeCWD, project.Spec.Root,
		map[string]string{createOperationEnvironment: ledger.operationMarker})
	if err != nil {
		return intmux.NewSessionResult{}, err
	}
	if appeared {
		identity, err := m.requireOwnedSession(ctx, project, sessionName)
		if err != nil {
			return intmux.NewSessionResult{}, err
		}
		if err := m.markCreateOperation(ctx, identity.ID, ledger); err != nil {
			return intmux.NewSessionResult{}, err
		}
		return intmux.NewSessionResult{Created: false, SessionID: identity.ID}, nil
	}
	args := []string{}
	if startsFreshServer {
		configPath, err := m.generatedAppConfigPath()
		if err != nil {
			lifecycle.AbortPersistentSessionCreate()
			return intmux.NewSessionResult{}, err
		}
		args = append(args, "-f", configPath)
	}
	args = append(args, "-d", "-P", "-F", tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}"), "-s", request.SessionName, "-c", request.RuntimeCWD)
	keys := make([]string, 0, len(request.Environment))
	for key := range request.Environment {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		args = append(args, "-e", key+"="+request.Environment[key])
	}
	action := materializeMutationAction(mutationCreateSession,
		m.boundMutationTarget("project-declaration", sessionName, project.Metadata.UID),
		"unique Project uid/root or absent session name",
		"one detached owned session exists", args...)
	var rawOutput []byte
	ensureErr := m.runMaterializeMutation(ctx, action, func() error {
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
		rawOutput, runErr = runRuntimeMutationCommand(ctx, m.runner, action)
		return runErr
	}, func(ctx context.Context) (bool, error) {
		resultRows := splitTmuxRows(string(rawOutput), 3)
		if len(resultRows) != 1 || exactTmuxHandle(resultRows[0][0], "$") == "" ||
			exactTmuxHandle(resultRows[0][1], "@") == "" || exactTmuxHandle(resultRows[0][2], "%") == "" {
			return false, nil
		}
		out, err := m.read(ctx, "list-panes", "-s", "-t", resultRows[0][0], "-F",
			tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{session_name}",
				"#{E:"+createOperationEnvironment+"}"))
		if err != nil {
			return false, err
		}
		rows := splitTmuxRows(out, 5)
		return len(rows) == 1 && rows[0][0] == resultRows[0][0] && rows[0][1] == resultRows[0][1] &&
			rows[0][2] == resultRows[0][2] && rows[0][3] == sessionName && rows[0][4] == ledger.operationMarker, nil
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
			if rollbackErr := m.recoverCreatedProjectByLease(ctx, result, ledger.operationMarker); rollbackErr != nil {
				ensureErr = errors.Join(ensureErr, fmt.Errorf("created Project owned rollback incomplete: %w", rollbackErr))
			}
		} else {
			m.recordErrorCreatedSession(ctx, project, sessionName, result.SessionID, ledger)
		}
		lifecycle.AbortPersistentSessionCreate()
		return intmux.NewSessionResult{}, tmuxError("materialize tmux session %q: %v", sessionName, ensureErr)
	}
	if exactTmuxHandle(result.SessionID, "$") == "" || exactTmuxHandle(result.WindowID, "@") == "" || exactTmuxHandle(result.PaneID, "%") == "" {
		if startsFreshServer {
			if rollbackErr := m.recoverCreatedProjectByLease(ctx, result, ledger.operationMarker); rollbackErr != nil {
				return intmux.NewSessionResult{}, errors.Join(errors.New("materialize tmux session: atomic result is incomplete"), rollbackErr)
			}
		} else {
			m.recordErrorCreatedSession(ctx, project, sessionName, result.SessionID, ledger)
		}
		lifecycle.AbortPersistentSessionCreate()
		return intmux.NewSessionResult{}, fmt.Errorf("materialize tmux session %q: atomic result is incomplete", sessionName)
	}
	if startsFreshServer {
		if err := m.bindCreatedProjectRouteAuthority(ctx, result, ledger.operationMarker); err != nil {
			if rollbackErr := m.recoverCreatedProjectByLease(ctx, result, ledger.operationMarker); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("created Project owned rollback incomplete: %w", rollbackErr))
			}
			lifecycle.AbortPersistentSessionCreate()
			return intmux.NewSessionResult{}, err
		}
		if err := m.writeCreatedProjectRouteMarker(ctx, result, ledger.operationMarker); err != nil {
			if rollbackErr := m.recoverCreatedProjectByLease(ctx, result, ledger.operationMarker); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("created Project owned rollback incomplete: %w", rollbackErr))
			}
			lifecycle.AbortPersistentSessionCreate()
			return intmux.NewSessionResult{}, err
		}
		if err := m.guardExactRoute(ctx, false, m.expectedSocketPath); err != nil {
			if rollbackErr := m.recoverCreatedProjectByLease(ctx, result, ledger.operationMarker); rollbackErr != nil {
				err = errors.Join(err, fmt.Errorf("created Project owned rollback incomplete: %w", rollbackErr))
			}
			lifecycle.AbortPersistentSessionCreate()
			return intmux.NewSessionResult{}, err
		}
	}
	if err := m.writeCreatedProjectAnchor(ctx, result, project, ledger.operationMarker); err != nil {
		m.recordErrorCreatedSession(ctx, project, sessionName, result.SessionID, ledger)
		lifecycle.AbortPersistentSessionCreate()
		return intmux.NewSessionResult{}, err
	}
	if err := lifecycle.CompletePersistentSessionCreate(ctx, request, result); err != nil {
		m.recordErrorCreatedSession(ctx, project, sessionName, result.SessionID, ledger)
		return intmux.NewSessionResult{}, err
	}
	ledger.markSession(result.SessionID)
	if claimErr := m.claimRuntimeUIDForRollback(ctx, runtimeSession, result.SessionID, project.Metadata.UID, ledger); claimErr != nil {
		return intmux.NewSessionResult{}, claimErr
	}
	if err := m.mirrorProject(ctx, result.SessionID, project); err != nil {
		return intmux.NewSessionResult{}, err
	}
	return result, nil
}

func (m *materializer) bindCreatedProjectRouteAuthority(ctx context.Context, result intmux.NewSessionResult, marker string) error {
	if !filepath.IsAbs(m.expectedSocketPath) || filepath.Clean(m.expectedSocketPath) != m.expectedSocketPath {
		return errors.New("created Project route authority has no exact physical socket")
	}
	out, err := m.runnerAtPhysicalSocket(m.expectedSocketPath).Run(ctx, "tmux", "display-message", "-p", "-t", result.PaneID, "-F",
		tmuxRowFormat("#{socket_path}", "#{pid}", "#{session_id}", "#{window_id}", "#{pane_id}", "#{E:"+createOperationEnvironment+"}"))
	if err != nil {
		return fmt.Errorf("observe created Project route authority: %w", err)
	}
	rows := splitTmuxRows(string(out), 6)
	if len(rows) != 1 || rows[0][0] != m.expectedSocketPath ||
		rows[0][2] != result.SessionID || rows[0][3] != result.WindowID || rows[0][4] != result.PaneID || rows[0][5] != marker {
		return errors.New("created Project route authority socket or $/@/% receipt drifted")
	}
	pid, pidErr := strconv.Atoi(strings.TrimSpace(rows[0][1]))
	if pidErr != nil || pid <= 0 {
		return errors.New("created Project route authority server pid is invalid")
	}
	m.routeAuthority = &runtimeMutationRouteAuthority{
		Class: runtimeMutationRouteApp, ServerPID: rows[0][1],
		SessionID: result.SessionID, WindowID: result.WindowID, PaneID: result.PaneID,
	}
	return nil
}

func (m *materializer) writeCreatedProjectAnchor(ctx context.Context, result intmux.NewSessionResult, project coremetadata.Project, marker string) error {
	action := materializeMutationAction(mutationWriteProjectAnchor,
		m.boundMutationTarget("session", result.SessionID, project.Metadata.UID),
		"exact created tuple and operation lease="+marker,
		"Project path anchor equals Registry root",
		"-t", result.SessionID, "-q", inttmux.ProjectPathSessionOption, project.Spec.Root)
	observeReceipt := func(ctx context.Context) (bool, error) {
		out, err := m.read(ctx, "list-panes", "-s", "-t", result.SessionID, "-F",
			tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{E:"+createOperationEnvironment+"}"))
		rows := splitTmuxRows(out, 4)
		if err != nil {
			return false, err
		}
		return len(rows) == 1 && rows[0][0] == result.SessionID && rows[0][1] == result.WindowID &&
			rows[0][2] == result.PaneID && rows[0][3] == marker, nil
	}
	return m.runMaterializeMutation(ctx, action, func() error {
		observed, err := observeReceipt(ctx)
		if err != nil || !observed {
			return errors.New("created Project tuple or operation lease drifted before path anchor")
		}
		return nil
	}, func() error {
		_, err := runRuntimeMutationCommand(ctx, m.runner, action)
		return err
	}, observeReceipt)
}

func (m *materializer) observeCreatedProjectReceipt(ctx context.Context, result intmux.NewSessionResult, marker string) (bool, error) {
	out, err := m.read(ctx, "list-panes", "-s", "-t", result.SessionID, "-F",
		tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{E:"+createOperationEnvironment+"}"))
	if err != nil {
		return false, err
	}
	rows := splitTmuxRows(out, 4)
	return len(rows) == 1 && rows[0][0] == result.SessionID && rows[0][1] == result.WindowID &&
		rows[0][2] == result.PaneID && rows[0][3] == marker, nil
}

func (m *materializer) writeCreatedProjectRouteMarker(ctx context.Context, result intmux.NewSessionResult, marker string) error {
	target, _, err := m.exactMutationRoute()
	if err != nil {
		return err
	}
	if target.flag != "-L" || strings.TrimSpace(target.value) == "" || !filepath.IsAbs(m.expectedSocketPath) {
		return errors.New("created Project route marker requires an exact logical and physical route")
	}
	printableTarget := m.boundMutationTarget("session", result.SessionID, "logical:"+target.value)
	printableTarget.Socket = "-S=" + m.expectedSocketPath
	printableTarget.Parent = result.WindowID + "/" + result.PaneID
	action := newRuntimeMutation(1, mutationWriteRouteMarker, printableTarget)
	bindRuntimeMutationGuard(&action, "exact created Project tuple and operation lease="+marker)
	action.Operands = []string{"-S", m.expectedSocketPath, "-gq", runtimeMutationSocketNameOption, target.value}
	observeReceipt := func(ctx context.Context) (bool, error) {
		if err := m.guardExactAppRoute(ctx, false, action.Target.PhysicalSocket); err != nil {
			return false, err
		}
		return m.observeCreatedProjectReceipt(ctx, result, marker)
	}
	return executeRuntimeMutationPlan(ctx, []runtimeMutationStep{{
		Action:           action,
		TargetRouteGuard: m.targetRouteGuard(action),
		Reobserve: func(ctx context.Context) (bool, error) {
			owned, err := observeReceipt(ctx)
			if err != nil || !owned {
				return false, err
			}
			out, err := m.routedRunner().Run(ctx, "tmux", "show-options", "-gqv", runtimeMutationSocketNameOption)
			if err != nil {
				return false, err
			}
			current := strings.TrimSpace(string(out))
			if current != "" && current != target.value {
				return false, fmt.Errorf("created Project logical route marker is %q, want %q", current, target.value)
			}
			return current == target.value, nil
		},
		Guard: func(ctx context.Context) error {
			owned, err := observeReceipt(ctx)
			if err != nil || !owned {
				return errors.New("created Project tuple or operation lease drifted before route marker")
			}
			out, err := m.routedRunner().Run(ctx, "tmux", "show-options", "-gqv", runtimeMutationSocketNameOption)
			if err != nil {
				return err
			}
			current := strings.TrimSpace(string(out))
			if current != "" && current != target.value {
				return fmt.Errorf("created Project logical route marker is %q, want blank or exact", current)
			}
			return nil
		},
		Apply: func(ctx context.Context) error {
			_, err := runRuntimeMutationCommand(ctx, m.runner, action)
			return err
		},
	}})
}

func (m *materializer) recoverCreatedProjectByLease(ctx context.Context, result intmux.NewSessionResult, marker string) error {
	if strings.TrimSpace(marker) == "" {
		return nil
	}
	bound, err := m.bindCreatedProjectRecoveryRoute(ctx)
	if err != nil || !bound {
		return err
	}
	out, err := m.routedRunner().Run(ctx, "tmux", "list-sessions", "-F",
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
	target := m.boundMutationTarget("session", result.SessionID, "lease:"+marker)
	target.Parent = result.WindowID + "/" + result.PaneID
	action := materializeMutationAction(mutationKillOwned,
		target,
		"exact created Project tuple and operation lease="+marker,
		"lease-owned created Project session is absent", "-t", result.SessionID)
	return executeRuntimeMutationPlan(ctx, []runtimeMutationStep{{
		Action:           action,
		TargetRouteGuard: m.targetRouteGuard(action),
		Reobserve: func(ctx context.Context) (bool, error) {
			out, err := m.routedRunner().Run(ctx, "tmux", "list-sessions", "-F", "#{session_id}")
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
		},
		Guard: func(ctx context.Context) error {
			owned, err := m.observeCreatedProjectReceipt(ctx, result, marker)
			if err != nil || !owned {
				return errors.New("created Project lease rollback containment is absent or changed")
			}
			return nil
		},
		Apply: func(ctx context.Context) error {
			_, err := runRuntimeMutationCommand(ctx, m.mutationRunner(action), action)
			return err
		},
	}})
}

// bindCreatedProjectRecoveryRoute is deliberately weaker than the normal app
// route guard: a synchronous tmux hook may return an error after new-session
// created a fresh server but before its logical route marker was written. The
// immutable logical declaration discovers only the physical socket; the
// unique exact $/@/% tuple plus operation lease is the destructive authority.
func (m *materializer) bindCreatedProjectRecoveryRoute(ctx context.Context) (bool, error) {
	if filepath.IsAbs(m.expectedSocketPath) && filepath.Clean(m.expectedSocketPath) == m.expectedSocketPath {
		return true, nil
	}
	target, _, err := m.exactMutationRoute()
	if err != nil {
		return false, err
	}
	if target.flag == "-S" {
		if !filepath.IsAbs(target.value) || filepath.Clean(target.value) != target.value {
			return false, fmt.Errorf("created Project recovery route is not an absolute clean socket: %q", target.value)
		}
		m.expectedSocketPath = target.value
		return true, nil
	}
	if target.flag != "-L" {
		return false, errors.New("created Project recovery requires an exact logical or physical route")
	}
	out, err := m.runnerAtTarget(target).Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
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
	m.expectedSocketPath = observed
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
	out, err := m.read(ctx, "list-sessions", "-F", tmuxRowFormat(
		"#{session_id}", "#{session_name}", "#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.ProjectPathSession+"}"))
	if err != nil {
		if inttmux.IsNoServerFailure(err) {
			return nil, err
		}
		return nil, tmuxError("list tmux session identities: %v", err)
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

func (m *materializer) claimRuntimeUID(ctx context.Context, kind runtimeObjectKind, target, uid string) (bool, error) {
	ownershipOption := runtimeObject{Kind: kind}.ownershipOption()
	var args []string
	switch kind {
	case runtimeWindow:
		args = append(args, "-w")
	case runtimePane:
		args = append(args, "-p")
	}
	args = append(args, "-t", target, "-q", ownershipOption, uid)
	stable, err := m.bindMaterializeIdentityTarget(ctx, string(kind), target, uid)
	if err != nil {
		return false, err
	}
	action := materializeMutationAction(mutationWriteIdentity,
		stable,
		"exact newly attributed "+string(kind)+" handle",
		"Registry uid is mirrored for rollback ownership",
		args...)
	if _, err := m.runMutation(ctx, action); err != nil {
		claimErr := tmuxError("claim created tmux %s %s: %v", kind, target, err)
		if got := m.option(ctx, target, "#{"+ownershipOption+"}"); got == uid {
			return true, claimErr
		}
		return false, claimErr
	}
	return true, nil
}

// claimRuntimeUIDForRollback establishes the UID before recording the object.
// A tmux command can report failure after applying the option, so an error is
// followed by an exact readback: a stuck claim enters the ledger and can be
// rolled back safely; an unstuck claim is preserved as an unowned residual.
func (m *materializer) claimRuntimeUIDForRollback(ctx context.Context, kind runtimeObjectKind, target, uid string, ledger *runtimeLedger) error {
	claimed, err := m.claimRuntimeUID(ctx, kind, target, uid)
	if claimed {
		ledger.record(kind, target, uid)
	}
	if err != nil && !claimed {
		m.warnUnclaimedHandle(kind, target)
	}
	return err
}

func (m *materializer) mirrorWindow(ctx context.Context, target string, window coremetadata.Window) error {
	return m.runIdentityWrites(ctx, "window", target, window.Metadata.UID, []identityPlanWrite{
		{operands: []string{"-w", "-t", target, tmuxopts.AutomaticRenameWindow, "off"}, effect: "automatic rename disabled"},
		{operands: []string{"-w", "-t", target, "-q", tmuxopts.WindowUID, window.Metadata.UID}, effect: "Window UID mirror equals Registry"},
		{operands: []string{"-w", "-t", target, "-q", tmuxopts.WindowName, window.Metadata.Name}, effect: "Window stable-name mirror equals Registry"},
		{verb: mutationRenameWindow, operands: []string{"-t", target, window.DisplayName()}, effect: "Window display name equals desired projection"},
	})
}

func (m *materializer) mirrorPane(ctx context.Context, target string, pane coremetadata.Pane) error {
	return m.runIdentityWrites(ctx, "pane", target, pane.Metadata.UID, []identityPlanWrite{
		{operands: []string{"-p", "-t", target, "-q", tmuxopts.PaneUID, pane.Metadata.UID}, effect: "Pane UID mirror equals Registry"},
		{operands: []string{"-p", "-t", target, "-q", tmuxopts.PaneName, pane.Metadata.Name}, effect: "Pane stable-name mirror equals Registry"},
	})
}

type identityPlanWrite struct {
	verb     runtimeMutationVerb
	operands []string
	effect   string
}

func (m *materializer) mirrorProject(ctx context.Context, target string, project coremetadata.Project) error {
	return m.runIdentityWrites(ctx, "session", target, project.Metadata.UID, []identityPlanWrite{
		{operands: []string{"-t", target, "-q", tmuxopts.ProjectUIDSession, project.Metadata.UID}, effect: "Project UID mirror equals Registry"},
		{operands: []string{"-t", target, "-q", tmuxopts.ProjectNameSession, project.Metadata.Name}, effect: "Project name mirror equals Registry"},
	})
}

func (m *materializer) runIdentityWrites(ctx context.Context, kind, target, uid string, writes []identityPlanWrite) error {
	stable, err := m.bindMaterializeIdentityTarget(ctx, kind, target, uid)
	if err != nil {
		return err
	}
	steps := make([]runtimeMutationStep, 0, len(writes))
	for index, write := range writes {
		verb := write.verb
		if verb == "" {
			verb = mutationWriteIdentity
		}
		action := newRuntimeMutation(index+1, verb, stable)
		bindRuntimeMutationGuard(&action, "exact "+kind+" carries claimed uid="+uid+" before "+write.effect)
		action.Operands = slices.Clone(write.operands)
		steps = append(steps, runtimeMutationStep{
			Action:           action,
			TargetRouteGuard: m.targetRouteGuard(action),
			Reobserve:        func(ctx context.Context) (bool, error) { return m.observeMutationEffect(ctx, action) },
			Guard: func(ctx context.Context) error {
				if err := m.guardExactRoute(ctx, false, action.Target.PhysicalSocket); err != nil {
					return err
				}
				return m.guardMaterializeIdentityTarget(ctx, action.Target, false)
			},
			Apply: func(ctx context.Context) error {
				_, err := runRuntimeMutationCommand(ctx, m.runner, action)
				return err
			},
		})
	}
	return executeRuntimeMutationPlan(ctx, steps)
}

func (m *materializer) recordErrorCreatedSession(
	ctx context.Context,
	project coremetadata.Project,
	sessionName string,
	exactSessionID string,
	ledger *runtimeLedger,
) {
	if ledger == nil || strings.TrimSpace(ledger.operationMarker) == "" {
		return
	}
	target := exactTmuxHandle(exactSessionID, "$")
	if target == "" {
		exists, err := m.sessions.SessionExists(ctx, sessionName)
		if err != nil || !exists {
			return
		}
		target = sessionName
	}
	out, err := m.routedRunner().Run(ctx, "tmux", "show-environment", "-t", target)
	if err != nil || sessionEnvironmentValue(string(out), createOperationEnvironment) != ledger.operationMarker {
		if exactTmuxHandle(exactSessionID, "$") != "" {
			m.warnUnclaimedHandle(runtimeSession, exactSessionID)
		}
		return
	}
	ledger.markSession(target)
	sessionID := exactTmuxHandle(exactSessionID, "$")
	if sessionID == "" {
		sessionID = m.option(ctx, target, "#{session_id}")
	}
	if sessionID == "" {
		if m.warn != nil {
			fmt.Fprintf(m.warn, "projmux: create failed with an owned but unidentifiable tmux session %s; preserved it\n", sessionName)
		}
		return
	}
	if claimErr := m.claimRuntimeUIDForRollback(ctx, runtimeSession, sessionID, project.Metadata.UID, ledger); claimErr != nil {
		return
	}
	_ = m.mirrorProject(ctx, sessionID, project)
}

func (m *materializer) markCreateOperation(ctx context.Context, sessionName string, ledger *runtimeLedger) error {
	if ledger == nil || strings.TrimSpace(ledger.operationMarker) == "" {
		return errors.New("materialize tmux session: create-operation lease is missing")
	}
	if err := m.guardExactRoute(ctx, false); err != nil {
		return tmuxError("mark tmux session %q for create operation: %v", sessionName, err)
	}
	action := materializeMutationAction(mutationWriteLease,
		m.boundMutationTarget("session", sessionName, "session:"+sessionName),
		"exact managed session="+sessionName,
		"operation lease is installed",
		"-t", sessionName, createOperationEnvironment, ledger.operationMarker)
	if _, err := m.runMutation(ctx, action); err != nil {
		return tmuxError("mark tmux session %q for create operation: %v", sessionName, err)
	}
	ledger.markSession(sessionName)
	return nil
}

func (m *materializer) clearCreateOperations(ctx context.Context, ledger *runtimeLedger) {
	if ledger == nil {
		return
	}
	var steps []runtimeMutationStep
	for _, sessionName := range ledger.markedSessionIDs {
		exists, err := m.sessions.SessionExists(ctx, sessionName)
		if err != nil || !exists {
			continue
		}
		out, err := m.routedRunner().Run(ctx, "tmux", "show-environment", "-t", sessionName)
		if err != nil || sessionEnvironmentValue(string(out), createOperationEnvironment) != ledger.operationMarker {
			continue
		}
		for _, environment := range []string{finalizeOperationEnvironment, createOperationEnvironment} {
			if sessionEnvironmentValue(string(out), environment) != ledger.operationMarker {
				continue
			}
			action := materializeMutationAction(mutationClearLease,
				m.boundMutationTarget("session", sessionName, "session:"+sessionName),
				"same operation lease="+ledger.operationMarker,
				"operation lease is absent",
				"-u", "-t", sessionName, environment)
			action.Order = len(steps) + 1
			steps = append(steps, runtimeMutationStep{
				Action:           action,
				TargetRouteGuard: m.targetRouteGuard(action),
				Reobserve:        func(ctx context.Context) (bool, error) { return m.observeMutationEffect(ctx, action) },
				Guard: func(ctx context.Context) error {
					if err := m.guardExactRoute(ctx, false, action.Target.PhysicalSocket); err != nil {
						return err
					}
					out, err := m.routedRunner().Run(ctx, "tmux", "show-environment", "-t", sessionName)
					if err != nil {
						return err
					}
					if sessionEnvironmentValue(string(out), environment) != ledger.operationMarker ||
						sessionEnvironmentValue(string(out), createOperationEnvironment) != ledger.operationMarker {
						return errors.New("create-operation lease changed")
					}
					return nil
				},
				Apply: func(ctx context.Context) error {
					_, err := runRuntimeMutationCommand(ctx, m.mutationRunner(action), action)
					return err
				},
			})
		}
	}
	if err := executeRuntimeMutationPlan(ctx, steps); err != nil && m.warn != nil {
		fmt.Fprintf(m.warn, "projmux: could not clear guarded create-operation lease(s): %v\n", err)
	}
}

// windowIDForUID returns the tmux window id inside sessionName that mirrors uid.
func (m *materializer) windowIDForUID(ctx context.Context, sessionName, uid string) (string, error) {
	if strings.TrimSpace(uid) == "" {
		return "", nil
	}
	out, err := m.read(ctx, "list-windows", "-t", sessionName, "-F",
		tmuxRowFormat("#{"+tmuxopts.WindowUID+"}", "#{window_id}"))
	if err != nil {
		return "", tmuxError("list tmux windows of session %q: %v", sessionName, err)
	}
	for _, fields := range splitTmuxRows(out, 2) {
		if fields[0] == uid {
			return fields[1], nil
		}
	}
	return "", nil
}

// panesOf lists the panes of a tmux window as (mirrored uid, pane id) rows in
// tmux order.
func (m *materializer) panesOf(ctx context.Context, windowID string) ([][2]string, error) {
	out, err := m.read(ctx, "list-panes", "-t", windowID, "-F",
		tmuxRowFormat("#{"+tmuxopts.PaneUID+"}", "#{pane_id}"))
	if err != nil {
		return nil, tmuxError("list tmux panes of window %q: %v", windowID, err)
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

type runtimeOwner struct {
	SessionID string
	WindowID  string
}

type runtimeOwnerSet map[runtimeOwner]struct{}

type runtimeOwners map[string]runtimeOwnerSet

// newWindow creates one detached tmux Window and accepts it only when the
// composite output, global before/after inventory, and exact owner relation all
// identify the same sole new Window and sole new primary Pane.
func (m *materializer) newWindow(ctx context.Context, sessionID, name, cwd string, command []string) (windowCreateResult, error) {
	beforeWindows, beforePanes, beforeErr := m.runtimeOwners(ctx)
	if beforeErr != nil {
		return windowCreateResult{}, tmuxError("inventory tmux runtime before window create: %v", beforeErr)
	}
	args := []string{"-d", "-P", "-F", tmuxRowFormat("#{window_id}", "#{pane_id}"), "-t", sessionID + ":"}
	if strings.TrimSpace(name) != "" {
		args = append(args, "-n", name)
	}
	if strings.TrimSpace(cwd) != "" {
		args = append(args, "-c", cwd)
	}
	action := materializeMutationAction(mutationCreateWindow,
		m.boundMutationTarget("window-parent", sessionID, "window-name:"+name),
		"exact owned session containment="+sessionID,
		"one detached owned Window and primary Pane exist",
		args...)
	action.Command = slices.Clone(command)
	var rawOutput []byte
	createErr := m.runMaterializeMutation(ctx, action, func() error {
		currentWindows, currentPanes, err := m.runtimeOwners(ctx)
		if err != nil {
			return err
		}
		if !equalRuntimeOwners(beforeWindows, currentWindows) || !equalRuntimeOwners(beforePanes, currentPanes) {
			return fmt.Errorf("runtime owner inventory drifted before Window create")
		}
		return m.guardMutationAction(ctx, action)
	}, func() error {
		var err error
		rawOutput, err = runRuntimeMutationCommand(ctx, m.runner, action)
		return err
	}, func(ctx context.Context) (bool, error) {
		output := strings.TrimSpace(string(rawOutput))
		if output == "" {
			return false, nil
		}
		afterWindows, afterPanes, err := m.runtimeOwners(ctx)
		if err != nil {
			return false, err
		}
		_, err = attributeCreatedWindow(output, sessionID, beforeWindows, beforePanes, afterWindows, afterPanes)
		return err == nil, err
	})
	output := strings.TrimSpace(string(rawOutput))
	afterWindows, afterPanes, inventoryErr := m.runtimeOwners(ctx)
	if inventoryErr != nil {
		m.warnCompositeWindowResult(output)
		inventoryFailure := tmuxError("inventory tmux runtime after window create: %v", inventoryErr)
		if createErr != nil {
			return windowCreateResult{}, errors.Join(tmuxError("create tmux window in session %q: %v", sessionID, createErr), inventoryFailure)
		}
		return windowCreateResult{}, inventoryFailure
	}
	result, attributionErr := attributeCreatedWindow(output, sessionID, beforeWindows, beforePanes, afterWindows, afterPanes)
	if attributionErr != nil {
		m.warnUnclaimedOwners("window", beforeWindows, afterWindows)
		m.warnUnclaimedOwners("pane", beforePanes, afterPanes)
		if createErr != nil {
			return windowCreateResult{}, errors.Join(tmuxError("create tmux window in session %q: %v", sessionID, createErr), attributionErr)
		}
		return windowCreateResult{}, attributionErr
	}
	if createErr != nil {
		return result, tmuxError("create tmux window in session %q: %v", sessionID, createErr)
	}
	return result, nil
}

func (m *materializer) runtimeOwners(ctx context.Context) (runtimeOwners, runtimeOwners, error) {
	windowsOut, err := m.read(ctx, "list-windows", "-a", "-F", tmuxRowFormat("#{session_id}", "#{window_id}"))
	if err != nil {
		return nil, nil, err
	}
	panesOut, err := m.read(ctx, "list-panes", "-a", "-F", tmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}"))
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
		owner := runtimeOwner{SessionID: row[0], WindowID: row[1]}
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
		owner := runtimeOwner{SessionID: row[0], WindowID: row[1]}
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
	wantWindowOwner := runtimeOwner{SessionID: sessionID, WindowID: result.WindowID}
	wantPaneOwner := runtimeOwner{SessionID: sessionID, WindowID: result.WindowID}
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

func (owners runtimeOwnerSet) contains(owner runtimeOwner) bool {
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
	if m.warn == nil {
		return
	}
	if residual := newRuntimeIDs(before, after); len(residual) > 0 {
		fmt.Fprintf(m.warn, "projmux: create attribution failed with unclaimed tmux %s drift; preserved %s; inspect these exact handles before retry or cleanup\n",
			kind, strings.Join(residual, ", "))
	}
}

func (m *materializer) warnUnclaimedHandle(kind runtimeObjectKind, id string) {
	if m.warn == nil || strings.TrimSpace(id) == "" {
		return
	}
	fmt.Fprintf(m.warn, "projmux: create could not claim tmux %s %s; preserved this exact residual handle for inspection before retry or cleanup\n", kind, id)
}

func (m *materializer) warnCompositeWindowResult(output string) {
	rows := splitTmuxRows(output, 2)
	if len(rows) != 1 {
		return
	}
	if id := exactTmuxHandle(rows[0][0], "@"); id != "" {
		m.warnUnclaimedHandle(runtimeWindow, id)
	}
	if id := exactTmuxHandle(rows[0][1], "%"); id != "" {
		m.warnUnclaimedHandle(runtimePane, id)
	}
}

// splitPlacementFlag maps the closed placement enum onto its tmux split axis.
func splitPlacementFlag(placement string) string {
	if placement == placementDown {
		return "-v"
	}
	return "-h"
}

// splitPane splits an anchor pane detached and returns the new pane id.
//
// `-d` is the whole point: tmux leaves the previously active pane active, so
// the split is a pure structural mutation with no focus side effect.
func (m *materializer) splitPane(ctx context.Context, anchorPaneID, placement, cwd string, command []string) (string, error) {
	before, beforeErr := m.runtimeIDs(ctx, "list-panes", anchorPaneID, "#{pane_id}", "%")
	if beforeErr != nil {
		return "", tmuxError("list tmux panes around %q before split: %v", anchorPaneID, beforeErr)
	}
	args := []string{"-d", "-P", "-F", "#{pane_id}", splitPlacementFlag(placement), "-t", anchorPaneID}
	if strings.TrimSpace(cwd) != "" {
		args = append(args, "-c", cwd)
	}
	action := materializeMutationAction(mutationCreatePane,
		m.boundMutationTarget("pane", anchorPaneID, "anchor:"+anchorPaneID),
		"exact owned anchor containment="+anchorPaneID,
		"one detached owned Pane exists",
		args...)
	action.Command = slices.Clone(command)
	var rawID []byte
	err := m.runMaterializeMutation(ctx, action, func() error {
		current, err := m.runtimeIDs(ctx, "list-panes", anchorPaneID, "#{pane_id}", "%")
		if err != nil {
			return err
		}
		if len(before) != len(current) {
			return errors.New("Pane inventory drifted before split")
		}
		for id := range before {
			if !current[id] {
				return errors.New("Pane inventory drifted before split")
			}
		}
		return m.guardMutationAction(ctx, action)
	}, func() error {
		var err error
		rawID, err = runRuntimeMutationCommand(ctx, m.runner, action)
		return err
	}, func(ctx context.Context) (bool, error) {
		id := exactTmuxHandle(strings.TrimSpace(string(rawID)), "%")
		if id == "" {
			return false, nil
		}
		current, err := m.runtimeIDs(ctx, "list-panes", anchorPaneID, "#{pane_id}", "%")
		if err != nil {
			return false, err
		}
		return !before[id] && current[id], nil
	})
	id := strings.TrimSpace(string(rawID))
	if err != nil {
		after, listErr := m.runtimeIDs(ctx, "list-panes", anchorPaneID, "#{pane_id}", "%")
		if listErr == nil {
			id = errorCreatedHandle(id, "%", before, after)
			if id == "" {
				m.warnUnclaimedRuntime("pane", before, after)
			}
		} else {
			id = ""
		}
		return id, tmuxError("split tmux pane %q: %v", anchorPaneID, err)
	}
	id = exactTmuxHandle(id, "%")
	if id == "" {
		return "", fmt.Errorf("split tmux pane %q: tmux returned no pane id", anchorPaneID)
	}
	if before[id] {
		return "", fmt.Errorf("split tmux pane %q: tmux returned pre-existing pane id %s", anchorPaneID, id)
	}
	return id, nil
}

// equalizeSplitLayout applies the same scoped, best-effort sizing used by the
// legacy AI split. It intentionally returns no error: layout observation is
// outside the create transaction's failure and rollback contract.
func (m *materializer) equalizeSplitLayout(ctx context.Context, anchorPaneID, placement string) {
	if m == nil || m.runner == nil {
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
	batchTarget := m.boundMutationTarget("pane", anchorPaneID, observed.paneUID(anchorPaneID))
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
			routeErr = m.targetRouteGuard(action)(ctx)
			routeChecked = true
		}
		return routeErr
	}
	guardBatch := func(ctx context.Context, action plannedRuntimeMutation) error {
		if !semanticChecked {
			// Revalidate the same physical generation immediately before the first
			// write, then require the complete ordered identity/geometry receipt to
			// be byte-identical to planning. Unknown or drifted state writes zero.
			semanticErr = m.targetRouteGuard(action)(ctx)
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
		target := m.boundMutationTarget("pane", resize.paneID, paneUID)
		target.Parent = observed.sessionID + "/" + observed.windowID
		action := materializeMutationAction(mutationWriteLayout,
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
				_, _ = runRuntimeMutationCommand(ctx, m.mutationRunner(action), action)
				return nil
			},
		})
	}
	// Layout remains best effort: a failed or partial resize cannot turn an
	// already successful managed create into a topology rollback. The important
	// boundary is that every attempted write came from one fully guarded plan.
	_ = executeRuntimeMutationPlan(ctx, steps)
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
	out, err := m.read(ctx, "list-panes", "-t", anchorPaneID, "-F", splitLayoutBatchFormat)
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

func (m *materializer) warnUnclaimedRuntime(kind string, before, after map[string]bool) {
	if m.warn == nil {
		return
	}
	var residual []string
	for id := range after {
		if !before[id] {
			residual = append(residual, id)
		}
	}
	slices.Sort(residual)
	if len(residual) > 0 {
		fmt.Fprintf(m.warn, "projmux: create failed with unclaimed tmux %s drift; preserved %s\n", kind, strings.Join(residual, ", "))
	}
}

func (m *materializer) runtimeIDs(ctx context.Context, command, target, format, prefix string) (map[string]bool, error) {
	out, err := m.read(ctx, command, "-t", target, "-F", format)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		if id := exactTmuxHandle(line, prefix); id != "" {
			ids[id] = true
		}
	}
	return ids, nil
}

func exactTmuxHandle(output, prefix string) string {
	output = strings.TrimSpace(output)
	if len(output) < 2 || !strings.HasPrefix(output, prefix) {
		return ""
	}
	for _, r := range strings.TrimPrefix(output, prefix) {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return output
}

func errorCreatedHandle(output, prefix string, before, after map[string]bool) string {
	var candidate string
	for line := range strings.SplitSeq(output, "\n") {
		id := exactTmuxHandle(line, prefix)
		if id == "" {
			continue
		}
		if candidate != "" && candidate != id {
			return ""
		}
		candidate = id
	}
	if candidate == "" || before[candidate] || !after[candidate] {
		return ""
	}
	return candidate
}

// The field separator of the materializer's own list queries, in the two
// spellings tmux distinguishes.
//
// A format must carry the escaped spelling. tmux renders a non-printable byte in
// list output as its octal escape, so a raw 0x1F in the format comes back raw on
// tmux 3.6 and as the four literal characters `\037` on tmux 3.5a, while the
// escaped spelling comes back identically on both. Parsing folds the escaped
// spelling back to the raw byte, which also accepts the raw form.
const (
	tmuxRowSep       = "\x1f"
	tmuxRowSepFormat = "\\037"
)

// tmuxRowFormat joins format fields with the separator tmux prints verbatim.
func tmuxRowFormat(fields ...string) string {
	return strings.Join(fields, tmuxRowSepFormat)
}

// splitTmuxRows parses a tmux list output into rows of exactly want fields.
func splitTmuxRows(output string, want int) [][]string {
	output = strings.ReplaceAll(output, tmuxRowSepFormat, tmuxRowSep)
	var rows [][]string
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, tmuxRowSep)
		if len(fields) != want {
			continue
		}
		rows = append(rows, fields)
	}
	return rows
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
