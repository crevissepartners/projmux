// Package agentguidance shares global and explicit Project guidance operations between CLI and SDK.
package agentguidance

import (
	"context"

	"github.com/crevissepartners/projmux/internal/config"
	guidance "github.com/crevissepartners/projmux/internal/core/agentguidance"
)

type Service struct {
	Paths         func() (config.Paths, error)
	RegistryPaths func() (config.Paths, error)
}

func (s Service) store(ctx context.Context) (guidance.Store, error) {
	if err := ctx.Err(); err != nil {
		return guidance.Store{}, err
	}
	p, err := s.Paths()
	return guidance.NewDefaultStore(p), err
}
func (s Service) Read(ctx context.Context) (guidance.File, error) {
	v, err := s.store(ctx)
	if err != nil {
		return guidance.File{}, err
	}
	return v.ReadFile(ctx)
}
func (s Service) Validate(ctx context.Context, b []byte) (guidance.Validation, error) {
	v, err := s.store(ctx)
	if err != nil {
		return guidance.Validation{}, err
	}
	return v.ValidateFile(ctx, b)
}
func (s Service) Write(ctx context.Context, b []byte, revision string) (guidance.File, error) {
	v, err := s.store(ctx)
	if err != nil {
		return guidance.File{}, err
	}
	return v.WriteFile(ctx, b, revision)
}
func (s Service) Reset(ctx context.Context, revision string) (guidance.File, error) {
	v, err := s.store(ctx)
	if err != nil {
		return guidance.File{}, err
	}
	return v.ResetFile(ctx, revision)
}
func (s Service) Inspect(ctx context.Context, process bool) (guidance.Inspection, error) {
	v, err := s.store(ctx)
	if err != nil {
		return guidance.Inspection{}, err
	}
	return v.InspectContext(ctx, process)
}
func (s Service) SetEnabled(ctx context.Context, fragment string, enabled bool) error {
	v, err := s.store(ctx)
	if err != nil {
		return err
	}
	return v.SetEnabledContext(ctx, fragment, enabled)
}
