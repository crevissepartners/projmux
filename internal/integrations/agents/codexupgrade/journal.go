// Package codexupgrade persists the private machine-local routing and the
// content-free Phase 4 rolling-admission operation. It owns neither Registry
// resource identity nor tmux/provider content.
package codexupgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	"github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexbundle"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
	localstate "github.com/crevissepartners/projmux/internal/state"
)

const (
	JournalVersion  = 1
	journalDirName  = "codex-generations"
	journalFileName = "rolling-upgrade.json"
)

// GenerationConfig is the serializable launch configuration. Environment is
// deliberately absent: private hosts receive the coordinator's sanitized
// process environment, with TMUX/TMUX_PANE removed by the host contract.
type GenerationConfig struct {
	Endpoint         metadata.CodexEndpointRef `json:"endpoint"`
	StateDomainPath  string                    `json:"stateDomainPath"`
	PrivateRoot      string                    `json:"privateRoot"`
	SocketPath       string                    `json:"socketPath"`
	LeaseRoot        string                    `json:"leaseRoot"`
	RequiredProtocol codexbundle.ProtocolRange `json:"requiredProtocol"`
}

func (cfg GenerationConfig) hostConfig() codexgenerationhost.PrivateGenerationConfig {
	return codexgenerationhost.PrivateGenerationConfig{
		Endpoint: codexgenerationhost.EndpointIdentity{
			StateDomainID: cfg.Endpoint.StateDomainID, EndpointGenerationID: cfg.Endpoint.EndpointGenerationID,
		},
		StateDomainPath: cfg.StateDomainPath, PrivateRoot: cfg.PrivateRoot,
		SocketPath: cfg.SocketPath, LeaseRoot: cfg.LeaseRoot, RequiredProtocol: cfg.RequiredProtocol,
	}
}

// HostConfig exposes the exact path-bearing launch input to the Phase 5
// production lifecycle adapter. Callers cannot weaken it: Journal validation
// has already checked every path and endpoint relation.
func (cfg GenerationConfig) HostConfig() codexgenerationhost.PrivateGenerationConfig {
	return cfg.hostConfig()
}

func ObserveRoute(ctx context.Context, route GenerationRoute) error {
	if route.Proof == nil || !route.Ready {
		return errors.New("codex generation route is not ready")
	}
	return codexgenerationhost.ObservePrivateGenerationRoute(ctx, route.Config.hostConfig(), *route.Proof, route.TUIPath)
}

func (cfg GenerationConfig) valid() bool {
	return cfg.Endpoint.Valid() && filepath.IsAbs(cfg.StateDomainPath) && filepath.IsAbs(cfg.PrivateRoot) &&
		filepath.IsAbs(cfg.SocketPath) && filepath.IsAbs(cfg.LeaseRoot) && cfg.RequiredProtocol.Valid() &&
		filepath.Clean(cfg.StateDomainPath) == cfg.StateDomainPath && filepath.Clean(cfg.PrivateRoot) == cfg.PrivateRoot &&
		filepath.Clean(cfg.SocketPath) == cfg.SocketPath && filepath.Clean(cfg.LeaseRoot) == cfg.LeaseRoot
}

// GenerationRoute is one exact pool slot plus its private transport proof.
// Paths stay in this owner-private journal and never enter Registry metadata.
type GenerationRoute struct {
	Generation         codexgeneration.Generation       `json:"generation"`
	Version            string                           `json:"version,omitempty"`
	Config             GenerationConfig                 `json:"config"`
	TUIPath            string                           `json:"tuiPath"`
	LaunchOperationRef string                           `json:"launchOperationRef,omitempty"`
	Ready              bool                             `json:"ready"`
	Proof              *codexgenerationhost.LaunchProof `json:"proof,omitempty"`
}

