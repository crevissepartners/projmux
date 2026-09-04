package metadata

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

// The recovery boundary is the operator half of the durable envelope.
//
// store.go owns the write side: it keeps an initialized marker so a lost
// registry cannot read as a first use, and it keeps bounded copies of the last
// verified bytes each semantic write replaced. Neither of those ever reads a
// copy back, because choosing which copy becomes the source of truth is not a
// property of a write -- it is a decision only an operator can make.
//
// This file is that decision, split into two operations with deliberately
// different powers:
//
//   - Inspect classifies the current registry and every bounded candidate and
//     writes nothing at all. It does not take the cross-process lock, does not
//     repair permissions, and does not create the metadata or recovery
//     directory, so it is safe to run against a first-use state directory and
//     against a state directory whose contents are already damaged.
//   - RestoreFrom publishes exactly one explicitly named source. It never picks
//     a source itself, it refuses anything it cannot verify, it preserves the
//     bytes it is about to replace before replacing them, and it proves the
//     source and the current bytes are still the ones it verified immediately
//     before the single rename that mutates the registry.
//
// The live tmux mirror is not in this file on purpose. A mirror carries the uids
// of live objects only, so rebuilding a registry from it would silently discard
// every offline resource, every Agent, every name reservation, and every owner
// relation the machine no longer shows. The mirror is a diagnostic input to the
// command layer, never a recovery source here.

const (
	// preservedFilePrefix names the copies RestoreFrom takes of the bytes it
	// replaces. The prefix deliberately differs from recoveryFilePrefix so the
	// write-side retention bound never counts or removes them, and so an
	// operator can tell "bytes a write replaced" from "bytes a restore
	// replaced" by name alone.
	preservedFilePrefix = "replaced-"
	// checksumPrefix keeps the reported digest self-describing, so an operator
	// comparing a planned checksum against a shell `sha256sum` is never left
	// guessing which algorithm produced it.
	checksumPrefix = "sha256:"
)

// ErrRecoverySourceRejected marks a recovery source this build refuses to
// publish: unreadable, empty, malformed, newer than this schema, or decodable
// but not a valid resource graph. It is fail-closed by design -- restoring a
// source that does not validate would replace a known-damaged registry with an
// unknown-damaged one, and the second state is worse because it looks healthy.
var ErrRecoverySourceRejected = errors.New("registry recovery source is not restorable")

// ErrRecoverySourceAmbiguous marks a selector that names more than one
// candidate. The store never breaks the tie: which of two copies an operator
// meant is not derivable from the copies.
var ErrRecoverySourceAmbiguous = errors.New("registry recovery source selector is ambiguous")

// ErrRecoverySourceNotFound marks a selector that names no candidate.
var ErrRecoverySourceNotFound = errors.New("registry recovery source does not exist")

// ErrRecoveryRaced marks a restore whose inputs changed after they were
// verified: the source is no longer the bytes that were validated, or the
// current registry is no longer the bytes the operator planned against. The
// registry is left byte-identical and the operator is told to re-plan, because
// the alternative is publishing a source nobody reviewed over a state nobody
// looked at.
var ErrRecoveryRaced = errors.New("registry recovery inputs changed before the restore")

// ErrRegistryDegraded marks an ordinary mutation refused because the live
// Registry is not a state the normal read -> mutate -> full-validate
// transaction can safely build on. Read-only and registry-recovery routes stay
// available; the exact next command deliberately plans rather than selects a
// source, because source selection belongs to the operator.
var ErrRegistryDegraded = errors.New("resource registry is in degraded mode")

// RegistryRecoveryPlanCommand is the exact no-write command every degraded
// mutation refusal gives the operator. It is intentionally a plan command: the
// recovery route may rank verified copies for display, but it never turns that
// ranking into an automatic restore decision.
const RegistryRecoveryPlanCommand = "projmux reconcile registry --dry-run"

// DegradedMode is the mutation gate derived from one recovery inspection.
// Active is false only for a valid Registry and legitimate first use.
type DegradedMode struct {
	Active bool
	State  RegistryState
	Reason string
	Next   string
	// Cause preserves the pre-existing typed metadata failure for callers that
	// use errors.Is while adding the degraded-mode remediation around it.
	Cause error
}

// Error converts an active decision into the actionable error ordinary write
// routes return. The classification and remediation wrap the validation detail
// so a raw graph-validation failure is never the entire user-visible message.
func (m DegradedMode) Error() error {
	if !m.Active {
		return nil
	}
	reason := strings.TrimSpace(m.Reason)
	if reason == "" {
		reason = "the registry cannot safely serve as mutation authority"
	}
	next := strings.TrimSpace(m.Next)
	if next == "" {
		next = RegistryRecoveryPlanCommand
	}
	if m.Cause != nil {
		return fmt.Errorf("metadata: %w (%s): %s: %w; ordinary mutations are disabled while read, diagnostic, and repair routes remain available; run exactly: %s",
			ErrRegistryDegraded, m.State, reason, m.Cause, next)
	}
	return fmt.Errorf("metadata: %w (%s): %s; ordinary mutations are disabled while read, diagnostic, and repair routes remain available; run exactly: %s",
		ErrRegistryDegraded, m.State, reason, next)
}

