//go:build linux

package tmux

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/lifecycle"
)

// TestListEphemeralSessionsReadsNeverAttachedRealTmuxSessions lists sessions
// of an isolated real tmux server that no client has attached to. tmux prints
// an empty session_last_attached for them, and the last row, whose session
// has no ephemeral option, ends in two empty fields.
func TestListEphemeralSessionsReadsNeverAttachedRealTmuxSessions(t *testing.T) {
	requireIsolatedTmuxSmoke(t, "PROJMUX_EPHEMERAL_SESSIONS_SMOKE")
	smokeRoot := isolatedTmuxSmokeRoot(t, "pmx-ephem-")
	socket := "projmux-ephemeral-" + filepath.Base(smokeRoot)
	ctx := context.Background()
	runner := resourceSmokeRunner{socket: socket, tmuxTmpDir: smokeRoot, configFile: "/dev/null"}
	// list-sessions orders rows by name, so the session without the
	// ephemeral option is the last row.
	if output, err := runner.Run(ctx, "tmux", "new-session", "-d", "-s", "never-a", "/usr/bin/sleep 60"); err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, output)
	}
	t.Cleanup(func() { cleanupIsolatedResourceSmoke(t, runner, smokeRoot) })
	if output, err := runner.Run(ctx, "tmux", "set-option", "-t", "never-a", "@projmux_ephemeral", "1"); err != nil {
		t.Fatalf("mark never-a ephemeral: %v: %s", err, output)
	}
	if output, err := runner.Run(ctx, "tmux", "new-session", "-d", "-s", "never-b", "/usr/bin/sleep 60"); err != nil {
		t.Fatalf("create never-b: %v: %s", err, output)
	}

	raw, err := runner.Run(ctx, "tmux", "list-sessions", "-F", "#{session_name}\t#{session_attached}\t#{session_last_attached}\t#{@projmux_ephemeral}")
	if err != nil {
		t.Fatalf("list sessions: %v: %s", err, raw)
	}
	if want := "never-a\t0\t\t1\nnever-b\t0\t\t\n"; string(raw) != want {
		t.Fatalf("tmux list-sessions printed %q, want %q", raw, want)
	}

	got, err := NewClient(runner).ListEphemeralSessions(ctx)
	if err != nil {
		t.Fatalf("ListEphemeralSessions() error = %v", err)
	}
	want := []lifecycle.SessionInventory{
		{Name: "never-a", Ephemeral: true},
		{Name: "never-b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListEphemeralSessions() = %#v, want %#v", got, want)
	}
}

func TestEphemeralSessionsSmokeSkipsOnlyThroughTheGate(t *testing.T) {
	auditIsolatedTmuxSmokeGate(t, "ephemeral_sessions_smoke_linux_test.go", map[string]string{
		"TestListEphemeralSessionsReadsNeverAttachedRealTmuxSessions": "PROJMUX_EPHEMERAL_SESSIONS_SMOKE",
	})
}
