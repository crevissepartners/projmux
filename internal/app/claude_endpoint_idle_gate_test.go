package app

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	claudeadapter "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// claudeEndpointIdleProbe instruments one helper's idle Registry gate. Every
// field belongs to one test, so parallel tests never share gate inputs.
type claudeEndpointIdleProbe struct {
	// ticks counts idle gate stat calls: one per tick that passed the identity part.
	ticks atomic.Int64
	// clock is the fake clock offset in nanoseconds.
	clock  atomic.Int64
	poster chan *liveClaudeProviderPoster
}

func newClaudeEndpointIdleProbe(stat func(*intmetadata.Store) (intmetadata.RegistryFileIdentity, error), floor time.Duration) (*claudeEndpointIdleProbe, *claudeEndpointIdleOptions) {
	probe := &claudeEndpointIdleProbe{poster: make(chan *liveClaudeProviderPoster, 1)}
	base := time.Now()
	return probe, &claudeEndpointIdleOptions{
		// Counting before the stat makes a tick counted after an observation
		// also stat and read the clock after it.
		stat: func(store *intmetadata.Store) (intmetadata.RegistryFileIdentity, error) {
			probe.ticks.Add(1)
			return stat(store)
		},
		now:    func() time.Time { return base.Add(time.Duration(probe.clock.Load())) },
		floor:  floor,
		poster: func(poster *liveClaudeProviderPoster) { probe.poster <- poster },
	}
}

// awaitTicks waits for count more idle ticks and fails if the helper exits.
func (p *claudeEndpointIdleProbe) awaitTicks(t *testing.T, done <-chan error, count int64) {
	t.Helper()
	target := p.ticks.Load() + count
	deadline := time.Now().Add(4 * time.Second)
	for p.ticks.Load() < target {
		select {
		case <-done:
			t.Fatal("helper exited while the idle Registry gate held")
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("idle ticks did not advance")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-done:
		t.Fatal("helper exited while the idle Registry gate held")
	default:
	}
}

// startClaudeEndpointWithIdleProbe starts a real helper through the fixture
// and returns once its first idle tick has run the full Registry evaluation.
func startClaudeEndpointWithIdleProbe(t *testing.T, stat func(*intmetadata.Store) (intmetadata.RegistryFileIdentity, error), floor time.Duration) (*claudeEndpointTestFixture, *claudeEndpointIdleProbe, <-chan error, coremetadata.AgentRouteRef, *liveClaudeProviderPoster) {
	t.Helper()
	f := newClaudeEndpointTestFixture(t)
	probe, options := newClaudeEndpointIdleProbe(stat, floor)
	f.idle = options
	_, done := f.start(t)
	var poster *liveClaudeProviderPoster
	select {
	case poster = <-probe.poster:
	default:
		t.Fatal("helper acknowledged readiness before building its provider poster")
	}
	probe.awaitTicks(t, done, 2)
	route, reason := f.route(t)
	if reason != "" || !probeClaudeRegistrationLease(f.bootstrap.RegistryPath, route) {
		t.Fatal("exact registration not ready")
	}
	return f, probe, done, route, poster
}

// claudeEndpointHeldIdleStat is a constant stat identity: with a one-hour floor
// the idle tick never re-evaluates the Registry after its first evaluation.
func claudeEndpointHeldIdleStat(*intmetadata.Store) (intmetadata.RegistryFileIdentity, error) {
	return intmetadata.RegistryFileIdentity{Device: 1, Inode: 1}, nil
}

// replaceClaudeEndpointAuthority records a different lease process for the same
// Pane, generation, and registration generation through the normal store and
// mutator path, so the route still resolves but its authority changed.
func replaceClaudeEndpointAuthority(t *testing.T, f *claudeEndpointTestFixture) {
	t.Helper()
	leaseProcess, _, err := claudeadapter.Process(f.provider.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
		route, reason := coremetadata.ResolveAgentRoute(*reg, f.bootstrap.AgentUID)
		authority, ok := route.Authority().(coremetadata.ClaudeAuthorityRef)
		if reason != "" || !ok {
			return errors.New("exact registration is unavailable")
		}
		m := intmetadata.DefaultMutator()
		if !m.ClearClaudeRegistration(reg, f.bootstrap.PaneUID, f.bootstrap.AgentUID, f.bootstrap.Generation, authority) {
			return errors.New("registration clear refused")
		}
		registration := f.bootstrap.Registration
		registration.Authority = authority
		registration.Authority.LeaseProcess = leaseProcess
		return m.RecordClaudeRegistration(reg, f.bootstrap.PaneUID, f.bootstrap.AgentUID, f.bootstrap.Generation, registration)
	})
	if err != nil {
		t.Fatal(err)
	}
	route, reason := f.route(t)
	authority, _ := route.Authority().(coremetadata.ClaudeAuthorityRef)
	if reason != "" || authority.LeaseProcess != leaseProcess {
		t.Fatal("registration authority did not change")
	}
}

