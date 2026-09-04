package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
	"github.com/crevissepartners/projmux/internal/version"
)

// TestInstalledIsolatedGenerationPinnedEmptyPromptCreateSmoke is retained as
// historical Phase-7 negative safety evidence. Its zero-turn native outcomes
// are not functional create success on the current tuple; Phase 0's maintained
// functional owner is TestInstalledPayloadFreePlainFallbackOutcomeSmoke.
func TestInstalledIsolatedGenerationPinnedEmptyPromptCreateSmoke(t *testing.T) {
	root, enabled, err := codexinstalled.SmokeRoot("PROJMUX_CODEX_PHASE3_SMOKE_ROOT")
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("historical Phase-7 negative fixture; use PROJMUX_CODEX_PHASE0_PAYLOAD_FREE_SMOKE_ROOT for functional fallback")
	}
	t.Skipf("historical Phase-7 zero-turn native fixture is negative safety evidence only (isolated root configured=%t)", strings.TrimSpace(root) != "")
	fixture, err := codexinstalled.NewClean(root)
	if err != nil {
		t.Fatal(err)
	}
	rootRemoved := false
	t.Cleanup(func() {
		if err := fixture.Cleanup(); err != nil {
			t.Errorf("Phase 3 installed fixture cleanup: %v", err)
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
	run := func(executable string, args ...string) string {
		t.Helper()
		command := exec.CommandContext(ctx, executable, args...) // #nosec G204 -- installed executable and structured test argv.
		command.Env = withoutInheritedTmuxEnvironment(os.Environ())
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
	// A fresh XDG config root intentionally has no generated app config. Use
	// the installed binary's public convergence path to bootstrap it before
	// resource materialization; with no live server on this isolated
	// TMUX_TMPDIR, apply writes the config and performs no tmux mutation.
	generatedConfig := filepath.Join(configHome, "projmux", "tmux.conf")
	applyOutput := run(installed, "config", "apply", "--config", generatedConfig, "--socket", "projmux")
	if !strings.Contains(applyOutput, "wrote "+generatedConfig) ||
		!strings.Contains(applyOutput, "skipped reload: no live tmux server -L projmux") {
		t.Fatalf("installed config bootstrap receipt is incomplete: %q", applyOutput)
	}
	if info, statErr := os.Stat(generatedConfig); statErr != nil || !info.Mode().IsRegular() {
		t.Fatalf("installed config bootstrap did not create the exact generated config: info=%v err=%v", info, statErr)
	}

	projectUID := oneLine("project uid", run(installed, "create", "project", "--root", fixture.Workspace, "--name", "phase3-smoke", "-o", "uid"))
	windowUID := oneLine("window uid", run(installed, "get", "windows", "--project", "uid:"+projectUID, "-o", "uid"))
	run(installed, "reconcile", "resources", "--socket", "projmux", "--materialize-project", "uid:"+projectUID, "-o", "json")
	beforeCreate, err := loadResourceRegistry()
	if err != nil {
		t.Fatal(err)
	}
	tmuxSocket := oneLine("isolated tmux socket", run("tmux", "-L", "projmux", "display-message", "-p", "-F", "#{socket_path}"))
	if !strings.HasPrefix(filepath.Clean(tmuxSocket), filepath.Clean(tmuxRoot)+string(filepath.Separator)) {
		t.Fatalf("tmux socket escaped exact cleanup root: %q", tmuxSocket)
	}
	beforeTmuxPanes := run("tmux", "-L", "projmux", "list-panes", "-a", "-F", "#{pane_id}")
	createOutcome, err := runInstalledPayloadFreeCreate(ctx, installed, withoutInheritedTmuxEnvironment(os.Environ()),
		"create", "agent", "--provider", "codex", "--project", "uid:"+projectUID, "--window", "uid:"+windowUID, "-o", "uid")
	if err != nil {
		t.Fatal(err)
	}
	agentUID := createOutcome.AgentUID

	registry, err := loadResourceRegistry()
	if err != nil {
		t.Fatal(err)
	}
	agent, ok := registry.Agent(agentUID)
	if !ok || agent.Spec.Provider != aiModeCodex || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil {
		t.Fatalf("installed Agent identity is incomplete: %#v", agent)
	}
	expectedDomain, err := defaultCodexStateDomainID(os.Getenv, os.UserHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	ref := agent.Status.SessionRef.Codex
	if !createOutcome.PlainReady {
		if agent.Status.Phase != coremetadata.PhaseFailed || agent.Status.PaneRef != "" ||
			agent.Status.Reason != "payload-free-readiness-"+string(createOutcome.Readiness) ||
			ref.ThreadID != createOutcome.ThreadID || ref.SessionID != "" || ref.HasStartedTurn || ref.Endpoint == nil ||
			!ref.Endpoint.Same(createOutcome.Endpoint) || ref.Endpoint.StateDomainID != expectedDomain || ref.Lifecycle == nil ||
			ref.Lifecycle.State != coremetadata.CodexGenerationCurrent {
			t.Fatalf("typed payload-free failure identity drifted: outcome=%+v agent=%#v", createOutcome, agent.Status)
		}
		if len(registry.Agents) != len(beforeCreate.Agents)+1 || len(registry.Panes) != len(beforeCreate.Panes) {
			t.Fatalf("typed payload-free failure synthesized a second identity/lane: agents=%d/%d panes=%d/%d",
				len(registry.Agents), len(beforeCreate.Agents), len(registry.Panes), len(beforeCreate.Panes))
		}
		for _, candidate := range registry.Panes {
			if candidate.Metadata.OwnerRef != nil && candidate.Metadata.OwnerRef.Kind == coremetadata.KindAgent &&
				candidate.Metadata.OwnerRef.UID == agentUID {
				t.Fatalf("typed payload-free failure retained an Agent Pane: %#v", candidate)
			}
		}
		if afterTmuxPanes := run("tmux", "-L", "projmux", "list-panes", "-a", "-F", "#{pane_id}"); afterTmuxPanes != beforeTmuxPanes {
			t.Fatalf("typed payload-free failure launched a TUI/plain lane: before=%q after=%q", beforeTmuxPanes, afterTmuxPanes)
		}
		beforeRetryThreads, err := installedCatalogThreadIDs(ctx, fixture.SocketPath, fixture.Workspace)
		if err != nil {
			t.Fatalf("read provider threads before exact Failed Agent retry: ids=%v err=%v", beforeRetryThreads, err)
		}
		beforeRetryAgent := *agent
		if err := requireInstalledPayloadFreeResumeRefusal(ctx, installed, withoutInheritedTmuxEnvironment(os.Environ()), agentUID); err != nil {
			t.Fatal(err)
		}
		afterRetry, err := loadResourceRegistry()
		if err != nil {
			t.Fatal(err)
		}
		afterRetryAgent, ok := afterRetry.Agent(agentUID)
		if !ok || len(afterRetry.Agents) != len(registry.Agents) || len(afterRetry.Panes) != len(registry.Panes) ||
			!afterRetryAgent.Status.SessionRef.SameConversation(beforeRetryAgent.Status.SessionRef) ||
			afterRetryAgent.Status.Phase != coremetadata.PhaseFailed || afterRetryAgent.Status.PaneRef != "" {
			t.Fatalf("exact Failed Agent retry changed Registry identity/lane: before=%#v after=%#v", beforeRetryAgent.Status, afterRetryAgent)
		}
		afterRetryThreads, err := installedCatalogThreadIDs(ctx, fixture.SocketPath, fixture.Workspace)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(beforeRetryThreads)
		slices.Sort(afterRetryThreads)
		if !slices.Equal(beforeRetryThreads, afterRetryThreads) {
			t.Fatalf("exact Failed Agent retry created another provider thread: before=%v after=%v", beforeRetryThreads, afterRetryThreads)
		}
		if afterRetryTmuxPanes := run("tmux", "-L", "projmux", "list-panes", "-a", "-F", "#{pane_id}"); afterRetryTmuxPanes != beforeTmuxPanes {
			t.Fatalf("exact Failed Agent retry created another TUI/plain lane: before=%q after=%q", beforeTmuxPanes, afterRetryTmuxPanes)
		}
		run("tmux", "-S", tmuxSocket, "kill-server")
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
			t.Fatalf("Phase 3 installed root remains after exact cleanup: %v", err)
		}
		health := endpoint.Health()
		t.Logf("evidence: tuple cli=%s app-server=%s route=default-proxy readiness=%s agent=%s endpoint=%s/%s thread=%s failed=true panes=0 retry-second-thread=0 ambient-lifecycle-mutations=0",
			version.String(), health.RunningVersion, createOutcome.Readiness, agentUID,
			ref.Endpoint.StateDomainID, ref.Endpoint.EndpointGenerationID, ref.ThreadID)
		return
	}

	pane, ok := registry.Pane(agent.Status.PaneRef)
	if !ok || pane.Metadata.OwnerRef == nil || pane.Metadata.OwnerRef.Kind != coremetadata.KindAgent ||
		pane.Metadata.OwnerRef.UID != agentUID || pane.Status.Activation.Codex == nil {
		t.Fatalf("installed Agent/Pane ownership is incomplete: agent=%#v pane=%#v", agent.Status, pane)
	}
	if strings.TrimSpace(ref.ThreadID) == "" || ref.SessionID != "" || ref.HasStartedTurn || ref.Endpoint == nil ||
		ref.Endpoint.StateDomainID != expectedDomain || ref.Lifecycle == nil ||
		ref.Lifecycle.State != coremetadata.CodexGenerationCurrent || pane.Status.Activation.Codex.ThreadID != ref.ThreadID ||
		pane.Status.Activation.Codex.TurnID != "" || pane.Status.Activation.RuntimeID == "" {
		t.Fatalf("payload-free installed identity chain drifted: agent=%#v pane=%#v", agent.Status, pane.Status)
	}
	for projection := range 3 {
		obligation, projected := codexgeneration.ProjectAgentObligation(*agent, false)
		if !projected || obligation.State != codexgeneration.ObligationNoTurn ||
			obligation.EndpointGenerationID != ref.Endpoint.EndpointGenerationID {
			t.Fatalf("no-turn projection %d=%+v projected=%t", projection, obligation, projected)
		}
	}
	if obligation, projected := codexgeneration.ProjectAgentObligation(*agent, true); !projected || obligation.State != codexgeneration.ObligationClosed {
		t.Fatalf("explicit close did not replace no-turn with closed: obligation=%+v projected=%t", obligation, projected)
	}

	// The generation-keyed discovery artifacts are the semantic startup barrier.
	// A turn-free installed thread has no rollout for thread/resume yet, so the
	// broker is expected to remain an unbound producer until the first real TUI
	// input materializes that rollout. It must not manufacture native authority
	// from the current endpoint merely because the exact broker is running.
	discoveryKey, err := codexbroker.NewEndpointKey(ref.Endpoint.StateDomainID, ref.Endpoint.EndpointGenerationID)
	if err != nil {
		t.Fatal(err)
	}
	discovery, err := codexBrokerDiscoveryForEndpoint(filepath.Join(stateHome, "projmux"), discoveryKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := waitInstalledPhase3Condition(ctx, 10*time.Second, func() (bool, error) {
		_, socketErr := os.Lstat(discovery.SocketPath())
		_, recordErr := os.Lstat(discovery.RecordPath())
		return socketErr == nil && recordErr == nil, nil
	}); err != nil {
		t.Fatalf("exact generation broker did not publish its isolated discovery artifacts: %v", err)
	}
	registry, err = loadResourceRegistry()
	if err != nil {
		t.Fatal(err)
	}
	preTurnPane, ok := registry.Pane(agent.Status.PaneRef)
	if !ok || preTurnPane.Status.Activation.Codex == nil || preTurnPane.Status.Activation.Codex.Authority != nil {
		t.Fatalf("turn-free installed Pane acquired unsupported native authority: %#v", preTurnPane.Status.Activation)
	}
	authorityProjection := run("tmux", "-L", "projmux", "display-message", "-p", "-t", pane.Status.Activation.RuntimeID,
		"#{@projmux_codex_authority}\037#{@projmux_codex_authority_reason}\037#{@projmux_codex_authority_epoch}")
	authorityFields := strings.Split(authorityProjection, "\x1f")
	if len(authorityFields) != 3 || authorityFields[0] != codexAuthorityHook ||
		strings.TrimSpace(authorityFields[1]) == "" || strings.TrimSpace(authorityFields[2]) != "" {
		t.Fatalf("turn-free installed Pane native-authority refusal is not exact: %q", authorityProjection)
	}

	expectedTUI, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	expectedTUI, err = filepath.EvalSymlinks(expectedTUI)
	if err != nil {
		t.Fatal(err)
	}
	format := "#{@projmux_pane_uid}\037#{@projmux_pane_owner_kind}\037#{@projmux_pane_owner_uid}\037#{@projmux_ai_agent}\037#{pane_start_command}\037#{socket_path}"
	observed := run("tmux", "-L", "projmux", "display-message", "-p", "-t", pane.Status.Activation.RuntimeID, format)
	fields := strings.Split(observed, "\x1f")
	if len(fields) != 6 || fields[0] != pane.Metadata.UID || fields[1] != string(coremetadata.KindAgent) ||
		fields[2] != agentUID || fields[3] != aiModeCodex || !strings.Contains(fields[4], expectedTUI) ||
		!strings.Contains(fields[4], "resume") || !strings.Contains(fields[4], "--remote") ||
		!strings.Contains(fields[4], "unix://") || !strings.Contains(fields[4], ref.ThreadID) {
		t.Fatalf("installed pinned TUI/Pane receipt is not exact: %q", observed)
	}
	tmuxSocket = filepath.Clean(fields[5])
	if !strings.HasPrefix(tmuxSocket, filepath.Clean(tmuxRoot)+string(filepath.Separator)) {
		t.Fatalf("tmux socket escaped exact cleanup root: %q", tmuxSocket)
	}

	client, _, err := codexappserver.AttachDefaultEndpoint(ctx, version.String(), codexappserver.AttachOptions{
		Timeout: 10 * time.Second, ExperimentalAPI: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := client.ReadCatalogThread(ctx, ref.ThreadID)
	_ = client.Close()
	if err != nil || thread.ID != ref.ThreadID || strings.TrimSpace(thread.RuntimeStatus) == "" {
		t.Fatalf("payload-free thread identity is not readable without turns: thread=%+v err=%v", thread, err)
	}
	registry, err = loadResourceRegistry()
	if err != nil {
		t.Fatal(err)
	}
	noTurnAgent, agentExists := registry.Agent(agentUID)
	noTurnPane, paneExists := registry.Pane(agent.Status.PaneRef)
	if !agentExists || !paneExists || noTurnAgent.Status.SessionRef == nil || noTurnAgent.Status.SessionRef.Codex == nil ||
		noTurnAgent.Status.SessionRef.Codex.ThreadID != ref.ThreadID || noTurnAgent.Status.SessionRef.Codex.HasStartedTurn ||
		noTurnPane.Status.Activation.Codex == nil || noTurnPane.Status.Activation.Codex.ThreadID != ref.ThreadID ||
		noTurnPane.Status.Activation.Codex.TurnID != "" || noTurnPane.Status.Activation.Codex.Authority != nil {
		t.Fatalf("payload-free thread gained a turn or native authority: agent=%#v pane=%#v",
			noTurnAgent.Status, noTurnPane.Status.Activation)
	}

	// Cleanup addresses the previously observed exact tmux socket, then waits
	// for the exact generation-keyed broker artifacts to retire by idle policy.
	run("tmux", "-S", tmuxSocket, "kill-server")
	if err := waitInstalledPhase3Condition(ctx, 40*time.Second, func() (bool, error) {
		_, socketErr := os.Lstat(discovery.SocketPath())
		_, recordErr := os.Lstat(discovery.RecordPath())
		return os.IsNotExist(socketErr) && os.IsNotExist(recordErr), nil
	}); err != nil {
		t.Fatalf("exact generation broker did not retire after Pane close: %v", err)
	}
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
		t.Fatalf("Phase 3 installed root remains after exact cleanup: %v", err)
	}
	health := endpoint.Health()
	t.Logf("evidence: tuple cli=%s app-server=%s route=default-proxy readiness=durable-ready agent=%s pane=%s runtime=%s endpoint=%s/%s thread-present=true turn-present=false pinned-tui=true",
		version.String(), health.RunningVersion, agentUID, pane.Metadata.UID, pane.Status.Activation.RuntimeID,
		ref.Endpoint.StateDomainID, ref.Endpoint.EndpointGenerationID)
}

func waitInstalledPhase3Condition(ctx context.Context, timeout time.Duration, condition func() (bool, error)) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		ready, err := condition()
		if err != nil || ready {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("semantic condition did not become true within %s", timeout)
		case <-ticker.C:
		}
	}
}

// TestInstalledIsolatedRealTmuxTwoAgentReconnectSmoke runs the maintained
// reconnect fixture with the installed projmux binary as an immutable input.
// The fixture carries the exact two-thread endpoint identities and the combined
// app guard owns sibling isolation; this outer smoke adds the installed binary,
// disposable Registry/XDG state, unique real-tmux socket, and exact contained
// cleanup boundary that unit tests cannot provide.
func TestInstalledIsolatedRealTmuxTwoAgentReconnectSmoke(t *testing.T) {
	root := strings.TrimSpace(os.Getenv("PROJMUX_CODEX_RECONNECT_SMOKE_ROOT"))
	if root == "" {
		t.Skip("set PROJMUX_CODEX_RECONNECT_SMOKE_ROOT for the installed real-tmux reconnect smoke")
	}
	root = filepath.Clean(root)
	tmpRoot := filepath.Clean("/tmp")
	if !filepath.IsAbs(root) || root == tmpRoot || !strings.HasPrefix(root, tmpRoot+string(filepath.Separator)) {
		t.Fatalf("reconnect smoke root must be an isolated child of %s", tmpRoot)
	}
	for _, inherited := range []string{"TMUX", "TMUX_PANE"} {
		if _, present := os.LookupEnv(inherited); present {
			t.Fatalf("%s must be removed for the installed real-tmux reconnect smoke", inherited)
		}
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("reconnect smoke root must start empty: entries=%d err=%v", len(entries), err)
	}
	installed, err := exec.LookPath("projmux")
	if err != nil {
		t.Fatalf("installed projmux is required for the reconnect smoke: %v", err)
	}
	installed, err = filepath.Abs(installed)
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(binary))
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repoRoot, "test", "e2e", "codex-lifecycle.sh")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", script)
	command.Dir = repoRoot
	environment := make([]string, 0, len(os.Environ())+3)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "TMUX=") || strings.HasPrefix(entry, "TMUX_PANE=") ||
			strings.HasPrefix(entry, "TMPDIR=") || strings.HasPrefix(entry, "PROJMUX_SMOKE_PREBUILT_BIN=") ||
			strings.HasPrefix(entry, "PROJMUX_SMOKE_EXPECTED_BIN_SHA256=") {
			continue
		}
		environment = append(environment, entry)
	}
	command.Env = append(environment,
		"TMPDIR="+root,
		"PROJMUX_SMOKE_PREBUILT_BIN="+installed,
		"PROJMUX_SMOKE_EXPECTED_BIN_SHA256="+digest,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("installed real-tmux reconnect smoke: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "Codex native lifecycle E2E passed") {
		t.Fatalf("installed reconnect smoke returned no terminal receipt:\n%s", output)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var residual []string
		for _, entry := range entries {
			residual = append(residual, entry.Name())
		}
		t.Fatalf("installed reconnect smoke left residuals below its exact root: %v", residual)
	}
}

