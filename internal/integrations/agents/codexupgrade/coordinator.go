package codexupgrade

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
)

type Decision string

const (
	DecisionReady   Decision = "ready"
	DecisionBlocked Decision = "blocked"
)

type Plan struct {
	Decision          Decision                                  `json:"decision"`
	OperationRef      string                                    `json:"operationRef"`
	StateDomainID     string                                    `json:"stateDomainID"`
	CurrentGeneration string                                    `json:"currentGeneration"`
	TargetGeneration  string                                    `json:"targetGeneration"`
	Blockers          []string                                  `json:"blockers,omitempty"`
	DrainLedger       []codexgeneration.DrainLedgerEntry        `json:"drainLedger,omitempty"`
	Mutations         codexgeneration.MutationCount             `json:"mutations"`
	Phase5            codexgeneration.RollingOperationMutations `json:"phase5Effects"`
}

type Request struct {
	OperationRef   string                              `json:"operationRef"`
	Current        GenerationRoute                     `json:"current"`
	Target         GenerationConfig                    `json:"target"`
	TargetBundleID string                              `json:"targetBundleID"`
	TargetTUIPath  string                              `json:"targetTUIPath"`
	Qualification  codexgeneration.QualificationResult `json:"qualification"`
}

func (request Request) validate() error {
	if strings.TrimSpace(request.OperationRef) == "" || request.Current.Generation.State != codexgeneration.StateCurrent ||
		!request.Current.Ready || request.Current.Proof == nil || !request.Current.valid(request.Current.Generation.Endpoint.StateDomainID) ||
		!request.Target.valid() || request.Current.Generation.Endpoint.StateDomainID != request.Target.Endpoint.StateDomainID ||
		request.Current.Generation.Endpoint.EndpointGenerationID == request.Target.Endpoint.EndpointGenerationID ||
		request.Current.Config.StateDomainPath != request.Target.StateDomainPath ||
		filepath.Dir(request.Current.Config.SocketPath) != request.Current.Config.PrivateRoot ||
		filepath.Dir(request.Target.SocketPath) != request.Target.PrivateRoot ||
		request.Current.Config.SocketPath == request.Target.SocketPath ||
		strings.TrimSpace(request.TargetBundleID) == "" || request.TargetBundleID != strings.TrimSpace(request.TargetBundleID) ||
		!filepathAbsoluteClean(request.TargetTUIPath) {
		return errors.New("invalid rolling upgrade request")
	}
	if err := request.Qualification.Validate(); err != nil || !codexgeneration.GateQualification(request.Qualification).Phase2Ready {
		return errors.New("rolling upgrade qualification is not a Phase 0 YES/YES receipt")
	}
	return nil
}

func verifyRequestArtifacts(request Request) error {
	current, err := codexgenerationhost.VerifyPrivateGenerationBundle(request.Current.Config.hostConfig())
	if err != nil || current.ID != request.Current.Generation.BundleID || current.ID != request.Current.Proof.BundleID ||
		current.ServerPath != request.Current.Proof.ExecutablePath || current.TUIPath != request.Current.TUIPath {
		return errors.New("exact-current-release-bundle-unverified")
	}
	target, err := codexgenerationhost.VerifyPrivateGenerationBundle(request.Target.hostConfig())
	if err != nil || target.ID != request.TargetBundleID || target.TUIPath != request.TargetTUIPath {
		return errors.New("exact-target-release-bundle-unverified")
	}
	if request.Qualification.Versions.Old != current.Version || request.Qualification.Versions.New != target.Version {
		return errors.New("qualification-version-pair-mismatch")
	}
	paths := []string{
		request.Current.Config.PrivateRoot,
		request.Target.PrivateRoot,
		request.Current.Config.StateDomainPath,
		request.Current.Config.LeaseRoot,
		request.Target.LeaseRoot,
	}
	canonical := make([]string, len(paths))
	for index, path := range paths {
		canonical[index], err = filepath.EvalSymlinks(path)
		if err != nil || !filepathAbsoluteClean(canonical[index]) {
			return errors.New("rolling-upgrade-root-identity-unverified")
		}
	}
	// Both mutable runtime roots must be pairwise disjoint and must not contain
	// or be contained by the shared mutable state domain or either immutable
	// lease. Candidate intent/socket writes therefore cannot alias state or
	// qualified release bytes through a symlinked ancestor spelling.
	for _, pair := range [][2]int{{0, 1}, {0, 2}, {0, 3}, {0, 4}, {1, 2}, {1, 3}, {1, 4}} {
		if pathsOverlap(canonical[pair[0]], canonical[pair[1]]) {
			return errors.New("rolling-upgrade-roots-overlap")
		}
	}
	return nil
}

