package app

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

const routeIdentityPaneUID = "pan-route-identity"

// isRouteIdentityRead reports whether one tmux argv is a server-identity
// re-proof read: the physical socket, the server generation, or one of the two
// ownership markers. Target-scoped display-message reads are effect
// observations, not identity reads.
func isRouteIdentityRead(argv []string) bool {
	if len(argv) == 0 {
		return false
	}
	switch argv[0] {
	case "display-message":
		if flagValue(argv, "-t") != "" {
			return false
		}
		format := flagValue(argv, "-F")
		return format == "#{socket_path}" || format == "#{pid}"
	case "show-options":
		return slices.Contains(argv, "-gqv") &&
			(slices.Contains(argv, tmuxopts.AppGlobal) || slices.Contains(argv, runtimeMutationSocketNameOption))
	}
	return false
}

func routeIdentityReads(calls [][]string) int {
	reads := 0
	for _, call := range calls {
		if isRouteIdentityRead(tmuxCommandArgv(call)) {
			reads++
		}
	}
	return reads
}

var routeIdentityWriteVerbs = []string{
	"set-option", "set-environment", "split-window", "new-window", "new-session", "resize-pane",
	"rename-window", "kill-session", "kill-window", "kill-pane", "select-pane", "select-window", "switch-client",
}

func isRouteIdentityWrite(argv []string) bool {
	return len(argv) > 0 && slices.Contains(routeIdentityWriteVerbs, argv[0])
}

// routeIdentityWrites is the ordered exact argv of every runtime write.
func routeIdentityWrites(calls [][]string) []string {
	var writes []string
	for _, call := range calls {
		if isRouteIdentityWrite(tmuxCommandArgv(call)) {
			writes = append(writes, strings.Join(call, " "))
		}
	}
	return writes
}

// routeIdentityReadsBeforeFirstWrite counts the identity reads a guarded call
// makes before it reaches tmux with its first write.
func routeIdentityReadsBeforeFirstWrite(calls [][]string) int {
	reads := 0
	for _, call := range calls {
		argv := tmuxCommandArgv(call)
		if isRouteIdentityWrite(argv) {
			break
		}
		if isRouteIdentityRead(argv) {
			reads++
		}
	}
	return reads
}

// routeIdentityProofSegment is the identity evidence read between two writes.
type routeIdentityProofSegment struct {
	reads, socketPath, pid int
}

// routeIdentitySegments splits the calls at every write: segment 0 precedes the
// first write and segment i follows write i.
func routeIdentitySegments(calls [][]string) []routeIdentityProofSegment {
	segments := []routeIdentityProofSegment{{}}
	for _, call := range calls {
		argv := tmuxCommandArgv(call)
		if isRouteIdentityWrite(argv) {
			segments = append(segments, routeIdentityProofSegment{})
			continue
		}
		if !isRouteIdentityRead(argv) {
			continue
		}
		segment := &segments[len(segments)-1]
		segment.reads++
		switch flagValue(argv, "-F") {
		case "#{socket_path}":
			segment.socketPath++
		case "#{pid}":
			segment.pid++
		}
	}
	return segments
}

// newRouteIdentityFixture binds a materializer to one app-owned fake server,
// with one Pane whose claimed uid lets runIdentityWrites run a real guarded plan.
func newRouteIdentityFixture() (*materializer, *fakeTmux, string) {
	server := newFakeTmux()
	session := server.addSession("route-identity")
	session.opts[tmuxopts.ProjectUIDSession] = "prj-route-identity"
	session.windows[0].opts[tmuxopts.WindowUID] = "win-route-identity"
	pane := session.windows[0].panes[0]
	pane.opts[tmuxopts.PaneUID] = routeIdentityPaneUID
	target := tmuxTransport{Kind: tmuxSocketPath, Value: server.socketPath, Source: tmuxSocketPathSource}
	return &materializer{
		runner: explicitTmuxRunner{runner: server, target: target},
		target: target, expectedSocketPath: server.socketPath,
		socketName:     defaultAppSocket,
		routeAuthority: &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"},
	}, server, pane.id
}

// unsetLegacyTopic runs one guarded identity write that always has a pending
// effect: it re-seeds the legacy topic, then plans its removal.
func unsetLegacyTopic(runtime *materializer, server *fakeTmux, paneID string) error {
	if _, _, pane := server.pane(paneID); pane != nil {
		pane.opts[aiPaneTopicOption] = "legacy"
	}
	return runtime.runIdentityWrites(context.Background(), "pane", paneID, routeIdentityPaneUID, []identityPlanWrite{{
		operands: []string{"-p", "-u", "-t", paneID, aiPaneTopicOption},
		effect:   "legacy AI topic projection absent",
	}})
}

func appRouteIdentityKey() runtimeRouteIdentityKey {
	return runtimeRouteIdentityKey{
		Scope: routeIdentityScopeResolvedRoute, SocketFlag: "-L", SocketValue: "app",
		PhysicalSocket: "/tmp/tmux-1000/app", AuthorityClass: runtimeMutationRouteApp, ServerPID: "4242",
		AppMarker: "1", SocketNameMarker: "app",
	}
}

