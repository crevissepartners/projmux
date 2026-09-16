package app

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// The Continue replay (`projmux start project` reopening a closed Project) is
// the second runtime-mutation transaction that holds the Registry lock while it
// re-proves one server identity dozens of times. It runs the same
// topologyMaterializeRun.execute engine the public materialize route runs, so
// the tests below hold it to exactly the create transaction's contract: the
// first proof of the transaction really runs against tmux, only an exactly
// equal tuple is reused, every guarded write drops the proofs, anything reused
// is re-proved before the Registry commits, and the scope is closed before the
// runtime ledger unwinds.

// continueOperationMarker matches the transaction ledger's create-operation
// marker. Its pid and wall-clock second are the only bytes two replays of one
// pinned plan cannot reproduce; roots, uids, generations, and socket identity
// are all pinned by the fixture, so the comparisons below are byte equality
// after this one substitution.
var continueOperationMarker = regexp.MustCompile(`v1:\d+:\d+:`)

func normalizeContinueOperationMarker(value string) string {
	return continueOperationMarker.ReplaceAllString(value, "v1:<pid>:<at>:")
}

// newContinueReplayActivation builds the offline two-Window, three-shell-Pane
// Project of the startup topology fixture inside the supplied roots and binds
// the activation to one exact app-owned route, which is what makes the replay's
// identity cacheable at all.
//
// The roots are arguments rather than fresh temp dirs because the differential
// test compares two whole replays byte for byte: a per-run temp dir would put
// different `-c` paths on the create argv and make every comparison trivially
// unequal.
func newContinueReplayActivation(t *testing.T, root, logs string) (*registryProjectTopologyMaterializer, *fakeResourceStore, *fakeTmux) {
	t.Helper()
	store := newFakeResourceStore(t)
	project, _ := store.registry.Project("prj-beta")
	project.Spec.Root = root
	for i := range store.registry.Panes {
		pane := &store.registry.Panes[i]
		if pane.Metadata.OwnerUID() == "win-beta-main" {
			pane.Spec.CWD = root
		}
	}
	store.dirs = map[string]bool{root: true, logs: true}
	mutator := store.mutator()
	if _, err := mutator.AddPane(&store.registry, "win-beta-main", coremetadata.BootstrapPane{
		Name: "logs", CWD: logs, Command: "DO-NOT-EXECUTE --pane",
	}, "/bin/zsh", "op-continue-fixture"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := mutator.AddWindow(&store.registry, "prj-beta", coremetadata.BootstrapWindow{
		Name: "review", Command: "DO-NOT-EXECUTE --window",
		Panes: []coremetadata.BootstrapPane{{CWD: root, Command: "DO-NOT-EXECUTE --primary"}},
	}, "/bin/zsh", "op-continue-fixture"); err != nil {
		t.Fatal(err)
	}
	// Shell-only, like the other topology fixtures: Agent replay has its own.
	if err := mutator.DeleteAgent(&store.registry, "agt-beta-codex"); err != nil {
		t.Fatal(err)
	}
	server := newFakeTmux()
	server.socketPath = "/tmp/fake-tmux/continue"
	runner := &routedTmuxRunner{servers: map[string]*fakeTmux{"-L\x00" + defaultAppSocket: server}}
	sessions := &fakeSessionMaterializer{tmux: server}
	target, err := tmuxSocketNameTarget(defaultAppSocket)
	if err != nil {
		t.Fatal(err)
	}
	generation := 0
	activation := &registryProjectTopologyMaterializer{
		resources: store.store(),
		runner:    runner,
		target:    target,
		// The route is declared rather than resolved so the replay carries the
		// same exact identity a bound production route carries.
		expectedSocketPath: server.socketPath,
		socketName:         defaultAppSocket,
		routeAuthority:     &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: server.serverPID},
		newReconciler:      reconcileFixtureReconciler(root, "beta"),
		newOperationID:     func() (string, error) { return "op-continue", nil },
		newGeneration:      func() (string, error) { generation++; return fmt.Sprintf("gen-continue-%02d", generation), nil },
		newMaterializer: func(exact tmuxCommandRunner, warn io.Writer) *materializer {
			return &materializer{runner: exact, mirror: intmetadata.NewMirror(exact), sessions: sessions, warn: warn}
		},
		warn:    io.Discard,
		agents:  newFakeTopologyAgentLauncher(),
		notices: &bytes.Buffer{},
	}
	return activation, store, server
}

