package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/crevissepartners/projmux/internal/config"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/diagnostics"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/services/mutations"
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
	assertProcessOwnerStop(t, f.store.Path(), owner.stderr.String(), diagnostics.OwnerStopControl, owner.cmd.Process.Pid)
	projectionStderr := processOwnerStderrProjection(t, owner.stderr.String(), diagnostics.OwnerStopControl)
	if !strings.Contains(projectionStderr, "was deleted by another process; nothing to clean up") &&
		!strings.Contains(projectionStderr, "was stopped by another process") && projectionStderr != "" {
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

// SDK and CLI share the exact delete-stop owner admission and actual retirement.
func TestProcessDeleteSDKRunningAndOfflineActualCLI(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		for _, offline := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/offline=%t", provider, offline), func(t *testing.T) {
				f := newProcessDeleteCLI(t, provider)
				f.ownerArgs = append(f.ownerArgs, "--", "initial task")
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				defer cancel()
				owner := f.startOwner(t, ctx)
				before := f.registry(t)
				pane, _ := before.Pane(owner.pane)
				activation := *pane.Status.Activation.Process
				if offline {
					_ = owner.input.Close()
					if err := owner.wait(); err != nil {
						t.Fatal(err)
					}
				}
				paths, err := config.DefaultPathsFromEnv()
				if err != nil {
					t.Fatal(err)
				}
				c, err := projmux.NewClient(projmux.Options{ConfigDir: paths.ConfigDir, StateDir: paths.StateDir, HomeDir: f.root})
				if err != nil {
					t.Fatal(err)
				}
				// Explicit roots stay bound even with poisoned ambient routing.
				poison := filepath.Join(f.root, "poison")
				if err = os.Mkdir(poison, 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PROJMUX_STATE_DIR", poison)
				t.Setenv("HOME", poison)
				req := core.DeleteRequest{Kind: core.KindAgent, Ref: "uid:" + owner.agent, DryRun: true}
				result, err := c.Delete(ctx, req)
				if err != nil || result.Committed || len(result.Plan.Targets) != 1 {
					t.Fatalf("dry %+v %v", result, err)
				}
				f.requireKept(t, owner)
				req.DryRun = false
				req.Confirm = true
				result, err = c.Delete(ctx, req)
				if err != nil || !result.Committed {
					t.Fatalf("delete %+v %v", result, err)
				}
				if !offline {
					if err = owner.wait(); err != nil {
						t.Fatal(err)
					}
				}
				f.requireDeleted(t, owner)
				if !mutationruntime.ProcessActivationAbsent(activation, localipc.Process) {
					t.Fatal("kernel owner/child still alive")
				}
				if result.Receipt.Target.UID != owner.agent || (result.Receipt.Effects.Runtime == core.RuntimeStopped) != (!offline) {
					t.Fatalf("receipt %+v", result.Receipt)
				}
				entries, _ := os.ReadDir(poison)
				if len(entries) != 0 {
					t.Fatal("ambient root written")
				}
			})
		}
	}
}