func standaloneRouteIdentityKey(class string) runtimeRouteIdentityKey {
	return runtimeRouteIdentityKey{
		Scope: routeIdentityScopeResolvedRoute, SocketFlag: "-S", SocketValue: "/tmp/tmux-1000/default",
		PhysicalSocket: "/tmp/tmux-1000/default", AuthorityClass: class, ServerPID: "4242",
	}
}

func TestRuntimeRouteIdentityCacheReusesOnlyTheExactProvenTuple(t *testing.T) {
	t.Parallel()

	var absent *runtimeRouteIdentityCache
	if absent.reuse(appRouteIdentityKey()) {
		t.Fatal("nil cache reused an identity")
	}
	if newRuntimeRouteIdentityCache("") != nil {
		t.Fatal("blank operation id opened a cache")
	}

	cold := newRuntimeRouteIdentityCache("op-cold")
	if cold.reuse(appRouteIdentityKey()) {
		t.Fatal("first identity check of a transaction reused a proof that never ran")
	}

	for _, test := range []struct {
		name   string
		mutate func(*runtimeRouteIdentityKey)
	}{
		{name: "server pid", mutate: func(k *runtimeRouteIdentityKey) { k.ServerPID = "5252" }},
		{name: "physical socket path", mutate: func(k *runtimeRouteIdentityKey) { k.PhysicalSocket = "/tmp/tmux-1000/other" }},
		{name: "socket flag", mutate: func(k *runtimeRouteIdentityKey) { k.SocketFlag, k.SocketValue = "-S", k.PhysicalSocket }},
		{name: "logical socket name route", mutate: func(k *runtimeRouteIdentityKey) { k.SocketValue = "other" }},
		{name: "authority class", mutate: func(k *runtimeRouteIdentityKey) { *k = standaloneRouteIdentityKey(runtimeMutationRouteStandalone) }},
		{name: "socket name marker", mutate: func(k *runtimeRouteIdentityKey) { k.SocketNameMarker = "foreign" }},
		{name: "app marker", mutate: func(k *runtimeRouteIdentityKey) { k.AppMarker = "" }},
		{name: "guard scope", mutate: func(k *runtimeRouteIdentityKey) { k.Scope = routeIdentityScopeExactRoute }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cache := newRuntimeRouteIdentityCache("op-key")
			cache.record(appRouteIdentityKey())
			if !cache.reuse(appRouteIdentityKey()) {
				t.Fatal("exact proven tuple was not reused")
			}
			differing := appRouteIdentityKey()
			test.mutate(&differing)
			if cache.reuse(differing) {
				t.Fatalf("differing %s reused the proof of %#v", test.name, appRouteIdentityKey())
			}
		})
	}

	t.Run("standalone classes are distinct", func(t *testing.T) {
		t.Parallel()
		cache := newRuntimeRouteIdentityCache("op-standalone")
		cache.record(standaloneRouteIdentityKey(runtimeMutationRouteStandalone))
		if !cache.reuse(standaloneRouteIdentityKey(runtimeMutationRouteStandalone)) {
			t.Fatal("exact standalone tuple was not reused")
		}
		if cache.reuse(standaloneRouteIdentityKey(runtimeMutationRouteStandaloneExplicit)) {
			t.Fatal("standalone-explicit reused a standalone proof")
		}
	})

	t.Run("incomplete identities are never cached", func(t *testing.T) {
		t.Parallel()
		for _, key := range []runtimeRouteIdentityKey{
			{},
			func() runtimeRouteIdentityKey { k := appRouteIdentityKey(); k.ServerPID = ""; return k }(),
			func() runtimeRouteIdentityKey { k := appRouteIdentityKey(); k.PhysicalSocket = ""; return k }(),
			func() runtimeRouteIdentityKey { k := appRouteIdentityKey(); k.PhysicalSocket = "relative"; return k }(),
			func() runtimeRouteIdentityKey { k := appRouteIdentityKey(); k.SocketNameMarker = ""; return k }(),
			func() runtimeRouteIdentityKey { k := appRouteIdentityKey(); k.AuthorityClass = "unknown"; return k }(),
			func() runtimeRouteIdentityKey {
				k := standaloneRouteIdentityKey(runtimeMutationRouteStandalone)
				k.AppMarker = "1"
				return k
			}(),
		} {
			cache := newRuntimeRouteIdentityCache("op-incomplete")
			cache.record(key)
			if cache.reuse(key) || cache.snapshot().Proofs != 0 {
				t.Fatalf("incomplete identity %#v was cached", key)
			}
		}
	})

	t.Run("route key requires a captured generation", func(t *testing.T) {
		t.Parallel()
		route := runtimeMutationRoute{
			target:     tmuxTransport{Kind: tmuxSocketName, Value: "app", Source: tmuxSocketNameSource},
			socketName: "app",
		}
		if _, ok := runtimeRouteIdentityKeyForRoute(routeIdentityScopeResolvedRoute, route); ok {
			t.Fatal("route without physical socket or authority was cacheable")
		}
		route.expectedSocketPath = "/tmp/tmux-1000/app"
		if _, ok := runtimeRouteIdentityKeyForRoute(routeIdentityScopeResolvedRoute, route); ok {
			t.Fatal("route without server generation was cacheable")
		}
		route.authority = &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"}
		key, ok := runtimeRouteIdentityKeyForRoute(routeIdentityScopeResolvedRoute, route)
		if !ok || key != appRouteIdentityKey() {
			t.Fatalf("route key = %#v, %v; want %#v", key, ok, appRouteIdentityKey())
		}
	})
}

