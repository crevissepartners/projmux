package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	claudeadapter "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// fakeClaudeHelperProcess records what the hook did with a started helper.
type fakeClaudeHelperProcess struct {
	killed, released atomic.Int32
}

func (p *fakeClaudeHelperProcess) Kill() error    { p.killed.Add(1); return nil }
func (p *fakeClaudeHelperProcess) Release() error { p.released.Add(1); return nil }

func (p *fakeClaudeHelperProcess) assertReleased(t *testing.T) {
	t.Helper()
	if killed, released := p.killed.Load(), p.released.Load(); killed != 0 || released != 1 {
		t.Fatalf("hook killed the helper %d times and released it %d times, want released once and never killed", killed, released)
	}
}

// observedClaudeAck reports the helper's acknowledgement write and its result.
type observedClaudeAck struct {
	w       io.Writer
	written chan error
}

func (a *observedClaudeAck) Write(p []byte) (int, error) {
	n, err := a.w.Write(p)
	select {
	case a.written <- err:
	default:
	}
	return n, err
}

// serveClaudeAdmissionHelper runs one helper in-process the way the detached
// helper runs it, with the caller's acknowledgement writer. The helper keeps
// running after the caller stops reading, as a released helper does.
func serveClaudeAdmissionHelper(t *testing.T, bootstrap claudeEndpointBootstrap, ack io.Writer) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- serveClaudeEndpoint(ctx, bootstrap, ack)
		close(done)
		if closer, ok := ack.(io.Closer); ok {
			_ = closer.Close()
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Error("helper did not exit")
		}
	})
	return cancel, done
}

// cleanupClaudeAdmissionLeaseDir removes the fixed /tmp activation lease dir
// after every helper registered later has exited.
func cleanupClaudeAdmissionLeaseDir(t *testing.T, bootstrap claudeEndpointBootstrap) string {
	t.Helper()
	dir := claudeActivationLeaseDir(bootstrap.RegistryPath, bootstrap.PaneUID, bootstrap.Generation)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// claudeAdmissionSuccessor is the bootstrap of a newer SessionStart for the same
// provider process, fenced by a different registrationGeneration.
func claudeAdmissionSuccessor(t *testing.T, bootstrap claudeEndpointBootstrap) claudeEndpointBootstrap {
	t.Helper()
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	bootstrap.Registration.Authority.RegistrationGeneration = hex.EncodeToString(nonce)
	return bootstrap
}

func beginClaudeAdmission(reg *coremetadata.Registry, bootstrap claudeEndpointBootstrap) error {
	return intmetadata.DefaultMutator().BeginClaudeRegistration(reg, bootstrap.PaneUID, bootstrap.AgentUID, bootstrap.Generation, bootstrap.Registration.Authority)
}

// claudeAdmissionResidue lists the files one in-process helper owns: its lease
// socket, its owner receipt, and its coordination socket.
func claudeAdmissionResidue(t *testing.T, bootstrap claudeEndpointBootstrap) []string {
	t.Helper()
	process, _, err := claudeadapter.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	authority := bootstrap.Registration.Authority
	authority.LeaseProcess = process
	lease := claudeLeaseSocket(bootstrap.RegistryPath, bootstrap.PaneUID, bootstrap.Generation, authority.RegistrationGeneration)
	target := claudeCoordinationTarget{AgentUID: bootstrap.AgentUID, PaneUID: bootstrap.PaneUID,
		Generation: bootstrap.Generation, Provider: aiModeClaude, Authority: authority}
	return []string{lease, lease + ".json", claudeCoordinationSocket(bootstrap.RegistryPath, target)}
}

func assertClaudeAdmissionResidue(t *testing.T, paths []string, present bool) {
	t.Helper()
	for _, path := range paths {
		if _, err := os.Lstat(path); present != (err == nil) {
			t.Fatalf("lease file %s present=%v, want %v (%v)", path, err == nil, present, err)
		}
	}
}

func assertClaudeAdmissionDirGone(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lease dir %s left behind: %v", dir, err)
	}
}

