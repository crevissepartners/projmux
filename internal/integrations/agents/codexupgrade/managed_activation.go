package codexupgrade

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/crevissepartners/projmux/internal/core/codexgeneration"
	coremetadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/agents/codexgenerationhost"
)

var managedActivationVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]{1,16})?$`)

// ManagedCurrentActivation is the content-free bridge from an already-running
// external/default generation to one Projmux-owned private admission current.
// The old route carries no lifecycle proof on purpose: its initial read-only
// default probe establishes the version/owner identity, but the journal never
// gains authority to stop, restart, kill, or adopt it.
type ManagedCurrentActivation struct {
	OperationRef   string
	OldEndpoint    coremetadata.CodexEndpointRef
	OldOwner       codexgeneration.OwnerClass
	OldVersion     string
	Target         GenerationConfig
	TargetBundleID string
	TargetTUIPath  string
	TargetVersion  string
	// Qualification is the measured receipt for exactly this version pair.
	//
	// It is a value, not a pointer: this route has no "unqualified" spelling.
	// A caller that has no receipt cannot express the absence here, it can only
	// hand over a zero value, which fails the gate with the same token as a
	// refused one. That is the point -- the earlier defect was that the request
	// had no place to carry a verdict at all, so the entry path could not have
	// consulted one even if the operator had produced it.
	Qualification codexgeneration.QualificationResult
}

// qualify is the L3 gate on this entry path.
//
// Every token is specific to managed-current activation. The handover route
// carries its own set, because the two paths are entered by different
// operators for different reasons and a shared token would tell a reader which
// property failed while hiding which door it failed at.
func (request ManagedCurrentActivation) qualify() error {
	if err := request.Qualification.Validate(); err != nil {
		return errors.New("managed-current-activation-qualification-invalid")
	}
	// The pair binding is checked before the verdict so a receipt for another
	// pair is never reported as a refused qualification of this one. They are
	// different operator mistakes with different fixes.
	if request.Qualification.Versions.Old != request.OldVersion ||
		request.Qualification.Versions.New != request.TargetVersion {
		return errors.New("managed-current-activation-qualification-version-pair-mismatch")
	}
	gate := codexgeneration.GateQualification(request.Qualification)
	if gate.EvidenceForged {
		return fmt.Errorf("managed-current-activation-qualification-evidence-forged: %s", gate.Discriminant)
	}
	if !gate.Phase2Ready {
		return errors.New("managed-current-activation-version-pair-not-qualified")
	}
	return nil
}

func (request ManagedCurrentActivation) validate() error {
	if request.OldOwner != codexgeneration.OwnerUnmanaged && request.OldOwner != codexgeneration.OwnerOfficialManaged {
		return errors.New("external-current-owner-unavailable")
	}
	if !request.OldEndpoint.Valid() || !request.Target.Endpoint.Valid() ||
		request.OldEndpoint.StateDomainID != request.Target.Endpoint.StateDomainID ||
		request.OldEndpoint.EndpointGenerationID == request.Target.Endpoint.EndpointGenerationID ||
		!managedActivationVersionPattern.MatchString(request.OldVersion) ||
		!managedActivationVersionPattern.MatchString(request.TargetVersion) || request.OldVersion == request.TargetVersion ||
		request.OldEndpoint.EndpointGenerationID != "codex-"+request.OldVersion ||
		request.Target.Endpoint.EndpointGenerationID != "codex-"+request.TargetVersion ||
		!request.Target.valid() || strings.TrimSpace(request.TargetBundleID) == "" ||
		request.TargetBundleID != strings.TrimSpace(request.TargetBundleID) || !filepathAbsoluteClean(request.TargetTUIPath) {
		return errors.New("managed-current-activation-request-invalid")
	}
	if pathsOverlap(request.Target.StateDomainPath, request.Target.PrivateRoot) ||
		pathsOverlap(request.Target.StateDomainPath, request.Target.LeaseRoot) ||
		pathsOverlap(request.Target.PrivateRoot, request.Target.LeaseRoot) {
		return errors.New("managed-current-activation-roots-overlap")
	}
	if _, err := codexgeneration.NewRollingUpgradeOperation(request.OperationRef, request.OldEndpoint.StateDomainID,
		request.OldEndpoint.EndpointGenerationID, request.Target.Endpoint.EndpointGenerationID); err != nil {
		return errors.New("managed-current-activation-operation-invalid")
	}
	identity, err := codexgenerationhost.VerifyPrivateGenerationBundle(request.Target.hostConfig())
	if err != nil || identity.ID != request.TargetBundleID || identity.TUIPath != request.TargetTUIPath || identity.Version != request.TargetVersion {
		return errors.New("managed-current-activation-bundle-unverified")
	}
	return nil
}

// ActivateManagedCurrent prepares and selects one private generation without
// issuing any lifecycle action against the external/default route. It reuses
// the Phase 4 journal/lease/admission/drain machinery, including its durable
// launch intent and Registry admission barrier, instead of publishing a second
// activation protocol.
func (coordinator *Coordinator) ActivateManagedCurrent(ctx context.Context, request ManagedCurrentActivation) (Journal, error) {
	if coordinator == nil || coordinator.Journal == nil || coordinator.Registry == nil {
		return Journal{}, errors.New("managed-current-activation-coordinator-unavailable")
	}
	if err := request.validate(); err != nil {
		return Journal{}, err
	}
	// The gate sits ahead of the prewrite, which is the only ordering that
	// satisfies the guarantee. The prewrite is what puts the target route in
	// the pool, and the Resume below is what publishes the drain; a gate placed
	// after either one would refuse a generation the journal already committed
	// to, and this route has no way back out of that.
	if err := request.qualify(); err != nil {
		return Journal{}, err
	}
	op, err := codexgeneration.NewRollingUpgradeOperation(request.OperationRef, request.OldEndpoint.StateDomainID,
		request.OldEndpoint.EndpointGenerationID, request.Target.Endpoint.EndpointGenerationID)
	if err != nil {
		return Journal{}, err
	}
	_, err = coordinator.Journal.Update(ctx, func(journal *Journal, exists bool) error {
		if exists {
			// A pool written before this gate shipped carries no receipt. Its
			// re-entry is refused rather than adopted: the journal is the only
			// record of what authorized that pool, and an absent receipt there
			// means nothing authorized it.
			if journal.Qualification == nil || !codexgeneration.GateQualification(*journal.Qualification).Phase2Ready {
				return errors.New("managed-current-activation-existing-pool-not-qualified")
			}
			current, currentOK := journal.CurrentRoute()
			if currentOK && managedActivationCurrentMatches(current, request) {
				return nil
			}
			if currentOK && current.Generation.Endpoint.Same(request.Target.Endpoint) {
				return errors.New("managed-current-activation-existing-pool-requires-operator-inspection")
			}
			if journal.Operation == nil || journal.Operation.OperationRef != request.OperationRef ||
				journal.Operation.OldGenerationID != request.OldEndpoint.EndpointGenerationID ||
				journal.Operation.TargetGenerationID != request.Target.Endpoint.EndpointGenerationID {
				return errors.New("managed-current-activation-existing-pool-requires-operator-inspection")
			}
			return nil
		}
		// The receipt is written with the routes it authorized, in the same
		// update. That is what makes the L3 diagnosis readable afterwards: the
		// pool row reports the qualification it is standing on rather than a
		// verdict that lived only in the caller's memory.
		qualification := request.Qualification
		*journal = Journal{
			Version: JournalVersion, StateDomainID: request.OldEndpoint.StateDomainID,
			CurrentGenerationID: request.OldEndpoint.EndpointGenerationID,
			Qualification:       &qualification,
			Routes: []GenerationRoute{
				{
					Generation: codexgeneration.Generation{
						Endpoint: request.OldEndpoint, State: codexgeneration.StateCurrent,
						Owner: request.OldOwner, BundleID: "external-" + request.OldVersion,
					},
					Version: request.OldVersion,
				},
				{
					Generation: codexgeneration.Generation{
						Endpoint: request.Target.Endpoint, State: codexgeneration.StatePreparing,
						Owner: codexgeneration.OwnerProjmuxPrivate, BundleID: request.TargetBundleID,
					},
					Version: request.TargetVersion, Config: request.Target, TUIPath: request.TargetTUIPath,
					LaunchOperationRef: request.OperationRef,
				},
			},
			Operation: &op,
		}
		return nil
	})
	if err != nil {
		return Journal{}, fmt.Errorf("managed current activation prewrite: %w", err)
	}
	journal, err := coordinator.Resume(ctx, request.OperationRef)
	if err != nil {
		return Journal{}, fmt.Errorf("managed current activation resume: %w", err)
	}
	current, ok := journal.CurrentRoute()
	if !ok || !managedActivationCurrentMatches(current, request) || journal.Operation == nil ||
		journal.Operation.OperationRef != request.OperationRef || journal.Operation.OldGenerationID != request.OldEndpoint.EndpointGenerationID ||
		journal.Operation.TargetGenerationID != request.Target.Endpoint.EndpointGenerationID ||
		!journal.Operation.AdmissionCommitted || !journal.Operation.DrainPublished ||
		journal.Operation.Mutations.OldEndpointStop != 0 || journal.Operation.Mutations.ForeignAdoption != 0 {
		return Journal{}, errors.New("managed-current-activation-did-not-converge")
	}
	return journal, nil
}

func managedActivationCurrentMatches(current GenerationRoute, request ManagedCurrentActivation) bool {
	return current.Generation.Endpoint.Same(request.Target.Endpoint) &&
		current.Generation.Owner == codexgeneration.OwnerProjmuxPrivate &&
		current.Generation.BundleID == request.TargetBundleID && current.Version == request.TargetVersion &&
		current.Config == request.Target && current.TUIPath == request.TargetTUIPath &&
		current.LaunchOperationRef == request.OperationRef && current.Ready && current.Proof != nil
}
