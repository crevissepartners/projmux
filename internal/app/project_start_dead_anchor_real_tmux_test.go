package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// TestStartProjectRefusesADeadAnchorThroughRealTmux is the acceptance
// observation for `start project` from a caller whose anchor Pane is gone,
// taken against a real tmux server.
//
// The caller holds an inherited TMUX receipt for a live app-owned server, and
// a TMUX_PANE plus a private producer anchor that both name a Pane the server
// never issued. The Project is stopped but keeps its Registry Window, so
// `start` takes the Continue path and replays that topology through the
// production route closure newRegistryProjectTopologyMaterializer builds. That
// closure resolves the anchored inherited route, and the anchored policy must
// refuse it by saying the anchor Pane is gone -- not that containment drifted
// or that tmux returned no row -- before anything is written: the Registry is
// not saved, no session appears on the server, and no receipt is printed.
func TestStartProjectRefusesADeadAnchorThroughRealTmux(t *testing.T) {
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
	// The server is live and app-owned, but holds only an unrelated session:
	// the Project's own session is stopped.
	created, err := server.tmux("new-session", "-d", "-s", "keeper", "-x", "400", "-y", "100", "-c", server.root,
		"-P", "-F", "#{pid}\t#{socket_path}", "tail", "-f", "/dev/null")
	if err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, created)
	}
	t.Cleanup(server.killServer)
	serverPID, socketPath, _ := strings.Cut(created, "\t")
	if socketPath != server.socket || serverPID == "" {
		t.Fatalf("isolated tmux receipt = %q, want pid on %s", created, server.socket)
	}
	server.seed(t)

	const sessionName = "name-handoff"
	store := realTmuxNameHandoffRegistry(t, projectRoot, sessionName, false)
	writesBefore, transactionsBefore := store.writes, store.transactions

	// The production closure reads the process environment and its ExecRunner
	// inherits it, so the dead anchor is set on the process, not on a stub.
	// TMUX_TMPDIR makes the app's `-L pnh` resolve to the isolated socket.
	const deadAnchor = "%99999"
	t.Setenv("TMUX_TMPDIR", server.root)
	t.Setenv("TMUX", server.socket+","+serverPID+",0")
	t.Setenv("TMUX_PANE", deadAnchor)
	t.Setenv(runtimeMutationAnchorPaneEnv, deadAnchor)

	topology := newRegistryProjectTopologyMaterializer()
	topology.resources = store.store()
	var notices bytes.Buffer
	topology.notices = &notices

	target, err := tmuxSocketNameTarget(server.logical)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	switcher := &switchCommand{
		sessions:        executor,
		identity:        stubSwitchIdentityResolver{name: sessionName},
		tmuxRunner:      inttmux.ExecRunner{},
		homeDir:         func() (string, error) { return home, nil },
		lookupEnv:       os.Getenv,
		projectTopology: topology,
		projectFreshStart: &registryProjectFreshStarter{
			resources: store.store(), runner: inttmux.ExecRunner{}, target: target, shell: "/bin/sh",
		},
		startupNotices: &recordingProjectStartupReporter{},
	}
	cmd := &projectLifecycleCommand{verb: projectLifecycleStart, store: store.store(), switcher: switcher, lookupEnv: os.Getenv}

	stdout, stderr, err := runRoute(t, cmd, "project", "uid:prj-name-handoff")
	if err == nil {
		t.Fatalf("start project materialized over a dead anchor Pane: stdout=%q stderr=%q", stdout, stderr)
	}
	message := err.Error()
	for _, want := range []string{
		"materialize Registry topology for session \"" + sessionName + "\"",
		"inherited invocation anchor pane " + deadAnchor + " no longer exists on the proven server",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("start project refusal = %q, want it to contain %q", message, want)
		}
	}
	for _, reject := range []string{"containment drifted", "no single containment row"} {
		if strings.Contains(message, reject) {
			t.Fatalf("start project refusal = %q, which misreports an absent anchor Pane as %q", message, reject)
		}
	}
	if strings.Contains(strings.ToLower(stdout), "receipt") || strings.TrimSpace(stdout) != "" {
		t.Fatalf("refused start project printed %q, want no receipt", stdout)
	}
	if store.writes != writesBefore {
		t.Fatalf("refused start project committed %d Registry write(s) (transactions %d -> %d)",
			store.writes-writesBefore, transactionsBefore, store.transactions)
	}
	for _, call := range executor.calls {
		if strings.HasPrefix(call, "ensure:") || strings.HasPrefix(call, "open:") {
			t.Fatalf("refused start project reached the session executor: %v", executor.calls)
		}
	}
	sessions, err := server.tmux("list-sessions", "-F", "#{session_name}")
	if err != nil {
		t.Fatalf("list isolated tmux sessions: %v: %s", err, sessions)
	}
	if sessions != "keeper" {
		t.Fatalf("isolated tmux sessions after the refusal = %q, want only keeper", sessions)
	}
}
