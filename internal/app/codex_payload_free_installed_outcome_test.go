package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
	"github.com/crevissepartners/projmux/internal/version"
)

type installedPayloadFreeCreateOutcome struct {
	PlainReady bool
	AgentUID   string
	ThreadID   string
	Endpoint   coremetadata.CodexEndpointRef
	Readiness  codexappserver.DurableResumeOutcome
}

var installedPayloadFreeResumeFailurePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^agent resume: native Codex thread preparation failed after provider identity became indeterminate; refusing a second CLI lane: codex app-server response refused: (thread-not-durable|thread-absent|protocol-error) \(code -?[0-9]+\)$`),
	regexp.MustCompile(`^agent resume: the stored Codex thread cannot be resumed natively right now \((generation-unavailable|legacy-generation-unavailable|handover-required)\); refusing to rebind it onto a lane with no native turn control: Codex generation route: (generation-unavailable|legacy-generation-unavailable|handover-required)$`),
}

func runInstalledPayloadFreeCreate(
	ctx context.Context,
	executable string,
	environment []string,
	args ...string,
) (installedPayloadFreeCreateOutcome, error) {
	command := exec.CommandContext(ctx, executable, args...) // #nosec G204 -- installed executable and structured test argv.
	command.Env = environment
	output, runErr := command.CombinedOutput()
	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if !errors.As(runErr, &exitErr) {
			return installedPayloadFreeCreateOutcome{}, fmt.Errorf("payload-free create did not return a process exit: %w", runErr)
		}
		exitCode = exitErr.ExitCode()
	}
	return classifyInstalledPayloadFreeCreateOutput(string(output), exitCode)
}

func classifyInstalledPayloadFreeCreateOutput(output string, exitCode int) (installedPayloadFreeCreateOutcome, error) {
	output = strings.TrimSpace(output)
	if exitCode != 0 {
		return installedPayloadFreeCreateOutcome{}, fmt.Errorf(
			"payload-free create exit=%d is negative safety evidence, never functional success: %s",
			exitCode, installedOutputReceipt(output))
	}
	if fields := strings.Fields(output); len(fields) == 1 {
		if kind, ok := coremetadata.UIDKind(fields[0]); ok && kind == coremetadata.KindAgent {
			return installedPayloadFreeCreateOutcome{PlainReady: true, AgentUID: fields[0]}, nil
		}
	}
	return installedPayloadFreeCreateOutcome{}, fmt.Errorf("successful payload-free create output is not one exact Agent uid: %s", installedOutputReceipt(output))
}

func requireInstalledPayloadFreeResumeRefusal(
	ctx context.Context,
	executable string,
	environment []string,
	agentUID string,
) error {
	if kind, ok := coremetadata.UIDKind(agentUID); !ok || kind != coremetadata.KindAgent {
		return errors.New("exact Agent retry identity is not an Agent uid")
	}
	command := exec.CommandContext(ctx, executable, "agent", "resume", "uid:"+agentUID) // #nosec G204 -- installed executable and exact Agent uid.
	command.Env = environment
	output, runErr := command.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		return fmt.Errorf("exact Agent retry exit is not the closed refusal: err=%v %s", runErr, installedOutputReceipt(string(output)))
	}
	return classifyInstalledPayloadFreeResumeOutput(string(output), exitErr.ExitCode())
}

func classifyInstalledPayloadFreeResumeOutput(output string, exitCode int) error {
	if exitCode != 1 {
		return fmt.Errorf("exact Agent retry exit=%d, want content-free refusal exit 1", exitCode)
	}
	trimmed := strings.TrimSpace(string(output))
	for _, pattern := range installedPayloadFreeResumeFailurePatterns {
		if pattern.MatchString(trimmed) {
			return nil
		}
	}
	return fmt.Errorf("exact Agent retry output is not a content-free no-second-lane refusal: %s", installedOutputReceipt(trimmed))
}

func installedOutputReceipt(output string) string {
	digest := sha256.Sum256([]byte(output))
	return fmt.Sprintf("output-bytes=%d output-sha256=%x", len(output), digest)
}

func installedCatalogThreadIDs(ctx context.Context, socketPath, cwd string) ([]string, error) {
	client, err := codexappserver.OpenPrivateUnix(ctx, socketPath, 10*codexappserver.DefaultProbeTimeout, "installed-phase7-observation", true)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	page, err := client.ListCatalogThreads(ctx, codexappserver.CatalogQuery{CWD: cwd})
	if err != nil {
		return nil, err
	}
	if page.NextCursor != nil {
		return nil, errors.New("isolated payload-free catalog unexpectedly exceeded one page")
	}
	ids := make([]string, 0, len(page.Threads))
	for _, thread := range page.Threads {
		ids = append(ids, thread.ID)
	}
	return ids, nil
}

func TestInstalledPayloadFreeCreateOutputClassificationRequiresFunctionalPlainSuccess(t *testing.T) {
	t.Parallel()
	valid := "Codex payload-free create preserved a content-free failed outcome: agent uid:agent-one thread thread-one endpoint state-one/generation-one readiness deadline; TUI was not launched and retry must use `projmux agent resume uid:agent-one`"
	outcome, err := classifyInstalledPayloadFreeCreateOutput("agent-one", 0)
	if err != nil || !outcome.PlainReady || outcome.AgentUID != "agent-one" {
		t.Fatalf("functional classification=%+v err=%v", outcome, err)
	}
	for _, test := range []struct {
		name     string
		output   string
		exitCode int
	}{
		{name: "arbitrary failure", output: "provider said secret conversation content", exitCode: 1},
		{name: "historical typed failure is negative only", output: valid, exitCode: 1},
		{name: "unknown readiness", output: strings.Replace(valid, "readiness deadline", "readiness provider-secret", 1), exitCode: 1},
		{name: "different retry identity", output: strings.Replace(valid, "uid:agent-one`", "uid:agent-two`", 1), exitCode: 1},
		{name: "typed non-Agent identity", output: strings.ReplaceAll(valid, "agent-one", "pane-one"), exitCode: 1},
		{name: "usage exit", output: valid, exitCode: 2},
		{name: "success with diagnostic", output: "agent-one provider-secret", exitCode: 0},
		{name: "success Pane uid", output: "pane-one", exitCode: 0},
		{name: "success garbage token", output: "garbage", exitCode: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got, err := classifyInstalledPayloadFreeCreateOutput(test.output, test.exitCode); err == nil {
				t.Fatalf("classification accepted non-contract output: %+v", got)
			}
		})
	}
}

// TestInstalledPayloadFreePlainFallbackOutcomeSmoke is the functional
// installed-product owner for Phase 0. It proves one plain Running Agent/Pane,
// zero provider threads, a content-free declaration, and exact socket cleanup
// inside a caller-owned isolated root.
func TestInstalledPayloadFreePlainFallbackOutcomeSmoke(t *testing.T) {
	root, enabled, err := codexinstalled.SmokeRoot("PROJMUX_CODEX_PHASE0_PAYLOAD_FREE_SMOKE_ROOT")
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("set PROJMUX_CODEX_PHASE0_PAYLOAD_FREE_SMOKE_ROOT for the installed Phase 0 payload-free smoke")
	}
	fixture, err := codexinstalled.NewClean(root)
	if err != nil {
		t.Fatal(err)
	}
	rootRemoved := false
	t.Cleanup(func() {
		if err := fixture.Cleanup(); err != nil {
			t.Errorf("Phase 0 installed fixture cleanup: %v", err)
		}
		if !rootRemoved {
			_ = os.RemoveAll(root)
		}
	})
	fixture.ApplyEnv(t.Setenv)

	home := filepath.Join(root, "home")
	configHome := filepath.Join(root, "config")
	stateHome := filepath.Join(root, "state")
	runtimeHome := filepath.Join(root, "runtime")
	tmuxRoot := filepath.Join(root, "tmux")
	for _, dir := range []string{home, configHome, stateHome, runtimeHome, tmuxRoot} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": configHome, "XDG_STATE_HOME": stateHome,
		"XDG_RUNTIME_DIR": runtimeHome, "TMUX_TMPDIR": tmuxRoot,
		"PROJMUX_MANAGED_ROOTS": fixture.Workspace, "PROJMUX_PROJDIR": fixture.Workspace,
		"SHELL": "/bin/sh", "TERM": "xterm-256color",
	} {
		t.Setenv(key, value)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	endpoint, started := fixture.StartDirect(ctx, version.String())
	if started.Class != codexinstalled.ResultPass {
		t.Fatalf("start exact isolated endpoint: %+v", started)
	}
	endpointClosed := false
	t.Cleanup(func() {
		if !endpointClosed {
			_ = endpoint.Close(context.Background())
		}
	})

	installed, err := exec.LookPath("projmux")
	if err != nil {
		t.Fatalf("installed projmux is required: %v", err)
	}
	installed, err = filepath.Abs(installed)
	if err != nil {
		t.Fatal(err)
	}
	environment := withoutInheritedTmuxEnvironment(os.Environ())
	run := func(executable string, args ...string) string {
		t.Helper()
		command := exec.CommandContext(ctx, executable, args...) // #nosec G204 -- installed executable and structured test argv.
		command.Env = environment
		output, runErr := command.CombinedOutput()
		if runErr != nil {
			t.Fatalf("%s %s: %v\n%s", filepath.Base(executable), strings.Join(args, " "), runErr, output)
		}
		return strings.TrimSpace(string(output))
	}
	oneLine := func(label, value string) string {
		t.Helper()
		fields := strings.Fields(value)
		if len(fields) != 1 {
			t.Fatalf("%s=%q, want one exact value", label, value)
		}
		return fields[0]
	}

	generatedConfig := filepath.Join(configHome, "projmux", "tmux.conf")
	run(installed, "config", "apply", "--config", generatedConfig, "--socket", "projmux")
	projectUID := oneLine("project uid", run(installed, "create", "project", "--root", fixture.Workspace, "--name", "phase0-smoke", "-o", "uid"))
	windowUID := oneLine("window uid", run(installed, "get", "windows", "--project", "uid:"+projectUID, "-o", "uid"))
	run(installed, "reconcile", "resources", "--socket", "projmux", "--materialize-project", "uid:"+projectUID, "-o", "json")
	tmuxSocket := oneLine("isolated tmux socket", run("tmux", "-L", "projmux", "display-message", "-p", "-F", "#{socket_path}"))
	if !strings.HasPrefix(filepath.Clean(tmuxSocket), filepath.Clean(tmuxRoot)+string(filepath.Separator)) {
		t.Fatalf("tmux socket escaped exact cleanup root: %q", tmuxSocket)
	}
	tmuxSocket = filepath.Clean(tmuxSocket)
	tmuxClosed := false
	killExactTmux := func() error {
		killCtx, killCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer killCancel()
		command := exec.CommandContext(killCtx, "tmux", "-S", tmuxSocket, "kill-server") // #nosec G204 -- exact observed socket proven under the fixture root.
		command.Env = environment
		output, killErr := command.CombinedOutput()
		if killErr != nil {
			return fmt.Errorf("kill exact isolated tmux socket: %w (%s)", killErr, installedOutputReceipt(string(output)))
		}
		return nil
	}
	t.Cleanup(func() {
		if tmuxClosed {
			return
		}
		if err := killExactTmux(); err != nil {
			t.Errorf("Phase 0 exact tmux cleanup: %v", err)
		}
	})
	beforeRegistry, err := loadResourceRegistry()
	if err != nil {
		t.Fatal(err)
	}
	beforeThreads, err := installedCatalogThreadIDs(ctx, fixture.SocketPath, fixture.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	beforePanes := strings.Fields(run("tmux", "-L", "projmux", "list-panes", "-a", "-F", "#{pane_id}"))

	outcome, err := runInstalledPayloadFreeCreate(ctx, installed, environment,
		"create", "codex", "--project", "uid:"+projectUID, "--window", "uid:"+windowUID, "-o", "uid")
	if err != nil || !outcome.PlainReady {
		t.Fatalf("functional payload-free create outcome=%+v err=%v", outcome, err)
	}
	registry, err := loadResourceRegistry()
	if err != nil {
		t.Fatal(err)
	}
	agent, ok := registry.Agent(outcome.AgentUID)
	if !ok || agent.Spec.Provider != aiModeCodex || agent.Status.Phase != coremetadata.PhaseRunning ||
		agent.Status.PaneRef == "" || agent.Status.SessionRef != nil ||
		!agent.Status.Activation.IsZero() {
		t.Fatalf("installed payload-free Agent is not one usable plain identity: %#v", agent)
	}
	pane, ok := registry.Pane(agent.Status.PaneRef)
	if !ok || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent ||
		pane.Metadata.OwnerRef.UID != agent.Metadata.UID || pane.Status.Activation.Codex != nil ||
		pane.Status.Activation.RuntimeID == "" {
		t.Fatalf("installed payload-free Pane ownership is incomplete: %#v", pane)
	}
	if len(registry.Agents) != len(beforeRegistry.Agents)+1 || len(registry.Panes) != len(beforeRegistry.Panes)+1 {
		t.Fatalf("installed cardinality agents=%d panes=%d, want 1/1",
			len(registry.Agents)-len(beforeRegistry.Agents), len(registry.Panes)-len(beforeRegistry.Panes))
	}
	afterThreads, err := installedCatalogThreadIDs(ctx, fixture.SocketPath, fixture.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(beforeThreads)
	slices.Sort(afterThreads)
	if !slices.Equal(beforeThreads, afterThreads) {
		t.Fatalf("payload-free create mutated provider threads: before=%v after=%v", beforeThreads, afterThreads)
	}
	afterPanes := strings.Fields(run("tmux", "-L", "projmux", "list-panes", "-a", "-F", "#{pane_id}"))
	if len(afterPanes) != len(beforePanes)+1 {
		t.Fatalf("installed live Pane cardinality=%d, want 1", len(afterPanes)-len(beforePanes))
	}
	format := "#{@projmux_pane_uid}\037#{@projmux_pane_owner_uid}\037#{@projmux_codex_authority}\037#{@projmux_codex_authority_reason}\037#{@projmux_codex_native_declared}\037#{pane_start_command}\037#{pane_dead}"
	receipt := run("tmux", "-L", "projmux", "display-message", "-p", "-t", pane.Status.Activation.RuntimeID, format)
	fields := strings.Split(receipt, "\x1f")
	if len(fields) != 7 || fields[0] != pane.Metadata.UID || fields[1] != agent.Metadata.UID ||
		fields[2] != codexAuthorityHook || fields[3] != codexNativeUnexplainedReason ||
		fields[4] != codexNativeDeclaredPayloadFreeFallback || !strings.Contains(fields[5], "exec") ||
		!strings.Contains(fields[5], "codex") || strings.Contains(fields[5], "--remote") ||
		strings.Contains(fields[5], "resume") || fields[6] != "0" {
		t.Fatalf("installed plain Pane receipt is not exact: %q", receipt)
	}
	described := run(installed, "describe", "agent", "uid:"+agent.Metadata.UID)
	if !strings.Contains(described, "LifecycleSource:") || !strings.Contains(described, codexAuthorityHook) ||
		!strings.Contains(described, "LifecycleDeclared:") || !strings.Contains(described, codexNativeDeclaredPayloadFreeFallback) {
		t.Fatalf("installed describe signal is missing:\n%s", described)
	}
	doctor := run(installed, "doctor", "--section", "integrations", "--json", "--verbose")
	if !strings.Contains(doctor, `"payload_free_fallback": 1`) || !strings.Contains(doctor, `"unexplained_hook": 0`) {
		t.Fatalf("installed Doctor signal is missing:\n%s", doctor)
	}

	if err := killExactTmux(); err != nil {
		t.Fatal(err)
	}
	tmuxClosed = true
	closed := endpoint.Close(ctx)
	endpointClosed = true
	if closed.Class != codexinstalled.ResultPass {
		t.Fatalf("close exact isolated endpoint: %+v", closed)
	}
	if err := fixture.Ledger().AssertNoAmbientMutation(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	rootRemoved = true
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("Phase 0 installed root remains after exact cleanup: %v", err)
	}
	t.Logf("evidence: tuple cli=%s payload-free=plain agent=%s pane=%s tmux-socket=%s provider-thread-delta=0 ambient-lifecycle-mutations=0 cleanup=exact-root-removed",
		version.String(), agent.Metadata.UID, pane.Status.Activation.RuntimeID, tmuxSocket)
}

func TestInstalledPayloadFreeResumeOutputClassificationIsStrictAndContentFree(t *testing.T) {
	t.Parallel()
	for _, output := range []string{
		"agent resume: native Codex thread preparation failed after provider identity became indeterminate; refusing a second CLI lane: codex app-server response refused: thread-not-durable (code -32600)",
		"agent resume: the stored Codex thread cannot be resumed natively right now (generation-unavailable); refusing to rebind it onto a lane with no native turn control: Codex generation route: generation-unavailable",
	} {
		if err := classifyInstalledPayloadFreeResumeOutput(output, 1); err != nil {
			t.Fatalf("content-free retry refusal rejected: %v", err)
		}
	}
	for _, test := range []struct {
		output   string
		exitCode int
	}{
		{output: "provider secret conversation content", exitCode: 1},
		{output: "agent resume: native Codex thread preparation failed after provider identity became indeterminate; refusing a second CLI lane: no rollout found for private title", exitCode: 1},
		{output: "", exitCode: 0},
		{output: "usage", exitCode: 2},
	} {
		if err := classifyInstalledPayloadFreeResumeOutput(test.output, test.exitCode); err == nil {
			t.Fatalf("retry classifier accepted a non-contract output at exit=%d", test.exitCode)
		}
	}
}
