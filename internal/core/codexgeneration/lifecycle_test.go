package codexgeneration

import (
	"reflect"
	"strings"
	"testing"

	"github.com/crevissepartners/projmux/internal/core/metadata"
)

func lifecycleEndpoint(generation string) *metadata.CodexEndpointRef {
	return &metadata.CodexEndpointRef{StateDomainID: "state-domain", EndpointGenerationID: generation}
}

func lifecycleOperation(endpoint *metadata.CodexEndpointRef) *LifecycleOperationRef {
	return &LifecycleOperationRef{ID: "operation-1", Endpoint: *endpoint}
}

func baseLifecycleProjection(kind metadata.AgentInteractionKind) LifecycleProjection {
	switch kind {
	case metadata.InteractionIdle:
		return LifecycleProjection{State: LifecycleStateIdle}
	case metadata.InteractionInProgress:
		return LifecycleProjection{State: LifecycleStateThinking, Badge: LifecycleBadgeInProgress, Attention: LifecycleAttentionBusy}
	case metadata.InteractionApprovalRequired:
		return LifecycleProjection{State: LifecycleStateWaiting, Badge: LifecycleBadgeApprovalRequired, Attention: LifecycleAttentionReply}
	case metadata.InteractionInputRequired:
		return LifecycleProjection{State: LifecycleStateWaiting, Badge: LifecycleBadgeInputRequired, Attention: LifecycleAttentionReply}
	case metadata.InteractionResponseComplete:
		return LifecycleProjection{State: LifecycleStateWaiting, Badge: LifecycleBadgeResponseComplete, Attention: LifecycleAttentionReply}
	default:
		return LifecycleProjection{}
	}
}

func expectedLifecycleProjection(kind metadata.AgentInteractionKind, state GenerationState) LifecycleProjection {
	base := baseLifecycleProjection(kind)
	switch state {
	case StateCurrent:
		return base
	case StateDraining:
		if base.Badge == "" {
			return LifecycleProjection{State: LifecycleStateDraining, Badge: LifecycleBadgeDraining}
		}
		base.State = LifecycleStateDraining
		return base
	case StateHandoverPending:
		return LifecycleProjection{State: LifecycleStateDraining, Badge: LifecycleBadgeHandoverPending}
	case StateRecovering:
		return LifecycleProjection{State: LifecycleStateRecovering, Badge: LifecycleBadgeRecovering}
	case StateBlocked:
		return LifecycleProjection{State: LifecycleStateBlocked, Badge: LifecycleBadgeBlocked}
	default:
		return LifecycleProjection{}
	}
}

func isMaintenanceBadge(badge string) bool {
	return badge == LifecycleBadgeDraining || badge == LifecycleBadgeHandoverPending ||
		badge == LifecycleBadgeRecovering || badge == LifecycleBadgeBlocked
}

func TestGenerationLifecycleProjectionClosedTable(t *testing.T) {
	interactions := metadata.AgentInteractionKinds()
	states := GenerationStates()
	if len(interactions) != 6 || len(states) != 7 {
		t.Fatalf("closed vocabularies changed: interactions=%v states=%v", interactions, states)
	}
	seen := map[[2]string]LifecycleProjection{}
	for _, interaction := range interactions {
		for _, state := range states {
			name := string(interaction) + "/" + string(state)
			t.Run(name, func(t *testing.T) {
				endpoint := lifecycleEndpoint("generation-1")
				input := LifecycleProjectionInput{
					Interaction: interaction, Endpoint: endpoint, GenerationState: state,
				}
				if state == StateDraining || state == StateHandoverPending || state == StateRecovering || state == StateBlocked {
					input.Operation = lifecycleOperation(endpoint)
				}
				got := ProjectLifecycle(input)
				want := expectedLifecycleProjection(interaction, state)
				if got != want {
					t.Fatalf("projection = %#v, want %#v", got, want)
				}
				key := [2]string{string(interaction), string(state)}
				if prior, exists := seen[key]; exists {
					t.Fatalf("meaning %v projected twice: %#v and %#v", key, prior, got)
				}
				seen[key] = got
				if repeat := ProjectLifecycle(input); repeat != got {
					t.Fatalf("repeat projection = %#v, want %#v", repeat, got)
				}
			})
		}
	}
	if got, want := len(seen), len(interactions)*len(states); got != want {
		t.Fatalf("generated table covered %d meanings, want %d", got, want)
	}
}

