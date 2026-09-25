package app

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	intmux "github.com/crevissepartners/projmux/internal/integrations/mux"
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
	if handled, out, err := answerTmuxReadSequence(ctx, name, args, r.Run); handled {
		return out, err
	}
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

// ownedKillRaceMaterializer is the exact app-route materializer the rollback
// table uses, over a plain fakeTmux whose beforeDispatch plays the concurrent
// writer.
func ownedKillRaceMaterializer(server *fakeTmux, warn *bytes.Buffer) *materializer {
	return &materializer{
		runner: server, mirror: intmetadata.NewMirror(server), warn: warn,
		target:             tmuxTransport{Kind: tmuxSocketPath, Value: server.socketPath, Source: tmuxSocketPathSource},
		expectedSocketPath: server.socketPath,
		socketName:         defaultAppSocket,
		routeAuthority:     &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: server.serverPID},
	}
}

// onNthOwnershipRead runs change, with the fake's lock held, just before the
// nth `display-message -t id -F #{option}` is served.
func onNthOwnershipRead(server *fakeTmux, id, option string, n int, change func(*fakeTmux)) {
	reads := 0
	server.beforeDispatch = func(f *fakeTmux, args []string) {
		if len(args) == 0 || args[0] != "display-message" || flagValue(args, "-t") != id || args[len(args)-1] != "#{"+option+"}" {
			return
		}
		reads++
		if reads == n {
			change(f)
		}
	}
}

func removeFakeSession(f *fakeTmux, id string) {
	f.sessions = slices.DeleteFunc(f.sessions, func(s *fakeTmuxSession) bool { return s.id == id })
}

func countTmuxVerb(server *fakeTmux, verb string) int {
	count := 0
	for _, call := range server.calls {
		if slices.Contains(tmuxCommandArgv(call), verb) {
			count++
		}
	}
	return count
}

func TestRollbackTreatsAnOwnedSessionRemovedBetweenReobserveAndGuardAsAbsent(t *testing.T) {
	server := newFakeTmux()
	vanishing := server.addSession("vanishing")
	vanishing.opts[tmuxopts.ProjectUIDSession] = "prj-vanishing"
	remaining := server.addSession("remaining")
	remaining.opts[tmuxopts.ProjectUIDSession] = "prj-remaining"
	// Read 1 is rollback's pre-plan ownership check; read 2 is the Guard's.
	// A concurrent writer kills the session between Reobserve and that read.
	onNthOwnershipRead(server, vanishing.id, tmuxopts.ProjectUIDSession, 2, func(f *fakeTmux) { removeFakeSession(f, vanishing.id) })
	ledger := &runtimeLedger{}
	ledger.record(runtimeSession, remaining.id, "prj-remaining")
	ledger.record(runtimeSession, vanishing.id, "prj-vanishing")
	var warnings bytes.Buffer
	ownedKillRaceMaterializer(server, &warnings).rollback(context.Background(), ledger)
	if warnings.Len() != 0 {
		t.Fatalf("rollback warned on a session a concurrent writer already removed: %q", warnings.String())
	}
	if server.session("vanishing") != nil || server.session("remaining") != nil {
		t.Fatalf("rollback did not continue to the rest of the ledger:\n%s", server.state())
	}
	if got := countTmuxVerb(server, "kill-session"); got != 1 {
		t.Fatalf("kill-session calls = %d, want only the remaining session's: %#v", got, server.calls)
	}
}

func TestRollbackStillRefusesAnOwnedSessionWhoseUIDDriftedBetweenReobserveAndGuard(t *testing.T) {
	server := newFakeTmux()
	owned := server.addSession("owned")
	owned.opts[tmuxopts.ProjectUIDSession] = "prj-owned"
	onNthOwnershipRead(server, owned.id, tmuxopts.ProjectUIDSession, 2, func(*fakeTmux) {
		owned.opts[tmuxopts.ProjectUIDSession] = "prj-someone-else"
	})
	ledger := &runtimeLedger{}
	ledger.record(runtimeSession, owned.id, "prj-owned")
	var warnings bytes.Buffer
	ownedKillRaceMaterializer(server, &warnings).rollback(context.Background(), ledger)
	if !strings.Contains(warnings.String(), `guard refused action "kill-owned" before first write`) ||
		!strings.Contains(warnings.String(), `ownership uid is "prj-someone-else", want "prj-owned"`) {
		t.Fatalf("warning = %q; want the drifted owner refused at the guard", warnings.String())
	}
	if server.session("owned") == nil || countTmuxVerb(server, "kill-session") != 0 {
		t.Fatalf("rollback killed a session that now carries another uid: %#v", server.calls)
	}
}

func retireRaceFixture(t *testing.T) (*fakeTmux, *fakeTmuxSession, *fakeTmuxPane) {
	t.Helper()
	server := newFakeTmux()
	session := server.addSession("retire")
	window := seedLiveWindow(t, server, session, "win-retire", "pan-keep")
	pane := newFakeTmuxPane(server.mint("%"))
	pane.opts[tmuxopts.PaneUID] = "pan-retire"
	window.panes = append(window.panes, pane)
	return server, session, pane
}

