package profiles

import (
	"bytes"
	"context"
	"errors"

	"github.com/crevissepartners/projmux/internal/core/profile"
)

// Spec is the typed, vocabulary-checked profile shared by CLI and SDK.
type Spec struct {
	Project      string      `json:"project"`
	Provider     string      `json:"provider"`
	Instructions string      `json:"instructions"`
	Model        string      `json:"model"`
	Effort       string      `json:"effort"`
	Roles        []string    `json:"roles"`
	Permissions  Permissions `json:"permissions"`
}

// Permissions describes the profile permissions, with non-null lists.
type Permissions struct {
	Sandbox  string   `json:"sandbox"`
	Approval string   `json:"approval"`
	Allow    []string `json:"allow"`
	Deny     []string `json:"deny"`
}

// Preview is a checked candidate, without a write or reservation.
type Preview struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
	Spec   Spec   `json:"spec"`
	DryRun bool   `json:"dryRun"`
}

func projectSpec(spec profile.Spec) Spec {
	return Spec{Project: spec.Project, Provider: spec.Provider, Instructions: spec.Instructions,
		Model: spec.Model, Effort: spec.Effort, Roles: append([]string{}, spec.Roles...),
		Permissions: Permissions{Sandbox: spec.Permissions.Sandbox, Approval: spec.Permissions.Approval,
			Allow: append([]string{}, spec.Permissions.Allow...), Deny: append([]string{}, spec.Permissions.Deny...)}}
}

// Parse validates only the supplied bytes. It never resolves storage paths.
func (s Service) Parse(ctx context.Context, content []byte) (Spec, error) {
	if err := ctx.Err(); err != nil {
		return Spec{}, err
	}
	content, err := profile.ReadLimited(bytes.NewReader(content))
	if err != nil {
		return Spec{}, err
	}
	spec, err := profile.Parse(content)
	if err != nil {
		return Spec{}, err
	}
	if err := ctx.Err(); err != nil {
		return Spec{}, err
	}
	return projectSpec(spec), nil
}

// Preview uses the same admission checks as Write, without invoking Write.
func (s Service) Preview(ctx context.Context, name string, content []byte) (Preview, error) {
	if err := ctx.Err(); err != nil {
		return Preview{}, err
	}
	if err := profile.ValidateName(name); err != nil {
		return Preview{}, err
	}
	content, err := profile.ReadLimited(bytes.NewReader(content))
	if err != nil {
		var refusal *profile.Error
		if errors.As(err, &refusal) {
			refusal.Name = name
		}
		return Preview{}, err
	}
	store, err := s.store(false)
	if err != nil {
		return Preview{}, err
	}
	if err := ctx.Err(); err != nil {
		return Preview{}, err
	}
	spec, err := store.Preview(name, content)
	if err != nil {
		return Preview{}, err
	}
	if err := ctx.Err(); err != nil {
		return Preview{}, err
	}
	return Preview{Name: name, Digest: profile.Digest(content), Size: int64(len(content)), Spec: projectSpec(spec), DryRun: true}, nil
}
