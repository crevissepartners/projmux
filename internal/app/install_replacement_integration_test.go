package app

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The isolated-fleet integration for the `L2` replacement guarantee.
//
// Every other test in this package drives the pass through injected seams. This
// one drives real processes: a real broker runtime published on a real Unix
// socket, a real atomic publication over the binary those processes are running,
// the real `internal install-replace` route, and the real `projmux doctor` row
// an operator reads afterwards. It is the only place the claim "an install
// finishes the replacement it starts" is observed rather than asserted.
//
// It runs in a fleet that is isolated three ways at once, because the contract
// this phase implements forbids demonstrating replacement anywhere else:
// `TMUX` and `TMUX_PANE` are dropped so no inherited client identity leaks in,
// `TMUX_TMPDIR` and a run-unique `-L` socket keep every tmux server under one
// owned root, and `HOME` plus the XDG variables move the state domain -- and
// with it the broker's whole discovery contract -- inside that root. The exact
// `#{socket_path}` is read back after creation and only a socket under the
// owned root is ever ended.
//
// It is gated on PMX_TEST_INSTALL_REPLACEMENT_BIN, which
// test/integration/install-replacement-drain.sh supplies.

const installReplacementIntegrationEnv = "PMX_TEST_INSTALL_REPLACEMENT_BIN"

// installReplacementFleet is one isolated fleet: a root, a published binary,
// and the environment every process in it runs under.
type installReplacementFleet struct {
	root   string
	binary string
	env    []string
	socket string
}

func newInstallReplacementFleet(t *testing.T, source string) *installReplacementFleet {
	t.Helper()
	// A short root. The broker's socket path is derived from the state domain
	// and the discovery contract refuses one that would exceed the platform
	// bound, which the test tree's own temp directory already does.
	root, err := os.MkdirTemp("", "pmxfleet")
	if err != nil {
		t.Fatalf("MkdirTemp() = %v", err)
	}
	fleet := &installReplacementFleet{
		root:   root,
		binary: filepath.Join(root, "bin", "projmux"),
		socket: "pmxfleet",
	}
	t.Cleanup(fleet.cleanup)

	for _, dir := range []string{"bin", "home", "state", "config", "cache", "data", "run", "tmux"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatalf("MkdirAll(%s) = %v", dir, err)
		}
	}
	fleet.publish(t, source)
	fleet.env = append(os.Environ(),
		"HOME="+filepath.Join(root, "home"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_RUNTIME_DIR="+filepath.Join(root, "run"),
		"TMUX_TMPDIR="+filepath.Join(root, "tmux"),
		"PROJMUX_INSTALLER=integration",
	)
	// The inherited client identity never crosses into the fleet.
	fleet.env = append(fleet.env, "TMUX=", "TMUX_PANE=")
	return fleet
}

// publish copies source over the fleet's binary the way `make install` does:
// into a temp name beside it, then a rename. The rename is what unlinks the
// image every already-running child is executing, which is the whole input to
// the vintage trigger.
func (f *installReplacementFleet) publish(t *testing.T, source string) {
	t.Helper()
	body, err := os.ReadFile(source) // #nosec G304 -- the path is this test's own build output.
	if err != nil {
		t.Fatalf("read %s: %v", source, err)
	}
	temp := f.binary + ".publishing"
	if err := os.WriteFile(temp, body, 0o700); err != nil { // #nosec G302 -- an executable this test runs.
		t.Fatalf("write %s: %v", temp, err)
	}
	if err := os.Rename(temp, f.binary); err != nil {
		t.Fatalf("rename %s: %v", temp, err)
	}
}