// holdClaudeRegistryLock holds the Registry mutation lock inside one
// transaction until release is called, then applies mutate in it.
func holdClaudeRegistryLock(t *testing.T, store *intmetadata.Store, mutate func(*coremetadata.Registry) error) func() {
	t.Helper()
	held, unblock, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		_, _, err := store.UpdateConvergent(func(reg *coremetadata.Registry) error {
			close(held)
			<-unblock
			if mutate != nil {
				return mutate(reg)
			}
			return nil
		})
		finished <- err
	}()
	select {
	case <-held:
	case <-time.After(4 * time.Second):
		t.Fatal("registry lock was not taken")
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			close(unblock)
			if err := <-finished; err != nil {
				t.Errorf("lock holder transaction: %v", err)
			}
		})
	}
	t.Cleanup(release)
	return release
}

func waitClaudeAdmission(t *testing.T, what string, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func claudeAdmissionPresent(path string) func() bool {
	return func() bool { _, err := os.Lstat(path); return err == nil }
}

// startReleasedClaudeHelper starts a helper while the test holds the Registry
// lock, waits until it wrote its owner receipt (the step right before Record),
// and lets the hook's acknowledgement deadline pass. The hook then returns and
// closes its read side, as the SessionStart hook process does.
func startReleasedClaudeHelper(t *testing.T, bootstrap claudeEndpointBootstrap) (*fakeClaudeHelperProcess, context.CancelFunc, <-chan error) {
	t.Helper()
	readAck, writeAck, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cancel, done := serveClaudeAdmissionHelper(t, bootstrap, writeAck)
	waitClaudeAdmission(t, "the helper owner receipt", claudeAdmissionPresent(claudeAdmissionResidue(t, bootstrap)[1]))
	helper := &fakeClaudeHelperProcess{}
	if err := awaitClaudeHelperAdmission(readAck, time.Now().Add(50*time.Millisecond), helper); err == nil {
		t.Fatal("hook confirmed an admission the helper never acknowledged")
	}
	_ = readAck.Close()
	return helper, cancel, done
}

func TestClaudeHelperAdmissionReleasesWithoutAck(t *testing.T) {
	t.Parallel()
	readAck, writeAck, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readAck.Close()
	defer writeAck.Close()
	helper := &fakeClaudeHelperProcess{}
	if err := awaitClaudeHelperAdmission(readAck, time.Now().Add(50*time.Millisecond), helper); err == nil {
		t.Fatal("expired acknowledgement deadline confirmed admission")
	}
	helper.assertReleased(t)
}

func TestClaudeHelperAdmissionReleasesOnFailedAckRead(t *testing.T) {
	t.Parallel()
	readAck, writeAck, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readAck.Close()
	_ = writeAck.Close()
	helper := &fakeClaudeHelperProcess{}
	if err := awaitClaudeHelperAdmission(readAck, time.Now().Add(2*time.Second), helper); err == nil {
		t.Fatal("closed acknowledgement confirmed admission")
	}
	helper.assertReleased(t)
}

func TestClaudeHelperAdmissionReleasesOnAck(t *testing.T) {
	t.Parallel()
	readAck, writeAck, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readAck.Close()
	defer writeAck.Close()
	if _, err := writeAck.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	helper := &fakeClaudeHelperProcess{}
	if err := awaitClaudeHelperAdmission(readAck, time.Now().Add(2*time.Second), helper); err != nil {
		t.Fatalf("acknowledged admission failed: %v", err)
	}
	helper.assertReleased(t)
}

// P1: the hook stopped waiting (Claude Code killed it at its hook timeout)
// after the helper recorded Ready, so the acknowledgement write fails.
func TestClaudeEndpointAdmissionAckFailureAfterRecordKeepsServing(t *testing.T) {
	t.Parallel()
	f := newClaudeEndpointTestFixture(t)
	dir := cleanupClaudeAdmissionLeaseDir(t, f.bootstrap)
	readAck, writeAck := io.Pipe()
	_ = readAck.Close()
	ack := &observedClaudeAck{w: writeAck, written: make(chan error, 1)}
	cancel, done := serveClaudeAdmissionHelper(t, f.bootstrap, ack)
	select {
	case err := <-ack.written:
		if err == nil {
			t.Fatal("closed acknowledgement accepted the byte")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("helper never acknowledged")
	}
	select {
	case err := <-done:
		t.Fatalf("helper exited after its hook stopped waiting: %v", err)
	case <-time.After(3 * claudeEndpointPollInterval):
	}
	route, reason := f.route(t)
	if reason != "" || !probeClaudeRegistrationLease(f.bootstrap.RegistryPath, route) {
		t.Fatalf("registration lost after a failed acknowledgement: %q", reason)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("helper did not exit")
	}
	assertClaudeAdmissionResidue(t, claudeAdmissionResidue(t, f.bootstrap), false)
	assertClaudeAdmissionDirGone(t, dir)
}

// P2 with R2 (a): a released helper whose Record fails because a newer
// SessionStart superseded its Begin exits by itself and leaves nothing behind.
func TestClaudeEndpointAdmissionReleasedHelperExitsCleanWhenRecordFails(t *testing.T) {
	t.Parallel()
	f := newClaudeEndpointTestFixture(t)
	dir := cleanupClaudeAdmissionLeaseDir(t, f.bootstrap)
	newer := claudeAdmissionSuccessor(t, f.bootstrap)
	release := holdClaudeRegistryLock(t, f.store, func(reg *coremetadata.Registry) error { return beginClaudeAdmission(reg, newer) })
	helper, _, done := startReleasedClaudeHelper(t, f.bootstrap)
	release()
	helper.assertReleased(t)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("superseded helper recorded its registration")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("released helper did not exit after Record failed")
	}
	assertClaudeAdmissionResidue(t, claudeAdmissionResidue(t, f.bootstrap), false)
	assertClaudeAdmissionDirGone(t, dir)
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane(f.bootstrap.PaneUID)
	if claude := pane.Status.Activation.Claude; claude == nil || claude.RegistrationGeneration != newer.Registration.Authority.RegistrationGeneration || claude.Registration != nil {
		t.Fatal("superseded helper changed the newer Begin")
	}
}

// R2 (b): a stale released helper that reaches Record after a newer helper is
// Ready cannot overwrite the newer registration.
func TestClaudeEndpointAdmissionStaleReleasedHelperKeepsNewerRegistration(t *testing.T) {
	t.Parallel()
	f := newClaudeEndpointTestFixture(t)
	cleanupClaudeAdmissionLeaseDir(t, f.bootstrap)
	newer := claudeAdmissionSuccessor(t, f.bootstrap)
	if _, _, err := f.store.UpdateConvergent(func(reg *coremetadata.Registry) error { return beginClaudeAdmission(reg, newer) }); err != nil {
		t.Fatal(err)
	}
	current := *f
	current.bootstrap = newer
	current.start(t)
	release := holdClaudeRegistryLock(t, f.store, nil)
	helper, _, done := startReleasedClaudeHelper(t, f.bootstrap)
	release()
	helper.assertReleased(t)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stale helper recorded its registration")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("stale released helper did not exit")
	}
	route, reason := f.route(t)
	authority, _ := route.Authority().(coremetadata.ClaudeAuthorityRef)
	if reason != "" || authority.RegistrationGeneration != newer.Registration.Authority.RegistrationGeneration ||
		!probeClaudeRegistrationLease(f.bootstrap.RegistryPath, route) {
		t.Fatalf("stale helper disturbed the newer registration: %q", reason)
	}
	assertClaudeAdmissionResidue(t, claudeAdmissionResidue(t, f.bootstrap), false)
	assertClaudeAdmissionResidue(t, claudeAdmissionResidue(t, newer), true)
}