func pathsOverlap(first, second string) bool {
	if first == second {
		return true
	}
	firstToSecond, firstErr := filepath.Rel(first, second)
	secondToFirst, secondErr := filepath.Rel(second, first)
	isDescendant := func(relative string, err error) bool {
		return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	return isDescendant(firstToSecond, firstErr) || isDescendant(secondToFirst, secondErr)
}

func filepathAbsoluteClean(path string) bool {
	return path != "" && filepath.IsAbs(path) && path == strings.TrimSpace(path) && path == filepath.Clean(path)
}

type RegistryStore interface {
	LoadSnapshot() (coremetadata.Registry, error)
	WithAdmissionBarrier(func(coremetadata.Registry) error) error
	UpdateConvergent(func(*coremetadata.Registry) error) (coremetadata.Registry, bool, error)
}

type CandidateRuntime interface {
	Observe(context.Context, GenerationConfig, codexgenerationhost.LaunchProof, string) error
	Prepare(context.Context, GenerationConfig, string, func(func() error) error, func() error, func(codexgenerationhost.LaunchProof) error) error
	Cleanup(context.Context, GenerationConfig, string, *codexgenerationhost.LaunchProof) (bool, error)
}

type privateCandidateRuntime struct{}

func (privateCandidateRuntime) Observe(ctx context.Context, cfg GenerationConfig, proof codexgenerationhost.LaunchProof, tuiPath string) error {
	return codexgenerationhost.ObservePrivateGenerationRoute(ctx, cfg.hostConfig(), proof, tuiPath)
}

func (privateCandidateRuntime) Prepare(ctx context.Context, cfg GenerationConfig, operationRef string, authorizeLaunch func(func() error) error, afterLaunch func() error, publish func(codexgenerationhost.LaunchProof) error) error {
	return codexgenerationhost.PrepareDurableGeneration(ctx, cfg.hostConfig(), operationRef, authorizeLaunch, afterLaunch, publish)
}

func (privateCandidateRuntime) Cleanup(ctx context.Context, cfg GenerationConfig, operationRef string, proof *codexgenerationhost.LaunchProof) (bool, error) {
	return codexgenerationhost.CleanupDurableCandidate(ctx, cfg.hostConfig(), operationRef, proof)
}

type Coordinator struct {
	Journal   *Store
	Registry  RegistryStore
	Runtime   CandidateRuntime
	Mutator   func() coremetadata.Mutator
	Failpoint func(string) error
}

const (
	FailBeforePrewrite       = "before-prewrite"
	FailAfterPrewrite        = "after-prewrite"
	FailBeforeCandidate      = "before-candidate"
	FailAfterCandidateLaunch = "after-candidate-launch-before-proof"
	FailAfterCandidate       = "after-candidate"
	FailBeforeAdmission      = "before-admission"
	FailAfterAdmission       = "after-admission"
	FailBeforeDrain          = "before-drain"
	FailAfterDrainRegistry   = "after-drain-registry"
	FailAfterDrainReceipt    = "after-drain-receipt"
)

func (coordinator *Coordinator) runtime() CandidateRuntime {
	if coordinator.Runtime != nil {
		return coordinator.Runtime
	}
	return privateCandidateRuntime{}
}

func (coordinator *Coordinator) mutator() coremetadata.Mutator {
	if coordinator.Mutator != nil {
		return coordinator.Mutator()
	}
	return coremetadata.Mutator{}
}

func (coordinator *Coordinator) fail(point string) error {
	if coordinator.Failpoint == nil {
		return nil
	}
	return coordinator.Failpoint(point)
}

func (coordinator *Coordinator) Plan(ctx context.Context, request Request) Plan {
	plan := Plan{
		Decision: DecisionReady, OperationRef: request.OperationRef,
		StateDomainID:     request.Current.Generation.Endpoint.StateDomainID,
		CurrentGeneration: request.Current.Generation.Endpoint.EndpointGenerationID,
		TargetGeneration:  request.Target.Endpoint.EndpointGenerationID,
	}
	if err := request.validate(); err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
		plan.Decision = DecisionBlocked
		return plan
	}
	if err := verifyRequestArtifacts(request); err != nil {
		plan.Blockers = append(plan.Blockers, err.Error())
		plan.Decision = DecisionBlocked
		return plan
	}
	if err := coordinator.runtime().Observe(ctx, request.Current.Config, *request.Current.Proof, request.Current.TUIPath); err != nil {
		plan.Blockers = append(plan.Blockers, "exact-current-owner-readiness-unproven")
	}
	if coordinator.Journal == nil {
		plan.Blockers = append(plan.Blockers, "rolling-journal-unconfigured")
	} else if journal, exists, err := coordinator.Journal.Load(); err != nil {
		plan.Blockers = append(plan.Blockers, "rolling-journal-invalid")
	} else if exists {
		current, ok := journal.CurrentRoute()
		if !ok || !current.Generation.Endpoint.Same(request.Current.Generation.Endpoint) || !reflectProof(current.Proof, request.Current.Proof) {
			plan.Blockers = append(plan.Blockers, "exact-current-request-mismatch")
		}
		sameOperation := journal.Operation != nil && journal.Operation.OperationRef == request.OperationRef &&
			journal.Operation.TargetGenerationID == request.Target.Endpoint.EndpointGenerationID && !journal.Operation.Aborted
		for _, route := range journal.Routes {
			if route.Generation.State != codexgeneration.StateRetired && route.Generation.State != codexgeneration.StateCurrent && !sameOperation {
				plan.Blockers = append(plan.Blockers, "two-slot-pool-full:"+route.Generation.Endpoint.EndpointGenerationID)
			}
		}
		if journal.Operation != nil && !journal.Operation.Aborted && !sameOperation {
			plan.Blockers = append(plan.Blockers, "operation-already-active:"+journal.Operation.OperationRef)
		}
		if journal.Operation != nil && journal.Operation.Aborted && journal.Operation.OperationRef == request.OperationRef {
			plan.Blockers = append(plan.Blockers, "operation-ref-already-used:"+request.OperationRef)
		}
		if current.Proof != nil {
			if err := coordinator.runtime().Observe(ctx, current.Config, *current.Proof, current.TUIPath); err != nil {
				plan.Blockers = append(plan.Blockers, "stored-current-owner-readiness-unproven")
			}
		}
		ledger, ledgerErr := codexgeneration.ProjectDrainLedger(current.Generation.Endpoint.EndpointGenerationID, journal.Obligations)
		if ledgerErr == nil {
			plan.DrainLedger = ledger
		}
	}
	slices.Sort(plan.Blockers)
	if len(plan.Blockers) != 0 {
		plan.Decision = DecisionBlocked
	}
	return plan
}

