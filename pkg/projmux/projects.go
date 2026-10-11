package projmux

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeProjectRootInvalid    Code = "project-root-invalid"
	CodeResourceNameInvalid   Code = "resource-name-invalid"
	CodeResourceNameConflict  Code = "resource-name-conflict"
	CodeResourceNameExhausted Code = "resource-name-exhausted"
	CodeResourceLabelInvalid  Code = "resource-label-invalid"
)

// CreateProject registers or reuses one explicit root without starting tmux or
// a provider. Cancellation before admission writes nothing; an admitted change
// finishes its atomic Registry transaction independently of later cancellation.
func (c *Client) CreateProject(ctx context.Context, req core.CreateProjectRequest) (core.CreateProjectResult, error) {
	if err := ctx.Err(); err != nil {
		return core.CreateProjectResult{}, err
	}
	if c.pathsErr != nil {
		return core.CreateProjectResult{}, &Refusal{Code: CodeRegistryUnavailable, message: c.pathsErr.Error()}
	}
	labels := map[string]string(nil)
	if len(req.Labels) > 0 {
		labels = make(map[string]string, len(req.Labels))
	}
	for _, raw := range req.Labels {
		l, err := selector.ParseLabel(raw)
		if err != nil {
			return core.CreateProjectResult{}, &Refusal{Code: CodeResourceLabelInvalid, message: selectorInputError(err, "CreateProjectRequest.Labels").Error()}
		}
		labels[l.Key] = l.Value
	}
	root, err := mutations.ProjectRoot(req.Root)
	if err != nil {
		return core.CreateProjectResult{}, projectRefusal(err)
	}
	id, err := mutations.NewOperationID()
	if err != nil {
		return core.CreateProjectResult{}, err
	}
	result, err := mutations.RegisterProject(ctx, mutations.NewStore(store.PathFor(c.paths.StateDir)), metadata.RegisterProjectOptions{Root: root, Name: req.Name, Labels: labels, DefaultShell: c.projectShell, SessionName: c.projectSessionName(root), OperationID: id})
	if err != nil {
		return core.CreateProjectResult{}, projectRefusal(err)
	}
	out := core.CreateProjectResult{Created: !result.Reused, Reused: result.Reused}
	// JSON conversion mirrors the full internal graph and allocates independent
	// maps/slices/pointers, as in the read API.
	for _, pair := range []struct{ source, target any }{
		{result.Project, &out.Project}, {result.Windows, &out.Windows}, {result.Panes, &out.Panes}, {mutations.ProjectReceipt(result), &out.Receipt},
	} {
		b, e := json.Marshal(pair.source)
		if e != nil {
			return core.CreateProjectResult{}, e
		}
		if e = json.Unmarshal(b, pair.target); e != nil {
			return core.CreateProjectResult{}, e
		}
	}
	out.Projection = core.ResourceList{APIVersion: metadata.APIVersion, Kind: "ProjectList", Items: []core.Resource{out.Project}}
	return out, nil
}
func projectRefusal(err error) error {
	var f *mutations.Failure
	if !errors.As(mutations.Classify(err), &f) {
		return err
	}
	message := f.Error()
	switch Code(f.Code) {
	case CodeProjectRootInvalid:
		message = "CreateProjectRequest.Root: " + message
	case CodeResourceNameInvalid, CodeResourceNameConflict, CodeResourceNameExhausted:
		message = "CreateProjectRequest.Name: " + message
	case CodeResourceLabelInvalid:
		message = "CreateProjectRequest.Labels: " + message
	}
	// Replace syntax owned by the CLI, preserving caller values in quotes.
	message = strings.Replace(message, "create project requires --root ", "create project requires Root ", 1)
	message = strings.Replace(message, "create project: --root ", "create project: Root ", 1)
	return &Refusal{Code: Code(f.Code), message: message}
}
