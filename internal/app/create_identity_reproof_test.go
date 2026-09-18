package app

import (
	"context"
	"slices"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// Replacement-injection tests for the readers that started sharing the
// create transaction's route identity scope -- the typed metadata mirror and
// the create-operation lease clear that now runs inside the scope on success --
// and for the proof the transaction's first write keeps: the route bind, read
// before the Registry lock, never stands in for it.
//
// The #1012 rule these pin: no guarded write relies on a proof older than the
// previous guarded write. A server swap (#{pid}), a socket swap, or a marker
// change right after a guarded write is refused before the next write, with
// the same outcome as the uncached path.

type routeDriftKind struct {
	name  string
	apply func(*fakeTmux)
}

func routeDriftKinds() []routeDriftKind {
	return []routeDriftKind{
		{name: "server generation swap", apply: func(s *fakeTmux) { s.serverPID = "5252" }},
		{name: "socket swap", apply: func(s *fakeTmux) { s.socketPath = "/tmp/fake-tmux/replacement" }},
		{name: "app marker cleared", apply: func(s *fakeTmux) { s.appMarker = "" }},
		{name: "logical marker changed", apply: func(s *fakeTmux) { s.socketName = "foreign" }},
	}
}

// driftAfterFirstWriteRunner applies one drift right after the first runtime
// write it forwards, before anything can observe that write's effect.
type driftAfterFirstWriteRunner struct {
	base  *fakeTmux
	drift func(*fakeTmux)
	fired bool
}

func (r *driftAfterFirstWriteRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := r.base.Run(ctx, name, args...)
	if !r.fired && r.drift != nil && isRouteIdentityWrite(tmuxCommandArgv(args)) {
		r.fired = true
		r.drift(r.base)
	}
	return out, err
}

// typedMirrorCase is one typed metadata mirror writer over the route identity
// fixture, with its target left unconverged so it has writes to make.
type typedMirrorCase struct {
	name string
	run  func(context.Context, runtimeMutationMetadataMirror, *fakeTmux) error
}

func typedMirrorCases() []typedMirrorCase {
	window := coremetadata.Window{Metadata: coremetadata.ObjectMeta{
		UID: "win-route-identity", Name: "main",
		OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindProject, UID: "prj-route-identity"},
	}}
	pane := coremetadata.Pane{
		Metadata: coremetadata.ObjectMeta{
			UID: routeIdentityPaneUID, Name: "shell",
			OwnerRef: &coremetadata.OwnerRef{Kind: coremetadata.KindWindow, UID: "win-route-identity"},
		},
		Spec: coremetadata.PaneSpec{Role: coremetadata.PaneRoleShell},
	}
	project := coremetadata.Project{Metadata: coremetadata.ObjectMeta{UID: "prj-route-identity", Name: "route-identity"}}
	return []typedMirrorCase{
		{name: "MirrorProject", run: func(ctx context.Context, mirror runtimeMutationMetadataMirror, _ *fakeTmux) error {
			return mirror.MirrorProject(ctx, "route-identity", project)
		}},
		{name: "MirrorWindow", run: func(ctx context.Context, mirror runtimeMutationMetadataMirror, server *fakeTmux) error {
			return mirror.MirrorWindow(ctx, server.session("route-identity").windows[0].id, window)
		}},
		{name: "MirrorPane", run: func(ctx context.Context, mirror runtimeMutationMetadataMirror, server *fakeTmux) error {
			return mirror.MirrorPane(ctx, server.session("route-identity").windows[0].panes[0].id, "win-route-identity", pane)
		}},
	}
}

// newScopedTypedMirror builds a typed mirror on the route the materializer
// speaks for. scoped shares the materializer's transaction scope; otherwise the
// mirror is exactly the pre-scope writer.
func newScopedTypedMirror(runtime *materializer, runner tmuxCommandRunner, scoped bool) runtimeMutationMetadataMirror {
	mirror := runtimeMutationMetadataMirror{
		runner: explicitTmuxRunner{runner: runner, target: runtime.target},
		route: runtimeMutationRoute{
			target: runtime.target, expectedSocketPath: runtime.expectedSocketPath,
			socketName: runtime.socketName, authority: runtime.routeAuthority,
		},
	}
	if scoped {
		mirror.scope = runtime
	}
	return mirror
}