func FuzzGenerationLifecycleProjectionMatchesClosedTable(f *testing.F) {
	f.Add(uint8(0), uint8(0))
	f.Add(uint8(5), uint8(6))
	interactions := metadata.AgentInteractionKinds()
	states := GenerationStates()
	f.Fuzz(func(t *testing.T, interactionIndex, stateIndex uint8) {
		interaction := interactions[int(interactionIndex)%len(interactions)]
		state := states[int(stateIndex)%len(states)]
		endpoint := lifecycleEndpoint("generation-property")
		input := LifecycleProjectionInput{Interaction: interaction, Endpoint: endpoint, GenerationState: state}
		if state == StateDraining || state == StateHandoverPending || state == StateRecovering || state == StateBlocked {
			input.Operation = lifecycleOperation(endpoint)
		}
		if got, want := ProjectLifecycle(input), expectedLifecycleProjection(interaction, state); got != want {
			t.Fatalf("interaction=%s state=%s got=%#v want=%#v", interaction, state, got, want)
		}
	})
}

func TestPlannedGenerationProjectionRequiresExactDurableOperationRef(t *testing.T) {
	endpoint := lifecycleEndpoint("generation-owner")
	for _, state := range []GenerationState{StateDraining, StateHandoverPending, StateRecovering, StateBlocked} {
		for _, operation := range []*LifecycleOperationRef{
			nil,
			{ID: "", Endpoint: *endpoint},
			{ID: "operation-foreign", Endpoint: *lifecycleEndpoint("generation-foreign")},
		} {
			got := ProjectLifecycle(LifecycleProjectionInput{
				Interaction: metadata.InteractionResponseComplete,
				Endpoint:    endpoint, GenerationState: state, Operation: operation,
			})
			if got != (LifecycleProjection{}) || isMaintenanceBadge(got.Badge) {
				t.Fatalf("state=%s operation=%#v projected inferred maintenance %#v", state, operation, got)
			}
		}
	}
}

func TestMarkerlessCrashAndVersionDriftRemainOrdinaryFailure(t *testing.T) {
	// Exit status and executable version are deliberately absent from
	// LifecycleProjectionInput. Without an explicit operation ref, an ordinary
	// failed turn is idle and cannot acquire a maintenance badge.
	got := ProjectLifecycle(LifecycleProjectionInput{
		Interaction:     metadata.InteractionIdle,
		Endpoint:        lifecycleEndpoint("generation-current"),
		GenerationState: StateCurrent,
	})
	if got != (LifecycleProjection{State: LifecycleStateIdle}) || isMaintenanceBadge(got.Badge) {
		t.Fatalf("markerless crash/version drift projected maintenance: %#v", got)
	}
}

func TestLifecycleProjectionAuthorityInputsExcludeProcessHeuristics(t *testing.T) {
	typeOf := reflect.TypeFor[LifecycleProjectionInput]()
	if typeOf.NumField() != 4 {
		t.Fatalf("authority input fields=%d, want exact interaction/endpoint/generation/operation tuple", typeOf.NumField())
	}
	for index := 0; index < typeOf.NumField(); index++ {
		name := strings.ToLower(typeOf.Field(index).Name)
		for _, forbidden := range []string{"exit", "version", "executable", "path", "socket", "pid"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("authority input acquired process heuristic %q", typeOf.Field(index).Name)
			}
		}
	}
}

func TestRuntimeMutationEquivalenceTableIsClosed(t *testing.T) {
	rows := RuntimeMutationClasses()
	if len(rows) != 8 {
		t.Fatalf("equivalence rows=%d, want 8", len(rows))
	}
	seen := map[[3]string]MutationEffect{}
	allowed := 0
	for _, row := range rows {
		key := [3]string{string(row.Owner), string(row.Freshness), string(row.Target)}
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate equivalence class %v", key)
		}
		seen[key] = row.Effect
		if row.Effect == MutationSemanticEffect {
			allowed++
			if row.Owner != MutationOwnerExact || row.Freshness != MutationCurrent || row.Target != MutationTargetExact {
				t.Fatalf("non-exact class allowed: %#v", row)
			}
		}
	}
	if allowed != 1 {
		t.Fatalf("allowed equivalence classes=%d, want 1", allowed)
	}
}

