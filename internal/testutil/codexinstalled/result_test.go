package codexinstalled

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTerminalResultSchemaRejectsUnknownAndBlankClasses(t *testing.T) {
	versions := VersionTuple{CLI: "0.152.0", Managed: "0.152.0", AppServer: "0.152.0"}
	for _, class := range []ResultClass{"", "unknown", "skipped"} {
		result := NewResult(versions, TopologyDirect, StageReady, class, "fixture-result")
		if err := result.Validate(); err == nil {
			t.Fatalf("class %q passed terminal validation", class)
		}
	}
	for _, class := range []ResultClass{ResultPass, ResultFail, ResultUnsupported, ResultInfraError} {
		result := NewResult(versions, TopologyManaged, StageReady, class, "fixture-result")
		if err := result.Validate(); err != nil {
			t.Fatalf("class %q rejected: %v", class, err)
		}
	}
	for _, class := range []ResultClass{ResultUnsupported, ResultInfraError} {
		result := NewResult(VersionTuple{}, TopologyManaged, StageReady, class, "pre-ready-terminal")
		if err := result.Validate(); err != nil {
			t.Fatalf("typed unknown terminal class %q rejected: %v", class, err)
		}
		if result.Versions.CLI != UnknownVersion || result.Versions.Managed != UnknownVersion || result.Versions.AppServer != UnknownVersion {
			t.Fatalf("blank versions were not made explicit: %+v", result.Versions)
		}
	}
	for _, class := range []ResultClass{ResultPass, ResultFail} {
		result := NewResult(VersionTuple{}, TopologyManaged, StageReady, class, "invalid-unknown-terminal")
		if err := result.Validate(); err == nil {
			t.Fatalf("class %q claimed a ready terminal result with unknown versions", class)
		}
	}
	provision := NewResult(VersionTuple{}, TopologyManaged, StageProvision, ResultPass, "pre-ready-provision")
	if err := provision.Validate(); err != nil {
		t.Fatalf("pre-ready provision result rejected: %v", err)
	}
	blank := Result{
		Versions: VersionTuple{}, Topology: TopologyDirect, Stage: StageReady,
		Class: ResultPass, Reason: "blank-version-negative",
	}
	if err := blank.Validate(); err == nil {
		t.Fatal("a raw terminal result with blank versions passed validation")
	}
}