// RegistryState is the classification of one registry-shaped file.
type RegistryState string

const (
	// RegistryStateFirstUse is an absent or empty registry in a state
	// directory that has never completed a write. It is the legitimate empty
	// registry, not a loss.
	RegistryStateFirstUse RegistryState = "first-use"
	// RegistryStateValid is a decodable envelope that passes graph validation.
	RegistryStateValid RegistryState = "valid"
	// RegistryStateMissing and RegistryStateEmpty are the two shapes of state
	// loss: no bytes where the marker proves bytes once existed.
	RegistryStateMissing RegistryState = "missing"
	RegistryStateEmpty   RegistryState = "empty"
	// RegistryStateMalformed is undecodable JSON.
	RegistryStateMalformed RegistryState = "malformed"
	// RegistryStateSchemaTooNew is an envelope this build must not reinterpret.
	RegistryStateSchemaTooNew RegistryState = "schema-too-new"
	// RegistryStateInvalid decodes but is not a valid resource graph: a
	// duplicate uid, a dangling ownerRef, a broken name reservation.
	RegistryStateInvalid RegistryState = "invalid"
	// RegistryStateUnreadable is an access failure rather than a content
	// problem.
	RegistryStateUnreadable RegistryState = "unreadable"
)

// restorable reports whether a state may be published over the live registry.
func (s RegistryState) restorable() bool { return s == RegistryStateValid }

// RegistryContents counts what a verified envelope holds. It is the operator's
// sanity check on a candidate: restoring a copy that holds three Projects when
// the machine had thirty is a decision, and it must be visible before the
// decision, not after.
type RegistryContents struct {
	Projects int `json:"projects"`
	Windows  int `json:"windows"`
	Panes    int `json:"panes"`
	Agents   int `json:"agents"`
	// Reservations is the number of held name reservations. It is reported
	// separately because reservations are the part of the registry no live tmux
	// object can testify to.
	Reservations int `json:"reservations"`
}

// RegistryFileInfo describes one registry-shaped file without interpreting it
// as authority.
type RegistryFileInfo struct {
	Path  string        `json:"path"`
	State RegistryState `json:"state"`
	// Detail is the human reason behind State. It is always populated for a
	// state other than valid so a report never shows a bare classification.
	Detail string `json:"detail,omitempty"`
	// Checksum is the digest of the exact bytes on disk, empty when there are
	// no readable bytes. It is what makes a plan and a later restore refer
	// provably to the same content.
	Checksum      string           `json:"checksum,omitempty"`
	Size          int64            `json:"size"`
	SchemaVersion int              `json:"schemaVersion,omitempty"`
	ModifiedAt    string           `json:"modifiedAt,omitempty"`
	Contents      RegistryContents `json:"contents"`
}

// RecoverySourceKind separates where a candidate came from.
type RecoverySourceKind string

const (
	// RecoverySourceWriteCopy is a bounded copy of the bytes a semantic write
	// replaced.
	RecoverySourceWriteCopy RecoverySourceKind = "write-copy"
	// RecoverySourceReplacedCopy is a copy of the bytes a restore replaced. It
	// is what makes a restore itself reversible.
	RecoverySourceReplacedCopy RecoverySourceKind = "replaced-copy"
	// RecoverySourceExplicitPath is a file the operator named by absolute path.
	// It is not enumerated and gets no weaker verification than a bounded copy.
	RecoverySourceExplicitPath RecoverySourceKind = "explicit-path"
)

// RecoverySource is one candidate the operator may select.
type RecoverySource struct {
	Name string             `json:"name"`
	Kind RecoverySourceKind `json:"kind"`
	// Eligible is the single field a caller needs to decide whether selecting
	// this source can succeed. Reason explains either direction.
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason"`
	RegistryFileInfo
}

// RecoveryInspection is the complete no-write picture of one state directory.
type RecoveryInspection struct {
	RegistryPath string `json:"registryPath"`
	MarkerPath   string `json:"markerPath"`
	RecoveryDir  string `json:"recoveryDir"`
	Initialized  bool   `json:"initialized"`
	// Retention is the write-side bound on write-copy candidates, reported so
	// an operator can see how far back the automatic history reaches.
	Retention int              `json:"retention"`
	Current   RegistryFileInfo `json:"current"`
	Sources   []RecoverySource `json:"sources"`
}

