package codexgeneration

import (
	"testing"

	"github.com/crevissepartners/projmux/internal/core/metadata"
)

func TestDerivedConsumersUseOneEndpointFenceAndStaleActionsAreZero(t *testing.T) {
	endpoint := metadata.CodexEndpointRef{StateDomainID: "state-main", EndpointGenerationID: "generation-n"}
	authority := metadata.CodexAuthorityRef{
		StateDomainID: endpoint.StateDomainID, EndpointGenerationID: endpoint.EndpointGenerationID,
		BrokerRuntimeID: "broker-n", ConnectionEpoch: 7, BindingEpoch: 11,
	}
	operation := metadata.CodexGenerationOperationRef{
		ID: "operation-drain", Endpoint: endpoint,
	}
	for _, state := range []GenerationState{StateCurrent, StateDraining} {
		t.Run(string(state), func(t *testing.T) {
			input := LifecycleProjectionInput{Interaction: metadata.InteractionApprovalRequired, Endpoint: &endpoint, GenerationState: state}
			if state == StateDraining {
				input.Operation = &operation
			}
			base := RuntimeMutationInput{DurableEndpoint: &endpoint, StoredAuthority: &authority, PresentedAuthority: &authority, TargetRuntimeID: "%7", EventRuntimeID: "%7"}
			got := ProjectConsumers(input, base, true)
			if got.Effect != MutationSemanticEffect || !got.Notification || !got.Sidebar || !got.Statusbar || !got.Reply {
				t.Fatalf("exact consumers = %#v", got)
			}
			if got.Fence == "" || got.Fence != ConsumerFence(got.Endpoint, authority) {
				t.Fatalf("consumer fence = %q", got.Fence)
			}

			stale := authority
			stale.ConnectionEpoch++
			base.PresentedAuthority = &stale
			got = ProjectConsumers(input, base, true)
			if got.Effect != MutationZeroWrite || got.Notification || got.Sidebar || got.Statusbar || got.Reply || got.Fence != "" {
				t.Fatalf("stale consumers = %#v, want zero actions", got)
			}
		})
	}
}

func TestDerivedConsumersPolicyAndInvalidAdmissionTuple(t *testing.T) {
	endpoint := metadata.CodexEndpointRef{StateDomainID: "state-main", EndpointGenerationID: "generation-n1"}
	authority := metadata.CodexAuthorityRef{StateDomainID: endpoint.StateDomainID, EndpointGenerationID: endpoint.EndpointGenerationID, BrokerRuntimeID: "broker-n1", ConnectionEpoch: 1, BindingEpoch: 2}
	mutation := RuntimeMutationInput{DurableEndpoint: &endpoint, StoredAuthority: &authority, PresentedAuthority: &authority, TargetRuntimeID: "%8", EventRuntimeID: "%8"}

	quiet := ProjectConsumers(LifecycleProjectionInput{Interaction: metadata.InteractionResponseComplete, Endpoint: &endpoint, GenerationState: StateCurrent}, mutation, false)
	if quiet.Notification || !quiet.Sidebar || !quiet.Statusbar || quiet.Reply {
		t.Fatalf("quiet/state-only consumer semantics = %#v", quiet)
	}

	invalid := ProjectConsumers(LifecycleProjectionInput{Interaction: metadata.InteractionResponseComplete, Endpoint: &endpoint, GenerationState: StateDraining}, mutation, true)
	if invalid.Effect != MutationZeroWrite || invalid.Notification || invalid.Sidebar || invalid.Statusbar || invalid.Reply {
		t.Fatalf("invalid planned tuple = %#v, want fail-closed", invalid)
	}
}