func TestTypedMetadataMirrorReusesTheTransactionProofOnlyUntilItsOwnWrite(t *testing.T) {
	t.Parallel()

	for _, test := range typedMirrorCases() {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			runtime, server, _ := newRouteIdentityFixture()
			runtime.openRouteIdentityCache("op-mirror")
			defer runtime.closeRouteIdentityCache()
			if err := runtime.guardExactRoute(ctx, false, runtime.expectedSocketPath); err != nil {
				t.Fatalf("first transaction proof: %v", err)
			}
			mirror := newScopedTypedMirror(runtime, server, true)

			start := len(server.calls)
			if err := test.run(ctx, mirror, server); err != nil {
				t.Fatalf("unconverged %s: %v", test.name, err)
			}
			calls := server.calls[start:]
			writes := routeIdentityWrites(calls)
			if len(writes) == 0 {
				t.Fatalf("unconverged %s wrote nothing; the fixture no longer exercises a write", test.name)
			}
			if reads := routeIdentityReadsBeforeFirstWrite(calls); reads != 0 {
				t.Fatalf("%s re-read identity %d times before its first write despite a proof no write had followed", test.name, reads)
			}
			segments := routeIdentitySegments(calls)
			if last := segments[len(segments)-1]; last.socketPath == 0 || last.pid == 0 {
				t.Fatalf("%s post-effect observation reused a proof older than its own write: segments=%#v", test.name, segments)
			}
			if got := runtime.guardedWrites; got != uint64(len(writes)) {
				t.Fatalf("%s writes through the guarded-write seam = %d, want every one of %d", test.name, got, len(writes))
			}

			// Converged now, with its own post-effect proof the newest evidence:
			// a repeat writes nothing and reads no identity.
			start = len(server.calls)
			if err := test.run(ctx, mirror, server); err != nil {
				t.Fatalf("converged %s: %v", test.name, err)
			}
			if writes := routeIdentityWrites(server.calls[start:]); len(writes) != 0 {
				t.Fatalf("converged %s wrote %q", test.name, writes)
			}
			if reads := routeIdentityReads(server.calls[start:]); reads != 0 {
				t.Fatalf("converged %s read identity %d times inside the transaction scope, want 0", test.name, reads)
			}

			// Outside a scope the same converged mirror proves the route itself.
			start = len(server.calls)
			if err := test.run(ctx, newScopedTypedMirror(runtime, server, false), server); err != nil {
				t.Fatalf("unscoped converged %s: %v", test.name, err)
			}
			if reads := routeIdentityReads(server.calls[start:]); reads == 0 {
				t.Fatalf("unscoped %s read no identity; it must prove the route in full", test.name)
			}
		})
	}
}

func TestTypedMetadataMirrorRefusesServerReplacementBeforeTheNextWriteLikeUncached(t *testing.T) {
	t.Parallel()

	type outcome struct {
		mirrorErr, nextErr    string
		mirrorWrites, nextOut []string
	}
	for _, test := range typedMirrorCases() {
		for _, drift := range routeDriftKinds() {
			t.Run(test.name+"/"+drift.name, func(t *testing.T) {
				t.Parallel()
				run := func(scoped bool) outcome {
					ctx := context.Background()
					runtime, server, paneID := newRouteIdentityFixture()
					if scoped {
						runtime.openRouteIdentityCache("op-mirror-drift")
						defer runtime.closeRouteIdentityCache()
						if err := runtime.guardExactRoute(ctx, false, runtime.expectedSocketPath); err != nil {
							t.Fatalf("first transaction proof: %v", err)
						}
					}
					runner := &driftAfterFirstWriteRunner{base: server, drift: drift.apply}
					var got outcome
					start := len(server.calls)
					if err := test.run(ctx, newScopedTypedMirror(runtime, runner, scoped), server); err != nil {
						got.mirrorErr = err.Error()
					}
					if !runner.fired {
						t.Fatalf("%s never wrote; the drift was not injected", test.name)
					}
					got.mirrorWrites = routeIdentityWrites(server.calls[start:])
					// The next guarded write of the same transaction.
					start = len(server.calls)
					if err := unsetLegacyTopic(runtime, server, paneID); err != nil {
						got.nextErr = err.Error()
					}
					got.nextOut = routeIdentityWrites(server.calls[start:])
					return got
				}
				cached, uncached := run(true), run(false)
				if cached.mirrorErr == "" || cached.nextErr == "" {
					t.Fatalf("%s after the mirror's write was not refused: mirror=%q next=%q", drift.name, cached.mirrorErr, cached.nextErr)
				}
				if len(cached.nextOut) != 0 {
					t.Fatalf("the write after the drift reached tmux: %q", cached.nextOut)
				}
				if cached.mirrorErr != uncached.mirrorErr || cached.nextErr != uncached.nextErr ||
					!slices.Equal(cached.mirrorWrites, uncached.mirrorWrites) || !slices.Equal(cached.nextOut, uncached.nextOut) {
					t.Fatalf("scoped mirror differs from the uncached writer under %s:\nscoped:   %#v\nuncached: %#v", drift.name, cached, uncached)
				}
			})
		}
	}
}