func (route GenerationRoute) valid(stateDomainID string) bool {
	if route.Generation.Endpoint.StateDomainID != stateDomainID {
		return false
	}
	if route.Version != "" {
		if !managedActivationVersionPattern.MatchString(route.Version) ||
			route.Generation.Endpoint.EndpointGenerationID != "codex-"+route.Version {
			return false
		}
	}
	if route.Generation.Owner != codexgeneration.OwnerProjmuxPrivate {
		ownerValid := route.Generation.Owner == codexgeneration.OwnerOfficialManaged || route.Generation.Owner == codexgeneration.OwnerUnmanaged || route.Generation.Owner == codexgeneration.OwnerUnknown
		versionValid := route.Version == "" || route.Generation.BundleID == "external-"+route.Version
		return ownerValid && versionValid &&
			route.Config == (GenerationConfig{}) && route.TUIPath == "" && route.LaunchOperationRef == "" && !route.Ready && route.Proof == nil
	}
	if !route.Generation.Endpoint.Same(route.Config.Endpoint) || !route.Config.valid() || !filepath.IsAbs(route.TUIPath) || filepath.Clean(route.TUIPath) != route.TUIPath {
		return false
	}
	if route.Ready != (route.Proof != nil) {
		return false
	}
	if route.Proof != nil {
		proofEndpoint := metadata.CodexEndpointRef{
			StateDomainID:        route.Proof.Endpoint.StateDomainID,
			EndpointGenerationID: route.Proof.Endpoint.EndpointGenerationID,
		}
		if !proofEndpoint.Same(route.Generation.Endpoint) || route.Proof.SocketPath != route.Config.SocketPath || route.Proof.BundleID != route.Generation.BundleID {
			return false
		}
	}
	return true
}

// Journal is the complete authoritative admission pool. Operation is retained
// after Draining/HandoverPending so every resume reuses the same ref.
type Journal struct {
	Version             int                                      `json:"version"`
	StateDomainID       string                                   `json:"stateDomainID"`
	CurrentGenerationID string                                   `json:"currentGenerationID"`
	Routes              []GenerationRoute                        `json:"routes"`
	Obligations         []codexgeneration.AgentObligation        `json:"obligations,omitempty"`
	Qualification       *codexgeneration.QualificationResult     `json:"qualification,omitempty"`
	Operation           *codexgeneration.RollingUpgradeOperation `json:"operation,omitempty"`
	ColdRecovery        *codexgeneration.ColdRecoveryOperation   `json:"coldRecovery,omitempty"`
	Handover            *codexgeneration.HandoverOperation       `json:"handover,omitempty"`
}

func (j Journal) Validate() error {
	if j.Version != JournalVersion || strings.TrimSpace(j.StateDomainID) == "" || len(j.Routes) == 0 {
		return errors.New("invalid Codex rolling journal")
	}
	pool := codexgeneration.Pool{StateDomainID: j.StateDomainID, Obligations: slices.Clone(j.Obligations)}
	seenCurrent := ""
	for _, route := range j.Routes {
		if !route.valid(j.StateDomainID) {
			return errors.New("invalid Codex generation route")
		}
		pool.Generations = append(pool.Generations, route.Generation)
		if route.Generation.State == codexgeneration.StateCurrent {
			seenCurrent = route.Generation.Endpoint.EndpointGenerationID
			if route.Generation.Owner == codexgeneration.OwnerProjmuxPrivate && !route.Ready {
				return errors.New("admission-current Codex generation is not ready")
			}
			if route.Generation.Owner != codexgeneration.OwnerProjmuxPrivate &&
				route.Generation.Owner != codexgeneration.OwnerOfficialManaged &&
				route.Generation.Owner != codexgeneration.OwnerUnmanaged {
				return errors.New("admission-current external Codex generation has unknown ownership")
			}
		}
	}
	if err := pool.Validate(); err != nil {
		return err
	}
	if seenCurrent == "" || j.CurrentGenerationID != seenCurrent {
		return errors.New("codex admission-current pointer mismatch")
	}
	if j.Operation != nil {
		if err := j.Operation.Validate(); err != nil {
			return err
		}
		if j.Operation.StateDomainID != j.StateDomainID {
			return errors.New("codex operation state domain mismatch")
		}
	}
	if j.Qualification != nil {
		if err := j.Qualification.Validate(); err != nil {
			return err
		}
	}
	if j.ColdRecovery != nil {
		if err := j.ColdRecovery.Validate(); err != nil {
			return err
		}
		recoveryEndpoint := metadata.CodexEndpointRef{StateDomainID: j.ColdRecovery.StateDomainID, EndpointGenerationID: j.ColdRecovery.GenerationID}
		route, ok := j.Route(recoveryEndpoint)
		if j.Operation == nil || !ok || route.Generation.Owner != codexgeneration.OwnerProjmuxPrivate ||
			j.Operation.OperationRef != j.ColdRecovery.RollingOperationRef || j.Operation.OldGenerationID != j.ColdRecovery.GenerationID ||
			j.StateDomainID != j.ColdRecovery.StateDomainID || route.LaunchOperationRef != j.ColdRecovery.LaunchOperationRef {
			return errors.New("codex cold recovery is not linked to exact old generation authority")
		}
	}
	if j.Handover != nil {
		if err := j.Handover.Validate(); err != nil {
			return err
		}
		if j.Operation == nil || j.Operation.OperationRef != j.Handover.RollingOperationRef ||
			!j.Operation.HandoverRequested || j.Operation.Aborted || j.Operation.OldGenerationID != j.Handover.OldGenerationID ||
			j.Operation.TargetGenerationID != j.Handover.SuccessorGenerationID || j.StateDomainID != j.Handover.StateDomainID {
			return errors.New("codex handover is not linked to exact rolling operation")
		}
	}
	return nil
}

