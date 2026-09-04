package codexinstalled

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/crevissepartners/projmux/internal/integrations/agents/codexappserver"
	payloadcap "github.com/crevissepartners/projmux/internal/integrations/agents/codexgeneration"
)

const (
	payloadFreeSmokeRootEnv = "PROJMUX_CODEX_PAYLOAD_FREE_SMOKE_ROOT"
	payloadFreeStateEnv     = "PROJMUX_CODEX_PAYLOAD_FREE_SOURCE_HOME"
	code01520Env            = "PROJMUX_CODEX_PAYLOAD_FREE_0152_0"
	code01521Env            = "PROJMUX_CODEX_PAYLOAD_FREE_0152_1"
	code01530Env            = "PROJMUX_CODEX_PAYLOAD_FREE_0153_0"
)

// TestInstalledExactPayloadFreeCapabilityMatrix is the installed C-1 owner.
// Every row uses an exact binary, copied private state, private app-server,
// private tmux server, and fixed content-free evidence. Missing binaries are
// reported as unavailable and never synthesized or promoted to PASS.
func TestInstalledExactPayloadFreeCapabilityMatrix(t *testing.T) {
	root, enabled, err := SmokeRoot(payloadFreeSmokeRootEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Skipf("set %s for exact payload-free qualification", payloadFreeSmokeRootEnv)
	}
	if err := validatePayloadFreeSmokeRoot(root); err != nil {
		t.Fatal(err)
	}
	if err := validateInheritedEnvironment(); err != nil {
		t.Fatal(err)
	}
	stateSource := filepath.Clean(strings.TrimSpace(os.Getenv(payloadFreeStateEnv)))
	if !filepath.IsAbs(stateSource) {
		t.Fatalf("%s must be absolute", payloadFreeStateEnv)
	}
	ambient := captureAmbientEndpoint(t, stateSource)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("payload-free smoke root must start empty: entries=%d err=%v", len(entries), err)
	}
	rootRemoved := false
	t.Cleanup(func() {
		if !rootRemoved {
			if err := os.RemoveAll(root); err != nil {
				t.Errorf("remove payload-free smoke root: %v", err)
			}
		}
	})

	type candidate struct{ version, env string }
	candidates := []candidate{{"0.152.0", code01520Env}, {"0.152.1", code01521Env}, {"0.153.0", code01530Env}}
	available := 0
	for _, candidate := range candidates {
		raw := strings.TrimSpace(os.Getenv(candidate.env))
		if raw == "" {
			t.Logf("payload-free-tuple version=%s availability=unavailable env=%s", candidate.version, candidate.env)
			continue
		}
		available++
		binary := exactGenerationBinary(t, candidate.env, candidate.version)
		for _, creator := range []string{"creator-live", "creator-closed"} {
			t.Run(candidate.version+"/"+creator, func(t *testing.T) {
				runInstalledPayloadFreeRow(t, root, stateSource, binary, candidate.version, creator)
			})
		}
	}
	if available == 0 {
		t.Fatal("payload-free matrix has no exact available binary")
	}
	assertAmbientEndpointUnchanged(t, ambient)
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	rootRemoved = true
	if _, err := os.Lstat(root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("payload-free exact cleanup incomplete: %v", err)
	}
}