// DegradedMode classifies the live Registry for the ordinary-mutation gate.
// The table is deliberately closed over RegistryState: valid and first-use are
// the only states from which a normal mutation may begin. Every damaged,
// unsupported, lost, or unreadable state stays inspectable and repairable but
// cannot be used as mutation authority.
func (i RecoveryInspection) DegradedMode() DegradedMode {
	mode := DegradedMode{State: i.Current.State}
	switch i.Current.State {
	case RegistryStateValid, RegistryStateFirstUse:
		return mode
	case RegistryStateMissing, RegistryStateEmpty, RegistryStateMalformed,
		RegistryStateSchemaTooNew, RegistryStateInvalid, RegistryStateUnreadable:
		mode.Active = true
		mode.Reason = i.Current.Detail
		mode.Next = RegistryRecoveryPlanCommand
		return mode
	default:
		mode.Active = true
		mode.Reason = fmt.Sprintf("registry state %q is not recognized by this build", i.Current.State)
		mode.Next = RegistryRecoveryPlanCommand
		return mode
	}
}

// EligibleSources returns the candidates a restore could publish, in the same
// order as Sources.
func (i RecoveryInspection) EligibleSources() []RecoverySource {
	var out []RecoverySource
	for _, source := range i.Sources {
		if source.Eligible {
			out = append(out, source)
		}
	}
	return out
}

// InspectRecovery classifies the current registry and every bounded candidate
// without writing anything.
//
// It deliberately avoids withLock. Taking the lock would create the metadata
// directory and a lock file, which is exactly the side effect a plan must not
// have -- and a plan is most needed precisely when the state directory is in a
// shape no writer should touch yet.
func (s *Store) InspectRecovery() (RecoveryInspection, error) {
	if s == nil {
		return RecoveryInspection{}, errors.New("metadata: nil registry store")
	}
	initialized, err := s.initialized()
	if err != nil {
		return RecoveryInspection{}, err
	}
	retention := s.retention
	if retention <= 0 {
		retention = defaultRecoveryRetention
	}
	inspection := RecoveryInspection{
		RegistryPath: s.path,
		MarkerPath:   s.markerPath,
		RecoveryDir:  s.recoveryDir,
		Initialized:  initialized,
		Retention:    retention,
		Current:      s.inspectCurrent(initialized),
	}
	inspection.Sources = s.inspectSources()
	return inspection, nil
}

// inspectCurrent classifies the live registry file. Absent or empty bytes are
// only state loss when the marker proves a write completed, which is the same
// rule the read path applies -- restated here rather than borrowed, because the
// read path answers with an error and a plan must answer with a classification.
func (s *Store) inspectCurrent(initialized bool) RegistryFileInfo {
	info := classifyRegistryFile(s.path, s.migrations)
	switch info.State {
	case RegistryStateMissing, RegistryStateEmpty:
		if !initialized {
			shape := info.State
			info.State = RegistryStateFirstUse
			info.Detail = fmt.Sprintf("%s and no completed write is recorded, so this is a legitimate first use", shape)
		}
	}
	return info
}

// inspectSources enumerates the bounded candidates newest first.
func (s *Store) inspectSources() []RecoverySource {
	var sources []RecoverySource
	for _, entry := range s.candidateNames() {
		path := filepath.Join(s.recoveryDir, entry.name)
		source := RecoverySource{Name: entry.name, Kind: entry.kind, RegistryFileInfo: classifyRegistryFile(path, s.migrations)}
		source.Eligible = source.State.restorable()
		if source.Eligible {
			source.Reason = "verified registry envelope"
		} else {
			source.Reason = source.Detail
		}
		sources = append(sources, source)
	}
	return sources
}

// candidateEntry is one recovery-directory file plus which producer wrote it.
type candidateEntry struct {
	name  string
	kind  RecoverySourceKind
	stamp string
	seq   int
}

// candidateNames lists both copy families newest first.
//
// The order is a total order over (stamp, sequence, name) so a JSON report is
// byte-stable across runs and across filesystems whose directory order differs.
// Newest first is the operator-facing order: the most recent verified bytes are
// the usual answer, and putting them anywhere but the top would invite reading
// the wrong row.
func (s *Store) candidateNames() []candidateEntry {
	dir, err := os.ReadDir(s.recoveryDir)
	if err != nil {
		return nil
	}
	var entries []candidateEntry
	for _, item := range dir {
		if item.IsDir() || !strings.HasSuffix(item.Name(), recoveryFileSuffix) {
			continue
		}
		name := item.Name()
		kind := RecoverySourceKind("")
		trimmed := ""
		switch {
		case strings.HasPrefix(name, recoveryFilePrefix):
			kind, trimmed = RecoverySourceWriteCopy, strings.TrimPrefix(name, recoveryFilePrefix)
		case strings.HasPrefix(name, preservedFilePrefix):
			kind, trimmed = RecoverySourceReplacedCopy, strings.TrimPrefix(name, preservedFilePrefix)
		default:
			// A file this store did not create is not a candidate. Offering an
			// operator's own note as a restore source would be a guess.
			continue
		}
		stamp, seq, ok := parseRecoveryName(recoveryFilePrefix + trimmed)
		if !ok {
			continue
		}
		entries = append(entries, candidateEntry{name: name, kind: kind, stamp: stamp, seq: seq})
	}
	sort.Slice(entries, func(a, b int) bool {
		if entries[a].stamp != entries[b].stamp {
			return entries[a].stamp > entries[b].stamp
		}
		if entries[a].seq != entries[b].seq {
			return entries[a].seq > entries[b].seq
		}
		return entries[a].name < entries[b].name
	})
	return entries
}

