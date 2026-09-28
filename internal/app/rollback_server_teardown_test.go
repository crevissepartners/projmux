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

// rollbackTeardownFailure is appTypedCommandFailure printed the way a real
// tmux exit prints, so a stopped plan's warning carries tmux's own words.
type rollbackTeardownFailure struct{ appTypedCommandFailure }

func (e rollbackTeardownFailure) Error() string { return "exit status 1: " + e.failure.Stderr }

// serverEndingRollbackSeam is the rollback seam plus the one thing real tmux
// does that it does not: a successful kill-session of the server's last
// session ends the server, and every later command, the route read included,
// gets afterEnd instead of an answer.
type serverEndingRollbackSeam struct {
	*rollbackTmuxSeam
	afterEnd error
	ended    bool
	// externalEndAtKill, when positive, ends the server from outside this
	// rollback just before its Nth kill command reaches tmux, so that kill
	// and everything after it meet a server no kill of this plan ended.
	externalEndAtKill int
	kills             int
	// killsAfterEnd counts the kill commands sent to the ended server.
	killsAfterEnd int
	// staleReadsAfterEnd answers that many reads after the end as the server
	// would while it is still exiting, so they see no teardown yet.
	staleReadsAfterEnd int
}

func (s *serverEndingRollbackSeam) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	kill := slices.ContainsFunc(args, func(arg string) bool { return strings.HasPrefix(arg, "kill-") })
	if kill && !s.ended {
		s.kills++
		if s.kills == s.externalEndAtKill {
			s.ended = true
		}
	}
	if s.ended && !kill && s.staleReadsAfterEnd > 0 {
		s.staleReadsAfterEnd--
		return s.rollbackTmuxSeam.Run(ctx, name, args...)
	}
	if s.ended {
		if kill {
			s.killsAfterEnd++
		}
		s.fakeTmux.calls = append(s.fakeTmux.calls, append([]string(nil), args...))
		return nil, s.afterEnd
	}
	out, err := s.rollbackTmuxSeam.Run(ctx, name, args...)
	if err == nil && slices.Contains(args, "kill-session") {
		s.mu.Lock()
		s.ended = len(s.sessions) == 0
		s.mu.Unlock()
	}
	return out, err
}

