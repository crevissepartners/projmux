package app

import (
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
)

type installedRecoveryInput struct {
	Root           string            `json:"root"`
	Binary         string            `json:"binary"`
	BinarySHA256   string            `json:"binarySHA256"`
	SourceHead     string            `json:"sourceHead"`
	SourceTree     string            `json:"sourceTree"`
	Releases       []string          `json:"releases"`
	AuthFile       string            `json:"authFile"`
	Model          string            `json:"model"`
	Inputs         []string          `json:"inputs"`
	HostNamespaces map[string]string `json:"hostNamespaces"`
	Evidence       string            `json:"evidence"`
}

type installedRecoveryAgent struct {
	Project      string                         `json:"project"`
	Window       string                         `json:"window"`
	Agent        string                         `json:"agent"`
	Pane         string                         `json:"pane"`
	Runtime      string                         `json:"runtime"`
	Activation   string                         `json:"activation"`
	Thread       string                         `json:"thread"`
	Session      string                         `json:"session"`
	Authority    coremetadata.CodexAuthorityRef `json:"authority"`
	ControlEpoch string                         `json:"controlEpoch"`
}

type installedRecoveryTurn struct {
	InputIndex int                      `json:"inputIndex"`
	Sample     string                   `json:"sample"`
	Agent      installedRecoveryAgent   `json:"identity"`
	Turn       string                   `json:"turn"`
	Status     codexappserver.TurnState `json:"status"`
}

type installedRecoveryRow struct {
	Name     string                            `json:"name"`
	Manager  codexinstalled.ManagedDaemonProof `json:"manager"`
	Turns    []installedRecoveryTurn           `json:"turns"`
	Attempts []*installedRecoveryAttempt       `json:"attempts,omitempty"`
}

type installedRecoveryLedger struct {
	Result           string                 `json:"result"`
	SourceHead       string                 `json:"sourceHead"`
	SourceTree       string                 `json:"sourceTree"`
	BinarySHA256     string                 `json:"binarySHA256"`
	TestBinarySHA256 string                 `json:"testBinarySHA256"`
	Submissions      int                    `json:"submissions"`
	Rows             []installedRecoveryRow `json:"rows"`
	Cleanup          bool                   `json:"cleanup"`
	AuthRemoved      bool                   `json:"authRemoved"`
	TmuxSocket       string                 `json:"tmuxSocket"`
}

