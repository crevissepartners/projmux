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
}

func (s *serverEndingRollbackSeam) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	if s.ended {
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
// raced into. Any other failure of that read still stops the plan, and a later
// step still runs, and fails, on the server the kill ended.
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
			// its kill-window reaches the server the session kill ended. That
			// kill itself fails on a lost server, and nothing this rollback
			// read proves its absence, so the plan stops as it always has.
			name: "a later step still runs on the ended server and stops the plan",
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
				if seam.session("owned") != nil {
					t.Fatalf("rollback left the session it created:\n%s", seam.state())
				}
				if countTmuxVerb(seam.fakeTmux, "kill-window") != 1 {
					t.Fatalf("the step after the server-ending kill was not attempted: %#v", seam.calls)
				}
				if !strings.Contains(warnings, "rollback stopped before an unguarded runtime write: exit status 1: no server running on "+seam.socketPath) {
					t.Fatalf("warning = %q, want the later kill's own lost-server failure", warnings)
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
