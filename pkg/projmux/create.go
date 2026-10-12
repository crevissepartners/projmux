package projmux

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/crevissepartners/projmux/internal/config"
	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/receipt"
	"github.com/crevissepartners/projmux/internal/core/resourcegraph"
	"github.com/crevissepartners/projmux/internal/core/selector"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/projectcandidates"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeCreateExecutableInvalid  Code = "create-executable-invalid"
	CodeCreateRouteRefused       Code = "create-route-refused"
	CodeCreateExecutionFailed    Code = "create-execution-failed"
	CodeCreateRequestInvalid     Code = "create-request-invalid"
	CodeProcessAnchorUnsupported Code = "process-create-pane-unsupported"
	CodeProcessSplitUnsupported  Code = "process-split-unsupported"
)

// CreateWindow creates one shell Window through the CLI's shared transaction.
// Cancellation is effective before admission; admitted execution and rollback
// finish independently. No provider is started by this method.
func (c *Client) CreateWindow(ctx context.Context, req core.CreateWindowRequest) (core.CreateWindowResult, error) {
	return c.createWindow(ctx, req, tmux.ExecRunner{})
}
func (c *Client) createWindow(ctx context.Context, req core.CreateWindowRequest, runner mutationruntime.TmuxCommandRunner) (core.CreateWindowResult, error) {
	var result core.CreateWindowResult
	scope, labels, socket, err := c.createInputs(ctx, "CreateWindowRequest", req.Project, req.Name, req.Labels, req.SocketPath)
	if err != nil {
		return result, err
	}
	execution, transaction := mutationruntime.NewSDKCreate(c.paths, socket, c.projectShell, c.projmuxExecutable, c.projectSessionName, runner, c.createDiscoveryRoots)

	var out mutationruntime.ShellCreateResult
	err = transaction.Transact(mutationruntime.CreateKindWindow, execution.WindowOperation(scope, mutationruntime.WindowRequest{Name: req.Name, Labels: labels}, &out), func(ctx context.Context, reg metadata.Registry, mut metadata.Mutator, op string) (mutationruntime.LiveSessionIdentity, error) {
		return execution.ProjectOwnershipGuard(scope)(ctx, reg, mut, op)
	})
	rows := out.Results

	if err != nil {
		return result, createRefusal(err, "CreateWindowRequest")
	}
	projection, err := c.createProjection(socket, core.KindWindow, rows[0])
	if err != nil {
		return result, err
	}
	result.Projection = projection
	result.Window = projection.Items[0].(core.Window)
	result.PaneID = rows[0].PaneID
	err = unregisterCopy(createReceipt(metadata.KindWindow, rows, []string{rows[0].WindowUID}), &result.Receipt)
	return result, err
}

