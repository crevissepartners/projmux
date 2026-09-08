package codexgeneration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

type QualificationVerdict string

const (
	VerdictYes QualificationVerdict = "yes"
	VerdictNo  QualificationVerdict = "no"
)

type QualificationReason string

const (
	ReasonQualified                 QualificationReason = "qualified"
	ReasonDistinctThreadFailed      QualificationReason = "distinct-thread-concurrency-failed"
	ReasonSameThreadOwnershipFailed QualificationReason = "same-thread-single-owner-failed"
	ReasonBundleLeaseFailed         QualificationReason = "bundle-lease-failed"
	ReasonAuthConfigIsolationFailed QualificationReason = "auth-config-isolation-failed"
	ReasonProtocolMismatch          QualificationReason = "protocol-mismatch"
	ReasonEvidenceIncomplete        QualificationReason = "evidence-incomplete"
)

type VersionPair struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// QualificationEvidence is intentionally boolean/counter-only. It is safe to
// persist because it cannot represent a prompt, response, credential, socket,
// filesystem path, or provider payload.
//
// The fields fall into three groups, and the difference between them is what
// makes a forged receipt detectable at all.
//
// The acceptance booleans say a probe reached its verdict. The violation
// counters say how many times the property under test was broken; a qualified
// pair has them at zero. Both are fully determined at a YES verdict -- every
// boolean true and every violation counter zero -- so a YES receipt carries no
// information a forger has to supply. Recomputing the verdict from them, which
// is all Validate does, therefore proves nothing about whether anything ran.
//
// The coverage counters are the third group and the only one that is not
// determined by the verdict. They say how many observations produced each
// acceptance boolean, and a producer that never ran leaves them at zero while
// still reaching a self-consistent YES. QualificationEvidenceIntegrity is what
// reads them, and GateQualification refuses a receipt whose acceptance claims
// no observation backs.
type QualificationEvidence struct {
	SharedStateDomain         bool `json:"sharedStateDomain"`
	DistinctPrivateEndpoints  bool `json:"distinctPrivateEndpoints"`
	DistinctThreadCreateTurn  bool `json:"distinctThreadCreateTurn"`
	DistinctThreadReadList    bool `json:"distinctThreadReadList"`
	CrashRestart              bool `json:"crashRestart"`
	CrossThreadWrites         int  `json:"crossThreadWrites"`
	StoreCorruptions          int  `json:"storeCorruptions"`
	LiveOwnerResumeWrites     int  `json:"liveOwnerResumeWrites"`
	OldStoppedBeforeResume    bool `json:"oldStoppedBeforeResume"`
	PersistedResumeSnapshot   bool `json:"persistedResumeSnapshot"`
	SharedAuthConfigPrivate   bool `json:"sharedAuthConfigPrivate"`
	BundleSourceRemovalLaunch bool `json:"bundleSourceRemovalLaunch"`
	BundleDriftRefused        bool `json:"bundleDriftRefused"`
	ProtocolMismatchRefused   bool `json:"protocolMismatchRefused"`
	AmbientMutations          int  `json:"ambientMutations"`

	// Coverage counters, schema version 2 onward.
	ObservedThreadTurns    int `json:"observedThreadTurns"`
	ObservedThreadReads    int `json:"observedThreadReads"`
	ObservedCrashRestarts  int `json:"observedCrashRestarts"`
	ObservedBundleLaunches int `json:"observedBundleLaunches"`
}

// The evidence-integrity discriminants.
//
// Each one names the exact field that failed, so a refusal on this path stays
// recoverable: the reason token says the counters are unbacked and the
// discriminant says which claim had nothing behind it.
const (
	EvidenceIntact                     = ""
	EvidenceNegativeCrossThreadWrites  = "crossThreadWrites-negative"
	EvidenceNegativeStoreCorruptions   = "storeCorruptions-negative"
	EvidenceNegativeResumeWrites       = "liveOwnerResumeWrites-negative"
	EvidenceNegativeAmbientMutations   = "ambientMutations-negative"
	EvidenceNegativeThreadTurns        = "observedThreadTurns-negative"
	EvidenceNegativeThreadReads        = "observedThreadReads-negative"
	EvidenceNegativeCrashRestarts      = "observedCrashRestarts-negative"
	EvidenceNegativeBundleLaunches     = "observedBundleLaunches-negative"
	EvidenceUnbackedThreadCreateTurn   = "distinctThreadCreateTurn-unobserved"
	EvidenceUnbackedThreadReadList     = "distinctThreadReadList-unobserved"
	EvidenceUnbackedCrashRestart       = "crashRestart-unobserved"
	EvidenceUnbackedBundleSourceLaunch = "bundleSourceRemovalLaunch-unobserved"
)