// TestInstalledManagedCodexAuthorityRecoveryMatrix is opt-in, model-dependent
// repo-client conformance. It is deliberately independent of pool qualification
// and persisted-thread stdio resume. All inputs are explicit fresh fixture work.
func TestInstalledManagedCodexAuthorityRecoveryMatrix(t *testing.T) {
	inputPath := os.Getenv("PROJMUX_CODEX_RECOVERY_INPUT")
	if inputPath == "" {
		t.Skip("requires explicit private managed-daemon matrix input")
	}
	raw, err := codexinstalled.ReadExplicitInput(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	var input installedRecoveryInput
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal("invalid recovery input")
	}
	if !filepath.IsAbs(input.Root) || !strings.HasPrefix(input.Root, "/tmp/") || input.Root == "/tmp" || !filepath.IsAbs(input.Binary) || len(input.Releases) != 2 || len(input.Inputs) != 10 || len(input.SourceHead) != 40 || len(input.SourceTree) != 40 || len(input.BinarySHA256) != 64 || !filepath.IsAbs(input.Evidence) || input.Model == "" {
		t.Fatal("explicit fixture root/binary/source/releases/model/ten inputs/evidence required")
	}
	for _, text := range input.Inputs {
		if strings.TrimSpace(text) == "" {
			t.Fatal("empty validation input")
		}
	}
	isolation := codexinstalled.ManagerIsolation{HostNamespaces: input.HostNamespaces}
	if _, err := isolation.Verify(); err != nil {
		t.Fatal(err)
	}
	testDigest := verifyInstalledRecoveryBinary(t, input.Binary, input.SourceHead, input.BinarySHA256)

	ledger := installedRecoveryLedger{Result: "RUNNING", SourceHead: input.SourceHead, SourceTree: input.SourceTree, BinarySHA256: input.BinarySHA256, TestBinarySHA256: testDigest}
	save := func() {
		t.Helper()
		body, err := json.MarshalIndent(ledger, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(input.Evidence, append(body, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		if t.Failed() {
			ledger.Result = "FAIL"
		}
		save()
	}()
	fixture := newInstalledRecoveryFixture(t, input.Root)
	if err := fixture.SelectManagedRelease(input.Releases[0]); err != nil {
		t.Fatal(err)
	}
	fixture.ApplyEnv(t.Setenv)
	// Authentication is copied only from an explicit protected private input.
	// Neither the auth bytes nor the explicit input strings enter this ledger.
	authInfo, err := os.Lstat(input.AuthFile)
	if err != nil || !authInfo.Mode().IsRegular() || authInfo.Mode().Perm()&0o077 != 0 {
		t.Fatal("protected explicit auth file required")
	}
	auth, err := os.ReadFile(input.AuthFile)
	if err != nil {
		t.Fatal("read private auth")
	}
	authPath := filepath.Join(fixture.CodexHome, "auth.json")
	if err := os.WriteFile(authPath, auth, 0o600); err != nil {
		t.Fatal(err)
	}
	auth = nil
	defer func() {
		if err := os.Remove(authPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("remove private copied auth: %v", err)
		}
		_, err := os.Lstat(authPath)
		ledger.AuthRemoved = errors.Is(err, os.ErrNotExist)
	}()
	configuration := "model = " + strconv.Quote(input.Model) + "\napproval_policy = \"never\"\nsandbox_mode = \"read-only\"\n"
	if err := os.WriteFile(filepath.Join(fixture.CodexHome, "config.toml"), []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	run := installedRecoveryCommand(t, ctx)
	runtime := setupInstalledRecoveryRuntime(t, fixture, input.Binary, run)
	socketName, tmuxSocket, project, window := runtime.Name, runtime.Socket, runtime.Project, runtime.Window
	ledger.TmuxSocket = tmuxSocket
	save()

	var activeAttempt *installedRecoveryAttempt
	var activeGeneration string
	var activeRetired *coremetadata.CodexAuthorityRef
	defer func() {
		if t.Failed() && activeAttempt != nil {
			failureCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			defer stop()
			if activeAttempt.AgentUID != "" {
				activeAttempt.observe(observeInstalledRecovery(failureCtx, activeAttempt.AgentUID, activeGeneration, activeRetired, tmuxSocket))
			}
			identity := installedRecoveryAgent{Agent: activeAttempt.AgentUID}
			if n := len(activeAttempt.Observations); n > 0 {
				identity = activeAttempt.Observations[n-1].Identity
			}
			activeAttempt.Failure = captureInstalledRecoveryFailure(failureCtx, fixture, tmuxSocket, identity)
			save()
		}
	}()
	var daemon *codexinstalled.ManagedDaemon
	agents := []string{}
	var survivor installedRecoveryAgent
	var previous coremetadata.CodexAuthorityRef
	names := []string{"baseline-0.151.0", "upgrade-0.154.0", "restart-1", "restart-2", "restart-3"}
	for rowIndex, name := range names {
		if daemon != nil {
			if err := daemon.Stop(ctx); err != nil {
				t.Fatal(err)
			}
			daemon = nil
		}
		if rowIndex == 1 {
			if err := fixture.SelectManagedRelease(input.Releases[1]); err != nil {
				t.Fatal(err)
			}
			fixture.ApplyEnv(t.Setenv)
		}
		expectedVersion := "0.154.0"
		if rowIndex == 0 {
			expectedVersion = "0.151.0"
		}
		if fixture.Versions().Managed != expectedVersion {
			t.Fatal("managed matrix release order is not 0.151.0 then 0.154.0")
		}
		daemon, err = fixture.StartManagedRecovery(ctx, isolation)
		if err != nil {
			t.Fatal(err)
		}
		ledger.Rows = append(ledger.Rows, installedRecoveryRow{Name: name, Manager: daemon.Proof})
		save()
		for sample := range 2 {
			inputIndex := rowIndex*2 + sample
			sampleName := "survivor"
			if sample == 1 {
				sampleName = "control"
			}
			attempt := &installedRecoveryAttempt{InputIndex: inputIndex, Sample: sampleName, Stage: "waiting-authority"}
			activeAttempt = attempt
			activeGeneration = "codex-" + daemon.Proof.Version
			activeRetired = nil
			ledger.Rows[rowIndex].Attempts = append(ledger.Rows[rowIndex].Attempts, attempt)
			save()
			var agentUID, expectedTurn string
			if rowIndex == 0 || sample == 1 {
				attempt.Stage = "submitting-create"
				agentUID, err = submitInstalledRecoveryInput(&ledger, attempt, observeInstalledConnectionSelection(fixture, daemon.Proof), save, func() string {
					return run(input.Binary, "create", "agent", "--provider", "codex", "--project", "uid:"+project, "--window", "uid:"+window, "--name", fmt.Sprintf("matrix-%d-%d", rowIndex, sample), "-o", "uid", "--", input.Inputs[inputIndex])
				})
				if err != nil {
					t.Fatal(err)
				}
				agents = append(agents, agentUID)
			} else {
				agentUID = survivor.Agent
			}
			attempt.AgentUID = agentUID
			attempt.Stage = "waiting-authority"
			save()
			var retired *coremetadata.CodexAuthorityRef
			if sample == 0 && rowIndex > 0 {
				retired = &previous
				activeRetired = retired
			}
			observed := waitInstalledRecoveryAuthority(t, ctx, agentUID, "codex-"+daemon.Proof.Version, retired, tmuxSocket, attempt, save)
			if observed.Project != project || observed.Window != window {
				t.Fatal("fixture Agent escaped exact project/window")
			}
			if sample == 0 && rowIndex > 0 {
				oldStable, newStable := survivor, observed
				oldStable.Authority, newStable.Authority = coremetadata.CodexAuthorityRef{}, coremetadata.CodexAuthorityRef{}
				oldStable.ControlEpoch, newStable.ControlEpoch = "", ""
				if oldStable != newStable {
					t.Fatal("survivor Agent/Pane/activation/session/thread changed")
				}
				if rowIndex > 1 && (observed.Authority.BrokerRuntimeID != previous.BrokerRuntimeID || observed.Authority.ConnectionEpoch <= previous.ConnectionEpoch || observed.Authority.BindingEpoch != previous.BindingEpoch) {
					t.Fatal("server-only restart replaced broker runtime/binding or failed to advance connection epoch")
				}
				attempt.Stage = "submitting-turn"
				receipt, err := submitInstalledRecoveryInput(&ledger, attempt, observeInstalledConnectionSelection(fixture, daemon.Proof), save, func() string {
					return run(input.Binary, "agent", "turn", "start", "uid:"+agentUID, "--", input.Inputs[inputIndex])
				})
				if err != nil {
					t.Fatal(err)
				}
				for field := range strings.FieldsSeq(receipt) {
					if after, ok := strings.CutPrefix(field, "turn="); ok {
						expectedTurn = after
					}
				}
				if expectedTurn == "" {
					t.Fatal("exact control returned no actual turn ID")
				}
			}
			route, err := (defaultCodexNativeThreadController{}).Resolve(ctx, observed.Authority.Endpoint())
			if err != nil {
				t.Fatal(err)
			}
			client, err := openCodexNativeRoute(ctx, route, true)
			if err != nil {
				t.Fatal(err)
			}
			attempt.Stage = "waiting-turn"
			save()
			turn := waitInstalledRecoveryTurn(t, ctx, client, observed.Thread, expectedTurn)
			_ = client.Close()
			ledger.Rows[rowIndex].Turns = append(ledger.Rows[rowIndex].Turns, installedRecoveryTurn{InputIndex: inputIndex, Sample: sampleName, Agent: observed, Turn: turn.TurnID, Status: turn.TurnState})
			attempt.Stage = "completed"
			activeAttempt = nil
			if sample == 0 {
				survivor = observed
				previous = observed.Authority
			}
			save()
		}
	}
	for _, agent := range agents {
		run(input.Binary, "delete", "agent", "uid:"+agent, "--project", "uid:"+project, "--window", "uid:"+window, "--socket", socketName, "--yes")
	}
	if got := run("tmux", "-L", socketName, "display-message", "-p", "-F", "#{socket_path}"); got != tmuxSocket {
		t.Fatal("tmux cleanup socket changed")
	}
	run("tmux", "-S", tmuxSocket, "kill-server")
	if err := daemon.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(authPath); err != nil {
		t.Fatal(err)
	}
	ledger.AuthRemoved = true
	// The normal broker idle path owns its shutdown. No process is guessed or
	// signalled by the conformance harness; a failed drain leaves FAIL evidence.
	waitCtx, stop := context.WithTimeout(ctx, 50*time.Second)
	defer stop()
	for {
		processes, err := fixture.OwnedProcesses()
		if err != nil {
			t.Fatal(err)
		}
		if len(processes) == 0 {
			break
		}
		select {
		case <-waitCtx.Done():
			t.Fatal("private owned processes remain after normal idle drain")
		case <-time.After(200 * time.Millisecond):
		}
	}
	if err := fixture.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(input.Root); err != nil {
		t.Fatal(err)
	}
	ledger.Cleanup = true
	ledger.Result = "PASS"
	save()
}

func waitInstalledRecoveryAuthority(t *testing.T, ctx context.Context, agentUID, generation string, retired *coremetadata.CodexAuthorityRef, socket string, attempt *installedRecoveryAttempt, save func()) installedRecoveryAgent {
	t.Helper()
	deadline, stop := context.WithTimeout(ctx, 90*time.Second)
	defer stop()
	for {
		probeCtx, cancel := context.WithTimeout(deadline, 3*time.Second)
		observed := observeInstalledRecovery(probeCtx, agentUID, generation, retired, socket)
		cancel()
		if attempt.observe(observed) {
			save()
		}
		if observed.Stage == "ready" {
			return observed.Identity
		}
		select {
		case <-deadline.Done():
			attempt.Stage = "authority-timeout"
			save()
			t.Fatalf("exact Agent authority did not recover (stage=%s, reason=%s)", observed.Stage, observed.Error)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func waitInstalledRecoveryTurn(t *testing.T, ctx context.Context, client *codexappserver.Client, thread, expected string) codexappserver.LifecycleSnapshot {
	t.Helper()
	deadline, stop := context.WithTimeout(ctx, 3*time.Minute)
	defer stop()
	for {
		snapshot, err := client.ReadLifecycleSnapshot(deadline, thread)
		if err == nil && snapshot.ThreadID == thread && snapshot.TurnID != "" && (expected == "" || snapshot.TurnID == expected) {
			if snapshot.TurnState == codexappserver.TurnStateCompleted {
				return snapshot
			}
			if snapshot.TurnState != codexappserver.TurnStateInProgress {
				t.Fatalf("actual new validation turn ended as %s", snapshot.TurnState)
			}
		}
		select {
		case <-deadline.Done():
			t.Fatal("actual new validation turn did not complete")
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func newInstalledRecoveryFixture(t *testing.T, root string) *codexinstalled.Fixture {
	t.Helper()
	fixture, err := codexinstalled.NewClean(root)
	if err != nil {
		t.Fatal(err)
	}
	for key, leaf := range map[string]string{"HOME": "home", "XDG_CONFIG_HOME": "config", "XDG_STATE_HOME": "state", "XDG_CACHE_HOME": "cache", "XDG_DATA_HOME": "data", "XDG_RUNTIME_DIR": "runtime", "TMUX_TMPDIR": "tmux"} {
		path := filepath.Join(root, leaf)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("PROJMUX_PROJDIR", fixture.Workspace)
	return fixture
}

func installedRecoveryCommand(t *testing.T, ctx context.Context) func(string, ...string) string {
	return func(executable string, args ...string) string {
		t.Helper()
		callCtx, stop := context.WithTimeout(ctx, 90*time.Second)
		defer stop()
		command := exec.CommandContext(callCtx, executable, args...) // #nosec G204 -- explicit installed fixture executable and argv.
		command.Env = withoutInheritedTmuxEnvironment(os.Environ())
		output, err := command.Output()
		if err != nil {
			t.Fatalf("fixture command %s %s failed: %v (payload/output omitted)", filepath.Base(executable), args[0], err)
		}
		return strings.TrimSpace(string(output))
	}
}

type installedRecoveryRuntime struct {
	Name    string `json:"name"`
	Socket  string `json:"socket"`
	Project string `json:"project"`
	Window  string `json:"window"`
}

func setupInstalledRecoveryRuntime(t *testing.T, fixture *codexinstalled.Fixture, binary string, run func(string, ...string) string) installedRecoveryRuntime {
	t.Helper()
	socketName := "cp1-" + filepath.Base(fixture.Root)
	run("tmux", "-L", socketName, "-f", "/dev/null", "new-session", "-d", "-s", "fixture-bootstrap", "-c", fixture.Workspace)
	tmuxSocket := run("tmux", "-L", socketName, "display-message", "-p", "-F", "#{socket_path}")
	if !strings.HasPrefix(tmuxSocket, os.Getenv("TMUX_TMPDIR")+string(filepath.Separator)) {
		t.Fatal("tmux socket escaped fixture")
	}
	// This is the exact server this fixture just created and proved private.
	// Publish its app ownership before config apply validates that contract.
	run("tmux", "-S", tmuxSocket, "set-option", "-g", tmuxopts.AppGlobal, "1")
	// Outside-tmux public create discovers -L projmux. This fixture-only alias
	// reaches the unique real server; its socket_path and logical marker remain
	// the unique route and all cleanup names that exact physical socket.
	if err := os.Symlink(tmuxSocket, filepath.Join(filepath.Dir(tmuxSocket), "projmux")); err != nil {
		t.Fatal(err)
	}
	generated := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "projmux", "tmux.conf")
	run(binary, "config", "apply", "--config", generated, "--socket", socketName)
	project := run(binary, "create", "project", "--root", fixture.Workspace, "--name", "recovery-matrix", "-o", "uid")
	window := run(binary, "get", "windows", "--project", "uid:"+project, "-o", "uid")
	run(binary, "reconcile", "resources", "--socket", socketName, "--materialize-project", "uid:"+project, "-o", "json")
	return installedRecoveryRuntime{Name: socketName, Socket: tmuxSocket, Project: project, Window: window}
}

func verifyInstalledRecoveryBinary(t *testing.T, binary, head, expectedSHA string) string {
	t.Helper()
	digest, err := codexinstalled.FileSHA256(binary)
	if err != nil || digest != expectedSHA {
		t.Fatal("candidate binary hash mismatch")
	}
	build, err := buildinfo.ReadFile(binary)
	if err != nil {
		t.Fatal("candidate build identity unavailable")
	}
	revision, modified := "", ""
	for _, setting := range build.Settings {
		if setting.Key == "vcs.revision" {
			revision = setting.Value
		}
		if setting.Key == "vcs.modified" {
			modified = setting.Value
		}
	}
	if revision != head || modified != "false" {
		t.Fatal("candidate binary is not the exact clean source head")
	}

	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	testDigest, err := codexinstalled.FileSHA256(testBinary)
	if err != nil {
		t.Fatal(err)
	}
	return testDigest
}