func TestRuntimeRouteIdentityCacheInvalidationTriggers(t *testing.T) {
	t.Parallel()

	t.Run("server swap drops every proof", func(t *testing.T) {
		t.Parallel()
		cache := newRuntimeRouteIdentityCache("op-swap")
		cache.record(appRouteIdentityKey())
		swapped := appRouteIdentityKey()
		swapped.PhysicalSocket = "/tmp/tmux-1000/replacement"
		if cache.reuse(swapped) {
			t.Fatal("swapped server reused the old proof")
		}
		if cache.reuse(appRouteIdentityKey()) {
			t.Fatal("old server proof survived a server swap")
		}
		if got := cache.snapshot(); got.Invalidations != 1 || got.LastInvalidation != "route-target-changed" {
			t.Fatalf("swap invalidation = %#v", got)
		}
	})

	t.Run("transaction end closes the scope", func(t *testing.T) {
		t.Parallel()
		cache := newRuntimeRouteIdentityCache("op-end")
		cache.record(appRouteIdentityKey())
		cache.close()
		cache.close()
		if cache.reuse(appRouteIdentityKey()) {
			t.Fatal("closed transaction scope reused a proof")
		}
		cache.record(appRouteIdentityKey())
		if cache.reuse(appRouteIdentityKey()) {
			t.Fatal("closed transaction scope accepted a new proof")
		}
		if got := cache.snapshot(); !got.Closed || got.LastInvalidation != "transaction-end" {
			t.Fatalf("closed cache stats = %#v", got)
		}
	})

	// Each materializer trigger is followed by the same guarded write. The
	// control proves that write's pre-write guards reuse the previous write's
	// post-effect proof; every trigger must make those pre-write guards read
	// tmux again, and the write must still succeed.
	for _, test := range []struct {
		name    string
		trigger func(*materializer, *fakeTmux, string)
		// wantReason is the invalidation the trigger itself records.
		wantReason string
		// proofInTrigger marks a trigger whose own guard is the full check.
		proofInTrigger bool
	}{
		{name: "control keeps the proof", trigger: func(*materializer, *fakeTmux, string) {}},
		{
			name: "server pid change",
			trigger: func(runtime *materializer, server *fakeTmux, _ string) {
				server.serverPID = "5252"
				runtime.routeAuthority = &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "5252"}
			},
		},
		{
			name: "socket path change",
			trigger: func(runtime *materializer, server *fakeTmux, _ string) {
				server.socketPath = "/tmp/fake-tmux/moved"
				target := tmuxTransport{Kind: tmuxSocketPath, Value: server.socketPath, Source: tmuxSocketPathSource}
				runtime.runner = explicitTmuxRunner{runner: server, target: target}
				runtime.target, runtime.expectedSocketPath = target, server.socketPath
			},
		},
		{
			name:       "reconnection rebinds the route",
			trigger:    func(runtime *materializer, _ *fakeTmux, _ string) { runtime.invalidateRouteIdentity("route-bind") },
			wantReason: "route-bind",
		},
		{
			name: "route re-resolution",
			trigger: func(runtime *materializer, _ *fakeTmux, _ string) {
				runtime.routeAuthority = nil
				if err := runtime.guardExactRoute(context.Background(), false, runtime.expectedSocketPath); err != nil {
					t.Errorf("re-resolve exact route: %v", err)
				}
			},
			wantReason:     "route-re-resolution",
			proofInTrigger: true,
		},
		{
			name: "probe error",
			trigger: func(runtime *materializer, server *fakeTmux, paneID string) {
				runtime.routeAuthority = &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "5252"}
				if err := unsetLegacyTopic(runtime, server, paneID); err == nil || !strings.Contains(err.Error(), "server generation drifted") {
					t.Errorf("mismatched generation probe = %v", err)
				}
				runtime.routeAuthority = &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: "4242"}
			},
			wantReason: "resolved-route-probe-error",
		},
		{
			name: "transaction end",
			trigger: func(runtime *materializer, _ *fakeTmux, _ string) {
				runtime.closeRouteIdentityCache()
				runtime.openRouteIdentityCache("op-next")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runtime, server, paneID := newRouteIdentityFixture()
			runtime.openRouteIdentityCache("op-trigger")
			defer runtime.closeRouteIdentityCache()
			if err := unsetLegacyTopic(runtime, server, paneID); err != nil {
				t.Fatalf("warm transaction proof: %v", err)
			}
			start := len(server.calls)
			test.trigger(runtime, server, paneID)
			if test.wantReason != "" {
				if got := runtime.routeIdentity.snapshot().LastInvalidation; got != test.wantReason {
					t.Fatalf("last invalidation = %q, want %q", got, test.wantReason)
				}
			}
			if !test.proofInTrigger {
				start = len(server.calls)
			}
			if err := unsetLegacyTopic(runtime, server, paneID); err != nil {
				t.Fatalf("guarded write after trigger: %v", err)
			}
			reads := routeIdentityReadsBeforeFirstWrite(server.calls[start:])
			if test.name == "control keeps the proof" {
				if reads != 0 {
					t.Fatalf("control pre-write guards re-read identity %d times, want reuse", reads)
				}
				return
			}
			if reads == 0 {
				t.Fatalf("%s reused a proof before the write; want a full identity check", test.name)
			}
		})
	}
}

