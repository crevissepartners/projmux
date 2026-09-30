package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// selfTargetPaneCallerEnv names the directory the in-Pane caller of
// TestRestartSelfTargetFollowsTheProcessChainThroughRealTmux reads its
// Registry from and writes its result to. Only that test sets it, on the
// process it starts inside the Agent's Pane.
//
// The package's TestMain strips TMUX and TMUX_PANE from every test process, so
// the shell tmux runs in the Pane hands its own values of the two to the
// caller under the other two names.
const (
	selfTargetPaneCallerEnv     = "PMX_TEST_SELF_TARGET_PANE_CALLER"
	selfTargetPaneCallerTmuxEnv = "PMX_TEST_SELF_TARGET_PANE_CALLER_TMUX"
	selfTargetPaneCallerPaneEnv = "PMX_TEST_SELF_TARGET_PANE_CALLER_TMUX_PANE"
)

// selfTargetPaneCallerResult is what the in-Pane caller observed.
type selfTargetPaneCallerResult struct {
	Err     string `json:"err"`
	Usage   bool   `json:"usage"`
	Stdout  string `json:"stdout"`
	TmuxEnv string `json:"tmuxEnv"`
	PaneEnv string `json:"paneEnv"`
	Chain   []int  `json:"chain"`
}

// newSelfTargetRealTmuxCommand is the `agent` namespace over registry with the
// production self target judgment: the real parent-chain walk, and the tmux
// binary through runner (nil is the production runner). Settings resolve
// under home, so the machine's own profiles are never read.
func newSelfTargetRealTmuxCommand(registry coremetadata.Registry, home string, lookupEnv func(string) string, runner tmuxCommandRunner) *agentCommand {
	store := &fakeResourceStore{registry: registry, dirs: map[string]bool{}, now: resourceFixtureClock}
	create := &createCommand{
		store:     store.store(),
		homeDir:   func() (string, error) { return home, nil },
		lookupEnv: func(string) string { return "" },
	}
	resolveWorkspace := func(_ string, _ coremetadata.Registry, owner coremetadata.Project, _, cwd string, additional []string) (coremetadata.AgentWorkspace, error) {
		if strings.TrimSpace(cwd) == "" {
			cwd = owner.Spec.Root
		}
		return coremetadata.AgentWorkspace{CWD: cwd, AdditionalWritableRoots: additional}, nil
	}
	rebinder := newAgentRebinder(create, realTmuxNameHandoffLauncher{})
	rebinder.resolveWorkspace = resolveWorkspace
	return &agentCommand{
		loadRegistry: store.store().load, store: store.store(), now: time.Now,
		resolveWorkspace: resolveWorkspace, rebind: rebinder,
		lookupEnv: lookupEnv, selfTargetRunner: runner,
	}
}

