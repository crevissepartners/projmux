package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
)

// sidebarDeadAnchorRecordingRunner records the argv of every tmux call the
// sidebar Continue pre-check makes and forwards it unchanged to the real
// ExecRunner, so the test can prove the refusal was observed on the isolated
// server and that every call on the way to it was a read.
type sidebarDeadAnchorRecordingRunner struct {
	mu    sync.Mutex
	calls [][]string
}

func (r *sidebarDeadAnchorRecordingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string{name}, args...))
	r.mu.Unlock()
	return inttmux.ExecRunner{}.Run(ctx, name, args...)
}

func (r *sidebarDeadAnchorRecordingRunner) recorded() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]string(nil), r.calls...)
}

// sidebarDeadAnchorMutatingVerb reports the first tmux verb in argv that can
// change server, client, or session state. Every argv token is checked, and a
// `;`-joined command sequence is split first, so a write hidden behind a read
// in one invocation is still caught.
func sidebarDeadAnchorMutatingVerb(argv []string) string {
	exact := map[string]bool{
		"set-option": true, "set": true, "set-environment": true, "setenv": true,
		"set-window-option": true, "setw": true, "new-session": true, "new": true,
		"new-window": true, "neww": true, "split-window": true, "splitw": true,
		"display-popup": true, "popup": true, "switch-client": true, "switchc": true,
		"send-keys": true, "send": true, "link-window": true, "linkw": true,
		"break-pane": true, "breakp": true, "join-pane": true, "joinp": true,
		"run-shell": true, "run": true, "source-file": true, "source": true,
		"set-hook": true, "bind-key": true, "bind": true, "unbind-key": true, "unbind": true,
	}
	prefixes := []string{"kill-", "rename-", "respawn-", "select-", "attach", "resize-", "move-", "swap-"}
	for _, token := range argv {
		for segment := range strings.SplitSeq(token, ";") {
			segment = strings.TrimSpace(strings.TrimSuffix(segment, `\`))
			if exact[segment] {
				return segment
			}
			for _, prefix := range prefixes {
				if strings.HasPrefix(segment, prefix) {
					return segment
				}
			}
		}
	}
	return ""
}

// TestSidebarOpenContinueRefusesADeadAnchorThroughRealTmux is the acceptance
// observation for the sidebar Continue pre-check from a caller whose anchor
// Pane is gone, taken against a real tmux server.
//
// The setup is the one `start project` refuses in
// TestStartProjectRefusesADeadAnchorThroughRealTmux: an inherited TMUX
// receipt for a live app-owned server, and a TMUX_PANE plus a private producer
// anchor that both name a Pane the server never issued, with the Project
// stopped but still holding its Registry Window. Here the stopped Project is
// opened from the sidebar instead, through openSidebarClosedProject. After the
// trust gate, the sidebar reobserves its typed anchor through
// validateSidebarProjectOpenRoute before prepareProjectContinue can write the
// Registry or the topology can be materialized. That pre-check must refuse in
// the same class as `start project` -- the anchor Pane is gone, not that
// containment drifted or that tmux returned no row -- and must do so with
// reads only: the Registry is neither opened for writing nor changed, the
// session executor is never reached, and the isolated server's sessions,
// Windows, Panes, and global options are exactly what they were before.
func TestSidebarOpenContinueRefusesADeadAnchorThroughRealTmux(t *testing.T) {
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

	// The production closure reads the process environment and its runner
	// inherits it, so the dead anchor is set on the process, not on a stub.
	// TMUX_TMPDIR makes the app's `-L pnh` resolve to the isolated socket.
	const deadAnchor = "%99999"
	t.Setenv("TMUX_TMPDIR", server.root)
	t.Setenv("TMUX", server.socket+","+serverPID+",0")
	t.Setenv("TMUX_PANE", deadAnchor)
	t.Setenv(runtimeMutationAnchorPaneEnv, deadAnchor)

	topology := newRegistryProjectTopologyMaterializer()
	topology.resources = store.store()

	target, err := tmuxSocketNameTarget(server.logical)
	if err != nil {
		t.Fatal(err)
	}
	runner := &sidebarDeadAnchorRecordingRunner{}
	home := t.TempDir()
	executor := &capturingSwitchSessionExecutor{authorizeSet: true, authorizeResult: true}
	cmd := &switchCommand{
		sessions:        executor,
		identity:        stubSwitchIdentityResolver{name: sessionName},
		tmuxRunner:      runner,
		homeDir:         func() (string, error) { return home, nil },
		lookupEnv:       os.Getenv,
		projectTopology: topology,
		projectFreshStart: &registryProjectFreshStarter{
			resources: store.store(), runner: runner, target: target, shell: "/bin/sh",
		},
		startupNotices: &recordingProjectStartupReporter{},
	}
	// This mirrors newSwitchCommand's wiring in switch.go exactly: the same
	// validator over the command's own tmux runner and environment. Only the
	// Registry reader is the fake store's snapshot, never the live Registry.
	cmd.validateProjectOpenRoute = func(ctx context.Context, anchor string) error {
		return validateSidebarProjectOpenRoute(ctx, cmd.tmuxRunner, cmd.lookupEnv, store.store().snapshot, anchor)
	}

	observe := func(label string, args ...string) string {
		t.Helper()
		out, err := server.tmux(args...)
		if err != nil {
			t.Fatalf("observe isolated tmux %s: %v: %s", label, err, out)
		}
		return out
	}
	observeServer := func() map[string]string {
		t.Helper()
		return map[string]string{
			"sessions": observe("sessions", "list-sessions", "-F", "#{session_id} #{session_name}"),
			"windows": observe("windows", "list-windows", "-a", "-F",
				"#{session_id} #{window_id} #{window_index} #{window_name}"),
			"panes": observe("panes", "list-panes", "-a", "-F",
				"#{pane_id} #{window_id} #{pane_pid} #{pane_dead}"),
			"global options": observe("global options", "show-options", "-g"),
		}
	}
	registryBefore := store.snapshot()
	writesBefore, transactionsBefore := store.writes, store.transactions
	serverBefore := observeServer()

	openCtx, openCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer openCancel()
	err = cmd.openSidebarClosedProject(openCtx, projectRoot, sessionName, deadAnchor,
		projectStartupCandidate{Kind: projectStartupKindTopology})

	calls := runner.recorded()
	for i, call := range calls {
		t.Logf("tmux call %d: %q", i, call)
	}
	if err == nil {
		t.Fatalf("sidebar Continue opened a Project over a dead anchor Pane: executor=%v", executor.calls)
	}
	message := err.Error()
	t.Logf("observed refusal: %s", message)
	want := "inherited invocation anchor pane " + deadAnchor + " no longer exists on the proven server"
	if !strings.Contains(message, want) {
		t.Fatalf("sidebar Continue refusal = %q, want it to contain %q", message, want)
	}
	for _, reject := range []string{"containment drifted", "no single containment row"} {
		if strings.Contains(message, reject) {
			t.Fatalf("sidebar Continue refusal = %q, which misreports an absent anchor Pane as %q", message, reject)
		}
	}
	if store.transactions != transactionsBefore || store.writes != writesBefore {
		t.Fatalf("refused sidebar Continue opened the Registry for writing: transactions %d -> %d, writes %d -> %d",
			transactionsBefore, store.transactions, writesBefore, store.writes)
	}
	if after := store.snapshot(); after != registryBefore {
		t.Fatalf("refused sidebar Continue changed the Registry:\nbefore:\n%s\nafter:\n%s", registryBefore, after)
	}
	for _, call := range executor.calls {
		if strings.HasPrefix(call, "ensure:") || strings.HasPrefix(call, "open:") {
			t.Fatalf("refused sidebar Continue reached the session executor: %v", executor.calls)
		}
	}
	if len(calls) == 0 {
		t.Fatal("sidebar Continue refused without asking tmux anything; the pre-check did not run against the server")
	}
	for _, call := range calls {
		if verb := sidebarDeadAnchorMutatingVerb(tmuxCommandArgv(call[1:])); verb != "" {
			t.Fatalf("refused sidebar Continue ran mutating tmux verb %q: %q", verb, call)
		}
	}
	serverAfter := observeServer()
	for _, key := range []string{"sessions", "windows", "panes", "global options"} {
		if serverAfter[key] != serverBefore[key] {
			t.Fatalf("refused sidebar Continue changed isolated tmux %s:\nbefore:\n%s\nafter:\n%s",
				key, serverBefore[key], serverAfter[key])
		}
	}
	names := observe("session names", "list-sessions", "-F", "#{session_name}")
	if names != "keeper" {
		t.Fatalf("isolated tmux sessions after the refusal = %q, want only keeper", names)
	}
}
