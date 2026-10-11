package core

// Capabilities is the agent capabilities JSON projection. Runtime is registry evidence, not live authority.
type Capabilities struct {
	Provider     string             `json:"provider"`
	Agent        *CapabilityAgent   `json:"agent,omitempty"`
	Runtime      *CapabilityRuntime `json:"runtimeEligibility,omitempty"`
	Capabilities []CapabilityEntry  `json:"capabilities"`
}

type CapabilityAgent struct {
	UID   string     `json:"uid"`
	Name  string     `json:"name"`
	Phase AgentPhase `json:"phase"`
}

type CapabilityRuntime struct {
	Ready                bool                    `json:"registryReady"`
	Evidence             string                  `json:"evidence"`
	LiveVerified         bool                    `json:"liveVerified"`
	Reason               string                  `json:"reason"`
	PaneUID              string                  `json:"paneUID,omitempty"`
	PaneRuntimeID        string                  `json:"paneRuntimeID,omitempty"`
	ActivationGeneration string                  `json:"activationGeneration,omitempty"`
	RouteIncarnation     string                  `json:"routeIncarnation,omitempty"`
	StateDomainID        string                  `json:"stateDomainID,omitempty"`
	EndpointGenerationID string                  `json:"endpointGenerationID,omitempty"`
	BrokerRuntimeID      string                  `json:"brokerRuntimeID,omitempty"`
	ConnectionEpoch      uint64                  `json:"connectionEpoch,omitempty"`
	BindingEpoch         uint64                  `json:"bindingEpoch,omitempty"`
	Coordination         *CapabilityCoordination `json:"coordination,omitempty"`
}

type CapabilityCoordination struct {
	Eligible bool   `json:"eligible"`
	Evidence string `json:"evidence"`
	Reason   string `json:"reason"`
	Recovery string `json:"recovery,omitempty"`
}

type CapabilityEntry struct {
	Action              string `json:"action"`
	Group               string `json:"group"`
	Callable            bool   `json:"callable"`
	Mode                string `json:"mode"`
	CompletionPrecision string `json:"completionPrecision"`
	Available           *bool  `json:"available,omitempty"`
	Evidence            string `json:"evidence,omitempty"`
	Reason              string `json:"reason,omitempty"`
}

// ModelSuggestions preserves suggestion order. Models is not an allowlist.
type ModelSuggestions struct {
	Provider        string   `json:"provider"`
	Models          []string `json:"models"`
	AcceptsUnlisted bool     `json:"acceptsUnlisted"`
}
