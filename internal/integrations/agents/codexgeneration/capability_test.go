package codexgeneration

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testTuple() Tuple {
	return Tuple{
		RoleTUI:          BinaryIdentity{SHA256: DigestString("role-tui"), Size: 101},
		RoleAppServer:    BinaryIdentity{SHA256: DigestString("role-app-server"), Size: 102},
		AppServerVersion: "0.153.0",
		Protocol:         ProtocolIdentity{Transport: "unix-websocket-jsonrpc", Schema: "v2"},
		SocketRoute:      SocketRouteIdentity{Kind: "private-unix", LocatorSHA256: DigestString("socket-a"), RuntimeSHA256: DigestString("socket-runtime-a")},
		StateDomainID:    "state-domain-a", StateDomainSHA256: DigestString("state-path-a"),
		Platform: "linux", Architecture: "amd64",
	}
}

func TestCapabilityCacheRejectsWrongRoleTUIEndpointRestartSocketReboundAndStateDomainMismatch(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "private.sock")
	listen := func() net.Listener {
		listener, err := net.Listen("unix", socket)
		if err != nil {
			t.Fatal(err)
		}
		return listener
	}
	firstListener := listen()
	firstRoute, err := IdentifySocketRoute(socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstListener.Close(); err != nil {
		t.Fatal(err)
	}
	secondListener := listen()
	t.Cleanup(func() { _ = secondListener.Close() })
	secondRoute, err := IdentifySocketRoute(socket)
	if err != nil {
		t.Fatal(err)
	}
	if firstRoute == secondRoute {
		t.Fatal("endpoint restart reused the prior bound socket identity")
	}

	tuple := testTuple()
	tuple.SocketRoute = firstRoute
	remote := StageEvidence{Stage: StageRemoteNew, Outcome: StagePass, Reason: "tui-live", PaneAlive: true, ExactThread: true}
	first := StageEvidence{Stage: StageFirstRealInput, Outcome: StagePass, Reason: "first-input", FirstInputObserved: true, ExactThread: true, ExactTurn: true, TurnCount: 1}
	record, err := Qualify(tuple, time.Now(), testEvidence(StagePass, remote, first))
	if err != nil {
		t.Fatal(err)
	}
	cache := Cache{Root: filepath.Join(root, "cache")}
	if err := cache.Publish(record); err != nil {
		t.Fatal(err)
	}
	mutants := map[string]Tuple{}
	wrongTUI := tuple
	wrongTUI.RoleTUI.SHA256 = DigestString("wrong-role-tui")
	mutants["wrong RoleTUI"] = wrongTUI
	restarted := tuple
	restarted.SocketRoute = secondRoute
	mutants["endpoint restart/socket rebound"] = restarted
	wrongState := tuple
	wrongState.StateDomainID = "other-state-domain"
	mutants["state-domain mismatch"] = wrongState
	for name, mutant := range mutants {
		t.Run(name, func(t *testing.T) {
			projection := Project(cache.Resolve(mutant))
			if projection.DurableResume != VerdictUnknown || projection.RemoteNew != VerdictUnknown || projection.CreateRoute != CreateRoutePlainFallback {
				t.Fatalf("negative tuple projection = %#v", projection)
			}
		})
	}
}

func TestCapabilityCacheCorruptFutureAndTrailingRecordsResolveUnknown(t *testing.T) {
	remote := StageEvidence{Stage: StageRemoteNew, Outcome: StagePass, Reason: "tui-live", PaneAlive: true, ExactThread: true}
	first := StageEvidence{Stage: StageFirstRealInput, Outcome: StagePass, Reason: "first-input", FirstInputObserved: true, ExactThread: true, ExactTurn: true, TurnCount: 1}
	record, err := Qualify(testTuple(), time.Now(), testEvidence(StagePass, remote, first))
	if err != nil {
		t.Fatal(err)
	}
	cache := Cache{Root: filepath.Join(t.TempDir(), "capabilities")}
	path, err := cache.path(record.CacheKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	valid, err := record.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var future map[string]any
	if err := json.Unmarshal(valid, &future); err != nil {
		t.Fatal(err)
	}
	future["schema_version"] = SchemaVersion + 1
	futureBytes, _ := json.Marshal(future)
	for name, encoded := range map[string][]byte{
		"corrupt": []byte("{"), "future": futureBytes, "trailing": append(append([]byte(nil), valid...), []byte("\n{}")...),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			resolved := cache.Resolve(record.Tuple)
			projection := Project(resolved)
			if projection.DurableResume != VerdictUnknown || projection.RemoteNew != VerdictUnknown || projection.CreateRoute != CreateRoutePlainFallback {
				t.Fatalf("invalid cache resolved = %#v", projection)
			}
		})
	}
}

