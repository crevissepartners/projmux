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
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// resumeNoAnchorRealTmuxEnv makes tmux mandatory for this boundary.
// test/integration/agent-resume-without-anchor.sh sets it so a missing tmux
// fails the integration suite instead of skipping.
const resumeNoAnchorRealTmuxEnv = "PMX_TEST_RESUME_NO_ANCHOR_REAL_TMUX"

// TestAgentResumeNeedsNoAnchorPaneThroughRealTmux is the acceptance observation
// for reviving an Agent whose surroundings have gone stale, taken against a
// real tmux server.
//
// The caller's environment is the one an operator actually holds when an Agent
// needs resuming: an inherited TMUX receipt for a live app-owned server, and a
// TMUX_PANE plus a private producer anchor that both name a Pane the server no
// longer has. Two things must be true at once. The anchored policy must refuse
// that environment and say the anchor Pane is gone -- not that the socket or
// the server generation drifted, which is what it used to say and what sent
// the operator looking at two things that were both correct. And `agent resume`
// must not need an anchor Pane at all: the Agent is one exact reference and the
// split target is its Window's stored anchorPaneRef, so it binds the app's
// logical route on the server generation alone and completes.
func TestAgentResumeNeedsNoAnchorPaneThroughRealTmux(t *testing.T) {
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
	const sessionName = "name-handoff"
	created, err := server.tmux("new-session", "-d", "-s", sessionName, "-n", "main", "-x", "400", "-y", "100", "-c", projectRoot,
		"-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pid}\t#{socket_path}", "tail", "-f", "/dev/null")
	if err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, created)
	}
	t.Cleanup(server.killServer)
	fields := strings.Split(created, "\t")
	if len(fields) != 5 || fields[4] != server.socket {
		t.Fatalf("isolated tmux receipt = %q, want session/window/pane/pid on %s", created, server.socket)
	}
	sessionID, windowID, paneID, serverPID := fields[0], fields[1], fields[2], fields[3]
	server.seed(t)
	for _, option := range [][]string{
		{"-t", sessionID, tmuxopts.ProjectUIDSession, "prj-name-handoff"},
		{"-t", sessionID, tmuxopts.ProjectPathSession, projectRoot},
		{"-w", "-t", windowID, tmuxopts.AutomaticRenameWindow, "off"},
		{"-w", "-t", windowID, tmuxopts.WindowUID, "win-name-handoff"},
		{"-w", "-t", windowID, tmuxopts.WindowName, "main"},
		{"-p", "-t", paneID, tmuxopts.PaneUID, "pan-name-handoff"},
	} {
		if out, err := server.tmux(append([]string{"set-option"}, option...)...); err != nil {
			t.Fatalf("seed isolated tmux %q: %v: %s", option, err, out)
		}
	}
	store := realTmuxNameHandoffRegistry(t, projectRoot, sessionName, true)
	test := realTmuxNameHandoffCase{agentName: "revived", paneName: "revived-pane"}
	addRealTmuxNameHandoffAgent(t, store, projectRoot, &test, false)

	// The Pane the caller's environment still points at. It is spelled as a
	// handle the server has never issued, which is the state an operator is in
	// after the Pane they were standing in went away.
	const deadAnchor = "%99999"
	inherited := map[string]string{
		"TMUX":                       server.socket + "," + serverPID + ",0",
		"TMUX_PANE":                  deadAnchor,
		runtimeMutationAnchorPaneEnv: deadAnchor,
	}
	lookupEnv := func(key string) string { return inherited[key] }
	runner := shellTmuxExecRunner{env: func() []string { return server.environment }}

	// Control: the anchored policy, which is what every non-selector route
	// still uses, must refuse this environment and name the cause.
	if _, err := resolveInvocationRuntimeMutationRoute(ctx, runner, lookupEnv); err == nil {
		t.Fatal("the anchored policy bound a route on a Pane the real server does not have")
	} else if !strings.Contains(err.Error(), "anchor pane "+deadAnchor+" no longer exists") {
		t.Fatalf("real-tmux anchored refusal = %q, want it to name the absent anchor Pane", err)
	} else if strings.Contains(err.Error(), "containment drifted") {
		t.Fatalf("real-tmux anchored refusal still calls an absent Pane drift: %q", err)
	}

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
		t.Fatalf("agent resume over a dead ambient anchor: err=%v stdout=%q stderr=%q", err, stdout, stderr)
	}
	if policies[false] != 0 {
		t.Fatalf("resume ran the anchored binder %d time(s)", policies[false])
	}
	if policies[true] == 0 {
		t.Fatal("resume bound no route at all, so this test proved nothing about the route")
	}

	// The Agent is Running on a new Pane, and that Pane is live on the isolated
	// server -- the resume really materialized rather than only bookkeeping.
	agent, ok := store.registry.Agent(test.agentUID)
	if !ok || agent.Status.Phase != coremetadata.PhaseRunning || agent.Status.PaneRef == "" || agent.Status.PaneRef == test.oldUID {
		t.Fatalf("agent/%s = %+v, want Running on a new Pane UID", test.agentName, agent.Status)
	}
	if labels := server.livePaneLabels(t); len(labels[agent.Status.PaneRef]) != 1 {
		t.Fatalf("resumed Pane %s is not live exactly once on the isolated server: %v", agent.Status.PaneRef, labels)
	}
}
