package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexupgrade"
)

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
		qualified: storedManagedActivationQualification("0.152.1", "0.153.0"),
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

// managedActivationQualification is a measured-shaped receipt for one pair.
//
// The coverage counters are positive because the gate reads them: a receipt
// that claims every property with nothing observed is the forgery the gate
// exists to refuse, so a fixture that left them at zero would be exercising
// that refusal instead of the path it names.
func managedActivationQualification(oldVersion, newVersion string) codexgeneration.QualificationResult {
	return codexgeneration.EvaluateQualification(
		codexgeneration.VersionPair{Old: oldVersion, New: newVersion},
		codexgeneration.QualificationEvidence{
			SharedStateDomain: true, DistinctPrivateEndpoints: true, DistinctThreadCreateTurn: true,
			DistinctThreadReadList: true, CrashRestart: true, OldStoppedBeforeResume: true,
			PersistedResumeSnapshot: true, SharedAuthConfigPrivate: true, BundleSourceRemovalLaunch: true,
			BundleDriftRefused: true, ProtocolMismatchRefused: true,
			ObservedThreadTurns: 2, ObservedThreadReads: 8, ObservedCrashRestarts: 2, ObservedBundleLaunches: 8,
		})
}

func storedManagedActivationQualification(oldVersion, newVersion string) func(codexgeneration.VersionPair) (codexgeneration.QualificationResult, bool, error) {
	stored := managedActivationQualification(oldVersion, newVersion)
	return func(pair codexgeneration.VersionPair) (codexgeneration.QualificationResult, bool, error) {
		if pair != stored.Versions {
			return codexgeneration.QualificationResult{}, false, nil
		}
		return stored, true, nil
	}
}
