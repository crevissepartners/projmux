package projmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/integrations/tmux"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	mutationruntime "github.com/crevissepartners/projmux/internal/services/mutations/runtime"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeDeleteKindUnsupported    Code = "delete-kind-unsupported"
	CodeDeleteProcessUnsupported Code = "delete-process-unsupported"
	CodeDeleteTransportRequired  Code = "delete-transport-required"
	CodeDeleteRuntimeRefused     Code = "delete-runtime-refused"
	CodeDeleteExecutionFailed    Code = "delete-execution-failed"
)

// Delete removes Windows selected by one explicit ref and their descendants.
// DryRun and missing Confirm write nothing. Cancellation is effective before
// admission only; the admitted transaction and compensation finish atomically.
// The SDK has no current Pane and never queues a deferred self-kill.
func (c *Client) Delete(ctx context.Context, req core.DeleteRequest) (core.DeleteResult, error) {
	return c.deleteWithRunner(ctx, req, tmux.ExecRunner{})
}
func (c *Client) deleteWithRunner(ctx context.Context, req core.DeleteRequest, runner mutationruntime.TmuxCommandRunner) (core.DeleteResult, error) {
	out := core.DeleteResult{}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if req.Kind != core.KindWindow {
		return out, &Refusal{Code: CodeDeleteKindUnsupported, message: fmt.Sprintf("DeleteRequest.Kind %q is unsupported", req.Kind)}
	}
	if strings.TrimSpace(req.Ref) == "" {
		return out, &Refusal{Code: CodeSelectorInvalid, message: "DeleteRequest.Ref must name one resource"}
	}
	query, err := deleteQuery(req)
	if err != nil {
		return out, resourceRefusal(err)
	}
	if c.pathsErr != nil {
		return out, resourceRefusal(c.pathsErr)
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return out, resourceRefusal(err)
	}
	resolution, err := resources.Resolve(reg, metadata.RuntimeObservation{}, nil, metadata.Kind(req.Kind), query)
	if err != nil {
		return out, resourceRefusal(err)
	}
	if err := resources.Enforce(selector.Target{Verb: selector.VerbDelete, Kind: metadata.Kind(req.Kind)}, query, resolution); err != nil {
		return out, resourceRefusal(err)
	}
	plan := mutationruntime.DeletePlan(mutations.BuildDeletePlan(reg, metadata.Kind(req.Kind), resolution))
	plan.ExactUID = query.Windows[0].UID != ""
	if err := unregisterCopy(mutations.PreviewDelete(mutations.DeletePlan(plan)), &out.Plan); err != nil {
		return out, err
	}
	if err := deleteTmuxOnly(reg, plan); err != nil {
		return out, err
	}
	socket := req.SocketPath
	if socket == "" {
		socket = c.runtimeSocketPath
	}
	target, err := mutationruntime.ResolveDeleteTarget("delete window", "", socket, nil)
	if err != nil {
		code := CodeDeleteTransportRequired
		var f *mutations.Failure
		if errors.As(err, &f) && f.Code == string(CodeRuntimeRequestInvalid) {
			code = CodeRuntimeRequestInvalid
		}
		message := err.Error()
		if code == CodeRuntimeRequestInvalid {
			message = "DeleteRequest.SocketPath must be absolute"
		}
		if code == CodeDeleteTransportRequired {
			message = "DeleteRequest.SocketPath or Options.SocketPath is required"
		}
		return out, &Refusal{Code: code, message: message, cause: err}
	}
	// Getenv is intentionally nil: no caller Pane or inherited transport is read.
	runtime := &mutationruntime.TmuxWindowDeleteRuntime{Runner: runner}
	runtime.UseExactTarget(target)
	live, err := runtime.Preflight(ctx, reg, plan)
	if err != nil {
		return out, deleteRefusal(CodeDeleteRuntimeRefused, err)
	}
	if err := unregisterCopy(live.Targets, &out.Windows); err != nil {
		return out, err
	}
	if req.DryRun {
		return out, nil
	}
	if mutationruntime.NeedsDeleteConfirmation(plan, mutationruntime.PaneLiveDeletePlan{}) && !req.Confirm {
		return out, &Refusal{Code: CodeConfirmationRequired, message: "DeleteRequest.Confirm is required; use DryRun to review the deletion plan first"}
	}
	s := mutations.NewStore(store.PathFor(c.paths.StateDir))
	var warnings bytes.Buffer
	e := mutationruntime.DeleteExecutor{Windows: runtime, Update: s.Update, Mutator: s.Mutator, Via: mutations.DeletionViaSDK, StateDir: func() (string, error) { return c.paths.StateDir, nil }, Warnings: &warnings, Allow: deleteTmuxOnly}
	e.Mutate = func(kind metadata.Kind, uids []string, apply func(*metadata.Registry, metadata.Mutator) error) error {
		_, err := s.Update(func(r *metadata.Registry) error {
			for _, uid := range uids {
				if _, _, ok := resources.ResourceFor(*r, kind, uid); !ok {
					return fmt.Errorf("%s %q disappeared before the mutation ran", strings.ToLower(string(kind)), uid)
				}
			}
			return apply(r, s.Mutator())
		})
		return err
	}
	result, err := e.Execute(ctx, "delete window", plan, resolution, live, mutationruntime.PaneLiveDeletePlan{})
	out.Committed, out.OperationID, out.Notice = result.Committed, result.OperationID, strings.TrimSuffix(warnings.String(), "\n")
	if result.Committed {
		if e := unregisterCopy(result.Affected, &out.Plan.Affected); e != nil {
			return out, e
		}
		if e := unregisterCopy(result.Receipt, &out.Receipt); e != nil {
			return out, e
		}
	}
	return out, deleteRefusal(CodeDeleteExecutionFailed, err)
}
func deleteRefusal(code Code, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var r *Refusal
	if errors.As(err, &r) {
		return err
	}
	return &Refusal{Code: code, message: err.Error(), cause: err}
}
func deleteTmuxOnly(reg metadata.Registry, plan mutationruntime.DeletePlan) error {
	for _, t := range plan.Targets {
		if reg.IsVirtualWindow(t.Match.UID) {
			return &Refusal{Code: CodeDeleteProcessUnsupported, message: "Delete requires a tmux Window; process lifecycle is unsupported"}
		}
		for _, d := range t.Descendants {
			if d.Kind == metadata.KindPane {
				if p, ok := reg.Pane(d.UID); ok && p.Spec.Runtime.EffectiveKind() == metadata.RuntimeProcess {
					return &Refusal{Code: CodeDeleteProcessUnsupported, message: "Delete cannot remove process descendants"}
				}
			}
		}
	}
	return nil
}
func deleteQuery(req core.DeleteRequest) (selector.Query, error) {
	q, err := renameQuery(core.RenameRequest{Kind: req.Kind, Ref: req.Ref, Project: req.Project, Window: req.Window})
	if err != nil {
		var f *resources.Failure
		if errors.As(err, &f) {
			return q, &resources.Failure{Code: f.Code, Cause: errors.New(strings.ReplaceAll(f.Error(), "RenameRequest", "DeleteRequest"))}
		}
		var input *selector.SelectorError
		if errors.As(err, &input) {
			copy := *input
			copy.Detail = strings.ReplaceAll(copy.Detail, "RenameRequest", "DeleteRequest")
			return q, &copy
		}
	}
	return q, err
}