func testEvidence(stored StageOutcome, remote, first StageEvidence) Evidence {
	thread := DigestString("durable-thread-opaque")
	for _, stage := range []*StageEvidence{&remote, &first} {
		if stage.Outcome == StagePass && stage.ThreadSHA256 == "" {
			stage.ThreadSHA256 = DigestString("remote-thread-opaque")
		}
	}
	if first.Outcome == StagePass && first.TurnSHA256 == "" {
		first.TurnSHA256 = DigestString("remote-turn-opaque")
	}
	return Evidence{
		ZeroTurnStart:   StageEvidence{Stage: StageZeroTurnStart, Outcome: StagePass, Reason: "started", ThreadSHA256: thread, ExactThread: true},
		IndependentRead: StageEvidence{Stage: StageIndependentRead, Outcome: StagePass, Reason: "read-visible", ThreadSHA256: thread, ExactThread: true},
		StoredResume:    StageEvidence{Stage: StageStoredResume, Outcome: stored, Reason: map[StageOutcome]string{StagePass: "stored-resume-exact", StageUnsupported: "no-rollout-found", StageUnknown: "deadline"}[stored], ThreadSHA256: thread, ExactThread: true},
		RemoteNew:       remote, FirstRealInput: first,
	}
}

func unknownStage(stage Stage) StageEvidence {
	return StageEvidence{Stage: stage, Outcome: StageUnknown, Reason: "not-observed"}
}