// SelectSource resolves an operator selector against the bounded candidates.
//
// An absolute path is taken literally: an operator restoring a copy carried from
// another machine is a real recovery, and refusing it would only push them into
// hand-copying the file over registry.json with none of the verification below.
// Anything else is matched against candidate names -- exact first, then as a
// unique substring, so `20260818T134813` selects a copy without retyping the
// whole name. More than one match is refused rather than ranked.
func (i RecoveryInspection) SelectSource(selector string) (RecoverySource, error) {
	trimmed := strings.TrimSpace(selector)
	if trimmed == "" {
		return RecoverySource{}, fmt.Errorf("metadata: %w: an empty selector names no source", ErrRecoverySourceNotFound)
	}
	if filepath.IsAbs(trimmed) {
		return RecoverySource{}, fmt.Errorf("metadata: %q is an absolute path; resolve it with InspectExplicitSource", trimmed)
	}
	var exact []RecoverySource
	var partial []RecoverySource
	for _, source := range i.Sources {
		switch {
		case source.Name == trimmed:
			exact = append(exact, source)
		case strings.Contains(source.Name, trimmed):
			partial = append(partial, source)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = partial
	}
	switch len(matches) {
	case 0:
		return RecoverySource{}, fmt.Errorf("metadata: %w: no recovery copy in %s matches %q", ErrRecoverySourceNotFound, i.RecoveryDir, trimmed)
	case 1:
		return matches[0], nil
	default:
		names := make([]string, 0, len(matches))
		for _, match := range matches {
			names = append(names, match.Name)
		}
		return RecoverySource{}, fmt.Errorf("metadata: %w: %q matches %d recovery copies (%s); name one exactly",
			ErrRecoverySourceAmbiguous, trimmed, len(matches), strings.Join(names, ", "))
	}
}

// InspectExplicitSource classifies a file the operator named by absolute path.
func (s *Store) InspectExplicitSource(path string) (RecoverySource, error) {
	if s == nil {
		return RecoverySource{}, errors.New("metadata: nil registry store")
	}
	if !filepath.IsAbs(path) {
		return RecoverySource{}, fmt.Errorf("metadata: explicit recovery source %q must be an absolute path", path)
	}
	clean := filepath.Clean(path)
	if clean == filepath.Clean(s.path) {
		return RecoverySource{}, fmt.Errorf("metadata: %w: %s is the live registry, not a recovery source", ErrRecoverySourceRejected, clean)
	}
	source := RecoverySource{Name: filepath.Base(clean), Kind: RecoverySourceExplicitPath, RegistryFileInfo: classifyRegistryFile(clean, s.migrations)}
	source.Eligible = source.State.restorable()
	if source.Eligible {
		source.Reason = "verified registry envelope"
	} else {
		source.Reason = source.Detail
	}
	return source, nil
}

// classifyRegistryFile reads and classifies one registry-shaped file. It stats
// and reads; it never writes, never repairs permissions, and never creates a
// containing directory.
func classifyRegistryFile(path string, migrations coremetadata.MigrationSet) RegistryFileInfo {
	info := RegistryFileInfo{Path: path}
	stat, statErr := os.Stat(path)
	if statErr == nil {
		info.Size = stat.Size()
		info.ModifiedAt = stat.ModTime().UTC().Format(time.RFC3339)
		if !stat.Mode().IsRegular() {
			info.State = RegistryStateUnreadable
			info.Detail = fmt.Sprintf("%s is not a regular file", path)
			return info
		}
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is a store-owned recovery copy or an operator-named source
	if err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			info.State = RegistryStateMissing
			info.Detail = fmt.Sprintf("%s does not exist", path)
		case errors.Is(err, fs.ErrPermission):
			info.State = RegistryStateUnreadable
			info.Detail = fmt.Sprintf("%s is not readable: %v", path, err)
		default:
			info.State = RegistryStateUnreadable
			info.Detail = fmt.Sprintf("%s could not be read: %v", path, err)
		}
		return info
	}
	info.Size = int64(len(data))
	info.Checksum = checksumOf(data)
	classifyRegistryBytes(&info, data, migrations)
	return info
}

