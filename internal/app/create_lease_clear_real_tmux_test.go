package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestOwnerCheckedLeaseClearArgvThroughRealTmux runs the argv
// runtimeMutationArgv assembles for an owner-checked clear against an isolated
// real tmux server. tmux itself must evaluate the lease comparison and parse
// the quoted $N inside the if-shell body: our marker goes, another operation's
// marker stays, and an absent variable is a successful no-op.
func TestOwnerCheckedLeaseClearArgvThroughRealTmux(t *testing.T) {
	requireRealTmux(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// A unix socket path must stay short: use the /tmp root, never $TMPDIR.
	root, err := filepath.EvalSymlinks(shortTempDomain(t))
	if err != nil {
		t.Fatal(err)
	}
	environment := []string{"TMUX_TMPDIR=" + root}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if key == "TMUX" || key == "TMUX_PANE" || key == "TMUX_TMPDIR" || key == runtimeMutationAnchorPaneEnv {
			continue
		}
		environment = append(environment, entry)
	}
	socket := filepath.Join(root, "s")
	tmux := func(args ...string) (string, error) {
		command := exec.CommandContext(ctx, "tmux", append([]string{"-S", socket, "-f", "/dev/null"}, args...)...)
		command.Env = environment
		out, err := command.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	receipt, err := tmux("new-session", "-d", "-s", "lease-clear", "-P", "-F", "#{session_id}\t#{pid}", "tail", "-f", "/dev/null")
	if err != nil {
		t.Fatalf("start isolated tmux: %v: %s", err, receipt)
	}
	sessionID, serverPID, _ := strings.Cut(receipt, "\t")
	// Registered after the root removal above, so LIFO kills the server first,
	// while its socket still exists.
	killRealTmuxServerOnCleanup(t, environment, socket, realTmuxServerPID(t, serverPID))

	clear := func(variable string) {
		t.Helper()
		action := ownerCheckedLeaseClear(variable)
		action.Target.ID, action.Target.UID, action.Operands[2] = sessionID, "session:"+sessionID, sessionID
		argv, err := runtimeMutationArgv(action)
		if err != nil {
			t.Fatalf("assemble clear of %s: %v", variable, err)
		}
		if out, err := tmux(argv...); err != nil {
			t.Fatalf("run %q: %v: %s", argv, err, out)
		}
	}
	lease := func(variable string) string {
		t.Helper()
		out, err := tmux("show-environment", "-t", sessionID)
		if err != nil {
			t.Fatalf("read session environment: %v: %s", err, out)
		}
		return sessionEnvironmentValue(out+"\n", variable)
	}
	for variable, value := range map[string]string{createOperationEnvironment: leaseClearOtherMarker, finalizeOperationEnvironment: leaseClearOurMarker} {
		if out, err := tmux("set-environment", "-t", sessionID, variable, value); err != nil {
			t.Fatalf("seed %s: %v: %s", variable, err, out)
		}
	}

	clear(createOperationEnvironment)
	clear(finalizeOperationEnvironment)
	if got := lease(createOperationEnvironment); got != leaseClearOtherMarker {
		t.Fatalf("create lease = %q, want the other operation's marker kept", got)
	}
	if got := lease(finalizeOperationEnvironment); got != "" {
		t.Fatalf("finalize lease = %q, want our marker removed", got)
	}
	// Absent now: a repeat is a no-op, not an error.
	clear(finalizeOperationEnvironment)
}
