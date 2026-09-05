package codexinstalled

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
)

const (
	generationSmokeRootEnv   = "PROJMUX_CODEX_GENERATION_SMOKE_ROOT"
	generationOldEnv         = "PROJMUX_CODEX_GENERATION_OLD"
	generationNewEnv         = "PROJMUX_CODEX_GENERATION_NEW"
	generationOldVersionEnv  = "PROJMUX_CODEX_GENERATION_OLD_VERSION"
	generationNewVersionEnv  = "PROJMUX_CODEX_GENERATION_NEW_VERSION"
	generationStateEnv       = "PROJMUX_CODEX_GENERATION_SOURCE_HOME"
	generationBundleRootEnv  = "PROJMUX_CODEX_GENERATION_BUNDLE_SMOKE_ROOT"
	generationReceiptPathEnv = "PROJMUX_CODEX_GENERATION_RECEIPT"
)

// TestInstalledIsolatedGenerationPoolQualification is the declared-pair Phase 0
// conformance gate. The pair under test is declared through
// PROJMUX_CODEX_GENERATION_OLD_VERSION / _NEW_VERSION and its two absolute
// executables through PROJMUX_CODEX_GENERATION_OLD / _NEW; each binary must
// report the version declared for it before anything is measured, and the
// canonical receipt is written to PROJMUX_CODEX_GENERATION_RECEIPT. The pair is
// the only declared input: every evidence boolean and counter below stays
// measured by this fixture. It is opt-in because it uses the installed Codex
// service and performs two bounded, minimal canary turns. The fixture owns only
// its explicit /tmp root, private sockets, child processes, and copied state. It
// neither discovers nor mutates the ambient/default daemon or tmux.
func TestInstalledIsolatedGenerationPoolQualification(t *testing.T) {
	root, enabled, err := SmokeRoot(generationSmokeRootEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skipf("set %s for the fixed dual-version generation qualification", generationSmokeRootEnv)
	}
	if err := validateInheritedEnvironment(); err != nil {
		t.Fatal(err)
	}
	pair, err := DeclaredGenerationPair(os.Getenv(generationOldVersionEnv), os.Getenv(generationNewVersionEnv))
	if err != nil {
		t.Fatal(err)
	}
	receiptPath := filepath.Clean(strings.TrimSpace(os.Getenv(generationReceiptPathEnv)))
	if !filepath.IsAbs(receiptPath) {
		t.Fatalf("%s must be absolute", generationReceiptPathEnv)
	}
	oldBinary := exactGenerationBinary(t, generationOldEnv, pair.Old)
	newBinary := exactGenerationBinary(t, generationNewEnv, pair.New)
	stateSource := filepath.Clean(strings.TrimSpace(os.Getenv(generationStateEnv)))
	if !filepath.IsAbs(stateSource) {
		t.Fatalf("%s must be absolute", generationStateEnv)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("generation smoke root must start empty: entries=%d err=%v", len(entries), err)
	}
	rootRemoved := false
	bundleRoot := filepath.Clean(strings.TrimSpace(os.Getenv(generationBundleRootEnv)))
	if !filepath.IsAbs(bundleRoot) || bundleRoot == filepath.Clean(string(filepath.Separator)) {
		t.Fatalf("%s must be an absolute dedicated directory", generationBundleRootEnv)
	}
	if err := os.MkdirAll(bundleRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	bundleEntries, err := os.ReadDir(bundleRoot)
	if err != nil || len(bundleEntries) != 0 {
		t.Fatalf("generation bundle root must start empty: entries=%d err=%v", len(bundleEntries), err)
	}
	bundleRootRemoved := false
	t.Cleanup(func() {
		if !rootRemoved {
			if err := os.RemoveAll(root); err != nil {
				t.Errorf("remove exact generation smoke root: %v", err)
			}
		}
		if !bundleRootRemoved {
			if err := os.RemoveAll(bundleRoot); err != nil {
				t.Errorf("remove exact generation bundle root: %v", err)
			}
		}
	})

	stateDomain := filepath.Join(root, "shared-state-domain")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(stateDomain, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	copySharedCodexConfig(t, stateSource, stateDomain)
	ledger := &generationConformanceLedger{}
	ledger.recordFilesystem(pathWithin(root, stateDomain), "shared-auth-config")

	protocol := codexbundle.ProtocolRange{Min: 2, Max: 2}
	oldLease := leaseInstalledBundle(t, filepath.Join(bundleRoot, "bundle-store"), oldBinary, pair.Old, protocol)
	newLease := leaseInstalledBundle(t, filepath.Join(bundleRoot, "bundle-store"), newBinary, pair.New, protocol)
	ledger.recordFilesystem(pathWithin(bundleRoot, oldLease.Root) && pathWithin(bundleRoot, newLease.Root), "bundle-leases")
	bundleDriftRefused, protocolMismatchRefused := true, true
	for _, lease := range []codexbundle.Lease{oldLease, newLease} {
		if _, err := codexbundle.Open(lease.Root, protocol); err != nil {
			t.Fatalf("reopen leased bundle: %v", err)
		}
		mismatchStore := filepath.Join(bundleRoot, "protocol-mismatch", lease.ID)
		if _, err := codexbundle.Create(mismatchStore, lease.Root, lease.Manifest, codexbundle.ProtocolRange{Min: 3, Max: 3}); codexbundle.RefusalOf(err) != codexbundle.RefusalProtocolMismatch {
			protocolMismatchRefused = false
		}
		drifted := lease.Manifest
		drifted.Artifacts = append([]codexbundle.Artifact(nil), lease.Manifest.Artifacts...)
		drifted.Artifacts[0].Mode = 0o700
		driftStore := filepath.Join(bundleRoot, "manifest-drift", lease.ID)
		_, driftErr := codexbundle.Create(driftStore, lease.Root, drifted, protocol)
		driftID, idErr := drifted.ContentID()
		_, finalErr := os.Lstat(filepath.Join(driftStore, driftID))
		if codexbundle.RefusalOf(driftErr) != codexbundle.RefusalArtifactModeDrift || idErr != nil || !errors.Is(finalErr, fs.ErrNotExist) {
			bundleDriftRefused = false
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	oldSocket, newSocket := filepath.Join(root, "old.sock"), filepath.Join(root, "new.sock")
	oldEndpoint := startPrivateGeneration(t, ctx, oldLease.Paths(codexbundle.RoleServer)[0], stateDomain, oldSocket, ledger, "old")
	newEndpoint := startPrivateGeneration(t, ctx, newLease.Paths(codexbundle.RoleServer)[0], stateDomain, newSocket, ledger, "new")
	t.Cleanup(func() { oldEndpoint.cleanup(t); newEndpoint.cleanup(t) })

	oldClient := openPrivateGeneration(t, ctx, oldSocket, "old", ledger)
	newClient := openPrivateGeneration(t, ctx, newSocket, "new", ledger)
	oldThread, newThread := createConcurrentGenerationThreads(t, ctx, oldClient, newClient, workspace)
	if oldThread.ThreadID == newThread.ThreadID {
		t.Fatal("old/new endpoints returned the same new thread")
	}
	oldTurn, newTurn := turnConcurrentGenerationThreads(t, ctx, oldClient, newClient, oldThread.ThreadID, newThread.ThreadID)
	oldSnapshot := waitForGenerationTurn(t, ctx, oldClient.Client, oldThread.ThreadID, oldTurn)
	newSnapshot := waitForGenerationTurn(t, ctx, newClient.Client, newThread.ThreadID, newTurn)
	assertGenerationReadList(t, ctx, oldClient, oldThread.ThreadID, newThread.ThreadID)
	assertGenerationReadList(t, ctx, newClient, newThread.ThreadID, oldThread.ThreadID)
	_ = oldClient.Close()
	_ = newClient.Close()

	// Kill rather than gracefully retire both isolated children, then wait for
	// exact process/socket absence before restarting. The barrier observes state;
	// no fixed sleep is completion evidence.
	oldEndpoint.crash(t, ctx)
	newEndpoint.crash(t, ctx)
	oldEndpoint = startPrivateGeneration(t, ctx, oldLease.Paths(codexbundle.RoleServer)[0], stateDomain, oldSocket, ledger, "old")
	newEndpoint = startPrivateGeneration(t, ctx, newLease.Paths(codexbundle.RoleServer)[0], stateDomain, newSocket, ledger, "new")
	oldClient = openPrivateGeneration(t, ctx, oldSocket, "old", ledger)
	newClient = openPrivateGeneration(t, ctx, newSocket, "new", ledger)
	if _, err := oldClient.ResumeThread(ctx, oldThread.ThreadID, workspace, nil); err != nil {
		t.Fatalf("old exact-thread restart resume: %v", err)
	}
	if _, err := newClient.ResumeThread(ctx, newThread.ThreadID, workspace, nil); err != nil {
		t.Fatalf("new exact-thread restart resume: %v", err)
	}
	assertGenerationReadList(t, ctx, oldClient, oldThread.ThreadID, newThread.ThreadID)
	assertGenerationReadList(t, ctx, newClient, newThread.ThreadID, oldThread.ThreadID)
	oldRestartSnapshot, err := oldClient.ReadLifecycleSnapshot(ctx, oldThread.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	newRestartSnapshot, err := newClient.ReadLifecycleSnapshot(ctx, newThread.ThreadID)
	if err != nil {
		t.Fatal(err)
	}

	oldRef := metadata.CodexEndpointRef{StateDomainID: "state-domain-qualification", EndpointGenerationID: "g-" + pair.Old}
	newRef := metadata.CodexEndpointRef{StateDomainID: "state-domain-qualification", EndpointGenerationID: "g-" + pair.New}
	if decision := codexgeneration.ApplySuccessorResume(oldRef, newRef, false, true, func() {
		_, _ = newClient.ResumeThread(ctx, oldThread.ThreadID, workspace, nil)
	}); decision != codexgeneration.ResumeOwnerStillLive {
		t.Fatalf("live-owner barrier decision=%s", decision)
	}
	liveOwnerResumeWrites := ledger.liveOwnerResumeWrites(oldThread.ThreadID)
	if liveOwnerResumeWrites != 0 {
		t.Fatalf("live-owner successor provider writes=%d", liveOwnerResumeWrites)
	}
	_ = oldClient.Close()
	oldEndpoint.stop(t, ctx)
	ledger.markOldStopped()
	resumed := false
	if decision := codexgeneration.ApplySuccessorResume(oldRef, newRef, true, true, func() {
		binding, resumeErr := newClient.ResumeThread(ctx, oldThread.ThreadID, workspace, nil)
		if resumeErr != nil || binding.ThreadID != oldThread.ThreadID {
			t.Fatalf("successor exact-thread resume binding=%+v err=%v", binding, resumeErr)
		}
		resumed = true
	}); decision != codexgeneration.ResumeAllowed || !resumed {
		t.Fatalf("post-stop successor decision=%s resumed=%t", decision, resumed)
	}
	snapshot, err := newClient.ReadLifecycleSnapshot(ctx, oldThread.ThreadID)
	if err != nil || snapshot.ThreadID != oldThread.ThreadID || snapshot.TurnID != oldTurn || snapshot.TurnState != codexappserver.TurnStateCompleted {
		t.Fatalf("successor snapshot does not preserve completed exact turn: snapshot=%+v err=%v", snapshot, err)
	}
	_ = newClient.Close()
	newEndpoint.stop(t, ctx)

	crossThreadWrites := ledger.crossThreadWrites(oldThread.ThreadID, newThread.ThreadID)
	storeCorruptions := generationStoreCorruptions(stateDomain,
		[]generationThreadSnapshot{
			{ThreadID: oldSnapshot.ThreadID, TurnID: oldSnapshot.TurnID},
			{ThreadID: newSnapshot.ThreadID, TurnID: newSnapshot.TurnID},
			{ThreadID: oldRestartSnapshot.ThreadID, TurnID: oldRestartSnapshot.TurnID},
			{ThreadID: newRestartSnapshot.ThreadID, TurnID: newRestartSnapshot.TurnID},
		}, oldThread.ThreadID, oldTurn, newThread.ThreadID, newTurn)
	ambientMutations := ledger.ambientMutations()
	bundleVersionTuple, bundleLaunches := bundleVersions(t, ctx, ledger, oldLease, newLease)
	evidence := codexgeneration.QualificationEvidence{
		SharedStateDomain: true, DistinctPrivateEndpoints: oldSocket != newSocket,
		DistinctThreadCreateTurn: true, DistinctThreadReadList: true, CrashRestart: true,
		CrossThreadWrites: crossThreadWrites, StoreCorruptions: storeCorruptions, LiveOwnerResumeWrites: liveOwnerResumeWrites,
		OldStoppedBeforeResume: true, PersistedResumeSnapshot: true,
		SharedAuthConfigPrivate:   sharedConfigPrivate(t, stateDomain),
		BundleSourceRemovalLaunch: bundleLaunches && bundleVersionTuple == pair.Old+"/"+pair.New,
		BundleDriftRefused:        bundleDriftRefused, ProtocolMismatchRefused: protocolMismatchRefused, AmbientMutations: ambientMutations,
	}
	result := codexgeneration.EvaluateQualification(pair, evidence)
	// The receipt is emitted before the verdict is asserted: a refusal is
	// exactly the outcome a consumer needs on disk, and the emitted bytes are
	// the same ones an upgrade request embeds verbatim.
	raw, err := EmitGenerationQualificationReceipt(receiptPath, result)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := codexgeneration.DecodeQualificationResult(raw)
	if err != nil || reopened != result {
		t.Fatalf("content-free receipt reopen=%+v err=%v", reopened, err)
	}
	t.Logf("generation-qualification=%s receipt=%s", raw, receiptPath)
	if result.Verdict != codexgeneration.VerdictYes {
		t.Fatalf("generation qualification: %+v", result)
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	rootRemoved = true
	if _, err := os.Lstat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("exact cleanup incomplete: %v", err)
	}
	if err := os.RemoveAll(bundleRoot); err != nil {
		t.Fatal(err)
	}
	bundleRootRemoved = true
	if _, err := os.Lstat(bundleRoot); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("exact bundle cleanup incomplete: %v", err)
	}
}

type generationProcessRecord struct {
	Isolated   bool
	Operation  string
	Generation string
}

type generationProviderRecord struct {
	Generation string
	Operation  string
	ThreadID   string
	OldStopped bool
}

type generationConformanceLedger struct {
	mu         sync.Mutex
	process    []generationProcessRecord
	provider   []generationProviderRecord
	oldStopped bool
}

func (ledger *generationConformanceLedger) recordProcess(isolated bool, operation, generation string) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	ledger.process = append(ledger.process, generationProcessRecord{Isolated: isolated, Operation: operation, Generation: generation})
}

func (ledger *generationConformanceLedger) recordFilesystem(isolated bool, operation string) {
	ledger.recordProcess(isolated, operation, "filesystem")
}

func (ledger *generationConformanceLedger) recordProvider(generation, operation, threadID string) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	ledger.provider = append(ledger.provider, generationProviderRecord{
		Generation: generation, Operation: operation, ThreadID: threadID, OldStopped: ledger.oldStopped,
	})
}

func (ledger *generationConformanceLedger) markOldStopped() {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	ledger.oldStopped = true
}

func (ledger *generationConformanceLedger) ambientMutations() int {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	count := 0
	for _, record := range ledger.process {
		if !record.Isolated {
			count++
		}
	}
	return count
}

func (ledger *generationConformanceLedger) liveOwnerResumeWrites(oldThread string) int {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	count := 0
	for _, record := range ledger.provider {
		if record.Generation == "new" && record.Operation == "thread-resume" && record.ThreadID == oldThread && !record.OldStopped {
			count++
		}
	}
	return count
}

func (ledger *generationConformanceLedger) crossThreadWrites(oldThread, newThread string) int {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	count := 0
	expected := map[string]int{
		"old/thread-start/" + oldThread:             1,
		"new/thread-start/" + newThread:             1,
		"old/turn-start/" + oldThread:               1,
		"new/turn-start/" + newThread:               1,
		"old/thread-resume/" + oldThread:            1,
		"new/thread-resume/" + newThread:            1,
		"new/thread-resume-after-stop/" + oldThread: 1,
	}
	got := make(map[string]int)
	for _, record := range ledger.provider {
		operation := record.Operation
		if operation == "thread-resume" && record.OldStopped {
			operation += "-after-stop"
		}
		key := record.Generation + "/" + operation + "/" + record.ThreadID
		got[key]++
		if _, ok := expected[key]; !ok {
			count++
		}
	}
	for key, want := range expected {
		if got[key] != want {
			if got[key] > want {
				count += got[key] - want
			} else {
				count += want - got[key]
			}
		}
	}
	return count
}

type generationThreadSnapshot struct {
	ThreadID string
	TurnID   string
}

func generationStoreCorruptions(stateDomain string, snapshots []generationThreadSnapshot, oldThread, oldTurn, newThread, newTurn string) int {
	corruptions := 0
	want := []generationThreadSnapshot{
		{ThreadID: oldThread, TurnID: oldTurn}, {ThreadID: newThread, TurnID: newTurn},
		{ThreadID: oldThread, TurnID: oldTurn}, {ThreadID: newThread, TurnID: newTurn},
	}
	if len(snapshots) != len(want) {
		corruptions++
	} else {
		for index := range want {
			if snapshots[index] != want[index] {
				corruptions++
			}
		}
	}
	rolloutCounts := map[string]int{oldThread: 0, newThread: 0}
	err := filepath.WalkDir(filepath.Join(stateDomain, "sessions"), func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), "rollout-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
			return nil
		}
		for threadID := range rolloutCounts {
			if strings.Contains(entry.Name(), threadID) {
				rolloutCounts[threadID]++
			}
		}
		return nil
	})
	if err != nil {
		corruptions++
	}
	for _, count := range rolloutCounts {
		if count != 1 {
			corruptions++
		}
	}
	return corruptions
}