func TestMaterializerRouteGuardReprovesIdentityAfterEveryGuardedWrite(t *testing.T) {
	t.Parallel()

	type measurement struct {
		reads    int
		writes   []string
		segments []routeIdentityProofSegment
		stats    runtimeRouteIdentityStats
	}
	measure := func(t *testing.T, open bool, actions int) measurement {
		t.Helper()
		runtime, server, paneID := newRouteIdentityFixture()
		if open {
			runtime.openRouteIdentityCache("op-count")
			defer runtime.closeRouteIdentityCache()
		}
		server.calls = nil
		for range actions {
			if err := unsetLegacyTopic(runtime, server, paneID); err != nil {
				t.Fatalf("guarded identity write: %v", err)
			}
		}
		return measurement{
			reads: routeIdentityReads(server.calls), writes: routeIdentityWrites(server.calls),
			segments: routeIdentitySegments(server.calls), stats: runtime.routeIdentity.snapshot(),
		}
	}

	uncachedOne, uncachedThree := measure(t, false, 1), measure(t, false, 3)
	cachedOne, cachedThree := measure(t, true, 1), measure(t, true, 3)

	if uncachedThree.reads != 3*uncachedOne.reads {
		t.Fatalf("uncached baseline identity reads = %d for 3 actions, want 3x%d", uncachedThree.reads, uncachedOne.reads)
	}
	if len(cachedThree.writes) != 3 || !slices.Equal(cachedThree.writes, uncachedThree.writes) {
		t.Fatalf("identity cache changed writes:\ncached=%q\nuncached=%q", cachedThree.writes, uncachedThree.writes)
	}
	// Segment 0 is the first proof of the transaction and segment i follows
	// guarded write i. Every one must read the socket path and generation from
	// tmux: the first proof really runs, and no guarded write relies on a proof
	// older than the previous guarded write.
	for i, segment := range cachedThree.segments {
		if segment.socketPath == 0 || segment.pid == 0 {
			t.Fatalf("cached identity segment %d = %#v, want a real socket_path and pid proof (all=%#v)", i, segment, cachedThree.segments)
		}
	}
	uncachedPerAction := (uncachedThree.reads - uncachedOne.reads) / 2
	cachedPerAction := (cachedThree.reads - cachedOne.reads) / 2
	t.Logf("identity reads per guarded identity write: uncached=%d cached=%d (first action uncached=%d cached=%d)",
		uncachedPerAction, cachedPerAction, uncachedOne.reads, cachedOne.reads)
	if cachedPerAction <= 0 || cachedPerAction >= uncachedPerAction || cachedOne.reads >= uncachedOne.reads {
		t.Fatalf("cached identity reads per action = %d (first %d), want 0 < reads < uncached %d (first %d)",
			cachedPerAction, cachedOne.reads, uncachedPerAction, uncachedOne.reads)
	}
	if cachedThree.stats.Reuses == 0 || cachedThree.stats.Proofs == 0 {
		t.Fatalf("cached transaction never proved or reused: %#v", cachedThree.stats)
	}
}

func TestMaterializerPlansRunOnlyThroughTheGuardedWriteSeam(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	seamFound := false
	guardedPlans := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, declaration := range parsed.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Recv == nil || function.Body == nil || len(function.Recv.List) != 1 {
				continue
			}
			receiver := function.Recv.List[0].Type
			if star, ok := receiver.(*ast.StarExpr); ok {
				receiver = star.X
			}
			if ident, ok := receiver.(*ast.Ident); !ok || ident.Name != "materializer" {
				continue
			}
			if function.Name.Name == "guardedWriteSteps" {
				seamFound = true
				continue
			}
			builds, plans := false, 0
			ast.Inspect(function.Body, func(node ast.Node) bool {
				switch value := node.(type) {
				case *ast.CallExpr:
					executor, ok := value.Fun.(*ast.Ident)
					if !ok || executor.Name != "executeRuntimeMutationPlan" {
						return true
					}
					if len(value.Args) == 2 {
						if inner, ok := value.Args[1].(*ast.CallExpr); ok {
							if selector, ok := inner.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "guardedWriteSteps" {
								plans++
								return true
							}
						}
					}
					t.Errorf("%s: materializer.%s runs a plan whose steps bypass guardedWriteSteps", name, function.Name.Name)
				case *ast.Ident:
					if value.Name == "runtimeMutationStep" {
						builds = true
					}
				}
				return true
			})
			if builds && plans == 0 {
				t.Errorf("%s: materializer.%s builds runtime mutation steps but never runs them through guardedWriteSteps", name, function.Name.Name)
			}
			guardedPlans += plans
		}
	}
	if !seamFound || guardedPlans == 0 {
		t.Fatalf("guarded-write seam found=%v guarded plans=%d", seamFound, guardedPlans)
	}
}