func TestRuntimeMutationCompositeFenceAndSiblingRecorder(t *testing.T) {
	durable := lifecycleEndpoint("generation-owner")
	stored := &metadata.CodexAuthorityRef{
		StateDomainID: durable.StateDomainID, EndpointGenerationID: durable.EndpointGenerationID,
		BrokerRuntimeID: "broker-runtime-owner", ConnectionEpoch: 1, BindingEpoch: 1,
	}
	tests := []struct {
		name         string
		presented    *metadata.CodexAuthorityRef
		eventRuntime string
		want         RuntimeMutationClass
		authority    AuthorityDecision
		writes       int
	}{
		{
			name: "owner current target", presented: stored, eventRuntime: "runtime-target",
			want:      RuntimeMutationClass{Owner: MutationOwnerExact, Freshness: MutationCurrent, Target: MutationTargetExact, Effect: MutationSemanticEffect},
			authority: AuthorityAllowed, writes: 1,
		},
		{
			name: "owner current sibling", presented: stored, eventRuntime: "runtime-sibling",
			want:      RuntimeMutationClass{Owner: MutationOwnerExact, Freshness: MutationCurrent, Target: MutationTargetSibling, Effect: MutationZeroWrite},
			authority: AuthorityAllowed,
		},
		{
			name:         "same number foreign generation",
			presented:    &metadata.CodexAuthorityRef{StateDomainID: "state-domain", EndpointGenerationID: "generation-foreign", BrokerRuntimeID: "broker-runtime-foreign", ConnectionEpoch: 1, BindingEpoch: 1},
			eventRuntime: "runtime-target",
			want:         RuntimeMutationClass{Owner: MutationOwnerForeign, Freshness: MutationStale, Target: MutationTargetExact, Effect: MutationZeroWrite},
			authority:    AuthorityEndpointMismatch,
		},
		{
			name:         "broker restart reused epochs",
			presented:    &metadata.CodexAuthorityRef{StateDomainID: "state-domain", EndpointGenerationID: "generation-owner", BrokerRuntimeID: "broker-runtime-restarted", ConnectionEpoch: 1, BindingEpoch: 1},
			eventRuntime: "runtime-target",
			want:         RuntimeMutationClass{Owner: MutationOwnerExact, Freshness: MutationStale, Target: MutationTargetExact, Effect: MutationZeroWrite},
			authority:    AuthorityBrokerRuntimeMismatch,
		},
		{
			name:         "connection stale",
			presented:    &metadata.CodexAuthorityRef{StateDomainID: "state-domain", EndpointGenerationID: "generation-owner", BrokerRuntimeID: "broker-runtime-owner", ConnectionEpoch: 2, BindingEpoch: 1},
			eventRuntime: "runtime-target",
			want:         RuntimeMutationClass{Owner: MutationOwnerExact, Freshness: MutationStale, Target: MutationTargetExact, Effect: MutationZeroWrite},
			authority:    AuthorityConnectionStale,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writes := struct{ registry, tmux int }{}
			got := ApplyRuntimeMutation(RuntimeMutationInput{
				DurableEndpoint: durable, StoredAuthority: stored, PresentedAuthority: test.presented,
				TargetRuntimeID: "runtime-target", EventRuntimeID: test.eventRuntime,
			}, func() { writes.registry++ }, func() { writes.tmux++ })
			if got.Class != test.want || got.Authority != test.authority || writes.registry != test.writes || writes.tmux != test.writes {
				t.Fatalf("decision=%#v writes=%+v, want %#v authority=%s writes=%d", got, writes, test.want, test.authority, test.writes)
			}
		})
	}
}

func TestRuntimeMutationClassesMatchDecisionKernel(t *testing.T) {
	durable := lifecycleEndpoint("generation-owner")
	current := &metadata.CodexAuthorityRef{
		StateDomainID: "state-domain", EndpointGenerationID: "generation-owner",
		BrokerRuntimeID: "runtime", ConnectionEpoch: 4, BindingEpoch: 7,
	}
	for _, row := range RuntimeMutationClasses() {
		presented := *current
		if row.Owner == MutationOwnerForeign {
			presented.EndpointGenerationID = "generation-foreign"
		}
		stored := *current
		if row.Freshness == MutationStale {
			stored.ConnectionEpoch++
		}
		eventRuntime := "runtime-target"
		if row.Target == MutationTargetSibling {
			eventRuntime = "runtime-sibling"
		}
		decision := DecideRuntimeMutation(RuntimeMutationInput{
			DurableEndpoint: durable, StoredAuthority: &stored, PresentedAuthority: &presented,
			TargetRuntimeID: "runtime-target", EventRuntimeID: eventRuntime,
		})
		// A foreign presented endpoint can still be current relative to its own
		// stored binding. Build that exact fixture for the foreign/current rows.
		if row.Owner == MutationOwnerForeign && row.Freshness == MutationCurrent {
			stored = presented
			decision = DecideRuntimeMutation(RuntimeMutationInput{
				DurableEndpoint: durable, StoredAuthority: &stored, PresentedAuthority: &presented,
				TargetRuntimeID: "runtime-target", EventRuntimeID: eventRuntime,
			})
		}
		if !reflect.DeepEqual(decision.Class, row) {
			t.Fatalf("row=%#v decision=%#v", row, decision)
		}
	}
}