// TestRollbackTreatsTheServerItsOwnKillEndedAsAbsent pins the last-session
// rollback verdict. Killing the server's last session ends the server, so the
// kill step's post-write route read gets a teardown response instead of a
// listing. When that step's own kill succeeded, the ended server is the
// absence the step wanted, whichever of tmux's two teardown responses the read
// raced into, and every later step's target ended with that server: the plan
// sends that socket no further kill and does not stop. A later kill that meets
// the teardown after an own kill converges on the same proof, read again. Any
// other failure of that read still stops the plan, and so does a server that
// ended before any kill of this plan succeeded.
func TestRollbackTreatsTheServerItsOwnKillEndedAsAbsent(t *testing.T) {
	typed := func(kind inttmux.CommandFailureKind, stderr string) error {
		return rollbackTeardownFailure{appTypedCommandFailure{failure: inttmux.CommandFailure{Kind: kind, Stderr: stderr}}}
	}
	for _, tt := range []struct {
		name string
		// afterEnd answers every command once the last session is killed.
		afterEnd func(socket string) error
		// arrange seeds the server and returns this operation's ledger.
		arrange func(t *testing.T, seam *serverEndingRollbackSeam) *runtimeLedger
		check   func(t *testing.T, seam *serverEndingRollbackSeam, warnings string)
	}{
		{
			name:     "teardown in progress",
			afterEnd: func(string) error { return typed(inttmux.CommandFailureExit, "server exited unexpectedly") },
			arrange:  lastOwnedSessionLedger,
			check:    wantServerEndedWithoutStop,
		},
		{
			name:     "teardown finished",
			afterEnd: func(socket string) error { return typed(inttmux.CommandFailureExit, "no server running on "+socket) },
			arrange:  lastOwnedSessionLedger,
			check:    wantServerEndedWithoutStop,
		},
		{
			name: "two owned sessions, the second kill ends the server",
			afterEnd: func(socket string) error {
				return typed(inttmux.CommandFailureExit, "no server running on "+socket)
			},
			arrange: func(t *testing.T, seam *serverEndingRollbackSeam) *runtimeLedger {
				first := seam.addSession("first")
				first.opts[tmuxopts.ProjectUIDSession] = "prj-first"
				second := seam.addSession("second")
				second.opts[tmuxopts.ProjectUIDSession] = "prj-second"
				ledger := &runtimeLedger{}
				ledger.record(runtimeSession, first.id, "prj-first")
				ledger.record(runtimeSession, second.id, "prj-second")
				return ledger
			},
			check: wantServerEndedWithoutStop,
		},
		{
			// The ledger's Window is killed after the session holding it, so
			// its step comes after the kill that ended the server. The Window
			// ended with that server, so the step converges without a kill.
			name: "a later step converges on the server the plan's own kill ended",
			afterEnd: func(socket string) error {
				return typed(inttmux.CommandFailureExit, "no server running on "+socket)
			},
			arrange: func(t *testing.T, seam *serverEndingRollbackSeam) *runtimeLedger {
				session := seam.addSession("owned")
				session.opts[tmuxopts.ProjectUIDSession] = "prj-owned"
				window := seedLiveWindow(t, seam.fakeTmux, session, "win-owned", "pan-owned")
				ledger := &runtimeLedger{}
				ledger.record(runtimeWindow, window.id, "win-owned")
				ledger.record(runtimeSession, session.id, "prj-owned")
				return ledger
			},
			check: func(t *testing.T, seam *serverEndingRollbackSeam, warnings string) {
				wantServerEndedWithoutStop(t, seam, warnings)
				if countTmuxVerb(seam.fakeTmux, "kill-window") != 0 {
					t.Fatalf("the step after the server-ending kill sent a kill-window: %#v", seam.calls)
				}
			},
		},
		{
			// The read after the session kill still reaches the exiting
			// server, so only the Window's kill meets the teardown. The
			// proof, read again there, converges the step.
			name: "a later kill that meets the teardown the confirming read missed converges",
			afterEnd: func(socket string) error {
				return typed(inttmux.CommandFailureExit, "no server running on "+socket)
			},
			arrange: func(t *testing.T, seam *serverEndingRollbackSeam) *runtimeLedger {
				seam.staleReadsAfterEnd = 1
				session := seam.addSession("owned")
				session.opts[tmuxopts.ProjectUIDSession] = "prj-owned"
				window := seedLiveWindow(t, seam.fakeTmux, session, "win-owned", "pan-owned")
				ledger := &runtimeLedger{}
				ledger.record(runtimeWindow, window.id, "win-owned")
				ledger.record(runtimeSession, session.id, "prj-owned")
				return ledger
			},
			check: func(t *testing.T, seam *serverEndingRollbackSeam, warnings string) {
				if seam.staleReadsAfterEnd != 0 {
					t.Fatalf("the confirming read did not race the ending server: %#v", seam.calls)
				}
				if strings.Contains(warnings, "rollback stopped") {
					t.Fatalf("rollback stopped on the server its own kill ended: %q", warnings)
				}
				if countTmuxVerb(seam.fakeTmux, "kill-window") != 1 || seam.killsAfterEnd != 1 {
					t.Fatalf("kill-window calls = %d, kills after end = %d, want the one kill that met the teardown: %#v",
						countTmuxVerb(seam.fakeTmux, "kill-window"), seam.killsAfterEnd, seam.calls)
				}
			},
		},
		{
			name: "a server that ended before any kill of the plan stops it",
			afterEnd: func(socket string) error {
				return typed(inttmux.CommandFailureExit, "no server running on "+socket)
			},
			arrange: func(t *testing.T, seam *serverEndingRollbackSeam) *runtimeLedger {
				seam.externalEndAtKill = 1
				return lastOwnedSessionLedger(t, seam)
			},
			check: wantStoppedOnExternalEnd,
		},
		{
			// The first kill leaves the server alive with the second session,
			// and the server is gone before the second kill. An own kill has
			// succeeded, and every ledger object ended with that server, so
			// the second step converges on the proof read at its kill.
			name: "a server gone after an own kill left it alive converges",
			afterEnd: func(socket string) error {
				return typed(inttmux.CommandFailureExit, "no server running on "+socket)
			},
			arrange: func(t *testing.T, seam *serverEndingRollbackSeam) *runtimeLedger {
				seam.externalEndAtKill = 2
				first := seam.addSession("first")
				first.opts[tmuxopts.ProjectUIDSession] = "prj-first"
				second := seam.addSession("second")
				second.opts[tmuxopts.ProjectUIDSession] = "prj-second"
				ledger := &runtimeLedger{}
				ledger.record(runtimeSession, first.id, "prj-first")
				ledger.record(runtimeSession, second.id, "prj-second")
				return ledger
			},
			check: func(t *testing.T, seam *serverEndingRollbackSeam, warnings string) {
				if strings.Contains(warnings, "rollback stopped") {
					t.Fatalf("rollback stopped after an own kill on a server that is gone: %q", warnings)
				}
			},
		},
		{
			name:     "a different typed exit on the route read still stops the plan",
			afterEnd: func(string) error { return typed(inttmux.CommandFailureExit, "access not allowed") },
			arrange:  lastOwnedSessionLedger,
			check:    wantStoppedAfterKill("access not allowed"),
		},
		{
			name:     "a teardown text on a non-exit failure still stops the plan",
			afterEnd: func(string) error { return typed(inttmux.CommandFailureRunner, "server exited unexpectedly") },
			arrange:  lastOwnedSessionLedger,
			check:    wantStoppedAfterKill("server exited unexpectedly"),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			seam := &serverEndingRollbackSeam{rollbackTmuxSeam: &rollbackTmuxSeam{fakeTmux: newFakeTmux()}}
			seam.afterEnd = tt.afterEnd(seam.socketPath)
			var warnings bytes.Buffer
			runtime := &materializer{
				runner: seam, mirror: intmetadata.NewMirror(seam), warn: &warnings,
				target:             tmuxTransport{Kind: tmuxSocketPath, Value: seam.socketPath, Source: tmuxSocketPathSource},
				expectedSocketPath: seam.socketPath,
				socketName:         defaultAppSocket,
				routeAuthority:     &runtimeMutationRouteAuthority{Class: runtimeMutationRouteApp, ServerPID: seam.serverPID},
			}
			ledger := tt.arrange(t, seam)
			runtime.rollback(context.Background(), ledger)
			if !seam.ended {
				t.Fatalf("no kill ended the server: %#v", seam.calls)
			}
			tt.check(t, seam, warnings.String())
		})
	}
}