func TestCachedRouteGuardRefusalsKeepTheirErrorFamily(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		arrange func(*fakeTmux)
		want    string
	}{
		{name: "socket path drift", arrange: func(s *fakeTmux) { s.socketPath = "/tmp/fake-tmux/foreign" }, want: "socket drifted"},
		{name: "server generation drift", arrange: func(s *fakeTmux) { s.serverPID = "5252" }, want: "planned runtime server generation drifted"},
		{name: "app ownership cleared", arrange: func(s *fakeTmux) { s.appMarker = "" }, want: "planned runtime socket is not app-owned"},
		{name: "logical marker drift", arrange: func(s *fakeTmux) { s.socketName = "foreign" }, want: "planned runtime socket logical route marker drifted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			refuse := func(open bool) (string, []string, runtimeRouteIdentityStats) {
				runtime, server, paneID := newRouteIdentityFixture()
				if open {
					runtime.openRouteIdentityCache("op-refusal")
					defer runtime.closeRouteIdentityCache()
				}
				test.arrange(server)
				server.calls = nil
				err := unsetLegacyTopic(runtime, server, paneID)
				if err == nil {
					t.Fatalf("%s was not refused (cache open=%v)", test.name, open)
				}
				return err.Error(), routeIdentityWrites(server.calls), runtime.routeIdentity.snapshot()
			}
			uncached, uncachedWrites, _ := refuse(false)
			cached, cachedWrites, stats := refuse(true)
			if cached != uncached {
				t.Fatalf("refusal wording changed with the identity cache:\ncached=%q\nuncached=%q", cached, uncached)
			}
			if !strings.Contains(cached, test.want) || !strings.Contains(cached, "runtime mutation plan:") {
				t.Fatalf("refusal = %q, want runtime mutation plan family with %q", cached, test.want)
			}
			if len(cachedWrites) != 0 || len(uncachedWrites) != 0 {
				t.Fatalf("refusal reached a write: cached=%q uncached=%q", cachedWrites, uncachedWrites)
			}
			if stats.Proofs != 0 || stats.Reuses != 0 {
				t.Fatalf("refused identity was cached or reused: %#v", stats)
			}
		})
	}
}

func TestStaleRouteIdentityCacheIsCaughtByTheNextFullProof(t *testing.T) {
	t.Parallel()

	runtime, server, paneID := newRouteIdentityFixture()
	runtime.openRouteIdentityCache("op-stale")
	defer runtime.closeRouteIdentityCache()
	if err := unsetLegacyTopic(runtime, server, paneID); err != nil {
		t.Fatalf("warm transaction proof: %v", err)
	}
	// The server restarts on the same socket with the same markers after the
	// write's post-effect proof. That proof is now stale.
	server.serverPID = "5252"
	action := materializeMutationAction(mutationWriteLayout, runtime.boundMutationTarget("pane", paneID, routeIdentityPaneUID),
		"exact layout", "Pane size", "-t", paneID, "-x", "40")
	// A guard with no write since that proof reuses it without reading tmux.
	start := len(server.calls)
	if err := runtime.targetRouteGuard(action)(context.Background()); err != nil {
		t.Fatalf("guard after the post-write proof refused without reading tmux: %v", err)
	}
	if reads := routeIdentityReads(server.calls[start:]); reads != 0 {
		t.Fatalf("guard after the post-write proof read identity %d times; the fixture no longer models reuse", reads)
	}

	// Today's uncached behavior for the same drift is the reference wording.
	reference, referenceServer, referencePane := newRouteIdentityFixture()
	referenceServer.serverPID = "5252"
	referenceAction := materializeMutationAction(mutationWriteLayout, reference.boundMutationTarget("pane", referencePane, routeIdentityPaneUID),
		"exact layout", "Pane size", "-t", referencePane, "-x", "40")
	wantGuard := reference.targetRouteGuard(referenceAction)(context.Background())
	wantWrite := unsetLegacyTopic(reference, referenceServer, referencePane)
	if wantGuard == nil || wantWrite == nil {
		t.Fatalf("reference drift was not refused: guard=%v write=%v", wantGuard, wantWrite)
	}

	// A full proof detects the stale cache with the uncached wording.
	gotGuard := runtime.revalidatedTargetRouteGuard(action)(context.Background())
	if gotGuard == nil || gotGuard.Error() != wantGuard.Error() {
		t.Fatalf("full proof over stale cache = %v, want %v", gotGuard, wantGuard)
	}
	staleRoute := runtimeMutationRoute{
		target: runtime.target, expectedSocketPath: runtime.expectedSocketPath,
		socketName: runtime.logicalSocketName(runtime.target), authority: runtime.routeAuthority,
	}
	for _, scope := range []string{routeIdentityScopeResolvedRoute, routeIdentityScopeExactRoute} {
		key, ok := runtimeRouteIdentityKeyForRoute(scope, staleRoute)
		if !ok || runtime.routeIdentity.reuse(key) {
			t.Fatalf("stale %s proof survived detection (cacheable=%v)", scope, ok)
		}
	}
	// Once detected, the next guarded write refuses exactly as it does without
	// a cache, before writing.
	start = len(server.calls)
	gotWrite := unsetLegacyTopic(runtime, server, paneID)
	if gotWrite == nil || gotWrite.Error() != wantWrite.Error() {
		t.Fatalf("guarded write after stale detection = %v, want %v", gotWrite, wantWrite)
	}
	if writes := routeIdentityWrites(server.calls[start:]); len(writes) != 0 {
		t.Fatalf("write after stale detection reached tmux: %q", writes)
	}
}