// QualificationEvidenceIntegrity reports whether the evidence counters can have
// come from a measurement, returning EvidenceIntact when they can.
//
// It answers two questions Validate cannot. A count is a tally of things that
// happened, so no counter may be negative -- a negative violation counter is
// not a stricter claim, it is a value no observation produces. And an
// acceptance boolean is a summary of observations, so a probe that claims
// success must have made at least one; a receipt asserting every property with
// every coverage counter at zero is exactly the shape a forger reaches for,
// because it is the shape that satisfies the verdict rule with no work done.
//
// This does not make forgery impossible -- nothing in-band can, since a forger
// who sets a coverage counter to one is not contradicted by anything else in
// the receipt. It makes the receipt state its coverage, so the gate refuses the
// evidence set that costs nothing to write.
func QualificationEvidenceIntegrity(evidence QualificationEvidence) string {
	for _, negative := range []struct {
		count int
		token string
	}{
		{evidence.CrossThreadWrites, EvidenceNegativeCrossThreadWrites},
		{evidence.StoreCorruptions, EvidenceNegativeStoreCorruptions},
		{evidence.LiveOwnerResumeWrites, EvidenceNegativeResumeWrites},
		{evidence.AmbientMutations, EvidenceNegativeAmbientMutations},
		{evidence.ObservedThreadTurns, EvidenceNegativeThreadTurns},
		{evidence.ObservedThreadReads, EvidenceNegativeThreadReads},
		{evidence.ObservedCrashRestarts, EvidenceNegativeCrashRestarts},
		{evidence.ObservedBundleLaunches, EvidenceNegativeBundleLaunches},
	} {
		if negative.count < 0 {
			return negative.token
		}
	}
	for _, unbacked := range []struct {
		claimed  bool
		observed int
		token    string
	}{
		{evidence.DistinctThreadCreateTurn, evidence.ObservedThreadTurns, EvidenceUnbackedThreadCreateTurn},
		{evidence.DistinctThreadReadList, evidence.ObservedThreadReads, EvidenceUnbackedThreadReadList},
		{evidence.CrashRestart, evidence.ObservedCrashRestarts, EvidenceUnbackedCrashRestart},
		{evidence.BundleSourceRemovalLaunch, evidence.ObservedBundleLaunches, EvidenceUnbackedBundleSourceLaunch},
	} {
		if unbacked.claimed && unbacked.observed <= 0 {
			return unbacked.token
		}
	}
	return EvidenceIntact
}

type QualificationResult struct {
	SchemaVersion int                   `json:"schemaVersion"`
	Versions      VersionPair           `json:"versions"`
	Verdict       QualificationVerdict  `json:"verdict"`
	Reason        QualificationReason   `json:"reason"`
	Evidence      QualificationEvidence `json:"evidence"`
}

func EvaluateQualification(versions VersionPair, evidence QualificationEvidence) QualificationResult {
	result := QualificationResult{SchemaVersion: QualificationSchemaVersion, Versions: versions, Evidence: evidence}
	switch {
	case !evidence.SharedStateDomain || !evidence.DistinctPrivateEndpoints ||
		!evidence.DistinctThreadCreateTurn || !evidence.DistinctThreadReadList || !evidence.CrashRestart ||
		evidence.CrossThreadWrites != 0 || evidence.StoreCorruptions != 0 || evidence.AmbientMutations != 0:
		result.Verdict, result.Reason = VerdictNo, ReasonDistinctThreadFailed
	case evidence.LiveOwnerResumeWrites != 0 || !evidence.OldStoppedBeforeResume || !evidence.PersistedResumeSnapshot:
		result.Verdict, result.Reason = VerdictNo, ReasonSameThreadOwnershipFailed
	case !evidence.SharedAuthConfigPrivate:
		result.Verdict, result.Reason = VerdictNo, ReasonAuthConfigIsolationFailed
	case !evidence.BundleSourceRemovalLaunch || !evidence.BundleDriftRefused:
		result.Verdict, result.Reason = VerdictNo, ReasonBundleLeaseFailed
	case !evidence.ProtocolMismatchRefused:
		result.Verdict, result.Reason = VerdictNo, ReasonProtocolMismatch
	default:
		result.Verdict, result.Reason = VerdictYes, ReasonQualified
	}
	return result
}