type privateGeneration struct {
	command    *exec.Cmd
	exited     chan error
	socket     string
	closed     bool
	readyInfo  fs.FileInfo
	ledger     *generationConformanceLedger
	generation string
}

func startPrivateGeneration(t *testing.T, ctx context.Context, binary, stateDomain, socket string, ledger *generationConformanceLedger, generation string) *privateGeneration {
	t.Helper()
	command := exec.CommandContext(ctx, binary, "app-server", "--listen", "unix://"+socket) // #nosec G204 -- exact verified lease binary and private socket.
	command.Env = isolatedEnvironment(os.Environ(), stateDomain)
	command.Stdout, command.Stderr = &boundedBuffer{}, &boundedBuffer{}
	if err := command.Start(); err != nil {
		t.Fatalf("start private generation: %v", err)
	}
	ledger.recordProcess(pathWithin(filepath.Dir(stateDomain), socket), "endpoint-start", generation)
	endpoint := &privateGeneration{command: command, exited: make(chan error, 1), socket: socket, ledger: ledger, generation: generation}
	go func() { endpoint.exited <- command.Wait() }()
	waitForGenerationSocket(t, ctx, endpoint)
	return endpoint
}

func waitForGenerationSocket(t *testing.T, ctx context.Context, endpoint *privateGeneration) {
	t.Helper()
	barrierCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	for {
		select {
		case err := <-endpoint.exited:
			endpoint.closed = true
			t.Fatalf("private generation exited before ready: %v", err)
		default:
		}
		if info, err := os.Lstat(endpoint.socket); err == nil && info.Mode()&os.ModeSocket != 0 {
			client, openErr := codexappserver.OpenPrivateUnix(barrierCtx, endpoint.socket, time.Second, "generation-qualification", true)
			if openErr == nil {
				endpoint.readyInfo = info
				_ = client.Close()
				return
			}
		}
		select {
		case <-barrierCtx.Done():
			t.Fatal("private generation readiness barrier timed out")
		default:
			runtime.Gosched()
		}
	}
}