// continueRouteObserver watches one replay from the tmux seam. `before` runs
// with the exact argv of the call that is about to reach the server, which is
// the only place a test can read the live identity cache or inject drift at a
// point the transaction has not yet observed.
type continueRouteObserver struct {
	base   tmuxCommandRunner
	before func(argv []string)
}

func (o *continueRouteObserver) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if o.before != nil {
		o.before(args)
	}
	return o.base.Run(ctx, name, args...)
}

// continueReplayOutcome is everything one Continue replay leaves observable.
type continueReplayOutcome struct {
	err           string
	committed     bool
	writes        []string
	panes         []string
	guardedWrites int
	identityReads int
	// guardedWrite is the last tmux write argv when guarded write k finished.
	guardedWrite map[int]string
	// scopeClosedAt is the index, into the transaction's own calls, of the
	// first call this replay issued after the identity scope closed.
	scopeClosedAt int
	// scopeOpenAfterClose records a call that saw the scope open again after it
	// had closed, which would mean the unwind reused a proof.
	scopeOpenAfterClose bool
	warnings            string
	// proofBeforeFirstWrite is the identity evidence the transaction read from
	// tmux before it issued its first runtime write, and readsBeforeFirstWrite
	// is how many identity reads that first proof cost.
	proofBeforeFirstWrite continueFirstProof
	readsBeforeFirstWrite int
	// firstReuseAt is the transaction-relative call index at which this replay
	// first reused a proof, and the two fields under it are what the cache and
	// the server had already answered by then. They are how the test states
	// boundary 1: nothing is reused before a full proof really ran.
	firstReuseAt          int
	proofsAtFirstReuse    int
	readsBeforeFirstReuse int
	proofBeforeFirstReuse continueFirstProof
	// identityEvidence is everything the replay read about the server identity.
	identityEvidence continueFirstProof
	// calls is every tmux argv this replay issued, observed at the runner seam.
	// The fake server's own log is not enough: the routed fake answers the
	// logical socket-name marker itself, so a read of that marker never reaches
	// server.calls and an identity proof would look incomplete there.
	calls [][]string
}

// continueFirstProof is the server-identity evidence read over a span of argv.
type continueFirstProof struct {
	socketPath, pid, app, logical, wrote bool
}

// continueIdentityEvidence reports which parts of the server identity were read
// over the given argv log. stopAtWrite ends the span at the first runtime
// write, which is how the "proved before it wrote" question is asked.
func continueIdentityEvidence(calls [][]string, stopAtWrite bool) continueFirstProof {
	proof := continueFirstProof{}
	for _, call := range calls {
		argv := tmuxCommandArgv(call)
		if len(argv) == 0 {
			continue
		}
		if isRouteIdentityWrite(argv) {
			proof.wrote = true
			if stopAtWrite {
				return proof
			}
			continue
		}
		if argv[0] == "display-message" && flagValue(argv, "-t") == "" {
			switch flagValue(argv, "-F") {
			case "#{socket_path}":
				proof.socketPath = true
			case "#{pid}":
				proof.pid = true
			}
		}
		proof.app = proof.app || (argv[0] == "show-options" && slices.Contains(argv, tmuxopts.AppGlobal))
		proof.logical = proof.logical || (argv[0] == "show-options" && slices.Contains(argv, runtimeMutationSocketNameOption))
	}
	return proof
}

