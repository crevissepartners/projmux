package projmux

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/cli"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	claude "github.com/crevissepartners/projmux/internal/integrations/agents/claude"
	"github.com/crevissepartners/projmux/internal/integrations/agents/localipc"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/processhost"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

type stopFailureRunner struct {
	path, session, uid, failure string
	reads, kills, calls         int
	cancel                      context.CancelFunc
}

func (r *stopFailureRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.calls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name != "tmux" || len(args) < 3 || args[0] != "-S" || args[1] != r.path {
		return nil, errors.New("unbound stop runner")
	}
	args = args[2:]
	var out strings.Builder
	for len(args) > 0 {
		n := len(args)
		for i, a := range args {
			if a == ";" {
				n = i
				break
			}
		}
		cmd := args[:n]
		if n == len(args) {
			args = nil
		} else {
			args = args[n+1:]
		}
		format := cmd[len(cmd)-1]
		switch cmd[0] {
		case "display-message":
			if strings.Contains(format, "#{pid}") {
				r.reads++
			}
			pid := "4242"
			if r.failure == "pid" && r.reads >= 4 {
				pid = "4243"
			}
			out.WriteString(strings.NewReplacer("#{socket_path}", r.path, "#{pid}", pid).Replace(format) + "\n")
		case "show-options":
			if format == tmuxopts.AppGlobal || format == mutationruntime.RuntimeMutationSocketNameOption {
				out.WriteString("\n")
			}
		case "list-sessions":
			if r.failure == "no-session" || (r.failure == "success" && r.kills > 0) {
				continue
			}
			uid := r.uid
			if r.failure == "uid" {
				uid = "other"
			}
			out.WriteString(strings.NewReplacer("#{session_id}", "$1", "#{session_name}", r.session, "#{"+tmuxopts.ProjectUIDSession+"}", uid, "#{"+tmuxopts.SessionRole+"}", "").Replace(format) + "\n")
			if r.failure == "cancel-after-admission" && r.cancel != nil {
				r.cancel()
				r.cancel = nil
			}
		case "kill-session":
			r.kills++
			if r.failure == "success" {
				return nil, nil
			}
			if r.cancel != nil {
				r.cancel()
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
			}
			return nil, errors.New("injected kill failure")
		default:
			return nil, errors.New("unexpected stop verb: " + cmd[0])
		}
	}
	return []byte(out.String()), nil
}

func TestStopProjectPIDUIDGuardsAndCancellation(t *testing.T) {
	for _, failure := range []string{"pid", "uid", "kill", "cancel-before", "cancel-after-admission"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			c, err := NewClient(Options{StateDir: filepath.Join(root, "state"), HomeDir: root})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.CreateProject(context.Background(), core.CreateProjectRequest{Root: root, Name: "project"})
			if err != nil {
				t.Fatal(err)
			}
			reg, err := store.NewStore(store.PathFor(c.paths.StateDir)).LoadReadOnly()
			if err != nil {
				t.Fatal(err)
			}
			runner := &stopFailureRunner{path: filepath.Join(root, "socket"), session: c.projectSessionName(root), uid: reg.Projects[0].Metadata.UID, failure: failure}
			// Snapshot the actual bytes: compensation must retain all desired state.
			path := store.PathFor(c.paths.StateDir)
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "cancel-before" {
				cancel()
			}
			if failure == "cancel-after-admission" {
				// Cancel at the admitted kill, after StopProject has detached the context.
				runner.cancel = cancel
				runner.failure = "kill"
			}
			result, err := c.stopProjectWithRunner(ctx, core.StopProjectRequest{Ref: "project", SocketPath: runner.path}, runner)
			if result.Stopped || err == nil {
				t.Fatalf("unexpected success %+v %v", result, err)
			}
			if failure == "cancel-before" {
				if !errors.Is(err, context.Canceled) || runner.calls != 0 {
					t.Fatal("cancelled call reached tmux")
				}
			} else {
				var refusal *Refusal
				if !errors.As(err, &refusal) {
					t.Fatalf("undeclared refusal %v", err)
				}
				if refusal.Code != CodeProjectStopExecutionFailed && refusal.Code != CodeProjectStopRouteRefused {
					t.Fatalf("code %s", refusal.Code)
				}
			}
			if failure == "cancel-after-admission" && ctx.Err() == nil {
				t.Fatal("admitted operation did not exercise cancellation")
			}
			if (failure == "pid" || failure == "uid") && runner.kills != 0 {
				t.Fatal("drift killed")
			}
			if (failure == "kill" || failure == "cancel-after-admission") && runner.kills != 1 {
				t.Fatalf("kill count %s: %v", strconv.Itoa(runner.kills), err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(before) != string(after) {
				t.Fatal("failed stop changed Registry")
			}
		})
	}
}

