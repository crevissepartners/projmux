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
type QualificationGate struct {
	Phase2Ready bool         `json:"phase2Ready"`
	Lane        FollowupLane `json:"lane"`
	Blocker     string       `json:"blocker,omitempty"`
}

func GateQualification(result QualificationResult) QualificationGate {
	if result.Validate() == nil && result.Verdict == VerdictYes {
		return QualificationGate{Phase2Ready: true, Lane: FollowupGenerationPool}
	}
	return QualificationGate{
		Lane:    FollowupSingleEndpointJournaledHandover,
		Blocker: "shared-state qualification is not fully yes; keep the unsafe pool and Phase 2+ closed",
	}
}

// retainedQualificationFields is kept next to the model as a reviewable
// allowlist for privacy tests.
var retainedQualificationFields = []string{"SchemaVersion", "Versions", "Verdict", "Reason", "Evidence"}

func RetainedQualificationFields() []string { return slices.Clone(retainedQualificationFields) }