func (o continueReplayOutcome) sameResult(other continueReplayOutcome) bool {
	return o.err == other.err && o.committed == other.committed &&
		slices.Equal(o.writes, other.writes) && slices.Equal(o.panes, other.panes)
}

// continueReplayProbe is what a per-test observer sees at the tmux seam: the
// replay's own materializer once execute has built it, the fake server, and the
// outcome being accumulated.
type continueReplayProbe struct {
	runtime *materializer
	server  *fakeTmux
	outcome *continueReplayOutcome
}

// runContinueReplay runs one Continue topology replay against a fresh fake
// server in the supplied roots. driftAfter > 0 restarts the server generation
// immediately after guarded write driftAfter; observe, when set, installs a
// hook that runs with the exact argv of every call before it reaches tmux.
func runContinueReplay(t *testing.T, root, logs string, cacheOff bool, driftAfter int,
	observe func(probe *continueReplayProbe) func(argv []string),
) continueReplayOutcome {
	t.Helper()
	activation, store, server := newContinueReplayActivation(t, root, logs)
	outcome := continueReplayOutcome{guardedWrite: map[int]string{}, scopeClosedAt: -1, firstReuseAt: -1}
	var warnings strings.Builder
	activation.warn = &warnings
	probe := &continueReplayProbe{server: server, outcome: &outcome}
	var extra func(argv []string)
	if observe != nil {
		extra = observe(probe)
	}
	start := len(server.calls)
	opened := false
	// Every tmux call of the replay -- route binding, guards, writes, and the
	// unwind -- passes the materializer's routed runner, which wraps this one.
	activation.runner = &continueRouteObserver{base: activation.runner, before: func(argv []string) {
		switch {
		case probe.runtime != nil && probe.runtime.routeIdentity != nil:
			opened = true
			if outcome.scopeClosedAt >= 0 {
				outcome.scopeOpenAfterClose = true
			}
		case opened && outcome.scopeClosedAt < 0:
			outcome.scopeClosedAt = len(outcome.calls)
		}
		if outcome.firstReuseAt < 0 && probe.runtime != nil {
			if stats := probe.runtime.routeIdentity.snapshot(); stats.Reuses > 0 {
				outcome.firstReuseAt, outcome.proofsAtFirstReuse = len(outcome.calls), stats.Proofs
			}
		}
		outcome.calls = append(outcome.calls, slices.Clone(argv))
		if extra != nil {
			extra(argv)
		}
	}}
	base := activation.newMaterializer
	activation.newMaterializer = func(exact tmuxCommandRunner, warn io.Writer) *materializer {
		runtime := base(exact, warn)
		runtime.routeIdentityDisabled = cacheOff
		runtime.afterGuardedWrite = func() {
			outcome.guardedWrites++
			if writes := routeIdentityWrites(server.calls[start:]); len(writes) > 0 {
				outcome.guardedWrite[outcome.guardedWrites] = writes[len(writes)-1]
			}
			if outcome.guardedWrites == driftAfter {
				server.serverPID = "5252"
			}
		}
		probe.runtime = runtime
		return runtime
	}
	committed := store.writes
	_, err := activation.MaterializeProjectTopology(context.Background(), projectTopologyMaterializeRequest{Root: root, SessionName: "beta"})
	if err != nil {
		outcome.err = normalizeContinueOperationMarker(err.Error())
	}
	outcome.committed = store.writes != committed
	for _, write := range routeIdentityWrites(server.calls[start:]) {
		outcome.writes = append(outcome.writes, normalizeContinueOperationMarker(write))
	}
	outcome.identityReads = routeIdentityReads(outcome.calls)
	outcome.proofBeforeFirstWrite = continueIdentityEvidence(outcome.calls, true)
	outcome.readsBeforeFirstWrite = routeIdentityReadsBeforeFirstWrite(outcome.calls)
	outcome.identityEvidence = continueIdentityEvidence(outcome.calls, false)
	if outcome.firstReuseAt >= 0 {
		outcome.proofBeforeFirstReuse = continueIdentityEvidence(outcome.calls[:outcome.firstReuseAt], false)
		outcome.readsBeforeFirstReuse = routeIdentityReads(outcome.calls[:outcome.firstReuseAt])
	}
	outcome.panes = fakeTmuxPaneSet(server)
	outcome.warnings = warnings.String()
	return outcome
}