func (endpoint *privateGeneration) crash(t *testing.T, ctx context.Context) {
	t.Helper()
	if endpoint.closed {
		return
	}
	endpoint.ledger.recordProcess(true, "endpoint-crash", endpoint.generation)
	if err := endpoint.command.Process.Kill(); err != nil {
		t.Fatalf("crash exact private generation: %v", err)
	}
	endpoint.waitGone(t, ctx)
}

func (endpoint *privateGeneration) stop(t *testing.T, ctx context.Context) {
	t.Helper()
	if endpoint.closed {
		return
	}
	endpoint.ledger.recordProcess(true, "endpoint-stop", endpoint.generation)
	if err := endpoint.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("stop exact private generation: %v", err)
	}
	endpoint.waitGone(t, ctx)
}

func (endpoint *privateGeneration) waitGone(t *testing.T, ctx context.Context) {
	t.Helper()
	barrierCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	select {
	case <-endpoint.exited:
		endpoint.closed = true
	case <-barrierCtx.Done():
		t.Fatal("private generation process exit barrier timed out")
	}
	info, err := os.Lstat(endpoint.socket)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("observe private generation socket: %v", err)
	}
	if !endpoint.closed || endpoint.readyInfo == nil || !os.SameFile(endpoint.readyInfo, info) {
		t.Fatal("refusing to remove a replacement private generation socket")
	}
	if err := os.Remove(endpoint.socket); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("remove exact stopped private socket: %v", err)
	}
	if _, err := os.Lstat(endpoint.socket); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("exact stopped private socket remains: %v", err)
	}
}

