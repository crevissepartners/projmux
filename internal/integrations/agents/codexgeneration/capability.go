// Package codexgeneration owns executable qualification of payload-free Codex
// launch capabilities. It is deliberately separate from the durable pool
// model in internal/core/codexgeneration: a pool generation can be healthy
// while one payload-free launch predicate is unsupported.
package codexgeneration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const SchemaVersion = 1

type Predicate string

const (
	PredicateDurableZeroTurnResume Predicate = "durable-zero-turn-resume"
	PredicateRemoteNewSession      Predicate = "remote-new-session"
)

type Verdict string

const (
	VerdictSupported   Verdict = "supported"
	VerdictUnsupported Verdict = "unsupported"
	VerdictUnknown     Verdict = "unknown"
)

type Stage string

const (
	StageZeroTurnStart   Stage = "zero-turn-start"
	StageIndependentRead Stage = "independent-read"
	StageStoredResume    Stage = "stored-resume"
	StageRemoteNew       Stage = "remote-new"
	StageFirstRealInput  Stage = "first-real-input"
)

type StageOutcome string

const (
	StagePass        StageOutcome = "pass"
	StageUnsupported StageOutcome = "unsupported"
	StageUnknown     StageOutcome = "unknown"
)

type BinaryIdentity struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type ProtocolIdentity struct {
	Transport string `json:"transport"`
	Schema    string `json:"schema"`
}

type SocketRouteIdentity struct {
	Kind          string `json:"kind"`
	LocatorSHA256 string `json:"locator_sha256"`
	RuntimeSHA256 string `json:"runtime_sha256"`
}

// Tuple is the complete cache identity. It intentionally carries no binary,
// socket, or state path. Those values are reduced to role/route/domain
// identities before a record may be persisted.
type Tuple struct {
	RoleTUI           BinaryIdentity      `json:"role_tui"`
	RoleAppServer     BinaryIdentity      `json:"role_app_server"`
	AppServerVersion  string              `json:"app_server_version"`
	Protocol          ProtocolIdentity    `json:"protocol"`
	SocketRoute       SocketRouteIdentity `json:"socket_route"`
	StateDomainID     string              `json:"state_domain_id"`
	StateDomainSHA256 string              `json:"state_domain_sha256"`
	Platform          string              `json:"platform"`
	Architecture      string              `json:"architecture"`
}

type StageEvidence struct {
	Stage              Stage        `json:"stage"`
	Outcome            StageOutcome `json:"outcome"`
	Reason             string       `json:"reason"`
	ThreadSHA256       string       `json:"thread_sha256,omitempty"`
	TurnSHA256         string       `json:"turn_sha256,omitempty"`
	ExactThread        bool         `json:"exact_thread"`
	ExactTurn          bool         `json:"exact_turn"`
	PaneAlive          bool         `json:"pane_alive"`
	FirstInputObserved bool         `json:"first_input_observed"`
	TurnCount          int          `json:"turn_count"`
}

type Evidence struct {
	ZeroTurnStart   StageEvidence `json:"zero_turn_start"`
	IndependentRead StageEvidence `json:"independent_read"`
	StoredResume    StageEvidence `json:"stored_resume"`
	RemoteNew       StageEvidence `json:"remote_new"`
	FirstRealInput  StageEvidence `json:"first_real_input"`
}

type PredicateResult struct {
	Predicate Predicate `json:"predicate"`
	Verdict   Verdict   `json:"verdict"`
	Reason    string    `json:"reason"`
}

// Record is immutable evidence for one exact tuple. CacheKey is a digest of
// Tuple and EvidenceSHA256 is a digest of the observed-at timestamp plus the
// content-free Evidence object. A consumer must validate both before use.
type Record struct {
	SchemaVersion  int             `json:"schema_version"`
	CacheKey       string          `json:"cache_key"`
	Tuple          Tuple           `json:"tuple"`
	ObservedAt     time.Time       `json:"observed_at"`
	EvidenceSHA256 string          `json:"evidence_sha256"`
	Evidence       Evidence        `json:"evidence"`
	DurableResume  PredicateResult `json:"durable_zero_turn_resume"`
	RemoteNew      PredicateResult `json:"remote_new_session"`
}

var (
	digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	idPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
)