// continueReplayRoots is one pair of roots shared by every replay in a test, so
// two replays of the same plan emit the same `-c` paths.
func continueReplayRoots(t *testing.T) (string, string) {
	t.Helper()
	return t.TempDir(), t.TempDir()
}

// TestContinueReplayProvesItsFirstRouteIdentityAgainstTmux is boundary 1 for the
// Continue path: the cache reuses a proof, it never stands in for one. The
// replay proves the server against tmux before its first runtime write and
// before its first reuse, and only an exactly equal tuple is reusable.
func TestContinueReplayProvesItsFirstRouteIdentityAgainstTmux(t *testing.T) {
	t.Parallel()

	root, logs := continueReplayRoots(t)
	var (
		proved    runtimeRouteIdentityStats
		reusedKey bool
		provenKey runtimeRouteIdentityKey
	)
	outcome := runContinueReplay(t, root, logs, false, 0, func(probe *continueReplayProbe) func([]string) {
		return func([]string) {
			if probe.runtime == nil {
				return
			}
			runtime := probe.runtime
			cache := runtime.routeIdentity
			if cache == nil {
				return
			}
			if stats := cache.snapshot(); stats.Proofs > proved.Proofs || stats.Reuses > proved.Reuses {
				proved = stats
			}
			if reusedKey {
				return
			}
			target, _, err := runtime.exactMutationRoute()
			if err != nil {
				return
			}
			key, ok := runtime.exactRouteIdentityKey(target)
			if !ok {
				return
			}
			// The live transaction's own proof is reusable for the exact tuple
			// the materializer guards on. The probe names the same route target,
			// so a miss cannot invalidate anything the replay is relying on; it
			// simply asks again on the next call until the proof is there.
			if cache.reuse(key) {
				provenKey, reusedKey = key, true
			}
		}
	})
	if outcome.err != "" || !outcome.committed {
		t.Fatalf("Continue replay failed: err=%q committed=%v", outcome.err, outcome.committed)
	}
	if outcome.guardedWrites == 0 {
		t.Fatal("Continue replay issued no guarded write")
	}
	// The first proof of the transaction really ran: the server generation and
	// the app-ownership marker reached tmux before the first runtime write. The
	// logical socket-name marker is deliberately not in this list: the first
	// write of a Continue replay is the `new-session` that the route is bound
	// through, and the uncached path does not read that marker before it
	// either -- the parity check below is what pins that.
	if proof := outcome.proofBeforeFirstWrite; !proof.wrote || !proof.pid || !proof.app {
		t.Fatalf("Continue replay wrote before proving identity: %#v", proof)
	}
	// Boundary 1: nothing is reused before a full proof really ran. At the
	// replay's first reuse the cache already held a recorded proof, and the
	// server had already answered the whole read set -- socket path, server
	// generation, and both ownership markers.
	if outcome.firstReuseAt <= 0 || outcome.proofsAtFirstReuse == 0 {
		t.Fatalf("Continue replay reused at call %d with %d recorded proofs", outcome.firstReuseAt, outcome.proofsAtFirstReuse)
	}
	if proof := outcome.proofBeforeFirstReuse; !proof.socketPath || !proof.pid || !proof.app {
		t.Fatalf("first reuse followed an incomplete proof: %#v (%d identity reads)", proof, outcome.readsBeforeFirstReuse)
	}
	// The logical socket-name marker is the extra evidence the exact-route
	// scope keys on, and the replay really read it from tmux.
	if !outcome.identityEvidence.logical {
		t.Fatalf("Continue replay never read the logical route marker: %#v", outcome.identityEvidence)
	}
	if proved.Proofs == 0 {
		t.Fatalf("Continue transaction reused a proof that never ran: %#v", proved)
	}
	if !reusedKey {
		t.Fatalf("Continue transaction never reused its own exact-route proof: stats=%#v", proved)
	}
	if proved.Reuses == 0 {
		t.Fatalf("Continue transaction opened a scope it never reused: %#v", proved)
	}
	// A differing key is a miss: the exact tuple the replay proved is reusable,
	// and every component of it is part of the key.
	for _, drift := range []struct {
		name   string
		mutate func(*runtimeRouteIdentityKey)
	}{
		{name: "server pid", mutate: func(k *runtimeRouteIdentityKey) { k.ServerPID = "5252" }},
		{name: "physical socket", mutate: func(k *runtimeRouteIdentityKey) { k.PhysicalSocket = "/tmp/fake-tmux/other" }},
		{name: "socket name marker", mutate: func(k *runtimeRouteIdentityKey) { k.SocketNameMarker = "foreign" }},
		{name: "app marker", mutate: func(k *runtimeRouteIdentityKey) { k.AppMarker = "" }},
		{name: "guard scope", mutate: func(k *runtimeRouteIdentityKey) { k.Scope = routeIdentityScopeResolvedRoute }},
	} {
		cache := newRuntimeRouteIdentityCache("op-continue")
		cache.record(provenKey)
		if !cache.reuse(provenKey) {
			t.Fatalf("the replay's own proven tuple %#v was not reusable", provenKey)
		}
		differing := provenKey
		drift.mutate(&differing)
		if cache.reuse(differing) {
			t.Fatalf("differing %s reused the Continue replay's proof", drift.name)
		}
	}
	// A replay with the scope closed reads the same identity many more times,
	// and it sees the same evidence before its first write.
	uncached := runContinueReplay(t, root, logs, true, 0, nil)
	t.Logf("Continue replay identity reads: cached=%d uncached=%d over %d guarded writes (first reuse after %d reads at call %d)",
		outcome.identityReads, uncached.identityReads, outcome.guardedWrites,
		outcome.readsBeforeFirstReuse, outcome.firstReuseAt)
	if uncached.firstReuseAt >= 0 {
		t.Fatalf("a replay with the scope closed reused a proof at call %d", uncached.firstReuseAt)
	}
	if outcome.proofBeforeFirstWrite != uncached.proofBeforeFirstWrite {
		t.Fatalf("first Continue proof = %#v, want the uncached %#v", outcome.proofBeforeFirstWrite, uncached.proofBeforeFirstWrite)
	}
	if outcome.identityReads >= uncached.identityReads {
		t.Fatalf("cached Continue identity reads = %d, want fewer than uncached %d", outcome.identityReads, uncached.identityReads)
	}
}