func TestSplitLayoutRevalidationNeverReusesTransactionIdentity(t *testing.T) {
	t.Parallel()

	runtime, server, paneID := newRouteIdentityFixture()
	runtime.openRouteIdentityCache("op-layout")
	defer runtime.closeRouteIdentityCache()
	if err := unsetLegacyTopic(runtime, server, paneID); err != nil {
		t.Fatalf("warm transaction proof: %v", err)
	}
	action := materializeMutationAction(mutationWriteLayout, runtime.boundMutationTarget("pane", paneID, routeIdentityPaneUID),
		"exact layout", "Pane size", "-t", paneID, "-x", "40")
	start := len(server.calls)
	if err := runtime.revalidatedTargetRouteGuard(action)(context.Background()); err != nil {
		t.Fatalf("revalidated guard: %v", err)
	}
	if reads := routeIdentityReads(server.calls[start:]); reads != 4 {
		t.Fatalf("pre-write revalidation identity reads = %d, want the full four-read proof", reads)
	}
}

func TestCreateTransactionScopesRouteIdentityReuseToOneOperation(t *testing.T) {
	t.Parallel()

	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, _ := newTestResourceCreateCommand(t, store, tmux)
	if _, _, err := runRoute(t, create, "pane", "--project", "beta", "--window", "main", "-o", "pane-id"); err != nil {
		t.Fatalf("first create Pane: %v", err)
	}
	// Later transactions start with a bound route. None may reuse a proof from
	// an earlier transaction: each proves identity before its first write.
	for transaction := 2; transaction <= 3; transaction++ {
		start := len(tmux.calls)
		if _, _, err := runRoute(t, create, "pane", "--project", "beta", "--window", "main", "-o", "pane-id"); err != nil {
			t.Fatalf("create Pane transaction %d: %v", transaction, err)
		}
		if create.runtime.routeIdentity != nil {
			t.Fatalf("transaction %d left its identity scope open", transaction)
		}
		var pid, app, logical bool
		wrote := false
		for _, call := range tmux.calls[start:] {
			argv := tmuxCommandArgv(call)
			if isRouteIdentityWrite(argv) {
				wrote = true
				if !pid || !app || !logical {
					t.Fatalf("transaction %d wrote %q before proving identity (pid=%v app=%v logical=%v)", transaction, call, pid, app, logical)
				}
				break
			}
			pid = pid || (argv[0] == "display-message" && flagValue(argv, "-F") == "#{pid}")
			app = app || (argv[0] == "show-options" && slices.Contains(argv, tmuxopts.AppGlobal))
			logical = logical || (argv[0] == "show-options" && slices.Contains(argv, runtimeMutationSocketNameOption))
		}
		if !wrote {
			t.Fatalf("transaction %d issued no runtime write", transaction)
		}
	}
}

// createDriftScenario is one canonical create transaction on the fake server.
type createDriftScenario struct {
	name  string
	build func(t *testing.T) (*createCommand, *fakeResourceStore, *fakeTmux)
	args  []string
}

func createDriftScenarios() []createDriftScenario {
	return []createDriftScenario{
		{
			name: "create pane",
			build: func(t *testing.T) (*createCommand, *fakeResourceStore, *fakeTmux) {
				store := newFakeResourceStore(t)
				tmux := newFakeTmux()
				create, _ := newTestResourceCreateCommand(t, store, tmux)
				return create, store, tmux
			},
			args: []string{"pane", "--project", "beta", "--window", "main", "-o", "pane-id"},
		},
		{
			name: "create agent",
			build: func(t *testing.T) (*createCommand, *fakeResourceStore, *fakeTmux) {
				store := newFakeResourceStore(t)
				tmux := newFakeTmux()
				seedOwnedSession(seedLiveAgentPane(t, tmux, "alpha", "win-alpha-main", "pan-alpha-zsh", "pan-alpha-codex"), "prj-alpha", "/srv/alpha")
				create, _ := newTestAgentCreateCommand(t, store, tmux)
				return create, store, tmux
			},
			args: []string{"agent", "--provider", "codex", "--interactive-only", "--project", "alpha", "--window", "main", "-o", "uid"},
		},
	}
}

// createDriftOutcome is everything a create transaction leaves observable.
type createDriftOutcome struct {
	stdout, err   string
	writes        []string
	committed     bool
	panes         []string
	guardedWrites int
	identityReads int
	// guardedWrite is the last tmux write argv when guarded write k finished.
	guardedWrite map[int]string
}

func (o createDriftOutcome) sameResult(other createDriftOutcome) bool {
	return o.stdout == other.stdout && o.err == other.err && o.committed == other.committed &&
		slices.Equal(o.writes, other.writes) && slices.Equal(o.panes, other.panes)
}

func fakeTmuxPaneSet(tmux *fakeTmux) []string {
	var panes []string
	for _, session := range tmux.sessions {
		for _, window := range session.windows {
			for _, pane := range window.panes {
				panes = append(panes, session.id+"/"+window.id+"/"+pane.id+"="+pane.opts[tmuxopts.PaneUID])
			}
		}
	}
	slices.Sort(panes)
	return panes
}