func DigestString(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func IdentifyBinary(path string) (BinaryIdentity, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return BinaryIdentity{}, errors.New("capability binary path must be absolute")
	}
	file, err := os.Open(path) // #nosec G304 -- caller supplies the explicit role binary being qualified.
	if err != nil {
		return BinaryIdentity{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return BinaryIdentity{}, errors.New("capability binary must be an executable regular file")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return BinaryIdentity{}, err
	}
	return BinaryIdentity{SHA256: hex.EncodeToString(hash.Sum(nil)), Size: info.Size()}, nil
}

// IdentifySocketRoute fingerprints both the exact locator and the currently
// bound filesystem object. Rebinding the same path therefore cannot reuse a
// previous PASS.
func IdentifySocketRoute(path string) (SocketRouteIdentity, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return SocketRouteIdentity{}, errors.New("capability socket route must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return SocketRouteIdentity{}, err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return SocketRouteIdentity{}, errors.New("capability socket route is not a socket")
	}
	runtimeIdentity := fmt.Sprintf("%s|%s|%d|%d|%#v", path, info.Mode(), info.Size(), info.ModTime().UnixNano(), info.Sys())
	return SocketRouteIdentity{Kind: "private-unix", LocatorSHA256: DigestString(path), RuntimeSHA256: DigestString(runtimeIdentity)}, nil
}

func NewTuple(tuiPath, appServerPath, appServerVersion string, protocol ProtocolIdentity, route SocketRouteIdentity, stateDomainID, stateDomainPath string) (Tuple, error) {
	tui, err := IdentifyBinary(tuiPath)
	if err != nil {
		return Tuple{}, fmt.Errorf("identify RoleTUI: %w", err)
	}
	server, err := IdentifyBinary(appServerPath)
	if err != nil {
		return Tuple{}, fmt.Errorf("identify RoleAppServer: %w", err)
	}
	stateDomainPath = filepath.Clean(strings.TrimSpace(stateDomainPath))
	if !filepath.IsAbs(stateDomainPath) {
		return Tuple{}, errors.New("capability state domain must be absolute")
	}
	tuple := Tuple{
		RoleTUI: tui, RoleAppServer: server, AppServerVersion: strings.TrimSpace(appServerVersion), Protocol: protocol,
		SocketRoute: route, StateDomainID: strings.TrimSpace(stateDomainID), StateDomainSHA256: DigestString(stateDomainPath),
		Platform: runtime.GOOS, Architecture: runtime.GOARCH,
	}
	return tuple, tuple.Validate()
}

func (tuple Tuple) Validate() error {
	for role, identity := range map[string]BinaryIdentity{"RoleTUI": tuple.RoleTUI, "RoleAppServer": tuple.RoleAppServer} {
		if !digestPattern.MatchString(identity.SHA256) || identity.Size <= 0 {
			return fmt.Errorf("capability %s identity is invalid", role)
		}
	}
	if !idPattern.MatchString(tuple.AppServerVersion) || !idPattern.MatchString(tuple.Protocol.Transport) || !idPattern.MatchString(tuple.Protocol.Schema) ||
		!idPattern.MatchString(tuple.SocketRoute.Kind) || !digestPattern.MatchString(tuple.SocketRoute.LocatorSHA256) ||
		!digestPattern.MatchString(tuple.SocketRoute.RuntimeSHA256) || !idPattern.MatchString(tuple.StateDomainID) ||
		!digestPattern.MatchString(tuple.StateDomainSHA256) || !idPattern.MatchString(tuple.Platform) || !idPattern.MatchString(tuple.Architecture) {
		return errors.New("capability tuple identity is incomplete")
	}
	return nil
}

func (tuple Tuple) Key() (string, error) {
	if err := tuple.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(tuple)
	if err != nil {
		return "", err
	}
	return DigestString(string(raw)), nil
}

func Qualify(tuple Tuple, observedAt time.Time, evidence Evidence) (Record, error) {
	if err := tuple.Validate(); err != nil {
		return Record{}, err
	}
	if observedAt.IsZero() {
		return Record{}, errors.New("capability evidence timestamp is required")
	}
	evidence = normalizeEvidence(evidence)
	if err := evidence.Validate(); err != nil {
		return Record{}, err
	}
	key, _ := tuple.Key()
	record := Record{SchemaVersion: SchemaVersion, CacheKey: key, Tuple: tuple, ObservedAt: observedAt.UTC(), Evidence: evidence}
	record.DurableResume = reduceDurableResume(evidence)
	record.RemoteNew = reduceRemoteNew(evidence)
	record.EvidenceSHA256 = record.evidenceDigest()
	return record, record.Validate()
}

func UnknownRecord(tuple Tuple) (Record, error) {
	unknown := func(stage Stage) StageEvidence {
		return StageEvidence{Stage: stage, Outcome: StageUnknown, Reason: "not-qualified"}
	}
	return Qualify(tuple, time.Unix(0, 0).UTC(), Evidence{
		ZeroTurnStart: unknown(StageZeroTurnStart), IndependentRead: unknown(StageIndependentRead),
		StoredResume: unknown(StageStoredResume), RemoteNew: unknown(StageRemoteNew), FirstRealInput: unknown(StageFirstRealInput),
	})
}

func normalizeEvidence(e Evidence) Evidence {
	e.ZeroTurnStart = normalizeStageEvidence(e.ZeroTurnStart)
	e.IndependentRead = normalizeStageEvidence(e.IndependentRead)
	e.StoredResume = normalizeStageEvidence(e.StoredResume)
	e.RemoteNew = normalizeStageEvidence(e.RemoteNew)
	e.FirstRealInput = normalizeStageEvidence(e.FirstRealInput)
	return e
}

func normalizeStageEvidence(stage StageEvidence) StageEvidence {
	stage.Reason = strings.TrimSpace(stage.Reason)
	stage.ThreadSHA256 = strings.TrimSpace(stage.ThreadSHA256)
	stage.TurnSHA256 = strings.TrimSpace(stage.TurnSHA256)
	return stage
}

func (e Evidence) Validate() error {
	stages := []struct {
		want Stage
		got  StageEvidence
	}{{StageZeroTurnStart, e.ZeroTurnStart}, {StageIndependentRead, e.IndependentRead}, {StageStoredResume, e.StoredResume}, {StageRemoteNew, e.RemoteNew}, {StageFirstRealInput, e.FirstRealInput}}
	for _, entry := range stages {
		if entry.got.Stage != entry.want || (entry.got.Outcome != StagePass && entry.got.Outcome != StageUnsupported && entry.got.Outcome != StageUnknown) ||
			!idPattern.MatchString(entry.got.Reason) || entry.got.TurnCount < 0 {
			return fmt.Errorf("capability stage %s evidence is invalid", entry.want)
		}
		for _, digest := range []string{entry.got.ThreadSHA256, entry.got.TurnSHA256} {
			if digest != "" && !digestPattern.MatchString(digest) {
				return errors.New("capability evidence identity digest is invalid")
			}
		}
	}
	return nil
}

func reduceDurableResume(e Evidence) PredicateResult {
	result := PredicateResult{Predicate: PredicateDurableZeroTurnResume, Verdict: VerdictUnknown, Reason: "evidence-incomplete"}
	if e.ZeroTurnStart.Outcome != StagePass || !e.ZeroTurnStart.ExactThread || e.IndependentRead.Outcome != StagePass || !e.IndependentRead.ExactThread {
		return result
	}
	if e.ZeroTurnStart.ThreadSHA256 == "" || e.ZeroTurnStart.ThreadSHA256 != e.IndependentRead.ThreadSHA256 ||
		e.ZeroTurnStart.ThreadSHA256 != e.StoredResume.ThreadSHA256 || !e.StoredResume.ExactThread {
		return result
	}
	if e.StoredResume.Outcome == StageUnsupported {
		result.Verdict, result.Reason = VerdictUnsupported, e.StoredResume.Reason
		return result
	}
	if e.StoredResume.Outcome == StagePass && e.StoredResume.ExactThread {
		result.Verdict, result.Reason = VerdictSupported, "stored-resume-exact"
	}
	return result
}

func reduceRemoteNew(e Evidence) PredicateResult {
	result := PredicateResult{Predicate: PredicateRemoteNewSession, Verdict: VerdictUnknown, Reason: "first-input-unproven"}
	if e.RemoteNew.Outcome == StageUnsupported {
		result.Verdict, result.Reason = VerdictUnsupported, e.RemoteNew.Reason
		return result
	}
	if e.RemoteNew.Outcome != StagePass || !e.RemoteNew.PaneAlive {
		return result
	}
	first := e.FirstRealInput
	if first.Outcome == StageUnsupported {
		result.Verdict, result.Reason = VerdictUnsupported, first.Reason
		return result
	}
	if first.Outcome == StagePass && first.FirstInputObserved && first.ExactThread && first.ExactTurn && first.TurnCount == 1 &&
		e.RemoteNew.ExactThread && e.RemoteNew.ThreadSHA256 != "" && e.RemoteNew.ThreadSHA256 == first.ThreadSHA256 && first.TurnSHA256 != "" {
		result.Verdict, result.Reason = VerdictSupported, "first-real-input-exact"
	}
	return result
}

func (record Record) evidenceDigest() string {
	raw, _ := json.Marshal(struct {
		ObservedAt time.Time `json:"observed_at"`
		Evidence   Evidence  `json:"evidence"`
	}{record.ObservedAt.UTC(), record.Evidence})
	return DigestString(string(raw))
}

func (record Record) Validate() error {
	if record.SchemaVersion != SchemaVersion {
		return fmt.Errorf("capability schema version = %d", record.SchemaVersion)
	}
	if err := record.Tuple.Validate(); err != nil {
		return err
	}
	key, _ := record.Tuple.Key()
	if record.CacheKey != key || record.EvidenceSHA256 != record.evidenceDigest() || record.ObservedAt.IsZero() {
		return errors.New("capability record digest or timestamp is invalid")
	}
	if err := record.Evidence.Validate(); err != nil {
		return err
	}
	if record.DurableResume != reduceDurableResume(record.Evidence) || record.RemoteNew != reduceRemoteNew(record.Evidence) {
		return errors.New("capability record verdict is not the evidence fixed point")
	}
	return nil
}

func (record Record) JSON() ([]byte, error) {
	if err := record.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(record)
}

func DecodeRecord(encoded []byte) (Record, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil {
		return Record{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return Record{}, errors.New("capability record contains trailing JSON")
	} else if err != io.EOF {
		return Record{}, err
	}
	return record, record.Validate()
}
