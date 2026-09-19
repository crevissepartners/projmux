package app

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	inttmux "github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

// rollbackTmuxSeam is the fake server with the two behaviors this rollback
// table needs and fakeTmux deliberately does not model, so no other test's
// meaning changes: tmux removes a Window together with its last Pane, and a
// server can stop answering after one failed write.
type rollbackTmuxSeam struct {
	*fakeTmux
	// failKillWindow makes the next kill-window fail with a server error and
	// every later list-windows fail too: the route itself is gone.
	failKillWindow bool
	serverLost     bool
}

func (r *rollbackTmuxSeam) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	lostServer := appTypedCommandFailure{failure: inttmux.CommandFailure{
		Kind: inttmux.CommandFailureExit, Stderr: "server exited unexpectedly",
	}}
	switch {
	case r.failKillWindow && slices.Contains(args, "kill-window"):
		r.failKillWindow, r.serverLost = false, true
		r.fakeTmux.calls = append(r.fakeTmux.calls, append([]string(nil), args...))
		return nil, lostServer
	case r.serverLost && slices.Contains(args, "list-windows"):
		return nil, lostServer
	}
	out, err := r.fakeTmux.Run(ctx, name, args...)
	if err == nil && slices.Contains(args, "kill-pane") {
		r.mu.Lock()
		for _, session := range r.sessions {
			session.windows = slices.DeleteFunc(session.windows, func(w *fakeTmuxWindow) bool { return len(w.panes) == 0 })
		}
		r.mu.Unlock()
	}
	return out, err
}

// TestRollbackContinuesPastAnObjectAnEarlierKillRemoved is the shared create
// rollback's execution-time contract. Every guard runs before the first kill,
// so an earlier kill of the same plan can remove a later target: that step has
// reached its desired absence and the rest of the ledger is still rolled back.
// An object that carries another uid is still preserved with its warning, and
// a failure that leaves the target's absence unproven still stops the plan.
func TestRollbackContinuesPastAnObjectAnEarlierKillRemoved(t *testing.T) {
	for _, tt := range []struct {
		name string
		// arrange seeds the server and returns the ledger this operation
		// recorded plus the check of the state rollback must leave.
		arrange func(t *testing.T, seam *rollbackTmuxSeam) (*runtimeLedger, func(t *testing.T, warnings string))
	}{
		{
			name: "a Window removed with its last Pane lets the session kill run",
			arrange: func(t *testing.T, seam *rollbackTmuxSeam) (*runtimeLedger, func(*testing.T, string)) {
				session := seam.addSession("owned")
				session.opts[tmuxopts.ProjectUIDSession] = "prj-owned"
				created := seedLiveWindow(t, seam.fakeTmux, session, "win-owned", "pan-owned")
				ledger := &runtimeLedger{}
				ledger.record(runtimeSession, session.id, "prj-owned")
				ledger.record(runtimeWindow, created.id, "win-owned")
				ledger.record(runtimePane, created.panes[0].id, "pan-owned")
				return ledger, func(t *testing.T, warnings string) {
					if seam.session("owned") != nil {
						t.Fatalf("rollback left the session it created:\n%s", seam.state())
					}
					if strings.Contains(warnings, "rollback stopped") {
						t.Fatalf("rollback stopped on a Window its own Pane kill removed: %q", warnings)
					}
				}
			},
		},
		{
			name: "an object carrying another uid is preserved and the rest rolls back",
			arrange: func(t *testing.T, seam *rollbackTmuxSeam) (*runtimeLedger, func(*testing.T, string)) {
				owned := seam.addSession("owned")
				owned.opts[tmuxopts.ProjectUIDSession] = "prj-owned"
				other := seam.addSession("other")
				window := seedLiveWindow(t, seam.fakeTmux, other, "win-owned", "pan-owned")
				window.opts[tmuxopts.WindowUID] = "win-someone-else"
				ledger := &runtimeLedger{}
				ledger.record(runtimeSession, owned.id, "prj-owned")
				ledger.record(runtimeWindow, window.id, "win-owned")
				return ledger, func(t *testing.T, warnings string) {
					if _, got := seam.window(window.id); got == nil {
						t.Fatal("rollback removed a Window that carries another uid")
					}
					if !strings.Contains(warnings, "rollback preserved window "+window.id) {
						t.Fatalf("ownership drift warning = %q", warnings)
					}
					if seam.session("owned") != nil {
						t.Fatalf("rollback did not reach the owned session after the preserved Window:\n%s", seam.state())
					}
				}
			},
		},
		{
			name: "a lost server stops the plan as before",
			arrange: func(t *testing.T, seam *rollbackTmuxSeam) (*runtimeLedger, func(*testing.T, string)) {
				session := seam.addSession("owned")
				session.opts[tmuxopts.ProjectUIDSession] = "prj-owned"
				created := seedLiveWindow(t, seam.fakeTmux, session, "win-owned", "pan-owned")
				seam.failKillWindow = true
				ledger := &runtimeLedger{}
				ledger.record(runtimeSession, session.id, "prj-owned")
				ledger.record(runtimeWindow, created.id, "win-owned")
				return ledger, func(t *testing.T, warnings string) {
					if !strings.Contains(warnings, "rollback stopped before an unguarded runtime write") ||
						!strings.Contains(warnings, "server exited unexpectedly") && !strings.Contains(warnings, "typed tmux command failure") {
						t.Fatalf("warning = %q, want the plan stopped on the server failure", warnings)
					}
					if seam.session("owned") == nil {
						t.Fatal("rollback went on past a failure it could not prove to be absence")
					}
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			seam := &rollbackTmuxSeam{fakeTmux: newFakeTmux()}
			var warnings bytes.Buffer
			runtime := &materializer{
				runner: seam, mirror: intmetadata.NewMirror(seam), warn: &warnings,
				target:             tmuxTransport{Kind: tmuxSocketPath, Value: seam.socketPath, Source: tmuxSocketPathSource},
				expectedSocketPath: seam.socketPath,
				socketName:         defaultAppSocket,
				routeAuthority:     &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: seam.serverPID},
			}
			ledger, check := tt.arrange(t, seam)
			runtime.rollback(context.Background(), ledger)
			check(t, warnings.String())
		})
	}
}