// createWindowWithBindHook runs the production-wired create window with after
// called once, right after the route bind returns.
func createWindowWithBindHook(t *testing.T, cacheOff bool, after func(*callBudgetFixture)) (callBudgetFixture, createDriftOutcome, int) {
	t.Helper()
	fixture := newCallBudgetFixture(t, 1)
	fixture.create.runtime.routeIdentityDisabled = cacheOff
	bindEnd := -1
	bind := fixture.create.bindExplicitRuntime
	fixture.create.bindExplicitRuntime = func(ctx context.Context) error {
		if err := bind(ctx); err != nil {
			return err
		}
		bindEnd = len(fixture.tmux.calls)
		if after != nil {
			after(&fixture)
		}
		return nil
	}
	committed := fixture.store.writes
	stdout, _, err := runRoute(t, fixture.create, "window", "--project", "uid:prj-target", "--name", "probe", "-o", "uid")
	got := createDriftOutcome{stdout: stdout, committed: fixture.store.writes != committed, panes: fakeTmuxPaneSet(fixture.tmux)}
	if err != nil {
		got.err = err.Error()
	}
	got.writes = routeIdentityWrites(fixture.tmux.calls)
	got.identityReads = routeIdentityReads(fixture.tmux.calls)
	if bindEnd < 0 {
		t.Fatal("create window never bound its route")
	}
	return fixture, got, bindEnd
}

func TestCreateWindowProvesIdentityUnderTheTransactionBeforeItsFirstWrite(t *testing.T) {
	t.Parallel()

	fixture, got, bindEnd := createWindowWithBindHook(t, false, nil)
	if got.err != "" || !got.committed {
		t.Fatalf("create window: err=%q committed=%v", got.err, got.committed)
	}
	// The route bind's own reads are not the transaction's proof: after the
	// bind and before the first write, the transaction reads the server
	// generation and both ownership markers against tmux itself.
	var pid, app, logical bool
	wrote := false
	for _, call := range fixture.tmux.calls[bindEnd:] {
		argv := tmuxCommandArgv(call)
		if isRouteIdentityWrite(argv) {
			wrote = true
			break
		}
		pid = pid || (argv[0] == "display-message" && flagValue(argv, "-t") == "" && flagValue(argv, "-F") == "#{pid}")
		app = app || (argv[0] == "show-options" && slices.Contains(argv, "-gqv") && slices.Contains(argv, tmuxopts.AppGlobal))
		logical = logical || (argv[0] == "show-options" && slices.Contains(argv, "-gqv") && slices.Contains(argv, runtimeMutationSocketNameOption))
	}
	if !wrote {
		t.Fatal("create window issued no runtime write")
	}
	if !pid || !app || !logical {
		t.Fatalf("first write after the bind without a full identity proof under the transaction (pid=%v app=%v logical=%v)", pid, app, logical)
	}

	for _, drift := range routeDriftKinds() {
		t.Run("drift between bind and first write/"+drift.name, func(t *testing.T) {
			t.Parallel()
			inject := func(f *callBudgetFixture) { drift.apply(f.tmux) }
			_, cached, _ := createWindowWithBindHook(t, false, inject)
			_, uncached, _ := createWindowWithBindHook(t, true, inject)
			if cached.err == "" || cached.committed || cached.stdout != "" || len(cached.writes) != 0 {
				t.Fatalf("drift after the bind was not refused before any write: err=%q committed=%v stdout=%q writes=%q",
					cached.err, cached.committed, cached.stdout, cached.writes)
			}
			if !cached.sameResult(uncached) {
				t.Fatalf("drift after the bind differs with the cache:\ncached=%#v\nuncached=%#v", cached, uncached)
			}
		})
	}
}

// commitDriftStore applies drift once, right after the Registry commit returns
// and before the create-operation lease clear.
func commitDriftStore(store *resourceStore, drift func()) {
	update := store.update
	store.update = func(fn func(*coremetadata.Registry) error) (coremetadata.Registry, error) {
		registry, err := update(fn)
		if err == nil && drift != nil {
			drift()
			drift = nil
		}
		return registry, err
	}
}