// run invokes the fleet's binary and returns its combined output.
func (f *installReplacementFleet) run(t *testing.T, extraEnv []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(f.binary, args...) // #nosec G204 -- the binary and args are this test's own.
	cmd.Env = append(append([]string(nil), f.env...), extraEnv...)
	cmd.Dir = f.root
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// fleetMember is one long-lived process of the fleet, with its own exit
// observed rather than polled.
//
// Waiting on the child is the only race-free way to see it go: a `kill(pid, 0)`
// probe answers "alive" for a child that has exited and not been reaped, so a
// poll would report a drained runtime as still running for as long as the test
// held its process slot.
type fleetMember struct {
	cmd *exec.Cmd
	// exit is closed once the child has been waited on, so every reader sees
	// the same fact and no reader consumes it from another.
	exit chan struct{}
}

// gone reports whether this member has exited, waiting up to budget for it.
func (m *fleetMember) gone(budget time.Duration) bool {
	select {
	case <-m.exit:
		return true
	case <-time.After(budget):
		return false
	}
}

// alive reports that this member has not exited yet.
func (m *fleetMember) alive() bool {
	select {
	case <-m.exit:
		return false
	default:
		return true
	}
}

// start launches one long-lived member of the fleet.
func (f *installReplacementFleet) start(t *testing.T, args ...string) *fleetMember {
	t.Helper()
	cmd := exec.Command(f.binary, args...) // #nosec G204 -- the binary and args are this test's own.
	cmd.Env = append([]string(nil), f.env...)
	cmd.Dir = f.root
	log, err := os.CreateTemp(f.root, "member-*.log")
	if err != nil {
		t.Fatalf("create member log: %v", err)
	}
	cmd.Stdout, cmd.Stderr = log, log
	t.Cleanup(func() { _ = log.Close() })
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %v: %v", args, err)
	}
	member := &fleetMember{cmd: cmd, exit: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(member.exit)
	}()
	t.Cleanup(func() {
		if t.Failed() {
			if body, readErr := os.ReadFile(log.Name()); readErr == nil && len(body) > 0 {
				t.Logf("%v said:\n%s", args, body)
			}
		}
	})
	t.Cleanup(func() {
		if member.alive() {
			_ = cmd.Process.Kill()
		}
		<-member.exit
	})
	return member
}

// tmux runs one tmux command inside the fleet's owned root.
func (f *installReplacementFleet) tmux(t *testing.T, args ...string) string {
	t.Helper()
	full := append([]string{"-L", f.socket}, args...)
	cmd := exec.Command("tmux", full...) // #nosec G204 -- the args are this test's own.
	cmd.Env = append([]string(nil), f.env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("tmux %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// cleanup ends exactly the tmux server this fleet created, and only when its
// own socket path proves to be under the owned root.
//
// A bare `tmux kill-server` and a run that trusts TMUX_TMPDIR alone are both
// refused by the repository's smoke contract, for the reason this fleet exists:
// a cleanup that cannot prove which server it is ending can end the operator's.
func (f *installReplacementFleet) cleanup() {
	root := filepath.Join(f.root, "tmux")
	entries, err := os.ReadDir(root)
	if err == nil {
		for _, entry := range entries {
			sockets, err := os.ReadDir(filepath.Join(root, entry.Name()))
			if err != nil {
				continue
			}
			for _, socket := range sockets {
				path := filepath.Join(root, entry.Name(), socket.Name())
				resolved, err := filepath.EvalSymlinks(path)
				if err != nil || !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
					continue
				}
				kill := exec.Command("tmux", "-S", resolved, "kill-server") // #nosec G204 -- proven to be under the owned root.
				kill.Env = append([]string(nil), f.env...)
				_ = kill.Run()
			}
		}
	}
	_ = os.RemoveAll(f.root)
}

// brokerSocket is the discovery socket the fleet's broker publishes.
func (f *installReplacementFleet) brokerSocket(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(f.root, "state", "projmux", "broker")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".sock") {
			return filepath.Join(dir, entry.Name())
		}
	}
	return ""
}

// replacementOutcome reads the record the last pass wrote.
func (f *installReplacementFleet) replacementOutcome(t *testing.T) installReplacementOutcome {
	t.Helper()
	outcome, ok := readInstallReplacementOutcome(
		filepath.Join(f.root, "state", "projmux", installReplacementFile))
	if !ok {
		t.Fatal("the install pass wrote no readable outcome record")
	}
	return outcome
}

// replacementRow reads the fleet's own `L2` row through the shipped command.
func (f *installReplacementFleet) replacementRow(t *testing.T, extraEnv []string) doctorReplacementRow {
	t.Helper()
	out, err := f.run(t, extraEnv, "doctor", "--section", "replacement", "--json")
	if err != nil {
		t.Fatalf("doctor --section replacement: %v\n%s", err, out)
	}
	var decoded struct {
		Replacement *doctorReplacementReport `json:"replacement"`
	}
	if err := json.Unmarshal([]byte(out), &decoded); err != nil {
		t.Fatalf("decode doctor JSON: %v\n%s", err, out)
	}
	if decoded.Replacement == nil {
		t.Fatalf("doctor produced no replacement table:\n%s", out)
	}
	for _, row := range decoded.Replacement.Rows {
		if row.Layer == doctorReplacementLayerProcesses {
			return row
		}
	}
	t.Fatalf("doctor produced no L2 row:\n%s", out)
	return doctorReplacementRow{}
}