func callClaudeEndpointCoordinationProbe(t *testing.T, f *claudeEndpointTestFixture, route coremetadata.AgentRouteRef) (claudeCoordinationResponse, error) {
	t.Helper()
	target, ok := claudeTargetForRoute(route)
	if !ok {
		t.Fatal("exact coordination target unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	return callClaudeCoordination(ctx, f.bootstrap.RegistryPath, route, claudeCoordinationRequest{
		Version: claudeCoordinationVersion, Operation: "probe", Target: target,
	})
}

// assertClaudeEndpointDeliveryRefuses checks every delivery-time fence against
// the pre-change route. The helper's own provider poster must refuse before its
// caller fence runs, which proves the helper's full current closure refused,
// and no connection may reach the provider socket.
func assertClaudeEndpointDeliveryRefuses(t *testing.T, f *claudeEndpointTestFixture, route coremetadata.AgentRouteRef, poster *liveClaudeProviderPoster, providerSocket *net.UnixListener, helperAlive bool) {
	t.Helper()
	if probeClaudeRegistrationLease(f.bootstrap.RegistryPath, route) {
		t.Fatal("lease readiness probe stayed ready")
	}
	response, err := callClaudeEndpointCoordinationProbe(t, f, route)
	if err == nil && response.Kind == "ready" {
		t.Fatal("coordination probe stayed ready")
	}
	if helperAlive && (err != nil || response.Kind != "stale") {
		t.Fatalf("live helper coordination probe = %q, %v; want the current-check stale refusal", response.Kind, err)
	}
	fenceCalls := 0
	outcome, err := poster.Post("idle registry gate fence", func() bool { fenceCalls++; return true })
	if err == nil || outcome.Reason != "provider-prewrite-refused" || outcome.WroteAny || outcome.FullFrameWritten || fenceCalls != 0 {
		t.Fatalf("provider push was not refused by the helper current closure: %+v fence=%d", outcome, fenceCalls)
	}
	_ = providerSocket.SetDeadline(time.Now().Add(20 * time.Millisecond))
	if conn, err := providerSocket.Accept(); err == nil {
		_ = conn.Close()
		t.Fatal("provider transport connection occurred")
	}
}

func awaitClaudeEndpointExit(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("helper did not exit")
	}
}

