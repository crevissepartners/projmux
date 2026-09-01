package codexinstalled

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"golang.org/x/mod/semver"
)

const QualificationSchemaVersion = 1

type QualificationPrimitive string

const (
	PrimitiveDaemonLifecycle QualificationPrimitive = "daemon-lifecycle"
	PrimitiveThreadList      QualificationPrimitive = "thread-list"
	PrimitivePreTurnAttach   QualificationPrimitive = "pre-turn-attach"
)

type QualificationTopology string

const (
	QualificationTopologyDirect        QualificationTopology = "direct"
	QualificationTopologyDirectManaged QualificationTopology = "direct-managed"
)

type QualificationStage string

const (
	QualificationStageReady  QualificationStage = "ready"
	QualificationStageRetire QualificationStage = "retire"
)

// QualificationReason is a closed, content-free vocabulary. Raw test output,
// endpoint paths, thread identity, and upstream payloads cannot be projected
// into the scheduled artifact through this type.
type QualificationReason string

const (
	QualificationReasonCompatible             QualificationReason = "compatible"
	QualificationReasonUnsupported            QualificationReason = "unsupported"
	QualificationReasonCanaryFailed           QualificationReason = "canary-failed"
	QualificationReasonInfrastructureError    QualificationReason = "infrastructure-error"
	QualificationReasonTerminalMissing        QualificationReason = "terminal-result-missing"
	QualificationReasonTerminalInvalid        QualificationReason = "terminal-result-invalid"
	QualificationReasonInstallationFailed     QualificationReason = "codex-installation-failed"
	QualificationReasonDeclaredVersionInvalid QualificationReason = "declared-version-invalid"
	QualificationReasonVersionMismatch        QualificationReason = "version-tuple-mismatch"
	QualificationReasonChildArtifactMissing   QualificationReason = "matrix-child-artifact-missing"
	QualificationReasonChildArtifactInvalid   QualificationReason = "matrix-child-artifact-invalid"
)

type QualificationResult struct {
	Versions  VersionTuple           `json:"versions"`
	Topology  QualificationTopology  `json:"topology"`
	Primitive QualificationPrimitive `json:"primitive"`
	Stage     QualificationStage     `json:"stage"`
	Class     ResultClass            `json:"class"`
	Reason    QualificationReason    `json:"reason"`
}

type QualificationArtifact struct {
	SchemaVersion int                   `json:"schema_version"`
	Results       []QualificationResult `json:"results"`
}

// QualificationSpec binds each scheduled matrix leg to its existing
// canonical installed test. The scheduled lane deliberately contains no
// app-server protocol implementation of its own.
type QualificationSpec struct {
	Primitive QualificationPrimitive
	Topology  QualificationTopology
	Stage     QualificationStage
	TestName  string
	SmokeEnv  string
}

func QualificationSpecs() []QualificationSpec {
	return []QualificationSpec{
		{
			Primitive: PrimitiveDaemonLifecycle,
			Topology:  QualificationTopologyDirectManaged,
			Stage:     QualificationStageRetire,
			TestName:  "TestInstalledHermeticTopologyQualification",
			SmokeEnv:  DefaultSmokeRootEnv,
		},
		{
			Primitive: PrimitiveThreadList,
			Topology:  QualificationTopologyDirect,
			Stage:     QualificationStageReady,
			TestName:  "TestInstalledIsolatedConversationCatalogSmoke",
			SmokeEnv:  "PROJMUX_CODEX_CATALOG_SMOKE_ROOT",
		},
		{
			Primitive: PrimitivePreTurnAttach,
			Topology:  QualificationTopologyDirect,
			Stage:     QualificationStageReady,
			TestName:  "TestInstalledIsolatedPreTurnBootstrapSmoke",
			SmokeEnv:  "PROJMUX_CODEX_BROKER_SMOKE_ROOT",
		},
	}
}

func QualificationSpecFor(primitive QualificationPrimitive) (QualificationSpec, bool) {
	for _, spec := range QualificationSpecs() {
		if spec.Primitive == primitive {
			return spec, true
		}
	}
	return QualificationSpec{}, false
}