func lastOwnedSessionLedger(t *testing.T, seam *serverEndingRollbackSeam) *runtimeLedger {
	session := seam.addSession("owned")
	session.opts[tmuxopts.ProjectUIDSession] = "prj-owned"
	ledger := &runtimeLedger{}
	ledger.record(runtimeSession, session.id, "prj-owned")
	return ledger
}

func wantServerEndedWithoutStop(t *testing.T, seam *serverEndingRollbackSeam, warnings string) {
	if len(seam.sessions) != 0 {
		t.Fatalf("rollback left a session it created:\n%s", seam.state())
	}
	if strings.Contains(warnings, "rollback stopped") {
		t.Fatalf("rollback stopped on the server its own kill ended: %q", warnings)
	}
	if seam.killsAfterEnd != 0 {
		t.Fatalf("rollback sent %d kill(s) to the server its own kill ended: %#v", seam.killsAfterEnd, seam.calls)
	}
}

// wantStoppedOnExternalEnd is the unchanged verdict for a server no kill of
// this plan ended: the kill that meets it fails, and the plan stops with that
// failure.
func wantStoppedOnExternalEnd(t *testing.T, seam *serverEndingRollbackSeam, warnings string) {
	if !strings.Contains(warnings, "rollback stopped before an unguarded runtime write: exit status 1: no server running on "+seam.socketPath) {
		t.Fatalf("warning = %q, want the kill's own lost-server failure", warnings)
	}
}

func wantStoppedAfterKill(stderr string) func(*testing.T, *serverEndingRollbackSeam, string) {
	return func(t *testing.T, seam *serverEndingRollbackSeam, warnings string) {
		if countTmuxVerb(seam.fakeTmux, "kill-session") != 1 {
			t.Fatalf("kill-session calls = %d, want the one owned kill: %#v", countTmuxVerb(seam.fakeTmux, "kill-session"), seam.calls)
		}
		if !strings.Contains(warnings, "rollback stopped before an unguarded runtime write: runtime mutation plan: expected effects were not fully observed") ||
			!strings.Contains(warnings, stderr) {
			t.Fatalf("warning = %q, want the plan stopped on the unexplained route failure %q", warnings, stderr)
		}
	}
}