// Keep request shape refusal messages SDK-facing without rewriting user values.
func TestStopProjectInputCodes(t *testing.T) {
	if _, err := NewClient(Options{SocketPath: "relative"}); err == nil || !strings.Contains(err.Error(), "SocketPath") {
		t.Fatalf("relative Options.SocketPath was not rejected by construction: %v", err)
	}

	c, err := NewClient(Options{StateDir: filepath.Join(t.TempDir(), "state")})
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"uid:", " uid: "} {
		_, err := c.StopProject(context.Background(), core.StopProjectRequest{Ref: ref})
		var refusal *Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeSelectorInvalid || !strings.Contains(err.Error(), "StopProjectRequest.Ref") || strings.Contains(err.Error(), "--project") {
			t.Fatalf("malformed Ref shape: %v", err)
		}
	}

	_, err = c.StopProject(context.Background(), core.StopProjectRequest{})
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Code != CodeSelectorInvalid || !strings.Contains(err.Error(), "StopProjectRequest.Ref") {
		t.Fatal(err)
	}
}

// A missing tmux executable cannot mask process admission. The selected exact
// claim still refuses, with typed stale cause and no writes to either root.
type absentProjectStopRunner struct{ calls int }

func (r *absentProjectStopRunner) Run(context.Context, string, ...string) ([]byte, error) {
	r.calls++
	return nil, exec.ErrNotFound
}