// TestRestartSelfTargetPaneCallerProcess is not a test of its own: it is the
// caller TestRestartSelfTargetFollowsTheProcessChainThroughRealTmux starts
// inside the Agent's Pane. It runs `agent relaunch --dry-run` with the TMUX
// and TMUX_PANE tmux gave that Pane and the production judgment, and writes
// what it got. Without selfTargetPaneCallerEnv it does nothing.
func TestRestartSelfTargetPaneCallerProcess(t *testing.T) {
	dir := os.Getenv(selfTargetPaneCallerEnv)
	if dir == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(dir, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	var registry coremetadata.Registry
	if err := json.Unmarshal(data, &registry); err != nil {
		t.Fatal(err)
	}
	agentUID, err := os.ReadFile(filepath.Join(dir, "agent"))
	if err != nil {
		t.Fatal(err)
	}
	inherited := map[string]string{"TMUX": os.Getenv(selfTargetPaneCallerTmuxEnv), "TMUX_PANE": os.Getenv(selfTargetPaneCallerPaneEnv)}
	command := newSelfTargetRealTmuxCommand(registry, filepath.Join(dir, "home"), func(key string) string { return inherited[key] }, nil)
	stdout, _, runErr := runRoute(t, command, "relaunch", "uid:"+string(agentUID), "--dry-run")
	result := selfTargetPaneCallerResult{Stdout: stdout, TmuxEnv: inherited["TMUX"], PaneEnv: inherited["TMUX_PANE"]}
	result.Chain, _ = processAncestry()
	if runErr != nil {
		result.Err, result.Usage = runErr.Error(), IsUsageError(runErr)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	// Written whole, then renamed: the waiting test never reads half a result.
	pending := filepath.Join(dir, "result.json.tmp")
	if err := os.WriteFile(pending, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(pending, filepath.Join(dir, "result.json")); err != nil {
		t.Fatal(err)
	}
}

// TestRestartSelfTargetFollowsTheProcessChainThroughRealTmux is the real-tmux
// observation of the self target judgment of a restart, on an isolated server
// whose Agent Pane runs a stand-in provider.
//
// Two callers carry that Pane's id in their environment. This test process is
// outside the Pane: it holds the id the way a process started from an Agent's
// shell and detached from it does, once as the private producer anchor with
// --socket and no TMUX at all, and once as an inherited TMUX and TMUX_PANE.
// Neither is refused, because its parent chain does not hold the Pane's
// process. The other caller is a process tmux really runs inside the Pane,
// with the environment tmux gave it: it is refused with relaunch-self-target.
// A caller whose server cannot be read is refused with the same token and told
// the judgment could not be made.
func TestRestartSelfTargetFollowsTheProcessChainThroughRealTmux(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	server := startRealTmuxNameHandoffServer(t, ctx)
	projectRoot := filepath.Join(server.root, "project")
	callerDir := filepath.Join(server.root, "caller")
	for _, dir := range []string{projectRoot, callerDir, filepath.Join(callerDir, "home")} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	const sessionName = "self-target"
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
	windowID, serverPID := fields[1], fields[3]
	server.seed(t)

	// The Agent's managed Pane, running a stand-in provider for now.
	agentPane, err := server.tmux("split-window", "-d", "-t", windowID, "-c", projectRoot, "-P", "-F", "#{pane_id}", "tail", "-f", "/dev/null")
	if err != nil || !strings.HasPrefix(agentPane, "%") {
		t.Fatalf("split the Agent Pane: %v: %s", err, agentPane)
	}

	store := realTmuxNameHandoffRegistry(t, projectRoot, sessionName, true)
	agent, err := store.mutator().CreateAgent(&store.registry, "win-name-handoff", coremetadata.CreateAgentOptions{
		Name: "worker", Provider: "claude", Workspace: coremetadata.AgentWorkspace{CWD: projectRoot}, OperationID: "op-self-target-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	pane := attachTopologyAgentPane(t, store, agent.Metadata.UID, "worker-pane", projectRoot)
	storedAgent, _ := store.registry.Agent(agent.Metadata.UID)
	storedAgent.Status.Phase = coremetadata.PhaseRunning
	storedAgent.Status.SessionRef = claudeConversationRef("conv-self-target")
	storedPane, _ := store.registry.Pane(pane.Metadata.UID)
	storedPane.Status.Activation = selfTargetActivation(agent.Metadata.UID, agentPane)
	if err := store.registry.Validate(); err != nil {
		t.Fatalf("self target real-tmux fixture: %v", err)
	}
	if storedAgent.Status.PaneRef != pane.Metadata.UID {
		t.Fatalf("agent/worker paneRef = %q, want %s", storedAgent.Status.PaneRef, pane.Metadata.UID)
	}
	if out, err := server.tmux("set-option", "-p", "-t", agentPane, tmuxopts.PaneUID, pane.Metadata.UID); err != nil {
		t.Fatalf("mirror the Agent Pane uid: %v: %s", err, out)
	}
	registry := store.registry.Clone()
	relaunch := []string{"relaunch", "uid:" + agent.Metadata.UID, "--dry-run"}
	runner := shellTmuxExecRunner{env: func() []string { return server.environment }}

	// Callers outside the Pane.
	for _, test := range []struct {
		name     string
		env      map[string]string
		flags    []string
		refused  bool
		wantText string
	}{
		{name: "anchor variable with --socket", flags: []string{"--socket", server.logical},
			env: map[string]string{runtimeMutationAnchorPaneEnv: agentPane}},
		{name: "inherited TMUX and TMUX_PANE",
			env: map[string]string{"TMUX": server.socket + "," + serverPID + ",0", "TMUX_PANE": agentPane}},
		{name: "a server that cannot be read", flags: []string{"--socket", server.logical + "-absent"}, refused: true,
			env:      map[string]string{runtimeMutationAnchorPaneEnv: agentPane},
			wantText: selfTargetUnobservedWording + "that Pane could not be read on the tmux server this command addresses;"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := newSelfTargetRealTmuxCommand(registry.Clone(), filepath.Join(callerDir, "home"),
				func(key string) string { return test.env[key] }, runner)
			stdout, stderr, err := runRoute(t, command, append(slices.Clone(relaunch), test.flags...)...)
			if test.refused {
				assertSelfTargetRefusal(t, err, relaunchReasonSelfTarget, test.wantText)
				return
			}
			if err != nil {
				t.Fatalf("relaunch --dry-run from outside the Pane: err=%v stdout=%q stderr=%q, want no refusal", err, stdout, stderr)
			}
			if !strings.HasPrefix(stdout, "agent/worker ") {
				t.Fatalf("relaunch --dry-run stdout = %q, want the worker's dry-run line", stdout)
			}
		})
	}

	// The caller inside the Pane: tmux respawns the Pane with a shell whose
	// child is this test binary, so the Pane's process is that caller's
	// parent and TMUX and TMUX_PANE are the ones tmux set.
	encoded, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{"registry.json": encoded, "agent": []byte(agent.Metadata.UID)} {
		if err := os.WriteFile(filepath.Join(callerDir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const script = selfTargetPaneCallerEnv + `="$0" ` + selfTargetPaneCallerTmuxEnv + `="$TMUX" ` + selfTargetPaneCallerPaneEnv + `="$TMUX_PANE" "$1" -test.run '^TestRestartSelfTargetPaneCallerProcess$' -test.count=1 >"$0/caller.log" 2>&1; exec tail -f /dev/null`
	if out, err := server.tmux("respawn-pane", "-k", "-t", agentPane, "sh", "-c", script, callerDir, binary); err != nil {
		t.Fatalf("respawn the Agent Pane with the in-Pane caller: %v: %s", err, out)
	}
	var result selfTargetPaneCallerResult
	resultPath := filepath.Join(callerDir, "result.json")
	for {
		data, err := os.ReadFile(resultPath)
		if err == nil {
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatalf("in-Pane caller result %q: %v", data, err)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			log, _ := os.ReadFile(filepath.Join(callerDir, "caller.log"))
			t.Fatalf("the in-Pane caller wrote no result; its output: %q", log)
		case <-time.After(50 * time.Millisecond):
		}
	}
	panePIDText, err := server.tmux("display-message", "-p", "-t", agentPane, "#{pane_pid}")
	if err != nil {
		t.Fatalf("read the Agent Pane process: %v: %s", err, panePIDText)
	}
	panePID, err := strconv.Atoi(panePIDText)
	if err != nil {
		t.Fatalf("Agent Pane process id %q: %v", panePIDText, err)
	}
	// The caller really was inside the Pane, with the environment tmux sets.
	if result.PaneEnv != agentPane || !strings.HasPrefix(result.TmuxEnv, server.socket+",") {
		t.Fatalf("in-Pane caller environment TMUX=%q TMUX_PANE=%q, want %s on %s", result.TmuxEnv, result.PaneEnv, agentPane, server.socket)
	}
	if !slices.Contains(result.Chain, panePID) {
		t.Fatalf("in-Pane caller parent chain %v does not hold the Pane process %d", result.Chain, panePID)
	}
	if !result.Usage || result.Stdout != "" {
		t.Fatalf("in-Pane caller: usage=%t stdout=%q err=%q, want a usage refusal and no output", result.Usage, result.Stdout, result.Err)
	}
	want := "agent relaunch: agent/worker owns the Pane this command runs in; closing that Pane would end the command before the resume. Run it from another Pane (relaunch-self-target); nothing was changed"
	if result.Err != want {
		t.Fatalf("in-Pane caller refusal = %q, want %q", result.Err, want)
	}
	// And this process is not in that Pane.
	if chain, err := processAncestry(); err != nil || slices.Contains(chain, panePID) {
		t.Fatalf("this test's parent chain %v (err=%v) holds the Pane process %d", chain, err, panePID)
	}
}