func (j Journal) Pool() codexgeneration.Pool {
	pool := codexgeneration.Pool{StateDomainID: j.StateDomainID, Obligations: slices.Clone(j.Obligations)}
	for _, route := range j.Routes {
		pool.Generations = append(pool.Generations, route.Generation)
	}
	return pool
}

func (j Journal) Route(endpoint metadata.CodexEndpointRef) (GenerationRoute, bool) {
	for _, route := range j.Routes {
		if route.Generation.Endpoint.Same(endpoint) {
			return route, true
		}
	}
	return GenerationRoute{}, false
}

func (j Journal) CurrentRoute() (GenerationRoute, bool) {
	for _, route := range j.Routes {
		if route.Generation.Endpoint.EndpointGenerationID == j.CurrentGenerationID && route.Generation.State == codexgeneration.StateCurrent {
			return route, true
		}
	}
	return GenerationRoute{}, false
}

type journalHooks struct {
	afterTempWrite func() error
	beforeRename   func() error
}

// Store serializes cross-process plan/apply/resume/abort journal access with a
// persistent advisory lock and atomic fsync+rename writes.
type Store struct {
	path  string
	hooks journalHooks
}

func PathFor(stateDir string) string       { return filepath.Join(stateDir, journalDirName, journalFileName) }
func NewStore(path string) *Store          { return &Store{path: path} }
func NewStateStore(stateDir string) *Store { return NewStore(PathFor(stateDir)) }
func (store *Store) Path() string {
	if store == nil {
		return ""
	}
	return store.path
}

func (store *Store) Load() (Journal, bool, error) {
	if store == nil || !filepath.IsAbs(store.path) {
		return Journal{}, false, errors.New("invalid Codex rolling journal path")
	}
	body, err := os.ReadFile(store.path) // #nosec G304 -- explicit owner-private state path
	if errors.Is(err, fs.ErrNotExist) {
		return Journal{}, false, nil
	}
	if err != nil {
		return Journal{}, false, err
	}
	var journal Journal
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return Journal{}, false, fmt.Errorf("decode Codex rolling journal: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Journal{}, false, errors.New("codex rolling journal has trailing JSON")
	}
	if err := journal.Validate(); err != nil {
		return Journal{}, false, err
	}
	return journal, true, nil
}