func (result QualificationResult) Validate() error {
	if result.Versions != result.Versions.normalized() {
		return fmt.Errorf("installed qualification artifact contains a blank version")
	}
	for _, version := range []string{result.Versions.CLI, result.Versions.Managed, result.Versions.AppServer} {
		if version != UnknownVersion && !semver.IsValid("v"+version) {
			return fmt.Errorf("installed qualification artifact contains a non-semver version")
		}
	}
	spec, ok := QualificationSpecFor(result.Primitive)
	if !ok {
		return fmt.Errorf("installed qualification artifact primitive %q is not terminal", result.Primitive)
	}
	if result.Topology != spec.Topology {
		return fmt.Errorf("installed qualification artifact topology %q does not match primitive %q", result.Topology, result.Primitive)
	}
	if result.Stage != spec.Stage {
		return fmt.Errorf("installed qualification artifact stage %q does not match primitive %q", result.Stage, result.Primitive)
	}
	switch result.Class {
	case ResultPass:
		if result.Reason != QualificationReasonCompatible {
			return fmt.Errorf("installed qualification pass has non-pass reason %q", result.Reason)
		}
		if result.Versions.CLI == UnknownVersion || result.Versions.Managed == UnknownVersion || result.Versions.AppServer == UnknownVersion {
			return fmt.Errorf("installed qualification pass has an unknown version")
		}
	case ResultUnsupported:
		if result.Reason != QualificationReasonUnsupported {
			return fmt.Errorf("installed qualification unsupported result has reason %q", result.Reason)
		}
	case ResultFail:
		if result.Reason != QualificationReasonCanaryFailed && result.Reason != QualificationReasonVersionMismatch {
			return fmt.Errorf("installed qualification failure has reason %q", result.Reason)
		}
	case ResultInfraError:
		if !slices.Contains([]QualificationReason{
			QualificationReasonInfrastructureError,
			QualificationReasonTerminalMissing,
			QualificationReasonTerminalInvalid,
			QualificationReasonInstallationFailed,
			QualificationReasonDeclaredVersionInvalid,
			QualificationReasonChildArtifactMissing,
			QualificationReasonChildArtifactInvalid,
		}, result.Reason) {
			return fmt.Errorf("installed qualification infrastructure result has reason %q", result.Reason)
		}
	default:
		return fmt.Errorf("installed qualification artifact class %q is not terminal", result.Class)
	}
	return nil
}

func (artifact QualificationArtifact) Validate(expected []QualificationPrimitive) error {
	if artifact.SchemaVersion != QualificationSchemaVersion {
		return fmt.Errorf("installed qualification artifact schema version = %d", artifact.SchemaVersion)
	}
	if len(artifact.Results) != len(expected) {
		return fmt.Errorf("installed qualification artifact results = %d, want %d", len(artifact.Results), len(expected))
	}
	seen := make(map[QualificationPrimitive]struct{}, len(artifact.Results))
	for _, result := range artifact.Results {
		if err := result.Validate(); err != nil {
			return err
		}
		if _, duplicate := seen[result.Primitive]; duplicate {
			return fmt.Errorf("installed qualification artifact duplicates primitive %q", result.Primitive)
		}
		seen[result.Primitive] = struct{}{}
	}
	for _, primitive := range expected {
		if _, ok := seen[primitive]; !ok {
			return fmt.Errorf("installed qualification artifact is missing primitive %q", primitive)
		}
	}
	return nil
}

func (artifact QualificationArtifact) JSON(expected []QualificationPrimitive) ([]byte, error) {
	if err := artifact.Validate(expected); err != nil {
		return nil, err
	}
	return json.MarshalIndent(artifact, "", "  ")
}

