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
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

const (
	rollingAdmissionSmokeRootEnv = "PROJMUX_CODEX_PHASE4_SMOKE_ROOT"
	rollingAdmissionBinaryEnv    = "PROJMUX_CODEX_PHASE4_PROJMUX"
)

// TestInstalledPrivateRollingAdmissionReceipt is the opt-in Phase 4 product
// observation. It runs only the explicitly supplied installed Projmux binary,
// two exact leased private Codex versions, and unique XDG/socket/process roots.
// Teardown uses the target's durable operation proof; the ambient/default
// endpoint is observed read-only and never stopped, restarted, killed, or
// adopted.
func TestInstalledPrivateRollingAdmissionReceipt(t *testing.T) {
	root, enabled, err := SmokeRoot(rollingAdmissionSmokeRootEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skipf("set %s for the Phase 4 installed private rolling-admission smoke", rollingAdmissionSmokeRootEnv)
	}
	if err := validateInheritedEnvironment(); err != nil {
		t.Fatal(err)
	}
	projmux := strings.TrimSpace(os.Getenv(rollingAdmissionBinaryEnv))
	if projmux == "" {
		projmux, err = exec.LookPath("projmux")
		if err != nil {
			t.Fatalf("set %s to the installed Projmux binary: %v", rollingAdmissionBinaryEnv, err)
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
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("Phase 4 smoke root must start empty: entries=%d err=%v", len(entries), err)
	}
	removed := false
	t.Cleanup(func() {
		if !removed {
			_ = os.RemoveAll(root)
		}
	})

	ambient := captureAmbientEndpoint(t, stateSource)
	stateDomain := filepath.Join(root, "state-domain")
	oldRoot := filepath.Join(root, "generation-old")
	newRoot := filepath.Join(root, "generation-new")
	stateHome := filepath.Join(root, "xdg-state")
	configHome := filepath.Join(root, "xdg-config")
	for _, path := range []string{stateDomain, oldRoot, newRoot, stateHome, configHome} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	copySharedCodexConfig(t, stateSource, stateDomain)
	protocol := codexbundle.ProtocolRange{Min: 2, Max: 2}
	bundleStore := filepath.Join(root, "bundles")
	oldLease := leaseInstalledBundle(t, bundleStore, oldBinary, "0.152.0", protocol)
	newLease := leaseInstalledBundle(t, bundleStore, newBinary, "0.152.1", protocol)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	oldEndpoint := metadata.CodexEndpointRef{StateDomainID: "installed-phase4", EndpointGenerationID: "generation-0.152.0"}
	newEndpoint := metadata.CodexEndpointRef{StateDomainID: "installed-phase4", EndpointGenerationID: "generation-0.152.1"}
	oldSocket := filepath.Join(oldRoot, "codex-generation-0.152.0.sock")
	newSocket := filepath.Join(newRoot, "codex-generation-0.152.1.sock")
	oldHost, err := codexgenerationhost.StartPrivateGeneration(ctx, codexgenerationhost.PrivateGenerationConfig{
		Endpoint:        codexgenerationhost.EndpointIdentity{StateDomainID: oldEndpoint.StateDomainID, EndpointGenerationID: oldEndpoint.EndpointGenerationID},
		StateDomainPath: stateDomain, PrivateRoot: oldRoot, SocketPath: oldSocket,
		LeaseRoot: oldLease.Root, RequiredProtocol: protocol,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldClosed := false
	t.Cleanup(func() {
		if !oldClosed {
			_ = oldHost.Close()
		}
	})
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	oldClient, err := codexappserver.OpenPrivateUnix(ctx, oldSocket, 10*time.Second, "installed-phase4", true)
	if err != nil {
		t.Fatal(err)
	}
	oldThread, err := oldClient.StartThread(ctx, workspace, nil)
	if err != nil {
		oldClient.Close()
		t.Fatalf("payload-free old Current thread/start: %v", err)
	}
	if err := oldClient.Close(); err != nil {
		t.Fatal(err)
	}
	qualification := codexgeneration.EvaluateQualification(codexgeneration.VersionPair{Old: "0.152.0", New: "0.152.1"}, codexgeneration.QualificationEvidence{
		SharedStateDomain: true, DistinctPrivateEndpoints: true, DistinctThreadCreateTurn: true,
		DistinctThreadReadList: true, CrashRestart: true, OldStoppedBeforeResume: true,
		PersistedResumeSnapshot: true, SharedAuthConfigPrivate: true, BundleSourceRemovalLaunch: true,
		BundleDriftRefused: true, ProtocolMismatchRefused: true,
	})
	request := codexupgrade.Request{
		OperationRef: "installed-phase4-upgrade",
		Current: codexupgrade.GenerationRoute{
			Generation: codexgeneration.Generation{Endpoint: oldEndpoint, State: codexgeneration.StateCurrent, Owner: codexgeneration.OwnerProjmuxPrivate, BundleID: oldLease.ID},
			Config:     codexupgrade.GenerationConfig{Endpoint: oldEndpoint, StateDomainPath: stateDomain, PrivateRoot: oldRoot, SocketPath: oldSocket, LeaseRoot: oldLease.Root, RequiredProtocol: protocol},
			TUIPath:    oldLease.Paths(codexbundle.RoleTUI)[0], Ready: true, Proof: pointerLaunchProof(oldHost.Proof()),
		},
		Target:         codexupgrade.GenerationConfig{Endpoint: newEndpoint, StateDomainPath: stateDomain, PrivateRoot: newRoot, SocketPath: newSocket, LeaseRoot: newLease.Root, RequiredProtocol: protocol},
		TargetBundleID: newLease.ID, TargetTUIPath: newLease.Paths(codexbundle.RoleTUI)[0], Qualification: qualification,
	}
	requestPath := filepath.Join(root, "request.json")
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(requestPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, projmux, "agent", "app-server", "upgrade", "apply", "--request", requestPath) // #nosec G204 -- explicit installed binary and private request.
	command.Env = phase4InstalledEnvironment(os.Environ(), stateDomain, stateHome, configHome)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("installed rolling admission: %v: %s", err, output)
	}
	for _, zero := range []string{`"oldEndpointStop": 0`, `"successorResume": 0`, `"endpointRefCAS": 0`, `"paneRelaunch": 0`, `"retirement": 0`, `"leaseRelease": 0`, `"foreignAdoption": 0`} {
		if !strings.Contains(string(output), zero) {
			t.Fatalf("installed receipt misses %s: %s", zero, output)
		}
	}
	paths := config.DefaultPaths(configHome, stateHome)
	journal, exists, err := codexupgrade.NewStateStore(paths.StateDir).Load()
	if err != nil || !exists || journal.Operation == nil || journal.CurrentGenerationID != newEndpoint.EndpointGenerationID ||
		journal.Operation.Mutations.CandidateStart != 1 || journal.Operation.Mutations.AdmissionCommit != 1 || journal.Operation.Mutations.DrainPublish != 1 {
		t.Fatalf("installed journal = (%+v,%t,%v)", journal, exists, err)
	}
	target, ok := journal.Route(newEndpoint)
	if !ok || target.Proof == nil {
		t.Fatal("installed target proof missing")
	}
	targetCleanupDone := false
	targetCleanupProof := *target.Proof
	targetCleanupConfig := installedRollingHostConfig(target.Config)
	cleanupTarget := func(cleanupContext context.Context) (bool, error) {
		return codexgenerationhost.CleanupDurableCandidate(
			cleanupContext, targetCleanupConfig, request.OperationRef, &targetCleanupProof,
		)
	}
	t.Cleanup(func() {
		if targetCleanupDone {
			return
		}
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, cleanupErr := cleanupTarget(cleanupContext); cleanupErr != nil {
			t.Errorf("exact installed target cleanup after assertion failure: %v", cleanupErr)
		}
	})
	oldRoute, ok := journal.Route(oldEndpoint)
	if !ok || oldRoute.Proof == nil || oldRoute.Generation.State != codexgeneration.StateDraining {
		t.Fatalf("installed old Draining route missing: %+v", oldRoute)
	}
	if err := codexgenerationhost.ObservePrivateGenerationRoute(ctx, installedRollingHostConfig(oldRoute.Config), *oldRoute.Proof, oldRoute.TUIPath); err != nil {
		t.Fatalf("old Draining proof no longer usable: %v", err)
	}
	oldClient, err = codexappserver.OpenPrivateUnix(ctx, oldRoute.Config.SocketPath, 10*time.Second, "installed-phase4-old-draining", true)
	if err != nil {
		t.Fatal(err)
	}
	oldRead, err := oldClient.ReadCatalogThread(ctx, oldThread.ThreadID)
	if err != nil || oldRead.ID != oldThread.ThreadID {
		oldClient.Close()
		t.Fatalf("old Draining exact thread/read = %+v, %v", oldRead, err)
	}
	if _, err := oldClient.ListCatalogThreads(ctx, codexappserver.CatalogQuery{}); err != nil {
		oldClient.Close()
		t.Fatalf("old Draining thread/list: %v", err)
	}
	if err := oldClient.Close(); err != nil {
		t.Fatal(err)
	}
	if err := codexgenerationhost.ObservePrivateGenerationRoute(ctx, installedRollingHostConfig(target.Config), *target.Proof, target.TUIPath); err != nil {
		t.Fatalf("journal-selected new Current proof unavailable: %v", err)
	}
	newClient, err := codexappserver.OpenPrivateUnix(ctx, target.Config.SocketPath, 10*time.Second, "installed-phase4-new-current", true)
	if err != nil {
		t.Fatal(err)
	}
	newThread, err := newClient.StartThread(ctx, workspace, nil)
	if err != nil || newThread.ThreadID == "" || newThread.ThreadID == oldThread.ThreadID {
		newClient.Close()
		t.Fatalf("new Current payload-free thread/start = %+v, %v", newThread, err)
	}
	newRead, err := newClient.ReadCatalogThread(ctx, newThread.ThreadID)
	if err != nil || newRead.ID != newThread.ThreadID {
		newClient.Close()
		t.Fatalf("new Current exact thread/read = %+v, %v", newRead, err)
	}
	if err := newClient.Close(); err != nil {
		t.Fatal(err)
	}
	if cleaned, err := cleanupTarget(ctx); err != nil || !cleaned {
		t.Fatalf("exact installed target teardown: cleaned=%t err=%v", cleaned, err)
	}
	targetCleanupDone = true
	if err := oldHost.Close(); err != nil {
		t.Fatal(err)
	}
	oldClosed = true
	assertAmbientEndpointUnchanged(t, ambient)
	for _, socket := range []string{oldSocket, newSocket} {
		if _, err := os.Lstat(socket); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("private socket remains: %s: %v", socket, err)
		}
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	removed = true
}

func installedRollingHostConfig(cfg codexupgrade.GenerationConfig) codexgenerationhost.PrivateGenerationConfig {
	return codexgenerationhost.PrivateGenerationConfig{
		Endpoint: codexgenerationhost.EndpointIdentity{
			StateDomainID: cfg.Endpoint.StateDomainID, EndpointGenerationID: cfg.Endpoint.EndpointGenerationID,
		},
		StateDomainPath: cfg.StateDomainPath, PrivateRoot: cfg.PrivateRoot, SocketPath: cfg.SocketPath,
		LeaseRoot: cfg.LeaseRoot, RequiredProtocol: cfg.RequiredProtocol,
	}
}

func pointerLaunchProof(proof codexgenerationhost.LaunchProof) *codexgenerationhost.LaunchProof {
	return &proof
}

func phase4InstalledEnvironment(environment []string, codexHome, stateHome, configHome string) []string {
	filtered := isolatedEnvironment(environment, codexHome)
	out := make([]string, 0, len(filtered)+2)
	for _, entry := range filtered {
		key, _, _ := strings.Cut(entry, "=")
		if key != "XDG_STATE_HOME" && key != "XDG_CONFIG_HOME" {
			out = append(out, entry)
		}
	}
	return append(out, "XDG_STATE_HOME="+stateHome, "XDG_CONFIG_HOME="+configHome)
}
