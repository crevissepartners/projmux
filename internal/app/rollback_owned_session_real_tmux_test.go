package app

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// ownedSessionRollbackRealTmuxEnv makes tmux mandatory for the real-tmux
// rollback boundary, so a host without tmux fails instead of skipping.
const ownedSessionRollbackRealTmuxEnv = "PMX_TEST_OWNED_SESSION_ROLLBACK_REAL_TMUX"

// TestRollbackRemovesAnOwnedSessionThroughRealTmux drives the rollback a
// refused create runs after it has already started a new tmux server, on that
// isolated real server.
//
// Only real tmux can prove this one: the kill-owned reobserve picks its list
// command by target kind and used to add -a for all three kinds, but -a is a
// list-windows/list-panes flag. tmux rejected `list-sessions -a` while parsing
// argv, before the server was even consulted, so the pre-write observation
// errored, the plan stopped at "observation unknown before first write for
// action kill-owned", and no kill ran — leaving the session this operation had
// just created alive on the server it had just started. A fake runner cannot
// see that, because the rejection is tmux's own argv parsing.
func TestRollbackRemovesAnOwnedSessionThroughRealTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		if os.Getenv(ownedSessionRollbackRealTmuxEnv) == "1" {
			t.Fatalf("%s=1 requires tmux: %v", ownedSessionRollbackRealTmuxEnv, err)
		}
		t.Skip("tmux is not installed")
	}

	// "last session" is the shape the Backlog reported: the refused create had
	// started the server, so rolling its session back empties the server.
	// "bystander session" keeps the server alive past the kill, so the
	// post-write observation is a real list-sessions read against a live
	// server rather than the IsNoServerFailure path.
	for _, row := range []struct {
		name      string
		bystander bool
	}{
		{name: "last session"},
		{name: "bystander session", bystander: true},
	} {
		t.Run(row.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			root, err := os.MkdirTemp("", "pkos-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.RemoveAll(root) })
			if root, err = filepath.EvalSymlinks(root); err != nil {
				t.Fatal(err)
			}
			// The dedicated TMUX_TMPDIR exists before the first tmux call, and
			// every call also carries -S, so no command can fall back to the
			// operator's live socket directory.
			environment := []string{"TMUX_TMPDIR=" + root}
			for _, entry := range os.Environ() {
				key, _, _ := strings.Cut(entry, "=")
				if key == "TMUX" || key == "TMUX_PANE" || key == "TMUX_TMPDIR" || key == runtimeMutationAnchorPaneEnv {
					continue
				}
				environment = append(environment, entry)
			}
			socket := filepath.Join(root, "s")
			t.Logf("isolated TMUX_TMPDIR=%s socket=%s", root, socket)
			tmux := func(args ...string) (string, error) {
				command := exec.CommandContext(ctx, "tmux", append([]string{"-S", socket, "-f", "/dev/null"}, args...)...)
				command.Env = environment
				out, err := command.CombinedOutput()
				return strings.TrimSpace(string(out)), err
			}

			// This new-session starts the isolated server, which is the create
			// path's own "fresh server" shape.
			receipt, err := tmux("new-session", "-d", "-s", "owned", "-P", "-F", "#{session_id}\t#{pid}", "tail", "-f", "/dev/null")
			if err != nil {
				t.Fatalf("start isolated tmux: %v: %s", err, receipt)
			}
			sessionID, serverPID, _ := strings.Cut(receipt, "\t")
			// Registered after the root removal above, so LIFO kills the server
			// first, while its socket still exists.
			killRealTmuxServerOnCleanup(t, environment, socket, realTmuxServerPID(t, serverPID))
			if row.bystander {
				if out, err := tmux("new-session", "-d", "-s", "bystander", "tail", "-f", "/dev/null"); err != nil {
					t.Fatalf("start bystander session: %v: %s", err, out)
				}
			}
			for _, option := range [][2]string{{tmuxopts.AppGlobal, "1"}, {runtimeMutationSocketNameOption, "owned-rollback"}} {
				if out, err := tmux("set-option", "-g", option[0], option[1]); err != nil {
					t.Fatalf("mark isolated tmux app-owned: %v: %s", err, out)
				}
			}

			target := tmuxTransport{Kind: tmuxSocketPath, Value: socket, Source: tmuxSocketPathSource}
			routed := explicitTmuxRunner{runner: shellTmuxExecRunner{env: func() []string { return environment }}, target: target}
			var warnings bytes.Buffer
			runtime := &materializer{runner: routed, mirror: intmetadata.NewMirror(routed), target: target, warn: &warnings}
			if err := runtime.guardExactRoute(ctx, false); err != nil {
				t.Fatalf("bind isolated app route: %v", err)
			}

			ledger := newRuntimeLedger("op-owned-rollback")
			if err := runtime.claimRuntimeUIDForRollback(ctx, runtimeSession, sessionID, "prj-owned-rollback", ledger); err != nil {
				t.Fatalf("claim created session %s: %v", sessionID, err)
			}
			if entries := ledger.entries(); len(entries) != 1 || entries[0].ID != sessionID {
				t.Fatalf("ledger = %#v, want one session %s", entries, sessionID)
			}

			runtime.rollback(ctx, ledger)

			// C-2's failure signature. It is the whole point of the argv fix:
			// a rejected list-sessions made the pre-write observation unknown,
			// so the kill never ran.
			if strings.Contains(warnings.String(), "observation unknown") {
				t.Fatalf("rollback never reached its kill: %q", warnings.String())
			}
			live, listErr := tmux("list-sessions", "-F", "#{session_id}\t#{session_name}")
			switch {
			case row.bystander:
				if strings.Contains(warnings.String(), "rollback stopped") {
					t.Fatalf("rollback stopped on a live server: %q", warnings.String())
				}
				if listErr != nil {
					t.Fatalf("list sessions after rollback: %v: %s", listErr, live)
				}
				if live != "" && strings.Contains(live, sessionID) {
					t.Fatalf("rollback kept the owned session: %q", live)
				}
				if !strings.Contains(live, "bystander") {
					t.Fatalf("rollback removed the bystander session: %q", live)
				}
			default:
				// Killing the server's last session ends the server, so an
				// empty listing and a no-server refusal are the same result.
				if listErr == nil && live != "" {
					t.Fatalf("rollback kept the owned session: %q", live)
				}
				if listErr != nil && !strings.Contains(live, "no server running") {
					t.Fatalf("list sessions after rollback: %v: %s", listErr, live)
				}
				// Ending the server is what this kill does, so the plan's own
				// route reobserve can no longer reach it and says so. That
				// residual belongs to the route authority, not to C-2, and it
				// is only tolerated once the session is provably gone above.
				if stopped := warnings.String(); strings.Contains(stopped, "rollback stopped") &&
					!strings.Contains(stopped, "no server running on "+socket) {
					t.Fatalf("rollback stopped for a reason other than the ended server: %q", stopped)
				}
			}
		})
	}
}