// runCreateUnderDrift runs one create transaction and, when driftAfter > 0,
// restarts the server generation (objects surviving) immediately after guarded
// write driftAfter.
func runCreateUnderDrift(t *testing.T, scenario createDriftScenario, cacheOff bool, driftAfter int) createDriftOutcome {
	t.Helper()
	create, store, tmux := scenario.build(t)
	create.runtime.routeIdentityDisabled = cacheOff
	outcome := createDriftOutcome{guardedWrite: map[int]string{}}
	start := len(tmux.calls)
	create.runtime.afterGuardedWrite = func() {
		outcome.guardedWrites++
		if writes := routeIdentityWrites(tmux.calls[start:]); len(writes) > 0 {
			outcome.guardedWrite[outcome.guardedWrites] = writes[len(writes)-1]
		}
		if outcome.guardedWrites == driftAfter {
			tmux.serverPID = "5252"
		}
	}
	committed := store.writes
	stdout, _, err := runRoute(t, create, scenario.args...)
	outcome.stdout = stdout
	if err != nil {
		outcome.err = err.Error()
	}
	outcome.writes = routeIdentityWrites(tmux.calls[start:])
	outcome.identityReads = routeIdentityReads(tmux.calls[start:])
	outcome.committed = store.writes != committed
	outcome.panes = fakeTmuxPaneSet(tmux)
	return outcome
}

func TestRouteIdentityCacheMatchesUncachedCreateUnderDriftAfterEachGuardedWrite(t *testing.T) {
	t.Parallel()

	for _, scenario := range createDriftScenarios() {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			cachedDry := runCreateUnderDrift(t, scenario, false, 0)
			uncachedDry := runCreateUnderDrift(t, scenario, true, 0)
			if cachedDry.err != "" || !cachedDry.committed {
				t.Fatalf("undrifted %s failed: %s", scenario.name, cachedDry.err)
			}
			if !cachedDry.sameResult(uncachedDry) || cachedDry.guardedWrites != uncachedDry.guardedWrites || cachedDry.guardedWrites == 0 {
				t.Fatalf("undrifted %s differs with the cache:\ncached=%#v\nuncached=%#v", scenario.name, cachedDry, uncachedDry)
			}
			t.Logf("%s: %d guarded writes, identity reads uncached=%d cached=%d",
				scenario.name, cachedDry.guardedWrites, uncachedDry.identityReads, cachedDry.identityReads)
			if cachedDry.identityReads >= uncachedDry.identityReads {
				t.Fatalf("%s identity reads cached=%d, want fewer than uncached=%d", scenario.name, cachedDry.identityReads, uncachedDry.identityReads)
			}

			refusedSplit := false
			for k := 1; k <= cachedDry.guardedWrites; k++ {
				cached := runCreateUnderDrift(t, scenario, false, k)
				uncached := runCreateUnderDrift(t, scenario, true, k)
				write := uncached.guardedWrite[k]
				t.Logf("drift after guarded write %d (%s): err=%q committed=%v", k, write, uncached.err, uncached.committed)
				if !cached.sameResult(uncached) {
					t.Errorf("drift after guarded write %d (%s) differs with the cache:\ncached:   err=%q committed=%v panes=%q\n          writes=%q\nuncached: err=%q committed=%v panes=%q\n          writes=%q",
						k, write, cached.err, cached.committed, cached.panes, cached.writes,
						uncached.err, uncached.committed, uncached.panes, uncached.writes)
				}
				if strings.Contains(write, " split-window ") && uncached.err != "" {
					refusedSplit = true
				}
			}
			if scenario.name == "create pane" && !refusedSplit {
				t.Fatal("no drift after the split-window write refused the create; the matrix no longer covers it")
			}
		})
	}
}