// R2 (c): a released helper still waiting on the Registry lock for Record is
// alive, so the dead-lease reaper leaves it alone; once the lock frees it
// records Ready and keeps serving although nobody reads its acknowledgement.
func TestClaudeEndpointAdmissionReleasedHelperWaitingOnLockBecomesReady(t *testing.T) {
	t.Parallel()
	f := newClaudeEndpointTestFixture(t)
	dir := cleanupClaudeAdmissionLeaseDir(t, f.bootstrap)
	release := holdClaudeRegistryLock(t, f.store, nil)
	helper, cancel, done := startReleasedClaudeHelper(t, f.bootstrap)
	residue := claudeAdmissionResidue(t, f.bootstrap)
	reapDeadClaudeLeases(superviseSpec{RegistryPath: f.bootstrap.RegistryPath, PaneUID: f.bootstrap.PaneUID,
		AgentUID: f.bootstrap.AgentUID, Generation: f.bootstrap.Generation})
	present := make([]bool, len(residue))
	for i, path := range residue {
		present[i] = claudeAdmissionPresent(path)()
	}
	release()
	helper.assertReleased(t)
	for i, path := range residue {
		if !present[i] {
			t.Fatalf("dead-lease reaper removed the live released helper's %s", path)
		}
	}
	waitClaudeAdmission(t, "the released helper to record Ready", func() bool {
		_, reason := f.route(t)
		return reason == ""
	})
	select {
	case err := <-done:
		t.Fatalf("released helper exited after recording Ready: %v", err)
	case <-time.After(3 * claudeEndpointPollInterval):
	}
	route, reason := f.route(t)
	if reason != "" || !probeClaudeRegistrationLease(f.bootstrap.RegistryPath, route) {
		t.Fatalf("released helper registration is not ready: %q", reason)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("helper did not exit")
	}
	assertClaudeAdmissionResidue(t, residue, false)
	assertClaudeAdmissionDirGone(t, dir)
}

