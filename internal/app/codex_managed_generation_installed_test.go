package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbroker"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
	"github.com/crevissepartners/projmux/internal/testutil/codexinstalled"
	"github.com/crevissepartners/projmux/internal/version"
)

// TestInstalledDefaultUpgradeOrdinaryCreatesActivateManagedGeneration is the
// maintained production/default ordering witness for the Phase 6 remediation.
// It requires the installed current CLI plus one exact older standalone
// executable, but contains both state domains, endpoints, tmux, and Registry
// below a caller-owned temporary root.
func TestInstalledDefaultUpgradeOrdinaryCreatesActivateManagedGeneration(t *testing.T) {
	root, enabled, err := codexinstalled.SmokeRoot("PROJMUX_CODEX_PHASE6_REMEDIATION_SMOKE_ROOT")
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skip("set PROJMUX_CODEX_PHASE6_REMEDIATION_SMOKE_ROOT and PROJMUX_CODEX_PHASE6_REMEDIATION_OLD for the installed default-upgrade smoke")
	}
	oldExecutable := filepath.Clean(strings.TrimSpace(os.Getenv("PROJMUX_CODEX_PHASE6_REMEDIATION_OLD")))
	oldInfo, err := os.Stat(oldExecutable)
	if !filepath.IsAbs(oldExecutable) || err != nil || !oldInfo.Mode().IsRegular() || oldInfo.Mode()&0o111 == 0 {
		t.Fatalf("PROJMUX_CODEX_PHASE6_REMEDIATION_OLD must name an absolute executable regular file: %v", err)
	}
	fixture, err := codexinstalled.NewClean(root)
	if err != nil {
		t.Fatal(err)
	}
	rootRemoved := false
	t.Cleanup(func() {
		_ = fixture.Cleanup()
		if !rootRemoved {
			_ = os.RemoveAll(root)
		}
	})
	originalPath := os.Getenv("PATH")
	fixture.ApplyEnv(t.Setenv)
	if result := fixture.ProvisionManagedPayload(); result.Class != codexinstalled.ResultPass {
		t.Fatalf("provision current managed payload: %+v", result)
	}
	// Product lookup must resolve the installed standalone release, not the
	// fixture's command-ledger shim; lifecycle safety is proven below from exact
	// process/socket identity and journal mutation receipts.
	t.Setenv("PATH", originalPath)

	home := filepath.Join(root, "home")
	configHome := filepath.Join(root, "config")
	stateHome := filepath.Join(root, "state")
	runtimeHome := filepath.Join(root, "runtime")
	tmuxRoot := filepath.Join(root, "tmux")
	isolatedTmuxSocket := ""
	t.Cleanup(func() {
		if isolatedTmuxSocket == "" || !strings.HasPrefix(filepath.Clean(isolatedTmuxSocket), filepath.Clean(tmuxRoot)+string(filepath.Separator)) {
			return
		}
		cleanup := exec.Command("tmux", "-S", isolatedTmuxSocket, "kill-server") // #nosec G204 -- exact socket observed below the isolated root.
		cleanup.Env = withoutInheritedTmuxEnvironment(os.Environ())
		_ = cleanup.Run()
	})
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

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	oldEndpoint, started := fixture.StartDirect(ctx, version.String(), oldExecutable)
	if started.Class != codexinstalled.ResultPass {
		t.Fatalf("start old unmanaged default endpoint: %+v", started)
	}
	oldClosed := false
	t.Cleanup(func() {
		if !oldClosed {
			_ = oldEndpoint.Close(context.Background())
		}
	})
	health := oldEndpoint.Health()
	if health.EndpointReadiness != codexappserver.EndpointReady || health.ManagerOwnership != codexappserver.ManagerUnmanaged ||
		health.VersionRelation != codexappserver.VersionSkew || health.ManagedVersion == "" || health.RunningVersion == "" ||
		health.ManagedVersion == health.RunningVersion {
		t.Fatalf("isolated default-upgrade topology = %+v", health)
	}
	oldSocketInfo, err := os.Lstat(fixture.SocketPath)
	if err != nil || oldSocketInfo.Mode()&os.ModeSocket == 0 {
		t.Fatalf("old endpoint socket identity=%v err=%v", oldSocketInfo, err)
	}
	oldClient, err := codexappserver.OpenPrivateUnix(ctx, fixture.SocketPath, 10*time.Second, version.String(), true)
	if err != nil {
		t.Fatal(err)
	}
	defer oldClient.Close()
	oldThread, err := oldClient.StartThread(ctx, fixture.Workspace, nil)
	if err != nil || strings.TrimSpace(oldThread.ThreadID) == "" {
		t.Fatalf("create old-generation continuity thread = %+v err=%v", oldThread, err)
	}

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
		command := exec.CommandContext(ctx, executable, args...) // #nosec G204 -- installed executable and structured argv.
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
	generatedConfig := filepath.Join(configHome, "projmux", "tmux.conf")
	run(installed, "config", "apply", "--config", generatedConfig, "--socket", "projmux")
	projectUID := oneLine("project uid", run(installed, "create", "project", "--root", fixture.Workspace, "--name", "phase6-remediation", "-o", "uid"))
	windowUID := oneLine("window uid", run(installed, "get", "windows", "--project", "uid:"+projectUID, "-o", "uid"))
	run(installed, "reconcile", "resources", "--socket", "projmux", "--materialize-project", "uid:"+projectUID, "-o", "json")
	isolatedTmuxSocket = oneLine("isolated tmux socket", run("tmux", "-L", "projmux", "display-message", "-p", "-F", "#{socket_path}"))
	if !strings.HasPrefix(filepath.Clean(isolatedTmuxSocket), filepath.Clean(tmuxRoot)+string(filepath.Separator)) {
		t.Fatalf("isolated tmux socket escaped root: %q", isolatedTmuxSocket)
	}
	anchorRuntimeID := oneLine("anchor Pane runtime", run("tmux", "-L", "projmux", "list-panes", "-a", "-F", "#{pane_id}"))
	oldRuntimeID := oneLine("old Agent Pane runtime", run("tmux", "-L", "projmux", "split-window", "-d", "-t", anchorRuntimeID,
		"-c", fixture.Workspace, "-P", "-F", "#{pane_id}", "tail", "-f", "/dev/null"))
	paths, err := config.DefaultPathsFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	stateDomainID, err := defaultCodexStateDomainID(os.Getenv, os.UserHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	oldRef := coremetadata.CodexEndpointRef{StateDomainID: stateDomainID, EndpointGenerationID: "codex-" + health.RunningVersion}
	registryStore := intmetadata.NewDefaultStore(paths)
	managedStopped := false
	t.Cleanup(func() {
		if managedStopped {
			return
		}
		journal, exists, loadErr := codexupgrade.NewStateStore(paths.StateDir).Load()
		if loadErr != nil || !exists || journal.Operation == nil {
			return
		}
		target := coremetadata.CodexEndpointRef{StateDomainID: journal.StateDomainID, EndpointGenerationID: journal.Operation.TargetGenerationID}
		route, found := journal.Route(target)
		if !found {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if route.Ready && route.Proof != nil {
			_ = codexgenerationhost.StopDurableGeneration(cleanupCtx, route.Config.HostConfig(), route.LaunchOperationRef, *route.Proof)
			return
		}
		_, _ = codexgenerationhost.CleanupDurableCandidate(cleanupCtx, route.Config.HostConfig(), route.LaunchOperationRef, route.Proof)
	})
	mutator := intmetadata.DefaultMutator()
	var oldAgentBefore coremetadata.Agent
	var oldPaneBefore coremetadata.Pane
	if _, changed, err := registryStore.UpdateConvergent(func(registry *coremetadata.Registry) error {
		agent, createErr := mutator.CreateAgent(registry, windowUID, coremetadata.CreateAgentOptions{
			Name: "old-codex", Provider: aiModeCodex, Workspace: coremetadata.AgentWorkspace{CWD: fixture.Workspace},
			OperationID: "phase6-remediation-old-agent",
		})
		if createErr != nil {
			return createErr
		}
		pane, attachErr := mutator.AttachAgentPane(registry, agent.Metadata.UID, coremetadata.BootstrapPane{CWD: fixture.Workspace}, "phase6-remediation-old-agent")
		if attachErr != nil {
			return attachErr
		}
		const paneGeneration = "phase6-remediation-old-pane"
		if _, activateErr := mutator.RecordPaneActivation(registry, pane.Metadata.UID, coremetadata.PaneActivationOptions{
			Generation: paneGeneration, RuntimeID: oldRuntimeID, AgentUID: agent.Metadata.UID, OperationID: "phase6-remediation-old-agent",
		}); activateErr != nil {
			return activateErr
		}
		if stageErr := mutator.StageCodexEndpoint(registry, agent.Metadata.UID, oldRef); stageErr != nil {
			return stageErr
		}
		if _, bindErr := mutator.BindCodexActivation(registry, coremetadata.CodexActivationObservation{
			AgentUID: agent.Metadata.UID, PaneUID: pane.Metadata.UID, Generation: paneGeneration,
			ThreadID: oldThread.ThreadID, Endpoint: oldRef,
		}); bindErr != nil {
			return bindErr
		}
		if _, interactionErr := mutator.SetAgentInteraction(registry, agent.Metadata.UID, coremetadata.InteractionIdle,
			string(coremetadata.InteractionSourceLifecycle)); interactionErr != nil {
			return interactionErr
		}
		storedAgent, agentOK := registry.Agent(agent.Metadata.UID)
		storedPane, paneOK := registry.Pane(pane.Metadata.UID)
		if !agentOK || !paneOK {
			return errors.New("seeded old Agent/Pane disappeared")
		}
		oldAgentBefore, oldPaneBefore = *storedAgent, *storedPane
		return nil
	}); err != nil || !changed {
		t.Fatalf("seed old Projmux Agent carrier changed=%t err=%v", changed, err)
	}
	for key, value := range map[string]string{
		tmuxopts.PaneUID: oldPaneBefore.Metadata.UID, tmuxopts.AgentUIDPane: oldAgentBefore.Metadata.UID,
		tmuxopts.PaneOwnerKind: string(coremetadata.KindAgent), tmuxopts.PaneOwnerUID: oldAgentBefore.Metadata.UID,
		tmuxopts.AgentThreadIDPane: oldThread.ThreadID, "@projmux_codex_authority": "provider-hook",
	} {
		run("tmux", "-L", "projmux", "set-option", "-p", "-t", oldRuntimeID, key, value)
	}
	beforeCreates, err := registryStore.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	beforeCreateTmuxPanes := run("tmux", "-L", "projmux", "list-panes", "-a", "-F", "#{pane_id}")
	createArgs := [][]string{
		{"create", "codex", "--project", "uid:" + projectUID, "--window", "uid:" + windowUID, "-o", "uid"},
		{"create", "agent", "--provider", "codex", "--project", "uid:" + projectUID, "--window", "uid:" + windowUID, "-o", "uid"},
	}
	createOutcomes := make([]installedPayloadFreeCreateOutcome, 0, len(createArgs))
	for _, args := range createArgs {
		outcome, createErr := runInstalledPayloadFreeCreate(ctx, installed, withoutInheritedTmuxEnvironment(os.Environ()), args...)
		if createErr != nil {
			t.Fatal(createErr)
		}
		createOutcomes = append(createOutcomes, outcome)
	}
	if createOutcomes[0].DurableReady != createOutcomes[1].DurableReady {
		t.Fatalf("ordinary create spellings observed mixed readiness on one exact tuple: %+v", createOutcomes)
	}
	agentUIDs := []string{createOutcomes[0].AgentUID, createOutcomes[1].AgentUID}

	journal, exists, err := codexupgrade.NewStateStore(paths.StateDir).Load()
	if err != nil || !exists || journal.Qualification != nil || len(journal.Routes) != 2 || journal.Operation == nil ||
		!journal.Operation.AdmissionCommitted || !journal.Operation.DrainPublished || journal.Operation.Mutations.OldEndpointStop != 0 ||
		journal.Operation.Mutations.ForeignAdoption != 0 || journal.Operation.Mutations.SuccessorResume != 0 || journal.Operation.Mutations.EndpointRefCAS != 0 {
		t.Fatalf("installed activation journal exists=%t err=%v journal=%+v", exists, err, journal)
	}
	current, ok := journal.CurrentRoute()
	if !ok || current.Generation.Owner != codexgeneration.OwnerProjmuxPrivate || current.Generation.State != codexgeneration.StateCurrent ||
		current.Version != health.ManagedVersion || current.Config.StateDomainPath != fixture.CodexHome || !current.Ready || current.Proof == nil {
		t.Fatalf("installed managed Current = %+v found=%t", current, ok)
	}
	if oldRef.StateDomainID != journal.StateDomainID {
		t.Fatalf("old Agent state domain=%q journal=%q", oldRef.StateDomainID, journal.StateDomainID)
	}
	oldRoute, ok := journal.Route(oldRef)
	if !ok || oldRoute.Generation.Owner != codexgeneration.OwnerUnmanaged || oldRoute.Generation.State != codexgeneration.StateDraining ||
		oldRoute.Version != health.RunningVersion || oldRoute.Ready || oldRoute.Proof != nil {
		t.Fatalf("installed old Draining route = %+v found=%t", oldRoute, ok)
	}
	registry, err := registryStore.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for index, uid := range agentUIDs {
		agent, ok := registry.Agent(uid)
		if !ok || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || agent.Status.SessionRef.Codex.Endpoint == nil ||
			!agent.Status.SessionRef.Codex.Endpoint.Same(current.Generation.Endpoint) {
			t.Fatalf("installed Agent %s current pin = %#v found=%t", uid, agent, ok)
		}
		if !createOutcomes[index].DurableReady && (agent.Status.Phase != coremetadata.PhaseFailed || agent.Status.PaneRef != "" ||
			agent.Status.Reason != "payload-free-readiness-"+string(createOutcomes[index].Readiness) ||
			agent.Status.SessionRef.Codex.ThreadID != createOutcomes[index].ThreadID ||
			!agent.Status.SessionRef.Codex.Endpoint.Same(createOutcomes[index].Endpoint) || agent.Status.SessionRef.Codex.HasStartedTurn) {
			t.Fatalf("installed typed readiness Agent %s = outcome:%+v status:%#v", uid, createOutcomes[index], agent.Status)
		}
	}
	if !createOutcomes[0].DurableReady {
		if agentUIDs[0] == agentUIDs[1] || createOutcomes[0].ThreadID == createOutcomes[1].ThreadID ||
			len(registry.Agents) != len(beforeCreates.Agents)+2 || len(registry.Panes) != len(beforeCreates.Panes) {
			t.Fatalf("typed ordinary creates synthesized/replaced identity or Pane: outcomes=%+v agents=%d/%d panes=%d/%d",
				createOutcomes, len(registry.Agents), len(beforeCreates.Agents), len(registry.Panes), len(beforeCreates.Panes))
		}
		if afterCreateTmuxPanes := run("tmux", "-L", "projmux", "list-panes", "-a", "-F", "#{pane_id}"); afterCreateTmuxPanes != beforeCreateTmuxPanes {
			t.Fatalf("typed ordinary creates launched a TUI/plain fallback lane: before=%q after=%q", beforeCreateTmuxPanes, afterCreateTmuxPanes)
		}
	}
	oldAgentAfter, agentOK := registry.Agent(oldAgentBefore.Metadata.UID)
	oldPaneAfter, paneOK := registry.Pane(oldPaneBefore.Metadata.UID)
	if !agentOK || !paneOK || oldAgentAfter.Status.Phase != coremetadata.PhaseRunning || oldAgentAfter.Status.PaneRef != oldPaneBefore.Metadata.UID ||
		oldAgentAfter.Status.SessionRef == nil || oldAgentAfter.Status.SessionRef.Codex == nil ||
		!oldAgentAfter.Status.SessionRef.SameConversation(oldAgentBefore.Status.SessionRef) ||
		oldAgentAfter.Status.SessionRef.Codex.Endpoint == nil || !oldAgentAfter.Status.SessionRef.Codex.Endpoint.Same(oldRef) ||
		oldAgentAfter.Status.SessionRef.Codex.HasStartedTurn || oldAgentAfter.Status.SessionRef.Codex.Lifecycle == nil ||
		oldAgentAfter.Status.SessionRef.Codex.Lifecycle.State != coremetadata.CodexGenerationDraining ||
		oldAgentAfter.Status.SessionRef.Codex.Lifecycle.Operation == nil || oldAgentAfter.Status.SessionRef.Codex.Lifecycle.Operation.ID != journal.Operation.OperationRef ||
		oldAgentAfter.Status.Interaction.Kind != coremetadata.InteractionIdle || oldPaneAfter.Status.Activation.RuntimeID != oldRuntimeID ||
		oldPaneAfter.Status.Activation.Generation != oldPaneBefore.Status.Activation.Generation || oldPaneAfter.Status.Activation.Codex == nil ||
		oldPaneAfter.Status.Activation.Codex.ThreadID != oldThread.ThreadID || oldPaneAfter.Status.Activation.Codex.TurnID != "" {
		t.Fatalf("old Projmux Agent continuity changed: before=%+v/%+v after=%+v/%+v", oldAgentBefore, oldPaneBefore, oldAgentAfter, oldPaneAfter)
	}
	if got := run("tmux", "-L", "projmux", "display-message", "-p", "-t", oldRuntimeID, "#{pane_dead}"); got != "0" {
		t.Fatalf("old Agent Pane carrier stopped: pane_dead=%s", got)
	}
	if thread, err := oldClient.ReadCatalogThread(ctx, oldThread.ThreadID); err != nil || thread.ID != oldThread.ThreadID {
		t.Fatalf("old-generation thread continuity = %+v err=%v", thread, err)
	}
	oldSocketAfter, err := os.Lstat(fixture.SocketPath)
	if err != nil || !os.SameFile(oldSocketInfo, oldSocketAfter) {
		t.Fatalf("old endpoint was stopped/restarted/replaced: socket=%v err=%v", oldSocketAfter, err)
	}
	doctor := run(installed, "doctor", "--section", "integrations", "--json", "--verbose")
	for _, want := range []string{`"current_generation_id": "` + current.Generation.Endpoint.EndpointGenerationID + `"`,
		`"state": "current"`, `"state": "draining"`, `"version": "` + health.ManagedVersion + `"`,
		`"version": "` + health.RunningVersion + `"`, `"action": "run-isolated-version-pair-qualification"`, `"doctor_mutations": 0`} {
		if !strings.Contains(doctor, want) {
			t.Fatalf("installed Doctor missing %q:\n%s", want, doctor)
		}
	}
	brokerKey, err := codexbroker.NewEndpointKey(current.Generation.Endpoint.StateDomainID, current.Generation.Endpoint.EndpointGenerationID)
	if err != nil {
		t.Fatal(err)
	}
	brokerDiscovery, err := codexBrokerDiscoveryForEndpoint(paths.StateDir, brokerKey)
	if err != nil {
		t.Fatal(err)
	}
	if createOutcomes[0].DurableReady {
		if _, err := os.Lstat(brokerDiscovery.SocketPath()); err != nil {
			t.Fatalf("current-generation broker socket before cleanup: %v", err)
		}
		if _, err := os.Lstat(brokerDiscovery.RecordPath()); err != nil {
			t.Fatalf("current-generation broker record before cleanup: %v", err)
		}
	} else {
		if _, err := os.Lstat(brokerDiscovery.SocketPath()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("typed readiness failures started a current-generation broker socket: %v", err)
		}
		if _, err := os.Lstat(brokerDiscovery.RecordPath()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("typed readiness failures started a current-generation broker record: %v", err)
		}
	}

	// Cleanup targets only identities observed inside the isolated root.
	run("tmux", "-S", isolatedTmuxSocket, "kill-server")
	if createOutcomes[0].DurableReady {
		if err := waitInstalledPhase3Condition(ctx, 40*time.Second, func() (bool, error) {
			_, socketErr := os.Lstat(brokerDiscovery.SocketPath())
			_, recordErr := os.Lstat(brokerDiscovery.RecordPath())
			return errors.Is(socketErr, os.ErrNotExist) && errors.Is(recordErr, os.ErrNotExist), nil
		}); err != nil {
			t.Fatalf("current-generation broker did not retire after exact Pane cleanup: %v", err)
		}
	}
	if err := codexgenerationhost.StopDurableGeneration(ctx, current.Config.HostConfig(), current.LaunchOperationRef, *current.Proof); err != nil {
		t.Fatalf("stop exact managed generation cleanup: %v", err)
	}
	managedStopped = true
	if err := oldClient.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Fatalf("close old continuity client: %v", err)
	}
	closed := oldEndpoint.Close(ctx)
	oldClosed = true
	if closed.Class != codexinstalled.ResultPass {
		t.Fatalf("close old endpoint cleanup: %+v", closed)
	}
	if err := fixture.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	rootRemoved = true
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installed Phase 6 remediation root remains after exact cleanup: %v", err)
	}
	readiness := "durable-ready"
	if !createOutcomes[0].DurableReady {
		readiness = string(createOutcomes[0].Readiness) + "," + string(createOutcomes[1].Readiness)
	}
	t.Logf("evidence: tuple old=%s current=%s route=private-generation readiness=%s old-agent=%s pane=%s thread=%s new-agents=%s,%s doctor-mutations=0 foreign-lifecycle-mutations=0 replay=0",
		health.RunningVersion, health.ManagedVersion, readiness, oldAgentBefore.Metadata.UID, oldRuntimeID, oldThread.ThreadID, agentUIDs[0], agentUIDs[1])
}