func (coordinator *Coordinator) Apply(ctx context.Context, request Request) (Journal, error) {
	if coordinator.Journal == nil || coordinator.Registry == nil {
		return Journal{}, errors.New("rolling upgrade coordinator is not configured")
	}
	plan := coordinator.Plan(ctx, request)
	if plan.Decision != DecisionReady {
		return Journal{}, fmt.Errorf("rolling upgrade blocked: %s", strings.Join(plan.Blockers, ","))
	}
	if err := coordinator.fail(FailBeforePrewrite); err != nil {
		return Journal{}, err
	}
	_, err := coordinator.Journal.Update(ctx, func(journal *Journal, exists bool) error {
		if exists && journal.Operation != nil {
			if journal.Operation.Aborted && journal.Operation.OperationRef == request.OperationRef {
				return errors.New("rolling upgrade operation ref was already used")
			}
			if !journal.Operation.Aborted {
				if journal.Operation.OperationRef == request.OperationRef && journal.Operation.TargetGenerationID == request.Target.Endpoint.EndpointGenerationID {
					return nil
				}
				return errors.New("another rolling upgrade operation is active")
			}
		}
		if !exists {
			*journal = Journal{
				Version: JournalVersion, StateDomainID: request.Current.Generation.Endpoint.StateDomainID,
				CurrentGenerationID: request.Current.Generation.Endpoint.EndpointGenerationID,
				Routes:              []GenerationRoute{request.Current},
			}
		}
		qualification := request.Qualification
		journal.Qualification = &qualification
		liveRoutes := 0
		currentRoutes := 0
		for _, route := range journal.Routes {
			if route.Generation.State != codexgeneration.StateRetired {
				liveRoutes++
			}
			if route.Generation.State == codexgeneration.StateCurrent {
				currentRoutes++
			}
		}
		if liveRoutes != 1 || currentRoutes != 1 {
			return errors.New("bounded pool has no free Preparing slot")
		}
		op, opErr := codexgeneration.NewRollingUpgradeOperation(request.OperationRef, journal.StateDomainID,
			journal.CurrentGenerationID, request.Target.Endpoint.EndpointGenerationID)
		if opErr != nil {
			return opErr
		}
		journal.Operation = &op
		journal.Routes = append(journal.Routes, GenerationRoute{
			Generation: codexgeneration.Generation{Endpoint: request.Target.Endpoint, State: codexgeneration.StatePreparing,
				Owner: codexgeneration.OwnerProjmuxPrivate, BundleID: request.TargetBundleID},
			Config: request.Target, TUIPath: request.TargetTUIPath, LaunchOperationRef: request.OperationRef,
		})
		return nil
	})
	if err != nil {
		return Journal{}, err
	}
	if err := coordinator.fail(FailAfterPrewrite); err != nil {
		return Journal{}, err
	}
	return coordinator.Resume(ctx, request.OperationRef)
}

