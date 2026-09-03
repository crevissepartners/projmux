package codexinstalled

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
	intmetadata "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmuxopts"
)

const (
	journaledHandoverSmokeRootEnv = "PROJMUX_CODEX_PHASE5_SMOKE_ROOT"
	journaledHandoverBinaryEnv    = "PROJMUX_CODEX_PHASE5_PROJMUX"
	journaledHandoverSocket       = "projmux"
)

// TestInstalledPrivateJournaledGenerationHandover is the opt-in Phase 5
// public-path observation. It owns unique state, XDG, tmux, private endpoint,
// and immutable lease roots; the ambient/default endpoint is read-only.
func TestInstalledPrivateJournaledGenerationHandover(t *testing.T) {
	root, enabled, err := SmokeRoot(journaledHandoverSmokeRootEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skipf("set %s for the Phase 5 installed private handover smoke", journaledHandoverSmokeRootEnv)
	}
	if err := validateInheritedEnvironment(); err != nil {
		t.Fatal(err)
	}
	projmux := strings.TrimSpace(os.Getenv(journaledHandoverBinaryEnv))
	if projmux == "" {
		projmux, err = exec.LookPath("projmux")
		if err != nil {
			t.Fatalf("set %s to the installed Projmux binary: %v", journaledHandoverBinaryEnv, err)
		}
	}
	projmux, err = filepath.Abs(projmux)
	if err != nil {
		t.Fatal(err)
	}
	oldBinary := exactGenerationBinary(t, generationOldEnv, "0.152.0")
	newBinary := exactGenerationBinary(t, generationNewEnv, "0.152.1")
	stateSource := filepath.Clean(strings.TrimSpace(os.Getenv(generationStateEnv)))
	if !filepath.IsAbs(stateSource) {
		t.Fatalf("%s must be absolute", generationStateEnv)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if entries, readErr := os.ReadDir(root); readErr != nil || len(entries) != 0 {
		t.Fatalf("Phase 5 smoke root must start empty: entries=%d err=%v", len(entries), readErr)
	}
	removed := false
	t.Cleanup(func() {
		if !removed {
			if cleanupErr := os.RemoveAll(root); cleanupErr != nil {
				t.Errorf("remove exact Phase 5 smoke root: %v", cleanupErr)
			}
		}
	})

	ambient := captureAmbientEndpoint(t, stateSource)
	stateDomain := filepath.Join(root, "state-domain")
	oldRoot, newRoot := filepath.Join(root, "generation-old"), filepath.Join(root, "generation-new")
	stateHome, configHome := filepath.Join(root, "xdg-state"), filepath.Join(root, "xdg-config")
	tmuxRoot, workspace := filepath.Join(root, "tmux"), filepath.Join(root, "workspace")
	for _, path := range []string{stateDomain, oldRoot, newRoot, stateHome, configHome, tmuxRoot, workspace} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	copySharedCodexConfig(t, stateSource, stateDomain)
	protocol := codexbundle.ProtocolRange{Min: 2, Max: 2}
	bundleStore := filepath.Join(root, "bundles")
	oldLease := leaseInstalledBundle(t, bundleStore, oldBinary, "0.152.0", protocol)
	newLease := leaseInstalledBundle(t, bundleStore, newBinary, "0.152.1", protocol)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	oldEndpoint := metadata.CodexEndpointRef{StateDomainID: "installed-phase5", EndpointGenerationID: "generation-0.152.0"}
	newEndpoint := metadata.CodexEndpointRef{StateDomainID: "installed-phase5", EndpointGenerationID: "generation-0.152.1"}
	oldConfig := codexgenerationhost.PrivateGenerationConfig{Endpoint: codexgenerationhost.EndpointIdentity{
		StateDomainID: oldEndpoint.StateDomainID, EndpointGenerationID: oldEndpoint.EndpointGenerationID},
		StateDomainPath: stateDomain, PrivateRoot: oldRoot, SocketPath: filepath.Join(oldRoot, "codex-generation-0.152.0.sock"),
		LeaseRoot: oldLease.Root, RequiredProtocol: protocol}
	var oldProof codexgenerationhost.LaunchProof
	if err := codexgenerationhost.PrepareDurableGeneration(ctx, oldConfig, "installed-phase5-old-launch", nil, nil,
		func(proof codexgenerationhost.LaunchProof) error { oldProof = proof; return nil }); err != nil {
		t.Fatal(err)
	}
	oldStopped := false
	t.Cleanup(func() {
		if !oldStopped {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cleanupCancel()
			if cleanupErr := codexgenerationhost.StopDurableGeneration(cleanupCtx, oldConfig, "installed-phase5-old-launch", oldProof); cleanupErr != nil {
				t.Errorf("stop exact old generation after assertion failure: %v", cleanupErr)
			}
		}
	})
	oldClient, err := codexappserver.OpenPrivateUnix(ctx, oldConfig.SocketPath, 10*time.Second, "installed-phase5", true)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := oldClient.StartThread(ctx, workspace, nil)
	if err != nil {
		oldClient.Close()
		t.Fatal(err)
	}
	turnID, err := oldClient.StartTurn(ctx, completed.ThreadID, "Reply with exactly HANDOVER_OK. Do not use tools.", "installed-phase5-completed")
	if err != nil {
		oldClient.Close()
		t.Fatal(err)
	}
	waitForGenerationTurn(t, ctx, oldClient, completed.ThreadID, turnID)
	noTurn, err := oldClient.StartThread(ctx, workspace, nil)
	if closeErr := oldClient.Close(); err != nil || closeErr != nil {
		t.Fatalf("create no-turn thread=%v close=%v", err, closeErr)
	}

	tmuxEnv := journaledHandoverEnvironment(os.Environ(), stateDomain, stateHome, configHome, tmuxRoot)
	runTmux := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(ctx, "tmux", append([]string{"-L", journaledHandoverSocket}, args...)...) // #nosec G204 -- fixed tmux and internally structured private argv.
		command.Env = tmuxEnv
		output, commandErr := command.CombinedOutput()
		if commandErr != nil {
			t.Fatalf("tmux %s: %v: %s", strings.Join(args, " "), commandErr, output)
		}
		return strings.TrimSpace(string(output))
	}
	completedPane := runTmux("-f", "/dev/null", "new-session", "-d", "-s", "installed-phase5", "-c", workspace,
		"-P", "-F", "#{pane_id}", "tail", "-f", "/dev/null")
	noTurnPane := runTmux("split-window", "-d", "-t", completedPane, "-c", workspace, "-P", "-F", "#{pane_id}", "tail", "-f", "/dev/null")
	tmuxClosed := false
	t.Cleanup(func() {
		if !tmuxClosed {
			cleanup := exec.Command("tmux", "-L", journaledHandoverSocket, "kill-server") // #nosec G204 -- exact private test socket.
			cleanup.Env = tmuxEnv
			_ = cleanup.Run()
		}
	})
	runTmux("set-option", "-g", tmuxopts.AppGlobal, "1")
	runTmux("set-option", "-g", "@projmux_socket_name", journaledHandoverSocket)

	paths := config.DefaultPaths(configHome, stateHome)
	mutator := metadata.Mutator{DirExists: func(path string) (bool, error) {
		info, statErr := os.Stat(path)
		return statErr == nil && info.IsDir(), statErr
	}}
	registry := metadata.NewRegistry()
	project, err := mutator.RegisterProject(&registry, metadata.RegisterProjectOptions{Root: workspace, Name: "installed-phase5", DefaultShell: "/bin/sh", OperationID: "phase5-project"})
	if err != nil {
		t.Fatal(err)
	}
	seedAgent := func(name, runtimeID, threadID, boundTurn string, interaction metadata.AgentInteractionKind) (metadata.Agent, metadata.Pane) {
		t.Helper()
		agent, createErr := mutator.CreateAgent(&registry, project.Windows[0].Metadata.UID, metadata.CreateAgentOptions{
			Name: name, Provider: "codex", Workspace: metadata.AgentWorkspace{CWD: workspace}, OperationID: "phase5-" + name})
		if createErr != nil {
			t.Fatal(createErr)
		}
		pane, attachErr := mutator.AttachAgentPane(&registry, agent.Metadata.UID, metadata.BootstrapPane{CWD: workspace}, "phase5-"+name)
		if attachErr != nil {
			t.Fatal(attachErr)
		}
		generation := "pane-generation-" + name
		if _, activateErr := mutator.RecordPaneActivation(&registry, pane.Metadata.UID, metadata.PaneActivationOptions{
			Generation: generation, RuntimeID: runtimeID, AgentUID: agent.Metadata.UID, OperationID: "phase5-" + name}); activateErr != nil {
			t.Fatal(activateErr)
		}
		if stageErr := mutator.StageCodexEndpoint(&registry, agent.Metadata.UID, oldEndpoint); stageErr != nil {
			t.Fatal(stageErr)
		}
		if _, bindErr := mutator.BindCodexActivation(&registry, metadata.CodexActivationObservation{AgentUID: agent.Metadata.UID,
			PaneUID: pane.Metadata.UID, Generation: generation, ThreadID: threadID, TurnID: boundTurn, Endpoint: oldEndpoint}); bindErr != nil {
			t.Fatal(bindErr)
		}
		if _, interactionErr := mutator.SetAgentInteraction(&registry, agent.Metadata.UID, interaction, string(metadata.InteractionSourceLifecycle)); interactionErr != nil {
			t.Fatal(interactionErr)
		}
		storedAgent, _ := registry.Agent(agent.Metadata.UID)
		storedPane, _ := registry.Pane(pane.Metadata.UID)
		for key, value := range map[string]string{tmuxopts.PaneUID: storedPane.Metadata.UID, tmuxopts.AgentUIDPane: storedAgent.Metadata.UID,
			tmuxopts.PaneOwnerKind: string(metadata.KindAgent), tmuxopts.PaneOwnerUID: storedAgent.Metadata.UID,
			"@projmux_codex_authority": "provider-hook"} {
			runTmux("set-option", "-p", "-t", runtimeID, key, value)
		}
		return *storedAgent, *storedPane
	}
	completedAgent, completedResourcePane := seedAgent("completed", completedPane, completed.ThreadID, turnID, metadata.InteractionResponseComplete)
	noTurnAgent, _ := seedAgent("no-turn", noTurnPane, noTurn.ThreadID, "", metadata.InteractionIdle)
	registryStore := intmetadata.NewDefaultStore(paths)
	if _, _, err := registryStore.UpdateConvergent(func(current *metadata.Registry) error { *current = registry; return nil }); err != nil {
		t.Fatal(err)
	}
	// "selectorless E2E" is a refusal guard, never lookup authority. The
	// installed command must reject the omitted Agent ref before it can infer a
	// Pane, mutate Registry, or touch either exact endpoint.
	registryBeforeSelectorless, err := os.ReadFile(registryStore.Path())
	if err != nil {
		t.Fatal(err)
	}
	panesBeforeSelectorless := runTmux("list-panes", "-a", "-F", "#{pane_id}")
	selectorless := exec.CommandContext(ctx, projmux, "agent", "resume") // #nosec G204 -- explicit installed binary; omitted selector is the negative under test.
	selectorless.Env = tmuxEnv
	selectorlessOutput, selectorlessErr := selectorless.CombinedOutput()
	if selectorlessErr == nil || !strings.Contains(string(selectorlessOutput), "requires one Agent reference") {
		t.Fatalf("selectorless Agent resume was not refused: err=%v output=%s", selectorlessErr, selectorlessOutput)
	}
	registryAfterSelectorless, err := os.ReadFile(registryStore.Path())
	if err != nil || string(registryAfterSelectorless) != string(registryBeforeSelectorless) || runTmux("list-panes", "-a", "-F", "#{pane_id}") != panesBeforeSelectorless {
		t.Fatalf("selectorless refusal changed exact scope: registry err=%v panes before=%q after=%q", err, panesBeforeSelectorless, runTmux("list-panes", "-a", "-F", "#{pane_id}"))
	}
	if err := codexgenerationhost.ObservePrivateGeneration(ctx, oldConfig, oldProof); err != nil {
		t.Fatalf("selectorless refusal changed old endpoint: %v", err)
	}

	qualification := codexgeneration.EvaluateQualification(codexgeneration.VersionPair{Old: "0.152.0", New: "0.152.1"}, codexgeneration.QualificationEvidence{
		SharedStateDomain: true, DistinctPrivateEndpoints: true, DistinctThreadCreateTurn: true, DistinctThreadReadList: true,
		CrashRestart: true, OldStoppedBeforeResume: true, PersistedResumeSnapshot: true, SharedAuthConfigPrivate: true,
		BundleSourceRemovalLaunch: true, BundleDriftRefused: true, ProtocolMismatchRefused: true})
	upgrade := codexupgrade.Request{OperationRef: "installed-phase5-upgrade", Current: codexupgrade.GenerationRoute{
		Generation: codexgeneration.Generation{Endpoint: oldEndpoint, State: codexgeneration.StateCurrent, Owner: codexgeneration.OwnerProjmuxPrivate, BundleID: oldLease.ID},
		Config: codexupgrade.GenerationConfig{Endpoint: oldEndpoint, StateDomainPath: stateDomain, PrivateRoot: oldRoot, SocketPath: oldConfig.SocketPath,
			LeaseRoot: oldLease.Root, RequiredProtocol: protocol}, TUIPath: oldLease.Paths(codexbundle.RoleTUI)[0],
		LaunchOperationRef: "installed-phase5-old-launch", Ready: true, Proof: &oldProof},
		Target: codexupgrade.GenerationConfig{Endpoint: newEndpoint, StateDomainPath: stateDomain, PrivateRoot: newRoot,
			SocketPath: filepath.Join(newRoot, "codex-generation-0.152.1.sock"), LeaseRoot: newLease.Root, RequiredProtocol: protocol},
		TargetBundleID: newLease.ID, TargetTUIPath: newLease.Paths(codexbundle.RoleTUI)[0], Qualification: qualification}
	if err := codexgenerationhost.ObservePrivateGenerationRoute(ctx, oldConfig, oldProof, upgrade.Current.TUIPath); err != nil {
		t.Fatalf("old route unusable before public upgrade: refusal=%s err=%v unwrap=%v",
			codexgenerationhost.HostRefusalOf(err), err, errors.Unwrap(err))
	}
	successorConfig := installedRollingHostConfig(upgrade.Target)
	var successorProof *codexgenerationhost.LaunchProof
	successorCleaned := false
	cleanupSuccessor := func() error {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_, cleanupErr := codexgenerationhost.CleanupDurableCandidate(cleanupCtx, successorConfig, upgrade.OperationRef, successorProof)
		return cleanupErr
	}
	// Register before public upgrade: a failure after the durable launch intent
	// but before proof publication still owns an exact nil-proof cleanup route.
	t.Cleanup(func() {
		if successorCleaned {
			return
		}
		if cleanupErr := cleanupSuccessor(); cleanupErr != nil {
			t.Errorf("exact successor cleanup after assertion failure: %v", cleanupErr)
		}
	})
	upgradePath := writeInstalledJSON(t, root, "upgrade.json", upgrade)
	runInstalledPhase5(t, ctx, projmux, tmuxEnv, "agent", "app-server", "upgrade", "apply", "--request", upgradePath)
	journalStore := codexupgrade.NewStateStore(paths.StateDir)
	upgradeJournal, exists, err := journalStore.Load()
	if err != nil || !exists {
		t.Fatalf("load upgraded journal=(%t,%v)", exists, err)
	}
	newRoute, ok := upgradeJournal.Route(newEndpoint)
	if !ok || newRoute.Proof == nil {
		t.Fatal("successor proof missing immediately after upgrade")
	}
	proof := *newRoute.Proof
	successorProof = &proof
	// Phase 6 full-flow overlap: both admitted generations own a distinct
	// thread/turn before any destructive handover receipt. Starting both turns
	// before either terminal wait is the semantic barrier; exact thread/turn
	// reads below prove that neither endpoint cross-wired the sibling tuple.
	oldOverlapClient, err := codexappserver.OpenPrivateUnix(ctx, oldConfig.SocketPath, 10*time.Second, "installed-phase6-overlap-old", true)
	if err != nil {
		t.Fatal(err)
	}
	newOverlapClient, err := codexappserver.OpenPrivateUnix(ctx, newRoute.Config.SocketPath, 10*time.Second, "installed-phase6-overlap-new", true)
	if err != nil {
		oldOverlapClient.Close()
		t.Fatal(err)
	}
	oldOverlapThread, oldThreadErr := oldOverlapClient.StartThread(ctx, workspace, nil)
	newOverlapThread, newThreadErr := newOverlapClient.StartThread(ctx, workspace, nil)
	if oldThreadErr != nil || newThreadErr != nil || oldOverlapThread.ThreadID == "" || newOverlapThread.ThreadID == "" || oldOverlapThread.ThreadID == newOverlapThread.ThreadID {
		oldOverlapClient.Close()
		newOverlapClient.Close()
		t.Fatalf("distinct overlap threads old=%+v/%v new=%+v/%v", oldOverlapThread, oldThreadErr, newOverlapThread, newThreadErr)
	}
	oldOverlapTurn, oldTurnErr := oldOverlapClient.StartTurn(ctx, oldOverlapThread.ThreadID, "Reply with exactly OLD_OK. Do not use tools.", "installed-phase6-overlap-old")
	newOverlapTurn, newTurnErr := newOverlapClient.StartTurn(ctx, newOverlapThread.ThreadID, "Reply with exactly NEW_OK. Do not use tools.", "installed-phase6-overlap-new")
	if oldTurnErr != nil || newTurnErr != nil || oldOverlapTurn == "" || newOverlapTurn == "" || oldOverlapTurn == newOverlapTurn {
		oldOverlapClient.Close()
		newOverlapClient.Close()
		t.Fatalf("distinct overlap turns old=%q/%v new=%q/%v", oldOverlapTurn, oldTurnErr, newOverlapTurn, newTurnErr)
	}
	oldOverlapSnapshot := waitForGenerationTurn(t, ctx, oldOverlapClient, oldOverlapThread.ThreadID, oldOverlapTurn)
	newOverlapSnapshot := waitForGenerationTurn(t, ctx, newOverlapClient, newOverlapThread.ThreadID, newOverlapTurn)
	if oldOverlapSnapshot.ThreadID != oldOverlapThread.ThreadID || oldOverlapSnapshot.TurnID != oldOverlapTurn ||
		newOverlapSnapshot.ThreadID != newOverlapThread.ThreadID || newOverlapSnapshot.TurnID != newOverlapTurn {
		t.Fatalf("overlap cross-wire old=%+v new=%+v", oldOverlapSnapshot, newOverlapSnapshot)
	}
	if closeOldErr, closeNewErr := oldOverlapClient.Close(), newOverlapClient.Close(); closeOldErr != nil || closeNewErr != nil {
		t.Fatalf("close overlap clients old=%v new=%v", closeOldErr, closeNewErr)
	}
	requester := codexupgrade.Coordinator{Journal: journalStore, Registry: registryStore, Mutator: intmetadata.DefaultMutator}
	requestedRef, created, err := requester.RequestHandover(ctx, oldEndpoint)
	if err != nil || !created || requestedRef != upgrade.OperationRef {
		t.Fatalf("request exact generation handover = (%q,%t,%v), want (%q,true,nil)",
			requestedRef, created, err, upgrade.OperationRef)
	}

	handoverBase := map[string]any{"operationRef": "installed-phase5-handover", "rollingOperationRef": upgrade.OperationRef}
	registryBeforeBlockedPlan, err := os.ReadFile(registryStore.Path())
	if err != nil {
		t.Fatal(err)
	}
	blockedPath := writeInstalledJSON(t, root, "handover-blocked.json", handoverBase)
	blocked := runInstalledPhase5(t, ctx, projmux, tmuxEnv, "agent", "app-server", "handover", "plan", "--request", blockedPath)
	if !strings.Contains(blocked, "unresolved-no-turn:"+noTurnAgent.Metadata.UID) {
		t.Fatalf("no-turn plan did not fail closed: %s", blocked)
	}
	for _, zero := range []string{`"oldEndpointStop": 0`, `"successorResume": 0`, `"successorSnapshot": 0`,
		`"endpointRefCAS": 0`, `"paneRelaunch": 0`, `"retirement": 0`, `"leaseRelease": 0`} {
		if !strings.Contains(blocked, zero) {
			t.Fatalf("blocked no-turn plan missed %s: %s", zero, blocked)
		}
	}
	if err := codexgenerationhost.ObservePrivateGeneration(ctx, oldConfig, oldProof); err != nil {
		t.Fatalf("blocked no-turn plan changed old owner: %v", err)
	}
	for _, paneID := range []string{completedPane, noTurnPane} {
		if got := runTmux("display-message", "-p", "-t", paneID, "#{pane_dead}"); got != "0" {
			t.Fatalf("blocked no-turn plan changed Pane %s: pane_dead=%s", paneID, got)
		}
	}
	registryAfterBlockedPlan, err := os.ReadFile(registryStore.Path())
	if err != nil || string(registryAfterBlockedPlan) != string(registryBeforeBlockedPlan) {
		t.Fatalf("blocked no-turn plan changed Registry: err=%v", err)
	}
	blockedRegistry, err := registryStore.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{completedAgent.Metadata.UID, noTurnAgent.Metadata.UID} {
		agent, found := blockedRegistry.Agent(uid)
		if !found || agent.Status.SessionRef == nil || agent.Status.SessionRef.Codex == nil || agent.Status.SessionRef.Codex.Endpoint == nil ||
			!agent.Status.SessionRef.Codex.Endpoint.Same(oldEndpoint) {
			t.Fatalf("blocked no-turn endpoint drift for %s: %+v", uid, agent)
		}
	}
	successorClient, err := codexappserver.OpenPrivateUnix(ctx, newRoute.Config.SocketPath, 10*time.Second, "installed-phase5-negative", true)
	if err != nil {
		t.Fatal(err)
	}
	blockedSnapshot, snapshotErr := successorClient.ReadLifecycleSnapshot(ctx, completed.ThreadID)
	closeErr := successorClient.Close()
	if snapshotErr != nil || closeErr != nil || blockedSnapshot.ThreadState != codexappserver.ThreadStateNotLoaded {
		t.Fatalf("blocked successor target snapshot=%+v read=%v close=%v", blockedSnapshot, snapshotErr, closeErr)
	}
	blockedJournal, exists, err := journalStore.Load()
	if err != nil || !exists || blockedJournal.Handover != nil {
		t.Fatalf("blocked plan wrote handover journal=(%+v,%t,%v)", blockedJournal.Handover, exists, err)
	}
	handoverBase["choices"] = []map[string]string{{"agentUID": noTurnAgent.Metadata.UID, "decision": "close"}}
	handoverPath := writeInstalledJSON(t, root, "handover.json", handoverBase)
	receipt := runInstalledPhase5(t, ctx, projmux, tmuxEnv, "agent", "app-server", "handover", "apply", "--request", handoverPath)
	if !strings.Contains(receipt, `"phase": "complete"`) || !strings.Contains(receipt, `"successorResume": 1`) ||
		!strings.Contains(receipt, `"successorSnapshot": 1`) || !strings.Contains(receipt, `"endpointRefCAS": 1`) ||
		!strings.Contains(receipt, `"paneRelaunch": 1`) || !strings.Contains(receipt, `"noTurnChoice": 1`) {
		t.Fatalf("installed handover receipt: %s", receipt)
	}
	oldStopped = true
	finalRegistry, err := registryStore.LoadSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	finalAgent, ok := finalRegistry.Agent(completedAgent.Metadata.UID)
	finalPane, paneOK := finalRegistry.Pane(completedResourcePane.Metadata.UID)
	if !ok || !paneOK || finalAgent.Status.PaneRef != completedResourcePane.Metadata.UID || finalAgent.Status.SessionRef == nil ||
		finalAgent.Status.SessionRef.Codex == nil || finalAgent.Status.SessionRef.Codex.Endpoint == nil ||
		!finalAgent.Status.SessionRef.Codex.Endpoint.Same(newEndpoint) || finalAgent.Status.SessionRef.Codex.ThreadID != completed.ThreadID ||
		finalPane.Status.Activation.RuntimeID != completedPane || finalPane.Status.Activation.AgentUID != completedAgent.Metadata.UID {
		t.Fatalf("same Agent/Pane/thread identity was not retained: agent=%+v pane=%+v", finalAgent, finalPane)
	}
	if _, exists := finalRegistry.Agent(noTurnAgent.Metadata.UID); exists {
		t.Fatal("explicit no-turn close retained the old Agent identity")
	}
	journal, exists, err := journalStore.Load()
	if err != nil || !exists || journal.Handover == nil || journal.Handover.Phase != codexgeneration.HandoverComplete {
		t.Fatalf("terminal journal=(%+v,%t,%v)", journal.Handover, exists, err)
	}
	newRoute, ok = journal.Route(newEndpoint)
	if !ok || newRoute.Proof == nil {
		t.Fatal("successor proof missing")
	}
	if _, statErr := os.Lstat(oldLease.Root); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("terminal old lease remains: %v", statErr)
	}
	markerAndCommand := runTmux("display-message", "-p", "-t", completedPane,
		"#{@projmux_codex_handover_operation}\x1f#{@projmux_codex_handover_generation}\x1f#{pane_start_command}")
	if !strings.HasPrefix(markerAndCommand, "installed-phase5-handover\x1fhandover-") ||
		!strings.Contains(markerAndCommand, newRoute.TUIPath) || !strings.Contains(markerAndCommand, completed.ThreadID) {
		t.Fatalf("same Pane does not carry successor bundle/thread relaunch: %q", markerAndCommand)
	}
	if _, statErr := os.Lstat(oldConfig.SocketPath); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("old socket remains after terminal handover: %v", statErr)
	}
	if signalErr := syscall.Kill(oldProof.PID, 0); signalErr == nil || !errors.Is(signalErr, syscall.ESRCH) {
		t.Fatalf("old process remains after terminal handover: pid=%d err=%v", oldProof.PID, signalErr)
	}
	if err := cleanupSuccessor(); err != nil {
		t.Fatalf("successor cleanup: %v", err)
	}
	successorCleaned = true
	runTmux("kill-server")
	tmuxClosed = true
	assertAmbientEndpointUnchanged(t, ambient)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	removed = true
}

func writeInstalledJSON(t *testing.T, root, name string, value any) string {
	t.Helper()
	path := filepath.Join(root, name)
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func runInstalledPhase5(t *testing.T, ctx context.Context, binary string, environment []string, args ...string) string {
	t.Helper()
	command := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- explicit installed binary and closed public CLI argv.
	command.Env = environment
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("installed projmux %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func journaledHandoverEnvironment(environment []string, codexHome, stateHome, configHome, tmuxRoot string) []string {
	out := phase4InstalledEnvironment(environment, codexHome, stateHome, configHome)
	filtered := out[:0]
	for _, entry := range out {
		key, _, _ := strings.Cut(entry, "=")
		if key != "TMUX_TMPDIR" {
			filtered = append(filtered, entry)
		}
	}
	return append(filtered, "TMUX_TMPDIR="+tmuxRoot)
}