// TestRouteIdentityCacheMatchesUncachedContinueReplayUnderDriftAfterEachGuardedWrite
// is the acceptance test for the Continue half: for every guarded write of a
// Project replay, restarting the server generation right after that write must
// produce the same refusal wording, the same argv sequence, the same Registry
// commit decision, and the same surviving Pane set whether or not the
// transaction was allowed to reuse identity proofs.
func TestRouteIdentityCacheMatchesUncachedContinueReplayUnderDriftAfterEachGuardedWrite(t *testing.T) {
	t.Parallel()

	root, logs := continueReplayRoots(t)
	cachedDry := runContinueReplay(t, root, logs, false, 0, nil)
	uncachedDry := runContinueReplay(t, root, logs, true, 0, nil)
	if cachedDry.err != "" || !cachedDry.committed {
		t.Fatalf("undrifted Continue replay failed: %s", cachedDry.err)
	}
	if !cachedDry.sameResult(uncachedDry) || cachedDry.guardedWrites != uncachedDry.guardedWrites || cachedDry.guardedWrites == 0 {
		t.Fatalf("undrifted Continue replay differs with the cache:\ncached:   err=%q committed=%v guarded=%d\n          writes=%q\n          panes=%q\nuncached: err=%q committed=%v guarded=%d\n          writes=%q\n          panes=%q",
			cachedDry.err, cachedDry.committed, cachedDry.guardedWrites, cachedDry.writes, cachedDry.panes,
			uncachedDry.err, uncachedDry.committed, uncachedDry.guardedWrites, uncachedDry.writes, uncachedDry.panes)
	}
	t.Logf("Continue replay: %d guarded writes, identity reads uncached=%d cached=%d",
		cachedDry.guardedWrites, uncachedDry.identityReads, cachedDry.identityReads)
	if cachedDry.identityReads >= uncachedDry.identityReads {
		t.Fatalf("Continue identity reads cached=%d, want fewer than uncached=%d", cachedDry.identityReads, uncachedDry.identityReads)
	}

	refusedCreate := false
	for k := 1; k <= cachedDry.guardedWrites; k++ {
		cached := runContinueReplay(t, root, logs, false, k, nil)
		uncached := runContinueReplay(t, root, logs, true, k, nil)
		write := uncached.guardedWrite[k]
		t.Logf("drift after guarded write %d (%s): committed=%v refused=%v", k, write, uncached.committed, uncached.err != "")
		if !cached.sameResult(uncached) {
			t.Errorf("drift after guarded write %d (%s) differs with the cache:\ncached:   err=%q committed=%v panes=%q\n          writes=%q\nuncached: err=%q committed=%v panes=%q\n          writes=%q",
				k, write, cached.err, cached.committed, cached.panes, cached.writes,
				uncached.err, uncached.committed, uncached.panes, uncached.writes)
		}
		if strings.HasPrefix(write, "new-session ") && uncached.err != "" {
			refusedCreate = true
		}
	}
	if !refusedCreate {
		t.Fatal("no drift after the new-session write refused the replay; the matrix no longer covers it")
	}
}