func (coordinator *Coordinator) Resume(ctx context.Context, operationRef string) (Journal, error) {
	if coordinator.Journal == nil || coordinator.Registry == nil {
		return Journal{}, errors.New("rolling upgrade coordinator is not configured")
	}
	for {
		journal, exists, err := coordinator.Journal.Load()
		if err != nil || !exists {
			return Journal{}, errors.Join(errors.New("rolling upgrade journal unavailable"), err)
		}
		if journal.Operation == nil || journal.Operation.OperationRef != operationRef {
			return Journal{}, errors.New("exact rolling upgrade operation not found")
		}
		switch journal.Operation.NextAction() {
		case codexgeneration.RollingActionPrepareCandidate:
			if err := coordinator.fail(FailBeforeCandidate); err != nil {
				return Journal{}, err
			}
			if _, err := coordinator.Journal.Update(ctx, func(current *Journal, exists bool) error {
				if !exists || current.Operation == nil || current.Operation.OperationRef != operationRef {
					return errors.New("candidate launch intent lost operation authority")
				}
				next, _, recordErr := current.Operation.RecordCandidateLaunchIntent()
				if recordErr != nil {
					return recordErr
				}
				current.Operation = &next
				return nil
			}); err != nil {
				return Journal{}, err
			}
			targetEndpoint := coremetadata.CodexEndpointRef{StateDomainID: journal.StateDomainID, EndpointGenerationID: journal.Operation.TargetGenerationID}
			target, ok := journal.Route(targetEndpoint)
			if !ok || target.Ready || target.Proof != nil {
				return Journal{}, errors.New("preparing target route mismatch")
			}
			err := coordinator.runtime().Prepare(ctx, target.Config, operationRef, func(start func() error) error {
				// The journal lock spans the last authority check and physical
				// supervisor start. Abort either wins first and makes this callback
				// refuse with zero starts, or waits and then observes the durable
				// supervisor intent during exact cleanup.
				_, fenceErr := coordinator.Journal.Update(ctx, func(latest *Journal, exists bool) error {
					if !exists || latest.Operation == nil || latest.Operation.OperationRef != operationRef ||
						latest.Operation.AbortIntended || latest.Operation.Aborted || latest.Operation.AdmissionCommitted ||
						!latest.Operation.CandidateLaunchIntended || latest.Operation.NextAction() != codexgeneration.RollingActionPrepareCandidate {
						return errors.New("candidate launch lost exact operation authority")
					}
					if _, ok := latest.Route(targetEndpoint); !ok {
						return errors.New("candidate launch route disappeared")
					}
					return start()
				})
				return fenceErr
			}, func() error {
				if _, err := coordinator.Journal.Update(ctx, func(current *Journal, exists bool) error {
					if !exists || current.Operation == nil || current.Operation.OperationRef != operationRef {
						return errors.New("candidate start receipt lost operation authority")
					}
					next, _, recordErr := current.Operation.RecordCandidateStart()
					if recordErr != nil {
						return recordErr
					}
					current.Operation = &next
					return nil
				}); err != nil {
					return err
				}
				return coordinator.fail(FailAfterCandidateLaunch)
			}, func(proof codexgenerationhost.LaunchProof) error {
				// Readiness is not enough to authorize admission: the exact TUI
				// executable must still be the single verified RoleTUI artifact.
				if err := coordinator.runtime().Observe(ctx, target.Config, proof, target.TUIPath); err != nil {
					return fmt.Errorf("candidate publication observe failed: refusal=%s proof-axis=%s: %w",
						codexgenerationhost.HostRefusalOf(err), codexgenerationhost.HostProofAxisOf(err), err)
				}
				_, publishErr := coordinator.Journal.Update(ctx, func(current *Journal, exists bool) error {
					if !exists || current.Operation == nil || current.Operation.OperationRef != operationRef ||
						current.Operation.NextAction() != codexgeneration.RollingActionPrepareCandidate {
						return errors.New("candidate publication lost operation authority")
					}
					for i := range current.Routes {
						if current.Routes[i].Generation.Endpoint.Same(targetEndpoint) {
							current.Routes[i].Proof = &proof
							current.Routes[i].Ready = true
							next, _, recordErr := current.Operation.RecordAction(codexgeneration.RollingActionPrepareCandidate, nil)
							if recordErr != nil {
								return recordErr
							}
							current.Operation = &next
							return nil
						}
					}
					return errors.New("candidate route disappeared")
				})
				return publishErr
			})
			if err != nil {
				return Journal{}, err
			}
			if err := coordinator.fail(FailAfterCandidate); err != nil {
				return Journal{}, err
			}
		case codexgeneration.RollingActionCommitAdmission:
			if err := coordinator.fail(FailBeforeAdmission); err != nil {
				return Journal{}, err
			}
			err := coordinator.Registry.WithAdmissionBarrier(func(coremetadata.Registry) error {
				targetEndpoint := coremetadata.CodexEndpointRef{StateDomainID: journal.StateDomainID, EndpointGenerationID: journal.Operation.TargetGenerationID}
				target, ok := journal.Route(targetEndpoint)
				if !ok || target.Proof == nil || !target.Ready {
					return errors.New("admission candidate route is not ready")
				}
				if err := coordinator.runtime().Observe(ctx, target.Config, *target.Proof, target.TUIPath); err != nil {
					return fmt.Errorf("admission candidate observe failed: refusal=%s proof-axis=%s: %w",
						codexgenerationhost.HostRefusalOf(err), codexgenerationhost.HostProofAxisOf(err), err)
				}
				_, updateErr := coordinator.Journal.Update(ctx, func(current *Journal, exists bool) error {
					if !exists || current.Operation == nil || current.Operation.OperationRef != operationRef {
						return errors.New("admission commit lost operation authority")
					}
					if current.Operation.AdmissionCommitted {
						return nil
					}
					if current.Operation.NextAction() != codexgeneration.RollingActionCommitAdmission {
						return errors.New("admission commit is out of order")
					}
					for i := range current.Routes {
						switch current.Routes[i].Generation.Endpoint.EndpointGenerationID {
						case current.Operation.OldGenerationID:
							current.Routes[i].Generation.State = codexgeneration.StateDraining
						case current.Operation.TargetGenerationID:
							if !current.Routes[i].Ready {
								return errors.New("candidate is not ready")
							}
							current.Routes[i].Generation.State = codexgeneration.StateCurrent
						}
					}
					current.CurrentGenerationID = current.Operation.TargetGenerationID
					next, _, recordErr := current.Operation.RecordAction(codexgeneration.RollingActionCommitAdmission, nil)
					if recordErr != nil {
						return recordErr
					}
					current.Operation = &next
					return nil
				})
				return updateErr
			})
			if err != nil {
				return Journal{}, err
			}
			if err := coordinator.fail(FailAfterAdmission); err != nil {
				return Journal{}, err
			}
		case codexgeneration.RollingActionPublishDrain:
			if err := coordinator.fail(FailBeforeDrain); err != nil {
				return Journal{}, err
			}
			operation := *journal.Operation
			endpoint := coremetadata.CodexEndpointRef{StateDomainID: operation.StateDomainID, EndpointGenerationID: operation.OldGenerationID}
			committed, _, updateErr := coordinator.Registry.UpdateConvergent(func(registry *coremetadata.Registry) error {
				return setGenerationLifecycle(registry, coordinator.mutator(), endpoint, operation.OperationRef, coremetadata.CodexGenerationDraining)
			})
			if updateErr != nil {
				return Journal{}, updateErr
			}
			if err := coordinator.fail(FailAfterDrainRegistry); err != nil {
				return Journal{}, err
			}
			obligations := projectObligations(committed)
			ledger, err := codexgeneration.ProjectDrainLedger(operation.OldGenerationID, obligations)
			if err != nil {
				return Journal{}, err
			}
			_, err = coordinator.Journal.Update(ctx, func(current *Journal, exists bool) error {
				if !exists || current.Operation == nil || current.Operation.OperationRef != operationRef {
					return errors.New("drain publication lost operation authority")
				}
				current.Obligations = obligations
				next, _, recordErr := current.Operation.RecordAction(codexgeneration.RollingActionPublishDrain, ledger)
				if recordErr != nil {
					return recordErr
				}
				current.Operation = &next
				return nil
			})
			if err != nil {
				return Journal{}, err
			}
			if err := coordinator.fail(FailAfterDrainReceipt); err != nil {
				return Journal{}, err
			}
		case codexgeneration.RollingActionNone:
			return journal, nil
		default:
			return Journal{}, errors.New("unknown rolling action")
		}
	}
}