func runInstalledPayloadFreeRow(t *testing.T, root, stateSource, binary, version, creatorOrdering string) {
	t.Helper()
	rowRoot := filepath.Join(root, strings.ReplaceAll(version, ".", "-"), creatorOrdering)
	stateDomain := filepath.Join(rowRoot, "codex-home")
	workspace := filepath.Join(rowRoot, "workspace")
	for _, path := range []string{stateDomain, workspace} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	copySharedCodexConfig(t, stateSource, stateDomain)
	ledger := &generationConformanceLedger{}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	socket := filepath.Join(rowRoot, "app-server.sock")
	endpoint := startPrivateGeneration(t, ctx, binary, stateDomain, socket, ledger, version+"-"+creatorOrdering)
	t.Cleanup(func() { endpoint.cleanup(t) })
	route, err := payloadcap.IdentifySocketRoute(socket)
	if err != nil {
		t.Fatal(err)
	}
	tuple, err := payloadcap.NewTuple(binary, binary, version,
		payloadcap.ProtocolIdentity{Transport: "unix-websocket-jsonrpc", Schema: "v2"}, route,
		"private-"+strings.ReplaceAll(version, ".", "-"), stateDomain)
	if err != nil {
		t.Fatal(err)
	}
	creator := openPrivateGeneration(t, ctx, socket, "creator", ledger)
	binding, err := creator.StartThread(ctx, workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if creatorOrdering == "creator-closed" {
		if err := creator.Close(); err != nil {
			t.Fatal(err)
		}
		creator = nil
	}
	reader := openPrivateGeneration(t, ctx, socket, "reader", ledger)
	read, readErr := reader.ReadCatalogThread(ctx, binding.ThreadID)
	_ = reader.Close()
	readExact := readErr == nil && read.ID == binding.ThreadID

	resumer := openPrivateGeneration(t, ctx, socket, "resumer", ledger)
	resumed, resumeErr := resumer.ResumeThread(ctx, binding.ThreadID, workspace, nil)
	_ = resumer.Close()
	if creator != nil {
		_ = creator.Close()
	}
	resumeOutcome, resumeReason := payloadcap.StageUnknown, "resume-indeterminate"
	resumeExact := false
	switch {
	case resumeErr == nil && resumed.ThreadID == binding.ThreadID:
		resumeOutcome, resumeReason, resumeExact = payloadcap.StagePass, "stored-resume-exact", true
	case errors.Is(resumeErr, codexappserver.ErrThreadNotDurable):
		resumeOutcome, resumeReason, resumeExact = payloadcap.StageUnsupported, "no-rollout-found", true
	case resumeErr != nil:
		resumeReason = "resume-protocol-error"
		t.Logf("stored-resume-classification=%T:%v", resumeErr, resumeErr)
	}

	observer := openPrivateGeneration(t, ctx, socket, "remote-observer", ledger)
	baseline, baselineErr := observer.ListLoadedThreadIDs(ctx)
	fixture := &Fixture{Root: rowRoot, CodexHome: stateDomain, Workspace: workspace}
	pane, paneErr := fixture.StartRemoteNewPane(ctx, binary, socket)
	remoteThread := ""
	paneAlive := false
	var aliveErr error
	if paneErr == nil {
		paneAlive, aliveErr = pane.Alive(ctx)
		if aliveErr == nil && paneAlive && baselineErr == nil {
			remoteThread = waitForOneNewLoadedThread(ctx, observer.Client, baseline)
			if remoteThread != "" {
				snapshot, snapshotErr := observer.ReadLifecycleSnapshot(ctx, remoteThread)
				if snapshotErr != nil {
					t.Fatalf("read content-free remote-new lifecycle: %v", snapshotErr)
				}
				if snapshot.ThreadID != remoteThread || snapshot.TurnCount != 0 || snapshot.TurnID != "" {
					t.Fatalf("remote-new content-free lifecycle = thread %q turns %d current turn %q", snapshot.ThreadID, snapshot.TurnCount, snapshot.TurnID)
				}
			}
		}
	}
	remoteEvidence := reduceContentFreeRemoteNew(paneErr, aliveErr, paneAlive, baselineErr == nil, remoteThread)
	firstOutcome, firstReason := payloadcap.StageUnknown, "first-input-unproven"
	if pane != nil {
		if err := pane.Close(ctx); err != nil {
			t.Errorf("close remote-new Pane: %v", err)
		}
	}
	_ = observer.Close()

	observation := PayloadFreeObservation{
		ZeroTurnStart: PayloadFreeStageObservation{
			Outcome: payloadcap.StagePass, Reason: "zero-turn-start-exact", ThreadID: binding.ThreadID, ExactThread: true,
		},
		IndependentRead: PayloadFreeStageObservation{
			Outcome:  map[bool]payloadcap.StageOutcome{true: payloadcap.StagePass, false: payloadcap.StageUnknown}[readExact],
			Reason:   map[bool]string{true: "independent-read-exact", false: "independent-read-failed"}[readExact],
			ThreadID: binding.ThreadID, ExactThread: readExact,
		},
		StoredResume: PayloadFreeStageObservation{
			Outcome: resumeOutcome, Reason: resumeReason, ThreadID: binding.ThreadID, ExactThread: resumeExact,
		},
		RemoteNew: remoteEvidence,
		FirstRealInput: PayloadFreeStageObservation{
			Outcome: firstOutcome, Reason: firstReason,
		},
	}
	record, err := QualifyPayloadFreeObservation(tuple, time.Now().UTC(), observation)
	if err != nil {
		t.Fatal(err)
	}
	if version == "0.153.0" && (record.Evidence.IndependentRead.Outcome != payloadcap.StagePass || record.DurableResume.Verdict != payloadcap.VerdictUnsupported) {
		t.Fatalf("exact 0.153.0 read/stored separation = %#v", record)
	}
	encoded, err := record.JSON()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("payload-free-capability=%s", encoded)
	if mutations := ledger.ambientMutations(); mutations != 0 {
		t.Fatalf("payload-free ambient lifecycle mutations = %d", mutations)
	}
	if turns := payloadFreeTurnWrites(ledger); turns != 0 {
		t.Fatalf("payload-free outbound turn/input writes = %d", turns)
	}

	endpoint.stop(t, ctx)
	if _, err := payloadcap.IdentifySocketRoute(socket); err == nil {
		t.Fatal("cleaned private endpoint remained a current capability tuple")
	}
	if err := os.RemoveAll(rowRoot); err != nil {
		t.Fatal(err)
	}
}

func validatePayloadFreeSmokeRoot(root string) error {
	for _, version := range []string{"0-152-0", "0-152-1", "0-153-0"} {
		for _, creator := range []string{"creator-live", "creator-closed"} {
			tmuxRoot := filepath.Join(root, version, creator, "tmux")
			if err := validateCapabilityTMUXSocketRoot(tmuxRoot); err != nil {
				return err
			}
		}
	}
	return nil
}

func reduceContentFreeRemoteNew(paneErr, aliveErr error, paneAlive, baselineExact bool, threadID string) PayloadFreeStageObservation {
	switch {
	case paneErr != nil:
		return PayloadFreeStageObservation{Outcome: payloadcap.StageUnknown, Reason: "remote-new-launch-indeterminate"}
	case aliveErr != nil:
		return PayloadFreeStageObservation{Outcome: payloadcap.StageUnknown, Reason: "remote-new-liveness-indeterminate"}
	case !paneAlive:
		return PayloadFreeStageObservation{Outcome: payloadcap.StageUnsupported, Reason: "remote-new-tui-exited"}
	case baselineExact && threadID != "":
		return PayloadFreeStageObservation{
			Outcome: payloadcap.StageUnknown, Reason: "content-free-thread-visible",
			ThreadID: threadID, ExactThread: true, PaneAlive: true,
		}
	default:
		return PayloadFreeStageObservation{Outcome: payloadcap.StageUnknown, Reason: "content-free-live-only", PaneAlive: true}
	}
}

func waitForOneNewLoadedThread(ctx context.Context, client *codexappserver.Client, baseline []string) string {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	known := make(map[string]struct{}, len(baseline))
	for _, id := range baseline {
		known[id] = struct{}{}
	}
	for ctx.Err() == nil {
		ids, err := client.ListLoadedThreadIDs(ctx)
		if err == nil {
			newIDs := make([]string, 0, 1)
			for _, id := range ids {
				if _, exists := known[id]; !exists {
					newIDs = append(newIDs, id)
				}
			}
			if len(newIDs) == 1 {
				return newIDs[0]
			}
			if len(newIDs) > 1 {
				return ""
			}
		}
		runtime.Gosched()
	}
	return ""
}

func payloadFreeTurnWrites(ledger *generationConformanceLedger) int {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	turns := 0
	for _, record := range ledger.provider {
		if record.Operation == "turn-start" {
			turns++
		}
	}
	return turns
}
