package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
)

// TestAgentResumeIntoStoppedProjectNeedsNoAnchorPaneThroughRealTmux is the
// stopped-Project half of TestAgentResumeNeedsNoAnchorPaneThroughRealTmux,
// taken against a real tmux server.
//
// The Project has no tmux session at all: the isolated server is alive only
// because an unrelated app-owned `bootstrap` session keeps it up. The caller's
// environment is still the stale one -- a TMUX receipt for that live server,
// and a TMUX_PANE plus a private producer anchor that both name a Pane the
// server never issued. `agent resume` must still complete and start the
// Project's session: ensureProjectRuntime goes through ensureRuntimeRoute,
// which reuses the exact-object route the resume transaction already bound
// with the explicit binder. It never runs the anchored binder and never enters
// the closed-Project topology materializer, both of which would refuse this
// environment because the anchor Pane is gone.
//
// The topology materializer has no seam on the resume path, so it cannot be
// counted. Instead the process environment carries the same dead anchor, and
// a control shows that the materializer's own route resolver, which reads that
// environment, refuses naming the absent anchor Pane. Had resume entered the
// materializer, it would have failed with that reason; it succeeds instead.
// TMUX_TMPDIR points at the isolated root, so any stray default-socket call
// lands on the isolated server's directory and never on the live one.
func TestAgentResumeIntoStoppedProjectNeedsNoAnchorPaneThroughRealTmux(t *testing.T) {
	if os.Getenv(resumeNoAnchorRealTmuxEnv) == "1" {
		t.Setenv(resumedPaneNameRealTmuxEnv, "1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	server := startRealTmuxNameHandoffServer(t, ctx)
	projectRoot := filepath.Join(server.root, "project")
	if err := os.Mkdir(projectRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	// An unrelated session keeps the server up; it is not the Project, so it
	// carries no Project, Window, or Pane identity.
	created, err := server.tmux("new-session", "-d", "-s", "bootstrap", "-P", "-F", "#{pid}\t#{socket_path}", "tail", "-f", "/dev/null")
	if err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, created)
	}
	t.Cleanup(server.killServer)
	fields := strings.Split(created, "\t")
	if len(fields) != 2 || fields[1] != server.socket {
		t.Fatalf("isolated tmux receipt = %q, want pid on %s", created, server.socket)
	}
	serverPID := fields[0]
	server.seed(t)

	const sessionName = "name-handoff"
	store := realTmuxNameHandoffRegistry(t, projectRoot, sessionName, false)
	test := realTmuxNameHandoffCase{agentName: "revived", paneName: "revived-pane"}
	addRealTmuxNameHandoffAgent(t, store, projectRoot, &test, false)

	// A handle the server has never issued: the Pane the operator was standing
	// in is gone.
	const deadAnchor = "%99999"
	inherited := map[string]string{
		"TMUX":                       server.socket + "," + serverPID + ",0",
		"TMUX_PANE":                  deadAnchor,
		runtimeMutationAnchorPaneEnv: deadAnchor,
	}
	for key, value := range inherited {
		t.Setenv(key, value)
	}
	t.Setenv("TMUX_TMPDIR", server.root)
	lookupEnv := func(key string) string { return inherited[key] }
	runner := shellTmuxExecRunner{env: func() []string { return server.environment }}

	wantAnchorRefusal := func(what string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s bound a route on a Pane the real server does not have", what)
		}
		if !strings.Contains(err.Error(), "anchor pane "+deadAnchor+" no longer exists") {
			t.Fatalf("%s refusal = %q, want it to name the absent anchor Pane", what, err)
		}
		if strings.Contains(err.Error(), "containment drifted") {
			t.Fatalf("%s refusal still calls an absent Pane drift: %q", what, err)
		}
	}
	// Control: the anchored policy refuses this environment and names the cause.
	_, err = resolveInvocationRuntimeMutationRoute(ctx, runner, lookupEnv)
	wantAnchorRefusal("the anchored policy", err)
	// Control: the closed-Project topology materializer resolves its route
	// from the process environment, which now holds the same dead anchor. Had
	// resume entered it, it would have refused here.
	topology := newRegistryProjectTopologyMaterializer()
	_, err = topology.resolveRoute(ctx, "")
	wantAnchorRefusal("the topology materializer route", err)
	_, err = topology.resolveRoute(ctx, deadAnchor)
	wantAnchorRefusal("the topology materializer route with the dead anchor", err)

	target := tmuxTransport{Kind: tmuxSocketName, Value: server.logical, Source: tmuxSocketNameSource}
	routed := explicitTmuxRunner{runner: runner, target: target}
	client := defaultTmuxClientWithRunner(routed)
	create := &createCommand{
		store:      store.store(),
		reconciler: newRegistryReconciler(routed, client),
		runtime: &materializer{
			runner: routed, mirror: intmetadata.NewMirror(routed), sessions: client, target: target,
			warn:       testWarnWriter{t},
			executable: func() (string, error) { return "", errors.New("no supervisor in the resume anchor test") },
			lookupEnv:  lookupEnv,
		},
		shell:          "/bin/sh",
		sessionNameFor: filepath.Base,
		newOperationID: newCreateOperationID,
		now:            time.Now,
		newGeneration:  coremetadata.NewGeneration,
	}
	policies := map[bool]int{}
	bind := func(ctx context.Context, explicit bool) error {
		policies[explicit]++
		route, err := resolveInvocationRuntimeMutationRouteWithPolicy(ctx, runner, lookupEnv, create.routeAnchor, explicit)
		if err != nil {
			return err
		}
		exact := explicitTmuxRunner{runner: runner, target: route.target}
		exactClient := defaultTmuxClientWithSocketRunner(exact, route.socketName)
		create.reconciler = newRegistryReconcilerWithRoute(exact, exactClient, route)
		create.reconciler.discoverRoots = func() ([]string, error) { return nil, nil }
		create.runtime.runner = exact
		create.runtime.mirror = intmetadata.NewMirror(exact)
		create.runtime.sessions = exactClient
		create.runtime.target = route.target
		create.runtime.expectedSocketPath = route.expectedSocketPath
		create.runtime.socketName = route.socketName
		create.runtime.routeAuthority = route.authority
		return nil
	}
	create.bindRuntime = func(ctx context.Context) error { return bind(ctx, false) }
	create.bindExplicitRuntime = func(ctx context.Context) error { return bind(ctx, true) }
	resolveWorkspace := func(_ string, _ coremetadata.Registry, owner coremetadata.Project, _, cwd string, additional []string) (coremetadata.AgentWorkspace, error) {
		if strings.TrimSpace(cwd) == "" {
			cwd = owner.Spec.Root
		}
		return coremetadata.AgentWorkspace{CWD: cwd, AdditionalWritableRoots: additional}, nil
	}
	rebinder := newAgentRebinder(create, realTmuxNameHandoffLauncher{})
	rebinder.resolveWorkspace = resolveWorkspace
	agents := &agentCommand{
		loadRegistry: store.store().load, store: store.store(), now: time.Now,
		resolveWorkspace: resolveWorkspace, rebind: rebinder,
	}

	stdout, stderr, err := runRoute(t, agents, "resume", "uid:"+test.agentUID)
	if err != nil || stdout != "agent/"+test.agentName+" resumed\n" {
		reason := ""
		if err != nil {
			reason = err.Error()
		}
		t.Fatalf("agent resume into a stopped Project over a dead ambient anchor: err=%v stdout=%q stderr=%q\n"+
			"anchor reason (error names \"anchor pane\"): %t\n"+
			"materializer entry (error names \"materialize Registry topology\"): %t",
			err, stdout, stderr,
			strings.Contains(reason, "anchor pane") || strings.Contains(stderr, "anchor pane"),
			strings.Contains(reason, "materialize Registry topology") || strings.Contains(stderr, "materialize Registry topology"))
	}
	if policies[false] != 0 {
		t.Fatalf("resume ran the anchored binder %d time(s)", policies[false])
	}
	if policies[true] == 0 {
		t.Fatal("resume bound no route at all, so this test proved nothing about the route")
	}

	agent, ok := store.registry.Agent(test.agentUID)
	if !ok || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef == "" || agent.Status.PaneRef == test.oldUID {
		t.Fatalf("agent/%s = %+v, want Running on a new Pane UID", test.agentName, agent.Status)
	}
	// The resume started the Project's session exactly once, and the resumed
	// Pane is live on it -- it really materialized rather than only bookkeeping.
	sessions, err := server.tmux("list-sessions", "-F", "#{session_name}")
	if err != nil {
		t.Fatalf("list isolated tmux sessions: %v: %s", err, sessions)
	}
	projectSessions := 0
	for name := range strings.SplitSeq(sessions, "\n") {
		if name == sessionName {
			projectSessions++
		}
	}
	if projectSessions != 1 {
		t.Fatalf("isolated server has %d %q session(s), want exactly one: %q", projectSessions, sessionName, sessions)
	}
	if labels := server.livePaneLabels(t); len(labels[agent.Status.PaneRef]) != 1 {
		t.Fatalf("resumed Pane %s is not live exactly once on the isolated server: %v", agent.Status.PaneRef, labels)
	}
	project, ok := store.registry.Project("prj-name-handoff")
	if !ok || project.Status.Session == nil || !project.Status.Session.Live {
		t.Fatalf("Project status after resume = %+v, want a Live session", project.Status)
	}
}