func TestCreateLeaseClearReusesTheCommitProofAndReprovesAfterItsWrite(t *testing.T) {
	t.Parallel()

	fixture := newCallBudgetFixture(t, 1)
	if _, _, err := runRoute(t, fixture.create, "window", "--project", "uid:prj-target", "--name", "probe", "-o", "uid"); err != nil {
		t.Fatalf("create window: %v", err)
	}
	calls := fixture.tmux.calls
	clear := slices.IndexFunc(calls, func(call []string) bool {
		argv := tmuxCommandArgv(call)
		return len(argv) > 0 && argv[0] == "set-environment" && slices.Contains(argv, "-u")
	})
	if clear < 0 {
		t.Fatal("create window never cleared its create-operation lease")
	}
	previousWrite := -1
	for i := clear - 1; i >= 0; i-- {
		if isRouteIdentityWrite(tmuxCommandArgv(calls[i])) {
			previousWrite = i
			break
		}
	}
	// Between the previous guarded write and the lease clear: that write's
	// post-effect proof, then the commit re-proof, and nothing from the lease
	// clear's own guards, which reuse the commit re-proof.
	window := calls[previousWrite+1 : clear]
	segments := routeIdentitySegments(window)
	if reads := routeIdentityReads(window); reads != 8 || segments[0].socketPath != 2 || segments[0].pid != 2 {
		t.Fatalf("identity reads between the previous write and the lease clear = %d (%#v), want the post-effect proof and the commit re-proof only",
			reads, segments)
	}
	tail := routeIdentitySegments(calls[clear:])
	if last := tail[len(tail)-1]; last.socketPath == 0 || last.pid == 0 {
		t.Fatalf("lease clear post-effect observation did not re-prove identity: %#v", tail)
	}
	if fixture.create.runtime.routeIdentity != nil {
		t.Fatal("create left its identity scope open after the lease clear")
	}

	for _, drift := range routeDriftKinds() {
		t.Run("drift after the lease clear write/"+drift.name, func(t *testing.T) {
			t.Parallel()
			run := func(cacheOff bool) (createDriftOutcome, string) {
				f := newCallBudgetFixture(t, 1)
				f.create.runtime.routeIdentityDisabled = cacheOff
				var warnings strings.Builder
				f.create.runtime.warn = &warnings
				f.create.runtime.afterGuardedWrite = func() {
					if argv := tmuxCommandArgv(f.tmux.calls[len(f.tmux.calls)-1]); len(argv) > 0 &&
						argv[0] == "set-environment" && slices.Contains(argv, "-u") {
						drift.apply(f.tmux)
					}
				}
				stdout, _, err := runRoute(t, f.create, "window", "--project", "uid:prj-target", "--name", "probe", "-o", "uid")
				got := createDriftOutcome{stdout: stdout, writes: routeIdentityWrites(f.tmux.calls), panes: fakeTmuxPaneSet(f.tmux)}
				if err != nil {
					got.err = err.Error()
				}
				return got, warnings.String()
			}
			cached, cachedWarn := run(false)
			uncached, uncachedWarn := run(true)
			if !cached.sameResult(uncached) || cached.err != "" {
				t.Fatalf("drift after the lease clear differs with the scope:\ncached=%#v\nuncached=%#v", cached, uncached)
			}
			const reported = "could not clear guarded create-operation lease(s)"
			if !strings.Contains(cachedWarn, reported) || !strings.Contains(uncachedWarn, reported) {
				t.Fatalf("lease clear post-effect proof did not report the drift:\ncached=%q\nuncached=%q", cachedWarn, uncachedWarn)
			}
		})
	}

	// The one window this change opens: after the commit re-proof and before
	// the lease clear no write of ours runs, so the clear's guards reuse that
	// proof. A replacement there is reported by the clear's own post-effect
	// proof after its write, and the committed create stands.
	t.Run("drift after the commit re-proof is reported by the lease clear's post-effect proof", func(t *testing.T) {
		t.Parallel()
		f := newCallBudgetFixture(t, 1)
		var warnings strings.Builder
		f.create.runtime.warn = &warnings
		commitDriftStore(f.create.store, func() { f.tmux.serverPID = "5252" })
		committed := f.store.writes
		if _, _, err := runRoute(t, f.create, "window", "--project", "uid:prj-target", "--name", "probe", "-o", "uid"); err != nil {
			t.Fatalf("committed create reported %v", err)
		}
		if f.store.writes == committed {
			t.Fatal("create did not commit")
		}
		if !strings.Contains(warnings.String(), "could not clear guarded create-operation lease(s)") ||
			!strings.Contains(warnings.String(), "server generation drifted") {
			t.Fatalf("lease clear did not report the post-commit drift: %q", warnings.String())
		}
	})
}