// TestContinueReplayClosesTheIdentityScopeBeforeItsRollback is boundary 6 for
// the Continue path: when the replay fails, the runtime ledger unwinds with the
// scope already closed, so every guard the rollback runs proves the server
// against tmux in full instead of trusting what the failed transaction proved.
func TestContinueReplayClosesTheIdentityScopeBeforeItsRollback(t *testing.T) {
	t.Parallel()

	root, logs := continueReplayRoots(t)
	dry := runContinueReplay(t, root, logs, false, 0, nil)
	if dry.err != "" {
		t.Fatalf("undrifted Continue replay failed: %s", dry.err)
	}
	// The split is the guarded write that leaves the most for a rollback to
	// unwind, so it is the drift that exercises the ledger hardest.
	split := 0
	for k := 1; k <= dry.guardedWrites; k++ {
		if strings.HasPrefix(dry.guardedWrite[k], "split-window ") {
			split = k
			break
		}
	}
	if split == 0 {
		t.Fatalf("Continue replay has no split write to drift after: %#v", dry.guardedWrite)
	}

	outcome := runContinueReplay(t, root, logs, false, split, nil)
	if outcome.err == "" || outcome.committed {
		t.Fatalf("drift after the split write did not refuse the replay: err=%q committed=%v", outcome.err, outcome.committed)
	}
	if outcome.scopeClosedAt < 0 {
		t.Fatal("the Continue transaction never closed its identity scope")
	}
	if outcome.scopeOpenAfterClose {
		t.Fatal("the Continue identity scope reopened after the transaction ended")
	}
	unwind := outcome.calls[outcome.scopeClosedAt:]
	if len(unwind) == 0 {
		t.Fatal("the failed Continue replay reached tmux no further than its transaction; the rollback is not covered")
	}
	// Everything after the scope closed -- the ledger rollback and the
	// create-operation cleanup -- re-reads the server identity rather than
	// standing on what the failed transaction proved. The proof stops at the
	// generation because that is what drifted; the ownership markers are only
	// read by a proof that gets that far.
	if reads := routeIdentityReads(unwind); reads == 0 {
		t.Fatalf("the Continue rollback issued %d calls without one identity read", len(unwind))
	}
	if proof := continueIdentityEvidence(unwind, false); !proof.socketPath || !proof.pid {
		t.Fatalf("the Continue rollback did not re-prove identity: %#v", proof)
	}
	if !strings.Contains(outcome.warnings, "drifted") {
		t.Fatalf("the Continue rollback did not run through the runtime ledger guard: warnings=%q", outcome.warnings)
	}
}

