package runtime

import (
	"context"
	"errors"
	"fmt"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type SessionMaterializer interface {
	SessionExists(context.Context, string) (bool, error)
	EnsureSession(context.Context, string, string) error
}

// MaterializeRuntime is the exact-route execution shared by detached creation
// and Pane deletion's replacement shells. The caller owns transaction policy.
type MaterializeRuntime struct {
	Runner   TmuxCommandRunner
	Sessions SessionMaterializer
	// Target is the immutable logical route; writes reobserve the same server.
	Target TmuxTransport
	// ExpectedSocketPath pins the physical identity before mutation.
	ExpectedSocketPath string
	// Warn receives non-fatal rollback diagnostics.
	Warn           io.Writer
	SocketName     string
	RouteAuthority *RuntimeMutationRouteAuthority
	// RouteIdentity and IdentityMemo are transaction-local proofs invalidated
	// by every guarded write.
	RouteIdentity     *RuntimeRouteIdentityCache
	AfterGuardedWrite func()
	IdentityMemo      *IdentityObservationMemo
	GuardedWrites     uint64
}

// runtimeLedger records the tmux objects one operation created so a later
// failure can undo exactly them.
//
// Rollback is ownership checked, not best effort: an entry is only removed when
// the tmux object still carries the same Projmux uid this operation mirrored
// onto it. A pre-existing session, a window another operation created, and an
// id that tmux has since recycled are all left alone.
type RuntimeLedger struct {
	Created          []RuntimeObject
	OperationMarker  string
	MarkedSessionIDs []string
	// currentWindows is the transaction-local stable-UID resolution already
	// proved by ensureProjectRuntime (or by an exact Window creation in this
	// transaction). It lets later Pane materialization consume the same current
	// locator without another list-windows round trip while the Registry lock is
	// held. It is never carried across transactions.
	CurrentWindows map[string]RuntimeOwner
}

func NewRuntimeLedger(operationID string) *RuntimeLedger {
	return NewRuntimeLedgerAt(operationID, time.Now())
}

// newRuntimeLedgerAt builds the ledger with an explicit clock reading for the
// operation marker. The create transaction passes its injected clock here so a
// test can compare two transactions byte for byte without crossing a second
// boundary; every other caller keeps newRuntimeLedger's wall clock.
func NewRuntimeLedgerAt(operationID string, now time.Time) *RuntimeLedger {
	return &RuntimeLedger{
		OperationMarker: NewCreateOperationMarkerAt(operationID, now),
		CurrentWindows:  make(map[string]RuntimeOwner),
	}
}
func (l *RuntimeLedger) Record(kind RuntimeObjectKind, id, uid string) {
	if l == nil || strings.TrimSpace(id) == "" {
		return
	}
	l.Created = append(l.Created, RuntimeObject{Kind: kind, ID: id, UID: uid})
}

// entries returns the ledger in creation order.
func (l *RuntimeLedger) Entries() []RuntimeObject {
	if l == nil {
		return nil
	}
	return l.Created
}
func (l *RuntimeLedger) MarkSession(session string) {
	if l == nil || strings.TrimSpace(session) == "" {
		return
	}
	if slices.Contains(l.MarkedSessionIDs, session) {
		return
	}
	l.MarkedSessionIDs = append(l.MarkedSessionIDs, session)
}
func (l *RuntimeLedger) ResetCurrentWindows() {
	if l == nil {
		return
	}
	l.CurrentWindows = make(map[string]RuntimeOwner)
}
func (l *RuntimeLedger) ObserveCurrentWindow(uid string, owner RuntimeOwner) {
	if l == nil || strings.TrimSpace(uid) == "" || ExactTmuxHandle(owner.SessionID, "$") == "" ||
		ExactTmuxHandle(owner.WindowID, "@") == "" {
		return
	}
	if l.CurrentWindows == nil {
		l.CurrentWindows = make(map[string]RuntimeOwner)
	}
	l.CurrentWindows[strings.TrimSpace(uid)] = owner
}
func (l *RuntimeLedger) CurrentWindow(uid string) (RuntimeOwner, bool) {
	if l == nil {
		return RuntimeOwner{}, false
	}
	owner, ok := l.CurrentWindows[strings.TrimSpace(uid)]
	return owner, ok
}

// identityObservationMemo keeps, for one identity-write plan, the latest
// $/@/% identity-bundle observation of each target that no write of ours has
// followed. Every guarded Apply/Undo empties it (guardedWriteSteps), so an
// entry is never older than the previous guarded write.
//
// A bind or an effect reobservation may answer from any entry. A guard -- the
// read that brackets a write -- reads tmux unless the entry was itself read by
// a guard after the latest write: the first guard after a write always reads,
// and the further guards of the same all-guards-before-first-write batch
// repeat that read with no write in between, so they reuse it.
type IdentityObservationMemo struct {
	Entries map[string]IdentityObservationEntry
}
type IdentityObservationEntry struct {
	Observation MaterializeIdentityObservation
	Guarded     bool
}

// scopeIdentityObservations opens the memo for one identity-write plan and
// returns its close. A nested call keeps the outer scope.
func (m *MaterializeRuntime) ScopeIdentityObservations() func() {
	if m == nil || m.IdentityMemo != nil {
		return func() {}
	}
	m.IdentityMemo = &IdentityObservationMemo{Entries: map[string]IdentityObservationEntry{}}
	return func() { m.IdentityMemo = nil }
}

// forgetIdentityObservations empties the memo after a guarded write.
func (m *MaterializeRuntime) ForgetIdentityObservations() {
	if m == nil || m.IdentityMemo == nil {
		return
	}
	clear(m.IdentityMemo.Entries)
}

// guardedWriteSteps is the materializer's guarded-write seam: every materializer
// plan passes its steps through it on the way into executeRuntimeMutationPlan.
// Each Apply and Undo it returns is a guarded write, and each one drops every
// identity proof this transaction could reuse. The step's post-effect
// reobservation, and any later guard, therefore proves the route against tmux
// again: no guarded write relies on a proof older than the previous guarded
// write.
func (m *MaterializeRuntime) GuardedWriteSteps(steps []RuntimeMutationStep) []RuntimeMutationStep {
	guarded := slices.Clone(steps)
	for i := range guarded {
		if apply := guarded[i].Apply; apply != nil {
			guarded[i].Apply = func(ctx context.Context) error {
				err := apply(ctx)
				m.GuardedWrites++
				m.InvalidateRouteIdentity("guarded-write")
				m.ForgetIdentityObservations()
				if m.AfterGuardedWrite != nil {
					m.AfterGuardedWrite()
				}
				return err
			}
		}
		if undo := guarded[i].Undo; undo != nil {
			guarded[i].Undo = func(ctx context.Context) error {
				err := undo(ctx)
				m.GuardedWrites++
				m.InvalidateRouteIdentity("guarded-write")
				m.ForgetIdentityObservations()
				return err
			}
		}
	}
	return guarded
}

// invalidateRouteIdentity drops every cached proof. Callers use it when the
// route itself is re-resolved or rebound onto a different server.
func (m *MaterializeRuntime) InvalidateRouteIdentity(reason string) {
	if m == nil {
		return
	}
	m.RouteIdentity.Invalidate(reason)
}

// exactRouteIdentityKey names the identity guardExactRouteOwnership proves when
// it requires the logical marker. A route without a bound physical socket or a
// captured server generation is not cacheable.
func (m *MaterializeRuntime) ExactRouteIdentityKey(target TmuxTransport) (RuntimeRouteIdentityKey, bool) {
	if m == nil || m.RouteAuthority == nil {
		return RuntimeRouteIdentityKey{}, false
	}
	return RuntimeRouteIdentityKeyForRoute(RouteIdentityScopeExactRoute, RuntimeMutationRoute{
		Target: target, ExpectedSocketPath: m.ExpectedSocketPath,
		SocketName: m.LogicalSocketName(target), Authority: m.RouteAuthority,
	})
}
func (m *MaterializeRuntime) ReadTmux(ctx context.Context, args ...string) (string, error) {
	out, err := m.RoutedRunner().Run(ctx, "tmux", args...)
	return strings.TrimSpace(string(out)), err
}
func (m *MaterializeRuntime) RoutedRunner() TmuxCommandRunner {
	if m.ExpectedSocketPath != "" {
		return m.RunnerAtPhysicalSocket(m.ExpectedSocketPath)
	}
	return m.Runner
}
func (m *MaterializeRuntime) ExactMutationRoute() (TmuxTransport, string, error) {
	target := m.Target
	if target.Flag() == "" || target.Value == "" {
		switch runner := m.Runner.(type) {
		case ExplicitTmuxRunner:
			target = runner.Target
		case *ExplicitTmuxRunner:
			target = runner.Target
		}
	}
	switch target.Flag() {
	case "-L":
		if strings.TrimSpace(target.Value) == "" {
			break
		}
		return target, "-L=" + target.Value, nil
	case "-S":
		if filepath.IsAbs(target.Value) {
			target.Value = filepath.Clean(target.Value)
			return target, "-S=" + target.Value, nil
		}
	}
	return TmuxTransport{}, "", errors.New("materializer requires an exact -L socket name or absolute -S socket path")
}
func (m *MaterializeRuntime) BoundMutationTarget(kind, id, uid string) RuntimeMutationTarget {
	_, route, _ := m.ExactMutationRoute()
	target := RuntimeMutationTarget{Socket: route, PhysicalSocket: PrintableRuntimeMutationSocket(m.ExpectedSocketPath), Kind: kind, ID: id, UID: uid}
	if m.RouteAuthority != nil {
		target.RouteAuthority = m.RouteAuthority.Printable()
	}
	return target
}

// mutationRunner returns the execution authority printed by one action.  The
// only action allowed to execute before a physical socket exists is the
// create-session declaration; every later action is transported through the
// exact absolute -S identity captured by the first post-create observation.
func (m *MaterializeRuntime) MutationRunner(action PlannedRuntimeMutation) TmuxCommandRunner {
	if filepath.IsAbs(action.Target.PhysicalSocket) {
		return m.RunnerAtPhysicalSocket(action.Target.PhysicalSocket)
	}
	return m.Runner
}
func (m *MaterializeRuntime) TargetRouteGuard(action PlannedRuntimeMutation) func(context.Context) error {
	return func(ctx context.Context) error {
		target, _, err := m.ExactMutationRoute()
		if err != nil {
			return err
		}
		if action.Target.PhysicalSocket == RuntimeMutationSocketAbsentBeforeCreate {
			if m.ExpectedSocketPath != "" || m.RouteAuthority != nil ||
				action.Target.Socket != target.Flag()+"="+target.Value || action.Target.RouteAuthority != "" {
				return errors.New("materializer absent-before-create authority disagrees with printable target")
			}
			probe := m.RunnerAtTarget(target)
			if _, err := probe.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}"); err != nil {
				if inttmux.IsNoServerFailure(err) {
					return nil
				}
				return fmt.Errorf("materializer absent-before-create authority is unknown: %w", err)
			}
			return errors.New("materializer absent-before-create server appeared after planning")
		}
		wantSocket := target.Flag() + "=" + target.Value
		if action.Verb == MutationWriteRouteMarker {
			wantSocket = "-S=" + m.ExpectedSocketPath
		}
		if m.RouteAuthority == nil || action.Target.PhysicalSocket != m.ExpectedSocketPath ||
			action.Target.RouteAuthority != m.RouteAuthority.Printable() || action.Target.Socket != wantSocket {
			return errors.New("materializer printable route authority disagrees with captured server generation")
		}
		if action.Verb == MutationWriteRouteMarker || action.Verb == MutationKillOwned {
			probe := m.RunnerAtPhysicalSocket(m.ExpectedSocketPath)
			pathOut, err := probe.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
			if err != nil || strings.TrimSpace(string(pathOut)) != m.ExpectedSocketPath {
				return errors.New("materializer printed physical socket drifted")
			}
			pidOut, err := probe.Run(ctx, "tmux", "display-message", "-p", "-F", "#{pid}")
			if err != nil || strings.TrimSpace(string(pidOut)) != m.RouteAuthority.ServerPID {
				return errors.New("materializer printed server generation drifted")
			}
			if action.Verb == MutationWriteRouteMarker {
				owned, err := probe.Run(ctx, "tmux", "show-options", "-gqv", tmuxopts.AppGlobal)
				if err != nil || strings.TrimSpace(string(owned)) != "1" {
					return errors.New("materializer route-marker target is not app-owned")
				}
			}
			return nil
		}
		route := RuntimeMutationRoute{
			Target: target, ExpectedSocketPath: m.ExpectedSocketPath,
			SocketName: m.LogicalSocketName(target), Authority: m.RouteAuthority,
		}
		return GuardPrintedRuntimeMutationRouteWithIdentity(ctx, m.BaseRunner(), route, action, m.RouteIdentity)
	}
}
func (m *MaterializeRuntime) RunnerAtPhysicalSocket(path string) TmuxCommandRunner {
	return m.RunnerAtTarget(TmuxTransport{Kind: TmuxSocketPath, Value: filepath.Clean(path), Source: TmuxSocketPathSource})
}
func (m *MaterializeRuntime) RunnerAtTarget(target TmuxTransport) TmuxCommandRunner {
	return ExplicitTmuxRunner{Runner: m.BaseRunner(), Target: target}
}
func (m *MaterializeRuntime) BaseRunner() TmuxCommandRunner {
	base := m.Runner
	for {
		switch routed := base.(type) {
		case ExplicitTmuxRunner:
			base = routed.Runner
			continue
		case *ExplicitTmuxRunner:
			base = routed.Runner
			continue
		}
		break
	}
	return base
}
func (m *MaterializeRuntime) GuardExactRoute(ctx context.Context, allowNoServer bool, plannedPhysical ...string) error {
	return m.GuardExactRouteOwnership(ctx, allowNoServer, true, plannedPhysical...)
}
func (m *MaterializeRuntime) GuardExactAppRoute(ctx context.Context, allowNoServer bool, plannedPhysical ...string) error {
	return m.GuardExactRouteOwnership(ctx, allowNoServer, false, plannedPhysical...)
}
func (m *MaterializeRuntime) GuardExactRouteOwnership(ctx context.Context, allowNoServer, requireLogical bool, plannedPhysical ...string) error {
	if len(plannedPhysical) == 1 {
		planned := strings.TrimSpace(plannedPhysical[0])
		if planned == RuntimeMutationSocketAbsentBeforeCreate {
			if !allowNoServer || m.ExpectedSocketPath != "" {
				return errors.New("printed absent-before-create socket disagrees with bound materializer route")
			}
		} else if filepath.Clean(planned) != filepath.Clean(m.ExpectedSocketPath) {
			return fmt.Errorf("printed physical socket %q disagrees with materializer route %q", planned, m.ExpectedSocketPath)
		}
	}
	// The printable checks above are pure and always run. Only the tmux read set
	// below is reusable, and only when this transaction already proved the exact
	// same identity. The app-route variant is deliberately never cached: it does
	// not read the logical marker, so it cannot answer for the full identity.
	if requireLogical {
		target, _, err := m.ExactMutationRoute()
		if err != nil {
			return err
		}
		if key, ok := m.ExactRouteIdentityKey(target); ok && m.RouteIdentity.Reuse(key) {
			return nil
		}
	}
	if err := m.ProveExactRouteOwnership(ctx, allowNoServer, requireLogical, plannedPhysical...); err != nil {
		m.RouteIdentity.Invalidate("exact-route-probe-error")
		return err
	}
	if requireLogical {
		// The proof may have bound the route authority, so the key is built from
		// what was actually proved rather than from the pre-proof state.
		if target, _, err := m.ExactMutationRoute(); err == nil {
			if key, ok := m.ExactRouteIdentityKey(target); ok {
				m.RouteIdentity.Record(key)
			}
		}
	}
	return nil
}
func (m *MaterializeRuntime) ProveExactRouteOwnership(ctx context.Context, allowNoServer, requireLogical bool, plannedPhysical ...string) error {
	target, route, err := m.ExactMutationRoute()
	if err != nil {
		return err
	}
	probe := m.Runner
	// physicalObservation is true only when the #{socket_path} read below goes
	// through the exact absolute -S socket the resolved-route proof would read
	// it through. Only then may that proof take this value instead of reading it
	// again (see guardResolvedRuntimeMutationRouteObserved).
	physicalObservation := false
	if m.ExpectedSocketPath != "" {
		probe = m.MutationRunner(PlannedRuntimeMutation{Target: RuntimeMutationTarget{PhysicalSocket: m.ExpectedSocketPath}})
		physicalObservation = filepath.IsAbs(m.ExpectedSocketPath)
	}
	// With a captured server generation and that exact physical socket, the
	// whole proof is one tmux invocation: the socket path and every value the
	// rest of this proof compares are read together through the one runner the
	// separate reads would each have used, with no tmux call in between. A
	// failed sequence reports the socket-path read's text, the first read. Any
	// other route keeps a lone socket-path read, because what follows it (a
	// rebind or a route re-resolution) decides which runner reads the rest. So
	// does a nested resolved-route proof that will reuse this transaction's
	// earlier proof: it compares nothing past the socket path, so nothing more
	// is read.
	var folded *ResolvedRouteObservation
	var observedOut []byte
	var observeErr error
	if physicalObservation && m.RouteAuthority != nil && !m.ResolvedRouteProofWouldReuse(target, requireLogical) {
		authority := m.RouteAuthority
		if authority.Class == RuntimeMutationRouteApp && !requireLogical {
			// The app-route variant reads the generation and the app marker only.
			var sections []string
			sections, observeErr = ReadTmuxSequence(ctx, probe, RouteReadSocketPath, RouteReadServerPID, RouteReadAppMarker)
			if observeErr == nil {
				folded = &ResolvedRouteObservation{Socket: sections[0], Pid: sections[1], App: sections[2]}
			}
		} else {
			var read ResolvedRouteObservation
			if read, observeErr = ReadResolvedRouteObservation(ctx, probe, authority); observeErr == nil {
				folded = &read
			}
		}
		if observeErr == nil {
			observedOut = []byte(folded.Socket)
		}
	} else {
		observedOut, observeErr = probe.Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	}
	if observeErr != nil {
		if allowNoServer && m.ExpectedSocketPath == "" && m.RouteAuthority == nil && inttmux.IsNoServerFailure(observeErr) {
			return nil
		}
		return fmt.Errorf("reobserve exact tmux route %s: %w", route, observeErr)
	}
	if len(plannedPhysical) == 1 && strings.TrimSpace(plannedPhysical[0]) == RuntimeMutationSocketAbsentBeforeCreate {
		return errors.New("materializer absent-before-create server appeared after planning")
	}
	observed := strings.TrimSpace(string(observedOut))
	if !filepath.IsAbs(observed) || filepath.Clean(observed) != observed {
		return fmt.Errorf("reobserve exact tmux route %s: socket path %q is not absolute and clean", route, observed)
	}
	if m.ExpectedSocketPath == "" {
		// A create-session plan may begin with no server. The first successful
		// observation after creation binds the immutable physical identity; no
		// -L basename inference participates.
		m.ExpectedSocketPath = observed
	}
	if m.ExpectedSocketPath != "" && observed != filepath.Clean(m.ExpectedSocketPath) {
		return fmt.Errorf("tmux socket drifted: observed %q, planned invocation %q", observed, filepath.Clean(m.ExpectedSocketPath))
	}
	// Once the logical declaration has resolved to a physical server, every
	// ownership read must use that exact physical authority. Reusing the -L
	// probe here would permit an alias replacement between socket observation
	// and the app/logical marker reads.
	probe = m.RunnerAtPhysicalSocket(m.ExpectedSocketPath)
	if m.RouteAuthority == nil && requireLogical {
		bound, err := ResolveExistingRuntimeMutationRoute(ctx, m.BaseRunner(), target, nil)
		if err != nil {
			return err
		}
		if bound.ExpectedSocketPath != m.ExpectedSocketPath || bound.Authority == nil {
			return errors.New("materializer exact route has no server-generation authority")
		}
		// Re-resolving the route rebinds the server generation this materializer
		// speaks for, so every earlier proof is void.
		m.InvalidateRouteIdentity("route-re-resolution")
		// The re-resolution issued its own reads after the socket observation
		// above, so no observation stands in for the nested proof.
		folded = nil
		m.Target = bound.Target
		target = bound.Target
		m.SocketName = bound.SocketName
		m.RouteAuthority = bound.Authority
	}
	if m.RouteAuthority != nil {
		if m.RouteAuthority.Class == RuntimeMutationRouteApp && !requireLogical {
			pid, owned := "", ""
			if folded != nil {
				pid, owned = folded.Pid, folded.App
			} else {
				// One invocation through probe; a failed sequence reports the
				// generation read's text, the first read.
				sections, err := ReadTmuxSequence(ctx, probe, RouteReadServerPID, RouteReadAppMarker)
				if err != nil {
					return errors.New("runtime mutation route: exact app server generation drifted")
				}
				pid, owned = sections[0], sections[1]
			}
			if strings.TrimSpace(pid) != m.RouteAuthority.ServerPID {
				return errors.New("runtime mutation route: exact app server generation drifted")
			}
			if strings.TrimSpace(owned) != "1" {
				return errors.New("runtime mutation route: exact server is not app-owned")
			}
			return nil
		}
		// When the socket path was read above through the same exact physical
		// socket, the same invocation also read the server generation and both
		// markers, and the nested proof takes them all. Otherwise it reads its
		// whole set itself, in one invocation.
		return GuardResolvedRuntimeMutationRouteObserved(ctx, m.BaseRunner(), RuntimeMutationRoute{
			Target: target, ExpectedSocketPath: m.ExpectedSocketPath,
			SocketName: m.LogicalSocketName(target), Authority: m.RouteAuthority,
		}, false, m.RouteIdentity, folded)
	}
	if requireLogical {
		if err := GuardRuntimeMutationServerOwnership(ctx, probe, target); err != nil {
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
	switch target.Flag() {
	case "-S":
		if observed != target.Value {
			return fmt.Errorf("tmux socket drifted: observed %q, planned %q", observed, target.Value)
		}
	case "-L":
		// The logical route is proved by expectedSocketPath, which was resolved
		// independently or bound at the first post-create observation.
	}
	return nil
}

// resolvedRouteProofWouldReuse reports whether the nested resolved-route proof
// proveExactRouteOwnership hands off to would answer from the identity cache.
// The app-route variant has no nested proof.
func (m *MaterializeRuntime) ResolvedRouteProofWouldReuse(target TmuxTransport, requireLogical bool) bool {
	if m.RouteAuthority.Class == RuntimeMutationRouteApp && !requireLogical {
		return false
	}
	key, cacheable := RuntimeRouteIdentityKeyForRoute(RouteIdentityScopeResolvedRoute, RuntimeMutationRoute{
		Target: target, ExpectedSocketPath: m.ExpectedSocketPath,
		SocketName: m.LogicalSocketName(target), Authority: m.RouteAuthority,
	})
	return cacheable && m.RouteIdentity.WouldReuse(key)
}
func (m *MaterializeRuntime) LogicalSocketName(target TmuxTransport) string {
	if strings.TrimSpace(m.SocketName) != "" {
		return strings.TrimSpace(m.SocketName)
	}
	if target.Flag() == "-L" {
		return target.Value
	}
	return ""
}
func MaterializeMutationAction(kind RuntimeMutationVerb, target RuntimeMutationTarget, guard, _ string, args ...string) PlannedRuntimeMutation {
	action := NewRuntimeMutation(1, kind, target)
	BindRuntimeMutationGuard(&action, guard)
	action.Operands = slices.Clone(args)
	return action
}
func (m *MaterializeRuntime) RunMutation(ctx context.Context, action PlannedRuntimeMutation) ([]byte, error) {
	var output []byte
	err := ExecuteRuntimeMutationPlan(ctx, m.GuardedWriteSteps([]RuntimeMutationStep{{
		Action:           action,
		TargetRouteGuard: m.TargetRouteGuard(action),
		Reobserve:        func(ctx context.Context) (bool, error) { return m.ObserveMutationEffect(ctx, action) },
		Guard: func(ctx context.Context) error {
			if err := m.GuardExactRoute(ctx, false, action.Target.PhysicalSocket); err != nil {
				return err
			}
			err := m.GuardMutationAction(ctx, action)
			if action.Verb != MutationKillOwned {
				return err
			}
			// An owned kill's goal is the exact object's absence; a concurrent
			// removal after Reobserve is converged, not drift.
			return RuntimeMutationAbsenceConverged(ctx, err, func(ctx context.Context) (bool, error) {
				return m.ObserveMutationEffect(ctx, action)
			})
		},
		Apply: func(ctx context.Context) error {
			var err error
			output, err = RunRuntimeMutationCommand(ctx, m.MutationRunner(action), action)
			return err
		},
	}}))
	return output, err
}
func (m *MaterializeRuntime) RunMaterializeMutation(ctx context.Context, action PlannedRuntimeMutation, guard, execute func() error, observer ...func(context.Context) (bool, error)) error {
	return ExecuteRuntimeMutationPlan(ctx, m.GuardedWriteSteps([]RuntimeMutationStep{{
		Action:           action,
		TargetRouteGuard: m.TargetRouteGuard(action),
		Reobserve: func(ctx context.Context) (bool, error) {
			observed, supported, err := m.ObserveMaterializeMutationEffect(ctx, action)
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
				if MaterializeEffectRequiresInvariant(action.Verb) {
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
			if err := m.GuardExactRoute(ctx, action.Verb == MutationCreateSession, action.Target.PhysicalSocket); err != nil {
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
	}}))
}
func MaterializeEffectRequiresInvariant(verb RuntimeMutationVerb) bool {
	switch verb {
	case MutationCreateSession, MutationFinalizeSession, MutationWriteProjectAnchor, MutationRenameWindow:
		return true
	default:
		return false
	}
}
func (m *MaterializeRuntime) ObserveMutationEffect(ctx context.Context, action PlannedRuntimeMutation) (bool, error) {
	observed, supported, err := m.ObserveMaterializeMutationEffect(ctx, action)
	if err != nil {
		return false, err
	}
	if !supported {
		return false, fmt.Errorf("materializer has no effect observer for %q", action.Verb)
	}
	return observed, nil
}
func (m *MaterializeRuntime) ObserveMaterializeMutationEffect(ctx context.Context, action PlannedRuntimeMutation) (bool, bool, error) {
	if action.Target.PhysicalSocket != RuntimeMutationSocketAbsentBeforeCreate {
		if err := m.GuardExactRoute(ctx, false, action.Target.PhysicalSocket); err != nil {
			return false, true, err
		}
	}
	switch action.Verb {
	case MutationCreateSession:
		exists, err := m.Sessions.SessionExists(ctx, action.Target.ID)
		if err != nil || !exists {
			return false, true, err
		}
		// The create declaration may print absent-before-create, but its first
		// successful post-effect observation must bind and prove the physical
		// app-owned socket before any follow-up plan row can be constructed.
		if err := m.GuardExactAppRoute(ctx, false); err != nil {
			return false, true, err
		}
		marker := ""
		for i := 0; i+1 < len(action.Operands); i++ {
			if action.Operands[i] == "-e" && strings.HasPrefix(action.Operands[i+1], CreateOperationEnvironment+"=") {
				marker = strings.TrimPrefix(action.Operands[i+1], CreateOperationEnvironment+"=")
			}
		}
		out, err := m.MutationRunner(PlannedRuntimeMutation{Target: m.BoundMutationTarget("session", action.Target.ID, action.Target.UID)}).Run(ctx, "tmux", "show-environment", "-t", action.Target.ID)
		return err == nil && marker != "" && SessionEnvironmentValue(string(out), CreateOperationEnvironment) == marker, true, err
	case MutationFinalizeSession:
		out, err := m.RoutedRunner().Run(ctx, "tmux", "show-environment", "-t", action.Target.ID)
		if err != nil {
			return false, true, err
		}
		want := ""
		if len(action.Operands) > 0 {
			want = action.Operands[len(action.Operands)-1]
		}
		return want != "" && SessionEnvironmentValue(string(out), FinalizeOperationEnvironment) == want, true, nil
	case MutationWriteIdentity, MutationWriteProjectAnchor, MutationTombstonePane, MutationRestorePane:
		option, want, unset, err := RuntimeMutationOptionEffect(action)
		if err != nil {
			return false, true, err
		}
		got, err := m.ReadMutationEffectOption(ctx, action.Target.ID, option)
		if err != nil {
			return false, true, err
		}
		if action.Verb == MutationWriteIdentity {
			matches, err := m.ObserveMaterializeIdentityTarget(ctx, action.Target)
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
		return MaterializeOptionEffectObserved(option, want, got), true, nil
	case MutationRenameWindow:
		if len(action.Operands) == 0 {
			return false, true, errors.New("rename effect operands are incomplete")
		}
		matches, err := m.ObserveMaterializeIdentityTarget(ctx, action.Target)
		if err != nil {
			return false, true, err
		}
		if !matches {
			return false, true, nil
		}
		got, err := m.ReadMutationEffectOption(ctx, action.Target.ID, "window_name")
		return got == action.Operands[len(action.Operands)-1], true, err
	case MutationWriteLease:
		if len(action.Operands) < 2 {
			return false, true, errors.New("lease effect operands are incomplete")
		}
		out, err := m.RoutedRunner().Run(ctx, "tmux", "show-environment", "-t", action.Target.ID)
		return err == nil && SessionEnvironmentValue(string(out), action.Operands[len(action.Operands)-2]) == action.Operands[len(action.Operands)-1], true, err
	case MutationClearLease:
		out, err := m.RoutedRunner().Run(ctx, "tmux", "show-environment", "-t", action.Target.ID)
		if err != nil {
			return false, true, err
		}
		if len(action.Operands) == 0 {
			return false, true, errors.New("clear-lease effect operands are incomplete")
		}
		got := SessionEnvironmentValue(string(out), action.Operands[len(action.Operands)-1])
		if action.LeaseMarker != "" {
			// An owner-checked clear is done once the variable no longer holds
			// our marker: absent, or another operation's lease it must keep.
			return got != action.LeaseMarker, true, nil
		}
		return got == "", true, nil
	case MutationWriteLayout:
		if len(action.Operands) < 4 {
			return false, true, errors.New("layout effect operands are incomplete")
		}
		format := "#{pane_width}"
		if action.Operands[len(action.Operands)-2] == "-y" {
			format = "#{pane_height}"
		}
		got, err := m.ReadTmux(ctx, "display-message", "-p", "-t", action.Target.ID, "-F", format)
		if err != nil {
			return false, true, fmt.Errorf("reobserve mutation effect %s on %s: %w", format, action.Target.ID, err)
		}
		return got == action.Operands[len(action.Operands)-1], true, nil
	case MutationKillOwned:
		// Each kind carries its own argv, flags included. -a widens
		// list-windows and list-panes past the current session, but
		// list-sessions is already server wide and rejects -a outright, so a
		// flag shared across the three kinds failed every session reobserve:
		// rollback then stopped on an unknown observation before its kill and
		// left the created session on the server it had just started.
		var out []byte
		var err error
		runner := m.RoutedRunner()
		switch action.Target.Kind {
		case "window":
			out, err = runner.Run(ctx, "tmux", "list-windows", "-a", "-F", "#{window_id}")
		case "pane":
			out, err = runner.Run(ctx, "tmux", "list-panes", "-a", "-F", "#{pane_id}")
		case "session":
			out, err = runner.Run(ctx, "tmux", "list-sessions", "-F", "#{session_id}")
		default:
			return false, true, errors.New("owned kill effect has an unknown target kind")
		}
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
func MaterializeOptionEffectObserved(option, want, got string) bool {
	if option != tmuxopts.AutomaticRenameWindow && option != tmuxopts.RemainOnExitPane {
		return got == want
	}
	wantValue, wantOK := ExactTmuxBoolean(want)
	gotValue, gotOK := ExactTmuxBoolean(got)
	return wantOK && gotOK && wantValue == gotValue
}
func ExactTmuxBoolean(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "on", "yes", "true":
		return true, true
	case "0", "off", "no", "false":
		return false, true
	default:
		return false, false
	}
}
func RuntimeMutationOptionEffect(action PlannedRuntimeMutation) (option, want string, unset bool, err error) {
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
func (m *MaterializeRuntime) ReadMutationEffectOption(ctx context.Context, target, option string) (string, error) {
	got, err := m.ReadTmux(ctx, "display-message", "-p", "-t", target, "-F", "#{"+option+"}")
	if err != nil {
		return "", fmt.Errorf("reobserve mutation effect option %s on %s: %w", option, target, err)
	}
	return got, nil
}
func MaterializeIdentityOwnershipOption(kind string) (string, error) {
	switch RuntimeObjectKind(kind) {
	case RuntimeSession:
		return tmuxopts.ProjectUIDSession, nil
	case RuntimeWindow:
		return tmuxopts.WindowUID, nil
	case RuntimePane:
		return tmuxopts.PaneUID, nil
	default:
		return "", fmt.Errorf("identity effect has unsupported target kind %q", kind)
	}
}

type MaterializeIdentityObservation struct {
	Uid    string
	Parent string
}

func (m *MaterializeRuntime) ReadMaterializeIdentityObservation(ctx context.Context, kind, target string) (MaterializeIdentityObservation, error) {
	ownershipOption, err := MaterializeIdentityOwnershipOption(kind)
	if err != nil {
		return MaterializeIdentityObservation{}, err
	}
	var format string
	switch RuntimeObjectKind(kind) {
	case RuntimeSession:
		format = TmuxRowFormat("#{session_id}", "#{"+ownershipOption+"}", "#{"+tmuxopts.SessionRole+"}")
	case RuntimeWindow:
		format = TmuxRowFormat("#{session_id}", "#{window_id}", "#{"+ownershipOption+"}",
			"#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.SessionRole+"}")
	case RuntimePane:
		format = TmuxRowFormat("#{session_id}", "#{window_id}", "#{pane_id}", "#{"+ownershipOption+"}",
			"#{"+tmuxopts.WindowUID+"}", "#{"+tmuxopts.ProjectUIDSession+"}", "#{"+tmuxopts.SessionRole+"}")
	}
	out, err := m.ReadTmux(ctx, "display-message", "-p", "-t", target, "-F", format)
	if err != nil {
		return MaterializeIdentityObservation{}, fmt.Errorf("observe exact %s identity target %s: %w", kind, target, err)
	}
	switch RuntimeObjectKind(kind) {
	case RuntimeSession:
		rows := SplitTmuxRows(out, 3)
		if len(rows) != 1 || rows[0][0] != target || ExactTmuxHandle(rows[0][0], "$") == "" {
			return MaterializeIdentityObservation{}, errors.New("session identity target containment is unavailable")
		}
		return MaterializeIdentityObservation{Uid: rows[0][1], Parent: "session-role=" + rows[0][2]}, nil
	case RuntimeWindow:
		rows := SplitTmuxRows(out, 5)
		if len(rows) != 1 || ExactTmuxHandle(rows[0][0], "$") == "" || rows[0][1] != target || ExactTmuxHandle(rows[0][1], "@") == "" {
			return MaterializeIdentityObservation{}, errors.New("window identity target containment is unavailable")
		}
		return MaterializeIdentityObservation{Uid: rows[0][2], Parent: rows[0][0] + "/root=" + rows[0][3] + "/role=" + rows[0][4]}, nil
	case RuntimePane:
		rows := SplitTmuxRows(out, 7)
		if len(rows) != 1 || ExactTmuxHandle(rows[0][0], "$") == "" || ExactTmuxHandle(rows[0][1], "@") == "" ||
			rows[0][2] != target || ExactTmuxHandle(rows[0][2], "%") == "" || strings.TrimSpace(rows[0][4]) == "" {
			return MaterializeIdentityObservation{}, errors.New("pane identity target containment is unavailable")
		}
		return MaterializeIdentityObservation{Uid: rows[0][3], Parent: rows[0][0] + "/" + rows[0][1] +
			"/window-uid=" + rows[0][4] + "/root=" + rows[0][5] + "/role=" + rows[0][6]}, nil
	default:
		return MaterializeIdentityObservation{}, fmt.Errorf("identity effect has unsupported target kind %q", kind)
	}
}

// identityObservation reads one target's identity bundle through the memo of
// the open identity-write plan, if any. forGuard marks the read that brackets a
// write; see identityObservationMemo.
func (m *MaterializeRuntime) IdentityObservation(ctx context.Context, kind, target string, forGuard bool) (MaterializeIdentityObservation, error) {
	key := kind + "\x00" + target
	if memo := m.IdentityMemo; memo != nil {
		if entry, ok := memo.Entries[key]; ok && (!forGuard || entry.Guarded) {
			return entry.Observation, nil
		}
	}
	observation, err := m.ReadMaterializeIdentityObservation(ctx, kind, target)
	if memo := m.IdentityMemo; memo != nil {
		if err != nil {
			delete(memo.Entries, key)
		} else {
			memo.Entries[key] = IdentityObservationEntry{Observation: observation, Guarded: forGuard}
		}
	}
	return observation, err
}
func (m *MaterializeRuntime) BindMaterializeIdentityTarget(ctx context.Context, kind, id, uid string) (RuntimeMutationTarget, error) {
	observation, err := m.IdentityObservation(ctx, kind, id, false)
	if err != nil {
		return RuntimeMutationTarget{}, err
	}
	if observation.Uid != "" && observation.Uid != uid {
		return RuntimeMutationTarget{}, fmt.Errorf("%s %s carries foreign uid %q, want %q", kind, id, observation.Uid, uid)
	}
	target := m.BoundMutationTarget(kind, id, uid)
	target.Parent = observation.Parent
	return target, nil
}
func (m *MaterializeRuntime) ObserveMaterializeIdentityTarget(ctx context.Context, target RuntimeMutationTarget) (bool, error) {
	observation, err := m.IdentityObservation(ctx, target.Kind, target.ID, false)
	if err != nil {
		return false, err
	}
	if observation.Parent != target.Parent {
		return false, fmt.Errorf("%s %s identity parent drifted: observed %q, planned %q", target.Kind, target.ID, observation.Parent, target.Parent)
	}
	if observation.Uid == "" {
		return false, nil
	}
	if observation.Uid != target.UID {
		return false, fmt.Errorf("%s %s carries foreign uid %q, want %q", target.Kind, target.ID, observation.Uid, target.UID)
	}
	return true, nil
}
func (m *MaterializeRuntime) GuardMaterializeIdentityTarget(ctx context.Context, target RuntimeMutationTarget, allowBlankUID bool) error {
	observation, err := m.IdentityObservation(ctx, target.Kind, target.ID, true)
	if err != nil {
		return err
	}
	if observation.Parent != target.Parent {
		return fmt.Errorf("%s %s identity parent drifted: observed %q, planned %q", target.Kind, target.ID, observation.Parent, target.Parent)
	}
	if observation.Uid == "" && allowBlankUID {
		return nil
	}
	if observation.Uid != target.UID {
		return fmt.Errorf("%s uid is %q, want %q", target.Kind, observation.Uid, target.UID)
	}
	return nil
}
func (m *MaterializeRuntime) GuardMutationAction(ctx context.Context, action PlannedRuntimeMutation) error {
	switch action.Verb {
	case MutationKillOwned:
		entry := RuntimeObject{Kind: RuntimeObjectKind(action.Target.Kind), ID: action.Target.ID, UID: action.Target.UID}
		got, err := m.ReadTmux(ctx, "display-message", "-p", "-t", entry.ID, "-F", "#{"+entry.OwnershipOption()+"}")
		if err != nil {
			return err
		}
		if got != entry.UID {
			return fmt.Errorf("ownership uid is %q, want %q", got, entry.UID)
		}
	case MutationWriteIdentity:
		return m.GuardMaterializeIdentityTarget(ctx, action.Target, true)
	case MutationCreateWindow:
		got, err := m.ReadTmux(ctx, "display-message", "-p", "-t", action.Target.ID, "-F", "#{session_id}")
		if err != nil {
			return err
		}
		if ExactTmuxHandle(got, "$") == "" {
			return fmt.Errorf("session target %q is no longer exact", action.Target.ID)
		}
	case MutationWriteLease:
		got, err := m.ReadTmux(ctx, "display-message", "-p", "-t", action.Target.ID, "-F", "#{session_id}")
		if err != nil {
			return err
		}
		if got != action.Target.ID {
			return fmt.Errorf("session identity drifted: got %q, want id=%s", got, action.Target.ID)
		}
	case MutationCreatePane, MutationWriteLayout:
		got, err := m.ReadTmux(ctx, "display-message", "-p", "-t", action.Target.ID, "-F", "#{pane_id}")
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
func (m *MaterializeRuntime) Option(ctx context.Context, target, format string) string {
	out, err := m.ReadTmux(ctx, "display-message", "-p", "-t", target, "-F", format)
	if err != nil {
		return ""
	}
	return out
}

// rollback removes, in reverse creation order, only the tmux objects this
// operation created that still carry the uid it mirrored onto them.
func (m *MaterializeRuntime) Rollback(ctx context.Context, ledger *RuntimeLedger) {
	entries := ledger.Entries()
	var steps []RuntimeMutationStep
	// serverEnded is set only once the server the plan guarded is proven ended
	// after a kill of this plan succeeded (see rollbackGuardedServerEnded).
	// Every ledger object lived on that server, so each later step's target
	// ended with it: such a step sends that socket nothing more and converges
	// as absent. ownKilled records that some kill of this plan succeeded; a
	// server that ended before that keeps every step's own verdict.
	serverEnded, ownKilled := false, false
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		got, err := m.ReadTmux(ctx, "display-message", "-p", "-t", entry.ID, "-F", "#{"+entry.OwnershipOption()+"}")
		if err != nil {
			// The object is already gone, so the desired rollback state holds.
			continue
		}
		if got != entry.UID {
			// The object still exists but no longer belongs to this operation.
			// Preserve it and make the residual drift explicit.
			if m.Warn != nil {
				fmt.Fprintf(m.Warn, "projmux: rollback preserved %s %s because its ownership uid is %q, want %q\n",
					entry.Kind, entry.ID, got, entry.UID)
			}
			continue
		}
		action := MaterializeMutationAction(MutationKillOwned,
			m.BoundMutationTarget(string(entry.Kind), entry.ID, entry.UID),
			"same mirrored ownership uid="+entry.UID,
			"owned created "+string(entry.Kind)+" is absent",
			"-t", entry.ID)
		action.Order = len(steps) + 1
		// killed is set only by this step's own successful kill, never by the
		// Apply fallback below that explains a failed kill away.
		killed := false
		steps = append(steps, RuntimeMutationStep{
			Action:           action,
			TargetRouteGuard: m.TargetRouteGuard(action),
			Reobserve: func(ctx context.Context) (bool, error) {
				if serverEnded {
					return true, nil
				}
				absent, err := m.ObserveMutationEffect(ctx, action)
				// Killing the server's last session ends the server, so the
				// post-write route read of the plan's own socket answers with
				// a teardown response instead of a listing. After this step's
				// own kill succeeded, that ended server is the absence it
				// wanted. The pre-write observation (killed is false) and any
				// other failure keep their verdict.
				if err != nil && killed && TmuxServerTeardownFailure(err) &&
					filepath.Clean(action.Target.PhysicalSocket) == filepath.Clean(m.ExpectedSocketPath) {
					return true, nil
				}
				return absent, err
			},
			Guard: func(ctx context.Context) error {
				if err := m.GuardExactRoute(ctx, false, action.Target.PhysicalSocket); err != nil {
					return err
				}
				got, err := m.ReadTmux(ctx, "display-message", "-p", "-t", entry.ID, "-F", "#{"+entry.OwnershipOption()+"}")
				if err == nil && got != entry.UID {
					err = fmt.Errorf("ownership uid is %q, want %q", got, entry.UID)
				}
				// A concurrent writer may remove the owned object after
				// Reobserve; its absence is the rollback's goal, not drift.
				return RuntimeMutationAbsenceConverged(ctx, err, func(ctx context.Context) (bool, error) {
					return m.ObserveMutationEffect(ctx, action)
				})
			},
			Apply: func(ctx context.Context) error {
				if serverEnded {
					return nil
				}
				_, err := RunRuntimeMutationCommand(ctx, m.Runner, action)
				if err == nil {
					killed, ownKilled = true, true
					serverEnded = m.RollbackGuardedServerEnded(ctx, action)
					return nil
				}
				// The read after an earlier own kill can still reach a server
				// that is ending, and only this kill then meets the teardown.
				// The same proof, read now, settles it.
				if ownKilled && TmuxServerTeardownFailure(err) && m.RollbackGuardedServerEnded(ctx, action) {
					serverEnded = true
					return nil
				}
				// Every guard ran before the first kill, so an earlier kill of
				// this same plan can remove this object first: tmux removes a
				// Window with its last Pane, and a session with its last
				// Window. Its own effect observer then proves the desired
				// absence, and the plan goes on to the rest of the ledger. A
				// failure the observer cannot explain away -- the object is
				// still listed, or the route itself cannot be read -- stops the
				// plan as before.
				if absent, observeErr := m.ObserveMutationEffect(ctx, action); observeErr == nil && absent {
					return nil
				}
				return err
			},
		})
	}
	if err := ExecuteRuntimeMutationPlan(ctx, m.GuardedWriteSteps(steps)); err != nil && m.Warn != nil {
		fmt.Fprintf(m.Warn, "projmux: rollback stopped before an unguarded runtime write: %v\n", err)
	}
}

// rollbackGuardedServerEnded reports whether the server the rollback plan
// guarded has ended. Callers ask only after a kill of this plan succeeded.
// Every guard, each kill's own server generation check included, ran before
// the first write, so the proof is a read of that exact physical socket
// answering with a teardown response instead of a server. A live server,
// another socket, or any other failure is no proof.
func (m *MaterializeRuntime) RollbackGuardedServerEnded(ctx context.Context, action PlannedRuntimeMutation) bool {
	if m.RouteAuthority == nil || m.ExpectedSocketPath == "" ||
		filepath.Clean(action.Target.PhysicalSocket) != filepath.Clean(m.ExpectedSocketPath) {
		return false
	}
	_, err := m.RunnerAtPhysicalSocket(m.ExpectedSocketPath).Run(ctx, "tmux", "display-message", "-p", "-F", "#{socket_path}")
	return err != nil && TmuxServerTeardownFailure(err)
}

// tmuxServerTeardownFailure is the closed set of tmux responses for a server
// that is ending or has ended: a recognized no-server refusal (teardown
// finished) or a typed exit whose stderr is exactly "server exited
// unexpectedly" (teardown in progress). Any other failure, typed or not, is not
// a teardown.
func TmuxServerTeardownFailure(err error) bool {
	if inttmux.IsNoServerFailure(err) {
		return true
	}
	var carrier interface{ CommandFailure() inttmux.CommandFailure }
	if !errors.As(err, &carrier) {
		return false
	}
	failure := carrier.CommandFailure()
	return failure.Kind == inttmux.CommandFailureExit && strings.TrimSpace(failure.Stderr) == "server exited unexpectedly"
}
func (m *MaterializeRuntime) ClaimRuntimeUID(ctx context.Context, kind RuntimeObjectKind, target, uid string) (bool, error) {
	ownershipOption := RuntimeObject{Kind: kind}.OwnershipOption()
	var args []string
	switch kind {
	case RuntimeWindow:
		args = append(args, "-w")
	case RuntimePane:
		args = append(args, "-p")
	}
	args = append(args, "-t", target, "-q", ownershipOption, uid)
	closeObservations := m.ScopeIdentityObservations()
	defer closeObservations()
	stable, err := m.BindMaterializeIdentityTarget(ctx, string(kind), target, uid)
	if err != nil {
		return false, err
	}
	action := MaterializeMutationAction(MutationWriteIdentity,
		stable,
		"exact newly attributed "+string(kind)+" handle",
		"Registry uid is mirrored for rollback ownership",
		args...)
	if _, err := m.RunMutation(ctx, action); err != nil {
		claimErr := fmt.Errorf("claim created tmux %s %s: %w", kind, target, err)
		if got := m.Option(ctx, target, "#{"+ownershipOption+"}"); got == uid {
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
func (m *MaterializeRuntime) ClaimRuntimeUIDForRollback(ctx context.Context, kind RuntimeObjectKind, target, uid string, ledger *RuntimeLedger) error {
	claimed, err := m.ClaimRuntimeUID(ctx, kind, target, uid)
	if claimed {
		ledger.Record(kind, target, uid)
	}
	if err != nil && !claimed {
		m.WarnUnclaimedHandle(kind, target)
	}
	return err
}
func (m *MaterializeRuntime) MirrorPaneIdentity(ctx context.Context, target string, pane coremetadata.Pane) error {
	if pane.Metadata.OwnerRef == nil {
		return fmt.Errorf("mirror Pane %s: stable ownerRef is required", pane.Metadata.UID)
	}
	writes := []IdentityPlanWrite{
		{Operands: []string{"-p", "-t", target, "-q", tmuxopts.PaneUID, pane.Metadata.UID}, Effect: "Pane UID mirror equals Registry"},
		{Operands: []string{"-p", "-t", target, "-q", tmuxopts.PaneName, pane.Metadata.Name}, Effect: "Pane stable-name mirror equals Registry"},
		{Operands: []string{"-p", "-t", target, "-q", tmuxopts.PaneOwnerKind, string(pane.Metadata.OwnerRef.Kind)}, Effect: "Pane stable owner kind equals Registry"},
		{Operands: []string{"-p", "-t", target, "-q", tmuxopts.PaneOwnerUID, pane.Metadata.OwnerRef.UID}, Effect: "Pane stable owner uid equals Registry"},
		{Operands: []string{"-p", "-t", target, "-q", tmuxopts.PaneRole, string(pane.Spec.Role)}, Effect: "Pane stable role equals Registry"},
	}
	if pane.Spec.Role == coremetadata.PaneRoleAgent {
		writes = append(writes, IdentityPlanWrite{
			Operands: []string{"-p", "-t", target, "-q", tmuxopts.AgentUIDPane, pane.Metadata.OwnerRef.UID},
			Effect:   "Pane Agent uid equals Registry owner",
		}, IdentityPlanWrite{
			Operands: []string{"-p", "-t", target, tmuxopts.RemainOnExitPane, "on"},
			Effect:   "managed Agent Pane remains as an exact dead anchor until lifecycle replacement",
		})
	}
	return m.RunIdentityWrites(ctx, "pane", target, pane.Metadata.UID, writes)
}

type IdentityPlanWrite struct {
	Verb     RuntimeMutationVerb
	Operands []string
	Effect   string
}

func (m *MaterializeRuntime) RunIdentityWrites(ctx context.Context, kind, target, uid string, writes []IdentityPlanWrite) error {
	defer m.ScopeIdentityObservations()()
	stable, err := m.BindMaterializeIdentityTarget(ctx, kind, target, uid)
	if err != nil {
		return err
	}
	steps := make([]RuntimeMutationStep, 0, len(writes))
	for index, write := range writes {
		verb := write.Verb
		if verb == "" {
			verb = MutationWriteIdentity
		}
		action := NewRuntimeMutation(index+1, verb, stable)
		BindRuntimeMutationGuard(&action, "exact "+kind+" carries claimed uid="+uid+" before "+write.Effect)
		action.Operands = slices.Clone(write.Operands)
		steps = append(steps, RuntimeMutationStep{
			Action:           action,
			TargetRouteGuard: m.TargetRouteGuard(action),
			Reobserve:        func(ctx context.Context) (bool, error) { return m.ObserveMutationEffect(ctx, action) },
			Guard: func(ctx context.Context) error {
				if err := m.GuardExactRoute(ctx, false, action.Target.PhysicalSocket); err != nil {
					return err
				}
				return m.GuardMaterializeIdentityTarget(ctx, action.Target, false)
			},
			Apply: func(ctx context.Context) error {
				_, err := RunRuntimeMutationCommand(ctx, m.Runner, action)
				return err
			},
		})
	}
	return ExecuteRuntimeMutationPlan(ctx, m.GuardedWriteSteps(steps))
}
func (m *MaterializeRuntime) MarkCreateOperation(ctx context.Context, sessionName string, ledger *RuntimeLedger) error {
	if ledger == nil || strings.TrimSpace(ledger.OperationMarker) == "" {
		return errors.New("materialize tmux session: create-operation lease is missing")
	}
	if err := m.GuardExactRoute(ctx, false); err != nil {
		return fmt.Errorf("mark tmux session %q for create operation: %w", sessionName, err)
	}
	action := MaterializeMutationAction(MutationWriteLease,
		m.BoundMutationTarget("session", sessionName, "session:"+sessionName),
		"exact managed session="+sessionName,
		"operation lease is installed",
		"-t", sessionName, CreateOperationEnvironment, ledger.OperationMarker)
	if _, err := m.RunMutation(ctx, action); err != nil {
		return fmt.Errorf("mark tmux session %q for create operation: %w", sessionName, err)
	}
	ledger.MarkSession(sessionName)
	return nil
}
func (m *MaterializeRuntime) ClearCreateOperations(ctx context.Context, ledger *RuntimeLedger) {
	if ledger == nil {
		return
	}
	var steps []RuntimeMutationStep
	// wrote turns true at the plan's first Apply. Until then, the lease read
	// below is the latest observation of each session's environment and no
	// write of ours has followed it; see Reobserve.
	wrote := false
	for _, sessionName := range ledger.MarkedSessionIDs {
		exists, err := m.Sessions.SessionExists(ctx, sessionName)
		if err != nil || !exists {
			continue
		}
		out, err := m.RoutedRunner().Run(ctx, "tmux", "show-environment", "-t", sessionName)
		if err != nil || SessionEnvironmentValue(string(out), CreateOperationEnvironment) != ledger.OperationMarker {
			continue
		}
		for _, environment := range []string{FinalizeOperationEnvironment, CreateOperationEnvironment} {
			if SessionEnvironmentValue(string(out), environment) != ledger.OperationMarker {
				continue
			}
			action := MaterializeMutationAction(MutationClearLease,
				m.BoundMutationTarget("session", sessionName, "session:"+sessionName),
				"same operation lease="+ledger.OperationMarker,
				"operation lease is absent",
				"-u", "-t", sessionName, environment)
			// This clear runs after the Registry lock is released, so the next
			// create may already own this variable. The lease comparison
			// therefore lives inside the single tmux write command, never in a
			// separate read before it.
			action.LeaseMarker = ledger.OperationMarker
			action.Order = len(steps) + 1
			steps = append(steps, RuntimeMutationStep{
				Action:           action,
				TargetRouteGuard: m.TargetRouteGuard(action),
				Reobserve: func(ctx context.Context) (bool, error) {
					if !wrote {
						// The lease read above found this exact environment
						// carrying our marker, and nothing of ours has written
						// since: the effect (our marker gone) is known pending.
						// The answer never declares an effect present, so it
						// cannot skip a write; the write itself compares the
						// lease again inside tmux.
						return false, nil
					}
					return m.ObserveMutationEffect(ctx, action)
				},
				Guard: func(ctx context.Context) error {
					// Only the route is guarded here. Another operation's
					// marker is a normal race, and the owner-checked clear
					// leaves it in place without a warning.
					return m.GuardExactRoute(ctx, false, action.Target.PhysicalSocket)
				},
				Apply: func(ctx context.Context) error {
					wrote = true
					_, err := RunRuntimeMutationCommand(ctx, m.MutationRunner(action), action)
					return err
				},
			})
		}
	}
	if err := ExecuteRuntimeMutationPlan(ctx, m.GuardedWriteSteps(steps)); err != nil && m.Warn != nil {
		fmt.Fprintf(m.Warn, "projmux: could not clear guarded create-operation lease(s): %v\n", err)
	}
}

type RuntimeOwner struct {
	SessionID string
	WindowID  string
}

func (m *MaterializeRuntime) WarnUnclaimedHandle(kind RuntimeObjectKind, id string) {
	if m.Warn == nil || strings.TrimSpace(id) == "" {
		return
	}
	fmt.Fprintf(m.Warn, "projmux: create could not claim tmux %s %s; preserved this exact residual handle for inspection before retry or cleanup\n", kind, id)
}

// splitPlacementFlag maps the closed placement enum onto its tmux split axis.
func SplitPlacementFlag(placement string) string {
	if placement == PlacementDown {
		return "-v"
	}
	return "-h"
}

// splitPane splits an anchor pane detached and returns the new pane id.
//
// `-d` is the whole point: tmux leaves the previously active pane active, so
// the split is a pure structural mutation with no focus side effect.
//
// processAnchor is the invocation's exact process target, or nil for a tmux
// anchor; a declared process anchor refuses the split before any tmux call.
func (m *MaterializeRuntime) SplitPane(ctx context.Context, anchorPaneID, placement, cwd string, command []string, processAnchor func(string) error) (string, error) {
	if err := processAnchor(anchorPaneID); err != nil {
		return "", err
	}
	before, beforeErr := m.RuntimeIDs(ctx, "list-panes", anchorPaneID, "#{pane_id}", "%")
	if beforeErr != nil {
		return "", fmt.Errorf("list tmux panes around %q before split: %w", anchorPaneID, beforeErr)
	}
	args := []string{"-d", "-P", "-F", "#{pane_id}", SplitPlacementFlag(placement), "-t", anchorPaneID}
	if strings.TrimSpace(cwd) != "" {
		args = append(args, "-c", cwd)
	}
	action := MaterializeMutationAction(MutationCreatePane,
		m.BoundMutationTarget("pane", anchorPaneID, "anchor:"+anchorPaneID),
		"exact owned anchor containment="+anchorPaneID,
		"one detached owned Pane exists",
		args...)
	action.Command = slices.Clone(command)
	var rawID []byte
	err := m.RunMaterializeMutation(ctx, action, func() error {
		current, err := m.RuntimeIDs(ctx, "list-panes", anchorPaneID, "#{pane_id}", "%")
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
		return m.GuardMutationAction(ctx, action)
	}, func() error {
		var err error
		rawID, err = RunRuntimeMutationCommand(ctx, m.Runner, action)
		return err
	}, func(ctx context.Context) (bool, error) {
		id := ExactTmuxHandle(strings.TrimSpace(string(rawID)), "%")
		if id == "" {
			return false, nil
		}
		current, err := m.RuntimeIDs(ctx, "list-panes", anchorPaneID, "#{pane_id}", "%")
		if err != nil {
			return false, err
		}
		return !before[id] && current[id], nil
	})
	id := strings.TrimSpace(string(rawID))
	if err != nil {
		after, listErr := m.RuntimeIDs(ctx, "list-panes", anchorPaneID, "#{pane_id}", "%")
		if listErr == nil {
			id = ErrorCreatedHandle(id, "%", before, after)
			if id == "" {
				m.WarnUnclaimedRuntime("pane", before, after)
			}
		} else {
			id = ""
		}
		return id, fmt.Errorf("split tmux pane %q: %w", anchorPaneID, err)
	}
	id = ExactTmuxHandle(id, "%")
	if id == "" {
		return "", fmt.Errorf("split tmux pane %q: tmux returned no pane id", anchorPaneID)
	}
	if before[id] {
		return "", fmt.Errorf("split tmux pane %q: tmux returned pre-existing pane id %s", anchorPaneID, id)
	}
	return id, nil
}
func (m *MaterializeRuntime) WarnUnclaimedRuntime(kind string, before, after map[string]bool) {
	if m.Warn == nil {
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
		fmt.Fprintf(m.Warn, "projmux: create failed with unclaimed tmux %s drift; preserved %s\n", kind, strings.Join(residual, ", "))
	}
}
func (m *MaterializeRuntime) RuntimeIDs(ctx context.Context, command, target, format, prefix string) (map[string]bool, error) {
	out, err := m.ReadTmux(ctx, command, "-t", target, "-F", format)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for line := range strings.SplitSeq(out, "\n") {
		if id := ExactTmuxHandle(line, prefix); id != "" {
			ids[id] = true
		}
	}
	return ids, nil
}
func ErrorCreatedHandle(output, prefix string, before, after map[string]bool) string {
	var candidate string
	for line := range strings.SplitSeq(output, "\n") {
		id := ExactTmuxHandle(line, prefix)
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

// requireCreatedPaneParent re-reads one freshly created Pane's exact parent
// tuple. A split is not attributed by a before/after owner diff the way
// new-window is, so the returned handle's parent is proven with its own exact
// observation before any uid claim or mirror is written to it.
func RequireCreatedPaneParent(ctx context.Context, runtime *MaterializeRuntime, paneID, sessionID, windowID string) error {
	out, err := runtime.ReadTmux(ctx, "display-message", "-p", "-t", paneID, "-F",
		TmuxRowFormat("#{session_id}", "#{window_id}"))
	if err != nil {
		return fmt.Errorf("inspect created tmux pane %s parent: %w", paneID, err)
	}
	rows := SplitTmuxRows(out, 2)
	if len(rows) != 1 || ExactTmuxHandle(rows[0][0], "$") == "" || ExactTmuxHandle(rows[0][1], "@") == "" {
		return fmt.Errorf("inspect created tmux pane %s parent: malformed row %q", paneID, strings.TrimSpace(out))
	}
	if rows[0][0] != sessionID || rows[0][1] != windowID {
		return fmt.Errorf("created tmux pane %s landed in %s/%s, want %s/%s",
			paneID, rows[0][0], rows[0][1], sessionID, windowID)
	}
	return nil
}

// adoptCreatedPane claims, ledgers, and mirrors one Pane this operation created
// after proving its parent. A Pane whose parent cannot be proven is left
// unclaimed and reported as an exact residual instead of being given this
// Project's identity in the wrong topology.
func AdoptCreatedPane(
	ctx context.Context,
	runtime *MaterializeRuntime,
	paneID, sessionID, windowID string,
	pane coremetadata.Pane,
	ledger *RuntimeLedger,
) error {
	if err := RequireCreatedPaneParent(ctx, runtime, paneID, sessionID, windowID); err != nil {
		runtime.WarnUnclaimedHandle(RuntimePane, paneID)
		return err
	}
	if err := runtime.ClaimRuntimeUIDForRollback(ctx, RuntimePane, paneID, pane.Metadata.UID, ledger); err != nil {
		return err
	}
	return runtime.MirrorPaneIdentity(ctx, paneID, pane)
}

func NewCreateOperationMarkerAt(operationID string, now time.Time) string {
	return fmt.Sprintf("v1:%d:%d:%s", os.Getpid(), now.Unix(), strings.TrimSpace(operationID))
}

const PlacementDown = "down"

func GuardRuntimeMutationServerOwnership(ctx context.Context, routed TmuxCommandRunner, target TmuxTransport) error {
	// Both markers are read in one tmux invocation through the same runner. A
	// failed sequence reports the error the first read (the app marker) gives.
	markers, err := ReadTmuxSequence(ctx, routed, RouteReadAppMarker, RouteReadSocketNameMarker)
	if err != nil {
		return fmt.Errorf("runtime mutation route: read app ownership marker: %w", err)
	}
	if strings.TrimSpace(markers[0]) != "1" {
		return errors.New("runtime mutation route: exact server is not app-owned")
	}
	logicalName := strings.TrimSpace(markers[1])
	if logicalName == "" {
		name := ""
		if target.Flag() == "-L" {
			name = target.Value
		}
		return MissingRuntimeMutationMarker(name)
	}
	if _, err := TmuxSocketNameTarget(logicalName); err != nil {
		return errors.New("runtime mutation route: app server logical marker is invalid")
	}
	if target.Flag() == "-L" && logicalName != target.Value {
		return fmt.Errorf("runtime mutation route: logical socket marker is %q, planned %q", logicalName, target.Value)
	}
	return nil
}

func GuardPrintedRuntimeMutationRouteWithIdentity(
	ctx context.Context,
	runner TmuxCommandRunner,
	route RuntimeMutationRoute,
	action PlannedRuntimeMutation,
	identity *RuntimeRouteIdentityCache,
) error {
	return GuardPrintedRuntimeMutationRouteWithMarkerPolicy(ctx, runner, route, action, false, identity)
}
