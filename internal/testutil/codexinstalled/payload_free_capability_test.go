package codexinstalled

import (
	"bytes"
	"errors"
	"testing"
	"time"

	payloadcap "github.com/crevissepartners/projmux/internal/integrations/agents/codexgeneration"
)

func TestContentFreeRemoteNewReductionDoesNotClaimLivenessAfterLaunchFailure(t *testing.T) {
	tests := []struct {
		name          string
		paneErr       error
		aliveErr      error
		paneAlive     bool
		baselineExact bool
		threadID      string
		outcome       payloadcap.StageOutcome
		reason        string
		exactThread   bool
	}{
		{name: "launch failed", paneErr: errors.New("private detail"), outcome: payloadcap.StageUnknown, reason: "remote-new-launch-indeterminate"},
		{name: "liveness failed", aliveErr: errors.New("private detail"), outcome: payloadcap.StageUnknown, reason: "remote-new-liveness-indeterminate"},
		{name: "pane exited", outcome: payloadcap.StageUnsupported, reason: "remote-new-tui-exited"},
		{name: "live only", paneAlive: true, outcome: payloadcap.StageUnknown, reason: "content-free-live-only"},
		{name: "baseline failed", paneAlive: true, threadID: "untrusted-thread", outcome: payloadcap.StageUnknown, reason: "content-free-live-only"},
		{name: "thread visible", paneAlive: true, baselineExact: true, threadID: "exact-thread", outcome: payloadcap.StageUnknown, reason: "content-free-thread-visible", exactThread: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := reduceContentFreeRemoteNew(test.paneErr, test.aliveErr, test.paneAlive, test.baselineExact, test.threadID)
			if got.Outcome != test.outcome || got.Reason != test.reason || got.PaneAlive != test.paneAlive || got.ExactThread != test.exactThread {
				t.Fatalf("content-free remote-new reduction = %#v", got)
			}
			if got.Reason == "content-free-live-only" && !got.PaneAlive {
				t.Fatal("live-only evidence had no live Pane")
			}
		})
	}
}

func TestPayloadFreeConformanceSeamHashesOpaqueIdentityAndRejectsUnrelatedFirstTurn(t *testing.T) {
	digest := payloadcap.DigestString
	tuple := payloadcap.Tuple{
		RoleTUI: payloadcap.BinaryIdentity{SHA256: digest("tui"), Size: 1}, RoleAppServer: payloadcap.BinaryIdentity{SHA256: digest("server"), Size: 1},
		AppServerVersion: "0.153.0", Protocol: payloadcap.ProtocolIdentity{Transport: "unix-websocket-jsonrpc", Schema: "v2"},
		SocketRoute:   payloadcap.SocketRouteIdentity{Kind: "private-unix", LocatorSHA256: digest("route"), RuntimeSHA256: digest("runtime")},
		StateDomainID: "private-fixture", StateDomainSHA256: digest("state"), Platform: "linux", Architecture: "amd64",
	}
	stage := func(outcome payloadcap.StageOutcome, reason, thread string) PayloadFreeStageObservation {
		return PayloadFreeStageObservation{Outcome: outcome, Reason: reason, ThreadID: thread, ExactThread: true}
	}
	observation := PayloadFreeObservation{
		ZeroTurnStart:   stage(payloadcap.StagePass, "start", "durable-thread-private"),
		IndependentRead: stage(payloadcap.StagePass, "read", "durable-thread-private"),
		StoredResume:    stage(payloadcap.StageUnsupported, "no-rollout-found", "durable-thread-private"),
		RemoteNew: PayloadFreeStageObservation{
			Outcome: payloadcap.StagePass, Reason: "remote", ThreadID: "remote-thread-private", ExactThread: true, PaneAlive: true,
		},
		FirstRealInput: PayloadFreeStageObservation{
			Outcome: payloadcap.StagePass, Reason: "first", ThreadID: "unrelated-thread-private", TurnID: "turn-private",
			ExactThread: true, ExactTurn: true, FirstInputObserved: true, TurnCount: 1,
		},
	}
	record, err := QualifyPayloadFreeObservation(tuple, time.Now(), observation)
	if err != nil {
		t.Fatal(err)
	}
	if record.DurableResume.Verdict != payloadcap.VerdictUnsupported || record.RemoteNew.Verdict != payloadcap.VerdictUnknown {
		t.Fatalf("private seam verdicts = %#v/%#v", record.DurableResume, record.RemoteNew)
	}
	encoded, err := record.JSON()
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range [][]byte{[]byte("durable-thread-private"), []byte("remote-thread-private"), []byte("unrelated-thread-private"), []byte("turn-private")} {
		if bytes.Contains(encoded, raw) {
			t.Fatalf("capability record retained opaque identity %q", raw)
		}
	}
}