// TestContinueReplayCommitReproofRefusesDriftBeforeCommitWithoutAWrite is
// boundary 5 for the Continue path: a replay that reused proofs proves the exact
// route once more as the last statement of its transaction, so a server that
// drifted in the write-free window before the Registry commit refuses the commit
// with the established route-identity error family instead of committing on
// stale evidence.
func TestContinueReplayCommitReproofRefusesDriftBeforeCommitWithoutAWrite(t *testing.T) {
	t.Parallel()

	root, logs := continueReplayRoots(t)
	var (
		flippedAt      = -1
		guardedAfter   int
		reusesAtCommit int
	)
	outcome := runContinueReplay(t, root, logs, false, 0, func(probe *continueReplayProbe) func([]string) {
		return func([]string) {
			if probe.runtime == nil {
				return
			}
			cache := probe.runtime.routeIdentity
			if cache == nil {
				return
			}
			stats := cache.snapshot()
			if flippedAt >= 0 {
				return
			}
			// reproveReusedRouteIdentity drops the transaction's proofs under
			// this exact reason before it reads tmux, so this is the first call
			// of the commit re-proof and the window behind it held no write.
			if stats.LastInvalidation != "commit-reproof" {
				return
			}
			flippedAt, reusesAtCommit = len(probe.outcome.calls), stats.Reuses
			guardedAfter = probe.outcome.guardedWrites
			probe.server.serverPID = "5252"
		}
	})
	if flippedAt < 0 {
		t.Fatal("the Continue transaction never reached its commit re-proof")
	}
	if reusesAtCommit == 0 {
		t.Fatalf("the commit re-proof ran for a transaction that reused nothing: %d", reusesAtCommit)
	}
	if outcome.guardedWrites != guardedAfter {
		t.Fatalf("the drift window was not write-free: %d guarded writes followed it", outcome.guardedWrites-guardedAfter)
	}
	if writes := routeIdentityWrites(outcome.calls[flippedAt:]); len(writes) != 0 {
		t.Fatalf("the commit re-proof window reached tmux with a write: %q", writes)
	}
	const wantCommitRefusal = "topology materialization: runtime mutation plan: route identity refused commit after reused proofs: planned runtime server generation drifted"
	if outcome.err != wantCommitRefusal {
		t.Fatalf("Continue commit refusal = %q, want %q", outcome.err, wantCommitRefusal)
	}
	if outcome.committed {
		t.Fatal("the drifted Continue replay committed the Registry")
	}
	if outcome.scopeClosedAt < 0 || outcome.scopeOpenAfterClose {
		t.Fatalf("the refused Continue transaction left its scope open: closedAt=%d reopened=%v", outcome.scopeClosedAt, outcome.scopeOpenAfterClose)
	}
	if !strings.Contains(outcome.warnings, "drifted") {
		t.Fatalf("the refused Continue replay did not unwind through the runtime ledger: warnings=%q", outcome.warnings)
	}
}