func (r QualificationResult) Validate() error {
	if r.SchemaVersion != QualificationSchemaVersion || !validVersionToken(r.Versions.Old) || !validVersionToken(r.Versions.New) {
		return fmt.Errorf("codex generation qualification receipt is incomplete")
	}
	want := EvaluateQualification(r.Versions, r.Evidence)
	if r.Verdict != want.Verdict || r.Reason != want.Reason {
		return fmt.Errorf("codex generation qualification verdict is inconsistent")
	}
	return nil
}

func validVersionToken(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if (char >= '0' && char <= '9') || char == '.' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func (r QualificationResult) JSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(r, "", "  ")
}

func DecodeQualificationResult(encoded []byte) (QualificationResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var result QualificationResult
	if err := decoder.Decode(&result); err != nil {
		return QualificationResult{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return QualificationResult{}, fmt.Errorf("codex generation qualification receipt contains trailing JSON")
	} else if err != io.EOF {
		return QualificationResult{}, err
	}
	if err := result.Validate(); err != nil {
		return QualificationResult{}, err
	}
	return result, nil
}

type FollowupLane string

const (
	FollowupGenerationPool                  FollowupLane = "generation-pool"
	FollowupSingleEndpointJournaledHandover FollowupLane = "single-endpoint-journaled-handover"
)

// QualificationGate is the only Phase 0 gate to later pool work. Any NO,
// especially either shared-state acceptance row, closes Phase 2+.
//
// Discriminant carries the exact evidence field when the gate closed on counter
// integrity rather than on the verdict, so a caller can tell a measured refusal
// from a receipt whose acceptance claims nothing backs.
type QualificationGate struct {
	Phase2Ready    bool         `json:"phase2Ready"`
	Lane           FollowupLane `json:"lane"`
	Blocker        string       `json:"blocker,omitempty"`
	Discriminant   string       `json:"discriminant,omitempty"`
	EvidenceForged bool         `json:"evidenceForged,omitempty"`
}

// GateQualification is the single place the receipt is judged.
//
// It asks three things in order, and they are not interchangeable. The receipt
// must decode as its own schema; its verdict must be the one its evidence
// produces, which is what Validate recomputes; and its evidence counters must
// be able to have come from a measurement, which Validate cannot see, because
// the counters a YES verdict requires are exactly the ones a forger can write
// for free. The third check is why this function rather than Validate is what
// every entry path calls.
func GateQualification(result QualificationResult) QualificationGate {
	closed := func(blocker, discriminant string, forged bool) QualificationGate {
		return QualificationGate{
			Lane: FollowupSingleEndpointJournaledHandover, Blocker: blocker,
			Discriminant: discriminant, EvidenceForged: forged,
		}
	}
	if result.Validate() != nil || result.Verdict != VerdictYes {
		return closed("shared-state qualification is not fully yes; keep the unsafe pool and Phase 2+ closed", "", false)
	}
	if discriminant := QualificationEvidenceIntegrity(result.Evidence); discriminant != EvidenceIntact {
		return closed("qualification evidence counters are unbacked; the receipt asserts a property no observation produced",
			discriminant, true)
	}
	return QualificationGate{Phase2Ready: true, Lane: FollowupGenerationPool}
}

// retainedQualificationFields is kept next to the model as a reviewable
// allowlist for privacy tests.
var retainedQualificationFields = []string{"SchemaVersion", "Versions", "Verdict", "Reason", "Evidence"}

func RetainedQualificationFields() []string { return slices.Clone(retainedQualificationFields) }