// R2 (c): the supervisor removes every lease file once it reaped the provider,
// without reading Registry. A released helper still waiting on the Registry
// lock at that moment must not recreate them or record Ready afterwards: its
// Record sees the provider gone, it exits, and nothing is left behind.
func TestClaudeEndpointAdmissionReleasedHelperWaitingOnLockSurvivesSupervisorCleanup(t *testing.T) {
	t.Parallel()
	f := newClaudeEndpointTestFixture(t)
	dir := cleanupClaudeAdmissionLeaseDir(t, f.bootstrap)
	release := holdClaudeRegistryLock(t, f.store, nil)
	helper, _, done := startReleasedClaudeHelper(t, f.bootstrap)
	residue := claudeAdmissionResidue(t, f.bootstrap)
	assertClaudeAdmissionResidue(t, residue, true)
	if err := f.provider.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = f.provider.Wait()
	cleanupClaudeActivationLeases(superviseSpec{RegistryPath: f.bootstrap.RegistryPath, PaneUID: f.bootstrap.PaneUID,
		AgentUID: f.bootstrap.AgentUID, Generation: f.bootstrap.Generation})
	select {
	case err := <-done:
		release()
		t.Fatalf("helper exited while blocked on the Registry lock: %v", err)
	default:
	}
	assertClaudeAdmissionResidue(t, residue, false)
	assertClaudeAdmissionDirGone(t, dir)
	release()
	helper.assertReleased(t)
	select {
	case err := <-done:
		if err == nil || err.Error() != "claude registration admission failed" {
			t.Fatalf("helper exit = %v, want only its Record refusal", err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("released helper did not exit after the provider was gone")
	}
	if _, reason := f.route(t); reason == "" {
		t.Fatal("registration became Ready after supervisor cleanup")
	}
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	if pane, _ := reg.Pane(f.bootstrap.PaneUID); pane.Status.Activation.Claude != nil && pane.Status.Activation.Claude.Registration != nil {
		t.Fatal("helper recorded a registration after supervisor cleanup")
	}
	assertClaudeAdmissionResidue(t, residue, false)
	assertClaudeAdmissionDirGone(t, dir)
}

// resetClaudeAdmissionRegistration returns the fixture's activation to the
// state before its first SessionStart: the provider process is bound, and no
// registration was ever claimed.
func resetClaudeAdmissionRegistration(t *testing.T, f *claudeEndpointTestFixture) {
	t.Helper()
	if _, _, err := f.store.UpdateConvergent(func(reg *coremetadata.Registry) error {
		pane, _ := reg.Pane(f.bootstrap.PaneUID)
		pane.Status.Activation.Claude.RegistrationSessionID = ""
		pane.Status.Activation.Claude.RegistrationGeneration = ""
		pane.Status.Activation.Claude.Registration = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// claudeAdmissionHookBootstrap builds a SessionStart hook bootstrap the way the
// hook does: from a lock-free read of the Registry as it is now.
func claudeAdmissionHookBootstrap(t *testing.T, f *claudeEndpointTestFixture) claudeEndpointBootstrap {
	t.Helper()
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	bootstrap, ok := claudeRegistrationBootstrap(reg, f.bootstrap.RegistryPath, []byte(`{"hook_event_name":"SessionStart","session_id":"actual-session"}`), func(key string) string {
		switch key {
		case internalActivationPaneUIDEnv:
			return f.bootstrap.PaneUID
		case internalActivationGenerationEnv:
			return f.bootstrap.Generation
		case "CLAUDE_CODE_MESSAGING_SOCKET":
			return f.bootstrap.Socket
		case "CLAUDE_CODE_MESSAGING_TOKEN":
			return f.bootstrap.Token
		}
		return ""
	}, f.provider.Process.Pid)
	if !ok {
		t.Fatal("valid SessionStart refused")
	}
	return bootstrap
}

func claudeAdmissionBinding(t *testing.T, f *claudeEndpointTestFixture) coremetadata.ClaudeActivationBinding {
	t.Helper()
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane(f.bootstrap.PaneUID)
	if pane.Status.Activation.Claude == nil {
		t.Fatal("provider process binding is gone")
	}
	return *pane.Status.Activation.Claude
}

// claudeAdmissionHook runs the hook's registration step for bootstrap with an
// in-process helper in place of the detached one, and waits for the hook to
// return. It reports whether the hook returned within budget; if it did not,
// it releases the Registry lock (release) and waits for the hook to finish.
type claudeAdmissionHook struct {
	helper  *fakeClaudeHelperProcess
	started bool
	cancel  context.CancelFunc
	done    <-chan error
	onTime  bool
}

func runClaudeAdmissionHook(t *testing.T, bootstrap claudeEndpointBootstrap, budget time.Duration, release func()) *claudeAdmissionHook {
	t.Helper()
	hook := &claudeAdmissionHook{helper: &fakeClaudeHelperProcess{}}
	start := func(bootstrap claudeEndpointBootstrap) error {
		readAck, writeAck, err := os.Pipe()
		if err != nil {
			return err
		}
		defer readAck.Close()
		hook.cancel, hook.done = serveClaudeAdmissionHelper(t, bootstrap, writeAck)
		hook.started = true
		return awaitClaudeHelperAdmission(readAck, time.Now().Add(200*time.Millisecond), hook.helper)
	}
	returned := make(chan struct{})
	go func() {
		registerClaudeEndpoint(bootstrap, start)
		close(returned)
	}()
	select {
	case <-returned:
		hook.onTime = true
	case <-time.After(budget):
		release()
		select {
		case <-returned:
		case <-time.After(4 * time.Second):
			t.Fatal("hook did not return after the Registry lock was released")
		}
	}
	return hook
}

// K1/K2/K3: Claude cancels the SessionStart hook at its 5s timeout. With the
// Registry lock held, the hook starts its helper without waiting on the lock
// and returns within its acknowledgement deadline; the helper, released and
// unread, claims and records its registration once the lock frees. No
// session id without Ready is ever published on the way.
func TestClaudeEndpointHookStartsHelperWithoutWaitingOnRegistryLock(t *testing.T) {
	t.Parallel()
	f := newClaudeEndpointTestFixture(t)
	dir := cleanupClaudeAdmissionLeaseDir(t, f.bootstrap)
	resetClaudeAdmissionRegistration(t, f)
	bootstrap := claudeAdmissionHookBootstrap(t, f)
	release := holdClaudeRegistryLock(t, f.store, nil)
	hook := runClaudeAdmissionHook(t, bootstrap, 2*time.Second, release)
	if !hook.onTime {
		t.Fatal("hook waited on the Registry lock instead of starting its helper")
	}
	if !hook.started {
		t.Fatal("hook returned without starting its helper")
	}
	hook.helper.assertReleased(t)
	if binding := claudeAdmissionBinding(t, f); binding.RegistrationSessionID != "" || binding.RegistrationGeneration != "" || binding.Registration != nil {
		t.Fatal("hook wrote the Registry while it was locked")
	}
	var beginOnly atomic.Bool
	stop, watched := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watched)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if reg, err := f.store.LoadDegradedReadOnly(); err == nil {
				if pane, ok := reg.Pane(f.bootstrap.PaneUID); ok {
					if claude := pane.Status.Activation.Claude; claude != nil && claude.RegistrationSessionID != "" && (claude.Registration == nil || !claude.Registration.Ready) {
						beginOnly.Store(true)
					}
				}
			}
			time.Sleep(time.Millisecond)
		}
	}()
	release()
	waitClaudeAdmission(t, "the released helper to record Ready", func() bool {
		_, reason := f.route(t)
		return reason == ""
	})
	close(stop)
	<-watched
	if beginOnly.Load() {
		t.Fatal("a session id without Ready was published")
	}
	select {
	case err := <-hook.done:
		t.Fatalf("released helper exited after recording Ready: %v", err)
	case <-time.After(3 * claudeEndpointPollInterval):
	}
	route, reason := f.route(t)
	authority, _ := route.Authority().(coremetadata.ClaudeAuthorityRef)
	if reason != "" || authority.RegistrationGeneration != bootstrap.Registration.Authority.RegistrationGeneration ||
		!probeClaudeRegistrationLease(f.bootstrap.RegistryPath, route) {
		t.Fatalf("released helper registration is not ready: %q", reason)
	}
	residue := claudeAdmissionResidue(t, bootstrap)
	hook.cancel()
	select {
	case <-hook.done:
	case <-time.After(4 * time.Second):
		t.Fatal("helper did not exit")
	}
	assertClaudeAdmissionResidue(t, residue, false)
	assertClaudeAdmissionDirGone(t, dir)
}

// K2/K3: the hook is cancelled after its bootstrap but before a helper admits
// the registration (no helper started, or the helper refused its producer).
// The hook alone never claims a registration, so the activation reads as
// never registered rather than registered then lost.
func TestClaudeEndpointHookWithoutHelperClaimsNothing(t *testing.T) {
	t.Parallel()
	f := newClaudeEndpointTestFixture(t)
	resetClaudeAdmissionRegistration(t, f)
	bootstrap := claudeAdmissionHookBootstrap(t, f)
	started := false
	registerClaudeEndpoint(bootstrap, func(claudeEndpointBootstrap) error {
		started = true
		return errors.New("claude helper start failed")
	})
	if !started {
		t.Fatal("hook did not try to start its helper")
	}
	reg, err := f.store.LoadDegradedReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	pane, _ := reg.Pane(f.bootstrap.PaneUID)
	if shape := coremetadata.ClassifyClaudeRegistration(*pane, true); shape != coremetadata.ClaudeRegistrationNeverStarted {
		t.Fatalf("activation without a helper reads as %q, want %q", shape, coremetadata.ClaudeRegistrationNeverStarted)
	}
}

// A hook bootstrapped before a newer SessionStart committed its registration
// gets the Registry lock only after it. Its helper's claim is a CAS on the
// generation the hook observed, so it neither fences out nor overwrites the
// newer registration, and exits leaving nothing behind.
func TestClaudeEndpointStaleHookHelperKeepsNewerRegistration(t *testing.T) {
	t.Parallel()
	f := newClaudeEndpointTestFixture(t)
	dir := cleanupClaudeAdmissionLeaseDir(t, f.bootstrap)
	resetClaudeAdmissionRegistration(t, f)
	stale := claudeAdmissionHookBootstrap(t, f)
	newer := claudeAdmissionHookBootstrap(t, f)
	if _, _, err := f.store.UpdateConvergent(func(reg *coremetadata.Registry) error { return beginClaudeAdmission(reg, newer) }); err != nil {
		t.Fatal(err)
	}
	current := *f
	current.bootstrap = newer
	current.start(t)
	release := holdClaudeRegistryLock(t, f.store, nil)
	hook := runClaudeAdmissionHook(t, stale, 2*time.Second, release)
	if !hook.onTime {
		t.Error("stale hook waited on the Registry lock instead of starting its helper")
	}
	release()
	if hook.started {
		select {
		case err := <-hook.done:
			if err == nil {
				t.Fatal("stale helper recorded its registration")
			}
		case <-time.After(4 * time.Second):
			t.Fatal("stale helper did not exit")
		}
	}
	route, reason := f.route(t)
	authority, _ := route.Authority().(coremetadata.ClaudeAuthorityRef)
	if reason != "" || authority.RegistrationGeneration != newer.Registration.Authority.RegistrationGeneration ||
		!probeClaudeRegistrationLease(f.bootstrap.RegistryPath, route) {
		t.Fatalf("stale hook disturbed the newer registration: %q", reason)
	}
	if binding := claudeAdmissionBinding(t, f); binding.RegistrationGeneration != newer.Registration.Authority.RegistrationGeneration {
		t.Fatal("stale helper claimed the registration generation")
	}
	assertClaudeAdmissionResidue(t, claudeAdmissionResidue(t, stale), false)
	assertClaudeAdmissionResidue(t, claudeAdmissionResidue(t, newer), true)
	if _, err := os.Lstat(dir); err != nil {
		t.Fatalf("newer helper lease dir: %v", err)
	}
}
