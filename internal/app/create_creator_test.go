package app

import (
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// creatorTestPanePID is the process id the seeded creator Pane reports as
// `#{pane_pid}`, and the pid the fake parent chain contains.
const creatorTestPanePID = 7001

var creatorAnnotationKeys = []string{
	coremetadata.AnnotationCreatorAgent,
	coremetadata.AnnotationCreatorPane,
	coremetadata.AnnotationCreatorBasis,
}

// creatorAnswerRunner forwards every tmux call to the fake server and, only
// for the creator query, lets a test rewrite the answer the way a Pane on a
// different server would read.
type creatorAnswerRunner struct {
	tmux    *fakeTmux
	rewrite func(row []string)
}

func (r *creatorAnswerRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := r.tmux.Run(ctx, name, args...)
	if err != nil || r.rewrite == nil || !isCreatorQuery(args) {
		return out, err
	}
	rows := splitTmuxRows(string(out), 4)
	if len(rows) != 1 {
		return out, err
	}
	r.rewrite(rows[0])
	return []byte(strings.Join(rows[0], tmuxRowSep) + "\n"), nil
}

func isCreatorQuery(call []string) bool {
	return slices.ContainsFunc(call, func(arg string) bool {
		return strings.Contains(arg, "#{socket_path}") && strings.Contains(arg, "#{pane_pid}")
	})
}

func creatorQueryCount(tmux *fakeTmux) int {
	count := 0
	for _, call := range tmux.calls {
		if isCreatorQuery(call) {
			count++
		}
	}
	return count
}

// bindCreatorTestRoute is bindTestCreateRuntimeRoute over any runner, so the
// creator query can be answered through creatorAnswerRunner.
func bindCreatorTestRoute(command *createCommand, runner tmuxCommandRunner, lookupEnv func(string) string) *runtimeMutationRoute {
	var resolved runtimeMutationRoute
	bind := func(ctx context.Context, explicit bool) error {
		route, err := resolveInvocationRuntimeMutationRouteWithPolicy(ctx, runner, lookupEnv, command.routeAnchor, explicit)
		if err != nil {
			return err
		}
		resolved = route
		exact := explicitTmuxRunner{runner: runner, target: route.target}
		command.reconciler.mirror = intmetadata.NewMirror(exact)
		command.runtime.runner = exact
		command.runtime.mirror = intmetadata.NewMirror(exact)
		command.runtime.target = route.target
		command.runtime.expectedSocketPath = route.expectedSocketPath
		command.runtime.socketName = route.socketName
		command.runtime.routeAuthority = route.authority
		return nil
	}
	command.bindRuntime = func(ctx context.Context) error { return bind(ctx, false) }
	command.bindExplicitRuntime = func(ctx context.Context) error { return bind(ctx, true) }
	return &resolved
}

type creatorFixture struct {
	store        *fakeResourceStore
	tmux         *fakeTmux
	runner       *creatorAnswerRunner
	command      *createCommand
	route        *runtimeMutationRoute
	env          map[string]string
	creatorAgent string
	creatorPane  string
	creatorID    string
}

// newCreatorFixture seeds one live Claude Agent and its managed Pane with an
// ordinary explicit create (observation seam still unset), then wires the
// command as if it now ran inside that Pane: the ambient TMUX_PANE is the
// creator's `%N`, the Pane reports creatorTestPanePID, and the fake parent
// chain contains it. Every check passes unless a test breaks one.
func newCreatorFixture(t *testing.T) *creatorFixture {
	t.Helper()
	store, tmux := aliveAlphaRuntime(t)
	runner := &creatorAnswerRunner{tmux: tmux}
	command, _ := newTestAgentCreateCommand(t, store, tmux)
	env := map[string]string{"TMUX": tmux.socketPath + "," + tmux.serverPID + ",17"}
	lookup := func(key string) string { return env[key] }
	route := bindCreatorTestRoute(command, runner, lookup)

	stdout, stderr, err := runRoute(t, command,
		"agent", "--provider", "claude", "--project", "uid:prj-alpha", "--window", "uid:win-alpha-main",
		"--name", "creator", "-o", "pane-id")
	if err != nil || stderr != "" {
		t.Fatalf("seed creator Agent: stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	creator := agentNamed(t, store, "win-alpha-main", "creator")
	creatorID := strings.TrimSpace(stdout)
	_, _, live := tmux.pane(creatorID)
	if live == nil {
		t.Fatalf("seeded creator Pane %q is not live; tmux:\n%s", creatorID, tmux.state())
	}
	live.pid = strconv.Itoa(creatorTestPanePID)
	if pane, ok := store.registry.Pane(creator.Status.PaneRef); !ok || pane.Status.Activation.RuntimeID != creatorID {
		t.Fatalf("seeded creator Pane %q activation runtime = %+v, want %s", creator.Status.PaneRef, pane, creatorID)
	}
	if got := creatorKeysOf(creator.Metadata); len(got) != 0 {
		t.Fatalf("seed created outside any Pane recorded a creator: %v", got)
	}
	env["TMUX_PANE"] = creatorID
	command.lookupEnv = lookup
	command.processAncestors = func() ([]int, error) { return []int{90001, creatorTestPanePID, 4242}, nil }
	return &creatorFixture{
		store: store, tmux: tmux, runner: runner, command: command, route: route, env: env,
		creatorAgent: creator.Metadata.UID, creatorPane: creator.Status.PaneRef, creatorID: creatorID,
	}
}

func (fx *creatorFixture) agentUIDs() map[string]bool {
	uids := map[string]bool{}
	for _, agent := range fx.store.registry.Agents {
		uids[agent.Metadata.UID] = true
	}
	return uids
}

// newAgentsSince returns the Agents a create added and each one's managed Pane.
func (fx *creatorFixture) newAgentsSince(t *testing.T, before map[string]bool) ([]coremetadata.Agent, []coremetadata.Pane) {
	t.Helper()
	var agents []coremetadata.Agent
	var panes []coremetadata.Pane
	for _, agent := range fx.store.registry.Agents {
		if before[agent.Metadata.UID] {
			continue
		}
		pane, ok := fx.store.registry.Pane(agent.Status.PaneRef)
		if !ok {
			t.Fatalf("new Agent %s has no managed Pane %q", agent.Metadata.UID, agent.Status.PaneRef)
		}
		agents = append(agents, agent)
		panes = append(panes, *pane)
	}
	return agents, panes
}

func creatorKeysOf(meta coremetadata.ObjectMeta) map[string]string {
	out := map[string]string{}
	for _, key := range creatorAnnotationKeys {
		if value, ok := meta.Annotations[key]; ok {
			out[key] = value
		}
	}
	return out
}

func assertNoCreatorKeysAnywhere(t *testing.T, store *fakeResourceStore) {
	t.Helper()
	for _, agent := range store.registry.Agents {
		if got := creatorKeysOf(agent.Metadata); len(got) != 0 {
			t.Fatalf("Agent %s carries creator keys %v", agent.Metadata.UID, got)
		}
	}
	for _, pane := range store.registry.Panes {
		if got := creatorKeysOf(pane.Metadata); len(got) != 0 {
			t.Fatalf("Pane %s carries creator keys %v", pane.Metadata.UID, got)
		}
	}
}

func TestExplicitCreateFromAnAgentPaneRecordsTheCreatorOnEveryNewAgentAndItsPane(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		argv      []string
		wantCount int
	}{
		{name: "create agent", wantCount: 1, argv: []string{
			"agent", "--provider", "claude", "--project", "uid:prj-alpha", "--window", "uid:win-alpha-main", "-o", "pane-id"}},
		{name: "provider shortcut", wantCount: 1, argv: []string{
			"claude", "--project", "uid:prj-alpha", "--window", "uid:win-alpha-main", "-o", "pane-id"}},
		{name: "create-window", wantCount: 1, argv: []string{
			"agent", "--provider", "codex", "--interactive-only", "--project", "uid:prj-alpha",
			"--window", "fresh", "--create-window", "-o", "pane-id"}},
		{name: "fan-out over every Window", wantCount: 2, argv: []string{
			"agent", "--provider", "codex", "--interactive-only", "--project", "uid:prj-alpha", "--all-windows", "-o", "pane-id"}},
		{name: "create window --provider", wantCount: 1, argv: []string{
			"window", "--project", "uid:prj-alpha", "--provider", "claude"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fx := newCreatorFixture(t)
			before := fx.agentUIDs()
			stdout, stderr, err := runRoute(t, fx.command, test.argv...)
			if err != nil || stderr != "" || stdout == "" {
				t.Fatalf("create = stdout=%q stderr=%q err=%v", stdout, stderr, err)
			}
			agents, panes := fx.newAgentsSince(t, before)
			if len(agents) != test.wantCount {
				t.Fatalf("new Agents = %d, want %d", len(agents), test.wantCount)
			}
			want := coremetadata.CreatorAnnotations(fx.creatorAgent, fx.creatorPane)
			for i := range agents {
				if got := creatorKeysOf(agents[i].Metadata); !maps.Equal(got, want) {
					t.Fatalf("new Agent %s creator keys = %v, want %v", agents[i].Metadata.UID, got, want)
				}
				if got := creatorKeysOf(panes[i].Metadata); !maps.Equal(got, want) {
					t.Fatalf("new Agent Pane %s creator keys = %v, want %v", panes[i].Metadata.UID, got, want)
				}
			}
			if got := creatorQueryCount(fx.tmux); got != 1 {
				t.Fatalf("creator tmux queries = %d, want exactly one per create invocation", got)
			}
			if fx.route.authority == nil || fx.route.authority.PaneID != "" {
				t.Fatalf("explicit route authority = %#v, want no ambient Pane containment", fx.route.authority)
			}
		})
	}
}

func TestCreatorIsNotRecordedWhenAnyCheckFailsAndTheCreateStillSucceeds(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		// arrange breaks exactly one check.
		arrange    func(t *testing.T, fx *creatorFixture)
		wantStderr string
		// wantSilentSkip is the internal reason of a skip that prints nothing:
		// the ambient Pane never looked like a live Agent Pane.
		wantSilentSkip string
		// wantQuery is whether the single tmux creator query was reached.
		wantQuery bool
	}{
		{
			name: "ambient Pane on a different socket",
			arrange: func(_ *testing.T, fx *creatorFixture) {
				fx.runner.rewrite = func(row []string) { row[0] = "/tmp/fake-tmux/other" }
			},
			wantStderr: "creator not recorded: anchor-server-mismatch\n",
			wantQuery:  true,
		},
		{
			name:       "ambient Pane on a different server pid",
			arrange:    func(_ *testing.T, fx *creatorFixture) { fx.runner.rewrite = func(row []string) { row[1] = "999999" } },
			wantStderr: "creator not recorded: anchor-server-mismatch\n",
			wantQuery:  true,
		},
		{
			name: "ambient Pane not in the Registry",
			arrange: func(_ *testing.T, fx *creatorFixture) {
				fx.env["TMUX_PANE"] = fx.tmux.addSession("unrelated").windows[0].panes[0].id
			},
			wantSilentSkip: creatorSkipPaneUnregistered,
		},
		{
			name: "two live Panes carry the ambient runtime id",
			arrange: func(t *testing.T, fx *creatorFixture) {
				shell, ok := fx.store.registry.Pane("pan-alpha-zsh")
				if !ok {
					t.Fatal("fixture shell Pane is missing")
				}
				shell.Status.Activation = coremetadata.PaneActivation{Generation: "gen-ambiguous", RuntimeID: fx.creatorID}
			},
			wantStderr: "creator not recorded: anchor-pane-ambiguous\n",
		},
		{
			name: "Window-owned shell Pane",
			arrange: func(t *testing.T, fx *creatorFixture) {
				stdout, stderr, err := runRoute(t, fx.command,
					"pane", "--project", "uid:prj-alpha", "--window", "uid:win-alpha-main", "-o", "pane-id")
				if err != nil || stderr != "" {
					t.Fatalf("seed shell Pane: stdout=%q stderr=%q err=%v", stdout, stderr, err)
				}
				fx.env["TMUX_PANE"] = strings.TrimSpace(stdout)
			},
			wantSilentSkip: creatorSkipCallerNotAgent,
		},
		{
			name: "pane_pid is not in the parent chain (Codex shape)",
			arrange: func(_ *testing.T, fx *creatorFixture) {
				fx.command.processAncestors = func() ([]int, error) { return []int{90001, 4242}, nil }
			},
			wantStderr: "creator not recorded: not-pane-descendant\n",
			wantQuery:  true,
		},
		{
			name: "parent chain cannot be observed",
			arrange: func(_ *testing.T, fx *creatorFixture) {
				fx.command.processAncestors = func() ([]int, error) { return nil, errors.New("no procfs") }
			},
			wantStderr: "creator not recorded: process-chain-unobservable\n",
			wantQuery:  true,
		},
		{
			name: "outside tmux",
			arrange: func(_ *testing.T, fx *creatorFixture) {
				delete(fx.env, "TMUX_PANE")
				delete(fx.env, "TMUX")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fx := newCreatorFixture(t)
			test.arrange(t, fx)
			before := fx.agentUIDs()
			queriesBefore := creatorQueryCount(fx.tmux)
			stdout, stderr, err := runRoute(t, fx.command,
				"agent", "--provider", "claude", "--project", "uid:prj-alpha", "--window", "uid:win-alpha-main", "-o", "pane-id")
			if err != nil || exactTmuxHandle(strings.TrimSpace(stdout), "%") == "" {
				t.Fatalf("create = stdout=%q stderr=%q err=%v, want an ordinary success", stdout, stderr, err)
			}
			if stderr != test.wantStderr {
				t.Fatalf("stderr = %q, want %q", stderr, test.wantStderr)
			}
			if test.wantStderr == "" {
				// A silent skip issues no tmux call, so re-observing is free
				// and exposes the reason the create kept to itself.
				working := fx.store.registry.Clone()
				if got := fx.command.observeCreator(context.Background(), &working); got.recorded() || got.skip != test.wantSilentSkip {
					t.Fatalf("silent creator observation = %+v, want skip %q", got, test.wantSilentSkip)
				}
			}
			if agents, _ := fx.newAgentsSince(t, before); len(agents) != 1 {
				t.Fatalf("new Agents = %d, want 1", len(agents))
			}
			assertNoCreatorKeysAnywhere(t, fx.store)
			// The Registry pre-check runs first, so an ambient Pane that is not a
			// live Agent Pane costs zero extra tmux calls.
			queries := creatorQueryCount(fx.tmux) - queriesBefore
			if want := map[bool]int{false: 0, true: 1}[test.wantQuery]; queries != want {
				t.Fatalf("creator tmux queries = %d, want %d", queries, want)
			}
		})
	}
}

// TestCreatorDiagnosticPrintsOnlyWhenTheAmbientPaneWasALiveAgentPane closes
// the printed/silent split over the whole token vocabulary.
func TestCreatorDiagnosticPrintsOnlyWhenTheAmbientPaneWasALiveAgentPane(t *testing.T) {
	t.Parallel()
	for skip, printed := range map[string]bool{
		"":                             false,
		creatorSkipAnchorInvalid:       false,
		creatorSkipPaneUnregistered:    false,
		creatorSkipCallerNotAgent:      false,
		creatorSkipPaneAmbiguous:       true,
		creatorSkipPaneRefMismatch:     true,
		creatorSkipServerUnproven:      true,
		creatorSkipAnchorQueryFailed:   true,
		creatorSkipServerMismatch:      true,
		creatorSkipAnchorPaneMismatch:  true,
		creatorSkipProcessUnobservable: true,
		creatorSkipNotPaneDescendant:   true,
	} {
		var stderr bytes.Buffer
		creatorProvenance{skip: skip}.reportSkip(&stderr)
		want := ""
		if printed {
			want = "creator not recorded: " + skip + "\n"
		}
		if stderr.String() != want {
			t.Fatalf("skip %q printed %q, want %q", skip, stderr.String(), want)
		}
	}
	var stderr bytes.Buffer
	creatorProvenance{agentUID: "agent-x", paneUID: "pane-x"}.reportSkip(&stderr)
	if stderr.Len() != 0 {
		t.Fatalf("a recorded creator printed %q", stderr.String())
	}
}

// TestRegistryCreatorPaneRequiresTheAgentPaneRoundTrip covers the in-memory
// branches a valid committed Registry cannot reach through a whole create: an
// Agent whose status.paneRef names a different Pane, an owner Agent that is
// gone, and a Pane that reconcile marked MissingRuntime.
func TestRegistryCreatorPaneRequiresTheAgentPaneRoundTrip(t *testing.T) {
	t.Parallel()
	fx := newCreatorFixture(t)
	seeded := fx.store.registry.Clone()
	if agent, pane, skip := registryCreatorPane(&seeded, fx.creatorID); skip != "" ||
		agent != fx.creatorAgent || pane != fx.creatorPane {
		t.Fatalf("seeded creator = %q/%q skip=%q, want %q/%q", agent, pane, skip, fx.creatorAgent, fx.creatorPane)
	}
	for _, test := range []struct {
		name   string
		mutate func(*coremetadata.Registry)
		want   string
	}{
		{name: "paneRef names another Pane", want: creatorSkipPaneRefMismatch, mutate: func(r *coremetadata.Registry) {
			agent, _ := r.Agent(fx.creatorAgent)
			agent.Status.PaneRef = "pane-elsewhere"
		}},
		{name: "owner Agent is gone", want: creatorSkipPaneRefMismatch, mutate: func(r *coremetadata.Registry) {
			r.Agents = slices.DeleteFunc(r.Agents, func(a coremetadata.Agent) bool { return a.Metadata.UID == fx.creatorAgent })
		}},
		{name: "Pane is MissingRuntime", want: creatorSkipPaneUnregistered, mutate: func(r *coremetadata.Registry) {
			pane, _ := r.Pane(fx.creatorPane)
			pane.Status.Conditions = append(pane.Status.Conditions, coremetadata.Condition{
				Type: coremetadata.ConditionMissingRuntime, Status: coremetadata.ConditionTrue})
		}},
	} {
		working := fx.store.registry.Clone()
		test.mutate(&working)
		if agent, pane, skip := registryCreatorPane(&working, fx.creatorID); skip != test.want || agent != "" || pane != "" {
			t.Fatalf("%s: creator = %q/%q skip=%q, want skip %q", test.name, agent, pane, skip, test.want)
		}
	}
}

// TestIntentCreateFromAnAgentPaneNeverRecordsTheCreator is owner ruling 3:
// the UI intents (picker, pane menu, `ai split`, launch choice) reach
// createCanonicalIntentAgent, which has no creator observation at all -- even
// when every check would pass, as the explicit create after it proves.
func TestIntentCreateFromAnAgentPaneNeverRecordsTheCreator(t *testing.T) {
	t.Parallel()
	fx := newCreatorFixture(t)
	withPopupOrigin(fx.command, fx.tmux, func(key string) string { return fx.env[key] })
	before := fx.agentUIDs()
	var stdout, stderr bytes.Buffer
	if _, err := fx.command.createFromIntent(agentPaneIntent{
		producer: canonicalProducerProviderPicker, provider: aiModeClaude, placement: "right", anchorPaneID: fx.creatorID,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("intent create: %v (stderr=%q)", err, stderr.String())
	}
	if agents, _ := fx.newAgentsSince(t, before); len(agents) != 1 {
		t.Fatalf("intent created %d Agents, want 1", len(agents))
	}
	assertNoCreatorKeysAnywhere(t, fx.store)
	if got := creatorQueryCount(fx.tmux); got != 0 || strings.Contains(stderr.String(), "creator not recorded") {
		t.Fatalf("intent observed the creator: queries=%d stderr=%q", got, stderr.String())
	}

	// Control: the same command, Pane, and seams record on the explicit route.
	before = fx.agentUIDs()
	if out, errOut, err := runRoute(t, fx.command,
		"agent", "--provider", "claude", "--project", "uid:prj-alpha", "--window", "uid:win-alpha-main"); err != nil || errOut != "" {
		t.Fatalf("explicit control create: stdout=%q stderr=%q err=%v", out, errOut, err)
	}
	agents, _ := fx.newAgentsSince(t, before)
	if len(agents) != 1 || !maps.Equal(creatorKeysOf(agents[0].Metadata), coremetadata.CreatorAnnotations(fx.creatorAgent, fx.creatorPane)) {
		t.Fatalf("explicit control create did not record the creator: %+v", agents)
	}
}

// TestWebCreatesNeverObserveOrRecordTheCreator is owner ruling 3's web half.
// The web API runs the create handler in-process with the web server's
// environment and parent chain, so its app withdraws the observation seam.
func TestWebCreatesNeverObserveOrRecordTheCreator(t *testing.T) {
	t.Parallel()
	if newCreateCommand().processAncestors == nil {
		t.Fatal("the CLI create command has no creator observation seam")
	}
	if app := webCLIApp(); app.create == nil || app.create.processAncestors != nil {
		t.Fatal("the web app's create command still observes the creator")
	}

	fx := newCreatorFixture(t)
	fx.command.withoutCreatorProvenance()
	before := fx.agentUIDs()
	stdout, stderr, err := runRoute(t, fx.command,
		"agent", "--provider", "claude", "--project", "uid:prj-alpha", "--window", "uid:win-alpha-main", "-o", "json")
	if err != nil || stderr != "" || stdout == "" {
		t.Fatalf("web-shaped create = stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if agents, _ := fx.newAgentsSince(t, before); len(agents) != 1 {
		t.Fatalf("new Agents = %d, want 1", len(agents))
	}
	assertNoCreatorKeysAnywhere(t, fx.store)
	if got := creatorQueryCount(fx.tmux); got != 0 {
		t.Fatalf("web-shaped create issued %d creator queries, want 0", got)
	}
}

func TestProcessAncestryWalksTheRealProcChainToTheParent(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("the /proc parent chain is observed on linux")
	}
	chain, err := processAncestry()
	if err != nil {
		t.Fatalf("processAncestry: %v (chain %v)", err, chain)
	}
	if len(chain) < 2 || chain[0] != os.Getpid() || !slices.Contains(chain, os.Getppid()) {
		t.Fatalf("chain = %v, want it to start at pid %d and contain parent %d", chain, os.Getpid(), os.Getppid())
	}
	if len(chain) > creatorProcessChainMaxSteps {
		t.Fatalf("chain length %d exceeds the %d-step bound", len(chain), creatorProcessChainMaxSteps)
	}
}

// annotatedSnapshot is fakeResourceStore.snapshot plus every Agent and Pane
// annotation. With strip, exactly the three creator keys are left out, so a
// comparison's only admitted difference is closed by key name.
func annotatedSnapshot(store *fakeResourceStore, strip bool) string {
	var b strings.Builder
	b.WriteString(store.snapshot())
	write := func(kind string, meta coremetadata.ObjectMeta) {
		for _, key := range slices.Sorted(maps.Keys(meta.Annotations)) {
			if strip && slices.Contains(creatorAnnotationKeys, key) {
				continue
			}
			b.WriteString("annotation " + kind + " " + meta.UID + " " + key + "=" + meta.Annotations[key] + "\n")
		}
	}
	for _, agent := range store.registry.Agents {
		write("agent", agent.Metadata)
	}
	for _, pane := range store.registry.Panes {
		write("pane", pane.Metadata)
	}
	return b.String()
}

// withoutOneCreatorQuery removes exactly the one creator query for paneID
// from a call ledger, and fails if it is not there exactly once.
func withoutOneCreatorQuery(t *testing.T, calls [][]string, paneID string) [][]string {
	t.Helper()
	var kept [][]string
	removed := 0
	for _, call := range calls {
		if isCreatorQuery(call) && slices.Contains(call, paneID) {
			removed++
			continue
		}
		kept = append(kept, call)
	}
	if removed != 1 {
		t.Fatalf("creator queries for %s = %d, want exactly 1", paneID, removed)
	}
	return kept
}