func TestSemanticLedgerCountsAmbientMutationWithoutRawArgvOracle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger")
	if err := os.WriteFile(path, []byte("ambient\t1\tunexpected\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ledger := newLedger(path)
	ledger.record(Command{Scope: ScopeIsolated, Operation: "managed-stop", Mutation: MutationEndpointLifecycle})
	ledger.record(Command{Scope: ScopeAmbient, Operation: "daemon-version", Mutation: MutationNone})
	if mutations, err := ledger.AmbientMutations(); err != nil || len(mutations) != 1 {
		t.Fatalf("shim-scoped ambient mutation ledger = %v, %v", mutations, err)
	}
	ledger.record(Command{Scope: ScopeAmbient, Operation: "protocol-session", Mutation: MutationProtocolSession})
	if mutations, err := ledger.AmbientMutations(); err != nil || len(mutations) != 2 {
		t.Fatalf("ambient mutation ledger = %v, %v", mutations, err)
	}
}

func TestFailureCleanupRemovesOnlyExactOwnedArtifacts(t *testing.T) {
	root := t.TempDir()
	fixture := &Fixture{
		Root: root, CodexHome: filepath.Join(root, "codex-home"), Workspace: filepath.Join(root, "workspace"),
		SocketPath: filepath.Join(root, "codex-home", "app-server-control", "app-server-control.sock"),
		shimPath:   filepath.Join(root, "fixture-bin", "codex"), ledger: newLedger(filepath.Join(root, "codex-command-ledger")),
		startResultPath: filepath.Join(root, "managed-start-result"), ownsState: true,
	}
	for _, ownedDir := range []string{fixture.CodexHome, fixture.Workspace, filepath.Dir(fixture.shimPath)} {
		if err := os.MkdirAll(ownedDir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, ownedFile := range []string{fixture.shimPath, fixture.ledger.path, fixture.startResultPath} {
		if err := os.WriteFile(ownedFile, []byte("owned"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	foreign := filepath.Join(root, "foreign.sock")
	if err := os.WriteFile(foreign, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.CodexHome, "owned-failure-artifact"), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(foreign); err != nil {
		t.Fatalf("cleanup removed a foreign artifact: %v", err)
	}
	for _, owned := range []string{fixture.CodexHome, fixture.Workspace, filepath.Dir(fixture.shimPath), fixture.ledger.path, fixture.startResultPath} {
		if _, err := os.Lstat(owned); !os.IsNotExist(err) {
			t.Fatalf("owned artifact remained after cleanup: %s (%v)", filepath.Base(owned), err)
		}
	}
}

func TestManagedIdentityMismatchNeverAuthorizesForeignPID(t *testing.T) {
	status := daemonVersion{Backend: "pid", SocketPath: "/tmp/exact/app-server.sock"}
	if err := validateManagedIdentity(status, 7001, 7001, "/tmp/exact/app-server.sock"); err != nil {
		t.Fatalf("exact managed identity rejected: %v", err)
	}
	for _, test := range []struct {
		name        string
		status      daemonVersion
		startedPID  int
		artifactPID int
	}{
		{name: "foreign pid artifact", status: status, startedPID: 7001, artifactPID: 1},
		{name: "foreign socket", status: daemonVersion{Backend: "pid", SocketPath: "/tmp/foreign.sock"}, startedPID: 7001, artifactPID: 7001},
		{name: "foreign backend", status: daemonVersion{Backend: "other", SocketPath: status.SocketPath}, startedPID: 7001, artifactPID: 7001},
	} {
		if err := validateManagedIdentity(test.status, test.startedPID, test.artifactPID, "/tmp/exact/app-server.sock"); err == nil {
			t.Fatalf("%s authorized a managed cleanup target", test.name)
		}
	}
}

func TestDirectProcessExitWaitIsIndependentlyBounded(t *testing.T) {
	if err := waitForProcessExit(make(chan error), 0); err == nil {
		t.Fatal("a process that never reports exit passed the bounded cleanup wait")
	}
	exited := make(chan error, 1)
	exited <- nil
	if err := waitForProcessExit(exited, time.Second); err != nil {
		t.Fatalf("an already-exited process missed the bounded cleanup wait: %v", err)
	}
}

func TestDirectResidualSocketReplacementIsPreserved(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex-home")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(codexHome, "app-server.sock")
	readyListener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	readyInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = readyListener.Close() })
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	foreignListener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = foreignListener.Close()
		_ = os.Remove(path)
	})
	removed, err := removeExactResidualSocket(path, readyInfo)
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("a replacement socket at the owned path was removed")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("replacement socket was not preserved: %v", err)
	}
	fixture := &Fixture{
		CodexHome: codexHome, SocketPath: path, Workspace: filepath.Join(root, "workspace"),
		directSocketInfo: readyInfo, ownsState: true,
		shimPath: filepath.Join(root, "fixture-bin", "codex"), ledger: newLedger(filepath.Join(root, "ledger")),
		startResultPath: filepath.Join(root, "managed-start-result"),
	}
	if err := fixture.Cleanup(); err == nil {
		t.Fatal("fixture cleanup accepted a replacement socket as an owned artifact")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("fixture cleanup removed the replacement socket: %v", err)
	}
}

func TestMissingManagedPayloadIsTypedUnsupported(t *testing.T) {
	root := t.TempDir()
	releaseRoot := filepath.Join(root, "release")
	if err := os.MkdirAll(filepath.Join(releaseRoot, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(releaseRoot, "bin", "codex")
	if err := os.WriteFile(executable, []byte("not executed"), 0o700); err != nil {
		t.Fatal(err)
	}
	fixture := &Fixture{
		CodexHome: filepath.Join(root, "codex-home"), realCodex: executable,
		versions: VersionTuple{CLI: "0.152.0"}.normalized(),
		ledger:   newLedger(filepath.Join(root, "ledger")),
	}
	result := fixture.ProvisionManagedPayload()
	if result.Class != ResultUnsupported || result.Stage != StageProvision || result.Reason != "managed-payload-manifest-missing" {
		t.Fatalf("missing managed payload = %+v, want typed unsupported provision result", result)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("typed unsupported result is invalid: %v", err)
	}
}

func TestManagedPayloadVersionComesFromManifest(t *testing.T) {
	root := t.TempDir()
	releaseRoot := filepath.Join(root, "release")
	if err := os.MkdirAll(filepath.Join(releaseRoot, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(releaseRoot, "bin", "codex")
	if err := os.WriteFile(executable, []byte("not executed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseRoot, "codex-package.json"), []byte(`{"version":"0.151.7"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := &Fixture{
		CodexHome: filepath.Join(root, "codex-home"), realCodex: executable,
		versions: VersionTuple{CLI: "0.152.0"}.normalized(),
		ledger:   newLedger(filepath.Join(root, "ledger")),
	}
	result := fixture.ProvisionManagedPayload()
	if result.Class != ResultPass {
		t.Fatalf("managed payload provision = %+v", result)
	}
	if result.Versions.CLI != "0.152.0" || result.Versions.Managed != "0.151.7" {
		t.Fatalf("managed version was inferred from CLI instead of observed manifest: %+v", result.Versions)
	}
}

func TestBlankManagedPayloadManifestVersionIsTypedInfraError(t *testing.T) {
	root := t.TempDir()
	releaseRoot := filepath.Join(root, "release")
	if err := os.MkdirAll(filepath.Join(releaseRoot, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(releaseRoot, "bin", "codex")
	if err := os.WriteFile(executable, []byte("not executed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseRoot, "codex-package.json"), []byte(`{"version":" "}`), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture := &Fixture{
		CodexHome: filepath.Join(root, "codex-home"), realCodex: executable,
		versions: VersionTuple{CLI: "0.152.0"}.normalized(),
		ledger:   newLedger(filepath.Join(root, "ledger")),
	}
	result := fixture.ProvisionManagedPayload()
	if result.Class != ResultInfraError || result.Reason != "managed-payload-manifest-invalid" {
		t.Fatalf("blank managed payload version = %+v, want typed infra-error", result)
	}
}

func TestInstalledCensusDeletionReceiptHasOneOwnerPerPrimitive(t *testing.T) {
	type receipt struct {
		removed  string
		owner    string
		boundary string
		negative string
	}
	receipts := []receipt{
		{"TestInstalledIsolatedDaemonLifecycleSmoke", "TestInstalledHermeticTopologyQualification", "direct ready/close + managed start/reuse/retire", "managed payload absence is typed unsupported"},
		{"catalog setup/readiness/socket assertions", "codexinstalled.Fixture", "thread/list", "direct startup failure is terminal"},
		{"pre-turn setup/readiness/attach skip", "codexinstalled.Fixture", "thread/start + second attach + thread/read", "attach failure is terminal"},
		{"installCodexArgvLedger + codexInstalledSmokeReadOnlyArgv", "codexinstalled.Ledger", "native binding and exact-turn control", "ambient semantic mutation count is zero"},
		{"retirement smoke root/readiness/argv blocks", "codexinstalled.Fixture + Ledger", "two-Agent shared connection and sibling fencing", "endpoint lifecycle mutation is rejected"},
		{"approval smoke root/readiness/argv blocks", "codexinstalled.Fixture + Ledger", "approval lease response-once", "endpoint lifecycle mutation is rejected"},
	}
	seenBoundary := map[string]string{}
	for _, item := range receipts {
		if item.removed == "" || item.owner == "" || item.boundary == "" || item.negative == "" {
			t.Fatalf("incomplete deletion receipt: %+v", item)
		}
		if previous := seenBoundary[item.boundary]; previous != "" {
			t.Fatalf("duplicate installed primitive %q owned by %q and %q", item.boundary, previous, item.owner)
		}
		seenBoundary[item.boundary] = item.owner
	}
	primitiveOwners := []struct {
		topology, primitive, owner string
	}{
		{"direct", "ready-close", "TestInstalledHermeticTopologyQualification"},
		{"managed", "start-reuse-retire", "TestInstalledHermeticTopologyQualification"},
		{"direct", "thread-list", "TestInstalledIsolatedConversationCatalogSmoke"},
		{"direct", "thread-start", "TestInstalledIsolatedPreTurnBootstrapSmoke"},
		{"direct", "thread-read", "TestInstalledIsolatedPreTurnBootstrapSmoke"},
		{"direct", "native-binding-control", "TestInstalledIsolatedBrokerNativeBindingSmoke"},
		{"direct", "shared-connection-retirement", "TestInstalledIsolatedRetiredObserverMatrixSmoke"},
		{"direct", "approval-lease", "TestInstalledIsolatedBrokerApprovalLeaseSmoke"},
		{"ambient-read-only", "model-list", "TestInstalledCodexModelCapabilitySmoke"},
		{"fake-app-server-real-tmux", "reconnect", "TestInstalledIsolatedRealTmuxTwoAgentReconnectSmoke"},
	}
	seenPrimitive := map[string]string{}
	for _, item := range primitiveOwners {
		key := item.topology + "/" + item.primitive
		if previous := seenPrimitive[key]; previous != "" {
			t.Fatalf("installed primitive %q has duplicate owners %q and %q", key, previous, item.owner)
		}
		seenPrimitive[key] = item.owner
	}
}