// CreatePane creates one shell Pane using an explicit Window and optional Pane
// anchor. It uses the stored anchor when Pane is empty, and never an active Pane.
func (c *Client) CreatePane(ctx context.Context, req core.CreatePaneRequest) (core.CreatePaneResult, error) {
	return c.createPane(ctx, req, tmux.ExecRunner{})
}
func (c *Client) createPane(ctx context.Context, req core.CreatePaneRequest, runner mutationruntime.TmuxCommandRunner) (core.CreatePaneResult, error) {
	var result core.CreatePaneResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if strings.TrimSpace(req.Window) == "" {
		return result, &Refusal{Code: CodeSelectorInvalid, message: "CreatePaneRequest.Window must name one Window"}
	}
	for _, pair := range []struct {
		kind       metadata.Kind
		raw, field string
	}{{metadata.KindWindow, req.Window, "Window"}, {metadata.KindPane, req.Pane, "Pane"}} {
		if pair.raw != "" {
			if _, err := selector.ParseRef(pair.kind, pair.raw); err != nil {
				return result, resourceRefusal(selectorInputError(err, "CreatePaneRequest."+pair.field))
			}
		}
	}
	if req.Placement != "" && req.Placement != "right" && req.Placement != "down" {
		return result, &Refusal{Code: CodeCreateRequestInvalid, message: "CreatePaneRequest.Placement requires right or down"}
	}
	if req.CwdFrom != "" {
		req.CwdFrom = strings.TrimSpace(req.CwdFrom)
		if req.CwdFrom != "project" && req.CwdFrom != "pane" {
			return result, &Refusal{Code: CodeCreateRequestInvalid, message: "CreatePaneRequest.CwdFrom requires project or pane"}
		}
	}
	scope, labels, socket, err := c.createInputs(ctx, "CreatePaneRequest", req.Project, req.Name, req.Labels, req.SocketPath)
	if err != nil {
		return result, err
	}
	selection := mutationruntime.CreateSelection{Name: req.Name, Windows: []string{req.Window}}
	if req.Pane != "" {
		selection.Panes = []string{req.Pane}
	}
	placement := req.Placement
	if placement == "" {
		placement = "right"
	}
	execution, transaction := mutationruntime.NewSDKCreate(c.paths, socket, c.projectShell, c.projmuxExecutable, c.projectSessionName, runner, c.createDiscoveryRoots)
	admission := execution.ProcessAnchorAdmission(scope, selection, "create pane")
	var out mutationruntime.ShellCreateResult
	err = transaction.TransactAdmitted(mutationruntime.CreateKindPane, admission, execution.PaneOperation(scope, mutationruntime.ShellPaneRequest{CreateSelection: selection, Placement: placement, CwdFrom: req.CwdFrom}, labels, &out), func(ctx context.Context, reg metadata.Registry, mut metadata.Mutator, op string) (mutationruntime.LiveSessionIdentity, error) {
		return execution.ProjectOwnershipGuard(scope)(ctx, reg, mut, op)
	})
	if err != nil {
		return result, createRefusal(err, "CreatePaneRequest")
	}
	projection, err := c.createProjection(socket, core.KindPane, out.Results[0])
	if err != nil {
		return result, err
	}
	result.Projection = projection
	result.Pane = projection.Items[0].(core.Pane)
	result.PaneID = out.Results[0].PaneID
	result.Notices = out.Notices
	err = unregisterCopy(createReceipt(metadata.KindPane, out.Results, out.SelectedWindowUIDs), &result.Receipt)
	return result, err
}
func (c *Client) createInputs(ctx context.Context, prefix, project, name string, rawLabels []string, socket string) (mutationruntime.CreateScope, map[string]string, string, error) {
	var scope mutationruntime.CreateScope
	if err := ctx.Err(); err != nil {
		return scope, nil, "", err
	}
	if strings.TrimSpace(project) == "" {
		return scope, nil, "", &Refusal{Code: CodeSelectorInvalid, message: prefix + ".Project must name one Project"}
	}
	ref, err := selector.ParseRef(metadata.KindProject, project)
	if err != nil {
		return scope, nil, "", resourceRefusal(selectorInputError(err, prefix+".Project"))
	}
	scope.Project = ref
	labels := map[string]string{}
	for _, raw := range rawLabels {
		label, err := selector.ParseLabel(raw)
		if err != nil {
			return scope, nil, "", &Refusal{Code: CodeResourceLabelInvalid, message: selectorInputError(err, prefix+".Labels").Error()}
		}
		labels[label.Key] = label.Value
	}
	if name != "" {
		if err := metadata.ValidateName(name); err != nil {
			return scope, nil, "", &Refusal{Code: CodeResourceNameInvalid, message: prefix + ".Name: " + err.Error()}
		}
	}
	if socket == "" {
		socket = c.runtimeSocketPath
	}
	if socket != "" && !filepath.IsAbs(socket) {
		return scope, nil, "", &Refusal{Code: CodeRuntimeRequestInvalid, message: prefix + ".SocketPath must be absolute"}
	}
	if c.pathsErr != nil {
		return scope, nil, "", resourceRefusal(c.pathsErr)
	}
	if c.projmuxExecutable == "" || !filepath.IsAbs(c.projmuxExecutable) {
		return scope, nil, "", &Refusal{Code: CodeCreateExecutableInvalid, message: "Options.ProjmuxExecutable must name an absolute projmux executable"}
	}
	info, err := os.Stat(c.projmuxExecutable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return scope, nil, "", &Refusal{Code: CodeCreateExecutableInvalid, message: "Options.ProjmuxExecutable must name an existing executable file", cause: err}
	}
	if _, err := resources.Load(c.paths); err != nil {
		return scope, nil, "", resourceRefusal(err)
	}
	if err := ctx.Err(); err != nil {
		return scope, nil, "", err
	}
	return scope, labels, socket, nil
}
func createRefusal(err error, prefix string) error {
	if err == nil {
		return nil
	}
	var failed *mutations.Failure
	if errors.As(err, &failed) {
		return &Refusal{Code: Code(failed.Code), message: err.Error(), cause: err}
	}
	var capability resourcegraph.ProcessCapabilityError
	if errors.As(err, &capability) {
		code := CodeProcessAnchorUnsupported
		if capability.Action == resourcegraph.ProcessSplit {
			code = CodeProcessSplitUnsupported
		}
		return &Refusal{Code: code, message: err.Error(), cause: err}
	}
	var selection *selector.SelectorError
	if errors.As(err, &selection) {
		return resourceRefusal(err)
	}
	if metadata.IsUsageError(err) {
		var f *mutations.Failure
		if errors.As(mutations.Classify(err), &f) {
			switch Code(f.Code) {
			case CodeResourceNameInvalid, CodeResourceNameConflict, CodeResourceNameExhausted:
				return &Refusal{Code: Code(f.Code), message: prefix + ".Name: " + err.Error(), cause: err}
			}
		}
		return &Refusal{Code: CodeCreateRequestInvalid, message: err.Error(), cause: err}
	}
	return &Refusal{Code: CodeCreateExecutionFailed, message: err.Error(), cause: err}
}
func (c *Client) createProjection(_ string, kind core.Kind, row mutationruntime.ShellCreateResource) (core.ResourceList, error) {
	registry, err := resources.Load(c.paths)
	if err != nil {
		return core.ResourceList{}, resourceRefusal(err)
	}
	value, err := publicResource(registry, metadata.Kind(kind), row.UID)
	if err != nil {
		return core.ResourceList{}, err
	}
	return core.ResourceList{APIVersion: metadata.APIVersion, Kind: string(kind) + "List", Items: []core.Resource{value}}, nil
}

func createReceipt(kind metadata.Kind, rows []mutationruntime.ShellCreateResource, selected []string) receipt.OperationReceipt {
	var values []mutationruntime.CreateReceiptResource
	for _, row := range rows {
		values = append(values, mutationruntime.CreateReceiptResource{Kind: row.Kind, UID: row.UID, Name: row.Name, Action: receipt.ActionCreated})
	}
	return mutationruntime.CreateReceipt(kind, values, selected)
}

func (c *Client) createDiscoveryRoots() ([]string, error) {
	service := projectcandidates.Service{Paths: func() (config.Paths, error) { return c.paths, c.pathsErr }, HomeDir: func() (string, error) { return c.discoveryHome, nil }, LookupEnv: func(key string) string { return c.discoveryEnv[key] }}
	result, err := service.List(context.Background())
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(result.Candidates))
	for _, candidate := range result.Candidates {
		paths = append(paths, candidate.Path)
	}
	return paths, nil
}
