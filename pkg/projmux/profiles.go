package projmux

import (
	"context"
	"errors"
	"fmt"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/profile"
	"github.com/crevissepartners/projmux/internal/services/profiles"
	"github.com/crevissepartners/projmux/pkg/projmux/core"
)

const (
	CodeProfileNameInvalid          Code = "profile-name-invalid"
	CodeProfileNameReserved         Code = "profile-name-reserved"
	CodeProfileNotFound             Code = "profile-not-found"
	CodeProfileTooLarge             Code = "profile-too-large"
	CodeProfileSyntaxInvalid        Code = "profile-syntax-invalid"
	CodeProfileKeyUnknown           Code = "profile-key-unknown"
	CodeProfileTableUnknown         Code = "profile-table-unknown"
	CodeProfileValueInvalid         Code = "profile-value-invalid"
	CodeProfileInstructionsNotFound Code = "profile-instructions-not-found"
	CodeProfileRoleDuplicate        Code = "profile-role-duplicate"
	CodeProfileRoleClaimed          Code = "profile-role-claimed"
	CodeProfileProviderUnknown      Code = "profile-provider-unknown"
	CodeProfileScopeChanged         Code = "profile-scope-changed"
	CodeProfileConfirmationRequired Code = "profile-confirmation-required"
)

func (c *Client) profileService() profiles.Service {
	return profiles.Service{Paths: c.instructions.Paths, RegistryPaths: func() (config.Paths, error) { return c.paths, c.pathsErr }}
}

// ListProfiles lists all profiles, or global and scoped profiles visible to one
// exact Project ref (name or uid:). It never infers the active Project.
func (c *Client) ListProfiles(ctx context.Context, projectRef string) ([]core.ProfileInfo, error) {
	entries, err := c.profileService().List(ctx, projectRef)
	if err != nil {
		return nil, profileRefusal(err)
	}
	out := make([]core.ProfileInfo, len(entries))
	for i, e := range entries {
		out[i] = core.ProfileInfo(e)
	}
	return out, nil
}

// ReadProfile returns exact stored bytes, limited to 64 KiB, without parsing.
func (c *Client) ReadProfile(ctx context.Context, name string) (core.ProfileContent, error) {
	p, err := c.profileService().Read(ctx, name)
	if err != nil {
		return core.ProfileContent{}, profileRefusal(err)
	}
	return core.ProfileContent{Name: p.Name, Source: p.Source, Content: p.Content, Digest: p.Digest}, nil
}

// WriteProfile validates and atomically stores at most 64 KiB as a 0600 file
// in a 0700 profiles directory. Pre-admission cancellation writes nothing;
// once admitted the atomic operation finishes. Concurrent edits are not serialized.
func (c *Client) WriteProfile(ctx context.Context, name string, content []byte) (core.ProfileWriteResult, error) {
	p, err := c.profileService().Write(ctx, name, content)
	if err != nil {
		return core.ProfileWriteResult{}, profileRefusal(err)
	}
	return core.ProfileWriteResult{Name: p.Name, Path: p.Path, Digest: p.Digest, Size: p.Size}, nil
}

// DeleteProfile requires confirm=true, equivalent to CLI --yes. It does not
// check running Agents or change their already captured profiles.
func (c *Client) DeleteProfile(ctx context.Context, name string, confirm bool) error {
	return profileRefusal(c.profileService().Delete(ctx, name, confirm))
}

func profileRefusal(err error) error {
	if err == nil {
		return nil
	}
	var p *profile.Error
	if errors.As(err, &p) {
		return &Refusal{Code: Code(profile.ReasonOf(err)), Name: p.Name, message: err.Error()}
	}
	var confirmation *profiles.ConfirmationError
	if errors.As(err, &confirmation) {
		return &Refusal{Code: CodeProfileConfirmationRequired, Name: confirmation.Name, message: fmt.Sprintf("DeleteProfile confirm must be true for %q; nothing was deleted", confirmation.Name)}
	}
	var project *profiles.ProjectError
	if errors.As(err, &project) {
		return resourceRefusal(selectorInputError(project.Err, "ListProfiles projectRef"))
	}
	return err
}

// ParseProfile validates UTF-8, the 64 KiB limit and the current profile
// vocabulary without reading storage or the Registry. No partial spec is returned
// on failure. Named instructions need not exist for pure parsing.
func (c *Client) ParseProfile(ctx context.Context, content []byte) (core.ProfileSpec, error) {
	spec, err := c.profileService().Parse(ctx, content)
	if err != nil {
		return core.ProfileSpec{}, profileRefusal(err)
	}
	return profileSpec(spec), nil
}

// PreviewProfile applies WriteProfile's name, size, parse, instructions, role
// and scope checks without writing files, migrations, locks or the Registry.
// It reserves nothing: a later WriteProfile checks the current store again.
func (c *Client) PreviewProfile(ctx context.Context, name string, content []byte) (core.ProfilePreview, error) {
	p, err := c.profileService().Preview(ctx, name, content)
	if err != nil {
		return core.ProfilePreview{}, profileRefusal(err)
	}
	return core.ProfilePreview{Name: p.Name, Digest: p.Digest, Size: p.Size, Spec: profileSpec(p.Spec), DryRun: p.DryRun}, nil
}

func profileSpec(s profiles.Spec) core.ProfileSpec {
	return core.ProfileSpec{Project: s.Project, Provider: s.Provider, Instructions: s.Instructions,
		Model: s.Model, Effort: s.Effort, Roles: s.Roles, Permissions: core.ProfilePermissions(s.Permissions)}
}
