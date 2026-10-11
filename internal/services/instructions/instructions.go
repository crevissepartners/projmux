// Package instructions executes stored instruction operations for CLI and SDK.
package instructions

import (
	"context"
	"fmt"

	"github.com/crevissepartners/projmux/internal/config"
	"github.com/crevissepartners/projmux/internal/core/persona"
	"github.com/crevissepartners/projmux/internal/core/profile"
)

// Service resolves paths per operation. It owns no process or global state.
type Service struct{ Paths func() (config.Paths, error) }

func (s Service) store() (persona.Store, error) {
	paths, err := s.Paths()
	if err != nil {
		return persona.Store{}, err
	}
	return persona.NewDefaultStore(paths), nil
}

func (s Service) List(ctx context.Context) ([]persona.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store, err := s.store()
	if err != nil {
		return nil, err
	}
	return store.List()
}

func (s Service) Read(ctx context.Context, name string) (persona.Persona, error) {
	if err := ctx.Err(); err != nil {
		return persona.Persona{}, err
	}
	store, err := s.store()
	if err != nil {
		return persona.Persona{}, err
	}
	return store.Load(name)
}

func (s Service) Write(ctx context.Context, name string, content []byte) (persona.Entry, error) {
	if err := ctx.Err(); err != nil {
		return persona.Entry{}, err
	}
	store, err := s.store()
	if err != nil {
		return persona.Entry{}, err
	}
	// Admission cancellation precedes the atomic store operation. Once admitted,
	// finish the write: cancellation cannot leave a partial file.
	if err := ctx.Err(); err != nil {
		return persona.Entry{}, err
	}
	return store.Write(name, content)
}

// InUseError identifies profiles which would be invalidated by deletion.
type InUseError struct {
	Name  string
	Users []string
}

func (e *InUseError) Error() string {
	return fmt.Sprintf("%s: instructions %q are used by profiles %q", profile.ReasonInstructionsInUse, e.Name, e.Users)
}

// UsageCheckError keeps failure to inspect profiles separate from store errors.
type UsageCheckError struct{ Err error }

func (e *UsageCheckError) Error() string { return e.Err.Error() }
func (e *UsageCheckError) Unwrap() error { return e.Err }

func (s Service) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store, err := s.store()
	if err != nil {
		return err
	}
	// Preserve the CLI rule: a missing or unreadable instruction bypasses the
	// profile check, leaving Delete to report the original store result.
	if _, err := store.Load(name); err == nil {
		paths, err := s.Paths()
		if err != nil {
			return &UsageCheckError{Err: err}
		}
		users, err := profile.NewDefaultStore(paths).ProfilesUsingInstructions(name)
		if err != nil {
			return &UsageCheckError{Err: err}
		}
		if len(users) != 0 {
			return &InUseError{Name: name, Users: users}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return store.Delete(name)
}