func (endpoint *privateGeneration) cleanup(t *testing.T) {
	t.Helper()
	if endpoint == nil || endpoint.closed {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	endpoint.stop(t, ctx)
}

type observedGenerationClient struct {
	*codexappserver.Client
	generation string
	ledger     *generationConformanceLedger
}

func openPrivateGeneration(t *testing.T, ctx context.Context, socket, generation string, ledger *generationConformanceLedger) *observedGenerationClient {
	t.Helper()
	client, err := codexappserver.OpenPrivateUnix(ctx, socket, 10*time.Second, "generation-qualification", true)
	if err != nil {
		t.Fatalf("open private generation: %v", err)
	}
	return &observedGenerationClient{Client: client, generation: generation, ledger: ledger}
}

func (client *observedGenerationClient) StartThread(ctx context.Context, workspace string, roots []string) (codexappserver.ThreadBinding, error) {
	binding, err := client.Client.StartThread(ctx, workspace, roots)
	client.ledger.recordProvider(client.generation, "thread-start", binding.ThreadID)
	return binding, err
}

func (client *observedGenerationClient) StartTurn(ctx context.Context, threadID, prompt, requestKey string) (string, error) {
	client.ledger.recordProvider(client.generation, "turn-start", threadID)
	return client.Client.StartTurn(ctx, threadID, prompt, requestKey)
}

func (client *observedGenerationClient) ResumeThread(ctx context.Context, threadID, workspace string, roots []string) (codexappserver.ThreadBinding, error) {
	client.ledger.recordProvider(client.generation, "thread-resume", threadID)
	return client.Client.ResumeThread(ctx, threadID, workspace, roots)
}

func createConcurrentGenerationThreads(t *testing.T, ctx context.Context, oldClient, newClient *observedGenerationClient, workspace string) (codexappserver.ThreadBinding, codexappserver.ThreadBinding) {
	t.Helper()
	var oldThread, newThread codexappserver.ThreadBinding
	var oldErr, newErr error
	var group sync.WaitGroup
	group.Add(2)
	go func() { defer group.Done(); oldThread, oldErr = oldClient.StartThread(ctx, workspace, nil) }()
	go func() { defer group.Done(); newThread, newErr = newClient.StartThread(ctx, workspace, nil) }()
	group.Wait()
	if oldErr != nil || newErr != nil {
		t.Fatalf("concurrent thread/start old=%v new=%v", oldErr, newErr)
	}
	return oldThread, newThread
}

func turnConcurrentGenerationThreads(t *testing.T, ctx context.Context, oldClient, newClient *observedGenerationClient, oldThread, newThread string) (string, string) {
	t.Helper()
	var oldTurn, newTurn string
	var oldErr, newErr error
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		oldTurn, oldErr = oldClient.StartTurn(ctx, oldThread, "Reply with exactly OLD_OK. Do not use tools.", "generation-old-canary")
	}()
	go func() {
		defer group.Done()
		newTurn, newErr = newClient.StartTurn(ctx, newThread, "Reply with exactly NEW_OK. Do not use tools.", "generation-new-canary")
	}()
	group.Wait()
	if oldErr != nil || newErr != nil {
		t.Fatalf("concurrent turn/start old=%v new=%v", oldErr, newErr)
	}
	return oldTurn, newTurn
}