func TestProcessDeleteClaimSDKCLIProjectionAndNoFakeWait(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, sdk := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/sdk=%t", provider, sdk), func(t *testing.T) {
				f := deferredResumeCLIFixture(t, provider)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
				awaitDetachedInitialConversation(t, ctx, f, first.ref)
				first.shutdown(t)
				cleanupDetachedResume(t, f, first.ref)
				detachedResumeCLI(t, ctx, f, "agent", "resume", first.ref, "--detach", "--wait-for-peer")
				uid := strings.TrimPrefix(first.ref, "uid:")
				path := mutationruntime.DeferredClaimPath(f.store.Path(), uid)
				claim, err := mutationruntime.ReadDeferredClaim(path)
				if err != nil || !mutationruntime.DeferredClaimLive(claim) {
					t.Fatalf("claim %+v %v", claim, err)
				}
				before, _ := f.store.LoadReadOnly()
				a, _ := before.Agent(uid)
				p, _ := before.Pane(a.Status.PaneRef)
				wait := p.Status.LastTermination.Clone()
				dry := detachedResumeCLI(t, ctx, f, "delete", "agent", first.ref, "--dry-run")
				if !bytes.Contains(dry, []byte("runtime=offline evidence=wait:")) || bytes.Contains(dry, []byte("would stop")) {
					t.Fatalf("claim projection changed: %s", dry)
				}
				// A copied baseline verifies the exact public dry-run bytes on this same UID.
				if baseline := os.Getenv("PMX_TEST_BASELINE_CLI"); baseline != "" {
					old, err := exec.CommandContext(ctx, baseline, "delete", "agent", first.ref, "--dry-run").CombinedOutput()
					if err != nil || !bytes.Equal(old, dry) {
						t.Fatalf("baseline dry changed: %v old%s new%s", err, old, dry)
					}
				}
				current, _ := mutationruntime.ReadDeferredClaim(path)
				if current.StopRequested || !mutationruntime.DeferredClaimLive(claim) {
					t.Fatal("dry requested claim stop")
				}
				// Protection is checked before the private claim admission, too.
				_, err = f.store.Update(func(r *coremetadata.Registry) error {
					a, _ := r.Agent(uid)
					if a.Metadata.Labels == nil {
						a.Metadata.Labels = map[string]string{}
					}
					a.Metadata.Labels[coremetadata.LabelProtected] = "true"
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				paths, err := config.DefaultPathsFromEnv()
				if err != nil {
					t.Fatal(err)
				}
				client, err := projmux.NewClient(projmux.Options{ConfigDir: paths.ConfigDir, StateDir: paths.StateDir, HomeDir: f.root})
				if err != nil {
					t.Fatal(err)
				}
				_, err = client.Delete(ctx, core.DeleteRequest{Kind: core.KindAgent, Ref: first.ref, Confirm: true})
				var refusal *projmux.Refusal
				if !errors.As(err, &refusal) || refusal.Code != projmux.CodeAgentProtected {
					t.Fatalf("protected %v", err)
				}
				current, _ = mutationruntime.ReadDeferredClaim(path)
				if current.StopRequested || !mutationruntime.DeferredClaimLive(claim) {
					t.Fatal("protected stopped claim")
				}
				_, err = f.store.Update(func(r *coremetadata.Registry) error {
					a, _ := r.Agent(uid)
					delete(a.Metadata.Labels, coremetadata.LabelProtected)
					return nil
				})
				if err != nil {
					t.Fatal(err)
				}
				if sdk {
					result, err := client.Delete(ctx, core.DeleteRequest{Kind: core.KindAgent, Ref: first.ref, Confirm: true})
					if err != nil || !result.Committed || result.Receipt.Effects.Runtime != core.RuntimeUnchanged {
						t.Fatalf("SDK claim %+v %v", result, err)
					}
				} else {
					out := detachedResumeCLI(t, ctx, f, "delete", "agent", first.ref, "--yes")
					if !bytes.Contains(out, []byte("runtime=offline evidence=wait:")) || !bytes.Contains(out, []byte("no process was signaled")) || !bytes.Contains(out, []byte("runtime=unchanged")) {
						t.Fatalf("CLI claim %s", out)
					}
				}
				if !mutationruntime.ProcessIdentityAbsent(claim.Process, localipc.Process) {
					t.Fatal("delete returned before claimant kernel retirement")
				}
				if latest, err := mutationruntime.ReadDeferredClaim(path); err != nil || latest.Nonce != "" {
					t.Fatalf("claim retained %+v %v", latest, err)
				}
				after, _ := f.store.LoadReadOnly()
				if _, ok := after.Agent(uid); ok {
					t.Fatal("Agent retained")
				}
				if _, ok := after.Pane(p.Metadata.UID); ok {
					t.Fatal("Pane retained")
				}
				// The only retirement witness belongs to the original born generation.
				_, err = mutationruntime.ReadProcessRetirement(f.store.Path(), p.Status.ProcessSession.Binding, wait)
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestProcessDeleteClaimBaselineActualCLI(t *testing.T) {
	baseline := os.Getenv("PMX_TEST_BASELINE_CLI")
	if baseline == "" {
		t.Log("baseline comparison is an additional explicit source qualification")
		return
	}
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			f := deferredResumeCLIFixture(t, provider)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			first := startResumeCLIInvocation(t, ctx, f, f.args("--provider", provider, "--profile", "none", "--model", "stub-model", "--effort", "low", "--", "initial"))
			awaitDetachedInitialConversation(t, ctx, f, first.ref)
			first.shutdown(t)
			cleanupDetachedResume(t, f, first.ref)
			detachedResumeCLI(t, ctx, f, "agent", "resume", first.ref, "--detach", "--wait-for-peer")
			uid := strings.TrimPrefix(first.ref, "uid:")
			path := mutationruntime.DeferredClaimPath(f.store.Path(), uid)
			oldClaim, err := mutationruntime.ReadDeferredClaim(path)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := f.store.LoadReadOnly()
			oldOutput, err := exec.CommandContext(ctx, baseline, "delete", "agent", first.ref, "--yes").CombinedOutput()
			if err != nil {
				t.Fatalf("baseline delete %v %s", err, oldOutput)
			}
			liveAtReturn := !mutationruntime.ProcessIdentityAbsent(oldClaim.Process, localipc.Process)
			t.Logf("baseline root=%s claimant-pid=%d claimant-start=%s returned-live=%t output=%s", f.root, oldClaim.Process.PID, oldClaim.Process.Start, liveAtReturn, oldOutput)
			if _, err = f.store.Update(func(r *coremetadata.Registry) error { *r = before.Clone(); return nil }); err != nil {
				t.Fatal(err)
			}
			detachedResumeCLI(t, ctx, f, "agent", "stop", first.ref)
			if !mutationruntime.ProcessIdentityAbsent(oldClaim.Process, localipc.Process) {
				t.Fatal("baseline exact recovery retained claimant")
			}

			detachedResumeCLI(t, ctx, f, "agent", "resume", first.ref, "--detach", "--wait-for-peer")
			next, err := mutationruntime.ReadDeferredClaim(path)
			if err != nil || !mutationruntime.DeferredClaimLive(next) {
				t.Fatal(err)
			}
			newOutput := detachedResumeCLI(t, ctx, f, "delete", "agent", first.ref, "--yes")
			if !bytes.Equal(oldOutput, newOutput) {
				t.Fatalf("CLI actual bytes changed: old%s new%s", oldOutput, newOutput)
			}
			if !mutationruntime.ProcessIdentityAbsent(next.Process, localipc.Process) {
				t.Fatal("new delete returned with claimant live")
			}
		})
	}
}