func (coordinator *Coordinator) Abort(ctx context.Context, operationRef string) (Journal, error) {
	if coordinator.Journal == nil {
		return Journal{}, errors.New("rolling journal is not configured")
	}
	// Persist the abort fence before any lifecycle effect. Admission and this
	// receipt serialize on the same journal lock: admission wins => no cleanup;
	// abort wins => Resume observes NextActionNone and cannot make the candidate
	// Current while cleanup is in flight.
	journal, err := coordinator.Journal.Update(ctx, func(current *Journal, exists bool) error {
		if !exists || current.Operation == nil || current.Operation.OperationRef != operationRef {
			return errors.New("exact rolling operation not found")
		}
		next, _, requestErr := current.Operation.RequestAbort()
		if requestErr != nil {
			return requestErr
		}
		current.Operation = &next
		return nil
	})
	if err != nil {
		return Journal{}, err
	}
	targetEndpoint := coremetadata.CodexEndpointRef{StateDomainID: journal.StateDomainID, EndpointGenerationID: journal.Operation.TargetGenerationID}
	target, targetExists := journal.Route(targetEndpoint)
	recoveredStart := false
	if targetExists {
		// Cleanup also handles the durable launch-intent window before a proof is
		// published. A missing intent is a no-op; an in-flight owned supervisor is
		// stopped through its inherited guard, never through process discovery.
		var cleanupErr error
		recoveredStart, cleanupErr = coordinator.runtime().Cleanup(ctx, target.Config, operationRef, target.Proof)
		if cleanupErr != nil {
			return Journal{}, cleanupErr
		}
	}
	return coordinator.Journal.Update(ctx, func(current *Journal, exists bool) error {
		if !exists || current.Operation == nil || current.Operation.OperationRef != operationRef {
			return errors.New("exact rolling operation not found")
		}
		var next codexgeneration.RollingUpgradeOperation
		var changed bool
		var err error
		if current.Operation.CandidateStarted {
			next, changed, err = current.Operation.AbortPreparedCandidate()
		} else if recoveredStart {
			next, changed, err = current.Operation.AbortRecoveredCandidate()
		} else {
			next, changed, err = current.Operation.Abort()
		}
		if err != nil {
			return err
		}
		current.Operation = &next
		if changed {
			current.Routes = slices.DeleteFunc(current.Routes, func(route GenerationRoute) bool {
				return route.Generation.Endpoint.EndpointGenerationID == next.TargetGenerationID
			})
		}
		return nil
	})
}