func waitForGenerationTurn(t *testing.T, ctx context.Context, client *codexappserver.Client, threadID, turnID string) codexappserver.LifecycleSnapshot {
	t.Helper()
	barrierCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	for {
		select {
		case <-barrierCtx.Done():
			t.Fatal("generation turn completion barrier timed out")
		case notification, ok := <-client.Notifications():
			if !ok {
				t.Fatal("generation notification stream closed before turn completion")
			}
			event, recognized, err := codexappserver.DecodeLifecycleEvent(notification)
			if err != nil {
				t.Fatalf("decode generation lifecycle event: %v", err)
			}
			if !recognized || event.ThreadID != threadID || event.TurnID != turnID {
				continue
			}
			if event.Kind == codexappserver.LifecycleApprovalPending {
				_, _ = client.InterruptExactTurn(barrierCtx, threadID, turnID)
				t.Fatal("qualification turn requested approval")
			}
			if event.Kind != codexappserver.LifecycleTurnCompleted {
				continue
			}
			switch event.TurnState {
			case codexappserver.TurnStateCompleted:
				return codexappserver.LifecycleSnapshot{ThreadID: threadID, TurnID: turnID, TurnState: event.TurnState}
			case codexappserver.TurnStateFailed, codexappserver.TurnStateInterrupted:
				t.Fatalf("qualification turn ended %s", event.TurnState)
			default:
				t.Fatalf("qualification turn ended with unknown state %s", event.TurnState)
			}
		}
	}
}