func (store *Store) Update(ctx context.Context, fn func(*Journal, bool) error) (Journal, error) {
	if store == nil || fn == nil || !filepath.IsAbs(store.path) {
		return Journal{}, errors.New("invalid Codex rolling journal update")
	}
	if err := localstate.EnsurePrivateDir(filepath.Dir(store.path)); err != nil {
		return Journal{}, err
	}
	lock, err := os.OpenFile(store.path+".flock", os.O_CREATE|os.O_RDWR, localstate.PrivateFileMode) // #nosec G304 -- journal lock sibling
	if err != nil {
		return Journal{}, err
	}
	granted := make(chan error, 1)
	go func() { granted <- unix.Flock(int(lock.Fd()), unix.LOCK_EX) }()
	select {
	case err := <-granted:
		if err != nil {
			_ = lock.Close()
			return Journal{}, err
		}
	case <-ctx.Done():
		// A blocking flock has no cancellation primitive. Hand ownership to a
		// releaser so a later grant cannot strand the journal locked after this
		// operation has returned.
		go func() {
			if err := <-granted; err == nil {
				_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
			}
			_ = lock.Close()
		}()
		return Journal{}, ctx.Err()
	}
	defer lock.Close()
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck -- best effort after durable write

	current, exists, err := store.Load()
	if err != nil {
		return Journal{}, err
	}
	working := cloneJournal(current)
	if err := fn(&working, exists); err != nil {
		return Journal{}, err
	}
	if err := working.Validate(); err != nil {
		return Journal{}, err
	}
	if exists && slices.EqualFunc(current.Routes, working.Routes, func(a, b GenerationRoute) bool {
		return a.Generation == b.Generation && a.Version == b.Version && a.Config == b.Config && a.TUIPath == b.TUIPath && a.LaunchOperationRef == b.LaunchOperationRef && a.Ready == b.Ready && reflectProof(a.Proof, b.Proof)
	}) &&
		current.Version == working.Version && current.StateDomainID == working.StateDomainID && current.CurrentGenerationID == working.CurrentGenerationID &&
		slices.Equal(current.Obligations, working.Obligations) && valuesEqual(current.Qualification, working.Qualification) &&
		operationsEqual(current.Operation, working.Operation) && valuesEqual(current.ColdRecovery, working.ColdRecovery) && valuesEqual(current.Handover, working.Handover) {
		return current, nil
	}
	if err := store.write(working); err != nil {
		return Journal{}, err
	}
	return working, nil
}

func cloneJournal(current Journal) Journal {
	working := current
	working.Routes = slices.Clone(current.Routes)
	for i := range working.Routes {
		if current.Routes[i].Proof != nil {
			proof := *current.Routes[i].Proof
			working.Routes[i].Proof = &proof
		}
	}
	working.Obligations = slices.Clone(current.Obligations)
	if current.Qualification != nil {
		qualification := *current.Qualification
		working.Qualification = &qualification
	}
	if current.Operation != nil {
		operation := *current.Operation
		operation.Ledger = slices.Clone(current.Operation.Ledger)
		working.Operation = &operation
	}
	if current.ColdRecovery != nil {
		recovery := *current.ColdRecovery
		working.ColdRecovery = &recovery
	}
	if current.Handover != nil {
		handover := *current.Handover
		handover.Targets = slices.Clone(current.Handover.Targets)
		handover.Choices = slices.Clone(current.Handover.Choices)
		if current.Handover.ExternalStopReceipt != nil {
			receipt := *current.Handover.ExternalStopReceipt
			handover.ExternalStopReceipt = &receipt
		}
		working.Handover = &handover
	}
	return working
}

func reflectProof(a, b *codexgenerationhost.LaunchProof) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func operationsEqual(a, b *codexgeneration.RollingUpgradeOperation) bool {
	if a == nil || b == nil {
		return a == b
	}
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}

func valuesEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(ab, bb)
}

func (store *Store) write(journal Journal) error {
	body, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(store.path), ".rolling-upgrade-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(localstate.PrivateFileMode); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if _, err := tmp.Write(body); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(err, tmp.Close())
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if store.hooks.afterTempWrite != nil {
		if err := store.hooks.afterTempWrite(); err != nil {
			return err
		}
	}
	if store.hooks.beforeRename != nil {
		if err := store.hooks.beforeRename(); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpName, store.path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(store.path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