// classifyRegistryBytes is the single content classifier behind both the plan
// and the restore verification, so a source can never be described one way in a
// preview and validated another way at publish time.
func classifyRegistryBytes(info *RegistryFileInfo, data []byte, migrations coremetadata.MigrationSet) {
	if len(strings.TrimSpace(string(data))) == 0 {
		info.State = RegistryStateEmpty
		info.Detail = fmt.Sprintf("%s holds no content", info.Path)
		return
	}
	var envelope struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		info.State = RegistryStateMalformed
		info.Detail = fmt.Sprintf("%s is not decodable JSON: %v", info.Path, err)
		return
	}
	info.SchemaVersion = envelope.SchemaVersion
	// Classify the envelope before decoding the body, exactly as the read path
	// does: a version this build does not know must be refused without any
	// field of it being reinterpreted.
	if _, err := coremetadata.ClassifySchemaVersionWith(migrations, envelope.SchemaVersion); err != nil {
		info.State = RegistryStateSchemaTooNew
		info.Detail = fmt.Sprintf("%s: %v", info.Path, err)
		return
	}
	var registry coremetadata.Registry
	if err := json.Unmarshal(data, &registry); err != nil {
		info.State = RegistryStateMalformed
		info.Detail = fmt.Sprintf("%s does not decode into a registry: %v", info.Path, err)
		return
	}
	migrated, _, _, err := coremetadata.MigrateRegistryWithEnvironment(migrations, registry, coremetadata.MigrationEnvironment{
		DirectoryExists: DirExists,
		NewUID:          coremetadata.NewUID,
	})
	if err != nil {
		info.State = RegistryStateInvalid
		info.Detail = fmt.Sprintf("%s cannot be migrated to the current schema: %v", info.Path, err)
		return
	}
	// The graph guard. A copy that decodes but holds a duplicate uid, a
	// dangling ownerRef, or a broken name reservation is not a state any route
	// could operate on, so it is not a state a restore may publish.
	if err := migrated.Validate(); err != nil {
		info.State = RegistryStateInvalid
		info.Detail = fmt.Sprintf("%s is not a valid resource graph: %v", info.Path, err)
		return
	}
	info.State = RegistryStateValid
	info.Contents = contentsOf(migrated)
}

func contentsOf(registry coremetadata.Registry) RegistryContents {
	return RegistryContents{
		Projects:     len(registry.Projects),
		Windows:      len(registry.Windows),
		Panes:        len(registry.Panes),
		Agents:       len(registry.Agents),
		Reservations: len(registry.NameReservations),
	}
}

// checksumOf digests exact bytes. The full digest is kept rather than a
// truncated one because it is used as a race guard, not as a display label.
func checksumOf(data []byte) string {
	sum := sha256.Sum256(data)
	return checksumPrefix + hex.EncodeToString(sum[:])
}

// RestoreRequest is one explicit, fully specified restore.
type RestoreRequest struct {
	// SourcePath is the exact file to publish. There is no "pick the newest"
	// mode: selection happens in the caller, against a plan the operator saw.
	SourcePath string
	// ExpectSourceChecksum and ExpectCurrentChecksum are the operator's guards
	// tying this restore to the plan it was read from. Empty means unguarded at
	// the operator level; the restore still proves internally that neither file
	// changed between verification and publish.
	ExpectSourceChecksum  string
	ExpectCurrentChecksum string
}

// RestoreResult reports what a restore did.
type RestoreResult struct {
	SourcePath     string `json:"sourcePath"`
	SourceChecksum string `json:"sourceChecksum"`
	// PublishedChecksum is the checksum of the canonical bytes committed to
	// registry.json. It differs from SourceChecksum only for a v3 source.
	PublishedChecksum string           `json:"publishedChecksum"`
	Contents          RegistryContents `json:"contents"`
	// Changed is false when the registry already held the source bytes. A
	// repeat restore is a no-op: no rename, no preserved copy, no marker write.
	Changed bool `json:"changed"`
	// PreservedPath holds the bytes this restore replaced, empty when there
	// were none. Corrupt bytes are preserved too -- especially those, since a
	// misjudged restore is the one case where the damaged original is the only
	// remaining evidence.
	PreservedPath     string `json:"preservedPath,omitempty"`
	PreservedChecksum string `json:"preservedChecksum,omitempty"`
	// ReplacedState is how the bytes this restore replaced classified.
	ReplacedState RegistryState `json:"replacedState,omitempty"`
	// V3 migration evidence is internal to the registry recovery boundary. It
	// records the exact imported bytes before their canonical v4 replacement;
	// no public lifecycle OperationReceipt is introduced here.
	SourceBackupPath    string                       `json:"sourceBackupPath,omitempty"`
	MigrationReportPath string                       `json:"migrationReportPath,omitempty"`
	Migration           coremetadata.MigrationReport `json:"-"`
}

type verifiedRecoverySource struct {
	info            RegistryFileInfo
	sourceBytes     []byte
	publishBytes    []byte
	publishChecksum string
	migration       coremetadata.MigrationReport
}

