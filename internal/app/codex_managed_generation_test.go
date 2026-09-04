package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

type codexManagedCurrentActivatorFunc func(context.Context) error

func (fn codexManagedCurrentActivatorFunc) Ensure(ctx context.Context) error { return fn(ctx) }

func shortManagedActivationRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "projmux-p6-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove short managed-activation root: %v", err)
		}
	})
	return root
}

func TestProductionManagedActivationBuildsExactDefaultUpgradeRequest(t *testing.T) {
	root := shortManagedActivationRoot(t)
	stateDomain := filepath.Join(root, "codex-home")
	if err := os.Mkdir(stateDomain, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"auth.json", "config.toml"} {
		if err := os.WriteFile(filepath.Join(stateDomain, name), []byte("fixture-"+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(root, "release", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("codex"), 0o700); err != nil {
		t.Fatal(err)
	}
	leaseRoot := filepath.Join(root, "lease", "sha256-test")
	if err := os.MkdirAll(filepath.Join(leaseRoot, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}

	var got codexupgrade.ManagedCurrentActivation
	activator := &productionCodexManagedCurrentActivator{
		stateDir: filepath.Join(root, "state"), coordinator: &codexupgrade.Coordinator{},
		probe: func(context.Context) codexappserver.Health {
			return codexappserver.Health{
				EndpointReadiness: codexappserver.EndpointReady, VersionRelation: codexappserver.VersionSkew,
				InstallCapability: codexappserver.InstallCapabilityManagedReady, ManagerOwnership: codexappserver.ManagerUnmanaged,
				CLIVersion: "0.153.0", ManagedVersion: "0.153.0", RunningVersion: "0.152.1",
			}
		},
		lookPath:  func(string) (string, error) { return executable, nil },
		lookupEnv: func(string) string { return stateDomain },
		homeDir:   func() (string, error) { return "", errors.New("must not use home") },
		lease: func(store, executable, releaseVersion string, protocol codexbundle.ProtocolRange) (codexbundle.Lease, error) {
			if !strings.HasSuffix(store, filepath.Join("codex-generations", "bundles")) || releaseVersion != "0.153.0" || protocol.Min != managedCodexProtocolVersion || protocol.Max != managedCodexProtocolVersion {
				t.Fatalf("lease input store=%q executable=%q version=%q protocol=%+v", store, executable, releaseVersion, protocol)
			}
			return codexbundle.Lease{ID: "sha256-test", Root: leaseRoot}, nil
		},
		activate: func(_ context.Context, request codexupgrade.ManagedCurrentActivation) (codexupgrade.Journal, error) {
			got = request
			return codexupgrade.Journal{}, nil
		},
	}
	if err := activator.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.OldOwner != codexgeneration.OwnerUnmanaged || got.OldVersion != "0.152.1" || got.TargetVersion != "0.153.0" ||
		got.OldEndpoint.EndpointGenerationID != "codex-0.152.1" || got.Target.Endpoint.EndpointGenerationID != "codex-0.153.0" ||
		got.OldEndpoint.StateDomainID == "" || got.OldEndpoint.StateDomainID != got.Target.Endpoint.StateDomainID ||
		got.Target.StateDomainPath != stateDomain || got.Target.LeaseRoot != leaseRoot || got.TargetBundleID != "sha256-test" ||
		got.TargetTUIPath != filepath.Join(leaseRoot, "bin", "codex") || got.OperationRef == "" {
		t.Fatalf("managed activation request = %+v", got)
	}
	wantPrivateRoot, wantSocketPath, err := managedCodexRuntimeLocation(activator.stateDir, got.Target.Endpoint.StateDomainID, got.TargetVersion)
	if err != nil || got.Target.PrivateRoot != wantPrivateRoot || got.Target.SocketPath != wantSocketPath ||
		filepath.Dir(got.Target.SocketPath) != got.Target.PrivateRoot || len([]byte(got.Target.SocketPath)) > managedCodexSocketPathMaxBytes {
		t.Fatalf("portable managed runtime root=%q socket=%q want=%q/%q err=%v", got.Target.PrivateRoot, got.Target.SocketPath, wantPrivateRoot, wantSocketPath, err)
	}
	if info, err := os.Stat(got.Target.PrivateRoot); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("private root info=%v err=%v", info, err)
	}
}

func TestManagedCodexRuntimeLocationPortableBoundRefusesBeforeMutation(t *testing.T) {
	root := shortManagedActivationRoot(t)
	stateDomain := filepath.Join(root, "codex-home")
	if err := os.Mkdir(stateDomain, 0o700); err != nil {
		t.Fatal(err)
	}
	shortRoot, shortSocket, err := managedCodexRuntimeLocation(filepath.Join(root, "state"), "codex-state-exact", "0.153.0")
	if err != nil || !filepath.IsAbs(shortRoot) || filepath.Dir(shortSocket) != shortRoot ||
		len([]byte(shortSocket)) > managedCodexSocketPathMaxBytes {
		t.Fatalf("portable short runtime=%q socket=%q err=%v", shortRoot, shortSocket, err)
	}
	repeatRoot, repeatSocket, repeatErr := managedCodexRuntimeLocation(filepath.Join(root, "state"), "codex-state-exact", "0.153.0")
	otherRoot, _, otherErr := managedCodexRuntimeLocation(filepath.Join(root, "state"), "codex-state-other", "0.153.0")
	if repeatErr != nil || otherErr != nil || repeatRoot != shortRoot || repeatSocket != shortSocket || otherRoot == shortRoot {
		t.Fatalf("runtime identity repeat=%q/%q other=%q errors=%v/%v", repeatRoot, repeatSocket, otherRoot, repeatErr, otherErr)
	}

	longStateDir := filepath.Join(root, strings.Repeat("long-state-", 12))
	_, unusableSocket, locationErr := managedCodexRuntimeLocation(longStateDir, "codex-state-exact", "0.153.0")
	if locationErr == nil || len([]byte(unusableSocket)) <= managedCodexSocketPathMaxBytes {
		t.Fatalf("long runtime socket=%q bytes=%d err=%v", unusableSocket, len([]byte(unusableSocket)), locationErr)
	}
	_, activationDomainID, err := defaultCodexStateDomain(func(string) string { return stateDomain }, os.UserHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	_, activationSocket, activationLocationErr := managedCodexRuntimeLocation(longStateDir, activationDomainID, "0.153.0")
	if activationLocationErr == nil {
		t.Fatalf("activation runtime socket unexpectedly fit portable bound: %q", activationSocket)
	}
	lookups, leases, activations := 0, 0, 0
	activator := &productionCodexManagedCurrentActivator{
		stateDir: longStateDir, coordinator: &codexupgrade.Coordinator{},
		probe: func(context.Context) codexappserver.Health {
			return codexappserver.Health{
				EndpointReadiness: codexappserver.EndpointReady, VersionRelation: codexappserver.VersionSkew,
				InstallCapability: codexappserver.InstallCapabilityManagedReady, ManagerOwnership: codexappserver.ManagerUnmanaged,
				CLIVersion: "0.153.0", ManagedVersion: "0.153.0", RunningVersion: "0.152.1",
			}
		},
		lookupEnv: func(string) string { return stateDomain }, homeDir: os.UserHomeDir,
		lookPath: func(string) (string, error) { lookups++; return "", errors.New("must not resolve executable") },
		lease: func(string, string, string, codexbundle.ProtocolRange) (codexbundle.Lease, error) {
			leases++
			return codexbundle.Lease{}, nil
		},
		activate: func(context.Context, codexupgrade.ManagedCurrentActivation) (codexupgrade.Journal, error) {
			activations++
			return codexupgrade.Journal{}, nil
		},
	}
	err = activator.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "managed-runtime-socket-too-long") ||
		!strings.Contains(err.Error(), activationSocket) || !strings.Contains(err.Error(), "XDG_STATE_HOME") {
		t.Fatalf("long runtime refusal = %v", err)
	}
	if lookups != 0 || leases != 0 || activations != 0 {
		t.Fatalf("long runtime gate mutated lookup=%d lease=%d activation=%d", lookups, leases, activations)
	}
	if _, statErr := os.Lstat(longStateDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("long runtime gate created state: %v", statErr)
	}
}

func TestManagedCodexActivationHostRefusalsNameExactCorrectiveAction(t *testing.T) {
	target := codexupgrade.GenerationConfig{PrivateRoot: "/exact/private", SocketPath: "/exact/private/s"}
	for _, test := range []struct {
		refusal codexgenerationhost.HostRefusal
		want    string
	}{
		{refusal: codexgenerationhost.HostRefusalPrivateRootInvalid, want: target.PrivateRoot},
		{refusal: codexgenerationhost.HostRefusalBundleDrift, want: "reinstall the complete managed Codex standalone release"},
		{refusal: codexgenerationhost.HostRefusalSocketOccupied, want: target.SocketPath},
		{refusal: codexgenerationhost.HostRefusalReadinessFailed, want: target.SocketPath},
		{refusal: codexgenerationhost.HostRefusalLaunchProofMismatch, want: target.PrivateRoot},
	} {
		action := managedCodexActivationFailureAction(&codexgenerationhost.HostError{Refusal: test.refusal}, target)
		if !strings.Contains(action, test.want) || strings.Contains(action, "projmux doctor") {
			t.Fatalf("host refusal %s action=%q", test.refusal, action)
		}
	}
}

func TestProductionManagedActivationUnsafeStateDomainNamesExactActionWithZeroMutation(t *testing.T) {
	root := t.TempDir()
	stateDomain := filepath.Join(root, "codex-home")
	if err := os.Mkdir(stateDomain, 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(stateDomain)
	if err != nil || !directoryOwnedBy(info, os.Geteuid()) || directoryOwnedBy(info, os.Geteuid()+1) {
		t.Fatalf("owner UID seam info=%v err=%v", info, err)
	}
	leaseCalls, activationCalls := 0, 0
	activator := &productionCodexManagedCurrentActivator{
		stateDir: filepath.Join(root, "state"), coordinator: &codexupgrade.Coordinator{},
		probe: func(context.Context) codexappserver.Health {
			return codexappserver.Health{
				EndpointReadiness: codexappserver.EndpointReady, VersionRelation: codexappserver.VersionSkew,
				InstallCapability: codexappserver.InstallCapabilityManagedReady, ManagerOwnership: codexappserver.ManagerUnmanaged,
				CLIVersion: "0.153.0", ManagedVersion: "0.153.0", RunningVersion: "0.152.1",
			}
		},
		lookupEnv: func(string) string { return stateDomain }, homeDir: os.UserHomeDir,
		lease: func(string, string, string, codexbundle.ProtocolRange) (codexbundle.Lease, error) {
			leaseCalls++
			return codexbundle.Lease{}, nil
		},
		activate: func(context.Context, codexupgrade.ManagedCurrentActivation) (codexupgrade.Journal, error) {
			activationCalls++
			return codexupgrade.Journal{}, nil
		},
	}
	err = activator.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "state-domain-not-owner-private") ||
		!strings.Contains(err.Error(), "chmod 700") || !strings.Contains(err.Error(), stateDomain) {
		t.Fatalf("unsafe state-domain refusal = %v", err)
	}
	if leaseCalls != 0 || activationCalls != 0 {
		t.Fatalf("unsafe gate mutations lease=%d activation=%d", leaseCalls, activationCalls)
	}
	if _, err := os.Stat(activator.stateDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe gate created state dir: %v", err)
	}
}

func TestOrdinaryCodexCreateSpellingsActivateRollingManagedCurrent(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	journal := codexupgrade.NewStateStore(stateDir)
	endpoint := coremetadata.CodexEndpointRef{StateDomainID: "test-domain", EndpointGenerationID: "codex-0.153.0"}
	config := codexupgrade.GenerationConfig{
		Endpoint: endpoint, StateDomainPath: "/test/state-domain", PrivateRoot: "/test/runtime",
		SocketPath: "/test/runtime/s", LeaseRoot: "/test/lease",
		RequiredProtocol: codexbundle.ProtocolRange{Min: 2, Max: 2},
	}
	proof := &codexgenerationhost.LaunchProof{
		Endpoint:   codexgenerationhost.EndpointIdentity{StateDomainID: endpoint.StateDomainID, EndpointGenerationID: endpoint.EndpointGenerationID},
		SocketPath: config.SocketPath, BundleID: "sha256-managed",
	}
	operation, err := codexgeneration.NewRollingUpgradeOperation("managed-activation-test", endpoint.StateDomainID, "codex-0.152.1", endpoint.EndpointGenerationID)
	if err != nil {
		t.Fatal(err)
	}
	operation, _, err = operation.RecordCandidateLaunchIntent()
	if err == nil {
		operation, _, err = operation.RecordCandidateStart()
	}
	if err == nil {
		operation, _, err = operation.RecordAction(codexgeneration.RollingActionPrepareCandidate, nil)
	}
	if err == nil {
		operation, _, err = operation.RecordAction(codexgeneration.RollingActionCommitAdmission, nil)
	}
	if err != nil {
		t.Fatalf("post-admission operation fixture: %v", err)
	}
	activationCalls, createCalls := 0, 0
	controller := rollingCodexNativeThreadController{
		journal: journal,
		fallback: defaultCodexNativeThreadController{current: func(context.Context) (codexNativeEndpointRoute, error) {
			return codexNativeEndpointRoute{}, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
		}},
		activator: codexManagedCurrentActivatorFunc(func(ctx context.Context) error {
			activationCalls++
			_, err := journal.Update(ctx, func(got *codexupgrade.Journal, exists bool) error {
				if exists {
					next, _, recordErr := got.Operation.RecordAction(codexgeneration.RollingActionPublishDrain, nil)
					got.Operation = &next
					return recordErr
				}
				*got = codexupgrade.Journal{
					Version: codexupgrade.JournalVersion, StateDomainID: endpoint.StateDomainID,
					CurrentGenerationID: endpoint.EndpointGenerationID,
					Routes: []codexupgrade.GenerationRoute{
						{
							Generation: codexgeneration.Generation{Endpoint: coremetadata.CodexEndpointRef{StateDomainID: endpoint.StateDomainID, EndpointGenerationID: "codex-0.152.1"}, State: codexgeneration.StateDraining, Owner: codexgeneration.OwnerUnmanaged, BundleID: "external-0.152.1"},
							Version:    "0.152.1",
						},
						{
							Generation: codexgeneration.Generation{Endpoint: endpoint, State: codexgeneration.StateCurrent, Owner: codexgeneration.OwnerProjmuxPrivate, BundleID: "sha256-managed"},
							Version:    "0.153.0", Config: config, TUIPath: "/test/lease/bin/codex", Ready: true, Proof: proof,
						},
					},
					Operation: &operation,
				}
				return nil
			})
			return err
		}),
		observe: func(context.Context, codexupgrade.GenerationRoute) error { return nil },
		create: func(_ context.Context, route codexNativeEndpointRoute, _ coremetadata.AgentWorkspace, prompt, _ string) (codexappserver.ThreadBinding, error) {
			createCalls++
			if !route.Endpoint.Same(endpoint) || route.State != codexgeneration.StateCurrent {
				t.Fatalf("create route = %+v", route)
			}
			binding := codexappserver.ThreadBinding{ThreadID: fmt.Sprintf("thread-managed-%d", createCalls)}
			if prompt != "" {
				binding.TurnID = fmt.Sprintf("turn-managed-%d", createCalls)
			}
			return binding, nil
		},
	}

	store := newFakeResourceStore(t)
	for index := range store.registry.Agents {
		if store.registry.Agents[index].Metadata.UID == "agt-alpha-codex" {
			store.registry.Agents[index].Status.Interaction.Kind = coremetadata.InteractionApprovalRequired
			store.registry.Agents[index].Status.SessionRef = &coremetadata.AgentSessionRef{
				Provider: "codex", ObservedAt: resourceFixtureClock,
				Codex: &coremetadata.CodexSessionRef{ThreadID: "thread-old-0.152.1", HasStartedTurn: true},
			}
		}
	}
	oldBefore, _ := store.registry.Agent("agt-alpha-codex")
	tmux := newFakeTmux()
	seedOwnedSession(seedLiveAgentPane(t, tmux, "alpha", "win-alpha-main", "pan-alpha-zsh", "pan-alpha-codex"), "prj-alpha", "/srv/alpha")
	create, _ := newTestAgentCreateCommand(t, store, tmux)
	panes := &fakeNativePaneLauncher{}
	create.codexNative = controller
	create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: panes}
	for _, argv := range [][]string{
		{"codex", "--project", "alpha", "--window", "main"},
		{"agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--", "new generation prompt"},
	} {
		if stdout, stderr, err := runRoute(t, create, argv...); err != nil || stdout == "" || stderr != "" {
			t.Fatalf("ordinary create %v: stdout=%q stderr=%q err=%v", argv, stdout, stderr, err)
		}
	}
	if activationCalls != 2 || createCalls != 1 {
		t.Fatalf("payload-free create reached generation activation: activation calls=%d create calls=%d, want 2/1 from the prompted create only",
			activationCalls, createCalls)
	}
	converged, exists, err := journal.Load()
	if err != nil || !exists || converged.Operation == nil || !converged.Operation.DrainPublished {
		t.Fatalf("ordinary create observed unconverged activation: exists=%t err=%v journal=%+v", exists, err, converged)
	}
	oldAfter, _ := store.registry.Agent("agt-alpha-codex")
	if oldAfter.Status.PaneRef != oldBefore.Status.PaneRef || oldAfter.Status.Phase != oldBefore.Status.Phase ||
		oldAfter.Status.Interaction != oldBefore.Status.Interaction || oldAfter.Status.SessionRef == nil ||
		!oldAfter.Status.SessionRef.SameConversation(oldBefore.Status.SessionRef) || oldAfter.Status.SessionRef.Codex.Endpoint != nil {
		t.Fatalf("legacy old-generation Agent continuity changed: before=%#v after=%#v", oldBefore.Status, oldAfter.Status)
	}
	plain := agentNamed(t, store, "win-alpha-main", "agent-test-1")
	if plain.Status.Phase != coremetadata.PhaseRunning || plain.Status.PaneRef == "" || plain.Status.SessionRef != nil {
		t.Fatalf("payload-free Agent did not remain on the pre-provider plain lane: %#v", plain.Status)
	}
	native := agentNamed(t, store, "win-alpha-main", "agent-test-3")
	if native.Status.SessionRef == nil || native.Status.SessionRef.Codex == nil || native.Status.SessionRef.Codex.Endpoint == nil ||
		!native.Status.SessionRef.Codex.Endpoint.Same(endpoint) {
		t.Fatalf("prompted Agent not pinned to managed current: %#v", native.Status.SessionRef)
	}
}

func TestOrdinaryCodexCreateActivationGateReturnsExactActionWithZeroMutation(t *testing.T) {
	stateDir := filepath.Join(t.TempDir(), "state")
	action := "run `chmod 700 \"/exact/codex-home\"` after verifying that it is the intended CODEX_HOME, then retry"
	controller := rollingCodexNativeThreadController{
		journal: codexupgrade.NewStateStore(stateDir),
		fallback: defaultCodexNativeThreadController{current: func(context.Context) (codexNativeEndpointRoute, error) {
			return codexNativeEndpointRoute{}, &codexNativeRouteError{Reason: codexNativeReasonGenerationUnavailable}
		}},
		activator: codexManagedCurrentActivatorFunc(func(context.Context) error {
			return managedActivationRefusal("state-domain-not-owner-private", action, errors.New("mode is not 0700"))
		}),
	}
	store := newFakeResourceStore(t)
	tmux := newFakeTmux()
	create, _ := newTestAgentCreateCommand(t, store, tmux)
	create.codexNative = controller
	create.resumes = &fakeNativeResumeLauncher{fakeResumeLauncher: newFakeResumeLauncher(), fakeNativePaneLauncher: &fakeNativePaneLauncher{}}
	before := store.snapshot()
	stdout, stderr, err := runRoute(t, create, "agent", "--provider", "codex", "--project", "alpha", "--window", "main", "--", "must not send")
	if err == nil || stdout != "" || stderr != "" || !strings.Contains(err.Error(), "managed-generation-activation-blocked") || !strings.Contains(err.Error(), action) {
		t.Fatalf("activation gate stdout=%q stderr=%q err=%v", stdout, stderr, err)
	}
	if store.snapshot() != before || store.writes != 0 || tmux.argvContains("split-window") || tmux.argvContains("new-window") {
		t.Fatalf("activation gate mutated registry/tmux writes=%d calls=%v", store.writes, tmux.calls)
	}
	if _, statErr := os.Stat(stateDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("activation gate created journal state: %v", statErr)
	}
}
