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
	op, err := codexgeneration.NewRollingUpgradeOperation(request.OperationRef, request.OldEndpoint.StateDomainID,
		request.OldEndpoint.EndpointGenerationID, request.Target.Endpoint.EndpointGenerationID)
	if err != nil {
		return Journal{}, err
	}
	_, err = coordinator.Journal.Update(ctx, func(journal *Journal, exists bool) error {
		if exists {
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
		*journal = Journal{
			Version: JournalVersion, StateDomainID: request.OldEndpoint.StateDomainID,
			CurrentGenerationID: request.OldEndpoint.EndpointGenerationID,
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