func signalValue(row doctorReplacementRow, key string) string {
	for _, signal := range row.Signals {
		if signal.Key == key {
			return signal.Value
		}
	}
	return ""
}

func waitFor(t *testing.T, what string, budget time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		if ready() {
			return
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("timed out after %s waiting for %s", budget, what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// TestInstallReplacementDrainIntegration is the isolated-fleet demonstration.
func TestInstallReplacementDrainIntegration(t *testing.T) {
	source := os.Getenv(installReplacementIntegrationEnv)
	if strings.TrimSpace(source) == "" {
		t.Skipf("%s is unset; run through test/integration/install-replacement-drain.sh", installReplacementIntegrationEnv)
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Fatalf("tmux is required for the isolated fleet: %v", err)
	}

	t.Run("a superseded broker is replaced and the row turns to replaced", func(t *testing.T) {
		fleet := newInstallReplacementFleet(t, source)

		// A long-lived member of the fleet, running the image that is about to
		// be superseded. `--idle-timeout` is long so nothing but the drain can
		// explain its exit.
		broker := fleet.start(t, "internal", "codex-broker", "serve", "--idle-timeout", "10m")
		waitFor(t, "the broker runtime to publish", 15*time.Second, func() bool {
			return fleet.brokerSocket(t) != ""
		})
		socket := fleet.brokerSocket(t)

		// Before the publication the fleet is clean: a live child was observed
		// and it runs the installed image.
		before := fleet.replacementRow(t, nil)
		if before.Replacement != doctorReplacementReplaced || before.Reason != doctorReplacementReasonNoResidual {
			t.Fatalf("baseline L2 = %s/%s, want replaced/%s", before.Replacement, before.Reason, doctorReplacementReasonNoResidual)
		}

		// The install publishes. The broker keeps running the image that has
		// just been unlinked out from under it.
		fleet.publish(t, source)

		// Nothing may reach the runtime between here and the pass. The trigger
		// lives inside the runtime, so *any* client of the newly published
		// binary drains it -- including `projmux doctor`, whose broker reading
		// is a dial. That is the behavior this phase wanted and it makes an
		// observation taken here indistinguishable from the pass's own work, so
		// the pass is the only thing that speaks to the runtime next.
		out, err := fleet.run(t, nil, "internal", "install-replace")
		if err != nil {
			t.Fatalf("internal install-replace: %v\n%s", err, out)
		}

		outcome := fleet.replacementOutcome(t)
		if outcome.Outcome != installReplacementOutcomeComplete {
			t.Fatalf("pass outcome = %+v, want %s", outcome, installReplacementOutcomeComplete)
		}
		if outcome.Attempted != 1 || outcome.Drained != 1 {
			t.Fatalf("pass counted %d attempted / %d drained, want 1 / 1", outcome.Attempted, outcome.Drained)
		}
		if outcome.Refusal != "drain-required" {
			t.Fatalf("pass refusal = %q, want drain-required", outcome.Refusal)
		}

		// The runtime is actually gone, and it went by draining rather than by
		// anything this test or the pass did to it: nothing here ever signalled
		// it, and the exit below is the child's own.
		if !broker.gone(15 * time.Second) {
			t.Fatal("the superseded runtime was still running after the pass reported it drained")
		}
		if _, err := os.Lstat(socket); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the drained runtime left its socket behind: %v", err)
		}

		// The replacement's second half: a runtime starts again on the image the
		// install published. This is the route the contract's role table names
		// -- "a new broker starts on the installed image when a binding needs
		// one" -- and without it the fleet has no long-lived child at all, which
		// the `L2` row correctly reports as `no-observed-processes` rather than
		// as a completed replacement. An empty fleet is not evidence of one.
		replacement := fleet.start(t, "internal", "codex-broker", "serve", "--idle-timeout", "10m")
		waitFor(t, "the replacement runtime to publish", 15*time.Second, func() bool {
			return fleet.brokerSocket(t) != ""
		})
		if !replacement.alive() {
			t.Fatal("the replacement runtime did not survive its own publication")
		}

		// And the row an operator reads says the replacement completed.
		after := fleet.replacementRow(t, nil)
		if after.Replacement != doctorReplacementReplaced || after.Reason != doctorReplacementReasonNoResidual {
			t.Fatalf("L2 after the pass = %s/%s, want replaced/%s", after.Replacement, after.Reason, doctorReplacementReasonNoResidual)
		}
		if got := signalValue(after, doctorReplacementSignalPassOutcome); got != installReplacementOutcomeComplete {
			t.Fatalf("L2 replacement.outcome = %q, want %s", got, installReplacementOutcomeComplete)
		}
	})

	t.Run("an attached session survives and the cutoff reports it", func(t *testing.T) {
		fleet := newInstallReplacementFleet(t, source)

		// The operator's own attached session, inside the owned tmux root.
		fleet.tmux(t, "new-session", "-d", "-s", "fleet", "exec "+fleet.binary+" shell")
		socketPath := fleet.tmux(t, "display-message", "-p", "#{socket_path}")
		if !strings.HasPrefix(socketPath, filepath.Join(fleet.root, "tmux")+string(os.PathSeparator)) {
			t.Fatalf("the fleet's tmux socket escaped the owned root: %s", socketPath)
		}
		waitFor(t, "the attached session to appear in the census", 20*time.Second, func() bool {
			row := fleet.replacementRow(t, nil)
			return signalValue(row, doctorReplacementSignalRoleResidual(projmuxProcessRoleSessionClient)) != "" ||
				signalValue(row, doctorReplacementSignalProcessesObserved) != "0"
		})

		// The install publishes. The attached session is now residual, and it
		// is a role the policy table never ends.
		fleet.publish(t, source)
		// Ages are whole seconds, so give the residual process an age a
		// one-second cutoff can be past.
		time.Sleep(3 * time.Second)

		shortCutoff := []string{replacementCutoffEnv + "=1s"}
		out, err := fleet.run(t, shortCutoff, "internal", "install-replace")
		if err != nil {
			t.Fatalf("internal install-replace: %v\n%s", err, out)
		}
		outcome := fleet.replacementOutcome(t)
		if outcome.Outcome != installReplacementOutcomeNoTarget {
			t.Fatalf("pass over an attached session = %s, want %s", outcome.Outcome, installReplacementOutcomeNoTarget)
		}
		if outcome.Attempted != 0 {
			t.Fatalf("pass attempted %d drains against report-only roles, want 0", outcome.Attempted)
		}
		if outcome.Reported == 0 {
			t.Fatalf("pass reported %d residual processes, want at least one", outcome.Reported)
		}

		// The session is still there. This is the acceptance the architecture
		// decision exists for: a uniform cutoff would have ended the terminal
		// the install was typed into.
		fleet.tmux(t, "has-session", "-t", "fleet")
		row := fleet.replacementRow(t, shortCutoff)
		if got := signalValue(row, doctorReplacementSignalRoleResidual(projmuxProcessRoleSessionClient)); got == "" {
			t.Fatalf("L2 stopped naming the attached session: %+v", row.Signals)
		}

		// And the cutoff is reported rather than acted on: the row says the
		// replacement is not going to complete, names the bound it judged
		// against, and counts what is past it.
		if row.Replacement != doctorReplacementNotReplaced || row.Reason != doctorReplacementReasonCutoffReached {
			t.Fatalf("L2 under a one-second cutoff = %s/%s, want not-replaced/%s",
				row.Replacement, row.Reason, doctorReplacementReasonCutoffReached)
		}
		if got := signalValue(row, doctorReplacementSignalCutoffSeconds); got != "1" {
			t.Fatalf("L2 replacement.cutoff-seconds = %q, want 1", got)
		}
		if got := signalValue(row, doctorReplacementSignalBeyondCutoff); got == "" || got == "0" {
			t.Fatalf("L2 replacement.beyond-cutoff = %q, want a positive count", got)
		}
		// Restoration stays reachable: reaching the cutoff ends the claim, not
		// the process, so the route that would replace it is still open.
		if row.Restoration != doctorRestorationRestorable {
			t.Fatalf("L2 restoration at the cutoff = %s, want %s", row.Restoration, doctorRestorationRestorable)
		}
	})
}
