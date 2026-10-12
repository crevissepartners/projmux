package projmux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	metadata "github.com/crevissepartners/projmux/internal/core/metadata"
	"github.com/crevissepartners/projmux/internal/core/selector"
	store "github.com/crevissepartners/projmux/internal/integrations/metadata"
	"github.com/crevissepartners/projmux/internal/services/mutations"
	"github.com/crevissepartners/projmux/internal/services/resources"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeConfirmationRequired     Code = "confirmation-required"
	CodeUnregisterPlanChanged    Code = "unregister-plan-changed"
	CodeRegistryUnregisterFailed Code = "registry-unregister-failed"
	CodeProjectLifecycleRefused  Code = "project-lifecycle-refused"
)

// UnregisterProject removes one explicit Project graph from the Registry while
// preserving runtime and external assets. DryRun and missing Confirm write
// nothing. Once admitted, later cancellation does not interrupt the transaction.
// Selection never infers inherited TMUX or an active Pane.
func (c *Client) UnregisterProject(ctx context.Context, req core.UnregisterRequest) (core.UnregisterResult, error) {
	out := core.UnregisterResult{}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if strings.TrimSpace(req.Ref) == "" {
		return out, &Refusal{Code: CodeSelectorInvalid, message: "UnregisterRequest.Ref must name exactly one Project"}
	}
	ref, err := selector.ParseRef(metadata.KindProject, req.Ref)
	if err != nil {
		return out, resourceRefusal(selectorInputError(err, "UnregisterRequest.Ref"))
	}
	if c.pathsErr != nil {
		return out, resourceRefusal(c.pathsErr)
	}
	reg, err := resources.Load(c.paths)
	if err != nil {
		return out, resourceRefusal(err)
	}
	query := selector.Query{Project: &ref}
	resolution, err := resources.Resolve(reg, metadata.RuntimeObservation{}, nil, metadata.KindProject, query)
	if err != nil {
		return out, resourceRefusal(err)
	}
	if err := resources.Enforce(selector.Target{Verb: selector.VerbDelete, Kind: metadata.KindProject}, query, resolution); err != nil {
		return out, resourceRefusal(err)
	}
	plan := mutations.BuildDeletePlan(reg, metadata.KindProject, resolution)
	preview := mutations.PreviewDelete(plan)
	if err := unregisterCopy(preview, &out.Plan); err != nil {
		return out, err
	}
	if req.DryRun {
		return out, nil
	}
	if plan.NeedsConfirmation() && !req.Confirm {
		return out, &Refusal{Code: CodeConfirmationRequired, message: "UnregisterRequest.Confirm is required; use DryRun to review the Registry-only plan first"}
	}
	s := mutations.NewStore(store.PathFor(c.paths.StateDir))
	var warnings bytes.Buffer
	result, err := mutations.Unregister(ctx, plan, resolution, mutations.UnregisterOptions{
		Mutate: func(kind metadata.Kind, uids []string, apply func(*metadata.Registry, metadata.Mutator) error) error {
			_, err := s.Update(func(r *metadata.Registry) error {
				for _, uid := range uids {
					if _, _, ok := resources.ResourceFor(*r, kind, uid); !ok {
						return fmt.Errorf("%s %q disappeared before the mutation ran", strings.ToLower(string(kind)), uid)
					}
				}
				return apply(r, s.Mutator())
			})
			return err
		},
		StateDir: func() (string, error) { return c.paths.StateDir, nil }, Via: mutations.DeletionViaSDK, Warnings: &warnings,
	})
	out.Committed, out.OperationID, out.Notice = result.Committed, result.OperationID, strings.TrimSuffix(warnings.String(), "\n")
	if result.Committed {
		preview.Affected = result.Affected
		if e := unregisterCopy(preview, &out.Plan); e != nil {
			return out, e
		}
		if e := unregisterCopy(result.Receipt, &out.Receipt); e != nil {
			return out, e
		}
	}
	if err != nil {
		var f *mutations.Failure
		if errors.As(err, &f) {
			return out, &Refusal{Code: Code(f.Code), message: fmt.Sprintf("project lifecycle action=delete-project stage=registry-unregister old_uid=%s new_uid=-: %s", strings.Join(resolution.UIDs(), ","), err), cause: err}
		}
	}
	return out, err
}
func unregisterCopy(source, target any) error {
	b, err := json.Marshal(source)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, target)
}