// TestInstalledIsolatedBrokerNativeBindingSmoke drives the cutover's product
// lifecycle and control path against a real installed Codex app-server.
//
// It is opt-in through PROJMUX_CODEX_CUTOVER_SMOKE_ROOT and requires a
// contained CODEX_HOME, an isolated state domain, and inherited tmux identity
// stripped, so it can never reach an ambient shared endpoint, an ambient
// runtime, or an ambient tmux server. The endpoint it talks to is a direct
// `codex app-server --listen unix://` under that contained CODEX_HOME, which
// upstream reports as running with no daemon backend: the unmanaged,
// exact-current endpoint this phase widened attach for.
//
// What it proves is the acceptance the fake-endpoint suite cannot: on that
// unmanaged endpoint, the product reaches native ready and steers its own
// exact active turn through the broker's fenced control wire, while the shared
// semantic ledger proves the whole path performed no endpoint lifecycle or
// ambient mutation.
func TestInstalledIsolatedBrokerNativeBindingSmoke(t *testing.T) {
	root, fixture := newInstalledBrokerFixture(t, "PROJMUX_CODEX_CUTOVER_SMOKE_ROOT", "native cutover")
	domain := filepath.Join(root, "state")
	if err := os.MkdirAll(domain, 0o700); err != nil {
		t.Fatal(err)
	}
	discovery, err := codexbroker.NewDiscovery(domain, codexbroker.DefaultEndpointKey)
	if err != nil {
		t.Fatalf("isolated state domain %q is unusable: %v", domain, err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	ledger := fixture.Ledger()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Native ready on the unmanaged endpoint. The attach is the product's own,
	// and requiring unmanaged ownership here is what keeps this test from
	// quietly passing against a daemon-managed ambient endpoint.
	client, health, err := codexappserver.AttachDefaultEndpoint(ctx, version.String(),
		codexappserver.AttachOptions{Timeout: 10 * time.Second, ExperimentalAPI: true})
	if err != nil {
		t.Fatalf("attach isolated endpoint: %v", err)
	}
	authority := codexappserver.AuthorityFor(health)
	t.Logf("evidence: endpoint readiness=%s ownership=%s version=%s attach=%s lifecycle=%s",
		health.EndpointReadiness, health.ManagerOwnership, health.VersionRelation, authority.Attach, authority.Lifecycle)
	if authority.Attach != codexappserver.EndpointAttachAllowed {
		_ = client.Close()
		t.Fatalf("isolated exact-current endpoint refused attach: %+v", authority)
	}
	if health.ManagerOwnership != codexappserver.ManagerUnmanaged {
		_ = client.Close()
		t.Fatalf("ownership = %s, want %s: this smoke must run against an unmanaged endpoint",
			health.ManagerOwnership, codexappserver.ManagerUnmanaged)
	}
	if authority.Lifecycle != codexappserver.DaemonLifecycleAuthorityNone {
		_ = client.Close()
		t.Fatalf("lifecycle authority = %s, want %s for an unmanaged endpoint",
			authority.Lifecycle, codexappserver.DaemonLifecycleAuthorityNone)
	}
	_ = client.Close()

	// The product's own prompted create. It is what materializes the thread's
	// rollout, which is the upstream precondition the broker's pre-turn
	// thread/resume bootstrap needs.
	created, err := codexappserver.StartDefaultThread(ctx, version.String(), workspace, nil,
		"Reply with the single word OK and nothing else.", "gen-smoke")
	if err != nil {
		t.Fatalf("prompted create against the isolated endpoint: %v", err)
	}
	t.Logf("evidence: prompted create thread-present=%v turn-present=%v",
		strings.TrimSpace(created.ThreadID) != "", strings.TrimSpace(created.TurnID) != "")
	if strings.TrimSpace(created.ThreadID) == "" || strings.TrimSpace(created.TurnID) == "" {
		t.Fatalf("prompted create returned no exact thread and turn")
	}
	settleCodexTurn(ctx, t, workspace, created.ThreadID)

	broker, err := codexbroker.NewBroker(codexbroker.Config{
		Opener: codexbroker.DefaultOpener(version.String(), codexappserver.AttachOptions{
			Timeout: 10 * time.Second, ExperimentalAPI: true,
		}),
	})
	if err != nil {
		t.Fatalf("broker: %v", err)
	}
	host, err := codexbroker.StartHost(codexbroker.HostConfig{Discovery: discovery, Broker: broker, IdleTimeout: time.Second})
	if err != nil {
		_ = broker.Close()
		t.Fatalf("publish isolated runtime: %v", err)
	}

	session := newCodexBrokerObserverSessionOn(codexLifecycleIdentity{
		AgentUID: "agent-smoke", PaneUID: "pane-smoke", RuntimeID: "%1",
		Generation: "gen-smoke", ThreadID: created.ThreadID,
	}, workspace, nil, discovery, nil)

	openCtx, openCancel := context.WithTimeout(ctx, 30*time.Second)
	connection, openErr := session.Open(openCtx)
	openCancel()
	if openErr != nil {
		_ = session.Close()
		_ = host.Close()
		_ = broker.Close()
		t.Fatalf("broker binding refused for the created thread: %v", openErr)
	}
	epoch, ok := connection.(*codexBrokerLifecycleEpoch)
	if !ok {
		t.Fatalf("open returned %T", connection)
	}
	t.Logf("evidence: broker binding opened connection-epoch=%d binding-epoch=%d lifecycle-events=%v",
		epoch.fence.Connection, epoch.fence.Binding, epoch.LifecycleEventsAvailable())

	// Start the turn this test will steer through the same fenced wire that
	// carries it, so the steered turn is provably the exact in-progress one and
	// not a race against a turn some other caller may already have finished.
	started, err := epoch.StartExactTurn(ctx, created.ThreadID, "Write the numbers 1 through 400, one per line, and nothing else.")
	if err != nil {
		t.Fatalf("start the turn to steer through the broker epoch: %v", err)
	}
	snapshot, err := epoch.ReadLifecycleSnapshot(ctx, created.ThreadID)
	if err != nil {
		t.Fatalf("read the lifecycle snapshot through the epoch fence: %v", err)
	}
	t.Logf("evidence: fenced snapshot thread-state=%s turn-state=%s turn-matches-started=%v",
		snapshot.ThreadState, snapshot.TurnState, snapshot.TurnID == started.TurnID)
	if snapshot.TurnID != started.TurnID || snapshot.TurnState != codexappserver.TurnStateInProgress {
		t.Fatalf("turn to steer is not the exact in-progress turn: snapshot turn-state=%s matches=%v",
			snapshot.TurnState, snapshot.TurnID == started.TurnID)
	}

	steered, steerErr := epoch.SteerExactTurn(ctx, created.ThreadID, started.TurnID, "Stop at 5 instead.")
	if steerErr != nil {
		t.Fatalf("steer the exact active turn through the broker epoch: %v", steerErr)
	}
	if steered.TurnID != started.TurnID {
		t.Fatalf("steer answered for turn %q, want the exact active turn", steered.TurnID)
	}
	t.Logf("evidence: exact active turn steered through the broker epoch turn-matches=%v", steered.TurnID == started.TurnID)

	if _, err := epoch.InterruptExactTurn(ctx, created.ThreadID, started.TurnID); err != nil {
		t.Fatalf("interrupt the steered turn through the broker epoch: %v", err)
	}

	_ = connection.Close()
	_ = session.Close()
	_ = host.Close()
	_ = broker.Close()
	for _, artifact := range []string{discovery.SocketPath(), discovery.RecordPath()} {
		if _, err := os.Lstat(artifact); err == nil {
			t.Fatalf("runtime left %q behind", filepath.Base(artifact))
		}
	}

	assertInstalledBrokerLedger(t, ledger)
}

// settleCodexTurn waits for the created thread to leave its first turn, so the
// turn this test starts and steers is the only one in progress.
func settleCodexTurn(ctx context.Context, t *testing.T, workspace, threadID string) {
	t.Helper()
	client, _, err := codexappserver.AttachDefaultEndpoint(ctx, version.String(),
		codexappserver.AttachOptions{Timeout: 10 * time.Second, ExperimentalAPI: true})
	if err != nil {
		t.Fatalf("attach to settle the created turn: %v", err)
	}
	defer client.Close()
	if _, err := client.ResumeThread(ctx, threadID, workspace, nil); err != nil {
		t.Fatalf("resume the created thread to settle its turn: %v", err)
	}
	deadline := time.Now().Add(2 * time.Minute)
	for {
		snapshot, err := client.ReadLifecycleSnapshot(ctx, threadID)
		if err != nil {
			t.Fatalf("read the created thread while settling: %v", err)
		}
		if snapshot.TurnState != codexappserver.TurnStateInProgress {
			t.Logf("evidence: created turn settled turn-state=%s thread-state=%s", snapshot.TurnState, snapshot.ThreadState)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("created turn stayed in progress past the settle deadline")
		}
		select {
		case <-ctx.Done():
			t.Fatalf("settling the created turn was cancelled: %v", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// TestInstalledIsolatedRetiredObserverMatrixSmoke is the retirement's final
// operational proof against a real installed Codex app-server.
//
// It is opt-in through PROJMUX_CODEX_RETIREMENT_SMOKE_ROOT and carries the same
// containment as the cutover smoke above: a contained CODEX_HOME, an isolated
// state domain, and inherited tmux identity stripped.
//
// What it proves is the number the per-Agent observer retirement is measured
// by. The retired producer opened one upstream app-server connection per
// managed Agent and owned a private control endpoint for each; two Agents now
// share one broker connection, one runtime, and one set of artifacts, one
// Agent's control traffic leaves the other's epoch untouched, and unbinding
// them both leaves nothing behind.
func TestInstalledIsolatedRetiredObserverMatrixSmoke(t *testing.T) {
	root, fixture := newInstalledBrokerFixture(t, "PROJMUX_CODEX_RETIREMENT_SMOKE_ROOT", "retirement matrix")
	domain := filepath.Join(root, "state")
	if err := os.MkdirAll(domain, 0o700); err != nil {
		t.Fatal(err)
	}
	discovery, err := codexbroker.NewDiscovery(domain, codexbroker.DefaultEndpointKey)
	if err != nil {
		t.Fatalf("isolated state domain %q is unusable: %v", domain, err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	ledger := fixture.Ledger()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Two managed Agents, each with its own exact thread. The prompted create
	// is the product's own, and it is what materializes each thread's rollout.
	type managed struct {
		agentUID   string
		paneUID    string
		runtimeID  string
		generation string
		threadID   string
	}
	agents := []managed{
		{agentUID: "agent-retire-a", paneUID: "pane-retire-a", runtimeID: "%1", generation: "gen-retire-a"},
		{agentUID: "agent-retire-b", paneUID: "pane-retire-b", runtimeID: "%2", generation: "gen-retire-b"},
	}
	for i := range agents {
		created, err := codexappserver.StartDefaultThread(ctx, version.String(), workspace, nil,
			"Reply with the single word OK and nothing else.", agents[i].generation)
		if err != nil {
			t.Fatalf("prompted create for %s: %v", agents[i].agentUID, err)
		}
		if strings.TrimSpace(created.ThreadID) == "" || strings.TrimSpace(created.TurnID) == "" {
			t.Fatalf("prompted create for %s returned no exact thread and turn", agents[i].agentUID)
		}
		agents[i].threadID = created.ThreadID
		settleCodexTurn(ctx, t, workspace, created.ThreadID)
	}
	t.Logf("evidence: managed Agents created=%d distinct-threads=%v",
		len(agents), agents[0].threadID != agents[1].threadID)

	broker, err := codexbroker.NewBroker(codexbroker.Config{
		Opener: codexbroker.DefaultOpener(version.String(), codexappserver.AttachOptions{
			Timeout: 10 * time.Second, ExperimentalAPI: true,
		}),
	})
	if err != nil {
		t.Fatalf("broker: %v", err)
	}
	host, err := codexbroker.StartHost(codexbroker.HostConfig{Discovery: discovery, Broker: broker, IdleTimeout: time.Second})
	if err != nil {
		_ = broker.Close()
		t.Fatalf("publish isolated runtime: %v", err)
	}
	defer func() {
		_ = host.Close()
		_ = broker.Close()
	}()

	sessions := make([]*codexBrokerObserverSession, 0, len(agents))
	epochs := make([]*codexBrokerLifecycleEpoch, 0, len(agents))
	for _, agent := range agents {
		session := newCodexBrokerObserverSessionOn(codexLifecycleIdentity{
			AgentUID: agent.agentUID, PaneUID: agent.paneUID, RuntimeID: agent.runtimeID,
			Generation: agent.generation, ThreadID: agent.threadID,
		}, workspace, nil, discovery, nil)
		sessions = append(sessions, session)
		openCtx, openCancel := context.WithTimeout(ctx, 30*time.Second)
		connection, openErr := session.Open(openCtx)
		openCancel()
		if openErr != nil {
			t.Fatalf("broker binding refused for %s: %v", agent.agentUID, openErr)
		}
		epoch, ok := connection.(*codexBrokerLifecycleEpoch)
		if !ok {
			t.Fatalf("open returned %T", connection)
		}
		epochs = append(epochs, epoch)
	}
	defer func() {
		for i := range sessions {
			_ = epochs[i].Close()
			_ = sessions[i].Close()
		}
	}()

	// One runtime, one upstream connection, two bindings. This is the whole
	// retirement in one reading: the retired producer would be showing two
	// connections and two private control endpoints here.
	stats := dialInstalledSmokeTelemetry(ctx, t, discovery)
	t.Logf("evidence: runtime=%s connections=%d bindings=%d open-attempts=%d clients=%d",
		stats.Runtime, stats.Broker.Connects-stats.Broker.Disconnects, stats.Broker.Bindings,
		stats.Broker.OpenAttempts, stats.Host.LiveSessions)
	if open := stats.Broker.Connects - stats.Broker.Disconnects; open != 1 {
		t.Fatalf("open upstream connections = %d for %d managed Agents, want exactly 1", open, len(agents))
	}
	if stats.Broker.Bindings != len(agents) {
		t.Fatalf("bindings = %d, want one per managed Agent (%d)", stats.Broker.Bindings, len(agents))
	}
	if stats.Broker.OpenAttempts != 1 {
		t.Fatalf("upstream open attempts = %d, want the single shared connection", stats.Broker.OpenAttempts)
	}
	if stats.Runtime != host.RuntimeID() {
		t.Fatalf("telemetry runtime = %q, want the single published runtime %q", stats.Runtime, host.RuntimeID())
	}

	// The projected diagnostic is what an operator actually reads.
	projected := projectCodexBrokerTelemetry(stats)
	if projected.State != codexBrokerStateRunning || projected.Connections != 1 || projected.Bindings != len(agents) {
		t.Fatalf("projected diagnostic = %+v, want one running connection with one binding per Agent", projected)
	}
	if projected.Evictions != 0 || projected.SnapshotFailures != 0 {
		t.Fatalf("a healthy matrix reported binding faults: %+v", projected)
	}

	// Control on one Agent must leave the other's epoch alone. The steered turn
	// is started through the same fenced wire that carries it, so it is
	// provably the exact in-progress turn of that exact Agent.
	beforeFence := epochs[1].fence
	started, err := epochs[0].StartExactTurn(ctx, agents[0].threadID,
		"Write the numbers 1 through 400, one per line, and nothing else.")
	if err != nil {
		t.Fatalf("start the turn to steer on %s: %v", agents[0].agentUID, err)
	}
	snapshot, err := epochs[0].ReadLifecycleSnapshot(ctx, agents[0].threadID)
	if err != nil {
		t.Fatalf("read the lifecycle snapshot for %s: %v", agents[0].agentUID, err)
	}
	if snapshot.TurnID != started.TurnID || snapshot.TurnState != codexappserver.TurnStateInProgress {
		t.Fatalf("turn to steer is not the exact in-progress turn of %s: turn-state=%s matches=%v",
			agents[0].agentUID, snapshot.TurnState, snapshot.TurnID == started.TurnID)
	}
	steered, err := epochs[0].SteerExactTurn(ctx, agents[0].threadID, started.TurnID, "Stop at 5 instead.")
	if err != nil {
		t.Fatalf("steer the exact active turn of %s: %v", agents[0].agentUID, err)
	}
	if steered.TurnID != started.TurnID {
		t.Fatalf("steer answered for turn %q, want the exact active turn", steered.TurnID)
	}
	if _, err := epochs[0].InterruptExactTurn(ctx, agents[0].threadID, started.TurnID); err != nil {
		t.Fatalf("interrupt the steered turn of %s: %v", agents[0].agentUID, err)
	}

	// The sibling's authority is untouched by all of that, and its own exact
	// thread still reads back through its own fence.
	if epochs[1].fence != beforeFence {
		t.Fatalf("sibling fence moved from %+v to %+v during the other Agent's turn", beforeFence, epochs[1].fence)
	}
	siblingSnapshot, err := epochs[1].ReadLifecycleSnapshot(ctx, agents[1].threadID)
	if err != nil {
		t.Fatalf("read the sibling lifecycle snapshot through its own fence: %v", err)
	}
	if siblingSnapshot.ThreadID != agents[1].threadID {
		t.Fatalf("sibling snapshot answered for thread %q, want %q", siblingSnapshot.ThreadID, agents[1].threadID)
	}
	t.Logf("evidence: sibling containment fence-unchanged=true thread-matches=%v",
		siblingSnapshot.ThreadID == agents[1].threadID)

	// Releasing one Agent leaves the other bound on the same connection.
	_ = epochs[0].Close()
	_ = sessions[0].Close()
	released := dialInstalledSmokeTelemetry(ctx, t, discovery)
	t.Logf("evidence: after releasing one Agent connections=%d bindings=%d",
		released.Broker.Connects-released.Broker.Disconnects, released.Broker.Bindings)
	if released.Broker.Bindings != len(agents)-1 {
		t.Fatalf("bindings after one release = %d, want %d", released.Broker.Bindings, len(agents)-1)
	}
	if open := released.Broker.Connects - released.Broker.Disconnects; open != 1 {
		t.Fatalf("open upstream connections after one release = %d, want the shared connection kept", open)
	}

	_ = epochs[1].Close()
	_ = sessions[1].Close()
	_ = host.Close()
	_ = broker.Close()
	for _, artifact := range []string{discovery.SocketPath(), discovery.RecordPath()} {
		if _, err := os.Lstat(artifact); err == nil {
			t.Fatalf("runtime left %q behind", filepath.Base(artifact))
		}
	}

	assertInstalledBrokerLedger(t, ledger)
}

// dialInstalledSmokeTelemetry reads the published runtime's content-free
// telemetry over its own local IPC, which is the same path Doctor and Settings
// take.
func dialInstalledSmokeTelemetry(ctx context.Context, t *testing.T, discovery codexbroker.Discovery) codexbroker.RuntimeTelemetry {
	t.Helper()
	conn, err := codexbroker.Dial(ctx, discovery, codexbroker.DialConfig{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("reach the published runtime for telemetry: %v", err)
	}
	defer conn.Close()
	telemetry, err := conn.Stats(ctx)
	if err != nil {
		t.Fatalf("read runtime telemetry: %v", err)
	}
	return telemetry
}

// TestInstalledIsolatedBrokerApprovalLeaseSmoke observes one real upstream
// approval server request end to end.
//
// It is the last observation the fake-endpoint suite cannot make. An approval
// never occurs on a no-turn thread, so every earlier proof of the lease's
// response-once authority was made against a scripted endpoint. Here the
// request is issued by a real Codex app-server, delivered over the broker's
// shared connection, minted into a single-use lease bound to the raw JSON-RPC
// id and both epochs, spent exactly once, and refused on the second attempt.
//
// It is opt-in through PROJMUX_CODEX_APPROVAL_SMOKE_ROOT, whose contained
// CODEX_HOME must be configured to require approval; the decision this test
// sends is the first non-executing one the endpoint itself offers, so nothing
// the model proposed is ever run.
//
// Whether an approval happens at all is the endpoint's decision, not this
// test's: a model that answers in prose escalates nothing. The turn is
// therefore attempted a bounded number of times, and a run that still sees no
// approval says so as a missed observation rather than as a lease failure.
func TestInstalledIsolatedBrokerApprovalLeaseSmoke(t *testing.T) {
	root, fixture := newInstalledBrokerFixture(t, "PROJMUX_CODEX_APPROVAL_SMOKE_ROOT", "approval lease")
	domain := filepath.Join(root, "state")
	if err := os.MkdirAll(domain, 0o700); err != nil {
		t.Fatal(err)
	}
	discovery, err := codexbroker.NewDiscovery(domain, codexbroker.DefaultEndpointKey)
	if err != nil {
		t.Fatalf("isolated state domain %q is unusable: %v", domain, err)
	}
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	ledger := fixture.Ledger()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	created, err := codexappserver.StartDefaultThread(ctx, version.String(), workspace, nil,
		"Reply with the single word OK and nothing else.", "gen-approval")
	if err != nil {
		t.Fatalf("prompted create against the isolated endpoint: %v", err)
	}
	settleCodexTurn(ctx, t, workspace, created.ThreadID)

	broker, err := codexbroker.NewBroker(codexbroker.Config{
		Opener: codexbroker.DefaultOpener(version.String(), codexappserver.AttachOptions{
			Timeout: 10 * time.Second, ExperimentalAPI: true,
		}),
	})
	if err != nil {
		t.Fatalf("broker: %v", err)
	}
	host, err := codexbroker.StartHost(codexbroker.HostConfig{Discovery: discovery, Broker: broker, IdleTimeout: time.Second})
	if err != nil {
		_ = broker.Close()
		t.Fatalf("publish isolated runtime: %v", err)
	}
	defer func() {
		_ = host.Close()
		_ = broker.Close()
	}()

	session := newCodexBrokerObserverSessionOn(codexLifecycleIdentity{
		AgentUID: "agent-approval", PaneUID: "pane-approval", RuntimeID: "%1",
		Generation: "gen-approval", ThreadID: created.ThreadID,
	}, workspace, nil, discovery, nil)
	defer func() { _ = session.Close() }()
	openCtx, openCancel := context.WithTimeout(ctx, 30*time.Second)
	connection, openErr := session.Open(openCtx)
	openCancel()
	if openErr != nil {
		t.Fatalf("broker binding refused for the created thread: %v", openErr)
	}
	epoch, ok := connection.(*codexBrokerLifecycleEpoch)
	if !ok {
		t.Fatalf("open returned %T", connection)
	}
	defer func() { _ = epoch.Close() }()

	// This smoke has one precondition it does not control: the endpoint's model
	// has to decide to escalate. The workspace is sandboxed read-only and the
	// prompt asks for a single write, which is the shape most likely to force
	// an escalation, but a model that answers in prose instead produces no
	// approval at all. That is a missed observation, not a broken lease, so the
	// turn is attempted a bounded number of times before the smoke gives up and
	// says which of the two it was.
	const approvalTurnAttempts = 3
	var (
		started  codexappserver.ControlResult
		envelope codexappserver.ApprovalEnvelope
	)
	for attempt := 1; attempt <= approvalTurnAttempts && envelope.RequestID == ""; attempt++ {
		var startErr error
		started, startErr = epoch.StartExactTurn(ctx, created.ThreadID,
			"Use your shell tool to run exactly this one command and nothing else: printf probe > ./projmux-approval-probe.txt")
		if startErr != nil {
			t.Fatalf("start the turn that should request approval (attempt %d): %v", attempt, startErr)
		}
		// The approval arrives on the broker's own stream, which is what mints
		// the lease. Anything else on that stream is ordinary lifecycle traffic.
		deadline := time.After(2 * time.Minute)
	attemptLoop:
		for {
			select {
			case notification, open := <-epoch.Notifications():
				if !open {
					t.Fatal("the broker stream ended before an approval arrived")
				}
				decoded, recognized, decodeErr := codexappserver.DecodeApprovalEnvelope(notification)
				if decodeErr != nil {
					t.Fatalf("decode the real approval request: %v", decodeErr)
				}
				if recognized {
					envelope = decoded
					break attemptLoop
				}
			case <-deadline:
				t.Logf("evidence: attempt %d of %d produced no approval for turn %s",
					attempt, approvalTurnAttempts, started.TurnID)
				_, _ = epoch.InterruptExactTurn(ctx, created.ThreadID, started.TurnID)
				break attemptLoop
			case <-ctx.Done():
				t.Fatalf("waiting for the approval was cancelled: %v", ctx.Err())
			}
		}
	}
	if envelope.RequestID == "" {
		t.Fatalf("the endpoint's model requested no command approval in %d turns; "+
			"this smoke observes a real approval and cannot manufacture one, so re-run it",
			approvalTurnAttempts)
	}
	t.Logf("evidence: upstream approval kind=%s thread-matches=%v turn-matches=%v raw-id-present=%v decisions=%v",
		envelope.Kind, envelope.ThreadID == created.ThreadID, envelope.TurnID == started.TurnID,
		len(envelope.RawRequestID) > 0, envelope.Decisions)
	if envelope.ThreadID != created.ThreadID || envelope.TurnID != started.TurnID {
		t.Fatalf("approval identity = thread %q turn %q, want the exact bound thread and started turn",
			envelope.ThreadID, envelope.TurnID)
	}
	// The offered set belongs to the endpoint, not to this test: current Codex
	// answers a command approval with accept/cancel, while other shapes offer
	// decline. Pick the first offered decision that executes nothing, because
	// accepting would run whatever the model proposed inside the smoke root.
	decision := codexappserver.ApprovalDecision("")
	for _, safe := range []codexappserver.ApprovalDecision{
		codexappserver.DecisionDecline, codexappserver.DecisionCancel,
	} {
		if slices.Contains(envelope.Decisions, safe) {
			decision = safe
			break
		}
	}
	if decision == "" {
		t.Fatalf("approval offered no non-executing decision: %v", envelope.Decisions)
	}

	result, err := codexappserver.ApprovalResponse(envelope, decision)
	if err != nil {
		t.Fatalf("build the %s response: %v", decision, err)
	}
	if err := epoch.RespondServerRequest(ctx, envelope.RawRequestID, result); err != nil {
		t.Fatalf("answer the real approval through the broker lease with %s: %v", decision, err)
	}
	// The lease is single use. A second answer for the same raw id must be
	// refused by the epoch before it can reach the wire again.
	if err := epoch.RespondServerRequest(ctx, envelope.RawRequestID, result); err == nil {
		t.Fatal("a spent approval lease answered a second time")
	}
	t.Logf("evidence: approval lease spent once with decision=%s and refused on the second answer", decision)

	if _, err := epoch.InterruptExactTurn(ctx, created.ThreadID, started.TurnID); err != nil {
		t.Logf("interrupt after the %s approval: %v", decision, err)
	}
	if _, err := os.Lstat(filepath.Join(workspace, "projmux-approval-probe.txt")); err == nil {
		t.Fatalf("the command answered with %s wrote its file anyway", decision)
	}

	assertInstalledBrokerLedger(t, ledger)
}

func newInstalledBrokerFixture(t *testing.T, envName, label string) (string, *codexinstalled.Fixture) {
	t.Helper()
	root, enabled, err := codexinstalled.SmokeRoot(envName)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skipf("set %s for the installed %s smoke", envName, label)
	}
	fixture, err := codexinstalled.NewExisting(root)
	if err != nil {
		t.Fatal(err)
	}
	fixture.ApplyEnv(t.Setenv)
	t.Cleanup(func() {
		if err := fixture.Cleanup(); err != nil {
			t.Errorf("installed %s fixture cleanup: %v", label, err)
		}
	})
	return root, fixture
}

func assertInstalledBrokerLedger(t *testing.T, ledger *codexinstalled.Ledger) {
	t.Helper()
	commands, err := ledger.Commands()
	if err != nil {
		t.Fatal(err)
	}
	operations, err := ledger.DistinctOperations()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("evidence: semantic Codex commands recorded=%d distinct=%v", len(commands), operations)
	if len(commands) == 0 {
		t.Fatal("no semantic Codex command was recorded, so the non-mutation claim is unproven")
	}
	proxyObserved, err := ledger.HasOperation("proxy-session")
	if err != nil {
		t.Fatal(err)
	}
	if !proxyObserved {
		t.Fatal("the semantic ledger never observed the official proxy product path")
	}
	if err := ledger.AssertNoLifecycleMutation(); err != nil {
		t.Fatal(err)
	}
	if err := ledger.AssertNoAmbientMutation(); err != nil {
		t.Fatal(err)
	}
}