// TestExactPayloadFreeCapabilitySeparatesReadVisibleFromStoredResumable is the
// C-1 durable-zero-turn-resume enforcement owner. It models the exact 0.153.0
// observation: start and independent read pass while stored resume is an
// independently unsupported predicate.
func TestExactPayloadFreeCapabilitySeparatesReadVisibleFromStoredResumable(t *testing.T) {
	record, err := Qualify(testTuple(), time.Date(2026, 9, 3, 15, 0, 0, 0, time.UTC),
		testEvidence(StageUnsupported, unknownStage(StageRemoteNew), unknownStage(StageFirstRealInput)))
	if err != nil {
		t.Fatal(err)
	}
	if record.Evidence.IndependentRead.Outcome != StagePass || record.Evidence.StoredResume.Outcome != StageUnsupported ||
		record.DurableResume.Verdict != VerdictUnsupported || record.DurableResume.Reason != "no-rollout-found" {
		t.Fatalf("0.153.0 read/stored verdict = %#v", record)
	}
	encoded, err := record.JSON()
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := DecodeRecord(encoded)
	if err != nil || reopened != record {
		t.Fatalf("record fixed point = %#v, %v", reopened, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		t.Fatal(err)
	}
	raw["evidence_sha256"] = DigestString("tampered")
	tampered, _ := json.Marshal(raw)
	if _, err := DecodeRecord(tampered); err == nil {
		t.Fatal("tampered evidence digest was accepted")
	}
}

// TestRemoteNewCapabilityRequiresExactFirstRealInputThreadAndTurn is the C-1
// remote-new-session enforcement owner. A living TUI alone can never publish
// PASS; the first real input must yield one exact thread/turn observation.
func TestRemoteNewCapabilityRequiresExactFirstRealInputThreadAndTurn(t *testing.T) {
	remote := StageEvidence{Stage: StageRemoteNew, Outcome: StagePass, Reason: "tui-live", PaneAlive: true, ExactThread: true}
	first := unknownStage(StageFirstRealInput)
	record, err := Qualify(testTuple(), time.Now(), testEvidence(StageUnsupported, remote, first))
	if err != nil {
		t.Fatal(err)
	}
	if record.RemoteNew.Verdict != VerdictUnknown {
		t.Fatalf("TUI liveness published remote-new PASS: %#v", record.RemoteNew)
	}
	first = StageEvidence{Stage: StageFirstRealInput, Outcome: StagePass, Reason: "first-input", FirstInputObserved: true, ExactThread: true, ExactTurn: true, TurnCount: 1}
	record, err = Qualify(testTuple(), time.Now(), testEvidence(StageUnsupported, remote, first))
	if err != nil {
		t.Fatal(err)
	}
	if record.RemoteNew.Verdict != VerdictSupported || record.RemoteNew.Reason != "first-real-input-exact" {
		t.Fatalf("exact first input verdict = %#v", record.RemoteNew)
	}
	for _, mutate := range []func(*Evidence){
		func(e *Evidence) { e.FirstRealInput.ExactThread = false },
		func(e *Evidence) { e.FirstRealInput.ExactTurn = false },
		func(e *Evidence) { e.FirstRealInput.TurnCount = 2 },
		func(e *Evidence) { e.FirstRealInput.FirstInputObserved = false },
		func(e *Evidence) { e.FirstRealInput.ThreadSHA256 = DigestString("unrelated-thread") },
	} {
		evidence := testEvidence(StageUnsupported, remote, first)
		mutate(&evidence)
		got, qualifyErr := Qualify(testTuple(), time.Now(), evidence)
		if qualifyErr != nil {
			t.Fatal(qualifyErr)
		}
		if got.RemoteNew.Verdict != VerdictUnknown {
			t.Fatalf("inexact first input published PASS: %#v", got.RemoteNew)
		}
	}
}

// TestCapabilityCacheInvalidatesEveryExactTupleAxis is the C-1 cache owner.
// Each one-axis drift must produce zero cached PASS reuse.
func TestCapabilityCacheInvalidatesEveryExactTupleAxis(t *testing.T) {
	remote := StageEvidence{Stage: StageRemoteNew, Outcome: StagePass, Reason: "tui-live", PaneAlive: true, ExactThread: true}
	first := StageEvidence{Stage: StageFirstRealInput, Outcome: StagePass, Reason: "first-input", FirstInputObserved: true, ExactThread: true, ExactTurn: true, TurnCount: 1}
	record, err := Qualify(testTuple(), time.Now(), testEvidence(StagePass, remote, first))
	if err != nil {
		t.Fatal(err)
	}
	cache := Cache{Root: filepath.Join(t.TempDir(), "capabilities")}
	if err := cache.Publish(record); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := cache.Lookup(record.Tuple); err != nil || !ok || got != record {
		t.Fatalf("exact cache lookup = %#v, %t, %v", got, ok, err)
	}
	mutations := map[string]func(*Tuple){
		"RoleTUI digest":       func(tuple *Tuple) { tuple.RoleTUI.SHA256 = DigestString("changed-tui") },
		"RoleAppServer digest": func(tuple *Tuple) { tuple.RoleAppServer.SHA256 = DigestString("changed-server") },
		"app-server version":   func(tuple *Tuple) { tuple.AppServerVersion = "0.153.1" },
		"protocol":             func(tuple *Tuple) { tuple.Protocol.Schema = "v3" },
		"socket route":         func(tuple *Tuple) { tuple.SocketRoute.LocatorSHA256 = DigestString("changed-route") },
		"socket rebound":       func(tuple *Tuple) { tuple.SocketRoute.RuntimeSHA256 = DigestString("changed-runtime") },
		"state domain id":      func(tuple *Tuple) { tuple.StateDomainID = "state-domain-b" },
		"state domain path":    func(tuple *Tuple) { tuple.StateDomainSHA256 = DigestString("state-path-b") },
		"platform":             func(tuple *Tuple) { tuple.Platform = "darwin" },
		"architecture":         func(tuple *Tuple) { tuple.Architecture = "arm64" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			tuple := record.Tuple
			mutate(&tuple)
			if got, ok, err := cache.Lookup(tuple); err != nil || ok || got != (Record{}) {
				t.Fatalf("drift lookup = %#v, %t, %v", got, ok, err)
			}
			unknown, err := UnknownRecord(tuple)
			if err != nil || Project(unknown).CreateRoute != CreateRoutePlainFallback || Project(unknown).RemoteNew != VerdictUnknown {
				t.Fatalf("drift projection = %#v, %v", Project(unknown), err)
			}
		})
	}
	if err := cache.Publish(record); err != nil {
		t.Fatalf("byte-identical publish is not a fixed point: %v", err)
	}
	requalified := record
	requalified.ObservedAt = requalified.ObservedAt.Add(time.Second)
	requalified.EvidenceSHA256 = requalified.evidenceDigest()
	if err := cache.Publish(requalified); err == nil {
		t.Fatal("immutable record was overwritten")
	}
}

func TestProjectionNeverOpensPhase2Route(t *testing.T) {
	remote := StageEvidence{Stage: StageRemoteNew, Outcome: StagePass, Reason: "tui-live", PaneAlive: true, ExactThread: true}
	first := StageEvidence{Stage: StageFirstRealInput, Outcome: StagePass, Reason: "first-input", FirstInputObserved: true, ExactThread: true, ExactTurn: true, TurnCount: 1}
	record, err := Qualify(testTuple(), time.Now(), testEvidence(StagePass, remote, first))
	if err != nil {
		t.Fatal(err)
	}
	projection := Project(record)
	if projection.RemoteNew != VerdictSupported || projection.CreateRoute != CreateRoutePlainFallback || projection.Reason != "phase-2-native-route-disabled" {
		t.Fatalf("Phase 1 projection = %#v", projection)
	}
}