func TestRetireOwnedPaneTreatsAPaneRemovedBetweenReobserveAndGuardAsAbsent(t *testing.T) {
	server, _, pane := retireRaceFixture(t)
	// The Guard's ownership read is the first; the Pane is gone before it.
	onNthOwnershipRead(server, pane.id, tmuxopts.PaneUID, 1, func(f *fakeTmux) {
		for _, session := range f.sessions {
			for _, window := range session.windows {
				window.panes = slices.DeleteFunc(window.panes, func(p *fakeTmuxPane) bool { return p.id == pane.id })
			}
		}
	})
	if err := ownedKillRaceMaterializer(server, nil).retireOwnedPane(context.Background(), pane.id, "pan-retire"); err != nil {
		t.Fatalf("retireOwnedPane() error = %v; want the concurrently removed Pane treated as retired", err)
	}
	if got := countTmuxVerb(server, "kill-pane"); got != 0 {
		t.Fatalf("kill-pane calls = %d, want zero: %#v", got, server.calls)
	}
}

func TestRetireOwnedPaneStillRefusesAPaneWhoseUIDDriftedBetweenReobserveAndGuard(t *testing.T) {
	server, _, pane := retireRaceFixture(t)
	onNthOwnershipRead(server, pane.id, tmuxopts.PaneUID, 1, func(*fakeTmux) { pane.opts[tmuxopts.PaneUID] = "pan-someone-else" })
	err := ownedKillRaceMaterializer(server, nil).retireOwnedPane(context.Background(), pane.id, "pan-retire")
	if err == nil || !strings.Contains(err.Error(), `guard refused action "kill-owned" before first write`) ||
		!strings.Contains(err.Error(), `ownership uid is "pan-someone-else", want "pan-retire"`) {
		t.Fatalf("retireOwnedPane() error = %v; want the drifted owner refused at the guard", err)
	}
	if got := countTmuxVerb(server, "kill-pane"); got != 0 {
		t.Fatalf("kill-pane calls = %d, want zero: %#v", got, server.calls)
	}
}

// onLeaseReceiptRead runs change, with the fake's lock held, just before the
// Guard's `list-panes -s -t id` receipt read that follows the plan's
// Reobserve (`list-sessions -F #{session_id}`).
func onLeaseReceiptRead(server *fakeTmux, id string, change func(*fakeTmux)) {
	reobserved, done := false, false
	server.beforeDispatch = func(f *fakeTmux, args []string) {
		if len(args) == 0 || done {
			return
		}
		if args[0] == "list-sessions" && flagValue(args, "-F") == "#{session_id}" {
			reobserved = true
			return
		}
		if reobserved && args[0] == "list-panes" && slices.Contains(args, "-s") && flagValue(args, "-t") == id {
			done = true
			change(f)
		}
	}
}

func TestRecoverCreatedProjectByLeaseTreatsASessionRemovedBetweenReobserveAndGuardAsAbsent(t *testing.T) {
	server := newFakeTmux()
	created := server.addSession("leased")
	marker := "op-lease-raced-away"
	created.env[createOperationEnvironment] = marker
	onLeaseReceiptRead(server, created.id, func(f *fakeTmux) { removeFakeSession(f, created.id) })
	if err := ownedKillRaceMaterializer(server, nil).recoverCreatedProjectByLease(context.Background(), intmux.NewSessionResult{}, marker); err != nil {
		t.Fatalf("recoverCreatedProjectByLease() error = %v; want the concurrently removed session treated as absent", err)
	}
	if got := countTmuxVerb(server, "kill-session"); got != 0 {
		t.Fatalf("kill-session calls = %d, want zero: %#v", got, server.calls)
	}
}

func TestRecoverCreatedProjectByLeaseStillRefusesALeaseChangedBetweenReobserveAndGuard(t *testing.T) {
	server := newFakeTmux()
	created := server.addSession("leased")
	marker := "op-lease-changed"
	created.env[createOperationEnvironment] = marker
	onLeaseReceiptRead(server, created.id, func(*fakeTmux) { created.env[createOperationEnvironment] = "op-someone-else" })
	err := ownedKillRaceMaterializer(server, nil).recoverCreatedProjectByLease(context.Background(), intmux.NewSessionResult{}, marker)
	if err == nil || !strings.Contains(err.Error(), `guard refused action "kill-owned" before first write`) ||
		!strings.Contains(err.Error(), "created Project lease rollback containment is absent or changed") {
		t.Fatalf("recoverCreatedProjectByLease() error = %v; want the changed lease refused at the guard", err)
	}
	if server.session("leased") == nil || countTmuxVerb(server, "kill-session") != 0 {
		t.Fatalf("recovery killed a session whose lease changed: %#v", server.calls)
	}
}