func assertGenerationReadList(t *testing.T, ctx context.Context, client *observedGenerationClient, ownThread, siblingThread string) {
	t.Helper()
	thread, err := client.ReadCatalogThread(ctx, ownThread)
	if err != nil || thread.ID != ownThread {
		t.Fatalf("read own thread=%q err=%v", thread.ID, err)
	}
	page, err := client.ListCatalogThreads(ctx, codexappserver.CatalogQuery{})
	if err != nil {
		t.Fatalf("list shared state catalog: %v", err)
	}
	for _, exact := range []string{ownThread, siblingThread} {
		count := 0
		for _, got := range page.Threads {
			if got.ID == exact {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("shared catalog exact thread count=%d want=1", count)
		}
	}
}

func exactGenerationBinary(t *testing.T, envName, wantVersion string) string {
	t.Helper()
	path := filepath.Clean(strings.TrimSpace(os.Getenv(envName)))
	if !filepath.IsAbs(path) {
		t.Fatalf("%s must be absolute", envName)
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		t.Fatalf("%s must name an executable regular file", envName)
	}
	command := exec.Command(path, "--version") // #nosec G204 -- explicit installed candidate.
	command.Env = isolatedEnvironment(os.Environ(), filepath.Join(t.TempDir(), "version-home"))
	output, err := command.Output()
	if err != nil || !strings.HasSuffix(strings.TrimSpace(string(output)), wantVersion) {
		t.Fatalf("%s is not exact Codex %s", envName, wantVersion)
	}
	return path
}

func copySharedCodexConfig(t *testing.T, source, target string) {
	t.Helper()
	for _, name := range []string{"auth.json", "config.toml"} {
		input := filepath.Join(source, name)
		info, err := os.Stat(input)
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
			t.Fatalf("shared Codex %s source is unavailable", name)
		}
		in, err := os.Open(input) // #nosec G304 -- explicit operator source and closed filenames.
		if err != nil {
			t.Fatalf("open shared Codex %s", name)
		}
		out, err := os.OpenFile(filepath.Join(target, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- exact private fixture target.
		if err != nil {
			_ = in.Close()
			t.Fatalf("create private shared Codex %s", name)
		}
		_, copyErr := io.Copy(out, in)
		inErr, outErr := in.Close(), out.Close()
		if copyErr != nil || inErr != nil || outErr != nil {
			t.Fatalf("copy shared Codex %s failed", name)
		}
	}
}

func sharedConfigPrivate(t *testing.T, target string) bool {
	t.Helper()
	for _, name := range []string{"auth.json", "config.toml"} {
		info, err := os.Stat(filepath.Join(target, name))
		if err != nil || info.Mode().Perm() != 0o600 || info.Size() <= 0 {
			return false
		}
	}
	return true
}

func leaseInstalledBundle(t *testing.T, store, binary, version string, protocol codexbundle.ProtocolRange) codexbundle.Lease {
	t.Helper()
	real, err := filepath.EvalSymlinks(binary)
	if err != nil {
		t.Fatal(err)
	}
	releaseRoot := filepath.Dir(filepath.Dir(real))
	specs := []codexbundle.ArtifactSpec{
		{Path: "bin/codex", Roles: []codexbundle.Role{codexbundle.RoleServer, codexbundle.RoleTUI}},
		{Path: "bin/codex-code-mode-host", Roles: []codexbundle.Role{codexbundle.RoleHelper}},
		{Path: "codex-path/rg", Roles: []codexbundle.Role{codexbundle.RoleHelper}},
		{Path: "codex-resources/bwrap", Roles: []codexbundle.Role{codexbundle.RoleHelper}},
	}
	source := filepath.Join(filepath.Dir(store), "source-"+version)
	for _, spec := range specs {
		from, to := filepath.Join(releaseRoot, filepath.FromSlash(spec.Path)), filepath.Join(source, filepath.FromSlash(spec.Path))
		if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(from, to); err != nil {
			t.Fatalf("stage installed bundle source link: %v", err)
		}
	}
	manifest, err := codexbundle.Inspect(source, version, protocol, specs)
	if err != nil {
		t.Fatalf("inspect installed bundle: %v", err)
	}
	lease, err := codexbundle.Create(store, source, manifest, protocol)
	if err != nil {
		t.Fatalf("lease installed bundle: %v", err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(source); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("bundle source removal failed: %v", err)
	}
	return lease
}

func bundleVersions(t *testing.T, ctx context.Context, ledger *generationConformanceLedger, oldLease, newLease codexbundle.Lease) (string, bool) {
	t.Helper()
	versions := make([]string, 0, 2)
	for _, lease := range []codexbundle.Lease{oldLease, newLease} {
		paths := lease.Paths(codexbundle.RoleTUI)
		if len(paths) != 1 {
			t.Fatal("bundle has no exact TUI")
		}
		command := exec.Command(paths[0], "--version") // #nosec G204 -- exact verified leased path.
		ledger.recordProcess(pathWithin(lease.Root, paths[0]), "leased-tui-version", lease.Manifest.Version)
		output, err := command.Output()
		if err != nil {
			return "", false
		}
		fields := strings.Fields(string(output))
		if len(fields) == 0 {
			return "", false
		}
		versions = append(versions, fields[len(fields)-1])
		helpers := lease.Paths(codexbundle.RoleHelper)
		if len(helpers) != 3 || !slices.ContainsFunc(helpers, func(path string) bool { return filepath.Base(path) == "codex-code-mode-host" }) {
			return "", false
		}
		for _, helper := range helpers {
			flag := "--version"
			if filepath.Base(helper) == "codex-code-mode-host" {
				flag = "--help"
			}
			helperCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			ledger.recordProcess(pathWithin(lease.Root, helper), "leased-helper-launch", lease.Manifest.Version)
			err := exec.CommandContext(helperCtx, helper, flag).Run() // #nosec G204 -- exact verified leased helper path.
			cancel()
			if err != nil {
				return "", false
			}
		}
	}
	return strings.Join(versions, "/"), true
}