// A leaf process Pane stops its exact owner and preserves the protected Agent,
// conversation, and sibling. A protected Agent delete still refuses before Stop.
func TestProcessDeleteLeafPaneActualCLI(t *testing.T) {
	for _, provider := range []string{aiModeClaude, aiModeCodex} {
		t.Run(provider, func(t *testing.T) {
			f := newProcessDeleteCLI(t, provider)
			f.ownerArgs = append(f.ownerArgs, "--", "initial task")
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
			defer cancel()
			owner := f.startOwner(t, ctx)
			// Wait for the actual conversation, so equality after deletion
			// cannot pass merely because both references are empty.
			processCLIUntil(t, ctx, func() bool {
				reg := f.registry(t)
				p, _ := reg.Pane(owner.pane)
				a, _ := reg.Agent(owner.agent)
				return p.Status.ProcessSession != nil &&
					(p.Status.ProcessSession.SessionID != "" || p.Status.ProcessSession.ThreadID != "") &&
					p.Status.ProcessSession.TurnID == "" && a.Status.SessionRef != nil
			})
			siblingUID := ""
			_, err := f.store.Update(func(reg *coremetadata.Registry) error {
				a, _ := reg.Agent(owner.agent)
				if a.Metadata.Labels == nil {
					a.Metadata.Labels = map[string]string{}
				}
				a.Metadata.Labels[coremetadata.LabelProtected] = "true"
				sibling, err := intmetadata.DefaultMutator().AddPane(reg, f.window, coremetadata.BootstrapPane{Name: "retained-shell"}, "", "retain-sibling")
				if err == nil {
					siblingUID = sibling.Metadata.UID
				}
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			before := f.registry(t)
			agent, _ := before.Agent(owner.agent)
			pane, _ := before.Pane(owner.pane)
			sibling, _ := before.Pane(siblingUID)
			activation := *pane.Status.Activation.Process
			plan := mutations.BuildDeletePlan(before, coremetadata.KindPane, selector.Resolution{
				Kind: coremetadata.KindPane, Matches: []selector.Match{{Kind: coremetadata.KindPane, UID: owner.pane}},
			})
			if len(plan.Targets) != 1 || len(plan.Targets[0].Descendants) != 0 || mutations.ValidateDeleteProtection(before, plan) != nil {
				t.Fatalf("expected one unprotected leaf Pane plan: %+v", plan)
			}

			_, stderr, code := f.run(t, ctx, "delete", "agent", "uid:"+owner.agent, "--yes")
			if code != 1 || !strings.Contains(stderr, "agent-protected") {
				t.Fatalf("protected Agent delete: exit=%d stderr=%q", code, stderr)
			}
			if after := f.registry(t); !reflect.DeepEqual(before, after) || !processBirthAlive(activation.Child) {
				t.Fatal("protected Agent refusal changed records or stopped provider")
			}
			stdout, stderr, code := f.run(t, ctx, "delete", "pane", "uid:"+owner.pane, "--dry-run")
			if code != 0 || !strings.Contains(stdout, "dry-run: nothing was deleted") || !strings.Contains(stdout, "runtime=running") {
				t.Fatalf("Pane dry-run: exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			if after := f.registry(t); !reflect.DeepEqual(before, after) || !processBirthAlive(activation.Child) {
				t.Fatal("Pane dry-run changed records or stopped provider")
			}

			stdout, stderr, code = f.run(t, ctx, "delete", "pane", "uid:"+owner.pane, "--yes")
			if code != 0 || !strings.Contains(stdout, "runtime=stopped evidence=wait:") {
				t.Fatalf("Pane delete: exit=%d stdout=%q stderr=%q owner=%s", code, stdout, stderr, owner.stderr.String())
			}
			// These are checked at command return, before reaping the owner:
			// durable actual Wait plus retirement, and kernel absence of both.
			after := f.registry(t)
			kept, ok := after.Agent(owner.agent)
			if !ok || kept.Status.Phase != coremetadata.PhaseOffline ||
				!reflect.DeepEqual(kept.Status.SessionRef, agent.Status.SessionRef) ||
				!reflect.DeepEqual(kept.Metadata.Labels, agent.Metadata.Labels) {
				t.Fatalf("Agent identity, conversation or protection not retained: %+v", kept)
			}
			wait := kept.Status.LastTermination
			if wait == nil || wait.AgentUID != owner.agent || wait.PaneUID != owner.pane ||
				wait.Generation != activation.Binding.Generation || wait.ExitCode == nil || *wait.ExitCode != 0 {
				t.Fatalf("missing actual successful Wait: %+v", wait)
			}
			if _, err := mutationruntime.ReadProcessRetirement(f.store.Path(), activation.Binding, wait); err != nil {
				t.Fatalf("missing retirement witness: %v", err)
			}
			if !mutationruntime.ProcessActivationAbsent(activation, localipc.Process) {
				t.Fatal("Pane delete returned before owner/provider retirement")
			}
			if _, ok := after.Pane(owner.pane); ok {
				t.Fatal("deleted Pane survived")
			}
			retained, ok := after.Pane(siblingUID)
			if !ok || !reflect.DeepEqual(retained, sibling) {
				t.Fatal("sibling Pane changed")
			}
			if _, ok := after.Window(f.window); !ok {
				t.Fatal("leaf Pane delete removed Window")
			}
			if err := owner.wait(); err != nil {
				t.Fatalf("owner after Stop: %v %s", err, owner.stderr.String())
			}
			assertProcessOwnerStop(t, f.store.Path(), owner.stderr.String(), diagnostics.OwnerStopControl, owner.cmd.Process.Pid)
		})
	}
}
