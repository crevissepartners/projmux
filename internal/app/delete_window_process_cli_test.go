package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
)

// These tests run the copied product binary against a real isolated tmux
// server and a stub provider. They prove the process, not a probe, is gone.

type windowDeleteOwner struct {
	cmd         *exec.Cmd
	done        chan error
	agent, pane string
}

func windowDeleteRun(t *testing.T, f processCreateCLI, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, f.binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	rc := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		rc = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return stdout.String() + stderr.String(), rc
}

func windowDeleteStartOwner(t *testing.T, f processCreateCLI, args ...string) *windowDeleteOwner {
	t.Helper()
	cmd := exec.Command(f.binary, args...)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	o := &windowDeleteOwner{cmd: cmd, done: make(chan error, 1)}
	go func() { o.done <- cmd.Wait() }()
	t.Cleanup(func() { _ = in.Close(); _ = cmd.Process.Kill() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || !strings.Contains(line, "runtime=process foreground=owned") {
		t.Fatalf("ownership %q: %v %s", line, err, stderr.String())
	}
	fields := strings.Fields(line)
	o.agent, o.pane = strings.TrimPrefix(fields[1], "uid:"), strings.TrimPrefix(fields[3], "uid:")
	return o
}

func windowDeleteChild(t *testing.T, f processCreateCLI, pane string) coremetadata.ProcessIdentity {
	t.Helper()
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	p, ok := reg.Pane(pane)
	if !ok || p.Status.Activation.Process == nil {
		t.Fatalf("Pane %s has no process activation", pane)
	}
	return p.Status.Activation.Process.Child
}

func windowDeleteFixture(t *testing.T) (processCreateCLI, string) {
	t.Helper()
	copiedCLI(t, "PMX_TEST_CLI")
	f, _, _, socket := hostMoveCLIFixture(t)
	reg, _ := f.store.LoadReadOnly()
	w, _ := reg.Window(f.window)
	// A second tmux Window keeps the server alive after the delete.
	if out, err := exec.Command("tmux", "-S", socket, "new-window", "-d", "-t", w.Status.RuntimeSessionID, "-n", "keeper", "sleep 300").CombinedOutput(); err != nil {
		t.Fatalf("keeper: %v %s", err, out)
	}
	return f, socket
}

func requireProviderGone(t *testing.T, label string, child coremetadata.ProcessIdentity, owner *exec.Cmd, done <-chan error) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for processBirthAlive(child) {
		select {
		case <-deadline:
			t.Fatalf("%s: provider child pid %d is still alive after the delete", label, child.PID)
		case <-time.After(50 * time.Millisecond):
		}
	}
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatalf("%s: owner host pid %d did not exit after the delete", label, owner.Process.Pid)
	}
}

// A tmux Window and a process Window in one plan: the process provider and its
// owner host are gone afterwards, and the receipt says runtime=stopped.
func TestDeleteWindowCLIStopsProcessProviderOfMixedWindows(t *testing.T) {
	f, socket := windowDeleteFixture(t)
	o := windowDeleteStartOwner(t, f, "create", "agent", "--host", "process", "--project", "uid:"+f.project, "--create-window", "--window", "pw", "--provider", "claude", "--profile", "none", "--name", "pwagent", "--", "hold")
	reg, _ := f.store.LoadReadOnly()
	agent, _ := reg.Agent(o.agent)
	processWindow := agent.Metadata.OwnerUID()
	child := windowDeleteChild(t, f, o.pane)
	out, rc := windowDeleteRun(t, f, "delete", "window", "uid:"+f.window, "uid:"+processWindow, "--socket-path", socket, "--yes")
	if rc != 0 || !strings.Contains(out, "runtime=stopped") || !strings.Contains(out, "stopped through its owner host") {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	requireProviderGone(t, "mixed Windows", child, o.cmd, o.done)
	after, _ := f.store.LoadReadOnly()
	if _, ok := after.Agent(o.agent); ok {
		t.Fatal("process Agent survived")
	}
	if _, ok := after.Pane(o.pane); ok {
		t.Fatal("process Pane survived")
	}
}

// A tmux Window that holds a process Pane: same result.
func TestDeleteWindowCLIStopsProcessProviderBelowTmuxWindow(t *testing.T) {
	f, agent, _, socket := func() (processCreateCLI, coremetadata.Agent, string, string) {
		copiedCLI(t, "PMX_TEST_CLI")
		return hostMoveCLIFixture(t)
	}()
	reg, _ := f.store.LoadReadOnly()
	w, _ := reg.Window(f.window)
	if out, err := exec.Command("tmux", "-S", socket, "new-window", "-d", "-t", w.Status.RuntimeSessionID, "-n", "keeper", "sleep 300").CombinedOutput(); err != nil {
		t.Fatalf("keeper: %v %s", err, out)
	}
	run, moved := startProcessRelaunchCLI(t, context.Background(), f, "uid:"+agent.Metadata.UID, "--host", "process", "--socket-path", socket, "--yes", "--", "continue")
	t.Cleanup(func() { _ = run.input.Close() })
	done := make(chan error, 1)
	go func() { done <- run.cmd.Wait() }()
	child := windowDeleteChild(t, f, moved.NewPaneUID)
	if !processBirthAlive(child) {
		t.Fatal("provider child is not running before the delete")
	}
	out, rc := windowDeleteRun(t, f, "delete", "window", "uid:"+f.window, "--socket-path", socket, "--yes")
	if rc != 0 || !strings.Contains(out, "runtime=stopped") {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	requireProviderGone(t, "tmux Window with process Pane", child, run.cmd, done)
	after, _ := f.store.LoadReadOnly()
	if _, ok := after.Pane(moved.NewPaneUID); ok {
		t.Fatal("process Pane survived")
	}
}

// An owner host that was killed took its child with it: nothing is left to
// stop, so the Window delete is Registry-only for that Agent and succeeds.
func TestDeleteWindowCLIUnknownProcessAgentIsRegistryOnly(t *testing.T) {
	copiedCLI(t, "PMX_TEST_CLI")
	f, agent, _, socket := hostMoveCLIFixture(t)
	reg, _ := f.store.LoadReadOnly()
	w, _ := reg.Window(f.window)
	if out, err := exec.Command("tmux", "-S", socket, "new-window", "-d", "-t", w.Status.RuntimeSessionID, "-n", "keeper", "sleep 300").CombinedOutput(); err != nil {
		t.Fatalf("keeper: %v %s", err, out)
	}
	run, moved := startProcessRelaunchCLI(t, context.Background(), f, "uid:"+agent.Metadata.UID, "--host", "process", "--socket-path", socket, "--yes", "--", "continue")
	t.Cleanup(func() { _ = run.input.Close() })
	child := windowDeleteChild(t, f, moved.NewPaneUID)
	_ = run.cmd.Process.Kill()
	for deadline := time.Now().Add(20 * time.Second); processBirthAlive(child); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("provider child pid %d outlived its killed owner after 20s; the unknown shape precondition failed", child.PID)
		}
	}
	out, rc := windowDeleteRun(t, f, "delete", "window", "uid:"+f.window, "--socket-path", socket, "--yes")
	if rc != 0 || !strings.Contains(out, "registry-only") || !strings.Contains(out, "runtime=unknown evidence=owner-host-and-child-absent") {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	after, _ := f.store.LoadReadOnly()
	if _, ok := after.Pane(moved.NewPaneUID); ok {
		t.Fatal("process Pane survived")
	}
}
