package codexinstalled

import (
	"strings"
	"time"

	payloadcap "github.com/crevissepartners/projmux/internal/integrations/agents/codexgeneration"
)

// PayloadFreeStageObservation is a private-fixture-only transient. Opaque
// provider identities exist only long enough to be equality-checked and
// hashed; prompts, turn items, responses, and paths are not fields and cannot
// enter the immutable capability record.
type PayloadFreeStageObservation struct {
	Outcome            payloadcap.StageOutcome
	Reason             string
	ThreadID           string
	TurnID             string
	ExactThread        bool
	ExactTurn          bool
	PaneAlive          bool
	FirstInputObserved bool
	TurnCount          int
}

type PayloadFreeObservation struct {
	ZeroTurnStart   PayloadFreeStageObservation
	IndependentRead PayloadFreeStageObservation
	StoredResume    PayloadFreeStageObservation
	RemoteNew       PayloadFreeStageObservation
	FirstRealInput  PayloadFreeStageObservation
}

// QualifyPayloadFreeObservation is the private installed-conformance seam. It
// reduces raw opaque IDs to digests before handing evidence to the product
// authority, so a returned/persisted Record is content-free.
func QualifyPayloadFreeObservation(tuple payloadcap.Tuple, observedAt time.Time, observation PayloadFreeObservation) (payloadcap.Record, error) {
	stage := func(kind payloadcap.Stage, raw PayloadFreeStageObservation) payloadcap.StageEvidence {
		threadDigest, turnDigest := "", ""
		if value := strings.TrimSpace(raw.ThreadID); value != "" {
			threadDigest = payloadcap.DigestString(value)
		}
		if value := strings.TrimSpace(raw.TurnID); value != "" {
			turnDigest = payloadcap.DigestString(value)
		}
		return payloadcap.StageEvidence{
			Stage: kind, Outcome: raw.Outcome, Reason: raw.Reason,
			ThreadSHA256: threadDigest, TurnSHA256: turnDigest,
			ExactThread: raw.ExactThread, ExactTurn: raw.ExactTurn, PaneAlive: raw.PaneAlive,
			FirstInputObserved: raw.FirstInputObserved, TurnCount: raw.TurnCount,
		}
	}
	return payloadcap.Qualify(tuple, observedAt, payloadcap.Evidence{
		ZeroTurnStart:   stage(payloadcap.StageZeroTurnStart, observation.ZeroTurnStart),
		IndependentRead: stage(payloadcap.StageIndependentRead, observation.IndependentRead),
		StoredResume:    stage(payloadcap.StageStoredResume, observation.StoredResume),
		RemoteNew:       stage(payloadcap.StageRemoteNew, observation.RemoteNew),
		FirstRealInput:  stage(payloadcap.StageFirstRealInput, observation.FirstRealInput),
	})
}