func TestClaudeEndpointIdleRegistryGateEvaluatesOnlyOnChangeFailureOrFloor(t *testing.T) {
	t.Parallel()
	var (
		clock                      time.Time
		identity                   = intmetadata.RegistryFileIdentity{Device: 1, Inode: 2, Size: 3, ModTimeNanoseconds: 4, ChangeTimeSeconds: 5, ChangeTimeNanoseconds: 6}
		statErr                    error
		identityOK, registryOK     = true, true
		ticks, identityCalls       int
		statCalls, loads           int
		duringLoad                 func()
		lastEvaluation             time.Time
		identityPart, registryPart = func() bool { identityCalls++; return identityOK }, func() bool {
			loads++
			if duringLoad != nil {
				duringLoad()
			}
			return registryOK
		}
	)
	gate := claudeEndpointIdleRegistryGate{
		stat:  func() (intmetadata.RegistryFileIdentity, error) { statCalls++; return identity, statErr },
		now:   func() time.Time { return clock },
		floor: claudeEndpointIdleRegistryFloor,
	}
	tick := func(wantLoad bool) {
		t.Helper()
		before := loads
		clock = clock.Add(claudeEndpointPollInterval)
		ticks++
		if !gate.current(identityPart, registryPart) {
			t.Fatal("idle gate refused a current endpoint")
		}
		if loaded := loads > before; loaded != wantLoad {
			t.Fatalf("tick %d loaded the Registry = %v, want %v", ticks, loaded, wantLoad)
		}
		if wantLoad {
			lastEvaluation = clock
		}
	}

	tick(true)
	tick(false)
	tick(false)
	for _, change := range []func(){
		func() { identity.Device++ }, func() { identity.Inode++ }, func() { identity.Size++ },
		func() { identity.ModTimeNanoseconds++ }, func() { identity.ChangeTimeSeconds++ }, func() { identity.ChangeTimeNanoseconds++ },
	} {
		change()
		tick(true)
		tick(false)
	}

	statErr = errors.New("registry stat failed")
	tick(true)
	tick(true)
	statErr = nil
	tick(true)
	tick(false)

	// A write that lands during the load is seen on the next tick because the
	// stat identity was captured before the load.
	identity.Size++
	duringLoad = func() { identity.Inode++ }
	tick(true)
	duringLoad = nil
	tick(true)
	tick(false)

	// Under a stat identity collision the floor alone forces the evaluation.
	floorStart := lastEvaluation
	for clock.Sub(floorStart) < claudeEndpointIdleRegistryFloor-claudeEndpointPollInterval {
		tick(false)
	}
	tick(true)
	if elapsed := clock.Sub(floorStart); elapsed != claudeEndpointIdleRegistryFloor {
		t.Fatalf("floor evaluation ran %s after the last evaluation, want %s", elapsed, claudeEndpointIdleRegistryFloor)
	}
	if identityCalls != ticks || statCalls != ticks {
		t.Fatalf("identity part ran %d and stat ran %d times over %d ticks", identityCalls, statCalls, ticks)
	}

	identityOK = false
	before := [2]int{statCalls, loads}
	if gate.current(identityPart, registryPart) || statCalls != before[0] || loads != before[1] {
		t.Fatal("identity refusal did not stop the tick before the Registry gate")
	}
	identityOK, registryOK = true, false
	identity.Inode++
	if gate.current(identityPart, registryPart) {
		t.Fatal("stale Registry evaluation did not refuse the tick")
	}
}

func TestClaudeEndpointRegistryStatIdentityTracksReplacementAndMissingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")
	store := intmetadata.NewStore(path)
	if _, err := store.RegistryFileIdentity(); err == nil {
		t.Fatal("missing Registry produced a stat identity")
	}
	identity := func() intmetadata.RegistryFileIdentity {
		t.Helper()
		observed, err := store.RegistryFileIdentity()
		if err != nil {
			t.Fatal(err)
		}
		return observed
	}
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	first := identity()
	if again := identity(); again != first {
		t.Fatal("unchanged Registry stat identity drifted")
	}
	// Registry writers create a temp file and rename it over the Registry, so a
	// replacement with identical bytes still carries a new inode.
	temp := filepath.Join(dir, "registry.json.tmp")
	if err := os.WriteFile(temp, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temp, path); err != nil {
		t.Fatal(err)
	}
	replaced := identity()
	if replaced.Inode == first.Inode {
		t.Fatal("same-bytes Registry replacement kept its stat identity")
	}
	if err := os.WriteFile(path, []byte("{ }"), 0o600); err != nil {
		t.Fatal(err)
	}
	if identity() == replaced {
		t.Fatal("in-place Registry rewrite kept its stat identity")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegistryFileIdentity(); err == nil {
		t.Fatal("removed Registry produced a stat identity")
	}
}

