package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/internal/version"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeSelectorInvalid     Code = "selector-invalid"
	CodeResourceNotFound    Code = "resource-not-found"
	CodeSelectorAmbiguous   Code = "selector-ambiguous"
	CodeRegistryUnavailable Code = "registry-unavailable"
)

// ResourceSelector holds the CLI's explicit resource scopes and label selectors.
// Each value follows the CLI grammar; values are never comma-split. Reads always
// cover the whole Registry unless an explicit scope is supplied.
type ResourceSelector struct {
	Project string
	Windows []string
	Panes   []string
	Labels  []string
}

// Version returns the version reported by the CLI.
func (c *Client) Version() string { return version.String() }

// ListResources returns a fresh CLI JSON list projection. It never uses an
// active Pane or cwd to supply implicit scopes. Empty results have [] items.
func (c *Client) ListResources(ctx context.Context, kind core.Kind, input ResourceSelector) (core.ResourceList, error) {
	if err := ctx.Err(); err != nil {
		return core.ResourceList{}, err
	}
	if !readKind(kind) {
		return core.ResourceList{}, &Refusal{Code: CodeSelectorInvalid, message: fmt.Sprintf("unsupported resource kind %q", kind)}
	}
	if c.pathsErr != nil {
		return core.ResourceList{}, resourceRefusal(c.pathsErr)
	}
	registry, err := resources.Load(c.paths)
	if err != nil {
		return core.ResourceList{}, resourceRefusal(err)
	}
	if kind == core.KindProject && len(input.Windows) > 0 {
		return core.ResourceList{}, &Refusal{Code: CodeSelectorInvalid, message: "ResourceSelector.Windows is not supported for Project lists"}
	}
	if kind != core.KindPane && len(input.Panes) > 0 {
		return core.ResourceList{}, &Refusal{Code: CodeSelectorInvalid, message: "ResourceSelector.Panes is supported only for Pane lists"}
	}
	query, err := resourceQuery(input)
	if err != nil {
		return core.ResourceList{}, resourceRefusal(err)
	}
	snapshot := resources.Observe(ctx, registry, c.transport)
	result, err := resources.Resolve(registry, snapshot.Runtime, &snapshot.Contexts, metadata.Kind(kind), query)
	if err != nil {
		return core.ResourceList{}, resourceRefusal(err)
	}
	if err = resources.Enforce(selector.Target{Verb: selector.VerbGet, Kind: metadata.Kind(kind), List: true}, query, result); err != nil {
		return core.ResourceList{}, resourceRefusal(err)
	}
	if err = ctx.Err(); err != nil {
		return core.ResourceList{}, err
	}
	out := core.ResourceList{APIVersion: metadata.APIVersion, Kind: string(kind) + "List", Items: make([]core.Resource, 0, len(result.Matches))}
	for _, match := range result.Matches {
		r, err := publicResource(registry, metadata.Kind(kind), match.UID)
		if err != nil {
			return core.ResourceList{}, err
		}
		context := core.Context{Value: match.Context.Value, Source: string(match.Context.Source), Observed: match.Context.Observed}
		switch r := r.(type) {
		case core.Project:
			out.Items = append(out.Items, core.ProjectView{Project: r, Context: context})
		case core.Window:
			out.Items = append(out.Items, core.WindowView{Window: r, Context: context})
		case core.Pane:
			out.Items = append(out.Items, core.PaneView{Pane: r, Context: context})
		case core.Agent:
			out.Items = append(out.Items, core.AgentView{Agent: r, Context: context})
		}
	}
	return out, nil
}

// GetResource resolves one explicit ref using the same exact-one rule as
// describe. The returned concrete value owns its complete object graph.
func (c *Client) GetResource(ctx context.Context, input core.Ref) (core.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !readKind(input.Kind) {
		return nil, &Refusal{Code: CodeSelectorInvalid, message: fmt.Sprintf("unsupported resource kind %q", input.Kind)}
	}
	if c.pathsErr != nil {
		return nil, resourceRefusal(c.pathsErr)
	}
	registry, err := resources.Load(c.paths)
	if err != nil {
		return nil, resourceRefusal(err)
	}
	ref, err := selector.ParseRef(metadata.Kind(input.Kind), input.Value)
	if err != nil {
		return nil, resourceRefusal(err)
	}
	var query selector.Query
	switch input.Kind {
	case core.KindProject:
		query.Project = &ref
	case core.KindWindow:
		query.Windows = []selector.Ref{ref}
	case core.KindPane:
		query.Panes = []selector.Ref{ref}
	case core.KindAgent:
		query.Agents = []selector.Ref{ref}
	default:
		return nil, &Refusal{Code: CodeSelectorInvalid, message: fmt.Sprintf("unsupported resource kind %q", input.Kind)}
	}
	snapshot := resources.Observe(ctx, registry, c.transport)
	result, err := resources.Resolve(registry, snapshot.Runtime, &snapshot.Contexts, metadata.Kind(input.Kind), query)
	if err != nil {
		return nil, resourceRefusal(err)
	}
	if err = resources.Enforce(selector.Target{Verb: selector.VerbDescribe, Kind: metadata.Kind(input.Kind)}, query, result); err != nil {
		return nil, resourceRefusal(err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return publicResource(registry, metadata.Kind(input.Kind), result.Matches[0].UID)
}
func resourceQuery(input ResourceSelector) (selector.Query, error) {
	var query selector.Query
	if input.Project != "" {
		ref, err := selector.ParseRef(metadata.KindProject, input.Project)
		if err != nil {
			return query, err
		}
		query.Project = &ref
	}
	for _, raw := range input.Windows {
		ref, err := selector.ParseRef(metadata.KindWindow, raw)
		if err != nil {
			return query, err
		}
		query.Windows = append(query.Windows, ref)
	}
	for _, raw := range input.Panes {
		ref, err := selector.ParseRef(metadata.KindPane, raw)
		if err != nil {
			return query, err
		}
		query.Panes = append(query.Panes, ref)
	}
	for _, raw := range input.Labels {
		label, err := selector.ParseLabel(raw)
		if err != nil {
			return query, err
		}
		query.Labels = append(query.Labels, label)
	}
	return query, nil
}
func resourceRefusal(err error) error {
	var failed *resources.Failure
	if errors.As(resources.Classify(err), &failed) {
		return &Refusal{Code: Code(failed.Code), message: failed.Error()}
	}
	return err
}
func publicResource(registry metadata.Registry, kind metadata.Kind, uid string) (core.Resource, error) {
	value, _, ok := resources.ResourceFor(registry, kind, uid)
	if !ok {
		return nil, fmt.Errorf("resolved uid %q is no longer in the registry", uid)
	}
	bytes, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	// Explicit definitions rather than aliases keep internal types out of the
	// public API. JSON conversion also isolates every nested map/slice/pointer.
	switch kind {
	case metadata.KindProject:
		var v core.Project
		err = json.Unmarshal(bytes, &v)
		return v, err
	case metadata.KindWindow:
		var v core.Window
		err = json.Unmarshal(bytes, &v)
		return v, err
	case metadata.KindPane:
		var v core.Pane
		err = json.Unmarshal(bytes, &v)
		return v, err
	case metadata.KindAgent:
		var v core.Agent
		err = json.Unmarshal(bytes, &v)
		return v, err
	}
	return nil, fmt.Errorf("unsupported resource kind %q", kind)
}

func readKind(k core.Kind) bool {
	return k == core.KindProject || k == core.KindWindow || k == core.KindPane || k == core.KindAgent
}