func (coordinator *Coordinator) RequestHandover(ctx context.Context, endpoint coremetadata.CodexEndpointRef) (string, bool, error) {
	if coordinator.Journal == nil || coordinator.Registry == nil || !endpoint.Valid() {
		return "", false, errors.New("handover requester is not configured")
	}
	journal, exists, err := coordinator.Journal.Load()
	if err != nil || !exists || journal.Operation == nil || journal.Operation.OldGenerationID != endpoint.EndpointGenerationID || journal.StateDomainID != endpoint.StateDomainID {
		return "", false, errors.Join(errors.New("exact Draining generation operation not found"), err)
	}
	oldRoute, oldExists := journal.Route(endpoint)
	if !oldExists || !journal.Operation.DrainPublished || journal.Operation.Aborted ||
		(oldRoute.Generation.State != codexgeneration.StateDraining && oldRoute.Generation.State != codexgeneration.StateHandoverPending) {
		return "", false, errors.New("exact generation is not in the Draining handover window")
	}
	operationRef := journal.Operation.OperationRef
	committed, _, err := coordinator.Registry.UpdateConvergent(func(registry *coremetadata.Registry) error {
		return setGenerationLifecycle(registry, coordinator.mutator(), endpoint, operationRef, coremetadata.CodexGenerationHandoverPending)
	})
	if err != nil {
		return "", false, err
	}
	obligations := projectObligations(committed)
	created := false
	_, err = coordinator.Journal.Update(ctx, func(current *Journal, exists bool) error {
		if !exists || current.Operation == nil || current.Operation.OperationRef != operationRef {
			return errors.New("handover request lost operation authority")
		}
		next, changed, requestErr := current.Operation.RequestGenerationHandover()
		if requestErr != nil {
			return requestErr
		}
		current.Operation = &next
		created = changed
		current.Obligations = obligations
		for i := range current.Routes {
			if current.Routes[i].Generation.Endpoint.Same(endpoint) {
				current.Routes[i].Generation.State = codexgeneration.StateHandoverPending
			}
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return operationRef, created, nil
}

func setGenerationLifecycle(registry *coremetadata.Registry, mutator coremetadata.Mutator, endpoint coremetadata.CodexEndpointRef, operationRef string, state coremetadata.CodexGenerationState) error {
	for i := range registry.Agents {
		agent := registry.Agents[i]
		ref := agent.Status.SessionRef
		if ref == nil || ref.Provider != "codex" || ref.Codex == nil || ref.Codex.Endpoint == nil || !ref.Codex.Endpoint.Same(endpoint) {
			continue
		}
		lifecycle := coremetadata.CodexGenerationLifecycleRef{State: state, Operation: &coremetadata.CodexGenerationOperationRef{ID: operationRef, Endpoint: endpoint}}
		if _, _, err := mutator.SetCodexGenerationLifecycle(registry, agent.Metadata.UID, endpoint, lifecycle); err != nil {
			return err
		}
	}
	return nil
}

func projectObligations(registry coremetadata.Registry) []codexgeneration.AgentObligation {
	obligations := make([]codexgeneration.AgentObligation, 0, len(registry.Agents))
	for i := range registry.Agents {
		if obligation, ok := codexgeneration.ProjectAgentObligation(registry.Agents[i], false); ok {
			obligations = append(obligations, obligation)
		}
	}
	slices.SortFunc(obligations, func(a, b codexgeneration.AgentObligation) int { return strings.Compare(a.AgentUID, b.AgentUID) })
	return obligations
}
