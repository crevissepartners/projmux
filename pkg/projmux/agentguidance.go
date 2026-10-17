package projmux

import (
	"context"
	"errors"
	"github.com/crevissepartners/projmux/internal/config"
	guidance "github.com/crevissepartners/projmux/internal/core/agentguidance"
	"github.com/crevissepartners/projmux/internal/services/agentguidance"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeAgentGuidanceInvalid    Code = "agent-guidance-invalid"
	CodeAgentGuidanceTooLarge   Code = "agent-guidance-too-large"
	CodeAgentGuidanceUnreadable Code = "agent-guidance-unreadable"
	CodeAgentGuidanceStale      Code = "agent-guidance-stale"
)

func (c *Client) guidance() agentguidance.Service {
	return agentguidance.Service{Paths: c.instructions.Paths, RegistryPaths: func() (config.Paths, error) { return c.paths, c.pathsErr }}
}

// ReadAgentGuidance reads the user file and built-in default without migration or writes.
func (c *Client) ReadAgentGuidance(ctx context.Context) (core.AgentGuidanceFile, error) {
	v, err := c.guidance().Read(ctx)
	return core.AgentGuidanceFile(v), guidanceRefusal(err)
}

// ValidateAgentGuidance validates a candidate against current policy and both
// assemblies. It creates no directories, locks, receipts, policy or snapshots.
func (c *Client) ValidateAgentGuidance(ctx context.Context, content []byte) (core.AgentGuidanceValidation, error) {
	v, err := c.guidance().Validate(ctx, content)
	return core.AgentGuidanceValidation(v), guidanceRefusal(err)
}

// WriteAgentGuidance requires the revision from Read or Validate, then checks it
// and the candidate under the shared writer lock. Only user bytes are replaced.
// External editors that ignore the lock are outside this concurrency guarantee.
func (c *Client) WriteAgentGuidance(ctx context.Context, content []byte, revision string) (core.AgentGuidanceFile, error) {
	v, err := c.guidance().Write(ctx, content, revision)
	return core.AgentGuidanceFile(v), guidanceRefusal(err)
}

// ResetAgentGuidance removes user bytes and restores the whole switch, retaining
// fragment masks. This is not a multi-file transaction: policy failure can follow
// successful removal. Cancellation before admission is preserved.
func (c *Client) ResetAgentGuidance(ctx context.Context, revision string) (core.AgentGuidanceFile, error) {
	v, err := c.guidance().Reset(ctx, revision)
	return core.AgentGuidanceFile(v), guidanceRefusal(err)
}

// InspectAgentGuidance uses the existing show behavior, including first-use migration.
func (c *Client) InspectAgentGuidance(ctx context.Context, process bool) (core.AgentGuidanceInspection, error) {
	v, err := c.guidance().Inspect(ctx, process)
	out := core.AgentGuidanceInspection{Disabled: v.Disabled, DisabledFragments: v.DisabledFragments, Text: v.Text, Digest: v.Digest, MigrationNotice: v.MigrationNotice, Source: string(v.Source)}
	if v.Parts != nil {
		out.Parts = make([]core.AgentGuidancePart, len(v.Parts))
		for i, p := range v.Parts {
			out.Parts[i] = core.AgentGuidancePart(p)
		}
	}
	return out, guidanceRefusal(err)
}

// SetAgentGuidanceEnabled controls all guidance (empty fragment) or a basename,
// preserving the existing enable/disable semantics.
func (c *Client) SetAgentGuidanceEnabled(ctx context.Context, fragment string, enabled bool) error {
	return guidanceRefusal(c.guidance().SetEnabled(ctx, fragment, enabled))
}
func guidanceRefusal(err error) error {
	var e *guidance.EditError
	if errors.As(err, &e) {
		return &Refusal{Code: Code(e.Code), message: err.Error(), cause: err}
	}
	return err
}

// ReadProjectGuidance reads raw and effective guidance for an explicit registered
// Project name or uid: ref. It never writes Registry or guidance state.
func (c *Client) ReadProjectGuidance(ctx context.Context, projectRef string) (core.ProjectGuidanceFile, error) {
	v, err := c.guidance().ReadProject(ctx, projectRef)
	return core.ProjectGuidanceFile(v), resourceRefusal(guidanceRefusal(err))
}

// ValidateProjectGuidance checks a candidate without filesystem writes. Its
// revision can replace invalid existing UTF-8 bytes with a valid candidate.
func (c *Client) ValidateProjectGuidance(ctx context.Context, projectRef string, content []byte) (core.ProjectGuidanceValidation, error) {
	v, err := c.guidance().ValidateProject(ctx, projectRef, content)
	return core.ProjectGuidanceValidation(v), resourceRefusal(guidanceRefusal(err))
}

// WriteProjectGuidance requires a matching target revision and uses the shared
// writer lock and atomic store. Cancellation before admission is preserved;
// after admission the atomic write completes. External editors can ignore locks.
func (c *Client) WriteProjectGuidance(ctx context.Context, projectRef string, content []byte, revision string) (core.ProjectGuidanceFile, error) {
	v, err := c.guidance().WriteProject(ctx, projectRef, content, revision)
	return core.ProjectGuidanceFile(v), resourceRefusal(guidanceRefusal(err))
}

// DeleteProjectGuidance removes only the explicit Project's file, retaining all
// global guidance settings. Deleting an absent file with its revision succeeds.
// Cancellation while waiting for writer admission preserves the context cause.
func (c *Client) DeleteProjectGuidance(ctx context.Context, projectRef, revision string) (core.ProjectGuidanceFile, error) {
	v, err := c.guidance().DeleteProject(ctx, projectRef, revision)
	return core.ProjectGuidanceFile(v), resourceRefusal(guidanceRefusal(err))
}