func TestRouteIdentityCommitReproofRefusesDriftBeforeCommitWithoutAWrite(t *testing.T) {
	t.Parallel()

	type result struct {
		outcome          createDriftOutcome
		liveCalls        int
		flipped          bool
		writesAfterFlip  []string
		guardedAfterFlip int
		warnings         string
	}
	run := func(t *testing.T, cacheOff bool, flipAt int) result {
		t.Helper()
		store := newFakeResourceStore(t)
		tmux := newFakeTmux()
		create, _ := newTestResourceCreateCommand(t, store, tmux)
		create.runtime.routeIdentityDisabled = cacheOff
		var warnings strings.Builder
		create.runtime.warn = &warnings
		var got result
		flipCall := 0
		// The final reconcile pass starts after every guarded write and its
		// post-effect proof, and it issues no guarded write before commit. Its
		// opening live-session read is therefore the no-write window.
		live := create.reconciler.liveSessions
		create.reconciler.liveSessions = func(ctx context.Context) (map[string]bool, error) {
			got.liveCalls++
			if got.liveCalls == flipAt {
				tmux.serverPID = "5252"
				got.flipped, flipCall = true, len(tmux.calls)
			}
			return live(ctx)
		}
		create.runtime.afterGuardedWrite = func() {
			if got.flipped {
				got.guardedAfterFlip++
			}
		}
		start, committed := len(tmux.calls), store.writes
		stdout, _, err := runRoute(t, create, "pane", "--project", "beta", "--window", "main", "-o", "pane-id")
		got.outcome = createDriftOutcome{stdout: stdout, committed: store.writes != committed, panes: fakeTmuxPaneSet(tmux)}
		if err != nil {
			got.outcome.err = err.Error()
		}
		got.outcome.writes = routeIdentityWrites(tmux.calls[start:])
		if got.flipped {
			got.writesAfterFlip = routeIdentityWrites(tmux.calls[flipCall:])
		}
		if create.runtime.routeIdentity != nil {
			t.Fatal("transaction left its identity scope open")
		}
		got.warnings = warnings.String()
		return got
	}

	dry := run(t, false, 0)
	if dry.outcome.err != "" || dry.liveCalls == 0 {
		t.Fatalf("undrifted create Pane: err=%q liveCalls=%d", dry.outcome.err, dry.liveCalls)
	}
	cached := run(t, false, dry.liveCalls)
	uncached := run(t, true, dry.liveCalls)
	t.Logf("cached error:   %q", cached.outcome.err)
	t.Logf("uncached error: %q (committed=%v)", uncached.outcome.err, uncached.outcome.committed)
	t.Logf("cached rollback warnings: %q", cached.warnings)

	for name, got := range map[string]result{"cached": cached, "uncached": uncached} {
		if !got.flipped || got.guardedAfterFlip != 0 || len(got.writesAfterFlip) != 0 {
			t.Fatalf("%s: drift window was not write-free: flipped=%v guarded=%d writes=%q", name, got.flipped, got.guardedAfterFlip, got.writesAfterFlip)
		}
	}
	const wantCommitRefusal = "runtime mutation plan: route identity refused commit after reused proofs: planned runtime server generation drifted"
	if cached.outcome.err != wantCommitRefusal {
		t.Fatalf("cached commit refusal = %q, want %q", cached.outcome.err, wantCommitRefusal)
	}
	if cached.outcome.committed || cached.outcome.stdout != "" {
		t.Fatalf("cached drift reached the Registry: committed=%v stdout=%q", cached.outcome.committed, cached.outcome.stdout)
	}
	if !strings.Contains(cached.warnings, "server generation drifted") {
		t.Fatalf("cached rollback did not run through the runtime ledger guard: warnings=%q", cached.warnings)
	}
	// Without reuse there is no stale proof to re-check, so today's uncached
	// path commits in this window.
	if uncached.outcome.err != "" || !uncached.outcome.committed {
		t.Fatalf("uncached no-write drift outcome changed: err=%q committed=%v", uncached.outcome.err, uncached.outcome.committed)
	}
}

// staleRestartRunner restarts the exact server on its socket right after the
// create's split write, while the transaction already holds reused proofs.
type staleRestartRunner struct {
	base    *fakeTmux
	restart func()
	fired   bool
}

func (r *staleRestartRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := r.base.Run(ctx, name, args...)
	if argv := tmuxCommandArgv(args); !r.fired && err == nil && len(argv) > 0 && argv[0] == "split-window" {
		r.fired = true
		r.restart()
	}
	return out, err
}

func TestStaleRouteIdentityDriftDuringCreateRollsBackThroughRuntimeLedger(t *testing.T) {
	t.Parallel()

	const splitResidual = `split tmux pane "%3": runtime mutation plan: expected effects were not fully observed; residual plan:`
	for _, test := range []struct {
		name    string
		restart func(*fakeTmux)
		// wantPanes is the uncached end state for the same drift: the ledger
		// rollback removes what it still owns and preserves what a drifted
		// generation forbids it to kill.
		wantPanes int
	}{
		{name: "restart loses runtime objects", restart: func(s *fakeTmux) { s.serverPID, s.sessions = "5252", nil }, wantPanes: 0},
		{name: "generation drifts with objects intact", restart: func(s *fakeTmux) { s.serverPID = "5252" }, wantPanes: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeResourceStore(t)
			tmux := newFakeTmux()
			create, _ := newTestResourceCreateCommand(t, store, tmux)
			if _, _, err := runRoute(t, create, "pane", "--project", "beta", "--window", "main", "-o", "pane-id"); err != nil {
				t.Fatalf("bind route: %v", err)
			}
			committed := store.writes
			runner := &staleRestartRunner{base: tmux, restart: func() { test.restart(tmux) }}
			create.runtime.runner = runner
			stdout, _, err := runRoute(t, create, "pane", "--project", "beta", "--window", "main", "-o", "pane-id")
			if !runner.fired {
				t.Fatal("fixture never reached the split write")
			}
			// The split is a guarded write, so its post-effect observation proves
			// the route again and fails with the same message as without a cache.
			if err == nil || !strings.Contains(err.Error(), splitResidual) {
				t.Fatalf("stale drift error = %v, want %q", err, splitResidual)
			}
			if stdout != "" || store.writes != committed {
				t.Fatalf("stale drift reached the Registry: stdout=%q writes %d->%d", stdout, committed, store.writes)
			}
			if got := tmux.paneCount(); got != test.wantPanes {
				t.Fatalf("panes after ledger rollback = %d, want uncached end state %d", got, test.wantPanes)
			}
			if create.runtime.routeIdentity != nil {
				t.Fatal("rollback ran with the identity scope still open")
			}
		})
	}
}
