package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
)

// processDeleteCLI is a copied product binary with an isolated HOME, no TMUX,
// an isolated TMUX_TMPDIR, and a recording tmux shim first on PATH: any tmux
// call a process Agent delete made would land in tmuxCalls.
type processDeleteCLI struct {
	processCreateCLI
	ownerArgs           []string
	tmuxShim, tmuxCalls string
}

// newProcessDeleteCLI builds the fixture for one provider. The recording tmux
// shim is put on PATH only for delete invocations, so an owner's own fixture
// reads keep their provider harness unchanged.
func newProcessDeleteCLI(t *testing.T, provider string) processDeleteCLI {
	t.Helper()
	if os.Getenv("PMX_TEST_CLI") == "" {
		t.Skip("set PMX_TEST_CLI to a copied product binary")
	}
	var f processCreateCLI
	var ownerArgs []string
	if provider == aiModeCodex {
		codex := newProcessCodexCreateCLI(t)
		f, ownerArgs = codex.processCreateCLI, codex.args()
	} else {
		f = newProcessCreateCLI(t)
		ownerArgs = f.args()
	}
	shim := filepath.Join(f.root, "tmux-shim")
	if err := os.MkdirAll(shim, 0o700); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(f.root, "tmux-calls")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + calls + "\nexit 1\n"
	if err := os.WriteFile(filepath.Join(shim, "tmux"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	// tmux silently falls back to /tmp/tmux-UID when TMUX_TMPDIR does not
	// exist, so the isolated directory must exist before anything inherits it.
	tmuxTmp := filepath.Join(f.root, "tmux-tmp")
	if err := os.MkdirAll(tmuxTmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX_TMPDIR", tmuxTmp)
	return processDeleteCLI{processCreateCLI: f, ownerArgs: ownerArgs, tmuxShim: shim, tmuxCalls: calls}
}

// processDeleteOwner is a foreground `create agent --host process` owner in
// its own process, the shape a process host owning a session has.
type processDeleteOwner struct {
	cmd    *exec.Cmd
	input  interface{ Close() error }
	stderr *bytes.Buffer
	agent  string
	pane   string
	waited bool
}

func (f processDeleteCLI) startOwner(t *testing.T, ctx context.Context) *processDeleteOwner {
	t.Helper()
	cmd := exec.CommandContext(ctx, f.binary, f.ownerArgs...)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	owner := &processDeleteOwner{cmd: cmd, input: input, stderr: &bytes.Buffer{}}
	cmd.Stderr = owner.stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		if !owner.waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || !strings.Contains(line, "runtime=process foreground=owned") {
		t.Fatalf("ownership %q: %v %s", line, err, owner.stderr.String())
	}
	fields := strings.Fields(line)
	owner.agent, owner.pane = strings.TrimPrefix(fields[1], "uid:"), strings.TrimPrefix(fields[3], "uid:")
	return owner
}

func (o *processDeleteOwner) wait() error {
	err := o.cmd.Wait()
	o.waited = true
	return err
}

func (f processDeleteCLI) run(t *testing.T, ctx context.Context, args ...string) (string, string, int) {
	t.Helper()
	if err := os.WriteFile(f.tmuxCalls, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, f.binary, args...)
	cmd.Env = append(os.Environ(), "PATH="+f.tmuxShim+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if calls, _ := os.ReadFile(f.tmuxCalls); len(calls) != 0 {
		t.Fatalf("process Agent delete invoked tmux: %q", calls)
	}
	return stdout.String(), stderr.String(), code
}

func (f processDeleteCLI) registry(t *testing.T) coremetadata.Registry {
	t.Helper()
	reg, err := f.store.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func (f processDeleteCLI) requireDeleted(t *testing.T, owner *processDeleteOwner) {
	t.Helper()
	reg := f.registry(t)
	if _, ok := reg.Agent(owner.agent); ok {
		t.Fatalf("Agent %s survived delete", owner.agent)
	}
	if _, ok := reg.Pane(owner.pane); ok {
		t.Fatalf("Pane %s survived delete", owner.pane)
	}
}

func (f processDeleteCLI) requireKept(t *testing.T, owner *processDeleteOwner) {
	t.Helper()
	reg := f.registry(t)
	if _, ok := reg.Agent(owner.agent); !ok {
		t.Fatalf("Agent %s was deleted by a refusal or dry run", owner.agent)
	}
}

// An Offline process Agent deletes with no TMUX, both without a socket flag
// and with `--socket projmux`, and never calls tmux.
func TestProcessDeleteOfflineAgentActualCLI(t *testing.T) {
	for _, socket := range [][]string{nil, {"--socket", "projmux"}} {
		t.Run(strings.Join(append([]string{"socket"}, socket...), "-"), func(t *testing.T) {
			f := newProcessDeleteCLI(t, aiModeClaude)
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			owner := f.startOwner(t, ctx)
			_ = owner.input.Close()
			if err := owner.wait(); err != nil {
				t.Fatalf("owner EOF: %v %s", err, owner.stderr.String())
			}
			ref := "uid:" + owner.agent

			_, stderr, code := f.run(t, ctx, append([]string{"delete", "agent", "owned", "--project", "uid:" + f.project, "--window", "uid:" + f.window, "--yes"}, socket...)...)
			if code != 1 || !strings.Contains(stderr, "process-delete-refused: ") || !strings.Contains(stderr, "projmux delete agent "+ref) {
				t.Fatalf("name selector: exit=%d stderr=%q", code, stderr)
			}
			f.requireKept(t, owner)

			stdout, stderr, code := f.run(t, ctx, append([]string{"delete", "agent", ref, "--dry-run"}, socket...)...)
			if code != 0 || !strings.Contains(stdout, "would delete") || !strings.Contains(stdout, "runtime=offline evidence=wait:") || !strings.Contains(stdout, "dry-run: nothing was deleted") {
				t.Fatalf("dry-run: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			f.requireKept(t, owner)

			stdout, stderr, code = f.run(t, ctx, append([]string{"delete", "agent", ref, "--yes"}, socket...)...)
			if code != 0 || !strings.Contains(stdout, "registry-only deleted this Agent") || !strings.Contains(stdout, "runtime=offline") {
				t.Fatalf("delete: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			f.requireDeleted(t, owner)
		})
	}
}

// After the owner is SIGKILLed no Wait is recorded; once the supervisor has
// ended the child, the Agent is unknown and its records are cleaned up.
func TestProcessDeleteUnknownAgentActualCLI(t *testing.T) {
	f := newProcessDeleteCLI(t, aiModeClaude)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	owner := f.startOwner(t, ctx)
	before := f.registry(t)
	pane, _ := before.Pane(owner.pane)
	birth := pane.Status.Activation.Process.Child
	if err := owner.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = owner.wait()
	processCLIUntil(t, ctx, func() bool {
		current, _, err := localipc.Process(birth.PID)
		return err != nil || current != birth
	})
	stdout, stderr, code := f.run(t, ctx, "delete", "agent", "uid:"+owner.agent, "--yes")
	if code != 0 || !strings.Contains(stdout, "runtime=unknown evidence=owner-host-and-child-absent") {
		t.Fatalf("delete: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	f.requireDeleted(t, owner)
}

// A live owner in another process is asked to Stop through its host control
// socket; it exits normally, records its Wait, and the delete removes it.
func TestProcessDeleteRunningAgentActualCLI(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) { testProcessDeleteRunningAgent(t, provider) })
	}
}

func testProcessDeleteRunningAgent(t *testing.T, provider string) {
	f := newProcessDeleteCLI(t, provider)
	f.ownerArgs = append(f.ownerArgs, "--", "initial task")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	owner := f.startOwner(t, ctx)
	ref := "uid:" + owner.agent

	stdout, stderr, code := f.run(t, ctx, "delete", "agent", ref, "--dry-run")
	if code != 0 || !strings.Contains(stdout, "process-host would stop through owner host pid=") || !strings.Contains(stdout, "runtime=running") {
		t.Fatalf("dry-run: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	reg := f.registry(t)
	pane, _ := reg.Pane(owner.pane)
	if pane.Status.Activation.Process == nil || !processBirthAlive(pane.Status.Activation.Process.Child) {
		t.Fatal("dry-run stopped the provider")
	}

	stdout, stderr, code = f.run(t, ctx, "delete", "agent", ref, "--yes")
	if code != 0 || !strings.Contains(stdout, "process-host stopped through its owner host and deleted this Agent; runtime=stopped evidence=wait:") {
		t.Fatalf("delete: exit=%d stdout=%q stderr=%q owner=%s", code, stdout, stderr, owner.stderr.String())
	}
	if !strings.Contains(stdout, "receipt operation=delete.agent identity=removed address=released topology=removed desired-state=removed runtime=stopped focus=unchanged") {
		t.Fatalf("receipt runtime: %q", stdout)
	}
	if err := owner.wait(); err != nil || strings.Contains(owner.stderr.String(), "delete agent") {
		t.Fatalf("owner after Stop: %v %s", err, owner.stderr.String())
	}
	if !strings.Contains(owner.stderr.String(), "was deleted by another process; nothing to clean up") &&
		!strings.Contains(owner.stderr.String(), "was stopped by another process") && owner.stderr.Len() != 0 {
		t.Fatalf("owner notice: %q", owner.stderr.String())
	}
	f.requireDeleted(t, owner)
}

// An owner whose generation another process stopped ends with its Wait exit,
// never a delete command, and leaves the Agent offline with its Wait.
func TestProcessOwnerStoppedElsewhereActualCLI(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := newProcessDeleteCLI(t, provider)
			f.ownerArgs = append(f.ownerArgs, "--", "initial task")
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			owner := f.startOwner(t, ctx)
			// The owner syncs its snapshot only once the provider session is
			// recorded; a Stop after that is what leaves it a closed host.
			var reg coremetadata.Registry
			processCLIUntil(t, ctx, func() bool {
				reg = f.registry(t)
				pane, _ := reg.Pane(owner.pane)
				session := pane.Status.ProcessSession
				return session != nil && (session.SessionID != "" || session.ThreadID != "")
			})
			agent, _ := reg.Agent(owner.agent)
			before, _ := reg.Pane(owner.pane)
			generation := before.Status.ProcessSession.Binding.Generation
			// The same bounded stale retry the delete route uses.
			stopper := newProcessAgentDeleter()
			stopper.store = &resourceStore{load: func() (coremetadata.Registry, error) { return f.store.LoadReadOnly() }}
			if err := stopper.stopAndWait(ctx, processDeleteTarget{Agent: agent.Metadata.UID, Pane: owner.pane, binding: before.Status.ProcessSession.Binding}); err != nil {
				t.Fatalf("external Stop: %v", err)
			}
			if err := owner.wait(); err != nil || strings.Contains(owner.stderr.String(), "delete agent") {
				t.Fatalf("stopped owner: %v %q", err, owner.stderr.String())
			}
			t.Logf("stopped owner stderr=%q", owner.stderr.String())

			after := f.registry(t)
			kept, _ := after.Agent(owner.agent)
			pane, _ := after.Pane(owner.pane)
			if kept == nil || kept.Status.Phase != coremetadata.PhaseOffline || !pane.Status.Activation.IsZero() ||
				pane.Status.ProcessSession.Binding.Generation != generation || pane.Status.LastTermination == nil {
				t.Fatal("stopped generation was not kept offline with its Wait")
			}
		})
	}
}

// The cleanup command process creation prints on a failed rollback runs as
// printed: outside a terminal it asks for --yes like every delete, and with
// --yes it removes the Agent.
func TestProcessDeleteCleanupGuidanceActualCLI(t *testing.T) {
	f := newProcessDeleteCLI(t, aiModeClaude)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	owner := f.startOwner(t, ctx)
	_ = owner.input.Close()
	if err := owner.wait(); err != nil {
		t.Fatalf("owner EOF: %v %s", err, owner.stderr.String())
	}
	guidance := processCreateCleanupError(processAgentCreateResult{Binding: processhost.Binding{Agent: owner.agent, Pane: owner.pane}, waitRecorded: true}, errors.New("fixture")).Error()
	_, command, ok := strings.Cut(guidance, "cleanup: ")
	command, _, _ = strings.Cut(command, ": ")
	argv := strings.Fields(command)
	if !ok || len(argv) < 4 || argv[0] != "projmux" {
		t.Fatalf("guidance %q", guidance)
	}
	_, stderr, code := f.run(t, ctx, argv[1:]...)
	if code != 2 || !strings.Contains(stderr, "Re-run with --yes") {
		t.Fatalf("non-interactive guidance: exit=%d stderr=%q", code, stderr)
	}
	f.requireKept(t, owner)
	stdout, stderr, code := f.run(t, ctx, append(argv[1:], "--yes")...)
	if code != 0 || !strings.Contains(stdout, "runtime=offline") {
		t.Fatalf("guidance --yes: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	f.requireDeleted(t, owner)
}
