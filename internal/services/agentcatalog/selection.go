package agentcatalog

import (
	"fmt"

	"github.com/crevissepartners/projmux/internal/aiprovider"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/services/resources"
)

func ResolveAgent(registry metadata.Registry, raw string) (metadata.Agent, error) {
	ref, err := selector.ParseRef(metadata.KindAgent, raw)
	if err != nil {
		return metadata.Agent{}, resources.Classify(err)
	}
	query := selector.Query{Agents: []selector.Ref{ref}}
	result, err := resources.Resolve(registry, metadata.RuntimeObservation{}, nil, metadata.KindAgent, query)
	if err != nil {
		return metadata.Agent{}, resources.Classify(err)
	}
	if err = resources.Enforce(selector.Target{Verb: selector.VerbStatus, Kind: metadata.KindAgent}, query, result); err != nil {
		return metadata.Agent{}, err
	}
	agent, ok := registry.Agent(result.Matches[0].UID)
	if !ok {
		return metadata.Agent{}, fmt.Errorf("agent capabilities: resolved Agent disappeared")
	}
	return agent.Clone(), nil
}
func AgentProvider(agent metadata.Agent) (aiprovider.ID, error) {
	m, ok := aiprovider.Lookup(agent.Spec.Provider)
	if !ok {
		return "", &Failure{Code: "provider-invalid", Cause: fmt.Errorf("agent capabilities: agent/%s has unsupported provider %q", agent.Metadata.Name, agent.Spec.Provider)}
	}
	return m.ID, nil
}