// RestoreFrom publishes one verified source over the live registry.
//
// The sequence is: verify the source with no lock held, take the recovery-only
// lock, re-read and re-verify the source, read the current bytes, enforce both
// operator guards, no-op out if the bytes already match, preserve the current
// bytes, stage the canonical publish bytes, re-verify the staged file, publish the
// initialized marker, prove under the lock that neither input moved, and only
// then rename. Every failure before the rename leaves the registry
// byte-identical and undoes whatever this call created.
//
// A current source is published verbatim. A v3 source is migrated in memory,
// its exact input is backed up with checksum-bearing evidence, and only the
// deterministic canonical v4 bytes are staged. Repeat restores compare against
// the canonical publish checksum and therefore remain byte-level no-ops.
func (s *Store) RestoreFrom(req RestoreRequest) (RestoreResult, error) {
	if s == nil {
		return RestoreResult{}, errors.New("metadata: nil registry store")
	}
	if !filepath.IsAbs(req.SourcePath) {
		return RestoreResult{}, fmt.Errorf("metadata: recovery source %q must be an absolute path", req.SourcePath)
	}
	source := filepath.Clean(req.SourcePath)
	if source == filepath.Clean(s.path) {
		return RestoreResult{}, fmt.Errorf("metadata: %w: %s is the live registry, not a recovery source", ErrRecoverySourceRejected, source)
	}
	// Verify before the lock. A rejected source must not have created the
	// metadata directory or a lock file on its way to being refused.
	verified, err := s.verifyRecoverySource(source, req.ExpectSourceChecksum)
	if err != nil {
		return RestoreResult{}, err
	}

	var result RestoreResult
	err = s.withRecoveryLock(func() error {
		// Re-verify under the recovery lock. Between the pre-lock verification and here
		// the file could have been replaced by something this build must not
		// publish, so the classification is redone rather than trusted.
		relocked, err := s.verifyRecoverySource(source, req.ExpectSourceChecksum)
		if err != nil {
			return err
		}
		if relocked.info.Checksum != verified.info.Checksum {
			return fmt.Errorf("metadata: %w: %s was %s when planned and is %s under the lock",
				ErrRecoveryRaced, source, verified.info.Checksum, relocked.info.Checksum)
		}
		verified = relocked

		current, currentBytes := s.currentForRestore()
		if guard := strings.TrimSpace(req.ExpectCurrentChecksum); guard != "" && guard != current.Checksum {
			return fmt.Errorf("metadata: %w: %s expected %s but holds %s; re-run the preview before restoring",
				ErrRecoveryRaced, s.path, guard, displayChecksum(current.Checksum))
		}

		result = RestoreResult{
			SourcePath: source, SourceChecksum: verified.info.Checksum,
			PublishedChecksum: verified.publishChecksum,
			Contents:          verified.info.Contents, ReplacedState: current.State,
			Migration: verified.migration,
		}
		// A repeat restore is a no-op on bytes, which is stronger than a no-op
		// on meaning: nothing is renamed, no copy is taken, and the marker,
		// inode, and mtime of the registry are untouched.
		if current.Checksum == verified.publishChecksum {
			return nil
		}
		return s.publishRestore(source, verified, currentBytes, current, &result)
	})
	if err != nil {
		return RestoreResult{}, err
	}
	return result, nil
}

// currentForRestore reads the live registry as raw bytes plus a classification.
// Unlike the read path it does not fail on damaged content: the whole point of a
// restore is to run against a registry that cannot be read normally.
func (s *Store) currentForRestore() (RegistryFileInfo, []byte) {
	info := classifyRegistryFile(s.path, s.migrations)
	data, err := os.ReadFile(s.path) // #nosec G304 -- s.path is the store's own registry path
	if err != nil {
		return info, nil
	}
	return info, data
}