func TestStopProjectProcessOnlyExplicitRootsAndMissingTmux(t *testing.T) {
	first, reg, uid, puid := sdkStopFixture(t)
	second, other, _, _ := sdkStopFixture(t)
	path := store.PathFor(first.paths.StateDir)
	storage := store.NewStore(path)
	_, err := storage.Update(func(reg *metadata.Registry) error {
		a, _ := reg.Agent(uid)
		p, _ := reg.Pane(puid)
		a.Status.Phase = metadata.PhaseRunning
		a.Status.LastTermination = nil
		p.Status.LastTermination = nil
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	poison := t.TempDir()
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "PROJMUX_CONFIG_DIR", "PROJMUX_STATE_DIR", "PROJMUX_REGISTRY_PATH", "TMUX", "TMUX_PANE"} {
		t.Setenv(key, poison)
	}
	runner := &absentProjectStopRunner{}
	result, err := first.stopProjectWithRunner(context.Background(), core.StopProjectRequest{Ref: "uid:" + reg.Projects[0].Metadata.UID}, runner)
	var refusal *Refusal
	var exact *mutationruntime.ProjectProcessStopFailure
	if !errors.As(err, &refusal) || refusal.Code != CodeProjectStopExecutionFailed || !errors.Is(err, processhost.ErrStale) || !errors.As(err, &exact) || exact.AgentUID != uid || exact.PaneUID != puid || exact.Generation != "generation" || result.Stopped || result.Receipt.Operation != "" || runner.calls == 0 {
		t.Fatalf("process-only %+v %v", result, err)
	}
	_, err = second.stopProjectWithRunner(context.Background(), core.StopProjectRequest{Ref: "uid:" + reg.Projects[0].Metadata.UID}, runner)
	if !errors.As(err, &refusal) || refusal.Code != CodeResourceNotFound {
		t.Fatal("borrowed another root")
	}
	inactive, err := second.stopProjectWithRunner(context.Background(), core.StopProjectRequest{Ref: "uid:" + other.Projects[0].Metadata.UID}, runner)
	if !errors.As(err, &refusal) || refusal.Code != CodeProjectNotLive || inactive.Stopped {
		t.Fatalf("inactive %+v %v", inactive, err)
	}
	after, _ := os.ReadFile(path)
	latest, e := store.NewStore(store.PathFor(second.paths.StateDir)).LoadReadOnly()
	if string(before) != string(after) || e != nil || !reflect.DeepEqual(other, latest) {
		t.Fatal("root changed")
	}
	entries, e := os.ReadDir(poison)
	if e != nil || len(entries) != 0 {
		t.Fatal("ambient root written")
	}
	if os.Getenv("HOME") != poison || os.Getenv("PROJMUX_STATE_DIR") != poison {
		t.Fatal("ambient env changed")
	}
}

// This subprocess owns only a fixture child and its private Stop socket. It
// publishes actual child Wait before retiring, so SDK receipt tests exercise
// confirmation rather than substituting a prebuilt list of stopped Agents.
func TestStopProjectReceiptOwnerFixture(t *testing.T) {
	path := os.Getenv("PROJMUX_TEST_RECEIPT_OWNER_REGISTRY")
	if path == "" {
		return
	}
	uid := os.Getenv("PROJMUX_TEST_RECEIPT_OWNER_AGENT")
	storage := store.NewStore(path)
	reg, err := storage.LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, ok := reg.Agent(uid)
	if !ok {
		t.Fatal("fixture Agent missing")
	}
	pane, ok := reg.Pane(agent.Status.PaneRef)
	if !ok {
		t.Fatal("fixture Pane missing")
	}
	b := pane.Status.ProcessSession.Binding
	child := exec.Command("/bin/sh", "-c", "read ignored || exit 0")
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	owner, _, err := localipc.Process(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	process, _, err := localipc.Process(child.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	activation := metadata.ProcessActivation{Binding: b, HostProcess: owner, Child: process}
	_, err = storage.Update(func(reg *metadata.Registry) error {
		a, _ := reg.Agent(uid)
		p, _ := reg.Pane(b.PaneUID)
		a.Status.Phase, a.Status.LastTermination = metadata.PhaseRunning, nil
		p.Status.LastTermination = nil
		p.Status.Activation = metadata.PaneActivation{Kind: metadata.RuntimeProcess, Generation: b.Generation, AgentUID: uid, OperationID: b.OperationID, Process: &activation}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	socket := claude.ActivationLeaseDir(path, b.PaneUID, b.Generation) + "/codex-host.sock"
	if err := os.MkdirAll(filepath.Dir(socket), 0700); err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(filepath.Dir(socket))
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0600); err != nil {
		t.Fatal(err)
	}
	if err := listener.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	fmt.Println("receipt-owner-ready")
	conn, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var request struct {
		Foreground struct {
			Action    string
			Authority processhost.Authority
		}
	}
	if err := json.NewDecoder(conn).Decode(&request); err != nil {
		t.Fatal(err)
	}
	if request.Foreground.Action != "stop" || request.Foreground.Authority.Binding.Agent != uid || request.Foreground.Authority.Binding.Generation != b.Generation {
		t.Fatal("wrong Stop authority")
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	code := child.ProcessState.ExitCode()
	wait := metadata.TerminationEvidence{Source: metadata.TerminationSourceSupervisor, Classification: metadata.TerminationNormal, ObservedAt: time.Now(), AgentUID: uid, PaneUID: b.PaneUID, Generation: b.Generation, OperationID: b.OperationID, ExitCode: &code}
	if _, err := storage.Update(func(reg *metadata.Registry) error {
		return (metadata.Mutator{}).RecordProcessWait(reg, activation, wait)
	}); err != nil {
		t.Fatal(err)
	}
	if err := mutationruntime.WriteProcessRetirement(path, activation, &wait); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(struct{ Accepted bool }{true}); err != nil {
		t.Fatal(err)
	}
}

func startProjectReceiptOwner(t *testing.T, c *Client, uid string) <-chan struct{} {
	t.Helper()
	// Replace only sdkStopFixture's synthetic retirement witness with this
	// fixture owner's actual activation; no live state is involved.
	retirements := filepath.Join(c.paths.StateDir, "process-retirements")
	entries, err := os.ReadDir(retirements)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(retirements, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStopProjectReceiptOwnerFixture$")
	cmd.Env = append(os.Environ(), "PROJMUX_TEST_RECEIPT_OWNER_REGISTRY="+store.PathFor(c.paths.StateDir), "PROJMUX_TEST_RECEIPT_OWNER_AGENT="+uid)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		<-done
		if waitErr != nil {
			t.Errorf("fixture owner failed: %v", waitErr)
		}
	})
	scanner := bufio.NewScanner(out)
	if !scanner.Scan() || scanner.Text() != "receipt-owner-ready" {
		t.Fatal("fixture owner not ready", scanner.Err())
	}
	reg, err := store.NewStore(store.PathFor(c.paths.StateDir)).LoadReadOnly()
	if err != nil {
		t.Fatal(err)
	}
	agent, _ := reg.Agent(uid)
	pane, _ := reg.Pane(agent.Status.PaneRef)
	activation := *pane.Status.Activation.Process
	lease := claude.ActivationLeaseDir(store.PathFor(c.paths.StateDir), pane.Metadata.UID, activation.Binding.Generation)
	t.Cleanup(func() {
		<-done
		for _, identity := range []metadata.ProcessIdentity{activation.HostProcess, activation.Child} {
			if _, _, err := localipc.Process(identity.PID); !errors.Is(err, localipc.ErrProcessAbsent) {
				t.Errorf("fixture process %d not absent: %v", identity.PID, err)
			}
		}
		if _, err := os.Stat(lease); !os.IsNotExist(err) {
			t.Errorf("fixture lease root not removed: %v", err)
		}
	})
	return done
}

func TestStopProjectReceiptRuntimeOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, tmux  string
		failedAgent bool
		runtime     core.RuntimeEffect
		project     core.ReceiptAction
	}{
		{"all stopped", "success", false, core.RuntimeStopped, core.ActionStopped},
		{"Agents stopped tmux failed", "kill", false, core.RuntimePartiallyStopped, core.ActionUnchanged},
		{"tmux stopped Agent unconfirmed", "success", true, core.RuntimePartiallyStopped, core.ActionStopped},
		{"Agents only", "no-session", false, core.RuntimeAgentsStopped, core.ActionUnchanged},
		{"Agents only partial", "no-session", true, core.RuntimePartiallyStopped, core.ActionUnchanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, reg, uid, _ := sdkStopFixture(t)
			storage := store.NewStore(store.PathFor(c.paths.StateDir))
			if tc.failedAgent {
				_, err := storage.Update(func(reg *metadata.Registry) error {
					mut := store.DefaultMutator()
					_, a, _, err := mut.CreateProcessWindow(reg, reg.Projects[0].Metadata.UID, metadata.BootstrapWindow{Name: "unconfirmed"}, metadata.CreateAgentOptions{Name: "unconfirmed", Provider: "codex", OperationID: "create-unconfirmed"}, metadata.ProcessBinding{HostInstanceID: "unavailable", Generation: "unconfirmed", OperationID: "unconfirmed"})
					if err == nil {
						stored, _ := reg.Agent(a.Metadata.UID)
						stored.Status.Phase = metadata.PhaseRunning
					}
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			done := startProjectReceiptOwner(t, c, uid)
			runner := &stopFailureRunner{path: filepath.Join(c.paths.StateDir, "socket"), session: c.projectSessionName(reg.Projects[0].Spec.Root), uid: reg.Projects[0].Metadata.UID, failure: tc.tmux}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := c.stopProjectWithRunner(ctx, core.StopProjectRequest{Ref: "uid:" + reg.Projects[0].Metadata.UID, SocketPath: runner.path}, runner)
			failed := tc.failedAgent || tc.tmux == "kill"
			if (err != nil) != failed || result.Stopped == failed || result.Receipt.Effects.Runtime != tc.runtime || len(result.Receipt.AffectedUIDs) != 2 || result.Receipt.AffectedUIDs[0].Action != tc.project || result.Receipt.AffectedUIDs[1].UID != uid || result.Receipt.AffectedUIDs[1].Action != core.ActionStopped {
				t.Fatalf("receipt=%+v err=%v", result, err)
			}
			// The public SDK JSON must validate and project through the same CLI
			// receipt vocabulary, with no adapter-specific replacement strings.
			encoded, marshalErr := json.Marshal(result.Receipt)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var cliReceipt cli.OperationReceipt
			if err := json.Unmarshal(encoded, &cliReceipt); err != nil {
				t.Fatal(err)
			}
			if err := cliReceipt.Validate(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(cliReceipt.HumanLine(), "runtime="+string(tc.runtime)) || cliReceipt.AffectedUIDs[0].Action != cli.ReceiptAction(tc.project) {
				t.Fatalf("SDK/CLI projection differs: %s", cliReceipt.HumanLine())
			}
			if failed {
				var refusal *Refusal
				if !errors.As(err, &refusal) || refusal.Code != CodeProjectStopExecutionFailed {
					t.Fatal(err)
				}
			}
			if tc.failedAgent && !strings.Contains(err.Error(), mutationruntime.ProjectStopAgentsStillRunning) {
				t.Fatal(err)
			}
			if tc.tmux == "kill" && !strings.Contains(err.Error(), "injected kill failure") {
				t.Fatal(err)
			}
			<-done
		})
	}
}
