// Package agentsessions shares the public, read-only session history routes.
// It reads Registry and append-only history only; it never opens transcripts.
package agentsessions

import (
	"context"
	"fmt"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/agents/sessionhistory"
	"github.com/crevissepartners/projmux/internal/services/resources"
)

const ReasonProviderUnsupported = "sessions-provider-unsupported"

type Failure struct{ Code, Message string }

func (e *Failure) Error() string            { return e.Message }
func (e *Failure) MetadataUsageError() bool { return true }

// Service receives explicit read dependencies. StateDir is evaluated after
// selection and provider validation, preserving CLI error precedence.
type Service struct {
	LoadRegistry func() (metadata.Registry, error)
	StateDir     func() (string, error)
}

type AgentResult struct {
	AgentUID     string                  `json:"agentUID"`
	AgentName    string                  `json:"agentName"`
	Sessions     []sessionhistory.Record `json:"sessions"`
	CorruptLines int                     `json:"corruptLines"`
}
type ProjectResult struct {
	ProjectUID   string                          `json:"projectUID"`
	ProjectName  string                          `json:"projectName"`
	Sessions     []sessionhistory.ProjectSession `json:"sessions"`
	Unattributed int                             `json:"unattributed"`
	Ambiguous    int                             `json:"ambiguous"`
	CorruptLines int                             `json:"corruptLines"`
}

// ListAgent requires the explicit Agent target used by agent sessions list.
// Query contains only parsed CLI scopes and labels, never ambient context.
func (s Service) ListAgent(ctx context.Context, query selector.Query) (AgentResult, error) {
	const spelling = "agent sessions list"
	if err := ctx.Err(); err != nil {
		return AgentResult{}, err
	}
	if len(query.Agents) == 0 {
		return AgentResult{}, &Failure{Code: "selector-invalid", Message: spelling + " requires <agent-ref>"}
	}
	registry, err := s.LoadRegistry()
	if err != nil {
		return AgentResult{}, err
	}
	resolution, err := resources.Resolve(registry, metadata.RuntimeObservation{}, nil, metadata.KindAgent, query)
	if err != nil {
		return AgentResult{}, err
	}
	if err := resources.Enforce(selector.Target{Verb: selector.VerbTopic, Kind: metadata.KindAgent}, query, resolution); err != nil {
		return AgentResult{}, err
	}
	found, ok := registry.Agent(resolution.Matches[0].UID)
	if !ok {
		return AgentResult{}, fmt.Errorf("%s: resolved uid %q is no longer in the registry", spelling, resolution.Matches[0].UID)
	}
	agent := found.Clone()
	provider := metadata.NormalizeProvider(agent.Spec.Provider)
	if provider != "claude" && provider != "codex" {
		return AgentResult{}, &Failure{Code: ReasonProviderUnsupported, Message: fmt.Sprintf("%s: agent/%s is a %q Agent; session history is recorded only for --provider claude or codex (%s)", spelling, agent.Metadata.Name, agent.Spec.Provider, ReasonProviderUnsupported)}
	}
	stateDir, err := s.stateDir(spelling)
	if err != nil {
		return AgentResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return AgentResult{}, err
	}
	listed, err := sessionhistory.List(stateDir, agent)
	if err != nil {
		return AgentResult{}, fmt.Errorf("%s: %w", spelling, err)
	}
	if err := ctx.Err(); err != nil {
		return AgentResult{}, err
	}
	return AgentResult{AgentUID: listed.AgentUID, AgentName: agent.Metadata.Name, Sessions: listed.Sessions, CorruptLines: listed.CorruptLines}, nil
}

// ListProject accepts exactly one Project ref. A removed Project can be read
// only by its exact Project-shaped UID, as on the public CLI route.
func (s Service) ListProject(ctx context.Context, raw string) (ProjectResult, error) {
	const spelling = "agent sessions project"
	if err := ctx.Err(); err != nil {
		return ProjectResult{}, err
	}
	registry, err := s.LoadRegistry()
	if err != nil {
		return ProjectResult{}, err
	}
	var projectUID, projectName string
	ref, parseErr := selector.ParseRef(metadata.KindProject, raw)
	if parseErr == nil && ref.IsUID() && metadata.IsProjectUIDShaped(ref.UID) {
		if _, registered := registry.Project(ref.UID); !registered {
			projectUID = ref.UID
		}
	}
	if projectUID == "" {
		if parseErr != nil {
			return ProjectResult{}, parseErr
		}
		query := selector.Query{Project: &ref}
		resolution, err := resources.Resolve(registry, metadata.RuntimeObservation{}, nil, metadata.KindProject, query)
		if err != nil {
			return ProjectResult{}, err
		}
		if err := resources.Enforce(selector.Target{Verb: selector.VerbGet, Kind: metadata.KindProject}, query, resolution); err != nil {
			return ProjectResult{}, err
		}
		project, ok := registry.Project(resolution.Matches[0].UID)
		if !ok {
			return ProjectResult{}, fmt.Errorf("%s: resolved uid %q is no longer in the registry", spelling, resolution.Matches[0].UID)
		}
		projectUID, projectName = project.Metadata.UID, project.Metadata.Name
	}
	stateDir, err := s.stateDir(spelling)
	if err != nil {
		return ProjectResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ProjectResult{}, err
	}
	listed, err := sessionhistory.ListProject(stateDir, &registry, projectUID)
	if err != nil {
		return ProjectResult{}, fmt.Errorf("%s: %w", spelling, err)
	}
	if err := ctx.Err(); err != nil {
		return ProjectResult{}, err
	}
	return ProjectResult{ProjectUID: listed.ProjectUID, ProjectName: projectName, Sessions: listed.Sessions, Unattributed: listed.Unattributed, Ambiguous: listed.Ambiguous, CorruptLines: listed.CorruptLines}, nil
}
func (s Service) stateDir(spelling string) (string, error) {
	if s.StateDir == nil {
		return "", fmt.Errorf("%s: the projmux state directory is not configured", spelling)
	}
	stateDir, err := s.StateDir()
	if err != nil {
		return "", fmt.Errorf("%s: %w", spelling, err)
	}
	return stateDir, nil
}