// publishRestore performs the mutating half. It is only reached with the
// recovery lock held, a verified source, and a decision that the bytes actually
// differ. It never acquires or waits for the ordinary mutation lock.
func (s *Store) publishRestore(source string, verified verifiedRecoverySource, currentBytes []byte, current RegistryFileInfo, result *RestoreResult) error {
	dir := filepath.Dir(s.path)
	if err := localstate.EnsurePrivateDir(dir); err != nil {
		return fmt.Errorf("metadata: create registry dir %s: %w", dir, err)
	}
	rollback := newRollback()
	pairPublished := false
	defer func() {
		if pairPublished {
			return
		}
		if result.MigrationReportPath != "" {
			_ = os.Remove(result.MigrationReportPath)
		}
		if result.SourceBackupPath != "" {
			_ = os.Remove(result.SourceBackupPath)
		}
	}()

	// Preserve first. Every later step can fail, and the operator's way back
	// from a restore they regret is these bytes.
	if len(currentBytes) > 0 {
		preserved, err := s.preserveReplacedBytes(currentBytes)
		if err != nil {
			return rollback.undo(err)
		}
		rollback.add(func() { _ = os.Remove(preserved) })
		result.PreservedPath = preserved
		result.PreservedChecksum = current.Checksum
	}
	if verified.info.SchemaVersion == 3 {
		backupPath, err := s.backupBytes(verified.sourceBytes, 3)
		if err != nil {
			return rollback.undo(err)
		}
		result.SourceBackupPath = backupPath
		if s.hooks.afterBackup != nil {
			if err := s.hooks.afterBackup(); err != nil {
				return rollback.undo(err)
			}
		}
		reportPath, err := s.writeMigrationEvidence(backupPath, verified.migration)
		if err != nil {
			return rollback.undo(err)
		}
		result.MigrationReportPath = reportPath
		pairPublished = true
		if s.hooks.afterMigrationReport != nil {
			if err := s.hooks.afterMigrationReport(); err != nil {
				return rollback.undo(err)
			}
		}
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(s.path)+".tmp-*")
	if err != nil {
		return rollback.undo(fmt.Errorf("metadata: create temp registry: %w", err))
	}
	tmpName := tmp.Name()
	staged := true
	defer func() {
		if staged {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(verified.publishBytes); err != nil {
		_ = tmp.Close()
		return rollback.undo(fmt.Errorf("metadata: write temp registry: %w", err))
	}
	if err := s.syncFile(tmp); err != nil {
		_ = tmp.Close()
		return rollback.undo(fmt.Errorf("metadata: sync temp registry: %w", err))
	}
	if err := tmp.Close(); err != nil {
		return rollback.undo(fmt.Errorf("metadata: close temp registry: %w", err))
	}
	// Re-verify the staged file rather than trusting that the bytes made it to
	// disk intact. This is the same guard the write path applies, restated for
	// bytes that came from outside this process.
	stagedInfo := RegistryFileInfo{Path: tmpName}
	stagedBytes, err := os.ReadFile(tmpName) // #nosec G304 -- tmpName is this call's own staged file
	if err != nil {
		return rollback.undo(fmt.Errorf("metadata: reread staged registry: %w", err))
	}
	classifyRegistryBytes(&stagedInfo, stagedBytes, s.migrations)
	if !stagedInfo.State.restorable() {
		return rollback.undo(fmt.Errorf("metadata: %w: staged copy of %s did not verify: %s", ErrRecoverySourceRejected, source, stagedInfo.Detail))
	}
	if checksumOf(stagedBytes) != verified.publishChecksum {
		return rollback.undo(fmt.Errorf("metadata: staged canonical copy of %s does not match the verified publish checksum", source))
	}

	created, err := s.ensureInitializedMarker()
	if err != nil {
		return rollback.undo(err)
	}
	if created {
		rollback.add(func() { _ = os.Remove(s.markerPath) })
	}
	if err := s.syncDir(dir); err != nil {
		return rollback.undo(err)
	}

	// The last guard before the only mutating step: both inputs must still be
	// what was verified. A source rewritten mid-restore, or a registry replaced
	// by something outside this store's lock, refuses here with nothing
	// published.
	if err := s.confirmRestoreInputs(source, result.SourceChecksum, current.Checksum); err != nil {
		return rollback.undo(err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return rollback.undo(fmt.Errorf("metadata: rename temp registry: %w", err))
	}
	staged = false
	// Past the rename the source bytes are the registry. What remains is
	// durability and hygiene, and a failure there is not a failed restore.
	_ = s.syncDir(dir)
	localstate.RepairPrivateFile(s.path)
	s.prunePreservedCopies()
	result.Changed = true
	return nil
}

// confirmRestoreInputs re-hashes both inputs immediately before the rename.
func (s *Store) confirmRestoreInputs(source, sourceChecksum, currentChecksum string) error {
	data, err := os.ReadFile(source) // #nosec G304 -- source is the operator-named recovery source already verified above
	if err != nil {
		return fmt.Errorf("metadata: %w: %s became unreadable before the restore: %v", ErrRecoveryRaced, source, err)
	}
	if got := checksumOf(data); got != sourceChecksum {
		return fmt.Errorf("metadata: %w: %s changed from %s to %s during the restore", ErrRecoveryRaced, source, sourceChecksum, got)
	}
	observed := classifyRegistryFile(s.path, s.migrations)
	if observed.Checksum != currentChecksum {
		return fmt.Errorf("metadata: %w: %s changed from %s to %s during the restore",
			ErrRecoveryRaced, s.path, displayChecksum(currentChecksum), displayChecksum(observed.Checksum))
	}
	return nil
}

func displayChecksum(checksum string) string {
	if checksum == "" {
		return "no bytes"
	}
	return checksum
}

// verifyRecoverySource reads, classifies, and guards one source, returning its
// classification and exact bytes.
func (s *Store) verifyRecoverySource(path, expectChecksum string) (verifiedRecoverySource, error) {
	info := classifyRegistryFile(path, s.migrations)
	if !info.State.restorable() {
		return verifiedRecoverySource{}, fmt.Errorf("metadata: %w: %s", ErrRecoverySourceRejected, info.Detail)
	}
	if guard := strings.TrimSpace(expectChecksum); guard != "" && guard != info.Checksum {
		return verifiedRecoverySource{}, fmt.Errorf("metadata: %w: %s expected %s but holds %s; re-run the preview before restoring",
			ErrRecoveryRaced, path, guard, info.Checksum)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is a store-owned recovery copy or an operator-named source
	if err != nil {
		return verifiedRecoverySource{}, fmt.Errorf("metadata: read recovery source %s: %w", path, err)
	}
	// The classification above read the file separately, so prove the bytes
	// this call is about to publish are the bytes that were classified.
	if checksumOf(data) != info.Checksum {
		return verifiedRecoverySource{}, fmt.Errorf("metadata: %w: %s changed while it was being verified", ErrRecoveryRaced, path)
	}
	verified := verifiedRecoverySource{info: info, sourceBytes: data, publishBytes: data, publishChecksum: info.Checksum}
	if info.SchemaVersion != 3 {
		return verified, nil
	}
	var sourceRegistry coremetadata.Registry
	if err := json.Unmarshal(data, &sourceRegistry); err != nil {
		return verifiedRecoverySource{}, fmt.Errorf("metadata: %w: decode v3 recovery source %s: %v", ErrRecoverySourceRejected, path, err)
	}
	migrated, _, report, err := coremetadata.MigrateRegistryWithEnvironment(s.migrations, sourceRegistry, s.migrationEnv)
	if err != nil {
		return verifiedRecoverySource{}, fmt.Errorf("metadata: %w: migrate v3 recovery source %s: %v", ErrRecoverySourceRejected, path, err)
	}
	publish, err := json.MarshalIndent(migrated.Normalize(), "", "  ")
	if err != nil {
		return verifiedRecoverySource{}, fmt.Errorf("metadata: encode canonical v4 recovery source %s: %w", path, err)
	}
	verified.publishBytes = append(publish, '\n')
	verified.publishChecksum = checksumOf(verified.publishBytes)
	verified.migration = report
	return verified, nil
}

// preserveReplacedBytes copies the bytes a restore is about to replace.
//
// Unlike the write-side copy this keeps content that does not verify. A
// verified-only rule would throw away precisely the bytes an operator most
// needs back: the damaged registry that motivated the restore in the first
// place.
func (s *Store) preserveReplacedBytes(data []byte) (string, error) {
	if err := localstate.EnsurePrivateDir(s.recoveryDir); err != nil {
		return "", fmt.Errorf("metadata: create registry recovery dir %s: %w", s.recoveryDir, err)
	}
	path, err := s.nextPreservedPath()
	if err != nil {
		return "", err
	}
	if err := s.writePrivateFile(path, data); err != nil {
		return "", fmt.Errorf("metadata: write replaced registry copy %s: %w", path, err)
	}
	return path, nil
}

// nextPreservedPath allocates the next replaced-copy name, using the same
// stamp-plus-sequence scheme as the write-side copies so lexicographic order
// stays creation order within the family.
func (s *Store) nextPreservedPath() (string, error) {
	stamp := s.clock().UTC().Format(recoveryStampLayout)
	seq := 0
	if names := s.preservedCopyNames(); len(names) > 0 {
		newestStamp, newestSeq, ok := parseRecoveryName(recoveryFilePrefix + strings.TrimPrefix(names[len(names)-1], preservedFilePrefix))
		if ok && newestStamp >= stamp {
			stamp, seq = newestStamp, newestSeq+1
		}
	}
	for range recoverySequenceLimit * recoverySequenceLimit {
		if seq >= recoverySequenceLimit {
			parsed, err := time.Parse(recoveryStampLayout, stamp)
			if err != nil {
				return "", fmt.Errorf("metadata: unparsable replaced registry stamp %q in %s", stamp, s.recoveryDir)
			}
			stamp, seq = parsed.Add(time.Second).Format(recoveryStampLayout), 0
		}
		path := filepath.Join(s.recoveryDir, fmt.Sprintf("%s%s-%02d%s", preservedFilePrefix, stamp, seq, recoveryFileSuffix))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return path, nil
		} else if err != nil {
			return "", fmt.Errorf("metadata: stat replaced registry copy %s: %w", path, err)
		}
		seq++
	}
	return "", fmt.Errorf("metadata: no free replaced registry copy name near %s in %s", stamp, s.recoveryDir)
}

// preservedCopyNames lists the replaced copies oldest first.
func (s *Store) preservedCopyNames() []string {
	entries, err := os.ReadDir(s.recoveryDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, preservedFilePrefix) || !strings.HasSuffix(name, recoveryFileSuffix) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// prunePreservedCopies bounds the replaced-copy family the same way the write
// side bounds its own. It runs after a committed restore, so like the write-side
// prune it is hygiene: a filesystem that refuses the removals must not turn a
// committed restore into a reported failure.
func (s *Store) prunePreservedCopies() {
	retention := s.retention
	if retention <= 0 {
		retention = defaultRecoveryRetention
	}
	names := s.preservedCopyNames()
	if len(names) <= retention {
		return
	}
	for _, name := range names[:len(names)-retention] {
		_ = os.Remove(filepath.Join(s.recoveryDir, name))
	}
}