func DecodeQualificationArtifact(encoded []byte, expected []QualificationPrimitive) (QualificationArtifact, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var artifact QualificationArtifact
	if err := decoder.Decode(&artifact); err != nil {
		return QualificationArtifact{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return QualificationArtifact{}, fmt.Errorf("installed qualification artifact contains trailing JSON")
	} else if err != io.EOF {
		return QualificationArtifact{}, err
	}
	if err := artifact.Validate(expected); err != nil {
		return QualificationArtifact{}, err
	}
	return artifact, nil
}

func ReduceQualification(spec QualificationSpec, observed []Result, testSucceeded bool) QualificationResult {
	versions := VersionTuple{}.normalized()
	worst := ResultPass
	hadResult := false
	invalid := false
	for _, result := range observed {
		if err := result.Validate(); err != nil {
			invalid = true
			continue
		}
		hadResult = true
		versions = mergeObservedVersions(versions, result.Versions)
		if qualificationClassRank(result.Class) > qualificationClassRank(worst) {
			worst = result.Class
		}
	}
	if invalid {
		return newQualificationResult(spec, versions, ResultInfraError, QualificationReasonTerminalInvalid)
	}
	if !hadResult {
		return newQualificationResult(spec, versions, ResultInfraError, QualificationReasonTerminalMissing)
	}
	if !qualificationEvidenceComplete(spec.Primitive, observed) {
		return newQualificationResult(spec, versions, ResultInfraError, QualificationReasonTerminalMissing)
	}
	if !testSucceeded && worst == ResultPass {
		return newQualificationResult(spec, versions, ResultFail, QualificationReasonCanaryFailed)
	}
	return newQualificationResult(spec, versions, worst, qualificationReasonForClass(worst))
}

func InstallationFailureQualification(spec QualificationSpec) QualificationResult {
	return newQualificationResult(
		spec, VersionTuple{}.normalized(), ResultInfraError, QualificationReasonInstallationFailed,
	)
}

// EnforceQualificationVersion keeps observed values in the typed artifact but
// refuses to report compatibility unless every independently observed version
// matches the version declared by the scheduled matrix.
func EnforceQualificationVersion(result QualificationResult, expected string) QualificationResult {
	if !semver.IsValid("v" + expected) {
		result.Class = ResultInfraError
		result.Reason = QualificationReasonDeclaredVersionInvalid
		return result
	}
	if result.Class != ResultPass {
		return result
	}
	if result.Versions.CLI != expected || result.Versions.Managed != expected || result.Versions.AppServer != expected {
		result.Class = ResultFail
		result.Reason = QualificationReasonVersionMismatch
	}
	return result
}

func AggregateQualificationArtifacts(children map[QualificationPrimitive][]byte) (QualificationArtifact, error) {
	artifact := QualificationArtifact{SchemaVersion: QualificationSchemaVersion}
	invalidChildren := 0
	for _, spec := range QualificationSpecs() {
		encoded, ok := children[spec.Primitive]
		if !ok {
			artifact.Results = append(artifact.Results, newQualificationResult(
				spec, VersionTuple{}.normalized(), ResultInfraError, QualificationReasonChildArtifactMissing,
			))
			invalidChildren++
			continue
		}
		child, err := DecodeQualificationArtifact(encoded, []QualificationPrimitive{spec.Primitive})
		if err != nil {
			artifact.Results = append(artifact.Results, newQualificationResult(
				spec, VersionTuple{}.normalized(), ResultInfraError, QualificationReasonChildArtifactInvalid,
			))
			invalidChildren++
			continue
		}
		artifact.Results = append(artifact.Results, child.Results[0])
	}
	expected := qualificationPrimitives()
	if err := artifact.Validate(expected); err != nil {
		return QualificationArtifact{}, err
	}
	if invalidChildren != 0 {
		return artifact, fmt.Errorf("installed qualification has %d missing or invalid matrix child artifacts", invalidChildren)
	}
	for _, result := range artifact.Results {
		if result.Class != ResultPass {
			return artifact, fmt.Errorf("installed qualification primitive %q is %s", result.Primitive, result.Class)
		}
	}
	return artifact, nil
}

func qualificationPrimitives() []QualificationPrimitive {
	primitives := make([]QualificationPrimitive, 0, len(QualificationSpecs()))
	for _, spec := range QualificationSpecs() {
		primitives = append(primitives, spec.Primitive)
	}
	return primitives
}

func newQualificationResult(spec QualificationSpec, versions VersionTuple, class ResultClass, reason QualificationReason) QualificationResult {
	return QualificationResult{
		Versions:  versions.normalized(),
		Topology:  spec.Topology,
		Primitive: spec.Primitive,
		Stage:     spec.Stage,
		Class:     class,
		Reason:    reason,
	}
}

func mergeObservedVersions(current, observed VersionTuple) VersionTuple {
	current = current.normalized()
	observed = observed.normalized()
	if observed.CLI != UnknownVersion {
		current.CLI = observed.CLI
	}
	if observed.Managed != UnknownVersion {
		current.Managed = observed.Managed
	}
	if observed.AppServer != UnknownVersion {
		current.AppServer = observed.AppServer
	}
	return current
}

func qualificationClassRank(class ResultClass) int {
	switch class {
	case ResultPass:
		return 0
	case ResultUnsupported:
		return 1
	case ResultFail:
		return 2
	case ResultInfraError:
		return 3
	default:
		return 4
	}
}

func qualificationReasonForClass(class ResultClass) QualificationReason {
	switch class {
	case ResultPass:
		return QualificationReasonCompatible
	case ResultUnsupported:
		return QualificationReasonUnsupported
	case ResultFail:
		return QualificationReasonCanaryFailed
	default:
		return QualificationReasonInfrastructureError
	}
}

func qualificationEvidenceComplete(primitive QualificationPrimitive, observed []Result) bool {
	switch primitive {
	case PrimitiveDaemonLifecycle:
		if qualificationHasNonPass(observed, TopologyDirect) {
			return true
		}
		if !qualificationHas(observed, TopologyDirect, StageReady, "direct-endpoint-ready") ||
			!qualificationHas(observed, TopologyDirect, StageClose, "direct-endpoint-closed") {
			return false
		}
		return qualificationHasNonPass(observed, TopologyManaged) ||
			qualificationHas(observed, TopologyManaged, StageRetire, "managed-endpoint-started-reused-retired")
	case PrimitiveThreadList:
		return qualificationHasNonPass(observed, TopologyDirect) ||
			qualificationHas(observed, TopologyDirect, StageReady, "thread-list-compatible")
	case PrimitivePreTurnAttach:
		return qualificationHasNonPass(observed, TopologyDirect) ||
			qualificationHas(observed, TopologyDirect, StageReady, "pre-turn-second-attach-thread-read-compatible")
	default:
		return false
	}
}

func qualificationHasNonPass(observed []Result, topology Topology) bool {
	for _, result := range observed {
		if result.Topology == topology && result.Class != ResultPass {
			return true
		}
	}
	return false
}

func qualificationHas(observed []Result, topology Topology, stage Stage, reason string) bool {
	for _, result := range observed {
		if result.Topology == topology && result.Stage == stage && result.Class == ResultPass && result.Reason == reason {
			return true
		}
	}
	return false
}