func TestClaudeEndpointIdleTickReloadsChangedRegistryBeforeFloor(t *testing.T) {
	t.Parallel()
	// The real stat with a one-hour floor leaves the Registry stat identity as
	// the only trigger that can stop this helper.
	f, probe, done, _, _ := startClaudeEndpointWithIdleProbe(t, (*intmetadata.Store).RegistryFileIdentity, time.Hour)
	probe.awaitTicks(t, done, 3)
	replaceClaudeEndpointAuthority(t, f)
	changed := time.Now()
	ticks := probe.ticks.Load()
	awaitClaudeEndpointExit(t, done)
	elapsed := time.Since(changed)
	if exitTicks := probe.ticks.Load(); exitTicks > ticks+1 {
		t.Fatalf("helper exited on idle tick %d, want at most the first tick after the change (%d)", exitTicks, ticks+1)
	}
	if bound := claudeEndpointIdleRegistryFloor / 2; elapsed >= bound {
		t.Fatalf("changed Registry stopped the helper after %s, want under %s", elapsed, bound)
	}
}

func TestClaudeEndpointIdleRegistryFloorBoundsExitUnderStatCollision(t *testing.T) {
	t.Parallel()
	if bound := claudeEndpointIdleRegistryFloor + claudeEndpointPollInterval; bound > 2*time.Second {
		t.Fatalf("idle Registry floor plus one tick is %s, want at most 2s", bound)
	}
	f, probe, done, route, _ := startClaudeEndpointWithIdleProbe(t, claudeEndpointHeldIdleStat, claudeEndpointIdleRegistryFloor)
	replaceClaudeEndpointAuthority(t, f)
	// The only Registry evaluation ran at fake time zero on the first tick.
	probe.clock.Store(int64(claudeEndpointIdleRegistryFloor - time.Nanosecond))
	probe.awaitTicks(t, done, 3)
	if probeClaudeRegistrationLease(f.bootstrap.RegistryPath, route) {
		t.Fatal("lease readiness followed the idle Registry gate")
	}
	probe.clock.Store(int64(claudeEndpointIdleRegistryFloor))
	ticks := probe.ticks.Load()
	awaitClaudeEndpointExit(t, done)
	if exitTicks := probe.ticks.Load(); exitTicks > ticks+1 {
		t.Fatalf("helper exited on idle tick %d, want at most the first tick after the floor (%d)", exitTicks, ticks+1)
	}
}

func TestClaudeEndpointDeliveryFenceStaysFreshWhileIdleRegistryGateHolds(t *testing.T) {
	t.Parallel()
	t.Run("registry authority change", func(t *testing.T) {
		t.Parallel()
		f, probe, done, route, poster := startClaudeEndpointWithIdleProbe(t, claudeEndpointHeldIdleStat, time.Hour)
		fenceCalls := 0
		outcome, err := poster.Post("idle registry gate fence", func() bool { fenceCalls++; return false })
		if err == nil || outcome.Reason != "provider-prewrite-refused" || outcome.WroteAny || fenceCalls != 1 {
			t.Fatalf("helper current closure was not exact before the change: %+v fence=%d", outcome, fenceCalls)
		}
		replaceClaudeEndpointAuthority(t, f)
		probe.awaitTicks(t, done, 2)
		assertClaudeEndpointDeliveryRefuses(t, f, route, poster, f.inbox, true)
		probe.awaitTicks(t, done, 2)
	})
	t.Run("provider socket replacement", func(t *testing.T) {
		t.Parallel()
		f, _, done, route, poster := startClaudeEndpointWithIdleProbe(t, claudeEndpointHeldIdleStat, time.Hour)
		if err := os.Remove(f.bootstrap.Socket); err != nil {
			t.Fatal(err)
		}
		replacement, err := net.ListenUnix("unix", &net.UnixAddr{Name: f.bootstrap.Socket, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		replacement.SetUnlinkOnClose(false)
		defer replacement.Close()
		if err := os.Chmod(f.bootstrap.Socket, 0o600); err != nil {
			t.Fatal(err)
		}
		assertClaudeEndpointDeliveryRefuses(t, f, route, poster, replacement, false)
		awaitClaudeEndpointExit(t, done)
	})
	t.Run("provider process death", func(t *testing.T) {
		t.Parallel()
		f, _, done, route, poster := startClaudeEndpointWithIdleProbe(t, claudeEndpointHeldIdleStat, time.Hour)
		if err := f.provider.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		_ = f.provider.Wait()
		assertClaudeEndpointDeliveryRefuses(t, f, route, poster, f.inbox, false)
		awaitClaudeEndpointExit(t, done)
	})
}
